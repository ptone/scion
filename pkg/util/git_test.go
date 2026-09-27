// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package util

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func setupGitRepo(t *testing.T) string {
	dir := t.TempDir()

	// Initialize git repo
	cmd := exec.Command("git", "init")
	cmd.Dir = dir
	if err := cmd.Run(); err != nil {
		t.Fatalf("failed to init git repo: %v", err)
	}

	// Config user for commits
	configCmds := [][]string{
		{"config", "user.email", "you@example.com"},
		{"config", "user.name", "Your Name"},
		{"commit", "--allow-empty", "-m", "root commit"},
	}

	for _, args := range configCmds {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if err := cmd.Run(); err != nil {
			t.Fatalf("failed to run git %v: %v", args, err)
		}
	}

	return dir
}

func TestGitUtils(t *testing.T) {
	// Clear container context so worktree tests can create worktrees
	t.Setenv("SCION_HOST_UID", "")

	// Need to be inside the repo for most tests
	repoDir := setupGitRepo(t)

	// Save current working dir to restore later
	originalWd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(originalWd) }()

	if err := os.Chdir(repoDir); err != nil {
		t.Fatal(err)
	}

	t.Run("IsGitRepo", func(t *testing.T) {
		if !IsGitRepo() {
			t.Error("expected true, got false")
		}
	})

	t.Run("RepoRoot", func(t *testing.T) {
		root, err := RepoRoot()
		if err != nil {
			t.Errorf("RepoRoot failed: %v", err)
		}
		// RepoRoot usually returns path with symlinks resolved, matching t.TempDir behavior
		// On macOS t.TempDir might be in /var/folders/... which is a symlink to /private/var/folders/...
		// We resolve both to compare safely.
		evalRoot, _ := filepath.EvalSymlinks(root)
		evalRepoDir, _ := filepath.EvalSymlinks(repoDir)

		if evalRoot != evalRepoDir {
			t.Errorf("expected root %q, got %q", evalRepoDir, evalRoot)
		}
	})

	t.Run("IsIgnored", func(t *testing.T) {
		ignoreFile := filepath.Join(repoDir, ".gitignore")
		if err := os.WriteFile(ignoreFile, []byte("ignored.txt"), 0644); err != nil {
			t.Fatal(err)
		}

		if !IsIgnored(repoDir, "ignored.txt") {
			t.Error("expected ignored.txt to be ignored")
		}

		if IsIgnored(repoDir, "not-ignored.txt") {
			t.Error("expected not-ignored.txt to NOT be ignored")
		}
	})

	t.Run("Worktrees", func(t *testing.T) {
		worktreePath := filepath.Join(repoDir, "wt-test")
		branchName := "test-branch"

		// Create
		if err := CreateWorktree(worktreePath, branchName); err != nil {
			t.Fatalf("CreateWorktree failed: %v", err)
		}

		if _, err := os.Stat(worktreePath); os.IsNotExist(err) {
			t.Errorf("worktree dir does not exist")
		}

		// Remove
		if _, err := RemoveWorktree(worktreePath, false); err != nil {
			t.Fatalf("RemoveWorktree failed: %v", err)
		}
		// Wait/Check? git worktree remove deletes the directory usually.
		if _, err := os.Stat(worktreePath); !os.IsNotExist(err) {
			t.Errorf("worktree dir still exists after removal")
		}

		// Test PruneWorktrees
		prunePath := filepath.Join(repoDir, "prune-test")
		pruneBranch := "prune-branch"
		if err := CreateWorktree(prunePath, pruneBranch); err != nil {
			t.Fatalf("CreateWorktree for prune failed: %v", err)
		}
		// Manually remove directory to simulate stale worktree
		if err := os.RemoveAll(prunePath); err != nil {
			t.Fatalf("Failed to remove prune path: %v", err)
		}
		// Prune
		if err := PruneWorktrees(); err != nil {
			t.Fatalf("PruneWorktrees failed: %v", err)
		}
		// Verify we can create it again (if prune failed, this might fail with 'already exists')
		if err := CreateWorktree(prunePath, pruneBranch); err != nil {
			t.Errorf("Failed to recreate worktree after prune: %v", err)
		}
		// Clean up
		_, _ = RemoveWorktree(prunePath, true)
	})

	t.Run("PruneWorktreesIn", func(t *testing.T) {
		prunePath := filepath.Join(repoDir, "prune-in-test")
		pruneBranch := "prune-in-branch"
		if err := CreateWorktree(prunePath, pruneBranch); err != nil {
			t.Fatalf("CreateWorktree failed: %v", err)
		}
		// Manually remove directory to simulate stale worktree
		if err := os.RemoveAll(prunePath); err != nil {
			t.Fatalf("Failed to remove prune path: %v", err)
		}

		// PruneWorktreesIn should work even when CWD is outside the repo
		outsideDir := t.TempDir()
		prevWd, _ := os.Getwd()
		if err := os.Chdir(outsideDir); err != nil {
			t.Fatal(err)
		}
		defer func() { _ = os.Chdir(prevWd) }()

		if err := PruneWorktreesIn(repoDir); err != nil {
			t.Fatalf("PruneWorktreesIn failed: %v", err)
		}

		// Verify we can create the worktree again (prune cleared the stale record)
		if err := os.Chdir(prevWd); err != nil {
			t.Fatal(err)
		}
		if err := CreateWorktree(prunePath, pruneBranch); err != nil {
			t.Errorf("Failed to recreate worktree after PruneWorktreesIn: %v", err)
		}
		// Clean up
		_, _ = RemoveWorktree(prunePath, true)
	})

	t.Run("DeleteBranchIn", func(t *testing.T) {
		// Create a branch via worktree, then remove the worktree without deleting the branch
		wtPath := filepath.Join(repoDir, "branch-del-test")
		branch := "delete-me-branch"
		if err := CreateWorktree(wtPath, branch); err != nil {
			t.Fatalf("CreateWorktree failed: %v", err)
		}
		if _, err := RemoveWorktree(wtPath, false); err != nil {
			t.Fatalf("RemoveWorktree failed: %v", err)
		}

		// Branch should still exist
		if !BranchExists(branch) {
			t.Fatal("expected branch to still exist after RemoveWorktree(deleteBranch=false)")
		}

		// DeleteBranchIn should remove it
		if !DeleteBranchIn(repoDir, branch) {
			t.Error("DeleteBranchIn returned false, expected true")
		}

		// Branch should be gone
		if BranchExists(branch) {
			t.Error("expected branch to be deleted after DeleteBranchIn")
		}

		// Deleting a non-existent branch should return false
		if DeleteBranchIn(repoDir, "no-such-branch") {
			t.Error("DeleteBranchIn returned true for non-existent branch")
		}
	})

	t.Run("FindWorktreeByBranch", func(t *testing.T) {
		wtPath := filepath.Join(repoDir, "wt-find")
		branch := "find-branch"

		if err := CreateWorktree(wtPath, branch); err != nil {
			t.Fatalf("setup failed: %v", err)
		}

		foundPath, err := FindWorktreeByBranch(branch)
		if err != nil {
			t.Errorf("FindWorktreeByBranch failed: %v", err)
		}

		// Normalize paths for comparison (resolve symlinks)
		evalFound, _ := filepath.EvalSymlinks(foundPath)
		evalWt, _ := filepath.EvalSymlinks(wtPath)

		if evalFound != evalWt {
			t.Errorf("expected %q, got %q", evalWt, evalFound)
		}

		// Clean up
		_, _ = RemoveWorktree(wtPath, true)
	})

	t.Run("RemoveWorktreeWithBranch", func(t *testing.T) {
		wtPath := filepath.Join(repoDir, "wt-rm-branch")
		branch := "rm-branch-test"

		if err := CreateWorktree(wtPath, branch); err != nil {
			t.Fatalf("CreateWorktree failed: %v", err)
		}

		deleted, err := RemoveWorktree(wtPath, true)
		if err != nil {
			t.Fatalf("RemoveWorktree failed: %v", err)
		}
		if !deleted {
			t.Error("expected branch to be deleted")
		}
		if BranchExists(branch) {
			t.Error("branch still exists after RemoveWorktree with deleteBranch=true")
		}
	})

	t.Run("CompareGitVersion", func(t *testing.T) {
		tests := []struct {
			version string
			major   int
			minor   int
			wantErr bool
		}{
			{"2.47.0", 2, 47, false},
			{"2.48.0", 2, 47, false},
			{"3.0.0", 2, 47, false},
			{"2.46.9", 2, 47, true},
			{"1.9.0", 2, 47, true},
			{"2.47.1.windows.1", 2, 47, false},
			{"invalid", 2, 47, true},
		}

		for _, tt := range tests {
			err := CompareGitVersion(tt.version, tt.major, tt.minor)
			if (err != nil) != tt.wantErr {
				t.Errorf("CompareGitVersion(%q, %d, %d) error = %v, wantErr %v", tt.version, tt.major, tt.minor, err, tt.wantErr)
			}
		}
	})

	t.Run("NormalizeGitRemote", func(t *testing.T) {
		tests := []struct {
			remote string
			want   string
		}{
			{"https://github.com/GoogleCloudPlatform/scion.git", "github.com/googlecloudplatform/scion"},
			{"http://github.com/GoogleCloudPlatform/scion.git", "github.com/googlecloudplatform/scion"},
			{"git@github.com:GoogleCloudPlatform/scion.git", "github.com/googlecloudplatform/scion"},
			{"github.com/GoogleCloudPlatform/scion.git", "github.com/googlecloudplatform/scion"},
			{"git@github.com:GoogleCloudPlatform/scion", "github.com/googlecloudplatform/scion"},
			{"HTTPS://github.com/GoogleCloudPlatform/scion.GIT", "github.com/googlecloudplatform/scion"},
			{"", ""},
		}

		for _, tt := range tests {
			got := NormalizeGitRemote(tt.remote)
			if got != tt.want {
				t.Errorf("NormalizeGitRemote(%q) = %q, want %q", tt.remote, got, tt.want)
			}
		}
	})
}

func TestCreateWorktree_FromWorktreeSucceeds(t *testing.T) {
	// Clear container context so worktree creation is allowed
	t.Setenv("SCION_HOST_UID", "")

	// Creating a worktree from within an existing worktree is a legitimate
	// operation (git supports it natively via the common git dir). This test
	// verifies that CreateWorktree resolves the main repo root correctly and
	// creates the sibling worktree.
	mainRepo := setupGitRepo(t)

	originalWd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(originalWd) }()
	if err := os.Chdir(mainRepo); err != nil {
		t.Fatal(err)
	}

	// Create a worktree from the main repo
	wtPath := filepath.Join(mainRepo, "child-wt")
	if err := CreateWorktree(wtPath, "child-branch"); err != nil {
		t.Fatalf("failed to create initial worktree: %v", err)
	}

	// Change into the worktree and create another worktree from there.
	// This should succeed — the new worktree is a peer managed by the main repo.
	if err := os.Chdir(wtPath); err != nil {
		t.Fatal(err)
	}
	siblingPath := filepath.Join(mainRepo, "sibling-wt")
	if err := CreateWorktree(siblingPath, "sibling-branch"); err != nil {
		t.Fatalf("expected worktree creation from within a worktree to succeed, got: %v", err)
	}

	// Clean up
	_, _ = RemoveWorktree(siblingPath, true)
	_, _ = RemoveWorktree(wtPath, true)
}

func TestCreateWorktree_RejectsInsideContainer(t *testing.T) {
	// When SCION_HOST_UID is set (agent container), worktree creation should
	// be refused to prevent path-identity mismatches from container mounts.
	t.Setenv("SCION_HOST_UID", "1000")

	mainRepo := setupGitRepo(t)

	wtPath := filepath.Join(mainRepo, "container-wt")
	err := CreateWorktree(wtPath, "container-branch")
	if err == nil {
		t.Fatal("expected error creating worktree inside container context")
	}
	if !strings.Contains(err.Error(), "SCION_HOST_UID") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestPruneWorktrees_SkipsInsideContainer(t *testing.T) {
	// When SCION_HOST_UID is set (agent container), pruning should be a no-op
	// to prevent destroying sibling worktree metadata that appears stale from
	// the container's mount layout.
	t.Setenv("SCION_HOST_UID", "1000")

	// Both prune functions should return nil without running git at all.
	if err := PruneWorktrees(); err != nil {
		t.Errorf("PruneWorktrees should no-op inside container, got: %v", err)
	}
	if err := PruneWorktreesIn("/nonexistent/path"); err != nil {
		t.Errorf("PruneWorktreesIn should no-op inside container, got: %v", err)
	}
}

func TestIsGitURL(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		// Valid URLs
		{"https://github.com/org/repo.git", true},
		{"https://github.com/org/repo", true},
		{"http://github.com/org/repo.git", true},
		{"git@github.com:org/repo.git", true},
		{"git@github.com:org/repo", true},
		{"ssh://git@github.com/org/repo", true},
		{"git://github.com/org/repo.git", true},
		{"HTTPS://GITHUB.COM/org/repo.git", true},
		{"git@gitlab.com:group/subgroup/repo.git", true},

		// Invalid inputs
		{"", false},
		{"/local/path/to/repo", false},
		{"./relative/path", false},
		{"../parent/path", false},
		{"github.com", false},          // bare hostname, no scheme recognized
		{"git@github.com:", false},     // no path after colon
		{"git@github.com:repo", false}, // no '/' in path
		{"https://github.com/", false}, // path is just '/'
		{"https://github.com", false},  // no path
	}

	for _, tt := range tests {
		got := IsGitURL(tt.input)
		if got != tt.want {
			t.Errorf("IsGitURL(%q) = %v, want %v", tt.input, got, tt.want)
		}
	}
}

func TestToHTTPSCloneURL(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		// SSH shorthand → HTTPS
		{"git@github.com:org/repo.git", "https://github.com/org/repo.git"},
		{"git@github.com:org/repo", "https://github.com/org/repo.git"},

		// ssh:// → HTTPS
		{"ssh://git@github.com/org/repo", "https://github.com/org/repo.git"},
		{"ssh://git@github.com/org/repo.git", "https://github.com/org/repo.git"},

		// HTTPS passthrough
		{"https://github.com/org/repo.git", "https://github.com/org/repo.git"},
		{"https://github.com/org/repo", "https://github.com/org/repo.git"},

		// git:// → HTTPS
		{"git://github.com/org/repo.git", "https://github.com/org/repo.git"},

		// http:// → HTTPS
		{"http://github.com/org/repo.git", "https://github.com/org/repo.git"},

		// Azure DevOps — must NOT append .git
		{"https://dev.azure.com/org/project/_git/repo", "https://dev.azure.com/org/project/_git/repo"},
		// ADO with erroneous .git suffix — must be stripped
		{"https://dev.azure.com/org/project/_git/repo.git", "https://dev.azure.com/org/project/_git/repo"},
		// ADO via visualstudio.com
		{"https://myorg.visualstudio.com/project/_git/repo", "https://myorg.visualstudio.com/project/_git/repo"},

		// Empty
		{"", ""},
	}

	for _, tt := range tests {
		got := ToHTTPSCloneURL(tt.input)
		if got != tt.want {
			t.Errorf("ToHTTPSCloneURL(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestExtractOrgRepo(t *testing.T) {
	tests := []struct {
		input    string
		wantOrg  string
		wantRepo string
	}{
		{"https://github.com/acme/widgets.git", "acme", "widgets"},
		{"git@github.com:acme/widgets.git", "acme", "widgets"},
		{"ssh://git@github.com/acme/widgets", "acme", "widgets"},
		{"https://github.com/Acme/Widgets.git", "acme", "widgets"},
		{"git://github.com/org/repo.git", "org", "repo"},
		{"", "", ""},
	}

	for _, tt := range tests {
		org, repo := ExtractOrgRepo(tt.input)
		if org != tt.wantOrg || repo != tt.wantRepo {
			t.Errorf("ExtractOrgRepo(%q) = (%q, %q), want (%q, %q)", tt.input, org, repo, tt.wantOrg, tt.wantRepo)
		}
	}
}

func TestNormalizeGitRemote(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"empty", "", ""},
		{"https", "https://github.com/org/repo.git", "github.com/org/repo"},
		{"ssh shorthand", "git@github.com:org/repo.git", "github.com/org/repo"},
		{"ssh scheme", "ssh://git@github.com/org/repo.git", "github.com/org/repo"},
		{"git scheme", "git://github.com/org/repo.git", "github.com/org/repo"},
		{"http", "http://github.com/org/repo.git", "github.com/org/repo"},
		{"https no .git", "https://github.com/org/repo", "github.com/org/repo"},
		{"https token auth", "https://x-access-token:ghp_abc123@github.com/org/repo.git", "github.com/org/repo"},
		{"https oauth", "https://user:x-oauth-basic@github.com/org/repo.git", "github.com/org/repo"},
		{"https user only", "https://user@github.com/org/repo.git", "github.com/org/repo"},
		{"uppercase host", "https://GitHub.COM/org/repo.git", "github.com/org/repo"},
		{"https trailing slash", "https://github.com/org/repo/", "github.com/org/repo"},
		{"https trailing slash with .git", "https://github.com/org/repo.git/", "github.com/org/repo"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := NormalizeGitRemote(tt.input)
			if got != tt.want {
				t.Errorf("NormalizeGitRemote(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestNormalizeGitRemote_CrossProtocolConsistency(t *testing.T) {
	// All of these refer to the same repository and must produce the same normalized form.
	// (HashProjectID is retained for deterministic identifiers.)
	variants := []string{
		"git@github.com:ptone/gamegame.git",
		"https://github.com/ptone/gamegame.git",
		"ssh://git@github.com/ptone/gamegame.git",
		"https://x-access-token:TOKEN@github.com/ptone/gamegame.git",
		"git://github.com/ptone/gamegame.git",
		"https://github.com/ptone/gamegame/",
		"https://github.com/ptone/gamegame",
	}

	want := "github.com/ptone/gamegame"
	for _, url := range variants {
		got := NormalizeGitRemote(url)
		if got != want {
			t.Errorf("NormalizeGitRemote(%q) = %q, want %q", url, got, want)
		}
	}

	// All should produce the same deterministic hash
	ids := make(map[string]bool)
	for _, url := range variants {
		ids[HashProjectID(NormalizeGitRemote(url))] = true
	}
	if len(ids) != 1 {
		t.Errorf("expected all URL variants to produce the same deterministic hash, got %d distinct IDs", len(ids))
	}
}

func TestHashProjectID(t *testing.T) {
	// Determinism: same input → same output
	id1 := HashProjectID("github.com/acme/widgets")
	id2 := HashProjectID("github.com/acme/widgets")
	if id1 != id2 {
		t.Errorf("HashProjectID not deterministic: %q != %q", id1, id2)
	}

	// Must be a valid UUID (36 chars, parseable)
	if len(id1) != 36 {
		t.Errorf("HashProjectID length = %d, want 36 (UUID format)", len(id1))
	}
	if _, err := uuid.Parse(id1); err != nil {
		t.Errorf("HashProjectID produced invalid UUID %q: %v", id1, err)
	}

	// Different inputs → different outputs
	id3 := HashProjectID("github.com/acme/gadgets")
	if id1 == id3 {
		t.Errorf("HashProjectID collision: %q == %q for different inputs", id1, id3)
	}

	// Branch qualifier produces different ID
	id4 := HashProjectID("github.com/acme/widgets@release/v2")
	if id1 == id4 {
		t.Errorf("HashProjectID collision with branch qualifier: %q == %q", id1, id4)
	}
}

func TestCloneSharedWorkspace(t *testing.T) {
	// Create a source repo to clone from (local path as "remote")
	sourceDir := setupGitRepo(t)

	// Add a file so the clone has content
	testFile := filepath.Join(sourceDir, "hello.txt")
	if err := os.WriteFile(testFile, []byte("hello world"), 0644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("git", "add", "hello.txt")
	cmd.Dir = sourceDir
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	cmd = exec.Command("git", "commit", "-m", "add hello")
	cmd.Dir = sourceDir
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}

	t.Run("SuccessfulClone", func(t *testing.T) {
		destDir := filepath.Join(t.TempDir(), "workspace")
		err := CloneSharedWorkspace(destDir, sourceDir, "", "")
		if err != nil {
			t.Fatalf("CloneSharedWorkspace failed: %v", err)
		}

		// Verify file exists
		content, err := os.ReadFile(filepath.Join(destDir, "hello.txt"))
		if err != nil {
			t.Fatal(err)
		}
		if string(content) != "hello world" {
			t.Errorf("unexpected content: %q", content)
		}

		// Verify git identity was configured
		cmd := exec.Command("git", "-C", destDir, "config", "user.name")
		output, err := cmd.Output()
		if err != nil {
			t.Fatal(err)
		}
		if got := strings.TrimSpace(string(output)); got != "Scion" {
			t.Errorf("expected user.name 'Scion', got %q", got)
		}

		cmd = exec.Command("git", "-C", destDir, "config", "user.email")
		output, err = cmd.Output()
		if err != nil {
			t.Fatal(err)
		}
		if got := strings.TrimSpace(string(output)); got != "agent@scion.dev" {
			t.Errorf("expected user.email 'agent@scion.dev', got %q", got)
		}
	})

	t.Run("CloneWithBranch", func(t *testing.T) {
		// Create a branch in the source repo
		cmd := exec.Command("git", "-C", sourceDir, "branch", "feature")
		if err := cmd.Run(); err != nil {
			t.Fatal(err)
		}

		destDir := filepath.Join(t.TempDir(), "workspace")
		err := CloneSharedWorkspace(destDir, sourceDir, "feature", "")
		if err != nil {
			t.Fatalf("CloneSharedWorkspace with branch failed: %v", err)
		}

		// Verify we're on the feature branch
		cmd = exec.Command("git", "-C", destDir, "branch", "--show-current")
		output, err := cmd.Output()
		if err != nil {
			t.Fatal(err)
		}
		if got := strings.TrimSpace(string(output)); got != "feature" {
			t.Errorf("expected branch 'feature', got %q", got)
		}
	})

	t.Run("CloneWithNonExistentBranch_FallsBack", func(t *testing.T) {
		destDir := filepath.Join(t.TempDir(), "workspace")
		err := CloneSharedWorkspace(destDir, sourceDir, "branch-does-not-exist", "")
		if err != nil {
			t.Fatalf("CloneSharedWorkspace should fall back to default branch: %v", err)
		}

		// Verify we're on the requested branch (created locally)
		cmd := exec.Command("git", "-C", destDir, "branch", "--show-current")
		output, err := cmd.Output()
		if err != nil {
			t.Fatal(err)
		}
		if got := strings.TrimSpace(string(output)); got != "branch-does-not-exist" {
			t.Errorf("expected branch 'branch-does-not-exist', got %q", got)
		}

		// Verify file content was cloned from the default branch
		content, err := os.ReadFile(filepath.Join(destDir, "hello.txt"))
		if err != nil {
			t.Fatal(err)
		}
		if string(content) != "hello world" {
			t.Errorf("unexpected content: %q", content)
		}

		// Verify git identity was still configured
		cmd = exec.Command("git", "-C", destDir, "config", "user.name")
		output, err = cmd.Output()
		if err != nil {
			t.Fatal(err)
		}
		if got := strings.TrimSpace(string(output)); got != "Scion" {
			t.Errorf("expected user.name 'Scion', got %q", got)
		}
	})

	t.Run("CloneFailure_BadURL", func(t *testing.T) {
		destDir := filepath.Join(t.TempDir(), "workspace")
		err := CloneSharedWorkspace(destDir, "/nonexistent/repo", "", "")
		if err == nil {
			t.Fatal("expected clone to fail with bad URL")
		}
		if !strings.Contains(err.Error(), "git clone failed") {
			t.Errorf("unexpected error: %v", err)
		}
	})

	t.Run("TokenSanitizedInRemote", func(t *testing.T) {
		// Clone with a fake token — since it's a local path, the token won't
		// actually be used for auth, but we can verify the remote URL is sanitized
		destDir := filepath.Join(t.TempDir(), "workspace")
		cloneURL := "https://example.com/org/repo.git"

		// This will fail because the URL is not a real repo, but we can test
		// sanitizeGitOutput separately
		err := CloneSharedWorkspace(destDir, cloneURL, "", "secret-token-123")
		if err != nil {
			// Expected failure — verify token is not in the error message
			if strings.Contains(err.Error(), "secret-token-123") {
				t.Error("token leaked in error message")
			}
		}
	})
}

func TestPullSharedWorkspace(t *testing.T) {
	// Create a source repo to pull from
	sourceDir := setupGitRepo(t)

	// Add initial content
	if err := os.WriteFile(filepath.Join(sourceDir, "initial.txt"), []byte("initial"), 0644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("git", "add", "initial.txt")
	cmd.Dir = sourceDir
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	cmd = exec.Command("git", "commit", "-m", "initial commit")
	cmd.Dir = sourceDir
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}

	// Clone from the source
	cloneDir := filepath.Join(t.TempDir(), "workspace")
	if err := CloneSharedWorkspace(cloneDir, sourceDir, "", ""); err != nil {
		t.Fatalf("Clone failed: %v", err)
	}

	t.Run("PullNoChanges", func(t *testing.T) {
		result, err := PullSharedWorkspace(cloneDir, "")
		if err != nil {
			t.Fatalf("Pull failed: %v", err)
		}
		if result.Updated {
			t.Error("expected Updated=false when already up to date")
		}
	})

	t.Run("PullNewChanges", func(t *testing.T) {
		// Add a new file to the source repo
		if err := os.WriteFile(filepath.Join(sourceDir, "new.txt"), []byte("new content"), 0644); err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command("git", "add", "new.txt")
		cmd.Dir = sourceDir
		if err := cmd.Run(); err != nil {
			t.Fatal(err)
		}
		cmd = exec.Command("git", "commit", "-m", "add new file")
		cmd.Dir = sourceDir
		if err := cmd.Run(); err != nil {
			t.Fatal(err)
		}

		result, err := PullSharedWorkspace(cloneDir, "")
		if err != nil {
			t.Fatalf("Pull failed: %v", err)
		}
		if !result.Updated {
			t.Error("expected Updated=true after pull with new commits")
		}
		if len(result.Commits) == 0 {
			t.Error("expected at least one commit in result")
		}
		if len(result.Commits) > 0 && result.Commits[0].Subject != "add new file" {
			t.Errorf("expected commit subject %q, got %q", "add new file", result.Commits[0].Subject)
		}

		// Verify the new file was pulled
		content, err := os.ReadFile(filepath.Join(cloneDir, "new.txt"))
		if err != nil {
			t.Fatal("new.txt should exist after pull")
		}
		if string(content) != "new content" {
			t.Errorf("unexpected content: %q", content)
		}
	})

	t.Run("PullFailure_NotARepo", func(t *testing.T) {
		notARepo := t.TempDir()
		_, err := PullSharedWorkspace(notARepo, "")
		if err == nil {
			t.Fatal("expected pull to fail for non-repo directory")
		}
		if !strings.Contains(err.Error(), "git pull failed") {
			t.Errorf("unexpected error: %v", err)
		}
	})
}

// gitC runs a git command against dir and fails the test on error, returning
// combined output for callers that want to inspect it.
func gitC(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v failed: %v (%s)", args, err, out)
	}
	return string(out)
}

// commitNewFile adds a file with the given content to dir and commits it,
// for use as "new upstream content" a subsequent pull should fast-forward
// onto.
func commitNewFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	gitC(t, dir, "add", name)
	gitC(t, dir, "commit", "-m", "add "+name)
}

// requireNoMarkers fails the test if any file exists under markerDir,
// reporting their names. Tests create markerDir empty and write hooks/
// filters that write into it; a non-empty markerDir after a pull means a
// landed hook or filter executed.
func requireNoMarkers(t *testing.T, markerDir string) {
	t.Helper()
	entries, _ := os.ReadDir(markerDir)
	var leftover []string
	for _, e := range entries {
		leftover = append(leftover, e.Name())
	}
	if len(leftover) != 0 {
		t.Errorf("a landed hook/filter executed; found marker files: %v", leftover)
	}
}

// TestFilterSyncedGitMetadata_HooksFilterConfig covers matrix cases (a)-(c): a
// sync-carried post-checkout/post-merge hook, a filter.*+info/attributes
// smudge driver, and core.fsmonitor/core.hooksPath in config. It proves none
// of them survive FilterSyncedGitMetadata in executable form, none execute
// on a subsequent host-side `git pull --ff-only`, and shared-workspace pull
// itself keeps working end to end. Each assertion fails if the
// FilterSyncedGitMetadata call is removed: the allowlist/config assertions
// fail because the landed files/keys would still be present verbatim, and
// the "no markers" assertion fails because a fast-forward `git pull` invokes
// `post-merge` (verified separately against an unfiltered clone).
func TestFilterSyncedGitMetadata_HooksFilterConfig(t *testing.T) {
	sourceDir := setupGitRepo(t)

	cloneDir := filepath.Join(t.TempDir(), "workspace")
	if err := CloneSharedWorkspace(cloneDir, sourceDir, "", ""); err != nil {
		t.Fatalf("Clone failed: %v", err)
	}

	markerDir := t.TempDir()
	gitDir := filepath.Join(cloneDir, ".git")

	// (a) Write a sync-carried post-checkout/post-merge hook.
	hooksDir := filepath.Join(gitDir, "hooks")
	if err := os.MkdirAll(hooksDir, 0755); err != nil {
		t.Fatal(err)
	}
	hookScript := "#!/bin/sh\necho ranmarker > " + filepath.Join(markerDir, "hook-ranmarker") + "\n"
	for _, name := range []string{"post-merge", "post-checkout"} {
		if err := os.WriteFile(filepath.Join(hooksDir, name), []byte(hookScript), 0755); err != nil {
			t.Fatal(err)
		}
	}

	// (b) Write a filter.*+info/attributes smudge driver.
	infoDir := filepath.Join(gitDir, "info")
	if err := os.MkdirAll(infoDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(infoDir, "attributes"), []byte("* filter=custom\n"), 0644); err != nil {
		t.Fatal(err)
	}
	// (c) Write execution-bearing config keys.
	landedConfig := [][]string{
		{"filter.custom.smudge", "sh -c 'echo ranmarker > " + filepath.Join(markerDir, "filter-ranmarker") + "'"},
		{"filter.custom.required", "true"},
		{"core.fsmonitor", "sh -c 'echo ranmarker > " + filepath.Join(markerDir, "fsmonitor-ranmarker") + "'"},
		{"core.hooksPath", "/tmp/custom-hooks"},
	}
	for _, kv := range landedConfig {
		gitC(t, cloneDir, "config", kv[0], kv[1])
	}

	if err := FilterSyncedGitMetadata(cloneDir, SyncedGitRemote{RemoteURL: sourceDir}); err != nil {
		t.Fatalf("FilterSyncedGitMetadata failed: %v", err)
	}

	// Allowlist assertions: hooks/ and info/ are gone entirely, not emptied.
	if _, err := os.Stat(hooksDir); !os.IsNotExist(err) {
		t.Errorf("expected hooks dir to be removed entirely, stat err: %v", err)
	}
	if _, err := os.Stat(infoDir); !os.IsNotExist(err) {
		t.Errorf("expected info dir to be removed entirely, stat err: %v", err)
	}
	configBytes, err := os.ReadFile(filepath.Join(gitDir, "config"))
	if err != nil {
		t.Fatalf("reading regenerated config: %v", err)
	}
	config := string(configBytes)
	for _, forbidden := range []string{"custom", "fsmonitor", "hooksPath", "ranmarker"} {
		if strings.Contains(config, forbidden) {
			t.Errorf("regenerated config unexpectedly contains %q:\n%s", forbidden, config)
		}
	}
	if !strings.Contains(config, sourceDir) {
		t.Errorf("regenerated config missing remote URL %q:\n%s", sourceDir, config)
	}

	// New upstream commit so the pull below exercises the fast-forward path
	// that would invoke post-merge.
	commitNewFile(t, sourceDir, "new.txt", "new content")

	result, err := PullSharedWorkspace(cloneDir, "")
	if err != nil {
		t.Fatalf("PullSharedWorkspace failed after neutralization: %v", err)
	}
	if !result.Updated {
		t.Error("expected Updated=true after pull with new commits")
	}
	content, err := os.ReadFile(filepath.Join(cloneDir, "new.txt"))
	if err != nil {
		t.Fatal("new.txt should exist after pull (objects/refs must still sync)")
	}
	if string(content) != "new content" {
		t.Errorf("unexpected content: %q", content)
	}
	requireNoMarkers(t, markerDir)
}

// TestFilterSyncedGitMetadata_ObjectsInfoRemoved is a dedicated,
// revert-checked test for the objects/info/{alternates,http-alternates}
// removal (P1-review R2): either file can point the object store at a
// different repository entirely — a cross-tenant read on a host where
// multiple projects' .git directories live side by side. Written as its own
// test (rather than folded into another test's fixture) so a no-op change
// to that removal fails only this test, not a broader one that happens to
// still pass for other reasons.
func TestFilterSyncedGitMetadata_ObjectsInfoRemoved(t *testing.T) {
	sourceDir := setupGitRepo(t)
	cloneDir := filepath.Join(t.TempDir(), "workspace")
	if err := CloneSharedWorkspace(cloneDir, sourceDir, "", ""); err != nil {
		t.Fatalf("Clone failed: %v", err)
	}
	objectsInfo := filepath.Join(cloneDir, ".git", "objects", "info")
	if err := os.MkdirAll(objectsInfo, 0755); err != nil {
		t.Fatal(err)
	}
	otherObjectStore := filepath.Join(t.TempDir(), "other-repo-objects")
	if err := os.WriteFile(filepath.Join(objectsInfo, "alternates"), []byte(otherObjectStore+"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(objectsInfo, "http-alternates"), []byte("https://example.invalid/other-repo.git/objects\n"), 0644); err != nil {
		t.Fatal(err)
	}

	if err := FilterSyncedGitMetadata(cloneDir, SyncedGitRemote{RemoteURL: sourceDir}); err != nil {
		t.Fatalf("FilterSyncedGitMetadata failed: %v", err)
	}

	if _, err := os.Stat(objectsInfo); !os.IsNotExist(err) {
		t.Errorf("expected objects/info to be removed entirely, stat err: %v", err)
	}
}

// TestLandSyncedGitWorkspace_FilterRunsUnconditionally covers the P1-review
// fix for the landing-failure case: a sync mirror is not all-or-nothing (an
// I/O error on one file does not stop it from copying the rest), so a
// landing whose sync step reports an error must still have its admin
// surface rebuilt. A landed hook must not survive just because the sync
// call that copied it also failed.
func TestLandSyncedGitWorkspace_FilterRunsUnconditionally(t *testing.T) {
	sourceDir := setupGitRepo(t)
	cloneDir := filepath.Join(t.TempDir(), "workspace")
	if err := CloneSharedWorkspace(cloneDir, sourceDir, "", ""); err != nil {
		t.Fatalf("Clone failed: %v", err)
	}
	hookPath := filepath.Join(cloneDir, ".git", "hooks", "post-merge")
	if err := os.WriteFile(hookPath, []byte("#!/bin/sh\necho ran\n"), 0755); err != nil {
		t.Fatal(err)
	}

	simulatedSyncErr := errors.New("simulated partial sync failure")
	err := LandSyncedGitWorkspace(cloneDir, SyncedGitRemote{RemoteURL: sourceDir}, func() error {
		// The provided sync step stands in for a mirror that copied the hook
		// above (already on disk, above) before reporting an error partway
		// through — exactly the rclone partial-copy-on-IO-error behavior
		// this fix targets.
		return simulatedSyncErr
	})
	if err == nil {
		t.Fatal("expected LandSyncedGitWorkspace to report the sync error")
	}
	if !errors.Is(err, simulatedSyncErr) {
		t.Errorf("expected the returned error to wrap the sync error, got: %v", err)
	}
	if _, statErr := os.Stat(hookPath); !os.IsNotExist(statErr) {
		t.Errorf("expected the hook to be removed by the rebuild even though sync reported an error, stat err: %v", statErr)
	}
}

// TestFilterSyncedGitMetadata_CommondirRedirection covers matrix case (d): a
// .git/commondir written into the landed .git repoints git's common dir
// (config, hooks, objects, refs) at a separate working-tree directory
// carrying its own config+hooks. A denylist that only strips .git/hooks and
// .git/config does not close this —
// git follows the redirect and never looks at the top-level .git/hooks at
// all. The allowlist rebuild closes it by deleting "commondir" itself (not
// on any allowlist), so no redirect exists to follow.
func TestFilterSyncedGitMetadata_CommondirRedirection(t *testing.T) {
	sourceDir := setupGitRepo(t)
	cloneDir := filepath.Join(t.TempDir(), "workspace")
	if err := CloneSharedWorkspace(cloneDir, sourceDir, "", ""); err != nil {
		t.Fatalf("Clone failed: %v", err)
	}
	gitDir := filepath.Join(cloneDir, ".git")

	markerDir := t.TempDir()
	marker := filepath.Join(markerDir, "commondir-ranmarker")

	// The redirected common dir must be a fully functional gitdir (its own
	// objects/refs/config matching the real repo) for git to actually honor
	// the redirect and complete a pull through it — otherwise git just fails
	// outright rather than exercising the redirect. Build it as a copy of
	// the pristine, cloned .git, then add a hook that records execution.
	customCommon := filepath.Join(t.TempDir(), "custom-common")
	if err := CopyDir(gitDir, customCommon); err != nil {
		t.Fatal(err)
	}
	customHooks := filepath.Join(customCommon, "hooks")
	if err := os.MkdirAll(customHooks, 0755); err != nil {
		t.Fatal(err)
	}
	hookScript := "#!/bin/sh\necho ranmarker > " + marker + "\n"
	if err := os.WriteFile(filepath.Join(customHooks, "post-merge"), []byte(hookScript), 0755); err != nil {
		t.Fatal(err)
	}

	relPath, err := filepath.Rel(gitDir, customCommon)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(gitDir, "commondir"), []byte(relPath+"\n"), 0644); err != nil {
		t.Fatal(err)
	}

	if err := FilterSyncedGitMetadata(cloneDir, SyncedGitRemote{RemoteURL: sourceDir}); err != nil {
		t.Fatalf("FilterSyncedGitMetadata failed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(gitDir, "commondir")); !os.IsNotExist(err) {
		t.Errorf("expected commondir to be removed, stat err: %v", err)
	}

	commitNewFile(t, sourceDir, "new.txt", "new content")
	if _, err := PullSharedWorkspace(cloneDir, ""); err != nil {
		t.Fatalf("PullSharedWorkspace failed after neutralization: %v", err)
	}
	requireNoMarkers(t, markerDir)
}

// TestFilterSyncedGitMetadata_SubmoduleGitdir covers matrix case (e):
// .git/modules/<sub>/{hooks,config} reached through a submodule gitlink,
// whose hooks/fsmonitor can run during a plain `git pull` (default
// fetch.recurseSubmodules=on-demand recurses into an already-initialized
// submodule using *its own* gitdir's config/hooks). The allowlist rebuild
// closes this by deleting "modules/" itself, so .git/modules/<sub> does not
// exist for git to recurse into.
func TestFilterSyncedGitMetadata_SubmoduleGitdir(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	subUpstream := setupGitRepo(t)
	superUpstream := setupGitRepo(t)
	gitC(t, superUpstream, "-c", "protocol.file.allow=always", "submodule", "add", "-q", subUpstream, "sub")
	gitC(t, superUpstream, "commit", "-q", "-m", "add submodule")

	cloneDir := filepath.Join(t.TempDir(), "workspace")
	cmd := exec.Command("git", "-c", "protocol.file.allow=always", "clone", "-q", "--recurse-submodules", superUpstream, cloneDir)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("clone --recurse-submodules failed: %v (%s)", err, out)
	}
	gitDir := filepath.Join(cloneDir, ".git")
	modulesDir := filepath.Join(gitDir, "modules", "sub")
	if _, err := os.Stat(modulesDir); err != nil {
		t.Fatalf("expected .git/modules/sub to exist after --recurse-submodules clone: %v", err)
	}

	// Advance the submodule so a subsequent superproject pull has a gitlink
	// bump to fetch.
	commitNewFile(t, subUpstream, "sub-new.txt", "sub new content")
	gitC(t, filepath.Join(superUpstream, "sub"), "-c", "protocol.file.allow=always", "pull", "-q", "origin",
		strings.TrimSpace(gitC(t, subUpstream, "branch", "--show-current")))
	gitC(t, superUpstream, "commit", "-qam", "bump submodule")

	markerDir := t.TempDir()
	marker := filepath.Join(markerDir, "submodule-ranmarker")
	for _, hook := range []string{"reference-transaction", "post-merge", "post-checkout"} {
		script := "#!/bin/sh\necho " + hook + " >> " + marker + "\n"
		if err := os.WriteFile(filepath.Join(modulesDir, "hooks", hook), []byte(script), 0755); err != nil {
			t.Fatal(err)
		}
	}
	fsmonitorLine := "\n[core]\n\tfsmonitor = \"echo FSM >> " + marker + "; false\"\n"
	f, err := os.OpenFile(filepath.Join(modulesDir, "config"), os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(fsmonitorLine); err != nil {
		t.Fatal(err)
	}
	f.Close()

	if err := FilterSyncedGitMetadata(cloneDir, SyncedGitRemote{RemoteURL: superUpstream}); err != nil {
		t.Fatalf("FilterSyncedGitMetadata failed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(gitDir, "modules")); !os.IsNotExist(err) {
		t.Errorf("expected modules/ to be removed entirely, stat err: %v", err)
	}

	// Default git pull (no --recurse-submodules override) must not resurrect
	// or execute anything from the deleted modules/sub. Unlike the other
	// cases, pull succeeding is not asserted here: CloneSharedWorkspace never
	// passes --recurse-submodules, so a legitimate hub-managed clone never
	// has .git/modules/<sub> populated in the first place (a submodule
	// gitlink with no initialized .git/modules entry is exactly the state a
	// normal, non-hardened shared-workspace clone would be in) — pull may
	// legitimately fail trying to update an uninitialized submodule. What
	// matters for this test is that nothing from the deleted modules/sub
	// executes either way.
	_, _ = PullSharedWorkspace(cloneDir, "")
	requireNoMarkers(t, markerDir)
}

// TestFilterSyncedGitMetadata_WorktreeSubmoduleGitdir covers the P1-review
// fix for the working-tree submodule case: a submodule whose gitdir lives in
// the working tree (<sub>/.git/ as a real directory — the "pre-absorb"
// layout, as opposed to the .git/modules/<sub> layout the previous test
// covers) is content outside the top-level .git directory the allowlist
// rebuild cannot reach at all.
// Closing this needs the regenerated config to stop a default pull from
// recursing into any submodule (fetch.recurseSubmodules=false,
// submodule.recurse=false in writeSyncedGitConfig's base keys) — the
// allowlist has no way to reach sub/.git in the first place.
func TestFilterSyncedGitMetadata_WorktreeSubmoduleGitdir(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	subUpstream := setupGitRepo(t)
	superUpstream := setupGitRepo(t)
	gitC(t, superUpstream, "-c", "protocol.file.allow=always", "submodule", "add", "-q", subUpstream, "sub")
	gitC(t, superUpstream, "commit", "-q", "-m", "add submodule")

	cloneDir := filepath.Join(t.TempDir(), "workspace")
	cmd := exec.Command("git", "-c", "protocol.file.allow=always", "clone", "-q", "--recurse-submodules", superUpstream, cloneDir)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("clone --recurse-submodules failed: %v (%s)", err, out)
	}
	gitDir := filepath.Join(cloneDir, ".git")
	subWorkDir := filepath.Join(cloneDir, "sub")
	subGitDir := filepath.Join(subWorkDir, ".git")

	// Relocate the submodule's gitdir into the working tree as a real
	// directory (the pre-absorb layout), replacing the gitfile the
	// --recurse-submodules clone created.
	if err := os.Remove(subGitDir); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(gitDir, "modules", "sub"), subGitDir); err != nil {
		t.Fatal(err)
	}
	_ = exec.Command("git", "-C", subGitDir, "config", "--unset", "core.worktree").Run()

	// Advance the submodule so a subsequent superproject pull has a gitlink
	// bump to fetch.
	commitNewFile(t, subUpstream, "sub-new.txt", "sub new content")
	gitC(t, filepath.Join(superUpstream, "sub"), "-c", "protocol.file.allow=always", "pull", "-q", "origin",
		strings.TrimSpace(gitC(t, subUpstream, "branch", "--show-current")))
	gitC(t, superUpstream, "commit", "-qam", "bump submodule")

	markerDir := t.TempDir()
	marker := filepath.Join(markerDir, "submodule-ranmarker")
	for _, hook := range []string{"reference-transaction", "post-merge", "post-checkout"} {
		script := "#!/bin/sh\necho " + hook + " >> " + marker + "\n"
		if err := os.WriteFile(filepath.Join(subGitDir, "hooks", hook), []byte(script), 0755); err != nil {
			t.Fatal(err)
		}
	}
	fsmonitorLine := "\n[core]\n\tfsmonitor = \"echo FSM >> " + marker + "; false\"\n"
	f, err := os.OpenFile(filepath.Join(subGitDir, "config"), os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(fsmonitorLine); err != nil {
		t.Fatal(err)
	}
	f.Close()

	if err := FilterSyncedGitMetadata(cloneDir, SyncedGitRemote{RemoteURL: superUpstream}); err != nil {
		t.Fatalf("FilterSyncedGitMetadata failed: %v", err)
	}

	// Unlike .git/modules/sub, sub/.git/ sits in the working tree and is
	// outside the top-level .git the allowlist rebuild operates on — it is
	// expected to still be standing. Closure here comes from the
	// regenerated config's submodule-recursion keys, not from this
	// directory being removed.
	if _, err := os.Stat(subGitDir); err != nil {
		t.Fatalf("expected sub/.git to remain (outside .git's allowlist rebuild): %v", err)
	}

	result, err := PullSharedWorkspace(cloneDir, "")
	if err != nil {
		t.Fatalf("PullSharedWorkspace failed: %v", err)
	}
	if !result.Updated {
		t.Error("expected Updated=true after pull with new commits")
	}
	requireNoMarkers(t, markerDir)
}

// TestFilterSyncedGitMetadata_GitfileFailsClosed covers matrix case (f): a
// landed ".git" that is a plain file (a "gitdir: <path>" redirect, as used
// by worktrees and submodule checkouts) rather than a real directory
// redirects git to a location this function never inspects.
// FilterSyncedGitMetadata must refuse to partially strip through it: it
// removes the entry and returns an error.
func TestFilterSyncedGitMetadata_GitfileFailsClosed(t *testing.T) {
	workspacePath := t.TempDir()
	gitFile := filepath.Join(workspacePath, ".git")
	if err := os.WriteFile(gitFile, []byte("gitdir: /tmp/somewhere-else\n"), 0644); err != nil {
		t.Fatal(err)
	}

	if err := FilterSyncedGitMetadata(workspacePath, SyncedGitRemote{}); err == nil {
		t.Fatal("expected FilterSyncedGitMetadata to fail closed on a gitfile .git")
	}
	if _, err := os.Stat(gitFile); !os.IsNotExist(err) {
		t.Errorf("expected the gitfile to be removed, stat err: %v", err)
	}
}

// TestFilterSyncedGitMetadata_SymlinkFailsClosed is TestFilterSyncedGitMetadata_GitfileFailsClosed's
// symlink variant. The target is deliberately a real, valid gitdir (as
// another project's .git would be) rather than an empty directory: an empty
// target is rejected by HEAD validation alone and would pass even with the
// shape gate removed, so it would not actually exercise the gate this test
// is for. The case the gate exists for is a symlink into a valid gitdir
// that must be left completely alone.
func TestFilterSyncedGitMetadata_SymlinkFailsClosed(t *testing.T) {
	otherProjectDir := setupGitRepo(t)
	otherGitDir := filepath.Join(otherProjectDir, ".git")
	if _, err := os.Stat(filepath.Join(otherGitDir, "hooks")); err != nil {
		t.Fatalf("test assumption broken: expected %s/hooks to exist", otherGitDir)
	}
	configBefore, err := os.ReadFile(filepath.Join(otherGitDir, "config"))
	if err != nil {
		t.Fatal(err)
	}

	workspacePath := t.TempDir()
	gitLink := filepath.Join(workspacePath, ".git")
	if err := os.Symlink(otherGitDir, gitLink); err != nil {
		t.Fatal(err)
	}

	if err := FilterSyncedGitMetadata(workspacePath, SyncedGitRemote{}); err == nil {
		t.Fatal("expected FilterSyncedGitMetadata to fail closed on a symlinked .git")
	}
	if _, err := os.Lstat(gitLink); !os.IsNotExist(err) {
		t.Errorf("expected the symlink to be removed, stat err: %v", err)
	}

	// The symlink target — another project's real, valid gitdir — must be
	// completely untouched: no hooks removal, no config regeneration. If the
	// shape gate ran the rebuild through the symlink instead of refusing it,
	// both of these would fail.
	if _, err := os.Stat(filepath.Join(otherGitDir, "hooks")); err != nil {
		t.Errorf("symlink target's hooks dir must survive untouched: %v", err)
	}
	configAfter, err := os.ReadFile(filepath.Join(otherGitDir, "config"))
	if err != nil {
		t.Fatal(err)
	}
	if string(configBefore) != string(configAfter) {
		t.Errorf("symlink target's config must survive untouched:\nbefore:\n%s\nafter:\n%s", configBefore, configAfter)
	}
}

// TestFilterSyncedGitMetadata_MissingGitDirNoOp covers matrix case (g): a
// workspace with no .git at all (a hub-native project with no repository, or
// one whose pod never created one) is a no-op, not an error — absence is not
// an execution vector.
func TestFilterSyncedGitMetadata_MissingGitDirNoOp(t *testing.T) {
	dir := t.TempDir()
	if err := FilterSyncedGitMetadata(dir, SyncedGitRemote{}); err != nil {
		t.Fatalf("expected no-op for missing .git, got: %v", err)
	}
}

// TestFilterSyncedGitMetadata_NonDefaultBranch covers matrix case (h): a
// workspace whose HEAD is on a non-default branch must have branch tracking
// regenerated for *that* branch (not a hardcoded default), or the
// subsequent pull fails with "no tracking information" in an otherwise
// ordinary workspace.
func TestFilterSyncedGitMetadata_NonDefaultBranch(t *testing.T) {
	sourceDir := setupGitRepo(t)
	gitC(t, sourceDir, "checkout", "-q", "-b", "feature")
	commitNewFile(t, sourceDir, "feature.txt", "feature content")

	cloneDir := filepath.Join(t.TempDir(), "workspace")
	if err := CloneSharedWorkspace(cloneDir, sourceDir, "feature", ""); err != nil {
		t.Fatalf("Clone failed: %v", err)
	}
	branch := strings.TrimSpace(gitC(t, cloneDir, "branch", "--show-current"))
	if branch != "feature" {
		t.Fatalf("expected clone to be on branch 'feature', got %q", branch)
	}

	if err := FilterSyncedGitMetadata(cloneDir, SyncedGitRemote{RemoteURL: sourceDir}); err != nil {
		t.Fatalf("FilterSyncedGitMetadata failed: %v", err)
	}

	configBytes, err := os.ReadFile(filepath.Join(cloneDir, ".git", "config"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(configBytes), `branch "feature"`) {
		t.Errorf("expected regenerated config to track branch 'feature', got:\n%s", configBytes)
	}

	commitNewFile(t, sourceDir, "feature2.txt", "more feature content")
	result, err := PullSharedWorkspace(cloneDir, "")
	if err != nil {
		t.Fatalf("PullSharedWorkspace failed on non-default branch: %v", err)
	}
	if !result.Updated {
		t.Error("expected Updated=true after pull with new commits")
	}
}

// TestFilterSyncedGitMetadata_GarbageHeadFailsClosed covers matrix case (i):
// a HEAD that is neither a valid refs/heads/<name> symref nor a bare hex
// object id (e.g. a HEAD of "ref: ../../elsewhere") is rejected rather than
// carried into the rebuilt repo.
func TestFilterSyncedGitMetadata_GarbageHeadFailsClosed(t *testing.T) {
	sourceDir := setupGitRepo(t)
	cloneDir := filepath.Join(t.TempDir(), "workspace")
	if err := CloneSharedWorkspace(cloneDir, sourceDir, "", ""); err != nil {
		t.Fatalf("Clone failed: %v", err)
	}

	for _, headContent := range []string{
		"ref: ../../elsewhere\n",
		"not-a-ref-or-oid\n",
		"deadbeef\n", // valid hex, wrong length
	} {
		t.Run(strings.TrimSpace(headContent), func(t *testing.T) {
			cd := filepath.Join(t.TempDir(), "workspace")
			if err := CopyDir(cloneDir, cd); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(cd, ".git", "HEAD"), []byte(headContent), 0644); err != nil {
				t.Fatal(err)
			}
			if err := FilterSyncedGitMetadata(cd, SyncedGitRemote{RemoteURL: sourceDir}); err == nil {
				t.Fatalf("expected FilterSyncedGitMetadata to fail closed on HEAD=%q", headContent)
			}
			if _, err := os.Stat(filepath.Join(cd, ".git")); !os.IsNotExist(err) {
				t.Errorf("expected .git to be removed after a garbage-HEAD rejection, stat err: %v", err)
			}
		})
	}
}

// TestFilterSyncedGitMetadata_UnsupportedFormatRejected covers matrix case
// (j): a landed repo declaring a non-sha1 object format or reftable ref
// storage is rejected outright — the allowlist drops reftable/, and a
// regenerated config would drop the extension key git needs to read either
// format, so carrying it forward would corrupt access rather than secure it.
func TestFilterSyncedGitMetadata_UnsupportedFormatRejected(t *testing.T) {
	sourceDir := setupGitRepo(t)

	for _, tc := range []struct {
		name string
		key  string
		val  string
	}{
		{"sha256", "extensions.objectFormat", "sha256"},
		{"reftable", "extensions.refStorage", "reftable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cloneDir := filepath.Join(t.TempDir(), "workspace")
			if err := CloneSharedWorkspace(cloneDir, sourceDir, "", ""); err != nil {
				t.Fatalf("Clone failed: %v", err)
			}
			gitC(t, cloneDir, "config", tc.key, tc.val)

			if err := FilterSyncedGitMetadata(cloneDir, SyncedGitRemote{RemoteURL: sourceDir}); err == nil {
				t.Fatalf("expected FilterSyncedGitMetadata to reject %s=%s", tc.key, tc.val)
			}
			if _, err := os.Stat(filepath.Join(cloneDir, ".git")); !os.IsNotExist(err) {
				t.Errorf("expected .git to be removed after format rejection, stat err: %v", err)
			}
		})
	}
}

// TestFilterSyncedGitMetadata_HubNativeNoRemote covers matrix case (k): when
// the caller has no legitimate remote for the workspace (e.g. a hub-native
// project with no git remote), FilterSyncedGitMetadata does not fabricate
// one from anything in the landed directory — the regenerated config
// carries identity+core only, no remote or branch section at all.
func TestFilterSyncedGitMetadata_HubNativeNoRemote(t *testing.T) {
	sourceDir := setupGitRepo(t)
	cloneDir := filepath.Join(t.TempDir(), "workspace")
	if err := CloneSharedWorkspace(cloneDir, sourceDir, "", ""); err != nil {
		t.Fatalf("Clone failed: %v", err)
	}

	if err := FilterSyncedGitMetadata(cloneDir, SyncedGitRemote{}); err != nil {
		t.Fatalf("FilterSyncedGitMetadata failed: %v", err)
	}

	configBytes, err := os.ReadFile(filepath.Join(cloneDir, ".git", "config"))
	if err != nil {
		t.Fatalf("reading regenerated config: %v", err)
	}
	config := string(configBytes)
	if strings.Contains(config, "[remote") || strings.Contains(config, sourceDir) {
		t.Errorf("expected no remote section when no remote is known, got:\n%s", config)
	}
	if !strings.Contains(config, "[user]") {
		t.Errorf("expected identity to still be configured, got:\n%s", config)
	}
}

// TestFilterSyncedGitMetadata_BranchNameConfigEscaping covers matrix case
// (l): git ref names may legally contain '"' and ']' (verified:
// refs/heads/a"b]x passes `git check-ref-format`). A workspace landed with
// HEAD on such a branch must not be able to use the branch name to terminate
// a `[branch "<name>"]` config section early and add keys not derived from
// host state. The regenerated config must contain no such unexpected key,
// whether because the branch name was charset-rejected (the actual behavior:
// it's outside [A-Za-z0-9._/-]) or because it was safely escaped.
func TestFilterSyncedGitMetadata_BranchNameConfigEscaping(t *testing.T) {
	sourceDir := setupGitRepo(t)
	cloneDir := filepath.Join(t.TempDir(), "workspace")
	if err := CloneSharedWorkspace(cloneDir, sourceDir, "", ""); err != nil {
		t.Fatalf("Clone failed: %v", err)
	}
	gitDir := filepath.Join(cloneDir, ".git")

	branch := `a"b]x`
	if !isValidGitRefName("refs/heads/" + branch) {
		t.Fatalf("test assumption broken: refs/heads/%s is no longer accepted by check-ref-format", branch)
	}

	headSHA := strings.TrimSpace(gitC(t, cloneDir, "rev-parse", "HEAD"))
	if err := os.WriteFile(filepath.Join(gitDir, "refs", "heads", branch), []byte(headSHA+"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(gitDir, "HEAD"), []byte("ref: refs/heads/"+branch+"\n"), 0644); err != nil {
		t.Fatal(err)
	}

	if err := FilterSyncedGitMetadata(cloneDir, SyncedGitRemote{RemoteURL: sourceDir}); err != nil {
		t.Fatalf("FilterSyncedGitMetadata failed: %v", err)
	}

	configBytes, err := os.ReadFile(filepath.Join(gitDir, "config"))
	if err != nil {
		t.Fatal(err)
	}
	config := string(configBytes)
	for _, forbidden := range []string{"fsmonitor", "hooksPath", "hook", "ranmarker"} {
		if strings.Contains(config, forbidden) {
			t.Errorf("regenerated config unexpectedly contains unexpected content %q:\n%s", forbidden, config)
		}
	}
	// Pin the charset layer specifically (on top of the "no unexpected
	// content" checks above): the actual behavior for a branch name outside
	// [A-Za-z0-9._/-] is to skip branch tracking entirely, not to escape and
	// write it. No [branch section at all should be present for this name.
	if strings.Contains(config, "[branch") {
		t.Errorf("expected no branch.* section for a charset-rejected branch name, got:\n%s", config)
	}
	// Whatever was written must still be a valid, parseable config file —
	// proving the section header was not terminated early.
	if out, err := exec.Command("git", "config", "--file", filepath.Join(gitDir, "config"), "--list").CombinedOutput(); err != nil {
		t.Errorf("regenerated config is not valid after a special-character branch name: %v (%s)", err, out)
	}
}

func TestSanitizeGitOutput(t *testing.T) {
	tests := []struct {
		name   string
		output string
		token  string
		want   string
	}{
		{"empty token", "fatal: error", "", "fatal: error"},
		{"token in URL", "fatal: could not read from https://oauth2:mytoken@github.com", "mytoken", "fatal: could not read from https://oauth2:***@github.com"},
		{"no token present", "fatal: some other error", "mytoken", "fatal: some other error"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := sanitizeGitOutput(tt.output, tt.token)
			if got != tt.want {
				t.Errorf("sanitizeGitOutput() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestClassifyGitError(t *testing.T) {
	tests := []struct {
		name     string
		stderr   string
		wantKind GitErrorKind
	}{
		{"auth failure 401", "fatal: Authentication failed for 'https://github.com/org/repo.git/': 401", GitErrAuth},
		{"auth failure 403", "remote: Permission denied (403)", GitErrAuth},
		{"invalid credentials", "fatal: Invalid credentials", GitErrAuth},
		{"could not read username", "fatal: could not read Username for 'https://github.com'", GitErrAuth},
		{"not found", "fatal: repository 'https://github.com/org/repo.git/' not found", GitErrNotFound},
		{"404", "ERROR: Repository not found. 404", GitErrNotFound},
		{"network error", "fatal: unable to access: Could not resolve host: github.com", GitErrNetwork},
		{"connection refused", "fatal: unable to connect: connection refused", GitErrNetwork},
		{"timed out", "fatal: unable to access: timed out", GitErrNetwork},
		{"non-fast-forward", "fatal: Not possible to fast-forward, aborting.", GitErrNonFastForward},
		{"unknown error", "fatal: some unknown error", GitErrUnknown},
		{"empty stderr", "", GitErrUnknown},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gitErr := ClassifyGitError(tt.stderr)
			if gitErr.Kind != tt.wantKind {
				t.Errorf("ClassifyGitError(%q).Kind = %v, want %v", tt.stderr, gitErr.Kind, tt.wantKind)
			}
			if gitErr.Message != tt.stderr {
				t.Errorf("ClassifyGitError(%q).Message = %q, want %q", tt.stderr, gitErr.Message, tt.stderr)
			}
		})
	}
}

func TestIsRemoteBranchNotFound(t *testing.T) {
	tests := []struct {
		name   string
		stderr string
		want   bool
	}{
		{"exact git message", "fatal: Remote branch a2a-bridge not found in upstream origin", true},
		{"lowercase variant", "fatal: remote branch my-branch not found in upstream origin", true},
		{"repo not found", "fatal: repository 'https://github.com/org/repo.git/' not found", false},
		{"auth failure", "fatal: Authentication failed for 'https://github.com/org/repo.git/'", false},
		{"empty", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isRemoteBranchNotFound(tt.stderr); got != tt.want {
				t.Errorf("isRemoteBranchNotFound(%q) = %v, want %v", tt.stderr, got, tt.want)
			}
		})
	}
}

func TestGitError_UserGuidance(t *testing.T) {
	tests := []struct {
		kind     GitErrorKind
		wantHint bool
	}{
		{GitErrAuth, true},
		{GitErrNotFound, true},
		{GitErrNetwork, true},
		{GitErrNonFastForward, true},
		{GitErrUnknown, false},
	}
	for _, tt := range tests {
		err := &GitError{Kind: tt.kind, Message: "test"}
		guidance := err.UserGuidance()
		if tt.wantHint && guidance == "" {
			t.Errorf("GitError{Kind: %v}.UserGuidance() returned empty, want non-empty", tt.kind)
		}
		if !tt.wantHint && guidance != "" {
			t.Errorf("GitError{Kind: %v}.UserGuidance() = %q, want empty", tt.kind, guidance)
		}
	}
}

func TestAuthenticatedCloneURL(t *testing.T) {
	const token = "ghp_exampletoken"

	tests := []struct {
		name     string
		cloneURL string
		token    string
		want     string
	}{
		{
			name:     "plain https remote gets credentials",
			cloneURL: "https://github.com/org/repo.git",
			token:    token,
			want:     "https://oauth2:" + token + "@github.com/org/repo.git",
		},
		{
			name:     "remote that already carries userinfo is not doubled up",
			cloneURL: "https://org@dev.azure.com/org/project/_git/repo",
			token:    token,
			want:     "https://oauth2:" + token + "@dev.azure.com/org/project/_git/repo",
		},
		{
			name:     "empty token leaves the URL alone",
			cloneURL: "https://github.com/org/repo.git",
			token:    "",
			want:     "https://github.com/org/repo.git",
		},
		{
			name:     "token with reserved characters is escaped",
			cloneURL: "https://github.com/org/repo.git",
			token:    "p@ss/word",
			want:     "https://oauth2:p%40ss%2Fword@github.com/org/repo.git",
		},
		{
			name:     "non-default port is preserved",
			cloneURL: "https://host.example.com:8443/org/repo.git",
			token:    token,
			want:     "https://oauth2:" + token + "@host.example.com:8443/org/repo.git",
		},
		{
			name:     "scp-style ssh remote is left alone",
			cloneURL: "git@github.com:org/repo.git",
			token:    token,
			want:     "git@github.com:org/repo.git",
		},
		{
			name:     "ssh scheme is left alone rather than given an oauth token",
			cloneURL: "ssh://git@github.com/org/repo.git",
			token:    token,
			want:     "ssh://git@github.com/org/repo.git",
		},
		{
			name:     "plain http is left alone",
			cloneURL: "http://internal.example.com/org/repo.git",
			token:    token,
			want:     "http://internal.example.com/org/repo.git",
		},
		{
			name:     "unparseable remote is returned unchanged",
			cloneURL: "not a url",
			token:    token,
			want:     "not a url",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := authenticatedCloneURL(tt.cloneURL, tt.token)
			if got != tt.want {
				t.Errorf("authenticatedCloneURL(%q, token) = %q, want %q", tt.cloneURL, got, tt.want)
			}
			if tt.token != "" && strings.Count(got, "@") > 1 {
				t.Errorf("result contains more than one @ separator: %q", got)
			}
		})
	}
}

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

package runtime

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GoogleCloudPlatform/scion/pkg/harness"
)

// P2: a private-config source that is a symlink, a non-regular file, or missing
// is refused, so the start fails closed rather than binding an unsafe source.
func TestCheckPrivateGitConfigSource(t *testing.T) {
	dir := t.TempDir()

	good := filepath.Join(dir, "config")
	if err := os.WriteFile(good, []byte("[core]\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := checkPrivateGitConfigSource(good); err != nil {
		t.Errorf("a regular broker-owned file should be accepted, got: %v", err)
	}

	link := filepath.Join(dir, "link")
	if err := os.Symlink(good, link); err != nil {
		t.Fatal(err)
	}
	if err := checkPrivateGitConfigSource(link); err == nil {
		t.Error("a symlink source must be refused, got nil")
	}

	if err := checkPrivateGitConfigSource(filepath.Join(dir, "missing")); err == nil {
		t.Error("a missing source must be refused, got nil")
	}

	if err := checkPrivateGitConfigSource(dir); err == nil {
		t.Error("a directory source must be refused, got nil")
	}
}

// P2 via buildCommonRunArgs: a worktree layout whose private-config source is a
// symlink refuses the start.
func TestBuildCommonRunArgs_RefusesSymlinkedPrivateConfig(t *testing.T) {
	repoRoot, workspace := setupHubManagedBaseRepo(t, "agent-1")
	realCfg := filepath.Join(t.TempDir(), "config")
	if err := os.WriteFile(realCfg, []byte("[core]\n"), 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(realCfg, link); err != nil {
		t.Fatal(err)
	}
	_, err := buildCommonRunArgs(RunConfig{
		Harness:                &harness.Generic{},
		Name:                   "test-agent",
		UnixUsername:           "scion",
		Image:                  "scion-agent:latest",
		RuntimeName:            "docker",
		RepoRoot:               repoRoot,
		Workspace:              workspace,
		PrivateGitConfigSource: link,
	})
	if err == nil {
		t.Fatal("expected a refusal for a symlinked private-config source, got nil")
	}
}

// P5: sibling worktree admin dirs are not writable — .git/worktrees is bound
// read-only with only the agent's own worktrees/<name>/ writable.
func TestWorktreesDir_SiblingNotWritable(t *testing.T) {
	repoRoot, workspace := setupHubManagedBaseRepo(t, "agent-1")
	gitDir := filepath.Join(repoRoot, ".git")
	// The agent's own worktree admin dir and a sibling's.
	for _, name := range []string{"agent-1", "other"} {
		d := filepath.Join(gitDir, "worktrees", name)
		if err := os.MkdirAll(d, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(d, "HEAD"), []byte("ref: refs/heads/x\n"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	args, err := buildCommonRunArgs(RunConfig{
		Harness:                &harness.Generic{},
		Name:                   "test-agent",
		UnixUsername:           "scion",
		Image:                  "scion-agent:latest",
		RuntimeName:            "docker",
		RepoRoot:               repoRoot,
		Workspace:              workspace,
		PrivateGitConfigSource: seedTestPrivateGitConfig(t),
	})
	if err != nil {
		t.Fatalf("buildCommonRunArgs failed: %v", err)
	}
	argStr := strings.Join(args, " ")

	// worktrees/ is read-only; the agent's own worktrees/agent-1 is read-write.
	wantRO := "-v " + filepath.Join(gitDir, "worktrees") + ":/repo-root/.git/worktrees:ro"
	if !strings.Contains(argStr, wantRO) {
		t.Errorf("expected read-only worktrees dir mount %q, got: %s", wantRO, argStr)
	}
	wantOwnRW := "-v " + filepath.Join(gitDir, "worktrees", "agent-1") + ":/repo-root/.git/worktrees/agent-1 "
	if !strings.Contains(argStr, wantOwnRW) {
		t.Errorf("expected the agent's own worktree admin dir to be read-write %q, got: %s", wantOwnRW, argStr)
	}
	// There must be no read-write overlay for the sibling.
	if strings.Contains(argStr, "/repo-root/.git/worktrees/other ") {
		t.Errorf("a sibling worktree admin dir must not be writable, got: %s", argStr)
	}
}

// P4: the shared-plain git layout is refused on the Apple runtime (cannot bind a
// single-file private config; a read-only .git would break the main worktree).
func TestSharedPlainGitMounts_RefusedOnApple(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	repoRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repoRoot, ".git", "hooks"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repoRoot, ".git", "config"), []byte("[core]\n"), 0644); err != nil {
		t.Fatal(err)
	}
	_, err := buildCommonRunArgs(RunConfig{
		Harness:      &harness.Generic{},
		Name:         "test-agent",
		UnixUsername: "scion",
		Image:        "scion-agent:latest",
		RuntimeName:  appleContainerRuntimeName,
		RepoRoot:     repoRoot,
		Workspace:    repoRoot, // shared-plain: workspace == repo root
	})
	if err == nil {
		t.Fatal("expected the shared-plain git layout to be refused on Apple, got nil")
	}
}

// A non-git shared-plain workspace (no .git dir) is allowed on Apple: there is no
// admin surface to protect and nothing to make private.
func TestSharedPlainGitMounts_NonGitAllowedOnApple(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	repoRoot := t.TempDir() // no .git
	if _, err := buildCommonRunArgs(RunConfig{
		Harness:      &harness.Generic{},
		Name:         "test-agent",
		UnixUsername: "scion",
		Image:        "scion-agent:latest",
		RuntimeName:  appleContainerRuntimeName,
		RepoRoot:     repoRoot,
		Workspace:    repoRoot,
	}); err != nil {
		t.Fatalf("a non-git shared workspace should be allowed on Apple, got: %v", err)
	}
}

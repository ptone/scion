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

package provision

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/GoogleCloudPlatform/scion/pkg/api"
)

// ---------------------------------------------------------------------------
// Task #49 — clone-depth measurement rows
//
// These tests measure today's behaviour BEFORE any fix. Each row
// corresponds to a scenario from the task brief. They are pins: the
// implementation is allowed to change the expectation, but if a row goes
// red the change must be deliberate and the commit message must say why.
// ---------------------------------------------------------------------------

// Row 1: GitCloneConfig{Depth: 0} marshalled to JSON — key absent (omitempty).
func TestCloneDepth_Row1_DepthZeroOmitted(t *testing.T) {
	gc := api.GitCloneConfig{URL: "https://example.com/repo.git", Depth: 0}
	b, err := json.Marshal(gc)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var raw map[string]interface{}
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatalf("unmarshal to map: %v", err)
	}

	if _, present := raw["depth"]; present {
		t.Errorf("Depth: 0 should be omitted by omitempty, got JSON: %s", b)
	} else {
		t.Logf("CONFIRMED: Depth: 0 → key absent. JSON: %s", b)
	}
}

// Row 2: JSON {} unmarshalled, then through gitCloneWorkspace → --depth 1.
// We run a real `git clone` against a local bare repo and verify the result
// is a shallow clone (depth 1).
func TestCloneDepth_Row2_DepthZeroProducesShallow(t *testing.T) {
	bareRepo, cloneURL := createLocalBareRepo(t)
	_ = bareRepo

	destDir := filepath.Join(t.TempDir(), "clone")

	// Unmarshal absent-depth JSON → Depth == 0
	var gc api.GitCloneConfig
	if err := json.Unmarshal([]byte(`{"url":"`+cloneURL+`"}`), &gc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if gc.Depth != 0 {
		t.Fatalf("expected Depth=0 after unmarshal, got %d", gc.Depth)
	}

	err := gitCloneWorkspace(context.Background(), ProvisionInput{
		GitClone: &gc,
		Resolved: ResolvedWorkspace{HostPath: destDir},
	})
	if err != nil {
		t.Fatalf("gitCloneWorkspace: %v", err)
	}

	if !isShallowClone(t, destDir) {
		t.Errorf("Depth: 0 should produce a shallow clone (depth 1), but the clone is NOT shallow")
	} else {
		t.Logf("CONFIRMED: Depth: 0 → shallow clone (depth 1)")
	}
}

// Row 3: GitCloneConfig{Depth: -1} marshalled → key present, value -1.
func TestCloneDepth_Row3_DepthNegOneSurvivesOmitempty(t *testing.T) {
	gc := api.GitCloneConfig{URL: "https://example.com/repo.git", Depth: -1}
	b, err := json.Marshal(gc)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var raw map[string]interface{}
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatalf("unmarshal to map: %v", err)
	}

	depthVal, present := raw["depth"]
	if !present {
		t.Errorf("Depth: -1 should survive omitempty, but key is absent. JSON: %s", b)
	} else {
		// JSON numbers decode as float64
		if d, ok := depthVal.(float64); !ok || d != -1 {
			t.Errorf("expected depth=-1, got %v", depthVal)
		} else {
			t.Logf("CONFIRMED: Depth: -1 → key present, value -1. JSON: %s", b)
		}
	}
}

// Row 4: Depth: -1 through provision.go → no --depth flag → full clone.
func TestCloneDepth_Row4_DepthNegOneProducesFullClone(t *testing.T) {
	_, cloneURL := createLocalBareRepo(t)

	destDir := filepath.Join(t.TempDir(), "clone")

	gc := &api.GitCloneConfig{URL: cloneURL, Depth: -1}
	err := gitCloneWorkspace(context.Background(), ProvisionInput{
		GitClone: gc,
		Resolved: ResolvedWorkspace{HostPath: destDir},
	})
	if err != nil {
		t.Fatalf("gitCloneWorkspace: %v", err)
	}

	if isShallowClone(t, destDir) {
		t.Errorf("Depth: -1 should produce a full clone (no --depth), but the clone IS shallow")
	} else {
		t.Logf("CONFIRMED: Depth: -1 → full clone (no --depth flag)")
	}
}

// Row 7: Push from a depth-1 clone to a SECOND remote.
func TestCloneDepth_Row7_ShallowPushToSecondRemote(t *testing.T) {
	_, originURL := createLocalBareRepo(t)

	// Clone shallow (depth 1) from origin
	cloneDir := filepath.Join(t.TempDir(), "clone")
	runGit(t, "", "clone", "--depth", "1", originURL, cloneDir)

	if !isShallowClone(t, cloneDir) {
		t.Fatal("precondition: clone should be shallow")
	}

	// Create a second bare repo to act as an upstream/fork
	secondDir := filepath.Join(t.TempDir(), "second-remote")
	runGit(t, "", "init", "--bare", secondDir)

	// Add it as a remote
	runGit(t, cloneDir, "remote", "add", "upstream", secondDir)

	// Make a commit so there's something to push
	writeFile(t, filepath.Join(cloneDir, "new.txt"), "task-49")
	runGit(t, cloneDir, "add", "new.txt")
	runGit(t, cloneDir, "commit", "-m", "task-49 test commit")

	// Try to push to the second remote
	cmd := exec.Command("git", "push", "upstream", "main")
	cmd.Dir = cloneDir
	output, err := cmd.CombinedOutput()

	if err != nil {
		t.Logf("MEASURED: push to second remote FAILED: %s\n%s", err, output)
	} else {
		t.Logf("MEASURED: push to second remote SUCCEEDED")
	}
	// This row is measurement — we log the result, the test does not assert
	// pass/fail because we are documenting what happens today, not prescribing it.
	// The brief says: "If row 8 succeeds and row 7 fails, that pair IS the
	// defect statement."
	//
	// However, to make the test useful as a pin, record the outcome.
	if err != nil {
		t.Logf("DEFECT CONFIRMED: shallow clone cannot push to a second remote")
	}
}

// Row 8: Push from a depth-1 clone back to origin.
func TestCloneDepth_Row8_ShallowPushToOrigin(t *testing.T) {
	_, originURL := createLocalBareRepo(t)

	// Clone shallow (depth 1) from origin
	cloneDir := filepath.Join(t.TempDir(), "clone")
	runGit(t, "", "clone", "--depth", "1", originURL, cloneDir)

	if !isShallowClone(t, cloneDir) {
		t.Fatal("precondition: clone should be shallow")
	}

	// Make a commit so there's something to push
	writeFile(t, filepath.Join(cloneDir, "new.txt"), "task-49")
	runGit(t, cloneDir, "add", "new.txt")
	runGit(t, cloneDir, "commit", "-m", "task-49 test commit")

	// Try to push to origin
	cmd := exec.Command("git", "push", "origin", "main")
	cmd.Dir = cloneDir
	output, err := cmd.CombinedOutput()

	if err != nil {
		t.Logf("MEASURED: push to origin FAILED: %s\n%s", err, output)
	} else {
		t.Logf("MEASURED: push to origin SUCCEEDED")
	}

	if err != nil {
		t.Logf("UNEXPECTED: even shallow clone should be able to push to origin")
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// createLocalBareRepo creates a bare git repository with two commits and
// returns (path, file:// URL). Two commits ensure a depth-1 clone is
// meaningfully shallow.
func createLocalBareRepo(t *testing.T) (string, string) {
	t.Helper()

	// Create a temporary working repo, make two commits, then clone it bare.
	workDir := filepath.Join(t.TempDir(), "work")
	if err := os.MkdirAll(workDir, 0755); err != nil {
		t.Fatal(err)
	}

	runGit(t, workDir, "init", "-b", "main")
	runGit(t, workDir, "config", "user.email", "test@test.com")
	runGit(t, workDir, "config", "user.name", "Test")

	writeFile(t, filepath.Join(workDir, "file1.txt"), "commit 1")
	runGit(t, workDir, "add", "file1.txt")
	runGit(t, workDir, "commit", "-m", "first commit")

	writeFile(t, filepath.Join(workDir, "file2.txt"), "commit 2")
	runGit(t, workDir, "add", "file2.txt")
	runGit(t, workDir, "commit", "-m", "second commit")

	// Clone bare
	bareDir := filepath.Join(t.TempDir(), "bare.git")
	runGit(t, "", "clone", "--bare", workDir, bareDir)

	return bareDir, "file://" + bareDir
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, out)
	}
}

func isShallowClone(t *testing.T, dir string) bool {
	t.Helper()
	cmd := exec.Command("git", "rev-parse", "--is-shallow-repository")
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git rev-parse --is-shallow-repository: %v\n%s", err, out)
	}
	return len(out) > 0 && out[0] == 't' // "true\n"
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

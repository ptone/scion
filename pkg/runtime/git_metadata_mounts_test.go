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

// A private-config source that is a symlink, a non-regular file, or missing
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

// Via buildCommonRunArgs: a worktree layout whose private-config source is a
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

// Sibling worktree admin dirs are not writable — .git/worktrees is bound
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

// The shared-plain git layout is refused on the Apple runtime (cannot bind a
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

// The read-write own-worktree overlay is chosen from the broker-trusted
// back-links in the common dir, NOT from the agent-writable <workspace>/.git
// gitfile. An agent-written gitfile (pointing at a sibling admin dir, at ".",
// or at "..") must not change which admin dir is overlaid read-write.
func TestWorktreeAdmin_IgnoresAgentWritableGitfile(t *testing.T) {
	for _, agentPtr := range []string{
		"gitdir: ../../.git/worktrees/other", // (a) a sibling's admin dir
		"gitdir: .",                          // (b) the whole worktrees/ dir
		"gitdir: ..",                         // (c) the .git root (would void Apple ro base)
	} {
		t.Run(agentPtr, func(t *testing.T) {
			repoRoot, workspace := setupHubManagedBaseRepo(t, "agent-1")
			gitDir := filepath.Join(repoRoot, ".git")
			// A sibling worktree admin dir alongside the agent's own.
			sibling := filepath.Join(gitDir, "worktrees", "other")
			if err := os.MkdirAll(sibling, 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(sibling, "HEAD"), []byte("ref: refs/heads/x\n"), 0644); err != nil {
				t.Fatal(err)
			}
			// The agent rewrites its own workspace/.git gitfile.
			if err := os.WriteFile(filepath.Join(workspace, ".git"), []byte(agentPtr+"\n"), 0644); err != nil {
				t.Fatal(err)
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
			// The own-worktree rw overlay targets the trusted admin (agent-1).
			if !strings.Contains(argStr, "-v "+filepath.Join(gitDir, "worktrees", "agent-1")+":/repo-root/.git/worktrees/agent-1 ") {
				t.Errorf("own rw overlay should target the trusted admin agent-1, got: %s", argStr)
			}
			// It must NOT give the sibling, the whole worktrees dir, or the .git
			// root a read-write overlay.
			if strings.Contains(argStr, "/repo-root/.git/worktrees/other ") {
				t.Errorf("a sibling admin dir must not be read-write, got: %s", argStr)
			}
			if strings.Contains(argStr, "/repo-root/.git/worktrees :") || strings.Contains(argStr, "/repo-root/.git/worktrees ") {
				t.Errorf("the whole worktrees dir must not be read-write, got: %s", argStr)
			}
			// worktrees/ itself stays read-only.
			if !strings.Contains(argStr, ":/repo-root/.git/worktrees:ro") {
				t.Errorf("worktrees/ must be read-only, got: %s", argStr)
			}
		})
	}
}

// checkPrivateGitConfigSource refuses a hard-linked source
// (link count > 1) — disabling the check previously survived every test.
func TestCheckPrivateGitConfigSource_RefusesHardLink(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "config")
	if err := os.WriteFile(good, []byte("[core]\n"), 0600); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(dir, "other")
	if err := os.Link(good, other); err != nil {
		t.Skipf("hard links unsupported here: %v", err)
	}
	if err := checkPrivateGitConfigSource(good); err == nil {
		t.Error("a hard-linked (nlink>1) source must be refused, got nil")
	}
}

// Regression for the overlay-selection bypass: the workspace .git gitfile is
// agent-writable, so an agent could replace it with a symlink to a sibling's
// gitfile. If the agent's own back-link is stale, only the sibling would then
// resolve to the (symlink-followed) target. The resolver must refuse a
// non-regular <workspace>/.git rather than let a sibling admin dir be selected.
func TestTrustedWorktreeAdminName_RefusesSymlinkedGitfile(t *testing.T) {
	repoRoot := t.TempDir()
	gitDir := filepath.Join(repoRoot, ".git")
	siblingWs := filepath.Join(repoRoot, "sib")
	if err := os.MkdirAll(siblingWs, 0o755); err != nil {
		t.Fatal(err)
	}
	// A legitimate sibling worktree admin whose back-link points at its own gitfile.
	setupWorktreeAdmin(t, repoRoot, "other", siblingWs)

	// The agent's own workspace, with a STALE own back-link (points elsewhere),
	// and a <ws>/.git that is a symlink to the sibling's gitfile.
	ws := filepath.Join(repoRoot, "wt")
	if err := os.MkdirAll(ws, 0o755); err != nil {
		t.Fatal(err)
	}
	ownAdmin := filepath.Join(gitDir, "worktrees", "agent-1")
	if err := os.MkdirAll(ownAdmin, 0o755); err != nil {
		t.Fatal(err)
	}
	// Stale: points at a path that no longer exists / is not this workspace.
	if err := os.WriteFile(filepath.Join(ownAdmin, "gitdir"), []byte(filepath.Join(repoRoot, "gone", ".git")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(siblingWs, ".git"), filepath.Join(ws, ".git")); err != nil {
		t.Fatal(err)
	}

	name, ok := trustedWorktreeAdminName(gitDir, ws)
	if ok || name != "" {
		t.Errorf("a symlinked <workspace>/.git must be refused (fail closed), got name=%q ok=%v", name, ok)
	}
}

// Invariant: wherever the base .git is bound read-write, the whole
// worktrees/ admin directory must be bound read-only, with only the agent's own
// worktrees/<name>/ overlaid read-write. This is what the back-link trust relies
// on; a future layout or a relaxed narrowing that left worktrees/ writable would
// let a container write a sibling's admin files again.
func TestWorktreesDir_IsReadOnlyWhereverGitIsReadWrite(t *testing.T) {
	for _, rt := range []string{"docker", "podman"} {
		t.Run(rt, func(t *testing.T) {
			repoRoot, workspace := setupHubManagedBaseRepo(t, "agent-1")
			gitDir := filepath.Join(repoRoot, ".git")
			args, err := buildCommonRunArgs(RunConfig{
				Harness:                &harness.Generic{},
				Name:                   "test-agent",
				UnixUsername:           "scion",
				Image:                  "scion-agent:latest",
				RuntimeName:            rt,
				RepoRoot:               repoRoot,
				Workspace:              workspace,
				PrivateGitConfigSource: seedTestPrivateGitConfig(t),
			})
			if err != nil {
				t.Fatalf("buildCommonRunArgs: %v", err)
			}
			argStr := strings.Join(args, " ")
			// The base .git is read-write (not a :ro base like Apple).
			if !strings.Contains(argStr, "-v "+gitDir+":/repo-root/.git ") {
				t.Fatalf("expected a read-write base .git mount, got: %s", argStr)
			}
			if strings.Contains(argStr, "-v "+gitDir+":/repo-root/.git:ro") {
				t.Fatalf("base .git unexpectedly read-only for %s", rt)
			}
			// worktrees/ itself must be read-only.
			if !strings.Contains(argStr, ":/repo-root/.git/worktrees:ro") {
				t.Errorf("worktrees/ must be read-only wherever .git is rw, got: %s", argStr)
			}
			// The only read-write bind under worktrees/ is the agent's own admin dir.
			for _, a := range args {
				i := strings.Index(a, ":/repo-root/.git/worktrees/")
				if i < 0 {
					continue
				}
				target := a[i+1:]
				isOwn := strings.HasPrefix(target, "/repo-root/.git/worktrees/agent-1")
				isRO := strings.HasSuffix(target, ":ro")
				if !isRO && !isOwn {
					t.Errorf("unexpected read-write bind under worktrees/: %s", a)
				}
			}
		})
	}
}

// bindReadOnlyCreateFirstFile must return an error (so the caller refuses the
// start) when it cannot create the placeholder, rather than silently registering
// no read-only bind and leaving a writable file inside the rw .git root.
func TestBindReadOnlyCreateFirstFile_FailsClosed(t *testing.T) {
	dir := t.TempDir()
	// A regular file where a directory is needed: MkdirAll of <blocker>/sub fails.
	blocker := filepath.Join(dir, "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(blocker, "sub", "config.worktree")
	registered := false
	err := bindReadOnlyCreateFirstFile(func(string, string, bool, bool) { registered = true }, src, "/repo-root/.git/config.worktree")
	if err == nil {
		t.Error("expected an error when the placeholder cannot be created, got nil")
	}
	if registered {
		t.Error("no mount must be registered when the placeholder cannot be created")
	}
}

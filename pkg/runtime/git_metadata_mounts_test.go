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
	"os/exec"
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

// Sibling worktree admin dirs are not writable — the .git root is read-only
// (so worktrees/ is read-only by inheritance) with only the agent's own
// worktrees/<name>/ overlaid read-write.
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

	// The .git root is read-only (worktrees/ read-only by inheritance); the
	// agent's own worktrees/agent-1 is overlaid read-write.
	wantRO := "-v " + gitDir + ":/repo-root/.git:ro"
	if !strings.Contains(argStr, wantRO) {
		t.Errorf("expected read-only .git root mount %q, got: %s", wantRO, argStr)
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
			// The .git root is read-only, so sibling worktree admin dirs are
			// read-only by inheritance (no explicit rw overlay for them).
			if !strings.Contains(argStr, "-v "+gitDir+":/repo-root/.git:ro") {
				t.Errorf("the .git root must be read-only, got: %s", argStr)
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

// Invariant: the .git ROOT is read-only in the docker/podman worktree
// layout, so no new root-level entry can be created, and the ONLY read-write
// binds under /repo-root/.git are the bounded overlay set (objects, refs, logs,
// lfs, rr-cache and the agent's own worktrees/<name>/). A sibling
// worktree admin dir is read-only by inheritance (no rw overlay for it).
func TestReadOnlyGitRoot_WorktreeLayout_BoundedRwOverlays(t *testing.T) {
	for _, rt := range []string{"docker", "podman"} {
		t.Run(rt, func(t *testing.T) {
			repoRoot, workspace := setupHubManagedBaseRepo(t, "agent-1")
			gitDir := filepath.Join(repoRoot, ".git")
			// A sibling worktree admin dir that must stay read-only.
			sibling := filepath.Join(gitDir, "worktrees", "other")
			if err := os.MkdirAll(sibling, 0o755); err != nil {
				t.Fatal(err)
			}
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
			// The base .git root is read-only.
			if !strings.Contains(argStr, "-v "+gitDir+":/repo-root/.git:ro") {
				t.Fatalf("expected a read-only base .git mount, got: %s", argStr)
			}
			if strings.Contains(argStr, "-v "+gitDir+":/repo-root/.git ") {
				t.Fatalf("base .git unexpectedly read-write for %s", rt)
			}
			// Every read-write (-v …:<target> with no :ro) bind under
			// /repo-root/.git must be in the bounded overlay allow-list.
			allowed := map[string]bool{
				"/repo-root/.git/objects":           true,
				"/repo-root/.git/refs":              true,
				"/repo-root/.git/logs":              true,
				"/repo-root/.git/lfs":               true,
				"/repo-root/.git/rr-cache":          true,
				"/repo-root/.git/worktrees/agent-1": true,
			}
			for i, a := range args {
				if a != "-v" || i+1 >= len(args) {
					continue
				}
				spec := args[i+1]
				j := strings.Index(spec, ":/repo-root/.git")
				if j < 0 || strings.HasSuffix(spec, ":ro") {
					continue // not a .git bind, or read-only
				}
				target := spec[j+1:]
				if target == "/repo-root/.git" {
					t.Errorf("the .git root must not be read-write: %s", spec)
					continue
				}
				if !allowed[target] {
					t.Errorf("unexpected read-write bind under /repo-root/.git: %s", spec)
				}
			}
			// The sibling admin dir gets no read-write overlay.
			if strings.Contains(argStr, ":/repo-root/.git/worktrees/other ") {
				t.Errorf("a sibling admin dir must not be read-write, got: %s", argStr)
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

// Functional check of the read-only-.git-root layout with real git: with the
// common .git root made read-only (as the container mount does) and only the
// overlay paths writable (objects/refs/logs and the agent's own worktree admin
// dir), an in-container git can still commit, create and delete a branch, fetch,
// pull and push, while it cannot create a new root-level file such as
// .git/commondir. This exercises the worktree layout; the outside-root fallback
// mounts the same shape (a registered worktree of the same common .git), so its
// git behaviour is identical.
func TestReadOnlyGitRoot_Functional(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions; cannot simulate a read-only .git root")
	}
	root := t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(root, "gc"))
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
	if err := os.WriteFile(filepath.Join(root, "gc"), []byte("[user]\n\temail=a@b.c\n\tname=A\n[init]\n\tdefaultBranch=main\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	git := func(dir string, args ...string) (string, error) {
		c := exec.Command("git", append([]string{"-C", dir}, args...)...)
		c.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
		out, err := c.CombinedOutput()
		return string(out), err
	}
	mustGit := func(dir string, args ...string) {
		t.Helper()
		if out, err := git(dir, args...); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	// A bare remote, a main checkout, and a linked worktree on branch "agent-1"
	// with its upstream pre-set (so push needs no config write).
	bare := filepath.Join(root, "bare.git")
	mustGit(root, "init", "-q", "--bare", bare)
	main := filepath.Join(root, "main")
	mustGit(root, "init", "-q", "-b", "main", main)
	mustGit(main, "remote", "add", "origin", bare)
	if err := os.WriteFile(filepath.Join(main, "f.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustGit(main, "add", ".")
	mustGit(main, "-c", "core.hooksPath="+os.DevNull, "commit", "-q", "-m", "init")
	mustGit(main, "push", "-q", "origin", "main")
	wt := filepath.Join(root, "wt")
	mustGit(main, "worktree", "add", "-q", "-b", "agent-1", wt)
	mustGit(wt, "config", "branch.agent-1.remote", "origin")
	mustGit(wt, "config", "branch.agent-1.merge", "refs/heads/agent-1")
	mustGit(wt, "push", "-q", "-u", "origin", "agent-1")

	// Make the common .git root read-only, leaving its subdirectories (objects,
	// refs, logs, worktrees) writable — exactly the container mount shape.
	commonGit := filepath.Join(main, ".git")
	if err := os.Chmod(commonGit, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(commonGit, 0o755) })

	// A new root-level file (e.g. commondir) cannot be created in the read-only root.
	if err := os.WriteFile(filepath.Join(commonGit, "commondir"), []byte("../evil\n"), 0o644); err == nil {
		t.Error("creating a root-level .git/commondir must fail under the read-only root")
	}

	// Normal git write operations in the worktree still work.
	if err := os.WriteFile(filepath.Join(wt, "g.txt"), []byte("y\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustGit(wt, "add", ".")
	mustGit(wt, "-c", "core.hooksPath="+os.DevNull, "commit", "-q", "-m", "work")
	mustGit(wt, "branch", "tmp")         // create a (loose) branch
	mustGit(wt, "fetch", "-q", "origin") // fetch
	mustGit(wt, "push", "-q")            // push (upstream pre-set)
	mustGit(wt, "pull", "-q", "--ff-only", "origin", "agent-1")
	// A trivial rebase onto the already-current upstream.
	mustGit(wt, "-c", "core.hooksPath="+os.DevNull, "rebase", "-q", "origin/agent-1")

	// Documented limitation: deleting a branch needs a packed-refs.lock in the
	// read-only .git root, so it fails cleanly (a clear error) and leaves the ref
	// in place — no corruption. git gc / pack-refs, fetch --prune and shallow
	// fetch share this limitation, as on the Apple layout.
	if out, err := git(wt, "branch", "-D", "tmp"); err == nil {
		t.Errorf("branch deletion should fail cleanly under the read-only .git root, got success:\n%s", out)
	}
	if out, err := git(wt, "rev-parse", "--verify", "-q", "refs/heads/tmp"); err != nil {
		t.Errorf("the branch ref must remain intact after the clean failure, got: %v\n%s", err, out)
	}
}

// On the Apple runtime the agent reads the repository .git/config directly (no
// redacted private copy), so a config carrying credentials is refused at start,
// with the refusal naming only the sections, never the values. A clean config
// starts normally.
func TestAppleRepoConfigWithSecrets_RefusedAtStart(t *testing.T) {
	cases := []struct {
		name    string
		config  string
		refused bool
		section string
	}{
		{"http extraheader", "[http \"https://host.example\"]\n\textraheader = Authorization: Basic QUJD\n", true, "http"},
		{"userinfo in remote url", "[remote \"origin\"]\n\turl = https://alice:s3cret@host.example/o/r.git\n", true, "remote"},
		{"credential entry", "[credential]\n\thelper = store\n", true, "credential"},
		{"clean config", "[core]\n\trepositoryformatversion = 0\n\tfilemode = true\n", false, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repoRoot, workspace := setupHubManagedBaseRepo(t, "agent-1")
			if err := os.WriteFile(filepath.Join(repoRoot, ".git", "config"), []byte(tc.config), 0o644); err != nil {
				t.Fatal(err)
			}
			_, err := buildCommonRunArgs(RunConfig{
				Harness:      &harness.Generic{},
				Name:         "test-agent",
				UnixUsername: "scion",
				Image:        "scion-agent:latest",
				RuntimeName:  appleContainerRuntimeName,
				RepoRoot:     repoRoot,
				Workspace:    workspace,
			})
			if tc.refused {
				if err == nil {
					t.Fatalf("expected the start to be refused for a config with %s secrets", tc.section)
				}
				if !strings.Contains(err.Error(), "holds credentials") || !strings.Contains(err.Error(), tc.section) {
					t.Errorf("refusal should name the section %q and no value, got: %v", tc.section, err)
				}
				// The refusal must not echo a secret value.
				for _, secret := range []string{"s3cret", "QUJD", "store", "alice:"} {
					if strings.Contains(err.Error(), secret) {
						t.Errorf("refusal leaked a secret value %q: %v", secret, err)
					}
				}
			} else if err != nil {
				t.Errorf("a clean config must start, got: %v", err)
			}
		})
	}
}

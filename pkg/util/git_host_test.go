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
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
)

// hostGitTestEnv isolates git from the host's global and system config and
// clears the agent-container guard, so CreateWorktree runs.
func hostGitTestEnv(t *testing.T) string {
	t.Helper()
	t.Setenv("SCION_HOST_UID", "")
	global := filepath.Join(t.TempDir(), "gitconfig")
	if err := os.WriteFile(global, []byte("[user]\n\temail = you@example.com\n\tname = Your Name\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", global)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	return global
}

func hostRunGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
	}
	return strings.TrimSpace(string(out))
}

// plantRepoCommands configures, in the repository at repo, every hook git
// runs for the host-side commands under test, a repository hooks path, a
// file-system monitor and a filter driver applied to every file, each
// creating a marker file in markers when it runs.
func plantRepoCommands(t *testing.T, repo, markers string) {
	t.Helper()
	hookDir := filepath.Join(markers, "hooks")
	if err := os.MkdirAll(hookDir, 0o755); err != nil {
		t.Fatal(err)
	}
	gitDir := hostRunGit(t, repo, "rev-parse", "--absolute-git-dir")
	for _, dir := range []string{filepath.Join(gitDir, "hooks"), hookDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		for _, hook := range []string{"post-checkout", "reference-transaction", "post-merge", "pre-commit", "post-index-change"} {
			script := "#!/bin/sh\ntouch '" + filepath.Join(markers, "hook-"+hook) + "'\n"
			if err := os.WriteFile(filepath.Join(dir, hook), []byte(script), 0o755); err != nil {
				t.Fatal(err)
			}
		}
	}
	hostRunGit(t, repo, "config", "core.hooksPath", hookDir)
	hostRunGit(t, repo, "config", "core.fsmonitor", "touch '"+filepath.Join(markers, "fsmonitor")+"'; false")
	hostRunGit(t, repo, "config", "filter.planted.smudge", "touch '"+filepath.Join(markers, "smudge")+"'; cat")
	hostRunGit(t, repo, "config", "filter.planted.clean", "touch '"+filepath.Join(markers, "clean")+"'; cat")
	hostRunGit(t, repo, "config", "filter.planted.required", "true")
	if err := os.MkdirAll(filepath.Join(gitDir, "info"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(gitDir, "info", "attributes"), []byte("* filter=planted\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func assertNoMarkers(t *testing.T, markers string) {
	t.Helper()
	entries, err := os.ReadDir(markers)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() != "hooks" {
			t.Errorf("repository-defined command ran: %s", e.Name())
		}
	}
}

// newHostGitRepo returns a repository with one committed file, and a
// marker directory outside it.
func newHostGitRepo(t *testing.T) (repo, markers string) {
	t.Helper()
	hostGitTestEnv(t)
	repo = filepath.Join(t.TempDir(), "repo")
	hostRunGit(t, filepath.Dir(repo), "init", "-q", "-b", "main", repo)
	if err := os.WriteFile(filepath.Join(repo, "file.txt"), []byte("content\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	hostRunGit(t, repo, "add", "file.txt")
	hostRunGit(t, repo, "commit", "-q", "-m", "initial")
	markers = t.TempDir()
	return repo, markers
}

func TestHostGitArgs(t *testing.T) {
	want := []string{
		"-c", "core.hooksPath=" + os.DevNull,
		"-c", "core.fsmonitor=false",
		"-c", "core.pager=cat",
		"-c", "protocol.ext.allow=never",
	}
	if got := HostGitArgs(); !reflect.DeepEqual(got, want) {
		t.Errorf("HostGitArgs() = %q, want %q", got, want)
	}
}

func TestParseGitConfigList(t *testing.T) {
	out := []byte("global\x00user.name\nA B\x00local\x00filter.My.Driver.smudge\nx\ny\x00local\x00core.flag\x00")
	got := parseGitConfigList(out)
	want := []gitConfigEntry{
		{scope: "global", key: "user.name", value: "A B"},
		{scope: "local", key: "filter.My.Driver.smudge", value: "x\ny"},
		{scope: "local", key: "core.flag", value: ""},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("parseGitConfigList = %#v, want %#v", got, want)
	}
	sec, sub, v := splitConfigKey("filter.My.Driver.smudge")
	if sec != "filter" || sub != "My.Driver" || v != "smudge" {
		t.Errorf("splitConfigKey = %q %q %q", sec, sub, v)
	}
}

// A filter driver the repository defines is replaced by the global
// definition of the same name, or disabled when there is none.
func TestHostGitCommand_FilterDriversFromRepositoryConfig(t *testing.T) {
	repo, _ := newHostGitRepo(t)
	global := os.Getenv("GIT_CONFIG_GLOBAL")
	f, err := os.OpenFile(global, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("[filter \"lfs\"]\n\tsmudge = git-lfs smudge -- %f\n\tprocess = git-lfs filter-process\n\trequired = true\n"); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	hostRunGit(t, repo, "config", "filter.lfs.smudge", "repo-smudge")
	hostRunGit(t, repo, "config", "filter.lfs.clean", "repo-clean")
	hostRunGit(t, repo, "config", "filter.Other.process", "repo-process")

	cmd, err := HostGitCommand(context.Background(), repo, HostGitOptions{}, "status")
	if err != nil {
		t.Fatal(err)
	}
	args := strings.Join(cmd.Args, "\x00")
	for _, want := range []string{
		"filter.Other.clean=", "filter.Other.smudge=", "filter.Other.process=", "filter.Other.required=false",
		"filter.lfs.clean=", "filter.lfs.smudge=git-lfs smudge -- %f", "filter.lfs.process=git-lfs filter-process", "filter.lfs.required=true",
	} {
		if !strings.Contains(args, "\x00"+want+"\x00") {
			t.Errorf("command args lack -c %q: %q", want, cmd.Args)
		}
	}
	if strings.Contains(args, "repo-") {
		t.Errorf("command args carry a repository filter value: %q", cmd.Args)
	}
	if cmd.Args[len(cmd.Args)-1] != "status" {
		t.Errorf("subcommand not last: %q", cmd.Args)
	}
}

func TestHostGitCommand_NetworkRefusesRepositoryKeys(t *testing.T) {
	for _, kv := range [][2]string{
		{"url.https://elsewhere.example/.insteadOf", "https://github.com/"},
		{"url.https://elsewhere.example/.pushInsteadOf", "https://github.com/"},
		{"credential.helper", "store"},
		{"credential.https://github.com.username", "x"},
		{"core.sshCommand", "ssh -v"},
		{"core.askPass", "true"},
		{"core.gitProxy", "true"},
		{"core.worktree", "/tmp"},
		{"remote.origin.uploadpack", "git-upload-pack"},
		{"remote.origin.receivepack", "git-receive-pack"},
		{"remote.origin.proxy", "http://proxy.example"},
		{"include.path", "/dev/null"},
		{"includeIf.gitdir:/.path", "/dev/null"},
		{"protocol.allow", "always"},
		{"http.proxy", "http://proxy.example"},
		{"http.https://github.com.proxy", "http://proxy.example"},
		{"gpg.program", "true"},
	} {
		t.Run(kv[0], func(t *testing.T) {
			repo, _ := newHostGitRepo(t)
			hostRunGit(t, repo, "config", kv[0], kv[1])
			_, err := HostGitCommand(context.Background(), repo, HostGitOptions{Network: true}, "fetch")
			var unsafe *UnsafeRepoConfigError
			if !errors.As(err, &unsafe) {
				t.Fatalf("HostGitCommand(Network) error = %v, want *UnsafeRepoConfigError", err)
			}
			if !strings.EqualFold(unsafe.Key, kv[0]) || unsafe.Scope != "local" {
				t.Errorf("refused key %q scope %q, want %q local", unsafe.Key, unsafe.Scope, kv[0])
			}
			// The same key is not refused for a local command.
			if _, err := HostGitCommand(context.Background(), repo, HostGitOptions{}, "status"); err != nil {
				t.Errorf("HostGitCommand(local) error = %v", err)
			}
		})
	}
}

// Keys in the host user's global config are trusted.
func TestHostGitCommand_NetworkAllowsGlobalKeys(t *testing.T) {
	repo, _ := newHostGitRepo(t)
	global := os.Getenv("GIT_CONFIG_GLOBAL")
	hostRunGit(t, repo, "config", "--file", global, "url.https://mirror.example/.insteadOf", "https://github.com/")
	hostRunGit(t, repo, "config", "--file", global, "credential.helper", "store")
	cmd, err := HostGitCommand(context.Background(), repo, HostGitOptions{Network: true}, "fetch")
	if err != nil {
		t.Fatalf("HostGitCommand: %v", err)
	}
	for _, want := range []string{"fetch.recurseSubmodules=false", "submodule.recurse=false", "merge.verifySignatures=false"} {
		if !slices.Contains(cmd.Args, want) {
			t.Errorf("network command lacks -c %s: %q", want, cmd.Args)
		}
	}
}

func TestHostGitCommand_RequiresDir(t *testing.T) {
	if _, err := HostGitCommand(context.Background(), "", HostGitOptions{}, "status"); err == nil {
		t.Error("expected an error for an empty dir")
	}
}

func TestCreateWorktree_RunsNoRepositoryCommands(t *testing.T) {
	repo, markers := newHostGitRepo(t)
	plantRepoCommands(t, repo, markers)

	wt := filepath.Join(repo, "agents", "a", "workspace")
	if err := os.MkdirAll(filepath.Dir(wt), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := CreateWorktree(wt, "agent-a"); err != nil {
		t.Fatalf("CreateWorktree: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(wt, "file.txt"))
	if err != nil || string(got) != "content\n" {
		t.Fatalf("worktree file = %q, %v", got, err)
	}
	// The branch-exists path runs the second worktree add.
	if _, err := RemoveWorktree(filepath.Dir(wt), wt, repo, false); err != nil {
		t.Fatalf("RemoveWorktree: %v", err)
	}
	if err := CreateWorktree(wt, "agent-a"); err != nil {
		t.Fatalf("CreateWorktree (existing branch): %v", err)
	}
	assertNoMarkers(t, markers)
}

func TestDeleteBranchIn_RunsNoRepositoryHooks(t *testing.T) {
	repo, markers := newHostGitRepo(t)
	hostRunGit(t, repo, "branch", "doomed")
	plantRepoCommands(t, repo, markers)
	if !DeleteBranchIn(repo, "doomed") {
		t.Fatal("DeleteBranchIn returned false")
	}
	if BranchExistsIn(repo, "doomed") {
		t.Error("branch still exists")
	}
	assertNoMarkers(t, markers)
}

// RemoveWorktree acts on the repository the caller names, not on the one
// the worktree's .git file points at.
func TestRemoveWorktree_IgnoresWorktreeGitFile(t *testing.T) {
	repo, markers := newHostGitRepo(t)
	agentDir := filepath.Join(repo, "agents", "a")
	wt := filepath.Join(agentDir, "workspace")
	if err := os.MkdirAll(agentDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := CreateWorktree(wt, "agent-a"); err != nil {
		t.Fatalf("CreateWorktree: %v", err)
	}

	other, _ := newHostGitRepo(t)
	hostRunGit(t, other, "branch", "agent-a")
	hostRunGit(t, other, "worktree", "add", "-q", filepath.Join(t.TempDir(), "o"), "-b", "other-wt")
	plantRepoCommands(t, other, markers)
	otherAdmin := filepath.Join(other, ".git", "worktrees", "o")
	if err := os.WriteFile(filepath.Join(wt, ".git"), []byte("gitdir: "+otherAdmin+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	deleted, err := RemoveWorktree(agentDir, wt, repo, true)
	if err != nil {
		t.Fatalf("RemoveWorktree: %v", err)
	}
	if !deleted {
		t.Error("expected the worktree's branch to be deleted")
	}
	if BranchExistsIn(repo, "agent-a") {
		t.Error("branch agent-a still exists in the caller's repository")
	}
	if !BranchExistsIn(other, "agent-a") || !BranchExistsIn(other, "other-wt") {
		t.Error("a branch was deleted in the repository the .git file names")
	}
	assertNoMarkers(t, markers)
}

// With no repository given, RemoveWorktree removes the directory only.
func TestRemoveWorktree_NoRepoRootLeavesGitAlone(t *testing.T) {
	repo, _ := newHostGitRepo(t)
	agentDir := filepath.Join(repo, "agents", "a")
	wt := filepath.Join(agentDir, "workspace")
	if err := os.MkdirAll(agentDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := CreateWorktree(wt, "agent-a"); err != nil {
		t.Fatal(err)
	}
	deleted, err := RemoveWorktree(agentDir, wt, "", true)
	if err != nil || deleted {
		t.Fatalf("RemoveWorktree = %v, %v", deleted, err)
	}
	if _, err := os.Stat(wt); !os.IsNotExist(err) {
		t.Error("worktree directory still exists")
	}
	if !BranchExistsIn(repo, "agent-a") {
		t.Error("branch deleted without a repository")
	}
}

// sharedWorkspaceFixture returns a source repository and a shared
// workspace cloned from it.
func sharedWorkspaceFixture(t *testing.T) (source, workspace, markers string) {
	t.Helper()
	source, markers = newHostGitRepo(t)
	workspace = filepath.Join(t.TempDir(), "workspace")
	if err := CloneSharedWorkspace(workspace, source, "", ""); err != nil {
		t.Fatalf("CloneSharedWorkspace: %v", err)
	}
	return source, workspace, markers
}

func commitFile(t *testing.T, repo, name, content, msg string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(repo, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	hostRunGit(t, repo, "add", name)
	hostRunGit(t, repo, "-c", "core.hooksPath="+os.DevNull, "commit", "-q", "-m", msg)
}

func TestPullSharedWorkspace_RunsNoRepositoryCommands(t *testing.T) {
	source, workspace, markers := sharedWorkspaceFixture(t)
	plantRepoCommands(t, workspace, markers)
	commitFile(t, source, "new.txt", "new\n", "add new")

	result, err := PullSharedWorkspace(workspace, source, "")
	if err != nil {
		t.Fatalf("PullSharedWorkspace: %v", err)
	}
	if !result.Updated || len(result.Commits) != 1 || result.Commits[0].Subject != "add new" {
		t.Errorf("result = %+v", result)
	}
	if got, err := os.ReadFile(filepath.Join(workspace, "new.txt")); err != nil || string(got) != "new\n" {
		t.Errorf("new.txt = %q, %v", got, err)
	}
	assertNoMarkers(t, markers)
}

// The pull uses the clone URL the hub passes, not the workspace's remote.
func TestPullSharedWorkspace_UsesCloneURL(t *testing.T) {
	source, workspace, _ := sharedWorkspaceFixture(t)
	other, _ := newHostGitRepo(t)
	commitFile(t, other, "other.txt", "other\n", "from other")
	hostRunGit(t, workspace, "remote", "set-url", "origin", other)
	commitFile(t, source, "new.txt", "new\n", "from source")

	result, err := PullSharedWorkspace(workspace, source, "")
	if err != nil {
		t.Fatalf("PullSharedWorkspace: %v", err)
	}
	if len(result.Commits) != 1 || result.Commits[0].Subject != "from source" {
		t.Errorf("commits = %+v, want the source commit only", result.Commits)
	}
	if _, err := os.Stat(filepath.Join(workspace, "other.txt")); !os.IsNotExist(err) {
		t.Error("pulled from the workspace's configured remote")
	}
	if got := hostRunGit(t, workspace, "rev-parse", "refs/remotes/origin/main"); got != hostRunGit(t, source, "rev-parse", "HEAD") {
		t.Errorf("origin/main = %s, want the source head", got)
	}
}

func TestPullSharedWorkspace_RefusesRepositoryTransportConfig(t *testing.T) {
	source, workspace, _ := sharedWorkspaceFixture(t)
	hostRunGit(t, workspace, "config", "url."+t.TempDir()+".insteadOf", source)
	commitFile(t, source, "new.txt", "new\n", "add new")

	_, err := PullSharedWorkspace(workspace, source, "")
	if err == nil || !strings.Contains(err.Error(), "git pull failed") || !strings.Contains(err.Error(), "insteadof") {
		t.Fatalf("PullSharedWorkspace error = %v, want a refusal naming the key", err)
	}
	if _, err := os.Stat(filepath.Join(workspace, "new.txt")); !os.IsNotExist(err) {
		t.Error("pull ran despite the refusal")
	}
}

func TestPullSharedWorkspace_RequiresCloneURL(t *testing.T) {
	_, workspace, _ := sharedWorkspaceFixture(t)
	if _, err := PullSharedWorkspace(workspace, "", ""); err == nil || !strings.Contains(err.Error(), "git pull failed") {
		t.Fatalf("error = %v, want a pull failure", err)
	}
}

// The upstream branch configured for the current branch is the one pulled.
func TestPullSharedWorkspace_ConfiguredUpstreamBranch(t *testing.T) {
	source, workspace, _ := sharedWorkspaceFixture(t)
	hostRunGit(t, source, "branch", "release")
	hostRunGit(t, workspace, "fetch", "-q", "origin")
	hostRunGit(t, workspace, "checkout", "-q", "-b", "work", "--track", "origin/release")
	hostRunGit(t, source, "checkout", "-q", "release")
	commitFile(t, source, "rel.txt", "rel\n", "release change")

	result, err := PullSharedWorkspace(workspace, source, "")
	if err != nil {
		t.Fatalf("PullSharedWorkspace: %v", err)
	}
	if len(result.Commits) != 1 || result.Commits[0].Subject != "release change" {
		t.Errorf("commits = %+v", result.Commits)
	}
}

// Host-side worktree creation leaves ordinary git use in the worktree
// unchanged: commit, push, fetch and rebase work.
func TestCreateWorktree_WorktreeSupportsCommitPushFetchRebase(t *testing.T) {
	repo, _ := newHostGitRepo(t)
	remote := filepath.Join(t.TempDir(), "remote.git")
	hostRunGit(t, repo, "init", "-q", "--bare", "-b", "main", remote)
	hostRunGit(t, repo, "remote", "add", "origin", remote)
	hostRunGit(t, repo, "push", "-q", "origin", "main")

	wt := filepath.Join(repo, "agents", "a", "workspace")
	if err := os.MkdirAll(filepath.Dir(wt), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := CreateWorktree(wt, "agent-a"); err != nil {
		t.Fatalf("CreateWorktree: %v", err)
	}
	commitFile(t, wt, "a.txt", "a\n", "agent change")
	hostRunGit(t, wt, "push", "-q", "-u", "origin", "agent-a")

	// Move main on the remote, then fetch and rebase onto it.
	other := filepath.Join(t.TempDir(), "other")
	hostRunGit(t, repo, "clone", "-q", remote, other)
	commitFile(t, other, "b.txt", "b\n", "main change")
	hostRunGit(t, other, "push", "-q", "origin", "main")

	hostRunGit(t, wt, "fetch", "-q", "origin")
	hostRunGit(t, wt, "rebase", "-q", "origin/main")
	if got := hostRunGit(t, wt, "log", "--format=%s", "-3"); got != "agent change\nmain change\ninitial" {
		t.Errorf("log after rebase = %q", got)
	}
	hostRunGit(t, wt, "push", "-q", "--force-with-lease", "origin", "agent-a")
	if got := hostRunGit(t, wt, "rev-parse", "@{upstream}"); got != hostRunGit(t, wt, "rev-parse", "HEAD") {
		t.Errorf("upstream %s != HEAD", got)
	}
}

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
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
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

// plantRepoCommands places, in the repository's default .git/hooks
// directory, every hook git runs for the host-side commands under test, each
// touching a marker in markers. These are repository content at the default
// path (not a config key); HostGitArgs' core.hooksPath=/dev/null pin keeps
// them from running. Config-based vectors (core.hooksPath, core.fsmonitor,
// filter drivers) are covered by the deny-by-default policy tests instead.
func plantRepoCommands(t *testing.T, repo, markers string) {
	t.Helper()
	gitDir := hostRunGit(t, repo, "rev-parse", "--absolute-git-dir")
	hookDir := filepath.Join(gitDir, "hooks")
	if err := os.MkdirAll(hookDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, hook := range []string{"post-checkout", "reference-transaction", "post-merge", "pre-commit", "post-index-change"} {
		script := "#!/bin/sh\ntouch '" + filepath.Join(markers, "hook-"+hook) + "'\n"
		if err := os.WriteFile(filepath.Join(hookDir, hook), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
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
		"-c", "submodule.recurse=false",
		"-c", "fetch.recurseSubmodules=false",
		"-c", "merge.verifySignatures=false",
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

// Filter driver variables the repository sets are replaced by the global
// value of the same variable, or emptied when there is none.
// A standard git-lfs filter in the repository config is allowed, and its
// variables are pinned to the global/system value as defence in depth.
func TestHostGitCommand_FilterPinsStandardLFS(t *testing.T) {
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
	// The repository sets the standard git-lfs commands too (git lfs
	// install --local): allowed by the policy.
	hostRunGit(t, repo, "config", "filter.lfs.smudge", "git-lfs smudge -- %f")
	hostRunGit(t, repo, "config", "filter.lfs.clean", "git-lfs clean -- %f")
	hostRunGit(t, repo, "config", "filter.lfs.required", "true")

	cmd, err := HostGitCommand(context.Background(), repo, HostGitOptions{Policy: RepoPolicyLinked}, "status")
	if err != nil {
		t.Fatalf("HostGitCommand: %v", err)
	}
	args := strings.Join(cmd.Args, "\x00")
	for _, want := range []string{"filter.lfs.clean=", "filter.lfs.smudge=git-lfs smudge -- %f", "filter.lfs.required=true"} {
		if !strings.Contains(args, "\x00"+want+"\x00") {
			t.Errorf("command args lack -c %q: %q", want, cmd.Args)
		}
	}
	if cmd.Args[len(cmd.Args)-1] != "status" {
		t.Errorf("subcommand not last: %q", cmd.Args)
	}
}

// Deny-by-default: a repository config key outside the allow-list (or an
// include directive) refuses the command, whether or not it is a network
// command, and names the key. This covers every finding so far plus an
// arbitrary unknown key.
func TestHostGitCommand_PolicyRefuses(t *testing.T) {
	cases := []struct{ key, value string }{
		// F1: a filter subsection containing '=' and a non-standard filter.
		{"filter.a=b.smudge", "touch /tmp/x; cat"},
		{"filter.custom.smudge", "evil"},
		// F2: signature-verification knobs that could run gpg.program.
		{"gpg.program", "touch /tmp/x"},
		{"gpg.ssh.program", "touch /tmp/x"},
		{"log.showSignature", "true"},
		// F3: http.* TLS / resolve / cookie / cert keys.
		{"http.sslVerify", "false"},
		{"http.sslCAInfo", "/tmp/ca"},
		{"http.curloptResolve", "example.com:443:127.0.0.1"},
		{"http.cookieFile", "/tmp/cookies"},
		{"http.https://github.com.sslCert", "/tmp/cert"},
		{"http.proxy", "http://proxy.example"},
		// Command / redirect keys.
		{"url.https://elsewhere.example/.insteadOf", "https://github.com/"},
		{"url.https://elsewhere.example/.pushInsteadOf", "https://github.com/"},
		{"credential.helper", "store"},
		{"credential.https://github.com.username", "x"},
		{"core.sshCommand", "ssh -v"},
		{"core.pager", "less"},
		{"core.editor", "vi"},
		{"core.hooksPath", "/tmp/hooks"},
		{"core.fsmonitor", "/tmp/fsm"},
		{"core.worktree", "/tmp/elsewhere"},
		{"remote.origin.uploadpack", "git-upload-pack"},
		{"remote.origin.receivepack", "git-receive-pack"},
		{"remote.origin.proxy", "http://proxy.example"},
		{"remote.origin.vcs", "hg"},
		{"alias.co", "checkout"},
		{"diff.external", "/tmp/d"},
		{"diff.x.textconv", "/tmp/t"},
		{"merge.x.driver", "/tmp/m %O %A %B"},
		{"submodule.x.update", "!touch /tmp/x"},
		{"submodule.x.url", "https://evil.example/s"},
		// An arbitrary unknown key proves the default is deny.
		{"totally.unknown.key", "whatever"},
	}
	for _, mode := range []RepoConfigPolicy{RepoPolicyManaged, RepoPolicyLinked} {
		for _, c := range cases {
			for _, network := range []bool{false, true} {
				name := fmt.Sprintf("policy%d/%s/net%v", mode, c.key, network)
				t.Run(name, func(t *testing.T) {
					repo, _ := newHostGitRepo(t)
					hostRunGit(t, repo, "config", c.key, c.value)
					_, err := HostGitCommand(context.Background(), repo, HostGitOptions{Policy: mode, Network: network}, "status")
					var refused *RepoConfigRefusedError
					if !errors.As(err, &refused) {
						t.Fatalf("error = %v, want *RepoConfigRefusedError", err)
					}
					if !strings.EqualFold(refused.Key, c.key) && !strings.HasPrefix(strings.ToLower(refused.Key), strings.ToLower(strings.SplitN(c.key, ".", 2)[0])) {
						t.Errorf("refused key = %q, want %q", refused.Key, c.key)
					}
				})
			}
		}
	}
}

// include/includeIf directives are refused outright.
func TestHostGitCommand_RefusesIncludes(t *testing.T) {
	for _, key := range []string{"include.path", "includeIf.gitdir:/x/.path"} {
		repo, _ := newHostGitRepo(t)
		// Write the directive as data (git config would try to open it).
		gitDir := hostRunGit(t, repo, "rev-parse", "--absolute-git-dir")
		cfg := filepath.Join(gitDir, "config")
		data, err := os.ReadFile(cfg)
		if err != nil {
			t.Fatal(err)
		}
		section, name, _ := strings.Cut(key, ".")
		if strings.Contains(key, ":") {
			// includeIf "<cond>" .path
			cond := strings.TrimSuffix(strings.SplitN(key, ".path", 2)[0], "")
			cond = strings.TrimPrefix(cond, "includeIf.")
			data = append(data, []byte("[includeIf \""+cond+"\"]\n\tpath = /dev/null\n")...)
		} else {
			data = append(data, []byte("["+section+"]\n\t"+name+" = /dev/null\n")...)
		}
		if err := os.WriteFile(cfg, data, 0o644); err != nil {
			t.Fatal(err)
		}
		_, err = HostGitCommand(context.Background(), repo, HostGitOptions{Policy: RepoPolicyLinked}, "status")
		var refused *RepoConfigRefusedError
		if !errors.As(err, &refused) {
			t.Errorf("%s: error = %v, want *RepoConfigRefusedError", key, err)
		}
	}
}

// The managed policy allows only the keys git and Scion write; the linked
// policy allows a typical user repository too.
func TestHostGitCommand_AllowsExpectedKeys(t *testing.T) {
	t.Run("managed", func(t *testing.T) {
		repo, _ := newHostGitRepo(t)
		for _, kv := range [][2]string{
			{"gc.auto", "0"}, {"branch.autoSetupMerge", "false"},
			{"user.name", "Scion"}, {"user.email", "agent@scion.dev"},
			{"extensions.worktreeConfig", "true"},
		} {
			hostRunGit(t, repo, "config", kv[0], kv[1])
		}
		if _, err := HostGitCommand(context.Background(), repo, HostGitOptions{Policy: RepoPolicyManaged}, "status"); err != nil {
			t.Fatalf("managed policy refused a Scion-written config: %v", err)
		}
	})
	t.Run("linked-typical", func(t *testing.T) {
		repo, _ := newHostGitRepo(t)
		for _, kv := range [][2]string{
			{"core.autocrlf", "input"}, {"core.ignorecase", "true"},
			{"remote.origin.url", "https://github.com/org/repo.git"},
			{"remote.origin.fetch", "+refs/heads/*:refs/remotes/origin/*"},
			{"remote.origin.pushurl", "git@github.com:org/repo.git"},
			{"branch.main.remote", "origin"}, {"branch.main.merge", "refs/heads/main"},
			{"branch.main.rebase", "true"},
			{"user.name", "Dev"}, {"user.email", "dev@example.com"},
			{"pull.rebase", "true"}, {"push.default", "simple"},
			{"init.defaultBranch", "main"}, {"color.ui", "auto"},
			{"filter.lfs.clean", "git-lfs clean -- %f"},
			{"filter.lfs.smudge", "git-lfs smudge -- %f"},
			{"filter.lfs.process", "git-lfs filter-process"},
			{"filter.lfs.required", "true"},
			{"submodule.sub.update", "checkout"},
		} {
			hostRunGit(t, repo, "config", kv[0], kv[1])
		}
		if _, err := HostGitCommand(context.Background(), repo, HostGitOptions{Policy: RepoPolicyLinked}, "status"); err != nil {
			t.Fatalf("linked policy refused a typical user config: %v", err)
		}
	})
	t.Run("managed-refuses-linked-only-keys", func(t *testing.T) {
		repo, _ := newHostGitRepo(t)
		hostRunGit(t, repo, "config", "pull.rebase", "true")
		_, err := HostGitCommand(context.Background(), repo, HostGitOptions{Policy: RepoPolicyManaged}, "status")
		var refused *RepoConfigRefusedError
		if !errors.As(err, &refused) {
			t.Fatalf("managed: error = %v, want pull.rebase refused", err)
		}
	})
}

// A repository-scope core.worktree is allowed only when it equals the
// caller's AllowedWorktreePath.
func TestHostGitCommand_CoreWorktreeMatch(t *testing.T) {
	repo, _ := newHostGitRepo(t)
	wt := repo // an existing directory git can treat as the work tree
	hostRunGit(t, repo, "config", "core.worktree", wt)
	if _, err := HostGitCommand(context.Background(), repo, HostGitOptions{Policy: RepoPolicyLinked, AllowedWorktreePath: wt}, "status"); err != nil {
		t.Errorf("matching core.worktree refused: %v", err)
	}
	_, err := HostGitCommand(context.Background(), repo, HostGitOptions{Policy: RepoPolicyLinked, AllowedWorktreePath: "/other"}, "status")
	var refused *RepoConfigRefusedError
	if !errors.As(err, &refused) {
		t.Errorf("mismatched core.worktree: error = %v, want refusal", err)
	}
}

// Keys in the host user's global config are not subject to the policy.
func TestHostGitCommand_GlobalKeysNotChecked(t *testing.T) {
	repo, _ := newHostGitRepo(t)
	global := os.Getenv("GIT_CONFIG_GLOBAL")
	hostRunGit(t, repo, "config", "--file", global, "url.https://mirror.example/.insteadOf", "https://github.com/")
	hostRunGit(t, repo, "config", "--file", global, "credential.helper", "store")
	hostRunGit(t, repo, "config", "--file", global, "core.sshCommand", "ssh -v")
	if _, err := HostGitCommand(context.Background(), repo, HostGitOptions{Policy: RepoPolicyManaged, Network: true}, "fetch"); err != nil {
		t.Fatalf("global keys triggered a refusal: %v", err)
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

// With no repository given, RemoveWorktree uses the repository the .git
// file names only when that repository's registry lists the worktree.
func TestRemoveWorktree_NoRepoRootRequiresRegisteredWorktree(t *testing.T) {
	t.Run("registered", func(t *testing.T) {
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
		if err != nil || !deleted {
			t.Fatalf("RemoveWorktree = %v, %v; want the branch deleted", deleted, err)
		}
		if BranchExistsIn(repo, "agent-a") {
			t.Error("branch agent-a still exists")
		}
	})
	t.Run("redirected", func(t *testing.T) {
		repo, markers := newHostGitRepo(t)
		agentDir := filepath.Join(repo, "agents", "a")
		wt := filepath.Join(agentDir, "workspace")
		if err := os.MkdirAll(agentDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := CreateWorktree(wt, "agent-a"); err != nil {
			t.Fatal(err)
		}
		other, _ := newHostGitRepo(t)
		hostRunGit(t, other, "worktree", "add", "-q", filepath.Join(t.TempDir(), "o"), "-b", "agent-a")
		plantRepoCommands(t, other, markers)
		otherAdmin := filepath.Join(other, ".git", "worktrees", "o")
		if err := os.WriteFile(filepath.Join(wt, ".git"), []byte("gitdir: "+otherAdmin+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		deleted, err := RemoveWorktree(agentDir, wt, "", true)
		if err != nil || deleted {
			t.Fatalf("RemoveWorktree = %v, %v; want no branch deleted", deleted, err)
		}
		if _, err := os.Stat(wt); !os.IsNotExist(err) {
			t.Error("worktree directory still exists")
		}
		if !BranchExistsIn(other, "agent-a") {
			t.Error("a branch was deleted in the repository the .git file names")
		}
		assertNoMarkers(t, markers)
	})
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

// Every command HostGitCommand builds, network or not, starts with
// HostGitArgs, so work-tree writes never recurse into submodules.
func TestHostGitCommand_CarriesFixedArgs(t *testing.T) {
	repo, _ := newHostGitRepo(t)
	for _, network := range []bool{false, true} {
		cmd, err := HostGitCommand(context.Background(), repo, HostGitOptions{Network: network}, "merge", "--ff-only", "HEAD")
		if err != nil {
			t.Fatal(err)
		}
		fixed := HostGitArgs()
		if !reflect.DeepEqual(cmd.Args[1:1+len(fixed)], fixed) {
			t.Errorf("args = %q, want them to start with %q", cmd.Args, fixed)
		}
	}
}

// allowVar unit coverage: the managed and linked allow-lists, and the value
// checks for core.worktree, filter.lfs.*, submodule.*.update and *.program.
func TestAllowVar(t *testing.T) {
	v := func(key, value string) gitConfigVar {
		sec, sub, name := splitConfigKey(key)
		return gitConfigVar{Section: strings.ToLower(sec), Subsection: sub, Name: strings.ToLower(name), Value: value}
	}
	managedOK := []string{"core.repositoryformatversion", "core.filemode", "extensions.worktreeConfig", "remote.origin.url", "remote.origin.fetch", "branch.main.remote", "branch.main.merge", "gc.auto", "user.name"}
	for _, k := range managedOK {
		if ok, _ := allowVar(RepoPolicyManaged, v(k, "x"), repoConfigAllowOptions{}); !ok {
			t.Errorf("managed allowVar(%q) = false, want true", k)
		}
	}
	linkedOnly := []string{"core.autocrlf", "pull.rebase", "push.default", "remote.origin.pushurl", "color.ui", "init.defaultBranch"}
	for _, k := range linkedOnly {
		if ok, _ := allowVar(RepoPolicyManaged, v(k, "x"), repoConfigAllowOptions{}); ok {
			t.Errorf("managed allowVar(%q) = true, want false", k)
		}
		if ok, reason := allowVar(RepoPolicyLinked, v(k, "x"), repoConfigAllowOptions{}); !ok {
			t.Errorf("linked allowVar(%q) = false (%s), want true", k, reason)
		}
	}
	deny := []string{"core.sshCommand", "core.pager", "core.editor", "core.hooksPath", "core.fsmonitor", "credential.helper", "url.x.insteadOf", "http.proxy", "http.sslVerify", "gpg.program", "gpg.ssh.program", "alias.st", "remote.origin.uploadpack", "diff.external", "diff.x.textconv", "merge.x.driver", "totally.unknown.key"}
	for _, mode := range []RepoConfigPolicy{RepoPolicyManaged, RepoPolicyLinked} {
		for _, k := range deny {
			if ok, _ := allowVar(mode, v(k, "x"), repoConfigAllowOptions{}); ok {
				t.Errorf("policy%d allowVar(%q) = true, want false", mode, k)
			}
		}
	}
	// core.worktree only with a matching AllowedWorktreePath.
	if ok, _ := allowVar(RepoPolicyLinked, v("core.worktree", "/wt"), repoConfigAllowOptions{worktreePath: "/wt"}); !ok {
		t.Error("core.worktree matching path: want allowed")
	}
	if ok, _ := allowVar(RepoPolicyLinked, v("core.worktree", "/wt"), repoConfigAllowOptions{worktreePath: "/other"}); ok {
		t.Error("core.worktree mismatched path: want refused")
	}
	// filter.lfs.* only when standard.
	if ok, _ := allowVar(RepoPolicyLinked, v("filter.lfs.smudge", "git-lfs smudge -- %f"), repoConfigAllowOptions{}); !ok {
		t.Error("standard filter.lfs.smudge: want allowed")
	}
	if ok, _ := allowVar(RepoPolicyLinked, v("filter.lfs.smudge", "evil"), repoConfigAllowOptions{}); ok {
		t.Error("non-standard filter.lfs.smudge: want refused")
	}
	// submodule.*.update only for none|checkout|rebase|merge.
	if ok, _ := allowVar(RepoPolicyLinked, v("submodule.s.update", "rebase"), repoConfigAllowOptions{}); !ok {
		t.Error("submodule update rebase: want allowed")
	}
	if ok, _ := allowVar(RepoPolicyLinked, v("submodule.s.update", "!cmd"), repoConfigAllowOptions{}); ok {
		t.Error("submodule update !cmd: want refused")
	}
}

func TestHostGitEnv(t *testing.T) {
	in := []string{
		"HOME=/h", "PATH=/bin", "GIT_DIR=/x", "GIT_WORK_TREE=/y", "GIT_COMMON_DIR=/z",
		"GIT_INDEX_FILE=/i", "GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=core.pager", "GIT_CONFIG_VALUE_0=x",
		"GIT_CONFIG_PARAMETERS='a.b=c'", "GIT_EXEC_PATH=/e", "GIT_EDITOR=vi", "GIT_PAGER=less",
		"GIT_CONFIG_GLOBAL=/g", "GIT_CONFIG_NOSYSTEM=1", "GIT_SSH_COMMAND=ssh", "GIT_TERMINAL_PROMPT=0",
	}
	want := []string{"HOME=/h", "PATH=/bin", "GIT_CONFIG_GLOBAL=/g", "GIT_CONFIG_NOSYSTEM=1", "GIT_SSH_COMMAND=ssh", "GIT_TERMINAL_PROMPT=0"}
	if got := HostGitEnv(in); !reflect.DeepEqual(got, want) {
		t.Errorf("HostGitEnv = %q, want %q", got, want)
	}
}

// A change to the repository's filter drivers while the command runs is
// reported after it.
func TestHostGitCmd_DetectsFilterConfigChange(t *testing.T) {
	ctx := context.Background()

	t.Run("refused key appears during the command", func(t *testing.T) {
		repo, _ := newHostGitRepo(t)
		cmd, err := HostGitCommand(ctx, repo, HostGitOptions{}, "config", "filter.late.smudge", "x")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := cmd.CombinedOutput(); !errors.Is(err, ErrRepoConfigChanged) {
			t.Errorf("refused key added during the command: error = %v, want ErrRepoConfigChanged", err)
		}
	})

	t.Run("allowed non-filter change is not a change", func(t *testing.T) {
		repo, _ := newHostGitRepo(t)
		cmd, err := HostGitCommand(ctx, repo, HostGitOptions{}, "config", "gc.auto", "0")
		if err != nil {
			t.Fatal(err)
		}
		if err := cmd.Run(); err != nil {
			t.Errorf("non-filter change: error = %v, want nil", err)
		}
	})

	t.Run("network: any repository key change is reported", func(t *testing.T) {
		repo, _ := newHostGitRepo(t)
		cmd, err := HostGitCommand(ctx, repo, HostGitOptions{Network: true}, "config", "user.name", "changed")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := cmd.Output(); !errors.Is(err, ErrRepoConfigChanged) {
			t.Errorf("network: error = %v, want ErrRepoConfigChanged", err)
		}
	})

	t.Run("no change, no error", func(t *testing.T) {
		repo, _ := newHostGitRepo(t)
		cmd, err := HostGitCommand(ctx, repo, HostGitOptions{}, "status", "--porcelain")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := cmd.Output(); err != nil {
			t.Errorf("unchanged: error = %v", err)
		}
	})
}

// With extensions.worktreeConfig on, the main worktree's config.worktree
// (in the common dir) is checked too (D-2).
func TestHostGitCommand_ChecksMainWorktreeConfig(t *testing.T) {
	repo, _ := newHostGitRepo(t)
	hostRunGit(t, repo, "config", "extensions.worktreeConfig", "true")
	gitDir := hostRunGit(t, repo, "rev-parse", "--absolute-git-dir")
	// A refused key placed in the main worktree's config.worktree.
	if err := os.WriteFile(filepath.Join(gitDir, "config.worktree"), []byte("[core]\n\tsshCommand = ssh -v\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := HostGitCommand(context.Background(), repo, HostGitOptions{Policy: RepoPolicyLinked}, "status")
	var refused *RepoConfigRefusedError
	if !errors.As(err, &refused) || !strings.EqualFold(refused.Key, "core.sshCommand") {
		t.Fatalf("error = %v, want core.sshCommand refused from config.worktree", err)
	}
}

// A remote operation carries the transport allow-list (D-3).
func TestHostGitCommand_NetworkTransportAllowlist(t *testing.T) {
	repo, _ := newHostGitRepo(t)
	cmd, err := HostGitCommand(context.Background(), repo, HostGitOptions{Network: true}, "fetch")
	if err != nil {
		t.Fatal(err)
	}
	args := strings.Join(cmd.Args, "\x00")
	for _, want := range []string{"protocol.allow=never", "protocol.https.allow=always", "protocol.ssh.allow=always"} {
		if !strings.Contains(args, "\x00"+want+"\x00") {
			t.Errorf("network args lack -c %q: %q", want, cmd.Args)
		}
	}
}

// diff/merge driver keys that name a command are refused; data attributes
// are allowed (D-3 explicit keys).
func TestHostGitCommand_DiffMergeExplicitKeys(t *testing.T) {
	allowed := [][2]string{
		{"diff.algorithm", "histogram"},
		{"diff.myd.xfuncname", "^def "},
		{"merge.conflictstyle", "zdiff3"},
		{"merge.mym.name", "my merge"},
	}
	for _, kv := range allowed {
		repo, _ := newHostGitRepo(t)
		hostRunGit(t, repo, "config", kv[0], kv[1])
		if _, err := HostGitCommand(context.Background(), repo, HostGitOptions{Policy: RepoPolicyLinked}, "status"); err != nil {
			t.Errorf("%s=%s refused: %v", kv[0], kv[1], err)
		}
	}
	refused := [][2]string{
		{"diff.external", "/tmp/d"},
		{"diff.myd.textconv", "/tmp/t"},
		{"diff.myd.command", "/tmp/c"},
		{"merge.mym.driver", "/tmp/m %O %A %B"},
		{"merge.tool", "mytool"},
	}
	for _, kv := range refused {
		repo, _ := newHostGitRepo(t)
		hostRunGit(t, repo, "config", kv[0], kv[1])
		_, err := HostGitCommand(context.Background(), repo, HostGitOptions{Policy: RepoPolicyLinked}, "status")
		var r *RepoConfigRefusedError
		if !errors.As(err, &r) {
			t.Errorf("%s=%s: error = %v, want refusal", kv[0], kv[1], err)
		}
	}
}

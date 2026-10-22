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

package agent

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GoogleCloudPlatform/scion/pkg/api"
	"github.com/GoogleCloudPlatform/scion/pkg/config"
)

func TestTrustedProjectRepoRoot(t *testing.T) {
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	setupGitRepo(t, mkdirAllT(t, repo))
	deep := mkdirAllT(t, filepath.Join(repo, "a", "b", ".scion"))
	if got := trustedProjectRepoRoot(deep); got != eval(t, repo) {
		t.Errorf("nested project: got %q, want %q", got, eval(t, repo))
	}

	link := filepath.Join(root, "link")
	if err := os.Symlink(deep, link); err != nil {
		t.Fatal(err)
	}
	if got := trustedProjectRepoRoot(link); got != eval(t, repo) {
		t.Errorf("symlinked project dir: got %q, want %q", got, eval(t, repo))
	}

	// Neither the environment nor the repository configuration is read.
	t.Run("ignores env and core.worktree", func(t *testing.T) {
		t.Setenv("GIT_DIR", filepath.Join(root, "elsewhere"))
		t.Setenv("GIT_WORK_TREE", root)
		gitCfg := filepath.Join(repo, ".git", "config")
		data, _ := os.ReadFile(gitCfg)
		mustWrite(t, gitCfg, string(data)+"[core]\n\tworktree = "+root+"\n")
		if got := trustedProjectRepoRoot(deep); got != eval(t, repo) {
			t.Errorf("with GIT_DIR, GIT_WORK_TREE and core.worktree set: got %q, want %q", got, eval(t, repo))
		}
	})

	gitfile := mkdirAllT(t, filepath.Join(root, "linked", ".scion"))
	mustWrite(t, filepath.Join(root, "linked", ".git"), "gitdir: "+filepath.Join(repo, ".git")+"\n")
	if got := trustedProjectRepoRoot(gitfile); got != "" {
		t.Errorf(".git file: got %q, want \"\"", got)
	}

	withCommondir := filepath.Join(root, "cd")
	setupGitRepo(t, mkdirAllT(t, withCommondir))
	mustWrite(t, filepath.Join(withCommondir, ".git", "commondir"), "../..\n")
	if got := trustedProjectRepoRoot(withCommondir); got != "" {
		t.Errorf(".git with commondir: got %q, want \"\"", got)
	}

	// A directory with no repository at or above it in the temp tree. t.TempDir
	// is not itself a git repo, so the walk reaches the filesystem root and
	// returns "".
	plain := mkdirAllT(t, filepath.Join(t.TempDir(), "plain"))
	if got := trustedProjectRepoRoot(plain); got != "" {
		t.Errorf("no repository: got %q, want \"\"", got)
	}
}

func mkdirAllT(t *testing.T, dir string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

// Provisioning stops when git reports a different repository root than the
// directory walk (here because core.worktree in the project's
// configuration names another directory).
func TestProvision_RefusesWhenRepoRootsDisagree(t *testing.T) {
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	setupGitRepo(t, mkdirAllT(t, repo))
	other := mkdirAllT(t, filepath.Join(root, "other"))
	if out, err := exec.Command("git", "-C", repo, "config", "core.worktree", other).CombinedOutput(); err != nil {
		t.Fatalf("git config: %v\n%s", err, out)
	}
	if _, err := checkProvisionRepoRoot(filepath.Join(repo, ".scion")); err == nil {
		t.Error("checkProvisionRepoRoot: expected an error when git and the walk disagree")
	}
	if got, err := checkProvisionRepoRoot(mkdirAllT(t, filepath.Join(root, "ok", ".scion"))); err == nil {
		t.Errorf("project with no repository: got %q, want an error", got)
	}
}

// An agent provisioned before records were written gets a backfill record
// from the directory walk on its next start.
func TestStart_BackfillsMissingRecord(t *testing.T) {
	f := newRepoRootMetadataFixture(t)
	agentDir := config.GetAgentDir(f.projectScionDir, f.agentName, false)
	if err := os.Remove(filepath.Join(agentDir, provisionedWorktreeStateFile)); err != nil {
		t.Fatal(err)
	}
	cfg := f.start(t)
	if got, want := cfg.RepoRoot, eval(t, f.projectDir); got != want {
		t.Errorf("restart: RepoRoot = %q, want %q", got, want)
	}
	rec, ok := readRepoRootRecord(agentDir)
	if !ok || rec.Source != repoRootSourceBackfill || rec.RepoRoot != eval(t, f.projectDir) {
		t.Errorf("record after backfill = %+v (ok=%v), want a backfill record", rec, ok)
	}
}

// A record written by an older version (no source) is not used; the
// per-agent workspace gets a backfill instead.
func TestStart_LegacyRecordNotUsed(t *testing.T) {
	f := newRepoRootMetadataFixture(t)
	agentDir := config.GetAgentDir(f.projectScionDir, f.agentName, false)
	mustWrite(t, filepath.Join(agentDir, provisionedWorktreeStateFile), `{"repoRoot":"`+eval(t, f.otherRepo)+`"}`)
	if _, ok := readRepoRootRecord(agentDir); ok {
		t.Fatal("a record without a source was accepted")
	}
	cfg := f.start(t)
	if got, want := cfg.RepoRoot, eval(t, f.projectDir); got != want {
		t.Errorf("restart: RepoRoot = %q, want the backfilled %q", got, want)
	}
}

func TestResolveStartRepoRoot_Conditions(t *testing.T) {
	t.Setenv("SCION_HOST_UID", "")
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	setupGitRepo(t, mkdirAllT(t, repo))
	projectDir := mkdirAllT(t, filepath.Join(repo, ".scion"))
	agentDir := mkdirAllT(t, filepath.Join(projectDir, "agents", "a"))
	ws := mkdirAllT(t, filepath.Join(agentDir, "workspace"))
	want := eval(t, repo)

	write := func(source string) {
		t.Helper()
		if err := writeRepoRootRecord(agentDir, want, source, nil); err != nil {
			t.Fatal(err)
		}
	}
	in := startRepoRootInput{agentName: "a", agentDir: agentDir, projectDir: projectDir, workspace: ws}

	write(repoRootSourceProvision)
	if got, _ := resolveStartRepoRoot(in); got != want {
		t.Errorf("provision record: got %q, want %q", got, want)
	}

	explicit := in
	explicit.explicit = true
	if got, _ := resolveStartRepoRoot(explicit); got != "" {
		t.Errorf("explicit start with a provision record: got %q, want \"\"", got)
	}

	mounted := in
	mounted.volumes = []api.VolumeMount{{Source: filepath.Join(projectDir, "agents"), Target: "/mnt/agents"}}
	if got, _ := resolveStartRepoRoot(mounted); got != "" {
		t.Errorf("agentDir inside a configured volume: got %q, want \"\"", got)
	}
	if err := writeRepoRootRecord(agentDir, want, repoRootSourceProvision, agentMountSources(ws, want, mounted.volumes)); err != errAgentDirMounted {
		t.Errorf("writeRepoRootRecord with agentDir mounted: err = %v, want errAgentDirMounted", err)
	}

	if err := os.Remove(filepath.Join(agentDir, provisionedWorktreeStateFile)); err != nil {
		t.Fatal(err)
	}
	elsewhere := in
	elsewhere.workspace = mkdirAllT(t, filepath.Join(root, "external-worktree"))
	if got, _ := resolveStartRepoRoot(elsewhere); got != "" {
		t.Errorf("no record, workspace outside the per-agent directory: got %q, want \"\"", got)
	}
	if _, ok := readRepoRootRecord(agentDir); ok {
		t.Error("a backfill record was written for a workspace outside the per-agent directory")
	}
	if got, _ := resolveStartRepoRoot(mounted); got != "" {
		t.Errorf("no record, agentDir inside a configured volume: got %q, want \"\"", got)
	}
	if _, ok := readRepoRootRecord(agentDir); ok {
		t.Error("a backfill record was written with agentDir inside a configured volume")
	}
}

// Behaviour change: a project whose checkout is a linked worktree (its
// top-level .git is a gitfile, not a directory) is refused at provision,
// because the repository root cannot be identified without reading git
// metadata the container could edit. detectRepoRoot used to accept it.
func TestProvision_RefusesLinkedWorktreeProject(t *testing.T) {
	t.Setenv("SCION_HOST_UID", "")
	root := t.TempDir()
	base := filepath.Join(root, "base")
	setupGitRepo(t, mkdirAllT(t, base))
	linked := filepath.Join(root, "linked")
	if out, err := exec.Command("git", "-C", base, "worktree", "add", "-b", "feature", linked).CombinedOutput(); err != nil {
		t.Fatalf("git worktree add: %v\n%s", err, out)
	}
	// <linked>/.git is a gitfile, so trustedProjectRepoRoot stops and the
	// cross-check refuses.
	_, err := checkProvisionRepoRoot(filepath.Join(linked, ".scion"))
	if err == nil {
		t.Fatal("checkProvisionRepoRoot: expected a refusal for a linked-worktree checkout")
	}
	for _, want := range []string{"interim", "main checkout", "submodule"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal message %q does not mention %q", err.Error(), want)
		}
	}
	if got := trustedProjectRepoRoot(filepath.Join(linked, ".scion")); got != "" {
		t.Errorf("trustedProjectRepoRoot of a linked worktree = %q, want \"\"", got)
	}
}

// An explicit workspace inside the project's .scion/agents directory is
// refused at provision; other locations under .scion (the project's own
// .scion, and the hub-native base's .scion/workspace) are allowed.
func TestRefuseWorkspaceInAgentsDir(t *testing.T) {
	root := t.TempDir()
	scion := mkdirAllT(t, filepath.Join(root, "proj", ".scion"))
	agents := mkdirAllT(t, filepath.Join(scion, "agents"))
	agentDir := mkdirAllT(t, filepath.Join(agents, "a"))
	wtWorkspace := mkdirAllT(t, filepath.Join(scion, "workspace", "worktrees", "a"))
	outside := mkdirAllT(t, filepath.Join(root, "proj", "work"))
	// Refused: the .scion directory itself, the agents tree, and below it.
	for _, ws := range []string{scion, agents, agentDir} {
		if err := refuseWorkspaceInAgentsDir(scion, ws); err == nil {
			t.Errorf("refuseWorkspaceInAgentsDir(%q): want a refusal", ws)
		}
	}
	// A relative ".scion" resolves to the same directory (the Case 1 relative
	// route), and is refused the same way.
	if err := refuseWorkspaceInAgentsDir(filepath.Dir(scion), scion); err == nil {
		t.Error("refuseWorkspaceInAgentsDir(projectRoot, .scion): want a refusal")
	}
	// Allowed: the hub-native .scion/workspace worktree, and a workspace
	// outside .scion entirely.
	for _, ws := range []string{wtWorkspace, outside} {
		if err := refuseWorkspaceInAgentsDir(scion, ws); err != nil {
			t.Errorf("refuseWorkspaceInAgentsDir(%q) = %v, want nil", ws, err)
		}
	}
	// The project directory given as the parent (the project root, not
	// .scion) is handled too.
	if err := refuseWorkspaceInAgentsDir(filepath.Dir(scion), agentDir); err == nil {
		t.Error("refuseWorkspaceInAgentsDir(projectRoot, agentDir): want a refusal")
	}
	// A symlink into the agents tree is caught (both sides resolve).
	link := filepath.Join(root, "link-to-agents")
	if err := os.Symlink(agentDir, link); err != nil {
		t.Fatal(err)
	}
	if err := refuseWorkspaceInAgentsDir(scion, link); err == nil {
		t.Error("refuseWorkspaceInAgentsDir(symlink into agents): want a refusal")
	}

	// A project whose .scion directory is itself reached through a symlink
	// (the projectDir passed in is named .scion but is a link): both the
	// ==.scion and the agents-tree checks still fire (both sides resolve).
	realProj := mkdirAllT(t, filepath.Join(root, "real", ".scion"))
	realAgentDir := mkdirAllT(t, filepath.Join(realProj, "agents", "x"))
	linkParent := mkdirAllT(t, filepath.Join(root, "linked"))
	scionLink := filepath.Join(linkParent, ".scion")
	if err := os.Symlink(realProj, scionLink); err != nil {
		t.Fatal(err)
	}
	if err := refuseWorkspaceInAgentsDir(scionLink, scionLink); err == nil {
		t.Error("refuseWorkspaceInAgentsDir(symlinked .scion, itself): want a refusal")
	}
	agentsLink := filepath.Join(root, "link-to-real-agent")
	if err := os.Symlink(realAgentDir, agentsLink); err != nil {
		t.Fatal(err)
	}
	if err := refuseWorkspaceInAgentsDir(scionLink, agentsLink); err == nil {
		t.Error("refuseWorkspaceInAgentsDir(symlinked .scion, symlinked agent dir): want a refusal")
	}
}

// A provision record naming another repository is not used (its root does
// not match the trusted walk from the project), so the resolved RepoRoot
// is empty.
func TestResolveStartRepoRoot_RejectsRecordNotMatchingProject(t *testing.T) {
	t.Setenv("SCION_HOST_UID", "")
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	setupGitRepo(t, mkdirAllT(t, repo))
	other := filepath.Join(root, "other")
	setupGitRepo(t, mkdirAllT(t, other))
	projectDir := mkdirAllT(t, filepath.Join(repo, ".scion"))
	agentDir := mkdirAllT(t, filepath.Join(projectDir, "agents", "a"))
	ws := mkdirAllT(t, filepath.Join(agentDir, "workspace"))

	// A provision record naming another repository, which is not an ancestor
	// of the per-agent workspace, so the mount check passes.
	if err := writeRepoRootRecord(agentDir, eval(t, other), repoRootSourceProvision, nil); err != nil {
		t.Fatal(err)
	}
	in := startRepoRootInput{agentName: "a", agentDir: agentDir, projectDir: projectDir, workspace: ws}
	got, _ := resolveStartRepoRoot(in)
	if got != "" {
		t.Errorf("resolveStartRepoRoot = %q, want \"\" (a record whose root does not match the project is not used)", got)
	}
}

// The provision refusal is root-only. A project whose root is a normal
// repository still provisions even when it contains a submodule (whose own
// .git is a gitfile), because trustedProjectRepoRoot walks up from the
// project directory and stops at the root's .git directory, above any
// contained submodule.
func TestProvision_ContainingSubmoduleStillProvisions(t *testing.T) {
	t.Setenv("SCION_HOST_UID", "")
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	setupGitRepo(t, mkdirAllT(t, repo))
	// A submodule-like nested checkout: a directory whose .git is a gitfile.
	sub := mkdirAllT(t, filepath.Join(repo, "vendor", "sub"))
	mustWrite(t, filepath.Join(sub, ".git"), "gitdir: "+filepath.Join(repo, ".git", "modules", "sub")+"\n")
	projectDir := mkdirAllT(t, filepath.Join(repo, ".scion"))

	got, err := checkProvisionRepoRoot(projectDir)
	if err != nil {
		t.Fatalf("checkProvisionRepoRoot refused a project that merely contains a submodule: %v", err)
	}
	if got != eval(t, repo) {
		t.Errorf("checkProvisionRepoRoot = %q, want the project root %q", got, eval(t, repo))
	}
}

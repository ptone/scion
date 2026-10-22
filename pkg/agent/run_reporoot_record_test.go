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
	t.Setenv("GIT_DIR", filepath.Join(root, "elsewhere"))
	gitCfg := filepath.Join(repo, ".git", "config")
	data, _ := os.ReadFile(gitCfg)
	mustWrite(t, gitCfg, string(data)+"[core]\n\tworktree = "+root+"\n")
	if got := trustedProjectRepoRoot(deep); got != eval(t, repo) {
		t.Errorf("with GIT_DIR and core.worktree set: got %q, want %q", got, eval(t, repo))
	}
	os.Unsetenv("GIT_DIR")

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

	if got := trustedProjectRepoRoot(mkdirAllT(t, filepath.Join(root, "plain"))); got != "" && got != eval(t, root) {
		t.Errorf("no repository: got %q", got)
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

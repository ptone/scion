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

package runtimebroker

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GoogleCloudPlatform/scion/pkg/api"
	"github.com/GoogleCloudPlatform/scion/pkg/config"
	"github.com/GoogleCloudPlatform/scion/pkg/runtime"
)

// A failed Start on a preserveWorkspace create (a hub start routed through
// create-with-gather, ptone/scion#6096) never runs the failed-create file
// cleanup: the agent's existing git workspace, its bytes and its branch are
// kept. Without the flag the same failure removes the agent's files.

const preserveCleanupFile = "unpushed.go"
const preserveCleanupContent = "package main // fake un-pushed change\n"

// gitPreserveT runs git in dir with a fixed identity and no user or system config.
func gitPreserveT(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.email=fake@example.com", "-c", "user.name=fake", "-c", "init.defaultBranch=main"}, args...)...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// seedAgentWorktree makes the project a git repository and the agent's
// workspace a worktree of it on branch "same-name", with an uncommitted
// file in the workspace. It returns the repository root and the workspace.
func seedAgentWorktree(t *testing.T, projectPath, agentDir string) (string, string) {
	t.Helper()
	// The broker's own host git calls read the user's global git config
	// through HOME: an empty HOME keeps the cleanup independent of it.
	t.Setenv("HOME", t.TempDir())
	// Inside an agent container (SCION_HOST_UID set) the cleanup
	// deliberately leaves git's worktree entry alone, so the branch it
	// still has checked out is kept (pkg/util RemoveWorktreeEntryIn). These
	// tests are about the host broker: clear it.
	t.Setenv("SCION_HOST_UID", "")
	repo := filepath.Dir(projectPath)
	gitPreserveT(t, repo, "init", "-q")
	if err := os.WriteFile(filepath.Join(repo, "README"), []byte("fake\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitPreserveT(t, repo, "add", "README")
	gitPreserveT(t, repo, "commit", "-q", "-m", "fake")
	workspace := filepath.Join(agentDir, "workspace")
	if err := os.MkdirAll(agentDir, 0o755); err != nil {
		t.Fatal(err)
	}
	gitPreserveT(t, repo, "worktree", "add", "-q", "-b", "same-name", workspace)
	if err := os.WriteFile(filepath.Join(workspace, preserveCleanupFile), []byte(preserveCleanupContent), 0o644); err != nil {
		t.Fatal(err)
	}
	writeAgentFiles(t, agentDir, "agent-a")
	return repo, workspace
}

func createSyncWithBody(srv *Server, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/api/v1/agents", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)
	return w
}

func TestCreateStartFailure_PreserveWorkspaceKeepsWorkspaceAndBranch(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Fatalf("git is required: %v", err)
	}
	srv, mgr, projectPath, agentDir := newSyncStartTestServer(t)
	repo, workspace := seedAgentWorktree(t, projectPath, agentDir)

	var sawFresh bool
	mgr.starts <- func(_ context.Context, opts api.StartOptions) (*api.AgentInfo, error) {
		sawFresh = opts.FreshProvision
		return nil, errors.New("fake image pull failed")
	}
	w := createSyncWithBody(srv, fmt.Sprintf(`{"id": "agent-a-id", "name": "same-name", "projectPath": %q, "preserveWorkspace": true, "config": {"task": "t"}}`, projectPath))
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d: %s", w.Code, http.StatusInternalServerError, w.Body.String())
	}
	if sawFresh {
		t.Error("a preserveWorkspace create must not set FreshProvision")
	}
	got, err := os.ReadFile(filepath.Join(workspace, preserveCleanupFile))
	if err != nil {
		t.Fatalf("the workspace file was removed by the failed-create cleanup: %v", err)
	}
	if string(got) != preserveCleanupContent {
		t.Errorf("workspace file = %q, want %q", got, preserveCleanupContent)
	}
	if owner := readAgentOwner(agentDir); owner != "agent-a" {
		t.Errorf("the agent's files were removed (owner file %q)", owner)
	}
	if out := gitPreserveT(t, repo, "branch", "--list", "same-name"); !strings.Contains(out, "same-name") {
		t.Errorf("the agent branch was deleted (git branch --list: %q)", out)
	}
	if out := gitPreserveT(t, repo, "worktree", "list", "--porcelain"); !strings.Contains(out, workspace) {
		t.Errorf("the worktree entry was removed (worktrees:\n%s)", out)
	}
}

// The control: without preserveWorkspace the same failure still cleans up
// the agent's files, so the test above reaches the cleanup branch. The
// branch is kept either way: the failed-start cleanup never deletes a
// branch (agent.DeleteStartFailureFiles).
func TestCreateStartFailure_PlainCreateStillCleansUp(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Fatalf("git is required: %v", err)
	}
	srv, mgr, projectPath, agentDir := newSyncStartTestServer(t)
	repo, workspace := seedAgentWorktree(t, projectPath, agentDir)

	mgr.starts <- func(context.Context, api.StartOptions) (*api.AgentInfo, error) {
		return nil, errors.New("fake image pull failed")
	}
	w := createSyncWithBody(srv, fmt.Sprintf(`{"id": "agent-a-id", "name": "same-name", "projectPath": %q, "config": {"task": "t"}}`, projectPath))
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d: %s", w.Code, http.StatusInternalServerError, w.Body.String())
	}
	if _, err := os.Stat(filepath.Join(workspace, preserveCleanupFile)); !os.IsNotExist(err) {
		t.Fatalf("a plain create's failure should clean up the agent's files (stat err %v)", err)
	}
	if out := gitPreserveT(t, repo, "worktree", "list", "--porcelain"); strings.Contains(out, workspace) {
		t.Errorf("a plain create's failure should remove the worktree entry (worktrees:\n%s)", out)
	}
	if out := gitPreserveT(t, repo, "branch", "--list", "same-name"); !strings.Contains(out, "same-name") {
		t.Errorf("the failed-start cleanup deleted the agent branch (git branch --list: %q)", out)
	}
}

// An asynchronous create launch removes the agent's files when it fails,
// so a preserveWorkspace create is refused with asyncLaunch.
func TestCreateAgent_PreserveWorkspaceRefusesAsyncLaunch(t *testing.T) {
	srv, mgr := newTestServerWithProvisionCapture()
	w := postPreserveCreate(t, srv, `{"name": "a", "id": "id-a", "slug": "a", "preserveWorkspace": true, "asyncLaunch": true, "config": {"template": "claude"}}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d: %s", w.Code, http.StatusBadRequest, w.Body.String())
	}
	if mgr.startCalled || mgr.provisionCalled || mgr.reprovisionCalled {
		t.Error("a refused request must not reach the manager")
	}
}

// newPreserveDownloadServer is a broker whose agents' workspaces live under
// a temp WorktreeBase and whose GCS download is recorded, not run.
func newPreserveDownloadServer(t *testing.T) (*Server, *startFuncManager, string, *int) {
	t.Helper()
	base := t.TempDir()
	mgr := &startFuncManager{
		mockManager: &mockManager{},
		starts:      make(chan func(context.Context, api.StartOptions) (*api.AgentInfo, error), 4),
	}
	cfg := DefaultServerConfig()
	cfg.BrokerID = "test-broker-id"
	cfg.BrokerName = "test-host"
	cfg.WorktreeBase = base
	srv := New(cfg, mgr, &runtime.MockRuntime{NameFunc: func() string { return "docker" }}, config.RuntimeBuildRecord{})
	downloads := 0
	srv.SetWorkspaceDownloader(func(_ context.Context, _, _, localPath string) error {
		downloads++
		return os.WriteFile(filepath.Join(localPath, preserveCleanupFile), []byte("package main // fake upload\n"), 0o644)
	})
	return srv, mgr, base, &downloads
}

const preserveDownloadBody = `{"id": "agent-a-id", "name": "same-name", "preserveWorkspace": true, "workspaceStoragePath": "fake/agent-a", "workspaceStorageBucket": "fake-bucket", "config": {"task": "t"}}`

// A preserveWorkspace create of an agent whose uploaded workspace already
// has files keeps them: the upload is not downloaded over them.
func TestCreateAgent_PreserveWorkspaceSkipsDownloadOverExistingWorkspace(t *testing.T) {
	srv, mgr, base, downloads := newPreserveDownloadServer(t)
	workspace := filepath.Join(base, "same-name", "workspace")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, preserveCleanupFile), []byte(preserveCleanupContent), 0o644); err != nil {
		t.Fatal(err)
	}
	var gotWorkspace string
	mgr.starts <- func(_ context.Context, opts api.StartOptions) (*api.AgentInfo, error) {
		gotWorkspace = opts.Workspace
		return &api.AgentInfo{ID: "c1", Name: opts.Name, Phase: "running"}, nil
	}

	w := createSyncWithBody(srv, preserveDownloadBody)
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d: %s", w.Code, http.StatusCreated, w.Body.String())
	}
	if *downloads != 0 {
		t.Errorf("the upload was downloaded %d time(s) over the existing workspace", *downloads)
	}
	got, err := os.ReadFile(filepath.Join(workspace, preserveCleanupFile))
	if err != nil || string(got) != preserveCleanupContent {
		t.Errorf("workspace file = %q (%v), want the agent's %q", got, err, preserveCleanupContent)
	}
	if gotWorkspace != workspace {
		t.Errorf("start workspace = %q, want the existing %q", gotWorkspace, workspace)
	}
}

// The control: with no existing workspace the preserveWorkspace create
// downloads the upload as before.
func TestCreateAgent_PreserveWorkspaceDownloadsIntoAbsentWorkspace(t *testing.T) {
	srv, mgr, _, downloads := newPreserveDownloadServer(t)
	mgr.starts <- func(_ context.Context, opts api.StartOptions) (*api.AgentInfo, error) {
		return &api.AgentInfo{ID: "c1", Name: opts.Name, Phase: "running"}, nil
	}
	w := createSyncWithBody(srv, preserveDownloadBody)
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d: %s", w.Code, http.StatusCreated, w.Body.String())
	}
	if *downloads != 1 {
		t.Errorf("downloads = %d, want 1 into the absent workspace", *downloads)
	}
}

// Inside an agent container (SCION_HOST_UID set), where a host worktree can
// look missing while it is in use, the failed-create cleanup leaves git's
// worktree entry and the branch it has checked out in place: nothing of the
// agent's history is lost.
func TestCreateStartFailure_InAgentContainerKeepsWorktreeEntryAndBranch(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Fatalf("git is required: %v", err)
	}
	srv, mgr, projectPath, agentDir := newSyncStartTestServer(t)
	repo, workspace := seedAgentWorktree(t, projectPath, agentDir)
	t.Setenv("SCION_HOST_UID", "1000")

	mgr.starts <- func(context.Context, api.StartOptions) (*api.AgentInfo, error) {
		return nil, errors.New("fake image pull failed")
	}
	w := createSyncWithBody(srv, fmt.Sprintf(`{"id": "agent-a-id", "name": "same-name", "projectPath": %q, "config": {"task": "t"}}`, projectPath))
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d: %s", w.Code, http.StatusInternalServerError, w.Body.String())
	}
	// The cleanup ran: the agent's files are gone.
	if _, err := os.Stat(filepath.Join(workspace, preserveCleanupFile)); !os.IsNotExist(err) {
		t.Fatalf("the failed-create cleanup should have removed the agent's files (stat err %v)", err)
	}
	if out := gitPreserveT(t, repo, "branch", "--list", "same-name"); !strings.Contains(out, "same-name") {
		t.Errorf("the agent branch must be kept inside an agent container (git branch --list: %q)", out)
	}
	if out := gitPreserveT(t, repo, "worktree", "list", "--porcelain"); !strings.Contains(out, "worktree "+workspace) {
		t.Errorf("git's worktree entry for %s must be kept inside an agent container; worktrees:\n%s", workspace, out)
	}
}

// A preserveWorkspace create that gathers env first (ptone/scion#6101 on
// top of ptone/scion#6096): the first pass answers 202 with an unmet
// required key and touches nothing, and the final replay with the key whose
// Start fails still keeps the agent's workspace, files, worktree entry and
// branch. The failed-start cleanup is skipped on the replay exactly as on a
// single-pass preserveWorkspace create.
func TestCreateStartFailureAfterEnvGather_PreserveWorkspaceKeepsWorkspaceAndBranch(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Fatalf("git is required: %v", err)
	}
	srv, mgr, projectPath, agentDir := newSyncStartTestServer(t)
	repo, workspace := seedAgentWorktree(t, projectPath, agentDir)
	survives := func(t *testing.T, when string) {
		t.Helper()
		got, err := os.ReadFile(filepath.Join(workspace, preserveCleanupFile))
		if err != nil {
			t.Fatalf("%s: the workspace file was removed: %v", when, err)
		}
		if string(got) != preserveCleanupContent {
			t.Errorf("%s: workspace file = %q, want %q", when, got, preserveCleanupContent)
		}
		if owner := readAgentOwner(agentDir); owner != "agent-a" {
			t.Errorf("%s: the agent's files were removed (owner file %q)", when, owner)
		}
		if out := gitPreserveT(t, repo, "branch", "--list", "same-name"); !strings.Contains(out, "same-name") {
			t.Errorf("%s: the agent branch was deleted (git branch --list: %q)", when, out)
		}
		if out := gitPreserveT(t, repo, "worktree", "list", "--porcelain"); !strings.Contains(out, workspace) {
			t.Errorf("%s: the worktree entry was removed (worktrees:\n%s)", when, out)
		}
	}

	// Pass 1: gatherEnv with an unmet required key: a needs answer, no Start.
	w := createSyncWithBody(srv, fmt.Sprintf(`{"id": "agent-a-id", "name": "same-name", "projectPath": %q, "preserveWorkspace": true, "gatherEnv": true, "requiredSecrets": [{"key": "FAKE_REQUIRED_KEY", "description": "fake"}], "config": {"task": "t"}}`, projectPath))
	if w.Code != http.StatusAccepted {
		t.Fatalf("pass 1 status = %d, want %d (a needs answer): %s", w.Code, http.StatusAccepted, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "FAKE_REQUIRED_KEY") {
		t.Fatalf("pass 1 did not report the unmet key: %s", w.Body.String())
	}
	survives(t, "after the needs answer")

	// Pass 2: the final replay with the key; its Start fails.
	var sawFresh bool
	mgr.starts <- func(_ context.Context, opts api.StartOptions) (*api.AgentInfo, error) {
		sawFresh = opts.FreshProvision
		return nil, errors.New("fake image pull failed")
	}
	w = createSyncWithBody(srv, fmt.Sprintf(`{"id": "agent-a-id", "name": "same-name", "projectPath": %q, "preserveWorkspace": true, "gatherEnv": true, "envGatherFinal": true, "requiredSecrets": [{"key": "FAKE_REQUIRED_KEY", "description": "fake"}], "resolvedEnv": {"FAKE_REQUIRED_KEY": "fake-value"}, "config": {"task": "t"}}`, projectPath))
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("replay status = %d, want %d (the Start failed): %s", w.Code, http.StatusInternalServerError, w.Body.String())
	}
	if sawFresh {
		t.Error("a preserveWorkspace replay must not set FreshProvision")
	}
	survives(t, "after the failed replay")
}

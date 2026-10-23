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

//go:build !no_sqlite

package hub

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GoogleCloudPlatform/scion/pkg/agent/state"
	"github.com/GoogleCloudPlatform/scion/pkg/api"
	"github.com/GoogleCloudPlatform/scion/pkg/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Project storage phase 2 (ptone/scion#5813, ptone/scion#5815): the GCS
// upload of a hub-managed workspace on agent create is gone. Agents of a
// hub-managed or shared-workspace project mount the workspace the hub
// serves; a remote broker reaches it only through the hub's shared
// workspace storage. A remote broker without it is refused with 409 before
// anything is dispatched (topology T6); a broker with a local path for the
// project, and per-agent workspace modes, are unaffected.

// setupRemoteBrokerWorkspaceServer is setupCreateAgentServer with its broker
// made remote (no local path for the project) and a local storage provider.
func setupRemoteBrokerWorkspaceServer(t *testing.T) (*Server, store.Store, *store.Project, *createAgentDispatcher) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	disp := &createAgentDispatcher{createPhase: string(state.PhaseRunning)}
	srv, s, project := setupCreateAgentServer(t, disp)
	srv.SetStorage(newContentMockStorage("local"))
	clearCreateAgentProviderPath(t, s, project)
	return srv, s, project, disp
}

// setBrokerWorkspaceStorage records the broker's reported workspace storage
// descriptor, as its heartbeat does.
func setBrokerWorkspaceStorage(t *testing.T, s store.Store, brokerID string, ws *api.BrokerWorkspaceStorage) {
	t.Helper()
	ctx := context.Background()
	broker, err := s.GetRuntimeBroker(ctx, brokerID)
	require.NoError(t, err)
	broker.WorkspaceStorage = ws
	require.NoError(t, s.UpdateRuntimeBroker(ctx, broker))
	got, err := s.GetRuntimeBroker(ctx, brokerID)
	require.NoError(t, err)
	if ws == nil {
		require.Nil(t, got.WorkspaceStorage, "fixture check: no descriptor")
	} else {
		require.NotNil(t, got.WorkspaceStorage, "fixture check: descriptor stored")
		require.Equal(t, ws.Backend, got.WorkspaceStorage.Backend)
	}
}

// requireRefusedCreate asserts a 409 naming wantText, no dispatch, and no
// agent row left behind.
func requireRefusedCreate(t *testing.T, s store.Store, project *store.Project, disp *createAgentDispatcher, code int, body, wantText string) {
	t.Helper()
	require.Equal(t, http.StatusConflict, code, "body: %s", body)
	var resp ErrorResponse
	require.NoError(t, json.Unmarshal([]byte(body), &resp))
	assert.Equal(t, ErrCodeConflict, resp.Error.Code)
	assert.Contains(t, resp.Error.Message, wantText)
	assert.False(t, strings.Contains(resp.Error.Message, "/"), "the message names no host path: %q", resp.Error.Message)
	require.Nil(t, disp.capturedAgent, "nothing may be dispatched")
	agents, err := s.ListAgents(context.Background(), store.AgentFilter{ProjectID: project.ID}, store.ListOptions{})
	require.NoError(t, err)
	require.Empty(t, agents.Items, "the agent row must be cleaned up")
}

func TestCreateAgent_RemoteBroker_LocalHubWorkspace_Refused409(t *testing.T) {
	srv, s, project, disp := setupRemoteBrokerWorkspaceServer(t)

	rec := doRequest(t, srv, http.MethodPost, "/api/v1/agents", CreateAgentRequest{
		Name: "t6-local-hub", ProjectID: project.ID,
	})
	requireRefusedCreate(t, s, project, disp, rec.Code, rec.Body.String(),
		"lives on the hub's local disk, which a remote runtime broker cannot reach")
}

func TestCreateAgent_RemoteBroker_SharedWorkspaceGit_LocalHub_Refused409(t *testing.T) {
	srv, s, project, disp := setupRemoteBrokerWorkspaceServer(t)
	project.GitRemote = "github.com/example/repo"
	project.Labels = map[string]string{store.LabelWorkspaceMode: store.WorkspaceModeShared}
	require.NoError(t, s.UpdateProject(context.Background(), project))
	require.True(t, project.IsSharedWorkspace(), "fixture check: shared-workspace project")

	rec := doRequest(t, srv, http.MethodPost, "/api/v1/agents", CreateAgentRequest{
		Name: "t6-shared-git", ProjectID: project.ID,
	})
	requireRefusedCreate(t, s, project, disp, rec.Code, rec.Body.String(),
		"lives on the hub's local disk")
}

func TestCreateAgent_RemoteBroker_NoSharedStorageDescriptor_Refused409(t *testing.T) {
	srv, s, project, disp := setupRemoteBrokerWorkspaceServer(t)
	srv.config.WorkspaceStorageConfig = nfsConfig(t.TempDir())
	for _, ws := range []*api.BrokerWorkspaceStorage{nil, {Backend: api.WorkspaceStorageBackendLocal}} {
		setBrokerWorkspaceStorage(t, s, project.DefaultRuntimeBrokerID, ws)
		rec := doRequest(t, srv, http.MethodPost, "/api/v1/agents", CreateAgentRequest{
			Name: "t6-no-descriptor", ProjectID: project.ID,
		})
		requireRefusedCreate(t, s, project, disp, rec.Code, rec.Body.String(),
			"does not report the hub's shared workspace storage (nfs)")
	}
}

func TestCreateAgent_RemoteBroker_SharedStorage_DispatchesUUIDWorkspace(t *testing.T) {
	srv, s, project, disp := setupRemoteBrokerWorkspaceServer(t)
	mountRoot := t.TempDir()
	srv.config.WorkspaceStorageConfig = nfsConfig(mountRoot)
	require.NoError(t, os.MkdirAll(filepath.Join(mountRoot, "share1"), 0o755))
	setBrokerWorkspaceStorage(t, s, project.DefaultRuntimeBrokerID, &api.BrokerWorkspaceStorage{Backend: api.WorkspaceStorageBackendNFS})

	rec := doRequest(t, srv, http.MethodPost, "/api/v1/agents", CreateAgentRequest{
		Name: "shared-storage-agent", ProjectID: project.ID,
	})
	require.Equal(t, http.StatusCreated, rec.Code, "body: %s", rec.Body.String())
	require.NotNil(t, disp.capturedAgent, "the agent must be dispatched")
	require.NotNil(t, disp.capturedAgent.AppliedConfig)
	assert.Equal(t, filepath.Join(mountRoot, "share1", "projects", project.ID, "workspace"),
		disp.capturedAgent.AppliedConfig.Workspace, "the workspace is the uuid tree agents mount")
	assert.Empty(t, disp.capturedAgent.AppliedConfig.WorkspaceStoragePath, "no GCS copy of the workspace")
	assert.Empty(t, disp.capturedAgent.AppliedConfig.WorkspaceStorageBucket)
}

func TestCreateAgent_BrokerWithLocalPath_Dispatches(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	disp := &createAgentDispatcher{createPhase: string(state.PhaseRunning)}
	srv, _, project := setupCreateAgentServer(t, disp)
	srv.SetStorage(newContentMockStorage("local"))

	rec := doRequest(t, srv, http.MethodPost, "/api/v1/agents", CreateAgentRequest{
		Name: "local-path-agent", ProjectID: project.ID,
	})
	require.Equal(t, http.StatusCreated, rec.Code, "body: %s", rec.Body.String())
	require.NotNil(t, disp.capturedAgent, "the agent must be dispatched")
	require.NotNil(t, disp.capturedAgent.AppliedConfig)
	assert.Empty(t, disp.capturedAgent.AppliedConfig.WorkspaceStoragePath)
}

func TestCreateAgent_RemoteBroker_ClonePerAgentGit_Dispatches(t *testing.T) {
	srv, s, project, disp := setupRemoteBrokerWorkspaceServer(t)
	project.GitRemote = "github.com/example/repo"
	require.NoError(t, s.UpdateProject(context.Background(), project))
	require.False(t, mountsHubProjectWorkspace(project), "fixture check: per-agent git mode")

	rec := doRequest(t, srv, http.MethodPost, "/api/v1/agents", CreateAgentRequest{
		Name: "clone-per-agent", ProjectID: project.ID,
	})
	require.Equal(t, http.StatusCreated, rec.Code, "body: %s", rec.Body.String())
	require.NotNil(t, disp.capturedAgent, "the agent must be dispatched")
}

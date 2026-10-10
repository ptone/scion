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
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/GoogleCloudPlatform/scion/pkg/api"
	"github.com/GoogleCloudPlatform/scion/pkg/config"
	"github.com/GoogleCloudPlatform/scion/pkg/hubclient"
)

// These tests drive the broker's start and restart handlers over HTTP
// (ptone/scion#2157). They use only the handlers' request and Manager.Start
// behaviour, so they also run unchanged against code that predates the
// change.

// startWithTemplateHub returns a test server with one Hub connection whose
// local storage holds the global template "web-dev", and that template's
// on-disk directory.
func startWithTemplateHub(t *testing.T) (*Server, *mockManager, string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	srv := newTestServer(t)
	stor, dir := newLocalStorageWithTemplate(t, "web-dev", true)
	srv.hubMu.Lock()
	srv.hubConnections["hub-1"] = &HubConnection{
		Name:         "hub-1",
		LocalStorage: stor,
		HubClient: &stubHubClient{templates: &stubTemplateService{
			getFunc: func(ctx context.Context, ref string) (*hubclient.Template, error) {
				return &hubclient.Template{ID: "tpl-uuid", Slug: "web-dev", Scope: "global"}, nil
			},
		}},
		Hydrator: newTestHydrator(t),
	}
	srv.hubMu.Unlock()
	return srv, srv.manager.(*mockManager), dir
}

func postStart(t *testing.T, srv *Server, agentName, projectDir string) {
	t.Helper()
	body := `{
		"projectPath": "` + projectDir + `",
		"templateName": "web-dev",
		"templateId": "tpl-uuid",
		"templateHash": "sha256:abc"
	}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/agents/"+agentName+"/start", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)
	require.Equal(t, http.StatusAccepted, w.Code, w.Body.String())
}

// TestStartAgent_MissingStateProvisionsFromHubTemplate: when the broker no
// longer has the agent's state, a start carrying the template identity
// provisions the agent from its own (hydrated) template, not the default.
func TestStartAgent_MissingStateProvisionsFromHubTemplate(t *testing.T) {
	srv, mgr, templateDir := startWithTemplateHub(t)
	projectDir := filepath.Join(t.TempDir(), ".scion")
	require.NoError(t, os.MkdirAll(projectDir, 0o755))

	postStart(t, srv, "gone-agent", projectDir)
	require.Equal(t, 1, mgr.startCalls)
	assert.Equal(t, templateDir, mgr.lastStartOpts.Template)
	assert.Equal(t, "web-dev", mgr.lastStartOpts.TemplateName)
}

// TestStartAgent_SurvivingStateKeepsTemplateUnset: when the agent's state
// survived, the start does not set a template path (the agent starts from
// its own state as before) and SCION_TEMPLATE names the template.
func TestStartAgent_SurvivingStateKeepsTemplateUnset(t *testing.T) {
	srv, mgr, _ := startWithTemplateHub(t)
	projectDir := filepath.Join(t.TempDir(), ".scion")
	agentDir := config.GetAgentDir(projectDir, "kept-agent", false)
	require.NoError(t, os.MkdirAll(filepath.Join(agentDir, "home"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(agentDir, "scion-agent.json"), []byte(`{"harness":"claude"}`), 0o644))

	postStart(t, srv, "kept-agent", projectDir)
	require.Equal(t, 1, mgr.startCalls)
	assert.Empty(t, mgr.lastStartOpts.Template)
	assert.Equal(t, "web-dev", mgr.lastStartOpts.Env["SCION_TEMPLATE"])
}

// TestRestartAgent_UsesRequestInputsWithoutContainer proves a restart of an
// agent with no container (on Kubernetes, stopping an agent deletes its
// pod) resolves the agent's project from the request and carries the
// workspace-recreation inputs to Manager.Start, as a start does.
func TestRestartAgent_UsesRequestInputsWithoutContainer(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	srv := newTestServer(t)
	mgr := srv.manager.(*mockManager)

	projectDir := filepath.Join(t.TempDir(), ".scion")
	require.NoError(t, os.MkdirAll(projectDir, 0o755))

	body := `{
		"resolvedEnv": {"SCION_AGENT_ID": "agent-uuid-2157"},
		"projectPath": "` + projectDir + `",
		"harnessConfig": "claude",
		"gitClone": {"url": "https://github.com/example/repo.git", "branch": "main"},
		"branch": "feature-branch",
		"templateName": "web-dev"
	}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/agents/gone-agent/restart", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)

	require.Equal(t, http.StatusAccepted, w.Code, w.Body.String())
	require.Equal(t, 1, mgr.startCalls)
	opts := mgr.lastStartOpts
	assert.Equal(t, projectDir, opts.ProjectPath, "the project comes from the request, not a container")
	require.NotNil(t, opts.GitClone, "the restart must carry the git clone settings")
	assert.Equal(t, "https://github.com/example/repo.git", opts.GitClone.URL)
	assert.Equal(t, "feature-branch", opts.Branch)
	assert.Equal(t, "claude", opts.HarnessConfig)
	assert.Equal(t, "https://github.com/example/repo.git", opts.Env["SCION_GIT_CLONE_URL"])
	assert.False(t, opts.FreshProvision, "a restart never clears an existing workspace")
}

// TestRestartAgent_OlderHubFallsBackToContainerProject proves a restart from
// an older Hub (no project in the request) still takes the project from the
// agent's container, and builds no start config.
func TestRestartAgent_OlderHubFallsBackToContainerProject(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	srv := newTestServer(t)
	mgr := srv.manager.(*mockManager)
	projectDir := filepath.Join(t.TempDir(), ".scion")
	require.NoError(t, os.MkdirAll(projectDir, 0o755))
	mgr.mu.Lock()
	mgr.agents = append(mgr.agents, api.AgentInfo{
		ID:              "container-2157",
		ContainerID:     "container-2157",
		Name:            "kept-agent",
		Slug:            "kept-agent",
		ProjectPath:     projectDir,
		Phase:           "running",
		ContainerStatus: "Up 1 hour",
	})
	mgr.mu.Unlock()

	req := httptest.NewRequest(http.MethodPost, "/api/v1/agents/kept-agent/restart", strings.NewReader(`{"resolvedEnv": {"FOO": "bar"}}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)

	require.Equal(t, http.StatusAccepted, w.Code, w.Body.String())
	opts := mgr.lastStartOpts
	assert.Equal(t, projectDir, opts.ProjectPath)
	assert.Nil(t, opts.GitClone)
	_, set := opts.Env["SCION_GIT_CLONE_URL"]
	assert.False(t, set)
}

// TestRestartAgent_PassesResolvedSecretsLikeStart: the resolved secrets a
// restart request carries reach Manager.Start the same way a start
// request's do (fake values only).
func TestRestartAgent_PassesResolvedSecretsLikeStart(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	body := `{
		"resolvedEnv": {"FOO": "bar"},
		"resolvedSecrets": [{"name": "FAKE_CREDS", "type": "file", "target": "~/.fake/creds.json", "value": "fake-secret-value", "source": "user"}]
	}`
	got := map[string][]api.ResolvedSecret{}
	for _, op := range []string{"start", "restart"} {
		srv := newTestServer(t)
		mgr := srv.manager.(*mockManager)
		req := httptest.NewRequest(http.MethodPost, "/api/v1/agents/test-agent-1/"+op, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		srv.Handler().ServeHTTP(w, req)
		require.Equal(t, http.StatusAccepted, w.Code, "%s: %s", op, w.Body.String())
		got[op] = mgr.lastStartOpts.ResolvedSecrets
	}
	require.Len(t, got["start"], 1)
	assert.Equal(t, got["start"], got["restart"], "restart must hand Manager.Start the secrets start does")
}

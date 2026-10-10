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

// templateIdentityFixture is a project directory and the start options a
// start or restart of agentName in it builds, for the
// applyStartTemplateIdentity tests (ptone/scion#2157).
type templateIdentityFixture struct {
	srv        *Server
	projectDir string
	opts       api.StartOptions
	env        map[string]string
}

func newTemplateIdentityFixture(t *testing.T, agentName string, provisioned bool) *templateIdentityFixture {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	projectDir := filepath.Join(t.TempDir(), ".scion")
	require.NoError(t, os.MkdirAll(projectDir, 0o755))
	if provisioned {
		agentDir := config.GetAgentDir(projectDir, agentName, false)
		require.NoError(t, os.MkdirAll(filepath.Join(agentDir, "home"), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(agentDir, "scion-agent.json"), []byte(`{"harness":"claude"}`), 0o644))
	}
	return &templateIdentityFixture{
		srv:        newTestServer(t),
		projectDir: projectDir,
		opts:       api.StartOptions{Name: agentName, ProjectPath: projectDir, BrokerMode: true},
		env:        map[string]string{},
	}
}

func (f *templateIdentityFixture) apply(in startContextInputs, conn *HubConnection, slug string) error {
	return f.srv.applyStartTemplateIdentity(context.Background(), in, &f.opts, conn, slug, f.env, func(string, api.EnvKind) {})
}

// templateConn is a co-located Hub connection whose local storage holds the
// global template slug; getErr, when set, fails the template lookup.
func templateConn(t *testing.T, slug string, getErr error, onGet func()) (*HubConnection, string) {
	t.Helper()
	stor, dir := newLocalStorageWithTemplate(t, slug, true)
	return &HubConnection{
		Name:         "hub-1",
		IsColocated:  true,
		LocalStorage: stor,
		HubClient: &stubHubClient{templates: &stubTemplateService{
			getFunc: func(ctx context.Context, ref string) (*hubclient.Template, error) {
				if onGet != nil {
					onGet()
				}
				if getErr != nil {
					return nil, getErr
				}
				return &hubclient.Template{ID: "tpl-uuid", Slug: slug, Scope: "global"}, nil
			},
		}},
	}, dir
}

// When the agent's broker-side state is gone, a start that carries the
// template identity hydrates the agent's own template and provisions from
// it, as create does.
func TestApplyStartTemplateIdentity_MissingStateHydratesTemplate(t *testing.T) {
	f := newTemplateIdentityFixture(t, "gone-agent", false)
	conn, wantDir := templateConn(t, "web-dev", nil, nil)

	err := f.apply(startContextInputs{TemplateID: "tpl-uuid", TemplateHash: "sha256:abc"}, conn, "web-dev")
	require.NoError(t, err)
	assert.Equal(t, wantDir, f.opts.Template, "the recreated agent must be provisioned from its own template")
	_, set := f.env["SCION_TEMPLATE"]
	assert.False(t, set, "Manager.Start sets SCION_TEMPLATE from opts.Template, as on create")
}

// When the agent's broker-side state survived, the start is left as it was:
// nothing is hydrated and opts.Template stays unset, so the existing agent
// resolves its image and harness-config exactly as before. Only
// SCION_TEMPLATE names the template.
func TestApplyStartTemplateIdentity_SurvivingStateIsUnchanged(t *testing.T) {
	f := newTemplateIdentityFixture(t, "kept-agent", true)
	conn, _ := templateConn(t, "web-dev", nil, func() {
		t.Error("the template must not be hydrated when the agent's state survived")
	})

	err := f.apply(startContextInputs{TemplateID: "tpl-uuid", TemplateHash: "sha256:abc"}, conn, "web-dev")
	require.NoError(t, err)
	assert.Empty(t, f.opts.Template)
	assert.Equal(t, "web-dev", f.env["SCION_TEMPLATE"])
}

// A hydration failure while the agent has to be provisioned again fails the
// start with the same error create returns, rather than provisioning the
// agent from the default template.
func TestApplyStartTemplateIdentity_HydrationFailureFailsStart(t *testing.T) {
	f := newTemplateIdentityFixture(t, "gone-agent", false)
	conn, _ := templateConn(t, "web-dev", errors.New("hub unavailable"), nil)

	err := f.apply(startContextInputs{TemplateID: "tpl-uuid", TemplateHash: "sha256:abc"}, conn, "web-dev")
	var sce *startContextError
	require.ErrorAs(t, err, &sce)
	assert.Equal(t, http.StatusInternalServerError, sce.Status)
	assert.True(t, sce.IsHubError)
	assert.Empty(t, f.opts.Template)
}

// An older Hub sends no template identity: a start that has to provision
// the agent again behaves as before (no hydration, no template path).
func TestApplyStartTemplateIdentity_NoIdentityKeepsPreviousBehaviour(t *testing.T) {
	f := newTemplateIdentityFixture(t, "gone-agent", false)
	conn, _ := templateConn(t, "web-dev", nil, func() {
		t.Error("nothing may be hydrated without a template identity")
	})

	require.NoError(t, f.apply(startContextInputs{}, conn, "web-dev"))
	assert.Empty(t, f.opts.Template)
	_, set := f.env["SCION_TEMPLATE"]
	assert.False(t, set)
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

func TestRestartAgentConfig_NilWithoutInputs(t *testing.T) {
	assert.Nil(t, restartAgentConfig("", "", "", nil, true, nil, nil, ""),
		"an older Hub's restart (sharedWorkspace alone) builds no config")
	cfg := restartAgentConfig("claude", "", "", nil, false, nil, nil, "")
	require.NotNil(t, cfg)
	assert.Equal(t, "claude", cfg.HarnessConfig)
}

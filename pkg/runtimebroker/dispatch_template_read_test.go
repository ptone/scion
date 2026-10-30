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
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/GoogleCloudPlatform/scion/pkg/api"
	"github.com/GoogleCloudPlatform/scion/pkg/apiclient"
	"github.com/GoogleCloudPlatform/scion/pkg/hubclient"
	"github.com/GoogleCloudPlatform/scion/pkg/storage"
	"github.com/GoogleCloudPlatform/scion/pkg/templatecache"
)

// Dispatch-bound template reads (ptone/scion#5770): every Hub template read
// the broker makes while hydrating an agent's template names that agent, so
// the Hub can let the broker running the agent read its user-scoped
// template, and a Hub refusal of a dispatched TemplateID reads as a clear
// message.

func hubNotFound() error {
	return &apiclient.APIError{StatusCode: http.StatusNotFound, Code: "not_found", Message: "Template not found"}
}

// A remote (hydrator) read names the agent, and a Hub 404 for a dispatch
// that named a TemplateID wraps errDispatchTemplateUnreadable.
func TestHydrateTemplate_HydratorNamesAgentAndMapsHubRefusal(t *testing.T) {
	srv := newTestServer(t)
	stub := &stubTemplateService{getFunc: func(ctx context.Context, ref string) (*hubclient.Template, error) {
		return nil, hubNotFound()
	}}
	cache, err := templatecache.New(t.TempDir(), 1<<20)
	require.NoError(t, err)
	client := &stubHubClient{templates: stub}
	conn := &HubConnection{Name: "remote", HubClient: client, Hydrator: templatecache.NewHydrator(cache, client)}

	_, err = srv.hydrateTemplate(context.Background(), &CreateAgentConfig{Template: "web-dev", TemplateID: "tpl-user", TemplateHash: "sha256:abc"}, conn, "agent-uuid-1")
	require.Error(t, err)
	assert.True(t, errors.Is(err, errDispatchTemplateUnreadable), "a Hub 404 for a dispatched TemplateID must map to the clear error, got %v", err)
	assert.Equal(t, []string{"agent-uuid-1"}, stub.getAgents, "the Hub read must name the agent")

	msg, ok := dispatchTemplateUnreadableText(err)
	require.True(t, ok)
	assert.Contains(t, msg, "template web-dev is no longer readable for this agent")
	assert.NotContains(t, msg, "Template not found", "the client text must not carry the Hub error")
}

// Any other hydration error is passed through unchanged.
func TestHydrateTemplate_OtherErrorsAreNotMapped(t *testing.T) {
	srv := newTestServer(t)
	stub := &stubTemplateService{getFunc: func(ctx context.Context, ref string) (*hubclient.Template, error) {
		return nil, &apiclient.APIError{StatusCode: http.StatusInternalServerError, Code: "internal_error"}
	}}
	cache, err := templatecache.New(t.TempDir(), 1<<20)
	require.NoError(t, err)
	client := &stubHubClient{templates: stub}
	conn := &HubConnection{Name: "remote", HubClient: client, Hydrator: templatecache.NewHydrator(cache, client)}

	_, err = srv.hydrateTemplate(context.Background(), &CreateAgentConfig{Template: "web-dev", TemplateID: "tpl-user"}, conn, "agent-uuid-1")
	require.Error(t, err)
	assert.False(t, errors.Is(err, errDispatchTemplateUnreadable))
}

// The co-located local-storage read names the agent too.
func TestHydrateTemplate_LocalStorageNamesAgent(t *testing.T) {
	stor, wantDir := newLocalStorageWithTemplate(t, "my-template", true)
	srv := newTestServer(t)
	stub := &stubTemplateService{getFunc: func(ctx context.Context, ref string) (*hubclient.Template, error) {
		return &hubclient.Template{ID: "some-uuid", Slug: "my-template", Scope: "global"}, nil
	}}
	conn := &HubConnection{Name: "local", IsColocated: true, LocalStorage: stor, HubClient: &stubHubClient{templates: stub}}

	path, err := srv.hydrateTemplate(context.Background(), &CreateAgentConfig{Template: "my-template", TemplateID: "some-uuid"}, conn, "agent-uuid-2")
	require.NoError(t, err)
	assert.Equal(t, wantDir, path)
	assert.Equal(t, []string{"agent-uuid-2"}, stub.getAgents)
}

// An async launch failure carrying the error reports the same clear text.
func TestClassifyStartErrorCode_DispatchTemplateUnreadable(t *testing.T) {
	err := &dispatchTemplateUnreadableError{template: "web-dev", cause: hubNotFound()}
	code, msg := classifyStartErrorCode(err, "web-dev", opCreateAgent)
	assert.Equal(t, api.LaunchCodeTemplateNotFound, code)
	assert.Contains(t, msg, "template web-dev is no longer readable for this agent")
}

// recordingTemplateHub registers hub connection "hub-1" whose template
// metadata read fails with getErr, and returns the stub recording the agent
// each read names.
func recordingTemplateHub(t *testing.T, srv *Server, getErr error) *stubTemplateService {
	t.Helper()
	stor, err := storage.NewLocal(storage.Config{Provider: storage.ProviderLocal, LocalPath: t.TempDir(), Bucket: "local"})
	require.NoError(t, err)
	stub := &stubTemplateService{getFunc: func(ctx context.Context, ref string) (*hubclient.Template, error) {
		return nil, getErr
	}}
	srv.hubMu.Lock()
	srv.hubConnections["hub-1"] = &HubConnection{
		Name:         "hub-1",
		IsColocated:  true,
		LocalStorage: stor,
		HubClient:    &recreationHubClient{stubHubClient{templates: stub}},
		Hydrator:     newTestHydrator(t),
	}
	srv.hubMu.Unlock()
	return stub
}

// Each create hydration site (the env-gather preflight, the policy
// preflight, and launch) names the agent the Hub dispatched (req.ID), and
// a Hub refusal answers with the clear message.
func TestCreateAgent_TemplateReadsNameAgent(t *testing.T) {
	cases := []struct {
		name         string
		gatherEnv    bool
		allowScripts bool
	}{
		{name: "env-gather preflight", gatherEnv: true},
		{name: "policy preflight", allowScripts: false},
		{name: "launch", allowScripts: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, _, projectDir := newTestServerWithHarnessConfig(t, "claude",
				"harness: claude\nimage: test-image\nuser: scion\nauth_selected_type: vertex-ai\n"+claudeAuthBlock,
				"schema_version: \"1\"\nprofiles:\n  default:\n    runtime: mock\n")
			srv.config.AllowContainerScriptHarnesses = tc.allowScripts
			stub := recordingTemplateHub(t, srv, hubNotFound())

			body := `{
				"name": "agent-dispatch-read",
				"id": "agent-uuid-dispatch-read",
				"gatherEnv": ` + map[bool]string{true: "true", false: "false"}[tc.gatherEnv] + `,
				"projectPath": "` + projectDir + `",
				"config": {
					"template": "user-tpl",
					"templateId": "tpl-user",
					"harnessConfig": "claude",
					"profile": "default",
					"gcpIdentity": {"metadata_mode": "passthrough"}
				}
			}`
			req := httptest.NewRequest(http.MethodPost, "/api/v1/agents", strings.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("X-Scion-Hub-Connection", "hub-1")
			w := httptest.NewRecorder()
			srv.Handler().ServeHTTP(w, req)

			require.NotEmpty(t, stub.getAgents, "the template must have been read: %d %s", w.Code, w.Body.String())
			for _, got := range stub.getAgents {
				assert.Equal(t, "agent-uuid-dispatch-read", got)
			}
			var resp ErrorResponse
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp), w.Body.String())
			assert.Equal(t, http.StatusInternalServerError, w.Code, w.Body.String())
			assert.Equal(t, ErrCodeTemplateError, resp.Error.Code)
			assert.Contains(t, resp.Error.Message, "template user-tpl is no longer readable for this agent", w.Body.String())
		})
	}
}

// startWithRecordingTemplateHub is startWithTemplateHub whose template
// service also records the agent each read names.
func startWithRecordingTemplateHub(t *testing.T) (*Server, *mockManager, *stubTemplateService) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	srv := newTestServer(t)
	stor, _ := newLocalStorageWithTemplate(t, "web-dev", true)
	stub := &stubTemplateService{getFunc: func(ctx context.Context, ref string) (*hubclient.Template, error) {
		return &hubclient.Template{ID: "tpl-uuid", Slug: "web-dev", Scope: "global"}, nil
	}}
	srv.hubMu.Lock()
	srv.hubConnections["hub-1"] = &HubConnection{
		Name:         "hub-1",
		LocalStorage: stor,
		HubClient:    &recreationHubClient{stubHubClient{templates: stub}},
		Hydrator:     newTestHydrator(t),
	}
	srv.hubMu.Unlock()
	return srv, srv.manager.(*mockManager), stub
}

// A start names the agent from the Hub-injected SCION_AGENT_ID, both when
// it provisions from the template and when it resolves only the env source.
func TestStartAgent_TemplateReadsNameAgentFromResolvedEnv(t *testing.T) {
	for _, provisioned := range []bool{false, true} {
		srv, mgr, stub := startWithRecordingTemplateHub(t)
		projectDir := filepath.Join(t.TempDir(), ".scion")
		require.NoError(t, os.MkdirAll(projectDir, 0o755))
		if provisioned {
			writeProvisionedAgent(t, projectDir, "start-agent")
		}
		body := `{
			"resolvedEnv": {"SCION_AGENT_ID": "agent-uuid-start"},
			"projectPath": "` + projectDir + `",
			"templateName": "web-dev",
			"templateId": "tpl-uuid",
			"templateHash": "sha256:abc"
		}`
		req := httptest.NewRequest(http.MethodPost, "/api/v1/agents/start-agent/start", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		srv.Handler().ServeHTTP(w, req)
		require.Equal(t, http.StatusAccepted, w.Code, w.Body.String())
		require.Equal(t, 1, mgr.startCalls)
		require.NotEmpty(t, stub.getAgents, "provisioned=%v: the template must be read", provisioned)
		for _, got := range stub.getAgents {
			assert.Equal(t, "agent-uuid-start", got, "provisioned=%v", provisioned)
		}
	}
}

// A restart names the agent from the Hub-injected SCION_AGENT_ID.
func TestRestartAgent_TemplateReadsNameAgentFromResolvedEnv(t *testing.T) {
	srv, mgr, stub := startWithRecordingTemplateHub(t)
	projectDir := filepath.Join(t.TempDir(), ".scion")
	require.NoError(t, os.MkdirAll(projectDir, 0o755))
	writeProvisionedAgent(t, projectDir, "restart-agent")
	addLiveContainer(mgr, "restart-agent", projectDir)

	postRecreationRestart(t, srv, "restart-agent", `{
		"resolvedEnv": {"SCION_AGENT_ID": "agent-uuid-restart"},
		"projectPath": "`+projectDir+`",
		"templateName": "web-dev",
		"templateId": "tpl-uuid",
		"templateHash": "sha256:abc"
	}`)
	require.Equal(t, 1, mgr.startCalls)
	require.NotEmpty(t, stub.getAgents, "the template must be read")
	for _, got := range stub.getAgents {
		assert.Equal(t, "agent-uuid-restart", got)
	}
}

// The broker advertises dispatchBoundCatalogRead everywhere the Hub learns
// capabilities: the static set (registration and every heartbeat), the
// registration names, and the info endpoint (ptone/scion#5770).
func TestDispatchBoundCatalogReadCapability(t *testing.T) {
	assert.True(t, StaticCapabilities(nil, "").DispatchBoundCatalogRead, "static set")
	assert.Contains(t, StaticCapabilityNames(nil), "dispatchBoundCatalogRead", "registration names")

	client := &mockRuntimeBrokerService{}
	svc := NewHeartbeatService(client, "b", time.Hour, &heartbeatMockManager{}, nil, slog.Default())
	hb := lastHeartbeat(t, svc, client)
	require.NotNil(t, hb.Capabilities)
	assert.True(t, hb.Capabilities.DispatchBoundCatalogRead, "heartbeat")

	srv := newTestServer(t)
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/info", nil))
	require.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), `"dispatchBoundCatalogRead":true`, "info")
}

// A start (async start launches name their operation, ptone/scion#4349)
// reports the same clear text under the start operation.
func TestClassifyStartErrorCode_DispatchTemplateUnreadable_StartOp(t *testing.T) {
	err := &dispatchTemplateUnreadableError{template: "web-dev", cause: hubNotFound()}
	code, msg := classifyStartErrorCode(err, "web-dev", opStartAgent)
	assert.Equal(t, api.LaunchCodeTemplateNotFound, code)
	assert.True(t, strings.HasPrefix(msg, failedOpText(opStartAgent)+": "), "a start names the start operation, got %q", msg)
	assert.Contains(t, msg, "template web-dev is no longer readable for this agent")
}

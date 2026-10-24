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
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/GoogleCloudPlatform/scion/pkg/api"
	"github.com/GoogleCloudPlatform/scion/pkg/config"
	"github.com/GoogleCloudPlatform/scion/pkg/hubclient"
	"github.com/GoogleCloudPlatform/scion/pkg/runtime"
	"github.com/GoogleCloudPlatform/scion/pkg/storage"
)

// A start sent with gatherEnv runs create's env-gather preflight before it
// registers, resolves its context or touches the runtime, and answers unmet
// required keys with a needs report (ptone/scion#6101).

// startGatherSettings declares MARKER_KEY as a passthrough marker. It names
// no profile: a start resolves its runtime strictly from a named profile,
// and these tests run on the default (mock) runtime.
const startGatherSettings = `
schema_version: "1"
harness_configs:
  claude:
    harness: claude
    env:
      MARKER_KEY: ""
`

// startGatherServer is a broker for start env-gather tests: a project with
// the claude harness-config (claudeAuthBlock) and startGatherSettings, an
// env-capturing manager, and a runtime that counts List calls.
type startGatherServer struct {
	srv        *Server
	mgr        *envCapturingManager
	projectDir string
	lists      *atomic.Int32
}

func newStartGatherServer(t *testing.T, settings string) *startGatherServer {
	t.Helper()
	for _, k := range []string{"ANTHROPIC_API_KEY", "GEMINI_API_KEY", "GOOGLE_API_KEY", "MARKER_KEY", "DECLARED_KEY"} {
		t.Setenv(k, "")
	}
	unsetHostGCPEnv(t)
	_, _, projectDir := newTestServerWithHarnessConfig(t, "claude",
		"harness: claude\nimage: test-image\nuser: scion\n"+claudeAuthBlock, settings)
	cfg := DefaultServerConfig()
	cfg.BrokerID = "test-broker-id"
	cfg.BrokerName = "test-host"
	cfg.StateDir = t.TempDir()
	cfg.ForceRuntime = "mock"
	lists := &atomic.Int32{}
	rt := &runtime.MockRuntime{
		NameFunc: func() string { return "docker" },
		ListFunc: func(context.Context, map[string]string) ([]api.AgentInfo, error) {
			lists.Add(1)
			return nil, nil
		},
	}
	mgr := &envCapturingManager{}
	return &startGatherServer{srv: New(cfg, mgr, rt, config.RuntimeBuildRecord{}), mgr: mgr, projectDir: projectDir, lists: lists}
}

// start posts a start of agent name with body fields merged over the
// project path, harness config and profile every hub start sends.
func (g *startGatherServer) start(t *testing.T, name string, fields map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	body := map[string]any{
		"projectPath":   g.projectDir,
		"harnessConfig": "claude",
		"templateName":  "claude",
		// The hub sends the agent's profile on every start, here none.
		"profile": "",
	}
	for k, v := range fields {
		if v == nil {
			delete(body, k)
			continue
		}
		body[k] = v
	}
	data, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/agents/"+name+"/start?agentId=hub-"+name, strings.NewReader(string(data)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	g.srv.Handler().ServeHTTP(w, req)
	return w
}

// writeAgentConfig writes agent name's scion-agent.json in the project.
func (g *startGatherServer) writeAgentConfig(t *testing.T, name, cfgJSON string) {
	t.Helper()
	projectDir, err := config.GetResolvedProjectDir(g.projectDir)
	if err != nil {
		t.Fatal(err)
	}
	dir := config.GetAgentDir(projectDir, name, false)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "scion-agent.json"), []byte(cfgJSON), 0o644); err != nil {
		t.Fatal(err)
	}
}

func decodeNeeds(t *testing.T, w *httptest.ResponseRecorder) EnvRequirementsResponse {
	t.Helper()
	if w.Code != http.StatusAccepted {
		t.Fatalf("status %d, want 202: %s", w.Code, w.Body.String())
	}
	var got EnvRequirementsResponse
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !got.EnvRequirements {
		t.Fatalf("want a needs report (envRequirements), got %s", w.Body.String())
	}
	return got
}

func decodeStarted(t *testing.T, w *httptest.ResponseRecorder) CreateAgentResponse {
	t.Helper()
	if w.Code != http.StatusAccepted {
		t.Fatalf("status %d, want 202: %s", w.Code, w.Body.String())
	}
	var got CreateAgentResponse
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Agent == nil || strings.Contains(w.Body.String(), `"envRequirements"`) {
		t.Fatalf("want a started agent, got %s", w.Body.String())
	}
	return got
}

func sorted(in []string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	return out
}

func TestStartEnvGather_UnmetAuthKeyAnswersNeedsAndStartsNothing(t *testing.T) {
	g := newStartGatherServer(t, startGatherSettings)
	// The answer comes before the start registers under the agent's name:
	// the registration hook never runs, and a launch already registered
	// under the name is not superseded (a registration would cancel it).
	registered := 0
	testHookNameRegistrationBeforeBegin = func(launchKey) { registered++ }
	t.Cleanup(func() { testHookNameRegistrationBeforeBegin = nil })
	cancelled := false
	existing := newLaunchRecord("other-launch", "hub-other", "start", "", time.Time{}, func() { cancelled = true })
	key := launchKey{Slug: "agent-b1"}
	g.srv.launchRegistry.Begin(key, existing)
	t.Cleanup(func() { g.srv.launchRegistry.Finish(key, existing) })
	w := g.start(t, "agent-b1", map[string]any{
		"gatherEnv":   true,
		"resolvedEnv": map[string]string{"MARKER_KEY": "v"},
		// Async fields: a needs answer must come before the name
		// registration an async start makes.
		"asyncLaunch": true, "launchId": "launch-1", "launchTimeoutSeconds": 600,
	})
	got := decodeNeeds(t, w)
	if !reflect.DeepEqual(got.Needs, []string{"ANTHROPIC_API_KEY"}) {
		t.Errorf("needs = %v, want [ANTHROPIC_API_KEY]", got.Needs)
	}
	if len(got.MarkerOnly) != 0 {
		t.Errorf("markerOnly = %v, want none (an auth key)", got.MarkerOnly)
	}
	if got.AgentID != "hub-agent-b1" {
		t.Errorf("agentId = %q, want the hub agent ID", got.AgentID)
	}
	if g.mgr.StartCalls() != 0 {
		t.Error("Start was called on a needs answer")
	}
	if registered != 0 {
		t.Errorf("the start registered under the agent's name %d time(s) before its needs answer", registered)
	}
	if cancelled {
		t.Error("the launch already registered under the name was superseded")
	}
	// Nothing is left behind: no tracked start.
	if keys := g.srv.startsInFlight.keys(); len(keys) != 0 {
		t.Errorf("tracked starts left behind: %v", keys)
	}
}

func TestStartEnvGather_NeedsAnswerComesBeforeProjectRecovery(t *testing.T) {
	// A start naming no project would recover its project path from the
	// agent's container (a manager List); a needs answer comes first.
	g := newStartGatherServer(t, startGatherSettings)
	// With no project named, the preflight reads the broker's global
	// settings, as create does.
	globalDir, err := config.GetGlobalDir()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(globalDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(globalDir, "settings.yaml"), []byte(startGatherSettings), 0o644); err != nil {
		t.Fatal(err)
	}
	w := g.start(t, "agent-b1b", map[string]any{"gatherEnv": true, "projectPath": nil})
	decodeNeeds(t, w)
	g.mgr.mu.Lock()
	listed := g.mgr.lastListFilter != nil
	g.mgr.mu.Unlock()
	if listed || g.lists.Load() != 0 {
		t.Error("the agent's containers were listed (project-path recovery) before the needs answer")
	}
	if g.mgr.StartCalls() != 0 {
		t.Error("Start was called on a needs answer")
	}
}

func TestStartEnvGather_SatisfiedStartsWithEcho(t *testing.T) {
	cases := []struct {
		name   string
		fields map[string]any
		cfg    string
	}{
		{"resolved env", map[string]any{
			"resolvedEnv": map[string]string{"ANTHROPIC_API_KEY": "v", "MARKER_KEY": "v"},
		}, ""},
		{"resolved secret target", map[string]any{
			"resolvedEnv":     map[string]string{"MARKER_KEY": "v"},
			"resolvedSecrets": []api.ResolvedSecret{{Name: "k", Type: "environment", Target: "ANTHROPIC_API_KEY", Value: "v"}},
		}, ""},
		{"persisted agent config env", map[string]any{
			"resolvedEnv": map[string]string{"MARKER_KEY": "v"},
		}, `{"harness":"claude","env":{"ANTHROPIC_API_KEY":"v"}}`},
		{"inline config env", map[string]any{
			"resolvedEnv":  map[string]string{"MARKER_KEY": "v"},
			"inlineConfig": map[string]any{"env": map[string]string{"ANTHROPIC_API_KEY": "v"}},
		}, `{"harness":"claude"}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			g := newStartGatherServer(t, startGatherSettings)
			name := "agent-b2-" + strings.ReplaceAll(c.name, " ", "-")
			if c.cfg != "" {
				g.writeAgentConfig(t, name, c.cfg)
			}
			fields := map[string]any{"gatherEnv": true}
			for k, v := range c.fields {
				fields[k] = v
			}
			got := decodeStarted(t, g.start(t, name, fields))
			if !got.EnvGathered {
				t.Error("envGathered echo missing")
			}
			if g.mgr.StartCalls() != 1 {
				t.Errorf("Start calls = %d, want 1", g.mgr.StartCalls())
			}
		})
	}
}

func TestStartEnvGather_ResolvedEnvOutranksPersistedConfigEnv(t *testing.T) {
	// The resolved env outranks the stored config env at launch, so an
	// empty resolved value is not satisfied by a stored one.
	g := newStartGatherServer(t, startGatherSettings)
	g.writeAgentConfig(t, "agent-b2b", `{"harness":"claude","env":{"ANTHROPIC_API_KEY":"v"}}`)
	got := decodeNeeds(t, g.start(t, "agent-b2b", map[string]any{
		"gatherEnv":   true,
		"resolvedEnv": map[string]string{"ANTHROPIC_API_KEY": "", "MARKER_KEY": "v"},
	}))
	if !reflect.DeepEqual(got.Needs, []string{"ANTHROPIC_API_KEY"}) {
		t.Errorf("needs = %v, want [ANTHROPIC_API_KEY]", got.Needs)
	}
}

func TestStartEnvGather_MarkerOnlyNeeds(t *testing.T) {
	fields := map[string]any{
		"gatherEnv":   true,
		"resolvedEnv": map[string]string{"ANTHROPIC_API_KEY": "v"},
	}
	t.Run("first pass reports the marker", func(t *testing.T) {
		g := newStartGatherServer(t, startGatherSettings)
		got := decodeNeeds(t, g.start(t, "agent-b3", fields))
		if !reflect.DeepEqual(got.Needs, []string{"MARKER_KEY"}) || !reflect.DeepEqual(got.MarkerOnly, []string{"MARKER_KEY"}) {
			t.Errorf("needs = %v markerOnly = %v, want [MARKER_KEY] for both", got.Needs, got.MarkerOnly)
		}
		if g.mgr.StartCalls() != 0 {
			t.Error("Start was called on a needs answer")
		}
	})
	t.Run("final pass starts with the marker unmet", func(t *testing.T) {
		g := newStartGatherServer(t, startGatherSettings)
		final := map[string]any{"envGatherFinal": true}
		for k, v := range fields {
			final[k] = v
		}
		got := decodeStarted(t, g.start(t, "agent-b3f", final))
		if !got.EnvGathered {
			t.Error("envGathered echo missing")
		}
		if v, ok := g.mgr.lastEnv["MARKER_KEY"]; ok && v != "" {
			t.Errorf("MARKER_KEY = %q, want it unmet", v)
		}
	})
}

func TestStartEnvGather_FinalPassHardKeyUnmet(t *testing.T) {
	g := newStartGatherServer(t, startGatherSettings)
	got := decodeNeeds(t, g.start(t, "agent-b4", map[string]any{
		"gatherEnv": true, "envGatherFinal": true,
	}))
	if !reflect.DeepEqual(sorted(got.Needs), []string{"ANTHROPIC_API_KEY", "MARKER_KEY"}) {
		t.Errorf("needs = %v", got.Needs)
	}
	if !reflect.DeepEqual(got.MarkerOnly, []string{"MARKER_KEY"}) {
		t.Errorf("markerOnly = %v, want [MARKER_KEY]", got.MarkerOnly)
	}
	if g.mgr.StartCalls() != 0 {
		t.Error("Start was called with a required key unmet")
	}
}

func TestStartEnvGather_MarkerThatIsAlsoDeclaredIsRequired(t *testing.T) {
	// A key that is a settings marker and a declared secret is not
	// marker-only: the final pass still answers it as a need.
	g := newStartGatherServer(t, startGatherSettings)
	got := decodeNeeds(t, g.start(t, "agent-b4b", map[string]any{
		"gatherEnv": true, "envGatherFinal": true,
		"resolvedEnv":     map[string]string{"ANTHROPIC_API_KEY": "v"},
		"requiredSecrets": []api.RequiredSecret{{Key: "MARKER_KEY", Description: "declared"}},
	}))
	if !reflect.DeepEqual(got.Needs, []string{"MARKER_KEY"}) || len(got.MarkerOnly) != 0 {
		t.Errorf("needs = %v markerOnly = %v, want [MARKER_KEY] and none", got.Needs, got.MarkerOnly)
	}
	if g.mgr.StartCalls() != 0 {
		t.Error("Start was called with a declared key unmet")
	}
}

func TestStartEnvGather_RequestCannotMarkKeysMarkerOnly(t *testing.T) {
	// markerOnly is computed by the broker; a request field of that name
	// changes nothing.
	g := newStartGatherServer(t, startGatherSettings)
	got := decodeNeeds(t, g.start(t, "agent-b13", map[string]any{
		"gatherEnv": true, "envGatherFinal": true,
		"resolvedEnv": map[string]string{"MARKER_KEY": "v"},
		"markerOnly":  []string{"ANTHROPIC_API_KEY"},
	}))
	if !reflect.DeepEqual(got.Needs, []string{"ANTHROPIC_API_KEY"}) || len(got.MarkerOnly) != 0 {
		t.Errorf("needs = %v markerOnly = %v", got.Needs, got.MarkerOnly)
	}
}

func TestStartEnvGather_NoGatherEnvRunsNoPreflight(t *testing.T) {
	// An older hub sends no gatherEnv: the start is as before, with no echo.
	g := newStartGatherServer(t, startGatherSettings)
	got := decodeStarted(t, g.start(t, "agent-b7", nil))
	if got.EnvGathered {
		t.Error("envGathered set without gatherEnv")
	}
	if g.mgr.StartCalls() != 1 {
		t.Errorf("Start calls = %d, want 1", g.mgr.StartCalls())
	}
}

func TestStartEnvGather_NoAuthSkipsPreflightAndEchoes(t *testing.T) {
	g := newStartGatherServer(t, startGatherSettings)
	got := decodeStarted(t, g.start(t, "agent-b8", map[string]any{"gatherEnv": true, "noAuth": true}))
	if !got.EnvGathered {
		t.Error("envGathered echo missing on the noAuth skip")
	}
	if g.mgr.StartCalls() != 1 {
		t.Errorf("Start calls = %d, want 1", g.mgr.StartCalls())
	}
}

func TestStartEnvGather_ReplayStartIsNeverAFreshProvision(t *testing.T) {
	// The replay is a start, never a create: FreshProvision (which lets
	// GetAgent clear a git-clone workspace) stays false, with the agent
	// directory present or gone, and with a git clone configured.
	for _, dirPresent := range []bool{true, false} {
		g := newStartGatherServer(t, startGatherSettings)
		name := "agent-b14-gone"
		if dirPresent {
			name = "agent-b14-present"
			g.writeAgentConfig(t, name, `{"harness":"claude"}`)
		}
		got := decodeStarted(t, g.start(t, name, map[string]any{
			"gatherEnv": true, "envGatherFinal": true,
			"resolvedEnv": map[string]string{"ANTHROPIC_API_KEY": "v", "MARKER_KEY": "v"},
			"gitClone":    map[string]any{"url": "https://example.com/repo.git"},
		}))
		if !got.EnvGathered {
			t.Error("envGathered echo missing")
		}
		g.mgr.mu.Lock()
		fresh := g.mgr.lastStartOpts.FreshProvision
		g.mgr.mu.Unlock()
		if fresh {
			t.Errorf("dir present=%v: the replay start ran as a fresh provision", dirPresent)
		}
	}
}

func TestStartEnvGather_ParityWithCreate(t *testing.T) {
	// The same agent scored as a create request and as the start the hub
	// derives from it (the applied config env travels in resolvedEnv at
	// start) has the same verdict.
	srvCases := []struct {
		name     string
		settings string
		create   CreateAgentRequest
	}{
		{"auth and marker", startGatherSettings, CreateAgentRequest{
			Name: "p1", Config: &CreateAgentConfig{Template: "claude", HarnessConfig: "claude"},
		}},
		{"config env satisfies", startGatherSettings, CreateAgentRequest{
			Name: "p2", Config: &CreateAgentConfig{Template: "claude", HarnessConfig: "claude",
				Env: []string{"ANTHROPIC_API_KEY=v"}},
			ResolvedEnv: map[string]string{"MARKER_KEY": "v"},
		}},
		{"declared secrets and as-needed autodetect", startGatherSettings, CreateAgentRequest{
			Name: "p3", Config: &CreateAgentConfig{Template: "claude", HarnessConfig: "claude", HarnessAuth: "vertex-ai"},
			RequiredSecrets:       []api.RequiredSecret{{Key: "DECLARED_KEY"}, {Key: "MARKER_KEY"}},
			AvailableAsNeededKeys: []string{"GOOGLE_CLOUD_PROJECT"},
			ResolvedSecrets:       []api.ResolvedSecret{{Name: "s", Type: "environment", Target: "GOOGLE_CLOUD_REGION", Value: "v"}},
		}},
	}
	for _, c := range srvCases {
		t.Run(c.name, func(t *testing.T) {
			g := newStartGatherServer(t, c.settings)
			createReq := c.create
			createReq.ProjectPath = g.projectDir

			// The start the hub derives: Config.Env moves into resolvedEnv.
			resolved := map[string]string{}
			for k, v := range createReq.ResolvedEnv {
				resolved[k] = v
			}
			for _, e := range createReq.Config.Env {
				kv := strings.SplitN(e, "=", 2)
				resolved[kv[0]] = kv[1]
			}
			profile := createReq.Config.Profile
			startReq := startEnvGatherRequest(startEnvGatherInputs{
				Name:                  createReq.Name,
				TemplateName:          createReq.Config.Template,
				HarnessConfig:         createReq.Config.HarnessConfig,
				Profile:               &profile,
				HarnessAuth:           createReq.Config.HarnessAuth,
				ResolvedEnv:           resolved,
				ResolvedSecrets:       createReq.ResolvedSecrets,
				RequiredSecrets:       createReq.RequiredSecrets,
				AvailableAsNeededKeys: createReq.AvailableAsNeededKeys,
			}, g.projectDir, nil)

			fromCreate := g.srv.evaluateEnvGather(createReq, "", "")
			fromStart := g.srv.evaluateEnvGather(startReq, "", "")
			norm := func(r envGatherResult) envGatherResult {
				r.Required, r.HubHas, r.Needs, r.MarkerOnly = sortedOrNil(r.Required), sortedOrNil(r.HubHas), sortedOrNil(r.Needs), sortedOrNil(r.MarkerOnly)
				return r
			}
			a, b := norm(fromCreate), norm(fromStart)
			if !reflect.DeepEqual(a, b) {
				t.Errorf("create %+v\nstart  %+v", a, b)
			}
		})
	}
}

func sortedOrNil(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	return sorted(in)
}

func TestStartEnvGather_TemplateHydrationFailureFailsTheStart(t *testing.T) {
	// The preflight scores the hub template; when it cannot be hydrated the
	// gathering start fails as a create's preflight does (500
	// template_error), and nothing starts.
	srv, projectDir := newTemplateHydrationFailureServer(t, fmt.Errorf("boom"))
	body, _ := json.Marshal(map[string]any{
		"gatherEnv": true, "projectPath": projectDir, "harnessConfig": "claude", "profile": "default",
		"templateName": "mytpl", "templateId": "tpl-uuid",
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/agents/agent-b15/start", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Scion-Hub-Connection", "hub-1")
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)
	var resp ErrorResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v: %s", err, w.Body.String())
	}
	if w.Code != http.StatusInternalServerError || resp.Error.Code != ErrCodeTemplateError {
		t.Fatalf("got %d %s, want 500 %s: %s", w.Code, resp.Error.Code, ErrCodeTemplateError, w.Body.String())
	}
	if m, ok := srv.manager.(*envCapturingManager); ok && m.StartCalls() != 0 {
		t.Error("Start was called")
	}
}

func TestStaticCapabilities_ReportStartEnvGather(t *testing.T) {
	rt := &runtime.MockRuntime{NameFunc: func() string { return "docker" }}
	if !StaticCapabilities(rt, "").StartEnvGather {
		t.Error("StaticCapabilities lacks startEnvGather")
	}
	found := false
	for _, n := range StaticCapabilityNames(rt) {
		if n == "startEnvGather" {
			found = true
		}
	}
	if !found {
		t.Error("StaticCapabilityNames lacks startEnvGather")
	}
}

func TestStartEnvGather_ScoresHydratedTemplateWithoutHandingItToLaunch(t *testing.T) {
	// Q-C: the preflight scores the hub template a start names (its bundled
	// harness-config sets GOOGLE_CLOUD_PROJECT, so that key is not a need),
	// but the hydration it did is not handed to launch as a template: an
	// existing agent starts from its own state.
	unsetHostGCPEnv(t)
	hc := "harness: claude\nimage: test-image\nuser: scion\nauth_selected_type: vertex-ai\n" + claudeAuthBlock
	srv, _, projectDir := newTestServerWithHarnessConfig(t, "claude", hc, "schema_version: \"1\"\n")
	stor, err := storage.NewLocal(storage.Config{Provider: storage.ProviderLocal, LocalPath: t.TempDir(), Bucket: "local"})
	if err != nil {
		t.Fatalf("NewLocal: %v", err)
	}
	hydratedDir := stor.ObjectFSPath(storage.TemplateStoragePath("", "global", "", "mytpl"))
	writeHarnessConfigDirAt(t, filepath.Join(hydratedDir, "harness-configs", "claude"),
		"harness: claude\nimage: test-image\nuser: scion\nauth_selected_type: vertex-ai\nenv:\n  GOOGLE_CLOUD_PROJECT: hydrated-proj\n"+claudeAuthBlock)
	if err := os.WriteFile(filepath.Join(hydratedDir, "scion-agent.yaml"), []byte("harness_config: claude\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	srv.hubMu.Lock()
	srv.hubConnections["hub-1"] = &HubConnection{
		Name: "hub-1", IsColocated: true, LocalStorage: stor,
		HubClient: &stubHubClient{templates: &stubTemplateService{
			getFunc: func(ctx context.Context, ref string) (*hubclient.Template, error) {
				return &hubclient.Template{ID: "tpl-uuid", Slug: "mytpl", Scope: "global"}, nil
			},
		}},
		Hydrator: newTestHydrator(t),
	}
	srv.hubMu.Unlock()

	r := httptest.NewRequest(http.MethodPost, "/api/v1/agents/agent-b13/start", nil)
	r.Header.Set("X-Scion-Hub-Connection", "hub-1")
	needs, pre, sce := srv.startEnvGather(context.Background(), r, startEnvGatherInputs{
		Name: "agent-b13", ProjectPath: projectDir, TemplateName: "mytpl", TemplateID: "tpl-uuid", HarnessConfig: "claude",
	})
	if sce != nil {
		t.Fatalf("hydration failed: %v", sce)
	}
	if needs == nil {
		t.Fatal("want a needs answer (the region key stays unmet)")
	}
	for _, k := range needs.Needs {
		if k == "GOOGLE_CLOUD_PROJECT" {
			t.Errorf("the preflight did not score the hydrated template: needs=%v", needs.Needs)
		}
	}
	if pre.TemplateDone || pre.TemplatePath != "" {
		t.Errorf("the hydrated template was handed to launch: %+v", pre)
	}
}

func TestStartEnvGather_NeedsAnswerHydratesTheTemplateOnce(t *testing.T) {
	// Through the start handler: a gathering start whose hub template
	// leaves a key unmet hydrates the template once (the preflight) and
	// answers its needs before launch would hydrate anything.
	unsetHostGCPEnv(t)
	hc := "harness: claude\nimage: test-image\nuser: scion\nauth_selected_type: vertex-ai\n" + claudeAuthBlock
	srv, mgr, projectDir := newTestServerWithHarnessConfig(t, "claude", hc, "schema_version: \"1\"\n")
	stor, err := storage.NewLocal(storage.Config{Provider: storage.ProviderLocal, LocalPath: t.TempDir(), Bucket: "local"})
	if err != nil {
		t.Fatalf("NewLocal: %v", err)
	}
	hydratedDir := stor.ObjectFSPath(storage.TemplateStoragePath("", "global", "", "mytpl"))
	writeHarnessConfigDirAt(t, filepath.Join(hydratedDir, "harness-configs", "claude"), hc)
	if err := os.WriteFile(filepath.Join(hydratedDir, "scion-agent.yaml"), []byte("harness_config: claude\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gets := 0
	srv.hubMu.Lock()
	srv.hubConnections["hub-1"] = &HubConnection{
		Name: "hub-1", IsColocated: true, LocalStorage: stor,
		HubClient: &stubHubClient{templates: &stubTemplateService{
			getFunc: func(ctx context.Context, ref string) (*hubclient.Template, error) {
				gets++
				return &hubclient.Template{ID: "tpl-uuid", Slug: "mytpl", Scope: "global"}, nil
			},
		}},
		Hydrator: newTestHydrator(t),
	}
	srv.hubMu.Unlock()

	body, _ := json.Marshal(map[string]any{
		"gatherEnv": true, "projectPath": projectDir, "harnessConfig": "claude", "profile": "",
		"templateName": "mytpl", "templateId": "tpl-uuid",
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/agents/agent-b1h/start", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Scion-Hub-Connection", "hub-1")
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)
	decodeNeeds(t, w)
	if gets != 1 {
		t.Errorf("template hydrated %d time(s), want 1", gets)
	}
	if mgr.StartCalls() != 0 {
		t.Error("Start was called on a needs answer")
	}
}

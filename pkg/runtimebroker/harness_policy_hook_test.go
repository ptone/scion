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

	"github.com/GoogleCloudPlatform/scion/pkg/agent"
	"github.com/GoogleCloudPlatform/scion/pkg/api"
	"github.com/GoogleCloudPlatform/scion/pkg/config"
)

// hookScriptedEntry is what a fake manager reports as the harness-config
// launch resolved, as pkg/agent does at its resolution points.
var hookScriptedEntry = config.HarnessConfigEntry{
	Harness:     "generic",
	Provisioner: &config.HarnessProvisionerConfig{Type: "container-script"},
}

// hookManager is a manager whose launch steps evaluate the policy on their
// context exactly as pkg/agent does (agent.CheckHarnessConfigPolicy) with a
// container-script entry named "hook-hc", at the step named by at.
type hookManager struct {
	provisionCapturingManager
	at string // "preflight", "provision" or "start"
}

func (m *hookManager) check(ctx context.Context, step string) error {
	if m.at != step {
		return nil
	}
	return agent.CheckHarnessConfigPolicy(ctx, "hook-hc", hookScriptedEntry)
}

func (m *hookManager) Preflight(ctx context.Context, opts api.StartOptions) error {
	return m.check(ctx, "preflight")
}

func (m *hookManager) Provision(ctx context.Context, opts api.StartOptions) (*api.ScionConfig, error) {
	if err := m.check(ctx, "provision"); err != nil {
		return nil, err
	}
	return m.provisionCapturingManager.Provision(ctx, opts)
}

func (m *hookManager) Start(ctx context.Context, opts api.StartOptions) (*api.AgentInfo, error) {
	if err := m.check(ctx, "start"); err != nil {
		return nil, err
	}
	return m.provisionCapturingManager.Start(ctx, opts)
}

func hookTestServer(t *testing.T, allow bool, at string) (*Server, *hookManager) {
	t.Helper()
	srv, _, _ := dispatchTestEnv(t, allow)
	mgr := &hookManager{at: at}
	srv.manager = mgr
	return srv, mgr
}

func postJSON(t *testing.T, srv *Server, path, body string) (int, string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)
	return w.Code, w.Body.String()
}

// Each dispatch path attaches the broker's policy to the launch context and
// answers a refusal from any resolution point with the same 403.
func TestHarnessPolicyHook_SameRefusalAtEveryHookPoint(t *testing.T) {
	cases := []struct {
		name, at, path, body string
	}{
		{"create sync start", "start", "/api/v1/agents",
			`{"name": "h1", "id": "id-h1", "slug": "h1", "config": {"template": "none"}}`},
		{"create provision-only", "provision", "/api/v1/agents",
			`{"name": "h2", "id": "id-h2", "slug": "h2", "provisionOnly": true, "config": {"template": "none"}}`},
		{"create async admission", "preflight", "/api/v1/agents",
			`{"name": "h3", "id": "id-h3", "slug": "h3", "asyncLaunch": true, "launchId": "L-h3", "launchTimeoutSeconds": 300, "config": {"template": "none"}}`},
		{"start", "start", "/api/v1/agents/h4/start", `{}`},
		{"restart", "start", "/api/v1/agents/h5/restart", `{}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, _ := hookTestServer(t, false, tc.at)
			code, body := postJSON(t, srv, tc.path, tc.body)
			assertPolicyRefusal(t, code, body, "hook-hc")
		})
	}
	t.Run("allow=true passes", func(t *testing.T) {
		srv, mgr := hookTestServer(t, true, "start")
		code, body := postJSON(t, srv, "/api/v1/agents/h6/start", `{}`)
		if code != http.StatusAccepted || mgr.StartCalls() != 1 {
			t.Fatalf("expected 202 with allow=true, got %d: %s", code, body)
		}
	})
}

// Create admission evaluates the policy on the harness-config launch
// resolves, here named by the template rather than the request, and refuses
// before any side effect: no Provision or Start, no agent directory, no
// staged bundle.
func TestHarnessPolicyHook_CreateRefusesBeforeSideEffects(t *testing.T) {
	srv, mgr, _ := dispatchTestEnv(t, false)
	root := t.TempDir()
	scion := filepath.Join(root, ".scion")
	writeHarnessConfigDirAt(t, filepath.Join(scion, "harness-configs", "tpl-hc"), scriptedHarnessYAML)
	// Templates resolve against the project path as given, as launch
	// resolves them.
	tplDir := filepath.Join(root, "templates", "tplx")
	if err := os.MkdirAll(tplDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tplDir, "scion-agent.yaml"), []byte("harness_config: tpl-hc\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	code, body := postJSON(t, srv, "/api/v1/agents",
		`{"name": "se-agent", "id": "id-se", "slug": "se-agent", "projectPath": "`+root+`", "config": {"template": "tplx"}}`)
	assertPolicyRefusal(t, code, body, "tpl-hc")
	if mgr.provisionCalled || mgr.StartCalls() != 0 {
		t.Error("Provision/Start must not run when create admission refuses")
	}
	if _, err := os.Stat(filepath.Join(scion, "agents", "se-agent")); !os.IsNotExist(err) {
		t.Errorf("agent directory exists after refusal (stat err=%v)", err)
	}
}

// The early check uses the dispatch's explicit name, else the profile or
// settings default; it does not treat the template name as a harness-config
// name.
func TestPolicyHarnessConfigName_NoTemplateNameInference(t *testing.T) {
	srv, _, dotScion := dispatchTestEnv(t, false)
	writeHarnessConfig(t, dotScion, "tplname", scriptedHarnessYAML)
	req := CreateAgentRequest{ProjectPath: dotScion, Config: &CreateAgentConfig{Template: "tplname"}}
	name, _, _, err := srv.lookupHarnessConfigForPolicy(req, "", "")
	if err != nil || name == "tplname" {
		t.Errorf("the template name was used as the harness-config name: name=%q err=%v", name, err)
	}
}

// A stamped create carrying a harness-config ID but no name: the hydrated
// bundle is evaluated.
func TestCreateAgentGate_HydratedBundleWithoutName(t *testing.T) {
	srv, mgr, _ := dispatchTestEnv(t, false)
	attachHubHCStub(t, srv).setBundle(scriptedHarnessYAML)
	code, body := postJSON(t, srv, "/api/v1/agents",
		`{"name": "nn-agent", "id": "id-nn", "slug": "nn-agent", "provisionOnly": true, "config": {"harnessConfigId": "hc-id", "harnessConfigHash": "sha256:dispatch"}}`)
	if code != http.StatusForbidden || !strings.Contains(body, ErrCodeForbidden) {
		t.Fatalf("expected 403 for a hydrated scripted bundle without a name, got %d: %s", code, body)
	}
	if mgr.provisionCalled {
		t.Error("Provision must not run when the gate refuses")
	}
}

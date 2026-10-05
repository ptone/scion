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
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/GoogleCloudPlatform/scion/pkg/agent"
	"github.com/GoogleCloudPlatform/scion/pkg/api"
	"github.com/GoogleCloudPlatform/scion/pkg/runtime"
)

// runtimeSteerFixture builds a docker-default broker whose project settings
// map profile "prov" to docker and profile "steer" to Kubernetes, plus an
// agent dir whose agent-info.json (container-writable) names "steer".
// provenance, when non-empty, is written as the agent's broker-side
// image-provenance.json.
func runtimeSteerFixture(t *testing.T, provenance string) (*Server, string, string) {
	t.Helper()
	srv := newDiscoveryTestServer(t, "docker")
	srv.config.ForceRuntime = ""
	srv.resolveAuxiliaryRuntime = func(_, _, _ string) runtime.Runtime {
		return fakeKubernetesRuntime("cluster-steer", "ns-steer")
	}
	projectDir := t.TempDir()
	writeProjectSettings(t, projectDir, `schema_version: "1"
active_profile: prov
profiles:
  prov:
    runtime: local-docker
  steer:
    runtime: k8s-steer
runtimes:
  local-docker:
    type: docker
  k8s-steer:
    type: kubernetes
    context: cluster-steer
    namespace: ns-steer
`)
	const id = "steer-agent"
	agentDir := filepath.Join(projectDir, "agents", id)
	if err := os.MkdirAll(filepath.Join(agentDir, "home"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(agentDir, "scion-agent.json"), []byte(`{"harness_config": "x"}`), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(agentDir, "home", "agent-info.json"), []byte(`{"name": "steer-agent", "profile": "steer"}`), 0644); err != nil {
		t.Fatal(err)
	}
	if provenance != "" {
		if err := os.WriteFile(filepath.Join(agentDir, "image-provenance.json"), []byte(provenance), 0600); err != nil {
			t.Fatal(err)
		}
	}
	return srv, projectDir, id
}

// TestRuntimeSelectionOpts_ProvenanceProfileSelectsRuntime pins
// ptone/scion#1799: rewriting agent-info.json's profile to one that selects
// a different runtime must not change the runtime a provenance-recorded
// agent starts on. That runtime decides whether the bare-image local-exists
// check runs, and so whether the image_registry prefix is applied.
func TestRuntimeSelectionOpts_ProvenanceProfileSelectsRuntime(t *testing.T) {
	srv, projectDir, id := runtimeSteerFixture(t, `{"version": 1, "profile": "prov"}`)

	saved := agent.GetSavedProfile(id, projectDir)
	if saved != "steer" {
		t.Fatalf("precondition: saved profile = %q, want steer", saved)
	}
	opts := api.StartOptions{Name: id, ProjectPath: projectDir, Profile: saved}
	runtimeOpts, err := runtimeSelectionOpts(opts, id)
	if err != nil {
		t.Fatal(err)
	}
	if runtimeOpts.Profile != "prov" {
		t.Fatalf("runtime-selection profile = %q, want the provisioned profile prov", runtimeOpts.Profile)
	}
	if opts.Profile != "steer" {
		t.Fatalf("runtimeSelectionOpts must not mutate opts; Profile = %q", opts.Profile)
	}
	if _, rtType := srv.resolveManagerForOpts(runtimeOpts); rtType != "docker" {
		t.Fatalf("runtime = %q, want the provisioned docker runtime", rtType)
	}
}

// TestRuntimeSelectionOpts_LegacyAgentKeepsSavedProfile: with no provenance
// file the saved profile still selects the runtime (pre-existing
// behaviour), which also shows the fixture's steer would otherwise apply.
func TestRuntimeSelectionOpts_LegacyAgentKeepsSavedProfile(t *testing.T) {
	srv, projectDir, id := runtimeSteerFixture(t, "")
	opts := api.StartOptions{Name: id, ProjectPath: projectDir, Profile: agent.GetSavedProfile(id, projectDir)}
	runtimeOpts, err := runtimeSelectionOpts(opts, id)
	if err != nil {
		t.Fatal(err)
	}
	if runtimeOpts.Profile != "steer" {
		t.Fatalf("legacy runtime-selection profile = %q, want the saved profile", runtimeOpts.Profile)
	}
	if _, rtType := srv.resolveManagerForOpts(runtimeOpts); rtType != "kubernetes" {
		t.Fatalf("legacy runtime = %q, want kubernetes", rtType)
	}
}

// TestRuntimeSelectionOpts_UnusableProvenanceFailsClosed: an existing but
// unusable provenance file is an error, never a fallback to the saved
// profile.
func TestRuntimeSelectionOpts_UnusableProvenanceFailsClosed(t *testing.T) {
	for _, body := range []string{"{not json", `{"profile": "prov"}`} {
		_, projectDir, id := runtimeSteerFixture(t, body)
		_, err := runtimeSelectionOpts(api.StartOptions{Name: id, ProjectPath: projectDir, Profile: "steer"}, id)
		if err == nil || !strings.Contains(err.Error(), "re-provision the agent") {
			t.Fatalf("body %q: expected an actionable provenance error, got %v", body, err)
		}
	}
}

// TestStartAgent_UnusableImageProvenanceIsConflict: the start handler fails
// closed with 409 (re-provision) on an unusable provenance file and never
// calls Manager.Start.
func TestStartAgent_UnusableImageProvenanceIsConflict(t *testing.T) {
	srv := newTestServer(t)
	mgr := srv.manager.(*mockManager)
	projectDir := t.TempDir()
	agentDir := filepath.Join(projectDir, "agents", "test-agent-1")
	if err := os.MkdirAll(filepath.Join(agentDir, "home"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(agentDir, "scion-agent.json"), []byte(`{}`), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(agentDir, "image-provenance.json"), []byte("{not json"), 0600); err != nil {
		t.Fatal(err)
	}

	body := `{"projectPath": ` + strconvQuote(projectDir) + `}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/agents/test-agent-1/start", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)

	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "re-provision the agent") {
		t.Fatalf("expected 409 with a re-provision hint, got %d: %s", w.Code, w.Body.String())
	}
	if mgr.startCalls != 0 {
		t.Fatalf("Start must not be called, got %d calls", mgr.startCalls)
	}
}

func strconvQuote(s string) string { return strconv.Quote(s) }

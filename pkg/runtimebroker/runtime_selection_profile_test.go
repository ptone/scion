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
	runtimeOpts, err := runtimeSelectionOpts(opts, id, false)
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
	runtimeOpts, err := runtimeSelectionOpts(opts, id, false)
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
		_, err := runtimeSelectionOpts(api.StartOptions{Name: id, ProjectPath: projectDir, Profile: "steer"}, id, false)
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

// classificationFixture is a docker-default broker whose project maps
// profile "local" to docker and "kube" to Kubernetes; the agent's
// agent-info.json (container-writable) saves "kube". provenance, when
// non-empty, is written as the agent's broker-side image-provenance.json.
func classificationFixture(t *testing.T, provenance string) (*Server, string, string) {
	t.Helper()
	cfg := DefaultServerConfig()
	cfg.StateDir = t.TempDir()
	srv, dotScion := newTestServerForStartContextMultiProfile(t, cfg, "docker", "kube", "kubernetes")
	const name = "classified-agent"
	writeSavedAgentProfile(t, dotScion, name, "kube")
	agentDir := filepath.Join(dotScion, "agents", name)
	if err := os.WriteFile(filepath.Join(agentDir, "scion-agent.json"), []byte(`{}`), 0644); err != nil {
		t.Fatal(err)
	}
	if provenance != "" {
		if err := os.WriteFile(filepath.Join(agentDir, "image-provenance.json"), []byte(provenance), 0600); err != nil {
			t.Fatal(err)
		}
	}
	return srv, dotScion, name
}

func buildClassifiedStart(t *testing.T, srv *Server, dotScion, name string) (*startContext, error) {
	t.Helper()
	return srv.buildStartContext(context.Background(), startContextInputs{
		Name:        name,
		ProjectPath: dotScion,
		// No SCION_METADATA_MODE: the runtime-dependent default applies, the
		// path an older hub that omits the mode would take.
		ResolvedEnv: map[string]string{},
		HTTPRequest: httptest.NewRequest(http.MethodPost, "/api/v1/agents/"+name+"/start", nil),
		Operation:   opHTTPStart,
	})
}

// TestBuildStartContext_ClassifiesRuntimeWithProvisionedProfile: start/restart
// classify the runtime with the provisioned profile. With the provenance
// profile on docker and agent-info.json's profile on Kubernetes, the default
// GCP metadata mode is block with the metadata redirect, not the Kubernetes
// passthrough default.
func TestBuildStartContext_ClassifiesRuntimeWithProvisionedProfile(t *testing.T) {
	srv, dotScion, name := classificationFixture(t, `{"version": 1, "profile": "local"}`)
	sc, err := buildClassifiedStart(t, srv, dotScion, name)
	if err != nil {
		t.Fatal(err)
	}
	if sc.RuntimeType != "docker" {
		t.Errorf("classified runtime = %q, want docker (the provisioned profile's)", sc.RuntimeType)
	}
	env := sc.Opts.Env
	if env["SCION_METADATA_MODE"] != "block" || env["GCE_METADATA_HOST"] != "localhost:18380" || env["GCE_METADATA_ROOT"] != "localhost:18380" {
		t.Errorf("metadata env = mode %q host %q root %q, want block with the localhost:18380 redirect",
			env["SCION_METADATA_MODE"], env["GCE_METADATA_HOST"], env["GCE_METADATA_ROOT"])
	}
}

// TestBuildStartContext_CorruptProvenanceIsConflict: an unusable provenance
// file fails buildStartContext with 409 (re-provision), never a fallback to
// the saved profile.
func TestBuildStartContext_CorruptProvenanceIsConflict(t *testing.T) {
	srv, dotScion, name := classificationFixture(t, "{not json")
	_, err := buildClassifiedStart(t, srv, dotScion, name)
	sce, ok := err.(*startContextError)
	if !ok || sce.Status != http.StatusConflict || !strings.Contains(sce.Message, "re-provision the agent") {
		t.Fatalf("expected a 409 re-provision startContextError, got %v", err)
	}
}

// TestBuildStartContext_LegacyAgentClassifiesWithSavedProfile: an agent with
// no provenance file keeps the saved-profile classification (pre-existing
// behaviour): Kubernetes, with its passthrough default.
func TestBuildStartContext_LegacyAgentClassifiesWithSavedProfile(t *testing.T) {
	srv, dotScion, name := classificationFixture(t, "")
	sc, err := buildClassifiedStart(t, srv, dotScion, name)
	if err != nil {
		t.Fatal(err)
	}
	if sc.RuntimeType != "kubernetes" || sc.Opts.Env["SCION_METADATA_MODE"] != "passthrough" {
		t.Errorf("legacy classification = runtime %q mode %q, want kubernetes / passthrough", sc.RuntimeType, sc.Opts.Env["SCION_METADATA_MODE"])
	}
}

// TestRejectRuntimeClassificationMismatch: the late check refuses a start
// whose preliminary classification differs from the authoritative runtime,
// treating Kubernetes spellings as one runtime.
func TestRejectRuntimeClassificationMismatch(t *testing.T) {
	for _, tc := range []struct {
		early, late string
		reject      bool
	}{
		{"docker", "docker", false},
		{"kubernetes", "k8s", false},
		{"docker", "kubernetes", true},
		{"kubernetes", "docker", true},
		{"podman", "docker", true},
	} {
		sce := rejectRuntimeClassificationMismatch(tc.early, tc.late)
		if (sce != nil) != tc.reject {
			t.Errorf("%s vs %s: rejected=%v, want %v", tc.early, tc.late, sce != nil, tc.reject)
		}
		if sce != nil && sce.Status != http.StatusConflict {
			t.Errorf("%s vs %s: status %d, want 409", tc.early, tc.late, sce.Status)
		}
	}
}

// TestRestartAgent_ClassificationMismatchIsConflictBeforeStop: when the
// preliminary classification (buildStartContext) and the authoritative
// runtime disagree — here the fixture's early lookup sees docker and the late
// one Kubernetes — and no more specific rejection applies, the restart is
// refused with 409 before the agent is stopped.
func TestRestartAgent_ClassificationMismatchIsConflictBeforeStop(t *testing.T) {
	srv, mgr, remapRuntime := newTestServerForLateCheckOrdering(t, "actual-agent-name", "url-id", "kubernetes")
	remapRuntime.RunFunc = func(ctx context.Context, config runtime.RunConfig) (string, error) {
		t.Fatal("Start must not run on a classification mismatch")
		return "", nil
	}
	body := `{"resolvedEnv": {"SCION_METADATA_MODE": "passthrough", "SCION_METADATA_MODE_SOURCE": "hub"}}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/agents/url-id/restart", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)

	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "differs from the runtime it resolves to") {
		t.Fatalf("expected 409 classification mismatch, got %d: %s", w.Code, w.Body.String())
	}
	if mgr.stopCalls != 0 {
		t.Errorf("expected no stop before the mismatch rejection, got %d", mgr.stopCalls)
	}
}

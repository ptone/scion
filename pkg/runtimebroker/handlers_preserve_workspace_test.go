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
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A create the hub marks preserveWorkspace (a start it routed through
// create-with-gather, ptone/scion#6096) must not clear an existing
// workspace: opts.FreshProvision stays false, and nothing else changes (no
// reprovision). The flag is refused with provisionOnly or reprovision, and a
// non-boolean value is a bad request.

func postPreserveCreate(t *testing.T, srv *Server, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/agents", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)
	return w
}

func TestCreateAgent_PreserveWorkspaceNeverSetsFreshProvision(t *testing.T) {
	srv, mgr := newTestServerWithProvisionCapture()
	w := postPreserveCreate(t, srv, `{
		"name": "routed-agent",
		"id": "agent-uuid-routed",
		"slug": "routed-agent",
		"preserveWorkspace": true,
		"config": {"template": "claude"}
	}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("expected status %d, got %d: %s", http.StatusCreated, w.Code, w.Body.String())
	}
	if !mgr.startCalled {
		t.Fatal("expected Start to be called for a full create")
	}
	if mgr.lastOpts.FreshProvision {
		t.Error("expected opts.FreshProvision to be false for a preserveWorkspace create")
	}
	if mgr.reprovisionCalled || mgr.provisionCalled {
		t.Error("preserveWorkspace must not take the reprovision or provision-only path")
	}
	var resp CreateAgentResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Reprovisioned {
		t.Error("a preserveWorkspace create must not report reprovisioned")
	}
}

// The control: a plain full create still sets FreshProvision, so a leftover
// same-named agent directory is re-cloned (GoogleCloudPlatform/scion#1931).
func TestCreateAgent_PlainCreateSetsFreshProvision(t *testing.T) {
	srv, mgr := newTestServerWithProvisionCapture()
	w := postPreserveCreate(t, srv, `{
		"name": "plain-agent",
		"id": "agent-uuid-plain",
		"slug": "plain-agent",
		"config": {"template": "claude"}
	}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("expected status %d, got %d: %s", http.StatusCreated, w.Code, w.Body.String())
	}
	if !mgr.lastOpts.FreshProvision {
		t.Error("expected opts.FreshProvision to be true for a plain create")
	}
}

func TestCreateAgent_PreserveWorkspaceRefusals(t *testing.T) {
	for _, tc := range []struct {
		name, body string
	}{
		{"non-boolean value", `{"name": "a", "id": "id-a", "slug": "a", "preserveWorkspace": "yes", "config": {"template": "claude"}}`},
		{"with provisionOnly", `{"name": "a", "id": "id-a", "slug": "a", "preserveWorkspace": true, "provisionOnly": true, "config": {"template": "claude"}}`},
		// Reprovision alone (no provisionOnly), so the reprovision arm of the
		// refusal is pinned on its own.
		{"with reprovision", `{"name": "a", "id": "id-a", "slug": "a", "preserveWorkspace": true, "reprovision": true, "config": {"template": "claude"}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, mgr := newTestServerWithProvisionCapture()
			w := postPreserveCreate(t, srv, tc.body)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("expected status %d, got %d: %s", http.StatusBadRequest, w.Code, w.Body.String())
			}
			if mgr.startCalled || mgr.provisionCalled || mgr.reprovisionCalled {
				t.Error("a refused request must not reach the manager")
			}
		})
	}
}

func TestBrokerCapabilities_AdvertisePreserveWorkspaceCreate(t *testing.T) {
	srv := newTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/info", nil)
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("info status %d: %s", w.Code, w.Body.String())
	}
	var resp BrokerInfoResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	if resp.Capabilities == nil || !resp.Capabilities.PreserveWorkspaceCreate {
		t.Fatalf("/api/v1/info capabilities = %+v, want preserveWorkspaceCreate", resp.Capabilities)
	}
	if !StaticCapabilities(nil, "").PreserveWorkspaceCreate {
		t.Fatal("StaticCapabilities must advertise PreserveWorkspaceCreate")
	}
}

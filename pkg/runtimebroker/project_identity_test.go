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
	"strings"
	"testing"

	"github.com/GoogleCloudPlatform/scion/pkg/config"
)

const identityHubProjectID = "55555555-5555-5555-5555-555555555555"

// identityProjectDir returns a git-style project (<root>/.scion directory)
// whose project-id marker is markerID ("" = no marker).
func identityProjectDir(t *testing.T, markerID string) (root, dotScion string) {
	t.Helper()
	root = filepath.Join(t.TempDir(), "proj")
	dotScion = filepath.Join(root, ".scion")
	if err := os.MkdirAll(dotScion, 0755); err != nil {
		t.Fatal(err)
	}
	if markerID != "" {
		if err := config.WriteProjectID(dotScion, markerID); err != nil {
			t.Fatal(err)
		}
	}
	return root, dotScion
}

func TestVerifySharedProjectIdentity(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	t.Run("matching marker", func(t *testing.T) {
		root, dotScion := identityProjectDir(t, identityHubProjectID)
		for _, p := range []string{root, dotScion} {
			if err := verifySharedProjectIdentity(p, identityHubProjectID); err != nil {
				t.Errorf("%s: %v", p, err)
			}
		}
	})
	t.Run("no marker is unchanged", func(t *testing.T) {
		root, _ := identityProjectDir(t, "")
		if err := verifySharedProjectIdentity(root, identityHubProjectID); err != nil {
			t.Fatal(err)
		}
		if err := verifySharedProjectIdentity(filepath.Join(t.TempDir(), "absent"), identityHubProjectID); err != nil {
			t.Fatalf("no .scion at all: %v", err)
		}
	})
	t.Run("forged marker is denied without leaking the path", func(t *testing.T) {
		root, _ := identityProjectDir(t, "99999999-9999-9999-9999-999999999999")
		err := verifySharedProjectIdentity(root, identityHubProjectID)
		if err == nil {
			t.Fatal("expected a mismatch error")
		}
		if strings.Contains(err.Error(), root) {
			t.Errorf("message must not contain the host path: %v", err)
		}
	})
	t.Run("marker file", func(t *testing.T) {
		for _, tc := range []struct {
			name, id string
			wantErr  bool
		}{
			{"matching", identityHubProjectID, false},
			{"disagreeing", "99999999-9999-9999-9999-999999999999", true},
			{"empty", "", true},
		} {
			root := filepath.Join(t.TempDir(), "hubproj")
			if err := os.MkdirAll(root, 0755); err != nil {
				t.Fatal(err)
			}
			if err := config.WriteProjectMarker(filepath.Join(root, ".scion"), &config.ProjectMarker{ProjectID: tc.id, ProjectName: "hubproj", ProjectSlug: "hubproj"}); err != nil {
				t.Fatal(err)
			}
			if err := verifySharedProjectIdentity(root, identityHubProjectID); (err != nil) != tc.wantErr {
				t.Errorf("%s: err=%v, wantErr=%v", tc.name, err, tc.wantErr)
			}
		}
	})
	t.Run("no-op without a hub ID or path", func(t *testing.T) {
		root, _ := identityProjectDir(t, "99999999-9999-9999-9999-999999999999")
		if verifySharedProjectIdentity(root, "") != nil || verifySharedProjectIdentity("", identityHubProjectID) != nil {
			t.Fatal("expected no-op")
		}
	})
}

// TestCreateAgent_ForgedMarkerDeniedBeforeProvision pins C-ADD-2 on the create
// path: a shared-workspace create whose project-id marker disagrees with the
// Hub project ID is refused with 409 before Manager.Provision runs, so a
// forged marker can never steer the settings ProvisionAgent loads, nor the
// provisioned profile it records in image provenance.
func TestCreateAgent_ForgedMarkerDeniedBeforeProvision(t *testing.T) {
	srv, mgr := newHubDefaultsWiringServer()
	t.Setenv("HOME", t.TempDir())
	root, _ := identityProjectDir(t, "99999999-9999-9999-9999-999999999999")
	body := `{"name": "identity-agent", "id": "agent-uuid-identity", "slug": "identity-agent", "provisionOnly": true,
		"projectId": "` + identityHubProjectID + `", "projectPath": ` + strconvQuote(root) + `,
		"config": {"template": "claude", "sharedWorkspace": true}}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/agents", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "verifies the project identity") {
		t.Fatalf("expected 409 identity mismatch, got %d: %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), root) {
		t.Errorf("response must not contain the host path: %s", w.Body.String())
	}
	if mgr.provisionCalled {
		t.Fatal("Provision must not run when the project identity check fails")
	}
}

// TestCreateAgent_MatchingMarkerProvisions: marker == Hub ID provisions
// normally.
func TestCreateAgent_MatchingMarkerProvisions(t *testing.T) {
	srv, mgr := newHubDefaultsWiringServer()
	t.Setenv("HOME", t.TempDir())
	root, _ := identityProjectDir(t, identityHubProjectID)
	postCreateAgent(t, srv, `{"name": "identity-agent", "id": "agent-uuid-identity", "slug": "identity-agent", "provisionOnly": true,
		"projectId": "`+identityHubProjectID+`", "projectPath": `+strconvQuote(root)+`,
		"config": {"template": "claude", "sharedWorkspace": true}}`)
	if !mgr.provisionCalled {
		t.Fatal("expected Provision to run when the marker matches the Hub project ID")
	}
}

// TestStartRestart_ForgedMarkerIsConflict: shared-workspace start and restart
// with a disagreeing marker are refused with 409 before Start (and, for
// restart, before Stop); the same agent without the shared flag is
// unaffected (the check applies to shared-workspace dispatch only).
func TestStartRestart_ForgedMarkerIsConflict(t *testing.T) {
	for _, op := range []string{"start", "restart"} {
		t.Run(op, func(t *testing.T) {
			srv, mgr, _, dotScion := handlerSteerFixture(t, "")
			if err := config.WriteProjectID(dotScion, "99999999-9999-9999-9999-999999999999"); err != nil {
				t.Fatal(err)
			}
			req := httptest.NewRequest(http.MethodPost, "/api/v1/agents/steer-agent/"+op+"?projectId="+identityHubProjectID,
				strings.NewReader(`{"sharedWorkspace": true, "projectPath": `+strconvQuote(dotScion)+`}`))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			srv.Handler().ServeHTTP(w, req)
			if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "verifies the project identity") {
				t.Fatalf("expected 409 identity mismatch, got %d: %s", w.Code, w.Body.String())
			}
			if mgr.startCalls != 0 || mgr.stopCalls != 0 {
				t.Errorf("expected no Start/Stop, got start=%d stop=%d", mgr.startCalls, mgr.stopCalls)
			}
		})
	}
}

// TestCreateAgent_ForgedMarkerDeniedBeforeEnvGather: env-gather loads project
// settings before buildStartContext, so the create handler checks the project
// identity before it as well.
func TestCreateAgent_ForgedMarkerDeniedBeforeEnvGather(t *testing.T) {
	srv, mgr := newHubDefaultsWiringServer()
	t.Setenv("HOME", t.TempDir())
	root, _ := identityProjectDir(t, "99999999-9999-9999-9999-999999999999")
	body := `{"name": "identity-agent", "id": "agent-uuid-identity", "slug": "identity-agent", "gatherEnv": true,
		"projectId": "` + identityHubProjectID + `", "projectPath": ` + strconvQuote(root) + `,
		"config": {"template": "claude", "sharedWorkspace": true}}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/agents", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "verifies the project identity") {
		t.Fatalf("expected 409 identity mismatch before env-gather, got %d: %s", w.Code, w.Body.String())
	}
	if mgr.provisionCalled {
		t.Fatal("Provision must not run")
	}
}

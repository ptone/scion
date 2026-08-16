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

package hub

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestNativeChatEnabled covers the tri-state toggle. Native chat shipped
// default-on, so an unset config must resolve to enabled — a hub upgraded
// without touching settings.yaml keeps its chat feature.
func TestNativeChatEnabled(t *testing.T) {
	enabled, disabled := true, false

	tests := []struct {
		name    string
		setting *bool
		want    bool
	}{
		{"unset defaults to enabled", nil, true},
		{"explicit true", &enabled, true},
		{"explicit false", &disabled, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := &Server{config: ServerConfig{NativeChatEnabled: tc.setting}}
			if got := srv.nativeChatEnabled(); got != tc.want {
				t.Errorf("nativeChatEnabled() = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestRequireNativeChat covers the per-request guard that replaced the
// startup-time route gate: the chat routes stay registered, so the guard is
// what makes them disappear when an admin turns chat off at runtime.
func TestRequireNativeChat(t *testing.T) {
	enabled, disabled := true, false

	tests := []struct {
		name       string
		setting    *bool
		wantStatus int
		wantCalled bool
	}{
		{"unset passes through", nil, http.StatusOK, true},
		{"explicit true passes through", &enabled, http.StatusOK, true},
		{"explicit false 404s", &disabled, http.StatusNotFound, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := &Server{config: ServerConfig{NativeChatEnabled: tc.setting}}
			called := false
			handler := srv.requireNativeChat(func(w http.ResponseWriter, _ *http.Request) {
				called = true
				w.WriteHeader(http.StatusOK)
			})

			req := httptest.NewRequest(http.MethodGet, "/api/v1/chat/prefs", nil)
			rr := httptest.NewRecorder()
			handler(rr, req)

			if rr.Code != tc.wantStatus {
				t.Errorf("status = %d, want %d", rr.Code, tc.wantStatus)
			}
			if called != tc.wantCalled {
				t.Errorf("handler called = %v, want %v", called, tc.wantCalled)
			}
		})
	}
}

// TestRequireNativeChatFollowsRuntimeToggle pins the hot-reload contract: a
// guard built once at route registration must observe a later config change,
// because ApplySnapshot rewrites the toggle in place.
func TestRequireNativeChatFollowsRuntimeToggle(t *testing.T) {
	srv := &Server{}
	handler := srv.requireNativeChat(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	call := func() int {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/chat/prefs", nil)
		rr := httptest.NewRecorder()
		handler(rr, req)
		return rr.Code
	}

	if got := call(); got != http.StatusOK {
		t.Fatalf("default-on: status = %d, want 200", got)
	}

	disabled := false
	ApplySnapshot(srv, Layer1Snapshot{NativeChatEnabled: &disabled})
	if got := call(); got != http.StatusNotFound {
		t.Errorf("after disable: status = %d, want 404", got)
	}

	enabled := true
	ApplySnapshot(srv, Layer1Snapshot{NativeChatEnabled: &enabled})
	if got := call(); got != http.StatusOK {
		t.Errorf("after re-enable: status = %d, want 200", got)
	}
}

// TestPublicSettingsNativeChat verifies the toggle reaches the web UI through
// the public (non-admin) settings endpoint, which is how the client decides
// whether to expose the chat routes.
func TestPublicSettingsNativeChat(t *testing.T) {
	disabled := false

	tests := []struct {
		name    string
		setting *bool
		want    bool
	}{
		{"unset is reported as enabled", nil, true},
		{"disabled is reported as disabled", &disabled, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := &Server{config: ServerConfig{NativeChatEnabled: tc.setting}}

			req := httptest.NewRequest(http.MethodGet, "/api/v1/settings/public", nil)
			rr := httptest.NewRecorder()
			srv.handlePublicSettings(rr, req)

			if rr.Code != http.StatusOK {
				t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
			}

			var resp PublicSettingsResponse
			if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
				t.Fatalf("invalid JSON: %v", err)
			}
			if resp.NativeChatEnabled != tc.want {
				t.Errorf("nativeChatEnabled = %v, want %v", resp.NativeChatEnabled, tc.want)
			}
		})
	}
}

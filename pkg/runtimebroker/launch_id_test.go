package runtimebroker

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/GoogleCloudPlatform/scion/pkg/api"
)

// TestLaunchIDRoundTrip covers a launch id on synchronous create, start and
// restart: it reaches the container as SCION_LAUNCH_ID, and the answer
// carries the effective launch id of the container that answered, which is
// absent when the request carried none.
func TestLaunchIDRoundTrip(t *testing.T) {
	const proposed = "launch-new"
	old, empty := "launch-old", ""
	endpoints := []struct {
		name   string
		path   string
		body   func(launchID string) string
		status int
	}{
		{"create", "/api/v1/agents", func(id string) string {
			return `{"name":"test-agent-1","id":"agent-uuid-1","launchId":"` + id + `","config":{"template":"claude"}}`
		}, http.StatusCreated},
		{"start", "/api/v1/agents/test-agent-1/start", func(id string) string { return `{"launchId":"` + id + `"}` }, http.StatusAccepted},
		{"restart", "/api/v1/agents/test-agent-1/restart", func(id string) string { return `{"launchId":"` + id + `"}` }, http.StatusAccepted},
	}
	cases := []struct {
		name     string
		launchID string
		reused   *string
		wantEnv  string
		want     *string
	}{
		{name: "new container", launchID: proposed, wantEnv: proposed, want: strPtr(proposed)},
		{name: "reused labelled container", launchID: proposed, reused: &old, wantEnv: proposed, want: &old},
		{name: "reused unlabelled container", launchID: proposed, reused: &empty, wantEnv: proposed, want: &empty},
		{name: "no launch id", launchID: "", reused: &old},
	}
	for _, ep := range endpoints {
		for _, tc := range cases {
			t.Run(ep.name+"/"+tc.name, func(t *testing.T) {
				srv := newTestServer(t)
				mgr := srv.manager.(*mockManager)
				mgr.reusedLaunchID = tc.reused

				req := httptest.NewRequest(http.MethodPost, ep.path, strings.NewReader(ep.body(tc.launchID)))
				req.Header.Set("Content-Type", "application/json")
				w := httptest.NewRecorder()
				srv.Handler().ServeHTTP(w, req)
				if w.Code != ep.status {
					t.Fatalf("status = %d, want %d: %s", w.Code, ep.status, w.Body.String())
				}
				if got := mgr.LastStartOpts().Env["SCION_LAUNCH_ID"]; got != tc.wantEnv {
					t.Errorf("SCION_LAUNCH_ID = %q, want %q", got, tc.wantEnv)
				}
				var resp CreateAgentResponse
				if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
					t.Fatalf("decode: %v", err)
				}
				switch {
				case tc.want == nil && resp.EffectiveLaunchID != nil:
					t.Errorf("effectiveLaunchId = %q, want absent", *resp.EffectiveLaunchID)
				case tc.want != nil && resp.EffectiveLaunchID == nil:
					t.Errorf("effectiveLaunchId absent, want %q", *tc.want)
				case tc.want != nil && *resp.EffectiveLaunchID != *tc.want:
					t.Errorf("effectiveLaunchId = %q, want %q", *resp.EffectiveLaunchID, *tc.want)
				}
			})
		}
	}
}

func strPtr(s string) *string { return &s }

// TestLaunchOutcomeNotActed: a start or restart error written before any
// container action carries the not_acted outcome; one written after the
// broker began acting does not, and neither does a success.
func TestLaunchOutcomeNotActed(t *testing.T) {
	paths := map[string]string{
		"start":   "/api/v1/agents/test-agent-1/start",
		"restart": "/api/v1/agents/test-agent-1/restart",
	}
	cases := []struct {
		name         string
		setup        func(*mockManager)
		wantError    bool
		wantNotActed bool
	}{
		{name: "lookup fails before any action", setup: func(m *mockManager) { m.listErr = errors.New("list failed") }, wantError: true, wantNotActed: true},
		{name: "start fails", setup: func(m *mockManager) { m.startErr = errors.New("start failed") }, wantError: true},
		{name: "success", setup: func(*mockManager) {}},
	}
	for op, path := range paths {
		for _, tc := range cases {
			t.Run(op+"/"+tc.name, func(t *testing.T) {
				srv := newTestServer(t)
				tc.setup(srv.manager.(*mockManager))

				req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"launchId":"launch-new"}`))
				req.Header.Set("Content-Type", "application/json")
				w := httptest.NewRecorder()
				srv.Handler().ServeHTTP(w, req)

				if gotError := w.Code >= 400; gotError != tc.wantError {
					t.Fatalf("status = %d, want error %v: %s", w.Code, tc.wantError, w.Body.String())
				}
				got := w.Header().Get(api.HeaderLaunchOutcome)
				if want := map[bool]string{true: api.LaunchOutcomeNotActed}[tc.wantNotActed]; got != want {
					t.Errorf("%s = %q, want %q (status %d)", api.HeaderLaunchOutcome, got, want, w.Code)
				}
			})
		}
	}
}

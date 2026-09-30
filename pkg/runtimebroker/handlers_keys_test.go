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
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/GoogleCloudPlatform/scion/pkg/agentkeys"
)

// postKeys builds and sends a POST /api/v1/agents/{slug}/keys request against
// srv's handler and returns the recorded response.
func postKeys(t *testing.T, srv *Server, slug, projectIDQuery string, body agentkeys.BrokerRequest) *httptest.ResponseRecorder {
	t.Helper()
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	url := "/api/v1/agents/" + slug + "/keys"
	if projectIDQuery != "" {
		url += "?projectId=" + projectIDQuery
	}
	req := httptest.NewRequest(http.MethodPost, url, bytes.NewReader(b))
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)
	return w
}

func decodeBrokerResult(t *testing.T, w *httptest.ResponseRecorder) agentkeys.BrokerResult {
	t.Helper()
	var result agentkeys.BrokerResult
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatalf("decoding BrokerResult from body %q: %v", w.Body.String(), err)
	}
	return result
}

// TestSendKeys_HTTP_Success covers the success path end to end through the
// HTTP mux: 200, agentkeys.OutcomeDispatched, the operation ID echoed back,
// and the handler passing the path slug, query projectID and body fields
// through to Manager.SendKeys unchanged.
func TestSendKeys_HTTP_Success(t *testing.T) {
	var gotProjectID, gotSlug, gotExpectedAgentID, gotKeys string
	mgr := &mockManager{
		sendKeysFunc: func(ctx context.Context, projectID, agentSlug, expectedAgentID, keys string) error {
			gotProjectID, gotSlug, gotExpectedAgentID, gotKeys = projectID, agentSlug, expectedAgentID, keys
			return nil
		},
	}
	srv := newTestServerWithManager(t, mgr)

	reqBody := agentkeys.BrokerRequest{
		ProjectID:     "proj-1",
		AgentID:       "agent-abc",
		OperationID:   "op-123",
		ExecuteBefore: time.Now().UTC().Add(10 * time.Second),
		Keys:          "C-c",
	}
	w := postKeys(t, srv, "test-agent", "proj-1", reqBody)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", w.Code, w.Body.String())
	}
	result := decodeBrokerResult(t, w)
	if result.Outcome != agentkeys.OutcomeDispatched {
		t.Errorf("Outcome = %q, want %q", result.Outcome, agentkeys.OutcomeDispatched)
	}
	if result.OperationID != "op-123" {
		t.Errorf("OperationID = %q, want %q", result.OperationID, "op-123")
	}

	if gotProjectID != "proj-1" || gotSlug != "test-agent" || gotExpectedAgentID != "agent-abc" || gotKeys != "C-c" {
		t.Errorf("SendKeys called with (%q,%q,%q,%q), want (proj-1,test-agent,agent-abc,C-c)",
			gotProjectID, gotSlug, gotExpectedAgentID, gotKeys)
	}
}

// TestSendKeys_HTTP_OutcomeMapping covers that each agentkeys sentinel
// SendKeys can return maps to the exact HTTP status and Outcome the frozen
// contract table assigns it.
func TestSendKeys_HTTP_OutcomeMapping(t *testing.T) {
	cases := []struct {
		name       string
		sendErr    error
		wantStatus int
		wantOut    agentkeys.Outcome
	}{
		{"not_found", agentkeys.ErrTargetNotFound, http.StatusNotFound, agentkeys.OutcomeNotFound},
		{"agent_not_running", agentkeys.ErrAgentNotRunning, http.StatusConflict, agentkeys.OutcomeAgentNotRunning},
		{"terminal_not_ready", agentkeys.ErrTerminalNotReady, http.StatusConflict, agentkeys.OutcomeTerminalNotReady},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mgr := &mockManager{
				sendKeysFunc: func(ctx context.Context, projectID, agentSlug, expectedAgentID, keys string) error {
					return tc.sendErr
				},
			}
			srv := newTestServerWithManager(t, mgr)

			w := postKeys(t, srv, "test-agent", "proj-1", agentkeys.BrokerRequest{
				ProjectID:     "proj-1",
				AgentID:       "agent-abc",
				OperationID:   "op-1",
				ExecuteBefore: time.Now().UTC().Add(10 * time.Second),
				Keys:          "Enter",
			})

			if w.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d; body = %s", w.Code, tc.wantStatus, w.Body.String())
			}
			result := decodeBrokerResult(t, w)
			if result.Outcome != tc.wantOut {
				t.Errorf("Outcome = %q, want %q", result.Outcome, tc.wantOut)
			}
			if result.OperationID != "op-1" {
				t.Errorf("OperationID = %q, want %q", result.OperationID, "op-1")
			}
		})
	}
}

// TestSendKeys_HTTP_ExpiredDeadlineAtAdmission covers "at broker admission":
// an already-past ExecuteBefore must be rejected before Manager.SendKeys is
// ever called, with 503 keys_unavailable.
func TestSendKeys_HTTP_ExpiredDeadlineAtAdmission(t *testing.T) {
	called := false
	mgr := &mockManager{
		sendKeysFunc: func(ctx context.Context, projectID, agentSlug, expectedAgentID, keys string) error {
			called = true
			return nil
		},
	}
	srv := newTestServerWithManager(t, mgr)

	w := postKeys(t, srv, "test-agent", "proj-1", agentkeys.BrokerRequest{
		ProjectID:     "proj-1",
		AgentID:       "agent-abc",
		OperationID:   "op-1",
		ExecuteBefore: time.Now().UTC().Add(-1 * time.Second),
		Keys:          "Enter",
	})

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503; body = %s", w.Code, w.Body.String())
	}
	result := decodeBrokerResult(t, w)
	if result.Outcome != agentkeys.OutcomeKeysUnavailable {
		t.Errorf("Outcome = %q, want %q", result.Outcome, agentkeys.OutcomeKeysUnavailable)
	}
	if called {
		t.Error("Manager.SendKeys must not be called once the admission deadline has already passed")
	}
}

// TestSendKeys_HTTP_MissingDeadline covers the same admission gate for a
// zero ExecuteBefore (a missing/invalid internal deadline) — fail closed,
// never invent a fallback window at the broker.
func TestSendKeys_HTTP_MissingDeadline(t *testing.T) {
	called := false
	mgr := &mockManager{
		sendKeysFunc: func(ctx context.Context, projectID, agentSlug, expectedAgentID, keys string) error {
			called = true
			return nil
		},
	}
	srv := newTestServerWithManager(t, mgr)

	w := postKeys(t, srv, "test-agent", "proj-1", agentkeys.BrokerRequest{
		ProjectID:   "proj-1",
		AgentID:     "agent-abc",
		OperationID: "op-1",
		Keys:        "Enter",
		// ExecuteBefore intentionally zero.
	})

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503; body = %s", w.Code, w.Body.String())
	}
	if called {
		t.Error("Manager.SendKeys must not be called with a missing execute-before deadline")
	}
}

// TestSendKeys_HTTP_AmbiguousFailureIsGenericEnvelope covers "any execution
// error with uncertain terminal effect is represented as unknown, not
// definitely undelivered": an unclassified SendKeys failure must not be
// reported as a well-formed BrokerResult at all (which would tempt a
// Hub-side adapter to trust an outcome the broker never decided) — it must
// be the ordinary, non-BrokerResult error envelope, so the adapter's
// documented "any other malformed/disagreeing response" rule classifies it
// as keys_outcome_unknown.
func TestSendKeys_HTTP_AmbiguousFailureIsGenericEnvelope(t *testing.T) {
	mgr := &mockManager{
		sendKeysFunc: func(ctx context.Context, projectID, agentSlug, expectedAgentID, keys string) error {
			return errors.New("tmux: some ambiguous failure")
		},
	}
	srv := newTestServerWithManager(t, mgr)

	w := postKeys(t, srv, "test-agent", "proj-1", agentkeys.BrokerRequest{
		ProjectID:     "proj-1",
		AgentID:       "agent-abc",
		OperationID:   "op-1",
		ExecuteBefore: time.Now().UTC().Add(10 * time.Second),
		Keys:          "Enter",
	})

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body = %s", w.Code, w.Body.String())
	}
	var result agentkeys.BrokerResult
	if err := json.Unmarshal(w.Body.Bytes(), &result); err == nil && result.Outcome != "" {
		t.Errorf("ambiguous failure must not decode as a well-formed BrokerResult with a non-empty Outcome, got %+v", result)
	}
}

// TestSendKeys_HTTP_MethodNotAllowed covers that the route only accepts
// POST, matching api.RuntimeBrokerAgentActionMethod's registration for
// AgentActionKeys.
func TestSendKeys_HTTP_MethodNotAllowed(t *testing.T) {
	srv := newTestServerWithManager(t, &mockManager{})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/agents/test-agent/keys", nil)
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405; body = %s", w.Code, w.Body.String())
	}
}

// TestSendKeys_HTTP_NoLeakOfDistinctiveSecret is the log-capture test: it
// sends a distinctive secret as the keys payload and asserts it appears
// nowhere in the captured debug/info/warn log output across every outcome
// path (success, each sentinel, and an ambiguous failure whose error text
// might otherwise be logged).
func TestSendKeys_HTTP_NoLeakOfDistinctiveSecret(t *testing.T) {
	const secret = "AK-SENTINEL-do-not-leak-8f3c1e9b"

	var buf bytes.Buffer
	origWriter := log.Writer()
	origFlags := log.Flags()
	log.SetOutput(&buf)
	log.SetFlags(0)
	t.Cleanup(func() {
		log.SetOutput(origWriter)
		log.SetFlags(origFlags)
	})
	origLevel := slog.SetLogLoggerLevel(slog.LevelDebug)
	t.Cleanup(func() { slog.SetLogLoggerLevel(origLevel) })

	outcomes := []error{
		nil,
		agentkeys.ErrTargetNotFound,
		agentkeys.ErrAgentNotRunning,
		agentkeys.ErrTerminalNotReady,
		errors.New("tmux: failed on " + secret), // a buggy manager that leaked the secret into its error
	}

	for i, sendErr := range outcomes {
		mgr := &mockManager{
			sendKeysFunc: func(ctx context.Context, projectID, agentSlug, expectedAgentID, keys string) error {
				return sendErr
			},
		}
		srv := newTestServerWithManager(t, mgr)

		buf.Reset()
		postKeys(t, srv, "test-agent", "proj-1", agentkeys.BrokerRequest{
			ProjectID:     "proj-1",
			AgentID:       "agent-abc",
			OperationID:   "op-1",
			ExecuteBefore: time.Now().UTC().Add(10 * time.Second),
			Keys:          secret,
		})

		if strings.Contains(buf.String(), secret) {
			t.Errorf("case %d: captured log output contains the distinctive secret\nLog: %s", i, buf.String())
		}
	}
}

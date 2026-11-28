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

package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/GoogleCloudPlatform/scion/pkg/hubclient"
	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestAuditM1_XForwardedForAllowlist(t *testing.T) {
	var mu sync.Mutex
	var gotHeaders http.Header

	fakeHub := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		gotHeaders = r.Header.Clone()
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/api/v1/auth/me":
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "u1", "email": "u1@example.com"})
		case strings.HasSuffix(r.URL.Path, "/agents"):
			_ = json.NewEncoder(w).Encode(map[string]any{"agents": []any{}})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})

	backend := NewBackend(BackendOptions{
		HubBaseURL: "http://in-process",
		Transport:  &InProcessTransport{Handler: fakeHub},
		Version:    "test-m1",
		ExperimentChecker: func(_ context.Context, _ Credential, _ string) (bool, error) {
			return true, nil
		},
		DisableTransportAuth: true,
	})
	srv, err := New(Options{Backend: backend})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	callBody := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"list_agents","arguments":{"project":"11111111-1111-1111-1111-111111111111"}}}`
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/mcp", strings.NewReader(callBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Authorization", "Bearer scion_pat_valid")
	req.Header.Add("X-Forwarded-For", "198.51.100.7, 10.0.0.1")
	req.Header.Set("Cookie", "session=secret")
	req.Header.Set("X-Goog-Iap-Jwt-Assertion", "iap-jwt")
	req.Header.Set("X-Untrusted", "drop-me")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d (%s), want 200", resp.StatusCode, string(body))
	}

	mu.Lock()
	h := gotHeaders
	mu.Unlock()

	xff := h.Values("X-Forwarded-For")
	if len(xff) != 1 || xff[0] != "198.51.100.7, 10.0.0.1" {
		t.Fatalf("X-Forwarded-For forwarded to Hub = %q, want [%q]", xff, "198.51.100.7, 10.0.0.1")
	}
	if h.Get("Cookie") != "" {
		t.Fatalf("Cookie leaked: %q", h.Get("Cookie"))
	}
	if h.Get("X-Goog-Iap-Jwt-Assertion") != "" {
		t.Fatalf("X-Goog-Iap-Jwt-Assertion leaked: %q", h.Get("X-Goog-Iap-Jwt-Assertion"))
	}
	if h.Get("X-Untrusted") != "" {
		t.Fatalf("X-Untrusted leaked: %q", h.Get("X-Untrusted"))
	}
}

func TestAuditL1_NoRedirectsAndDotSegmentRejection(t *testing.T) {
	t.Run("backend_does_not_follow_redirects", func(t *testing.T) {
		var redirectTargetHit atomic.Bool
		fakeHub := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/api/v1/redirect-target" {
				redirectTargetHit.Store(true)
				w.WriteHeader(http.StatusOK)
				return
			}
			http.Redirect(w, r, "/api/v1/redirect-target", http.StatusFound)
		})

		backend := NewBackend(BackendOptions{
			HubBaseURL:           "http://in-process",
			Transport:            &InProcessTransport{Handler: fakeHub},
			DisableTransportAuth: true,
		})
		_, err := backend.ListAgents(context.Background(), Credential{Bearer: "scion_pat_x"}, "11111111-1111-1111-1111-111111111111")
		if err == nil {
			t.Fatal("expected error when Hub responds with 302 redirect")
		}
		if redirectTargetHit.Load() {
			t.Fatal("backend http.Client followed a redirect from the Hub")
		}
	})

	t.Run("dot_segments_rejected", func(t *testing.T) {
		var hubTouched atomic.Bool
		fakeHub := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			if r.URL.Path == "/api/v1/auth/me" {
				_ = json.NewEncoder(w).Encode(map[string]any{"id": "u1", "email": "u1@example.com"})
				return
			}
			hubTouched.Store(true)
			w.WriteHeader(http.StatusOK)
		})

		backend := NewBackend(BackendOptions{
			HubBaseURL: "http://in-process",
			Transport:  &InProcessTransport{Handler: fakeHub},
			ExperimentChecker: func(_ context.Context, _ Credential, _ string) (bool, error) {
				return true, nil
			},
			DisableTransportAuth: true,
		})
		srv, err := New(Options{Backend: backend})
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		ts := httptest.NewServer(srv.Handler())
		defer ts.Close()

		ctx := context.Background()
		cl := mcp.NewClient(&mcp.Implementation{Name: "l1-test", Version: "1.0"}, nil)
		sess, err := cl.Connect(ctx, &mcp.StreamableClientTransport{
			Endpoint: ts.URL + "/mcp",
			HTTPClient: &http.Client{
				Transport: &staticHeaderTransport{
					base:    &http.Transport{DisableKeepAlives: true},
					headers: map[string]string{"Authorization": "Bearer scion_pat_l1"},
				},
			},
		}, nil)
		if err != nil {
			t.Fatalf("Connect: %v", err)
		}
		defer func() { _ = sess.Close() }()

		badCalls := []struct {
			name string
			args map[string]any
		}{
			{"list_agents", map[string]any{"project": ".."}},
			{"list_agents", map[string]any{"project": "."}},
			{"list_agents", map[string]any{"project": ""}},
			{"send_message", map[string]any{"project": "11111111-1111-1111-1111-111111111111", "agent": "..", "text": "hi"}},
			{"send_message", map[string]any{"project": "..", "agent": "echo-t", "text": "hi"}},
			{"send_message", map[string]any{"conversation_id": "..", "text": "hi"}},
			{"list_conversations", map[string]any{"project": ".."}},
			{"read_messages", map[string]any{"conversation_id": ".."}},
			{"read_messages", map[string]any{"conversation_id": "."}},
			{"wait_for_messages", map[string]any{"conversation_id": ".."}},
		}
		for _, tc := range badCalls {
			res, err := sess.CallTool(ctx, &mcp.CallToolParams{Name: tc.name, Arguments: tc.args})
			if err != nil {
				t.Fatalf("%s(%v) protocol error: %v", tc.name, tc.args, err)
			}
			if !res.IsError {
				t.Fatalf("%s(%v): expected tool error for dot/empty segment, got success", tc.name, tc.args)
			}
		}

		for _, badURI := range []string{
			"scion://conversations/..",
			"scion://conversations/.",
			"scion://conversations/conv-1/messages/..",
			"scion://conversations/../messages/msg-1",
		} {
			if _, err := sess.ReadResource(ctx, &mcp.ReadResourceParams{URI: badURI}); err == nil {
				t.Fatalf("ReadResource(%q): expected error for dot segment", badURI)
			}
		}

		if hubTouched.Load() {
			t.Fatal("dot-segment inputs reached the Hub backend")
		}
	})
}

func TestAuditL2_PrincipalCacheBoundedByLiveEntries(t *testing.T) {
	const capLimit = 16
	cache := newPrincipalCacheWithCap(time.Minute, capLimit)
	now := time.Now()

	for i := 0; i < capLimit*4; i++ {
		key := fmt.Sprintf("cred-hash-%d", i)
		cache.set(key, Principal{ID: fmt.Sprintf("user-%d", i)}, now)
		if got := cache.len(); got > capLimit {
			t.Fatalf("after insert %d: cache.len() = %d, exceeds cap %d", i, got, capLimit)
		}
	}
	if got := cache.len(); got != capLimit {
		t.Fatalf("final cache.len() = %d, want %d", got, capLimit)
	}

	// Updating an existing key must not grow past capLimit.
	existing := fmt.Sprintf("cred-hash-%d", capLimit*4-1)
	cache.set(existing, Principal{ID: "updated"}, now)
	if got := cache.len(); got != capLimit {
		t.Fatalf("after update: cache.len() = %d, want %d", got, capLimit)
	}
	if p, ok := cache.get(existing, now); !ok || p.ID != "updated" {
		t.Fatalf("updated entry = (%+v, %v), want updated", p, ok)
	}
}

func TestStreamLimiter_CapAndRelease(t *testing.T) {
	lim := NewStreamLimiter(2)
	if lim.Max() != 2 {
		t.Fatalf("Max() = %d, want 2", lim.Max())
	}

	r1, ok := lim.Acquire("user-a")
	if !ok {
		t.Fatal("first Acquire failed")
	}
	r2, ok := lim.Acquire("user-a")
	if !ok {
		t.Fatal("second Acquire failed")
	}
	if _, ok := lim.Acquire("user-a"); ok {
		t.Fatal("third Acquire for user-a should have been capped")
	}
	// Different principal has its own cap.
	rB, ok := lim.Acquire("user-b")
	if !ok {
		t.Fatal("Acquire for user-b should succeed independently")
	}
	rB()

	// Release is idempotent.
	r1()
	r1()
	if lim.Active("user-a") != 1 {
		t.Fatalf("Active(user-a) = %d, want 1", lim.Active("user-a"))
	}
	r3, ok := lim.Acquire("user-a")
	if !ok {
		t.Fatal("Acquire after release should succeed")
	}
	r2()
	r3()
	if lim.Active("user-a") != 0 {
		t.Fatalf("Active(user-a) = %d, want 0", lim.Active("user-a"))
	}
}

func TestWaitForMessages_ClientDisconnectCancelsHubWatch(t *testing.T) {
	watchStarted := make(chan struct{})
	watchCancelled := make(chan struct{})

	fakeHub := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/v1/auth/me" {
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "u-cancel", "email": "cancel@example.com"})
			return
		}
		if strings.HasSuffix(r.URL.Path, "/watch") {
			close(watchStarted)
			<-r.Context().Done()
			close(watchCancelled)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	})

	backend := NewBackend(BackendOptions{
		HubBaseURL: "http://in-process",
		Transport:  &InProcessTransport{Handler: fakeHub},
		ExperimentChecker: func(_ context.Context, _ Credential, _ string) (bool, error) {
			return true, nil
		},
		DisableTransportAuth: true,
	})
	srv, err := New(Options{Backend: backend, MaxStreamsPerPrincipal: 2})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	reqCtx, cancel := context.WithCancel(context.Background())
	callBody := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"wait_for_messages","arguments":{"conversation_id":"conv-cancel","timeout_seconds":30}}}`
	req, _ := http.NewRequestWithContext(reqCtx, http.MethodPost, ts.URL+"/mcp", strings.NewReader(callBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Authorization", "Bearer scion_pat_cancel")

	done := make(chan error, 1)
	go func() {
		resp, err := http.DefaultClient.Do(req)
		if resp != nil {
			_ = resp.Body.Close()
		}
		done <- err
	}()

	select {
	case <-watchStarted:
	case <-time.After(3 * time.Second):
		cancel()
		t.Fatal("Hub /watch was not invoked")
	}

	// Simulate client disconnect.
	cancel()

	select {
	case <-watchCancelled:
	case <-time.After(3 * time.Second):
		t.Fatal("Hub /watch context was not cancelled on client disconnect")
	}

	if err := <-done; err == nil {
		t.Error("expected HTTP client error after request cancellation")
	}
	// Wait briefly for deferred stream release to run.
	deadline := time.Now().Add(2 * time.Second)
	for srv.streams.Active("u-cancel") != 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if got := srv.streams.Active("u-cancel"); got != 0 {
		t.Fatalf("stream slot not released after disconnect: active=%d", got)
	}
}

type visitorMockBackend struct {
	HubBackend
	principal Principal
}

func (m *visitorMockBackend) Me(_ context.Context, _ Credential) (Principal, error) {
	return m.principal, nil
}

func (m *visitorMockBackend) ExperimentEnabled(_ context.Context, _ Credential, _ string) (bool, error) {
	return true, nil
}

func TestListConversations_VisitorNotAvailable(t *testing.T) {
	t.Run("principal_kind_visitor", func(t *testing.T) {
		srv, err := New(Options{
			Backend: &visitorMockBackend{
				principal: Principal{Kind: "visitor", ID: "vis-1", Email: "vis@partner.example"},
			},
		})
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		ts := httptest.NewServer(srv.Handler())
		defer ts.Close()

		ctx := context.Background()
		cl := mcp.NewClient(&mcp.Implementation{Name: "vis-test", Version: "1.0"}, nil)
		sess, err := cl.Connect(ctx, &mcp.StreamableClientTransport{
			Endpoint: ts.URL + "/mcp",
			HTTPClient: &http.Client{
				Transport: &staticHeaderTransport{
					base:    &http.Transport{DisableKeepAlives: true},
					headers: map[string]string{HeaderFederationToken: "fed-visitor-jwt"},
				},
			},
		}, nil)
		if err != nil {
			t.Fatalf("Connect: %v", err)
		}
		defer func() { _ = sess.Close() }()

		res, err := sess.CallTool(ctx, &mcp.CallToolParams{Name: "list_conversations", Arguments: map[string]any{}})
		if err != nil {
			t.Fatalf("CallTool: %v", err)
		}
		if res.IsError {
			t.Fatalf("expected structured not_available result, got tool error: %+v", res.Content)
		}
		var out ListConversationsOutput
		if err := json.Unmarshal([]byte(res.Content[0].(*mcp.TextContent).Text), &out); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if !out.NotAvailable || !strings.Contains(strings.ToLower(out.Reason), "visitor") {
			t.Fatalf("got %+v, want NotAvailable=true with visitor reason", out)
		}
	})

	t.Run("hub_visitor_403_fallback", func(t *testing.T) {
		fakeHub := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			if r.URL.Path == "/api/v1/auth/me" {
				_ = json.NewEncoder(w).Encode(map[string]any{"id": "vis-2", "email": "vis2@partner.example"})
				return
			}
			if r.URL.Path == "/api/v1/conversations" {
				w.WriteHeader(http.StatusForbidden)
				_ = json.NewEncoder(w).Encode(map[string]any{
					"error": map[string]any{
						"code":    "forbidden",
						"message": "project visitor credentials are not permitted on this route",
					},
				})
				return
			}
			w.WriteHeader(http.StatusNotFound)
		})
		backend := NewBackend(BackendOptions{
			HubBaseURL: "http://in-process",
			Transport:  &InProcessTransport{Handler: fakeHub},
			ExperimentChecker: func(_ context.Context, _ Credential, _ string) (bool, error) {
				return true, nil
			},
			DisableTransportAuth: true,
		})
		srv, err := New(Options{Backend: backend})
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		ts := httptest.NewServer(srv.Handler())
		defer ts.Close()

		ctx := context.Background()
		cl := mcp.NewClient(&mcp.Implementation{Name: "vis-403-test", Version: "1.0"}, nil)
		sess, err := cl.Connect(ctx, &mcp.StreamableClientTransport{
			Endpoint: ts.URL + "/mcp",
			HTTPClient: &http.Client{
				Transport: &staticHeaderTransport{
					base:    &http.Transport{DisableKeepAlives: true},
					headers: map[string]string{HeaderFederationToken: "fed-visitor-jwt-2"},
				},
			},
		}, nil)
		if err != nil {
			t.Fatalf("Connect: %v", err)
		}
		defer func() { _ = sess.Close() }()

		res, err := sess.CallTool(ctx, &mcp.CallToolParams{Name: "list_conversations", Arguments: map[string]any{}})
		if err != nil {
			t.Fatalf("CallTool: %v", err)
		}
		if res.IsError {
			t.Fatalf("expected structured not_available result on visitor 403, got error: %+v", res.Content)
		}
		var out ListConversationsOutput
		if err := json.Unmarshal([]byte(res.Content[0].(*mcp.TextContent).Text), &out); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if !out.NotAvailable {
			t.Fatalf("got %+v, want NotAvailable=true", out)
		}
	})
}

func TestSendMessage_TargetXORAndUnknownAgentCandidates(t *testing.T) {
	fakeHub := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/api/v1/auth/me":
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "u1", "email": "u1@example.com"})
		case strings.HasSuffix(r.URL.Path, "/agents/unknown-agent/message"):
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"error": map[string]any{"code": "not_found", "message": "agent not found"},
			})
		case strings.HasSuffix(r.URL.Path, "/agents"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"agents": []map[string]any{
					{"id": "ag-1", "slug": "echo-t", "name": "Echo T", "projectId": "11111111-1111-1111-1111-111111111111", "phase": "running"},
					{"id": "ag-2", "slug": "worker-2", "name": "Worker 2", "projectId": "11111111-1111-1111-1111-111111111111", "phase": "stopped"},
				},
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})

	backend := NewBackend(BackendOptions{
		HubBaseURL: "http://in-process",
		Transport:  &InProcessTransport{Handler: fakeHub},
		ExperimentChecker: func(_ context.Context, _ Credential, _ string) (bool, error) {
			return true, nil
		},
		DisableTransportAuth: true,
	})
	srv, err := New(Options{Backend: backend})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	ctx := context.Background()
	cl := mcp.NewClient(&mcp.Implementation{Name: "xor-test", Version: "1.0"}, nil)
	sess, err := cl.Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint: ts.URL + "/mcp",
		HTTPClient: &http.Client{
			Transport: &staticHeaderTransport{
				base:    &http.Transport{DisableKeepAlives: true},
				headers: map[string]string{"Authorization": "Bearer scion_pat_xor"},
			},
		},
	}, nil)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer func() { _ = sess.Close() }()

	// Both target forms -> error.
	res, err := sess.CallTool(ctx, &mcp.CallToolParams{
		Name: "send_message",
		Arguments: map[string]any{
			"project":         "11111111-1111-1111-1111-111111111111",
			"agent":           "echo-t",
			"conversation_id": "conv-1",
			"text":            "hi",
		},
	})
	if err != nil || !res.IsError {
		t.Fatalf("both targets: err=%v isError=%v, want tool error", err, res != nil && res.IsError)
	}

	// Neither target form -> error.
	res, err = sess.CallTool(ctx, &mcp.CallToolParams{
		Name:      "send_message",
		Arguments: map[string]any{"text": "hi"},
	})
	if err != nil || !res.IsError {
		t.Fatalf("neither target: err=%v isError=%v, want tool error", err, res != nil && res.IsError)
	}

	// Unknown agent -> includes candidate list from list_agents (§4.4).
	res, err = sess.CallTool(ctx, &mcp.CallToolParams{
		Name: "send_message",
		Arguments: map[string]any{
			"project": "11111111-1111-1111-1111-111111111111",
			"agent":   "unknown-agent",
			"text":    "hi",
		},
	})
	if err != nil || !res.IsError {
		t.Fatalf("unknown agent: err=%v res=%+v, want tool error", err, res)
	}
	msg := res.Content[0].(*mcp.TextContent).Text
	if !strings.Contains(msg, "echo-t") || !strings.Contains(msg, "worker-2") {
		t.Fatalf("expected candidate agents in error message, got: %q", msg)
	}
}

func TestServerOptions_DefaultsAndValidation(t *testing.T) {
	backend := NewBackend(BackendOptions{DisableTransportAuth: true})

	srv, err := New(Options{Backend: backend})
	if err != nil {
		t.Fatalf("New default: %v", err)
	}
	if srv.opts.MaxStreamsPerPrincipal != DefaultMaxStreamsPerPrincipal {
		t.Fatalf("MaxStreamsPerPrincipal = %d, want %d", srv.opts.MaxStreamsPerPrincipal, DefaultMaxStreamsPerPrincipal)
	}
	if srv.opts.StreamWait != DefaultStreamWait {
		t.Fatalf("StreamWait = %s, want %s", srv.opts.StreamWait, DefaultStreamWait)
	}

	if _, err := New(Options{Backend: backend, MaxStreamsPerPrincipal: -1}); err == nil {
		t.Fatal("expected error for negative MaxStreamsPerPrincipal")
	}
	if _, err := New(Options{Backend: backend, StreamWait: -time.Second}); err == nil {
		t.Fatal("expected error for negative StreamWait")
	}

	// Verify AcquireStream returns *jsonrpc.Error with CodeResourceExhausted (-32025).
	srv1, err := New(Options{Backend: backend, MaxStreamsPerPrincipal: 1})
	if err != nil {
		t.Fatalf("New(cap=1): %v", err)
	}
	rel, err := srv1.AcquireStream("p1")
	if err != nil {
		t.Fatalf("first AcquireStream: %v", err)
	}
	defer rel()
	_, err = srv1.AcquireStream("p1")
	var jErr *jsonrpc.Error
	if !errors.As(err, &jErr) || jErr.Code != CodeResourceExhausted {
		t.Fatalf("capped AcquireStream err = %v, want *jsonrpc.Error code %d", err, CodeResourceExhausted)
	}
}

func TestMessageDTO_Hardening(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	raw := &hubclient.Message{
		ID:                    "msg-42",
		ConversationID:        "conv-9",
		Sender:                "visitor:bob@partner.example",
		SenderID:              "u-bob",
		Recipient:             "agent:echo-t",
		Msg:                   "untrusted payload",
		CreatedAt:             now,
		DispatchFailureReason: strPtr("internal-secret-reason"),
	}
	dto := projectHubMessageDTO(raw, "conv-9")
	if dto.Sender.Kind != "user" {
		t.Fatalf("Sender.Kind = %q, want user", dto.Sender.Kind)
	}
	if dto.ResourceURI != "scion://conversations/conv-9/messages/msg-42" {
		t.Fatalf("ResourceURI = %q", dto.ResourceURI)
	}
	b, err := json.Marshal(dto)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(b), "internal-secret-reason") {
		t.Fatalf("raw internal store field leaked into MessageDTO JSON: %s", string(b))
	}
}

func strPtr(s string) *string { return &s }

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
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/GoogleCloudPlatform/scion/pkg/experiments"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// TestStandalone_HeaderAllowlistStripsCookieAndIAP verifies §10 AC4 in
// standalone mode (network transport): Cookie and X-Goog-Iap-Jwt-Assertion sent
// to /mcp never reach the Hub.
func TestStandalone_HeaderAllowlistStripsCookieAndIAP(t *testing.T) {
	var mu sync.Mutex
	var captured []http.Header

	fakeHub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		captured = append(captured, r.Header.Clone())
		mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/api/v1/experiments":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"experiments": map[string]bool{experiments.MCPServer: true},
			})
		case r.URL.Path == "/api/v1/auth/me":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id":          "user-1",
				"email":       "alice@example.com",
				"displayName": "Alice",
			})
		case strings.HasSuffix(r.URL.Path, "/message"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"message_id":      "msg-1",
				"conversation_id": "conv-1",
				"status":          "delivered",
				"cursor":          "cur-1",
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer fakeHub.Close()

	backend := NewBackend(BackendOptions{
		HubBaseURL:           fakeHub.URL,
		Transport:            fakeHub.Client().Transport,
		Version:              "standalone-test",
		DisableTransportAuth: false,
	})
	srv, err := New(Options{Version: "standalone-test", Backend: backend})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	mcpTS := httptest.NewServer(srv.Handler())
	defer mcpTS.Close()

	callBody := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"send_message","arguments":{"project":"11111111-1111-1111-1111-111111111111","agent":"echo-t","text":"hello"}}}`
	req, err := http.NewRequest(http.MethodPost, mcpTS.URL+"/mcp", strings.NewReader(callBody))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Authorization", "Bearer scion_pat_standalone")
	req.Header.Set("Cookie", "session=secret-cookie")
	req.Header.Set("X-Goog-Iap-Jwt-Assertion", "eyJ.iap.assertion")
	req.Header.Set("X-Custom-Untrusted", "drop-me")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST /mcp status = %d (%s), want 200", resp.StatusCode, string(body))
	}

	mu.Lock()
	defer mu.Unlock()
	if len(captured) < 3 {
		t.Fatalf("expected >= 3 Hub requests (experiments, me, message), got %d", len(captured))
	}
	for i, h := range captured {
		if h.Get("Cookie") != "" {
			t.Errorf("request[%d]: Cookie leaked to Hub: %q", i, h.Get("Cookie"))
		}
		if h.Get("X-Goog-Iap-Jwt-Assertion") != "" {
			t.Errorf("request[%d]: X-Goog-Iap-Jwt-Assertion leaked to Hub: %q", i, h.Get("X-Goog-Iap-Jwt-Assertion"))
		}
		if h.Get("X-Custom-Untrusted") != "" {
			t.Errorf("request[%d]: X-Custom-Untrusted leaked to Hub: %q", i, h.Get("X-Custom-Untrusted"))
		}
		if got := h.Get("Authorization"); got != "Bearer scion_pat_standalone" {
			t.Errorf("request[%d]: Authorization = %q, want Bearer scion_pat_standalone", i, got)
		}
		if got := h.Get(HeaderScionClient); got != ClientHeaderValue {
			t.Errorf("request[%d]: X-Scion-Client = %q, want %q", i, got, ClientHeaderValue)
		}
	}
}

// TestStandalone_TransportAuthKeepsCallerAuthorizationSeparate verifies F12:
// when SCION_TRANSPORT_MODE=iap|cloudrun_invoker is set, hubclient.New layers
// the bridge's transport credential into Proxy-Authorization or
// X-Serverless-Authorization without replacing the caller's Authorization.
func TestStandalone_TransportAuthKeepsCallerAuthorizationSeparate(t *testing.T) {
	cases := []struct {
		mode       string
		wantHeader string
	}{
		{mode: "iap", wantHeader: "Proxy-Authorization"},
		{mode: "cloudrun_invoker", wantHeader: "X-Serverless-Authorization"},
	}

	for _, tc := range cases {
		t.Run(tc.mode, func(t *testing.T) {
			const (
				transportTok = "eyJhbGciOiJSUzI1NiJ9.eyJleHAiOjQxMDI0NDQ4MDB9.transport-sig"
				callerTok    = "scion_pat_caller_secret"
			)
			t.Setenv("SCION_TRANSPORT_MODE", tc.mode)
			t.Setenv("SCION_TRANSPORT_TOKEN", transportTok)

			var mu sync.Mutex
			var captured []http.Header

			fakeHub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				captured = append(captured, r.Header.Clone())
				mu.Unlock()

				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/api/v1/experiments":
					_ = json.NewEncoder(w).Encode(map[string]any{
						"experiments": map[string]bool{experiments.MCPServer: true},
					})
				case "/api/v1/auth/me":
					_ = json.NewEncoder(w).Encode(map[string]any{
						"id":    "user-1",
						"email": "alice@example.com",
					})
				default:
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer fakeHub.Close()

			backend := NewBackend(BackendOptions{
				HubBaseURL:           fakeHub.URL,
				Transport:            fakeHub.Client().Transport,
				Version:              "ta-test",
				DisableTransportAuth: false,
			})
			srv, err := New(Options{Version: "ta-test", Backend: backend})
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			mcpTS := httptest.NewServer(srv.Handler())
			defer mcpTS.Close()

			initBody := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"ta-client","version":"0.1"}}}`
			req, err := http.NewRequest(http.MethodPost, mcpTS.URL+"/mcp", strings.NewReader(initBody))
			if err != nil {
				t.Fatalf("NewRequest: %v", err)
			}
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Accept", "application/json, text/event-stream")
			req.Header.Set("Authorization", "Bearer "+callerTok)
			req.Header.Set("Cookie", "session=drop-me")
			req.Header.Set("X-Goog-Iap-Jwt-Assertion", "eyJ.drop-me.sig")

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
			defer mu.Unlock()
			if len(captured) < 2 {
				t.Fatalf("expected >= 2 Hub requests (experiments, me), got %d", len(captured))
			}
			for i, h := range captured {
				if got := h.Get("Authorization"); got != "Bearer "+callerTok {
					t.Errorf("request[%d]: Authorization = %q, want %q", i, got, "Bearer "+callerTok)
				}
				if got := h.Get(tc.wantHeader); got != "Bearer "+transportTok {
					t.Errorf("request[%d]: %s = %q, want %q", i, tc.wantHeader, got, "Bearer "+transportTok)
				}
				if h.Get("Cookie") != "" {
					t.Errorf("request[%d]: Cookie leaked: %q", i, h.Get("Cookie"))
				}
				if h.Get("X-Goog-Iap-Jwt-Assertion") != "" {
					t.Errorf("request[%d]: X-Goog-Iap-Jwt-Assertion leaked: %q", i, h.Get("X-Goog-Iap-Jwt-Assertion"))
				}
			}
		})
	}
}

// TestStandalone_ExperimentProbeCachingAndAuthErrors verifies §4.5:
//   - experiment check runs after credential extraction so invalid/missing
//     credentials never probe the Hub;
//   - GET /api/v1/experiments?include=server is cached for 30s per Hub;
//   - 401 from the Hub during experiment probe surfaces as 401 (not 503) and is
//     not cached.
func TestStandalone_ExperimentProbeCachingAndAuthErrors(t *testing.T) {
	var expFlag atomic.Bool
	expFlag.Store(true)
	var expCalls atomic.Int32

	var nowMu sync.Mutex
	curNow := time.Date(2026, 11, 28, 12, 0, 0, 0, time.UTC)
	getNow := func() time.Time {
		nowMu.Lock()
		defer nowMu.Unlock()
		return curNow
	}
	advance := func(d time.Duration) {
		nowMu.Lock()
		curNow = curNow.Add(d)
		nowMu.Unlock()
	}

	fakeHub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Header.Get("Authorization") != "Bearer scion_pat_good" {
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"error": map[string]any{"code": "unauthorized", "message": "invalid token"},
			})
			return
		}
		switch r.URL.Path {
		case "/api/v1/experiments":
			if r.URL.Query().Get("include") != "server" {
				t.Errorf("expected include=server query param, got %q", r.URL.RawQuery)
			}
			expCalls.Add(1)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"experiments": map[string]bool{experiments.MCPServer: expFlag.Load()},
			})
		case "/api/v1/auth/me":
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "u1", "email": "u1@example.com"})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer fakeHub.Close()

	backend := NewBackend(BackendOptions{
		HubBaseURL:           fakeHub.URL,
		Transport:            fakeHub.Client().Transport,
		ExperimentCacheTTL:   30 * time.Second,
		Now:                  getNow,
		DisableTransportAuth: true,
	})
	srv, err := New(Options{Backend: backend})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	mcpTS := httptest.NewServer(srv.Handler())
	defer mcpTS.Close()

	initBody := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"cache-test","version":"0.1"}}}`
	post := func(auth string) *http.Response {
		req, _ := http.NewRequest(http.MethodPost, mcpTS.URL+"/mcp", strings.NewReader(initBody))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("Do: %v", err)
		}
		return resp
	}

	// 1. Missing credential -> 401 without calling /api/v1/experiments.
	r := post("")
	_ = r.Body.Close()
	if r.StatusCode != http.StatusUnauthorized {
		t.Fatalf("missing cred status = %d, want 401", r.StatusCode)
	}
	if expCalls.Load() != 0 {
		t.Fatalf("expected 0 experiment calls for missing credential, got %d", expCalls.Load())
	}

	// 2. Bad token while cache is cold -> Hub 401 surfaces as 401 (not 503), not cached.
	r = post("Bearer scion_pat_bad")
	_ = r.Body.Close()
	if r.StatusCode != http.StatusUnauthorized {
		t.Fatalf("bad cred status = %d, want 401", r.StatusCode)
	}
	if r.Header.Get("WWW-Authenticate") != wwwAuthenticateHeader {
		t.Fatalf("WWW-Authenticate = %q, want %q", r.Header.Get("WWW-Authenticate"), wwwAuthenticateHeader)
	}

	// 3. Valid token populates cache (1 call).
	r = post("Bearer scion_pat_good")
	_ = r.Body.Close()
	if r.StatusCode != http.StatusOK {
		t.Fatalf("good cred status = %d, want 200", r.StatusCode)
	}
	if expCalls.Load() != 1 {
		t.Fatalf("expCalls = %d, want 1", expCalls.Load())
	}

	// 4. At +29s, cache is still hot (still 1 call even after flipping flag off).
	expFlag.Store(false)
	advance(29 * time.Second)
	r = post("Bearer scion_pat_good")
	_ = r.Body.Close()
	if r.StatusCode != http.StatusOK {
		t.Fatalf("cached status at +29s = %d, want 200", r.StatusCode)
	}
	if expCalls.Load() != 1 {
		t.Fatalf("expCalls at +29s = %d, want 1 (cached)", expCalls.Load())
	}

	// 5. At +30s, cache expires -> re-probes Hub -> sees flag off -> 404.
	advance(1 * time.Second)
	r = post("Bearer scion_pat_good")
	_ = r.Body.Close()
	if r.StatusCode != http.StatusNotFound {
		t.Fatalf("post-expiry status at +30s = %d, want 404", r.StatusCode)
	}
	if expCalls.Load() != 2 {
		t.Fatalf("expCalls at +30s = %d, want 2", expCalls.Load())
	}
}

// TestStandalone_ReadyzUnhealthyAfter30sAndRecovers verifies §4.5: /readyz
// returns 503 once hub_endpoint's /healthz has been failing for >= 30s, stays
// 200 before 30s, recovers to 200 once /healthz succeeds, and leaks no
// goroutines on shutdown.
func TestStandalone_ReadyzUnhealthyAfter30sAndRecovers(t *testing.T) {
	var hubHealthy atomic.Bool
	hubHealthy.Store(true)
	var probeCount atomic.Int32

	var nowMu sync.Mutex
	curNow := time.Date(2026, 11, 28, 12, 0, 0, 0, time.UTC)
	getNow := func() time.Time {
		nowMu.Lock()
		defer nowMu.Unlock()
		return curNow
	}
	advance := func(d time.Duration) {
		nowMu.Lock()
		curNow = curNow.Add(d)
		nowMu.Unlock()
	}

	backend := NewBackend(BackendOptions{
		HubBaseURL: "http://in-process",
		Transport: &InProcessTransport{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		})},
		ExperimentChecker:    func(context.Context, Credential, string) (bool, error) { return true, nil },
		DisableTransportAuth: true,
	})

	srv, err := New(Options{
		Backend: backend,
		HubHealthProbe: func(ctx context.Context) error {
			probeCount.Add(1)
			if !hubHealthy.Load() {
				return errors.New("hub unreachable")
			}
			return nil
		},
		HubHealthInterval: 10 * time.Millisecond,
		HubUnhealthyAfter: 30 * time.Second,
		Now:               getNow,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	runtime.GC()
	time.Sleep(20 * time.Millisecond)
	beforeG := runtime.NumGoroutine()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.Serve(ctx, ln) }()

	baseURL := "http://" + ln.Addr().String()
	waitProbe := func(minCount int32) {
		t.Helper()
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			if probeCount.Load() >= minCount {
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
		t.Fatalf("timed out waiting for probe count >= %d (got %d)", minCount, probeCount.Load())
	}

	getStatus := func(path string) int {
		t.Helper()
		resp, err := http.Get(baseURL + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		_ = resp.Body.Close()
		return resp.StatusCode
	}

	waitProbe(1)
	if code := getStatus("/readyz"); code != http.StatusOK {
		t.Fatalf("healthy /readyz = %d, want 200", code)
	}

	// Hub starts failing; wait for at least one failed probe to record failingSince.
	hubHealthy.Store(false)
	waitProbe(probeCount.Load() + 2)

	// At +29s: still ready (200).
	advance(29 * time.Second)
	if code := getStatus("/readyz"); code != http.StatusOK {
		t.Fatalf("failing 29s /readyz = %d, want 200", code)
	}

	// At +30s: unhealthy (503), while /healthz stays 200.
	advance(1 * time.Second)
	if code := getStatus("/readyz"); code != http.StatusServiceUnavailable {
		t.Fatalf("failing 30s /readyz = %d, want 503", code)
	}
	if code := getStatus("/healthz"); code != http.StatusOK {
		t.Fatalf("failing 30s /healthz = %d, want 200", code)
	}

	// Hub recovers -> /readyz returns 200 again.
	hubHealthy.Store(true)
	waitProbe(probeCount.Load() + 2)
	if code := getStatus("/readyz"); code != http.StatusOK {
		t.Fatalf("recovered /readyz = %d, want 200", code)
	}

	// Shutdown cleanly with no goroutine leak.
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Serve error on shutdown: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Serve did not exit within 5s")
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		runtime.GC()
		if runtime.NumGoroutine() <= beforeG+2 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	if afterG := runtime.NumGoroutine(); afterG > beforeG+2 {
		t.Fatalf("goroutine leak after standalone shutdown: before=%d after=%d", beforeG, afterG)
	}
}

// TestReadMessages_OldHubRejectsStartTail verifies the P1 deferred item: when
// standalone mode talks to an older Hub that returns 400 on start=tail,
// read_messages returns a clear, actionable tool error guiding the caller to
// pass from_start=true or a cursor, and from_start=true still succeeds.
func TestReadMessages_OldHubRejectsStartTail(t *testing.T) {
	fakeHub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/api/v1/experiments":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"experiments": map[string]bool{experiments.MCPServer: true},
			})
		case r.URL.Path == "/api/v1/auth/me":
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "u1", "email": "u1@example.com"})
		case strings.HasSuffix(r.URL.Path, "/watch"):
			if r.URL.Query().Get("start") == "tail" {
				w.WriteHeader(http.StatusBadRequest)
				_ = json.NewEncoder(w).Encode(map[string]any{
					"error": map[string]any{
						"code":    "validation_error",
						"message": `invalid 'start' parameter "tail": must be head or beginning`,
					},
				})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"events": []any{
					map[string]any{
						"eventId":    "msg:m1",
						"type":       "conversation.message.created",
						"occurredAt": time.Now().UTC().Format(time.RFC3339),
						"cursor":     "cur-1",
						"data": map[string]any{
							"id":              "m1",
							"conversation_id": "conv-old",
							"sender":          "user:alice",
							"sender_id":       "u1",
							"msg":             "hello from old hub",
						},
					},
				},
				"nextCursor": "cur-1",
				"hasMore":    false,
				"truncated":  false,
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer fakeHub.Close()

	backend := NewBackend(BackendOptions{
		HubBaseURL:           fakeHub.URL,
		Transport:            fakeHub.Client().Transport,
		DisableTransportAuth: true,
	})
	srv, err := New(Options{Backend: backend})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	mcpTS := httptest.NewServer(srv.Handler())
	defer mcpTS.Close()

	ctx := context.Background()
	cl := mcp.NewClient(&mcp.Implementation{Name: "old-hub-client", Version: "0.1"}, nil)
	hc := &http.Client{
		Transport: &staticHeaderTransport{
			base:    &http.Transport{DisableKeepAlives: true},
			headers: map[string]string{"Authorization": "Bearer scion_pat_test"},
		},
	}
	sess, err := cl.Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint:   mcpTS.URL + "/mcp",
		HTTPClient: hc,
	}, nil)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer func() { _ = sess.Close() }()

	// 1. Default read_messages (start=tail) -> clear actionable tool error.
	res, err := sess.CallTool(ctx, &mcp.CallToolParams{
		Name:      "read_messages",
		Arguments: map[string]any{"conversation_id": "conv-old"},
	})
	if err != nil {
		t.Fatalf("CallTool protocol error: %v", err)
	}
	if !res.IsError {
		t.Fatalf("expected tool error on old Hub start=tail 400, got success")
	}
	errText := res.Content[0].(*mcp.TextContent).Text
	if !strings.Contains(errText, "from_start=true") || !strings.Contains(errText, "cursor") {
		t.Fatalf("expected clear guidance naming from_start=true and cursor, got: %q", errText)
	}

	// 2. read_messages with from_start=true -> succeeds.
	okRes, err := sess.CallTool(ctx, &mcp.CallToolParams{
		Name: "read_messages",
		Arguments: map[string]any{
			"conversation_id": "conv-old",
			"from_start":      true,
		},
	})
	if err != nil || okRes.IsError {
		t.Fatalf("read_messages(from_start=true) failed: err=%v res=%+v", err, okRes)
	}
	var out ReadMessagesOutput
	if err := json.Unmarshal([]byte(okRes.Content[0].(*mcp.TextContent).Text), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(out.Messages) != 1 || out.Messages[0].Text != "hello from old hub" {
		t.Fatalf("unexpected messages: %+v", out.Messages)
	}
}

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
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/GoogleCloudPlatform/scion/pkg/experiments"
	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type sseRPCMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *struct {
		Code    int64           `json:"code"`
		Message string          `json:"message"`
		Data    json.RawMessage `json:"data,omitempty"`
	} `json:"error,omitempty"`
}

func readSSEJSONRPCFrames(body io.Reader, out chan<- sseRPCMessage) {
	defer close(out)
	sc := bufio.NewScanner(body)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	var dataLines []string
	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			if len(dataLines) > 0 {
				var msg sseRPCMessage
				if err := json.Unmarshal([]byte(strings.Join(dataLines, "\n")), &msg); err == nil {
					out <- msg
				}
				dataLines = nil
			}
			continue
		}
		if strings.HasPrefix(line, "data:") {
			dataLines = append(dataLines, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
	}
	if len(dataLines) > 0 {
		var msg sseRPCMessage
		if err := json.Unmarshal([]byte(strings.Join(dataLines, "\n")), &msg); err == nil {
			out <- msg
		}
	}
}

type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func TestEvents_CapabilitiesListPollAndStreamUnit(t *testing.T) {
	var failWatchCount atomic.Int32
	var forceLongPoll atomic.Bool
	var logBuf syncBuffer
	logger := slog.New(slog.NewJSONHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelInfo}))

	fakeHub := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v1/auth/me":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "user-ev-1", "email": "ev@example.com"})
		case strings.HasSuffix(r.URL.Path, "/watch"):
			if failWatchCount.Add(-1) >= 0 {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusServiceUnavailable)
				_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"code": "unavailable", "message": "temporary 503"}})
				return
			}
			if r.URL.Query().Get("cursor") == "bad-cursor" {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusBadRequest)
				_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"code": "invalid_cursor", "message": "malformed cursor"}})
				return
			}
			if r.URL.Query().Get("cursor") == "expired-cursor" {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusGone)
				_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"code": "cursor_expired", "message": "cursor ahead of head"}})
				return
			}

			if strings.Contains(r.Header.Get("Accept"), "text/event-stream") && !forceLongPoll.Load() {
				fl, ok := w.(http.Flusher)
				if !ok {
					w.WriteHeader(http.StatusInternalServerError)
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				w.WriteHeader(http.StatusOK)
				fl.Flush()
				_, _ = fmt.Fprintf(w, "id: cur-head\nevent: active\ndata: {\"cursor\":\"cur-head\",\"truncated\":false}\n\n")
				fl.Flush()
				select {
				case <-r.Context().Done():
				case <-time.After(150 * time.Millisecond):
					_, _ = fmt.Fprintf(w, "id: cur-head\nevent: heartbeat\ndata: {\"cursor\":\"cur-head\"}\n\n")
					fl.Flush()
					<-r.Context().Done()
				}
				return
			}

			// JSON long-poll response.
			w.Header().Set("Content-Type", "application/json")
			timedOut := r.URL.Query().Get("wait") != "0"
			if timedOut {
				select {
				case <-r.Context().Done():
					return
				case <-time.After(100 * time.Millisecond):
				}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"events":     []any{},
				"nextCursor": "cur-head",
				"hasMore":    false,
				"truncated":  false,
				"timedOut":   timedOut,
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})

	backend := NewBackend(BackendOptions{
		HubBaseURL: "http://in-process",
		Transport:  &InProcessTransport{Handler: fakeHub},
		Version:    "test-p3",
		ExperimentChecker: func(_ context.Context, _ Credential, name string) (bool, error) {
			return name == experiments.MCPServer, nil
		},
		DisableTransportAuth: true,
	})

	srv, err := New(Options{
		Version:                "test-p3",
		Backend:                backend,
		Logger:                 logger,
		MaxStreamsPerPrincipal: 2,
		StreamWait:             200 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	// 1. Capabilities advertise io.modelcontextprotocol/events via go-sdk client.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cl := mcp.NewClient(&mcp.Implementation{Name: "ev-client", Version: "1.0"}, nil)
	if err := mcp.AddSendingCustomMethod[*EventsListParams, *EventsListResult](cl, "events/list"); err != nil {
		t.Fatalf("AddSendingCustomMethod(events/list): %v", err)
	}
	if err := mcp.AddSendingCustomMethod[*EventsPollParams, *EventsPollResult](cl, "events/poll"); err != nil {
		t.Fatalf("AddSendingCustomMethod(events/poll): %v", err)
	}
	hc := &http.Client{
		Transport: &staticHeaderTransport{
			base:    &http.Transport{DisableKeepAlives: true},
			headers: map[string]string{"Authorization": "Bearer scion_pat_ev"},
		},
	}
	sess, err := cl.Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint:   ts.URL + "/mcp",
		HTTPClient: hc,
	}, nil)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer func() { _ = sess.Close() }()

	initRes := sess.InitializeResult()
	if initRes == nil || initRes.Capabilities == nil || initRes.Capabilities.Extensions == nil {
		t.Fatalf("expected Capabilities.Extensions in InitializeResult, got %+v", initRes)
	}
	if _, ok := initRes.Capabilities.Extensions[EventsExtensionID]; !ok {
		t.Fatalf("expected extension %q in capabilities, got %+v", EventsExtensionID, initRes.Capabilities.Extensions)
	}

	// 2. events/list returns conversation.message.created with poll and push delivery.
	listRes, err := mcp.CallCustomMethod[*EventsListParams, *EventsListResult](ctx, sess, "events/list", &EventsListParams{})
	if err != nil {
		t.Fatalf("events/list: %v", err)
	}
	if listRes.ResultType != "complete" || len(listRes.Events) != 1 {
		t.Fatalf("unexpected events/list result: %+v", listRes)
	}
	if listRes.Events[0].Name != EventNameConversationMessageCreated {
		t.Fatalf("event name = %q, want %q", listRes.Events[0].Name, EventNameConversationMessageCreated)
	}

	// 3. events/poll: 410 resets to head with truncated:true; 400 -> -32602.
	expCur := "expired-cursor"
	pollTrunc, err := mcp.CallCustomMethod[*EventsPollParams, *EventsPollResult](ctx, sess, "events/poll", &EventsPollParams{
		Name:      EventNameConversationMessageCreated,
		Arguments: map[string]any{"conversation_id": "conv-1"},
		Cursor:    &expCur,
	})
	if err != nil {
		t.Fatalf("events/poll (expired cursor): %v", err)
	}
	if !pollTrunc.Truncated || pollTrunc.Cursor != "cur-head" {
		t.Fatalf("expected truncated=true and cursor=cur-head, got %+v", pollTrunc)
	}

	badCur := "bad-cursor"
	_, badErr := mcp.CallCustomMethod[*EventsPollParams, *EventsPollResult](ctx, sess, "events/poll", &EventsPollParams{
		Name:      EventNameConversationMessageCreated,
		Arguments: map[string]any{"conversation_id": "conv-1"},
		Cursor:    &badCur,
	})
	var rpcErr *jsonrpc.Error
	if badErr == nil || !errors.As(badErr, &rpcErr) || rpcErr.Code != jsonrpc.CodeInvalidParams {
		t.Fatalf("expected JSON-RPC -32602 for bad cursor, got: %v (rpcErr=%+v)", badErr, rpcErr)
	}

	// 4. events/stream: SEP-2243 Mcp-Name mismatch -> HTTP 400 + -32020.
	mismatchReq, _ := http.NewRequestWithContext(ctx, http.MethodPost, ts.URL+"/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":10,"method":"events/stream","params":{"name":"conversation.message.created","arguments":{"conversation_id":"conv-1"}}}`))
	mismatchReq.Header.Set("Authorization", "Bearer scion_pat_ev")
	mismatchReq.Header.Set("Content-Type", "application/json")
	mismatchReq.Header.Set("Mcp-Name", "wrong.event")
	mismatchResp, err := http.DefaultClient.Do(mismatchReq)
	if err != nil {
		t.Fatalf("mismatch request: %v", err)
	}
	mismatchBody, _ := io.ReadAll(mismatchResp.Body)
	_ = mismatchResp.Body.Close()
	if mismatchResp.StatusCode != http.StatusBadRequest || !strings.Contains(string(mismatchBody), "-32020") {
		t.Fatalf("expected 400/-32020 on Mcp-Name mismatch, got %d: %s", mismatchResp.StatusCode, string(mismatchBody))
	}

	// 5. events/stream long-poll fallback when Hub answers JSON instead of SSE.
	forceLongPoll.Store(true)
	lpCtx, lpCancel := context.WithTimeout(ctx, 5*time.Second)
	lpReq, _ := http.NewRequestWithContext(lpCtx, http.MethodPost, ts.URL+"/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":11,"method":"events/stream","params":{"name":"conversation.message.created","arguments":{"conversation_id":"conv-1"}}}`))
	lpReq.Header.Set("Authorization", "Bearer scion_pat_ev")
	lpReq.Header.Set("Content-Type", "application/json")
	lpResp, err := http.DefaultClient.Do(lpReq)
	if err != nil {
		t.Fatalf("long-poll fallback stream: %v", err)
	}
	lpFrames := make(chan sseRPCMessage, 8)
	go readSSEJSONRPCFrames(lpResp.Body, lpFrames)

	gotActive := <-lpFrames
	if gotActive.Method != "notifications/events/active" {
		t.Fatalf("long-poll fallback first frame = %q, want active", gotActive.Method)
	}
	gotHB := <-lpFrames
	if gotHB.Method != "notifications/events/heartbeat" {
		t.Fatalf("long-poll fallback second frame = %q, want heartbeat", gotHB.Method)
	}
	lpCancel()
	_ = lpResp.Body.Close()
	forceLongPoll.Store(false)

	// 6. Transient Hub 503 emits notifications/events/error and recovers without closing the stream.
	failWatchCount.Store(1)
	trCtx, trCancel := context.WithTimeout(ctx, 5*time.Second)
	trReq, _ := http.NewRequestWithContext(trCtx, http.MethodPost, ts.URL+"/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":12,"method":"events/stream","params":{"name":"conversation.message.created","arguments":{"conversation_id":"conv-1"}}}`))
	trReq.Header.Set("Authorization", "Bearer scion_pat_ev")
	trReq.Header.Set("Content-Type", "application/json")
	trResp, err := http.DefaultClient.Do(trReq)
	if err != nil {
		t.Fatalf("transient error stream: %v", err)
	}
	trFrames := make(chan sseRPCMessage, 8)
	go readSSEJSONRPCFrames(trResp.Body, trFrames)

	trErrFrame := <-trFrames
	if trErrFrame.Method != "notifications/events/error" {
		t.Fatalf("transient first frame = %q, want notifications/events/error", trErrFrame.Method)
	}
	trActiveFrame := <-trFrames
	if trActiveFrame.Method != "notifications/events/active" {
		t.Fatalf("transient recovery frame = %q, want notifications/events/active", trActiveFrame.Method)
	}
	trCancel()
	_ = trResp.Body.Close()

	// 7. Stream cap (MaxStreamsPerPrincipal = 2): 3rd concurrent stream returns -32025.
	capCtx, capCancel := context.WithTimeout(ctx, 5*time.Second)
	defer capCancel()
	var openResps []*http.Response
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		req, _ := http.NewRequestWithContext(capCtx, http.MethodPost, ts.URL+"/mcp", strings.NewReader(fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":"events/stream","params":{"name":"conversation.message.created","arguments":{"conversation_id":"conv-1"}}}`, 20+i)))
		req.Header.Set("Authorization", "Bearer scion_pat_ev")
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("open stream %d: %v", i, err)
		}
		openResps = append(openResps, resp)
		ch := make(chan sseRPCMessage, 4)
		wg.Add(1)
		go func(r io.Reader) {
			defer wg.Done()
			readSSEJSONRPCFrames(r, ch)
		}(resp.Body)
		<-ch // wait for active so stream is held
	}

	thirdReq, _ := http.NewRequestWithContext(capCtx, http.MethodPost, ts.URL+"/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":22,"method":"events/stream","params":{"name":"conversation.message.created","arguments":{"conversation_id":"conv-1"}}}`))
	thirdReq.Header.Set("Authorization", "Bearer scion_pat_ev")
	thirdReq.Header.Set("Content-Type", "application/json")
	thirdResp, err := http.DefaultClient.Do(thirdReq)
	if err != nil {
		t.Fatalf("third stream: %v", err)
	}
	thirdBody, _ := io.ReadAll(thirdResp.Body)
	_ = thirdResp.Body.Close()
	if !strings.Contains(string(thirdBody), "-32025") {
		t.Fatalf("expected -32025 ResourceExhausted on 3rd stream, got: %s", string(thirdBody))
	}

	capCancel()
	for _, r := range openResps {
		_ = r.Body.Close()
	}
	wg.Wait()

	// Verify audit logs were written for events/list, events/poll, and events/stream.
	logs := logBuf.String()
	for _, m := range []string{`"method":"events/list"`, `"method":"events/poll"`, `"method":"events/stream"`} {
		if !strings.Contains(logs, m) {
			t.Errorf("audit log missing %s", m)
		}
	}
}

func TestEventsStream_MaxAgeMsTruncatedActiveOnSSE(t *testing.T) {
	now := time.Now().UTC()
	oldTime := now.Add(-10 * time.Minute)
	freshTime := now.Add(-5 * time.Second)

	fakeHub := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v1/auth/me":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "user-age", "email": "age@example.com"})
		case strings.HasSuffix(r.URL.Path, "/watch"):
			fl := w.(http.Flusher)
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			fl.Flush()
			_, _ = fmt.Fprintf(w, "id: cur-0\nevent: active\ndata: {\"cursor\":\"cur-0\",\"truncated\":false}\n\n")
			fl.Flush()
			for i, ts := range []time.Time{oldTime, oldTime.Add(time.Second)} {
				evJSON, _ := json.Marshal(map[string]any{
					"eventId":    fmt.Sprintf("msg:old-%d", i),
					"type":       EventNameConversationMessageCreated,
					"occurredAt": ts,
					"cursor":     fmt.Sprintf("cur-old-%d", i),
					"data":       map[string]any{"id": fmt.Sprintf("old-%d", i), "conversationId": "conv-age", "sender": "agent:a", "msg": "old", "createdAt": ts},
				})
				_, _ = fmt.Fprintf(w, "id: cur-old-%d\nevent: message\ndata: %s\n\n", i, evJSON)
				fl.Flush()
			}
			freshJSON, _ := json.Marshal(map[string]any{
				"eventId":    "msg:fresh-1",
				"type":       EventNameConversationMessageCreated,
				"occurredAt": freshTime,
				"cursor":     "cur-fresh-1",
				"data":       map[string]any{"id": "fresh-1", "conversationId": "conv-age", "sender": "agent:a", "msg": "fresh", "createdAt": freshTime},
			})
			_, _ = fmt.Fprintf(w, "id: cur-fresh-1\nevent: message\ndata: %s\n\n", freshJSON)
			fl.Flush()
			<-r.Context().Done()
		}
	})

	backend := NewBackend(BackendOptions{
		HubBaseURL:           "http://in-process",
		Transport:            &InProcessTransport{Handler: fakeHub},
		ExperimentChecker:    func(context.Context, Credential, string) (bool, error) { return true, nil },
		DisableTransportAuth: true,
	})
	srv, err := New(Options{Backend: backend, StreamWait: time.Second})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, ts.URL+"/mcp", strings.NewReader(
		`{"jsonrpc":"2.0","id":1,"method":"events/stream","params":{"name":"conversation.message.created","arguments":{"conversation_id":"conv-age"},"cursor":"cur-0","maxAgeMs":60000}}`,
	))
	req.Header.Set("Authorization", "Bearer scion_pat_age")
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	frames := make(chan sseRPCMessage, 16)
	go readSSEJSONRPCFrames(resp.Body, frames)

	var sawTruncatedActiveBeforeEvent bool
	for {
		select {
		case fr := <-frames:
			switch fr.Method {
			case "notifications/events/active":
				var p struct {
					Truncated bool `json:"truncated"`
				}
				_ = json.Unmarshal(fr.Params, &p)
				if p.Truncated {
					sawTruncatedActiveBeforeEvent = true
				}
			case "notifications/events/event":
				var occ EventOccurrence
				_ = json.Unmarshal(fr.Params, &occ)
				if occ.Data.Text != "fresh" {
					t.Fatalf("expected only fresh message, got %q", occ.Data.Text)
				}
				if !sawTruncatedActiveBeforeEvent {
					t.Fatal("expected notifications/events/active with truncated:true before fresh event")
				}
				cancel()
				return
			}
		case <-time.After(3 * time.Second):
			t.Fatal("timed out waiting for fresh event")
		}
	}
}

func TestEventsStream_MidStreamGapAndErrorCodes(t *testing.T) {
	t.Run("mid-stream 410 sends active truncated without error", func(t *testing.T) {
		var calls atomic.Int32
		fakeHub := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/api/v1/auth/me" {
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]any{"id": "u-gap", "email": "gap@example.com"})
				return
			}
			n := calls.Add(1)
			switch n {
			case 1:
				fl := w.(http.Flusher)
				w.Header().Set("Content-Type", "text/event-stream")
				w.WriteHeader(http.StatusOK)
				fl.Flush()
				_, _ = fmt.Fprintf(w, "id: cur-1\nevent: active\ndata: {\"cursor\":\"cur-1\",\"truncated\":false}\n\n")
				fl.Flush()
				// Close stream cleanly to trigger reconnect.
			case 2:
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusGone)
				_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"code": "cursor_expired", "message": "ahead"}})
			default:
				fl := w.(http.Flusher)
				w.Header().Set("Content-Type", "text/event-stream")
				w.WriteHeader(http.StatusOK)
				fl.Flush()
				_, _ = fmt.Fprintf(w, "id: cur-head\nevent: active\ndata: {\"cursor\":\"cur-head\",\"truncated\":false}\n\n")
				fl.Flush()
				<-r.Context().Done()
			}
		})

		backend := NewBackend(BackendOptions{
			HubBaseURL:           "http://in-process",
			Transport:            &InProcessTransport{Handler: fakeHub},
			ExperimentChecker:    func(context.Context, Credential, string) (bool, error) { return true, nil },
			DisableTransportAuth: true,
		})
		srv, err := New(Options{Backend: backend, StreamWait: time.Second})
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		ts := httptest.NewServer(srv.Handler())
		defer ts.Close()

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		req, _ := http.NewRequestWithContext(ctx, http.MethodPost, ts.URL+"/mcp", strings.NewReader(
			`{"jsonrpc":"2.0","id":1,"method":"events/stream","params":{"name":"conversation.message.created","arguments":{"conversation_id":"conv-gap"}}}`,
		))
		req.Header.Set("Authorization", "Bearer scion_pat_gap")
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("Do: %v", err)
		}
		defer func() { _ = resp.Body.Close() }()

		frames := make(chan sseRPCMessage, 16)
		go readSSEJSONRPCFrames(resp.Body, frames)

		// First active (initial connect).
		f1 := <-frames
		if f1.Method != "notifications/events/active" {
			t.Fatalf("first frame = %q, want active", f1.Method)
		}
		// Second frame after mid-stream 410 must be active{truncated:true}, never error.
		f2 := <-frames
		if f2.Method != "notifications/events/active" {
			t.Fatalf("mid-stream 410 frame = %q, want notifications/events/active (no error frame)", f2.Method)
		}
		var p struct {
			Cursor    string `json:"cursor"`
			Truncated bool   `json:"truncated"`
		}
		_ = json.Unmarshal(f2.Params, &p)
		if !p.Truncated || p.Cursor != "cur-head" {
			t.Fatalf("mid-stream 410 active = %+v, want truncated=true cursor=cur-head", p)
		}
		cancel()
	})

	for _, tc := range []struct {
		name     string
		status   int
		hubCode  string
		wantCode int64
		wantKind string
	}{
		{"mid-stream 400 -> -32602", http.StatusBadRequest, "invalid_cursor", jsonrpc.CodeInvalidParams, ""},
		{"mid-stream 404 -> -32023 kind=conversation", http.StatusNotFound, "not_found", codeNotFound, "conversation"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			fakeHub := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/v1/auth/me" {
					w.Header().Set("Content-Type", "application/json")
					_ = json.NewEncoder(w).Encode(map[string]any{"id": "u-err", "email": "err@example.com"})
					return
				}
				if calls.Add(1) == 1 {
					fl := w.(http.Flusher)
					w.Header().Set("Content-Type", "text/event-stream")
					w.WriteHeader(http.StatusOK)
					fl.Flush()
					_, _ = fmt.Fprintf(w, "id: cur-1\nevent: active\ndata: {\"cursor\":\"cur-1\",\"truncated\":false}\n\n")
					fl.Flush()
					return
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.status)
				_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"code": tc.hubCode, "message": "hub err"}})
			})

			backend := NewBackend(BackendOptions{
				HubBaseURL:           "http://in-process",
				Transport:            &InProcessTransport{Handler: fakeHub},
				ExperimentChecker:    func(context.Context, Credential, string) (bool, error) { return true, nil },
				DisableTransportAuth: true,
			})
			srv, err := New(Options{Backend: backend, StreamWait: time.Second})
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			ts := httptest.NewServer(srv.Handler())
			defer ts.Close()

			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			req, _ := http.NewRequestWithContext(ctx, http.MethodPost, ts.URL+"/mcp", strings.NewReader(
				`{"jsonrpc":"2.0","id":1,"method":"events/stream","params":{"name":"conversation.message.created","arguments":{"conversation_id":"conv-err"}}}`,
			))
			req.Header.Set("Authorization", "Bearer scion_pat_err")
			req.Header.Set("Content-Type", "application/json")
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("Do: %v", err)
			}
			defer func() { _ = resp.Body.Close() }()

			frames := make(chan sseRPCMessage, 8)
			go readSSEJSONRPCFrames(resp.Body, frames)
			<-frames // active
			term := <-frames
			if term.Method != "notifications/events/terminated" {
				t.Fatalf("expected terminated, got %q", term.Method)
			}
			var payload struct {
				Error struct {
					Code int64          `json:"code"`
					Data map[string]any `json:"data"`
				} `json:"error"`
			}
			_ = json.Unmarshal(term.Params, &payload)
			if payload.Error.Code != tc.wantCode {
				t.Fatalf("terminated error.code = %d, want %d (params=%s)", payload.Error.Code, tc.wantCode, string(term.Params))
			}
			if tc.wantKind != "" && payload.Error.Data["kind"] != tc.wantKind {
				t.Fatalf("terminated error.data.kind = %v, want %q", payload.Error.Data["kind"], tc.wantKind)
			}
		})
	}
}

func TestEventsStream_FrontDoorRefusals(t *testing.T) {
	var expOn atomic.Bool
	expOn.Store(true)

	fakeHub := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "u-ref", "email": "ref@example.com"})
	})
	backend := NewBackend(BackendOptions{
		HubBaseURL:           "http://in-process",
		Transport:            &InProcessTransport{Handler: fakeHub},
		ExperimentChecker:    func(context.Context, Credential, string) (bool, error) { return expOn.Load(), nil },
		DisableTransportAuth: true,
	})
	srv, err := New(Options{
		Backend:        backend,
		AllowedOrigins: []string{"https://allowed.example.com"},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	streamBody := `{"jsonrpc":"2.0","id":1,"method":"events/stream","params":{"name":"conversation.message.created","arguments":{"conversation_id":"conv-1"}}}`
	agentJWT := makeFakeJWT(t, map[string]any{
		"iss":        "scion-hub",
		"aud":        []string{"scion-hub-api"},
		"sub":        "agent-1",
		"project_id": "proj-1",
		"scopes":     []string{"agent:status:update"},
	})

	cases := []struct {
		name       string
		setup      func()
		headers    map[string]string
		wantStatus int
	}{
		{
			name:       "disallowed origin -> 403",
			setup:      func() { expOn.Store(true) },
			headers:    map[string]string{"Authorization": "Bearer scion_pat_ok", "Origin": "https://evil.example.com"},
			wantStatus: http.StatusForbidden,
		},
		{
			name:       "experiment off -> 404",
			setup:      func() { expOn.Store(false) },
			headers:    map[string]string{"Authorization": "Bearer scion_pat_ok"},
			wantStatus: http.StatusNotFound,
		},
		{
			name:       "agent JWT -> 401",
			setup:      func() { expOn.Store(true) },
			headers:    map[string]string{"Authorization": "Bearer " + agentJWT},
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:       "shutdown -> 503",
			setup:      func() { expOn.Store(true); srv.BeginShutdown() },
			headers:    map[string]string{"Authorization": "Bearer scion_pat_ok"},
			wantStatus: http.StatusServiceUnavailable,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.setup()
			req, _ := http.NewRequest(http.MethodPost, ts.URL+"/mcp", strings.NewReader(streamBody))
			req.Header.Set("Content-Type", "application/json")
			for k, v := range tc.headers {
				req.Header.Set(k, v)
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("Do: %v", err)
			}
			_ = resp.Body.Close()
			if resp.StatusCode != tc.wantStatus {
				t.Fatalf("got status %d, want %d", resp.StatusCode, tc.wantStatus)
			}
		})
	}
}

func TestEventsStream_InvalidJSONRPCRequest(t *testing.T) {
	fakeHub := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "u-inv", "email": "inv@example.com"})
	})
	backend := NewBackend(BackendOptions{
		HubBaseURL:           "http://in-process",
		Transport:            &InProcessTransport{Handler: fakeHub},
		ExperimentChecker:    func(context.Context, Credential, string) (bool, error) { return true, nil },
		DisableTransportAuth: true,
	})
	srv, err := New(Options{Backend: backend})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	for _, tc := range []struct {
		name string
		body string
	}{
		{"missing id", `{"jsonrpc":"2.0","method":"events/stream","params":{"name":"conversation.message.created","arguments":{"conversation_id":"conv-1"}}}`},
		{"null id", `{"jsonrpc":"2.0","id":null,"method":"events/stream","params":{"name":"conversation.message.created","arguments":{"conversation_id":"conv-1"}}}`},
		{"wrong jsonrpc version", `{"jsonrpc":"1.0","id":1,"method":"events/stream","params":{"name":"conversation.message.created","arguments":{"conversation_id":"conv-1"}}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req, _ := http.NewRequest(http.MethodPost, ts.URL+"/mcp", strings.NewReader(tc.body))
			req.Header.Set("Authorization", "Bearer scion_pat_inv")
			req.Header.Set("Content-Type", "application/json")
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("Do: %v", err)
			}
			body, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if !strings.Contains(string(body), "-32600") {
				t.Fatalf("expected -32600 InvalidRequest, got: %s", string(body))
			}
		})
	}
}

func TestEventsStream_StreamWaitClampedTo55s(t *testing.T) {
	fakeHub := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/auth/me" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "u-clamp", "email": "clamp@example.com"})
			return
		}
		fl := w.(http.Flusher)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fl.Flush()
		_, _ = fmt.Fprintf(w, "id: cur-1\nevent: active\ndata: {\"cursor\":\"cur-1\",\"truncated\":false}\n\n")
		fl.Flush()
		<-r.Context().Done()
	})
	backend := NewBackend(BackendOptions{
		HubBaseURL:           "http://in-process",
		Transport:            &InProcessTransport{Handler: fakeHub},
		ExperimentChecker:    func(context.Context, Credential, string) (bool, error) { return true, nil },
		DisableTransportAuth: true,
	})
	srv, err := New(Options{Backend: backend, StreamWait: 90 * time.Second})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, ts.URL+"/mcp", strings.NewReader(
		`{"jsonrpc":"2.0","id":1,"method":"events/stream","params":{"name":"conversation.message.created","arguments":{"conversation_id":"conv-1"}}}`,
	))
	req.Header.Set("Authorization", "Bearer scion_pat_clamp")
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	frames := make(chan sseRPCMessage, 4)
	go readSSEJSONRPCFrames(resp.Body, frames)
	act := <-frames
	var p struct {
		HeartbeatMs int64 `json:"heartbeatMs"`
	}
	_ = json.Unmarshal(act.Params, &p)
	if p.HeartbeatMs != MaxStreamWait.Milliseconds() {
		t.Fatalf("active.heartbeatMs = %d, want clamped %d", p.HeartbeatMs, MaxStreamWait.Milliseconds())
	}
	cancel()
}

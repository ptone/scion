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

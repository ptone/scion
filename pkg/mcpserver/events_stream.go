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
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/GoogleCloudPlatform/scion/pkg/apiclient"
	"github.com/GoogleCloudPlatform/scion/pkg/hubclient"
	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
)

const (
	maxStreamRequestBodyBytes = 1 << 20 // 1 MiB
	streamBackoffInitial      = 250 * time.Millisecond
	streamBackoffMax          = 10 * time.Second
)

// streamLimiter enforces max_streams_per_principal across concurrent
// events/stream connections on this replica.
type streamLimiter struct {
	mu     sync.Mutex
	max    int
	counts map[string]int
}

func newStreamLimiter(max int) *streamLimiter {
	if max <= 0 {
		max = DefaultMaxStreamsPerPrincipal
	}
	return &streamLimiter{
		max:    max,
		counts: make(map[string]int),
	}
}

func (l *streamLimiter) acquire(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.counts[key] >= l.max {
		return false
	}
	l.counts[key]++
	return true
}

func (l *streamLimiter) release(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if n := l.counts[key]; n <= 1 {
		delete(l.counts, key)
	} else {
		l.counts[key] = n - 1
	}
}

type rawJSONRPCRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

type eventsStreamParams struct {
	Name        string         `json:"name"`
	Arguments   map[string]any `json:"arguments,omitempty"`
	Cursor      *string        `json:"cursor,omitempty"`
	MaxAgeMs    *int64         `json:"maxAgeMs,omitempty"`
	HeartbeatMs int64          `json:"heartbeatMs,omitempty"`
}

// dispatchMCP sits behind frontDoor and routes POST requests with
// method == "events/stream" to the HTTP/SSE events stream handler while
// passing all other MCP traffic to go-sdk's StreamableHTTPHandler.
//
// Why HTTP/SSE-level inside pkg/mcpserver: go-sdk v1.8.0's ServerSession
// cannot send custom notification method names (notifications/events/*).
// Routing inside frontDoor preserves the exact same shutdown refusal,
// experiment check, Origin allowlist, credential validation, principal
// resolution, and audit logging.
func (s *Server) dispatchMCP(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.Body == nil {
			next.ServeHTTP(w, r)
			return
		}

		body, err := io.ReadAll(io.LimitReader(r.Body, maxStreamRequestBodyBytes+1))
		if err != nil {
			next.ServeHTTP(w, r)
			return
		}
		if len(body) > maxStreamRequestBodyBytes {
			// Restore the stream and let go-sdk handle oversized bodies.
			r.Body = io.NopCloser(io.MultiReader(bytes.NewReader(body), r.Body))
			next.ServeHTTP(w, r)
			return
		}

		var rpcReq rawJSONRPCRequest
		if json.Unmarshal(body, &rpcReq) == nil && rpcReq.Method == "events/stream" {
			s.handleEventsStream(w, r, rpcReq)
			return
		}

		r.Body = io.NopCloser(bytes.NewReader(body))
		next.ServeHTTP(w, r)
	})
}

func (s *Server) handleEventsStream(w http.ResponseWriter, r *http.Request, rpcReq rawJSONRPCRequest) {
	start := time.Now()
	auth, ok := requestAuthFromContext(r.Context())
	if !ok {
		writeJSONRPCErrorResponse(w, http.StatusInternalServerError, rpcReq.ID, newJSONRPCError(jsonrpc.CodeInternalError, "missing request authentication context", nil))
		return
	}

	// Validate SEP-2243 Mcp-Method header when present.
	if hdrMethod := strings.TrimSpace(r.Header.Get("Mcp-Method")); hdrMethod != "" && hdrMethod != "events/stream" {
		s.auditEventCall(nil, auth, "events/stream", "", "header_mismatch", http.StatusBadRequest, time.Since(start))
		writeJSONRPCErrorResponse(w, http.StatusBadRequest, rpcReq.ID, newJSONRPCError(codeHeaderMismatch, "Mcp-Method header does not match request method", nil))
		return
	}

	var params eventsStreamParams
	if len(rpcReq.Params) > 0 {
		if err := json.Unmarshal(rpcReq.Params, &params); err != nil {
			s.auditEventCall(nil, auth, "events/stream", "", "invalid_input", http.StatusBadRequest, time.Since(start))
			writeJSONRPCErrorResponse(w, http.StatusOK, rpcReq.ID, newJSONRPCError(jsonrpc.CodeInvalidParams, "invalid params JSON", nil))
			return
		}
	}

	// Validate SEP-2243 Mcp-Name header when present.
	if hdrName := strings.TrimSpace(r.Header.Get("Mcp-Name")); hdrName != "" && hdrName != strings.TrimSpace(params.Name) {
		s.auditEventCall(nil, auth, "events/stream", "", "header_mismatch", http.StatusBadRequest, time.Since(start))
		writeJSONRPCErrorResponse(w, http.StatusBadRequest, rpcReq.ID, newJSONRPCError(codeHeaderMismatch, "Mcp-Name header does not match params.name", nil))
		return
	}

	convID, filter, rpcErr := parseConversationEventArgs(params.Name, params.Arguments)
	if rpcErr != nil {
		s.auditEventCall(nil, auth, "events/stream", convID, "invalid_input", http.StatusBadRequest, time.Since(start))
		writeJSONRPCErrorResponse(w, http.StatusOK, rpcReq.ID, rpcErr)
		return
	}

	if params.MaxAgeMs != nil && *params.MaxAgeMs < 0 {
		s.auditEventCall(nil, auth, "events/stream", convID, "invalid_input", http.StatusBadRequest, time.Since(start))
		writeJSONRPCErrorResponse(w, http.StatusOK, rpcReq.ID, newJSONRPCError(jsonrpc.CodeInvalidParams, "maxAgeMs must be non-negative", nil))
		return
	}

	// Enforce max_streams_per_principal (§4.5).
	principalKey := auth.Principal.ID
	if principalKey == "" {
		principalKey = auth.Credential.Hash()
	}
	if !s.streamLimiter.acquire(principalKey) {
		s.auditEventCall(nil, auth, "events/stream", convID, "resource_exhausted", http.StatusTooManyRequests, time.Since(start))
		writeJSONRPCErrorResponse(w, http.StatusOK, rpcReq.ID, newJSONRPCError(
			codeResourceExhausted,
			"maximum concurrent streams per principal reached",
			map[string]any{"limit": "subscriptions", "max": s.streamLimiter.max},
		))
		return
	}
	defer s.streamLimiter.release(principalKey)

	flusher, ok := w.(http.Flusher)
	if !ok {
		s.auditEventCall(nil, auth, "events/stream", convID, "error", http.StatusInternalServerError, time.Since(start))
		writeJSONRPCErrorResponse(w, http.StatusInternalServerError, rpcReq.ID, newJSONRPCError(jsonrpc.CodeInternalError, "streaming not supported", nil))
		return
	}

	// Derive a stream context cancelled on either client disconnect or server BeginShutdown.
	streamCtx, cancelStream := context.WithCancel(r.Context())
	defer cancelStream()
	stopShutdownWatch := make(chan struct{})
	defer close(stopShutdownWatch)
	go func() {
		select {
		case <-s.shutdownCh:
			cancelStream()
		case <-streamCtx.Done():
		case <-stopShutdownWatch:
		}
	}()

	wait := s.streamWait
	if params.HeartbeatMs > 0 {
		requested := time.Duration(params.HeartbeatMs) * time.Millisecond
		minWait := time.Second
		if s.streamWait < minWait {
			minWait = s.streamWait
		}
		if requested < minWait {
			requested = minWait
		}
		if requested > s.streamWait {
			requested = s.streamWait
		}
		wait = requested
	}

	var cursor string
	if params.Cursor != nil {
		cursor = strings.TrimSpace(*params.Cursor)
	}

	var minTime time.Time
	if cursor != "" && params.MaxAgeMs != nil {
		minTime = time.Now().Add(-time.Duration(*params.MaxAgeMs) * time.Millisecond)
	}

	subID := decodeSubID(rpcReq.ID)

	// Open the initial upstream Hub connection BEFORE flushing SSE headers so
	// synchronous errors (400 invalid cursor, 401/403, 404) return a JSON-RPC
	// error response immediately per SEP-3415.
	useSSE := true
	var (
		initialStream    *hubclient.WatchStream
		initialPollRes   *hubclient.WatchResult
		initialExpired   bool
		initialTransient error
	)

	openOpts := hubclient.WatchOptions{
		Cursor: cursor,
		Filter: filter,
		Limit:  defaultReadLimit,
		Wait:   wait,
	}
	if cursor == "" {
		openOpts.Start = "head"
	}

	ws, err := s.backend.WatchStream(streamCtx, auth.Credential, convID, openOpts)
	if err != nil {
		switch {
		case errors.Is(err, hubclient.ErrWatchStreamUnsupported):
			useSSE = false
			// Fall back to long-poll: run an immediate poll to validate before opening SSE.
			pollOpts := openOpts
			pollOpts.Immediate = true
			pollOpts.Wait = 0
			pRes, pErr := s.backend.Watch(streamCtx, auth.Credential, convID, pollOpts)
			if pErr != nil {
				switch {
				case isCursorExpiredErr(pErr):
					initialExpired = true
				case isInvalidCursorErr(pErr) || isAuthOrNotFoundHubErr(pErr):
					s.auditEventCall(nil, auth, "events/stream", convID, "error", hubStatusCode(pErr), time.Since(start))
					writeJSONRPCErrorResponse(w, http.StatusOK, rpcReq.ID, mapHubErrorToJSONRPC(pErr))
					return
				default:
					initialTransient = pErr
				}
			} else {
				initialPollRes = pRes
			}
		case isCursorExpiredErr(err):
			initialExpired = true
		case isInvalidCursorErr(err) || isAuthOrNotFoundHubErr(err):
			s.auditEventCall(nil, auth, "events/stream", convID, "error", hubStatusCode(err), time.Since(start))
			writeJSONRPCErrorResponse(w, http.StatusOK, rpcReq.ID, mapHubErrorToJSONRPC(err))
			return
		default:
			initialTransient = err
		}
	} else {
		initialStream = ws
	}

	// Commit SSE response headers.
	rc := http.NewResponseController(w)
	_ = rc.SetWriteDeadline(time.Time{})
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache, no-store")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	writeSSEJSON := func(v any) error {
		b, mErr := json.Marshal(v)
		if mErr != nil {
			return mErr
		}
		if _, wErr := fmt.Fprintf(w, "data: %s\n\n", b); wErr != nil {
			return wErr
		}
		flusher.Flush()
		return nil
	}

	sendNotification := func(method string, notifParams any) error {
		return writeSSEJSON(map[string]any{
			"jsonrpc": "2.0",
			"method":  method,
			"params":  notifParams,
		})
	}

	sendActive := func(cur string, truncated bool) error {
		var curVal any
		if cur != "" {
			curVal = cur
		}
		return sendNotification("notifications/events/active", map[string]any{
			"cursor":      curVal,
			"truncated":   truncated,
			"heartbeatMs": wait.Milliseconds(),
			"_meta":       map[string]any{SubscriptionIDMetaKey: subID},
		})
	}

	sendHeartbeat := func(cur string) error {
		var curVal any
		if cur != "" {
			curVal = cur
		}
		return sendNotification("notifications/events/heartbeat", map[string]any{
			"cursor": curVal,
			"_meta":  map[string]any{SubscriptionIDMetaKey: subID},
		})
	}

	sendEvent := func(ev hubclient.WatchEvent) error {
		return sendNotification("notifications/events/event", projectEventOccurrence(ev, subID))
	}

	sendError := func(code int64, msg string, data map[string]any) error {
		errObj := map[string]any{
			"code":    code,
			"message": msg,
		}
		if len(data) > 0 {
			errObj["data"] = data
		}
		return sendNotification("notifications/events/error", map[string]any{
			"error": errObj,
			"_meta": map[string]any{SubscriptionIDMetaKey: subID},
		})
	}

	sendTerminated := func(reason, detail string) {
		msg := "Forbidden"
		if reason == "unauthorized" {
			msg = "Unauthorized"
		}
		data := map[string]any{"reason": reason}
		if detail != "" {
			data["detail"] = detail
		}
		_ = sendNotification("notifications/events/terminated", map[string]any{
			"reason": reason,
			"error": map[string]any{
				"code":    codeForbidden,
				"message": msg,
				"data":    data,
			},
			"_meta": map[string]any{SubscriptionIDMetaKey: subID},
		})
	}

	sendFinalResult := func() {
		if len(rpcReq.ID) == 0 {
			return
		}
		_ = writeSSEJSON(map[string]any{
			"jsonrpc": "2.0",
			"id":      json.RawMessage(rpcReq.ID),
			"result": map[string]any{
				"resultType": "complete",
				"_meta":      map[string]any{SubscriptionIDMetaKey: subID},
			},
		})
	}

	resultLabel := "ok"
	finalHubStatus := http.StatusOK
	defer func() {
		s.auditEventCall(nil, auth, "events/stream", convID, resultLabel, finalHubStatus, time.Since(start))
	}()

	var (
		sentActive       bool
		pendingTruncated bool
		backoff          = streamBackoffInitial
	)

	if initialExpired {
		if err := sendError(jsonrpc.CodeInternalError, "cursor expired; restarting from head", map[string]any{"reason": "cursor_expired"}); err != nil {
			return
		}
		cursor = ""
		pendingTruncated = true
	}
	if initialTransient != nil {
		if err := sendError(jsonrpc.CodeInternalError, formatHubToolError(initialTransient), map[string]any{"reason": "upstream_error"}); err != nil {
			return
		}
		if !sleepWithContext(streamCtx, backoff) {
			if s.shuttingDown.Load() {
				resultLabel = "shutdown"
				sendFinalResult()
			}
			return
		}
		backoff = nextBackoff(backoff)
	}

	if initialPollRes != nil {
		truncated := initialPollRes.Truncated || pendingTruncated
		pendingTruncated = false
		activeCur := initialPollRes.NextCursor
		if len(initialPollRes.Events) > 0 {
			activeCur = cursor
		}
		filtered := make([]hubclient.WatchEvent, 0, len(initialPollRes.Events))
		for _, ev := range initialPollRes.Events {
			if !minTime.IsZero() && !ev.OccurredAt.IsZero() && ev.OccurredAt.Before(minTime) {
				truncated = true
				continue
			}
			filtered = append(filtered, ev)
		}
		if err := sendActive(activeCur, truncated); err != nil {
			return
		}
		sentActive = true
		for _, ev := range filtered {
			if err := sendEvent(ev); err != nil {
				return
			}
			if ev.Cursor != "" {
				cursor = ev.Cursor
			}
		}
		if initialPollRes.NextCursor != "" {
			cursor = initialPollRes.NextCursor
		}
		minTime = time.Time{}
	}

	for {
		if streamCtx.Err() != nil {
			if s.shuttingDown.Load() {
				resultLabel = "shutdown"
				sendFinalResult()
			}
			return
		}

		if useSSE {
			curStream := initialStream
			initialStream = nil

			if curStream == nil {
				opts := hubclient.WatchOptions{
					Cursor: cursor,
					Filter: filter,
					Limit:  defaultReadLimit,
					Wait:   wait,
				}
				if cursor == "" {
					opts.Start = "head"
				}
				var openErr error
				curStream, openErr = s.backend.WatchStream(streamCtx, auth.Credential, convID, opts)
				if openErr != nil {
					if streamCtx.Err() != nil {
						if s.shuttingDown.Load() {
							resultLabel = "shutdown"
							sendFinalResult()
						}
						return
					}
					if errors.Is(openErr, hubclient.ErrWatchStreamUnsupported) {
						useSSE = false
						continue
					}
					if action := handleStreamHubError(openErr, &cursor, &pendingTruncated, &finalHubStatus, &resultLabel, sendError, sendTerminated, sendFinalResult); action == streamActionStop {
						return
					} else if action == streamActionRetryImmediate {
						continue
					}
					if !sleepWithContext(streamCtx, backoff) {
						if s.shuttingDown.Load() {
							resultLabel = "shutdown"
							sendFinalResult()
						}
						return
					}
					backoff = nextBackoff(backoff)
					continue
				}
			}

			// Ensure streamCtx cancellation closes curStream so Next() unblocks immediately.
			stopClose := context.AfterFunc(streamCtx, func() {
				_ = curStream.Close()
			})

			streamGotFrame := false
			for {
				frame, readErr := curStream.Next()
				if readErr != nil {
					break
				}
				streamGotFrame = true
				backoff = streamBackoffInitial

				switch frame.Kind {
				case "active":
					if frame.Cursor != "" && cursor == "" {
						cursor = frame.Cursor
					}
					trunc := frame.Truncated || pendingTruncated
					pendingTruncated = false
					if !sentActive || trunc {
						if err := sendActive(frame.Cursor, trunc); err != nil {
							stopClose()
							_ = curStream.Close()
							return
						}
						sentActive = true
					}
				case "message":
					if frame.Event != nil {
						if !minTime.IsZero() && !frame.Event.OccurredAt.IsZero() && frame.Event.OccurredAt.Before(minTime) {
							continue
						}
						if err := sendEvent(*frame.Event); err != nil {
							stopClose()
							_ = curStream.Close()
							return
						}
						if frame.Event.Cursor != "" {
							cursor = frame.Event.Cursor
						}
					} else if frame.Cursor != "" {
						cursor = frame.Cursor
					}
				case "heartbeat":
					if frame.Cursor != "" {
						cursor = frame.Cursor
					}
					if err := sendHeartbeat(cursor); err != nil {
						stopClose()
						_ = curStream.Close()
						return
					}
				case "error":
					// Hub signaled credential end; break to reconnect so Hub
					// returns 401/403 and we emit notifications/events/terminated.
					stopClose()
					_ = curStream.Close()
					curStream = nil
				}
				if curStream == nil {
					break
				}
			}

			stopClose()
			if curStream != nil {
				_ = curStream.Close()
			}
			minTime = time.Time{}

			if streamCtx.Err() != nil {
				if s.shuttingDown.Load() {
					resultLabel = "shutdown"
					sendFinalResult()
				}
				return
			}
			if !streamGotFrame {
				// Avoid tight reconnect spin if the upstream closed immediately without frames.
				if !sleepWithContext(streamCtx, backoff) {
					if s.shuttingDown.Load() {
						resultLabel = "shutdown"
						sendFinalResult()
					}
					return
				}
				backoff = nextBackoff(backoff)
			}
			continue
		}

		// Long-poll fallback mode.
		pollOpts := hubclient.WatchOptions{
			Cursor: cursor,
			Filter: filter,
			Limit:  defaultReadLimit,
			Wait:   wait,
		}
		if cursor == "" {
			pollOpts.Start = "head"
		}
		res, pollErr := s.backend.Watch(streamCtx, auth.Credential, convID, pollOpts)
		if pollErr != nil {
			if streamCtx.Err() != nil {
				if s.shuttingDown.Load() {
					resultLabel = "shutdown"
					sendFinalResult()
				}
				return
			}
			if action := handleStreamHubError(pollErr, &cursor, &pendingTruncated, &finalHubStatus, &resultLabel, sendError, sendTerminated, sendFinalResult); action == streamActionStop {
				return
			} else if action == streamActionRetryImmediate {
				continue
			}
			if !sleepWithContext(streamCtx, backoff) {
				if s.shuttingDown.Load() {
					resultLabel = "shutdown"
					sendFinalResult()
				}
				return
			}
			backoff = nextBackoff(backoff)
			continue
		}

		backoff = streamBackoffInitial
		if !sentActive || pendingTruncated || res.Truncated {
			trunc := res.Truncated || pendingTruncated
			pendingTruncated = false
			activeCur := res.NextCursor
			if len(res.Events) > 0 {
				activeCur = cursor
			}
			if err := sendActive(activeCur, trunc); err != nil {
				return
			}
			sentActive = true
		}

		for _, ev := range res.Events {
			if err := sendEvent(ev); err != nil {
				return
			}
			if ev.Cursor != "" {
				cursor = ev.Cursor
			}
		}
		if res.NextCursor != "" {
			cursor = res.NextCursor
		}
		if len(res.Events) == 0 && res.TimedOut {
			if err := sendHeartbeat(cursor); err != nil {
				return
			}
		}
		if res.RetryAfter > 0 {
			if !sleepWithContext(streamCtx, res.RetryAfter) {
				if s.shuttingDown.Load() {
					resultLabel = "shutdown"
					sendFinalResult()
				}
				return
			}
		}
	}
}

type streamErrorAction int

const (
	streamActionBackoff streamErrorAction = iota
	streamActionRetryImmediate
	streamActionStop
)

func handleStreamHubError(
	err error,
	cursor *string,
	pendingTruncated *bool,
	finalHubStatus *int,
	resultLabel *string,
	sendError func(code int64, msg string, data map[string]any) error,
	sendTerminated func(reason, detail string),
	sendFinalResult func(),
) streamErrorAction {
	if isCursorExpiredErr(err) {
		if wErr := sendError(jsonrpc.CodeInternalError, "cursor expired; restarting from head", map[string]any{"reason": "cursor_expired"}); wErr != nil {
			return streamActionStop
		}
		*cursor = ""
		*pendingTruncated = true
		return streamActionRetryImmediate
	}

	var apiErr *apiclient.APIError
	if errors.As(err, &apiErr) {
		switch apiErr.StatusCode {
		case http.StatusUnauthorized:
			*finalHubStatus = http.StatusUnauthorized
			*resultLabel = "terminated_unauthorized"
			sendTerminated("unauthorized", apiErr.Message)
			sendFinalResult()
			return streamActionStop
		case http.StatusForbidden:
			*finalHubStatus = http.StatusForbidden
			*resultLabel = "terminated_forbidden"
			sendTerminated("forbidden", apiErr.Message)
			sendFinalResult()
			return streamActionStop
		case http.StatusNotFound:
			*finalHubStatus = http.StatusNotFound
			*resultLabel = "terminated_not_found"
			sendTerminated("forbidden", apiErr.Message)
			sendFinalResult()
			return streamActionStop
		case http.StatusBadRequest:
			*finalHubStatus = http.StatusBadRequest
			*resultLabel = "invalid_cursor"
			sendTerminated("invalid_cursor", apiErr.Message)
			sendFinalResult()
			return streamActionStop
		}
	}

	*finalHubStatus = hubStatusCode(err)
	if wErr := sendError(jsonrpc.CodeInternalError, formatHubToolError(err), map[string]any{"reason": "upstream_error"}); wErr != nil {
		return streamActionStop
	}
	return streamActionBackoff
}

func isAuthOrNotFoundHubErr(err error) bool {
	var apiErr *apiclient.APIError
	if !errors.As(err, &apiErr) {
		return false
	}
	return apiErr.StatusCode == http.StatusUnauthorized ||
		apiErr.StatusCode == http.StatusForbidden ||
		apiErr.StatusCode == http.StatusNotFound
}

func nextBackoff(cur time.Duration) time.Duration {
	if cur <= 0 {
		return streamBackoffInitial
	}
	next := cur * 2
	if next > streamBackoffMax {
		return streamBackoffMax
	}
	return next
}

func sleepWithContext(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

func decodeSubID(raw json.RawMessage) any {
	if len(raw) == 0 {
		return nil
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return string(raw)
	}
	return v
}

func writeJSONRPCErrorResponse(w http.ResponseWriter, httpStatus int, id json.RawMessage, rpcErr *jsonrpc.Error) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(httpStatus)
	resp := map[string]any{
		"jsonrpc": "2.0",
		"error":   rpcErr,
	}
	if len(id) > 0 {
		resp["id"] = json.RawMessage(id)
	} else {
		resp["id"] = nil
	}
	_ = json.NewEncoder(w).Encode(resp)
}

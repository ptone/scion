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
	"log/slog"
	"net/http"
	"time"

	"github.com/GoogleCloudPlatform/scion/pkg/util/logging"
)

// perfTraceMiddleware installs a fresh *PerfTrace into the request context
// when perfTraceEnabled, and emits one structured log line with the
// accumulated phase/store-call/decision data after the handler returns.
//
// When disabled (the default), this is a single boolean check followed by
// a direct call to next.ServeHTTP: no trace allocated, no context value
// installed, no timer started.
//
// Registered as the innermost middleware in applyMiddleware (server.go) --
// i.e. the last one whose context modifications reach the actual route
// handler -- so PerfTraceFromContext(ctx) is available all the way down
// through listProjectAgents / listAgents and, via AuthzService's wrapped
// store (see perftrace_store.go) and AuthzService.Decide (authz.go), the
// authorization kernel itself.
func (s *Server) perfTraceMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !perfTraceEnabled {
			next.ServeHTTP(w, r)
			return
		}

		trace := newPerfTrace()
		ctx := ContextWithPerfTrace(r.Context(), trace)
		r = r.WithContext(ctx)

		start := time.Now()
		next.ServeHTTP(w, r)
		total := time.Since(start)

		if s.perfTraceLog == nil {
			return
		}
		attrs := trace.LogAttrs()
		attrs = append(attrs,
			slog.String("method", r.Method),
			slog.String("path", r.URL.Path),
			slog.Int64("total_ms", total.Milliseconds()),
		)
		if meta := logging.RequestMetaFromContext(ctx); meta != nil && meta.RequestID != "" {
			attrs = append(attrs, slog.String("request_id", meta.RequestID))
		}
		// Bounded, fixed-key attributes only (see PerfTrace.LogAttrs) plus
		// method/path/request_id, which RequestLogMiddleware already logs
		// for every request -- nothing here is a config value, env entry,
		// or credential.
		s.perfTraceLog.LogAttrs(ctx, slog.LevelInfo, "perf_trace", attrs...)
	})
}

// writePerfTraceHeaders sets response headers carrying the request's
// accumulated perf-trace data, if tracing is enabled AND the caller opted
// in via HeaderPerfTraceRequest. Requiring the per-request opt-in on top of
// the server-wide flag means enabling tracing does not by itself expose
// timing/count data to every caller.
//
// Must be called before the handler's first write to w (i.e. immediately
// before writeJSON), since HTTP forbids setting headers after the status
// line is sent. Consequently this cannot include the handler's own
// "serialize" phase, which by definition has not ended yet at this point --
// that phase is still captured in full by the structured log line
// perfTraceMiddleware emits after the whole request (including
// serialization) completes.
func writePerfTraceHeaders(w http.ResponseWriter, r *http.Request) {
	if !perfTraceEnabled || r.Header.Get(HeaderPerfTraceRequest) != "1" {
		return
	}
	trace := PerfTraceFromContext(r.Context())
	if trace == nil {
		return
	}
	phases, storeCalls, decisions := trace.HeaderValues()
	if phases != "" {
		w.Header().Set(HeaderPerfTracePhases, phases)
	}
	if storeCalls != "" {
		w.Header().Set(HeaderPerfTraceStoreCalls, storeCalls)
	}
	w.Header().Set(HeaderPerfTraceDecisions, decisions)
}

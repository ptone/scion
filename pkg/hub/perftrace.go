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
	"context"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

// perfTraceEnabled gates the entire ptone/scion#2392 instrumentation
// feature: bounded-cardinality timing/count instrumentation for agent-list
// phases and authorization store-call amplification, added to quantify the
// server-side bottleneck ptone/scion#2367 diagnosed (repeated authorization
// evaluation per agent per list request).
//
// Read once at process start, not per-request, so checking it costs a
// single boolean comparison. When false (the default in every environment
// unless explicitly opted into), no *PerfTrace is ever installed into any
// context, WrapStoreForPerfTrace returns its input unchanged, and every
// StartPhase/RecordDecision call below resolves to a nil-receiver no-op --
// see each function's doc comment for exactly what "cheap when disabled"
// means at that call site.
var perfTraceEnabled = os.Getenv("SCION_HUB_PERF_TRACE") == "1"

// HeaderPerfTraceRequest is the opt-in request header a caller sets to get
// perf-trace data back on the response, in addition to (not instead of) the
// structured log line perfTraceMiddleware always emits while tracing is
// enabled. Requiring this per-request opt-in on top of the server-wide
// SCION_HUB_PERF_TRACE=1 means turning tracing on hub-wide does not by
// itself hand timing/count data to every caller -- only requests that ask
// for it get it back.
const HeaderPerfTraceRequest = "X-Scion-Perf-Trace"

// Response headers set on an opt-in request while tracing is enabled (see
// writePerfTraceHeaders in perftrace_middleware.go). Values are compact
// comma-separated name=integer pairs; see PerfTrace.HeaderValues for the
// exact format and the no-sensitive-content guarantee.
const (
	HeaderPerfTracePhases     = "X-Scion-Perf-Phases"
	HeaderPerfTraceStoreCalls = "X-Scion-Perf-Store-Calls"
	HeaderPerfTraceDecisions  = "X-Scion-Perf-Decisions"
)

// PerfTrace accumulates bounded-cardinality timing and count data for a
// single request: wall-clock duration per named phase (list fetch,
// enrichment, capability evaluation, messageability, serialization, audit
// dispatch, ...), a count of the AK1 kernel decisions evaluated for the
// request plus their cumulative duration, and call counts for the specific
// store methods ptone/scion#2367 identified as the authorization hot path
// (see perftrace_store.go).
//
// Cardinality is bounded by construction: every phase name and store-call
// method name passed to AddPhase/IncStoreCall below is a fixed string
// literal chosen at the call site, never a caller- or request-derived
// value. A PerfTrace can therefore never grow unbounded, and its exported
// views (HeaderValues, LogAttrs) can never leak request content through map
// keys -- only these fixed names and integer durations/counts.
type PerfTrace struct {
	mu                sync.Mutex
	phaseDurations    map[string]time.Duration
	storeCalls        map[string]int64
	decisions         int64
	decisionsDuration time.Duration
}

func newPerfTrace() *PerfTrace {
	return &PerfTrace{
		phaseDurations: make(map[string]time.Duration),
		storeCalls:     make(map[string]int64),
	}
}

// AddPhase accumulates duration under name. A phase name may be recorded
// more than once per request (e.g. a loop that times each iteration); the
// durations sum. Safe for concurrent use. A nil receiver is a no-op, so
// every caller in this package can call it unconditionally on whatever
// PerfTraceFromContext(ctx) returns, tracing enabled or not.
func (t *PerfTrace) AddPhase(name string, d time.Duration) {
	if t == nil {
		return
	}
	t.mu.Lock()
	t.phaseDurations[name] += d
	t.mu.Unlock()
}

// IncStoreCall increments the call count for the named store method.
// Nil-receiver no-op, like AddPhase.
func (t *PerfTrace) IncStoreCall(name string) {
	if t == nil {
		return
	}
	t.mu.Lock()
	t.storeCalls[name]++
	t.mu.Unlock()
}

// RecordDecision records one AK1 kernel decision's evaluation duration.
// Nil-receiver no-op, like AddPhase.
func (t *PerfTrace) RecordDecision(d time.Duration) {
	if t == nil {
		return
	}
	t.mu.Lock()
	t.decisions++
	t.decisionsDuration += d
	t.mu.Unlock()
}

// snapshot returns independent copies of the accumulated data, safe to
// range over after the lock is released.
func (t *PerfTrace) snapshot() (phases map[string]time.Duration, storeCalls map[string]int64, decisions int64, decisionsDuration time.Duration) {
	t.mu.Lock()
	defer t.mu.Unlock()
	phases = make(map[string]time.Duration, len(t.phaseDurations))
	for k, v := range t.phaseDurations {
		phases[k] = v
	}
	storeCalls = make(map[string]int64, len(t.storeCalls))
	for k, v := range t.storeCalls {
		storeCalls[k] = v
	}
	return phases, storeCalls, t.decisions, t.decisionsDuration
}

// HeaderValues renders the trace as three compact strings suitable for HTTP
// response headers: phase durations in whole milliseconds
// ("db_fetch=12,enrich=3"), store-call counts
// ("GetEffectiveGroups=1,GetRoleDefinitionsByIDs=1"), and a decisions
// summary ("count=234,ms=410"). Entries are sorted for stable,
// diffable/greppable output. A nil receiver returns three empty strings.
func (t *PerfTrace) HeaderValues() (phases, storeCalls, decisions string) {
	if t == nil {
		return "", "", ""
	}
	p, s, dCount, dDur := t.snapshot()

	phaseParts := make([]string, 0, len(p))
	for name, d := range p {
		phaseParts = append(phaseParts, fmt.Sprintf("%s=%d", name, d.Milliseconds()))
	}
	sort.Strings(phaseParts)

	storeParts := make([]string, 0, len(s))
	for name, c := range s {
		storeParts = append(storeParts, fmt.Sprintf("%s=%d", name, c))
	}
	sort.Strings(storeParts)

	return strings.Join(phaseParts, ","), strings.Join(storeParts, ","), fmt.Sprintf("count=%d,ms=%d", dCount, dDur.Milliseconds())
}

// LogAttrs renders the trace as slog attributes for the structured
// per-request log line perfTraceMiddleware emits. Same bounded-cardinality,
// fixed-key guarantee as HeaderValues: attribute keys are always
// "phase_<literal>_ms" / "store_calls_<literal>" for the fixed set of names
// used at the StartPhase/IncStoreCall call sites, never request-derived, so
// this can never log a project ID, agent name, config value, or token. A
// nil receiver returns nil.
func (t *PerfTrace) LogAttrs() []slog.Attr {
	if t == nil {
		return nil
	}
	p, s, dCount, dDur := t.snapshot()
	attrs := make([]slog.Attr, 0, len(p)+len(s)+2)
	for name, d := range p {
		attrs = append(attrs, slog.Int64("phase_"+name+"_ms", d.Milliseconds()))
	}
	for name, c := range s {
		attrs = append(attrs, slog.Int64("store_calls_"+name, c))
	}
	attrs = append(attrs, slog.Int64("decisions", dCount), slog.Int64("decisions_ms", dDur.Milliseconds()))
	return attrs
}

type perfTraceKey struct{}

// ContextWithPerfTrace installs t into ctx. Called once per request, by
// perfTraceMiddleware, only when perfTraceEnabled.
func ContextWithPerfTrace(ctx context.Context, t *PerfTrace) context.Context {
	return context.WithValue(ctx, perfTraceKey{}, t)
}

// PerfTraceFromContext retrieves the request's PerfTrace, or nil if tracing
// is disabled (the common case: no trace was ever installed) or the
// context did not come from a traced request. Every method on *PerfTrace
// treats a nil receiver as a no-op, so callers never need a separate nil
// check -- see StartPhase and RecordDecision below for the resulting
// call-site pattern.
func PerfTraceFromContext(ctx context.Context) *PerfTrace {
	t, _ := ctx.Value(perfTraceKey{}).(*PerfTrace)
	return t
}

// noopStopPhase is the stop function StartPhase returns when there is
// nothing to record into. A single shared value, so the disabled path
// (perfTraceEnabled == false, the default) never allocates a closure.
func noopStopPhase() {}

// StartPhase begins timing a named phase and returns a function that
// records the elapsed duration when called. Intended usage:
//
//	done := StartPhase(ctx, "db_fetch")
//	result, err := s.store.ListAgents(ctx, filter, opts)
//	done()
//
// Cheap when disabled: PerfTraceFromContext does one context.Value lookup,
// finds no trace (none was ever installed), and this returns the shared
// noopStopPhase -- no timer started, no closure allocated, no lock taken.
func StartPhase(ctx context.Context, name string) func() {
	t := PerfTraceFromContext(ctx)
	if t == nil {
		return noopStopPhase
	}
	start := time.Now()
	return func() { t.AddPhase(name, time.Since(start)) }
}

// RecordDecision records one AK1 kernel decision's evaluation duration
// against the request's trace, if any. Called from AuthzService.Decide
// (authz.go), the single choke point every authorization decision passes
// through, so this is the exact per-request "decisions evaluated" count
// ptone/scion#2367 measured indirectly via persisted decision-audit rows
// (949 for one 117-agent project load). Cheap when disabled: same
// nil-lookup-and-return path as StartPhase.
func RecordDecision(ctx context.Context, d time.Duration) {
	PerfTraceFromContext(ctx).RecordDecision(d)
}

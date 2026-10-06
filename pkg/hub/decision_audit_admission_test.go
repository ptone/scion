// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package hub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/GoogleCloudPlatform/scion/pkg/experiments"
	"github.com/GoogleCloudPlatform/scion/pkg/store"
)

// This finite fixture graph is test-only. Modes are a closed enum; no arbitrary
// handler callback, production name whitelist or Approved bit grants admission.
// One fixture owns one clock, one timer slot, one handler, one legacy recorder,
// one router, one caller and one finite settings source. Captures cannot be swapped
// during invocation. Mutation tests change actual facts before validation only.
type auditFixtureMode uint8

const (
	auditFixtureAccept auditFixtureMode = iota
	auditFixtureError
	auditFixturePanic
	auditFixtureCancel
	auditFixtureNonAcceptance
	auditFixtureReenter
	auditFixtureDrain
	auditFixtureCallerCancel
)

// A closed caller graph: no parent, goroutine, real timer, arbitrary value
// provider or blocking wait. Only this fixture's Cancel can close its channel.
type auditFixtureCaller struct {
	done chan struct{}
	once sync.Once
}

func (c *auditFixtureCaller) Deadline() (time.Time, bool) { return time.Time{}, false }
func (c *auditFixtureCaller) Done() <-chan struct{}       { return c.done }
func (c *auditFixtureCaller) Value(any) any               { return nil }
func (c *auditFixtureCaller) Err() error {
	select {
	case <-c.done:
		return context.Canceled
	default:
		return nil
	}
}
func (c *auditFixtureCaller) Cancel() { c.once.Do(func() { close(c.done) }) }

// Every method panics: rejection must use live identity without invoking one.
// This hostile context owns no parent, wait, callback, timer or goroutine.
type auditHostileCaller struct{}

func (*auditHostileCaller) Deadline() (time.Time, bool) { panic("unproved caller Deadline invoked") }
func (*auditHostileCaller) Done() <-chan struct{}       { panic("unproved caller Done invoked") }
func (*auditHostileCaller) Value(any) any               { panic("unproved caller Value invoked") }
func (*auditHostileCaller) Err() error                  { panic("unproved caller Err invoked") }

type auditFixtureClock struct {
	tick    time.Duration
	epoch   uint64
	valid   bool
	timerAt time.Duration
	cancel  context.CancelFunc
}

func (c *auditFixtureClock) Read() decisionAuditClockReading {
	return decisionAuditClockReading{tick: c.tick, epoch: c.epoch, valid: c.valid}
}
func (c *auditFixtureClock) CancelAt(at time.Duration, cancel context.CancelFunc) func() {
	// The local reviewed scheduler has exactly one timer, no goroutine or queue.
	if c.cancel != nil {
		panic("fixture timer capacity exceeded")
	}
	c.timerAt, c.cancel = at, cancel
	return func() { c.cancel = nil }
}
func (c *auditFixtureClock) advance(tick time.Duration) {
	c.tick = tick
	if c.cancel != nil && tick >= c.timerAt {
		cancel := c.cancel
		c.cancel = nil
		cancel()
	}
}

type auditFixtureLegacy struct{ records []*store.DecisionAuditRecord }

func (l *auditFixtureLegacy) EmitDecisionAudit(_ context.Context, record *store.DecisionAuditRecord) {
	if len(l.records) >= 8 {
		panic("finite legacy fixture capacity exceeded")
	}
	l.records = append(l.records, record)
}

type auditFixtureHandler struct {
	mode                auditFixtureMode
	clock               *auditFixtureClock
	router              *decisionAuditRouter
	calls               int
	records             []store.DecisionAuditRecord
	returned            bool
	canceled            bool
	retained            *store.DecisionAuditRecord
	caller              *auditFixtureCaller
	callerCanceledOwned bool
}

func (h *auditFixtureHandler) AcceptDecision(ctx context.Context, record *store.DecisionAuditRecord) (decisionAuditAcceptance, error) {
	h.calls++
	if h.calls > 8 {
		panic("finite handler invocation bound exceeded")
	}
	h.records = append(h.records, *record)
	h.retained = record
	defer func() { h.retained = nil; h.returned = true }()
	switch h.mode {
	case auditFixtureError:
		return decisionAuditAcceptance{}, errors.New("finite fixture error")
	case auditFixturePanic:
		panic("finite fixture panic")
	case auditFixtureCancel:
		return decisionAuditAcceptance{}, context.Canceled
	case auditFixtureCallerCancel:
		// Cancel the originating admitted caller after ownership, then accept.
		// This is distinct from returning a canceled error or firing the timer.
		h.callerCanceledOwned = h.router.inspect().active == decisionAuditNewCapacity && h.caller.Err() == nil
		h.caller.Cancel()
	case auditFixtureNonAcceptance:
		return decisionAuditAcceptance{}, nil
	case auditFixtureReenter:
		// A deterministic scheduler interleaving while the original slot is owned.
		// Bound recursion so a broken K check fails assertions rather than hangs.
		if h.calls == 1 {
			h.router.EmitDecisionAudit(ctx, &store.DecisionAuditRecord{Result: "deny", ResourceType: "project"})
		}
	case auditFixtureDrain:
		h.router.invalidateSource()
		h.clock.advance(h.clock.tick + decisionAuditCancelBudget)
		h.canceled = ctx.Err() != nil
	}
	return decisionAuditAcceptance{accepted: true}, nil
}

type auditFixture struct {
	router   *decisionAuditRouter
	handler  *auditFixtureHandler
	clock    *auditFixtureClock
	legacy   *auditFixtureLegacy
	contract decisionAuditLocalContract
	settings *auditFixtureSettingStore
	ops      *OperationalSettings
	caller   *auditFixtureCaller
}

func newAuditFixture(t *testing.T, mode auditFixtureMode) *auditFixture {
	t.Helper()
	clock := &auditFixtureClock{epoch: 1, valid: true}
	legacy := &auditFixtureLegacy{}
	srv := &Server{}
	// This registry exists only in the fixture. The real compiled registry test
	// independently demands the production default-false registration.
	reg, err := experiments.NewRegistry([]experiments.Experiment{{
		Name: experiments.AuthorizationDecisionAuditV2, Title: "Authorization decision audit v2",
		Description: "Finite test-domain decision routing only.", Layers: []experiments.Layer{experiments.LayerServer},
		Stage: experiments.StageAlpha, Issue: "ptone/scion#2379", Owner: "audit-update", ReviewBy: "2026-11-30",
	}}, nil)
	if err != nil {
		t.Fatalf("finite registry construction: %v", err)
	}
	srv.experiments = reg
	router := newDecisionAuditRouter(legacy, srv)
	srv.decisionAuditRouter = router
	caller := &auditFixtureCaller{done: make(chan struct{})}
	handler := &auditFixtureHandler{mode: mode, clock: clock, router: router, caller: caller}
	manifest := decisionAuditManifestV1{
		version: decisionAuditManifestVersion, source: "fixture/591e24c", profile: fmt.Sprintf("finite-mode-%d", mode),
		root: "sampled-decision", signature: "EmitDecisionAudit(context.Context,*store.DecisionAuditRecord)",
		ratification: "Amendment14/finite-local-mechanics-only", generation: 1, clockEpoch: 1, expires: time.Hour,
		entries: []decisionAuditManifestEntry{
			{ordinal: 0, slot: "root", receiver: "decisionAuditRouter", target: "EmitDecisionAudit", signature: "context,record->void", capture: "legacy+handler+clock+generation+caller", contract: "exclusive-K1", instance: 1, generation: 1},
			{ordinal: 1, slot: "handler", receiver: "auditFixtureHandler", target: "AcceptDecision", signature: "context,record->acceptance,error", capture: fmt.Sprintf("mode-%d+clock+router+caller", mode), contract: "finite-sync-return-2s", instance: 2, generation: 1},
			{ordinal: 2, slot: "clock", receiver: "auditFixtureClock", target: "Read", signature: "->elapsed,epoch,valid", capture: "single-tick-epoch", contract: "injected-elapsed", instance: 3, generation: 1},
			{ordinal: 3, slot: "timer", receiver: "auditFixtureClock", target: "CancelAt", signature: "elapsed,cancel->stop", capture: "one-timer+context-cancel", contract: "finite-single-timer", instance: 3, generation: 1},
			{ordinal: 4, slot: "legacy", receiver: "auditFixtureLegacy", target: "EmitDecisionAudit", signature: "context,record->void", capture: "eight-record-array", contract: "finite-recorder", instance: 4, generation: 1},
			{ordinal: 5, slot: "settings", receiver: "auditFixtureSettingStore", target: "ListHubSettings", signature: "context->bounded-rows,error", capture: "single-row+clock+closed-mode", contract: "cooperative-read-1s", instance: 5, generation: 1},
			{ordinal: 6, slot: "caller", receiver: "auditFixtureCaller", target: "Err/Done/Deadline/Value/Cancel", signature: "bounded-context+cancel->canceled", capture: "one-close-channel+no-parent", contract: "finite-caller-cancellation", instance: 6, generation: 1},
		},
	}
	actual := manifest
	actual.entries = append([]decisionAuditManifestEntry(nil), manifest.entries...)
	contract := decisionAuditLocalContract{expected: manifest, actual: actual, clock: clock, handler: handler}
	f := &auditFixture{router: router, handler: handler, clock: clock, legacy: legacy, contract: contract, caller: caller}
	f.ops, f.settings = newAuditFixtureSettings(t, f)
	binding := decisionAuditLocalBinding{root: router, legacy: legacy, handler: handler, clock: clock, settings: f.ops, caller: caller}
	actualBinding := binding
	f.contract.expected.binding, f.contract.actual.binding = &binding, &actualBinding
	admission, _ := validateDecisionAuditManifest(f.contract)
	router.contract, router.admission = f.contract, admission
	return f
}

// Each observation uses the actual owned Refresh publication seam and the
// finite authoritative fixture. Sequence labels describe test ordering only;
// the observer itself must issue and validate its own sequence/attachment token.
func (f *auditFixture) observe(_ uint64, revision int64, enabled bool) decisionAuditRefreshObservation {
	f.settings.mu.Lock()
	f.settings.settings["experiments"].Value = json.RawMessage(fmt.Sprintf(`{"overrides":{"hub.authorization_decision_audit_v2":%t}}`, enabled))
	f.settings.settings["experiments"].Revision = revision
	f.settings.mu.Unlock()
	_, _ = f.ops.Refresh(context.Background())
	_, observation := f.ops.decisionAuditSnapshot()
	return observation
}
func (f *auditFixture) emit() {
	f.router.EmitDecisionAudit(f.caller, &store.DecisionAuditRecord{Result: "allow", ResourceType: "project", ResourceID: "finite-fixture"})
}
func (f *auditFixture) requireAdmission(t *testing.T) {
	t.Helper()
	if f.router.admission == nil {
		t.Fatal("complete finite fixture admission required (Stage A intentionally rejects)")
	}
}

func TestDecisionAuditAdmission_ExactFiniteManifest(t *testing.T) {
	f := newAuditFixture(t, auditFixtureAccept)
	admission, rejection := validateDecisionAuditManifest(f.contract)
	if admission == nil {
		t.Errorf("exact finite manifest admission is nil/rejected (%s); want one nonempty admission", rejection)
		return
	}
	if len(admission.canonical) == 0 || admission.manifest.generation != 1 || len(admission.manifest.entries) != len(f.contract.expected.entries) {
		t.Fatal("admission must retain complete immutable canonical facts")
	}
	// Mutating constructor input must not mutate the admitted value.
	f.contract.actual.entries[0].target = "drift"
	f.contract.actual.binding.handler = &auditUnprovedHandler{}
	if admission.manifest.entries[0].target != "EmitDecisionAudit" || admission.manifest.binding == nil || admission.manifest.binding.handler != f.handler {
		t.Fatal("admission aliases mutable manifest input")
	}
}

func TestDecisionAuditAdmission_RejectsWholeManifest(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*decisionAuditLocalContract)
	}{
		{"missing", func(c *decisionAuditLocalContract) { c.actual.entries = c.actual.entries[:5] }},
		{"extra", func(c *decisionAuditLocalContract) { c.actual.entries = append(c.actual.entries, c.actual.entries[0]) }},
		{"duplicate", func(c *decisionAuditLocalContract) { c.actual.entries[1] = c.actual.entries[0] }},
		{"reorder", func(c *decisionAuditLocalContract) {
			c.actual.entries[0], c.actual.entries[1] = c.actual.entries[1], c.actual.entries[0]
		}},
		{"signature", func(c *decisionAuditLocalContract) { c.actual.entries[1].signature = "changed" }},
		{"receiver", func(c *decisionAuditLocalContract) { c.actual.entries[1].receiver = "changed" }},
		{"instance", func(c *decisionAuditLocalContract) { c.actual.entries[1].instance++ }},
		{"live-root", func(c *decisionAuditLocalContract) { c.actual.binding.root = nil }},
		{"live-clock", func(c *decisionAuditLocalContract) { c.actual.binding.clock = nil }},
		{"live-caller", func(c *decisionAuditLocalContract) {
			c.actual.binding.caller = &auditFixtureCaller{done: make(chan struct{})}
		}},
		{"live-settings", func(c *decisionAuditLocalContract) { c.actual.binding.settings = nil }},
		{"live-handler", func(c *decisionAuditLocalContract) { c.actual.binding.handler = &auditUnprovedHandler{} }},
		{"target", func(c *decisionAuditLocalContract) { c.actual.entries[1].target = "changed" }},
		{"capture", func(c *decisionAuditLocalContract) { c.actual.entries[1].capture = "changed" }},
		{"contract", func(c *decisionAuditLocalContract) { c.actual.entries[1].contract = "unknown" }},
		{"generation", func(c *decisionAuditLocalContract) { c.actual.generation++ }},
		{"version", func(c *decisionAuditLocalContract) { c.actual.version = "unknown" }},
		{"expired", func(c *decisionAuditLocalContract) { c.actual.expires = 0 }},
		{"unratified", func(c *decisionAuditLocalContract) { c.expected.ratification = "" }},
		{"too-large", func(c *decisionAuditLocalContract) {
			c.actual.entries[1].signature = strings.Repeat("x", decisionAuditManifestMaxBytes+1)
		}},
		{"collision", func(c *decisionAuditLocalContract) { c.actual.entries[1].slot = c.actual.entries[0].slot }},
		{"too-many", func(c *decisionAuditLocalContract) {
			for len(c.actual.entries) <= decisionAuditManifestMaxEntries {
				c.actual.entries = append(c.actual.entries, c.actual.entries[0])
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newAuditFixture(t, auditFixtureAccept)
			tc.mutate(&f.contract)
			a, r := validateDecisionAuditManifest(f.contract)
			if a != nil || r == "" {
				t.Fatalf("whole rejection required, got %v/%q", a, r)
			}
		})
	}
}

// An unproved implementation is never invoked, including on rejection paths.
type auditUnprovedHandler struct{ calls int }

func (h *auditUnprovedHandler) AcceptDecision(context.Context, *store.DecisionAuditRecord) (decisionAuditAcceptance, error) {
	h.calls++
	panic("unproved handler invoked")
}
func TestDecisionAuditAdmission_RejectsUnprovedHandlerAndClock(t *testing.T) {
	for _, mode := range []string{"no-handler", "no-clock", "unknown-clock", "unknown-handler", "no-timer-proof", "no-read-proof", "no-return-proof"} {
		t.Run(mode, func(t *testing.T) {
			f := newAuditFixture(t, auditFixtureAccept)
			unknown := &auditUnprovedHandler{}
			switch mode {
			case "no-handler":
				f.contract.handler = nil
			case "no-clock":
				f.contract.clock = nil
			case "unknown-clock":
				f.clock.valid = false
			case "unknown-handler":
				f.contract.handler = unknown
			case "no-timer-proof":
				f.contract.actual.entries[3].contract = ""
			case "no-read-proof":
				f.contract.actual.entries[5].contract = ""
			case "no-return-proof":
				f.contract.actual.entries[1].contract = "context-ignoring-unbounded"
			}
			a, r := validateDecisionAuditManifest(f.contract)
			if a != nil || r == "" {
				t.Fatal("unproved contract accepted")
			}
			if unknown.calls != 0 {
				t.Fatal("unproved handler ran before rejection")
			}
		})
	}
}

func TestDecisionAuditAdmission_LeaseBoundaryAndEpoch(t *testing.T) {
	for _, mode := range []string{"before", "at", "after", "epoch", "regression", "unknown", "wall-metadata"} {
		t.Run(mode, func(t *testing.T) {
			f := newAuditFixture(t, auditFixtureAccept)
			f.requireAdmission(t)
			f.clock.tick = time.Second
			o := f.observe(1, 1, true)
			if !o.successful || o.deadline != time.Second+decisionAuditLease {
				t.Fatal("successful lease must anchor to q0")
			}
			f.clock.tick = o.deadline - time.Nanosecond
			want := 1
			switch mode {
			case "at":
				f.clock.tick = o.deadline
				want = 0
			case "after":
				f.clock.tick = o.deadline + time.Nanosecond
				want = 0
			case "epoch":
				f.clock.epoch++
				want = 0
			case "regression":
				f.clock.tick = 0
				want = 0
			case "unknown":
				f.clock.valid = false
				want = 0
			case "wall-metadata":
				o.snapshot.UpdatedAt = time.Unix(1<<30, 0)
				f.clock.tick = o.deadline
				want = 0
			}
			f.emit()
			if f.handler.calls != want || len(f.legacy.records) != 1-want {
				t.Fatal("lease/epoch handoff ownership mismatch")
			}
		})
	}
	if decisionAuditLease+decisionAuditLeaseSlack != 75*time.Second {
		t.Fatal("75-second total bound changed")
	}
}

func TestDecisionAuditRouter_LegacyRejectedStates(t *testing.T) {
	for _, mode := range []string{"off", "absent", "malformed", "unknown", "stale", "unhealthy", "unratified", "drift", "capacity", "unproved-caller", "pre-canceled-caller"} {
		t.Run(mode, func(t *testing.T) {
			f := newAuditFixture(t, auditFixtureAccept)
			f.observe(1, 1, mode != "off")
			var caller context.Context = f.caller
			f.router.gate.Lock()
			switch mode {
			case "unproved-caller":
				caller = &auditHostileCaller{}
			case "pre-canceled-caller":
				f.caller.Cancel()
			case "absent":
				f.router.state.observation.snapshot.Present = false
			case "malformed":
				f.router.state.observation.snapshot.Malformed = true
			case "unknown":
				f.router.state.observation.snapshot.Overrides = map[string]bool{"hub.unknown": true}
			case "stale":
				f.clock.tick = decisionAuditLease
			case "unhealthy":
				f.router.state.fault = true
			case "unratified":
				f.router.admission = nil
			case "drift":
				f.router.contract.actual.generation++
			case "capacity":
				f.router.state.active = decisionAuditNewCapacity
			}
			f.router.gate.Unlock()
			f.router.EmitDecisionAudit(caller, &store.DecisionAuditRecord{Result: "allow", ResourceType: "project", ResourceID: "finite-fixture"})
			if len(f.legacy.records) != 1 || f.handler.calls != 0 {
				t.Fatal("rejected record requires exactly one legacy owner")
			}
		})
	}
}

func TestDecisionAuditRouter_OneSynchronousNewOwner(t *testing.T) {
	f := newAuditFixture(t, auditFixtureAccept)
	f.observe(1, 1, true)
	authz := &AuthzService{DecisionAuditSampleRate: 1}
	authz.SetDecisionAuditEmitter(f.router)
	request := AuthzRequest{Resource: Resource{Type: "project", ID: "finite-fixture"}, Action: ActionRead}
	decision := Decision{Allowed: true, Reason: "finite authorization result"}
	authz.emitDecisionAudit(f.caller, request, decision)
	if !decision.Allowed || decision.Reason != "finite authorization result" {
		t.Fatal("audit changed authorization result")
	}
	if f.handler.calls+len(f.legacy.records) != 1 {
		t.Fatal("sampled decision must have exactly one owner")
	}
	if len(f.legacy.records) == 1 && f.legacy.records[0].Result != "allow" {
		t.Fatal("legacy result mapping changed")
	}
	if f.handler.calls != 1 {
		t.Errorf("NEW count=%d, want 1; legacy count=%d (Stage A expected 1)", f.handler.calls, len(f.legacy.records))
		return
	}
	if len(f.legacy.records) != 0 || !f.handler.returned || f.handler.retained != nil || f.router.inspect().active != 0 {
		t.Fatal("NEW must return synchronously, release references, and exclude legacy")
	}
}

func TestDecisionAuditRouter_NewFailureNoFallback(t *testing.T) {
	for _, mode := range []auditFixtureMode{auditFixtureError, auditFixturePanic, auditFixtureCancel, auditFixtureNonAcceptance, auditFixtureCallerCancel} {
		t.Run(fmt.Sprint(mode), func(t *testing.T) {
			f := newAuditFixture(t, mode)
			f.requireAdmission(t)
			f.observe(1, 1, true)
			f.emit()
			state := f.router.inspect()
			if mode == auditFixtureCallerCancel && (!f.handler.callerCanceledOwned || f.caller.Err() != context.Canceled || !f.handler.returned || f.handler.retained != nil) {
				t.Fatal("originating caller must cancel after NEW ownership, accept, and release references")
			}
			if f.handler.calls != 1 || len(f.legacy.records) != 0 || !state.fault || state.failures != 1 || state.active != 0 {
				t.Fatal("NEW fault must latch once without same-record fallback")
			}
			f.observe(2, 1, true)
			f.emit()
			if f.handler.calls != 1 || len(f.legacy.records) != 1 || f.router.inspect().failures != 1 {
				t.Fatal("next decision must be legacy without re-arm")
			}
		})
	}
}

func TestDecisionAuditRouter_ConcurrentOwnershipAndCapacity(t *testing.T) {
	// Deterministic reentrant scheduler models the second owner transaction during
	// a live K=1 slot without scheduler timing, goroutines or an unbounded wait.
	f := newAuditFixture(t, auditFixtureReenter)
	f.requireAdmission(t)
	f.observe(1, 1, true)
	f.emit()
	if f.handler.calls != 1 || len(f.legacy.records) != 1 || f.router.inspect().active != 0 {
		t.Fatal("occupied K=1 must route competing decision to legacy without waiting")
	}
	old := f.observe(2, 1, true)
	f.router.invalidateSource()
	f.router.finishRefresh(old, old.snapshot, nil)
	f.emit()
	if f.handler.calls != 1 || len(f.legacy.records) != 2 {
		t.Fatal("obsolete observation cannot resurrect NEW after invalidation")
	}
}

func TestDecisionAuditRouter_DisableDrainsCooperativeSlots(t *testing.T) {
	f := newAuditFixture(t, auditFixtureDrain)
	f.requireAdmission(t)
	f.observe(1, 1, true)
	h := f.clock.tick
	f.emit()
	if !f.handler.canceled || !f.handler.returned || f.handler.retained != nil || f.clock.tick > h+decisionAuditCompleteBudget || f.router.inspect().active != 0 {
		t.Fatal("h-based finite cancel/complete/reference-release bound violated")
	}
	_ = f.router.CloseNew(context.Background())
	f.emit()
	if len(f.legacy.records) != 1 {
		t.Fatal("after NEW close, one legacy owner required")
	}
}

func TestDecisionAuditRouter_CloseSeparatesLegacyDrain(t *testing.T) {
	f := newAuditFixture(t, auditFixtureAccept)
	f.requireAdmission(t)
	f.observe(1, 1, true)
	srv := f.router.server
	if err := srv.CleanupResources(context.Background()); err != nil {
		t.Fatal(err)
	}
	f.emit()
	if f.handler.calls != 0 || len(f.legacy.records) != 1 || !f.router.inspect().closed {
		t.Fatal("resource cleanup must close only NEW admission before HTTP drain")
	}
	_ = f.router.CloseNew(context.Background())
	f.emit()
	if len(f.legacy.records) != 2 {
		t.Fatal("idempotent NEW close must retain legacy")
	}
	if decisionAuditDrainTimeout+decisionAuditAbortGrace != 5*time.Second {
		t.Fatal("legacy close bound changed")
	}
}

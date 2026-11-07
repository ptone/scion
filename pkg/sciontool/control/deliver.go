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

package control

// The deliver-message route (design v2.22 r3, sha afc9fc37; cited below
// as "r3 §x, line n"). It runs one flush into the harness's tmux pane:
// optional interrupt keys, the readiness wait (paste only), a bracketed
// paste through a named buffer, then the submit keys. Every tmux argv is
// built by pkg/agent/tmuxdeliver from the validated request; nothing in
// the request names a command, a target or a flag.

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"sync"
	"sync/atomic"
	"time"

	"github.com/GoogleCloudPlatform/scion/pkg/agent/state"
	"github.com/GoogleCloudPlatform/scion/pkg/agent/tmuxdeliver"
	core "github.com/GoogleCloudPlatform/scion/pkg/conduit"
	"github.com/GoogleCloudPlatform/scion/pkg/sciontool/log"
)

// RouteDeliverMessage delivers one flush of message text or keys to the
// harness. It is idempotent by key (broker_id, delivery_id): a replay of
// a finished key answers the stored outcome and never acts again.
const RouteDeliverMessage = "deliver-message"

// Request bounds (r3 §1.2, lines 34-48).
const (
	// MaxDeliverText is the largest text accepted, in bytes (r3 §1.2
	// line 42, §1.6 line 90).
	MaxDeliverText = 512 << 10
	// MaxDeliverDeadline is the largest deadline_ms (r3 §1.2 line 44,
	// §2.2 line 149).
	MaxDeliverDeadline = 92 * time.Second
	// maxMessageIDs is the most message ids per flush (r3 §1.2 line 40).
	maxMessageIDs = 64
	// skewTolerance: a not_after earlier than receipt + deadline_ms
	// minus this is treated as clock skew and ignored (r3 §1.2 line 45,
	// §2.2 line 159). With the 92 s deadline the cut-off is receipt +
	// 62 s (r3 §6.3 line 351).
	skewTolerance = 30 * time.Second
	// flushBudget bounds the steps after the hard stop: the paste, the
	// submits and at most 3 extra submits of 2 s (r3 §2.2, the 8 s
	// paste budget and the 18 s gap to the hub's 110 s timeout, line
	// 164).
	flushBudget = 18 * time.Second
)

// Response outcomes and stages (r3 §1.3, lines 52-68).
const (
	outcomeDelivered      = "delivered"
	outcomePartial        = "partial"
	outcomeInProgress     = "in_progress"
	outcomeBusy           = "busy"
	outcomeExpired        = "expired_before_paste"
	outcomeFailed         = "failed"
	stageTarget           = "target"
	statusExpired         = http.StatusRequestTimeout
	statusTargetMissing   = http.StatusUnprocessableEntity
	statusFailedAfterStep = http.StatusInternalServerError
)

// idPattern is the delivery, broker and message id shape (r3 §1.2,
// lines 37-40), and the readiness nonce's.
var idPattern = regexp.MustCompile(`^[A-Za-z0-9-]{1,64}$`)

// DeliverOptions enable the deliver-message route.
type DeliverOptions struct {
	// Runner runs the tmux steps and the readiness probe. Required.
	Runner tmuxdeliver.Runner
	// Now and Sleep replace the clock in tests; nil uses the real clock.
	Now   func() time.Time
	Sleep func(ctx context.Context, d time.Duration) bool
}

// deliverer serves the route. One flush runs at a time.
type deliverer struct {
	runner tmuxdeliver.Runner
	now    func() time.Time
	sleep  func(ctx context.Context, d time.Duration) bool
	memo   *tmuxdeliver.MemoStore
	logf   deliverLogger

	mu      sync.Mutex
	running *idemKey // the flush in progress, nil when idle
	store   *idemStore
	bufSeq  uint64

	skewIgnored atomic.Int64 // not_after values ignored as clock skew
}

// deliverLogger is the route's logging; tests replace it to check that
// no text or key name reaches info.
type deliverLogger struct {
	info, warn, debug func(format string, args ...any)
}

var defaultDeliverLogger = deliverLogger{info: log.Info, warn: log.Warn, debug: log.Debug}

func newDeliverer(opts DeliverOptions) *deliverer {
	if opts.Runner == nil {
		panic("control: DeliverOptions.Runner is required")
	}
	d := &deliverer{
		runner: opts.Runner,
		now:    opts.Now,
		sleep:  opts.Sleep,
		memo:   &tmuxdeliver.MemoStore{},
		logf:   defaultDeliverLogger,
		store:  newIdemStore(idemMaxEntries, idemTTL),
	}
	if d.now == nil {
		d.now = time.Now
	}
	return d
}

// deliverRequest is the wire request (r3 §1.2). Pointers mark fields
// whose absence differs from their zero value.
type deliverRequest struct {
	V              *int           `json:"v"`
	DeliveryID     string         `json:"delivery_id"`
	BrokerID       string         `json:"broker_id"`
	MessageIDs     []string       `json:"message_ids"`
	Op             tmuxdeliver.Op `json:"op"`
	Text           *string        `json:"text"`
	InterruptKeys  []string       `json:"interrupt_keys"`
	DeadlineMS     *int64         `json:"deadline_ms"`
	NotAfterUnixMS *int64         `json:"not_after_unix_ms"`
	SubmitKeys     []string       `json:"submit_keys"`
	ExtraSubmits   *extraSubmits  `json:"extra_submits"`
	Ready          *readinessSpec `json:"ready"`
}

type extraSubmits struct {
	Count   int   `json:"count"`
	DelayMS int64 `json:"delay_ms"`
}

type readinessSpec struct {
	Strategy  string `json:"strategy"`
	SettleMS  int64  `json:"settle_ms"`
	TimeoutMS int64  `json:"timeout_ms"`
	Nonce     string `json:"nonce"`
}

type deliverResponse struct {
	Outcome  string `json:"outcome"`
	Stage    string `json:"stage,omitempty"`
	Replayed *bool  `json:"replayed,omitempty"`
}

type badRequestResponse struct {
	Error string `json:"error"`
	Field string `json:"field,omitempty"`
}

// fieldError names the request field that failed validation. Its text
// is the field name only, never a value.
type fieldError struct{ field string }

func (e *fieldError) Error() string { return "invalid field: " + e.field }

func bad(field string) error { return &fieldError{field: field} }

// decodeDeliverRequest decodes exactly one JSON object with no unknown
// fields (r3 §5.3 line 279).
func decodeDeliverRequest(body io.Reader) (*deliverRequest, error) {
	dec := json.NewDecoder(body)
	dec.DisallowUnknownFields()
	var req deliverRequest
	if err := dec.Decode(&req); err != nil {
		return nil, err
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("trailing data after the request object")
	}
	return &req, nil
}

// validate checks every field against the route's fixed bounds; it
// never adjusts a value (r3 §1.7, "No silent clamping").
func (req *deliverRequest) validate() error {
	if req.V == nil || *req.V != 1 {
		return bad("v")
	}
	if !idPattern.MatchString(req.DeliveryID) {
		return bad("delivery_id")
	}
	if !idPattern.MatchString(req.BrokerID) {
		return bad("broker_id")
	}
	if len(req.MessageIDs) < 1 || len(req.MessageIDs) > maxMessageIDs {
		return bad("message_ids")
	}
	for _, id := range req.MessageIDs {
		if !idPattern.MatchString(id) {
			return bad("message_ids")
		}
	}
	if !req.Op.Valid() {
		return bad("op")
	}
	pastes := req.Op == tmuxdeliver.OpPaste || req.Op == tmuxdeliver.OpInterruptPaste
	interrupts := req.Op == tmuxdeliver.OpInterruptPaste || req.Op == tmuxdeliver.OpInterruptSubmit
	switch {
	case pastes && (req.Text == nil || *req.Text == ""):
		return bad("text")
	case !pastes && req.Text != nil:
		return bad("text")
	case req.Text != nil && len(*req.Text) > MaxDeliverText:
		return bad("text")
	}
	if interrupts {
		if len(req.InterruptKeys) == 0 || tmuxdeliver.CheckKeys(tmuxdeliver.FieldInterruptKeys, req.InterruptKeys) != nil {
			return bad("interrupt_keys")
		}
	} else if req.InterruptKeys != nil {
		return bad("interrupt_keys")
	}
	if req.DeadlineMS == nil || *req.DeadlineMS <= 0 || *req.DeadlineMS > MaxDeliverDeadline.Milliseconds() {
		return bad("deadline_ms")
	}
	if req.NotAfterUnixMS != nil && *req.NotAfterUnixMS <= 0 {
		return bad("not_after_unix_ms")
	}
	if len(req.SubmitKeys) == 0 || tmuxdeliver.CheckKeys(tmuxdeliver.FieldSubmitKeys, req.SubmitKeys) != nil {
		return bad("submit_keys")
	}
	if es := req.ExtraSubmits; es != nil {
		if es.Count < 0 || es.Count > tmuxdeliver.MaxExtraSubmits {
			return bad("extra_submits.count")
		}
		if es.DelayMS < 0 || es.DelayMS > tmuxdeliver.MaxExtraSubmitDelay.Milliseconds() {
			return bad("extra_submits.delay_ms")
		}
	}
	if r := req.Ready; r != nil {
		// Readiness applies to op=paste only (r3 §1.2 line 48, §3.1).
		if req.Op != tmuxdeliver.OpPaste {
			return bad("ready")
		}
		switch r.Strategy {
		case state.ReadyStrategyHook, state.ReadyStrategyDelay:
		default:
			return bad("ready.strategy")
		}
		if r.SettleMS < 0 || r.SettleMS > tmuxdeliver.MaxReadySettle.Milliseconds() {
			return bad("ready.settle_ms")
		}
		if r.TimeoutMS < 0 || r.TimeoutMS > tmuxdeliver.MaxReadyTimeout.Milliseconds() {
			return bad("ready.timeout_ms")
		}
		if r.Nonce != "" && !idPattern.MatchString(r.Nonce) {
			return bad("ready.nonce")
		}
	}
	return nil
}

// hardStop is the instant after which no paste or first key may start:
// the earlier of receipt + deadline_ms and not_after_unix_ms (r3 §1.2
// line 45, §2.2 lines 158-160). The skew guard ignores a not_after that
// is already past at receipt, or earlier than receipt + deadline_ms -
// skewTolerance, and uses the receipt bound alone. skewed reports that.
func hardStop(receipt time.Time, deadline time.Duration, notAfterUnixMS *int64) (stop time.Time, skewed bool) {
	stop = receipt.Add(deadline)
	if notAfterUnixMS == nil {
		return stop, false
	}
	notAfter := time.UnixMilli(*notAfterUnixMS)
	if !notAfter.After(receipt) || notAfter.Before(stop.Add(-skewTolerance)) {
		return stop, true
	}
	if notAfter.Before(stop) {
		stop = notAfter
	}
	return stop, false
}

// errExpired: the hard stop passed before the first acting step.
var errExpired = errors.New("control: hard stop passed before the paste")

// gatedRunner refuses the first acting step (any step but the readiness
// probe) once the hard stop has passed, so no paste or first key starts
// late (r3 §1.2 line 45). Later steps of a flush that has started run.
type gatedRunner struct {
	inner tmuxdeliver.Runner
	now   func() time.Time
	stop  time.Time
	acted bool
}

func (g *gatedRunner) Run(ctx context.Context, argv []string, stdin io.Reader) (string, error) {
	if !g.acted {
		if !g.now().Before(g.stop) {
			return "", errExpired
		}
		g.acted = true
	}
	return g.inner.Run(ctx, argv, stdin)
}

// serve handles one deliver-message request.
func (d *deliverer) serve(w http.ResponseWriter, r *http.Request) {
	receipt := d.now()
	req, err := decodeDeliverRequest(http.MaxBytesReader(w, r.Body, core.MaxRPCBody))
	if err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			writeJSON(w, http.StatusRequestEntityTooLarge, badRequestResponse{Error: "request too large"})
			return
		}
		writeJSON(w, http.StatusBadRequest, badRequestResponse{Error: "malformed request"})
		return
	}
	if err := req.validate(); err != nil {
		var fe *fieldError
		errors.As(err, &fe)
		d.logf.info("deliver-message: refused, invalid field %s", fe.field)
		writeJSON(w, http.StatusBadRequest, badRequestResponse{Error: "invalid request", Field: fe.field})
		return
	}
	key := idemKey{brokerID: req.BrokerID, deliveryID: req.DeliveryID}

	d.mu.Lock()
	if out, ok := d.store.get(key, receipt); ok {
		d.mu.Unlock()
		d.logf.info("deliver-message: delivery_id=%s op=%s outcome=%s replayed=true", req.DeliveryID, req.Op, out.outcome)
		replayed := true
		writeJSON(w, out.status, deliverResponse{Outcome: out.outcome, Stage: out.stage, Replayed: &replayed})
		return
	}
	if d.running != nil {
		outcome := outcomeBusy
		if *d.running == key {
			outcome = outcomeInProgress
		}
		d.mu.Unlock()
		d.logf.info("deliver-message: delivery_id=%s op=%s outcome=%s", req.DeliveryID, req.Op, outcome)
		writeJSON(w, http.StatusConflict, deliverResponse{Outcome: outcome})
		return
	}
	d.running = &key
	d.bufSeq++
	seq := d.bufSeq
	d.mu.Unlock()

	res := d.flush(r.Context(), req, receipt, seq)

	d.mu.Lock()
	d.running = nil
	if res.store {
		d.store.put(key, res.idemOutcome, d.now())
	}
	d.mu.Unlock()

	textBytes := 0
	if req.Text != nil {
		textBytes = len(*req.Text)
	}
	d.logf.info("deliver-message: delivery_id=%s bytes=%d op=%s outcome=%s stage=%s replayed=false",
		req.DeliveryID, textBytes, req.Op, res.outcome, res.stage)
	resp := deliverResponse{Outcome: res.outcome, Stage: res.stage}
	if res.status == http.StatusOK {
		replayed := false
		resp.Replayed = &replayed
	}
	writeJSON(w, res.status, resp)
}

// flushResult is a flush's answer. store is set when any acting step
// was attempted, so a replay never acts twice; a flush that did nothing
// (expired, target missing) is not stored and may be retried.
type flushResult struct {
	idemOutcome
	store bool
}

func (d *deliverer) flush(ctx context.Context, req *deliverRequest, receipt time.Time, seq uint64) flushResult {
	stop, skewed := hardStop(receipt, time.Duration(*req.DeadlineMS)*time.Millisecond, req.NotAfterUnixMS)
	if skewed {
		// Counts only: no times or ids at warn (r3 §2.2 line 159).
		n := d.skewIgnored.Add(1)
		d.logf.warn("deliver-message: not_after ignored as clock skew (%d so far)", n)
		d.logf.debug("deliver-message: observed skew %dms (not_after minus the receipt bound)",
			time.UnixMilli(*req.NotAfterUnixMS).Sub(receipt.Add(time.Duration(*req.DeadlineMS)*time.Millisecond)).Milliseconds())
	}
	gate := &gatedRunner{inner: d.runner, now: d.now, stop: stop}

	// The tmux target must exist before anything is attempted (r3 §1.3,
	// 422 stage target). The check is not an acting step.
	if _, err := d.runner.Run(ctx, targetCheckArgv(), nil); err != nil {
		return flushResult{idemOutcome: idemOutcome{status: statusTargetMissing, outcome: outcomeFailed, stage: stageTarget}}
	}

	if req.Op == tmuxdeliver.OpPaste && req.Ready != nil {
		if gone := d.waitReady(ctx, req.Ready, receipt, stop); gone {
			return flushResult{idemOutcome: idemOutcome{status: statusTargetMissing, outcome: outcomeFailed, stage: stageTarget}}
		}
	}

	treq := tmuxdeliver.Request{
		Op:            req.Op,
		InterruptKeys: req.InterruptKeys,
		SubmitKeys:    req.SubmitKeys,
	}
	if req.Text != nil {
		treq.Text = *req.Text
		name, err := tmuxdeliver.BufferName(req.DeliveryID, seq)
		if err != nil { // unreachable: delivery_id is validated
			return flushResult{idemOutcome: idemOutcome{status: http.StatusBadRequest, outcome: outcomeFailed}}
		}
		treq.BufferName = name
	}
	if es := req.ExtraSubmits; es != nil {
		treq.ExtraSubmits = es.Count
		treq.ExtraSubmitDelay = time.Duration(es.DelayMS) * time.Millisecond
	}
	if err := tmuxdeliver.CheckRoute(treq); err != nil { // repeats validate's bounds
		return flushResult{idemOutcome: idemOutcome{status: http.StatusBadRequest, outcome: outcomeFailed}}
	}

	// The caller went away during the wait: nothing was done, so nothing
	// is stored.
	if ctx.Err() != nil {
		return flushResult{idemOutcome: idemOutcome{status: http.StatusServiceUnavailable, outcome: outcomeFailed}}
	}

	// The flush is bounded: the hard stop plus the post-start budget. The
	// bound is a duration from now, so an injected clock stays consistent.
	fctx, cancel := context.WithTimeout(ctx, stop.Sub(d.now())+flushBudget)
	defer cancel()
	err := tmuxdeliver.Deliver(fctx, gate, treq)
	var partial *tmuxdeliver.PartialError
	switch {
	case err == nil:
		return flushResult{idemOutcome: idemOutcome{status: http.StatusOK, outcome: outcomeDelivered}, store: true}
	case errors.As(err, &partial):
		d.logf.debug("deliver-message: partial at %s: %v", partial.Stage, partial.Err)
		return flushResult{idemOutcome: idemOutcome{status: http.StatusOK, outcome: outcomePartial, stage: partial.Stage}, store: true}
	case errors.Is(err, errExpired) && !gate.acted:
		return flushResult{idemOutcome: idemOutcome{status: statusExpired, outcome: outcomeExpired}}
	case errors.Is(err, tmuxdeliver.ErrEmptyAfterSanitize):
		return flushResult{idemOutcome: idemOutcome{status: http.StatusBadRequest, outcome: outcomeFailed, stage: "text"}}
	default:
		// A step failed before the paste completed. Errors carry the
		// verb only (runnerError), never text.
		d.logf.debug("deliver-message: flush failed: %v", err)
		return flushResult{idemOutcome: idemOutcome{status: statusFailedAfterStep, outcome: outcomeFailed}, store: gate.acted}
	}
}

// waitReady runs the readiness wait for op=paste, bounded by the
// request's settle and timeout and by the hard stop (r3 §3.1). It
// reports whether the target went away. A wait that times out is not a
// failure: the flush pastes anyway, unless the hard stop has passed,
// which the gated runner then refuses.
func (d *deliverer) waitReady(ctx context.Context, rs *readinessSpec, receipt, stop time.Time) (gone bool) {
	maxDeadline := tmuxdeliver.MaxReadyTimeout
	if rs.TimeoutMS > 0 {
		maxDeadline = time.Duration(rs.TimeoutMS) * time.Millisecond
	}
	if untilStop := stop.Sub(receipt); untilStop < maxDeadline {
		maxDeadline = untilStop
	}
	if maxDeadline <= 0 {
		return false
	}
	t := tmuxdeliver.Timings{
		Deadline: maxDeadline,
		Settle:   time.Duration(rs.SettleMS) * time.Millisecond,
	}
	wctx, cancel := context.WithTimeout(ctx, stop.Sub(d.now()))
	defer cancel()
	res := tmuxdeliver.Wait(wctx, tmuxdeliver.WaitConfig{
		Runner:      d.runner,
		Timings:     t,
		Memo:        d.memo,
		Key:         tmuxdeliver.Target,
		MaxDeadline: maxDeadline,
		MaxSettle:   tmuxdeliver.MaxReadySettle,
		Gone: func(ctx context.Context, _ error) bool {
			_, err := d.runner.Run(ctx, targetCheckArgv(), nil)
			return err != nil
		},
		Now:   d.now,
		Sleep: d.sleep,
	})
	d.logf.debug("deliver-message: readiness outcome=%d mode=%s waited=%s", res.Outcome, res.Mode, res.Waited)
	return res.Outcome == tmuxdeliver.WaitTargetGone
}

// targetCheckArgv checks that the harness pane exists. It is fixed.
func targetCheckArgv() []string {
	return []string{"tmux", "has-session", "-t", tmuxdeliver.Target}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

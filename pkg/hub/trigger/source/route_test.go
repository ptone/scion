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

package source_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/GoogleCloudPlatform/scion/pkg/hub/trigger/source"
)

type fakeVerifier struct {
	deliv source.Delivery
	err   error
	calls int
}

func (f *fakeVerifier) Verify(_ *http.Request, _ []byte, _ source.SecretSource) (source.Delivery, error) {
	f.calls++
	return f.deliv, f.err
}

func (f *fakeVerifier) DedupKey(d source.Delivery, body []byte) string {
	sum := sha256.Sum256(body)
	return d.ID + ":" + hex.EncodeToString(sum[:])
}

type fakeResolver struct {
	lifecycleHandled bool
	lifecycleErr     error
	lifecycleCalls   int

	inst        source.Installation
	instErr     error
	instCalls   int
	instPayload any

	targets        []source.Target
	targetsErr     error
	targetsCalls   int
	targetsPayload any
}

func (f *fakeResolver) Installation(_ context.Context, _ source.Delivery, payload any) (source.Installation, error) {
	f.instCalls++
	f.instPayload = payload
	return f.inst, f.instErr
}

func (f *fakeResolver) Targets(_ context.Context, _ source.Installation, payload any) ([]source.Target, error) {
	f.targetsCalls++
	f.targetsPayload = payload
	return f.targets, f.targetsErr
}

func (f *fakeResolver) OnLifecycle(_ context.Context, _ source.Delivery, _ any) (bool, error) {
	f.lifecycleCalls++
	return f.lifecycleHandled, f.lifecycleErr
}

type fakeNormalizer struct {
	ev    source.Event
	err   error
	calls int

	finalizeEv    *source.Event
	finalizeErr   error
	finalizeCalls int
}

func (f *fakeNormalizer) Normalize(_ context.Context, _ source.Delivery, _ any, _ source.Target) (source.Event, error) {
	f.calls++
	return f.ev, f.err
}

func (f *fakeNormalizer) Finalize(_ context.Context, _ source.Delivery, _ any, ev source.Event) (source.Event, error) {
	f.finalizeCalls++
	if f.finalizeErr != nil {
		return ev, f.finalizeErr
	}
	if f.finalizeEv != nil {
		return *f.finalizeEv, nil
	}
	return ev, nil
}

type fakeActor struct {
	isSelf bool
	calls  int
	seen   source.Principal
}

func (f *fakeActor) IsSelf(_ context.Context, sender source.Principal) bool {
	f.calls++
	f.seen = sender
	return f.isSelf
}

type fakeSink struct {
	results    []source.TargetResult
	err        error
	calls      int
	gotEvent   source.Event
	gotBodySHA string
	gotTargets []source.Target
}

func (f *fakeSink) EnqueueForTargets(_ context.Context, ev source.Event, bodySHA256 string, targets []source.Target) ([]source.TargetResult, error) {
	f.calls++
	f.gotEvent = ev
	f.gotBodySHA = bodySHA256
	f.gotTargets = append([]source.Target(nil), targets...)
	return f.results, f.err
}

type fakeLimiter struct {
	allow bool
	calls int
}

func (f *fakeLimiter) Allow(_ time.Time) bool {
	f.calls++
	return f.allow
}

type fakeAudit struct {
	receipts       []source.ReceiptAuditRecord
	targetMismatch int
	selfIgnored    int
}

func (f *fakeAudit) RecordReceipt(_ context.Context, rec source.ReceiptAuditRecord) {
	f.receipts = append(f.receipts, rec)
}

func (f *fakeAudit) RecordTargetMismatch(_ context.Context, _ time.Time) {
	f.targetMismatch++
}

func (f *fakeAudit) RecordSelfIgnored(_ context.Context) {
	f.selfIgnored++
}

type fakeMetrics struct {
	deliveries []string // "kind:result"
}

func (f *fakeMetrics) RecordDelivery(kind, result string) {
	f.deliveries = append(f.deliveries, kind+":"+result)
}

func newTestRouteHarness() (source.RouteDeps, *fakeVerifier, *fakeResolver, *fakeNormalizer, *fakeActor, *fakeSink, *fakeLimiter, *fakeAudit, *fakeMetrics) {
	now := time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)
	v := &fakeVerifier{
		deliv: source.Delivery{
			ID:         "deliv-1",
			Event:      "issue_comment",
			Headers:    map[string]string{"X-Test": "1"},
			ReceivedAt: now,
		},
	}
	res := &fakeResolver{
		inst: source.Installation{ID: "42", Account: "acme", Active: true},
		targets: []source.Target{
			{ProjectID: "proj-1", InstallationID: "42", Resource: "acme/widgets", RoutingStatus: "none"},
		},
	}
	norm := &fakeNormalizer{
		ev: source.Event{
			Handled:    true,
			SourceKind: "fake",
			SourceID:   "42",
			DeliveryID: "deliv-1",
			Type:       "fake.issue_comment.created",
			Action:     "created",
			Resource:   "acme/widgets",
			Subject:    "fake:acme/widgets#1",
			Sender:     source.Principal{Login: "alice", Type: "User"},
			Payload:    []byte(`{"ok":true}`),
		},
	}
	actor := &fakeActor{}
	sink := &fakeSink{
		results: []source.TargetResult{
			{ProjectID: "proj-1", EventID: "ev-1", Status: source.TargetStatusQueued},
		},
	}
	lim := &fakeLimiter{allow: true}
	aud := &fakeAudit{}
	met := &fakeMetrics{}

	deps := source.RouteDeps{
		Kind:       "fake",
		Verifier:   v,
		Resolver:   res,
		Normalizer: norm,
		Actor:      actor,
		Sink:       sink,
		Limiter:    lim,
		Audit:      aud,
		Metrics:    met,
		Now:        func() time.Time { return now },
		PingEvent:  "ping",
	}
	return deps, v, res, norm, actor, sink, lim, aud, met
}

func postTestBody(deps source.RouteDeps, body []byte) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/api/v1/webhooks/fake", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	deps.ServeHTTP(rec, req)
	return rec
}

func TestRouteDeps_MethodNotAllowed(t *testing.T) {
	deps, v, _, _, _, _, _, _, _ := newTestRouteHarness()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/webhooks/fake", nil)
	rec := httptest.NewRecorder()
	deps.ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
	if got := rec.Header().Get("Allow"); got != http.MethodPost {
		t.Errorf("Allow = %q, want POST", got)
	}
	if v.calls != 0 {
		t.Errorf("verifier called %d times on GET, want 0", v.calls)
	}
}

func TestRouteDeps_VerifyFailures(t *testing.T) {
	cases := []struct {
		name               string
		verifyErr          error
		wantStatus         int
		wantBodySubstr     string
		wantTargetMismatch int
		wantSecretHook     int
		wantMetric         string
	}{
		{
			name:           "secret not configured",
			verifyErr:      source.ErrSecretNotConfigured,
			wantStatus:     http.StatusServiceUnavailable,
			wantBodySubstr: "webhook is not configured",
			wantSecretHook: 1,
			wantMetric:     "fake:error",
		},
		{
			name:           "invalid signature",
			verifyErr:      source.ErrInvalidSignature,
			wantStatus:     http.StatusUnauthorized,
			wantBodySubstr: "invalid webhook signature",
			wantMetric:     "fake:error",
		},
		{
			name:               "target mismatch records audit",
			verifyErr:          source.ErrTargetMismatch,
			wantStatus:         http.StatusBadRequest,
			wantBodySubstr:     "wrong target",
			wantTargetMismatch: 1,
			wantMetric:         "fake:target_mismatch",
		},
		{
			name:           "invalid headers",
			verifyErr:      source.ErrInvalidHeaders,
			wantStatus:     http.StatusBadRequest,
			wantBodySubstr: "invalid event header",
			wantMetric:     "fake:error",
		},
		{
			name:           "invalid delivery id",
			verifyErr:      source.ErrInvalidDeliveryID,
			wantStatus:     http.StatusBadRequest,
			wantBodySubstr: "invalid delivery id",
			wantMetric:     "fake:error",
		},
		{
			name:           "verifier ErrSkip returns 202 and audits",
			verifyErr:      source.ErrSkip{Reason: "filtered"},
			wantStatus:     http.StatusAccepted,
			wantBodySubstr: `"reason":"filtered"`,
			wantMetric:     "fake:skipped",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			deps, v, res, norm, _, sink, _, aud, met := newTestRouteHarness()
			v.err = tc.verifyErr
			secretHookCalls := 0
			deps.OnSecretNotConfigured = func() { secretHookCalls++ }

			rec := postTestBody(deps, []byte(`{}`))
			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d: %s", rec.Code, tc.wantStatus, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), tc.wantBodySubstr) {
				t.Errorf("body = %q, want substring %q", rec.Body.String(), tc.wantBodySubstr)
			}
			if aud.targetMismatch != tc.wantTargetMismatch {
				t.Errorf("targetMismatch = %d, want %d", aud.targetMismatch, tc.wantTargetMismatch)
			}
			if secretHookCalls != tc.wantSecretHook {
				t.Errorf("secretHookCalls = %d, want %d", secretHookCalls, tc.wantSecretHook)
			}
			var skipErr source.ErrSkip
			if errors.As(tc.verifyErr, &skipErr) {
				if len(aud.receipts) != 1 || aud.receipts[0].Result != source.RouteStatusSkipped || aud.receipts[0].Delivery != "deliv-1" {
					t.Errorf("verifier ErrSkip must emit skipped receipt audit with delivery, got %+v", aud.receipts)
				}
			}
			if res.lifecycleCalls != 0 || norm.calls != 0 || sink.calls != 0 {
				t.Errorf("downstream stages should not run after verify failure")
			}
			if len(met.deliveries) != 1 || met.deliveries[0] != tc.wantMetric {
				t.Errorf("metrics = %v, want [%s]", met.deliveries, tc.wantMetric)
			}
		})
	}
}

func TestRouteDeps_WriteErrorHookAndUnhandledEventAndLogSemantics(t *testing.T) {
	t.Run("injectable WriteError is invoked on error exits", func(t *testing.T) {
		deps, _, _, _, _, _, lim, _, _ := newTestRouteHarness()
		lim.allow = false
		var gotStatus int
		var gotCode, gotMsg string
		deps.WriteError = func(w http.ResponseWriter, status int, code, message string) {
			gotStatus, gotCode, gotMsg = status, code, message
			w.WriteHeader(status)
		}

		rec := postTestBody(deps, []byte(`{}`))
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("status = %d, want 503", rec.Code)
		}
		if gotStatus != http.StatusServiceUnavailable || gotCode != "unavailable" || gotMsg != "rate limit exceeded" {
			t.Errorf("WriteError got (%d, %q, %q), want (503, unavailable, rate limit exceeded)", gotStatus, gotCode, gotMsg)
		}
	})

	t.Run("normalizer Handled=false returns 200 ignored without resolving or enqueuing", func(t *testing.T) {
		deps, _, res, norm, actor, sink, _, aud, met := newTestRouteHarness()
		norm.ev.Handled = false

		rec := postTestBody(deps, []byte(`{}`))
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"status":"ignored"`) {
			t.Fatalf("got %d %s, want 200 ignored", rec.Code, rec.Body.String())
		}
		if res.instCalls != 0 || actor.calls != 0 || res.targetsCalls != 0 || sink.calls != 0 || len(aud.receipts) != 0 {
			t.Errorf("unhandled event must stop before Installation/Actor/Targets/Sink")
		}
		if len(met.deliveries) != 1 || met.deliveries[0] != "fake:unsupported" {
			t.Errorf("metrics = %v, want [fake:unsupported]", met.deliveries)
		}
	})

	t.Run("RouteDeps.Kind overrides Event.SourceKind and Resolver receives normalized Event", func(t *testing.T) {
		deps, _, res, norm, _, sink, _, _, _ := newTestRouteHarness()
		deps.Kind = "canonical-kind"
		norm.ev.SourceKind = "different-kind"

		rec := postTestBody(deps, []byte(`{}`))
		if rec.Code != http.StatusAccepted {
			t.Fatalf("status = %d, want 202: %s", rec.Code, rec.Body.String())
		}
		if sink.gotEvent.SourceKind != "canonical-kind" {
			t.Errorf("sink Event.SourceKind = %q, want canonical-kind", sink.gotEvent.SourceKind)
		}
		if _, ok := res.instPayload.(source.Event); !ok {
			t.Errorf("Resolver.Installation received %T, want source.Event", res.instPayload)
		}
		if _, ok := res.targetsPayload.(source.Event); !ok {
			t.Errorf("Resolver.Targets received %T, want source.Event", res.targetsPayload)
		}
	})

	t.Run("received log fires before target rejection with RawID casing and never on invalid signature", func(t *testing.T) {
		var buf bytes.Buffer
		prev := slog.Default()
		slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
		t.Cleanup(func() { slog.SetDefault(prev) })

		deps, v, _, _, _, _, _, _, _ := newTestRouteHarness()
		v.deliv.ID = "aaaa-bbbb"
		v.deliv.RawID = "AAAA-BBBB"
		v.err = source.ErrInvalidSignature

		rec := postTestBody(deps, []byte(`{}`))
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401", rec.Code)
		}
		if strings.Contains(buf.String(), "Source webhook received") {
			t.Errorf("must not log received on invalid signature: %s", buf.String())
		}

		buf.Reset()
		v.err = source.ErrTargetMismatch
		rec = postTestBody(deps, []byte(`{}`))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400", rec.Code)
		}
		if !strings.Contains(buf.String(), "Source webhook received") || !strings.Contains(buf.String(), "delivery_id=AAAA-BBBB") {
			t.Errorf("expected received log with original RawID casing on target mismatch, got: %s", buf.String())
		}
	})
}

func TestRouteDeps_LifecycleHandled(t *testing.T) {
	t.Run("lifecycle ok", func(t *testing.T) {
		deps, _, res, norm, _, sink, _, aud, _ := newTestRouteHarness()
		res.lifecycleHandled = true

		rec := postTestBody(deps, []byte(`{"action":"created"}`))
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), `"status":"ok"`) {
			t.Errorf("body = %q, want status ok", rec.Body.String())
		}
		if norm.calls != 0 || sink.calls != 0 || len(aud.receipts) != 0 {
			t.Errorf("lifecycle delivery must not normalize, enqueue, or emit receipt audit")
		}
	})

	t.Run("ping returns pong", func(t *testing.T) {
		deps, v, res, _, _, sink, _, _, _ := newTestRouteHarness()
		v.deliv.Event = "ping"
		res.lifecycleHandled = true

		rec := postTestBody(deps, []byte(`{"zen":"ok"}`))
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), `"status":"pong"`) {
			t.Errorf("body = %q, want status pong", rec.Body.String())
		}
		if sink.calls != 0 {
			t.Errorf("ping must not call sink")
		}
	})

	t.Run("lifecycle error returns 400", func(t *testing.T) {
		deps, _, res, _, _, sink, _, _, _ := newTestRouteHarness()
		res.lifecycleHandled = true
		res.lifecycleErr = errors.New("bad json")

		rec := postTestBody(deps, []byte(`{bad`))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body.String())
		}
		if sink.calls != 0 {
			t.Errorf("failed lifecycle must not call sink")
		}
	})
}

func TestRouteDeps_ContentDisabledUnsupportedAndRateLimited(t *testing.T) {
	t.Run("content disabled returns 200 ignored", func(t *testing.T) {
		deps, _, _, norm, _, sink, lim, _, _ := newTestRouteHarness()
		deps.ContentEnabled = func(context.Context) bool { return false }

		rec := postTestBody(deps, []byte(`{}`))
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"status":"ignored"`) {
			t.Fatalf("got %d %s, want 200 ignored", rec.Code, rec.Body.String())
		}
		if lim.calls != 0 || norm.calls != 0 || sink.calls != 0 {
			t.Errorf("disabled content must not hit limiter/normalizer/sink")
		}
	})

	t.Run("unsupported event returns 200 ignored before limiter", func(t *testing.T) {
		deps, v, _, norm, _, sink, lim, _, met := newTestRouteHarness()
		v.deliv.Unsupported = true

		rec := postTestBody(deps, []byte(`{}`))
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"status":"ignored"`) {
			t.Fatalf("got %d %s, want 200 ignored", rec.Code, rec.Body.String())
		}
		if lim.calls != 0 || norm.calls != 0 || sink.calls != 0 {
			t.Errorf("unsupported event must not consume limiter tokens or call normalizer/sink")
		}
		if len(met.deliveries) != 1 || met.deliveries[0] != "fake:unsupported" {
			t.Errorf("metrics = %v, want [fake:unsupported]", met.deliveries)
		}
	})

	t.Run("rate limited returns 503 with Retry-After", func(t *testing.T) {
		deps, _, _, norm, _, sink, lim, _, met := newTestRouteHarness()
		lim.allow = false

		rec := postTestBody(deps, []byte(`{}`))
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("status = %d, want 503", rec.Code)
		}
		if rec.Header().Get("Retry-After") != "5" {
			t.Errorf("Retry-After = %q, want 5", rec.Header().Get("Retry-After"))
		}
		if norm.calls != 0 || sink.calls != 0 {
			t.Errorf("rate limited request must not normalize or enqueue")
		}
		if len(met.deliveries) != 1 || met.deliveries[0] != "fake:rate_limited" {
			t.Errorf("metrics = %v, want [fake:rate_limited]", met.deliveries)
		}
	})
}

func TestRouteDeps_ErrSkipAndNormalizeErrors(t *testing.T) {
	t.Run("held ErrSkip after targets emits 202 skipped with per-target reasons and audit", func(t *testing.T) {
		deps, _, res, norm, _, sink, _, aud, met := newTestRouteHarness()
		res.targets = []source.Target{
			{ProjectID: "proj-a", InstallationID: "42", Resource: "acme/widgets", RoutingStatus: "ok", BypassedProjectIDs: []string{"proj-c"}},
			{ProjectID: "proj-b", InstallationID: "42", Resource: "acme/widgets", RoutingStatus: "ok", BypassedProjectIDs: []string{"proj-c"}},
		}
		norm.err = source.ErrSkip{Reason: "attributes_ceiling"}

		rec := postTestBody(deps, []byte(`{"big":true}`))
		if rec.Code != http.StatusAccepted {
			t.Fatalf("status = %d, want 202: %s", rec.Code, rec.Body.String())
		}
		var resp source.AcceptedResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if resp.Status != source.RouteStatusSkipped || len(resp.SkippedProjects) != 2 {
			t.Fatalf("unexpected response: %+v", resp)
		}
		if resp.SkippedProjects[0].Reason != "attributes_ceiling" || resp.SkippedProjects[1].Reason != "attributes_ceiling" {
			t.Errorf("unexpected skipped reasons: %+v", resp.SkippedProjects)
		}
		if sink.calls != 0 {
			t.Errorf("ErrSkip must not call sink")
		}
		if len(aud.receipts) != 1 || aud.receipts[0].Result != source.RouteStatusSkipped || aud.receipts[0].Routing != source.RoutingRoutedToPrimary {
			t.Errorf("unexpected audit receipts: %+v", aud.receipts)
		}
		if len(met.deliveries) != 1 || met.deliveries[0] != "fake:skipped" {
			t.Errorf("metrics = %v, want [fake:skipped]", met.deliveries)
		}
	})

	t.Run("envelope normalize error fails immediately with 400", func(t *testing.T) {
		deps, _, res, norm, _, sink, _, aud, _ := newTestRouteHarness()
		norm.ev = source.Event{}
		norm.err = errors.New("malformed json")

		rec := postTestBody(deps, []byte(`{bad`))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400", rec.Code)
		}
		if res.instCalls != 0 || sink.calls != 0 || len(aud.receipts) != 0 {
			t.Errorf("envelope error must stop before installation lookup")
		}
	})
}

func TestRouteDeps_OwnActorAndNoTargetsAndUnknownInstallation(t *testing.T) {
	t.Run("own-actor ignored with 202 and no receipt audit", func(t *testing.T) {
		deps, _, res, _, actor, sink, _, aud, met := newTestRouteHarness()
		actor.isSelf = true

		rec := postTestBody(deps, []byte(`{}`))
		if rec.Code != http.StatusAccepted {
			t.Fatalf("status = %d, want 202: %s", rec.Code, rec.Body.String())
		}
		var resp source.AcceptedResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if resp.Status != source.RouteStatusIgnored || resp.Reason != "self" {
			t.Fatalf("unexpected response: %+v", resp)
		}
		if aud.selfIgnored != 1 || len(aud.receipts) != 0 {
			t.Errorf("expected selfIgnored=1 and 0 receipts, got selfIgnored=%d receipts=%d", aud.selfIgnored, len(aud.receipts))
		}
		if res.targetsCalls != 0 || sink.calls != 0 {
			t.Errorf("own-actor must stop before Targets/Sink")
		}
		if len(met.deliveries) != 1 || met.deliveries[0] != "fake:self_ignored" {
			t.Errorf("metrics = %v, want [fake:self_ignored]", met.deliveries)
		}
	})

	t.Run("no targets returns 202 no_project with receipt audit", func(t *testing.T) {
		deps, _, res, _, _, sink, _, aud, _ := newTestRouteHarness()
		res.targets = nil

		rec := postTestBody(deps, []byte(`{}`))
		if rec.Code != http.StatusAccepted {
			t.Fatalf("status = %d, want 202: %s", rec.Code, rec.Body.String())
		}
		var resp source.AcceptedResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if resp.Status != source.RouteStatusNoProject {
			t.Fatalf("status = %q, want no_project", resp.Status)
		}
		if len(aud.receipts) != 1 || aud.receipts[0].Result != source.RouteStatusNoProject {
			t.Errorf("unexpected receipts: %+v", aud.receipts)
		}
		if sink.calls != 0 {
			t.Errorf("no targets must not call sink")
		}
	})

	t.Run("unknown installation returns 202 unknown_installation with audit", func(t *testing.T) {
		deps, _, res, _, actor, sink, _, aud, _ := newTestRouteHarness()
		res.instErr = source.ErrInstallationNotFound

		rec := postTestBody(deps, []byte(`{}`))
		if rec.Code != http.StatusAccepted {
			t.Fatalf("status = %d, want 202: %s", rec.Code, rec.Body.String())
		}
		var resp source.AcceptedResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if resp.Status != source.RouteStatusUnknownInstallation {
			t.Fatalf("status = %q, want unknown_installation", resp.Status)
		}
		if len(aud.receipts) != 1 || aud.receipts[0].Result != source.RouteStatusUnknownInstallation {
			t.Errorf("unexpected receipts: %+v", aud.receipts)
		}
		if actor.calls != 0 || sink.calls != 0 {
			t.Errorf("unknown installation must stop before Actor/Sink")
		}
	})
}

func TestRouteDeps_SinkErrorAndPartialAuditAndHappyPath(t *testing.T) {
	t.Run("sink error with no committed events returns 500 and no receipt audit", func(t *testing.T) {
		deps, _, _, _, _, sink, _, aud, met := newTestRouteHarness()
		sink.results = nil
		sink.err = errors.New("db down")

		rec := postTestBody(deps, []byte(`{"hello":"world"}`))
		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("status = %d, want 500: %s", rec.Code, rec.Body.String())
		}
		if len(aud.receipts) != 0 {
			t.Errorf("expected 0 receipts when nothing committed, got %+v", aud.receipts)
		}
		if len(met.deliveries) != 1 || met.deliveries[0] != "fake:error" {
			t.Errorf("metrics = %v, want [fake:error]", met.deliveries)
		}
	})

	t.Run("sink partial error after queued target returns 500 and emits error receipt audit", func(t *testing.T) {
		deps, _, _, _, _, sink, _, aud, _ := newTestRouteHarness()
		sink.results = []source.TargetResult{
			{ProjectID: "proj-1", EventID: "ev-1", Status: source.TargetStatusQueued},
		}
		sink.err = errors.New("second tx failed")

		rec := postTestBody(deps, []byte(`{"hello":"world"}`))
		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("status = %d, want 500", rec.Code)
		}
		if len(aud.receipts) != 1 || aud.receipts[0].Result != source.RouteStatusError || len(aud.receipts[0].Events) != 1 {
			t.Errorf("expected error receipt audit with partial event, got %+v", aud.receipts)
		}
	})

	t.Run("happy path propagates body SHA-256, headers, finalize, and receipt audit", func(t *testing.T) {
		deps, _, _, norm, _, sink, _, aud, met := newTestRouteHarness()
		finalized := norm.ev
		finalized.Attributes = map[string]string{"finalized": "yes"}
		norm.finalizeEv = &finalized

		body := []byte(`{"hello":"world"}`)
		wantSum := sha256.Sum256(body)
		wantSHA := hex.EncodeToString(wantSum[:])

		rec := postTestBody(deps, body)
		if rec.Code != http.StatusAccepted {
			t.Fatalf("status = %d, want 202: %s", rec.Code, rec.Body.String())
		}
		var resp source.AcceptedResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if resp.Status != source.RouteStatusQueued || len(resp.Events) != 1 || resp.Events[0].EventID != "ev-1" {
			t.Fatalf("unexpected response: %+v", resp)
		}
		if norm.finalizeCalls != 1 {
			t.Errorf("finalizeCalls = %d, want 1", norm.finalizeCalls)
		}
		if sink.gotBodySHA != wantSHA {
			t.Errorf("bodySHA = %q, want %q", sink.gotBodySHA, wantSHA)
		}
		if sink.gotEvent.Headers["X-Test"] != "1" {
			t.Errorf("headers not propagated to sink: %+v", sink.gotEvent.Headers)
		}
		if sink.gotEvent.Attributes["finalized"] != "yes" {
			t.Errorf("finalized attributes not passed to sink: %+v", sink.gotEvent.Attributes)
		}
		if len(aud.receipts) != 1 || aud.receipts[0].Result != source.RouteStatusQueued || aud.receipts[0].Kind != "fake" {
			t.Errorf("unexpected receipt audit: %+v", aud.receipts)
		}
		if len(met.deliveries) != 1 || met.deliveries[0] != "fake:accepted" {
			t.Errorf("metrics = %v, want [fake:accepted]", met.deliveries)
		}
	})
}

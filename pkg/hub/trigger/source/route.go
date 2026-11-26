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

package source

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"time"
)

// DefaultMaxBodyBytes is the default maximum webhook request body size read by
// RouteDeps.ServeHTTP when MaxBody is zero or negative (10 MiB).
const DefaultMaxBodyBytes int64 = 10 * 1024 * 1024

// Provider-neutral delivery and per-target outcome status values.
const (
	TargetStatusQueued    = "queued"
	TargetStatusDuplicate = "duplicate"
	TargetStatusSkipped   = "skipped"

	RouteStatusQueued              = "queued"
	RouteStatusDuplicate           = "duplicate"
	RouteStatusSkipped             = "skipped"
	RouteStatusIgnored             = "ignored"
	RouteStatusUnknownInstallation = "unknown_installation"
	RouteStatusNoProject           = "no_project"
	RouteStatusError               = "error"

	RoutingStatusNone         = "none"
	RoutingStatusOK           = "ok"
	RoutingStatusStalePrimary = "stale_primary"

	RoutingRoutedToPrimary = "routed_to_primary"
	RoutingStalePrimary    = "stale_primary"
)

// Sentinel errors recognized by RouteDeps.ServeHTTP when returned (directly or
// wrapped) by WireVerifier, InstallationResolver, or EventSink implementations.
var (
	ErrSecretNotConfigured  = errors.New("source: webhook secret is not configured")
	ErrInvalidSignature     = errors.New("source: invalid webhook signature")
	ErrTargetMismatch       = errors.New("source: webhook target mismatch")
	ErrInvalidHeaders       = errors.New("source: invalid webhook headers")
	ErrInvalidDeliveryID    = errors.New("source: invalid delivery id")
	ErrInstallationNotFound = errors.New("source: installation not found")
	ErrUnavailable          = errors.New("source: ingress backend unavailable")
)

// ErrSkip lets a Verifier or Normalizer end a delivery with a receipt audit
// (result "skipped") and an HTTP 202 response. When returned after targets are
// resolved (for example, the GitHub attributes-ceiling case), Reason is
// recorded on each entry in skipped_projects; when returned before target
// resolution, Reason is recorded on the top-level response.
type ErrSkip struct {
	Reason string
}

// Error implements error.
func (e ErrSkip) Error() string {
	if e.Reason == "" {
		return "source: delivery skipped"
	}
	return "source: delivery skipped: " + e.Reason
}

// TargetResult reports the per-target outcome produced by EventSink.EnqueueForTargets.
type TargetResult struct {
	ProjectID string
	EventID   string
	Status    string // TargetStatusQueued, TargetStatusDuplicate, or TargetStatusSkipped
	Reason    string // populated when Status == TargetStatusSkipped
}

// AcceptedEvent is one per-project queued or duplicate entry in AcceptedResponse.
type AcceptedEvent struct {
	ProjectID string `json:"project_id"`
	EventID   string `json:"event_id"`
	Status    string `json:"status"`
}

// SkippedTarget is one per-project skipped entry in AcceptedResponse.
type SkippedTarget struct {
	ProjectID string `json:"project_id"`
	Reason    string `json:"reason"`
}

// AcceptedResponse is the HTTP 202 response payload returned for verified
// content-event deliveries.
type AcceptedResponse struct {
	Status          string          `json:"status"`
	Reason          string          `json:"reason,omitempty"`
	Delivery        string          `json:"delivery,omitempty"`
	Events          []AcceptedEvent `json:"events,omitempty"`
	SkippedProjects []SkippedTarget `json:"skipped_projects,omitempty"`
}

// ReceiptAuditRecord carries the content-free metadata for a webhook receipt
// audit event.
type ReceiptAuditRecord struct {
	Kind               string
	Delivery           string
	Event              string
	Action             string
	InstallationID     string
	Resource           string
	PayloadBytes       int
	Result             string
	Routing            string
	BypassedProjectIDs []string
	ProjectIDs         []string
	Events             []AcceptedEvent
	SkippedProjects    []SkippedTarget
}

// EventSink persists and enqueues a normalised event for each resolved target
// project (per-target transaction: quota lock, dedup by key and body SHA-256,
// pending cap, enqueue, and post-commit wake).
type EventSink interface {
	EnqueueForTargets(ctx context.Context, ev Event, bodySHA256 string, targets []Target) ([]TargetResult, error)
}

// RouteLimiter enforces the hub-wide verified-delivery rate bucket for a source
// kind.
type RouteLimiter interface {
	Allow(now time.Time) bool
}

// RouteAudit records content-free webhook receipt, target-mismatch, and
// own-actor ignore audit signals.
type RouteAudit interface {
	RecordReceipt(ctx context.Context, rec ReceiptAuditRecord)
	RecordTargetMismatch(ctx context.Context, now time.Time)
	RecordSelfIgnored(ctx context.Context)
}

// RouteMetrics records delivery outcome counters for a source kind.
type RouteMetrics interface {
	RecordDelivery(kind, result string)
}

// RouteDeps wires the provider-neutral shared-route ingress pipeline for an
// app-shaped webhook source.
type RouteDeps struct {
	Kind       string
	Verifier   WireVerifier
	Resolver   InstallationResolver
	Normalizer Normalizer
	Actor      ActorIdentity
	Secrets    SecretSource
	Sink       EventSink
	Limiter    RouteLimiter
	Audit      RouteAudit
	Metrics    RouteMetrics
	MaxBody    int64

	// Now returns the current clock time (defaults to time.Now).
	Now func() time.Time
	// PingEvent names the provider's webhook ping event (e.g. "ping"). When
	// Resolver.OnLifecycle handles a delivery with Event == PingEvent, ServeHTTP
	// responds with 200 {"status":"pong"} instead of 200 {"status":"ok"}.
	PingEvent string
	// ContentEnabled is an optional gate checked before processing non-lifecycle
	// content deliveries. When it returns false, ServeHTTP responds with
	// 200 {"status":"ignored","event":...}.
	ContentEnabled func(ctx context.Context) bool
	// OnSecretNotConfigured is an optional hook called when Verify fails with
	// ErrSecretNotConfigured (e.g. for a once-per-process warning log).
	OnSecretNotConfigured func()
	// SecretNotConfiguredMessage overrides the 503 error message returned when
	// Verify fails with ErrSecretNotConfigured.
	SecretNotConfiguredMessage string
	// PersistErrorMessage overrides the 500 error message returned when
	// Sink.EnqueueForTargets fails.
	PersistErrorMessage string
	// WriteError writes an HTTP error response (and any hub-standard rejection
	// logs). When nil, ServeHTTP falls back to writing a plain JSON error body.
	WriteError func(w http.ResponseWriter, status int, code, message string)
}

// ServeHTTP executes the provider-neutral shared-route webhook pipeline:
// method check -> body read -> Verify -> OnLifecycle -> experiment gate ->
// supported-event check -> rate limit -> body SHA-256 -> Normalize (envelope) ->
// Installation -> Actor.IsSelf -> Targets -> Normalize finalize / held error ->
// Sink.EnqueueForTargets -> receipt audit + 202 response.
func (d RouteDeps) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		d.writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "Method not allowed")
		return
	}

	maxBody := d.MaxBody
	if maxBody <= 0 {
		maxBody = DefaultMaxBodyBytes
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxBody))
	if err != nil {
		d.writeError(w, http.StatusBadRequest, "invalid_request", "failed to read request body")
		return
	}

	ctx := r.Context()
	now := d.nowUTC()

	if d.Verifier == nil {
		d.recordMetric("error")
		d.writeError(w, http.StatusInternalServerError, "internal_error", "internal server error")
		return
	}

	deliv, err := d.Verifier.Verify(r, body, d.Secrets)
	if err != nil {
		// Emit the receipt log after signature authentication succeeds, even if
		// target/header/delivery validation subsequently rejects the request.
		if !errors.Is(err, ErrSecretNotConfigured) && !errors.Is(err, ErrInvalidSignature) &&
			(errors.Is(err, ErrTargetMismatch) || errors.Is(err, ErrInvalidHeaders) || errors.Is(err, ErrInvalidDeliveryID) || isSkipErr(err)) {
			d.logReceived(deliv)
		}
		d.handleVerifyError(w, ctx, now, deliv, len(body), err)
		return
	}
	if !deliv.ReceivedAt.IsZero() {
		now = deliv.ReceivedAt.UTC()
	}

	d.logReceived(deliv)

	// Lifecycle routing (ping / installation / installation_repositories).
	if d.Resolver != nil {
		handled, lErr := d.Resolver.OnLifecycle(ctx, deliv, body)
		if lErr != nil {
			d.recordMetric("error")
			d.writeError(w, http.StatusBadRequest, "invalid_request", "invalid webhook payload")
			return
		}
		if handled {
			if d.PingEvent != "" && deliv.Event == d.PingEvent {
				writeRouteJSON(w, http.StatusOK, map[string]string{"status": "pong"})
				return
			}
			writeRouteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
			return
		}
	}

	// Step 0: content-event experiment gate.
	if d.ContentEnabled != nil && !d.ContentEnabled(ctx) {
		writeRouteJSON(w, http.StatusOK, map[string]string{"status": RouteStatusIgnored, "event": deliv.Event})
		return
	}

	// Step 2 (unsupported event family): 200 ignored before consuming rate-limit tokens.
	if deliv.Unsupported {
		d.recordMetric("unsupported")
		writeRouteJSON(w, http.StatusOK, map[string]string{"status": RouteStatusIgnored, "event": deliv.Event})
		return
	}

	// Step 3: hub-wide verified delivery rate bucket.
	if d.Limiter != nil && !d.Limiter.Allow(now) {
		d.recordMetric("rate_limited")
		w.Header().Set("Retry-After", "5")
		d.writeError(w, http.StatusServiceUnavailable, "unavailable", "rate limit exceeded")
		return
	}

	rawBodySum := sha256.Sum256(body)
	rawBodySHA256 := hex.EncodeToString(rawBodySum[:])

	// Step 4: normalise once from the raw payload (pure, no DB or network I/O).
	// Envelope errors leave ev.SourceID == "" and fail immediately; held errors
	// (family/subject/attributes-ceiling) populate ev's routing/sender fields
	// and wait until step 8 to preserve the refusal order.
	if d.Normalizer == nil {
		d.recordMetric("error")
		d.writeError(w, http.StatusInternalServerError, "internal_error", "internal server error")
		return
	}
	ev, normErr := d.Normalizer.Normalize(ctx, deliv, body, Target{})
	if normErr == nil && !ev.Handled {
		d.recordMetric("unsupported")
		writeRouteJSON(w, http.StatusOK, map[string]string{"status": RouteStatusIgnored, "event": deliv.Event})
		return
	}
	if normErr != nil && ev.SourceID == "" {
		var skipErr ErrSkip
		if errors.As(normErr, &skipErr) {
			d.recordReceipt(ctx, ReceiptAuditRecord{
				Kind:         d.Kind,
				Delivery:     deliv.ID,
				Event:        deliv.Event,
				Action:       ev.Action,
				Resource:     ev.Resource,
				PayloadBytes: len(body),
				Result:       RouteStatusSkipped,
			})
			d.recordMetric("skipped")
			writeRouteJSON(w, http.StatusAccepted, AcceptedResponse{
				Status:   RouteStatusSkipped,
				Reason:   skipErr.Reason,
				Delivery: deliv.ID,
			})
			return
		}
		d.recordMetric("error")
		d.writeError(w, http.StatusBadRequest, "invalid_request", "invalid payload")
		return
	}

	if d.Resolver == nil {
		d.recordMetric("error")
		d.writeError(w, http.StatusInternalServerError, "internal_error", "internal server error")
		return
	}

	// Step 5: installation lookup — resolve from the single normalized event.
	inst, err := d.Resolver.Installation(ctx, deliv, ev)
	if err != nil {
		if errors.Is(err, ErrUnavailable) {
			d.recordMetric("error")
			d.writeError(w, http.StatusServiceUnavailable, "unavailable", "trigger store is not available")
			return
		}
		if !errors.Is(err, ErrInstallationNotFound) {
			slog.Error("Source trigger ingress installation lookup failed",
				"source_kind", d.Kind,
				"installation_id", ev.SourceID,
				"delivery_id", deliv.ID,
				"error", err,
			)
			d.recordMetric("error")
			d.writeError(w, http.StatusInternalServerError, "internal_error", "internal server error")
			return
		}
		inst = Installation{}
	}
	if errors.Is(err, ErrInstallationNotFound) || !inst.Active {
		d.recordReceipt(ctx, ReceiptAuditRecord{
			Kind:           d.Kind,
			Delivery:       deliv.ID,
			Event:          deliv.Event,
			Action:         ev.Action,
			InstallationID: ev.SourceID,
			Resource:       ev.Resource,
			PayloadBytes:   len(body),
			Result:         RouteStatusUnknownInstallation,
		})
		d.recordMetric("skipped")
		writeRouteJSON(w, http.StatusAccepted, AcceptedResponse{
			Status:   RouteStatusUnknownInstallation,
			Delivery: deliv.ID,
		})
		return
	}

	// Step 6: own-actor loop guard.
	if d.Actor != nil && d.Actor.IsSelf(ctx, ev.Sender) {
		if d.Audit != nil {
			d.Audit.RecordSelfIgnored(ctx)
		}
		d.recordMetric("self_ignored")
		writeRouteJSON(w, http.StatusAccepted, AcceptedResponse{
			Status:   RouteStatusIgnored,
			Reason:   "self",
			Delivery: deliv.ID,
		})
		return
	}

	// Step 7: target fan-out and primary routing from the normalized event.
	targets, err := d.Resolver.Targets(ctx, inst, ev)
	if err != nil {
		if errors.Is(err, ErrUnavailable) {
			d.recordMetric("error")
			d.writeError(w, http.StatusServiceUnavailable, "unavailable", "trigger store is not available")
			return
		}
		slog.Error("Source trigger ingress target fan-out lookup failed",
			"source_kind", d.Kind,
			"installation_id", inst.ID,
			"delivery_id", deliv.ID,
			"error", err,
		)
		d.recordMetric("error")
		d.writeError(w, http.StatusInternalServerError, "internal_error", "internal server error")
		return
	}
	if len(targets) == 0 {
		d.recordReceipt(ctx, ReceiptAuditRecord{
			Kind:           d.Kind,
			Delivery:       deliv.ID,
			Event:          deliv.Event,
			Action:         ev.Action,
			InstallationID: inst.ID,
			Resource:       ev.Resource,
			PayloadBytes:   len(body),
			Result:         RouteStatusNoProject,
		})
		d.recordMetric("skipped")
		writeRouteJSON(w, http.StatusAccepted, AcceptedResponse{
			Status:   RouteStatusNoProject,
			Delivery: deliv.ID,
		})
		return
	}

	selectedIDs := make([]string, len(targets))
	for i := range targets {
		selectedIDs[i] = targets[i].ProjectID
	}
	bypassed := targets[0].BypassedProjectIDs
	auditRouting := ""
	switch targets[0].RoutingStatus {
	case RoutingStatusOK:
		if len(bypassed) > 0 {
			auditRouting = RoutingRoutedToPrimary
		}
	case RoutingStatusStalePrimary:
		auditRouting = RoutingStalePrimary
	}

	// Step 8: optional post-actor normalization finalize, then handle held errors.
	if normErr == nil {
		if fin, ok := d.Normalizer.(FinalizingNormalizer); ok {
			ev, normErr = fin.Finalize(ctx, deliv, body, ev)
		}
	}
	if normErr != nil {
		var skipErr ErrSkip
		if errors.As(normErr, &skipErr) {
			skipped := make([]SkippedTarget, len(targets))
			for i := range targets {
				skipped[i] = SkippedTarget{
					ProjectID: targets[i].ProjectID,
					Reason:    skipErr.Reason,
				}
			}
			d.recordReceipt(ctx, ReceiptAuditRecord{
				Kind:               d.Kind,
				Delivery:           deliv.ID,
				Event:              deliv.Event,
				Action:             ev.Action,
				InstallationID:     inst.ID,
				Resource:           ev.Resource,
				PayloadBytes:       len(body),
				Result:             RouteStatusSkipped,
				Routing:            auditRouting,
				BypassedProjectIDs: bypassed,
				ProjectIDs:         selectedIDs,
				SkippedProjects:    skipped,
			})
			d.recordMetric("skipped")
			writeRouteJSON(w, http.StatusAccepted, AcceptedResponse{
				Status:          RouteStatusSkipped,
				Delivery:        deliv.ID,
				SkippedProjects: skipped,
			})
			return
		}
		d.recordMetric("error")
		d.writeError(w, http.StatusBadRequest, "invalid_request", "invalid payload")
		return
	}

	// Step 9: per-target persistence + enqueue via EventSink.
	if d.Sink == nil {
		d.recordMetric("error")
		d.writeError(w, http.StatusServiceUnavailable, "unavailable", "trigger event queue is not available")
		return
	}
	if d.Kind != "" {
		ev.SourceKind = d.Kind
	}
	if ev.SourceID == "" {
		ev.SourceID = inst.ID
	}
	if ev.DeliveryID == "" {
		ev.DeliveryID = deliv.ID
	}
	if ev.DedupKey == "" {
		ev.DedupKey = deliv.ID
	}
	if ev.ReceivedAt.IsZero() {
		ev.ReceivedAt = now
	}
	if ev.Headers == nil && len(deliv.Headers) > 0 {
		ev.Headers = deliv.Headers
	}

	results, sinkErr := d.Sink.EnqueueForTargets(ctx, ev, rawBodySHA256, targets)
	events, skipped, anyQueued := partitionTargetResults(results)

	if sinkErr != nil {
		if errors.Is(sinkErr, ErrUnavailable) {
			d.recordMetric("error")
			d.writeError(w, http.StatusServiceUnavailable, "unavailable", "trigger event queue is not available")
			return
		}
		if anyQueued {
			d.recordReceipt(ctx, ReceiptAuditRecord{
				Kind:               d.Kind,
				Delivery:           deliv.ID,
				Event:              deliv.Event,
				Action:             ev.Action,
				InstallationID:     inst.ID,
				Resource:           ev.Resource,
				PayloadBytes:       len(body),
				Result:             RouteStatusError,
				Routing:            auditRouting,
				BypassedProjectIDs: bypassed,
				ProjectIDs:         selectedIDs,
				Events:             events,
				SkippedProjects:    skipped,
			})
		}
		d.recordMetric("error")
		msg := d.PersistErrorMessage
		if msg == "" {
			msg = "failed to persist trigger event"
		}
		d.writeError(w, http.StatusInternalServerError, "internal_error", msg)
		return
	}

	// Step 10: 202 response body + content-free receipt audit.
	topStatus := RouteStatusSkipped
	metricResult := "skipped"
	switch {
	case anyQueued:
		topStatus = RouteStatusQueued
		metricResult = "accepted"
	case len(events) > 0:
		topStatus = RouteStatusDuplicate
		metricResult = "duplicate"
	}

	d.recordReceipt(ctx, ReceiptAuditRecord{
		Kind:               d.Kind,
		Delivery:           deliv.ID,
		Event:              deliv.Event,
		Action:             ev.Action,
		InstallationID:     inst.ID,
		Resource:           ev.Resource,
		PayloadBytes:       len(body),
		Result:             topStatus,
		Routing:            auditRouting,
		BypassedProjectIDs: bypassed,
		ProjectIDs:         selectedIDs,
		Events:             events,
		SkippedProjects:    skipped,
	})
	d.recordMetric(metricResult)

	writeRouteJSON(w, http.StatusAccepted, AcceptedResponse{
		Status:          topStatus,
		Delivery:        deliv.ID,
		Events:          events,
		SkippedProjects: skipped,
	})
}

func isSkipErr(err error) bool {
	var skipErr ErrSkip
	return errors.As(err, &skipErr)
}

func (d RouteDeps) logReceived(deliv Delivery) {
	deliveryID := deliv.RawID
	if deliveryID == "" {
		deliveryID = deliv.ID
	}
	slog.Info("Source webhook received",
		"source_kind", d.Kind,
		"event", deliv.Event,
		"delivery_id", deliveryID,
	)
}

func (d RouteDeps) handleVerifyError(w http.ResponseWriter, ctx context.Context, now time.Time, deliv Delivery, payloadBytes int, err error) {
	switch {
	case errors.Is(err, ErrSecretNotConfigured):
		if d.OnSecretNotConfigured != nil {
			d.OnSecretNotConfigured()
		}
		d.recordMetric("error")
		msg := d.SecretNotConfiguredMessage
		if msg == "" {
			msg = "webhook is not configured"
		}
		d.writeError(w, http.StatusServiceUnavailable, "internal_error", msg)
	case errors.Is(err, ErrInvalidSignature):
		d.recordMetric("error")
		d.writeError(w, http.StatusUnauthorized, "unauthorized", "invalid webhook signature")
	case errors.Is(err, ErrTargetMismatch):
		if d.ContentEnabled != nil && !d.ContentEnabled(ctx) {
			writeRouteJSON(w, http.StatusOK, map[string]string{"status": RouteStatusIgnored, "event": deliv.Event})
			return
		}
		if d.Audit != nil {
			d.Audit.RecordTargetMismatch(ctx, now)
		}
		d.recordMetric("target_mismatch")
		d.writeError(w, http.StatusBadRequest, "invalid_request", "wrong target")
	case errors.Is(err, ErrInvalidHeaders):
		if d.ContentEnabled != nil && !d.ContentEnabled(ctx) {
			writeRouteJSON(w, http.StatusOK, map[string]string{"status": RouteStatusIgnored, "event": deliv.Event})
			return
		}
		d.recordMetric("error")
		d.writeError(w, http.StatusBadRequest, "invalid_request", "invalid event header")
	case errors.Is(err, ErrInvalidDeliveryID):
		if d.ContentEnabled != nil && !d.ContentEnabled(ctx) {
			writeRouteJSON(w, http.StatusOK, map[string]string{"status": RouteStatusIgnored, "event": deliv.Event})
			return
		}
		d.recordMetric("error")
		d.writeError(w, http.StatusBadRequest, "invalid_request", "invalid delivery id")
	default:
		var skipErr ErrSkip
		if errors.As(err, &skipErr) {
			d.recordReceipt(ctx, ReceiptAuditRecord{
				Kind:         d.Kind,
				Delivery:     deliv.ID,
				Event:        deliv.Event,
				PayloadBytes: payloadBytes,
				Result:       RouteStatusSkipped,
			})
			d.recordMetric("skipped")
			writeRouteJSON(w, http.StatusAccepted, AcceptedResponse{
				Status:   RouteStatusSkipped,
				Reason:   skipErr.Reason,
				Delivery: deliv.ID,
			})
			return
		}
		d.recordMetric("error")
		d.writeError(w, http.StatusUnauthorized, "unauthorized", "invalid webhook signature")
	}
}

func (d RouteDeps) writeError(w http.ResponseWriter, status int, code, message string) {
	if d.WriteError != nil {
		d.WriteError(w, status, code, message)
		return
	}
	writeRouteError(w, status, code, message)
}

func (d RouteDeps) nowUTC() time.Time {
	if d.Now != nil {
		return d.Now().UTC()
	}
	return time.Now().UTC()
}

func (d RouteDeps) recordReceipt(ctx context.Context, rec ReceiptAuditRecord) {
	if d.Audit != nil {
		d.Audit.RecordReceipt(ctx, rec)
	}
}

func (d RouteDeps) recordMetric(result string) {
	if d.Metrics != nil {
		d.Metrics.RecordDelivery(d.Kind, result)
	}
}

func partitionTargetResults(results []TargetResult) (events []AcceptedEvent, skipped []SkippedTarget, anyQueued bool) {
	for _, res := range results {
		switch res.Status {
		case TargetStatusQueued:
			anyQueued = true
			events = append(events, AcceptedEvent{
				ProjectID: res.ProjectID,
				EventID:   res.EventID,
				Status:    TargetStatusQueued,
			})
		case TargetStatusDuplicate:
			events = append(events, AcceptedEvent{
				ProjectID: res.ProjectID,
				EventID:   res.EventID,
				Status:    TargetStatusDuplicate,
			})
		case TargetStatusSkipped:
			skipped = append(skipped, SkippedTarget{
				ProjectID: res.ProjectID,
				Reason:    res.Reason,
			})
		}
	}
	return events, skipped, anyQueued
}

type routeErrorBody struct {
	Error routeErrorDetail `json:"error"`
}

type routeErrorDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func writeRouteError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(routeErrorBody{
		Error: routeErrorDetail{
			Code:    code,
			Message: message,
		},
	})
}

func writeRouteJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(data)
}

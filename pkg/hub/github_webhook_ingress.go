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
	"errors"
	"log/slog"
	"maps"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/GoogleCloudPlatform/scion/pkg/durablequeue"
	"github.com/GoogleCloudPlatform/scion/pkg/hub/trigger"
	"github.com/GoogleCloudPlatform/scion/pkg/hub/trigger/source"
	"github.com/GoogleCloudPlatform/scion/pkg/store"
	"github.com/GoogleCloudPlatform/scion/pkg/util/logging"
)

// GitHub App content-event ingress rate-limit, header, status, and audit
// constants (Stage 3 design §4.1–§4.4).
const (
	GitHubWebhookVerifiedRatePerSecond = 100.0
	GitHubWebhookVerifiedBurst         = 500

	GitHubAuditEventRejected              = "trigger.github.rejected"
	GitHubAuditRejectReasonTargetMismatch = "target_mismatch"

	GitHubWebhookStatusQueued              = "queued"
	GitHubWebhookStatusDuplicate           = "duplicate"
	GitHubWebhookStatusSkipped             = "skipped"
	GitHubWebhookStatusIgnored             = "ignored"
	GitHubWebhookStatusUnknownInstallation = "unknown_installation"
	GitHubWebhookStatusNoProject           = "no_project"
	GitHubWebhookStatusError               = "error"

	GitHubWebhookSkipReasonQueueFull         = "queue_full"
	GitHubWebhookSkipReasonAttributesCeiling = "attributes_ceiling"
	GitHubWebhookSkipReasonInvalidEvent      = "invalid_event"

	GitHubWebhookRoutingRoutedToPrimary = "routed_to_primary"
	GitHubWebhookRoutingStalePrimary    = "stale_primary"

	githubTargetMismatchAuditInterval = time.Minute
	githubStalePrimaryWarnInterval    = time.Hour
	githubUnknownIdentityWarnInterval = time.Hour
	maxGitHubStalePrimaryWarnEntries  = 4096
	githubCandidateProjectsPageSize   = 200
	maxGitHubStoredUserAgentBytes     = 512
	maxGitHubStoredHeaderBytes        = 256
)

var (
	errGitHubDuplicateDelivery       = errors.New("duplicate github webhook delivery")
	errGitHubProjectQueueFull        = errors.New("github trigger event queue full for project")
	errGitHubMissingPendingCounterTx = errors.New("transaction store does not implement DurableQueuePendingCounter")
)

// GitHubWebhookProjectEvent reports the per-project outcome of an accepted
// GitHub content webhook delivery (§4.1 step 10).
type GitHubWebhookProjectEvent struct {
	ProjectID string `json:"project_id"`
	EventID   string `json:"event_id"`
	Status    string `json:"status"`
}

// GitHubWebhookSkippedProject reports a candidate project whose event was not
// persisted during fan-out (§4.1 steps 8–10).
type GitHubWebhookSkippedProject struct {
	ProjectID string `json:"project_id"`
	Reason    string `json:"reason"`
}

// GitHubWebhookAcceptedResponse is the HTTP 202 response payload returned for
// verified GitHub App content-event deliveries (§4.1 steps 5–10).
type GitHubWebhookAcceptedResponse struct {
	Status          string                        `json:"status"`
	Reason          string                        `json:"reason,omitempty"`
	Delivery        string                        `json:"delivery,omitempty"`
	Events          []GitHubWebhookProjectEvent   `json:"events,omitempty"`
	SkippedProjects []GitHubWebhookSkippedProject `json:"skipped_projects,omitempty"`
}

type githubStalePrimaryKey struct {
	installationID int64
	repo           string
}

type githubReceiptAuditRecord struct {
	Delivery           string
	Event              string
	Action             string
	InstallationID     int64
	Repo               string
	PayloadBytes       int
	Result             string
	Routing            string
	BypassedProjectIDs []string
	ProjectIDs         []string
	Events             []GitHubWebhookProjectEvent
	SkippedProjects    []GitHubWebhookSkippedProject
}

// githubWebhookIngressState holds the per-Server verified token bucket,
// rate-limited target-mismatch audit window, rate-limited stale-primary warning
// map, and outcome counters for GitHub App content-event ingress. Zero value is
// ready to use with Stage 3 defaults.
type githubWebhookIngressState struct {
	mu sync.Mutex

	bucketInit      bool
	bucketTokens    float64
	bucketLastCheck time.Time
	bucketRate      float64
	bucketBurst     int

	lastTargetMismatchAudit    time.Time
	suppressedTargetMismatches int

	stalePrimaryWarns       map[githubStalePrimaryKey]time.Time
	lastUnknownIdentityWarn time.Time

	nowFn       func() time.Time
	auditLogger *slog.Logger
	warnLogger  *slog.Logger

	// In-process outcome counters read by unit tests; placeholders for the G7
	// Prometheus counter scion_trigger_github_deliveries_total{result} (§15 G7).
	selfIgnoredCount    atomic.Int64
	targetMismatchCount atomic.Int64
	rateLimitedCount    atomic.Int64

	// Test-only seam wrappers injected at the GitHubSource*() accessor
	// boundary to prove production ingress routes through the seams.
	// Must remain nil outside tests.
	wrapWireVerifier func(source.WireVerifier) source.WireVerifier
	wrapResolver     func(source.InstallationResolver) source.InstallationResolver
	wrapNormalizer   func(source.Normalizer) source.Normalizer
	wrapActor        func(source.ActorIdentity) source.ActorIdentity
}

func (st *githubWebhookIngressState) getWrapWireVerifier() func(source.WireVerifier) source.WireVerifier {
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.wrapWireVerifier
}

func (st *githubWebhookIngressState) getWrapResolver() func(source.InstallationResolver) source.InstallationResolver {
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.wrapResolver
}

func (st *githubWebhookIngressState) getWrapNormalizer() func(source.Normalizer) source.Normalizer {
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.wrapNormalizer
}

func (st *githubWebhookIngressState) getWrapActor() func(source.ActorIdentity) source.ActorIdentity {
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.wrapActor
}

func (st *githubWebhookIngressState) now() time.Time {
	st.mu.Lock()
	fn := st.nowFn
	st.mu.Unlock()
	if fn != nil {
		return fn()
	}
	return timeNow()
}

func (st *githubWebhookIngressState) auditLog() *slog.Logger {
	st.mu.Lock()
	l := st.auditLogger
	st.mu.Unlock()
	if l != nil {
		return l
	}
	return logging.Subsystem("hub.audit")
}

func (st *githubWebhookIngressState) allowVerified(now time.Time) bool {
	st.mu.Lock()
	defer st.mu.Unlock()

	rate := st.bucketRate
	if rate <= 0 {
		rate = GitHubWebhookVerifiedRatePerSecond
	}
	burst := st.bucketBurst
	if burst == 0 {
		burst = GitHubWebhookVerifiedBurst
	}
	if burst < 0 {
		return false
	}

	if !st.bucketInit {
		st.bucketInit = true
		st.bucketTokens = float64(burst) - 1
		st.bucketLastCheck = now
		return true
	}

	elapsed := now.Sub(st.bucketLastCheck).Seconds()
	if elapsed > 0 {
		st.bucketTokens += elapsed * rate
		if st.bucketTokens > float64(burst) {
			st.bucketTokens = float64(burst)
		}
		st.bucketLastCheck = now
	}
	if st.bucketTokens >= 1 {
		st.bucketTokens--
		return true
	}
	return false
}

func (st *githubWebhookIngressState) recordTargetMismatchAudit(ctx context.Context, now time.Time) {
	st.targetMismatchCount.Add(1)

	st.mu.Lock()
	if !st.lastTargetMismatchAudit.IsZero() && now.Sub(st.lastTargetMismatchAudit) < githubTargetMismatchAuditInterval {
		st.suppressedTargetMismatches++
		st.mu.Unlock()
		return
	}
	suppressed := st.suppressedTargetMismatches
	st.suppressedTargetMismatches = 0
	st.lastTargetMismatchAudit = now
	l := st.auditLogger
	st.mu.Unlock()

	if l == nil {
		l = logging.Subsystem("hub.audit")
	}
	attrs := []slog.Attr{
		slog.String("event_type", GitHubAuditEventRejected),
		slog.String("reason", GitHubAuditRejectReasonTargetMismatch),
	}
	if suppressed > 0 {
		attrs = append(attrs, slog.Int("suppressed_count", suppressed))
	}
	if reqID := logging.RequestIDFromContext(ctx); reqID != "" {
		attrs = append(attrs, slog.String(logging.AttrRequestID, reqID))
	}
	l.LogAttrs(ctx, slog.LevelWarn, GitHubAuditEventRejected, attrs...)
}

func (st *githubWebhookIngressState) maybeWarnStalePrimary(installationID int64, repo string, now time.Time) {
	key := githubStalePrimaryKey{
		installationID: installationID,
		repo:           strings.ToLower(strings.TrimSpace(repo)),
	}

	st.mu.Lock()
	if st.stalePrimaryWarns == nil {
		st.stalePrimaryWarns = make(map[githubStalePrimaryKey]time.Time)
	}
	if last, ok := st.stalePrimaryWarns[key]; ok && now.Sub(last) < githubStalePrimaryWarnInterval {
		st.mu.Unlock()
		return
	}
	if len(st.stalePrimaryWarns) >= maxGitHubStalePrimaryWarnEntries {
		var oldestKey githubStalePrimaryKey
		var oldestTime time.Time
		hasOldest := false
		for k, t := range st.stalePrimaryWarns {
			if now.Sub(t) >= githubStalePrimaryWarnInterval {
				delete(st.stalePrimaryWarns, k)
				continue
			}
			if !hasOldest || t.Before(oldestTime) {
				oldestKey = k
				oldestTime = t
				hasOldest = true
			}
		}
		if len(st.stalePrimaryWarns) >= maxGitHubStalePrimaryWarnEntries && hasOldest {
			delete(st.stalePrimaryWarns, oldestKey)
		}
	}
	st.stalePrimaryWarns[key] = now
	l := st.warnLogger
	st.mu.Unlock()

	if l == nil {
		l = slog.Default()
	}
	l.Warn("GitHub trigger primary project is stale; fanning out to all candidate projects",
		"installation_id", installationID,
		"repo", repo,
		"routing", GitHubWebhookRoutingStalePrimary,
	)
}

func (st *githubWebhookIngressState) maybeWarnUnknownAppIdentity(appID int64, now time.Time) {
	st.mu.Lock()
	if !st.lastUnknownIdentityWarn.IsZero() && now.Sub(st.lastUnknownIdentityWarn) < githubUnknownIdentityWarnInterval {
		st.mu.Unlock()
		return
	}
	st.lastUnknownIdentityWarn = now
	l := st.warnLogger
	st.mu.Unlock()

	if l == nil {
		l = slog.Default()
	}
	l.Warn("GitHub App bot login unavailable; own-App webhook guard is relying on performed_via_github_app.id until GET /app succeeds or server.github_app.app_slug is set",
		"app_id", appID,
	)
}

func (st *githubWebhookIngressState) recordReceiptAuditWithKind(ctx context.Context, sourceKind string, rec githubReceiptAuditRecord) {
	if sourceKind == "" {
		sourceKind = store.TriggerSourceKindGitHub
	}
	l := st.auditLog()
	attrs := []slog.Attr{
		slog.String("event_type", trigger.AuditEventReceived),
		slog.String("source_kind", sourceKind),
		slog.String("delivery", rec.Delivery),
		slog.String("event", rec.Event),
		slog.Int("payload_bytes", rec.PayloadBytes),
		slog.String("result", rec.Result),
	}
	if rec.Action != "" {
		attrs = append(attrs, slog.String("action", rec.Action))
	}
	if rec.InstallationID > 0 {
		attrs = append(attrs, slog.Int64("installation_id", rec.InstallationID))
	}
	if rec.Repo != "" {
		attrs = append(attrs, slog.String("repo", rec.Repo))
	}
	if rec.Routing != "" {
		attrs = append(attrs, slog.String("routing", rec.Routing))
	}
	if len(rec.BypassedProjectIDs) > 0 {
		attrs = append(attrs, slog.Any("bypassed_project_ids", rec.BypassedProjectIDs))
	}
	if len(rec.ProjectIDs) > 0 {
		attrs = append(attrs, slog.Any("project_ids", rec.ProjectIDs))
	}
	if len(rec.Events) > 0 {
		attrs = append(attrs, slog.Any("events", rec.Events))
	}
	if len(rec.SkippedProjects) > 0 {
		attrs = append(attrs, slog.Any("skipped_projects", rec.SkippedProjects))
	}
	if reqID := logging.RequestIDFromContext(ctx); reqID != "" {
		attrs = append(attrs, slog.String(logging.AttrRequestID, reqID))
	}
	l.LogAttrs(ctx, slog.LevelInfo, trigger.AuditEventReceived, attrs...)
}

// sourceEventSink implements source.EventSink for app-shaped webhook sources:
// per-target transaction (quota lock, dedup by key and body SHA-256, pending
// cap, trigger_events insert + durablequeue Enqueue) and post-commit wake.
// The source kind is taken from ev.SourceKind, which RouteDeps.ServeHTTP sets
// from RouteDeps.Kind as the single source of truth.
type sourceEventSink struct {
	srv *Server
}

var _ source.EventSink = (*sourceEventSink)(nil)

func (sink *sourceEventSink) EnqueueForTargets(ctx context.Context, ev source.Event, bodySHA256 string, targets []source.Target) ([]source.TargetResult, error) {
	if sink == nil || sink.srv == nil || sink.srv.store == nil {
		return nil, source.ErrUnavailable
	}
	queue := sink.srv.TriggerEventQueue()
	if queue == nil {
		return nil, source.ErrUnavailable
	}

	sourceKind := ev.SourceKind
	if sourceKind == "" {
		return nil, errors.New("source event sink: missing event source kind")
	}
	storedHeaders := sanitizeGitHubEventHeaders(ev.Headers)
	projectedPayload := string(ev.Payload)
	receivedAt := ev.ReceivedAt
	if receivedAt.IsZero() {
		receivedAt = sink.srv.githubWebhookIngress.now().UTC()
	}

	var (
		results   []source.TargetResult
		anyQueued bool
	)

	for i := range targets {
		tgt := &targets[i]
		dedupKey := ""
		if ev.DedupKey != "" && tgt.ProjectID != "" {
			dedupKey = ev.DedupKey + "/" + tgt.ProjectID
		}
		storeEv := store.TriggerEvent{
			ID:           uuid.NewString(),
			ProjectID:    tgt.ProjectID,
			SourceKind:   sourceKind,
			SourceID:     ev.SourceID,
			Type:         ev.Type,
			Subject:      ev.Subject,
			DedupKey:     dedupKey,
			BodySHA256:   bodySHA256,
			OccurredAt:   ev.OccurredAt,
			ReceivedAt:   receivedAt,
			Attributes:   maps.Clone(ev.Attributes),
			Lists:        copyTriggerStringListMap(ev.Lists),
			Headers:      maps.Clone(storedHeaders),
			Payload:      projectedPayload,
			PayloadBytes: ev.PayloadBytes,
			Status:       store.TriggerEventStatusPending,
			CreatedAt:    receivedAt,
		}

		var dupEventID string
		txErr := sink.srv.store.WithTx(ctx, func(tx store.Store) error {
			if lErr := tx.LockTriggerQuota(ctx, tgt.ProjectID); lErr != nil {
				return lErr
			}
			existing, gErr := tx.GetTriggerEventByDedupKey(ctx, sourceKind, ev.SourceID, storeEv.DedupKey)
			if gErr == nil && existing != nil {
				dupEventID = existing.ID
				return errGitHubDuplicateDelivery
			}
			if gErr != nil && !errors.Is(gErr, store.ErrNotFound) {
				return gErr
			}

			existingByBody, bErr := tx.GetTriggerEventByBodySHA256(ctx, tgt.ProjectID, sourceKind, storeEv.BodySHA256)
			if bErr == nil && existingByBody != nil {
				dupEventID = existingByBody.ID
				return errGitHubDuplicateDelivery
			}
			if bErr != nil && !errors.Is(bErr, store.ErrNotFound) {
				return bErr
			}

			txCounter, ok := tx.(store.DurableQueuePendingCounter)
			if !ok {
				return errGitHubMissingPendingCounterTx
			}
			pending, cErr := txCounter.CountDurableQueuePending(ctx, trigger.EventQueueKind, tgt.ProjectID)
			if cErr != nil {
				return cErr
			}
			if pending >= trigger.MaxPendingEventsPerProject {
				return errGitHubProjectQueueFull
			}

			_, enqErr := trigger.EnqueueEvent(ctx, tx, tx, queue, &storeEv)
			return enqErr
		})

		if txErr == nil {
			anyQueued = true
			results = append(results, source.TargetResult{
				ProjectID: tgt.ProjectID,
				EventID:   storeEv.ID,
				Status:    source.TargetStatusQueued,
			})
			continue
		}

		if errors.Is(txErr, errGitHubProjectQueueFull) {
			results = append(results, source.TargetResult{
				ProjectID: tgt.ProjectID,
				Status:    source.TargetStatusSkipped,
				Reason:    GitHubWebhookSkipReasonQueueFull,
			})
			continue
		}

		if errors.Is(txErr, store.ErrInvalidInput) {
			slog.Warn("Skipping GitHub trigger event rejected by store validation",
				"project_id", tgt.ProjectID,
				"installation_id", ev.SourceID,
				"delivery_id", ev.DeliveryID,
				"reason", GitHubWebhookSkipReasonInvalidEvent,
			)
			results = append(results, source.TargetResult{
				ProjectID: tgt.ProjectID,
				Status:    source.TargetStatusSkipped,
				Reason:    GitHubWebhookSkipReasonInvalidEvent,
			})
			continue
		}

		if errors.Is(txErr, errGitHubDuplicateDelivery) ||
			(!errors.Is(txErr, durablequeue.ErrDuplicate) && errors.Is(txErr, store.ErrAlreadyExists)) {
			if dupEventID == "" {
				existing, gErr := sink.srv.store.GetTriggerEventByDedupKey(ctx, sourceKind, ev.SourceID, storeEv.DedupKey)
				if errors.Is(gErr, store.ErrNotFound) && storeEv.BodySHA256 != "" {
					existing, gErr = sink.srv.store.GetTriggerEventByBodySHA256(ctx, tgt.ProjectID, sourceKind, storeEv.BodySHA256)
				}
				if gErr != nil || existing == nil {
					if anyQueued {
						queue.Wake(trigger.EventQueueKind)
					}
					slog.Error("Failed to resolve GitHub trigger event after unique conflict",
						"project_id", tgt.ProjectID,
						"installation_id", ev.SourceID,
						"delivery_id", ev.DeliveryID,
						"error", txErr,
						"lookup_error", gErr,
					)
					return results, txErr
				}
				dupEventID = existing.ID
			}
			results = append(results, source.TargetResult{
				ProjectID: tgt.ProjectID,
				EventID:   dupEventID,
				Status:    source.TargetStatusDuplicate,
			})
			continue
		}

		if anyQueued {
			queue.Wake(trigger.EventQueueKind)
		}
		slog.Error("Failed to persist GitHub trigger event",
			"project_id", tgt.ProjectID,
			"installation_id", ev.SourceID,
			"delivery_id", ev.DeliveryID,
			"error", txErr,
		)
		return results, txErr
	}

	if anyQueued {
		queue.Wake(trigger.EventQueueKind)
	}

	return results, nil
}

type githubRouteLimiter struct {
	st *githubWebhookIngressState
}

func (l githubRouteLimiter) Allow(now time.Time) bool {
	if l.st.allowVerified(now) {
		return true
	}
	l.st.rateLimitedCount.Add(1)
	return false
}

type githubRouteAudit struct {
	st *githubWebhookIngressState
}

func (a githubRouteAudit) RecordReceipt(ctx context.Context, rec source.ReceiptAuditRecord) {
	var instID int64
	if rec.InstallationID != "" {
		if parsed, err := strconv.ParseInt(rec.InstallationID, 10, 64); err == nil && parsed > 0 {
			instID = parsed
		}
	}
	var events []GitHubWebhookProjectEvent
	if len(rec.Events) > 0 {
		events = make([]GitHubWebhookProjectEvent, len(rec.Events))
		for i, e := range rec.Events {
			events[i] = GitHubWebhookProjectEvent{
				ProjectID: e.ProjectID,
				EventID:   e.EventID,
				Status:    e.Status,
			}
		}
	}
	var skipped []GitHubWebhookSkippedProject
	if len(rec.SkippedProjects) > 0 {
		skipped = make([]GitHubWebhookSkippedProject, len(rec.SkippedProjects))
		for i, sp := range rec.SkippedProjects {
			skipped[i] = GitHubWebhookSkippedProject{
				ProjectID: sp.ProjectID,
				Reason:    sp.Reason,
			}
		}
	}
	a.st.recordReceiptAuditWithKind(ctx, rec.Kind, githubReceiptAuditRecord{
		Delivery:           rec.Delivery,
		Event:              rec.Event,
		Action:             rec.Action,
		InstallationID:     instID,
		Repo:               rec.Resource,
		PayloadBytes:       rec.PayloadBytes,
		Result:             rec.Result,
		Routing:            rec.Routing,
		BypassedProjectIDs: rec.BypassedProjectIDs,
		ProjectIDs:         rec.ProjectIDs,
		Events:             events,
		SkippedProjects:    skipped,
	})
}

func (a githubRouteAudit) RecordTargetMismatch(ctx context.Context, now time.Time) {
	a.st.recordTargetMismatchAudit(ctx, now)
}

func (a githubRouteAudit) RecordSelfIgnored(_ context.Context) {
	a.st.selfIgnoredCount.Add(1)
}

func (s *Server) githubWebhookSecretSource() source.SecretSource {
	return source.SecretFunc(func(ctx context.Context) (string, error) {
		s.mu.RLock()
		webhookSecret := s.config.GitHubAppConfig.WebhookSecret
		s.mu.RUnlock()
		if webhookSecret != "" {
			return webhookSecret, nil
		}
		sec, err := s.loadGitHubAppSecret(ctx, GitHubAppSecretWebhookSecret)
		if err == nil {
			return sec, nil
		}
		if !errors.Is(err, store.ErrNotFound) {
			slog.Warn("GitHub webhook secret lookup failed", "error", err)
		}
		return "", nil
	})
}

// githubSourceRouteDeps builds the provider-neutral source.RouteDeps for GitHub
// App webhooks from the Server's GitHubSource*() seam accessors.
func (s *Server) githubSourceRouteDeps() source.RouteDeps {
	return source.RouteDeps{
		Kind:       store.TriggerSourceKindGitHub,
		Verifier:   s.GitHubSourceWireVerifier(),
		Resolver:   s.GitHubSourceInstallationResolver(),
		Normalizer: s.GitHubSourceNormalizer(),
		Actor:      s.GitHubSourceActorIdentity(),
		Secrets:    s.githubWebhookSecretSource(),
		Sink:       &sourceEventSink{srv: s},
		Limiter:    githubRouteLimiter{st: &s.githubWebhookIngress},
		Audit:      githubRouteAudit{st: &s.githubWebhookIngress},
		MaxBody:    source.DefaultMaxBodyBytes,
		Now:        s.githubWebhookIngress.now,
		PingEvent:  "ping",
		ContentEnabled: func(context.Context) bool {
			return s.githubTriggersEnabled()
		},
		OnSecretNotConfigured: func() {
			s.githubWebhookNoSecretWarnOnce.Do(func() {
				slog.Warn("GitHub webhook received but no webhook secret is configured; rejecting")
			})
		},
		SecretNotConfiguredMessage: "GitHub webhook is not configured",
		PersistErrorMessage:        "failed to persist github trigger event",
		WriteError: func(w http.ResponseWriter, status int, code, message string) {
			writeError(w, status, code, message, nil)
		},
	}
}

func (s *Server) listGitHubInstallationCandidateProjects(ctx context.Context, installationID int64, repoFullName string) ([]store.Project, error) {
	var (
		candidates []store.Project
		cursor     string
	)
	for {
		page, err := s.store.ListProjects(ctx, store.ProjectFilter{
			GitHubInstallationID: &installationID,
		}, store.ListOptions{
			Limit:          githubCandidateProjectsPageSize,
			Cursor:         cursor,
			SkipTotalCount: true,
		})
		if err != nil {
			return nil, err
		}
		for i := range page.Items {
			proj := page.Items[i]
			if s.IsGitHubTriggerCandidate(installationID, &proj, repoFullName) {
				candidates = append(candidates, proj)
			}
		}
		if page.NextCursor == "" || len(page.Items) == 0 {
			return candidates, nil
		}
		cursor = page.NextCursor
	}
}

func sanitizeGitHubEventHeaders(in map[string]string) map[string]string {
	if len(in) == 0 {
		return map[string]string{}
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		maxBytes := maxGitHubStoredHeaderBytes
		if strings.EqualFold(k, "User-Agent") {
			maxBytes = maxGitHubStoredUserAgentBytes
		}
		out[k] = sanitizeGitHubHeaderValue(v, maxBytes)
	}
	return out
}

func sanitizeGitHubHeaderValue(val string, maxBytes int) string {
	val = strings.ToValidUTF8(val, "")
	val = strings.ReplaceAll(val, "\x00", "")
	if len(val) <= maxBytes {
		return val
	}
	for maxBytes > 0 && !utf8.RuneStart(val[maxBytes]) {
		maxBytes--
	}
	return val[:maxBytes]
}

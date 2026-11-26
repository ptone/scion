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

//go:build !no_sqlite

package hub

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/GoogleCloudPlatform/scion/pkg/hub/trigger/source"
	"github.com/GoogleCloudPlatform/scion/pkg/store"
)

// handleGitHubContentWebhook preserves the test-only entry point exercised by
// TestServerGitHubSourceSeams_WireVerifierParity while delegating through the
// production source.RouteDeps pipeline.
func (s *Server) handleGitHubContentWebhook(w http.ResponseWriter, r *http.Request, _, _ string, body []byte) {
	r2 := r.Clone(r.Context())
	r2.Body = io.NopCloser(bytes.NewReader(body))
	s.githubSourceRouteDeps().ServeHTTP(w, r2)
}

type spyWireVerifier struct {
	inner source.WireVerifier
	calls *atomic.Int64
}

func (s *spyWireVerifier) Verify(r *http.Request, body []byte, secrets source.SecretSource) (source.Delivery, error) {
	s.calls.Add(1)
	return s.inner.Verify(r, body, secrets)
}

func (s *spyWireVerifier) DedupKey(d source.Delivery, body []byte) string {
	return s.inner.DedupKey(d, body)
}

type spyInstallationResolver struct {
	inner          source.InstallationResolver
	lifecycleCalls *atomic.Int64
	instCalls      *atomic.Int64
	targetsCalls   *atomic.Int64
}

func (s *spyInstallationResolver) Installation(ctx context.Context, d source.Delivery, payload any) (source.Installation, error) {
	s.instCalls.Add(1)
	return s.inner.Installation(ctx, d, payload)
}

func (s *spyInstallationResolver) Targets(ctx context.Context, inst source.Installation, payload any) ([]source.Target, error) {
	s.targetsCalls.Add(1)
	return s.inner.Targets(ctx, inst, payload)
}

func (s *spyInstallationResolver) OnLifecycle(ctx context.Context, d source.Delivery, payload any) (bool, error) {
	s.lifecycleCalls.Add(1)
	return s.inner.OnLifecycle(ctx, d, payload)
}

type spyNormalizer struct {
	inner source.Normalizer
	calls *atomic.Int64
}

func (s *spyNormalizer) Normalize(ctx context.Context, d source.Delivery, payload any, t source.Target) (source.Event, error) {
	s.calls.Add(1)
	return s.inner.Normalize(ctx, d, payload, t)
}

func (s *spyNormalizer) Finalize(ctx context.Context, d source.Delivery, payload any, ev source.Event) (source.Event, error) {
	if fin, ok := s.inner.(source.FinalizingNormalizer); ok {
		return fin.Finalize(ctx, d, payload, ev)
	}
	return ev, nil
}

type spyActorIdentity struct {
	inner source.ActorIdentity
	calls *atomic.Int64
}

func (s *spyActorIdentity) IsSelf(ctx context.Context, sender source.Principal) bool {
	s.calls.Add(1)
	return s.inner.IsSelf(ctx, sender)
}

// TestGitHubWebhookIngress_RoutesThroughSeamAccessors proves that production
// handleGitHubWebhook routes both content and lifecycle deliveries through the
// Server's GitHubSource*() seam accessors rather than calling package-private
// GitHub helpers directly.
func TestGitHubWebhookIngress_RoutesThroughSeamAccessors(t *testing.T) {
	f := setupGitHubIngressFixture(t)
	ctx := context.Background()
	proj := f.createCandidateProject(t, "seam-wire-proj", "https://github.com/acme/widgets.git", f.instID)

	var (
		verifyCalls    atomic.Int64
		lifecycleCalls atomic.Int64
		instCalls      atomic.Int64
		targetsCalls   atomic.Int64
		normalizeCalls atomic.Int64
		actorCalls     atomic.Int64
	)

	f.srv.githubWebhookIngress.mu.Lock()
	f.srv.githubWebhookIngress.wrapWireVerifier = func(v source.WireVerifier) source.WireVerifier {
		return &spyWireVerifier{inner: v, calls: &verifyCalls}
	}
	f.srv.githubWebhookIngress.wrapResolver = func(r source.InstallationResolver) source.InstallationResolver {
		return &spyInstallationResolver{
			inner:          r,
			lifecycleCalls: &lifecycleCalls,
			instCalls:      &instCalls,
			targetsCalls:   &targetsCalls,
		}
	}
	f.srv.githubWebhookIngress.wrapNormalizer = func(n source.Normalizer) source.Normalizer {
		return &spyNormalizer{inner: n, calls: &normalizeCalls}
	}
	f.srv.githubWebhookIngress.wrapActor = func(a source.ActorIdentity) source.ActorIdentity {
		return &spyActorIdentity{inner: a, calls: &actorCalls}
	}
	f.srv.githubWebhookIngress.mu.Unlock()

	resetCounts := func() {
		verifyCalls.Store(0)
		lifecycleCalls.Store(0)
		instCalls.Store(0)
		targetsCalls.Store(0)
		normalizeCalls.Store(0)
		actorCalls.Store(0)
	}

	t.Run("content delivery traverses all four seam accessors", func(t *testing.T) {
		resetCounts()
		deliv := uuid.NewString()
		body := sampleIssueCommentPayload(t, f.instID, "acme/widgets", "alice", "User", "@scion-bot /review", f.currentTime())
		rec := f.postDelivery(t, "issue_comment", deliv, body, nil)
		require.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())

		var accepted GitHubWebhookAcceptedResponse
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &accepted))
		assert.Equal(t, GitHubWebhookStatusQueued, accepted.Status)
		require.Len(t, accepted.Events, 1)
		assert.Equal(t, proj.ID, accepted.Events[0].ProjectID)

		assert.Equal(t, int64(1), verifyCalls.Load(), "GitHubSourceWireVerifier.Verify must be called")
		assert.Equal(t, int64(1), lifecycleCalls.Load(), "GitHubSourceInstallationResolver.OnLifecycle must be called")
		assert.Equal(t, int64(1), normalizeCalls.Load(), "GitHubSourceNormalizer.Normalize must be called")
		assert.Equal(t, int64(1), instCalls.Load(), "GitHubSourceInstallationResolver.Installation must be called")
		assert.Equal(t, int64(1), actorCalls.Load(), "GitHubSourceActorIdentity.IsSelf must be called")
		assert.Equal(t, int64(1), targetsCalls.Load(), "GitHubSourceInstallationResolver.Targets must be called")
	})

	t.Run("installation lifecycle delivery routes through Verifier and OnLifecycle only", func(t *testing.T) {
		resetCounts()
		instBody, err := json.Marshal(map[string]any{
			"action": "created",
			"installation": map[string]any{
				"id":                   int64(776655),
				"app_id":               f.appID,
				"repository_selection": "selected",
				"account":              map[string]any{"login": "seam-org", "type": "Organization"},
			},
			"repositories": []map[string]any{
				{"id": 1, "full_name": "seam-org/app", "name": "app"},
			},
		})
		require.NoError(t, err)

		rec := f.postDelivery(t, "installation", uuid.NewString(), instBody, nil)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		assert.JSONEq(t, `{"status":"ok"}`, rec.Body.String())

		assert.Equal(t, int64(1), verifyCalls.Load())
		assert.Equal(t, int64(1), lifecycleCalls.Load())
		assert.Equal(t, int64(0), normalizeCalls.Load())
		assert.Equal(t, int64(0), instCalls.Load())
		assert.Equal(t, int64(0), actorCalls.Load())
		assert.Equal(t, int64(0), targetsCalls.Load())

		gotInst, err := f.store.GetGitHubInstallation(ctx, 776655)
		require.NoError(t, err)
		assert.Equal(t, store.GitHubInstallationStatusActive, gotInst.Status)
	})

	t.Run("ping delivery routes through Verifier and OnLifecycle and returns pong", func(t *testing.T) {
		resetCounts()
		rec := f.postDelivery(t, "ping", uuid.NewString(), []byte(`{"zen":"ok"}`), nil)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		assert.JSONEq(t, `{"status":"pong"}`, rec.Body.String())

		assert.Equal(t, int64(1), verifyCalls.Load())
		assert.Equal(t, int64(1), lifecycleCalls.Load())
		assert.Equal(t, int64(0), normalizeCalls.Load())
	})
}

// TestSourceEventSink_UsesConfiguredKind verifies that sourceEventSink persists
// and deduplicates events using ev.SourceKind (set from RouteDeps.Kind) rather
// than a hard-coded GitHub constant.
func TestSourceEventSink_UsesConfiguredKind(t *testing.T) {
	f := setupGitHubIngressFixture(t)
	ctx := context.Background()
	proj := f.createCandidateProject(t, "sink-kind-proj", "https://github.com/acme/widgets.git", f.instID)

	sink := &sourceEventSink{srv: f.srv}
	now := time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)
	ev := source.Event{
		Handled:      true,
		SourceKind:   "jira",
		SourceID:     "site-1",
		DeliveryID:   "jira-deliv-1",
		Type:         "jira.issue.updated",
		Subject:      "jira:PROJ-42",
		OccurredAt:   now,
		ReceivedAt:   now,
		Attributes:   map[string]string{"provider": "jira"},
		Headers:      map[string]string{"User-Agent": "Jira/1.0"},
		Payload:      []byte(`{"issue":"PROJ-42"}`),
		PayloadBytes: len(`{"issue":"PROJ-42"}`),
	}
	targets := []source.Target{{ProjectID: proj.ID, InstallationID: "site-1", Resource: "PROJ"}}

	res1, err := sink.EnqueueForTargets(ctx, ev, "bodysha-jira-1", targets)
	require.NoError(t, err)
	require.Len(t, res1, 1)
	assert.Equal(t, source.TargetStatusQueued, res1[0].Status)

	stored, err := f.store.GetTriggerEventByDedupKey(ctx, "jira", "site-1", "jira-deliv-1/"+proj.ID)
	require.NoError(t, err)
	assert.Equal(t, "jira", stored.SourceKind)
	assert.Equal(t, res1[0].EventID, stored.ID)

	// Redelivery with same body SHA-256 deduplicates under "jira".
	ev.DeliveryID = "jira-deliv-2"
	res2, err := sink.EnqueueForTargets(ctx, ev, "bodysha-jira-1", targets)
	require.NoError(t, err)
	require.Len(t, res2, 1)
	assert.Equal(t, source.TargetStatusDuplicate, res2[0].Status)
	assert.Equal(t, res1[0].EventID, res2[0].EventID)
}

// TestGitHubWebhookIngress_ErrorPathLogsViaHubAPIError proves that rejected
// deliveries flow through the hub's writeError/apierr.WriteError path so
// rate-limited 503s emit ERROR "API Error" logs and 400s emit INFO "API client error" logs.
func TestGitHubWebhookIngress_ErrorPathLogsViaHubAPIError(t *testing.T) {
	f := setupGitHubIngressFixture(t)
	f.createCandidateProject(t, "errlog-proj", "https://github.com/acme/widgets.git", f.instID)

	logBuf := &ghIngressLogBuffer{}
	prevLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(logBuf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	t.Cleanup(func() { slog.SetDefault(prevLogger) })

	// 1. 400 invalid payload emits INFO "API client error".
	logBuf.Reset()
	rec400 := f.postDelivery(t, "issue_comment", uuid.NewString(), []byte(`{"action":"created"}`), nil)
	require.Equal(t, http.StatusBadRequest, rec400.Code, rec400.Body.String())
	logs400 := logBuf.String()
	assert.Contains(t, logs400, `level=INFO`)
	assert.Contains(t, logs400, `msg="API client error"`)
	assert.Contains(t, logs400, `status=400`)
	assert.Contains(t, logs400, `invalid payload`)

	// 2. Drained verified bucket emits 503 + ERROR "API Error".
	f.srv.githubWebhookIngress.mu.Lock()
	f.srv.githubWebhookIngress.bucketInit = true
	f.srv.githubWebhookIngress.bucketTokens = 0
	f.srv.githubWebhookIngress.bucketRate = 0.0001
	f.srv.githubWebhookIngress.bucketBurst = 1
	f.srv.githubWebhookIngress.bucketLastCheck = f.currentTime()
	f.srv.githubWebhookIngress.mu.Unlock()

	logBuf.Reset()
	body := sampleIssueCommentPayload(t, f.instID, "acme/widgets", "alice", "User", "hello", f.currentTime())
	rec503 := f.postDelivery(t, "issue_comment", uuid.NewString(), body, nil)
	require.Equal(t, http.StatusServiceUnavailable, rec503.Code, rec503.Body.String())
	logs503 := logBuf.String()
	assert.Contains(t, logs503, `level=ERROR`)
	assert.Contains(t, logs503, `msg="API Error"`)
	assert.Contains(t, logs503, `status=503`)
	assert.Contains(t, logs503, `rate limit exceeded`)
}

// TestGitHubWebhookIngress_DuplicateCaseKeysUseNormalizedTenancy proves that
// installation lookup and target fan-out use the single Normalize parse rather
// than a case-insensitive struct re-decode of the raw body.
func TestGitHubWebhookIngress_DuplicateCaseKeysUseNormalizedTenancy(t *testing.T) {
	f := setupGitHubIngressFixture(t)
	ctx := context.Background()

	const otherInstID int64 = 998877
	require.NoError(t, f.store.CreateGitHubInstallation(ctx, &store.GitHubInstallation{
		InstallationID: otherInstID,
		AccountLogin:   "other",
		AccountType:    "Organization",
		AppID:          f.appID,
		Status:         store.GitHubInstallationStatusActive,
	}))

	projA := f.createCandidateProject(t, "case-proj-a", "https://github.com/acme/widgets.git", f.instID)
	projB := f.createCandidateProject(t, "case-proj-b", "https://github.com/other/repo.git", otherInstID)

	// Signed body carrying both exact lowercase keys ("installation", "repository")
	// and differently-cased keys ("Installation", "Repository") that a second
	// encoding/json struct decode would overwrite.
	body := []byte(fmt.Sprintf(`{
		"action": "created",
		"installation": {"id": %d},
		"Installation": {"id": %d},
		"repository": {
			"id": 101,
			"name": "widgets",
			"full_name": "acme/widgets",
			"private": true,
			"default_branch": "main",
			"owner": {"login": "acme", "id": 10, "type": "Organization"}
		},
		"Repository": {
			"full_name": "other/repo"
		},
		"sender": {"login": "alice", "id": 202, "type": "User"},
		"issue": {
			"number": 42,
			"state": "open",
			"title": "Case test",
			"html_url": "https://github.com/acme/widgets/issues/42",
			"user": {"login": "alice", "id": 202, "type": "User"}
		},
		"comment": {
			"id": 9001,
			"body": "hello",
			"author_association": "MEMBER",
			"html_url": "https://github.com/acme/widgets/issues/42#issuecomment-9001",
			"created_at": "2026-11-18T12:00:00Z",
			"updated_at": "2026-11-18T12:00:00Z",
			"user": {"login": "alice", "id": 202, "type": "User"}
		}
	}`, f.instID, otherInstID))

	deliv := uuid.NewString()
	rec := f.postDelivery(t, "issue_comment", deliv, body, nil)
	require.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())

	var accepted GitHubWebhookAcceptedResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &accepted))
	require.Equal(t, GitHubWebhookStatusQueued, accepted.Status)
	require.Len(t, accepted.Events, 1)
	assert.Equal(t, projA.ID, accepted.Events[0].ProjectID, "must route using normalized lowercase keys, not case-folded struct decode")

	evsA, err := f.store.ListTriggerEvents(ctx, store.TriggerEventFilter{ProjectID: projA.ID}, store.ListOptions{})
	require.NoError(t, err)
	require.Len(t, evsA.Items, 1)
	assert.Equal(t, strconv.FormatInt(f.instID, 10), evsA.Items[0].SourceID)

	evsB, err := f.store.ListTriggerEvents(ctx, store.TriggerEventFilter{ProjectID: projB.ID}, store.ListOptions{})
	require.NoError(t, err)
	assert.Empty(t, evsB.Items)
}

// TestGitHubWebhookIngress_ReceivedLogBeforeTargetRejectionAndPreservesDeliveryCase
// verifies that a signature-verified delivery logs "Source webhook received"
// with original delivery_id casing even when target validation subsequently rejects it.
func TestGitHubWebhookIngress_ReceivedLogBeforeTargetRejectionAndPreservesDeliveryCase(t *testing.T) {
	f := setupGitHubIngressFixture(t)

	logBuf := &ghIngressLogBuffer{}
	prevLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(logBuf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	t.Cleanup(func() { slog.SetDefault(prevLogger) })

	upperDeliv := strings.ToUpper(uuid.NewString())
	body := sampleIssueCommentPayload(t, f.instID, "acme/widgets", "alice", "User", "hello", f.currentTime())
	rec := f.postDelivery(t, "issue_comment", upperDeliv, body, func(r *http.Request) {
		r.Header.Set("X-GitHub-Hook-Installation-Target-ID", "999999")
	})
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())

	logs := logBuf.String()
	assert.Contains(t, logs, `msg="Source webhook received"`)
	assert.Contains(t, logs, `source_kind=github`)
	assert.Contains(t, logs, `delivery_id=`+upperDeliv)
}

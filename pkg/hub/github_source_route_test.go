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
	"io"
	"net/http"
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
// and deduplicates events using its configured source kind rather than a
// hard-coded GitHub constant.
func TestSourceEventSink_UsesConfiguredKind(t *testing.T) {
	f := setupGitHubIngressFixture(t)
	ctx := context.Background()
	proj := f.createCandidateProject(t, "sink-kind-proj", "https://github.com/acme/widgets.git", f.instID)

	sink := &sourceEventSink{srv: f.srv, kind: "jira"}
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

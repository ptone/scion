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
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/GoogleCloudPlatform/scion/pkg/agent/state"
	"github.com/GoogleCloudPlatform/scion/pkg/store"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// Regression tests for the seven guards converted in handlers_logs.go.
//
// Every one had the #591 shape — CheckAccess reached only when
// GetUserIdentityFromContext returned non-nil — so the check was skipped in
// silence for agent and broker callers while all the user-path tests passed.
// These tests exercise the identity kinds the old guard could not see.
//
// Reachability, since the claim is that this closes a hole rather than a latent
// one: UnifiedAuthMiddleware validates agent tokens with no path allowlist, and
// BrokerAuthMiddleware establishes a broker identity for any path once the
// signature verifies. Both kinds genuinely reach these handlers with
// credentials the hub itself issues.
//
// Test naming: everything file-local is prefixed logsAuthz.

type logsAuthzFixture struct {
	srv    *Server
	store  store.Store
	owner  *store.User
	outsdr *store.User
	proj   *store.Project
	target *store.Agent
	peer   *store.Agent

	broker       *store.RuntimeBroker
	brokerSecret []byte
}

func logsAuthzSetup(t *testing.T) *logsAuthzFixture {
	t.Helper()

	s, err := newTestStore(":memory:")
	if err != nil {
		t.Skipf("skipping: test store unavailable (%v)", err)
	}
	require.NoError(t, s.Migrate(context.Background()))

	cfg := DefaultServerConfig()
	cfg.DevAuthToken = testDevToken
	cfg.BrokerAuthConfig = DefaultBrokerAuthConfig()
	srv, err := New(cfg, s)
	require.NoError(t, err)
	srv.SetHubID("test-hub-id")
	t.Cleanup(func() { _ = srv.Shutdown(context.Background()) })

	// Rule 9: never rely on the ambient value. With GOOGLE_CLOUD_PROJECT set,
	// the server builds a real Cloud Logging client, so a test written against
	// the nil -> 501 path passes in CI and does something else in a developer
	// container. A zero-value service reaches the guards without touching the
	// embedded clients, which is all these denial tests need.
	srv.logQueryService = &LogQueryService{}

	ctx := context.Background()
	f := &logsAuthzFixture{srv: srv, store: s}

	f.owner = &store.User{
		ID: tid("logsauthz-owner"), Email: "logsauthz-owner@example.com",
		DisplayName: "Owner", Role: store.UserRoleMember, Status: "active",
		Created: time.Now(),
	}
	require.NoError(t, s.CreateUser(ctx, f.owner))

	// A member with no relationship to the project at all.
	f.outsdr = &store.User{
		ID: tid("logsauthz-outsider"), Email: "logsauthz-outsider@example.com",
		DisplayName: "Outsider", Role: store.UserRoleMember, Status: "active",
		Created: time.Now(),
	}
	require.NoError(t, s.CreateUser(ctx, f.outsdr))

	f.proj = &store.Project{
		ID: tid("logsauthz-p"), Name: "Logs Authz P", Slug: "logsauthz-p",
		OwnerID: f.owner.ID,
	}
	require.NoError(t, s.CreateProject(ctx, f.proj))

	f.brokerSecret = []byte("logsauthz-secret-key-32-bytes!!!")
	f.broker = &store.RuntimeBroker{
		ID: uuid.New().String(), Name: "logsauthz-broker", Slug: "logsauthz-broker",
		Status: store.BrokerStatusOnline, AutoProvide: true,
		Created: time.Now(), Updated: time.Now(),
	}
	require.NoError(t, s.CreateRuntimeBroker(ctx, f.broker))
	require.NoError(t, s.CreateBrokerSecret(ctx, &store.BrokerSecret{
		BrokerID:  f.broker.ID,
		SecretKey: f.brokerSecret,
		Algorithm: store.BrokerSecretAlgorithmHMACSHA256,
		Status:    store.BrokerSecretStatusActive,
	}))

	mk := func(name string) *store.Agent {
		a := &store.Agent{
			ID: tid(name), Slug: tid(name), Name: name,
			ProjectID: f.proj.ID, Phase: string(state.PhaseRunning),
			CreatedBy: f.owner.ID, OwnerID: f.owner.ID,
			Ancestry: []string{f.owner.ID},
		}
		require.NoError(t, s.CreateAgent(ctx, a))
		return a
	}
	f.target = mk("logsauthz-target")
	f.peer = mk("logsauthz-peer")

	return f
}

// paths returns every endpoint served by the seven converted guards.
func (f *logsAuthzFixture) paths() []struct{ name, path string } {
	return []struct{ name, path string }{
		{"agent logs", "/api/v1/agents/" + f.target.ID + "/logs"},
		{"agent cloud logs", "/api/v1/agents/" + f.target.ID + "/cloud-logs"},
		{"agent cloud logs stream", "/api/v1/agents/" + f.target.ID + "/cloud-logs/stream"},
		{"agent message logs", "/api/v1/agents/" + f.target.ID + "/message-logs"},
		{"agent message logs stream", "/api/v1/agents/" + f.target.ID + "/message-logs/stream"},
		{"project message logs", "/api/v1/projects/" + f.proj.ID + "/message-logs"},
		{"project message logs stream", "/api/v1/projects/" + f.proj.ID + "/message-logs/stream"},
	}
}

// asBroker issues an HMAC-signed broker request. brokerIdentityImpl implements
// neither UserIdentity nor AgentIdentity, so before the conversion a broker
// passed through every one of these guards untouched.
func (f *logsAuthzFixture) asBroker(t *testing.T, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, bytes.NewReader(nil))
	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	nonce := "logsauthz-nonce-" + uuid.New().String()
	req.Header.Set(HeaderBrokerID, f.broker.ID)
	req.Header.Set(HeaderTimestamp, timestamp)
	req.Header.Set(HeaderNonce, nonce)

	svc := f.srv.brokerAuthService
	require.NotNil(t, svc, "broker auth service must be configured")
	mac := hmac.New(sha256.New, f.brokerSecret)
	mac.Write(svc.buildCanonicalString(req, timestamp, nonce))
	req.Header.Set(HeaderSignature, base64.StdEncoding.EncodeToString(mac.Sum(nil)))

	rec := httptest.NewRecorder()
	f.srv.Handler().ServeHTTP(rec, req)
	return rec
}

// asUser issues a request bearing a real access token for the given user.
//
// Injecting an identity into the request context does not work here: these
// requests go through srv.Handler(), so UnifiedAuthMiddleware runs and
// establishes the identity itself, and a context-injected one is ignored. That
// is standing rule 4 — the test must supply the credential the middleware would
// genuinely have produced rather than the assertion being relaxed to match.
func (f *logsAuthzFixture) asUser(t *testing.T, u *store.User, path string) *httptest.ResponseRecorder {
	t.Helper()
	svc := f.srv.GetUserTokenService()
	require.NotNil(t, svc)
	tok, _, err := svc.GenerateAccessToken(u.ID, u.Email, u.DisplayName,
		string(u.Role), ClientTypeAPI)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	rec := httptest.NewRecorder()
	f.srv.Handler().ServeHTTP(rec, req)
	return rec
}

// TestLogsAuthz_BrokerDenied is the core #591 regression for this file. A
// validly-signed broker reaches every one of these handlers, and before the
// conversion the authorization guard did not apply to it.
func TestLogsAuthz_BrokerDenied(t *testing.T) {
	f := logsAuthzSetup(t)

	for _, tc := range f.paths() {
		t.Run(tc.name, func(t *testing.T) {
			rec := f.asBroker(t, tc.path)
			require.Equal(t, http.StatusForbidden, rec.Code,
				"a broker identity must be denied; before conversion this guard "+
					"was skipped entirely for callers that are not users")
		})
	}
}

// TestLogsAuthz_UnauthenticatedDenied pins that no converted site can be
// reached without an identity. The helper answers 401 rather than 403 here, so
// this also documents that the conversion did not collapse the two.
func TestLogsAuthz_UnauthenticatedDenied(t *testing.T) {
	f := logsAuthzSetup(t)

	for _, tc := range f.paths() {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tc.path, nil)
			rec := httptest.NewRecorder()
			f.srv.Handler().ServeHTTP(rec, req)
			require.Equal(t, http.StatusUnauthorized, rec.Code)
		})
	}
}

// TestLogsAuthz_UnrelatedUserDenied is the user-path control. It passed before
// the conversion too — that is the point. The user path was always checked, and
// its continuing to pass is what let the bypass survive, so it is pinned here
// to show the conversion did not weaken it.
func TestLogsAuthz_UnrelatedUserDenied(t *testing.T) {
	f := logsAuthzSetup(t)

	for _, tc := range f.paths() {
		t.Run(tc.name, func(t *testing.T) {
			rec := f.asUser(t, f.outsdr, tc.path)
			require.Equal(t, http.StatusForbidden, rec.Code)
		})
	}
}

// TestLogsAuthz_SameProjectAgentStillReads is the positive control, and it is
// deliberately narrow.
//
// It uses the one endpoint of the seven that does not depend on Cloud Logging:
// handleAgentLogs relays through the dispatcher, so an allowed caller reaches
// the "no dispatcher configured" 501 instead of dereferencing a nil log client.
// That 501 is proof the caller cleared both the isolation check and
// authorization. The other six cannot be positively tested at handler level
// with a zero-value LogQueryService — an allowed caller panics in the Cloud
// Logging client — so their allow paths are covered at the authz layer.
//
// Without this test the suite would be satisfied by a guard that denies
// everyone, which is the failure mode a security change is most likely to ship.
func TestLogsAuthz_SameProjectAgentStillReads(t *testing.T) {
	f := logsAuthzSetup(t)

	svc := f.srv.GetAgentTokenService()
	require.NotNil(t, svc)
	// peer and target are both in the project; the project-scoped read baseline
	// is what admits this caller.
	tok, err := svc.GenerateAgentToken(f.peer.ID, f.peer.ProjectID, nil, nil)
	require.NoError(t, err)

	rec := doRequestWithAgentToken(t, f.srv, http.MethodGet,
		"/api/v1/agents/"+f.target.ID+"/logs", nil, tok)

	require.Equal(t, http.StatusNotImplemented, rec.Code,
		"a same-project agent must clear isolation and authorization and reach "+
			"the dispatcher; 403 here would mean the conversion over-tightened "+
			"the read path, 404 would mean isolation denies on identity kind")
}

// TestLogsAuthz_ProjectOwnerStillReads is the user-side positive control, same
// endpoint and same reasoning.
func TestLogsAuthz_ProjectOwnerStillReads(t *testing.T) {
	f := logsAuthzSetup(t)

	rec := f.asUser(t, f.owner, "/api/v1/agents/"+f.target.ID+"/logs")

	require.Equal(t, http.StatusNotImplemented, rec.Code,
		"the project owner must still be able to read agent logs")
}

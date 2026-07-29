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
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-jose/go-jose/v4/jwt"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestBrokerAuthz_AutoProvideDispatch_GateLiveness pins the #591 close of the
// AutoProvide dispatch bypass. An AutoProvide broker is shared infrastructure
// declared available-to-all, but "available-to-all" means available to every
// AUTHORIZED caller, not to every caller: AutoProvide relaxes only the
// project-linkage requirement, never identity-type and never scope.
//
// It drives BOTH dispatch twins against a real store-backed AutoProvide broker
// (canDispatchToBroker, which writes no response, and checkBrokerDispatchAccess,
// which writes an HTTP response) and asserts the same decision from each:
//   - DENY: nil identity, a broker-typed identity, an agent WITHOUT ScopeAgentCreate
//   - ALLOW: an authenticated non-owner user (the available-to-all user positive),
//     and a cross-project agent WITH ScopeAgentCreate (the available-to-all agent
//     positive; linkage is skipped, scope is not).
//
// RED-on-revert (task 27 acceptance bar): restoring the pre-fix early-return
// `if broker.AutoProvide { return true }` ahead of the switch makes the
// broker-typed and agent-without-scope arms bypass (both flip to allowed);
// dropping the ScopeAgentCreate check alone flips only the unscoped-agent arm;
// making the default case return true flips only the broker-typed arm. The two
// positives stay GREEN throughout.
func TestBrokerAuthz_AutoProvideDispatch_GateLiveness(t *testing.T) {
	srv, s, _, bob, _, _, broker := setupBrokerAuthzTest(t)
	base := context.Background()

	// Declare the broker available-to-all. It serves only alice's project, so the
	// cross-project agent positive below cannot be passing on linkage.
	broker.AutoProvide = true
	require.NoError(t, s.UpdateRuntimeBroker(base, broker))

	// available-to-all positives
	nonOwnerUser := NewAuthenticatedUser(bob.ID, bob.Email, bob.DisplayName, string(bob.Role), string(ClientTypeWeb))
	scopedCrossProjectAgent := &agentIdentityWrapper{&AgentTokenClaims{
		Claims:    jwt.Claims{Subject: tid("agent-scoped-crossproject")},
		ProjectID: tid("unrelated-project"),
		Scopes:    []AgentTokenScope{ScopeAgentCreate},
	}}

	// deny arms
	unscopedAgent := &agentIdentityWrapper{&AgentTokenClaims{
		Claims:    jwt.Claims{Subject: tid("agent-unscoped")},
		ProjectID: tid("unrelated-project"),
	}}
	brokerTyped := NewBrokerIdentity(tid("some-other-broker"))

	cases := []struct {
		name     string
		identity Identity // nil => unauthenticated
		want     bool
	}{
		{"nil_identity_denied", nil, false},
		{"broker_typed_denied", brokerTyped, false},
		{"agent_without_scope_denied", unscopedAgent, false},
		{"non_owner_user_allowed", nonOwnerUser, true},
		{"cross_project_scoped_agent_allowed", scopedCrossProjectAgent, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := base
			if tc.identity != nil {
				ctx = contextWithIdentity(base, tc.identity)
			}

			// Twin 1: canDispatchToBroker (no HTTP response).
			assert.Equalf(t, tc.want, srv.canDispatchToBroker(ctx, broker),
				"canDispatchToBroker decision mismatch for %s", tc.name)

			// Twin 2: checkBrokerDispatchAccess (writes the HTTP response).
			rec := httptest.NewRecorder()
			got := srv.checkBrokerDispatchAccess(ctx, rec, broker.ID)
			assert.Equalf(t, tc.want, got,
				"checkBrokerDispatchAccess decision mismatch for %s", tc.name)
			if !tc.want {
				assert.GreaterOrEqualf(t, rec.Code, http.StatusBadRequest,
					"denied caller %s must receive a 4xx, got %d", tc.name, rec.Code)
			}
		})
	}
}

// TestBrokerAuthz_Reregistration_OwnerOnly pins the #591 re-registration
// ownership close. Re-registration overwrites broker settings (AutoProvide) and
// mints a fresh join token, so where an owner is recorded only that owner may do
// it. It drives the real POST /api/v1/brokers route and store-verifies the WRITE.
//
// RED-on-revert (task 27 acceptance bar): removing the recorded-owner check in
// CreateBrokerRegistration lets the non-owner attempt succeed (201) and flip
// AutoProvide in the store, reddening the 403 and the no-mutation assertions.
func TestBrokerAuthz_Reregistration_OwnerOnly(t *testing.T) {
	srv, s, alice, bob, _, _, _ := setupBrokerAuthzTest(t)
	ctx := context.Background()

	// Alice registers a broker through the real route, so CreatedBy == alice.
	rec := doRequestAsUser(t, srv, alice, http.MethodPost, "/api/v1/brokers",
		CreateBrokerRegistrationRequest{Name: "owned-shared-broker", AutoProvide: false})
	require.Equal(t, http.StatusCreated, rec.Code, "owner initial registration: %s", rec.Body.String())
	var first CreateBrokerRegistrationResponse
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&first))
	require.NotEmpty(t, first.JoinToken)

	// Consume the first join token (single-use, keyed per broker) so a later
	// re-registration can mint a fresh one.
	joinRec := doRequestNoAuth(t, srv, http.MethodPost, "/api/v1/brokers/join",
		BrokerJoinRequest{BrokerID: first.BrokerID, JoinToken: first.JoinToken, Hostname: "h", Version: "1.0.0"})
	require.Equal(t, http.StatusOK, joinRec.Code, "first join: %s", joinRec.Body.String())

	// Store precondition: recorded owner is alice, AutoProvide is off.
	before, err := s.GetRuntimeBroker(ctx, first.BrokerID)
	require.NoError(t, err)
	require.Equal(t, alice.ID, before.CreatedBy)
	require.False(t, before.AutoProvide)

	// Bob (authenticated non-owner) attempts to re-register by name, flipping
	// AutoProvide on. Must be DENIED with 403.
	recBob := doRequestAsUser(t, srv, bob, http.MethodPost, "/api/v1/brokers",
		CreateBrokerRegistrationRequest{Name: "owned-shared-broker", AutoProvide: true})
	assert.Equal(t, http.StatusForbidden, recBob.Code,
		"non-owner re-registration must be 403; got: %s", recBob.Body.String())

	// Store-verify NO mutation from the refused write: the ownership check returns
	// before AutoProvide is overwritten or a token is minted.
	afterDenied, err := s.GetRuntimeBroker(ctx, first.BrokerID)
	require.NoError(t, err)
	assert.False(t, afterDenied.AutoProvide, "non-owner attempt must not flip AutoProvide")
	assert.Equal(t, alice.ID, afterDenied.CreatedBy, "recorded owner must be unchanged")

	// Owner (alice) re-registers: succeeds, flips AutoProvide, mints a fresh token.
	recAlice := doRequestAsUser(t, srv, alice, http.MethodPost, "/api/v1/brokers",
		CreateBrokerRegistrationRequest{Name: "owned-shared-broker", AutoProvide: true})
	require.Equal(t, http.StatusCreated, recAlice.Code,
		"owner re-registration must succeed; got: %s", recAlice.Body.String())
	var second CreateBrokerRegistrationResponse
	require.NoError(t, json.NewDecoder(recAlice.Body).Decode(&second))
	assert.Equal(t, first.BrokerID, second.BrokerID, "re-registration keeps the same broker id")
	assert.True(t, second.Reregistered)
	assert.NotEmpty(t, second.JoinToken)
	assert.NotEqual(t, first.JoinToken, second.JoinToken,
		"owner re-registration mints a fresh join token")

	afterOwner, err := s.GetRuntimeBroker(ctx, first.BrokerID)
	require.NoError(t, err)
	assert.True(t, afterOwner.AutoProvide, "owner re-registration flips AutoProvide in the store")
	assert.Equal(t, alice.ID, afterOwner.CreatedBy, "owner unchanged after owner re-registration")
}

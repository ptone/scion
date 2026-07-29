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

// Regression tests for the policy-write authorization gate (ptone/scion#591).
//
// Before the gate, every policy-write handler performed no authorization at all:
// any authenticated caller — user, agent, or broker — could author a policy,
// bind it to any principal, or unbind an existing one. That is the mechanism
// behind the measured privilege escalation (arm B: a caller grants itself access
// to a resource it cannot reach; arm C: a resource-scoped self-allow overrides
// an admin's hub-scoped deny).
//
// The gate is deliberately the most conservative one available: hub-admin only,
// via requireAdmin, at the entry of each write handler. Project-owner self-
// service is intentionally NOT granted here; relaxing to it is a separate, later
// change.
//
// The covered set is the full policy-write API — five operations:
//
//   createPolicy, updatePolicy, deletePolicy, addPolicyBinding
//       gated in 536d8f5c (the four authoring/binding ops)
//   removePolicyBinding
//       gated in b5f230a3 (the follow-up)
//
// removePolicyBinding is load-bearing, not incidental: a policy reaches a
// principal only through a binding (see GetPoliciesForPrincipals in
// pkg/store/entadapter/policy_store.go, which joins policies via their binding
// edges), so detaching a binding is equivalent to overriding the policy it
// carried. Left ungated, unbinding would reopen the arm-C escape from the other
// side — a caller could shed an admin's deny by removing its binding. So the
// gate closes both arms only with removePolicyBinding included; the four-op form
// at 536d8f5c did not, and this file must not claim it did.
//
// These tests pin exactly that contract, for each of the five operations:
//
//   unauthenticated            -> 401
//   agent (any scopes)         -> 403
//   broker (HMAC-signed)       -> 403
//   non-admin user             -> 403
//   project owner (non-admin)  -> 403
//   hub admin                  -> success
//
// plus the end-to-end arm-B assertion that a non-admin's self-service chain is
// severed at its first step.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/GoogleCloudPlatform/scion/pkg/store"
	"github.com/stretchr/testify/require"
)

type policyAuthzFixture struct {
	srv          *Server
	store        store.Store
	admin        *store.User
	member       *store.User
	owner        *store.User
	project      *store.Project
	agentToken   string
	broker       *store.RuntimeBroker
	brokerSecret []byte
}

func setupPolicyAuthz(t *testing.T) *policyAuthzFixture {
	t.Helper()
	// testServerWithBrokerAuth (not the stock testServer) so a real HMAC-signed
	// broker request can reach the policy handlers — the broker caller kind is
	// one of the identities the #591 idiom silently admitted.
	srv, s := testServerWithBrokerAuth(t)
	ctx := context.Background()

	mkUser := func(name, role string) *store.User {
		u := &store.User{
			ID:          tid(name),
			Email:       name + "@example.com",
			DisplayName: name,
			Role:        role,
			Status:      "active",
			Created:     time.Now(),
		}
		require.NoError(t, s.CreateUser(ctx, u))
		ensureHubMembership(ctx, s, u.ID)
		return u
	}

	admin := mkUser("pa-admin", store.UserRoleAdmin)
	member := mkUser("pa-member", store.UserRoleMember)
	owner := mkUser("pa-owner", store.UserRoleMember)

	project := &store.Project{
		ID:      tid("pa-project"),
		Name:    "PA Project",
		Slug:    "pa-project",
		OwnerID: owner.ID,
	}
	require.NoError(t, s.CreateProject(ctx, project))

	// An agent WITH the agent-management scopes, to prove the gate denies on
	// identity kind rather than on scope: even a well-scoped agent cannot manage
	// policies.
	agent := &store.Agent{
		ID: tid("pa-agent"), Slug: tid("pa-agent"), Name: "PA Agent",
		ProjectID: project.ID, OwnerID: owner.ID,
	}
	require.NoError(t, s.CreateAgent(ctx, agent))
	atok, err := srv.GetAgentTokenService().GenerateAgentToken(
		agent.ID, agent.ProjectID,
		[]AgentTokenScope{ScopeAgentCreate, ScopeAgentLifecycle}, nil)
	require.NoError(t, err)

	// A broker with an active HMAC secret, so a real signed broker request can
	// authenticate and reach the policy handlers.
	brokerSecret := []byte("pa-broker-hmac-secret-key-for-tests")
	broker := &store.RuntimeBroker{
		ID:      tid("pa-broker"),
		Name:    "pa-broker",
		Slug:    "pa-broker",
		Status:  store.BrokerStatusOnline,
		Created: time.Now(),
		Updated: time.Now(),
	}
	require.NoError(t, s.CreateRuntimeBroker(ctx, broker))
	require.NoError(t, s.CreateBrokerSecret(ctx, &store.BrokerSecret{
		BrokerID:  broker.ID,
		SecretKey: brokerSecret,
		Algorithm: store.BrokerSecretAlgorithmHMACSHA256,
		Status:    store.BrokerSecretStatusActive,
	}))

	return &policyAuthzFixture{
		srv: srv, store: s,
		admin: admin, member: member, owner: owner,
		project: project, agentToken: atok,
		broker: broker, brokerSecret: brokerSecret,
	}
}

// seedPolicy inserts a policy directly through the store (an out-of-band admin
// action) so update/delete/bind have an existing target. name must be unique
// per call within a test.
func (f *policyAuthzFixture) seedPolicy(t *testing.T, name string) *store.Policy {
	t.Helper()
	p := &store.Policy{
		ID:           tid(name),
		Name:         name,
		ScopeType:    store.PolicyScopeHub,
		ResourceType: "agent",
		Actions:      []string{string(ActionDelete)},
		Effect:       store.PolicyEffectDeny,
	}
	require.NoError(t, f.store.CreatePolicy(context.Background(), p))
	return p
}

// deniedCaller is one of the caller kinds the gate must reject, paired with the
// exact status it must produce.
type deniedCaller struct {
	name string
	do   func(method, path string, body interface{}) *httptest.ResponseRecorder
	want int
}

func (f *policyAuthzFixture) deniedCallers(t *testing.T) []deniedCaller {
	return []deniedCaller{
		{
			// The 401 here is supplied by the auth middleware on this route, not
			// by the gate: requireAdmin never runs for an unauthenticated caller,
			// so this row does NOT go red if the gate is reverted. It is a
			// contract assertion (unauth is rejected), not a gate-liveness probe.
			name: "unauthenticated",
			do: func(m, p string, b interface{}) *httptest.ResponseRecorder {
				return doRequestNoAuth(t, f.srv, m, p, b)
			},
			want: http.StatusUnauthorized,
		},
		{
			name: "agent",
			do: func(m, p string, b interface{}) *httptest.ResponseRecorder {
				return doRequestWithAgentToken(t, f.srv, m, p, b, f.agentToken)
			},
			want: http.StatusForbidden,
		},
		{
			// A real HMAC-signed broker request. Brokers satisfy neither
			// UserIdentity nor AgentIdentity — the caller kind the #591 idiom
			// skipped — so requireAdmin must reject it. Reuses the signing helper
			// from the bypass-agents suite (same package) rather than duplicating
			// the HMAC construction.
			name: "broker",
			do: func(m, p string, b interface{}) *httptest.ResponseRecorder {
				bf := &bypassAgentsFixture{srv: f.srv, broker: f.broker, brokerSecret: f.brokerSecret}
				return bf.asBroker(t, m, p, b)
			},
			want: http.StatusForbidden,
		},
		{
			name: "non-admin user",
			do: func(m, p string, b interface{}) *httptest.ResponseRecorder {
				return doRequestAsUser(t, f.srv, f.member, m, p, b)
			},
			want: http.StatusForbidden,
		},
		{
			name: "project owner (non-admin)",
			do: func(m, p string, b interface{}) *httptest.ResponseRecorder {
				return doRequestAsUser(t, f.srv, f.owner, m, p, b)
			},
			want: http.StatusForbidden,
		},
	}
}

func validCreateReq(name string) CreatePolicyRequest {
	return CreatePolicyRequest{
		Name:         name,
		ScopeType:    store.PolicyScopeHub,
		ResourceType: "agent",
		Actions:      []string{string(ActionDelete)},
		Effect:       store.PolicyEffectDeny,
	}
}

func TestPolicyAPI_CreateGate(t *testing.T) {
	f := setupPolicyAuthz(t)
	const path = "/api/v1/policies"

	for _, c := range f.deniedCallers(t) {
		t.Run(c.name, func(t *testing.T) {
			rec := c.do(http.MethodPost, path, validCreateReq("blocked-"+c.name))
			require.Equal(t, c.want, rec.Code, "body: %s", rec.Body.String())
		})
	}

	t.Run("hub admin succeeds", func(t *testing.T) {
		rec := doRequestAsUser(t, f.srv, f.admin, http.MethodPost, path, validCreateReq("admin-created"))
		require.Equal(t, http.StatusCreated, rec.Code, "body: %s", rec.Body.String())
		var created store.Policy
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &created))
		require.Equal(t, "admin-created", created.Name)
	})
}

func TestPolicyAPI_UpdateGate(t *testing.T) {
	f := setupPolicyAuthz(t)
	p := f.seedPolicy(t, "pa-update-target")
	path := "/api/v1/policies/" + p.ID
	body := UpdatePolicyRequest{Description: "changed"}

	for _, c := range f.deniedCallers(t) {
		t.Run(c.name, func(t *testing.T) {
			rec := c.do(http.MethodPatch, path, body)
			require.Equal(t, c.want, rec.Code, "body: %s", rec.Body.String())
		})
	}

	t.Run("hub admin succeeds", func(t *testing.T) {
		rec := doRequestAsUser(t, f.srv, f.admin, http.MethodPatch, path, UpdatePolicyRequest{Description: "admin-changed"})
		require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
	})
}

func TestPolicyAPI_DeleteGate(t *testing.T) {
	f := setupPolicyAuthz(t)

	// Negatives share one seeded policy: a denied delete must not remove it.
	pn := f.seedPolicy(t, "pa-delete-negatives")
	for _, c := range f.deniedCallers(t) {
		t.Run(c.name, func(t *testing.T) {
			rec := c.do(http.MethodDelete, "/api/v1/policies/"+pn.ID, nil)
			require.Equal(t, c.want, rec.Code, "body: %s", rec.Body.String())
			_, err := f.store.GetPolicy(context.Background(), pn.ID)
			require.NoError(t, err, "denied delete must leave the policy intact")
		})
	}

	t.Run("hub admin succeeds", func(t *testing.T) {
		pd := f.seedPolicy(t, "pa-delete-admin")
		rec := doRequestAsUser(t, f.srv, f.admin, http.MethodDelete, "/api/v1/policies/"+pd.ID, nil)
		require.Equal(t, http.StatusNoContent, rec.Code, "body: %s", rec.Body.String())
		_, err := f.store.GetPolicy(context.Background(), pd.ID)
		require.Error(t, err, "admin delete must remove the policy")
	})
}

func TestPolicyAPI_AddBindingGate(t *testing.T) {
	f := setupPolicyAuthz(t)
	p := f.seedPolicy(t, "pa-bind-target")
	path := "/api/v1/policies/" + p.ID + "/bindings"
	body := AddPolicyBindingRequest{
		PrincipalType: store.PolicyPrincipalTypeUser,
		PrincipalID:   f.member.ID,
	}

	for _, c := range f.deniedCallers(t) {
		t.Run(c.name, func(t *testing.T) {
			rec := c.do(http.MethodPost, path, body)
			require.Equal(t, c.want, rec.Code, "body: %s", rec.Body.String())
		})
	}

	t.Run("hub admin succeeds", func(t *testing.T) {
		rec := doRequestAsUser(t, f.srv, f.admin, http.MethodPost, path, AddPolicyBindingRequest{
			PrincipalType: store.PolicyPrincipalTypeUser,
			PrincipalID:   f.admin.ID,
		})
		require.Equal(t, http.StatusCreated, rec.Code, "body: %s", rec.Body.String())
	})
}

func TestPolicyAPI_RemoveBindingGate(t *testing.T) {
	f := setupPolicyAuthz(t)
	ctx := context.Background()
	p := f.seedPolicy(t, "pa-unbind-target")

	// A binding to remove. Left ungated, unbinding is arm-C-adjacent: a caller
	// could unbind an admin's deny to escape it.
	require.NoError(t, f.store.AddPolicyBinding(ctx, &store.PolicyBinding{
		PolicyID:      p.ID,
		PrincipalType: store.PolicyPrincipalTypeUser,
		PrincipalID:   f.member.ID,
	}))

	hasBinding := func() bool {
		bs, err := f.store.GetPolicyBindings(ctx, p.ID)
		require.NoError(t, err)
		for _, b := range bs {
			if b.PrincipalType == store.PolicyPrincipalTypeUser && b.PrincipalID == f.member.ID {
				return true
			}
		}
		return false
	}

	path := "/api/v1/policies/" + p.ID + "/bindings/user/" + f.member.ID

	for _, c := range f.deniedCallers(t) {
		t.Run(c.name, func(t *testing.T) {
			require.True(t, hasBinding(), "precondition: binding present")
			rec := c.do(http.MethodDelete, path, nil)
			require.Equal(t, c.want, rec.Code, "body: %s", rec.Body.String())
			require.True(t, hasBinding(), "denied unbind must leave the binding intact")
		})
	}

	t.Run("hub admin succeeds", func(t *testing.T) {
		require.True(t, hasBinding(), "precondition: binding present")
		rec := doRequestAsUser(t, f.srv, f.admin, http.MethodDelete, path, nil)
		require.Equal(t, http.StatusNoContent, rec.Code, "body: %s", rec.Body.String())
		require.False(t, hasBinding(), "admin unbind must remove the binding")
	})
}

// TestPolicyAPI_ArmBSelfServiceSevered is the end-to-end assertion: the measured
// privilege-escalation chain (create a self-authored policy, then bind it to
// yourself) is stopped at its first HTTP step for a non-admin caller, so no
// self-authored policy ever reaches the store to be evaluated.
func TestPolicyAPI_ArmBSelfServiceSevered(t *testing.T) {
	f := setupPolicyAuthz(t)
	ctx := context.Background()

	const attackName = "arm-b-self-grant"
	rec := doRequestAsUser(t, f.srv, f.member, http.MethodPost, "/api/v1/policies",
		validCreateReq(attackName))
	require.Equal(t, http.StatusForbidden, rec.Code,
		"a non-admin creating a policy must be forbidden; body: %s", rec.Body.String())

	// The chain is severed at step 1: nothing the attacker authored exists to
	// bind or to be evaluated.
	result, err := f.store.ListPolicies(ctx, store.PolicyFilter{}, store.ListOptions{Limit: 100})
	require.NoError(t, err)
	for _, p := range result.Items {
		require.NotEqual(t, attackName, p.Name, "no attacker-authored policy must have been persisted")
		require.NotEqual(t, f.member.ID, p.CreatedBy, "no policy must have been created by the non-admin caller")
	}
}

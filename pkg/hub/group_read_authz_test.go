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

// Regression tests for the group-read authorization gate (ptone/scion#591, N63).
//
// Before the gate, the three permission-subsystem group READ handlers performed
// no authorization at all — any authenticated caller (an unrelated user, a
// cross-project agent, or a broker) could:
//
//   listGroups        GET /api/v1/groups            — enumerate every group hub-wide
//   getGroup          GET /api/v1/groups/{id|slug}  — resolve a group, including the
//                                                     project:<slug>:members group by
//                                                     guessing a project slug (the
//                                                     GetGroupBySlug fallback)
//   listGroupMembers  GET /api/v1/groups/{id}/members — dump membership enriched with
//                                                     display names and roles
//
// Each handler computed per-item capabilities for display but never gated on the
// result (I97: capabilities-for-display are commentary, not a control), so the
// annotation disclosed group structure and membership across tenants.
//
// The gate mirrors the sibling listPolicies read gate (handlers_policies.go:111-127)
// one file over: hub-admin only, via requireAdmin, at the entry of each read
// handler, fail-closed before the store read. Groups and Policies are registered
// on adjacent server.go lines under one "Groups and Policies (Hub Permissions
// System)" comment; one was gated and its neighbour was not until this change.
//
// These tests pin the contract for each of the three reads:
//
//   unauthenticated            -> 401 (auth middleware, not the gate)
//   agent (any scopes)         -> 403
//   broker (HMAC-signed)       -> 403
//   non-admin user             -> 403
//   project owner (non-admin)  -> 403
//   hub admin                  -> success
//
// plus an explicit pin of the slug-fallback vector on getGroup (a non-admin must
// not resolve a group by slug either).

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

type groupReadAuthzFixture struct {
	srv          *Server
	store        store.Store
	admin        *store.User
	member       *store.User
	owner        *store.User
	project      *store.Project
	group        *store.Group
	agentToken   string
	broker       *store.RuntimeBroker
	brokerSecret []byte
}

func setupGroupReadAuthz(t *testing.T) *groupReadAuthzFixture {
	t.Helper()
	// testServerWithBrokerAuth (not the stock testServer) so a real HMAC-signed
	// broker request can reach the group handlers — the broker caller kind is one
	// of the identities the pre-fix idiom silently admitted.
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

	admin := mkUser("gr-admin", store.UserRoleAdmin)
	member := mkUser("gr-member", store.UserRoleMember)
	owner := mkUser("gr-owner", store.UserRoleMember)

	project := &store.Project{
		ID:      tid("gr-project"),
		Name:    "GR Project",
		Slug:    "gr-project",
		OwnerID: owner.ID,
	}
	require.NoError(t, s.CreateProject(ctx, project))

	// An agent WITH the agent-management scopes, to prove the gate denies on
	// identity kind rather than on scope: even a well-scoped agent cannot read
	// groups.
	agent := &store.Agent{
		ID: tid("gr-agent"), Slug: tid("gr-agent"), Name: "GR Agent",
		ProjectID: project.ID, OwnerID: owner.ID,
	}
	require.NoError(t, s.CreateAgent(ctx, agent))
	atok, err := srv.GetAgentTokenService().GenerateAgentToken(
		agent.ID, agent.ProjectID,
		[]AgentTokenScope{ScopeAgentCreate, ScopeAgentLifecycle}, nil)
	require.NoError(t, err)

	// A broker with an active HMAC secret, so a real signed broker request can
	// authenticate and reach the group handlers.
	brokerSecret := []byte("gr-broker-hmac-secret-key-for-tests")
	broker := &store.RuntimeBroker{
		ID:      tid("gr-broker"),
		Name:    "gr-broker",
		Slug:    "gr-broker",
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

	// A group whose ID differs from its slug, so getGroup-by-slug exercises the
	// GetGroupBySlug fallback (GetGroup(id) misses, GetGroupBySlug hits). The
	// slug models the guessable project:<slug>:members target named in the brief.
	group := &store.Group{
		ID:        tid("gr-group"),
		Name:      "GR Members",
		Slug:      "project-gr-project-members",
		GroupType: store.GroupTypeExplicit,
		OwnerID:   owner.ID,
		CreatedBy: owner.ID,
		Created:   time.Now(),
		Updated:   time.Now(),
	}
	require.NoError(t, s.CreateGroup(ctx, group))
	require.NoError(t, s.AddGroupMember(ctx, &store.GroupMember{
		GroupID:    group.ID,
		MemberType: store.GroupMemberTypeUser,
		MemberID:   owner.ID,
		Role:       "owner",
		AddedAt:    time.Now(),
	}))

	return &groupReadAuthzFixture{
		srv: srv, store: s,
		admin: admin, member: member, owner: owner,
		project: project, group: group, agentToken: atok,
		broker: broker, brokerSecret: brokerSecret,
	}
}

// deniedCallers reuses the deniedCaller contract type from policy_api_authz_test.go
// (same package): each caller kind the gate must reject, paired with the status it
// must produce.
func (f *groupReadAuthzFixture) deniedCallers(t *testing.T) []deniedCaller {
	return []deniedCaller{
		{
			// 401 is supplied by the auth middleware, not the gate: requireAdmin
			// never runs for an unauthenticated caller, so this row does NOT go red
			// if the gate is reverted. It is a contract assertion, not a
			// gate-liveness probe.
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
			// UserIdentity nor AgentIdentity — the caller kind the pre-fix idiom
			// skipped — so requireAdmin must reject it.
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

func TestGroupAPI_ListGate(t *testing.T) {
	f := setupGroupReadAuthz(t)
	const path = "/api/v1/groups"

	for _, c := range f.deniedCallers(t) {
		t.Run(c.name, func(t *testing.T) {
			rec := c.do(http.MethodGet, path, nil)
			require.Equal(t, c.want, rec.Code, "body: %s", rec.Body.String())
		})
	}

	t.Run("hub admin succeeds", func(t *testing.T) {
		rec := doRequestAsUser(t, f.srv, f.admin, http.MethodGet, path, nil)
		require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
		var resp ListGroupsResponse
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
		require.NotEmpty(t, resp.Groups, "admin list must return the seeded group")
	})
}

func TestGroupAPI_GetGate(t *testing.T) {
	f := setupGroupReadAuthz(t)
	path := "/api/v1/groups/" + f.group.ID

	for _, c := range f.deniedCallers(t) {
		t.Run(c.name, func(t *testing.T) {
			rec := c.do(http.MethodGet, path, nil)
			require.Equal(t, c.want, rec.Code, "body: %s", rec.Body.String())
		})
	}

	t.Run("hub admin succeeds", func(t *testing.T) {
		rec := doRequestAsUser(t, f.srv, f.admin, http.MethodGet, path, nil)
		require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
	})
}

// TestGroupAPI_GetBySlugGate pins the slug-fallback vector named in the brief: a
// non-admin must not resolve a group by slug either. The path uses the group's
// slug (not its ID), so the handler's GetGroup(id) miss falls through to
// GetGroupBySlug — the exact route by which project:<slug>:members was reachable
// by guessing a project slug. The gate runs ahead of the fallback, so every
// non-admin caller is 403 (not 404), and the admin arm proves the slug actually
// resolves (200), so a reverted gate would flip the non-admin arms to a
// resolvable read rather than silently 404ing.
func TestGroupAPI_GetBySlugGate(t *testing.T) {
	f := setupGroupReadAuthz(t)
	path := "/api/v1/groups/" + f.group.Slug

	for _, c := range f.deniedCallers(t) {
		t.Run(c.name, func(t *testing.T) {
			rec := c.do(http.MethodGet, path, nil)
			require.Equal(t, c.want, rec.Code, "body: %s", rec.Body.String())
		})
	}

	t.Run("hub admin resolves the group by slug", func(t *testing.T) {
		rec := doRequestAsUser(t, f.srv, f.admin, http.MethodGet, path, nil)
		require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
		var g store.Group
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &g))
		require.Equal(t, f.group.ID, g.ID, "slug fallback must resolve to the seeded group")
	})
}

func TestGroupAPI_ListMembersGate(t *testing.T) {
	f := setupGroupReadAuthz(t)
	path := "/api/v1/groups/" + f.group.ID + "/members"

	for _, c := range f.deniedCallers(t) {
		t.Run(c.name, func(t *testing.T) {
			rec := c.do(http.MethodGet, path, nil)
			require.Equal(t, c.want, rec.Code, "body: %s", rec.Body.String())
		})
	}

	t.Run("hub admin succeeds", func(t *testing.T) {
		rec := doRequestAsUser(t, f.srv, f.admin, http.MethodGet, path, nil)
		require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
	})
}

// TestGroupAPI_GetMemberGate pins the gate on the fourth (singleton) group read,
// getGroupMember (N77, completes N63): GET /api/v1/groups/{id|slug}/members/
// {type}/{memberID}, the cross-tenant membership-confirmation oracle. The gate
// lives in getGroupMember, NOT in the handleGroupMemberByID dispatcher, because
// the dispatcher also routes DELETE -> removeGroupMember, which carries its own
// CheckAccess; gating the read path only leaves the write authz untouched. Both
// the id-path and the slug-path (GetGroupBySlug fallback) arms are covered — the
// slug is the same reachability vector N63 closed for getGroup/listGroupMembers.
func TestGroupAPI_GetMemberGate(t *testing.T) {
	f := setupGroupReadAuthz(t)
	// The seeded owner is a member (role owner) of the seeded group.
	idPath := "/api/v1/groups/" + f.group.ID + "/members/user/" + f.owner.ID
	slugPath := "/api/v1/groups/" + f.group.Slug + "/members/user/" + f.owner.ID

	for _, c := range f.deniedCallers(t) {
		t.Run(c.name+" (by id)", func(t *testing.T) {
			rec := c.do(http.MethodGet, idPath, nil)
			require.Equal(t, c.want, rec.Code, "body: %s", rec.Body.String())
		})
		t.Run(c.name+" (by slug)", func(t *testing.T) {
			rec := c.do(http.MethodGet, slugPath, nil)
			require.Equal(t, c.want, rec.Code, "body: %s", rec.Body.String())
		})
	}

	t.Run("hub admin succeeds (by id)", func(t *testing.T) {
		rec := doRequestAsUser(t, f.srv, f.admin, http.MethodGet, idPath, nil)
		require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
		var m store.GroupMember
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &m))
		require.Equal(t, f.owner.ID, m.MemberID, "admin must read the seeded membership")
	})

	t.Run("hub admin succeeds (by slug)", func(t *testing.T) {
		rec := doRequestAsUser(t, f.srv, f.admin, http.MethodGet, slugPath, nil)
		require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
	})
}

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

// End-to-end routing tests for the api.AgentActionKeys early branches wired
// into handleAgentAction (handlers_agents_core.go) and
// handleProjectAgentAction (handlers_projects_core.go) -- task 2.1's round-1
// review (reviews/2.1-r1.md, findings 2 and 3) required these to be driven
// through the real mux (srv.Handler()), not just the extracted
// authorizeAgentKeys/authorizeAgentKeysCrossProject functions the sibling
// file authorize_agentkeys_matrix_test.go covers. No real keys handler
// exists yet (task 2.2 adds ExecuteAgentKeys): every "allowed" case below
// still ends in the generic `not_found`/"Action" 404 the two switches'
// shared default branch already produces for any action without a
// dispatch case, exactly like hitting any other not-yet-implemented action
// on these routes today. These tests only assert that the AUTHORIZATION
// decision reached before that point is correct and identical in shape
// across both route shapes.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/GoogleCloudPlatform/scion/pkg/agent/state"
	"github.com/GoogleCloudPlatform/scion/pkg/store"
	"github.com/stretchr/testify/require"
)

// agentKeysRouteFixture builds a server plus two real projects/agents so the
// tests below can exercise both /keys route shapes end-to-end.
type agentKeysRouteFixture struct {
	srv      *Server
	store    store.Store
	projectA *store.Project
	projectB *store.Project
	agentInA *store.Agent // target agent in project A
	agentInB *store.Agent // target agent in project B
}

func newAgentKeysRouteFixture(t *testing.T) *agentKeysRouteFixture {
	t.Helper()
	srv, s := testServer(t)
	ctx := context.Background()

	owner := &store.User{
		ID: tid("agentkeys-route-owner"), Email: "agentkeys-route-owner@test.com",
		DisplayName: "Owner", Role: store.UserRoleMember, Status: "active",
	}
	require.NoError(t, s.CreateUser(ctx, owner))

	projA := &store.Project{ID: tid("agentkeys-route-proj-a"), Name: "Route A", Slug: "agentkeys-route-proj-a", OwnerID: owner.ID}
	require.NoError(t, s.CreateProject(ctx, projA))
	projB := &store.Project{ID: tid("agentkeys-route-proj-b"), Name: "Route B", Slug: "agentkeys-route-proj-b", OwnerID: owner.ID}
	require.NoError(t, s.CreateProject(ctx, projB))

	agentA := &store.Agent{
		ID: tid("agentkeys-route-agent-a"), Slug: "agentkeys-route-agent-a", Name: "Agent A",
		ProjectID: projA.ID, Phase: string(state.PhaseRunning), OwnerID: owner.ID,
	}
	require.NoError(t, s.CreateAgent(ctx, agentA))
	agentB := &store.Agent{
		ID: tid("agentkeys-route-agent-b"), Slug: "agentkeys-route-agent-b", Name: "Agent B",
		ProjectID: projB.ID, Phase: string(state.PhaseRunning), OwnerID: owner.ID,
	}
	require.NoError(t, s.CreateAgent(ctx, agentB))

	return &agentKeysRouteFixture{srv: srv, store: s, projectA: projA, projectB: projB, agentInA: agentA, agentInB: agentB}
}

// agentToken mints a real, signed agent JWT for a synthetic caller "agent"
// in callerProjectID with the given scopes. The credential-status gate in
// the shared auth middleware (auth.go's evaluateAgentCredentialStatus) only
// consults a credential-ID-keyed store, not store.Agent by ID -- a token
// with no matching credential row authenticates via the documented legacy
// compatibility path -- so the caller does not need its own store.Agent row
// for these routing tests, unlike authorizeAgentKeys' *target*, which must
// be a real row.
func (f *agentKeysRouteFixture) agentToken(t *testing.T, callerAgentID, callerProjectID string, scopes ...AgentTokenScope) string {
	t.Helper()
	tok, err := f.srv.GetAgentTokenService().GenerateAgentToken(callerAgentID, callerProjectID, scopes, nil)
	require.NoError(t, err)
	return tok
}

// errorCode extracts error.code from a Hub error envelope response body.
func errorCode(t *testing.T, body []byte) string {
	t.Helper()
	var env struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	require.NoError(t, json.Unmarshal(body, &env), "response body: %s", string(body))
	return env.Error.Code
}

// agentKeysLookupSpyStore wraps a store.Store and counts calls to the two
// agent-resolution methods, so a test can prove no agent lookup ran before
// a denial (contract §3.1 invariant 4 / AK-21c).
type agentKeysLookupSpyStore struct {
	store.Store
	getAgentCalls       int32
	getAgentBySlugCalls int32
}

func (s *agentKeysLookupSpyStore) GetAgent(ctx context.Context, id string) (*store.Agent, error) {
	atomic.AddInt32(&s.getAgentCalls, 1)
	return s.Store.GetAgent(ctx, id)
}

func (s *agentKeysLookupSpyStore) GetAgentBySlug(ctx context.Context, projectID, slug string) (*store.Agent, error) {
	atomic.AddInt32(&s.getAgentBySlugCalls, 1)
	return s.Store.GetAgentBySlug(ctx, projectID, slug)
}

func (s *agentKeysLookupSpyStore) lookupCount() int32 {
	return atomic.LoadInt32(&s.getAgentCalls) + atomic.LoadInt32(&s.getAgentBySlugCalls)
}

// TestAgentActionKeysRoute_TopLevel_CrossProjectAndMissing pins AK-21 (an
// existing, foreign-project target: 422) and AK-21b (a target ID that does
// not exist in any project: 404) on the top-level route.
func TestAgentActionKeysRoute_TopLevel_CrossProjectAndMissing(t *testing.T) {
	f := newAgentKeysRouteFixture(t)
	token := f.agentToken(t, tid("agentkeys-route-caller-t"), f.projectA.ID, ScopeAgentLifecycle)

	t.Run("AK-21: existing target in a foreign project -> 422", func(t *testing.T) {
		rec := doRequestWithAgentToken(t, f.srv, http.MethodPost, "/api/v1/agents/"+f.agentInB.ID+"/keys", nil, token)
		if rec.Code != http.StatusUnprocessableEntity {
			t.Fatalf("status = %d, want 422: %s", rec.Code, rec.Body.String())
		}
		if code := errorCode(t, rec.Body.Bytes()); code != "cross_project_keys_unsupported" {
			t.Errorf("code = %q, want cross_project_keys_unsupported", code)
		}
	})

	t.Run("AK-21b: nonexistent target ID -> 404 not_found", func(t *testing.T) {
		rec := doRequestWithAgentToken(t, f.srv, http.MethodPost, "/api/v1/agents/"+tid("agentkeys-route-nonexistent")+"/keys", nil, token)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want 404: %s", rec.Code, rec.Body.String())
		}
		if code := errorCode(t, rec.Body.Bytes()); code != "not_found" {
			t.Errorf("code = %q, want not_found (not agent_not_found)", code)
		}
	})
}

// TestAgentActionKeysRoute_ProjectScoped_CrossProjectNoLookup pins AK-21c:
// on the project-scoped route, an agent-credential cross-project refusal
// must be decided before any agent-target lookup, so an existing and a
// nonexistent slug produce identical 422 responses and neither triggers a
// store lookup.
func TestAgentActionKeysRoute_ProjectScoped_CrossProjectNoLookup(t *testing.T) {
	f := newAgentKeysRouteFixture(t)
	token := f.agentToken(t, tid("agentkeys-route-caller-p1"), f.projectA.ID, ScopeAgentLifecycle)

	spy := &agentKeysLookupSpyStore{Store: f.store}
	f.srv.store = spy

	existing := doRequestWithAgentToken(t, f.srv, http.MethodPost,
		"/api/v1/projects/"+f.projectB.ID+"/agents/"+f.agentInB.Slug+"/keys", nil, token)
	nonexistent := doRequestWithAgentToken(t, f.srv, http.MethodPost,
		"/api/v1/projects/"+f.projectB.ID+"/agents/does-not-exist/keys", nil, token)

	for name, rec := range map[string]*httptest.ResponseRecorder{"existing slug": existing, "nonexistent slug": nonexistent} {
		if rec.Code != http.StatusUnprocessableEntity {
			t.Fatalf("%s: status = %d, want 422: %s", name, rec.Code, rec.Body.String())
		}
		if code := errorCode(t, rec.Body.Bytes()); code != "cross_project_keys_unsupported" {
			t.Errorf("%s: code = %q, want cross_project_keys_unsupported", name, code)
		}
	}

	if existing.Code != nonexistent.Code || errorCode(t, existing.Body.Bytes()) != errorCode(t, nonexistent.Body.Bytes()) {
		t.Fatalf("existing and nonexistent slug responses differ: %d %s vs %d %s",
			existing.Code, existing.Body.String(), nonexistent.Code, nonexistent.Body.String())
	}

	if got := spy.lookupCount(); got != 0 {
		t.Fatalf("expected zero agent lookups before the cross-project refusal, got %d (GetAgent=%d GetAgentBySlug=%d)",
			got, atomic.LoadInt32(&spy.getAgentCalls), atomic.LoadInt32(&spy.getAgentBySlugCalls))
	}
}

// TestAgentActionKeysRoute_ProjectScoped_SameProjectMissingAgent pins that a
// same-project resolution miss reports the keys contract's own "not_found"
// code, not the route's other resolver's "agent_not_found" shape (contract
// §3 invariant 3).
func TestAgentActionKeysRoute_ProjectScoped_SameProjectMissingAgent(t *testing.T) {
	f := newAgentKeysRouteFixture(t)
	token := f.agentToken(t, tid("agentkeys-route-caller-p2"), f.projectA.ID, ScopeAgentLifecycle)

	rec := doRequestWithAgentToken(t, f.srv, http.MethodPost,
		"/api/v1/projects/"+f.projectA.ID+"/agents/does-not-exist/keys", nil, token)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404: %s", rec.Code, rec.Body.String())
	}
	if code := errorCode(t, rec.Body.Bytes()); code != "not_found" {
		t.Errorf("code = %q, want not_found (not agent_not_found)", code)
	}
}

// TestAgentActionKeysRoute_BothShapesAgree replaces the round-1 AC4 tests
// that passed even with this PR's production code deleted
// (TestAgentActionKeys_RouteMetadataCoversBothRouteShapes /
// TestAgentActionKeys_CapabilityProjectionConsistentAcrossRouteShapes,
// finding 3): it drives both route shapes through the real mux for the same
// identity/target pair and asserts they reach the same outcome, and that
// the attach capability ComputeCapabilities projects for the caller agrees
// with the keys route's own allow/deny decision.
func TestAgentActionKeysRoute_BothShapesAgree(t *testing.T) {
	f := newAgentKeysRouteFixture(t)

	allowedCallerID := tid("agentkeys-route-agree-allowed")
	deniedCallerID := tid("agentkeys-route-agree-denied")
	allowedToken := f.agentToken(t, allowedCallerID, f.projectA.ID, ScopeAgentLifecycle)
	deniedToken := f.agentToken(t, deniedCallerID, f.projectA.ID, ScopeAgentCreate) // no lifecycle scope

	cases := []struct {
		name       string
		token      string
		callerID   string
		wantStatus int
		wantCode   string
		wantAttach bool
	}{
		{
			name: "authorized agent, same project (unimplemented handler, not a denial)",
			token: allowedToken, callerID: allowedCallerID,
			wantStatus: http.StatusNotFound, wantCode: "not_found", wantAttach: true,
		},
		{
			name: "agent missing lifecycle scope",
			token: deniedToken, callerID: deniedCallerID,
			wantStatus: http.StatusForbidden, wantCode: "keys_denied", wantAttach: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			topLevel := doRequestWithAgentToken(t, f.srv, http.MethodPost, "/api/v1/agents/"+f.agentInA.ID+"/keys", nil, tc.token)
			projectScoped := doRequestWithAgentToken(t, f.srv, http.MethodPost,
				"/api/v1/projects/"+f.projectA.ID+"/agents/"+f.agentInA.Slug+"/keys", nil, tc.token)

			if topLevel.Code != tc.wantStatus {
				t.Fatalf("top-level status = %d, want %d: %s", topLevel.Code, tc.wantStatus, topLevel.Body.String())
			}
			if projectScoped.Code != tc.wantStatus {
				t.Fatalf("project-scoped status = %d, want %d: %s", projectScoped.Code, tc.wantStatus, projectScoped.Body.String())
			}
			if got := errorCode(t, topLevel.Body.Bytes()); got != tc.wantCode {
				t.Errorf("top-level code = %q, want %q", got, tc.wantCode)
			}
			if got := errorCode(t, projectScoped.Body.Bytes()); got != tc.wantCode {
				t.Errorf("project-scoped code = %q, want %q", got, tc.wantCode)
			}

			// Capability projection must agree with the route decision: the
			// same identity/resource pair, evaluated independently through
			// ComputeCapabilities, must include ActionAttach exactly when
			// the keys route allowed the call.
			callerIdentity := authzHelperAgent(f.projectA.ID, ScopeAgentLifecycle)
			if tc.callerID == deniedCallerID {
				callerIdentity = authzHelperAgent(f.projectA.ID, ScopeAgentCreate)
			}
			caps := f.srv.GetAuthzService().ComputeCapabilities(context.Background(), callerIdentity, agentResource(f.agentInA))
			hasAttach := capabilityAllows(caps, ActionAttach)
			if hasAttach != tc.wantAttach {
				t.Errorf("capability projection attach=%v, want %v (route decision disagrees with capability projection)", hasAttach, tc.wantAttach)
			}
		})
	}
}

// TestAgentActionKeysRoute_RevokedAgentCredential_NeverReachesTheGate
// reuses the established agent credential-revocation path (finding 6:
// credential_revocation_test.go's TestRevokedTokenDeniedBeforeExpiry
// pattern, setupCredentialTestServer/createCredTestAgent/
// srv.GenerateAgentToken/RevokeAgentCredential) against a request shaped
// like the keys route, now that the routing from finding 2 exists. A
// revoked agent credential is rejected by the shared auth middleware
// (evaluateAgentCredentialStatus) before any handler runs, so it never
// reaches authorizeAgentKeys — the same "missing credential" state
// TestAuthorizeAgentKeys_MissingAndInvalidCredentials covers directly at
// the function level. An expired JWT (as opposed to a revoked credential
// record) fails signature/claims validation in the same shared middleware,
// even earlier, for the same reason; it is not given its own case here
// because that failure mode is already exercised for agent tokens in
// general (agenttoken_test.go) and is not specific to the keys route.
func TestAgentActionKeysRoute_RevokedAgentCredential_NeverReachesTheGate(t *testing.T) {
	srv, s, user, project := setupCredentialTestServer(t)
	ctx := context.Background()

	targetID := tid("agentkeys-route-revoke-target")
	createCredTestAgent(t, s, targetID, project.ID, user.ID)

	callerID := tid("agentkeys-route-revoke-caller")
	createCredTestAgent(t, s, callerID, project.ID, user.ID)

	token, err := srv.GenerateAgentToken(callerID, project.ID, nil, AgentRoleFull, nil)
	require.NoError(t, err)

	claims, err := srv.agentTokenService.ValidateAgentToken(token)
	require.NoError(t, err)
	cred, err := s.GetAgentCredentialByJTIHash(ctx, hashJTI(claims.ID))
	require.NoError(t, err)

	// Before revocation: the token authenticates and reaches the keys gate
	// (proves the setup is valid, so the post-revocation 401 below is
	// meaningful). The caller has full-role lifecycle scope on its own
	// project's agent, so this is expected to be an authorization "allowed"
	// (404 "Action", unimplemented handler), not itself a denial.
	before := doRequestWithAgentToken(t, srv, http.MethodPost, "/api/v1/agents/"+targetID+"/keys", nil, token)
	if before.Code == http.StatusUnauthorized {
		t.Fatalf("token should authenticate before revocation, got 401: %s", before.Body.String())
	}

	require.NoError(t, s.RevokeAgentCredential(ctx, cred.ID, "test", "explicit"))

	after := doRequestWithAgentToken(t, srv, http.MethodPost, "/api/v1/agents/"+targetID+"/keys", nil, token)
	if after.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 after revocation (never reaching authorizeAgentKeys), got %d: %s",
			after.Code, after.Body.String())
	}
}

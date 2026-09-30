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

// Table-driven authorization and route/capability-metadata tests for
// authorizeAgentKeys (.design/agent-keys-contract.md §3, ptone/scion#2191 /
// task 2.1, ptone/scion#2195). The matrix required by issue #2195's
// acceptance criteria is spread across the Test functions below:
//
//   - TestAuthorizeAgentKeys_IdentityKinds: message-only authority (via the
//     UAT-scope test below), missing credentials (nil identity), and every
//     principal kind, including unsupported ones.
//   - TestAuthorizeAgentKeys_OwnerAndNonOwner: owner/non-owner, explicit deny
//     (the agent.attach manage-alias carve-out).
//   - TestAuthorizeAgentKeys_AgentCredential_ProjectBoundary: same/cross
//     project for agent credentials, and self/parent/ancestor not
//     bypassing scope or the project boundary.
//   - TestAuthorizeAgentKeys_UserAccessTokenCredentialRestrictions:
//     live UAT credential restrictions, including message-only authority.
//   - TestAuthorizeAgentKeys_MissingAndInvalidCredentials: missing/invalid
//     credentials.
//   - TestAuthorizeAgentKeys_RevokedUAT_NeverReachesTheGate: revoked
//     credentials, reusing the established RS4 UAT-revocation path rather
//     than reimplementing it (issue #2195's Verification section).
//   - TestAuthorizeAgentKeys_MessageModeIrrelevant: closed/open message
//     modes.
//   - TestAuthorizeAgentKeysCrossProject: the project-scoped route's
//     pre-resolution boundary check (contract §3.1 invariant 4, AK-21c).
//   - TestAgentActionKeys_RouteMetadataCoversBothRouteShapes and
//     TestAgentActionKeys_CapabilityProjectionConsistentAcrossRouteShapes:
//     route/capability metadata agreement across both route shapes.
//   - TestAgentAttachRegistry_EnforcementListsAuthorizeAgentKeys: the
//     registry stays an accurate index of what enforces agent.attach.

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/GoogleCloudPlatform/scion/pkg/agentkeys"
	"github.com/GoogleCloudPlatform/scion/pkg/hub/permissions"
	"github.com/GoogleCloudPlatform/scion/pkg/store"
)

// authzKeysHelperRequest builds a request carrying identity, matching
// authzHelperRequest's shape in authorize_test.go (kept local to this file
// so agent-keys-specific test intent is discoverable without hunting
// through authorize_test.go's other fixtures).
func authzKeysHelperRequest(identity Identity) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/api/v1/agentkeys-test", nil)
	if identity != nil {
		req = req.WithContext(contextWithIdentity(req.Context(), identity))
	}
	return req
}

// ---------------------------------------------------------------------------
// Identity kinds, including unsupported principal kinds
// ---------------------------------------------------------------------------

func TestAuthorizeAgentKeys_IdentityKinds(t *testing.T) {
	srv, s := testServer(t)
	authzHelperSeedAdmin(t, s)
	target := authzHelperTargetAgent()

	tests := []struct {
		name        string
		identity    Identity
		wantAllowed bool
		wantOutcome agentkeys.Outcome
	}{
		{
			name:        "nil identity is unauthenticated (missing credential)",
			identity:    nil,
			wantAllowed: false,
			wantOutcome: agentkeys.OutcomeKeysDenied,
		},
		{
			name:        "user allowed by policy (super-admin)",
			identity:    authzHelperAdmin(),
			wantAllowed: true,
		},
		{
			name:        "user denied by policy (no attach grant)",
			identity:    authzHelperMember(),
			wantAllowed: false,
			wantOutcome: agentkeys.OutcomeKeysDenied,
		},
		{
			// DevUser.Role() is hardcoded "admin" (the local dev pseudo-user
			// stands in for a trusted local operator when no auth server is
			// configured), so it is allowed here for the same reason
			// authzHelperAdmin() is above -- this case exists to confirm
			// "dev" reaches the same user-branch CheckAccess call as "user"
			// (contract: UAT identity stays human; "dev" is one of the two
			// Type() values authorizeAgentKeys routes to that branch), not
			// to claim dev identities are unprivileged.
			name:        "dev identity follows the user branch",
			identity:    NewDevUser(DevUserConfig{}),
			wantAllowed: true,
		},
		{
			name:        "agent with lifecycle scope in the target's project",
			identity:    authzHelperAgent(authzHelperProjectA, ScopeAgentLifecycle),
			wantAllowed: true,
		},
		{
			name:        "agent with lifecycle scope in a different project",
			identity:    authzHelperAgent(authzHelperProjectB, ScopeAgentLifecycle),
			wantAllowed: false,
			wantOutcome: agentkeys.OutcomeCrossProjectKeysUnsupported,
		},
		{
			name:        "agent without lifecycle scope, same project",
			identity:    authzHelperAgent(authzHelperProjectA, ScopeAgentCreate),
			wantAllowed: false,
			wantOutcome: agentkeys.OutcomeKeysDenied,
		},
		{
			name:        "broker identity is an unsupported principal kind",
			identity:    NewBrokerIdentity("authz-broker"),
			wantAllowed: false,
			wantOutcome: agentkeys.OutcomeKeysDenied,
		},
		{
			name: "federated agent identity is an unsupported principal kind",
			identity: NewFederatedAgentIdentity("https://issuer.example", "fed-agent",
				authzHelperProjectA, "fed", "", nil, []AgentTokenScope{ScopeAgentLifecycle}),
			wantAllowed: false,
			wantOutcome: agentkeys.OutcomeKeysDenied,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := authzKeysHelperRequest(tc.identity)
			got := srv.authorizeAgentKeys(r, target)
			if got.Allowed != tc.wantAllowed {
				t.Fatalf("Allowed = %v, want %v (reason: %s)", got.Allowed, tc.wantAllowed, got.Reason)
			}
			if !tc.wantAllowed && got.Outcome != tc.wantOutcome {
				t.Errorf("Outcome = %q, want %q", got.Outcome, tc.wantOutcome)
			}
			if tc.wantAllowed && got.Outcome != "" {
				t.Errorf("Outcome = %q, want empty on allow", got.Outcome)
			}
		})
	}
}

func TestAuthorizeAgentKeys_NilTargetDenied(t *testing.T) {
	srv, _ := testServer(t)

	got := srv.authorizeAgentKeys(authzKeysHelperRequest(authzHelperAdmin()), nil)
	if got.Allowed {
		t.Fatal("expected a nil target agent to be denied")
	}
	if got.Outcome != agentkeys.OutcomeKeysDenied {
		t.Errorf("Outcome = %q, want %q", got.Outcome, agentkeys.OutcomeKeysDenied)
	}
}

// ---------------------------------------------------------------------------
// Owner / non-owner, explicit deny (miller79/scion#88 cross-member attach
// carve-out: agent.attach is ExcludeFromManageAlias, so owner/admin manage
// authority does not imply it)
// ---------------------------------------------------------------------------

func TestAuthorizeAgentKeys_OwnerAndNonOwner(t *testing.T) {
	f := newGoldenFixture(t)
	srv := &Server{authzService: f.authz}

	owner := NewAuthenticatedUser(f.projectOwnerID, "proj-owner@golden.test", "Project Owner", "member", "api")
	admin := NewAuthenticatedUser(f.projectAdminID, "proj-admin@golden.test", "Project Admin", "member", "api")
	member := NewAuthenticatedUser(f.memberAlphaID, "member-alpha@golden.test", "Member Alpha", "member", "api")
	superAdmin := NewAuthenticatedUser(f.superAdminID, "superadmin@golden.test", "Super Admin", "admin", "api")

	memberAgent := &store.Agent{
		ID: "member-agent", Name: "member-agent", Slug: "member-agent",
		ProjectID: f.projectAlpha.ID, OwnerID: f.memberAlphaID,
	}

	cases := []struct {
		name     string
		identity UserIdentity
		target   *store.Agent
		allowed  bool
	}{
		{"owner attaches to own agent", owner, f.agentAlpha, true},
		{"owner explicitly denied on member's agent (manage alias carve-out)", owner, memberAgent, false},
		{"admin explicitly denied on member's agent", admin, memberAgent, false},
		{"member attaches to own agent", member, memberAgent, true},
		{"member denied on owner's agent (non-owner)", member, f.agentAlpha, false},
		{"super-admin attaches to member's agent", superAdmin, memberAgent, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := srv.authorizeAgentKeys(authzKeysHelperRequest(tc.identity), tc.target)
			if got.Allowed != tc.allowed {
				t.Fatalf("Allowed = %v, want %v (reason: %s)", got.Allowed, tc.allowed, got.Reason)
			}
			if !tc.allowed && got.Outcome != agentkeys.OutcomeKeysDenied {
				t.Errorf("Outcome = %q, want %q", got.Outcome, agentkeys.OutcomeKeysDenied)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Agent credential: same/cross project, and no self/parent/ancestor shortcut
// ---------------------------------------------------------------------------

func TestAuthorizeAgentKeys_AgentCredential_ProjectBoundary(t *testing.T) {
	srv, _ := testServer(t)

	selfID := authzHelperAgentID
	target := &store.Agent{
		ID: selfID, Name: "self", Slug: "self",
		ProjectID: authzHelperProjectA, OwnerID: "someone-else",
	}

	t.Run("self status does not bypass missing scope", func(t *testing.T) {
		// The caller IS the target agent, but holds no ScopeAgentLifecycle.
		// authorizeAgentMessage grants a self-message bypass in this exact
		// shape; authorizeAgentKeys must not.
		identity := authzHelperAgent(authzHelperProjectA)
		got := srv.authorizeAgentKeys(authzKeysHelperRequest(identity), target)
		if got.Allowed {
			t.Fatal("expected self status, without scope, to be denied")
		}
		if got.Outcome != agentkeys.OutcomeKeysDenied {
			t.Errorf("Outcome = %q, want %q", got.Outcome, agentkeys.OutcomeKeysDenied)
		}
	})

	t.Run("self status does not bypass the project boundary", func(t *testing.T) {
		identity := authzHelperAgent(authzHelperProjectB, ScopeAgentLifecycle)
		got := srv.authorizeAgentKeys(authzKeysHelperRequest(identity), target)
		if got.Allowed {
			t.Fatal("expected self status, cross-project, to be denied")
		}
		if got.Outcome != agentkeys.OutcomeCrossProjectKeysUnsupported {
			t.Errorf("Outcome = %q, want %q", got.Outcome, agentkeys.OutcomeCrossProjectKeysUnsupported)
		}
	})

	t.Run("parent/ancestor status does not bypass missing scope", func(t *testing.T) {
		parentTarget := &store.Agent{
			ID: "child-agent", Name: "child", Slug: "child",
			ProjectID: authzHelperProjectA, OwnerID: "someone-else",
			Ancestry: []string{authzHelperAgentID}, // caller is the parent
		}
		identity := authzHelperAgent(authzHelperProjectA) // no lifecycle scope
		got := srv.authorizeAgentKeys(authzKeysHelperRequest(identity), parentTarget)
		if got.Allowed {
			t.Fatal("expected parent status, without scope, to be denied")
		}
	})

	t.Run("ancestor status does not bypass the project boundary", func(t *testing.T) {
		descendantTarget := &store.Agent{
			ID: "descendant-agent", Name: "descendant", Slug: "descendant",
			ProjectID: authzHelperProjectB, OwnerID: "someone-else",
			Ancestry: []string{authzHelperAgentID},
		}
		identity := authzHelperAgent(authzHelperProjectA, ScopeAgentLifecycle)
		got := srv.authorizeAgentKeys(authzKeysHelperRequest(identity), descendantTarget)
		if got.Allowed {
			t.Fatal("expected ancestor status, cross-project, to be denied")
		}
		if got.Outcome != agentkeys.OutcomeCrossProjectKeysUnsupported {
			t.Errorf("Outcome = %q, want %q", got.Outcome, agentkeys.OutcomeCrossProjectKeysUnsupported)
		}
	})

	t.Run("same project, scoped agent: allowed", func(t *testing.T) {
		peer := &store.Agent{
			ID: "peer-agent", Name: "peer", Slug: "peer",
			ProjectID: authzHelperProjectA, OwnerID: "someone-else",
		}
		identity := authzHelperAgent(authzHelperProjectA, ScopeAgentLifecycle)
		got := srv.authorizeAgentKeys(authzKeysHelperRequest(identity), peer)
		if !got.Allowed {
			t.Fatalf("expected a same-project scoped agent to be allowed: %s", got.Reason)
		}
	})
}

// ---------------------------------------------------------------------------
// User access token (UAT) credential restrictions, including message-only
// authority
// ---------------------------------------------------------------------------

func TestAuthorizeAgentKeys_UserAccessTokenCredentialRestrictions(t *testing.T) {
	f := newGoldenFixture(t)
	srv := &Server{authzService: f.authz}

	baseOwner := NewAuthenticatedUser(f.projectOwnerID, "proj-owner@golden.test", "Project Owner", "member", "api")

	t.Run("UAT scoped to agent:attach on own agent: allowed", func(t *testing.T) {
		uat := NewScopedUserIdentity(baseOwner, f.projectAlpha.ID, []string{"agent:attach"})
		got := srv.authorizeAgentKeys(authzKeysHelperRequest(uat), f.agentAlpha)
		if !got.Allowed {
			t.Fatalf("expected an agent:attach-scoped UAT to be allowed: %s", got.Reason)
		}
	})

	t.Run("message-only authority (UAT scoped to agent:message only): denied", func(t *testing.T) {
		uat := NewScopedUserIdentity(baseOwner, f.projectAlpha.ID, []string{"agent:message"})
		got := srv.authorizeAgentKeys(authzKeysHelperRequest(uat), f.agentAlpha)
		if got.Allowed {
			t.Fatal("expected a message-only-scoped UAT to be denied keys authority")
		}
		if got.Outcome != agentkeys.OutcomeKeysDenied {
			t.Errorf("Outcome = %q, want %q", got.Outcome, agentkeys.OutcomeKeysDenied)
		}
	})

	t.Run("UAT scoped to agent:read only: denied", func(t *testing.T) {
		uat := NewScopedUserIdentity(baseOwner, f.projectAlpha.ID, []string{"agent:read"})
		got := srv.authorizeAgentKeys(authzKeysHelperRequest(uat), f.agentAlpha)
		if got.Allowed {
			t.Fatal("expected a read-only-scoped UAT to be denied keys authority")
		}
	})

	t.Run("UAT project boundary: token scoped to a different project is denied", func(t *testing.T) {
		uat := NewScopedUserIdentity(baseOwner, f.projectBeta.ID, []string{"agent:attach"})
		got := srv.authorizeAgentKeys(authzKeysHelperRequest(uat), f.agentAlpha)
		if got.Allowed {
			t.Fatal("expected a UAT scoped to a different project to be denied")
		}
	})
}

// ---------------------------------------------------------------------------
// Missing and invalid credentials
// ---------------------------------------------------------------------------

// agentKeysFakeIdentity implements the minimal Identity interface only, so
// it can masquerade as an "agent" or "user" Type() without satisfying the
// richer AgentIdentity/UserIdentity interfaces authorizeAgentKeys asserts
// for. This exercises the defensive "invalid ... identity" branches that a
// well-formed production Identity implementation can never actually reach.
type agentKeysFakeIdentity struct {
	id       string
	typeName string
}

func (f *agentKeysFakeIdentity) ID() string   { return f.id }
func (f *agentKeysFakeIdentity) Type() string { return f.typeName }

func TestAuthorizeAgentKeys_MissingAndInvalidCredentials(t *testing.T) {
	srv, _ := testServer(t)
	target := authzHelperTargetAgent()

	t.Run("missing credential: nil identity", func(t *testing.T) {
		got := srv.authorizeAgentKeys(authzKeysHelperRequest(nil), target)
		if got.Allowed {
			t.Fatal("expected a missing credential to be denied")
		}
		if got.Outcome != agentkeys.OutcomeKeysDenied {
			t.Errorf("Outcome = %q, want %q", got.Outcome, agentkeys.OutcomeKeysDenied)
		}
	})

	t.Run("invalid credential: Type()==agent without AgentIdentity", func(t *testing.T) {
		fake := &agentKeysFakeIdentity{id: "fake-agent", typeName: "agent"}
		got := srv.authorizeAgentKeys(authzKeysHelperRequest(fake), target)
		if got.Allowed {
			t.Fatal("expected an invalid agent identity to be denied")
		}
		if got.Outcome != agentkeys.OutcomeKeysDenied {
			t.Errorf("Outcome = %q, want %q", got.Outcome, agentkeys.OutcomeKeysDenied)
		}
	})

	t.Run("invalid credential: Type()==user without UserIdentity", func(t *testing.T) {
		fake := &agentKeysFakeIdentity{id: "fake-user", typeName: "user"}
		got := srv.authorizeAgentKeys(authzKeysHelperRequest(fake), target)
		if got.Allowed {
			t.Fatal("expected an invalid user identity to be denied")
		}
		if got.Outcome != agentkeys.OutcomeKeysDenied {
			t.Errorf("Outcome = %q, want %q", got.Outcome, agentkeys.OutcomeKeysDenied)
		}
	})
}

// TestAuthorizeAgentKeys_RevokedUAT_NeverReachesTheGate reuses the
// established UAT-revocation path (RS4, rs4_credential_test.go /
// rs1_extended_test.go's mintScopedUAT/doRequestWithUAT helpers) rather than
// reimplementing credential revocation for keys (issue #2195's Verification
// section: "Reuse established credential-revocation tests without
// reimplementing the wider auth refactor").
//
// A revoked UAT fails the shared UnifiedAuthMiddleware before any handler
// (including a future keys handler) runs, so it never produces an identity
// authorizeAgentKeys could evaluate — the same "missing credential" state
// TestAuthorizeAgentKeys_MissingAndInvalidCredentials covers directly. This
// test pins that the revocation gate itself still works, so that upstream
// guarantee keeps holding for whichever route task 2.2 wires it into.
func TestAuthorizeAgentKeys_RevokedUAT_NeverReachesTheGate(t *testing.T) {
	srv, s := testServer(t)

	projectID := tid("agentkeys-revoke-project")
	ownerID := tid("agentkeys-revoke-owner")
	project := &store.Project{ID: projectID, Name: "Revoke Project", Slug: "agentkeys-revoke-project", OwnerID: ownerID}
	if err := s.CreateProject(t.Context(), project); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	createTestUserWithProjectRole(t, s, ownerID, "agentkeys-revoke-owner@test.com", projectID, store.ProjectRoleOwner)

	mintCtx := rs4MintContext(ownerID)
	uatKey, token, err := srv.uatService.CreateToken(mintCtx, ownerID, "agentkeys-revoke-test", projectID, []string{"agent:read"}, nil)
	if err != nil {
		t.Fatalf("CreateToken: %v", err)
	}

	// Before revocation: the token authenticates (proves the setup is valid
	// so the post-revocation 401 below is meaningful, not an unrelated
	// misconfiguration).
	before := doRequestWithUAT(t, srv, uatKey, http.MethodGet, "/api/v1/agents", nil)
	if before.Code == http.StatusUnauthorized {
		t.Fatalf("token should authenticate before revocation, got 401: %s", before.Body.String())
	}

	if err := srv.uatService.RevokeToken(mintCtx, ownerID, token.ID); err != nil {
		t.Fatalf("RevokeToken: %v", err)
	}

	after := doRequestWithUAT(t, srv, uatKey, http.MethodGet, "/api/v1/agents", nil)
	if after.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 after revocation (never reaching any authorization gate), got %d: %s",
			after.Code, after.Body.String())
	}
}

// ---------------------------------------------------------------------------
// Message mode is irrelevant to keys
// ---------------------------------------------------------------------------

func TestAuthorizeAgentKeys_MessageModeIrrelevant(t *testing.T) {
	f := newGoldenFixture(t)
	srv := &Server{authzService: f.authz}
	owner := NewAuthenticatedUser(f.projectOwnerID, "proj-owner@golden.test", "Project Owner", "member", "api")

	t.Run("closed message mode does not prevent an otherwise-authorized keys call", func(t *testing.T) {
		closed := *f.agentAlpha
		closed.MessageMode = store.MessageModeNone
		got := srv.authorizeAgentKeys(authzKeysHelperRequest(owner), &closed)
		if !got.Allowed {
			t.Fatalf("expected closed message mode not to block an attach-authorized owner: %s", got.Reason)
		}
	})

	t.Run("open message mode does not authorize a message-only caller", func(t *testing.T) {
		open := *f.agentAlpha
		open.MessageMode = store.MessageModeProject
		messageOnly := NewScopedUserIdentity(owner, f.projectAlpha.ID, []string{"agent:message"})
		got := srv.authorizeAgentKeys(authzKeysHelperRequest(messageOnly), &open)
		if got.Allowed {
			t.Fatal("expected open message mode not to grant keys to a message-only caller")
		}
	})
}

// ---------------------------------------------------------------------------
// authorizeAgentKeysCrossProject: the project-scoped route's pre-resolution
// boundary check (contract §3.1 invariant 4, AK-21c)
// ---------------------------------------------------------------------------

func TestAuthorizeAgentKeysCrossProject(t *testing.T) {
	srv, _ := testServer(t)

	t.Run("agent identity, same project: no verdict (nil)", func(t *testing.T) {
		identity := authzHelperAgent(authzHelperProjectA, ScopeAgentLifecycle)
		got := srv.authorizeAgentKeysCrossProject(authzKeysHelperRequest(identity), authzHelperProjectA)
		if got != nil {
			t.Fatalf("expected nil (no verdict) for a same-project agent, got %+v", got)
		}
	})

	t.Run("agent identity, cross project: denied without any target lookup", func(t *testing.T) {
		identity := authzHelperAgent(authzHelperProjectA, ScopeAgentLifecycle)
		got := srv.authorizeAgentKeysCrossProject(authzKeysHelperRequest(identity), authzHelperProjectB)
		if got == nil || got.Allowed {
			t.Fatalf("expected a cross-project agent to be denied, got %+v", got)
		}
		if got.Outcome != agentkeys.OutcomeCrossProjectKeysUnsupported {
			t.Errorf("Outcome = %q, want %q", got.Outcome, agentkeys.OutcomeCrossProjectKeysUnsupported)
		}
	})

	t.Run("human identity: no blanket cross-project ban (nil)", func(t *testing.T) {
		got := srv.authorizeAgentKeysCrossProject(authzKeysHelperRequest(authzHelperAdmin()), authzHelperProjectB)
		if got != nil {
			t.Fatalf("expected nil (no blanket ban) for a human caller, got %+v", got)
		}
	})

	t.Run("nil identity: no verdict (nil)", func(t *testing.T) {
		got := srv.authorizeAgentKeysCrossProject(authzKeysHelperRequest(nil), authzHelperProjectA)
		if got != nil {
			t.Fatalf("expected nil for an unauthenticated caller (the full gate denies it), got %+v", got)
		}
	})
}

// ---------------------------------------------------------------------------
// Route and capability metadata agreement across both route shapes
// ---------------------------------------------------------------------------

// TestAgentActionKeys_RouteMetadataCoversBothRouteShapes pins that both
// mux prefixes that will host the /keys route shapes (contract §2.1) already
// carry RoutePolicy metadata today -- the coarse, pre-dispatch classification
// every action on either route (including a future "keys" action) passes
// through before per-action authorization (agentActionPermission,
// authorizeAgentKeys) refines it further. The two entries' Resource fields
// intentionally differ (top-level is agent-specific; the project-scoped
// prefix is shared by every project sub-resource, not just agents), so
// "agree" here means both routes gate on RoutePolicy -- neither falls back
// to RoutePublic/RouteAuthenticated, which would skip permission checking
// entirely -- not that their coarse metadata is byte-identical. Task 2.2
// adds no new mux pattern for keys (it reuses these existing prefixes, the
// same way message/pty/exec already do), so this table is already the
// metadata keys' own route wiring lands under.
func TestAgentActionKeys_RouteMetadataCoversBothRouteShapes(t *testing.T) {
	topLevel, ok := routeMetadataTable["/api/v1/agents/"]
	if !ok {
		t.Fatal("missing route metadata for /api/v1/agents/ (top-level route shape)")
	}
	projectScoped, ok := routeMetadataTable["/api/v1/projects/"]
	if !ok {
		t.Fatal("missing route metadata for /api/v1/projects/ (project-scoped route shape)")
	}
	if topLevel.Classification != RoutePolicy {
		t.Errorf("top-level route Classification = %v, want %v", topLevel.Classification, RoutePolicy)
	}
	if projectScoped.Classification != RoutePolicy {
		t.Errorf("project-scoped route Classification = %v, want %v", projectScoped.Classification, RoutePolicy)
	}
}

// TestAgentActionKeys_CapabilityProjectionConsistentAcrossRouteShapes proves
// capability computation for a keys-eligible resource does not depend on
// which route shape resolved it: agentResource() is a pure function of the
// already-resolved *store.Agent, called identically by both the top-level
// and project-scoped dispatch paths, so the same agent always projects the
// same "attach" capability (the one keys reuses per decision 2) regardless
// of route shape.
func TestAgentActionKeys_CapabilityProjectionConsistentAcrossRouteShapes(t *testing.T) {
	f := newGoldenFixture(t)
	owner := NewAuthenticatedUser(f.projectOwnerID, "proj-owner@golden.test", "Project Owner", "member", "api")

	// Simulate the two ways an agent record reaches authorization: the
	// top-level route's GetAgent(id) and the project-scoped route's
	// GetAgentBySlug/GetAgent -- both ultimately hand agentResource() the
	// same *store.Agent fields.
	viaTopLevel := agentResource(f.agentAlpha)
	agentCopy := *f.agentAlpha
	viaProjectScoped := agentResource(&agentCopy)

	capTop := f.authz.ComputeCapabilities(t.Context(), owner, viaTopLevel)
	capProject := f.authz.ComputeCapabilities(t.Context(), owner, viaProjectScoped)

	topHasAttach := capabilityAllows(capTop, ActionAttach)
	projectHasAttach := capabilityAllows(capProject, ActionAttach)
	if topHasAttach != projectHasAttach {
		t.Fatalf("capability projection disagrees across route shapes: top-level attach=%v project-scoped attach=%v",
			topHasAttach, projectHasAttach)
	}
	if !topHasAttach {
		t.Fatal("expected the agent's owner to have attach capability (and therefore keys eligibility) on their own agent")
	}
}

// ---------------------------------------------------------------------------
// Registry bookkeeping
// ---------------------------------------------------------------------------

// TestAgentAttachRegistry_EnforcementListsAuthorizeAgentKeys pins the
// contract's explicit instruction (.design/agent-keys-contract.md §3): once
// authorizeAgentKeys lands, agent.attach's Enforcement list must name it, so
// the registry stays an accurate index of what enforces each permission.
func TestAgentAttachRegistry_EnforcementListsAuthorizeAgentKeys(t *testing.T) {
	for _, p := range permissions.Registry {
		if p.ID != "agent.attach" {
			continue
		}
		for _, e := range p.Enforcement {
			if e == "pkg/hub/authorize.go:authorizeAgentKeys" {
				return
			}
		}
		t.Fatalf("agent.attach Enforcement %v does not list authorizeAgentKeys", p.Enforcement)
	}
	t.Fatal("agent.attach permission not found in registry")
}

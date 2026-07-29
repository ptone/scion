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
	"testing"
	"time"

	"github.com/GoogleCloudPlatform/scion/pkg/store"
)

// Regression pins for the N79 arm of #591. The N73 root fix (Role() -> "")
// closes the admin-role promotion; it does NOT close the ID-KEYED fast-path
// short-circuits in the capability evaluators, which key on the MINTING user's
// ID (resource owner, ancestry, project owner) and so still confer access
// outside a UAT's project + scope bounds. The N79 arm is two changes:
//
//	CHANGE (1) skip all 8 fast-path short-circuits for a *ScopedUserIdentity in
//	           ComputeCapabilities / ComputeScopeCapabilities /
//	           ComputeCapabilitiesBatch, falling to the enforced per-action loop.
//	CHANGE (2) run enforceUATConstraints at the TOP of checkAccessPrecomputed —
//	           the batch/precomputed evaluator, which never calls CheckAccess and
//	           so was the ONLY allow path with no scope check.
//
// The acceptance invariant (TestN79_Invariant_*) is: no path may return an
// allow for a *ScopedUserIdentity without enforceUATConstraints returning nil
// for that exact resource and action.
//
// Every pin below is GREEN with the arm and RED without it; the RED proof
// stashes pkg/hub/capabilities.go (the arm), leaving this file to compile
// against the root-fix tree (it references only pre-existing API), so the
// failures are behavioural.

// n79Minter is a plain MEMBER used as the minting user. Membership role is
// irrelevant to a scoped token (Role() is "" for a UAT); what matters per pin is
// the ID-keyed relationship (resource owner / ancestor / project owner) seeded
// on the resource or in the store.
func n79Minter() UserIdentity {
	return NewAuthenticatedUser(tid("n79_minter"), "n79-minter@test.com", "N79 Minter", store.UserRoleMember, "api")
}

// n79StoreUser creates a real store.User (member role) and returns a matching
// UserIdentity. Needed where a minter must be recorded in the store (e.g. as a
// project members-group owner for isProjectOwnerOrAdmin).
func n79StoreUser(t *testing.T, s store.Store, id, name string) UserIdentity {
	t.Helper()
	u := &store.User{ID: id, Email: id + "@test.com", DisplayName: name, Role: store.UserRoleMember, Status: "active", Created: time.Now()}
	if err := s.CreateUser(context.Background(), u); err != nil {
		t.Fatalf("seed user %s: %v", id, err)
	}
	return NewAuthenticatedUser(u.ID, u.Email, u.DisplayName, store.UserRoleMember, "api")
}

// n79ProjectWithMinterAsOwner seeds a project (owned/created by a distinct real
// user, so the members group and its FK-bound owner are created cleanly) and
// promotes minterID to OWNER of the project members group, so
// isProjectOwnerOrAdmin(minterID, project) is true.
func n79ProjectWithMinterAsOwner(t *testing.T, srv *Server, s store.Store, projID, slug, minterID string) *store.Project {
	t.Helper()
	ctx := context.Background()
	owner := &store.User{ID: tid(slug + "_owner"), Email: slug + "-owner@test.com", DisplayName: "Owner", Role: store.UserRoleMember, Status: "active", Created: time.Now()}
	if err := s.CreateUser(ctx, owner); err != nil {
		t.Fatalf("seed project owner user: %v", err)
	}
	proj := &store.Project{ID: projID, Name: slug, Slug: slug, OwnerID: owner.ID, CreatedBy: owner.ID, Created: time.Now(), Updated: time.Now()}
	if err := s.CreateProject(ctx, proj); err != nil {
		t.Fatalf("seed project %s: %v", slug, err)
	}
	srv.createProjectMembersGroupAndPolicy(ctx, proj)
	addProjectMemberWithRole(t, s, proj, minterID, store.GroupMemberRoleOwner)
	return proj
}

// -----------------------------------------------------------------------------
// Pin 9 / axis A — RESOURCE-OWNER bypass (OwnerID == identity.ID()), flipped
// independently. Measured on BOTH the batch fast path (Batch #2, capabilities.go
// :348 — no type assertion) and the slow path (checkAccessPrecomputed #1,
// :415). The resource lives in project B while the token is scoped to project P,
// so the post-fix deny is attributable to the project bound, not to a missing
// owner relationship.
// -----------------------------------------------------------------------------

func TestN79_ResourceOwnerBypass_OutOfScope_Denied(t *testing.T) {
	srv, _ := testServer(t)
	az := srv.authzService
	ctx := context.Background()

	minter := n79Minter()
	scoped := NewScopedUserIdentity(minter, tid("n79_ro_P"), []string{"agent:read"})
	res := Resource{
		Type: "agent", ID: tid("n79_ro_agent"),
		OwnerID:    minter.ID(),
		ParentType: "project", ParentID: tid("n79_ro_B"),
	}

	// Precondition: the bypass is live (owner match) AND enforceUATConstraints
	// denies this exact resource+action, so the pin is a measurement, not a
	// tautology.
	if res.OwnerID != minter.ID() {
		t.Fatalf("test setup: owner relationship not established")
	}
	if d := az.enforceUATConstraints(scoped, res, ActionRead); d == nil {
		t.Fatalf("test setup: enforceUATConstraints nil for out-of-project resource; pin vacuous")
	}

	// Batch #2 (OwnerID fast path).
	caps := az.ComputeCapabilitiesBatch(ctx, scoped, []Resource{res}, "agent")
	if capabilityAllows(caps[0], ActionRead) {
		t.Errorf("batch: resource-owner bypass granted read to a UAT out of its project scope; actions=%v", caps[0].Actions)
	}
	// Slow path #1 (checkAccessPrecomputed resource-owner bypass), measured
	// independently of the batch fast path.
	if d := az.checkAccessPrecomputed(scoped, nil, nil, res, ActionRead); d.Allowed {
		t.Errorf("checkAccessPrecomputed: resource-owner bypass granted read out of scope; reason=%q", d.Reason)
	}
}

// -----------------------------------------------------------------------------
// Pin 9 / axis B — ANCESTRY bypass (identity.ID() in resource.Ancestry), flipped
// independently. Measured on BOTH the batch fast path (Batch #3, :353 — no type
// assertion) and the slow path (checkAccessPrecomputed #2, :421). Out-of-scope
// resource as above.
// -----------------------------------------------------------------------------

func TestN79_AncestryBypass_OutOfScope_Denied(t *testing.T) {
	srv, _ := testServer(t)
	az := srv.authzService
	ctx := context.Background()

	minter := n79Minter()
	scoped := NewScopedUserIdentity(minter, tid("n79_anc_P"), []string{"agent:read"})
	res := Resource{
		Type: "agent", ID: tid("n79_anc_agent"),
		Ancestry:   []string{minter.ID()},
		ParentType: "project", ParentID: tid("n79_anc_B"),
	}

	if !canAccessAsAncestor(minter.ID(), res) {
		t.Fatalf("test setup: ancestry relationship not established")
	}
	if d := az.enforceUATConstraints(scoped, res, ActionRead); d == nil {
		t.Fatalf("test setup: enforceUATConstraints nil for out-of-project resource; pin vacuous")
	}

	// Batch #3 (ancestry fast path).
	caps := az.ComputeCapabilitiesBatch(ctx, scoped, []Resource{res}, "agent")
	if capabilityAllows(caps[0], ActionRead) {
		t.Errorf("batch: ancestry bypass granted read to a UAT out of its project scope; actions=%v", caps[0].Actions)
	}
	// Slow path #2 (checkAccessPrecomputed ancestry bypass).
	if d := az.checkAccessPrecomputed(scoped, nil, nil, res, ActionRead); d.Allowed {
		t.Errorf("checkAccessPrecomputed: ancestry bypass granted read out of scope; reason=%q", d.Reason)
	}
}

// -----------------------------------------------------------------------------
// Pin 9 / axis C — PROJECT-OWNER bypass (isProjectOwnerOrAdmin), flipped
// independently. The minter genuinely owns project B; the token is scoped to a
// different project P. This axis is the one that CROSSES PROJECTS on the
// SINGLE-RESOURCE function (ComputeCapabilities #241, :243) — not just the batch
// (Batch #4, :358). checkAccessPrecomputed carries no project-owner bypass, so
// this axis has no slow-path row.
// -----------------------------------------------------------------------------

func TestN79_ProjectOwnerBypass_CrossProject_Denied(t *testing.T) {
	srv, s := testServer(t)
	az := srv.authzService
	ctx := context.Background()

	minterID := tid("n79_po_minter")
	minter := n79StoreUser(t, s, minterID, "N79 ProjOwner")
	projB := n79ProjectWithMinterAsOwner(t, srv, s, tid("n79_po_B"), "n79-po-b", minterID)

	// Precondition: the project-owner bypass predicate is TRUE, so the deny is the
	// fix, not an absent membership.
	if !az.isProjectOwnerOrAdmin(ctx, minterID, projB.ID) {
		t.Fatalf("test setup: minter is not project owner of B; pin vacuous")
	}

	scoped := NewScopedUserIdentity(minter, tid("n79_po_P"), []string{"agent:read"})
	res := Resource{Type: "agent", ID: tid("n79_po_agent"), ParentType: "project", ParentID: projB.ID}

	// CROSS-PROJECT over-return on the SINGLE-RESOURCE function.
	single := az.ComputeCapabilities(ctx, scoped, res)
	if capabilityAllows(single, ActionRead) {
		t.Errorf("ComputeCapabilities: project-owner bypass granted read to a UAT scoped to a different project; actions=%v", single.Actions)
	}
	// Batch #4 (isProjectOwner closure).
	caps := az.ComputeCapabilitiesBatch(ctx, scoped, []Resource{res}, "agent")
	if capabilityAllows(caps[0], ActionRead) {
		t.Errorf("batch: project-owner bypass granted read out of scope; actions=%v", caps[0].Actions)
	}
}

// -----------------------------------------------------------------------------
// Pin 9 / carol witness — pins CHANGE (2) SPECIFICALLY. carol is a plain member:
// she owns nothing, is no resource's ancestor, and is owner/admin of no project,
// so she trips NONE of the fast-path short-circuits — CHANGE (1) is inert for
// her by construction. Her only route to an allow is evaluatePolicies inside
// checkAccessPrecomputed (a hub-wide read policy), which had no scope check
// before CHANGE (2). The token is scoped to project P; the resource is in
// project R. Even a MATCHING agent:read scope must not save the read — the
// project bound denies first. Pre-fix the batch over-returns (an allow) while
// the single-resource function already denies; post-fix the two agree at deny.
//
// NOTE on severity: dev5 measured this over-return as a batch row of 1-of-7.
// That is a FLOOR from a single witness, not a magnitude — do not treat 1-of-7
// as the severity of the finding.
// -----------------------------------------------------------------------------

func TestN79_CarolWitness_EvaluatePoliciesPath_Change2(t *testing.T) {
	srv, s := testServer(t)
	az := srv.authzService
	ctx := context.Background()

	carolID := tid("n79_carol")
	carol := &store.User{
		ID: carolID, Email: "carol@test.com", DisplayName: "Carol",
		Role: store.UserRoleMember, Status: "active", Created: time.Now(),
	}
	if err := s.CreateUser(ctx, carol); err != nil {
		t.Fatalf("seed carol: %v", err)
	}
	ensureHubMembership(ctx, s, carolID)

	// A hub-wide read-all policy bound directly to carol: the "global read" that
	// lets the batch slow path over-return.
	pol := &store.Policy{
		ID: tid("n79_carol_pol"), Name: "carol read agents",
		ScopeType: "hub", ResourceType: "agent", Actions: []string{"read"}, Effect: "allow",
	}
	if err := s.CreatePolicy(ctx, pol); err != nil {
		t.Fatalf("seed policy: %v", err)
	}
	if err := s.AddPolicyBinding(ctx, &store.PolicyBinding{
		PolicyID: pol.ID, PrincipalType: "user", PrincipalID: carolID,
	}); err != nil {
		t.Fatalf("bind policy: %v", err)
	}

	minter := NewAuthenticatedUser(carolID, carol.Email, carol.DisplayName, store.UserRoleMember, "api")
	scoped := NewScopedUserIdentity(minter, tid("n79_carol_P"), []string{"agent:read"})
	res := Resource{Type: "agent", ID: tid("n79_carol_agent"), ParentType: "project", ParentID: tid("n79_carol_R")}

	// Preconditions (so the pin is a measurement): carol's policy DOES grant read
	// (the over-return source), and enforceUATConstraints denies this exact
	// resource+action (the bound CHANGE (2) enforces). carol trips no fast path,
	// so CHANGE (1) cannot be what closes this — CHANGE (2) is.
	_, policies := az.precomputeForIdentity(ctx, scoped)
	if d := az.evaluatePolicies(policies, Resource{Type: "agent", ID: res.ID}, ActionRead); !d.Allowed {
		t.Fatalf("test setup: carol's global read policy did not grant read; pin vacuous")
	}
	if d := az.enforceUATConstraints(scoped, res, ActionRead); d == nil {
		t.Fatalf("test setup: enforceUATConstraints nil for out-of-project resource; pin vacuous")
	}

	// Batch (checkAccessPrecomputed -> evaluatePolicies): must go to deny.
	caps := az.ComputeCapabilitiesBatch(ctx, scoped, []Resource{res}, "agent")
	if capabilityAllows(caps[0], ActionRead) {
		t.Errorf("carol batch over-return: read granted to a UAT out of its project scope via evaluatePolicies; actions=%v", caps[0].Actions)
	}
	// Parity with the single-resource function, which already enforced the bound.
	if capabilityAllows(az.ComputeCapabilities(ctx, scoped, res), ActionRead) {
		t.Errorf("carol single-resource read unexpectedly allowed")
	}
}

// -----------------------------------------------------------------------------
// Pin 9 / scope-inert — the ID-keyed bypass keys on ID(), never on scopes: the
// token's scopes are INERT on this path. Empty, matching, broad, and unrelated
// scope sets all reach the SAME refusal, because project confinement (B != P)
// denies regardless. (Pre-fix, all four leak identically for the same reason:
// the owner bypass ignores scopes.)
// -----------------------------------------------------------------------------

func TestN79_ScopeInert_OwnerBypass_SameRefusal(t *testing.T) {
	srv, _ := testServer(t)
	az := srv.authzService
	ctx := context.Background()
	minter := n79Minter()

	res := Resource{
		Type: "agent", ID: tid("n79_si_agent"),
		OwnerID:    minter.ID(),
		ParentType: "project", ParentID: tid("n79_si_B"),
	}
	pProject := tid("n79_si_P")

	scopeSets := map[string][]string{
		"empty":     nil,
		"matching":  {"agent:read"},
		"broad":     {"agent:read", "agent:update", "agent:delete", "agent:create"},
		"unrelated": {"skill:read"},
	}
	for name, scopes := range scopeSets {
		scoped := NewScopedUserIdentity(minter, pProject, scopes)
		caps := az.ComputeCapabilitiesBatch(ctx, scoped, []Resource{res}, "agent")
		if capabilityAllows(caps[0], ActionRead) {
			t.Errorf("scope set %q: owner bypass leaked read out of project scope (scopes are inert on this path); actions=%v", name, caps[0].Actions)
		}
	}
}

// -----------------------------------------------------------------------------
// Pin 9 / severity-split — a WRITE grant, pinned distinctly from the read rows.
// The scope-level project-owner short-circuit (ComputeScopeCapabilities #282,
// :283) returns allActions, which for agent SCOPE actions includes create. A
// read-only token (scoped to the SAME project B, but carrying only agent:list)
// is thereby handed a WRITE (create) capability. The token stays in project B so
// ONLY the scope confinement is what denies create post-fix, isolating the write
// aspect; the legitimately-scoped list survives (no over-restriction).
// -----------------------------------------------------------------------------

func TestN79_SeveritySplit_ScopeCreateToReadOnlyToken_WriteGrantRemoved(t *testing.T) {
	srv, s := testServer(t)
	az := srv.authzService
	ctx := context.Background()

	minterID := tid("n79_sev_minter")
	minter := n79StoreUser(t, s, minterID, "N79 Sev")
	projB := n79ProjectWithMinterAsOwner(t, srv, s, tid("n79_sev_B"), "n79-sev-b", minterID)
	if !az.isProjectOwnerOrAdmin(ctx, minterID, projB.ID) {
		t.Fatalf("test setup: minter not project owner; pin vacuous")
	}

	scoped := NewScopedUserIdentity(minter, projB.ID, []string{"agent:list"})
	cap := az.ComputeScopeCapabilities(ctx, scoped, "project", projB.ID, "agent")

	// SEVERITY: the write action must NOT be granted (distinct from read rows).
	if capabilityAllows(cap, ActionCreate) {
		t.Errorf("WRITE-GRANT: scope-level create handed to a read-only (agent:list) token via the project-owner bypass; actions=%v", cap.Actions)
	}
	// Not over-restricted: the action the token IS scoped for still works.
	if !capabilityAllows(cap, ActionList) {
		t.Errorf("over-restriction: agent:list denied to a token scoped for it; actions=%v", cap.Actions)
	}
}

// -----------------------------------------------------------------------------
// Pin 9 / ACCEPTANCE INVARIANT — no path may return an allow for a
// *ScopedUserIdentity without enforceUATConstraints returning nil for that exact
// resource and action. Checked across every ID-keyed bypass axis and every
// resource action, on all three evaluator surfaces. A liveness assertion keeps
// the invariant from being vacuously satisfied by denying everything.
// -----------------------------------------------------------------------------

func TestN79_Invariant_AllowImpliesEnforceUATConstraintsNil(t *testing.T) {
	srv, s := testServer(t)
	az := srv.authzService
	ctx := context.Background()

	minterID := tid("n79_inv_minter")
	minter := n79StoreUser(t, s, minterID, "N79 Inv")
	projB := n79ProjectWithMinterAsOwner(t, srv, s, tid("n79_inv_B"), "n79-inv-b", minterID)

	pProject := tid("n79_inv_P")
	scoped := NewScopedUserIdentity(minter, pProject, []string{"agent:read"})

	resources := []Resource{
		// owner bypass, out of scope (project B):
		{Type: "agent", ID: tid("n79_inv_owned"), OwnerID: minterID, ParentType: "project", ParentID: projB.ID},
		// ancestry bypass, out of scope:
		{Type: "agent", ID: tid("n79_inv_anc"), Ancestry: []string{minterID}, ParentType: "project", ParentID: projB.ID},
		// project-owner bypass, out of scope:
		{Type: "agent", ID: tid("n79_inv_projowned"), ParentType: "project", ParentID: projB.ID},
		// legitimate: owned resource in the token's own project P, read-scoped:
		{Type: "agent", ID: tid("n79_inv_inscope"), OwnerID: minterID, ParentType: "project", ParentID: pProject},
	}

	for _, res := range resources {
		for _, action := range ResourceActions["agent"] {
			euc := az.enforceUATConstraints(scoped, res, action)
			if euc == nil {
				continue // an allow here is permitted by the invariant
			}
			if capabilityAllows(az.ComputeCapabilities(ctx, scoped, res), action) {
				t.Errorf("INVARIANT VIOLATED (ComputeCapabilities): allow for %s:%s while enforceUATConstraints denies (%s)", res.ID, action, euc.Reason)
			}
			caps := az.ComputeCapabilitiesBatch(ctx, scoped, []Resource{res}, "agent")
			if capabilityAllows(caps[0], action) {
				t.Errorf("INVARIANT VIOLATED (batch): allow for %s:%s while enforceUATConstraints denies (%s)", res.ID, action, euc.Reason)
			}
			if d := az.checkAccessPrecomputed(scoped, nil, nil, res, action); d.Allowed {
				t.Errorf("INVARIANT VIOLATED (checkAccessPrecomputed): allow for %s:%s while enforceUATConstraints denies (%s)", res.ID, action, euc.Reason)
			}
		}
	}

	// Liveness: the in-scope owned read IS allowed (enforceUATConstraints nil),
	// so the invariant is not vacuously satisfied by a blanket deny.
	inScope := resources[3]
	if !capabilityAllows(az.ComputeCapabilities(ctx, scoped, inScope), ActionRead) {
		t.Errorf("liveness: the in-scope owned read was denied; the invariant test is vacuous")
	}
}

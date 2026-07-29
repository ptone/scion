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
	"github.com/go-jose/go-jose/v4/jwt"
)

// Regression pins for the N79 arm of #591. The N73 root fix (Role() -> "")
// closes the admin-role promotion; it does NOT close the ID-KEYED fast-path
// short-circuits in the capability evaluators, which key on the MINTING user's
// ID (resource owner, ancestry, project owner) and so still confer access
// outside a UAT's project + scope bounds. The N79 arm is three changes:
//
//	CHANGE (1) skip all 8 fast-path short-circuits for a *ScopedUserIdentity in
//	           ComputeCapabilities / ComputeScopeCapabilities /
//	           ComputeCapabilitiesBatch, falling to the enforced per-action loop.
//	CHANGE (2) run enforceUATConstraints at the TOP of checkAccessPrecomputed —
//	           the batch/precomputed evaluator, which never calls CheckAccess and
//	           so was the ONLY allow path with no scope check.
//	CHANGE (3) COARSE type gate at the batch OwnerID/ancestry fast paths: only the
//	           user family enters; every non-user identity (broker, agent, ...)
//	           skips them. Change 1 is the FINE gate nested inside it (a scoped
//	           UAT, though user-family, still skips). This closes T1 — a live
//	           forged-broker leak whose ID collides with a victim user id.
//
// Neither layer alone satisfies the invariant: change 3 admits a *ScopedUserIdentity
// (it is user-family) so change 1 is still needed to stop the UAT scope escape;
// change 1 does nothing for a broker so change 3 is still needed to stop the
// forged-broker leak. The pins below therefore FAIL if EITHER change is reverted.
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

// -----------------------------------------------------------------------------
// Pin 9 / T1 — FORGED-BROKER batch leak, pinning CHANGE (3) as a DIFFERENTIAL.
// A broker identity implements only ID()/Type()/BrokerID() — no Email/
// DisplayName/Role — so it is NOT a UserIdentity. The batch OwnerID/ancestry
// fast paths key on bare identity.ID(), so a self-minted broker whose ID
// collides with a victim user id trips them and lists the victim's owned
// resources. This leaks TODAY, independent of F3b.
//
// change 3 gates the fast paths on the user family, so the forged broker flips
// from allow-on-all to deny-on-all — MATCHING a non-colliding control broker
// (always denied) — WHILE the genuine victim user keeps batch access on its
// owned and descendant rows. The guard closes the broker without regressing the
// user. change 1 (scoped skip) does nothing for a broker, so this pin FAILS if
// change 3 is reverted; the scoped-UAT pins above FAIL if change 1 is reverted.
//
// The forged-broker assertion uses the PRODUCTION resource shape and the
// ancestry-only shape (#591 / N87). Every user-created agent carries BOTH OwnerID
// and Ancestry (handlers_agents_core.go sets ancestry=[userID] and OwnerID=
// createdBy; agentResource copies both), so the owner-ONLY fixture used by the
// earlier version of this pin is NEVER produced by the create path — and that fix-
// ture is closed by change 3 (fast path) + the UserIdentity-guarded owner bypass,
// which manufactured a green while the LIVE shapes leaked. On the production and
// ancestry-only shapes the forged broker reached the bare-ID ancestry grant in
// checkAccessPrecomputed (the slow-path twin of the fast paths), so N87 gates that
// grant to the user-or-agent family. This pin therefore asserts the forged broker
// is DENIED on BOTH live shapes, keeps the genuine-user no-regression check, and
// adds a genuine-AGENT-ancestor no-regression arm (the guard is user-OR-agent, not
// user-only; a legitimate descendant agent must still get ancestor access).
// -----------------------------------------------------------------------------

func TestN79_T1_ForgedBrokerBatch_Differential(t *testing.T) {
	srv, s := testServer(t)
	az := srv.authzService
	ctx := context.Background()

	victimID := tid("n79_t1_victim")
	victim := n79StoreUser(t, s, victimID, "N79 T1 Victim")
	agentActions := ResourceActions["agent"]

	// PRODUCTION shape: a real user-created agent carries BOTH OwnerID and Ancestry
	// set to its creator. This is the shape the create path actually produces.
	production := Resource{Type: "agent", ID: tid("n79_t1_prod"), OwnerID: victimID, Ancestry: []string{victimID}, ParentType: "project", ParentID: tid("n79_t1_proj")}
	// ANCESTRY-ONLY shape: a descendant of the victim with no direct OwnerID match,
	// isolating the bare-ID ancestry grant that N87 closes.
	ancestryOnly := Resource{Type: "agent", ID: tid("n79_t1_anc"), Ancestry: []string{victimID}, ParentType: "project", ParentID: tid("n79_t1_proj")}

	forged := NewBrokerIdentity(victimID)               // ID collides with the victim
	control := NewBrokerIdentity(tid("n79_t1_control")) // non-colliding broker

	// Preconditions: the forged broker's ID matches the victim (the leak primitive)
	// and it satisfies NEITHER UserIdentity NOR AgentIdentity, so the N87 guard
	// denies it before canAccessAsAncestor.
	if forged.ID() != victimID {
		t.Fatalf("test setup: forged broker id does not collide with victim; pin vacuous")
	}
	if _, ok := forged.(UserIdentity); ok {
		t.Fatalf("test setup: broker unexpectedly satisfies UserIdentity")
	}
	if _, ok := forged.(AgentIdentity); ok {
		t.Fatalf("test setup: broker unexpectedly satisfies AgentIdentity")
	}

	// DIFFERENTIAL 1: forged broker DENIED on BOTH live shapes (production and
	// ancestry-only), MATCHING the non-colliding control broker (also denied). Prior
	// to N87 the forged broker was granted 7/7 on both via the bare-ID :486 grant.
	fCaps := az.ComputeCapabilitiesBatch(ctx, forged, []Resource{production, ancestryOnly}, "agent")
	if len(fCaps[0].Actions) != 0 {
		t.Errorf("N87 forged-broker leak (production shape, owner+ancestry): batch granted %d/%d actions via the bare-ID ancestry grant; actions=%v",
			len(fCaps[0].Actions), len(agentActions), fCaps[0].Actions)
	}
	if len(fCaps[1].Actions) != 0 {
		t.Errorf("N87 forged-broker leak (ancestry-only shape): batch granted %d/%d actions via the bare-ID ancestry grant; actions=%v",
			len(fCaps[1].Actions), len(agentActions), fCaps[1].Actions)
	}
	cCaps := az.ComputeCapabilitiesBatch(ctx, control, []Resource{production, ancestryOnly}, "agent")
	if len(cCaps[0].Actions) != 0 || len(cCaps[1].Actions) != 0 {
		t.Errorf("control (non-colliding) broker unexpectedly granted %v/%v; forged and control brokers must agree at deny", cCaps[0].Actions, cCaps[1].Actions)
	}

	// DIFFERENTIAL 2: the genuine victim USER keeps batch access on BOTH shapes
	// (the guard must not regress real users).
	uCaps := az.ComputeCapabilitiesBatch(ctx, victim, []Resource{production, ancestryOnly}, "agent")
	if len(uCaps[0].Actions) != len(agentActions) {
		t.Errorf("regression: victim user lost production-row access; got %v want all %d actions", uCaps[0].Actions, len(agentActions))
	}
	if len(uCaps[1].Actions) != len(agentActions) {
		t.Errorf("regression: victim user lost ancestry-only-row access; got %v want all %d actions", uCaps[1].Actions, len(agentActions))
	}

	// DIFFERENTIAL 3 (N87): a genuine AGENT ancestor keeps ancestor access — the
	// guard is user-OR-agent, not user-only. The agent's ID is a legitimate
	// ancestor of the resource; it is not a UserIdentity, so it reaches the guarded
	// :486 grant as an AgentIdentity and must be granted.
	agentAncestorID := tid("n79_t1_agent_anc")
	var agentAncestor AgentIdentity = &agentIdentityWrapper{&AgentTokenClaims{
		Claims:    jwt.Claims{Subject: agentAncestorID},
		ProjectID: tid("n79_t1_proj"),
	}}
	agentDescendant := Resource{Type: "agent", ID: tid("n79_t1_agent_desc"), Ancestry: []string{agentAncestorID}, ParentType: "project", ParentID: tid("n79_t1_proj")}
	aCaps := az.ComputeCapabilitiesBatch(ctx, agentAncestor, []Resource{agentDescendant}, "agent")
	if len(aCaps[0].Actions) != len(agentActions) {
		t.Errorf("regression: genuine AGENT ancestor lost descendant access under the N87 user-or-agent guard; got %v want all %d actions", aCaps[0].Actions, len(agentActions))
	}
}

// -----------------------------------------------------------------------------
// Pointer-receiver foot-gun lock (rev1 latent finding on the N73 root fix).
// ScopedUserIdentity.Role() is a POINTER-receiver override of the embedded
// UserIdentity's promoted Role(). Because it is declared on the outer type it
// shadows the promoted method for value selection too, so a VALUE-typed
// ScopedUserIdentity has NO Role() in its value method set and does NOT satisfy
// UserIdentity — CheckAccess rejects it as "invalid user identity" (fail-closed).
//
// The hazard rev1 flagged is a FUTURE "hardening" that switches Role() to a
// VALUE receiver: that would put Role() (empty) into the value method set,
// making a value SATISFY UserIdentity while STILL escaping the *ScopedUserIdentity
// assertion that gates enforceUATConstraints (a value is not the pointer type).
// Such a value would then be treated as an UNSCOPED member and take the owner
// bypass with no scope enforcement. This pin locks the current fail-closed
// property so any such receiver flip goes RED here.
//
// Measured both ways: with the current pointer receiver the value fails to
// satisfy UserIdentity and CheckAccess denies "invalid user identity"; with a
// value receiver the value satisfies UserIdentity and CheckAccess returns
// allow "resource owner" (the regression this pin catches).
//
// #591 (N89): arms 1-4 exercise the SINGLE path (CheckAccess). rev1 measured that
// a value was NOT fail-closed on the BATCH path before the N87 :486 allowlist —
// batched over an ancestry row whose ancestor is the minting user, the value took
// the bare-ID ancestry grant in checkAccessPrecomputed and was list-admitted 7/7.
// Arm 5 asserts the batch path too, closed by the same N87 allowlist landing in
// this commit (a value is neither UserIdentity nor AgentIdentity), so the value is
// fail-closed on BOTH paths and the unqualified name is honest — no interim rename
// is needed because the code fix and this control land together.
func TestN79_PointerReceiverFootgun_ValueIsFailClosed(t *testing.T) {
	srv, _ := testServer(t)
	az := srv.authzService
	ctx := context.Background()

	minter := n79Minter()
	// A VALUE (not pointer) ScopedUserIdentity, the exact shape rev1's foot-gun
	// warns a future refactor could reintroduce as a caller construction.
	val := *NewScopedUserIdentity(minter, tid("n79_fg_P"), []string{"agent:read"})

	// 1. Fail-closed at the type layer: a value must NOT satisfy UserIdentity
	//    (pointer-receiver Role() shadows the promoted method out of the value
	//    method set). A value-receiver flip makes this true and trips the pin.
	if _, ok := any(val).(UserIdentity); ok {
		t.Errorf("value ScopedUserIdentity satisfies UserIdentity; pointer-receiver Role() must keep it out of the value method set (a value-receiver flip re-opens the N73 bypass)")
	}
	// 2. A value is not the pointer type, so it also escapes the
	//    *ScopedUserIdentity assertion that gates enforceUATConstraints — which
	//    is exactly why (1) fail-closed is load-bearing.
	if _, ok := any(val).(*ScopedUserIdentity); ok {
		t.Errorf("value unexpectedly matched *ScopedUserIdentity")
	}
	// 3. Behavioural lock: CheckAccess on an OWNER-MATCHED resource must DENY.
	//    Under the pointer receiver it denies "invalid user identity"; a
	//    value-receiver flip would route to the owner bypass and allow
	//    "resource owner" with no scope enforcement.
	res := Resource{Type: "agent", ID: tid("n79_fg_agent"), OwnerID: minter.ID(), ParentType: "project", ParentID: tid("n79_fg_B")}
	if d := az.CheckAccess(ctx, val, res, ActionRead); d.Allowed {
		t.Errorf("CheckAccess granted a VALUE ScopedUserIdentity on an owner-matched resource (reason=%q); value must be fail-closed, not treated as an unscoped owner", d.Reason)
	}
	// 4. The pointer construction is unaffected: it satisfies UserIdentity and
	//    Role() is the fail-closed empty string (never the minting admin).
	ptr := NewScopedUserIdentity(minter, tid("n79_fg_P"), []string{"agent:read"})
	if _, ok := any(ptr).(UserIdentity); !ok {
		t.Errorf("pointer ScopedUserIdentity must satisfy UserIdentity")
	}
	if r := ptr.Role(); r != "" {
		t.Errorf("Role() = %q, want \"\" (fail-closed for a UAT)", r)
	}

	// 5. BATCH-PATH lock (#591 / N89). Batched over an ancestry row whose ancestor
	//    is the minting user, the value must get 0/7 — the N87 :486 allowlist denies
	//    it (neither UserIdentity nor AgentIdentity) before canAccessAsAncestor.
	//    Pre-N87 this was 7/7 (the batch-path miss twin of the single-path arms).
	ancRow := Resource{Type: "agent", ID: tid("n79_fg_anc"), Ancestry: []string{minter.ID()}, ParentType: "project", ParentID: tid("n79_fg_P2")}
	vCaps := az.ComputeCapabilitiesBatch(ctx, val, []Resource{ancRow}, "agent")
	if len(vCaps[0].Actions) != 0 {
		t.Errorf("value ScopedUserIdentity granted %d/%d on an ancestry row via the BATCH path; the N87 :486 allowlist must deny a non-user-family value; actions=%v",
			len(vCaps[0].Actions), len(ResourceActions["agent"]), vCaps[0].Actions)
	}
}

// -----------------------------------------------------------------------------
// N87 identity-kind matrix on the checkAccessPrecomputed ancestry grant (:486).
// The ruling requires EVERY concrete Identity kind bucketed on one ancestry row,
// so a future kind cannot silently re-open the leak ("identity kind seven"). Each
// row constructs an identity whose ID is the sole ancestor of an ancestry-only
// resource and asserts allow/deny through ComputeCapabilitiesBatch. The gate is a
// POSITIVE ALLOWLIST (UserIdentity OR AgentIdentity proceed; all else denied), so
// the deny cells fail CLOSED for any unknown future kind.
//
// The seven concrete Identity implementers in this package:
//   *AuthenticatedUser (user)    -> allow via user-family ancestry
//   *DevUser (dev)               -> allow (user-family; admin-role over-determines)
//   *ScopedUserIdentity (ptr)    -> DENY, but by enforceUATConstraints (change 2),
//                                    NOT :486 — bounded out of its project
//   ScopedUserIdentity (value)   -> DENY at :486 (neither UserIdentity nor
//                                    AgentIdentity) — the N89 value residual
//   *agentIdentityWrapper (agent)-> allow via agent-family ancestry (:486)
//   *evaluateAgentIdentity(agent)-> allow via agent-family ancestry (:486) — kind 7
//   *brokerIdentityImpl (broker) -> DENY at :486 — the N87 forged-broker leak
// -----------------------------------------------------------------------------

func TestN87_AncestryGrant_IdentityKindMatrix(t *testing.T) {
	srv, _ := testServer(t)
	az := srv.authzService
	ctx := context.Background()
	nAgent := len(ResourceActions["agent"])

	proj := tid("n87m_proj")
	otherProj := tid("n87m_other")

	memberID := tid("n87m_member")
	member := NewAuthenticatedUser(memberID, "n87m-member@test.com", "N87M Member", store.UserRoleMember, "api")

	dev := NewDevUser(DevUserConfig{Username: "n87m-dev", DisplayName: "N87M Dev", Email: "n87m-dev@test.com"})

	scopedMinterID := tid("n87m_scoped_minter")
	scopedMinter := NewAuthenticatedUser(scopedMinterID, "n87m-sm@test.com", "N87M SM", store.UserRoleMember, "api")
	// Scoped to otherProj while the row lives in proj: enforceUATConstraints denies.
	scopedPtr := NewScopedUserIdentity(scopedMinter, otherProj, []string{"agent:read", "agent:update", "agent:delete"})

	valMinterID := tid("n87m_val_minter")
	valMinter := NewAuthenticatedUser(valMinterID, "n87m-vm@test.com", "N87M VM", store.UserRoleMember, "api")
	valScoped := *NewScopedUserIdentity(valMinter, otherProj, []string{"agent:read"})

	agentID := tid("n87m_agent")
	agentWrapper := &agentIdentityWrapper{&AgentTokenClaims{Claims: jwt.Claims{Subject: agentID}, ProjectID: proj}}

	evalAgentID := tid("n87m_evalagent")
	evalAgent := &evaluateAgentIdentity{id: evalAgentID, projectID: proj}

	brokerID := tid("n87m_broker")
	broker := NewBrokerIdentity(brokerID)

	cases := []struct {
		name       string
		identity   Identity
		ancestorID string // put into the row's Ancestry; the identity's own ID
		wantAllow  bool
		closedBy   string
	}{
		{"AuthenticatedUser(member)", member, memberID, true, "user-family ancestry"},
		{"DevUser(dev)", dev, dev.ID(), true, "user-family (admin over-determines)"},
		{"ScopedUserIdentity(ptr,out-of-scope)", scopedPtr, scopedMinterID, false, "enforceUATConstraints (change 2)"},
		{"ScopedUserIdentity(value)", valScoped, valMinterID, false, ":486 allowlist (N89)"},
		{"agentIdentityWrapper(agent)", agentWrapper, agentID, true, "agent-family ancestry (:486)"},
		{"evaluateAgentIdentity(agent)", evalAgent, evalAgentID, true, "agent-family ancestry (:486)"},
		{"brokerIdentityImpl(broker)", broker, brokerID, false, ":486 allowlist (N87)"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			row := Resource{Type: "agent", ID: tid("n87m_row_" + tc.name), Ancestry: []string{tc.ancestorID}, ParentType: "project", ParentID: proj}
			caps := az.ComputeCapabilitiesBatch(ctx, tc.identity, []Resource{row}, "agent")
			got := len(caps[0].Actions)
			if tc.wantAllow && got != nAgent {
				t.Errorf("%s: got %d/%d actions, want ALLOW (all %d) via %s; actions=%v", tc.name, got, nAgent, nAgent, tc.closedBy, caps[0].Actions)
			}
			if !tc.wantAllow && got != 0 {
				t.Errorf("%s: got %d/%d actions, want DENY (0) closed by %s; actions=%v", tc.name, got, nAgent, tc.closedBy, caps[0].Actions)
			}
		})
	}
}

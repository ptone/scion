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
	"time"

	"github.com/GoogleCloudPlatform/scion/pkg/secret"
	"github.com/GoogleCloudPlatform/scion/pkg/store"
)

// Regression pins for the N73 root fix (#591): ScopedUserIdentity gives an
// explicit Role() override returning the empty string so an admin-minted,
// project-scoped User Access Token (UAT) never PROMOTES the minting user's
// "admin" role into a hub-wide authority answer. Every pin below is written so
// that it is GREEN with the fix and RED without it; the RED proof is done by
// `git stash`-ing the three code files (identity.go, handlers_auth.go,
// auth.go) — this test file references only pre-existing API (Role(), ID(),
// requireAdmin, the capability functions, the list handlers, and /auth/me's
// JSON, decoded into a map) so that it still COMPILES against the pre-fix tree
// and the failures are behavioural, not compile errors.
//
// The minting user in these pins is a genuine hub admin; the token is what is
// scoped. Without the override, Role() answers "admin" for the token and each
// pin's attack arm passes where it must fail.

// n73Request builds a request carrying the given identity in context, so a
// handler can be called directly (bypassing auth middleware) with a synthetic
// UAT identity — mirroring authzHelperRequest but with a caller-chosen method
// and target.
func n73Request(method, target string, identity Identity) *http.Request {
	req := httptest.NewRequest(method, target, nil)
	if identity != nil {
		req = req.WithContext(contextWithIdentity(req.Context(), identity))
	}
	return req
}

// n73AdminUser is a genuine hub admin used as the MINTING user for the scoped
// tokens. Its ID is deliberately absent from every seeded project group, so the
// project-owner/admin short-circuits (isProjectOwnerOrAdmin) never fire for it
// and a denial is attributable to the Role() override alone, not to a missing
// membership. Where a control needs the minting user to own a resource, the
// resource's OwnerID is set to this ID explicitly.
func n73AdminUser() UserIdentity {
	return NewAuthenticatedUser("n73-admin", "n73-admin@test.com", "N73 Admin", store.UserRoleAdmin, "api")
}

// -----------------------------------------------------------------------------
// Pin 1 — VALUE: Role() is empty (and != admin) for a UAT, but ID/Email/
// DisplayName still carry the minting user (attribution intact, no overshoot).
// -----------------------------------------------------------------------------

func TestScopedUserIdentity_RoleEmpty_AttributionIntact(t *testing.T) {
	admin := n73AdminUser()
	scoped := NewScopedUserIdentity(admin, "n73-project", []string{"hub:manage"})

	// The load-bearing assertion: a UAT answers no hub-wide authority question.
	if got := scoped.Role(); got != "" {
		t.Errorf("ScopedUserIdentity.Role() = %q, want \"\" (a UAT must not promote the minting role)", got)
	}
	if scoped.Role() == store.UserRoleAdmin {
		t.Error("ScopedUserIdentity.Role() must never equal store.UserRoleAdmin")
	}

	// Anti-overshoot: identity/attribution fields still resolve to the minting
	// user. Only Role() changes.
	if got := scoped.ID(); got != admin.ID() {
		t.Errorf("ID() = %q, want minting user %q", got, admin.ID())
	}
	if got := scoped.Email(); got != admin.Email() {
		t.Errorf("Email() = %q, want minting user %q", got, admin.Email())
	}
	if got := scoped.DisplayName(); got != admin.DisplayName() {
		t.Errorf("DisplayName() = %q, want minting user %q", got, admin.DisplayName())
	}
}

// -----------------------------------------------------------------------------
// Pin 2 — STRUCTURAL: *ScopedUserIdentity declares its OWN Role method (it does
// not promote the embedded interface's). Proven by construction with a nil
// embedded UserIdentity: the override returns "" without touching the embed,
// whereas a PROMOTED Role() would dispatch on the nil interface and panic. This
// is the pin that survives refactors — it catches a future maintainer deleting
// the override (re-embedding the promotion) by NAME/shape, not by a value that
// a policy could coincidentally reproduce.
// -----------------------------------------------------------------------------

func TestScopedUserIdentityDoesNotPromoteRole(t *testing.T) {
	// Deliberately nil embedded UserIdentity.
	scoped := &ScopedUserIdentity{}

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("ScopedUserIdentity.Role() panicked (%v): Role() is being PROMOTED "+
				"from the embedded UserIdentity instead of declared on the type — the "+
				"#591/N73 override was removed", r)
		}
	}()

	if got := scoped.Role(); got != "" {
		t.Errorf("Role() on a nil-embed ScopedUserIdentity = %q, want \"\"", got)
	}
}

// -----------------------------------------------------------------------------
// Pin 3 — requireAdmin: an admin-minted UAT holding the hub:manage scope is
// still denied 403. This is the sharp root-fix arm, distinct from the B7 pin
// (TestRequireAdmin_ScopedUATRejected, which reds without B7's zero-scope
// enforceUATConstraints call). requireAdmin builds resource {Type:"hub"} and
// action "manage"; enforceUATConstraints skips the project check for a hub
// resource and the scope "hub:manage" is satisfied, so the token PASSES
// enforceUATConstraints. The only thing that then denies it is Role() != admin
// — i.e. the root fix. Without the override, Role() promotes "admin" and the
// token is (wrongly) admitted.
// -----------------------------------------------------------------------------

func TestRequireAdmin_ScopedUAT_HubManageScope_RootFix(t *testing.T) {
	srv, _ := testServer(t)

	adminUAT := NewScopedUserIdentity(n73AdminUser(), "n73-project", []string{"hub:manage"})

	rec := httptest.NewRecorder()
	user, ok := srv.requireAdmin(rec, n73Request(http.MethodGet, "/api/v1/some-admin-route", adminUAT))
	if ok {
		t.Fatalf("hub:manage-scoped admin-minted UAT was admitted by requireAdmin (ok=true, user=%v); "+
			"it passes enforceUATConstraints, so only Role()==\"\" (the root fix) can deny it", user)
	}
	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403 (body: %s)", rec.Code, rec.Body.String())
	}
}

// -----------------------------------------------------------------------------
// Pin 4 — S2, keyed BY THE CONSTANT store.UserRoleAdmin: the hub-scoped secrets
// read path denies the same admin-minted UAT. resolveEnvSecretAccess's ScopeHub
// arm gates on `userIdent.Role() != store.UserRoleAdmin`; with the fix Role() is
// "" and the token is Forbidden. A literal-keyed test ("admin") would have
// missed this site — this pins the constant.
// -----------------------------------------------------------------------------

func TestSecretsHubScope_ScopedAdminUAT_Denied(t *testing.T) {
	srv, s := testServer(t)
	// Backend set so the pre-fix path would reach List() and answer 200; the
	// fix must deny before that.
	srv.SetSecretBackend(secret.NewLocalBackend(s, "test-hub-id"))

	adminUAT := NewScopedUserIdentity(n73AdminUser(), "n73-project", []string{"hub:manage"})

	rec := httptest.NewRecorder()
	srv.listSecrets(rec, n73Request(http.MethodGet, "/api/v1/secrets?scope=hub", adminUAT))
	if rec.Code != http.StatusForbidden {
		t.Errorf("hub-scoped secrets read under an admin-minted UAT: status = %d, want 403 (body: %s)",
			rec.Code, rec.Body.String())
	}
}

// -----------------------------------------------------------------------------
// Pin 5 — SINKS. Each list handler filters items through a per-item ActionRead
// capability. With the admin bypass gone, an admin-minted UAT sees only what
// its underlying identity can actually reach (owner/membership/policy/public),
// NOT the whole hub. Four separate handlers, four separate tests: the bug was
// present at every read sink, and one test cannot establish which handler it
// exercised.
// -----------------------------------------------------------------------------

func TestListProjects_ScopedAdminUAT_SubsetOnly(t *testing.T) {
	srv, s := testServer(t)
	ctx := context.Background()
	admin := n73AdminUser()

	// P is owned by the minting admin. Under the N79 arm the batch list path no
	// longer lets the owner short-circuit BYPASS the token's scope (that bypass is
	// skipped for a ScopedUserIdentity and the request now flows through
	// enforceUATConstraints), so the token is given project:read below and P stays
	// visible via the owner bypass INSIDE checkAccessPrecomputed, gated behind a
	// passing project+scope check — the intended intersection model.
	owned := &store.Project{
		ID: tid("n73_proj_owned"), Name: "Owned", Slug: "n73-owned",
		OwnerID: admin.ID(), Created: time.Now(), Updated: time.Now(),
	}
	// Q is owned by someone else (attack: must NOT be visible after the fix).
	other := &store.Project{
		ID: tid("n73_proj_other"), Name: "Other", Slug: "n73-other",
		OwnerID: "someone-else", Created: time.Now(), Updated: time.Now(),
	}
	if err := s.CreateProject(ctx, owned); err != nil {
		t.Fatalf("seed owned project: %v", err)
	}
	if err := s.CreateProject(ctx, other); err != nil {
		t.Fatalf("seed other project: %v", err)
	}

	adminUAT := NewScopedUserIdentity(admin, owned.ID, []string{"hub:manage", "project:read"})
	rec := httptest.NewRecorder()
	srv.listProjects(rec, n73Request(http.MethodGet, "/api/v1/projects", adminUAT))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}

	var resp ListProjectsResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	seen := map[string]bool{}
	for _, p := range resp.Projects {
		seen[p.ID] = true
	}
	if seen[other.ID] {
		t.Errorf("admin-minted UAT saw project %q it does not own — admin bypass not removed at listProjects", other.ID)
	}
	if !seen[owned.ID] {
		t.Errorf("admin-minted UAT did not see its own project %q — over-restriction", owned.ID)
	}
}

func TestListUsers_ScopedAdminUAT_SubsetOnly(t *testing.T) {
	srv, s := testServer(t)
	ctx := context.Background()
	admin := n73AdminUser()

	member := &store.User{
		ID: tid("n73_user_member"), Email: "n73member@example.com", DisplayName: "N73 Member",
		Role: store.UserRoleMember, Status: "active", Created: time.Now(),
	}
	if err := s.CreateUser(ctx, member); err != nil {
		t.Fatalf("seed member: %v", err)
	}

	adminUAT := NewScopedUserIdentity(admin, "n73-project", []string{"hub:manage"})
	rec := httptest.NewRecorder()
	srv.listUsers(rec, n73Request(http.MethodGet, "/api/v1/users", adminUAT))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}

	var resp ListUsersResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	for _, u := range resp.Users {
		if u.ID == member.ID {
			t.Errorf("admin-minted UAT saw other user %q — admin bypass not removed at listUsers", member.ID)
		}
	}
}

func TestListAgents_ScopedAdminUAT_SubsetOnly(t *testing.T) {
	srv, s := testServer(t)
	ctx := context.Background()
	admin := n73AdminUser()

	proj := &store.Project{
		ID: tid("n73_agent_proj"), Name: "Agent Proj", Slug: "n73-agent-proj",
		OwnerID: "someone-else", Created: time.Now(), Updated: time.Now(),
	}
	if err := s.CreateProject(ctx, proj); err != nil {
		t.Fatalf("seed project: %v", err)
	}
	agent := &store.Agent{
		ID: tid("n73_agent"), Slug: "n73-agent", Name: "N73 Agent",
		ProjectID: proj.ID, OwnerID: tid("n73_other_owner"), Phase: "running", StateVersion: 1,
		Created: time.Now(), Updated: time.Now(),
	}
	if err := s.CreateAgent(ctx, agent); err != nil {
		t.Fatalf("seed agent: %v", err)
	}

	adminUAT := NewScopedUserIdentity(admin, "n73-project", []string{"hub:manage"})
	rec := httptest.NewRecorder()
	srv.listAgents(rec, n73Request(http.MethodGet, "/api/v1/agents", adminUAT))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}

	var resp ListAgentsResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	for _, a := range resp.Agents {
		if a.ID == agent.ID {
			t.Errorf("admin-minted UAT saw agent %q in a project it does not own — admin bypass not removed at listAgents", agent.ID)
		}
	}
}

func TestListSkills_ScopedAdminUAT_SubsetOnly_PublicCarveOut(t *testing.T) {
	srv, s := testServer(t)
	ctx := context.Background()
	admin := n73AdminUser()

	// Private skill owned by someone else: must NOT be visible after the fix.
	priv := &store.Skill{
		ID: tid("n73_skill_priv"), Name: "priv", Slug: "n73-priv",
		Scope: store.SkillScopeGlobal, Status: "active", OwnerID: "someone-else",
		Visibility: store.VisibilityPrivate, Created: time.Now(), Updated: time.Now(),
	}
	// Public skill: the visibility carve-out must keep it visible (no
	// over-restriction).
	pub := &store.Skill{
		ID: tid("n73_skill_pub"), Name: "pub", Slug: "n73-pub",
		Scope: store.SkillScopeGlobal, Status: "active", OwnerID: "someone-else",
		Visibility: store.VisibilityPublic, Created: time.Now(), Updated: time.Now(),
	}
	if err := s.CreateSkill(ctx, priv); err != nil {
		t.Fatalf("seed private skill: %v", err)
	}
	if err := s.CreateSkill(ctx, pub); err != nil {
		t.Fatalf("seed public skill: %v", err)
	}

	adminUAT := NewScopedUserIdentity(admin, "n73-project", []string{"hub:manage"})
	rec := httptest.NewRecorder()
	srv.listSkills(rec, n73Request(http.MethodGet, "/api/v1/skills", adminUAT))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}

	var resp ListSkillsResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	seen := map[string]bool{}
	for _, sk := range resp.Skills {
		seen[sk.ID] = true
	}
	if seen[priv.ID] {
		t.Errorf("admin-minted UAT saw private skill %q it cannot read — admin bypass not removed at listSkills", priv.ID)
	}
	if !seen[pub.ID] {
		t.Errorf("public skill %q not returned — the visibility carve-out was over-restricted", pub.ID)
	}
}

// -----------------------------------------------------------------------------
// Pin 6 — CAPABILITY functions, one test per function. Each has a byte-identical
// `Role()=="admin"` short-circuit; the resources below carry no OwnerID, no
// ancestry and no project parent, so the OTHER (ID-keyed) short-circuits
// (resource owner, ancestry, isProjectOwnerOrAdmin) do not fire and the deny is
// attributable solely to the removed admin short-circuit — i.e. the root fix.
// Three separate tests because one cannot establish which of the three
// duplicated short-circuits it exercised.
// -----------------------------------------------------------------------------

func TestComputeCapabilities_ScopedAdminUAT_NotAllActions(t *testing.T) {
	srv, _ := testServer(t)
	adminUAT := NewScopedUserIdentity(n73AdminUser(), "n73-project", []string{"hub:manage"})

	// A user resource: no OwnerID, no project parent.
	res := Resource{Type: "user", ID: "n73-target-user"}
	cap := srv.authzService.ComputeCapabilities(context.Background(), adminUAT, res)
	if capabilityAllows(cap, ActionRead) {
		t.Errorf("ComputeCapabilities granted read to an admin-minted UAT via the admin short-circuit; actions=%v", cap.Actions)
	}
}

func TestComputeScopeCapabilities_ScopedAdminUAT_NotAllActions(t *testing.T) {
	srv, _ := testServer(t)
	adminUAT := NewScopedUserIdentity(n73AdminUser(), "n73-project", []string{"hub:manage"})

	// scopeType "" so the project-owner short-circuit is skipped; resource type
	// "project" — matching how the list handlers call it.
	cap := srv.authzService.ComputeScopeCapabilities(context.Background(), adminUAT, "", "", "project")
	if capabilityAllows(cap, ActionList) {
		t.Errorf("ComputeScopeCapabilities granted list to an admin-minted UAT via the admin short-circuit; actions=%v", cap.Actions)
	}
}

func TestComputeCapabilitiesBatch_ScopedAdminUAT_NotAllActions(t *testing.T) {
	srv, _ := testServer(t)
	adminUAT := NewScopedUserIdentity(n73AdminUser(), "n73-project", []string{"hub:manage"})

	resources := []Resource{{Type: "user", ID: "n73-target-user"}}
	caps := srv.authzService.ComputeCapabilitiesBatch(context.Background(), adminUAT, resources, "user")
	if len(caps) != 1 {
		t.Fatalf("expected 1 capability, got %d", len(caps))
	}
	if capabilityAllows(caps[0], ActionRead) {
		t.Errorf("ComputeCapabilitiesBatch granted read to an admin-minted UAT via the admin short-circuit; actions=%v", caps[0].Actions)
	}
}

// -----------------------------------------------------------------------------
// Pin 7 — OPPOSITE-OUTCOME control (mandatory): a genuine SESSION admin (not
// token-scoped) STILL gets admin everywhere. Without this, every pin above is
// satisfiable by breaking admin entirely. This control stays GREEN with AND
// without the fix — the fix narrows UATs, not admins.
// -----------------------------------------------------------------------------

func TestGenuineSessionAdmin_StillAdmin(t *testing.T) {
	srv, _ := testServer(t)
	ctx := context.Background()
	sessionAdmin := n73AdminUser() // NOT wrapped in a ScopedUserIdentity.

	// requireAdmin still admits it.
	rec := httptest.NewRecorder()
	if _, ok := srv.requireAdmin(rec, n73Request(http.MethodGet, "/api/v1/some-admin-route", sessionAdmin)); !ok {
		t.Fatalf("genuine session admin denied by requireAdmin; status=%d body=%s", rec.Code, rec.Body.String())
	}

	// All three capability functions still return allActions via the admin
	// short-circuit.
	if cap := srv.authzService.ComputeCapabilities(ctx, sessionAdmin, Resource{Type: "user", ID: "n73-target-user"}); !capabilityAllows(cap, ActionRead) {
		t.Errorf("ComputeCapabilities denied read to a genuine session admin; actions=%v", cap.Actions)
	}
	if cap := srv.authzService.ComputeScopeCapabilities(ctx, sessionAdmin, "", "", "project"); !capabilityAllows(cap, ActionList) {
		t.Errorf("ComputeScopeCapabilities denied list to a genuine session admin; actions=%v", cap.Actions)
	}
	caps := srv.authzService.ComputeCapabilitiesBatch(ctx, sessionAdmin, []Resource{{Type: "user", ID: "n73-target-user"}}, "user")
	if len(caps) != 1 || !capabilityAllows(caps[0], ActionRead) {
		t.Errorf("ComputeCapabilitiesBatch denied read to a genuine session admin; caps=%v", caps)
	}
}

// -----------------------------------------------------------------------------
// Pin 8 — ATTRIBUTION: /auth/me under a UAT reports the MINTING user's true role
// (via MintingUserRole) and marks the identity token-scoped with its project and
// scopes. This pins §3 so the fix cannot later be "simplified" by pointing
// handleMe back at Role() (which is now "" for a UAT). The response is decoded
// as a generic map so the pin compiles against the pre-fix tree; the
// distinguishing signal is the presence of tokenScoped/scopedProjectId, which
// the pre-fix handler does not emit.
// -----------------------------------------------------------------------------

func TestAuthMe_ScopedUAT_Attribution(t *testing.T) {
	srv, _ := testServer(t)
	adminUAT := NewScopedUserIdentity(n73AdminUser(), "n73-project", []string{"hub:manage"})

	rec := httptest.NewRecorder()
	srv.handleAuthMe(rec, n73Request(http.MethodGet, "/api/v1/auth/me", adminUAT))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}

	var m map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&m); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if ts, _ := m["tokenScoped"].(bool); !ts {
		t.Errorf("/auth/me did not mark a UAT caller tokenScoped; body=%s", rec.Body.String())
	}
	if pid, _ := m["scopedProjectId"].(string); pid != "n73-project" {
		t.Errorf("scopedProjectId = %q, want %q", pid, "n73-project")
	}
	// Attribution must report the minting user's true role, not the empty
	// authority role and not a bare "".
	if role, _ := m["role"].(string); role != store.UserRoleAdmin {
		t.Errorf("role = %q, want minting role %q (via MintingUserRole)", role, store.UserRoleAdmin)
	}
	// Scopes echoed back.
	scopes, _ := m["scopes"].([]any)
	found := false
	for _, sc := range scopes {
		if s, _ := sc.(string); s == "hub:manage" {
			found = true
		}
	}
	if !found {
		t.Errorf("scopes = %v, want to contain %q", m["scopes"], "hub:manage")
	}
}

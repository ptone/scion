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
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/GoogleCloudPlatform/scion/pkg/api"
	"github.com/GoogleCloudPlatform/scion/pkg/store"
	"github.com/stretchr/testify/require"
)

// Regression tests for who becomes an owner of a project.
//
// Ownership is not an ordinary attribute. isProjectOwnerOrAdmin (authz.go:508)
// is consulted as a short-circuit near the top of CheckAccess, so being recorded
// with role=owner in the project's members group grants every action on every
// resource in the project at once. Every gate elsewhere in this branch — the
// workspace gates, the cache gates, the harness gates — is downstream of it.
// That makes the question "what makes someone an owner?" load-bearing for all of
// them, and it is the question these tests pin.
//
// Two paths answered it wrongly. createProject and registerProject each have a
// branch for a project that already exists — idempotent re-create, and register
// matching an existing project by ID, git remote or slug. Both branches passed
// the calling user into createProjectMembersGroupAndPolicy as an additional
// owner, on the reasoning that the person linking a project deserves membership
// of it. But nothing on either branch establishes that the caller has any
// relationship to the project they named. Naming an existing project's ID is not
// evidence; it is a guess that happened to be right.
//
// Measured at b8396793, before the fix: an authenticated user with no
// relationship to a project was refused its workspace with 403, then POSTed that
// project's own id/slug/gitRemote to /projects/register, received 200, and read
// and wrote the workspace freely. The gates were not bypassed and did not
// misbehave — they were asked about an owner and correctly answered allow. The
// caller had simply been made one. The same worked through POST /projects with
// the victim's UUID as "id".
//
// The fix grants ownership only from project.CreatedBy, which is set when the
// project is first created. The on-existing branches still ensure the groups
// exist — that backfill is why they call the function at all — but no longer add
// the caller.
//
// The four tests below are the two halves of that. Two attacks that must now
// fail, and two positive controls, because "nobody becomes an owner" is trivially
// satisfiable by breaking ownership outright, and that failure mode would leave
// every project in the deployment unadministerable. Test naming: everything
// file-local is prefixed ogGrant.

type ogGrantFixture struct {
	srv   *Server
	store store.Store

	// victim owns the project under attack; intruder is an authenticated user
	// with no relationship to it whatsoever.
	victim   *store.User
	intruder *store.User

	// The directory served as the project workspace, holding the canary.
	workspacePath    string
	embeddedBrokerID string
}

const (
	ogGrantCanaryName    = "canary.txt"
	ogGrantCanaryContent = "owner-grant-canary-must-not-leak"
)

func ogGrantSetup(t *testing.T) *ogGrantFixture {
	t.Helper()

	s, err := newTestStore(":memory:")
	if err != nil {
		t.Skipf("skipping: test store unavailable (%v)", err)
	}
	require.NoError(t, s.Migrate(context.Background()))

	cfg := DefaultServerConfig()
	cfg.DevAuthToken = testDevToken
	srv, err := New(cfg, s)
	require.NoError(t, err)
	srv.SetHubID("test-hub-id")
	t.Cleanup(func() { _ = srv.Shutdown(context.Background()) })

	ctx := context.Background()
	f := &ogGrantFixture{srv: srv, store: s}

	f.victim = &store.User{
		ID: tid("oggrant-victim"), Email: "oggrant-victim@example.com",
		DisplayName: "Victim", Role: store.UserRoleMember, Status: "active",
		Created: time.Now(),
	}
	require.NoError(t, s.CreateUser(ctx, f.victim))

	f.intruder = &store.User{
		ID: tid("oggrant-intruder"), Email: "oggrant-intruder@example.com",
		DisplayName: "Intruder", Role: store.UserRoleMember, Status: "active",
		Created: time.Now(),
	}
	require.NoError(t, s.CreateUser(ctx, f.intruder))

	// The embedded broker's LocalPath is what the workspace handlers serve.
	root := t.TempDir()
	f.workspacePath = filepath.Join(root, "workspace")
	require.NoError(t, os.MkdirAll(f.workspacePath, 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(f.workspacePath, ogGrantCanaryName),
		[]byte(ogGrantCanaryContent), 0o644))

	f.embeddedBrokerID = tid("oggrant-embedded-broker")
	srv.SetEmbeddedBrokerID(f.embeddedBrokerID)
	require.NoError(t, s.CreateRuntimeBroker(ctx, &store.RuntimeBroker{
		ID: f.embeddedBrokerID, Name: "oggrant-embedded", Slug: "oggrant-embedded",
	}))

	return f
}

// ---------------------------------------------------------------------------
// Requests. Projects are created through the HTTP handlers rather than seeded
// into the store, because the grant under test happens in those handlers and a
// seeded project would not exercise it.
// ---------------------------------------------------------------------------

func (f *ogGrantFixture) asUser(t *testing.T, u *store.User, method, path string,
	body any) *httptest.ResponseRecorder {
	t.Helper()
	svc := f.srv.GetUserTokenService()
	require.NotNil(t, svc)
	tok, _, err := svc.GenerateAccessToken(u.ID, u.Email, u.DisplayName,
		string(u.Role), ClientTypeAPI)
	require.NoError(t, err)

	var rdr io.Reader = bytes.NewReader(nil)
	if body != nil {
		raw, mErr := json.Marshal(body)
		require.NoError(t, mErr)
		rdr = bytes.NewReader(raw)
	}
	req := httptest.NewRequest(method, path, rdr)
	req.Header.Set("Authorization", "Bearer "+tok)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	rec := httptest.NewRecorder()
	f.srv.Handler().ServeHTTP(rec, req)
	return rec
}

// createVictimProject creates a project as the victim through POST /projects and
// attaches the canary workspace to it. It returns the stored project.
func (f *ogGrantFixture) createVictimProject(t *testing.T, name string) *store.Project {
	t.Helper()
	rec := f.asUser(t, f.victim, http.MethodPost, "/api/v1/projects",
		CreateProjectRequest{
			ID:        api.NewUUID(),
			Name:      name,
			GitRemote: "https://example.invalid/" + api.Slugify(name) + ".git",
		})
	require.Equal(t, http.StatusCreated, rec.Code, "body=%s", rec.Body.String())

	var created store.Project
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &created))
	require.NotEmpty(t, created.ID)

	require.NoError(t, f.store.AddProjectProvider(context.Background(),
		&store.ProjectProvider{
			ProjectID: created.ID, BrokerID: f.embeddedBrokerID,
			BrokerName: "oggrant-embedded", LocalPath: f.workspacePath,
		}))

	stored, err := f.store.GetProject(context.Background(), created.ID)
	require.NoError(t, err)
	return stored
}

// ---------------------------------------------------------------------------
// Observations.
// ---------------------------------------------------------------------------

// readWorkspace fetches the canary file and returns the status code. This is
// the endpoint the escalation was measured against, and it is used rather than
// a store query because ownership matters only insofar as it changes what the
// server will do.
func (f *ogGrantFixture) readWorkspace(t *testing.T, u *store.User, p *store.Project) *httptest.ResponseRecorder {
	t.Helper()
	return f.asUser(t, u, http.MethodGet,
		"/api/v1/projects/"+p.ID+"/workspace/files/"+ogGrantCanaryName, nil)
}

// isRecordedOwner reports whether the user holds role=owner or role=admin in any
// explicit group of the project — the exact question isProjectOwnerOrAdmin asks.
// Checked alongside the HTTP verdict because the two can drift: a grant that
// happens but is not yet reachable through any route is still a grant, and would
// become reachable the moment a new route was added.
func (f *ogGrantFixture) isRecordedOwner(t *testing.T, u *store.User, p *store.Project) bool {
	t.Helper()
	ctx := context.Background()

	// "No owner row" and "nothing left that could hold an owner row" are
	// different answers, and only the first is evidence. Measured: a deleted
	// project returns false here, which is the answer every refusal case
	// asserts, so a request that destroyed its own subject would be
	// indistinguishable from one that was refused.
	//
	// Why it returns false is a separate question from that observation, and
	// the two guards below do not depend on settling it. aid-verify measured
	// that DeleteProject in this store leaves a project's explicit groups
	// behind rather than cascading them, so "the project is gone" and "the
	// groups are gone" are separately reachable states; there is one guard for
	// each. Both run in either polarity, because a positive control whose
	// group vanished should fail as a broken fixture rather than as "the
	// backfill stopped promoting anyone".
	_, err := f.store.GetProject(ctx, p.ID)
	require.NoError(t, err,
		"the project no longer exists, so this answer would be false by "+
			"absence rather than because no grant was recorded")

	groups, err := f.store.ListGroups(ctx, store.GroupFilter{
		ProjectID: p.ID, GroupType: store.GroupTypeExplicit,
	}, store.ListOptions{Limit: 10})
	require.NoError(t, err)
	require.NotEmpty(t, groups.Items,
		"the project has no explicit group left to record ownership in, so "+
			"this answer would be false by absence")
	for _, g := range groups.Items {
		m, err := f.store.GetGroupMembership(ctx, g.ID, store.GroupMemberTypeUser, u.ID)
		if err != nil {
			continue
		}
		if m.Role == store.GroupMemberRoleOwner || m.Role == store.GroupMemberRoleAdmin {
			return true
		}
	}
	return false
}

// requireIntruderStillPlainMember proves a refusal was a refusal and not a
// subject that went away. The ownerless escalation needs the intruder to be the
// group's sole member: if the request removed that membership, or the group,
// the intruder is not an owner afterwards and isRecordedOwner reports exactly
// what a successful defence reports. Asserting the membership survived with its
// role unchanged keeps "the promotion was refused" separable from "there is no
// longer anyone to promote".
//
// This is only meaningful where the intruder is a member to begin with, so it
// belongs to the ownerless-project cases. In the two createVictimProject
// attacks the intruder is an outsider and the subject's liveness is carried by
// the project check inside isRecordedOwner plus the workspace read, which is
// asserted equal to 403 and would fail on the 404 a deleted project returns.
func (f *ogGrantFixture) requireIntruderStillPlainMember(t *testing.T, p *store.Project) {
	t.Helper()
	ctx := context.Background()
	group, err := f.store.GetGroupBySlug(ctx, "project:"+p.Slug+":members")
	require.NoError(t, err,
		"the project's members group is gone after the refusal, so there is "+
			"nothing left the intruder could have been promoted in")
	m, err := f.store.GetGroupMembership(ctx, group.ID, store.GroupMemberTypeUser, f.intruder.ID)
	require.NoError(t, err,
		"the intruder's membership is gone after the refusal, so \"not an "+
			"owner\" no longer means the promotion was refused")
	require.Equal(t, store.GroupMemberRoleMember, m.Role,
		"the intruder is no longer a plain member, so the state this case was "+
			"measured against is not the state it ended in")
}

// ---------------------------------------------------------------------------
// The two attacks.
// ---------------------------------------------------------------------------

// TestOGGrant_RegisterOnExistingGrantsNothing is the primary regression test. It
// is the measured escalation, executed end to end at the HTTP boundary in the
// order the attacker ran it: confirm the door is shut, push on it, confirm it is
// still shut.
//
// The pre-check is not ceremony. Without it, a test in which the intruder is
// refused at the end passes just as well when the workspace route is broken for
// everyone, and it would then keep passing after the fix was reverted.
func TestOGGrant_RegisterOnExistingGrantsNothing(t *testing.T) {
	f := ogGrantSetup(t)
	victimProject := f.createVictimProject(t, "OG Grant Register")

	before := f.readWorkspace(t, f.intruder, victimProject)
	require.Equal(t, http.StatusForbidden, before.Code,
		"precondition: the intruder must start out refused; body=%s", before.Body.String())

	// The attack: announce the victim's own project as if it were the
	// intruder's. Every field is copied from the victim's project, which is
	// what makes register treat this as a match rather than a new project.
	reg := f.asUser(t, f.intruder, http.MethodPost, "/api/v1/projects/register",
		RegisterProjectRequest{
			ID:        victimProject.ID,
			Name:      victimProject.Name,
			GitRemote: victimProject.GitRemote,
		})

	require.False(t, f.isRecordedOwner(t, f.intruder, victimProject),
		"registering an existing project made the caller an owner of it "+
			"(register returned %d)", reg.Code)

	after := f.readWorkspace(t, f.intruder, victimProject)
	require.Equal(t, http.StatusForbidden, after.Code,
		"the intruder read the victim's workspace after registering their "+
			"project (register returned %d); body=%s", reg.Code, after.Body.String())
	require.NotContains(t, after.Body.String(), ogGrantCanaryContent)
}

// TestOGGrant_CreateOnExistingIDGrantsNothing is the same escalation through the
// other door. createProject's idempotency branch returns the existing project to
// anyone who supplies its UUID, and it granted ownership on the way. It is
// tested separately rather than folded in because it is a separate branch in a
// separate handler that happened to share the mistake, and a fix to one would
// not have fixed the other.
func TestOGGrant_CreateOnExistingIDGrantsNothing(t *testing.T) {
	f := ogGrantSetup(t)
	victimProject := f.createVictimProject(t, "OG Grant Create")

	before := f.readWorkspace(t, f.intruder, victimProject)
	require.Equal(t, http.StatusForbidden, before.Code,
		"precondition: the intruder must start out refused; body=%s", before.Body.String())

	create := f.asUser(t, f.intruder, http.MethodPost, "/api/v1/projects",
		CreateProjectRequest{
			ID:   victimProject.ID,
			Name: "a name of the intruder's choosing",
		})

	require.False(t, f.isRecordedOwner(t, f.intruder, victimProject),
		"re-creating an existing project by ID made the caller an owner of it "+
			"(create returned %d)", create.Code)

	after := f.readWorkspace(t, f.intruder, victimProject)
	require.Equal(t, http.StatusForbidden, after.Code,
		"the intruder read the victim's workspace after re-creating their "+
			"project by ID (create returned %d); body=%s", create.Code, after.Body.String())
	require.NotContains(t, after.Body.String(), ogGrantCanaryContent)
}

// ---------------------------------------------------------------------------
// The two positive controls.
// ---------------------------------------------------------------------------

// TestOGGrant_InitialCreatorStillBecomesOwner pins what the fix must not break.
// Creating a project is the act that establishes the entitlement, and both
// entry points that create one must still confer it — otherwise a fresh project
// belongs to nobody and cannot be administered.
//
// Register is covered as well as create because the fix edited both handlers,
// and register's creating branch sits directly beside the branch that was
// changed.
func TestOGGrant_InitialCreatorStillBecomesOwner(t *testing.T) {
	t.Run("via create", func(t *testing.T) {
		f := ogGrantSetup(t)
		p := f.createVictimProject(t, "OG Grant Positive Create")

		require.True(t, f.isRecordedOwner(t, f.victim, p),
			"the creating user is not an owner of the project they just created")

		rec := f.readWorkspace(t, f.victim, p)
		require.Equal(t, http.StatusOK, rec.Code, "body=%s", rec.Body.String())
		require.Contains(t, rec.Body.String(), ogGrantCanaryContent,
			"the owner's read must actually return the file, or the intruder's "+
				"403 above proves nothing about the gate")
	})

	t.Run("via register", func(t *testing.T) {
		f := ogGrantSetup(t)
		rec := f.asUser(t, f.victim, http.MethodPost, "/api/v1/projects/register",
			RegisterProjectRequest{
				Name:      "OG Grant Positive Register",
				GitRemote: "https://example.invalid/og-grant-positive-register.git",
			})
		require.Equal(t, http.StatusOK, rec.Code, "body=%s", rec.Body.String())

		var resp RegisterProjectResponse
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
		require.NotNil(t, resp.Project)

		p, err := f.store.GetProject(context.Background(), resp.Project.ID)
		require.NoError(t, err)
		require.True(t, f.isRecordedOwner(t, f.victim, p),
			"the user who registered a new project is not an owner of it")
	})
}

// TestOGGrant_OwnerReregisteringKeepsOwnership covers the case the removed grant
// was presumably written for. The legitimate client re-announces its project on
// every startup, so the on-existing branch is the common path, not the rare one,
// and it must not cost the real owner anything.
//
// It does not, because ownership on that branch comes from project.CreatedBy and
// the re-add is idempotent. The test exists because that is a claim about a
// branch a reader can no longer see any grant on, and the absence of a grant
// looks the same whether or not it was safe to remove.
func TestOGGrant_OwnerReregisteringKeepsOwnership(t *testing.T) {
	f := ogGrantSetup(t)
	p := f.createVictimProject(t, "OG Grant Reregister")
	require.True(t, f.isRecordedOwner(t, f.victim, p), "precondition")

	t.Run("re-register", func(t *testing.T) {
		rec := f.asUser(t, f.victim, http.MethodPost, "/api/v1/projects/register",
			RegisterProjectRequest{
				ID: p.ID, Name: p.Name, GitRemote: p.GitRemote,
			})
		require.Equal(t, http.StatusOK, rec.Code, "body=%s", rec.Body.String())
		require.True(t, f.isRecordedOwner(t, f.victim, p),
			"the owner lost ownership by re-registering their own project")
	})

	t.Run("idempotent re-create", func(t *testing.T) {
		rec := f.asUser(t, f.victim, http.MethodPost, "/api/v1/projects",
			CreateProjectRequest{ID: p.ID, Name: p.Name})
		require.Equal(t, http.StatusOK, rec.Code, "body=%s", rec.Body.String())
		require.True(t, f.isRecordedOwner(t, f.victim, p),
			"the owner lost ownership by re-creating their own project")
	})

	t.Run("and can still read the workspace", func(t *testing.T) {
		rec := f.readWorkspace(t, f.victim, p)
		require.Equal(t, http.StatusOK, rec.Code, "body=%s", rec.Body.String())
		require.Contains(t, rec.Body.String(), ogGrantCanaryContent)
	})
}

// ---------------------------------------------------------------------------
// The residual: the same escalation through the other source of ownership.
//
// Removing the caller-supplied grant above left a second way to become an
// owner. createProjectMembersGroupAndPolicy promotes the sole member of a group
// that has no owners — a backfill for projects created before ownership was
// enforced. That promotion did not ask who was asking, so a plain member of an
// ownerless project could trigger it against themselves by POSTing the
// project's own ID to /projects/register: they were the only member, so they
// became the owner, and the owner-or-admin short-circuit did the rest.
//
// The two tests below are the two states that leave a group ownerless with a
// live member in it. Neither is hypothetical: the first is old data, and the
// second is the trusted-proxy deployment the FK-retry path in that function is
// written to support. They are separate tests rather than one table because
// they arrive at the same group through different failures, and a fix that
// covered one and not the other would be worth seeing as one red test.
// ---------------------------------------------------------------------------

// ownerlessProjectWithSoleIntruderMember builds the precondition and asserts it.
// The project is seeded through the store and the group through the real
// function under a context carrying no identity, because this is the project's
// past, not a request: the escalation is the single request made afterwards.
//
// createdBy models the two ownerless states — empty for a legacy project, or a
// user ID that is not in the store for the trusted-proxy case.
func (f *ogGrantFixture) ownerlessProjectWithSoleIntruderMember(
	t *testing.T, name, createdBy string) *store.Project {
	t.Helper()
	ctx := context.Background()

	p := &store.Project{
		ID:        api.NewUUID(),
		Name:      name,
		Slug:      api.Slugify(name),
		GitRemote: "https://example.invalid/" + api.Slugify(name) + ".git",
		CreatedBy: createdBy,
	}
	require.NoError(t, f.store.CreateProject(ctx, p))
	f.srv.createProjectMembersGroupAndPolicy(ctx, p)

	group, err := f.store.GetGroupBySlug(ctx, "project:"+p.Slug+":members")
	require.NoError(t, err)
	require.NoError(t, f.store.AddGroupMember(ctx, &store.GroupMember{
		GroupID:    group.ID,
		MemberType: store.GroupMemberTypeUser,
		MemberID:   f.intruder.ID,
		Role:       store.GroupMemberRoleMember,
	}))

	require.NoError(t, f.store.AddProjectProvider(ctx, &store.ProjectProvider{
		ProjectID: p.ID, BrokerID: f.embeddedBrokerID,
		BrokerName: "oggrant-embedded", LocalPath: f.workspacePath,
	}))

	// Both halves of the precondition are asserted, because the backfill fires
	// only on their conjunction. If a future change to the seeding left an owner
	// behind or a second member in the group, the promotion would never have
	// been attempted and every assertion below would pass without measuring
	// anything.
	owners, err := f.store.CountGroupMembersByRole(ctx, group.ID, store.GroupMemberRoleOwner)
	require.NoError(t, err)
	require.Zero(t, owners,
		"precondition: the project must have no owner, or the backfill never runs")
	members, err := f.store.GetGroupMembers(ctx, group.ID)
	require.NoError(t, err)
	require.Len(t, members, 1,
		"precondition: the intruder must be the group's SOLE member, or the "+
			"backfill never runs")
	require.False(t, f.isRecordedOwner(t, f.intruder, p),
		"precondition: the intruder starts as a plain member")

	stored, err := f.store.GetProject(ctx, p.ID)
	require.NoError(t, err)
	return stored
}

// ogGrantAssertNoSelfPromotion runs the escalation and checks the three things
// that must all hold. The store read comes first deliberately: the role is what
// was actually granted, and the workspace 403 is only its consequence. A change
// that made the workspace route refuse owners would satisfy the HTTP assertion
// while the grant went through.
func ogGrantAssertNoSelfPromotion(t *testing.T, f *ogGrantFixture, p *store.Project) {
	t.Helper()

	before := f.readWorkspace(t, f.intruder, p)
	require.Equal(t, http.StatusForbidden, before.Code,
		"precondition: a plain member must start out refused the workspace; body=%s",
		before.Body.String())

	reg := f.asUser(t, f.intruder, http.MethodPost, "/api/v1/projects/register",
		RegisterProjectRequest{ID: p.ID, Name: p.Name, GitRemote: p.GitRemote})

	f.requireIntruderStillPlainMember(t, p)
	require.False(t, f.isRecordedOwner(t, f.intruder, p),
		"a plain member of an ownerless project promoted themselves to owner by "+
			"registering it (register returned %d)", reg.Code)

	after := f.readWorkspace(t, f.intruder, p)
	require.Equal(t, http.StatusForbidden, after.Code,
		"the member read the workspace after registering the project (register "+
			"returned %d); body=%s", reg.Code, after.Body.String())
	require.NotContains(t, after.Body.String(), ogGrantCanaryContent)
}

// TestOGGrant_SoleMemberCannotSelfPromoteOnLegacyProject is the first ownerless
// state: a project recorded with no creator at all, which is what projects
// created before ownership enforcement look like.
func TestOGGrant_SoleMemberCannotSelfPromoteOnLegacyProject(t *testing.T) {
	f := ogGrantSetup(t)
	p := f.ownerlessProjectWithSoleIntruderMember(t, "OG Grant Legacy Ownerless", "")
	ogGrantAssertNoSelfPromotion(t, f, p)
}

// TestOGGrant_SoleMemberCannotSelfPromoteOnAbsentCreator is the second: the
// creator is recorded, but no user row exists for them. That is not corrupt
// data — it is the trusted-proxy deployment where authentication comes from
// proxy headers and no DB user is provisioned, which the FK retry in
// createProjectMembersGroupAndPolicy exists to keep working. The recorded
// creator cannot be added as an owner, so the group is left ownerless with
// whoever else is in it.
func TestOGGrant_SoleMemberCannotSelfPromoteOnAbsentCreator(t *testing.T) {
	f := ogGrantSetup(t)
	p := f.ownerlessProjectWithSoleIntruderMember(t,
		"OG Grant Absent Creator", tid("oggrant-creator-not-in-store"))
	ogGrantAssertNoSelfPromotion(t, f, p)
}

// TestOGGrant_BackfillStillPromotesSomeoneElse is the control that keeps the
// constraint honest about its own shape. The refusal is on the caller, not on
// the promotion: the same group, the same sole member, reached by a request
// from somebody else still backfills as it always did.
//
// This is recorded as behaviour rather than endorsed as a design. It means an
// ownerless project's sole member can still acquire ownership when an admin
// happens to touch the project, which is ownership assignment by accident.
// Deliberate assignment wants its own administrative flow; there isn't one yet,
// and this test is where its absence is visible.
func TestOGGrant_BackfillStillPromotesSomeoneElse(t *testing.T) {
	f := ogGrantSetup(t)
	p := f.ownerlessProjectWithSoleIntruderMember(t, "OG Grant Backfill By Other", "")

	admin := &store.User{
		ID: tid("oggrant-admin"), Email: "oggrant-admin@example.com",
		DisplayName: "Admin", Role: store.UserRoleAdmin, Status: "active",
		Created: time.Now(),
	}
	require.NoError(t, f.store.CreateUser(context.Background(), admin))

	rec := f.asUser(t, admin, http.MethodPost, "/api/v1/projects/register",
		RegisterProjectRequest{ID: p.ID, Name: p.Name, GitRemote: p.GitRemote})
	require.Equal(t, http.StatusOK, rec.Code, "body=%s", rec.Body.String())

	require.True(t, f.isRecordedOwner(t, f.intruder, p),
		"the sole-member backfill no longer fires for anyone, which is a wider "+
			"change than refusing to promote the caller")
}

// TestOGGrant_SelfPromotionRefusedByEveryRoute pins the placement of the
// constraint. It is inside createProjectMembersGroupAndPolicy rather than in
// registerProject, because register is not the only handler that reaches that
// backfill with an existing project and an arbitrary caller: createProject's
// idempotency branch and getProject call the same function.
//
// The two routes below are not equally load-bearing, and the difference was
// measured rather than assumed:
//
//   - POST /projects with an existing ID reaches the backfill and is refused
//     there. Reverting the constraint reds this arm, so a fix written into
//     registerProject alone would have left this door open.
//   - GET /projects/{id} does NOT reach the backfill today. getProject is gated
//     ahead of its group backfill by authorizeProjectReadNoOracle, and a plain
//     member has no read on the project, so the 404 arrives first. Reverting
//     the constraint leaves this arm green: it measures the read gate, not this
//     one. It is kept because the backfill sits a few lines below that gate in
//     the same function — public-project read is a documented follow-on, and
//     relaxing the gate would put this route straight back onto the promotion
//     with nothing between them.
func TestOGGrant_SelfPromotionRefusedByEveryRoute(t *testing.T) {
	routes := []struct {
		name string
		// want records what the route answers a plain member of an ownerless
		// project, so that a change in how far the request gets is visible here
		// rather than silent.
		want int
		call func(*ogGrantFixture, *store.Project) *httptest.ResponseRecorder
	}{
		{"POST /projects with an existing ID", http.StatusOK,
			func(f *ogGrantFixture, p *store.Project) *httptest.ResponseRecorder {
				return f.asUser(t, f.intruder, http.MethodPost, "/api/v1/projects",
					CreateProjectRequest{ID: p.ID, Name: p.Name})
			}},
		{"GET /projects/{id}", http.StatusNotFound,
			func(f *ogGrantFixture, p *store.Project) *httptest.ResponseRecorder {
				return f.asUser(t, f.intruder, http.MethodGet, "/api/v1/projects/"+p.ID, nil)
			}},
	}

	for _, rt := range routes {
		t.Run(rt.name, func(t *testing.T) {
			f := ogGrantSetup(t)
			p := f.ownerlessProjectWithSoleIntruderMember(t, "OG Grant Route "+api.Slugify(rt.name), "")

			rec := rt.call(f, p)
			require.Equal(t, rt.want, rec.Code,
				"%s no longer answers a plain member the way this test was "+
					"measured against; body=%s", rt.name, rec.Body.String())
			f.requireIntruderStillPlainMember(t, p)
			require.False(t, f.isRecordedOwner(t, f.intruder, p),
				"the sole member was promoted to owner by their own %s", rt.name)

			after := f.readWorkspace(t, f.intruder, p)
			require.Equal(t, http.StatusForbidden, after.Code, "body=%s", after.Body.String())
		})
	}
}

// TestOGGrant_SelfPromotionUnauthenticated records that the route is closed to
// anonymous callers upstream of any of this. Not evidence about the constraint —
// an unauthenticated caller has no identity to promote — but the arm has to be
// stated to be known.
func TestOGGrant_SelfPromotionUnauthenticated(t *testing.T) {
	f := ogGrantSetup(t)
	p := f.ownerlessProjectWithSoleIntruderMember(t, "OG Grant Anonymous", "")

	body, err := json.Marshal(RegisterProjectRequest{ID: p.ID, Name: p.Name, GitRemote: p.GitRemote})
	require.NoError(t, err)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/projects/register", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	f.srv.Handler().ServeHTTP(rec, req)

	require.Equal(t, http.StatusUnauthorized, rec.Code, "body=%s", rec.Body.String())
	f.requireIntruderStillPlainMember(t, p)
	require.False(t, f.isRecordedOwner(t, f.intruder, p))
}

// ---------------------------------------------------------------------------
// B10: the same backfill, reached by a NON-USER principal.
//
// The self-grant refusal above was keyed on GetUserIdentityFromContext, which
// returns nil for an agent or broker. So the promotion's else-if stayed
// reachable by any non-user principal: an agent-authenticated request fell
// through and promoted the sole USER member — which for an agent is the user
// who owns it. A user refused the self-grant therefore obtained it by making
// the same request through an agent they own. Measured live by aid-rev1 at
// fa93e4b6 (agent-driven GET /projects/{id} flipped the sole member
// member->owner while the user arm stayed member).
//
// The fix skips the backfill for ANY non-user principal and keys the user
// refusal on the wide identity (GetIdentityFromContext). The two tests below are
// the direct-call table over every principal kind — including a broker, which
// has no HTTP route reaching this function today but must be pinned so the
// else-if can never promote one — and the end-to-end reproduction of rev1's
// measured agent vector at the HTTP boundary.
// ---------------------------------------------------------------------------

// TestOGGrant_NonUserPrincipalCannotTriggerBackfill calls the real backfill
// directly with a context carrying one principal kind per arm. f.intruder is the
// group's sole member throughout; the arm asserts whether that member ended up
// promoted. The direct call is used rather than a route because the broker arm
// has no route reaching here, and the property under test is the function's
// behaviour for every principal, routable or not.
func TestOGGrant_NonUserPrincipalCannotTriggerBackfill(t *testing.T) {
	cases := []struct {
		name string
		// caller builds the identity-bearing context for the arm. p is the seeded
		// ownerless project whose members group has f.intruder as its sole member.
		caller       func(f *ogGrantFixture, p *store.Project) context.Context
		wantPromoted bool
	}{
		{
			// The measured attack: the victim's agent. GetUserIdentityFromContext is
			// nil for it, which is exactly what let it through before the fix. Its
			// ancestry root is the sole member, modelling "an agent they own".
			"agent principal is skipped",
			func(f *ogGrantFixture, p *store.Project) context.Context {
				claims := &AgentTokenClaims{ProjectID: p.ID, Ancestry: []string{f.intruder.ID}}
				claims.Subject = tid("oggrant-attacker-agent")
				return context.WithValue(context.Background(), agentContextKey{}, claims)
			},
			false,
		},
		{
			// The same nil path, no route today — pinned so the else-if can never
			// reach a broker either. Reverting the fix reds this arm too.
			"broker principal is skipped",
			func(f *ogGrantFixture, p *store.Project) context.Context {
				return contextWithIdentity(context.Background(),
					NewBrokerIdentity(tid("oggrant-attacker-broker")))
			},
			false,
		},
		{
			// CONTROL: the skip is scoped to non-user principals, not a blanket
			// disable. A user caller who is NOT the sole member still backfills — the
			// admin-touches-an-ownerless-project case — promoting with AND without
			// the fix. If this arm reds, the fix over-reached into the legitimate
			// backfill.
			"unrelated user principal still backfills",
			func(f *ogGrantFixture, p *store.Project) context.Context {
				other := NewAuthenticatedUser(tid("oggrant-other-user"),
					"oggrant-other@example.com", "Other",
					string(store.UserRoleMember), string(ClientTypeAPI))
				return contextWithIdentity(context.Background(), other)
			},
			true,
		},
		{
			// CONTROL: the 06e21ac7 self-grant refusal, now keyed on the wide
			// identity — a user caller who IS the sole member is still refused, with
			// AND without the fix.
			"sole-member user principal is refused",
			func(f *ogGrantFixture, p *store.Project) context.Context {
				self := NewAuthenticatedUser(f.intruder.ID, f.intruder.Email,
					f.intruder.DisplayName, string(f.intruder.Role), string(ClientTypeAPI))
				return contextWithIdentity(context.Background(), self)
			},
			false,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := ogGrantSetup(t)
			p := f.ownerlessProjectWithSoleIntruderMember(t,
				"OG Grant Principal "+api.Slugify(c.name), "")

			f.srv.createProjectMembersGroupAndPolicy(c.caller(f, p), p)

			require.Equal(t, c.wantPromoted, f.isRecordedOwner(t, f.intruder, p),
				"sole-member promotion outcome wrong for caller %q", c.name)
		})
	}
}

// TestOGGrant_AgentReadDoesNotPromoteSoleMember reproduces rev1's measured live
// vector end to end at the HTTP boundary: the sole member's own agent reads the
// project it belongs to. getProject gates on ActionRead, which an agent passes
// for its own project (the project-read baseline, authz.go), and then reaches
// the backfill — the door a plain user GET does not get through (that read is
// refused before the backfill; see TestOGGrant_SelfPromotionRefusedByEveryRoute).
//
// The 200 is asserted, not incidental: it proves the request reached the
// backfill, so the not-promoted check below is not vacuously true because the
// read was refused short of it. Reverting the fix reds this test — the read
// returns 200 and the sole member becomes owner.
func TestOGGrant_AgentReadDoesNotPromoteSoleMember(t *testing.T) {
	f := ogGrantSetup(t)
	p := f.ownerlessProjectWithSoleIntruderMember(t, "OG Grant Agent Read", "")

	svc := f.srv.GetAgentTokenService()
	require.NotNil(t, svc)
	// An agent belonging to the project, whose ancestry root is the sole member:
	// the user who owns the agent and would be the beneficiary of the escalation.
	tok, err := svc.GenerateAgentToken(tid("oggrant-agent"), p.ID,
		[]AgentTokenScope{ScopeAgentStatusUpdate}, []string{f.intruder.ID})
	require.NoError(t, err)

	rec := doRequestWithAgentToken(t, f.srv, http.MethodGet, "/api/v1/projects/"+p.ID, nil, tok)
	require.Equal(t, http.StatusOK, rec.Code,
		"the agent must reach the backfill (read its own project) or this test "+
			"proves nothing; body=%s", rec.Body.String())

	require.False(t, f.isRecordedOwner(t, f.intruder, p),
		"the sole member was promoted to owner by their own agent's read of the "+
			"project — the measured B10 escalation")
}

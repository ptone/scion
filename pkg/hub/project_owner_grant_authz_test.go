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
	groups, err := f.store.ListGroups(ctx, store.GroupFilter{
		ProjectID: p.ID, GroupType: store.GroupTypeExplicit,
	}, store.ListOptions{Limit: 10})
	require.NoError(t, err)
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

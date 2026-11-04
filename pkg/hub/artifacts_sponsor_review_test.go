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
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"go/ast"
	"go/token"
	"net/http"
	"testing"
	"time"

	"github.com/GoogleCloudPlatform/scion/pkg/artifacts"
	"github.com/GoogleCloudPlatform/scion/pkg/experiments"
	"github.com/GoogleCloudPlatform/scion/pkg/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Automatic review access for the delegating user (ptone/scion#7134):
// artifactHost.SponsorReviewGrant and sponsorReadsHomeThroughRole.

// enableSponsorReviewForTest turns hub.artifact_sponsor_review_grant on (on
// top of whatever registry the server has, hub.artifacts included when
// enableArtifactsForTest ran first).
func enableSponsorReviewForTest(t *testing.T, srv *Server) {
	t.Helper()
	var active []experiments.Experiment
	for _, e := range experiments.Default().All() {
		if e.Name == experiments.Artifacts || e.Name == experiments.ArtifactSponsorReviewGrant {
			e.Default = true
		}
		active = append(active, e)
	}
	reg, err := experiments.NewRegistry(active, nil)
	require.NoError(t, err)
	srv.experiments = reg
	require.True(t, srv.experimentEnabled(experiments.ArtifactSponsorReviewGrant))
}

// requireRoleError asserts that err is the role refusal for scope, naming
// the scope and artifact.read only.
func requireRoleError(t *testing.T, err error, scope string, msgAndArgs ...any) {
	t.Helper()
	var roleErr *artifacts.SponsorReviewRoleError
	require.True(t, errors.As(err, &roleErr), append([]any{"want a role error, got %v"}, err)...)
	assert.Equal(t, scope, roleErr.Scope, msgAndArgs...)
	assert.Equal(t, artifacts.PermissionRead, roleErr.Permission, msgAndArgs...)
}

// TestArtifactHostSponsorReviewGrant: the policy answers off while the
// experiment is off; with it on, a grant only for an active user who reads
// the asked scope through a role. The caller's own rights never carry over.
func TestArtifactHostSponsorReviewGrant(t *testing.T) {
	f := newReviewGrantFixture(t)
	host := newArtifactHost(f.srv)
	ctx := contextWithIdentity(context.Background(), f.user(f.admin))

	require.ErrorIs(t, host.SponsorReviewGrant(ctx, f.home, f.delegator), artifacts.ErrSponsorReviewOff)

	enableSponsorReviewForTest(t, f.srv)
	inactive := tid("sr-inactive")
	createDCUser(t, f.s, inactive, "sr-inactive@test.com", f.home, store.ProjectRoleMember)
	u, err := f.s.GetUser(context.Background(), inactive)
	require.NoError(t, err)
	u.Status = store.UserStatusSuspended
	require.NoError(t, f.s.UpdateUser(context.Background(), u))

	assert.NoError(t, host.SponsorReviewGrant(ctx, f.home, f.delegator), "member of home")
	assert.NoError(t, host.SponsorReviewGrant(ctx, f.home, f.owner), "owner of home")
	for name, tc := range map[string]struct{ scope, user string }{
		"member of another project only": {f.home, f.outside},
		"inactive member":                {f.home, inactive},
		"unknown user":                   {f.home, tid("sr-missing-user")},
		"deleted home project":           {tid("sr-missing-project"), f.delegator},
	} {
		requireRoleError(t, host.SponsorReviewGrant(ctx, tc.scope, tc.user), tc.scope, name)
	}
	assert.Error(t, host.SponsorReviewGrant(ctx, f.home, ""), "empty user")
}

// TestArtifactHostSponsorReviewHomeScopeOnly: the role check is made
// against the artifact's home, not the agent's project. The delegating
// user is admitted to the agent's project (DelegatingUser resolves them)
// and reads there, but holds no role in another project: an artifact homed
// there gets no grant, and the refusal names that scope and artifact.read
// only.
func TestArtifactHostSponsorReviewHomeScopeOnly(t *testing.T) {
	f := newReviewGrantFixture(t)
	enableSponsorReviewForTest(t, f.srv)
	host := newArtifactHost(f.srv)
	ctx := context.Background()

	sponsor, err := host.DelegatingUser(ctx, f.agent.ID)
	require.NoError(t, err)
	require.Equal(t, f.delegator, sponsor, "the delegating user is admitted to the agent's project")
	require.NoError(t, host.SponsorReviewGrant(ctx, f.home, sponsor), "role in the agent's project, artifact homed there")

	err = host.SponsorReviewGrant(ctx, f.other, sponsor)
	requireRoleError(t, err, f.other)
	assert.Equal(t, "artifacts: no active role with artifact.read in project "+f.other, err.Error())
	assert.NotContains(t, err.Error(), sponsor)
	assert.NotContains(t, err.Error(), f.home)
}

// TestArtifactHostSponsorReadsHomeChecks: each condition the local check
// keeps answers no: a test identity that can no longer sign in, the local
// development user while local-development authority is off, and every
// lookup failure (an error, never a grant).
func TestArtifactHostSponsorReadsHomeChecks(t *testing.T) {
	f := newReviewGrantFixture(t)
	host := newArtifactHost(f.srv)
	ctx := context.Background()

	// A test identity past its window, holding a member role in home.
	expired := gateAFixtureUser(t, f.s, "sr-expired", time.Now().Add(-time.Hour))
	createDCUser(t, f.s, expired.ID, expired.Email, f.home, store.ProjectRoleMember)
	ok, err := host.sponsorReadsHomeThroughRole(ctx, expired.ID, f.home)
	require.NoError(t, err)
	assert.False(t, ok, "a test identity past its window")

	if _, err := f.s.GetUser(ctx, DevUserID); errors.Is(err, store.ErrNotFound) {
		createDCUser(t, f.s, DevUserID, "dev-sr@test.com", f.home, store.ProjectRoleMember)
	}
	f.srv.authzService.setDevLocalAuthorityEnabled(false)
	ok, err = host.sponsorReadsHomeThroughRole(ctx, DevUserID, f.home)
	require.NoError(t, err)
	assert.False(t, ok, "the dev user with local-development authority off")

	for name, fault := range map[string]*sponsorFaultStore{
		"user lookup":    {Store: f.s, failUser: f.delegator},
		"project lookup": {Store: f.s, failProject: f.home},
		"role bindings":  {Store: f.s, failBindings: true},
	} {
		faulty := hostOverStore(fault, false)
		ok, err := faulty.sponsorReadsHomeThroughRole(ctx, f.delegator, f.home)
		assert.False(t, ok, name)
		if name != "role bindings" {
			assert.ErrorIs(t, err, errSponsorStoreFault, name)
		}
	}
	ok, err = hostOverStore(f.s, false).sponsorReadsHomeThroughRole(ctx, f.delegator, f.home)
	require.NoError(t, err)
	assert.True(t, ok, "control: the same host without faults")
}

// TestSponsorReviewIdentityOnlyASubject pins the check-only identity: in
// sponsorReadsHomeThroughRole nothing puts an identity into a context, the
// identity built from the user record is passed only as CheckAccess's
// subject, and CheckAccess runs under maskAllAuthzMemo.
func TestSponsorReviewIdentityOnlyASubject(t *testing.T) {
	forbidden := map[string]bool{
		"WithValue": true, "contextWithIdentity": true, "contextWithCredentialContext": true,
		"WithIdentity": true, "withIdentity": true, "ContextWithIdentity": true,
	}
	found := false
	forEachHubFile(t, func(name string, _ *token.FileSet, f *ast.File) {
		if name != "artifacts_host.go" {
			return
		}
		ast.Inspect(f, func(n ast.Node) bool {
			fd, ok := n.(*ast.FuncDecl)
			if !ok || fd.Name.Name != "sponsorReadsHomeThroughRole" || fd.Body == nil {
				return true
			}
			found = true
			uses, checks := 0, 0
			ast.Inspect(fd.Body, func(m ast.Node) bool {
				call, ok := m.(*ast.CallExpr)
				if !ok {
					return true
				}
				sym := extractCallSymbol(call)
				if forbidden[sym] {
					t.Errorf("sponsorReadsHomeThroughRole calls %s: the identity must never enter a context", sym)
				}
				if sym == "CheckAccess" {
					checks++
					if len(call.Args) < 2 {
						t.Errorf("CheckAccess with %d arguments", len(call.Args))
						return true
					}
					mask, ok := call.Args[0].(*ast.CallExpr)
					if !ok || extractCallSymbol(mask) != "maskAllAuthzMemo" {
						t.Error("CheckAccess's context must be maskAllAuthzMemo(ctx)")
					}
				}
				for i, arg := range call.Args {
					if id, ok := arg.(*ast.Ident); ok && id.Name == "identity" {
						uses++
						if sym != "CheckAccess" || i != 1 {
							t.Errorf("identity passed to %s (arg %d); only CheckAccess's subject argument is allowed", sym, i)
						}
					}
				}
				return true
			})
			if uses != 1 || checks != 1 {
				t.Errorf("identity used %d times and CheckAccess called %d times, want 1 and 1", uses, checks)
			}
			return false
		})
	})
	require.True(t, found, "sponsorReadsHomeThroughRole not found in artifacts_host.go: scanner broken")
}

// sponsorReviewManifest is a one-file manifest body for the versions route.
func sponsorReviewManifest(t *testing.T, kind string, body []byte) []byte {
	t.Helper()
	sum := sha256.Sum256(body)
	b, err := json.Marshal(artifacts.CreateVersionRequest{Kind: kind, Entry: "report.md",
		Files: []artifacts.ManifestFile{{Path: "report.md", Size: int64(len(body)), SHA256: hex.EncodeToString(sum[:])}}})
	require.NoError(t, err)
	return b
}

// TestArtifactSponsorReviewGrantOnHub: with the experiment on, an agent's
// publish gives its delegating user a review grant with its event, so the
// user starts a review version with no manual grant and is refused an
// ordinary one (review_only); a steward's removal is final. With the
// experiment off, the publish adds nothing.
func TestArtifactSponsorReviewGrantOnHub(t *testing.T) {
	f := newReviewGrantFixture(t)
	st, _ := enableArtifactsForTest(t, f.srv)
	ctx := context.Background()
	agent := artifactTestAgent(f.agent.ID, f.home, ScopeProjectArtifactRead, ScopeProjectArtifactWrite)
	publish := func(name string) string {
		t.Helper()
		rec := identityArtifactRequest(t, f.srv, agent, http.MethodPost, "/api/v1/artifacts?name="+name+"&scope="+f.home, []byte("# report\n"))
		require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
		var pub artifacts.ArtifactResponse
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &pub))
		return pub.Artifact.ID
	}
	sponsorRef := artifacts.PrincipalRef(artifacts.PrincipalKindUser, f.delegator)
	sponsorGrant := func(id string) *artifacts.Grant {
		t.Helper()
		gs, err := st.ListGrants(ctx, id)
		require.NoError(t, err)
		for i := range gs {
			if gs[i].SubjectKind == artifacts.SubjectPrincipal && gs[i].SubjectRef == sponsorRef {
				return &gs[i]
			}
		}
		return nil
	}

	off := publish("off.md")
	assert.Nil(t, sponsorGrant(off), "experiment off: no grant")

	enableSponsorReviewForTest(t, f.srv)
	id := publish("on.md")
	g := sponsorGrant(id)
	require.NotNil(t, g, "experiment on: the automatic grant")
	assert.Equal(t, artifacts.GrantReview, g.Permission)
	assert.Equal(t, artifacts.SponsorReviewCreator, g.CreatedByRef)
	evs, err := st.ListEvents(ctx, id)
	require.NoError(t, err)
	var created int
	for _, e := range evs {
		if e.Action == artifacts.EventGrantCreated {
			created++
			assert.Equal(t, artifacts.ActorKindSystem, e.ActorKind)
			assert.Equal(t, artifacts.AuthoritySponsorReview, e.Authority)
		}
	}
	assert.Equal(t, 1, created)

	delegator := f.user(f.delegator)
	path := "/api/v1/artifacts/" + id + "/versions"
	rec := identityArtifactRequest(t, f.srv, delegator, http.MethodPost, path, sponsorReviewManifest(t, artifacts.VersionKindReview, []byte("# report {>>ok?<<}\n")))
	assert.Equal(t, http.StatusCreated, rec.Code, "review by the delegating user: %s", rec.Body.String())
	rec = identityArtifactRequest(t, f.srv, delegator, http.MethodPost, path, sponsorReviewManifest(t, "", []byte("# rewritten\n")))
	assert.Equal(t, http.StatusForbidden, rec.Code, "ordinary version: %s", rec.Body.String())
	assert.Contains(t, rec.Body.String(), artifacts.CodeReviewOnly)

	owner := f.user(f.owner)
	rec = identityArtifactRequest(t, f.srv, owner, http.MethodDelete, "/api/v1/artifacts/"+id+"/grants/"+g.ID, nil)
	require.Equal(t, http.StatusNoContent, rec.Code, "steward removes the grant: %s", rec.Body.String())
	rec = identityArtifactRequest(t, f.srv, agent, http.MethodPost, "/api/v1/artifacts/"+id+"/versions", sponsorReviewManifest(t, "", []byte("# v3\n")))
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	assert.Nil(t, sponsorGrant(id), "a removed grant is never re-created")
}

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
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/GoogleCloudPlatform/scion/pkg/store"
	"github.com/stretchr/testify/require"
)

// Regression tests for the READ gate on the idempotency branch of createProject
// — the branch reached when POST /projects carries an "id" that already belongs
// to a project.
//
// It is the same branch as the matched branch of register, on a different route:
// authenticated caller names an existing project, gets 200 with that project's
// stored record, and the group/policy backfill runs on it. A gate on one route
// and not the other is not a gate, so this file drives the identical caller
// matrix through POST /projects and asserts the identical outcomes. It reuses the
// register gate's fixture deliberately: two fixtures that drifted apart would let
// the two routes agree on paper and diverge in fact.
//
// The 404s are the missing-project answer, code and message included, for the
// reason recorded in project_register_read_gate_test.go: a refusal that is
// distinguishable from absence is still an answer about the project.
//
// WHAT THIS GATE DOES NOT CLAIM. A free id still falls through to the create
// below and answers 201, so a caller can learn that an id is TAKEN by getting 404
// where they would otherwise have received a new project. That is inherent to
// idempotent create by client-supplied id. TestCIGate_FreeIDStillCreates pins it
// as known behaviour rather than leaving it to be discovered as a surprise: what
// the gate removes is the existing project's record and the write on it.
//
// Test naming: everything file-local is prefixed ciGate.

const ciGateCreatePath = "/api/v1/projects"

// TestCIGate_IdempotentCreateCallerMatrix mirrors
// TestRRGate_MatchedRegisterCallerMatrix, caller class for caller class.
func TestCIGate_IdempotentCreateCallerMatrix(t *testing.T) {
	for _, c := range rrGateCallers() {
		t.Run(c.name, func(t *testing.T) {
			f := rrGateSetup(t)
			body := CreateProjectRequest{ID: f.proj.ID, Name: f.proj.Name}

			var rec *httptest.ResponseRecorder
			switch {
			case c.name == "in-project agent":
				rec = f.asAgent(t, f.inAgent, http.MethodPost, ciGateCreatePath, body)
			case c.name == "cross-project agent":
				rec = f.asAgent(t, f.xAgent, http.MethodPost, ciGateCreatePath, body)
			case c.name == "broker":
				rec = f.asBroker(t, http.MethodPost, ciGateCreatePath, body)
			default:
				u := map[string]*store.User{
					"owner": f.owner, "hub admin": f.admin,
					"plain member": f.member, "outsider user": f.outsider,
				}[c.name]
				require.NotNil(t, u, "unmapped caller class %q", c.name)
				rec = f.asUser(t, u, http.MethodPost, ciGateCreatePath, body)
			}

			if c.want == http.StatusNotFound {
				rrGateRequireMissingShaped(t, rec, c.name)
				require.NotContains(t, rec.Body.String(), f.proj.Slug,
					"%s: the refusal handed back the project's stored slug, which the "+
						"request never supplied", c.name)
			} else {
				// 200, not 201: the idempotency branch returns the project that
				// was already there. A 201 here would mean the caller reached the
				// create path instead, and the case would be measuring nothing.
				require.Equal(t, http.StatusOK, rec.Code, "%s: body=%s", c.name, rec.Body.String())
			}

			f.rrGateRequireSubjectIntact(t, f.proj, c.name)
			require.Equal(t, []string{f.owner.ID}, f.rrGateOwners(t, f.proj),
				"%s: the owner set of the project changed", c.name)
		})
	}
}

func TestCIGate_UnauthenticatedDenied(t *testing.T) {
	f := rrGateSetup(t)
	rec := f.anonymous(http.MethodPost, ciGateCreatePath,
		CreateProjectRequest{ID: f.proj.ID, Name: f.proj.Name})
	require.Equal(t, http.StatusUnauthorized, rec.Code, "body=%s", rec.Body.String())
	f.rrGateRequireSubjectIntact(t, f.proj, "anonymous")
}

// TestCIGate_RefusedCreateDoesNotBackfillNamedProject is the write half. legacy
// has no groups, so whether the backfill ran is directly observable rather than
// inferred from a status code.
func TestCIGate_RefusedCreateDoesNotBackfillNamedProject(t *testing.T) {
	cases := []struct {
		name string
		do   func(*testing.T, *rrGateFixture, any) *httptest.ResponseRecorder
	}{
		{"outsider user", func(t *testing.T, f *rrGateFixture, b any) *httptest.ResponseRecorder {
			return f.asUser(t, f.outsider, http.MethodPost, ciGateCreatePath, b)
		}},
		{"cross-project agent", func(t *testing.T, f *rrGateFixture, b any) *httptest.ResponseRecorder {
			return f.asAgent(t, f.xAgent, http.MethodPost, ciGateCreatePath, b)
		}},
		{"broker", func(t *testing.T, f *rrGateFixture, b any) *httptest.ResponseRecorder {
			return f.asBroker(t, http.MethodPost, ciGateCreatePath, b)
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := rrGateSetup(t)
			require.False(t, f.rrGateMembersGroupExists(t, f.legacy),
				"legacy must start with no members group or this case measures nothing")

			rec := c.do(t, f, CreateProjectRequest{ID: f.legacy.ID, Name: f.legacy.Name})
			rrGateRequireMissingShaped(t, rec, c.name)

			require.False(t, f.rrGateMembersGroupExists(t, f.legacy),
				"%s: a refused idempotent create still ran the group/policy backfill "+
					"on a project this caller may not read", c.name)
			f.rrGateRequireSubjectIntact(t, f.legacy, c.name)
		})
	}
}

// TestCIGate_AdminCreateStillBackfills is the positive control for the write: the
// backfill is why this branch calls those functions, and a gate that stopped it
// for everyone would satisfy every assertion above.
func TestCIGate_AdminCreateStillBackfills(t *testing.T) {
	f := rrGateSetup(t)
	require.False(t, f.rrGateMembersGroupExists(t, f.legacy))

	rec := f.asUser(t, f.admin, http.MethodPost, ciGateCreatePath,
		CreateProjectRequest{ID: f.legacy.ID, Name: f.legacy.Name})
	require.Equal(t, http.StatusOK, rec.Code, "body=%s", rec.Body.String())

	require.True(t, f.rrGateMembersGroupExists(t, f.legacy),
		"the gate stopped the backfill for a caller it allowed through")
	require.Equal(t, []string{f.owner.ID}, f.rrGateOwners(t, f.legacy),
		"the backfill made someone other than the project's recorded creator an owner")
}

// ciGateOwnerlessProjectWithSoleMember builds the one shape the sole-member
// promotion backfill fires on: a project with no recorded creator, whose members
// group has exactly one member and no owner. The group is built by calling the
// production function rather than hand-seeded, so it is the group production
// emits.
func ciGateOwnerlessProjectWithSoleMember(t *testing.T, f *rrGateFixture,
	name string, member *store.User) *store.Project {
	t.Helper()
	ctx := context.Background()

	p := &store.Project{
		ID: tid("cigate-" + name), Name: name, Slug: tid("cigate-" + name),
		Visibility: store.VisibilityPrivate,
	}
	require.NoError(t, f.store.CreateProject(ctx, p))
	f.srv.createProjectMembersGroupAndPolicy(ctx, p)

	g, err := f.store.GetGroupBySlug(ctx, "project:"+p.Slug+":members")
	require.NoError(t, err)
	require.NoError(t, f.store.AddGroupMember(ctx, &store.GroupMember{
		GroupID:    g.ID,
		MemberType: store.GroupMemberTypeUser,
		MemberID:   member.ID,
		Role:       store.GroupMemberRoleMember,
	}))

	// Both halves of the precondition, because the promotion fires only on their
	// conjunction: an owner left behind, or a second member, and the arm under
	// test is never reached and the assertions below pass without measuring it.
	require.Empty(t, f.rrGateOwners(t, p),
		"precondition: the project must have no owner or the promotion never runs")
	members, err := f.store.GetGroupMembers(ctx, g.ID)
	require.NoError(t, err)
	require.Len(t, members, 1,
		"precondition: the caller must be the group's SOLE member or the "+
			"promotion never runs")
	return p
}

// TestCIGate_GateDoesNotShadowSelfPromotionRefusal exists because this commit
// takes coverage away, and says so.
//
// The sole-member promotion inside createProjectMembersGroupAndPolicy refuses one
// target: the caller of the current request. That refusal was pinned end to end
// by TestOGGrant_SelfPromotionRefusedByEveryRoute's POST /projects arm, where a
// plain member reached the backfill and was turned away there. With the read gate
// in front, that caller no longer reaches the backfill at all, so the arm now
// measures this gate and not the promotion — it went vacuous, exactly as its own
// comment records the GET arm already had.
//
// This restores an end-to-end path to the promotion. A HUB ADMIN who is the sole
// member of an ownerless project passes the read gate, reaches the backfill, and
// must still be refused the promotion, because the constraint is on the caller
// and not on the route. Verified non-vacuous by reverting the constraint: this
// test reds.
func TestCIGate_GateDoesNotShadowSelfPromotionRefusal(t *testing.T) {
	f := rrGateSetup(t)
	p := ciGateOwnerlessProjectWithSoleMember(t, f, "Sole Member Admin", f.admin)

	rec := f.asUser(t, f.admin, http.MethodPost, ciGateCreatePath,
		CreateProjectRequest{ID: p.ID, Name: p.Name})
	require.Equal(t, http.StatusOK, rec.Code,
		"the caller must get PAST the gate, or this test measures the gate again "+
			"instead of the promotion behind it; body=%s", rec.Body.String())

	require.Empty(t, f.rrGateOwners(t, p),
		"the sole member was promoted to owner by their own request")
}

// TestCIGate_FreeIDStillCreates is the boundary of the gate's condition, and the
// honest record of what it leaves in place. An id nobody holds is not a matched
// project: the caller gets 201 and a project of their own, exactly as before.
func TestCIGate_FreeIDStillCreates(t *testing.T) {
	f := rrGateSetup(t)
	newID := tid("cigate-free-id")

	rec := f.asUser(t, f.outsider, http.MethodPost, ciGateCreatePath,
		CreateProjectRequest{ID: newID, Name: "CI Gate Free ID"})
	require.Equal(t, http.StatusCreated, rec.Code, "body=%s", rec.Body.String())

	got, err := f.store.GetProject(context.Background(), newID)
	require.NoError(t, err, "creating a project on a free client-supplied id no longer works")
	require.Equal(t, f.outsider.ID, got.CreatedBy)
}

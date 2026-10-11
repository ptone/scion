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
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/GoogleCloudPlatform/scion/pkg/api"
	"github.com/GoogleCloudPlatform/scion/pkg/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// roleDefHidingStore reports one role definition as missing.
type roleDefHidingStore struct {
	store.Store
	hidden string
}

func (s *roleDefHidingStore) GetRoleDefinition(ctx context.Context, id string) (*store.RoleDefinition, error) {
	if id == s.hidden {
		return nil, store.ErrNotFound
	}
	return s.Store.GetRoleDefinition(ctx, id)
}

// projectGroupBindingsFaultStore fails the project-scoped group binding
// lookup chatMemberProjectIDs makes (read authorization does not make it).
type projectGroupBindingsFaultStore struct{ store.Store }

func (s *projectGroupBindingsFaultStore) ListRoleBindingsForPrincipals(ctx context.Context, principals []store.PrincipalRef, scopeTypes []string, scopeIDs []string) ([]*store.RoleBinding, error) {
	if len(scopeTypes) == 1 && scopeTypes[0] == store.RoleScopeProject {
		return nil, errors.New("injected project binding fault")
	}
	return s.Store.ListRoleBindingsForPrincipals(ctx, principals, scopeTypes, scopeIDs)
}

// membershipFixture builds users, groups and bindings for the parity cases.
type membershipFixture struct {
	t   *testing.T
	s   store.Store
	ctx context.Context
}

func (f *membershipFixture) project(name string) string {
	f.t.Helper()
	p := &store.Project{ID: api.NewUUID(), Name: name, Slug: name, Created: time.Now(), Updated: time.Now()}
	require.NoError(f.t, f.s.CreateProject(f.ctx, p))
	return p.ID
}

func (f *membershipFixture) group(name string) string {
	f.t.Helper()
	id := api.NewUUID()
	require.NoError(f.t, f.s.CreateGroup(f.ctx, &store.Group{ID: id, Name: name, Slug: name + "-" + id[:8],
		GroupType: store.GroupTypeExplicit, Created: time.Now(), Updated: time.Now()}))
	return id
}

func (f *membershipFixture) addMember(groupID, memberType, memberID string) {
	f.t.Helper()
	require.NoError(f.t, f.s.AddGroupMember(f.ctx, &store.GroupMember{GroupID: groupID, MemberType: memberType,
		MemberID: memberID, Role: store.GroupMemberRoleMember, AddedAt: time.Now()}))
}

func (f *membershipFixture) role(name string) string {
	f.t.Helper()
	rd, err := f.s.GetRoleDefinitionByName(f.ctx, name, store.RoleScopeProject)
	require.NoError(f.t, err)
	return rd.ID
}

func (f *membershipFixture) bind(roleID, principalType, principalID, projectID string, notBefore, expiresAt *time.Time) {
	f.t.Helper()
	_, err := f.s.CreateRoleBinding(f.ctx, &store.RoleBinding{RoleDefinitionID: roleID, PrincipalType: principalType,
		PrincipalID: principalID, ScopeType: store.RoleScopeProject, ScopeID: projectID,
		NotBefore: notBefore, ExpiresAt: expiresAt, CreatedBy: "test"})
	require.NoError(f.t, err)
}

// chatMemberProjectIDs is CheckEffectiveMembership for every project at
// once: for each case, the batched answer equals the single-project check
// (and the expected value), including group membership, nested groups,
// inactive bindings and a binding whose role definition is missing (the
// check fails, so no membership).
func TestChatMemberProjectIDs_MatchesCheckEffectiveMembership(t *testing.T) {
	srv, s := testServer(t)
	ctx := context.Background()
	f := &membershipFixture{t: t, s: s, ctx: ctx}
	u := addHumanMember(t, s, f.project("home"), "parity@example.com", "Parity")

	member, admin := f.role(store.ProjectRoleMember), f.role(store.ProjectRoleAdmin)
	past, later := time.Now().Add(-time.Hour), time.Now().Add(time.Hour)

	grp := f.group("parity-group")
	f.addMember(grp, store.GroupMemberTypeUser, u.ID)
	inner := f.group("parity-inner")
	f.addMember(inner, store.GroupMemberTypeUser, u.ID)
	outer := f.group("parity-outer")
	f.addMember(outer, store.GroupMemberTypeGroup, inner)

	direct := f.project("direct")
	f.bind(member, store.RoleBindingPrincipalUser, u.ID, direct, nil, nil)
	viaGroup := f.project("via-group")
	f.bind(member, store.RoleBindingPrincipalGroup, grp, viaGroup, nil, nil)
	nested := f.project("nested")
	f.bind(member, store.RoleBindingPrincipalGroup, outer, nested, nil, nil)
	// A group-bound owner role (which both functions ignore) cannot be
	// created: the store refuses project-owner for a group principal.
	expired := f.project("expired")
	f.bind(member, store.RoleBindingPrincipalUser, u.ID, expired, nil, &past)
	notYet := f.project("not-yet")
	f.bind(member, store.RoleBindingPrincipalUser, u.ID, notYet, &later, nil)
	missingDirect := f.project("missing-direct")
	f.bind(admin, store.RoleBindingPrincipalUser, u.ID, missingDirect, nil, nil)
	missingGroup := f.project("missing-group")
	f.bind(admin, store.RoleBindingPrincipalGroup, grp, missingGroup, nil, nil)
	// A valid binding does not rescue a project that also has one naming a
	// missing role definition: CheckEffectiveMembership fails for it.
	mixed := f.project("mixed")
	f.bind(member, store.RoleBindingPrincipalUser, u.ID, mixed, nil, nil)
	f.bind(admin, store.RoleBindingPrincipalGroup, grp, mixed, nil, nil)
	none := f.project("none")

	// project-admin is the role whose definition goes missing.
	srv.store = &roleDefHidingStore{Store: s, hidden: admin}

	got, err := srv.chatMemberProjectIDs(ctx, u.ID)
	require.NoError(t, err)
	for _, tc := range []struct {
		name    string
		project string
		want    bool
	}{
		{"direct member", direct, true},
		{"group member", viaGroup, true},
		{"nested group member", nested, true},
		{"expired direct binding", expired, false},
		{"not yet active binding", notYet, false},
		{"missing role definition, direct", missingDirect, false},
		{"missing role definition, group", missingGroup, false},
		{"valid and missing role definition", mixed, false},
		{"no binding", none, false},
	} {
		check := srv.CheckEffectiveMembership(ctx, u.ID, tc.project)
		assert.Equal(t, check.IsMember, got[tc.project], "%s: parity with CheckEffectiveMembership", tc.name)
		assert.Equal(t, tc.want, got[tc.project], tc.name)
	}
}

// A member through a group gets that project's unread counted on the badge
// and in the spaces rollup, like a direct member.
func TestChatUnreadCount_GroupMemberCounted(t *testing.T) {
	srv, s, wcs, proj := setupSharedChatTest(t)
	ctx := context.Background()
	f := &membershipFixture{t: t, s: s, ctx: ctx}
	u := &store.User{ID: api.NewUUID(), Email: "grp@example.com", DisplayName: "Grp",
		Role: "member", Status: "active", Created: time.Now()}
	require.NoError(t, s.CreateUser(ctx, u))
	ensureHubMembership(ctx, s, u.ID)
	grp := f.group("unread-group")
	f.addMember(grp, store.GroupMemberTypeUser, u.ID)
	f.bind(f.role(store.ProjectRoleMember), store.RoleBindingPrincipalGroup, grp, proj.ID, nil, nil)

	uf := &unreadFixture{t: t, s: s, wcs: wcs, proj: proj}
	uf.thread("unread", u.ID, false, true)

	rec := doRequestAsUser(t, srv, u, http.MethodGet, "/api/v1/chat/unread-count", nil)
	assert.Equal(t, chatUnreadCountResponse{Conversations: 1, Threads: 1}, getUnreadCount(t, rec))

	rec = doRequestAsUser(t, srv, u, http.MethodGet, "/api/v1/chat/spaces", nil)
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
	var spaces chatSpacesResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &spaces))
	var found bool
	for _, sp := range spaces.Spaces {
		if sp.ProjectID == proj.ID {
			found = true
			assert.True(t, sp.UnreadTracked)
			assert.Equal(t, 1, sp.UnreadCount)
		}
	}
	assert.True(t, found, "the group member's space is listed")
}

// A failed membership lookup fails closed: the spaces list still answers,
// with every space listed but none tracking unread, the thread list shows
// no unread, and the strict badge count fails instead.
func TestChatSpaces_MembershipLookupFailureTracksNothing(t *testing.T) {
	srv, s, wcs, proj := setupSharedChatTest(t)
	ctx := context.Background()
	me := DevUserID
	bindProjectMember(t, s, proj.ID, me)
	f := &membershipFixture{t: t, s: s, ctx: ctx}
	// A group, so the lookup reaches the project-scoped group bindings.
	f.addMember(f.group("fault-group"), store.GroupMemberTypeUser, me)
	uf := &unreadFixture{t: t, s: s, wcs: wcs, proj: proj}
	topicID, _ := uf.thread("unread", me, true, true)

	require.Equal(t, 1, spacesUnreadTotal(t, srv), "tracked before the fault")
	srv.store = &projectGroupBindingsFaultStore{Store: s}

	rec := doRequest(t, srv, http.MethodGet, "/api/v1/chat/spaces", nil)
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
	var spaces chatSpacesResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &spaces))
	var found bool
	for _, sp := range spaces.Spaces {
		assert.False(t, sp.UnreadTracked, "space %s", sp.ProjectSlug)
		assert.Zero(t, sp.UnreadCount, "space %s", sp.ProjectSlug)
		if sp.ProjectID == proj.ID {
			found = true
			assert.Equal(t, 1, sp.ThreadCount)
		}
	}
	assert.True(t, found, "the space is still listed")

	rec = doRequest(t, srv, http.MethodGet, "/api/v1/chat/spaces/"+proj.ID+"/threads", nil)
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
	var threads chatTopicListResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &threads))
	for _, th := range threads.Threads {
		if th.ID == topicID {
			assert.False(t, th.HasUnread)
		}
	}

	rec = doRequest(t, srv, http.MethodGet, "/api/v1/chat/unread-count", nil)
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}

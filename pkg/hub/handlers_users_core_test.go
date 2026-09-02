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
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/GoogleCloudPlatform/scion/pkg/api"
	"github.com/GoogleCloudPlatform/scion/pkg/store"
)

// TestDeleteUser_CascadesSkillInjections verifies that deleting a user removes
// all user-scoped skill injection entries, while leaving other scopes unaffected.
func TestDeleteUser_CascadesSkillInjections(t *testing.T) {
	srv, s := testServer(t)
	ctx := context.Background()

	// Create a test user to delete.
	user := &store.User{
		ID:          tid("user-cascade"),
		Email:       "cascade@example.com",
		DisplayName: "Cascade User",
		Role:        "member",
		Status:      "active",
	}
	require.NoError(t, s.CreateUser(ctx, user))

	// Create a second user whose skill injections should NOT be deleted.
	otherUser := &store.User{
		ID:          tid("user-other"),
		Email:       "other@example.com",
		DisplayName: "Other User",
		Role:        "member",
		Status:      "active",
	}
	require.NoError(t, s.CreateUser(ctx, otherUser))

	// Add user-scoped skill injections for the target user.
	si1 := &store.SkillInjection{
		ID:       api.NewUUID(),
		Scope:    store.SkillInjectionScopeUser,
		ScopeID:  user.ID,
		SkillURI: "skill://org/skill-a@1.0.0",
	}
	si2 := &store.SkillInjection{
		ID:       api.NewUUID(),
		Scope:    store.SkillInjectionScopeUser,
		ScopeID:  user.ID,
		SkillURI: "skill://org/skill-b@1.0.0",
	}
	require.NoError(t, s.AddSkillInjection(ctx, si1))
	require.NoError(t, s.AddSkillInjection(ctx, si2))

	// Add a skill injection for the other user (should be unaffected).
	siOther := &store.SkillInjection{
		ID:       api.NewUUID(),
		Scope:    store.SkillInjectionScopeUser,
		ScopeID:  otherUser.ID,
		SkillURI: "skill://org/skill-other@1.0.0",
	}
	require.NoError(t, s.AddSkillInjection(ctx, siOther))

	// Verify entries exist before deletion.
	list, err := s.ListSkillInjections(ctx, store.SkillInjectionScopeUser, user.ID)
	require.NoError(t, err)
	assert.Len(t, list, 2)

	// Delete the user via API.
	rec := doRequest(t, srv, http.MethodDelete, "/api/v1/users/"+user.ID, nil)
	assert.Equal(t, http.StatusNoContent, rec.Code)

	// Verify user skill injections were cascade-deleted.
	list, err = s.ListSkillInjections(ctx, store.SkillInjectionScopeUser, user.ID)
	require.NoError(t, err)
	assert.Empty(t, list, "user skill injections should be cascade-deleted on user deletion")

	// Verify the other user's skill injections were NOT affected.
	otherList, err := s.ListSkillInjections(ctx, store.SkillInjectionScopeUser, otherUser.ID)
	require.NoError(t, err)
	assert.Len(t, otherList, 1, "other user's skill injections should be unaffected")
}

// TestUpdateUser_RoleDemotion_RemovesSuperAdminBinding verifies that changing
// a user's role from "admin" to "viewer" deletes the super-admin role binding,
// preventing the user from retaining all permissions (D5 fix).
func TestUpdateUser_RoleDemotion_RemovesSuperAdminBinding(t *testing.T) {
	srv, s := testServer(t)
	ctx := context.Background()

	userID := tid("demote-user")
	require.NoError(t, s.CreateUser(ctx, &store.User{
		ID: userID, Email: "demote@example.com", DisplayName: "Demote User",
		Role: "admin", Status: "active",
	}))

	// Seed a super-admin binding (like startup backfill would create).
	superAdminRD, err := s.GetRoleDefinitionByName(ctx, store.SystemRoleSuperAdmin, store.RoleScopeSystem)
	require.NoError(t, err)
	_, err = s.CreateRoleBinding(ctx, &store.RoleBinding{
		RoleDefinitionID: superAdminRD.ID,
		PrincipalType:    store.RoleBindingPrincipalUser,
		PrincipalID:      userID,
		ScopeType:        store.RoleScopeSystem,
		CreatedBy:        store.SystemBackfillCreatedBy,
	})
	require.NoError(t, err)

	// Verify the super-admin binding exists.
	bindings, err := s.ListRoleBindingsForPrincipal(ctx, store.RoleBindingPrincipalUser, userID)
	require.NoError(t, err)
	hasSuperAdmin := false
	for _, b := range bindings {
		if b.RoleDefinitionID == superAdminRD.ID {
			hasSuperAdmin = true
			break
		}
	}
	require.True(t, hasSuperAdmin, "super-admin binding should exist before demotion")

	// Demote user to viewer.
	rec := doRequest(t, srv, http.MethodPatch, "/api/v1/users/"+userID, map[string]string{
		"role": "viewer",
	})
	require.Equal(t, http.StatusOK, rec.Code, "update should succeed: %s", rec.Body.String())

	// Verify the super-admin binding is gone.
	bindings, err = s.ListRoleBindingsForPrincipal(ctx, store.RoleBindingPrincipalUser, userID)
	require.NoError(t, err)
	for _, b := range bindings {
		if b.RoleDefinitionID == superAdminRD.ID {
			t.Fatal("super-admin binding should be deleted after demotion to viewer")
		}
	}

	// Verify a viewer binding was created.
	viewerRD, err := s.GetRoleDefinitionByName(ctx, store.SystemRoleHubViewer, store.RoleScopeSystem)
	require.NoError(t, err)
	hasViewer := false
	for _, b := range bindings {
		if b.RoleDefinitionID == viewerRD.ID {
			hasViewer = true
			break
		}
	}
	assert.True(t, hasViewer, "viewer binding should be created after demotion")
}

// TestUpdateUser_LastAdmin_DemotionRefused verifies that demoting the last
// super-admin user is refused with a 409 "last_admin" error (R-1).
func TestUpdateUser_LastAdmin_DemotionRefused(t *testing.T) {
	srv, s := testServer(t)
	ctx := context.Background()

	// First, delete ALL existing super-admin bindings (including the dev user's).
	superAdminRD, err := s.GetRoleDefinitionByName(ctx, store.SystemRoleSuperAdmin, store.RoleScopeSystem)
	require.NoError(t, err)

	filter := store.RoleBindingFilter{
		RoleDefinitionID: superAdminRD.ID,
		ScopeType:        store.RoleScopeSystem,
	}
	existingBindings, err := s.ListRoleBindingsFiltered(ctx, filter, 1000, 0)
	require.NoError(t, err)
	for _, b := range existingBindings {
		require.NoError(t, s.DeleteRoleBinding(ctx, b.ID))
	}

	// Create the sole admin user with a single super-admin binding.
	userID := tid("last-admin")
	require.NoError(t, s.CreateUser(ctx, &store.User{
		ID: userID, Email: "last-admin@example.com", DisplayName: "Last Admin",
		Role: "admin", Status: "active",
	}))
	_, err = s.CreateRoleBinding(ctx, &store.RoleBinding{
		RoleDefinitionID: superAdminRD.ID,
		PrincipalType:    store.RoleBindingPrincipalUser,
		PrincipalID:      userID,
		ScopeType:        store.RoleScopeSystem,
		CreatedBy:        store.SystemBackfillCreatedBy,
	})
	require.NoError(t, err)

	// Attempt demotion — should be refused.
	rec := doRequest(t, srv, http.MethodPatch, "/api/v1/users/"+userID, map[string]string{
		"role": "viewer",
	})
	assert.Equal(t, http.StatusConflict, rec.Code, "body: %s", rec.Body.String())
	assert.Contains(t, rec.Body.String(), "last_admin")

	// Verify user role was NOT changed (transaction rollback).
	user, err := s.GetUser(ctx, userID)
	require.NoError(t, err)
	assert.Equal(t, "admin", user.Role, "user role should not change on refused demotion")
}

// TestUpdateUser_ViewerCannotCreateProject verifies that after demotion from
// admin to viewer, the user can no longer create projects.
func TestUpdateUser_ViewerCannotCreateProject(t *testing.T) {
	srv, s := testServer(t)
	ctx := context.Background()

	userID := tid("viewer-no-proj")
	require.NoError(t, s.CreateUser(ctx, &store.User{
		ID: userID, Email: "viewer-noproj@example.com", DisplayName: "Viewer User",
		Role: "admin", Status: "active",
	}))

	// Seed super-admin binding (like startup backfill would create).
	superAdminRD, err := s.GetRoleDefinitionByName(ctx, store.SystemRoleSuperAdmin, store.RoleScopeSystem)
	require.NoError(t, err)
	_, err = s.CreateRoleBinding(ctx, &store.RoleBinding{
		RoleDefinitionID: superAdminRD.ID,
		PrincipalType:    store.RoleBindingPrincipalUser,
		PrincipalID:      userID,
		ScopeType:        store.RoleScopeSystem,
		CreatedBy:        store.SystemBackfillCreatedBy,
	})
	require.NoError(t, err)

	// Demote to viewer.
	rec := doRequest(t, srv, http.MethodPatch, "/api/v1/users/"+userID, map[string]string{
		"role": "viewer",
	})
	require.Equal(t, http.StatusOK, rec.Code)

	// Now verify authorization: viewer should NOT have project.create.
	if srv.authzService != nil {
		user := NewAuthenticatedUser(userID, "viewer-noproj@example.com", "Viewer User", "viewer", "api")
		decision := srv.authzService.Decide(ctx, AuthzRequest{
			Principal:  principalContextForIdentity(user),
			Credential: credentialContextForIdentity(user),
			Resource:   Resource{Type: "project"},
			Action:     ActionCreate,
			Permission: "project.create",
		})
		assert.False(t, decision.Allowed, "viewer should not have project.create permission")
	}
}

// TestUpdateUser_TransactionRollback_OnSyncFailure verifies the C-1 atomicity
// fix: when syncUserRoleBindings fails (e.g., last-admin guard), the User.Role
// update is rolled back — the database remains consistent.
func TestUpdateUser_TransactionRollback_OnSyncFailure(t *testing.T) {
	srv, s := testServer(t)
	ctx := context.Background()

	// Set up a sole super-admin (triggers last-admin guard on demotion).
	superAdminRD, err := s.GetRoleDefinitionByName(ctx, store.SystemRoleSuperAdmin, store.RoleScopeSystem)
	require.NoError(t, err)

	// Clear all existing super-admin bindings.
	filter := store.RoleBindingFilter{
		RoleDefinitionID: superAdminRD.ID,
		ScopeType:        store.RoleScopeSystem,
	}
	existingBindings, err := s.ListRoleBindingsFiltered(ctx, filter, 1000, 0)
	require.NoError(t, err)
	for _, b := range existingBindings {
		require.NoError(t, s.DeleteRoleBinding(ctx, b.ID))
	}

	userID := tid("txn-rollback")
	require.NoError(t, s.CreateUser(ctx, &store.User{
		ID: userID, Email: "txn-rollback@example.com", DisplayName: "TXN Test",
		Role: "admin", Status: "active",
	}))
	_, err = s.CreateRoleBinding(ctx, &store.RoleBinding{
		RoleDefinitionID: superAdminRD.ID,
		PrincipalType:    store.RoleBindingPrincipalUser,
		PrincipalID:      userID,
		ScopeType:        store.RoleScopeSystem,
		CreatedBy:        store.SystemBackfillCreatedBy,
	})
	require.NoError(t, err)

	// Attempt demotion — should fail with 409 (last admin).
	rec := doRequest(t, srv, http.MethodPatch, "/api/v1/users/"+userID, map[string]string{
		"role": "viewer",
	})
	require.Equal(t, http.StatusConflict, rec.Code, "body: %s", rec.Body.String())

	// Verify User.Role is still "admin" (transaction rolled back).
	user, err := s.GetUser(ctx, userID)
	require.NoError(t, err)
	assert.Equal(t, "admin", user.Role, "User.Role must be rolled back on sync failure")

	// Verify the super-admin binding still exists.
	bindings, err := s.ListRoleBindingsForPrincipal(ctx, store.RoleBindingPrincipalUser, userID)
	require.NoError(t, err)
	hasSuperAdmin := false
	for _, b := range bindings {
		if b.RoleDefinitionID == superAdminRD.ID {
			hasSuperAdmin = true
			break
		}
	}
	assert.True(t, hasSuperAdmin, "super-admin binding must survive after rollback")
}

// TestUpdateUser_ConcurrentRoleUpdates verifies R-5: concurrent PATCH requests
// changing the same user's role do not produce duplicate bindings. The WithTx
// wrapper (C-1 fix) serializes the operations at the database level.
func TestUpdateUser_ConcurrentRoleUpdates(t *testing.T) {
	srv, s := testServer(t)
	ctx := context.Background()

	// Create a second admin so the last-admin guard doesn't block.
	superAdminRD, err := s.GetRoleDefinitionByName(ctx, store.SystemRoleSuperAdmin, store.RoleScopeSystem)
	require.NoError(t, err)
	guardUserID := tid("guard-admin")
	require.NoError(t, s.CreateUser(ctx, &store.User{
		ID: guardUserID, Email: "guard@example.com", DisplayName: "Guard Admin",
		Role: "admin", Status: "active",
	}))
	_, err = s.CreateRoleBinding(ctx, &store.RoleBinding{
		RoleDefinitionID: superAdminRD.ID,
		PrincipalType:    store.RoleBindingPrincipalUser,
		PrincipalID:      guardUserID,
		ScopeType:        store.RoleScopeSystem,
		CreatedBy:        store.SystemBackfillCreatedBy,
	})
	require.NoError(t, err)

	// Create the target user as admin with super-admin binding.
	userID := tid("concurrent-user")
	require.NoError(t, s.CreateUser(ctx, &store.User{
		ID: userID, Email: "concurrent@example.com", DisplayName: "Concurrent Test",
		Role: "admin", Status: "active",
	}))
	_, err = s.CreateRoleBinding(ctx, &store.RoleBinding{
		RoleDefinitionID: superAdminRD.ID,
		PrincipalType:    store.RoleBindingPrincipalUser,
		PrincipalID:      userID,
		ScopeType:        store.RoleScopeSystem,
		CreatedBy:        store.SystemBackfillCreatedBy,
	})
	require.NoError(t, err)

	// Fire concurrent PATCH requests to change the user's role.
	const concurrency = 5
	var wg sync.WaitGroup
	roles := []string{"viewer", "member", "viewer", "member", "viewer"}
	results := make([]*struct {
		code int
		body string
	}, concurrency)

	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		i := i
		go func() {
			defer wg.Done()
			rec := doRequest(t, srv, http.MethodPatch, "/api/v1/users/"+userID, map[string]string{
				"role": roles[i],
			})
			results[i] = &struct {
				code int
				body string
			}{code: rec.Code, body: rec.Body.String()}
		}()
	}
	wg.Wait()

	// All requests should succeed (200 OK) — the transaction serializes them.
	for i, r := range results {
		assert.Equal(t, http.StatusOK, r.code, "request %d failed: %s", i, r.body)
	}

	// Verify the user has exactly ONE system-scope role binding.
	bindings, err := s.ListRoleBindingsForPrincipal(ctx, store.RoleBindingPrincipalUser, userID)
	require.NoError(t, err)

	systemBindings := 0
	for _, b := range bindings {
		if b.ScopeType == store.RoleScopeSystem {
			systemBindings++
		}
	}
	assert.Equal(t, 1, systemBindings,
		"user should have exactly 1 system-scope binding after concurrent updates, got %d", systemBindings)

	// Verify the final User.Role matches the final binding.
	user, err := s.GetUser(ctx, userID)
	require.NoError(t, err)

	// Determine which role definition matches the surviving binding.
	roleBindingRDMap := map[string]string{}
	roleDefs := []struct {
		name string
		role string
	}{
		{store.SystemRoleSuperAdmin, "admin"},
		{store.SystemRoleHubMember, "member"},
		{store.SystemRoleHubViewer, "viewer"},
	}
	for _, rd := range roleDefs {
		rdObj, err := s.GetRoleDefinitionByName(ctx, rd.name, store.RoleScopeSystem)
		if err == nil {
			roleBindingRDMap[rdObj.ID] = rd.role
		}
	}

	for _, b := range bindings {
		if b.ScopeType == store.RoleScopeSystem {
			expectedRole := roleBindingRDMap[b.RoleDefinitionID]
			assert.Equal(t, expectedRole, user.Role,
				"User.Role (%s) must match the surviving binding role (%s)", user.Role, expectedRole)
		}
	}
}

// TestUpdateUser_DuplicateBindingIdempotent verifies that re-assigning the same
// role (e.g., "viewer" → "viewer") is handled gracefully — no error, no
// duplicate bindings.
func TestUpdateUser_DuplicateBindingIdempotent(t *testing.T) {
	srv, s := testServer(t)
	ctx := context.Background()

	userID := tid("idempotent-user")
	require.NoError(t, s.CreateUser(ctx, &store.User{
		ID: userID, Email: "idempotent@example.com", DisplayName: "Idempotent Test",
		Role: "viewer", Status: "active",
	}))

	// Create the viewer binding manually.
	viewerRD, err := s.GetRoleDefinitionByName(ctx, store.SystemRoleHubViewer, store.RoleScopeSystem)
	require.NoError(t, err)
	_, err = s.CreateRoleBinding(ctx, &store.RoleBinding{
		RoleDefinitionID: viewerRD.ID,
		PrincipalType:    store.RoleBindingPrincipalUser,
		PrincipalID:      userID,
		ScopeType:        store.RoleScopeSystem,
		CreatedBy:        store.SystemReconcileCreatedBy,
	})
	require.NoError(t, err)

	// PATCH with the same role — should be a no-op.
	rec := doRequest(t, srv, http.MethodPatch, "/api/v1/users/"+userID, map[string]string{
		"role": "viewer",
	})
	assert.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())

	// Verify still exactly one viewer binding.
	bindings, err := s.ListRoleBindingsForPrincipal(ctx, store.RoleBindingPrincipalUser, userID)
	require.NoError(t, err)
	viewerCount := 0
	for _, b := range bindings {
		if b.RoleDefinitionID == viewerRD.ID && b.ScopeType == store.RoleScopeSystem {
			viewerCount++
		}
	}
	assert.Equal(t, 1, viewerCount, "should have exactly one viewer binding")
}

// TestUpdateUser_RoleSync_Returns500_OnError verifies C-1a: when the binding
// sync encounters an error (after the C-1 transactional fix), the handler
// returns an error response — not 200 OK.
func TestUpdateUser_RoleSync_Returns500_OnError(t *testing.T) {
	srv, s := testServer(t)
	ctx := context.Background()

	// Verify that the handler structure returns non-200 on sync errors by
	// reading the response body. We test this via the last-admin path (R-1)
	// which is a controlled failure in sync — the handler must NOT return 200.
	superAdminRD, err := s.GetRoleDefinitionByName(ctx, store.SystemRoleSuperAdmin, store.RoleScopeSystem)
	require.NoError(t, err)

	// Clear existing super-admin bindings.
	filter := store.RoleBindingFilter{
		RoleDefinitionID: superAdminRD.ID,
		ScopeType:        store.RoleScopeSystem,
	}
	existing, err := s.ListRoleBindingsFiltered(ctx, filter, 1000, 0)
	require.NoError(t, err)
	for _, b := range existing {
		require.NoError(t, s.DeleteRoleBinding(ctx, b.ID))
	}

	userID := tid("sync-error")
	require.NoError(t, s.CreateUser(ctx, &store.User{
		ID: userID, Email: "sync-error@example.com", DisplayName: "Sync Error Test",
		Role: "admin", Status: "active",
	}))
	_, err = s.CreateRoleBinding(ctx, &store.RoleBinding{
		RoleDefinitionID: superAdminRD.ID,
		PrincipalType:    store.RoleBindingPrincipalUser,
		PrincipalID:      userID,
		ScopeType:        store.RoleScopeSystem,
		CreatedBy:        store.SystemBackfillCreatedBy,
	})
	require.NoError(t, err)

	// Attempt demotion of last admin — sync MUST fail and handler MUST NOT
	// return 200 OK (this was the original C-1a bug: swallowed errors → 200).
	rec := doRequest(t, srv, http.MethodPatch, "/api/v1/users/"+userID, map[string]string{
		"role": "viewer",
	})

	assert.NotEqual(t, http.StatusOK, rec.Code,
		"handler must NOT return 200 when sync fails (C-1a fix); got body: %s", rec.Body.String())
	assert.Equal(t, http.StatusConflict, rec.Code,
		"expected 409 for last-admin guard; got %d: %s", rec.Code, rec.Body.String())

	// Parse the error response to verify structured error.
	var errResp struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	err = json.Unmarshal(rec.Body.Bytes(), &errResp)
	require.NoError(t, err, "response should be valid JSON error")
	assert.Equal(t, "last_admin", errResp.Error.Code, "error code should be 'last_admin'")
}

// TestUpdateUser_MultipleAdmins_DemotionAllowed verifies that demotion is
// permitted when multiple super-admin bindings exist (not the last admin).
func TestUpdateUser_MultipleAdmins_DemotionAllowed(t *testing.T) {
	srv, s := testServer(t)
	ctx := context.Background()

	superAdminRD, err := s.GetRoleDefinitionByName(ctx, store.SystemRoleSuperAdmin, store.RoleScopeSystem)
	require.NoError(t, err)

	// Create two admin users with super-admin bindings.
	user1ID := tid("multi-admin-1")
	user2ID := tid("multi-admin-2")

	for _, u := range []struct {
		id    string
		email string
	}{
		{user1ID, "admin1@example.com"},
		{user2ID, "admin2@example.com"},
	} {
		require.NoError(t, s.CreateUser(ctx, &store.User{
			ID: u.id, Email: u.email, DisplayName: "Admin",
			Role: "admin", Status: "active",
		}))
		_, err = s.CreateRoleBinding(ctx, &store.RoleBinding{
			RoleDefinitionID: superAdminRD.ID,
			PrincipalType:    store.RoleBindingPrincipalUser,
			PrincipalID:      u.id,
			ScopeType:        store.RoleScopeSystem,
			CreatedBy:        store.SystemBackfillCreatedBy,
		})
		require.NoError(t, err)
	}

	// Demote user1 — should succeed because user2 (and dev user) are still admins.
	rec := doRequest(t, srv, http.MethodPatch, "/api/v1/users/"+user1ID, map[string]string{
		"role": "viewer",
	})
	assert.Equal(t, http.StatusOK, rec.Code, "demotion should succeed with multiple admins: %s", rec.Body.String())

	// Verify user1 is now a viewer.
	user1, err := s.GetUser(ctx, user1ID)
	require.NoError(t, err)
	assert.Equal(t, "viewer", user1.Role)

	// Verify user1's super-admin binding was deleted.
	bindings, err := s.ListRoleBindingsForPrincipal(ctx, store.RoleBindingPrincipalUser, user1ID)
	require.NoError(t, err)
	for _, b := range bindings {
		assert.NotEqual(t, superAdminRD.ID, b.RoleDefinitionID,
			"super-admin binding should be deleted after demotion")
	}

	// Verify user1 has a viewer binding.
	viewerRD, err := s.GetRoleDefinitionByName(ctx, store.SystemRoleHubViewer, store.RoleScopeSystem)
	require.NoError(t, err)
	hasViewer := false
	for _, b := range bindings {
		if b.RoleDefinitionID == viewerRD.ID {
			hasViewer = true
			break
		}
	}
	assert.True(t, hasViewer, "viewer binding should exist after demotion")
}

// R3-REQ-2: Verify that the PATCH response body contains the updated role
// (not the stale pre-transaction value).
func TestUpdateUser_ResponseBodyContainsNewRole(t *testing.T) {
	srv, s := testServer(t)
	ctx := context.Background()

	superAdminRD, err := s.GetRoleDefinitionByName(ctx, store.SystemRoleSuperAdmin, store.RoleScopeSystem)
	require.NoError(t, err)

	// Create two admin users so the last-admin guard doesn't block.
	user1ID := tid("resp-body-1")
	user2ID := tid("resp-body-2")
	for _, u := range []struct {
		id    string
		email string
	}{
		{user1ID, "resp1@example.com"},
		{user2ID, "resp2@example.com"},
	} {
		require.NoError(t, s.CreateUser(ctx, &store.User{
			ID: u.id, Email: u.email, DisplayName: "Admin",
			Role: "admin", Status: "active",
		}))
		_, err = s.CreateRoleBinding(ctx, &store.RoleBinding{
			RoleDefinitionID: superAdminRD.ID,
			PrincipalType:    store.RoleBindingPrincipalUser,
			PrincipalID:      u.id,
			ScopeType:        store.RoleScopeSystem,
			CreatedBy:        store.SystemBackfillCreatedBy,
		})
		require.NoError(t, err)
	}

	// Demote user1 from admin to viewer.
	rec := doRequest(t, srv, http.MethodPatch, "/api/v1/users/"+user1ID, map[string]string{
		"role": "viewer",
	})
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())

	// The response body must contain the NEW role, not the old one.
	var respUser store.User
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&respUser))
	assert.Equal(t, "viewer", respUser.Role,
		"response body should contain the updated role, not the stale pre-transaction value")

	// Verify persistence matches the response.
	persisted, err := s.GetUser(ctx, user1ID)
	require.NoError(t, err)
	assert.Equal(t, respUser.Role, persisted.Role,
		"response role should match persisted role")
}

// R3-REQ-1: Verify that DELETE /api/v1/admin/role-bindings/:id refuses to
// delete the last super-admin binding.
func TestDeleteRoleBinding_LastSuperAdmin_Returns409(t *testing.T) {
	srv, s := testServer(t)
	ctx := context.Background()

	superAdminRD, err := s.GetRoleDefinitionByName(ctx, store.SystemRoleSuperAdmin, store.RoleScopeSystem)
	require.NoError(t, err)

	// The dev user (seeded by testServer) is the only super-admin.
	// Find their super-admin binding.
	devBindings, err := s.ListRoleBindingsForPrincipal(ctx, store.RoleBindingPrincipalUser, DevUserID)
	require.NoError(t, err)
	var devSuperAdminBindingID string
	for _, b := range devBindings {
		if b.RoleDefinitionID == superAdminRD.ID && b.ScopeType == store.RoleScopeSystem {
			devSuperAdminBindingID = b.ID
			break
		}
	}
	require.NotEmpty(t, devSuperAdminBindingID, "dev user should have a super-admin binding")

	// Try to delete the dev user's own super-admin binding — this is the
	// last one, so the guard should refuse with 409.
	rec := doRequest(t, srv, http.MethodDelete,
		"/api/v1/admin/role-bindings/"+devSuperAdminBindingID, nil)
	assert.Equal(t, http.StatusConflict, rec.Code,
		"should refuse to delete last super-admin binding: %s", rec.Body.String())

	// Verify the binding still exists.
	_, err = s.GetRoleBinding(ctx, devSuperAdminBindingID)
	assert.NoError(t, err, "last super-admin binding should not have been deleted")
}

// R3-REQ-1: Verify that DELETE succeeds when multiple super-admin bindings exist.
func TestDeleteRoleBinding_MultipleSuperAdmins_Succeeds(t *testing.T) {
	srv, s := testServer(t)
	ctx := context.Background()

	superAdminRD, err := s.GetRoleDefinitionByName(ctx, store.SystemRoleSuperAdmin, store.RoleScopeSystem)
	require.NoError(t, err)

	// Create two admin users with super-admin bindings.
	user1ID := tid("del-multi-1")
	user2ID := tid("del-multi-2")
	for _, u := range []struct {
		id    string
		email string
	}{
		{user1ID, "del1@example.com"},
		{user2ID, "del2@example.com"},
	} {
		require.NoError(t, s.CreateUser(ctx, &store.User{
			ID: u.id, Email: u.email, DisplayName: "Admin",
			Role: "admin", Status: "active",
		}))
		_, err = s.CreateRoleBinding(ctx, &store.RoleBinding{
			RoleDefinitionID: superAdminRD.ID,
			PrincipalType:    store.RoleBindingPrincipalUser,
			PrincipalID:      u.id,
			ScopeType:        store.RoleScopeSystem,
			CreatedBy:        store.SystemBackfillCreatedBy,
		})
		require.NoError(t, err)
	}

	// Get user1's super-admin binding.
	bindings, err := s.ListRoleBindingsForPrincipal(ctx, store.RoleBindingPrincipalUser, user1ID)
	require.NoError(t, err)
	var targetBindingID string
	for _, b := range bindings {
		if b.RoleDefinitionID == superAdminRD.ID && b.ScopeType == store.RoleScopeSystem {
			targetBindingID = b.ID
			break
		}
	}
	require.NotEmpty(t, targetBindingID, "should find super-admin binding for user1")

	// Delete user1's super-admin binding — should succeed because user2 and
	// dev user still have super-admin bindings.
	rec := doRequest(t, srv, http.MethodDelete,
		"/api/v1/admin/role-bindings/"+targetBindingID, nil)
	assert.Equal(t, http.StatusNoContent, rec.Code,
		"should succeed with multiple super-admins: %s", rec.Body.String())

	// Verify the binding is gone.
	_, err = s.GetRoleBinding(ctx, targetBindingID)
	assert.Error(t, err, "deleted binding should not exist")
}

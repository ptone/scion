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
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/GoogleCloudPlatform/scion/pkg/hub/permissions"
	"github.com/GoogleCloudPlatform/scion/pkg/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// seedRolesTestUser creates a user in the store if it does not already exist.
// The store requires user principals to exist before role bindings can reference them.
func seedRolesTestUser(t *testing.T, s store.Store, id, email string) {
	t.Helper()
	ctx := context.Background()
	if _, err := s.GetUser(ctx, id); err == nil {
		return // already exists
	}
	require.NoError(t, s.CreateUser(ctx, &store.User{
		ID: id, Email: email, DisplayName: email, Role: "member", Status: "active",
	}))
}

// seedRolesTestAgent creates an agent in the store with a minimal project,
// so role bindings referencing agent principals pass existence validation.
func seedRolesTestAgent(t *testing.T, s store.Store, agentID, projectID string) {
	t.Helper()
	ctx := context.Background()
	if _, err := s.GetAgent(ctx, agentID); err == nil {
		return // already exists
	}
	// Ensure project exists for the agent.
	if _, err := s.GetProject(ctx, projectID); err != nil {
		_ = s.CreateProject(ctx, &store.Project{
			ID: projectID, Name: "roles-test-project", Slug: "roles-test-project",
		})
	}
	require.NoError(t, s.CreateAgent(ctx, &store.Agent{
		ID: agentID, Slug: agentID, Name: "roles-test-agent", ProjectID: projectID,
	}))
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// createRoleViaAPI creates a role definition through the handler and returns it.
func createRoleViaAPI(t *testing.T, srv *Server, req createRoleDefinitionRequest) *store.RoleDefinition {
	t.Helper()
	rec := doRequest(t, srv, http.MethodPost, "/api/v1/admin/roles", req)
	require.Equal(t, http.StatusCreated, rec.Code, "body: %s", rec.Body.String())
	var def store.RoleDefinition
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&def))
	return &def
}

// createBindingViaAPI creates a role binding through the handler and returns it.
func createBindingViaAPI(t *testing.T, srv *Server, req createRoleBindingRequest) *store.RoleBinding {
	t.Helper()
	rec := doRequest(t, srv, http.MethodPost, "/api/v1/admin/role-bindings", req)
	require.Equal(t, http.StatusCreated, rec.Code, "body: %s", rec.Body.String())
	var rb store.RoleBinding
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&rb))
	return &rb
}

// ---------------------------------------------------------------------------
// Tests: Role Definition CRUD
// ---------------------------------------------------------------------------

func TestRolesAPI_CreateRoleDefinition(t *testing.T) {
	srv, _ := testServer(t)

	def := createRoleViaAPI(t, srv, createRoleDefinitionRequest{
		Name:        "custom-viewer",
		Description: "Custom viewer role",
		ScopeType:   "system",
		Permissions: []string{"agent.read", "project.read"},
	})

	assert.NotEmpty(t, def.ID)
	assert.Equal(t, "custom-viewer", def.Name)
	assert.Equal(t, "system", def.ScopeType)
	assert.Equal(t, []string{"agent.read", "project.read"}, def.Permissions)
	assert.False(t, def.System)
}

func TestRolesAPI_CreateRoleDefinition_MissingName(t *testing.T) {
	srv, _ := testServer(t)

	rec := doRequest(t, srv, http.MethodPost, "/api/v1/admin/roles", createRoleDefinitionRequest{
		ScopeType:   "system",
		Permissions: []string{"agent.read"},
	})
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestRolesAPI_CreateRoleDefinition_InvalidScopeType(t *testing.T) {
	srv, _ := testServer(t)

	rec := doRequest(t, srv, http.MethodPost, "/api/v1/admin/roles", createRoleDefinitionRequest{
		Name:        "bad-scope",
		ScopeType:   "invalid",
		Permissions: []string{"agent.read"},
	})
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestRolesAPI_CreateRoleDefinition_InvalidPermissions(t *testing.T) {
	srv, _ := testServer(t)

	rec := doRequest(t, srv, http.MethodPost, "/api/v1/admin/roles", createRoleDefinitionRequest{
		Name:        "bad-perms",
		ScopeType:   "system",
		Permissions: []string{"nonexistent.permission"},
	})
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestRolesAPI_CreateRoleDefinition_ProjectScope(t *testing.T) {
	srv, _ := testServer(t)

	def := createRoleViaAPI(t, srv, createRoleDefinitionRequest{
		Name:        "project-viewer",
		Description: "Project viewer role",
		ScopeType:   "project",
		Permissions: []string{"agent.read", "agent.list"},
	})

	assert.Equal(t, "project", def.ScopeType)
	assert.False(t, def.System)
}

func TestRolesAPI_GetRoleDefinition(t *testing.T) {
	srv, _ := testServer(t)

	created := createRoleViaAPI(t, srv, createRoleDefinitionRequest{
		Name:        "get-test-role",
		Description: "Get test",
		ScopeType:   "system",
		Permissions: []string{"agent.read"},
	})

	rec := doRequest(t, srv, http.MethodGet, "/api/v1/admin/roles/"+created.ID, nil)
	require.Equal(t, http.StatusOK, rec.Code)

	var def store.RoleDefinition
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&def))
	assert.Equal(t, created.ID, def.ID)
	assert.Equal(t, "get-test-role", def.Name)
}

func TestRolesAPI_GetRoleDefinition_NotFound(t *testing.T) {
	srv, _ := testServer(t)

	rec := doRequest(t, srv, http.MethodGet, "/api/v1/admin/roles/00000000-0000-0000-0000-000000000000", nil)
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestRolesAPI_ListRoleDefinitions(t *testing.T) {
	srv, _ := testServer(t)

	// System roles are seeded, so the list should already be non-empty.
	rec := doRequest(t, srv, http.MethodGet, "/api/v1/admin/roles", nil)
	require.Equal(t, http.StatusOK, rec.Code)

	var resp listRoleDefinitionsResponse
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
	assert.Greater(t, resp.TotalCount, 0)
}

func TestRolesAPI_UpdateRoleDefinition(t *testing.T) {
	srv, _ := testServer(t)

	created := createRoleViaAPI(t, srv, createRoleDefinitionRequest{
		Name:        "update-test-role",
		Description: "Before update",
		ScopeType:   "system",
		Permissions: []string{"agent.read"},
	})

	rec := doRequest(t, srv, http.MethodPut, "/api/v1/admin/roles/"+created.ID, updateRoleDefinitionRequest{
		Name:        "updated-role",
		Description: "After update",
		Permissions: []string{"agent.read", "project.read"},
	})
	require.Equal(t, http.StatusOK, rec.Code)

	var updated store.RoleDefinition
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&updated))
	assert.Equal(t, "updated-role", updated.Name)
	assert.Equal(t, "After update", updated.Description)
	assert.Equal(t, []string{"agent.read", "project.read"}, updated.Permissions)
}

func TestRolesAPI_UpdateRoleDefinition_SystemRole(t *testing.T) {
	srv, st := testServer(t)

	// Find a system role.
	defs, err := st.ListRoleDefinitions(t.Context())
	require.NoError(t, err)

	var systemRole *store.RoleDefinition
	for _, d := range defs {
		if d.System {
			systemRole = d
			break
		}
	}
	require.NotNil(t, systemRole, "expected at least one system role")

	rec := doRequest(t, srv, http.MethodPut, "/api/v1/admin/roles/"+systemRole.ID, updateRoleDefinitionRequest{
		Name:        "hacked-name",
		Description: "hacked",
		Permissions: []string{"agent.read"},
	})
	assert.Equal(t, http.StatusForbidden, rec.Code)
}

func TestRolesAPI_DeleteRoleDefinition(t *testing.T) {
	srv, _ := testServer(t)

	created := createRoleViaAPI(t, srv, createRoleDefinitionRequest{
		Name:        "delete-test-role",
		Description: "To be deleted",
		ScopeType:   "system",
		Permissions: []string{"agent.read"},
	})

	rec := doRequest(t, srv, http.MethodDelete, "/api/v1/admin/roles/"+created.ID, nil)
	assert.Equal(t, http.StatusNoContent, rec.Code)

	// Verify it's gone.
	rec = doRequest(t, srv, http.MethodGet, "/api/v1/admin/roles/"+created.ID, nil)
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestRolesAPI_DeleteRoleDefinition_SystemRole(t *testing.T) {
	srv, st := testServer(t)

	defs, err := st.ListRoleDefinitions(t.Context())
	require.NoError(t, err)

	var systemRole *store.RoleDefinition
	for _, d := range defs {
		if d.System {
			systemRole = d
			break
		}
	}
	require.NotNil(t, systemRole)

	rec := doRequest(t, srv, http.MethodDelete, "/api/v1/admin/roles/"+systemRole.ID, nil)
	assert.Equal(t, http.StatusForbidden, rec.Code)
}

func TestRolesAPI_DeleteRoleDefinition_WithBindings(t *testing.T) {
	srv, _ := testServer(t)

	// Create a custom role.
	role := createRoleViaAPI(t, srv, createRoleDefinitionRequest{
		Name:        "bound-role",
		Description: "Has bindings",
		ScopeType:   "system",
		Permissions: []string{"agent.read"},
	})

	// Create a binding to it.
	createBindingViaAPI(t, srv, createRoleBindingRequest{
		RoleDefinitionID: role.ID,
		PrincipalType:    "user",
		PrincipalID:      DevUserID,
		ScopeType:        "system",
	})

	// Try to delete — should fail because of active bindings.
	rec := doRequest(t, srv, http.MethodDelete, "/api/v1/admin/roles/"+role.ID, nil)
	assert.Equal(t, http.StatusConflict, rec.Code)
}

func TestRolesAPI_MethodNotAllowed_Roles(t *testing.T) {
	srv, _ := testServer(t)

	rec := doRequest(t, srv, http.MethodPatch, "/api/v1/admin/roles", nil)
	assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)
}

// ---------------------------------------------------------------------------
// Tests: Role Binding CRUD
// ---------------------------------------------------------------------------

func TestRolesAPI_CreateRoleBinding(t *testing.T) {
	srv, s := testServer(t)

	userID := tid("rb-some-user")
	seedRolesTestUser(t, s, userID, "rb-some-user@test.local")

	// Create a custom role first.
	role := createRoleViaAPI(t, srv, createRoleDefinitionRequest{
		Name:        "binding-test-role",
		Description: "For binding tests",
		ScopeType:   "system",
		Permissions: []string{"agent.read"},
	})

	binding := createBindingViaAPI(t, srv, createRoleBindingRequest{
		RoleDefinitionID: role.ID,
		PrincipalType:    "user",
		PrincipalID:      userID,
		ScopeType:        "system",
	})

	assert.NotEmpty(t, binding.ID)
	assert.Equal(t, role.ID, binding.RoleDefinitionID)
	assert.Equal(t, "user", binding.PrincipalType)
	assert.Equal(t, userID, binding.PrincipalID)
	assert.Equal(t, "system", binding.ScopeType)
}

func TestRolesAPI_CreateRoleBinding_MissingFields(t *testing.T) {
	srv, _ := testServer(t)

	tests := []struct {
		name string
		req  createRoleBindingRequest
	}{
		{"missing roleDefinitionId", createRoleBindingRequest{PrincipalType: "user", PrincipalID: "u1", ScopeType: "system"}},
		{"missing principalType", createRoleBindingRequest{RoleDefinitionID: "some-id", PrincipalID: "u1", ScopeType: "system"}},
		{"missing principalId", createRoleBindingRequest{RoleDefinitionID: "some-id", PrincipalType: "user", ScopeType: "system"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := doRequest(t, srv, http.MethodPost, "/api/v1/admin/role-bindings", tt.req)
			assert.Equal(t, http.StatusBadRequest, rec.Code, "body: %s", rec.Body.String())
		})
	}
}

func TestRolesAPI_CreateRoleBinding_InvalidPrincipalType(t *testing.T) {
	srv, _ := testServer(t)

	rec := doRequest(t, srv, http.MethodPost, "/api/v1/admin/role-bindings", createRoleBindingRequest{
		RoleDefinitionID: "some-id",
		PrincipalType:    "organization",
		PrincipalID:      "o1",
		ScopeType:        "system",
	})
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestRolesAPI_CreateRoleBinding_ProjectScopeMissingScopeID(t *testing.T) {
	srv, _ := testServer(t)

	role := createRoleViaAPI(t, srv, createRoleDefinitionRequest{
		Name:        "project-scope-no-id-role",
		ScopeType:   "project",
		Permissions: []string{"agent.read"},
	})

	// project-scoped binding with empty scope_id → 400
	rec := doRequest(t, srv, http.MethodPost, "/api/v1/admin/role-bindings", createRoleBindingRequest{
		RoleDefinitionID: role.ID,
		PrincipalType:    "user",
		PrincipalID:      "u1",
		ScopeType:        "project",
		ScopeID:          "",
	})
	assert.Equal(t, http.StatusBadRequest, rec.Code, "body: %s", rec.Body.String())
	assert.Contains(t, rec.Body.String(), "scope_id is required")
}

func TestRolesAPI_CreateRoleBinding_InvalidScopeType(t *testing.T) {
	srv, _ := testServer(t)

	rec := doRequest(t, srv, http.MethodPost, "/api/v1/admin/role-bindings", createRoleBindingRequest{
		RoleDefinitionID: "some-id",
		PrincipalType:    "user",
		PrincipalID:      "u1",
		ScopeType:        "invalid",
	})
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestRolesAPI_CreateRoleBinding_SuperAdmin_Blocked(t *testing.T) {
	srv, st := testServer(t)

	// Find the super-admin role definition.
	rd, err := st.GetRoleDefinitionByName(t.Context(), store.SystemRoleSuperAdmin, store.RoleScopeSystem)
	require.NoError(t, err)

	// Try to create a super-admin binding — should be blocked by the D10 store guard.
	rec := doRequest(t, srv, http.MethodPost, "/api/v1/admin/role-bindings", createRoleBindingRequest{
		RoleDefinitionID: rd.ID,
		PrincipalType:    "user",
		PrincipalID:      "some-user",
		ScopeType:        "system",
	})
	assert.Equal(t, http.StatusForbidden, rec.Code)
}

func TestRolesAPI_ListRoleBindings(t *testing.T) {
	srv, s := testServer(t)

	listUserID := tid("rb-list-user")
	seedRolesTestUser(t, s, listUserID, "rb-list-user@test.local")

	// Create a binding so the list is non-empty.
	role := createRoleViaAPI(t, srv, createRoleDefinitionRequest{
		Name:        "list-bindings-role",
		ScopeType:   "system",
		Permissions: []string{"agent.read"},
	})
	createBindingViaAPI(t, srv, createRoleBindingRequest{
		RoleDefinitionID: role.ID,
		PrincipalType:    "user",
		PrincipalID:      listUserID,
		ScopeType:        "system",
	})

	rec := doRequest(t, srv, http.MethodGet, "/api/v1/admin/role-bindings", nil)
	require.Equal(t, http.StatusOK, rec.Code)

	var resp listRoleBindingsResponse
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
	assert.GreaterOrEqual(t, resp.TotalCount, 1)
}

func TestRolesAPI_DeleteRoleBinding(t *testing.T) {
	srv, s := testServer(t)

	delUserID := tid("rb-del-user")
	seedRolesTestUser(t, s, delUserID, "rb-del-user@test.local")

	// Create a custom role and binding.
	role := createRoleViaAPI(t, srv, createRoleDefinitionRequest{
		Name:        "del-binding-role",
		ScopeType:   "system",
		Permissions: []string{"agent.read"},
	})
	binding := createBindingViaAPI(t, srv, createRoleBindingRequest{
		RoleDefinitionID: role.ID,
		PrincipalType:    "user",
		PrincipalID:      delUserID,
		ScopeType:        "system",
	})

	rec := doRequest(t, srv, http.MethodDelete, "/api/v1/admin/role-bindings/"+binding.ID, nil)
	assert.Equal(t, http.StatusNoContent, rec.Code)
}

func TestRolesAPI_DeleteRoleBinding_NotFound(t *testing.T) {
	srv, _ := testServer(t)

	rec := doRequest(t, srv, http.MethodDelete, "/api/v1/admin/role-bindings/00000000-0000-0000-0000-000000000000", nil)
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

// TestRolesAPI_DeleteRoleBinding_ProjectScoped verifies that the admin API can
// delete a project-scoped binding (system-authorized bypass of project governance).
func TestRolesAPI_DeleteRoleBinding_ProjectScoped(t *testing.T) {
	srv, s := testServer(t)
	ctx := context.Background()

	ownerID := tid("ps-del-owner")
	memberID := tid("ps-del-member")
	projectID := tid("ps-del-project")

	seedRolesTestUser(t, s, ownerID, "ps-del-owner@test.local")
	seedRolesTestUser(t, s, memberID, "ps-del-member@test.local")
	require.NoError(t, s.CreateProject(ctx, &store.Project{
		ID: projectID, Name: "ps-del-project", Slug: "ps-del-project",
		OwnerID: ownerID, CreatedBy: ownerID, Created: time.Now(), Updated: time.Now(),
	}))

	// Seed owner binding so the project has an owner.
	ownerRoleDef, err := s.GetRoleDefinitionByName(ctx, store.ProjectRoleOwner, store.RoleScopeProject)
	require.NoError(t, err)
	_, err = s.CreateRoleBinding(ctx, &store.RoleBinding{
		RoleDefinitionID: ownerRoleDef.ID,
		PrincipalType:    store.RoleBindingPrincipalUser,
		PrincipalID:      ownerID,
		ScopeType:        store.RoleScopeProject,
		ScopeID:          projectID,
	})
	require.NoError(t, err)

	// Create a member binding to delete.
	memberRoleDef, err := s.GetRoleDefinitionByName(ctx, store.ProjectRoleMember, store.RoleScopeProject)
	require.NoError(t, err)
	memberBinding, err := s.CreateRoleBinding(ctx, &store.RoleBinding{
		RoleDefinitionID: memberRoleDef.ID,
		PrincipalType:    store.RoleBindingPrincipalUser,
		PrincipalID:      memberID,
		ScopeType:        store.RoleScopeProject,
		ScopeID:          projectID,
	})
	require.NoError(t, err)

	// Admin API: DELETE project-scoped binding — should succeed (system-authorized).
	rec := doRequest(t, srv, http.MethodDelete, "/api/v1/admin/role-bindings/"+memberBinding.ID, nil)
	assert.Equal(t, http.StatusNoContent, rec.Code, "admin should be able to delete project-scoped binding: %s", rec.Body.String())

	// Verify binding is actually removed.
	_, err = s.GetRoleBinding(ctx, memberBinding.ID)
	assert.ErrorIs(t, err, store.ErrNotFound, "binding should be deleted from store")
}

// TestRolesAPI_DeleteRoleBinding_LastOwnerProtected verifies the last-owner
// invariant is preserved even through the admin endpoint.
func TestRolesAPI_DeleteRoleBinding_LastOwnerProtected(t *testing.T) {
	srv, s := testServer(t)
	ctx := context.Background()

	ownerID := tid("lo-del-owner")
	projectID := tid("lo-del-project")

	seedRolesTestUser(t, s, ownerID, "lo-del-owner@test.local")
	require.NoError(t, s.CreateProject(ctx, &store.Project{
		ID: projectID, Name: "lo-del-project", Slug: "lo-del-project",
		OwnerID: ownerID, CreatedBy: ownerID, Created: time.Now(), Updated: time.Now(),
	}))

	ownerRoleDef, err := s.GetRoleDefinitionByName(ctx, store.ProjectRoleOwner, store.RoleScopeProject)
	require.NoError(t, err)
	ownerBinding, err := s.CreateRoleBinding(ctx, &store.RoleBinding{
		RoleDefinitionID: ownerRoleDef.ID,
		PrincipalType:    store.RoleBindingPrincipalUser,
		PrincipalID:      ownerID,
		ScopeType:        store.RoleScopeProject,
		ScopeID:          projectID,
	})
	require.NoError(t, err)

	// Try to delete the last owner — should return 409 (last_owner).
	rec := doRequest(t, srv, http.MethodDelete, "/api/v1/admin/role-bindings/"+ownerBinding.ID, nil)
	assert.Equal(t, http.StatusConflict, rec.Code, "should not delete last owner")

	var resp map[string]interface{}
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
	errObj, _ := resp["error"].(map[string]interface{})
	assert.Equal(t, "last_owner", errObj["code"], "denial code should be last_owner")
}

// TestRolesAPI_DeleteRoleBinding_Idempotent verifies that deleting an already-
// deleted binding returns 404 (not 409 or 500).
func TestRolesAPI_DeleteRoleBinding_Idempotent(t *testing.T) {
	srv, s := testServer(t)

	delUserID := tid("idem-del-user")
	seedRolesTestUser(t, s, delUserID, "idem-del-user@test.local")

	role := createRoleViaAPI(t, srv, createRoleDefinitionRequest{
		Name:        "idem-del-role",
		ScopeType:   "system",
		Permissions: []string{"agent.read"},
	})
	binding := createBindingViaAPI(t, srv, createRoleBindingRequest{
		RoleDefinitionID: role.ID,
		PrincipalType:    "user",
		PrincipalID:      delUserID,
		ScopeType:        "system",
	})

	// First delete — success.
	rec := doRequest(t, srv, http.MethodDelete, "/api/v1/admin/role-bindings/"+binding.ID, nil)
	assert.Equal(t, http.StatusNoContent, rec.Code)

	// Second delete — should return 404, not 409.
	rec = doRequest(t, srv, http.MethodDelete, "/api/v1/admin/role-bindings/"+binding.ID, nil)
	assert.Equal(t, http.StatusNotFound, rec.Code, "repeated deletion should return 404")
}

func TestRolesAPI_ListBindingsForUser(t *testing.T) {
	srv, s := testServer(t)

	targetUserID := tid("rb-target-user")
	seedRolesTestUser(t, s, targetUserID, "rb-target-user@test.local")

	// Create a custom role and bind it to a specific user.
	role := createRoleViaAPI(t, srv, createRoleDefinitionRequest{
		Name:        "user-binding-role",
		ScopeType:   "system",
		Permissions: []string{"agent.read"},
	})
	createBindingViaAPI(t, srv, createRoleBindingRequest{
		RoleDefinitionID: role.ID,
		PrincipalType:    "user",
		PrincipalID:      targetUserID,
		ScopeType:        "system",
	})

	rec := doRequest(t, srv, http.MethodGet, "/api/v1/admin/role-bindings/user/"+targetUserID, nil)
	require.Equal(t, http.StatusOK, rec.Code)

	var resp listRoleBindingsResponse
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
	assert.GreaterOrEqual(t, resp.TotalCount, 1)
}

func TestRolesAPI_ListBindingsForUser_Empty(t *testing.T) {
	srv, _ := testServer(t)

	rec := doRequest(t, srv, http.MethodGet, "/api/v1/admin/role-bindings/user/nonexistent-user", nil)
	require.Equal(t, http.StatusOK, rec.Code)

	var resp listRoleBindingsResponse
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
	assert.Equal(t, 0, resp.TotalCount)
}

func TestRolesAPI_MethodNotAllowed_Bindings(t *testing.T) {
	srv, _ := testServer(t)

	rec := doRequest(t, srv, http.MethodPatch, "/api/v1/admin/role-bindings", nil)
	assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)
}

// ---------------------------------------------------------------------------
// Tests: Permissions Registry Endpoint
// ---------------------------------------------------------------------------

func TestRolesAPI_ListPermissions(t *testing.T) {
	srv, _ := testServer(t)

	rec := doRequest(t, srv, http.MethodGet, "/api/v1/admin/permissions", nil)
	require.Equal(t, http.StatusOK, rec.Code)

	var resp listPermissionsResponse
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
	assert.Equal(t, len(permissions.Registry), resp.TotalCount)
	assert.Greater(t, resp.TotalCount, 0)

	// Verify role permissions are in the registry.
	found := make(map[string]bool)
	for _, p := range resp.Items {
		found[p.ID] = true
	}
	assert.True(t, found["role.read"], "role.read should be in registry")
	assert.True(t, found["role.create"], "role.create should be in registry")
	assert.True(t, found["role_binding.read"], "role_binding.read should be in registry")
	assert.True(t, found["role_binding.create"], "role_binding.create should be in registry")
}

func TestRolesAPI_ListPermissions_MethodNotAllowed(t *testing.T) {
	srv, _ := testServer(t)

	rec := doRequest(t, srv, http.MethodPost, "/api/v1/admin/permissions", nil)
	assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)
}

// ---------------------------------------------------------------------------
// Tests: Unauthenticated access
// ---------------------------------------------------------------------------

func TestRolesAPI_Unauthenticated_Roles(t *testing.T) {
	srv, _ := testServer(t)

	rec := doRequestNoAuth(t, srv, http.MethodGet, "/api/v1/admin/roles", nil)
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestRolesAPI_Unauthenticated_Bindings(t *testing.T) {
	srv, _ := testServer(t)

	rec := doRequestNoAuth(t, srv, http.MethodGet, "/api/v1/admin/role-bindings", nil)
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestRolesAPI_Unauthenticated_Permissions(t *testing.T) {
	srv, _ := testServer(t)

	rec := doRequestNoAuth(t, srv, http.MethodGet, "/api/v1/admin/permissions", nil)
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

// ---------------------------------------------------------------------------
// Tests: Validate Permission IDs
// ---------------------------------------------------------------------------

func TestValidatePermissionIDs_Valid(t *testing.T) {
	err := validatePermissionIDs([]string{"agent.read", "project.read"})
	assert.NoError(t, err)
}

func TestValidatePermissionIDs_Invalid(t *testing.T) {
	err := validatePermissionIDs([]string{"agent.read", "fake.perm"})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "fake.perm")
}

func TestValidatePermissionIDs_Empty(t *testing.T) {
	err := validatePermissionIDs(nil)
	assert.NoError(t, err)
}

// ---------------------------------------------------------------------------
// Tests: Role Binding with agent principal type
// ---------------------------------------------------------------------------

func TestRolesAPI_CreateRoleBinding_AgentPrincipal(t *testing.T) {
	srv, s := testServer(t)

	agentID := tid("rb-some-agent")
	projectID := tid("rb-agent-project")
	seedRolesTestAgent(t, s, agentID, projectID)

	role := createRoleViaAPI(t, srv, createRoleDefinitionRequest{
		Name:        "agent-binding-role",
		ScopeType:   "project",
		Permissions: []string{"agent.read"},
	})

	binding := createBindingViaAPI(t, srv, createRoleBindingRequest{
		RoleDefinitionID: role.ID,
		PrincipalType:    "agent",
		PrincipalID:      agentID,
		ScopeType:        "project",
		ScopeID:          projectID,
	})

	assert.Equal(t, "agent", binding.PrincipalType)
	assert.Equal(t, agentID, binding.PrincipalID)
	assert.Equal(t, "project", binding.ScopeType)
}

func TestRolesAPI_CreateRoleBinding_GroupPrincipal(t *testing.T) {
	srv, s := testServer(t)
	ctx := t.Context()

	// Create a group so the group existence check passes
	require.NoError(t, s.CreateGroup(ctx, &store.Group{
		ID: tid("rb-test-group"), Slug: "rb-test-group", Name: "RB Test Group",
	}))

	role := createRoleViaAPI(t, srv, createRoleDefinitionRequest{
		Name:        "group-binding-role",
		ScopeType:   "system",
		Permissions: []string{"agent.read"},
	})

	binding := createBindingViaAPI(t, srv, createRoleBindingRequest{
		RoleDefinitionID: role.ID,
		PrincipalType:    "group",
		PrincipalID:      tid("rb-test-group"),
		ScopeType:        "system",
	})

	assert.Equal(t, "group", binding.PrincipalType)
	assert.Equal(t, tid("rb-test-group"), binding.PrincipalID)
	assert.Equal(t, "system", binding.ScopeType)
}

func TestRolesAPI_CreateRoleBinding_GroupPrincipal_NotFound(t *testing.T) {
	srv, _ := testServer(t)

	role := createRoleViaAPI(t, srv, createRoleDefinitionRequest{
		Name:        "group-notfound-role",
		ScopeType:   "system",
		Permissions: []string{"agent.read"},
	})

	// Try to create a binding for a non-existent group
	rec := doRequest(t, srv, http.MethodPost, "/api/v1/admin/role-bindings", createRoleBindingRequest{
		RoleDefinitionID: role.ID,
		PrincipalType:    "group",
		PrincipalID:      "00000000-0000-0000-0000-000000000099",
		ScopeType:        "system",
	})
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "group not found")
}

func TestRolesAPI_CreateRoleBinding_GroupPrincipal_BySlug(t *testing.T) {
	srv, s := testServer(t)
	ctx := t.Context()

	// Create a group with a known slug.
	require.NoError(t, s.CreateGroup(ctx, &store.Group{
		ID: tid("slug-resolve-group"), Slug: "my-team-slug", Name: "Slug Resolve Group",
	}))

	role := createRoleViaAPI(t, srv, createRoleDefinitionRequest{
		Name:        "slug-resolve-role",
		ScopeType:   "system",
		Permissions: []string{"agent.read"},
	})

	// Use the slug as principalId — should resolve to the UUID.
	binding := createBindingViaAPI(t, srv, createRoleBindingRequest{
		RoleDefinitionID: role.ID,
		PrincipalType:    "group",
		PrincipalID:      "my-team-slug",
		ScopeType:        "system",
	})

	assert.Equal(t, "group", binding.PrincipalType)
	assert.Equal(t, tid("slug-resolve-group"), binding.PrincipalID, "principalId should be resolved to UUID")
}

func TestRolesAPI_CreateRoleBinding_GroupPrincipal_NeitherUUIDNorSlug(t *testing.T) {
	srv, _ := testServer(t)

	role := createRoleViaAPI(t, srv, createRoleDefinitionRequest{
		Name:        "neither-uuid-nor-slug-role",
		ScopeType:   "system",
		Permissions: []string{"agent.read"},
	})

	// A non-UUID string that also doesn't match any slug.
	rec := doRequest(t, srv, http.MethodPost, "/api/v1/admin/role-bindings", createRoleBindingRequest{
		RoleDefinitionID: role.ID,
		PrincipalType:    "group",
		PrincipalID:      "nonexistent-slug",
		ScopeType:        "system",
	})
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "group not found")
}

// ---------------------------------------------------------------------------
// Tests: Update role definition with invalid permissions
// ---------------------------------------------------------------------------

func TestRolesAPI_UpdateRoleDefinition_InvalidPermissions(t *testing.T) {
	srv, _ := testServer(t)

	created := createRoleViaAPI(t, srv, createRoleDefinitionRequest{
		Name:        "update-invalid-perms",
		ScopeType:   "system",
		Permissions: []string{"agent.read"},
	})

	rec := doRequest(t, srv, http.MethodPut, "/api/v1/admin/roles/"+created.ID, updateRoleDefinitionRequest{
		Name:        "updated-name",
		Permissions: []string{"nonexistent.permission"},
	})
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

// ---------------------------------------------------------------------------
// Helpers: Non-admin identity
// ---------------------------------------------------------------------------

const (
	nonAdminUserID    = "11111111-1111-1111-1111-111111111111"
	nonAdminUserEmail = "scoped-admin@test.local"
)

// doRequestAsIdentity performs an HTTP request with a custom identity injected
// into the context. This bypasses the dev-auth super-admin fast-path by using
// an AuthenticatedUser whose Role() != "admin", so that CanDelegate and
// permission-based route guards are actually exercised.
func doRequestAsIdentity(t *testing.T, srv *Server, identity Identity, method, path string, body interface{}) *httptest.ResponseRecorder {
	t.Helper()
	var bodyBytes []byte
	if body != nil {
		var err error
		bodyBytes, err = json.Marshal(body)
		if err != nil {
			t.Fatalf("failed to marshal body: %v", err)
		}
	}

	req := httptest.NewRequest(method, path, bytes.NewReader(bodyBytes))
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	// Inject the custom identity directly into the context so the route
	// guard and handler see a non-admin user instead of the DevUser.
	ctx := contextWithIdentity(req.Context(), identity)
	req = req.WithContext(ctx)

	// Serve using the mux directly (bypassing auth middleware) since the
	// identity is already in the context. The route guards and handlers
	// call GetIdentityFromContext, which will find our injected identity.
	rec := httptest.NewRecorder()
	srv.mux.ServeHTTP(rec, req)
	return rec
}

// setupNonAdminUser creates a non-admin user identity and grants it specific
// permissions via a custom role binding. Returns the identity.
//
// The user gets a role binding granting the specified permissions at system scope.
func setupNonAdminUser(t *testing.T, st store.Store, perms []string) *AuthenticatedUser {
	t.Helper()
	ctx := t.Context()

	// Ensure the user exists in the store (CreateRoleBinding validates principal existence).
	seedRolesTestUser(t, st, nonAdminUserID, nonAdminUserEmail)

	user := NewAuthenticatedUser(nonAdminUserID, nonAdminUserEmail, "Scoped Admin", "member", "api")

	// Create a custom role definition with the requested permissions.
	rd, err := st.CreateRoleDefinition(ctx, &store.RoleDefinition{
		Name:        "test-scoped-admin-" + t.Name(),
		Description: "Test scoped admin role",
		ScopeType:   store.RoleScopeSystem,
		Permissions: perms,
		System:      false,
	})
	require.NoError(t, err)

	// Bind it to our test user.
	_, err = st.CreateRoleBinding(ctx, &store.RoleBinding{
		RoleDefinitionID: rd.ID,
		PrincipalType:    store.RoleBindingPrincipalUser,
		PrincipalID:      nonAdminUserID,
		ScopeType:        store.RoleScopeSystem,
		CreatedBy:        "test-setup",
	})
	require.NoError(t, err)

	return user
}

// ---------------------------------------------------------------------------
// Tests: Non-admin CanDelegate enforcement
// ---------------------------------------------------------------------------

func TestRolesAPI_NonAdmin_CreateRole_WithHeldPermissions(t *testing.T) {
	srv, st := testServer(t)

	// Give non-admin user role.create, role.read, and agent.read permissions.
	user := setupNonAdminUser(t, st, []string{"role.create", "role.read", "agent.read"})

	// Create a role containing only permissions the user holds → should succeed.
	rec := doRequestAsIdentity(t, srv, user, http.MethodPost, "/api/v1/admin/roles", createRoleDefinitionRequest{
		Name:        "non-admin-created-role",
		Description: "Created by non-admin",
		ScopeType:   "system",
		Permissions: []string{"agent.read"},
	})
	require.Equal(t, http.StatusCreated, rec.Code, "body: %s", rec.Body.String())

	var def store.RoleDefinition
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&def))
	assert.Equal(t, "non-admin-created-role", def.Name)
	assert.Equal(t, []string{"agent.read"}, def.Permissions)
}

func TestRolesAPI_NonAdmin_CreateRole_WithUnheldPermissions(t *testing.T) {
	srv, st := testServer(t)

	// Give non-admin user role.create and agent.read, but NOT user.suspend.
	user := setupNonAdminUser(t, st, []string{"role.create", "role.read", "agent.read"})

	// Try to create a role containing user.suspend → should get 403 (CanDelegate denies).
	rec := doRequestAsIdentity(t, srv, user, http.MethodPost, "/api/v1/admin/roles", createRoleDefinitionRequest{
		Name:        "escalated-role",
		Description: "Attempting privilege escalation",
		ScopeType:   "system",
		Permissions: []string{"agent.read", "user.suspend"},
	})
	assert.Equal(t, http.StatusForbidden, rec.Code, "body: %s", rec.Body.String())
}

func TestRolesAPI_NonAdmin_UpdateRole_WithUnheldPermissions(t *testing.T) {
	srv, st := testServer(t)

	// Give non-admin user role.create, role.update, role.read, and agent.read.
	user := setupNonAdminUser(t, st, []string{"role.create", "role.update", "role.read", "agent.read"})

	// First, create a role as admin (DevUser) with only agent.read.
	created := createRoleViaAPI(t, srv, createRoleDefinitionRequest{
		Name:        "update-escalation-test",
		Description: "Role to be updated by non-admin",
		ScopeType:   "system",
		Permissions: []string{"agent.read"},
	})

	// Non-admin tries to update the role to add user.suspend → should get 403.
	rec := doRequestAsIdentity(t, srv, user, http.MethodPut, "/api/v1/admin/roles/"+created.ID, updateRoleDefinitionRequest{
		Name:        "update-escalation-test",
		Description: "Escalated",
		Permissions: []string{"agent.read", "user.suspend"},
	})
	assert.Equal(t, http.StatusForbidden, rec.Code, "body: %s", rec.Body.String())
}

func TestRolesAPI_NonAdmin_CreateBinding_WithHeldPermissions(t *testing.T) {
	srv, st := testServer(t)

	otherUserID := tid("rb-other-user-held")
	seedRolesTestUser(t, st, otherUserID, "rb-other-user-held@test.local")

	// Give non-admin user role_binding.create, role_binding.read, and agent.read.
	user := setupNonAdminUser(t, st, []string{"role_binding.create", "role_binding.read", "agent.read"})

	// Create a role as admin with only agent.read (a permission our user holds).
	role := createRoleViaAPI(t, srv, createRoleDefinitionRequest{
		Name:        "binding-held-test",
		Description: "Contains only held permissions",
		ScopeType:   "system",
		Permissions: []string{"agent.read"},
	})

	// Non-admin creates binding for this role → should succeed.
	rec := doRequestAsIdentity(t, srv, user, http.MethodPost, "/api/v1/admin/role-bindings", createRoleBindingRequest{
		RoleDefinitionID: role.ID,
		PrincipalType:    "user",
		PrincipalID:      otherUserID,
		ScopeType:        "system",
	})
	require.Equal(t, http.StatusCreated, rec.Code, "body: %s", rec.Body.String())
}

func TestRolesAPI_NonAdmin_CreateBinding_WithUnheldPermissions(t *testing.T) {
	srv, st := testServer(t)

	otherUserID := tid("rb-other-user-unheld")
	seedRolesTestUser(t, st, otherUserID, "rb-other-user-unheld@test.local")

	// Give non-admin user role_binding.create, role_binding.read, and agent.read
	// but NOT user.suspend.
	user := setupNonAdminUser(t, st, []string{"role_binding.create", "role_binding.read", "agent.read"})

	// Create a role as admin that includes user.suspend (a permission our user does NOT hold).
	role := createRoleViaAPI(t, srv, createRoleDefinitionRequest{
		Name:        "binding-unheld-test",
		Description: "Contains unheld permissions",
		ScopeType:   "system",
		Permissions: []string{"agent.read", "user.suspend"},
	})

	// Non-admin tries to create binding for this role → should get 403.
	rec := doRequestAsIdentity(t, srv, user, http.MethodPost, "/api/v1/admin/role-bindings", createRoleBindingRequest{
		RoleDefinitionID: role.ID,
		PrincipalType:    "user",
		PrincipalID:      otherUserID,
		ScopeType:        "system",
	})
	assert.Equal(t, http.StatusForbidden, rec.Code, "body: %s", rec.Body.String())
}

// ---------------------------------------------------------------------------
// Tests: Lifecycle fields (notBefore / expiresAt) — C9
// ---------------------------------------------------------------------------

func TestRolesAPI_CreateRoleBinding_WithLifecycleFields(t *testing.T) {
	srv, s := testServer(t)

	userID := tid("rb-lifecycle-user")
	seedRolesTestUser(t, s, userID, "rb-lifecycle-user@test.local")

	role := createRoleViaAPI(t, srv, createRoleDefinitionRequest{
		Name:        "lifecycle-test-role",
		ScopeType:   "system",
		Permissions: []string{"agent.read"},
	})

	notBefore := time.Now().Add(1 * time.Hour).Truncate(time.Second).UTC()
	expiresAt := time.Now().Add(24 * time.Hour).Truncate(time.Second).UTC()

	// Create binding with lifecycle fields.
	binding := createBindingViaAPI(t, srv, createRoleBindingRequest{
		RoleDefinitionID: role.ID,
		PrincipalType:    "user",
		PrincipalID:      userID,
		ScopeType:        "system",
		NotBefore:        &notBefore,
		ExpiresAt:        &expiresAt,
	})

	assert.NotEmpty(t, binding.ID)
	require.NotNil(t, binding.NotBefore, "notBefore should be returned")
	require.NotNil(t, binding.ExpiresAt, "expiresAt should be returned")
	assert.True(t, binding.NotBefore.Equal(notBefore), "notBefore round-trip: want %v got %v", notBefore, *binding.NotBefore)
	assert.True(t, binding.ExpiresAt.Equal(expiresAt), "expiresAt round-trip: want %v got %v", expiresAt, *binding.ExpiresAt)
}

func TestRolesAPI_CreateRoleBinding_ExpiresAtInPast(t *testing.T) {
	srv, _ := testServer(t)

	role := createRoleViaAPI(t, srv, createRoleDefinitionRequest{
		Name:        "past-expiry-role",
		ScopeType:   "system",
		Permissions: []string{"agent.read"},
	})

	past := time.Now().Add(-1 * time.Hour).UTC()
	rec := doRequest(t, srv, http.MethodPost, "/api/v1/admin/role-bindings", createRoleBindingRequest{
		RoleDefinitionID: role.ID,
		PrincipalType:    "user",
		PrincipalID:      DevUserID,
		ScopeType:        "system",
		ExpiresAt:        &past,
	})
	assert.Equal(t, http.StatusBadRequest, rec.Code, "body: %s", rec.Body.String())
	assert.Contains(t, rec.Body.String(), "expiresAt must be in the future")
}

func TestRolesAPI_CreateRoleBinding_ExpiresAtBeforeNotBefore(t *testing.T) {
	srv, _ := testServer(t)

	role := createRoleViaAPI(t, srv, createRoleDefinitionRequest{
		Name:        "bad-window-role",
		ScopeType:   "system",
		Permissions: []string{"agent.read"},
	})

	notBefore := time.Now().Add(24 * time.Hour).UTC()
	expiresAt := time.Now().Add(1 * time.Hour).UTC()
	rec := doRequest(t, srv, http.MethodPost, "/api/v1/admin/role-bindings", createRoleBindingRequest{
		RoleDefinitionID: role.ID,
		PrincipalType:    "user",
		PrincipalID:      DevUserID,
		ScopeType:        "system",
		NotBefore:        &notBefore,
		ExpiresAt:        &expiresAt,
	})
	assert.Equal(t, http.StatusBadRequest, rec.Code, "body: %s", rec.Body.String())
	assert.Contains(t, rec.Body.String(), "expiresAt must be after notBefore")
}

func TestRolesAPI_CreateRoleBinding_WithOnlyNotBefore(t *testing.T) {
	srv, s := testServer(t)

	userID := tid("rb-notbefore-only-user")
	seedRolesTestUser(t, s, userID, "rb-notbefore-only-user@test.local")

	role := createRoleViaAPI(t, srv, createRoleDefinitionRequest{
		Name:        "notbefore-only-role",
		ScopeType:   "system",
		Permissions: []string{"agent.read"},
	})

	notBefore := time.Now().Add(1 * time.Hour).Truncate(time.Second).UTC()
	binding := createBindingViaAPI(t, srv, createRoleBindingRequest{
		RoleDefinitionID: role.ID,
		PrincipalType:    "user",
		PrincipalID:      userID,
		ScopeType:        "system",
		NotBefore:        &notBefore,
	})

	require.NotNil(t, binding.NotBefore, "notBefore should be persisted")
	assert.Nil(t, binding.ExpiresAt, "expiresAt should be nil when not set")
}

func TestRolesAPI_CreateRoleBinding_WithOnlyExpiresAt(t *testing.T) {
	srv, s := testServer(t)

	userID := tid("rb-expiresat-only-user")
	seedRolesTestUser(t, s, userID, "rb-expiresat-only-user@test.local")

	role := createRoleViaAPI(t, srv, createRoleDefinitionRequest{
		Name:        "expiresat-only-role",
		ScopeType:   "system",
		Permissions: []string{"agent.read"},
	})

	expiresAt := time.Now().Add(24 * time.Hour).Truncate(time.Second).UTC()
	binding := createBindingViaAPI(t, srv, createRoleBindingRequest{
		RoleDefinitionID: role.ID,
		PrincipalType:    "user",
		PrincipalID:      userID,
		ScopeType:        "system",
		ExpiresAt:        &expiresAt,
	})

	assert.Nil(t, binding.NotBefore, "notBefore should be nil when not set")
	require.NotNil(t, binding.ExpiresAt, "expiresAt should be persisted")
}

// ---------------------------------------------------------------------------
// D6: Role applicability validation
// ---------------------------------------------------------------------------

func TestRolesAPI_CreateRoleBinding_AgentRoleRejectsUser(t *testing.T) {
	srv, st := testServer(t)

	userID := tid("d6-user-agent-reject")
	seedRolesTestUser(t, st, userID, "d6-user-agent-reject@test.local")

	// Look up the agent-role-baseline built-in role.
	rd, err := st.GetRoleDefinitionByName(t.Context(), store.AgentRoleDefBaseline, store.RoleScopeSystem)
	require.NoError(t, err)

	// Attempt to bind an agent-only role to a user — must be rejected.
	rec := doRequest(t, srv, http.MethodPost, "/api/v1/admin/role-bindings", createRoleBindingRequest{
		RoleDefinitionID: rd.ID,
		PrincipalType:    "user",
		PrincipalID:      userID,
		ScopeType:        "system",
	})
	assert.Equal(t, http.StatusBadRequest, rec.Code, "body: %s", rec.Body.String())
	assert.Contains(t, rec.Body.String(), "not applicable")
}

func TestRolesAPI_CreateRoleBinding_UserRoleRejectsAgent(t *testing.T) {
	srv, st := testServer(t)

	agentID := tid("d6-agent-user-reject")
	projectID := tid("d6-agent-user-project")
	seedRolesTestAgent(t, st, agentID, projectID)

	// Look up hub-admin — a user-only role.
	rd, err := st.GetRoleDefinitionByName(t.Context(), store.SystemRoleHubAdmin, store.RoleScopeSystem)
	require.NoError(t, err)

	// Attempt to bind a user-only role to an agent — must be rejected.
	rec := doRequest(t, srv, http.MethodPost, "/api/v1/admin/role-bindings", createRoleBindingRequest{
		RoleDefinitionID: rd.ID,
		PrincipalType:    "agent",
		PrincipalID:      agentID,
		ScopeType:        "system",
	})
	assert.Equal(t, http.StatusBadRequest, rec.Code, "body: %s", rec.Body.String())
	assert.Contains(t, rec.Body.String(), "not applicable")
}

func TestRolesAPI_CreateRoleBinding_AgentRoleAcceptsAgent(t *testing.T) {
	srv, st := testServer(t)

	agentID := tid("d6-agent-ok")
	projectID := tid("d6-agent-ok-project")
	seedRolesTestAgent(t, st, agentID, projectID)

	// Look up agent-role-baseline.
	rd, err := st.GetRoleDefinitionByName(t.Context(), store.AgentRoleDefBaseline, store.RoleScopeSystem)
	require.NoError(t, err)

	// Agent binding with an agent role — should succeed.
	binding := createBindingViaAPI(t, srv, createRoleBindingRequest{
		RoleDefinitionID: rd.ID,
		PrincipalType:    "agent",
		PrincipalID:      agentID,
		ScopeType:        "system",
	})
	assert.Equal(t, "agent", binding.PrincipalType)
	assert.Equal(t, agentID, binding.PrincipalID)
}

func TestRolesAPI_CreateRoleBinding_UserGroupRoleAcceptsGroup(t *testing.T) {
	srv, st := testServer(t)
	ctx := t.Context()

	// Create a group so the existence check passes.
	require.NoError(t, st.CreateGroup(ctx, &store.Group{
		ID: tid("d6-group-ok"), Slug: "d6-group-ok", Name: "D6 Group OK",
	}))

	// hub-member allows user and group.
	rd, err := st.GetRoleDefinitionByName(ctx, store.SystemRoleHubMember, store.RoleScopeSystem)
	require.NoError(t, err)

	binding := createBindingViaAPI(t, srv, createRoleBindingRequest{
		RoleDefinitionID: rd.ID,
		PrincipalType:    "group",
		PrincipalID:      tid("d6-group-ok"),
		ScopeType:        "system",
	})
	assert.Equal(t, "group", binding.PrincipalType)
	assert.Equal(t, tid("d6-group-ok"), binding.PrincipalID)
}

func TestRolesAPI_CreateRoleBinding_CustomRoleNoApplicabilityCheck(t *testing.T) {
	srv, s := testServer(t)

	agentID := tid("d6-custom-agent")
	projectID := tid("d6-custom-project")
	seedRolesTestAgent(t, s, agentID, projectID)

	// Custom roles have no applicability entry — should be unrestricted.
	role := createRoleViaAPI(t, srv, createRoleDefinitionRequest{
		Name:        "d6-custom-role",
		Description: "Custom role for D6 test",
		ScopeType:   "system",
		Permissions: []string{"agent.read"},
	})

	// Binding a custom role to an agent should succeed (no restriction).
	binding := createBindingViaAPI(t, srv, createRoleBindingRequest{
		RoleDefinitionID: role.ID,
		PrincipalType:    "agent",
		PrincipalID:      agentID,
		ScopeType:        "system",
	})
	assert.Equal(t, "agent", binding.PrincipalType)

	// Verify custom role returns full applicableTo set (R2 contract: no nil ambiguity).
	rec := doRequest(t, srv, http.MethodGet, "/api/v1/admin/roles/"+role.ID, nil)
	require.Equal(t, http.StatusOK, rec.Code)
	var def store.RoleDefinition
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &def))
	assert.Equal(t, []string{"user", "agent", "group"}, def.ApplicableTo,
		"custom roles should return all principal types (no nil ambiguity)")
}

func TestRolesAPI_ListRoleDefinitions_IncludesApplicableTo(t *testing.T) {
	srv, _ := testServer(t)

	rec := doRequest(t, srv, http.MethodGet, "/api/v1/admin/roles", nil)
	assert.Equal(t, http.StatusOK, rec.Code)

	var resp listRoleDefinitionsResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))

	// Find the agent-role-baseline definition and verify applicableTo.
	var found bool
	for _, def := range resp.Items {
		if def.Name == store.AgentRoleDefBaseline {
			found = true
			assert.Equal(t, []string{"agent"}, def.ApplicableTo,
				"agent-role-baseline should only be applicable to agents")
			break
		}
	}
	assert.True(t, found, "agent-role-baseline should be in the list")
}

func TestRolesAPI_GetRoleDefinition_IncludesApplicableTo(t *testing.T) {
	srv, st := testServer(t)

	rd, err := st.GetRoleDefinitionByName(t.Context(), store.AgentRoleDefBaseline, store.RoleScopeSystem)
	require.NoError(t, err)

	rec := doRequest(t, srv, http.MethodGet, "/api/v1/admin/roles/"+rd.ID, nil)
	assert.Equal(t, http.StatusOK, rec.Code)

	var def store.RoleDefinition
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &def))
	assert.Equal(t, []string{"agent"}, def.ApplicableTo,
		"single role definition response should include applicableTo")
}

// ---------------------------------------------------------------------------
// Server-side binding filters, roleName enrichment, source/provenance
// ---------------------------------------------------------------------------

func TestRolesAPI_ListRoleBindings_FilterByRoleDefinitionId(t *testing.T) {
	srv, s := testServer(t)

	userID := tid("filter-rd-user")
	seedRolesTestUser(t, s, userID, "filter-rd@test.local")

	roleA := createRoleViaAPI(t, srv, createRoleDefinitionRequest{
		Name: "filter-role-a", ScopeType: "system", Permissions: []string{"agent.read"},
	})
	roleB := createRoleViaAPI(t, srv, createRoleDefinitionRequest{
		Name: "filter-role-b", ScopeType: "system", Permissions: []string{"agent.read"},
	})
	createBindingViaAPI(t, srv, createRoleBindingRequest{
		RoleDefinitionID: roleA.ID, PrincipalType: "user", PrincipalID: userID, ScopeType: "system",
	})
	createBindingViaAPI(t, srv, createRoleBindingRequest{
		RoleDefinitionID: roleB.ID, PrincipalType: "user", PrincipalID: userID, ScopeType: "system",
	})

	// Filter by roleA — should include only roleA bindings.
	rec := doRequest(t, srv, http.MethodGet, "/api/v1/admin/role-bindings?roleDefinitionId="+roleA.ID, nil)
	require.Equal(t, http.StatusOK, rec.Code)

	var resp listRoleBindingsResponse
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
	assert.Equal(t, 1, resp.TotalCount)
	require.Len(t, resp.Items, 1)
	assert.Equal(t, roleA.ID, resp.Items[0].RoleDefinitionID)
}

func TestRolesAPI_ListRoleBindings_FilterByPrincipal(t *testing.T) {
	srv, s := testServer(t)

	userA := tid("filter-pa-user-a")
	userB := tid("filter-pa-user-b")
	seedRolesTestUser(t, s, userA, "filter-pa-a@test.local")
	seedRolesTestUser(t, s, userB, "filter-pa-b@test.local")

	role := createRoleViaAPI(t, srv, createRoleDefinitionRequest{
		Name: "filter-principal-role", ScopeType: "system", Permissions: []string{"agent.read"},
	})
	createBindingViaAPI(t, srv, createRoleBindingRequest{
		RoleDefinitionID: role.ID, PrincipalType: "user", PrincipalID: userA, ScopeType: "system",
	})
	createBindingViaAPI(t, srv, createRoleBindingRequest{
		RoleDefinitionID: role.ID, PrincipalType: "user", PrincipalID: userB, ScopeType: "system",
	})

	// Filter by userA
	rec := doRequest(t, srv, http.MethodGet, "/api/v1/admin/role-bindings?principalType=user&principalId="+userA, nil)
	require.Equal(t, http.StatusOK, rec.Code)

	var resp listRoleBindingsResponse
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
	for _, item := range resp.Items {
		assert.Equal(t, userA, item.PrincipalID, "filtered result should only contain userA bindings")
	}
}

func TestRolesAPI_ListRoleBindings_FilterByScopeType(t *testing.T) {
	srv, _ := testServer(t)

	// Seeded roles create system-scoped bindings. Filter to system only.
	rec := doRequest(t, srv, http.MethodGet, "/api/v1/admin/role-bindings?scopeType=system", nil)
	require.Equal(t, http.StatusOK, rec.Code)

	var resp listRoleBindingsResponse
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
	for _, item := range resp.Items {
		assert.Equal(t, "system", item.ScopeType)
	}
}

func TestRolesAPI_ListRoleBindings_InvalidUUID_Returns400(t *testing.T) {
	srv, _ := testServer(t)

	rec := doRequest(t, srv, http.MethodGet, "/api/v1/admin/role-bindings?roleDefinitionId=not-a-uuid", nil)
	assert.Equal(t, http.StatusBadRequest, rec.Code, "body: %s", rec.Body.String())
	assert.Contains(t, rec.Body.String(), "invalid roleDefinitionId")
}

func TestRolesAPI_ListRoleBindings_UnknownFilterParam_Returns400(t *testing.T) {
	srv, _ := testServer(t)

	rec := doRequest(t, srv, http.MethodGet, "/api/v1/admin/role-bindings?bogusParam=foo", nil)
	assert.Equal(t, http.StatusBadRequest, rec.Code, "body: %s", rec.Body.String())
	assert.Contains(t, rec.Body.String(), "unknown query parameter")
}

func TestRolesAPI_ListRoleBindings_IncludeGroupDerived_Accepted(t *testing.T) {
	srv, _ := testServer(t)

	// R-3: includeGroupDerived is a known param — must not return 400.
	rec := doRequest(t, srv, http.MethodGet, "/api/v1/admin/role-bindings?includeGroupDerived=true", nil)
	assert.Equal(t, http.StatusOK, rec.Code, "includeGroupDerived should be accepted")
}

// TestRolesAPI_ListRoleBindings_IncludeGroupDerived_ExpandsGroups verifies
// that includeGroupDerived=true truthfully expands group-derived bindings
// when filtering for a specific user principal. The response should include
// the group's direct bindings with source = group slug.
func TestRolesAPI_ListRoleBindings_IncludeGroupDerived_ExpandsGroups(t *testing.T) {
	srv, s := testServer(t)
	ctx := t.Context()

	// Create a user and a group, then add the user as a group member.
	userID := tid("igd-user")
	groupID := tid("igd-group")
	seedRolesTestUser(t, s, userID, "igd-user@test.local")
	require.NoError(t, s.CreateGroup(ctx, &store.Group{
		ID: groupID, Slug: "igd-test-group", Name: "IGD Test Group",
	}))
	require.NoError(t, s.AddGroupMember(ctx, &store.GroupMember{
		GroupID:    groupID,
		MemberType: store.GroupMemberTypeUser,
		MemberID:   userID,
		Role:       "member",
	}))

	// Create a role and assign it directly to the group.
	role := createRoleViaAPI(t, srv, createRoleDefinitionRequest{
		Name:        "igd-group-role",
		ScopeType:   "system",
		Permissions: []string{"agent.read"},
	})
	createBindingViaAPI(t, srv, createRoleBindingRequest{
		RoleDefinitionID: role.ID,
		PrincipalType:    "group",
		PrincipalID:      groupID,
		ScopeType:        "system",
	})

	// Also create a direct binding for the user.
	directRole := createRoleViaAPI(t, srv, createRoleDefinitionRequest{
		Name:        "igd-direct-role",
		ScopeType:   "system",
		Permissions: []string{"project.read"},
	})
	createBindingViaAPI(t, srv, createRoleBindingRequest{
		RoleDefinitionID: directRole.ID,
		PrincipalType:    "user",
		PrincipalID:      userID,
		ScopeType:        "system",
	})

	// Query with includeGroupDerived=true for the specific user.
	rec := doRequest(t, srv, http.MethodGet,
		"/api/v1/admin/role-bindings?principalType=user&principalId="+userID+"&includeGroupDerived=true", nil)
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())

	var resp listRoleBindingsResponse
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))

	// Should include the direct user binding + the group-derived binding.
	var foundDirect, foundGroupDerived bool
	for _, item := range resp.Items {
		if item.RoleDefinitionID == directRole.ID && item.PrincipalType == "user" {
			foundDirect = true
			assert.Equal(t, "direct", item.Source, "direct bindings should have source 'direct'")
		}
		if item.RoleDefinitionID == role.ID && item.PrincipalType == "group" {
			foundGroupDerived = true
			assert.Equal(t, "group", item.Source,
				"group-derived binding should have source 'group'")
			assert.Equal(t, "igd-test-group", item.SourceGroupSlug,
				"group-derived binding should carry source group slug")
		}
	}
	assert.True(t, foundDirect, "direct user binding should be in response")
	assert.True(t, foundGroupDerived, "group-derived binding should be included when includeGroupDerived=true")

	// Without includeGroupDerived, the group binding should NOT appear.
	rec2 := doRequest(t, srv, http.MethodGet,
		"/api/v1/admin/role-bindings?principalType=user&principalId="+userID, nil)
	require.Equal(t, http.StatusOK, rec2.Code)

	var resp2 listRoleBindingsResponse
	require.NoError(t, json.NewDecoder(rec2.Body).Decode(&resp2))

	for _, item := range resp2.Items {
		if item.RoleDefinitionID == role.ID && item.PrincipalType == "group" {
			t.Fatal("group-derived binding should NOT appear without includeGroupDerived=true")
		}
	}
}

// TestRolesAPI_ListRoleBindings_ScopeOnlyGroupDerived_ExpandsGroups tests
// the frontend's actual query pattern: scopeType=project&scopeId=X&includeGroupDerived=true
// without principalType or principalId. This is the R2-REQ-1 fix for the
// project-members-editor page.
func TestRolesAPI_ListRoleBindings_ScopeOnlyGroupDerived_ExpandsGroups(t *testing.T) {
	srv, s := testServer(t)
	ctx := t.Context()

	projectID := tid("scope-exp-proj")
	require.NoError(t, s.CreateProject(ctx, &store.Project{
		ID:   projectID,
		Name: "Scope Expand Project",
		Slug: "scope-expand-proj",
	}))

	// Create a user and a group, then add the user as a group member.
	userID := tid("scope-exp-user")
	groupID := tid("scope-exp-group")
	seedRolesTestUser(t, s, userID, "scope-exp-user@test.local")
	require.NoError(t, s.CreateGroup(ctx, &store.Group{
		ID: groupID, Slug: "scope-exp-grp", Name: "Scope Expand Group",
	}))
	require.NoError(t, s.AddGroupMember(ctx, &store.GroupMember{
		GroupID:    groupID,
		MemberType: store.GroupMemberTypeUser,
		MemberID:   userID,
		Role:       "member",
	}))

	// Create a project-scoped role and assign it to the group.
	role := createRoleViaAPI(t, srv, createRoleDefinitionRequest{
		Name:        "scope-exp-custom",
		ScopeType:   "project",
		Permissions: []string{"agent.read"},
	})
	_, err := s.CreateRoleBinding(ctx, &store.RoleBinding{
		RoleDefinitionID: role.ID,
		PrincipalType:    "group",
		PrincipalID:      groupID,
		ScopeType:        store.RoleScopeProject,
		ScopeID:          projectID,
		CreatedBy:        "test",
	})
	require.NoError(t, err)

	// Also create a direct user binding for the project.
	directRole := createRoleViaAPI(t, srv, createRoleDefinitionRequest{
		Name:        "scope-exp-direct",
		ScopeType:   "project",
		Permissions: []string{"project.read"},
	})
	_, err = s.CreateRoleBinding(ctx, &store.RoleBinding{
		RoleDefinitionID: directRole.ID,
		PrincipalType:    "user",
		PrincipalID:      userID,
		ScopeType:        store.RoleScopeProject,
		ScopeID:          projectID,
		CreatedBy:        "test",
	})
	require.NoError(t, err)

	// Query with scopeType=project&scopeId=X&includeGroupDerived=true
	// (the exact frontend pattern from project-members-editor).
	rec := doRequest(t, srv, http.MethodGet,
		"/api/v1/admin/role-bindings?scopeType=project&scopeId="+projectID+"&includeGroupDerived=true", nil)
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())

	var resp listRoleBindingsResponse
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))

	// Should include the direct user binding + expanded group-derived binding.
	var foundDirect, foundDerived bool
	for _, item := range resp.Items {
		if item.RoleDefinitionID == directRole.ID && item.PrincipalType == "user" && item.PrincipalID == userID {
			foundDirect = true
			assert.Equal(t, "direct", item.Source, "direct binding should have source 'direct'")
		}
		// The expanded binding should show principalType=user, principalId=userID
		// with source=group and group provenance fields.
		if item.RoleDefinitionID == role.ID && item.PrincipalType == "user" && item.PrincipalID == userID {
			foundDerived = true
			assert.Equal(t, "group", item.Source, "derived binding should have source 'group'")
			assert.Equal(t, "scope-exp-grp", item.SourceGroupSlug,
				"derived binding should carry source group slug")
			assert.Equal(t, groupID, item.SourceGroupID,
				"derived binding should carry source group ID")
		}
	}
	assert.True(t, foundDirect, "direct user binding should be in scope-filtered response")
	assert.True(t, foundDerived, "group-derived expanded binding should appear for scope-only includeGroupDerived query")

	// Without includeGroupDerived, only the direct and the group bindings
	// themselves (not expanded) should appear.
	rec2 := doRequest(t, srv, http.MethodGet,
		"/api/v1/admin/role-bindings?scopeType=project&scopeId="+projectID, nil)
	require.Equal(t, http.StatusOK, rec2.Code)

	var resp2 listRoleBindingsResponse
	require.NoError(t, json.NewDecoder(rec2.Body).Decode(&resp2))

	for _, item := range resp2.Items {
		if item.RoleDefinitionID == role.ID && item.PrincipalType == "user" && item.PrincipalID == userID {
			t.Fatal("group-derived expanded binding should NOT appear without includeGroupDerived=true")
		}
	}
}

func TestRolesAPI_ListRoleBindings_PrincipalIdWithoutType_Returns400(t *testing.T) {
	srv, _ := testServer(t)

	rec := doRequest(t, srv, http.MethodGet, "/api/v1/admin/role-bindings?principalId=some-id", nil)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "principalType is required")
}

func TestRolesAPI_ListRoleBindings_InvalidPrincipalType_Returns400(t *testing.T) {
	srv, _ := testServer(t)

	rec := doRequest(t, srv, http.MethodGet, "/api/v1/admin/role-bindings?principalType=robot", nil)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "invalid principalType")
}

func TestRolesAPI_ListRoleBindings_InvalidScopeType_Returns400(t *testing.T) {
	srv, _ := testServer(t)

	rec := doRequest(t, srv, http.MethodGet, "/api/v1/admin/role-bindings?scopeType=universe", nil)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "invalid scopeType")
}

func TestRolesAPI_ListRoleBindings_RoleNameEnrichment(t *testing.T) {
	srv, s := testServer(t)

	userID := tid("enrich-rn-user")
	seedRolesTestUser(t, s, userID, "enrich-rn@test.local")

	role := createRoleViaAPI(t, srv, createRoleDefinitionRequest{
		Name: "enrichment-test-role", ScopeType: "system", Permissions: []string{"agent.read"},
	})
	createBindingViaAPI(t, srv, createRoleBindingRequest{
		RoleDefinitionID: role.ID, PrincipalType: "user", PrincipalID: userID, ScopeType: "system",
	})

	// List with filter to isolate our binding.
	rec := doRequest(t, srv, http.MethodGet, "/api/v1/admin/role-bindings?roleDefinitionId="+role.ID, nil)
	require.Equal(t, http.StatusOK, rec.Code)

	var resp listRoleBindingsResponse
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
	require.Len(t, resp.Items, 1)
	assert.Equal(t, "enrichment-test-role", resp.Items[0].RoleName,
		"response should include human-readable roleName")
}

func TestRolesAPI_ListRoleBindings_SourceFieldDirect(t *testing.T) {
	srv, s := testServer(t)

	userID := tid("source-direct-user")
	seedRolesTestUser(t, s, userID, "source-direct@test.local")

	role := createRoleViaAPI(t, srv, createRoleDefinitionRequest{
		Name: "source-test-role", ScopeType: "system", Permissions: []string{"agent.read"},
	})
	createBindingViaAPI(t, srv, createRoleBindingRequest{
		RoleDefinitionID: role.ID, PrincipalType: "user", PrincipalID: userID, ScopeType: "system",
	})

	rec := doRequest(t, srv, http.MethodGet, "/api/v1/admin/role-bindings?roleDefinitionId="+role.ID, nil)
	require.Equal(t, http.StatusOK, rec.Code)

	var resp listRoleBindingsResponse
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
	require.Len(t, resp.Items, 1)
	// R2-REQ-2: source must be "direct" for non-group-derived bindings.
	assert.Equal(t, "direct", resp.Items[0].Source,
		"direct bindings must have source='direct'")
}

func TestRolesAPI_ListRoleBindings_TotalCountReflectsFilter(t *testing.T) {
	srv, s := testServer(t)

	userID := tid("filter-count-user")
	seedRolesTestUser(t, s, userID, "filter-count@test.local")

	roleA := createRoleViaAPI(t, srv, createRoleDefinitionRequest{
		Name: "count-role-a", ScopeType: "system", Permissions: []string{"agent.read"},
	})
	roleB := createRoleViaAPI(t, srv, createRoleDefinitionRequest{
		Name: "count-role-b", ScopeType: "system", Permissions: []string{"agent.read"},
	})
	createBindingViaAPI(t, srv, createRoleBindingRequest{
		RoleDefinitionID: roleA.ID, PrincipalType: "user", PrincipalID: userID, ScopeType: "system",
	})
	createBindingViaAPI(t, srv, createRoleBindingRequest{
		RoleDefinitionID: roleB.ID, PrincipalType: "user", PrincipalID: userID, ScopeType: "system",
	})

	// Unfiltered count should be >= 2
	recAll := doRequest(t, srv, http.MethodGet, "/api/v1/admin/role-bindings", nil)
	require.Equal(t, http.StatusOK, recAll.Code)
	var respAll listRoleBindingsResponse
	require.NoError(t, json.NewDecoder(recAll.Body).Decode(&respAll))

	// Filtered count for roleA should be exactly 1
	recA := doRequest(t, srv, http.MethodGet, "/api/v1/admin/role-bindings?roleDefinitionId="+roleA.ID, nil)
	require.Equal(t, http.StatusOK, recA.Code)
	var respA listRoleBindingsResponse
	require.NoError(t, json.NewDecoder(recA.Body).Decode(&respA))

	assert.Equal(t, 1, respA.TotalCount, "filtered totalCount should reflect only matched bindings")
	assert.Greater(t, respAll.TotalCount, respA.TotalCount, "unfiltered count should be higher than filtered")
}

func TestRolesAPI_ListBindingsForUser_IncludesRoleName(t *testing.T) {
	srv, s := testServer(t)

	userID := tid("user-bind-rn")
	seedRolesTestUser(t, s, userID, "user-bind-rn@test.local")

	role := createRoleViaAPI(t, srv, createRoleDefinitionRequest{
		Name: "user-bindings-rn-role", ScopeType: "system", Permissions: []string{"agent.read"},
	})
	createBindingViaAPI(t, srv, createRoleBindingRequest{
		RoleDefinitionID: role.ID, PrincipalType: "user", PrincipalID: userID, ScopeType: "system",
	})

	rec := doRequest(t, srv, http.MethodGet, "/api/v1/admin/role-bindings/user/"+userID, nil)
	require.Equal(t, http.StatusOK, rec.Code)

	var resp listRoleBindingsResponse
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))

	var found bool
	for _, item := range resp.Items {
		if item.RoleDefinitionID == role.ID {
			found = true
			assert.Equal(t, "user-bindings-rn-role", item.RoleName)
			assert.Equal(t, "direct", item.Source, "direct bindings should have source='direct'")
		}
	}
	assert.True(t, found, "binding with our role should be in user bindings")
}

// TestEnrichRoleDefinitionsApplicability_NeverNil verifies invariant O-2:
// enrichRoleDefinitionsApplicability always sets ApplicableTo to a non-nil,
// non-empty slice — preventing nil ambiguity that would cause frontends to
// fail-closed on missing values.
func TestEnrichRoleDefinitionsApplicability_NeverNil(t *testing.T) {
	// All built-in roles must have explicit ApplicableTo after enrichment.
	builtInRoles := BuiltInRoles()
	defs := make([]*store.RoleDefinition, len(builtInRoles))
	for i, br := range builtInRoles {
		defs[i] = &store.RoleDefinition{
			Name:      br.Name,
			ScopeType: br.ScopeType,
			// ApplicableTo is nil (as read from DB — not persisted in Ent schema).
		}
	}
	enrichRoleDefinitionsApplicability(defs)
	for _, def := range defs {
		assert.NotNil(t, def.ApplicableTo,
			"built-in role %q must have non-nil ApplicableTo after enrichment", def.Name)
		assert.NotEmpty(t, def.ApplicableTo,
			"built-in role %q must have non-empty ApplicableTo after enrichment", def.Name)
	}

	// Custom roles (unknown names) must also get non-nil ApplicableTo.
	customDefs := []*store.RoleDefinition{
		{Name: "custom-role-1", ScopeType: "project"},
		{Name: "custom-role-2", ScopeType: "system"},
	}
	enrichRoleDefinitionsApplicability(customDefs)
	for _, def := range customDefs {
		assert.NotNil(t, def.ApplicableTo,
			"custom role %q must have non-nil ApplicableTo after enrichment", def.Name)
		assert.NotEmpty(t, def.ApplicableTo,
			"custom role %q must have non-empty ApplicableTo after enrichment", def.Name)
		assert.Equal(t, allPrincipalTypes, def.ApplicableTo,
			"custom role %q should get allPrincipalTypes", def.Name)
	}
}

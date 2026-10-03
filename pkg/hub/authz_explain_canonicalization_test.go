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

	"github.com/GoogleCloudPlatform/scion/pkg/hub/permissions"
	"github.com/GoogleCloudPlatform/scion/pkg/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// =============================================================================
// Unit tests: resolveResourcePermission (production enforcement)
// =============================================================================

// TestResolveResourcePermission_Contract pins the production resolver used
// when a request carries no explicit permission: an unambiguous registry
// (Resource, Action) pair resolves to its ID, and every other pair returns
// errUnresolvablePermission.
func TestResolveResourcePermission_Contract(t *testing.T) {
	tests := []struct {
		name     string
		resource string
		action   Action
		wantID   string
	}{
		{"canonical user.read", "user", ActionRead, "user.read"},
		{"canonical user.list", "user", ActionList, "user.list"},
		{"canonical agent.create", "agent", ActionCreate, "agent.create"},
		{"canonical project.read", "project", ActionRead, "project.read"},
		{"agent+manage has no permission", "agent", ActionManage, ""},

		// Non-canonical and unknown pairs are not resolvable.
		{"hub+user.read", "hub", "user.read", ""},
		{"hub.user+read", "hub.user", ActionRead, ""},
		{"unknown+unknown", "widget", "frobnicate", ""},
		// Ambiguous pairs are not resolvable.
		{"ambiguous hub+read", "hub", ActionRead, ""},
		{"ambiguous hub+update", "hub", ActionUpdate, ""},
		{"ambiguous hub+execute", "hub", "execute", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := resolveResourcePermission(tt.resource, tt.action)
			if tt.wantID == "" {
				require.ErrorIs(t, err, errUnresolvablePermission)
				assert.Empty(t, got)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantID, got)
		})
	}
}

// TestResolveResourcePermission_AllRegistryPermissions verifies that every
// registry permission with a unique (Resource, Action) resolves to its own
// ID, and that every shared pair is reported as ambiguous.
func TestResolveResourcePermission_AllRegistryPermissions(t *testing.T) {
	count := make(map[string]int)
	for _, p := range permissions.Registry {
		count[p.Resource+"\x00"+p.Action]++
	}
	for _, p := range permissions.Registry {
		t.Run(p.ID, func(t *testing.T) {
			got, err := resolveResourcePermission(p.Resource, Action(p.Action))
			if count[p.Resource+"\x00"+p.Action] > 1 {
				require.ErrorIs(t, err, errUnresolvablePermission)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, p.ID, got)
		})
	}
}

// TestIsKnownPermission verifies the registry lookup helper.
func TestIsKnownPermission(t *testing.T) {
	assert.True(t, isKnownPermission("user.read"), "user.read is canonical")
	assert.True(t, isKnownPermission("agent.create"), "agent.create is canonical")
	assert.True(t, isKnownPermission("hub.settings.read"), "hub.settings.read is canonical")
	assert.False(t, isKnownPermission("hub.user.read"), "hub.user.read is NOT canonical")
	assert.False(t, isKnownPermission("widget.frobnicate"), "widget.frobnicate is NOT canonical")
	assert.False(t, isKnownPermission(""), "empty string is NOT canonical")
}

// =============================================================================
// Integration test: group-derived bindings
// =============================================================================

// TestExplainAPI_GroupDerivedBinding verifies that explain correctly reports
// allowed=true for permissions granted via group membership (the hub-members
// group → hub-member role path).
func TestExplainAPI_GroupDerivedBinding(t *testing.T) {
	srv, s := testServer(t)
	ctx := context.Background()

	memberID := tid("explain-group-derived")
	require.NoError(t, s.CreateUser(ctx, &store.User{
		ID:          memberID,
		Email:       "group-derived@test.com",
		DisplayName: "Group Derived",
		Role:        "member",
		Status:      "active",
	}))
	ensureHubMembership(ctx, s, memberID)
	identity := NewAuthenticatedUser(memberID, "group-derived@test.com", "Group Derived", "member", "api")

	// Check a permission that hub-member gets via group membership.
	body := map[string]interface{}{
		"operationId": "user.read",
		"resource": map[string]interface{}{
			"type": "user",
			"id":   "test-user",
		},
		"action": "read",
	}
	bodyBytes, _ := json.Marshal(body)
	req := newRequestWithIdentity(t, http.MethodPost, "/api/v1/authz/explain", bodyBytes, identity)
	rec := httptest.NewRecorder()
	srv.handleAuthzExplain(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)

	var resp explainResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))

	assert.True(t, resp.Allowed,
		"hub-member with group-derived binding should be allowed user.read")
	require.NotNil(t, resp.Provenance, "provenance should be populated")
	assert.Equal(t, "user.read", resp.Provenance.Permission)

	// Provenance should include grant details showing the group derivation.
	assert.NotEmpty(t, resp.Provenance.Grants,
		"provenance should include at least one active grant")

	// Verify the grant references hub-member role.
	foundHubMember := false
	for _, g := range resp.Provenance.Grants {
		if g.RoleName == "hub-member" {
			foundHubMember = true
			break
		}
	}
	assert.True(t, foundHubMember,
		"provenance should include a grant from the hub-member role")
}

// =============================================================================
// Integration test: project-scoped explain
// =============================================================================

// TestExplainAPI_ProjectScopedPermission verifies that explain correctly
// evaluates project-scoped permissions with canonical resource type + action.
func TestExplainAPI_ProjectScopedPermission(t *testing.T) {
	srv, s := testServer(t)
	ctx := context.Background()

	memberID := tid("explain-project-scope")
	require.NoError(t, s.CreateUser(ctx, &store.User{
		ID:          memberID,
		Email:       "project-scope@test.com",
		DisplayName: "Project Scope",
		Role:        "member",
		Status:      "active",
	}))
	ensureHubMembership(ctx, s, memberID)

	project := &store.Project{
		ID:        tid("explain-project-scope-proj"),
		Name:      "Scope Test",
		Slug:      "scope-test",
		CreatedBy: DevUserID,
		OwnerID:   DevUserID,
	}
	require.NoError(t, s.CreateProject(ctx, project))

	// Give the member a project-member role binding.
	rd, err := s.GetRoleDefinitionByName(ctx, "project-member", string(store.RoleScopeProject))
	require.NoError(t, err, "project-member role should exist")
	_, err = s.CreateRoleBinding(ctx, &store.RoleBinding{
		RoleDefinitionID: rd.ID,
		PrincipalType:    store.RoleBindingPrincipalUser,
		PrincipalID:      memberID,
		ScopeType:        store.RoleScopeProject,
		ScopeID:          project.ID,
		CreatedBy:        "test",
	})
	require.NoError(t, err)

	identity := NewAuthenticatedUser(memberID, "project-scope@test.com", "Project Scope", "member", "api")

	t.Run("project-scoped agent.read allowed", func(t *testing.T) {
		body := map[string]interface{}{
			"operationId": "agent.read",
			"resource": map[string]interface{}{
				"type":      "agent",
				"id":        tid("test-agent"),
				"projectId": project.ID,
			},
			"action": "read",
		}
		bodyBytes, _ := json.Marshal(body)
		req := newRequestWithIdentity(t, http.MethodPost, "/api/v1/authz/explain", bodyBytes, identity)
		rec := httptest.NewRecorder()
		srv.handleAuthzExplain(rec, req)

		require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())

		var resp explainResponse
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
		assert.True(t, resp.Allowed, "project-member should be allowed agent.read in their project")
	})

	t.Run("project-scoped agent.read denied for wrong project", func(t *testing.T) {
		body := map[string]interface{}{
			"operationId": "agent.read",
			"resource": map[string]interface{}{
				"type":      "agent",
				"id":        tid("test-agent"),
				"projectId": tid("other-project"),
			},
			"action": "read",
		}
		bodyBytes, _ := json.Marshal(body)
		req := newRequestWithIdentity(t, http.MethodPost, "/api/v1/authz/explain", bodyBytes, identity)
		rec := httptest.NewRecorder()
		srv.handleAuthzExplain(rec, req)

		require.Equal(t, http.StatusOK, rec.Code)

		var resp explainResponse
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
		assert.False(t, resp.Allowed, "project-member should be denied agent.read in a different project")
	})
}

// =============================================================================
// Integration test: redaction with an explicit operation
// =============================================================================

// TestExplainAPI_RedactionWithCanonicalization retains the historical test
// name while pinning redaction under the operation-centric contract.
func TestExplainAPI_RedactionWithCanonicalization(t *testing.T) {
	srv, s := testServer(t)
	ctx := context.Background()

	targetID := tid("explain-redact-canon")
	require.NoError(t, s.CreateUser(ctx, &store.User{
		ID:          targetID,
		Email:       "redact-canon@test.com",
		DisplayName: "Secret Name",
		Role:        "member",
		Status:      "active",
	}))
	ensureHubMembership(ctx, s, targetID)

	// Admin (dev user) explains for a hub-member using non-canonical input.
	body := map[string]interface{}{
		"operationId": "user.read",
		"resource": map[string]interface{}{
			"type": "user",
			"id":   "test",
		},
		"action":        "read",
		"principalId":   targetID,
		"principalKind": "user",
	}
	rec := doRequest(t, srv, http.MethodPost, "/api/v1/authz/explain", body)
	require.Equal(t, http.StatusOK, rec.Code, "status: %s", rec.Body.String())

	var resp explainResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))

	// Should still be allowed (canonicalization fixes the permission).
	assert.True(t, resp.Allowed,
		"hub-member should be allowed user.read even with non-canonical input")

	require.NotNil(t, resp.Provenance, "cross-principal explain must include provenance")

	// Redaction should still work: principal IDs should be "[redacted]".
	for _, g := range resp.Provenance.Grants {
		assert.Equal(t, "[redacted]", g.PrincipalID,
			"cross-principal grant principal ID must be redacted even with canonicalization")
	}

	// Display name should not leak.
	assert.NotContains(t, rec.Body.String(), "Secret Name",
		"cross-principal explain must not leak display names")
}

// =============================================================================
// Regression test: operation ownership replaces permission-name inference
// =============================================================================

// TestExplainAPI_HubUserReadRegression is the canonical regression test for the
// authz explain permission-name mismatch bug. Explicit operation ownership
// admits the canonical request and rejects the two former inference forms.
func TestExplainAPI_HubUserReadRegression(t *testing.T) {
	srv, s := testServer(t)
	ctx := context.Background()

	memberID := tid("explain-regression-member")
	require.NoError(t, s.CreateUser(ctx, &store.User{
		ID:          memberID,
		Email:       "regression@test.com",
		DisplayName: "Regression Test",
		Role:        "member",
		Status:      "active",
	}))
	ensureHubMembership(ctx, s, memberID)
	identity := NewAuthenticatedUser(memberID, "regression@test.com", "Regression Test", "member", "api")

	variants := []struct {
		name         string
		resourceType string
		action       string
		wantStatus   int
	}{
		{"canonical", "user", "read", http.StatusOK},
		{"non-canonical hub+user.read", "hub", "user.read", http.StatusBadRequest},
		{"non-canonical hub.user+read", "hub.user", "read", http.StatusBadRequest},
	}

	for _, v := range variants {
		t.Run(v.name, func(t *testing.T) {
			body := map[string]interface{}{
				"operationId": "user.read",
				"resource": map[string]interface{}{
					"type": v.resourceType,
					"id":   "any-user",
				},
				"action": v.action,
			}
			bodyBytes, _ := json.Marshal(body)
			req := newRequestWithIdentity(t, http.MethodPost, "/api/v1/authz/explain", bodyBytes, identity)
			rec := httptest.NewRecorder()
			srv.handleAuthzExplain(rec, req)

			require.Equal(t, v.wantStatus, rec.Code, "body: %s", rec.Body.String())
			if v.wantStatus != http.StatusOK {
				return
			}

			var resp explainResponse
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))

			// All variants must resolve to user.read and be allowed.
			assert.True(t, resp.Allowed,
				"hub-member must be allowed user.read via %s (resource.type=%q action=%q)",
				v.name, v.resourceType, v.action)

			require.NotNil(t, resp.Provenance,
				"provenance must be populated for %s", v.name)
			assert.Equal(t, "user.read", resp.Provenance.Permission,
				"permission must be canonical user.read for %s (resource.type=%q action=%q)",
				v.name, v.resourceType, v.action)
		})
	}
}

// =============================================================================
// Integration test: effective_permissions mode with canonicalization
// =============================================================================

// TestExplainAPI_EffectivePermissionsCanonical verifies that the
// effective_permissions mode returns canonical permission IDs and that
// the hub-member user.read permission appears in the effective set.
func TestExplainAPI_EffectivePermissionsCanonical(t *testing.T) {
	srv, s := testServer(t)
	ctx := context.Background()

	memberID := tid("explain-effperm-canon")
	require.NoError(t, s.CreateUser(ctx, &store.User{
		ID:          memberID,
		Email:       "effperm-canon@test.com",
		DisplayName: "EffPerm Canon",
		Role:        "member",
		Status:      "active",
	}))
	ensureHubMembership(ctx, s, memberID)
	identity := NewAuthenticatedUser(memberID, "effperm-canon@test.com", "EffPerm Canon", "member", "api")

	body := map[string]interface{}{
		"resource": map[string]interface{}{
			"type": "user",
			"id":   "any-user",
		},
		"action": "read",
		"mode":   "effective_permissions",
	}
	bodyBytes, _ := json.Marshal(body)
	req := newRequestWithIdentity(t, http.MethodPost, "/api/v1/authz/explain", bodyBytes, identity)
	rec := httptest.NewRecorder()
	srv.handleAuthzExplain(rec, req)

	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())

	var resp explainResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))

	// Should have effective permissions.
	require.NotEmpty(t, resp.EffectivePermissions,
		"hub-member should have effective permissions")

	// user.read should be in the effective set.
	foundUserRead := false
	for _, pp := range resp.EffectivePermissions {
		if pp.PermissionID == "user.read" {
			foundUserRead = true
			assert.True(t, pp.Granted,
				"user.read should be granted in effective permissions")
			break
		}
	}
	assert.True(t, foundUserRead,
		"user.read must appear in hub-member's effective permissions")

	// No permission should have the non-canonical "hub.user.read" ID.
	for _, pp := range resp.EffectivePermissions {
		assert.NotEqual(t, "hub.user.read", pp.PermissionID,
			"effective permissions must not contain non-canonical hub.user.read")
	}
}

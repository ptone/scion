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

	"github.com/GoogleCloudPlatform/scion/pkg/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setupAgentRoleTest creates a server with a member user and project.
// The user is the project creator and is in the hub-members group.
func setupAgentRoleTest(t *testing.T) (*Server, store.Store, *store.User, *store.Project) {
	t.Helper()
	srv, s := testServer(t)
	ctx := context.Background()

	user := &store.User{
		ID:          tid("user-role-test"),
		Email:       "roletest@test.com",
		DisplayName: "Role Tester",
		Role:        store.UserRoleMember,
		Status:      "active",
		Created:     time.Now(),
	}
	require.NoError(t, s.CreateUser(ctx, user))
	ensureHubMembership(ctx, s, user.ID)

	project := &store.Project{
		ID:        tid("project-role-test"),
		Name:      "role-test-project",
		Slug:      "role-test-project",
		OwnerID:   user.ID,
		CreatedBy: user.ID,
		Created:   time.Now(),
		Updated:   time.Now(),
	}
	require.NoError(t, s.CreateProject(ctx, project))
	srv.createProjectMembersGroupAndPolicy(ctx, project)

	return srv, s, user, project
}

func doAgentRoleRequest(t *testing.T, srv *Server, user *store.User, body interface{}) *httptest.ResponseRecorder {
	t.Helper()
	token, _, _, err := srv.userTokenService.GenerateTokenPair(
		user.ID, user.Email, user.DisplayName, user.Role, ClientTypeWeb,
	)
	require.NoError(t, err)

	bodyBytes, err := json.Marshal(body)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/agents", bytes.NewReader(bodyBytes))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	return rec
}

// getStoredAgentRole retrieves the stored role for a created agent by slug.
// Returns ("", false) if the agent was not persisted (e.g., request failed downstream).
func getStoredAgentRole(t *testing.T, s store.Store, projectID, slug string) (string, bool) {
	t.Helper()
	agent, err := s.GetAgentBySlug(context.Background(), projectID, slug)
	if err != nil {
		return "", false
	}
	if agent.AppliedConfig == nil {
		return "", false
	}
	return agent.AppliedConfig.AgentRole, true
}

func TestCreateAgent_InvalidAgentRole_Returns400(t *testing.T) {
	srv, _, user, project := setupAgentRoleTest(t)

	rec := doAgentRoleRequest(t, srv, user, CreateAgentRequest{
		Name:      "test-invalid-role",
		ProjectID: project.ID,
		AgentRole: "superadmin",
	})

	assert.Equal(t, http.StatusBadRequest, rec.Code,
		"invalid agentRole should return 400; got: %s", rec.Body.String())
	assert.Contains(t, rec.Body.String(), "invalid agentRole")
}

func TestCreateAgent_NoAgentRole_GetsBaseline(t *testing.T) {
	srv, s, user, project := setupAgentRoleTest(t)

	rec := doAgentRoleRequest(t, srv, user, CreateAgentRequest{
		Name:      "test-no-role",
		ProjectID: project.ID,
	})

	// Should not fail with an agentRole validation error.
	if rec.Code == http.StatusBadRequest {
		assert.NotContains(t, rec.Body.String(), "invalid agentRole",
			"empty agentRole should not trigger validation error")
	}

	// If the agent was persisted, verify default role.
	if role, ok := getStoredAgentRole(t, s, project.ID, "test-no-role"); ok {
		assert.Equal(t, "baseline", role,
			"default role should be baseline for member user")
	}
}

func TestCreateAgent_ValidAgentRoles_AcceptedByValidation(t *testing.T) {
	srv, _, user, project := setupAgentRoleTest(t)

	validRoles := []string{"none", "readonly", "baseline", "full"}
	for _, role := range validRoles {
		t.Run("role="+role, func(t *testing.T) {
			rec := doAgentRoleRequest(t, srv, user, CreateAgentRequest{
				Name:      "test-role-" + role,
				ProjectID: project.ID,
				AgentRole: role,
			})
			// Valid role values should not trigger an agentRole validation error.
			if rec.Code == http.StatusBadRequest {
				assert.NotContains(t, rec.Body.String(), "invalid agentRole",
					"valid role %q should not trigger agentRole validation error", role)
			}
		})
	}
}

func TestCreateAgent_RoleReadonly_StoresReadonly(t *testing.T) {
	srv, s, user, project := setupAgentRoleTest(t)

	_ = doAgentRoleRequest(t, srv, user, CreateAgentRequest{
		Name:      "test-readonly",
		ProjectID: project.ID,
		AgentRole: "readonly",
	})

	if role, ok := getStoredAgentRole(t, s, project.ID, "test-readonly"); ok {
		assert.Equal(t, "readonly", role,
			"readonly role should be stored as-is (readonly < member baseline ceiling)")
	}
}

func TestCreateAgent_RoleNone_SetsNoAuth(t *testing.T) {
	srv, s, user, project := setupAgentRoleTest(t)

	_ = doAgentRoleRequest(t, srv, user, CreateAgentRequest{
		Name:      "test-none-role",
		ProjectID: project.ID,
		AgentRole: "none",
	})

	agent, err := s.GetAgentBySlug(context.Background(), project.ID, "test-none-role")
	if err == nil && agent.AppliedConfig != nil {
		assert.Equal(t, "none", agent.AppliedConfig.AgentRole)
		assert.True(t, agent.AppliedConfig.NoAuth,
			"role=none should set NoAuth=true")
	}
}

func TestCreateAgent_RoleFull_CappedByMemberCeiling(t *testing.T) {
	srv, s, user, project := setupAgentRoleTest(t)

	_ = doAgentRoleRequest(t, srv, user, CreateAgentRequest{
		Name:      "test-full-capped",
		ProjectID: project.ID,
		AgentRole: "full",
	})

	if role, ok := getStoredAgentRole(t, s, project.ID, "test-full-capped"); ok {
		// Member user ceiling is baseline, so full should be capped.
		assert.Equal(t, "baseline", role,
			"member user requesting full should be capped to baseline")
	}
}

func TestCreateAgent_AdminGetsFull(t *testing.T) {
	srv, s, _, project := setupAgentRoleTest(t)
	ctx := context.Background()

	admin := &store.User{
		ID:          tid("user-admin-role"),
		Email:       "admin-role@test.com",
		DisplayName: "Admin Role Tester",
		Role:        store.UserRoleAdmin,
		Status:      "active",
		Created:     time.Now(),
	}
	require.NoError(t, s.CreateUser(ctx, admin))
	ensureHubMembership(ctx, s, admin.ID)

	_ = doAgentRoleRequest(t, srv, admin, CreateAgentRequest{
		Name:      "test-admin-full",
		ProjectID: project.ID,
		AgentRole: "full",
	})

	if role, ok := getStoredAgentRole(t, s, project.ID, "test-admin-full"); ok {
		assert.Equal(t, "full", role,
			"admin user requesting full should get full")
	}
}

func TestCreateAgent_ProjectMaxCapsRole(t *testing.T) {
	srv, s, _, _ := setupAgentRoleTest(t)
	ctx := context.Background()

	// Create a project with max-agent-role=readonly.
	project := &store.Project{
		ID:   tid("project-readonly-max"),
		Name: "readonly-max-project",
		Slug: "readonly-max-project",
		Annotations: map[string]string{
			"scion.dev/max-agent-role": "readonly",
		},
		Created: time.Now(),
		Updated: time.Now(),
	}
	require.NoError(t, s.CreateProject(ctx, project))
	srv.createProjectMembersGroupAndPolicy(ctx, project)

	admin := &store.User{
		ID:          tid("user-admin-cap"),
		Email:       "admin-cap@test.com",
		DisplayName: "Admin Cap Tester",
		Role:        store.UserRoleAdmin,
		Status:      "active",
		Created:     time.Now(),
	}
	require.NoError(t, s.CreateUser(ctx, admin))
	ensureHubMembership(ctx, s, admin.ID)

	_ = doAgentRoleRequest(t, srv, admin, CreateAgentRequest{
		Name:      "test-admin-capped",
		ProjectID: project.ID,
		AgentRole: "full",
	})

	if role, ok := getStoredAgentRole(t, s, project.ID, "test-admin-capped"); ok {
		assert.Equal(t, "readonly", role,
			"admin requesting full in readonly-max project should get readonly")
	}
}

// ---------------------------------------------------------------------------
// Phase 7 — Read endpoint enforcement (ScopeProjectRead)
// ---------------------------------------------------------------------------

// doAgentReadRequest makes a GET request to the given path using an agent token
// with the specified scopes.
func doAgentReadRequest(t *testing.T, srv *Server, agentID, projectID, path string, scopes []AgentTokenScope) *httptest.ResponseRecorder {
	t.Helper()
	tokenSvc := srv.GetAgentTokenService()
	require.NotNil(t, tokenSvc)

	token, err := tokenSvc.GenerateAgentToken(agentID, projectID, scopes, nil)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("X-Scion-Agent-Token", token)

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	return rec
}

// setupReadScopeTest creates a server, user, project, and agent for read scope tests.
func setupReadScopeTest(t *testing.T) (*Server, store.Store, *store.Agent, *store.Project) {
	t.Helper()
	srv, s := testServer(t)
	ctx := context.Background()

	user := &store.User{
		ID:          tid("user-read-scope"),
		Email:       "readscope@test.com",
		DisplayName: "Read Scope Tester",
		Role:        store.UserRoleMember,
		Status:      "active",
		Created:     time.Now(),
	}
	require.NoError(t, s.CreateUser(ctx, user))
	ensureHubMembership(ctx, s, user.ID)

	project := &store.Project{
		ID:        tid("project-read-scope"),
		Name:      "read-scope-project",
		Slug:      "read-scope-project",
		OwnerID:   user.ID,
		CreatedBy: user.ID,
		Created:   time.Now(),
		Updated:   time.Now(),
	}
	require.NoError(t, s.CreateProject(ctx, project))
	srv.createProjectMembersGroupAndPolicy(ctx, project)

	agent := &store.Agent{
		ID:        tid("agent-read-scope"),
		Slug:      "read-scope-agent",
		Name:      "Read Scope Agent",
		ProjectID: project.ID,
		Phase:     "running",
		Created:   time.Now(),
		Updated:   time.Now(),
	}
	require.NoError(t, s.CreateAgent(ctx, agent))

	return srv, s, agent, project
}

func TestReadEndpoint_BaselineAgent_Allowed(t *testing.T) {
	srv, _, agent, project := setupReadScopeTest(t)

	// Baseline scopes include ScopeProjectRead.
	scopes := ScopesForRole(AgentRoleBaseline)

	endpoints := []string{
		"/api/v1/agents?projectId=" + project.ID,
		"/api/v1/agents/" + agent.ID,
		"/api/v1/templates",
		"/api/v1/skills",
		"/api/v1/harness-configs",
		"/api/v1/projects",
		"/api/v1/projects/" + project.ID,
	}

	for _, ep := range endpoints {
		t.Run(ep, func(t *testing.T) {
			rec := doAgentReadRequest(t, srv, agent.ID, project.ID, ep, scopes)
			assert.NotEqual(t, http.StatusForbidden, rec.Code,
				"baseline agent should not be forbidden on %s; got %d: %s",
				ep, rec.Code, rec.Body.String())
		})
	}
}

func TestReadEndpoint_ReadonlyAgent_Allowed(t *testing.T) {
	srv, _, agent, project := setupReadScopeTest(t)

	// Readonly scopes include ScopeProjectRead.
	scopes := ScopesForRole(AgentRoleReadOnly)

	endpoints := []string{
		"/api/v1/agents?projectId=" + project.ID,
		"/api/v1/agents/" + agent.ID,
		"/api/v1/templates",
		"/api/v1/skills",
		"/api/v1/harness-configs",
		"/api/v1/projects",
		"/api/v1/projects/" + project.ID,
	}

	for _, ep := range endpoints {
		t.Run(ep, func(t *testing.T) {
			rec := doAgentReadRequest(t, srv, agent.ID, project.ID, ep, scopes)
			assert.NotEqual(t, http.StatusForbidden, rec.Code,
				"readonly agent should not be forbidden on %s; got %d: %s",
				ep, rec.Code, rec.Body.String())
		})
	}
}

func TestReadEndpoint_NoReadScope_Blocked(t *testing.T) {
	srv, _, agent, project := setupReadScopeTest(t)

	// Simulate a legacy agent or misconfigured token: valid JWT but no ScopeProjectRead.
	scopes := []AgentTokenScope{ScopeAgentStatusUpdate}

	endpoints := []string{
		"/api/v1/agents?projectId=" + project.ID,
		"/api/v1/agents/" + agent.ID,
		"/api/v1/templates",
		"/api/v1/skills",
		"/api/v1/harness-configs",
		"/api/v1/projects",
		"/api/v1/projects/" + project.ID,
	}

	for _, ep := range endpoints {
		t.Run(ep, func(t *testing.T) {
			rec := doAgentReadRequest(t, srv, agent.ID, project.ID, ep, scopes)
			assert.Equal(t, http.StatusForbidden, rec.Code,
				"agent without ScopeProjectRead should be forbidden on %s; got %d: %s",
				ep, rec.Code, rec.Body.String())
			assert.Contains(t, rec.Body.String(), "project:read",
				"error should mention the missing scope")
		})
	}
}

func TestReadEndpoint_ProjectScopedAgents_NoReadScope_Blocked(t *testing.T) {
	srv, _, agent, project := setupReadScopeTest(t)

	// Agent with valid JWT but no ScopeProjectRead should be blocked on project-scoped routes.
	scopes := []AgentTokenScope{ScopeAgentStatusUpdate}

	endpoints := []string{
		"/api/v1/projects/" + project.ID + "/agents",
		"/api/v1/projects/" + project.ID + "/agents/" + agent.ID,
	}

	for _, ep := range endpoints {
		t.Run(ep, func(t *testing.T) {
			rec := doAgentReadRequest(t, srv, agent.ID, project.ID, ep, scopes)
			assert.Equal(t, http.StatusForbidden, rec.Code,
				"agent without ScopeProjectRead should be forbidden on %s; got %d: %s",
				ep, rec.Code, rec.Body.String())
			assert.Contains(t, rec.Body.String(), "project:read",
				"error should mention the missing scope")
		})
	}
}

func TestReadEndpoint_ProjectScopedAgents_WithReadScope_Allowed(t *testing.T) {
	srv, _, agent, project := setupReadScopeTest(t)

	// Baseline scopes include ScopeProjectRead — should be allowed.
	scopes := ScopesForRole(AgentRoleBaseline)

	endpoints := []string{
		"/api/v1/projects/" + project.ID + "/agents",
		"/api/v1/projects/" + project.ID + "/agents/" + agent.ID,
	}

	for _, ep := range endpoints {
		t.Run(ep, func(t *testing.T) {
			rec := doAgentReadRequest(t, srv, agent.ID, project.ID, ep, scopes)
			assert.NotEqual(t, http.StatusForbidden, rec.Code,
				"baseline agent should not be forbidden on %s; got %d: %s",
				ep, rec.Code, rec.Body.String())
		})
	}
}

func TestReadEndpoint_UserCaller_NotAffected(t *testing.T) {
	srv, _, _, project := setupReadScopeTest(t)

	// User callers should not be affected by the agent scope check.
	token, _, _, err := srv.userTokenService.GenerateTokenPair(
		tid("user-read-scope"), "readscope@test.com", "Read Scope Tester",
		store.UserRoleMember, ClientTypeWeb,
	)
	require.NoError(t, err)

	endpoints := []string{
		"/api/v1/agents?projectId=" + project.ID,
		"/api/v1/templates",
		"/api/v1/skills",
		"/api/v1/harness-configs",
		"/api/v1/projects",
		"/api/v1/projects/" + project.ID,
	}

	for _, ep := range endpoints {
		t.Run(ep, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, ep, nil)
			req.Header.Set("Authorization", "Bearer "+token)

			rec := httptest.NewRecorder()
			srv.Handler().ServeHTTP(rec, req)

			assert.NotEqual(t, http.StatusForbidden, rec.Code,
				"user caller should not be forbidden on %s; got %d: %s",
				ep, rec.Code, rec.Body.String())
		})
	}
}

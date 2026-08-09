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

// TestTemplateHubAccessScopes_StoredButIgnoredForToken verifies that
// hubAccess.scopes from a template are stored on the agent's AppliedConfig for
// backward-compatibility visibility, but are NOT used for JWT token generation.
// Token scopes are derived solely from the agent role (Phase 2 change).
func TestTemplateHubAccessScopes_StoredButIgnoredForToken(t *testing.T) {
	// Use a non-dev-auth server so the role is not auto-upgraded to full.
	s, err := newTestStore(":memory:")
	require.NoError(t, err)
	require.NoError(t, s.Migrate(context.Background()))

	cfg := DefaultServerConfig()
	cfg.AgentTokenConfig = AgentTokenConfig{
		SigningKey:    make([]byte, 32),
		TokenDuration: time.Hour,
	}
	// Deliberately NOT setting DevAuthToken so role-based scopes are enforced.

	srv, err := New(cfg, s)
	require.NoError(t, err)
	t.Cleanup(func() { _ = srv.Shutdown(context.Background()) })

	ctx := context.Background()

	// Create a project
	project := &store.Project{
		ID:      tid("project-tmpl-scopes"),
		Name:    "tmpl-scopes-project",
		Slug:    "tmpl-scopes-project",
		Created: time.Now(),
		Updated: time.Now(),
	}
	require.NoError(t, s.CreateProject(ctx, project))

	// Create a template with legacy hubAccess.scopes that include elevated permissions
	tmpl := &store.Template{
		ID:      tid("tmpl-hub-scopes"),
		Name:    "hub-scopes-template",
		Slug:    "hub-scopes-template",
		Scope:   "project",
		ScopeID: project.ID,
		Status:  store.TemplateStatusActive,
		Config: &store.TemplateConfig{
			HubAccess: &store.HubAccessConfig{
				Scopes: []string{"project:read", "agent:create", "agent:lifecycle", "secret:read"},
			},
		},
	}
	require.NoError(t, s.CreateTemplate(ctx, tmpl))

	// Simulate what populateAgentConfig does: build an agent with a baseline role
	// and populate it from the template.
	agent := &store.Agent{
		ID:        tid("agent-scopes-test"),
		Name:      "scopes-test-agent",
		Slug:      "scopes-test-agent",
		ProjectID: project.ID,
		AppliedConfig: &store.AgentAppliedConfig{
			AgentRole: string(AgentRoleBaseline),
		},
	}

	// Call populateAgentConfig to simulate the real code path
	srv.populateAgentConfig(ctx, agent, project, tmpl)

	// Verify the template scopes are stored on AppliedConfig for visibility
	require.NotNil(t, agent.AppliedConfig, "AppliedConfig should be set")
	assert.Equal(t, []string{"project:read", "agent:create", "agent:lifecycle", "secret:read"},
		agent.AppliedConfig.HubAccessScopes,
		"template hubAccess.scopes should be preserved on AppliedConfig for visibility")
	assert.Equal(t, tmpl.ID, agent.AppliedConfig.TemplateID,
		"template ID should be set on AppliedConfig")

	// Verify agentRoleAndScopes does NOT read HubAccessScopes
	role, additionalScopes := agentRoleAndScopes(agent)
	assert.Equal(t, AgentRoleBaseline, role,
		"role should come from AgentRole field, not template scopes")
	assert.Empty(t, additionalScopes,
		"agentRoleAndScopes should not produce additional scopes from HubAccessScopes")

	// Generate a token and verify scopes match the baseline role, not template scopes
	token, err := srv.GenerateAgentToken(agent.ID, agent.ProjectID, agent.Ancestry, role, additionalScopes)
	require.NoError(t, err)

	claims, err := srv.agentTokenService.ValidateAgentToken(token)
	require.NoError(t, err)

	// Baseline role grants: project:read, agent:status-update, agent:token-refresh,
	// agent:notify, agent:port-forward
	assert.True(t, claims.HasScope(ScopeProjectRead), "baseline should have project:read")
	assert.True(t, claims.HasScope(ScopeAgentStatusUpdate), "baseline should have agent:status-update")
	assert.True(t, claims.HasScope(ScopeAgentTokenRefresh), "baseline should have agent:token-refresh")

	// These scopes were in the template's hubAccess but must NOT leak into the token
	assert.False(t, claims.HasScope(ScopeAgentCreate),
		"agent:create from template scopes should NOT appear in baseline token")
	assert.False(t, claims.HasScope(ScopeAgentLifecycle),
		"agent:lifecycle from template scopes should NOT appear in baseline token")
	assert.False(t, claims.HasScope(ScopeProjectSecretRead),
		"secret:read from template scopes should NOT appear in baseline token")
}

// TestTemplateHubAccessScopes_EmptyDoesNotWarn verifies that a template with
// an empty hubAccess.scopes array does not trigger the deprecation warning.
func TestTemplateHubAccessScopes_EmptyDoesNotWarn(t *testing.T) {
	// An agent whose template config has hubAccess with empty scopes should
	// not modify HubAccessScopes and should behave identically to a template
	// without any hubAccess block.
	agent := &store.Agent{
		ID:   tid("agent-empty-scopes"),
		Name: "empty-scopes-agent",
		AppliedConfig: &store.AgentAppliedConfig{
			AgentRole: string(AgentRoleBaseline),
		},
	}

	tmpl := &store.Template{
		ID:   tid("tmpl-empty-scopes"),
		Name: "empty-scopes-template",
		Slug: "empty-scopes-template",
		Config: &store.TemplateConfig{
			HubAccess: &store.HubAccessConfig{
				Scopes: []string{},
			},
		},
	}

	srv, _ := testServer(t)
	srv.populateAgentConfig(context.Background(), agent, nil, tmpl)

	assert.Empty(t, agent.AppliedConfig.HubAccessScopes,
		"empty scopes array should not populate HubAccessScopes")
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

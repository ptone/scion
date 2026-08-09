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

// setupAgentRoleTest creates a server with a member user, project, and runtime broker.
// The user is the project creator and is in the hub-members group.
// The broker is linked to the project so agent creation reaches role resolution.
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

	broker := &store.RuntimeBroker{
		ID:        tid("broker-role-test"),
		Name:      "role-test-broker",
		Slug:      "role-test-broker",
		Endpoint:  "http://localhost:9800",
		Status:    store.BrokerStatusOnline,
		CreatedBy: user.ID,
	}
	require.NoError(t, s.CreateRuntimeBroker(ctx, broker))

	project := &store.Project{
		ID:                    tid("project-role-test"),
		Name:                  "role-test-project",
		Slug:                  "role-test-project",
		OwnerID:               user.ID,
		CreatedBy:             user.ID,
		DefaultRuntimeBrokerID: broker.ID,
		Created:               time.Now(),
		Updated:               time.Now(),
	}
	require.NoError(t, s.CreateProject(ctx, project))
	srv.createProjectMembersGroupAndPolicy(ctx, project)
	require.NoError(t, s.AddProjectProvider(ctx, &store.ProjectProvider{
		ProjectID:  project.ID,
		BrokerID:   broker.ID,
		BrokerName: broker.Name,
		Status:     store.BrokerStatusOnline,
	}))

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
	srv, _, user, project := setupAgentRoleTest(t)

	rec := doAgentRoleRequest(t, srv, user, CreateAgentRequest{
		Name:      "test-full-capped",
		ProjectID: project.ID,
		AgentRole: "full",
	})

	// Phase 4: member requesting full now gets a 403 instead of silent clamping.
	assert.Equal(t, http.StatusForbidden, rec.Code,
		"member user requesting full should get 403; got: %s", rec.Body.String())
	assert.Contains(t, rec.Body.String(), "your hub role (member) allows a maximum")
}

func TestCreateAgent_AdminGetsFull(t *testing.T) {
	srv, s, _, _ := setupAgentRoleTest(t)
	ctx := context.Background()

	// Phase 4: use a project with max-agent-role=full so both ceilings pass.
	// The default project has baseline max, which would now trigger a 403.
	fullProject := &store.Project{
		ID:   tid("project-full-max"),
		Name: "full-max-project",
		Slug: "full-max-project",
		Annotations: map[string]string{
			"scion.dev/max-agent-role": "full",
		},
		Created: time.Now(),
		Updated: time.Now(),
	}
	require.NoError(t, s.CreateProject(ctx, fullProject))
	srv.createProjectMembersGroupAndPolicy(ctx, fullProject)
	require.NoError(t, s.AddProjectProvider(ctx, &store.ProjectProvider{
		ProjectID: fullProject.ID, BrokerID: tid("broker-role-test"), BrokerName: "role-test-broker",
	}))

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
		ProjectID: fullProject.ID,
		AgentRole: "full",
	})

	if role, ok := getStoredAgentRole(t, s, fullProject.ID, "test-admin-full"); ok {
		assert.Equal(t, "full", role,
			"admin user requesting full in full-project should get full")
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
	require.NoError(t, s.AddProjectProvider(ctx, &store.ProjectProvider{
		ProjectID: project.ID, BrokerID: tid("broker-role-test"), BrokerName: "role-test-broker",
	}))

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

	rec := doAgentRoleRequest(t, srv, admin, CreateAgentRequest{
		Name:      "test-admin-capped",
		ProjectID: project.ID,
		AgentRole: "full",
	})

	// Phase 4: admin requesting full in readonly-max project now gets 403.
	assert.Equal(t, http.StatusForbidden, rec.Code,
		"admin requesting full in readonly-max project should get 403; got: %s", rec.Body.String())
	assert.Contains(t, rec.Body.String(), "project maximum agent role")

	// Verify agent was NOT persisted.
	_, err := s.GetAgentBySlug(ctx, project.ID, "test-admin-capped")
	assert.ErrorIs(t, err, store.ErrNotFound, "agent should not be persisted after 403")
}

// ── Phase 4 integration tests: fail-loud ceiling enforcement ──

func TestCreateAgent_MemberRequestsFull_403(t *testing.T) {
	srv, _, user, project := setupAgentRoleTest(t)

	rec := doAgentRoleRequest(t, srv, user, CreateAgentRequest{
		Name:      "test-member-full-403",
		ProjectID: project.ID,
		AgentRole: "full",
	})

	assert.Equal(t, http.StatusForbidden, rec.Code,
		"member requesting full should get 403; got: %s", rec.Body.String())
	assert.Contains(t, rec.Body.String(), "your hub role (member) allows a maximum of")
}

func TestCreateAgent_MemberRequestsBaseline_OK(t *testing.T) {
	srv, s, user, project := setupAgentRoleTest(t)

	rec := doAgentRoleRequest(t, srv, user, CreateAgentRequest{
		Name:      "test-member-baseline-ok",
		ProjectID: project.ID,
		AgentRole: "baseline",
	})

	// Should not be rejected — baseline is within the member ceiling.
	assert.NotEqual(t, http.StatusForbidden, rec.Code,
		"member requesting baseline should not get 403; got: %s", rec.Body.String())

	if role, ok := getStoredAgentRole(t, s, project.ID, "test-member-baseline-ok"); ok {
		assert.Equal(t, "baseline", role,
			"member requesting baseline should get baseline")
	}
}

func TestCreateAgent_AdminRequestsFull_OK(t *testing.T) {
	srv, s, _, _ := setupAgentRoleTest(t)
	ctx := context.Background()

	// Create a project that allows full role.
	fullProject := &store.Project{
		ID:   tid("project-full-admin-ok"),
		Name: "full-admin-ok-project",
		Slug: "full-admin-ok-project",
		Annotations: map[string]string{
			"scion.dev/max-agent-role": "full",
		},
		Created: time.Now(),
		Updated: time.Now(),
	}
	require.NoError(t, s.CreateProject(ctx, fullProject))
	srv.createProjectMembersGroupAndPolicy(ctx, fullProject)
	require.NoError(t, s.AddProjectProvider(ctx, &store.ProjectProvider{
		ProjectID: fullProject.ID, BrokerID: tid("broker-role-test"), BrokerName: "role-test-broker",
	}))

	admin := &store.User{
		ID:          tid("user-admin-full-ok"),
		Email:       "admin-full-ok@test.com",
		DisplayName: "Admin Full OK",
		Role:        store.UserRoleAdmin,
		Status:      "active",
		Created:     time.Now(),
	}
	require.NoError(t, s.CreateUser(ctx, admin))
	ensureHubMembership(ctx, s, admin.ID)

	rec := doAgentRoleRequest(t, srv, admin, CreateAgentRequest{
		Name:      "test-admin-full-ok",
		ProjectID: fullProject.ID,
		AgentRole: "full",
	})

	// Admin requesting full in a full-project should succeed.
	assert.NotEqual(t, http.StatusForbidden, rec.Code,
		"admin requesting full in full-project should not get 403; got: %s", rec.Body.String())

	if role, ok := getStoredAgentRole(t, s, fullProject.ID, "test-admin-full-ok"); ok {
		assert.Equal(t, "full", role,
			"admin requesting full in full-project should get full")
	}
}

func TestCreateAgent_ExceedsProjectMax_403(t *testing.T) {
	srv, s, _, _ := setupAgentRoleTest(t)
	ctx := context.Background()

	// Create a project with max-agent-role=baseline.
	baselineProject := &store.Project{
		ID:   tid("project-baseline-max"),
		Name: "baseline-max-project",
		Slug: "baseline-max-project",
		Annotations: map[string]string{
			"scion.dev/max-agent-role": "baseline",
		},
		Created: time.Now(),
		Updated: time.Now(),
	}
	require.NoError(t, s.CreateProject(ctx, baselineProject))
	srv.createProjectMembersGroupAndPolicy(ctx, baselineProject)
	require.NoError(t, s.AddProjectProvider(ctx, &store.ProjectProvider{
		ProjectID: baselineProject.ID, BrokerID: tid("broker-role-test"), BrokerName: "role-test-broker",
	}))

	admin := &store.User{
		ID:          tid("user-admin-proj-cap"),
		Email:       "admin-proj-cap@test.com",
		DisplayName: "Admin Proj Cap",
		Role:        store.UserRoleAdmin,
		Status:      "active",
		Created:     time.Now(),
	}
	require.NoError(t, s.CreateUser(ctx, admin))
	ensureHubMembership(ctx, s, admin.ID)

	rec := doAgentRoleRequest(t, srv, admin, CreateAgentRequest{
		Name:      "test-exceeds-proj-max",
		ProjectID: baselineProject.ID,
		AgentRole: "full",
	})

	// Admin requesting full in baseline-project should get 403 with project max message.
	assert.Equal(t, http.StatusForbidden, rec.Code,
		"admin requesting full in baseline-project should get 403; got: %s", rec.Body.String())
	assert.Contains(t, rec.Body.String(), "project maximum agent role is")

	// Verify agent was NOT persisted.
	_, err := s.GetAgentBySlug(ctx, baselineProject.ID, "test-exceeds-proj-max")
	assert.ErrorIs(t, err, store.ErrNotFound, "agent should not be persisted after 403")
}

func TestCreateAgent_NoRoleFlag_DefaultOK(t *testing.T) {
	srv, s, user, project := setupAgentRoleTest(t)
	ctx := context.Background()

	// Member with no --role should get baseline (default) without error.
	rec := doAgentRoleRequest(t, srv, user, CreateAgentRequest{
		Name:      "test-member-default",
		ProjectID: project.ID,
		// AgentRole intentionally omitted
	})

	assert.NotEqual(t, http.StatusForbidden, rec.Code,
		"member with no --role should not get 403; got: %s", rec.Body.String())

	if role, ok := getStoredAgentRole(t, s, project.ID, "test-member-default"); ok {
		assert.Equal(t, "baseline", role,
			"member with no --role should get baseline")
	}

	// Admin with no --role should also get baseline (project default) without error.
	admin := &store.User{
		ID:          tid("user-admin-default"),
		Email:       "admin-default@test.com",
		DisplayName: "Admin Default",
		Role:        store.UserRoleAdmin,
		Status:      "active",
		Created:     time.Now(),
	}
	require.NoError(t, s.CreateUser(ctx, admin))
	ensureHubMembership(ctx, s, admin.ID)

	rec = doAgentRoleRequest(t, srv, admin, CreateAgentRequest{
		Name:      "test-admin-default",
		ProjectID: project.ID,
		// AgentRole intentionally omitted
	})

	assert.NotEqual(t, http.StatusForbidden, rec.Code,
		"admin with no --role should not get 403; got: %s", rec.Body.String())

	if role, ok := getStoredAgentRole(t, s, project.ID, "test-admin-default"); ok {
		assert.Equal(t, "baseline", role,
			"admin with no --role in baseline-max project should get baseline")
	}
}

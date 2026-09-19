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

//go:build integration

package hub

// Regression test for #1634: agent-creates-agent 403 on GCP SA assignment.
//
// The root cause is google/uuid.UUID not being registered with pgx v5's type
// system, causing pgx to encode UUID parameters as OID 25 (text) instead of
// OID 2950 (uuid) under statement-cache pressure. Postgres rejects with
// SQLSTATE 42883 ("operator does not exist: uuid = text"). This only surfaces
// on Postgres — SQLite has no UUID type enforcement — and only on the
// agent-creates-agent path, which is deeper (delegation ceiling walk) than the
// user-creates-agent path.
//
// This test MUST run against a live Postgres instance to exercise the fix.
// It skips when SCION_TEST_POSTGRES_URL is not set.

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/GoogleCloudPlatform/scion/pkg/agent/state"
	"github.com/GoogleCloudPlatform/scion/pkg/ent/entc"
	"github.com/GoogleCloudPlatform/scion/pkg/store"
	"github.com/GoogleCloudPlatform/scion/pkg/store/entadapter"
	"github.com/GoogleCloudPlatform/scion/pkg/store/enttest"
)

// TestPostgres_AgentCreatesAgent_SAAssignment exercises the full
// agent-creates-agent path with project-default SA assignment on a Postgres
// backend. Without the pgx UUID type registration fix (#1634), the delegation
// ceiling walk in authz.go fails with SQLSTATE 42883 and the request returns
// 403.
func TestPostgres_AgentCreatesAgent_SAAssignment(t *testing.T) {
	if !enttest.Active() {
		t.Skip("integration: set SCION_TEST_POSTGRES_URL to run Postgres-backed authz tests")
	}

	ctx := context.Background()

	// ── Postgres-backed store ────────────────────────────────────────────
	dsn := enttest.NewSchemaURL(t)
	client, err := entc.OpenPostgres(dsn, entc.PoolConfig{MaxOpenConns: 5, MaxIdleConns: 2})
	require.NoError(t, err)
	t.Cleanup(func() { _ = client.Close() })
	s := entadapter.NewCompositeStore(client)

	// ── Server ───────────────────────────────────────────────────────────
	cfg := DefaultServerConfig()
	cfg.DevAuthToken = testDevToken
	cfg.DevUserConfig = DevUserConfig{
		Username:    "dev",
		DisplayName: "Development User",
		Email:       "dev@localhost",
	}
	cfg.BrokerAuthConfig = DefaultBrokerAuthConfig()
	srv, err := New(cfg, s)
	require.NoError(t, err)
	srv.SetHubID("test-hub-pgx-uuid")
	t.Cleanup(func() { _ = srv.Shutdown(ctx) })

	// Remove backfill marker so pre-backfill agents are allowed through
	// the delegation ceiling (matching the pattern in bypassAgentsServer).
	_ = s.DeleteHubSetting(ctx, "migration_delegation_edge_backfill_v1")

	// ── Seed data ────────────────────────────────────────────────────────
	owner := &store.User{
		ID:          tid("pgx-owner"),
		Email:       "pgx-owner@example.com",
		DisplayName: "PGX Owner",
		Role:        store.UserRoleMember,
		Status:      "active",
		Created:     time.Now(),
	}
	require.NoError(t, s.CreateUser(ctx, owner))

	proj := &store.Project{
		ID:      tid("pgx-proj"),
		Name:    "PGX Project",
		Slug:    "pgx-proj",
		OwnerID: owner.ID,
	}
	require.NoError(t, s.CreateProject(ctx, proj))

	// Auto-provide broker so agent creation can resolve one.
	brokerID := uuid.New().String()
	broker := &store.RuntimeBroker{
		ID:          brokerID,
		Name:        "pgx-broker",
		Slug:        "pgx-broker",
		Status:      store.BrokerStatusOnline,
		AutoProvide: true,
		Created:     time.Now(),
		Updated:     time.Now(),
	}
	require.NoError(t, s.CreateRuntimeBroker(ctx, broker))
	require.NoError(t, s.CreateBrokerSecret(ctx, &store.BrokerSecret{
		BrokerID:  broker.ID,
		SecretKey: []byte("pgx-secret-key-32-bytes-ok!!!!!!"),
		Algorithm: store.BrokerSecretAlgorithmHMACSHA256,
		Status:    store.BrokerSecretStatusActive,
	}))
	require.NoError(t, s.AddProjectProvider(ctx, &store.ProjectProvider{
		ProjectID:  proj.ID,
		BrokerID:   broker.ID,
		BrokerName: broker.Name,
		Status:     store.BrokerStatusOnline,
	}))
	proj.DefaultRuntimeBrokerID = broker.ID
	require.NoError(t, s.UpdateProject(ctx, proj))

	// ── Project-scoped GCP SA as project default ─────────────────────────
	callerSAEmail := "pgx-caller-sa@proj.iam.gserviceaccount.com"
	callerSA := &store.GCPServiceAccount{
		ID:        tid("pgx-caller-sa"),
		Scope:     store.ScopeProject,
		ScopeID:   proj.ID,
		Email:     callerSAEmail,
		ProjectID: "gcp-proj",
		Verified:  true,
		CreatedBy: owner.ID,
		CreatedAt: time.Now(),
	}
	require.NoError(t, s.CreateGCPServiceAccount(ctx, callerSA))

	targetSAEmail := "pgx-target-sa@proj.iam.gserviceaccount.com"
	targetSA := &store.GCPServiceAccount{
		ID:        tid("pgx-target-sa"),
		Scope:     store.ScopeProject,
		ScopeID:   proj.ID,
		Email:     targetSAEmail,
		ProjectID: "gcp-proj",
		Verified:  true,
		CreatedBy: owner.ID,
		CreatedAt: time.Now(),
	}
	require.NoError(t, s.CreateGCPServiceAccount(ctx, targetSA))

	// Set target SA as the project default.
	proj.Annotations = map[string]string{
		projectSettingDefaultGCPIdentityMode: store.GCPMetadataModeAssign,
		projectSettingDefaultGCPIdentitySAID: targetSA.ID,
	}
	require.NoError(t, s.UpdateProject(ctx, proj))

	// ── Parent agent with full role and the caller SA assigned ───────────
	parentAgent := &store.Agent{
		ID:        tid("pgx-parent"),
		Slug:      tid("pgx-parent"),
		Name:      "pgx-parent",
		ProjectID: proj.ID,
		Phase:     string(state.PhaseRunning),
		CreatedBy: owner.ID,
		OwnerID:   owner.ID,
		Ancestry:  []string{owner.ID},
		AppliedConfig: &store.AgentAppliedConfig{
			AgentRole: string(AgentRoleFull),
			GCPIdentity: &store.GCPIdentityConfig{
				MetadataMode:        store.GCPMetadataModeAssign,
				ServiceAccountID:    callerSA.ID,
				ServiceAccountEmail: callerSAEmail,
			},
		},
	}
	require.NoError(t, s.CreateAgent(ctx, parentAgent))

	// ── Enforce SA assignment with a checker that allows the target SA ───
	checker := store.NewFakeCallerPermissionChecker().AllowTarget(targetSAEmail)
	enforceSAAssign(srv, checker)

	// ── Call agent-create endpoint AS the parent agent ────────────────────
	// This exercises the full delegation ceiling walk on Postgres, which
	// requires uuid parameters to be encoded correctly (OID 2950, not 25).
	tokenSvc := srv.GetAgentTokenService()
	require.NotNil(t, tokenSvc)
	fullScopes := ScopesForRole(AgentRoleFull)
	tok, err := tokenSvc.GenerateAgentToken(parentAgent.ID, parentAgent.ProjectID, fullScopes, nil)
	require.NoError(t, err)

	rec := doRequestWithAgentToken(t, srv, http.MethodPost,
		"/api/v1/projects/"+proj.ID+"/agents",
		CreateAgentRequest{Name: "pgx-child-agent"},
		tok,
	)

	// ── Assertions ───────────────────────────────────────────────────────
	// Without the fix: 403 (delegation ceiling fails with SQLSTATE 42883).
	// With the fix: 201 (child created, SA assigned from project default).
	require.Equal(t, http.StatusCreated, rec.Code,
		"agent-creates-agent must succeed on Postgres with UUID fix; got: %s",
		rec.Body.String())

	var resp CreateAgentResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.NotNil(t, resp.Agent, "response must include the created agent")

	child, err := s.GetAgent(ctx, resp.Agent.ID)
	require.NoError(t, err)
	require.NotNil(t, child.AppliedConfig, "child agent must have applied config")
	require.NotNil(t, child.AppliedConfig.GCPIdentity, "child agent must have GCP identity config")
	assert.Equal(t, store.GCPMetadataModeAssign, child.AppliedConfig.GCPIdentity.MetadataMode,
		"child agent must inherit project-default assign mode")
	assert.Equal(t, targetSA.ID, child.AppliedConfig.GCPIdentity.ServiceAccountID,
		"child agent must be assigned the project-default SA")
	assert.Equal(t, targetSAEmail, child.AppliedConfig.GCPIdentity.ServiceAccountEmail,
		"child agent must have the project-default SA email")
}

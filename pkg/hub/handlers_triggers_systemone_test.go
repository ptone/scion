//go:build !no_sqlite

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

package hub

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/GoogleCloudPlatform/scion/pkg/durablequeue"
	"github.com/GoogleCloudPlatform/scion/pkg/experiments"
	"github.com/GoogleCloudPlatform/scion/pkg/hub/trigger"
	"github.com/GoogleCloudPlatform/scion/pkg/ruleengine"
	"github.com/GoogleCloudPlatform/scion/pkg/secret"
	"github.com/GoogleCloudPlatform/scion/pkg/store"
	"github.com/GoogleCloudPlatform/scion/pkg/systemone/egress"
	systemoneruntime "github.com/GoogleCloudPlatform/scion/pkg/systemone/runtime"
)

func enableSystemOneExperimentForTriggerTest(t *testing.T, srv *Server, enabled bool) {
	t.Helper()
	var active []experiments.Experiment
	for _, e := range srv.experimentRegistry().All() {
		if e.Name == experiments.SystemOne {
			e.Default = enabled
		}
		active = append(active, e)
	}
	reg, err := experiments.NewRegistry(active, nil)
	require.NoError(t, err)
	srv.experiments = reg
}

func TestTriggerAuthoring_SystemOneD1AllowlistAndD2AgentExclusion(t *testing.T) {
	f := setupTriggersTestFixture(t)
	ctx := context.Background()

	epRec := f.doWebhookEndpoint(t, f.ownerAuth, "", http.MethodPost, CreateWebhookEndpointRequest{Name: "so-auth-ep"})
	require.Equal(t, http.StatusCreated, epRec.Code)
	var ep WebhookEndpointCreatedResponse
	require.NoError(t, json.Unmarshal(epRec.Body.Bytes(), &ep))

	// Create an enabled System One instance in the hub store.
	now := time.Now().UTC()
	inst := &store.SystemOneInstance{
		ID:        tid("so-inst-1"),
		Name:      "hub-judge",
		Adapter:   "typesafe",
		Model:     "jev-v1",
		Endpoint:  "https://judge.example.com",
		Auth:      store.SystemOneInstanceAuth{Kind: "api_key"},
		Enabled:   true,
		Revision:  1,
		CreatedAt: now,
		UpdatedAt: now,
	}
	require.NoError(t, f.store.CreateSystemOneInstance(ctx, inst))

	soExpr := `{"inputs":["event.subject"],"spec":{"version":1,"binding":{"instance":"hub-judge"},"questions":[{"id":"q1","type":"binary","instructions":"Is this urgent?"}],"output":{"question":"q1"}}}`

	// 1. With hub.system_one OFF: authoring a system_one element is refused (400 invalid_rule, unknown_dialect).
	recOff := f.doTrigger(t, f.ownerAuth, "", http.MethodPost, CreateTriggerRequest{
		Name:   "so-exp-off",
		Source: TriggerSourceSpec{Kind: store.TriggerSourceKindWebhook, ID: ep.ID},
		Match: &TriggerMatchSpec{
			Rules: []TriggerMatchRuleSpec{
				{Dialect: "cel", Expr: `event.type == "webhook.received"`},
				{Dialect: "system_one", Expr: soExpr},
			},
		},
		Action: TriggerActionSpec{Type: "message", AgentID: f.targetAgent.ID, Message: "hi"},
	})
	require.Equal(t, http.StatusBadRequest, recOff.Code, recOff.Body.String())
	ebOff := decodeTriggerRuleError(t, recOff)
	assert.Equal(t, ErrCodeInvalidRule, ebOff.Code)
	assert.Equal(t, ruleengine.ReasonUnknownDialect, ebOff.Details["reason"])

	// Enable hub.system_one experiment.
	enableSystemOneExperimentForTriggerTest(t, f.srv, true)

	// 2. D1: Project instance allowlist is EMPTY by default -> 400 invalid_rule (binding error).
	recNoAllow := f.doTrigger(t, f.ownerAuth, "", http.MethodPost, CreateTriggerRequest{
		Name:   "so-not-allowlisted",
		Source: TriggerSourceSpec{Kind: store.TriggerSourceKindWebhook, ID: ep.ID},
		Match: &TriggerMatchSpec{
			Rules: []TriggerMatchRuleSpec{
				{Dialect: "system_one", Expr: soExpr},
			},
		},
		Action: TriggerActionSpec{Type: "message", AgentID: f.targetAgent.ID, Message: "hi"},
	})
	require.Equal(t, http.StatusBadRequest, recNoAllow.Code, recNoAllow.Body.String())
	ebNoAllow := decodeTriggerRuleError(t, recNoAllow)
	assert.Equal(t, ErrCodeInvalidRule, ebNoAllow.Code)
	assert.Equal(t, "binding", ebNoAllow.Details["category"])

	// Opt the project into "hub-judge" (D1 explicit opt-in).
	_, _, err := f.store.SetProjectSystemOneInstances(ctx, f.projectID, []string{"hub-judge"})
	require.NoError(t, err)

	// 3. D2: Agent caller may NOT author System One elements -> 400 invalid_rule {reason: agent_not_allowed}.
	recAgentSO := f.doTrigger(t, f.agentAAuth, "", http.MethodPost, CreateTriggerRequest{
		Name:   "so-agent-denied",
		Source: TriggerSourceSpec{Kind: store.TriggerSourceKindWebhook, ID: ep.ID},
		Match: &TriggerMatchSpec{
			Rules: []TriggerMatchRuleSpec{
				{Dialect: "cel", Expr: `event.type == "webhook.received"`},
				{Dialect: "system_one", Expr: soExpr},
			},
		},
		Action: TriggerActionSpec{Type: "message", AgentID: f.agentA.ID, Message: "hi"},
	})
	require.Equal(t, http.StatusBadRequest, recAgentSO.Code, recAgentSO.Body.String())
	ebAgent := decodeTriggerRuleError(t, recAgentSO)
	assert.Equal(t, ErrCodeInvalidRule, ebAgent.Code)
	assert.Equal(t, "agent_not_allowed", ebAgent.Details["reason"])

	// 3b. O3: Legacy scalar match.dialect: "system_one" is also refused for agent callers.
	recAgentLegacySO := f.doTrigger(t, f.agentAAuth, "", http.MethodPost, CreateTriggerRequest{
		Name:   "so-agent-denied-legacy-scalar",
		Source: TriggerSourceSpec{Kind: store.TriggerSourceKindWebhook, ID: ep.ID},
		Match: &TriggerMatchSpec{
			Dialect: "system_one",
			Expr:    soExpr,
		},
		Action: TriggerActionSpec{Type: "message", AgentID: f.agentA.ID, Message: "hi"},
	})
	require.Equal(t, http.StatusBadRequest, recAgentLegacySO.Code, recAgentLegacySO.Body.String())
	ebAgentLegacy := decodeTriggerRuleError(t, recAgentLegacySO)
	assert.Equal(t, ErrCodeInvalidRule, ebAgentLegacy.Code)
	assert.Equal(t, "agent_not_allowed", ebAgentLegacy.Details["reason"])

	// 4. User caller with allowlisted instance succeeds (201 Created).
	recOK := f.doTrigger(t, f.ownerAuth, "", http.MethodPost, CreateTriggerRequest{
		Name:   "so-owner-ok",
		Source: TriggerSourceSpec{Kind: store.TriggerSourceKindWebhook, ID: ep.ID},
		Match: &TriggerMatchSpec{
			Rules: []TriggerMatchRuleSpec{
				{Dialect: "cel", Expr: `event.type == "webhook.received"`},
				{Dialect: "system_one", Expr: soExpr},
			},
		},
		Action: TriggerActionSpec{Type: "message", AgentID: f.targetAgent.ID, Message: "hi"},
	})
	require.Equal(t, http.StatusCreated, recOK.Code, recOK.Body.String())

	// 5. Removing the instance from the project allowlist refuses subsequent resume (400 invalid_rule).
	var created TriggerResponse
	require.NoError(t, json.Unmarshal(recOK.Body.Bytes(), &created))
	_, _, err = f.store.SetProjectSystemOneInstances(ctx, f.projectID, nil)
	require.NoError(t, err)
	pauseRec := f.doTrigger(t, f.ownerAuth, created.ID+"/pause", http.MethodPost, nil)
	require.Equal(t, http.StatusOK, pauseRec.Code, pauseRec.Body.String())

	resumeRec := f.doTrigger(t, f.ownerAuth, created.ID+"/resume", http.MethodPost, nil)
	require.Equal(t, http.StatusBadRequest, resumeRec.Code, resumeRec.Body.String())
	ebResume := decodeTriggerRuleError(t, resumeRec)
	assert.Equal(t, ErrCodeInvalidRule, ebResume.Code)
	assert.Equal(t, "binding", ebResume.Details["category"])
}

type m1CountingSecretBackend struct {
	secret.SecretBackend
	reads *atomic.Int32
}

func (c *m1CountingSecretBackend) Get(ctx context.Context, name, scope, scopeID string) (*secret.SecretWithValue, error) {
	c.reads.Add(1)
	return c.SecretBackend.Get(ctx, name, scope, scopeID)
}

// TestTriggerDispatcher_M1RecordedAuthorityPreEgressProductionWiring proves M1
// through the real production serverTriggerCore / resolveRecordedAuthority /
// SystemOneRuntime / SecretBackend wiring:
//   - authorized author (project-member): secret read + provider call occur;
//   - de-roled author (downgraded to project-viewer): 0 secret reads, 0 provider
//     calls, terminal refused fire with ErrFireRecordedAuthorityRefused;
//   - removed author (project role binding deleted): 0 secret reads, 0 provider
//     calls, terminal refused fire with ErrFireRecordedAuthorityRefused;
//   - deleted author (user deleted from store): 0 secret reads, 0 provider calls,
//     terminal refused fire with ErrFireRecordedAuthorityRefused.
func TestTriggerDispatcher_M1RecordedAuthorityPreEgressProductionWiring(t *testing.T) {
	f := setupTriggersTestFixture(t)
	ctx := context.Background()
	enableSystemOneExperimentForTriggerTest(t, f.srv, true)

	var providerCalls atomic.Int32
	tlsSrv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		providerCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
		  "model": "jev-1.14.0",
		  "answers": {
		    "q1": {"type": "noul", "noul": 0.92}
		  },
		  "usage": {"input_tokens": 12, "output_tokens": 2}
		}`))
	}))
	t.Cleanup(tlsSrv.Close)

	f.srv.setSystemOneRuntimeOptions(
		systemoneruntime.WithEgressOptionsForTesting(
			egress.WithLoopbackServerForTesting(tlsSrv.URL, tlsSrv.Client()),
		),
	)

	var secretReads atomic.Int32
	f.srv.SetSecretBackend(&m1CountingSecretBackend{
		SecretBackend: f.srv.secretBackend,
		reads:         &secretReads,
	})

	// Create a real enabled typesafe instance via the admin API (stores encrypted API key).
	superAdmin, err := f.store.GetUser(ctx, hubConfigTokenUser(t, f.store, "m1-s1-admin", store.SystemRoleSuperAdmin))
	require.NoError(t, err)
	createInstRec := doRequestAsUser(t, f.srv, superAdmin, http.MethodPost, systemOneAdminPath, map[string]any{
		"name":    "hub-judge-m1",
		"adapter": "typesafe",
		"model":   "jev-1.14.0",
		"auth":    map[string]any{"kind": "api_key", "apiKey": "sk-offline-m1-test-key-0001"},
	})
	require.Equal(t, http.StatusCreated, createInstRec.Code, createInstRec.Body.String())

	instRow, err := f.store.GetSystemOneInstanceByName(ctx, "hub-judge-m1")
	require.NoError(t, err)
	instRow.Endpoint = tlsSrv.URL
	require.NoError(t, f.store.UpdateSystemOneInstance(ctx, instRow))

	_, _, err = f.store.SetProjectSystemOneInstances(ctx, f.projectID, []string{"hub-judge-m1"})
	require.NoError(t, err)

	epRec := f.doWebhookEndpoint(t, f.ownerAuth, "", http.MethodPost, CreateWebhookEndpointRequest{Name: "m1-ep"})
	require.Equal(t, http.StatusCreated, epRec.Code, epRec.Body.String())
	var ep WebhookEndpointCreatedResponse
	require.NoError(t, json.Unmarshal(epRec.Body.Bytes(), &ep))

	soExpr := `{"inputs":["event.subject"],"spec":{"version":1,"binding":{"instance":"hub-judge-m1"},"questions":[{"id":"q1","type":"binary","instructions":"Is this urgent?"}],"output":{"question":"q1"}}}`

	// Trigger 1 authored by f.admin (project-admin, holds trigger.create/update and agent.message).
	tr1Rec := f.doTrigger(t, f.adminAuth, "", http.MethodPost, CreateTriggerRequest{
		Name:   "m1-admin-trig",
		Source: TriggerSourceSpec{Kind: store.TriggerSourceKindWebhook, ID: ep.ID},
		Match: &TriggerMatchSpec{
			Rules: []TriggerMatchRuleSpec{
				{Dialect: "cel", Expr: `event.type == "webhook.received"`},
				{Dialect: "system_one", Expr: soExpr},
			},
		},
		Action: TriggerActionSpec{Type: "message", AgentID: f.targetAgent.ID, Message: "alert"},
	})
	require.Equal(t, http.StatusCreated, tr1Rec.Code, tr1Rec.Body.String())
	var tr1 TriggerResponse
	require.NoError(t, json.Unmarshal(tr1Rec.Body.Bytes(), &tr1))

	// Construct the real production dispatcher wired to serverTriggerCore.
	disp, err := f.srv.newTriggerDispatcher(nil)
	require.NoError(t, err)

	dispatchAndLoadFire := func(evTag, triggerID string) *store.TriggerFire {
		t.Helper()
		ev := &store.TriggerEvent{
			ID:         tid("m1-ev-" + evTag),
			ProjectID:  f.projectID,
			SourceKind: store.TriggerSourceKindWebhook,
			SourceID:   ep.ID,
			Type:       "webhook.received",
			Subject:    "production outage on auth",
			DedupKey:   "m1-dedup-" + evTag,
			ReceivedAt: time.Now().UTC(),
		}
		require.NoError(t, f.store.CreateTriggerEvent(ctx, ev))
		payload, mErr := json.Marshal(trigger.EventQueuePayload{
			Version: trigger.EventQueuePayloadVersion,
			EventID: ev.ID,
		})
		require.NoError(t, mErr)
		res := disp.HandleItem(ctx, durablequeue.Item{
			Kind:     trigger.EventQueueKind,
			DedupKey: ev.ID,
			Payload:  payload,
		}, nil)
		require.True(t, res.Done, "event %s HandleItem should finish: err=%v retryAt=%v", evTag, res.Err, res.RetryAt)

		fires, lErr := f.store.ListTriggerFires(ctx, store.TriggerFireFilter{
			ProjectID: f.projectID,
			TriggerID: triggerID,
		}, store.ListOptions{Limit: 20})
		require.NoError(t, lErr)
		for i := range fires.Items {
			if fires.Items[i].EventID == ev.ID {
				return &fires.Items[i]
			}
		}
		t.Fatalf("no fire recorded for event %s trigger %s", ev.ID, triggerID)
		return nil
	}

	// 1. Authorized positive control: f.admin has project-admin -> secret read & provider call occur.
	secretReads.Store(0)
	providerCalls.Store(0)
	fireOK := dispatchAndLoadFire("authorized", tr1.ID)
	assert.Equal(t, int32(1), providerCalls.Load(), "authorized author must reach System One provider")
	assert.GreaterOrEqual(t, secretReads.Load(), int32(1), "authorized author must read provider secret")
	assert.NotEqual(t, store.TriggerFireStatusRefused, fireOK.Status, "authorized positive control must not be refused (err=%q)", fireOK.Error)
	assert.NotEqual(t, trigger.ErrFireRecordedAuthorityRefused, fireOK.Error)

	// 2. De-roled author: downgrade f.admin from project-admin to project-viewer.
	_, err = f.store.DeleteRoleBindingsForPrincipal(ctx, store.RoleBindingPrincipalUser, f.admin.ID)
	require.NoError(t, err)
	viewerRD, err := f.store.GetRoleDefinitionByName(ctx, store.ProjectRoleViewer, store.RoleScopeProject)
	require.NoError(t, err)
	_, err = f.store.CreateRoleBinding(ctx, &store.RoleBinding{
		RoleDefinitionID: viewerRD.ID,
		PrincipalType:    store.RoleBindingPrincipalUser,
		PrincipalID:      f.admin.ID,
		ScopeType:        store.RoleScopeProject,
		ScopeID:          f.projectID,
		CreatedBy:        f.owner.ID,
		CreatedAt:        time.Now().UTC(),
	})
	require.NoError(t, err)

	secretReads.Store(0)
	providerCalls.Store(0)
	fireDeroled := dispatchAndLoadFire("deroled", tr1.ID)
	assert.Equal(t, int32(0), providerCalls.Load(), "de-roled author must cause zero provider calls")
	assert.Equal(t, int32(0), secretReads.Load(), "de-roled author must cause zero secret reads")
	assert.Equal(t, store.TriggerFireStatusRefused, fireDeroled.Status)
	assert.Equal(t, trigger.ErrFireRecordedAuthorityRefused, fireDeroled.Error)
	assert.Equal(t, "0/0", fireDeroled.MatchedThrough)

	// 3. Removed author: remove all project role bindings for f.admin.
	_, err = f.store.DeleteRoleBindingsForPrincipal(ctx, store.RoleBindingPrincipalUser, f.admin.ID)
	require.NoError(t, err)

	secretReads.Store(0)
	providerCalls.Store(0)
	fireRemoved := dispatchAndLoadFire("removed", tr1.ID)
	assert.Equal(t, int32(0), providerCalls.Load(), "removed author must cause zero provider calls")
	assert.Equal(t, int32(0), secretReads.Load(), "removed author must cause zero secret reads")
	assert.Equal(t, store.TriggerFireStatusRefused, fireRemoved.Status)
	assert.Equal(t, trigger.ErrFireRecordedAuthorityRefused, fireRemoved.Error)
	assert.Equal(t, "0/0", fireRemoved.MatchedThrough)

	// Pause tr1 so only tr2 is evaluated for the deleted-author case.
	pauseRec := f.doTrigger(t, f.ownerAuth, tr1.ID+"/pause", http.MethodPost, nil)
	require.Equal(t, http.StatusOK, pauseRec.Code, pauseRec.Body.String())

	// 4. Deleted author: create a fresh project-admin user, author tr2, then delete the user.
	delUser := &store.User{
		ID:          tid("m1-del-user"),
		Email:       "m1-del-user@triggers.example.com",
		DisplayName: "M1 Deleted Author",
		Role:        store.UserRoleMember,
		Status:      "active",
		Created:     time.Now().UTC(),
	}
	require.NoError(t, f.store.CreateUser(ctx, delUser))
	adminRD, err := f.store.GetRoleDefinitionByName(ctx, store.ProjectRoleAdmin, store.RoleScopeProject)
	require.NoError(t, err)
	_, err = f.store.CreateRoleBinding(ctx, &store.RoleBinding{
		RoleDefinitionID: adminRD.ID,
		PrincipalType:    store.RoleBindingPrincipalUser,
		PrincipalID:      delUser.ID,
		ScopeType:        store.RoleScopeProject,
		ScopeID:          f.projectID,
		CreatedBy:        f.owner.ID,
		CreatedAt:        time.Now().UTC(),
	})
	require.NoError(t, err)
	delUserAuth := NewAuthenticatedUser(delUser.ID, delUser.Email, delUser.DisplayName, delUser.Role, "api")

	tr2Rec := f.doTrigger(t, delUserAuth, "", http.MethodPost, CreateTriggerRequest{
		Name:   "m1-deleted-author-trig",
		Source: TriggerSourceSpec{Kind: store.TriggerSourceKindWebhook, ID: ep.ID},
		Match: &TriggerMatchSpec{
			Rules: []TriggerMatchRuleSpec{
				{Dialect: "system_one", Expr: soExpr},
			},
		},
		Action: TriggerActionSpec{Type: "message", AgentID: f.targetAgent.ID, Message: "alert"},
	})
	require.Equal(t, http.StatusCreated, tr2Rec.Code, tr2Rec.Body.String())
	var tr2 TriggerResponse
	require.NoError(t, json.Unmarshal(tr2Rec.Body.Bytes(), &tr2))

	require.NoError(t, f.store.DeleteUser(ctx, delUser.ID))

	secretReads.Store(0)
	providerCalls.Store(0)
	fireDeleted := dispatchAndLoadFire("deleted", tr2.ID)
	assert.Equal(t, int32(0), providerCalls.Load(), "deleted author must cause zero provider calls")
	assert.Equal(t, int32(0), secretReads.Load(), "deleted author must cause zero secret reads")
	assert.Equal(t, store.TriggerFireStatusRefused, fireDeleted.Status)
	assert.Equal(t, trigger.ErrFireRecordedAuthorityRefused, fireDeleted.Error)
	assert.Equal(t, "0/0", fireDeleted.MatchedThrough)
}

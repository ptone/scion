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
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/GoogleCloudPlatform/scion/pkg/experiments"
	"github.com/GoogleCloudPlatform/scion/pkg/ruleengine"
	"github.com/GoogleCloudPlatform/scion/pkg/store"
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

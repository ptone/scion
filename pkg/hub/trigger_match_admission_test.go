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
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/GoogleCloudPlatform/scion/pkg/api"
	"github.com/GoogleCloudPlatform/scion/pkg/ruleengine"
	"github.com/GoogleCloudPlatform/scion/pkg/store"
	"github.com/GoogleCloudPlatform/scion/pkg/store/entadapter"
	"github.com/GoogleCloudPlatform/scion/pkg/store/enttest"
	"github.com/GoogleCloudPlatform/scion/pkg/triggerrule"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func seedStoredTriggerWithRules(t *testing.T, f *triggersTestFixture, epID, name, status string, rules []store.TriggerMatchRule, creatorID, initiatorKind, initiatorID string) *store.Trigger {
	t.Helper()
	now := time.Now().UTC()
	tr := &store.Trigger{
		ID:            api.NewUUID(),
		ProjectID:     f.projectID,
		Name:          name,
		Description:   "initial-desc",
		Status:        status,
		SourceKind:    store.TriggerSourceKindWebhook,
		SourceID:      epID,
		MatchTypes:    []string{"webhook.received"},
		MatchRules:    rules,
		ActionType:    store.TriggerActionMessage,
		ActionPayload: `{"agentId":"` + f.targetAgent.ID + `","message":"hi"}`,
		SubjectMode:   store.TriggerSubjectModeNew,
		CreatedBy:     creatorID,
		InitiatorAttribution: store.InitiatorAttribution{
			InitiatorPrincipalKind:  initiatorKind,
			InitiatorPrincipalID:    initiatorID,
			InitiatorCredentialKind: store.InitiatorCredentialKindSession,
			AttributionVersion:      1,
			AuthorizationRevision:   1,
		},
		CreatedAt: now,
		UpdatedAt: now,
	}
	require.NoError(t, f.store.CreateTrigger(context.Background(), tr))
	return tr
}

func runPatchSkipsAdmissionForUnchangedElements(t *testing.T, f *triggersTestFixture) {
	t.Helper()
	ctx := context.Background()

	epRec := f.doWebhookEndpoint(t, f.ownerAuth, "", http.MethodPost, CreateWebhookEndpointRequest{Name: "j16c-ep-" + api.NewUUID()[:8]})
	require.Equal(t, http.StatusCreated, epRec.Code, epRec.Body.String())
	var ep WebhookEndpointCreatedResponse
	require.NoError(t, json.Unmarshal(epRec.Body.Bytes(), &ep))

	const (
		celExprA = `event.attributes["branch"] == "main"`
		celExprB = `event.attributes["branch"] == "release"`
		celExprC = `has(event.payload.pr)`
		s1Expr   = `Does this PR touch authentication?`
		s1Expr2  = `Does this PR touch billing?`
	)

	// 1. POST create with system_one is still refused (400 dialect_not_allowed).
	createRec := f.doTrigger(t, f.ownerAuth, "", http.MethodPost, CreateTriggerRequest{
		Name:   "j16c-create-s1",
		Source: TriggerSourceSpec{Kind: store.TriggerSourceKindWebhook, ID: ep.ID},
		Match: &TriggerMatchSpec{Rules: []TriggerMatchRuleSpec{
			{Dialect: "cel", Expr: celExprA},
			{Dialect: "system_one", Expr: s1Expr},
		}},
		Action: TriggerActionSpec{Type: "message", AgentID: f.targetAgent.ID, Message: "hi"},
	})
	require.Equal(t, http.StatusBadRequest, createRec.Code, createRec.Body.String())
	createErr := decodeTriggerRuleError(t, createRec)
	assert.Equal(t, ErrCodeInvalidRule, createErr.Code)
	assert.Equal(t, "system_one", createErr.Details["dialect"])
	assert.Equal(t, ruleengine.ReasonDialectNotAllowed, createErr.Details["reason"])
	assert.Equal(t, ruleengine.ReasonDialectNotAllowed, createErr.Details["category"])
	assert.Equal(t, float64(2), createErr.Details["element"])

	// Seed a trigger with [cel_A, system_one] directly in the store.
	tr := seedStoredTriggerWithRules(t, f, ep.ID, "j16c-mixed", store.TriggerStatusActive, []store.TriggerMatchRule{
		{Dialect: "cel", Expr: celExprA},
		{Dialect: "system_one", Expr: s1Expr},
	}, f.owner.ID, "user", f.owner.ID)

	// 2. PATCH description only -> 200, rules byte-identical.
	newDesc := "updated-desc"
	descRec := f.doTrigger(t, f.ownerAuth, tr.ID, http.MethodPatch, UpdateTriggerRequest{
		Description: &newDesc,
	})
	require.Equal(t, http.StatusOK, descRec.Code, descRec.Body.String())
	stored, err := f.store.GetTrigger(ctx, tr.ID)
	require.NoError(t, err)
	assert.Equal(t, newDesc, stored.Description)
	assert.Equal(t, []store.TriggerMatchRule{
		{Dialect: "cel", Expr: celExprA},
		{Dialect: "system_one", Expr: s1Expr},
	}, stored.MatchRules)

	// 3. PATCH action only (changesFutureDispatch=true, req.Match=nil) -> 200, rules byte-identical.
	actionRec := f.doTrigger(t, f.ownerAuth, tr.ID, http.MethodPatch, UpdateTriggerRequest{
		Action: &TriggerActionSpec{Type: "message", AgentID: f.targetAgent.ID, Message: "updated-msg"},
	})
	require.Equal(t, http.StatusOK, actionRec.Code, actionRec.Body.String())
	stored, err = f.store.GetTrigger(ctx, tr.ID)
	require.NoError(t, err)
	assert.Equal(t, []store.TriggerMatchRule{
		{Dialect: "cel", Expr: celExprA},
		{Dialect: "system_one", Expr: s1Expr},
	}, stored.MatchRules)

	// 4. PATCH match editing CEL in place, system_one unchanged at same index -> 200.
	editCEL := []TriggerMatchRuleSpec{
		{Dialect: "cel", Expr: celExprB},
		{Dialect: "system_one", Expr: s1Expr},
	}
	editCELRec := f.doTrigger(t, f.ownerAuth, tr.ID, http.MethodPatch, UpdateTriggerRequest{
		Match: &TriggerMatchPatchSpec{Rules: &editCEL},
	})
	require.Equal(t, http.StatusOK, editCELRec.Code, editCELRec.Body.String())
	stored, err = f.store.GetTrigger(ctx, tr.ID)
	require.NoError(t, err)
	assert.Equal(t, []store.TriggerMatchRule{
		{Dialect: "cel", Expr: celExprB},
		{Dialect: "system_one", Expr: s1Expr},
	}, stored.MatchRules)

	// 5. Dialect normalization: whitespace around dialect name still matches stored;
	// whitespace added to expr does NOT match (expr is byte-identical, never trimmed).
	trimmedDialect := []TriggerMatchRuleSpec{
		{Dialect: "cel", Expr: celExprB},
		{Dialect: "  system_one  ", Expr: s1Expr},
	}
	require.Equal(t, http.StatusOK, f.doTrigger(t, f.ownerAuth, tr.ID, http.MethodPatch, UpdateTriggerRequest{
		Match: &TriggerMatchPatchSpec{Rules: &trimmedDialect},
	}).Code)

	paddedExpr := []TriggerMatchRuleSpec{
		{Dialect: "cel", Expr: celExprB},
		{Dialect: "system_one", Expr: " " + s1Expr},
	}
	paddedExprRec := f.doTrigger(t, f.ownerAuth, tr.ID, http.MethodPatch, UpdateTriggerRequest{
		Match: &TriggerMatchPatchSpec{Rules: &paddedExpr},
	})
	require.Equal(t, http.StatusBadRequest, paddedExprRec.Code, paddedExprRec.Body.String())
	assert.Equal(t, ruleengine.ReasonDialectNotAllowed, decodeTriggerRuleError(t, paddedExprRec).Details["reason"])

	// 6. User reorder-only PATCH ([cel_B, s1] -> [s1, cel_B]) -> 200, byte-identical modulo order.
	reordered := []TriggerMatchRuleSpec{
		{Dialect: "system_one", Expr: s1Expr},
		{Dialect: "cel", Expr: celExprB},
	}
	reorderRec := f.doTrigger(t, f.ownerAuth, tr.ID, http.MethodPatch, UpdateTriggerRequest{
		Match: &TriggerMatchPatchSpec{Rules: &reordered},
	})
	require.Equal(t, http.StatusOK, reorderRec.Code, reorderRec.Body.String())
	stored, err = f.store.GetTrigger(ctx, tr.ID)
	require.NoError(t, err)
	assert.Equal(t, []store.TriggerMatchRule{
		{Dialect: "system_one", Expr: s1Expr},
		{Dialect: "cel", Expr: celExprB},
	}, stored.MatchRules)

	// 7. User inserting a CEL row before system_one -> 200.
	insertBefore := []TriggerMatchRuleSpec{
		{Dialect: "cel", Expr: celExprC},
		{Dialect: "system_one", Expr: s1Expr},
		{Dialect: "cel", Expr: celExprB},
	}
	require.Equal(t, http.StatusOK, f.doTrigger(t, f.ownerAuth, tr.ID, http.MethodPatch, UpdateTriggerRequest{
		Match: &TriggerMatchPatchSpec{Rules: &insertBefore},
	}).Code)

	// 8. Duplicating a stored system_one element -> 400 dialect_not_allowed (bounded multiset).
	dupS1 := []TriggerMatchRuleSpec{
		{Dialect: "cel", Expr: celExprC},
		{Dialect: "system_one", Expr: s1Expr},
		{Dialect: "system_one", Expr: s1Expr},
	}
	dupRec := f.doTrigger(t, f.ownerAuth, tr.ID, http.MethodPatch, UpdateTriggerRequest{
		Match: &TriggerMatchPatchSpec{Rules: &dupS1},
	})
	require.Equal(t, http.StatusBadRequest, dupRec.Code, dupRec.Body.String())
	dupErr := decodeTriggerRuleError(t, dupRec)
	assert.Equal(t, ErrCodeInvalidRule, dupErr.Code)
	assert.Equal(t, float64(3), dupErr.Details["element"])
	assert.Equal(t, ruleengine.ReasonDialectNotAllowed, dupErr.Details["reason"])
	assert.Equal(t, ruleengine.ReasonDialectNotAllowed, dupErr.Details["category"])

	// 9. Adding a new system_one element -> 400 dialect_not_allowed.
	addS1 := []TriggerMatchRuleSpec{
		{Dialect: "cel", Expr: celExprC},
		{Dialect: "system_one", Expr: s1Expr},
		{Dialect: "system_one", Expr: s1Expr2},
	}
	addRec := f.doTrigger(t, f.ownerAuth, tr.ID, http.MethodPatch, UpdateTriggerRequest{
		Match: &TriggerMatchPatchSpec{Rules: &addS1},
	})
	require.Equal(t, http.StatusBadRequest, addRec.Code, addRec.Body.String())
	assert.Equal(t, ruleengine.ReasonDialectNotAllowed, decodeTriggerRuleError(t, addRec).Details["reason"])

	// 10. Editing system_one element's expr -> 400 dialect_not_allowed; stored trigger unchanged.
	editS1 := []TriggerMatchRuleSpec{
		{Dialect: "cel", Expr: celExprC},
		{Dialect: "system_one", Expr: s1Expr2},
		{Dialect: "cel", Expr: celExprB},
	}
	editS1Rec := f.doTrigger(t, f.ownerAuth, tr.ID, http.MethodPatch, UpdateTriggerRequest{
		Match: &TriggerMatchPatchSpec{Rules: &editS1},
	})
	require.Equal(t, http.StatusBadRequest, editS1Rec.Code, editS1Rec.Body.String())
	editS1Err := decodeTriggerRuleError(t, editS1Rec)
	assert.Equal(t, float64(2), editS1Err.Details["element"])
	assert.Equal(t, ruleengine.ReasonDialectNotAllowed, editS1Err.Details["reason"])

	// 11. Removing a non-admitted system_one element -> 200.
	removeS1 := []TriggerMatchRuleSpec{
		{Dialect: "cel", Expr: celExprC},
		{Dialect: "cel", Expr: celExprB},
	}
	removeRec := f.doTrigger(t, f.ownerAuth, tr.ID, http.MethodPatch, UpdateTriggerRequest{
		Match: &TriggerMatchPatchSpec{Rules: &removeS1},
	})
	require.Equal(t, http.StatusOK, removeRec.Code, removeRec.Body.String())
	stored, err = f.store.GetTrigger(ctx, tr.ID)
	require.NoError(t, err)
	assert.Equal(t, []store.TriggerMatchRule{
		{Dialect: "cel", Expr: celExprC},
		{Dialect: "cel", Expr: celExprB},
	}, stored.MatchRules)

	// 12. Shape/size/control-character checks STILL run on unchanged stored system_one elements.
	ctrlExpr := "bad\u202eexpr"
	trCtrl := seedStoredTriggerWithRules(t, f, ep.ID, "j16c-ctrl", store.TriggerStatusActive, []store.TriggerMatchRule{
		{Dialect: "cel", Expr: celExprA},
		{Dialect: "system_one", Expr: ctrlExpr},
	}, f.owner.ID, "user", f.owner.ID)
	keepCtrl := []TriggerMatchRuleSpec{
		{Dialect: "cel", Expr: celExprB},
		{Dialect: "system_one", Expr: ctrlExpr},
	}
	ctrlRec := f.doTrigger(t, f.ownerAuth, trCtrl.ID, http.MethodPatch, UpdateTriggerRequest{
		Match: &TriggerMatchPatchSpec{Rules: &keepCtrl},
	})
	require.Equal(t, http.StatusBadRequest, ctrlRec.Code, ctrlRec.Body.String())
	ctrlErr := decodeTriggerRuleError(t, ctrlRec)
	assert.Equal(t, "control_characters", ctrlErr.Details["category"])
	assert.Equal(t, float64(2), ctrlErr.Details["element"])

	oversizeExpr := strings.Repeat("a", triggerrule.MaxMatchRuleBytes+1)
	trBig := seedStoredTriggerWithRules(t, f, ep.ID, "j16c-oversize", store.TriggerStatusActive, []store.TriggerMatchRule{
		{Dialect: "cel", Expr: celExprA},
		{Dialect: "system_one", Expr: oversizeExpr},
	}, f.owner.ID, "user", f.owner.ID)
	keepBig := []TriggerMatchRuleSpec{
		{Dialect: "cel", Expr: celExprB},
		{Dialect: "system_one", Expr: oversizeExpr},
	}
	bigRec := f.doTrigger(t, f.ownerAuth, trBig.ID, http.MethodPatch, UpdateTriggerRequest{
		Match: &TriggerMatchPatchSpec{Rules: &keepBig},
	})
	require.Equal(t, http.StatusBadRequest, bigRec.Code, bigRec.Body.String())
	bigErr := decodeTriggerRuleError(t, bigRec)
	assert.Equal(t, "size", bigErr.Details["category"])
	assert.Equal(t, float64(2), bigErr.Details["element"])

	// 13. Resume keeps full re-admission and returns precise body shape
	// invalid_rule {element: i, category: dialect_not_allowed, dialect, reason}.
	pausedS1 := seedStoredTriggerWithRules(t, f, ep.ID, "j16c-paused-s1", store.TriggerStatusPaused, []store.TriggerMatchRule{
		{Dialect: "cel", Expr: celExprA},
		{Dialect: "system_one", Expr: s1Expr},
	}, f.owner.ID, "user", f.owner.ID)
	resumeRec := f.doTrigger(t, f.ownerAuth, pausedS1.ID+"/resume", http.MethodPost, nil)
	require.Equal(t, http.StatusBadRequest, resumeRec.Code, resumeRec.Body.String())
	resumeErr := decodeTriggerRuleError(t, resumeRec)
	assert.Equal(t, ErrCodeInvalidRule, resumeErr.Code)
	assert.Equal(t, "match", resumeErr.Details["field"])
	assert.Equal(t, float64(2), resumeErr.Details["element"])
	assert.Equal(t, ruleengine.ReasonDialectNotAllowed, resumeErr.Details["category"])
	assert.Equal(t, "system_one", resumeErr.Details["dialect"])
	assert.Equal(t, ruleengine.ReasonDialectNotAllowed, resumeErr.Details["reason"])
	rawElems, ok := resumeErr.Details["elements"].([]any)
	require.True(t, ok)
	require.Len(t, rawElems, 1)
	elem0 := rawElems[0].(map[string]any)
	assert.Equal(t, float64(2), elem0["element"])
	assert.Equal(t, ruleengine.ReasonDialectNotAllowed, elem0["category"])
	assert.Equal(t, "system_one", elem0["dialect"])
	assert.Equal(t, ruleengine.ReasonDialectNotAllowed, elem0["reason"])

	stillPaused, err := f.store.GetTrigger(ctx, pausedS1.ID)
	require.NoError(t, err)
	assert.Equal(t, store.TriggerStatusPaused, stillPaused.Status)
}

func TestTriggersAuthoring_PatchSkipsAdmissionForUnchangedElements(t *testing.T) {
	f := setupTriggersTestFixture(t)
	runPatchSkipsAdmissionForUnchangedElements(t, f)
}

func TestTriggersAuthoring_PatchSkipsAdmissionForUnchangedElements_Postgres(t *testing.T) {
	if !enttest.Active() {
		t.Skip("requires -tags integration and SCION_TEST_POSTGRES_URL")
	}
	client := enttest.NewClient(t)
	t.Logf("dialect=%s", client.Driver().Dialect())
	srv, s := testServerWithStore(t, entadapter.NewCompositeStore(client))
	f := setupTriggersTestFixtureOnServer(t, srv, s)
	runPatchSkipsAdmissionForUnchangedElements(t, f)
}

func TestTriggersAuthoring_AgentD2SystemOnePositionInvariance(t *testing.T) {
	f := setupTriggersTestFixture(t)

	epRec := f.doWebhookEndpoint(t, f.agentAAuth, "", http.MethodPost, CreateWebhookEndpointRequest{Name: "j16c-agent-d2-ep"})
	require.Equal(t, http.StatusCreated, epRec.Code, epRec.Body.String())
	var ep WebhookEndpointCreatedResponse
	require.NoError(t, json.Unmarshal(epRec.Body.Bytes(), &ep))

	const (
		celA = `event.attributes["branch"] == "main"`
		celB = `event.attributes["branch"] == "release"`
		celC = `has(event.payload.pr)`
		s1A  = `Does this PR touch authentication?`
		s1B  = `Does this PR touch billing?`
	)

	// HTTP-level guard: agent PATCH on a trigger holding system_one remains 403
	// (agentTriggerEditAllowed=false).
	agentTr := seedStoredTriggerWithRules(t, f, ep.ID, "j16c-agent-held", store.TriggerStatusActive, []store.TriggerMatchRule{
		{Dialect: "cel", Expr: celA},
		{Dialect: "system_one", Expr: s1A},
	}, f.agentA.ID, "agent", f.agentA.ID)
	agentEditCEL := []TriggerMatchRuleSpec{
		{Dialect: "cel", Expr: celB},
		{Dialect: "system_one", Expr: s1A},
	}
	httpPatchRec := f.doTrigger(t, f.agentAAuth, agentTr.ID, http.MethodPatch, UpdateTriggerRequest{
		Match: &TriggerMatchPatchSpec{Rules: &agentEditCEL},
	})
	assert.Equal(t, http.StatusForbidden, httpPatchRec.Code, httpPatchRec.Body.String())

	// Direct validation-seam tests for the D2 agent carve-out (final ruling):
	// for an agent PATCH, the ordered rule prefix from index 0 through and
	// including the last system_one element must be byte-identical to stored;
	// CEL rows strictly after the last system_one remain agent-editable.
	agentCtx := contextWithIdentity(context.Background(), f.agentAAuth)

	cases := []struct {
		name         string
		stored       []triggerrule.Rule
		submitted    []TriggerMatchRuleSpec
		wantOK       bool
		wantDialect  string
		wantCategory string
	}{
		{
			name: "pinned: agent [CEL(false), S1] -> [CEL(true), S1] -> 400 D2",
			stored: []triggerrule.Rule{
				{Dialect: "cel", Expr: "false"},
				{Dialect: "system_one", Expr: s1A},
			},
			submitted: []TriggerMatchRuleSpec{
				{Dialect: "cel", Expr: "true"},
				{Dialect: "system_one", Expr: s1A},
			},
			wantOK:       false,
			wantDialect:  "cel",
			wantCategory: triggerCategoryAgentSystemOnePrefixChanged,
		},
		{
			name: "pinned: agent [S1a, CEL(x), S1b] -> [S1a, CEL(y), S1b] -> 400 D2",
			stored: []triggerrule.Rule{
				{Dialect: "system_one", Expr: s1A},
				{Dialect: "cel", Expr: celA},
				{Dialect: "system_one", Expr: s1B},
			},
			submitted: []TriggerMatchRuleSpec{
				{Dialect: "system_one", Expr: s1A},
				{Dialect: "cel", Expr: celB},
				{Dialect: "system_one", Expr: s1B},
			},
			wantOK:       false,
			wantDialect:  "cel",
			wantCategory: triggerCategoryAgentSystemOnePrefixChanged,
		},
		{
			name: "pinned: [CEL, S1] -> [S1, CEL] by agent -> 400 D2",
			stored: []triggerrule.Rule{
				{Dialect: "cel", Expr: celA},
				{Dialect: "system_one", Expr: s1A},
			},
			submitted: []TriggerMatchRuleSpec{
				{Dialect: "system_one", Expr: s1A},
				{Dialect: "cel", Expr: celA},
			},
			wantOK:       false,
			wantDialect:  "system_one",
			wantCategory: triggerReasonAgentNotAllowed,
		},
		{
			name: "pinned: agent removing one of two identical S1 -> 400 D2",
			stored: []triggerrule.Rule{
				{Dialect: "system_one", Expr: s1A},
				{Dialect: "system_one", Expr: s1A},
			},
			submitted: []TriggerMatchRuleSpec{
				{Dialect: "system_one", Expr: s1A},
			},
			wantOK:       false,
			wantDialect:  "system_one",
			wantCategory: triggerReasonAgentNotAllowed,
		},
		{
			name: "pinned: agent swapping two identical S1 -> 200 byte-identical",
			stored: []triggerrule.Rule{
				{Dialect: "system_one", Expr: s1A},
				{Dialect: "system_one", Expr: s1A},
			},
			submitted: []TriggerMatchRuleSpec{
				{Dialect: "system_one", Expr: s1A},
				{Dialect: "system_one", Expr: s1A},
			},
			wantOK: true,
		},
		{
			name: "positive control: agent appending CEL after last S1 -> 200",
			stored: []triggerrule.Rule{
				{Dialect: "cel", Expr: celA},
				{Dialect: "system_one", Expr: s1A},
			},
			submitted: []TriggerMatchRuleSpec{
				{Dialect: "cel", Expr: celA},
				{Dialect: "system_one", Expr: s1A},
				{Dialect: "cel", Expr: celC},
			},
			wantOK: true,
		},
		{
			name: "positive control: agent editing CEL after last S1 -> 200",
			stored: []triggerrule.Rule{
				{Dialect: "cel", Expr: celA},
				{Dialect: "system_one", Expr: s1A},
				{Dialect: "cel", Expr: celB},
			},
			submitted: []TriggerMatchRuleSpec{
				{Dialect: "cel", Expr: celA},
				{Dialect: "system_one", Expr: s1A},
				{Dialect: "cel", Expr: celC},
			},
			wantOK: true,
		},
		{
			name: "positive control: agent removing CEL after last S1 -> 200",
			stored: []triggerrule.Rule{
				{Dialect: "cel", Expr: celA},
				{Dialect: "system_one", Expr: s1A},
				{Dialect: "cel", Expr: celB},
			},
			submitted: []TriggerMatchRuleSpec{
				{Dialect: "cel", Expr: celA},
				{Dialect: "system_one", Expr: s1A},
			},
			wantOK: true,
		},
		{
			name: "insert CEL before S1 -> 400 D2",
			stored: []triggerrule.Rule{
				{Dialect: "cel", Expr: celA},
				{Dialect: "system_one", Expr: s1A},
			},
			submitted: []TriggerMatchRuleSpec{
				{Dialect: "cel", Expr: celC},
				{Dialect: "cel", Expr: celA},
				{Dialect: "system_one", Expr: s1A},
			},
			wantOK:       false,
			wantDialect:  "system_one",
			wantCategory: triggerReasonAgentNotAllowed,
		},
		{
			name: "remove CEL before S1 -> 400 D2",
			stored: []triggerrule.Rule{
				{Dialect: "cel", Expr: celA},
				{Dialect: "system_one", Expr: s1A},
			},
			submitted: []TriggerMatchRuleSpec{
				{Dialect: "system_one", Expr: s1A},
			},
			wantOK:       false,
			wantDialect:  "system_one",
			wantCategory: triggerReasonAgentNotAllowed,
		},
		{
			name: "agent editing S1 expr -> 400 D2",
			stored: []triggerrule.Rule{
				{Dialect: "cel", Expr: celA},
				{Dialect: "system_one", Expr: s1A},
			},
			submitted: []TriggerMatchRuleSpec{
				{Dialect: "cel", Expr: celA},
				{Dialect: "system_one", Expr: s1B},
			},
			wantOK:       false,
			wantDialect:  "system_one",
			wantCategory: triggerReasonAgentNotAllowed,
		},
		{
			name: "agent removing S1 -> 400 D2",
			stored: []triggerrule.Rule{
				{Dialect: "cel", Expr: celA},
				{Dialect: "system_one", Expr: s1A},
			},
			submitted: []TriggerMatchRuleSpec{
				{Dialect: "cel", Expr: celA},
			},
			wantOK:       false,
			wantDialect:  "system_one",
			wantCategory: triggerReasonAgentNotAllowed,
		},
		{
			name: "agent clearing rules removes S1 -> 400 D2",
			stored: []triggerrule.Rule{
				{Dialect: "system_one", Expr: s1A},
			},
			submitted:    []TriggerMatchRuleSpec{},
			wantOK:       false,
			wantDialect:  "system_one",
			wantCategory: triggerReasonAgentNotAllowed,
		},
		{
			name: "agent adding S1 -> 400 D2",
			stored: []triggerrule.Rule{
				{Dialect: "cel", Expr: celA},
			},
			submitted: []TriggerMatchRuleSpec{
				{Dialect: "cel", Expr: celA},
				{Dialect: "system_one", Expr: s1A},
			},
			wantOK:       false,
			wantDialect:  "system_one",
			wantCategory: triggerReasonAgentNotAllowed,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			_, _, ok := f.srv.writeAndValidateTriggerMatch(rec, agentCtx, &TriggerMatchSpec{
				Types: []string{"webhook.received"},
				Rules: tc.submitted,
			}, triggerMatchValidationOpts{
				allowStoredAdmissionSkip: true,
				storedRules:              tc.stored,
			})
			if tc.wantOK {
				assert.True(t, ok, "expected validation to pass; body=%s", rec.Body.String())
			} else {
				assert.False(t, ok, "expected D2 refusal")
				require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
				eb := decodeTriggerRuleError(t, rec)
				assert.Equal(t, ErrCodeInvalidRule, eb.Code)
				assert.Equal(t, tc.wantDialect, eb.Details["dialect"])
				assert.Equal(t, triggerReasonAgentNotAllowed, eb.Details["reason"])
				assert.Equal(t, tc.wantCategory, eb.Details["category"])
			}
		})
	}
}

type countingSystemOneDialect struct {
	compileCalls atomic.Int32
}

func (d *countingSystemOneDialect) Name() string { return "system_one" }

func (d *countingSystemOneDialect) Compile(_ context.Context, rule string, kind ruleengine.Kind, env ruleengine.EnvironmentRef) (ruleengine.Compiled, error) {
	d.compileCalls.Add(1)
	return ruleengine.NewCompiled(env.Name, "system_one", kind, rule), nil
}

func (d *countingSystemOneDialect) Evaluate(_ context.Context, _ ruleengine.Compiled, _ ruleengine.Input, _ ruleengine.Budget) (ruleengine.Decision, error) {
	return ruleengine.Decision{Kind: ruleengine.KindFilter, Bool: true}, nil
}

func (d *countingSystemOneDialect) Declares() ruleengine.Capabilities {
	return ruleengine.Capabilities{MaxEvalTime: time.Second}
}

func installGrantedCountingSystemOneDialect(t *testing.T, f *triggersTestFixture) *countingSystemOneDialect {
	t.Helper()
	fakeS1 := &countingSystemOneDialect{}
	eng := ruleengine.NewEngine()
	require.NoError(t, eng.Register(f.srv.celDialect))
	require.NoError(t, eng.Register(fakeS1))
	require.NoError(t, eng.AllowConsumerDialects(
		triggerrule.ConsumerName,
		ruleengine.Grant(ruleengine.DialectCEL),
		ruleengine.Grant("system_one"),
	))
	trRules, err := triggerrule.New(eng)
	require.NoError(t, err)

	origRules, origTriggerRules := f.srv.rules, f.srv.triggerRules
	f.srv.rules = eng
	f.srv.triggerRules = trRules
	t.Cleanup(func() {
		f.srv.rules = origRules
		f.srv.triggerRules = origTriggerRules
	})
	return fakeS1
}

// TestTriggersAuthoring_GrantedSystemOneHookRunsOnCreateAndResume verifies O1:
// authorizeNewOrChangedTriggerMatchElement runs on create and resume (not only
// PATCH), so even when system_one is granted, an agent POST create or resume
// with system_one returns 400 agent_not_allowed while user create/resume passes.
func TestTriggersAuthoring_GrantedSystemOneHookRunsOnCreateAndResume(t *testing.T) {
	f := setupTriggersTestFixture(t)
	fakeS1 := installGrantedCountingSystemOneDialect(t, f)

	agentEpRec := f.doWebhookEndpoint(t, f.agentAAuth, "", http.MethodPost, CreateWebhookEndpointRequest{Name: "o1-agent-ep"})
	require.Equal(t, http.StatusCreated, agentEpRec.Code, agentEpRec.Body.String())
	var agentEp WebhookEndpointCreatedResponse
	require.NoError(t, json.Unmarshal(agentEpRec.Body.Bytes(), &agentEp))

	ownerEpRec := f.doWebhookEndpoint(t, f.ownerAuth, "", http.MethodPost, CreateWebhookEndpointRequest{Name: "o1-owner-ep"})
	require.Equal(t, http.StatusCreated, ownerEpRec.Code, ownerEpRec.Body.String())
	var ownerEp WebhookEndpointCreatedResponse
	require.NoError(t, json.Unmarshal(ownerEpRec.Body.Bytes(), &ownerEp))

	// 1. Agent POST create with system_one -> 400 agent_not_allowed, 0 compile calls.
	agentCreateRec := f.doTrigger(t, f.agentAAuth, "", http.MethodPost, CreateTriggerRequest{
		Name:   "o1-agent-create-s1",
		Source: TriggerSourceSpec{Kind: store.TriggerSourceKindWebhook, ID: agentEp.ID},
		Match: &TriggerMatchSpec{Rules: []TriggerMatchRuleSpec{
			{Dialect: "cel", Expr: "true"},
			{Dialect: "system_one", Expr: "Does this PR touch authentication?"},
		}},
		Action: TriggerActionSpec{Type: "message", AgentID: f.targetAgent.ID, Message: "hi"},
	})
	require.Equal(t, http.StatusBadRequest, agentCreateRec.Code, agentCreateRec.Body.String())
	agentCreateErr := decodeTriggerRuleError(t, agentCreateRec)
	assert.Equal(t, ErrCodeInvalidRule, agentCreateErr.Code)
	assert.Equal(t, float64(2), agentCreateErr.Details["element"])
	assert.Equal(t, "system_one", agentCreateErr.Details["dialect"])
	assert.Equal(t, triggerReasonAgentNotAllowed, agentCreateErr.Details["reason"])
	assert.Equal(t, triggerReasonAgentNotAllowed, agentCreateErr.Details["category"])
	assert.Equal(t, int32(0), fakeS1.compileCalls.Load())

	// 2. User POST create with system_one -> 201 Created, passes hook and compiles once.
	userCreateRec := f.doTrigger(t, f.ownerAuth, "", http.MethodPost, CreateTriggerRequest{
		Name:   "o1-owner-create-s1",
		Source: TriggerSourceSpec{Kind: store.TriggerSourceKindWebhook, ID: ownerEp.ID},
		Match: &TriggerMatchSpec{Rules: []TriggerMatchRuleSpec{
			{Dialect: "cel", Expr: "true"},
			{Dialect: "system_one", Expr: "Does this PR touch authentication?"},
		}},
		Action: TriggerActionSpec{Type: "message", AgentID: f.targetAgent.ID, Message: "hi"},
	})
	require.Equal(t, http.StatusCreated, userCreateRec.Code, userCreateRec.Body.String())
	assert.Equal(t, int32(1), fakeS1.compileCalls.Load())

	// 3. Agent POST resume on a paused agent-held trigger with stored system_one -> 400 agent_not_allowed.
	pausedAgentTr := seedStoredTriggerWithRules(t, f, agentEp.ID, "o1-agent-paused-s1", store.TriggerStatusPaused, []store.TriggerMatchRule{
		{Dialect: "cel", Expr: "true"},
		{Dialect: "system_one", Expr: "Does this PR touch authentication?"},
	}, f.agentA.ID, "agent", f.agentA.ID)
	agentResumeRec := f.doTrigger(t, f.agentAAuth, pausedAgentTr.ID+"/resume", http.MethodPost, nil)
	require.Equal(t, http.StatusBadRequest, agentResumeRec.Code, agentResumeRec.Body.String())
	agentResumeErr := decodeTriggerRuleError(t, agentResumeRec)
	assert.Equal(t, ErrCodeInvalidRule, agentResumeErr.Code)
	assert.Equal(t, triggerReasonAgentNotAllowed, agentResumeErr.Details["reason"])
	assert.Equal(t, int32(1), fakeS1.compileCalls.Load(), "agent resume refused before compiling system_one")

	// 4. User POST resume on a paused user trigger with stored system_one -> 200 OK, compiles element.
	pausedOwnerTr := seedStoredTriggerWithRules(t, f, ownerEp.ID, "o1-owner-paused-s1", store.TriggerStatusPaused, []store.TriggerMatchRule{
		{Dialect: "cel", Expr: "true"},
		{Dialect: "system_one", Expr: "Does this PR touch authentication?"},
	}, f.owner.ID, "user", f.owner.ID)
	ownerResumeRec := f.doTrigger(t, f.ownerAuth, pausedOwnerTr.ID+"/resume", http.MethodPost, nil)
	require.Equal(t, http.StatusOK, ownerResumeRec.Code, ownerResumeRec.Body.String())
	assert.Equal(t, int32(2), fakeS1.compileCalls.Load())
}

// TestTriggersAuthoring_GrantedSystemOneZeroCompileCallsOnUnchangedPatch
// verifies N3: with a granted system_one dialect counting compile calls,
// patching description, action, or only the CEL row of a stored [cel, system_one]
// trigger returns 200 with 0 compile calls for the unchanged system_one element,
// while editing the system_one element does invoke Compile.
func TestTriggersAuthoring_GrantedSystemOneZeroCompileCallsOnUnchangedPatch(t *testing.T) {
	f := setupTriggersTestFixture(t)
	fakeS1 := installGrantedCountingSystemOneDialect(t, f)

	epRec := f.doWebhookEndpoint(t, f.ownerAuth, "", http.MethodPost, CreateWebhookEndpointRequest{Name: "n3-counting-ep"})
	require.Equal(t, http.StatusCreated, epRec.Code, epRec.Body.String())
	var ep WebhookEndpointCreatedResponse
	require.NoError(t, json.Unmarshal(epRec.Body.Bytes(), &ep))

	const (
		celA = `event.attributes["branch"] == "main"`
		celB = `event.attributes["branch"] == "release"`
		s1A  = `Does this PR touch authentication?`
		s1B  = `Does this PR touch billing?`
	)

	tr := seedStoredTriggerWithRules(t, f, ep.ID, "n3-mixed-s1", store.TriggerStatusActive, []store.TriggerMatchRule{
		{Dialect: "cel", Expr: celA},
		{Dialect: "system_one", Expr: s1A},
	}, f.owner.ID, "user", f.owner.ID)
	require.Equal(t, int32(0), fakeS1.compileCalls.Load())

	// 1. PATCH description only -> 200, 0 compile calls.
	newDesc := "n3-updated-desc"
	require.Equal(t, http.StatusOK, f.doTrigger(t, f.ownerAuth, tr.ID, http.MethodPatch, UpdateTriggerRequest{
		Description: &newDesc,
	}).Code)
	assert.Equal(t, int32(0), fakeS1.compileCalls.Load())

	// 2. PATCH action only (changesFutureDispatch=true, req.Match=nil) -> 200, 0 compile calls.
	require.Equal(t, http.StatusOK, f.doTrigger(t, f.ownerAuth, tr.ID, http.MethodPatch, UpdateTriggerRequest{
		Action: &TriggerActionSpec{Type: "message", AgentID: f.targetAgent.ID, Message: "n3-msg"},
	}).Code)
	assert.Equal(t, int32(0), fakeS1.compileCalls.Load())

	// 3. PATCH match editing CEL only, system_one unchanged -> 200, 0 compile calls for element 2.
	editCEL := []TriggerMatchRuleSpec{
		{Dialect: "cel", Expr: celB},
		{Dialect: "system_one", Expr: s1A},
	}
	require.Equal(t, http.StatusOK, f.doTrigger(t, f.ownerAuth, tr.ID, http.MethodPatch, UpdateTriggerRequest{
		Match: &TriggerMatchPatchSpec{Rules: &editCEL},
	}).Code)
	assert.Equal(t, int32(0), fakeS1.compileCalls.Load())

	// 4. Positive control: PATCH editing system_one expr -> 200, 1 compile call.
	editS1 := []TriggerMatchRuleSpec{
		{Dialect: "cel", Expr: celB},
		{Dialect: "system_one", Expr: s1B},
	}
	require.Equal(t, http.StatusOK, f.doTrigger(t, f.ownerAuth, tr.ID, http.MethodPatch, UpdateTriggerRequest{
		Match: &TriggerMatchPatchSpec{Rules: &editS1},
	}).Code)
	assert.Equal(t, int32(1), fakeS1.compileCalls.Load())
}

func TestTriggersAuthoring_PatchUnchangedNonAdmitted_PerPrincipalMatrix(t *testing.T) {
	f := setupTriggersTestFixture(t)

	epRec := f.doWebhookEndpoint(t, f.ownerAuth, "", http.MethodPost, CreateWebhookEndpointRequest{Name: "j16c-matrix-ep"})
	require.Equal(t, http.StatusCreated, epRec.Code, epRec.Body.String())
	var ep WebhookEndpointCreatedResponse
	require.NoError(t, json.Unmarshal(epRec.Body.Bytes(), &ep))

	const (
		celA = `event.attributes["branch"] == "main"`
		celB = `event.attributes["branch"] == "release"`
		s1A  = `Does this PR touch authentication?`
		s1B  = `Does this PR touch billing?`
	)

	projBoundary := TokenBoundary{Kind: BoundaryKindProject, ProjectID: f.projectID}
	wrongProjBoundary := TokenBoundary{Kind: BoundaryKindProject, ProjectID: f.otherProjID}
	uatFull := scheduleTestUAT(t, f.ownerAuth, projBoundary, "trigger:update", "trigger:create", "webhook_endpoint:read", "agent:message")
	uatUpdateOnly := scheduleTestUAT(t, f.ownerAuth, projBoundary, "trigger:update", "webhook_endpoint:read", "agent:message")
	uatNoUpdate := scheduleTestUAT(t, f.ownerAuth, projBoundary, "trigger:read", "webhook_endpoint:read", "agent:message")
	uatWrongProj := scheduleTestUAT(t, f.ownerAuth, wrongProjBoundary, "trigger:update", "trigger:create", "webhook_endpoint:read", "agent:message")

	principals := []struct {
		name               string
		auth               Identity
		wantKeepS1Status   int
		wantKeepS1Category string
		wantMutateS1Status int
	}{
		{name: "owner", auth: f.ownerAuth, wantKeepS1Status: http.StatusOK, wantMutateS1Status: http.StatusBadRequest},
		{name: "admin", auth: f.adminAuth, wantKeepS1Status: http.StatusOK, wantMutateS1Status: http.StatusBadRequest},
		{name: "member", auth: f.memberAuth, wantKeepS1Status: http.StatusForbidden, wantMutateS1Status: http.StatusForbidden},
		{name: "viewer", auth: f.viewerAuth, wantKeepS1Status: http.StatusForbidden, wantMutateS1Status: http.StatusForbidden},
		{name: "agent_a", auth: f.agentAAuth, wantKeepS1Status: http.StatusForbidden, wantMutateS1Status: http.StatusForbidden},
		{name: "uat_update_and_create", auth: uatFull, wantKeepS1Status: http.StatusOK, wantMutateS1Status: http.StatusBadRequest},
		{name: "uat_update_only", auth: uatUpdateOnly, wantKeepS1Status: http.StatusBadRequest, wantKeepS1Category: triggerCategorySystemOneAuthority, wantMutateS1Status: http.StatusBadRequest},
		{name: "uat_no_update", auth: uatNoUpdate, wantKeepS1Status: http.StatusForbidden, wantMutateS1Status: http.StatusForbidden},
		{name: "uat_wrong_proj", auth: uatWrongProj, wantKeepS1Status: http.StatusForbidden, wantMutateS1Status: http.StatusForbidden},
	}

	for _, p := range principals {
		t.Run(p.name, func(t *testing.T) {
			tr := seedStoredTriggerWithRules(t, f, ep.ID, "j16c-p-"+p.name, store.TriggerStatusActive, []store.TriggerMatchRule{
				{Dialect: "cel", Expr: celA},
				{Dialect: "system_one", Expr: s1A},
			}, f.owner.ID, "user", f.owner.ID)

			keepS1 := []TriggerMatchRuleSpec{
				{Dialect: "cel", Expr: celB},
				{Dialect: "system_one", Expr: s1A},
			}
			keepRec := f.doTrigger(t, p.auth, tr.ID, http.MethodPatch, UpdateTriggerRequest{
				Match: &TriggerMatchPatchSpec{Rules: &keepS1},
			})
			assert.Equal(t, p.wantKeepS1Status, keepRec.Code, "keep S1 body=%s", keepRec.Body.String())
			if p.wantKeepS1Category != "" {
				eb := decodeTriggerRuleError(t, keepRec)
				assert.Equal(t, ErrCodeInvalidRule, eb.Code)
				assert.Equal(t, p.wantKeepS1Category, eb.Details["category"])
			}

			mutateS1 := []TriggerMatchRuleSpec{
				{Dialect: "cel", Expr: celB},
				{Dialect: "system_one", Expr: s1B},
			}
			mutRec := f.doTrigger(t, p.auth, tr.ID, http.MethodPatch, UpdateTriggerRequest{
				Match: &TriggerMatchPatchSpec{Rules: &mutateS1},
			})
			assert.Equal(t, p.wantMutateS1Status, mutRec.Code, "mutate S1 body=%s", mutRec.Body.String())
			if p.wantMutateS1Status == http.StatusBadRequest {
				assert.Equal(t, ruleengine.ReasonDialectNotAllowed, decodeTriggerRuleError(t, mutRec).Details["reason"])
			}
		})
	}
}

// TestTriggersAuthoring_SystemOneReattributionAuthority verifies
// mixed-rule-arrays-design.md §11.6:
//   - a trigger:update-only UAT re-attributing an S1-bearing trigger -> 400 system_one_authority;
//   - a non-re-attributing description PATCH by that UAT -> 200;
//   - owner/admin (holding trigger.create) -> 200;
//   - an agent re-attribution -> 400 agent_not_allowed (via direct helper, since HTTP refuses agents today);
//   - re-attribution of a CEL-only trigger -> unchanged (200).
func TestTriggersAuthoring_SystemOneReattributionAuthority(t *testing.T) {
	f := setupTriggersTestFixture(t)
	ctx := context.Background()

	epRec := f.doWebhookEndpoint(t, f.ownerAuth, "", http.MethodPost, CreateWebhookEndpointRequest{Name: "s116-reattrib-ep"})
	require.Equal(t, http.StatusCreated, epRec.Code, epRec.Body.String())
	var ep WebhookEndpointCreatedResponse
	require.NoError(t, json.Unmarshal(epRec.Body.Bytes(), &ep))

	const (
		celA = `event.attributes["branch"] == "main"`
		celB = `event.attributes["branch"] == "release"`
		s1A  = `Does this PR touch authentication?`
	)

	projBoundary := TokenBoundary{Kind: BoundaryKindProject, ProjectID: f.projectID}
	uatUpdateOnly := scheduleTestUAT(t, f.ownerAuth, projBoundary, "trigger:update", "webhook_endpoint:read", "agent:message")
	uatUpdateAndCreate := scheduleTestUAT(t, f.ownerAuth, projBoundary, "trigger:update", "trigger:create", "webhook_endpoint:read", "agent:message")

	s1Tr := seedStoredTriggerWithRules(t, f, ep.ID, "s116-s1-tr", store.TriggerStatusActive, []store.TriggerMatchRule{
		{Dialect: "cel", Expr: celA},
		{Dialect: "system_one", Expr: s1A},
	}, f.owner.ID, "user", f.owner.ID)

	// 1. trigger:update-only UAT re-attributing via match edit -> 400 system_one_authority, stored trigger unchanged.
	editCEL := []TriggerMatchRuleSpec{
		{Dialect: "cel", Expr: celB},
		{Dialect: "system_one", Expr: s1A},
	}
	recMatch := f.doTrigger(t, uatUpdateOnly, s1Tr.ID, http.MethodPatch, UpdateTriggerRequest{
		Match: &TriggerMatchPatchSpec{Rules: &editCEL},
	})
	require.Equal(t, http.StatusBadRequest, recMatch.Code, recMatch.Body.String())
	eb := decodeTriggerRuleError(t, recMatch)
	assert.Equal(t, ErrCodeInvalidRule, eb.Code)
	assert.Equal(t, triggerCategorySystemOneAuthority, eb.Details["category"])
	assert.Equal(t, triggerMsgSystemOneReattributionRequires, eb.Details["message"])
	assert.Equal(t, float64(2), eb.Details["element"])
	assert.Equal(t, "system_one", eb.Details["dialect"])

	// 2. trigger:update-only UAT re-attributing via action edit (req.Match == nil) -> 400 system_one_authority.
	recAction := f.doTrigger(t, uatUpdateOnly, s1Tr.ID, http.MethodPatch, UpdateTriggerRequest{
		Action: &TriggerActionSpec{Type: "message", AgentID: f.targetAgent.ID, Message: "new-msg"},
	})
	require.Equal(t, http.StatusBadRequest, recAction.Code, recAction.Body.String())
	assert.Equal(t, triggerCategorySystemOneAuthority, decodeTriggerRuleError(t, recAction).Details["category"])

	storedAfterRefusal, err := f.store.GetTrigger(ctx, s1Tr.ID)
	require.NoError(t, err)
	assert.Equal(t, 1, storedAfterRefusal.AuthorizationRevision, "refused re-attribution must not mutate stored trigger")

	// 3. Non-re-attributing description PATCH by trigger:update-only UAT -> 200 (no re-attribution).
	newDesc := "desc-without-reattrib"
	recDesc := f.doTrigger(t, uatUpdateOnly, s1Tr.ID, http.MethodPatch, UpdateTriggerRequest{
		Description: &newDesc,
	})
	require.Equal(t, http.StatusOK, recDesc.Code, recDesc.Body.String())

	// 4. Owner, admin, and trigger:update+trigger:create UAT -> 200.
	require.Equal(t, http.StatusOK, f.doTrigger(t, f.ownerAuth, s1Tr.ID, http.MethodPatch, UpdateTriggerRequest{
		Match: &TriggerMatchPatchSpec{Rules: &editCEL},
	}).Code)
	require.Equal(t, http.StatusOK, f.doTrigger(t, f.adminAuth, s1Tr.ID, http.MethodPatch, UpdateTriggerRequest{
		Action: &TriggerActionSpec{Type: "message", AgentID: f.targetAgent.ID, Message: "admin-msg"},
	}).Code)
	require.Equal(t, http.StatusOK, f.doTrigger(t, uatUpdateAndCreate, s1Tr.ID, http.MethodPatch, UpdateTriggerRequest{
		Action: &TriggerActionSpec{Type: "message", AgentID: f.targetAgent.ID, Message: "uat-msg"},
	}).Code)

	// 5. Agent re-attribution on S1-bearing trigger -> 400 D2 (agent_not_allowed), via direct helper.
	agentCtx := contextWithIdentity(ctx, f.agentAAuth)
	agentRec := httptest.NewRecorder()
	ok := f.srv.authorizeTriggerSystemOneReattribution(agentRec, agentCtx, f.projectID, []triggerrule.Rule{
		{Dialect: "cel", Expr: celA},
		{Dialect: "system_one", Expr: s1A},
	})
	assert.False(t, ok)
	require.Equal(t, http.StatusBadRequest, agentRec.Code, agentRec.Body.String())
	agentErr := decodeTriggerRuleError(t, agentRec)
	assert.Equal(t, ErrCodeInvalidRule, agentErr.Code)
	assert.Equal(t, triggerReasonAgentNotAllowed, agentErr.Details["reason"])
	assert.Equal(t, triggerReasonAgentNotAllowed, agentErr.Details["category"])
	assert.Equal(t, "system_one", agentErr.Details["dialect"])

	// 6. Re-attribution of a CEL-only trigger -> unchanged behavior (200 for trigger:update-only UAT, allowed for agent helper).
	celTr := seedStoredTriggerWithRules(t, f, ep.ID, "s116-cel-only", store.TriggerStatusActive, []store.TriggerMatchRule{
		{Dialect: "cel", Expr: celA},
	}, f.owner.ID, "user", f.owner.ID)
	editCELOnly := []TriggerMatchRuleSpec{{Dialect: "cel", Expr: celB}}
	recCEL := f.doTrigger(t, uatUpdateOnly, celTr.ID, http.MethodPatch, UpdateTriggerRequest{
		Match: &TriggerMatchPatchSpec{Rules: &editCELOnly},
	})
	require.Equal(t, http.StatusOK, recCEL.Code, recCEL.Body.String())

	agentCELRec := httptest.NewRecorder()
	assert.True(t, f.srv.authorizeTriggerSystemOneReattribution(agentCELRec, agentCtx, f.projectID, []triggerrule.Rule{
		{Dialect: "cel", Expr: celB},
	}))
}

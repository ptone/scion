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
		name      string
		stored    []triggerrule.Rule
		submitted []TriggerMatchRuleSpec
		wantOK    bool
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
			wantOK: false,
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
			wantOK: false,
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
			wantOK: false,
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
			wantOK: false,
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
			wantOK: false,
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
			wantOK: false,
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
			wantOK: false,
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
			wantOK: false,
		},
		{
			name: "agent clearing rules removes S1 -> 400 D2",
			stored: []triggerrule.Rule{
				{Dialect: "system_one", Expr: s1A},
			},
			submitted: []TriggerMatchRuleSpec{},
			wantOK:    false,
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
			wantOK: false,
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
				assert.Equal(t, "system_one", eb.Details["dialect"])
				assert.Equal(t, triggerReasonAgentNotAllowed, eb.Details["reason"])
				assert.Equal(t, triggerReasonAgentNotAllowed, eb.Details["category"])
			}
		})
	}
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
	uatFull := scheduleTestUAT(t, f.ownerAuth, projBoundary, "trigger:update", "webhook_endpoint:read", "agent:message")
	uatNoUpdate := scheduleTestUAT(t, f.ownerAuth, projBoundary, "trigger:read", "webhook_endpoint:read", "agent:message")
	uatWrongProj := scheduleTestUAT(t, f.ownerAuth, wrongProjBoundary, "trigger:update", "webhook_endpoint:read", "agent:message")

	principals := []struct {
		name               string
		auth               Identity
		wantKeepS1Status   int
		wantMutateS1Status int
	}{
		{name: "owner", auth: f.ownerAuth, wantKeepS1Status: http.StatusOK, wantMutateS1Status: http.StatusBadRequest},
		{name: "admin", auth: f.adminAuth, wantKeepS1Status: http.StatusOK, wantMutateS1Status: http.StatusBadRequest},
		{name: "member", auth: f.memberAuth, wantKeepS1Status: http.StatusForbidden, wantMutateS1Status: http.StatusForbidden},
		{name: "viewer", auth: f.viewerAuth, wantKeepS1Status: http.StatusForbidden, wantMutateS1Status: http.StatusForbidden},
		{name: "agent_a", auth: f.agentAAuth, wantKeepS1Status: http.StatusForbidden, wantMutateS1Status: http.StatusForbidden},
		{name: "uat_full", auth: uatFull, wantKeepS1Status: http.StatusOK, wantMutateS1Status: http.StatusBadRequest},
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

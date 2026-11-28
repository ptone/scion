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

package rulefacts_test

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/GoogleCloudPlatform/scion/pkg/celengine"
	"github.com/GoogleCloudPlatform/scion/pkg/messages"
	"github.com/GoogleCloudPlatform/scion/pkg/messaging"
	"github.com/GoogleCloudPlatform/scion/pkg/rulefacts"
)

// walkStrings visits every string value recursively inside v (maps, slices),
// reporting its dotted path to fn.
func walkStrings(path string, v any, fn func(path, val string)) {
	switch x := v.(type) {
	case string:
		fn(path, x)
	case []string:
		for i, s := range x {
			fn(path+"["+strconv.Itoa(i)+"]", s)
		}
	case []any:
		for i, elem := range x {
			walkStrings(path+"["+strconv.Itoa(i)+"]", elem, fn)
		}
	case map[string]any:
		for k, val := range x {
			next := k
			if path != "" {
				next = path + "." + k
			}
			walkStrings(next, val, fn)
		}
	case map[string]string:
		for k, val := range x {
			next := k
			if path != "" {
				next = path + "." + k
			}
			fn(next, val)
		}
	}
}

// TestProject_NoOppositeEndLeak enforces v1 D1, v1 §9 item 2, and Amendment A1
// §B.2 / §B.3 / §C item 8:
//   - Direct-DM fixtures drop conversation.ref and conversation.id in policy_out
//     and policy_in; group conversations keep them.
//   - Seeded opposite-end and third-party IDs in event.source.{id,name},
//     event.subject, event.reason, event.templated_vars, mentions.*_ids, and
//     conversation.*_participant_ids are ABSENT in policy_out and policy_in
//     (verified via recursive walk over the entire projected activation).
//   - event.source.{id,name} IS kept when source.kind in {agent,user} and
//     source.id equals the visible end's ID.
//   - policy_a2a and trigger_chat retain the seeded IDs.
func TestProject_NoOppositeEndLeak(t *testing.T) {
	// Amendment A2 §3 distinct-id discipline: every principal role carries a
	// distinct ID so coincidental equality never masks a leak.
	const (
		senderID          = "11111111-1111-1111-1111-111111111111"
		senderCreatedByID = "11111111-1111-1111-1111-111111111112"
		senderRootUserID  = "11111111-1111-1111-1111-111111111113"
		recipientID       = "22222222-2222-2222-2222-222222222222"
		thirdPartyID      = "33333333-3333-3333-3333-333333333333"
	)

	sender := rulefacts.EndFromAgentWithLookup(
		rulefacts.AgentInput{
			ID:            senderID,
			Slug:          "sender-agent",
			Name:          "Sender Agent",
			ProjectID:     "proj-1",
			CreatedByKind: "user",
			CreatedByID:   senderCreatedByID,
			RootUserID:    senderRootUserID,
		},
		nil,
		func() (string, rulefacts.TemplateLookupStatus) { return "developer", rulefacts.TemplateLookupOK },
	)
	recipient := rulefacts.EndFromUser(rulefacts.UserInput{
		ID:          recipientID,
		Slug:        "recipient-user",
		DisplayName: "Recipient User",
		ProjectID:   "proj-2",
	})

	t.Run("direct_dm_and_seeded_event_fields", func(t *testing.T) {
		facts := rulefacts.Facts{
			Message: rulefacts.MessageFacts{
				ID:         "msg-leak-1",
				CreatedAt:  fixedFixtureTime(),
				Kind:       rulefacts.KindEvent,
				Type:       messages.TypeSystem,
				Origin:     rulefacts.OriginTrigger,
				Body:       "neutral content",
				Bytes:      15,
				BodySource: rulefacts.BodySourceEvaluatedInput,
				Event: &rulefacts.EventFacts{
					Type:          "trigger.fired",
					EventType:     "webhook.deploy",
					EventID:       "evt-1",
					FireID:        "fire-1",
					Status:        "FAILED",
					Reason:        "undeliverable to " + recipientID + " and " + thirdPartyID,
					Subject:       "agent:" + thirdPartyID,
					Source:        &rulefacts.EventSource{Kind: "agent", ID: recipientID, Name: "named-" + recipientID},
					Trigger:       &rulefacts.EventTrigger{ID: "trig-1", Name: "deploy-watch"},
					Schedule:      &rulefacts.EventSchedule{ID: "sched-1", Name: "nightly"},
					External:      &rulefacts.EventExternal{Present: true, Verified: "github-v1", Bytes: 128},
					TemplatedVars: []string{"actor:" + recipientID, "cc:" + thirdPartyID},
				},
				Interaction: &rulefacts.InteractionFacts{Kind: rulefacts.InteractionDecisionPrompt, Count: 1},
				System:      &rulefacts.SystemFacts{Category: "trigger"},
			},
			Sender:    &sender,
			Recipient: &recipient,
			Conversation: rulefacts.ConversationFacts{
				Ref:                   "dm:agent:" + senderID + ":user:" + recipientID,
				ID:                    "conv-direct-" + recipientID,
				Kind:                  "direct",
				Surface:               "native",
				ProjectID:             "proj-1",
				AgentCount:            2,
				UserCount:             1,
				AgentParticipantIDs:   []string{senderID, thirdPartyID},
				UserParticipantIDs:    []string{recipientID},
				UnmentionedAgentSlugs: []string{"third-party-" + thirdPartyID},
			},
			Mentions: rulefacts.MentionsFacts{
				AgentCount: 1,
				UserCount:  1,
				AgentIDs:   []string{thirdPartyID},
				UserIDs:    []string{recipientID},
			},
			Delivery: rulefacts.DeliveryFacts{
				Surface:      "agent_to_user",
				Via:          "direct",
				CrossProject: true,
			},
		}

		// policy_out: must leak neither recipientID nor thirdPartyID anywhere,
		// and delivery.cross_project must be absent (Amendment A2 §2.3).
		outMap := rulefacts.Project(facts, rulefacts.ScopePolicyOut).Map()
		_, hasRecipient := outMap["recipient"]
		assert.False(t, hasRecipient, "recipient must be absent in policy_out")
		assert.NotContains(t, outMap["delivery"].(map[string]any), "cross_project", "A2: delivery.cross_project must be absent in policy_out")
		convOut := outMap["conversation"].(map[string]any)
		_, hasRef := convOut["ref"]
		_, hasID := convOut["id"]
		assert.False(t, hasRef, "D1: conversation.ref must be absent for direct DM in policy_out")
		assert.False(t, hasID, "D1: conversation.id must be absent for direct DM in policy_out")
		assert.NotContains(t, convOut, "agent_participant_ids")
		assert.NotContains(t, convOut, "user_participant_ids")
		mentionsOut := outMap["mentions"].(map[string]any)
		assert.NotContains(t, mentionsOut, "agent_ids")
		assert.NotContains(t, mentionsOut, "user_ids")

		evOut := outMap["message"].(map[string]any)["event"].(map[string]any)
		assert.NotContains(t, evOut, "subject")
		assert.NotContains(t, evOut, "reason")
		assert.NotContains(t, evOut, "templated_vars")
		srcOut := evOut["source"].(map[string]any)
		assert.Equal(t, "agent", srcOut["kind"])
		assert.NotContains(t, srcOut, "id")
		assert.NotContains(t, srcOut, "name")

		walkStrings("", outMap, func(path, val string) {
			assert.NotContainsf(t, val, recipientID, "policy_out leaked opposite-end ID at %s", path)
			assert.NotContainsf(t, val, thirdPartyID, "policy_out leaked third-party ID at %s", path)
		})

		// policy_in: must leak neither senderID nor thirdPartyID anywhere,
		// and delivery.cross_project must be absent (Amendment A2 §2.3).
		inMap := rulefacts.Project(facts, rulefacts.ScopePolicyIn).Map()
		_, hasSender := inMap["sender"]
		assert.False(t, hasSender, "sender must be absent in policy_in")
		assert.NotContains(t, inMap["delivery"].(map[string]any), "cross_project", "A2: delivery.cross_project must be absent in policy_in")
		convIn := inMap["conversation"].(map[string]any)
		assert.NotContains(t, convIn, "ref")
		assert.NotContains(t, convIn, "id")
		assert.NotContains(t, convIn, "agent_participant_ids")
		assert.NotContains(t, convIn, "user_participant_ids")
		mentionsIn := inMap["mentions"].(map[string]any)
		assert.NotContains(t, mentionsIn, "agent_ids")
		assert.NotContains(t, mentionsIn, "user_ids")
		walkStrings("", inMap, func(path, val string) {
			assert.NotContainsf(t, val, senderID, "policy_in leaked opposite-end ID at %s", path)
			assert.NotContainsf(t, val, thirdPartyID, "policy_in leaked third-party ID at %s", path)
		})

		// policy_a2a and trigger_chat: retain seeded IDs and delivery.cross_project.
		a2aMap := rulefacts.Project(facts, rulefacts.ScopePolicyA2A).Map()
		assert.Equal(t, true, a2aMap["delivery"].(map[string]any)["cross_project"])
		evA2A := a2aMap["message"].(map[string]any)["event"].(map[string]any)
		assert.Equal(t, "agent:"+thirdPartyID, evA2A["subject"])
		assert.Contains(t, evA2A["reason"], recipientID)
		assert.Equal(t, recipientID, evA2A["source"].(map[string]any)["id"])
		assert.Equal(t, []string{"actor:" + recipientID, "cc:" + thirdPartyID}, evA2A["templated_vars"])

		trigMap := rulefacts.Project(facts, rulefacts.ScopeTriggerChat).Map()
		trigDelivery := trigMap["message"].(map[string]any)["context"].(map[string]any)["delivery"].(map[string]any)
		assert.Equal(t, true, trigDelivery["cross_project"])
		evTrig := trigMap["message"].(map[string]any)["event"].(map[string]any)
		assert.Equal(t, "agent:"+thirdPartyID, evTrig["subject"])
		assert.Equal(t, recipientID, evTrig["source"].(map[string]any)["id"])
	})

	t.Run("event_source_kept_when_matching_visible_end", func(t *testing.T) {
		facts := rulefacts.Facts{
			Message: rulefacts.MessageFacts{
				Kind:       rulefacts.KindEvent,
				Type:       messages.TypeSystem,
				Origin:     rulefacts.OriginTrigger,
				BodySource: rulefacts.BodySourceEvaluatedInput,
				Event: &rulefacts.EventFacts{
					Type:   "trigger.fired",
					Source: &rulefacts.EventSource{Kind: "agent", ID: senderID, Name: "sender-agent"},
				},
			},
			Sender:    &sender,
			Recipient: &recipient,
			Conversation: rulefacts.ConversationFacts{
				Ref:  "conv-group-1",
				ID:   "conv-group-1",
				Kind: "group",
			},
		}

		// In policy_out, visible end is sender (senderID) -> source.id and source.name ARE kept.
		outSrc := rulefacts.Project(facts, rulefacts.ScopePolicyOut).Map()["message"].(map[string]any)["event"].(map[string]any)["source"].(map[string]any)
		assert.Equal(t, "agent", outSrc["kind"])
		assert.Equal(t, senderID, outSrc["id"])
		assert.Equal(t, "sender-agent", outSrc["name"])

		// In policy_out for a group conversation, conversation.ref and conversation.id ARE kept (D1).
		outConv := rulefacts.Project(facts, rulefacts.ScopePolicyOut).Map()["conversation"].(map[string]any)
		assert.Equal(t, "conv-group-1", outConv["ref"])
		assert.Equal(t, "conv-group-1", outConv["id"])

		// In policy_in, visible end is recipient (recipientID != senderID) -> source.id and source.name DROPPED.
		inSrc := rulefacts.Project(facts, rulefacts.ScopePolicyIn).Map()["message"].(map[string]any)["event"].(map[string]any)["source"].(map[string]any)
		assert.Equal(t, "agent", inSrc["kind"])
		assert.NotContains(t, inSrc, "id")
		assert.NotContains(t, inSrc, "name")
	})
}

// TestProject_OutOfScopeCompileFailsWithLineColumn verifies v1 §9 item 2:
// referencing an out-of-scope end fails CEL compilation with line and column.
func TestProject_OutOfScopeCompileFailsWithLineColumn(t *testing.T) {
	outEng, err := celengine.New(celengine.Environment{
		Name:       "policy_out_test",
		Version:    1,
		Variables:  rulefacts.Declarations(rulefacts.ScopePolicyOut, rulefacts.NamingPolicy),
		Extensions: []celengine.Extension{celengine.ExtensionStrings},
		Macros:     celengine.MacrosStandard,
	}, celengine.DefaultLimits(), celengine.DefaultCacheConfig())
	require.NoError(t, err)

	_, err = outEng.Compile(`recipient.slug == "alice"`, celengine.Kind{Name: "violation", Result: celengine.Bool()})
	require.Error(t, err)
	var ce *celengine.CompileError
	require.True(t, errors.As(err, &ce), "expected *celengine.CompileError, got %T: %v", err, err)
	assert.Equal(t, celengine.CategoryUnknownVariable, ce.Category)
	assert.GreaterOrEqual(t, ce.Line, 1)
	assert.GreaterOrEqual(t, ce.Column, 1)

	inEng, err := celengine.New(celengine.Environment{
		Name:       "policy_in_test",
		Version:    1,
		Variables:  rulefacts.Declarations(rulefacts.ScopePolicyIn, rulefacts.NamingPolicy),
		Extensions: []celengine.Extension{celengine.ExtensionStrings},
		Macros:     celengine.MacrosStandard,
	}, celengine.DefaultLimits(), celengine.DefaultCacheConfig())
	require.NoError(t, err)

	_, err = inEng.Compile(`sender.slug == "dev-worker"`, celengine.Kind{Name: "violation", Result: celengine.Bool()})
	require.Error(t, err)
	require.True(t, errors.As(err, &ce))
	assert.Equal(t, celengine.CategoryUnknownVariable, ce.Category)
	assert.GreaterOrEqual(t, ce.Line, 1)
	assert.GreaterOrEqual(t, ce.Column, 1)

	// Amendment A2 §2.3 / §3.4: delivery.cross_project is statically invisible
	// in policy_out (and policy_in). In R1 (map declarations + authoring path
	// check), Schema.ValidatePath rejects it at authoring time and direct CEL
	// evaluation against Project(policy_out) yields missing_input.
	assert.Error(t, rulefacts.Schema.ValidatePath(rulefacts.ScopePolicyOut, "delivery.cross_project"))
	assert.Error(t, rulefacts.Schema.ValidatePath(rulefacts.ScopePolicyIn, "delivery.cross_project"))
	cpProg, err := outEng.Compile(`delivery.cross_project`, celengine.Kind{Name: "violation", Result: celengine.Bool()})
	require.NoError(t, err)
	_, _, evalErr := cpProg.Eval(context.Background(), rulefacts.Project(rulefacts.Facts{
		Message:  rulefacts.MessageFacts{Kind: rulefacts.KindText, Type: "chat", Origin: rulefacts.OriginAgent, BodySource: rulefacts.BodySourceEvaluatedInput},
		Delivery: rulefacts.DeliveryFacts{Surface: "agent_to_user", CrossProject: true},
	}, rulefacts.ScopePolicyOut).Map())
	var ee *celengine.EvalError
	require.True(t, errors.As(evalErr, &ee), "expected *EvalError on delivery.cross_project in policy_out, got %T: %v", evalErr, evalErr)
	assert.Equal(t, celengine.ReasonMissingInput, ee.Reason)
}

// TestProject_RecipientInvariance_Out verifies Amendment A2 §2.1 & §3.1:
// Project(facts, ScopePolicyOut) is byte-identical (JSON-canonical) when only
// the recipient end varies (id, slug, project_id, all attributes and labels,
// created_by, root_user_id — including flipping Delivery.CrossProject).
func TestProject_RecipientInvariance_Out(t *testing.T) {
	sender := rulefacts.EndFromAgentWithLookup(
		rulefacts.AgentInput{
			ID:            "agent-sender-1",
			Slug:          "dev-worker",
			Name:          "Dev Worker",
			ProjectID:     "proj-A",
			CreatedByKind: "user",
			CreatedByID:   "user-creator-1",
			RootUserID:    "user-root-1",
			Labels:        map[string]string{"role": "developer"},
		},
		&rulefacts.AppliedConfigInput{TemplateID: "tmpl-1", TemplateHash: "sha256:v1"},
		func() (string, rulefacts.TemplateLookupStatus) { return "developer", rulefacts.TemplateLookupOK },
	)

	recipient1 := rulefacts.EndFromAgentWithLookup(
		rulefacts.AgentInput{
			ID:            "agent-recip-1",
			Slug:          "recip-one",
			Name:          "Recipient One",
			ProjectID:     "proj-A",
			CreatedByKind: "user",
			CreatedByID:   "user-recip-creator-1",
			RootUserID:    "user-recip-root-1",
			Labels:        map[string]string{"tier": "alpha"},
		},
		&rulefacts.AppliedConfigInput{TemplateID: "tmpl-r1", TemplateHash: "sha256:r1"},
		func() (string, rulefacts.TemplateLookupStatus) { return "reviewer", rulefacts.TemplateLookupOK },
	)
	recipient2 := rulefacts.EndFromAgentWithLookup(
		rulefacts.AgentInput{
			ID:            "agent-recip-2",
			Slug:          "recip-two",
			Name:          "Recipient Two",
			ProjectID:     "proj-B", // differs from sender.ProjectID -> flips CrossProject!
			CreatedByKind: "agent",
			CreatedByID:   "agent-recip-creator-2",
			RootUserID:    "user-recip-root-2",
			Labels:        map[string]string{"tier": "beta", "extra": "yes"},
		},
		&rulefacts.AppliedConfigInput{TemplateID: "tmpl-r2", TemplateHash: "sha256:r2"},
		func() (string, rulefacts.TemplateLookupStatus) { return "", rulefacts.TemplateLookupNotFound },
	)

	// Hold End.Kind, ConversationInfo, and DeliveryInfo constant (A2 §2.1 + C3).
	fixedConv := rulefacts.ConversationInfo{
		Ref:        "dm:fixed:key",
		ID:         "conv-dm-fixed",
		Kind:       "direct",
		Surface:    "native",
		ProjectID:  "proj-A",
		AgentCount: 2,
	}

	buildFacts := func(recip *rulefacts.End) rulefacts.Facts {
		f, err := rulefacts.FromMessage(
			rulefacts.MessageInput{
				Type:         messages.TypeInstruction,
				Sender:       "agent:dev-worker",
				SenderID:     sender.ID,
				Recipient:    "agent:" + recip.Slug,
				Body:         "Status update",
				BodySource:   rulefacts.BodySourceEvaluatedInput,
				SenderEnd:    &sender,
				RecipientEnd: recip,
			},
			fixedConv,
			rulefacts.DeliveryInfo{
				Surface: "agent_dm",
				Via:     "direct",
			},
		)
		require.NoError(t, err)
		return f
	}

	f1 := buildFacts(&recipient1)
	f2 := buildFacts(&recipient2)
	require.False(t, f1.Delivery.CrossProject)
	require.True(t, f2.Delivery.CrossProject)

	b1, err := json.Marshal(rulefacts.Project(f1, rulefacts.ScopePolicyOut).Map())
	require.NoError(t, err)
	b2, err := json.Marshal(rulefacts.Project(f2, rulefacts.ScopePolicyOut).Map())
	require.NoError(t, err)
	assert.Equal(t, string(b1), string(b2), "policy_out projection must be byte-identical when only the recipient end identity/attributes change")
}

// flattenLeafPaths returns a map of dotted leaf path -> canonical JSON value
// for comparing two projected activation maps path-by-path.
func flattenLeafPaths(t *testing.T, m map[string]any) map[string]string {
	t.Helper()
	out := make(map[string]string)
	var walk func(prefix string, v any)
	walk = func(prefix string, v any) {
		if sub, ok := v.(map[string]any); ok {
			for k, val := range sub {
				next := k
				if prefix != "" {
					next = prefix + "." + k
				}
				walk(next, val)
			}
			return
		}
		b, err := json.Marshal(v)
		require.NoError(t, err)
		out[prefix] = string(b)
	}
	walk("", m)
	return out
}

func diffPaths(a, b map[string]string) []string {
	var diff []string
	for k, va := range a {
		if vb, ok := b[k]; !ok || va != vb {
			diff = append(diff, k)
		}
	}
	for k := range b {
		if _, ok := a[k]; !ok {
			diff = append(diff, k)
		}
	}
	return diff
}

// TestProject_RecipientKindVariation_Out_C3 verifies joint ruling C3
// (working-copy §0a alongside A2 dab1ced9): varying ONLY the recipient kind
// (agent <-> user, with realistic hub delivery path for each) in policy_out
// may differ ONLY on the exact allow-list:
//   - {delivery.surface, delivery.via} for non-reply message types
//   - {delivery.surface, delivery.via, message.intent} for message.type == reply
//
// and recipient.* is absent in both variants.
func TestProject_RecipientKindVariation_Out_C3(t *testing.T) {
	sender := rulefacts.EndFromAgentWithLookup(
		rulefacts.AgentInput{
			ID:        "agent-sender-1",
			Slug:      "dev-worker",
			Name:      "Dev Worker",
			ProjectID: "proj-A",
		},
		nil,
		func() (string, rulefacts.TemplateLookupStatus) { return "developer", rulefacts.TemplateLookupOK },
	)
	recipAgent := rulefacts.EndFromAgentWithLookup(
		rulefacts.AgentInput{ID: "recip-1", Slug: "peer", Name: "Peer", ProjectID: "proj-A"},
		nil,
		func() (string, rulefacts.TemplateLookupStatus) { return "reviewer", rulefacts.TemplateLookupOK },
	)
	recipUser := rulefacts.EndFromUser(rulefacts.UserInput{
		ID: "recip-1", Slug: "peer", DisplayName: "Peer", ProjectID: "proj-A",
	})
	fixedConv := rulefacts.ConversationInfo{
		Ref: "dm:fixed", ID: "conv-fixed", Kind: "direct", Surface: "native", ProjectID: "proj-A", AgentCount: 1, UserCount: 1,
	}

	for _, msgType := range []string{messages.TypeInstruction, messages.TypeChat, messages.TypeReply} {
		t.Run("type_"+msgType, func(t *testing.T) {
			fAgent, err := rulefacts.FromMessage(rulefacts.MessageInput{
				Type: msgType, Sender: "agent:dev-worker", SenderID: sender.ID,
				Recipient: "agent:peer", Body: "hello", BodySource: rulefacts.BodySourceEvaluatedInput,
				SenderEnd: &sender, RecipientEnd: &recipAgent,
			}, fixedConv, rulefacts.DeliveryInfo{Surface: "agent_dm", Via: "direct"})
			require.NoError(t, err)

			fUser, err := rulefacts.FromMessage(rulefacts.MessageInput{
				Type: msgType, Sender: "agent:dev-worker", SenderID: sender.ID,
				Recipient: "user:peer", Body: "hello", BodySource: rulefacts.BodySourceEvaluatedInput,
				SenderEnd: &sender, RecipientEnd: &recipUser,
			}, fixedConv, rulefacts.DeliveryInfo{Surface: "agent_to_user", Via: "direct"})
			require.NoError(t, err)

			outAgent := rulefacts.Project(fAgent, rulefacts.ScopePolicyOut).Map()
			outUser := rulefacts.Project(fUser, rulefacts.ScopePolicyOut).Map()
			assert.NotContains(t, outAgent, "recipient")
			assert.NotContains(t, outUser, "recipient")

			allowed := map[string]bool{
				"delivery.surface": true,
				"delivery.via":     true,
			}
			if msgType == messages.TypeReply {
				allowed["message.intent"] = true
			}
			for _, p := range diffPaths(flattenLeafPaths(t, outAgent), flattenLeafPaths(t, outUser)) {
				assert.Truef(t, allowed[p], "type=%s: unexpected differing path %q in policy_out when varying recipient kind (allowed: %v)", msgType, p, allowed)
			}
		})
	}
}

// TestProject_SenderKindVariation_In_C3 verifies joint ruling C3 for policy_in:
// varying ONLY the sender kind (agent <-> user, with realistic hub delivery
// path for each) may differ ONLY on the exact allow-list {delivery.surface,
// delivery.via, message.intent (only when reply)}, and sender.* is absent in
// both variants.
func TestProject_SenderKindVariation_In_C3(t *testing.T) {
	recipient := rulefacts.EndFromAgentWithLookup(
		rulefacts.AgentInput{
			ID:        "agent-recip-1",
			Slug:      "dev-worker",
			Name:      "Dev Worker",
			ProjectID: "proj-A",
		},
		nil,
		func() (string, rulefacts.TemplateLookupStatus) { return "developer", rulefacts.TemplateLookupOK },
	)
	senderAgent := rulefacts.EndFromAgentWithLookup(
		rulefacts.AgentInput{ID: "sender-1", Slug: "peer", Name: "Peer", ProjectID: "proj-A"},
		nil,
		func() (string, rulefacts.TemplateLookupStatus) { return "reviewer", rulefacts.TemplateLookupOK },
	)
	senderUser := rulefacts.EndFromUser(rulefacts.UserInput{
		ID: "sender-1", Slug: "peer", DisplayName: "Peer", ProjectID: "proj-A",
	})
	fixedConv := rulefacts.ConversationInfo{
		Ref: "dm:fixed", ID: "conv-fixed", Kind: "direct", Surface: "native", ProjectID: "proj-A", AgentCount: 1, UserCount: 1,
	}

	for _, msgType := range []string{messages.TypeInstruction, messages.TypeReply} {
		t.Run("type_"+msgType, func(t *testing.T) {
			fAgent, err := rulefacts.FromMessage(rulefacts.MessageInput{
				Type: msgType, Sender: "agent:peer", SenderID: senderAgent.ID,
				Recipient: "agent:dev-worker", Body: "hello", BodySource: rulefacts.BodySourceEvaluatedInput,
				SenderEnd: &senderAgent, RecipientEnd: &recipient,
			}, fixedConv, rulefacts.DeliveryInfo{Surface: "agent_dm", Via: "direct"})
			require.NoError(t, err)

			fUser, err := rulefacts.FromMessage(rulefacts.MessageInput{
				Type: msgType, Sender: "user:peer", SenderID: senderUser.ID,
				Recipient: "agent:dev-worker", Body: "hello", BodySource: rulefacts.BodySourceEvaluatedInput,
				SenderEnd: &senderUser, RecipientEnd: &recipient,
			}, fixedConv, rulefacts.DeliveryInfo{Surface: "web_chat", Via: "explicit"})
			require.NoError(t, err)

			inAgent := rulefacts.Project(fAgent, rulefacts.ScopePolicyIn).Map()
			inUser := rulefacts.Project(fUser, rulefacts.ScopePolicyIn).Map()
			assert.NotContains(t, inAgent, "sender")
			assert.NotContains(t, inUser, "sender")

			// R-2 / updated C3 (20:57Z): policy_in allow-list tightened to
			// {delivery.surface, delivery.via, message.origin} — message.intent
			// cannot vary when the recipient is held fixed.
			allowed := map[string]bool{
				"delivery.surface": true,
				"delivery.via":     true,
				"message.origin":   true,
			}
			for _, p := range diffPaths(flattenLeafPaths(t, inAgent), flattenLeafPaths(t, inUser)) {
				assert.Truef(t, allowed[p], "type=%s: unexpected differing path %q in policy_in when varying sender kind (allowed: %v)", msgType, p, allowed)
			}
		})
	}
}

// TestProject_SenderInvariance_In verifies Amendment A2 §2.1 & §3.1:
// Project(facts, ScopePolicyIn) is byte-identical (JSON-canonical) when only
// the sender end varies.
func TestProject_SenderInvariance_In(t *testing.T) {
	recipient := rulefacts.EndFromAgentWithLookup(
		rulefacts.AgentInput{
			ID:            "agent-recip-1",
			Slug:          "dev-worker",
			Name:          "Dev Worker",
			ProjectID:     "proj-A",
			CreatedByKind: "user",
			CreatedByID:   "user-creator-1",
			RootUserID:    "user-root-1",
			Labels:        map[string]string{"role": "developer"},
		},
		&rulefacts.AppliedConfigInput{TemplateID: "tmpl-1", TemplateHash: "sha256:v1"},
		func() (string, rulefacts.TemplateLookupStatus) { return "developer", rulefacts.TemplateLookupOK },
	)

	sender1 := rulefacts.EndFromAgentWithLookup(
		rulefacts.AgentInput{
			ID:            "agent-sender-1",
			Slug:          "sender-one",
			Name:          "Sender One",
			ProjectID:     "proj-A",
			CreatedByKind: "user",
			CreatedByID:   "user-s1",
			RootUserID:    "user-sr1",
			Labels:        map[string]string{"team": "a"},
		},
		&rulefacts.AppliedConfigInput{TemplateID: "tmpl-s1", TemplateHash: "sha256:s1"},
		func() (string, rulefacts.TemplateLookupStatus) { return "lead", rulefacts.TemplateLookupOK },
	)
	sender2 := rulefacts.EndFromAgentWithLookup(
		rulefacts.AgentInput{
			ID:            "agent-sender-2",
			Slug:          "sender-two",
			Name:          "Sender Two",
			ProjectID:     "proj-B",
			CreatedByKind: "agent",
			CreatedByID:   "agent-s2",
			RootUserID:    "user-sr2",
			Labels:        map[string]string{"team": "b"},
		},
		&rulefacts.AppliedConfigInput{TemplateID: "tmpl-s2", TemplateHash: "sha256:s2"},
		func() (string, rulefacts.TemplateLookupStatus) { return "", rulefacts.TemplateLookupError },
	)

	fixedConv := rulefacts.ConversationInfo{
		Ref:        "dm:fixed:key",
		ID:         "conv-dm-fixed",
		Kind:       "direct",
		Surface:    "native",
		ProjectID:  "proj-A",
		AgentCount: 2,
	}

	buildFacts := func(s *rulefacts.End) rulefacts.Facts {
		f, err := rulefacts.FromMessage(
			rulefacts.MessageInput{
				Type:         messages.TypeInstruction,
				Sender:       "agent:" + s.Slug,
				SenderID:     s.ID,
				Recipient:    "agent:" + recipient.Slug,
				Body:         "Inbound task",
				BodySource:   rulefacts.BodySourceEvaluatedInput,
				SenderEnd:    s,
				RecipientEnd: &recipient,
			},
			fixedConv,
			rulefacts.DeliveryInfo{
				Surface: "agent_dm",
				Via:     "direct",
			},
		)
		require.NoError(t, err)
		return f
	}

	f1 := buildFacts(&sender1)
	f2 := buildFacts(&sender2)
	require.False(t, f1.Delivery.CrossProject)
	require.True(t, f2.Delivery.CrossProject)

	b1, err := json.Marshal(rulefacts.Project(f1, rulefacts.ScopePolicyIn).Map())
	require.NoError(t, err)
	b2, err := json.Marshal(rulefacts.Project(f2, rulefacts.ScopePolicyIn).Map())
	require.NoError(t, err)
	assert.Equal(t, string(b1), string(b2), "policy_in projection must be byte-identical when only the sender end changes")
}

// TestProject_SameEndProvenanceKeptWhenEqualToOppositeEnd verifies Amendment A2
// §2.2 & §3.3 (positive control): when an agent messages its own creator
// (sender.created_by.id == recipient.id and sender.root_user_id == recipient.id),
// same-end provenance remains present in policy_out while recipient.* and
// delivery.cross_project are absent.
func TestProject_SameEndProvenanceKeptWhenEqualToOppositeEnd(t *testing.T) {
	const creatorUserID = "user-uuid-creator-42"

	sender := rulefacts.EndFromAgentWithLookup(
		rulefacts.AgentInput{
			ID:            "agent-uuid-1",
			Slug:          "dev-worker",
			Name:          "Dev Worker",
			ProjectID:     "proj-1",
			CreatedByKind: "user",
			CreatedByID:   creatorUserID,
			RootUserID:    creatorUserID,
		},
		nil,
		func() (string, rulefacts.TemplateLookupStatus) { return "developer", rulefacts.TemplateLookupOK },
	)
	recipient := rulefacts.EndFromUser(rulefacts.UserInput{
		ID:          creatorUserID,
		Slug:        "alice",
		DisplayName: "Alice",
		ProjectID:   "proj-1",
	})

	facts, err := rulefacts.FromMessage(
		rulefacts.MessageInput{
			Type:         messages.TypeInstruction,
			Sender:       "agent:dev-worker",
			SenderID:     sender.ID,
			Recipient:    "user:alice",
			Body:         "Done",
			BodySource:   rulefacts.BodySourceEvaluatedInput,
			SenderEnd:    &sender,
			RecipientEnd: &recipient,
		},
		rulefacts.ConversationInfo{
			Ref:       "dm:agent:agent-uuid-1:user:" + creatorUserID,
			ID:        "conv-dm-1",
			Kind:      "direct",
			Surface:   "native",
			ProjectID: "proj-1",
		},
		rulefacts.DeliveryInfo{Surface: "agent_to_user", Via: "direct"},
	)
	require.NoError(t, err)

	out := rulefacts.Project(facts, rulefacts.ScopePolicyOut).Map()
	assert.NotContains(t, out, "recipient")
	assert.NotContains(t, out["delivery"].(map[string]any), "cross_project")
	sMap := out["sender"].(map[string]any)
	assert.Equal(t, creatorUserID, sMap["root_user_id"])
	assert.Equal(t, creatorUserID, sMap["created_by"].(map[string]any)["id"])
}

// TestEndFromAgent_TemplateLookupSemantics verifies v1 §3.3b and §9 item 4:
// attributes.template is absent on not_found/error while template_lookup
// carries the reason, and template_id/template_version remain present.
func TestEndFromAgent_TemplateLookupSemantics(t *testing.T) {
	applied := &rulefacts.AppliedConfigInput{
		TemplateID:   "tmpl-1",
		TemplateHash: "sha256:v1",
	}
	base := rulefacts.AgentInput{ID: "a-1", Slug: "dev", Name: "Dev", ProjectID: "p-1"}

	eng, err := celengine.New(celengine.Environment{
		Name:       "tmpl_lookup_test",
		Version:    1,
		Variables:  rulefacts.Declarations(rulefacts.ScopePolicyOut, rulefacts.NamingPolicy),
		Extensions: []celengine.Extension{celengine.ExtensionStrings},
		Macros:     celengine.MacrosStandard,
	}, celengine.DefaultLimits(), celengine.DefaultCacheConfig())
	require.NoError(t, err)
	hasTmplProg, err := eng.Compile(`has(sender.attributes.template)`, celengine.Kind{Name: "check", Result: celengine.Bool()})
	require.NoError(t, err)
	readTmplProg, err := eng.Compile(`sender.attributes.template == "developer"`, celengine.Kind{Name: "check", Result: celengine.Bool()})
	require.NoError(t, err)

	cases := []struct {
		name       string
		end        rulefacts.End
		wantLookup string
		wantHas    bool
	}{
		{
			name: "ok",
			end: rulefacts.EndFromAgentWithLookup(base, applied, func() (string, rulefacts.TemplateLookupStatus) {
				return "developer", rulefacts.TemplateLookupOK
			}),
			wantLookup: "ok",
			wantHas:    true,
		},
		{
			name: "not_found",
			end: rulefacts.EndFromAgentWithLookup(base, applied, func() (string, rulefacts.TemplateLookupStatus) {
				return "", rulefacts.TemplateLookupNotFound
			}),
			wantLookup: "not_found",
			wantHas:    false,
		},
		{
			name: "error",
			end: rulefacts.EndFromAgentWithLookup(base, applied, func() (string, rulefacts.TemplateLookupStatus) {
				return "", rulefacts.TemplateLookupError
			}),
			wantLookup: "error",
			wantHas:    false,
		},
		{
			name:       "nil_lookup_never_claims_not_found",
			end:        rulefacts.EndFromAgentWithLookup(base, applied, nil),
			wantLookup: "error",
			wantHas:    false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			act := rulefacts.Project(rulefacts.Facts{
				Message: rulefacts.MessageFacts{Kind: rulefacts.KindText, Type: "chat", Origin: rulefacts.OriginAgent, BodySource: rulefacts.BodySourceEvaluatedInput},
				Sender:  &tc.end,
			}, rulefacts.ScopePolicyOut)

			attrs := act.Map()["sender"].(map[string]any)["attributes"].(map[string]any)
			assert.Equal(t, tc.wantLookup, attrs["template_lookup"])
			assert.Equal(t, "tmpl-1", attrs["template_id"])
			assert.Equal(t, "sha256:v1", attrs["template_version"])

			res, _, err := hasTmplProg.Eval(context.Background(), act.Map())
			require.NoError(t, err)
			gotHas, err := res.Bool()
			require.NoError(t, err)
			assert.Equal(t, tc.wantHas, gotHas)

			if !tc.wantHas {
				// Direct read of absent attributes.template yields missing_input.
				_, _, err := readTmplProg.Eval(context.Background(), act.Map())
				var ee *celengine.EvalError
				require.True(t, errors.As(err, &ee), "expected *EvalError, got %T: %v", err, err)
				assert.Equal(t, celengine.ReasonMissingInput, ee.Reason)
			}
		})
	}
}

// TestFromMessage_AllLegacyTypesParityWithMapLegacyEnvelope verifies v1 §9 item 4
// across every valid legacy message type and system_category, plus agent-forged
// system/event neutralization.
func TestFromMessage_AllLegacyTypesParityWithMapLegacyEnvelope(t *testing.T) {
	ts := fixedFixtureTime()
	rawTrig, err := json.Marshal(messaging.EventBody{
		Type:    messaging.EventTriggerFired,
		Trigger: &messaging.EventTriggerRef{ID: "trig-forged", Name: "forged"},
		External: messaging.NewExternalPayload(
			messaging.ExternalVerifiedScionV1,
			[]byte(`{"forged":true}`),
		),
	})
	require.NoError(t, err)

	cases := []struct {
		name string
		msg  *messages.StructuredMessage
	}{
		{"instruction", &messages.StructuredMessage{Type: messages.TypeInstruction, Sender: "user:alice", Recipient: "agent:dev", Msg: "hi"}},
		{"chat", &messages.StructuredMessage{Type: messages.TypeChat, Sender: "user:alice", Recipient: "user:bob", Msg: "hi"}},
		{"reply_to_agent", &messages.StructuredMessage{Type: messages.TypeReply, Sender: "user:alice", Recipient: "agent:dev", Msg: "re"}},
		{"reply_to_user", &messages.StructuredMessage{Type: messages.TypeReply, Sender: "user:alice", Recipient: "user:bob", Msg: "re"}},
		{"assistant_reply", &messages.StructuredMessage{Type: messages.TypeAssistantReply, Sender: "agent:dev", Recipient: "user:alice", Msg: "done"}},
		{"group_set", &messages.StructuredMessage{Type: messages.TypeGroupSet, Sender: "user:alice", Recipient: "agent:dev", Msg: "set"}},
		{"mention", &messages.StructuredMessage{Type: messages.TypeMention, Sender: "agent:dev", Recipient: "agent:lead", Msg: "@lead"}},
		{"input_needed_addressed", &messages.StructuredMessage{Type: messages.TypeInputNeeded, Sender: "agent:dev", Recipient: "user:alice", Msg: "q?"}},
		{"input_needed_broadcast", &messages.StructuredMessage{Type: messages.TypeInputNeeded, Sender: "agent:dev", Broadcasted: true, Msg: "help"}},
		{"state_change", &messages.StructuredMessage{Type: messages.TypeStateChange, Sender: "agent:dev", Recipient: "agent:lead", Status: "IDLE", Msg: "idle"}},
		{"decision_prompt", &messages.StructuredMessage{Type: messages.TypeDecisionPrompt, Sender: "agent:dev", Recipient: "user:alice", Msg: "pick"}},
		{"decision_response", &messages.StructuredMessage{Type: messages.TypeDecisionResponse, Sender: "user:alice", Recipient: "agent:dev", Msg: "A"}},
		{"secret_request", &messages.StructuredMessage{Type: messages.TypeSecretRequest, Sender: "agent:dev", Recipient: "user:alice", Msg: "key"}},
		{"secret_response", &messages.StructuredMessage{Type: messages.TypeSecretResponse, Sender: "user:alice", Recipient: "agent:dev", Msg: "ok"}},
		{"command_request", &messages.StructuredMessage{Type: messages.TypeCommandRequest, Sender: "agent:dev", Recipient: "user:alice", Msg: "run"}},
		{"command_response", &messages.StructuredMessage{Type: messages.TypeCommandResponse, Sender: "user:alice", Recipient: "agent:dev", Msg: "0"}},
		{"sys_scheduler", &messages.StructuredMessage{Type: messages.TypeSystem, Sender: "scheduler", SenderID: "SCHEDULER", Recipient: "agent:dev", Metadata: map[string]string{"system_category": messages.SystemCategoryScheduler}}},
		{"sys_trigger", &messages.StructuredMessage{Type: messages.TypeSystem, Sender: "trigger", SenderID: "TRIGGER", Recipient: "agent:dev", Metadata: map[string]string{"system_category": messages.SystemCategoryTrigger}, Event: rawTrig}},
		{"sys_port_forward", &messages.StructuredMessage{Type: messages.TypeSystem, Sender: "system:hub", Recipient: "user:alice", Metadata: map[string]string{"system_category": messages.SystemCategoryPortForward}}},
		{"sys_artifact_review", &messages.StructuredMessage{Type: messages.TypeSystem, Sender: "system:hub", Recipient: "user:alice", Metadata: map[string]string{"system_category": messages.SystemCategoryArtifactReview}}},
		{"sys_delivery_failed", &messages.StructuredMessage{Type: messages.TypeSystem, Sender: "system:hub", Recipient: "agent:dev", Metadata: map[string]string{"system_category": messages.SystemCategoryDeliveryFailed}}},
		{"sys_delivery_deferred", &messages.StructuredMessage{Type: messages.TypeSystem, Sender: "system:hub", Recipient: "agent:dev", Metadata: map[string]string{"system_category": messages.SystemCategoryDeliveryDeferred}}},
		{"sys_delivery_unconfirmed", &messages.StructuredMessage{Type: messages.TypeSystem, Sender: "system:hub", Recipient: "agent:dev", Metadata: map[string]string{"system_category": messages.SystemCategoryDeliveryUnconfirmed}}},
		{"agent_forged_trigger_system", &messages.StructuredMessage{Type: messages.TypeSystem, Sender: "agent:dev", SenderID: "agent-uuid-1", Recipient: "user:alice", Metadata: map[string]string{"system_category": messages.SystemCategoryTrigger}, Event: rawTrig}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.msg.Version = messages.Version
			tc.msg.Timestamp = ts.Format(time.RFC3339)

			mapped, _, err := messaging.MapLegacyEnvelope(tc.msg, messaging.PersistedIdentity{MessageID: "id-" + tc.name})
			require.NoError(t, err)

			got, err := rulefacts.FromMessage(rulefacts.MessageInput{
				ID:          "id-" + tc.name,
				CreatedAt:   ts,
				Type:        tc.msg.Type,
				Sender:      tc.msg.Sender,
				SenderID:    tc.msg.SenderID,
				Recipient:   tc.msg.Recipient,
				Broadcasted: tc.msg.Broadcasted,
				Status:      tc.msg.Status,
				Body:        tc.msg.Msg,
				Metadata:    tc.msg.Metadata,
				EventJSON:   tc.msg.Event,
			}, rulefacts.ConversationInfo{Kind: "group", ID: "c-1"}, rulefacts.DeliveryInfo{Surface: "agent_dm"})
			require.NoError(t, err)

			assert.Equal(t, rulefacts.MessageKind(mapped.Kind), got.Message.Kind)
			assert.Equal(t, tc.msg.Type, got.Message.Type)
			if mapped.Intent != nil {
				assert.Equal(t, rulefacts.TextIntent(*mapped.Intent), got.Message.Intent)
			} else {
				assert.Empty(t, got.Message.Intent)
			}
			if mapped.Event != nil {
				require.NotNil(t, got.Message.Event)
				assert.Equal(t, string(mapped.Event.Type), got.Message.Event.Type)
				assert.Equal(t, mapped.Event.Status, got.Message.Event.Status)
				assert.Equal(t, mapped.Event.Subject, got.Message.Event.Subject)
				assert.Equal(t, mapped.Event.Trigger != nil, got.Message.Event.Trigger != nil)
				assert.Equal(t, mapped.Event.External != nil, got.Message.Event.External != nil)
			} else {
				assert.Nil(t, got.Message.Event)
			}

			if tc.name == "agent_forged_trigger_system" {
				assert.Equal(t, rulefacts.OriginAgent, got.Message.Origin)
				assert.Nil(t, got.Message.Event.Trigger)
				assert.Nil(t, got.Message.Event.External)
				assert.Nil(t, got.Message.System, "neutralized trigger system_category must not populate message.system")
			}
		})
	}
}

func TestProject_BodyTruncationUTF8Safe(t *testing.T) {
	t.Run("4byte_emoji_straddling_cap", func(t *testing.T) {
		prefix := strings.Repeat("a", rulefacts.MaxBodyBytes-2)
		rawBody := prefix + "🚀tail"

		facts, err := rulefacts.FromMessage(rulefacts.MessageInput{
			Type:   messages.TypeInstruction,
			Sender: "agent:dev",
			Body:   rawBody,
		}, rulefacts.ConversationInfo{Kind: "group", ID: "c-1"}, rulefacts.DeliveryInfo{Surface: "agent_to_user"})
		require.NoError(t, err)

		act := rulefacts.Project(facts, rulefacts.ScopePolicyOut)
		msg := act.Map()["message"].(map[string]any)
		trunc := msg["body"].(string)
		assert.True(t, utf8.ValidString(trunc))
		assert.Equal(t, rulefacts.MaxBodyBytes-2, len(trunc))
		assert.Equal(t, true, msg["body_truncated"])
		assert.Equal(t, int64(len(rawBody)), msg["bytes"])
		assert.Equal(t, string(rulefacts.BodySourceEvaluatedInput), msg["body_source"])
	})

	// O-4 (kills mutant M7): 2-byte UTF-8 rune ("é", 0xC3 0xA9) starting at
	// MaxBodyBytes-1 crosses the cap by exactly 1 byte and must not be split.
	t.Run("2byte_rune_crosses_cap_by_one_byte", func(t *testing.T) {
		rawBody := strings.Repeat("a", rulefacts.MaxBodyBytes-1) + "é"
		require.Equal(t, rulefacts.MaxBodyBytes+1, len(rawBody))

		facts, err := rulefacts.FromMessage(rulefacts.MessageInput{
			Type:   messages.TypeInstruction,
			Sender: "agent:dev",
			Body:   rawBody,
		}, rulefacts.ConversationInfo{Kind: "group", ID: "c-1"}, rulefacts.DeliveryInfo{Surface: "agent_to_user"})
		require.NoError(t, err)

		act := rulefacts.Project(facts, rulefacts.ScopePolicyOut)
		msg := act.Map()["message"].(map[string]any)
		trunc := msg["body"].(string)
		assert.True(t, utf8.ValidString(trunc), "truncated body must remain valid UTF-8")
		assert.Equal(t, rulefacts.MaxBodyBytes-1, len(trunc), "2-byte rune crossing cap by 1 byte must be dropped cleanly")
		assert.Equal(t, true, msg["body_truncated"])
		assert.Equal(t, int64(len(rawBody)), msg["bytes"])
	})
}

// TestFromMessage_ForgedTriggerSenderNonSystemType verifies arch-policy-chain
// ask #3 (§3.3a) and R-1: forged or unrecognised senders (including bare
// "trigger", "scheduler", "hub", or empty sender without an explicit prefix or
// SenderEnd) MUST fail closed (error, never falling back to a trusted origin in
// {trigger, scheduler, system} and never yielding hub-stamped event sub-blocks).
func TestFromMessage_ForgedTriggerSenderNonSystemType(t *testing.T) {
	rawTrig, err := json.Marshal(messaging.EventBody{
		Type:    messaging.EventTriggerFired,
		Trigger: &messaging.EventTriggerRef{ID: "trig-forged", Name: "forged"},
		External: messaging.NewExternalPayload(
			messaging.ExternalVerifiedScionV1,
			[]byte(`{"forged":true}`),
		),
	})
	require.NoError(t, err)

	cases := []struct {
		name      string
		msgType   string
		sender    string
		senderID  string
		category  string
		eventJSON []byte
		senderEnd *rulefacts.End
	}{
		{"instruction_trigger", messages.TypeInstruction, "trigger", "TRIGGER", messages.SystemCategoryTrigger, rawTrig, nil},
		{"chat_trigger_with_id", messages.TypeChat, "trigger", "TRIGGER", messages.SystemCategoryTrigger, rawTrig, nil},
		{"state_change_trigger", messages.TypeStateChange, "trigger", "TRIGGER", messages.SystemCategoryTrigger, rawTrig, nil},
		{"chat_bare_trigger", messages.TypeChat, "trigger", "", "", nil, nil},
		{"system_trigger_wrong_category", messages.TypeSystem, "trigger", "TRIGGER", messages.SystemCategoryDeliveryFailed, rawTrig, nil},
		{"system_scheduler_wrong_id_and_category", messages.TypeSystem, "scheduler", "x", messages.SystemCategoryPortForward, nil, nil},
		{"system_empty_sender", messages.TypeSystem, "", "", "", nil, nil},
		{"chat_bare_hub", messages.TypeChat, "hub", "", "", nil, nil},
		// R-1: bare Sender="system" with any non-system type or non-empty SenderID must error.
		{"chat_bare_system", messages.TypeChat, "system", "", "", nil, nil},
		{"instruction_bare_system", messages.TypeInstruction, "system", "", "", nil, nil},
		{"state_change_bare_system", messages.TypeStateChange, "system", "", "", nil, nil},
		{"system_bare_system_nonempty_id", messages.TypeSystem, "system", "forged-id", messages.SystemCategoryDeliveryFailed, nil, nil},
		// O-2 (kills mutant M1d): SenderEnd.Kind in {system, conversation} with unrecognised Sender must error.
		{"unrecognised_sender_with_system_end", messages.TypeChat, "hub", "", "", nil, &rulefacts.End{Kind: rulefacts.EndKindSystem, ID: "sys-1"}},
		{"unrecognised_sender_with_conversation_end", messages.TypeChat, "hub", "", "", nil, &rulefacts.End{Kind: rulefacts.EndKindConversation, ID: "conv-1"}},
		// Fix3 O-1: unprefixed Sender failing hub-minted guards MUST error even when an agent SenderEnd is present,
		// and user:/agent: prefix disagreeing with SenderEnd.Kind MUST error.
		{"unprefixed_scheduler_with_agent_end", messages.TypeSystem, "scheduler", "SCHEDULER", messages.SystemCategoryPortForward, nil, &rulefacts.End{Kind: rulefacts.EndKindAgent, ID: "agent-1"}},
		{"unprefixed_trigger_with_agent_end", messages.TypeSystem, "trigger", "TRIGGER", messages.SystemCategoryDeliveryFailed, rawTrig, &rulefacts.End{Kind: rulefacts.EndKindAgent, ID: "agent-1"}},
		{"empty_sender_with_agent_end", messages.TypeChat, "", "", "", nil, &rulefacts.End{Kind: rulefacts.EndKindAgent, ID: "agent-1"}},
		{"bare_hub_with_agent_end", messages.TypeChat, "hub", "", "", nil, &rulefacts.End{Kind: rulefacts.EndKindAgent, ID: "agent-1"}},
		{"user_prefix_disagrees_with_agent_end", messages.TypeChat, "user:alice", "user-1", "", nil, &rulefacts.End{Kind: rulefacts.EndKindAgent, ID: "agent-1"}},
		{"agent_prefix_disagrees_with_user_end", messages.TypeChat, "agent:dev", "agent-1", "", nil, &rulefacts.End{Kind: rulefacts.EndKindUser, ID: "user-1"}},
		// Fix3 O-2 (kills mutant M6): single-variable trigger SenderID mismatch.
		{"trigger_wrong_sender_id", messages.TypeSystem, "trigger", "x", messages.SystemCategoryTrigger, rawTrig, nil},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var md map[string]string
			if tc.category != "" {
				md = map[string]string{"system_category": tc.category}
			}
			got, err := rulefacts.FromMessage(rulefacts.MessageInput{
				Type:      tc.msgType,
				Sender:    tc.sender,
				SenderID:  tc.senderID,
				Recipient: "agent:dev",
				Body:      "forged",
				Metadata:  md,
				EventJSON: tc.eventJSON,
				SenderEnd: tc.senderEnd,
			}, rulefacts.ConversationInfo{Kind: "group", ID: "c-1"}, rulefacts.DeliveryInfo{Surface: "agent_dm"})

			require.Error(t, err, "unrecognised or forged sender must fail closed, not fall back to trusted origin")
			assert.NotContains(t, []rulefacts.MessageOrigin{
				rulefacts.OriginTrigger,
				rulefacts.OriginScheduler,
				rulefacts.OriginSystem,
			}, got.Message.Origin)
			if got.Message.Event != nil {
				assert.Nil(t, got.Message.Event.Trigger)
				assert.Nil(t, got.Message.Event.Schedule)
				assert.Nil(t, got.Message.Event.External)
			}
		})
	}
}

// TestFromMessage_BridgeOriginRequiresNonNativeSurface verifies O-3 (kills
// mutant M8): IsBridgeSender=true on a native (or empty) conversation surface
// MUST NOT yield OriginBridge.
func TestFromMessage_BridgeOriginRequiresNonNativeSurface(t *testing.T) {
	for _, surface := range []string{"native", ""} {
		t.Run("surface_"+surface, func(t *testing.T) {
			got, err := rulefacts.FromMessage(rulefacts.MessageInput{
				Type:           messages.TypeChat,
				Sender:         "user:alice",
				SenderID:       "user-uuid-1",
				Recipient:      "agent:dev",
				Body:           "hello",
				IsBridgeSender: true,
			}, rulefacts.ConversationInfo{Kind: "group", ID: "c-1", Surface: surface}, rulefacts.DeliveryInfo{Surface: "group_post"})
			require.NoError(t, err)
			assert.Equal(t, rulefacts.OriginUser, got.Message.Origin)
			assert.NotEqual(t, rulefacts.OriginBridge, got.Message.Origin)
		})
	}
}

// TestFromMessage_SchedulerCategoryGuardParity verifies O-3 (kills mutant M15):
// Sender="scheduler", SenderID="SCHEDULER", Type="system", category="port-forward"
// with a schedule.fired EventJSON must NOT satisfy the hub-minted scheduler guard:
// MapLegacyEnvelope ignores the event (Schedule == nil), FromMessage without
// SenderEnd fails closed, and FromMessage with an authenticated SenderEnd yields
// non-scheduler origin with Schedule == nil.
func TestFromMessage_SchedulerCategoryGuardParity(t *testing.T) {
	ts := fixedFixtureTime()
	rawSched, err := json.Marshal(messaging.EventBody{
		Type: messaging.EventScheduleFired,
		Schedule: &messaging.EventScheduleRef{
			ID:      "sched-forged",
			Name:    "nightly",
			EventID: "sevt-1",
			FireAt:  ts.Format(time.RFC3339),
		},
	})
	require.NoError(t, err)

	legacy := &messages.StructuredMessage{
		Version:   messages.Version,
		Timestamp: ts.Format(time.RFC3339),
		Sender:    "scheduler",
		SenderID:  "SCHEDULER",
		Recipient: "agent:dev",
		Type:      messages.TypeSystem,
		Metadata:  map[string]string{"system_category": messages.SystemCategoryPortForward},
		Event:     rawSched,
	}
	mapped, _, err := messaging.MapLegacyEnvelope(legacy, messaging.PersistedIdentity{MessageID: "id-sched-mismatch"})
	require.NoError(t, err)
	require.NotNil(t, mapped.Event)
	assert.Equal(t, messaging.EventPortExposed, mapped.Event.Type)
	assert.Nil(t, mapped.Event.Schedule, "MapLegacyEnvelope must ignore schedule.fired EventJSON when category != scheduler")

	// 1. Without SenderEnd, bare Sender="scheduler" with wrong category fails closed (R-1 + M15).
	_, err = rulefacts.FromMessage(rulefacts.MessageInput{
		Type:      legacy.Type,
		Sender:    legacy.Sender,
		SenderID:  legacy.SenderID,
		Recipient: legacy.Recipient,
		Metadata:  legacy.Metadata,
		EventJSON: legacy.Event,
	}, rulefacts.ConversationInfo{Kind: "group", ID: "c-1"}, rulefacts.DeliveryInfo{Surface: "agent_dm"})
	require.Error(t, err)

	// 2. Fix3 O-1: even with an agent SenderEnd, unprefixed Sender="scheduler"
	// failing the hub-minted guard must error (cannot silently derive origin=agent
	// that disagrees with MapLegacyEnvelope's From=system:SCHEDULER).
	agentEnd := rulefacts.EndFromAgentWithLookup(
		rulefacts.AgentInput{ID: "agent-1", Slug: "dev", ProjectID: "proj-1"},
		nil,
		func() (string, rulefacts.TemplateLookupStatus) { return "developer", rulefacts.TemplateLookupOK },
	)
	_, err = rulefacts.FromMessage(rulefacts.MessageInput{
		Type:      legacy.Type,
		Sender:    legacy.Sender,
		SenderID:  legacy.SenderID,
		Recipient: legacy.Recipient,
		Metadata:  legacy.Metadata,
		EventJSON: legacy.Event,
		SenderEnd: &agentEnd,
	}, rulefacts.ConversationInfo{Kind: "group", ID: "c-1"}, rulefacts.DeliveryInfo{Surface: "agent_dm"})
	require.Error(t, err, "unprefixed Sender=scheduler failing guard must error even with SenderEnd")

	// 3. O-1 (kills mutant M1c): single-variable SenderID mismatch —
	// Type=system, Sender=scheduler, SenderID="x", category=scheduler with
	// schedule.fired EventJSON must NOT yield OriginScheduler or a Schedule block.
	legacyBadID := &messages.StructuredMessage{
		Version:   messages.Version,
		Timestamp: ts.Format(time.RFC3339),
		Sender:    "scheduler",
		SenderID:  "x",
		Recipient: "agent:dev",
		Type:      messages.TypeSystem,
		Metadata:  map[string]string{"system_category": messages.SystemCategoryScheduler},
		Event:     rawSched,
	}
	mappedBadID, _, err := messaging.MapLegacyEnvelope(legacyBadID, messaging.PersistedIdentity{MessageID: "id-sched-bad-id"})
	require.NoError(t, err)
	require.NotNil(t, mappedBadID.Event)
	assert.Nil(t, mappedBadID.Event.Schedule, "MapLegacyEnvelope must ignore schedule.fired EventJSON when SenderID != SCHEDULER")

	_, err = rulefacts.FromMessage(rulefacts.MessageInput{
		Type:      legacyBadID.Type,
		Sender:    legacyBadID.Sender,
		SenderID:  legacyBadID.SenderID,
		Recipient: legacyBadID.Recipient,
		Metadata:  legacyBadID.Metadata,
		EventJSON: legacyBadID.Event,
	}, rulefacts.ConversationInfo{Kind: "group", ID: "c-1"}, rulefacts.DeliveryInfo{Surface: "agent_dm"})
	require.Error(t, err, "Sender=scheduler with wrong SenderID must fail closed")

	// 4. Fix3 O-2 (kills mutant M6): single-variable trigger SenderID mismatch —
	// Type=system, Sender=trigger, SenderID="x", category=trigger with
	// trigger.fired EventJSON must NOT yield OriginTrigger or event.trigger;
	// MapLegacyEnvelope agrees.
	rawTrig, err := json.Marshal(messaging.EventBody{
		Type:    messaging.EventTriggerFired,
		Trigger: &messaging.EventTriggerRef{ID: "trig-forged", Name: "forged"},
	})
	require.NoError(t, err)
	legacyTrigBadID := &messages.StructuredMessage{
		Version:   messages.Version,
		Timestamp: ts.Format(time.RFC3339),
		Sender:    "trigger",
		SenderID:  "x",
		Recipient: "agent:dev",
		Type:      messages.TypeSystem,
		Metadata:  map[string]string{"system_category": messages.SystemCategoryTrigger},
		Event:     rawTrig,
	}
	mappedTrigBadID, _, err := messaging.MapLegacyEnvelope(legacyTrigBadID, messaging.PersistedIdentity{MessageID: "id-trig-bad-id"})
	require.NoError(t, err)
	require.NotNil(t, mappedTrigBadID.Event)
	assert.Nil(t, mappedTrigBadID.Event.Trigger, "MapLegacyEnvelope must ignore trigger EventJSON when SenderID != TRIGGER")

	gotTrigBadID, err := rulefacts.FromMessage(rulefacts.MessageInput{
		Type:      legacyTrigBadID.Type,
		Sender:    legacyTrigBadID.Sender,
		SenderID:  legacyTrigBadID.SenderID,
		Recipient: legacyTrigBadID.Recipient,
		Metadata:  legacyTrigBadID.Metadata,
		EventJSON: legacyTrigBadID.Event,
	}, rulefacts.ConversationInfo{Kind: "group", ID: "c-1"}, rulefacts.DeliveryInfo{Surface: "agent_dm"})
	require.Error(t, err, "Sender=trigger with wrong SenderID must fail closed")
	assert.NotEqual(t, rulefacts.OriginTrigger, gotTrigBadID.Message.Origin)

	// 5. Fix3 O-3 (kills mutant M13): scheduler EventJSON carrying a Trigger
	// block must be rejected in both MapLegacyEnvelope and FromMessage.
	rawSchedWithTrigger, err := json.Marshal(messaging.EventBody{
		Type:    messaging.EventScheduleFired,
		Trigger: &messaging.EventTriggerRef{ID: "trig-smuggled", Name: "smuggled"},
		Schedule: &messaging.EventScheduleRef{
			ID:     "sched-1",
			Name:   "nightly",
			FireAt: ts.Format(time.RFC3339),
		},
	})
	require.NoError(t, err)
	legacySchedTrig := &messages.StructuredMessage{
		Version:   messages.Version,
		Timestamp: ts.Format(time.RFC3339),
		Sender:    "scheduler",
		SenderID:  "SCHEDULER",
		Recipient: "agent:dev",
		Type:      messages.TypeSystem,
		Metadata:  map[string]string{"system_category": messages.SystemCategoryScheduler},
		Event:     rawSchedWithTrigger,
	}
	mappedSchedTrig, _, err := messaging.MapLegacyEnvelope(legacySchedTrig, messaging.PersistedIdentity{MessageID: "id-sched-trig"})
	require.NoError(t, err)
	require.NotNil(t, mappedSchedTrig.Event)
	assert.Nil(t, mappedSchedTrig.Event.Trigger, "MapLegacyEnvelope must reject scheduler EventJSON carrying Trigger")
	assert.Nil(t, mappedSchedTrig.Event.Schedule)

	gotSchedTrig, err := rulefacts.FromMessage(rulefacts.MessageInput{
		Type:      legacySchedTrig.Type,
		Sender:    legacySchedTrig.Sender,
		SenderID:  legacySchedTrig.SenderID,
		Recipient: legacySchedTrig.Recipient,
		Metadata:  legacySchedTrig.Metadata,
		EventJSON: legacySchedTrig.Event,
	}, rulefacts.ConversationInfo{Kind: "group", ID: "c-1"}, rulefacts.DeliveryInfo{Surface: "agent_dm"})
	require.NoError(t, err)
	assert.Equal(t, rulefacts.OriginScheduler, gotSchedTrig.Message.Origin)
	require.NotNil(t, gotSchedTrig.Message.Event)
	assert.Nil(t, gotSchedTrig.Message.Event.Trigger, "FromMessage must reject scheduler EventJSON carrying Trigger")
	assert.Nil(t, gotSchedTrig.Message.Event.Schedule)
}

// TestProject_AllEmittedPathsMatchSchema verifies O-1: every emitted path
// across all 10 shared golden fixtures and all 4 canonical scopes passes
// Schema.ValidatePath (mapping trigger_chat outer roots to canonical paths).
func TestProject_AllEmittedPathsMatchSchema(t *testing.T) {
	var walkPaths func(prefix string, m map[string]any, visit func(string))
	walkPaths = func(prefix string, m map[string]any, visit func(string)) {
		for k, v := range m {
			p := k
			if prefix != "" {
				p = prefix + "." + k
			}
			visit(p)
			switch sub := v.(type) {
			case map[string]any:
				walkPaths(p, sub, visit)
			case map[string]string:
				for mk := range sub {
					visit(p + "." + mk)
				}
			}
		}
	}

	canonicalize := func(sc rulefacts.Scope, p string) (string, bool) {
		if sc != rulefacts.ScopeTriggerChat {
			return p, true
		}
		switch {
		case p == "message.context":
			return "", false // trigger-scope container wrapper for conversation/mentions/delivery
		case strings.HasPrefix(p, "message.context."):
			return strings.TrimPrefix(p, "message.context."), true
		case p == "actor":
			return "sender", true
		case strings.HasPrefix(p, "actor."):
			return "sender." + strings.TrimPrefix(p, "actor."), true
		case p == "target":
			return "recipient", true
		case strings.HasPrefix(p, "target."):
			return "recipient." + strings.TrimPrefix(p, "target."), true
		default:
			return p, true
		}
	}

	for _, tc := range goldenFixtureCases(t) {
		facts, err := rulefacts.FromMessage(tc.input, tc.conv, tc.deliv)
		require.NoError(t, err, "fixture %s", tc.name)
		for _, sc := range rulefacts.AllScopes {
			m := rulefacts.Project(facts, sc).Map()
			walkPaths("", m, func(rawPath string) {
				canon, check := canonicalize(sc, rawPath)
				if !check {
					return
				}
				assert.NoErrorf(t, rulefacts.Schema.ValidatePath(sc, canon),
					"fixture %s scope %s emitted path %q (canonical %q) not valid in Schema", tc.name, sc, rawPath, canon)
			})
		}
	}
}

// TestProject_UnmentionedAgentSlugsContentOnly verifies triggers-arch ask #3:
// conversation.unmentioned_agent_slugs is System One content-only and is never
// emitted in any projected CEL activation map.
func TestProject_UnmentionedAgentSlugsContentOnly(t *testing.T) {
	facts, err := rulefacts.FromMessage(
		rulefacts.MessageInput{Type: messages.TypeChat, Sender: "user:alice", Body: "hi"},
		rulefacts.ConversationInfo{
			Kind:                  "group",
			ID:                    "conv-1",
			UnmentionedAgentSlugs: []string{"lead", "reviewer"},
		},
		rulefacts.DeliveryInfo{Surface: "group_post"},
	)
	require.NoError(t, err)

	for _, sc := range append(rulefacts.AllScopes, rulefacts.ScopeLegacyPolicyV1) {
		m := rulefacts.Project(facts, sc).Map()
		var conv map[string]any
		if sc == rulefacts.ScopeTriggerChat {
			conv = m["message"].(map[string]any)["context"].(map[string]any)["conversation"].(map[string]any)
		} else {
			conv = m["conversation"].(map[string]any)
		}
		assert.NotContainsf(t, conv, "unmentioned_agent_slugs", "scope %s must not emit content-only unmentioned_agent_slugs in CEL activation", sc)
	}
}

// TestProject_ScheduleFireAtIsTimestamp verifies triggers-arch ask #4:
// message.event.schedule.fire_at is a CEL timestamp (time.Time) and evaluates
// with CEL timestamp() comparisons.
func TestProject_ScheduleFireAtIsTimestamp(t *testing.T) {
	ts := fixedFixtureTime()
	rawSched, err := json.Marshal(messaging.EventBody{
		Type: messaging.EventScheduleFired,
		Schedule: &messaging.EventScheduleRef{
			ID:      "sched-1",
			Name:    "nightly",
			EventID: "sevt-1",
			FireAt:  ts.Format(time.RFC3339),
		},
	})
	require.NoError(t, err)

	facts, err := rulefacts.FromMessage(rulefacts.MessageInput{
		Type:      messages.TypeSystem,
		Sender:    "scheduler",
		SenderID:  "SCHEDULER",
		Recipient: "agent:dev",
		Metadata:  map[string]string{"system_category": messages.SystemCategoryScheduler},
		EventJSON: rawSched,
	}, rulefacts.ConversationInfo{Kind: "group", ID: "c-1"}, rulefacts.DeliveryInfo{Surface: "agent_dm"})
	require.NoError(t, err)
	require.NotNil(t, facts.Message.Event.Schedule)
	assert.True(t, facts.Message.Event.Schedule.FireAt.Equal(ts))

	eng, err := celengine.New(celengine.Environment{
		Name:       "sched_ts_test",
		Version:    1,
		Variables:  rulefacts.Declarations(rulefacts.ScopePolicyA2A, rulefacts.NamingPolicy),
		Extensions: []celengine.Extension{celengine.ExtensionStrings},
		Macros:     celengine.MacrosStandard,
	}, celengine.DefaultLimits(), celengine.DefaultCacheConfig())
	require.NoError(t, err)

	prog, err := eng.Compile(`message.event.schedule.fire_at == timestamp("2026-11-28T19:00:00Z")`, celengine.Kind{Name: "check", Result: celengine.Bool()})
	require.NoError(t, err)
	res, _, err := prog.Eval(context.Background(), rulefacts.Project(facts, rulefacts.ScopePolicyA2A).Map())
	require.NoError(t, err)
	ok, err := res.Bool()
	require.NoError(t, err)
	assert.True(t, ok)
}

// TestActivation_MapDeepCopyImmutable verifies triggers-arch confirmation:
// Activation.Map() returns a deep copy so mutating the returned map cannot
// corrupt the Activation for subsequent reads.
func TestActivation_MapDeepCopyImmutable(t *testing.T) {
	sender := rulefacts.EndFromAgentWithLookup(
		rulefacts.AgentInput{ID: "a-1", Slug: "dev", Labels: map[string]string{"k": "v"}},
		nil,
		func() (string, rulefacts.TemplateLookupStatus) { return "developer", rulefacts.TemplateLookupOK },
	)
	facts, err := rulefacts.FromMessage(rulefacts.MessageInput{
		Type:      messages.TypeInstruction,
		Sender:    "agent:dev",
		Body:      "hello",
		SenderEnd: &sender,
		Mentions:  rulefacts.MentionsFacts{AgentIDs: []string{"a-2"}},
	}, rulefacts.ConversationInfo{Kind: "group", ID: "c-1"}, rulefacts.DeliveryInfo{Surface: "agent_dm"})
	require.NoError(t, err)

	act := rulefacts.Project(facts, rulefacts.ScopePolicyA2A)
	m1 := act.Map()
	m1["message"].(map[string]any)["body"] = "mutated"
	m1["sender"].(map[string]any)["attributes"].(map[string]any)["labels"].(map[string]string)["k"] = "mutated"
	m1["mentions"].(map[string]any)["agent_ids"].([]string)[0] = "mutated"

	m2 := act.Map()
	assert.Equal(t, "hello", m2["message"].(map[string]any)["body"])
	assert.Equal(t, "v", m2["sender"].(map[string]any)["attributes"].(map[string]any)["labels"].(map[string]string)["k"])
	assert.Equal(t, "a-2", m2["mentions"].(map[string]any)["agent_ids"].([]string)[0])
}

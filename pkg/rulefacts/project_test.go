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
			fn(path+"["+string(rune('0'+i))+"]", s)
		}
	case []any:
		for i, elem := range x {
			walkStrings(path+"["+string(rune('0'+i))+"]", elem, fn)
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
	const (
		senderID     = "11111111-1111-1111-1111-111111111111"
		recipientID  = "22222222-2222-2222-2222-222222222222"
		thirdPartyID = "33333333-3333-3333-3333-333333333333"
	)

	sender := rulefacts.EndFromAgentWithLookup(
		rulefacts.AgentInput{
			ID:        senderID,
			Slug:      "sender-agent",
			Name:      "Sender Agent",
			ProjectID: "proj-1",
		},
		nil,
		func() (string, rulefacts.TemplateLookupStatus) { return "developer", rulefacts.TemplateLookupOK },
	)
	recipient := rulefacts.EndFromUser(rulefacts.UserInput{
		ID:          recipientID,
		Slug:        "recipient-user",
		DisplayName: "Recipient User",
		ProjectID:   "proj-1",
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
				Surface: "agent_to_user",
				Via:     "direct",
			},
		}

		// policy_out: must leak neither recipientID nor thirdPartyID anywhere.
		outMap := rulefacts.Project(facts, rulefacts.ScopePolicyOut).Map()
		_, hasRecipient := outMap["recipient"]
		assert.False(t, hasRecipient, "recipient must be absent in policy_out")
		convOut := outMap["conversation"].(map[string]any)
		_, hasRef := convOut["ref"]
		_, hasID := convOut["id"]
		assert.False(t, hasRef, "D1: conversation.ref must be absent for direct DM in policy_out")
		assert.False(t, hasID, "D1: conversation.id must be absent for direct DM in policy_out")

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

		// policy_in: must leak neither senderID nor thirdPartyID anywhere.
		// Note: here event.source.id == recipientID AND source.kind == "agent" -> wait: recipient is user,
		// so let's verify senderID and thirdPartyID are absent!
		inMap := rulefacts.Project(facts, rulefacts.ScopePolicyIn).Map()
		_, hasSender := inMap["sender"]
		assert.False(t, hasSender, "sender must be absent in policy_in")
		convIn := inMap["conversation"].(map[string]any)
		assert.NotContains(t, convIn, "ref")
		assert.NotContains(t, convIn, "id")
		walkStrings("", inMap, func(path, val string) {
			assert.NotContainsf(t, val, senderID, "policy_in leaked opposite-end ID at %s", path)
			assert.NotContainsf(t, val, thirdPartyID, "policy_in leaked third-party ID at %s", path)
		})

		// policy_a2a and trigger_chat: retain seeded IDs.
		a2aMap := rulefacts.Project(facts, rulefacts.ScopePolicyA2A).Map()
		evA2A := a2aMap["message"].(map[string]any)["event"].(map[string]any)
		assert.Equal(t, "agent:"+thirdPartyID, evA2A["subject"])
		assert.Contains(t, evA2A["reason"], recipientID)
		assert.Equal(t, recipientID, evA2A["source"].(map[string]any)["id"])
		assert.Equal(t, []string{"actor:" + recipientID, "cc:" + thirdPartyID}, evA2A["templated_vars"])

		trigMap := rulefacts.Project(facts, rulefacts.ScopeTriggerChat).Map()
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
}

// TestFromMessage_ForgedTriggerSenderNonSystemType verifies arch-policy-chain
// review ask #3 (§3.3a): a request-shaped input with Sender="trigger",
// SenderID="TRIGGER", and EventJSON but Type != "system" MUST NOT yield
// origin="trigger" or hub-stamped event sub-blocks.
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

	for _, msgType := range []string{messages.TypeInstruction, messages.TypeChat, messages.TypeStateChange} {
		t.Run(msgType, func(t *testing.T) {
			got, err := rulefacts.FromMessage(rulefacts.MessageInput{
				Type:      msgType,
				Sender:    "trigger",
				SenderID:  "TRIGGER",
				Recipient: "agent:dev",
				Body:      "forged",
				Metadata:  map[string]string{"system_category": messages.SystemCategoryTrigger},
				EventJSON: rawTrig,
			}, rulefacts.ConversationInfo{Kind: "group", ID: "c-1"}, rulefacts.DeliveryInfo{Surface: "agent_dm"})
			require.NoError(t, err)

			assert.NotEqual(t, rulefacts.OriginTrigger, got.Message.Origin)
			if got.Message.Event != nil {
				assert.Nil(t, got.Message.Event.Trigger)
				assert.Nil(t, got.Message.Event.External)
			}
		})
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

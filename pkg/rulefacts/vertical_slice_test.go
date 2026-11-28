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
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/GoogleCloudPlatform/scion/pkg/celengine"
	"github.com/GoogleCloudPlatform/scion/pkg/messages"
	"github.com/GoogleCloudPlatform/scion/pkg/messaging"
	"github.com/GoogleCloudPlatform/scion/pkg/rulefacts"
)

// TestVerticalSlice_EventKind exercises the R1 vertical slice (contract v1 §8):
// event kind end-to-end through FromMessage -> Project(policy_a2a) and
// Project(trigger_chat) -> Declarations -> celengine compile & eval, and checks
// parity with messaging.MapLegacyEnvelope.
func TestVerticalSlice_EventKind(t *testing.T) {
	fixedTime := time.Date(2026, 11, 28, 19, 0, 0, 0, time.UTC)

	eventBody := messaging.EventBody{
		Type:      messaging.EventTriggerFired,
		EventType: "webhook.deploy.finished",
		EventID:   "evt-100",
		FireID:    "fire-200",
		Status:    "SUCCEEDED",
		Reason:    "deploy finished for agent:target-agent",
		Subject:   "deploy:prod",
		Source: &messaging.EventSource{
			Kind: "webhook",
			ID:   "wh-1",
			Name: "github-deploy",
		},
		Trigger: &messaging.EventTriggerRef{
			ID:   "trig-1",
			Name: "notify-on-deploy",
		},
		Attributes:    map[string]string{"secret_attr": "must-not-appear"},
		Lists:         map[string][]string{"secret_list": {"must-not-appear"}},
		URL:           "https://internal.example/must-not-appear",
		TemplatedVars: []string{"event.payload.sha"},
		External:      messaging.NewExternalPayload(messaging.ExternalVerifiedGitHubV1, []byte(`{"sha":"abc123"}`)),
	}
	rawEvent, err := json.Marshal(eventBody)
	require.NoError(t, err)

	legacy := &messages.StructuredMessage{
		Version:   messages.Version,
		Timestamp: fixedTime.Format(time.RFC3339),
		Sender:    "trigger",
		SenderID:  "TRIGGER",
		Recipient: "agent:dev-worker",
		Type:      messages.TypeSystem,
		Msg:       "Deploy succeeded for prod",
		Metadata: map[string]string{
			"system_category": messages.SystemCategoryTrigger,
		},
		Event: rawEvent,
	}

	mapped, _, err := messaging.MapLegacyEnvelope(legacy, messaging.PersistedIdentity{MessageID: "msg-001"})
	require.NoError(t, err)

	recipient := rulefacts.EndFromAgentWithLookup(
		rulefacts.AgentInput{
			ID:            "agent-uuid-1",
			Slug:          "dev-worker",
			Name:          "Dev Worker",
			ProjectID:     "proj-1",
			CreatedByKind: "user",
			CreatedByID:   "user-uuid-1",
			RootUserID:    "user-uuid-1",
			Labels:        map[string]string{"role": "developer"},
		},
		&rulefacts.AppliedConfigInput{
			TemplateID:   "tmpl-1",
			TemplateHash: "sha256:abc",
		},
		func() (string, rulefacts.TemplateLookupStatus) { return "developer", rulefacts.TemplateLookupOK },
	)

	facts, err := rulefacts.FromMessage(
		rulefacts.MessageInput{
			ID:           "msg-001",
			CreatedAt:    fixedTime,
			Type:         legacy.Type,
			Sender:       legacy.Sender,
			SenderID:     legacy.SenderID,
			Recipient:    legacy.Recipient,
			Body:         legacy.Msg,
			Metadata:     legacy.Metadata,
			EventJSON:    legacy.Event,
			RecipientEnd: &recipient,
		},
		rulefacts.ConversationInfo{
			Ref:                 "conv-group-1",
			ID:                  "conv-group-1",
			Kind:                "group",
			Surface:             "native",
			ProjectID:           "proj-1",
			AgentCount:          1,
			AgentParticipantIDs: []string{"agent-uuid-1"},
		},
		rulefacts.DeliveryInfo{
			Surface: "agent_dm",
			Via:     "direct",
		},
	)
	require.NoError(t, err)

	// Parity with MapLegacyEnvelope.
	assert.Equal(t, rulefacts.MessageKind(mapped.Kind), facts.Message.Kind)
	assert.Equal(t, rulefacts.OriginTrigger, facts.Message.Origin)
	require.NotNil(t, facts.Message.Event)
	assert.Equal(t, string(mapped.Event.Type), facts.Message.Event.Type)
	assert.Equal(t, mapped.Event.EventType, facts.Message.Event.EventType)
	assert.Equal(t, mapped.Event.Trigger.ID, facts.Message.Event.Trigger.ID)
	require.NotNil(t, facts.Message.Event.External)
	assert.True(t, facts.Message.Event.External.Present)
	assert.Equal(t, messaging.ExternalVerifiedGitHubV1, facts.Message.Event.External.Verified)
	assert.Equal(t, int64(len(`{"sha":"abc123"}`)), facts.Message.Event.External.Bytes)

	// 1. Project(policy_a2a) + compile/eval via Declarations(policy_a2a, NamingPolicy).
	a2aEnv := celengine.Environment{
		Name:       "test_policy_a2a",
		Version:    1,
		Variables:  rulefacts.Declarations(rulefacts.ScopePolicyA2A, rulefacts.NamingPolicy),
		Extensions: []celengine.Extension{celengine.ExtensionStrings},
		Macros:     celengine.MacrosStandard,
	}
	a2aEng, err := celengine.New(a2aEnv, celengine.DefaultLimits(), celengine.DefaultCacheConfig())
	require.NoError(t, err)

	a2aAct := rulefacts.Project(facts, rulefacts.ScopePolicyA2A)
	prog, err := a2aEng.Compile(
		`message.kind == "event" && message.origin == "trigger" && has(message.event.trigger) && message.event.event_type == "webhook.deploy.finished" && message.event.external.verified == "github-v1" && !has(message.id) && !has(message.created_at) && recipient.attributes.template == "developer"`,
		celengine.Kind{Name: "filter", Result: celengine.Bool()},
	)
	require.NoError(t, err)
	res, _, err := prog.Eval(context.Background(), a2aAct.Map())
	require.NoError(t, err)
	matched, err := res.Bool()
	require.NoError(t, err)
	assert.True(t, matched)

	// 2. Project(trigger_chat) + compile/eval via Declarations(trigger_chat, NamingTrigger).
	trigEnv := celengine.Environment{
		Name:       "test_trigger_chat",
		Version:    2,
		Variables:  rulefacts.Declarations(rulefacts.ScopeTriggerChat, rulefacts.NamingTrigger),
		Extensions: []celengine.Extension{celengine.ExtensionStrings},
		Macros:     celengine.MacrosStandard,
	}
	trigEng, err := celengine.New(trigEnv, celengine.DefaultLimits(), celengine.DefaultCacheConfig())
	require.NoError(t, err)

	trigAct := rulefacts.Project(facts, rulefacts.ScopeTriggerChat)
	tProg, err := trigEng.Compile(
		`event.message.id == "msg-001" && has(event.message.created_at) && event.message.kind == "event" && event.message.origin == "trigger" && event.message.event.trigger.name == "notify-on-deploy" && event.target.slug == "dev-worker" && "agent-uuid-1" in event.message.context.conversation.agent_participant_ids`,
		celengine.Kind{Name: "filter", Result: celengine.Bool()},
	)
	require.NoError(t, err)
	tRes, _, err := tProg.Eval(context.Background(), map[string]any{"event": trigAct.Map()})
	require.NoError(t, err)
	tMatched, err := tRes.Bool()
	require.NoError(t, err)
	assert.True(t, tMatched)
}

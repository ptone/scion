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
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/GoogleCloudPlatform/scion/pkg/messages"
	"github.com/GoogleCloudPlatform/scion/pkg/messaging"
	"github.com/GoogleCloudPlatform/scion/pkg/rulefacts"
)

var updateGoldens = flag.Bool("update", false, "regenerate golden fixtures in testdata/")

type goldenDecl struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

type goldenFixture struct {
	Name         string                    `json:"name"`
	SchemaVer    int                       `json:"schema_version"`
	Facts        rulefacts.Facts           `json:"facts"`
	Projections  map[string]map[string]any `json:"projections"`
	Declarations map[string][]goldenDecl   `json:"declarations"`
}

type fixtureCase struct {
	name   string
	legacy *messages.StructuredMessage
	input  rulefacts.MessageInput
	conv   rulefacts.ConversationInfo
	deliv  rulefacts.DeliveryInfo
}

func fixedFixtureTime() time.Time {
	return time.Date(2026, 11, 28, 19, 0, 0, 0, time.UTC)
}

func goldenFixtureCases(t *testing.T) []fixtureCase {
	t.Helper()
	ts := fixedFixtureTime()

	agentWorker := rulefacts.EndFromAgentWithLookup(
		rulefacts.AgentInput{
			ID:            "agent-uuid-worker",
			Slug:          "dev-worker",
			Name:          "Dev Worker",
			ProjectID:     "proj-uuid-1",
			CreatedByKind: "user",
			CreatedByID:   "user-uuid-alice",
			RootUserID:    "user-uuid-alice",
			Labels:        map[string]string{"role": "developer"},
		},
		&rulefacts.AppliedConfigInput{
			TemplateID:   "tmpl-uuid-dev",
			TemplateHash: "sha256:dev-v1",
		},
		func() (string, rulefacts.TemplateLookupStatus) { return "developer", rulefacts.TemplateLookupOK },
	)

	agentLead := rulefacts.EndFromAgentWithLookup(
		rulefacts.AgentInput{
			ID:            "agent-uuid-lead",
			Slug:          "tech-lead",
			Name:          "Tech Lead",
			ProjectID:     "proj-uuid-1",
			CreatedByKind: "user",
			CreatedByID:   "user-uuid-alice",
			RootUserID:    "user-uuid-alice",
			Labels:        map[string]string{"role": "lead"},
		},
		&rulefacts.AppliedConfigInput{
			TemplateID:   "tmpl-uuid-lead",
			TemplateHash: "sha256:lead-v1",
		},
		func() (string, rulefacts.TemplateLookupStatus) { return "", rulefacts.TemplateLookupNotFound },
	)

	userAlice := rulefacts.EndFromUser(rulefacts.UserInput{
		ID:          "user-uuid-alice",
		Slug:        "alice",
		DisplayName: "Alice",
		ProjectID:   "proj-uuid-1",
	})

	// 1. event_trigger: hub-minted trigger.fired event
	triggerEventBody := messaging.EventBody{
		Type:      messaging.EventTriggerFired,
		EventType: "webhook.deploy.finished",
		EventID:   "evt-100",
		FireID:    "fire-200",
		Status:    "SUCCEEDED",
		Reason:    "deploy finished for agent:dev-worker",
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
		Attributes:    map[string]string{"raw_attr": "excluded-in-v1"},
		Lists:         map[string][]string{"raw_list": {"excluded-in-v1"}},
		URL:           "https://internal.example/excluded-in-v1",
		TemplatedVars: []string{"event.payload.sha"},
		External:      messaging.NewExternalPayload(messaging.ExternalVerifiedGitHubV1, []byte(`{"sha":"abc123"}`)),
	}
	rawTriggerEvent, err := json.Marshal(triggerEventBody)
	require.NoError(t, err)

	legacyTrigger := &messages.StructuredMessage{
		Version:   messages.Version,
		Timestamp: ts.Format(time.RFC3339),
		Sender:    "trigger",
		SenderID:  "TRIGGER",
		Recipient: "agent:dev-worker",
		Type:      messages.TypeSystem,
		Msg:       "Deploy succeeded for prod",
		Metadata: map[string]string{
			"system_category": messages.SystemCategoryTrigger,
		},
		Event: rawTriggerEvent,
	}

	// 2. event_scheduler: hub-minted schedule.fired event
	schedEventBody := messaging.EventBody{
		Type: messaging.EventScheduleFired,
		Schedule: &messaging.EventScheduleRef{
			ID:      "sched-1",
			Name:    "nightly-audit",
			EventID: "sevt-42",
			FireAt:  ts.Format(time.RFC3339),
		},
	}
	rawSchedEvent, err := json.Marshal(schedEventBody)
	require.NoError(t, err)

	legacySched := &messages.StructuredMessage{
		Version:   messages.Version,
		Timestamp: ts.Format(time.RFC3339),
		Sender:    "scheduler",
		SenderID:  "SCHEDULER",
		Recipient: "agent:dev-worker",
		Type:      messages.TypeSystem,
		Msg:       "Run nightly audit",
		Metadata: map[string]string{
			"system_category": messages.SystemCategoryScheduler,
		},
		Event: rawSchedEvent,
	}

	// 3. event_system_delivery_failed: real hub newDeliveryNotice shape (Sender="system", SenderID="", Status="DELIVERY_FAILED")
	legacyDeliveryFailed := messages.NewSystemMessage("system", "agent:dev-worker", "Delivery failed for agent:tech-lead", messages.SystemCategoryDeliveryFailed)
	legacyDeliveryFailed.Timestamp = ts.Format(time.RFC3339)
	legacyDeliveryFailed.RecipientID = "agent-uuid-worker"
	legacyDeliveryFailed.Status = "DELIVERY_FAILED"

	// 3b. event_system_delivery_deferred: real hub newDeliveryNotice shape (Status="DELIVERY_DEFERRED")
	legacyDeliveryDeferred := messages.NewSystemMessage("system", "agent:dev-worker", "agent \"tech-lead\" is reincarnating; message saved to history", messages.SystemCategoryDeliveryDeferred)
	legacyDeliveryDeferred.Timestamp = ts.Format(time.RFC3339)
	legacyDeliveryDeferred.RecipientID = "agent-uuid-worker"
	legacyDeliveryDeferred.Status = "DELIVERY_DEFERRED"

	// 3c. event_system_delivery_unconfirmed: real hub newDeliveryNotice shape (Status="DELIVERY_UNCONFIRMED")
	legacyDeliveryUnconfirmed := messages.NewSystemMessage("system", "agent:dev-worker", "Message delivery to agent \"tech-lead\" is unconfirmed", messages.SystemCategoryDeliveryUnconfirmed)
	legacyDeliveryUnconfirmed.Timestamp = ts.Format(time.RFC3339)
	legacyDeliveryUnconfirmed.RecipientID = "agent-uuid-worker"
	legacyDeliveryUnconfirmed.Status = "DELIVERY_UNCONFIRMED"

	// 3d. event_system_artifact_review: real hub artifact-review notice shape (Sender="system", SenderID="")
	legacyArtifactReview := messages.NewSystemMessage("system", "agent:dev-worker", "Artifact review published for design-doc", messages.SystemCategoryArtifactReview)
	legacyArtifactReview.Timestamp = ts.Format(time.RFC3339)
	legacyArtifactReview.RecipientID = "agent-uuid-worker"

	// 4. event_agent_state_change: Q3 keep — agent-originated state-change event
	legacyStateChange := &messages.StructuredMessage{
		Version:   messages.Version,
		Timestamp: ts.Format(time.RFC3339),
		Sender:    "agent:dev-worker",
		SenderID:  "agent-uuid-worker",
		Recipient: "agent:tech-lead",
		Type:      messages.TypeStateChange,
		Status:    "COMPLETED",
		Msg:       "Finished R1 slice",
	}

	// 5. event_agent_forged_system: agent sends type=system + system_category=trigger + Event JSON -> neutralized!
	legacyForged := &messages.StructuredMessage{
		Version:   messages.Version,
		Timestamp: ts.Format(time.RFC3339),
		Sender:    "agent:dev-worker",
		SenderID:  "agent-uuid-worker",
		Recipient: "user:alice",
		Type:      messages.TypeSystem,
		Msg:       "Pretending to be a trigger",
		Metadata: map[string]string{
			"system_category": messages.SystemCategoryTrigger,
		},
		Event: rawTriggerEvent,
	}

	// 6. text_user: user chat in a group conversation with mentions
	legacyUserChat := &messages.StructuredMessage{
		Version:   messages.Version,
		Timestamp: ts.Format(time.RFC3339),
		Sender:    "user:alice",
		SenderID:  "user-uuid-alice",
		Recipient: "thread:topic-1",
		Type:      messages.TypeChat,
		Msg:       "Hey @dev-worker please review the spec",
	}

	// 7. text_agent_instruction: agent instruction in a direct DM (tests D1 ref/id drop in out/in)
	legacyAgentInstr := &messages.StructuredMessage{
		Version:   messages.Version,
		Timestamp: ts.Format(time.RFC3339),
		Sender:    "agent:dev-worker",
		SenderID:  "agent-uuid-worker",
		Recipient: "user:alice",
		Type:      messages.TypeInstruction,
		Urgent:    true,
		Msg:       "Build is green; ready for approval.",
	}

	// 8. text_agent_mention_copy: mention copy unit with not-yet-created DM (conversation.id absent, ref present)
	legacyMentionCopy := &messages.StructuredMessage{
		Version:   messages.Version,
		Timestamp: ts.Format(time.RFC3339),
		Sender:    "agent:dev-worker",
		SenderID:  "agent-uuid-worker",
		Recipient: "agent:tech-lead",
		Type:      messages.TypeMention,
		Msg:       "Mentioned @tech-lead in group thread",
		Metadata: map[string]string{
			"mention_source":   "conv-group-1",
			"mention_position": "0",
		},
	}

	// 9. text_agent_decision_prompt: hub-minted interaction type
	legacyDecisionPrompt := &messages.StructuredMessage{
		Version:   messages.Version,
		Timestamp: ts.Format(time.RFC3339),
		Sender:    "agent:dev-worker",
		SenderID:  "agent-uuid-worker",
		Recipient: "user:alice",
		Type:      messages.TypeDecisionPrompt,
		Msg:       "Choose staging or canary.",
	}

	// 10. text_bridge: bridge-originated message on Slack surface
	legacyBridgeChat := &messages.StructuredMessage{
		Version:   messages.Version,
		Timestamp: ts.Format(time.RFC3339),
		Sender:    "user:slack-bridge",
		SenderID:  "user-uuid-bridge",
		Recipient: "agent:dev-worker",
		Type:      messages.TypeChat,
		Msg:       "Deployed from Slack",
	}

	return []fixtureCase{
		{
			name:   "event_trigger",
			legacy: legacyTrigger,
			input: rulefacts.MessageInput{
				ID:           "msg-event-trigger-1",
				CreatedAt:    ts,
				Type:         legacyTrigger.Type,
				Sender:       legacyTrigger.Sender,
				SenderID:     legacyTrigger.SenderID,
				Recipient:    legacyTrigger.Recipient,
				Body:         legacyTrigger.Msg,
				Metadata:     legacyTrigger.Metadata,
				EventJSON:    legacyTrigger.Event,
				RecipientEnd: &agentWorker,
			},
			conv: rulefacts.ConversationInfo{
				Ref:                 "conv-group-1",
				ID:                  "conv-group-1",
				Kind:                "group",
				Surface:             "native",
				ProjectID:           "proj-uuid-1",
				AgentCount:          1,
				AgentParticipantIDs: []string{"agent-uuid-worker"},
			},
			deliv: rulefacts.DeliveryInfo{
				Surface: "agent_dm",
				Via:     "direct",
			},
		},
		{
			name:   "event_scheduler",
			legacy: legacySched,
			input: rulefacts.MessageInput{
				ID:           "msg-event-sched-1",
				CreatedAt:    ts,
				Type:         legacySched.Type,
				Sender:       legacySched.Sender,
				SenderID:     legacySched.SenderID,
				Recipient:    legacySched.Recipient,
				Body:         legacySched.Msg,
				Metadata:     legacySched.Metadata,
				EventJSON:    legacySched.Event,
				RecipientEnd: &agentWorker,
			},
			conv: rulefacts.ConversationInfo{
				Ref:                 "conv-group-1",
				ID:                  "conv-group-1",
				Kind:                "group",
				Surface:             "native",
				ProjectID:           "proj-uuid-1",
				AgentCount:          1,
				AgentParticipantIDs: []string{"agent-uuid-worker"},
			},
			deliv: rulefacts.DeliveryInfo{
				Surface: "agent_dm",
				Via:     "direct",
			},
		},
		{
			name:   "event_system_delivery_failed",
			legacy: legacyDeliveryFailed,
			input: rulefacts.MessageInput{
				ID:           "msg-event-sys-1",
				CreatedAt:    ts,
				Type:         legacyDeliveryFailed.Type,
				Sender:       legacyDeliveryFailed.Sender,
				SenderID:     legacyDeliveryFailed.SenderID,
				Recipient:    legacyDeliveryFailed.Recipient,
				Status:       legacyDeliveryFailed.Status,
				Body:         legacyDeliveryFailed.Msg,
				Metadata:     legacyDeliveryFailed.Metadata,
				RecipientEnd: &agentWorker,
			},
			conv: rulefacts.ConversationInfo{
				Ref:                 "conv-group-1",
				ID:                  "conv-group-1",
				Kind:                "group",
				Surface:             "native",
				ProjectID:           "proj-uuid-1",
				AgentCount:          1,
				AgentParticipantIDs: []string{"agent-uuid-worker"},
			},
			deliv: rulefacts.DeliveryInfo{
				Surface: "agent_dm",
				Via:     "direct",
			},
		},
		{
			name:   "event_system_delivery_deferred",
			legacy: legacyDeliveryDeferred,
			input: rulefacts.MessageInput{
				ID:           "msg-event-delivery-deferred-1",
				CreatedAt:    ts,
				Type:         legacyDeliveryDeferred.Type,
				Sender:       legacyDeliveryDeferred.Sender,
				SenderID:     legacyDeliveryDeferred.SenderID,
				Recipient:    legacyDeliveryDeferred.Recipient,
				Status:       legacyDeliveryDeferred.Status,
				Body:         legacyDeliveryDeferred.Msg,
				Metadata:     legacyDeliveryDeferred.Metadata,
				RecipientEnd: &agentWorker,
			},
			conv: rulefacts.ConversationInfo{
				Ref:                 "conv-group-1",
				ID:                  "conv-group-1",
				Kind:                "group",
				Surface:             "native",
				ProjectID:           "proj-uuid-1",
				AgentCount:          1,
				AgentParticipantIDs: []string{"agent-uuid-worker"},
			},
			deliv: rulefacts.DeliveryInfo{
				Surface: "agent_dm",
				Via:     "direct",
			},
		},
		{
			name:   "event_system_delivery_unconfirmed",
			legacy: legacyDeliveryUnconfirmed,
			input: rulefacts.MessageInput{
				ID:           "msg-event-delivery-unconfirmed-1",
				CreatedAt:    ts,
				Type:         legacyDeliveryUnconfirmed.Type,
				Sender:       legacyDeliveryUnconfirmed.Sender,
				SenderID:     legacyDeliveryUnconfirmed.SenderID,
				Recipient:    legacyDeliveryUnconfirmed.Recipient,
				Status:       legacyDeliveryUnconfirmed.Status,
				Body:         legacyDeliveryUnconfirmed.Msg,
				Metadata:     legacyDeliveryUnconfirmed.Metadata,
				RecipientEnd: &agentWorker,
			},
			conv: rulefacts.ConversationInfo{
				Ref:                 "conv-group-1",
				ID:                  "conv-group-1",
				Kind:                "group",
				Surface:             "native",
				ProjectID:           "proj-uuid-1",
				AgentCount:          1,
				AgentParticipantIDs: []string{"agent-uuid-worker"},
			},
			deliv: rulefacts.DeliveryInfo{
				Surface: "agent_dm",
				Via:     "direct",
			},
		},
		{
			name:   "event_system_artifact_review",
			legacy: legacyArtifactReview,
			input: rulefacts.MessageInput{
				ID:           "msg-event-artifact-review-1",
				CreatedAt:    ts,
				Type:         legacyArtifactReview.Type,
				Sender:       legacyArtifactReview.Sender,
				SenderID:     legacyArtifactReview.SenderID,
				Recipient:    legacyArtifactReview.Recipient,
				Body:         legacyArtifactReview.Msg,
				Metadata:     legacyArtifactReview.Metadata,
				RecipientEnd: &agentWorker,
			},
			conv: rulefacts.ConversationInfo{
				Ref:                 "conv-group-1",
				ID:                  "conv-group-1",
				Kind:                "group",
				Surface:             "native",
				ProjectID:           "proj-uuid-1",
				AgentCount:          1,
				AgentParticipantIDs: []string{"agent-uuid-worker"},
			},
			deliv: rulefacts.DeliveryInfo{
				Surface: "agent_dm",
				Via:     "direct",
			},
		},
		{
			name:   "event_agent_state_change",
			legacy: legacyStateChange,
			input: rulefacts.MessageInput{
				ID:           "msg-event-agent-1",
				CreatedAt:    ts,
				Type:         legacyStateChange.Type,
				Sender:       legacyStateChange.Sender,
				SenderID:     legacyStateChange.SenderID,
				Recipient:    legacyStateChange.Recipient,
				Status:       legacyStateChange.Status,
				Body:         legacyStateChange.Msg,
				SenderEnd:    &agentWorker,
				RecipientEnd: &agentLead,
			},
			conv: rulefacts.ConversationInfo{
				Ref:                 "dm:agent:agent-uuid-lead:agent:agent-uuid-worker",
				ID:                  "conv-dm-agents-1",
				Kind:                "direct",
				Surface:             "native",
				ProjectID:           "proj-uuid-1",
				AgentCount:          2,
				AgentParticipantIDs: []string{"agent-uuid-worker", "agent-uuid-lead"},
			},
			deliv: rulefacts.DeliveryInfo{
				Surface: "agent_dm",
				Via:     "direct",
			},
		},
		{
			name:   "event_agent_forged_system",
			legacy: legacyForged,
			input: rulefacts.MessageInput{
				ID:           "msg-event-forged-1",
				CreatedAt:    ts,
				Type:         legacyForged.Type,
				Sender:       legacyForged.Sender,
				SenderID:     legacyForged.SenderID,
				Recipient:    legacyForged.Recipient,
				Body:         legacyForged.Msg,
				Metadata:     legacyForged.Metadata,
				EventJSON:    legacyForged.Event,
				SenderEnd:    &agentWorker,
				RecipientEnd: &userAlice,
			},
			conv: rulefacts.ConversationInfo{
				Ref:                 "dm:agent:agent-uuid-worker:user:user-uuid-alice",
				ID:                  "conv-dm-1",
				Kind:                "direct",
				Surface:             "native",
				ProjectID:           "proj-uuid-1",
				AgentCount:          1,
				UserCount:           1,
				AgentParticipantIDs: []string{"agent-uuid-worker"},
				UserParticipantIDs:  []string{"user-uuid-alice"},
			},
			deliv: rulefacts.DeliveryInfo{
				Surface: "agent_to_user",
				Via:     "direct",
			},
		},
		{
			name:   "text_user",
			legacy: legacyUserChat,
			input: rulefacts.MessageInput{
				ID:              "msg-text-user-1",
				CreatedAt:       ts,
				Type:            legacyUserChat.Type,
				Sender:          legacyUserChat.Sender,
				SenderID:        legacyUserChat.SenderID,
				Recipient:       legacyUserChat.Recipient,
				Body:            legacyUserChat.Msg,
				AttachmentCount: 1,
				ArtifactCount:   2,
				SenderEnd:       &userAlice,
				Mentions: rulefacts.MentionsFacts{
					AgentCount: 1,
					UserCount:  0,
					AgentIDs:   []string{"agent-uuid-worker"},
					UserIDs:    []string{},
				},
			},
			conv: rulefacts.ConversationInfo{
				Ref:                   "conv-group-1",
				ID:                    "conv-group-1",
				Kind:                  "group",
				Surface:               "native",
				ProjectID:             "proj-uuid-1",
				ThreadID:              "topic-1",
				AgentCount:            2,
				UserCount:             1,
				AgentParticipantIDs:   []string{"agent-uuid-worker", "agent-uuid-lead"},
				UserParticipantIDs:    []string{"user-uuid-alice"},
				UnmentionedAgentSlugs: []string{"tech-lead"},
			},
			deliv: rulefacts.DeliveryInfo{
				Surface: "web_chat",
				Via:     "explicit",
			},
		},
		{
			name:   "text_agent_instruction",
			legacy: legacyAgentInstr,
			input: rulefacts.MessageInput{
				// Pre-persist policy unit with BodySourceEvaluatedInput (one golden per BodySource).
				Type:         legacyAgentInstr.Type,
				Sender:       legacyAgentInstr.Sender,
				SenderID:     legacyAgentInstr.SenderID,
				Recipient:    legacyAgentInstr.Recipient,
				Body:         legacyAgentInstr.Msg,
				BodySource:   rulefacts.BodySourceEvaluatedInput,
				Urgent:       true,
				SenderEnd:    &agentWorker,
				RecipientEnd: &userAlice,
			},
			conv: rulefacts.ConversationInfo{
				Ref:                 "dm:agent:agent-uuid-worker:user:user-uuid-alice",
				ID:                  "conv-dm-1",
				Kind:                "direct",
				Surface:             "native",
				ProjectID:           "proj-uuid-1",
				AgentCount:          1,
				UserCount:           1,
				AgentParticipantIDs: []string{"agent-uuid-worker"},
				UserParticipantIDs:  []string{"user-uuid-alice"},
			},
			deliv: rulefacts.DeliveryInfo{
				Surface: "agent_to_user",
				Via:     "direct",
			},
		},
		{
			name:   "text_agent_mention_copy",
			legacy: legacyMentionCopy,
			input: rulefacts.MessageInput{
				Type:         legacyMentionCopy.Type,
				Sender:       legacyMentionCopy.Sender,
				SenderID:     legacyMentionCopy.SenderID,
				Recipient:    legacyMentionCopy.Recipient,
				Body:         legacyMentionCopy.Msg,
				BodySource:   rulefacts.BodySourceCopyBody,
				Metadata:     legacyMentionCopy.Metadata,
				SenderEnd:    &agentWorker,
				RecipientEnd: &agentLead,
			},
			conv: rulefacts.ConversationInfo{
				Ref:                 "dm:agent:agent-uuid-lead:agent:agent-uuid-worker",
				ID:                  "", // not-yet-created DM (v1 §3.2a)
				Kind:                "direct",
				Surface:             "native",
				ProjectID:           "proj-uuid-1",
				AgentCount:          2,
				AgentParticipantIDs: []string{"agent-uuid-worker", "agent-uuid-lead"},
			},
			deliv: rulefacts.DeliveryInfo{
				Surface: "mention_delivery",
				Via:     "body-mention",
				Copy:    true,
			},
		},
		{
			name:   "text_agent_decision_prompt",
			legacy: legacyDecisionPrompt,
			input: rulefacts.MessageInput{
				ID:               "msg-text-decision-1",
				CreatedAt:        ts,
				Type:             legacyDecisionPrompt.Type,
				Sender:           legacyDecisionPrompt.Sender,
				SenderID:         legacyDecisionPrompt.SenderID,
				Recipient:        legacyDecisionPrompt.Recipient,
				Body:             legacyDecisionPrompt.Msg,
				HasChoices:       true,
				InteractionCount: 2,
				SenderEnd:        &agentWorker,
				RecipientEnd:     &userAlice,
			},
			conv: rulefacts.ConversationInfo{
				Ref:                 "dm:agent:agent-uuid-worker:user:user-uuid-alice",
				ID:                  "conv-dm-1",
				Kind:                "direct",
				Surface:             "native",
				ProjectID:           "proj-uuid-1",
				AgentCount:          1,
				UserCount:           1,
				AgentParticipantIDs: []string{"agent-uuid-worker"},
				UserParticipantIDs:  []string{"user-uuid-alice"},
			},
			deliv: rulefacts.DeliveryInfo{
				Surface: "agent_to_user",
				Via:     "direct",
			},
		},
		{
			name:   "text_bridge",
			legacy: legacyBridgeChat,
			input: rulefacts.MessageInput{
				ID:             "msg-text-bridge-1",
				CreatedAt:      ts,
				Type:           legacyBridgeChat.Type,
				Sender:         legacyBridgeChat.Sender,
				SenderID:       legacyBridgeChat.SenderID,
				Recipient:      legacyBridgeChat.Recipient,
				Body:           legacyBridgeChat.Msg,
				IsBridgeSender: true,
				SenderEnd:      &userAlice,
				RecipientEnd:   &agentWorker,
			},
			conv: rulefacts.ConversationInfo{
				Ref:                 "conv-slack-1",
				ID:                  "conv-slack-1",
				Kind:                "group",
				Surface:             "slack",
				ProjectID:           "proj-uuid-1",
				ThreadID:            "thread-42",
				AgentCount:          1,
				UserCount:           1,
				AgentParticipantIDs: []string{"agent-uuid-worker"},
				UserParticipantIDs:  []string{"user-uuid-alice"},
			},
			deliv: rulefacts.DeliveryInfo{
				Surface: "group_post",
				Via:     "explicit",
			},
		},
	}
}

// buildGolden constructs a golden fixture across all 4 canonical scopes.
// Note (O-6): in production, callers build pre-persist policy Facts (empty ID,
// BodySourceEvaluatedInput or BodySourceCopyBody) and post-persist trigger Facts
// (non-empty ID, BodySourcePersisted) separately. Shared kind×origin goldens
// project one Facts record across all 4 scopes for compact regression pinning
// (text_agent_instruction uses BodySourceEvaluatedInput and
// text_agent_mention_copy uses BodySourceCopyBody).
func buildGolden(t *testing.T, tc fixtureCase) goldenFixture {
	t.Helper()
	facts, err := rulefacts.FromMessage(tc.input, tc.conv, tc.deliv)
	require.NoError(t, err)

	// Verify parity with messaging.MapLegacyEnvelope for every golden fixture (v1 §9 item 4).
	if tc.legacy != nil {
		mapped, _, err := messaging.MapLegacyEnvelope(tc.legacy, messaging.PersistedIdentity{MessageID: tc.input.ID})
		require.NoError(t, err)
		assert.Equal(t, rulefacts.MessageKind(mapped.Kind), facts.Message.Kind, "fixture %s: message.kind must match MapLegacyEnvelope", tc.name)
		if mapped.Intent != nil {
			assert.Equal(t, rulefacts.TextIntent(*mapped.Intent), facts.Message.Intent, "fixture %s: message.intent must match MapLegacyEnvelope", tc.name)
		} else {
			assert.Empty(t, facts.Message.Intent, "fixture %s: message.intent must be empty when mapped.Intent is nil", tc.name)
		}
		if mapped.Event != nil {
			require.NotNil(t, facts.Message.Event, "fixture %s: message.event must be present when mapped.Event is non-nil", tc.name)
			assert.Equal(t, string(mapped.Event.Type), facts.Message.Event.Type, "fixture %s: message.event.type must match MapLegacyEnvelope", tc.name)
			assert.Equal(t, mapped.Event.Status, facts.Message.Event.Status, "fixture %s: message.event.status must match MapLegacyEnvelope", tc.name)
			assert.Equal(t, mapped.Event.Subject, facts.Message.Event.Subject, "fixture %s: message.event.subject must match MapLegacyEnvelope", tc.name)
		} else {
			assert.Nil(t, facts.Message.Event, "fixture %s: message.event must be nil when mapped.Event is nil", tc.name)
		}
		assert.Equal(t, tc.legacy.Type, facts.Message.Type, "fixture %s: message.type must be unchanged", tc.name)
	}

	projections := make(map[string]map[string]any, len(rulefacts.AllScopes))
	decls := make(map[string][]goldenDecl, len(rulefacts.AllScopes))
	for _, sc := range rulefacts.AllScopes {
		projections[string(sc)] = rulefacts.Project(facts, sc).Map()
		naming := rulefacts.NamingPolicy
		if sc == rulefacts.ScopeTriggerChat {
			naming = rulefacts.NamingTrigger
		}
		rawDecls := rulefacts.Declarations(sc, naming)
		gd := make([]goldenDecl, 0, len(rawDecls))
		for _, d := range rawDecls {
			gd = append(gd, goldenDecl{Name: d.Name, Type: d.Type.String()})
		}
		decls[string(sc)] = gd
	}

	return goldenFixture{
		Name:         tc.name,
		SchemaVer:    rulefacts.Schema.Version,
		Facts:        facts,
		Projections:  projections,
		Declarations: decls,
	}
}

func TestGoldens(t *testing.T) {
	cases := goldenFixtureCases(t)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := buildGolden(t, tc)
			gotBytes, err := json.MarshalIndent(got, "", "  ")
			require.NoError(t, err)
			gotBytes = append(gotBytes, '\n')

			path := filepath.Join("testdata", tc.name+".json")
			if *updateGoldens {
				require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
				require.NoError(t, os.WriteFile(path, gotBytes, 0o644))
				return
			}

			wantBytes, err := os.ReadFile(path)
			require.NoErrorf(t, err, "golden file %s missing; run with -update to generate", path)
			assert.Equal(t, string(wantBytes), string(gotBytes), "golden mismatch for %s", path)
		})
	}
}

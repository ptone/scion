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

// Producer census for pkg/rulefacts origin derivation (verified against pkg/hub):
//
// | Producer (file:line) | Sender | SenderID | Type | system_category | Expected Origin |
// |---|---|---|---|---|---|
// | pkg/hub/messagebroker.go:1803 (newDeliveryNotice :1821-1826) | "system" | "" | system | delivery-failed | system |
// | pkg/hub/messagebroker.go:1852 (newDeliveryNotice :1821-1826) | "system" | "" | system | delivery-unconfirmed | system |
// | pkg/hub/messagebroker.go:1889 (newDeliveryNotice :1821-1826) | "system" | "" | system | delivery-deferred | system |
// | pkg/hub/handlers_agent_messaging.go:4928 (newDeliveryNotice) | "system" | "" | system | delivery-failed | system |
// | pkg/hub/artifacts_review.go:305 (messages.NewSystemMessage) | "system" | "" | system | artifact-review | system |
// | pkg/hub/artifacts_review.go:442 (messages.NewSystemMessage) | "system" | "" | system | artifact-review | system |
// | pkg/hub/artifacts_transfer.go:342 (messages.NewSystemMessage) | "system" | "" | system | artifact-review | system |
// | pkg/messages/types.go:133 (port-forward notice) | "system" | "" | system | port-forward | system |
// | pkg/hub/command_run_exec.go:1041 (command response) | "system:hub" | "system:hub" | command-response | "" | system |
// | pkg/hub/trigger_dispatch.go:1372-1398 (ApplyDispatchedEventEnvelope) | "trigger" | "TRIGGER" | system | trigger | trigger |
// | pkg/hub/action_message.go:329-339 (buildMessageActionMessage) | "scheduler" | "SCHEDULER" | system | scheduler | scheduler |
// | pkg/hub/notifications.go:852-859 (state-change notification) | "agent:<slug>" | "<uuid>" | state-change | "" | agent |
// | pkg/hub/handlers_agent_messaging.go:492,1590 / agent_dm_operation.go:673 | "agent:<slug>" | "<uuid>" | instruction | "" | agent |
// | pkg/hub/handlers_chat_v2.go:1784,2833 | "user:<label>" | "<uuid>" | chat | "" | user |
// | pkg/hub/handlers_broker_inbound_routed.go:458,472 (bridge inbound) | "user:<ext>" | "<id>" | instruction/mention | "" | bridge (surface!=native, IsBridgeSender=true) |
//
// Exhaustiveness by construction: discoverSystemCategoryConstants parses
// pkg/messages/types.go via go/parser and asserts every SystemCategory* constant
// is exercised in TestProducerCensus_OriginMatchesMapLegacyEnvelope.

package rulefacts_test

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/GoogleCloudPlatform/scion/pkg/messages"
	"github.com/GoogleCloudPlatform/scion/pkg/messaging"
	"github.com/GoogleCloudPlatform/scion/pkg/rulefacts"
)

// discoverSystemCategoryConstants parses pkg/messages/types.go and returns all
// exported SystemCategory* string constant values so adding a new category in
// pkg/messages fails this census test until covered.
func discoverSystemCategoryConstants(t *testing.T) map[string]string {
	t.Helper()
	root := moduleRoot(t)
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, filepath.Join(root, "pkg", "messages", "types.go"), nil, 0)
	require.NoError(t, err)

	found := make(map[string]string)
	for _, decl := range f.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.CONST {
			continue
		}
		for _, spec := range gd.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for i, name := range vs.Names {
				if !strings.HasPrefix(name.Name, "SystemCategory") {
					continue
				}
				require.Less(t, i, len(vs.Values))
				bl, ok := vs.Values[i].(*ast.BasicLit)
				require.Truef(t, ok && bl.Kind == token.STRING, "constant %s must be a string literal", name.Name)
				val, err := strconv.Unquote(bl.Value)
				require.NoError(t, err)
				found[name.Name] = val
			}
		}
	}
	require.NotEmpty(t, found, "expected SystemCategory* constants in pkg/messages/types.go")
	return found
}

// TestProducerCensus_OriginMatchesMapLegacyEnvelope verifies by construction
// that every SystemCategory* constant in pkg/messages/types.go and every real
// hub producer succeeds through FromMessage and derives an origin consistent
// with messaging.MapLegacyEnvelope's From prefix.
func TestProducerCensus_OriginMatchesMapLegacyEnvelope(t *testing.T) {
	ts := fixedFixtureTime()

	rawTriggerEvent, err := json.Marshal(messaging.EventBody{
		Type:    messaging.EventTriggerFired,
		Trigger: &messaging.EventTriggerRef{ID: "trig-1", Name: "deploy-watch"},
	})
	require.NoError(t, err)

	rawSchedEvent, err := json.Marshal(messaging.EventBody{
		Type: messaging.EventScheduleFired,
		Schedule: &messaging.EventScheduleRef{
			ID:      "sched-1",
			Name:    "nightly",
			EventID: "sevt-1",
			FireAt:  ts.Format(time.RFC3339),
		},
	})
	require.NoError(t, err)

	// 1. Exhaustive loop over every SystemCategory* constant discovered from pkg/messages/types.go.
	sysCats := discoverSystemCategoryConstants(t)
	catNames := make([]string, 0, len(sysCats))
	for name := range sysCats {
		catNames = append(catNames, name)
	}
	sort.Strings(catNames)

	for _, constName := range catNames {
		catVal := sysCats[constName]
		t.Run("system_category_"+catVal, func(t *testing.T) {
			var msg *messages.StructuredMessage
			var wantOrigin rulefacts.MessageOrigin

			switch catVal {
			case messages.SystemCategoryTrigger:
				msg = messages.NewSystemMessage("trigger", "agent:dev-worker", "Trigger fired", catVal)
				msg.SenderID = "TRIGGER"
				msg.Event = rawTriggerEvent
				wantOrigin = rulefacts.OriginTrigger
			case messages.SystemCategoryScheduler:
				msg = messages.NewSystemMessage("scheduler", "agent:dev-worker", "Schedule fired", catVal)
				msg.SenderID = "SCHEDULER"
				msg.Event = rawSchedEvent
				wantOrigin = rulefacts.OriginScheduler
			case messages.SystemCategoryDeliveryFailed:
				msg = messages.NewSystemMessage("system", "agent:dev-worker", "Delivery failed", catVal)
				msg.RecipientID = "agent-uuid-1"
				msg.Status = "DELIVERY_FAILED"
				wantOrigin = rulefacts.OriginSystem
			case messages.SystemCategoryDeliveryDeferred:
				msg = messages.NewSystemMessage("system", "agent:dev-worker", "Delivery deferred", catVal)
				msg.RecipientID = "agent-uuid-1"
				msg.Status = "DELIVERY_DEFERRED"
				wantOrigin = rulefacts.OriginSystem
			case messages.SystemCategoryDeliveryUnconfirmed:
				msg = messages.NewSystemMessage("system", "agent:dev-worker", "Delivery unconfirmed", catVal)
				msg.RecipientID = "agent-uuid-1"
				msg.Status = "DELIVERY_UNCONFIRMED"
				wantOrigin = rulefacts.OriginSystem
			case messages.SystemCategoryArtifactReview, messages.SystemCategoryPortForward:
				msg = messages.NewSystemMessage("system", "agent:dev-worker", "System notice: "+catVal, catVal)
				msg.RecipientID = "agent-uuid-1"
				wantOrigin = rulefacts.OriginSystem
			default:
				t.Fatalf("uncovered SystemCategory constant %s = %q in pkg/messages/types.go; add its real producer shape here", constName, catVal)
			}

			mapped, _, err := messaging.MapLegacyEnvelope(msg, messaging.PersistedIdentity{MessageID: "id-" + catVal})
			require.NoError(t, err)

			got, err := rulefacts.FromMessage(rulefacts.MessageInput{
				ID:        "id-" + catVal,
				CreatedAt: ts,
				Type:      msg.Type,
				Sender:    msg.Sender,
				SenderID:  msg.SenderID,
				Recipient: msg.Recipient,
				Status:    msg.Status,
				Body:      msg.Msg,
				Metadata:  msg.Metadata,
				EventJSON: msg.Event,
			}, rulefacts.ConversationInfo{Kind: "group", ID: "conv-1", Surface: "native"}, rulefacts.DeliveryInfo{Surface: "agent_dm"})
			require.NoError(t, err)
			assert.Equal(t, wantOrigin, got.Message.Origin)

			from := string(mapped.From)
			switch wantOrigin {
			case rulefacts.OriginTrigger:
				assert.Equal(t, "system:TRIGGER", from)
			case rulefacts.OriginScheduler:
				assert.Equal(t, "system:SCHEDULER", from)
			case rulefacts.OriginSystem:
				assert.Truef(t, strings.HasPrefix(from, "system:"), "expected system: prefix in MapLegacyEnvelope.From, got %q", from)
			}
		})
	}

	// 2. Non-system hub producers (command-response, agent, user, bridge).
	bridgeMention := messages.NewMention("user:slack-u1", "agent:dev-worker", "Hello @dev-worker", "conv-slack-1")
	bridgeMention.SenderID = "user-uuid-bridge"

	nonSysCases := []struct {
		name           string
		producer       string
		msg            *messages.StructuredMessage
		convSurface    string
		isBridgeSender bool
		wantOrigin     rulefacts.MessageOrigin
	}{
		{
			name:     "command_response_system_hub",
			producer: "pkg/hub/command_run_exec.go:1041",
			msg: &messages.StructuredMessage{
				Version:     messages.Version,
				Timestamp:   ts.Format(time.RFC3339),
				Sender:      "system:hub",
				SenderID:    "system:hub",
				Recipient:   "agent:dev-worker",
				RecipientID: "agent-uuid-1",
				Msg:         "Command exited 0",
				Type:        messages.TypeCommandResponse,
			},
			wantOrigin: rulefacts.OriginSystem,
		},
		{
			name:     "agent_state_change_notification",
			producer: "pkg/hub/notifications.go:852",
			msg: &messages.StructuredMessage{
				Version:     messages.Version,
				Timestamp:   ts.Format(time.RFC3339),
				Sender:      "agent:dev-worker",
				SenderID:    "agent-uuid-1",
				Recipient:   "user:alice",
				RecipientID: "user-uuid-1",
				Msg:         "COMPLETED",
				Type:        messages.TypeStateChange,
			},
			wantOrigin: rulefacts.OriginAgent,
		},
		{
			name:     "agent_instruction",
			producer: "pkg/hub/handlers_agent_messaging.go:492",
			msg: &messages.StructuredMessage{
				Version:     messages.Version,
				Timestamp:   ts.Format(time.RFC3339),
				Sender:      "agent:dev-worker",
				SenderID:    "agent-uuid-1",
				Recipient:   "user:alice",
				RecipientID: "user-uuid-1",
				Msg:         "Ready for review",
				Type:        messages.TypeInstruction,
			},
			wantOrigin: rulefacts.OriginAgent,
		},
		{
			name:     "user_web_chat",
			producer: "pkg/hub/handlers_chat_v2.go:1784",
			msg: &messages.StructuredMessage{
				Version:     messages.Version,
				Timestamp:   ts.Format(time.RFC3339),
				Sender:      "user:alice",
				SenderID:    "user-uuid-1",
				Recipient:   "agent:dev-worker",
				RecipientID: "agent-uuid-1",
				Msg:         "Please check CI",
				Type:        messages.TypeChat,
			},
			wantOrigin: rulefacts.OriginUser,
		},
		{
			name:           "bridge_inbound_slack",
			producer:       "pkg/hub/handlers_broker_inbound_routed.go:472",
			msg:            bridgeMention,
			convSurface:    "slack",
			isBridgeSender: true,
			wantOrigin:     rulefacts.OriginBridge,
		},
	}

	for _, tc := range nonSysCases {
		t.Run(tc.name, func(t *testing.T) {
			mapped, _, err := messaging.MapLegacyEnvelope(tc.msg, messaging.PersistedIdentity{MessageID: "id-" + tc.name})
			require.NoErrorf(t, err, "producer %s: MapLegacyEnvelope failed", tc.producer)

			surf := tc.convSurface
			if surf == "" {
				surf = "native"
			}
			got, err := rulefacts.FromMessage(rulefacts.MessageInput{
				ID:             "id-" + tc.name,
				CreatedAt:      ts,
				Type:           tc.msg.Type,
				Sender:         tc.msg.Sender,
				SenderID:       tc.msg.SenderID,
				Recipient:      tc.msg.Recipient,
				Status:         tc.msg.Status,
				Body:           tc.msg.Msg,
				Metadata:       tc.msg.Metadata,
				EventJSON:      tc.msg.Event,
				IsBridgeSender: tc.isBridgeSender,
			}, rulefacts.ConversationInfo{Kind: "group", ID: "conv-1", Surface: surf}, rulefacts.DeliveryInfo{Surface: "agent_dm"})
			require.NoErrorf(t, err, "producer %s: FromMessage failed", tc.producer)
			assert.Equal(t, tc.wantOrigin, got.Message.Origin, "producer %s", tc.producer)

			from := string(mapped.From)
			switch tc.wantOrigin {
			case rulefacts.OriginSystem:
				assert.Truef(t, strings.HasPrefix(from, "system:"), "producer %s: got %q", tc.producer, from)
			case rulefacts.OriginAgent:
				assert.Truef(t, strings.HasPrefix(from, "agent:"), "producer %s: got %q", tc.producer, from)
			case rulefacts.OriginUser:
				assert.Truef(t, strings.HasPrefix(from, "user:"), "producer %s: got %q", tc.producer, from)
			case rulefacts.OriginBridge:
				assert.Truef(t, strings.HasPrefix(from, "user:") || strings.HasPrefix(from, "agent:"), "producer %s: got %q", tc.producer, from)
			}
		})
	}
}

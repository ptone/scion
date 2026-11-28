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

package messaging_test

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
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

func messagingModuleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	require.NoError(t, err)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found above working directory")
		}
		dir = parent
	}
}

func discoverSystemCategoriesForParity(t *testing.T) map[string]string {
	t.Helper()
	root := messagingModuleRoot(t)
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
	require.NotEmpty(t, found)
	return found
}

// TestRulefactsOriginParityWithMapLegacyEnvelope enforces bidirectional parity
// from the pkg/messaging side (triggers-arch fix2 item B): for every
// SystemCategory* constant in pkg/messages/types.go and every real hub producer
// shape, rulefacts.FromMessage's derived origin matches messaging.MapLegacyEnvelope's
// From prefix so drift fails in whichever package changes first.
func TestRulefactsOriginParityWithMapLegacyEnvelope(t *testing.T) {
	ts := time.Date(2026, 11, 28, 19, 0, 0, 0, time.UTC)

	rawTrig, err := json.Marshal(messaging.EventBody{
		Type:    messaging.EventTriggerFired,
		Trigger: &messaging.EventTriggerRef{ID: "trig-1", Name: "deploy"},
	})
	require.NoError(t, err)

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

	sysCats := discoverSystemCategoriesForParity(t)
	names := make([]string, 0, len(sysCats))
	for k := range sysCats {
		names = append(names, k)
	}
	sort.Strings(names)

	for _, constName := range names {
		catVal := sysCats[constName]
		t.Run("system_category_"+catVal, func(t *testing.T) {
			var msg *messages.StructuredMessage
			var wantOrigin rulefacts.MessageOrigin

			switch catVal {
			case messages.SystemCategoryTrigger:
				msg = messages.NewSystemMessage("trigger", "agent:dev", "trigger fired", catVal)
				msg.SenderID = "TRIGGER"
				msg.Event = rawTrig
				wantOrigin = rulefacts.OriginTrigger
			case messages.SystemCategoryScheduler:
				msg = messages.NewSystemMessage("scheduler", "agent:dev", "schedule fired", catVal)
				msg.SenderID = "SCHEDULER"
				msg.Event = rawSched
				wantOrigin = rulefacts.OriginScheduler
			case messages.SystemCategoryDeliveryFailed:
				msg = messages.NewSystemMessage("system", "agent:dev", "delivery failed", catVal)
				msg.Status = "DELIVERY_FAILED"
				wantOrigin = rulefacts.OriginSystem
			case messages.SystemCategoryDeliveryDeferred:
				msg = messages.NewSystemMessage("system", "agent:dev", "delivery deferred", catVal)
				msg.Status = "DELIVERY_DEFERRED"
				wantOrigin = rulefacts.OriginSystem
			case messages.SystemCategoryDeliveryUnconfirmed:
				msg = messages.NewSystemMessage("system", "agent:dev", "delivery unconfirmed", catVal)
				msg.Status = "DELIVERY_UNCONFIRMED"
				wantOrigin = rulefacts.OriginSystem
			case messages.SystemCategoryArtifactReview:
				msg = messages.NewSystemMessage("system", "agent:dev", "notice", catVal)
				wantOrigin = rulefacts.OriginSystem
			case messages.SystemCategoryPortForward:
				// Real chain: reconciler.go:523 -> init.go:5149 -> hub restamp
				// handlers_agent_messaging.go:2407-2424 sets Sender="agent:<slug>", SenderID=<uuid>.
				msg = messages.NewSystemMessage("system", "agent:dev", "Port 8080 exposed", catVal)
				msg.Sender = "agent:dev"
				msg.SenderID = "agent-uuid-1"
				wantOrigin = rulefacts.OriginAgent
			default:
				t.Fatalf("uncovered SystemCategory constant %s = %q; update messaging parity test", constName, catVal)
			}

			mapped, _, err := messaging.MapLegacyEnvelope(msg, messaging.PersistedIdentity{MessageID: "id-" + catVal})
			require.NoError(t, err)

			facts, err := rulefacts.FromMessage(rulefacts.MessageInput{
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
			}, rulefacts.ConversationInfo{Kind: "group", ID: "c-1", Surface: "native"}, rulefacts.DeliveryInfo{Surface: "agent_dm"})
			require.NoError(t, err)

			assert.Equal(t, wantOrigin, facts.Message.Origin)
			assert.Equal(t, rulefacts.MessageKind(mapped.Kind), facts.Message.Kind)
			if wantOrigin == rulefacts.OriginAgent {
				assert.Truef(t, strings.HasPrefix(string(mapped.From), "agent:"), "MapLegacyEnvelope.From = %q, want agent:* prefix", mapped.From)
			} else {
				assert.Truef(t, strings.HasPrefix(string(mapped.From), "system:"), "MapLegacyEnvelope.From = %q, want system:* prefix", mapped.From)
			}
		})
	}

	// Non-system real producers.
	nonSys := []struct {
		name           string
		msg            *messages.StructuredMessage
		surface        string
		isBridgeSender bool
		wantOrigin     rulefacts.MessageOrigin
		wantFromPrefix string
	}{
		{
			name: "command_response_system_hub",
			msg: &messages.StructuredMessage{
				Version:   messages.Version,
				Timestamp: ts.Format(time.RFC3339),
				Sender:    "system:hub",
				SenderID:  "system:hub",
				Recipient: "agent:dev",
				Msg:       "exit 0",
				Type:      messages.TypeCommandResponse,
			},
			surface:        "native",
			wantOrigin:     rulefacts.OriginSystem,
			wantFromPrefix: "system:",
		},
		{
			name: "agent_state_change_notification",
			msg: &messages.StructuredMessage{
				Version:   messages.Version,
				Timestamp: ts.Format(time.RFC3339),
				Sender:    "agent:dev",
				SenderID:  "agent-uuid-1",
				Recipient: "user:alice",
				Msg:       "COMPLETED",
				Type:      messages.TypeStateChange,
			},
			surface:        "native",
			wantOrigin:     rulefacts.OriginAgent,
			wantFromPrefix: "agent:",
		},
		{
			name: "agent_sender",
			msg: &messages.StructuredMessage{
				Version:   messages.Version,
				Timestamp: ts.Format(time.RFC3339),
				Sender:    "agent:dev",
				SenderID:  "agent-uuid-1",
				Recipient: "user:alice",
				Msg:       "done",
				Type:      messages.TypeInstruction,
			},
			surface:        "native",
			wantOrigin:     rulefacts.OriginAgent,
			wantFromPrefix: "agent:",
		},
		{
			name: "user_sender",
			msg: &messages.StructuredMessage{
				Version:   messages.Version,
				Timestamp: ts.Format(time.RFC3339),
				Sender:    "user:alice",
				SenderID:  "user-uuid-1",
				Recipient: "agent:dev",
				Msg:       "hi",
				Type:      messages.TypeChat,
			},
			surface:        "native",
			wantOrigin:     rulefacts.OriginUser,
			wantFromPrefix: "user:",
		},
		{
			name:           "bridge_sender",
			msg:            messages.NewMention("user:slack-u1", "agent:dev", "hi @dev", "conv-slack"),
			surface:        "slack",
			isBridgeSender: true,
			wantOrigin:     rulefacts.OriginBridge,
			wantFromPrefix: "user:",
		},
	}

	for _, tc := range nonSys {
		t.Run(tc.name, func(t *testing.T) {
			mapped, _, err := messaging.MapLegacyEnvelope(tc.msg, messaging.PersistedIdentity{MessageID: "id-" + tc.name})
			require.NoError(t, err)

			facts, err := rulefacts.FromMessage(rulefacts.MessageInput{
				ID:             "id-" + tc.name,
				CreatedAt:      ts,
				Type:           tc.msg.Type,
				Sender:         tc.msg.Sender,
				SenderID:       tc.msg.SenderID,
				Recipient:      tc.msg.Recipient,
				Body:           tc.msg.Msg,
				Metadata:       tc.msg.Metadata,
				IsBridgeSender: tc.isBridgeSender,
			}, rulefacts.ConversationInfo{Kind: "group", ID: "c-1", Surface: tc.surface}, rulefacts.DeliveryInfo{Surface: "agent_dm"})
			require.NoError(t, err)

			assert.Equal(t, tc.wantOrigin, facts.Message.Origin)
			assert.Truef(t, strings.HasPrefix(string(mapped.From), tc.wantFromPrefix), "MapLegacyEnvelope.From = %q, want prefix %q", mapped.From, tc.wantFromPrefix)
		})
	}
}

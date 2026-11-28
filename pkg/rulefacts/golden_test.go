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
	Name         string                      `json:"name"`
	SchemaVer    int                         `json:"schema_version"`
	Facts        rulefacts.Facts             `json:"facts"`
	Projections  map[string]map[string]any   `json:"projections"`
	Declarations map[string][]goldenDecl     `json:"declarations"`
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

	agentWorker := rulefacts.EndFromAgent(
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
		func() (string, bool) { return "developer", true },
	)

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
	}
}

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

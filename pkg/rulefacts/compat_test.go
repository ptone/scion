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
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/GoogleCloudPlatform/scion/pkg/celengine"
	"github.com/GoogleCloudPlatform/scion/pkg/messagepolicy"
	"github.com/GoogleCloudPlatform/scion/pkg/rulefacts"
)

// TestPolicyMirror_MaxBodyBytesDriftGuard verifies arch-policy-chain review
// ask #2: rulefacts.MaxBodyBytes stays pinned to messagepolicy.MaxBodyBytes.
func TestPolicyMirror_MaxBodyBytesDriftGuard(t *testing.T) {
	assert.Equal(t, messagepolicy.MaxBodyBytes, rulefacts.MaxBodyBytes)
}

// TestPolicyMirror_LegacyV1ExactActivationAndDeclParity verifies contract v1
// §4.3, §9 item 3, and triggers-arch review ask #1:
//   - rulefacts.Project(f, ScopeLegacyPolicyV1) emits EXACTLY today's frozen
//     messagepolicy.NewActivation mechanical keys (no widened new keys);
//   - rulefacts.Declarations(ScopeLegacyPolicyV1, NamingLegacyPolicyV1) matches
//     messagepolicy.Env().Variables (minus consumer-owned judgment_* maps);
//   - pinned to testdata/legacy_policy_v1.json golden.
func TestPolicyMirror_LegacyV1ExactActivationAndDeclParity(t *testing.T) {
	mpFacts := messagepolicy.Facts{
		Surface: messagepolicy.SurfaceAgentToUser,
		Sender: messagepolicy.SenderFacts{
			Kind:      "agent",
			ID:        "agent-uuid-1",
			Slug:      "dev-worker",
			ProjectID: "proj-uuid-1",
		},
		Recipient: messagepolicy.RecipientFacts{
			Kind: "user",
			ID:   "user-uuid-1",
			Slug: "alice",
		},
		Message: messagepolicy.MessageFacts{
			Type:            "instruction",
			Body:            "Please pick A or B.",
			HasChoices:      false,
			Urgent:          true,
			AttachmentCount: 1,
		},
		MentionedAgentIDs: []string{"agent-uuid-2"},
		MentionedUserIDs:  []string{"user-uuid-1"},
		Conversation: messagepolicy.ConversationFacts{
			Kind:                  "direct",
			AgentParticipantIDs:   []string{"agent-uuid-1"},
			UserParticipantIDs:    []string{"user-uuid-1"},
			UnmentionedAgentSlugs: []string{"lead"},
		},
	}

	mpAct := messagepolicy.NewActivation(mpFacts).Map()
	// Strip consumer-owned judgment_* maps before comparing mechanical facts.
	delete(mpAct, "judgment_flags")
	delete(mpAct, "judgment_labels")
	delete(mpAct, "judgment_scores")

	senderEnd := rulefacts.EndFromAgentWithLookup(rulefacts.AgentInput{
		ID:        mpFacts.Sender.ID,
		Slug:      mpFacts.Sender.Slug,
		Name:      "Dev Worker",
		ProjectID: mpFacts.Sender.ProjectID,
	}, &rulefacts.AppliedConfigInput{TemplateID: "tmpl-1", TemplateHash: "sha256:v1"}, func() (string, rulefacts.TemplateLookupStatus) {
		return "developer", rulefacts.TemplateLookupOK
	})
	recipientEnd := rulefacts.EndFromUser(rulefacts.UserInput{
		ID:          mpFacts.Recipient.ID,
		Slug:        mpFacts.Recipient.Slug,
		DisplayName: "Alice",
		ProjectID:   "proj-uuid-1",
	})

	rfFacts, err := rulefacts.FromMessage(
		rulefacts.MessageInput{
			Type:            mpFacts.Message.Type,
			Sender:          "agent:" + mpFacts.Sender.Slug,
			SenderID:        mpFacts.Sender.ID,
			Recipient:       "user:" + mpFacts.Recipient.Slug,
			Body:            mpFacts.Message.Body,
			BodySource:      rulefacts.BodySourceEvaluatedInput,
			HasChoices:      mpFacts.Message.HasChoices,
			Urgent:          mpFacts.Message.Urgent,
			AttachmentCount: mpFacts.Message.AttachmentCount,
			SenderEnd:       &senderEnd,
			RecipientEnd:    &recipientEnd,
			Mentions: rulefacts.MentionsFacts{
				AgentIDs: mpFacts.MentionedAgentIDs,
				UserIDs:  mpFacts.MentionedUserIDs,
			},
		},
		rulefacts.ConversationInfo{
			Ref:                   "dm:agent:agent-uuid-1:user:user-uuid-1",
			ID:                    "conv-dm-1",
			Kind:                  mpFacts.Conversation.Kind,
			Surface:               "native",
			ProjectID:             "proj-uuid-1",
			AgentParticipantIDs:   mpFacts.Conversation.AgentParticipantIDs,
			UserParticipantIDs:    mpFacts.Conversation.UserParticipantIDs,
			UnmentionedAgentSlugs: mpFacts.Conversation.UnmentionedAgentSlugs,
		},
		rulefacts.DeliveryInfo{
			Surface: mpFacts.Surface,
			Via:     "direct",
		},
	)
	require.NoError(t, err)

	// 1. Exact equality of ScopeLegacyPolicyV1 activation with messagepolicy.NewActivation.
	rfLegacy := rulefacts.Project(rfFacts, rulefacts.ScopeLegacyPolicyV1).Map()
	assert.Equal(t, mpAct, rfLegacy, "ScopeLegacyPolicyV1 projection must match messagepolicy.NewActivation exactly")

	// 2. Exact equality of Declarations(ScopeLegacyPolicyV1, NamingLegacyPolicyV1)
	//    with messagepolicy.Env().Variables (minus consumer-owned judgment_* maps).
	var wantVars []celengine.Variable
	for _, v := range messagepolicy.Env().Variables {
		if v.Name == "judgment_flags" || v.Name == "judgment_labels" || v.Name == "judgment_scores" {
			continue
		}
		wantVars = append(wantVars, v)
	}
	gotVars := rulefacts.Declarations(rulefacts.ScopeLegacyPolicyV1, rulefacts.NamingLegacyPolicyV1)
	require.Len(t, gotVars, len(wantVars))
	for i := range wantVars {
		assert.Equal(t, wantVars[i].Name, gotVars[i].Name)
		assert.Truef(t, wantVars[i].Type.Equal(gotVars[i].Type), "var %s type %s != %s", wantVars[i].Name, gotVars[i].Type, wantVars[i].Type)
	}

	// 3. Frozen v1 message.* keys also match in every canonical scope.
	mpMsg := mpAct["message"].(map[string]any)
	frozenKeys := []string{
		"type", "body", "body_truncated", "bytes",
		"has_choices", "urgent", "attachment_count",
	}
	for _, scope := range rulefacts.AllScopes {
		proj := rulefacts.Project(rfFacts, scope).Map()
		rfMsg := proj["message"].(map[string]any)
		for _, k := range frozenKeys {
			assert.Equalf(t, mpMsg[k], rfMsg[k], "scope %s: frozen v1 key message.%s diverged", scope, k)
		}
	}

	// 4. Golden file testdata/legacy_policy_v1.json.
	goldenDecls := make([]goldenDecl, 0, len(gotVars))
	for _, d := range gotVars {
		goldenDecls = append(goldenDecls, goldenDecl{Name: d.Name, Type: d.Type.String()})
	}
	payload := map[string]any{
		"name":         "legacy_policy_v1",
		"activation":   rfLegacy,
		"declarations": goldenDecls,
	}
	gotBytes, err := json.MarshalIndent(payload, "", "  ")
	require.NoError(t, err)
	gotBytes = append(gotBytes, '\n')
	path := filepath.Join("testdata", "legacy_policy_v1.json")
	if *updateGoldens {
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, gotBytes, 0o644))
		return
	}
	wantBytes, err := os.ReadFile(path)
	require.NoErrorf(t, err, "golden file %s missing; run with -update", path)
	assert.Equal(t, string(wantBytes), string(gotBytes))
}

// TestPolicyMirror_SystemOneContentEnvSubset verifies contract v1 §3.5:
// every System One content-environment field in messagepolicy.SystemOneContentEnv()
// is declared and eligible in rulefacts.Schema with matching MaxBytes.
func TestPolicyMirror_SystemOneContentEnvSubset(t *testing.T) {
	mpContentEnv := messagepolicy.SystemOneContentEnv()
	rfFields := rulefacts.Schema.ContentInputFields(rulefacts.ScopePolicyA2A)
	byPath := make(map[string]rulefacts.Field, len(rfFields))
	for _, f := range rfFields {
		byPath[f.Path] = f
	}

	for _, decl := range mpContentEnv.Fields {
		f, ok := byPath[decl.Name]
		require.Truef(t, ok, "System One content field %q missing from rulefacts.Schema", decl.Name)
		require.NotNil(t, f.Content)
		assert.True(t, f.Content.Eligible)
		assert.Equal(t, decl.MaxBytes, f.Content.MaxBytes, "MaxBytes mismatch for %q", decl.Name)
	}
}

// TestPolicyMirror_LegacyV1ParityAllSharedFixturesAndEdges verifies O-4:
// ScopeLegacyPolicyV1 matches messagepolicy.NewActivation (minus consumer-owned
// judgment_* maps) across all 10 shared golden fixtures plus nil-sender,
// nil-recipient, and >16 KiB UTF-8 truncated-body edge cases.
func TestPolicyMirror_LegacyV1ParityAllSharedFixturesAndEdges(t *testing.T) {
	toMPFacts := func(f rulefacts.Facts, rawBody string) messagepolicy.Facts {
		mp := messagepolicy.Facts{
			Surface: f.Delivery.Surface,
			Message: messagepolicy.MessageFacts{
				Type:            f.Message.Type,
				Body:            rawBody,
				HasChoices:      f.Message.HasChoices,
				Urgent:          f.Message.Urgent,
				AttachmentCount: int(f.Message.AttachmentCount),
			},
			MentionedAgentIDs: f.Mentions.AgentIDs,
			MentionedUserIDs:  f.Mentions.UserIDs,
			Conversation: messagepolicy.ConversationFacts{
				Kind:                  f.Conversation.Kind,
				AgentParticipantIDs:   f.Conversation.AgentParticipantIDs,
				UserParticipantIDs:    f.Conversation.UserParticipantIDs,
				UnmentionedAgentSlugs: f.Conversation.UnmentionedAgentSlugs,
			},
		}
		if f.Sender != nil {
			mp.Sender = messagepolicy.SenderFacts{
				Kind:      string(f.Sender.Kind),
				ID:        f.Sender.ID,
				Slug:      f.Sender.Slug,
				ProjectID: f.Sender.ProjectID,
			}
		}
		if f.Recipient != nil {
			mp.Recipient = messagepolicy.RecipientFacts{
				Kind: string(f.Recipient.Kind),
				ID:   f.Recipient.ID,
				Slug: f.Recipient.Slug,
			}
		}
		return mp
	}

	assertParity := func(t *testing.T, f rulefacts.Facts, rawBody string) {
		t.Helper()
		mpAct := messagepolicy.NewActivation(toMPFacts(f, rawBody)).Map()
		delete(mpAct, "judgment_flags")
		delete(mpAct, "judgment_labels")
		delete(mpAct, "judgment_scores")

		rfLegacy := rulefacts.Project(f, rulefacts.ScopeLegacyPolicyV1).Map()
		assert.Equal(t, mpAct, rfLegacy)
	}

	for _, tc := range goldenFixtureCases(t) {
		t.Run("fixture_"+tc.name, func(t *testing.T) {
			f, err := rulefacts.FromMessage(tc.input, tc.conv, tc.deliv)
			require.NoError(t, err)
			assertParity(t, f, tc.input.Body)
		})
	}

	t.Run("edge_nil_sender_and_recipient", func(t *testing.T) {
		f, err := rulefacts.FromMessage(
			rulefacts.MessageInput{Type: "chat", Sender: "system:hub", Body: "notice"},
			rulefacts.ConversationInfo{Kind: "group", ID: "c-1"},
			rulefacts.DeliveryInfo{Surface: "group_post"},
		)
		require.NoError(t, err)
		assertParity(t, f, "notice")
	})

	t.Run("edge_truncated_utf8_body", func(t *testing.T) {
		rawBody := strings.Repeat("a", rulefacts.MaxBodyBytes-2) + "🚀overflow"
		f, err := rulefacts.FromMessage(
			rulefacts.MessageInput{Type: "instruction", Sender: "agent:dev", Body: rawBody},
			rulefacts.ConversationInfo{Kind: "direct", Ref: "dm:a:b"},
			rulefacts.DeliveryInfo{Surface: "agent_to_user"},
		)
		require.NoError(t, err)
		assertParity(t, f, rawBody)
	})
}

// TestPolicyMirror_LegacyV1AndSyntheticChainShapeSmoke verifies contract v1
// §4.3 and §9 item 3 (legacy-v1 half): existing policy fixture rules from
// legacy env message_policy v1 compile and evaluate identically against
// rulefacts.Declarations(ScopeLegacyPolicyV1, NamingLegacyPolicyV1).
//
// The second sub-block is a synthetic chain-shape smoke test combining
// ScopePolicyA2A with a consumer-owned `chain` map; real P1 chain env v2
// compatibility is deferred until P1's chain env lands on main.
func TestPolicyMirror_LegacyV1AndSyntheticChainShapeSmoke(t *testing.T) {
	v1Vars := append(
		rulefacts.Declarations(rulefacts.ScopeLegacyPolicyV1, rulefacts.NamingLegacyPolicyV1),
		celengine.Variable{Name: "judgment_flags", Type: celengine.Map(celengine.String(), celengine.Bool())},
		celengine.Variable{Name: "judgment_labels", Type: celengine.Map(celengine.String(), celengine.String())},
		celengine.Variable{Name: "judgment_scores", Type: celengine.Map(celengine.String(), celengine.Double())},
	)
	v1Eng, err := celengine.New(celengine.Environment{
		Name:       "message_policy_compat_v1",
		Version:    1,
		Variables:  v1Vars,
		Extensions: []celengine.Extension{celengine.ExtensionStrings},
		Macros:     celengine.MacrosStandard,
	}, celengine.DefaultLimits(), celengine.DefaultCacheConfig())
	require.NoError(t, err)

	legacyV1Rules := []struct {
		name string
		expr string
		want bool
	}{
		{
			name: "all_v1_variables",
			expr: `
				surface == "agent_to_user" &&
				sender.kind == "agent" &&
				sender.slug == "dev-worker" &&
				recipient.kind == "user" &&
				recipient.slug == "alice" &&
				message.type == "instruction" &&
				!message.has_choices &&
				message.urgent &&
				message.attachment_count == 1 &&
				message.bytes == 19 &&
				!message.body_truncated &&
				"agent-uuid-2" in mentioned_agent_ids &&
				"user-uuid-1" in mentioned_user_ids &&
				conversation.kind == "direct" &&
				size(conversation.agent_participant_ids) == 1 &&
				size(judgment_scores) == 0
			`,
			want: true,
		},
		{name: "has_choices_false", expr: `message.has_choices`, want: false},
		{name: "body_truncation_check", expr: `message.body_truncated && message.bytes > 16384`, want: false},
		{name: "regex_on_body", expr: `message.body.matches("(?i)pick A or B")`, want: true},
		{name: "sender_project", expr: `sender.project_id == "proj-uuid-1"`, want: true},
		{name: "recipient_kind_user", expr: `recipient.kind == "user" && !message.has_choices`, want: true},
		{name: "judgment_map_key_guard", expr: `has(judgment_flags.asks_choice) && judgment_flags.asks_choice`, want: false},
	}

	senderEnd := rulefacts.EndFromAgentWithLookup(
		rulefacts.AgentInput{
			ID:        "agent-uuid-1",
			Slug:      "dev-worker",
			Name:      "Dev Worker",
			ProjectID: "proj-uuid-1",
		},
		&rulefacts.AppliedConfigInput{TemplateID: "tmpl-1", TemplateHash: "sha256:v1"},
		func() (string, rulefacts.TemplateLookupStatus) { return "developer", rulefacts.TemplateLookupOK },
	)
	recipientEnd := rulefacts.EndFromUser(rulefacts.UserInput{
		ID:          "user-uuid-1",
		Slug:        "alice",
		DisplayName: "Alice",
		ProjectID:   "proj-uuid-1",
	})

	rfFacts, err := rulefacts.FromMessage(
		rulefacts.MessageInput{
			Type:            "instruction",
			Sender:          "agent:dev-worker",
			SenderID:        "agent-uuid-1",
			Recipient:       "user:alice",
			Body:            "Please pick A or B.",
			BodySource:      rulefacts.BodySourceEvaluatedInput,
			HasChoices:      false,
			Urgent:          true,
			AttachmentCount: 1,
			SenderEnd:       &senderEnd,
			RecipientEnd:    &recipientEnd,
			Mentions: rulefacts.MentionsFacts{
				AgentIDs: []string{"agent-uuid-2"},
				UserIDs:  []string{"user-uuid-1"},
			},
		},
		rulefacts.ConversationInfo{
			Ref:                 "dm:agent:agent-uuid-1:user:user-uuid-1",
			ID:                  "conv-dm-1",
			Kind:                "direct",
			Surface:             "native",
			ProjectID:           "proj-uuid-1",
			AgentCount:          1,
			UserCount:           1,
			AgentParticipantIDs: []string{"agent-uuid-1"},
			UserParticipantIDs:  []string{"user-uuid-1"},
		},
		rulefacts.DeliveryInfo{
			Surface: "agent_to_user",
			Via:     "direct",
		},
	)
	require.NoError(t, err)

	v1Act := rulefacts.Project(rfFacts, rulefacts.ScopeLegacyPolicyV1).Map()
	v1Act["judgment_flags"] = map[string]bool{}
	v1Act["judgment_labels"] = map[string]string{}
	v1Act["judgment_scores"] = map[string]float64{}

	for _, tc := range legacyV1Rules {
		t.Run("v1_"+tc.name, func(t *testing.T) {
			prog, err := v1Eng.Compile(tc.expr, celengine.Kind{Name: "violation", Result: celengine.Bool()})
			require.NoError(t, err)
			res, _, err := prog.Eval(context.Background(), v1Act)
			require.NoError(t, err)
			got, err := res.Bool()
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}

	v2Vars := append(
		rulefacts.Declarations(rulefacts.ScopePolicyA2A, rulefacts.NamingPolicy),
		celengine.Variable{Name: "chain", Type: celengine.Map(celengine.String(), celengine.Dyn())},
	)
	v2Eng, err := celengine.New(celengine.Environment{
		Name:       "message_policy_compat_chain_v2",
		Version:    2,
		Variables:  v2Vars,
		Extensions: []celengine.Extension{celengine.ExtensionStrings},
		Macros:     celengine.MacrosStandard,
	}, celengine.DefaultLimits(), celengine.DefaultCacheConfig())
	require.NoError(t, err)

	chainV2Rules := []struct {
		name string
		expr string
		want bool
	}{
		{
			name: "chain_prev_and_message_facts",
			expr: `message.kind == "text" && message.origin == "agent" && delivery.surface == "agent_to_user" && sender.attributes.template == "developer" && chain.step == 1 && chain.flags.asks_choice`,
			want: true,
		},
		{
			name: "agent_forged_event_check",
			expr: `message.kind == "event" && message.origin == "agent"`,
			want: false,
		},
		{
			name: "mentions_and_conversation",
			expr: `mentions.agent_count == 1 && "agent-uuid-2" in mentions.agent_ids && conversation.kind == "direct"`,
			want: true,
		},
	}

	v2Act := rulefacts.Project(rfFacts, rulefacts.ScopePolicyA2A).Map()
	v2Act["chain"] = map[string]any{
		"step":  int64(1),
		"flags": map[string]bool{"asks_choice": true},
	}

	for _, tc := range chainV2Rules {
		t.Run("chain_v2_"+tc.name, func(t *testing.T) {
			prog, err := v2Eng.Compile(tc.expr, celengine.Kind{Name: "violation", Result: celengine.Bool()})
			require.NoError(t, err)
			res, _, err := prog.Eval(context.Background(), v2Act)
			require.NoError(t, err)
			got, err := res.Bool()
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

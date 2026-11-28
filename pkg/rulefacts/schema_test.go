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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/GoogleCloudPlatform/scion/pkg/rulefacts"
)

func TestSchema_Valid(t *testing.T) {
	require.NoError(t, rulefacts.Schema.Validate())
	assert.Equal(t, 1, rulefacts.Schema.Version)
	assert.NotEmpty(t, rulefacts.Schema.Fields)
}

// TestRegistry_IdentityRefComplete verifies Amendment A1 §B.1 and §B.3:
// every path in Schema has an explicit, valid IdentityRef and matches the
// contract's mandatory assignments.
func TestRegistry_IdentityRefComplete(t *testing.T) {
	for _, f := range rulefacts.Schema.Fields {
		assert.Truef(t, f.IdentityRef.Valid(), "field %q must declare a valid IdentityRef (got %q)", f.Path, f.IdentityRef)
		assert.Truef(t, f.Grade.Valid(), "field %q must declare a valid Grade (got %q)", f.Path, f.Grade)
		assert.NotEmptyf(t, f.Producer, "field %q must declare a Producer", f.Path)
	}

	// Spot-check mandatory A1 §B.1 assignments.
	cases := []struct {
		path    string
		scope   rulefacts.Scope
		wantRef rulefacts.IdentityRef
	}{
		{"sender.id", rulefacts.ScopePolicyOut, rulefacts.IdentityRefSelfEnd},
		{"sender.id", rulefacts.ScopePolicyIn, rulefacts.IdentityRefOppositeEnd},
		{"recipient.id", rulefacts.ScopePolicyIn, rulefacts.IdentityRefSelfEnd},
		{"recipient.id", rulefacts.ScopePolicyOut, rulefacts.IdentityRefOppositeEnd},
		{"mentions.agent_ids", rulefacts.ScopePolicyA2A, rulefacts.IdentityRefThirdParty},
		{"mentions.user_ids", rulefacts.ScopePolicyA2A, rulefacts.IdentityRefThirdParty},
		{"conversation.agent_participant_ids", rulefacts.ScopePolicyA2A, rulefacts.IdentityRefThirdParty},
		{"conversation.user_participant_ids", rulefacts.ScopePolicyA2A, rulefacts.IdentityRefThirdParty},
		{"conversation.unmentioned_agent_slugs", rulefacts.ScopePolicyA2A, rulefacts.IdentityRefThirdParty},
		{"conversation.ref", rulefacts.ScopePolicyOut, rulefacts.IdentityRefValueDependent},
		{"conversation.id", rulefacts.ScopePolicyOut, rulefacts.IdentityRefValueDependent},
		{"message.event.source.id", rulefacts.ScopePolicyOut, rulefacts.IdentityRefValueDependent},
		{"message.event.source.name", rulefacts.ScopePolicyOut, rulefacts.IdentityRefValueDependent},
		{"message.event.source.kind", rulefacts.ScopePolicyOut, rulefacts.IdentityRefNone},
		{"message.event.subject", rulefacts.ScopePolicyOut, rulefacts.IdentityRefValueDependent},
		{"message.event.reason", rulefacts.ScopePolicyOut, rulefacts.IdentityRefValueDependent},
		{"message.event.templated_vars", rulefacts.ScopePolicyOut, rulefacts.IdentityRefValueDependent},
		{"message.event.type", rulefacts.ScopePolicyOut, rulefacts.IdentityRefNone},
		{"message.event.trigger.id", rulefacts.ScopePolicyOut, rulefacts.IdentityRefNone},
		{"message.event.schedule.id", rulefacts.ScopePolicyOut, rulefacts.IdentityRefNone},
		{"message.event.external.verified", rulefacts.ScopePolicyOut, rulefacts.IdentityRefNone},
		{"message.interaction.kind", rulefacts.ScopePolicyOut, rulefacts.IdentityRefNone},
		{"message.system.category", rulefacts.ScopePolicyOut, rulefacts.IdentityRefNone},
		{"delivery.surface", rulefacts.ScopePolicyOut, rulefacts.IdentityRefNone},
	}
	for _, tc := range cases {
		f, ok := rulefacts.Schema.Lookup(tc.path)
		require.Truef(t, ok, "path %q missing from Schema", tc.path)
		assert.Equalf(t, tc.wantRef, f.IdentityRefFor(tc.scope), "path %q in scope %q", tc.path, tc.scope)
	}

	// Amendment A1 §B.1: EventBody.Attributes, Lists, and URL must NOT be facts in v1.
	for _, forbidden := range []string{
		"message.event.attributes",
		"message.event.lists",
		"message.event.url",
		"message.event.external.json",
		"message.event.external.text",
	} {
		_, ok := rulefacts.Schema.Lookup(forbidden)
		assert.Falsef(t, ok, "path %q must NOT be declared in v1 Schema", forbidden)
	}
}

func TestSchema_VisibilityAndValidatePath(t *testing.T) {
	// Persisted-only paths visible ONLY in trigger_chat.
	for _, p := range []string{"message.id", "message.created_at"} {
		assert.Error(t, rulefacts.Schema.ValidatePath(rulefacts.ScopePolicyOut, p))
		assert.Error(t, rulefacts.Schema.ValidatePath(rulefacts.ScopePolicyIn, p))
		assert.Error(t, rulefacts.Schema.ValidatePath(rulefacts.ScopePolicyA2A, p))
		assert.NoError(t, rulefacts.Schema.ValidatePath(rulefacts.ScopeTriggerChat, p))
	}

	// Direction rules: sender absent in policy_in, recipient absent in policy_out.
	assert.NoError(t, rulefacts.Schema.ValidatePath(rulefacts.ScopePolicyOut, "sender.attributes.template"))
	assert.Error(t, rulefacts.Schema.ValidatePath(rulefacts.ScopePolicyIn, "sender.attributes.template"))
	assert.Error(t, rulefacts.Schema.ValidatePath(rulefacts.ScopePolicyOut, "recipient.attributes.template"))
	assert.NoError(t, rulefacts.Schema.ValidatePath(rulefacts.ScopePolicyIn, "recipient.attributes.template"))

	// Dynamic label keys allowed when parent labels map is visible.
	assert.NoError(t, rulefacts.Schema.ValidatePath(rulefacts.ScopePolicyOut, "sender.attributes.labels.team"))
	assert.Error(t, rulefacts.Schema.ValidatePath(rulefacts.ScopePolicyIn, "sender.attributes.labels.team"))

	// Q1 a2a_equiv: third-party identity lists visible in policy_a2a AND trigger_chat, absent in out/in.
	for _, p := range []string{
		"mentions.agent_ids",
		"mentions.user_ids",
		"conversation.agent_participant_ids",
		"conversation.user_participant_ids",
	} {
		assert.Error(t, rulefacts.Schema.ValidatePath(rulefacts.ScopePolicyOut, p))
		assert.Error(t, rulefacts.Schema.ValidatePath(rulefacts.ScopePolicyIn, p))
		assert.NoError(t, rulefacts.Schema.ValidatePath(rulefacts.ScopePolicyA2A, p))
		assert.NoError(t, rulefacts.Schema.ValidatePath(rulefacts.ScopeTriggerChat, p))
	}

	// Unknown typo path fails in all scopes.
	for _, sc := range rulefacts.AllScopes {
		assert.Error(t, rulefacts.Schema.ValidatePath(sc, "message.nonexistent_field"))
	}
}

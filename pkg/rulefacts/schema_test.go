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
		{"conversation.thread_id", rulefacts.ScopePolicyOut, rulefacts.IdentityRefValueDependent},
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
		{"delivery.cross_project", rulefacts.ScopePolicyOut, rulefacts.IdentityRefOppositeEnd},
		{"delivery.cross_project", rulefacts.ScopePolicyIn, rulefacts.IdentityRefOppositeEnd},
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

	// A1 §B.2, A2 §2.3 & A3 §2.1 static visibility: always-dropped paths must exclude policy_out and policy_in.
	for _, p := range []string{
		"message.event.subject",
		"message.event.reason",
		"message.event.templated_vars",
		"delivery.cross_project",
		"conversation.thread_id",
	} {
		assert.Error(t, rulefacts.Schema.ValidatePath(rulefacts.ScopePolicyOut, p))
		assert.Error(t, rulefacts.Schema.ValidatePath(rulefacts.ScopePolicyIn, p))
		assert.NoError(t, rulefacts.Schema.ValidatePath(rulefacts.ScopePolicyA2A, p))
		assert.NoError(t, rulefacts.Schema.ValidatePath(rulefacts.ScopeTriggerChat, p))
	}

	// conversation.unmentioned_agent_slugs is content-only, not a CEL fact path.
	for _, sc := range append(rulefacts.AllScopes, rulefacts.ScopeLegacyPolicyV1) {
		assert.Error(t, rulefacts.Schema.ValidatePath(sc, "conversation.unmentioned_agent_slugs"))
	}

	// message.event.schedule.fire_at is TypeTimestamp.
	fireAtField, ok := rulefacts.Schema.Lookup("message.event.schedule.fire_at")
	require.True(t, ok)
	assert.Equal(t, rulefacts.CELKindTimestamp, fireAtField.Type.Kind)

	// O-2: ScopeLegacyPolicyV1 is mirror-only (uses legacy flat names, not
	// canonical registry paths), so it has no bit in Schema.Fields and
	// ValidatePath rejects it for all paths.
	assert.Equal(t, rulefacts.ScopeSet(0), rulefacts.ScopeLegacyPolicyV1.Bit())
	assert.Empty(t, rulefacts.Schema.VisibleIn(rulefacts.ScopeLegacyPolicyV1))
	for _, p := range []string{
		"message.kind",
		"message.origin",
		"message.type",
		"delivery.surface",
		"surface",
		"sender.attributes",
		"conversation.ref",
	} {
		assert.Error(t, rulefacts.Schema.ValidatePath(rulefacts.ScopeLegacyPolicyV1, p),
			"legacy_policy_v1 is mirror-only and must not validate path %q in Schema", p)
	}
}

// TestSchema_IdentityCarrierCensus pins Amendment A3 §2.4: every string or
// list(string) path in Schema.Fields must have an explicit IdentityRef AND a
// census classification here so a newly added string/list(string) path cannot
// silently carry a principal ID or DM key into policy_out/policy_in.
func TestSchema_IdentityCarrierCensus(t *testing.T) {
	type censusEntry struct {
		wantRef      rulefacts.IdentityRef
		absentOutIn  bool // statically absent in policy_out and policy_in
		contentOnly  bool
		isContentCat bool
	}

	// Explicit census of EVERY string and list(string) path in Schema v1 + A1 + A2 + A3.
	census := map[string]censusEntry{
		// A3 §2.4 Identity-carrier table:
		"conversation.ref":                     {wantRef: rulefacts.IdentityRefValueDependent}, // D1 runtime drop for direct
		"conversation.id":                      {wantRef: rulefacts.IdentityRefValueDependent}, // D1 runtime drop for direct
		"conversation.thread_id":               {wantRef: rulefacts.IdentityRefValueDependent, absentOutIn: true},
		"message.event.subject":                {wantRef: rulefacts.IdentityRefValueDependent, absentOutIn: true},
		"message.event.reason":                 {wantRef: rulefacts.IdentityRefValueDependent, absentOutIn: true},
		"message.event.templated_vars":         {wantRef: rulefacts.IdentityRefValueDependent, absentOutIn: true},
		"message.event.source.id":              {wantRef: rulefacts.IdentityRefValueDependent}, // A1 §B.2 runtime visible-end check
		"message.event.source.name":            {wantRef: rulefacts.IdentityRefValueDependent}, // A1 §B.2 runtime visible-end check
		"mentions.agent_ids":                   {wantRef: rulefacts.IdentityRefThirdParty, absentOutIn: true},
		"mentions.user_ids":                    {wantRef: rulefacts.IdentityRefThirdParty, absentOutIn: true},
		"conversation.agent_participant_ids":   {wantRef: rulefacts.IdentityRefThirdParty, absentOutIn: true},
		"conversation.user_participant_ids":    {wantRef: rulefacts.IdentityRefThirdParty, absentOutIn: true},
		"conversation.unmentioned_agent_slugs": {wantRef: rulefacts.IdentityRefThirdParty, absentOutIn: true, contentOnly: true},
		"message.body":                         {wantRef: rulefacts.IdentityRefNone, isContentCat: true},

		// Same-end fields (sender.* absent in policy_in; recipient.* absent in policy_out):
		"sender.kind":                           {wantRef: rulefacts.IdentityRefSelfEnd},
		"sender.id":                             {wantRef: rulefacts.IdentityRefSelfEnd},
		"sender.slug":                           {wantRef: rulefacts.IdentityRefSelfEnd},
		"sender.project_id":                     {wantRef: rulefacts.IdentityRefSelfEnd},
		"sender.created_by.kind":                {wantRef: rulefacts.IdentityRefSelfEnd},
		"sender.created_by.id":                  {wantRef: rulefacts.IdentityRefSelfEnd},
		"sender.root_user_id":                   {wantRef: rulefacts.IdentityRefSelfEnd},
		"sender.attributes.name":                {wantRef: rulefacts.IdentityRefSelfEnd},
		"sender.attributes.template":            {wantRef: rulefacts.IdentityRefSelfEnd},
		"sender.attributes.template_lookup":     {wantRef: rulefacts.IdentityRefSelfEnd},
		"sender.attributes.template_id":         {wantRef: rulefacts.IdentityRefSelfEnd},
		"sender.attributes.template_version":    {wantRef: rulefacts.IdentityRefSelfEnd},
		"recipient.kind":                        {wantRef: rulefacts.IdentityRefSelfEnd},
		"recipient.id":                          {wantRef: rulefacts.IdentityRefSelfEnd},
		"recipient.slug":                        {wantRef: rulefacts.IdentityRefSelfEnd},
		"recipient.project_id":                  {wantRef: rulefacts.IdentityRefSelfEnd},
		"recipient.created_by.kind":             {wantRef: rulefacts.IdentityRefSelfEnd},
		"recipient.created_by.id":               {wantRef: rulefacts.IdentityRefSelfEnd},
		"recipient.root_user_id":                {wantRef: rulefacts.IdentityRefSelfEnd},
		"recipient.attributes.name":             {wantRef: rulefacts.IdentityRefSelfEnd},
		"recipient.attributes.template":         {wantRef: rulefacts.IdentityRefSelfEnd},
		"recipient.attributes.template_lookup":  {wantRef: rulefacts.IdentityRefSelfEnd},
		"recipient.attributes.template_id":      {wantRef: rulefacts.IdentityRefSelfEnd},
		"recipient.attributes.template_version": {wantRef: rulefacts.IdentityRefSelfEnd},

		// Non-carrier string descriptors, enums, and resource IDs:
		"message.id":                      {wantRef: rulefacts.IdentityRefNone, absentOutIn: true},
		"message.kind":                    {wantRef: rulefacts.IdentityRefNone},
		"message.intent":                  {wantRef: rulefacts.IdentityRefNone},
		"message.type":                    {wantRef: rulefacts.IdentityRefNone},
		"message.origin":                  {wantRef: rulefacts.IdentityRefNone},
		"message.body_source":             {wantRef: rulefacts.IdentityRefNone},
		"message.event.type":              {wantRef: rulefacts.IdentityRefNone},
		"message.event.event_type":        {wantRef: rulefacts.IdentityRefNone},
		"message.event.event_id":          {wantRef: rulefacts.IdentityRefNone},
		"message.event.fire_id":           {wantRef: rulefacts.IdentityRefNone},
		"message.event.status":            {wantRef: rulefacts.IdentityRefNone},
		"message.event.source.kind":       {wantRef: rulefacts.IdentityRefNone},
		"message.event.trigger.id":        {wantRef: rulefacts.IdentityRefNone},
		"message.event.trigger.name":      {wantRef: rulefacts.IdentityRefNone},
		"message.event.schedule.id":       {wantRef: rulefacts.IdentityRefNone},
		"message.event.schedule.name":     {wantRef: rulefacts.IdentityRefNone},
		"message.event.schedule.event_id": {wantRef: rulefacts.IdentityRefNone},
		"message.event.external.verified": {wantRef: rulefacts.IdentityRefNone},
		"message.interaction.kind":        {wantRef: rulefacts.IdentityRefNone},
		"message.system.category":         {wantRef: rulefacts.IdentityRefNone},
		"conversation.kind":               {wantRef: rulefacts.IdentityRefNone},
		"conversation.surface":            {wantRef: rulefacts.IdentityRefNone},
		"conversation.project_id":         {wantRef: rulefacts.IdentityRefNone},
		"delivery.surface":                {wantRef: rulefacts.IdentityRefNone},
		"delivery.via":                    {wantRef: rulefacts.IdentityRefNone},
	}

	seen := make(map[string]bool)
	for _, f := range rulefacts.Schema.Fields {
		isStringOrStringList := f.Type.Kind == rulefacts.CELKindString ||
			(f.Type.Kind == rulefacts.CELKindList && f.Type.Elem != nil && f.Type.Elem.Kind == rulefacts.CELKindString)
		if !isStringOrStringList {
			continue
		}
		seen[f.Path] = true
		entry, ok := census[f.Path]
		require.Truef(t, ok, "string/list(string) path %q is missing from A3 §2.4 identity-carrier census; classify it explicitly", f.Path)
		assert.Equal(t, entry.wantRef, f.IdentityRef, "IdentityRef mismatch for %q", f.Path)
		if entry.absentOutIn {
			assert.Falsef(t, f.Visible.Has(rulefacts.ScopePolicyOut), "path %q must be absent in policy_out", f.Path)
			assert.Falsef(t, f.Visible.Has(rulefacts.ScopePolicyIn), "path %q must be absent in policy_in", f.Path)
		}
		if entry.contentOnly {
			require.NotNil(t, f.Content)
			assert.True(t, f.Content.ContentOnly)
		}
		if entry.isContentCat {
			assert.Equal(t, rulefacts.GradeContent, f.Grade)
		}
	}

	// Ensure no stale census entries remain.
	for p := range census {
		assert.Truef(t, seen[p], "census entry %q no longer exists in Schema.Fields", p)
	}
}

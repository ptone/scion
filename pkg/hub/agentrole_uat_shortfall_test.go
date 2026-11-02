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

package hub

import (
	"testing"

	"github.com/GoogleCloudPlatform/scion/pkg/hub/permissions"
	"github.com/GoogleCloudPlatform/scion/pkg/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// readonlyUATSelectors are the scopes a token needs for an agent it creates
// to get the readonly role: every permission the project:read agent scope
// covers.
var readonlyUATSelectors = []string{
	"harness_config:list", "harness_config:read", "project:read",
	"skill:list", "skill:read", "template:list", "template:read",
}

func TestAgentRoleUATSelectors(t *testing.T) {
	assert.Equal(t, RoleShortfall{Selectors: readonlyUATSelectors}, AgentRoleUATSelectors(AgentRoleReadOnly))
	// baseline adds only self operations, so it needs nothing more.
	assert.Equal(t, RoleShortfall{Selectors: readonlyUATSelectors}, AgentRoleUATSelectors(AgentRoleBaseline))
	assert.True(t, AgentRoleUATSelectors(AgentRoleNone).Empty())

	full := AgentRoleUATSelectors(AgentRoleFull)
	assert.Equal(t, []string{
		"agent:attach", "agent:create", "agent:delete", "agent:lifecycle", "agent:set_message_mode",
		"gcp_service_account:assign", "harness_config:list", "harness_config:read", "project:read",
		"skill:list", "skill:read", "template:create", "template:list", "template:read", "template:update",
	}, full.Selectors)
	// project.secret_read has no token scope; secret.use is intrinsic to a
	// token hop.
	assert.Equal(t, []string{"project.secret_read"}, full.Ungrantable)
}

// The selectors AgentRoleUATSelectors names are exactly what makes the role
// fit: adding them leaves no shortfall and roleFitsCeiling agrees.
func TestAgentRoleUATShortfall_SelectorsMakeRoleFit(t *testing.T) {
	for _, role := range []AgentRole{AgentRoleReadOnly, AgentRoleBaseline} {
		s, ok := AgentRoleUATShortfall(append([]string{"agent:create"}, AgentRoleUATSelectors(role).Selectors...), role)
		require.True(t, ok)
		assert.True(t, s.Empty(), "role %s: %+v", role, s)
	}

	// Dropping any one selector leaves exactly that selector missing.
	for i, drop := range readonlyUATSelectors {
		sel := append(append([]string{"agent:create"}, readonlyUATSelectors[:i]...), readonlyUATSelectors[i+1:]...)
		s, ok := AgentRoleUATShortfall(sel, AgentRoleReadOnly)
		require.True(t, ok)
		assert.Equal(t, RoleShortfall{Selectors: []string{drop}}, s)
	}
}

func TestAgentRoleUATShortfall_ManageAliasesCover(t *testing.T) {
	s, ok := AgentRoleUATShortfall([]string{
		"project:read", permissions.UATScopeSkillManage, permissions.UATScopeTemplateManage, permissions.UATScopeHarnessConfigManage,
	}, AgentRoleReadOnly)
	require.True(t, ok)
	assert.True(t, s.Empty(), "%+v", s)
}

func TestAgentRoleUATShortfall_UnknownSelector(t *testing.T) {
	_, ok := AgentRoleUATShortfall([]string{"project:read", "no:such_scope"}, AgentRoleReadOnly)
	assert.False(t, ok)
}

// The repro token of ptone/scion#7069: project read/update plus agent
// scopes, none of the skill, template or harness config reads.
func TestAgentRoleUATShortfall_IssueRepro(t *testing.T) {
	s, ok := AgentRoleUATShortfall([]string{
		"project:read", "project:update", "agent:create", "agent:read", "agent:list",
		"agent:lifecycle", "agent:delete", "agent:message", "agent:attach",
	}, AgentRoleReadOnly)
	require.True(t, ok)
	assert.Equal(t, []string{
		"harness_config:list", "harness_config:read", "skill:list", "skill:read", "template:list", "template:read",
	}, s.Selectors)
	assert.Empty(t, s.Ungrantable)
}

// A ceiling that is not bounded has no shortfall to name.
func TestRoleCeilingShortfall_NotBounded(t *testing.T) {
	for _, kind := range []store.EffectCeilingKind{store.EffectCeilingPrincipal, store.EffectCeilingUnrecorded} {
		assert.True(t, roleCeilingShortfall(store.EffectCeiling{Kind: kind}, AgentRoleFull).Empty(), kind)
	}
}

func TestShortfallDenialMessages(t *testing.T) {
	uatProv := store.AuthorityProvenance{SourceCredentialKind: store.SourceCredentialUAT}
	agentProv := store.AuthorityProvenance{SourceCredentialKind: store.SourceCredentialAgent}
	ceiling := store.EffectCeiling{
		Kind:          store.EffectCeilingBounded,
		Version:       permissions.CeilingVersionV1,
		PermissionIDs: []string{"agent.create", "project.read"},
	}

	assert.Equal(t,
		"the token's scopes fit no usable agent role; the readonly role also needs the scopes "+
			"harness_config:list, harness_config:read, skill:list, skill:read, template:list, template:read; "+
			"add them, request role=none explicitly, or create from a session",
		noUsableRoleMessage(ceiling, uatProv))
	// An agent source has no token scopes to add: the message is unchanged.
	assert.Equal(t, reasonNoUsableRole, noUsableRoleMessage(ceiling, agentProv))

	assert.Equal(t,
		`the credential's scopes do not cover agent role "baseline"; it also needs the scopes `+
			"harness_config:list, harness_config:read, skill:list, skill:read, template:list, template:read; "+
			"request a role the token covers, request role=none explicitly, or create from a session",
		roleNotCoveredMessage(ceiling, uatProv, AgentRoleBaseline))
	assert.Contains(t, roleNotCoveredMessage(ceiling, uatProv, AgentRoleFull), "; no token scope grants project.secret_read")
	assert.Equal(t,
		`the credential's scopes do not cover agent role "readonly"; request a role the token covers, request role=none explicitly, or create from a session`,
		roleNotCoveredMessage(ceiling, agentProv, AgentRoleReadOnly))
}

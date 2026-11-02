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
	"fmt"
	"strings"

	"github.com/GoogleCloudPlatform/scion/pkg/hub/permissions"
	"github.com/GoogleCloudPlatform/scion/pkg/store"
)

// RoleShortfall is what a bounded ceiling lacks for an agent role to fit it
// (roleFitsCeiling). Selectors are the user access token scopes that grant
// the missing permissions; Ungrantable are missing permission IDs that no
// token scope grants. Both are sorted and empty when the role fits.
type RoleShortfall struct {
	Selectors   []string
	Ungrantable []string
}

// Empty reports whether nothing is missing.
func (s RoleShortfall) Empty() bool {
	return len(s.Selectors) == 0 && len(s.Ungrantable) == 0
}

// roleCeilingShortfall returns what the bounded ceiling c lacks for role, by
// the rule roleFitsCeiling applies: for each role scope outside
// ceilingOptionalRoleScopes that ceilingAllowsScope refuses, every covered
// permission c does not allow and that is not a self operation (or, for a
// scope with zero registry coverage, its zeroCoverageScopeMapping
// permission). A ceiling that is not bounded yields an empty shortfall:
// nothing a token could add would change the outcome.
func roleCeilingShortfall(c store.EffectCeiling, role AgentRole) RoleShortfall {
	frozen, ok := c.Frozen()
	if !ok {
		return RoleShortfall{}
	}
	var missing []string
	for _, scope := range ScopesForRole(role) {
		if ceilingOptionalRoleScopes[scope] || ceilingAllowsScope(c, scope) {
			continue
		}
		coverage := agentScopeCoverage([]AgentTokenScope{scope})
		if len(coverage) == 0 {
			if perm, mapped := zeroCoverageMappedPermission(scope); mapped && !frozen.Allows(perm) {
				missing = append(missing, perm)
			}
			continue
		}
		for _, perm := range coverage {
			if !frozen.Allows(perm) && !selfOperationSet[perm] {
				missing = append(missing, perm)
			}
		}
	}
	var out RoleShortfall
	for _, perm := range sortedUniqueIDs(missing) {
		if sel, ok := uatSelectorForPermission[perm]; ok {
			out.Selectors = append(out.Selectors, sel)
		} else {
			out.Ungrantable = append(out.Ungrantable, perm)
		}
	}
	out.Selectors = sortedUniqueIDs(out.Selectors)
	return out
}

// uatSelectorForPermission maps each permission ID to the user access token
// scope that names it. Reserved permissions are not offered as scopes.
var uatSelectorForPermission = func() map[string]string {
	m := make(map[string]string)
	for _, p := range permissions.Registry {
		if p.UATScope != "" && !p.IsReserved() {
			m[p.ID] = p.UATScope
		}
	}
	return m
}()

// AgentRoleUATShortfall reports what a user access token carrying selectors
// lacks for an agent it creates to get role. It reads the token's ceiling
// the way agent create does (a bounded ceiling plus the intrinsic
// permissions of a token hop). ok is false when a selector does not resolve.
func AgentRoleUATShortfall(selectors []string, role AgentRole) (RoleShortfall, bool) {
	frozen, ok := permissions.BuildCeilingFromSelectors(selectors)
	if !ok {
		return RoleShortfall{}, false
	}
	const holder = "token-holder"
	prov := store.AuthorityProvenance{
		ProvenanceVersion:    store.ProvenanceVersionV1,
		SourcePrincipalKind:  store.DelegationPrincipalUser,
		SourcePrincipalID:    holder,
		SourceCredentialKind: store.SourceCredentialUAT,
	}
	c := store.EffectCeiling{
		Kind:          store.EffectCeilingBounded,
		Version:       frozen.Version,
		PermissionIDs: frozen.PermissionIDs,
	}
	return roleCeilingShortfall(uatHopEffectCeiling(store.DelegationPrincipalUser, holder, prov, c), role), true
}

// AgentRoleUATSelectors returns the user access token scopes a token needs
// for an agent it creates to get role, and the permissions of role that no
// token scope grants.
func AgentRoleUATSelectors(role AgentRole) RoleShortfall {
	s, _ := AgentRoleUATShortfall(nil, role)
	return s
}

// describeShortfall renders a shortfall for a denial message, for example
// "it also needs the scopes skill:list, skill:read". It returns "" for an
// empty shortfall.
func describeShortfall(s RoleShortfall) string {
	var parts []string
	if len(s.Selectors) > 0 {
		parts = append(parts, "it also needs the scopes "+strings.Join(s.Selectors, ", "))
	}
	if len(s.Ungrantable) > 0 {
		parts = append(parts, "no token scope grants "+strings.Join(s.Ungrantable, ", "))
	}
	return strings.Join(parts, "; ")
}

// noUsableRoleMessage is the denial message for a defaulted role that fits
// no role above none. For a user access token it names the scopes the
// readonly role still needs.
func noUsableRoleMessage(c store.EffectCeiling, prov store.AuthorityProvenance) string {
	if prov.SourceCredentialKind != store.SourceCredentialUAT {
		return reasonNoUsableRole
	}
	s := roleCeilingShortfall(c, AgentRoleReadOnly)
	if len(s.Selectors) == 0 {
		return reasonNoUsableRole
	}
	return fmt.Sprintf("the token's scopes fit no usable agent role; the readonly role also needs the scopes %s; add them, request role=none explicitly, or create from a session",
		strings.Join(s.Selectors, ", "))
}

// roleNotCoveredMessage is the denial message for an explicit role the
// credential does not cover. For a user access token it names what the role
// still needs.
func roleNotCoveredMessage(c store.EffectCeiling, prov store.AuthorityProvenance, role AgentRole) string {
	detail := ""
	if prov.SourceCredentialKind == store.SourceCredentialUAT {
		if d := describeShortfall(roleCeilingShortfall(c, role)); d != "" {
			detail = "; " + d
		}
	}
	return fmt.Sprintf("the credential's scopes do not cover agent role %q%s; request a role the token covers, request role=none explicitly, or create from a session", role, detail)
}

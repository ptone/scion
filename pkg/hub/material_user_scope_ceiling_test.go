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

//go:build !no_sqlite

// Package hub — F follow-up 2 precondition: what step 10 decides for a
// user-scope secret.use today. Check 8 (authorizeRuntimeUserItem) may route
// through Decide(secret.use, ActionUse) only if step 10 admits an agent whose
// recorded ceiling allows secret.use and denies one whose ceiling does not.
package hub

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/GoogleCloudPlatform/scion/pkg/secret"
	"github.com/GoogleCloudPlatform/scion/pkg/store"
	"github.com/stretchr/testify/require"
)

const userScopeCeilingKey = "USER_SCOPE_CEILING_KEY"

// userScopeCeilingFixture is a material fixture in production-like state:
// the delegation-edge backfill is complete, the agent has one recorded edge
// from its root user (session source, project scope, full role) carrying the
// given effect ceiling, and the root has shared a user-scope secret with its
// progeny (AllowProgeny, CreatedBy the root).
type userScopeCeilingFixture struct {
	*materialFixture
}

func newUserScopeCeilingFixture(t *testing.T, name string, ceiling store.EffectCeiling) *userScopeCeilingFixture {
	t.Helper()
	f := newMaterialFixture(t, name)
	ctx := context.Background()
	setBackfillCompleted(t, f.Store)
	require.NoError(t, f.Store.CreateDelegationEdge(ctx, &store.DelegationEdge{
		DelegatorType: store.DelegationPrincipalUser, DelegatorID: f.UserID,
		DelegateType: store.DelegationPrincipalAgent, DelegateID: f.AgentID,
		ScopeType: store.RoleScopeProject, ScopeID: f.ProjectID, Role: string(AgentRoleFull), Active: true,
		AuthorityProvenance: store.AuthorityProvenance{
			ProvenanceVersion:    store.ProvenanceVersionV1,
			SourcePrincipalKind:  store.DelegationPrincipalUser,
			SourcePrincipalID:    f.UserID,
			SourceCredentialKind: store.SourceCredentialSession,
		},
		EffectCeiling: ceiling,
	}))
	_, _, err := f.Server.secretBackend.Set(ctx, &secret.SetSecretInput{
		Name: userScopeCeilingKey, Value: "v", SecretType: store.SecretTypeEnvironment, Target: userScopeCeilingKey,
		Scope: store.ScopeUser, ScopeID: f.UserID, AllowProgeny: true, CreatedBy: f.UserID, UpdatedBy: f.UserID,
	})
	require.NoError(t, err)
	return &userScopeCeilingFixture{materialFixture: f}
}

// decideSecretUse is the decision check 8 would make: secret.use with
// ActionUse for the agent's token on the shared item, with the resource
// shape check 8 passes to the progeny resolver today. Explained, so the
// progeny candidate's outcome is visible.
func (f *userScopeCeilingFixture) decideSecretUse(t *testing.T) Decision {
	t.Helper()
	ctx := context.Background()
	meta, err := f.Server.secretBackend.GetMeta(ctx, userScopeCeilingKey, store.ScopeUser, f.UserID)
	require.NoError(t, err)
	require.NotNil(t, meta)
	agent := newFullAgentIdentity(f.AgentID, f.ProjectID, []string{f.UserID}, []AgentTokenScope{ScopeProjectSecretRead})
	return f.Server.authzService.Decide(ctx, AuthzRequest{
		Principal:  principalContextForIdentity(agent),
		Credential: credentialContextForIdentity(agent),
		Resource:   Resource{Type: "secret", ID: meta.ID, OwnerID: meta.ScopeID, ParentType: "user", ParentID: meta.ScopeID},
		Action:     ActionUse,
		Permission: "secret.use",
		Explain:    true,
	})
}

// readStatus is the runtime read through the by-key endpoint (check 8).
func (f *userScopeCeilingFixture) readStatus(t *testing.T) (int, string) {
	t.Helper()
	rec := doRequestWithAgentToken(t, f.Server, http.MethodGet,
		"/api/v1/agents/"+f.AgentID+"/secrets/"+userScopeCeilingKey+"?scope=user", nil, f.Token)
	return rec.Code, rec.Body.String()
}

// TestAgentSecretRead_UserScopeSecretUseStep10Characterization pins today's
// outcomes (the follow-up 2 proof). For both a principal ceiling and a
// bounded ceiling that leaves out secret.use:
//   - the runtime read is admitted: check 8 consults no ceiling;
//   - the progeny relationship accepts (secret.use, ActionUse);
//   - step 10 denies the decision. For the principal ceiling the reason is
//     that the delegating user does not hold secret.use: step 10 runs the
//     generic delegator-authority check, not a user-material ceiling, so it
//     would deny today's ordinary allow case.
//
// Routing check 8 through Decide(secret.use) with today's step 10 would
// therefore deny every user-scope read by an agent with a recorded edge
// from a plain user. When the user-material ceiling lands, the principal
// case flips to allowed, this test fails, and
// TestAgentSecretRead_UserScopeCeilingApplied is un-gated instead.
func TestAgentSecretRead_UserScopeSecretUseStep10Characterization(t *testing.T) {
	cases := []struct {
		name    string
		ceiling store.EffectCeiling
	}{
		{"principal", store.EffectCeiling{Kind: store.EffectCeilingPrincipal}},
		{"bounded without secret use", boundedCeiling("project.read", "agent.read")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newUserScopeCeilingFixture(t, "uscc-"+strings.ReplaceAll(tc.name, " ", "-"), tc.ceiling)

			code, body := f.readStatus(t)
			require.Equal(t, http.StatusOK, code, "check 8 consults no ceiling today: %s", body)

			d := f.decideSecretUse(t)
			r := relationshipResult(t, d, RelationshipRuleProgeny)
			require.True(t, r.Accepted, "the progeny relationship admits the pair before step 10")
			require.False(t, d.Allowed, "step 10 denies secret.use; reason %q", d.Reason)
			require.Equal(t, DeniedByDelegationCeiling, d.DeniedBy, "reason %q", d.Reason)
			if tc.ceiling.Kind == store.EffectCeilingPrincipal {
				require.Contains(t, d.Reason, "does not hold secret.use",
					"the principal-ceiling deny is the generic delegator check, not a user-material ceiling")
			}
		})
	}
}

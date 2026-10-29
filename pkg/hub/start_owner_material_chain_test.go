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

// Package hub — the owner material chain rule (start_owner_material_chain.go):
// the owner's user-scope rows not shared with progeny, secrets and env vars,
// are delivered at create, start and restart only when the agent's recorded
// chain ends at the owner and its ancestry starts there. Shared rows keep
// their rule (the parent ceiling for secrets, unchanged for env vars).
package hub

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/GoogleCloudPlatform/scion/pkg/api"
	"github.com/GoogleCloudPlatform/scion/pkg/config/opsettings"
	"github.com/GoogleCloudPlatform/scion/pkg/secret"
	"github.com/GoogleCloudPlatform/scion/pkg/store"
)

const (
	// omcSharedEnvKey is a user-scope env var shared with progeny.
	omcSharedEnvKey   = "OMC_SHARED_ENV"
	omcSharedEnvValue = "omc-shared-env-value"
	// omcPrivateEnvKey is a plain user-scope env var not shared.
	omcPrivateEnvKey   = "OMC_PRIVATE_ENV"
	omcPrivateEnvValue = "omc-private-env-value"
	// omcPrivateSecretEnvKey is a secret-flagged user-scope env var not
	// shared.
	omcPrivateSecretEnvKey   = "OMC_PRIVATE_SECRET_ENV"
	omcPrivateSecretEnvValue = "omc-private-secret-env-value"
	// omcPrivateAsNeededKey is an as-needed user-scope secret not shared.
	omcPrivateAsNeededKey   = "OMC_PRIVATE_AS_NEEDED"
	omcPrivateAsNeededValue = "omc-private-as-needed-value"
	// omcPrivateAsNeededEnvKey is an as-needed plain user-scope env var not
	// shared.
	omcPrivateAsNeededEnvKey   = "OMC_PRIVATE_AS_NEEDED_ENV"
	omcPrivateAsNeededEnvValue = "omc-private-as-needed-env-value"

	omcWithheldMsg = "start-time user material withheld: the agent's recorded delegation chain does not end at the material's owner"
	omcLegacyMsg   = "start-time user material delivered through the legacy fallback: the agent's delegation chain is not recorded"
)

// omcSeedOwnerEnv writes the owner's env vars and the extra as-needed
// private secret (the sd fixture writes the other three secrets).
func omcSeedOwnerEnv(t *testing.T, f *sdFixture, ownerID string) {
	t.Helper()
	for _, v := range []store.EnvVar{
		{Key: omcSharedEnvKey, Value: omcSharedEnvValue, AllowProgeny: true},
		{Key: omcPrivateEnvKey, Value: omcPrivateEnvValue},
		{Key: omcPrivateSecretEnvKey, Value: omcPrivateSecretEnvValue, Secret: true},
		{Key: omcPrivateAsNeededEnvKey, Value: omcPrivateAsNeededEnvValue, InjectionMode: store.InjectionModeAsNeeded},
	} {
		v.ID = api.NewUUID()
		v.Scope = store.ScopeUser
		v.ScopeID = ownerID
		v.CreatedBy = ownerID
		if v.InjectionMode == "" {
			v.InjectionMode = store.InjectionModeAlways
		}
		_, err := f.Store.UpsertEnvVar(context.Background(), &v)
		require.NoError(t, err)
	}
	_, _, err := f.Server.secretBackend.Set(context.Background(), &secret.SetSecretInput{
		Name: omcPrivateAsNeededKey, Value: omcPrivateAsNeededValue, SecretType: store.SecretTypeEnvironment,
		Target: omcPrivateAsNeededKey, Scope: store.ScopeUser, ScopeID: ownerID,
		InjectionMode: store.InjectionModeAsNeeded, CreatedBy: ownerID, UpdatedBy: ownerID,
	})
	require.NoError(t, err)
}

func newOMCFixture(t *testing.T, name string) *sdFixture {
	t.Helper()
	f := newSDFixture(t, name)
	omcSeedOwnerEnv(t, f, f.UserID)
	return f
}

// omcDelivery is what start time delivers of the owner's rows. Each field
// counts only the user-scope row.
type omcDelivery struct {
	sharedSecret       bool // sdAlwaysKey
	sharedAsNeeded     bool // sdAsNeededKey
	privateSecret      bool // sdPrivateKey
	privateAsNeeded    bool // omcPrivateAsNeededKey
	sharedEnv          bool // omcSharedEnvKey
	privateEnv         bool // omcPrivateEnvKey
	privateSecretEnv   bool // omcPrivateSecretEnvKey
	privateAsNeededEnv bool // omcPrivateAsNeededEnvKey
}

// omcAll is every row delivered.
var omcAll = omcDelivery{true, true, true, true, true, true, true, true}

// omcStartDelivery drives every start-time site: resolveSecrets and
// resolveEnvFromStorage (create), resolveAsNeededForKeys (both passes), and
// the start and restart launch selection (selectDeliverySecrets and
// selectDeliveryEnv), which must agree with the create-path functions.
func omcStartDelivery(t *testing.T, d *HTTPAgentDispatcher, s store.Store, agentID string) omcDelivery {
	t.Helper()
	ctx := context.Background()
	agent, err := s.GetAgent(ctx, agentID)
	require.NoError(t, err)
	var out omcDelivery

	userSecret := func(rs []ResolvedSecret, name, value string) bool {
		for _, r := range rs {
			if r.Source == store.ScopeUser && r.Name == name && r.Value == value {
				return true
			}
		}
		return false
	}
	resolved, _, err := d.resolveSecrets(ctx, agent)
	require.NoError(t, err)
	out.sharedSecret = userSecret(resolved, sdAlwaysKey, sdAlwaysValue)
	out.privateSecret = userSecret(resolved, sdPrivateKey, sdPrivateValue)

	env, _, err := d.resolveEnvFromStorage(ctx, agent)
	require.NoError(t, err)
	out.sharedEnv = env[omcSharedEnvKey] == omcSharedEnvValue
	out.privateEnv = env[omcPrivateEnvKey] == omcPrivateEnvValue
	_, out.privateSecretEnv = env[omcPrivateSecretEnvKey]

	got, err := d.resolveAsNeededForKeys(ctx, agent, []string{sdAsNeededKey, omcPrivateAsNeededKey, omcPrivateAsNeededEnvKey}, nil)
	require.NoError(t, err)
	out.sharedAsNeeded = got[sdAsNeededKey] == sdAsNeededValue
	out.privateAsNeeded = got[omcPrivateAsNeededKey] == omcPrivateAsNeededValue
	out.privateAsNeededEnv = got[omcPrivateAsNeededEnvKey] == omcPrivateAsNeededEnvValue

	for _, purpose := range []MaterialPurpose{PurposeDispatch, PurposeRestart} {
		sel := newDeliverySelector(d.authzService, agent, purpose)
		selected, err := d.selectDeliverySecrets(ctx, sel, agent)
		require.NoError(t, err)
		assert.Equal(t, out.sharedSecret, userSecret(selected, sdAlwaysKey, sdAlwaysValue), "launch selection (%s) delivers %s as resolveSecrets does", purpose, sdAlwaysKey)
		assert.Equal(t, out.privateSecret, userSecret(selected, sdPrivateKey, sdPrivateValue), "launch selection (%s) delivers %s as resolveSecrets does", purpose, sdPrivateKey)

		selEnv, _, err := d.selectDeliveryEnv(ctx, newDeliverySelector(d.authzService, agent, purpose), agent)
		require.NoError(t, err)
		assert.Equal(t, out.sharedEnv, selEnv[omcSharedEnvKey] == omcSharedEnvValue, "launch selection (%s) delivers %s as resolveEnvFromStorage does", purpose, omcSharedEnvKey)
		assert.Equal(t, out.privateEnv, selEnv[omcPrivateEnvKey] == omcPrivateEnvValue, "launch selection (%s) delivers %s as resolveEnvFromStorage does", purpose, omcPrivateEnvKey)
		_, has := selEnv[omcPrivateSecretEnvKey]
		assert.Equal(t, out.privateSecretEnv, has, "launch selection (%s) delivers %s as resolveEnvFromStorage does", purpose, omcPrivateSecretEnvKey)
	}
	return out
}

// omcRedelegated is the owner's direct agent whose own edge was re-recorded
// to another active project member B (Ancestry and OwnerID stay the
// owner's). It returns the agent and B.
func omcRedelegated(t *testing.T, f *sdFixture, name string) (string, string) {
	t.Helper()
	other := tid("omc-other-" + name)
	createDCUser(t, f.Store, other, name+"-other@test.com", f.ProjectID, store.ProjectRoleOwner)
	id, _ := f.direct(t, name, false)
	f.recordEdge(t, store.DelegationPrincipalUser, other, id, store.EffectCeiling{Kind: store.EffectCeilingPrincipal}, nil)
	return id, other
}

// omcAgentWith stores a full-role agent owned by the fixture user with the
// given ancestry (nil for none) and createdBy, and no edge.
func omcAgentWith(t *testing.T, f *sdFixture, name string, ancestry []string, createdBy string) string {
	t.Helper()
	id := tid("omc-agent-" + name)
	require.NoError(t, f.Store.CreateAgent(context.Background(), &store.Agent{
		ID: id, Slug: "omc-" + id[:8], Name: "omc-" + id[:8], ProjectID: f.ProjectID, Phase: "running",
		CreatedBy: createdBy, OwnerID: f.UserID, StateVersion: 1,
		AppliedConfig: &store.AgentAppliedConfig{AgentRole: string(AgentRoleFull)}, Ancestry: ancestry,
	}))
	return id
}

// TestOwnerMaterialChain_CompositionMatrix: shared and
// non-shared rows, secrets and env vars, on a clean chain and on a
// re-delegated chain. The clean chain delivers everything (the owner's own
// direct agent keeps all of the owner's rows). On the re-delegated chain the
// shared secret is refused by the parent ceiling (its chain does not end at
// the secret's owner), every non-shared row
// by the chain rule, and the shared env var stays on today's rule.
func TestOwnerMaterialChain_CompositionMatrix(t *testing.T) {
	t.Run("clean chain", func(t *testing.T) {
		f := newOMCFixture(t, "omc-clean")
		id, _ := f.direct(t, "clean", true)
		assert.Equal(t, omcAll, omcStartDelivery(t, f.disp, f.Store, id))
	})
	t.Run("re-delegated chain", func(t *testing.T) {
		f := newOMCFixture(t, "omc-redeleg")
		id, _ := omcRedelegated(t, f, "redeleg")
		assert.Equal(t, omcDelivery{sharedEnv: true}, omcStartDelivery(t, f.disp, f.Store, id))
	})
	t.Run("progeny agent: owner path not involved", func(t *testing.T) {
		f := newOMCFixture(t, "omc-progeny")
		child, _, _ := f.progeny(t, "progeny", true)
		got := omcStartDelivery(t, f.disp, f.Store, child)
		assert.True(t, got.sharedSecret, "the progeny path delivers the shared secret")
		assert.True(t, got.sharedEnv, "the progeny pass delivers the shared env var")
		assert.False(t, got.privateSecret || got.privateEnv || got.privateSecretEnv || got.privateAsNeeded,
			"rows not shared never reach a progeny agent")
	})
}

// TestOwnerMaterialChain_ProvenanceRootNotOwner: the chain
// ends at the owner, but the stored ancestry starts at another user. The
// read is keyed on Ancestry[0], so the owner's rows not shared are withheld.
func TestOwnerMaterialChain_ProvenanceRootNotOwner(t *testing.T) {
	f := newOMCFixture(t, "omc-root")
	other := tid("omc-root-other")
	createDCUser(t, f.Store, other, "omc-root-other@test.com", f.ProjectID, store.ProjectRoleOwner)
	id := omcAgentWith(t, f, "root", []string{other}, f.UserID)
	f.recordEdge(t, store.DelegationPrincipalUser, f.UserID, id, store.EffectCeiling{Kind: store.EffectCeilingPrincipal}, nil)

	got := omcStartDelivery(t, f.disp, f.Store, id)
	assert.False(t, got.privateSecret || got.privateAsNeeded || got.privateEnv || got.privateSecretEnv || got.privateAsNeededEnv,
		"every row not shared is withheld: %+v", got)
	assert.False(t, got.sharedSecret, "the parent ceiling refuses the shared secret too: the ancestry does not start at its owner")
	assert.True(t, got.sharedEnv, "the shared env var stays on today's rule")
}

// TestOwnerMaterialChain_LegacyCohorts pins the behaviour for agents and
// rows written by older versions. Legacy chains keep today's delivery of
// the owner's rows not shared and log the cohort;
// recorded shapes are judged by who the chain ends at.
func TestOwnerMaterialChain_LegacyCohorts(t *testing.T) {
	type tc struct {
		name        string
		build       func(t *testing.T, f *sdFixture) string
		wantPrivate bool
		cohort      string // legacy Warn cohort, "" for none
	}
	edge := func(f *sdFixture, t *testing.T, id string, mutate func(*store.DelegationEdge)) {
		f.recordEdge(t, store.DelegationPrincipalUser, f.UserID, id, store.EffectCeiling{Kind: store.EffectCeilingPrincipal}, mutate)
	}
	cases := []tc{
		{"no edge", func(t *testing.T, f *sdFixture) string {
			return omcAgentWith(t, f, "noedge", []string{f.UserID}, f.UserID)
		}, true, legacyDeliveryCohortNoEdge},
		{"empty ancestry, no edge", func(t *testing.T, f *sdFixture) string {
			return omcAgentWith(t, f, "noanc", nil, f.UserID)
		}, true, legacyDeliveryCohortNoEdge},
		{"empty ancestry, edge from the owner", func(t *testing.T, f *sdFixture) string {
			id := omcAgentWith(t, f, "noanc-edge", nil, f.UserID)
			edge(f, t, id, nil)
			return id
		}, true, ""},
		{"empty ancestry, edge from another user", func(t *testing.T, f *sdFixture) string {
			other := tid("omc-noanc-other")
			createDCUser(t, f.Store, other, "omc-noanc-other@test.com", f.ProjectID, store.ProjectRoleOwner)
			id := omcAgentWith(t, f, "noanc-other", nil, f.UserID)
			f.recordEdge(t, store.DelegationPrincipalUser, other, id, store.EffectCeiling{Kind: store.EffectCeilingPrincipal}, nil)
			return id
		}, false, ""},
		{"missing agent CreatedBy", func(t *testing.T, f *sdFixture) string {
			id := omcAgentWith(t, f, "nocreator", []string{f.UserID}, "")
			edge(f, t, id, nil)
			return id
		}, true, ""},
		{"migration sentinel edge", func(t *testing.T, f *sdFixture) string {
			id := omcAgentWith(t, f, "sentinel", []string{f.UserID}, f.UserID)
			f.recordEdge(t, store.DelegationPrincipalUser, migrationDelegatorID, id, store.EffectCeiling{Kind: store.EffectCeilingPrincipal}, nil)
			return id
		}, true, legacyDeliveryCohortSentinelEdge},
		{"edge without recorded provenance", func(t *testing.T, f *sdFixture) string {
			id := omcAgentWith(t, f, "unrec", []string{f.UserID}, f.UserID)
			createDCEdge(t, f.Store, store.DelegationPrincipalUser, f.UserID, store.DelegationPrincipalAgent, id,
				store.RoleScopeProject, f.ProjectID, string(AgentRoleFull))
			return id
		}, true, legacyDeliveryCohortUnadoptedEdge},
		{"edge without recorded provenance from another user", func(t *testing.T, f *sdFixture) string {
			// No recorded delegator to compare: legacy keeps delivery.
			other := tid("omc-unrec-other")
			createDCUser(t, f.Store, other, "omc-unrec-other@test.com", f.ProjectID, store.ProjectRoleOwner)
			id := omcAgentWith(t, f, "unrec-other", []string{f.UserID}, f.UserID)
			createDCEdge(t, f.Store, store.DelegationPrincipalUser, other, store.DelegationPrincipalAgent, id,
				store.RoleScopeProject, f.ProjectID, string(AgentRoleFull))
			return id
		}, true, legacyDeliveryCohortUnadoptedEdge},
		{"recorded principal does not type-check", func(t *testing.T, f *sdFixture) string {
			id := omcAgentWith(t, f, "badprincipal", []string{f.UserID}, f.UserID)
			edge(f, t, id, func(e *store.DelegationEdge) { e.SourcePrincipalKind = store.DelegationPrincipalAgent })
			return id
		}, true, ownerChainCohortInconsistentPrincipal},
		{"adopted baseline edge from the owner", func(t *testing.T, f *sdFixture) string {
			id := omcAgentWith(t, f, "adopted", []string{f.UserID}, f.UserID)
			edge(f, t, id, func(e *store.DelegationEdge) {
				e.SourceCredentialKind = store.SourceCredentialSystemMigration
				e.Role = string(AgentRoleBaseline)
				e.EffectCeiling = store.EffectCeiling{Kind: store.EffectCeilingBounded, PermissionIDs: lafV1(t, AgentRoleBaseline, false)}
			})
			return id
		}, true, ""},
		{"adopted readonly edge from the owner", func(t *testing.T, f *sdFixture) string {
			id := omcAgentWith(t, f, "adopted-ro", []string{f.UserID}, f.UserID)
			edge(f, t, id, func(e *store.DelegationEdge) {
				e.SourceCredentialKind = store.SourceCredentialSystemMigration
				e.Role = string(AgentRoleReadOnly)
				e.EffectCeiling = store.EffectCeiling{Kind: store.EffectCeilingBounded, PermissionIDs: lafV1(t, AgentRoleReadOnly, false)}
			})
			return id
		}, true, ""},
		{"no edge, ancestry starts at another user", func(t *testing.T, f *sdFixture) string {
			// Legacy delivery keeps the ancestry check: withheld.
			other := tid("omc-legacy-root-other")
			createDCUser(t, f.Store, other, "omc-legacy-root-other@test.com", f.ProjectID, store.ProjectRoleOwner)
			return omcAgentWith(t, f, "legacy-root", []string{other}, f.UserID)
		}, false, ""},
		{"edge recorded from a user access token (uat_created_no_deliver_mapping)", func(t *testing.T, f *sdFixture) string {
			id := omcAgentWith(t, f, "uat", []string{f.UserID}, f.UserID)
			edge(f, t, id, func(e *store.DelegationEdge) {
				e.SourceCredentialKind = store.SourceCredentialUAT
				e.EffectCeiling = boundedCeiling("project.read")
			})
			return id
		}, true, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newOMCFixture(t, "omc-legacy")
			id := c.build(t, f)
			var buf bytes.Buffer
			f.disp.log = slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))

			got := omcStartDelivery(t, f.disp, f.Store, id)
			for name, v := range map[string]bool{
				sdPrivateKey: got.privateSecret, omcPrivateAsNeededKey: got.privateAsNeeded,
				omcPrivateEnvKey: got.privateEnv, omcPrivateSecretEnvKey: got.privateSecretEnv,
				omcPrivateAsNeededEnvKey: got.privateAsNeededEnv,
			} {
				assert.Equal(t, c.wantPrivate, v, "row %s", name)
			}
			legacy := omcRecords(t, buf.String(), omcLegacyMsg)
			if c.cohort == "" {
				assert.Empty(t, legacy, "no legacy Warn for a recorded chain")
			} else {
				require.NotEmpty(t, legacy, "the legacy delivery is logged; log: %s", buf.String())
				for _, r := range legacy {
					assert.Equal(t, c.cohort, r["cohort"])
					assert.Equal(t, id, r["agent_id"])
				}
			}
			if c.wantPrivate {
				assert.Empty(t, omcRecords(t, buf.String(), omcWithheldMsg), "nothing withheld")
			}
		})
	}
}

// TestOwnerMaterialChain_RowWithoutCreatedBy: a row recorded before
// CreatedBy existed is judged by its owner (the scope) alone.
func TestOwnerMaterialChain_RowWithoutCreatedBy(t *testing.T) {
	const key, value = "OMC_NO_CREATOR", "omc-no-creator-value"
	for _, redelegated := range []bool{false, true} {
		f := newOMCFixture(t, "omc-nocreator")
		_, _, err := f.Server.secretBackend.Set(context.Background(), &secret.SetSecretInput{
			Name: key, Value: value, SecretType: store.SecretTypeEnvironment, Target: key,
			Scope: store.ScopeUser, ScopeID: f.UserID, InjectionMode: store.InjectionModeAlways,
		})
		require.NoError(t, err)
		var id string
		if redelegated {
			id, _ = omcRedelegated(t, f, "nocreator")
		} else {
			id, _ = f.direct(t, "nocreator", true)
		}
		agent, err := f.Store.GetAgent(context.Background(), id)
		require.NoError(t, err)
		resolved, _, err := f.disp.resolveSecrets(context.Background(), agent)
		require.NoError(t, err)
		found := false
		for _, r := range resolved {
			if r.Name == key && r.Value == value {
				found = true
			}
		}
		assert.Equal(t, !redelegated, found, "redelegated=%v", redelegated)
	}
}

// TestOwnerMaterialChain_FailClosed: a chain that cannot be followed above
// the agent's own edge, a cycle, and a lookup fault withhold the rows.
func TestOwnerMaterialChain_FailClosed(t *testing.T) {
	t.Run("unrecorded hop above the agent", func(t *testing.T) {
		f := newOMCFixture(t, "omc-above")
		parent := omcAgentWith(t, f, "above-parent", []string{f.UserID}, f.UserID)
		id, _ := f.direct(t, "above", false)
		f.recordEdge(t, store.DelegationPrincipalAgent, parent, id, store.EffectCeiling{Kind: store.EffectCeilingPrincipal}, nil)
		agent, err := f.Store.GetAgent(context.Background(), id)
		require.NoError(t, err)
		v := f.Server.authzService.ownerChainVerdict(context.Background(), agent)
		assert.Equal(t, ownerChainVerdict{outcome: ownerChainWithhold, reason: ownerChainReasonUnrecordedAbove}, v)
		got := omcStartDelivery(t, f.disp, f.Store, id)
		assert.False(t, got.privateSecret || got.privateEnv || got.privateSecretEnv)
	})
	t.Run("agent delegator whose chain ends at the owner", func(t *testing.T) {
		f := newOMCFixture(t, "omc-agentdeleg")
		parent, _ := f.direct(t, "agentdeleg-parent", true)
		id, _ := f.direct(t, "agentdeleg", false)
		f.recordEdge(t, store.DelegationPrincipalAgent, parent, id, store.EffectCeiling{Kind: store.EffectCeilingPrincipal}, nil)
		got := omcStartDelivery(t, f.disp, f.Store, id)
		assert.True(t, got.privateSecret && got.privateEnv && got.privateSecretEnv, "%+v", got)
	})
	t.Run("cycle", func(t *testing.T) {
		f := newOMCFixture(t, "omc-cycle")
		a := omcAgentWith(t, f, "cycle-a", []string{f.UserID}, f.UserID)
		b := omcAgentWith(t, f, "cycle-b", []string{f.UserID}, f.UserID)
		f.recordEdge(t, store.DelegationPrincipalAgent, b, a, store.EffectCeiling{Kind: store.EffectCeilingPrincipal}, nil)
		f.recordEdge(t, store.DelegationPrincipalAgent, a, b, store.EffectCeiling{Kind: store.EffectCeilingPrincipal}, nil)
		agent, err := f.Store.GetAgent(context.Background(), a)
		require.NoError(t, err)
		v := f.Server.authzService.ownerChainVerdict(context.Background(), agent)
		assert.Equal(t, ownerChainVerdict{outcome: ownerChainWithhold, reason: ownerChainReasonInvalid}, v)
	})
	t.Run("lookup fault", func(t *testing.T) {
		f := newOMCFixture(t, "omc-fault")
		id, _ := f.direct(t, "fault", true)
		agent, err := f.Store.GetAgent(context.Background(), id)
		require.NoError(t, err)
		a := &AuthzService{store: omcEdgeFaultStore{Store: f.Store}}
		v := a.ownerChainVerdict(context.Background(), agent)
		assert.Equal(t, ownerChainVerdict{outcome: ownerChainWithhold, reason: ownerChainReasonLookupError}, v)
	})
}

type omcEdgeFaultStore struct{ store.Store }

// omcEdgesOverrideStore returns edges for agentID's delegate lookup (the
// store's partial unique index forbids two active edges for one delegate
// and scope); every other lookup is the store's.
type omcEdgesOverrideStore struct {
	store.Store
	agentID string
	edges   []*store.DelegationEdge
}

func (o omcEdgesOverrideStore) GetDelegationEdgesForDelegate(ctx context.Context, delegateType, delegateID string) ([]*store.DelegationEdge, error) {
	if delegateID == o.agentID {
		return o.edges, nil
	}
	return o.Store.GetDelegationEdgesForDelegate(ctx, delegateType, delegateID)
}

// omcTwoEdges is two active, recorded principal edges from the user
// delegator to delegate in the fixture project.
func omcTwoEdges(f *sdFixture, delegatorID, delegateID, name string) []*store.DelegationEdge {
	edge := func(edgeID string) *store.DelegationEdge {
		return &store.DelegationEdge{
			ID: edgeID, DelegatorType: store.DelegationPrincipalUser, DelegatorID: delegatorID,
			DelegateType: store.DelegationPrincipalAgent, DelegateID: delegateID,
			ScopeType: store.RoleScopeProject, ScopeID: f.ProjectID, Role: string(AgentRoleFull), Active: true,
			AuthorityProvenance: store.AuthorityProvenance{ProvenanceVersion: store.ProvenanceVersionV1, SourcePrincipalKind: store.DelegationPrincipalUser, SourcePrincipalID: delegatorID, SourceCredentialKind: store.SourceCredentialSession},
			EffectCeiling:       store.EffectCeiling{Kind: store.EffectCeilingPrincipal},
		}
	}
	return []*store.DelegationEdge{edge(tid(name + "-1")), edge(tid(name + "-2"))}
}

// omcCreateDelivery is what the create-path functions deliver of the
// owner's unshared rows (resolveSecrets and resolveEnvFromStorage, which
// build no delivery credential), and the dispatcher's log.
func omcCreateDelivery(t *testing.T, f *sdFixture, agentID string) (privateSecret, privateEnv bool, log string) {
	t.Helper()
	ctx := context.Background()
	var buf bytes.Buffer
	f.disp.log = slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	agent, err := f.Store.GetAgent(ctx, agentID)
	require.NoError(t, err)
	resolved, _, err := f.disp.resolveSecrets(ctx, agent)
	require.NoError(t, err)
	for _, r := range resolved {
		privateSecret = privateSecret || (r.Source == store.ScopeUser && r.Name == sdPrivateKey && r.Value == sdPrivateValue)
	}
	env, _, err := f.disp.resolveEnvFromStorage(ctx, agent)
	require.NoError(t, err)
	privateEnv = env[omcPrivateEnvKey] == omcPrivateEnvValue
	return privateSecret, privateEnv, buf.String()
}

// TestOwnerMaterialChain_AmbiguousEdgesLegacy: more than one active edge on
// the agent's own hop is a legacy shape: delivery is kept, with the legacy
// Warn (cohort legacy_ambiguous_edges), as the parent ceiling treats it as
// unrecorded. The verdict and the end-to-end delivery are pinned.
func TestOwnerMaterialChain_AmbiguousEdgesLegacy(t *testing.T) {
	f := newOMCFixture(t, "omc-ambig")
	id, _ := f.direct(t, "ambig", false)
	agent, err := f.Store.GetAgent(context.Background(), id)
	require.NoError(t, err)
	f.Server.authzService.store = omcEdgesOverrideStore{Store: f.Server.authzService.store, agentID: id, edges: omcTwoEdges(f, f.UserID, id, "omc-dup")}

	v := f.Server.authzService.ownerChainVerdict(context.Background(), agent)
	assert.Equal(t, ownerChainVerdict{outcome: ownerChainLegacy, reason: ownerChainCohortAmbiguousEdges}, v)

	privateSecret, privateEnv, log := omcCreateDelivery(t, f, id)
	assert.True(t, privateSecret, "legacy delivery is kept")
	assert.True(t, privateEnv, "legacy delivery is kept")
	recs := omcRecords(t, log, omcLegacyMsg)
	require.NotEmpty(t, recs, "log: %s", log)
	for _, r := range recs {
		assert.Equal(t, ownerChainCohortAmbiguousEdges, r["cohort"])
	}
}

// TestOwnerMaterialChain_AmbiguousEdgesAboveAgent: more than one active edge
// above the agent's own hop cannot be followed: the rows are withheld
// (unrecorded_hop_above_agent), with the withheld Warn.
func TestOwnerMaterialChain_AmbiguousEdgesAboveAgent(t *testing.T) {
	f := newOMCFixture(t, "omc-ambig-above")
	parent := omcAgentWith(t, f, "ambig-above-parent", []string{f.UserID}, f.UserID)
	id, _ := f.direct(t, "ambig-above", false)
	f.recordEdge(t, store.DelegationPrincipalAgent, parent, id, store.EffectCeiling{Kind: store.EffectCeilingPrincipal}, nil)
	f.Server.authzService.store = omcEdgesOverrideStore{Store: f.Server.authzService.store, agentID: parent, edges: omcTwoEdges(f, f.UserID, parent, "omc-dup-above")}

	agent, err := f.Store.GetAgent(context.Background(), id)
	require.NoError(t, err)
	v := f.Server.authzService.ownerChainVerdict(context.Background(), agent)
	assert.Equal(t, ownerChainVerdict{outcome: ownerChainWithhold, reason: ownerChainReasonUnrecordedAbove}, v)

	privateSecret, privateEnv, log := omcCreateDelivery(t, f, id)
	assert.False(t, privateSecret)
	assert.False(t, privateEnv)
	recs := omcRecords(t, log, omcWithheldMsg)
	require.NotEmpty(t, recs, "log: %s", log)
	for _, r := range recs {
		assert.Equal(t, ownerChainReasonUnrecordedAbove, r["reason"])
	}
}

// TestOwnerMaterialChain_LookupFaultEndToEnd: a store fault while following
// the chain withholds the rows at every site, with the withheld Warn.
func TestOwnerMaterialChain_LookupFaultEndToEnd(t *testing.T) {
	f := newOMCFixture(t, "omc-fault-e2e")
	id, _ := f.direct(t, "fault-e2e", true)
	f.Server.authzService.store = omcEdgeFaultStore{Store: f.Server.authzService.store}

	privateSecret, privateEnv, log := omcCreateDelivery(t, f, id)
	assert.False(t, privateSecret)
	assert.False(t, privateEnv)
	recs := omcRecords(t, log, omcWithheldMsg)
	require.NotEmpty(t, recs, "log: %s", log)
	for _, r := range recs {
		assert.Equal(t, ownerChainReasonLookupError, r["reason"])
	}
}

// TestOwnerMaterialChain_VerdictOncePerDispatch: inside one dispatch (the
// memo withOwnerChainMemo installs) the chain is followed once, whatever the
// number of sites; without a memo each call follows it.
func TestOwnerMaterialChain_VerdictOncePerDispatch(t *testing.T) {
	f := newOMCFixture(t, "omc-memo")
	id, _ := f.direct(t, "memo", true)
	agent, err := f.Store.GetAgent(context.Background(), id)
	require.NoError(t, err)
	counter := &omcEdgeCountingStore{Store: f.Store}
	a := &AuthzService{store: counter}

	ctx := withOwnerChainMemo(context.Background())
	assert.Equal(t, ctx, withOwnerChainMemo(ctx), "a nested dispatch keeps the memo")
	first := a.ownerChainVerdictFor(ctx, agent)
	n := counter.calls
	require.Positive(t, n)
	for i := 0; i < 3; i++ {
		assert.Equal(t, first, a.ownerChainVerdictFor(ctx, agent))
	}
	assert.Equal(t, n, counter.calls, "the verdict is reused within the dispatch")

	a.ownerChainVerdictFor(context.Background(), agent)
	assert.Greater(t, counter.calls, n, "without a memo the chain is followed again")
}

type omcEdgeCountingStore struct {
	store.Store
	calls int
}

func (c *omcEdgeCountingStore) GetDelegationEdgesForDelegate(ctx context.Context, delegateType, delegateID string) ([]*store.DelegationEdge, error) {
	c.calls++
	return c.Store.GetDelegationEdgesForDelegate(ctx, delegateType, delegateID)
}

const (
	omcUserTZ    = "Europe/Paris"
	omcProjectTZ = "Asia/Tokyo"
	omcHubTZ     = "America/Chicago"
)

// omcSeedTZ writes a TZ env row in scope.
func omcSeedTZ(t *testing.T, f *sdFixture, scope, scopeID, tz string, shared bool) {
	t.Helper()
	_, err := f.Store.UpsertEnvVar(context.Background(), &store.EnvVar{
		ID: api.NewUUID(), Key: agentTZEnvKey, Value: tz, Scope: scope, ScopeID: scopeID,
		InjectionMode: store.InjectionModeAlways, AllowProgeny: shared, CreatedBy: scopeID,
	})
	require.NoError(t, err)
}

// TestOwnerMaterialChain_TZ: the owner's user-scope TZ row is a rung of the
// agent TZ chain. Not shared, it is held to the chain rule: a re-delegated
// agent takes TZ from a lower scope, or the hub default when none has one.
// Shared, it stays on today's rule.
func TestOwnerMaterialChain_TZ(t *testing.T) {
	for _, c := range []struct {
		name        string
		shared      bool
		redelegated bool
		projectRow  bool
		want        agentTZ
	}{
		{"clean chain, unshared", false, false, true, agentTZ{TZ: omcUserTZ, Source: TZSourceUser}},
		{"re-delegated, unshared, project row below", false, true, true, agentTZ{TZ: omcProjectTZ, Source: TZSourceProject}},
		{"re-delegated, unshared, no lower row", false, true, false, agentTZ{TZ: omcHubTZ, Source: TZSourceHubDefault}},
		{"re-delegated, shared", true, true, true, agentTZ{TZ: omcUserTZ, Source: TZSourceUser}},
		{"clean chain, shared", true, false, false, agentTZ{TZ: omcUserTZ, Source: TZSourceUser}},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newOMCFixture(t, "omc-tz")
			f.disp.SetHubAgentDefaultsProvider(func() opsettings.AgentDefaultsSettings {
				return opsettings.AgentDefaultsSettings{DefaultTimezone: omcHubTZ}
			})
			omcSeedTZ(t, f, store.ScopeUser, f.UserID, omcUserTZ, c.shared)
			if c.projectRow {
				omcSeedTZ(t, f, store.ScopeProject, f.ProjectID, omcProjectTZ, false)
			}
			var id string
			if c.redelegated {
				id, _ = omcRedelegated(t, f, "tz")
			} else {
				id, _ = f.direct(t, "tz", true)
			}
			agent, err := f.Store.GetAgent(context.Background(), id)
			require.NoError(t, err)
			got := f.disp.resolveAgentTZ(context.Background(), agent, false)
			assert.Equal(t, c.want.TZ, got.TZ)
			assert.Equal(t, c.want.Source, got.Source)
		})
	}
}

// TestOwnerMaterialChain_ReportsSkipWithheldRows: the read-only reports
// apply the rule without logging. For a re-delegated agent the owner's
// withheld rows do not count as present (hasAnyKey), do not label a key
// "user" (buildEnvSources, the env-gather hubHas labels), and do not raise
// the "stored but not included" warning; a clean chain is unchanged.
func TestOwnerMaterialChain_ReportsSkipWithheldRows(t *testing.T) {
	const projectKey = "OMC_LABEL_KEY"
	for _, redelegated := range []bool{false, true} {
		name := "clean"
		if redelegated {
			name = "re-delegated"
		}
		t.Run(name, func(t *testing.T) {
			f := newOMCFixture(t, "omc-reports")
			ctx := context.Background()
			// The owner's unshared row and a project row for the same key.
			_, err := f.Store.UpsertEnvVar(ctx, &store.EnvVar{ID: api.NewUUID(), Key: projectKey, Value: "user-v", Scope: store.ScopeUser, ScopeID: f.UserID, InjectionMode: store.InjectionModeAlways, CreatedBy: f.UserID})
			require.NoError(t, err)
			_, err = f.Store.UpsertEnvVar(ctx, &store.EnvVar{ID: api.NewUUID(), Key: projectKey, Value: "project-v", Scope: store.ScopeProject, ScopeID: f.ProjectID, InjectionMode: store.InjectionModeAlways, CreatedBy: f.UserID})
			require.NoError(t, err)
			var id string
			if redelegated {
				id, _ = omcRedelegated(t, f, "reports")
			} else {
				id, _ = f.direct(t, "reports", true)
			}
			agent, err := f.Store.GetAgent(ctx, id)
			require.NoError(t, err)
			var buf bytes.Buffer
			f.disp.log = slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))

			// hasAnyKey: the owner-only keys.
			for _, key := range []string{omcPrivateEnvKey, sdPrivateKey} {
				found, err := f.Server.hasAnyKey(ctx, agent, []string{key})
				require.NoError(t, err)
				assert.Equal(t, !redelegated, found, "hasAnyKey(%s)", key)
			}
			shared, err := f.Server.hasAnyKey(ctx, agent, []string{omcSharedEnvKey})
			require.NoError(t, err)
			assert.True(t, shared, "a shared row stays on today's rule")

			// buildEnvSources.
			sources := f.disp.buildEnvSources(ctx, agent, map[string]string{projectKey: "x", omcPrivateEnvKey: "x"})
			if redelegated {
				assert.Equal(t, "project", sources[projectKey], "the withheld user row does not label the key")
				assert.NotContains(t, sources, omcPrivateEnvKey)
			} else {
				assert.Equal(t, "user", sources[projectKey])
				assert.Equal(t, "user", sources[omcPrivateEnvKey])
			}

			// buildEnvGatherResponse.
			resp := f.Server.buildEnvGatherResponse(ctx, agent, &RemoteEnvRequirementsResponse{
				HubHas: []string{projectKey}, Needs: []string{omcPrivateEnvKey, sdPrivateKey},
			})
			require.Len(t, resp.HubHas, 1)
			userWarnings := 0
			for _, w := range resp.HubWarnings {
				if strings.Contains(w, "(user scope)") {
					userWarnings++
				}
			}
			if redelegated {
				assert.Equal(t, "project", resp.HubHas[0].Scope)
				assert.Zero(t, userWarnings, "no user-scope warning for withheld rows: %v", resp.HubWarnings)
			} else {
				assert.Equal(t, "user", resp.HubHas[0].Scope)
				assert.Equal(t, 2, userWarnings, "%v", resp.HubWarnings)
			}
			assert.Empty(t, omcRecords(t, buf.String(), omcWithheldMsg), "the reports log nothing")
		})
	}
}

// TestOwnerRowDecision covers the per-row rule in isolation.
func TestOwnerRowDecision(t *testing.T) {
	const u, b = "user-u", "user-b"
	ends := func(id string) ownerChainVerdict {
		return ownerChainVerdict{outcome: ownerChainEndsAtUser, terminalUserID: id}
	}
	legacy := ownerChainVerdict{outcome: ownerChainLegacy, reason: legacyDeliveryCohortNoEdge}
	for _, c := range []struct {
		name         string
		v            ownerChainVerdict
		ancestry     []string
		owner        string
		wantDeliver  bool
		wantLegacy   bool
		wantReasonEq string
	}{
		{"clean", ends(u), []string{u}, u, true, false, ""},
		{"clean, no ancestry", ends(u), nil, u, true, false, ""},
		{"terminal is another user", ends(b), []string{u}, u, false, false, ownerChainReasonTerminalNotOwner},
		{"root is another user", ends(u), []string{b}, u, false, false, ownerChainReasonRootNotOwner},
		{"empty owner", ends(u), []string{u}, "", false, false, ownerChainReasonRootNotOwner},
		{"legacy", legacy, []string{u}, u, true, true, legacyDeliveryCohortNoEdge},
		{"legacy, root is another user", legacy, []string{b}, u, false, false, ownerChainReasonRootNotOwner},
		{"withhold", ownerChainVerdict{outcome: ownerChainWithhold, reason: ownerChainReasonLookupError}, []string{u}, u, false, false, ownerChainReasonLookupError},
	} {
		t.Run(c.name, func(t *testing.T) {
			deliver, isLegacy, reason := ownerRowDecision(c.v, c.ancestry, c.owner)
			assert.Equal(t, c.wantDeliver, deliver)
			assert.Equal(t, c.wantLegacy, isLegacy)
			assert.Equal(t, c.wantReasonEq, reason)
		})
	}
}

// omcRecords returns the WARN records of a JSON log with message msg.
func omcRecords(t *testing.T, raw, msg string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(raw), "\n") {
		var rec map[string]any
		if line == "" || json.Unmarshal([]byte(line), &rec) != nil {
			continue
		}
		if rec["level"] == "WARN" && rec["msg"] == msg {
			out = append(out, rec)
		}
	}
	return out
}

type omcMetrics struct {
	mu   sync.Mutex
	got  map[string]int64 // site/outcome/reason/kind
	seen int
}

func (m *omcMetrics) RecordOwnerMaterialChain(_ context.Context, site, outcome, reason, kind string, n int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.got == nil {
		m.got = map[string]int64{}
	}
	m.got[site+"/"+outcome+"/"+reason+"/"+kind] += n
	m.seen++
}

// TestOwnerMaterialChain_WithheldObservable: every withheld row is counted
// and reported. One Warn per site and reason names the agent, the counts
// and the reason, never a key or a value; a repeat within the window is
// suppressed; the metric counts every row.
func TestOwnerMaterialChain_WithheldObservable(t *testing.T) {
	f := newOMCFixture(t, "omc-warn")
	id, other := omcRedelegated(t, f, "warn")
	m := &omcMetrics{}
	f.Server.SetOwnerMaterialChainMetrics(m)
	var buf bytes.Buffer
	f.disp.log = slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	before := f.Server.authzService.OwnerMaterialChainWithheld()

	agent, err := f.Store.GetAgent(context.Background(), id)
	require.NoError(t, err)
	sel := newDeliverySelector(f.disp.authzService, agent, PurposeRestart)
	_, err = f.disp.selectDeliverySecrets(context.Background(), sel, agent)
	require.NoError(t, err)
	_, _, err = f.disp.selectDeliveryEnv(context.Background(), newDeliverySelector(f.disp.authzService, agent, PurposeRestart), agent)
	require.NoError(t, err)

	recs := omcRecords(t, buf.String(), omcWithheldMsg)
	require.Len(t, recs, 2, "one Warn per site; log: %s", buf.String())
	bySite := map[string]map[string]any{}
	for _, r := range recs {
		bySite[r["site"].(string)] = r
		assert.Equal(t, id, r["agent_id"])
		assert.Equal(t, f.UserID, r["owner_id"])
		assert.Equal(t, other, r["terminal_user_id"])
		assert.Equal(t, ownerChainReasonTerminalNotOwner, r["reason"])
	}
	require.Contains(t, bySite, ownerMaterialSiteLaunchSecrets)
	require.Contains(t, bySite, ownerMaterialSiteLaunchEnv)
	// The launch selection lists every owner row: sdPrivateKey and
	// omcPrivateAsNeededKey are the two secrets not shared; the launch env
	// pass skips as-needed rows, leaving two env vars.
	assert.EqualValues(t, 2, bySite[ownerMaterialSiteLaunchSecrets]["secrets"])
	assert.EqualValues(t, 0, bySite[ownerMaterialSiteLaunchSecrets]["env_vars"])
	assert.EqualValues(t, 0, bySite[ownerMaterialSiteLaunchEnv]["secrets"])
	assert.EqualValues(t, 2, bySite[ownerMaterialSiteLaunchEnv]["env_vars"])
	for _, s := range []string{sdPrivateKey, omcPrivateAsNeededKey, omcPrivateEnvKey, omcPrivateSecretEnvKey,
		sdPrivateValue, omcPrivateAsNeededValue, omcPrivateEnvValue, omcPrivateSecretEnvValue} {
		assert.NotContains(t, buf.String(), s, "a withheld row's key or value was logged")
	}
	assert.Equal(t, before+4, f.Server.authzService.OwnerMaterialChainWithheld())
	assert.EqualValues(t, 2, m.got[ownerMaterialSiteLaunchSecrets+"/withheld/"+ownerChainReasonTerminalNotOwner+"/"+ownerMaterialKindSecret])
	assert.EqualValues(t, 2, m.got[ownerMaterialSiteLaunchEnv+"/withheld/"+ownerChainReasonTerminalNotOwner+"/"+ownerMaterialKindEnvVar])

	// A second start within the window: counted, not logged again.
	buf.Reset()
	_, err = f.disp.selectDeliverySecrets(context.Background(), newDeliverySelector(f.disp.authzService, agent, PurposeRestart), agent)
	require.NoError(t, err)
	assert.Empty(t, omcRecords(t, buf.String(), omcWithheldMsg), "rate limited")
	assert.Equal(t, before+6, f.Server.authzService.OwnerMaterialChainWithheld())
}

// omcDispatched is what a real create, start and restart send the broker of
// the owner's rows not shared, and of the shared secret.
type omcDispatched struct {
	sharedSecret, privateSecret, privateEnv, privateSecretEnv bool
	tz                                                        string
}

// omcDispatch runs DispatchAgentCreate, DispatchAgentStart and
// DispatchAgentRestart for the agent through a mock broker and returns what
// each sent.
func omcDispatch(t *testing.T, f *sdFixture, agentID string, noAuth bool) (create, start, restart omcDispatched, client *mockRuntimeBrokerClient) {
	t.Helper()
	ctx := context.Background()
	brokerID := tid("omc-broker-" + agentID[:8])
	require.NoError(t, f.Store.CreateRuntimeBroker(ctx, &store.RuntimeBroker{
		ID: brokerID, Name: "omc-broker-" + agentID[:8], Slug: "omc-broker-" + agentID[:8],
		Endpoint: "http://localhost:9800", Status: store.BrokerStatusOnline,
	}))
	require.NoError(t, f.Store.AddProjectProvider(ctx, &store.ProjectProvider{
		ProjectID: f.ProjectID, BrokerID: brokerID, BrokerName: "omc-broker-" + agentID[:8],
		LocalPath: "/home/user/projects/omc/.scion", Status: store.BrokerStatusOnline,
	}))
	agent, err := f.Store.GetAgent(ctx, agentID)
	require.NoError(t, err)
	agent.RuntimeBrokerID = brokerID
	agent.AppliedConfig.HarnessConfig = "claude"
	agent.AppliedConfig.NoAuth = noAuth

	svc, err := NewAgentTokenService(AgentTokenConfig{})
	require.NoError(t, err)
	client = &mockRuntimeBrokerClient{}
	d := NewHTTPAgentDispatcherWithClient(f.Store, client, false, slog.Default())
	d.SetTokenGenerator(runTokenGenerator{svc: svc})
	d.SetSecretBackend(f.Server.secretBackend)
	d.SetAuthzService(f.Server.authzService)

	of := func(rs []ResolvedSecret, env map[string]string) omcDispatched {
		var o omcDispatched
		for _, r := range rs {
			if r.Source != store.ScopeUser {
				continue
			}
			o.sharedSecret = o.sharedSecret || (r.Name == sdAlwaysKey && r.Value == sdAlwaysValue)
			o.privateSecret = o.privateSecret || (r.Name == sdPrivateKey && r.Value == sdPrivateValue)
		}
		o.privateEnv = env[omcPrivateEnvKey] == omcPrivateEnvValue
		_, o.privateSecretEnv = env[omcPrivateSecretEnvKey]
		o.tz = env[agentTZEnvKey]
		return o
	}
	_, err = d.DispatchAgentCreate(ctx, agent)
	require.NoError(t, err)
	require.NotNil(t, client.lastCreateReq)
	create = of(client.lastCreateReq.ResolvedSecrets, client.lastCreateReq.ResolvedEnv)
	if noAuth {
		return create, start, restart, client
	}
	require.NoError(t, d.DispatchAgentStart(ctx, agent, "", false))
	require.True(t, client.startCalled)
	start = of(client.lastStartResolvedSecrets, client.lastResolvedEnv)
	require.NoError(t, d.DispatchAgentRestart(ctx, agent))
	require.True(t, client.restartCalled)
	restart = of(client.lastRestartExtras.ResolvedSecrets, client.lastRestartResolvedEnv)
	return create, start, restart, client
}

// TestOwnerMaterialChain_CreateStartRestartParity: create, start and
// restart send the same owner rows, on a clean chain (all, and the owner's
// TZ), a re-delegated chain (none of the rows not shared, not the shared
// secret, and TZ from the project row below) and a legacy chain (all).
func TestOwnerMaterialChain_CreateStartRestartParity(t *testing.T) {
	for _, c := range []struct {
		name  string
		build func(t *testing.T, f *sdFixture) string
		want  omcDispatched
	}{
		{"clean", func(t *testing.T, f *sdFixture) string {
			id, _ := f.direct(t, "parity-clean", true)
			return id
		}, omcDispatched{sharedSecret: true, privateSecret: true, privateEnv: true, privateSecretEnv: true, tz: omcUserTZ}},
		{"re-delegated", func(t *testing.T, f *sdFixture) string {
			id, _ := omcRedelegated(t, f, "parity-redeleg")
			return id
		}, omcDispatched{tz: omcProjectTZ}},
		{"legacy, no edge", func(t *testing.T, f *sdFixture) string {
			return omcAgentWith(t, f, "parity-legacy", []string{f.UserID}, f.UserID)
		}, omcDispatched{sharedSecret: true, privateSecret: true, privateEnv: true, privateSecretEnv: true, tz: omcUserTZ}},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newOMCFixture(t, "omc-parity")
			// The owner's unshared TZ row, and a project TZ row below it.
			omcSeedTZ(t, f, store.ScopeUser, f.UserID, omcUserTZ, false)
			omcSeedTZ(t, f, store.ScopeProject, f.ProjectID, omcProjectTZ, false)
			id := c.build(t, f)
			create, start, restart, _ := omcDispatch(t, f, id, false)
			assert.Equal(t, c.want, create, "create")
			assert.Equal(t, c.want, start, "start")
			assert.Equal(t, c.want, restart, "restart")
		})
	}
}

// TestOwnerMaterialChain_NoAuthCloneToken: the NoAuth clone-token fallback
// reads the owner's GITHUB_TOKEN by name. It is held to the same checks: a
// clean chain gets it, a re-delegated chain does not.
func TestOwnerMaterialChain_NoAuthCloneToken(t *testing.T) {
	const token = "omc-owner-clone-token"
	for _, c := range []struct {
		name   string
		shared bool
		build  func(t *testing.T, f *sdFixture) string
		want   bool
	}{
		{"clean, not shared", false, func(t *testing.T, f *sdFixture) string {
			id, _ := f.direct(t, "gh-clean", true)
			return id
		}, true},
		{"clean, shared", true, func(t *testing.T, f *sdFixture) string {
			id, _ := f.direct(t, "gh-clean-shared", true)
			return id
		}, true},
		{"re-delegated, not shared", false, func(t *testing.T, f *sdFixture) string {
			id, _ := omcRedelegated(t, f, "gh-redeleg")
			return id
		}, false},
		{"re-delegated, shared", true, func(t *testing.T, f *sdFixture) string {
			id, _ := omcRedelegated(t, f, "gh-redeleg-shared")
			return id
		}, false},
		{"legacy, no edge", false, func(t *testing.T, f *sdFixture) string {
			return omcAgentWith(t, f, "gh-legacy", []string{f.UserID}, f.UserID)
		}, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newOMCFixture(t, "omc-gh")
			_, _, err := f.Server.secretBackend.Set(context.Background(), &secret.SetSecretInput{
				Name: "GITHUB_TOKEN", Value: token, SecretType: store.SecretTypeEnvironment, Target: "GITHUB_TOKEN",
				Scope: store.ScopeUser, ScopeID: f.UserID, InjectionMode: store.InjectionModeAlways,
				AllowProgeny: c.shared, CreatedBy: f.UserID, UpdatedBy: f.UserID,
			})
			require.NoError(t, err)
			id := c.build(t, f)
			_, _, _, client := omcDispatch(t, f, id, true)
			assert.Equal(t, c.want, client.lastCreateReq.ResolvedEnv["GITHUB_TOKEN"] == token)
		})
	}
}

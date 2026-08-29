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
	"context"

	"github.com/GoogleCloudPlatform/scion/pkg/secret"
	"github.com/GoogleCloudPlatform/scion/pkg/store"
	"github.com/go-jose/go-jose/v4/jwt"
)

// computeEntitledSecretKeys returns the full set of secret key names that
// an agent is entitled to fetch via the secrets endpoint.
//
// Entitlement is derived from the key LISTING (what secrets exist and are
// scoped to this agent), NOT from resolved values. A secret whose value
// cannot be read or decrypted at this instant is still present in the
// listing and still part of the entitled set — the value question is
// answered at fetch time, not at entitlement time.
//
// This function is the SINGLE source of truth for entitled-set computation.
// Every credential writer (create, start, restart, resetAuth, refresh)
// must call this function. Do not derive entitlement from Resolve() output
// or from any other path. (#127, R7/R8)
//
// Errors are returned only on listing failures (store.ListSecrets or
// store.ListProgenySecrets). On error, callers must NOT record an empty
// entitled set — that would silently strip the agent. Record nothing
// (leave NULL) and log loudly.
func computeEntitledSecretKeys(
	ctx context.Context,
	backend secret.SecretBackend,
	secretStore store.SecretStore,
	authzService *AuthzService,
	agent *store.Agent,
) ([]string, error) {
	if backend == nil {
		return nil, nil
	}

	hubID := backend.HubID()

	// Scope precedence matches Resolve(): runtime_broker < hub < project < user.
	// We only need the key names, not values, so we use List (metadata only).
	// A key that appears in multiple scopes is deduplicated — the merged map
	// mirrors the merge logic in Resolve(), ensuring the entitled set covers
	// exactly the keys that Resolve() would have considered.
	merged := make(map[string]struct{})

	type scopeEntry struct {
		scope   string
		scopeID string
	}
	scopes := make([]scopeEntry, 0, 4)
	if agent.RuntimeBrokerID != "" {
		scopes = append(scopes, scopeEntry{scope: store.ScopeRuntimeBroker, scopeID: agent.RuntimeBrokerID})
	}
	scopes = append(scopes, scopeEntry{scope: store.ScopeHub, scopeID: hubID})
	if agent.ProjectID != "" {
		scopes = append(scopes, scopeEntry{scope: store.ScopeProject, scopeID: agent.ProjectID})
	}
	if agent.OwnerID != "" {
		scopes = append(scopes, scopeEntry{scope: store.ScopeUser, scopeID: agent.OwnerID})
	}

	for _, sc := range scopes {
		secrets, err := secretStore.ListSecrets(ctx, store.SecretFilter{
			Scope:   sc.scope,
			ScopeID: sc.scopeID,
		})
		if err != nil {
			return nil, err
		}
		for _, s := range secrets {
			// Exclude hub-internal infrastructure secrets, matching Resolve().
			if s.SecretType == store.SecretTypeInternal {
				continue
			}
			merged[s.Key] = struct{}{}
		}
	}

	// Progeny secret listing: same gate and same authz check as Resolve().
	// Ancestry length > 1 means the agent was created by another agent;
	// authzService must be present to evaluate the policy check.
	// (localbackend.go:224, gcpbackend.go:385)
	if len(agent.Ancestry) > 1 && authzService != nil {
		progenySecrets, err := secretStore.ListProgenySecrets(ctx, agent.Ancestry)
		if err != nil {
			return nil, err
		}
		for _, s := range progenySecrets {
			// Skip if a higher-precedence scope already set this key,
			// matching Resolve() (localbackend.go:231).
			if _, exists := merged[s.Key]; exists {
				continue
			}
			// Skip internal secrets, matching Resolve() (localbackend.go:235).
			if s.SecretType == store.SecretTypeInternal {
				continue
			}

			// Apply the same authz check that Resolve() applies to progeny
			// candidates (localbackend.go:242, gcpbackend.go:400). Without
			// this, entitlement would be WIDER than what resolution grants,
			// and the entitled set is about to become what the fetch endpoint
			// trusts. (R10)
			//
			// Resolve() calls opts.AuthzCheck(meta), which the dispatcher
			// wires as authzService.CheckAccess with the secret's ID as the
			// resource (httpdispatcher.go:2716–2726). We call CheckAccess
			// directly with the same inputs.
			agentID := agent.ID
			ancestry := agent.Ancestry
			decision := authzService.CheckAccess(ctx, &agentIdentityWrapper{
				AgentTokenClaims: &AgentTokenClaims{
					Claims:    jwt.Claims{Subject: agentID},
					ProjectID: agent.ProjectID,
					Ancestry:  ancestry,
				},
			}, Resource{
				Type: "secret",
				ID:   s.ID,
			}, ActionRead)
			if !decision.Allowed {
				continue
			}

			merged[s.Key] = struct{}{}
		}
	}

	keys := make([]string, 0, len(merged))
	for k := range merged {
		keys = append(keys, k)
	}
	return keys, nil
}

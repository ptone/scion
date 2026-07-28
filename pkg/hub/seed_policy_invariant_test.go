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

package hub

import (
	"context"
	"log/slog"
	"testing"

	"github.com/GoogleCloudPlatform/scion/pkg/api"
	"github.com/GoogleCloudPlatform/scion/pkg/secret"
	"github.com/GoogleCloudPlatform/scion/pkg/store"
	"github.com/stretchr/testify/require"
)

// TestSystemPolicies_NoWildcardOrDispatchAction pins the invariant that no
// policy the system creates on its own grants hub-wide privilege or dispatch.
//
// WHY THIS IS A TEST AND NOT A COMMENT. matchesAction (authz.go) grants an
// action iff the policy's Actions contains "*" or the exact action, and
// matchesResource treats ResourceType "*" as matching every resource type. So a
// "*"-resource policy's Actions are the only thing scoping it: whatever action
// it lists, it grants on every resource type hub-wide. hub-member-read-all
// already has ResourceType "*"; the only thing keeping it from being a hub-wide
// grant of anything is its Actions list, and nothing else in the code enforces
// that. This test is the enforcement.
//
// TWO LAYERS, STRICTLY STRONGER TOGETHER — do not simplify one away:
//   - ALLOWLIST (exhaustive over the policy, the thing we control): any policy
//     whose ResourceType is "*" must have Actions that are a SUBSET of
//     {read, list}. This is exhaustive over the policy shape rather than over a
//     list of dangerous actions we would have to enumerate — assign, create,
//     delete, manage, dispatch, ... — and could never complete. A denylist of a
//     few named actions would pass a hub-wide "assign" grant; this does not.
//   - DENYLIST (for the concrete-resource rows the allowlist never fires on): a
//     row with a concrete ResourceType such as "broker" and Actions ["dispatch"]
//     passes the allowlist, so we also forbid the "*" and dispatch actions on
//     every policy regardless of resource type.
//
// THE UNIVERSE (the filter, so a future reader can tell whether a new policy
// site belongs in scope). "System-created" = every policy the server writes
// without a caller authoring it. There are exactly four such sites on this
// branch:
//   - seed.go:seedDefaultPoliciesAndGroups   -> hub-member-read-all, hub-member-create-projects
//   - handlers_projects_core.go:createProjectMembersGroupAndPolicy -> project:<slug>:member-create-agents
//   - handlers_env_secrets.go:ensureProgenyPolicy -> progeny secret policy
// Deliberately OUT of scope: the caller-authored policy API
// (handlers_policies.go), read-hydration in the store adapter, and test
// fixtures. If you add a new system-created policy site, drive it here too or
// this test is no longer the same sentence as the claim it pins.
//
// MERGE OBLIGATION — svc-accnt branch. That branch adds a FIFTH system-created
// site: seed.go:ensureProjectAssignPolicy, creating
// project:<slug>:assign-gcp-service-accounts (ScopeType project, ResourceType
// gcp_service_account, Actions ["assign"]). It passes both layers on the merits
// (concrete resource, no "*"/dispatch). But by the rule above it MUST be driven
// here and added to the anti-vacuous name list below at merge time. If it is
// not, this test stays green while measuring less than it claims — the empty-scan
// failure mode arriving through a merge instead of an empty universe. Neither
// branch can certify the merged claim alone.
//
// The test drives every site for real and then scans every resulting policy,
// rather than reading the literals, so a forbidden action added at any of them
// fails here.
func TestSystemPolicies_NoWildcardOrDispatchAction(t *testing.T) {
	st, err := newTestStore(":memory:")
	require.NoError(t, err)
	defer func() { _ = st.Close() }()

	ctx := context.Background()
	srv := &Server{store: st, envSecretLog: slog.Default()}

	// Site 1+2: the hub seed.
	seedDefaultPoliciesAndGroups(ctx, st)

	// Site 3: the per-project member policy. Create the owning user and project
	// first so the group/policy write has its referents.
	owner := &store.User{
		ID: api.NewUUID(), Email: "owner@example.com", DisplayName: "Owner",
		Role: store.UserRoleMember, Status: "active",
	}
	require.NoError(t, st.CreateUser(ctx, owner))
	project := &store.Project{
		ID: api.NewUUID(), Name: "Invariant Project", Slug: "invariant-project",
		OwnerID: owner.ID, CreatedBy: owner.ID,
	}
	require.NoError(t, st.CreateProject(ctx, project))
	srv.createProjectMembersGroupAndPolicy(ctx, project, owner.ID)

	// Site 4: the implicit progeny-secret policy.
	progenyMeta := &secret.SecretMeta{
		ID: api.NewUUID(), Name: "API_KEY", SecretType: "environment",
		Scope: "user", ScopeID: owner.ID, AllowProgeny: true, CreatedBy: owner.ID,
	}
	srv.ensureProgenyPolicy(ctx, progenyMeta)

	// Enumerate every policy now in the store. With no caller-authored policy API
	// exercised, every row here is system-created.
	result, err := st.ListPolicies(ctx, store.PolicyFilter{}, store.ListOptions{Limit: 1000})
	require.NoError(t, err)

	// Guard against a vacuous pass: if a creation path silently no-ops, an empty
	// scan would pass and pin nothing. Assert the four known policies are present
	// by name before scanning, so the instrument cannot pass by measuring nothing.
	byName := make(map[string]store.Policy, len(result.Items))
	for _, p := range result.Items {
		byName[p.Name] = p
	}
	for _, want := range []string{
		"hub-member-read-all",
		"hub-member-create-projects",
		"project:" + project.Slug + ":member-create-agents",
		progenyPolicyName(progenyMeta.ID),
	} {
		_, ok := byName[want]
		require.Truef(t, ok, "expected system-created policy %q to be present; the "+
			"test's universe is stale relative to the code it pins", want)
	}

	// Layer 1 — allowlist. A "*" ResourceType matches every resource type, so the
	// Actions are the only scope; they must stay within {read, list}. Exhaustive
	// over the policy shape, not over a threat taxonomy we cannot enumerate.
	wildcardResourceAllowedActions := map[string]bool{"read": true, "list": true}

	// Layer 2 — denylist, for the concrete-resource rows layer 1 never fires on.
	for _, p := range result.Items {
		for _, a := range p.Actions {
			if p.ResourceType == "*" {
				require.Truef(t, wildcardResourceAllowedActions[a],
					"system-created policy %q has ResourceType %q, which matches every "+
						"resource type, and lists action %q — outside the {read, list} "+
						"allowlist. A wildcard-resource policy grants its actions hub-wide "+
						"on every resource type, so it may grant only read and list.",
					p.Name, p.ResourceType, a)
			}
			require.NotEqualf(t, "*", a,
				"system-created policy %q carries the action wildcard %q; combined with "+
					"its ResourceType %q this grants every action. Keep Actions a concrete "+
					"list.", p.Name, a, p.ResourceType)
			require.NotEqualf(t, string(ActionDispatch), a,
				"system-created policy %q grants %q; no system-created policy may grant "+
					"the dispatch action on any resource type.", p.Name, a)
		}
	}
}

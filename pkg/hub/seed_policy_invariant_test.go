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
// policy the system creates on its own carries an action wildcard ("*") or the
// "dispatch" action.
//
// WHY THIS IS A TEST AND NOT A COMMENT. matchesAction (authz.go) grants an
// action iff the policy's Actions contains "*" or the exact action, and
// matchesResource treats ResourceType "*" as matching every resource type. So a
// policy is a dispatch grant on a broker iff Effect=="allow" AND ResourceType in
// {"*","broker"} AND Actions intersects {"*","dispatch"}. hub-member-read-all
// already has ResourceType "*", which matches a broker resource; the only thing
// keeping it from granting dispatch (and everything else) hub-wide is that its
// Actions stay a concrete list. That is one token of drift, and nothing else in
// the code enforces it. This test is the enforcement.
//
// THE UNIVERSE (the filter, so a future reader can tell whether a new policy
// site belongs in scope). "System-created" = every policy the server writes
// without a caller authoring it. There are exactly four such sites:
//   - seed.go:seedDefaultPoliciesAndGroups   -> hub-member-read-all, hub-member-create-projects
//   - handlers_projects_core.go:createProjectMembersGroupAndPolicy -> project:<slug>:member-create-agents
//   - handlers_env_secrets.go:ensureProgenyPolicy -> progeny secret policy
// Deliberately OUT of scope: the caller-authored policy API
// (handlers_policies.go), read-hydration in the store adapter, and test
// fixtures. If you add a new system-created policy site, drive it here too or
// this test is no longer the same sentence as the claim it pins.
//
// The test drives all four sites for real and then scans every resulting policy,
// rather than reading the literals, so a wildcard or dispatch action added at any
// of them fails here.
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

	// The invariant. matchesAction grants on "*" or an exact action match, so the
	// two forbidden tokens are the action wildcard and the dispatch action itself.
	for _, p := range result.Items {
		for _, a := range p.Actions {
			require.NotEqualf(t, "*", a,
				"system-created policy %q carries the action wildcard %q; with its "+
					"ResourceType %q this can grant dispatch (and more). Keep Actions a "+
					"concrete list.", p.Name, a, p.ResourceType)
			require.NotEqualf(t, string(ActionDispatch), a,
				"system-created policy %q grants %q; no system-created policy may grant "+
					"dispatch (PR #591 invariant).", p.Name, a)
		}
	}
}

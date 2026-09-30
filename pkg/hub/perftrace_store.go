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

	"github.com/GoogleCloudPlatform/scion/pkg/store"
)

// perfCountingStore wraps a store.Store and counts calls to the six methods
// ptone/scion#2367's investigation identified as the authorization hot
// path: principal group resolution (GetEffectiveGroups /
// GetEffectiveGroupsForAgent, from AuthzService.authorizationPrincipals),
// role binding / definition loads (ListRoleBindingsForPrincipals,
// GetRoleDefinitionsByIDs), constraint loads (ListAccessConstraints), and
// delegation-ceiling checks (GetDelegationEdgesForDelegate). Each of these
// is a candidate for being called once per agent per list request rather
// than once per request; their per-request call counts are the direct
// evidence for which case it actually is.
//
// Coordination note (ptone/scion#2376/#2377, agent:slow-list-authz-arch):
// this type intentionally does not touch pkg/hub/authz.go, authz_list.go,
// capabilities.go, or authz_delegation_ceiling.go, all separately being
// changed by the request-local authorization-input-reuse work tracked
// there. It wraps store.Store at exactly the NewAuthzService(...) call site
// in server.go's New() -- so AuthzService's own store field (which authz.go
// reads directly) is this counting wrapper, with zero edits to any authz
// file. That means these counts are real store round trips *after* whatever
// memoization #2376/#2377 add, which is what their own parity tests assert
// on. Agreed and recorded in the risk table at
// gs://scion-xproject-exchange/slow-list/design/authz-reuse.md.
//
// Deliberately scoped to only the store AuthzService holds, not srv.store
// (used everywhere else in the Server): this type re-exposes only the plain
// store.Store interface, not any concrete store's extra methods --
// entadapter.CompositeStore.DB() in particular, which
// runMembershipMigration's D4 index installation type-asserts for during
// New(). An earlier version of this wrapped srv.store itself (before
// store: s was assigned in New()) and broke exactly that startup step; see
// perftrace_integration_test.go for a regression test pinning this store
// down to AuthzService alone.
type perfCountingStore struct {
	store.Store
}

// WrapStoreForPerfTrace returns s unchanged when tracing is disabled
// (perfTraceEnabled == false, the default), so the disabled case has zero
// overhead: not even an extra interface indirection, since s itself is
// returned rather than a wrapper around it.
func WrapStoreForPerfTrace(s store.Store) store.Store {
	if !perfTraceEnabled {
		return s
	}
	return perfCountingStore{Store: s}
}

func (c perfCountingStore) GetEffectiveGroups(ctx context.Context, userID string) ([]string, error) {
	if t := PerfTraceFromContext(ctx); t != nil {
		t.IncStoreCall("GetEffectiveGroups")
	}
	return c.Store.GetEffectiveGroups(ctx, userID)
}

func (c perfCountingStore) GetEffectiveGroupsForAgent(ctx context.Context, agentID string) ([]string, error) {
	if t := PerfTraceFromContext(ctx); t != nil {
		t.IncStoreCall("GetEffectiveGroupsForAgent")
	}
	return c.Store.GetEffectiveGroupsForAgent(ctx, agentID)
}

func (c perfCountingStore) ListRoleBindingsForPrincipals(ctx context.Context, principals []store.PrincipalRef, scopeTypes []string, scopeIDs []string) ([]*store.RoleBinding, error) {
	if t := PerfTraceFromContext(ctx); t != nil {
		t.IncStoreCall("ListRoleBindingsForPrincipals")
	}
	return c.Store.ListRoleBindingsForPrincipals(ctx, principals, scopeTypes, scopeIDs)
}

func (c perfCountingStore) GetRoleDefinitionsByIDs(ctx context.Context, ids []string) (map[string]*store.RoleDefinition, error) {
	if t := PerfTraceFromContext(ctx); t != nil {
		t.IncStoreCall("GetRoleDefinitionsByIDs")
	}
	return c.Store.GetRoleDefinitionsByIDs(ctx, ids)
}

func (c perfCountingStore) ListAccessConstraints(ctx context.Context, limit, offset int) ([]*store.AccessConstraint, error) {
	if t := PerfTraceFromContext(ctx); t != nil {
		t.IncStoreCall("ListAccessConstraints")
	}
	return c.Store.ListAccessConstraints(ctx, limit, offset)
}

func (c perfCountingStore) GetDelegationEdgesForDelegate(ctx context.Context, delegateType, delegateID string) ([]*store.DelegationEdge, error) {
	if t := PerfTraceFromContext(ctx); t != nil {
		t.IncStoreCall("GetDelegationEdgesForDelegate")
	}
	return c.Store.GetDelegationEdgesForDelegate(ctx, delegateType, delegateID)
}

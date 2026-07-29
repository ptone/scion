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
	"errors"

	"github.com/GoogleCloudPlatform/scion/pkg/store"
)

// Capabilities represents the set of actions a user can perform on a resource.
type Capabilities struct {
	Actions []string `json:"actions"`
}

// ResourceActions maps resource types to the actions applicable to individual resources.
var ResourceActions = map[string][]Action{
	"agent":               {ActionRead, ActionUpdate, ActionDelete, ActionStart, ActionStop, ActionMessage, ActionAttach},
	"project":             {ActionRead, ActionUpdate, ActionDelete, ActionManage, ActionRegister},
	"skill":               {ActionRead, ActionUpdate, ActionDelete},
	"template":            {ActionRead, ActionUpdate, ActionDelete},
	"harness_config":      {ActionRead, ActionUpdate, ActionDelete},
	"group":               {ActionRead, ActionUpdate, ActionDelete, ActionAddMember, ActionRemoveMember},
	"user":                {ActionRead, ActionUpdate},
	"policy":              {ActionRead, ActionUpdate, ActionDelete},
	"broker":              {ActionRead, ActionUpdate, ActionDelete, ActionDispatch},
	"gcp_service_account": {ActionRead, ActionDelete, ActionVerify},
}

// ScopeActions maps resource types to scope-level actions (e.g., create, list).
var ScopeActions = map[string][]Action{
	"agent":               {ActionCreate, ActionList, ActionStopAll},
	"project":             {ActionCreate, ActionList},
	"skill":               {ActionCreate, ActionList},
	"template":            {ActionCreate, ActionList},
	"harness_config":      {ActionCreate, ActionList},
	"group":               {ActionCreate, ActionList},
	"policy":              {ActionCreate, ActionList},
	"broker":              {ActionCreate, ActionList},
	"gcp_service_account": {ActionCreate, ActionList, ActionMint},
}

// agentResource constructs a Resource from a store.Agent for capability computation.
func agentResource(a *store.Agent) Resource {
	return Resource{
		Type:       "agent",
		ID:         a.ID,
		OwnerID:    a.OwnerID,
		ParentType: "project",
		ParentID:   a.ProjectID,
		Labels:     a.Labels,
		Ancestry:   a.Ancestry,
	}
}

// projectResource constructs a Resource from a store.Project for capability computation.
func projectResource(g *store.Project) Resource {
	return Resource{
		Type:    "project",
		ID:      g.ID,
		OwnerID: g.OwnerID,
		Labels:  g.Labels,
	}
}

// templateResource constructs a Resource from a store.Template for capability computation.
func templateResource(t *store.Template) Resource {
	r := Resource{
		Type:    "template",
		ID:      t.ID,
		OwnerID: t.OwnerID,
	}
	// Project-scoped templates are children of their project (mirrors
	// harnessConfigResource and policyResource). Without this the resource is
	// parentless, and since #595 made project-scoped policy matching an
	// allow-list, a parentless resource matches no project-scoped policy at
	// all — so project-scoped template policies would match nothing.
	//
	// ScopeID is the authoritative field. Deliberately no fallback to the
	// deprecated t.ProjectID (store/models.go): a deprecated field must not
	// become load-bearing in the authz engine. Legacy ProjectID-only rows are
	// handled by backfill, not here.
	//
	// Global- and user-scoped templates stay parentless, which is correct:
	// they do not belong to a project.
	if t.Scope == store.TemplateScopeProject && t.ScopeID != "" {
		r.ParentType = "project"
		r.ParentID = t.ScopeID
	}
	return r
}

// harnessConfigResource constructs a Resource from a store.HarnessConfig for capability computation.
func harnessConfigResource(hc *store.HarnessConfig) Resource {
	if hc == nil {
		return Resource{}
	}
	r := Resource{
		Type:    "harness_config",
		ID:      hc.ID,
		OwnerID: hc.OwnerID,
	}
	// Project-scoped harness configs are children of the project, so project
	// owner/admin bypass applies.
	//
	// NOT A PAIR ON THIS BRANCH. This line previously ended "(mirrors
	// gcpServiceAccountResource)". It does not mirror it here: that function is
	// unconditional in this tree and claims ParentType "project" for every
	// scope, including hub- and user-scoped accounts whose ScopeID is not a
	// project ID at all. svc-accnt P0.2 makes it conditional and restores the
	// pairing. Until that merges the two differ, and reading this line as a
	// statement that gcpServiceAccountResource already scopes its parent is the
	// specific mistake that would cause that conversion to be skipped.
	//
	// This comment is expected to CONFLICT TEXTUALLY with the svc-accnt tree at
	// merge. That is deliberate, and it is the cheaper failure: a comment
	// conflict is loud, harmless, and lands the reviewer's eye on exactly the
	// two functions whose relationship is the issue, whereas a silent
	// divergence here is a fail-open. Do not pre-resolve it by deleting this
	// half — that leaves one surviving assertion of a pair that does not exist,
	// which is what this edit removed. Resolve it by confirming
	// gcpServiceAccountResource is conditional in the merged tree, then restore
	// the short form.
	//
	// AND REWRITE THE DEPENDENT IN THE SAME ACT. A deliberate conflict alarms at
	// the text, not at what reasons from it. The AGENT paragraph above the
	// service-account read gate in createAgentInProject
	// (handlers_agents_core.go) records why that gate cannot deny an agent
	// today, and the clause it records is "ParentType project for every scope,
	// so a hub-scoped account resolves to the hub instance ID: non-empty, and
	// equal to no project". If the merged gcpServiceAccountResource sets no
	// project parent for a hub-scoped account, that clause is false: the
	// resource then yields "" from projectIDForResource and the agent read
	// baseline is skipped by its pid != "" guard (authz.go) instead of by an ID
	// mismatch. Same outcome, different reason. The merge performs that half of
	// the conversion by itself while resolving this hunk, so nobody decides it,
	// behaviour does not change, and nothing else complains — which is why the
	// dependent is named here rather than left to be noticed. Found by sa-arch.
	if hc.Scope == store.HarnessConfigScopeProject && hc.ScopeID != "" {
		r.ParentType = "project"
		r.ParentID = hc.ScopeID
	}
	return r
}

// groupResource constructs a Resource from a store.Group for capability computation.
func groupResource(g *store.Group) Resource {
	r := Resource{
		Type:    "group",
		ID:      g.ID,
		OwnerID: g.OwnerID,
		Labels:  g.Labels,
	}
	// Project-scoped groups (e.g. "project:<slug>:members") are children of the
	// project. Setting the parent lets project owner/admin bypass apply.
	if g.ProjectID != "" {
		r.ParentType = "project"
		r.ParentID = g.ProjectID
	}
	return r
}

// userResource constructs a Resource from a store.User for capability computation.
func userResource(u *store.User) Resource {
	return Resource{
		Type: "user",
		ID:   u.ID,
	}
}

// policyResource constructs a Resource from a store.Policy for capability computation.
func policyResource(p *store.Policy) Resource {
	r := Resource{
		Type:   "policy",
		ID:     p.ID,
		Labels: p.Labels,
	}
	// Project-scoped policies are children of the project for authz purposes.
	if p.ScopeType == "project" && p.ScopeID != "" {
		r.ParentType = "project"
		r.ParentID = p.ScopeID
	}
	return r
}

// brokerResource constructs a Resource from a store.RuntimeBroker for capability computation.
//
// Deliberately parentless, and unlike templates this is not a defect: brokers
// are many-to-many with projects via store.ProjectProvider, which links
// BrokerID to ProjectID. A many-to-many relation cannot be expressed as one
// ParentType/ParentID pair, so there is no single project to name here. Do not
// "fix" this by mirroring templateResource — the correct place to decide
// whether a caller may use a broker is the dispatch check, not the parent.
func brokerResource(b *store.RuntimeBroker) Resource {
	return Resource{
		Type:    "broker",
		ID:      b.ID,
		OwnerID: b.CreatedBy,
	}
}

// gcpServiceAccountResource constructs a Resource from a store.GCPServiceAccount for capability computation.
func gcpServiceAccountResource(sa *store.GCPServiceAccount) Resource {
	return Resource{
		Type:       "gcp_service_account",
		ID:         sa.ID,
		OwnerID:    sa.CreatedBy,
		ParentType: "project",
		ParentID:   sa.ScopeID,
	}
}

// ComputeCapabilities evaluates which actions the identity can perform on a single resource.
func (a *AuthzService) ComputeCapabilities(ctx context.Context, identity Identity, resource Resource) *Capabilities {
	actions, ok := ResourceActions[resource.Type]
	if !ok {
		return &Capabilities{Actions: []string{}}
	}

	// #591 (N73/N79): a project-scoped UAT must not take ANY fast-path
	// short-circuit below. Admin and project-owner are keyed on the MINTING
	// user's role/ID, so they would confer access outside the token's project +
	// scope bounds. Skip them and fall to the per-action loop, which routes
	// through CheckAccess -> enforceUATConstraints. The fast paths remain for
	// every non-scoped identity (a real optimisation). See
	// ComputeCapabilitiesBatch and checkAccessPrecomputed for the batch-path
	// equivalents.
	_, scoped := identity.(*ScopedUserIdentity)

	if !scoped {
		// Admin short-circuit: return all actions
		if user, ok := identity.(UserIdentity); ok && user.Role() == "admin" {
			return allActions(actions)
		}

		// Project owner/admin short-circuit: full access on project and
		// project-scoped resources. Mirrors the bypass in checkAccessForUser so
		// capability lists match what the user can actually do.
		if user, ok := identity.(UserIdentity); ok {
			if projectID := projectIDForResource(resource); projectID != "" {
				if a.isProjectOwnerOrAdmin(ctx, user.ID(), projectID) {
					return allActions(actions)
				}
			}
		}
	}

	var allowed []string
	for _, action := range actions {
		decision := a.CheckAccess(ctx, identity, resource, action)
		if decision.Allowed {
			allowed = append(allowed, string(action))
		}
	}
	if allowed == nil {
		allowed = []string{}
	}
	return &Capabilities{Actions: allowed}
}

// ComputeScopeCapabilities evaluates scope-level actions (e.g., create, list) for a resource type.
func (a *AuthzService) ComputeScopeCapabilities(ctx context.Context, identity Identity, scopeType, scopeID, resourceType string) *Capabilities {
	actions, ok := ScopeActions[resourceType]
	if !ok {
		return &Capabilities{Actions: []string{}}
	}

	// #591 (N73/N79): a scoped UAT skips the fast-path short-circuits and falls
	// to the enforced per-action loop. See ComputeCapabilities.
	_, scoped := identity.(*ScopedUserIdentity)

	// Admin short-circuit
	if !scoped {
		if user, ok := identity.(UserIdentity); ok && user.Role() == "admin" {
			return allActions(actions)
		}
	}

	resource := Resource{
		Type:       resourceType,
		ParentType: scopeType,
		ParentID:   scopeID,
	}

	// Project owner/admin short-circuit at scope level (e.g. agent:create
	// inside a project the user owns).
	if !scoped {
		if user, ok := identity.(UserIdentity); ok && scopeType == "project" && scopeID != "" {
			if a.isProjectOwnerOrAdmin(ctx, user.ID(), scopeID) {
				return allActions(actions)
			}
		}
	}

	var allowed []string
	for _, action := range actions {
		decision := a.CheckAccess(ctx, identity, resource, action)
		if decision.Allowed {
			allowed = append(allowed, string(action))
		}
	}
	if allowed == nil {
		allowed = []string{}
	}
	return &Capabilities{Actions: allowed}
}

// ComputeCapabilitiesBatch evaluates capabilities for a list of resources, optimized
// for batch operation by expanding groups and fetching policies once.
func (a *AuthzService) ComputeCapabilitiesBatch(ctx context.Context, identity Identity, resources []Resource, resourceType string) []*Capabilities {
	actions, ok := ResourceActions[resourceType]
	if !ok {
		caps := make([]*Capabilities, len(resources))
		for i := range caps {
			caps[i] = &Capabilities{Actions: []string{}}
		}
		return caps
	}

	// #591 (N73/N79): two nested gates protect the fast-path short-circuits below.
	// COARSE (N79 change 3): only the user family enters the fast paths; every
	// non-user identity (broker, agent, anything else) skips them so it cannot
	// trip the bare-ID OwnerID/ancestry paths — this closes T1, a self-minted
	// broker whose ID collides with a victim user id that would otherwise list
	// the victim's owned and descendant resources. FINE (N79 change 1): a
	// project-scoped UAT, though it IS a UserIdentity, still skips them and falls
	// to the per-action checkAccessPrecomputed loop, which enforceUATConstraints
	// gates at its top (change 2). The fast paths remain for every non-scoped
	// genuine user (a real optimisation).
	_, isUser := identity.(UserIdentity)
	_, scoped := identity.(*ScopedUserIdentity)

	// Admin short-circuit: return all actions for all resources
	if !scoped {
		if user, ok := identity.(UserIdentity); ok && user.Role() == "admin" {
			allCap := allActions(actions)
			caps := make([]*Capabilities, len(resources))
			for i := range caps {
				caps[i] = allCap
			}
			return caps
		}
	}

	// Pre-fetch principals and policies once for the identity
	principals, policies := a.precomputeForIdentity(ctx, identity)

	// Per-batch project ownership cache. Most batches list resources from a
	// single project, so this collapses to one lookup per project.
	projectOwnerCache := map[string]bool{}
	isProjectOwner := func(projectID string) bool {
		if projectID == "" {
			return false
		}
		user, ok := identity.(UserIdentity)
		if !ok {
			return false
		}
		if cached, ok := projectOwnerCache[projectID]; ok {
			return cached
		}
		v := a.isProjectOwnerOrAdmin(ctx, user.ID(), projectID)
		projectOwnerCache[projectID] = v
		return v
	}

	caps := make([]*Capabilities, len(resources))
	for i, resource := range resources {
		// #591 (N73/N79): the OwnerID and ancestry fast paths below carry NO type
		// assertion and key on bare identity.ID(), so they are double-gated.
		// COARSE (change 3): only the user family enters — a non-user identity
		// (e.g. a self-minted broker whose ID collides with a victim user id)
		// would otherwise trip OwnerID/ancestry and list the victim's owned and
		// descendant resources (T1, a live leak). FINE (change 1): a scoped UAT,
		// though it IS a UserIdentity, still skips them and falls to
		// checkAccessPrecomputed (enforceUATConstraints-gated) below. NOTE the
		// ancestry SLOW path in checkAccessPrecomputed remains bare-ID (T1r,
		// follow-on); the OwnerID axis is fully closed here plus the slow-path
		// owner bypass sits under a UserIdentity assertion.
		if isUser {
			if !scoped {
				// Owner short-circuit
				if resource.OwnerID != "" && resource.OwnerID == identity.ID() {
					caps[i] = allActions(actions)
					continue
				}
				// Ancestry short-circuit: ancestors get full access
				if canAccessAsAncestor(identity.ID(), resource) {
					caps[i] = allActions(actions)
					continue
				}
				// Project owner/admin short-circuit
				if isProjectOwner(projectIDForResource(resource)) {
					caps[i] = allActions(actions)
					continue
				}
			}
		}

		var allowed []string
		for _, action := range actions {
			decision := a.checkAccessPrecomputed(identity, principals, policies, resource, action)
			if decision.Allowed {
				allowed = append(allowed, string(action))
			}
		}
		if allowed == nil {
			allowed = []string{}
		}
		caps[i] = &Capabilities{Actions: allowed}
	}
	return caps
}

// precomputeForIdentity fetches group memberships and policies once for an identity.
func (a *AuthzService) precomputeForIdentity(ctx context.Context, identity Identity) ([]store.PrincipalRef, []store.Policy) {
	var principals []store.PrincipalRef

	switch identity.Type() {
	case "user", "dev":
		principals = append(principals, store.PrincipalRef{Type: "user", ID: identity.ID()})
		groupIDs, err := a.store.GetEffectiveGroups(ctx, identity.ID())
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			a.logger.Warn("failed to get effective groups for user", "userID", identity.ID(), "error", err.Error())
		}
		for _, gid := range groupIDs {
			principals = append(principals, store.PrincipalRef{Type: "group", ID: gid})
		}
	case "agent":
		principals = append(principals, store.PrincipalRef{Type: "agent", ID: identity.ID()})
		groupIDs, err := a.store.GetEffectiveGroupsForAgent(ctx, identity.ID())
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			a.logger.Warn("failed to get effective groups for agent", "agent_id", identity.ID(), "error", err.Error())
		}
		for _, gid := range groupIDs {
			principals = append(principals, store.PrincipalRef{Type: "group", ID: gid})
		}
	}

	policies, err := a.store.GetPoliciesForPrincipals(ctx, principals)
	if err != nil {
		a.logger.Warn("failed to get policies for principals", "error", err)
	}

	return principals, policies
}

// checkAccessPrecomputed evaluates access using pre-fetched principals and policies.
func (a *AuthzService) checkAccessPrecomputed(identity Identity, _ []store.PrincipalRef, policies []store.Policy, resource Resource, action Action) Decision {
	// #591 (N73/N79): this evaluator is the batch/precomputed path and never
	// calls CheckAccess, so it is the ONLY place a scoped UAT's project + scope
	// bounds are enforced on this path. Run enforceUATConstraints ahead of the
	// two ID-keyed grants below; a non-nil decision denies. dev5's carol witness
	// (plain member, no ownership, no ancestry, token scoped to project P) proves
	// this change is MEASURED-necessary: she bypasses admin and all four
	// owner/ancestry paths by construction, reaching an allow only via
	// evaluatePolicies here — so change (1) alone provably cannot bound her.
	// No-op (nil) for every non-scoped identity, so no blast-radius change.
	if scoped, ok := identity.(*ScopedUserIdentity); ok {
		if denied := a.enforceUATConstraints(scoped, resource, action); denied != nil {
			return *denied
		}
	}

	// Owner bypass (already handled in batch caller, but kept for single-resource calls)
	if user, ok := identity.(UserIdentity); ok {
		if resource.OwnerID != "" && resource.OwnerID == user.ID() {
			return Decision{Allowed: true, Reason: "resource owner"}
		}
	}

	// Ancestry bypass (already handled in batch caller, but kept for single-resource calls)
	if canAccessAsAncestor(identity.ID(), resource) {
		return Decision{Allowed: true, Reason: "ancestor access"}
	}

	return a.evaluatePolicies(policies, resource, action)
}

// allActions returns a Capabilities with all provided actions.
func allActions(actions []Action) *Capabilities {
	strs := make([]string, len(actions))
	for i, a := range actions {
		strs[i] = string(a)
	}
	return &Capabilities{Actions: strs}
}

// capabilityAllows returns true when the capability set includes the action.
func capabilityAllows(cap *Capabilities, action Action) bool {
	if cap == nil {
		return false
	}
	needle := string(action)
	for _, allowed := range cap.Actions {
		if allowed == needle {
			return true
		}
	}
	return false
}

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
	"net/http"
	"strconv"
	"strings"

	"github.com/GoogleCloudPlatform/scion/pkg/api"
	"github.com/GoogleCloudPlatform/scion/pkg/store"
)

// ============================================================================
// Policy Endpoints
// ============================================================================

// ListPoliciesResponse is the response for listing policies.
type ListPoliciesResponse struct {
	Policies     []PolicyWithCapabilities `json:"policies"`
	NextCursor   string                   `json:"nextCursor,omitempty"`
	TotalCount   int                      `json:"totalCount"`
	Capabilities *Capabilities            `json:"_capabilities,omitempty"`
}

// CreatePolicyRequest is the request body for creating a policy.
type CreatePolicyRequest struct {
	Name         string                  `json:"name"`
	Description  string                  `json:"description,omitempty"`
	ScopeType    string                  `json:"scopeType"` // "hub", "project", "resource"
	ScopeID      string                  `json:"scopeId,omitempty"`
	ResourceType string                  `json:"resourceType"` // "*" for all
	ResourceID   string                  `json:"resourceId,omitempty"`
	Actions      []string                `json:"actions"`
	Effect       string                  `json:"effect"` // "allow", "deny"
	Conditions   *store.PolicyConditions `json:"conditions,omitempty"`
	Priority     int                     `json:"priority"`
	Labels       map[string]string       `json:"labels,omitempty"`
	Annotations  map[string]string       `json:"annotations,omitempty"`
}

// UpdatePolicyRequest is the request body for updating a policy.
type UpdatePolicyRequest struct {
	Name         string                  `json:"name,omitempty"`
	Description  string                  `json:"description,omitempty"`
	ResourceType string                  `json:"resourceType,omitempty"`
	ResourceID   string                  `json:"resourceId,omitempty"`
	Actions      []string                `json:"actions,omitempty"`
	Effect       string                  `json:"effect,omitempty"`
	Conditions   *store.PolicyConditions `json:"conditions,omitempty"`
	Priority     *int                    `json:"priority,omitempty"`
	Labels       map[string]string       `json:"labels,omitempty"`
	Annotations  map[string]string       `json:"annotations,omitempty"`
}

// ListPolicyBindingsResponse is the response for listing policy bindings.
type ListPolicyBindingsResponse struct {
	Bindings []store.PolicyBinding `json:"bindings"`
}

// AddPolicyBindingRequest is the request body for adding a binding to a policy.
type AddPolicyBindingRequest struct {
	PrincipalType string `json:"principalType"` // "user", "group", or "agent"
	PrincipalID   string `json:"principalId"`
}

// EvaluateRequest is the request body for the policy evaluation endpoint.
type EvaluateRequest struct {
	PrincipalType string `json:"principalType"` // "user" or "agent"
	PrincipalID   string `json:"principalId"`
	ResourceType  string `json:"resourceType"`
	ResourceID    string `json:"resourceId,omitempty"`
	Action        string `json:"action"`
}

// EvaluateResponse is the response from the policy evaluation endpoint.
type EvaluateResponse struct {
	Allowed         bool     `json:"allowed"`
	Reason          string   `json:"reason"`
	MatchedPolicy   string   `json:"matchedPolicy,omitempty"`
	PolicyName      string   `json:"policyName,omitempty"`
	Scope           string   `json:"scope,omitempty"`
	EffectiveGroups []string `json:"effectiveGroups,omitempty"`
}

// handlePolicies handles GET and POST on /api/v1/policies
func (s *Server) handlePolicies(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.listPolicies(w, r)
	case http.MethodPost:
		s.createPolicy(w, r)
	default:
		MethodNotAllowed(w)
	}
}

func (s *Server) listPolicies(w http.ResponseWriter, r *http.Request) {
	// Policy reads are gated to hub admins (ptone/scion#591). The listing
	// previously returned every policy to any authenticated caller (annotated
	// with per-item capabilities but not filtered by them), disclosing the hub's
	// entire authorization posture — which policies exist, their scopes, effects,
	// and principals. Fail closed at the entry.
	//
	// Hub-admin-only is the ratified interim per the EM/lead ruling 2026-07-29 —
	// stricter than the design's scope-relative viewability (permissions-design.md
	// §6.2), which a future change may relax toward. See PR-body behaviour-change
	// #12.
	if _, ok := s.requireAdmin(w, r); !ok {
		return
	}

	ctx := r.Context()
	query := r.URL.Query()

	filter := store.PolicyFilter{
		ScopeType:    query.Get("scopeType"),
		ScopeID:      query.Get("scopeId"),
		ResourceType: query.Get("resourceType"),
		Effect:       query.Get("effect"),
	}

	limit := 50
	if l := query.Get("limit"); l != "" {
		if parsed, err := strconv.Atoi(l); err == nil && parsed > 0 {
			limit = parsed
		}
	}

	result, err := s.store.ListPolicies(ctx, filter, store.ListOptions{
		Limit:  limit,
		Cursor: query.Get("cursor"),
	})
	if err != nil {
		writeErrorFromErr(w, err, "")
		return
	}

	// Compute per-item and scope capabilities
	identity := GetIdentityFromContext(ctx)
	policies := make([]PolicyWithCapabilities, len(result.Items))
	if identity != nil {
		resources := make([]Resource, len(result.Items))
		for i := range result.Items {
			resources[i] = policyResource(&result.Items[i])
		}
		caps := s.authzService.ComputeCapabilitiesBatch(ctx, identity, resources, "policy")
		for i := range result.Items {
			policies[i] = PolicyWithCapabilities{Policy: result.Items[i], Cap: caps[i]}
		}
	} else {
		for i := range result.Items {
			policies[i] = PolicyWithCapabilities{Policy: result.Items[i]}
		}
	}

	var scopeCap *Capabilities
	if identity != nil {
		scopeCap = s.authzService.ComputeScopeCapabilities(ctx, identity, "", "", "policy")
	}

	writeJSON(w, http.StatusOK, ListPoliciesResponse{
		Policies:     policies,
		NextCursor:   result.NextCursor,
		TotalCount:   result.TotalCount,
		Capabilities: scopeCap,
	})
}

func (s *Server) createPolicy(w http.ResponseWriter, r *http.Request) {
	// Policy writes are gated to hub admins (ptone/scion#591). Before this gate
	// the handler performed no authorization, so any authenticated caller could
	// author a policy — including one granting themselves access to a resource
	// and overriding an admin-established deny. Fail closed at the entry, before
	// any parsing.
	//
	// Scope: hub-admin-only is the ratified interim per the EM/lead ruling
	// 2026-07-29 — deliberately stricter than the design's scope-relative
	// `manage` (permissions-design.md §6.2), which a future change may relax
	// toward. See PR-body behaviour-change #12.
	if _, ok := s.requireAdmin(w, r); !ok {
		return
	}

	ctx := r.Context()

	var req CreatePolicyRequest
	if err := readJSON(r, &req); err != nil {
		BadRequest(w, "Invalid request body: "+err.Error())
		return
	}

	// Validate required fields
	if req.Name == "" {
		ValidationError(w, "name is required", nil)
		return
	}
	if req.ScopeType == "" {
		ValidationError(w, "scopeType is required", nil)
		return
	}
	if req.ScopeType != store.PolicyScopeHub && req.ScopeType != store.PolicyScopeProject && req.ScopeType != store.PolicyScopeResource {
		ValidationError(w, "scopeType must be 'hub', 'project', or 'resource'", nil)
		return
	}
	if req.ScopeType != store.PolicyScopeHub && req.ScopeID == "" {
		ValidationError(w, "scopeId is required for project and resource scopes", nil)
		return
	}
	if len(req.Actions) == 0 {
		ValidationError(w, "actions is required", nil)
		return
	}
	if req.Effect == "" {
		ValidationError(w, "effect is required", nil)
		return
	}
	if req.Effect != store.PolicyEffectAllow && req.Effect != store.PolicyEffectDeny {
		ValidationError(w, "effect must be 'allow' or 'deny'", nil)
		return
	}

	resourceType := req.ResourceType
	if resourceType == "" {
		resourceType = "*"
	}

	policy := &store.Policy{
		ID:           api.NewUUID(),
		Name:         req.Name,
		Description:  req.Description,
		ScopeType:    req.ScopeType,
		ScopeID:      req.ScopeID,
		ResourceType: resourceType,
		ResourceID:   req.ResourceID,
		Actions:      req.Actions,
		Effect:       req.Effect,
		Conditions:   req.Conditions,
		Priority:     req.Priority,
		Labels:       req.Labels,
		Annotations:  req.Annotations,
	}

	// Populate CreatedBy from auth context
	if identity := GetIdentityFromContext(ctx); identity != nil {
		policy.CreatedBy = identity.ID()
	}

	if err := s.store.CreatePolicy(ctx, policy); err != nil {
		writeErrorFromErr(w, err, "")
		return
	}

	writeJSON(w, http.StatusCreated, policy)
}

// handlePolicyRoutes handles /api/v1/policies/{policyId}/...
func (s *Server) handlePolicyRoutes(w http.ResponseWriter, r *http.Request) {
	// Extract policy ID and remaining path
	path := strings.TrimPrefix(r.URL.Path, "/api/v1/policies/")
	if path == "" {
		NotFound(w, "Policy")
		return
	}

	// Check for evaluate endpoint before treating as policy ID
	if path == "evaluate" {
		s.handlePolicyEvaluate(w, r)
		return
	}

	// Parse the policy ID
	parts := strings.SplitN(path, "/", 2)
	policyID := parts[0]
	subPath := ""
	if len(parts) > 1 {
		subPath = parts[1]
	}

	// Check for nested /bindings path
	if strings.HasPrefix(subPath, "bindings") {
		bindingPath := strings.TrimPrefix(subPath, "bindings")
		bindingPath = strings.TrimPrefix(bindingPath, "/")
		if bindingPath == "" {
			s.handlePolicyBindings(w, r, policyID)
		} else {
			s.handlePolicyBindingByID(w, r, policyID, bindingPath)
		}
		return
	}

	// Otherwise handle as policy resource
	if subPath != "" {
		NotFound(w, "Policy resource")
		return
	}

	switch r.Method {
	case http.MethodGet:
		s.getPolicy(w, r, policyID)
	case http.MethodPatch:
		s.updatePolicy(w, r, policyID)
	case http.MethodDelete:
		s.deletePolicy(w, r, policyID)
	default:
		MethodNotAllowed(w)
	}
}

func (s *Server) getPolicy(w http.ResponseWriter, r *http.Request, id string) {
	// Policy reads are gated to hub admins (ptone/scion#591). The gate runs
	// before the store lookup, so a non-admin gets 403 (not 404) even for a
	// non-existent ID — deliberate, to keep the read from becoming an existence
	// oracle. Hub-admin-only is the ratified interim per the EM/lead ruling
	// 2026-07-29 (see listPolicies and permissions-design.md §6.2; PR-body
	// behaviour-change #12).
	if _, ok := s.requireAdmin(w, r); !ok {
		return
	}

	ctx := r.Context()

	policy, err := s.store.GetPolicy(ctx, id)
	if err != nil {
		writeErrorFromErr(w, err, "")
		return
	}

	resp := PolicyWithCapabilities{Policy: *policy}
	if identity := GetIdentityFromContext(ctx); identity != nil {
		resp.Cap = s.authzService.ComputeCapabilities(ctx, identity, policyResource(policy))
	}

	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) updatePolicy(w http.ResponseWriter, r *http.Request, id string) {
	// Policy writes are gated to hub admins (ptone/scion#591). Fail closed at
	// the entry, before any parsing. Hub-admin-only is the ratified interim per
	// the EM/lead ruling 2026-07-29 — stricter than the design's scope-relative
	// `manage` (see createPolicy and permissions-design.md §6.2; PR-body
	// behaviour-change #12).
	if _, ok := s.requireAdmin(w, r); !ok {
		return
	}

	ctx := r.Context()

	policy, err := s.store.GetPolicy(ctx, id)
	if err != nil {
		writeErrorFromErr(w, err, "")
		return
	}

	var req UpdatePolicyRequest
	if err := readJSON(r, &req); err != nil {
		BadRequest(w, "Invalid request body: "+err.Error())
		return
	}

	if req.Name != "" {
		policy.Name = req.Name
	}
	if req.Description != "" {
		policy.Description = req.Description
	}
	if req.ResourceType != "" {
		policy.ResourceType = req.ResourceType
	}
	if req.ResourceID != "" {
		policy.ResourceID = req.ResourceID
	}
	if len(req.Actions) > 0 {
		policy.Actions = req.Actions
	}
	if req.Effect != "" {
		if req.Effect != store.PolicyEffectAllow && req.Effect != store.PolicyEffectDeny {
			ValidationError(w, "effect must be 'allow' or 'deny'", nil)
			return
		}
		policy.Effect = req.Effect
	}
	if req.Conditions != nil {
		policy.Conditions = req.Conditions
	}
	if req.Priority != nil {
		policy.Priority = *req.Priority
	}
	if req.Labels != nil {
		policy.Labels = req.Labels
	}
	if req.Annotations != nil {
		policy.Annotations = req.Annotations
	}

	if err := s.store.UpdatePolicy(ctx, policy); err != nil {
		writeErrorFromErr(w, err, "")
		return
	}

	writeJSON(w, http.StatusOK, policy)
}

func (s *Server) deletePolicy(w http.ResponseWriter, r *http.Request, id string) {
	// Policy writes are gated to hub admins (ptone/scion#591). Fail closed at
	// the entry, before any processing — the gate needs no resource fetch.
	//
	// This changes the not-found behavior for a non-admin caller: the gate runs
	// before the store lookup, so a non-admin deleting a policy now gets 403
	// even for a non-existent ID, where the ungated handler returned 404. That
	// is deliberate — deciding on the caller before the fetch keeps delete from
	// becoming an existence oracle (a 404-vs-403 split would leak whether a
	// policy ID exists). Admin callers see the unchanged 204/404.
	//
	// Hub-admin-only is the ratified interim per the EM/lead ruling 2026-07-29 —
	// stricter than the design's scope-relative `manage` (see createPolicy and
	// permissions-design.md §6.2; PR-body behaviour-change #12).
	if _, ok := s.requireAdmin(w, r); !ok {
		return
	}

	ctx := r.Context()

	if err := s.store.DeletePolicy(ctx, id); err != nil {
		writeErrorFromErr(w, err, "")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// handlePolicyBindings handles GET and POST on /api/v1/policies/{policyId}/bindings
func (s *Server) handlePolicyBindings(w http.ResponseWriter, r *http.Request, policyID string) {
	// Gate ahead of the existence check (ptone/scion#591, closes aid-rev1 nit I2).
	// The GetPolicy below runs before the per-handler gates in listPolicyBindings /
	// addPolicyBinding, so without a gate here a non-admin learns whether a policy
	// ID exists (404 vs 403) across the whole /bindings subtree — an existence
	// oracle. requireAdmin at the dispatcher entry closes it for both the GET
	// (list) and POST (add) paths. The per-handler gates remain as defence in
	// depth; this dispatcher gate is the one that shuts the oracle. Admin-only per
	// the EM/lead ruling 2026-07-29; ships as part of the one #591 read-hardening
	// commit (PR-body behaviour-change #12).
	if _, ok := s.requireAdmin(w, r); !ok {
		return
	}

	ctx := r.Context()

	// Verify policy exists
	_, err := s.store.GetPolicy(ctx, policyID)
	if err != nil {
		writeErrorFromErr(w, err, "")
		return
	}

	switch r.Method {
	case http.MethodGet:
		s.listPolicyBindings(w, r, policyID)
	case http.MethodPost:
		s.addPolicyBinding(w, r, policyID)
	default:
		MethodNotAllowed(w)
	}
}

func (s *Server) listPolicyBindings(w http.ResponseWriter, r *http.Request, policyID string) {
	// Policy reads are gated to hub admins (ptone/scion#591): a policy's binding
	// list names the principals it grants or denies, so it discloses the same
	// authorization posture as the policy itself. Fail closed at the entry.
	// Hub-admin-only is the ratified interim per the EM/lead ruling 2026-07-29
	// (see listPolicies and permissions-design.md §6.2; PR-body behaviour-change
	// #12).
	//
	// This per-handler gate is defence in depth: the dispatcher
	// handlePolicyBindings already runs requireAdmin ahead of its GetPolicy
	// existence check (closing the I2 existence oracle), so a non-admin never
	// reaches here. Kept so the read gate is self-evident at the handler and
	// survives any future change to the dispatch path.
	if _, ok := s.requireAdmin(w, r); !ok {
		return
	}

	ctx := r.Context()

	bindings, err := s.store.GetPolicyBindings(ctx, policyID)
	if err != nil {
		writeErrorFromErr(w, err, "")
		return
	}

	writeJSON(w, http.StatusOK, ListPolicyBindingsResponse{
		Bindings: bindings,
	})
}

func (s *Server) addPolicyBinding(w http.ResponseWriter, r *http.Request, policyID string) {
	// Policy writes are gated to hub admins (ptone/scion#591). Binding a policy
	// to a principal is the grant-conferring step; before this gate any
	// authenticated caller could bind a self-authored policy to themselves. Fail
	// closed at the entry, before any parsing.
	//
	// Caveat for a future fixer: this handler's own gate is fail-closed, but its
	// dispatcher handlePolicyBindings calls store.GetPolicy before dispatching
	// here (and handlePolicyBindingByID does the same for unbind), so a caller
	// learns whether a policy ID exists — a 404-vs-gate split — ahead of the
	// admin check. Harmless while policy reads are open to all; it becomes a live
	// existence oracle the moment policy reads are gated, at which point the
	// GetPolicy calls in those two dispatchers must move behind the admin check.
	//
	// Hub-admin-only is the ratified interim per the EM/lead ruling 2026-07-29 —
	// stricter than the design's scope-relative `manage` (see createPolicy and
	// permissions-design.md §6.2; PR-body behaviour-change #12).
	if _, ok := s.requireAdmin(w, r); !ok {
		return
	}

	ctx := r.Context()

	var req AddPolicyBindingRequest
	if err := readJSON(r, &req); err != nil {
		BadRequest(w, "Invalid request body: "+err.Error())
		return
	}

	if req.PrincipalType == "" {
		ValidationError(w, "principalType is required", nil)
		return
	}
	if req.PrincipalType != store.PolicyPrincipalTypeUser && req.PrincipalType != store.PolicyPrincipalTypeGroup && req.PrincipalType != store.PolicyPrincipalTypeAgent {
		ValidationError(w, "principalType must be 'user', 'group', or 'agent'", nil)
		return
	}
	if req.PrincipalID == "" {
		ValidationError(w, "principalId is required", nil)
		return
	}

	binding := &store.PolicyBinding{
		PolicyID:      policyID,
		PrincipalType: req.PrincipalType,
		PrincipalID:   req.PrincipalID,
	}

	if err := s.store.AddPolicyBinding(ctx, binding); err != nil {
		if err == store.ErrAlreadyExists {
			Conflict(w, "Binding already exists for this policy")
			return
		}
		writeErrorFromErr(w, err, "")
		return
	}

	writeJSON(w, http.StatusCreated, binding)
}

// handlePolicyBindingByID handles DELETE on /api/v1/policies/{policyId}/bindings/{type}/{id}
func (s *Server) handlePolicyBindingByID(w http.ResponseWriter, r *http.Request, policyID, bindingPath string) {
	// Gate at the dispatcher entry (ptone/scion#591, closes aid-rev1 nit I2). As
	// in handlePolicyBindings, the GetPolicy below would otherwise leak
	// policy-existence (404 vs 403) to a non-admin before removePolicyBinding's own
	// gate runs. requireAdmin here shuts that oracle for the unbind path; the
	// per-handler removePolicyBinding gate stays as defence in depth. Admin-only
	// per the EM/lead ruling 2026-07-29; part of the one #591 read-hardening commit.
	if _, ok := s.requireAdmin(w, r); !ok {
		return
	}

	ctx := r.Context()

	// Parse bindingPath as "type/id"
	parts := strings.SplitN(bindingPath, "/", 2)
	if len(parts) != 2 {
		NotFound(w, "Binding")
		return
	}
	principalType := parts[0]
	principalID := parts[1]

	if principalType != store.PolicyPrincipalTypeUser && principalType != store.PolicyPrincipalTypeGroup && principalType != store.PolicyPrincipalTypeAgent {
		NotFound(w, "Binding")
		return
	}

	// Verify policy exists
	_, err := s.store.GetPolicy(ctx, policyID)
	if err != nil {
		writeErrorFromErr(w, err, "")
		return
	}

	switch r.Method {
	case http.MethodDelete:
		s.removePolicyBinding(w, r, policyID, principalType, principalID)
	default:
		MethodNotAllowed(w)
	}
}

func (s *Server) removePolicyBinding(w http.ResponseWriter, r *http.Request, policyID, principalType, principalID string) {
	// Policy writes are gated to hub admins (ptone/scion#591). A policy reaches a
	// principal only through a binding, so unbinding is equivalent to overriding
	// the policy it carried. The route takes an arbitrary (policyID, principal),
	// so left ungated it is abusable two ways:
	//   - deny-escape: a caller sheds an admin's deny by removing its binding —
	//     the arm-C escape from the other side (detach == override); and
	//   - cross-principal allow-stripping: a caller unbinds a policy from any
	//     OTHER principal, revoking their granted access — a denial-of-service on
	//     the hub's authorization config, not merely a self-serving escalation.
	// This gate closes both and completes the caller-authored policy-API write-op
	// gate begun in 536d8f5c (the four authoring/binding ops).
	//
	// Hub-admin-only is the ratified interim per the EM/lead ruling 2026-07-29 —
	// stricter than the design's scope-relative `manage` (see createPolicy and
	// permissions-design.md §6.2; PR-body behaviour-change #12).
	if _, ok := s.requireAdmin(w, r); !ok {
		return
	}

	ctx := r.Context()

	if err := s.store.RemovePolicyBinding(ctx, policyID, principalType, principalID); err != nil {
		writeErrorFromErr(w, err, "")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// handlePolicyEvaluate handles POST /api/v1/policies/evaluate
func (s *Server) handlePolicyEvaluate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		MethodNotAllowed(w)
		return
	}

	ctx := r.Context()

	// Require authenticated user (admin or the evaluated principal)
	callerIdentity := GetIdentityFromContext(ctx)
	if callerIdentity == nil {
		Unauthorized(w)
		return
	}

	var req EvaluateRequest
	if err := readJSON(r, &req); err != nil {
		BadRequest(w, "Invalid request body: "+err.Error())
		return
	}

	if req.PrincipalType == "" || req.PrincipalID == "" {
		ValidationError(w, "principalType and principalId are required", nil)
		return
	}
	if req.ResourceType == "" {
		ValidationError(w, "resourceType is required", nil)
		return
	}
	if req.Action == "" {
		ValidationError(w, "action is required", nil)
		return
	}

	// Authorization: only admins or the evaluated principal can call this
	if callerUser, ok := callerIdentity.(UserIdentity); ok {
		if callerUser.Role() != "admin" && callerIdentity.ID() != req.PrincipalID {
			Forbidden(w)
			return
		}
	} else if callerIdentity.ID() != req.PrincipalID {
		Forbidden(w)
		return
	}

	// Build the resource
	resource := Resource{
		Type: req.ResourceType,
		ID:   req.ResourceID,
	}

	// If a resourceID is provided, look up the resource for owner/parent info
	if req.ResourceID != "" {
		populateResourceContext(ctx, s, &resource, req.ResourceType, req.ResourceID)
	}

	// Build the identity from the request
	var evalIdentity Identity
	var effectiveGroups []string

	switch req.PrincipalType {
	case "user":
		user, err := s.store.GetUser(ctx, req.PrincipalID)
		if err != nil {
			NotFound(w, "User")
			return
		}
		evalIdentity = NewAuthenticatedUser(user.ID, user.Email, user.DisplayName, user.Role, "api")
		groupIDs, _ := s.store.GetEffectiveGroups(ctx, user.ID)
		effectiveGroups = groupIDs
	case "agent":
		agent, err := s.store.GetAgent(ctx, req.PrincipalID)
		if err != nil {
			NotFound(w, "Agent")
			return
		}
		evalIdentity = &evaluateAgentIdentity{
			id:        agent.ID,
			projectID: agent.ProjectID,
		}
		groupIDs, _ := s.store.GetEffectiveGroupsForAgent(ctx, agent.ID)
		effectiveGroups = groupIDs
	default:
		ValidationError(w, "principalType must be 'user' or 'agent'", nil)
		return
	}

	decision := s.authzService.CheckAccess(ctx, evalIdentity, resource, Action(req.Action))

	writeJSON(w, http.StatusOK, EvaluateResponse{
		Allowed:         decision.Allowed,
		Reason:          decision.Reason,
		MatchedPolicy:   decision.PolicyID,
		PolicyName:      decision.PolicyName,
		Scope:           decision.Scope,
		EffectiveGroups: effectiveGroups,
	})
}

// evaluateAgentIdentity is a minimal AgentIdentity for evaluation purposes.
type evaluateAgentIdentity struct {
	id        string
	projectID string
}

func (e *evaluateAgentIdentity) ID() string                    { return e.id }
func (e *evaluateAgentIdentity) Type() string                  { return "agent" }
func (e *evaluateAgentIdentity) ProjectID() string             { return e.projectID }
func (e *evaluateAgentIdentity) Scopes() []AgentTokenScope     { return nil }
func (e *evaluateAgentIdentity) HasScope(AgentTokenScope) bool { return true }
func (e *evaluateAgentIdentity) Ancestry() []string            { return nil }
func (e *evaluateAgentIdentity) OriginUserID() string          { return "" }

// populateResourceContext fills in owner/parent info from the store.
func populateResourceContext(ctx context.Context, s *Server, resource *Resource, resourceType, resourceID string) {
	switch resourceType {
	case "agent":
		agent, err := s.store.GetAgent(ctx, resourceID)
		if err == nil {
			resource.OwnerID = agent.OwnerID
			resource.ParentType = "project"
			resource.ParentID = agent.ProjectID
		}
	case "project":
		project, err := s.store.GetProject(ctx, resourceID)
		if err == nil {
			resource.OwnerID = project.OwnerID
		}
	}
}

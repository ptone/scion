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
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/GoogleCloudPlatform/scion/pkg/hub/permissions"
	"github.com/GoogleCloudPlatform/scion/pkg/store"
)

// ---------------------------------------------------------------------------
// Request / Response types
// ---------------------------------------------------------------------------

// createRoleDefinitionRequest is the payload for POST /api/v1/admin/roles.
type createRoleDefinitionRequest struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	ScopeType   string   `json:"scopeType"`
	Permissions []string `json:"permissions"`
}

// updateRoleDefinitionRequest is the payload for PUT /api/v1/admin/roles/:id.
type updateRoleDefinitionRequest struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Permissions []string `json:"permissions"`
}

// createRoleBindingRequest is the payload for POST /api/v1/admin/role-bindings.
type createRoleBindingRequest struct {
	RoleDefinitionID string     `json:"roleDefinitionId"`
	PrincipalType    string     `json:"principalType"`
	PrincipalID      string     `json:"principalId"`
	ScopeType        string     `json:"scopeType"`
	ScopeID          string     `json:"scopeId"`
	NotBefore        *time.Time `json:"notBefore,omitempty"`
	ExpiresAt        *time.Time `json:"expiresAt,omitempty"`
}

// listRoleDefinitionsResponse wraps the list result for the API.
type listRoleDefinitionsResponse struct {
	Items      []*store.RoleDefinition `json:"items"`
	TotalCount int                     `json:"totalCount"`
}

// RoleBindingInfo is a role binding enriched with human-friendly display info.
type RoleBindingInfo struct {
	store.RoleBinding
	RoleName             string `json:"roleName,omitempty"`
	PrincipalDisplayName string `json:"principalDisplayName,omitempty"`
	ScopeDisplayName     string `json:"scopeDisplayName,omitempty"`
	CreatedByDisplayName string `json:"createdByDisplayName,omitempty"`
	Source               string `json:"source"` // "direct" for non-group-derived; "group" for group-derived bindings
	SourceGroupID        string `json:"sourceGroupId,omitempty"`
	SourceGroupName      string `json:"sourceGroupName,omitempty"`
	SourceGroupSlug      string `json:"sourceGroupSlug,omitempty"`
}

// listRoleBindingsResponse wraps the list result for the API.
type listRoleBindingsResponse struct {
	Items      []RoleBindingInfo `json:"items"`
	TotalCount int               `json:"totalCount"`
}

// listPermissionsResponse wraps the permissions registry for the API.
type listPermissionsResponse struct {
	Items      []permissions.Permission `json:"items"`
	TotalCount int                      `json:"totalCount"`
}

// ---------------------------------------------------------------------------
// Route handlers: Role Definitions
// ---------------------------------------------------------------------------

// handleAdminRoles handles GET (list) and POST (create) on
// /api/v1/admin/roles.
// Authorization: route guard checks role.read for GET.
// POST requires role.create via inline Decide.
func (s *Server) handleAdminRoles(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.listRoleDefinitions(w, r)
	case http.MethodPost:
		user, ok := s.requireWritePermissionForRole(w, r, "role.create", "create")
		if !ok {
			return
		}
		s.createRoleDefinition(w, r, user)
	default:
		MethodNotAllowed(w)
	}
}

// handleAdminRoleByID handles GET / PUT / DELETE on
// /api/v1/admin/roles/:id.
func (s *Server) handleAdminRoleByID(w http.ResponseWriter, r *http.Request) {
	id := extractID(r, "/api/v1/admin/roles")
	if id == "" {
		BadRequest(w, "role definition ID is required")
		return
	}

	switch r.Method {
	case http.MethodGet:
		s.getRoleDefinition(w, r, id)
	case http.MethodPut:
		user, ok := s.requireWritePermissionForRole(w, r, "role.update", "update")
		if !ok {
			return
		}
		s.updateRoleDefinition(w, r, id, user)
	case http.MethodDelete:
		user, ok := s.requireWritePermissionForRole(w, r, "role.delete", "delete")
		if !ok {
			return
		}
		s.deleteRoleDefinition(w, r, id, user)
	default:
		MethodNotAllowed(w)
	}
}

// ---------------------------------------------------------------------------
// Route handlers: Role Bindings
// ---------------------------------------------------------------------------

// handleAdminRoleBindings handles GET (list) and POST (create) on
// /api/v1/admin/role-bindings.
// Authorization: route guard checks role_binding.read for GET.
// POST requires role_binding.create via inline Decide.
func (s *Server) handleAdminRoleBindings(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.listRoleBindings(w, r)
	case http.MethodPost:
		user, ok := s.requireWritePermissionForRoleBinding(w, r, "role_binding.create", "create")
		if !ok {
			return
		}
		s.createRoleBinding(w, r, user)
	default:
		MethodNotAllowed(w)
	}
}

// handleAdminRoleBindingByID handles DELETE on /api/v1/admin/role-bindings/:id
// and GET on /api/v1/admin/role-bindings/user/:userID.
func (s *Server) handleAdminRoleBindingByID(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/v1/admin/role-bindings/")
	path = strings.TrimSuffix(path, "/")

	// Handle user-specific binding lookup.
	if strings.HasPrefix(path, "user/") {
		userID := strings.TrimPrefix(path, "user/")
		if userID == "" {
			BadRequest(w, "user ID is required")
			return
		}
		if r.Method != http.MethodGet {
			MethodNotAllowed(w)
			return
		}
		s.listBindingsForUser(w, r, userID)
		return
	}

	// Otherwise it's a binding ID.
	id := path
	if id == "" {
		BadRequest(w, "role binding ID is required")
		return
	}

	switch r.Method {
	case http.MethodDelete:
		user, ok := s.requireWritePermissionForRoleBinding(w, r, "role_binding.delete", "delete")
		if !ok {
			return
		}
		// The caller passed the system-scope role_binding.delete check above.
		// This authorizes system-level governance bypass (e.g., skipping
		// project-level governance while still enforcing structural invariants
		// like last-owner protection).
		s.deleteRoleBinding(w, r, id, user, true /* systemAuthorized */)
	default:
		MethodNotAllowed(w)
	}
}

// ---------------------------------------------------------------------------
// Route handler: Permissions Registry
// ---------------------------------------------------------------------------

// handleAdminPermissions handles GET on /api/v1/admin/permissions.
func (s *Server) handleAdminPermissions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		MethodNotAllowed(w)
		return
	}
	s.listPermissions(w, r)
}

// ---------------------------------------------------------------------------
// CRUD: Role Definitions
// ---------------------------------------------------------------------------

func (s *Server) listRoleDefinitions(w http.ResponseWriter, r *http.Request) {
	defs, err := s.store.ListRoleDefinitions(r.Context())
	if err != nil {
		writeErrorFromErr(w, err, "")
		return
	}
	if defs == nil {
		defs = []*store.RoleDefinition{}
	}
	// Enrich role definitions with applicability metadata from seed data.
	enrichRoleDefinitionsApplicability(defs)
	writeJSON(w, http.StatusOK, listRoleDefinitionsResponse{
		Items:      defs,
		TotalCount: len(defs),
	})
}

// builtInApplicabilityMap builds a lookup from role name to applicable principal
// types from the authoritative seed data.
var builtInApplicabilityMap = func() map[string][]string {
	m := make(map[string][]string)
	for _, role := range BuiltInRoles() {
		if len(role.ApplicableTo) > 0 {
			m[role.Name] = role.ApplicableTo
		}
	}
	return m
}()

// allPrincipalTypes is the exhaustive set of principal types for custom roles
// that have no explicit applicability restriction. Using an explicit list
// avoids nil/empty ambiguity for frontends that fail-closed on missing values.
var allPrincipalTypes = []string{
	store.RoleBindingPrincipalUser,
	store.RoleBindingPrincipalAgent,
	store.RoleBindingPrincipalGroup,
}

// enrichRoleDefinitionsApplicability sets the ApplicableTo field on role
// definitions from the authoritative seed data. Custom roles get the full
// principal type set (no nil ambiguity).
func enrichRoleDefinitionsApplicability(defs []*store.RoleDefinition) {
	for _, def := range defs {
		if applicableTo, ok := builtInApplicabilityMap[def.Name]; ok {
			def.ApplicableTo = applicableTo
		} else if len(def.ApplicableTo) == 0 {
			def.ApplicableTo = allPrincipalTypes
		}
	}
}

func (s *Server) getRoleDefinition(w http.ResponseWriter, r *http.Request, id string) {
	def, err := s.store.GetRoleDefinition(r.Context(), id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			NotFound(w, "Role Definition")
			return
		}
		writeErrorFromErr(w, err, "")
		return
	}
	// D6: Enrich single role definition with applicability metadata.
	// Custom roles get all principal types to avoid nil ambiguity.
	if applicableTo, ok := builtInApplicabilityMap[def.Name]; ok {
		def.ApplicableTo = applicableTo
	} else if len(def.ApplicableTo) == 0 {
		def.ApplicableTo = allPrincipalTypes
	}
	writeJSON(w, http.StatusOK, def)
}

func (s *Server) createRoleDefinition(w http.ResponseWriter, r *http.Request, user UserIdentity) {
	var req createRoleDefinitionRequest
	if err := readJSON(r, &req); err != nil {
		BadRequest(w, "invalid request body: "+err.Error())
		return
	}

	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" {
		BadRequest(w, "name is required")
		return
	}

	if req.ScopeType != store.RoleScopeSystem && req.ScopeType != store.RoleScopeProject {
		BadRequest(w, "scopeType must be \"system\" or \"project\"")
		return
	}

	// Validate permissions against registry.
	if err := validatePermissionIDs(req.Permissions); err != nil {
		BadRequest(w, err.Error())
		return
	}

	// CanDelegate check: actor must hold all permissions in the custom role.
	if s.authzService != nil {
		decision := s.authzService.CanDelegate(r.Context(), user, GrantDescriptor{
			Type:                  GrantTypeCustomRole,
			CustomRolePermissions: req.Permissions,
			ScopeType:             req.ScopeType,
		})
		if !decision.Allowed {
			writeForbidden(w, "cannot create role: "+decision.Reason)
			return
		}
	}

	rd := &store.RoleDefinition{
		Name:        req.Name,
		Description: req.Description,
		ScopeType:   req.ScopeType,
		Permissions: req.Permissions,
		System:      false, // Only seed creates system roles.
	}

	created, err := s.store.CreateRoleDefinition(r.Context(), rd)
	if err != nil {
		if errors.Is(err, store.ErrAlreadyExists) {
			Conflict(w, "a role definition with this name and scope already exists")
			return
		}
		writeErrorFromErr(w, err, "")
		return
	}

	slog.Info("role definition created",
		"role_id", created.ID, "name", created.Name, "actor", user.Email())

	writeJSON(w, http.StatusCreated, created)
}

func (s *Server) updateRoleDefinition(w http.ResponseWriter, r *http.Request, id string, user UserIdentity) {
	var req updateRoleDefinitionRequest
	if err := readJSON(r, &req); err != nil {
		BadRequest(w, "invalid request body: "+err.Error())
		return
	}

	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" {
		BadRequest(w, "name is required")
		return
	}

	// Fetch existing to check system flag.
	existing, err := s.store.GetRoleDefinition(r.Context(), id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			NotFound(w, "Role Definition")
			return
		}
		writeErrorFromErr(w, err, "")
		return
	}

	if existing.System {
		writeForbidden(w, "system roles cannot be modified")
		return
	}

	// Validate permissions against registry.
	if err := validatePermissionIDs(req.Permissions); err != nil {
		BadRequest(w, err.Error())
		return
	}

	// CanDelegate check: actor must hold all permissions in the updated role.
	if s.authzService != nil {
		decision := s.authzService.CanDelegate(r.Context(), user, GrantDescriptor{
			Type:                  GrantTypeCustomRole,
			CustomRolePermissions: req.Permissions,
			ScopeType:             existing.ScopeType,
		})
		if !decision.Allowed {
			writeForbidden(w, "cannot update role: "+decision.Reason)
			return
		}
	}

	existing.Name = req.Name
	existing.Description = req.Description
	existing.Permissions = req.Permissions

	updated, err := s.store.UpdateRoleDefinition(r.Context(), existing)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			NotFound(w, "Role Definition")
			return
		}
		writeErrorFromErr(w, err, "")
		return
	}

	slog.Info("role definition updated",
		"role_id", updated.ID, "name", updated.Name, "actor", user.Email())

	writeJSON(w, http.StatusOK, updated)
}

func (s *Server) deleteRoleDefinition(w http.ResponseWriter, r *http.Request, id string, user UserIdentity) {
	// Fetch first to check system flag and include name in logs.
	def, err := s.store.GetRoleDefinition(r.Context(), id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			NotFound(w, "Role Definition")
			return
		}
		writeErrorFromErr(w, err, "")
		return
	}

	if def.System {
		writeForbidden(w, "system roles cannot be deleted")
		return
	}

	if err := s.store.DeleteRoleDefinition(r.Context(), id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			NotFound(w, "Role Definition")
			return
		}
		if errors.Is(err, store.ErrInvalidInput) {
			Conflict(w, err.Error())
			return
		}
		writeErrorFromErr(w, err, "")
		return
	}

	slog.Info("role definition deleted",
		"role_id", def.ID, "name", def.Name, "actor", user.Email())

	w.WriteHeader(http.StatusNoContent)
}

// ---------------------------------------------------------------------------
// CRUD: Role Bindings
// ---------------------------------------------------------------------------

// knownBindingFilterParams is the set of recognised query parameter names for
// role-binding filtering. Any query parameter not in this set and not a
// pagination param triggers a fail-closed 400 response.
var knownBindingFilterParams = map[string]bool{
	"roleDefinitionId":    true,
	"principalType":       true,
	"principalId":         true,
	"scopeType":           true,
	"scopeId":             true,
	"includeGroupDerived": true, // R-3: accepted for frontend contract; expansion below
	"limit":               true,
	"offset":              true,
}

func (s *Server) listRoleBindings(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// Fail-closed: reject any unknown query parameter so clients cannot
	// silently ignore a typo and receive unfiltered global data.
	for param := range r.URL.Query() {
		if !knownBindingFilterParams[param] {
			BadRequest(w, fmt.Sprintf("unknown query parameter: %s", param))
			return
		}
	}

	limit, offset := parsePaginationParams(r)

	// Parse filter parameters.
	filter := store.RoleBindingFilter{
		RoleDefinitionID: r.URL.Query().Get("roleDefinitionId"),
		PrincipalType:    r.URL.Query().Get("principalType"),
		PrincipalID:      r.URL.Query().Get("principalId"),
		ScopeType:        r.URL.Query().Get("scopeType"),
		ScopeID:          r.URL.Query().Get("scopeId"),
	}

	// R-2: Validate roleDefinitionId as a valid UUID — never silently drop predicate.
	if filter.RoleDefinitionID != "" {
		if _, err := uuid.Parse(filter.RoleDefinitionID); err != nil {
			BadRequest(w, "invalid roleDefinitionId: not a valid UUID")
			return
		}
	}

	// Validate filter combinations.
	if filter.PrincipalID != "" && filter.PrincipalType == "" {
		BadRequest(w, "principalType is required when principalId is specified")
		return
	}
	if filter.ScopeID != "" && filter.ScopeType == "" {
		BadRequest(w, "scopeType is required when scopeId is specified")
		return
	}
	// Note: scopeType without scopeId is intentionally permitted. For example,
	// scopeType=project without scopeId returns all project-scoped bindings
	// hub-wide — valid for admin audit/overview purposes.
	if filter.PrincipalType != "" {
		if filter.PrincipalType != store.RoleBindingPrincipalUser &&
			filter.PrincipalType != store.RoleBindingPrincipalAgent &&
			filter.PrincipalType != store.RoleBindingPrincipalGroup {
			BadRequest(w, fmt.Sprintf("invalid principalType: %s", filter.PrincipalType))
			return
		}
	}
	if filter.ScopeType != "" {
		if filter.ScopeType != store.RoleScopeSystem && filter.ScopeType != store.RoleScopeProject {
			BadRequest(w, fmt.Sprintf("invalid scopeType: %s", filter.ScopeType))
			return
		}
	}

	bindings, err := s.store.ListRoleBindingsFiltered(ctx, filter, limit, offset)
	if err != nil {
		writeErrorFromErr(w, err, "")
		return
	}
	if bindings == nil {
		bindings = []*store.RoleBinding{}
	}

	total, err := s.store.CountRoleBindingsFiltered(ctx, filter)
	if err != nil {
		writeErrorFromErr(w, err, "")
		return
	}

	// includeGroupDerived: when truthy, expand group-derived bindings.
	// Two modes:
	// 1. principalType=user + principalId=X: find the user's groups and
	//    return their bindings (per-user expansion).
	// 2. scopeType=project + scopeId=X (no principalType/principalId):
	//    find group-principal bindings for the scope and expand group
	//    members into effective user rows (project-wide expansion).
	includeGroupDerived := r.URL.Query().Get("includeGroupDerived") == "true"
	var groupDerivedBindings []*store.RoleBinding
	groupSourceMap := map[string]groupSourceInfo{} // binding ID → group info
	if includeGroupDerived {
		if filter.PrincipalType == store.RoleBindingPrincipalUser && filter.PrincipalID != "" {
			// Per-user expansion.
			groupDerivedBindings, groupSourceMap = s.expandGroupDerivedBindings(ctx, filter)
		} else if filter.ScopeType != "" && filter.PrincipalType == "" {
			// Project-wide (or scope-wide) expansion.
			groupDerivedBindings, groupSourceMap = s.expandScopeGroupDerivedBindings(ctx, filter)
		}
	}

	// O-3: Build role-name cache using batch lookup to avoid N+1 queries.
	allBindings := append(bindings, groupDerivedBindings...)
	roleNameCache, cacheErr := s.buildRoleNameCache(ctx, allBindings)
	if cacheErr != nil {
		writeError(w, http.StatusInternalServerError, "enrichment_error",
			"unable to resolve role names", nil)
		return
	}

	// Enrich bindings with human-friendly display names, role name, and source.
	enriched := make([]RoleBindingInfo, 0, len(allBindings))
	for i, b := range allBindings {
		if b == nil {
			slog.Warn("nil role binding in list result, skipping", "index", i)
			continue
		}
		info := RoleBindingInfo{RoleBinding: *b}
		info.RoleName = roleNameCache[b.RoleDefinitionID]
		if gInfo, ok := groupSourceMap[b.ID]; ok {
			info.Source = "group"
			info.SourceGroupID = gInfo.ID
			info.SourceGroupName = gInfo.Name
			info.SourceGroupSlug = gInfo.Slug
		} else {
			info.Source = "direct"
		}
		info.PrincipalDisplayName = s.resolveGroupMemberDisplayName(ctx, b.PrincipalType, b.PrincipalID)
		info.CreatedByDisplayName = s.resolveGroupMemberDisplayName(ctx, store.GroupMemberTypeUser, b.CreatedBy)
		if b.ScopeType == store.RoleScopeProject && b.ScopeID != "" {
			project, err := s.store.GetProject(ctx, b.ScopeID)
			if err == nil && project != nil {
				info.ScopeDisplayName = project.Name
			}
		}
		enriched = append(enriched, info)
	}

	writeJSON(w, http.StatusOK, listRoleBindingsResponse{
		Items:      enriched,
		TotalCount: total + len(groupDerivedBindings),
	})
}

// groupSourceInfo holds metadata about the source group for group-derived bindings.
type groupSourceInfo struct {
	ID   string
	Name string
	Slug string
}

// resolveGroupInfo fetches a group and returns a groupSourceInfo.
// Falls back to ID-only info on lookup failure.
func (s *Server) resolveGroupInfo(ctx context.Context, groupID string) groupSourceInfo {
	group, err := s.store.GetGroup(ctx, groupID)
	if err != nil {
		slog.Warn("includeGroupDerived: failed to look up group",
			"group_id", groupID, "error", err)
		return groupSourceInfo{ID: groupID}
	}
	return groupSourceInfo{
		ID:   group.ID,
		Name: group.Name,
		Slug: group.Slug,
	}
}

// expandGroupDerivedBindings performs per-user expansion: looks up the groups
// a user belongs to and returns their bindings, along with a map from binding
// ID to the source group info. The filter's remaining predicates
// (roleDefinitionId, scopeType, scopeId) are preserved so group-derived
// bindings are filtered consistently with direct bindings.
func (s *Server) expandGroupDerivedBindings(ctx context.Context, filter store.RoleBindingFilter) ([]*store.RoleBinding, map[string]groupSourceInfo) {
	userID := filter.PrincipalID
	groups, err := s.store.GetUserGroups(ctx, userID)
	if err != nil {
		slog.Warn("includeGroupDerived: failed to look up user groups",
			"user_id", userID, "error", err)
		return nil, nil
	}
	if len(groups) == 0 {
		return nil, nil
	}

	// Resolve group metadata.
	groupInfoCache := make(map[string]groupSourceInfo, len(groups))
	for _, gm := range groups {
		if _, ok := groupInfoCache[gm.GroupID]; !ok {
			groupInfoCache[gm.GroupID] = s.resolveGroupInfo(ctx, gm.GroupID)
		}
	}

	// For each group, query bindings with the same filter constraints but
	// targeting the group as principal.
	var result []*store.RoleBinding
	sourceMap := make(map[string]groupSourceInfo)
	for _, gm := range groups {
		gFilter := store.RoleBindingFilter{
			RoleDefinitionID: filter.RoleDefinitionID,
			PrincipalType:    store.RoleBindingPrincipalGroup,
			PrincipalID:      gm.GroupID,
			ScopeType:        filter.ScopeType,
			ScopeID:          filter.ScopeID,
		}
		gBindings, gErr := s.store.ListRoleBindingsFiltered(ctx, gFilter, 1000, 0)
		if gErr != nil {
			slog.Warn("includeGroupDerived: failed to list group bindings",
				"group_id", gm.GroupID, "error", gErr)
			continue
		}
		gInfo := groupInfoCache[gm.GroupID]
		for _, b := range gBindings {
			sourceMap[b.ID] = gInfo
			result = append(result, b)
		}
	}

	return result, sourceMap
}

// expandScopeGroupDerivedBindings performs project-wide (scope-wide) expansion:
// finds all group-principal bindings for the given scope, then expands each
// group's members into effective user rows. Each derived row carries the
// original binding's role definition but identifies the effective user as
// the principal, with source="group" and group metadata.
//
// Lifecycle filtering: suspended/deleted users are excluded. Transitive group
// membership (groups within groups) is expanded up to a depth limit to prevent
// cycles. Expired or not-yet-valid bindings are excluded by lifecycle checks.
func (s *Server) expandScopeGroupDerivedBindings(ctx context.Context, filter store.RoleBindingFilter) ([]*store.RoleBinding, map[string]groupSourceInfo) {
	// Find all group-principal bindings for the scope.
	groupFilter := store.RoleBindingFilter{
		RoleDefinitionID: filter.RoleDefinitionID,
		PrincipalType:    store.RoleBindingPrincipalGroup,
		ScopeType:        filter.ScopeType,
		ScopeID:          filter.ScopeID,
	}
	groupBindings, err := s.store.ListRoleBindingsFiltered(ctx, groupFilter, 1000, 0)
	if err != nil {
		slog.Warn("includeGroupDerived: failed to list group bindings for scope",
			"scope_type", filter.ScopeType, "scope_id", filter.ScopeID, "error", err)
		return nil, nil
	}
	if len(groupBindings) == 0 {
		return nil, nil
	}

	// Resolve group metadata and collect unique group IDs.
	groupInfoCache := make(map[string]groupSourceInfo)
	for _, b := range groupBindings {
		if _, ok := groupInfoCache[b.PrincipalID]; !ok {
			groupInfoCache[b.PrincipalID] = s.resolveGroupInfo(ctx, b.PrincipalID)
		}
	}

	// For each group binding, expand group members into effective user rows.
	var result []*store.RoleBinding
	sourceMap := make(map[string]groupSourceInfo)
	// Dedup key includes the source group binding ID so that each distinct
	// group path emits its own row. When a user belongs to two groups that
	// each grant the same role, both rows appear with their respective
	// group provenance — preventing silent first-group-wins.
	seen := make(map[string]bool) // dedup key: bindingID + userID

	for _, gb := range groupBindings {
		// Lifecycle check: skip expired or not-yet-valid bindings.
		now := time.Now()
		if gb.ExpiresAt != nil && gb.ExpiresAt.Before(now) {
			continue
		}
		if gb.NotBefore != nil && gb.NotBefore.After(now) {
			continue
		}

		gInfo := groupInfoCache[gb.PrincipalID]

		// Expand group members (including transitive groups, up to depth 5).
		userIDs := s.expandGroupMembers(ctx, gb.PrincipalID, 5)
		for _, userID := range userIDs {
			dedupKey := gb.ID + ":" + userID
			if seen[dedupKey] {
				continue
			}
			seen[dedupKey] = true

			// Create a synthetic binding representing the effective user membership.
			// Uses a deterministic synthetic ID to avoid collisions.
			syntheticID := fmt.Sprintf("derived:%s:%s:%s", gb.ID, userID, gb.RoleDefinitionID)
			derived := &store.RoleBinding{
				ID:               syntheticID,
				RoleDefinitionID: gb.RoleDefinitionID,
				PrincipalType:    store.RoleBindingPrincipalUser,
				PrincipalID:      userID,
				ScopeType:        gb.ScopeType,
				ScopeID:          gb.ScopeID,
				CreatedBy:        gb.CreatedBy,
				NotBefore:        gb.NotBefore,
				ExpiresAt:        gb.ExpiresAt,
			}
			if !gb.CreatedAt.IsZero() {
				derived.CreatedAt = gb.CreatedAt
			}
			sourceMap[syntheticID] = gInfo
			result = append(result, derived)
		}
	}

	return result, sourceMap
}

// expandGroupMembers returns all user member IDs of a group, expanding
// transitive group memberships up to maxDepth levels. Cycles are detected
// via a visited set. Suspended/deleted users are excluded.
func (s *Server) expandGroupMembers(ctx context.Context, groupID string, maxDepth int) []string {
	if maxDepth <= 0 {
		return nil
	}
	visitedGroups := make(map[string]bool)
	// userStatusCache prevents N+1 GetUser calls when the same user appears
	// in multiple groups during transitive expansion. Cache values:
	// true = active (include), false = suspended/deleted/error (exclude).
	userStatusCache := make(map[string]bool)
	var userIDs []string
	s.expandGroupMembersRecursive(ctx, groupID, maxDepth, visitedGroups, userStatusCache, &userIDs)
	return userIDs
}

func (s *Server) expandGroupMembersRecursive(ctx context.Context, groupID string, depth int, visitedGroups map[string]bool, userStatusCache map[string]bool, userIDs *[]string) {
	if depth <= 0 || visitedGroups[groupID] {
		return
	}
	visitedGroups[groupID] = true

	members, err := s.store.GetGroupMembers(ctx, groupID)
	if err != nil {
		slog.Warn("includeGroupDerived: failed to get group members",
			"group_id", groupID, "error", err)
		return
	}

	for _, m := range members {
		switch m.MemberType {
		case store.GroupMemberTypeUser:
			// Check user lifecycle — exclude suspended/deleted users.
			// Use cache to avoid repeated GetUser calls for the same user.
			active, cached := userStatusCache[m.MemberID]
			if !cached {
				user, uErr := s.store.GetUser(ctx, m.MemberID)
				if uErr != nil {
					// User not found or store error — skip safely.
					userStatusCache[m.MemberID] = false
					continue
				}
				active = user.Status != store.UserStatusSuspended
				userStatusCache[m.MemberID] = active
			}
			if !active {
				continue
			}
			*userIDs = append(*userIDs, m.MemberID)
		case store.GroupMemberTypeGroup:
			// Transitive expansion.
			s.expandGroupMembersRecursive(ctx, m.MemberID, depth-1, visitedGroups, userStatusCache, userIDs)
		}
	}
}

// parsePaginationParams extracts limit and offset from query parameters.
// Returns 0 for limit (store uses default) and 0 for offset if not provided.
func parsePaginationParams(r *http.Request) (limit, offset int) {
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}
	if v := r.URL.Query().Get("offset"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			offset = n
		}
	}
	return limit, offset
}

// buildRoleNameCache resolves role definition names for a set of bindings
// using a single batch query (O-3). Returns a map from roleDefinitionID to name
// and an error if the batch query itself fails (O-4). Individual missing
// definitions are logged as warnings and the cache entry is set to "(unknown)"
// so that callers can distinguish "no definition" from "not enriched".
func (s *Server) buildRoleNameCache(ctx context.Context, bindings []*store.RoleBinding) (map[string]string, error) {
	ids := make([]string, 0)
	seen := make(map[string]bool)
	for _, b := range bindings {
		if b != nil && b.RoleDefinitionID != "" && !seen[b.RoleDefinitionID] {
			ids = append(ids, b.RoleDefinitionID)
			seen[b.RoleDefinitionID] = true
		}
	}
	if len(ids) == 0 {
		return map[string]string{}, nil
	}

	rdMap, err := s.store.GetRoleDefinitionsByIDs(ctx, ids)
	if err != nil {
		slog.Warn("failed to batch-load role definitions for enrichment", "error", err)
		return nil, fmt.Errorf("batch-load role definitions: %w", err)
	}

	cache := make(map[string]string, len(rdMap))
	for id, rd := range rdMap {
		cache[id] = rd.Name
	}
	// O-4: Log warning for orphaned bindings (roleDefinitionID not found).
	// Set a sentinel value so callers can detect the gap.
	for _, id := range ids {
		if _, ok := cache[id]; !ok {
			slog.Warn("orphaned role binding: role definition not found",
				"role_definition_id", id)
			cache[id] = "(unknown)"
		}
	}
	return cache, nil
}

func (s *Server) createRoleBinding(w http.ResponseWriter, r *http.Request, user UserIdentity) {
	var req createRoleBindingRequest
	if err := readJSON(r, &req); err != nil {
		BadRequest(w, "invalid request body: "+err.Error())
		return
	}

	if req.RoleDefinitionID == "" {
		BadRequest(w, "roleDefinitionId is required")
		return
	}
	if req.PrincipalType == "" {
		BadRequest(w, "principalType is required")
		return
	}
	if req.PrincipalType != store.RoleBindingPrincipalUser && req.PrincipalType != store.RoleBindingPrincipalAgent && req.PrincipalType != store.RoleBindingPrincipalGroup {
		BadRequest(w, "principalType must be \"user\", \"agent\", or \"group\"")
		return
	}
	if req.PrincipalID == "" {
		BadRequest(w, "principalId is required")
		return
	}

	// Resolve email to UUID for user principals (mirrors addGroupMember pattern).
	if req.PrincipalType == store.RoleBindingPrincipalUser && strings.Contains(req.PrincipalID, "@") {
		resolvedUser, err := s.store.GetUserByEmail(r.Context(), req.PrincipalID)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				BadRequest(w, "user not found with email: "+req.PrincipalID)
				return
			}
			writeErrorFromErr(w, err, "")
			return
		}
		if resolvedUser == nil {
			BadRequest(w, "user not found with email: "+req.PrincipalID)
			return
		}
		req.PrincipalID = resolvedUser.ID
	}

	// Verify group exists for group principals.
	// Try UUID first, fall back to slug lookup (mirrors email→UUID for users).
	if req.PrincipalType == store.RoleBindingPrincipalGroup {
		g, err := s.store.GetGroup(r.Context(), req.PrincipalID)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				g, err = s.store.GetGroupBySlug(r.Context(), req.PrincipalID)
				if err != nil {
					if errors.Is(err, store.ErrNotFound) {
						BadRequest(w, "group not found: "+req.PrincipalID)
					} else {
						writeErrorFromErr(w, err, "")
					}
					return
				}
			} else {
				writeErrorFromErr(w, err, "")
				return
			}
		}
		req.PrincipalID = g.ID
	}

	if req.ScopeType != store.RoleScopeSystem && req.ScopeType != store.RoleScopeProject {
		BadRequest(w, "scopeType must be \"system\" or \"project\"")
		return
	}
	if req.ScopeType == store.RoleScopeProject && req.ScopeID == "" {
		BadRequest(w, "scope_id is required when scope_type is 'project'")
		return
	}

	// Validate lifecycle fields.
	if req.ExpiresAt != nil && !req.ExpiresAt.After(time.Now()) {
		BadRequest(w, "expiresAt must be in the future")
		return
	}
	if req.NotBefore != nil && req.ExpiresAt != nil && !req.ExpiresAt.After(*req.NotBefore) {
		BadRequest(w, "expiresAt must be after notBefore")
		return
	}

	// D6: Validate role applicability — reject if the role is not applicable
	// to the requested principal type. This enforces the seed-declared
	// applicability rules server-side, even if the client bypasses filtering.
	roleDef, err := s.store.GetRoleDefinition(r.Context(), req.RoleDefinitionID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			BadRequest(w, "role definition not found")
			return
		}
		writeErrorFromErr(w, err, "")
		return
	}
	if applicableTo, ok := builtInApplicabilityMap[roleDef.Name]; ok && len(applicableTo) > 0 {
		applicable := false
		for _, pt := range applicableTo {
			if pt == req.PrincipalType {
				applicable = true
				break
			}
		}
		if !applicable {
			BadRequest(w, fmt.Sprintf("role %q is not applicable to %s principals", roleDef.Name, req.PrincipalType))
			return
		}
	}

	// R4 brief item 2: project-scoped role-binding create routes through
	// ProjectMembershipService for built-in project roles (typed governance,
	// delegation, lock, last-owner protection, transactional audit, D4).
	// Custom project-scoped roles use the direct store path (R-7 fix):
	// they bypass membership-service governance but still pass through:
	//   - D6 applicability check (above) for built-in role names,
	//   - CanDelegate security check (below) verifying the actor holds
	//     all permissions in the target role,
	//   - CreatedBy audit trail on the store record.
	// System-scoped operations remain generic (direct store path below).
	isBuiltInProjectRole := validProjectRoles[roleDef.Name]
	if req.ScopeType == store.RoleScopeProject && isBuiltInProjectRole {
		if s.membershipService == nil {
			writeError(w, http.StatusInternalServerError, "internal_error",
				"membership service unavailable — project-scope mutations require governance", nil)
			return
		}
		mReq := MembershipRequest{
			Op:            MembershipOpAdd,
			ProjectID:     req.ScopeID,
			Actor:         user,
			PrincipalType: req.PrincipalType,
			PrincipalID:   req.PrincipalID,
			RoleDefID:     req.RoleDefinitionID,
			NotBefore:     req.NotBefore,
			ExpiresAt:     req.ExpiresAt,
		}
		result, denial := s.membershipService.AddMember(r.Context(), mReq)
		if denial != nil && !denial.Allowed {
			writeError(w, denial.HTTPStatus, denial.DenialCode, denial.Reason, nil)
			return
		}
		writeJSON(w, http.StatusCreated, result.Binding)
		return
	}

	// CanDelegate check: security invariant — the actor must hold all
	// permissions granted by the target role (system-scoped only at this point).
	if s.authzService != nil {
		decision := s.authzService.CanDelegate(r.Context(), user, GrantDescriptor{
			Type:             GrantTypeRoleBinding,
			RoleDefinitionID: req.RoleDefinitionID,
			ScopeType:        req.ScopeType,
			ScopeID:          req.ScopeID,
		})
		if !decision.Allowed {
			writeForbidden(w, "cannot create binding: "+decision.Reason)
			return
		}
	}

	rb := &store.RoleBinding{
		RoleDefinitionID: req.RoleDefinitionID,
		PrincipalType:    req.PrincipalType,
		PrincipalID:      req.PrincipalID,
		ScopeType:        req.ScopeType,
		ScopeID:          req.ScopeID,
		NotBefore:        req.NotBefore,
		ExpiresAt:        req.ExpiresAt,
		CreatedBy:        user.ID(),
	}

	created, err := s.store.CreateRoleBinding(r.Context(), rb)
	if err != nil {
		if errors.Is(err, store.ErrAlreadyExists) {
			Conflict(w, "this role binding already exists")
			return
		}
		if errors.Is(err, store.ErrSuperAdminBindingRestricted) {
			writeForbidden(w, "super-admin role bindings can only be created by the system reconciler")
			return
		}
		if errors.Is(err, store.ErrNotFound) {
			BadRequest(w, "role definition not found")
			return
		}
		writeErrorFromErr(w, err, "")
		return
	}

	slog.Info("role binding created",
		"binding_id", created.ID, "role_definition_id", req.RoleDefinitionID,
		"principal", req.PrincipalType+":"+req.PrincipalID, "actor", user.Email())

	writeJSON(w, http.StatusCreated, created)
}

func (s *Server) deleteRoleBinding(w http.ResponseWriter, r *http.Request, id string, user UserIdentity, systemAuthorized bool) {
	ctx := r.Context()

	// Verify the binding exists before deleting.
	binding, err := s.store.GetRoleBinding(ctx, id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			NotFound(w, "Role Binding")
			return
		}
		writeErrorFromErr(w, err, "")
		return
	}

	// R4 brief item 2: project-scoped role-binding delete MUST route
	// through ProjectMembershipService for serialization lock,
	// last-owner protection, typed governance, transactional audit,
	// and stable denial mapping. System-scoped operations remain generic.
	// R5-1: fail-closed — if membershipService is nil, return 500. Never fall
	// through to direct store mutation for project scope.
	if binding.ScopeType == store.RoleScopeProject {
		if s.membershipService == nil {
			writeError(w, http.StatusInternalServerError, "internal_error",
				"membership service unavailable — project-scope mutations require governance", nil)
			return
		}
		// R2-OPT-4: systemAuthorized is passed from the handler dispatch,
		// established after the caller's successful system-scope
		// role_binding.delete authorization check — not derived from URL
		// string matching. When true, project-level governance is skipped
		// but structural invariants (last-owner guard) are still enforced.
		mReq := MembershipRequest{
			Op:               MembershipOpRemove,
			ProjectID:        binding.ScopeID,
			Actor:            user,
			BindingID:        id,
			SystemAuthorized: systemAuthorized,
		}
		_, denial := s.membershipService.RemoveMember(ctx, mReq)
		if denial != nil && !denial.Allowed {
			writeError(w, denial.HTTPStatus, denial.DenialCode, denial.Reason, nil)
			return
		}
		slog.Info("role binding deleted (via membership service)",
			"binding_id", id, "actor", user.Email())
		w.WriteHeader(http.StatusNoContent)
		return
	}

	// R3-REQ-1 + A-7: All non-project role-binding deletions run inside a
	// transaction that enforces two invariants:
	//   1. Last super-admin guard (system-scope only): LockSystemRoleSync +
	//      count prevents deleting the last super-admin binding.
	//   2. Boundary lockout guard: GovernanceService.GuardRoleBindingDeletion
	//      prevents deleting the last binding carrying access_constraint.admin.
	// Both checks run inside the same transaction so the evaluated state and
	// the deletion share a single commit boundary.
	var lastAdminErr *LastAdminError
	var govErr *GovernanceError
	txErr := s.store.WithTx(ctx, func(tx store.Store) error {
		// Guard 1: last super-admin (system-scope super-admin bindings only).
		// R3: Only count lifecycle-valid bindings (not expired, not future).
		if binding.ScopeType == store.RoleScopeSystem {
			rd, rdErr := tx.GetRoleDefinition(ctx, binding.RoleDefinitionID)
			if rdErr != nil {
				return rdErr
			}
			if rd.Name == store.SystemRoleSuperAdmin {
				if lockErr := tx.LockSystemRoleSync(ctx, rd.ID); lockErr != nil {
					return fmt.Errorf("lock system role sync: %w", lockErr)
				}
				activeCount := countActiveBindings(ctx, tx, rd.ID, store.RoleScopeSystem)
				if activeCount <= 1 {
					lastAdminErr = &LastAdminError{}
					return nil // no-op commit — guard tripped
				}
			}
		}

		// Guard 2: boundary lockout (access_constraint.admin) — A-7.
		// TODO(RS5): CheckRoleBindingRemovalTx is currently a no-op stub.
		// RS5 will deliver the full activation-aware evaluator.
		if s.governanceService != nil {
			if gErr := s.governanceService.CheckRoleBindingRemovalTx(ctx, tx, binding); gErr != nil {
				var ge *GovernanceError
				if errors.As(gErr, &ge) {
					govErr = ge
					return nil // no-op commit — guard tripped
				}
				return gErr // unexpected error — roll back
			}
		}

		return tx.DeleteRoleBinding(ctx, id)
	})

	if lastAdminErr != nil {
		writeError(w, http.StatusConflict, "last_admin",
			"cannot delete last super-admin binding", nil)
		return
	}
	if govErr != nil {
		writeError(w, http.StatusConflict, govErr.Code,
			govErr.Message, nil)
		return
	}
	if txErr != nil {
		if errors.Is(txErr, store.ErrNotFound) {
			NotFound(w, "Role Binding")
			return
		}
		writeErrorFromErr(w, txErr, "")
		return
	}

	slog.Info("role binding deleted",
		"binding_id", id, "actor", user.Email())

	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) listBindingsForUser(w http.ResponseWriter, r *http.Request, userID string) {
	ctx := r.Context()
	bindings, err := s.store.ListRoleBindingsForPrincipal(ctx, store.RoleBindingPrincipalUser, userID)
	if err != nil {
		writeErrorFromErr(w, err, "")
		return
	}
	if bindings == nil {
		bindings = []*store.RoleBinding{}
	}

	// O-3: Build role-name cache using batch lookup.
	roleNameCache, cacheErr := s.buildRoleNameCache(ctx, bindings)
	if cacheErr != nil {
		writeError(w, http.StatusInternalServerError, "enrichment_error",
			"unable to resolve role names", nil)
		return
	}

	// Enrich bindings with human-friendly display names.
	enriched := make([]RoleBindingInfo, 0, len(bindings))
	for i, b := range bindings {
		if b == nil {
			slog.Warn("nil role binding in list result, skipping", "index", i)
			continue
		}
		info := RoleBindingInfo{RoleBinding: *b}
		info.RoleName = roleNameCache[b.RoleDefinitionID]
		info.Source = "direct"
		info.PrincipalDisplayName = s.resolveGroupMemberDisplayName(ctx, b.PrincipalType, b.PrincipalID)
		info.CreatedByDisplayName = s.resolveGroupMemberDisplayName(ctx, store.GroupMemberTypeUser, b.CreatedBy)
		if b.ScopeType == store.RoleScopeProject && b.ScopeID != "" {
			project, err := s.store.GetProject(ctx, b.ScopeID)
			if err == nil && project != nil {
				info.ScopeDisplayName = project.Name
			}
		}
		enriched = append(enriched, info)
	}

	writeJSON(w, http.StatusOK, listRoleBindingsResponse{
		Items:      enriched,
		TotalCount: len(enriched),
	})
}

// ---------------------------------------------------------------------------
// Permissions Registry
// ---------------------------------------------------------------------------

func (s *Server) listPermissions(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, listPermissionsResponse{
		Items:      permissions.Registry,
		TotalCount: len(permissions.Registry),
	})
}

// ---------------------------------------------------------------------------
// Authorization helpers
// ---------------------------------------------------------------------------

// requireWritePermissionForRole checks that the authenticated user has the
// specified role permission.
func (s *Server) requireWritePermissionForRole(w http.ResponseWriter, r *http.Request, permission string, action string) (UserIdentity, bool) {
	identity := GetIdentityFromContext(r.Context())
	if identity == nil {
		writeError(w, http.StatusUnauthorized, ErrCodeUnauthorized, "authentication required", nil)
		return nil, false
	}
	user, ok := identity.(UserIdentity)
	if !ok {
		Forbidden(w)
		return nil, false
	}
	if s.authzService == nil {
		Forbidden(w)
		return nil, false
	}
	decision := s.authzService.Decide(r.Context(), AuthzRequest{
		Principal:  principalContextForIdentity(user),
		Credential: credentialContextForIdentity(user),
		Resource:   Resource{Type: "role", ID: "hub"},
		Action:     Action(action),
		Permission: permission,
	})
	if !decision.Allowed {
		Forbidden(w)
		return nil, false
	}
	return user, true
}

// requireWritePermissionForRoleBinding checks that the authenticated user has
// the specified role_binding permission.
func (s *Server) requireWritePermissionForRoleBinding(w http.ResponseWriter, r *http.Request, permission string, action string) (UserIdentity, bool) {
	identity := GetIdentityFromContext(r.Context())
	if identity == nil {
		writeError(w, http.StatusUnauthorized, ErrCodeUnauthorized, "authentication required", nil)
		return nil, false
	}
	user, ok := identity.(UserIdentity)
	if !ok {
		Forbidden(w)
		return nil, false
	}
	if s.authzService == nil {
		Forbidden(w)
		return nil, false
	}
	decision := s.authzService.Decide(r.Context(), AuthzRequest{
		Principal:  principalContextForIdentity(user),
		Credential: credentialContextForIdentity(user),
		Resource:   Resource{Type: "role_binding", ID: "hub"},
		Action:     Action(action),
		Permission: permission,
	})
	if !decision.Allowed {
		Forbidden(w)
		return nil, false
	}
	return user, true
}

// validatePermissionIDs checks that all provided IDs exist in the permissions registry.
func validatePermissionIDs(ids []string) error {
	valid := make(map[string]bool, len(permissions.Registry))
	for _, p := range permissions.Registry {
		valid[p.ID] = true
	}
	var invalid []string
	for _, id := range ids {
		if !valid[id] {
			invalid = append(invalid, id)
		}
	}
	if len(invalid) > 0 {
		return errors.New("invalid permission IDs: " + strings.Join(invalid, ", "))
	}
	return nil
}

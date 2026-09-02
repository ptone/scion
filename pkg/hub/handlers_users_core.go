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
	"log/slog"
	"net/http"
	"strconv"

	"github.com/GoogleCloudPlatform/scion/pkg/store"
)

type ListUsersResponse struct {
	Users        []UserWithCapabilities `json:"users"`
	NextCursor   string                 `json:"nextCursor,omitempty"`
	TotalCount   int                    `json:"totalCount"`
	Capabilities *Capabilities          `json:"_capabilities,omitempty"`
}

func (s *Server) handleUsers(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.listUsers(w, r)
	case http.MethodPost:
		s.createUser(w, r)
	default:
		MethodNotAllowed(w)
	}
}

func (s *Server) listUsers(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	query := r.URL.Query()

	filter := store.UserFilter{
		Role:   query.Get("role"),
		Status: query.Get("status"),
		Search: query.Get("search"),
	}

	limit := 50
	if l := query.Get("limit"); l != "" {
		if parsed, err := strconv.Atoi(l); err == nil && parsed > 0 {
			limit = parsed
		}
	}

	result, err := s.store.ListUsers(ctx, filter, store.ListOptions{
		Limit:   limit,
		Cursor:  query.Get("cursor"),
		SortBy:  query.Get("sort"),
		SortDir: query.Get("dir"),
	})
	if err != nil {
		writeErrorFromErr(w, err, "")
		return
	}

	// Compute per-item capabilities (users have no scope-level create action)
	identity := GetIdentityFromContext(ctx)
	users := make([]UserWithCapabilities, 0, len(result.Items))
	if identity != nil {
		resources := make([]Resource, len(result.Items))
		for i := range result.Items {
			resources[i] = userResource(&result.Items[i])
		}
		caps := s.authzService.ComputeCapabilitiesBatch(ctx, identity, resources, "user")
		for i := range result.Items {
			if !capabilityAllows(caps[i], ActionRead) {
				continue
			}
			users = append(users, UserWithCapabilities{User: result.Items[i], Cap: caps[i]})
		}
	} else {
		for i := range result.Items {
			users = append(users, UserWithCapabilities{User: result.Items[i]})
		}
	}

	totalCount := result.TotalCount
	if identity != nil && len(users) < len(result.Items) {
		totalCount = len(users)
	}

	writeJSON(w, http.StatusOK, ListUsersResponse{
		Users:      users,
		NextCursor: result.NextCursor,
		TotalCount: totalCount,
	})
}

func (s *Server) createUser(w http.ResponseWriter, r *http.Request) {
	// User creation is managed by the hub's internal sign-in flows (OAuth).
	// Direct API creation is not permitted.
	writeError(w, http.StatusForbidden, ErrCodeForbidden,
		"user creation is managed through sign-in flows and cannot be performed via the API", nil)
}

func (s *Server) handleUserByID(w http.ResponseWriter, r *http.Request) {
	id := extractID(r, "/api/v1/users")

	if id == "" {
		NotFound(w, "User")
		return
	}

	switch r.Method {
	case http.MethodGet:
		s.getUser(w, r, id)
	case http.MethodPatch:
		s.updateUser(w, r, id)
	case http.MethodDelete:
		s.deleteUser(w, r, id)
	default:
		MethodNotAllowed(w)
	}
}

func (s *Server) getUser(w http.ResponseWriter, r *http.Request, id string) {
	ctx := r.Context()
	user, err := s.store.GetUser(ctx, id)
	if err != nil {
		writeErrorFromErr(w, err, "")
		return
	}

	resp := UserWithCapabilities{User: *user}
	if identity := GetIdentityFromContext(ctx); identity != nil {
		resp.Cap = s.authzService.ComputeCapabilities(ctx, identity, userResource(user))
	}

	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) updateUser(w http.ResponseWriter, r *http.Request, id string) {
	ctx := r.Context()

	user, err := s.store.GetUser(ctx, id)
	if err != nil {
		writeErrorFromErr(w, err, "")
		return
	}

	var updates struct {
		DisplayName string                 `json:"displayName,omitempty"`
		Role        string                 `json:"role,omitempty"`
		Status      string                 `json:"status,omitempty"`
		Preferences *store.UserPreferences `json:"preferences,omitempty"`
	}

	if err := readJSON(r, &updates); err != nil {
		BadRequest(w, "Invalid request body: "+err.Error())
		return
	}

	if updates.DisplayName != "" {
		user.DisplayName = updates.DisplayName
	}
	previousRole := user.Role
	if updates.Role != "" {
		user.Role = updates.Role
	}
	if updates.Status != "" {
		user.Status = updates.Status
	}
	if updates.Preferences != nil {
		user.Preferences = updates.Preferences
	}

	if err := s.store.UpdateUser(ctx, user); err != nil {
		writeErrorFromErr(w, err, "")
		return
	}

	// Synchronize role bindings when the user's role changes to ensure
	// the binding set stays consistent with the User.Role field.
	// D5-fix: prevents a viewer-role user from retaining a super-admin
	// binding (and thus all permissions) after a role demotion.
	if updates.Role != "" && updates.Role != previousRole {
		s.syncUserRoleBindings(ctx, user.ID, previousRole, updates.Role)
	}

	writeJSON(w, http.StatusOK, user)
}

func (s *Server) deleteUser(w http.ResponseWriter, r *http.Request, id string) {
	ctx := r.Context()

	// Clean up user-scoped skill injections before deleting the user record.
	// These rows have no FK cascade, so they must be removed explicitly.
	if n, err := s.store.DeleteSkillInjectionsByScope(ctx, store.SkillInjectionScopeUser, id); err != nil {
		slog.Warn("failed to delete user skill injections", "user_id", id, "error", err)
	} else if n > 0 {
		slog.Info("deleted user skill injections", "user_id", id, "count", n)
	}

	if err := s.store.DeleteUser(ctx, id); err != nil {
		writeErrorFromErr(w, err, "")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// syncUserRoleBindings reconciles system-scoped role bindings when a user's
// role changes. When demoting from "admin", the super-admin binding is deleted.
// When promoting to "admin", a super-admin binding is created. For non-admin
// roles, the appropriate hub-member or hub-viewer binding is ensured.
//
// This mirrors the startup reconciliation (ReconcileSuperAdminBindings) but
// runs in real-time as part of the role update handler.
func (s *Server) syncUserRoleBindings(ctx context.Context, userID, oldRole, newRole string) {
	roleMap := map[string]string{
		"admin":  store.SystemRoleSuperAdmin,
		"member": store.SystemRoleHubMember,
		"viewer": store.SystemRoleHubViewer,
	}

	// Delete the old role binding if it maps to a known system role.
	if oldRoleName, ok := roleMap[oldRole]; ok {
		oldRD, err := s.store.GetRoleDefinitionByName(ctx, oldRoleName, store.RoleScopeSystem)
		if err == nil {
			bindings, listErr := s.store.ListRoleBindingsForPrincipal(
				ctx, store.RoleBindingPrincipalUser, userID)
			if listErr == nil {
				for _, b := range bindings {
					if b.RoleDefinitionID == oldRD.ID && b.ScopeType == store.RoleScopeSystem {
						if delErr := s.store.DeleteRoleBinding(ctx, b.ID); delErr != nil {
							slog.Warn("failed to delete old role binding during role sync",
								"user_id", userID, "role", oldRoleName, "binding_id", b.ID, "error", delErr)
						} else {
							slog.Info("deleted role binding during role sync",
								"user_id", userID, "old_role", oldRole, "binding_id", b.ID)
						}
					}
				}
			}
		}
	}

	// Create the new role binding.
	if newRoleName, ok := roleMap[newRole]; ok {
		newRD, err := s.store.GetRoleDefinitionByName(ctx, newRoleName, store.RoleScopeSystem)
		if err != nil {
			slog.Warn("role definition not found during role sync",
				"user_id", userID, "role", newRoleName, "error", err)
			return
		}
		_, err = s.store.CreateRoleBinding(ctx, &store.RoleBinding{
			RoleDefinitionID: newRD.ID,
			PrincipalType:    store.RoleBindingPrincipalUser,
			PrincipalID:      userID,
			ScopeType:        store.RoleScopeSystem,
			ScopeID:          "",
			CreatedBy:        store.SystemReconcileCreatedBy,
		})
		if err != nil {
			if errors.Is(err, store.ErrAlreadyExists) {
				return // binding already exists — idempotent
			}
			slog.Warn("failed to create new role binding during role sync",
				"user_id", userID, "role", newRoleName, "error", err)
		} else {
			slog.Info("created role binding during role sync",
				"user_id", userID, "new_role", newRole)
		}
	}
}

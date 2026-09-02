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

	// Wrap User.Role update + binding sync in a single transaction so
	// a partial failure (e.g., binding sync fails after User.Role is
	// written) never leaves inconsistent authority state. C-1 fix.
	//
	// R2-REQ-3: The user is re-read inside the transaction to capture
	// the actual current role after any concurrent transaction commits.
	// This prevents TOCTOU races where two concurrent requests both read
	// the same oldRole and both try to delete the same binding.
	err = s.store.WithTx(ctx, func(tx store.Store) error {
		// Re-read user inside transaction for serialization correctness.
		txUser, txErr := tx.GetUser(ctx, id)
		if txErr != nil {
			return txErr
		}

		previousRole := txUser.Role
		if updates.DisplayName != "" {
			txUser.DisplayName = updates.DisplayName
		}
		if updates.Role != "" {
			txUser.Role = updates.Role
		}
		if updates.Status != "" {
			txUser.Status = updates.Status
		}
		if updates.Preferences != nil {
			txUser.Preferences = updates.Preferences
		}

		if txErr := tx.UpdateUser(ctx, txUser); txErr != nil {
			return txErr
		}
		roleChanged := updates.Role != "" && updates.Role != previousRole
		if roleChanged {
			return s.syncUserRoleBindings(ctx, tx, txUser.ID, previousRole, updates.Role)
		}
		// Update the outer user pointer for the response.
		*user = *txUser
		return nil
	})
	if err != nil {
		if writeErrorFromLastAdmin(w, err) {
			return
		}
		writeErrorFromErr(w, err, "")
		return
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

// syncUserRoleBindings synchronizes system-scope role bindings when a user's
// role changes. It operates within the caller's transaction (tx) so that the
// User.Role update and binding mutations are atomic — a partial failure rolls
// back the entire operation (C-1 fix).
//
// R-1: Before deleting a super-admin binding, it counts remaining super-admin
// bindings inside the transaction. If this is the last one, the operation is
// refused with a "last_admin" error to prevent lockout.
func (s *Server) syncUserRoleBindings(ctx context.Context, tx store.Store, userID, oldRole, newRole string) error {
	roleMap := map[string]string{
		"admin":  store.SystemRoleSuperAdmin,
		"member": store.SystemRoleHubMember,
		"viewer": store.SystemRoleHubViewer,
	}

	// Delete the old role binding if it maps to a known system role.
	if oldRoleName, ok := roleMap[oldRole]; ok {
		oldRD, err := tx.GetRoleDefinitionByName(ctx, oldRoleName, store.RoleScopeSystem)
		if err != nil {
			return fmt.Errorf("lookup old role definition %q: %w", oldRoleName, err)
		}

		// R2-REQ-3: Acquire a serialization lock on the role definition row
		// BEFORE counting or mutating bindings. On PostgreSQL this is
		// SELECT ... FOR UPDATE, preventing concurrent demotions from both
		// seeing the pre-delete count (write-skew prevention). On SQLite
		// the database-level write lock provides the same guarantee.
		if lockErr := tx.LockSystemRoleSync(ctx, oldRD.ID); lockErr != nil {
			return fmt.Errorf("lock system role sync for %q: %w", oldRoleName, lockErr)
		}

		// R-1: Last-super-admin guard — refuse demotion if this is the last
		// super-admin binding. The lock above serializes concurrent callers
		// so the count is stable within this transaction.
		if oldRoleName == store.SystemRoleSuperAdmin {
			count, countErr := tx.CountRoleBindingsFiltered(ctx, store.RoleBindingFilter{
				RoleDefinitionID: oldRD.ID,
				ScopeType:        store.RoleScopeSystem,
			})
			if countErr != nil {
				return fmt.Errorf("count super-admin bindings: %w", countErr)
			}
			if count <= 1 {
				return &LastAdminError{}
			}
		}

		bindings, err := tx.ListRoleBindingsForPrincipal(
			ctx, store.RoleBindingPrincipalUser, userID)
		if err != nil {
			return fmt.Errorf("list bindings for user %s: %w", userID, err)
		}
		for _, b := range bindings {
			if b.RoleDefinitionID == oldRD.ID && b.ScopeType == store.RoleScopeSystem {
				if err := tx.DeleteRoleBinding(ctx, b.ID); err != nil {
					return fmt.Errorf("delete old role binding %s: %w", b.ID, err)
				}
				slog.Info("deleted role binding during role sync",
					"user_id", userID, "old_role", oldRole, "binding_id", b.ID)
			}
		}
	}

	// Create the new role binding.
	if newRoleName, ok := roleMap[newRole]; ok {
		newRD, err := tx.GetRoleDefinitionByName(ctx, newRoleName, store.RoleScopeSystem)
		if err != nil {
			return fmt.Errorf("lookup new role definition %q: %w", newRoleName, err)
		}
		_, err = tx.CreateRoleBinding(ctx, &store.RoleBinding{
			RoleDefinitionID: newRD.ID,
			PrincipalType:    store.RoleBindingPrincipalUser,
			PrincipalID:      userID,
			ScopeType:        store.RoleScopeSystem,
			ScopeID:          "",
			CreatedBy:        store.SystemReconcileCreatedBy,
		})
		if err != nil {
			if errors.Is(err, store.ErrAlreadyExists) {
				return nil // binding already exists — idempotent
			}
			return fmt.Errorf("create new role binding %q: %w", newRoleName, err)
		}
		slog.Info("created role binding during role sync",
			"user_id", userID, "new_role", newRole)
	}

	return nil
}

// LastAdminError is returned when a role demotion would remove the last
// super-admin binding, risking system lockout.
type LastAdminError struct{}

func (e *LastAdminError) Error() string {
	return "cannot demote: this is the last super-admin user"
}

// writeErrorFromLastAdmin handles LastAdminError with the stable "last_admin" code.
func writeErrorFromLastAdmin(w http.ResponseWriter, err error) bool {
	var lae *LastAdminError
	if errors.As(err, &lae) {
		writeError(w, http.StatusConflict, "last_admin", lae.Error(), nil)
		return true
	}
	return false
}

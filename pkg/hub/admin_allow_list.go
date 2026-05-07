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
	"log/slog"
	"net/http"
	"strings"

	"github.com/GoogleCloudPlatform/scion/pkg/store"
	"github.com/google/uuid"
)

type AllowListAddRequest struct {
	Email string `json:"email"`
	Note  string `json:"note"`
}

type AllowListResponse struct {
	Items      []store.AllowListEntry `json:"items"`
	TotalCount int                    `json:"totalCount"`
	NextCursor string                 `json:"nextCursor,omitempty"`
}

// handleAdminAllowList handles GET/POST /api/v1/admin/allow-list.
func (s *Server) handleAdminAllowList(w http.ResponseWriter, r *http.Request) {
	user := GetUserIdentityFromContext(r.Context())
	if user == nil || user.Role() != "admin" {
		Forbidden(w)
		return
	}

	switch r.Method {
	case http.MethodGet:
		s.handleAdminAllowListGet(w, r)
	case http.MethodPost:
		s.handleAdminAllowListAdd(w, r, user)
	default:
		MethodNotAllowed(w)
	}
}

// handleAdminAllowListByEmail handles DELETE /api/v1/admin/allow-list/{email}.
func (s *Server) handleAdminAllowListByEmail(w http.ResponseWriter, r *http.Request) {
	user := GetUserIdentityFromContext(r.Context())
	if user == nil || user.Role() != "admin" {
		Forbidden(w)
		return
	}

	if r.Method != http.MethodDelete {
		MethodNotAllowed(w)
		return
	}

	// Extract email from path: /api/v1/admin/allow-list/{email}
	email := strings.TrimPrefix(r.URL.Path, "/api/v1/admin/allow-list/")
	if email == "" {
		writeError(w, http.StatusBadRequest, ErrCodeInvalidRequest, "email is required", nil)
		return
	}

	if err := s.store.RemoveAllowListEntry(r.Context(), email); err != nil {
		if err == store.ErrNotFound {
			writeError(w, http.StatusNotFound, ErrCodeNotFound, "email not found in allow list", nil)
			return
		}
		InternalError(w)
		return
	}

	slog.Info("allow list entry removed",
		"email", email,
		"removed_by", user.Email(),
	)

	writeJSON(w, http.StatusOK, map[string]string{"status": "removed"})
}

func (s *Server) handleAdminAllowListGet(w http.ResponseWriter, r *http.Request) {
	opts := store.ListOptions{
		Limit: 50,
	}
	if q := r.URL.Query(); q.Get("cursor") != "" {
		opts.Cursor = q.Get("cursor")
	}

	result, err := s.store.ListAllowListEntries(r.Context(), opts)
	if err != nil {
		InternalError(w)
		return
	}

	writeJSON(w, http.StatusOK, AllowListResponse{
		Items:      result.Items,
		TotalCount: result.TotalCount,
		NextCursor: result.NextCursor,
	})
}

func (s *Server) handleAdminAllowListAdd(w http.ResponseWriter, r *http.Request, user UserIdentity) {
	var req AllowListAddRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, ErrCodeInvalidRequest, "invalid request body", nil)
		return
	}

	email := strings.TrimSpace(strings.ToLower(req.Email))
	if email == "" || !strings.Contains(email, "@") {
		writeError(w, http.StatusBadRequest, ErrCodeInvalidRequest, "valid email is required", nil)
		return
	}

	entry := &store.AllowListEntry{
		ID:      uuid.New().String(),
		Email:   email,
		Note:    req.Note,
		AddedBy: user.ID(),
	}

	if err := s.store.AddAllowListEntry(r.Context(), entry); err != nil {
		if err == store.ErrAlreadyExists {
			writeError(w, http.StatusConflict, ErrCodeConflict, "email already on allow list", nil)
			return
		}
		InternalError(w)
		return
	}

	slog.Info("allow list entry added",
		"email", email,
		"added_by", user.Email(),
	)

	writeJSON(w, http.StatusCreated, entry)
}

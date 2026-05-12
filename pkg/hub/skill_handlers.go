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
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/GoogleCloudPlatform/scion/pkg/api"
	"github.com/GoogleCloudPlatform/scion/pkg/storage"
	"github.com/GoogleCloudPlatform/scion/pkg/store"
)

// --- Request / Response types -----------------------------------------------

// CreateSkillRequest is the request body for creating a skill.
type CreateSkillRequest struct {
	Name        string `json:"name"`
	DisplayName string `json:"displayName,omitempty"`
	Description string `json:"description,omitempty"`
	Scope       string `json:"scope"`
	ScopeID     string `json:"scopeId,omitempty"`
	Status      string `json:"status,omitempty"`
}

// UpdateSkillRequest is the request body for updating a skill.
type UpdateSkillRequest struct {
	Name        string `json:"name,omitempty"`
	DisplayName string `json:"displayName,omitempty"`
	Description string `json:"description,omitempty"`
	Status      string `json:"status,omitempty"`
}

// ListSkillsResponse is the response from listing skills.
type ListSkillsResponse struct {
	Skills     []store.Skill `json:"skills"`
	NextCursor string        `json:"nextCursor,omitempty"`
	TotalCount int           `json:"totalCount"`
}

// PublishVersionRequest is the request body for publishing a new skill version.
type PublishVersionRequest struct {
	Version     string              `json:"version"`
	Changelog   string              `json:"changelog,omitempty"`
	Files       []FileUploadRequest `json:"files,omitempty"`
	ContentHash string              `json:"contentHash,omitempty"`
}

// PublishVersionResponse is the response from publishing a skill version.
type PublishVersionResponse struct {
	Version    *store.SkillVersion `json:"version"`
	UploadURLs []UploadURLInfo     `json:"uploadUrls,omitempty"`
}

// SkillResolveRequest is the request for batch skill resolution.
type SkillResolveRequest struct {
	Skills    []SkillResolveRef `json:"skills"`
	ProjectID string            `json:"projectId,omitempty"`
	UserID    string            `json:"userId,omitempty"`
}

// SkillResolveRef is a single skill URI to resolve.
type SkillResolveRef struct {
	URI string `json:"uri"`
}

// SkillResolveResponse is the response from batch skill resolution.
type SkillResolveResponse struct {
	Resolved []ResolvedSkill       `json:"resolved"`
	Errors   []SkillResolveError   `json:"errors,omitempty"`
	Warnings []SkillResolveWarning `json:"warnings,omitempty"`
}

// ResolvedSkill is a successfully resolved skill with download URLs.
type ResolvedSkill struct {
	URI             string              `json:"uri"`
	Name            string              `json:"name"`
	ResolvedVersion string              `json:"resolvedVersion"`
	ContentHash     string              `json:"contentHash"`
	Files           []ResolvedSkillFile `json:"files"`
}

// ResolvedSkillFile is a file in a resolved skill with a download URL.
type ResolvedSkillFile struct {
	Path    string    `json:"path"`
	URL     string    `json:"url"`
	Method  string    `json:"method"`
	Size    int64     `json:"size,omitempty"`
	Hash    string    `json:"hash,omitempty"`
	Expires time.Time `json:"expires"`
}

// SkillResolveError reports a resolution failure for a single skill URI.
type SkillResolveError struct {
	URI   string `json:"uri"`
	Error string `json:"error"`
}

// SkillResolveWarning reports a resolution warning (e.g. deprecated skill).
type SkillResolveWarning struct {
	URI     string `json:"uri"`
	Message string `json:"message"`
}

// --- Handlers ----------------------------------------------------------------

// handleSkills dispatches GET (list) and POST (create) on /api/v1/skills.
func (s *Server) handleSkills(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.listSkills(w, r)
	case http.MethodPost:
		s.createSkill(w, r)
	default:
		MethodNotAllowed(w)
	}
}

// handleSkillRoutes routes /api/v1/skills/{id}[/action] requests.
func (s *Server) handleSkillRoutes(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/v1/skills/")
	if path == "" {
		NotFound(w, "Skill")
		return
	}

	parts := strings.SplitN(path, "/", 2)
	skillID := parts[0]
	action := ""
	if len(parts) > 1 {
		action = parts[1]
	}

	switch {
	case action == "":
		s.handleSkillCRUD(w, r, skillID)
	case action == "versions":
		s.handleSkillVersions(w, r, skillID)
	case strings.HasPrefix(action, "versions/"):
		s.handleSkillVersionRoutes(w, r, skillID, strings.TrimPrefix(action, "versions/"))
	default:
		NotFound(w, "Skill action")
	}
}

// handleSkillResolve handles POST /api/v1/skills/resolve for batch resolution.
func (s *Server) handleSkillResolve(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		MethodNotAllowed(w)
		return
	}

	ctx := r.Context()

	var req SkillResolveRequest
	if err := readJSON(r, &req); err != nil {
		BadRequest(w, "Invalid request body: "+err.Error())
		return
	}

	if len(req.Skills) == 0 {
		ValidationError(w, "at least one skill URI is required", nil)
		return
	}

	response := SkillResolveResponse{
		Resolved: []ResolvedSkill{},
		Errors:   []SkillResolveError{},
	}

	stor := s.GetStorage()

	for _, ref := range req.Skills {
		// Parse the skill URI into name, scope, scopeID, and version constraint
		name, scope, scopeID, versionConstraint := parseSkillURI(ref.URI, req.ProjectID, req.UserID)

		if name == "" {
			response.Errors = append(response.Errors, SkillResolveError{
				URI:   ref.URI,
				Error: "invalid skill URI",
			})
			continue
		}

		// Look up the skill by name/scope
		skill, err := s.store.GetSkillByName(ctx, name, scope, scopeID)
		if err != nil {
			response.Errors = append(response.Errors, SkillResolveError{
				URI:   ref.URI,
				Error: "skill not found",
			})
			continue
		}

		// Check for deprecation
		if skill.Status == store.SkillStatusArchived {
			response.Errors = append(response.Errors, SkillResolveError{
				URI:   ref.URI,
				Error: "skill is archived",
			})
			continue
		}

		// Find the matching version
		var version *store.SkillVersion
		if versionConstraint == "" || versionConstraint == "latest" {
			// Get all versions and pick the latest published one
			versions, err := s.store.ListSkillVersions(ctx, skill.ID)
			if err != nil || len(versions) == 0 {
				response.Errors = append(response.Errors, SkillResolveError{
					URI:   ref.URI,
					Error: "no published versions found",
				})
				continue
			}
			// Find latest published version
			for i := range versions {
				if versions[i].Status == store.SkillVersionStatusPublished {
					version = &versions[i]
					break
				}
			}
			if version == nil {
				response.Errors = append(response.Errors, SkillResolveError{
					URI:   ref.URI,
					Error: "no published versions found",
				})
				continue
			}
		} else {
			// Try exact version match
			v, err := s.store.GetSkillVersionByNumber(ctx, skill.ID, versionConstraint)
			if err != nil {
				response.Errors = append(response.Errors, SkillResolveError{
					URI:   ref.URI,
					Error: "version not found: " + versionConstraint,
				})
				continue
			}
			version = v
		}

		// Emit warning for deprecated versions
		if version.Status == store.SkillVersionStatusDeprecated {
			response.Warnings = append(response.Warnings, SkillResolveWarning{
				URI:     ref.URI,
				Message: "skill version " + version.Version + " is deprecated",
			})
		}

		// Generate download URLs for the version's files
		resolved := ResolvedSkill{
			URI:             ref.URI,
			Name:            skill.Name,
			ResolvedVersion: version.Version,
			ContentHash:     version.ContentHash,
			Files:           []ResolvedSkillFile{},
		}

		if stor != nil && len(version.Files) > 0 {
			storagePath := skillVersionStoragePath(skill, version)
			for _, file := range version.Files {
				objectPath := storagePath + "/" + file.Path
				signedURL, err := stor.GenerateSignedURL(ctx, objectPath, signedURLOpts("GET"))
				if err != nil {
					continue
				}
				resolved.Files = append(resolved.Files, ResolvedSkillFile{
					Path:    file.Path,
					URL:     signedURL.URL,
					Method:  "GET",
					Size:    file.Size,
					Hash:    file.Hash,
					Expires: signedURL.Expires,
				})
			}

			// For local storage, rewrite file:// URLs to HTTP proxy URLs
			if stor.Provider() == storage.ProviderLocal {
				hubURL := requestBaseURL(r)
				resolved.Files = rewriteLocalSkillDownloadURLs(resolved.Files, hubURL, skill.ID, version.Version)
			}
		}

		response.Resolved = append(response.Resolved, resolved)
	}

	writeJSON(w, http.StatusOK, response)
}

// handleSkillCRUD handles GET/PUT/DELETE on /api/v1/skills/{id}.
func (s *Server) handleSkillCRUD(w http.ResponseWriter, r *http.Request, id string) {
	switch r.Method {
	case http.MethodGet:
		s.getSkill(w, r, id)
	case http.MethodPut:
		s.updateSkill(w, r, id)
	case http.MethodDelete:
		s.deleteSkill(w, r, id)
	default:
		MethodNotAllowed(w)
	}
}

// handleSkillVersions handles GET/POST on /api/v1/skills/{id}/versions.
func (s *Server) handleSkillVersions(w http.ResponseWriter, r *http.Request, skillID string) {
	switch r.Method {
	case http.MethodGet:
		s.listSkillVersions(w, r, skillID)
	case http.MethodPost:
		s.publishSkillVersion(w, r, skillID)
	default:
		MethodNotAllowed(w)
	}
}

// handleSkillVersionRoutes routes requests under /api/v1/skills/{id}/versions/{ver}[/action].
func (s *Server) handleSkillVersionRoutes(w http.ResponseWriter, r *http.Request, skillID, versionPath string) {
	parts := strings.SplitN(versionPath, "/", 2)
	versionStr := parts[0]
	action := ""
	if len(parts) > 1 {
		action = parts[1]
	}

	switch action {
	case "":
		s.getSkillVersion(w, r, skillID, versionStr)
	case "upload":
		s.handleSkillVersionUpload(w, r, skillID, versionStr)
	case "finalize":
		s.handleSkillVersionFinalize(w, r, skillID, versionStr)
	case "download":
		s.handleSkillVersionDownload(w, r, skillID, versionStr)
	default:
		NotFound(w, "Skill version action")
	}
}

// --- CRUD implementations ---------------------------------------------------

// listSkills handles GET /api/v1/skills.
func (s *Server) listSkills(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	query := r.URL.Query()

	filter := store.SkillFilter{
		Name:    query.Get("name"),
		Scope:   query.Get("scope"),
		ScopeID: query.Get("scopeId"),
		Status:  query.Get("status"),
		OwnerID: query.Get("ownerId"),
		Search:  query.Get("search"),
	}

	// Default to active skills only
	if filter.Status == "" {
		filter.Status = store.SkillStatusActive
	}

	limit := 50
	if l := query.Get("limit"); l != "" {
		if parsed, err := strconv.Atoi(l); err == nil && parsed > 0 {
			limit = parsed
		}
	}

	result, err := s.store.ListSkills(ctx, filter, store.ListOptions{
		Limit:  limit,
		Cursor: query.Get("cursor"),
	})
	if err != nil {
		writeErrorFromErr(w, err, "")
		return
	}

	writeJSON(w, http.StatusOK, ListSkillsResponse{
		Skills:     result.Items,
		NextCursor: result.NextCursor,
		TotalCount: result.TotalCount,
	})
}

// createSkill handles POST /api/v1/skills.
func (s *Server) createSkill(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	var req CreateSkillRequest
	if err := readJSON(r, &req); err != nil {
		BadRequest(w, "Invalid request body: "+err.Error())
		return
	}

	if req.Name == "" {
		ValidationError(w, "name is required", nil)
		return
	}

	skill := &store.Skill{
		ID:          api.NewUUID(),
		Name:        req.Name,
		DisplayName: req.DisplayName,
		Description: req.Description,
		Scope:       req.Scope,
		ScopeID:     req.ScopeID,
		Status:      req.Status,
	}

	if skill.Scope == "" {
		skill.Scope = store.SkillScopeGlobal
	}
	if skill.Status == "" {
		skill.Status = store.SkillStatusActive
	}

	if err := s.store.CreateSkill(ctx, skill); err != nil {
		writeErrorFromErr(w, err, "")
		return
	}

	writeJSON(w, http.StatusCreated, skill)
}

// getSkill handles GET /api/v1/skills/{id}.
func (s *Server) getSkill(w http.ResponseWriter, r *http.Request, id string) {
	ctx := r.Context()
	skill, err := s.store.GetSkill(ctx, id)
	if err != nil {
		writeErrorFromErr(w, err, "")
		return
	}

	writeJSON(w, http.StatusOK, skill)
}

// updateSkill handles PUT /api/v1/skills/{id}.
func (s *Server) updateSkill(w http.ResponseWriter, r *http.Request, id string) {
	ctx := r.Context()

	existing, err := s.store.GetSkill(ctx, id)
	if err != nil {
		writeErrorFromErr(w, err, "")
		return
	}

	var req UpdateSkillRequest
	if err := readJSON(r, &req); err != nil {
		BadRequest(w, "Invalid request body: "+err.Error())
		return
	}

	if req.Name != "" {
		existing.Name = req.Name
	}
	if req.DisplayName != "" {
		existing.DisplayName = req.DisplayName
	}
	if req.Description != "" {
		existing.Description = req.Description
	}
	if req.Status != "" {
		existing.Status = req.Status
	}

	if err := s.store.UpdateSkill(ctx, existing); err != nil {
		writeErrorFromErr(w, err, "")
		return
	}

	writeJSON(w, http.StatusOK, existing)
}

// deleteSkill handles DELETE /api/v1/skills/{id}.
func (s *Server) deleteSkill(w http.ResponseWriter, r *http.Request, id string) {
	ctx := r.Context()

	if err := s.store.DeleteSkill(ctx, id); err != nil {
		writeErrorFromErr(w, err, "")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// --- Version operations ------------------------------------------------------

// listSkillVersions handles GET /api/v1/skills/{id}/versions.
func (s *Server) listSkillVersions(w http.ResponseWriter, r *http.Request, skillID string) {
	ctx := r.Context()

	// Ensure the skill exists
	if _, err := s.store.GetSkill(ctx, skillID); err != nil {
		writeErrorFromErr(w, err, "")
		return
	}

	versions, err := s.store.ListSkillVersions(ctx, skillID)
	if err != nil {
		writeErrorFromErr(w, err, "")
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"versions": versions,
	})
}

// publishSkillVersion handles POST /api/v1/skills/{id}/versions.
func (s *Server) publishSkillVersion(w http.ResponseWriter, r *http.Request, skillID string) {
	ctx := r.Context()

	skill, err := s.store.GetSkill(ctx, skillID)
	if err != nil {
		writeErrorFromErr(w, err, "")
		return
	}

	var req PublishVersionRequest
	if err := readJSON(r, &req); err != nil {
		BadRequest(w, "Invalid request body: "+err.Error())
		return
	}

	if req.Version == "" {
		ValidationError(w, "version is required", nil)
		return
	}

	version := &store.SkillVersion{
		ID:          api.NewUUID(),
		SkillID:     skillID,
		Version:     req.Version,
		ContentHash: req.ContentHash,
		Changelog:   req.Changelog,
		Status:      store.SkillVersionStatusPublished,
	}

	// If no files are provided, start as draft until finalized
	if len(req.Files) == 0 {
		version.Status = store.SkillVersionStatusDraft
	}

	if err := s.store.CreateSkillVersion(ctx, version); err != nil {
		writeErrorFromErr(w, err, "")
		return
	}

	// Update skill's latest version if this is higher
	if skill.LatestVersion == "" || req.Version > skill.LatestVersion {
		skill.LatestVersion = req.Version
		_ = s.store.UpdateSkill(ctx, skill)
	}

	response := PublishVersionResponse{
		Version: version,
	}

	// Generate upload URLs if files were specified and storage is available
	if len(req.Files) > 0 {
		stor := s.GetStorage()
		if stor != nil {
			storagePath := skillVersionStoragePath(skill, version)
			uploadURLs, _, err := generateUploadURLs(ctx, stor, storagePath, req.Files)
			if err == nil && len(uploadURLs) > 0 {
				// For local storage, rewrite file:// URLs to HTTP proxy URLs
				if stor.Provider() == storage.ProviderLocal {
					hubURL := requestBaseURL(r)
					uploadURLs = rewriteLocalSkillUploadURLs(uploadURLs, hubURL, skillID, version.Version)
				}
				response.UploadURLs = uploadURLs
			}
		}
	}

	writeJSON(w, http.StatusCreated, response)
}

// getSkillVersion handles GET /api/v1/skills/{id}/versions/{ver}.
func (s *Server) getSkillVersion(w http.ResponseWriter, r *http.Request, skillID, versionStr string) {
	if r.Method != http.MethodGet {
		MethodNotAllowed(w)
		return
	}

	ctx := r.Context()

	version, err := s.store.GetSkillVersionByNumber(ctx, skillID, versionStr)
	if err != nil {
		writeErrorFromErr(w, err, "")
		return
	}

	writeJSON(w, http.StatusOK, version)
}

// --- URI parsing helpers -----------------------------------------------------

// parseSkillURI extracts name, scope, scopeID, and version from a skill URI.
// Supports forms:
//
//	skill://<registry>/<scope>/<name>@<version>
//	skill:///<scope>/<name>@<version>
//	<bare-name>
func parseSkillURI(uri, projectID, userID string) (name, scope, scopeID, version string) {
	// Strip scheme
	raw := uri
	if strings.HasPrefix(raw, "skill://") {
		raw = strings.TrimPrefix(raw, "skill://")
		// Remove registry part (e.g. "scion/" or empty for "///")
		if idx := strings.Index(raw, "/"); idx >= 0 {
			raw = raw[idx+1:] // now <scope>/<name>[@<version>]
		}
	}

	// Split version
	if at := strings.LastIndex(raw, "@"); at >= 0 {
		version = raw[at+1:]
		raw = raw[:at]
	}

	// Split scope and name
	if slash := strings.LastIndex(raw, "/"); slash >= 0 {
		scope = raw[:slash]
		name = raw[slash+1:]
	} else {
		// Bare name — default scope search
		name = raw
		scope = store.SkillScopeGlobal
	}

	// Resolve scope IDs
	switch scope {
	case store.SkillScopeProject:
		scopeID = projectID
	case store.SkillScopeUser:
		scopeID = userID
	}

	return name, scope, scopeID, version
}

// skillVersionStoragePath builds the storage path for a skill version.
func skillVersionStoragePath(skill *store.Skill, version *store.SkillVersion) string {
	// Build: skills/<scope>[/<scopeID>]/<name>/<version>
	base := "skills"
	switch skill.Scope {
	case store.SkillScopeGlobal:
		base += "/global/" + skill.Name
	case store.SkillScopeProject:
		base += "/projects/" + skill.ScopeID + "/" + skill.Name
	case store.SkillScopeUser:
		base += "/users/" + skill.ScopeID + "/" + skill.Name
	default:
		base += "/" + skill.Name
	}
	return base + "/" + version.Version
}

// signedURLOpts creates SignedURLOptions with the standard expiry.
func signedURLOpts(method string) storage.SignedURLOptions {
	return storage.SignedURLOptions{
		Method:  method,
		Expires: SignedURLExpiry,
	}
}

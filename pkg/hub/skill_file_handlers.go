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
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/GoogleCloudPlatform/scion/pkg/storage"
	"github.com/GoogleCloudPlatform/scion/pkg/store"
)

// SkillFinalizeRequest is the request body for finalizing a skill version upload.
type SkillFinalizeRequest struct {
	Files []store.SkillFile `json:"files"`
}

// SkillDownloadResponse contains signed URLs for downloading skill version files.
type SkillDownloadResponse struct {
	Files []DownloadURLInfo `json:"files"`
}

// handleSkillVersionUpload handles POST /api/v1/skills/{id}/versions/{ver}/upload.
func (s *Server) handleSkillVersionUpload(w http.ResponseWriter, r *http.Request, skillID, versionStr string) {
	if r.Method != http.MethodPost {
		MethodNotAllowed(w)
		return
	}

	ctx := r.Context()

	skill, err := s.store.GetSkill(ctx, skillID)
	if err != nil {
		writeErrorFromErr(w, err, "")
		return
	}

	version, err := s.store.GetSkillVersionByNumber(ctx, skillID, versionStr)
	if err != nil {
		writeErrorFromErr(w, err, "")
		return
	}

	stor := s.GetStorage()
	if stor == nil {
		RuntimeError(w, "Storage not configured")
		return
	}

	var req UploadRequest
	if err := readJSON(r, &req); err != nil {
		BadRequest(w, "Invalid request body: "+err.Error())
		return
	}

	if len(req.Files) == 0 {
		ValidationError(w, "at least one file is required", nil)
		return
	}

	// Generate upload URLs
	storagePath := skillVersionStoragePath(skill, version)
	uploadURLs, _, err := generateUploadURLs(ctx, stor, storagePath, req.Files)
	if err != nil {
		RuntimeError(w, "Failed to generate upload URLs: "+err.Error())
		return
	}
	if len(uploadURLs) == 0 && len(req.Files) > 0 {
		RuntimeError(w, "Failed to generate upload URLs")
		return
	}

	// For local storage, rewrite file:// URLs to HTTP proxy URLs
	if stor.Provider() == storage.ProviderLocal {
		hubURL := requestBaseURL(r)
		uploadURLs = rewriteLocalSkillUploadURLs(uploadURLs, hubURL, skillID, version.Version)
	}

	writeJSON(w, http.StatusOK, UploadResponse{
		UploadURLs: uploadURLs,
	})
}

// handleSkillVersionFinalize handles POST /api/v1/skills/{id}/versions/{ver}/finalize.
func (s *Server) handleSkillVersionFinalize(w http.ResponseWriter, r *http.Request, skillID, versionStr string) {
	if r.Method != http.MethodPost {
		MethodNotAllowed(w)
		return
	}

	ctx := r.Context()

	skill, err := s.store.GetSkill(ctx, skillID)
	if err != nil {
		writeErrorFromErr(w, err, "")
		return
	}

	version, err := s.store.GetSkillVersionByNumber(ctx, skillID, versionStr)
	if err != nil {
		writeErrorFromErr(w, err, "")
		return
	}

	stor := s.GetStorage()
	if stor == nil {
		RuntimeError(w, "Storage not configured")
		return
	}

	var req SkillFinalizeRequest
	if err := readJSON(r, &req); err != nil {
		BadRequest(w, "Invalid request body: "+err.Error())
		return
	}

	if len(req.Files) == 0 {
		ValidationError(w, "at least one file is required", nil)
		return
	}

	// Verify files exist in storage
	storagePath := skillVersionStoragePath(skill, version)
	for _, file := range req.Files {
		objectPath := storagePath + "/" + file.Path
		exists, exErr := stor.Exists(ctx, objectPath)
		if exErr != nil || !exists {
			ValidationError(w, fmt.Sprintf("file not found in storage: %s", file.Path), nil)
			return
		}
	}

	// Compute content hash
	contentHash := computeSkillContentHash(req.Files)

	// Update the version record
	version.Files = req.Files
	version.ContentHash = contentHash
	version.Status = store.SkillVersionStatusPublished

	// Since we can't update a version directly via the store interface,
	// we delete and recreate. However, a simpler approach is to use the
	// skill update path. For now, we'll delete the old version and create
	// a new one with the same ID.
	if err := s.store.DeleteSkillVersion(ctx, version.ID); err != nil {
		writeErrorFromErr(w, err, "")
		return
	}
	if err := s.store.CreateSkillVersion(ctx, version); err != nil {
		writeErrorFromErr(w, err, "")
		return
	}

	// Update the skill's latest version tracking
	skill.LatestVersion = version.Version
	_ = s.store.UpdateSkill(ctx, skill)

	writeJSON(w, http.StatusOK, version)
}

// handleSkillVersionDownload handles GET /api/v1/skills/{id}/versions/{ver}/download.
func (s *Server) handleSkillVersionDownload(w http.ResponseWriter, r *http.Request, skillID, versionStr string) {
	if r.Method != http.MethodGet {
		MethodNotAllowed(w)
		return
	}

	ctx := r.Context()

	skill, err := s.store.GetSkill(ctx, skillID)
	if err != nil {
		writeErrorFromErr(w, err, "")
		return
	}

	version, err := s.store.GetSkillVersionByNumber(ctx, skillID, versionStr)
	if err != nil {
		writeErrorFromErr(w, err, "")
		return
	}

	stor := s.GetStorage()
	if stor == nil {
		RuntimeError(w, "Storage not configured")
		return
	}

	if len(version.Files) == 0 {
		ValidationError(w, "skill version has no files", nil)
		return
	}

	// Generate download URLs
	storagePath := skillVersionStoragePath(skill, version)

	downloadURLs := make([]DownloadURLInfo, 0, len(version.Files))
	for _, file := range version.Files {
		objectPath := storagePath + "/" + file.Path
		signedURL, err := stor.GenerateSignedURL(ctx, objectPath, signedURLOpts("GET"))
		if err != nil {
			continue
		}
		downloadURLs = append(downloadURLs, DownloadURLInfo{
			Path: file.Path,
			URL:  signedURL.URL,
			Size: file.Size,
			Hash: file.Hash,
		})
	}

	// For local storage, rewrite file:// URLs to HTTP proxy URLs
	if stor.Provider() == storage.ProviderLocal {
		hubURL := requestBaseURL(r)
		downloadURLs = rewriteLocalDownloadURLs(downloadURLs, hubURL, "skills", skillID)
	}

	writeJSON(w, http.StatusOK, SkillDownloadResponse{
		Files: downloadURLs,
	})
}

// --- Helper functions --------------------------------------------------------

// computeSkillContentHash computes a content hash from sorted skill file hashes.
func computeSkillContentHash(files []store.SkillFile) string {
	sorted := make([]store.SkillFile, len(files))
	copy(sorted, files)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].Path < sorted[j].Path
	})

	hasher := sha256.New()
	for _, file := range sorted {
		hasher.Write([]byte(file.Hash))
	}

	return "sha256:" + hex.EncodeToString(hasher.Sum(nil))
}

// rewriteLocalSkillUploadURLs rewrites file:// URLs to HTTP proxy URLs for skill uploads.
func rewriteLocalSkillUploadURLs(urls []UploadURLInfo, hubEndpoint, skillID, version string) []UploadURLInfo {
	if hubEndpoint == "" {
		return urls
	}
	hubEndpoint = strings.TrimRight(hubEndpoint, "/")
	for i := range urls {
		if strings.HasPrefix(urls[i].URL, "file://") {
			urls[i].URL = fmt.Sprintf("%s/api/v1/skills/%s/versions/%s/files/%s",
				hubEndpoint, skillID, version, urls[i].Path)
			urls[i].Method = http.MethodPut
			urls[i].Headers = map[string]string{
				"Content-Type": "application/octet-stream",
			}
		}
	}
	return urls
}

// rewriteLocalSkillDownloadURLs rewrites file:// URLs to HTTP proxy URLs for skill downloads.
func rewriteLocalSkillDownloadURLs(files []ResolvedSkillFile, hubEndpoint, skillID, version string) []ResolvedSkillFile {
	if hubEndpoint == "" {
		return files
	}
	hubEndpoint = strings.TrimRight(hubEndpoint, "/")
	for i := range files {
		if strings.HasPrefix(files[i].URL, "file://") {
			files[i].URL = fmt.Sprintf("%s/api/v1/skills/%s/versions/%s/files/%s?raw=1",
				hubEndpoint, skillID, version, files[i].Path)
		}
	}
	return files
}

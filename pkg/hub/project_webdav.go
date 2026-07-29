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
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path"
	"strings"
	"time"

	"github.com/GoogleCloudPlatform/scion/pkg/store"
	"golang.org/x/net/webdav"
)

// syncExcludePatterns defines directory/file prefixes excluded from WebDAV sync.
// These are matched against the first path component.
var syncExcludePatterns = []string{
	".git",
	".scion",
	"node_modules",
}

// syncExcludeExtensions defines file extensions excluded from WebDAV sync.
var syncExcludeExtensions = []string{
	".env",
}

// handleProjectWebDAV serves a WebDAV endpoint for project workspace file sync.
// It mounts at /api/v1/projects/{projectId}/dav/ and serves the project's workspace
// directory with file exclusion filters applied.
//
// For hub-managed and shared-workspace projects, it serves the workspace directly.
// For linked projects (workspace on a remote broker), it serves from the hub's
// cached copy. The cache is populated via the cache/refresh or cache/notify
// endpoints (Phase 3: Linked Project Relay).
func (s *Server) handleProjectWebDAV(w http.ResponseWriter, r *http.Request, projectID, davPath string) {
	ctx := r.Context()

	project, err := s.store.GetProject(ctx, projectID)
	if err != nil {
		writeErrorFromErr(w, err, "")
		return
	}

	// Gate before the path resolves, for the same reason as the workspace file
	// routes: resolveProjectWebDAVPath answers 409 with a message describing the
	// project's shape, so authorizing after it would leak that shape to a caller
	// the gate is about to refuse.
	//
	// This is the same gate as the /workspace/files routes, and it has to be.
	// Both endpoints serve the same bytes out of the same directory returned by
	// the same resolveProjectWebDAVPath. Gating /workspace/files while leaving
	// /dav/ open would not be a partial fix; it would be no fix, because PUT and
	// DELETE through this handler reach the same files. Whatever the two gates
	// say, they must say the same thing — hence the shared
	// workspaceActionForMethod rather than a second method table here.
	if !s.authorizeProjectWorkspaceAccess(w, r, project) {
		return
	}

	// Determine workspace path based on project type
	workspacePath, err := s.resolveProjectWebDAVPath(ctx, project)
	if err != nil {
		Conflict(w, err.Error())
		return
	}

	// Ensure the workspace directory exists
	if err := os.MkdirAll(workspacePath, 0755); err != nil {
		slog.Error("failed to create project workspace directory", "project_id", projectID, "error", err)
		InternalError(w)
		return
	}

	// Build the prefix that the WebDAV handler should strip.
	// The full URL path is /api/v1/{projects|projects}/{id}/dav/...
	// We need to strip everything up to and including /dav
	prefixEnd := strings.Index(r.URL.Path, "/dav/")
	if prefixEnd == -1 {
		prefixEnd = strings.Index(r.URL.Path, "/dav")
	}
	if prefixEnd == -1 {
		NotFound(w, "WebDAV endpoint")
		return
	}
	prefix := r.URL.Path[:prefixEnd+len("/dav")]

	handler := &webdav.Handler{
		Prefix:     prefix,
		FileSystem: &filteredFS{root: webdav.Dir(workspacePath)},
		LockSystem: webdav.NewMemLS(),
		Logger: func(r *http.Request, err error) {
			if err != nil {
				slog.Debug("webdav operation", "method", r.Method, "path", r.URL.Path, "error", err)
			}
		},
	}

	handler.ServeHTTP(w, r)

	// Update sync state after successful write operations
	if r.Method == "PUT" || r.Method == "DELETE" || r.Method == "MKCOL" || r.Method == "MOVE" {
		go s.updateProjectSyncState(project.ID, workspacePath)
	}
}

// updateProjectSyncState recalculates and persists file count and total bytes for a project.
func (s *Server) updateProjectSyncState(projectID, workspacePath string) {
	var fileCount int
	var totalBytes int64

	_ = walkFilteredDir(workspacePath, func(relPath string, info os.FileInfo) {
		fileCount++
		totalBytes += info.Size()
	})

	now := time.Now()
	state := &store.ProjectSyncState{
		ProjectID:    projectID,
		BrokerID:     "", // hub-managed
		LastSyncTime: &now,
		FileCount:    fileCount,
		TotalBytes:   totalBytes,
	}

	if err := s.store.UpsertProjectSyncState(context.Background(), state); err != nil {
		slog.Warn("failed to update project sync state", "project_id", projectID, "error", err)
	}
}

// resolveProjectWebDAVPath determines the filesystem path to serve via WebDAV
// for a given project. For hub-managed and shared-workspace projects, this is the
// hub-managed workspace directory. For linked projects (workspace on a remote
// broker), this is the hub's cached copy of that workspace.
func (s *Server) resolveProjectWebDAVPath(ctx context.Context, project *store.Project) (string, error) {
	// Hub-managed projects (no git remote) always have a managed workspace
	if project.GitRemote == "" {
		path, err := hubManagedProjectPath(project.Slug)
		if err != nil {
			return "", fmt.Errorf("failed to resolve project path")
		}
		return path, nil
	}

	// Shared-workspace git projects have a managed workspace on the hub
	if project.IsSharedWorkspace() {
		path, err := hubManagedProjectPath(project.Slug)
		if err != nil {
			return "", fmt.Errorf("failed to resolve project path")
		}
		return path, nil
	}

	// Linked projects: check if there's a co-located broker with a local path
	providers, err := s.store.GetProjectProviders(ctx, project.ID)
	if err != nil {
		return "", fmt.Errorf("failed to resolve project providers")
	}

	// Git projects with no providers have no accessible workspace — the workspace
	// lives on broker machines and no broker has registered for this project yet.
	if len(providers) == 0 {
		return "", fmt.Errorf("workspace is not available for git-anchored projects without a registered provider")
	}

	// Check for co-located (embedded) broker with a local path
	for _, p := range providers {
		if s.isEmbeddedBroker(p.BrokerID) && p.LocalPath != "" {
			// Co-located broker: serve from local filesystem directly
			return p.LocalPath, nil
		}
	}

	// Remote linked project: serve from the hub's cached copy.
	// The cache is populated via cache/refresh or cache/notify endpoints.
	cachePath, err := hubManagedProjectPath(project.Slug)
	if err != nil {
		return "", fmt.Errorf("failed to resolve project cache path")
	}

	// If cache doesn't exist yet, return the path anyway (MkdirAll will create it).
	// The client should trigger a cache/refresh to populate it.
	if !hasProjectCache(project.Slug) {
		slog.Debug("linked project cache not yet populated",
			"project_id", project.ID, "slug", project.Slug)
	}

	return cachePath, nil
}

// walkFilteredDir walks a directory, calling fn for each non-excluded file.
func walkFilteredDir(root string, fn func(relPath string, info os.FileInfo)) error {
	return walkFilteredDirRecursive(root, "", fn)
}

func walkFilteredDirRecursive(root, prefix string, fn func(relPath string, info os.FileInfo)) error {
	fullDir := root
	if prefix != "" {
		fullDir = root + "/" + prefix
	}

	entries, err := os.ReadDir(fullDir)
	if err != nil {
		return err
	}

	for _, entry := range entries {
		name := entry.Name()
		relPath := name
		if prefix != "" {
			relPath = prefix + "/" + name
		}

		if isExcluded(relPath) {
			continue
		}

		if entry.IsDir() {
			_ = walkFilteredDirRecursive(root, relPath, fn)
			continue
		}

		info, err := entry.Info()
		if err != nil {
			continue
		}
		fn(relPath, info)
	}
	return nil
}

// filteredFS wraps a webdav.FileSystem to exclude sync-excluded paths.
type filteredFS struct {
	root webdav.FileSystem
}

func (fs *filteredFS) Mkdir(ctx context.Context, name string, perm os.FileMode) error {
	if isExcluded(name) {
		return os.ErrPermission
	}
	return fs.root.Mkdir(ctx, name, perm)
}

func (fs *filteredFS) OpenFile(ctx context.Context, name string, flag int, perm os.FileMode) (webdav.File, error) {
	if isExcluded(name) {
		return nil, os.ErrNotExist
	}

	f, err := fs.root.OpenFile(ctx, name, flag, perm)
	if err != nil {
		return f, err
	}

	// If this is a directory being opened for reading, wrap to filter children
	info, statErr := f.Stat()
	if statErr == nil && info.IsDir() {
		return &filteredDir{File: f, dirName: name}, nil
	}

	return f, nil
}

func (fs *filteredFS) RemoveAll(ctx context.Context, name string) error {
	if isExcluded(name) {
		return os.ErrPermission
	}
	return fs.root.RemoveAll(ctx, name)
}

func (fs *filteredFS) Rename(ctx context.Context, oldName, newName string) error {
	if isExcluded(oldName) || isExcluded(newName) {
		return os.ErrPermission
	}
	return fs.root.Rename(ctx, oldName, newName)
}

func (fs *filteredFS) Stat(ctx context.Context, name string) (os.FileInfo, error) {
	if isExcluded(name) {
		return nil, os.ErrNotExist
	}
	return fs.root.Stat(ctx, name)
}

// filteredDir wraps a webdav.File (directory) to exclude entries from Readdir.
type filteredDir struct {
	webdav.File
	dirName string
}

func (d *filteredDir) Readdir(count int) ([]os.FileInfo, error) {
	entries, err := d.File.Readdir(count)
	if err != nil {
		return entries, err
	}

	filtered := make([]os.FileInfo, 0, len(entries))
	for _, entry := range entries {
		childPath := path.Join(d.dirName, entry.Name())
		if !isExcluded(childPath) {
			filtered = append(filtered, entry)
		}
	}
	return filtered, nil
}

// isExcluded returns true if a path should be excluded from sync.
// name is a slash-separated path relative to the workspace root (may have a leading /).
func isExcluded(name string) bool {
	// Normalize: strip leading slash
	clean := strings.TrimPrefix(path.Clean(name), "/")
	if clean == "" || clean == "." {
		return false
	}

	// Get the top-level component
	topLevel := clean
	if idx := strings.IndexByte(clean, '/'); idx >= 0 {
		topLevel = clean[:idx]
	}

	// Check directory prefix exclusions
	for _, pattern := range syncExcludePatterns {
		if topLevel == pattern {
			return true
		}
	}

	// Check extension exclusions (on the full filename, not just top-level)
	baseName := path.Base(clean)
	for _, ext := range syncExcludeExtensions {
		if strings.HasSuffix(baseName, ext) {
			return true
		}
	}

	return false
}

// ProjectSyncStatusResponse is the response for the sync status endpoint.
type ProjectSyncStatusResponse struct {
	ProjectID  string                   `json:"projectId"`
	States     []store.ProjectSyncState `json:"states"`
	TotalFiles int                      `json:"totalFiles"`
	TotalBytes int64                    `json:"totalBytes"`
}

// MarshalJSON implements custom marshaling to support legacy groveId field.
func (r ProjectSyncStatusResponse) MarshalJSON() ([]byte, error) {
	type Alias ProjectSyncStatusResponse
	return json.Marshal(&struct {
		Alias
		GroveID string `json:"groveId"`
	}{
		Alias:   Alias(r),
		GroveID: r.ProjectID,
	})
}

// UnmarshalJSON implements custom unmarshaling to support legacy groveId field.
func (r *ProjectSyncStatusResponse) UnmarshalJSON(data []byte) error {
	type Alias ProjectSyncStatusResponse
	aux := &struct {
		GroveID string `json:"groveId"`
		*Alias
	}{
		Alias: (*Alias)(r),
	}
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	if r.ProjectID == "" && aux.GroveID != "" {
		r.ProjectID = aux.GroveID
	}
	return nil
}

// handleProjectSyncStatus returns the sync status for a project.
//
// This is the second entry handler in this file. Until this gate was added it
// had no authorization call of any kind: any caller the middleware
// authenticated — a user with no relationship to the project, an agent
// belonging to a different project, a runtime broker — could read any
// project's sync state. That is every field of store.ProjectSyncState, and
// naming only some of them undersells it: the IDs of the brokers serving the
// project, its file and byte counts, and — the two most sensitive and the two
// most easily overlooked — LastCommitSHA, which identifies the exact code
// state of the project, and LastSyncTime, which says when it last moved.
// The workspace sibling in this same file
// refused those same callers, so the exposure was reachable by asking a
// neighbouring route the same question.
//
// It shares authorizeProjectWorkspaceAccess with the routes above rather than
// growing a rule of its own. GET maps to a read, and project_cache.go's
// cache/status handler — which discloses the very same ProjectSyncState
// records — is gated as a read too. Two endpoints that return the same records
// must answer the same callers the same way, or the stricter one is decorative.
func (s *Server) handleProjectSyncStatus(w http.ResponseWriter, r *http.Request, projectID string) {
	if r.Method != http.MethodGet {
		MethodNotAllowed(w)
		return
	}

	ctx := r.Context()

	// Verify project exists
	project, err := s.store.GetProject(ctx, projectID)
	if err != nil {
		writeErrorFromErr(w, err, "")
		return
	}

	// Gate before the sync states are read, which is the disclosure itself.
	if !s.authorizeProjectWorkspaceAccess(w, r, project) {
		return
	}

	states, err := s.store.ListProjectSyncStates(ctx, projectID)
	if err != nil {
		InternalError(w)
		return
	}

	var totalFiles int
	var totalBytes int64
	for _, st := range states {
		totalFiles += st.FileCount
		totalBytes += st.TotalBytes
	}

	writeJSON(w, http.StatusOK, ProjectSyncStatusResponse{
		ProjectID:  projectID,
		States:     states,
		TotalFiles: totalFiles,
		TotalBytes: totalBytes,
	})
}

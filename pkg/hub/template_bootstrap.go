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
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/GoogleCloudPlatform/scion/pkg/api"
	"github.com/GoogleCloudPlatform/scion/pkg/config"
	"github.com/GoogleCloudPlatform/scion/pkg/config/templateimport"
	"github.com/GoogleCloudPlatform/scion/pkg/storage"
	"github.com/GoogleCloudPlatform/scion/pkg/store"
	"github.com/GoogleCloudPlatform/scion/pkg/transfer"
)

// BootstrapTemplatesFromDir imports or updates local templates from a directory
// into the Hub's database and storage. On first run it imports all templates;
// on subsequent runs it detects changed templates (by content hash) and
// re-uploads only those that differ from the database version.
func (s *Server) BootstrapTemplatesFromDir(ctx context.Context, templatesDir string) error {
	// Check if the directory exists
	info, err := os.Stat(templatesDir)
	if err != nil || !info.IsDir() {
		s.templateLog.Debug("template bootstrap: directory not found, skipping", "dir", templatesDir)
		return nil
	}

	// Check that storage is configured
	stor := s.GetStorage()
	if stor == nil {
		s.templateLog.Warn("template bootstrap: no storage backend configured, skipping")
		return nil
	}

	// Scan the directory for template subdirectories
	entries, err := os.ReadDir(templatesDir)
	if err != nil {
		return err
	}

	imported, updated := 0, 0
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		name := entry.Name()
		templatePath := filepath.Join(templatesDir, name)
		slug := api.Slugify(name)

		// Check if this template already exists in the database
		existing, err := s.store.GetTemplateBySlug(ctx, slug, string(store.TemplateScopeGlobal), "")
		if err != nil && err != store.ErrNotFound {
			s.templateLog.Warn("template bootstrap: failed to look up template, skipping",
				"template", name, "error", err)
			continue
		}

		if existing == nil {
			// New template — import it
			if err := s.bootstrapSingleTemplate(ctx, name, templatePath, store.TemplateScopeGlobal, ""); err != nil {
				s.templateLog.Warn("template bootstrap: failed to import template, skipping",
					"template", name, "error", err)
				continue
			}
			imported++
		} else {
			// Existing template — check if local files have changed
			changed, err := s.syncExistingTemplate(ctx, existing, templatePath)
			if err != nil {
				s.templateLog.Warn("template bootstrap: failed to sync template, skipping",
					"template", name, "error", err)
				continue
			}
			if changed {
				updated++
			}
		}
	}

	if imported > 0 || updated > 0 {
		s.templateLog.Info("template bootstrap: sync complete",
			"imported", imported, "updated", updated)
	}

	return nil
}

// syncExistingTemplate checks if a local template directory has changed
// compared to what is stored in the Hub database. If so, it re-uploads
// the files and updates the database record. Returns true if an update occurred.
func (s *Server) syncExistingTemplate(ctx context.Context, existing *store.Template, templatePath string) (bool, error) {
	stor := s.GetStorage()

	// Collect current files from disk
	files, err := transfer.CollectFiles(templatePath, nil)
	if err != nil {
		return false, err
	}

	// Build the file list and compute the new content hash
	var templateFiles []store.TemplateFile
	for _, fi := range files {
		templateFiles = append(templateFiles, store.TemplateFile{
			Path: fi.Path,
			Size: fi.Size,
			Hash: fi.Hash,
			Mode: fi.Mode,
		})
	}
	newHash := computeContentHash(templateFiles)

	// If content hash matches, nothing to do
	if newHash == existing.ContentHash {
		return false, nil
	}

	s.templateLog.Info("template bootstrap: local template changed, re-syncing",
		"template", existing.Name, "oldHash", existing.ContentHash, "newHash", newHash)

	// Re-upload all files to storage
	storagePath := existing.StoragePath
	if storagePath == "" {
		storagePath = storage.TemplateStoragePath(existing.Scope, "", existing.Slug)
	}

	var uploadedFiles []store.TemplateFile
	for _, fi := range files {
		objectPath := storagePath + "/" + fi.Path

		f, err := os.Open(fi.FullPath)
		if err != nil {
			s.templateLog.Warn("template bootstrap: failed to open file, skipping",
				"file", fi.Path, "error", err)
			continue
		}

		_, err = stor.Upload(ctx, objectPath, f, storage.UploadOptions{})
		f.Close()
		if err != nil {
			s.templateLog.Warn("template bootstrap: failed to upload file, skipping",
				"file", fi.Path, "error", err)
			continue
		}

		uploadedFiles = append(uploadedFiles, store.TemplateFile{
			Path: fi.Path,
			Size: fi.Size,
			Hash: fi.Hash,
			Mode: fi.Mode,
		})
	}

	// Update the database record with new files and hash
	existing.Files = uploadedFiles
	existing.ContentHash = newHash
	existing.Harness = detectHarnessFromConfig(templatePath, existing.Name)

	if err := s.store.UpdateTemplate(ctx, existing); err != nil {
		return false, err
	}

	return true, nil
}

// bootstrapSingleTemplate imports one local template directory into the
// Hub's database and storage backend under the given scope and groveID.
// For global templates pass store.TemplateScopeGlobal and "".
func (s *Server) bootstrapSingleTemplate(ctx context.Context, name, templatePath, scope, groveID string) error {
	stor := s.GetStorage()

	// Collect files from the template directory
	files, err := transfer.CollectFiles(templatePath, nil)
	if err != nil {
		return err
	}

	// Detect harness type from the template config
	harness := detectHarnessFromConfig(templatePath, name)

	slug := api.Slugify(name)

	// Create a pending template record
	storagePath := storage.TemplateStoragePath(scope, groveID, slug)
	tmpl := &store.Template{
		ID:            api.NewUUID(),
		Name:          name,
		Slug:          slug,
		Harness:       harness,
		Scope:         scope,
		ScopeID:       groveID,
		GroveID:       groveID, // deprecated alias kept for compatibility
		Status:        store.TemplateStatusPending,
		StoragePath:   storagePath,
		StorageBucket: stor.Bucket(),
		StorageURI:    storage.TemplateStorageURI(stor.Bucket(), scope, groveID, slug),
		Visibility:    store.VisibilityPrivate,
	}

	if err := s.store.CreateTemplate(ctx, tmpl); err != nil {
		return err
	}

	// Upload each file to storage
	var templateFiles []store.TemplateFile
	for _, fi := range files {
		objectPath := storagePath + "/" + fi.Path

		f, err := os.Open(fi.FullPath)
		if err != nil {
			s.templateLog.Warn("template bootstrap: failed to open file, skipping",
				"file", fi.Path, "error", err)
			continue
		}

		_, err = stor.Upload(ctx, objectPath, f, storage.UploadOptions{})
		f.Close()
		if err != nil {
			s.templateLog.Warn("template bootstrap: failed to upload file, skipping",
				"file", fi.Path, "error", err)
			continue
		}

		templateFiles = append(templateFiles, store.TemplateFile{
			Path: fi.Path,
			Size: fi.Size,
			Hash: fi.Hash,
			Mode: fi.Mode,
		})
	}

	// Compute content hash and activate the template
	contentHash := computeContentHash(templateFiles)
	tmpl.Files = templateFiles
	tmpl.ContentHash = contentHash
	tmpl.Status = store.TemplateStatusActive

	if err := s.store.UpdateTemplate(ctx, tmpl); err != nil {
		return err
	}

	s.templateLog.Info("template bootstrap: imported template",
		"name", name, "files", len(templateFiles), "harness", harness)
	return nil
}

// detectHarnessFromConfig reads a template's config and returns the harness type.
// It checks the ScionConfig fields (HarnessConfig, DefaultHarnessConfig, Harness)
// and falls back to name-based inference.
func detectHarnessFromConfig(templatePath, templateName string) string {
	t := &config.Template{Name: templateName, Path: templatePath}
	cfg, err := t.LoadConfig()
	if err == nil && cfg != nil {
		// Check config fields in priority order
		if cfg.HarnessConfig != "" {
			return inferHarnessFromName(cfg.HarnessConfig)
		}
		if cfg.DefaultHarnessConfig != "" {
			return inferHarnessFromName(cfg.DefaultHarnessConfig)
		}
		if cfg.Harness != "" {
			return cfg.Harness
		}
	}

	// Fall back to name-based inference
	return inferHarnessFromName(templateName)
}

// inferHarnessFromName guesses the harness type from a name string.
func inferHarnessFromName(name string) string {
	lower := strings.ToLower(name)
	switch {
	case strings.Contains(lower, "claude"):
		return "claude"
	case strings.Contains(lower, "gemini"):
		return "gemini"
	case strings.Contains(lower, "opencode"):
		return "opencode"
	case strings.Contains(lower, "codex"):
		return "codex"
	default:
		return ""
	}
}

// importTemplatesFromRemote fetches a remote source URL, discovers scion
// templates within it, and registers each one into the Hub store scoped
// to the given grove. Returns the names of all templates imported or updated.
func (s *Server) importTemplatesFromRemote(ctx context.Context, groveID, sourceURL string) ([]string, error) {
	if !config.IsRemoteURI(sourceURL) {
		return nil, fmt.Errorf("source must be a remote URI (http://, https://, or rclone)")
	}

	stor := s.GetStorage()
	if stor == nil {
		return nil, fmt.Errorf("template storage is not configured")
	}

	// Fetch to a temporary directory
	cachePath, err := config.FetchRemoteTemplate(ctx, sourceURL)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch remote templates: %w", err)
	}
	defer os.RemoveAll(cachePath)

	// Collect template directories to import
	type templateDir struct{ name, path string }
	var dirs []templateDir

	if templateimport.IsScionTemplate(cachePath) {
		// URL pointed directly at a single template directory
		dirs = append(dirs, templateDir{filepath.Base(cachePath), cachePath})
	} else {
		entries, err := os.ReadDir(cachePath)
		if err != nil {
			return nil, err
		}
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			dir := filepath.Join(cachePath, entry.Name())
			if templateimport.IsScionTemplate(dir) {
				dirs = append(dirs, templateDir{entry.Name(), dir})
			}
		}
	}

	if len(dirs) == 0 {
		return nil, fmt.Errorf("no scion templates found at %s", sourceURL)
	}

	var imported []string
	for _, td := range dirs {
		slug := api.Slugify(td.name)
		existing, err := s.store.GetTemplateBySlug(ctx, slug, store.TemplateScopeGrove, groveID)
		if err != nil && err != store.ErrNotFound {
			s.templateLog.Warn("template import: failed to look up template, skipping",
				"name", td.name, "error", err)
			continue
		}
		if existing == nil {
			if err := s.bootstrapSingleTemplate(ctx, td.name, td.path, store.TemplateScopeGrove, groveID); err != nil {
				s.templateLog.Warn("template import: failed to import template, skipping",
					"name", td.name, "error", err)
				continue
			}
		} else {
			if _, err := s.syncExistingTemplate(ctx, existing, td.path); err != nil {
				s.templateLog.Warn("template import: failed to sync template, skipping",
					"name", td.name, "error", err)
				continue
			}
		}
		imported = append(imported, td.name)
	}
	return imported, nil
}

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
	"log/slog"
	"os"
	"path/filepath"

	"github.com/GoogleCloudPlatform/scion/pkg/api"
	"github.com/GoogleCloudPlatform/scion/pkg/storage"
	"github.com/GoogleCloudPlatform/scion/pkg/store"
	"github.com/GoogleCloudPlatform/scion/pkg/transfer"
)

// BootstrapSkillsFromDir imports or updates local skills from a directory
// into the Hub's database and storage. Each subdirectory containing a SKILL.md
// file is treated as a skill. On first run it imports all skills; on subsequent
// runs it detects changed skills (by content hash) and re-uploads only those
// that differ from the database version.
//
// This follows the same pattern as BootstrapTemplatesFromDir.
func (s *Server) BootstrapSkillsFromDir(ctx context.Context, skillsDir string) error {
	log := slog.Default().With("subsystem", "hub.skills")

	// Check if the directory exists
	info, err := os.Stat(skillsDir)
	if err != nil || !info.IsDir() {
		log.Debug("skill bootstrap: directory not found, skipping", "dir", skillsDir)
		return nil
	}

	// Check that storage is configured
	stor := s.GetStorage()
	if stor == nil {
		log.Warn("skill bootstrap: no storage backend configured, skipping")
		return nil
	}

	// Scan the directory for skill subdirectories
	entries, err := os.ReadDir(skillsDir)
	if err != nil {
		return err
	}

	imported, updated := 0, 0
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		name := entry.Name()
		skillPath := filepath.Join(skillsDir, name)

		// Verify this directory contains a SKILL.md (skip non-skill dirs)
		if _, err := os.Stat(filepath.Join(skillPath, "SKILL.md")); err != nil {
			log.Debug("skill bootstrap: no SKILL.md found, skipping", "dir", name)
			continue
		}

		// Check if this skill already exists in the database
		existing, err := s.store.GetSkillByName(ctx, name, store.SkillScopeGlobal, "")
		if err != nil && err != store.ErrNotFound {
			log.Warn("skill bootstrap: failed to look up skill, skipping",
				"skill", name, "error", err)
			continue
		}

		if existing == nil {
			// New skill — import it
			if err := s.bootstrapSingleSkill(ctx, name, skillPath, stor, log); err != nil {
				log.Warn("skill bootstrap: failed to import skill, skipping",
					"skill", name, "error", err)
				continue
			}
			imported++
		} else {
			// Existing skill — check if local files have changed
			changed, err := s.syncExistingSkill(ctx, existing, skillPath, stor, log)
			if err != nil {
				log.Warn("skill bootstrap: failed to sync skill, skipping",
					"skill", name, "error", err)
				continue
			}
			if changed {
				updated++
			}
		}
	}

	if imported > 0 || updated > 0 {
		log.Info("skill bootstrap: sync complete",
			"imported", imported, "updated", updated)
	}

	return nil
}

// bootstrapSingleSkill imports one local skill directory into the Hub's
// database and storage backend as a global-scope skill with version 1.0.0.
func (s *Server) bootstrapSingleSkill(ctx context.Context, name, skillPath string, stor storage.Storage, log *slog.Logger) error {
	// Collect files from the skill directory
	files, err := transfer.CollectFiles(skillPath, nil)
	if err != nil {
		return err
	}

	skillID := api.NewUUID()
	storagePath := storage.SkillStoragePath(store.SkillScopeGlobal, "", name)

	// Create the skill record
	skill := &store.Skill{
		ID:          skillID,
		Name:        name,
		DisplayName: name,
		Scope:       store.SkillScopeGlobal,
		Status:      store.SkillStatusActive,
	}

	if err := s.store.CreateSkill(ctx, skill); err != nil {
		return err
	}

	// Upload each file to storage and build file manifest
	var skillFiles []store.SkillFile
	versionPath := storagePath + "/1.0.0"
	for _, fi := range files {
		objectPath := versionPath + "/" + fi.Path

		f, err := os.Open(fi.FullPath)
		if err != nil {
			log.Warn("skill bootstrap: failed to open file, skipping",
				"file", fi.Path, "error", err)
			continue
		}

		_, err = stor.Upload(ctx, objectPath, f, storage.UploadOptions{})
		f.Close()
		if err != nil {
			log.Warn("skill bootstrap: failed to upload file, skipping",
				"file", fi.Path, "error", err)
			continue
		}

		skillFiles = append(skillFiles, store.SkillFile{
			Path: fi.Path,
			Size: fi.Size,
			Hash: fi.Hash,
			Mode: fi.Mode,
		})
	}

	// Create version 1.0.0
	contentHash := computeSkillContentHash(skillFiles)
	version := &store.SkillVersion{
		ID:          api.NewUUID(),
		SkillID:     skillID,
		Version:     "1.0.0",
		ContentHash: contentHash,
		Files:       skillFiles,
		Status:      store.SkillVersionStatusPublished,
	}

	if err := s.store.CreateSkillVersion(ctx, version); err != nil {
		return err
	}

	// Update skill with latest version
	skill.LatestVersion = "1.0.0"
	if err := s.store.UpdateSkill(ctx, skill); err != nil {
		return err
	}

	log.Info("skill bootstrap: imported skill",
		"name", name, "files", len(skillFiles), "version", "1.0.0")

	return nil
}

// syncExistingSkill checks whether the local skill files have changed compared
// to the stored version. If they differ, it re-uploads all files and updates
// the existing version record. Returns true if the content changed.
func (s *Server) syncExistingSkill(ctx context.Context, existing *store.Skill, skillPath string, stor storage.Storage, log *slog.Logger) (bool, error) {
	// Collect current files from disk
	files, err := transfer.CollectFiles(skillPath, nil)
	if err != nil {
		return false, err
	}

	// Build file manifest for comparison
	var preview []store.SkillFile
	for _, fi := range files {
		preview = append(preview, store.SkillFile{
			Path: fi.Path,
			Size: fi.Size,
			Hash: fi.Hash,
			Mode: fi.Mode,
		})
	}
	newHash := computeSkillContentHash(preview)

	// Check if the latest version has the same hash
	if existing.LatestVersion != "" {
		latestVersion, err := s.store.GetSkillVersionByNumber(ctx, existing.ID, existing.LatestVersion)
		if err == nil && latestVersion.ContentHash == newHash {
			return false, nil // No changes
		}
	}

	// Content changed — re-upload files
	storagePath := storage.SkillVersionStoragePath(store.SkillScopeGlobal, "", existing.Name, "1.0.0")
	var skillFiles []store.SkillFile
	for _, fi := range files {
		objectPath := storagePath + "/" + fi.Path

		f, err := os.Open(fi.FullPath)
		if err != nil {
			log.Warn("skill bootstrap: failed to open file, skipping",
				"file", fi.Path, "error", err)
			continue
		}

		_, err = stor.Upload(ctx, objectPath, f, storage.UploadOptions{})
		f.Close()
		if err != nil {
			log.Warn("skill bootstrap: failed to upload file, skipping",
				"file", fi.Path, "error", err)
			continue
		}

		skillFiles = append(skillFiles, store.SkillFile{
			Path: fi.Path,
			Size: fi.Size,
			Hash: fi.Hash,
			Mode: fi.Mode,
		})
	}

	contentHash := computeSkillContentHash(skillFiles)

	// Update or create version 1.0.0
	existingVersion, err := s.store.GetSkillVersionByNumber(ctx, existing.ID, "1.0.0")
	if err == nil {
		existingVersion.Files = skillFiles
		existingVersion.ContentHash = contentHash
		// SkillVersion doesn't have an Update method — we only check for changes
		// and the version is immutable once published, so we log and skip.
		log.Info("skill bootstrap: skill content changed but version already exists",
			"skill", existing.Name, "version", "1.0.0",
			"oldHash", existingVersion.ContentHash, "newHash", contentHash)
	} else {
		// Version doesn't exist yet — create it
		version := &store.SkillVersion{
			ID:          api.NewUUID(),
			SkillID:     existing.ID,
			Version:     "1.0.0",
			ContentHash: contentHash,
			Files:       skillFiles,
			Status:      store.SkillVersionStatusPublished,
		}
		if err := s.store.CreateSkillVersion(ctx, version); err != nil {
			return false, err
		}
		existing.LatestVersion = "1.0.0"
		if err := s.store.UpdateSkill(ctx, existing); err != nil {
			return false, err
		}
	}

	log.Info("skill bootstrap: skill re-synced",
		"skill", existing.Name)

	return true, nil
}

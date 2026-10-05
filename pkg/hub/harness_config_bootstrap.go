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
	"os"
	"path/filepath"

	"github.com/GoogleCloudPlatform/scion/pkg/api"
	"github.com/GoogleCloudPlatform/scion/pkg/config"
	"github.com/GoogleCloudPlatform/scion/pkg/store"
)

// BootstrapHarnessConfigsFromDir imports or updates local harness configs from
// a directory into the Hub's database and storage. On first run it imports all
// configs; on subsequent runs it detects changed configs (by content hash) and
// re-uploads only those that differ from the database version.
func (s *Server) BootstrapHarnessConfigsFromDir(ctx context.Context, harnessConfigsDir string) error {
	info, err := os.Stat(harnessConfigsDir)
	if err != nil || !info.IsDir() {
		s.resourceLog.Debug("harness config bootstrap: directory not found, skipping", "dir", harnessConfigsDir)
		return nil
	}

	if s.GetStorage() == nil {
		s.resourceLog.Warn("harness config bootstrap: no storage backend configured, skipping")
		return nil
	}

	entries, err := os.ReadDir(harnessConfigsDir)
	if err != nil {
		return err
	}

	imported, updated := 0, 0
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		name := entry.Name()
		dirPath := filepath.Join(harnessConfigsDir, name)
		slug := api.Slugify(name)

		// Load config.yaml to get harness type
		hcDir, err := config.LoadHarnessConfigDir(dirPath)
		if err != nil {
			s.resourceLog.Warn("harness config bootstrap: failed to load config, skipping",
				"config", name, "error", err)
			continue
		}

		existing, err := s.store.GetHarnessConfigBySlug(ctx, slug, store.HarnessConfigScopeGlobal, "")
		if err != nil && err != store.ErrNotFound {
			s.resourceLog.Warn("harness config bootstrap: failed to look up config, skipping",
				"config", name, "error", err)
			continue
		}

		if existing == nil {
			if err := s.bootstrapSingleHarnessConfig(ctx, name, dirPath, hcDir, store.HarnessConfigScopeGlobal, ""); err != nil {
				s.resourceLog.Warn("harness config bootstrap: failed to import config, skipping",
					"config", name, "error", err)
				continue
			}
			imported++
		} else {
			oldHash := existing.ContentHash
			changed, err := s.syncExistingHarnessConfig(ctx, existing, dirPath, hcDir, false)
			if err != nil {
				s.resourceLog.Warn("harness config bootstrap: failed to sync config, skipping",
					"config", name, "error", err)
				continue
			}
			if changed {
				updated++
				s.warnBootstrapOverwrite("harness-config", name, existing.ID, dirPath, oldHash,
					s.currentHarnessConfigHash(ctx, existing.ID))
			}
		}
	}

	if imported > 0 || updated > 0 {
		s.resourceLog.Info("harness config bootstrap: sync complete",
			"imported", imported, "updated", updated)
	}

	return nil
}

// bootstrapSingleHarnessConfig imports one local harness config directory into
// the Hub's database and storage backend through the shared ResourceStore.
func (s *Server) bootstrapSingleHarnessConfig(ctx context.Context, name, dirPath string, hcDir *config.HarnessConfigDir, scope, scopeID string) error {
	_, err := s.harnessConfigStore(hcDir.Config.Harness).Bootstrap(ctx, name, dirPath, scope, scopeID, "", false)
	return err
}

// isHarnessConfigDir reports whether dir looks like a harness-config directory,
// i.e. it contains a config.yaml file. Analogous to
// templateimport.IsScionTemplate (which checks for scion-agent.yaml).
func isHarnessConfigDir(dir string) bool {
	info, err := os.Stat(filepath.Join(dir, "config.yaml"))
	return err == nil && !info.IsDir()
}

// syncExistingHarnessConfig re-syncs a local harness config directory through
// the shared ResourceStore. Returns true if the stored content changed. When
// force is true the config is re-uploaded and storage reconciled even if the
// content hash is unchanged (used by direct imports).
func (s *Server) syncExistingHarnessConfig(ctx context.Context, existing *store.HarnessConfig, dirPath string, hcDir *config.HarnessConfigDir, force bool) (bool, error) {
	return s.harnessConfigStore(hcDir.Config.Harness).Bootstrap(ctx, existing.Name, dirPath, existing.Scope, existing.ScopeID, "", force)
}

// warnBootstrapOverwrite reports that the workstation (non-hosted) startup
// bootstrap replaced an existing hub record's content with the local
// ~/.scion copy. Any edit made through the hub (web UI, API) since the last
// import is lost at this point, so this is a WARN, not routine info
// (ptone/scion#611, workstation-mode overwrite).
func (s *Server) warnBootstrapOverwrite(kind, name, id, dir, oldHash, newHash string) {
	s.resourceLog.Warn("workstation bootstrap: hub record overwritten from local disk copy; "+
		"hub-side edits to this "+kind+" were replaced",
		"kind", kind, "name", name, "id", id, "dir", dir,
		"oldHash", oldHash, "newHash", newHash)
}

// currentHarnessConfigHash re-reads a harness-config's stored content hash
// (for logging); returns "" when it cannot be read.
func (s *Server) currentHarnessConfigHash(ctx context.Context, id string) string {
	hc, err := s.store.GetHarnessConfig(ctx, id)
	if err != nil || hc == nil {
		return ""
	}
	return hc.ContentHash
}

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
	"strings"

	"github.com/GoogleCloudPlatform/scion/pkg/store"
)

// flagExtraReposForRemovedRepos marks any stored extra-repo entries on project
// that match removedRepos (keyed by lowercased owner/repo) with
// RemovedFromInstallationAt via a single-column store write. Entries are never
// deleted.
func (s *Server) flagExtraReposForRemovedRepos(ctx context.Context, project *store.Project, installationID int64, removedRepos map[string]bool) {
	if project == nil || project.ID == "" || len(project.GitHubExtraRepos) == 0 || len(removedRepos) == 0 {
		return
	}

	var matched []string
	for _, entry := range project.GitHubExtraRepos {
		if removedRepos[strings.ToLower(entry.Repo)] && entry.RemovedFromInstallationAt == nil {
			matched = append(matched, entry.Repo)
		}
	}
	if len(matched) == 0 {
		return
	}

	flagged, err := s.store.MarkProjectGitHubExtraReposRemoved(ctx, project.ID, matched, timeNow().UTC())
	if err != nil {
		slog.WarnContext(ctx, "Failed to flag removed extra repos on project",
			"project_id", project.ID, "installation_id", installationID, "error", err)
		return
	}
	if flagged > 0 {
		s.githubTokenCache.invalidateProject(project.ID)
		slog.InfoContext(ctx, "Flagged extra read-only repos removed from GitHub App installation",
			"project_id", project.ID, "installation_id", installationID, "flagged", flagged)
	}
}

// clearExtraReposForAddedRepos clears RemovedFromInstallationAt on any project
// associated with installationID whose stored extra-repo entries match the
// newly added repositories.
func (s *Server) clearExtraReposForAddedRepos(ctx context.Context, installationID int64, addedRepos []string) {
	if len(addedRepos) == 0 {
		return
	}
	addedSet := make(map[string]bool, len(addedRepos))
	for _, r := range addedRepos {
		if trimmed := strings.TrimSpace(r); trimmed != "" {
			addedSet[strings.ToLower(trimmed)] = true
		}
	}
	if len(addedSet) == 0 {
		return
	}

	if err := s.forEachGitHubAppProject(ctx, func(project *store.Project) {
		if project.GitHubInstallationID == nil || *project.GitHubInstallationID != installationID {
			return
		}
		if len(project.GitHubExtraRepos) == 0 {
			return
		}
		var matched []string
		for _, entry := range project.GitHubExtraRepos {
			if entry.RemovedFromInstallationAt != nil && addedSet[strings.ToLower(entry.Repo)] {
				matched = append(matched, entry.Repo)
			}
		}
		if len(matched) == 0 {
			return
		}
		cleared, err := s.store.ClearProjectGitHubExtraReposRemoved(ctx, project.ID, matched)
		if err != nil {
			slog.WarnContext(ctx, "Failed to clear removed flag on re-added extra repos",
				"project_id", project.ID, "installation_id", installationID, "error", err)
			return
		}
		if cleared > 0 {
			s.githubTokenCache.invalidateProject(project.ID)
		}
	}); err != nil {
		slog.WarnContext(ctx, "Failed to list projects for re-added extra repos check",
			"installation_id", installationID, "error", err)
	}
}

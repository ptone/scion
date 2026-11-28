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
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/GoogleCloudPlatform/scion/pkg/config"
	"github.com/GoogleCloudPlatform/scion/pkg/experiments"
	"github.com/GoogleCloudPlatform/scion/pkg/hub/githubapp"
	"github.com/GoogleCloudPlatform/scion/pkg/store"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

const (
	// maxGitHubExtraRepos is the maximum number of extra read-only repositories
	// a project may configure (M3 design OQ4).
	maxGitHubExtraRepos = 20

	// Error codes for extra read-only repositories.
	ErrCodeExtraReposDisabled    = "extra_repos_disabled"
	ErrCodeOwnerMismatch         = "owner_mismatch"
	ErrCodeRepoNotInInstallation = "repo_not_in_installation"
	ErrCodeNotExtraRepo          = "not_extra_repo"

	// Status values computed at read and mint time for each extra repo entry.
	extraRepoStatusOK                      = "ok"
	extraRepoStatusNotInInstallation       = "not_in_installation"
	extraRepoStatusOwnerMismatch           = "owner_mismatch"
	extraRepoStatusRemovedFromInstallation = "removed_from_installation"

	// Mutation audit event type for extra-repos updates.
	mutationTypeProjectGitHubExtraReposUpdate = "project.github_extra_repos.update"

	// Metric name for extra-read token mint attempts.
	MetricGitHubExtraReadMintsTotal = "scion_github_extra_read_mints_total"
)

var (
	// ErrExtraReposDisabled is returned by MintGitHubAppExtraReadToken when the
	// hub.github_extra_repos experiment is off or github_app.extra_repos_policy is "off".
	ErrExtraReposDisabled = errors.New("extra read-only repositories are disabled")

	// ErrNoExtraRepos is returned by MintGitHubAppExtraReadToken when the project
	// has no valid extra read-only repositories for its current installation.
	ErrNoExtraRepos = errors.New("project has no valid extra read-only repositories")

	githubExtraRepoNameRe = regexp.MustCompile(`^[A-Za-z0-9-]+/[A-Za-z0-9._-]+$`)
)

// ProjectGitHubExtraRepoEntry is one entry in the read-model extra_repos block.
type ProjectGitHubExtraRepoEntry struct {
	Repo                      string     `json:"repo"`
	Status                    string     `json:"status"`
	AddedBy                   string     `json:"added_by,omitempty"`
	AddedAt                   time.Time  `json:"added_at"`
	RemovedFromInstallationAt *time.Time `json:"removed_from_installation_at,omitempty"`
}

// ProjectGitHubExtraReposInfo is the extra_repos block of GET /api/v1/projects/{id}/github
// and the response body of PUT/DELETE /api/v1/projects/{id}/github-extra-repos.
type ProjectGitHubExtraReposInfo struct {
	Policy  string                        `json:"policy"`
	Max     int                           `json:"max"`
	Version string                        `json:"version"`
	Repos   []ProjectGitHubExtraRepoEntry `json:"repos"`
}

// GitHubExtraReadMetricsSnapshot is the JSON snapshot of extra-read mint counts
// exposed on GET /metrics.
type GitHubExtraReadMetricsSnapshot struct {
	MintsByResult map[string]int64 `json:"mints_by_result"`
}

// GitHubExtraReadMetrics tracks scion_github_extra_read_mints_total{result} in
// both an in-process snapshot and an OpenTelemetry counter.
type GitHubExtraReadMetrics struct {
	mu      sync.Mutex
	counts  map[string]int64
	once    sync.Once
	counter metric.Int64Counter
}

// NewGitHubExtraReadMetrics constructs a new GitHubExtraReadMetrics recorder.
func NewGitHubExtraReadMetrics() *GitHubExtraReadMetrics {
	return &GitHubExtraReadMetrics{
		counts: make(map[string]int64),
	}
}

func (m *GitHubExtraReadMetrics) ensureOTelCounter() metric.Int64Counter {
	m.once.Do(func() {
		mp := otel.GetMeterProvider()
		if mp == nil {
			return
		}
		c, err := mp.Meter(instrumentationScope).Int64Counter(
			MetricGitHubExtraReadMintsTotal,
			metric.WithUnit("{mint}"),
			metric.WithDescription("Total GitHub App extra read-only installation token mint attempts by result"),
		)
		if err == nil {
			m.counter = c
		}
	})
	return m.counter
}

// Record increments the counter for the given result label.
func (m *GitHubExtraReadMetrics) Record(ctx context.Context, result string) {
	if m == nil {
		return
	}
	m.mu.Lock()
	if m.counts == nil {
		m.counts = make(map[string]int64)
	}
	m.counts[result]++
	m.mu.Unlock()

	if c := m.ensureOTelCounter(); c != nil {
		c.Add(ctx, 1, metric.WithAttributes(attribute.String("result", result)))
	}
}

// GetSnapshot returns a copy of the recorded mint counts by result.
func (m *GitHubExtraReadMetrics) GetSnapshot() *GitHubExtraReadMetricsSnapshot {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	copied := make(map[string]int64, len(m.counts))
	for k, v := range m.counts {
		copied[k] = v
	}
	return &GitHubExtraReadMetricsSnapshot{MintsByResult: copied}
}

// githubExtraReposPolicy returns the normalized hub-level extra-repos policy
// ("project_admin", "hub_admin", or "off").
func (s *Server) githubExtraReposPolicy() string {
	s.mu.RLock()
	raw := s.config.GitHubAppConfig.ExtraReposPolicy
	s.mu.RUnlock()
	return config.NormalizeGitHubExtraReposPolicy(raw)
}

func (s *Server) recordGitHubExtraReadMint(ctx context.Context, result string) {
	if s.githubExtraReadMetrics != nil {
		s.githubExtraReadMetrics.Record(ctx, result)
	}
}

// computeExtraRepoStatus evaluates the current status of a single stored
// extra-repo entry against the project's installation (which may be nil when
// unassociated or missing from the store).
func computeExtraRepoStatus(entry store.GitHubExtraRepo, inst *store.GitHubInstallation, instRepoSet map[string]bool) string {
	if entry.RemovedFromInstallationAt != nil {
		return extraRepoStatusRemovedFromInstallation
	}
	if inst == nil {
		return extraRepoStatusNotInInstallation
	}
	owner, _, ok := strings.Cut(entry.Repo, "/")
	if !ok || !strings.EqualFold(owner, inst.AccountLogin) {
		return extraRepoStatusOwnerMismatch
	}
	if !instRepoSet[strings.ToLower(entry.Repo)] {
		return extraRepoStatusNotInInstallation
	}
	return extraRepoStatusOK
}

func installationRepoSet(inst *store.GitHubInstallation) map[string]bool {
	if inst == nil || len(inst.Repositories) == 0 {
		return nil
	}
	set := make(map[string]bool, len(inst.Repositories))
	for _, r := range inst.Repositories {
		set[strings.ToLower(r)] = true
	}
	return set
}

// buildProjectGitHubExtraReposInfo computes the read-model extra_repos block.
func (s *Server) buildProjectGitHubExtraReposInfo(ctx context.Context, project *store.Project) ProjectGitHubExtraReposInfo {
	var inst *store.GitHubInstallation
	if project != nil && project.GitHubInstallationID != nil {
		if loaded, err := s.store.GetGitHubInstallation(ctx, *project.GitHubInstallationID); err == nil {
			inst = loaded
		}
	}
	repoSet := installationRepoSet(inst)

	entries := make([]ProjectGitHubExtraRepoEntry, 0, len(project.GitHubExtraRepos))
	for _, e := range project.GitHubExtraRepos {
		entries = append(entries, ProjectGitHubExtraRepoEntry{
			Repo:                      e.Repo,
			Status:                    computeExtraRepoStatus(e, inst, repoSet),
			AddedBy:                   e.AddedBy,
			AddedAt:                   e.AddedAt,
			RemovedFromInstallationAt: e.RemovedFromInstallationAt,
		})
	}

	return ProjectGitHubExtraReposInfo{
		Policy:  s.githubExtraReposPolicy(),
		Max:     maxGitHubExtraRepos,
		Version: store.HashGitHubExtraRepos(project.GitHubExtraRepos),
		Repos:   entries,
	}
}

type updateGitHubExtraReposRequest struct {
	Repos           []string `json:"repos"`
	ExpectedVersion string   `json:"expected_version"`
}

// handleProjectGitHubExtraRepos handles PUT and DELETE /api/v1/projects/{id}/github-extra-repos.
func (s *Server) handleProjectGitHubExtraRepos(w http.ResponseWriter, r *http.Request, project *store.Project) {
	if !s.experimentEnabled(experiments.GitHubExtraRepos) {
		NotFound(w, "route")
		return
	}

	if r.Method != http.MethodPut && r.Method != http.MethodDelete {
		MethodNotAllowed(w, http.MethodPut, http.MethodDelete)
		return
	}

	// Policy-based authorization gate (on top of project.update checked by
	// handleProjectGitHubRoutes).
	policy := s.githubExtraReposPolicy()
	switch policy {
	case config.GitHubExtraReposPolicyOff:
		writeError(w, http.StatusForbidden, ErrCodeExtraReposDisabled,
			"extra read-only repositories are disabled by hub policy", nil)
		return
	case config.GitHubExtraReposPolicyHubAdmin:
		if !s.enforceRoutePermission(w, r, githubInstallationBindPermission) {
			return
		}
	case config.GitHubExtraReposPolicyProjectAdmin:
		// project.update was already authorized by handleProjectGitHubRoutes.
	default:
		// Fail closed on any unrecognized policy value.
		writeError(w, http.StatusForbidden, ErrCodeExtraReposDisabled,
			"extra read-only repositories are disabled by hub policy", nil)
		return
	}

	ctx := r.Context()
	actorID := ""
	if user := GetUserIdentityFromContext(ctx); user != nil {
		actorID = user.ID()
	}

	beforeRepos := make([]string, 0, len(project.GitHubExtraRepos))
	for _, e := range project.GitHubExtraRepos {
		beforeRepos = append(beforeRepos, e.Repo)
	}

	var (
		expectedVersion string
		nextEntries     []store.GitHubExtraRepo
		afterRepos      []string
	)

	if r.Method == http.MethodDelete {
		expectedVersion = strings.TrimSpace(r.URL.Query().Get("expected_version"))
		if expectedVersion == "" && r.Body != nil && r.ContentLength != 0 {
			var delReq struct {
				ExpectedVersion string `json:"expected_version"`
			}
			if err := readJSON(r, &delReq); err != nil {
				writeError(w, http.StatusBadRequest, ErrCodeInvalidRequest, "invalid request body", nil)
				return
			}
			expectedVersion = strings.TrimSpace(delReq.ExpectedVersion)
		}
		if expectedVersion == "" {
			expectedVersion = store.HashGitHubExtraRepos(project.GitHubExtraRepos)
		}
		nextEntries = nil
		afterRepos = []string{}
	} else {
		var req updateGitHubExtraReposRequest
		if err := readJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, ErrCodeInvalidRequest, "invalid request body", nil)
			return
		}
		expectedVersion = strings.TrimSpace(req.ExpectedVersion)

		if len(req.Repos) > maxGitHubExtraRepos {
			writeError(w, http.StatusUnprocessableEntity, ErrCodeUnprocessable,
				fmt.Sprintf("at most %d extra repositories are allowed", maxGitHubExtraRepos), nil)
			return
		}

		if project.GitHubInstallationID == nil {
			writeError(w, http.StatusUnprocessableEntity, ErrCodeUnprocessable,
				"project is not associated with a GitHub App installation", nil)
			return
		}
		inst, err := s.store.GetGitHubInstallation(ctx, *project.GitHubInstallationID)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				writeError(w, http.StatusUnprocessableEntity, ErrCodeUnprocessable,
					"project GitHub App installation not found", nil)
				return
			}
			writeError(w, http.StatusInternalServerError, ErrCodeInternalError,
				"failed to load GitHub App installation", nil)
			return
		}

		primaryLower := strings.ToLower(s.githubOwnerRepo(project.GitRemote))
		instRepoSet := installationRepoSet(inst)

		existingByLower := make(map[string]store.GitHubExtraRepo, len(project.GitHubExtraRepos))
		for _, e := range project.GitHubExtraRepos {
			existingByLower[strings.ToLower(e.Repo)] = e
		}

		now := timeNow().UTC().Truncate(time.Microsecond)
		seen := make(map[string]bool, len(req.Repos))
		nextEntries = make([]store.GitHubExtraRepo, 0, len(req.Repos))
		afterRepos = make([]string, 0, len(req.Repos))

		for _, raw := range req.Repos {
			if !githubExtraRepoNameRe.MatchString(raw) {
				writeError(w, http.StatusUnprocessableEntity, ErrCodeUnprocessable,
					fmt.Sprintf("invalid repository %q: expected owner/name", raw), nil)
				return
			}
			lower := strings.ToLower(raw)
			if seen[lower] {
				writeError(w, http.StatusUnprocessableEntity, ErrCodeUnprocessable,
					fmt.Sprintf("duplicate repository %q", raw), nil)
				return
			}
			seen[lower] = true

			if primaryLower != "" && lower == primaryLower {
				writeError(w, http.StatusUnprocessableEntity, ErrCodeUnprocessable,
					fmt.Sprintf("repository %q is already the project's primary repository", raw), nil)
				return
			}

			owner, _, _ := strings.Cut(raw, "/")
			if !strings.EqualFold(owner, inst.AccountLogin) {
				writeError(w, http.StatusUnprocessableEntity, ErrCodeOwnerMismatch,
					fmt.Sprintf("repository %q owner does not match installation account %q", raw, inst.AccountLogin), nil)
				return
			}

			if !instRepoSet[lower] {
				writeError(w, http.StatusUnprocessableEntity, ErrCodeRepoNotInInstallation,
					fmt.Sprintf("repository %q is not accessible by the project's GitHub App installation", raw), nil)
				return
			}

			entry := store.GitHubExtraRepo{
				Repo:    raw,
				AddedBy: actorID,
				AddedAt: now,
			}
			if prev, ok := existingByLower[lower]; ok {
				// Preserve original AddedBy/AddedAt on re-save, while clearing
				// RemovedFromInstallationAt (re-validated against installation).
				entry.AddedBy = prev.AddedBy
				entry.AddedAt = prev.AddedAt
				entry.RemovedFromInstallationAt = nil
			}
			nextEntries = append(nextEntries, entry)
			afterRepos = append(afterRepos, raw)
		}
	}

	if _, err := s.store.UpdateProjectGitHubExtraRepos(ctx, project.ID, expectedVersion, nextEntries); err != nil {
		if errors.Is(err, store.ErrVersionConflict) {
			writeError(w, http.StatusConflict, ErrCodeConflict,
				"extra repositories list was modified concurrently; reload and retry", nil)
			return
		}
		writeErrorFromErr(w, err, "")
		return
	}

	// Update in-memory snapshot for response and event publication.
	project.GitHubExtraRepos = nextEntries
	s.githubTokenCache.invalidateProject(project.ID)
	s.githubAssociationReportCache.invalidate(project.ID)

	// Emit mutation audit record (no tokens).
	s.emitGitHubExtraReposAudit(ctx, project.ID, actorID, beforeRepos, afterRepos)

	if s.events != nil {
		s.events.PublishProjectUpdated(ctx, project)
	}

	writeJSON(w, http.StatusOK, s.buildProjectGitHubExtraReposInfo(ctx, project))
}

func (s *Server) emitGitHubExtraReposAudit(ctx context.Context, projectID, actorID string, beforeRepos, afterRepos []string) {
	beforeJSON, _ := json.Marshal(map[string]any{"repos": beforeRepos})
	afterJSON, _ := json.Marshal(map[string]any{"repos": afterRepos})
	actorKind := "user"
	if actorID == "" {
		actorKind = "system"
	}
	s.emitMutationAudit(ctx, &store.MutationAuditRecord{
		Timestamp:          timeNow().UTC(),
		MutationType:       mutationTypeProjectGitHubExtraReposUpdate,
		ActorPrincipalKind: actorKind,
		ActorPrincipalID:   actorID,
		TargetType:         "project",
		TargetID:           projectID,
		BeforeSummary:      string(beforeJSON),
		AfterSummary:       string(afterJSON),
	})
}

// MintGitHubAppExtraReadToken mints a read-only GitHub App installation token
// scoped strictly to the project's valid extra repositories. It enforces the
// hub.github_extra_repos experiment, the github_app.extra_repos_policy kill
// switch, and the same authorizeGitHubAppAssociation choke as primary minting.
//
// Failures here never mutate project.GitHubAppStatus (the primary token's
// health is the project's health); they are logged and counted via
// scion_github_extra_read_mints_total{result}.
func (s *Server) MintGitHubAppExtraReadToken(ctx context.Context, project *store.Project) (string, string, []string, error) {
	if !s.experimentEnabled(experiments.GitHubExtraRepos) || s.githubExtraReposPolicy() == config.GitHubExtraReposPolicyOff {
		s.recordGitHubExtraReadMint(ctx, "disabled")
		return "", "", nil, ErrExtraReposDisabled
	}

	if project == nil || project.GitHubInstallationID == nil || len(project.GitHubExtraRepos) == 0 {
		s.recordGitHubExtraReadMint(ctx, "no_extra_repos")
		return "", "", nil, ErrNoExtraRepos
	}

	inst, err := s.store.GetGitHubInstallation(ctx, *project.GitHubInstallationID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			s.recordGitHubExtraReadMint(ctx, "no_extra_repos")
			return "", "", nil, ErrNoExtraRepos
		}
		s.recordGitHubExtraReadMint(ctx, "error")
		return "", "", nil, fmt.Errorf("load GitHub installation: %w", err)
	}

	repoSet := installationRepoSet(inst)
	validFull := make([]string, 0, len(project.GitHubExtraRepos))
	bareNames := make([]string, 0, len(project.GitHubExtraRepos))
	seen := make(map[string]bool, len(project.GitHubExtraRepos))
	for _, entry := range project.GitHubExtraRepos {
		if computeExtraRepoStatus(entry, inst, repoSet) != extraRepoStatusOK {
			continue
		}
		lower := strings.ToLower(entry.Repo)
		if seen[lower] {
			continue
		}
		seen[lower] = true
		_, repoName, ok := strings.Cut(entry.Repo, "/")
		if !ok || repoName == "" {
			continue
		}
		validFull = append(validFull, entry.Repo)
		bareNames = append(bareNames, repoName)
	}
	if len(validFull) == 0 {
		s.recordGitHubExtraReadMint(ctx, "no_extra_repos")
		return "", "", nil, ErrNoExtraRepos
	}

	// Intersect {contents: read, metadata: read} with the App grant.
	// Fail closed if App permissions are unavailable or contents is not granted.
	id, hasID := s.GitHubAppIdentity(ctx)
	if !hasID || id == nil {
		const msg = "Unable to verify GitHub App permissions. See the hub log for details."
		slog.WarnContext(ctx, "GitHub App extra-read token refused: app permissions unavailable",
			"project_id", project.ID, "installation_id", *project.GitHubInstallationID)
		s.recordGitHubExtraReadMint(ctx, "error")
		return "", "", nil, &githubapp.TokenMintError{
			ErrorCode: githubapp.ErrCodeTokenMintFailed,
			Message:   msg,
		}
	}
	if githubPermLevelRank(id.Permissions["contents"]) < githubPermLevelRank("read") {
		const msg = "GitHub App does not have contents:read permission required for extra read-only repositories."
		slog.WarnContext(ctx, "GitHub App extra-read token refused: contents not granted on App",
			"project_id", project.ID, "installation_id", *project.GitHubInstallationID)
		s.recordGitHubExtraReadMint(ctx, "permission_denied")
		return "", "", nil, &githubapp.TokenMintError{
			ErrorCode: githubapp.ErrCodePermissionDenied,
			Message:   msg,
		}
	}
	perms := githubapp.TokenPermissions{
		Contents: "read",
	}
	if githubPermLevelRank(id.Permissions["metadata"]) >= githubPermLevelRank("read") {
		perms.Metadata = "read"
	}

	// Sort bare names deterministically for cache and API consistency.
	sortedBare := slices.Clone(bareNames)
	sort.Slice(sortedBare, func(i, j int) bool {
		return strings.ToLower(sortedBare[i]) < strings.ToLower(sortedBare[j])
	})

	tok, exp, _, err := s.mintGitHubAppTokenInternal(ctx, project, &perms, true, sortedBare, true)
	if err != nil {
		var mintErr *githubapp.TokenMintError
		result := "error"
		if isTokenMintError(err, &mintErr) {
			result = mintErr.ErrorCode
		}
		slog.WarnContext(ctx, "GitHub App extra-read token mint failed",
			"project_id", project.ID, "installation_id", *project.GitHubInstallationID,
			"repo_count", len(validFull), "result", result)
		s.recordGitHubExtraReadMint(ctx, result)
		return "", "", nil, err
	}

	s.recordGitHubExtraReadMint(ctx, "ok")
	return tok, exp, validFull, nil
}

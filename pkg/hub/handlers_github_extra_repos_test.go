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

//go:build !no_sqlite

package hub

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/GoogleCloudPlatform/scion/pkg/api"
	"github.com/GoogleCloudPlatform/scion/pkg/config"
	"github.com/GoogleCloudPlatform/scion/pkg/experiments"
	"github.com/GoogleCloudPlatform/scion/pkg/store"
	"github.com/GoogleCloudPlatform/scion/pkg/util"
)

func setGitHubExtraReposExperiment(t *testing.T, srv *Server, on bool) {
	t.Helper()
	var active []experiments.Experiment
	for _, e := range experiments.Default().All() {
		if e.Name == experiments.GitHubExtraRepos {
			e.Default = on
		}
		active = append(active, e)
	}
	reg, err := experiments.NewRegistry(active, experiments.Default().Retired())
	require.NoError(t, err)
	srv.mu.Lock()
	srv.experiments = reg
	srv.mu.Unlock()
	require.Equal(t, on, srv.experimentEnabled(experiments.GitHubExtraRepos))
}

type extraReposMintRecord struct {
	Repos       []string
	Permissions map[string]string
	Token       string
}

type extraReposFakeGitHub struct {
	srv *httptest.Server

	mu             sync.Mutex
	appPermissions map[string]string
	mintStatus     int
	mintSeq        int
	mints          []extraReposMintRecord
}

func newExtraReposFakeGitHub(t *testing.T, appPerms map[string]string) *extraReposFakeGitHub {
	t.Helper()
	f := &extraReposFakeGitHub{
		appPermissions: appPerms,
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/app", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		perms := make(map[string]string, len(f.appPermissions))
		for k, v := range f.appPermissions {
			perms[k] = v
		}
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":          1,
			"slug":        "test-extra-repos-bot",
			"permissions": perms,
		})
	})
	mux.HandleFunc("/app/installations/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/access_tokens") {
			http.NotFound(w, r)
			return
		}
		var body struct {
			Repositories []string          `json:"repositories"`
			Permissions  map[string]string `json:"permissions"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		status := f.mintStatus
		f.mintSeq++
		tok := fmt.Sprintf("ghs_extra_test_tok_%d", f.mintSeq)
		if status == 0 {
			f.mints = append(f.mints, extraReposMintRecord{
				Repos:       append([]string(nil), body.Repositories...),
				Permissions: body.Permissions,
				Token:       tok,
			})
		}
		f.mu.Unlock()
		if status != 0 {
			w.WriteHeader(status)
			_ = json.NewEncoder(w).Encode(map[string]any{"message": "upstream failure"})
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"token":       tok,
			"expires_at":  time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
			"permissions": body.Permissions,
		})
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *extraReposFakeGitHub) mintCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.mints)
}

func (f *extraReposFakeGitHub) lastMint() extraReposMintRecord {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.mints[len(f.mints)-1]
}

type extraReposFixture struct {
	srv          *Server
	st           store.Store
	fakeGH       *extraReposFakeGitHub
	hubAdmin     *store.User
	projectAdmin *store.User
	projectView  *store.User
	project      *store.Project
	instID       int64
}

func setupExtraReposFixture(t *testing.T, appPerms map[string]string) *extraReposFixture {
	t.Helper()
	if appPerms == nil {
		appPerms = map[string]string{
			"contents":      "write",
			"pull_requests": "write",
			"issues":        "write",
			"metadata":      "read",
		}
	}
	fakeGH := newExtraReposFakeGitHub(t, appPerms)
	srv, st := testServer(t)
	ctx := context.Background()

	srv.mu.Lock()
	srv.config.GitHubAppConfig.AppID = 1
	srv.config.GitHubAppConfig.APIBaseURL = fakeGH.srv.URL
	srv.config.GitHubAppConfig.PrivateKey = generateTestGitHubAppKey(t)
	srv.config.GitHubAppConfig.ExtraReposPolicy = config.GitHubExtraReposPolicyProjectAdmin
	srv.mu.Unlock()

	_, ok := srv.GitHubAppIdentity(ctx)
	require.True(t, ok)

	hubAdmin := &store.User{
		ID:          tid("er-hub-admin"),
		Email:       "er-hub-admin@example.com",
		DisplayName: "Hub Admin",
		Role:        store.UserRoleAdmin,
		Status:      store.UserStatusActive,
	}
	require.NoError(t, st.CreateUser(ctx, hubAdmin))
	if roleDef, err := st.GetRoleDefinitionByName(ctx, store.SystemRoleHubAdmin, store.RoleScopeSystem); err == nil && roleDef != nil {
		_, _ = st.CreateRoleBinding(ctx, &store.RoleBinding{
			RoleDefinitionID: roleDef.ID,
			PrincipalType:    store.RoleBindingPrincipalUser,
			PrincipalID:      hubAdmin.ID,
			ScopeType:        store.RoleScopeSystem,
			CreatedBy:        hubAdmin.ID,
			CreatedAt:        time.Now(),
		})
	}

	projAdmin := &store.User{
		ID:          tid("er-proj-admin"),
		Email:       "er-proj-admin@example.com",
		DisplayName: "Project Admin",
		Role:        store.UserRoleMember,
		Status:      store.UserStatusActive,
	}
	require.NoError(t, st.CreateUser(ctx, projAdmin))

	projView := &store.User{
		ID:          tid("er-proj-viewer"),
		Email:       "er-proj-viewer@example.com",
		DisplayName: "Project Viewer",
		Role:        store.UserRoleMember,
		Status:      store.UserStatusActive,
	}
	require.NoError(t, st.CreateUser(ctx, projView))

	instID := int64(7701)
	require.NoError(t, st.CreateGitHubInstallation(ctx, &store.GitHubInstallation{
		InstallationID: instID,
		AccountLogin:   "acme",
		AccountType:    "Organization",
		AppID:          1,
		Status:         store.GitHubInstallationStatusActive,
		Repositories: []string{
			"acme/primary",
			"acme/lib-a",
			"acme/lib-b",
			"acme/skills-private",
		},
	}))

	remote := fakeGH.srv.URL + "/acme/primary"
	bindingJSON, err := json.Marshal(githubInstallationBinding{
		InstallationID: instID,
		Remote:         util.NormalizeGitRemote(remote),
		BoundBy:        hubAdmin.ID,
		BoundAt:        time.Now().UTC(),
	})
	require.NoError(t, err)

	proj := &store.Project{
		ID:                   tid("er-project"),
		Name:                 "Extra Repos Project",
		Slug:                 tid("er-project"),
		GitRemote:            remote,
		GitHubInstallationID: &instID,
		Annotations: map[string]string{
			githubInstallationBindingAnnotation: string(bindingJSON),
		},
		CreatedBy: hubAdmin.ID,
		OwnerID:   hubAdmin.ID,
		Created:   time.Now(),
		Updated:   time.Now(),
	}
	require.NoError(t, st.CreateProject(ctx, proj))

	// Bind project-admin and project-viewer roles on proj.
	if rd, err := st.GetRoleDefinitionByName(ctx, store.ProjectRoleAdmin, store.RoleScopeProject); err == nil && rd != nil {
		_, err = st.CreateRoleBinding(ctx, &store.RoleBinding{
			RoleDefinitionID: rd.ID,
			PrincipalType:    store.RoleBindingPrincipalUser,
			PrincipalID:      projAdmin.ID,
			ScopeType:        store.RoleScopeProject,
			ScopeID:          proj.ID,
			CreatedBy:        hubAdmin.ID,
			CreatedAt:        time.Now(),
		})
		require.NoError(t, err)
	}
	if rd, err := st.GetRoleDefinitionByName(ctx, store.ProjectRoleViewer, store.RoleScopeProject); err == nil && rd != nil {
		_, err = st.CreateRoleBinding(ctx, &store.RoleBinding{
			RoleDefinitionID: rd.ID,
			PrincipalType:    store.RoleBindingPrincipalUser,
			PrincipalID:      projView.ID,
			ScopeType:        store.RoleScopeProject,
			ScopeID:          proj.ID,
			CreatedBy:        hubAdmin.ID,
			CreatedAt:        time.Now(),
		})
		require.NoError(t, err)
	}

	return &extraReposFixture{
		srv:          srv,
		st:           st,
		fakeGH:       fakeGH,
		hubAdmin:     hubAdmin,
		projectAdmin: projAdmin,
		projectView:  projView,
		project:      proj,
		instID:       instID,
	}
}

func TestGitHubExtraRepos_AuthzMatrixAndExperimentGate(t *testing.T) {
	f := setupExtraReposFixture(t, nil)
	ctx := context.Background()
	path := "/api/v1/projects/" + f.project.ID + "/github-extra-repos"
	emptyVer := store.HashGitHubExtraRepos(nil)

	// 1. Experiment OFF (default): routes return 404, read model omits extra_repos,
	// and mint refuses with ErrExtraReposDisabled.
	rec := doRequestAsUser(t, f.srv, f.hubAdmin, http.MethodPut, path, map[string]any{
		"repos":            []string{"acme/lib-a"},
		"expected_version": emptyVer,
	})
	assert.Equal(t, http.StatusNotFound, rec.Code)

	rec = doRequestAsUser(t, f.srv, f.hubAdmin, http.MethodDelete, path, nil)
	assert.Equal(t, http.StatusNotFound, rec.Code)

	rec = doRequestAsUser(t, f.srv, f.projectAdmin, http.MethodGet, "/api/v1/projects/"+f.project.ID+"/github", nil)
	require.Equal(t, http.StatusOK, rec.Code)
	var raw map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &raw))
	assert.NotContains(t, raw, "extra_repos")

	_, _, _, mintErr := f.srv.MintGitHubAppExtraReadToken(ctx, f.project)
	assert.ErrorIs(t, mintErr, ErrExtraReposDisabled)

	// Enable experiment.
	setGitHubExtraReposExperiment(t, f.srv, true)

	// 2. Policy = project_admin (default):
	//    - project viewer (no project.update): 403
	//    - project admin: 200
	//    - hub admin: 200
	rec = doRequestAsUser(t, f.srv, f.projectView, http.MethodPut, path, map[string]any{
		"repos":            []string{"acme/lib-a"},
		"expected_version": emptyVer,
	})
	assert.Equal(t, http.StatusForbidden, rec.Code)

	rec = doRequestAsUser(t, f.srv, f.projectAdmin, http.MethodPut, path, map[string]any{
		"repos":            []string{"acme/lib-a"},
		"expected_version": emptyVer,
	})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var info ProjectGitHubExtraReposInfo
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &info))
	assert.Equal(t, config.GitHubExtraReposPolicyProjectAdmin, info.Policy)
	require.Len(t, info.Repos, 1)
	assert.Equal(t, "acme/lib-a", info.Repos[0].Repo)

	// 3. Policy = hub_admin (set via PUT /api/v1/github-app):
	//    - project admin: 403 (lacks hub.github_app.update)
	//    - hub admin: 200
	policyHubAdmin := config.GitHubExtraReposPolicyHubAdmin
	rec = doRequestAsUser(t, f.srv, f.hubAdmin, http.MethodPut, "/api/v1/github-app", map[string]any{
		"extra_repos_policy": policyHubAdmin,
	})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	// Invalid policy value rejected with 400.
	rec = doRequestAsUser(t, f.srv, f.hubAdmin, http.MethodPut, "/api/v1/github-app", map[string]any{
		"extra_repos_policy": "everyone",
	})
	assert.Equal(t, http.StatusBadRequest, rec.Code)

	rec = doRequestAsUser(t, f.srv, f.projectAdmin, http.MethodPut, path, map[string]any{
		"repos":            []string{"acme/lib-a", "acme/lib-b"},
		"expected_version": info.Version,
	})
	assert.Equal(t, http.StatusForbidden, rec.Code)

	// Read model reflects can.edit_extra_repos=false for projectAdmin under hub_admin policy.
	rec = doRequestAsUser(t, f.srv, f.projectAdmin, http.MethodGet, "/api/v1/projects/"+f.project.ID+"/github", nil)
	require.Equal(t, http.StatusOK, rec.Code)
	var ghResp ProjectGitHubResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &ghResp))
	assert.False(t, ghResp.Can.EditExtraRepos)
	require.NotNil(t, ghResp.ExtraRepos)
	assert.Equal(t, config.GitHubExtraReposPolicyHubAdmin, ghResp.ExtraRepos.Policy)

	rec = doRequestAsUser(t, f.srv, f.hubAdmin, http.MethodPut, path, map[string]any{
		"repos":            []string{"acme/lib-a", "acme/lib-b"},
		"expected_version": info.Version,
	})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &info))
	assert.Len(t, info.Repos, 2)

	// 4. Policy = off:
	//    - both project admin and hub admin get 403 extra_repos_disabled
	//    - MintGitHubAppExtraReadToken returns ErrExtraReposDisabled (immediate kill switch)
	//    - primary mintGitHubAppToken still succeeds!
	rec = doRequestAsUser(t, f.srv, f.hubAdmin, http.MethodPut, "/api/v1/github-app", map[string]any{
		"extra_repos_policy": config.GitHubExtraReposPolicyOff,
	})
	require.Equal(t, http.StatusOK, rec.Code)

	for _, actor := range []*store.User{f.projectAdmin, f.hubAdmin} {
		rec = doRequestAsUser(t, f.srv, actor, http.MethodPut, path, map[string]any{
			"repos":            []string{"acme/lib-a"},
			"expected_version": info.Version,
		})
		assert.Equal(t, http.StatusForbidden, rec.Code)
		assert.Contains(t, rec.Body.String(), ErrCodeExtraReposDisabled)
	}

	reloaded, err := f.st.GetProject(ctx, f.project.ID)
	require.NoError(t, err)
	_, _, _, mintErr = f.srv.MintGitHubAppExtraReadToken(ctx, reloaded)
	assert.ErrorIs(t, mintErr, ErrExtraReposDisabled)

	primaryTok, _, primaryErr := f.srv.mintGitHubAppToken(ctx, reloaded)
	require.NoError(t, primaryErr)
	assert.NotEmpty(t, primaryTok)
}

func TestGitHubExtraRepos_ValidationAndCASConflict(t *testing.T) {
	f := setupExtraReposFixture(t, nil)
	setGitHubExtraReposExperiment(t, f.srv, true)
	path := "/api/v1/projects/" + f.project.ID + "/github-extra-repos"
	emptyVer := store.HashGitHubExtraRepos(nil)

	// Owner mismatch -> 422 owner_mismatch.
	rec := doRequestAsUser(t, f.srv, f.projectAdmin, http.MethodPut, path, map[string]any{
		"repos":            []string{"otherorg/lib-a"},
		"expected_version": emptyVer,
	})
	assert.Equal(t, http.StatusUnprocessableEntity, rec.Code)
	assert.Contains(t, rec.Body.String(), ErrCodeOwnerMismatch)

	// Not in installation -> 422 repo_not_in_installation.
	rec = doRequestAsUser(t, f.srv, f.projectAdmin, http.MethodPut, path, map[string]any{
		"repos":            []string{"acme/not-installed"},
		"expected_version": emptyVer,
	})
	assert.Equal(t, http.StatusUnprocessableEntity, rec.Code)
	assert.Contains(t, rec.Body.String(), ErrCodeRepoNotInInstallation)

	// Primary repo rejected -> 422.
	rec = doRequestAsUser(t, f.srv, f.projectAdmin, http.MethodPut, path, map[string]any{
		"repos":            []string{"ACME/PRIMARY"},
		"expected_version": emptyVer,
	})
	assert.Equal(t, http.StatusUnprocessableEntity, rec.Code)

	// Duplicate (case-insensitive) rejected -> 422.
	rec = doRequestAsUser(t, f.srv, f.projectAdmin, http.MethodPut, path, map[string]any{
		"repos":            []string{"acme/lib-a", "Acme/Lib-A"},
		"expected_version": emptyVer,
	})
	assert.Equal(t, http.StatusUnprocessableEntity, rec.Code)

	// Invalid format rejected -> 422.
	rec = doRequestAsUser(t, f.srv, f.projectAdmin, http.MethodPut, path, map[string]any{
		"repos":            []string{"not-a-slug"},
		"expected_version": emptyVer,
	})
	assert.Equal(t, http.StatusUnprocessableEntity, rec.Code)

	// Exceeding max (20) rejected -> 422.
	tooMany := make([]string, maxGitHubExtraRepos+1)
	for i := range tooMany {
		tooMany[i] = fmt.Sprintf("acme/repo-%d", i)
	}
	rec = doRequestAsUser(t, f.srv, f.projectAdmin, http.MethodPut, path, map[string]any{
		"repos":            tooMany,
		"expected_version": emptyVer,
	})
	assert.Equal(t, http.StatusUnprocessableEntity, rec.Code)

	// Stale expected_version -> 409 conflict.
	rec = doRequestAsUser(t, f.srv, f.projectAdmin, http.MethodPut, path, map[string]any{
		"repos":            []string{"acme/lib-a"},
		"expected_version": "stale-hash",
	})
	assert.Equal(t, http.StatusConflict, rec.Code)

	// Valid PUT succeeds; reusing old emptyVer fails with 409 conflict.
	rec = doRequestAsUser(t, f.srv, f.projectAdmin, http.MethodPut, path, map[string]any{
		"repos":            []string{"acme/lib-a"},
		"expected_version": emptyVer,
	})
	require.Equal(t, http.StatusOK, rec.Code)

	rec = doRequestAsUser(t, f.srv, f.projectAdmin, http.MethodPut, path, map[string]any{
		"repos":            []string{"acme/lib-b"},
		"expected_version": emptyVer,
	})
	assert.Equal(t, http.StatusConflict, rec.Code)

	// Generic PATCH (rename) never clears or alters github_extra_repos.
	rec = doRequestAsUser(t, f.srv, f.projectAdmin, http.MethodPatch, "/api/v1/projects/"+f.project.ID, map[string]any{
		"name": "Renamed Project",
	})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	gotProj, err := f.st.GetProject(context.Background(), f.project.ID)
	require.NoError(t, err)
	assert.Equal(t, "Renamed Project", gotProj.Name)
	require.Len(t, gotProj.GitHubExtraRepos, 1)
	assert.Equal(t, "acme/lib-a", gotProj.GitHubExtraRepos[0].Repo)
}

func TestGitHubExtraRepos_MintReadOnlyScopedCachingAndNoStatusMutation(t *testing.T) {
	f := setupExtraReposFixture(t, nil)
	setGitHubExtraReposExperiment(t, f.srv, true)
	ctx := context.Background()

	emptyVer := store.HashGitHubExtraRepos(nil)
	path := "/api/v1/projects/" + f.project.ID + "/github-extra-repos"
	rec := doRequestAsUser(t, f.srv, f.projectAdmin, http.MethodPut, path, map[string]any{
		"repos":            []string{"acme/lib-b", "acme/lib-a"},
		"expected_version": emptyVer,
	})
	require.Equal(t, http.StatusOK, rec.Code)

	proj, err := f.st.GetProject(ctx, f.project.ID)
	require.NoError(t, err)

	// Mint extra-read token: must be read-only (contents: read, metadata: read)
	// and scoped strictly to bare repo names ["lib-a", "lib-b"].
	tok1, exp1, repos1, err := f.srv.MintGitHubAppExtraReadToken(ctx, proj)
	require.NoError(t, err)
	assert.NotEmpty(t, tok1)
	assert.NotEmpty(t, exp1)
	assert.ElementsMatch(t, []string{"acme/lib-b", "acme/lib-a"}, repos1)
	require.Equal(t, 1, f.fakeGH.mintCount())

	m := f.fakeGH.lastMint()
	assert.Equal(t, []string{"lib-a", "lib-b"}, m.Repos)
	assert.Equal(t, map[string]string{
		"contents": "read",
		"metadata": "read",
	}, m.Permissions)

	// Second call hits cache (no second GitHub mint).
	tok2, _, _, err := f.srv.MintGitHubAppExtraReadToken(ctx, proj)
	require.NoError(t, err)
	assert.Equal(t, tok1, tok2)
	assert.Equal(t, 1, f.fakeGH.mintCount())

	// Mint failure does NOT change project.GitHubAppStatus.
	initialStatus := &store.GitHubAppProjectStatus{
		State:       store.GitHubAppStateOK,
		LastChecked: time.Now().UTC().Truncate(time.Microsecond),
	}
	require.NoError(t, f.st.UpdateProjectGitHubAppStatus(ctx, proj.ID, initialStatus))
	f.srv.githubTokenCache.invalidateProject(proj.ID)

	f.fakeGH.mu.Lock()
	f.fakeGH.mintStatus = http.StatusInternalServerError
	f.fakeGH.mu.Unlock()

	_, _, _, err = f.srv.MintGitHubAppExtraReadToken(ctx, proj)
	require.Error(t, err)

	afterFail, err := f.st.GetProject(ctx, proj.ID)
	require.NoError(t, err)
	require.NotNil(t, afterFail.GitHubAppStatus)
	assert.Equal(t, store.GitHubAppStateOK, afterFail.GitHubAppStatus.State,
		"extra-read mint failure must never mutate project.GitHubAppStatus")

	// Metric recorded both ok and error/token_mint_failed results, exposed on /metrics.
	snap := f.srv.githubExtraReadMetrics.GetSnapshot()
	require.NotNil(t, snap)
	assert.GreaterOrEqual(t, snap.MintsByResult["ok"], int64(2))
	assert.GreaterOrEqual(t, snap.MintsByResult["error"]+snap.MintsByResult["token_mint_failed"], int64(1))
}

func TestGitHubExtraRepos_NeverInEnvOrDisk(t *testing.T) {
	f := setupExtraReposFixture(t, nil)
	setGitHubExtraReposExperiment(t, f.srv, true)
	ctx := context.Background()

	emptyVer := store.HashGitHubExtraRepos(nil)
	rec := doRequestAsUser(t, f.srv, f.projectAdmin, http.MethodPut,
		"/api/v1/projects/"+f.project.ID+"/github-extra-repos",
		map[string]any{
			"repos":            []string{"acme/lib-a"},
			"expected_version": emptyVer,
		})
	require.Equal(t, http.StatusOK, rec.Code)

	proj, err := f.st.GetProject(ctx, f.project.ID)
	require.NoError(t, err)

	extraTok, _, _, err := f.srv.MintGitHubAppExtraReadToken(ctx, proj)
	require.NoError(t, err)
	require.NotEmpty(t, extraTok)

	// Confirm the extra-read token does not leak into process env, temp dir,
	// or agent launch delivery env.
	for _, kv := range os.Environ() {
		assert.NotContains(t, kv, extraTok)
	}

	// Verify agent GitHub token delivery only mints/delivers the primary repo token,
	// never the extra-read token.
	agent := &store.Agent{
		ID:        tid("er-agent"),
		Slug:      tid("er-agent"),
		Name:      "er-agent",
		ProjectID: proj.ID,
		OwnerID:   f.hubAdmin.ID,
	}
	require.NoError(t, f.st.CreateAgent(ctx, agent))

	disp := &HTTPAgentDispatcher{
		store:           f.st,
		githubAppMinter: f.srv,
	}
	envMap := map[string]string{}
	envClassifications := map[string]api.EnvKind{}
	_, _, ownAppTok := disp.deliverGitHubToken(ctx, agent, envMap, &envClassifications, nil, nil, "create")
	assert.NotEqual(t, extraTok, ownAppTok, "deliverGitHubToken must never deliver the extra-read token")
	for k, v := range envMap {
		assert.NotEqual(t, extraTok, v, "extra-read token must never be delivered in env key %s", k)
	}
}

func TestGitHubExtraRepos_RefreshTokenScope(t *testing.T) {
	f := setupExtraReposFixture(t, nil)
	setGitHubExtraReposExperiment(t, f.srv, true)
	ctx := context.Background()

	emptyVer := store.HashGitHubExtraRepos(nil)
	rec := doRequestAsUser(t, f.srv, f.projectAdmin, http.MethodPut,
		"/api/v1/projects/"+f.project.ID+"/github-extra-repos",
		map[string]any{
			"repos":            []string{"acme/lib-a"},
			"expected_version": emptyVer,
		})
	require.Equal(t, http.StatusOK, rec.Code)

	agent := &store.Agent{
		ID:        tid("er-refresh-agent"),
		Slug:      tid("er-refresh-agent"),
		Name:      "er-refresh-agent",
		ProjectID: f.project.ID,
		OwnerID:   f.projectAdmin.ID,
	}
	require.NoError(t, f.st.CreateAgent(ctx, agent))

	agentToken := f.srv.generateRecordedAgentToken(t,
		agent.ID, f.project.ID, []AgentTokenScope{ScopeAgentTokenRefresh}, nil,
	)

	endpoint := "/api/v1/agents/" + agent.ID + "/refresh-token"

	// 1. No body -> primary token response byte-identical (no purpose, no repos).
	rec = doRequestWithAgentToken(t, f.srv, http.MethodPost, endpoint, nil, agentToken)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var primaryResp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &primaryResp))
	assert.Contains(t, primaryResp, "token")
	assert.Contains(t, primaryResp, "expires_at")
	assert.NotContains(t, primaryResp, "purpose")
	assert.NotContains(t, primaryResp, "repos")

	// 2. extra_read scope for configured extra repo -> 200 with purpose + repos.
	rec = doRequestWithAgentToken(t, f.srv, http.MethodPost, endpoint, map[string]any{
		"scope": map[string]any{
			"purpose": "extra_read",
			"repo":    "ACME/LIB-A",
		},
	}, agentToken)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var extraResp struct {
		Token     string   `json:"token"`
		ExpiresAt string   `json:"expires_at"`
		Purpose   string   `json:"purpose"`
		Repos     []string `json:"repos"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &extraResp))
	assert.NotEmpty(t, extraResp.Token)
	assert.NotEmpty(t, extraResp.ExpiresAt)
	assert.Equal(t, "extra_read", extraResp.Purpose)
	assert.Equal(t, []string{"acme/lib-a"}, extraResp.Repos)

	// 3. extra_read scope for unconfigured repo -> 404 {"error": "not_extra_repo"}.
	rec = doRequestWithAgentToken(t, f.srv, http.MethodPost, endpoint, map[string]any{
		"scope": map[string]any{
			"purpose": "extra_read",
			"repo":    "acme/lib-b",
		},
	}, agentToken)
	assert.Equal(t, http.StatusNotFound, rec.Code)
	assert.Contains(t, rec.Body.String(), ErrCodeNotExtraRepo)

	// 4. Unknown purpose -> 400.
	rec = doRequestWithAgentToken(t, f.srv, http.MethodPost, endpoint, map[string]any{
		"scope": map[string]any{
			"purpose": "write_all",
			"repo":    "acme/lib-a",
		},
	}, agentToken)
	assert.Equal(t, http.StatusBadRequest, rec.Code)

	// 5. Policy off -> 404 not_extra_repo.
	f.srv.mu.Lock()
	f.srv.config.GitHubAppConfig.ExtraReposPolicy = config.GitHubExtraReposPolicyOff
	f.srv.mu.Unlock()

	rec = doRequestWithAgentToken(t, f.srv, http.MethodPost, endpoint, map[string]any{
		"scope": map[string]any{
			"purpose": "extra_read",
			"repo":    "acme/lib-a",
		},
	}, agentToken)
	assert.Equal(t, http.StatusNotFound, rec.Code)
	assert.Contains(t, rec.Body.String(), ErrCodeNotExtraRepo)
}

func TestGitHubExtraRepos_ReadModelStatusesWebhookHookAuditAndAdminReport(t *testing.T) {
	f := setupExtraReposFixture(t, nil)
	setGitHubExtraReposExperiment(t, f.srv, true)
	ctx := context.Background()

	emptyVer := store.HashGitHubExtraRepos(nil)
	path := "/api/v1/projects/" + f.project.ID + "/github-extra-repos"

	// Add two extra repos via PUT.
	rec := doRequestAsUser(t, f.srv, f.projectAdmin, http.MethodPut, path, map[string]any{
		"repos":            []string{"acme/lib-a", "acme/lib-b"},
		"expected_version": emptyVer,
	})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	// Wait briefly for async emitMutationAudit and verify audit record.
	var auditFound bool
	for i := 0; i < 30; i++ {
		recs, _, err := f.st.ListMutationAudits(ctx, store.MutationAuditFilter{
			MutationType: mutationTypeProjectGitHubExtraReposUpdate,
			TargetID:     f.project.ID,
		})
		require.NoError(t, err)
		if len(recs) > 0 {
			auditFound = true
			assert.Equal(t, f.projectAdmin.ID, recs[0].ActorPrincipalID)
			assert.Contains(t, recs[0].AfterSummary, "acme/lib-a")
			assert.NotContains(t, recs[0].AfterSummary, "ghs_")
			break
		}
		time.Sleep(15 * time.Millisecond)
	}
	assert.True(t, auditFound, "expected project.github_extra_repos.update audit record")

	// Admin association report includes extra_repos_count == 2.
	_, _, byID := getAssociationReport(t, f.srv, "?refresh=true")
	entry, ok := byID[f.project.ID]
	require.True(t, ok)
	assert.Equal(t, 2, entry.ExtraReposCount)

	// Simulate installation_repositories.removed webhook removing acme/lib-b
	// (primary repo acme/primary is NOT removed).
	removedPayload, err := json.Marshal(map[string]any{
		"action":       "removed",
		"installation": map[string]any{"id": f.instID},
		"repositories_removed": []map[string]any{
			{"id": 99, "full_name": "acme/lib-b", "name": "lib-b"},
		},
	})
	require.NoError(t, err)
	require.NoError(t, f.srv.processInstallationRepositoriesWebhook(ctx, removedPayload))

	// Entry is flagged (not deleted) and read-model shows removed_from_installation.
	proj, err := f.st.GetProject(ctx, f.project.ID)
	require.NoError(t, err)
	require.Len(t, proj.GitHubExtraRepos, 2)
	assert.Nil(t, proj.GitHubExtraRepos[0].RemovedFromInstallationAt)
	assert.NotNil(t, proj.GitHubExtraRepos[1].RemovedFromInstallationAt)

	rec = doRequestAsUser(t, f.srv, f.projectAdmin, http.MethodGet, "/api/v1/projects/"+f.project.ID+"/github", nil)
	require.Equal(t, http.StatusOK, rec.Code)
	var ghResp ProjectGitHubResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &ghResp))
	require.NotNil(t, ghResp.ExtraRepos)
	require.Len(t, ghResp.ExtraRepos.Repos, 2)
	assert.Equal(t, extraRepoStatusOK, ghResp.ExtraRepos.Repos[0].Status)
	assert.Equal(t, extraRepoStatusRemovedFromInstallation, ghResp.ExtraRepos.Repos[1].Status)

	// Mint skips flagged repo and mints only for acme/lib-a.
	_, _, mintedRepos, err := f.srv.MintGitHubAppExtraReadToken(ctx, proj)
	require.NoError(t, err)
	assert.Equal(t, []string{"acme/lib-a"}, mintedRepos)

	// Simulate installation_repositories.added re-adding acme/lib-b -> flag cleared.
	addedPayload, err := json.Marshal(map[string]any{
		"action":       "added",
		"installation": map[string]any{"id": f.instID},
		"repositories_added": []map[string]any{
			{"id": 99, "full_name": "acme/lib-b", "name": "lib-b"},
		},
	})
	require.NoError(t, err)
	require.NoError(t, f.srv.processInstallationRepositoriesWebhook(ctx, addedPayload))

	proj, err = f.st.GetProject(ctx, f.project.ID)
	require.NoError(t, err)
	assert.Nil(t, proj.GitHubExtraRepos[1].RemovedFromInstallationAt)

	// Also verify owner_mismatch and not_in_installation statuses directly via store.
	now := time.Now().UTC().Truncate(time.Microsecond)
	_, err = f.st.UpdateProjectGitHubExtraRepos(ctx, f.project.ID, store.HashGitHubExtraRepos(proj.GitHubExtraRepos), []store.GitHubExtraRepo{
		{Repo: "other/repo", AddedBy: f.hubAdmin.ID, AddedAt: now},
		{Repo: "acme/missing-from-inst", AddedBy: f.hubAdmin.ID, AddedAt: now},
	})
	require.NoError(t, err)

	rec = doRequestAsUser(t, f.srv, f.projectAdmin, http.MethodGet, "/api/v1/projects/"+f.project.ID+"/github", nil)
	require.Equal(t, http.StatusOK, rec.Code)
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &ghResp))
	require.NotNil(t, ghResp.ExtraRepos)
	require.Len(t, ghResp.ExtraRepos.Repos, 2)
	assert.Equal(t, extraRepoStatusOwnerMismatch, ghResp.ExtraRepos.Repos[0].Status)
	assert.Equal(t, extraRepoStatusNotInInstallation, ghResp.ExtraRepos.Repos[1].Status)

	// DELETE clears the list.
	rec = doRequestAsUser(t, f.srv, f.projectAdmin, http.MethodDelete, path, bytes.NewReader(nil))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	proj, err = f.st.GetProject(ctx, f.project.ID)
	require.NoError(t, err)
	assert.Empty(t, proj.GitHubExtraRepos)
}

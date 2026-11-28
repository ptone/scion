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
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/GoogleCloudPlatform/scion/pkg/agent"
	"github.com/GoogleCloudPlatform/scion/pkg/ent/githubresolutioncache"
	"github.com/GoogleCloudPlatform/scion/pkg/githubfetch"
	"github.com/GoogleCloudPlatform/scion/pkg/secret"
	"github.com/GoogleCloudPlatform/scion/pkg/store"
	"github.com/GoogleCloudPlatform/scion/pkg/store/enttest"
)

// TestSkillsResolve_P4_CredentialsAndRefs covers private skill resolution via
// App, GH_REPO, GH_OWNER, and GITHUB_TOKEN across main, slashed branch, tag,
// and 40-hex SHA refs, asserting no token leaks into responses, logs, or DB rows.
func TestSkillsResolve_P4_CredentialsAndRefs(t *testing.T) {
	const (
		owner     = "acme"
		repo      = "private-skills"
		skillPath = "skills/deploy"
		shaMain   = "1111111111111111111111111111111111111111"
		shaSlash  = "2222222222222222222222222222222222222222"
		shaTag    = "3333333333333333333333333333333333333333"
		shaDirect = "4444444444444444444444444444444444444444"

		tokRepo  = "ghp_secret_repo_token_9549"
		tokOwner = "ghp_secret_owner_token_9549"
		tokDef   = "ghp_secret_default_token_9549"
		tokApp   = "ghs_minted_app_token_9549"
	)

	var (
		mu             sync.Mutex
		acceptedTokens = map[string]bool{
			tokRepo:  true,
			tokOwner: true,
			tokDef:   true,
			tokApp:   true,
		}
		lastAuthHeader string
		seenRawPaths   []string
	)

	mux := http.NewServeMux()
	mux.HandleFunc("/app", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":          1,
			"slug":        "p4-test-app",
			"permissions": map[string]string{"contents": "read", "metadata": "read"},
		})
	})
	mux.HandleFunc("/app/installations/", func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/access_tokens") {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"token":      tokApp,
			"expires_at": time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
		})
	})
	mux.HandleFunc("/repos/"+owner+"/"+repo+"/", func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		tok := strings.TrimPrefix(auth, "Bearer ")
		mu.Lock()
		ok := strings.HasPrefix(auth, "Bearer ") && acceptedTokens[tok]
		lastAuthHeader = auth
		seenRawPaths = append(seenRawPaths, r.URL.EscapedPath())
		mu.Unlock()
		if !ok {
			http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
			return
		}

		escaped := r.URL.EscapedPath()
		switch {
		case escaped == "/repos/"+owner+"/"+repo+"/commits/main":
			w.Header().Set("ETag", `"etag-main"`)
			_, _ = w.Write([]byte(shaMain))
		case escaped == "/repos/"+owner+"/"+repo+"/commits/"+url.PathEscape("feat/x/y"):
			w.Header().Set("ETag", `"etag-slash"`)
			_, _ = w.Write([]byte(shaSlash))
		case escaped == "/repos/"+owner+"/"+repo+"/commits/v1.2.3":
			w.Header().Set("ETag", `"etag-tag"`)
			_, _ = w.Write([]byte(shaTag))
		case strings.HasPrefix(r.URL.Path, "/repos/"+owner+"/"+repo+"/git/trees/"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"sha":"tree1","truncated":false,"tree":[
				{"path":"SKILL.md","mode":"100644","type":"blob","sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","size":10},
				{"path":"run.sh","mode":"100755","type":"blob","sha":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","size":20}
			]}`))
		default:
			http.NotFound(w, r)
		}
	})
	gh := httptest.NewServer(mux)
	t.Cleanup(gh.Close)

	srv, s, alice, _, project := setupSkillAuthzTest(t)
	entClient := enttest.NewClient(t)
	srv.ghResolutionStore = NewGitHubResolutionStore(entClient)
	wireFakeSkillGitHub(srv, gh.URL)
	srv.config.GitHubAppConfig.AppID = 1
	srv.config.GitHubAppConfig.PrivateKey = generateTestGitHubAppKey(t)

	ctx := context.Background()
	const instID = int64(9001)
	require.NoError(t, s.CreateGitHubInstallation(ctx, &store.GitHubInstallation{
		InstallationID: instID,
		AccountLogin:   owner,
		AccountType:    "Organization",
		AppID:          1,
		Status:         store.GitHubInstallationStatusActive,
	}))
	bindProjectForTest(t, srv, s, project, instID, owner, repo)

	logs := captureJSONSlog(t)

	resolveOne := func(t *testing.T, uri string) (string, ResolveSkillsResponse) {
		t.Helper()
		rec := doRequestAsUser(t, srv, alice, http.MethodPost, "/api/v1/skills/resolve",
			ResolveSkillsRequest{Skills: []ResolveSkillRef{{URI: uri}}, ProjectID: project.ID})
		require.Equal(t, http.StatusOK, rec.Code, "body: %s\nlogs: %s", rec.Body.String(), logs())
		raw := rec.Body.String()
		var resp ResolveSkillsResponse
		require.NoError(t, json.Unmarshal([]byte(raw), &resp))
		return raw, resp
	}

	localBackend := secret.NewLocalBackend(s, srv.HubID(), "")
	setProjectSecrets := func(vals map[string]string) {
		b := &importCredSecretBackend{
			SecretBackend: localBackend,
			scope:         secret.ScopeProject,
			projectID:     project.ID,
			values:        vals,
		}
		srv.SetSecretBackend(b)
	}

	// 1. GH_OWNER__REPO takes precedence over GH_OWNER, App, GITHUB_TOKEN.
	setProjectSecrets(map[string]string{
		"GH_ACME__PRIVATE_SKILLS": tokRepo,
		"GH_ACME":                 tokOwner,
		"GITHUB_TOKEN":            tokDef,
	})
	raw, resp := resolveOne(t, "gh://"+owner+"/"+repo+"/deploy@main")
	require.Empty(t, resp.Errors)
	require.Len(t, resp.Resolved, 1)
	assert.Equal(t, safeShortSHA(shaMain), resp.Resolved[0].ResolvedVersion)
	mu.Lock()
	assert.Equal(t, "Bearer "+tokRepo, lastAuthHeader)
	mu.Unlock()
	assert.NotContains(t, raw, tokRepo)

	// 2. GH_OWNER takes precedence over App and GITHUB_TOKEN, with slashed branch ref.
	setProjectSecrets(map[string]string{
		"GH_ACME":      tokOwner,
		"GITHUB_TOKEN": tokDef,
	})
	raw, resp = resolveOne(t, "gh://"+owner+"/"+repo+"/deploy@feat/x/y")
	require.Empty(t, resp.Errors)
	require.Len(t, resp.Resolved, 1)
	assert.Equal(t, safeShortSHA(shaSlash), resp.Resolved[0].ResolvedVersion)
	mu.Lock()
	assert.Equal(t, "Bearer "+tokOwner, lastAuthHeader)
	assert.Contains(t, seenRawPaths, "/repos/"+owner+"/"+repo+"/commits/feat%2Fx%2Fy",
		"slashed branch ref must be path-escaped")
	mu.Unlock()
	assert.NotContains(t, raw, tokOwner)

	// 3. GitHub App takes precedence over GITHUB_TOKEN, with tag ref.
	setProjectSecrets(map[string]string{
		"GITHUB_TOKEN": tokDef,
	})
	raw, resp = resolveOne(t, "gh://"+owner+"/"+repo+"/deploy@v1.2.3")
	require.Empty(t, resp.Errors)
	require.Len(t, resp.Resolved, 1)
	assert.Equal(t, safeShortSHA(shaTag), resp.Resolved[0].ResolvedVersion)
	mu.Lock()
	assert.Equal(t, "Bearer "+tokApp, lastAuthHeader)
	mu.Unlock()
	assert.NotContains(t, raw, tokApp)

	// 4. GITHUB_TOKEN is used when no App installation is attached, with 40-hex SHA ref.
	project.GitHubInstallationID = nil
	require.NoError(t, s.UpdateProject(ctx, project))
	raw, resp = resolveOne(t, "gh://"+owner+"/"+repo+"/deploy@"+shaDirect)
	require.Empty(t, resp.Errors)
	require.Len(t, resp.Resolved, 1)
	assert.Equal(t, safeShortSHA(shaDirect), resp.Resolved[0].ResolvedVersion)
	mu.Lock()
	assert.Equal(t, "Bearer "+tokDef, lastAuthHeader)
	mu.Unlock()
	assert.NotContains(t, raw, tokDef)

	// Verify no token ever leaked into logs or DB rows.
	logBuf := logs()
	rows, err := entClient.GitHubResolutionCache.Query().All(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, rows)
	for _, secretTok := range []string{tokRepo, tokOwner, tokDef, tokApp} {
		assert.NotContains(t, logBuf, secretTok, "token must never appear in logs")
		for _, row := range rows {
			assert.NotContains(t, row.CacheKey, secretTok, "token must never appear in cache_key")
			assert.NotContains(t, row.TokenScope, secretTok, "token must never appear in token_scope")
			assert.NotContains(t, row.OriginalURI, secretTok, "token must never appear in original_uri")
		}
	}
}

// TestSkillsResolve_P4_ScopeSharingAndIsolation verifies:
//   - Two projects linked to the same GitHub App installation share a cache entry.
//   - A different installation, different PAT, or anonymous request never hits another scope's entry.
//   - A requester failing canUseProjectGitHubToken gets "forbidden" BEFORE any cache read.
func TestSkillsResolve_P4_ScopeSharingAndIsolation(t *testing.T) {
	const (
		owner     = "acme"
		repo      = "shared-repo"
		skillPath = "skills/widget"
		uri       = "gh://" + owner + "/" + repo + "/widget@main"
		sha       = "5555555555555555555555555555555555555555"
		instA     = int64(7001)
		instB     = int64(7002)
	)

	var apiCalls atomic.Int64
	mux := http.NewServeMux()
	mux.HandleFunc("/app", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":          1,
			"slug":        "p4-scope-app",
			"permissions": map[string]string{"contents": "read", "metadata": "read"},
		})
	})
	mux.HandleFunc("/app/installations/", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"token":      "ghs_inst_tok_" + r.URL.Path,
			"expires_at": time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
		})
	})
	mux.HandleFunc("/repos/"+owner+"/"+repo+"/commits/main", func(w http.ResponseWriter, r *http.Request) {
		apiCalls.Add(1)
		_, _ = w.Write([]byte(sha))
	})
	mux.HandleFunc(ghTreeTestPath(owner, repo, sha, skillPath), func(w http.ResponseWriter, r *http.Request) {
		apiCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(ghTreeTestSingleFile))
	})
	gh := httptest.NewServer(mux)
	t.Cleanup(gh.Close)

	srv, s, alice, bob, projectA1 := setupSkillAuthzTest(t)
	srv.ghResolutionStore = NewGitHubResolutionStore(enttest.NewClient(t))
	wireFakeSkillGitHub(srv, gh.URL)
	srv.config.GitHubAppConfig.AppID = 1
	srv.config.GitHubAppConfig.PrivateKey = generateTestGitHubAppKey(t)

	ctx := context.Background()
	for _, id := range []int64{instA, instB} {
		require.NoError(t, s.CreateGitHubInstallation(ctx, &store.GitHubInstallation{
			InstallationID: id, AccountLogin: owner, AccountType: "Organization", AppID: 1, Status: store.GitHubInstallationStatusActive,
		}))
	}

	makeProjectForAlice := func(slug string, instID *int64) *store.Project {
		p := &store.Project{
			ID:        tid("proj-" + slug),
			Name:      slug,
			Slug:      slug,
			OwnerID:   alice.ID,
			CreatedBy: alice.ID,
			Created:   time.Now(),
			Updated:   time.Now(),
		}
		require.NoError(t, s.CreateProject(ctx, p))
		srv.seedProjectCreatorMembership(ctx, p)
		if instID != nil {
			bindProjectForTest(t, srv, s, p, *instID, owner, repo)
		}
		return p
	}

	bindProjectForTest(t, srv, s, projectA1, instA, owner, repo)
	instAPtr, instBPtr := instA, instB
	projectA2 := makeProjectForAlice("proj-a2", &instAPtr)
	projectB := makeProjectForAlice("proj-b", &instBPtr)
	projectPAT := makeProjectForAlice("proj-pat", nil)
	projectAnon := makeProjectForAlice("proj-anon", nil)

	resolve := func(t *testing.T, user *store.User, projID string) ResolveSkillsResponse {
		t.Helper()
		rec := doRequestAsUser(t, srv, user, http.MethodPost, "/api/v1/skills/resolve",
			ResolveSkillsRequest{Skills: []ResolveSkillRef{{URI: uri}}, ProjectID: projID})
		require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
		var resp ResolveSkillsResponse
		require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
		return resp
	}

	// 1. projectA1 populates cache for app:7001.
	resp := resolve(t, alice, projectA1.ID)
	require.Len(t, resp.Resolved, 1)
	require.Equal(t, int64(2), apiCalls.Load())

	// 2. projectA2 on the SAME installation shares the cache entry (0 new calls).
	resp = resolve(t, alice, projectA2.ID)
	require.Len(t, resp.Resolved, 1)
	assert.Equal(t, int64(2), apiCalls.Load(), "two projects on the same installation must share the cache entry")

	// 3. Unauthorized user (bob) gets forbidden BEFORE any cache read even though app:7001 is warm.
	resp = resolve(t, bob, projectA1.ID)
	require.Empty(t, resp.Resolved)
	require.Len(t, resp.Errors, 1)
	assert.Equal(t, agent.SkillErrCodeForbidden, resp.Errors[0].Code)
	assert.Equal(t, int64(2), apiCalls.Load())

	// 4. Different installation (app:7002) misses and calls GitHub.
	resp = resolve(t, alice, projectB.ID)
	require.Len(t, resp.Resolved, 1)
	assert.Equal(t, int64(4), apiCalls.Load(), "different installation must not hit app:7001 cache")

	// 5. Different PAT (secret:<hmac>) misses and calls GitHub.
	useReimportCredBackend(srv, secret.ScopeProject, projectPAT.ID, map[string]string{
		"GITHUB_TOKEN": "ghp_isolated_pat_token",
	})
	resp = resolve(t, alice, projectPAT.ID)
	require.Len(t, resp.Resolved, 1)
	assert.Equal(t, int64(6), apiCalls.Load(), "PAT scope must not hit App cache")

	// 6. Anonymous project misses and calls GitHub.
	resp = resolve(t, alice, projectAnon.ID)
	require.Len(t, resp.Resolved, 1)
	assert.Equal(t, int64(8), apiCalls.Load(), "anon scope must not hit credentialed cache")
}

// TestSkillsResolve_P4_RevocationStaleAndETag304 verifies:
//   - Refresh sends If-None-Match; 304 extends TTL without re-listing tree.
//   - For a credentialed scope, transient failure (5xx / 429) serves stale.
//   - For a credentialed scope, authoritative 404/401/403 deletes all {host,owner,repo,scope}
//     rows, writes 1-minute negative memo, and is NOT served stale.
func TestSkillsResolve_P4_RevocationStaleAndETag304(t *testing.T) {
	const (
		owner     = "acme"
		repo      = "revocation-repo"
		skillPath = "skills/s"
		uri       = "gh://" + owner + "/" + repo + "/s@main"
		sha       = "6666666666666666666666666666666666666666"
		pat       = "ghp_revocation_test_pat"
	)

	var (
		mode        atomic.Int32 // 0=200, 1=304, 2=500, 3=404
		commitCalls atomic.Int64
		treeCalls   atomic.Int64
		gotINM      atomic.Value
	)
	gotINM.Store("")

	mux := http.NewServeMux()
	mux.HandleFunc("/repos/"+owner+"/"+repo+"/commits/main", func(w http.ResponseWriter, r *http.Request) {
		commitCalls.Add(1)
		gotINM.Store(r.Header.Get("If-None-Match"))
		switch mode.Load() {
		case 1:
			w.Header().Set("ETag", `"etag-v1"`)
			w.WriteHeader(http.StatusNotModified)
		case 2:
			w.WriteHeader(http.StatusInternalServerError)
		case 3:
			http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
		default:
			w.Header().Set("ETag", `"etag-v1"`)
			_, _ = w.Write([]byte(sha))
		}
	})
	mux.HandleFunc(ghTreeTestPath(owner, repo, sha, skillPath), func(w http.ResponseWriter, _ *http.Request) {
		treeCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(ghTreeTestSingleFile))
	})
	gh := httptest.NewServer(mux)
	t.Cleanup(gh.Close)

	srv, _, alice, _, project := setupSkillAuthzTest(t)
	entClient := enttest.NewClient(t)
	srv.ghResolutionStore = NewGitHubResolutionStore(entClient)
	wireFakeSkillGitHub(srv, gh.URL)
	useReimportCredBackend(srv, secret.ScopeProject, project.ID, map[string]string{
		"GITHUB_TOKEN": pat,
	})

	ctx := context.Background()
	cred := srv.githubCredential(ctx, ghCredRequest{
		Scope: store.TemplateScopeProject, ScopeID: project.ID, Owner: owner, Repo: repo,
	})
	scope := cred.EffectiveScope()
	require.True(t, strings.HasPrefix(scope, "secret:"))

	resolve := func(t *testing.T) ResolveSkillsResponse {
		t.Helper()
		rec := doRequestAsUser(t, srv, alice, http.MethodPost, "/api/v1/skills/resolve",
			ResolveSkillsRequest{Skills: []ResolveSkillRef{{URI: uri}}, ProjectID: project.ID})
		require.Equal(t, http.StatusOK, rec.Code)
		var resp ResolveSkillsResponse
		require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
		return resp
	}

	// 1. Initial resolve populates ref (with ETag) + tree.
	resp := resolve(t)
	require.Len(t, resp.Resolved, 1)
	require.Equal(t, int64(1), commitCalls.Load())
	require.Equal(t, int64(1), treeCalls.Load())

	ep := srv.skillGitHubEndpoint()
	refKey := githubfetch.RefCacheKey(ep.APIHost(), owner, repo, "main", scope)

	// Expire the ref row so the next call revalidates.
	expireRef := func() {
		row, err := srv.ghResolutionStore.load(ctx, refKey)
		require.NoError(t, err)
		require.NotNil(t, row)
		row.ExpiresAt = time.Now().Add(-time.Minute)
		require.NoError(t, srv.ghResolutionStore.Put(ctx, refKey, *row))
	}

	// 2. Refresh sends If-None-Match; 304 extends TTL without re-listing tree.
	expireRef()
	mode.Store(1)
	resp = resolve(t)
	require.Len(t, resp.Resolved, 1)
	assert.Equal(t, `"etag-v1"`, gotINM.Load())
	assert.Equal(t, int64(2), commitCalls.Load())
	assert.Equal(t, int64(1), treeCalls.Load(), "304 must extend TTL without re-listing tree")

	// Ref row should now be fresh again (next resolve makes 0 GitHub calls).
	resp = resolve(t)
	require.Len(t, resp.Resolved, 1)
	assert.Equal(t, int64(2), commitCalls.Load(), "extended TTL must serve fresh cache hit")

	// 3. Transient 5xx serves stale entry for credentialed scope.
	expireRef()
	mode.Store(2)
	resp = resolve(t)
	require.Len(t, resp.Resolved, 1, "5xx must serve stale entry")
	assert.Equal(t, safeShortSHA(sha), resp.Resolved[0].ResolvedVersion)

	// 4. Authoritative 404 for credentialed scope deletes ref+tree rows and is NOT served stale.
	// Step 3's transient 5xx activated refresh-failure backoff; clear it to
	// simulate backoff expiry before the next revalidation sees the revoked 404.
	srv.clearGHRefreshFailure(refKey + ":" + skillPath)
	expireRef()
	mode.Store(3)
	resp = resolve(t)
	require.Empty(t, resp.Resolved, "revoked 404 must NOT serve stale entry for credentialed scope")
	require.Len(t, resp.Errors, 1)
	assert.Equal(t, agent.SkillErrCodeNotFound, resp.Errors[0].Code)

	count, err := entClient.GitHubResolutionCache.Query().
		Where(githubresolutioncache.TokenScopeEQ(scope)).
		Count(ctx)
	require.NoError(t, err)
	assert.Zero(t, count, "authoritative 404 must delete both ref and tree rows for the scope")

	// 5. Negative memo absorbs the immediate retry (0 new GitHub calls).
	callsBefore := commitCalls.Load()
	resp = resolve(t)
	require.Len(t, resp.Errors, 1)
	assert.Equal(t, agent.SkillErrCodeNotFound, resp.Errors[0].Code)
	assert.Equal(t, callsBefore, commitCalls.Load(), "negative memo must absorb immediate retry")
}

// TestSkillsResolve_P4_HostedRefusesDenylistedAddress verifies that hosted Hub
// skill resolution (s.workstation == false, production remotefetch Doer) refuses
// loopback/denylisted addresses rather than talking to them via a bare http.Client.
func TestSkillsResolve_P4_HostedRefusesDenylistedAddress(t *testing.T) {
	var hits atomic.Int64
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(gh.Close)

	srv, _, alice, _, project := setupSkillAuthzTest(t)
	srv.workstation = false
	srv.skillGitHubDoer = nil // production remotefetch.NetworkVetted path
	srv.config.GitHubAppConfig.APIBaseURL = gh.URL
	srv.config.GitHubAppConfig.RawBaseURL = gh.URL

	rec := doRequestAsUser(t, srv, alice, http.MethodPost, "/api/v1/skills/resolve",
		ResolveSkillsRequest{
			Skills:    []ResolveSkillRef{{URI: "gh://acme/repo/s@main"}},
			ProjectID: project.ID,
		})
	require.Equal(t, http.StatusOK, rec.Code)
	var resp ResolveSkillsResponse
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
	require.Empty(t, resp.Resolved)
	require.Len(t, resp.Errors, 1)
	assert.Equal(t, agent.SkillErrCodeResolveFailed, resp.Errors[0].Code)
	assert.Zero(t, hits.Load(), "hosted NetworkVetted must refuse loopback/http address without contacting it")
}

// TestSkillsResolve_P4_SharedCooldownWithReimport verifies that a rate limit
// recorded on Hub skills activates the cooldown for that exact principal scope
// ("app:<id>") and is immediately observed by template reimport (and vice versa),
// without affecting other scopes.
func TestSkillsResolve_P4_SharedCooldownWithReimport(t *testing.T) {
	const instID = int64(8181)
	srv, s, _, _, project := setupSkillAuthzTest(t)
	srv.ghResolutionStore = NewGitHubResolutionStore(enttest.NewClient(t))
	clock := &ghCooldownClock{t: time.Now()}
	srv.ghNow = clock.Now
	srv.ghCooldown = githubfetch.NewCooldown(clock.Now)

	mux := http.NewServeMux()
	mux.HandleFunc("/app", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":          1,
			"slug":        "p4-shared-cd-app",
			"permissions": map[string]string{"contents": "read", "metadata": "read"},
		})
	})
	mux.HandleFunc("/app/installations/", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"token":      "ghs_shared_cd_tok",
			"expires_at": time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
		})
	})
	mux.HandleFunc("/repos/acme/limited/", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "45")
		w.WriteHeader(http.StatusTooManyRequests)
	})
	gh := httptest.NewServer(mux)
	t.Cleanup(gh.Close)
	wireFakeSkillGitHub(srv, gh.URL)
	srv.config.GitHubAppConfig.AppID = 1
	srv.config.GitHubAppConfig.PrivateKey = generateTestGitHubAppKey(t)

	ctx := context.Background()
	require.NoError(t, s.CreateGitHubInstallation(ctx, &store.GitHubInstallation{
		InstallationID: instID, AccountLogin: "acme", AccountType: "Organization", AppID: 1, Status: store.GitHubInstallationStatusActive,
	}))
	bindProjectForTest(t, srv, s, project, instID, "acme", "limited")

	// Trigger 429 on skill resolution.
	_, err := srv.resolveGitHubSkill(ctx, "gh://acme/limited/s@main", project.ID, nil)
	var ghe *githubfetch.Error
	require.ErrorAs(t, err, &ghe)
	require.Equal(t, githubfetch.KindRateLimited, ghe.Kind)

	// Reimport uses the same githubCredential -> Credential.Scope ("app:8181")
	// and the same srv.githubCooldown().
	reimportCred := srv.githubCredential(ctx, ghCredRequest{
		Scope: store.TemplateScopeProject, ScopeID: project.ID, Owner: "acme", Repo: "limited",
	})
	assert.Equal(t, "app:"+strconv.FormatInt(instID, 10), reimportCred.Scope)
	_, active := srv.githubCooldown().Active(reimportCred.EffectiveScope())
	assert.True(t, active, "cooldown triggered by skills must be active for reimport on the same principal")

	_, otherActive := srv.githubCooldown().Active(githubfetch.AnonScope)
	assert.False(t, otherActive, "cooldown must not leak to anon scope")
}

// TestSkillsResolve_P4_PerRequestCredentialMemo verifies R1: resolving N warm
// gh:// URIs in one repo for one project performs only one pass of SecretBackend
// Get calls (including for anon/absent results), while a URI in a different repo
// in the same batch performs its own lookup.
func TestSkillsResolve_P4_PerRequestCredentialMemo(t *testing.T) {
	const (
		owner = "acme"
		repoA = "repo-a"
		repoB = "repo-b"
		sha   = "7777777777777777777777777777777777777777"
		pat   = "ghp_memo_test_pat"
	)

	t.Run("PAT project deduplicates same repo and separates other repo", func(t *testing.T) {
		srv, _, alice, _, project := setupSkillAuthzTest(t)
		srv.ghResolutionStore = NewGitHubResolutionStore(enttest.NewClient(t))
		wireFakeSkillGitHub(srv, "https://api.github.com")

		backend, _ := useReimportCredBackend(srv, secret.ScopeProject, project.ID, map[string]string{
			"GITHUB_TOKEN": pat,
		})
		cred := srv.githubCredential(context.Background(), ghCredRequest{
			Scope: store.TemplateScopeProject, ScopeID: project.ID, Owner: owner, Repo: repoA,
		})
		scope := cred.EffectiveScope()
		require.True(t, strings.HasPrefix(scope, "secret:"))

		// Seed 4 warm skills in repoA (mixed case owner/repo) and 1 in repoB.
		uris := []string{
			"gh://acme/repo-a/s1@main",
			"gh://Acme/Repo-A/s2@main",
			"gh://ACME/REPO-A/s3@main",
			"gh://acme/repo-a/s4@main",
			"gh://acme/repo-b/s5@main",
		}
		for _, u := range uris {
			seedGHSkillCache(t, srv, u, scope, sha, `"e1"`, time.Now().Add(time.Hour), nil)
		}

		// Reset counter right before the batch resolve.
		backend.gets = 0
		refs := make([]ResolveSkillRef, len(uris))
		for i, u := range uris {
			refs[i] = ResolveSkillRef{URI: u}
		}
		rec := doRequestAsUser(t, srv, alice, http.MethodPost, "/api/v1/skills/resolve",
			ResolveSkillsRequest{Skills: refs, ProjectID: project.ID})
		require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
		var resp ResolveSkillsResponse
		require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
		require.Empty(t, resp.Errors)
		require.Len(t, resp.Resolved, len(uris))

		// One lookup pass for (project, acme/repo-a) = 3 Gets (GH_ACME__REPO_A,
		// GH_ACME, GITHUB_TOKEN) + one lookup pass for (project, acme/repo-b) = 3 Gets.
		// Without per-request memoization, 5 URIs would perform 15 Gets.
		assert.Equal(t, 6, backend.gets, "4 URIs in repo-a + 1 in repo-b must do exactly 2 credential lookup passes")
	})

	t.Run("anon project memoizes absent credentials across same repo", func(t *testing.T) {
		srv, _, alice, _, project := setupSkillAuthzTest(t)
		srv.ghResolutionStore = NewGitHubResolutionStore(enttest.NewClient(t))
		wireFakeSkillGitHub(srv, "https://api.github.com")

		backend, _ := useReimportCredBackend(srv, secret.ScopeProject, project.ID, nil)
		uris := []string{
			"gh://acme/repo-a/s1@main",
			"gh://acme/repo-a/s2@main",
			"gh://acme/repo-a/s3@main",
			"gh://acme/repo-a/s4@main",
		}
		for _, u := range uris {
			seedGHSkillCache(t, srv, u, githubfetch.AnonScope, sha, `"e1"`, time.Now().Add(time.Hour), nil)
		}

		backend.gets = 0
		refs := make([]ResolveSkillRef, len(uris))
		for i, u := range uris {
			refs[i] = ResolveSkillRef{URI: u}
		}
		rec := doRequestAsUser(t, srv, alice, http.MethodPost, "/api/v1/skills/resolve",
			ResolveSkillsRequest{Skills: refs, ProjectID: project.ID})
		require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
		var resp ResolveSkillsResponse
		require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
		require.Empty(t, resp.Errors)
		require.Len(t, resp.Resolved, len(uris))

		// Exactly 1 lookup pass (3 Gets: GH_ACME__REPO_A, GH_ACME, GITHUB_TOKEN)
		// for all 4 URIs, not 12.
		assert.Equal(t, 3, backend.gets, "absent/anon credentials must be memoized per repo within a request")
	})
}

// TestSkillsResolve_P4_CredentialedStaleTimeoutBackoff verifies O2: after a
// credentialed stale refresh fails transiently (timeout), a second resolve
// within the refresh-failure backoff window serves stale immediately without
// making another upstream call.
func TestSkillsResolve_P4_CredentialedStaleTimeoutBackoff(t *testing.T) {
	const (
		owner = "acme"
		repo  = "hanging-repo"
		uri   = "gh://" + owner + "/" + repo + "/s@main"
		sha   = "8888888888888888888888888888888888888888"
		pat   = "ghp_timeout_backoff_pat"
	)

	var upstreamCalls atomic.Int64
	release := make(chan struct{})
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls.Add(1)
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(func() {
		close(release)
		gh.Close()
	})

	srv, _, alice, _, project := setupSkillAuthzTest(t)
	srv.ghResolutionStore = NewGitHubResolutionStore(enttest.NewClient(t))
	srv.config.GitHubAppConfig.APIBaseURL = gh.URL
	srv.config.GitHubAppConfig.RawBaseURL = gh.URL
	// Short client timeout so the hanging fake fails quickly with KindTimeout.
	srv.skillGitHubDoer = &skillTestHTTPDoer{client: &http.Client{Timeout: 50 * time.Millisecond}}

	useReimportCredBackend(srv, secret.ScopeProject, project.ID, map[string]string{
		"GITHUB_TOKEN": pat,
	})
	cred := srv.githubCredential(context.Background(), ghCredRequest{
		Scope: store.TemplateScopeProject, ScopeID: project.ID, Owner: owner, Repo: repo,
	})
	scope := cred.EffectiveScope()
	seedGHSkillCache(t, srv, uri, scope, sha, `"e1"`, time.Now().Add(-time.Minute), nil)

	resolve := func(t *testing.T) ResolveSkillsResponse {
		t.Helper()
		rec := doRequestAsUser(t, srv, alice, http.MethodPost, "/api/v1/skills/resolve",
			ResolveSkillsRequest{Skills: []ResolveSkillRef{{URI: uri}}, ProjectID: project.ID})
		require.Equal(t, http.StatusOK, rec.Code)
		var resp ResolveSkillsResponse
		require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
		return resp
	}

	// First resolve hits the hanging upstream, times out, records refresh failure,
	// and serves the stale entry.
	resp1 := resolve(t)
	require.Empty(t, resp1.Errors)
	require.Len(t, resp1.Resolved, 1)
	assert.Equal(t, safeShortSHA(sha), resp1.Resolved[0].ResolvedVersion)
	callsAfterFirst := upstreamCalls.Load()
	require.GreaterOrEqual(t, callsAfterFirst, int64(1))

	// Second resolve within the backoff window serves stale immediately with 0
	// additional upstream calls.
	resp2 := resolve(t)
	require.Empty(t, resp2.Errors)
	require.Len(t, resp2.Resolved, 1)
	assert.Equal(t, safeShortSHA(sha), resp2.Resolved[0].ResolvedVersion)
	assert.Equal(t, callsAfterFirst, upstreamCalls.Load(),
		"second resolve within transient-failure backoff must not call hanging upstream again")
}

// TestSkillsResolve_P4_SHAPinnedTTLBoundsRevocationForNonAnon verifies O3:
// SHA-pinned (@<40-hex>) tree rows for non-anon scopes use the 30m fresh TTL
// (DefaultResolutionCacheTTL) rather than the 24h SHA TTL, while anon keeps 24h.
func TestSkillsResolve_P4_SHAPinnedTTLBoundsRevocationForNonAnon(t *testing.T) {
	const (
		owner     = "acme"
		repo      = "sha-ttl-repo"
		skillPath = "skills/s"
		sha       = "9999999999999999999999999999999999999999"
		uri       = "gh://" + owner + "/" + repo + "/s@" + sha
		pat       = "ghp_sha_ttl_pat"
	)

	mux := http.NewServeMux()
	mux.HandleFunc(ghTreeTestPath(owner, repo, sha, skillPath), func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(ghTreeTestSingleFile))
	})
	gh := httptest.NewServer(mux)
	t.Cleanup(gh.Close)

	srv, _, _, _, project := setupSkillAuthzTest(t)
	srv.ghResolutionStore = NewGitHubResolutionStore(enttest.NewClient(t))
	wireFakeSkillGitHub(srv, gh.URL)

	fixedNow := time.Now().UTC().Truncate(time.Second)
	srv.ghNow = func() time.Time { return fixedNow }
	srv.ghRandFloat64 = func() float64 { return 0.5 } // 0% jitter = exact nominal TTL

	ctx := context.Background()
	ep := srv.skillGitHubEndpoint()

	// 1. Anon SHA-pinned ref gets 24h TTL.
	_, err := srv.resolveGitHubSkill(ctx, uri, "", nil)
	require.NoError(t, err)
	anonKey := githubfetch.TreeCacheKey(ep.APIHost(), owner, repo, sha, skillPath, githubfetch.AnonScope)
	anonEntry, hit, err := srv.ghResolutionStore.Get(ctx, anonKey)
	require.NoError(t, err)
	require.True(t, hit)
	assert.WithinDuration(t, fixedNow.Add(agent.DefaultSHAResolutionCacheTTL), anonEntry.ExpiresAt, time.Second,
		"anon SHA-pinned tree row must keep 24h TTL")

	// 2. Credentialed SHA-pinned ref gets 30m fresh TTL.
	useReimportCredBackend(srv, secret.ScopeProject, project.ID, map[string]string{
		"GITHUB_TOKEN": pat,
	})
	cred := srv.githubCredential(ctx, ghCredRequest{
		Scope: store.TemplateScopeProject, ScopeID: project.ID, Owner: owner, Repo: repo,
	})
	_, err = srv.resolveGitHubSkill(ctx, uri, project.ID, nil)
	require.NoError(t, err)
	credKey := githubfetch.TreeCacheKey(ep.APIHost(), owner, repo, sha, skillPath, cred.EffectiveScope())
	credEntry, hit, err := srv.ghResolutionStore.Get(ctx, credKey)
	require.NoError(t, err)
	require.True(t, hit)
	assert.WithinDuration(t, fixedNow.Add(agent.DefaultResolutionCacheTTL), credEntry.ExpiresAt, time.Second,
		"non-anon SHA-pinned tree row must use 30m fresh TTL to bound revocation exposure")
}

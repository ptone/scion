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
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/GoogleCloudPlatform/scion/pkg/agent"
	"github.com/GoogleCloudPlatform/scion/pkg/githubfetch"
	"github.com/GoogleCloudPlatform/scion/pkg/store"
	"github.com/GoogleCloudPlatform/scion/pkg/store/enttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// statusGitHub is a fake GitHub API whose commits and git trees endpoints
// answer with the configured status (200 serves a valid response).
type statusGitHub struct {
	*httptest.Server
	commitStatus   atomic.Int64
	contentsStatus atomic.Int64
	commitCalls    atomic.Int64
	contentsCalls  atomic.Int64
}

func newStatusGitHub(t *testing.T, owner, repo, skillPath, commitSHA string) *statusGitHub {
	t.Helper()
	f := &statusGitHub{}
	f.commitStatus.Store(http.StatusOK)
	f.contentsStatus.Store(http.StatusOK)
	commitsPrefix := "/repos/" + owner + "/" + repo + "/commits/"
	treesPrefix := "/repos/" + owner + "/" + repo + "/git/trees/"
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, commitsPrefix):
			f.commitCalls.Add(1)
			if st := int(f.commitStatus.Load()); st != http.StatusOK {
				http.Error(w, `{"message":"No commit found"}`, st)
				return
			}
			_, _ = w.Write([]byte(commitSHA))
		case strings.HasPrefix(r.URL.Path, treesPrefix):
			assert.True(t, strings.HasPrefix(strings.TrimPrefix(r.URL.Path, treesPrefix), commitSHA+":"+skillPath),
				"the tree must be listed at the resolved commit: %s", r.URL.Path)
			f.contentsCalls.Add(1)
			if st := int(f.contentsStatus.Load()); st != http.StatusOK {
				http.Error(w, `{"message":"Not Found"}`, st)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(ghTreeTestSingleFile))
		default:
			t.Errorf("unexpected GitHub API request: %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(f.Close)
	return f
}

func (f *statusGitHub) calls() int64 { return f.commitCalls.Load() + f.contentsCalls.Load() }

// A ref GitHub reports as not found is asked for once: a second resolve
// request within the TTL gets the same per-URI error without a GitHub call.
// Another ref (another cache key) is still resolved against GitHub.
func TestSkillsResolve_GHNotFoundIsRemembered(t *testing.T) {
	const (
		owner     = "acme"
		repo      = "missing-repo"
		skillPath = "skills/gone"
		uri       = "gh://" + owner + "/" + repo + "/gone@no-such-branch"
		otherURI  = "gh://" + owner + "/" + repo + "/gone@other-branch"
		commitSHA = "abababababababababababababababababababab"
	)

	srv, _, alice, _, project := setupSkillAuthzTest(t)
	srv.ghResolutionStore = NewGitHubResolutionStore(enttest.NewClient(t))
	gh := newStatusGitHub(t, owner, repo, skillPath, commitSHA)
	gh.commitStatus.Store(http.StatusNotFound)
	wireFakeSkillGitHub(srv, gh.URL)

	resolveErr := func(t *testing.T, u string) ResolveSkillError {
		t.Helper()
		rec := doRequestAsUser(t, srv, alice, http.MethodPost, "/api/v1/skills/resolve",
			ResolveSkillsRequest{Skills: []ResolveSkillRef{{URI: u}}, ProjectID: project.ID})
		require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
		var resp ResolveSkillsResponse
		require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
		require.Empty(t, resp.Resolved)
		require.Len(t, resp.Errors, 1)
		return resp.Errors[0]
	}

	first := resolveErr(t, uri)
	require.Equal(t, int64(1), gh.calls(), "the first resolve asks GitHub")
	assert.Equal(t, "skill not found", first.Message)
	assert.Equal(t, http.StatusNotFound, first.Status)

	second := resolveErr(t, uri)
	assert.Equal(t, int64(1), gh.calls(), "the remembered not found must be served without a GitHub call")
	assert.NotEmpty(t, second.CorrelationID)
	first.CorrelationID, second.CorrelationID = "", ""
	assert.Equal(t, first, second, "the remembered failure must be reported as the original one")

	resolveErr(t, otherURI)
	assert.Equal(t, int64(2), gh.calls(), "another ref must still be resolved against GitHub")
}

// A GitHub 404, 401, or 403 is mapped to its ResolveSkillError code (also when
// served from the negative memo). Other GitHub failures keep resolve_failed.
func TestSkillsResolve_GHNotFoundCode(t *testing.T) {
	const (
		owner     = "acme"
		repo      = "code-repo"
		skillPath = "skills/thing"
		commitSHA = "efefefefefefefefefefefefefefefefefefefef"
	)
	cases := []struct {
		name           string
		uri            string
		commitStatus   int
		contentsStatus int
		wantCode       string
	}{
		{"missing ref", "gh://" + owner + "/" + repo + "/thing@no-such-branch", http.StatusNotFound, http.StatusOK, agent.SkillErrCodeNotFound},
		{"missing skill path", "gh://" + owner + "/" + repo + "/thing@main", http.StatusOK, http.StatusNotFound, agent.SkillErrCodeNotFound},
		{"unauthorized ref", "gh://" + owner + "/" + repo + "/thing@unauth", http.StatusUnauthorized, http.StatusOK, agent.SkillErrCodeCredentialRejected},
		{"forbidden ref", "gh://" + owner + "/" + repo + "/thing@forbid", http.StatusForbidden, http.StatusOK, agent.SkillErrCodeCredentialRejected},
		{"unprocessable ref", "gh://" + owner + "/" + repo + "/thing@bad", http.StatusUnprocessableEntity, http.StatusOK, agent.SkillErrCodeResolveFailed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, _, alice, _, project := setupSkillAuthzTest(t)
			srv.ghResolutionStore = NewGitHubResolutionStore(enttest.NewClient(t))
			gh := newStatusGitHub(t, owner, repo, skillPath, commitSHA)
			gh.commitStatus.Store(int64(tc.commitStatus))
			gh.contentsStatus.Store(int64(tc.contentsStatus))
			wireFakeSkillGitHub(srv, gh.URL)

			for i := 0; i < 2; i++ {
				rec := doRequestAsUser(t, srv, alice, http.MethodPost, "/api/v1/skills/resolve",
					ResolveSkillsRequest{Skills: []ResolveSkillRef{{URI: tc.uri}}, ProjectID: project.ID})
				require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
				var resp ResolveSkillsResponse
				require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
				require.Empty(t, resp.Resolved)
				require.Len(t, resp.Errors, 1)
				assert.Equal(t, tc.wantCode, resp.Errors[0].Code, "request %d: %s", i+1, resp.Errors[0].Message)
			}
		})
	}
}

// A skill path that is missing at an existing ref is remembered too.
func TestResolveGitHubSkill_MissingSkillPathIsRemembered(t *testing.T) {
	const (
		owner     = "acme"
		repo      = "path-repo"
		skillPath = "skills/absent"
		uri       = "gh://" + owner + "/" + repo + "/absent@main"
		commitSHA = "cdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcd"
	)

	srv, _, _, _, project := setupSkillAuthzTest(t)
	srv.ghResolutionStore = NewGitHubResolutionStore(enttest.NewClient(t))
	gh := newStatusGitHub(t, owner, repo, skillPath, commitSHA)
	gh.contentsStatus.Store(http.StatusNotFound)
	wireFakeSkillGitHub(srv, gh.URL)

	_, err := srv.resolveGitHubSkill(context.Background(), uri, project.ID, nil)
	require.Error(t, err)
	require.Equal(t, int64(1), gh.contentsCalls.Load())

	_, err2 := srv.resolveGitHubSkill(context.Background(), uri, project.ID, nil)
	require.Error(t, err2)
	assert.Equal(t, err.Error(), err2.Error())
	assert.Equal(t, int64(1), gh.commitCalls.Load(), "no new commit lookup")
	assert.Equal(t, int64(1), gh.contentsCalls.Load(), "no new contents lookup")
}

// Transient statuses (5xx, 422) may succeed on the next attempt, so they are
// never remembered: every resolve asks GitHub again.
func TestResolveGitHubSkill_OtherFailuresAreNotRemembered(t *testing.T) {
	for _, status := range []int{http.StatusInternalServerError, http.StatusBadGateway, http.StatusUnprocessableEntity} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			const (
				owner     = "acme"
				repo      = "flaky-repo"
				skillPath = "skills/flaky"
				uri       = "gh://" + owner + "/" + repo + "/flaky@main"
				commitSHA = "efefefefefefefefefefefefefefefefefefefef"
			)

			srv, _, _, _, project := setupSkillAuthzTest(t)
			srv.ghResolutionStore = NewGitHubResolutionStore(enttest.NewClient(t))
			gh := newStatusGitHub(t, owner, repo, skillPath, commitSHA)
			gh.commitStatus.Store(int64(status))
			wireFakeSkillGitHub(srv, gh.URL)

			for i := 0; i < 2; i++ {
				_, err := srv.resolveGitHubSkill(context.Background(), uri, project.ID, nil)
				require.Error(t, err)
			}
			// 5xx retries once per resolve (MaxRetries: 1), 422 does not retry.
			assert.GreaterOrEqual(t, gh.commitCalls.Load(), int64(2), "each resolve must ask GitHub")
			assert.Zero(t, srv.ghFailures.Len(), "nothing may be remembered")
		})
	}
}

// A successful fetch drops a remembered failure for its key.
func TestFetchAndCacheGitHubSkill_SuccessClearsRememberedFailure(t *testing.T) {
	const (
		owner     = "acme"
		repo      = "fixed-repo"
		skillPath = "skills/fixed"
		uri       = "gh://" + owner + "/" + repo + "/fixed@main"
		commitSHA = "1212121212121212121212121212121212121212"
	)

	srv, _, _, _, _ := setupSkillAuthzTest(t)
	srv.ghResolutionStore = NewGitHubResolutionStore(enttest.NewClient(t))
	gh := newStatusGitHub(t, owner, repo, skillPath, commitSHA)
	wireFakeSkillGitHub(srv, gh.URL)

	ep := srv.skillGitHubEndpoint()
	ghRef, err := agent.ParseGitHubSkillURI(uri)
	require.NoError(t, err)
	refKey := githubfetch.RefCacheKey(ep.APIHost(), ghRef.Owner, ghRef.Repo, ghRef.Ref, githubfetch.AnonScope)
	flightKey := refKey + ":" + ghRef.SkillPath

	srv.ghFailures.Record(flightKey, errors.New("remembered"))
	require.Error(t, srv.ghFailures.Recent(flightKey))

	entry, err := srv.fetchAndCacheGitHubSkill(
		context.Background(), ep, flightKey, refKey, uri, ghRef,
		githubfetch.Credential{Scope: githubfetch.AnonScope, Kind: githubfetch.CredAnonymous},
		true, nil, nil, nil, newGHSHAMemo(),
	)
	require.NoError(t, err)
	require.NotNil(t, entry)
	assert.NoError(t, srv.ghFailures.Recent(flightKey), "a success must clear the remembered failure")
}

func TestIsGHAuthoritativeDenialAndTransient(t *testing.T) {
	for _, kind := range []githubfetch.Kind{githubfetch.KindNotFound, githubfetch.KindAuthRejected, githubfetch.KindForbidden} {
		err := &githubfetch.Error{Kind: kind}
		assert.True(t, isGHAuthoritativeDenial(err), "kind %s", kind)
		assert.True(t, isGHAuthoritativeDenial(fmt.Errorf("wrapped: %w", err)), "wrapped %s", kind)
		assert.False(t, isGHTransientFailure(err), "kind %s must not be transient", kind)
	}
	for _, kind := range []githubfetch.Kind{githubfetch.KindRateLimited, githubfetch.KindTimeout, githubfetch.KindUnreachable, githubfetch.KindUpstream} {
		err := &githubfetch.Error{Kind: kind}
		assert.True(t, isGHTransientFailure(err), "kind %s", kind)
		assert.False(t, isGHAuthoritativeDenial(err), "kind %s must not be authoritative denial", kind)
	}
	assert.False(t, isGHAuthoritativeDenial(nil))
	assert.False(t, isGHTransientFailure(nil))
}

// A caller that passed the pre-check before a concurrent flight recorded a
// 404 must get the remembered failure from the re-check inside its own
// flight, not ask GitHub again.
func TestResolveGitHubSkill_FlightRecheckServesRememberedFailure(t *testing.T) {
	const (
		owner     = "acme"
		repo      = "race-repo"
		skillPath = "skills/gone"
		uri       = "gh://" + owner + "/" + repo + "/gone@no-such-branch"
		commitSHA = "3434343434343434343434343434343434343434"
	)

	srv, _, _, _, project := setupSkillAuthzTest(t)
	srv.ghResolutionStore = NewGitHubResolutionStore(enttest.NewClient(t))
	gh := newStatusGitHub(t, owner, repo, skillPath, commitSHA)
	gh.commitStatus.Store(http.StatusNotFound)
	wireFakeSkillGitHub(srv, gh.URL)

	held := make(chan struct{})
	release := make(chan struct{})
	var joins atomic.Int64
	hook := func(string) {
		if joins.Add(1) == 1 {
			close(held)
			<-release
		}
	}
	ghFlightJoinHook.Store(&hook)
	t.Cleanup(func() { ghFlightJoinHook.Store(nil) })

	doneB := make(chan error, 1)
	go func() {
		_, err := srv.resolveGitHubSkill(context.Background(), uri, project.ID, nil)
		doneB <- err
	}()
	select {
	case <-held:
	case <-time.After(5 * time.Second):
		t.Fatal("caller B never reached the join point")
	}

	_, errA := srv.resolveGitHubSkill(context.Background(), uri, project.ID, nil)
	require.Error(t, errA)
	require.Equal(t, int64(1), gh.commitCalls.Load())

	close(release)
	var errB error
	select {
	case errB = <-doneB:
	case <-time.After(5 * time.Second):
		t.Fatal("caller B did not return")
	}
	require.Error(t, errB)
	assert.Equal(t, errA.Error(), errB.Error())
	assert.Equal(t, int64(1), gh.commitCalls.Load(), "B's flight must not ask GitHub again")
}

// For an anon scope, a stale entry is served ahead of a remembered failure,
// and its background refresh returning 404 deletes the stale entry.
func TestResolveGitHubSkill_StaleEntryWinsOverRememberedFailure(t *testing.T) {
	const (
		owner     = "acme"
		repo      = "stale-fail-repo"
		skillPath = "skills/widget"
		uri       = "gh://" + owner + "/" + repo + "/widget@main"
		staleSHA  = "5656565656565656565656565656565656565656"
	)

	srv, _, _, _, project := setupSkillAuthzTest(t)
	srv.ghResolutionStore = NewGitHubResolutionStore(enttest.NewClient(t))
	gh := newStatusGitHub(t, owner, repo, skillPath, staleSHA)
	gh.commitStatus.Store(http.StatusNotFound)
	wireFakeSkillGitHub(srv, gh.URL)

	ctx := context.Background()
	refKey, _, flightKey := seedGHSkillCache(t, srv, uri, githubfetch.AnonScope, staleSHA, `"v1"`, time.Now().Add(-time.Minute), nil)
	srv.ghFailures.Record(flightKey, errors.New("remembered not found"))

	resp, err := srv.resolveGitHubSkill(ctx, uri, project.ID, nil)
	require.NoError(t, err, "the anon stale entry must be served immediately via SWR")
	assert.Equal(t, safeShortSHA(staleSHA), resp.ResolvedVersion)

	// Wait for the background refresh goroutine to reach GitHub and finish its
	// flight; its 404 must delete the cached rows.
	require.Eventually(t, func() bool { return gh.commitCalls.Load() >= 1 }, 5*time.Second, 10*time.Millisecond,
		"background refresh must reach GitHub")
	_, _, _ = srv.ghResolveFlight.Do(flightKey, func() (interface{}, error) { return nil, nil })
	_, ok, err := srv.ghResolutionStore.GetStale(ctx, refKey, agent.DefaultResolutionCacheTTL, agent.MaxResolutionStaleAge)
	require.NoError(t, err)
	assert.False(t, ok, "background 404 must purge the stale ref row")
}

// A 404 remembered for an anonymous scope is not served to the same project
// once it is backed by an installation: that is another cache key, so GitHub is asked again.
func TestResolveGitHubSkill_PublicNotFoundNotServedToInstallation(t *testing.T) {
	const (
		instID = int64(515151)
		uri    = "gh://acme/private/s@main"
	)
	srv, s, _, _, project := setupSkillAuthzTest(t)
	srv.ghResolutionStore = NewGitHubResolutionStore(enttest.NewClient(t))
	srv.ghCooldown = githubfetch.NewCooldown(time.Now)

	var repoCalls atomic.Int64
	mux := http.NewServeMux()
	mux.HandleFunc("/app", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"id":          1,
			"slug":        "skill-fail-cache-app",
			"permissions": map[string]string{"contents": "write", "pull_requests": "write", "issues": "write", "metadata": "read"},
		})
	})
	mux.HandleFunc("/app/installations/", func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/access_tokens") {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"token":      "ghs_test_value",
			"expires_at": time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
		})
	})
	mux.HandleFunc("/repos/", func(w http.ResponseWriter, r *http.Request) {
		repoCalls.Add(1)
		http.NotFound(w, r)
	})
	gh := httptest.NewServer(mux)
	t.Cleanup(gh.Close)
	wireFakeSkillGitHub(srv, gh.URL)
	srv.config.GitHubAppConfig.AppID = 1
	srv.config.GitHubAppConfig.PrivateKey = generateTestGitHubAppKey(t)

	ctx := context.Background()
	_, err := srv.resolveGitHubSkill(ctx, uri, project.ID, nil)
	require.Error(t, err)
	require.Equal(t, int64(1), repoCalls.Load())
	_, err = srv.resolveGitHubSkill(ctx, uri, project.ID, nil)
	require.Error(t, err)
	require.Equal(t, int64(1), repoCalls.Load(), "the anon 404 is remembered")

	require.NoError(t, s.CreateGitHubInstallation(ctx, &store.GitHubInstallation{
		InstallationID: instID,
		AccountLogin:   "acme",
		AccountType:    "Organization",
		AppID:          1,
		Status:         store.GitHubInstallationStatusActive,
	}))
	bindProjectForTest(t, srv, s, project, instID, "acme", "private")

	_, err = srv.resolveGitHubSkill(ctx, uri, project.ID, nil)
	require.Error(t, err)
	assert.Equal(t, int64(2), repoCalls.Load(),
		"an installation-backed resolution must not be served the anon scope's remembered 404")
}

// A large 404 body is never copied into the remembered githubfetch.Error.
func TestResolveGitHubSkill_RememberedErrorBodyIsBounded(t *testing.T) {
	const uri = "gh://acme/big-page/s@main"
	srv, _, _, _, project := setupSkillAuthzTest(t)
	srv.ghResolutionStore = NewGitHubResolutionStore(enttest.NewClient(t))

	page := strings.Repeat("<p>not found</p>", 64*1024) // about 1 MiB
	var hits atomic.Int64
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(page))
	}))
	t.Cleanup(gh.Close)
	wireFakeSkillGitHub(srv, gh.URL)

	_, err := srv.resolveGitHubSkill(context.Background(), uri, project.ID, nil)
	require.Error(t, err)
	require.Equal(t, 1, srv.ghFailures.Len())

	_, err = srv.resolveGitHubSkill(context.Background(), uri, project.ID, nil)
	require.Error(t, err)
	require.Equal(t, int64(1), hits.Load(), "the second resolve must not ask GitHub")
	assert.LessOrEqual(t, len(err.Error()), 512,
		"a remembered error must not carry the raw response body")
	assert.NotContains(t, err.Error(), "<p>not found</p>")
}

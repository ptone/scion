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
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/GoogleCloudPlatform/scion/pkg/agent"
	"github.com/GoogleCloudPlatform/scion/pkg/githubfetch"
)

// ghTreeTestSingleFile is a git trees API response listing one regular,
// non-executable SKILL.md, the listing most fake GitHub servers serve.
const ghTreeTestSingleFile = `{"sha":"t","truncated":false,"tree":[{"path":"SKILL.md","mode":"100644","type":"blob","sha":"x","size":1}]}`

// ghTreeTestPath is the request path of the recursive git tree of skillPath
// at commitSHA (the query string is not part of a ServeMux pattern).
func ghTreeTestPath(owner, repo, commitSHA, skillPath string) string {
	return "/repos/" + owner + "/" + repo + "/git/trees/" + commitSHA + ":" + skillPath
}

// skillTestHTTPDoer adapts an *http.Client to githubfetch.Doer so Hub skill
// tests can exercise local httptest fakes without relaxing remotefetch's
// production SSRF, HTTPS, and IP-denylist controls.
type skillTestHTTPDoer struct {
	client *http.Client
}

func (d *skillTestHTTPDoer) Open(ctx context.Context, r githubfetch.Request) (*githubfetch.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.URL, nil)
	if err != nil {
		return nil, err
	}
	if r.Authorization != "" {
		req.Header.Set("Authorization", r.Authorization)
	}
	for k, vals := range r.Header {
		for _, v := range vals {
			req.Header.Add(k, v)
		}
	}
	c := d.client
	if c == nil {
		c = http.DefaultClient
	}
	resp, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	return &githubfetch.Response{
		Status: resp.StatusCode,
		Header: resp.Header.Clone(),
		Body:   resp.Body,
	}, nil
}

// wireFakeSkillGitHub configures srv's skill endpoint to target baseURL via
// skillTestHTTPDoer. Tests verifying hosted NetworkVetted refusal leave
// srv.skillGitHubDoer nil.
func wireFakeSkillGitHub(srv *Server, baseURL string) {
	srv.config.GitHubAppConfig.APIBaseURL = baseURL
	srv.config.GitHubAppConfig.RawBaseURL = baseURL
	srv.skillGitHubDoer = &skillTestHTTPDoer{}
}

// seedGHSkillCache populates the ref (for non-SHA refs) and tree rows for uri
// under scope in srv.ghResolutionStore, returning (refKey, treeKey, flightKey).
func seedGHSkillCache(
	t *testing.T,
	srv *Server,
	uri, scope, commitSHA, etag string,
	expiresAt time.Time,
	files []GitHubFileEntry,
) (refKey, treeKey, flightKey string) {
	t.Helper()
	ghRef, err := agent.ParseGitHubSkillURI(uri)
	require.NoError(t, err)
	if ghRef.Ref == "" {
		ghRef.Ref = "HEAD"
	}
	if scope == "" {
		scope = githubfetch.AnonScope
	}
	if len(files) == 0 {
		files = []GitHubFileEntry{{Path: "SKILL.md", URL: "http://example.invalid/SKILL.md", Hash: "x", Size: 1, Mode: "0644"}}
	}
	ep := srv.skillGitHubEndpoint()
	apiHost := ep.APIHost()
	ctx := context.Background()

	treeKey = githubfetch.TreeCacheKey(apiHost, ghRef.Owner, ghRef.Repo, commitSHA, ghRef.SkillPath, scope)
	treeExpiresAt := expiresAt
	if !githubfetch.IsFullCommitSHA(ghRef.Ref) {
		refKey = githubfetch.RefCacheKey(apiHost, ghRef.Owner, ghRef.Repo, ghRef.Ref, scope)
		flightKey = refKey + ":" + strings.Trim(ghRef.SkillPath, "/")
		require.NoError(t, srv.ghResolutionStore.Put(ctx, refKey, GitHubCacheEntry{
			CommitSHA:   commitSHA,
			BundleHash:  "ref:" + commitSHA,
			TokenScope:  scope,
			ETag:        etag,
			ExpiresAt:   expiresAt,
			OriginalURI: uri,
		}))
		// Keep the tree row fresh unless the caller explicitly passed a past
		// expiry for a modeless test.
		if fileEntriesHaveModes(files) && time.Now().After(treeExpiresAt) {
			treeExpiresAt = time.Now().Add(time.Hour)
		}
	} else {
		flightKey = treeKey
	}

	require.NoError(t, srv.ghResolutionStore.Put(ctx, treeKey, GitHubCacheEntry{
		CommitSHA:   commitSHA,
		FileEntries: files,
		BundleHash:  computeBundleHash(files),
		TokenScope:  scope,
		ExpiresAt:   treeExpiresAt,
		OriginalURI: uri,
	}))
	return refKey, treeKey, flightKey
}

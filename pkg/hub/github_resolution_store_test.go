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
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/GoogleCloudPlatform/scion/pkg/githubfetch"
)

// TestGitHubSkillCacheKeys tests canonical ref and tree cache keys from githubfetch.
func TestGitHubSkillCacheKeys(t *testing.T) {
	ep := githubfetch.DefaultEndpoint()
	key1 := githubfetch.RefCacheKey(ep.APIHost(), "owner", "repo", "main", githubfetch.AnonScope)
	key2 := githubfetch.RefCacheKey(ep.APIHost(), "owner", "repo", "main", githubfetch.AnonScope)
	require.Equal(t, key1, key2, "same inputs should produce same key")

	key3 := githubfetch.RefCacheKey(ep.APIHost(), "owner", "repo", "main", "app:12345")
	require.NotEqual(t, key1, key3, "different token scope should produce different key")

	key4 := githubfetch.RefCacheKey(ep.APIHost(), "Owner", "Repo", "main", githubfetch.AnonScope)
	require.Equal(t, key1, key4, "owner/repo should be lowercased")

	empty := githubfetch.RefCacheKey(ep.APIHost(), "acme", "skills", "", githubfetch.AnonScope)
	head := githubfetch.RefCacheKey(ep.APIHost(), "acme", "skills", "HEAD", githubfetch.AnonScope)
	require.NotEqual(t, empty, head)
}

// TestSkillEndpointRawURL tests permanent raw URL construction via githubfetch.Endpoint.
func TestSkillEndpointRawURL(t *testing.T) {
	const commitSHA = "abcdef1234567890abcdef1234567890abcdef12"
	cases := []struct {
		name      string
		rawBase   string
		owner     string
		repo      string
		skillPath string
		filePath  string
		want      string
	}{
		{
			name:      "simple path",
			rawBase:   "https://raw.githubusercontent.com",
			owner:     "acme",
			repo:      "skills",
			skillPath: "skills/deploy",
			filePath:  "SKILL.md",
			want:      "https://raw.githubusercontent.com/acme/skills/" + commitSHA + "/skills/deploy/SKILL.md",
		},
		{
			name:      "trailing slash on base is not doubled",
			rawBase:   "https://raw.githubusercontent.com/",
			owner:     "acme",
			repo:      "skills",
			skillPath: "",
			filePath:  "SKILL.md",
			want:      "https://raw.githubusercontent.com/acme/skills/" + commitSHA + "/SKILL.md",
		},
		{
			name:      "spaces are escaped but separators are preserved",
			rawBase:   "https://raw.githubusercontent.com",
			owner:     "acme",
			repo:      "skills",
			skillPath: "my skills",
			filePath:  "read me.md",
			want:      "https://raw.githubusercontent.com/acme/skills/" + commitSHA + "/my%20skills/read%20me.md",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ep := githubfetch.Endpoint{RawBase: tc.rawBase}
			src := githubfetch.Source{Owner: tc.owner, Repo: tc.repo, Path: tc.skillPath}
			require.Equal(t, tc.want, ep.RawURL(src, commitSHA, tc.filePath))
		})
	}
}

// TestSkillListTree_FilesModesAndURLs pins what the hub reads from the recursive
// git tree of a skill directory via githubfetch.Client.ListTree.
func TestSkillListTree_FilesModesAndURLs(t *testing.T) {
	const commitSHA = "abcdef1234567890abcdef1234567890abcdef12"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/repos/acme/skills/git/trees/"+commitSHA+":skills/deploy", r.URL.Path)
		require.Equal(t, "1", r.URL.Query().Get("recursive"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"sha":"t","truncated":false,"tree":[
			{"path":"SKILL.md","mode":"100644","type":"blob","sha":"ce013625030ba8dba906f756967f9e9ca394464a","size":6},
			{"path":"scripts","mode":"040000","type":"tree","sha":"1111111111111111111111111111111111111111"},
			{"path":"scripts/run.sh","mode":"100755","type":"blob","sha":"95d09f2b10159347eece71399a7e2e907ea3df4f","size":11},
			{"path":"link","mode":"120000","type":"blob","sha":"2222222222222222222222222222222222222222","size":8}
		]}`))
	}))
	defer srv.Close()

	ep := githubfetch.Endpoint{APIBase: srv.URL, RawBase: "https://raw.githubusercontent.com"}
	client := githubfetch.New(githubfetch.Options{
		Endpoint: ep,
		Doer:     &skillTestHTTPDoer{client: srv.Client()},
		Cooldown: githubfetch.NewCooldown(nil),
	})
	src := githubfetch.Source{Owner: "acme", Repo: "skills", Path: "skills/deploy"}
	tree, err := client.ListTree(context.Background(), src, commitSHA, githubfetch.Credential{Scope: githubfetch.AnonScope}, githubfetch.SkillLimits())
	require.NoError(t, err)
	require.Len(t, tree.Files, 2)
	require.Equal(t, "SKILL.md", tree.Files[0].Path)
	require.Equal(t, "0644", fmt.Sprintf("%04o", tree.Files[0].Mode))
	require.Equal(t, "https://raw.githubusercontent.com/acme/skills/"+commitSHA+"/skills/deploy/SKILL.md", ep.RawURL(src, commitSHA, tree.Files[0].Path))
	require.Equal(t, "scripts/run.sh", tree.Files[1].Path)
	require.Equal(t, "0755", fmt.Sprintf("%04o", tree.Files[1].Mode))
	require.Equal(t, "https://raw.githubusercontent.com/acme/skills/"+commitSHA+"/skills/deploy/scripts/run.sh", ep.RawURL(src, commitSHA, tree.Files[1].Path))
}

// TestSkillListTree_RejectsUnusableTrees covers the listings the hub refuses
// instead of caching an incomplete or unsafe skill.
func TestSkillListTree_RejectsUnusableTrees(t *testing.T) {
	const commitSHA = "abcdef1234567890abcdef1234567890abcdef12"
	cases := []struct {
		name     string
		body     string
		wantKind githubfetch.Kind
	}{
		{
			name:     "truncated",
			body:     `{"truncated":true,"tree":[{"path":"SKILL.md","mode":"100644","type":"blob","sha":"x","size":1}]}`,
			wantKind: githubfetch.KindTooLarge,
		},
		{
			name:     "submodule",
			body:     `{"truncated":false,"tree":[{"path":"SKILL.md","mode":"100644","type":"blob","sha":"x","size":1},{"path":"vendor","mode":"160000","type":"commit","sha":"y"}]}`,
			wantKind: githubfetch.KindUnsupportedContent,
		},
		{
			name:     "more than the file limit",
			body:     ghManyFilesTree(githubfetch.SkillLimits().MaxFiles + 1),
			wantKind: githubfetch.KindTooLarge,
		},
		{
			name:     "parent segment",
			body:     `{"truncated":false,"tree":[{"path":"../SKILL.md","mode":"100644","type":"blob","sha":"x","size":1}]}`,
			wantKind: githubfetch.KindUnsupportedContent,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()

			ep := githubfetch.Endpoint{APIBase: srv.URL, RawBase: "https://raw.githubusercontent.com"}
			client := githubfetch.New(githubfetch.Options{
				Endpoint: ep,
				Doer:     &skillTestHTTPDoer{client: srv.Client()},
				Cooldown: githubfetch.NewCooldown(nil),
			})
			_, err := client.ListTree(context.Background(), githubfetch.Source{Owner: "acme", Repo: "skills", Path: "skills/deploy"}, commitSHA, githubfetch.Credential{Scope: githubfetch.AnonScope}, githubfetch.SkillLimits())
			require.Error(t, err)
			var ghe *githubfetch.Error
			require.ErrorAs(t, err, &ghe)
			require.Equal(t, tc.wantKind, ghe.Kind)
		})
	}
}

// ghManyFilesTree is a git trees API response listing n regular files.
func ghManyFilesTree(n int) string {
	var b strings.Builder
	b.WriteString(`{"truncated":false,"tree":[`)
	for i := 0; i < n; i++ {
		if i > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, `{"path":"f%04d.md","mode":"100644","type":"blob","sha":"a","size":1}`, i)
	}
	b.WriteString("]}")
	return b.String()
}

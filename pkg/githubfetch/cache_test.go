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

package githubfetch

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/GoogleCloudPlatform/scion/pkg/artifacts/remotefetch"
)

func TestCacheKeyAndPrefixes(t *testing.T) {
	ep := DefaultEndpoint()
	ghe := Endpoint{APIBase: "https://GHE.Example.COM/api/v3"}

	const sha = "abcdef1234567890abcdef1234567890abcdef12"

	if got := CacheKey(CacheKeyRef, ep.APIHost(), "Acme", "Skills", "Main", "ignored", "app:42"); got != "ref:api.github.com/acme/skills@Main#app:42" {
		t.Fatalf("CacheKey(ref) = %q", got)
	}
	if got := CacheKey(CacheKeyTree, ep.APIHost(), "Acme", "Skills", sha, "/skills/Deploy/", "app:42"); got != "tree:api.github.com/acme/skills@"+sha+":skills/Deploy#app:42" {
		t.Fatalf("CacheKey(tree) = %q", got)
	}
	if got := CacheKey(CacheKeyRef, ghe.APIHost(), "Acme", "Skills", "feat/x/y", "", ""); got != "ref:ghe.example.com/acme/skills@feat/x/y#anon" {
		t.Fatalf("CacheKey(ref, GHE, empty scope) = %q", got)
	}

	refPfx, treePfx := RepoCacheKeyPrefixes(ghe.APIHost(), "Acme", "Skills")
	if refPfx != "ref:ghe.example.com/acme/skills@" {
		t.Fatalf("refPrefix = %q", refPfx)
	}
	if treePfx != "tree:ghe.example.com/acme/skills@" {
		t.Fatalf("treePrefix = %q", treePfx)
	}
	// Sibling repo with shared name prefix must not match because of trailing '@'.
	siblingRef := RefCacheKey(ghe.APIHost(), "Acme", "Skills-Extra", "main", "app:1")
	if strings.HasPrefix(siblingRef, refPfx) {
		t.Fatalf("sibling repo %q must not match prefix %q", siblingRef, refPfx)
	}
}

func TestResolveRefConditional_NotModified(t *testing.T) {
	const sha = "1234567890abcdef1234567890abcdef12345678"

	cases := []struct {
		name string
		doer func(gotINM *string) Doer
	}{
		{
			name: "doer returns 304 response",
			doer: func(gotINM *string) Doer {
				return doerFunc(func(_ context.Context, r Request) (*Response, error) {
					*gotINM = r.Header.Get("If-None-Match")
					return &Response{
						Status: http.StatusNotModified,
						Header: http.Header{"Etag": {`"v2"`}},
						Body:   io.NopCloser(strings.NewReader("")),
					}, nil
				})
			},
		},
		{
			name: "remotefetch returns ReasonStatus 304 error",
			doer: func(gotINM *string) Doer {
				return doerFunc(func(_ context.Context, r Request) (*Response, error) {
					*gotINM = r.Header.Get("If-None-Match")
					return nil, &remotefetch.Error{
						Reason: remotefetch.ReasonStatus,
						Status: http.StatusNotModified,
						Header: http.Header{"Etag": {`"v2"`}},
					}
				})
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var gotINM string
			c := New(Options{Doer: tc.doer(&gotINM), Cooldown: NewCooldown(nil)})
			src := Source{Owner: "acme", Repo: "skills", Ref: "feat/a", Path: "skills/x"}
			res, outSrc, notMod, err := c.ResolveRefConditional(context.Background(), src, Credential{Scope: AnonScope}, `"v1"`)
			if err != nil {
				t.Fatalf("ResolveRefConditional err = %v", err)
			}
			if !notMod {
				t.Fatalf("expected notModified = true")
			}
			if gotINM != `"v1"` {
				t.Fatalf("If-None-Match = %q, want %q", gotINM, `"v1"`)
			}
			if res.ETag != `"v2"` {
				t.Fatalf("ETag = %q, want %q", res.ETag, `"v2"`)
			}
			if outSrc.Owner != src.Owner || outSrc.Repo != src.Repo || outSrc.Ref != src.Ref || outSrc.Path != src.Path {
				t.Fatalf("outSrc = %+v, want %+v", outSrc, src)
			}
		})
	}

	t.Run("200 returns new SHA and ETag", func(t *testing.T) {
		var gotINM string
		c := New(Options{
			Doer: doerFunc(func(_ context.Context, r Request) (*Response, error) {
				gotINM = r.Header.Get("If-None-Match")
				return &Response{
					Status: http.StatusOK,
					Header: http.Header{"Etag": {`"v3"`}},
					Body:   io.NopCloser(strings.NewReader(sha + "\n")),
				}, nil
			}),
			Cooldown: NewCooldown(nil),
		})
		src := Source{Owner: "acme", Repo: "skills", Ref: "main", Path: "skills/x"}
		res, _, notMod, err := c.ResolveRefConditional(context.Background(), src, Credential{Scope: AnonScope}, `"v1"`)
		if err != nil {
			t.Fatalf("err = %v", err)
		}
		if notMod {
			t.Fatalf("expected notModified = false")
		}
		if gotINM != `"v1"` || res.CommitSHA != sha || res.ETag != `"v3"` {
			t.Fatalf("got INM=%q SHA=%q ETag=%q", gotINM, res.CommitSHA, res.ETag)
		}
	})
}

type doerFunc func(ctx context.Context, r Request) (*Response, error)

func (f doerFunc) Open(ctx context.Context, r Request) (*Response, error) {
	return f(ctx, r)
}

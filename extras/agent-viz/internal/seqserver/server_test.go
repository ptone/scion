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

package seqserver

import (
	"encoding/json"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/GoogleCloudPlatform/scion/extras/agent-viz/internal/digest"
)

const testIndex = "<!doctype html><title>app</title><scion-seq-viz></scion-seq-viz>"

func testAssets() fs.FS {
	return fstest.MapFS{
		"index.html":      {Data: []byte(testIndex)},
		"assets/index.js": {Data: []byte("console.log(1)")},
	}
}

func testServer(t *testing.T) *Server {
	t.Helper()
	d, err := digest.BuildSyntheticDigest(1, 6, 120_000, digest.DefaultOptions())
	if err != nil {
		t.Fatalf("BuildSyntheticDigest: %v", err)
	}
	return New(d)
}

// get issues a request without following redirects, so a redirect is visible
// as a redirect rather than silently resolved.
func get(t *testing.T, h http.Handler, target string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
	return rec
}

// TestSPAFallbackServesApp is a regression test for an endless redirect: the
// handler used to rewrite unknown paths to "/index.html" and re-enter
// http.FileServer, which redirects anything ending in "index.html" to "./".
// For a deep link like /run/abc that produced a 301 chain instead of the app.
func TestSPAFallbackServesApp(t *testing.T) {
	h := testServer(t).staticHandler(testAssets())

	paths := []string{
		"/",
		"/index.html",
		"/foo",
		"/run/abc",
		"/run/abc/at/12345",
		"/deeply/nested/client/route",
	}
	for _, p := range paths {
		rec := get(t, h, p)
		if rec.Code != http.StatusOK {
			t.Errorf("GET %s: status = %d, want 200 (Location: %q)",
				p, rec.Code, rec.Header().Get("Location"))
			continue
		}
		if !strings.Contains(rec.Body.String(), "scion-seq-viz") {
			t.Errorf("GET %s: body did not contain the app root element", p)
		}
		if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
			t.Errorf("GET %s: Content-Type = %q, want text/html", p, ct)
		}
	}
}

func TestRealAssetIsServedNotFallback(t *testing.T) {
	h := testServer(t).staticHandler(testAssets())
	rec := get(t, h, "/assets/index.js")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := rec.Body.String(); got != "console.log(1)" {
		t.Errorf("body = %q, want the real asset, not the SPA fallback", got)
	}
}

// The entry bundle is emitted at a stable unhashed path, so a cached index.html
// would keep pointing at a stale build.
func TestIndexIsNoCache(t *testing.T) {
	h := testServer(t).staticHandler(testAssets())
	if got := get(t, h, "/").Header().Get("Cache-Control"); got != "no-cache" {
		t.Errorf("Cache-Control = %q, want no-cache", got)
	}
}

// Unknown /api/ paths must 404 rather than fall through to the SPA, or a
// mistyped fetch would resolve to HTML and fail as a confusing JSON parse error.
func TestUnknownAPIPathIs404(t *testing.T) {
	h := testServer(t).staticHandler(testAssets())
	for _, p := range []string{"/api/nope", "/api/digest/extra"} {
		if rec := get(t, h, p); rec.Code != http.StatusNotFound {
			t.Errorf("GET %s: status = %d, want 404", p, rec.Code)
		}
	}
	// Same guarantee when the bundle was never built.
	h = testServer(t).staticHandler(nil)
	if rec := get(t, h, "/api/nope"); rec.Code != http.StatusNotFound {
		t.Errorf("unbuilt: status = %d, want 404", rec.Code)
	}
}

func TestMissingAssetsPlaceholder(t *testing.T) {
	h := testServer(t).staticHandler(nil)
	rec := get(t, h, "/")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "make seq-web") {
		t.Error("placeholder should tell the user how to build the frontend")
	}
}

func TestDigestEndpoint(t *testing.T) {
	s := testServer(t)
	rec := httptest.NewRecorder()
	s.handleDigest(rec, httptest.NewRequest(http.MethodGet, "/api/digest", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q", ct)
	}

	var d digest.Digest
	if err := json.Unmarshal(rec.Body.Bytes(), &d); err != nil {
		t.Fatalf("response is not valid digest JSON: %v", err)
	}
	if d.Version != digest.SchemaVersion {
		t.Errorf("version = %d, want %d", d.Version, digest.SchemaVersion)
	}
	if len(d.Lifelines) == 0 || len(d.Intervals) == 0 {
		t.Error("digest round-tripped empty")
	}
	if len(d.Warp.Knots) == 0 {
		t.Error("warp knots did not survive serialization")
	}
}

func TestHealthz(t *testing.T) {
	s := testServer(t)
	rec := httptest.NewRecorder()
	s.handleHealthz(rec, httptest.NewRequest(http.MethodGet, "/api/healthz", nil))
	if rec.Code != http.StatusOK || rec.Body.String() != "ok" {
		t.Errorf("healthz = %d %q", rec.Code, rec.Body.String())
	}
}

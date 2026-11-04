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

package artifacts

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path"
	"regexp"
	"strings"
	"testing"
)

// Phase 2b (ptone/scion#7107): the operator's secondary link origin. The
// tests name the security review condition (C1-C6) and the kill check (K)
// each one pins. The hub wiring (outermost placement, the hub's credential
// headers, settings and audit) is tested in pkg/hub.

const (
	testLinkOrigin  = "https://links.example.net"
	testLinkHost    = "links.example.net"
	testHubOrigin   = "https://hub.example.com"
	testHubHost     = "hub.example.com"
	testAgentHeader = "X-Scion-Agent-Token"
)

// testServesOnOrigin stands in for the hub's route check (isArtifactViewRequest
// or isArtifactSharedRequest, tested in pkg/hub): a GET or HEAD of a clean
// path with a capability segment under the share or view route.
func testServesOnOrigin(r *http.Request) bool {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		return false
	}
	p := r.URL.Path
	if path.Clean(p) != p || strings.Contains(r.URL.RawPath, "%") {
		return false
	}
	for _, route := range []string{RouteShared, RouteView} {
		if rest, ok := strings.CutPrefix(p, route); ok {
			seg, _, _ := strings.Cut(rest, "/")
			return seg != ""
		}
	}
	return false
}

// stripHeaders returns a credential stripper that removes names.
func stripHeaders(names ...string) func(http.Header) {
	return func(h http.Header) {
		for _, n := range names {
			h.Del(n)
		}
	}
}

// linkOriginLimits returns limits with the given link origin and the test
// hub origin.
func linkOriginLimits(lo string) func(context.Context) Limits {
	return func(context.Context) Limits { return Limits{LinkOrigin: lo, PublicOrigin: testHubOrigin} }
}

// guarded serves r through LinkOriginGuard(lo) in front of f.svc, as the
// hub mounts it.
func (f *fixture) guarded(lo string, r *http.Request) *httptest.ResponseRecorder {
	f.t.Helper()
	h := LinkOriginGuard(func() string { return lo }, testServesOnOrigin, stripHeaders(testAgentHeader))(f.svc)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	return rec
}

func requestOn(method, host, target string, hdr map[string]string) *http.Request {
	r := httptest.NewRequest(method, target, nil)
	r.Host = host
	for k, v := range hdr {
		r.Header.Set(k, v)
	}
	return r
}

// openSharedLink follows a share link through the guard on host and
// returns the view response.
func (f *fixture) openSharedLink(lo, host, token string, hdr map[string]string) *httptest.ResponseRecorder {
	f.t.Helper()
	rec := f.guarded(lo, requestOn(http.MethodGet, host, RouteShared+token, hdr))
	if rec.Code != http.StatusSeeOther {
		f.t.Fatalf("shared on %s: %d %s", host, rec.Code, rec.Body.String())
	}
	loc := rec.Header().Get("Location")
	if !strings.HasPrefix(loc, RouteView) {
		f.t.Fatalf("shared on %s: Location %q is not a relative view path", host, loc)
	}
	view := f.guarded(lo, requestOn(http.MethodGet, host, loc, hdr))
	if view.Code != http.StatusOK {
		f.t.Fatalf("view on %s: %d %s", host, view.Code, view.Body.String())
	}
	return view
}

// cspSource returns the script-src source of a view CSP.
func cspSource(t *testing.T, csp string) string {
	t.Helper()
	m := regexp.MustCompile(`script-src (\S+) 'unsafe-inline'`).FindStringSubmatch(csp)
	if m == nil {
		t.Fatalf("CSP %q has no script-src", csp)
	}
	return m[1]
}

// TestSecondaryOriginMatch: a request is a secondary-origin request iff its
// Host names the link origin's host (case-insensitive) and port (exact,
// default implied). C1, K2.
func TestSecondaryOriginMatch(t *testing.T) {
	for _, tc := range []struct {
		lo, host string
		want     bool
	}{
		{testLinkOrigin, testLinkHost, true},
		{testLinkOrigin, "LINKS.Example.NET", true},
		{testLinkOrigin, "links.example.net:443", true},
		{testLinkOrigin, "links.example.net.", true},
		{testLinkOrigin, "links.example.net:8443", false},
		{testLinkOrigin, "links.example.net:80", false},
		{testLinkOrigin, "links.example.net.evil.com", false},
		{testLinkOrigin, "xlinks.example.net", false},
		{testLinkOrigin, "links.example", false},
		{testLinkOrigin, testHubHost, false},
		{testLinkOrigin, "", false},
		{"https://links.example.net:8443", "links.example.net:8443", true},
		{"https://links.example.net:8443", "links.example.net", false},
		{"https://links.example.net:8443", "links.example.net:84", false},
		{"https://links.example.net:8443", "links.example.net:84430", false},
		{"http://localhost:9000", "localhost:9000", true},
		{"http://localhost:9000", "localhost", false},
		{"", testLinkHost, false},
		{"https://links.example.net/x", testLinkHost, false},
		{"ftp://links.example.net", testLinkHost, false},
		{"https://", "", false},
	} {
		r := requestOn(http.MethodGet, tc.host, "/", nil)
		if got := SecondaryOriginRequest(r, tc.lo); got != tc.want {
			t.Errorf("SecondaryOriginRequest(Host %q, %q) = %v, want %v", tc.host, tc.lo, got, tc.want)
		}
	}
}

// TestSecondaryOriginIgnoresForwardedHeaders: detection reads only the Host
// the hub received; X-Forwarded-Host and Forwarded never make or unmake a
// secondary-origin request. C2 [r2] tests A and B, K14.
func TestSecondaryOriginIgnoresForwardedHeaders(t *testing.T) {
	for _, hdr := range []map[string]string{
		{"X-Forwarded-Host": testLinkHost},
		{"Forwarded": "host=" + testLinkHost},
		{"Forwarded": `for=192.0.2.1;host="` + testLinkHost + `";proto=https`},
		{"X-Original-Host": testLinkHost, "X-Host": testLinkHost},
	} {
		if SecondaryOriginRequest(requestOn(http.MethodGet, testHubHost, "/", hdr), testLinkOrigin) {
			t.Errorf("hub Host with %v detected as secondary", hdr)
		}
	}
	for _, hdr := range []map[string]string{
		{"X-Forwarded-Host": testHubHost},
		{"Forwarded": "host=" + testHubHost},
	} {
		if !SecondaryOriginRequest(requestOn(http.MethodGet, testLinkHost, "/", hdr), testLinkOrigin) {
			t.Errorf("secondary Host with %v not detected as secondary", hdr)
		}
	}
}

// TestLinkOriginGuardConfines: on the secondary Host only the share and
// view routes are served; everything else answers 404 before the inner
// handler runs. On the hub Host every request reaches it. C2, K3, K4.
func TestLinkOriginGuardConfines(t *testing.T) {
	refused := []struct{ method, target string }{
		{http.MethodGet, "/api/v1/artifacts/0190a2b4-0000-7000-8000-000000000001"},
		{http.MethodPost, "/api/v1/artifacts/0190a2b4-0000-7000-8000-000000000001/links"},
		{http.MethodGet, "/api/v1/artifacts"},
		{http.MethodGet, "/api/v1/agents"},
		{http.MethodGet, "/api/v1/projects"},
		{http.MethodPut, "/api/v1/admin/server-config"},
		{http.MethodGet, "/"},
		{http.MethodGet, "/index.html"},
		{http.MethodGet, "/assets/app.js"},
		{http.MethodGet, "/auth/login/google"},
		{http.MethodGet, "/events"},
		{http.MethodGet, "/api/v1/portview/abc/"},
		{http.MethodGet, "/.well-known/openid-configuration"},
		{http.MethodGet, "/healthz"},
		{http.MethodGet, "/api/v1/artifacts/shared"},
		{http.MethodGet, "/api/v1/artifacts/shared/"},
		{http.MethodPost, "/api/v1/artifacts/shared/tok"},
		{http.MethodDelete, "/api/v1/artifacts/view/cap/x"},
		{http.MethodOptions, "/api/v1/artifacts/view/cap/x"},
		{http.MethodGet, "/api/v1/artifacts/sharedx/tok"},
	}
	allowed := []struct{ method, target string }{
		{http.MethodGet, "/api/v1/artifacts/shared/tok"},
		{http.MethodHead, "/api/v1/artifacts/shared/tok"},
		{http.MethodGet, "/api/v1/artifacts/shared/tok/files/a/b.png"},
		{http.MethodGet, "/api/v1/artifacts/view/cap/index.html"},
		{http.MethodHead, "/api/v1/artifacts/view/cap/index.html"},
	}
	var reached []string
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = append(reached, r.Method+" "+r.URL.Path)
		w.WriteHeader(http.StatusTeapot)
	})
	guard := LinkOriginGuard(func() string { return testLinkOrigin }, testServesOnOrigin, stripHeaders(testAgentHeader))(inner)
	serve := func(method, host, target string) int {
		r := httptest.NewRequest(method, target, nil)
		r.Host = host
		rec := httptest.NewRecorder()
		guard.ServeHTTP(rec, r)
		return rec.Code
	}
	for _, rq := range refused {
		reached = nil
		if code := serve(rq.method, testLinkHost, rq.target); code != http.StatusNotFound || len(reached) != 0 {
			t.Errorf("secondary %s %s: %d (inner reached %v), want 404 before the handler", rq.method, rq.target, code, reached)
		}
		reached = nil
		if code := serve(rq.method, testHubHost, rq.target); code != http.StatusTeapot || len(reached) != 1 {
			t.Errorf("hub %s %s: %d, want it served as today", rq.method, rq.target, code)
		}
	}
	for _, rq := range allowed {
		for _, host := range []string{testLinkHost, testHubHost} {
			reached = nil
			if code := serve(rq.method, host, rq.target); code != http.StatusTeapot || len(reached) != 1 {
				t.Errorf("%s %s %s: %d, want it served", host, rq.method, rq.target, code)
			}
		}
	}
	// No link origin: nothing is confined.
	open := LinkOriginGuard(func() string { return "" }, testServesOnOrigin, nil)(inner)
	r := httptest.NewRequest(http.MethodGet, "/api/v1/agents", nil)
	r.Host = testLinkHost
	rec := httptest.NewRecorder()
	open.ServeHTTP(rec, r)
	if rec.Code != http.StatusTeapot {
		t.Errorf("empty link origin confined a request: %d", rec.Code)
	}
}

// TestLinkOriginGuardConfinesTheService: through the real service on the
// secondary Host, the artifact read and link creation answer 404 while a
// share link redirects (303, relative) and its view serves (200). C2.
func TestLinkOriginGuardConfinesTheService(t *testing.T) {
	f, id := newLinkFixture(t)
	f.svc.SetLimits(linkOriginLimits(testLinkOrigin))
	token, _ := f.mustMintLink(userU, id, "")

	for _, rq := range []struct{ method, target string }{
		{http.MethodGet, "/api/v1/artifacts/" + id},
		{http.MethodPost, linksPath(id)},
	} {
		r := withPrincipal(requestOn(rq.method, testLinkHost, rq.target, nil), userU)
		if rec := f.guarded(testLinkOrigin, r); rec.Code != http.StatusNotFound {
			t.Errorf("secondary %s %s: %d, want 404", rq.method, rq.target, rec.Code)
		}
		r = withPrincipal(requestOn(rq.method, testHubHost, rq.target, nil), userU)
		if rec := f.guarded(testLinkOrigin, r); rec.Code == http.StatusNotFound {
			t.Errorf("hub %s %s: 404, want it served as today", rq.method, rq.target)
		}
	}
	view := f.openSharedLink(testLinkOrigin, testLinkHost, token, nil)
	if view.Body.String() != "# shared" {
		t.Errorf("view body %q", view.Body.String())
	}
}

// TestLinkOriginGuardDropsCredentials: on the secondary Host the guard
// removes the Cookie, Authorization and agent-token headers (and any the
// hub names) before the inner handler, which sees no credential, and the
// response equals the same request without them. On the hub Host they
// pass. C3, K5 (in pkg/hub), K6, K7.
func TestLinkOriginGuardDropsCredentials(t *testing.T) {
	creds := map[string]string{
		"Cookie":                   "scion_sess=valid-session",
		"Authorization":            "Bearer user-token",
		testAgentHeader:            "agent-token",
		"X-Scion-Federation-Token": "fed",
	}
	var seen http.Header
	var marked bool
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Clone()
		_, marked = secondaryOriginHost(r.Context())
		w.WriteHeader(http.StatusOK)
	})
	guard := LinkOriginGuard(func() string { return testLinkOrigin }, testServesOnOrigin, stripHeaders(testAgentHeader, "X-Scion-Federation-Token"))(inner)
	target := RouteShared + "tok"

	r := requestOn(http.MethodGet, testLinkHost, target, creds)
	r.AddCookie(&http.Cookie{Name: "other", Value: "x"})
	guard.ServeHTTP(httptest.NewRecorder(), r)
	for name := range creds {
		if v := seen.Values(name); len(v) != 0 {
			t.Errorf("secondary Host: inner saw %s = %v", name, v)
		}
	}
	if !marked {
		t.Error("secondary Host: request not marked as secondary-origin")
	}
	if r.Header.Get("Authorization") == "" {
		t.Error("the guard changed the caller's request instead of a clone")
	}

	r = requestOn(http.MethodGet, testHubHost, target, creds)
	guard.ServeHTTP(httptest.NewRecorder(), r)
	for name, want := range creds {
		if got := seen.Get(name); got != want {
			t.Errorf("hub Host: inner saw %s = %q, want %q", name, got, want)
		}
	}
	if marked {
		t.Error("hub Host: request marked as secondary-origin")
	}
}

// TestSecondaryOriginCredentialsChangeNothing: through the real service, a
// share link opened on the secondary Host with a session cookie, a bearer
// token and an agent token answers exactly as without them. C3, K6, K7.
func TestSecondaryOriginCredentialsChangeNothing(t *testing.T) {
	f, id := newLinkFixture(t)
	f.svc.SetLimits(linkOriginLimits(testLinkOrigin))
	token, _ := f.mustMintLink(userU, id, "")
	bare := f.openSharedLink(testLinkOrigin, testLinkHost, token, nil)
	withCreds := f.openSharedLink(testLinkOrigin, testLinkHost, token, map[string]string{
		"Cookie": "scion_sess=valid-session", "Authorization": "Bearer user-token", testAgentHeader: "agent-token",
	})
	if response(bare) != response(withCreds) {
		t.Errorf("credentials changed the response:\n%s\nvs\n%s", response(bare), response(withCreds))
	}
}

// TestViewCSPFromConfiguration: the view CSP source is the configured link
// origin on the secondary Host and the hub's configured public origin on
// any other Host; the request Host never appears. C1, K1, K2.
func TestViewCSPFromConfiguration(t *testing.T) {
	f, id := newLinkFixture(t)
	f.svc.SetLimits(linkOriginLimits(testLinkOrigin))
	token, _ := f.mustMintLink(userU, id, "")

	for _, tc := range []struct{ host, wantHost string }{
		{testLinkHost, testLinkHost},
		{"LINKS.EXAMPLE.NET", testLinkHost},
		{testHubHost, testHubHost},
		{"evil.example.org", testHubHost},
		{"links.example.net:8443", testHubHost},
		{"evil.com; script-src *", testHubHost},
	} {
		view := f.openSharedLink(testLinkOrigin, tc.host, token, nil)
		csp := view.Header().Get("Content-Security-Policy")
		if src := cspSource(t, csp); !strings.HasPrefix(src, tc.wantHost+RouteView) {
			t.Errorf("Host %q: CSP source %q, want it on %s", tc.host, src, tc.wantHost)
		}
		if !strings.EqualFold(tc.host, tc.wantHost) && strings.Contains(strings.ToLower(csp), strings.ToLower(tc.host)) {
			t.Errorf("Host %q: CSP echoes the request host: %q", tc.host, csp)
		}
	}

	// An empty link origin: every Host gets the hub origin.
	f.svc.SetLimits(linkOriginLimits(""))
	for _, host := range []string{testLinkHost, "evil.example.org", testHubHost} {
		view := f.openSharedLink("", host, token, nil)
		if src := cspSource(t, view.Header().Get("Content-Security-Policy")); !strings.HasPrefix(src, testHubHost+RouteView) {
			t.Errorf("empty link origin, Host %q: CSP source %q, want the hub origin", host, src)
		}
	}

	// No configured public origin: no source at all, never the Host.
	f.svc.SetLimits(func(context.Context) Limits { return Limits{} })
	view := f.openSharedLink("", "evil.example.org", token, nil)
	if src := cspSource(t, view.Header().Get("Content-Security-Policy")); src != "'none'" {
		t.Errorf("no public origin: CSP source %q, want 'none'", src)
	}
}

// TestViewCSPIgnoresForwardedHeaders: a hub-Host request carrying
// X-Forwarded-Host or Forwarded naming the secondary origin is served and
// secured as a hub request (CSP on the hub origin); a secondary-Host
// request carrying the hub's host there keeps the link-origin CSP.
// C2 [r2] tests A and B, K14.
func TestViewCSPIgnoresForwardedHeaders(t *testing.T) {
	f, id := newLinkFixture(t)
	f.svc.SetLimits(linkOriginLimits(testLinkOrigin))
	token, _ := f.mustMintLink(userU, id, "")

	for _, hdr := range []map[string]string{
		{"X-Forwarded-Host": testLinkHost},
		{"Forwarded": "host=" + testLinkHost},
	} {
		view := f.openSharedLink(testLinkOrigin, testHubHost, token, hdr)
		if src := cspSource(t, view.Header().Get("Content-Security-Policy")); !strings.HasPrefix(src, testHubHost+RouteView) {
			t.Errorf("test A %v: CSP source %q, want the hub origin", hdr, src)
		}
		// Still served as a hub request: the API answers.
		r := withPrincipal(requestOn(http.MethodGet, testHubHost, "/api/v1/artifacts/"+id, hdr), userU)
		if rec := f.guarded(testLinkOrigin, r); rec.Code != http.StatusOK {
			t.Errorf("test A %v: hub API %d, want 200", hdr, rec.Code)
		}
	}

	hdr := map[string]string{"X-Forwarded-Host": testHubHost, "Forwarded": "host=" + testHubHost}
	view := f.openSharedLink(testLinkOrigin, testLinkHost, token, hdr)
	if src := cspSource(t, view.Header().Get("Content-Security-Policy")); !strings.HasPrefix(src, testLinkHost+RouteView) {
		t.Errorf("test B: CSP source %q, want the link origin", src)
	}
	r := withPrincipal(requestOn(http.MethodGet, testLinkHost, "/api/v1/artifacts/"+id, hdr), userU)
	if rec := f.guarded(testLinkOrigin, r); rec.Code != http.StatusNotFound {
		t.Errorf("test B: secondary API %d, want 404", rec.Code)
	}
}

// TestViewCSPTakesNoRequestInput: viewCSP is never called with the request
// Host (C1 check: `git grep 'viewCSP(r.Host'` finds nothing).
func TestViewCSPTakesNoRequestInput(t *testing.T) {
	src, err := os.ReadFile("view.go")
	if err != nil {
		t.Fatal(err)
	}
	if regexp.MustCompile(`viewCSP\(\s*r\.`).Match(src) {
		t.Error("view.go builds the view CSP from the request")
	}
	if !strings.Contains(string(src), "viewCSP(b.viewSourceHost(ctx), ") {
		t.Error("view.go no longer builds the view CSP from viewSourceHost")
	}
}

// TestShareURL: shareUrl is present only with a valid link origin, equals
// the origin plus the relative link path, does not follow the request Host
// or X-Forwarded-* headers, and url stays hub-relative. C6, K12, K13.
func TestShareURL(t *testing.T) {
	f, id := newLinkFixture(t)
	mint := func(host string, hdr map[string]string) CreateLinkResponse {
		t.Helper()
		h := map[string]string{"Content-Type": "application/json"}
		for k, v := range hdr {
			h[k] = v
		}
		r := withPrincipal(requestOn(http.MethodPost, host, linksPath(id), h), userU)
		rec := httptest.NewRecorder()
		f.svc.ServeHTTP(rec, r)
		if rec.Code != http.StatusCreated {
			t.Fatalf("mint on %s: %d %s", host, rec.Code, rec.Body.String())
		}
		if strings.Contains(rec.Header().Get("Location"), "/shared/") {
			t.Errorf("the token appears in a response header")
		}
		return decodeInto[CreateLinkResponse](t, rec)
	}

	for _, lo := range []string{"", "https://links.example.net/x", "ftp://links.example.net", "https://u@links.example.net"} {
		f.svc.SetLimits(linkOriginLimits(lo))
		resp := mint(testHubHost, nil)
		if resp.ShareURL != "" {
			t.Errorf("link origin %q: shareUrl %q, want none", lo, resp.ShareURL)
		}
		if !strings.HasPrefix(resp.URL, RouteShared) {
			t.Errorf("link origin %q: url %q, want hub-relative", lo, resp.URL)
		}
	}

	for _, lo := range []string{testLinkOrigin, "https://links.example.net:8443"} {
		f.svc.SetLimits(linkOriginLimits(lo))
		for _, tc := range []struct {
			host string
			hdr  map[string]string
		}{
			{testHubHost, nil},
			{"evil.example.org", nil},
			{testHubHost, map[string]string{"X-Forwarded-Host": "evil.example.org", "X-Forwarded-Proto": "http", "Forwarded": "host=evil.example.org"}},
		} {
			resp := mint(tc.host, tc.hdr)
			if !strings.HasPrefix(resp.URL, RouteShared) || strings.Contains(resp.URL, "://") {
				t.Errorf("url %q, want hub-relative", resp.URL)
			}
			if resp.ShareURL != lo+resp.URL {
				t.Errorf("Host %q %v: shareUrl %q, want %q", tc.host, tc.hdr, resp.ShareURL, lo+resp.URL)
			}
		}
	}

	// The token appears only in the create response: the list does not
	// carry it.
	f.svc.SetLimits(linkOriginLimits(testLinkOrigin))
	resp := mint(testHubHost, nil)
	token := strings.TrimPrefix(resp.URL, RouteShared)
	list := f.do(&userU, http.MethodGet, linksPath(id), nil, nil)
	if list.Code != http.StatusOK || strings.Contains(list.Body.String(), token) {
		t.Errorf("list: %d, token present %v", list.Code, strings.Contains(list.Body.String(), token))
	}
}

// TestLinkOriginGuardAlwaysDropsCookieAndAuthorization: the Cookie and
// Authorization headers are removed on the secondary Host even when the
// host passes no stripper of its own. C3, K6.
func TestLinkOriginGuardAlwaysDropsCookieAndAuthorization(t *testing.T) {
	var seen http.Header
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { seen = r.Header.Clone() })
	guard := LinkOriginGuard(func() string { return testLinkOrigin }, testServesOnOrigin, nil)(inner)
	r := requestOn(http.MethodGet, testLinkHost, RouteView+"cap/index.html",
		map[string]string{"Cookie": "scion_sess=valid", "Authorization": "Bearer t", "Accept": "text/html"})
	guard.ServeHTTP(httptest.NewRecorder(), r)
	if seen.Get("Cookie") != "" || seen.Get("Authorization") != "" {
		t.Errorf("inner saw Cookie %q, Authorization %q", seen.Get("Cookie"), seen.Get("Authorization"))
	}
	if seen.Get("Accept") != "text/html" {
		t.Errorf("a non-credential header was dropped: Accept %q", seen.Get("Accept"))
	}
}

// TestLinkOriginGuardNilRouteCheckServesNothing: without a route check the
// guard admits nothing on the secondary Host (fail closed). C2.
func TestLinkOriginGuardNilRouteCheckServesNothing(t *testing.T) {
	reached := false
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { reached = true })
	guard := LinkOriginGuard(func() string { return testLinkOrigin }, nil, nil)(inner)
	rec := httptest.NewRecorder()
	guard.ServeHTTP(rec, requestOn(http.MethodGet, testLinkHost, RouteShared+"tok", nil))
	if rec.Code != http.StatusNotFound || reached {
		t.Errorf("nil route check: %d, inner reached %v; want 404", rec.Code, reached)
	}
}

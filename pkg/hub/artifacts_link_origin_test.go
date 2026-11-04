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
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/GoogleCloudPlatform/scion/pkg/artifacts"
	"github.com/GoogleCloudPlatform/scion/pkg/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Phase 2b (ptone/scion#7107): the hub wiring of the secondary link origin.
// The guard's own behaviour is tested in pkg/artifacts; these tests pin
// that the hub feeds it the setting, puts it outermost (before
// authentication and the web server's session bridge), strips the hub's
// credential headers, and builds shareUrl and the view CSP from
// configuration.

const (
	loTestOrigin    = "https://links.example.net"
	loTestHost      = "links.example.net"
	loTestHubOrigin = "https://hub.example.com"
	loTestHubHost   = "hub.example.com"
)

// linkOriginServer is a hub with artifacts on, its public origin set to
// loTestHubOrigin and the artifacts section set to doc.
func linkOriginServer(t *testing.T, doc string) (*Server, store.Store) {
	t.Helper()
	srv, s := testServer(t)
	enableArtifactsForTest(t, srv)
	srv.config.HubEndpoints = &HubEndpoints{Operator: loTestHubOrigin, Public: loTestHubOrigin}
	srv.SetOperationalSettings(artifactsOps(t, doc))
	return srv, s
}

// hostRequest serves a request on host through the full hub handler.
func hostRequest(srv *Server, method, host, target string, body []byte, hdr map[string]string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, target, bytes.NewReader(body))
	r.Host = host
	for k, v := range hdr {
		r.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, r)
	return rec
}

func bearerFor(t *testing.T, srv *Server, user *store.User) string {
	t.Helper()
	token, _, _, err := srv.userTokenService.GenerateTokenPair(user.ID, user.Email, user.DisplayName, user.Role, ClientTypeWeb)
	require.NoError(t, err)
	return "Bearer " + token
}

// linkOriginFixture publishes an artifact as its owner and creates a share
// link on the hub Host.
func linkOriginFixture(t *testing.T, srv *Server, s store.Store, slug string) (owner *store.User, id string, created artifacts.CreateLinkResponse) {
	t.Helper()
	ctx := context.Background()
	p := artifactProject(t, s, slug)
	createTestUserWithProjectRole(t, s, tid(slug+"-owner"), slug+"-owner@test.com", p.ID, store.ProjectRoleMember)
	owner, err := s.GetUser(ctx, tid(slug+"-owner"))
	require.NoError(t, err)
	rec := userArtifactRequest(t, srv, owner, http.MethodPost, "/api/v1/artifacts?name=report.md&scope="+p.ID, []byte("# report\n"))
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	id = decodeArtifactID(t, rec)
	rec = userArtifactRequest(t, srv, owner, http.MethodPost, "/api/v1/artifacts/"+id+"/links", nil)
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &created))
	return owner, id, created
}

// TestArtifactsLinkOriginOnRoutes: with link_origin set, the create
// response carries shareUrl on it; on that Host the API, the web app root
// and port view answer 404 even for the owner, while the share link
// redirects and its view serves with the link-origin CSP. The hub Host is
// unchanged. Conditions 1, 2 and 6.
func TestArtifactsLinkOriginOnRoutes(t *testing.T) {
	srv, s := linkOriginServer(t, `{"link_origin":"`+loTestOrigin+`"}`)
	owner, id, created := linkOriginFixture(t, srv, s, "lo-routes")
	auth := map[string]string{"Authorization": bearerFor(t, srv, owner)}

	assert.True(t, strings.HasPrefix(created.URL, artifacts.RouteShared), created.URL)
	assert.Equal(t, loTestOrigin+created.URL, created.ShareURL)

	for _, rq := range []struct{ method, target string }{
		{http.MethodGet, "/api/v1/artifacts/" + id},
		{http.MethodPost, "/api/v1/artifacts/" + id + "/links"},
		{http.MethodGet, "/api/v1/projects"},
		{http.MethodGet, "/"},
		{http.MethodGet, "/api/v1/portview/abc/"},
		{http.MethodGet, "/api/v1/artifacts/shared/../projects"},
		{http.MethodGet, "/api/v1/artifacts/view/x/%2e%2e/y"},
		{http.MethodGet, "/api/v1/artifacts/shared%2Ftok"},
		{http.MethodPost, created.URL},
	} {
		rec := hostRequest(srv, rq.method, loTestHost, rq.target, nil, auth)
		assert.Equal(t, http.StatusNotFound, rec.Code, "secondary %s %s", rq.method, rq.target)
	}
	rec := hostRequest(srv, http.MethodGet, loTestHubHost, "/api/v1/artifacts/"+id, nil, auth)
	assert.Equal(t, http.StatusOK, rec.Code, "hub Host artifact read")

	shared := hostRequest(srv, http.MethodGet, loTestHost, created.URL, nil, nil)
	require.Equal(t, http.StatusSeeOther, shared.Code, shared.Body.String())
	loc := shared.Header().Get("Location")
	require.True(t, strings.HasPrefix(loc, artifacts.RouteView), loc)
	view := hostRequest(srv, http.MethodGet, loTestHost, loc, nil, nil)
	require.Equal(t, http.StatusOK, view.Code, view.Body.String())
	assert.Contains(t, view.Header().Get("Content-Security-Policy"), "script-src "+loTestHost+artifacts.RouteView)

	view = hostRequest(srv, http.MethodGet, "unrelated.example.org", loc, nil, nil)
	require.Equal(t, http.StatusOK, view.Code)
	csp := view.Header().Get("Content-Security-Policy")
	assert.Contains(t, csp, "script-src "+loTestHubHost+artifacts.RouteView)
	assert.NotContains(t, csp, "unrelated.example.org")
}

// TestArtifactsLinkOriginForwardedHeaders: detection uses the Host the hub
// receives. A hub-Host request naming the secondary origin in
// X-Forwarded-Host or Forwarded is served as a hub request (test A); a
// secondary-Host request naming the hub there is still confined (test B).
// Condition 2 [r2], kill check K14.
func TestArtifactsLinkOriginForwardedHeaders(t *testing.T) {
	srv, s := linkOriginServer(t, `{"link_origin":"`+loTestOrigin+`"}`)
	owner, id, created := linkOriginFixture(t, srv, s, "lo-fwd")
	bearer := bearerFor(t, srv, owner)

	for _, fwd := range []map[string]string{
		{"X-Forwarded-Host": loTestHost},
		{"Forwarded": "host=" + loTestHost},
	} {
		fwd["Authorization"] = bearer
		rec := hostRequest(srv, http.MethodGet, loTestHubHost, "/api/v1/artifacts/"+id, nil, fwd)
		assert.Equal(t, http.StatusOK, rec.Code, "test A %v", fwd)
		shared := hostRequest(srv, http.MethodGet, loTestHubHost, created.URL, nil, fwd)
		require.Equal(t, http.StatusSeeOther, shared.Code)
		view := hostRequest(srv, http.MethodGet, loTestHubHost, shared.Header().Get("Location"), nil, fwd)
		require.Equal(t, http.StatusOK, view.Code)
		assert.Contains(t, view.Header().Get("Content-Security-Policy"), "script-src "+loTestHubHost+artifacts.RouteView, "test A %v", fwd)
	}

	fwd := map[string]string{"X-Forwarded-Host": loTestHubHost, "Authorization": bearer}
	rec := hostRequest(srv, http.MethodGet, loTestHost, "/api/v1/artifacts/"+id, nil, fwd)
	assert.Equal(t, http.StatusNotFound, rec.Code, "test B")
	shared := hostRequest(srv, http.MethodGet, loTestHost, created.URL, nil, fwd)
	require.Equal(t, http.StatusSeeOther, shared.Code)
	view := hostRequest(srv, http.MethodGet, loTestHost, shared.Header().Get("Location"), nil, fwd)
	require.Equal(t, http.StatusOK, view.Code)
	assert.Contains(t, view.Header().Get("Content-Security-Policy"), "script-src "+loTestHost+artifacts.RouteView, "test B")
}

// TestArtifactsLinkOriginOutermost: the guard runs before authentication,
// so a handler behind the hub's whole middleware chain sees no identity
// and no credential header on the secondary Host, whatever the request
// carried; on the hub Host the same credentials authenticate. Condition 3,
// kill checks K5, K6 and K7.
func TestArtifactsLinkOriginOutermost(t *testing.T) {
	srv, s := linkOriginServer(t, `{"link_origin":"`+loTestOrigin+`"}`)
	ctx := context.Background()
	p := artifactProject(t, s, "lo-outer")
	createTestUserWithProjectRole(t, s, tid("lo-outer-user"), "lo-outer-user@test.com", p.ID, store.ProjectRoleMember)
	user, err := s.GetUser(ctx, tid("lo-outer-user"))
	require.NoError(t, err)
	_, agentTok := artifactAgent(t, srv, s, p.ID, "lo-outer-agent", AgentRoleBaseline)

	var mu sync.Mutex
	var sawIdentity Identity
	var sawHeaders http.Header
	probe := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		sawIdentity = GetIdentityFromContext(r.Context())
		sawHeaders = r.Header.Clone()
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	})
	h := srv.applyMiddleware(probe)
	serve := func(host string, hdr map[string]string) (Identity, http.Header) {
		r := httptest.NewRequest(http.MethodGet, artifacts.RouteView+"cap/index.html", nil)
		r.Host = host
		for k, v := range hdr {
			r.Header.Set(k, v)
		}
		mu.Lock()
		sawIdentity, sawHeaders = nil, nil
		mu.Unlock()
		h.ServeHTTP(httptest.NewRecorder(), r)
		mu.Lock()
		defer mu.Unlock()
		return sawIdentity, sawHeaders
	}

	for name, hdr := range map[string]map[string]string{
		"bearer":      {"Authorization": bearerFor(t, srv, user)},
		"agent token": {"X-Scion-Agent-Token": agentTok},
	} {
		id, _ := serve(loTestHubHost, hdr)
		assert.NotNil(t, id, "%s on the hub Host: no identity", name)

		id, headers := serve(loTestHost, hdr)
		assert.Nil(t, id, "%s on the secondary Host: the handler saw an identity", name)
		for k := range hdr {
			assert.Empty(t, headers.Values(k), "%s on the secondary Host: %s reached the handler", name, k)
		}
	}

	// Every hub credential header is removed on the secondary Host; other
	// headers pass.
	creds := map[string]string{
		"Cookie":                    "scion_sess=x",
		"X-Scion-Federation-Token":  "f",
		"X-Scion-Broker-ID":         "b",
		"X-Scion-Signature":         "s",
		"X-Scion-On-Behalf-Of":      "o",
		"X-Scion-Issuer-Delegation": "d",
		"X-Forwarded-User-Email":    "e@test.com",
		IAPAssertionHeader:          "a",
		DefaultJWTHeader:            "j",
	}
	sent := map[string]string{"Accept": "text/html"}
	for k, v := range creds {
		sent[k] = v
	}
	_, headers := serve(loTestHost, sent)
	for k := range creds {
		assert.Empty(t, headers.Values(k), "credential header %s reached the handler", k)
	}
	assert.Equal(t, "text/html", headers.Get("Accept"))
}

// TestArtifactsLinkOriginSessionCookie: through the web server, a signed-in
// session cookie sent to the secondary Host never reaches the hub as a
// credential, and the web app is not served there; on the hub Host the
// session reaches the hub as before. Condition 3, kill check K6.
func TestArtifactsLinkOriginSessionCookie(t *testing.T) {
	ws := newDevAuthWebServer(t)
	var mu sync.Mutex
	var calls int
	var sawAuth, sawCookie string
	ws.MountHubAPI(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls++
		sawAuth, sawCookie = r.Header.Get("Authorization"), r.Header.Get("Cookie")
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}), func(context.Context) error { return nil })
	ws.SetLinkOriginGuard(artifacts.LinkOriginGuard(func() string { return loTestOrigin }, servesOnLinkOrigin, nil))
	cookies := devSignIn(t, ws)
	require.NotEmpty(t, cookies)
	h := ws.Handler()
	serve := func(host, target string) int {
		r := withCookies(httptest.NewRequest(http.MethodGet, target, nil), cookies...)
		r.Host = host
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		return rec.Code
	}

	assert.Equal(t, http.StatusNotFound, serve(loTestHost, "/"), "web app on the secondary Host")
	assert.Equal(t, http.StatusNotFound, serve(loTestHost, "/auth/me"), "auth route on the secondary Host")

	require.Equal(t, http.StatusOK, serve(loTestHubHost, artifacts.RouteShared+"tok"))
	mu.Lock()
	hubAuth, hubCookie := sawAuth, sawCookie
	mu.Unlock()
	assert.True(t, hubAuth != "" || hubCookie != "", "hub Host: the session did not reach the hub")

	require.Equal(t, http.StatusOK, serve(loTestHost, artifacts.RouteShared+"tok"))
	mu.Lock()
	defer mu.Unlock()
	assert.Empty(t, sawAuth, "secondary Host: the session became a bearer token")
	assert.Empty(t, sawCookie, "secondary Host: the session cookie reached the hub")
}

// TestArtifactsLinkOriginFallbacks: an empty or invalid link_origin, or a
// stored one on the hub's own host name at any port, leaves links on the hub
// origin (no shareUrl) and confines nothing. Condition 5.
func TestArtifactsLinkOriginFallbacks(t *testing.T) {
	for _, doc := range []string{
		`{}`,
		`{"link_origin":""}`,
		`{"link_origin":"https://links.example.net/path"}`,
		`{"link_origin":"` + loTestHubOrigin + `"}`,
		`{"link_origin":"https://hub.example.com:8443"}`,
		`{"link_origin":"https://HUB.example.com"}`,
	} {
		t.Run(doc, func(t *testing.T) {
			srv, s := linkOriginServer(t, doc)
			owner, id, created := linkOriginFixture(t, srv, s, "lo-fb")
			assert.Empty(t, created.ShareURL)
			assert.True(t, strings.HasPrefix(created.URL, artifacts.RouteShared))
			assert.Empty(t, srv.artifactLinkOrigin())
			rec := hostRequest(srv, http.MethodGet, loTestHost, "/api/v1/artifacts/"+id, nil,
				map[string]string{"Authorization": bearerFor(t, srv, owner)})
			assert.Equal(t, http.StatusOK, rec.Code)
		})
	}
	// A same-site value is used (with a warning).
	srv, _ := linkOriginServer(t, `{"link_origin":"https://links.example.com"}`)
	assert.Equal(t, "https://links.example.com", srv.artifactLinkOrigin())
}

// TestArtifactsLinkOriginWebServerFromProvider: a combined server whose
// access settings provider is the hub applies the hub's guard without an
// explicit SetLinkOriginGuard, so the web app is not served on the link
// origin. Review O1.
func TestArtifactsLinkOriginWebServerFromProvider(t *testing.T) {
	srv, _ := linkOriginServer(t, `{"link_origin":"`+loTestOrigin+`"}`)
	ws := newDevAuthWebServer(t)
	ws.SetAccessSettingsProvider(srv)
	ws.MountHubAPI(srv.Handler(), func(context.Context) error { return nil })
	h := ws.Handler()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Host = loTestHost
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	assert.Equal(t, http.StatusNotFound, rec.Code, "GET / on the link origin")
	r = httptest.NewRequest(http.MethodGet, "/", nil)
	r.Host = loTestHubHost
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	assert.NotEqual(t, http.StatusNotFound, rec.Code, "GET / on the hub host")
}

// TestArtifactsLinkOriginWebServerStartFailsClosed: a web server that
// mounts the hub API with no secondary link origin guard (no hub provider,
// no SetLinkOriginGuard) refuses to start, before it binds a listener.
func TestArtifactsLinkOriginWebServerStartFailsClosed(t *testing.T) {
	ws := newTestWebServer(t, WebServerConfig{Host: "127.0.0.1", Port: 0})
	ws.MountHubAPI(http.NotFoundHandler(), func(context.Context) error { return nil })
	// A cancelled context: should Start ever serve here, it shuts down and
	// returns at once, so the assertions below fail instead of hanging.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	t.Cleanup(func() {
		if ws.httpServer != nil {
			_ = ws.httpServer.Close()
		}
	})
	err := ws.Start(ctx)
	require.ErrorIs(t, err, errLinkOriginGuardMissing)
	assert.Nil(t, ws.httpServer, "the server was built despite the missing guard")
}

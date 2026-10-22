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
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/GoogleCloudPlatform/scion/pkg/experiments"
	"github.com/GoogleCloudPlatform/scion/pkg/hub/internal/portview"
	"github.com/GoogleCloudPlatform/scion/pkg/store"
	"github.com/GoogleCloudPlatform/scion/pkg/util/logging"
	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Port page view (hub.port_view, ptone/scion#5569).

// setPortViewExperiments sets hub.conduit and hub.port_view on srv.
func setPortViewExperiments(t *testing.T, srv *Server, conduitOn, portViewOn bool) {
	t.Helper()
	fakeStore := newFakeHubSettingStore()
	fakeStore.seed("experiments", json.RawMessage(fmt.Sprintf(`{"overrides":{%q:%t,%q:%t}}`,
		conduitExperiment, conduitOn, experiments.PortView, portViewOn)))
	ops := NewOperationalSettings(fakeStore, emptyKoanf(), emptyKoanf())
	_, err := ops.Refresh(context.Background())
	require.NoError(t, err)
	srv.SetOperationalSettings(ops)
}

// portViewFixture is a conduit proxy fixture with hub.port_view on.
type portViewFixture struct {
	*conduitProxyFixture
	strangerToken string
}

func newPortViewFixture(t *testing.T, app http.Handler) *portViewFixture {
	t.Helper()
	f := &portViewFixture{conduitProxyFixture: newConduitProxyFixture(t, app)}
	setPortViewExperiments(t, f.srv, true, true)
	require.NotEmpty(t, f.srv.portViewSigningKey(), "the port view key is initialised with the server")
	var err error
	f.strangerToken, _, _, err = f.srv.userTokenService.GenerateTokenPair(
		f.stranger.ID(), "stranger@conduit.test", "Stranger", store.UserRoleMember, ClientTypeWeb)
	require.NoError(t, err)
	return f
}

// capFor mints a capability for userID on port of the launched agent.
func (f *portViewFixture) capFor(userID string, port int, exp time.Time) string {
	return portview.Mint(f.srv.portViewSigningKey(), f.launched.ID, port, userID, exp)
}

// ownerCap is a valid capability for the agent owner on the app port.
func (f *portViewFixture) ownerCap() string {
	return f.capFor(f.launched.OwnerID, f.app.port, time.Now().Add(time.Hour))
}

func (f *portViewFixture) viewURL(capability, sub string) string {
	return f.base + portview.Route + capability + "/" + sub
}

func (f *portViewFixture) portLink(sub string) string {
	return f.base + "/api/v1/agents/" + f.launched.ID + "/ports/" + strconv.Itoa(f.app.port) + "/proxy/" + sub
}

// pvResult is a response read whole.
type pvResult struct {
	status int
	header http.Header
	body   string
}

// send sends one request without following redirects and reads it whole.
func (f *portViewFixture) send(t *testing.T, method, target string, header http.Header, body io.Reader) pvResult {
	t.Helper()
	req, err := http.NewRequest(method, target, body)
	require.NoError(t, err)
	for k, v := range header {
		req.Header[k] = v
	}
	client := &http.Client{
		Timeout:       30 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	resp, err := client.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	h := resp.Header.Clone()
	h.Del("Date")
	return pvResult{status: resp.StatusCode, header: h, body: string(b)}
}

// navHeader is what a browser sends for a page navigation.
func navHeader() http.Header {
	return http.Header{"Sec-Fetch-Mode": {"navigate"}, "Accept": {"text/html,application/xhtml+xml"}}
}

// subHeader is what a proxied page sends for a module script or fetch.
func subHeader() http.Header {
	return http.Header{"Sec-Fetch-Mode": {"cors"}, "Origin": {"null"}, "Accept": {"*/*"}}
}

func withBearer(h http.Header, token string) http.Header {
	h.Set("Authorization", "Bearer "+token)
	return h
}

// resolveLocation resolves a response's Location against the request URL.
func resolveLocation(t *testing.T, requestURL string, res pvResult) *url.URL {
	t.Helper()
	loc := res.header.Get("Location")
	require.NotEmpty(t, loc, "no Location (status %d, body %q)", res.status, res.body)
	base, err := url.Parse(requestURL)
	require.NoError(t, err)
	ref, err := url.Parse(loc)
	require.NoError(t, err)
	assert.False(t, ref.IsAbs(), "the Location is relative: %q", loc)
	assert.False(t, strings.HasPrefix(loc, "/"), "the Location is relative to the request path: %q", loc)
	return base.ResolveReference(ref)
}

// requirePortViewInvalid asserts the one capability-failure answer.
func requirePortViewInvalid(t *testing.T, res pvResult) {
	t.Helper()
	require.Equal(t, http.StatusNotFound, res.status, res.body)
	assert.Equal(t, portViewInvalidBody, res.body)
	want := http.Header{
		"Content-Type":                {"text/plain; charset=utf-8"},
		"Cache-Control":               {"no-store"},
		"Content-Security-Policy":     {untrustedContentSandboxCSP},
		"X-Content-Type-Options":      {"nosniff"},
		"Referrer-Policy":             {"no-referrer"},
		"Access-Control-Allow-Origin": {"*"},
		"Content-Length":              {strconv.Itoa(len(portViewInvalidBody))},
	}
	assert.Equal(t, want, res.header)
}

// TestPortView_PortLinkRedirectsAndServes: with hub.port_view on, a page
// navigation to the port link by a signed-in user is answered 302 to a
// page link (relative Location, query kept, not stored), and the page
// link serves the app with no credential at all.
func TestPortView_PortLinkRedirectsAndServes(t *testing.T) {
	f := newPortViewFixture(t, nil)
	f.startAgent(t)

	link := f.portLink("dir/index.html?a=1&b=two")
	res := f.send(t, http.MethodGet, link, withBearer(navHeader(), f.userToken), nil)
	require.Equal(t, http.StatusFound, res.status, res.body)
	assert.Equal(t, "no-store", res.header.Get("Cache-Control"))
	view := resolveLocation(t, link, res)
	require.True(t, strings.HasPrefix(view.Path, portview.Route), "redirected to %s", view)
	capability, sub, ok := strings.Cut(strings.TrimPrefix(view.Path, portview.Route), "/")
	require.True(t, ok)
	assert.Equal(t, "dir/index.html", sub)
	assert.Equal(t, "a=1&b=two", view.RawQuery)
	g, err := portview.Parse(f.srv.portViewSigningKey(), capability, time.Now(), f.srv.portViewTTL())
	require.NoError(t, err)
	assert.Equal(t, portview.Grant{AgentID: f.launched.ID, Port: f.app.port, UserID: f.launched.OwnerID, Expires: g.Expires}, g)

	// A sub-request of the page, with no credential.
	served := f.send(t, http.MethodGet, view.String(), subHeader(), nil)
	require.Equal(t, http.StatusOK, served.status, served.body)
	assert.Equal(t, "hello /dir/index.html?a=1&b=two", served.body)
}

// TestPortView_ViewTTL: the page link's expiry is the configured
// view_ttl after the time it is created (2h by default).
func TestPortView_ViewTTL(t *testing.T) {
	f := newPortViewFixture(t, nil)
	now := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	f.srv.portViewClock = func() time.Time { return now }
	for _, tc := range []struct {
		ttl, want time.Duration
	}{{0, 2 * time.Hour}, {30 * time.Minute, 30 * time.Minute}, {24 * time.Hour, 24 * time.Hour}} {
		f.srv.config.PortViewTTL = tc.ttl
		link := f.portLink("x")
		res := f.send(t, http.MethodGet, link, withBearer(navHeader(), f.userToken), nil)
		require.Equal(t, http.StatusFound, res.status, res.body)
		view := resolveLocation(t, link, res)
		capability, _, _ := strings.Cut(strings.TrimPrefix(view.Path, portview.Route), "/")
		g, err := portview.Parse(f.srv.portViewSigningKey(), capability, now, tc.want)
		require.NoError(t, err)
		assert.Equal(t, now.Add(tc.want), g.Expires.UTC(), "ttl %s", tc.ttl)
	}
}

// TestPortView_PortLinkUnchanged: the port link keeps today's answer for
// everything but a page navigation by a signed-in user with the
// experiment on.
func TestPortView_PortLinkUnchanged(t *testing.T) {
	f := newPortViewFixture(t, nil)
	f.startAgent(t)
	link := f.portLink("page.html")
	served := func(t *testing.T, res pvResult) {
		t.Helper()
		require.Equal(t, http.StatusOK, res.status, res.body)
		assert.Equal(t, "hello /page.html?", res.body)
		assert.Empty(t, res.header.Values("Location"))
	}
	t.Run("not a navigation", func(t *testing.T) {
		served(t, f.send(t, http.MethodGet, link, withBearer(http.Header{"Accept": {"*/*"}}, f.userToken), nil))
		served(t, f.send(t, http.MethodGet, link, withBearer(subHeader(), f.userToken), nil))
	})
	t.Run("not GET or HEAD", func(t *testing.T) {
		res := f.send(t, http.MethodPost, link, withBearer(navHeader(), f.userToken), strings.NewReader("x"))
		require.Equal(t, http.StatusOK, res.status, res.body)
		assert.Empty(t, res.header.Values("Location"))
	})
	t.Run("agent caller", func(t *testing.T) {
		h := navHeader()
		h.Set("X-Scion-Agent-Token", f.agentToken(t, f.launched))
		served(t, f.send(t, http.MethodGet, link, h, nil))
	})
	t.Run("access token caller", func(t *testing.T) {
		// A scoped token of the owner: not a signed-in browser user.
		tok := createPortViewUAT(t, f)
		served(t, f.send(t, http.MethodGet, link, withBearer(navHeader(), tok), nil))
	})
	t.Run("experiment off", func(t *testing.T) {
		setPortViewExperiments(t, f.srv, true, false)
		t.Cleanup(func() { setPortViewExperiments(t, f.srv, true, true) })
		served(t, f.send(t, http.MethodGet, link, withBearer(navHeader(), f.userToken), nil))
	})
	t.Run("caller without access", func(t *testing.T) {
		res := f.send(t, http.MethodGet, link, withBearer(navHeader(), f.strangerToken), nil)
		assert.Equal(t, http.StatusForbidden, res.status, res.body)
		assert.Empty(t, res.header.Values("Location"))
	})
	// The port link answers itself (no redirect) wherever the page view
	// route would not serve the link.
	notRedirected := func(t *testing.T, res pvResult) {
		t.Helper()
		assert.NotEqual(t, http.StatusFound, res.status, res.body)
		assert.Empty(t, res.header.Values("Location"))
	}
	t.Run("page view would not serve the caller", func(t *testing.T) {
		// The agent's owner has no role in its project: the port link
		// admits them as the owner, the page view identity is limited to
		// the project and is refused.
		ctx := context.Background()
		loneID := tid("port-view-lone-owner")
		require.NoError(t, f.store.CreateUser(ctx, &store.User{
			ID: loneID, Email: "lone@conduit.test", DisplayName: "Lone", Role: store.UserRoleMember, Status: store.UserStatusActive,
		}))
		ensureHubMembership(ctx, f.store, loneID)
		lone := &store.Agent{
			ID: tid("port-view-lone-agent"), Slug: "port-view-lone-agent", Name: "Lone",
			ProjectID: f.launched.ProjectID, OwnerID: loneID, Ancestry: []string{loneID},
			RuntimeBrokerID: "broker-1", Phase: "running",
		}
		require.NoError(t, f.store.CreateAgent(ctx, lone))
		require.NoError(t, f.store.UpdateAgentExposedPorts(ctx, lone.ID, []store.ExposedPort{{Port: f.app.port, Host: conduitProxyHost, Mode: "rw"}}))
		got, err := f.store.GetAgent(ctx, lone.ID)
		require.NoError(t, err)
		_, ok := f.srv.portViewIdentity(ctx, loneID, got)
		require.False(t, ok, "the page view identity is refused")
		tok, _, _, err := f.srv.userTokenService.GenerateTokenPair(loneID, "lone@conduit.test", "Lone", store.UserRoleMember, ClientTypeWeb)
		require.NoError(t, err)
		loneLink := f.base + "/api/v1/agents/" + lone.ID + "/ports/" + strconv.Itoa(f.app.port) + "/proxy/page.html"
		check := f.send(t, http.MethodGet, loneLink, withBearer(http.Header{"Accept": {"*/*"}}, tok), nil)
		require.NotEqual(t, http.StatusForbidden, check.status, "the port link admits the owner: %s", check.body)
		notRedirected(t, f.send(t, http.MethodGet, loneLink, withBearer(navHeader(), tok), nil))
	})
	t.Run("port not on loopback", func(t *testing.T) {
		ctx := context.Background()
		other := f.app.port + 1
		if other > 65535 {
			other = f.app.port - 1
		}
		require.NoError(t, f.store.UpdateAgentExposedPorts(ctx, f.launched.ID, []store.ExposedPort{
			{Port: f.app.port, Host: conduitProxyHost, Mode: "rw"},
			{Port: other, Host: "192.0.2.10", Mode: "rw"},
		}))
		t.Cleanup(func() {
			_ = f.store.UpdateAgentExposedPorts(context.Background(), f.launched.ID, []store.ExposedPort{{Port: f.app.port, Host: conduitProxyHost, Mode: "rw"}})
		})
		target := f.base + "/api/v1/agents/" + f.launched.ID + "/ports/" + strconv.Itoa(other) + "/proxy/page.html"
		notRedirected(t, f.send(t, http.MethodGet, target, withBearer(navHeader(), f.userToken), nil))
	})
	t.Run("conduit off", func(t *testing.T) {
		setPortViewExperiments(t, f.srv, false, true)
		t.Cleanup(func() { setPortViewExperiments(t, f.srv, true, true) })
		require.False(t, f.srv.conduitRoutable())
		notRedirected(t, f.send(t, http.MethodGet, link, withBearer(navHeader(), f.userToken), nil))
	})
}

// createPortViewUAT creates a user access token of the agent owner that
// holds agent:port_access on the agent's project and returns its value.
func createPortViewUAT(t *testing.T, f *portViewFixture) string {
	t.Helper()
	require.NotNil(t, f.srv.uatService)
	tok, _, err := f.srv.uatService.CreateToken(context.Background(), f.launched.OwnerID, "port view test",
		f.launched.ProjectID, []string{store.UATScopeAgentPortAccess}, nil)
	require.NoError(t, err)
	return tok
}

// TestPortView_ExperimentOff: with hub.port_view off the route answers the
// route-not-found 404 whatever the capability.
func TestPortView_ExperimentOff(t *testing.T) {
	f := newPortViewFixture(t, nil)
	f.startAgent(t)
	setPortViewExperiments(t, f.srv, true, false)
	res := f.send(t, http.MethodGet, f.viewURL(f.ownerCap(), "index.html"), subHeader(), nil)
	assert.Equal(t, http.StatusNotFound, res.status)
	assert.NotEqual(t, portViewInvalidBody, res.body)
	assert.Contains(t, res.header.Get("Content-Type"), "application/json")
	select {
	case r := <-f.app.got:
		t.Fatalf("the app received %s %s", r.Method, r.URL)
	default:
	}
}

// portViewSignWith builds a page link capability in the documented
// layout, signed with key.
func portViewSignWith(key []byte, agentID string, port int, userID string, exp time.Time) string {
	e := strconv.FormatInt(exp.Unix(), 10)
	m := hmac.New(sha256.New, key)
	m.Write([]byte("port-view\n" + agentID + "\n" + strconv.Itoa(port) + "\n" + userID + "\n" + e))
	return "v1." + agentID + "." + strconv.Itoa(port) + "." + userID + "." + e + "." +
		base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

// TestPortView_UniformFailure: every way a capability can fail gets
// byte-identical status, headers and body, for GET, POST and a page
// navigation with a malformed one, and nothing reaches the app.
func TestPortView_UniformFailure(t *testing.T) {
	f := newPortViewFixture(t, nil)
	f.startAgent(t)
	ctx := context.Background()
	now := time.Now()
	owner, port := f.launched.OwnerID, f.app.port
	key := f.srv.portViewSigningKey()

	// A soft-deleted agent of the same owner, exposing the same port.
	deleted := &store.Agent{
		ID: tid("port-view-deleted"), Slug: "port-view-deleted", Name: "Deleted",
		ProjectID: f.launched.ProjectID, OwnerID: owner, Ancestry: f.launched.Ancestry,
		RuntimeBrokerID: "broker-1", Phase: "stopped",
	}
	require.NoError(t, f.store.CreateAgent(ctx, deleted))
	require.NoError(t, f.store.UpdateAgentExposedPorts(ctx, deleted.ID, []store.ExposedPort{{Port: port, Host: conduitProxyHost, Mode: "rw"}}))
	got, err := f.store.GetAgent(ctx, deleted.ID)
	require.NoError(t, err)
	got.DeletedAt = now
	require.NoError(t, f.store.UpdateAgent(ctx, got))
	got, err = f.store.GetAgent(ctx, deleted.ID)
	require.NoError(t, err)
	require.False(t, got.DeletedAt.IsZero(), "the agent is soft-deleted")

	require.NotEqual(t, key, f.srv.artifactViewKey, "the page view key is not the artifact view key")
	otherKey := bytes.Repeat([]byte{7}, 32)
	cases := []struct{ name, capability string }{
		{"malformed", "not-a-link"},
		{"wrong version", strings.Replace(f.ownerCap(), "v1.", "v2.", 1)},
		{"bad signature", portview.Mint(otherKey, f.launched.ID, port, owner, now.Add(time.Hour))},
		{"artifact view key", portViewSignWith(f.srv.artifactViewKey, f.launched.ID, port, owner, now.Add(time.Hour))},
		{"tampered port", strings.Replace(f.ownerCap(), "."+strconv.Itoa(port)+".", "."+strconv.Itoa(port+1)+".", 1)},
		{"expired", f.capFor(owner, port, now.Add(-time.Second))},
		{"too far in the future", f.capFor(owner, port, now.Add(f.srv.portViewTTL()+portview.MaxSkew+time.Minute))},
		{"user missing", f.capFor(tid("port-view-nobody"), port, now.Add(time.Hour))},
		{"agent missing", portview.Mint(key, tid("port-view-no-agent"), port, owner, now.Add(time.Hour))},
		{"agent deleted", portview.Mint(key, deleted.ID, port, owner, now.Add(time.Hour))},
		{"access refused", f.capFor(f.stranger.ID(), port, now.Add(time.Hour))},
	}
	var first *pvResult
	check := func(t *testing.T, res pvResult) {
		t.Helper()
		requirePortViewInvalid(t, res)
		if first == nil {
			first = &res
			return
		}
		assert.Equal(t, first.status, res.status)
		assert.Equal(t, first.body, res.body)
		assert.True(t, reflect.DeepEqual(first.header, res.header), "headers differ:\n%v\n%v", first.header, res.header)
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			check(t, f.send(t, http.MethodGet, f.viewURL(tc.capability, "app.js"), subHeader(), nil))
			check(t, f.send(t, http.MethodPost, f.viewURL(tc.capability, "api/x"), subHeader(), strings.NewReader("{}")))
			// A credential of another kind does not change the answer.
			check(t, f.send(t, http.MethodGet, f.viewURL(tc.capability, "app.js"), withBearer(subHeader(), f.userToken), nil))
		})
	}
	t.Run("user suspended", func(t *testing.T) {
		valid := f.ownerCap()
		ok := f.send(t, http.MethodGet, f.viewURL(valid, "app.js"), subHeader(), nil)
		require.Equal(t, http.StatusOK, ok.status, ok.body)
		u, err := f.store.GetUser(ctx, owner)
		require.NoError(t, err)
		u.Status = store.UserStatusSuspended
		require.NoError(t, f.store.UpdateUser(ctx, u))
		check(t, f.send(t, http.MethodGet, f.viewURL(valid, "app.js"), subHeader(), nil))
	})
	t.Run("malformed navigation", func(t *testing.T) {
		check(t, f.send(t, http.MethodGet, f.viewURL("not-a-link", "index.html"), navHeader(), nil))
	})
	// The suspended-user case above is the only one that reached the app.
	n := 0
	for {
		select {
		case <-f.app.got:
			n++
			continue
		default:
		}
		break
	}
	assert.Equal(t, 1, n, "only the one valid request reached the app")
}

// TestPortView_LinkWithoutTrailingSlash: GET of a valid link without the
// trailing slash answers 308 to "<link>/" with the query kept; a failed
// link without it, and any other method, get the single 404.
func TestPortView_LinkWithoutTrailingSlash(t *testing.T) {
	f := newPortViewFixture(t, nil)
	f.startAgent(t)
	valid := f.ownerCap()
	bare := f.base + portview.Route + valid + "?a=1"
	res := f.send(t, http.MethodGet, bare, subHeader(), nil)
	require.Equal(t, http.StatusPermanentRedirect, res.status, res.body)
	assert.Equal(t, "no-store", res.header.Get("Cache-Control"))
	got := resolveLocation(t, bare, res)
	assert.Equal(t, f.viewURL(valid, "?a=1"), got.String())
	served := f.send(t, http.MethodGet, got.String(), subHeader(), nil)
	require.Equal(t, http.StatusOK, served.status, served.body)
	assert.Equal(t, "hello /?a=1", served.body)

	expired := f.capFor(f.launched.OwnerID, f.app.port, time.Now().Add(-time.Second))
	requirePortViewInvalid(t, f.send(t, http.MethodGet, f.base+portview.Route+expired, subHeader(), nil))
	// Other methods get the single 404.
	requirePortViewInvalid(t, f.send(t, http.MethodPost, f.base+portview.Route+valid, subHeader(), strings.NewReader("x")))

	// HEAD is redirected like GET.
	head := f.send(t, http.MethodHead, bare, subHeader(), nil)
	require.Equal(t, http.StatusPermanentRedirect, head.status)
	assert.Equal(t, f.viewURL(valid, "?a=1"), resolveLocation(t, bare, head).String())

	// A WebSocket upgrade is not redirected: the single 404.
	ws := subHeader()
	ws.Set("Connection", "Upgrade")
	ws.Set("Upgrade", "websocket")
	ws.Set("Sec-WebSocket-Version", "13")
	ws.Set("Sec-WebSocket-Key", "dGhlIHNhbXBsZSBub25jZQ==")
	requirePortViewInvalid(t, f.send(t, http.MethodGet, f.base+portview.Route+valid, ws, nil))

	// A page navigation with a failed link goes to the port link's root.
	nav := f.send(t, http.MethodGet, f.base+portview.Route+expired+"?a=1", navHeader(), nil)
	require.Equal(t, http.StatusFound, nav.status, nav.body)
	assert.Equal(t, f.portLink("?a=1"), resolveLocation(t, f.base+portview.Route+expired+"?a=1", nav).String())
}

// TestPortView_PortNotExposed: a valid capability for a port the agent
// does not expose gets the port link's port-not-available answer.
func TestPortView_PortNotExposed(t *testing.T) {
	f := newPortViewFixture(t, nil)
	other := f.app.port + 1
	if other > 65535 {
		other = f.app.port - 1
	}
	capability := f.capFor(f.launched.OwnerID, other, time.Now().Add(time.Hour))
	res := f.send(t, http.MethodGet, f.viewURL(capability, "x"), subHeader(), nil)
	assert.Equal(t, http.StatusNotFound, res.status)
	assert.Contains(t, res.header.Get("Content-Type"), "application/json")
	assert.NotEqual(t, portViewInvalidBody, res.body)
	nav := f.send(t, http.MethodGet, f.viewURL(capability, "x"), navHeader(), nil)
	assert.Equal(t, http.StatusNotFound, nav.status)
	assert.Contains(t, nav.body, "Port Not Available")
}

// TestPortView_AgentWithoutConduitSession: the page link is served over
// conduit only; an agent with no conduit session gets the agent-offline
// answer.
func TestPortView_AgentWithoutConduitSession(t *testing.T) {
	f := newPortViewFixture(t, nil)
	res := f.send(t, http.MethodGet, f.viewURL(f.ownerCap(), "x"), subHeader(), nil)
	assert.Equal(t, http.StatusServiceUnavailable, res.status, res.body)
	assert.Contains(t, res.body, ErrCodeAgentOffline)
	assert.Equal(t, "*", res.header.Get("Access-Control-Allow-Origin"))
}

// TestPortView_NavigationMatrix: which link a page navigation on the page
// view route is sent to. Non-navigations are served or refused on the
// capability alone, whatever identity they carry.
func TestPortView_NavigationMatrix(t *testing.T) {
	f := newPortViewFixture(t, nil)
	f.startAgent(t)
	valid := f.ownerCap()
	expired := f.capFor(f.launched.OwnerID, f.app.port, time.Now().Add(-time.Minute))
	tampered := strings.Replace(valid, "."+f.launched.OwnerID+".", "."+f.stranger.ID()+".", 1)
	toPortLink := func(t *testing.T, target string, res pvResult) {
		t.Helper()
		require.Equal(t, http.StatusFound, res.status, res.body)
		assert.Equal(t, "no-store", res.header.Get("Cache-Control"))
		got := resolveLocation(t, target, res)
		want, err := url.Parse(f.portLink("dir/page.html?q=1"))
		require.NoError(t, err)
		assert.Equal(t, want.String(), got.String())
	}
	served := func(t *testing.T, res pvResult) {
		t.Helper()
		require.Equal(t, http.StatusOK, res.status, res.body)
		assert.Equal(t, "hello /dir/page.html?q=1", res.body)
	}
	at := func(c string) string { return f.viewURL(c, "dir/page.html?q=1") }

	t.Run("same user signed in", func(t *testing.T) {
		served(t, f.send(t, http.MethodGet, at(valid), withBearer(navHeader(), f.userToken), nil))
	})
	t.Run("no session", func(t *testing.T) {
		served(t, f.send(t, http.MethodGet, at(valid), navHeader(), nil))
	})
	t.Run("other user signed in", func(t *testing.T) {
		toPortLink(t, at(valid), f.send(t, http.MethodGet, at(valid), withBearer(navHeader(), f.strangerToken), nil))
	})
	t.Run("other user signed in, not a navigation", func(t *testing.T) {
		served(t, f.send(t, http.MethodGet, at(valid), withBearer(subHeader(), f.strangerToken), nil))
	})
	t.Run("navigation by Accept only", func(t *testing.T) {
		h := withBearer(http.Header{"Accept": {"text/html"}}, f.strangerToken)
		toPortLink(t, at(valid), f.send(t, http.MethodGet, at(valid), h, nil))
	})
	t.Run("expired navigation", func(t *testing.T) {
		toPortLink(t, at(expired), f.send(t, http.MethodGet, at(expired), navHeader(), nil))
		toPortLink(t, at(expired), f.send(t, http.MethodGet, at(expired), withBearer(navHeader(), f.userToken), nil))
	})
	t.Run("tampered navigation", func(t *testing.T) {
		toPortLink(t, at(tampered), f.send(t, http.MethodGet, at(tampered), navHeader(), nil))
	})
	t.Run("expired, not a navigation", func(t *testing.T) {
		requirePortViewInvalid(t, f.send(t, http.MethodGet, at(expired), subHeader(), nil))
		requirePortViewInvalid(t, f.send(t, http.MethodGet, at(expired), withBearer(subHeader(), f.userToken), nil))
	})
	t.Run("valid link the user can no longer use, navigation", func(t *testing.T) {
		foreign := f.capFor(f.stranger.ID(), f.app.port, time.Now().Add(time.Hour))
		toPortLink(t, at(foreign), f.send(t, http.MethodGet, at(foreign), navHeader(), nil))
		gone := portview.Mint(f.srv.portViewSigningKey(), tid("port-view-gone-agent"), f.app.port, f.launched.OwnerID, time.Now().Add(time.Hour))
		res := f.send(t, http.MethodGet, f.viewURL(gone, "dir/page.html?q=1"), navHeader(), nil)
		require.Equal(t, http.StatusFound, res.status, res.body)
		got := resolveLocation(t, f.viewURL(gone, "dir/page.html?q=1"), res)
		assert.Equal(t, "/api/v1/agents/"+tid("port-view-gone-agent")+"/ports/"+strconv.Itoa(f.app.port)+"/proxy/dir/page.html", got.Path)
	})
	t.Run("valid link the user can no longer use, not a navigation", func(t *testing.T) {
		foreign := f.capFor(f.stranger.ID(), f.app.port, time.Now().Add(time.Hour))
		requirePortViewInvalid(t, f.send(t, http.MethodGet, at(foreign), subHeader(), nil))
	})
	t.Run("expired form post navigation", func(t *testing.T) {
		requirePortViewInvalid(t, f.send(t, http.MethodPost, at(expired), navHeader(), strings.NewReader("a=1")))
	})
}

// TestPortView_FrontProxyAssertion: the route authorizes by the
// capability only, whether or not an authenticating front proxy
// asserted an identity.
func TestPortView_FrontProxyAssertion(t *testing.T) {
	f := newPortViewFixture(t, nil)
	f.startAgent(t)
	prevAuth, prevProv, prevBase := f.srv.authConfig.ProxyAuthenticator, f.srv.authConfig.ProxyUserProvisioner, f.base
	t.Cleanup(func() {
		f.srv.authConfig.ProxyAuthenticator, f.srv.authConfig.ProxyUserProvisioner = prevAuth, prevProv
		f.base = prevBase
	})
	asserted := map[string]UserIdentity{"owner@conduit.test": f.owner, "stranger@conduit.test": f.stranger}
	var provisioned []string
	// front serves the hub with a front-proxy authenticator answering with
	// user (nil: no assertion); the auth middleware reads its config when
	// the handler is built.
	front := func(t *testing.T, user *ProxyUserInfo) {
		t.Helper()
		f.srv.authConfig.ProxyAuthenticator = portProxyStubAuthenticator{user: user}
		f.srv.authConfig.ProxyUserProvisioner = func(_ context.Context, info *ProxyUserInfo) (UserIdentity, error) {
			provisioned = append(provisioned, info.Email)
			return asserted[info.Email], nil
		}
		srv := httptest.NewServer(f.srv.Handler())
		t.Cleanup(srv.Close)
		f.base = srv.URL
	}

	t.Run("valid assertion, bad capability", func(t *testing.T) {
		front(t, &ProxyUserInfo{Subject: "front-proxy-owner", Email: "owner@conduit.test"})
		bad := f.capFor(f.launched.OwnerID, f.app.port, time.Now().Add(-time.Second))
		requirePortViewInvalid(t, f.send(t, http.MethodGet, f.viewURL(bad, "app.js"), subHeader(), nil))
		assert.Equal(t, []string{"owner@conduit.test"}, provisioned, "the assertion was verified")
	})
	t.Run("valid assertion of another user, good capability", func(t *testing.T) {
		provisioned = nil
		front(t, &ProxyUserInfo{Subject: "front-proxy-stranger", Email: "stranger@conduit.test"})
		res := f.send(t, http.MethodGet, f.viewURL(f.ownerCap(), "app.js"), subHeader(), nil)
		require.Equal(t, http.StatusOK, res.status, res.body)
		assert.Equal(t, "hello /app.js?", res.body)
		assert.Equal(t, []string{"stranger@conduit.test"}, provisioned, "the assertion was verified")
	})
	t.Run("no assertion, good capability", func(t *testing.T) {
		provisioned = nil
		front(t, nil)
		res := f.send(t, http.MethodGet, f.viewURL(f.ownerCap(), "app.js"), subHeader(), nil)
		require.Equal(t, http.StatusOK, res.status, res.body)
		assert.Equal(t, "hello /app.js?", res.body)
		assert.Empty(t, provisioned)
	})
}

// TestPortView_Preflight: the route answers a preflight itself, on the
// capability alone, and never forwards it to the app.
func TestPortView_Preflight(t *testing.T) {
	f := newPortViewFixture(t, nil)
	f.startAgent(t)
	pre := func(capability, method, headers string) pvResult {
		h := http.Header{"Origin": {"null"}, "Access-Control-Request-Method": {method}}
		if headers != "" {
			h.Set("Access-Control-Request-Headers", headers)
		}
		return f.send(t, http.MethodOptions, f.viewURL(capability, "api/items"), h, nil)
	}
	res := pre(f.ownerCap(), "PUT", "Content-Type, Authorization, Cookie, X-App-Key")
	require.Equal(t, http.StatusNoContent, res.status, res.body)
	assert.Equal(t, []string{"*"}, res.header.Values("Access-Control-Allow-Origin"))
	assert.Equal(t, "PUT", res.header.Get("Access-Control-Allow-Methods"))
	assert.Equal(t, "content-type, x-app-key", res.header.Get("Access-Control-Allow-Headers"))
	assert.Equal(t, "600", res.header.Get("Access-Control-Max-Age"))
	assert.Empty(t, res.header.Values("Access-Control-Allow-Credentials"))

	res = pre(f.ownerCap(), "TRACE", "authorization")
	require.Equal(t, http.StatusNoContent, res.status)
	assert.Empty(t, res.header.Values("Access-Control-Allow-Methods"))
	assert.Empty(t, res.header.Values("Access-Control-Allow-Headers"))

	requirePortViewInvalid(t, pre("not-a-link", "GET", ""))
	requirePortViewInvalid(t, pre(f.capFor(f.launched.OwnerID, f.app.port, time.Now().Add(-time.Second)), "GET", ""))

	select {
	case r := <-f.app.got:
		t.Fatalf("a preflight reached the app: %s %s", r.Method, r.URL)
	default:
	}
}

// TestPortView_ResponsePolicy: the app's Set-Cookie,
// Content-Security-Policy, X-Content-Type-Options, Clear-Site-Data,
// Access-Control-* and Referrer-Policy headers are replaced by the hub's,
// each sent once; Cache-Control defaults to private.
func TestPortView_ResponsePolicy(t *testing.T) {
	app := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Set-Cookie", "app=1; Path=/")
		h.Set("Content-Security-Policy", "default-src *")
		h.Set("X-Content-Type-Options", "off")
		h.Set("Clear-Site-Data", `"cookies"`)
		h.Set("Access-Control-Allow-Origin", "https://app.example")
		h.Set("Access-Control-Allow-Credentials", "true")
		h.Set("Access-Control-Expose-Headers", "X-App")
		h.Set("Referrer-Policy", "unsafe-url")
		h.Set("X-App", "ok")
		if r.URL.Path == "/cached" {
			h.Set("Cache-Control", "max-age=60")
		}
		_, _ = io.WriteString(w, "body")
	})
	f := newPortViewFixture(t, app)
	f.startAgent(t)
	res := f.send(t, http.MethodGet, f.viewURL(f.ownerCap(), "page"), subHeader(), nil)
	require.Equal(t, http.StatusOK, res.status, res.body)
	assert.Equal(t, "body", res.body)
	assert.Equal(t, []string{untrustedContentSandboxCSP}, res.header.Values("Content-Security-Policy"))
	assert.Equal(t, []string{"nosniff"}, res.header.Values("X-Content-Type-Options"))
	assert.Equal(t, []string{"no-referrer"}, res.header.Values("Referrer-Policy"))
	assert.Equal(t, []string{"*"}, res.header.Values("Access-Control-Allow-Origin"))
	assert.Empty(t, res.header.Values("Access-Control-Allow-Credentials"))
	assert.Empty(t, res.header.Values("Access-Control-Expose-Headers"))
	assert.Empty(t, res.header.Values("Set-Cookie"))
	assert.Empty(t, res.header.Values("Clear-Site-Data"))
	assert.Equal(t, []string{"private"}, res.header.Values("Cache-Control"))
	assert.Equal(t, "ok", res.header.Get("X-App"))

	cached := f.send(t, http.MethodGet, f.viewURL(f.ownerCap(), "cached"), subHeader(), nil)
	require.Equal(t, http.StatusOK, cached.status)
	assert.Equal(t, []string{"max-age=60"}, cached.header.Values("Cache-Control"))
}

// TestPortView_RequestStripping: credential and identity headers never
// reach the app, one header per subtest; Origin "null" is forwarded.
func TestPortView_RequestStripping(t *testing.T) {
	f := newPortViewFixture(t, nil)
	f.startAgent(t)
	agentTok := f.agentToken(t, f.launched)
	cases := map[string]string{
		"Cookie":                          "scion_sess=abc; other=1",
		"Authorization":                   "Bearer " + f.userToken,
		"X-Scion-Agent-Token":             agentTok,
		"X-Scion-Broker-HMAC":             "abc",
		"X-Forwarded-User-Id":             "u1",
		"X-Forwarded-User-Email":          "u1@example.com",
		"X-Forwarded-User-Name":           "U",
		"X-Forwarded-User-Role":           "admin",
		IAPAssertionHeader:                "assertion",
		"X-Goog-Authenticated-User-Email": "accounts.google.com:u1@example.com",
		"X-Goog-Authenticated-User-Id":    "accounts.google.com:1",
	}
	for name, value := range cases {
		t.Run(name, func(t *testing.T) {
			for len(f.app.got) > 0 {
				<-f.app.got
			}
			h := subHeader()
			h.Set(name, value)
			res := f.send(t, http.MethodGet, f.viewURL(f.ownerCap(), "probe"), h, nil)
			require.Equal(t, http.StatusOK, res.status, res.body)
			select {
			case r := <-f.app.got:
				assert.Empty(t, r.Header.Values(name), "%s reached the app", name)
				assert.Equal(t, "null", r.Header.Get("Origin"))
			case <-time.After(10 * time.Second):
				t.Fatal("the request did not reach the app")
			}
		})
	}
	// Headers the hub refuses before the route never reach the app either.
	for name, value := range map[string]string{"X-Scion-Broker-ID": "broker-1", FederationTokenHeader: "x"} {
		t.Run(name, func(t *testing.T) {
			for len(f.app.got) > 0 {
				<-f.app.got
			}
			h := subHeader()
			h.Set(name, value)
			res := f.send(t, http.MethodGet, f.viewURL(f.ownerCap(), "probe"), h, nil)
			assert.NotEqual(t, http.StatusOK, res.status)
			assert.Zero(t, len(f.app.got), "the request reached the app")
		})
	}
}

// TestPortView_WebSocketRunsAsRestrictedIdentity: a WebSocket opened on a
// page link with no credential echoes; its conduit stream is tracked as
// the user limited to port access in the agent's project, and it closes
// when that user is suspended.
func TestPortView_WebSocketRunsAsRestrictedIdentity(t *testing.T) {
	f := newPortViewFixture(t, echoApp())
	f.startAgent(t)
	wsURL := "ws" + strings.TrimPrefix(f.viewURL(f.ownerCap(), "ws"), "http")
	c, _, err := websocket.DefaultDialer.Dial(wsURL, http.Header{"Origin": {"null"}})
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Close() })
	require.NoError(t, c.SetReadDeadline(time.Now().Add(10*time.Second)))
	require.NoError(t, c.WriteMessage(websocket.TextMessage, []byte("ping")))
	_, msg, err := c.ReadMessage()
	require.NoError(t, err)
	require.Equal(t, "ping", string(msg))

	a := f.srv.conduitAuthz.Load()
	require.NotNil(t, a)
	a.mu.Lock()
	var idents []Identity
	for st := range a.streams {
		idents = append(idents, st.Identity)
	}
	a.mu.Unlock()
	require.Len(t, idents, 1)
	scoped, ok := idents[0].(*ScopedUserIdentity)
	require.True(t, ok, "stream identity is %T", idents[0])
	assert.Equal(t, f.launched.OwnerID, scoped.ID())
	assert.Equal(t, []string{portViewScope}, scoped.ScopedScopes())
	assert.Equal(t, TokenBoundary{Kind: BoundaryKindProject, ProjectID: f.launched.ProjectID}, scoped.Boundary())

	rec := doRequest(t, f.srv, http.MethodPatch, "/api/v1/users/"+f.launched.OwnerID, map[string]string{"status": store.UserStatusSuspended})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	requireAuthzClose(t, c)
}

// TestPortView_AuthPassThrough: step 3f admits a credential-less request
// of any method in the route's one shape, with no identity, and nothing
// else; the plain-text 401 of the port link (step 3g) is unchanged.
func TestPortView_AuthPassThrough(t *testing.T) {
	srv, _ := testServer(t)
	setPortViewExperiments(t, srv, false, true)
	type seen struct {
		identity Identity
		authType string
	}
	var mu sync.Mutex
	var got []seen
	h := UnifiedAuthMiddleware(srv.authConfig)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		authType, _ := r.Context().Value(logging.AuthTypeKey{}).(string)
		got = append(got, seen{GetIdentityFromContext(r.Context()), authType})
		mu.Unlock()
		w.WriteHeader(http.StatusTeapot)
	}))
	for _, method := range []string{http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodOptions} {
		for _, target := range []string{"/api/v1/portview/v1.x/", "/api/v1/portview/v1.x/a/b.js", "/api/v1/portview/cap/dir/"} {
			got = nil
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(method, target, nil))
			require.Equal(t, http.StatusTeapot, rec.Code, "%s %s", method, target)
			require.Len(t, got, 1)
			assert.True(t, isNilIdentity(got[0].identity), "%s %s: identity attached", method, target)
			assert.Equal(t, string(AuthTypeSignedURL), got[0].authType)
		}
	}
	for _, target := range []string{
		"/api/v1/portview/",
		"/api/v1/portviewx/cap/a",
		"/api/v1/portview//a",
		"/api/v1/portview/cap/../../agents/x",
		"/api/v1/portview/cap/./a",
		"/api/v1/portview/ca%2Fp/a",
		"/api/v1/portview/c%61p/a",
		"/api/v1/portview/cap/a%2fb",
		"/api/v1/portview/cap/%2e%2e/a",
		"/api/v1/portview/cap/a%5cb",
		"/api/v1/portview/cap/a%00",
		"/api/v1/portview/cap//a",
		"/api/v1/PORTVIEW/cap/a",
	} {
		got = nil
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
		assert.Equal(t, http.StatusUnauthorized, rec.Code, target)
		assert.Empty(t, got, "%s passed through", target)
		assert.Contains(t, rec.Header().Get("Content-Type"), "application/json", target)
	}

	// (b) The port link's plain-text 401 is unchanged.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/agents/a1/ports/3000/proxy/app.js", nil)
	req.Header.Set("Accept", "*/*")
	req.Header.Set("Origin", "null")
	h.ServeHTTP(rec, req)
	requirePortProxyNoCredentials(t, rec.Code, rec.Header(), rec.Body.String(), true)
	assert.Empty(t, got)
}

// TestPortView_AuthStepOrder: (a) a credential-less request to the page
// view route reaches the route's own answer through the full handler, and
// (c) step 3f sits above step 3g in UnifiedAuthMiddleware.
func TestPortView_AuthStepOrder(t *testing.T) {
	srv, _ := testServer(t)
	setPortViewExperiments(t, srv, false, true)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/portview/v1.not-a-link/app.js", nil)
	req.Header.Set("Origin", "null")
	req.Header.Set("Sec-Fetch-Mode", "cors")
	srv.Handler().ServeHTTP(rec, req)
	h := rec.Header().Clone()
	// A recorder does not add Content-Length; a server does.
	h.Set("Content-Length", strconv.Itoa(rec.Body.Len()))
	requirePortViewInvalid(t, pvResult{status: rec.Code, header: h, body: rec.Body.String()})

	src, err := os.ReadFile("auth.go")
	require.NoError(t, err)
	s := string(src)
	pv := strings.Index(s, "if isPortViewRequest(r) {")
	proxy := strings.Index(s, "if isProxyRoute(r.URL.Path) && !isBrowserRequest(r) {")
	require.Positive(t, pv, "step 3f not found in auth.go")
	require.Positive(t, proxy, "step 3g not found in auth.go")
	assert.Less(t, pv, proxy, "step 3f must stay above step 3g")
	assert.Equal(t, 1, strings.Count(s, "isPortViewRequest(r)"), "one step 3f")
}

// TestPortView_LogsNeverHoldTheLink: every log line written while serving
// page view requests (both request loggers, auth debug, the proxy line,
// failures, redirects and transport errors), and the request log line of
// any request whose Referer is a page link, holds no part of the
// capability; such requests are not traced.
func TestPortView_LogsNeverHoldTheLink(t *testing.T) {
	var buf safeBuffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	f := newPortViewFixture(t, nil)
	f.srv.authConfig.Debug = true
	f.srv.config.Debug = true
	// The auth middleware and the request logger are read when the handler
	// is built: one server with the default request log middleware, one
	// with the request logger cmd installs.
	legacySrv := httptest.NewServer(f.srv.Handler())
	t.Cleanup(legacySrv.Close)
	f.srv.SetRequestLogger(slog.Default())
	requestLogSrv := httptest.NewServer(f.srv.Handler())
	t.Cleanup(requestLogSrv.Close)
	bases := []string{legacySrv.URL, requestLogSrv.URL}

	valid := f.ownerCap()
	expired := f.capFor(f.launched.OwnerID, f.app.port, time.Now().Add(-time.Second))
	foreign := f.capFor(f.stranger.ID(), f.app.port, time.Now().Add(time.Hour))
	caps := []string{valid, expired, foreign}
	// Every request carries the page link as its Referer, as a page that
	// sets its own referrer policy would send it, and a User-Agent.
	h := func(base http.Header) http.Header {
		base.Set("Referer", f.base+portview.Route+valid+"/index.html?r=1")
		base.Set("User-Agent", "port-view-log-test")
		return base
	}

	// Before the agent connects: the agent-offline path.
	for _, b := range bases {
		f.base = b
		f.send(t, http.MethodGet, f.viewURL(valid, "a.js"), h(subHeader()), nil)
	}
	f.base = f.public.URL
	f.startAgent(t)
	for _, b := range bases {
		f.base = b
		link := f.portLink("p.html?x=1")
		minted := f.send(t, http.MethodGet, link, h(withBearer(navHeader(), f.userToken)), nil)
		require.Equal(t, http.StatusFound, minted.status)
		mintedCap, _, _ := strings.Cut(strings.TrimPrefix(resolveLocation(t, link, minted).Path, portview.Route), "/")
		caps = append(caps, mintedCap)
		f.send(t, http.MethodGet, f.viewURL(valid, "a.js?q=1"), h(subHeader()), nil)
		// A sub-path at the agent port that itself names a page link.
		f.send(t, http.MethodGet, f.viewURL(valid, "api/v1/portview/"+valid+"/x"), h(subHeader()), nil)
		f.send(t, http.MethodGet, f.viewURL(mintedCap, "p.html"), h(navHeader()), nil)
		f.send(t, http.MethodGet, f.viewURL(expired, "a.js"), h(subHeader()), nil)
		f.send(t, http.MethodGet, f.viewURL(expired, "p.html"), h(navHeader()), nil)
		f.send(t, http.MethodGet, f.viewURL(foreign, "a.js"), h(subHeader()), nil)
		f.send(t, http.MethodOptions, f.viewURL(valid, "a.js"), h(http.Header{"Origin": {"null"}, "Access-Control-Request-Method": {"GET"}}), nil)
		f.send(t, http.MethodGet, f.viewURL(valid, "a.js"), h(withBearer(navHeader(), f.strangerToken)), nil)
		// Another hub route, requested by the page with the link as Referer,
		// and with the link in its query.
		f.send(t, http.MethodGet, b+"/api/v1/agents", h(subHeader()), nil)
		f.send(t, http.MethodGet, b+"/api/v1/agents?next="+url.QueryEscape(f.viewURL(valid, "x")), h(subHeader()), nil)
	}
	// A transport error: the port answers nothing.
	f.app.srv.Close()
	for _, b := range bases {
		f.base = b
		f.send(t, http.MethodGet, f.viewURL(valid, "gone.js"), h(subHeader()), nil)
	}

	out := buf.String()
	require.Contains(t, out, `"surface":"portview"`, "the proxy line was logged")
	require.Contains(t, out, logging.RedactedPortViewPath, "request lines use the redacted path")
	require.Contains(t, out, `"httpRequest"`, "the request logger cmd installs wrote its lines")
	require.Contains(t, out, `"userAgent":"port-view-log-test"`, "the request log records request headers")
	for _, c := range caps {
		parts := strings.Split(c, ".")
		sig := parts[len(parts)-1]
		assert.NotContains(t, out, c)
		assert.NotContains(t, out, sig, "a capability signature was logged")
	}

	r := httptest.NewRequest(http.MethodGet, portview.Route+valid+"/a.js", nil)
	assert.False(t, traceableRequest(r), "page view requests are not traced")
}

// safeBuffer is a bytes.Buffer safe for concurrent writers.
type safeBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *safeBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *safeBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

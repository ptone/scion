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

package bridge

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/textproto"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/GoogleCloudPlatform/scion/pkg/transportauth"
)

const (
	testUATToken   = "scion_pat_origin-test-token"
	testGECred     = "google-origin-test-credential"
	testGEHubToken = "hub-issued-origin-test-token"
)

// bodyRecordedRequest is what redirectHub saw of one answered request.
type bodyRecordedRequest struct {
	recordedRequest
	body string
}

// redirectHub is a hub double that serves https and plain http on one port.
// A request without ?final=1 is answered with a redirect of the given status
// to redirectTo(r); a request with it is recorded and answered by answer.
type redirectHub struct {
	port     string
	endpoint string // https://<host>:<port>

	handled atomic.Int32 // every request the hub handled, redirects included

	mu   sync.Mutex
	reqs []bodyRecordedRequest
}

func newRedirectHub(t *testing.T, serverTLS *tls.Config, status int, redirectTo func(r *http.Request) string, answer http.HandlerFunc) *redirectHub {
	t.Helper()
	return newRedirectHubAt(t, serverTLS, testHubName, status, redirectTo, answer)
}

// newRedirectHubAt is newRedirectHub with the hub endpoint named host.
func newRedirectHubAt(t *testing.T, serverTLS *tls.Config, host string, status int, redirectTo func(r *http.Request) string, answer http.HandlerFunc) *redirectHub {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	h := &redirectHub{}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.handled.Add(1)
		if r.URL.Query().Get("final") != "1" {
			// The Location header is written as given: http.Redirect
			// would percent-encode non-ASCII bytes.
			w.Header().Set("Location", redirectTo(r))
			w.WriteHeader(status)
			return
		}
		body, _ := io.ReadAll(r.Body)
		scheme := "http"
		if r.TLS != nil {
			scheme = "https"
		}
		h.mu.Lock()
		h.reqs = append(h.reqs, bodyRecordedRequest{
			recordedRequest: recordedRequest{scheme: scheme, host: r.Host, header: r.Header.Clone()},
			body:            string(body),
		})
		h.mu.Unlock()
		answer(w, r)
	})
	srv := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = srv.Serve(&schemeMuxListener{Listener: ln, tlsConfig: serverTLS}) }()
	t.Cleanup(func() { _ = srv.Close() })
	_, h.port, _ = net.SplitHostPort(ln.Addr().String())
	h.endpoint = "https://" + net.JoinHostPort(host, h.port)
	return h
}

func (h *redirectHub) requests() []bodyRecordedRequest {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]bodyRecordedRequest(nil), h.reqs...)
}

// originRedirectTargets are the redirect targets outside the hub origin. other
// is an https server on another port; a target on the hub's own port carries
// ?final=1 so the hub would answer and record it.
var originRedirectTargets = map[string]func(r *http.Request, hubPort, otherPort string) string{
	"other host": func(r *http.Request, _, otherPort string) string {
		return "https://other.example.com:" + otherPort + r.URL.Path + "?final=1"
	},
	"same host other port": func(r *http.Request, _, otherPort string) string {
		return "https://" + testHubName + ":" + otherPort + r.URL.Path + "?final=1"
	},
	"same host and port other scheme": func(r *http.Request, hubPort, _ string) string {
		return "http://" + testHubName + ":" + hubPort + r.URL.Path + "?final=1"
	},
}

// redirectStatuses are the redirect statuses the client tests run under: 302
// (a POST becomes a GET without a body) and 307 (the method and body are
// sent again).
var redirectStatuses = []int{http.StatusFound, http.StatusTemporaryRedirect}

// originClient is one hub client under test: call makes the client's hub
// request against endpoint, answer answers it at the hub, and check checks
// the credentials of a request the hub answered.
type originClient struct {
	call   func(t *testing.T, endpoint string) error
	answer http.HandlerFunc
	check  func(t *testing.T, got bodyRecordedRequest)
}

// assertTransportAuth checks the transport auth header of got for mode (nil:
// no transport auth).
func assertTransportAuth(t *testing.T, got recordedRequest, mode *transportauth.HeaderMode) {
	t.Helper()
	want := map[string]string{"Proxy-Authorization": "", "X-Serverless-Authorization": ""}
	if mode != nil {
		switch *mode {
		case transportauth.HeaderAuthorization:
			want["Authorization"] = "Bearer " + testTransportToken
		case transportauth.HeaderProxyAuthorization:
			want["Proxy-Authorization"] = "Bearer " + testTransportToken
		case transportauth.HeaderServerlessAuthorization:
			want["X-Serverless-Authorization"] = "Bearer " + testTransportToken
		}
	}
	for name, value := range want {
		if got.header.Get(name) != value {
			t.Errorf("%s = %q, want %q", name, got.header.Get(name), value)
		}
	}
}

// uatOriginClients covers the UAT validator, which attaches the caller's
// credential in Authorization.
func uatOriginClients() map[string]originClient {
	return map[string]originClient{
		"uat/caller Authorization": {
			call: func(t *testing.T, endpoint string) error {
				_, err := NewUATValidator(endpoint, time.Minute).Validate(context.Background(), testUATToken)
				return err
			},
			answer: func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, `{"id":"user-1","email":"user@example.com"}`)
			},
			check: func(t *testing.T, got bodyRecordedRequest) {
				if a := got.header.Get("Authorization"); a != "Bearer "+testUATToken {
					t.Errorf("Authorization = %q, want the caller's bearer", a)
				}
			},
		},
	}
}

// oidcOriginClients covers the OIDC proxy client in each transport auth mode.
func oidcOriginClients() map[string]originClient {
	clients := map[string]originClient{}
	for modeName, mode := range transportAuthModes {
		clients["oidc/"+modeName] = originClient{
			call: func(t *testing.T, endpoint string) error {
				s := &Server{bridge: newFederationTestBridge(t, endpoint, mode)}
				_, err := s.fetchCachedOIDC(context.Background(), "jwks", endpoint+"/.well-known/jwks.json")
				return err
			},
			answer: func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, `{"keys":[]}`)
			},
			check: func(t *testing.T, got bodyRecordedRequest) {
				assertTransportAuth(t, got.recordedRequest, mode)
			},
		}
	}
	return clients
}

// geOriginClients covers the GE exchange validator, which sends the caller's
// credential in the request body, in each transport auth mode. Transport auth
// is set by option or by SetTransportAuth, on the default client, on a client
// given by WithGEHTTPClient (transport auth by option) and on a client given
// by SetHTTPClient (transport auth by SetTransportAuth).
func geOriginClients() map[string]originClient {
	answer := func(w http.ResponseWriter, r *http.Request) {
		user := geExchangeUser{ID: "user-1", Email: "user@example.com"}
		writeExchangeResponse(w, user, testGEHubToken, time.Now().Add(time.Hour), time.Now().Add(time.Hour))
	}
	cfg := GEExchangeConfig{CredentialType: "id_token", CacheTTL: time.Minute}
	clients := map[string]originClient{}
	for modeName, mode := range transportAuthModes {
		for _, setter := range []string{"option", "SetTransportAuth", "WithGEHTTPClient", "SetHTTPClient"} {
			clients["ge/"+modeName+"/"+setter] = originClient{
				call: func(t *testing.T, endpoint string) error {
					var opts []GEValidatorOption
					if setter == "WithGEHTTPClient" {
						opts = append(opts, WithGEHTTPClient(&http.Client{Timeout: 15 * time.Second}))
					}
					byOption := setter == "option" || setter == "WithGEHTTPClient"
					if mode != nil && byOption {
						opts = append(opts, WithGETransportAuth(staticTransportSource{}, *mode))
					}
					v := NewGEExchangeValidator(endpoint, cfg, testLogger(), opts...)
					if setter == "SetHTTPClient" {
						v.SetHTTPClient(&http.Client{Timeout: 15 * time.Second})
					}
					if mode != nil && !byOption {
						v.SetTransportAuth(staticTransportSource{}, *mode)
					}
					_, err := v.Validate(context.Background(), testGECred)
					return err
				},
				answer: answer,
				check: func(t *testing.T, got bodyRecordedRequest) {
					assertTransportAuth(t, got.recordedRequest, mode)
				},
			}
		}
	}
	return clients
}

func allOriginClients() map[string]originClient {
	clients := map[string]originClient{}
	for _, group := range []map[string]originClient{uatOriginClients(), oidcOriginClients(), geOriginClients()} {
		for name, c := range group {
			clients[name] = c
		}
	}
	return clients
}

func TestHubOriginClients_OtherOriginRedirectRefused(t *testing.T) {
	for clientName, client := range allOriginClients() {
		for caseName, target := range originRedirectTargets {
			for _, status := range redirectStatuses {
				t.Run(fmt.Sprintf("%s/%s/%d", clientName, caseName, status), func(t *testing.T) {
					serverTLS := installTestTransport(t)
					other := newTLSHeaderRecorder(t)
					otherPort := other.port(t)
					var hub *redirectHub
					hub = newRedirectHub(t, serverTLS, status, func(r *http.Request) string {
						return target(r, hub.port, otherPort)
					}, client.answer)

					err := client.call(t, hub.endpoint)
					if got := other.requests(); len(got) != 0 {
						t.Errorf("other origin received %d requests, want 0", len(got))
					}
					if got := hub.requests(); len(got) != 0 {
						t.Errorf("redirect target on the hub port received %d requests, want 0", len(got))
					}
					if !errors.Is(err, errHubOtherOrigin) {
						t.Fatalf("call error = %v, want errHubOtherOrigin", err)
					}
					assertErrorOmitsTarget(t, err, otherPort)
				})
			}
		}
	}
}

// assertErrorOmitsTarget checks that err's text names no URL and no host:
// no "://", neither the hub host nor the other host, and not the other
// server's port (when otherPort is not empty).
func assertErrorOmitsTarget(t *testing.T, err error, otherPort string) {
	t.Helper()
	msg := err.Error()
	for _, s := range []string{"://", "other.example.com", testHubName, unicodeHubName} {
		if strings.Contains(msg, s) {
			t.Errorf("error %q includes %q", msg, s)
		}
	}
	if otherPort != "" && containsURL(msg, ":"+otherPort) {
		t.Errorf("error %q includes the other port", msg)
	}
}

func TestHubOriginClients_SameOriginRedirectFollowed(t *testing.T) {
	for clientName, client := range allOriginClients() {
		for _, status := range redirectStatuses {
			t.Run(fmt.Sprintf("%s/%d", clientName, status), func(t *testing.T) {
				serverTLS := installTestTransport(t)
				hub := newRedirectHub(t, serverTLS, status, func(r *http.Request) string {
					return r.URL.Path + "?final=1"
				}, client.answer)

				if err := client.call(t, hub.endpoint); err != nil {
					t.Fatalf("call: %v", err)
				}
				got := hub.requests()
				if len(got) != 1 {
					t.Fatalf("hub answered %d requests, want 1", len(got))
				}
				if got[0].scheme != "https" {
					t.Errorf("hub answered over %s, want https", got[0].scheme)
				}
				client.check(t, got[0])
			})
		}
	}
}

func TestGEExchangeValidator_SameOriginTemporaryRedirectResendsBody(t *testing.T) {
	serverTLS := installTestTransport(t)
	client := geOriginClients()["ge/no transport auth/option"]
	hub := newRedirectHub(t, serverTLS, http.StatusTemporaryRedirect, func(r *http.Request) string {
		return r.URL.Path + "?final=1"
	}, client.answer)
	if err := client.call(t, hub.endpoint); err != nil {
		t.Fatalf("call: %v", err)
	}
	got := hub.requests()
	if len(got) != 1 {
		t.Fatalf("hub answered %d requests, want 1", len(got))
	}
	if !strings.Contains(got[0].body, testGECred) {
		t.Errorf("hub request body does not carry the credential")
	}
}

// closeRecorder is a request body that records whether it was closed.
type closeRecorder struct {
	io.Reader
	closed bool
}

func (c *closeRecorder) Close() error {
	c.closed = true
	return nil
}

func TestHubOriginTransport_RefusesOtherOriginBeforeBase(t *testing.T) {
	base := &countingRoundTripper{}
	transport := newHubOriginTransport(base, "https://"+testHubName)
	for _, target := range []string{
		"https://other.example.com/api",
		"http://" + testHubName + "/api",
		"https://" + testHubName + ":8443/api",
		"https://sub." + testHubName + "/api",
	} {
		body := &closeRecorder{Reader: strings.NewReader("credential")}
		req, err := http.NewRequest(http.MethodPost, target, body)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := transport.RoundTrip(req)
		if resp != nil {
			t.Errorf("RoundTrip(%s) returned a response", target)
		}
		if !errors.Is(err, errHubOtherOrigin) {
			t.Errorf("RoundTrip(%s) error = %v, want errHubOtherOrigin", target, err)
		}
		if !body.closed {
			t.Errorf("RoundTrip(%s) did not close the request body", target)
		}
		if strings.Contains(err.Error(), target) {
			t.Errorf("error %q includes the request URL", err)
		}
	}
	if n := base.calls.Load(); n != 0 {
		t.Errorf("base transport saw %d requests, want 0", n)
	}
}

func TestHubOriginTransport_PassesHubOriginToBase(t *testing.T) {
	base := &countingRoundTripper{}
	transport := newHubOriginTransport(base, "https://"+strings.ToUpper(testHubName))
	for _, target := range []string{
		"https://" + testHubName + "/api",
		"https://" + testHubName + ":443/api",
		"HTTPS://" + testHubName + "/other?x=1",
	} {
		req, err := http.NewRequest(http.MethodGet, target, nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := transport.RoundTrip(req); err != nil {
			t.Errorf("RoundTrip(%s): %v", target, err)
		}
	}
	if n := base.calls.Load(); n != 3 {
		t.Errorf("base transport saw %d requests, want 3", n)
	}
}

func TestHubOriginTransport_EndpointWithoutOriginRefusesAll(t *testing.T) {
	for _, endpoint := range []string{"", "hub.example.com", "://bad", "https://hub example.com"} {
		base := &countingRoundTripper{}
		transport := newHubOriginTransport(base, endpoint)
		for _, target := range []string{"https://hub.example.com/api", "http://hub.example.com/api"} {
			req, err := http.NewRequest(http.MethodGet, target, nil)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := transport.RoundTrip(req); !errors.Is(err, errHubOtherOrigin) {
				t.Errorf("endpoint %q: RoundTrip(%s) error = %v, want errHubOtherOrigin", endpoint, target, err)
			}
		}
		if n := base.calls.Load(); n != 0 {
			t.Errorf("endpoint %q: base transport saw %d requests, want 0", endpoint, n)
		}
	}
}

func TestParseHubOrigin_ErrorsOmitEndpoint(t *testing.T) {
	const password = "s3cret-pw"
	for _, endpoint := range []string{
		"https://hub-user:" + password + "@",
		"hub-user:" + password + "@hub.example.com",
		"https://hub-user:" + password + "@hub example.com",
	} {
		_, err := parseHubOrigin(endpoint)
		if err == nil {
			t.Errorf("parseHubOrigin(%q) succeeded, want an error", endpoint)
			continue
		}
		if msg := err.Error(); strings.Contains(msg, password) || strings.Contains(msg, "hub-user") {
			t.Errorf("error for endpoint %q includes endpoint text: %q", endpoint, msg)
		}
	}
}

func TestHubOriginContains_ZeroOriginContainsNothing(t *testing.T) {
	for _, raw := range []string{"https://hub.example.com/", "http://:80/", "/relative"} {
		u, err := url.Parse(raw)
		if err != nil {
			t.Fatal(err)
		}
		if (hubOrigin{}).contains(u) {
			t.Errorf("zero hubOrigin contains %q", raw)
		}
	}
}

func TestGEExchangeValidator_HubOriginTransportOutermost(t *testing.T) {
	cfg := GEExchangeConfig{CredentialType: "id_token"}
	check := func(name string, v *GEExchangeValidator) {
		t.Helper()
		if _, ok := v.httpClient.Transport.(*hubOriginTransport); !ok {
			t.Errorf("%s: outermost transport is %T, want *hubOriginTransport", name, v.httpClient.Transport)
		}
	}
	v := NewGEExchangeValidator("https://"+testHubName, cfg, testLogger())
	check("default", v)
	v.SetTransportAuth(staticTransportSource{}, transportauth.HeaderServerlessAuthorization)
	check("after SetTransportAuth", v)
	v.SetHTTPClient(&http.Client{})
	check("after SetHTTPClient", v)
	v.SetTransportAuth(staticTransportSource{}, transportauth.HeaderProxyAuthorization)
	check("after SetHTTPClient and SetTransportAuth", v)
	guard, ok := v.httpClient.Transport.(*hubOriginTransport)
	if !ok {
		t.Fatalf("outermost transport is %T, want *hubOriginTransport", v.httpClient.Transport)
	}
	if guard.base == nil {
		t.Error("SetTransportAuth left no transport auth inside the hubOriginTransport")
	}
	check("with option", NewGEExchangeValidator("https://"+testHubName, cfg, testLogger(),
		WithGEHTTPClient(&http.Client{}), WithGETransportAuth(staticTransportSource{}, transportauth.HeaderAuthorization)))
}

// dialLog records the addresses a transport dials.
type dialLog struct {
	mu    sync.Mutex
	addrs []string
}

func (d *dialLog) dialed() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.addrs...)
}

// installDialRecordingTransport is installTestTransport with every address
// http.DefaultTransport dials recorded before the test dialer maps it to
// loopback.
func installDialRecordingTransport(t *testing.T) (*tls.Config, *dialLog) {
	t.Helper()
	serverTLS := installTestTransport(t)
	tr, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		t.Fatalf("http.DefaultTransport is %T, want *http.Transport", http.DefaultTransport)
	}
	log := &dialLog{}
	dial := tr.DialContext
	tr.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		log.mu.Lock()
		log.addrs = append(log.addrs, addr)
		log.mu.Unlock()
		return dial(ctx, network, addr)
	}
	return serverTLS, log
}

// unicodeHubName is a hub host name with an "i" and a "k", which U+0130 and
// U+212A fold onto under strings.ToLower.
const unicodeHubName = "kelvin.internal"

// unicodeFoldTargets are redirect targets on the hub port whose host folds
// onto unicodeHubName under strings.ToLower. ?final=1 makes the hub answer
// and record a request that reaches it.
var unicodeFoldTargets = map[string]func(r *http.Request, hubPort string) string{
	"U+0130 for i": func(r *http.Request, hubPort string) string {
		return "https://kelvin.%C4%B0nternal:" + hubPort + r.URL.Path + "?final=1"
	},
	"U+212A for k": func(r *http.Request, hubPort string) string {
		return "https://%E2%84%AAelvin.internal:" + hubPort + r.URL.Path + "?final=1"
	},
	"raw U+0130 for i": func(r *http.Request, hubPort string) string {
		return "https://kelvin.\u0130nternal:" + hubPort + r.URL.Path + "?final=1"
	},
	"raw U+212A for k": func(r *http.Request, hubPort string) string {
		return "https://\u212aelvin.internal:" + hubPort + r.URL.Path + "?final=1"
	},
}

func TestHubOriginClients_UnicodeFoldHostRedirectRefused(t *testing.T) {
	for clientName, client := range allOriginClients() {
		for caseName, target := range unicodeFoldTargets {
			for _, status := range redirectStatuses {
				t.Run(fmt.Sprintf("%s/%s/%d", clientName, caseName, status), func(t *testing.T) {
					serverTLS, dials := installDialRecordingTransport(t)
					var hub *redirectHub
					hub = newRedirectHubAt(t, serverTLS, unicodeHubName, status, func(r *http.Request) string {
						return target(r, hub.port)
					}, client.answer)

					err := client.call(t, hub.endpoint)
					hubAddr := net.JoinHostPort(unicodeHubName, hub.port)
					for _, addr := range dials.dialed() {
						if addr != hubAddr {
							t.Errorf("dialed %q, want only the hub %q", addr, hubAddr)
						}
					}
					if got := hub.requests(); len(got) != 0 {
						t.Errorf("redirect target on the hub port received %d requests, want 0", len(got))
					}
					if !errors.Is(err, errHubOtherOrigin) {
						t.Fatalf("call error = %v, want errHubOtherOrigin", err)
					}
					assertErrorOmitsTarget(t, err, "")
				})
			}
		}
	}
}

func TestHubOriginContains_Table(t *testing.T) {
	cases := []struct {
		name, endpoint, target string
		want                   bool
	}{
		{"percent-encoded U+0130 host", "https://hub.internal", "https://hub.%C4%B0nternal/api", false},
		{"raw UTF-8 U+0130 host", "https://hub.internal", "https://hub.\u0130nternal/api", false},
		{"U+212A Kelvin sign host", "https://kelvin.internal", "https://\u212aelvin.internal/api", false},
		{"trailing-dot host", "https://hub.internal", "https://hub.internal./api", false},
		{"port with leading zeros", "https://hub.internal", "https://hub.internal:0443/api", false},
		{"IPv6 with and without default port", "https://[::1]", "https://[::1]:443/api", true},
		{"upper-case IPv6 hex", "https://[::abcd]", "https://[::ABCD]/api", true},
		{"userinfo with the hub host", "https://hub.internal", "https://user:pw@hub.internal/api", true},
		{"same origin", "https://hub.internal", "https://HUB.internal:443/api", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			origin, err := parseHubOrigin(tc.endpoint)
			if err != nil {
				t.Fatalf("parseHubOrigin(%q): %v", tc.endpoint, err)
			}
			u, err := url.Parse(tc.target)
			if err != nil {
				t.Fatalf("url.Parse(%q): %v", tc.target, err)
			}
			if got := origin.contains(u); got != tc.want {
				t.Errorf("origin of %q contains %q = %v, want %v", tc.endpoint, tc.target, got, tc.want)
			}
		})
	}
}

func TestHubOriginTransport_RefusesUnicodeFoldHosts(t *testing.T) {
	cases := []struct{ endpoint, target string }{
		{"https://hub.internal", "https://hub.%C4%B0nternal/api"},
		{"https://hub.internal", "https://hub.\u0130nternal/api"},
		{"https://kelvin.internal", "https://\u212aelvin.internal/api"},
		{"https://hub.internal", "https://hub.internal./api"},
		{"https://hub.internal", "https://hub.internal:0443/api"},
	}
	for _, tc := range cases {
		base := &countingRoundTripper{}
		transport := newHubOriginTransport(base, tc.endpoint)
		req, err := http.NewRequest(http.MethodGet, tc.target, nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := transport.RoundTrip(req); !errors.Is(err, errHubOtherOrigin) {
			t.Errorf("RoundTrip(%s) error = %v, want errHubOtherOrigin", tc.target, err)
		}
		if n := base.calls.Load(); n != 0 {
			t.Errorf("RoundTrip(%s): base transport saw %d requests, want 0", tc.target, n)
		}
	}
}

func TestParseHubOrigin_NonASCIIHostRefused(t *testing.T) {
	for _, endpoint := range []string{
		"https://hub.\u0130nternal",
		"https://hub.%C4%B0nternal",
		"https://\u212aelvin.internal",
		"https://user:pw@hub.\u0130nternal",
	} {
		_, err := parseHubOrigin(endpoint)
		if err == nil {
			t.Errorf("parseHubOrigin(%q) succeeded, want an error", endpoint)
			continue
		}
		if msg := err.Error(); strings.Contains(msg, "nternal") || strings.Contains(msg, "pw") {
			t.Errorf("error for endpoint %q includes endpoint text: %q", endpoint, msg)
		}
	}
}

func TestFederationHeaderTransport_RefusesUnicodeFoldHost(t *testing.T) {
	base := &countingRoundTripper{}
	transport, err := newFederationHeaderTransport(base, testFederationToken, "https://hub.internal")
	if err != nil {
		t.Fatalf("newFederationHeaderTransport: %v", err)
	}
	for _, target := range []string{"https://hub.%C4%B0nternal/api", "https://hub.\u0130nternal/api"} {
		req, err := http.NewRequest(http.MethodGet, target, nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := transport.RoundTrip(req); !errors.Is(err, errFederationOtherOrigin) {
			t.Errorf("RoundTrip(%s) error = %v, want errFederationOtherOrigin", target, err)
		}
	}
	if n := base.calls.Load(); n != 0 {
		t.Errorf("base transport saw %d requests, want 0", n)
	}
	if _, err := newFederationHeaderTransport(base, testFederationToken, "https://hub.\u0130nternal"); err == nil {
		t.Error("newFederationHeaderTransport with a non-ASCII host succeeded, want an error")
	}
}

// hubOriginLayers returns how many hubOriginTransport layers t starts with.
func hubOriginLayers(t http.RoundTripper) int {
	n := 0
	for {
		guard, ok := t.(*hubOriginTransport)
		if !ok {
			return n
		}
		n++
		t = guard.base
	}
}

func TestGEExchangeValidator_SingleHubOriginLayer(t *testing.T) {
	cfg := GEExchangeConfig{CredentialType: "id_token"}
	endpoint := "https://" + testHubName
	guarded := &http.Client{Transport: newHubOriginTransport(&countingRoundTripper{}, endpoint)}
	callerTransport := guarded.Transport

	v := NewGEExchangeValidator(endpoint, cfg, testLogger(),
		WithGEHTTPClient(guarded), WithGETransportAuth(staticTransportSource{}, transportauth.HeaderAuthorization))
	if n := hubOriginLayers(v.httpClient.Transport); n != 1 {
		t.Errorf("after the constructor: %d hubOriginTransport layers, want 1", n)
	}
	guard, ok := v.httpClient.Transport.(*hubOriginTransport)
	if !ok {
		t.Fatalf("outermost transport is %T, want *hubOriginTransport", v.httpClient.Transport)
	}
	if hubOriginLayers(guard.base) != 0 {
		t.Error("transport auth wraps a hubOriginTransport")
	}
	v.SetTransportAuth(staticTransportSource{}, transportauth.HeaderProxyAuthorization)
	if n := hubOriginLayers(v.httpClient.Transport); n != 1 {
		t.Errorf("after SetTransportAuth: %d hubOriginTransport layers, want 1", n)
	}
	v.SetHTTPClient(guarded)
	if n := hubOriginLayers(v.httpClient.Transport); n != 1 {
		t.Errorf("after SetHTTPClient with a guarded client: %d hubOriginTransport layers, want 1", n)
	}
	if guarded.Transport != callerTransport {
		t.Error("the caller's client Transport was changed")
	}
	if v.httpClient == guarded {
		t.Error("the validator uses the caller's client, want a copy")
	}
}

func TestGEExchangeValidator_WithGEHTTPClientLeavesCallerClient(t *testing.T) {
	custom := &http.Client{Timeout: 30 * time.Second}
	v := NewGEExchangeValidator("https://"+testHubName, GEExchangeConfig{CredentialType: "id_token"}, testLogger(),
		WithGEHTTPClient(custom), WithGETransportAuth(staticTransportSource{}, transportauth.HeaderAuthorization))
	if custom.Transport != nil {
		t.Errorf("WithGEHTTPClient set the caller's client Transport to %T", custom.Transport)
	}
	if v.httpClient.Timeout != 30*time.Second {
		t.Errorf("validator client Timeout = %v, want the custom client's 30s", v.httpClient.Timeout)
	}
}

// TestHubOriginClients_TransportFailureErrorOmitsURL makes each client's
// hub request fail in the transport (the hub closes every connection) and
// checks that the error quotes no URL.
func TestHubOriginClients_TransportFailureErrorOmitsURL(t *testing.T) {
	for clientName, client := range allOriginClients() {
		t.Run(clientName, func(t *testing.T) {
			installTestTransport(t)
			ln, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = ln.Close() })
			// The hub accepts each connection and closes it at once, so
			// every request fails in the transport.
			go func() {
				for {
					c, err := ln.Accept()
					if err != nil {
						return
					}
					_ = c.Close()
				}
			}()
			_, port, _ := net.SplitHostPort(ln.Addr().String())
			endpoint := "https://" + net.JoinHostPort(testHubName, port)

			err = client.call(t, endpoint)
			if err == nil {
				t.Fatal("call to a hub that closes every connection succeeded, want an error")
			}
			if errors.Is(err, errHubOtherOrigin) {
				t.Fatalf("call error = %v, want a transport failure", err)
			}
			if msg := err.Error(); strings.Contains(msg, "://") {
				t.Errorf("error %q quotes a URL", msg)
			}
		})
	}
}

// TestHubOriginClients_UnparsableLocationErrorOmitsUserInformation has the
// hub redirect to a Location that url.Parse rejects and that carries user
// information. Each client must fail with errHubRedirectLocation, quote
// neither a URL nor the user information, and send the hub exactly one
// request (the one it redirected).
func TestHubOriginClients_UnparsableLocationErrorOmitsUserInformation(t *testing.T) {
	const user, password = "redir-user", "s3cret-pw"
	for clientName, client := range allOriginClients() {
		t.Run(clientName, func(t *testing.T) {
			serverTLS := installTestTransport(t)
			hub := newRedirectHub(t, serverTLS, http.StatusFound, func(r *http.Request) string {
				return "https://" + user + ":" + password + "@hub example.com" + r.URL.Path
			}, client.answer)

			err := client.call(t, hub.endpoint)
			if err == nil {
				t.Fatal("call with an unparsable redirect Location succeeded, want an error")
			}
			if !errors.Is(err, errHubRedirectLocation) {
				t.Errorf("call error = %v, want errHubRedirectLocation", err)
			}
			msg := err.Error()
			for _, s := range []string{"://", user, password} {
				if strings.Contains(msg, s) {
					t.Errorf("error %q includes %q", msg, s)
				}
			}
			if n := hub.handled.Load(); n != 1 {
				t.Errorf("hub handled %d requests, want 1", n)
			}
		})
	}
}

// TestFederationCallSites_UnparsableLocationRefused has the hub redirect a
// federation call to a Location that url.Parse rejects and that carries
// user information. Each call site must fail with errHubRedirectLocation,
// must not quote the user information, and must send the hub exactly one
// request.
func TestFederationCallSites_UnparsableLocationRefused(t *testing.T) {
	const user, password = "redir-user", "s3cret-pw"
	for site, call := range federationCallSites {
		t.Run(site, func(t *testing.T) {
			serverTLS := installTestTransport(t)
			hub := newRedirectHub(t, serverTLS, http.StatusFound, func(r *http.Request) string {
				return "https://" + user + ":" + password + "@hub example.com" + r.URL.Path
			}, func(w http.ResponseWriter, r *http.Request) {})

			err := call(t, newFederationTestBridge(t, hub.endpoint))
			if !errors.Is(err, errHubRedirectLocation) {
				t.Fatalf("call error = %v, want errHubRedirectLocation", err)
			}
			msg := err.Error()
			for _, s := range []string{user, password} {
				if strings.Contains(msg, s) {
					t.Errorf("error %q includes %q", msg, s)
				}
			}
			if site == "federatedUserGet" && strings.Contains(msg, "://") {
				t.Errorf("error %q quotes a URL", msg)
			}
			if n := hub.handled.Load(); n != 1 {
				t.Errorf("hub handled %d requests, want 1", n)
			}
		})
	}
}

// locationRoundTripper answers every request with status and Location loc,
// recording whether the response body was closed.
type locationRoundTripper struct {
	status int
	loc    string
	body   *closeRecorder
}

func (l *locationRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	l.body = &closeRecorder{Reader: strings.NewReader("redirect")}
	h := http.Header{}
	h.Set("Location", l.loc)
	return &http.Response{StatusCode: l.status, Header: h, Body: l.body, Request: req}, nil
}

func TestHubOriginTransport_UnparsableLocation(t *testing.T) {
	const bad = "https://u:pw@hub example.com/x"
	cases := []struct {
		name    string
		status  int
		loc     string
		refused bool
	}{
		{"302 unparsable", http.StatusFound, bad, true},
		{"301 unparsable", http.StatusMovedPermanently, bad, true},
		{"303 unparsable", http.StatusSeeOther, bad, true},
		{"307 unparsable", http.StatusTemporaryRedirect, bad, true},
		{"308 unparsable", http.StatusPermanentRedirect, bad, true},
		{"302 parsable", http.StatusFound, "/other", false},
		{"302 empty", http.StatusFound, "", false},
		{"300 unparsable, not followed by http.Client", http.StatusMultipleChoices, bad, false},
		{"200 unparsable", http.StatusOK, bad, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			base := &locationRoundTripper{status: tc.status, loc: tc.loc}
			transport := newHubOriginTransport(base, "https://"+testHubName)
			req, err := http.NewRequest(http.MethodGet, "https://"+testHubName+"/api", nil)
			if err != nil {
				t.Fatal(err)
			}
			resp, err := transport.RoundTrip(req)
			if tc.refused {
				if !errors.Is(err, errHubRedirectLocation) || resp != nil {
					t.Fatalf("RoundTrip = %v, %v; want nil, errHubRedirectLocation", resp, err)
				}
				if !base.body.closed {
					t.Error("refused response body was not closed")
				}
				if strings.Contains(err.Error(), "pw") {
					t.Errorf("error %q includes the Location user information", err)
				}
				return
			}
			if err != nil || resp == nil || resp.StatusCode != tc.status {
				t.Fatalf("RoundTrip = %v, %v; want the base response", resp, err)
			}
			_ = resp.Body.Close()
		})
	}
}

// injectedTransportText is the text of errInjectedTransport: a URL with user
// information and the hub host and port, as a dial error or a malformed
// response header line can carry them.
const injectedTransportText = "dial tcp hub.example.com:443: Location: https://redir-user:s3cret-pw@evil.example/x"

// errInjectedTransport is the error errorRoundTripper returns.
var errInjectedTransport = errors.New(injectedTransportText)

// errorRoundTripper fails every request with err.
type errorRoundTripper struct{ err error }

func (e errorRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Body != nil {
		_ = req.Body.Close()
	}
	return nil, e.err
}

// assertNoTransportText checks that msg quotes nothing of
// injectedTransportText or of a Location with user information.
func assertNoTransportText(t *testing.T, msg string) {
	t.Helper()
	for _, s := range []string{"://", "redir-user", "s3cret-pw", "evil.example", "hub.example.com"} {
		if strings.Contains(msg, s) {
			t.Errorf("error %q includes %q", msg, s)
		}
	}
}

// hubTransports are the two transports that guard hub requests, each over
// base, for the hub https://hub.example.com.
var hubTransports = map[string]func(t *testing.T, base http.RoundTripper) http.RoundTripper{
	"hubOriginTransport": func(t *testing.T, base http.RoundTripper) http.RoundTripper {
		return newHubOriginTransport(base, "https://"+testHubName)
	},
	"federationHeaderTransport": func(t *testing.T, base http.RoundTripper) http.RoundTripper {
		tr, err := newFederationHeaderTransport(base, testFederationToken, "https://"+testHubName)
		if err != nil {
			t.Fatal(err)
		}
		return tr
	},
}

func TestHubTransports_BaseErrorTextFixed(t *testing.T) {
	for name, build := range hubTransports {
		t.Run(name, func(t *testing.T) {
			transport := build(t, errorRoundTripper{err: errInjectedTransport})
			req, err := http.NewRequest(http.MethodGet, "https://"+testHubName+"/api", nil)
			if err != nil {
				t.Fatal(err)
			}
			_, err = transport.RoundTrip(req)
			if !errors.Is(err, errInjectedTransport) {
				t.Fatalf("RoundTrip error = %v, want one wrapping the base error", err)
			}
			assertNoTransportText(t, err.Error())
		})
	}
}

// timeoutError is a net.Error that is a timeout.
type timeoutError struct{}

func (timeoutError) Error() string   { return "i/o timeout" }
func (timeoutError) Timeout() bool   { return true }
func (timeoutError) Temporary() bool { return true }

func TestHubTransports_BaseErrorChainKept(t *testing.T) {
	for name, build := range hubTransports {
		t.Run(name, func(t *testing.T) {
			roundTrip := func(t *testing.T, baseErr error) error {
				t.Helper()
				req, err := http.NewRequest(http.MethodGet, "https://"+testHubName+"/api", nil)
				if err != nil {
					t.Fatal(err)
				}
				_, err = build(t, errorRoundTripper{err: baseErr}).RoundTrip(req)
				return err
			}
			t.Run("context.Canceled", func(t *testing.T) {
				if err := roundTrip(t, context.Canceled); !errors.Is(err, context.Canceled) {
					t.Errorf("error = %v, want errors.Is context.Canceled", err)
				}
			})
			t.Run("context.DeadlineExceeded", func(t *testing.T) {
				if err := roundTrip(t, context.DeadlineExceeded); !errors.Is(err, context.DeadlineExceeded) {
					t.Errorf("error = %v, want errors.Is context.DeadlineExceeded", err)
				}
			})
			t.Run("net.Error timeout", func(t *testing.T) {
				err := roundTrip(t, fmt.Errorf("read: %w", timeoutError{}))
				var netErr net.Error
				if !errors.As(err, &netErr) || !netErr.Timeout() {
					t.Errorf("error = %v, want errors.As a net.Error with Timeout", err)
				}
				client := &http.Client{Transport: build(t, errorRoundTripper{err: timeoutError{}})}
				_, err = client.Get("https://" + testHubName + "/api")
				var urlErr *url.Error
				if !errors.As(err, &urlErr) || !urlErr.Timeout() {
					t.Errorf("client error = %v, want a url.Error with Timeout", err)
				}
			})
			for _, sentinel := range []error{errHubOtherOrigin, errFederationOtherOrigin, errHubRedirectLocation} {
				t.Run("sentinel "+sentinel.Error(), func(t *testing.T) {
					if err := roundTrip(t, sentinel); err != sentinel {
						t.Errorf("error = %#v, want the sentinel itself", err)
					}
				})
			}
		})
	}
}

// installErrorDefaultTransport replaces http.DefaultTransport, which every
// hub client builds on, with errorRoundTripper{err}. It is restored on
// cleanup; tests that use it must not call t.Parallel.
func installErrorDefaultTransport(t *testing.T, err error) {
	t.Helper()
	prev := http.DefaultTransport
	http.DefaultTransport = errorRoundTripper{err: err}
	t.Cleanup(func() { http.DefaultTransport = prev })
}

func TestHubOriginClients_TransportErrorOmitsItsText(t *testing.T) {
	for clientName, client := range allOriginClients() {
		t.Run(clientName, func(t *testing.T) {
			installErrorDefaultTransport(t, errInjectedTransport)
			err := client.call(t, "https://"+testHubName)
			if !errors.Is(err, errInjectedTransport) {
				t.Fatalf("call error = %v, want one wrapping the transport error", err)
			}
			assertNoTransportText(t, err.Error())
		})
	}
}

func TestFederationCallSites_TransportErrorOmitsItsText(t *testing.T) {
	for site, call := range federationCallSites {
		t.Run(site, func(t *testing.T) {
			installErrorDefaultTransport(t, errInjectedTransport)
			err := call(t, newFederationTestBridge(t, "https://"+testHubName))
			if err == nil {
				t.Fatal("call succeeded, want the transport error")
			}
			msg := err.Error()
			for _, s := range []string{"redir-user", "s3cret-pw", "evil.example", "Location:"} {
				if strings.Contains(msg, s) {
					t.Errorf("error %q includes %q", msg, s)
				}
			}
			// callerHubClient errors carry the hub request URL as the shared
			// hub client redacts it; federatedUserGet errors carry no URL.
			if site == "federatedUserGet" && strings.Contains(msg, "://") {
				t.Errorf("error %q quotes a URL", msg)
			}
		})
	}
}

// newMalformedHeaderHub starts an https hub on loopback, named testHubName
// through installTestTransport, that answers every request with a 302 whose
// Location header line holds a DEL byte, which net/http's response header
// reader refuses. It returns the hub endpoint.
func newMalformedHeaderHub(t *testing.T, serverTLS *tls.Config) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	tlsLn := tls.NewListener(ln, serverTLS)
	go func() {
		for {
			c, err := tlsLn.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				_ = c.SetDeadline(time.Now().Add(5 * time.Second))
				br := bufio.NewReader(c)
				for {
					line, err := br.ReadString('\n')
					if err != nil || line == "\r\n" {
						break
					}
				}
				_, _ = io.WriteString(c, "HTTP/1.1 302 Found\r\n"+
					"Location: https://redir-user:s3cret-pw@hub.example.com/\x7f\r\n"+
					"Content-Length: 0\r\nConnection: close\r\n\r\n")
			}(c)
		}
	}()
	_, port, _ := net.SplitHostPort(ln.Addr().String())
	return "https://" + net.JoinHostPort(testHubName, port)
}

func TestHubOriginClients_MalformedHubHeaderOmitted(t *testing.T) {
	clients := allOriginClients()
	for _, name := range []string{"uat/caller Authorization", "oidc/no transport auth", "ge/no transport auth/option"} {
		client, ok := clients[name]
		if !ok {
			t.Fatalf("no client %q", name)
		}
		t.Run(name, func(t *testing.T) {
			endpoint := newMalformedHeaderHub(t, installTestTransport(t))
			err := client.call(t, endpoint)
			if err == nil {
				t.Fatal("call succeeded, want an error")
			}
			msg := err.Error()
			for _, s := range []string{"://", "redir-user", "s3cret-pw", "Location:"} {
				if strings.Contains(msg, s) {
					t.Errorf("error %q includes %q", msg, s)
				}
			}
		})
	}
	for site, call := range federationCallSites {
		t.Run("federation/"+site, func(t *testing.T) {
			endpoint := newMalformedHeaderHub(t, installTestTransport(t))
			err := call(t, newFederationTestBridge(t, endpoint))
			if err == nil {
				t.Fatal("call succeeded, want an error")
			}
			msg := err.Error()
			for _, s := range []string{"redir-user", "s3cret-pw", "Location:"} {
				if strings.Contains(msg, s) {
					t.Errorf("error %q includes %q", msg, s)
				}
			}
			if site == "federatedUserGet" && strings.Contains(msg, "://") {
				t.Errorf("error %q quotes a URL", msg)
			}
		})
	}
}

// trailerHubBody is a JSON body every hub client accepts: the UAT
// validator, the GE exchange validator and the OIDC proxy.
const trailerHubBody = `{"id":"user-1","email":"user@example.com","accessToken":"hub-token","user":{"id":"user-1","email":"user@example.com"}}`

// newMalformedTrailerHub starts an https hub on loopback, named testHubName
// through installTestTransport, that answers every request with a chunked
// 200 whose body is trailerHubBody and whose trailer line holds a DEL byte
// and a URL with user information, which net/http's trailer reader refuses
// when the body is read to its end. It returns the hub endpoint.
func newMalformedTrailerHub(t *testing.T, serverTLS *tls.Config) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	tlsLn := tls.NewListener(ln, serverTLS)
	go func() {
		for {
			c, err := tlsLn.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				_ = c.SetDeadline(time.Now().Add(5 * time.Second))
				br := bufio.NewReader(c)
				for {
					line, err := br.ReadString('\n')
					if err != nil || line == "\r\n" {
						break
					}
				}
				_, _ = io.WriteString(c, "HTTP/1.1 200 OK\r\n"+
					"Content-Type: application/json\r\n"+
					"Transfer-Encoding: chunked\r\n"+
					"Trailer: X-T\r\n"+
					"Connection: close\r\n\r\n"+
					fmt.Sprintf("%x\r\n%s\r\n", len(trailerHubBody), trailerHubBody)+
					"0\r\n"+
					"X-T: https://redir-user:s3cret-pw@evil.example/\x7f\r\n\r\n")
			}(c)
		}
	}()
	_, port, _ := net.SplitHostPort(ln.Addr().String())
	return "https://" + net.JoinHostPort(testHubName, port)
}

// assertTrailerErrorOmitted checks a client error from a malformed trailer
// hub: when there is one, it quotes no URL and no user information, and
// errors.As still reaches the textproto.ProtocolError inside it.
func assertTrailerErrorOmitted(t *testing.T, err error, mustFail bool) {
	t.Helper()
	if err == nil {
		if mustFail {
			t.Fatal("call succeeded, want the trailer read error")
		}
		return
	}
	msg := err.Error()
	for _, s := range []string{"://", "redir-user", "s3cret-pw", "X-T:"} {
		if strings.Contains(msg, s) {
			t.Errorf("error %q includes %q", msg, s)
		}
	}
	var protoErr textproto.ProtocolError
	if !errors.As(err, &protoErr) {
		t.Errorf("error %v: errors.As does not reach a textproto.ProtocolError", err)
	}
}

func TestHubOriginClients_MalformedHubTrailerOmitted(t *testing.T) {
	for clientName, client := range allOriginClients() {
		t.Run(clientName, func(t *testing.T) {
			endpoint := newMalformedTrailerHub(t, installTestTransport(t))
			err := client.call(t, endpoint)
			// The OIDC proxy and the GE validator read the body to its end,
			// so they meet the trailer; the UAT validator's JSON decoder
			// stops at the end of the object.
			mustFail := strings.HasPrefix(clientName, "oidc/") || strings.HasPrefix(clientName, "ge/")
			assertTrailerErrorOmitted(t, err, mustFail)
		})
	}
	for site, call := range federationCallSites {
		t.Run("federation/"+site, func(t *testing.T) {
			endpoint := newMalformedTrailerHub(t, installTestTransport(t))
			err := call(t, newFederationTestBridge(t, endpoint))
			if err != nil && site == "callerHubClient" {
				// callerHubClient errors carry the hub request URL as the
				// shared hub client redacts it; check the trailer text only.
				for _, s := range []string{"redir-user", "s3cret-pw", "X-T:"} {
					if strings.Contains(err.Error(), s) {
						t.Errorf("error %q includes %q", err, s)
					}
				}
				return
			}
			assertTrailerErrorOmitted(t, err, false)
		})
	}
}

// bodyRoundTripper answers every request with 200 and body.
type bodyRoundTripper struct{ body io.ReadCloser }

func (b bodyRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: b.body, Request: req}, nil
}

// readErrorBody returns data once, then err on every Read, and records
// whether it was closed.
type readErrorBody struct {
	data   []byte
	err    error
	closed bool
}

func (r *readErrorBody) Read(p []byte) (int, error) {
	if len(r.data) > 0 {
		n := copy(p, r.data)
		r.data = r.data[n:]
		return n, nil
	}
	return 0, r.err
}

func (r *readErrorBody) Close() error {
	r.closed = true
	return nil
}

func TestHubTransports_ResponseBodyErrors(t *testing.T) {
	for name, build := range hubTransports {
		t.Run(name, func(t *testing.T) {
			read := func(t *testing.T, body *readErrorBody) ([]byte, error, error) {
				t.Helper()
				req, err := http.NewRequest(http.MethodGet, "https://"+testHubName+"/api", nil)
				if err != nil {
					t.Fatal(err)
				}
				resp, err := build(t, bodyRoundTripper{body: body}).RoundTrip(req)
				if err != nil {
					t.Fatalf("RoundTrip: %v", err)
				}
				buf := make([]byte, 64)
				n, _ := resp.Body.Read(buf)
				_, readErr := resp.Body.Read(buf[n:])
				return buf[:n], readErr, resp.Body.Close()
			}
			t.Run("io.EOF identity", func(t *testing.T) {
				body := &readErrorBody{data: []byte("ok"), err: io.EOF}
				data, readErr, closeErr := read(t, body)
				if string(data) != "ok" || readErr != io.EOF {
					t.Errorf("read %q, %v; want \"ok\", io.EOF itself", data, readErr)
				}
				if closeErr != nil || !body.closed {
					t.Errorf("Close = %v, closed %v; want it passed through", closeErr, body.closed)
				}
				resp, err := build(t, bodyRoundTripper{body: &readErrorBody{data: []byte(`{"a":1}`), err: io.EOF}}).
					RoundTrip(mustRequest(t))
				if err != nil {
					t.Fatal(err)
				}
				if all, err := io.ReadAll(resp.Body); err != nil || string(all) != `{"a":1}` {
					t.Errorf("io.ReadAll = %q, %v; want the body and no error", all, err)
				}
			})
			t.Run("other read error", func(t *testing.T) {
				_, readErr, _ := read(t, &readErrorBody{data: []byte("ok"), err: errInjectedTransport})
				if !errors.Is(readErr, errInjectedTransport) {
					t.Fatalf("read error = %v, want one wrapping the body error", readErr)
				}
				assertNoTransportText(t, readErr.Error())
			})
		})
	}
}

func mustRequest(t *testing.T) *http.Request {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, "https://"+testHubName+"/api", nil)
	if err != nil {
		t.Fatal(err)
	}
	return req
}

func TestHubTransports_WrappedSentinelReturnedBare(t *testing.T) {
	for name, build := range hubTransports {
		t.Run(name, func(t *testing.T) {
			for _, sentinel := range []error{errHubOtherOrigin, errFederationOtherOrigin, errHubRedirectLocation} {
				baseErr := fmt.Errorf("proxy https://redir-user:s3cret-pw@evil.example: %w", sentinel)
				_, err := build(t, errorRoundTripper{err: baseErr}).RoundTrip(mustRequest(t))
				if err != sentinel {
					t.Errorf("error = %v, want the sentinel %v itself", err, sentinel)
				}
			}
		})
	}
}

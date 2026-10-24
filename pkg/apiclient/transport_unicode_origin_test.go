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

package apiclient

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/GoogleCloudPlatform/scion/pkg/transportauth"
)

// foldHubName is a hub host name with a "k" and an "i", which U+212A and
// U+0130 fold onto under strings.ToLower.
const foldHubName = "kitchen.test"

// foldHopTargets are redirect targets whose host is not the hub origin but
// folds onto, or is a different spelling of, foldHubName.
var foldHopTargets = map[string]string{
	"percent-encoded U+0130": "http://k%C4%B0tchen.test/landed",
	"raw UTF-8 U+0130":       "http://k\u0130tchen.test/landed",
	"percent-encoded U+212A": "http://%E2%84%AAitchen.test/landed",
	"raw UTF-8 U+212A":       "http://\u212aitchen.test/landed",
	"trailing-dot host":      "http://kitchen.test./landed",
}

// foldHop is one request the fold test server received.
type foldHop struct {
	host, path string
	header     http.Header
}

// foldNet is one httptest server that every dialed address reaches. It
// records each request. A request for /start is
// redirected to loc, with the Location header written as given.
type foldNet struct {
	addr string

	mu   sync.Mutex
	hops []foldHop
}

func newFoldNet(t *testing.T, loc string) *foldNet {
	t.Helper()
	n := &foldNet{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n.mu.Lock()
		n.hops = append(n.hops, foldHop{host: r.Host, path: r.URL.Path, header: r.Header.Clone()})
		n.mu.Unlock()
		if r.URL.Path == "/start" {
			w.Header().Set("Location", loc)
			w.WriteHeader(http.StatusFound)
			return
		}
		_, _ = io.WriteString(w, "ok")
	}))
	t.Cleanup(srv.Close)
	n.addr = srv.Listener.Addr().String()
	return n
}

func (n *foldNet) transport() *http.Transport {
	var d net.Dialer
	return &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			return d.DialContext(ctx, network, n.addr)
		},
	}
}

// landed returns the requests for /landed.
func (n *foldNet) landed() []foldHop {
	n.mu.Lock()
	defer n.mu.Unlock()
	var out []foldHop
	for _, h := range n.hops {
		if h.path == "/landed" {
			out = append(out, h)
		}
	}
	return out
}

// credentialHeaders returns the names of the cross-origin credential headers
// h carries: Authorization, Proxy-Authorization, X-Serverless-Authorization
// and every X-Scion-* header.
func credentialHeaders(h http.Header) []string {
	var names []string
	for name := range h {
		canonical := http.CanonicalHeaderKey(name)
		switch {
		case canonical == "Authorization", canonical == "Proxy-Authorization",
			canonical == "X-Serverless-Authorization", strings.HasPrefix(canonical, "X-Scion-"):
			names = append(names, canonical)
		}
	}
	return names
}

// foldAuths are the client credentials the fold tests send.
var foldAuths = map[string]Authenticator{
	"bearer":      &BearerAuth{Token: "hub-token"},
	"agent_token": &AgentTokenAuth{Token: "agent-token", RunID: "run-1"},
	"hmac":        &HMACAuth{BrokerID: "broker-1", SecretKey: []byte("secret")},
}

// foldTransportModes are the transport auth header modes the fold tests use.
var foldTransportModes = map[string]transportauth.HeaderMode{
	"transport_authorization":            transportauth.HeaderAuthorization,
	"transport_proxy_authorization":      transportauth.HeaderProxyAuthorization,
	"transport_serverless_authorization": transportauth.HeaderServerlessAuthorization,
}

// foldPaths are the two client paths whose redirect hops go through the
// cross-origin header removal: Transport.Get and AuthenticatedHTTPClient.
var foldPaths = map[string]func(t *testing.T, tr *Transport) error{
	"Get": func(t *testing.T, tr *Transport) error {
		resp, err := tr.Get(context.Background(), "/start", http.Header{"X-Scion-Run-Id": {"caller-run"}})
		if err != nil {
			return err
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		return resp.Body.Close()
	},
	"AuthenticatedHTTPClient": func(t *testing.T, tr *Transport) error {
		req := newGet(t, "http://"+foldHubName+"/start")
		req.Header.Set("X-Scion-Run-Id", "caller-run")
		resp, err := tr.AuthenticatedHTTPClient().Do(req)
		if err != nil {
			return err
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		return resp.Body.Close()
	},
}

func newFoldTransport(n *foldNet, auth Authenticator, mode transportauth.HeaderMode) *Transport {
	hc := &http.Client{
		Transport: transportauth.Wrap(n.transport(), stubTokenSource{tok: "transport-token"}, mode),
		Timeout:   10 * time.Second,
	}
	return NewTransport("http://"+foldHubName, WithHTTPClient(hc), WithAuth(auth))
}

func TestRedirectToFoldedHubHostOmitsCredentialHeaders(t *testing.T) {
	for targetName, loc := range foldHopTargets {
		for authName, auth := range foldAuths {
			for modeName, mode := range foldTransportModes {
				for pathName, call := range foldPaths {
					t.Run(targetName+"/"+authName+"/"+modeName+"/"+pathName, func(t *testing.T) {
						n := newFoldNet(t, loc)
						if err := call(t, newFoldTransport(n, auth, mode)); err != nil {
							t.Fatalf("call: %v", err)
						}
						landed := n.landed()
						if len(landed) != 1 {
							t.Fatalf("redirect hop reached the server %d times, want 1", len(landed))
						}
						if got := credentialHeaders(landed[0].header); len(got) != 0 {
							t.Errorf("hop to %s (Host %q) carried credential headers %v, want none", loc, landed[0].host, got)
						}
					})
				}
			}
		}
	}
}

func TestRedirectWithinHubOriginKeepsCredentialHeaders(t *testing.T) {
	for authName, auth := range foldAuths {
		for modeName, mode := range foldTransportModes {
			for pathName, call := range foldPaths {
				t.Run(authName+"/"+modeName+"/"+pathName, func(t *testing.T) {
					n := newFoldNet(t, "http://"+foldHubName+"/landed")
					if err := call(t, newFoldTransport(n, auth, mode)); err != nil {
						t.Fatalf("call: %v", err)
					}
					landed := n.landed()
					if len(landed) != 1 {
						t.Fatalf("redirect hop reached the server %d times, want 1", len(landed))
					}
					if len(credentialHeaders(landed[0].header)) == 0 {
						t.Error("same-origin hop carried no credential headers")
					}
					// BearerAuth and Authorization-mode transport auth share
					// one header; only the other modes are checked here.
					if authName == "bearer" && mode == transportauth.HeaderAuthorization {
						return
					}
					if h := transportHeaderFor(mode); landed[0].header.Get(h) != "Bearer transport-token" {
						t.Errorf("same-origin hop %s = %q, want the transport token", h, landed[0].header.Get(h))
					}
				})
			}
		}
	}
}

// transportHeaderFor names the header transport auth in mode uses.
func transportHeaderFor(mode transportauth.HeaderMode) string {
	switch mode {
	case transportauth.HeaderProxyAuthorization:
		return "Proxy-Authorization"
	case transportauth.HeaderServerlessAuthorization:
		return "X-Serverless-Authorization"
	}
	return "Authorization"
}

func TestSameOrigin_NonASCIIAndNonCanonicalHosts(t *testing.T) {
	for _, tc := range []struct {
		hub, target string
		same        bool
	}{
		{"http://kitchen.test", "http://k%C4%B0tchen.test/x", false},
		{"http://kitchen.test", "http://k\u0130tchen.test/x", false},
		{"http://kitchen.test", "http://%E2%84%AAitchen.test/x", false},
		{"http://kitchen.test", "http://\u212aitchen.test/x", false},
		{"http://kitchen.test", "http://kitchen.test./x", false},
		{"http://kitchen.test", "http://kitchen.test:080/x", false},
		{"http://k\u0130tchen.test", "http://k\u0130tchen.test/x", false},
		{"https://[::1]", "https://[::1]:443/x", true},
		{"https://[::abcd]", "https://[::ABCD]/x", true},
		{"http://kitchen.test", "http://user:pw@kitchen.test/x", true},
	} {
		if got := sameOrigin(newGet(t, tc.target).URL, parseOrigin(tc.hub)); got != tc.same {
			t.Errorf("sameOrigin(%q, origin of %q) = %v, want %v", tc.target, tc.hub, got, tc.same)
		}
	}
}

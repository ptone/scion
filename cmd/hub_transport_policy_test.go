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

package cmd

import (
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/GoogleCloudPlatform/scion/pkg/config"
	"github.com/GoogleCloudPlatform/scion/pkg/credentials"
	"github.com/GoogleCloudPlatform/scion/pkg/transportauth"
	"github.com/GoogleCloudPlatform/scion/pkg/tunnelclient"
	"github.com/GoogleCloudPlatform/scion/pkg/wsclient"
)

// Placeholder credential values; each is distinct, so a header the counting
// hub sees names the credential that was sent.
const (
	htpOAuthToken     = "htp-oauth-access-token"
	htpHubToken       = "htp-hub-token-pat"
	htpAgentToken     = "htp-agent-identity-token"
	htpTransportToken = "htp-transport-id-token"
	htpUserinfoSecret = "htp-userinfo-secret"
	htpInjected       = "http://host.docker.internal:9810"
	htpRefused        = "http://hub.example.com:9810"
)

// htpHub records every request it receives, with its credential headers.
type htpHub struct {
	*httptest.Server
	mu   sync.Mutex
	reqs []http.Header
}

func newHTPHub(t *testing.T) *htpHub {
	t.Helper()
	h := &htpHub{}
	h.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.mu.Lock()
		h.reqs = append(h.reqs, r.Header.Clone())
		h.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}))
	t.Cleanup(h.Close)
	return h
}

func (h *htpHub) requests() []http.Header {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]http.Header(nil), h.reqs...)
}

// credentialHeaders names the credential headers present in h (no values).
func credentialHeaders(h http.Header) []string {
	var names []string
	for _, k := range []string{"Authorization", "Proxy-Authorization", "X-Scion-Agent-Token"} {
		if h.Get(k) != "" {
			names = append(names, k)
		}
	}
	return names
}

func (h *htpHub) assertNoRequests(t *testing.T) {
	t.Helper()
	if reqs := h.requests(); len(reqs) != 0 {
		t.Fatalf("%d request(s) reached the hub (credential headers %v); want none", len(reqs), credentialHeaders(reqs[0]))
	}
}

// htpRouteDials makes http.DefaultTransport connect every dial to the
// counting hub, whatever host the URL names, so a request to a refused URL
// would arrive there (a missing check shows as a recorded request, not as a
// dial error).
func htpRouteDials(t *testing.T, hub *htpHub) {
	t.Helper()
	addr := hub.Listener.Addr().String()
	orig := http.DefaultTransport
	tr := orig.(*http.Transport).Clone()
	tr.Proxy = nil
	tr.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, network, addr)
	}
	http.DefaultTransport = tr
	t.Cleanup(func() { http.DefaultTransport = orig })
}

// htpEnv clears every credential, transport and hub-selection source and
// returns a fresh HOME. With user set, an OAuth login for endpoint, a
// SCION_HUB_TOKEN and a transport (IAP) credential are all present.
func htpEnv(t *testing.T, endpoint string, user bool) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	credPath := filepath.Join(home, "credentials.json")
	origPath := credentials.ExportCredentialsPath()
	credentials.SetCredentialsPath(func() string { return credPath })
	t.Cleanup(func() { credentials.SetCredentialsPath(origPath) })
	for _, name := range []string{
		"SCION_HUB_ENDPOINT", "SCION_HUB_URL", "SCION_AGENT_ID", "SCION_HUB_TOKEN", "SCION_AUTH_TOKEN",
		"SCION_DEV_TOKEN", "SCION_DEV_TOKEN_FILE", "SCION_HUB_PROFILE",
		"SCION_TRANSPORT_TOKEN", "SCION_TRANSPORT_MODE", "SCION_TRANSPORT_AUDIENCE", "SCION_HUB_OIDC_AUDIENCE",
	} {
		t.Setenv(name, "")
	}
	t.Setenv("SCION_TRANSPORT_TOKEN_FILE", filepath.Join(home, "absent-transport-token"))
	savedEndpoint, savedSource := hubEndpoint, hubSource
	hubEndpoint, hubSource = "", ""
	t.Cleanup(func() { hubEndpoint, hubSource = savedEndpoint, savedSource })
	if user {
		if err := credentials.Store(endpoint, &credentials.TokenResponse{AccessToken: htpOAuthToken, ExpiresIn: time.Hour}); err != nil {
			t.Fatal(err)
		}
		t.Setenv("SCION_HUB_TOKEN", htpHubToken)
		t.Setenv("SCION_TRANSPORT_MODE", "iap")
		t.Setenv("SCION_TRANSPORT_TOKEN", htpTransportToken)
	}
	return home
}

func assertTransportRefusal(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("no error; want the transport refusal")
	}
	if !errors.Is(err, config.ErrPlaintextNonLoopback) && !strings.Contains(err.Error(), config.ErrPlaintextNonLoopback.Error()) {
		t.Fatalf("error %v is not the transport refusal", err)
	}
	if strings.Contains(err.Error(), htpUserinfoSecret) {
		t.Fatalf("refusal echoes URL user info: %v", err)
	}
}

var htpRefusedEndpoints = []string{
	htpRefused,
	"http://10.0.0.1:9810",
	"http://[2001:db8::1]:9810",
	htpInjected,
	"http://user:" + htpUserinfoSecret + "@10.0.0.1:9810",
}

// TestUserHubPaths_RefusePlaintextNonLoopbackBeforeAnyRequest runs every CLI
// path that sends (or receives) a user credential against a refused URL,
// with every user credential present, and checks that nothing reached the
// hub and that a hub client was never built.
func TestUserHubPaths_RefusePlaintextNonLoopbackBeforeAnyRequest(t *testing.T) {
	paths := map[string]func(t *testing.T, endpoint string) error{
		"P1 getHubClient (hub.go)": func(t *testing.T, endpoint string) error {
			hubEndpoint, hubSource = endpoint, "" // --hub flag
			c, err := getHubClient(nil)
			if err == nil {
				_, _ = c.Health(context.Background())
			}
			return err
		},
		"P1 hubClientForEndpoint": func(t *testing.T, endpoint string) error {
			c, err := hubClientForEndpoint(nil, endpoint)
			if err == nil {
				_, _ = c.Health(context.Background())
			}
			return err
		},
		"P2 requireHub (hubsync.EnsureHubReady)": func(t *testing.T, endpoint string) error {
			hubEndpoint, hubSource = endpoint, "" // --hub flag
			saved := autoConfirm
			autoConfirm = true
			t.Cleanup(func() { autoConfirm = saved })
			t.Chdir(t.TempDir())
			_, err := requireHubWith("", true)
			return err
		},
		"P3 hub auth login flow": func(t *testing.T, endpoint string) error {
			saved := hubAuthNoBrowser
			hubAuthNoBrowser = true
			t.Cleanup(func() { hubAuthNoBrowser = saved })
			c := &cobra.Command{}
			c.SetContext(context.Background())
			c.SetOut(io.Discard)
			c.SetErr(io.Discard)
			_, err := hubAuthLoginFlow(c, endpoint, "google")
			return err
		},
		"P4 hub health check (hub add/use)": func(t *testing.T, endpoint string) error {
			return hubResponds(endpoint, false)
		},
		"P4 hub health check fan-out (hub list --check)": func(t *testing.T, endpoint string) error {
			return hubResponds(endpoint, true)
		},
		"P4 hub credential verification": func(t *testing.T, endpoint string) error {
			if _, ok := hubAccepts(endpoint, htpHubToken, false); ok {
				return nil
			}
			return config.ErrPlaintextNonLoopback
		},
		"P5 hub fan-out client": func(t *testing.T, endpoint string) error {
			c, err := hubClientFor(nil, endpoint, true)
			if err == nil {
				_, _ = c.Health(context.Background())
			}
			return err
		},
		"P6 completion": func(t *testing.T, endpoint string) error {
			_, err := fetchHubAgents(context.Background(), endpoint, &config.Settings{})
			return err
		},
		"P7 tunnel client config (tunnel, ssh, vscode)": func(t *testing.T, endpoint string) error {
			_, _, err := tunnelClientConfig(endpoint, io.Discard)
			return err
		},
		"P7 tunnel REST client": func(t *testing.T, endpoint string) error {
			h := &tunnelHub{endpoint: endpoint}
			c, err := h.client(tunnelclient.Credential{Token: htpHubToken, Kind: tunnelclient.CredentialToken})
			if err == nil {
				_, _ = c.Health(context.Background())
			}
			return err
		},
		"P8 ssh hub connection": func(t *testing.T, endpoint string) error {
			_, err := newSSHHubConn(endpoint, htpHubToken)
			return err
		},
		"P8 ssh hub resolution": func(t *testing.T, endpoint string) error {
			_, err := resolveSSHHub(&sshCommandEnv{home: t.TempDir(), endpoint: endpoint, token: htpHubToken})
			return err
		},
		"P8 ssh command env reads no credential": func(t *testing.T, endpoint string) error {
			hubEndpoint, hubSource = endpoint, "" // --hub flag
			env, err := defaultSSHCommandEnv()
			if err != nil {
				return err
			}
			if env.token != "" {
				t.Fatal("defaultSSHCommandEnv read a credential for a refused hub")
			}
			return config.ErrPlaintextNonLoopback
		},
		"P9 attach": func(t *testing.T, endpoint string) error {
			origTransport, origAttach := resolveAttachTransportFn, attachToAgentFn
			t.Cleanup(func() { resolveAttachTransportFn, attachToAgentFn = origTransport, origAttach })
			resolveAttachTransportFn = func() (transportauth.TokenSource, transportauth.HeaderMode, error) {
				t.Fatal("attach resolved the transport credential for a refused hub")
				return nil, 0, nil
			}
			attachToAgentFn = func(context.Context, string, string, string, ...wsclient.AttachOption) error {
				t.Fatal("attach dialed a refused hub")
				return nil
			}
			return attachHubSession(context.Background(), &HubContext{Endpoint: endpoint}, hubAttachTarget{Name: "a", ID: "a"})
		},
		"P10 onboarding status": func(t *testing.T, endpoint string) error {
			if quickstartStatusAllowed(endpoint) {
				return nil
			}
			return config.ErrPlaintextNonLoopback
		},
	}
	names := make([]string, 0, len(paths))
	for name := range paths {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		for _, endpoint := range htpRefusedEndpoints {
			t.Run(name+"/"+endpoint, func(t *testing.T) {
				htpEnv(t, endpoint, true)
				hub := newHTPHub(t)
				htpRouteDials(t, hub)
				assertTransportRefusal(t, paths[name](t, endpoint))
				hub.assertNoRequests(t)
			})
		}
	}
}

// TestUserHubPaths_LoopbackAndHTTPSStillWork checks the controls: the same
// clients reach a loopback http hub and an https hub.
func TestUserHubPaths_LoopbackAndHTTPSStillWork(t *testing.T) {
	hub := newHTPHub(t)
	for _, endpoint := range []string{hub.URL, strings.Replace(hub.URL, "127.0.0.1", "localhost", 1)} {
		htpEnv(t, endpoint, false)
		t.Setenv("SCION_HUB_TOKEN", htpHubToken)
		c, err := hubClientForEndpoint(nil, endpoint)
		if err != nil {
			t.Fatalf("hubClientForEndpoint(%s): %v", endpoint, err)
		}
		if _, err := c.Health(context.Background()); err != nil {
			t.Fatalf("Health(%s): %v", endpoint, err)
		}
		if err := hubResponds(endpoint, false); err != nil {
			t.Fatalf("hubResponds(%s): %v", endpoint, err)
		}
		if _, _, err := tunnelClientConfig(endpoint, io.Discard); err != nil {
			t.Fatalf("tunnelClientConfig(%s): %v", endpoint, err)
		}
		if !quickstartStatusAllowed(endpoint) {
			t.Fatalf("quickstartStatusAllowed(%s) = false", endpoint)
		}
	}
	if n := len(hub.requests()); n != 4 {
		t.Fatalf("loopback requests = %d, want 4", n)
	}

	for _, endpoint := range []string{"https://hub.example.com", "https://hub.example.com/scion"} {
		htpEnv(t, endpoint, false)
		if _, err := hubClientForEndpoint(nil, endpoint); err != nil {
			t.Fatalf("hubClientForEndpoint(%s): %v", endpoint, err)
		}
		if _, _, err := tunnelClientConfig(endpoint, io.Discard); err != nil {
			t.Fatalf("tunnelClientConfig(%s): %v", endpoint, err)
		}
		if _, err := newSSHHubConn(endpoint, htpHubToken); err != nil {
			t.Fatalf("newSSHHubConn(%s): %v", endpoint, err)
		}
	}
}

// TestHubTransportRefusal_NamesTheSetting checks that the refusal names
// where the endpoint came from.
func TestHubTransportRefusal_NamesTheSetting(t *testing.T) {
	t.Run("environment", func(t *testing.T) {
		htpEnv(t, htpRefused, false)
		t.Setenv("SCION_HUB_ENDPOINT", htpRefused)
		t.Setenv("SCION_HUB_TOKEN", htpHubToken)
		_, err := getHubClient(nil)
		assertTransportRefusal(t, err)
		if !strings.Contains(err.Error(), "hub endpoint from SCION_HUB_ENDPOINT") {
			t.Errorf("refusal %q does not name SCION_HUB_ENDPOINT", err)
		}
	})
	t.Run("flag", func(t *testing.T) {
		htpEnv(t, htpRefused, false)
		t.Setenv("SCION_HUB_TOKEN", htpHubToken)
		hubEndpoint, hubSource = htpRefused, "" // --hub flag
		_, err := getHubClient(nil)
		assertTransportRefusal(t, err)
		if !strings.Contains(err.Error(), "hub endpoint from --hub flag") {
			t.Errorf("refusal %q does not name the --hub flag", err)
		}
	})
}

// htpAgentEnv sets up a hub-managed agent: SCION_AGENT_ID, the injected
// endpoint and an agent token in the token file, and no user credential.
func htpAgentEnv(t *testing.T) string {
	t.Helper()
	home := htpEnv(t, htpInjected, false)
	t.Setenv("SCION_AGENT_ID", "agent-1")
	t.Setenv("SCION_HUB_ENDPOINT", htpInjected)
	if err := os.MkdirAll(filepath.Join(home, ".scion"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".scion", "scion-token"), []byte(htpAgentToken+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return home
}

// TestInjectedAgentTokenException covers the one exception (sec-review-3's
// three conditions): allowed only for a hub-managed agent AND the
// broker-injected agent-token source AND exactly the injected endpoint;
// a user credential is refused in every mode, including inside an agent.
func TestInjectedAgentTokenException(t *testing.T) {
	t.Run("all three conditions: only the agent token is sent", func(t *testing.T) {
		htpAgentEnv(t)
		t.Setenv("SCION_TRANSPORT_MODE", "iap")
		t.Setenv("SCION_TRANSPORT_TOKEN", htpTransportToken)
		hub := newHTPHub(t)
		htpRouteDials(t, hub)
		for _, run := range []func() error{
			func() error { // P1
				c, err := getHubClient(nil)
				if err != nil {
					return err
				}
				_, err = c.Health(context.Background())
				return err
			},
			func() error { // P2
				c, _, err := hubClientAndAuthSourceForEndpoint(nil, htpInjected)
				if err != nil {
					return err
				}
				_, err = c.Health(context.Background())
				return err
			},
		} {
			if err := run(); err != nil {
				t.Fatalf("agent token to the injected endpoint: %v", err)
			}
		}
		reqs := hub.requests()
		if len(reqs) != 2 {
			t.Fatalf("requests = %d, want 2", len(reqs))
		}
		for _, r := range reqs {
			if names := credentialHeaders(r); len(names) != 1 || r.Get("X-Scion-Agent-Token") != htpAgentToken {
				t.Fatalf("credential headers %v; want only the injected agent token", names)
			}
		}
	})

	negatives := map[string]func(t *testing.T) string{
		"not a hub-managed agent": func(t *testing.T) string {
			t.Setenv("SCION_AGENT_ID", "")
			return htpInjected
		},
		"no broker-injected agent token (SCION_HUB_TOKEN only)": func(t *testing.T) string {
			_ = os.Remove(filepath.Join(os.Getenv("HOME"), ".scion", "scion-token"))
			t.Setenv("SCION_HUB_TOKEN", htpHubToken)
			return htpInjected
		},
		"OAuth login inside the agent": func(t *testing.T) string {
			if err := credentials.Store(htpInjected, &credentials.TokenResponse{AccessToken: htpOAuthToken, ExpiresIn: time.Hour}); err != nil {
				t.Fatal(err)
			}
			return htpInjected
		},
		"transport credential only inside the agent": func(t *testing.T) string {
			_ = os.Remove(filepath.Join(os.Getenv("HOME"), ".scion", "scion-token"))
			t.Setenv("SCION_TRANSPORT_MODE", "iap")
			t.Setenv("SCION_TRANSPORT_TOKEN", htpTransportToken)
			return htpInjected
		},
		"endpoint is not the injected value (other port)": func(t *testing.T) string {
			return "http://host.docker.internal:9811"
		},
		"endpoint is another bridge host": func(t *testing.T) string {
			return "http://host.containers.internal:9810"
		},
		"endpoint is another non-loopback host": func(t *testing.T) string {
			return htpRefused
		},
	}
	for name, setup := range negatives {
		t.Run(name, func(t *testing.T) {
			htpAgentEnv(t)
			endpoint := setup(t)
			hub := newHTPHub(t)
			htpRouteDials(t, hub)
			c, _, err := hubClientAndAuthSourceForEndpoint(nil, endpoint)
			if err == nil {
				_, _ = c.Health(context.Background())
			}
			assertTransportRefusal(t, err)
			hub.assertNoRequests(t)
		})
	}

	t.Run("user-credential paths stay gated inside the agent", func(t *testing.T) {
		htpAgentEnv(t)
		t.Setenv("SCION_HUB_TOKEN", htpHubToken)
		hub := newHTPHub(t)
		htpRouteDials(t, hub)
		_, _, err := tunnelClientConfig(htpInjected, io.Discard)
		assertTransportRefusal(t, err)
		err = attachHubSession(context.Background(), &HubContext{Endpoint: htpInjected}, hubAttachTarget{Name: "a", ID: "a"})
		assertTransportRefusal(t, err)
		_, err = newSSHHubConn(htpInjected, htpHubToken)
		assertTransportRefusal(t, err)
		hub.assertNoRequests(t)
	})
}

// htpAllowedHubClientNew lists the cmd files that may call hubclient.New
// directly: the broker-side clients, which apply config.CheckHubTransport
// themselves (D289). Every other hub client in cmd goes through
// newUserHubClient.
var htpAllowedHubClientNew = map[string]bool{
	"broker.go":      true,
	"broker_join.go": true,
}

// TestHubClientConstructionGoesThroughTransportRule fails when a cmd file
// builds a hub client or raw hub transport outside the transport rule.
func TestHubClientConstructionGoesThroughTransportRule(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	scanned := 0
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, file, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", file, err)
		}
		scanned++
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			pkg, ok := sel.X.(*ast.Ident)
			if !ok {
				return true
			}
			switch {
			case pkg.Name == "hubclient" && sel.Sel.Name == "New" && !htpAllowedHubClientNew[file]:
				t.Errorf("%s: hubclient.New outside the transport rule; use newUserHubClient", fset.Position(call.Pos()))
			case pkg.Name == "apiclient" && sel.Sel.Name == "NewTransport" && file != "ssh_hostkey.go":
				t.Errorf("%s: apiclient.NewTransport outside the transport rule; check checkUserHubTransport first and list the file here", fset.Position(call.Pos()))
			case pkg.Name == "wsclient" && strings.HasPrefix(sel.Sel.Name, "Attach") && sel.Sel.Name != "AttachOption" && file != "attach.go":
				t.Errorf("%s: wsclient.%s outside attach.go (attachHubSession applies the transport rule)", fset.Position(call.Pos()), sel.Sel.Name)
			}
			return true
		})
	}
	if scanned < 50 {
		t.Fatalf("scanned %d cmd files; the scan did not find the package sources", scanned)
	}
}

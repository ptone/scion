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

package substrate

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"k8s.io/client-go/kubernetes"
)

// TargetActorHeader is the header Substrate's inbound atenet-router uses to
// address a specific actor (substrate-runtime.md §1).
const TargetActorHeader = "ate-target-actor"

// ErrInvalidRouterEndpoint matches every router_endpoint refusal from
// ValidateRouterEndpoint.
var ErrInvalidRouterEndpoint = errors.New("substrate: invalid router_endpoint")

// ErrRouterClientCertNotConfigured is returned by NewMTLSRouterClient when
// the client certificate or key file path is empty. The Substrate v1 router
// accepts only mTLS clients, so there is nothing useful to build without
// them.
var ErrRouterClientCertNotConfigured = errors.New("substrate: router client certificate not configured: router mTLS needs both the client certificate file and the client key file")

// ErrRouterRedirectRefused is returned (wrapped in a *url.Error) when the
// router answers a request made by an mTLS RouterClient with a redirect.
// Router calls never need one, and following it could carry the request's
// headers and body to another host or over plaintext.
var ErrRouterRedirectRefused = errors.New("substrate: router answered with a redirect; redirects are refused")

// RouterClient sends HTTP requests to an actor's in-actor control server
// (sciontool substrate-serve) through Substrate's inbound atenet-router.
// An inbound request through the router auto-resumes a SUSPENDED actor —
// callers that must not do that (e.g. a health probe against an actor the
// hub believes is suspended) should check the actor's state via the ateapi
// Control client first rather than call through RouterClient.
type RouterClient struct {
	// Endpoint is the router's base URL, e.g.
	// "https://atenet-router.ate-system.svc:443".
	Endpoint string
	// HTTPClient performs the requests. Required: Do refuses to send
	// without one. It has no blanket timeout: callers set an appropriate
	// deadline on the context passed to Do instead, because a single fixed
	// timeout can't fit every call this client makes — an exec's timeout_s
	// is caller-chosen and can legitimately exceed a short healthz/bootstrap
	// deadline (see pkg/runtime's doExec, which derives its context deadline
	// from timeout_s; a flat 30s client timeout would cut off exec calls
	// whose timeout_s was 60s).
	HTTPClient *http.Client
}

// NewRouterClient builds a PLAINTEXT RouterClient targeting endpoint, with no
// TLS configuration of its own. It exists for tests against httptest
// servers only and must never be used in production code: production
// builds its router client with NewMTLSRouterClient (a test in this package
// fails if any non-test file under pkg/ or cmd/ refers to NewRouterClient).
func NewRouterClient(endpoint string) *RouterClient {
	return &RouterClient{
		Endpoint:   strings.TrimRight(endpoint, "/"),
		HTTPClient: &http.Client{},
	}
}

// RouterTLSConfig configures NewMTLSRouterClient.
type RouterTLSConfig struct {
	// Endpoint is the router's base URL. It must pass
	// ValidateRouterEndpoint (https://<host>:443).
	Endpoint string
	// CertFile and KeyFile are the paths of the broker's client
	// certificate (chain) and private key, in PEM. Both are required. They
	// are re-read on every TLS handshake, so a rotated certificate (a
	// podCertificate projected volume) is used without a restart. They may
	// name the same file when it holds both the key and the chain.
	CertFile string
	KeyFile  string
	// ClusterTrustBundle and CAFile are the CA source that verifies the
	// router's serving certificate, with the same meaning and precedence
	// as in DialerConfig. One is required; the system roots are never used.
	ClusterTrustBundle string
	CAFile             string
	// ServerName is the name the router's serving certificate is verified
	// against. Defaults to the host of Endpoint.
	ServerName string
}

// NewMTLSRouterClient builds the production RouterClient: HTTPS with a
// client certificate, verifying the router against the configured CA
// source. There is no plaintext or unverified mode:
//   - MinVersion is TLS 1.2 and InsecureSkipVerify is never set;
//   - RootCAs come only from cfg's ClusterTrustBundle or CAFile (read
//     through the same lookup as the ateapi dialer), never the system roots;
//   - ServerName is cfg.ServerName, else the Endpoint host;
//   - no proxy from the environment is used for the fixed in-cluster router;
//   - redirects are refused (ErrRouterRedirectRefused), never followed;
//   - the client certificate is re-read from CertFile/KeyFile on every
//     handshake (GetClientCertificate), and loaded once here so a missing or
//     mismatched pair fails construction rather than the first request.
func NewMTLSRouterClient(ctx context.Context, k8sClient kubernetes.Interface, cfg RouterTLSConfig) (*RouterClient, error) {
	return newMTLSRouterClient(ctx, newCTBLookup(k8sClient), cfg)
}

// newMTLSRouterClient is NewMTLSRouterClient over an explicit lookup.
func newMTLSRouterClient(ctx context.Context, lookup ctbLookup, cfg RouterTLSConfig) (*RouterClient, error) {
	if cfg.CertFile == "" || cfg.KeyFile == "" {
		return nil, ErrRouterClientCertNotConfigured
	}
	if err := ValidateRouterEndpoint(cfg.Endpoint); err != nil {
		return nil, err
	}
	u, err := url.Parse(cfg.Endpoint)
	if err != nil {
		// Unreachable after ValidateRouterEndpoint; kept so a future change
		// to the validator cannot leave u nil.
		return nil, fmt.Errorf("%w: it does not parse as a URL", ErrInvalidRouterEndpoint)
	}

	certs := clientCertFiles{certFile: cfg.CertFile, keyFile: cfg.KeyFile}
	if _, err := certs.load(); err != nil {
		return nil, err
	}

	pool, err := rootCAPool(ctx, lookup, cfg.ClusterTrustBundle, cfg.CAFile)
	if err != nil {
		return nil, err
	}

	serverName := cfg.ServerName
	if serverName == "" {
		serverName = u.Hostname()
	}

	tlsCfg := &tls.Config{
		MinVersion: tls.VersionTLS12,
		RootCAs:    pool,
		ServerName: serverName,
		GetClientCertificate: func(*tls.CertificateRequestInfo) (*tls.Certificate, error) {
			return certs.load()
		},
	}

	base, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return nil, errors.New("substrate: http.DefaultTransport is not an *http.Transport")
	}
	transport := base.Clone()
	transport.TLSClientConfig = tlsCfg
	// The router is a fixed in-cluster endpoint: never route it through an
	// HTTP(S)_PROXY from the broker's environment.
	transport.Proxy = nil

	return &RouterClient{
		Endpoint: strings.TrimRight(cfg.Endpoint, "/"),
		HTTPClient: &http.Client{
			Transport:     transport,
			CheckRedirect: refuseRedirect,
		},
	}, nil
}

// refuseRedirect is the mTLS client's CheckRedirect: it refuses every
// redirect with an error rather than returning the 3xx response
// (http.ErrUseLastResponse), so a caller cannot mistake a redirect for an
// answer from the actor.
func refuseRedirect(*http.Request, []*http.Request) error {
	return ErrRouterRedirectRefused
}

// clientCertFiles loads the broker's client certificate from disk.
type clientCertFiles struct {
	certFile string
	keyFile  string
}

// load reads the key pair. The kubelet swaps a projected volume's files
// atomically, but the certificate and the key are two reads, so a read that
// straddles a rotation can see a mismatched pair; one immediate retry covers
// that. A second failure is returned, which aborts the handshake.
func (c clientCertFiles) load() (*tls.Certificate, error) {
	pair, err := tls.LoadX509KeyPair(c.certFile, c.keyFile)
	if err != nil {
		pair, err = tls.LoadX509KeyPair(c.certFile, c.keyFile)
	}
	if err != nil {
		return nil, fmt.Errorf("substrate: load router client certificate: %w", err)
	}
	return &pair, nil
}

// ValidateRouterEndpoint accepts only https://<host>:443, with the port
// written out, no userinfo, and no path (other than "/"), query or
// fragment: the Substrate v1 router serves mTLS on 443 and nothing in
// plaintext. A refusal matches ErrInvalidRouterEndpoint, says which rule
// failed, and does not echo the configured value.
func ValidateRouterEndpoint(endpoint string) error {
	fail := func(reason string) error {
		return fmt.Errorf("%w: %s; use https://<host>:443 (the Substrate v1 router accepts only mTLS on 443)", ErrInvalidRouterEndpoint, reason)
	}
	if endpoint == "" {
		return fail("it is empty")
	}
	if strings.ContainsAny(endpoint, " \t\r\n") {
		return fail("it contains whitespace")
	}
	lower := strings.ToLower(endpoint)
	if !strings.HasPrefix(lower, "https://") {
		if strings.Contains(lower, "://") {
			return fail("the scheme is not https")
		}
		return fail("it has no https:// scheme")
	}
	u, err := url.Parse(endpoint)
	if err != nil {
		return fail("it does not parse as a URL")
	}
	if u.Scheme != "https" || u.Opaque != "" {
		return fail("the scheme is not https")
	}
	if u.User != nil {
		return fail("it carries userinfo")
	}
	if u.Hostname() == "" {
		return fail("the host is empty")
	}
	switch u.Port() {
	case "443":
	case "":
		return fail("the port is not written out")
	default:
		return fail("the port is not 443")
	}
	if (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || strings.Contains(endpoint, "#") {
		return fail("it has a path, query or fragment")
	}
	return nil
}

// Do sends method+path to the actor identified by atespace/actor, through
// the router, with the given body and extra headers (may be nil). The
// caller is responsible for closing the returned response body.
func (c *RouterClient) Do(ctx context.Context, atespace, actor, method, path string, body io.Reader, headers map[string]string) (*http.Response, error) {
	if c.HTTPClient == nil {
		return nil, errors.New("substrate: RouterClient has no HTTP client; build it with NewMTLSRouterClient")
	}
	req, err := http.NewRequestWithContext(ctx, method, c.Endpoint+path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set(TargetActorHeader, atespace+"/"+actor)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	return c.HTTPClient.Do(req)
}

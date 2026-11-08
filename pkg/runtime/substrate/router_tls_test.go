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
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// testRouterHost is the router name the test server's certificate carries,
// the same form as the deployed router_endpoint host.
const testRouterHost = "atenet-router.ate-system.svc"

// testRouterEndpoint is the router_endpoint the tests configure. The client
// dials the httptest server instead (see dialTo), so the endpoint keeps the
// required :443 while the TLS checks run for real.
const testRouterEndpoint = "https://" + testRouterHost + ":443"

// testCA is a CA generated at run time. Keys never leave the test.
type testCA struct {
	cert    *x509.Certificate
	key     *ecdsa.PrivateKey
	certPEM []byte
}

var testSerial atomic.Int64

func newTestCA(t *testing.T, cn string) *testCA {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(testSerial.Add(1)),
		Subject:               pkix.Name{CommonName: cn},
		NotBefore:             time.Now().Add(-time.Minute),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return &testCA{cert: cert, key: key, certPEM: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})}
}

// issue returns a leaf certificate and key in PEM, signed by ca. A server
// leaf carries dnsName; a client leaf carries none.
func (ca *testCA) issue(t *testing.T, cn, dnsName string, client bool) (certPEM, keyPEM []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(testSerial.Add(1)),
		Subject:      pkix.Name{CommonName: cn},
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
	}
	if client {
		tmpl.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}
	} else {
		tmpl.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}
		tmpl.DNSNames = []string{dnsName}
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.cert, &key.PublicKey, ca.key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
}

// writeFileAtomic replaces path's contents in one rename, the way the
// kubelet updates a projected volume.
func writeFileAtomic(t *testing.T, path string, data []byte) {
	t.Helper()
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, path); err != nil {
		t.Fatal(err)
	}
}

// routerFixture is a TLS test server requiring a client certificate from
// clientCA, plus client cert/key/CA files in a temp dir.
type routerFixture struct {
	srv      *httptest.Server
	serverCA *testCA
	clientCA *testCA
	dir      string
	certFile string
	keyFile  string
	caFile   string
	hits     atomic.Int64
	// redirectTo, when set before the first request, makes the handler
	// answer 307 to that URL.
	redirectTo string
}

// newRouterFixture starts the server. serverTLS may adjust the server's TLS
// config before start (for example to cap its version).
func newRouterFixture(t *testing.T, serverTLS func(*tls.Config)) *routerFixture {
	t.Helper()
	f := &routerFixture{
		serverCA: newTestCA(t, "test-router-ca"),
		clientCA: newTestCA(t, "test-podidentity-ca"),
		dir:      t.TempDir(),
	}
	srvCertPEM, srvKeyPEM := f.serverCA.issue(t, testRouterHost, testRouterHost, false)
	srvPair, err := tls.X509KeyPair(srvCertPEM, srvKeyPEM)
	if err != nil {
		t.Fatal(err)
	}
	clientPool := x509.NewCertPool()
	clientPool.AddCert(f.clientCA.cert)

	f.srv = httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.hits.Add(1)
		if f.redirectTo != "" {
			http.Redirect(w, r, f.redirectTo, http.StatusTemporaryRedirect)
			return
		}
		cn := ""
		if r.TLS != nil && len(r.TLS.PeerCertificates) > 0 {
			cn = r.TLS.PeerCertificates[0].Subject.CommonName
		}
		w.Header().Set("X-Client-CN", cn)
		w.Header().Set("X-Target", r.Header.Get(TargetActorHeader))
		w.WriteHeader(http.StatusOK)
	}))
	f.srv.TLS = &tls.Config{
		Certificates: []tls.Certificate{srvPair},
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    clientPool,
	}
	if serverTLS != nil {
		serverTLS(f.srv.TLS)
	}
	f.srv.StartTLS()
	t.Cleanup(f.srv.Close)

	f.certFile = filepath.Join(f.dir, "tls.crt")
	f.keyFile = filepath.Join(f.dir, "tls.key")
	f.caFile = filepath.Join(f.dir, "ca.crt")
	f.writeClientCert(t, "broker-1")
	writeFileAtomic(t, f.caFile, f.serverCA.certPEM)
	return f
}

// writeClientCert issues a client certificate with the given CN and writes
// it and its key to the fixture's files.
func (f *routerFixture) writeClientCert(t *testing.T, cn string) {
	t.Helper()
	certPEM, keyPEM := f.clientCA.issue(t, cn, "", true)
	writeFileAtomic(t, f.keyFile, keyPEM)
	writeFileAtomic(t, f.certFile, certPEM)
}

func (f *routerFixture) config() RouterTLSConfig {
	return RouterTLSConfig{
		Endpoint: testRouterEndpoint,
		CertFile: f.certFile,
		KeyFile:  f.keyFile,
		CAFile:   f.caFile,
	}
}

// build constructs the client with no ClusterTrustBundle lookup available
// (a lookup call fails the test) and points its dialer at the test server.
func (f *routerFixture) build(t *testing.T, cfg RouterTLSConfig) (*RouterClient, error) {
	t.Helper()
	rc, err := newMTLSRouterClient(context.Background(), failingLookup(t), cfg)
	if err != nil {
		return nil, err
	}
	dialTo(t, rc, f.srv.Listener.Addr().String())
	return rc, nil
}

// failingLookup fails the test if the ClusterTrustBundle lookup is used.
func failingLookup(t *testing.T) ctbLookup {
	leg := func(context.Context, string) (string, error) {
		t.Error("unexpected ClusterTrustBundle lookup")
		return "", errors.New("unexpected lookup")
	}
	return ctbLookup{v1: leg, v1beta1: leg}
}

// dialTo makes rc connect to addr whatever host the URL names, leaving the
// TLS ServerName and verification untouched.
func dialTo(t *testing.T, rc *RouterClient, addr string) {
	t.Helper()
	tr := routerTransport(t, rc)
	var d net.Dialer
	tr.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
		return d.DialContext(ctx, network, addr)
	}
}

func routerTransport(t *testing.T, rc *RouterClient) *http.Transport {
	t.Helper()
	tr, ok := rc.HTTPClient.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("router transport is %T, want *http.Transport", rc.HTTPClient.Transport)
	}
	return tr
}

// get sends one request through rc and returns the CN the server saw.
func get(t *testing.T, rc *RouterClient) (string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	resp, err := rc.Do(ctx, "ns-a", "actor-1", http.MethodGet, "/healthz", nil, nil)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if got := resp.Header.Get("X-Target"); got != "ns-a/actor-1" {
		t.Errorf("%s = %q, want ns-a/actor-1", TargetActorHeader, got)
	}
	return resp.Header.Get("X-Client-CN"), nil
}

func TestMTLSRouterClient_PresentsClientCertificate(t *testing.T) {
	f := newRouterFixture(t, nil)
	rc, err := f.build(t, f.config())
	if err != nil {
		t.Fatalf("newMTLSRouterClient() error = %v", err)
	}
	cn, err := get(t, rc)
	if err != nil {
		t.Fatalf("Do() error = %v", err)
	}
	if cn != "broker-1" {
		t.Errorf("server saw client CN %q, want broker-1", cn)
	}
}

func TestMTLSRouterClient_TLSConfigInvariants(t *testing.T) {
	f := newRouterFixture(t, nil)
	rc, err := f.build(t, f.config())
	if err != nil {
		t.Fatal(err)
	}
	cfg := routerTransport(t, rc).TLSClientConfig
	if cfg == nil {
		t.Fatal("TLSClientConfig is nil")
	}
	if cfg.MinVersion < tls.VersionTLS12 {
		t.Errorf("MinVersion = %#x, want >= TLS 1.2", cfg.MinVersion)
	}
	if cfg.InsecureSkipVerify {
		t.Error("InsecureSkipVerify must never be set")
	}
	if cfg.GetClientCertificate == nil {
		t.Error("GetClientCertificate is nil; the client certificate would not be re-read")
	}
	if len(cfg.Certificates) != 0 {
		t.Error("static Certificates set; rotation would not be picked up")
	}
	if cfg.ServerName != testRouterHost {
		t.Errorf("ServerName = %q, want the endpoint host %q", cfg.ServerName, testRouterHost)
	}
	if routerTransport(t, rc).Proxy != nil {
		t.Error("router transport has a Proxy func; router traffic must never use an environment proxy")
	}
	// RootCAs are exactly the configured CA file: no system roots, nothing else.
	want := x509.NewCertPool()
	want.AddCert(f.serverCA.cert)
	if cfg.RootCAs == nil || !cfg.RootCAs.Equal(want) {
		t.Error("RootCAs is not exactly the configured CA")
	}
	if rc.Endpoint != testRouterEndpoint {
		t.Errorf("Endpoint = %q, want %q", rc.Endpoint, testRouterEndpoint)
	}
}

func TestMTLSRouterClient_WrongCARefused(t *testing.T) {
	f := newRouterFixture(t, nil)
	other := newTestCA(t, "unrelated-ca")
	writeFileAtomic(t, f.caFile, other.certPEM)

	rc, err := f.build(t, f.config())
	if err != nil {
		t.Fatal(err)
	}
	_, err = get(t, rc)
	if err == nil {
		t.Fatal("Do() succeeded against a router whose certificate the configured CA did not sign")
	}
	var unknown x509.UnknownAuthorityError
	if !errors.As(err, &unknown) {
		t.Errorf("Do() error = %v, want an unknown-authority error", err)
	}
	if n := f.hits.Load(); n != 0 {
		t.Errorf("handler ran %d time(s), want 0", n)
	}
}

func TestMTLSRouterClient_WrongServerNameRefused(t *testing.T) {
	f := newRouterFixture(t, nil)

	t.Run("explicit ServerName", func(t *testing.T) {
		cfg := f.config()
		cfg.ServerName = "other-router.ate-system.svc"
		rc, err := f.build(t, cfg)
		if err != nil {
			t.Fatal(err)
		}
		_, err = get(t, rc)
		var hostErr x509.HostnameError
		if !errors.As(err, &hostErr) {
			t.Fatalf("Do() error = %v, want a hostname error", err)
		}
	})
	t.Run("endpoint host differs from the certificate", func(t *testing.T) {
		cfg := f.config()
		cfg.Endpoint = "https://other-router.ate-system.svc:443"
		rc, err := f.build(t, cfg)
		if err != nil {
			t.Fatal(err)
		}
		if got := routerTransport(t, rc).TLSClientConfig.ServerName; got != "other-router.ate-system.svc" {
			t.Fatalf("ServerName = %q, want the endpoint host", got)
		}
		_, err = get(t, rc)
		var hostErr x509.HostnameError
		if !errors.As(err, &hostErr) {
			t.Fatalf("Do() error = %v, want a hostname error", err)
		}
	})
	if n := f.hits.Load(); n != 0 {
		t.Errorf("handler ran %d time(s), want 0", n)
	}
}

func TestMTLSRouterClient_ExplicitServerNameUsed(t *testing.T) {
	f := newRouterFixture(t, nil)
	cfg := f.config()
	cfg.Endpoint = "https://router-alias.example:443"
	cfg.ServerName = testRouterHost
	rc, err := f.build(t, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := get(t, rc); err != nil {
		t.Fatalf("Do() error = %v, want the explicit ServerName to verify", err)
	}
}

// TestMTLSRouterClient_RotatedCertificateReRead: after the cert and key files
// are replaced, the next new connection presents the new certificate, with
// the same client (no restart).
func TestMTLSRouterClient_RotatedCertificateReRead(t *testing.T) {
	f := newRouterFixture(t, nil)
	rc, err := f.build(t, f.config())
	if err != nil {
		t.Fatal(err)
	}
	if cn, err := get(t, rc); err != nil || cn != "broker-1" {
		t.Fatalf("before rotation: CN %q, err %v; want broker-1", cn, err)
	}

	f.writeClientCert(t, "broker-1-rotated")
	rc.HTTPClient.CloseIdleConnections()

	cn, err := get(t, rc)
	if err != nil {
		t.Fatalf("after rotation: Do() error = %v", err)
	}
	if cn != "broker-1-rotated" {
		t.Errorf("after rotation the server saw CN %q, want broker-1-rotated", cn)
	}
}

// TestMTLSRouterClient_MismatchedRotatedPairFailsClosed: a rotation that
// leaves a certificate and key that do not belong together fails the
// handshake; the client never falls back to the pair it loaded earlier.
func TestMTLSRouterClient_MismatchedRotatedPairFailsClosed(t *testing.T) {
	// The router only VERIFIES a client certificate if one is given, so a
	// client that regressed to sending none would reach the handler; the
	// refusal must come from the client's own certificate load.
	f := newRouterFixture(t, func(c *tls.Config) { c.ClientAuth = tls.VerifyClientCertIfGiven })
	rc, err := f.build(t, f.config())
	if err != nil {
		t.Fatal(err)
	}
	if cn, err := get(t, rc); err != nil || cn != "broker-1" {
		t.Fatalf("before rotation: CN %q, err %v; want broker-1", cn, err)
	}
	before := f.hits.Load()

	newCertPEM, _ := f.clientCA.issue(t, "broker-1-rotated", "", true)
	writeFileAtomic(t, f.certFile, newCertPEM) // the key file still holds the old key
	rc.HTTPClient.CloseIdleConnections()

	_, err = get(t, rc)
	if err == nil {
		t.Fatal("Do() succeeded with a mismatched certificate and key")
	}
	if !errors.Is(err, errRouterClientCertLoad) {
		t.Errorf("Do() error = %v, want the client-side certificate load error", err)
	}
	if n := f.hits.Load(); n != before {
		t.Errorf("handler ran %d more time(s) after the mismatched rotation, want 0", n-before)
	}
}

// TestMTLSRouterClient_UnreadableRotatedCertificateFailsClosed: if the files
// turn unusable after construction, the next handshake fails rather than
// going out without a client certificate.
func TestMTLSRouterClient_UnreadableRotatedCertificateFailsClosed(t *testing.T) {
	// As above: the router does not require a certificate, so only the
	// client's own load failure can stop the request.
	f := newRouterFixture(t, func(c *tls.Config) { c.ClientAuth = tls.VerifyClientCertIfGiven })
	rc, err := f.build(t, f.config())
	if err != nil {
		t.Fatal(err)
	}
	writeFileAtomic(t, f.keyFile, []byte("not a key"))
	rc.HTTPClient.CloseIdleConnections()
	_, err = get(t, rc)
	if err == nil {
		t.Fatal("Do() succeeded with an unreadable client key")
	}
	if !errors.Is(err, errRouterClientCertLoad) {
		t.Errorf("Do() error = %v, want the client-side certificate load error", err)
	}
	if n := f.hits.Load(); n != 0 {
		t.Errorf("handler ran %d time(s), want 0", n)
	}
}

func TestMTLSRouterClient_BelowTLS12Refused(t *testing.T) {
	f := newRouterFixture(t, func(c *tls.Config) {
		c.MinVersion = tls.VersionTLS10
		c.MaxVersion = tls.VersionTLS11
	})
	rc, err := f.build(t, f.config())
	if err != nil {
		t.Fatal(err)
	}
	_, err = get(t, rc)
	if err == nil {
		t.Fatal("Do() succeeded against a router capped at TLS 1.1")
	}
	// The failure must be the protocol-version refusal, not some other error.
	// (Go clients also default to TLS 1.2, so MinVersion itself is pinned by
	// TestMTLSRouterClient_TLSConfigInvariants.)
	if !strings.Contains(err.Error(), "protocol version") {
		t.Errorf("Do() error = %v, want a TLS protocol version failure", err)
	}
	if n := f.hits.Load(); n != 0 {
		t.Errorf("handler ran %d time(s), want 0", n)
	}
}

func TestMTLSRouterClient_ServerWithoutClientAuthStillVerified(t *testing.T) {
	// A server that does not ask for a client certificate must still be
	// verified: a wrong CA is refused even then.
	f := newRouterFixture(t, func(c *tls.Config) { c.ClientAuth = tls.NoClientCert })
	writeFileAtomic(t, f.caFile, newTestCA(t, "unrelated-ca").certPEM)
	rc, err := f.build(t, f.config())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := get(t, rc); err == nil {
		t.Fatal("Do() succeeded against an unverified router")
	}
}

func TestMTLSRouterClient_ClusterTrustBundleSource(t *testing.T) {
	f := newRouterFixture(t, nil)
	cfg := f.config()
	cfg.CAFile = "/nonexistent/ca.pem" // the bundle takes precedence
	cfg.ClusterTrustBundle = testCTBName
	lookup := ctbLookup{
		v1: func(_ context.Context, name string) (string, error) {
			if name != testCTBName {
				t.Errorf("lookup name = %q, want %q", name, testCTBName)
			}
			return string(f.serverCA.certPEM), nil
		},
		v1beta1: func(context.Context, string) (string, error) {
			t.Error("v1beta1 reached after a v1 answer")
			return "", errors.New("unexpected")
		},
	}
	rc, err := newMTLSRouterClient(context.Background(), lookup, cfg)
	if err != nil {
		t.Fatalf("newMTLSRouterClient() error = %v", err)
	}
	dialTo(t, rc, f.srv.Listener.Addr().String())
	if cn, err := get(t, rc); err != nil || cn != "broker-1" {
		t.Fatalf("Do() = (%q, %v), want broker-1", cn, err)
	}
}

func TestMTLSRouterClient_ConstructionRefusals(t *testing.T) {
	f := newRouterFixture(t, nil)
	emptyCA := filepath.Join(f.dir, "empty-ca.crt")
	writeFileAtomic(t, emptyCA, []byte("no certificates here\n"))
	otherCertPEM, _ := f.clientCA.issue(t, "other", "", true)
	otherCert := filepath.Join(f.dir, "other.crt")
	writeFileAtomic(t, otherCert, otherCertPEM)

	tests := []struct {
		name    string
		mutate  func(*RouterTLSConfig)
		wantErr error // nil: any error
	}{
		{"no cert and no key", func(c *RouterTLSConfig) { c.CertFile, c.KeyFile = "", "" }, ErrRouterClientCertNotConfigured},
		{"no cert", func(c *RouterTLSConfig) { c.CertFile = "" }, ErrRouterClientCertNotConfigured},
		{"no key", func(c *RouterTLSConfig) { c.KeyFile = "" }, ErrRouterClientCertNotConfigured},
		{"plaintext endpoint", func(c *RouterTLSConfig) { c.Endpoint = "http://" + testRouterHost + ":80" }, ErrInvalidRouterEndpoint},
		{"non-443 endpoint", func(c *RouterTLSConfig) { c.Endpoint = "https://" + testRouterHost + ":8443" }, ErrInvalidRouterEndpoint},
		{"no CA source", func(c *RouterTLSConfig) { c.CAFile, c.ClusterTrustBundle = "", "" }, nil},
		{"CA file with no certificate", func(c *RouterTLSConfig) { c.CAFile = emptyCA }, nil},
		{"missing cert file", func(c *RouterTLSConfig) { c.CertFile = filepath.Join(f.dir, "absent.crt") }, nil},
		{"cert and key do not match", func(c *RouterTLSConfig) { c.CertFile = otherCert }, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := f.config()
			tt.mutate(&cfg)
			rc, err := newMTLSRouterClient(context.Background(), failingLookup(t), cfg)
			if err == nil || rc != nil {
				t.Fatalf("newMTLSRouterClient() = (%v, %v), want a refusal", rc, err)
			}
			if tt.wantErr != nil && !errors.Is(err, tt.wantErr) {
				t.Errorf("error = %v, want it to match %v", err, tt.wantErr)
			}
		})
	}
}

// TestMTLSRouterClient_RedirectRefused: a 307 from the router to a plaintext
// URL is refused; no second request is made anywhere.
func TestMTLSRouterClient_RedirectRefused(t *testing.T) {
	var plainHits atomic.Int64
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		plainHits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(plain.Close)

	f := newRouterFixture(t, nil)
	f.redirectTo = plain.URL + "/scion/v1/exec"
	rc, err := f.build(t, f.config())
	if err != nil {
		t.Fatal(err)
	}
	if rc.HTTPClient.CheckRedirect == nil {
		t.Fatal("router client has no CheckRedirect")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	resp, err := rc.Do(ctx, "ns-a", "actor-1", http.MethodPost, "/scion/v1/exec", strings.NewReader("{}"), nil)
	if err == nil {
		_ = resp.Body.Close()
		t.Fatalf("Do() = status %d, want the redirect refused", resp.StatusCode)
	}
	if !errors.Is(err, ErrRouterRedirectRefused) {
		t.Errorf("Do() error = %v, want ErrRouterRedirectRefused", err)
	}
	if n := f.hits.Load(); n != 1 {
		t.Errorf("router handler ran %d time(s), want exactly 1", n)
	}
	if n := plainHits.Load(); n != 0 {
		t.Errorf("plaintext listener got %d request(s), want 0", n)
	}
}

// plaintextListener is a plain-http httptest server that counts requests.
func plaintextListener(t *testing.T) (*httptest.Server, *atomic.Int64) {
	t.Helper()
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

// TestRouterClientDo_RefusesPlaintextLiteral: a literal RouterClient (not
// built by NewRouterClient) never sends plaintext; the refusal does not
// echo the endpoint.
func TestRouterClientDo_RefusesPlaintextLiteral(t *testing.T) {
	plain, hits := plaintextListener(t)
	for name, rc := range map[string]*RouterClient{
		"literal":    {Endpoint: plain.URL, HTTPClient: &http.Client{}},
		"zero value": {HTTPClient: &http.Client{}},
	} {
		t.Run(name, func(t *testing.T) {
			rc := rc
			if rc.Endpoint == "" {
				rc.Endpoint = plain.URL
			}
			resp, err := rc.Do(context.Background(), "ns", "a", http.MethodGet, "/healthz", nil, nil)
			if err == nil {
				_ = resp.Body.Close()
				t.Fatal("Do() sent a plaintext request from a literal RouterClient")
			}
			if !errors.Is(err, ErrRouterPlaintextRefused) {
				t.Errorf("Do() error = %v, want ErrRouterPlaintextRefused", err)
			}
			u, _ := url.Parse(plain.URL)
			if strings.Contains(err.Error(), u.Host) {
				t.Errorf("error %q echoes the endpoint", err)
			}
		})
	}
	if n := hits.Load(); n != 0 {
		t.Errorf("plaintext listener got %d request(s), want 0", n)
	}
}

// TestRouterClientDo_RefusesEndpointMutatedToPlaintext: an mTLS client whose
// Endpoint is changed to http after construction is refused.
func TestRouterClientDo_RefusesEndpointMutatedToPlaintext(t *testing.T) {
	plain, hits := plaintextListener(t)
	f := newRouterFixture(t, nil)
	rc, err := f.build(t, f.config())
	if err != nil {
		t.Fatal(err)
	}
	rc.Endpoint = plain.URL
	resp, err := rc.Do(context.Background(), "ns", "a", http.MethodGet, "/healthz", nil, nil)
	if err == nil {
		_ = resp.Body.Close()
		t.Fatal("Do() sent plaintext from an mTLS client with a mutated endpoint")
	}
	if !errors.Is(err, ErrRouterPlaintextRefused) {
		t.Errorf("Do() error = %v, want ErrRouterPlaintextRefused", err)
	}
	if n := hits.Load(); n != 0 {
		t.Errorf("plaintext listener got %d request(s), want 0", n)
	}
}

// TestNewRouterClient_PlaintextFixtureStillWorks: the test-only constructor
// keeps serving httptest plaintext fixtures.
func TestNewRouterClient_PlaintextFixtureStillWorks(t *testing.T) {
	plain, hits := plaintextListener(t)
	rc := NewRouterClient(plain.URL)
	resp, err := rc.Do(context.Background(), "ns", "a", http.MethodGet, "/healthz", nil, nil)
	if err != nil {
		t.Fatalf("Do() error = %v, want the plaintext fixture to work", err)
	}
	_ = resp.Body.Close()
	if n := hits.Load(); n != 1 {
		t.Errorf("plaintext listener got %d request(s), want 1", n)
	}
}

func TestRouterClientDo_RefusesWithoutHTTPClient(t *testing.T) {
	rc := &RouterClient{Endpoint: testRouterEndpoint}
	if _, err := rc.Do(context.Background(), "ns", "a", http.MethodGet, "/", nil, nil); err == nil {
		t.Fatal("Do() succeeded with no HTTP client, want a refusal")
	}
}

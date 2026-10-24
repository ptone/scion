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

package config

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"regexp"
	"sort"
	"strings"
	gosync "sync"
	"syscall"
	"time"

	"github.com/rclone/rclone/fs/fspath"
)

// Remote template sources are fetched by the CLI, by the hub (import,
// discover and reimport endpoints) and by runtime brokers (template
// resolution). All of them go through FetchRemoteTemplate, which calls
// ValidateRemoteFetchSource before touching the cache or the network, so the
// rules below apply to every caller.

// rcloneBackendRule describes an rclone backend accepted as a remote template
// source.
//
// The backend's configuration is built in code (see rcloneFetchConfig): no
// option can be set from the source string, from the environment or from an
// rclone config file. If an entry ever needs an option that selects a host
// or endpoint, that option must be validated at dial time against
// non-global addresses (see checkPublicDialAddress) before it is accepted.
type rcloneBackendRule struct {
	// validatePath checks the path part of ":backend:path".
	validatePath func(path string) error
	// anonymousOptions are applied on top of the backend defaults in
	// RemoteFetchAnonymous mode so that no process-held credential is used.
	anonymousOptions map[string]string
}

// allowedRcloneBackends is the explicit allowlist of rclone backends, keyed
// by the exact backend prefix accepted in ":backend:path".
var allowedRcloneBackends = map[string]rcloneBackendRule{
	"gcs": {
		validatePath: validateGCSPath,
		// Unauthenticated client: only public buckets are readable.
		anonymousOptions: map[string]string{"anonymous": "true"},
	},
}

// RcloneRemote is an accepted rclone template source.
type RcloneRemote struct {
	Backend string // allowlisted backend prefix, e.g. "gcs"
	Path    string // backend path, e.g. "bucket/templates/x"
}

// rcloneBackendTokenRe bounds which backend names are echoed in errors.
var rcloneBackendTokenRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,32}$`)

// gcsBucketRe matches GCS bucket names (lowercase letters, digits, '-', '_'
// and '.', starting and ending with a letter or digit).
var gcsBucketRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{1,220}[a-z0-9]$`)

func allowedRcloneBackendNames() string {
	names := make([]string, 0, len(allowedRcloneBackends))
	for name := range allowedRcloneBackends {
		names = append(names, name)
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

// ParseRcloneRemote parses an rclone template source of the form
// ":backend:path" and accepts it only if backend is allowlisted, the string
// carries no connection-string parameters and the path is valid for the
// backend. Error messages never repeat the source string.
func ParseRcloneRemote(uri string) (RcloneRemote, error) {
	if !strings.HasPrefix(uri, ":") {
		return RcloneRemote{}, errors.New("rclone source must have the form :backend:path")
	}
	rest := uri[1:]
	end := strings.IndexAny(rest, ":,")
	if end < 0 {
		return RcloneRemote{}, errors.New("rclone source must have the form :backend:path")
	}
	backend := rest[:end]
	if rest[end] == ',' {
		return RcloneRemote{}, errors.New("rclone source must not carry connection-string parameters (:backend,key=value:); use :backend:path")
	}
	rule, ok := allowedRcloneBackends[backend]
	if !ok {
		if rcloneBackendTokenRe.MatchString(backend) {
			return RcloneRemote{}, fmt.Errorf("rclone backend %q is not accepted for remote templates (accepted: %s)", backend, allowedRcloneBackendNames())
		}
		return RcloneRemote{}, fmt.Errorf("rclone backend is not accepted for remote templates (accepted: %s)", allowedRcloneBackendNames())
	}
	path := rest[end+1:]
	if err := rule.validatePath(path); err != nil {
		return RcloneRemote{}, err
	}

	// Cross-check with rclone's own parser: both must agree on the backend,
	// the absence of parameters and the path.
	parsed, err := fspath.Parse(uri)
	if err != nil || parsed.Name != ":"+backend || len(parsed.Config) != 0 || parsed.Path != path {
		return RcloneRemote{}, errors.New("rclone source must have the form :backend:path")
	}
	return RcloneRemote{Backend: backend, Path: path}, nil
}

// validateGCSPath accepts "bucket" or "bucket/prefix/..." with an optional
// single trailing slash.
func validateGCSPath(path string) error {
	p := strings.TrimSuffix(path, "/")
	if p == "" {
		return errors.New("gcs source must name a bucket (:gcs:bucket/path)")
	}
	for _, r := range p {
		if r < 0x20 || r == 0x7f || r == '\\' || r == ':' {
			return errors.New("gcs source path contains a character that is not accepted")
		}
	}
	segments := strings.Split(p, "/")
	if !gcsBucketRe.MatchString(segments[0]) {
		return errors.New("gcs source must start with a valid bucket name (:gcs:bucket/path)")
	}
	for _, seg := range segments[1:] {
		if seg == "" || seg == "." || seg == ".." {
			return errors.New("gcs source path must not contain empty, '.' or '..' segments")
		}
	}
	return nil
}

// ValidateRemoteFetchSource checks that uri is a remote template source that
// may be fetched:
//   - rclone sources: see ParseRcloneRemote;
//   - GitHub folders: https, host exactly github.com, no port, no userinfo;
//   - archives: https, no userinfo, and a host that is not a non-global IP
//     literal or localhost. The resolved address of every connection is also
//     checked when the archive is downloaded (see checkPublicDialAddress).
//
// Error messages never repeat the source string.
func ValidateRemoteFetchSource(uri string) error {
	if !IsRemoteURI(uri) {
		return errors.New("not a remote template source")
	}
	switch DetectRemoteType(uri) {
	case RemoteTypeRclone:
		_, err := ParseRcloneRemote(uri)
		return err
	case RemoteTypeGitHub:
		u, err := url.Parse(uri)
		if err != nil {
			return errors.New("invalid GitHub source URL")
		}
		if u.Scheme != "https" {
			return errors.New("GitHub source must use https")
		}
		if u.User != nil {
			return errors.New("GitHub source URL must not contain credentials")
		}
		if u.Host != "github.com" {
			return errors.New("GitHub source host must be github.com")
		}
		_, err = parseGitHubURL(uri)
		return err
	case RemoteTypeArchive:
		u, err := url.Parse(uri)
		if err != nil {
			return errors.New("invalid archive source URL")
		}
		if u.User != nil {
			return errors.New("archive source URL must not contain credentials")
		}
		if _, ok := archiveTestOrigins.Load(u.Scheme + "://" + u.Host); ok {
			return nil
		}
		if u.Scheme != "https" {
			return errors.New("archive source must use https")
		}
		return validatePublicHost(u.Hostname())
	default:
		return errors.New("unsupported remote template source (expected a GitHub folder URL, an https archive URL or :gcs:bucket/path)")
	}
}

// validatePublicHost refuses empty hosts, localhost names and IP literals
// outside the global unicast range.
func validatePublicHost(host string) error {
	h := strings.TrimSuffix(strings.ToLower(host), ".")
	if h == "" {
		return errors.New("source URL must name a host")
	}
	if h == "localhost" || strings.HasSuffix(h, ".localhost") {
		return errors.New("source host must be a public address")
	}
	if ip, err := netip.ParseAddr(h); err == nil {
		if !isPublicAddr(ip) {
			return errors.New("source host must be a public address")
		}
	}
	return nil
}

// nonGlobalPrefixes lists ranges that net/netip has no predicate for.
var nonGlobalPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),          // "this network"
	netip.MustParsePrefix("100.64.0.0/10"),      // shared address space (CGNAT)
	netip.MustParsePrefix("192.0.0.0/24"),       // IETF protocol assignments
	netip.MustParsePrefix("192.0.2.0/24"),       // documentation
	netip.MustParsePrefix("198.18.0.0/15"),      // benchmarking
	netip.MustParsePrefix("198.51.100.0/24"),    // documentation
	netip.MustParsePrefix("203.0.113.0/24"),     // documentation
	netip.MustParsePrefix("240.0.0.0/4"),        // reserved, incl. broadcast
	netip.MustParsePrefix("64:ff9b:1::/48"),     // local-use NAT64
	netip.MustParsePrefix("100::/64"),           // discard-only
	netip.MustParsePrefix("2001::/23"),          // IETF protocol assignments
	netip.MustParsePrefix("2001:db8::/32"),      // documentation
	netip.MustParsePrefix("2002::/16"),          // 6to4 (embeds IPv4)
	netip.MustParsePrefix("fec0::/10"),          // site-local (deprecated)
	netip.MustParsePrefix("::ffff:0:0:0/96"),    // IPv4-translated
	netip.MustParsePrefix("64:ff9b::/96"),       // NAT64 (embeds IPv4)
	netip.MustParsePrefix("::/128"),             // unspecified
	netip.MustParsePrefix("169.254.169.254/32"), // cloud metadata (also link-local)
}

// isPublicAddr reports whether ip is a global unicast address outside every
// private, loopback, link-local, multicast and special-purpose range.
func isPublicAddr(ip netip.Addr) bool {
	ip = ip.Unmap().WithZone("")
	if !ip.IsValid() || !ip.IsGlobalUnicast() || ip.IsPrivate() ||
		ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsInterfaceLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified() {
		return false
	}
	for _, p := range nonGlobalPrefixes {
		if p.Contains(ip) {
			return false
		}
	}
	return true
}

// errNonPublicAddress is returned when a connection would reach a
// non-global address.
var errNonPublicAddress = errors.New("connection to a non-public address is not allowed for remote template fetch")

// checkPublicDialAddress checks a resolved "ip:port" dial address.
func checkPublicDialAddress(address string) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return errNonPublicAddress
	}
	ip, err := netip.ParseAddr(host)
	if err != nil || !isPublicAddr(ip) {
		return errNonPublicAddress
	}
	return nil
}

// lookupNetIPFunc resolves a host name to addresses.
type lookupNetIPFunc func(ctx context.Context, host string) ([]netip.Addr, error)

// dialFunc opens a connection to a resolved "ip:port" address.
type dialFunc func(ctx context.Context, network, address string) (net.Conn, error)

// Seams for tests: name resolution and the final dial of archive downloads.
var (
	archiveLookup lookupNetIPFunc = func(ctx context.Context, host string) ([]netip.Addr, error) {
		return net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	}
	archiveDial dialFunc = (&net.Dialer{
		Timeout:   30 * time.Second,
		KeepAlive: 30 * time.Second,
		// Re-checks the address actually connected to.
		Control: func(_, address string, _ syscall.RawConn) error {
			return checkPublicDialAddress(address)
		},
	}).DialContext
	archiveProxyDial dialFunc = (&net.Dialer{
		Timeout:   30 * time.Second,
		KeepAlive: 30 * time.Second,
	}).DialContext
)

// resolvePublic resolves host and returns its addresses only if every one
// of them is public.
func resolvePublic(ctx context.Context, host string) ([]netip.Addr, error) {
	if err := validatePublicHost(host); err != nil {
		return nil, err
	}
	addrs, err := archiveLookup(ctx, host)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve source host: %w", err)
	}
	if len(addrs) == 0 {
		return nil, errors.New("source host has no addresses")
	}
	for _, a := range addrs {
		if !isPublicAddr(a) {
			return nil, errNonPublicAddress
		}
	}
	return addrs, nil
}

// newArchiveHTTPClient returns the HTTP client for archive downloads. Every
// connection, including those made for redirects, is checked at dial time:
// the host is resolved, every resolved address must be public, and the
// connection goes to a checked address (so a DNS answer that changes after
// the check is never used). Redirects must stay on https.
//
// When the environment configures an HTTP proxy, connections to the proxy
// itself are not checked (the proxy is set by whoever runs the process); the
// target host is still resolved and checked before the request is handed to
// the proxy.
func newArchiveHTTPClient() *http.Client {
	t := http.DefaultTransport.(*http.Transport).Clone()
	envProxy := t.Proxy
	var proxyAddrs gosync.Map
	t.Proxy = func(r *http.Request) (*url.URL, error) {
		if envProxy == nil {
			return nil, nil
		}
		p, err := envProxy(r)
		if err != nil || p == nil {
			return p, err
		}
		if _, err := resolvePublic(r.Context(), r.URL.Hostname()); err != nil {
			return nil, err
		}
		proxyAddrs.Store(canonicalProxyAddr(p), struct{}{})
		return p, nil
	}
	t.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		if _, ok := proxyAddrs.Load(address); ok {
			return archiveProxyDial(ctx, network, address)
		}
		if _, ok := archiveTestAddrs.Load(address); ok {
			return archiveProxyDial(ctx, network, address)
		}
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, err
		}
		addrs, err := resolvePublic(ctx, host)
		if err != nil {
			return nil, err
		}
		var lastErr error
		for _, a := range addrs {
			conn, err := archiveDial(ctx, network, net.JoinHostPort(a.String(), port))
			if err == nil {
				return conn, nil
			}
			lastErr = err
		}
		return nil, lastErr
	}
	return &http.Client{Transport: t, CheckRedirect: archiveCheckRedirect}
}

// canonicalProxyAddr returns "host:port" for a proxy URL, with the scheme's
// default port, in the form http.Transport dials it.
func canonicalProxyAddr(p *url.URL) string {
	port := p.Port()
	if port == "" {
		switch p.Scheme {
		case "https":
			port = "443"
		case "socks5", "socks5h":
			port = "1080"
		default:
			port = "80"
		}
	}
	// http.Transport dials the IDNA (lowercase ASCII) form of the host.
	return net.JoinHostPort(strings.ToLower(p.Hostname()), port)
}

// maxFetchRedirects bounds redirects for remote template downloads.
const maxFetchRedirects = 10

// archiveCheckRedirect accepts a redirect only to https without userinfo.
func archiveCheckRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= maxFetchRedirects {
		return errors.New("too many redirects")
	}
	if req.URL.Scheme != "https" {
		return errors.New("redirect to a non-https URL is not allowed")
	}
	if req.URL.User != nil {
		return errors.New("redirect URL must not contain credentials")
	}
	return nil
}

// githubTarballHosts are the hosts a GitHub tarball download may reach,
// including GitHub's redirect to its archive host.
var githubTarballHosts = map[string]bool{
	"github.com":          true,
	"codeload.github.com": true,
}

// githubCheckRedirect accepts a redirect only to https on a GitHub tarball
// host, without userinfo.
func githubCheckRedirect(req *http.Request, via []*http.Request) error {
	if err := archiveCheckRedirect(req, via); err != nil {
		return err
	}
	if req.URL.Port() != "" || !githubTarballHosts[strings.ToLower(req.URL.Hostname())] {
		return errors.New("redirect to a host other than GitHub is not allowed")
	}
	return nil
}

// Archive servers registered by tests (see AllowArchiveServerForTest). Both
// maps are empty outside tests.
var (
	archiveTestOrigins gosync.Map // "scheme://host:port"
	archiveTestAddrs   gosync.Map // "host:port"
)

// AllowArchiveServerForTest lets archive sources served by the test server
// at rawURL (for example an httptest.Server URL on a loopback address, over
// http) pass ValidateRemoteFetchSource and the dial-time address check. Only
// that exact scheme, host and port are let through. It returns a restore
// func.
//
// TEST-ONLY: never call this from production code.
func AllowArchiveServerForTest(rawURL string) (restore func()) {
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" {
		panic("AllowArchiveServerForTest: invalid server URL")
	}
	origin := u.Scheme + "://" + u.Host
	addr := u.Host
	archiveTestOrigins.Store(origin, struct{}{})
	archiveTestAddrs.Store(addr, struct{}{})
	return func() {
		archiveTestOrigins.Delete(origin)
		archiveTestAddrs.Delete(addr)
	}
}

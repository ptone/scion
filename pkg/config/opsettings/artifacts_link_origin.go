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

package opsettings

import (
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"

	"golang.org/x/net/publicsuffix"

	"github.com/GoogleCloudPlatform/scion/pkg/config"
)

// ArtifactsLinkOriginKey names the artifacts.link_origin setting in
// validation errors, audit records and logs.
const ArtifactsLinkOriginKey = "artifacts.link_origin"

// ValidateLinkOrigin checks an artifacts.link_origin value and returns it
// normalized, or "" for an empty value (no secondary origin).
//
// A valid value is an origin, scheme://host[:port]: scheme https, or http
// only for a loopback host; a host name of letters, digits, '.' and '-', or
// an IPv4 address; no userinfo, path, query or fragment (a lone trailing
// "/" is accepted and dropped). The result has a lower-cased scheme and
// host and omits the scheme's default port, so two spellings of one origin
// normalize to the same string. IPv6 literals are refused: the view CSP
// names the origin as a host-source, which cannot carry one.
//
// This is the value's own shape only. Whether it equals or shares a
// registrable domain with the hub's public origin is LinkOriginHubConflict,
// which needs the hub's endpoint.
func ValidateLinkOrigin(raw string) (string, error) {
	if raw == "" {
		return "", nil
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("%s: invalid URL", ArtifactsLinkOriginKey)
	}
	if u.Opaque != "" || u.Host == "" {
		return "", fmt.Errorf("%s: must be an origin of the form https://host[:port]", ArtifactsLinkOriginKey)
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return "", fmt.Errorf("%s: must be an https origin", ArtifactsLinkOriginKey)
	}
	if u.User != nil || strings.Contains(u.Host, "@") {
		return "", fmt.Errorf("%s: must not contain user credentials", ArtifactsLinkOriginKey)
	}
	if u.RawQuery != "" || u.ForceQuery {
		return "", fmt.Errorf("%s: must not contain a query string", ArtifactsLinkOriginKey)
	}
	if u.Fragment != "" || strings.Contains(raw, "#") {
		return "", fmt.Errorf("%s: must not contain a fragment", ArtifactsLinkOriginKey)
	}
	if (u.Path != "" && u.Path != "/") || (u.RawPath != "" && u.RawPath != "/") {
		return "", fmt.Errorf("%s: must not contain a path", ArtifactsLinkOriginKey)
	}
	host := strings.ToLower(u.Hostname())
	if host == "" {
		return "", fmt.Errorf("%s: must include a host", ArtifactsLinkOriginKey)
	}
	if strings.Contains(host, ":") || strings.HasPrefix(u.Host, "[") {
		return "", fmt.Errorf("%s: an IPv6 address is not supported; use a host name", ArtifactsLinkOriginKey)
	}
	if !validLinkOriginHost(host) {
		return "", fmt.Errorf("%s: host must be a host name of letters, digits, '.' and '-', or an IPv4 address", ArtifactsLinkOriginKey)
	}
	if scheme == "http" && !config.IsLoopbackHost(host) {
		return "", fmt.Errorf("%s: must use https; http is accepted only for localhost or a loopback address", ArtifactsLinkOriginKey)
	}
	authority := host
	if portStr := u.Port(); portStr != "" || strings.HasSuffix(u.Host, ":") {
		port, err := strconv.Atoi(portStr)
		if err != nil || port < 1 || port > 65535 || portStr != strconv.Itoa(port) {
			return "", fmt.Errorf("%s: port must be a number between 1 and 65535", ArtifactsLinkOriginKey)
		}
		if port != defaultPort(scheme) {
			authority = host + ":" + portStr
		}
	}
	return scheme + "://" + authority, nil
}

// validLinkOriginHost accepts a host name made of letters, digits, '.' and
// '-' (the characters the view CSP accepts in a host-source), with no empty
// label and no label starting or ending with '-'.
func validLinkOriginHost(host string) bool {
	if len(host) > 253 {
		return false
	}
	for _, label := range strings.Split(host, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for i := 0; i < len(label); i++ {
			c := label[i]
			if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' {
				return false
			}
		}
	}
	return true
}

func defaultPort(scheme string) int {
	if scheme == "http" {
		return 80
	}
	return 443
}

// LinkOriginParts is a valid link origin split for matching a request Host:
// the lower-cased host name, the effective port (explicit, or the scheme's
// default), and the scheme's default port.
type LinkOriginParts struct {
	Host, Port, DefaultPort string
}

// ParseLinkOrigin parses a link origin by the rules of ValidateLinkOrigin and
// splits it. It is the one parse every reader of artifacts.link_origin uses
// (validation, the hub conflict check and the request match), so they cannot
// disagree. An empty or invalid value yields ok false.
func ParseLinkOrigin(raw string) (LinkOriginParts, bool) {
	n, err := ValidateLinkOrigin(raw)
	if err != nil || n == "" {
		return LinkOriginParts{}, false
	}
	scheme, rest, _ := strings.Cut(n, "://")
	def := strconv.Itoa(defaultPort(scheme))
	host, port, hasPort := strings.Cut(rest, ":")
	if !hasPort {
		port = def
	}
	return LinkOriginParts{Host: host, Port: port, DefaultPort: def}, true
}

// LinkOriginHubConflict compares a link origin with the hub's public URL.
// sameHost is true when the link origin's host name is the hub's own host
// name (case-insensitive, a trailing dot ignored), whatever the scheme and
// port: such a value is refused on save and ignored at runtime, since the
// hub would confine its own host to the artifact routes, and a second port
// on the same host gives no cookie isolation. sameSite is true when both
// hosts share a registrable domain (eTLD+1, best effort from the public
// suffix list): such a value is accepted with a warning, because a secondary
// origin under the hub's site isolates little. An invalid link origin, or a
// hub URL with no host, conflicts with nothing.
func LinkOriginHubConflict(linkOrigin, hubPublicURL string) (sameHost, sameSite bool) {
	lo, ok := ParseLinkOrigin(linkOrigin)
	if !ok {
		return false, false
	}
	hub := urlHostName(hubPublicURL)
	if hub == "" {
		return false, false
	}
	if lo.Host == hub {
		return true, true
	}
	return false, sameRegistrableDomain(lo.Host, hub)
}

// RequestOnHubHost reports whether a request Host (host[:port], as the hub
// receives it) names the hub's own host name, whatever the port. A link
// origin on that host name is never used (LinkOriginHubConflict), so such a
// request is never a secondary-origin request.
func RequestOnHubHost(reqHost, hubPublicURL string) bool {
	hub := urlHostName(hubPublicURL)
	if hub == "" || reqHost == "" {
		return false
	}
	host := reqHost
	if h, _, err := net.SplitHostPort(reqHost); err == nil {
		host = h
	}
	return strings.TrimSuffix(strings.ToLower(host), ".") == hub
}

// urlHostName returns the lower-cased host name of an absolute URL, a
// trailing dot dropped, or "" when it has none.
func urlHostName(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
}

// sameRegistrableDomain reports whether two host names share an eTLD+1.
// IP addresses and names the public suffix list cannot place share none,
// except that identical hosts always do.
func sameRegistrableDomain(a, b string) bool {
	if a == b {
		return true
	}
	if net.ParseIP(a) != nil || net.ParseIP(b) != nil {
		return false
	}
	ra, errA := publicsuffix.EffectiveTLDPlusOne(a)
	rb, errB := publicsuffix.EffectiveTLDPlusOne(b)
	return errA == nil && errB == nil && ra == rb
}

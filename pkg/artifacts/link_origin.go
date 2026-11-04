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
	"net"
	"net/http"
	"net/url"
	"strings"

	"github.com/GoogleCloudPlatform/scion/pkg/config/opsettings"
)

// The operator's secondary link origin (artifacts.link_origin, ptone/scion#7107
// phase 2b) is an origin that routes back to this hub, on which share links
// are offered. A request is a secondary-origin request exactly when its Host,
// as the hub receives it (r.Host), names that origin's host and port. One
// detection, SecondaryOriginRequest, decides it; LinkOriginGuard applies it
// once per request and records the result in the request context, and the
// confinement, the credential drop and the view CSP source all follow that
// record. X-Forwarded-Host, Forwarded and every other header a requester
// can set are never read: a proxy in front of the secondary origin must
// preserve Host, or the hub cannot tell the origins apart.

// linkOrigin is a parsed secondary link origin.
type linkOrigin struct {
	host string // lower-cased host name, no trailing dot
	port string // explicit or the scheme's default port
	def  string // the scheme's default port
	// cspHost is the host-source of the view CSP: host, plus the port
	// when it is not the scheme's default.
	cspHost string
}

// parseLinkOrigin parses a link origin with opsettings.ParseLinkOrigin, the
// one parse validation and the hub's conflict check use too, so the guard
// never accepts a value the settings would refuse. An empty or invalid value
// gives no secondary origin, no confinement and no shareUrl.
func parseLinkOrigin(raw string) (linkOrigin, bool) {
	p, ok := opsettings.ParseLinkOrigin(raw)
	if !ok {
		return linkOrigin{}, false
	}
	o := linkOrigin{host: p.Host, port: p.Port, def: p.DefaultPort, cspHost: p.Host}
	if p.Port != p.DefaultPort {
		o.cspHost += ":" + p.Port
	}
	// Kept as defence in depth: the CSP source must be a plain host-source
	// whatever the parser accepts.
	if !validCSPHost(o.cspHost) {
		return linkOrigin{}, false
	}
	return o, true
}

// matchesHost reports whether a request Host names this origin: the host
// compared case-insensitively (a single trailing dot ignored, since it
// names the same host), the port exactly, with the scheme's default port
// implied when the Host carries none.
func (o linkOrigin) matchesHost(reqHost string) bool {
	host, port := reqHost, ""
	if h, p, err := net.SplitHostPort(reqHost); err == nil {
		host, port = h, p
	}
	if port == "" {
		port = o.def
	}
	return strings.TrimSuffix(strings.ToLower(host), ".") == o.host && port == o.port
}

// SecondaryOriginRequest reports whether r is a request on the secondary
// link origin linkOriginValue: whether r.Host, the Host the hub received,
// names that origin's host and port. It reads nothing else from the
// request. An empty or unparseable linkOriginValue matches no request.
func SecondaryOriginRequest(r *http.Request, linkOriginValue string) bool {
	o, ok := parseLinkOrigin(linkOriginValue)
	return ok && o.matchesHost(r.Host)
}

// secondaryOriginKey marks a request context as a secondary-origin request;
// its value is the origin's CSP host-source.
type secondaryOriginKey struct{}

// secondaryOriginHost returns the CSP host-source of the secondary link
// origin when LinkOriginGuard marked ctx as a secondary-origin request.
func secondaryOriginHost(ctx context.Context) (string, bool) {
	h, ok := ctx.Value(secondaryOriginKey{}).(string)
	return h, ok && h != ""
}

// LinkOriginGuard returns the outermost middleware for the secondary link
// origin (security review conditions 2 and 3). linkOriginValue yields the
// current normalized artifacts.link_origin ("" when none is set), read on
// every request so a settings change applies without a restart.
//
// On a request that is not a secondary-origin request (SecondaryOriginRequest),
// the middleware does nothing. On one that is:
//
//   - only a request servesOnOrigin admits is served: the host passes its
//     check for a read of the share-link route (RouteShared) or the view
//     route (RouteView), the one its authentication lets through without a
//     credential. Every other request, the web app and every other API
//     route included, answers 404 before any inner handler or
//     authentication runs. A nil servesOnOrigin admits nothing;
//   - the Cookie and Authorization headers are removed, and then
//     stripCredentials (when not nil) removes the host's other credential
//     headers, so no ambient credential can authenticate on that origin;
//   - the request context is marked, so the view route builds its CSP on
//     the configured link origin.
//
// The inner handler gets a clone; the caller's request is not changed.
func LinkOriginGuard(linkOriginValue func() string, servesOnOrigin func(*http.Request) bool, stripCredentials func(http.Header)) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			o, ok := parseLinkOrigin(linkOriginValue())
			if !ok || !o.matchesHost(r.Host) {
				next.ServeHTTP(w, r)
				return
			}
			if servesOnOrigin == nil || !servesOnOrigin(r) {
				writeNotFound(w)
				return
			}
			r2 := r.Clone(context.WithValue(r.Context(), secondaryOriginKey{}, o.cspHost))
			r2.Header.Del("Cookie")
			r2.Header.Del("Authorization")
			if stripCredentials != nil {
				stripCredentials(r2.Header)
			}
			next.ServeHTTP(w, r2)
		})
	}
}

// shareURL returns the absolute share link on the secondary link origin
// for the hub-relative link path rel, or "" when limits carry no valid
// link origin. It is built from the configured value only.
func shareURL(l Limits, rel string) string {
	if _, ok := parseLinkOrigin(l.LinkOrigin); !ok {
		return ""
	}
	return strings.TrimSuffix(l.LinkOrigin, "/") + rel
}

// hubCSPHost returns the CSP host-source of the hub's configured public
// origin, or "" when none is configured or it cannot be named.
func hubCSPHost(publicOrigin string) string {
	u, err := url.Parse(publicOrigin)
	if err != nil || u.Host == "" {
		return ""
	}
	h := strings.ToLower(u.Host)
	if !validCSPHost(h) {
		return ""
	}
	return h
}

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

package hub

import (
	"log/slog"
	"net"
	"net/http"
	"strings"

	"github.com/GoogleCloudPlatform/scion/pkg/config"
	"github.com/GoogleCloudPlatform/scion/pkg/util/logging"
)

// Hub-broker transport policy, hub side.
//
// The broker join and secret rotation responses carry the broker's HMAC
// secret, broker registration and join-token re-mint (POST /api/v1/brokers)
// return a join token, and the control channel and the broker conduit
// session carry the secrets of every agent the broker runs. The hub serves
// them only on a request that arrived over an encrypted channel or from this
// host (confidentialArrival); every other request is refused before anything
// secret is produced. The broker side refuses the same hubs before sending
// anything (config.CheckHubTransport); this is the hub's own check, for a
// broker that does not.

// ErrCodeTransportNotConfidential is the error code of a request refused by
// the hub-broker transport policy.
const ErrCodeTransportNotConfidential = "transport_not_confidential"

// transportRefusalMessage is the body message of that refusal. It names
// both fixes and the trusted proxy setting, and nothing about the request.
const transportRefusalMessage = "This request carries broker credentials and must reach the hub over TLS or from this host. " +
	"Use the hub's https:// URL. If TLS ends at a proxy in front of the hub, list the proxy's addresses in " +
	config.TrustedProxiesKey + " so the hub trusts its X-Forwarded-Proto header."

// forwardingHeaders mark a request relayed by a proxy. A loopback peer that
// sends any of them is a proxy on this host, not a local client, and is
// judged as a proxy.
var forwardingHeaders = []string{"Forwarded", "X-Forwarded-For", "X-Forwarded-Proto", "X-Forwarded-Host", "X-Real-Ip"}

// confidentialArrival reports whether r reached the hub over an encrypted
// channel or from this host. In order:
//  1. r.TLS is set: the hub terminated TLS itself.
//  2. The immediate peer is a loopback address and r has no forwarding
//     header: a client on this host (the co-located broker, a local CLI).
//  3. trusted is not empty, the immediate peer is in it, and the forwarded
//     protocol is exactly https (forwardedProtoIsHTTPS). With trusted empty
//     a forwarded protocol is never trusted, so a forged header from any
//     peer is ignored.
//
// Everything else is refused. Only r.RemoteAddr is used as the peer;
// X-Forwarded-For is never walked.
func confidentialArrival(r *http.Request, trusted []*net.IPNet) bool {
	if r.TLS != nil {
		return true
	}
	peer := remotePeerIP(r)
	if peer == nil {
		return false
	}
	if peer.IsLoopback() && !hasForwardingHeader(r) {
		return true
	}
	if len(trusted) == 0 || !ipInNets(peer, trusted) {
		return false
	}
	return forwardedProtoIsHTTPS(r)
}

// remotePeerIP is the IP of r's immediate peer, or nil when RemoteAddr does
// not hold one.
func remotePeerIP(r *http.Request) net.IP {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return nil
	}
	return net.ParseIP(host)
}

func ipInNets(ip net.IP, nets []*net.IPNet) bool {
	for _, n := range nets {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

func hasForwardingHeader(r *http.Request) bool {
	for _, h := range forwardingHeaders {
		if _, ok := r.Header[http.CanonicalHeaderKey(h)]; ok {
			return true
		}
	}
	return false
}

// forwardedProtoIsHTTPS reports whether the proxy's forwarded protocol is
// unambiguously https: every X-Forwarded-Proto line together holds exactly
// one value, "https", and every Forwarded line together holds exactly one
// element whose proto is https; when both headers are present both must say
// so, and at least one must be. More than one value (a proxy that appends
// instead of overwriting), an empty value or a disagreement is not https.
func forwardedProtoIsHTTPS(r *http.Request) bool {
	xfp, hasXFP := headerList(r, "X-Forwarded-Proto")
	fwd, hasFwd := headerList(r, "Forwarded")
	if !hasXFP && !hasFwd {
		return false
	}
	if hasXFP && (len(xfp) != 1 || !strings.EqualFold(xfp[0], "https")) {
		return false
	}
	if hasFwd && (len(fwd) != 1 || !forwardedElementProtoHTTPS(fwd[0])) {
		return false
	}
	return true
}

// headerList splits every line of header name on commas and trims each
// item. ok reports whether the header is present at all; an empty item is
// kept, so it makes the list ambiguous.
func headerList(r *http.Request, name string) (items []string, ok bool) {
	lines, ok := r.Header[http.CanonicalHeaderKey(name)]
	if !ok {
		return nil, false
	}
	for _, line := range lines {
		for _, item := range strings.Split(line, ",") {
			items = append(items, strings.TrimSpace(item))
		}
	}
	return items, true
}

// forwardedElementProtoHTTPS reports whether one Forwarded element
// (RFC 7239: pairs separated by ";") has exactly one proto pair, equal to
// https (quoted or not).
func forwardedElementProtoHTTPS(element string) bool {
	found := 0
	https := false
	for _, pair := range strings.Split(element, ";") {
		k, v, ok := strings.Cut(strings.TrimSpace(pair), "=")
		if !ok || !strings.EqualFold(strings.TrimSpace(k), "proto") {
			continue
		}
		found++
		v = strings.Trim(strings.TrimSpace(v), `"`)
		https = strings.EqualFold(v, "https")
	}
	return found == 1 && https
}

// requireConfidentialArrival writes the transport refusal (403) and returns
// false when r did not arrive confidentially (confidentialArrival with the
// hub's server.trusted_proxies).
func (s *Server) requireConfidentialArrival(w http.ResponseWriter, r *http.Request) bool {
	if confidentialArrival(r, s.transportTrustedNets) {
		return true
	}
	slog.Warn("Refused a broker credential request that did not arrive over TLS or from this host",
		"path", logging.RequestPath(r), "remote_addr", r.RemoteAddr, "trusted_proxies_configured", len(s.transportTrustedNets) > 0)
	writeError(w, http.StatusForbidden, ErrCodeTransportNotConfidential, transportRefusalMessage, nil)
	return false
}

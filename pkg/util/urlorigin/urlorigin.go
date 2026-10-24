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

// Package urlorigin compares URL origins (scheme, host and port) for HTTP
// clients that send a credential only to one origin.
//
// The comparison fails closed. A host with a non-ASCII byte has no origin:
// net/http dials the IDNA (punycode) form of such a host, and Unicode case
// folding (strings.ToLower maps U+0130 to "i" and U+212A to "k") does not
// give that form, so a folded comparison could match a host that net/http
// dials as a different name. A host with a non-ASCII name is written in its
// punycode form. Spellings that net/http would dial to the same host are not
// normalized either and compare as different origins: a trailing-dot host
// ("hub.example.com."), a port with leading zeros (":0443") and non-canonical
// IPv4 or IPv6 forms.
package urlorigin

import (
	"net/url"
	"strings"
)

// Origin is the scheme, host and effective port of a URL, with the scheme
// and host lower-cased (an IPv6 zone ID is kept as written).
type Origin struct {
	Scheme string
	Host   string
	Port   string
}

// Of returns the origin of u, with the scheme and host lower-cased and the
// default port (80 for http, 443 for https) filled in when u has none. ok is
// false when u is nil, has no host, or its host has a non-ASCII byte.
func Of(u *url.URL) (o Origin, ok bool) {
	if u == nil {
		return Origin{}, false
	}
	host := u.Hostname()
	if host == "" || !isASCII(host) {
		return Origin{}, false
	}
	o.Scheme = strings.ToLower(u.Scheme)
	o.Host = lowerHost(host)
	o.Port = u.Port()
	if o.Port == "" {
		switch o.Scheme {
		case "http":
			o.Port = "80"
		case "https":
			o.Port = "443"
		}
	}
	return o, true
}

// Same reports whether u has the origin want. The zero Origin is the origin
// of no URL.
func Same(u *url.URL, want Origin) bool {
	got, ok := Of(u)
	return ok && got == want
}

// lowerHost lower-cases host up to an IPv6 zone separator ("%") and keeps
// the zone byte-exact, since interface names are case-sensitive.
func lowerHost(host string) string {
	if i := strings.IndexByte(host, '%'); i >= 0 {
		return strings.ToLower(host[:i]) + host[i:]
	}
	return strings.ToLower(host)
}

// isASCII reports whether s has only bytes below 0x80.
func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			return false
		}
	}
	return true
}

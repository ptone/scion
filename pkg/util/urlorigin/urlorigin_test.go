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

package urlorigin

import (
	"net"
	"net/url"
	"testing"
)

func mustParse(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("url.Parse(%q): %v", raw, err)
	}
	return u
}

func TestOf(t *testing.T) {
	cases := []struct {
		raw  string
		want Origin
		ok   bool
	}{
		{"https://hub.example.com", Origin{"https", "hub.example.com", "443"}, true},
		{"http://hub.example.com", Origin{"http", "hub.example.com", "80"}, true},
		{"https://HUB.Example.COM:8443/x", Origin{"https", "hub.example.com", "8443"}, true},
		{"HTTPS://hub.example.com", Origin{"https", "hub.example.com", "443"}, true},
		{"https://[::1]", Origin{"https", "::1", "443"}, true},
		{"https://[::ABCD]:8443", Origin{"https", "::abcd", "8443"}, true},
		{"https://user:pw@hub.example.com/", Origin{"https", "hub.example.com", "443"}, true},
		{"https://hub.example.com:", Origin{"https", "hub.example.com", "443"}, true},
		{"ftp://hub.example.com", Origin{"ftp", "hub.example.com", ""}, true},
		{"https://hub.example.com.", Origin{"https", "hub.example.com.", "443"}, true},
		{"https://hub.example.com:0443", Origin{"https", "hub.example.com", "0443"}, true},
		{"https://[FE80::1%25ETH0]", Origin{"https", "fe80::1%ETH0", "443"}, true},
		{"https://[fe80::1%25eth0]:8443", Origin{"https", "fe80::1%eth0", "8443"}, true},
		{"https://hub.%C4%B0nternal/", Origin{}, false},
		{"https://hub.\u0130nternal/", Origin{}, false},
		{"https://\u212aelvin.internal/", Origin{}, false},
		{"https://k%E2%84%AAelvin.internal/", Origin{}, false},
		{"/relative", Origin{}, false},
		{"https://:443/", Origin{}, false},
	}
	for _, tc := range cases {
		got, ok := Of(mustParse(t, tc.raw))
		if got != tc.want || ok != tc.ok {
			t.Errorf("Of(%q) = %+v, %v; want %+v, %v", tc.raw, got, ok, tc.want, tc.ok)
		}
	}
	if got, ok := Of(nil); ok || got != (Origin{}) {
		t.Errorf("Of(nil) = %+v, %v; want the zero Origin, false", got, ok)
	}
}

// TestSame is the shared origin table: the bridge hub origin check and the
// apiclient redirect policy both compare origins with Same.
func TestSame(t *testing.T) {
	cases := []struct {
		name, endpoint, target string
		want                   bool
	}{
		{"percent-encoded U+0130 host", "https://hub.internal", "https://hub.%C4%B0nternal/api", false},
		{"raw UTF-8 U+0130 host", "https://hub.internal", "https://hub.\u0130nternal/api", false},
		{"U+212A Kelvin sign host", "https://kelvin.internal", "https://\u212aelvin.internal/api", false},
		{"trailing-dot host", "https://hub.internal", "https://hub.internal./api", false},
		{"port with leading zeros", "https://hub.internal", "https://hub.internal:0443/api", false},
		{"other port", "https://hub.internal", "https://hub.internal:8443/api", false},
		{"other scheme", "https://hub.internal", "http://hub.internal:443/api", false},
		{"subdomain", "https://hub.internal", "https://sub.hub.internal/api", false},
		{"IPv6 with and without default port", "https://[::1]", "https://[::1]:443/api", true},
		{"upper-case IPv6 hex", "https://[::abcd]", "https://[::ABCD]/api", true},
		{"userinfo with the hub host", "https://hub.internal", "https://user:pw@hub.internal/api", true},
		{"case and explicit default port", "https://hub.internal", "HTTPS://HUB.internal:443/api", true},
		{"IPv6 zone differing in case", "https://[fe80::1%25ETH0]", "https://[fe80::1%25eth0]/api", false},
		{"identical IPv6 zones", "https://[fe80::1%25ETH0]", "https://[FE80::1%25ETH0]:443/api", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			want, ok := Of(mustParse(t, tc.endpoint))
			if !ok {
				t.Fatalf("Of(%q) has no origin", tc.endpoint)
			}
			if got := Same(mustParse(t, tc.target), want); got != tc.want {
				t.Errorf("Same(%q, origin of %q) = %v, want %v", tc.target, tc.endpoint, got, tc.want)
			}
		})
	}
}

func TestSame_ZeroOriginMatchesNothing(t *testing.T) {
	for _, raw := range []string{"https://hub.example.com/", "http://:80/", "/relative", "https://hub.\u0130nternal/"} {
		if Same(mustParse(t, raw), Origin{}) {
			t.Errorf("Same(%q, zero Origin) = true", raw)
		}
	}
}

func TestOf_HostAndPortJoin(t *testing.T) {
	for raw, want := range map[string]string{
		"https://hub.example.com":   "hub.example.com:443",
		"http://HUB.example.com:80": "hub.example.com:80",
		"https://[::1]":             "[::1]:443",
	} {
		o, _ := Of(mustParse(t, raw))
		if got := net.JoinHostPort(o.Host, o.Port); got != want {
			t.Errorf("host:port of %q = %q, want %q", raw, got, want)
		}
	}
}

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
	"encoding/json"
	"testing"
)

// TestValidateLinkOrigin is the condition 5 validation table for the
// value's own shape (kill check K10: accepting a path fails it).
func TestValidateLinkOrigin(t *testing.T) {
	accepted := []struct{ raw, want string }{
		{"", ""},
		{"https://h", "https://h"},
		{"https://h:8443", "https://h:8443"},
		{"https://links.example.net", "https://links.example.net"},
		{"HTTPS://Links.Example.NET", "https://links.example.net"},
		{"https://links.example.net/", "https://links.example.net"},
		{"https://links.example.net:443", "https://links.example.net"},
		{"https://203.0.113.7:8443", "https://203.0.113.7:8443"},
		{"http://localhost:9000", "http://localhost:9000"},
		{"http://127.0.0.1:9000", "http://127.0.0.1:9000"},
		{"http://localhost:80", "http://localhost"},
	}
	for _, tc := range accepted {
		got, err := ValidateLinkOrigin(tc.raw)
		if err != nil || got != tc.want {
			t.Errorf("ValidateLinkOrigin(%q) = %q, %v; want %q", tc.raw, got, err, tc.want)
		}
	}
	rejected := map[string]string{
		"path":                 "https://h/share",
		"nested path":          "https://h/a/b",
		"encoded path":         "https://h/%2F",
		"query":                "https://h?x=1",
		"empty query":          "https://h?",
		"fragment":             "https://h#f",
		"empty fragment":       "https://h#",
		"userinfo":             "https://u:p@h",
		"user only":            "https://u@h",
		"http non-loopback":    "http://links.example.net",
		"empty host":           "https://",
		"empty host with port": "https://:8443",
		"no scheme":            "links.example.net",
		"other scheme":         "ftp://h",
		"opaque":               "https:h",
		"ipv6":                 "https://[2001:db8::1]",
		"underscore host":      "https://a_b.example.net",
		"empty label":          "https://a..b",
		"port zero":            "https://h:0",
		"port too large":       "https://h:65536",
		"port not numeric":     "https://h:x",
		"empty port":           "https://h:",
		"leading zero port":    "https://h:08443",
		"space":                "https://h x",
	}
	for name, raw := range rejected {
		if got, err := ValidateLinkOrigin(raw); err == nil {
			t.Errorf("%s: ValidateLinkOrigin(%q) = %q, want an error", name, raw, got)
		}
	}
}

// TestLinkOriginHubConflict: a value on the hub's own host name, whatever
// its scheme or port, is a conflict (refused on save, ignored at runtime;
// kill check K11), and one sharing the hub's registrable domain is flagged
// for the warning (condition 5).
func TestLinkOriginHubConflict(t *testing.T) {
	for _, tc := range []struct {
		name, lo, hub      string
		wantSame, wantSite bool
	}{
		{"same origin", "https://hub.example.com", "https://hub.example.com", true, true},
		{"hub URL with a path", "https://hub.example.com", "https://hub.example.com/", true, true},
		{"explicit default port", "https://hub.example.com", "https://hub.example.com:443", true, true},
		{"host case differs", "https://hub.example.com", "https://HUB.example.com", true, true},
		{"hub host with a trailing dot", "https://hub.example.com", "https://hub.example.com.", true, true},
		{"same host, other port", "https://hub.example.com:8443", "https://hub.example.com", true, true},
		{"same host, other scheme", "https://localhost:8080", "http://localhost:8080", true, true},
		{"same loopback host, other port", "http://localhost:9000", "http://localhost:8080", true, true},
		{"other loopback name", "http://127.0.0.1:9000", "http://localhost:8080", false, false},
		{"same site", "https://links.example.com", "https://hub.example.com", false, true},
		{"same site under a public suffix", "https://a.example.co.uk", "https://b.example.co.uk", false, true},
		{"different site", "https://links.example.net", "https://hub.example.com", false, false},
		{"different public suffix tenants", "https://a.github.io", "https://b.github.io", false, false},
		{"unparseable hub", "https://links.example.net", "::", false, false},
		{"empty hub", "https://links.example.net", "", false, false},
		{"empty link origin", "", "https://hub.example.com", false, false},
		{"invalid link origin", "https://hub.example.com/x", "https://hub.example.com", false, false},
	} {
		same, site := LinkOriginHubConflict(tc.lo, tc.hub)
		if same != tc.wantSame || site != tc.wantSite {
			t.Errorf("%s: LinkOriginHubConflict(%q, %q) = %v, %v; want %v, %v",
				tc.name, tc.lo, tc.hub, same, site, tc.wantSame, tc.wantSite)
		}
	}
}

// TestParseLinkOrigin: the one parse splits a valid value into host, port
// and default port, and refuses what ValidateLinkOrigin refuses.
func TestParseLinkOrigin(t *testing.T) {
	for raw, want := range map[string]LinkOriginParts{
		"https://links.example.net":      {Host: "links.example.net", Port: "443", DefaultPort: "443"},
		"HTTPS://Links.Example.NET:443/": {Host: "links.example.net", Port: "443", DefaultPort: "443"},
		"https://links.example.net:8443": {Host: "links.example.net", Port: "8443", DefaultPort: "443"},
		"http://localhost:9000":          {Host: "localhost", Port: "9000", DefaultPort: "80"},
	} {
		got, ok := ParseLinkOrigin(raw)
		if !ok || got != want {
			t.Errorf("ParseLinkOrigin(%q) = %+v, %v; want %+v", raw, got, ok, want)
		}
	}
	for _, raw := range []string{"", "https://h/x", "http://links.example.net", "ftp://h", "https://u@h", "https://[::1]"} {
		if got, ok := ParseLinkOrigin(raw); ok {
			t.Errorf("ParseLinkOrigin(%q) = %+v, want refused", raw, got)
		}
	}
}

// TestRequestOnHubHost: a request Host on the hub's host name, at any port,
// is on the hub host; any other is not.
func TestRequestOnHubHost(t *testing.T) {
	hub := "https://hub.example.com"
	for host, want := range map[string]bool{
		"hub.example.com":      true,
		"HUB.example.com:8443": true,
		"hub.example.com.":     true,
		"links.example.net":    false,
		"hub.example.com.evil": false,
		"xhub.example.com":     false,
		"":                     false,
	} {
		if got := RequestOnHubHost(host, hub); got != want {
			t.Errorf("RequestOnHubHost(%q) = %v, want %v", host, got, want)
		}
	}
	if RequestOnHubHost("hub.example.com", "") {
		t.Error("an empty hub URL matched")
	}
}

// TestParseArtifactsDocLinkOrigin: a valid link_origin is resolved
// normalized; an invalid stored one is ignored with the problem named,
// and the service keeps running (links fall back to the hub origin).
func TestParseArtifactsDocLinkOrigin(t *testing.T) {
	got, err := ParseArtifactsDoc(json.RawMessage(`{"link_origin":"https://Links.Example.NET:443"}`))
	if err != nil || got.LinkOrigin != "https://links.example.net" || got.LinkOriginInvalid != "" || !got.Enabled {
		t.Fatalf("valid link_origin: %+v, %v", got, err)
	}
	for _, raw := range []string{
		`{"link_origin":"https://h/path"}`,
		`{"link_origin":"http://links.example.net"}`,
		`{"link_origin":"https://u@h"}`,
	} {
		got, err := ParseArtifactsDoc(json.RawMessage(raw))
		if err != nil || got.Malformed || !got.Enabled {
			t.Errorf("%s: service disabled: %+v, %v", raw, got, err)
		}
		if got.LinkOrigin != "" || got.LinkOriginInvalid == "" {
			t.Errorf("%s: invalid link_origin not ignored: %+v", raw, got)
		}
	}
	got, err = ParseArtifactsDoc(json.RawMessage(`{}`))
	if err != nil || got.LinkOrigin != "" || got.LinkOriginInvalid != "" {
		t.Errorf("absent link_origin: %+v, %v", got, err)
	}
}

// TestValidateArtifactsLinkOrigin: a write with an invalid link_origin is
// refused, naming the key; a valid or empty one is accepted.
func TestValidateArtifactsLinkOrigin(t *testing.T) {
	for _, raw := range []string{
		`{"link_origin":"https://links.example.net"}`,
		`{"link_origin":"https://links.example.net:8443"}`,
		`{"link_origin":""}`,
	} {
		if errs := Validate("artifacts", json.RawMessage(raw)); len(errs) != 0 {
			t.Errorf("Validate(%s) = %v, want no errors", raw, errs)
		}
	}
	for _, raw := range []string{
		`{"link_origin":"https://links.example.net/x"}`,
		`{"link_origin":"http://links.example.net"}`,
		`{"link_origin":7}`,
	} {
		errs := Validate("artifacts", json.RawMessage(raw))
		if len(errs) == 0 {
			t.Errorf("Validate(%s) accepted", raw)
		}
	}
	errs := Validate("artifacts", json.RawMessage(`{"link_origin":"https://h?q"}`))
	if len(errs) != 1 || errs[0].Path != "link_origin" {
		t.Errorf("Validate(query) = %+v, want one error on link_origin", errs)
	}
}

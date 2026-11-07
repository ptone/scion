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
	"strings"
	"testing"
)

// assertNormalizeRefused checks that NormalizeEgressAllowEntry refuses entry
// with an error containing wantReason.
func assertNormalizeRefused(t *testing.T, entry, wantReason string) {
	t.Helper()
	got, err := NormalizeEgressAllowEntry(entry)
	if err == nil {
		t.Errorf("NormalizeEgressAllowEntry(%q) = %q, nil; want a refusal (%s)", entry, got, wantReason)
		return
	}
	if !strings.Contains(err.Error(), wantReason) {
		t.Errorf("NormalizeEgressAllowEntry(%q) error = %q, want it to contain %q", entry, err, wantReason)
	}
}

// The accepted forms and their canonical strings.
func TestNormalizeEgressAllowEntry_PortAndSchemeForms(t *testing.T) {
	cases := map[string]string{
		"api.example.com":               "api.example.com",
		"  API.Example.COM.  ":          "api.example.com",
		"*.example.com":                 "*.example.com",
		"api.example.com:443":           "api.example.com",
		"otel.example.com:4317":         "otel.example.com:4317",
		"https://api.example.com":       "api.example.com",
		"https://api.example.com:8443/": "api.example.com:8443",
		"http://plain.example.com":      "http://plain.example.com:80",
		"HTTP://Plain.Example.com:8080": "http://plain.example.com:8080",
		"*.example.com:8443":            "*.example.com:8443",
		"http://*.example.com:8080":     "http://*.example.com:8080",
	}
	for entry, want := range cases {
		got, err := NormalizeEgressAllowEntry(entry)
		if err != nil || got != want {
			t.Errorf("NormalizeEgressAllowEntry(%q) = %q, %v; want %q", entry, got, err, want)
		}
	}
	if got, err := NormalizeEgressAllowEntry("   "); got != "" || err != nil {
		t.Errorf("NormalizeEgressAllowEntry(blank) = %q, %v; want skipped without error", got, err)
	}
}

// Rule: the port is an integer from 1 to 65535.
func TestNormalizeEgressAllowEntry_PortRange(t *testing.T) {
	for _, ok := range []string{"api.example.com:1", "api.example.com:65535", "http://api.example.com:1", "https://api.example.com:65535"} {
		if _, err := NormalizeEgressAllowEntry(ok); err != nil {
			t.Errorf("NormalizeEgressAllowEntry(%q) error = %v, want accepted", ok, err)
		}
	}
	for _, bad := range []string{
		"api.example.com:0", "api.example.com:65536", "api.example.com:https", "api.example.com:-1", "api.example.com:",
		"http://api.example.com:0", "http://api.example.com:65536", "http://api.example.com:x",
	} {
		assertNormalizeRefused(t, bad, "invalid port")
	}
	assertNormalizeRefused(t, "api.example.com:443:1", "not host:port")
}

// Rule: a URL entry carries no userinfo, and the refusal does not echo it.
func TestNormalizeEgressAllowEntry_UserinfoRefused(t *testing.T) {
	for _, bad := range []string{"http://user@api.example.com", "http://user:tok-secret@api.example.com:8080", "https://user@api.example.com"} {
		assertNormalizeRefused(t, bad, "userinfo")
		_, err := NormalizeEgressAllowEntry(bad)
		if err != nil && (strings.Contains(err.Error(), "tok-secret") || strings.Contains(err.Error(), "user@")) {
			t.Errorf("NormalizeEgressAllowEntry(%q) error = %q echoes the userinfo", bad, err)
		}
	}
}

// Rule: a URL entry carries no path.
func TestNormalizeEgressAllowEntry_PathRefused(t *testing.T) {
	for _, bad := range []string{"http://api.example.com/v1", "https://api.example.com:8443/a/b"} {
		assertNormalizeRefused(t, bad, "path")
	}
}

// Rule: a URL entry carries no query.
func TestNormalizeEgressAllowEntry_QueryRefused(t *testing.T) {
	for _, bad := range []string{"http://api.example.com?x=1", "http://api.example.com/?x=1", "https://api.example.com?"} {
		assertNormalizeRefused(t, bad, "query")
	}
}

// Rule: a URL entry carries no fragment.
func TestNormalizeEgressAllowEntry_FragmentRefused(t *testing.T) {
	for _, bad := range []string{"http://api.example.com#frag", "https://api.example.com/#x"} {
		assertNormalizeRefused(t, bad, "fragment")
	}
}

// Rule: only http:// and https:// schemes.
func TestNormalizeEgressAllowEntry_SchemeRefused(t *testing.T) {
	for _, bad := range []string{"ftp://api.example.com", "grpc://api.example.com:4317"} {
		assertNormalizeRefused(t, bad, "unsupported scheme")
	}
}

// Rule: the host part passes every existing host check, whatever the
// syntax (design review r2, R2-3 negatives, plus the catch-all and
// two-label forms).
func TestNormalizeEgressAllowEntry_HostChecksCarryOver(t *testing.T) {
	cases := []struct {
		entry      string
		wantReason string
	}{
		{"http://169.254.169.254", "IP/CIDR"},
		{"http://user@host", "userinfo"},
		{"host:0", "invalid port"},
		{"hub.ns.svc:8080", "ICANN"},
		{"10.0.0.1:443", "IP/CIDR"},
		{"http://hub.scion-system.svc.cluster.local:8080", "ICANN"},
		{"kubernetes.default.svc:443", "ICANN"},
		{"https://127.1", "IP/CIDR"},
		{"https://[2001:db8::1]:443", "unsupported character"},
		{"*:443", "catch-all"},
		{"http://*", "catch-all"},
		{"https://*:8443", "catch-all"},
		{"*.com:443", "itself a public suffix"},
		{"metadata:80", "single-label"},
	}
	for _, tc := range cases {
		assertNormalizeRefused(t, tc.entry, tc.wantReason)
	}
}

// ParseEgressAllow returns the typed targets and refuses one host and port
// on two protocols, deterministically naming both.
func TestParseEgressAllow_TargetsAndProtocolConflict(t *testing.T) {
	got, err := ParseEgressAllow([]string{"svc.example.com:8080", "http://svc.example.com:8081", "", "*.example.com"})
	if err != nil {
		t.Fatalf("ParseEgressAllow() error = %v", err)
	}
	want := []EgressAllowTarget{
		{EgressProtocolTLSPassthrough, "svc.example.com", 8080},
		{EgressProtocolHTTP, "svc.example.com", 8081},
		{EgressProtocolTLSPassthrough, "*.example.com", 443},
	}
	if len(got) != len(want) {
		t.Fatalf("ParseEgressAllow() = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("ParseEgressAllow()[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}

	for _, entries := range [][]string{
		{"svc.example.com:8080", "http://svc.example.com:8080"},
		{"http://h.example.com:443", "h.example.com"},
	} {
		_, err := ParseEgressAllow(entries)
		if err == nil {
			t.Errorf("ParseEgressAllow(%q) = nil error, want a refusal", entries)
			continue
		}
		for _, want := range []string{"http", "tls_passthrough"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("ParseEgressAllow(%q) error = %q, want it to name %q", entries, err, want)
			}
		}
	}
}

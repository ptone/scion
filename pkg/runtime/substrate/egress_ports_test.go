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
// with an error containing wantReason, returning the zero entry.
func assertNormalizeRefused(t *testing.T, entry, wantReason string) {
	t.Helper()
	got, err := NormalizeEgressAllowEntry(entry)
	if err == nil {
		t.Errorf("NormalizeEgressAllowEntry(%q) = %+v, nil; want a refusal (%s)", entry, got, wantReason)
		return
	}
	if !got.IsZero() {
		t.Errorf("NormalizeEgressAllowEntry(%q) returned %+v alongside its error", entry, got)
	}
	if !strings.Contains(err.Error(), wantReason) {
		t.Errorf("NormalizeEgressAllowEntry(%q) error = %q, want it to contain %q", entry, err, wantReason)
	}
}

// The accepted forms and the canonical entry each returns.
func TestNormalizeEgressAllowEntry_PortAndSchemeForms(t *testing.T) {
	cases := map[string]EgressAllowEntry{
		"api.example.com":               {Scheme: "https", Host: "api.example.com", Port: 443},
		"  API.Example.COM.  ":          {Scheme: "https", Host: "api.example.com", Port: 443},
		"*.example.com":                 {Scheme: "https", Host: "*.example.com", Port: 443, Wildcard: true},
		"api.example.com:443":           {Scheme: "https", Host: "api.example.com", Port: 443},
		"otel.example.com:4317":         {Scheme: "https", Host: "otel.example.com", Port: 4317},
		"https://api.example.com":       {Scheme: "https", Host: "api.example.com", Port: 443},
		"https://api.example.com:8443/": {Scheme: "https", Host: "api.example.com", Port: 8443},
		"http://plain.example.com":      {Scheme: "http", Host: "plain.example.com", Port: 80},
		"HTTP://Plain.Example.com:8080": {Scheme: "http", Host: "plain.example.com", Port: 8080},
		"*.example.com:8443":            {Scheme: "https", Host: "*.example.com", Port: 8443, Wildcard: true},
		"http://*.example.com:8080":     {Scheme: "http", Host: "*.example.com", Port: 8080, Wildcard: true},
	}
	for entry, want := range cases {
		got, err := NormalizeEgressAllowEntry(entry)
		if err != nil || got != want {
			t.Errorf("NormalizeEgressAllowEntry(%q) = %+v, %v; want %+v", entry, got, err, want)
		}
	}
	if got, err := NormalizeEgressAllowEntry("   "); !got.IsZero() || err != nil {
		t.Errorf("NormalizeEgressAllowEntry(blank) = %+v, %v; want the zero entry without error", got, err)
	}
	if p := (EgressAllowEntry{Scheme: "http"}).Protocol(); p != EgressProtocolHTTP {
		t.Errorf("http entry Protocol() = %q", p)
	}
	if p := (EgressAllowEntry{Scheme: "https"}).Protocol(); p != EgressProtocolTLSPassthrough {
		t.Errorf("https entry Protocol() = %q", p)
	}
}

// Design review r3 (R3-4) list, verbatim: each refused, and
// *.example.com:8443 accepted.
func TestNormalizeEgressAllowEntry_R3List(t *testing.T) {
	for entry, reason := range map[string]string{
		"host:abc":          "invalid port",
		"http://host#f":     "fragment",
		"https://user@host": "userinfo",
		"https://host/path": "path",
		"*:443":             "catch-all",
		"http://*":          "catch-all",
		"*.com:443":         "itself a public suffix",
		"localhost:8080":    "single-label",
		"ftp://host":        "unsupported scheme",
		"tcp://host:22":     "unsupported scheme",
	} {
		assertNormalizeRefused(t, entry, reason)
	}
	if _, err := NormalizeEgressAllowEntry("*.example.com:8443"); err != nil {
		t.Errorf("NormalizeEgressAllowEntry(*.example.com:8443) error = %v, want accepted", err)
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

// ParseEgressAllow returns the entries and refuses one host and port
// on two protocols, deterministically naming both.
func TestParseEgressAllow_TargetsAndProtocolConflict(t *testing.T) {
	got, err := ParseEgressAllow([]string{"svc.example.com:8080", "http://svc.example.com:8081", "", "*.example.com"})
	if err != nil {
		t.Fatalf("ParseEgressAllow() error = %v", err)
	}
	want := []EgressAllowEntry{
		{Scheme: "https", Host: "svc.example.com", Port: 8080},
		{Scheme: "http", Host: "svc.example.com", Port: 8081},
		{Scheme: "https", Host: "*.example.com", Port: 443, Wildcard: true},
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

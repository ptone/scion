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

package logging

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestRedactQuery(t *testing.T) {
	cases := map[string]string{
		"":                                 "",
		"raw=1&version=1.0.0":              "raw=1&version=1.0.0",
		"raw=1&version=1.0.0&exp=5&sig=AB": "raw=1&version=1.0.0&exp=5&sig=REDACTED",
		"sig=AB&raw=1":                     "sig=REDACTED&raw=1",
		"sig=AB&sig=CD":                    "sig=REDACTED&sig=REDACTED",
		"%73ig=AB":                         "%73ig=REDACTED",
		"SIG=AB":                           "SIG=REDACTED",
		"sig":                              "sig",
		"sig=":                             "sig=REDACTED",
		"signature=keep&xsig=keep":         "signature=keep&xsig=keep",
		"%zz=AB&sig=CD":                    "REDACTED",
	}
	for in, want := range cases {
		if got := RedactQuery(in); got != want {
			t.Errorf("RedactQuery(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestRedactURL(t *testing.T) {
	u, err := url.Parse("https://hub.example.com/api/v1/skills/x/files/SKILL.md?raw=1&exp=5&sig=SECRETSIG")
	if err != nil {
		t.Fatal(err)
	}
	got := RedactURL(u)
	if strings.Contains(got, "SECRETSIG") {
		t.Fatalf("signature leaked: %s", got)
	}
	if want := "https://hub.example.com/api/v1/skills/x/files/SKILL.md?raw=1&exp=5&sig=REDACTED"; got != want {
		t.Fatalf("RedactURL = %q, want %q", got, want)
	}
	if u.RawQuery != "raw=1&exp=5&sig=SECRETSIG" {
		t.Fatalf("input URL was modified: %q", u.RawQuery)
	}
	if RedactURL(nil) != "" {
		t.Fatal("RedactURL(nil) should be empty")
	}
}

func TestRequestLogMiddleware_RedactsSignature(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	handler := RequestLogMiddleware(logger, "hub", HubPathPatterns(), 0)(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }))
	req := httptest.NewRequest(http.MethodGet, "/api/v1/skills/x/files/SKILL.md?raw=1&version=1.0.0&exp=5&sig=SECRETSIG", nil)
	handler.ServeHTTP(httptest.NewRecorder(), req)
	out := buf.String()
	if strings.Contains(out, "SECRETSIG") {
		t.Fatalf("request log leaked the signature: %s", out)
	}
	if !strings.Contains(out, "sig=REDACTED") {
		t.Fatalf("request log missing redacted URL: %s", out)
	}
}

// artifactPathCases are spellings of artifact credential paths (and a few
// other artifact paths); every one must be redacted on every sink.
var artifactPathCases = []string{
	"/api/v1/artifacts/shared/TOKEN",
	"/api/v1/artifacts/shared/TOKEN/files/a/b.png",
	"/api/v1/artifacts/view/TOKEN/index.html",
	"/api/v1/artifacts/shared/../shared/TOKEN",
	"/api/v1//artifacts/shared/TOKEN",
	"/api/v1/artifacts/x/../shared/TOKEN",
	"/api/v1/artifacts/%73hared/TOKEN",
	"/api/v1/artifacts/a%2Fb/../shared/TOKEN",
	"/api/v1/artifacts/a%2fb/..%2fview%2fTOKEN",
	"/api/v1/artifacts/%2e%2e/../shared/TOKEN",
	"/api/v1/artifacts/%2E%2E/../shared/TOKEN",
	"/api/v1/artifacts/.%2e/../shared/TOKEN",
	"/api/v1/%61rtifacts/shared/TOKEN",
	"/api/v1/%2561rtifacts/shared/TOKEN",
	"/API/V1/ARTIFACTS/SHARED/TOKEN",
	"/api/v1/artifacts/00000000-0000-4000-8000-000000000001/files/TOKEN.md",
}

// TestArtifactPathsRedactedOnEverySink: for every spelling, no part of the
// token survives in RequestPath, RedactURL or the trace predicate, whether
// the request arrives with the spelling as its raw path or parsed.
func TestArtifactPathsRedactedOnEverySink(t *testing.T) {
	const tok = "AbCdEfGhIjKlMnOpQrStUvWxYz0123456789-_AbCd"
	for _, c := range artifactPathCases {
		raw := strings.ReplaceAll(c, "TOKEN", tok)
		u, err := url.Parse("https://hub.example" + raw)
		if err != nil {
			t.Fatalf("%s: %v", raw, err)
		}
		r := httptest.NewRequest(http.MethodGet, raw, nil)
		if got := RequestPath(r); strings.Contains(got, tok) || got != RedactedArtifactPath {
			t.Errorf("RequestPath(%s) = %q", raw, got)
		}
		if got := RedactURL(u); strings.Contains(got, tok) {
			t.Errorf("RedactURL(%s) = %q", raw, got)
		}
		if !IsCredentialURL(u) || !IsCredentialURL(r.URL) {
			t.Errorf("IsCredentialURL(%s) = false", raw)
		}
	}
}

// TestNonArtifactPathsKept: other request paths are logged as they are.
func TestNonArtifactPathsKept(t *testing.T) {
	for _, p := range []string{"/api/v1/agents/a1", "/api/v1/projects/p/shared-dirs", "/", "/healthz"} {
		r := httptest.NewRequest(http.MethodGet, p, nil)
		if got := RequestPath(r); got != p {
			t.Errorf("RequestPath(%s) = %q", p, got)
		}
		if IsCredentialURL(r.URL) {
			t.Errorf("IsCredentialURL(%s) = true", p)
		}
	}
	if RequestPath(nil) != "" {
		t.Errorf("RequestPath(nil)")
	}
}

func TestRedactURLsInText(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		{"no URL", "connection refused", "connection refused"},
		{"URL without credentials", `Get "https://example.com/api/v1/agents": EOF`, `Get "https://example.com/api/v1/agents": EOF`},
		{
			"username-only token and query token in a url.Error",
			`Get "https://ghp_tok@github.com/o/r.git?access_token=abc&page=2": dial tcp: i/o timeout`,
			`Get "https://REDACTED@github.com/o/r.git?access_token=REDACTED&page=REDACTED": dial tcp: i/o timeout`,
		},
		{"password already masked by net/http", `Get "https://user:***@host/p": EOF`, `Get "https://REDACTED@host/p": EOF`},
		{
			"unparseable URL in a url.Parse error",
			`parse "https://alice:hunter2@host/%zz": invalid URL escape "%zz"`,
			`parse "https://REDACTED": invalid URL escape "%zz"`,
		},
		{
			"unquoted URL followed by a colon",
			"git clone https://x:tok@h/r.git: exit status 128",
			"git clone https://REDACTED@h/r.git: exit status 128",
		},
		{"fragment", "redirect to https://h/cb#access_token=xyz failed", "redirect to https://h/cb#REDACTED failed"},
		{"bare query token", "fetch https://h/p?ghp_secret failed", "fetch https://h/p?REDACTED failed"},
		{
			"database DSN",
			"open postgres://app:s3cret@db:5432/scion?sslmode=disable: refused",
			"open postgres://REDACTED@db:5432/scion?sslmode=REDACTED: refused",
		},
		{
			"git single-quoted URL",
			"fatal: unable to access 'https://tok@github.com/o/r.git/': The requested URL returned error: 403",
			"fatal: unable to access 'https://REDACTED@github.com/o/r.git/': The requested URL returned error: 403",
		},
		{"single quote in password", `Get "https://u:p'ss@h/x": EOF`, `Get "https://REDACTED@h/x": EOF`},
		{
			"single quote in password of an unparseable URL",
			`parse "https://u:p'ss@h/%zz": invalid URL escape "%zz"`,
			`parse "https://REDACTED": invalid URL escape "%zz"`,
		},
		{"single quote in query value", "fetch https://h/cb?token=ab'cd failed", "fetch https://h/cb?token=REDACTED failed"},
		{"URL followed by a closing single quote", "open 'https://h/p?k=v'", "open 'https://h/p?k=REDACTED'"},
		{
			"escaped double quote in password",
			`parse "https://u:p\"ss@h/%zz": invalid URL escape "%zz"`,
			`parse "https://REDACTED": invalid URL escape "%zz"`,
		},
		{
			"escaped double quote in password before a space",
			`parse "https://u:p\"a ss@h/%zz": invalid URL escape "%zz"`,
			`parse "https://REDACTED": invalid URL escape "%zz"`,
		},
		{"escaped double quote in query value", `Get "https://h/cb?t=ab\"cd": EOF`, `Get "https://REDACTED": EOF`},
		{
			"escaped double quote in path before the query",
			`parse "https://h/pa\"th%zz?token=abc": invalid URL escape "%zz"`,
			`parse "https://REDACTED": invalid URL escape "%zz"`,
		},
		{"second escaped double quote before the closing quote", `Get "https://h/p?t=a\"b\"c": EOF`, `Get "https://REDACTED": EOF`},
		{"escaped double quote without a closing quote", `x "https://h/p\"rest of text`, `x "https://REDACTED`},
		{"escaped backslash before the closing quote", `Get "https://h/p?t=a\\": EOF`, `Get "https://h/p?t=REDACTED": EOF`},
		{"URLs joined by quotes", `'https://h/x','https://u:p@h2/'`, `'https://h/x','https://REDACTED@h2/'`},
		{"URL nested in a path", "via https://proxy/https://u:p@h/x", "via https://proxy/https://REDACTED@h/x"},
		{
			"URL nested in a query",
			`Get "https://idp/authorize?redirect_uri=https://app/cb&client_secret=S": EOF`,
			`Get "https://idp/authorize?redirect_uri=REDACTED&client_secret=REDACTED": EOF`,
		},
		{"URL nested in a fragment", "go https://h/p#next=https://u:p@h2/x", "go https://h/p#REDACTED"},
		{"nested scheme starts at its first letter", "https://h/1a://u:p@z", "https://h/1a://REDACTED@z"},
		{"URL nested after an empty scheme", "see https://h/x/://y/https://u:p@z", "see https://h/x/://y/https://REDACTED@z"},
		{"backtick trimmed from a URL with a query", "see `https://h/x?t=a` now", "see `https://h/x?t=REDACTED` now"},
		{"trailing punctuation kept outside the URL", "(see https://h/p?t=abc).", "(see https://h/p?t=REDACTED)."},
		{"bare IPv6 host unchanged", `Get "http://[fd00::1]": EOF`, `Get "http://[fd00::1]": EOF`},
		{"IPv6 host with userinfo", `Get "http://u:p@[fd00::1]": EOF`, `Get "http://REDACTED@[fd00::1]": EOF`},
		{"backticks unchanged", "see `https://h/x` for details", "see `https://h/x` for details"},
		{"uppercase scheme and non-ASCII unchanged", `Get "HTTPS://Example.com/Ünï": EOF`, `Get "HTTPS://Example.com/Ünï": EOF`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := redactURLsInText(tc.in); got != tc.want {
				t.Errorf("redactURLsInText(%q)\n got %q\nwant %q", tc.in, got, tc.want)
			}
		})
	}

	// A long run of adjacent URLs is cut into 5000 parts. The scan is
	// linear; a quadratic one would take on the order of 10^8 steps here.
	t.Run("long run of adjacent URLs", func(t *testing.T) {
		run := strings.Repeat("a://", 5000)
		in := run + "https://u:p@h/"
		want := run + "https://REDACTED@h/"
		if got := redactURLsInText(in); got != want {
			t.Errorf("redactURLsInText(long run): got %d bytes ending %q, want %d bytes ending %q",
				len(got), got[max(0, len(got)-40):], len(want), want[len(want)-40:])
		}
	})
}

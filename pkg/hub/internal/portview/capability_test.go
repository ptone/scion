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

package portview

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

const (
	testAgent = "0b6f2c1e-4a8d-4f5e-9c3b-2d1e0f9a8b7c"
	testUser  = "7d3e9f10-2b4c-4d6e-8f01-a2b3c4d5e6f7"
	testTTL   = 2 * time.Hour
)

var (
	testKey  = []byte("0123456789abcdef0123456789abcdef")
	otherKey = []byte("fedcba9876543210fedcba9876543210")
	testNow  = time.Unix(1_800_000_000, 0)
)

func mintAt(exp time.Time) string {
	return Mint(testKey, testAgent, 8080, testUser, exp)
}

// sign builds a capability from raw field strings, signed over exactly
// those strings with key, so encodings Mint never produces can be tried
// with a valid signature.
func sign(key []byte, ver, agent, port, user, exp string) string {
	m := hmac.New(sha256.New, key)
	m.Write([]byte("port-view\n" + agent + "\n" + port + "\n" + user + "\n" + exp))
	return ver + "." + agent + "." + port + "." + user + "." + exp + "." +
		base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

func TestMintParseRoundTrip(t *testing.T) {
	exp := testNow.Add(testTTL)
	c := mintAt(exp)
	if !strings.HasPrefix(c, "v1."+testAgent+".8080."+testUser+"."+strconv.FormatInt(exp.Unix(), 10)+".") {
		t.Fatalf("capability layout = %q", c)
	}
	g, err := Parse(testKey, c, testNow, testTTL)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	want := Grant{AgentID: testAgent, Port: 8080, UserID: testUser, Expires: time.Unix(exp.Unix(), 0)}
	if g != want {
		t.Fatalf("grant = %+v, want %+v", g, want)
	}
	// The signature is over the documented message.
	if sign(testKey, "v1", testAgent, "8080", testUser, strconv.FormatInt(exp.Unix(), 10)) != c {
		t.Fatal("Mint does not sign the documented message")
	}
}

func TestParseRefuses(t *testing.T) {
	exp := testNow.Add(time.Hour)
	e := strconv.FormatInt(exp.Unix(), 10)
	good := mintAt(exp)
	parts := strings.Split(good, ".")
	with := func(i int, v string) string {
		p := append([]string(nil), parts...)
		p[i] = v
		return strings.Join(p, ".")
	}
	// A valid signature over a non-canonical field still fails.
	cases := map[string]string{
		"empty":                   "",
		"garbage":                 "not-a-capability",
		"five fields":             strings.Join(parts[:5], "."),
		"seven fields":            good + ".x",
		"version v2":              sign(testKey, "v2", testAgent, "8080", testUser, e),
		"version V1":              sign(testKey, "V1", testAgent, "8080", testUser, e),
		"agent upper case":        sign(testKey, "v1", strings.ToUpper(testAgent), "8080", testUser, e),
		"agent without hyphens":   sign(testKey, "v1", strings.ReplaceAll(testAgent, "-", ""), "8080", testUser, e),
		"agent braces":            sign(testKey, "v1", "{"+testAgent+"}", "8080", testUser, e),
		"agent urn":               sign(testKey, "v1", "urn:uuid:"+testAgent, "8080", testUser, e),
		"agent not a uuid":        sign(testKey, "v1", "my-agent", "8080", testUser, e),
		"user upper case":         sign(testKey, "v1", testAgent, "8080", strings.ToUpper(testUser), e),
		"user empty":              sign(testKey, "v1", testAgent, "8080", "", e),
		"port leading zero":       sign(testKey, "v1", testAgent, "08080", testUser, e),
		"port plus sign":          sign(testKey, "v1", testAgent, "+8080", testUser, e),
		"port zero":               sign(testKey, "v1", testAgent, "0", testUser, e),
		"port too large":          sign(testKey, "v1", testAgent, "65536", testUser, e),
		"port negative":           sign(testKey, "v1", testAgent, "-1", testUser, e),
		"port hex":                sign(testKey, "v1", testAgent, "0x1f90", testUser, e),
		"exp leading zero":        sign(testKey, "v1", testAgent, "8080", testUser, "0"+e),
		"exp plus sign":           sign(testKey, "v1", testAgent, "8080", testUser, "+"+e),
		"exp zero":                sign(testKey, "v1", testAgent, "8080", testUser, "0"),
		"exp fraction":            sign(testKey, "v1", testAgent, "8080", testUser, e+".5"),
		"tampered agent":          with(1, "1b6f2c1e-4a8d-4f5e-9c3b-2d1e0f9a8b7c"),
		"tampered port":           with(2, "8081"),
		"tampered user":           with(3, "8d3e9f10-2b4c-4d6e-8f01-a2b3c4d5e6f7"),
		"tampered exp":            with(4, strconv.FormatInt(exp.Unix()+1, 10)),
		"tampered sig":            with(5, flipFirst(parts[5])),
		"sig padded":              good + "=",
		"sig std alphabet":        with(5, strings.NewReplacer("-", "+", "_", "/").Replace(parts[5]) + "+/"),
		"sig empty":               with(5, ""),
		"sig non-canonical tail":  with(5, nonCanonicalTail(parts[5])),
		"wrong key":               Mint(otherKey, testAgent, 8080, testUser, exp),
		"artifact view shape":     artifactViewShaped(exp),
		"artifact view signature": with(5, artifactViewSig(exp)),
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			if g, err := Parse(testKey, c, testNow, testTTL); !errors.Is(err, ErrInvalid) || g != (Grant{}) {
				t.Fatalf("Parse(%q) = %+v, %v; want ErrInvalid and a zero grant", c, g, err)
			}
		})
	}
}

func TestParseExpiry(t *testing.T) {
	cases := []struct {
		name string
		exp  time.Time
		ok   bool
	}{
		{"one second left", testNow.Add(time.Second), true},
		{"full lifetime", testNow.Add(testTTL), true},
		{"lifetime plus skew", testNow.Add(testTTL + MaxSkew), true},
		{"exactly now", testNow, false},
		{"expired", testNow.Add(-time.Second), false},
		{"long expired", testNow.Add(-48 * time.Hour), false},
		{"beyond lifetime plus skew", testNow.Add(testTTL + MaxSkew + time.Second), false},
		{"far future", testNow.Add(365 * 24 * time.Hour), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse(testKey, mintAt(tc.exp), testNow, testTTL)
			if (err == nil) != tc.ok {
				t.Fatalf("Parse err = %v, want ok=%v", err, tc.ok)
			}
		})
	}
}

func TestParseEmptyKey(t *testing.T) {
	c := Mint(nil, testAgent, 8080, testUser, testNow.Add(time.Hour))
	if _, err := Parse(nil, c, testNow, testTTL); !errors.Is(err, ErrInvalid) {
		t.Fatalf("Parse with no key: %v, want ErrInvalid", err)
	}
}

func TestTarget(t *testing.T) {
	good := mintAt(testNow.Add(time.Hour))
	if a, p, ok := Target(good); !ok || a != testAgent || p != 8080 {
		t.Fatalf("Target(valid) = %q, %d, %v", a, p, ok)
	}
	// The signature and expiry are not checked.
	expired := Mint(otherKey, testAgent, 9000, testUser, testNow.Add(-time.Hour))
	if a, p, ok := Target(expired); !ok || a != testAgent || p != 9000 {
		t.Fatalf("Target(expired, other key) = %q, %d, %v", a, p, ok)
	}
	for _, c := range []string{
		"", "v1", "v1.x.8080.y.1.sig",
		strings.Replace(good, "v1.", "v2.", 1),
		strings.Replace(good, ".8080.", ".08080.", 1),
		strings.Replace(good, ".8080.", ".0.", 1),
		strings.Replace(good, ".8080.", ".70000.", 1),
		good + ".extra",
	} {
		if _, _, ok := Target(c); ok {
			t.Errorf("Target(%q) ok, want refused", c)
		}
	}
}

func TestIsNavigation(t *testing.T) {
	cases := []struct {
		mode, accept string
		want         bool
	}{
		{"navigate", "", true},
		{"navigate", "*/*", true},
		{"cors", "text/html", false},
		{"no-cors", "text/html,*/*", false},
		{"websocket", "", false},
		{"same-origin", "text/html", false},
		{"", "text/html,application/xhtml+xml", true},
		{"", "application/json", false},
		{"", "", false},
	}
	for _, tc := range cases {
		r := httptest.NewRequest("GET", "/x", nil)
		if tc.mode != "" {
			r.Header.Set("Sec-Fetch-Mode", tc.mode)
		}
		if tc.accept != "" {
			r.Header.Set("Accept", tc.accept)
		}
		if got := IsNavigation(r); got != tc.want {
			t.Errorf("IsNavigation(mode=%q, accept=%q) = %v, want %v", tc.mode, tc.accept, got, tc.want)
		}
	}
}

func TestPreflightMethod(t *testing.T) {
	for _, m := range []string{"GET", "HEAD", "POST", "PUT", "PATCH", "DELETE"} {
		if got := PreflightMethod(m); got != m {
			t.Errorf("PreflightMethod(%q) = %q", m, got)
		}
	}
	for _, m := range []string{"", "OPTIONS", "CONNECT", "TRACE", "get", "PROPFIND", "GET, POST"} {
		if got := PreflightMethod(m); got != "" {
			t.Errorf("PreflightMethod(%q) = %q, want none", m, got)
		}
	}
}

func TestPreflightHeaders(t *testing.T) {
	cases := map[string]string{
		"":                                    "",
		"content-type":                        "content-type",
		"Content-Type, X-App":                 "content-type, x-app",
		"authorization":                       "",
		"Authorization, content-type":         "content-type",
		"cookie,COOKIE, x-a":                  "x-a",
		" x-a , , x-b ":                       "x-a, x-b",
		"x-a\r\nset-cookie: a=b":              "",
		"x a, x-b":                            "x-b",
		"x-a\x00, x-b":                        "x-b",
		"x-app-key, Authorization, x-request": "x-app-key, x-request",
	}
	for in, want := range cases {
		if got := PreflightHeaders(in); got != want {
			t.Errorf("PreflightHeaders(%q) = %q, want %q", in, got, want)
		}
	}
}

func flipFirst(s string) string {
	if s[0] == 'A' {
		return "B" + s[1:]
	}
	return "A" + s[1:]
}

// nonCanonicalTail changes the unused low bits of the last signature
// character, which a lenient decoder would accept as the same bytes.
func nonCanonicalTail(s string) string {
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"
	last := strings.IndexByte(alphabet, s[len(s)-1])
	// 32 bytes encode to 43 characters; the last carries 4 bits and 2
	// unused ones.
	return s[:len(s)-1] + string(alphabet[last^1])
}

// artifactViewShaped is a capability in the artifact view layout
// ("<id>.<seq>.<exp>.<sig>"), signed with the artifact domain.
func artifactViewShaped(exp time.Time) string {
	e := strconv.FormatInt(exp.Unix(), 10)
	return testAgent + ".1." + e + "." + artifactViewSigFor(testAgent+"\n1\n"+e)
}

// artifactViewSig signs this test's agent and expiry with the artifact
// view domain instead of the port view one.
func artifactViewSig(exp time.Time) string {
	e := strconv.FormatInt(exp.Unix(), 10)
	return artifactViewSigFor(testAgent + "\n8080\n" + testUser + "\n" + e)
}

func artifactViewSigFor(rest string) string {
	m := hmac.New(sha256.New, testKey)
	m.Write([]byte("artifact-view\n" + rest))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

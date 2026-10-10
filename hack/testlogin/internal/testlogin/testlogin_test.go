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

package testlogin

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"

	"github.com/GoogleCloudPlatform/scion/hack/testlogin/internal/challenge"
)

// fakeHub implements just enough of the hub API for the tool: the test-login
// endpoint (validating the challenge the same way the hub does), /auth/me,
// the user lookup and one admin-only endpoint. Every behaviour the tool
// checks can be bent per test.
type fakeHub struct {
	t   *testing.T
	key []byte

	enabled       bool
	respRole      string        // role in the test-login response and token
	meRole        string        // role reported by /auth/me
	meEmail       string        // email override for /auth/me
	adminStatus   int           // status of the admin-only endpoint
	createdOffset time.Duration // created = login time + offset
	lastLoginOff  time.Duration // lastLogin = login time + offset
	tokenLife     time.Duration
	userStatus    int
	legacy        bool   // an older hub: ignores createOnly, omits "created"
	exists        bool   // the requested email already belongs to a user
	forceCreated  *bool  // if set, report this "created" value regardless
	rateLimitFrom int    // answer test-login call number N and later with 429 (0: off)
	retryAfter    string // Retry-After sent with 429
	uidSuffix     string // appended to generated user ids

	// Fields below change while the fake serves requests; mu guards them.
	mu            sync.Mutex
	requests      int
	loginCalls    int  // test-login calls received, preflight included
	userLookups   int  // GET /api/v1/users/<id> calls
	createOnly    bool // createOnly in the last authenticated request
	authedLogins  int
	createdUsers  int
	requestedRole string
	email         string
	displayName   string
	uid           string
	access        string
	refresh       string
	issued        []string // every token the fake has handed out
	loginAt       time.Time
}

func newFakeHub(t *testing.T, secret []byte) (*fakeHub, *httptest.Server) {
	t.Helper()
	f := &fakeHub{
		t:           t,
		key:         challenge.DeriveUserSigningKey(secret),
		enabled:     true,
		respRole:    "member",
		meRole:      "member",
		adminStatus: http.StatusForbidden,
		tokenLife:   15 * time.Minute,
		userStatus:  http.StatusOK,
	}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	return f, srv
}

func writeErr(w http.ResponseWriter, status int, code string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"code": code, "message": code}})
}

func (f *fakeHub) bearer(r *http.Request) string {
	return strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
}

func (f *fakeHub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests++
	switch {
	case r.URL.Path == pathTestLogin && r.Method == http.MethodPost:
		f.testLogin(w, r)
	case r.URL.Path == pathAuthMe:
		if f.access == "" || f.bearer(r) != f.access {
			writeErr(w, http.StatusUnauthorized, "user_not_found")
			return
		}
		email := f.email
		if f.meEmail != "" {
			email = f.meEmail
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"id": f.uid, "email": email, "role": f.meRole})
	case strings.HasPrefix(r.URL.Path, pathUsers):
		f.userLookups++
		if f.bearer(r) != f.access {
			writeErr(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		if f.userStatus != http.StatusOK {
			writeErr(w, f.userStatus, "forbidden")
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": f.uid, "email": f.email, "role": f.respRole,
			"created":   f.loginAt.Add(f.createdOffset),
			"lastLogin": f.loginAt.Add(f.lastLoginOff),
		})
	case r.URL.Path == pathAdminConfig:
		if f.adminStatus == http.StatusOK {
			_, _ = w.Write([]byte(`{}`))
			return
		}
		writeErr(w, f.adminStatus, "forbidden")
	default:
		writeErr(w, http.StatusNotFound, "not_found")
	}
}

func (f *fakeHub) testLogin(w http.ResponseWriter, r *http.Request) {
	if !f.enabled {
		writeErr(w, http.StatusForbidden, "forbidden")
		return
	}
	f.loginCalls++
	if f.rateLimitFrom > 0 && f.loginCalls >= f.rateLimitFrom {
		if f.retryAfter != "" {
			w.Header().Set("Retry-After", f.retryAfter)
		}
		writeErr(w, http.StatusTooManyRequests, "rate_limited")
		return
	}
	parts := strings.Fields(r.Header.Get("Authorization"))
	if len(parts) != 2 || !strings.EqualFold(parts[0], "bearer") {
		writeErr(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	tok, err := jwt.ParseSigned(parts[1], []jose.SignatureAlgorithm{jose.HS256})
	if err != nil {
		writeErr(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	var c jwt.Claims
	if err := tok.Claims(f.key, &c); err != nil || c.Expiry == nil ||
		c.Validate(jwt.Expected{Issuer: "scion-hub", AnyAudience: jwt.Audience{"scion-test-login"}, Time: time.Now()}) != nil {
		writeErr(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	if life := c.Expiry.Time().Sub(c.IssuedAt.Time()); life > 5*time.Minute {
		f.t.Errorf("challenge lifetime %v exceeds 5m", life)
	}
	f.authedLogins++
	var req struct {
		Email, Role, DisplayName string
		CreateOnly               bool `json:"createOnly"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	f.requestedRole, f.email, f.displayName, f.createOnly = req.Role, req.Email, req.DisplayName, req.CreateOnly
	if f.exists && req.CreateOnly && !f.legacy {
		writeErr(w, http.StatusConflict, "conflict")
		return
	}
	created := !f.exists
	if f.forceCreated != nil {
		created = *f.forceCreated
	}
	if created {
		f.createdUsers++
	}
	f.uid = "uid-" + randHex(f.t, 8) + f.uidSuffix
	f.loginAt = time.Now()
	f.access = f.mintToken("access", f.tokenLife)
	f.refresh = f.mintToken("refresh", 7*24*time.Hour)
	f.issued = append(f.issued, f.access, f.refresh)
	http.SetCookie(w, &http.Cookie{Name: "scion_sess", Value: f.refresh})
	resp := map[string]any{
		"user":         map[string]string{"id": f.uid, "email": f.email, "displayName": f.displayName, "role": f.respRole},
		"accessToken":  f.access,
		"refreshToken": f.refresh,
		"expiresIn":    int64(f.tokenLife.Seconds()),
	}
	if !f.legacy {
		resp["created"] = created
	}
	_ = json.NewEncoder(w).Encode(resp)
}

func (f *fakeHub) mintToken(typ string, life time.Duration) string {
	return f.mintCustom(map[string]any{"type": typ, "exp": time.Now().Add(life).Unix()})
}

// mintCustom signs a token with the fake's key, starting from a valid
// testlogin access-token claim set and applying overrides.
func (f *fakeHub) mintCustom(overrides map[string]any) string {
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.HS256, Key: f.key}, nil)
	if err != nil {
		f.t.Fatal(err)
	}
	now := time.Now()
	uid, email := f.uid, f.email
	if uid == "" {
		uid, email = "uid-"+randHex(f.t, 8), "test-fixture-"+randHex(f.t, 8)+"@"+EmailDomain
	}
	claims := map[string]any{
		"iss": "scion-hub", "aud": "scion-hub-api", "sub": uid, "uid": uid,
		"email": email, "role": f.respRole, "type": "access", "client": "web",
		"iat": now.Unix(), "nbf": now.Unix(), "exp": now.Add(f.tokenLife).Unix(), "jti": randHex(f.t, 8),
	}
	for k, v := range overrides {
		claims[k] = v
	}
	s, err := jwt.Signed(signer).Claims(claims).Serialize()
	if err != nil {
		f.t.Fatal(err)
	}
	return s
}

func randHex(t *testing.T, n int) string {
	t.Helper()
	s, err := randomHex(n)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// env is one test's setup: a throwaway secret in a 0600 file, a fake hub
// and a private output directory.
type env struct {
	secret     []byte
	secretFile string
	dir        string
	out        string
	hub        *fakeHub
	srv        *httptest.Server
}

func newEnv(t *testing.T) *env {
	t.Helper()
	dir := t.TempDir()
	secret := []byte("s3cr3t-" + randHex(t, 16))
	sf := filepath.Join(dir, "hub.env")
	content := "# hub environment\nOTHER=1\nSESSION_SECRET=\"" + string(secret) + "\"\n"
	if err := os.WriteFile(sf, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	hub, srv := newFakeHub(t, secret)
	return &env{secret: secret, secretFile: sf, dir: dir, out: filepath.Join(dir, "token"), hub: hub, srv: srv}
}

// run is the only place the tests invoke Run (TestEveryRunIsChecked
// enforces this), so the secret-absence check covers every run.
func (e *env) run(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), args, &stdout, &stderr, Options{})
	e.assertNoSecrets(t, stdout.String()+stderr.String())
	return code, stdout.String(), stderr.String()
}

func (e *env) mint(t *testing.T, extra ...string) (int, string, string) {
	t.Helper()
	return e.run(t, append([]string{"mint", "--hub-url", e.srv.URL, "--secret-file", e.secretFile, "--out", e.out}, extra...)...)
}

func (e *env) cleanup(t *testing.T, tokenFile string) (int, string, string) {
	t.Helper()
	return e.run(t, "cleanup", "--hub-url", e.srv.URL, "--token-file", tokenFile)
}

// assertNoSecrets fails if any secret material appears in output: the
// session secret, the derived key in common encodings, and both tokens.
func (e *env) assertNoSecrets(t *testing.T, output string) {
	t.Helper()
	key := challenge.DeriveUserSigningKey(e.secret)
	forbidden := map[string]string{
		"session secret": string(e.secret),
		"key hex":        hex.EncodeToString(key),
		"key base64":     base64.StdEncoding.EncodeToString(key),
		"key base64url":  base64.RawURLEncoding.EncodeToString(key),
	}
	e.hub.mu.Lock()
	for i, tok := range e.hub.issued {
		forbidden["issued token "+string(rune('0'+i))] = tok
		if sig := tok[strings.LastIndex(tok, ".")+1:]; len(sig) >= 16 {
			forbidden["issued token signature "+string(rune('0'+i))] = sig
		}
	}
	e.hub.mu.Unlock()
	for name, v := range forbidden {
		if v != "" && strings.Contains(output, v) {
			t.Errorf("%s appears in output", name)
		}
	}
}

func assertGone(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Errorf("%s still exists (err=%v)", path, err)
	}
}

func TestMintHappyPath(t *testing.T) {
	e := newEnv(t)
	code, stdout, stderr := e.mint(t)
	if code != 0 {
		t.Fatalf("exit %d, stderr:\n%s", code, stderr)
	}
	st, err := os.Stat(e.out)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Errorf("token file mode %04o, want 0600", st.Mode().Perm())
	}
	data, err := os.ReadFile(e.out)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != e.hub.access {
		t.Error("token file does not hold exactly the access token")
	}
	if strings.Contains(string(data), e.hub.refresh) {
		t.Error("refresh token written to the token file")
	}
	if e.hub.requestedRole != "member" {
		t.Errorf("requested role %q, want member", e.hub.requestedRole)
	}
	if !strings.HasPrefix(e.hub.email, "test-fixture-") || !strings.HasSuffix(e.hub.email, "@scion-test.invalid") {
		t.Errorf("unexpected generated email %q", e.hub.email)
	}
	if !strings.HasPrefix(e.hub.displayName, "test-fixture ") {
		t.Errorf("unexpected display name %q", e.hub.displayName)
	}
	if e.hub.createdUsers != 1 {
		t.Errorf("created %d users, want 1", e.hub.createdUsers)
	}
	for _, want := range []string{e.hub.uid, e.hub.email, e.out, "member"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout missing %q:\n%s", want, stdout)
		}
	}
}

func TestMintNamePrefix(t *testing.T) {
	e := newEnv(t)
	if code, _, stderr := e.mint(t, "--name-prefix", "edit-uat"); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	if !strings.HasPrefix(e.hub.email, "edit-uat-") || !strings.HasPrefix(e.hub.displayName, "edit-uat ") {
		t.Errorf("prefix not applied: %q / %q", e.hub.email, e.hub.displayName)
	}
}

func TestMintFailuresRemoveTokenFile(t *testing.T) {
	cases := []struct {
		name    string
		bend    func(*fakeHub)
		wantErr string
	}{
		{"response role admin", func(f *fakeHub) { f.respRole = "admin" }, `role "admin"`},
		{"response role viewer", func(f *fakeHub) { f.respRole = "viewer" }, `role "viewer"`},
		{"auth me role admin", func(f *fakeHub) { f.meRole = "admin" }, "/auth/me returned role"},
		{"auth me other email", func(f *fakeHub) { f.meEmail = "someone@example.com" }, "/auth/me returned email"},
		{"admin endpoint allowed", func(f *fakeHub) { f.adminStatus = http.StatusOK }, "want 403"},
		{"admin endpoint not found", func(f *fakeHub) { f.adminStatus = http.StatusNotFound }, "want 403"},
		{"created false", func(f *fakeHub) { f.forceCreated = new(bool) }, "created=false"},
		{"legacy hub, pre-existing user", func(f *fakeHub) { f.legacy = true; f.createdOffset = -time.Hour }, "existing account"},
		{"legacy hub, created in the future", func(f *fakeHub) { f.legacy = true; f.createdOffset = time.Hour; f.lastLoginOff = time.Hour }, "not created by this run"},
		{"legacy hub, lastLogin far from created", func(f *fakeHub) { f.legacy = true; f.lastLoginOff = time.Minute }, "not a newly created user"},
		{"legacy hub, user lookup forbidden", func(f *fakeHub) { f.legacy = true; f.userStatus = http.StatusForbidden }, "cannot confirm the user is new"},
		{"email exists (409)", func(f *fakeHub) { f.exists = true }, "already exists (409"},
		{"rate limited", func(f *fakeHub) { f.rateLimitFrom = 2; f.retryAfter = "7" }, "Retry-After: 7s"},
		{"token lifetime too long", func(f *fakeHub) { f.tokenLife = 31 * time.Minute }, "exceeds 30m"},
		{"test-login disabled", func(f *fakeHub) { f.enabled = false }, "not enabled"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			tc.bend(e.hub)
			code, _, stderr := e.mint(t)
			if code == 0 {
				t.Fatal("expected failure")
			}
			if !strings.Contains(stderr, tc.wantErr) {
				t.Errorf("stderr missing %q:\n%s", tc.wantErr, stderr)
			}
			assertGone(t, e.out)
		})
	}
}

// TestMintFailureAdvice checks that deletion is advised only for an account
// this run is confirmed to have created.
func TestMintFailureAdvice(t *testing.T) {
	cases := []struct {
		name      string
		bend      func(*fakeHub)
		want      string
		forbidden string
	}{
		{"confirmed new, later check fails", func(f *fakeHub) { f.adminStatus = http.StatusOK }, "this run created user", "do not delete"},
		{"hub reports created false", func(f *fakeHub) { f.forceCreated = new(bool) }, "existing account", "admin must delete"},
		{"legacy hub, existing account", func(f *fakeHub) { f.legacy = true; f.createdOffset = -time.Hour }, "existing account", "admin must delete"},
		{"legacy hub, freshness unconfirmed", func(f *fakeHub) { f.legacy = true; f.userStatus = http.StatusForbidden }, "could not confirm", "admin must delete"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			tc.bend(e.hub)
			code, _, stderr := e.mint(t)
			if code == 0 {
				t.Fatal("expected failure")
			}
			if !strings.Contains(stderr, tc.want) || !strings.Contains(stderr, e.hub.uid) {
				t.Errorf("stderr missing %q or the uid:\n%s", tc.want, stderr)
			}
			if strings.Contains(stderr, tc.forbidden) {
				t.Errorf("stderr contains %q:\n%s", tc.forbidden, stderr)
			}
		})
	}
}

func TestMintCreateOnly(t *testing.T) {
	e := newEnv(t)
	code, _, stderr := e.mint(t)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	if !e.hub.createOnly {
		t.Error("request did not set createOnly")
	}
	if e.hub.userLookups != 0 {
		t.Errorf("user lookups %d; with created=true the timestamp fallback must not run", e.hub.userLookups)
	}
	if strings.Contains(stderr, "does not support createOnly") {
		t.Errorf("fallback note printed for a hub that reports created:\n%s", stderr)
	}
}

func TestMintLegacyHubFallback(t *testing.T) {
	e := newEnv(t)
	e.hub.legacy = true
	code, _, stderr := e.mint(t)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	if n := strings.Count(stderr, "does not support createOnly"); n != 1 {
		t.Errorf("fallback note printed %d times, want 1:\n%s", n, stderr)
	}
	if e.hub.userLookups != 1 {
		t.Errorf("user lookups %d, want 1 (timestamp fallback)", e.hub.userLookups)
	}
}

func TestMintConflictWritesNothing(t *testing.T) {
	e := newEnv(t)
	e.hub.exists = true
	code, _, stderr := e.mint(t)
	if code == 0 {
		t.Fatal("expected failure")
	}
	if !strings.Contains(stderr, "already exists (409") || !strings.Contains(stderr, "nothing to clean up") {
		t.Errorf("stderr:\n%s", stderr)
	}
	for _, advice := range []string{"admin must", "review:"} {
		if strings.Contains(stderr, advice) {
			t.Errorf("stderr gives cleanup advice %q after a 409:\n%s", advice, stderr)
		}
	}
	if e.hub.createdUsers != 0 {
		t.Errorf("created %d users", e.hub.createdUsers)
	}
	assertGone(t, e.out)
}

func TestMintRateLimited(t *testing.T) {
	cases := []struct {
		name       string
		from       int
		retryAfter string
		want       string
	}{
		{"at preflight", 1, "3", "Retry-After: 3s"},
		{"at login", 2, "7", "Retry-After: 7s"},
		{"no Retry-After", 2, "", "Retry-After: not given"},
		{"HTTP date", 2, "Sat, 10 Oct 2026 21:00:00 GMT", "Retry-After: Sat, 10 Oct 2026 21:00:00 GMT"},
		{"oversized", 2, strings.Repeat("9", 41), "Retry-After: not given"},
		{"non-ASCII", 2, "5\u00e9", "Retry-After: not given"},
		{"non-ASCII date", 2, "Sat, 10 Oct 2026 21:00:00 GMT\u202e", "Retry-After: not given"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			e.hub.rateLimitFrom, e.hub.retryAfter = tc.from, tc.retryAfter
			code, _, stderr := e.mint(t)
			if code == 0 {
				t.Fatal("expected failure")
			}
			if !strings.Contains(stderr, "429") || !strings.Contains(stderr, tc.want) || !strings.Contains(stderr, "not retrying") {
				t.Errorf("stderr missing %q:\n%s", tc.want, stderr)
			}
			if e.hub.loginCalls != tc.from {
				t.Errorf("test-login calls %d, want %d (no retry)", e.hub.loginCalls, tc.from)
			}
			if e.hub.createdUsers != 0 {
				t.Errorf("created %d users", e.hub.createdUsers)
			}
			assertGone(t, e.out)
		})
	}
}

// TestRateLimitedCleansRetryAfter covers values Go's HTTP client would
// deliver only from a lenient transport: control characters must never be
// echoed.
func TestRateLimitedCleansRetryAfter(t *testing.T) {
	for _, v := range []string{"5\x01", "\x1b[2J5", "5\x7f", "5\u0085", "5\n6", strings.Repeat("1", 41), "7\u00e9"} {
		m := &minter{retryAfter: v}
		got := m.rateLimited().Error()
		if !strings.Contains(got, "Retry-After: not given") {
			t.Errorf("Retry-After %q printed as %q", v, got)
		}
	}
	for v, want := range map[string]string{"7": "Retry-After: 7s", " 12 ": "Retry-After: 12s", "Sat, 10 Oct 2026 21:00:00 GMT": "Retry-After: Sat, 10 Oct 2026 21:00:00 GMT"} {
		if got := (&minter{retryAfter: v}).rateLimited().Error(); !strings.Contains(got, want) {
			t.Errorf("Retry-After %q printed as %q, want %q", v, got, want)
		}
	}
}

// TestMintMalformedRetryAfterIsNotEchoed checks that a 429 whose Retry-After
// carries control characters (which Go's client rejects, quoting the line
// in its error) fails without retrying and prints no control characters.
func TestMintMalformedRetryAfterIsNotEchoed(t *testing.T) {
	for _, v := range []string{"5\x01", "\x1b[2J5"} {
		e := newEnv(t)
		e.hub.rateLimitFrom, e.hub.retryAfter = 2, v
		code, stdout, stderr := e.mint(t)
		if code == 0 {
			t.Fatal("expected failure")
		}
		assertPrintable(t, stdout+stderr)
		if e.hub.loginCalls != 2 {
			t.Errorf("test-login calls %d, want 2 (no retry)", e.hub.loginCalls)
		}
		assertGone(t, e.out)
	}
}

func assertPrintable(t *testing.T, s string) {
	t.Helper()
	for _, r := range s {
		if (r < 0x20 && r != '\n' && r != '\t') || (r >= 0x7f && r <= 0x9f) ||
			r == '\u2028' || r == '\u2029' || unicode.Is(unicode.Cf, r) {
			t.Errorf("output contains control character %U:\n%q", r, s)
			return
		}
	}
}

func TestPrintableWriter(t *testing.T) {
	var buf bytes.Buffer
	in := "ok\tline\n\x1b[31mred\x07 \u0085 \xff caf\u00e9\n"
	n, err := printableWriter{&buf}.Write([]byte(in))
	if err != nil || n != len(in) {
		t.Fatalf("Write = %d, %v; want %d, nil", n, err, len(in))
	}
	if want := "ok\tline\n?[31mred? ? ? caf\u00e9\n"; buf.String() != want {
		t.Errorf("got %q, want %q", buf.String(), want)
	}

	// Unicode format characters and line separators that can change how a
	// line displays: bidi embeddings, overrides and isolates, zero-width
	// characters, the BOM, the soft hyphen, and U+2028/U+2029.
	for _, r := range []rune{
		'\u202a', '\u202b', '\u202c', '\u202d', '\u202e',
		'\u2066', '\u2067', '\u2068', '\u2069',
		'\u200b', '\u200c', '\u200d', '\u200e', '\u200f', '\u2060', '\ufeff', '\u00ad',
		'\u2028', '\u2029',
	} {
		buf.Reset()
		in := "uid-1" + string(r) + "x"
		if _, err := (printableWriter{&buf}).Write([]byte(in)); err != nil {
			t.Fatal(err)
		}
		if buf.String() != "uid-1?x" {
			t.Errorf("%U: got %q, want %q", r, buf.String(), "uid-1?x")
		}
	}

	// Ordinary non-ASCII text is kept.
	buf.Reset()
	in = "Gr\u00fc\u00dfe \u65e5\u672c \u0645\u0631\u062d\u0628\u0627"
	if _, err := (printableWriter{&buf}).Write([]byte(in)); err != nil || buf.String() != in {
		t.Errorf("got %q, %v; want %q unchanged", buf.String(), err, in)
	}
}

// TestHubTextCannotSpoofOutput checks the end-to-end path: a user id from
// the hub carrying a bidi override is printed unquoted in the failure
// advice, and must reach the output only with the override replaced.
func TestHubTextCannotSpoofOutput(t *testing.T) {
	e := newEnv(t)
	e.hub.uidSuffix = "\u202eeteled-ton-od"
	e.hub.adminStatus = http.StatusOK // fail after the user is confirmed new
	code, stdout, stderr := e.mint(t)
	if code == 0 {
		t.Fatal("expected failure")
	}
	if !strings.Contains(stderr, "this run created user uid") {
		t.Fatalf("advice line missing:\n%s", stderr)
	}
	assertPrintable(t, stdout+stderr)
}

func TestMintDisabledCreatesNothing(t *testing.T) {
	e := newEnv(t)
	e.hub.enabled = false
	if code, _, _ := e.mint(t); code == 0 {
		t.Fatal("expected failure")
	}
	if e.hub.authedLogins != 0 || e.hub.createdUsers != 0 {
		t.Errorf("authed logins %d, users %d; want 0 and 0", e.hub.authedLogins, e.hub.createdUsers)
	}
	assertGone(t, e.out)
}

func TestPreflightCreatesNothing(t *testing.T) {
	e := newEnv(t)
	u, err := parseHubURL(e.srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	m := &minter{base: u, client: httpClient(nil), stdout: &stdout, stderr: &stderr}
	err = m.preflight(context.Background())
	e.assertNoSecrets(t, stdout.String()+stderr.String())
	if err != nil {
		t.Fatalf("preflight: %v", err)
	}
	if e.hub.requests != 1 || e.hub.authedLogins != 0 || e.hub.createdUsers != 0 {
		t.Errorf("requests %d, authed logins %d, users %d; want 1, 0, 0", e.hub.requests, e.hub.authedLogins, e.hub.createdUsers)
	}
}

func TestMintWrongSecret(t *testing.T) {
	e := newEnv(t)
	if err := os.WriteFile(e.secretFile, []byte("SESSION_SECRET=not-the-hub-secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	code, _, stderr := e.mint(t)
	if code == 0 {
		t.Fatal("expected failure")
	}
	if !strings.Contains(stderr, "not the one this hub signs with") {
		t.Errorf("unexpected stderr:\n%s", stderr)
	}
	if strings.Contains(stderr, "not-the-hub-secret") {
		t.Error("secret from file appears in stderr")
	}
	if e.hub.createdUsers != 0 {
		t.Errorf("created %d users, want 0", e.hub.createdUsers)
	}
	assertGone(t, e.out)
}

func TestMintRefusesToOverwrite(t *testing.T) {
	e := newEnv(t)
	if err := os.WriteFile(e.out, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	code, _, stderr := e.mint(t)
	if code == 0 || !strings.Contains(stderr, "refusing to overwrite") {
		t.Fatalf("exit %d, stderr:\n%s", code, stderr)
	}
	if data, _ := os.ReadFile(e.out); string(data) != "keep" {
		t.Error("existing file was modified")
	}
	if e.hub.requests != 0 {
		t.Errorf("hub was contacted %d times before the output path was checked", e.hub.requests)
	}
}

func TestMintRefusesSymlinkOutput(t *testing.T) {
	e := newEnv(t)
	target := filepath.Join(e.dir, "target")
	if err := os.Symlink(target, e.out); err != nil {
		t.Fatal(err)
	}
	if code, _, _ := e.mint(t); code == 0 {
		t.Fatal("expected failure")
	}
	if _, err := os.Lstat(target); !os.IsNotExist(err) {
		t.Error("symlink target was created")
	}
	if fi, err := os.Lstat(e.out); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Error("symlink was replaced or removed")
	}
}

func TestMintHasNoRoleFlag(t *testing.T) {
	e := newEnv(t)
	code, _, _ := e.mint(t, "--role", "admin")
	if code != 2 {
		t.Fatalf("exit %d, want 2 (unknown flag)", code)
	}
	if e.hub.requests != 0 {
		t.Error("hub contacted despite a usage error")
	}
	assertGone(t, e.out)
}

func TestMintUsageErrors(t *testing.T) {
	e := newEnv(t)
	if code, _, _ := e.run(t); code != 2 {
		t.Errorf("no args: exit %d", code)
	}
	if code, _, _ := e.run(t, "mint"); code != 2 {
		t.Errorf("missing flags: exit %d", code)
	}
	if code, _, _ := e.mint(t, "--name-prefix", "Bad Prefix"); code != 2 {
		t.Errorf("bad prefix: exit %d", code)
	}
	if e.hub.requests != 0 {
		t.Error("hub contacted despite usage errors")
	}
	assertGone(t, e.out)
}

func TestParseHubURL(t *testing.T) {
	for _, ok := range []string{"http://127.0.0.1:8080", "http://localhost:8080/", "http://[::1]:9", "https://hub.example.com"} {
		if _, err := parseHubURL(ok); err != nil {
			t.Errorf("%s: %v", ok, err)
		}
	}
	for _, bad := range []string{"http://hub.example.com", "http://10.0.0.1:8080", "ftp://127.0.0.1", "127.0.0.1:8080", "https://u:p@hub.example.com", "https://hub.example.com/?x=1"} {
		if _, err := parseHubURL(bad); err == nil {
			t.Errorf("%s: expected an error", bad)
		}
	}
}

func TestCleanup(t *testing.T) {
	t.Run("live token", func(t *testing.T) {
		e := newEnv(t)
		if code, _, stderr := e.mint(t); code != 0 {
			t.Fatalf("mint: %s", stderr)
		}
		code, stdout, stderr := e.cleanup(t, e.out)
		if code != 0 {
			t.Fatalf("exit %d: %s", code, stderr)
		}
		assertGone(t, e.out)
		if !strings.Contains(stdout, e.hub.uid) || !strings.Contains(stdout, "admin must delete") {
			t.Errorf("stdout:\n%s", stdout)
		}
	})
	t.Run("token no longer accepted", func(t *testing.T) {
		e := newEnv(t)
		if code, _, stderr := e.mint(t); code != 0 {
			t.Fatalf("mint: %s", stderr)
		}
		e.hub.access = "" // the hub now rejects the token
		code, stdout, stderr := e.cleanup(t, e.out)
		if code != 0 {
			t.Fatalf("exit %d: %s", code, stderr)
		}
		if strings.Contains(stdout, "((") || !strings.Contains(stdout, "no longer accepts the token (user_not_found)") {
			t.Errorf("stdout:\n%s", stdout)
		}
		assertGone(t, e.out)
	})
	t.Run("unexpected role still removes the file", func(t *testing.T) {
		e := newEnv(t)
		if code, _, stderr := e.mint(t); code != 0 {
			t.Fatalf("mint: %s", stderr)
		}
		e.hub.meRole = "admin"
		if code, _, _ := e.cleanup(t, e.out); code == 0 {
			t.Fatal("expected a non-zero exit")
		}
		assertGone(t, e.out)
	})
	t.Run("missing file", func(t *testing.T) {
		e := newEnv(t)
		if code, _, _ := e.cleanup(t, e.out); code == 0 {
			t.Fatal("expected failure")
		}
	})
}

// TestCleanupRefusesNonTokenFiles checks that cleanup sends nothing and
// leaves the file in place unless it holds a token shaped like the ones
// mint writes. Each token case breaks exactly one shape check (the rest of
// its claims are valid, lifetime included), and reason pins which check
// rejected it.
func TestCleanupRefusesNonTokenFiles(t *testing.T) {
	now := time.Now().Unix()
	cases := []struct {
		name    string
		content func(e *env) string
		reason  string
	}{
		{"session secret file", func(e *env) string { return "SESSION_SECRET=" + string(e.secret) + "\n" }, "not a JWT"},
		{"bare secret", func(e *env) string { return string(e.secret) }, "not a JWT"},
		{"empty", func(*env) string { return "" }, "not a JWT"},
		{"three dotted words", func(*env) string { return "a.b.c" }, "not base64url"},
		{"refresh token", func(e *env) string { return e.hub.mintToken("refresh", 7*24*time.Hour) }, "not an access token"},
		{"refresh type, valid lifetime", func(e *env) string { return e.hub.mintCustom(map[string]any{"type": "refresh"}) }, "not an access token"},
		{"no type", func(e *env) string { return e.hub.mintCustom(map[string]any{"type": nil}) }, "not an access token"},
		{"admin role", func(e *env) string { return e.hub.mintCustom(map[string]any{"role": "admin"}) }, "role is not member"},
		{"real email domain", func(e *env) string { return e.hub.mintCustom(map[string]any{"email": "person@example.com"}) }, "email is not in"},
		{"no subject", func(e *env) string { return e.hub.mintCustom(map[string]any{"sub": "", "uid": ""}) }, "missing subject"},
		{"uid differs from sub", func(e *env) string { return e.hub.mintCustom(map[string]any{"uid": "uid-other"}) }, "uid does not match subject"},
		{"missing iat", func(e *env) string { return e.hub.mintCustom(map[string]any{"iat": nil}) }, "invalid iat"},
		{"negative iat", func(e *env) string { return e.hub.mintCustom(map[string]any{"iat": -60, "exp": 840}) }, "invalid iat"},
		{"exp before iat", func(e *env) string { return e.hub.mintCustom(map[string]any{"exp": now - 60}) }, "exp is not after iat"},
		{"long lifetime", func(e *env) string {
			return e.hub.mintCustom(map[string]any{"exp": time.Now().Add(30 * 24 * time.Hour).Unix()})
		}, "lifetime exceeds"},
		{"lifetime that overflows a Duration", func(e *env) string {
			return e.hub.mintCustom(map[string]any{"iat": 1, "exp": int64(maxClaimTime)})
		}, "lifetime exceeds"},
		{"exp out of range", func(e *env) string {
			return e.hub.mintCustom(map[string]any{"iat": int64(math.MaxInt64 - 900), "exp": int64(math.MaxInt64)})
		}, "exp is out of range"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			content := tc.content(e)
			e.hub.issued = append(e.hub.issued, content) // must never be echoed either
			if err := os.WriteFile(e.out, []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
			code, _, stderr := e.cleanup(t, e.out)
			if code == 0 {
				t.Fatal("expected a non-zero exit")
			}
			if !strings.Contains(stderr, "nothing was sent and the file was left in place") || !strings.Contains(stderr, tc.reason) {
				t.Errorf("stderr missing refusal or reason %q:\n%s", tc.reason, stderr)
			}
			if e.hub.requests != 0 {
				t.Errorf("hub received %d requests", e.hub.requests)
			}
			if data, err := os.ReadFile(e.out); err != nil || string(data) != content {
				t.Error("file was modified or removed")
			}
		})
	}
}

// TestTestloginShape checks each shape rule on its own, starting from a
// valid claim set and breaking one claim per case.
func TestTestloginShape(t *testing.T) {
	now := time.Now().Unix()
	valid := func() tokenClaims {
		return tokenClaims{Sub: "u1", UID: "u1", Email: "x@" + EmailDomain, Role: "member", Type: "access", Iat: now, Exp: now + 900}
	}
	if c := valid(); c.testloginShape() != nil {
		t.Fatal("valid claims rejected")
	}
	if c := valid(); func() bool { c.UID = ""; return c.testloginShape() != nil }() {
		t.Error("claims without uid rejected")
	}
	if c := valid(); func() bool { c.Exp = c.Iat + maxTokenLifetimeSeconds; return c.testloginShape() != nil }() {
		t.Error("lifetime of exactly 30 minutes rejected")
	}
	cases := []struct {
		name   string
		mutate func(*tokenClaims)
		reason string
	}{
		{"no sub", func(c *tokenClaims) { c.Sub, c.UID = "", "" }, "missing subject"},
		{"uid differs", func(c *tokenClaims) { c.UID = "u2" }, "uid does not match subject"},
		{"type refresh", func(c *tokenClaims) { c.Type = "refresh" }, "not an access token"},
		{"type empty", func(c *tokenClaims) { c.Type = "" }, "not an access token"},
		{"role admin", func(c *tokenClaims) { c.Role = "admin" }, "role is not member"},
		{"email domain", func(c *tokenClaims) { c.Email = "x@example.com" }, "email is not in"},
		{"iat zero", func(c *tokenClaims) { c.Iat = 0 }, "invalid iat"},
		{"iat negative", func(c *tokenClaims) { c.Iat, c.Exp = -900, 0 }, "invalid iat"},
		{"iat most negative", func(c *tokenClaims) { c.Iat = math.MinInt64 }, "invalid iat"},
		{"exp equals iat", func(c *tokenClaims) { c.Exp = c.Iat }, "exp is not after iat"},
		{"lifetime 1801s", func(c *tokenClaims) { c.Exp = c.Iat + maxTokenLifetimeSeconds + 1 }, "lifetime exceeds"},
		{"lifetime overflowing a Duration", func(c *tokenClaims) { c.Iat, c.Exp = 1, maxClaimTime }, "lifetime exceeds"},
		{"exp max int64", func(c *tokenClaims) { c.Iat, c.Exp = math.MaxInt64-900, math.MaxInt64 }, "exp is out of range"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := valid()
			tc.mutate(&c)
			err := c.testloginShape()
			if err == nil || !strings.Contains(err.Error(), tc.reason) {
				t.Errorf("err = %v, want one containing %q", err, tc.reason)
			}
		})
	}
}

// TestEveryRunIsChecked keeps the secret-absence check on every run: the
// test files may refer to Run only inside the (*env).run method. It scans
// the syntax tree of every test file in the directory, so any call form (or
// taking Run as a value) is caught, and it rejects an external test package,
// whose qualified testlogin.Run uses this scan does not track.
func TestEveryRunIsChecked(t *testing.T) {
	files, err := filepath.Glob("*_test.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	inHelper, outside := 0, 0
	for _, name := range files {
		f, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		if f.Name.Name != "testlogin" {
			t.Errorf("%s: package %s; tests here must be in package testlogin so this check covers them", name, f.Name.Name)
			continue
		}
		selectors := map[*ast.Ident]bool{}
		ast.Inspect(f, func(n ast.Node) bool {
			if sel, ok := n.(*ast.SelectorExpr); ok {
				selectors[sel.Sel] = true // x.Run (such as t.Run) is a different Run
			}
			return true
		})
		for _, decl := range f.Decls {
			fd, isFunc := decl.(*ast.FuncDecl)
			helper := isFunc && fd.Name.Name == "run" && isEnvPointerRecv(fd)
			ast.Inspect(decl, func(n ast.Node) bool {
				id, ok := n.(*ast.Ident)
				if !ok || id.Name != "Run" || selectors[id] {
					return true
				}
				if helper {
					inHelper++
				} else {
					outside++
					t.Errorf("%s: Run used outside env.run", fset.Position(id.Pos()))
				}
				return true
			})
		}
	}
	if inHelper != 1 || outside != 0 {
		t.Errorf("Run references: %d in env.run (want 1), %d elsewhere (want 0)", inHelper, outside)
	}
}

// isEnvPointerRecv reports whether fd is a method with receiver type *env.
func isEnvPointerRecv(fd *ast.FuncDecl) bool {
	if fd.Recv == nil || len(fd.Recv.List) != 1 {
		return false
	}
	star, ok := fd.Recv.List[0].Type.(*ast.StarExpr)
	if !ok {
		return false
	}
	id, ok := star.X.(*ast.Ident)
	return ok && id.Name == "env"
}

func TestReadSecretFile(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string, mode os.FileMode) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(content), mode); err != nil {
			t.Fatal(err)
		}
		return p
	}
	cases := []struct {
		name, content, varName, want string
	}{
		{"plain", "SESSION_SECRET=abc\n", "", "abc"},
		{"export and double quotes", "export SESSION_SECRET=\"a b c\"\n", "", "a b c"},
		{"single quotes", "SESSION_SECRET='x=y'\n", "", "x=y"},
		{"scion var wins", "SESSION_SECRET=low\nSCION_SERVER_SESSION_SECRET=high\n", "", "high"},
		{"later assignment wins", "SESSION_SECRET=one\nSESSION_SECRET=two\n", "", "two"},
		{"comments and crlf", "# SESSION_SECRET=no\r\n; x\r\nSESSION_SECRET=yes\r\n", "", "yes"},
		{"drop-in", "[Service]\nEnvironment=\"OTHER=1\" \"SESSION_SECRET=d i\"\n", "", "d i"},
		{"drop-in unquoted", "[Service]\nEnvironment=SCION_SERVER_SESSION_SECRET=z OTHER=2\n", "", "z"},
		{"custom var", "HUB_SECRET=custom\nSESSION_SECRET=other\n", "HUB_SECRET", "custom"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := write(strings.ReplaceAll(tc.name, " ", "_"), tc.content, 0o600)
			got, err := readSecretFile(p, tc.varName, nil)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}

	t.Run("missing variable error has no values", func(t *testing.T) {
		p := write("missing", "OTHER=value-must-not-leak\nSESSION_SECRET=\n", 0o600)
		_, err := readSecretFile(p, "", nil)
		if err == nil {
			t.Fatal("expected an error")
		}
		if strings.Contains(err.Error(), "value-must-not-leak") {
			t.Error("error contains a value from the file")
		}
	})
	t.Run("custom var is exclusive", func(t *testing.T) {
		p := write("exclusive", "SESSION_SECRET=default\n", 0o600)
		if _, err := readSecretFile(p, "HUB_SECRET", nil); err == nil {
			t.Fatal("expected an error: --secret-var must not fall back")
		}
	})
	t.Run("directory rejected", func(t *testing.T) {
		if _, err := readSecretFile(dir, "", nil); err == nil {
			t.Fatal("expected an error")
		}
	})
	t.Run("loose permissions warn", func(t *testing.T) {
		p := write("loose", "SESSION_SECRET=v\n", 0o644)
		var warn bytes.Buffer
		if _, err := readSecretFile(p, "", &warn); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(warn.String(), "group or other") {
			t.Errorf("no warning: %q", warn.String())
		}
	})
}

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
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

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

	mu            sync.Mutex
	requests      int
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
	var req struct{ Email, Role, DisplayName string }
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	f.requestedRole, f.email, f.displayName = req.Role, req.Email, req.DisplayName
	f.createdUsers++
	f.uid = "uid-" + randHex(f.t, 8)
	f.loginAt = time.Now()
	f.access = f.mintToken("access", f.tokenLife)
	f.refresh = f.mintToken("refresh", 7*24*time.Hour)
	f.issued = append(f.issued, f.access, f.refresh)
	http.SetCookie(w, &http.Cookie{Name: "scion_sess", Value: f.refresh})
	_ = json.NewEncoder(w).Encode(map[string]any{
		"user":         map[string]string{"id": f.uid, "email": f.email, "displayName": f.displayName, "role": f.respRole},
		"accessToken":  f.access,
		"refreshToken": f.refresh,
		"expiresIn":    int64(f.tokenLife.Seconds()),
	})
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
		{"pre-existing user", func(f *fakeHub) { f.createdOffset = -time.Hour }, "existing account"},
		{"created in the future", func(f *fakeHub) { f.createdOffset = time.Hour; f.lastLoginOff = time.Hour }, "not created by this run"},
		{"lastLogin far from created", func(f *fakeHub) { f.lastLoginOff = time.Minute }, "not a newly created user"},
		{"user lookup forbidden", func(f *fakeHub) { f.userStatus = http.StatusForbidden }, "cannot confirm the user is new"},
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
		{"existing account", func(f *fakeHub) { f.createdOffset = -time.Hour }, "existing account", "admin must delete"},
		{"freshness unconfirmed", func(f *fakeHub) { f.userStatus = http.StatusForbidden }, "could not confirm", "admin must delete"},
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

func TestMintDisabledCreatesNothing(t *testing.T) {
	e := newEnv(t)
	e.hub.enabled = false
	if code, _, _ := e.mint(t); code == 0 {
		t.Fatal("expected failure")
	}
	if e.hub.authedLogins != 0 || e.hub.createdUsers != 0 {
		t.Errorf("authed logins %d, users %d; want 0 and 0", e.hub.authedLogins, e.hub.createdUsers)
	}
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
// mint writes.
func TestCleanupRefusesNonTokenFiles(t *testing.T) {
	cases := []struct {
		name    string
		content func(e *env) string
	}{
		{"session secret file", func(e *env) string { return "SESSION_SECRET=" + string(e.secret) + "\n" }},
		{"bare secret", func(e *env) string { return string(e.secret) }},
		{"empty", func(*env) string { return "" }},
		{"three dotted words", func(*env) string { return "a.b.c" }},
		{"refresh token", func(e *env) string { return e.hub.mintToken("refresh", 7*24*time.Hour) }},
		{"admin role", func(e *env) string { return e.hub.mintCustom(map[string]any{"role": "admin"}) }},
		{"real email domain", func(e *env) string { return e.hub.mintCustom(map[string]any{"email": "person@example.com"}) }},
		{"long lifetime", func(e *env) string {
			return e.hub.mintCustom(map[string]any{"exp": time.Now().Add(30 * 24 * time.Hour).Unix()})
		}},
		{"no subject", func(e *env) string { return e.hub.mintCustom(map[string]any{"sub": "", "uid": ""}) }},
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
			if !strings.Contains(stderr, "nothing was sent and the file was left in place") {
				t.Errorf("stderr:\n%s", stderr)
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

// TestEveryRunIsChecked keeps the secret-absence check on every run: the
// tests may call Run only through env.run.
func TestEveryRunIsChecked(t *testing.T) {
	data, err := os.ReadFile("testlogin_test.go")
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(data), "Run("+"context."); n != 1 {
		t.Errorf("found %d direct calls of Run; call it only through env.run", n)
	}
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

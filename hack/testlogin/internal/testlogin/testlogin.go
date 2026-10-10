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

// Package testlogin implements the testlogin command: it obtains a
// short-lived, member-role test identity from a hub through the hub's
// test-login endpoint, verifies it, and writes only the access token to a
// private file. See hack/testlogin/README.md.
package testlogin

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/GoogleCloudPlatform/scion/hack/testlogin/internal/challenge"
)

const (
	// role is the only role this tool ever requests. There is deliberately
	// no flag or option to change it.
	role = "member"

	// EmailDomain is the reserved (RFC 2606) domain used for generated
	// test identities.
	EmailDomain = "scion-test.invalid"

	// maxTokenLifetime bounds exp-iat of the access token the hub returns.
	maxTokenLifetime        = 30 * time.Minute
	maxTokenLifetimeSeconds = int64(maxTokenLifetime / time.Second)

	// maxClaimTime rejects absurd exp values (after 9999-12-31T23:59:59Z).
	maxClaimTime = 253402300799

	// freshnessSkew is the clock and timestamp-precision allowance used when
	// checking that the hub created the user during this run.
	freshnessSkew = 5 * time.Second

	defaultNamePrefix = "test-fixture"
	maxResponseBytes  = 64 << 10
	maxTokenFileBytes = 16 << 10

	pathTestLogin   = "/api/v1/auth/test-login"
	pathAuthMe      = "/api/v1/auth/me"
	pathUsers       = "/api/v1/users/"
	pathAdminConfig = "/api/v1/admin/server-config"
)

var prefixRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)

// Options carries test hooks. The zero value is what the command uses.
type Options struct {
	// HTTPClient overrides the HTTP client.
	HTTPClient *http.Client
	// Email overrides the generated email (tests only; not exposed as a flag).
	Email string
}

const usage = `testlogin obtains a short-lived member test identity from a hub.

Usage:
  testlogin mint    --hub-url URL --secret-file PATH --out PATH [--secret-var NAME] [--name-prefix P]
  testlogin cleanup --hub-url URL --token-file PATH

mint:    creates a new test user with role "member" through the hub's test-login
         endpoint, verifies it, and writes only the access token to --out
         (mode 0600; an existing file is never overwritten).
cleanup: checks the token in --token-file, then deletes the file. The user
         itself is not deleted; an admin must delete it (its id is printed).
`

// Run executes the command with args (excluding the program name) and
// returns the process exit code.
func Run(ctx context.Context, args []string, stdout, stderr io.Writer, opts Options) int {
	// Messages can include text from the hub or from Go's HTTP client
	// (which quotes malformed response lines); strip control characters so
	// nothing printed can drive the terminal.
	stdout, stderr = printableWriter{stdout}, printableWriter{stderr}
	if len(args) == 0 {
		_, _ = fmt.Fprint(stderr, usage)
		return 2
	}
	switch args[0] {
	case "mint":
		return runMint(ctx, args[1:], stdout, stderr, opts)
	case "cleanup":
		return runCleanup(ctx, args[1:], stdout, stderr, opts)
	case "-h", "--help", "help":
		_, _ = fmt.Fprint(stdout, usage)
		return 0
	default:
		_, _ = fmt.Fprintf(stderr, "testlogin: unknown subcommand %q\n\n%s", args[0], usage)
		return 2
	}
}

// printableWriter replaces control characters (C0 other than tab and
// newline, DEL, and C1), Unicode format characters (category Cf: bidi
// controls, zero-width characters and the like), the line and paragraph
// separators U+2028 and U+2029, and invalid UTF-8 with '?', so that nothing
// printed can drive the terminal or change how a line displays.
type printableWriter struct{ w io.Writer }

func (p printableWriter) Write(b []byte) (int, error) {
	n := len(b)
	clean := make([]byte, 0, n)
	for len(b) > 0 {
		r, size := utf8.DecodeRune(b)
		switch {
		case r == utf8.RuneError && size <= 1, unprintable(r):
			clean = append(clean, '?')
		default:
			clean = append(clean, b[:size]...)
		}
		b = b[size:]
	}
	if _, err := p.w.Write(clean); err != nil {
		return 0, err
	}
	return n, nil
}

// unprintable reports whether printableWriter replaces r.
func unprintable(r rune) bool {
	return (r < 0x20 && r != '\n' && r != '\t') ||
		(r >= 0x7f && r <= 0x9f) ||
		r == '\u2028' || r == '\u2029' ||
		unicode.Is(unicode.Cf, r)
}

func errorf(w io.Writer, format string, a ...any) {
	_, _ = fmt.Fprintf(w, "testlogin: error: "+format+"\n", a...)
}

func runMint(ctx context.Context, args []string, stdout, stderr io.Writer, opts Options) int {
	fs := flag.NewFlagSet("mint", flag.ContinueOnError)
	fs.SetOutput(stderr)
	hubURL := fs.String("hub-url", "", "hub base URL, e.g. http://127.0.0.1:8080 (http only for loopback)")
	secretFile := fs.String("secret-file", "", "path to the hub's EnvironmentFile or unit drop-in holding the session secret")
	secretVar := fs.String("secret-var", "", "variable name in --secret-file (default: SCION_SERVER_SESSION_SECRET, then SESSION_SECRET)")
	out := fs.String("out", "", "path of the access-token file to create (must not exist)")
	prefix := fs.String("name-prefix", defaultNamePrefix, "prefix for the generated email local part and display name")
	timeout := fs.Duration("timeout", 30*time.Second, "overall timeout")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 0 {
		errorf(stderr, "unexpected arguments: %v", fs.Args())
		return 2
	}
	if *hubURL == "" || *secretFile == "" || *out == "" {
		errorf(stderr, "--hub-url, --secret-file and --out are required")
		return 2
	}
	if !prefixRE.MatchString(*prefix) {
		errorf(stderr, "--name-prefix must match %s", prefixRE)
		return 2
	}
	base, err := parseHubURL(*hubURL)
	if err != nil {
		errorf(stderr, "%v", err)
		return 2
	}

	ctx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()

	m := &minter{
		base:   base,
		client: httpClient(opts.HTTPClient),
		stdout: stdout,
		stderr: stderr,
	}
	if err := m.run(ctx, *secretFile, *secretVar, *out, *prefix, opts.Email); err != nil {
		errorf(stderr, "%v", err)
		switch {
		case m.preexisting:
			errorf(stderr, "review: uid %s is an existing account (the generated email was already registered), and test-login may have changed its role to %s; do not delete it blindly, an admin must review it", m.createdUID, role)
		case m.fresh:
			errorf(stderr, "this run created user uid %s; an admin must delete it", m.createdUID)
		case m.createdUID != "":
			errorf(stderr, "review: the hub returned uid %s, but this run could not confirm that it created that account; do not delete it blindly, an admin must review it", m.createdUID)
		}
		return 1
	}
	return 0
}

type minter struct {
	base       *url.URL
	client     *http.Client
	stdout     io.Writer
	stderr     io.Writer
	retryAfter string // Retry-After of the last response, if any
	createdUID string
	// preexisting is set when the user returned by test-login was not
	// created by this run.
	preexisting bool
	// fresh is set once the user is confirmed to have been created by this run.
	fresh bool
}

type userJSON struct {
	ID          string    `json:"id"`
	Email       string    `json:"email"`
	DisplayName string    `json:"displayName"`
	Role        string    `json:"role"`
	Created     time.Time `json:"created"`
	LastLogin   time.Time `json:"lastLogin"`
}

// testLoginResponse deliberately has no field for the refresh token, so it
// is never decoded into memory the tool holds on to.
type testLoginResponse struct {
	User        *userJSON `json:"user"`
	AccessToken string    `json:"accessToken"`
	// Created reports whether this call created the user. Hubs without
	// createOnly support omit it (nil).
	Created *bool `json:"created"`
}

func (m *minter) run(ctx context.Context, secretFile, secretVar, out, prefix, email string) (err error) {
	// Reserve the output path first: this refuses an existing file (or
	// symlink) before anything is created on the hub. The token itself is
	// written only after every check has passed; on any failure the file is
	// removed.
	f, err := os.OpenFile(out, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return fmt.Errorf("refusing to overwrite existing file %s", out)
		}
		return fmt.Errorf("create token file: %w", err)
	}
	written := false
	defer func() {
		if !written {
			_ = f.Close()
			if rmErr := os.Remove(out); rmErr != nil && !errors.Is(rmErr, os.ErrNotExist) {
				errorf(m.stderr, "could not remove %s: %v", out, rmErr)
			} else {
				_, _ = fmt.Fprintf(m.stderr, "testlogin: removed %s\n", out)
			}
		}
	}()
	if err := f.Chmod(0o600); err != nil {
		return fmt.Errorf("chmod token file: %w", err)
	}

	secret, err := readSecretFile(secretFile, secretVar, m.stderr)
	if err != nil {
		return err
	}
	key := challenge.DeriveUserSigningKey(secret)
	challenge.Zero(secret)
	defer challenge.Zero(key)

	if err := m.preflight(ctx); err != nil {
		return err
	}

	if email == "" {
		suffix, err := randomHex(8)
		if err != nil {
			return err
		}
		email = prefix + "-" + suffix + "@" + EmailDomain
	}
	local, _, _ := strings.Cut(email, "@")
	displayName := strings.Replace(local, prefix+"-", prefix+" ", 1)

	chal, err := challenge.Mint(key, prefix, time.Now())
	challenge.Zero(key)
	if err != nil {
		return err
	}

	start := time.Now()
	resp, err := m.testLogin(ctx, chal, email, displayName)
	if err != nil {
		return err
	}
	token := resp.AccessToken
	u := resp.User

	// Establish first whether this run created the account, so that any
	// later failure knows whether it may be deleted. A hub that reports
	// "created" is authoritative; for older hubs fall back to checking the
	// user's timestamps.
	switch {
	case resp.Created != nil && *resp.Created:
	case resp.Created != nil:
		m.preexisting = true
		return fmt.Errorf("hub reports that user %s already existed (created=false)", u.ID)
	default:
		_, _ = fmt.Fprintln(m.stderr, "testlogin: note: this hub does not support createOnly (no \"created\" in the response); checking the user's timestamps instead")
		if err := m.checkFresh(ctx, token, u.ID, start); err != nil {
			return err
		}
	}
	m.fresh = true

	if u.Role != role {
		return fmt.Errorf("hub returned role %q for the new user, want %q", u.Role, role)
	}
	if !strings.EqualFold(u.Email, email) {
		return fmt.Errorf("hub returned email %q, want %q", u.Email, email)
	}

	claims, err := decodeClaims(token)
	if err != nil {
		return err
	}
	if err := claims.check(u.ID, email); err != nil {
		return err
	}

	if err := m.checkMe(ctx, token, u.ID, email); err != nil {
		return err
	}
	if err := m.checkNotAdmin(ctx, token); err != nil {
		return err
	}

	if _, err := io.WriteString(f, token); err != nil {
		return fmt.Errorf("write token file: %w", err)
	}
	if err := f.Sync(); err != nil {
		return fmt.Errorf("sync token file: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close token file: %w", err)
	}
	written = true

	exp := time.Unix(claims.Exp, 0).UTC()
	_, _ = fmt.Fprintf(m.stdout, "uid:        %s\n", u.ID)
	_, _ = fmt.Fprintf(m.stdout, "email:      %s\n", email)
	_, _ = fmt.Fprintf(m.stdout, "role:       %s\n", role)
	_, _ = fmt.Fprintf(m.stdout, "expires:    %s (%s)\n", exp.Format(time.RFC3339), time.Duration(claims.Exp-claims.Iat)*time.Second)
	_, _ = fmt.Fprintf(m.stdout, "token file: %s\n", out)
	_, _ = fmt.Fprintf(m.stdout, "verified:   /auth/me role %s; admin endpoint 403; user created by this run\n", role)
	_, _ = fmt.Fprintf(m.stdout, "cleanup:    testlogin cleanup --hub-url %s --token-file %s, then an admin deletes user %s\n", m.base, out, u.ID)
	return nil
}

// preflight checks that test-login is enabled without creating anything:
// with no Authorization header the hub's handler answers 403 when the
// endpoint is disabled and 401 when it is enabled.
func (m *minter) preflight(ctx context.Context) error {
	status, body, err := m.do(ctx, http.MethodPost, pathTestLogin, "", []byte("{}"))
	if err != nil {
		return fmt.Errorf("test-login preflight: %w", err)
	}
	switch status {
	case http.StatusUnauthorized:
		return nil
	case http.StatusTooManyRequests:
		return m.rateLimited()
	case http.StatusForbidden:
		return fmt.Errorf("test-login is not enabled on this hub (preflight returned 403 %s); refusing to continue", errorCode(body))
	default:
		return fmt.Errorf("test-login preflight returned HTTP %d %s, want 401; refusing to continue", status, errorCode(body))
	}
}

// rateLimited reports a 429 from test-login. The tool does not retry.
func (m *minter) rateLimited() error {
	after := strings.Trim(m.retryAfter, " \t")
	switch {
	case after == "" || len(after) > 40 || strings.ContainsFunc(after, func(r rune) bool { return r < ' ' || r > '~' }):
		after = "not given"
	case strings.Trim(after, "0123456789") == "":
		after += "s" // delay in seconds; otherwise an HTTP date, shown as sent
	}
	return fmt.Errorf("hub rate-limited test-login (429, Retry-After: %s); not retrying, run again later", after)
}

func (m *minter) testLogin(ctx context.Context, chal, email, displayName string) (*testLoginResponse, error) {
	// createOnly makes a hub that supports it answer 409 without changing
	// anything if the email already exists. Older hubs ignore the field.
	reqBody, err := json.Marshal(map[string]any{
		"email":       email,
		"role":        role,
		"displayName": displayName,
		"createOnly":  true,
	})
	if err != nil {
		return nil, err
	}
	status, body, err := m.do(ctx, http.MethodPost, pathTestLogin, chal, reqBody)
	if err != nil {
		return nil, fmt.Errorf("test-login: %w", err)
	}
	defer challenge.Zero(body) // the body also carries the refresh token
	switch status {
	case http.StatusOK:
	case http.StatusUnauthorized:
		return nil, errors.New("hub rejected the test-login challenge (401): the session secret in --secret-file is not the one this hub signs with")
	case http.StatusConflict:
		return nil, fmt.Errorf("an account with email %s already exists (409 %s); with createOnly the hub changed nothing, so there is nothing to clean up", email, errorCode(body))
	case http.StatusTooManyRequests:
		return nil, m.rateLimited()
	default:
		return nil, fmt.Errorf("test-login returned HTTP %d %s", status, errorCode(body))
	}
	var resp testLoginResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("decode test-login response: %w", err)
	}
	if resp.User != nil && resp.User.ID != "" {
		m.createdUID = resp.User.ID
	}
	if resp.User == nil || resp.User.ID == "" || resp.AccessToken == "" {
		return nil, errors.New("test-login response is missing the user or the access token")
	}
	return &resp, nil
}

func (m *minter) checkMe(ctx context.Context, token, uid, email string) error {
	status, body, err := m.do(ctx, http.MethodGet, pathAuthMe, token, nil)
	if err != nil {
		return fmt.Errorf("/auth/me: %w", err)
	}
	if status != http.StatusOK {
		return fmt.Errorf("/auth/me returned HTTP %d %s, want 200", status, errorCode(body))
	}
	var me userJSON
	if err := json.Unmarshal(body, &me); err != nil {
		return fmt.Errorf("decode /auth/me: %w", err)
	}
	if me.ID != uid {
		return fmt.Errorf("/auth/me returned uid %q, want %q", me.ID, uid)
	}
	if me.Role != role {
		return fmt.Errorf("/auth/me returned role %q, want %q", me.Role, role)
	}
	if !strings.EqualFold(me.Email, email) {
		return fmt.Errorf("/auth/me returned email %q, want %q", me.Email, email)
	}
	return nil
}

// checkFresh fails unless the hub created the user during this run. The
// test-login endpoint updates (and re-roles) an existing user with the same
// email, so a pre-existing account must never be accepted.
func (m *minter) checkFresh(ctx context.Context, token, uid string, start time.Time) error {
	status, body, err := m.do(ctx, http.MethodGet, pathUsers+url.PathEscape(uid), token, nil)
	if err != nil {
		return fmt.Errorf("user lookup: %w", err)
	}
	if status != http.StatusOK {
		return fmt.Errorf("user lookup returned HTTP %d %s, want 200; cannot confirm the user is new", status, errorCode(body))
	}
	var u userJSON
	if err := json.Unmarshal(body, &u); err != nil {
		return fmt.Errorf("decode user: %w", err)
	}
	if u.ID != uid {
		return fmt.Errorf("user lookup returned uid %q, want %q", u.ID, uid)
	}
	now := time.Now()
	if u.Created.IsZero() || u.Created.Before(start.Add(-freshnessSkew)) || u.Created.After(now.Add(freshnessSkew)) {
		m.preexisting = true
		return fmt.Errorf("user %s was not created by this run (created %s, run started %s); the email already existed", uid, u.Created.UTC().Format(time.RFC3339), start.UTC().Format(time.RFC3339))
	}
	if !u.LastLogin.IsZero() {
		d := u.LastLogin.Sub(u.Created)
		if d < 0 {
			d = -d
		}
		if d > freshnessSkew {
			m.preexisting = true
			return fmt.Errorf("user %s has lastLogin %s far from created %s; not a newly created user", uid, u.LastLogin.UTC().Format(time.RFC3339), u.Created.UTC().Format(time.RFC3339))
		}
	}
	return nil
}

func (m *minter) checkNotAdmin(ctx context.Context, token string) error {
	status, body, err := m.do(ctx, http.MethodGet, pathAdminConfig, token, nil)
	if err != nil {
		return fmt.Errorf("admin endpoint check: %w", err)
	}
	if status != http.StatusForbidden {
		return fmt.Errorf("admin-only endpoint %s returned HTTP %d %s, want 403", pathAdminConfig, status, errorCode(body))
	}
	return nil
}

func runCleanup(ctx context.Context, args []string, stdout, stderr io.Writer, opts Options) int {
	fs := flag.NewFlagSet("cleanup", flag.ContinueOnError)
	fs.SetOutput(stderr)
	hubURL := fs.String("hub-url", "", "hub base URL (http only for loopback)")
	tokenFile := fs.String("token-file", "", "access-token file written by mint")
	timeout := fs.Duration("timeout", 30*time.Second, "overall timeout")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 0 {
		errorf(stderr, "unexpected arguments: %v", fs.Args())
		return 2
	}
	if *hubURL == "" || *tokenFile == "" {
		errorf(stderr, "--hub-url and --token-file are required")
		return 2
	}
	base, err := parseHubURL(*hubURL)
	if err != nil {
		errorf(stderr, "%v", err)
		return 2
	}
	ctx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()
	m := &minter{base: base, client: httpClient(opts.HTTPClient), stdout: stdout, stderr: stderr}

	st, err := os.Lstat(*tokenFile)
	if err != nil {
		errorf(stderr, "token file: %v", err)
		return 1
	}
	if !st.Mode().IsRegular() {
		errorf(stderr, "token file %s is not a regular file; not touching it", *tokenFile)
		return 1
	}
	raw, err := readLimited(*tokenFile, maxTokenFileBytes)
	if err != nil {
		errorf(stderr, "read token file: %v", err)
		return 1
	}
	token := strings.TrimSpace(string(raw))
	challenge.Zero(raw)

	// Refuse anything that is not a token written by mint before sending
	// it anywhere or deleting the file: --token-file could name the wrong
	// file, and its content must not be sent to the hub as a credential.
	c, err := decodeClaims(token)
	if err == nil {
		err = c.testloginShape()
	}
	if err != nil {
		errorf(stderr, "%s does not hold an access token written by testlogin mint (%v); refusing: nothing was sent and the file was left in place", *tokenFile, err)
		return 1
	}
	uid := c.Sub
	_, _ = fmt.Fprintf(stdout, "uid:     %s\nemail:   %s\nexpires: %s\n", uid, c.Email, time.Unix(c.Exp, 0).UTC().Format(time.RFC3339))

	ok := true
	status, body, err := m.do(ctx, http.MethodGet, pathAuthMe, token, nil)
	switch {
	case err != nil:
		errorf(stderr, "/auth/me: %v", err)
		ok = false
	case status == http.StatusOK:
		var me userJSON
		if jerr := json.Unmarshal(body, &me); jerr != nil {
			errorf(stderr, "decode /auth/me: %v", jerr)
			ok = false
		} else if me.Role != role || me.ID != uid {
			errorf(stderr, "/auth/me returned uid %q role %q; want uid %q role %q", me.ID, me.Role, uid, role)
			ok = false
		} else {
			_, _ = fmt.Fprintf(stdout, "verified: token is live for uid %s with role %s\n", me.ID, me.Role)
		}
	case status == http.StatusUnauthorized:
		_, _ = fmt.Fprintf(stdout, "verified: hub no longer accepts the token %s\n", errorCode(body))
	default:
		errorf(stderr, "/auth/me returned HTTP %d %s", status, errorCode(body))
		ok = false
	}

	if err := os.Remove(*tokenFile); err != nil {
		errorf(stderr, "remove token file: %v", err)
		return 1
	}
	if _, err := os.Lstat(*tokenFile); !errors.Is(err, os.ErrNotExist) {
		errorf(stderr, "token file %s still exists after removal", *tokenFile)
		return 1
	}
	_, _ = fmt.Fprintf(stdout, "removed:  %s\n", *tokenFile)
	_, _ = fmt.Fprintf(stdout, "next:     an admin must delete user %s (after its agents and projects are deleted); this tool does not delete users\n", uid)
	if !ok {
		return 1
	}
	return 0
}

// do sends a request and returns the status and the (size-limited) body.
// bearer, when set, is sent as the Authorization header. Nothing about the
// request or response is logged.
func (m *minter) do(ctx context.Context, method, path, bearer string, body []byte) (int, []byte, error) {
	u := *m.base
	u.Path = strings.TrimSuffix(u.Path, "/") + path
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), rd)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := m.client.Do(req)
	if err != nil {
		return 0, nil, scrubURLError(err)
	}
	defer func() { _ = resp.Body.Close() }()
	m.retryAfter = resp.Header.Get("Retry-After")
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return 0, nil, err
	}
	return resp.StatusCode, data, nil
}

// scrubURLError reduces a client error to its cause; the URL carries no
// secrets, but this keeps messages short and predictable.
func scrubURLError(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return fmt.Errorf("%s %s: %w", ue.Op, ue.URL, ue.Err)
	}
	return err
}

// httpClient returns a client that keeps no cookies (Set-Cookie values from
// the hub, which include session data, are dropped) and does not follow
// redirects, so the bearer is only ever sent to the configured hub URL.
func httpClient(c *http.Client) *http.Client {
	if c != nil {
		return c
	}
	return &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

// parseHubURL validates the base URL. Plain http is accepted only for a
// loopback host so a bearer token never crosses the network unencrypted.
func parseHubURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("invalid --hub-url: %w", err)
	}
	if u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("--hub-url must be a plain base URL such as http://127.0.0.1:8080")
	}
	switch u.Scheme {
	case "https":
	case "http":
		if !isLoopback(u.Hostname()) {
			return nil, errors.New("--hub-url: plain http is only allowed for a loopback address; use https")
		}
	default:
		return nil, errors.New("--hub-url must use http or https")
	}
	return u, nil
}

func isLoopback(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// errorCode extracts the hub's error code from an error body, for messages.
// Only the short code is returned, never the raw body.
func errorCode(body []byte) string {
	var e struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &e) == nil && e.Error.Code != "" && len(e.Error.Code) <= 64 {
		return "(" + e.Error.Code + ")"
	}
	return ""
}

type tokenClaims struct {
	Sub   string `json:"sub"`
	UID   string `json:"uid"`
	Email string `json:"email"`
	Role  string `json:"role"`
	Type  string `json:"type"`
	Iat   int64  `json:"iat"`
	Exp   int64  `json:"exp"`
}

// decodeClaims reads a JWT's claims without verifying the signature. It is
// used only for self-checks on a token the hub just issued; the hub's own
// answers (/auth/me, the admin probe) are the authority.
func decodeClaims(token string) (*tokenClaims, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, errors.New("not a JWT")
	}
	for _, p := range parts {
		if p == "" || strings.Trim(p, "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_") != "" {
			return nil, errors.New("not a JWT")
		}
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, errors.New("JWT payload is not base64url")
	}
	var c tokenClaims
	if err := json.Unmarshal(payload, &c); err != nil {
		return nil, errors.New("JWT payload is not JSON")
	}
	return &c, nil
}

// testloginShape checks the claims every token written by mint has: an
// access token for a member with a generated test email and a short
// lifetime. Errors name the failing claim but never echo claim values, since
// cleanup applies this to a file that may hold something else.
func (c *tokenClaims) testloginShape() error {
	if c.Sub == "" {
		return errors.New("missing subject")
	}
	if c.UID != "" && c.UID != c.Sub {
		return errors.New("uid does not match subject")
	}
	if c.Type != "access" {
		return errors.New("not an access token")
	}
	if c.Role != role {
		return fmt.Errorf("role is not %s", role)
	}
	if !strings.HasSuffix(strings.ToLower(c.Email), "@"+EmailDomain) {
		return fmt.Errorf("email is not in %s", EmailDomain)
	}
	// Integer seconds throughout: with iat > 0 and exp > iat, exp - iat
	// cannot overflow, and no time.Duration multiplication is involved.
	if c.Iat <= 0 {
		return errors.New("missing or invalid iat")
	}
	if c.Exp <= c.Iat {
		return errors.New("exp is not after iat")
	}
	if c.Exp > maxClaimTime {
		return errors.New("exp is out of range")
	}
	if c.Exp-c.Iat > maxTokenLifetimeSeconds {
		return fmt.Errorf("lifetime exceeds %s", maxTokenLifetime)
	}
	return nil
}

// check verifies a token the hub just issued in mint.
func (c *tokenClaims) check(uid, email string) error {
	if err := c.testloginShape(); err != nil {
		return fmt.Errorf("access token: %w", err)
	}
	if c.Sub != uid {
		return fmt.Errorf("access token subject %q does not match uid %q", c.Sub, uid)
	}
	if !strings.EqualFold(c.Email, email) {
		return fmt.Errorf("access token email %q, want %q", c.Email, email)
	}
	if time.Unix(c.Exp, 0).Before(time.Now()) {
		return errors.New("access token is already expired")
	}
	return nil
}

func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate random suffix: %w", err)
	}
	return hex.EncodeToString(b), nil
}

func readLimited(path string, limit int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("file is larger than %d bytes", limit)
	}
	return data, nil
}

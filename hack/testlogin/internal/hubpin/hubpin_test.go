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

//go:build !no_sqlite

package hubpin

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/GoogleCloudPlatform/scion/hack/testlogin/internal/challenge"
	"github.com/GoogleCloudPlatform/scion/hack/testlogin/internal/testlogin"
	"github.com/GoogleCloudPlatform/scion/pkg/ent/entc"
	"github.com/GoogleCloudPlatform/scion/pkg/hub"
	"github.com/GoogleCloudPlatform/scion/pkg/store"
	"github.com/GoogleCloudPlatform/scion/pkg/store/entadapter"
)

func randomSecret(t *testing.T) string {
	t.Helper()
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return "pin-" + hex.EncodeToString(b)
}

// TestChallengeAcceptedByHubTokenService pins the challenge claim format:
// a challenge minted by this package with a given key must pass
// hub.UserTokenService.ValidateTestLoginToken for the same key, and fail
// for a different one.
func TestChallengeAcceptedByHubTokenService(t *testing.T) {
	key := challenge.DeriveUserSigningKey([]byte(randomSecret(t)))
	svc, err := hub.NewUserTokenService(hub.UserTokenConfig{SigningKey: key})
	if err != nil {
		t.Fatal(err)
	}
	tok, err := challenge.Mint(key, "pin", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.ValidateTestLoginToken(tok); err != nil {
		t.Fatalf("hub rejected the challenge: %v", err)
	}
	other, err := hub.NewUserTokenService(hub.UserTokenConfig{
		SigningKey: challenge.DeriveUserSigningKey([]byte(randomSecret(t))),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := other.ValidateTestLoginToken(tok); err == nil {
		t.Fatal("a hub with a different secret accepted the challenge")
	}
	if challenge.Audience != hub.TestLoginAudience || challenge.Issuer != hub.UserTokenIssuer ||
		challenge.Lifetime != hub.DefaultTestLoginTokenDuration {
		t.Fatal("challenge constants drifted from pkg/hub")
	}
}

// testHub is an in-process hub (hub API mounted on the web server, as in
// combined mode) backed by a fresh SQLite database.
type testHub struct {
	srv    *hub.Server
	url    string
	store  store.Store
	secret string

	mu       sync.Mutex
	requests int      // requests the hub received
	issued   []string // every access and refresh token test-login returned
}

// record wraps the hub handler and keeps every token the test-login
// endpoint returns, so tests can assert that none of them is ever printed.
func (h *testHub) record(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.mu.Lock()
		h.requests++
		h.mu.Unlock()
		if r.URL.Path != "/api/v1/auth/test-login" {
			next.ServeHTTP(w, r)
			return
		}
		rec := httptest.NewRecorder()
		next.ServeHTTP(rec, r)
		var body struct {
			AccessToken  string `json:"accessToken"`
			RefreshToken string `json:"refreshToken"`
		}
		if json.Unmarshal(rec.Body.Bytes(), &body) == nil {
			h.mu.Lock()
			for _, tok := range []string{body.AccessToken, body.RefreshToken} {
				if tok != "" {
					h.issued = append(h.issued, tok)
				}
			}
			h.mu.Unlock()
		}
		for k, v := range rec.Header() {
			w.Header()[k] = v
		}
		w.WriteHeader(rec.Code)
		_, _ = w.Write(rec.Body.Bytes())
	})
}

func startHub(t *testing.T, secret string, enableTestLogin bool) *testHub {
	t.Helper()
	ctx := context.Background()
	dsn := "file:" + filepath.Join(t.TempDir(), "hub.db") + "?cache=shared"
	client, err := entc.OpenSQLite(dsn, entc.PoolConfig{MaxOpenConns: 1})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	st := entadapter.NewCompositeStore(client)
	t.Cleanup(func() { _ = st.Close() })
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	cfg := hub.DefaultServerConfig()
	cfg.SharedSigningSecret = secret
	srv, err := hub.New(cfg, st)
	if err != nil {
		t.Fatalf("hub.New: %v", err)
	}
	t.Cleanup(func() { _ = srv.Shutdown(context.Background()) })

	ws := hub.NewWebServer(hub.WebServerConfig{
		SessionSecret:   secret,
		EnableTestLogin: enableTestLogin,
	})
	ws.SetStore(srv.GetStore())
	ws.SetUserTokenService(srv.GetUserTokenService())
	ws.SetAuthzService(srv.GetAuthzService())
	ws.MountHubAPI(srv.Handler(), func(context.Context) error { return nil })

	h := &testHub{srv: srv, store: srv.GetStore(), secret: secret}
	hs := httptest.NewServer(h.record(ws.Handler()))
	t.Cleanup(hs.Close)
	h.url = hs.URL
	return h
}

// TestDerivationMatchesHub pins DeriveUserSigningKey to the hub's own
// derivation: a hub started with a shared secret must accept a challenge
// signed with the key this package derives from the same secret.
func TestDerivationMatchesHub(t *testing.T) {
	secret := randomSecret(t)
	h := startHub(t, secret, true)
	tok, err := challenge.Mint(challenge.DeriveUserSigningKey([]byte(secret)), "pin", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := h.srv.GetUserTokenService().ValidateTestLoginToken(tok); err != nil {
		t.Fatalf("hub started with the same session secret rejected the challenge "+
			"(the derivation in internal/challenge has drifted from pkg/hub): %v", err)
	}
}

type run struct {
	code           int
	stdout, stderr string
}

// runTool is the only place these tests invoke the tool
// (TestEveryRunIsChecked enforces this). Every run is checked for the hub's
// secret, the secret in the file given to the tool (fileSecret, which
// differs in the wrong-secret test), keys derived from either, and every
// token the hub has issued.
func runTool(t *testing.T, h *testHub, fileSecret string, opts testlogin.Options, args ...string) run {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := testlogin.Run(context.Background(), args, &stdout, &stderr, opts)
	r := run{code, stdout.String(), stderr.String()}
	output := r.stdout + r.stderr
	forbidden := map[string]string{}
	for j, sec := range []string{h.secret, fileSecret} {
		key := challenge.DeriveUserSigningKey([]byte(sec))
		forbidden[fmt.Sprintf("secret %d", j)] = sec
		forbidden[fmt.Sprintf("key hex %d", j)] = hex.EncodeToString(key)
		forbidden[fmt.Sprintf("key base64 %d", j)] = base64.StdEncoding.EncodeToString(key)
		forbidden[fmt.Sprintf("key base64url %d", j)] = base64.RawURLEncoding.EncodeToString(key)
	}
	h.mu.Lock()
	for i, tok := range h.issued {
		forbidden[fmt.Sprintf("issued token %d", i)] = tok
		forbidden[fmt.Sprintf("issued token signature %d", i)] = tok[strings.LastIndex(tok, ".")+1:]
	}
	h.mu.Unlock()
	for name, v := range forbidden {
		if v != "" && strings.Contains(output, v) {
			t.Errorf("%s appears in output", name)
		}
	}
	return r
}

func writeSecretFile(t *testing.T, dir, secret string) string {
	t.Helper()
	p := filepath.Join(dir, "hub.env")
	if err := os.WriteFile(p, []byte("SCION_SERVER_SESSION_SECRET="+secret+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// TestMintAndCleanupAgainstHub runs the whole tool against the real
// handlers: test-login, /auth/me, the user lookup and the admin endpoint.
func TestMintAndCleanupAgainstHub(t *testing.T) {
	secret := randomSecret(t)
	h := startHub(t, secret, true)
	dir := t.TempDir()
	sf := writeSecretFile(t, dir, secret)
	out := filepath.Join(dir, "token")
	email := "pin-" + hex.EncodeToString([]byte(randomSecret(t))[:6]) + "@" + testlogin.EmailDomain

	r := runTool(t, h, secret, testlogin.Options{Email: email}, "mint", "--hub-url", h.url, "--secret-file", sf, "--out", out)
	if r.code != 0 {
		t.Fatalf("mint exit %d\nstdout:\n%s\nstderr:\n%s", r.code, r.stdout, r.stderr)
	}
	st, err := os.Stat(out)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Errorf("token file mode %04o", st.Mode().Perm())
	}
	tok, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	claims, err := h.srv.GetUserTokenService().ValidateUserToken(string(tok))
	if err != nil {
		t.Fatalf("hub does not accept the written token: %v", err)
	}
	if claims.Role != "member" || claims.TokenType != hub.TokenTypeAccess {
		t.Errorf("token role %q type %q", claims.Role, claims.TokenType)
	}
	u, err := h.store.GetUserByEmail(context.Background(), email)
	if err != nil {
		t.Fatalf("user not created: %v", err)
	}
	if u.Role != "member" || !strings.Contains(r.stdout, u.ID) {
		t.Errorf("stored role %q; stdout:\n%s", u.Role, r.stdout)
	}

	r = runTool(t, h, secret, testlogin.Options{}, "cleanup", "--hub-url", h.url, "--token-file", out)
	if r.code != 0 {
		t.Fatalf("cleanup exit %d\nstdout:\n%s\nstderr:\n%s", r.code, r.stdout, r.stderr)
	}
	if _, err := os.Lstat(out); !os.IsNotExist(err) {
		t.Error("token file still present after cleanup")
	}
}

// TestRefusesExistingAccount checks the fail-closed rule: test-login updates
// an existing user with the requested email, and the tool must reject that
// account and remove the token file.
func TestRefusesExistingAccount(t *testing.T) {
	secret := randomSecret(t)
	h := startHub(t, secret, true)
	dir := t.TempDir()
	sf := writeSecretFile(t, dir, secret)
	out := filepath.Join(dir, "token")
	email := "existing@" + testlogin.EmailDomain
	created := time.Now().Add(-time.Hour)
	if err := h.store.CreateUser(context.Background(), &store.User{
		ID: uuid.NewString(), Email: email, DisplayName: "existing", Role: "viewer",
		Status: "active", Created: created, LastLogin: created,
	}); err != nil {
		t.Fatal(err)
	}

	r := runTool(t, h, secret, testlogin.Options{Email: email}, "mint", "--hub-url", h.url, "--secret-file", sf, "--out", out)
	if r.code == 0 {
		t.Fatal("mint accepted a pre-existing account")
	}
	if !strings.Contains(r.stderr, "existing account") || strings.Contains(r.stderr, "admin must delete") {
		t.Errorf("stderr:\n%s", r.stderr)
	}
	if _, err := os.Lstat(out); !os.IsNotExist(err) {
		t.Error("token file left behind")
	}
}

func TestRefusesWhenTestLoginDisabled(t *testing.T) {
	secret := randomSecret(t)
	h := startHub(t, secret, false)
	dir := t.TempDir()
	out := filepath.Join(dir, "token")
	email := "disabled@" + testlogin.EmailDomain
	r := runTool(t, h, secret, testlogin.Options{Email: email}, "mint", "--hub-url", h.url, "--secret-file", writeSecretFile(t, dir, secret), "--out", out)
	if r.code == 0 || !strings.Contains(r.stderr, "not enabled") {
		t.Fatalf("exit %d, stderr:\n%s", r.code, r.stderr)
	}
	if _, err := h.store.GetUserByEmail(context.Background(), email); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("user lookup err = %v, want not found", err)
	}
}

func TestWrongSecretCreatesNothing(t *testing.T) {
	h := startHub(t, randomSecret(t), true)
	dir := t.TempDir()
	out := filepath.Join(dir, "token")
	email := "wrong@" + testlogin.EmailDomain
	other := randomSecret(t)
	r := runTool(t, h, other, testlogin.Options{Email: email}, "mint", "--hub-url", h.url, "--secret-file", writeSecretFile(t, dir, other), "--out", out)
	if r.code == 0 || !strings.Contains(r.stderr, "401") {
		t.Fatalf("exit %d, stderr:\n%s", r.code, r.stderr)
	}
	if _, err := h.store.GetUserByEmail(context.Background(), email); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("user lookup err = %v, want not found", err)
	}
	if _, err := os.Lstat(out); !os.IsNotExist(err) {
		t.Error("token file left behind")
	}
}

// TestCleanupRefusesSecretFile checks that cleanup pointed at the hub's
// secret file sends nothing and leaves the file in place.
func TestCleanupRefusesSecretFile(t *testing.T) {
	secret := randomSecret(t)
	h := startHub(t, secret, true)
	sf := writeSecretFile(t, t.TempDir(), secret)
	before, err := os.ReadFile(sf)
	if err != nil {
		t.Fatal(err)
	}
	r := runTool(t, h, secret, testlogin.Options{}, "cleanup", "--hub-url", h.url, "--token-file", sf)
	if r.code == 0 {
		t.Fatal("cleanup accepted a secret file")
	}
	if h.requests != 0 {
		t.Errorf("hub received %d requests", h.requests)
	}
	if after, err := os.ReadFile(sf); err != nil || !bytes.Equal(before, after) {
		t.Error("secret file was modified or removed")
	}
}

// TestEveryRunIsChecked keeps the output check on every run: the tests may
// invoke the tool only through runTool.
func TestEveryRunIsChecked(t *testing.T) {
	data, err := os.ReadFile("hubpin_test.go")
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(data), "testlogin."+"Run("); n != 1 {
		t.Errorf("found %d direct calls of testlogin.Run; call it only through runTool", n)
	}
}

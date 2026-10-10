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
	"encoding/hex"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
	srv   *hub.Server
	url   string
	store store.Store
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

	hs := httptest.NewServer(ws.Handler())
	t.Cleanup(hs.Close)
	return &testHub{srv: srv, url: hs.URL, store: srv.GetStore()}
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

func runTool(t *testing.T, opts testlogin.Options, args ...string) run {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := testlogin.Run(context.Background(), args, &stdout, &stderr, opts)
	return run{code, stdout.String(), stderr.String()}
}

func writeSecretFile(t *testing.T, dir, secret string) string {
	t.Helper()
	p := filepath.Join(dir, "hub.env")
	if err := os.WriteFile(p, []byte("SCION_SERVER_SESSION_SECRET="+secret+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func assertNoSecret(t *testing.T, secret string, r run) {
	t.Helper()
	if strings.Contains(r.stdout+r.stderr, secret) {
		t.Error("session secret appears in output")
	}
	key := challenge.DeriveUserSigningKey([]byte(secret))
	if strings.Contains(r.stdout+r.stderr, hex.EncodeToString(key)) {
		t.Error("derived key appears in output")
	}
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

	r := runTool(t, testlogin.Options{Email: email}, "mint", "--hub-url", h.url, "--secret-file", sf, "--out", out)
	assertNoSecret(t, secret, r)
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
	if strings.Contains(r.stdout+r.stderr, string(tok)) {
		t.Error("access token appears in output")
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

	r = runTool(t, testlogin.Options{}, "cleanup", "--hub-url", h.url, "--token-file", out)
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
		ID: "pre-existing-user", Email: email, DisplayName: "existing", Role: "viewer",
		Status: "active", Created: created, LastLogin: created,
	}); err != nil {
		t.Fatal(err)
	}

	r := runTool(t, testlogin.Options{Email: email}, "mint", "--hub-url", h.url, "--secret-file", sf, "--out", out)
	assertNoSecret(t, secret, r)
	if r.code == 0 {
		t.Fatal("mint accepted a pre-existing account")
	}
	if !strings.Contains(r.stderr, "existing account") {
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
	r := runTool(t, testlogin.Options{Email: email}, "mint", "--hub-url", h.url, "--secret-file", writeSecretFile(t, dir, secret), "--out", out)
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
	r := runTool(t, testlogin.Options{Email: email}, "mint", "--hub-url", h.url, "--secret-file", writeSecretFile(t, dir, other), "--out", out)
	assertNoSecret(t, other, r)
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

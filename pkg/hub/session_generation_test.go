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

package hub

// Session generation in hub user JWTs: every hub JWT carries the user's
// session generation (claim sg) and the hub refuses a token whose claim
// differs from the users row in either direction. A token without the claim
// reads as 0. Each test names, in its comment, the check whose removal makes
// it fail.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4/jwt"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/GoogleCloudPlatform/scion/pkg/hub/authctx"
	"github.com/GoogleCloudPlatform/scion/pkg/store"
)

// sessionGenFixture is a SQLite-backed hub with one active member user.
type sessionGenFixture struct {
	srv  *Server
	s    store.Store
	user *store.User
}

func newSessionGenFixture(t *testing.T) *sessionGenFixture {
	t.Helper()
	srv, s := testServer(t)
	id := tid("sessgen-user")
	u := &store.User{
		ID:          id,
		Email:       id + "@example.com",
		DisplayName: "Session Gen User",
		Role:        store.UserRoleMember,
		Status:      store.UserStatusActive,
		Created:     time.Now(),
	}
	require.NoError(t, s.CreateUser(context.Background(), u))
	return &sessionGenFixture{srv: srv, s: s, user: u}
}

// mintPair mints an access and refresh token pair for the fixture user with
// the given session generation and client type.
func (f *sessionGenFixture) mintPair(t *testing.T, gen int64, ct ClientType) (access, refresh string) {
	t.Helper()
	u := f.user
	access, refresh, _, err := f.srv.userTokenService.MintTokenPair(u.ID, u.Email, u.DisplayName, u.Role, gen, ct)
	require.NoError(t, err)
	return access, refresh
}

// sessionGenClaimlessClaims mirrors UserTokenClaims without the sg claim:
// the shape of a token minted before the claim existed.
type sessionGenClaimlessClaims struct {
	jwt.Claims
	UserID      string        `json:"uid"`
	Email       string        `json:"email"`
	DisplayName string        `json:"name,omitempty"`
	Role        string        `json:"role"`
	TokenType   UserTokenType `json:"type"`
	ClientType  ClientType    `json:"client"`
}

// mintClaimless signs a token for the fixture user that has no sg claim.
func (f *sessionGenFixture) mintClaimless(t *testing.T, tt UserTokenType, ct ClientType) string {
	t.Helper()
	now := time.Now()
	u := f.user
	tok, err := jwt.Signed(f.srv.userTokenService.signer).Claims(sessionGenClaimlessClaims{
		Claims: jwt.Claims{
			Issuer:    UserTokenIssuer,
			Subject:   u.ID,
			Audience:  jwt.Audience{UserTokenAudience},
			IssuedAt:  jwt.NewNumericDate(now),
			Expiry:    jwt.NewNumericDate(now.Add(time.Hour)),
			NotBefore: jwt.NewNumericDate(now),
			ID:        generateUserTokenID(),
		},
		UserID: u.ID, Email: u.Email, DisplayName: u.DisplayName, Role: u.Role,
		TokenType: tt, ClientType: ct,
	}).Serialize()
	require.NoError(t, err)
	claims, err := f.srv.userTokenService.ValidateUserToken(tok)
	require.NoError(t, err)
	require.Zero(t, claims.SessionGeneration, "a token without the sg claim decodes to 0")
	return tok
}

// revoke calls the admin revoke-sessions endpoint for the fixture user and
// returns the user's new session generation.
func (f *sessionGenFixture) revoke(t *testing.T) int64 {
	t.Helper()
	before := f.generation(t)
	rec := doRequest(t, f.srv, http.MethodPost, "/api/v1/users/"+f.user.ID+"/revoke-sessions", map[string]interface{}{})
	require.Equal(t, http.StatusOK, rec.Code, "revoke-sessions: %s", rec.Body.String())
	after := f.generation(t)
	require.Equal(t, before+1, after, "revoke-sessions increments the session generation")
	return after
}

func (f *sessionGenFixture) generation(t *testing.T) int64 {
	t.Helper()
	u, err := f.s.GetUser(context.Background(), f.user.ID)
	require.NoError(t, err)
	return u.SessionGeneration
}

// me calls GET /api/v1/auth/me with token as the bearer credential.
func (f *sessionGenFixture) me(t *testing.T, token string) *httptest.ResponseRecorder {
	t.Helper()
	return doRequestWithToken(t, f.srv, token, http.MethodGet, "/api/v1/auth/me", nil)
}

// requireRefusedAsRevoked asserts the bearer arm's session generation refusal.
func requireRefusedAsRevoked(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	require.Equal(t, http.StatusUnauthorized, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), "session revoked")
}

// assertDeniedAfterRevoke checks that token is accepted on an ordinary API
// route before a revoke and refused after it.
func (f *sessionGenFixture) assertDeniedAfterRevoke(t *testing.T, token string) {
	t.Helper()
	rec := f.me(t, token)
	require.Equal(t, http.StatusOK, rec.Code, "accepted before the revoke: %s", rec.Body.String())
	f.revoke(t)
	requireRefusedAsRevoked(t, f.me(t, token))
}

// Fails if the session generation check is removed from the JWT bearer arm
// (UnifiedAuthMiddleware): a CLI access token minted before the revoke would
// still be accepted.
func TestSessionGen_BearerArm_CLIAccessToken_DeniedAfterRevoke(t *testing.T) {
	f := newSessionGenFixture(t)
	access, _ := f.mintPair(t, 0, ClientTypeCLI)
	claims, err := f.srv.userTokenService.ValidateUserToken(access)
	require.NoError(t, err)
	require.Equal(t, TokenTypeCLI, claims.TokenType)
	f.assertDeniedAfterRevoke(t, access)
}

// Fails if the session generation check is removed from the JWT bearer arm:
// a web access token minted before the revoke would still be accepted.
func TestSessionGen_BearerArm_WebAccessToken_DeniedAfterRevoke(t *testing.T) {
	f := newSessionGenFixture(t)
	access, _ := f.mintPair(t, 0, ClientTypeWeb)
	claims, err := f.srv.userTokenService.ValidateUserToken(access)
	require.NoError(t, err)
	require.Equal(t, TokenTypeAccess, claims.TokenType)
	f.assertDeniedAfterRevoke(t, access)
}

// Fails if the session generation check is removed from the JWT bearer arm:
// a refresh token presented as a bearer credential, minted before the
// revoke, would still be accepted.
func TestSessionGen_BearerArm_RefreshTokenAsBearer_DeniedAfterRevoke(t *testing.T) {
	f := newSessionGenFixture(t)
	_, refresh := f.mintPair(t, 0, ClientTypeCLI)
	claims, err := f.srv.userTokenService.ValidateUserToken(refresh)
	require.NoError(t, err)
	require.Equal(t, TokenTypeRefresh, claims.TokenType)
	f.assertDeniedAfterRevoke(t, refresh)
}

// Fails if the session generation check is removed from the JWT bearer arm:
// an API access token of the GE-exchange shape (MintAccessTokenWithTTL,
// client type api), minted before the revoke, would still be accepted.
func TestSessionGen_BearerArm_GEExchangeToken_DeniedAfterRevoke(t *testing.T) {
	f := newSessionGenFixture(t)
	u := f.user
	tok, _, err := f.srv.userTokenService.MintAccessTokenWithTTL(u.ID, u.Email, u.DisplayName, u.Role, 0, ClientTypeAPI, 10*time.Minute)
	require.NoError(t, err)
	f.assertDeniedAfterRevoke(t, tok)
}

// sessionGenOAuthTokens obtains a token pair for the fixture user through
// the real POST /api/v1/auth/token issuance path (mocked Google exchange).
func (f *sessionGenFixture) sessionGenOAuthTokens(t *testing.T, clientType string) AuthTokenResponse {
	t.Helper()
	f.srv.oauthService = NewOAuthService(OAuthConfig{
		Web: OAuthClientConfig{Google: OAuthProviderConfig{ClientID: "test-web-id", ClientSecret: "test-web-secret"}},
		CLI: OAuthClientConfig{Google: OAuthProviderConfig{ClientID: "test-cli-id", ClientSecret: "test-cli-secret"}},
	}, nil)
	f.srv.oauthService.httpClient = &http.Client{Transport: &mockOAuthTransport{
		tokenJSON:    `{"access_token":"mock-token","token_type":"Bearer","expires_in":3600}`,
		userinfoJSON: `{"id":"sub-` + f.user.ID + `","email":"` + f.user.Email + `","verified_email":true,"name":"Session Gen User"}`,
	}}
	rec := doRequestNoAuth(t, f.srv, http.MethodPost, "/api/v1/auth/token", AuthTokenRequest{
		Provider:    "google",
		Code:        "test-code",
		RedirectURI: "http://localhost:8080/callback",
		GrantType:   "authorization_code",
		ClientType:  clientType,
	})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var resp AuthTokenResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.NotEmpty(t, resp.AccessToken)
	require.NotEmpty(t, resp.RefreshToken)
	return resp
}

// Fails if an issuance path mints with a session generation other than the
// users row's (for example 0): a token issued after the revoke would then be
// refused. Each token type is obtained through the real OAuth token
// endpoint, after the revoke.
func TestSessionGen_BearerArm_TokenIssuedAfterRevoke_Accepted(t *testing.T) {
	cases := []struct {
		name       string
		clientType string
		pick       func(AuthTokenResponse) string
		wantType   UserTokenType
	}{
		{"cli_access", "cli", func(r AuthTokenResponse) string { return r.AccessToken }, TokenTypeCLI},
		{"web_access", "web", func(r AuthTokenResponse) string { return r.AccessToken }, TokenTypeAccess},
		{"refresh", "cli", func(r AuthTokenResponse) string { return r.RefreshToken }, TokenTypeRefresh},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newSessionGenFixture(t)
			gen := f.revoke(t)
			tok := tc.pick(f.sessionGenOAuthTokens(t, tc.clientType))
			claims, err := f.srv.userTokenService.ValidateUserToken(tok)
			require.NoError(t, err)
			require.Equal(t, tc.wantType, claims.TokenType)
			assert.Equal(t, gen, claims.SessionGeneration, "issued with the users row's generation")
			rec := f.me(t, tok)
			assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		})
	}
}

// Fails if the bearer arm refuses a never-revoked user's tokens (generation
// 0 on both sides), for each token type.
func TestSessionGen_BearerArm_NeverRevoked_Accepted(t *testing.T) {
	f := newSessionGenFixture(t)
	require.Zero(t, f.generation(t))
	cliAccess, cliRefresh := f.mintPair(t, 0, ClientTypeCLI)
	webAccess, _ := f.mintPair(t, 0, ClientTypeWeb)
	for name, tok := range map[string]string{"cli_access": cliAccess, "web_access": webAccess, "refresh": cliRefresh} {
		t.Run(name, func(t *testing.T) {
			rec := f.me(t, tok)
			assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		})
	}
}

// Fails if the session generation check is removed from the JWT bearer arm,
// or if a missing sg claim stops reading as 0: a token without the claim
// must be refused for a user whose sessions were ever revoked.
func TestSessionGen_BearerArm_ClaimlessToken_EverRevokedUser_Denied(t *testing.T) {
	f := newSessionGenFixture(t)
	f.revoke(t)
	for name, tok := range map[string]string{
		"cli_access": f.mintClaimless(t, TokenTypeCLI, ClientTypeCLI),
		"web_access": f.mintClaimless(t, TokenTypeAccess, ClientTypeWeb),
		"refresh":    f.mintClaimless(t, TokenTypeRefresh, ClientTypeCLI),
	} {
		t.Run(name, func(t *testing.T) {
			requireRefusedAsRevoked(t, f.me(t, tok))
		})
	}
}

// Fails if a token without the sg claim is refused for a user whose sessions
// were never revoked (a missing claim must read as 0, equal to the row).
func TestSessionGen_BearerArm_ClaimlessToken_NeverRevokedUser_Accepted(t *testing.T) {
	f := newSessionGenFixture(t)
	require.Zero(t, f.generation(t))
	for name, tok := range map[string]string{
		"cli_access": f.mintClaimless(t, TokenTypeCLI, ClientTypeCLI),
		"web_access": f.mintClaimless(t, TokenTypeAccess, ClientTypeWeb),
		"refresh":    f.mintClaimless(t, TokenTypeRefresh, ClientTypeCLI),
	} {
		t.Run(name, func(t *testing.T) {
			rec := f.me(t, tok)
			assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		})
	}
}

// Fails if the session generation check is removed from the JWT bearer arm,
// or if the comparison refuses only when the row is ahead of the claim
// (row > claim) instead of on any inequality: a token whose claim is ahead
// of the row must be refused.
func TestSessionGen_BearerArm_ClaimAheadOfRow_Denied(t *testing.T) {
	f := newSessionGenFixture(t)
	t.Run("row_0_claim_1", func(t *testing.T) {
		access, _ := f.mintPair(t, 1, ClientTypeCLI)
		requireRefusedAsRevoked(t, f.me(t, access))
	})
	t.Run("row_2_claim_3", func(t *testing.T) {
		f.revoke(t)
		row := f.revoke(t)
		require.Equal(t, int64(2), row)
		access, _ := f.mintPair(t, row+1, ClientTypeWeb)
		requireRefusedAsRevoked(t, f.me(t, access))
		current, _ := f.mintPair(t, row, ClientTypeWeb)
		rec := f.me(t, current)
		assert.Equal(t, http.StatusOK, rec.Code, "a token at the row's generation is accepted: %s", rec.Body.String())
	})
}

// authctxProbe runs the hub's own auth configuration in front of a handler
// that records the session generation the request context carries.
type authctxProbe struct {
	reached bool
	gen     int64
	ok      bool
}

func (p *authctxProbe) serve(t *testing.T, cfg AuthConfig, req *http.Request) *httptest.ResponseRecorder {
	t.Helper()
	h := UnifiedAuthMiddleware(cfg)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p.reached = true
		p.gen, p.ok = authctx.SessionGeneration(r.Context())
		w.WriteHeader(http.StatusOK)
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func sessionGenProbeRequest(bearer string) *http.Request {
	req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/me", nil)
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	return req
}

// Fails if the JWT bearer arm stops recording the users row's session
// generation in the request context (authctx.WithSessionGeneration is
// removed or given another value).
func TestSessionGen_BearerArm_SetsAuthctxRowValue(t *testing.T) {
	f := newSessionGenFixture(t)
	t.Run("generation_0", func(t *testing.T) {
		access, _ := f.mintPair(t, 0, ClientTypeCLI)
		var p authctxProbe
		rec := p.serve(t, f.srv.authConfig, sessionGenProbeRequest(access))
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		require.True(t, p.reached)
		assert.True(t, p.ok, "a hub-JWT request carries a session generation")
		assert.Equal(t, int64(0), p.gen)
	})
	t.Run("generation_2", func(t *testing.T) {
		f.revoke(t)
		row := f.revoke(t)
		access, _ := f.mintPair(t, row, ClientTypeWeb)
		var p authctxProbe
		rec := p.serve(t, f.srv.authConfig, sessionGenProbeRequest(access))
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		require.True(t, p.reached)
		assert.True(t, p.ok)
		assert.Equal(t, row, p.gen, "the value is the users row's generation")
	})
}

// Fails if a session generation is recorded for a request that was not
// authenticated by the hub-JWT arm: user access tokens (UAT), the dev token
// and proxy-authenticated requests must leave it unset.
func TestSessionGen_BearerArm_UAT_DevToken_Proxy_NoAuthctx(t *testing.T) {
	f := newSessionGenFixture(t)

	t.Run("uat", func(t *testing.T) {
		key, _, err := f.srv.uatService.CreateTokenWithParams(rs4MintContext(f.user.ID), CreateTokenParams{
			UserID: f.user.ID, Name: "sessgen", Boundary: TokenBoundary{Kind: BoundaryKindHub}, Scopes: []string{"inbox:read"},
		})
		require.NoError(t, err)
		var p authctxProbe
		rec := p.serve(t, f.srv.authConfig, sessionGenProbeRequest(key))
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		require.True(t, p.reached)
		assert.False(t, p.ok, "a UAT request carries no session generation")
	})

	t.Run("dev_token", func(t *testing.T) {
		var p authctxProbe
		rec := p.serve(t, f.srv.authConfig, sessionGenProbeRequest(testDevToken))
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		require.True(t, p.reached)
		assert.False(t, p.ok, "a dev-token request carries no session generation")
	})

	t.Run("proxy", func(t *testing.T) {
		cfg := f.srv.authConfig
		cfg.ProxyAuthenticator = &mockProxyAuthenticator{user: &ProxyUserInfo{Email: f.user.Email, Subject: "proxy-sub"}}
		cfg.ProxyUserProvisioner = func(_ context.Context, info *ProxyUserInfo) (UserIdentity, error) {
			return NewAuthenticatedUser(f.user.ID, info.Email, f.user.DisplayName, f.user.Role, "web"), nil
		}
		var p authctxProbe
		rec := p.serve(t, cfg, sessionGenProbeRequest(""))
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		require.True(t, p.reached)
		assert.False(t, p.ok, "a proxy-authenticated request carries no session generation")
	})
}

// Fails if hub.New stops wiring its store argument into the auth
// middleware's UserStore (server.go, AuthConfig{UserStore: s}). Without it
// the JWT bearer arm skips the users-row block, including the session
// generation check; see the comment at that skip in auth.go.
func TestNew_WiresAuthUserStoreFromStore(t *testing.T) {
	srv, s := testServer(t)
	require.NotNil(t, srv.authConfig.UserStore, "the auth middleware has a user store")
	assert.True(t, any(srv.authConfig.UserStore) == any(s), "the auth middleware's user store is the store passed to New")
}

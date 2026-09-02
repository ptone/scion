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

package hub

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/GoogleCloudPlatform/scion/pkg/config"
	"github.com/GoogleCloudPlatform/scion/pkg/store"
	"github.com/go-jose/go-jose/v4/jwt"
)

// F-REV-04: Direct regression test for the JWT suspension store-failure path.
//
// The JWT auth middleware checks user suspension status by calling
// UserStore.GetUser. Prior to the fix (F-REV-03, commit 5dff865), a store
// error caused the middleware to silently continue, admitting suspended users.
// The fix returns 503 on non-ErrNotFound store errors. This test proves that
// behavior.

// stubUserStore implements the subset of store.UserStore needed by the auth
// middleware. It returns configurable responses from GetUser and GetUserByEmail.
type stubUserStore struct {
	store.UserStore
	getUser        func(ctx context.Context, id string) (*store.User, error)
	getUserByEmail func(ctx context.Context, email string) (*store.User, error)
}

func (s *stubUserStore) GetUser(ctx context.Context, id string) (*store.User, error) {
	if s.getUser != nil {
		return s.getUser(ctx, id)
	}
	return nil, store.ErrNotFound
}

func (s *stubUserStore) GetUserByEmail(ctx context.Context, email string) (*store.User, error) {
	if s.getUserByEmail != nil {
		return s.getUserByEmail(ctx, email)
	}
	return nil, store.ErrNotFound
}

func TestJWTAuth_StoreError_Returns503(t *testing.T) {
	userTokenSvc, err := NewUserTokenService(UserTokenConfig{})
	if err != nil {
		t.Fatalf("failed to create user token service: %v", err)
	}

	accessToken, _, _, err := userTokenSvc.GenerateTokenPair(
		"user-store-err", "store-err@example.com", "Store Err User", "member", ClientTypeWeb,
	)
	if err != nil {
		t.Fatalf("failed to generate tokens: %v", err)
	}

	storeErr := errors.New("database connection lost")

	cfg := AuthConfig{
		Mode:         "production",
		UserTokenSvc: userTokenSvc,
		UserStore: &stubUserStore{
			getUser: func(_ context.Context, _ string) (*store.User, error) {
				return nil, storeErr
			},
		},
		Debug: false,
	}

	middleware := UnifiedAuthMiddleware(cfg)
	handler := middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("handler must not be reached on store error")
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/test", nil)
	req.Header.Set("Authorization", "Bearer "+accessToken)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503 on store error, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestJWTAuth_SuspendedUser_Returns403(t *testing.T) {
	userTokenSvc, err := NewUserTokenService(UserTokenConfig{})
	if err != nil {
		t.Fatalf("failed to create user token service: %v", err)
	}

	accessToken, _, _, err := userTokenSvc.GenerateTokenPair(
		"user-suspended", "suspended@example.com", "Suspended User", "member", ClientTypeWeb,
	)
	if err != nil {
		t.Fatalf("failed to generate tokens: %v", err)
	}

	cfg := AuthConfig{
		Mode:         "production",
		UserTokenSvc: userTokenSvc,
		UserStore: &stubUserStore{
			getUser: func(_ context.Context, _ string) (*store.User, error) {
				return &store.User{
					ID:     "user-suspended",
					Email:  "suspended@example.com",
					Status: store.UserStatusSuspended,
				}, nil
			},
		},
		Debug: false,
	}

	middleware := UnifiedAuthMiddleware(cfg)
	handler := middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("handler must not be reached for suspended user")
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/test", nil)
	req.Header.Set("Authorization", "Bearer "+accessToken)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Errorf("expected 403 for suspended user, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestJWTAuth_ActiveUser_PassesThrough(t *testing.T) {
	userTokenSvc, err := NewUserTokenService(UserTokenConfig{})
	if err != nil {
		t.Fatalf("failed to create user token service: %v", err)
	}

	accessToken, _, _, err := userTokenSvc.GenerateTokenPair(
		"user-active", "active@example.com", "Active User", "member", ClientTypeWeb,
	)
	if err != nil {
		t.Fatalf("failed to generate tokens: %v", err)
	}

	cfg := AuthConfig{
		Mode:         "production",
		UserTokenSvc: userTokenSvc,
		UserStore: &stubUserStore{
			getUser: func(_ context.Context, _ string) (*store.User, error) {
				return &store.User{
					ID:     "user-active",
					Email:  "active@example.com",
					Status: store.UserStatusActive,
				}, nil
			},
		},
		Debug: false,
	}

	middleware := UnifiedAuthMiddleware(cfg)
	var gotIdentity Identity
	handler := middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotIdentity = GetIdentityFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/test", nil)
	req.Header.Set("Authorization", "Bearer "+accessToken)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200 for active user, got %d: %s", rec.Code, rec.Body.String())
	}
	if gotIdentity == nil {
		t.Fatal("expected identity in context")
	}
	if gotIdentity.ID() != "user-active" {
		t.Errorf("expected user ID 'user-active', got %q", gotIdentity.ID())
	}
}

func TestJWTAuth_DeletedUser_PassesThroughToHandler(t *testing.T) {
	userTokenSvc, err := NewUserTokenService(UserTokenConfig{})
	if err != nil {
		t.Fatalf("failed to create user token service: %v", err)
	}

	accessToken, _, _, err := userTokenSvc.GenerateTokenPair(
		"user-deleted", "deleted@example.com", "Deleted User", "member", ClientTypeWeb,
	)
	if err != nil {
		t.Fatalf("failed to generate tokens: %v", err)
	}

	cfg := AuthConfig{
		Mode:         "production",
		UserTokenSvc: userTokenSvc,
		UserStore: &stubUserStore{
			getUser: func(_ context.Context, _ string) (*store.User, error) {
				return nil, store.ErrNotFound
			},
		},
		Debug: false,
	}

	middleware := UnifiedAuthMiddleware(cfg)
	var handlerReached bool
	handler := middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handlerReached = true
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/test", nil)
	req.Header.Set("Authorization", "Bearer "+accessToken)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	// ErrNotFound (deleted user) falls through — downstream handlers will
	// fail closed when the identity has no matching store record.
	if !handlerReached {
		t.Error("expected handler to be reached for deleted user (ErrNotFound)")
	}
	if rec.Code != http.StatusOK {
		t.Errorf("expected 200 for deleted user pass-through, got %d", rec.Code)
	}
}

// D4 circuit-breaker: legacy proxy path must check suspension.
func TestLegacyProxyAuth_SuspendedUser_Returns403(t *testing.T) {
	cfg := AuthConfig{
		Mode:           "production",
		TrustedProxies: []string{"192.0.2.0/24"},
		UserStore: &stubUserStore{
			getUser: func(_ context.Context, _ string) (*store.User, error) {
				return &store.User{
					ID:     "legacy-proxy-suspended",
					Email:  "suspended@example.com",
					Status: store.UserStatusSuspended,
				}, nil
			},
		},
		Debug: false,
	}

	middleware := UnifiedAuthMiddleware(cfg)
	handler := middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("handler must not be reached for suspended user via legacy proxy")
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/test", nil)
	req.RemoteAddr = "192.0.2.1:1234"
	req.Header.Set("X-Forwarded-User-Id", "legacy-proxy-suspended")
	req.Header.Set("X-Forwarded-User-Email", "suspended@example.com")
	req.Header.Set("X-Forwarded-User-Name", "Suspended Legacy")

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Errorf("expected 403 for suspended user via legacy proxy, got %d: %s", rec.Code, rec.Body.String())
	}
}

// C-2: Legacy proxy path must fail closed on store errors.
func TestLegacyProxyAuth_StoreError_Returns503(t *testing.T) {
	storeErr := errors.New("database connection lost")

	cfg := AuthConfig{
		Mode:           "production",
		TrustedProxies: []string{"192.0.2.0/24"},
		UserStore: &stubUserStore{
			getUser: func(_ context.Context, _ string) (*store.User, error) {
				return nil, storeErr
			},
		},
		Debug: false,
	}

	middleware := UnifiedAuthMiddleware(cfg)
	handler := middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("handler must not be reached on store error via legacy proxy")
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/test", nil)
	req.RemoteAddr = "192.0.2.1:1234"
	req.Header.Set("X-Forwarded-User-Id", "legacy-proxy-store-err")
	req.Header.Set("X-Forwarded-User-Email", "store-err@example.com")
	req.Header.Set("X-Forwarded-User-Name", "Store Err User")

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503 on store error via legacy proxy, got %d: %s", rec.Code, rec.Body.String())
	}
}

// C-2: Legacy proxy with no UserStore should still pass through (startup scenario).
func TestLegacyProxyAuth_NoUserStore_PassesThrough(t *testing.T) {
	cfg := AuthConfig{
		Mode:           "production",
		TrustedProxies: []string{"192.0.2.0/24"},
		UserStore:      nil,
		Debug:          false,
	}

	middleware := UnifiedAuthMiddleware(cfg)
	var handlerReached bool
	handler := middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handlerReached = true
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/test", nil)
	req.RemoteAddr = "192.0.2.1:1234"
	req.Header.Set("X-Forwarded-User-Id", "legacy-proxy-nostore")
	req.Header.Set("X-Forwarded-User-Email", "nostore@example.com")
	req.Header.Set("X-Forwarded-User-Name", "No Store User")

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if !handlerReached {
		t.Error("expected handler to be reached when no UserStore configured")
	}
	if rec.Code != http.StatusOK {
		t.Errorf("expected 200 for legacy proxy with no UserStore, got %d", rec.Code)
	}
}

func TestJWTAuth_NoUserStore_PassesThrough(t *testing.T) {
	userTokenSvc, err := NewUserTokenService(UserTokenConfig{})
	if err != nil {
		t.Fatalf("failed to create user token service: %v", err)
	}

	accessToken, _, _, err := userTokenSvc.GenerateTokenPair(
		"user-nostore", "nostore@example.com", "No Store User", "member", ClientTypeWeb,
	)
	if err != nil {
		t.Fatalf("failed to generate tokens: %v", err)
	}

	cfg := AuthConfig{
		Mode:         "production",
		UserTokenSvc: userTokenSvc,
		UserStore:    nil, // no user store configured
		Debug:        false,
	}

	middleware := UnifiedAuthMiddleware(cfg)
	var handlerReached bool
	handler := middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handlerReached = true
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/test", nil)
	req.Header.Set("Authorization", "Bearer "+accessToken)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if !handlerReached {
		t.Error("expected handler to be reached when no UserStore is configured")
	}
	if rec.Code != http.StatusOK {
		t.Errorf("expected 200 when no UserStore, got %d", rec.Code)
	}
}

// ---------------------------------------------------------------------------
// Item-4 / F-1: Federation suspension tests
// ---------------------------------------------------------------------------

// setupFederationUserAuth creates a FederationAuthenticator configured with
// issuer_type=user and returns the helpers needed to sign tokens and build
// the AuthConfig. The returned token has email=feduser@example.com.
func setupFederationUserAuth(t *testing.T) (token string, fedAuthPtr *atomic.Pointer[FederationAuthenticator]) {
	t.Helper()

	privKey, jwksSrv, kid := setupFederationTestServer(t)
	issuer := jwksSrv.URL

	fedCfg := config.FederationConfig{
		Enabled: true,
		TrustedIssuers: []config.TrustedIssuerConfig{
			{
				IssuerURL:        issuer,
				JWKSURL:          jwksSrv.URL,
				ExpectedAudience: "test-audience",
				IssuerType:       "user",
			},
		},
	}

	auth := newTestAuthenticatorWithConfig(t, fedCfg, "test-audience")

	fedPtr := &atomic.Pointer[FederationAuthenticator]{}
	fedPtr.Store(auth)

	now := time.Now()
	claims := federationClaims{
		Claims: jwt.Claims{
			Issuer:    issuer,
			Subject:   "fed-user-sub-1",
			Audience:  jwt.Audience{"test-audience"},
			IssuedAt:  jwt.NewNumericDate(now.Add(-1 * time.Minute)),
			Expiry:    jwt.NewNumericDate(now.Add(5 * time.Minute)),
			NotBefore: jwt.NewNumericDate(now.Add(-1 * time.Minute)),
		},
		Email: "feduser@example.com",
		Name:  "Fed User",
	}
	tok := signGenericToken(t, privKey, kid, claims)
	return tok, fedPtr
}

// TestFederationAuth_SuspendedUser_Returns403 verifies that a federated user
// whose local account is suspended is rejected with 403 "user_suspended".
func TestFederationAuth_SuspendedUser_Returns403(t *testing.T) {
	token, fedPtr := setupFederationUserAuth(t)

	cfg := AuthConfig{
		Mode:           "production",
		FederationAuth: fedPtr,
		UserStore: &stubUserStore{
			getUserByEmail: func(_ context.Context, email string) (*store.User, error) {
				if email == "feduser@example.com" {
					return &store.User{
						ID:     "fed-user-local-1",
						Email:  "feduser@example.com",
						Status: store.UserStatusSuspended,
					}, nil
				}
				return nil, store.ErrNotFound
			},
		},
		Debug: false,
	}

	middleware := UnifiedAuthMiddleware(cfg)
	handler := middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("handler must not be reached for suspended federated user")
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/test", nil)
	req.Header.Set(FederationTokenHeader, token)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Errorf("expected 403 for suspended federated user, got %d: %s", rec.Code, rec.Body.String())
	}
}

// TestFederationAuth_StoreError_Returns503 verifies that a store error during
// the federation suspension check returns 503 (fail closed).
func TestFederationAuth_StoreError_Returns503(t *testing.T) {
	token, fedPtr := setupFederationUserAuth(t)

	cfg := AuthConfig{
		Mode:           "production",
		FederationAuth: fedPtr,
		UserStore: &stubUserStore{
			getUserByEmail: func(_ context.Context, _ string) (*store.User, error) {
				return nil, errors.New("database connection lost")
			},
		},
		Debug: false,
	}

	middleware := UnifiedAuthMiddleware(cfg)
	handler := middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("handler must not be reached on store error for federated user")
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/test", nil)
	req.Header.Set(FederationTokenHeader, token)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503 on store error for federated user, got %d: %s", rec.Code, rec.Body.String())
	}
}

// TestFederationAuth_ActiveUser_PassesThrough verifies that a federated user
// with an active local account passes through to the handler.
func TestFederationAuth_ActiveUser_PassesThrough(t *testing.T) {
	token, fedPtr := setupFederationUserAuth(t)

	cfg := AuthConfig{
		Mode:           "production",
		FederationAuth: fedPtr,
		UserStore: &stubUserStore{
			getUserByEmail: func(_ context.Context, email string) (*store.User, error) {
				if email == "feduser@example.com" {
					return &store.User{
						ID:     "fed-user-local-1",
						Email:  "feduser@example.com",
						Status: store.UserStatusActive,
					}, nil
				}
				return nil, store.ErrNotFound
			},
		},
		Debug: false,
	}

	middleware := UnifiedAuthMiddleware(cfg)
	var handlerReached bool
	handler := middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handlerReached = true
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/test", nil)
	req.Header.Set(FederationTokenHeader, token)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200 for active federated user, got %d: %s", rec.Code, rec.Body.String())
	}
	if !handlerReached {
		t.Error("expected handler to be reached for active federated user")
	}
}

// TestFederationAuth_NoLocalUser_PassesThrough verifies that a federated user
// with no local account (ErrNotFound) is allowed through — federation tokens
// may represent users not yet provisioned locally.
func TestFederationAuth_NoLocalUser_PassesThrough(t *testing.T) {
	token, fedPtr := setupFederationUserAuth(t)

	cfg := AuthConfig{
		Mode:           "production",
		FederationAuth: fedPtr,
		UserStore: &stubUserStore{
			getUserByEmail: func(_ context.Context, _ string) (*store.User, error) {
				return nil, store.ErrNotFound
			},
		},
		Debug: false,
	}

	middleware := UnifiedAuthMiddleware(cfg)
	var handlerReached bool
	handler := middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handlerReached = true
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/test", nil)
	req.Header.Set(FederationTokenHeader, token)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200 for unprovisioned federated user, got %d: %s", rec.Code, rec.Body.String())
	}
	if !handlerReached {
		t.Error("expected handler to be reached for unprovisioned federated user")
	}
}

// TestFederationAuth_NoUserStore_PassesThrough verifies that when UserStore is
// nil (startup/test scenario), the federation path still authenticates without
// a suspension check.
func TestFederationAuth_NoUserStore_PassesThrough(t *testing.T) {
	token, fedPtr := setupFederationUserAuth(t)

	cfg := AuthConfig{
		Mode:           "production",
		FederationAuth: fedPtr,
		UserStore:      nil,
		Debug:          false,
	}

	middleware := UnifiedAuthMiddleware(cfg)
	var handlerReached bool
	handler := middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handlerReached = true
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/test", nil)
	req.Header.Set(FederationTokenHeader, token)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200 for federation with no UserStore, got %d: %s", rec.Code, rec.Body.String())
	}
	if !handlerReached {
		t.Error("expected handler to be reached with no UserStore configured")
	}
}

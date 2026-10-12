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
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
)

// headerProxyAuthResult runs one request through UnifiedAuthMiddleware and
// reports the user identity the next handler saw (nil when it was not
// reached or saw none) and the response code.
func headerProxyAuthResult(t *testing.T, cfg AuthConfig, remoteAddr string, withHeaders bool) (UserIdentity, int) {
	t.Helper()
	var seen UserIdentity
	handler := UnifiedAuthMiddleware(cfg)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = GetUserIdentityFromContext(r.Context())
		w.WriteHeader(http.StatusNoContent)
	}))
	req := httptest.NewRequest(http.MethodGet, "/api/v1/agents", nil)
	req.RemoteAddr = remoteAddr
	if withHeaders {
		req.Header.Set("X-Forwarded-User-Id", "proxy-user-1")
		req.Header.Set("X-Forwarded-User-Email", "proxy-user@example.com")
		req.Header.Set("X-Forwarded-User-Name", "Proxy User")
		req.Header.Set("X-Forwarded-User-Role", "admin")
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return seen, rec.Code
}

// TestUnifiedAuth_ForwardedIdentityHeaders: the X-Forwarded-User-* headers
// become the request identity only when the header provider is configured
// (HeaderProxyAuth), the peer is in TrustedProxies and the headers are
// present. Setting TrustedProxies alone, which is how client addresses are
// resolved behind a proxy, never makes them an identity.
func TestUnifiedAuth_ForwardedIdentityHeaders(t *testing.T) {
	const trustedPeer = "10.0.0.2:4567"
	const otherPeer = "192.0.2.10:4567"
	trusted := []string{"10.0.0.0/8"}

	tests := []struct {
		name        string
		cfg         AuthConfig
		remoteAddr  string
		withHeaders bool
		wantUser    bool
	}{
		{name: "trusted proxies set, header provider off", cfg: AuthConfig{TrustedProxies: trusted}, remoteAddr: trustedPeer, withHeaders: true},
		{name: "header provider on, empty trusted list", cfg: AuthConfig{HeaderProxyAuth: true}, remoteAddr: trustedPeer, withHeaders: true},
		{name: "header provider on, peer not trusted", cfg: AuthConfig{TrustedProxies: trusted, HeaderProxyAuth: true}, remoteAddr: otherPeer, withHeaders: true},
		{name: "header provider on, no headers", cfg: AuthConfig{TrustedProxies: trusted, HeaderProxyAuth: true}, remoteAddr: trustedPeer},
		{name: "header provider on, trusted peer with headers", cfg: AuthConfig{TrustedProxies: trusted, HeaderProxyAuth: true}, remoteAddr: trustedPeer, withHeaders: true, wantUser: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			user, code := headerProxyAuthResult(t, tt.cfg, tt.remoteAddr, tt.withHeaders)
			if !tt.wantUser {
				assert.Nil(t, user, "no identity may come from the forwarded headers")
				assert.NotEqual(t, http.StatusNoContent, code, "an unauthenticated API request must not reach the handler")
				return
			}
			if assert.NotNil(t, user) {
				assert.Equal(t, "proxy-user-1", user.ID())
				assert.Equal(t, "proxy-user@example.com", user.Email())
			}
			assert.Equal(t, http.StatusNoContent, code)
		})
	}
}

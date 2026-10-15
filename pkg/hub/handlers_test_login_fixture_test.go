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
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/GoogleCloudPlatform/scion/pkg/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func postTestLogin(t *testing.T, ws *WebServer, svc *UserTokenService, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/test-login", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", testLoginAuthHeader(t, svc))
	rec := httptest.NewRecorder()
	ws.handleTestLogin(rec, req)
	return rec
}

// The reserved test-identity domain is refused on the requested email
// before any lookup or write: creating such a user is refused.
func TestHandleTestLogin_RefusesFixtureDomainOnCreate(t *testing.T) {
	for _, email := range []string{
		"new@" + store.TestFixtureEmailDomain,
		"New@SCION-FIXTURE.INVALID",
		"sub@x." + store.TestFixtureEmailDomain,
	} {
		t.Run(email, func(t *testing.T) {
			ws, svc := newTestLoginWebServer(t, true)
			mockStore := ws.store.(*testLoginStore)

			rec := postTestLogin(t, ws, svc, `{"email":"`+email+`","role":"admin"}`)

			assertTestLoginJSONError(t, rec, http.StatusForbidden, ErrCodeForbidden, "test-login cannot sign in as this user")
			assert.Empty(t, mockStore.users, "no user may be created")
			assert.Empty(t, rec.Result().Cookies(), "no session cookie")
		})
	}
}

// ... and signing in as an existing reserved-domain row is refused too,
// with the row left untouched.
func TestHandleTestLogin_RefusesFixtureDomainOnExisting(t *testing.T) {
	ws, svc := newTestLoginWebServer(t, true)
	mockStore := ws.store.(*testLoginStore)
	email := "test-identity-abc@" + store.TestFixtureEmailDomain
	exp := time.Now().Add(time.Hour)
	orig := store.User{ID: "fixture-id", Email: email, DisplayName: "Fixture", Role: "member", Status: "active",
		Kind: store.UserKindTestFixture, ExpiresAt: &exp}
	row := orig
	mockStore.users[email] = &row

	rec := postTestLogin(t, ws, svc, `{"email":"`+email+`","role":"admin","displayName":"Changed"}`)

	assertTestLoginJSONError(t, rec, http.StatusForbidden, ErrCodeForbidden, "test-login cannot sign in as this user")
	assert.Equal(t, orig, *mockStore.users[email], "the row must be unchanged")
}

// testLoginRefusesUser refuses a test-fixture row by kind, independently of
// the domain check.
func TestHandleTestLogin_RefusesTestFixtureKind(t *testing.T) {
	ws, svc := newTestLoginWebServer(t, true)
	mockStore := ws.store.(*testLoginStore)
	exp := time.Now().Add(time.Hour)
	orig := store.User{ID: "fixture-id", Email: "odd@example.com", DisplayName: "Fixture", Role: "viewer", Status: "active",
		Kind: store.UserKindTestFixture, ExpiresAt: &exp}
	row := orig
	mockStore.users[orig.Email] = &row

	rec := postTestLogin(t, ws, svc, `{"email":"odd@example.com","role":"admin"}`)

	assertTestLoginJSONError(t, rec, http.StatusForbidden, ErrCodeForbidden, "test-login cannot sign in as this user")
	assert.Equal(t, orig, *mockStore.users[orig.Email])
	assert.False(t, testLoginRefusesUser(&store.User{Kind: store.UserKindHuman}))
	assert.False(t, testLoginRefusesUser(&store.User{}))
}

// The domain test-login callers use for their own synthetic users stays
// usable: the refusal matches the reserved domain exactly, not any
// ".invalid" address.
func TestHandleTestLogin_ScionTestInvalidStillWorks(t *testing.T) {
	ws, svc := newTestLoginWebServer(t, true)
	mockStore := ws.store.(*testLoginStore)

	// Create.
	rec := postTestLogin(t, ws, svc, `{"email":"test-fixture-0123456789abcdef@scion-test.invalid","role":"member"}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var resp TestLoginResponse
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
	assert.Equal(t, "member", resp.User.Role)
	assert.Contains(t, mockStore.users, "test-fixture-0123456789abcdef@scion-test.invalid")

	// Existing.
	rec = postTestLogin(t, ws, svc, `{"email":"test-fixture-0123456789abcdef@scion-test.invalid","role":"member"}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	// Another .invalid domain also works.
	rec = postTestLogin(t, ws, svc, `{"email":"someone@example.invalid","role":"member"}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
}

// testLoginBindCases is the Phase 4 bind table (ptone/scion#4240): test-login
// is served only when the listen address is provably loopback. Names are
// never resolved, so every hostname other than the exact "localhost" is
// refused, including ones that resolve to loopback.
var testLoginBindCases = []struct {
	host  string
	start bool
}{
	// MUST START.
	{"127.0.0.1", true},
	{"::1", true},
	{"localhost", true},
	{"127.0.0.53", true}, // all of 127.0.0.0/8 is loopback
	// MUST REFUSE.
	{"", false}, // unset: every interface
	{"0.0.0.0", false},
	{"::", false},
	{"192.168.1.10", false},
	{"10.0.0.5", false},
	{"2001:db8::1", false},
	{"example.com", false},
	{"localhost.localdomain", false},
	{"localhost.", false},
	{"LOCALHOST", false},
	{"[::1]", false},
	{"127.0.0.1:8080", false},
	{" 127.0.0.1", false},
}

func TestCheckTestLoginBind(t *testing.T) {
	for _, tc := range testLoginBindCases {
		t.Run("host="+tc.host, func(t *testing.T) {
			err := CheckTestLoginBind(true, tc.host)
			if tc.start {
				assert.NoError(t, err)
			} else {
				assert.ErrorIs(t, err, ErrTestLoginNonLoopback)
			}
			// Disabled test-login never refuses, whatever the host.
			assert.NoError(t, CheckTestLoginBind(false, tc.host))
		})
	}
}

// The NewWebServer defense-in-depth check: on a host that is not loopback,
// test-login stays disabled and the handler answers 403 for any email,
// including the scion-test.invalid domain test-login callers use (DEC-1(c):
// no non-issuance login path on a hub that is not loopback-only). On a
// loopback host it works.
func TestNewWebServer_TestLoginBind(t *testing.T) {
	for _, tc := range testLoginBindCases {
		t.Run("host="+tc.host, func(t *testing.T) {
			ws := NewWebServer(WebServerConfig{EnableTestLogin: true, Host: tc.host})
			assert.Equal(t, tc.start, ws.config.EnableTestLogin)
			svc, err := NewUserTokenService(UserTokenConfig{})
			require.NoError(t, err)
			ws.SetUserTokenService(svc)
			mockStore := newTestLoginStore()
			ws.SetStore(mockStore)

			rec := postTestLogin(t, ws, svc, `{"email":"test-fixture-0123456789abcdef@scion-test.invalid","role":"member"}`)
			if tc.start {
				require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
				return
			}
			assertTestLoginJSONError(t, rec, http.StatusForbidden, ErrCodeForbidden, "test-login is not enabled")
			assert.Empty(t, mockStore.users, "no user may be created")
			assert.Empty(t, rec.Result().Cookies(), "no session cookie")
		})
	}
}

// The handler repeats the bind check, so a WebServer whose host changes
// after construction does not serve test-login either.
func TestHandleTestLogin_RefusesNonLoopbackHost(t *testing.T) {
	ws, svc := newTestLoginWebServer(t, true)
	require.True(t, ws.config.EnableTestLogin)
	ws.config.Host = "0.0.0.0"
	mockStore := ws.store.(*testLoginStore)

	rec := postTestLogin(t, ws, svc, `{"email":"someone@example.invalid","role":"admin"}`)

	assertTestLoginJSONError(t, rec, http.StatusForbidden, ErrCodeForbidden, "test-login is not enabled")
	assert.Empty(t, mockStore.users)
}

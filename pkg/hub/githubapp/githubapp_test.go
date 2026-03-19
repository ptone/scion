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

package githubapp

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	jose "github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
)

// generateTestKey generates a test RSA private key in PEM format.
func generateTestKey(t *testing.T) ([]byte, *rsa.PrivateKey) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("failed to generate test key: %v", err)
	}

	pemData := pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(key),
	})

	return pemData, key
}

func TestParsePrivateKey_PKCS1(t *testing.T) {
	pemData, original := generateTestKey(t)

	parsed, err := ParsePrivateKey(pemData)
	if err != nil {
		t.Fatalf("ParsePrivateKey failed: %v", err)
	}

	if parsed.N.Cmp(original.N) != 0 {
		t.Error("parsed key doesn't match original")
	}
}

func TestParsePrivateKey_PKCS8(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("failed to generate key: %v", err)
	}

	pkcs8Bytes, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatalf("failed to marshal PKCS8: %v", err)
	}

	pemData := pem.EncodeToMemory(&pem.Block{
		Type:  "PRIVATE KEY",
		Bytes: pkcs8Bytes,
	})

	parsed, err := ParsePrivateKey(pemData)
	if err != nil {
		t.Fatalf("ParsePrivateKey failed: %v", err)
	}

	if parsed.N.Cmp(key.N) != 0 {
		t.Error("parsed key doesn't match original")
	}
}

func TestParsePrivateKey_Invalid(t *testing.T) {
	tests := []struct {
		name string
		data []byte
	}{
		{"empty", []byte{}},
		{"garbage", []byte("not a pem block")},
		{"bad pem content", pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: []byte("invalid")})},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParsePrivateKey(tc.data)
			if err == nil {
				t.Error("expected error, got nil")
			}
		})
	}
}

func TestNewClient(t *testing.T) {
	pemData, _ := generateTestKey(t)

	t.Run("valid config", func(t *testing.T) {
		client, err := NewClient(Config{AppID: 12345}, pemData)
		if err != nil {
			t.Fatalf("NewClient failed: %v", err)
		}
		if client.appID != 12345 {
			t.Errorf("expected appID 12345, got %d", client.appID)
		}
		if client.apiBaseURL != "https://api.github.com" {
			t.Errorf("expected default API URL, got %s", client.apiBaseURL)
		}
	})

	t.Run("custom API base URL", func(t *testing.T) {
		client, err := NewClient(Config{
			AppID:      12345,
			APIBaseURL: "https://github.example.com/api/v3/",
		}, pemData)
		if err != nil {
			t.Fatalf("NewClient failed: %v", err)
		}
		if client.apiBaseURL != "https://github.example.com/api/v3" {
			t.Errorf("expected trimmed URL, got %s", client.apiBaseURL)
		}
	})

	t.Run("missing app ID", func(t *testing.T) {
		_, err := NewClient(Config{}, pemData)
		if err == nil {
			t.Error("expected error for missing app ID")
		}
	})

	t.Run("invalid key", func(t *testing.T) {
		_, err := NewClient(Config{AppID: 1}, []byte("bad key"))
		if err == nil {
			t.Error("expected error for bad key")
		}
		mintErr, ok := err.(*TokenMintError)
		if !ok {
			t.Fatalf("expected TokenMintError, got %T", err)
		}
		if mintErr.ErrorCode != ErrCodePrivateKeyInvalid {
			t.Errorf("expected error code %s, got %s", ErrCodePrivateKeyInvalid, mintErr.ErrorCode)
		}
	})
}

func TestGenerateJWT(t *testing.T) {
	pemData, key := generateTestKey(t)

	client, err := NewClient(Config{AppID: 42}, pemData)
	if err != nil {
		t.Fatalf("NewClient failed: %v", err)
	}

	token, err := client.GenerateJWT()
	if err != nil {
		t.Fatalf("GenerateJWT failed: %v", err)
	}

	if token == "" {
		t.Fatal("expected non-empty token")
	}

	// Parse and verify the JWT
	parsed, err := jwt.ParseSigned(token, []jose.SignatureAlgorithm{jose.RS256})
	if err != nil {
		t.Fatalf("failed to parse JWT: %v", err)
	}

	var claims jwt.Claims
	if err := parsed.Claims(&key.PublicKey, &claims); err != nil {
		t.Fatalf("failed to verify JWT claims: %v", err)
	}

	if claims.Issuer != "42" {
		t.Errorf("expected issuer '42', got '%s'", claims.Issuer)
	}

	// Check expiry is ~10 minutes from now
	now := time.Now()
	expiry := claims.Expiry.Time()
	if expiry.Before(now.Add(9*time.Minute)) || expiry.After(now.Add(11*time.Minute)) {
		t.Errorf("expected expiry ~10min from now, got %v", expiry)
	}
}

func TestMintInstallationToken(t *testing.T) {
	pemData, _ := generateTestKey(t)

	t.Run("successful mint", func(t *testing.T) {
		expiresAt := time.Now().Add(1 * time.Hour).UTC().Format(time.RFC3339)
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost {
				t.Errorf("expected POST, got %s", r.Method)
			}
			if r.URL.Path != "/app/installations/12345/access_tokens" {
				t.Errorf("unexpected path: %s", r.URL.Path)
			}

			// Verify Authorization header has Bearer token
			auth := r.Header.Get("Authorization")
			if auth == "" || len(auth) < 8 {
				t.Error("missing or short Authorization header")
			}

			// Verify request body
			var reqBody accessTokenRequest
			if err := json.NewDecoder(r.Body).Decode(&reqBody); err != nil {
				t.Fatalf("failed to decode request body: %v", err)
			}
			if len(reqBody.Repositories) != 1 || reqBody.Repositories[0] != "my-repo" {
				t.Errorf("expected repos [my-repo], got %v", reqBody.Repositories)
			}
			if reqBody.Permissions["contents"] != "write" {
				t.Errorf("expected contents:write, got %v", reqBody.Permissions)
			}

			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(accessTokenResponse{
				Token:     "ghs_test_token_123",
				ExpiresAt: expiresAt,
				Permissions: map[string]string{
					"contents":      "write",
					"pull_requests": "write",
					"metadata":      "read",
				},
			})
		}))
		defer server.Close()

		client, err := NewClient(Config{
			AppID:      42,
			APIBaseURL: server.URL,
		}, pemData)
		if err != nil {
			t.Fatalf("NewClient failed: %v", err)
		}

		token, err := client.MintInstallationToken(
			context.Background(),
			12345,
			[]string{"my-repo"},
			DefaultTokenPermissions(),
		)
		if err != nil {
			t.Fatalf("MintInstallationToken failed: %v", err)
		}

		if token.Token != "ghs_test_token_123" {
			t.Errorf("expected token ghs_test_token_123, got %s", token.Token)
		}
		if token.Permissions["contents"] != "write" {
			t.Errorf("expected contents:write permission")
		}
	})

	t.Run("installation not found (404)", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNotFound)
			json.NewEncoder(w).Encode(githubErrorResponse{Message: "Not Found"})
		}))
		defer server.Close()

		client, _ := NewClient(Config{AppID: 42, APIBaseURL: server.URL}, pemData)
		_, err := client.MintInstallationToken(context.Background(), 99999, nil, DefaultTokenPermissions())
		if err == nil {
			t.Fatal("expected error")
		}

		mintErr, ok := err.(*TokenMintError)
		if !ok {
			t.Fatalf("expected TokenMintError, got %T", err)
		}
		if mintErr.ErrorCode != ErrCodeInstallationRevoked {
			t.Errorf("expected %s, got %s", ErrCodeInstallationRevoked, mintErr.ErrorCode)
		}
	})

	t.Run("installation suspended (403)", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusForbidden)
			json.NewEncoder(w).Encode(githubErrorResponse{Message: "Installation has been suspended"})
		}))
		defer server.Close()

		client, _ := NewClient(Config{AppID: 42, APIBaseURL: server.URL}, pemData)
		_, err := client.MintInstallationToken(context.Background(), 12345, nil, DefaultTokenPermissions())
		if err == nil {
			t.Fatal("expected error")
		}

		mintErr := err.(*TokenMintError)
		if mintErr.ErrorCode != ErrCodeInstallationSuspended {
			t.Errorf("expected %s, got %s", ErrCodeInstallationSuspended, mintErr.ErrorCode)
		}
	})

	t.Run("repo not accessible (422)", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusUnprocessableEntity)
			json.NewEncoder(w).Encode(githubErrorResponse{Message: "Repository not found"})
		}))
		defer server.Close()

		client, _ := NewClient(Config{AppID: 42, APIBaseURL: server.URL}, pemData)
		_, err := client.MintInstallationToken(context.Background(), 12345, []string{"nonexistent"}, DefaultTokenPermissions())
		if err == nil {
			t.Fatal("expected error")
		}

		mintErr := err.(*TokenMintError)
		if mintErr.ErrorCode != ErrCodeRepoNotAccessible {
			t.Errorf("expected %s, got %s", ErrCodeRepoNotAccessible, mintErr.ErrorCode)
		}
	})

	t.Run("unauthorized (401)", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
			json.NewEncoder(w).Encode(githubErrorResponse{Message: "Bad credentials"})
		}))
		defer server.Close()

		client, _ := NewClient(Config{AppID: 42, APIBaseURL: server.URL}, pemData)
		_, err := client.MintInstallationToken(context.Background(), 12345, nil, DefaultTokenPermissions())
		if err == nil {
			t.Fatal("expected error")
		}

		mintErr := err.(*TokenMintError)
		if mintErr.ErrorCode != ErrCodePrivateKeyInvalid {
			t.Errorf("expected %s, got %s", ErrCodePrivateKeyInvalid, mintErr.ErrorCode)
		}
	})
}

func TestConfig_IsConfigured(t *testing.T) {
	tests := []struct {
		name     string
		config   Config
		expected bool
	}{
		{"empty", Config{}, false},
		{"app_id only", Config{AppID: 1}, false},
		{"app_id + key path", Config{AppID: 1, PrivateKeyPath: "/path"}, true},
		{"app_id + inline key", Config{AppID: 1, PrivateKey: "key"}, true},
		{"key path only", Config{PrivateKeyPath: "/path"}, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.config.IsConfigured(); got != tc.expected {
				t.Errorf("expected %v, got %v", tc.expected, got)
			}
		})
	}
}

func TestClassifyGitHubError(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		body       string
		expected   string
	}{
		{"404", 404, `{"message":"Not Found"}`, ErrCodeInstallationRevoked},
		{"403 suspended", 403, `{"message":"Installation has been suspended"}`, ErrCodeInstallationSuspended},
		{"403 other", 403, `{"message":"denied"}`, ErrCodePermissionDenied},
		{"401 app not found", 401, `{"message":"App not found"}`, ErrCodeAppNotFound},
		{"401 bad creds", 401, `{"message":"Bad credentials"}`, ErrCodePrivateKeyInvalid},
		{"422 repo", 422, `{"message":"Repository not found"}`, ErrCodeRepoNotAccessible},
		{"422 other", 422, `{"message":"Invalid"}`, ErrCodePermissionDenied},
		{"500", 500, `{"message":"Server Error"}`, ErrCodeTokenMintFailed},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := classifyGitHubError(tc.statusCode, []byte(tc.body))
			if err.ErrorCode != tc.expected {
				t.Errorf("expected %s, got %s", tc.expected, err.ErrorCode)
			}
		})
	}
}

// Ensure jose import is available for JWT parsing in tests
var _ = fmt.Sprintf

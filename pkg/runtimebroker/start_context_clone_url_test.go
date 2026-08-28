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

package runtimebroker

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestStripGitCloneCredentials(t *testing.T) {
	tests := []struct {
		name         string
		rawURL       string
		wantCleanURL string
		wantPassword string
	}{
		{
			name:         "no credentials",
			rawURL:       "https://github.com/org/repo",
			wantCleanURL: "https://github.com/org/repo",
			wantPassword: "",
		},
		{
			name:         "user and password",
			rawURL:       "https://user:FAKE-AUTH-SENTINEL-not-a-real-credential@github.com/org/repo",
			wantCleanURL: "https://github.com/org/repo",
			wantPassword: "FAKE-AUTH-SENTINEL-not-a-real-credential",
		},
		{
			name:         "oauth2 token pattern",
			rawURL:       "https://oauth2:FAKE-KEY-SENTINEL-not-a-real-credential@github.com/org/repo.git",
			wantCleanURL: "https://github.com/org/repo.git",
			wantPassword: "FAKE-KEY-SENTINEL-not-a-real-credential",
		},
		{
			name:         "x-access-token pattern",
			rawURL:       "https://x-access-token:FAKE-AUTH-SENTINEL-not-a-real-credential@github.com/org/repo",
			wantCleanURL: "https://github.com/org/repo",
			wantPassword: "FAKE-AUTH-SENTINEL-not-a-real-credential",
		},
		{
			name:         "token-only URL (no colon)",
			rawURL:       "https://FAKE-KEY-SENTINEL-not-a-real-credential@github.com/org/repo",
			wantCleanURL: "https://github.com/org/repo",
			wantPassword: "FAKE-KEY-SENTINEL-not-a-real-credential",
		},
		{
			name:         "no scheme no credentials — returned as-is",
			rawURL:       "github.com/org/repo",
			wantCleanURL: "github.com/org/repo",
			wantPassword: "",
		},
		{
			name:         "no scheme with credentials",
			rawURL:       "user:FAKE-AUTH-SENTINEL-not-a-real-credential@github.com/org/repo",
			wantCleanURL: "https://github.com/org/repo",
			wantPassword: "FAKE-AUTH-SENTINEL-not-a-real-credential",
		},
		{
			name:         "empty string",
			rawURL:       "",
			wantCleanURL: "",
			wantPassword: "",
		},
		{
			name:         "http scheme preserved",
			rawURL:       "http://user:FAKE-AUTH-SENTINEL-not-a-real-credential@internal.host/repo",
			wantCleanURL: "http://internal.host/repo",
			wantPassword: "FAKE-AUTH-SENTINEL-not-a-real-credential",
		},
		{
			name:         "path and query preserved",
			rawURL:       "https://user:FAKE-AUTH-SENTINEL-not-a-real-credential@github.com/org/repo.git?ref=main",
			wantCleanURL: "https://github.com/org/repo.git?ref=main",
			wantPassword: "FAKE-AUTH-SENTINEL-not-a-real-credential",
		},
		{
			name:         "user with empty password",
			rawURL:       "https://user:@github.com/org/repo",
			wantCleanURL: "https://github.com/org/repo",
			wantPassword: "user",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotClean, gotPw := stripGitCloneCredentials(tt.rawURL)
			assert.Equal(t, tt.wantCleanURL, gotClean, "clean URL")
			assert.Equal(t, tt.wantPassword, gotPw, "password")

			// The mutation that must go red: the clean URL must never
			// contain the extracted password. This is the security
			// assertion — if stripping is reverted (return rawURL, ""),
			// any test case with credentials fails here.
			if tt.wantPassword != "" {
				assert.NotContains(t, gotClean, tt.wantPassword,
					"the clean URL must not contain the credential value")
			}
		})
	}
}

// TestStripGitCloneCredentials_NoCredentialInCleanURL is the standalone
// security assertion: for every credential-bearing URL, the credential
// value must not appear anywhere in the returned clean URL. This test
// goes red if stripGitCloneCredentials is reverted to a no-op.
func TestStripGitCloneCredentials_NoCredentialInCleanURL(t *testing.T) {
	credentialURLs := []string{
		"https://user:FAKE-KEY-SENTINEL-not-a-real-credential@github.com/org/repo",
		"https://oauth2:FAKE-KEY-SENTINEL-not-a-real-credential@github.com/org/repo",
		"https://FAKE-KEY-SENTINEL-not-a-real-credential@github.com/org/repo",
		"user:FAKE-KEY-SENTINEL-not-a-real-credential@github.com/org/repo",
	}

	for _, rawURL := range credentialURLs {
		cleanURL, password := stripGitCloneCredentials(rawURL)
		assert.NotEmpty(t, password, "expected a credential to be extracted from %q", rawURL)
		assert.NotContains(t, cleanURL, "FAKE-KEY-SENTINEL-not-a-real-credential",
			"credential leaked into clean URL for input %q", rawURL)
	}
}

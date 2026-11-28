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

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/GoogleCloudPlatform/scion/pkg/githubfetch"
	"github.com/GoogleCloudPlatform/scion/pkg/secret"
	"github.com/GoogleCloudPlatform/scion/pkg/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGitHubCredential_ScopeHMACAndPrecedence(t *testing.T) {
	srv, s, project, _ := setupWorkspaceProject(t, "gh-cred-scope")
	ctx := context.Background()

	const tokA = "ghp_secret_token_alpha"
	const tokB = "ghp_secret_token_beta"

	// Ensure the server initialized its dedicated GitHub scope HMAC purpose key.
	require.True(t, srv.githubScopeCapabilityKeys().configured(), "dedicated github scope HMAC key must be configured")

	t.Run("project precedence GH_REPO > GH_OWNER > GITHUB_TOKEN > anon", func(t *testing.T) {
		useReimportCredBackend(srv, secret.ScopeProject, project.ID, map[string]string{
			"GH_ACME__REPO": tokA,
			"GH_ACME":       tokB,
			"GITHUB_TOKEN":  "ghp_default",
		})
		cred := srv.githubCredential(ctx, ghCredRequest{
			Scope:   store.TemplateScopeProject,
			ScopeID: project.ID,
			Owner:   "acme",
			Repo:    "repo",
		})
		assert.Equal(t, tokA, cred.Token)
		assert.Equal(t, githubfetch.CredSecret, cred.Kind)
		assert.True(t, strings.HasPrefix(cred.Scope, "secret:"))
		assert.Len(t, strings.TrimPrefix(cred.Scope, "secret:"), 32)
		assert.NotContains(t, cred.Scope, tokA)
		assert.NotEqual(t, "secret:"+githubfetch.TokenFingerprint(tokA)[:32], cred.Scope,
			"with a purpose key present, scope must be keyed HMAC rather than unkeyed SHA-256")

		// Same token produces identical scope; different token produces different scope.
		cred2 := srv.githubCredential(ctx, ghCredRequest{
			Scope:   store.TemplateScopeProject,
			ScopeID: project.ID,
			Owner:   "acme",
			Repo:    "repo",
		})
		assert.Equal(t, cred.Scope, cred2.Scope)

		credOtherRepo := srv.githubCredential(ctx, ghCredRequest{
			Scope:   store.TemplateScopeProject,
			ScopeID: project.ID,
			Owner:   "acme",
			Repo:    "other",
		})
		assert.Equal(t, tokB, credOtherRepo.Token)
		assert.NotEqual(t, cred.Scope, credOtherRepo.Scope)
	})

	t.Run("app mint failure falls through to GITHUB_TOKEN with Hint", func(t *testing.T) {
		installID := int64(12345)
		project.GitHubInstallationID = &installID
		require.NoError(t, s.UpdateProject(ctx, project))

		// Configure AppID without a valid private key so minting fails and falls through.
		srv.config.GitHubAppConfig.AppID = 999
		t.Cleanup(func() { srv.config.GitHubAppConfig.AppID = 0 })
		useReimportCredBackend(srv, secret.ScopeProject, project.ID, map[string]string{
			"GITHUB_TOKEN": tokB,
		})
		cred := srv.githubCredential(ctx, ghCredRequest{
			Scope:   store.TemplateScopeProject,
			ScopeID: project.ID,
			Owner:   "acme",
			Repo:    "repo",
		})
		assert.Equal(t, tokB, cred.Token)
		assert.Equal(t, githubfetch.CredSecret, cred.Kind)
		assert.Contains(t, cred.Hint, "GitHub App token could not be minted")
		assert.Contains(t, cred.Hint, "GITHUB_TOKEN")
		assert.NotContains(t, cred.Hint, tokB)
	})

	t.Run("anon when no credential is configured", func(t *testing.T) {
		project.GitHubInstallationID = nil
		require.NoError(t, s.UpdateProject(ctx, project))
		useReimportCredBackend(srv, secret.ScopeProject, project.ID, nil)

		cred := srv.githubCredential(ctx, ghCredRequest{
			Scope:   store.TemplateScopeProject,
			ScopeID: project.ID,
			Owner:   "acme",
			Repo:    "repo",
		})
		assert.True(t, cred.IsAnonymous())
		assert.Equal(t, githubfetch.AnonScope, cred.Scope)
	})
}

type failingGitHubScopeSecretBackend struct {
	secret.SecretBackend
}

func (failingGitHubScopeSecretBackend) Get(context.Context, string, string, string) (*secret.SecretWithValue, error) {
	return nil, errors.New("secret backend unavailable")
}

func (failingGitHubScopeSecretBackend) Set(context.Context, *secret.SetSecretInput) (bool, *secret.SecretMeta, error) {
	return false, nil, errors.New("secret backend unavailable")
}

type failingGitHubScopeStore struct {
	store.Store
}

func (failingGitHubScopeStore) GetSecretValue(context.Context, string, string, string) (string, error) {
	return "", errors.New("store unavailable")
}

func (failingGitHubScopeStore) SetSecret(context.Context, *store.Secret) (bool, error) {
	return false, errors.New("store unavailable")
}

func TestGitHubScopeKey_FailClosedNoUnkeyedSHA256(t *testing.T) {
	const tok = "ghp_fail_closed_test_token"
	unkeyed := "secret:" + githubfetch.TokenFingerprint(tok)[:32]

	t.Run("uninitialized server uses per-process ephemeral HMAC key", func(t *testing.T) {
		s1 := &Server{}
		s2 := &Server{}

		scope1 := s1.secretCredentialScope(tok)
		scope1Again := s1.secretCredentialScope(tok)
		scope2 := s2.secretCredentialScope(tok)

		assert.True(t, strings.HasPrefix(scope1, "secret:"))
		assert.Len(t, strings.TrimPrefix(scope1, "secret:"), 32)
		assert.NotContains(t, scope1, tok)
		assert.NotEqual(t, unkeyed, scope1, "must never fall back to unkeyed SHA-256")
		assert.Equal(t, scope1, scope1Again, "ephemeral key must be stable within one Server")
		assert.NotEqual(t, scope1, scope2, "distinct Servers must generate distinct ephemeral keys")
	})

	t.Run("legacy init failure uses ephemeral key on non-strict deployment", func(t *testing.T) {
		s := &Server{
			secretBackend: failingGitHubScopeSecretBackend{},
			store:         failingGitHubScopeStore{},
		}
		err := s.initGitHubScopeKey(context.Background())
		require.NoError(t, err)
		require.True(t, s.githubScopeCapabilityKeys().configured(), "ephemeral key must be installed")
		scope := s.secretCredentialScope(tok)
		assert.NotEqual(t, unkeyed, scope, "ephemeral key must produce keyed HMAC, not unkeyed SHA-256")
	})

	t.Run("legacy init failure fails startup when RequireStableSigningKey is true", func(t *testing.T) {
		s := &Server{
			config:        ServerConfig{RequireStableSigningKey: true},
			secretBackend: failingGitHubScopeSecretBackend{},
			store:         failingGitHubScopeStore{},
		}
		err := s.initGitHubScopeKey(context.Background())
		require.Error(t, err)
	})
}

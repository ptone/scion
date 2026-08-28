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

package entadapter

import (
	"context"
	"testing"
	"time"

	"github.com/GoogleCloudPlatform/scion/pkg/store"
	"github.com/GoogleCloudPlatform/scion/pkg/store/enttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newTestCredentialStore returns a fresh Ent-backed AgentCredentialStore.
func newTestCredentialStore(t *testing.T) *AgentCredentialStore {
	t.Helper()
	client := enttest.NewClient(t)
	return NewAgentCredentialStore(client)
}

// createTestCredential creates a credential with the given JTI hash and returns it.
func createTestCredential(t *testing.T, s *AgentCredentialStore, jtiHash string) *store.AgentCredential {
	t.Helper()
	cred := &store.AgentCredential{
		AgentID:      "agent-1",
		ProjectID:    "project-1",
		TokenJTIHash: jtiHash,
		IssuedAt:     time.Now(),
		ExpiresAt:    time.Now().Add(24 * time.Hour),
	}
	err := s.CreateAgentCredential(context.Background(), cred)
	require.NoError(t, err)
	return cred
}

func TestUpdateAgentCredentialEntitledKeys(t *testing.T) {
	s := newTestCredentialStore(t)
	ctx := context.Background()

	t.Run("set keys on existing credential", func(t *testing.T) {
		cred := createTestCredential(t, s, "jti-hash-set-keys")

		keys := []string{"API_KEY", "DB_PASSWORD", "GEMINI_API_KEY"}
		err := s.UpdateAgentCredentialEntitledKeys(ctx, cred.TokenJTIHash, cred.AgentID, keys)
		require.NoError(t, err)

		// Read back and verify
		got, err := s.GetAgentCredentialByJTIHash(ctx, cred.TokenJTIHash)
		require.NoError(t, err)
		assert.Equal(t, keys, got.EntitledSecretKeys)
	})

	t.Run("set empty keys (entitled to zero secrets)", func(t *testing.T) {
		cred := createTestCredential(t, s, "jti-hash-empty-keys")

		err := s.UpdateAgentCredentialEntitledKeys(ctx, cred.TokenJTIHash, cred.AgentID, []string{})
		require.NoError(t, err)

		got, err := s.GetAgentCredentialByJTIHash(ctx, cred.TokenJTIHash)
		require.NoError(t, err)
		// Empty slice: entitled to zero secrets (distinct from NULL/nil)
		assert.NotNil(t, got.EntitledSecretKeys)
		assert.Empty(t, got.EntitledSecretKeys)
	})

	t.Run("NULL before update (pre-migration state)", func(t *testing.T) {
		cred := createTestCredential(t, s, "jti-hash-null-check")

		// Before any update, EntitledSecretKeys is NULL (nil in Go)
		got, err := s.GetAgentCredentialByJTIHash(ctx, cred.TokenJTIHash)
		require.NoError(t, err)
		assert.Nil(t, got.EntitledSecretKeys, "newly created credential should have nil (NULL) entitled keys")
	})

	t.Run("not found returns ErrNotFound for unknown hash", func(t *testing.T) {
		err := s.UpdateAgentCredentialEntitledKeys(ctx, "nonexistent-jti-hash", "agent-1", []string{"KEY"})
		assert.ErrorIs(t, err, store.ErrNotFound)
	})

	t.Run("not found returns ErrNotFound for wrong agent (cross-agent guard)", func(t *testing.T) {
		cred := createTestCredential(t, s, "jti-hash-cross-agent")

		// Try to update with a different agent ID — must fail
		err := s.UpdateAgentCredentialEntitledKeys(ctx, cred.TokenJTIHash, "wrong-agent-id", []string{"KEY"})
		assert.ErrorIs(t, err, store.ErrNotFound, "update with wrong agent ID must return ErrNotFound")

		// Verify the credential was not modified
		got, err := s.GetAgentCredentialByJTIHash(ctx, cred.TokenJTIHash)
		require.NoError(t, err)
		assert.Nil(t, got.EntitledSecretKeys, "credential must remain NULL after failed cross-agent update")
	})

	t.Run("overwrite existing keys", func(t *testing.T) {
		cred := createTestCredential(t, s, "jti-hash-overwrite")

		// Set initial keys
		err := s.UpdateAgentCredentialEntitledKeys(ctx, cred.TokenJTIHash, cred.AgentID, []string{"KEY_A", "KEY_B"})
		require.NoError(t, err)

		// Overwrite with different set
		err = s.UpdateAgentCredentialEntitledKeys(ctx, cred.TokenJTIHash, cred.AgentID, []string{"KEY_C"})
		require.NoError(t, err)

		got, err := s.GetAgentCredentialByJTIHash(ctx, cred.TokenJTIHash)
		require.NoError(t, err)
		assert.Equal(t, []string{"KEY_C"}, got.EntitledSecretKeys)
	})
}

func TestEntitledSecretKeysRoundTrip(t *testing.T) {
	s := newTestCredentialStore(t)
	ctx := context.Background()

	// Create credential, set entitled keys, verify they survive read-back
	cred := createTestCredential(t, s, "jti-hash-roundtrip")

	keys := []string{"SECRET_1", "SECRET_2", "SECRET_3"}
	err := s.UpdateAgentCredentialEntitledKeys(ctx, cred.TokenJTIHash, cred.AgentID, keys)
	require.NoError(t, err)

	got, err := s.GetAgentCredentialByJTIHash(ctx, cred.TokenJTIHash)
	require.NoError(t, err)
	assert.Equal(t, keys, got.EntitledSecretKeys)

	// Verify other fields are unchanged
	assert.Equal(t, cred.AgentID, got.AgentID)
	assert.Equal(t, cred.ProjectID, got.ProjectID)
	assert.Equal(t, cred.TokenJTIHash, got.TokenJTIHash)
}

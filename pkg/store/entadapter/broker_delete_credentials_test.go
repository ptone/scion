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
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/GoogleCloudPlatform/scion/pkg/store"
	"github.com/GoogleCloudPlatform/scion/pkg/store/enttest"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// seedBrokerWithCredentials creates a broker row with an HMAC secret and an
// unconsumed join token.
func seedBrokerWithCredentials(t *testing.T, ps *ProjectStore, bs *BrokerSecretStore, name string) string {
	t.Helper()
	ctx := context.Background()
	id := uuid.NewString()
	require.NoError(t, ps.CreateRuntimeBroker(ctx, &store.RuntimeBroker{
		ID: id, Name: name, Slug: name, Status: store.BrokerStatusOnline,
	}))
	require.NoError(t, bs.CreateBrokerSecret(ctx, &store.BrokerSecret{
		BrokerID:  id,
		SecretKey: []byte("delete-credentials-secret-32-byt"),
		Algorithm: store.BrokerSecretAlgorithmHMACSHA256,
		Status:    store.BrokerSecretStatusActive,
	}))
	require.NoError(t, bs.CreateJoinToken(ctx, &store.BrokerJoinToken{
		BrokerID:  id,
		TokenHash: "hash-" + uuid.NewString(),
		ExpiresAt: time.Now().Add(time.Hour),
		CreatedBy: uuid.NewString(),
	}))
	return id
}

// DeleteRuntimeBroker deletes the broker's secret and join token with the
// row, so no credential is left for the ID once the delete returns.
func TestDeleteRuntimeBroker_DeletesSecretAndJoinToken(t *testing.T) {
	client := enttest.NewClient(t)
	ps, bs := NewProjectStore(client), NewBrokerSecretStore(client)
	ctx := context.Background()
	id := seedBrokerWithCredentials(t, ps, bs, "delete-creds")

	require.NoError(t, ps.DeleteRuntimeBroker(ctx, id))

	_, err := bs.GetBrokerSecret(ctx, id)
	assert.ErrorIs(t, err, store.ErrNotFound, "the broker secret is deleted with the row")
	active, err := bs.GetActiveSecrets(ctx, id)
	require.NoError(t, err)
	assert.Empty(t, active)
	_, err = bs.GetJoinTokenByBrokerID(ctx, id)
	assert.ErrorIs(t, err, store.ErrNotFound, "the join token is deleted with the row")
}

// The credential rows are keyed by the canonical ID: a delete by a
// non-canonical form of the ID still removes them.
func TestDeleteRuntimeBroker_NonCanonicalID_DeletesSecretAndJoinToken(t *testing.T) {
	client := enttest.NewClient(t)
	ps, bs := NewProjectStore(client), NewBrokerSecretStore(client)
	ctx := context.Background()
	id := seedBrokerWithCredentials(t, ps, bs, "delete-creds-upper")

	require.NoError(t, ps.DeleteRuntimeBroker(ctx, strings.ToUpper(id)))

	_, err := bs.GetBrokerSecret(ctx, id)
	assert.ErrorIs(t, err, store.ErrNotFound)
	_, err = bs.GetJoinTokenByBrokerID(ctx, id)
	assert.ErrorIs(t, err, store.ErrNotFound)
}

// Another broker's credentials are untouched.
func TestDeleteRuntimeBroker_KeepsOtherBrokersCredentials(t *testing.T) {
	client := enttest.NewClient(t)
	ps, bs := NewProjectStore(client), NewBrokerSecretStore(client)
	ctx := context.Background()
	gone := seedBrokerWithCredentials(t, ps, bs, "delete-creds-gone")
	kept := seedBrokerWithCredentials(t, ps, bs, "delete-creds-kept")

	require.NoError(t, ps.DeleteRuntimeBroker(ctx, gone))

	_, err := bs.GetBrokerSecret(ctx, kept)
	assert.NoError(t, err, "another broker's secret stays")
	_, err = bs.GetJoinTokenByBrokerID(ctx, kept)
	assert.NoError(t, err, "another broker's join token stays")
	_, err = ps.GetRuntimeBroker(ctx, kept)
	assert.NoError(t, err)
}

// A re-created row with the same ID starts with no credential: the old
// secret and join token went with the deleted row.
func TestDeleteRuntimeBroker_RecreatedSameIDInheritsNoCredential(t *testing.T) {
	client := enttest.NewClient(t)
	ps, bs := NewProjectStore(client), NewBrokerSecretStore(client)
	ctx := context.Background()
	id := seedBrokerWithCredentials(t, ps, bs, "delete-creds-recreate")

	require.NoError(t, ps.DeleteRuntimeBroker(ctx, id))
	require.NoError(t, ps.CreateRuntimeBroker(ctx, &store.RuntimeBroker{
		ID: id, Name: "delete-creds-recreate", Slug: "delete-creds-recreate", Status: store.BrokerStatusOnline,
	}))

	_, err := bs.GetBrokerSecret(ctx, id)
	assert.ErrorIs(t, err, store.ErrNotFound, "a re-created row has no secret from the deleted one")
	_, err = bs.GetJoinTokenByBrokerID(ctx, id)
	assert.ErrorIs(t, err, store.ErrNotFound, "a re-created row has no join token from the deleted one")
}

// Deleting an ID with no broker row fails with ErrNotFound and deletes
// nothing: the credential deletes run only in a transaction that deleted
// the row.
func TestDeleteRuntimeBroker_MissingRowDeletesNoCredential(t *testing.T) {
	client := enttest.NewClient(t)
	ps, bs := NewProjectStore(client), NewBrokerSecretStore(client)
	ctx := context.Background()
	id := uuid.NewString()
	require.NoError(t, bs.CreateBrokerSecret(ctx, &store.BrokerSecret{
		BrokerID:  id,
		SecretKey: []byte("delete-credentials-secret-32-byt"),
		Algorithm: store.BrokerSecretAlgorithmHMACSHA256,
		Status:    store.BrokerSecretStatusActive,
	}))

	assert.ErrorIs(t, ps.DeleteRuntimeBroker(ctx, id), store.ErrNotFound)
	_, err := bs.GetBrokerSecret(ctx, id)
	assert.NoError(t, err, "a failed delete leaves the secret")
}

// seedOrphanCredentials stores a secret and a join token for a broker ID
// that has no runtime broker row.
func seedOrphanCredentials(t *testing.T, bs *BrokerSecretStore) string {
	t.Helper()
	ctx := context.Background()
	id := uuid.NewString()
	require.NoError(t, bs.CreateBrokerSecret(ctx, &store.BrokerSecret{
		BrokerID:  id,
		SecretKey: []byte("orphan-credential-secret-32-byte"),
		Algorithm: store.BrokerSecretAlgorithmHMACSHA256,
		Status:    store.BrokerSecretStatusActive,
	}))
	require.NoError(t, bs.CreateJoinToken(ctx, &store.BrokerJoinToken{
		BrokerID:  id,
		TokenHash: "hash-" + uuid.NewString(),
		ExpiresAt: time.Now().Add(time.Hour),
		CreatedBy: uuid.NewString(),
	}))
	return id
}

// The orphan sweep removes a secret and a join token whose broker row does
// not exist, keeps a live broker's, and reports what it removed.
func TestDeleteOrphanBrokerCredentials_RemovesOrphansKeepsLive(t *testing.T) {
	client := enttest.NewClient(t)
	ps, bs := NewProjectStore(client), NewBrokerSecretStore(client)
	ctx := context.Background()
	live := seedBrokerWithCredentials(t, ps, bs, "orphan-sweep-live")
	orphan := seedOrphanCredentials(t, bs)
	orphanSecretOnly := uuid.NewString()
	require.NoError(t, bs.CreateBrokerSecret(ctx, &store.BrokerSecret{
		BrokerID: orphanSecretOnly, SecretKey: []byte("orphan-credential-secret-32-byte"),
		Algorithm: store.BrokerSecretAlgorithmHMACSHA256, Status: store.BrokerSecretStatusActive,
	}))

	secrets, tokens, err := bs.DeleteOrphanBrokerCredentials(ctx)
	require.NoError(t, err)
	assert.Equal(t, 2, secrets, "both orphan secrets are removed")
	assert.Equal(t, 1, tokens, "the orphan join token is removed")

	for _, id := range []string{orphan, orphanSecretOnly} {
		_, err = bs.GetBrokerSecret(ctx, id)
		assert.ErrorIs(t, err, store.ErrNotFound, "orphan secret removed")
	}
	_, err = bs.GetJoinTokenByBrokerID(ctx, orphan)
	assert.ErrorIs(t, err, store.ErrNotFound, "orphan join token removed")
	_, err = bs.GetBrokerSecret(ctx, live)
	assert.NoError(t, err, "a live broker's secret is kept")
	_, err = bs.GetJoinTokenByBrokerID(ctx, live)
	assert.NoError(t, err, "a live broker's join token is kept")
}

// A second run removes nothing.
func TestDeleteOrphanBrokerCredentials_SecondRunIsNoop(t *testing.T) {
	client := enttest.NewClient(t)
	ps, bs := NewProjectStore(client), NewBrokerSecretStore(client)
	ctx := context.Background()
	live := seedBrokerWithCredentials(t, ps, bs, "orphan-sweep-noop")
	seedOrphanCredentials(t, bs)

	_, _, err := bs.DeleteOrphanBrokerCredentials(ctx)
	require.NoError(t, err)
	secrets, tokens, err := bs.DeleteOrphanBrokerCredentials(ctx)
	require.NoError(t, err)
	assert.Equal(t, 0, secrets)
	assert.Equal(t, 0, tokens)
	_, err = bs.GetBrokerSecret(ctx, live)
	assert.NoError(t, err)
}

// Inside store.WithTx the sweep runs on the caller's transaction: rolling
// it back keeps the orphans.
func TestDeleteOrphanBrokerCredentials_InsideWithTxUsesCallerTransaction(t *testing.T) {
	composite := NewCompositeStore(enttest.NewClient(t))
	ctx := context.Background()
	orphan := seedOrphanCredentials(t, composite.BrokerSecretStore)

	rollback := errors.New("roll back")
	err := composite.WithTx(ctx, func(tx store.Store) error {
		secrets, tokens, err := tx.DeleteOrphanBrokerCredentials(ctx)
		require.NoError(t, err)
		assert.Equal(t, 1, secrets)
		assert.Equal(t, 1, tokens)
		return rollback
	})
	require.ErrorIs(t, err, rollback)
	_, err = composite.GetBrokerSecret(ctx, orphan)
	assert.NoError(t, err, "the rolled-back sweep removed nothing")

	secrets, tokens, err := composite.DeleteOrphanBrokerCredentials(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, secrets)
	assert.Equal(t, 1, tokens)
}

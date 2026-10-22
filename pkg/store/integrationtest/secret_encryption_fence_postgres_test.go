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

//go:build integration

package integrationtest

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/GoogleCloudPlatform/scion/pkg/ent/entc"
	enthubencryptionpolicy "github.com/GoogleCloudPlatform/scion/pkg/ent/hubencryptionpolicy"
	"github.com/GoogleCloudPlatform/scion/pkg/secret/envelope"
	"github.com/GoogleCloudPlatform/scion/pkg/store"
	"github.com/GoogleCloudPlatform/scion/pkg/store/entadapter"
	"github.com/GoogleCloudPlatform/scion/pkg/store/enttest"
)

// blockWindow is how long a write must stay blocked behind a lock before
// the test releases it.
const blockWindow = 300 * time.Millisecond

func fencePolicyPG(gen int64, writer string) store.EncryptionPolicy {
	return store.EncryptionPolicy{
		Generation: gen, MinReaderVersion: store.EncryptionPolicyMinReaderVersion,
		AllowedWriteKIDs: []string{writer}, RequiredReadKIDs: []string{writer},
		State: store.EncryptionPolicyPrepared,
	}
}

func fakeV2(kid string) string {
	return envelope.FormatV2Value(kid, bytes.Repeat([]byte{0x5A}, envelope.NonceSize+envelope.TagSize+4))
}

// TestSecretEncryptionFence_Postgres pins the PostgreSQL locking of the
// encryption policy fence (ptone/scion#1530 §6): fenced payload writes
// take a shared lock on the policy row and transitions an exclusive one,
// so a write never commits against a policy that a concurrent transition
// is replacing, and a transition waits for in-flight writes.
//
// It requires SCION_TEST_POSTGRES_URL (CI: launch-store-postgres-others):
//
//	go test -tags integration ./pkg/store/integrationtest/... -run TestSecretEncryptionFence_Postgres
func TestSecretEncryptionFence_Postgres(t *testing.T) {
	ctx := context.Background()
	dsn := enttest.NewSchemaURL(t) // skips when Postgres is not configured

	client, err := entc.OpenPostgres(dsn, entc.PoolConfig{MaxOpenConns: 4})
	require.NoError(t, err)
	defer func() { _ = client.Close() }()
	cs := entadapter.NewCompositeStore(client)

	applied, err := cs.UpdateEncryptionPolicy(ctx, 0, fencePolicyPG(1, "k2"))
	require.NoError(t, err)
	require.True(t, applied)

	t.Run("TransitionBlocksWriteWhichSeesTheNewPolicy", func(t *testing.T) {
		tx, err := client.Tx(ctx)
		require.NoError(t, err)
		defer func() { _ = tx.Rollback() }()
		// The transition's exclusive lock on the policy row.
		_, err = tx.HubEncryptionPolicy.Query().Where(enthubencryptionpolicy.IDEQ(1)).ForUpdate().Only(ctx)
		require.NoError(t, err)

		done := make(chan error, 1)
		go func() {
			done <- cs.CreateSecret(ctx, &store.Secret{ID: uuid.NewString(), Key: "PG_FENCE_BLOCKED",
				EncryptedValue: fakeV2("k2"), Scope: store.ScopeHub, ScopeID: "pg-fence"})
		}()
		select {
		case err := <-done:
			t.Fatalf("fenced write did not wait for the policy transition: %v", err)
		case <-time.After(blockWindow):
		}
		// Move the fence to k3 and commit: the blocked k2 write must now be
		// refused, not committed under the old policy.
		_, err = tx.HubEncryptionPolicy.UpdateOneID(1).SetGeneration(2).SetAllowedWriteKids([]string{"k3"}).
			SetRequiredReadKids([]string{"k3"}).SetUpdatedAt(time.Now().UTC()).Save(ctx)
		require.NoError(t, err)
		require.NoError(t, tx.Commit())
		select {
		case err := <-done:
			assert.ErrorIs(t, err, store.ErrEncryptionPolicyMismatch)
		case <-time.After(30 * time.Second):
			t.Fatal("fenced write never completed")
		}
		_, err = cs.GetSecret(ctx, "PG_FENCE_BLOCKED", store.ScopeHub, "pg-fence")
		assert.ErrorIs(t, err, store.ErrNotFound)
	})

	t.Run("OpenWriteBlocksTransition", func(t *testing.T) {
		release := make(chan struct{})
		written := make(chan struct{})
		writeDone := make(chan error, 1)
		go func() {
			writeDone <- cs.WithTx(ctx, func(tx store.Store) error {
				if err := tx.CreateSecret(ctx, &store.Secret{ID: uuid.NewString(), Key: "PG_FENCE_INFLIGHT",
					EncryptedValue: fakeV2("k3"), Scope: store.ScopeHub, ScopeID: "pg-fence"}); err != nil {
					return err
				}
				close(written)
				<-release
				return nil
			})
		}()
		select {
		case <-written:
		case err := <-writeDone:
			t.Fatalf("in-flight write failed: %v", err)
		case <-time.After(30 * time.Second):
			t.Fatal("in-flight write never ran")
		}

		transition := make(chan error, 1)
		go func() {
			ok, err := cs.UpdateEncryptionPolicy(ctx, 2, fencePolicyPG(3, "k4"))
			if err == nil && !ok {
				err = assert.AnError
			}
			transition <- err
		}()
		select {
		case err := <-transition:
			t.Fatalf("policy transition did not wait for the in-flight write: %v", err)
		case <-time.After(blockWindow):
		}
		close(release)
		require.NoError(t, <-writeDone)
		select {
		case err := <-transition:
			require.NoError(t, err)
		case <-time.After(30 * time.Second):
			t.Fatal("policy transition never completed")
		}
		got, err := cs.GetSecret(ctx, "PG_FENCE_INFLIGHT", store.ScopeHub, "pg-fence")
		require.NoError(t, err)
		assert.Equal(t, fakeV2("k3"), got.EncryptedValue)
		p, err := cs.GetEncryptionPolicy(ctx)
		require.NoError(t, err)
		assert.Equal(t, int64(3), p.Generation)
	})
}

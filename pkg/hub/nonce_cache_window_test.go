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
	"encoding/base64"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/GoogleCloudPlatform/scion/pkg/ent/noncecache"
	"github.com/GoogleCloudPlatform/scion/pkg/store"
	"github.com/GoogleCloudPlatform/scion/pkg/store/enttest"
)

// newWindowTestCache returns a NonceCache whose own sweep never fires
// during the test.
func newWindowTestCache(t *testing.T) *NonceCache {
	t.Helper()
	nc := NewNonceCache(24 * time.Hour)
	t.Cleanup(nc.Stop)
	return nc
}

// TestNonceCache_WindowBoundary: a key is held through its expiry, dropped
// by a sweep after it, and a key whose expiry is before the latest now seen
// is refused (it may already have been swept).
func TestNonceCache_WindowBoundary(t *testing.T) {
	nc := newWindowTestCache(t)
	base := time.Unix(1_800_000_000, 0)
	expiry := base.Add(5 * time.Minute)

	require.True(t, nc.Add("k", expiry, base), "first use")
	require.False(t, nc.Add("k", expiry, base), "exact repeat")

	// A first use exactly at the window edge is accepted.
	require.True(t, nc.Add("edge", expiry, expiry))

	// A sweep at the expiry keeps the entry: it is still refused.
	nc.sweep(expiry)
	require.False(t, nc.Add("k", expiry, expiry))

	// A sweep after the expiry drops it, and the key stays refused because
	// its expiry is before sweptTo.
	nc.sweep(expiry.Add(time.Second))
	nc.mu.Lock()
	_, held := nc.nonces["k"]
	nc.mu.Unlock()
	require.False(t, held, "the sweep drops the expired entry")
	require.False(t, nc.Add("k", expiry, base), "a stale copy is refused after the sweep")

	// A new key whose expiry is before the latest now seen is refused, even
	// with a stale now from its own timestamp check.
	require.False(t, nc.Add("late", expiry, base))

	// A later Add advances sweptTo too.
	nc2 := newWindowTestCache(t)
	require.True(t, nc2.Add("a", expiry.Add(time.Hour), expiry.Add(time.Second)))
	require.False(t, nc2.Add("b", expiry, base), "expiry before another caller's now")
}

// TestNonceCacheStore_RecordUntil: a key is recorded once, kept until the
// accept time plus retain, and refused once this replica's clock is past
// the accept time.
func TestNonceCacheStore_RecordUntil(t *testing.T) {
	ctx := context.Background()
	ns := NewNonceCacheStore(enttest.NewClient(t))

	ok, err := ns.RecordUntil(ctx, "broker-sig:past", time.Now().Add(-time.Second), 5*time.Minute)
	require.NoError(t, err)
	require.False(t, ok, "past its accept time")
	n, err := ns.client.NonceCache.Query().Count(ctx)
	require.NoError(t, err)
	require.Zero(t, n, "a refused late request writes no row")

	acceptUntil := time.Now().Add(time.Minute).Truncate(time.Second)
	ok, err = ns.RecordUntil(ctx, "broker-sig:k", acceptUntil, 5*time.Minute)
	require.NoError(t, err)
	require.True(t, ok)
	ok, err = ns.RecordUntil(ctx, "broker-sig:k", acceptUntil, 5*time.Minute)
	require.NoError(t, err)
	require.False(t, ok, "repeat")

	row, err := ns.client.NonceCache.Query().Where(noncecache.Nonce("broker-sig:k")).Only(ctx)
	require.NoError(t, err)
	require.True(t, row.ExpiresAt.Equal(acceptUntil.Add(5*time.Minute)), "expires_at %v", row.ExpiresAt)

	// A purge now keeps the row.
	_, err = ns.PurgeExpired(ctx)
	require.NoError(t, err)
	ok, err = ns.RecordUntil(ctx, "broker-sig:k", acceptUntil, 5*time.Minute)
	require.NoError(t, err)
	require.False(t, ok, "still recorded after a purge")
}

// TestBrokerSeenRecord_RecordWindow: a verified request is recorded under
// its verified HMAC, until its timestamp plus MaxClockSkew in memory and
// plus twice that in the DB row.
func TestBrokerSeenRecord_RecordWindow(t *testing.T) {
	skew := DefaultBrokerAuthConfig().MaxClockSkew
	ts := time.Now().Add(-skew + 30*time.Second).Truncate(time.Second)

	t.Run("memory", func(t *testing.T) {
		s := newSeenRecordStore(t)
		svc := newSeenRecordService(t, s, nil)
		brokerID, secret := seedSeenRecordBroker(t, s, store.BrokerSecretStatusActive)
		req := signSeenRecordRequest(svc, brokerID, secret, ts, "window-mem", nil, nil)
		sig := req.Header.Get(HeaderSignature)
		_, err := svc.ValidateBrokerSignature(context.Background(), req)
		require.NoError(t, err)

		svc.nonces.mu.Lock()
		defer svc.nonces.mu.Unlock()
		require.Len(t, svc.nonces.nonces, 1)
		for key, expiry := range svc.nonces.nonces {
			require.Equal(t, brokerReplayKey(mustDecodeSig(t, sig)), key)
			require.True(t, expiry.Equal(ts.Add(skew)), "expiry %v", expiry)
		}
	})

	t.Run("db", func(t *testing.T) {
		s := newSeenRecordStore(t)
		ns := NewNonceCacheStore(enttest.NewClient(t))
		svc := newSeenRecordService(t, s, ns)
		brokerID, secret := seedSeenRecordBroker(t, s, store.BrokerSecretStatusActive)
		req := signSeenRecordRequest(svc, brokerID, secret, ts, "window-db", nil, nil)
		sig := req.Header.Get(HeaderSignature)
		_, err := svc.ValidateBrokerSignature(context.Background(), req)
		require.NoError(t, err)

		rows, err := ns.client.NonceCache.Query().All(context.Background())
		require.NoError(t, err)
		require.Len(t, rows, 1)
		require.Equal(t, brokerReplayKey(mustDecodeSig(t, sig)), rows[0].Nonce)
		require.True(t, rows[0].ExpiresAt.Equal(ts.Add(2*skew)), "expires_at %v", rows[0].ExpiresAt)
	})
}

func mustDecodeSig(t *testing.T, sig string) []byte {
	t.Helper()
	b, err := base64.StdEncoding.DecodeString(sig)
	require.NoError(t, err)
	return b
}

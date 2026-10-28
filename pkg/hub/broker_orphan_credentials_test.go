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
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/GoogleCloudPlatform/scion/pkg/store"
)

const orphanSweepTestKey = "orphan-sweep-test-secret-32-byte"

// orphanSweepStore returns a fresh store holding a live broker with a secret
// and a join token, and an orphan secret and join token (no broker row).
func orphanSweepStore(t *testing.T) (s store.Store, live, orphan string) {
	t.Helper()
	ctx := context.Background()
	s, err := newTestStore(t, ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	live, orphan = uuid.NewString(), uuid.NewString()
	require.NoError(t, s.CreateRuntimeBroker(ctx, &store.RuntimeBroker{
		ID: live, Name: "orphan-sweep-live", Slug: "orphan-sweep-live", Status: store.BrokerStatusOnline,
		Created: time.Now(), Updated: time.Now(),
	}))
	for _, id := range []string{live, orphan} {
		require.NoError(t, s.CreateBrokerSecret(ctx, &store.BrokerSecret{
			BrokerID: id, SecretKey: []byte(orphanSweepTestKey),
			Algorithm: store.BrokerSecretAlgorithmHMACSHA256, Status: store.BrokerSecretStatusActive,
		}))
		require.NoError(t, s.CreateJoinToken(ctx, &store.BrokerJoinToken{
			BrokerID: id, TokenHash: "orphan-sweep-hash-" + id, ExpiresAt: time.Now().Add(time.Hour), CreatedBy: "dev",
		}))
	}
	return s, live, orphan
}

// orphanSweepLogs sends the default logger to a buffer for the test.
func orphanSweepLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(old) })
	return &buf
}

func assertCredentials(t *testing.T, s store.Store, id string, present bool, msg string) {
	t.Helper()
	ctx := context.Background()
	_, errSecret := s.GetBrokerSecret(ctx, id)
	_, errToken := s.GetJoinTokenByBrokerID(ctx, id)
	if present {
		assert.NoError(t, errSecret, msg+": secret")
		assert.NoError(t, errToken, msg+": join token")
	} else {
		assert.ErrorIs(t, errSecret, store.ErrNotFound, msg+": secret")
		assert.ErrorIs(t, errToken, store.ErrNotFound, msg+": join token")
	}
}

// Hub startup removes the credentials that have no broker row and keeps a
// live broker's.
func TestNew_SweepsOrphanBrokerCredentials(t *testing.T) {
	s, live, orphan := orphanSweepStore(t)
	cfg := DefaultServerConfig()
	cfg.DevAuthToken = testDevToken
	cfg.BrokerAuthConfig = DefaultBrokerAuthConfig()
	_, err := newTestHubServer(t, cfg, s)
	require.NoError(t, err)

	assertCredentials(t, s, orphan, false, "orphan removed at startup")
	assertCredentials(t, s, live, true, "live broker kept")
}

// The sweep logs only counts, never key material or IDs, and a second run
// removes nothing.
func TestSweepOrphanBrokerCredentials_LogsCountsAndIsIdempotent(t *testing.T) {
	s, live, orphan := orphanSweepStore(t)
	logs := orphanSweepLogs(t)

	secrets, tokens, err := sweepOrphanBrokerCredentials(context.Background(), s)
	require.NoError(t, err)
	assert.Equal(t, 1, secrets)
	assert.Equal(t, 1, tokens)
	out := logs.String()
	assert.Contains(t, out, "secrets_removed=1")
	assert.Contains(t, out, "join_tokens_removed=1")
	for _, leak := range []string{orphanSweepTestKey, base64.StdEncoding.EncodeToString([]byte(orphanSweepTestKey)), orphan, live} {
		assert.NotContains(t, out, leak, "the sweep log carries counts only")
	}

	logs.Reset()
	secrets, tokens, err = sweepOrphanBrokerCredentials(context.Background(), s)
	require.NoError(t, err)
	assert.Equal(t, 0, secrets)
	assert.Equal(t, 0, tokens)
	assert.NotContains(t, logs.String(), "Swept broker credentials", "a second run removes and logs nothing")
	assertCredentials(t, s, live, true, "live broker kept")
}

// orphanSweepFaultStore fails the orphan sweep.
type orphanSweepFaultStore struct {
	store.Store
}

func (f *orphanSweepFaultStore) DeleteOrphanBrokerCredentials(context.Context) (int, int, error) {
	return 0, 0, errors.New("db down")
}

// A failed sweep is returned and logged as an error, and does not stop
// startup: the orphan stays, and broker authentication still refuses it
// (no broker row).
func TestSweepOrphanBrokerCredentials_FailureLoggedStartupContinues(t *testing.T) {
	s, _, orphan := orphanSweepStore(t)
	fault := &orphanSweepFaultStore{Store: s}
	logs := orphanSweepLogs(t)

	_, _, err := sweepOrphanBrokerCredentials(context.Background(), fault)
	require.Error(t, err)
	assert.Contains(t, logs.String(), "level=ERROR")
	assert.Contains(t, logs.String(), "Failed to sweep broker credentials that have no broker row")
	assert.NotContains(t, logs.String(), orphanSweepTestKey)

	cfg := DefaultServerConfig()
	cfg.DevAuthToken = testDevToken
	cfg.BrokerAuthConfig = DefaultBrokerAuthConfig()
	srv, err := newTestHubServer(t, cfg, fault)
	require.NoError(t, err, "a failed sweep does not stop startup")
	require.NotNil(t, srv)
	assertCredentials(t, s, orphan, true, "the failed sweep removed nothing")
}

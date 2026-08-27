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
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
	"github.com/stretchr/testify/require"

	"github.com/GoogleCloudPlatform/scion/pkg/messaging"
	"github.com/GoogleCloudPlatform/scion/pkg/store"
)

// testConvUpserter implements messaging.ConversationUpserter against the raw
// SQLite conversations table. It inserts or updates a conversation row,
// matching the semantics of the real Ent-backed store.
type testConvUpserter struct {
	db *sql.DB
}

func (u *testConvUpserter) UpsertConversationByExternalRef(ctx context.Context, conv *store.Conversation) (*store.Conversation, error) {
	// Check if a row with this external_ref already exists.
	var existingID string
	err := u.db.QueryRowContext(ctx,
		`SELECT id FROM conversations WHERE external_ref = ?`, conv.ExternalRef).Scan(&existingID)
	if err == nil {
		// Row exists — return it.
		conv.ID = existingID
		return conv, nil
	}
	if err != sql.ErrNoRows {
		return nil, fmt.Errorf("test upsert lookup: %w", err)
	}
	// Insert new row.
	id := fmt.Sprintf("conv-auto-%d", time.Now().UnixNano())
	now := time.Now().UTC().Format(time.RFC3339Nano)
	projectID := ""
	if conv.ProjectID != nil {
		projectID = *conv.ProjectID
	}
	_, err = u.db.ExecContext(ctx,
		`INSERT INTO conversations (id, project_id, kind, surface, external_ref, drift_state, last_activity_at, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		id, projectID, conv.Kind, conv.Surface, conv.ExternalRef, conv.DriftState, now, now)
	if err != nil {
		return nil, fmt.Errorf("test upsert insert: %w", err)
	}
	conv.ID = id
	return conv, nil
}

func countConversations(t *testing.T, db *sql.DB) int {
	t.Helper()
	var n int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM conversations`).Scan(&n))
	return n
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(&discardWriter{}, nil))
}

type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }

// ---------------------------------------------------------------------------
// AC-27-1: Soft-deleted topic does NOT cause the mint guard to mint
// (reproduction -- must fail before the fix)
// ---------------------------------------------------------------------------

func TestDEF27_SoftDeletedTopicDoesNotMint_SQLite(t *testing.T) {
	store, db := newUnifyTestStore(t)
	defer db.Close() //nolint:errcheck

	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	// Create a topic with a conversation_id (simulates the dual-write path).
	topic := WebChatTopic{
		ID:             "topic-def27-deleted",
		ProjectID:      "proj-1",
		Name:           "will-be-deleted",
		ConversationID: "conv-def27-existing",
		CreatedBy:      "user-1",
		CreatedAt:      now,
	}
	require.NoError(t, store.CreateTopic(ctx, topic))

	// Verify the topic exists and has a conversation_id.
	got, err := store.GetTopicConversationID(ctx, "topic-def27-deleted")
	require.NoError(t, err)
	require.Equal(t, "conv-def27-existing", got)

	// Soft-delete the topic.
	require.NoError(t, store.DeleteTopic(ctx, "topic-def27-deleted"))

	// The user-facing accessor must return ErrNotFound (it hides deleted topics).
	_, err = store.GetTopicConversationID(ctx, "topic-def27-deleted")
	require.Error(t, err, "user-facing accessor must hide soft-deleted topics")

	// The mint-guard accessor must still see the tombstoned topic.
	convID, err := store.GetTopicConversationIDIncludingDeleted(ctx, "topic-def27-deleted")
	require.NoError(t, err, "GetTopicConversationIDIncludingDeleted must NOT return error for soft-deleted topic")
	require.Equal(t, "conv-def27-existing", convID,
		"expected no new conversation for soft-deleted topic -- "+
			"GetTopicConversationIDIncludingDeleted must return the existing conversation_id")
}

// ---------------------------------------------------------------------------
// AC-27-2 (paired positive): live topic still resolves, unknown thread mints
// ---------------------------------------------------------------------------

func TestDEF27_LiveTopicStillResolves_SQLite(t *testing.T) {
	store, db := newUnifyTestStore(t)
	defer db.Close() //nolint:errcheck

	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	topic := WebChatTopic{
		ID:             "topic-def27-live",
		ProjectID:      "proj-1",
		Name:           "live-topic",
		ConversationID: "conv-def27-live",
		CreatedBy:      "user-1",
		CreatedAt:      now,
	}
	require.NoError(t, store.CreateTopic(ctx, topic))

	// Both accessors must return the conversation_id for a live topic.
	convID, err := store.GetTopicConversationID(ctx, "topic-def27-live")
	require.NoError(t, err)
	require.Equal(t, "conv-def27-live", convID)

	convID2, err := store.GetTopicConversationIDIncludingDeleted(ctx, "topic-def27-live")
	require.NoError(t, err)
	require.Equal(t, "conv-def27-live", convID2)
}

func TestDEF27_UnknownThreadStillMints_SQLite(t *testing.T) {
	store, db := newUnifyTestStore(t)
	defer db.Close() //nolint:errcheck

	ctx := context.Background()

	// An unknown topic ID must return ErrNotFound from both accessors.
	_, err := store.GetTopicConversationID(ctx, "nonexistent-topic")
	require.Error(t, err, "user-facing accessor must return error for unknown topic")

	_, err = store.GetTopicConversationIDIncludingDeleted(ctx, "nonexistent-topic")
	require.Error(t, err, "including-deleted accessor must return error for unknown topic")

	// When both accessors return ErrNotFound, the sink falls through to upsert
	// (mints a new conversation). This is the normal path for non-native threads.
	_ = db // verify we can still use the DB
}

// ---------------------------------------------------------------------------
// AC-27-5: User-facing accessor regression -- still hides deleted topics
// ---------------------------------------------------------------------------

func TestDEF27_UserFacingAccessorHidesDeleted_SQLite(t *testing.T) {
	store, db := newUnifyTestStore(t)
	defer db.Close() //nolint:errcheck

	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	topic := WebChatTopic{
		ID:             "topic-def27-userfacing",
		ProjectID:      "proj-1",
		Name:           "user-facing-test",
		ConversationID: "conv-def27-uf",
		CreatedBy:      "user-1",
		CreatedAt:      now,
	}
	require.NoError(t, store.CreateTopic(ctx, topic))

	// Soft-delete.
	require.NoError(t, store.DeleteTopic(ctx, "topic-def27-userfacing"))

	// GetTopicConversationID (user-facing) must return ErrNotFound.
	_, err := store.GetTopicConversationID(ctx, "topic-def27-userfacing")
	require.Error(t, err, "GetTopicConversationID must return ErrNotFound for soft-deleted topics")
}

// ---------------------------------------------------------------------------
// section 8: Tombstoned topic with empty conversation_id returns unresolved
// ---------------------------------------------------------------------------

func TestDEF27_TombstonedTopicWithNoConvID_ReturnsUnresolved_SQLite(t *testing.T) {
	store, db := newUnifyTestStore(t)
	defer db.Close() //nolint:errcheck

	ctx := context.Background()

	// Insert a topic WITHOUT conversation_id (pre-backfill legacy row),
	// then soft-delete it.
	_, err := db.ExecContext(ctx,
		`INSERT INTO webchat_topic (id, project_id, name, is_general, created_by, created_at)
		 VALUES ('topic-def27-noconv', 'proj-1', 'no-conv-topic', 0, 'user-1', ?)`,
		time.Now().UTC().Format(time.RFC3339Nano))
	require.NoError(t, err)

	// Soft-delete.
	_, err = db.ExecContext(ctx,
		`UPDATE webchat_topic SET deleted_at = ? WHERE id = 'topic-def27-noconv'`,
		time.Now().UTC().Format(time.RFC3339Nano))
	require.NoError(t, err)

	// GetTopicConversationIDIncludingDeleted must return ("", nil):
	// topic found, but no conversation_id.
	convID, err := store.GetTopicConversationIDIncludingDeleted(ctx, "topic-def27-noconv")
	require.NoError(t, err, "must not error for tombstoned topic without conversation_id")
	require.Empty(t, convID, "conversation_id must be empty for unbackfilled tombstoned topic")

	// The sink handles empty convID at derive_key.go lines 161-165:
	// returns nil (unresolved), message stored without conversation link.
	// Degraded, not dropped.
}

// ---------------------------------------------------------------------------
// AC-27-3: Postgres backend tests (separate functions, own setup)
// ---------------------------------------------------------------------------

func newDEF27PgTestStore(t *testing.T) (*pgWebChatStore, *sql.DB) {
	t.Helper()
	dsn := os.Getenv("SCION_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("set SCION_TEST_POSTGRES_DSN to run Postgres DEF-27 tests")
	}

	db, err := sql.Open("postgres", dsn)
	require.NoError(t, err)

	store := &pgWebChatStore{db: db}
	require.NoError(t, store.Init())

	// Create the conversations table (Ent-managed in production).
	_, err = db.Exec(`
CREATE TABLE IF NOT EXISTS conversations (
    id TEXT PRIMARY KEY,
    project_id TEXT,
    kind TEXT NOT NULL,
    surface TEXT NOT NULL,
    external_ref TEXT NOT NULL DEFAULT '',
    parent_ref TEXT NOT NULL DEFAULT '',
    display_name TEXT NOT NULL DEFAULT '',
    default_agent_id TEXT,
    drift_state TEXT NOT NULL DEFAULT 'active',
    last_activity_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    archived_at TIMESTAMPTZ,
    deleted_at TIMESTAMPTZ
)
`)
	require.NoError(t, err)

	// Clean up previous test data to ensure isolation.
	_, _ = db.Exec(`DELETE FROM webchat_topic WHERE id LIKE 'topic-def27-%'`)
	_, _ = db.Exec(`DELETE FROM conversations WHERE id LIKE 'conv-def27-%'`)

	return store, db
}

func TestDEF27_SoftDeletedTopicDoesNotMint_Postgres(t *testing.T) {
	store, db := newDEF27PgTestStore(t)
	defer db.Close() //nolint:errcheck

	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	topic := WebChatTopic{
		ID:             "topic-def27-pg-deleted",
		ProjectID:      "proj-1",
		Name:           "pg-will-be-deleted",
		ConversationID: "conv-def27-pg-existing",
		CreatedBy:      "user-1",
		CreatedAt:      now,
	}
	require.NoError(t, store.CreateTopic(ctx, topic))

	// Verify topic exists with conversation_id.
	got, err := store.GetTopicConversationID(ctx, "topic-def27-pg-deleted")
	require.NoError(t, err)
	require.Equal(t, "conv-def27-pg-existing", got)

	// Soft-delete.
	require.NoError(t, store.DeleteTopic(ctx, "topic-def27-pg-deleted"))

	// User-facing accessor must return error.
	_, err = store.GetTopicConversationID(ctx, "topic-def27-pg-deleted")
	require.Error(t, err, "user-facing accessor must hide soft-deleted topics on Postgres")

	// Mint-guard accessor must still see the tombstoned topic.
	convID, err := store.GetTopicConversationIDIncludingDeleted(ctx, "topic-def27-pg-deleted")
	require.NoError(t, err, "GetTopicConversationIDIncludingDeleted must NOT return error for soft-deleted topic on Postgres")
	require.Equal(t, "conv-def27-pg-existing", convID,
		"expected no new conversation for soft-deleted topic -- "+
			"GetTopicConversationIDIncludingDeleted must return the existing conversation_id on Postgres")

	// Cleanup.
	_, _ = db.Exec(`DELETE FROM webchat_topic WHERE id = 'topic-def27-pg-deleted'`)
	_, _ = db.Exec(`DELETE FROM conversations WHERE id = 'conv-def27-pg-existing'`)
}

func TestDEF27_LiveTopicStillResolves_Postgres(t *testing.T) {
	store, db := newDEF27PgTestStore(t)
	defer db.Close() //nolint:errcheck

	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	topic := WebChatTopic{
		ID:             "topic-def27-pg-live",
		ProjectID:      "proj-1",
		Name:           "pg-live-topic",
		ConversationID: "conv-def27-pg-live",
		CreatedBy:      "user-1",
		CreatedAt:      now,
	}
	require.NoError(t, store.CreateTopic(ctx, topic))

	convID, err := store.GetTopicConversationID(ctx, "topic-def27-pg-live")
	require.NoError(t, err)
	require.Equal(t, "conv-def27-pg-live", convID)

	convID2, err := store.GetTopicConversationIDIncludingDeleted(ctx, "topic-def27-pg-live")
	require.NoError(t, err)
	require.Equal(t, "conv-def27-pg-live", convID2)

	// Cleanup.
	_, _ = db.Exec(`DELETE FROM webchat_topic WHERE id = 'topic-def27-pg-live'`)
	_, _ = db.Exec(`DELETE FROM conversations WHERE id = 'conv-def27-pg-live'`)
}

func TestDEF27_UnknownThreadStillMints_Postgres(t *testing.T) {
	store, db := newDEF27PgTestStore(t)
	defer db.Close() //nolint:errcheck

	ctx := context.Background()

	_, err := store.GetTopicConversationID(ctx, "nonexistent-pg-topic")
	require.Error(t, err, "user-facing accessor must return error for unknown topic on Postgres")

	_, err = store.GetTopicConversationIDIncludingDeleted(ctx, "nonexistent-pg-topic")
	require.Error(t, err, "including-deleted accessor must return error for unknown topic on Postgres")

	_ = db
}

func TestDEF27_UserFacingAccessorHidesDeleted_Postgres(t *testing.T) {
	store, db := newDEF27PgTestStore(t)
	defer db.Close() //nolint:errcheck

	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	topic := WebChatTopic{
		ID:             "topic-def27-pg-uf",
		ProjectID:      "proj-1",
		Name:           "pg-user-facing",
		ConversationID: "conv-def27-pg-uf",
		CreatedBy:      "user-1",
		CreatedAt:      now,
	}
	require.NoError(t, store.CreateTopic(ctx, topic))

	require.NoError(t, store.DeleteTopic(ctx, "topic-def27-pg-uf"))

	_, err := store.GetTopicConversationID(ctx, "topic-def27-pg-uf")
	require.Error(t, err, "GetTopicConversationID must return ErrNotFound for soft-deleted topics on Postgres")

	// Cleanup.
	_, _ = db.Exec(`DELETE FROM webchat_topic WHERE id = 'topic-def27-pg-uf'`)
	_, _ = db.Exec(`DELETE FROM conversations WHERE id = 'conv-def27-pg-uf'`)
}

func TestDEF27_TombstonedTopicWithNoConvID_ReturnsUnresolved_Postgres(t *testing.T) {
	store, db := newDEF27PgTestStore(t)
	defer db.Close() //nolint:errcheck

	ctx := context.Background()

	// Insert a topic WITHOUT conversation_id, then soft-delete it.
	_, err := db.ExecContext(ctx,
		`INSERT INTO webchat_topic (id, project_id, name, is_general, created_by, created_at)
		 VALUES ('topic-def27-pg-noconv', 'proj-1', 'pg-no-conv', FALSE, 'user-1', NOW())`)
	require.NoError(t, err)

	_, err = db.ExecContext(ctx,
		`UPDATE webchat_topic SET deleted_at = NOW() WHERE id = 'topic-def27-pg-noconv'`)
	require.NoError(t, err)

	convID, err := store.GetTopicConversationIDIncludingDeleted(ctx, "topic-def27-pg-noconv")
	require.NoError(t, err, "must not error for tombstoned topic without conversation_id on Postgres")
	require.Empty(t, convID, "conversation_id must be empty for unbackfilled tombstoned topic on Postgres")

	// Cleanup.
	_, _ = db.Exec(`DELETE FROM webchat_topic WHERE id = 'topic-def27-pg-noconv'`)
}

// ---------------------------------------------------------------------------
// AC-27-1 PROPER: Sink-level tests — drive ResolveOrCreateConversationByKey
// against the real store and assert on the conversations table.
// ---------------------------------------------------------------------------

func TestDEF27_SinkLevel_SoftDeletedTopicDoesNotMint_SQLite(t *testing.T) {
	wcStore, db := newUnifyTestStore(t)
	defer db.Close() //nolint:errcheck
	upserter := &testConvUpserter{db: db}

	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	// Create a topic with a linked conversation via dual-write.
	topic := WebChatTopic{
		ID:             "topic-sink-deleted",
		ProjectID:      "proj-1",
		Name:           "sink-deleted-topic",
		ConversationID: "conv-sink-existing",
		CreatedBy:      "user-1",
		CreatedAt:      now,
	}
	require.NoError(t, wcStore.CreateTopic(ctx, topic))

	// Soft-delete the topic.
	require.NoError(t, wcStore.DeleteTopic(ctx, "topic-sink-deleted"))

	// Count conversations BEFORE driving the sink.
	countBefore := countConversations(t, db)

	// Drive the sink with the thread ref for the soft-deleted topic.
	pid := "proj-1"
	result := messaging.ResolveOrCreateConversationByKey(
		ctx, upserter, testLogger(),
		"thread:proj-1:topic-sink-deleted", "group", &pid,
		messaging.WithKeyTopicLookup(wcStore))

	// Count conversations AFTER — must be unchanged (no mint).
	countAfter := countConversations(t, db)
	require.Equal(t, countBefore, countAfter,
		"conversation count must not increase — soft-deleted topic must NOT cause a mint")

	// Verify no row with the thread ref was created.
	var refCount int
	require.NoError(t, db.QueryRow(
		`SELECT COUNT(*) FROM conversations WHERE external_ref = ?`,
		"thread:proj-1:topic-sink-deleted").Scan(&refCount))
	require.Equal(t, 0, refCount,
		"no conversations row bearing the soft-deleted topic's thread ref should exist — "+
			"UpsertConversationByExternalRef must NOT be called for a soft-deleted native topic")

	// The result should resolve to the existing conversation (or nil is acceptable per spec §5).
	if result != nil {
		require.Equal(t, "conv-sink-existing", result.ConversationID,
			"if resolved, must be the existing conversation, not a minted one")
	}
}

func TestDEF27_SinkLevel_LiveTopicResolvesToExistingConv_SQLite(t *testing.T) {
	wcStore, db := newUnifyTestStore(t)
	defer db.Close() //nolint:errcheck
	upserter := &testConvUpserter{db: db}

	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	topic := WebChatTopic{
		ID:             "topic-sink-live",
		ProjectID:      "proj-1",
		Name:           "sink-live-topic",
		ConversationID: "conv-sink-live",
		CreatedBy:      "user-1",
		CreatedAt:      now,
	}
	require.NoError(t, wcStore.CreateTopic(ctx, topic))

	countBefore := countConversations(t, db)

	pid := "proj-1"
	result := messaging.ResolveOrCreateConversationByKey(
		ctx, upserter, testLogger(),
		"thread:proj-1:topic-sink-live", "group", &pid,
		messaging.WithKeyTopicLookup(wcStore))

	countAfter := countConversations(t, db)
	require.Equal(t, countBefore, countAfter,
		"conversation count must not increase — live topic resolves to existing conversation")

	require.NotNil(t, result, "live topic must resolve")
	require.Equal(t, "conv-sink-live", result.ConversationID,
		"must resolve to the topic's linked conversation")
}

func TestDEF27_SinkLevel_UnknownThreadStillMints_SQLite(t *testing.T) {
	wcStore, db := newUnifyTestStore(t)
	defer db.Close() //nolint:errcheck
	upserter := &testConvUpserter{db: db}

	ctx := context.Background()

	countBefore := countConversations(t, db)

	pid := "proj-1"
	result := messaging.ResolveOrCreateConversationByKey(
		ctx, upserter, testLogger(),
		"thread:proj-1:unknown-thread-id", "group", &pid,
		messaging.WithKeyTopicLookup(wcStore))

	countAfter := countConversations(t, db)
	require.Equal(t, countBefore+1, countAfter,
		"conversation count must increase by 1 — unknown thread should mint")

	require.NotNil(t, result, "unknown thread must mint a conversation")
}

func TestDEF27_SinkLevel_TombstonedNoConvID_ReturnsUnresolved_SQLite(t *testing.T) {
	wcStore, db := newUnifyTestStore(t)
	defer db.Close() //nolint:errcheck
	upserter := &testConvUpserter{db: db}

	ctx := context.Background()

	// Insert a topic WITHOUT conversation_id (pre-backfill legacy), then soft-delete.
	_, err := db.ExecContext(ctx,
		`INSERT INTO webchat_topic (id, project_id, name, is_general, created_by, created_at)
		 VALUES ('topic-sink-noconv', 'proj-1', 'no-conv', 0, 'user-1', ?)`,
		time.Now().UTC().Format(time.RFC3339Nano))
	require.NoError(t, err)
	_, err = db.ExecContext(ctx,
		`UPDATE webchat_topic SET deleted_at = ? WHERE id = 'topic-sink-noconv'`,
		time.Now().UTC().Format(time.RFC3339Nano))
	require.NoError(t, err)

	countBefore := countConversations(t, db)

	pid := "proj-1"
	result := messaging.ResolveOrCreateConversationByKey(
		ctx, upserter, testLogger(),
		"thread:proj-1:topic-sink-noconv", "group", &pid,
		messaging.WithKeyTopicLookup(wcStore))

	countAfter := countConversations(t, db)
	require.Equal(t, countBefore, countAfter,
		"conversation count must not increase — tombstoned topic with no convID must not mint")

	// §8 decision: returns unresolved (nil). Message stored unlinked. Degraded, not dropped.
	require.Nil(t, result,
		"tombstoned topic with empty conversation_id must return unresolved")
}

// ---------------------------------------------------------------------------
// AC-27-3: Sink-level Postgres tests (separate functions, own setup)
// ---------------------------------------------------------------------------

func TestDEF27_SinkLevel_SoftDeletedTopicDoesNotMint_Postgres(t *testing.T) {
	wcStore, db := newDEF27PgTestStore(t)
	defer db.Close() //nolint:errcheck
	upserter := &testConvUpserter{db: db}

	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	topic := WebChatTopic{
		ID:             "topic-sink-pg-deleted",
		ProjectID:      "proj-1",
		Name:           "pg-sink-deleted",
		ConversationID: "conv-sink-pg-existing",
		CreatedBy:      "user-1",
		CreatedAt:      now,
	}
	require.NoError(t, wcStore.CreateTopic(ctx, topic))
	require.NoError(t, wcStore.DeleteTopic(ctx, "topic-sink-pg-deleted"))

	countBefore := countConversations(t, db)

	pid := "proj-1"
	result := messaging.ResolveOrCreateConversationByKey(
		ctx, upserter, testLogger(),
		"thread:proj-1:topic-sink-pg-deleted", "group", &pid,
		messaging.WithKeyTopicLookup(wcStore))

	countAfter := countConversations(t, db)
	require.Equal(t, countBefore, countAfter,
		"conversation count must not increase — soft-deleted topic must NOT cause a mint (Postgres)")

	var refCount int
	require.NoError(t, db.QueryRow(
		`SELECT COUNT(*) FROM conversations WHERE external_ref = $1`,
		"thread:proj-1:topic-sink-pg-deleted").Scan(&refCount))
	require.Equal(t, 0, refCount,
		"no conversations row bearing the soft-deleted topic's thread ref should exist (Postgres)")

	if result != nil {
		require.Equal(t, "conv-sink-pg-existing", result.ConversationID)
	}

	_, _ = db.Exec(`DELETE FROM webchat_topic WHERE id = 'topic-sink-pg-deleted'`)
	_, _ = db.Exec(`DELETE FROM conversations WHERE id = 'conv-sink-pg-existing'`)
}

func TestDEF27_SinkLevel_LiveTopicResolvesToExistingConv_Postgres(t *testing.T) {
	wcStore, db := newDEF27PgTestStore(t)
	defer db.Close() //nolint:errcheck
	upserter := &testConvUpserter{db: db}

	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	topic := WebChatTopic{
		ID:             "topic-sink-pg-live",
		ProjectID:      "proj-1",
		Name:           "pg-sink-live",
		ConversationID: "conv-sink-pg-live",
		CreatedBy:      "user-1",
		CreatedAt:      now,
	}
	require.NoError(t, wcStore.CreateTopic(ctx, topic))

	countBefore := countConversations(t, db)

	pid := "proj-1"
	result := messaging.ResolveOrCreateConversationByKey(
		ctx, upserter, testLogger(),
		"thread:proj-1:topic-sink-pg-live", "group", &pid,
		messaging.WithKeyTopicLookup(wcStore))

	countAfter := countConversations(t, db)
	require.Equal(t, countBefore, countAfter,
		"conversation count must not increase — live topic resolves (Postgres)")

	require.NotNil(t, result)
	require.Equal(t, "conv-sink-pg-live", result.ConversationID)

	_, _ = db.Exec(`DELETE FROM webchat_topic WHERE id = 'topic-sink-pg-live'`)
	_, _ = db.Exec(`DELETE FROM conversations WHERE id = 'conv-sink-pg-live'`)
}

func TestDEF27_SinkLevel_UnknownThreadStillMints_Postgres(t *testing.T) {
	wcStore, db := newDEF27PgTestStore(t)
	defer db.Close() //nolint:errcheck
	upserter := &testConvUpserter{db: db}

	ctx := context.Background()

	countBefore := countConversations(t, db)

	pid := "proj-1"
	result := messaging.ResolveOrCreateConversationByKey(
		ctx, upserter, testLogger(),
		"thread:proj-1:unknown-pg-thread", "group", &pid,
		messaging.WithKeyTopicLookup(wcStore))

	countAfter := countConversations(t, db)
	require.Equal(t, countBefore+1, countAfter,
		"conversation count must increase by 1 — unknown thread should mint (Postgres)")

	require.NotNil(t, result)

	_, _ = db.Exec(`DELETE FROM conversations WHERE external_ref = 'thread:proj-1:unknown-pg-thread'`)
}

func TestDEF27_SinkLevel_TombstonedNoConvID_ReturnsUnresolved_Postgres(t *testing.T) {
	wcStore, db := newDEF27PgTestStore(t)
	defer db.Close() //nolint:errcheck
	upserter := &testConvUpserter{db: db}

	ctx := context.Background()

	_, err := db.ExecContext(ctx,
		`INSERT INTO webchat_topic (id, project_id, name, is_general, created_by, created_at)
		 VALUES ('topic-sink-pg-noconv', 'proj-1', 'pg-no-conv', FALSE, 'user-1', NOW())`)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx,
		`UPDATE webchat_topic SET deleted_at = NOW() WHERE id = 'topic-sink-pg-noconv'`)
	require.NoError(t, err)

	countBefore := countConversations(t, db)

	pid := "proj-1"
	result := messaging.ResolveOrCreateConversationByKey(
		ctx, upserter, testLogger(),
		"thread:proj-1:topic-sink-pg-noconv", "group", &pid,
		messaging.WithKeyTopicLookup(wcStore))

	countAfter := countConversations(t, db)
	require.Equal(t, countBefore, countAfter,
		"conversation count must not increase — tombstoned no-convID must not mint (Postgres)")

	require.Nil(t, result,
		"tombstoned topic with empty conversation_id must return unresolved (Postgres)")

	_, _ = db.Exec(`DELETE FROM webchat_topic WHERE id = 'topic-sink-pg-noconv'`)
}

// ---------------------------------------------------------------------------
// AC-27-4: Mutation proof (documented, not a test)
//
// Two mutations are documented. Both must produce FAIL with an assertion
// naming the mint (not a panic, not a compile error).
//
// MUTATION 1 — Accessor mutation (store layer):
//
//   1. In pkg/hub/webchannel_store.go, restore the deleted_at filter:
//      Change:
//        const query = `SELECT COALESCE(conversation_id, '') FROM webchat_topic WHERE id = ?`
//      To:
//        const query = `SELECT COALESCE(conversation_id, '') FROM webchat_topic WHERE id = ? AND deleted_at IS NULL`
//
//   2. Run:
//        go test ./pkg/hub/... -run TestDEF27_SoftDeletedTopicDoesNotMint_SQLite -v -count=1
//
//   3. The test must fail with:
//        "GetTopicConversationIDIncludingDeleted must NOT return error for soft-deleted topic"
//      This names the defect: the mint guard fails to see the tombstoned topic.
//
//   The same applies to the Postgres backend by mutating webchannel_store_postgres.go.
//
// MUTATION 2 — Wiring mutation (sink layer):
//
//   1. In pkg/messaging/derive_key.go line 164, change:
//        convID, lookupErr := cfg.topicLookup.GetTopicConversationIDIncludingDeleted(ctx, threadID)
//      To:
//        convID, lookupErr := cfg.topicLookup.GetTopicConversationID(ctx, threadID)
//
//   2. Run:
//        go test ./pkg/hub/... -run TestDEF27_SinkLevel -v -count=1
//
//   3. Two tests fail:
//        TestDEF27_SinkLevel_SoftDeletedTopicDoesNotMint_SQLite:
//          "conversation count must not increase — soft-deleted topic must NOT cause a mint"
//          (expected 1, actual 2)
//        TestDEF27_SinkLevel_TombstonedNoConvID_ReturnsUnresolved_SQLite:
//          "conversation count must not increase — tombstoned topic with no convID must not mint"
//          (expected 0, actual 1)
//
//   Both failures name the mint (conversation count increased). The paired
//   positives (LiveTopicResolves, UnknownThreadStillMints) remain PASS.
//   No panics, no compile errors — the mutation is the defect.
//
// Mutation 2 is the architect's exact test: it proves the sink-level tests
// catch DEF-27 reintroduced at the wiring point, not just at the accessor.
// ---------------------------------------------------------------------------

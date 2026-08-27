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
	"os"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
	"github.com/stretchr/testify/require"
)

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
// AC-27-4: Mutation proof (documented, not a test)
//
// To verify AC-27-4 (the mutation that proves the test catches the defect):
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
//      It is NOT a panic or compile error.
//
//   The same applies to the Postgres backend by mutating webchannel_store_postgres.go.
// ---------------------------------------------------------------------------

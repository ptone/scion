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
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/GoogleCloudPlatform/scion/pkg/ent"
	"github.com/GoogleCloudPlatform/scion/pkg/ent/entc"
	"github.com/GoogleCloudPlatform/scion/pkg/hub/internal/hubtestutil"
)

func testSoftDeleteDMParity(t *testing.T, wcs WebChatStore, db *sql.DB, isPostgres bool) {
	t.Helper()
	ctx := context.Background()

	rebind := func(q string) string {
		if !isPostgres {
			return q
		}
		var out []byte
		arg := 1
		for i := 0; i < len(q); i++ {
			if q[i] == '?' {
				out = append(out, fmt.Sprintf("$%d", arg)...)
				arg++
			} else {
				out = append(out, q[i])
			}
		}
		return string(out)
	}

	userA := uuid.NewString()
	agentA := uuid.NewString()
	dmKey := fmt.Sprintf("dm:agent:%s:user:%s", agentA, userA)
	otherKey := fmt.Sprintf("dm:agent:%s:user:%s", uuid.NewString(), userA)

	convID := uuid.NewString()
	otherConvID := uuid.NewString()
	nonNativeConvID := uuid.NewString()
	now := time.Now().UTC().Truncate(time.Microsecond)
	oldActivity := now.Add(-48 * time.Hour)

	insertConv := func(id, surface, extRef string, lastAct time.Time) {
		_, err := db.ExecContext(ctx, rebind(`
			INSERT INTO conversations (id, kind, surface, external_ref, parent_ref, display_name, drift_state, last_activity_at, last_message_seq, created_at)
			VALUES (?, 'direct', ?, ?, '', '', 'active', ?, 0, ?)
		`), id, surface, extRef, lastAct, lastAct)
		require.NoError(t, err)
	}
	insertConv(convID, "native", dmKey, oldActivity)
	insertConv(otherConvID, "native", otherKey, oldActivity)
	insertConv(nonNativeConvID, "slack", dmKey, oldActivity)

	// Seed thread_group_members row for convID.
	groupID := uuid.NewString()
	projID := uuid.NewString()
	_, err := db.ExecContext(ctx, rebind(`
		INSERT INTO thread_group_members (id, group_id, project_id, conversation_id, added_by, added_at)
		VALUES (?, ?, ?, ?, ?, ?)
	`), uuid.NewString(), groupID, projID, convID, userA, now)
	require.NoError(t, err)

	// Seed webchat_dm rows for both participants + another DM.
	require.NoError(t, wcs.UpsertDM(ctx, WebChatDM{
		ConversationKey: dmKey,
		ParticipantID:   userA,
		PeerID:          agentA,
		PeerKind:        "agent",
		LastActivityAt:  oldActivity,
	}))
	require.NoError(t, wcs.UpsertDM(ctx, WebChatDM{
		ConversationKey: dmKey,
		ParticipantID:   agentA,
		PeerID:          userA,
		PeerKind:        "user",
		LastActivityAt:  oldActivity,
	}))
	require.NoError(t, wcs.UpsertDM(ctx, WebChatDM{
		ConversationKey: otherKey,
		ParticipantID:   userA,
		PeerID:          uuid.NewString(),
		PeerKind:        "agent",
		LastActivityAt:  oldActivity,
	}))

	// Seed webchat_read_state (pinned & muted) and webchat_mention.
	require.NoError(t, wcs.SetPinned(ctx, userA, dmKey, true))
	require.NoError(t, wcs.SetMuted(ctx, userA, dmKey, true))
	require.NoError(t, wcs.RecordMentions(ctx, dmKey, uuid.NewString(), []string{userA}))

	// 1. Predicate that does NOT match (cutoff older than last_activity_at) -> ErrLifecycleSkipped and rollback.
	tooOldCutoff := oldActivity.Add(-time.Hour)
	gotID, err := wcs.SoftDeleteDM(ctx, dmKey, &tooOldCutoff)
	require.ErrorIs(t, err, ErrLifecycleSkipped)
	assert.Empty(t, gotID)

	// Everything must still be intact after rollback.
	dms, err := wcs.ListDMs(ctx, userA)
	require.NoError(t, err)
	assert.Len(t, dms, 2)
	rs, err := wcs.GetReadState(ctx, userA, dmKey)
	require.NoError(t, err)
	require.NotNil(t, rs)
	assert.True(t, rs.Pinned)
	assert.True(t, rs.Muted)

	// 2. Predicate that DOES match (cutoff newer than last_activity_at) -> soft-deleted.
	matchingCutoff := oldActivity.Add(time.Hour)
	gotID, err = wcs.SoftDeleteDM(ctx, dmKey, &matchingCutoff)
	require.NoError(t, err)
	assert.Equal(t, convID, gotID)

	// Native conversation has deleted_at set; non-native and other DM are untouched.
	var deletedAt sql.NullTime
	require.NoError(t, db.QueryRowContext(ctx, rebind(`SELECT deleted_at FROM conversations WHERE id = ?`), convID).Scan(&deletedAt))
	assert.True(t, deletedAt.Valid, "native DM conversation must be soft-deleted")

	var otherDeletedAt sql.NullTime
	require.NoError(t, db.QueryRowContext(ctx, rebind(`SELECT deleted_at FROM conversations WHERE id = ?`), otherConvID).Scan(&otherDeletedAt))
	assert.False(t, otherDeletedAt.Valid, "other DM must remain live")

	var slackDeletedAt sql.NullTime
	require.NoError(t, db.QueryRowContext(ctx, rebind(`SELECT deleted_at FROM conversations WHERE id = ?`), nonNativeConvID).Scan(&slackDeletedAt))
	assert.False(t, slackDeletedAt.Valid, "non-native conversation with same external_ref must not be touched")

	// Thread-group membership removed.
	var tgmCount int
	require.NoError(t, db.QueryRowContext(ctx, rebind(`SELECT COUNT(*) FROM thread_group_members WHERE conversation_id = ?`), convID).Scan(&tgmCount))
	assert.Zero(t, tgmCount)

	// Both participants' webchat_dm rows removed; otherKey remains.
	dms, err = wcs.ListDMs(ctx, userA)
	require.NoError(t, err)
	require.Len(t, dms, 1)
	assert.Equal(t, otherKey, dms[0].ConversationKey)
	agentDMs, err := wcs.ListDMs(ctx, agentA)
	require.NoError(t, err)
	assert.Empty(t, agentDMs)

	// Read state and mentions removed.
	rs, err = wcs.GetReadState(ctx, userA, dmKey)
	require.NoError(t, err)
	assert.Nil(t, rs)
	var mentionCount int
	require.NoError(t, db.QueryRowContext(ctx, rebind(`SELECT COUNT(*) FROM webchat_mention WHERE conversation_key = ?`), dmKey).Scan(&mentionCount))
	assert.Zero(t, mentionCount)

	// 3. Legacy DM (registry rows only, no live conversation row):
	legacyKey := fmt.Sprintf("dm:agent:%s:user:%s", uuid.NewString(), userA)
	require.NoError(t, wcs.UpsertDM(ctx, WebChatDM{
		ConversationKey: legacyKey,
		ParticipantID:   userA,
		PeerID:          "legacy-agent",
		PeerKind:        "agent",
		LastActivityAt:  oldActivity,
	}))
	// With predicate -> ErrLifecycleSkipped (no live conversation matched).
	_, err = wcs.SoftDeleteDM(ctx, legacyKey, &matchingCutoff)
	require.ErrorIs(t, err, ErrLifecycleSkipped)
	// Without predicate -> clears registry and returns "".
	gotID, err = wcs.SoftDeleteDM(ctx, legacyKey, nil)
	require.NoError(t, err)
	assert.Empty(t, gotID)
	dms, err = wcs.ListDMs(ctx, userA)
	require.NoError(t, err)
	assert.Len(t, dms, 1)
}

func TestSoftDeleteDM_SQLite(t *testing.T) {
	_, s := testServer(t)
	type dbProvider interface {
		DB() *sql.DB
	}
	p, ok := s.(dbProvider)
	require.True(t, ok)

	wcs := NewWebChatStore(p.DB(), "sqlite")
	require.NoError(t, wcs.Init())

	testSoftDeleteDMParity(t, wcs, p.DB(), false)
}

func TestSoftDeleteDM_Postgres(t *testing.T) {
	dsn := hubtestutil.RequirePostgresDSN(t)
	ctx := context.Background()
	schema := fmt.Sprintf("test_soft_delete_dm_%d", time.Now().UnixNano())

	cfg, err := pgx.ParseConfig(dsn)
	require.NoError(t, err)
	cfg.RuntimeParams["search_path"] = schema
	db := stdlib.OpenDB(*cfg)
	t.Cleanup(func() { _ = db.Close() })

	_, err = db.ExecContext(ctx, "CREATE SCHEMA "+schema)
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = db.ExecContext(context.Background(), "DROP SCHEMA IF EXISTS "+schema+" CASCADE") })

	drv := entsql.OpenDB(dialect.Postgres, db)
	client := ent.NewClient(ent.Driver(drv))
	require.NoError(t, entc.AutoMigrate(ctx, client))

	wcs := NewWebChatStore(db, "postgres")
	require.NoError(t, wcs.Init())

	testSoftDeleteDMParity(t, wcs, db, true)
}

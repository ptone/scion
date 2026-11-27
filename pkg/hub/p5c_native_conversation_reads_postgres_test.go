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

package hub

import (
	"context"
	"testing"
	"time"

	"github.com/GoogleCloudPlatform/scion/pkg/api"
	"github.com/GoogleCloudPlatform/scion/pkg/messages"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestChatSearchConversationIDWithoutThreadID_Postgres is the Postgres twin of
// TestSearchChatMessages_ConversationIDWithoutThreadID: it proves search, DM
// privacy, scoped conversation search, pagination, and counts work both with
// thread_id set and after every message row's thread_id is nulled.
func TestChatSearchConversationIDWithoutThreadID_Postgres(t *testing.T) {
	wcs, db := newPGSearchStore(t)
	ctx := context.Background()

	projID := api.NewUUID()
	topicID := api.NewUUID()
	alice, bob, charlie := api.NewUUID(), api.NewUUID(), api.NewUUID()
	dmKey, err := messages.DMConversationKey("user", alice, "user", bob)
	require.NoError(t, err)

	base := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	t1, t2, dm1 := api.NewUUID(), api.NewUUID(), api.NewUUID()
	pgInsertMessage(t, db, t1, projID, topicID, "needle topic one", base.Add(1*time.Second))
	pgInsertMessage(t, db, t2, projID, topicID, "needle topic two", base.Add(2*time.Second))
	pgInsertMessage(t, db, dm1, projID, dmKey, "needle secret dm", base.Add(3*time.Second))

	verify := func(label string) {
		t.Helper()
		res, _, err := wcs.SearchChatMessages(ctx, ChatSearchFilter{
			Query:               "needle",
			ProjectID:           projID,
			DMParticipantUserID: alice,
			Limit:               10,
		})
		require.NoError(t, err, label)
		require.Len(t, res, 3, label)
		assert.Equal(t, dm1, res[0].MessageID, label)
		assert.Equal(t, dmKey, res[0].ConversationKey, label)
		assert.Equal(t, t2, res[1].MessageID, label)
		assert.Equal(t, topicID, res[1].ConversationKey, label)
		assert.Equal(t, t1, res[2].MessageID, label)
		assert.Equal(t, topicID, res[2].ConversationKey, label)

		res, _, err = wcs.SearchChatMessages(ctx, ChatSearchFilter{
			Query:               "needle",
			ProjectID:           projID,
			DMParticipantUserID: charlie,
			Limit:               10,
		})
		require.NoError(t, err, label)
		assert.Equal(t, []string{t2, t1}, []string{res[0].MessageID, res[1].MessageID}, label)

		res, _, err = wcs.SearchChatMessages(ctx, ChatSearchFilter{
			Query:     "needle",
			ProjectID: projID,
			Limit:     10,
		})
		require.NoError(t, err, label)
		require.Len(t, res, 2, label)

		assert.Equal(t, []string{t2, t1}, searchIDs(t, wcs, ChatSearchFilter{Query: "needle", ConversationKey: topicID}), label)
		assert.Equal(t, []string{dm1}, searchIDs(t, wcs, ChatSearchFilter{Query: "needle", ConversationKey: dmKey}), label)

		topicConvID := uuid.NewSHA1(uuid.NameSpaceOID, []byte("search-test:"+topicID)).String()
		cnt, err := wcs.CountMessages(ctx, topicConvID)
		require.NoError(t, err, label)
		assert.Equal(t, 2, cnt, label)
	}

	verify("with thread_id set")

	_, err = db.ExecContext(ctx, `UPDATE messages SET thread_id = NULL`)
	require.NoError(t, err)
	verify("with thread_id nulled")
}

// TestBackfillNativeMessageConversations_Idempotent_Postgres is the Postgres
// twin of TestBackfillNativeMessageConversations_Idempotent.
func TestBackfillNativeMessageConversations_Idempotent_Postgres(t *testing.T) {
	wcs, db := newPGSearchStore(t)
	ctx := context.Background()

	projID := api.NewUUID()
	topicID := api.NewUUID()
	topicConvID := api.NewUUID()
	otherConvID := api.NewUUID()
	mNull, mKeep, mOther := api.NewUUID(), api.NewUUID(), api.NewUUID()
	now := time.Now().UTC()

	_, err := db.ExecContext(ctx, `INSERT INTO conversations (id, project_id, kind, surface, external_ref, created_at)
		VALUES ($1, $2, 'group', 'native', $3, NOW())`, topicConvID, projID, "thread:"+projID+":"+topicID)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `INSERT INTO webchat_topic (id, project_id, name, created_by, created_at, conversation_id)
		VALUES ($1, $2, 'general', 'u1', NOW(), $3)`, topicID, projID, topicConvID)
	require.NoError(t, err)

	_, err = db.ExecContext(ctx, `INSERT INTO messages (id, project_id, created, channel, thread_id, conversation_id, msg, sender) VALUES
		($1, $2, $3, 'web', $4, NULL, 'null conv', 'user:a'),
		($5, $2, $3, 'web', $4, $6, 'keep conv', 'user:a'),
		($7, $2, $3, 'web', 'unlinked-topic', NULL, 'other', 'user:a')`,
		mNull, projID, now, topicID, mKeep, otherConvID, mOther)
	require.NoError(t, err)

	_, err = db.ExecContext(ctx, `DELETE FROM webchat_migrations WHERE name = 'messages_native_conversation_backfill'`)
	require.NoError(t, err)
	require.NoError(t, NewWebChatStore(db, "postgres").Init())

	check := func(id, want string) {
		t.Helper()
		var got string
		require.NoError(t, db.QueryRowContext(ctx, `SELECT COALESCE(conversation_id::text, '') FROM messages WHERE id = $1`, id).Scan(&got))
		assert.Equal(t, want, got, "message %s", id)
	}
	check(mNull, topicConvID)
	check(mKeep, otherConvID)
	check(mOther, "")

	var migCount int
	require.NoError(t, db.QueryRowContext(ctx, `SELECT COUNT(*) FROM webchat_migrations WHERE name = 'messages_native_conversation_backfill'`).Scan(&migCount))
	assert.Equal(t, 1, migCount)

	// Idempotent on repeat runs.
	require.NoError(t, wcs.Init())
	_, err = db.ExecContext(ctx, `DELETE FROM webchat_migrations WHERE name = 'messages_native_conversation_backfill'`)
	require.NoError(t, err)
	require.NoError(t, NewWebChatStore(db, "postgres").Init())
	check(mNull, topicConvID)
	check(mKeep, otherConvID)
	check(mOther, "")
}

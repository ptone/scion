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
	"encoding/json"
	"log/slog"
	"net/http"
	"testing"
	"time"

	"github.com/GoogleCloudPlatform/scion/pkg/messages"
	"github.com/GoogleCloudPlatform/scion/pkg/store"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestBackfillNativeMessageConversations_Idempotent verifies that the
// messages_native_conversation_backfill startup migration populates
// messages.conversation_id from webchat_topic.conversation_id where NULL or
// empty, preserves already-set conversation_ids, and is idempotent on repeat
// runs (even if re-executed after removing the migration marker).
func TestBackfillNativeMessageConversations_Idempotent(t *testing.T) {
	wcs, db := newPromoteTestStoreWithConversations(t)
	defer func() { _ = db.Close() }()

	projID := uuid.NewString()
	topicID := uuid.NewString()
	topicConvID := uuid.NewString()
	otherConvID := uuid.NewString()
	now := time.Now().UTC().Format(time.RFC3339Nano)

	_, err := db.Exec(`INSERT INTO conversations (id, project_id, kind, surface, external_ref, created_at)
		VALUES (?, ?, 'group', 'native', ?, ?)`, topicConvID, projID, "thread:"+projID+":"+topicID, now)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO webchat_topic (id, project_id, name, created_by, created_at, conversation_id)
		VALUES (?, ?, 'general', 'u1', ?, ?)`, topicID, projID, now, topicConvID)
	require.NoError(t, err)

	// Insert: (1) empty conversation_id, (2) NULL conversation_id (override
	// via column list), (3) already-set conversation_id that must not be
	// overwritten, (4) non-topic message that must stay untouched.
	_, err = db.Exec(`INSERT INTO messages (id, project_id, sender, recipient, channel, thread_id, conversation_id, msg, created) VALUES
		('m-empty', ?, 'user:a', 'chat', 'web', ?, '', 'empty conv', ?),
		('m-keep',  ?, 'user:a', 'chat', 'web', ?, ?, 'keep conv', ?),
		('m-other', ?, 'user:a', 'chat', 'web', 'unlinked-topic', '', 'other', ?)`,
		projID, topicID, now,
		projID, topicID, otherConvID, now,
		projID, now)
	require.NoError(t, err)

	// Delete the marker so Init re-runs the migration on the seeded rows.
	_, err = db.Exec(`DELETE FROM webchat_migrations WHERE name = 'messages_native_conversation_backfill'`)
	require.NoError(t, err)

	require.NoError(t, NewWebChatStore(db, "sqlite").Init())

	check := func(id, want string) {
		t.Helper()
		var got string
		require.NoError(t, db.QueryRow(`SELECT COALESCE(conversation_id, '') FROM messages WHERE id = ?`, id).Scan(&got))
		assert.Equal(t, want, got, "message %s", id)
	}
	check("m-empty", topicConvID)
	check("m-keep", otherConvID)
	check("m-other", "")

	var migCount int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM webchat_migrations WHERE name = 'messages_native_conversation_backfill'`).Scan(&migCount))
	assert.Equal(t, 1, migCount)

	// Idempotent: a second Init and a forced re-run produce identical state.
	require.NoError(t, wcs.Init())
	_, err = db.Exec(`DELETE FROM webchat_migrations WHERE name = 'messages_native_conversation_backfill'`)
	require.NoError(t, err)
	require.NoError(t, NewWebChatStore(db, "sqlite").Init())
	check("m-empty", topicConvID)
	check("m-keep", otherConvID)
	check("m-other", "")
}

// TestSearchChatMessages_ConversationIDWithoutThreadID verifies that chat
// search, DM privacy, scoped conversation search, pagination, and message
// counts work identically both when messages.thread_id is set and after every
// message row's thread_id is nulled (P5c acceptance criterion 6).
func TestSearchChatMessages_ConversationIDWithoutThreadID(t *testing.T) {
	wcs, db := newTestWebChatStoreWithMessages(t)
	defer func() { _ = db.Close() }()
	ctx := context.Background()

	projID := uuid.NewString()
	topicID := uuid.NewString()
	alice, bob, charlie := uuid.NewString(), uuid.NewString(), uuid.NewString()
	dmKey, err := messages.DMConversationKey("user", alice, "user", bob)
	require.NoError(t, err)

	base := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	insertTestMessage(t, db, "t1", projID, topicID, "user:"+alice, "needle topic one", base.Add(1*time.Second))
	insertTestMessage(t, db, "t2", projID, topicID, "user:"+bob, "needle topic two", base.Add(2*time.Second))
	insertTestMessage(t, db, "dm1", projID, dmKey, "user:"+alice, "needle secret dm", base.Add(3*time.Second))

	verify := func(label string) {
		t.Helper()
		// Participant sees topic + own DM, with derived ConversationKey.
		res, _, err := wcs.SearchChatMessages(ctx, ChatSearchFilter{
			Query:               "needle",
			ProjectID:           projID,
			DMParticipantUserID: alice,
			Limit:               10,
		})
		require.NoError(t, err, label)
		require.Len(t, res, 3, label)
		assert.Equal(t, "dm1", res[0].MessageID, label)
		assert.Equal(t, dmKey, res[0].ConversationKey, label)
		assert.Equal(t, "t2", res[1].MessageID, label)
		assert.Equal(t, topicID, res[1].ConversationKey, label)
		assert.Equal(t, "t1", res[2].MessageID, label)
		assert.Equal(t, topicID, res[2].ConversationKey, label)

		// Non-participant sees only topic messages, never the DM.
		res, _, err = wcs.SearchChatMessages(ctx, ChatSearchFilter{
			Query:               "needle",
			ProjectID:           projID,
			DMParticipantUserID: charlie,
			Limit:               10,
		})
		require.NoError(t, err, label)
		assert.Equal(t, []string{"t2", "t1"}, []string{res[0].MessageID, res[1].MessageID}, label)

		// Empty DMParticipantUserID fails closed on DMs.
		res, _, err = wcs.SearchChatMessages(ctx, ChatSearchFilter{
			Query:     "needle",
			ProjectID: projID,
			Limit:     10,
		})
		require.NoError(t, err, label)
		require.Len(t, res, 2, label)

		// Scoped search by topic key and by DM key.
		assert.Equal(t, []string{"t2", "t1"}, searchIDs(t, wcs, ChatSearchFilter{Query: "needle", ConversationKey: topicID}), label)
		assert.Equal(t, []string{"dm1"}, searchIDs(t, wcs, ChatSearchFilter{Query: "needle", ConversationKey: dmKey}), label)

		// Pagination across topic + DM results.
		page1, cursor, err := wcs.SearchChatMessages(ctx, ChatSearchFilter{
			Query:               "needle",
			ProjectID:           projID,
			DMParticipantUserID: alice,
			Limit:               2,
		})
		require.NoError(t, err, label)
		require.Len(t, page1, 2, label)
		require.NotEmpty(t, cursor, label)
		page2, nextCursor, err := wcs.SearchChatMessages(ctx, ChatSearchFilter{
			Query:               "needle",
			ProjectID:           projID,
			DMParticipantUserID: alice,
			Limit:               2,
			Cursor:              cursor,
		})
		require.NoError(t, err, label)
		require.Len(t, page2, 1, label)
		assert.Empty(t, nextCursor, label)
		assert.Equal(t, "t1", page2[0].MessageID, label)

		// Counts key on conversation_id.
		topicConvID := seedSearchConversation(t, db, projID, topicID)
		cnt, err := wcs.CountMessages(ctx, topicConvID)
		require.NoError(t, err, label)
		assert.Equal(t, 2, cnt, label)
	}

	verify("with thread_id set")

	// Null every message row's thread_id and verify every read still passes.
	_, err = db.Exec(`UPDATE messages SET thread_id = NULL`)
	require.NoError(t, err)
	verify("with thread_id nulled")
}

// TestP5c_WriteGapAndRecentMessages verifies:
//  1. resolveUserMessageConversation refuses to persist non-broadcast messages
//     when no conversation can be resolved (no silent NULL conversation_id),
//     while broadcasts still return true.
//  2. conversationRecentMessages returns (nil, nil) for a topic with no
//     resolved conversation instead of falling back to ThreadID.
func TestP5c_WriteGapAndRecentMessages(t *testing.T) {
	srv, s := testServer(t)
	ctx := context.Background()

	proxy := &MessageBrokerProxy{
		store:      s,
		log:        slog.Default(),
		messageLog: slog.Default(),
	}

	// Native web non-broadcast with undetermined principal kind and no
	// thread/conversation must be refused (returns false, no silent NULL
	// conversation_id).
	storeMsg := &store.Message{ID: uuid.NewString()}
	ok := proxy.resolveUserMessageConversation(ctx, uuid.NewString(), storeMsg, &messages.StructuredMessage{
		Channel:     "web",
		Sender:      "unknown-sender",
		SenderID:    uuid.NewString(),
		Recipient:   "unknown-recipient",
		RecipientID: uuid.NewString(),
		Msg:         "hello",
	})
	assert.False(t, ok, "unresolvable native web non-broadcast message must be refused")
	assert.Empty(t, storeMsg.ConversationID)

	// Native web message with empty sender/recipient IDs and no thread/conversation
	// must also be refused.
	ok = proxy.resolveUserMessageConversation(ctx, uuid.NewString(), &store.Message{ID: uuid.NewString()}, &messages.StructuredMessage{
		Channel:   "web",
		Sender:    "user:a",
		Recipient: "user:b",
		Msg:       "hello",
	})
	assert.False(t, ok, "native web message missing IDs with no conversation must be refused")

	// Broadcasts are still allowed without a conversation.
	ok = proxy.resolveUserMessageConversation(ctx, uuid.NewString(), &store.Message{ID: uuid.NewString()}, &messages.StructuredMessage{
		Broadcasted: true,
		Msg:         "broadcast",
	})
	assert.True(t, ok, "broadcasts are ephemeral and allowed without conversation")

	// conversationRecentMessages for a topic without a conversation returns
	// (nil, nil) even if a legacy message row exists with matching thread_id.
	proj := &store.Project{ID: uuid.NewString(), Name: "p5c-recent", Slug: "p5c-recent"}
	require.NoError(t, s.CreateProject(ctx, proj))
	topicID := uuid.NewString()
	require.NoError(t, s.CreateMessage(ctx, &store.Message{
		ID:        uuid.NewString(),
		ProjectID: proj.ID,
		Sender:    "user:a",
		Recipient: "chat",
		Channel:   "web",
		ThreadID:  topicID,
		Msg:       "orphan thread_id row",
		Type:      messages.TypeChat,
		CreatedAt: time.Now().UTC(),
	}))
	msgs, err := srv.conversationRecentMessages(ctx, topicID, false, nil, 10)
	require.NoError(t, err)
	assert.Nil(t, msgs, "topic branch with no resolved conversation must return nil")
}

// TestP5c_HeldWebMentionsOriginMarker verifies the chat_v2_origin marker for
// held-message mention translation:
//  1. A chat-v2 held row with thread_id nulled still translates via the marker.
//  2. A POST /agents/{id}/message held row with thread_id nulled (no marker)
//     stays verbatim.
//  3. A pre-upgrade held row without the marker still translates via the
//     read-only thread_id fallback (to be removed in P6).
func TestP5c_HeldWebMentionsOriginMarker(t *testing.T) {
	srv, s, wcs, proj, db := setupSendTest(t)
	ctx := context.Background()

	aliceID := uuid.NewString()
	createTestUserWithProjectRole(t, s, aliceID, "alice.smith@test.com", proj.ID, store.ProjectRoleMember)
	alice, err := s.GetUser(ctx, aliceID)
	require.NoError(t, err)
	alice.DisplayName = "Alice Smith"
	require.NoError(t, s.UpdateUser(ctx, alice))

	topicID := uuid.NewString()
	require.NoError(t, wcs.CreateTopic(ctx, WebChatTopic{
		ID:        topicID,
		ProjectID: proj.ID,
		Name:      "origin-marker-topic",
		CreatedBy: DevUserID,
		CreatedAt: time.Now().UTC(),
	}))
	setTopicConversationID(t, db, s, topicID, proj.ID)
	topicConvID := topicConversationID(t, wcs, topicID)
	require.NotEmpty(t, topicConvID)

	// 0. Live chat-v2 send stamps chat_v2_origin atomically in CreateMessage.
	bot := &store.Agent{
		ID:        uuid.NewString(),
		ProjectID: proj.ID,
		Name:      "origin-bot",
		Slug:      "origin-bot",
		Phase:     "running",
		OwnerID:   DevUserID,
		CreatedBy: DevUserID,
	}
	require.NoError(t, s.CreateAgent(ctx, bot))
	require.NoError(t, wcs.UpdateTopic(ctx, topicID, TopicUpdate{DefaultAgent: &bot.Slug}))
	srv.SetDispatcher(&brokerMockDispatcher{})
	sendRec := doRequest(t, srv, http.MethodPost, "/api/v1/chat/conversations/"+topicID+"/messages",
		map[string]string{"content": "hello @alice-smith"})
	require.Equal(t, http.StatusCreated, sendRec.Code, sendRec.Body.String())
	var sendResp struct {
		ID string `json:"id"`
	}
	require.NoError(t, json.Unmarshal(sendRec.Body.Bytes(), &sendResp))
	persisted, err := s.GetMessage(ctx, sendResp.ID)
	require.NoError(t, err)
	assert.True(t, persisted.ChatV2Origin, "persisted chat-v2 message must carry chat_v2_origin=true in the messages row")

	// 1. Chat-v2 row with thread_id empty and chat_v2_origin marker set -> translates.
	chatV2ID := uuid.NewString()
	row1 := &store.Message{
		ID:             chatV2ID,
		ProjectID:      proj.ID,
		Sender:         "user:dev",
		SenderID:       DevUserID,
		Recipient:      "agent:bot",
		Channel:        "web",
		ThreadID:       "",
		ConversationID: topicConvID,
		ChatV2Origin:   true,
		Msg:            "Ping @alice-smith please",
		Type:           messages.TypeInstruction,
	}
	sm1 := &messages.StructuredMessage{Msg: row1.Msg}
	require.True(t, shouldTranslateHeldWebMentions(row1))
	require.NoError(t, srv.enrichHeldWebMentions(ctx, row1, sm1, newHeldWebMentionCache()))
	assert.Equal(t, "Ping @alice.smith@test.com please", sm1.Msg, "marked chat-v2 row without thread_id must translate")

	// 2. /agents/{id}/message row with thread_id empty and no marker -> stays verbatim.
	directID := uuid.NewString()
	row2 := &store.Message{
		ID:             directID,
		ProjectID:      proj.ID,
		Sender:         "user:dev",
		SenderID:       DevUserID,
		Recipient:      "agent:bot",
		Channel:        "web",
		ThreadID:       "",
		ConversationID: topicConvID,
		Msg:            "Ping @alice-smith please",
		Type:           messages.TypeInstruction,
	}
	sm2 := &messages.StructuredMessage{Msg: row2.Msg}
	require.True(t, shouldTranslateHeldWebMentions(row2))
	require.NoError(t, srv.enrichHeldWebMentions(ctx, row2, sm2, newHeldWebMentionCache()))
	assert.Equal(t, "Ping @alice-smith please", sm2.Msg, "unmarked row without thread_id must stay verbatim")

	// 3. Pre-upgrade held row without marker (thread_id still set) -> fallback translates.
	legacyID := uuid.NewString()
	row3 := &store.Message{
		ID:             legacyID,
		ProjectID:      proj.ID,
		Sender:         "user:dev",
		SenderID:       DevUserID,
		Recipient:      "agent:bot",
		Channel:        "web",
		ThreadID:       topicID,
		ConversationID: topicConvID,
		Msg:            "Ping @alice-smith please",
		Type:           messages.TypeInstruction,
	}
	sm3 := &messages.StructuredMessage{Msg: row3.Msg}
	require.True(t, shouldTranslateHeldWebMentions(row3))
	require.NoError(t, srv.enrichHeldWebMentions(ctx, row3, sm3, newHeldWebMentionCache()))
	assert.Equal(t, "Ping @alice.smith@test.com please", sm3.Msg, "pre-upgrade unmarked row must translate via fallback")
}

// TestP5c_F1StoredReadersWithoutThreadID verifies F1: scheduled-send reply-to,
// action-inbox recipient fallback, and decisions_agent prompt conversation
// checks all work when messages.thread_id is empty/NULL.
func TestP5c_F1StoredReadersWithoutThreadID(t *testing.T) {
	t.Run("scheduled send reply-to uses conversation_id", func(t *testing.T) {
		srv, s, wcs, proj, db := setupSendTest(t)
		ctx := context.Background()

		topicID := uuid.NewString()
		require.NoError(t, wcs.CreateTopic(ctx, WebChatTopic{
			ID: topicID, ProjectID: proj.ID, Name: "sched-f1", CreatedBy: DevUserID, CreatedAt: time.Now().UTC(),
		}))
		setTopicConversationID(t, db, s, topicID, proj.ID)
		topicConvID := topicConversationID(t, wcs, topicID)

		otherTopicID := uuid.NewString()
		require.NoError(t, wcs.CreateTopic(ctx, WebChatTopic{
			ID: otherTopicID, ProjectID: proj.ID, Name: "sched-f1-other", CreatedBy: DevUserID, CreatedAt: time.Now().UTC(),
		}))
		setTopicConversationID(t, db, s, otherTopicID, proj.ID)
		otherConvID := topicConversationID(t, wcs, otherTopicID)

		sameConvMsgID := uuid.NewString()
		require.NoError(t, s.CreateMessage(ctx, &store.Message{
			ID: sameConvMsgID, ProjectID: proj.ID, Sender: "user:dev", SenderID: DevUserID,
			Recipient: "thread:" + topicID, Msg: "in same conv", Type: messages.TypeChat,
			Channel: "web", ThreadID: "", ConversationID: topicConvID, CreatedAt: time.Now().UTC(),
		}))
		otherConvMsgID := uuid.NewString()
		require.NoError(t, s.CreateMessage(ctx, &store.Message{
			ID: otherConvMsgID, ProjectID: proj.ID, Sender: "user:dev", SenderID: DevUserID,
			Recipient: "thread:" + otherTopicID, Msg: "in other conv", Type: messages.TypeChat,
			Channel: "web", ThreadID: "", ConversationID: otherConvID, CreatedAt: time.Now().UTC(),
		}))

		check := srv.checkScheduledFire(ctx, &ScheduledChatMessage{
			SenderUserID: DevUserID, ConversationKey: topicID, ReplyToID: sameConvMsgID, FireAt: time.Now().UTC(),
		})
		assert.Empty(t, check.reason)
		assert.False(t, check.transient)
		assert.Equal(t, sameConvMsgID, check.replyToID, "same-conversation reply target without thread_id must be preserved")

		checkOther := srv.checkScheduledFire(ctx, &ScheduledChatMessage{
			SenderUserID: DevUserID, ConversationKey: topicID, ReplyToID: otherConvMsgID, FireAt: time.Now().UTC(),
		})
		assert.Empty(t, checkOther.replyToID, "cross-conversation reply target must be cleared")
	})

	t.Run("action inbox isRecipient derives chatKey from conversation_id", func(t *testing.T) {
		srv, s, _, proj, _ := setupSendTest(t)
		ctx := context.Background()

		peer := mustCreateActiveUser(t, s, "f1-inbox-peer")
		outsider := mustCreateActiveUser(t, s, "f1-inbox-outsider")
		dmKey, err := messages.DMConversationKey("user", DevUserID, "user", peer)
		require.NoError(t, err)
		dmConv, err := s.UpsertConversationByExternalRef(ctx, &store.Conversation{
			Kind: "direct", Surface: "native", ExternalRef: dmKey, DriftState: "active",
		})
		require.NoError(t, err)

		promptID := uuid.NewString()
		require.NoError(t, s.CreateMessage(ctx, &store.Message{
			ID: promptID, ProjectID: proj.ID, Sender: "user:dev", SenderID: DevUserID,
			Recipient: "user:peer", RecipientID: "", Msg: "decision prompt", Type: messages.TypeChat,
			Channel: "web", ThreadID: "", ConversationID: dmConv.ID, CreatedAt: time.Now().UTC(),
		}))

		peerUser, err := s.GetUser(ctx, peer)
		require.NoError(t, err)
		outsiderUser, err := s.GetUser(ctx, outsider)
		require.NoError(t, err)

		peerIdent := NewAuthenticatedUser(peerUser.ID, peerUser.Email, peerUser.DisplayName, peerUser.Role, string(ClientTypeWeb))
		outsiderIdent := NewAuthenticatedUser(outsiderUser.ID, outsiderUser.Email, outsiderUser.DisplayName, outsiderUser.Role, string(ClientTypeWeb))

		peerScope, err := srv.newActionInboxScope(ctx, peerIdent, scopeMemberProjects)
		require.NoError(t, err)
		outsiderScope, err := srv.newActionInboxScope(ctx, outsiderIdent, scopeMemberProjects)
		require.NoError(t, err)

		assert.True(t, peerScope.isRecipient(ctx, "", promptID, false),
			"DM participant must be recognized via conversation_id when thread_id is empty")
		assert.False(t, outsiderScope.isRecipient(ctx, "", promptID, false),
			"non-participant must be refused via conversation_id when thread_id is empty")
	})

	t.Run("chatKeyForConversationID resolves native topic and DM without thread_id", func(t *testing.T) {
		srv, s, wcs, proj, db := setupSendTest(t)
		ctx := context.Background()

		topicID := uuid.NewString()
		require.NoError(t, wcs.CreateTopic(ctx, WebChatTopic{
			ID: topicID, ProjectID: proj.ID, Name: "f1-topic", CreatedBy: DevUserID, CreatedAt: time.Now().UTC(),
		}))
		setTopicConversationID(t, db, s, topicID, proj.ID)
		topicConvID := topicConversationID(t, wcs, topicID)

		gotTopic, err := srv.chatKeyForConversationID(ctx, topicConvID)
		require.NoError(t, err)
		assert.Equal(t, topicID, gotTopic)

		peer := mustCreateActiveUser(t, s, "f1-key-peer")
		dmKey, err := messages.DMConversationKey("user", DevUserID, "user", peer)
		require.NoError(t, err)
		dmConv, err := s.UpsertConversationByExternalRef(ctx, &store.Conversation{
			Kind: "direct", Surface: "native", ExternalRef: dmKey, DriftState: "active",
		})
		require.NoError(t, err)

		gotDM, err := srv.chatKeyForConversationID(ctx, dmConv.ID)
		require.NoError(t, err)
		assert.Equal(t, dmKey, gotDM)
	})
}

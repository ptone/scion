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
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"io"
	"log/slog"

	"github.com/go-jose/go-jose/v4/jwt"
	"github.com/google/uuid"
	"github.com/knadh/koanf/v2"
	_ "github.com/mattn/go-sqlite3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/GoogleCloudPlatform/scion/pkg/api"
	"github.com/GoogleCloudPlatform/scion/pkg/messages"
	"github.com/GoogleCloudPlatform/scion/pkg/messaging"
	"github.com/GoogleCloudPlatform/scion/pkg/store"
)

// slogDiscard returns a *slog.Logger that discards all output.
func slogDiscard() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// ---------------------------------------------------------------------------
// Test fixtures
// ---------------------------------------------------------------------------

// readSwitchWorld sets up a minimal world for read-switch testing:
//
//   - project "rs-proj"
//   - agent "rs-agent" (owned by alice, running)
//   - user alice (manager), user bob (member with read-only)
//   - a DM conversation between alice and the agent
//   - two messages with conversation_id stamped (simulating dual-write)
//   - one message without conversation_id (simulating pre-dual-write legacy)
//
// Returns the server, store, users, agent, project, conversation ID, and
// the OperationalSettings + fakeHubSettingStore for flag manipulation.
func readSwitchWorld(t *testing.T) (
	srv *Server,
	s store.Store,
	alice *store.User,
	agent *store.Agent,
	project *store.Project,
	convID string,
	ops *OperationalSettings,
	fakeStore *fakeHubSettingStore,
) {
	t.Helper()

	srv, s = testServer(t)
	ctx := context.Background()

	// Wire up operational settings so we can control the flag.
	fakeStore = newFakeHubSettingStore()
	ops = NewOperationalSettings(fakeStore, emptyKoanf(), emptyKoanf())
	srv.SetOperationalSettings(ops)

	// --- users ---
	alice = &store.User{
		ID: tid("rs-alice"), Email: "alice@rs.test",
		DisplayName: "Alice", Role: store.UserRoleMember, Status: "active",
		Created: time.Now(),
	}
	require.NoError(t, s.CreateUser(ctx, alice))

	// --- project ---
	project = &store.Project{
		ID: tid("rs-project"), Name: "RS Project",
		Slug: "rs-proj", OwnerID: alice.ID, CreatedBy: alice.ID,
		Created: time.Now(), Updated: time.Now(),
	}
	require.NoError(t, s.CreateProject(ctx, project))
	srv.createProjectMembersGroupAndPolicy(ctx, project)

	// --- agent (owned by alice, running) ---
	agent = &store.Agent{
		ID: tid("rs-agent"), Slug: "rs-agent", Name: "RS Agent",
		ProjectID: project.ID, OwnerID: alice.ID, Visibility: store.VisibilityPrivate,
		Phase:   "running",
		Created: time.Now(), Updated: time.Now(),
	}
	require.NoError(t, s.CreateAgent(ctx, agent))

	// --- create a DM conversation (simulating dual-write) ---
	convResult := messaging.ResolveOrCreateDMConversation(ctx, s, srv.messageLog, "agent", agent.ID, "user", alice.ID)
	require.NotNil(t, convResult, "conversation resolution should succeed")
	convID = convResult.ConversationID

	// --- messages ---
	now := time.Now()

	// m1: alice → agent WITH conversation_id (dual-write era)
	require.NoError(t, s.CreateMessage(ctx, &store.Message{
		ID: uuid.NewString(), ProjectID: project.ID,
		Sender: "user:alice", SenderID: alice.ID,
		Recipient: "agent:rs-agent", RecipientID: agent.ID,
		Msg: "hello with conv", Type: "instruction",
		AgentID: agent.ID, Channel: "web",
		ConversationID: convID,
		CreatedAt:       now.Add(-2 * time.Minute),
	}))

	// m2: agent → alice WITH conversation_id (dual-write era)
	require.NoError(t, s.CreateMessage(ctx, &store.Message{
		ID: uuid.NewString(), ProjectID: project.ID,
		Sender: "agent:rs-agent", SenderID: agent.ID,
		Recipient: "user:alice", RecipientID: alice.ID,
		Msg: "reply with conv", Type: "state-change",
		AgentID: agent.ID, Channel: "web",
		ConversationID: convID,
		CreatedAt:       now.Add(-1 * time.Minute),
	}))

	// m3: alice → agent WITHOUT conversation_id (pre-dual-write legacy)
	require.NoError(t, s.CreateMessage(ctx, &store.Message{
		ID: uuid.NewString(), ProjectID: project.ID,
		Sender: "user:alice", SenderID: alice.ID,
		Recipient: "agent:rs-agent", RecipientID: agent.ID,
		Msg: "legacy message", Type: "instruction",
		AgentID: agent.ID, Channel: "web",
		CreatedAt: now.Add(-3 * time.Minute),
	}))

	return srv, s, alice, agent, project, convID, ops, fakeStore
}

// setReadSwitchFlag sets the ConversationReadSwitch flag via the operational
// settings store and refreshes the settings.
func setReadSwitchFlag(t *testing.T, fakeStore *fakeHubSettingStore, ops *OperationalSettings, enabled bool) {
	t.Helper()
	doc, err := json.Marshal(map[string]interface{}{
		"conversation_read_switch": enabled,
	})
	require.NoError(t, err)
	fakeStore.seed("messaging", json.RawMessage(doc))
	_, err = ops.Refresh(context.Background())
	require.NoError(t, err)
}

// clearReadSwitchFlag removes the messaging section entirely, simulating
// the flag being OFF (compiled default).
func clearReadSwitchFlag(t *testing.T, fakeStore *fakeHubSettingStore, ops *OperationalSettings) {
	t.Helper()
	// Remove the messaging section so ConversationReadSwitch() returns false.
	fakeStore.mu.Lock()
	delete(fakeStore.settings, "messaging")
	fakeStore.mu.Unlock()
	_, err := ops.Refresh(context.Background())
	require.NoError(t, err)
}

// ---------------------------------------------------------------------------
// Test 1: Flag OFF — reads use old path (backward compat)
// ---------------------------------------------------------------------------

func TestReadSwitch_FlagOFF_AgentMessages_UsesOldPath(t *testing.T) {
	srv, _, alice, agent, _, _, ops, fakeStore := readSwitchWorld(t)

	// Ensure flag is OFF.
	clearReadSwitchFlag(t, fakeStore, ops)
	require.False(t, ops.ConversationReadSwitch(), "flag should be OFF")

	// Query agent messages — should return all 3 messages (old path uses AgentID filter).
	rec := doMessageRequestAsUser(t, srv, alice, http.MethodGet,
		"/api/v1/agents/"+agent.ID+"/messages", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	var result store.ListResult[store.Message]
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&result))

	// With flag OFF, the old-path query (AgentID filter) returns all 3
	// messages regardless of conversation_id presence.
	assert.Equal(t, 3, result.TotalCount,
		"flag OFF: old path should return all messages for the agent")
}

func TestReadSwitch_FlagOFF_UserInbox_UsesOldPath(t *testing.T) {
	srv, _, alice, agent, _, _, ops, fakeStore := readSwitchWorld(t)

	// Ensure flag is OFF.
	clearReadSwitchFlag(t, fakeStore, ops)

	// Query user inbox with agent filter.
	rec := doMessageRequestAsUser(t, srv, alice, http.MethodGet,
		"/api/v1/messages?agent="+agent.ID, nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	var result store.ListResult[store.Message]
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&result))

	// Alice is the recipient of m2 (agent → alice). m1 and m3 have alice as
	// sender, not recipient — so only 1 message should be returned.
	assert.Equal(t, 1, result.TotalCount,
		"flag OFF: user inbox should return messages where user is recipient")
}

// ---------------------------------------------------------------------------
// Test 2: Flag ON — reads use ConversationID
// ---------------------------------------------------------------------------

func TestReadSwitch_FlagON_AgentMessages_UsesConversationID(t *testing.T) {
	srv, _, alice, agent, _, convID, ops, fakeStore := readSwitchWorld(t)

	// Turn flag ON.
	setReadSwitchFlag(t, fakeStore, ops, true)
	require.True(t, ops.ConversationReadSwitch(), "flag should be ON")

	// Query agent messages — with flag ON, the handler adds ConversationID
	// to the filter. Only messages with that conversation_id are returned.
	rec := doMessageRequestAsUser(t, srv, alice, http.MethodGet,
		"/api/v1/agents/"+agent.ID+"/messages", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	var result store.ListResult[store.Message]
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&result))

	// Only m1 and m2 have the conversation_id — m3 (legacy) does not.
	// When ConversationID filter is added to the query, only the two
	// dual-write era messages are returned.
	assert.Equal(t, 2, result.TotalCount,
		"flag ON: should return only messages with conversation_id")

	// Verify all returned messages have the correct conversation_id.
	for _, msg := range result.Items {
		assert.Equal(t, convID, msg.ConversationID,
			"flag ON: returned message should have matching conversation_id")
	}
}

func TestReadSwitch_FlagON_UserInbox_UsesConversationID(t *testing.T) {
	srv, _, alice, agent, _, convID, ops, fakeStore := readSwitchWorld(t)

	// Turn flag ON.
	setReadSwitchFlag(t, fakeStore, ops, true)

	// Query user inbox with agent filter.
	rec := doMessageRequestAsUser(t, srv, alice, http.MethodGet,
		"/api/v1/messages?agent="+agent.ID, nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	var result store.ListResult[store.Message]
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&result))

	// With flag ON and agent filter, the inbox handler resolves the DM
	// conversation and adds ConversationID to the filter. Alice is still
	// filtered as recipient. Only m2 has both RecipientID=alice AND
	// conversation_id set.
	assert.Equal(t, 1, result.TotalCount,
		"flag ON: user inbox should return messages with conversation_id where user is recipient")
	if len(result.Items) > 0 {
		assert.Equal(t, convID, result.Items[0].ConversationID,
			"flag ON: returned inbox message should have matching conversation_id")
	}
}

// ---------------------------------------------------------------------------
// Test 3: Hot-reload toggle — flag change takes effect without restart
// ---------------------------------------------------------------------------

func TestReadSwitch_HotReloadToggle(t *testing.T) {
	srv, _, alice, agent, _, _, ops, fakeStore := readSwitchWorld(t)

	// Step 1: Start with flag OFF — should get all 3 agent messages.
	clearReadSwitchFlag(t, fakeStore, ops)
	require.False(t, ops.ConversationReadSwitch())

	rec1 := doMessageRequestAsUser(t, srv, alice, http.MethodGet,
		"/api/v1/agents/"+agent.ID+"/messages", nil)
	require.Equal(t, http.StatusOK, rec1.Code)

	var result1 store.ListResult[store.Message]
	require.NoError(t, json.NewDecoder(rec1.Body).Decode(&result1))
	assert.Equal(t, 3, result1.TotalCount, "step 1 (OFF): should see all 3")

	// Step 2: Toggle ON — should get only 2 messages (those with conv_id).
	setReadSwitchFlag(t, fakeStore, ops, true)
	require.True(t, ops.ConversationReadSwitch())

	rec2 := doMessageRequestAsUser(t, srv, alice, http.MethodGet,
		"/api/v1/agents/"+agent.ID+"/messages", nil)
	require.Equal(t, http.StatusOK, rec2.Code)

	var result2 store.ListResult[store.Message]
	require.NoError(t, json.NewDecoder(rec2.Body).Decode(&result2))
	assert.Equal(t, 2, result2.TotalCount, "step 2 (ON): should see only 2 with conv_id")

	// Step 3: Toggle back OFF — should get all 3 again (no restart needed).
	setReadSwitchFlag(t, fakeStore, ops, false)
	require.False(t, ops.ConversationReadSwitch())

	rec3 := doMessageRequestAsUser(t, srv, alice, http.MethodGet,
		"/api/v1/agents/"+agent.ID+"/messages", nil)
	require.Equal(t, http.StatusOK, rec3.Code)

	var result3 store.ListResult[store.Message]
	require.NoError(t, json.NewDecoder(rec3.Body).Decode(&result3))
	assert.Equal(t, 3, result3.TotalCount, "step 3 (OFF again): should see all 3 again")
}

// ---------------------------------------------------------------------------
// Test 4: Conversation history (chat v2) read-switch
// ---------------------------------------------------------------------------

func TestReadSwitch_ConversationHistory_FlagOFF(t *testing.T) {
	srv, s, alice, agent, project, _, ops, fakeStore := readSwitchWorld(t)
	ctx := context.Background()

	// Set up WebChatStore.
	db, err := sql.Open("sqlite3", ":memory:")
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	wcs := NewWebChatStore(db, "sqlite3")
	require.NoError(t, wcs.Init())
	srv.SetWebChatStore(wcs)

	// Create a thread topic.
	topicID := uuid.NewString()
	err = wcs.CreateTopic(ctx, WebChatTopic{
		ID: topicID, ProjectID: project.ID, Name: "Test Thread",
		CreatedBy: alice.ID, CreatedAt: time.Now(), LastActivityAt: time.Now(),
	})
	require.NoError(t, err)

	// Create a thread conversation.
	convResult := messaging.ResolveOrCreateThreadConversation(ctx, s, srv.messageLog, topicID, project.ID)
	require.NotNil(t, convResult)

	// Create messages in the thread — some with conv_id, some without.
	now := time.Now()
	require.NoError(t, s.CreateMessage(ctx, &store.Message{
		ID: uuid.NewString(), ProjectID: project.ID,
		Sender: "user:alice", SenderID: alice.ID,
		Recipient: "agent:rs-agent", RecipientID: agent.ID,
		Msg: "thread msg with conv", Type: "instruction",
		AgentID: agent.ID, Channel: "web", ThreadID: topicID,
		ConversationID: convResult.ConversationID,
		CreatedAt:       now.Add(-1 * time.Minute),
	}))
	require.NoError(t, s.CreateMessage(ctx, &store.Message{
		ID: uuid.NewString(), ProjectID: project.ID,
		Sender: "user:alice", SenderID: alice.ID,
		Recipient: "agent:rs-agent", RecipientID: agent.ID,
		Msg: "thread msg legacy", Type: "instruction",
		AgentID: agent.ID, Channel: "web", ThreadID: topicID,
		CreatedAt: now.Add(-2 * time.Minute),
	}))

	// Flag OFF — query by channel+threadID.
	clearReadSwitchFlag(t, fakeStore, ops)

	rec := doMessageRequestAsUser(t, srv, alice, http.MethodGet,
		"/api/v1/chat/conversations/"+topicID+"/messages", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	var resp chatHistoryResponse
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))

	assert.Equal(t, 2, len(resp.Messages),
		"flag OFF: conversation history should return all thread messages")
}

func TestReadSwitch_ConversationHistory_FlagON(t *testing.T) {
	srv, s, alice, agent, project, _, ops, fakeStore := readSwitchWorld(t)
	ctx := context.Background()

	// Set up WebChatStore.
	db, err := sql.Open("sqlite3", ":memory:")
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	wcs := NewWebChatStore(db, "sqlite3")
	require.NoError(t, wcs.Init())
	srv.SetWebChatStore(wcs)

	// Create a thread topic.
	topicID := uuid.NewString()
	err = wcs.CreateTopic(ctx, WebChatTopic{
		ID: topicID, ProjectID: project.ID, Name: "Test Thread",
		CreatedBy: alice.ID, CreatedAt: time.Now(), LastActivityAt: time.Now(),
	})
	require.NoError(t, err)

	// Create a thread conversation.
	convResult := messaging.ResolveOrCreateThreadConversation(ctx, s, srv.messageLog, topicID, project.ID)
	require.NotNil(t, convResult)

	// Create messages in the thread — some with conv_id, some without.
	now := time.Now()
	require.NoError(t, s.CreateMessage(ctx, &store.Message{
		ID: uuid.NewString(), ProjectID: project.ID,
		Sender: "user:alice", SenderID: alice.ID,
		Recipient: "agent:rs-agent", RecipientID: agent.ID,
		Msg: "thread msg with conv", Type: "instruction",
		AgentID: agent.ID, Channel: "web", ThreadID: topicID,
		ConversationID: convResult.ConversationID,
		CreatedAt:       now.Add(-1 * time.Minute),
	}))
	require.NoError(t, s.CreateMessage(ctx, &store.Message{
		ID: uuid.NewString(), ProjectID: project.ID,
		Sender: "user:alice", SenderID: alice.ID,
		Recipient: "agent:rs-agent", RecipientID: agent.ID,
		Msg: "thread msg legacy", Type: "instruction",
		AgentID: agent.ID, Channel: "web", ThreadID: topicID,
		CreatedAt: now.Add(-2 * time.Minute),
	}))

	// Flag ON — should query by ConversationID.
	setReadSwitchFlag(t, fakeStore, ops, true)

	rec := doMessageRequestAsUser(t, srv, alice, http.MethodGet,
		"/api/v1/chat/conversations/"+topicID+"/messages", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	var resp chatHistoryResponse
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))

	// Only the message with conversation_id should be returned.
	assert.Equal(t, 1, len(resp.Messages),
		"flag ON: conversation history should return only messages with conversation_id")
	if len(resp.Messages) > 0 {
		assert.Equal(t, convResult.ConversationID, resp.Messages[0].ConversationID,
			"flag ON: returned message should have matching conversation_id")
	}
}

// ---------------------------------------------------------------------------
// Test 5: Broker inbound dual-write stamps conversation_id
// ---------------------------------------------------------------------------

func TestBrokerInbound_DualWrite_StampsConversationID(t *testing.T) {
	srv, s, alice, agent, project, _, _, _ := readSwitchWorld(t)
	ctx := context.Background()

	// Pre-create the DM conversation that the dual-write will resolve.
	convResult := messaging.ResolveOrCreateDMConversation(ctx, s, srv.messageLog, "user", alice.ID, "agent", agent.ID)
	require.NotNil(t, convResult)

	// Send a broker inbound message. This requires broker auth + HMAC,
	// which is complex to set up. Instead, we directly test the pattern by
	// creating a message the same way the handler does and checking the
	// conversation_id was stamped.
	//
	// (Integration testing of the full HTTP path would require setting up
	// broker HMAC auth, which is beyond the scope of a unit test for the
	// dual-write pattern.)

	// Simulate the dual-write pattern from handleBrokerInbound:
	storeMsg := &store.Message{
		ID:        uuid.NewString(),
		ProjectID: project.ID,
		Sender:    "user:alice@rs.test",
		SenderID:  alice.ID,
		Recipient: "agent:rs-agent",
		RecipientID: agent.ID,
		Msg:       "broker inbound test",
		Type:      "instruction",
		AgentID:   agent.ID,
		Channel:   "discord",
		CreatedAt: time.Now(),
	}

	// Apply the same dual-write pattern as the handler
	var cr *messaging.ConversationResult
	if storeMsg.ThreadID != "" {
		cr = messaging.ResolveOrCreateThreadConversation(ctx, s, srv.messageLog, storeMsg.ThreadID, agent.ProjectID)
	} else if storeMsg.SenderID != "" && agent.ID != "" {
		cr = messaging.ResolveOrCreateDMConversation(ctx, s, srv.messageLog, "user", storeMsg.SenderID, "agent", agent.ID)
	}
	if cr != nil {
		storeMsg.ConversationID = cr.ConversationID
	}

	require.NoError(t, s.CreateMessage(ctx, storeMsg))

	// Verify the message was stamped.
	msg, err := s.GetMessage(ctx, storeMsg.ID)
	require.NoError(t, err)
	assert.Equal(t, convResult.ConversationID, msg.ConversationID,
		"broker inbound message should be stamped with conversation_id")
}

// ---------------------------------------------------------------------------
// Test 6: Unparseable sender address must NOT create a conversation
// ---------------------------------------------------------------------------

func TestDualWrite_UnparseableSenderAddress_NoConversationCreated(t *testing.T) {
	srv, s, _, agent, _, _, _, _ := readSwitchWorld(t)
	ctx := context.Background()

	// Create a dedicated user for this test to avoid ambiguity with the
	// alice+agent DM that readSwitchWorld already creates.
	bob := &store.User{
		ID: uuid.NewString(), Email: "bob@rs.test",
		DisplayName: "Bob", Role: store.UserRoleMember, Status: "active",
		Created: time.Now(),
	}
	require.NoError(t, s.CreateUser(ctx, bob))

	// Build a MessageRequest with a StructuredMessage whose Sender has no
	// kind prefix — PrincipalKindFromAddress will return ("", false) inside
	// the handler, so no DM conversation should be created.
	msgReq := MessageRequest{
		StructuredMessage: &messages.StructuredMessage{
			Sender:    "bare-name-no-prefix", // no kind prefix
			SenderID:  bob.ID,
			Recipient: "agent:rs-agent",
			Msg:       "unparseable sender test",
			Type:      "instruction",
			Version:   messages.Version,
			Timestamp: time.Now().UTC().Format(time.RFC3339),
		},
	}
	body, err := json.Marshal(msgReq)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/agents/"+agent.ID+"/actions/message", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	// Set up user auth context so the handler passes phase checks.
	userIdent := NewAuthenticatedUser(bob.ID, bob.Email, bob.DisplayName, string(bob.Role), "api")
	req = req.WithContext(contextWithIdentity(req.Context(), userIdent))

	rr := httptest.NewRecorder()
	srv.handleAgentMessage(rr, req, agent.ID)
	// Response status is NOT checked — the handler returns 503 because no
	// dispatcher is configured, but the dual-write code at line 836 has
	// already executed by that point. We only care about the DB side effect.

	// Count conversation rows: no DM should exist for bob+agent.
	convs, err := s.ListConversations(ctx, store.ConversationFilter{Kind: "direct"}, store.ListOptions{})
	require.NoError(t, err)

	var matchCount int
	for _, conv := range convs.Items {
		if strings.Contains(conv.ExternalRef, bob.ID) && strings.Contains(conv.ExternalRef, agent.ID) {
			matchCount++
		}
	}
	assert.Equal(t, 0, matchCount,
		"unparseable sender address must NOT create a conversation row")

	// Floor assertion (Rule 14): readSwitchWorld creates an alice+agent DM,
	// so there must be at least 1 direct conversation. This proves the query
	// is hitting the right table.
	assert.GreaterOrEqual(t, len(convs.Items), 1,
		"floor: at least 1 direct conversation should exist (alice+agent DM from readSwitchWorld)")
}

// ---------------------------------------------------------------------------
// Test 7: AC-DEF15-4 — invalid dm: key as ThreadID creates zero conversations
// ---------------------------------------------------------------------------

func TestOutbound_InvalidDMKeyThreadID_NoConversationCreated(t *testing.T) {
	srv, s := testServer(t)
	ctx := context.Background()

	project := &store.Project{
		ID: uuid.NewString(), Name: "def15-project",
		Slug: "def15-project", Visibility: store.VisibilityPrivate,
	}
	require.NoError(t, s.CreateProject(ctx, project))

	recipient := &store.User{
		ID: uuid.NewString(), Email: "human@def15.test",
		DisplayName: "Human", Role: store.UserRoleMember, Status: "active",
		Created: time.Now(),
	}
	require.NoError(t, s.CreateUser(ctx, recipient))

	agent := &store.Agent{
		ID: uuid.NewString(), Name: "def15-agent", Slug: "def15-agent",
		ProjectID: project.ID, Phase: "running",
		Visibility: store.VisibilityPrivate,
	}
	require.NoError(t, s.CreateAgent(ctx, agent))

	// Count conversations before the request.
	convsBefore, err := s.ListConversations(ctx, store.ConversationFilter{}, store.ListOptions{})
	require.NoError(t, err)
	countBefore := len(convsBefore.Items)

	// Send an outbound message with an INVALID dm: key as ThreadID.
	// "bot" is not a valid principal kind and the UUID is malformed.
	body, _ := json.Marshal(OutboundMessageRequest{
		Recipient: "user:human@def15.test",
		Msg:       "bad dm key test",
		ThreadID:  "dm:bot:baduuid:user:" + uuid.NewString(),
		Channel:   "web",
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/agents/"+agent.ID+"/outbound-message", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(contextWithIdentity(req.Context(), &agentIdentityWrapper{&AgentTokenClaims{
		Claims:    jwt.Claims{Subject: agent.ID},
		ProjectID: project.ID,
	}}))

	rr := httptest.NewRecorder()
	srv.handleAgentOutboundMessage(rr, req, agent.ID)
	// The request may succeed (message delivered without conversation) or fail
	// validation, but either way no conversation row should be created.

	// Count conversations after the request.
	convsAfter, err := s.ListConversations(ctx, store.ConversationFilter{}, store.ListOptions{})
	require.NoError(t, err)
	countAfter := len(convsAfter.Items)

	assert.Equal(t, countBefore, countAfter,
		"AC-DEF15-4: invalid dm: key must not create any conversation rows")
}

// ---------------------------------------------------------------------------
// Test 8: AC-DEF16-1 — ValidateLegacyMessage rejects BEFORE conversation row
// ---------------------------------------------------------------------------

func TestDEF16_ValidationRejectsBeforeConversationCreated_Outbound(t *testing.T) {
	srv, s := testServer(t)
	ctx := context.Background()

	project := &store.Project{
		ID: uuid.NewString(), Name: "def16-project",
		Slug: "def16-project", Visibility: store.VisibilityPrivate,
	}
	require.NoError(t, s.CreateProject(ctx, project))

	recipient := &store.User{
		ID: uuid.NewString(), Email: "human@def16.test",
		DisplayName: "Human", Role: store.UserRoleMember, Status: "active",
		Created: time.Now(),
	}
	require.NoError(t, s.CreateUser(ctx, recipient))

	agent := &store.Agent{
		ID: uuid.NewString(), Name: "def16-agent", Slug: "def16-agent",
		ProjectID: project.ID, Phase: "running",
		Visibility: store.VisibilityPrivate,
	}
	require.NoError(t, s.CreateAgent(ctx, agent))

	// Count conversations before.
	convsBefore, err := s.ListConversations(ctx, store.ConversationFilter{}, store.ListOptions{})
	require.NoError(t, err)
	countBefore := len(convsBefore.Items)

	// Send a request that ValidateLegacyMessage rejects:
	// thread_id set but channel not set → "thread_id requires channel to be set".
	body, _ := json.Marshal(OutboundMessageRequest{
		Recipient: "user:human@def16.test",
		Msg:       "should be rejected",
		ThreadID:  "some-thread",
		// Channel intentionally omitted — triggers validation failure.
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/agents/"+agent.ID+"/outbound-message", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(contextWithIdentity(req.Context(), &agentIdentityWrapper{&AgentTokenClaims{
		Claims:    jwt.Claims{Subject: agent.ID},
		ProjectID: project.ID,
	}}))

	rr := httptest.NewRecorder()
	srv.handleAgentOutboundMessage(rr, req, agent.ID)

	// Must be rejected with 400.
	assert.Equal(t, http.StatusBadRequest, rr.Code,
		"AC-DEF16-1: thread_id without channel should be rejected")
	assert.Contains(t, rr.Body.String(), "thread_id requires channel",
		"AC-DEF16-1: error message should mention the validation rule")

	// Count conversations after — must be unchanged.
	convsAfter, err := s.ListConversations(ctx, store.ConversationFilter{}, store.ListOptions{})
	require.NoError(t, err)
	countAfter := len(convsAfter.Items)

	assert.Equal(t, countBefore, countAfter,
		"AC-DEF16-1: rejected request must not create any conversation rows")
}

func TestDEF16_ValidationRejectsBeforeConversationCreated_Inbound(t *testing.T) {
	srv, s, alice, agent, _, _, _, _ := readSwitchWorld(t)
	ctx := context.Background()

	// Count conversations before.
	convsBefore, err := s.ListConversations(ctx, store.ConversationFilter{}, store.ListOptions{})
	require.NoError(t, err)
	countBefore := len(convsBefore.Items)

	// Send a message that ValidateLegacyMessage rejects:
	// thread_id set but channel not set.
	msgReq := MessageRequest{
		StructuredMessage: &messages.StructuredMessage{
			Sender:    "user:" + alice.DisplayName,
			SenderID:  alice.ID,
			Recipient: "agent:" + agent.Slug,
			Msg:       "should be rejected",
			Type:      "instruction",
			ThreadID:  "some-thread",
			// Channel intentionally omitted — triggers validation failure.
			Version:   messages.Version,
			Timestamp: time.Now().UTC().Format(time.RFC3339),
		},
	}
	body, err := json.Marshal(msgReq)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/agents/"+agent.ID+"/actions/message", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	userIdent := NewAuthenticatedUser(alice.ID, alice.Email, alice.DisplayName, string(alice.Role), "api")
	req = req.WithContext(contextWithIdentity(req.Context(), userIdent))

	rr := httptest.NewRecorder()
	srv.handleAgentMessage(rr, req, agent.ID)

	// Must be rejected with 400.
	assert.Equal(t, http.StatusBadRequest, rr.Code,
		"AC-DEF16-1 inbound: thread_id without channel should be rejected")
	assert.Contains(t, rr.Body.String(), "thread_id requires channel",
		"AC-DEF16-1 inbound: error message should mention the validation rule")

	// Count conversations after — must be unchanged.
	convsAfter, err := s.ListConversations(ctx, store.ConversationFilter{}, store.ListOptions{})
	require.NoError(t, err)
	countAfter := len(convsAfter.Items)

	assert.Equal(t, countBefore, countAfter,
		"AC-DEF16-1 inbound: rejected request must not create any conversation rows")
}

// ---------------------------------------------------------------------------
// Test 9: Broker delegation — dm: ThreadID produces kind=direct
// ---------------------------------------------------------------------------

func TestBrokerDelegation_DMThreadID_ProducesDirectConversation(t *testing.T) {
	_, s := testServer(t)
	ctx := context.Background()

	agentID := uuid.NewString()
	userID := uuid.NewString()

	// Build a dm: key.
	dmKey, err := messages.DMConversationKey("agent", agentID, "user", userID)
	require.NoError(t, err)

	// Send the dm:-keyed ThreadID through ResolveOrCreateThreadConversation,
	// which is the delegation path that broker inbound and messagebroker.go use.
	convResult := messaging.ResolveOrCreateThreadConversation(ctx, s, slogDiscard(), dmKey, "some-project")
	require.NotNil(t, convResult, "dm: key through thread delegation should resolve")

	// Verify the conversation has kind=direct (not group).
	conv, err := s.GetConversation(ctx, convResult.ConversationID)
	require.NoError(t, err)
	assert.Equal(t, "direct", conv.Kind,
		"dm:-keyed ThreadID through broker delegation must produce kind=direct")
	assert.Equal(t, dmKey, conv.ExternalRef,
		"dm:-keyed ThreadID through broker delegation must preserve the dm: key as external_ref")
	assert.Nil(t, conv.ProjectID,
		"dm conversations are global — ProjectID must be nil")
}

// ---------------------------------------------------------------------------
// Test 10: AC-S215-COEXISTENCE — validDMKey and DeriveConversationKey guard
// different sinks
// ---------------------------------------------------------------------------

// TestValidDMKey_CoexistenceWithDeriveConversationKey proves that the two DM
// guards in the outbound message handler protect DIFFERENT sinks:
//
//   - validDMKey guards the message row: a malformed dm: key is rejected with
//     HTTP 400 and the message is never persisted.
//   - DeriveConversationKey guards the conversation row: a well-formed but
//     non-canonical dm: key passes validDMKey but fails DeriveConversationKey's
//     canonicality check. The message IS persisted (HTTP 200) but no
//     conversation row is created.
//
// Traceability: AC-S215-COEXISTENCE
func TestValidDMKey_CoexistenceWithDeriveConversationKey(t *testing.T) {
	srv, s := testServer(t)
	ctx := context.Background()

	// --- Setup: project, user, agent ---
	project := &store.Project{
		ID: uuid.NewString(), Name: "coexist-project",
		Slug: "coexist-project", Visibility: store.VisibilityPrivate,
	}
	require.NoError(t, s.CreateProject(ctx, project))

	user := &store.User{
		ID: uuid.NewString(), Email: "human@coexist.test",
		DisplayName: "Human", Role: store.UserRoleMember, Status: "active",
		Created: time.Now(),
	}
	require.NoError(t, s.CreateUser(ctx, user))

	agent := &store.Agent{
		ID: uuid.NewString(), Name: "coexist-agent", Slug: "coexist-agent",
		ProjectID: project.ID, Phase: "running",
		Visibility: store.VisibilityPrivate,
	}
	require.NoError(t, s.CreateAgent(ctx, agent))

	// -------------------------------------------------------------------
	// Sub-test 1: validDMKey rejects malformed dm: key → message NOT stored.
	// -------------------------------------------------------------------
	t.Run("malformed_dm_key_rejected_no_message_stored", func(t *testing.T) {
		// Count messages and conversations before the request.
		msgsBefore, err := s.ListMessages(ctx, store.MessageFilter{ProjectID: project.ID}, store.ListOptions{})
		require.NoError(t, err)
		convsBefore, err := s.ListConversations(ctx, store.ConversationFilter{}, store.ListOptions{})
		require.NoError(t, err)

		// "bot" is not "user" or "agent", so this fails the dmKeyRegexp.
		badKey := "dm:bot:00000000-0000-0000-0000-000000000001:user:" + user.ID

		body, _ := json.Marshal(OutboundMessageRequest{
			Recipient: "user:human@coexist.test",
			Msg:       "bad dm key test",
			ThreadID:  badKey,
			Channel:   "web",
		})
		req := httptest.NewRequest(http.MethodPost, "/api/v1/agents/"+agent.ID+"/outbound-message", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req = req.WithContext(contextWithIdentity(req.Context(), &agentIdentityWrapper{&AgentTokenClaims{
			Claims:    jwt.Claims{Subject: agent.ID},
			ProjectID: project.ID,
		}}))

		rr := httptest.NewRecorder()
		srv.handleAgentOutboundMessage(rr, req, agent.ID)

		assert.Equal(t, http.StatusBadRequest, rr.Code,
			"AC-S215-COEXISTENCE sub1: malformed dm: key must be rejected with 400")

		// Message count must not change.
		msgsAfter, err := s.ListMessages(ctx, store.MessageFilter{ProjectID: project.ID}, store.ListOptions{})
		require.NoError(t, err)
		assert.Equal(t, len(msgsBefore.Items), len(msgsAfter.Items),
			"AC-S215-COEXISTENCE sub1: validDMKey rejection must prevent message persistence")

		// Conversation count must not change.
		convsAfter, err := s.ListConversations(ctx, store.ConversationFilter{}, store.ListOptions{})
		require.NoError(t, err)
		assert.Equal(t, len(convsBefore.Items), len(convsAfter.Items),
			"AC-S215-COEXISTENCE sub1: validDMKey rejection must prevent conversation creation")
	})

	// -------------------------------------------------------------------
	// Sub-test 2: Key passes validDMKey but fails DeriveConversationKey
	// canonicality → message IS stored without conversation.
	//
	// dm:user:<UUID>:agent:<UUID> matches dmKeyRegexp (both kinds are
	// valid, both UUIDs are well-formed) but DeriveConversationKey
	// re-derives the canonical form as dm:agent:<UUID>:user:<UUID>
	// (sorted lexicographically) and rejects the mismatch.
	//
	// We simulate the outbound handler's dual-write pattern directly
	// (as TestBrokerInbound_DualWrite_StampsConversationID does) because
	// the full HTTP path requires a registered broker proxy with a "web"
	// channel, which is beyond the scope of this unit test.
	// -------------------------------------------------------------------
	t.Run("non_canonical_dm_key_message_stored_no_conversation", func(t *testing.T) {
		convsBefore, err := s.ListConversations(ctx, store.ConversationFilter{}, store.ListOptions{})
		require.NoError(t, err)
		countBefore := len(convsBefore.Items)

		// Non-canonical: user before agent. validDMKey passes this
		// (regex allows both "user" and "agent" in either position).
		nonCanonicalKey := "dm:user:" + user.ID + ":agent:" + agent.ID

		// Verify the key passes validDMKey (the regex guard).
		require.True(t, validDMKey(nonCanonicalKey),
			"precondition: non-canonical key must pass validDMKey regex")

		// Verify the key fails DeriveConversationKey (canonicality check).
		_, _, _, deriveErr := messaging.DeriveConversationKey(messaging.KeyInputs{
			ThreadID:      nonCanonicalKey,
			ProjectID:     project.ID,
			SenderKind:    "agent",
			SenderID:      agent.ID,
			RecipientKind: "user",
			RecipientID:   user.ID,
		})
		require.Error(t, deriveErr,
			"precondition: non-canonical key must fail DeriveConversationKey")
		assert.Contains(t, deriveErr.Error(), "not canonical",
			"precondition: DeriveConversationKey must reject with canonicality error")

		// Simulate the outbound handler's dual-write pattern:
		// 1. Build the message.
		storeMsg := &store.Message{
			ID:          uuid.NewString(),
			ProjectID:   project.ID,
			Sender:      "agent:" + agent.Slug,
			SenderID:    agent.ID,
			Recipient:   "user:" + user.DisplayName,
			RecipientID: user.ID,
			Msg:         "non-canonical dm key test",
			Type:        messages.TypeInputNeeded,
			AgentID:     agent.ID,
			Channel:     "web",
			ThreadID:    nonCanonicalKey,
			CreatedAt:   time.Now(),
		}

		// 2. Attempt conversation resolution (mirrors handler lines 269-288).
		extRef, kind, projID, keyErr := messaging.DeriveConversationKey(messaging.KeyInputs{
			ThreadID:      storeMsg.ThreadID,
			ProjectID:     project.ID,
			SenderKind:    "agent",
			SenderID:      agent.ID,
			RecipientKind: "user",
			RecipientID:   user.ID,
		})
		// Key derivation fails — handler logs warning and skips conversation.
		require.Error(t, keyErr, "DeriveConversationKey must reject non-canonical key")

		var convResult *messaging.ConversationResult
		if keyErr == nil {
			convResult = messaging.ResolveOrCreateConversationByKey(ctx, s, slogDiscard(), extRef, kind, projID)
		}
		if convResult != nil {
			storeMsg.ConversationID = convResult.ConversationID
		}

		// 3. Persist the message — this succeeds regardless of conversation failure.
		require.NoError(t, s.CreateMessage(ctx, storeMsg),
			"message persistence must succeed even when conversation resolution fails")

		// Assert: message IS stored with the non-canonical ThreadID.
		msg, err := s.GetMessage(ctx, storeMsg.ID)
		require.NoError(t, err)
		assert.Equal(t, nonCanonicalKey, msg.ThreadID,
			"AC-S215-COEXISTENCE sub2: message must carry the original non-canonical ThreadID")
		assert.Empty(t, msg.ConversationID,
			"AC-S215-COEXISTENCE sub2: no conversation_id should be stamped")

		// Assert: no new conversation rows created.
		convsAfter, err := s.ListConversations(ctx, store.ConversationFilter{}, store.ListOptions{})
		require.NoError(t, err)
		assert.Equal(t, countBefore, len(convsAfter.Items),
			"AC-S215-COEXISTENCE sub2: DeriveConversationKey failure must not create conversation rows")
	})
}

// ---------------------------------------------------------------------------
// Helper: emptyKoanf for tests that don't need file/env config.
// ---------------------------------------------------------------------------

// emptyKoanfForReadSwitch returns an empty koanf for tests.
// This duplicates the emptyKoanf() helper from operational_settings_test.go
// to avoid cross-file coupling; Go test files within the same package share
// a namespace so if emptyKoanf is already defined, this wrapper is a no-op.
func init() {
	// Ensure koanf import is used.
	_ = koanf.New(".")
}

// TestHandleGroupMessage_ThreadID_NotPropagated proves that even when a
// StructuredMessage arrives at handleGroupMessage with a ThreadID set, that
// ThreadID does NOT appear in persisted message rows and does NOT influence
// conversation key derivation.
//
// We call handleGroupMessage directly (same-package test access) to isolate
// the unit under test from upstream validation that rejects group[] recipients.
// This is the exact code path from handleAgentMessage line ~668:
//
//	if messages.IsGroupRecipient(structuredMsg.Recipient) {
//	    s.handleGroupMessage(w, r, id, structuredMsg, plainMessage, req.Interrupt)
//	}
//
// Traceability: AC-S215-M1
func TestHandleGroupMessage_ThreadID_NotPropagated(t *testing.T) {
	srv, s := testServer(t)
	ctx := context.Background()

	// --- Setup: project, user, two agents (anchor + target) ---

	project := &store.Project{
		ID:         api.NewUUID(),
		Name:       "groupmsg-threadid-project",
		Slug:       "groupmsg-threadid-project",
		Visibility: store.VisibilityPrivate,
	}
	require.NoError(t, s.CreateProject(ctx, project))

	userID := api.NewUUID()
	require.NoError(t, s.CreateUser(ctx, &store.User{
		ID:          userID,
		Email:       "test@example.com",
		DisplayName: "Test",
		Role:        store.UserRoleMember,
		Status:      "active",
		Created:     time.Now(),
	}))

	anchorAgent := &store.Agent{
		ID:         api.NewUUID(),
		Name:       "anchor-agent",
		Slug:       "anchor-agent",
		ProjectID:  project.ID,
		Phase:      "running",
		Visibility: store.VisibilityPrivate,
	}
	require.NoError(t, s.CreateAgent(ctx, anchorAgent))

	targetAgent := &store.Agent{
		ID:         api.NewUUID(),
		Name:       "target-agent",
		Slug:       "target-agent",
		ProjectID:  project.ID,
		Phase:      "running",
		Visibility: store.VisibilityPrivate,
	}
	require.NoError(t, s.CreateAgent(ctx, targetAgent))

	// --- Call handleGroupMessage directly with a ThreadID-bearing message ---
	// This simulates what handleAgentMessage does at line ~668 when it detects
	// a group[] recipient. The StructuredMessage carries a ThreadID that should
	// NOT propagate into persisted store.Message rows.

	injectedThreadID := "thread:proj:some-thread-id"

	structuredMsg := &messages.StructuredMessage{
		Version:   messages.Version,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Sender:    "user:Test",
		SenderID:  userID,
		Recipient: fmt.Sprintf("group[agent:%s,agent:%s]", targetAgent.Slug, anchorAgent.Slug),
		Msg:       "test group message with thread id",
		Type:      messages.TypeInstruction,
		Channel:   "web",
		ThreadID:  injectedThreadID,
	}

	req := httptest.NewRequest(http.MethodPost,
		"/api/v1/agents/"+anchorAgent.ID+"/actions/message",
		nil)
	req = req.WithContext(ctx)

	rr := httptest.NewRecorder()
	srv.handleGroupMessage(rr, req, anchorAgent.ID, structuredMsg, structuredMsg.Msg, false)

	// handleGroupMessage returns 200 with a GroupMessageResponse. Individual
	// recipients may show "failed" (no dispatcher/broker), but messages are
	// persisted before dispatch is attempted.
	require.Equal(t, http.StatusOK, rr.Code,
		"handleGroupMessage should return 200; got: %s", rr.Body.String())

	// --- Assert 1: No stored message has a non-empty ThreadID ---

	result, err := s.ListMessages(ctx,
		store.MessageFilter{ProjectID: project.ID},
		store.ListOptions{})
	require.NoError(t, err)
	require.NotEmpty(t, result.Items,
		"expected at least one persisted message")

	for _, msg := range result.Items {
		assert.Empty(t, msg.ThreadID,
			"AC-S215-M1: persisted message %s has ThreadID=%q, want empty: "+
				"handleGroupMessage must not propagate StructuredMessage.ThreadID "+
				"into stored messages", msg.ID, msg.ThreadID)
	}

	// --- Assert 2: ThreadID did not influence conversation key derivation ---
	// handleGroupMessage uses ResolveOrCreateDMConversation (principal-pair only),
	// NOT DeriveConversationKey. Verify no stored field echoes the injected ThreadID.

	for _, msg := range result.Items {
		assert.NotEqual(t, injectedThreadID, msg.GroupID,
			"AC-S215-M1: message %s GroupID must not be the injected ThreadID", msg.ID)
	}

	// Verify conversations (if any) don't reference the injected ThreadID.
	convs, err := s.ListConversations(ctx, store.ConversationFilter{}, store.ListOptions{})
	require.NoError(t, err)
	for _, conv := range convs.Items {
		assert.NotContains(t, conv.ExternalRef, injectedThreadID,
			"AC-S215-M1: conversation %s ExternalRef must not contain the injected ThreadID", conv.ID)
	}

	t.Logf("AC-S215-M1 PASS: %d message(s) persisted, none carry the "+
		"injected ThreadID %q", len(result.Items), injectedThreadID)
}

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
	"sync"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4/jwt"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/GoogleCloudPlatform/scion/pkg/api"
	"github.com/GoogleCloudPlatform/scion/pkg/config/opsettings"
	"github.com/GoogleCloudPlatform/scion/pkg/experiments"
	"github.com/GoogleCloudPlatform/scion/pkg/store"
)

type lifecycleEventRecorder struct {
	noopEventPublisher
	mu     sync.Mutex
	events []recordedDMLifecycle
}

type recordedDMLifecycle struct {
	dmKey          string
	action         string
	conversationID string
	subjects       []string
}

func (r *lifecycleEventRecorder) PublishChatDMLifecycleEvent(_ context.Context, dmKey, action, conversationID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var subjects []string
	for _, uid := range dmUserParticipants(dmKey) {
		subjects = append(subjects, "user."+uid+".chat.dm.lifecycle")
	}
	r.events = append(r.events, recordedDMLifecycle{
		dmKey:          dmKey,
		action:         action,
		conversationID: conversationID,
		subjects:       subjects,
	})
}

func (r *lifecycleEventRecorder) snapshot() []recordedDMLifecycle {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]recordedDMLifecycle, len(r.events))
	copy(out, r.events)
	return out
}

func enableConversationLifecycleExperiment(t *testing.T, srv *Server, enabled bool) {
	t.Helper()
	fake := newFakeHubSettingStore()
	fake.seed("experiments", json.RawMessage(fmt.Sprintf(`{"overrides":{%q:%t}}`, experiments.ConversationLifecycle, enabled)))
	ops := NewOperationalSettings(fake, emptyKoanf(), emptyKoanf())
	_, err := ops.Refresh(context.Background())
	require.NoError(t, err)
	srv.SetOperationalSettings(ops)
}

func setupLifecycleHub(t *testing.T) (*Server, store.Store, WebChatStore, *sql.DB, *store.User, *store.User, *store.Project, *store.Agent, *lifecycleEventRecorder) {
	t.Helper()
	ctx := context.Background()
	srv, s, alice, bob, project := setupDemoPolicyTest(t)

	type dbProvider interface {
		DB() *sql.DB
	}
	p, ok := s.(dbProvider)
	require.True(t, ok)
	wcs := NewWebChatStore(p.DB(), "sqlite")
	require.NoError(t, wcs.Init())
	srv.SetWebChatStore(wcs)

	rec := &lifecycleEventRecorder{}
	srv.events = rec

	agent := &store.Agent{
		ID:        api.NewUUID(),
		Slug:      "life-bot",
		Name:      "Life Bot",
		ProjectID: project.ID,
		OwnerID:   alice.ID,
	}
	require.NoError(t, s.CreateAgent(ctx, agent))

	return srv, s, wcs, p.DB(), alice, bob, project, agent, rec
}

func seedLifecycleDirectConversation(t *testing.T, ctx context.Context, s store.Store, wcs WebChatStore, surface, dmKey, p1Kind, p1ID, p2Kind, p2ID string) *store.Conversation {
	t.Helper()
	conv := &store.Conversation{
		ID:             api.NewUUID(),
		Kind:           "direct",
		Surface:        surface,
		ExternalRef:    dmKey,
		LastActivityAt: time.Now().UTC(),
	}
	require.NoError(t, s.CreateConversation(ctx, conv))
	require.NoError(t, s.AddParticipant(ctx, &store.ConversationParticipant{
		ConversationID: conv.ID,
		PrincipalKind:  p1Kind,
		PrincipalID:    p1ID,
		Role:           "member",
	}))
	require.NoError(t, s.AddParticipant(ctx, &store.ConversationParticipant{
		ConversationID: conv.ID,
		PrincipalKind:  p2Kind,
		PrincipalID:    p2ID,
		Role:           "member",
	}))
	if surface == "native" {
		require.NoError(t, wcs.UpsertDM(ctx, WebChatDM{
			ConversationKey: dmKey,
			ParticipantID:   p1ID,
			PeerID:          p2ID,
			PeerKind:        p2Kind,
			LastActivityAt:  time.Now().UTC(),
		}))
		require.NoError(t, wcs.UpsertDM(ctx, WebChatDM{
			ConversationKey: dmKey,
			ParticipantID:   p2ID,
			PeerID:          p1ID,
			PeerKind:        p1Kind,
			LastActivityAt:  time.Now().UTC(),
		}))
	}
	return conv
}

// TestConversationLifecycle_DeleteDM_AuthzMatrix covers design §9 criterion 7
// and is referenced by authzop catalog entry inbox.conversation.delete.
func TestConversationLifecycle_DeleteDM_AuthzMatrix(t *testing.T) {
	ctx := context.Background()
	srv, s, wcs, _, alice, bob, project, agent, _ := setupLifecycleHub(t)

	otherProject := &store.Project{ID: api.NewUUID(), Slug: "other-proj", Name: "Other Project"}
	require.NoError(t, s.CreateProject(ctx, otherProject))
	createTestUserWithProjectRole(t, s, alice.ID, alice.Email, otherProject.ID, store.ProjectRoleMember)

	dmKey := makeDMKey(agent.ID, alice.ID)
	conv := seedLifecycleDirectConversation(t, ctx, s, wcs, "native", dmKey, "user", alice.ID, "agent", agent.ID)

	// 1. Experiment OFF -> 404 even for participant.
	enableConversationLifecycleExperiment(t, srv, false)
	rec := doRequestAsUser(t, srv, alice, http.MethodDelete, "/api/v1/conversations/"+conv.ID, nil)
	assert.Equal(t, http.StatusNotFound, rec.Code, "experiment off must return 404")

	// Turn experiment ON for remaining checks.
	enableConversationLifecycleExperiment(t, srv, true)

	// 2. Agent principal -> 403 forbidden (D3: human-only).
	agentReq := httptest.NewRequest(http.MethodDelete, "/api/v1/conversations/"+conv.ID, nil)
	agentReq = agentReq.WithContext(contextWithIdentity(agentReq.Context(), newTestAgentIdentity(agent.ID, agent.ProjectID, nil)))
	rec = httptest.NewRecorder()
	srv.mux.ServeHTTP(rec, agentReq)
	assert.Equal(t, http.StatusForbidden, rec.Code, "agent principal must get 403")

	// 3. Non-participant user -> 404.
	rec = doRequestAsUser(t, srv, bob, http.MethodDelete, "/api/v1/conversations/"+conv.ID, nil)
	assert.Equal(t, http.StatusNotFound, rec.Code, "non-participant must get 404")

	// 4. Non-native surface -> 409 unsupported_surface.
	slackDM := seedLifecycleDirectConversation(t, ctx, s, wcs, "slack", makeDMKey(agent.ID, alice.ID), "user", alice.ID, "agent", agent.ID)
	rec = doRequestAsUser(t, srv, alice, http.MethodDelete, "/api/v1/conversations/"+slackDM.ID, nil)
	assert.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), "unsupported_surface")

	// 5. Group conversation -> 409 unsupported_conversation in Phase 1.
	groupConv := &store.Conversation{
		ID:          api.NewUUID(),
		ProjectID:   &project.ID,
		Kind:        "group",
		Surface:     "native",
		ExternalRef: "thread:life-group",
		DisplayName: "Life Group",
	}
	require.NoError(t, s.CreateConversation(ctx, groupConv))
	rec = doRequestAsUser(t, srv, alice, http.MethodDelete, "/api/v1/conversations/"+groupConv.ID, nil)
	assert.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())

	// 6. Scoped UAT checks (real bearer tokens through full HTTP middleware):
	doAsUAT := func(projID string, scopes []string, path string) *httptest.ResponseRecorder {
		tok := mintHubConfigToken(t, srv, alice.ID, projectBoundary(projID), scopes...)
		req := httptest.NewRequest(http.MethodDelete, path, nil)
		req.Header.Set("Authorization", "Bearer "+tok)
		w := httptest.NewRecorder()
		srv.Handler().ServeHTTP(w, req)
		return w
	}
	//    a) UAT with only inbox:read -> 403.
	rec = doAsUAT(project.ID, []string{"inbox:read", "agent:read"}, "/api/v1/conversations/"+conv.ID)
	assert.Equal(t, http.StatusForbidden, rec.Code, "UAT without inbox:write must get 403")

	//    b) UAT with inbox:write on wrong project -> 403.
	rec = doAsUAT(otherProject.ID, []string{"inbox:write", "agent:read"}, "/api/v1/conversations/"+conv.ID)
	assert.Equal(t, http.StatusForbidden, rec.Code, "UAT on wrong project must get 403")

	//    c) UAT with inbox:write + agent:read on peer agent's project -> 204.
	rec = doAsUAT(project.ID, []string{"inbox:write", "agent:read"}, "/api/v1/conversations/"+conv.ID)
	assert.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())

	// 7. Deleting the same DM again -> 404.
	rec = doRequestAsUser(t, srv, alice, http.MethodDelete, "/api/v1/conversations/"+conv.ID, nil)
	assert.Equal(t, http.StatusNotFound, rec.Code, "already-deleted DM must return 404")
}

// TestConversationLifecycle_DeleteDM_FreshConversationAndSideEffects covers
// design §9 criteria 1 and 2: soft-delete clears registry, read state, mentions,
// scheduled messages, stars, publishes chat.dm.lifecycle, leaves messages intact,
// and a subsequent message from either side mints a fresh conversation ID with
// only the new message and reset pin/mute.
func TestConversationLifecycle_DeleteDM_FreshConversationAndSideEffects(t *testing.T) {
	ctx := context.Background()
	srv, s, wcs, db, alice, bob, project, agent, recEvents := setupLifecycleHub(t)
	enableConversationLifecycleExperiment(t, srv, true)

	// Part A: User <-> Agent DM
	dmKey := makeDMKey(agent.ID, alice.ID)
	oldConv := seedLifecycleDirectConversation(t, ctx, s, wcs, "native", dmKey, "user", alice.ID, "agent", agent.ID)

	oldMsg := &store.Message{
		ID:             api.NewUUID(),
		ProjectID:      project.ID,
		Sender:         "user:" + alice.Email,
		SenderID:       alice.ID,
		Recipient:      "agent:" + agent.Slug,
		RecipientID:    agent.ID,
		Msg:            "hello before delete",
		Type:           "chat",
		Channel:        "web",
		ThreadID:       dmKey,
		ConversationID: oldConv.ID,
		DispatchState:  store.MessageDispatchDispatched,
	}
	require.NoError(t, s.CreateMessage(ctx, oldMsg))
	require.NoError(t, wcs.SetPinned(ctx, alice.ID, dmKey, true))
	require.NoError(t, wcs.SetMuted(ctx, alice.ID, dmKey, true))
	require.NoError(t, wcs.RecordMentions(ctx, dmKey, oldMsg.ID, []string{alice.ID}))

	// Seed a scheduled message and a star on the DM.
	sms := scheduledMessageStoreFrom(wcs)
	require.NotNil(t, sms)
	sm := &ScheduledChatMessage{
		ID:              api.NewUUID(),
		SenderUserID:    alice.ID,
		ConversationKey: dmKey,
		Content:         "future reminder",
		FireAt:          time.Now().UTC().Add(time.Hour),
		Status:          ScheduledMessagePending,
		CreatedAt:       time.Now().UTC(),
	}
	_, _, err := sms.CreateScheduledMessage(ctx, sm)
	require.NoError(t, err)

	stars := messageStarStoreFrom(wcs)
	require.NotNil(t, stars)
	_, _, err = stars.AddMessageStar(ctx, alice.ID, StarTarget{
		MessageID: oldMsg.ID,
		Selector:  StarSelector{ConversationKey: dmKey, Channel: "web", ThreadID: dmKey},
	})
	require.NoError(t, err)

	// Delete the DM.
	rec := doRequestAsUser(t, srv, alice, http.MethodDelete, "/api/v1/conversations/"+oldConv.ID, nil)
	require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())

	// Verify GET old conversation -> 404, DM list empty, messages row intact.
	rec = doRequestAsUser(t, srv, alice, http.MethodGet, "/api/v1/conversations/"+oldConv.ID, nil)
	assert.Equal(t, http.StatusNotFound, rec.Code)

	dms, err := wcs.ListDMs(ctx, alice.ID)
	require.NoError(t, err)
	assert.Empty(t, dms)

	var msgCount int
	require.NoError(t, db.QueryRowContext(ctx, `SELECT COUNT(*) FROM messages WHERE id = ?`, oldMsg.ID).Scan(&msgCount))
	assert.Equal(t, 1, msgCount, "messages row must remain intact for retention Phase B")

	// Scheduled message and star removed.
	gotSM, err := sms.GetScheduledMessage(ctx, alice.ID, sm.ID)
	require.NoError(t, err)
	assert.Nil(t, gotSM)
	gotStar, err := stars.GetMessageStar(ctx, alice.ID, oldMsg.ID)
	require.NoError(t, err)
	assert.Nil(t, gotStar)

	// chat.dm.lifecycle event published to user participant.
	evts := recEvents.snapshot()
	require.Len(t, evts, 1)
	assert.Equal(t, dmKey, evts[0].dmKey)
	assert.Equal(t, "deleted", evts[0].action)
	assert.Equal(t, oldConv.ID, evts[0].conversationID)
	assert.Equal(t, []string{"user." + alice.ID + ".chat.dm.lifecycle"}, evts[0].subjects)

	// Send a new message from user to agent through the real chat send route -> mints a fresh conversation ID!
	rec = doRequestAsUser(t, srv, alice, http.MethodPost, "/api/v1/chat/conversations/"+dmKey+"/messages", map[string]string{
		"content": "fresh hello after delete",
	})
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())

	newConv, err := s.GetConversationByExternalRef(ctx, "native", dmKey)
	require.NoError(t, err)
	assert.NotEqual(t, oldConv.ID, newConv.ID, "new message after delete must mint a fresh conversation ID")

	// History of the new conversation shows ONLY the new message.
	rec = doRequestAsUser(t, srv, alice, http.MethodGet, "/api/v1/conversations/"+newConv.ID+"/messages", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var msgList struct {
		Items []store.Message `json:"items"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &msgList))
	require.Len(t, msgList.Items, 1)
	assert.Equal(t, "fresh hello after delete", msgList.Items[0].Msg)

	// Both participants' DM lists show the DM again, and pin/mute are reset.
	dms, err = wcs.ListDMs(ctx, alice.ID)
	require.NoError(t, err)
	require.Len(t, dms, 1)
	agentDMs, err := wcs.ListDMs(ctx, agent.ID)
	require.NoError(t, err)
	require.Len(t, agentDMs, 1)

	rs, err := wcs.GetReadState(ctx, alice.ID, dmKey)
	require.NoError(t, err)
	if rs != nil {
		assert.False(t, rs.Pinned, "pinned state must be reset after delete")
		assert.False(t, rs.Muted, "muted state must be reset after delete")
	}

	// Part B: User <-> User DM publishes lifecycle event to BOTH user participants.
	u1, u2 := alice.ID, bob.ID
	if u1 > u2 {
		u1, u2 = u2, u1
	}
	uuKey := fmt.Sprintf("dm:user:%s:user:%s", u1, u2)
	uuConv := seedLifecycleDirectConversation(t, ctx, s, wcs, "native", uuKey, "user", alice.ID, "user", bob.ID)
	rec = doRequestAsUser(t, srv, alice, http.MethodDelete, "/api/v1/conversations/"+uuConv.ID, nil)
	require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())

	evts = recEvents.snapshot()
	require.Len(t, evts, 2)
	assert.ElementsMatch(t, []string{
		"user." + alice.ID + ".chat.dm.lifecycle",
		"user." + bob.ID + ".chat.dm.lifecycle",
	}, evts[1].subjects)
}

// TestConversationLifecycle_PromoteDeletedDM_Returns404 covers design §9 criterion 11.
func TestConversationLifecycle_PromoteDeletedDM_Returns404(t *testing.T) {
	ctx := context.Background()
	srv, s, wcs, _, alice, _, _, agent, _ := setupLifecycleHub(t)
	enableConversationLifecycleExperiment(t, srv, true)

	dmKey := makeDMKey(agent.ID, alice.ID)
	conv := seedLifecycleDirectConversation(t, ctx, s, wcs, "native", dmKey, "user", alice.ID, "agent", agent.ID)

	// Delete the DM.
	rec := doRequestAsUser(t, srv, alice, http.MethodDelete, "/api/v1/conversations/"+conv.ID, nil)
	require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())

	// Attempting to promote the deleted DM must return 404.
	rec = doRequestAsUser(t, srv, alice, http.MethodPost, "/api/v1/chat/conversations/"+dmKey+"/promote", map[string]string{
		"name": "Should Fail",
	})
	assert.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
}

// TestConversationLifecycle_ChatRetentionPhaseBPurgesDeletedDM covers design §9 criterion 10.
func TestConversationLifecycle_ChatRetentionPhaseBPurgesDeletedDM(t *testing.T) {
	ctx := context.Background()
	srv, s, alice, _, project := setupDemoPolicyTest(t)

	type retentionProvider interface {
		DB() *sql.DB
		ChatRetention() store.ChatRetentionStore
	}
	p, ok := s.(retentionProvider)
	require.True(t, ok)

	wcs := NewWebChatStore(p.DB(), "sqlite")
	require.NoError(t, wcs.Init())
	srv.SetWebChatStore(wcs)
	srv.SetChatRetentionStore(p.ChatRetention())

	// Enable both hub.conversation_lifecycle and hub.chat_retention (with enabled=true, grace=1 day).
	fake := newFakeHubSettingStore()
	fake.seed("experiments", json.RawMessage(fmt.Sprintf(`{"overrides":{%q:true,%q:true}}`,
		experiments.ConversationLifecycle, experiments.ChatRetention)))
	fake.seed(opsettings.ChatRetentionSectionName, json.RawMessage(`{"enabled":true,"deleted_conversation_grace_days":1,"sweep":{"chunk_size":50,"pause_ms":0}}`))
	ops := NewOperationalSettings(fake, emptyKoanf(), emptyKoanf())
	_, err := ops.Refresh(ctx)
	require.NoError(t, err)
	srv.SetOperationalSettings(ops)

	agent := &store.Agent{
		ID:        api.NewUUID(),
		Slug:      "ret-bot",
		Name:      "Ret Bot",
		ProjectID: project.ID,
	}
	require.NoError(t, s.CreateAgent(ctx, agent))

	dmKey := makeDMKey(agent.ID, alice.ID)
	conv := seedLifecycleDirectConversation(t, ctx, s, wcs, "native", dmKey, "user", alice.ID, "agent", agent.ID)

	msg := &store.Message{
		ID:             api.NewUUID(),
		ProjectID:      project.ID,
		Sender:         "user:" + alice.Email,
		SenderID:       alice.ID,
		Recipient:      "agent:" + agent.Slug,
		RecipientID:    agent.ID,
		Msg:            "message to be purged by phase B",
		Type:           "chat",
		Channel:        "web",
		ThreadID:       dmKey,
		ConversationID: conv.ID,
		DispatchState:  store.MessageDispatchDispatched,
	}
	require.NoError(t, s.CreateMessage(ctx, msg))

	// Soft-delete DM via HTTP route.
	rec := doRequestAsUser(t, srv, alice, http.MethodDelete, "/api/v1/conversations/"+conv.ID, nil)
	require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())

	// Backdate deleted_at past the 1-day grace period.
	oldDeleted := time.Now().UTC().Add(-5 * 24 * time.Hour).Truncate(time.Microsecond)
	_, err = p.DB().ExecContext(ctx, `UPDATE conversations SET deleted_at = ? WHERE id = ?`, oldDeleted, conv.ID)
	require.NoError(t, err)

	// Run Phase B retention sweep.
	sw := newChatRetentionSweeper(p.ChatRetention())
	res, err := sw.RunTick(ctx, srv.chatRetentionPolicyAt(time.Now()))
	require.NoError(t, err)
	assert.True(t, res.Ran, "retention tick must run")
	assert.EqualValues(t, 1, res.Purged.DeletedConv, "Phase B must purge the deleted DM's message")

	var remaining int
	require.NoError(t, p.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM messages WHERE id = ?`, msg.ID).Scan(&remaining))
	assert.Zero(t, remaining)
}

// TestConversationLifecycle_GetDirectConversationIncludesDMPeer verifies that
// single-conversation GET populates dmPeer with the peer agent's name so CLI
// prompts can name the peer without an extra list call.
func TestConversationLifecycle_GetDirectConversationIncludesDMPeer(t *testing.T) {
	ctx := context.Background()
	srv, s, wcs, _, alice, _, _, agent, _ := setupLifecycleHub(t)

	dmKey := makeDMKey(agent.ID, alice.ID)
	conv := seedLifecycleDirectConversation(t, ctx, s, wcs, "native", dmKey, "user", alice.ID, "agent", agent.ID)

	rec := doRequestAsUser(t, srv, alice, http.MethodGet, "/api/v1/conversations/"+conv.ID, nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	var got struct {
		ID     string            `json:"id"`
		DMPeer *conversationPeer `json:"dmPeer"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	assert.Equal(t, conv.ID, got.ID)
	require.NotNil(t, got.DMPeer, "GET /api/v1/conversations/{id} must populate dmPeer")
	assert.Equal(t, "agent", got.DMPeer.Kind)
	assert.Equal(t, agent.ID, got.DMPeer.ID)
	assert.Equal(t, agent.Name, got.DMPeer.Name)
}

// TestConversationLifecycle_DeleteDM_AgentMessageMintsFreshConversation covers
// design §9 criterion 2 ("from either side"): after a user deletes a DM, an
// agent-originated outbound message to that user through handleAgentOutboundMessage
// mints a fresh conversation ID, shows only the new message in history, and
// re-registers webchat_dm for the user.
func TestConversationLifecycle_DeleteDM_AgentMessageMintsFreshConversation(t *testing.T) {
	ctx := context.Background()
	srv, s, wcs, _, alice, _, project, agent, _ := setupLifecycleHub(t)
	enableConversationLifecycleExperiment(t, srv, true)

	dmKey := makeDMKey(agent.ID, alice.ID)
	oldConv := seedLifecycleDirectConversation(t, ctx, s, wcs, "native", dmKey, "user", alice.ID, "agent", agent.ID)

	oldMsg := &store.Message{
		ID:             api.NewUUID(),
		ProjectID:      project.ID,
		Sender:         "agent:" + agent.Slug,
		SenderID:       agent.ID,
		Recipient:      "user:" + alice.Email,
		RecipientID:    alice.ID,
		Msg:            "old agent message before delete",
		Type:           "chat",
		Channel:        "web",
		ThreadID:       dmKey,
		ConversationID: oldConv.ID,
		DispatchState:  store.MessageDispatchDispatched,
	}
	require.NoError(t, s.CreateMessage(ctx, oldMsg))

	// User deletes the DM.
	rec := doRequestAsUser(t, srv, alice, http.MethodDelete, "/api/v1/conversations/"+oldConv.ID, nil)
	require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())

	// Agent sends a fresh outbound DM to alice via the real handleAgentOutboundMessage route.
	body, err := json.Marshal(OutboundMessageRequest{
		Recipient:   "user:" + alice.Email,
		RecipientID: alice.ID,
		Msg:         "fresh agent message after delete",
		Type:        "instruction",
	})
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost,
		"/api/v1/projects/"+agent.ProjectID+"/agents/"+agent.ID+"/outbound-message",
		bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(contextWithIdentity(req.Context(), &agentIdentityWrapper{&AgentTokenClaims{
		Claims:    jwt.Claims{Subject: agent.ID},
		ProjectID: agent.ProjectID,
		Ancestry:  agent.Ancestry,
	}}))
	rr := httptest.NewRecorder()
	srv.handleAgentOutboundMessage(rr, req, agent.ID)
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())

	// A new conversation ID must be minted for the DM key.
	newConv, err := s.GetConversationByExternalRef(ctx, "native", dmKey)
	require.NoError(t, err)
	assert.NotEqual(t, oldConv.ID, newConv.ID, "agent message after delete must mint a fresh conversation ID")

	// History of the new conversation shows ONLY the new agent message.
	rec = doRequestAsUser(t, srv, alice, http.MethodGet, "/api/v1/conversations/"+newConv.ID+"/messages", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var msgList struct {
		Items []store.Message `json:"items"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &msgList))
	require.Len(t, msgList.Items, 1)
	assert.Equal(t, "fresh agent message after delete", msgList.Items[0].Msg)

	// User's DM list shows the DM again.
	dms, err := wcs.ListDMs(ctx, alice.ID)
	require.NoError(t, err)
	require.Len(t, dms, 1)
	assert.Equal(t, dmKey, dms[0].ConversationKey)
}

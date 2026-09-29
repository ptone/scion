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

// ---------------------------------------------------------------------------
// REPRO (agent-mention-fanout investigation, amf-inv) — NOT A FIX.
//
// Report: when an AGENT @mentions another agent in the body of a message it
// sends, the hub never detects the mention and the mentioned agent receives
// nothing. Human native-chat sends (handlers_chat_v2.go sendAgentRouted) do
// fan out via resolveRoutingAgents.
//
// Each TestAMFRepro_* test below sends an agent-authored message whose body
// contains "@amf-bystander" and asserts the bystander agent receives a
// dispatch. They FAIL on main (bb97a07) — that failure is the repro.
//
// TestAMFControl_ExplicitMentionsField_FansOut PASSES: it shows the
// server-side fan-out machinery (processMentions) works when the caller
// supplies MessageRequest.Mentions explicitly — nothing on the agent path
// ever populates that field from the body.
//
// Run: go test ./pkg/hub/ -run 'TestAMF' -count=1
// ---------------------------------------------------------------------------

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/GoogleCloudPlatform/scion/pkg/eventbus"
	"github.com/GoogleCloudPlatform/scion/pkg/messages"
	"github.com/GoogleCloudPlatform/scion/pkg/store"
	"github.com/go-jose/go-jose/v4/jwt"
	"github.com/stretchr/testify/require"
)

const amfBystanderSlug = "amf-bystander"

// amfSetup reuses paritySetup (two same-project agents + DM conversation,
// recording dispatcher, broker proxy) and adds a third same-project agent,
// the bystander, which is only ever @mentioned in message bodies.
func amfSetup(t *testing.T) (srv *Server, s store.Store, project *store.Project,
	sender, target, bystander *store.Agent, dmConvID string, dispatcher *recordingDispatcher) {
	t.Helper()
	srv, s, project, sender, target, dmConvID, dispatcher, _ = paritySetup(t)
	bystander = &store.Agent{
		ID:              tid("amf-bystander"),
		Name:            amfBystanderSlug,
		Slug:            amfBystanderSlug,
		ProjectID:       project.ID,
		Phase:           "running",
		RuntimeBrokerID: target.RuntimeBrokerID,
		MessageMode:     store.MessageModeProject,
		Ancestry:        sender.Ancestry,
	}
	require.NoError(t, s.CreateAgent(context.Background(), bystander))
	return
}

func dispatchesTo(d *recordingDispatcher, agentID string) []dispatchCall {
	var out []dispatchCall
	for _, c := range d.getCalls() {
		if c.Agent != nil && c.Agent.ID == agentID {
			out = append(out, c)
		}
	}
	return out
}

func agentCtx(ctx context.Context, a *store.Agent) context.Context {
	return contextWithIdentity(ctx, &agentIdentityWrapper{&AgentTokenClaims{
		Claims:    jwt.Claims{Subject: a.ID},
		ProjectID: a.ProjectID,
		Ancestry:  a.Ancestry,
	}})
}

// amfWireWebBroker swaps in a production-shaped broker proxy with a "web"
// spoke and the project's user-message subscription, so agent→user/topic
// sends take the deliveryUserBroker path end to end (handler →
// PublishUserMessage → MessageBrokerProxy.deliverToUser). The dispatcher is
// the same recordingDispatcher, so any mention fan-out would be observed.
func amfWireWebBroker(t *testing.T, srv *Server, s store.Store, projectID string, dispatcher *recordingDispatcher) {
	t.Helper()
	if srv.webChatStore == nil {
		dbProvider, ok := s.(interface{ DB() *sql.DB })
		require.True(t, ok)
		wcs := NewWebChatStore(dbProvider.DB(), "sqlite3")
		require.NoError(t, wcs.Init())
		srv.SetWebChatStore(wcs)
	}
	if old := srv.GetMessageBrokerProxy(); old != nil {
		old.Stop()
	}
	fanout := eventbus.NewFanOutEventBus([]eventbus.NamedEventBus{
		{Name: eventbus.InProcessBusName, Bus: eventbus.NewInProcessEventBus(slog.Default())},
		{Name: "web", Bus: nullSpokeEventBus{}},
	}, slog.Default())
	events := NewChannelEventPublisher()
	t.Cleanup(events.Close)
	proxy := NewMessageBrokerProxy(fanout, s, events,
		func() AgentDispatcher { return dispatcher }, slog.Default())
	proxy.Start()
	t.Cleanup(proxy.Stop)
	srv.SetMessageBrokerProxy(proxy)
	srv.mu.RLock()
	proxy.webChatStore = srv.webChatStore
	srv.mu.RUnlock()
	proxy.subscribeProjectUserMessages(projectID)
}

// Path 1: agent → agent via POST /agents/{target}/message (handleAgentMessage
// → ExecuteAgentDM). This is what agent-mode `scion message @target "..."`
// uses (cmd/message.go sendMessageViaConversation, RefAgent branch — which,
// unlike the bare-slug sendMessageViaHub path, does no client-side
// sendMentionMessages either).
func TestAMFRepro_AgentToAgent_Structured_BodyMentionNotFannedOut(t *testing.T) {
	srv, _, _, sender, target, bystander, _, dispatcher := amfSetup(t)

	rr := sendViaStructured(t, srv, sender, target, "hey @"+amfBystanderSlug+" can you take a look?")
	require.Equal(t, http.StatusOK, rr.Code, "body: %s", rr.Body.String())

	require.Len(t, dispatchesTo(dispatcher, target.ID), 1, "primary recipient must be dispatched")
	require.NotEmpty(t, dispatchesTo(dispatcher, bystander.ID),
		"REPRO: @%s in an agent-authored message body was not detected — bystander got no dispatch", amfBystanderSlug)
}

// Path 2: agent → agent via POST /agents/{sender}/outbound-message with
// conversation_ref=conv:<agent-agent DM> (handleAgentOutboundMessage →
// ExecuteAgentDM).
func TestAMFRepro_AgentToAgent_OutboundConvRef_BodyMentionNotFannedOut(t *testing.T) {
	srv, _, _, sender, target, bystander, dmConvID, dispatcher := amfSetup(t)

	rr := sendViaOutbound(t, srv, sender, dmConvID, "looping in @"+amfBystanderSlug+" here")
	require.Equal(t, http.StatusOK, rr.Code, "body: %s", rr.Body.String())

	require.Len(t, dispatchesTo(dispatcher, target.ID), 1, "primary recipient must be dispatched")
	require.NotEmpty(t, dispatchesTo(dispatcher, bystander.ID),
		"REPRO: @%s in an agent outbound DM body was not detected — bystander got no dispatch", amfBystanderSlug)
}

// Path 3: agent → native-chat topic (group conversation) via outbound-message
// with conversation_ref=conv:<group>. This is the `scion message conv:<topic>`
// / `#thread` path and the one most visible in web chat: a human sees the
// agent's "@other-agent ..." post in the topic, but other-agent never hears it.
func TestAMFRepro_AgentToTopic_OutboundConvRef_BodyMentionNotFannedOut(t *testing.T) {
	srv, s, project, sender, _, bystander, _, dispatcher := amfSetup(t)

	amfWireWebBroker(t, srv, s, project.ID, dispatcher)
	grantAgentProjectAccess(t, s, sender.ID, project.ID)

	createBytes, _ := json.Marshal(createConversationRequest{
		DisplayName: "amf-topic",
		ProjectID:   project.ID,
		Kind:        "group",
	})
	createReq := httptest.NewRequest(http.MethodPost, "/api/v1/conversations", bytes.NewReader(createBytes))
	createReq.Header.Set("Content-Type", "application/json")
	createReq = createReq.WithContext(agentContextWithScopes(sender.ID, project.ID, []AgentTokenScope{ScopeProjectRead}))
	createRR := httptest.NewRecorder()
	srv.handleCreateConversation(createRR, createReq)
	require.Equal(t, http.StatusCreated, createRR.Code, "body: %s", createRR.Body.String())
	var created conversationResponse
	require.NoError(t, json.Unmarshal(createRR.Body.Bytes(), &created))

	before := len(dispatcher.getCalls())
	sendRR := postConvRefNoRecipient(t, srv, project.ID, sender.ID,
		"@"+amfBystanderSlug+" please pick up the review", "conv:"+created.ID)
	require.Equal(t, http.StatusOK, sendRR.Code, "body: %s", sendRR.Body.String())

	// Sanity: the message itself was persisted into the topic.
	var sendResp map[string]interface{}
	require.NoError(t, json.Unmarshal(sendRR.Body.Bytes(), &sendResp))
	msgID, _ := sendResp["message_id"].(string)
	require.NotEmpty(t, msgID)

	require.NotEmpty(t, dispatchesTo(dispatcher, bystander.ID),
		"REPRO: agent post into topic %s with @%s was persisted but the mentioned agent got no dispatch (dispatches after send: %d)",
		created.ID, amfBystanderSlug, len(dispatcher.getCalls())-before)
}

// Path 4: agent → human user via outbound-message with recipient=user:<email>
// (`scion message user:<email> "..."`). The reply lands in the human's DM;
// an @agent in it is never forwarded.
func TestAMFRepro_AgentToUser_Outbound_BodyMentionNotFannedOut(t *testing.T) {
	srv, s, project, sender, _, bystander, _, dispatcher := amfSetup(t)

	amfWireWebBroker(t, srv, s, project.ID, dispatcher)
	owner, err := s.GetUser(context.Background(), project.OwnerID)
	require.NoError(t, err)

	body, _ := json.Marshal(OutboundMessageRequest{
		Recipient: "user:" + owner.Email,
		Msg:       "done — @" + amfBystanderSlug + " please deploy",
		Type:      "instruction",
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/agents/"+sender.ID+"/outbound-message", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(agentCtx(req.Context(), sender))
	rr := httptest.NewRecorder()
	srv.handleAgentOutboundMessage(rr, req, sender.ID)
	require.Equal(t, http.StatusOK, rr.Code, "body: %s", rr.Body.String())

	require.NotEmpty(t, dispatchesTo(dispatcher, bystander.ID),
		"REPRO: @%s in an agent→user message body was not detected — bystander got no dispatch", amfBystanderSlug)
}

// Control (PASSES on main): same as Path 1 but with the explicit
// MessageRequest.Mentions field populated. processMentions fans out a
// TypeMention to the bystander. Proves the infrastructure exists; the gap is
// that nothing derives Mentions from the body on agent sends (hubclient's
// SendStructuredMessage never sets the field — pkg/hubclient/agents.go:505).
func TestAMFControl_ExplicitMentionsField_FansOut(t *testing.T) {
	srv, _, _, sender, target, bystander, _, dispatcher := amfSetup(t)

	sm := &messages.StructuredMessage{
		Version:   messages.Version,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Type:      messages.TypeInstruction,
		Recipient: "agent:" + target.Slug,
		Msg:       "hey @" + amfBystanderSlug + " can you take a look?",
	}
	reqBody, _ := json.Marshal(MessageRequest{StructuredMessage: sm, Mentions: []string{amfBystanderSlug}})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/agents/"+target.ID+"/message", bytes.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(agentCtx(req.Context(), sender))
	rr := httptest.NewRecorder()
	srv.handleAgentMessage(rr, req, target.ID)
	require.Equal(t, http.StatusOK, rr.Code, "body: %s", rr.Body.String())

	got := dispatchesTo(dispatcher, bystander.ID)
	require.Len(t, got, 1, "explicit Mentions field must fan out to the bystander")
	require.NotNil(t, got[0].StructuredMessage)
	require.Equal(t, messages.TypeMention, got[0].StructuredMessage.Type)
	require.Equal(t, "agent:"+sender.Slug, got[0].StructuredMessage.Sender)
}

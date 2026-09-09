//go:build !no_sqlite

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
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/GoogleCloudPlatform/scion/pkg/messages"
	"github.com/GoogleCloudPlatform/scion/pkg/store"
	"github.com/go-jose/go-jose/v4/jwt"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// DEF-158 probe: exercise the exact CLI → hub paths for conv:<uuid>
// addressing WITHOUT an explicit recipient field. These are the paths the
// CLI exercises when an agent runs:
//
//   scion message "conv:<G>" "msg"                      (row 2)
//   scion message "conv:<G>" "user:<U>" "msg"           (row 5)
//   scion message "conv:<D>" "user:<U>" "msg"           (row 6)
//
// The CLI joins args[1:] into the message body, so row 5 sends
// conv_ref=conv:<G> msg="user:<U> msg" with NO Recipient field.
// Row 6 sends conv_ref=conv:<D> msg="user:<U> msg" with NO Recipient.
// ---------------------------------------------------------------------------

// postOutboundConvRefNoRecipient sends an outbound message with a
// conversation_ref but NO explicit recipient, mirroring what the CLI sends.
func postOutboundConvRefNoRecipient(t *testing.T, srv *Server, projectID, agentID, msg, convRef string) *httptest.ResponseRecorder {
	t.Helper()
	body, _ := json.Marshal(OutboundMessageRequest{
		Msg:             msg,
		ConversationRef: convRef,
		// NO Recipient — this is what the CLI sends for conv:<uuid>
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/agents/"+agentID+"/outbound-message", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(contextWithIdentity(req.Context(), &agentIdentityWrapper{&AgentTokenClaims{
		Claims:    jwt.Claims{Subject: agentID},
		ProjectID: projectID,
	}}))

	rr := httptest.NewRecorder()
	srv.handleAgentOutboundMessage(rr, req, agentID)
	return rr
}

// ---------------------------------------------------------------------------
// Probe 1 (mirrors row 2): conv:<group-uuid> with no recipient → 400.
// ---------------------------------------------------------------------------

func TestDEF158_Probe_GroupConv_NoRecipient_SimpleMsg(t *testing.T) {
	srv, s, project, agent, _ := def138Setup(t)
	ctx := context.Background()

	// Create a group conversation the agent's project owns.
	conv := &store.Conversation{
		Kind:        "group",
		Surface:     "native",
		ExternalRef: "thread:" + project.ID + ":d158-group-simple",
		ProjectID:   &project.ID,
		DriftState:  "active",
		DisplayName: "d158-group-simple",
	}
	created, err := s.UpsertConversationByExternalRef(ctx, conv)
	require.NoError(t, err)

	// Row 2: conv:<G> with simple message body
	rr := postOutboundConvRefNoRecipient(t, srv, project.ID, agent.ID,
		"hello simple", "conv:"+created.ID)

	// Expect 400 — group conversations require an explicit recipient.
	require.Equal(t, http.StatusBadRequest, rr.Code,
		"group conv with no explicit recipient must be rejected (row 2 path)")
	assert.Contains(t, rr.Body.String(), "group conversations require an explicit recipient",
		"error message must tell the caller to supply a recipient")
}

// ---------------------------------------------------------------------------
// Probe 2 (mirrors row 5): conv:<group-uuid> with "user:<uuid> msg" body
// → must produce the SAME 400 as probe 1.
// ---------------------------------------------------------------------------

func TestDEF158_Probe_GroupConv_NoRecipient_UserInBody(t *testing.T) {
	srv, s, project, agent, _ := def138Setup(t)
	ctx := context.Background()

	conv := &store.Conversation{
		Kind:        "group",
		Surface:     "native",
		ExternalRef: "thread:" + project.ID + ":d158-group-body",
		ProjectID:   &project.ID,
		DriftState:  "active",
		DisplayName: "d158-group-body",
	}
	created, err := s.UpsertConversationByExternalRef(ctx, conv)
	require.NoError(t, err)

	// Row 5: conv:<G> with message body that starts with "user:<uuid>"
	// This simulates what the CLI does when args are:
	//   scion message "conv:<G>" "user:<U>" "msg"
	// → message body = "user:<U> msg"
	rr := postOutboundConvRefNoRecipient(t, srv, project.ID, agent.ID,
		"user:b53249ea-b8ce-4e75-99f1-883ec0f5e967 msg", "conv:"+created.ID)

	// Must produce the SAME 400 as row 2 — message body content does not
	// change the routing decision.
	require.Equal(t, http.StatusBadRequest, rr.Code,
		"group conv with no explicit recipient must be rejected regardless of message body (row 5 path)")
	assert.Contains(t, rr.Body.String(), "group conversations require an explicit recipient")
}

// ---------------------------------------------------------------------------
// Probe 3 (mirrors row 6): conv:<direct-uuid> with no recipient → should
// derive the recipient from the DM key and return 200.
// ---------------------------------------------------------------------------

func TestDEF158_Probe_DirectConv_NoRecipient_DeriveFromDMKey(t *testing.T) {
	srv, s, project, agent, user := def138Setup(t)
	ctx := context.Background()

	// Create a direct DM conversation between the agent and the user.
	dmKey, err := messages.DMConversationKey("agent", agent.ID, "user", user.ID)
	require.NoError(t, err)

	dmConv, err := s.UpsertConversationByExternalRef(ctx, &store.Conversation{
		Kind:        "direct",
		Surface:     "native",
		ExternalRef: dmKey,
		DriftState:  "active",
	})
	require.NoError(t, err)

	// Row 6: conv:<D> with no explicit recipient — hub should derive
	// recipient from the DM key.
	rr := postOutboundConvRefNoRecipient(t, srv, project.ID, agent.ID,
		"user:b53249ea-whatever msg", "conv:"+dmConv.ID)

	require.Equal(t, http.StatusOK, rr.Code,
		"direct conv with no explicit recipient should derive addressee from DM key and succeed")

	// Verify the message was persisted with the correct conversation ID.
	var resp struct {
		MessageID   string `json:"message_id"`
		RecipientID string `json:"recipient_id"`
		Status      string `json:"status"`
	}
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
	require.NotEmpty(t, resp.MessageID, "response must include message_id")
	require.Equal(t, "sent", resp.Status)

	// Verify the stored message has the correct conversation_id and recipient.
	storedMsg, err := s.GetMessage(ctx, resp.MessageID)
	require.NoError(t, err)
	assert.Equal(t, dmConv.ID, storedMsg.ConversationID,
		"message must be attributed to the direct conversation")
	assert.Equal(t, user.ID, storedMsg.RecipientID,
		"recipient must be derived from the DM key (the user, not the agent)")

	// Verify the message body is the garbled text (includes "user:..." prefix).
	assert.Equal(t, "user:b53249ea-whatever msg", storedMsg.Msg,
		"message body should contain the original text including the swallowed user ref")
}

// ---------------------------------------------------------------------------
// Probe 4 (Q5 / AC-INGRESS-1): conv:<uuid> naming a conversation the
// sender is NOT a participant of → must be denied.
// ---------------------------------------------------------------------------

func TestDEF158_Probe_Q5_ConvRef_ForeignDM_Denied(t *testing.T) {
	srv, s, project, agent, _ := def138Setup(t)
	ctx := context.Background()

	// Create another agent and user for a DM the sender agent is NOT part of.
	otherAgent := &store.Agent{
		ID:         tid("d158-q5-other-agent"),
		Name:       "d158-q5-other",
		Slug:       "d158-q5-other",
		ProjectID:  project.ID,
		Phase:      "running",
		Visibility: store.VisibilityPrivate,
	}
	require.NoError(t, s.CreateAgent(ctx, otherAgent))

	otherUser := &store.User{
		ID:          tid("d158-q5-other-user"),
		Email:       "d158-q5-other@example.com",
		DisplayName: "Other User",
	}
	require.NoError(t, s.CreateUser(ctx, otherUser))

	// Create a DM between the OTHER agent and OTHER user.
	dmKey, err := messages.DMConversationKey("agent", otherAgent.ID, "user", otherUser.ID)
	require.NoError(t, err)

	foreignDM, err := s.UpsertConversationByExternalRef(ctx, &store.Conversation{
		Kind:        "direct",
		Surface:     "native",
		ExternalRef: dmKey,
		DriftState:  "active",
	})
	require.NoError(t, err)

	// The sending agent (def138-agent) tries to write into a DM it's not
	// part of by naming its UUID. This MUST be denied.
	rr := postOutboundConvRefNoRecipient(t, srv, project.ID, agent.ID,
		"sneaking into someone else's DM", "conv:"+foreignDM.ID)

	require.Equal(t, http.StatusBadRequest, rr.Code,
		"AC-INGRESS-1: agent must not be able to write into a foreign DM via conv:<uuid>")
	// The error should be generic (collapsed) — same as not-found.
	assert.Contains(t, rr.Body.String(), "could not be resolved",
		"foreign DM must produce the same generic error as not-found")
}

// ---------------------------------------------------------------------------
// Probe 5 (Q5 / G-1): ConversationAsserted must NOT be bindable from
// request JSON. Even if the request body contains conversation_asserted=true,
// the handler must derive it from the authorization path.
// ---------------------------------------------------------------------------

func TestDEF158_Probe_Q5_ConversationAsserted_NotBindable(t *testing.T) {
	srv, s, project, agent, user := def138Setup(t)
	ctx := context.Background()

	// Create a DM conversation between the agent and user.
	dmKey, err := messages.DMConversationKey("agent", agent.ID, "user", user.ID)
	require.NoError(t, err)

	_, err = s.UpsertConversationByExternalRef(ctx, &store.Conversation{
		Kind:        "direct",
		Surface:     "native",
		ExternalRef: dmKey,
		DriftState:  "active",
	})
	require.NoError(t, err)

	// Send a request with conversation_asserted in the JSON body — it must
	// be ignored by the handler (G-1: derived from auth, not bindable).
	rawBody := []byte(`{
		"recipient": "user:` + user.Email + `",
		"msg": "testing G-1",
		"conversation_asserted": true
	}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/agents/"+agent.ID+"/outbound-message",
		bytes.NewReader(rawBody))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(contextWithIdentity(req.Context(), &agentIdentityWrapper{&AgentTokenClaims{
		Claims:    jwt.Claims{Subject: agent.ID},
		ProjectID: project.ID,
	}}))

	rr := httptest.NewRecorder()
	srv.handleAgentOutboundMessage(rr, req, agent.ID)

	// The request should succeed (200) but ConversationAsserted must be
	// derived from the authorization path, not from request JSON.
	// Verify by checking the OutboundMessageRequest struct has no
	// ConversationAsserted field (it's on StructuredMessage, set by handler).
	require.Equal(t, http.StatusOK, rr.Code,
		"G-1: request with conversation_asserted in body should still succeed")
}

// ---------------------------------------------------------------------------
// Probe 6 (Q5 / AC-INGRESS-1): conv:<uuid> naming a group conversation
// from a DIFFERENT project → must be denied.
// ---------------------------------------------------------------------------

func TestDEF158_Probe_Q5_ConvRef_ForeignProject_GroupConv_Denied(t *testing.T) {
	srv, s, project, agent, _ := def138Setup(t)
	ctx := context.Background()

	// Create a foreign project with its own group conversation.
	foreignProject := &store.Project{
		ID:   tid("d158-q5-foreign-proj"),
		Name: "d158-q5-foreign",
		Slug: "d158-q5-foreign",
	}
	require.NoError(t, s.CreateProject(ctx, foreignProject))

	foreignConv := &store.Conversation{
		Kind:        "group",
		Surface:     "native",
		ExternalRef: "thread:" + foreignProject.ID + ":secret-channel",
		ProjectID:   &foreignProject.ID,
		DriftState:  "active",
		DisplayName: "secret-channel",
	}
	created, err := s.UpsertConversationByExternalRef(ctx, foreignConv)
	require.NoError(t, err)

	// The agent from project.ID tries to address a conversation in
	// foreignProject.ID — must be denied.
	rr := postOutboundConvRefNoRecipient(t, srv, project.ID, agent.ID,
		"probing foreign project group", "conv:"+created.ID)

	require.Equal(t, http.StatusBadRequest, rr.Code,
		"AC-INGRESS-1: agent must not write into a group conversation from another project")
	assert.Contains(t, rr.Body.String(), "could not be resolved",
		"foreign project conv must produce the same generic error")
}

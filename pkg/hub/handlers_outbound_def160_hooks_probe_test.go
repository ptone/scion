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
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-jose/go-jose/v4/jwt"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// DEF-160 Hooks Probe: assistant-reply with no recipient
//
// The hooks handler at pkg/sciontool/hooks/handlers/hub.go:154 sends an
// outbound message with {Msg, Type:"assistant-reply", Visibility:"verbose",
// Metadata} and NO Recipient, RecipientID, Channel, ThreadID, or
// ConversationRef. The function comment at handlers_agent_messaging.go:66
// says "The recipient defaults to the agent's creator when not explicitly
// specified" — but the code at :214 guards with:
//
//   if recipientID == "" && recipient == "" && req.ConversationRef == "" {
//       ValidationError(w, "recipient is required ...")
//       return
//   }
//
// This probe confirms whether the 400 actually fires.
// ---------------------------------------------------------------------------

func TestDEF160_HooksAssistantReply_NoRecipient(t *testing.T) {
	srv, _, _, agent, _ := def138Setup(t)

	// Reproduce the exact payload shape from hub.go:154-158.
	body, err := json.Marshal(OutboundMessageRequest{
		Msg:        "This is a test assistant reply.",
		Type:       "assistant-reply",
		Visibility: "verbose",
		Metadata:   map[string]string{"source": "hook"},
	})
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/agents/"+agent.ID+"/outbound-message", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(contextWithIdentity(req.Context(), &agentIdentityWrapper{&AgentTokenClaims{
		Claims:    jwt.Claims{Subject: agent.ID},
		ProjectID: agent.ProjectID,
	}}))

	rr := httptest.NewRecorder()
	srv.handleAgentOutboundMessage(rr, req, agent.ID)

	// The question: does this succeed (some hidden defaulting) or fail (400)?
	t.Logf("Status: %d", rr.Code)
	t.Logf("Body: %s", rr.Body.String())

	if rr.Code == http.StatusOK {
		t.Log("HOOKS PROBE: message accepted — some recipient defaulting exists that code-reading did not find")
	} else {
		assert.Equal(t, http.StatusBadRequest, rr.Code,
			"expected 400 (ValidationError) when recipient is missing")
		assert.Contains(t, rr.Body.String(), "recipient is required",
			"error should name the missing field")
		t.Logf("HOOKS PROBE CONFIRMED: assistant-reply with no recipient is refused with %d. "+
			"The hooks path at hub.go:154 hits this guard and the error is swallowed at :160. "+
			"Assistant replies do not appear in the Messages tab via this path.", rr.Code)
	}
}

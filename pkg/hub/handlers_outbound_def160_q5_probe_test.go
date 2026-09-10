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
	"net/http"
	"testing"

	"github.com/GoogleCloudPlatform/scion/pkg/messages"
	"github.com/GoogleCloudPlatform/scion/pkg/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// DEF-160 Q5 Probe: group conv-ref + non-participant recipient
//
// Proves that when a caller supplies an explicit recipient alongside a group
// conversation_id, the message persists even when the recipient is NOT a
// participant of the group conversation. This is a row-shape violation:
// the message asserts recipient_id = X against conversation G where X is
// not a participant of G.
// ---------------------------------------------------------------------------

func TestDEF160_Q5_GroupConvNonParticipantRecipient_Persists(t *testing.T) {
	srv, s, project, agent, _ := def138Setup(t)
	ctx := context.Background()

	// Create a non-participant user — this user exists but is not part of
	// any conversation participants.
	outsider := &store.User{
		ID:          tid("def160-outsider"),
		Email:       "outsider@example.com",
		DisplayName: "Outsider User",
	}
	require.NoError(t, s.CreateUser(ctx, outsider))

	// Create a group conversation in the agent's project.
	threadRef := "thread:" + project.ID + ":def160-topic"
	conv := &store.Conversation{
		Kind:        "group",
		Surface:     "native",
		ExternalRef: threadRef,
		ProjectID:   &project.ID,
		DriftState:  "active",
	}
	created, err := s.UpsertConversationByExternalRef(ctx, conv)
	require.NoError(t, err)

	// Count messages before.
	msgsBefore, err := s.ListMessages(ctx, store.MessageFilter{AgentID: agent.ID}, store.ListOptions{Limit: 100})
	require.NoError(t, err)
	countBefore := len(msgsBefore.Items)

	// Agent sends to group conversation with an explicit non-participant recipient.
	rr := postOutboundWithConv(t, srv, project.ID, agent.ID, outsider.Email, "msg to non-participant in group", created.ID)

	// Q5 probe: does this persist?
	if rr.Code == http.StatusOK {
		// Message was accepted — verify the row shape.
		msgsAfter, err := s.ListMessages(ctx, store.MessageFilter{AgentID: agent.ID}, store.ListOptions{Limit: 100})
		require.NoError(t, err)
		require.Greater(t, len(msgsAfter.Items), countBefore,
			"message row should be written")

		// Find the probe message and verify it carries the non-participant
		// recipient_id alongside the group conversation_id.
		var found bool
		for _, m := range msgsAfter.Items {
			if m.Msg == "msg to non-participant in group" {
				found = true
				assert.Equal(t, created.ID, m.ConversationID,
					"message should carry the group conversation_id")
				assert.Equal(t, outsider.ID, m.RecipientID,
					"message should carry the non-participant recipient_id")
				t.Logf("Q5 POSITIVE: message %s persisted with recipient_id=%s against group conversation=%s",
					m.ID, m.RecipientID, m.ConversationID)
				break
			}
		}
		require.True(t, found, "probe message not found in store")
	} else {
		// Message was refused — Q5 would be negative (the path is guarded).
		t.Logf("Q5 NEGATIVE: server refused with status %d: %s", rr.Code, rr.Body.String())
	}
}

// ---------------------------------------------------------------------------
// DEF-160 Q5 Probe: direct conv-ref + non-DM-key recipient
//
// Same shape as the group case but for direct conversations: conv:<direct-uuid>
// + an explicit recipient who is NOT named in the DM key. For direct
// conversations, the DM key IS the ACL, so a message naming a non-key
// recipient is a worse shape violation than the group case.
// ---------------------------------------------------------------------------

func TestDEF160_Q5_DirectConvNonKeyRecipient_Persists(t *testing.T) {
	srv, s, project, agent, _ := def138Setup(t)
	ctx := context.Background()

	// Create a user who IS in the DM key.
	dmPartner := &store.User{
		ID:          tid("def160-dm-partner"),
		Email:       "dmpartner@example.com",
		DisplayName: "DM Partner",
	}
	require.NoError(t, s.CreateUser(ctx, dmPartner))

	// Create a user who is NOT in the DM key.
	outsider := &store.User{
		ID:          tid("def160-direct-outsider"),
		Email:       "directoutsider@example.com",
		DisplayName: "Direct Outsider",
	}
	require.NoError(t, s.CreateUser(ctx, outsider))

	// Create a direct conversation between the agent and dmPartner.
	dmKey, err := messages.DMConversationKey("agent", agent.ID, "user", dmPartner.ID)
	require.NoError(t, err)
	conv := &store.Conversation{
		Kind:        "direct",
		Surface:     "native",
		ExternalRef: dmKey,
		DriftState:  "active",
	}
	created, err := s.UpsertConversationByExternalRef(ctx, conv)
	require.NoError(t, err)

	// Count messages before.
	msgsBefore, err := s.ListMessages(ctx, store.MessageFilter{AgentID: agent.ID}, store.ListOptions{Limit: 100})
	require.NoError(t, err)
	countBefore := len(msgsBefore.Items)

	// Agent sends to the direct conversation but names the OUTSIDER as recipient.
	// The DM key names agent + dmPartner. The outsider is not in the key.
	rr := postOutboundWithConv(t, srv, project.ID, agent.ID, outsider.Email, "msg to non-key-participant in direct", created.ID)

	// Q5 probe: does this persist?
	if rr.Code == http.StatusOK {
		msgsAfter, err := s.ListMessages(ctx, store.MessageFilter{AgentID: agent.ID}, store.ListOptions{Limit: 100})
		require.NoError(t, err)
		require.Greater(t, len(msgsAfter.Items), countBefore,
			"message row should be written")

		var found bool
		for _, m := range msgsAfter.Items {
			if m.Msg == "msg to non-key-participant in direct" {
				found = true
				assert.Equal(t, created.ID, m.ConversationID,
					"message should carry the direct conversation_id")
				assert.Equal(t, outsider.ID, m.RecipientID,
					"message should carry the non-key-participant recipient_id")
				t.Logf("Q5 DIRECT POSITIVE: message %s persisted with recipient_id=%s against direct conversation=%s (DM key does not name this recipient)",
					m.ID, m.RecipientID, m.ConversationID)
				break
			}
		}
		require.True(t, found, "probe message not found in store")
	} else {
		t.Logf("Q5 DIRECT NEGATIVE: server refused with status %d: %s", rr.Code, rr.Body.String())
	}
}

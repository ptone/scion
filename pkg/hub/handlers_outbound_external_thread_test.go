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
	"errors"
	"log/slog"
	"net/http"
	"testing"
	"time"

	"github.com/GoogleCloudPlatform/scion/pkg/api"
	"github.com/GoogleCloudPlatform/scion/pkg/messaging"
	"github.com/GoogleCloudPlatform/scion/pkg/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOutboundExternalThreadConversationState(t *testing.T) {
	projA := "11111111-1111-1111-1111-111111111111"
	projB := "22222222-2222-2222-2222-222222222222"
	zero := "00000000-0000-0000-0000-000000000000"
	empty := ""
	now := time.Now().UTC()
	boom := errors.New("db down")

	cases := []struct {
		name      string
		cr        messaging.ConversationReader
		projectID string
		want      outboundThreadState
		wantConv  bool
		wantErr   bool
	}{
		{
			name:      "not found",
			cr:        fakeConvReader{err: store.ErrNotFound},
			projectID: projA,
			want:      outboundThreadMissing,
		},
		{
			name:      "nil conversation",
			cr:        fakeConvReader{},
			projectID: projA,
			want:      outboundThreadMissing,
		},
		{
			name:      "lookup error",
			cr:        fakeConvReader{err: boom},
			projectID: projA,
			want:      outboundThreadMissing,
			wantErr:   true,
		},
		{
			name:      "soft deleted conversation",
			cr:        fakeConvReader{conv: &store.Conversation{ID: "c-del", ProjectID: &projA, DeletedAt: &now}},
			projectID: projA,
			want:      outboundThreadDeleted,
		},
		{
			name:      "foreign project conversation",
			cr:        fakeConvReader{conv: &store.Conversation{ID: "c-b", ProjectID: &projB}},
			projectID: projA,
			want:      outboundThreadMissing,
		},
		{
			name:      "nil project on conversation",
			cr:        fakeConvReader{conv: &store.Conversation{ID: "c-nil"}},
			projectID: projA,
			want:      outboundThreadMissing,
		},
		{
			name:      "empty project on conversation",
			cr:        fakeConvReader{conv: &store.Conversation{ID: "c-empty", ProjectID: &empty}},
			projectID: projA,
			want:      outboundThreadMissing,
		},
		{
			name:      "zero UUID project on conversation",
			cr:        fakeConvReader{conv: &store.Conversation{ID: "c-zero", ProjectID: &zero}},
			projectID: projA,
			want:      outboundThreadMissing,
		},
		{
			name:      "empty caller project",
			cr:        fakeConvReader{conv: &store.Conversation{ID: "c-a", ProjectID: &projA}},
			projectID: "",
			want:      outboundThreadMissing,
		},
		{
			name:      "matching active project conversation",
			cr:        fakeConvReader{conv: &store.Conversation{ID: "c-a", ProjectID: &projA, Surface: "slack", Kind: "group"}},
			projectID: projA,
			want:      outboundThreadExists,
			wantConv:  true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			conv, got, err := outboundExternalThreadConversationState(
				context.Background(), tc.cr, "slack", tc.projectID, "thread:"+tc.projectID+":C123:1.2",
			)
			if tc.wantErr {
				require.Error(t, err)
				assert.Nil(t, conv)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
			if tc.wantConv {
				require.NotNil(t, conv)
				assert.Equal(t, "c-a", conv.ID)
			} else {
				assert.Nil(t, conv)
			}
		})
	}
}

func TestOutbound_ExternalSurface_RawThreadID_NoConversation_Rejected422(t *testing.T) {
	surfaces := []struct {
		channel  string
		threadID string
	}{
		{channel: "slack", threadID: "C0123456:1700000000.000100"},
		{channel: "discord", threadID: "123456789012345678"},
		{channel: "teams", threadID: "19:abc@thread.tacv2;messageid=1700000000"},
		{channel: "telegram", threadID: "-100123456789:42"},
	}

	for _, tc := range surfaces {
		t.Run(tc.channel, func(t *testing.T) {
			srv, s, project, agent, user := def138Setup(t)
			ctx := context.Background()
			setupThreadTestChannels(t, srv, s, project, "web", tc.channel)

			before := countProjectConversations(t, s, project.ID)

			rr := postOutboundRequest(t, srv, project.ID, agent.ID, OutboundMessageRequest{
				Recipient: "user:" + user.Email,
				Msg:       "reply to unknown external thread",
				ThreadID:  tc.threadID,
				Channel:   tc.channel,
			})
			require.Equal(t, http.StatusUnprocessableEntity, rr.Code, "body: %s", rr.Body.String())

			apiErr := decodeErrorResponse(t, rr.Body.Bytes())
			assert.Equal(t, ErrCodeUnprocessable, apiErr.Code)
			assert.Equal(t,
				`thread_id "`+tc.threadID+`" does not match an existing conversation; `+
					`address the conversation with conv:<uuid> (see 'scion conversation list'), `+
					`or omit thread_id to message the recipient directly`,
				apiErr.Message,
			)

			// No conversation row may be minted on the external or native surface.
			extRef, err := messaging.ThreadConversationExternalRef(project.ID, tc.threadID)
			require.NoError(t, err)
			_, err = s.GetConversationByExternalRef(ctx, tc.channel, extRef)
			assert.ErrorIs(t, err, store.ErrNotFound, "no external conversation row may be created")
			_, err = s.GetConversationByExternalRef(ctx, "native", extRef)
			assert.ErrorIs(t, err, store.ErrNotFound, "no native conversation row may be created")
			assert.Equal(t, before, countProjectConversations(t, s, project.ID), "conversation count must not increase")

			// No message row may be persisted.
			assertOnlyControlMessage(t, srv, s, project, agent, user)
		})
	}
}

func TestOutbound_ExternalSurface_RawThreadID_InboundConversation_AcceptedAndForeignRejected(t *testing.T) {
	surfaces := []struct {
		channel  string
		threadID string
		otherID  string
	}{
		{channel: "slack", threadID: "C111:1700000000.000111", otherID: "C222:1700000000.000222"},
		{channel: "discord", threadID: "900000000000000001", otherID: "900000000000000002"},
		{channel: "teams", threadID: "19:projA@thread.tacv2;messageid=101", otherID: "19:projB@thread.tacv2;messageid=202"},
		{channel: "telegram", threadID: "-100111111111:11", otherID: "-100222222222:22"},
	}

	for _, tc := range surfaces {
		t.Run(tc.channel, func(t *testing.T) {
			srv, s, project, agent, user := def138Setup(t)
			ctx := context.Background()
			setupThreadTestChannels(t, srv, s, project, "web", tc.channel)

			// Create an inbound conversation in the sender's project via the P4
			// Phase 11 inbound resolution path.
			inboundConv, err := srv.resolveBrokerInboundConversation(
				ctx, slog.Default(), project.ID, tc.channel, tc.threadID, "", "Inbound Thread", agent.ID,
				tc.channel, "",
			)
			require.NoError(t, err)
			require.NotNil(t, inboundConv)
			before := countProjectConversations(t, s, project.ID)

			// Same-project outbound reply with raw thread_id must succeed and
			// route to the inbound-created conversation.
			const msgText = "reply to inbound external thread"
			rr := postOutboundRequest(t, srv, project.ID, agent.ID, OutboundMessageRequest{
				Recipient: "user:" + user.Email,
				Msg:       msgText,
				ThreadID:  tc.threadID,
				Channel:   tc.channel,
			})
			require.Equal(t, http.StatusOK, rr.Code, "body: %s", rr.Body.String())

			var body map[string]interface{}
			require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &body))
			assert.Equal(t, inboundConv.ConversationID, body["conversation_id"])
			assert.Equal(t, before, countProjectConversations(t, s, project.ID), "existing conversation must be reused without minting")

			stored := waitForSenderMessage(t, s, agent.ID, msgText)
			assert.Equal(t, inboundConv.ConversationID, stored.ConversationID)

			// Create another project and seed an inbound conversation there.
			otherProject := &store.Project{
				ID:   api.NewUUID(),
				Name: "Other Project " + tc.channel,
				Slug: "other-proj-" + tc.channel + "-" + api.NewUUID()[:8],
			}
			require.NoError(t, s.CreateProject(ctx, otherProject))
			foreignConv, err := srv.resolveBrokerInboundConversation(
				ctx, slog.Default(), otherProject.ID, tc.channel, tc.otherID, "", "Foreign Thread", "",
				tc.channel, "",
			)
			require.NoError(t, err)
			require.NotNil(t, foreignConv)

			// Sender in project A addressing project B's thread_id must get 422
			// and write no conversation row in project A.
			rrForeign := postOutboundRequest(t, srv, project.ID, agent.ID, OutboundMessageRequest{
				Recipient: "user:" + user.Email,
				Msg:       "reply to another project thread",
				ThreadID:  tc.otherID,
				Channel:   tc.channel,
			})
			require.Equal(t, http.StatusUnprocessableEntity, rrForeign.Code, "body: %s", rrForeign.Body.String())
			apiErr := decodeErrorResponse(t, rrForeign.Body.Bytes())
			assert.Equal(t, ErrCodeUnprocessable, apiErr.Code)
			assert.Equal(t, before, countProjectConversations(t, s, project.ID), "foreign thread rejection must not mint a row")
		})
	}
}

func TestOutbound_GroupConvRef_ThreadIDConsistency(t *testing.T) {
	t.Run("conv_ref with thread external_ref", func(t *testing.T) {
		srv, s, project, agent, _ := def138Setup(t)
		ctx := context.Background()
		setupThreadTestChannels(t, srv, s, project, "web", "slack")

		const threadKey = "C555:1700000000.000555"
		inboundConv, err := srv.resolveBrokerInboundConversation(
			ctx, slog.Default(), project.ID, "slack", threadKey, "", "Slack Thread", agent.ID,
			"slack", "",
		)
		require.NoError(t, err)

		// Mismatched thread_id => 400.
		rrMismatch := postOutboundRequest(t, srv, project.ID, agent.ID, OutboundMessageRequest{
			Msg:             "mismatched thread_id",
			ConversationRef: "conv:" + inboundConv.ConversationID,
			ThreadID:        "C999:1700000000.000999",
			Channel:         "slack",
		})
		require.Equal(t, http.StatusBadRequest, rrMismatch.Code, "body: %s", rrMismatch.Body.String())
		apiErr := decodeErrorResponse(t, rrMismatch.Body.Bytes())
		assert.Equal(t, ErrCodeInvalidRequest, apiErr.Code)
		assert.Equal(t, "thread_id does not match the conversation", apiErr.Message)

		msgs, err := s.ListMessages(ctx, store.MessageFilter{ConversationID: inboundConv.ConversationID}, store.ListOptions{Limit: 10})
		require.NoError(t, err)
		assert.Empty(t, msgs.Items, "rejected request must not persist a message")

		// Matching thread_id => 200.
		rrMatch := postOutboundRequest(t, srv, project.ID, agent.ID, OutboundMessageRequest{
			Msg:             "matching thread_id",
			ConversationRef: "conv:" + inboundConv.ConversationID,
			ThreadID:        threadKey,
			Channel:         "slack",
		})
		require.Equal(t, http.StatusOK, rrMatch.Code, "body: %s", rrMatch.Body.String())
		var matchBody map[string]interface{}
		require.NoError(t, json.Unmarshal(rrMatch.Body.Bytes(), &matchBody))
		assert.Equal(t, inboundConv.ConversationID, matchBody["conversation_id"])

		// Empty thread_id => 200 (backfilled to threadKey).
		rrEmpty := postOutboundRequest(t, srv, project.ID, agent.ID, OutboundMessageRequest{
			Msg:             "empty thread_id",
			ConversationRef: "conv:" + inboundConv.ConversationID,
			Channel:         "slack",
		})
		require.Equal(t, http.StatusOK, rrEmpty.Code, "body: %s", rrEmpty.Body.String())
		var emptyBody map[string]interface{}
		require.NoError(t, json.Unmarshal(rrEmpty.Body.Bytes(), &emptyBody))
		assert.Equal(t, inboundConv.ConversationID, emptyBody["conversation_id"])
	})

	t.Run("raw conversation_id with thread external_ref", func(t *testing.T) {
		srv, s, project, agent, user := def138Setup(t)
		ctx := context.Background()
		setupThreadTestChannels(t, srv, s, project, "web", "slack")

		const threadKey = "C777:1700000000.000777"
		inboundConv, err := srv.resolveBrokerInboundConversation(
			ctx, slog.Default(), project.ID, "slack", threadKey, "", "Slack Thread Raw", agent.ID,
			"slack", "",
		)
		require.NoError(t, err)

		// Mismatched thread_id with raw conversation_id => 400.
		rrMismatch := postOutboundRequest(t, srv, project.ID, agent.ID, OutboundMessageRequest{
			Recipient:      "user:" + user.Email,
			Msg:            "raw conv_id mismatched thread_id",
			ConversationID: inboundConv.ConversationID,
			ThreadID:       "C888:1700000000.000888",
			Channel:        "slack",
		})
		require.Equal(t, http.StatusBadRequest, rrMismatch.Code, "body: %s", rrMismatch.Body.String())
		apiErr := decodeErrorResponse(t, rrMismatch.Body.Bytes())
		assert.Equal(t, ErrCodeInvalidRequest, apiErr.Code)
		assert.Equal(t, "thread_id does not match the conversation", apiErr.Message)

		// Matching thread_id with raw conversation_id => 200.
		rrMatch := postOutboundRequest(t, srv, project.ID, agent.ID, OutboundMessageRequest{
			Recipient:      "user:" + user.Email,
			Msg:            "raw conv_id matching thread_id",
			ConversationID: inboundConv.ConversationID,
			ThreadID:       threadKey,
			Channel:        "slack",
		})
		require.Equal(t, http.StatusOK, rrMatch.Code, "body: %s", rrMatch.Body.String())
	})

	t.Run("native group conv_ref with empty external_ref rejects non-empty thread_id", func(t *testing.T) {
		srv, s, project, agent, _ := def138Setup(t)
		ctx := context.Background()
		setupThreadTestChannels(t, srv, s, project, "web")

		nativeGroup := &store.Conversation{
			ID:          api.NewUUID(),
			Kind:        "group",
			Surface:     "native",
			ProjectID:   &project.ID,
			DisplayName: "Native Group",
			DriftState:  "active",
		}
		require.NoError(t, s.CreateConversation(ctx, nativeGroup))

		rrMismatch := postOutboundRequest(t, srv, project.ID, agent.ID, OutboundMessageRequest{
			Msg:             "unexpected thread_id on native group",
			ConversationRef: "conv:" + nativeGroup.ID,
			ThreadID:        "C999:1700000000.000999",
			Channel:         "web",
		})
		require.Equal(t, http.StatusBadRequest, rrMismatch.Code, "body: %s", rrMismatch.Body.String())
		apiErr := decodeErrorResponse(t, rrMismatch.Body.Bytes())
		assert.Equal(t, ErrCodeInvalidRequest, apiErr.Code)
		assert.Equal(t, "thread_id does not match the conversation", apiErr.Message)

		rrEmpty := postOutboundRequest(t, srv, project.ID, agent.ID, OutboundMessageRequest{
			Msg:             "empty thread_id on native group",
			ConversationRef: "conv:" + nativeGroup.ID,
			Channel:         "web",
		})
		require.Equal(t, http.StatusOK, rrEmpty.Code, "body: %s", rrEmpty.Body.String())
	})
}

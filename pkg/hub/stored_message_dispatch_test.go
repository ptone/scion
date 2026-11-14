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

	"github.com/GoogleCloudPlatform/scion/pkg/messages"
	"github.com/GoogleCloudPlatform/scion/pkg/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func storedMessageDispatchRow() *store.Message {
	senderProject, recipientProject := "proj-s", "proj-r"
	return &store.Message{
		ID:                 "msg-1",
		ProjectID:          "proj-r",
		Sender:             "agent:sender",
		SenderID:           "sender-id",
		Recipient:          "agent:target",
		RecipientID:        "target-id",
		Msg:                "body",
		Type:               messages.TypeInstruction,
		Urgent:             true,
		Broadcasted:        true,
		AgentID:            "target-id",
		Channel:            "chan",
		ThreadID:           "thread-1",
		ConversationID:     "conv-1",
		CreatedAt:          time.Date(2026, 11, 14, 16, 30, 0, 0, time.FixedZone("x", 3600)),
		SenderProjectID:    &senderProject,
		RecipientProjectID: &recipientProject,
		AuthorizingUserID:  "user-1",
		DelegationGrantID:  "grant-1",
		DeferredAttachments: []store.DeferredAttachment{
			{Name: "a.txt", Size: 3},
		},
	}
}

func TestStoredMessageDispatch_RebuildsEnvelopeAndCarriesID(t *testing.T) {
	row := storedMessageDispatchRow()
	before := *row

	ctx, sm, err := storedMessageDispatch(context.Background(), row)
	require.NoError(t, err)

	assert.Equal(t, "msg-1", dispatchMessageIDFromContext(ctx))
	assert.Equal(t, &messages.StructuredMessage{
		Version:         messages.Version,
		Timestamp:       "2026-11-14T15:30:00Z",
		Sender:          "agent:sender",
		SenderID:        "sender-id",
		Recipient:       "agent:target",
		RecipientID:     "target-id",
		Msg:             "body",
		Type:            messages.TypeInstruction,
		Urgent:          true,
		Broadcasted:     true,
		Channel:         "chan",
		ThreadID:        "thread-1",
		ConversationID:  "conv-1",
		StoredMessageID: "msg-1",
	}, sm, "attachments, metadata and rendered text are not reconstructed")
	assert.Equal(t, before, *row, "the row is not modified")
}

func TestStoredMessageDispatch_UnrecoverableRow(t *testing.T) {
	cases := map[string]func(*store.Message){
		"no id":            func(m *store.Message) { m.ID = "" },
		"no sender":        func(m *store.Message) { m.Sender = "" },
		"no recipient":     func(m *store.Message) { m.Recipient = "" },
		"no body":          func(m *store.Message) { m.Msg = "" },
		"no creation time": func(m *store.Message) { m.CreatedAt = time.Time{} },
		"no type":          func(m *store.Message) { m.Type = "" },
		"unknown type":     func(m *store.Message) { m.Type = "free text" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			row := storedMessageDispatchRow()
			mutate(row)
			parent := context.Background()
			ctx, sm, err := storedMessageDispatch(parent, row)
			require.ErrorIs(t, err, errStoredMessageUnrecoverable)
			assert.Nil(t, sm)
			assert.Equal(t, parent, ctx, "no message id attached")
		})
	}

	t.Run("nil row", func(t *testing.T) {
		_, sm, err := storedMessageDispatch(context.Background(), nil)
		require.ErrorIs(t, err, errStoredMessageUnrecoverable)
		assert.Nil(t, sm)
	})
}

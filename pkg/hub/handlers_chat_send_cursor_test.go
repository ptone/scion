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
	"database/sql"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/GoogleCloudPlatform/scion/pkg/hub/eventfeed"
	"github.com/GoogleCloudPlatform/scion/pkg/store"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestHandleConversationSend_ReturnsAtomicCursor verifies that
// POST /api/v1/chat/conversations/{threadID}/messages returns an eventfeed
// cursor positioned at the sent message's own conversation_seq (both on 201
// create and on 200 idempotency replay), so callers can resume watching with
// no gap even if another message commits immediately after the send.
func TestHandleConversationSend_ReturnsAtomicCursor(t *testing.T) {
	srv, st := testServer(t)
	dbp, ok := st.(interface{ DB() *sql.DB })
	require.True(t, ok, "test store must expose DB()")
	wcs := NewWebChatStore(dbp.DB(), "sqlite")
	require.NoError(t, wcs.Init())
	srv.SetWebChatStore(wcs)
	ctx := context.Background()

	proj := &store.Project{
		ID:   uuid.NewString(),
		Name: "Chat Cursor Project",
		Slug: "chat-cursor-proj",
	}
	require.NoError(t, st.CreateProject(ctx, proj))

	// Create a native group conversation (which also creates its webchat topic).
	createRec := doRequest(t, srv, http.MethodPost, "/api/v1/conversations", map[string]any{
		"displayName": "cursor-group",
		"projectId":   proj.ID,
		"kind":        "group",
	})
	require.Equal(t, http.StatusCreated, createRec.Code, "body: %s", createRec.Body.String())
	var conv struct {
		ID          string `json:"id"`
		ExternalRef string `json:"externalRef"`
	}
	require.NoError(t, json.Unmarshal(createRec.Body.Bytes(), &conv))
	parts := strings.SplitN(conv.ExternalRef, ":", 3)
	require.Len(t, parts, 3)
	threadID := parts[2]

	// Send message #1 with an idempotency key.
	sendRec1 := doRequest(t, srv, http.MethodPost, "/api/v1/chat/conversations/"+threadID+"/messages", map[string]any{
		"content":         "first-message",
		"idempotency_key": "idem-cursor-1",
	})
	require.Equal(t, http.StatusCreated, sendRec1.Code, "body: %s", sendRec1.Body.String())
	var resp1 chatMessageResponse
	require.NoError(t, json.Unmarshal(sendRec1.Body.Bytes(), &resp1))
	require.NotEmpty(t, resp1.Cursor, "expected atomic eventfeed cursor on 201 chat send response")

	seq1, err := eventfeed.DecodeCursor(eventfeed.Cursor(resp1.Cursor), conv.ID)
	require.NoError(t, err)
	assert.Greater(t, seq1, int64(0))

	// Idempotency replay (200 OK) must also carry the same cursor.
	replayRec := doRequest(t, srv, http.MethodPost, "/api/v1/chat/conversations/"+threadID+"/messages", map[string]any{
		"content":         "first-message",
		"idempotency_key": "idem-cursor-1",
	})
	require.Equal(t, http.StatusOK, replayRec.Code, "body: %s", replayRec.Body.String())
	var replayResp chatMessageResponse
	require.NoError(t, json.Unmarshal(replayRec.Body.Bytes(), &replayResp))
	assert.Equal(t, resp1.Cursor, replayResp.Cursor)

	// Send message #2 and confirm watching from resp1.Cursor returns ONLY message #2.
	sendRec2 := doRequest(t, srv, http.MethodPost, "/api/v1/chat/conversations/"+threadID+"/messages", map[string]any{
		"content": "second-message",
	})
	require.Equal(t, http.StatusCreated, sendRec2.Code, "body: %s", sendRec2.Body.String())
	var resp2 chatMessageResponse
	require.NoError(t, json.Unmarshal(sendRec2.Body.Bytes(), &resp2))
	require.NotEmpty(t, resp2.Cursor)
	assert.NotEqual(t, resp1.Cursor, resp2.Cursor)

	watchRec := doRequest(t, srv, http.MethodGet, "/api/v1/conversations/"+conv.ID+"/watch?wait=0&filter=all&cursor="+resp1.Cursor, nil)
	require.Equal(t, http.StatusOK, watchRec.Code, "body: %s", watchRec.Body.String())
	var watchBody struct {
		Events []struct {
			Data struct {
				ID  string `json:"id"`
				Msg string `json:"msg"`
			} `json:"data"`
		} `json:"events"`
	}
	require.NoError(t, json.Unmarshal(watchRec.Body.Bytes(), &watchBody))
	require.Len(t, watchBody.Events, 1)
	assert.Equal(t, resp2.ID, watchBody.Events[0].Data.ID)
	assert.Equal(t, "second-message", watchBody.Events[0].Data.Msg)
}

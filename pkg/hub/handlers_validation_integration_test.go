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
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/GoogleCloudPlatform/scion/pkg/messages"
	"github.com/GoogleCloudPlatform/scion/pkg/store"
)

// ---------- AC-8: Validate() invoked on all three inbound paths ----------

// TestHandleAgentMessage_RejectsInvalidMessage proves that the hub handler
// path rejects messages that fail validation (AC-8, path 1).
func TestHandleAgentMessage_RejectsInvalidMessage(t *testing.T) {
	srv, s := testServer(t)
	ctx := context.Background()

	// Set up project + agent.
	project := &store.Project{
		ID:      tid("proj-val-hub"),
		Slug:    "val-hub-proj",
		Name:    "Validation Hub Test",
		Created: time.Now(),
		Updated: time.Now(),
	}
	require.NoError(t, s.CreateProject(ctx, project))

	agent := &store.Agent{
		ID:           tid("agent-val-hub"),
		Slug:         "val-agent",
		Name:         "Validation Agent",
		ProjectID:    project.ID,
		Phase:        "running",
		StateVersion: 1,
		Created:      time.Now(),
		Updated:      time.Now(),
	}
	require.NoError(t, s.CreateAgent(ctx, agent))

	// Send a message with thread_id but no channel (the Teams regression).
	msgReq := MessageRequest{
		StructuredMessage: &messages.StructuredMessage{
			Version:   messages.Version,
			Timestamp: time.Now().UTC().Format(time.RFC3339),
			Sender:    "user:alice",
			Recipient: "agent:" + agent.Slug,
			Msg:       "hello",
			Type:      messages.TypeInstruction,
			ThreadID:  "thread-123",
			Channel:   "", // violation: thread_id without channel
		},
	}

	// Use the project-scoped agent action route (same as production flow).
	rec := doRequest(t, srv, http.MethodPost,
		"/api/v1/projects/"+project.ID+"/agents/"+agent.Slug+"/message", msgReq)

	// Must be rejected with a 4xx (validation error).
	assert.True(t, rec.Code >= 400 && rec.Code < 500,
		"expected 4xx for invalid message, got %d: %s", rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), "thread_id requires channel",
		"response should mention the specific validation failure")
}

// TestHandleBrokerInbound_RejectsInvalidMessage proves that the broker
// inbound path rejects messages that fail validation (AC-8, path 2).
func TestHandleBrokerInbound_RejectsInvalidMessage(t *testing.T) {
	srv, s := testServer(t)
	ctx := context.Background()

	// Set up project + running agent.
	project := &store.Project{
		ID:      tid("proj-val-broker"),
		Slug:    "val-broker-proj",
		Name:    "Validation Broker Test",
		Created: time.Now(),
		Updated: time.Now(),
	}
	require.NoError(t, s.CreateProject(ctx, project))

	agent := &store.Agent{
		ID:           tid("agent-val-broker"),
		Slug:         "val-broker-agent",
		Name:         "Validation Broker Agent",
		ProjectID:    project.ID,
		Phase:        "running",
		StateVersion: 1,
		Created:      time.Now(),
		Updated:      time.Now(),
	}
	require.NoError(t, s.CreateAgent(ctx, agent))

	// Build inbound request with thread_id but no channel.
	// Use agent: sender to bypass user identity resolution (which
	// requires the user to exist in the store). The validation check
	// itself is sender-agnostic.
	topic := "scion.project." + project.ID + ".agent." + agent.Slug + ".messages"
	payload := inboundMessageRequest{
		Topic: topic,
		Message: &messages.StructuredMessage{
			Version:   messages.Version,
			Timestamp: time.Now().UTC().Format(time.RFC3339),
			Sender:    "agent:other-agent",
			Recipient: "agent:" + agent.Slug,
			Msg:       "hello from teams",
			Type:      messages.TypeInstruction,
			ThreadID:  "thread-456",
			Channel:   "", // Teams regression
		},
	}
	body, err := json.Marshal(payload)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/broker/inbound", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(contextWithBrokerIdentity(req.Context(), NewBrokerIdentity("test-broker")))

	rec := httptest.NewRecorder()
	srv.mux.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code,
		"broker inbound should reject invalid message with 400")
	assert.Contains(t, rec.Body.String(), "thread_id requires channel",
		"response should mention the specific validation failure")
}

// TestHandleBrokerInbound_AcceptsValidMessage ensures valid messages still
// pass through validation without being rejected.
func TestHandleBrokerInbound_AcceptsValidMessage(t *testing.T) {
	srv, s := testServer(t)
	ctx := context.Background()

	project := &store.Project{
		ID:      tid("proj-val-broker-ok"),
		Slug:    "val-broker-ok-proj",
		Name:    "Validation Broker OK Test",
		Created: time.Now(),
		Updated: time.Now(),
	}
	require.NoError(t, s.CreateProject(ctx, project))

	agent := &store.Agent{
		ID:           tid("agent-val-broker-ok"),
		Slug:         "val-broker-ok-agent",
		Name:         "Validation Broker OK Agent",
		ProjectID:    project.ID,
		Phase:        "running",
		StateVersion: 1,
		Created:      time.Now(),
		Updated:      time.Now(),
	}
	require.NoError(t, s.CreateAgent(ctx, agent))

	// Valid message — should not be rejected by validation.
	// It may fail at dispatch (no broker available), but the response
	// should NOT be a validation error.
	// Use agent: sender to bypass user identity resolution.
	topic := "scion.project." + project.ID + ".agent." + agent.Slug + ".messages"
	payload := inboundMessageRequest{
		Topic: topic,
		Message: &messages.StructuredMessage{
			Version:   messages.Version,
			Timestamp: time.Now().UTC().Format(time.RFC3339),
			Sender:    "agent:other-agent",
			Recipient: "agent:" + agent.Slug,
			Msg:       "hello from discord",
			Type:      messages.TypeInstruction,
			Channel:   "discord",
		},
	}
	body, err := json.Marshal(payload)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/broker/inbound", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(contextWithBrokerIdentity(req.Context(), NewBrokerIdentity("test-broker")))

	rec := httptest.NewRecorder()
	srv.mux.ServeHTTP(rec, req)

	// Should NOT be a validation error (400). The dispatch may fail (502 or 503)
	// because no broker/dispatcher is configured, but that's expected.
	assert.NotEqual(t, http.StatusBadRequest, rec.Code,
		"valid message should not be rejected by validation; got %d: %s",
		rec.Code, rec.Body.String())
}

// TestNativeChatPath_RejectsInvalidMessage proves that the native chat send
// path (handleConversationSend → sendAgentRouted) rejects messages that fail
// ValidateLegacyMessage (AC-8, path 3 — native web chat).
//
// Rule 10: this test MUST FAIL when the ValidateLegacyMessage call in
// sendAgentRouted is removed. It exercises a validation rule that is only
// enforced by ValidateLegacyMessage (empty body), not by the handler's own
// input checks. The handler allows empty content when attachments are
// present, but the StructuredMessage ends up with Msg="" which
// ValidateLegacyMessage rejects.
func TestNativeChatPath_RejectsInvalidMessage(t *testing.T) {
	srv, s := testServer(t)
	ctx := context.Background()

	// Set up the WebChatStore.
	db, err := sql.Open("sqlite3", ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	wcs := NewWebChatStore(db, "sqlite3")
	require.NoError(t, wcs.Init())
	srv.SetWebChatStore(wcs)

	// Set up project + agent.
	project := &store.Project{
		ID:      tid("proj-val-chat"),
		Slug:    "val-chat-proj",
		Name:    "Validation Chat Test",
		Created: time.Now(),
		Updated: time.Now(),
	}
	require.NoError(t, s.CreateProject(ctx, project))

	agent := &store.Agent{
		ID:        tid("agent-val-chat"),
		Slug:      "val-chat-agent",
		Name:      "Validation Chat Agent",
		ProjectID: project.ID,
		Phase:     "idle",
		OwnerID:   DevUserID,
		CreatedBy: DevUserID,
		Created:   time.Now(),
		Updated:   time.Now(),
	}
	require.NoError(t, s.CreateAgent(ctx, agent))

	// Create a topic with default_agent so the message routes through
	// sendAgentRouted.
	topicID := tid("topic-val-chat")
	require.NoError(t, wcs.CreateTopic(ctx, WebChatTopic{
		ID:           topicID,
		ProjectID:    project.ID,
		Name:         "validation-chat-thread",
		CreatedBy:    DevUserID,
		CreatedAt:    time.Now().UTC(),
		DefaultAgent: agent.ID,
	}))

	// Create an attachment so the handler allows empty content
	// (content="" is permitted when attachments are present).
	attachID := tid("attach-val-chat")
	require.NoError(t, wcs.CreateAttachment(ctx, AttachmentMeta{
		ID:         attachID,
		ProjectID:  project.ID,
		Filename:   "test.txt",
		MimeType:   "text/plain",
		Size:       42,
		UploadedBy: DevUserID,
		CreatedAt:  time.Now().UTC(),
	}))

	// Send a message with empty content but valid attachment. The handler
	// lets this through (attachments present), but sendAgentRouted builds
	// a StructuredMessage with Msg="" which ValidateLegacyMessage rejects.
	body := map[string]interface{}{
		"content":     "",
		"attachments": []string{attachID},
	}
	rec := doRequest(t, srv, http.MethodPost,
		"/api/v1/chat/conversations/"+topicID+"/messages", body)

	// Must be rejected by ValidateLegacyMessage (400 VALIDATION_ERROR).
	assert.Equal(t, http.StatusBadRequest, rec.Code,
		"expected 400 for empty body through validation choke point, got %d: %s",
		rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), "msg field is required",
		"response should mention the specific validation failure from ValidateLegacyMessage")
}

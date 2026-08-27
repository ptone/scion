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

package teams

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/GoogleCloudPlatform/scion/pkg/messages"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHubClient_DeliverInbound(t *testing.T) {
	var receivedBody inboundPayload
	var receivedHeaders http.Header

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "POST", r.Method)
		assert.Equal(t, "/api/v1/broker/inbound", r.URL.Path)
		assert.Equal(t, "application/json", r.Header.Get("Content-Type"))

		receivedHeaders = r.Header.Clone()

		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		err = json.Unmarshal(body, &receivedBody)
		require.NoError(t, err)

		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	hmacKey := base64.StdEncoding.EncodeToString([]byte("test-secret-key-1234"))
	client := NewHubClient(ts.URL, hmacKey, "teams-broker-1", slog.Default())
	client.httpClient = ts.Client()

	msg := &messages.StructuredMessage{
		Version:   messages.Version,
		Timestamp: "2026-01-01T00:00:00Z",
		Sender:    "Test User",
		SenderID:  "user-aad-id",
		Msg:       "Hello from Teams",
		Type:      "chat",
		Channel:   "my-project",
		Metadata: map[string]string{
			"teams_conversation_id": "conv-123",
		},
	}

	err := client.DeliverInbound(context.Background(), "teams.message", msg)
	require.NoError(t, err)

	assert.Equal(t, "teams.message", receivedBody.Topic)
	assert.Equal(t, "Hello from Teams", receivedBody.Message.Msg)
	assert.Equal(t, "Test User", receivedBody.Message.Sender)

	// Verify HMAC headers are present.
	assert.NotEmpty(t, receivedHeaders.Get("X-Scion-Broker-ID"))
	assert.Equal(t, "teams-broker-1", receivedHeaders.Get("X-Scion-Broker-ID"))
	assert.NotEmpty(t, receivedHeaders.Get("X-Scion-Signature"))
	assert.NotEmpty(t, receivedHeaders.Get("X-Scion-Timestamp"))
	assert.NotEmpty(t, receivedHeaders.Get("X-Scion-Nonce"))
}

func TestHubClient_DeliverInbound_HubError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("internal error"))
	}))
	defer ts.Close()

	hmacKey := base64.StdEncoding.EncodeToString([]byte("test-key"))
	client := NewHubClient(ts.URL, hmacKey, "broker-1", slog.Default())
	client.httpClient = ts.Client()

	msg := &messages.StructuredMessage{
		Version: messages.Version,
		Msg:     "test",
		Type:    "chat",
	}

	err := client.DeliverInbound(context.Background(), "test", msg)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "500")
}

func TestHubClient_DeliverInbound_Hub4xx(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error":"bad request"}`))
	}))
	defer ts.Close()

	hmacKey := base64.StdEncoding.EncodeToString([]byte("key"))
	client := NewHubClient(ts.URL, hmacKey, "broker", slog.Default())
	client.httpClient = ts.Client()

	msg := &messages.StructuredMessage{
		Version: messages.Version,
		Msg:     "test",
		Type:    "chat",
	}

	err := client.DeliverInbound(context.Background(), "test", msg)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "400")
}

func TestHubClient_DeliverCallback(t *testing.T) {
	var receivedData map[string]interface{}

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "POST", r.Method)
		assert.Equal(t, "/api/v1/broker/callback", r.URL.Path)

		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)

		var payload callbackPayload
		err = json.Unmarshal(body, &payload)
		require.NoError(t, err)
		receivedData = payload.Data

		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	hmacKey := base64.StdEncoding.EncodeToString([]byte("secret"))
	client := NewHubClient(ts.URL, hmacKey, "broker", slog.Default())
	client.httpClient = ts.Client()

	data := map[string]interface{}{
		"action": "submit",
		"value":  "test",
	}

	err := client.DeliverCallback(context.Background(), data)
	require.NoError(t, err)
	assert.Equal(t, "submit", receivedData["action"])
}

func TestHubClient_SignRequest_NoCredentials(t *testing.T) {
	// When no HMAC credentials are set, signing should be a no-op.
	client := NewHubClient("http://example.com", "", "", slog.Default())

	req, _ := http.NewRequest("GET", "http://example.com/test", nil)
	err := client.signRequest(req)
	assert.NoError(t, err)
	assert.Empty(t, req.Header.Get("X-Scion-Signature"))
}

// TestTeamsConvFields verifies conversation resolution field derivation
// from Teams messages (Phase 11).
func TestTeamsConvFields(t *testing.T) {
	t.Run("thread reply uses reply_to_id as external_ref", func(t *testing.T) {
		msg := &messages.StructuredMessage{
			Channel: "teams",
			Metadata: map[string]string{
				"teams_conversation_id": "19:abc@thread.v2",
				"teams_reply_to_id":     "1234567890",
			},
		}
		surface, extRef, parentRef := teamsConvFields(msg)
		assert.Equal(t, "teams", surface)
		assert.Equal(t, "1234567890", extRef)
		assert.Equal(t, "19:abc@thread.v2", parentRef)
	})

	t.Run("top-level message uses conversation_id as external_ref", func(t *testing.T) {
		msg := &messages.StructuredMessage{
			Channel: "teams",
			Metadata: map[string]string{
				"teams_conversation_id": "19:abc@thread.v2",
			},
		}
		surface, extRef, parentRef := teamsConvFields(msg)
		assert.Equal(t, "teams", surface)
		assert.Equal(t, "19:abc@thread.v2", extRef)
		assert.Equal(t, "19:abc@thread.v2", parentRef)
	})

	t.Run("non-teams channel returns empty fields", func(t *testing.T) {
		msg := &messages.StructuredMessage{
			Channel: "discord",
			Metadata: map[string]string{
				"teams_conversation_id": "19:abc@thread.v2",
			},
		}
		surface, extRef, parentRef := teamsConvFields(msg)
		assert.Empty(t, surface)
		assert.Empty(t, extRef)
		assert.Empty(t, parentRef)
	})

	t.Run("AC-8 regression: empty channel with thread_id not set as surface", func(t *testing.T) {
		msg := &messages.StructuredMessage{
			Channel:  "",
			ThreadID: "19:abc@thread.v2",
			Metadata: map[string]string{
				"teams_conversation_id": "19:abc@thread.v2",
			},
		}
		surface, extRef, parentRef := teamsConvFields(msg)
		// When Channel is not "teams", no conversation fields should be derived.
		// This guards against the AC-8 regression where channel="" with a
		// thread_id set could pass malformed data to the hub.
		assert.Empty(t, surface)
		assert.Empty(t, extRef)
		assert.Empty(t, parentRef)
	})

	t.Run("nil message returns empty fields", func(t *testing.T) {
		surface, extRef, parentRef := teamsConvFields(nil)
		assert.Empty(t, surface)
		assert.Empty(t, extRef)
		assert.Empty(t, parentRef)
	})

	t.Run("nil metadata returns empty fields", func(t *testing.T) {
		msg := &messages.StructuredMessage{Channel: "teams"}
		surface, extRef, parentRef := teamsConvFields(msg)
		assert.Empty(t, surface)
		assert.Empty(t, extRef)
		assert.Empty(t, parentRef)
	})
}

// TestDeliverInbound_ConversationFields verifies that the Teams plugin
// populates conversation resolution fields in the inbound payload.
func TestDeliverInbound_ConversationFields(t *testing.T) {
	t.Run("teams message includes conversation fields", func(t *testing.T) {
		var receivedPayload inboundPayload
		hub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			body, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(body, &receivedPayload)
			w.WriteHeader(http.StatusOK)
		}))
		defer hub.Close()

		hmacKey := base64.StdEncoding.EncodeToString([]byte("test-key"))
		client := NewHubClient(hub.URL, hmacKey, "teams-broker", slog.Default())
		client.httpClient = hub.Client()

		msg := &messages.StructuredMessage{
			Version:   messages.Version,
			Timestamp: "2026-01-01T00:00:00Z",
			Sender:    "Test User",
			Msg:       "hello from teams thread",
			Type:      messages.TypeInstruction,
			Channel:   "teams",
			ThreadID:  "1234567890",
			Metadata: map[string]string{
				"teams_conversation_id": "19:meeting_abc@thread.v2",
				"teams_reply_to_id":     "1234567890",
			},
		}

		err := client.DeliverInbound(context.Background(), "scion.project.p1.agent.coder.messages", msg)
		require.NoError(t, err)

		assert.Equal(t, "teams", receivedPayload.Surface)
		assert.Equal(t, "1234567890", receivedPayload.ExternalRef)
		assert.Equal(t, "19:meeting_abc@thread.v2", receivedPayload.ParentRef)
	})

	t.Run("AC-8 regression: empty channel produces no conversation fields", func(t *testing.T) {
		var receivedPayload inboundPayload
		hub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			body, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(body, &receivedPayload)
			w.WriteHeader(http.StatusOK)
		}))
		defer hub.Close()

		hmacKey := base64.StdEncoding.EncodeToString([]byte("test-key"))
		client := NewHubClient(hub.URL, hmacKey, "teams-broker", slog.Default())
		client.httpClient = hub.Client()

		msg := &messages.StructuredMessage{
			Version:  messages.Version,
			Sender:   "Test User",
			Msg:      "hello",
			Type:     messages.TypeInstruction,
			Channel:  "", // AC-8: empty channel
			ThreadID: "19:abc@thread.v2",
			Metadata: map[string]string{
				"teams_conversation_id": "19:abc@thread.v2",
			},
		}

		err := client.DeliverInbound(context.Background(), "test.topic", msg)
		require.NoError(t, err)

		// With channel="" (not "teams"), no conversation fields should be set.
		assert.Empty(t, receivedPayload.Surface)
		assert.Empty(t, receivedPayload.ExternalRef)
		assert.Empty(t, receivedPayload.ParentRef)
	})
}

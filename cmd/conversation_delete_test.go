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

package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/GoogleCloudPlatform/scion/pkg/hubclient"
	"github.com/GoogleCloudPlatform/scion/pkg/store"
	"github.com/google/uuid"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newConversationDeleteTestHub(t *testing.T, convID, dmPeer string, deleteStatus int) (*httptest.Server, *[]string) {
	t.Helper()
	var deletedIDs []string
	peerID := uuid.NewString()
	userID := uuid.NewString()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/conversations/"+convID:
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(hubclient.ConversationDetail{
				Conversation: store.Conversation{
					ID:          convID,
					Kind:        "direct",
					Surface:     "native",
					ExternalRef: "dm:agent:" + peerID + ":user:" + userID,
				},
				Participants: []hubclient.ConversationParticipantDetail{
					{ConversationParticipant: store.ConversationParticipant{ConversationID: convID, PrincipalKind: "user", PrincipalID: userID, Role: "member"}},
					{ConversationParticipant: store.ConversationParticipant{ConversationID: convID, PrincipalKind: "agent", PrincipalID: peerID, Role: "member"}},
				},
				DMPeer: &hubclient.ConversationPeer{
					Kind: "agent",
					ID:   peerID,
					Name: dmPeer,
				},
			})
		case r.Method == http.MethodDelete && r.URL.Path == "/api/v1/conversations/"+convID:
			if deleteStatus == http.StatusNoContent {
				deletedIDs = append(deletedIDs, convID)
				w.WriteHeader(http.StatusNoContent)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(deleteStatus)
			_, _ = w.Write([]byte(`{"error":{"code":"not_found","message":"Conversation not found"}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &deletedIDs
}

func TestConversationDelete_NonTTYRequiresYes(t *testing.T) {
	convID := uuid.NewString()
	srv, deletedIDs := newConversationDeleteTestHub(t, convID, "my-agent", http.StatusNoContent)
	client, err := hubclient.New(srv.URL)
	require.NoError(t, err)

	origTTY := conversationDeleteIsTerminal
	conversationDeleteIsTerminal = func() bool { return false }
	t.Cleanup(func() { conversationDeleteIsTerminal = origTTY })

	cmd := &cobra.Command{}
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)

	err = executeConversationDelete(context.Background(), cmd, client, "", []string{"conv:" + convID}, false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--yes")
	assert.Empty(t, *deletedIDs, "non-TTY without --yes must not delete")
}

func TestConversationDelete_TTYPromptCancelAndConfirm(t *testing.T) {
	convID := uuid.NewString()
	srv, deletedIDs := newConversationDeleteTestHub(t, convID, "my-agent", http.StatusNoContent)
	client, err := hubclient.New(srv.URL)
	require.NoError(t, err)

	origTTY := conversationDeleteIsTerminal
	conversationDeleteIsTerminal = func() bool { return true }
	t.Cleanup(func() { conversationDeleteIsTerminal = origTTY })

	// 1. Decline ("n") -> cancels without deleting.
	cmd := &cobra.Command{}
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetIn(strings.NewReader("n\n"))

	require.NoError(t, executeConversationDelete(context.Background(), cmd, client, "", []string{convID}, false))
	assert.Contains(t, errOut.String(), "DM with my-agent")
	assert.Contains(t, errOut.String(), "for both participants")
	assert.Contains(t, errOut.String(), "Cancelled.")
	assert.Empty(t, *deletedIDs)

	// 2. Confirm ("y") -> deletes.
	out.Reset()
	errOut.Reset()
	cmd.SetIn(strings.NewReader("y\n"))

	require.NoError(t, executeConversationDelete(context.Background(), cmd, client, "", []string{convID}, false))
	assert.Equal(t, []string{convID}, *deletedIDs)
	assert.Contains(t, out.String(), "Deleted conversation "+convID)
}

func TestConversationDelete_YesFlagAndJSON(t *testing.T) {
	convID := uuid.NewString()
	srv, deletedIDs := newConversationDeleteTestHub(t, convID, "my-agent", http.StatusNoContent)
	client, err := hubclient.New(srv.URL)
	require.NoError(t, err)

	origTTY := conversationDeleteIsTerminal
	conversationDeleteIsTerminal = func() bool { return false }
	t.Cleanup(func() { conversationDeleteIsTerminal = origTTY })

	origFmt := outputFormat
	outputFormat = "json"
	t.Cleanup(func() { outputFormat = origFmt })

	cmd := &cobra.Command{}
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)

	require.NoError(t, executeConversationDelete(context.Background(), cmd, client, "", []string{"conv:" + convID}, true))
	assert.Equal(t, []string{convID}, *deletedIDs)

	var res conversationDeleteJSONResult
	require.NoError(t, json.Unmarshal(out.Bytes(), &res))
	assert.Equal(t, []string{convID}, res.Deleted)
}

func TestConversationDelete_ExperimentOffMessage(t *testing.T) {
	convID := uuid.NewString()
	srv, _ := newConversationDeleteTestHub(t, convID, "my-agent", http.StatusNotFound)
	client, err := hubclient.New(srv.URL)
	require.NoError(t, err)

	cmd := &cobra.Command{}
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)

	err = executeConversationDelete(context.Background(), cmd, client, "", []string{convID}, true)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found, or conversation lifecycle is not enabled on this hub (experiment hub.conversation_lifecycle)")
}

func TestConversationDelete_DeduplicatesRepeatedRefs(t *testing.T) {
	convID := uuid.NewString()
	srv, deletedIDs := newConversationDeleteTestHub(t, convID, "my-agent", http.StatusNoContent)
	client, err := hubclient.New(srv.URL)
	require.NoError(t, err)

	cmd := &cobra.Command{}
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)

	require.NoError(t, executeConversationDelete(context.Background(), cmd, client, "", []string{convID, "conv:" + convID}, true))
	assert.Equal(t, []string{convID}, *deletedIDs, "duplicate refs must only delete once")
}

func TestConversationDelete_PartialMultiRefFailureEmitsDeletedIDs(t *testing.T) {
	conv1 := uuid.NewString()
	conv2 := uuid.NewString()
	var deletedIDs []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && (r.URL.Path == "/api/v1/conversations/"+conv1 || r.URL.Path == "/api/v1/conversations/"+conv2):
			id := strings.TrimPrefix(r.URL.Path, "/api/v1/conversations/")
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(hubclient.ConversationDetail{
				Conversation: store.Conversation{ID: id, Kind: "direct", Surface: "native"},
			})
		case r.Method == http.MethodDelete && r.URL.Path == "/api/v1/conversations/"+conv1:
			deletedIDs = append(deletedIDs, conv1)
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodDelete && r.URL.Path == "/api/v1/conversations/"+conv2:
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`{"error":{"code":"unsupported_surface","message":"non-native"}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)

	client, err := hubclient.New(srv.URL)
	require.NoError(t, err)

	cmd := &cobra.Command{}
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)

	err = executeConversationDelete(context.Background(), cmd, client, "", []string{conv1, conv2}, true)
	require.Error(t, err)
	assert.Equal(t, []string{conv1}, deletedIDs)
	assert.Contains(t, out.String(), "Deleted conversation "+conv1, "already-deleted IDs must be printed before returning the error")
}

func TestConversationDelete_Gates(t *testing.T) {
	assert.False(t, agentAllowed["conversation.delete"], "conversation.delete must be human-only (omitted from agentAllowed)")
	assert.True(t, windowsAllowed["conversation.delete"], "conversation.delete must be allowed on Windows")
}

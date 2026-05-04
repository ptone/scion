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

package chatapp

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/GoogleCloudPlatform/scion/extras/scion-chat-app/internal/state"
	"github.com/GoogleCloudPlatform/scion/pkg/messages"
)

// TestHandleBrokerMessage_UserMessageRouting verifies that user-targeted
// messages with the full scion broker topic prefix are correctly routed
// to handleUserMessage.
func TestHandleBrokerMessage_UserMessageRouting(t *testing.T) {
	log := slog.Default()
	relay := NewNotificationRelay(nil, nil, log)

	// Message with empty RecipientID triggers early return in handleUserMessage
	// without touching the store, so we can test topic routing safely.
	msg := &messages.StructuredMessage{
		Sender: "agent:test-agent",
		Msg:    "hello from agent",
	}

	// Full scion-prefixed topic should route to handleUserMessage.
	err := relay.HandleBrokerMessage(context.Background(),
		"scion.grove.grove-123.user.user-456.messages", msg)
	if err != nil {
		t.Errorf("expected nil error for user message topic, got: %v", err)
	}
}

// TestHandleBrokerMessage_IgnoredTopics verifies that unrecognized or
// malformed topics are silently ignored.
func TestHandleBrokerMessage_IgnoredTopics(t *testing.T) {
	log := slog.Default()
	relay := NewNotificationRelay(nil, nil, log)
	msg := &messages.StructuredMessage{Msg: "test"}

	topics := []string{
		"x",
		"scion.global.broadcast",
		"user.user-456.message", // old unprefixed format
	}

	for _, topic := range topics {
		t.Run(topic, func(t *testing.T) {
			err := relay.HandleBrokerMessage(context.Background(), topic, msg)
			if err != nil {
				t.Errorf("expected nil error for ignored topic %q, got: %v", topic, err)
			}
		})
	}
}

// fakeMessenger records SendMessage and SendCard calls for test assertions.
type fakeMessenger struct {
	messages []SendMessageRequest
}

func (f *fakeMessenger) SendMessage(_ context.Context, req SendMessageRequest) (string, error) {
	f.messages = append(f.messages, req)
	return "msg-1", nil
}

func (f *fakeMessenger) SendCard(_ context.Context, spaceID string, card Card) (string, error) {
	f.messages = append(f.messages, SendMessageRequest{SpaceID: spaceID, Card: &card})
	return "msg-1", nil
}
func (f *fakeMessenger) UpdateMessage(context.Context, string, SendMessageRequest) error { return nil }
func (f *fakeMessenger) OpenDialog(context.Context, string, Dialog) error                { return nil }
func (f *fakeMessenger) UpdateDialog(context.Context, string, Dialog) error              { return nil }
func (f *fakeMessenger) GetUser(context.Context, string) (*ChatUser, error)              { return nil, nil }
func (f *fakeMessenger) SetAgentIdentity(context.Context, AgentIdentity) error           { return nil }

// newTestStore creates an ephemeral SQLite store in a temp directory.
func newTestStore(t *testing.T) *state.Store {
	t.Helper()
	dir := t.TempDir()
	s, err := state.New(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("creating test store: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// TestHandleUserMessage_NoSubscriptionRequired verifies that a direct message
// from an agent to a user is delivered even when the user has zero subscriptions.
func TestHandleUserMessage_NoSubscriptionRequired(t *testing.T) {
	store := newTestStore(t)

	// Seed a user mapping and a space link but NO subscriptions.
	if err := store.SetUserMapping(&state.UserMapping{
		PlatformUserID: "users/12345",
		Platform:       "googlechat",
		HubUserID:      "hub-user-1",
		HubUserEmail:   "test@example.com",
		RegisteredBy:   "auto",
	}); err != nil {
		t.Fatalf("setting user mapping: %v", err)
	}

	if err := store.SetSpaceLink(&state.SpaceLink{
		SpaceID:   "spaces/AAQAx",
		Platform:  "googlechat",
		GroveID:   "grove-abc",
		GroveSlug: "my-grove",
		LinkedBy:  "test",
	}); err != nil {
		t.Fatalf("setting space link: %v", err)
	}

	fm := &fakeMessenger{}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
	relay := NewNotificationRelay(store, fm, log)

	msg := &messages.StructuredMessage{
		Sender:      "agent:simon",
		RecipientID: "hub-user-1",
		Msg:         "Here is the answer to your question.",
		Type:        messages.TypeInstruction,
	}

	err := relay.HandleBrokerMessage(context.Background(),
		"scion.grove.grove-abc.user.hub-user-1.messages", msg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(fm.messages) == 0 {
		t.Fatal("expected at least one message to be sent, got none — direct messages must not require subscriptions")
	}

	got := fm.messages[0]
	if got.SpaceID != "spaces/AAQAx" {
		t.Errorf("message sent to wrong space: got %q, want %q", got.SpaceID, "spaces/AAQAx")
	}
	if got.Card == nil {
		t.Fatal("expected a card in the message")
	}
	wantTitle := "\U0001F916 simon"
	if got.Card.Header.Title != wantTitle {
		t.Errorf("card title = %q, want %q", got.Card.Header.Title, wantTitle)
	}

	// @mentions should be in the text body, not inside the card
	if got.Text != "<users/12345>" {
		t.Errorf("text body = %q, want @mention %q", got.Text, "<users/12345>")
	}

	// Card should have no action buttons
	if len(got.Card.Actions) != 0 {
		t.Errorf("expected no card actions, got %d", len(got.Card.Actions))
	}
}

func TestFormatMention(t *testing.T) {
	tests := []struct {
		platform string
		userID   string
		want     string
	}{
		{"slack", "U0ABC123", "<@U0ABC123>"},
		{"google_chat", "users/12345", "<users/12345>"},
		{"googlechat", "users/12345", "<users/12345>"},
		{"unknown", "user123", "<user123>"},
	}

	for _, tt := range tests {
		t.Run(tt.platform+"/"+tt.userID, func(t *testing.T) {
			got := formatMention(tt.platform, tt.userID)
			if got != tt.want {
				t.Errorf("formatMention(%q, %q) = %q, want %q", tt.platform, tt.userID, got, tt.want)
			}
		})
	}
}

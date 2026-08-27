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

package messaging

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/GoogleCloudPlatform/scion/pkg/store"
)

// mockConversationUpserter is a test double for ConversationUpserter.
type mockConversationUpserter struct {
	// lastConv captures the most recent conversation passed to Upsert.
	lastConv *store.Conversation
	// returnConv is the conversation returned by Upsert.
	returnConv *store.Conversation
	// returnErr is the error returned by Upsert.
	returnErr error
}

func (m *mockConversationUpserter) UpsertConversationByExternalRef(
	_ context.Context, conv *store.Conversation,
) (*store.Conversation, error) {
	m.lastConv = conv
	if m.returnErr != nil {
		return nil, m.returnErr
	}
	if m.returnConv != nil {
		return m.returnConv, nil
	}
	// Default: echo back with an ID.
	result := *conv
	result.ID = "conv-id-123"
	return &result, nil
}

func TestResolveOrCreateDMConversation_HappyPath(t *testing.T) {
	mock := &mockConversationUpserter{
		returnConv: &store.Conversation{ID: "conv-abc"},
	}
	logger := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))

	got := ResolveOrCreateDMConversation(context.Background(), mock, logger, "alice", "bob", "proj-1")
	if got != "conv-abc" {
		t.Errorf("expected conv-abc, got %q", got)
	}
}

func TestResolveOrCreateDMConversation_EmptySender(t *testing.T) {
	mock := &mockConversationUpserter{}
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	got := ResolveOrCreateDMConversation(context.Background(), mock, logger, "", "bob", "proj-1")
	if got != "" {
		t.Errorf("expected empty string for empty sender, got %q", got)
	}
	if mock.lastConv != nil {
		t.Error("upsert should not have been called with empty sender")
	}
}

func TestResolveOrCreateDMConversation_EmptyRecipient(t *testing.T) {
	mock := &mockConversationUpserter{}
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	got := ResolveOrCreateDMConversation(context.Background(), mock, logger, "alice", "", "proj-1")
	if got != "" {
		t.Errorf("expected empty string for empty recipient, got %q", got)
	}
	if mock.lastConv != nil {
		t.Error("upsert should not have been called with empty recipient")
	}
}

func TestResolveOrCreateDMConversation_UpsertError(t *testing.T) {
	mock := &mockConversationUpserter{
		returnErr: errors.New("db connection lost"),
	}
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	got := ResolveOrCreateDMConversation(context.Background(), mock, logger, "alice", "bob", "proj-1")
	if got != "" {
		t.Errorf("expected empty string on upsert error, got %q", got)
	}
	output := buf.String()
	if !strings.Contains(output, "conversation resolution failed") {
		t.Errorf("expected error log, got: %s", output)
	}
}

func TestResolveOrCreateDMConversation_ExternalRefIsSortedDMPair(t *testing.T) {
	mock := &mockConversationUpserter{}
	logger := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))

	// Call with "bob" first, "alice" second — ref should sort to alice:bob.
	ResolveOrCreateDMConversation(context.Background(), mock, logger, "bob", "alice", "")
	if mock.lastConv == nil {
		t.Fatal("expected upsert to be called")
	}
	expected := "dm:alice:bob"
	if mock.lastConv.ExternalRef != expected {
		t.Errorf("expected external_ref %q, got %q", expected, mock.lastConv.ExternalRef)
	}
}

func TestResolveOrCreateDMConversation_ProjectIDSet(t *testing.T) {
	mock := &mockConversationUpserter{}
	logger := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))

	ResolveOrCreateDMConversation(context.Background(), mock, logger, "alice", "bob", "proj-42")
	if mock.lastConv == nil {
		t.Fatal("expected upsert to be called")
	}
	if mock.lastConv.ProjectID == nil {
		t.Fatal("expected ProjectID to be set")
	}
	if *mock.lastConv.ProjectID != "proj-42" {
		t.Errorf("expected ProjectID proj-42, got %q", *mock.lastConv.ProjectID)
	}
}

func TestResolveOrCreateDMConversation_ProjectIDNilWhenEmpty(t *testing.T) {
	mock := &mockConversationUpserter{}
	logger := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))

	ResolveOrCreateDMConversation(context.Background(), mock, logger, "alice", "bob", "")
	if mock.lastConv == nil {
		t.Fatal("expected upsert to be called")
	}
	if mock.lastConv.ProjectID != nil {
		t.Errorf("expected ProjectID to be nil for empty projectID, got %v", mock.lastConv.ProjectID)
	}
}

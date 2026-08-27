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
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/GoogleCloudPlatform/scion/pkg/api"
	"github.com/GoogleCloudPlatform/scion/pkg/messages"
	"github.com/GoogleCloudPlatform/scion/pkg/store"
)

// TestHandleGroupMessage_ThreadID_NotPropagated proves that even when a
// StructuredMessage arrives at handleGroupMessage with a ThreadID set, that
// ThreadID does NOT appear in persisted message rows and does NOT influence
// conversation key derivation.
//
// Traceability: AC-S215-M1
func TestHandleGroupMessage_ThreadID_NotPropagated(t *testing.T) {
	srv, s := testServer(t)
	ctx := context.Background()

	// --- Setup: project, user, two agents (anchor + target) ---

	project := &store.Project{
		ID:         api.NewUUID(),
		Name:       "groupmsg-threadid-project",
		Slug:       "groupmsg-threadid-project",
		Visibility: store.VisibilityPrivate,
	}
	if err := s.CreateProject(ctx, project); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}

	userID := api.NewUUID()
	if err := s.CreateUser(ctx, &store.User{
		ID:          userID,
		Email:       "test@example.com",
		DisplayName: "Test",
	}); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	anchorAgent := &store.Agent{
		ID:         api.NewUUID(),
		Name:       "anchor-agent",
		Slug:       "anchor-agent",
		ProjectID:  project.ID,
		Phase:      "running",
		Visibility: store.VisibilityPrivate,
	}
	if err := s.CreateAgent(ctx, anchorAgent); err != nil {
		t.Fatalf("CreateAgent(anchor): %v", err)
	}

	targetAgent := &store.Agent{
		ID:         api.NewUUID(),
		Name:       "target-agent",
		Slug:       "target-agent",
		ProjectID:  project.ID,
		Phase:      "running",
		Visibility: store.VisibilityPrivate,
	}
	if err := s.CreateAgent(ctx, targetAgent); err != nil {
		t.Fatalf("CreateAgent(target): %v", err)
	}

	// --- Send a group message with a ThreadID set ---

	injectedThreadID := "thread:proj:some-thread-id"

	body := map[string]interface{}{
		"structured_message": &messages.StructuredMessage{
			Version:   messages.Version,
			Timestamp: time.Now().UTC().Format(time.RFC3339),
			Sender:    "user:Test",
			SenderID:  userID,
			Recipient: "group[agent:target-agent,agent:anchor-agent]",
			Msg:       "test group message with thread id",
			Type:      messages.TypeInstruction,
			Channel:   "web",
			ThreadID:  injectedThreadID,
		},
	}

	rec := doRequest(t, srv, http.MethodPost,
		fmt.Sprintf("/api/v1/agents/%s/message", anchorAgent.ID), body)

	// The request may return 200 (group response) even if dispatch fails
	// (no runtime broker configured). The important thing is that the message
	// was persisted before dispatch was attempted.
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 from group message handler, got %d: %s",
			rec.Code, rec.Body.String())
	}

	// --- Assert 1: No stored message has a non-empty ThreadID ---

	result, err := s.ListMessages(ctx,
		store.MessageFilter{ProjectID: project.ID},
		store.ListOptions{})
	if err != nil {
		t.Fatalf("ListMessages: %v", err)
	}

	if len(result.Items) == 0 {
		t.Fatal("expected at least one persisted message, got none")
	}

	for _, msg := range result.Items {
		if msg.ThreadID != "" {
			t.Errorf("persisted message %s has ThreadID=%q, want empty: "+
				"handleGroupMessage must not propagate the caller's ThreadID "+
				"into stored messages (AC-S215-M1)", msg.ID, msg.ThreadID)
		}
	}

	// --- Assert 2: ThreadID did not influence conversation key derivation ---
	// handleGroupMessage creates store.Message rows directly without calling
	// DeriveConversationKey or ResolveOrCreateDMConversation. Verify that no
	// stored message references the injected ThreadID in any field that could
	// serve as a conversation key.

	for _, msg := range result.Items {
		// Check that no field echoes back the injected thread ID.
		if strings.Contains(msg.ThreadID, injectedThreadID) {
			t.Errorf("message %s ThreadID contains the injected value %q: "+
				"conversation key must be derived from principal pairs, "+
				"not from caller-supplied ThreadID (AC-S215-M1)",
				msg.ID, injectedThreadID)
		}
		// GroupID should be a fresh UUID, not derived from ThreadID.
		if msg.GroupID == injectedThreadID {
			t.Errorf("message %s GroupID equals the injected ThreadID %q: "+
				"group key must not be influenced by caller-supplied ThreadID (AC-S215-M1)",
				msg.ID, injectedThreadID)
		}
	}

	t.Logf("AC-S215-M1 PASS: %d message(s) persisted, none carry the "+
		"injected ThreadID %q", len(result.Items), injectedThreadID)
}

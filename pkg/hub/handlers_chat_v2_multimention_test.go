//go:build !no_sqlite

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

// DEF-169 investigation: multi-mention delivery test.
//
// Symptom: a message mentioning @agent-a @agent-b in a native-chat topic
// delivers to only one agent (reported as "the last one mentioned").
//
// This test sends a message through the full handleConversationSend path
// (not sendAgentRouted in isolation) with two distinct @-mentions and
// asserts:
//  1. Both agents receive a dispatch call (via mock dispatcher).
//  2. Two store.Message rows are persisted (one primary + one fan-out).
//  3. The HTTP response contains mention results for both agents.

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/GoogleCloudPlatform/scion/pkg/messages"
	"github.com/GoogleCloudPlatform/scion/pkg/store"
)

func TestChatV2_Send_MultiMention_BothAgentsReceiveDispatch(t *testing.T) {
	srv, s, wcs, proj, db := setupSendTest(t)
	ctx := t.Context()

	// Create two distinct agents in the same project.
	agentA := &store.Agent{
		ID:        tid("multi-mention-agent-a"),
		ProjectID: proj.ID,
		Name:      "Agent Alpha",
		Slug:      "agent-alpha",
		Phase:     "idle",
		OwnerID:   DevUserID,
		CreatedBy: DevUserID,
	}
	agentB := &store.Agent{
		ID:        tid("multi-mention-agent-b"),
		ProjectID: proj.ID,
		Name:      "Agent Beta",
		Slug:      "agent-beta",
		Phase:     "idle",
		OwnerID:   DevUserID,
		CreatedBy: DevUserID,
	}
	for _, a := range []*store.Agent{agentA, agentB} {
		if err := s.CreateAgent(ctx, a); err != nil {
			t.Fatalf("CreateAgent(%s): %v", a.Slug, err)
		}
	}

	// Install a recording dispatcher so we can verify dispatch calls.
	dispatcher := &brokerMockDispatcher{}
	srv.SetDispatcher(dispatcher)

	// Create a topic with NO default_agent — routing is purely mention-driven.
	topicID := tid("topic-multi-mention")
	if err := wcs.CreateTopic(ctx, WebChatTopic{
		ID:        topicID,
		ProjectID: proj.ID,
		Name:      "multi-mention-thread",
		CreatedBy: "dev",
		CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("CreateTopic: %v", err)
	}
	setTopicConversationID(t, db, s, topicID, proj.ID)

	// Send a message mentioning BOTH agents.
	body := map[string]string{"content": "@agent-alpha @agent-beta please review"}
	rec := doRequest(t, srv, http.MethodPost,
		"/api/v1/chat/conversations/"+topicID+"/messages", body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}

	// --- Assert 1: HTTP response ---
	var resp chatMessageResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Type != messages.TypeMention {
		t.Errorf("expected type %q, got %q", messages.TypeMention, resp.Type)
	}
	// The response should contain mention results for both agents.
	if len(resp.Mentions) < 2 {
		t.Errorf("expected at least 2 mention results, got %d: %+v", len(resp.Mentions), resp.Mentions)
	}
	deliveredCount := 0
	for _, mr := range resp.Mentions {
		if mr.Status == "delivered" {
			deliveredCount++
		}
	}
	if deliveredCount < 2 {
		t.Errorf("expected at least 2 delivered mentions, got %d; results: %+v", deliveredCount, resp.Mentions)
	}

	// --- Assert 2: Persisted messages ---
	// One message for the primary agent (agents[0]) and one for the fan-out agent.
	msgsA, err := s.ListMessages(ctx, store.MessageFilter{AgentID: agentA.ID}, store.ListOptions{Limit: 10})
	if err != nil {
		t.Fatalf("ListMessages(agentA): %v", err)
	}
	msgsB, err := s.ListMessages(ctx, store.MessageFilter{AgentID: agentB.ID}, store.ListOptions{Limit: 10})
	if err != nil {
		t.Fatalf("ListMessages(agentB): %v", err)
	}
	totalPersisted := len(msgsA.Items) + len(msgsB.Items)
	if totalPersisted < 2 {
		t.Errorf("expected at least 2 persisted messages (1 per agent), got %d (agentA=%d, agentB=%d)",
			totalPersisted, len(msgsA.Items), len(msgsB.Items))
	}
	// Each agent should have at least one message.
	if len(msgsA.Items) == 0 {
		t.Errorf("agent-alpha has 0 persisted messages; expected at least 1")
	}
	if len(msgsB.Items) == 0 {
		t.Errorf("agent-beta has 0 persisted messages; expected at least 1")
	}

	// --- Assert 3: Dispatch calls ---
	dispatched := dispatcher.getMessages()
	if len(dispatched) < 2 {
		t.Errorf("expected at least 2 dispatch calls, got %d", len(dispatched))
		for i, d := range dispatched {
			t.Logf("  dispatch[%d]: slug=%q msg=%q", i, d.agentSlug, d.msg)
		}
	}
	// Verify each agent received a dispatch.
	dispatchedSlugs := make(map[string]bool)
	for _, d := range dispatched {
		dispatchedSlugs[d.agentSlug] = true
	}
	if !dispatchedSlugs["agent-alpha"] {
		t.Errorf("agent-alpha was NOT dispatched; dispatched slugs: %v", dispatchedSlugs)
	}
	if !dispatchedSlugs["agent-beta"] {
		t.Errorf("agent-beta was NOT dispatched; dispatched slugs: %v", dispatchedSlugs)
	}

	// --- Assert 4: Dispatch structured messages carry correct recipients ---
	for _, d := range dispatched {
		if d.structured == nil {
			t.Errorf("dispatch to %q has nil structured message", d.agentSlug)
			continue
		}
		expectedRecipient := "agent:" + d.agentSlug
		if d.structured.Recipient != expectedRecipient {
			t.Errorf("dispatch to %q: structured.Recipient = %q, want %q",
				d.agentSlug, d.structured.Recipient, expectedRecipient)
		}
	}
}

// TestChatV2_Send_MultiMention_ThreeAgents extends the multi-mention test
// to three agents, verifying the fan-out loop handles N>2 correctly.
func TestChatV2_Send_MultiMention_ThreeAgents(t *testing.T) {
	srv, s, wcs, proj, db := setupSendTest(t)
	ctx := t.Context()

	slugs := []string{"tri-agent-x", "tri-agent-y", "tri-agent-z"}
	agents := make([]*store.Agent, 0, len(slugs))
	for _, slug := range slugs {
		a := &store.Agent{
			ID:        tid("multi3-" + slug),
			ProjectID: proj.ID,
			Name:      slug,
			Slug:      slug,
			Phase:     "idle",
			OwnerID:   DevUserID,
			CreatedBy: DevUserID,
		}
		if err := s.CreateAgent(ctx, a); err != nil {
			t.Fatalf("CreateAgent(%s): %v", slug, err)
		}
		agents = append(agents, a)
	}

	dispatcher := &brokerMockDispatcher{}
	srv.SetDispatcher(dispatcher)

	topicID := tid("topic-multi-3")
	if err := wcs.CreateTopic(ctx, WebChatTopic{
		ID:        topicID,
		ProjectID: proj.ID,
		Name:      "three-mention-thread",
		CreatedBy: "dev",
		CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("CreateTopic: %v", err)
	}
	setTopicConversationID(t, db, s, topicID, proj.ID)

	body := map[string]string{"content": "@tri-agent-x @tri-agent-y @tri-agent-z coordinate"}
	rec := doRequest(t, srv, http.MethodPost,
		"/api/v1/chat/conversations/"+topicID+"/messages", body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}

	// Verify all three agents got dispatched.
	dispatched := dispatcher.getMessages()
	if len(dispatched) != 3 {
		t.Errorf("expected exactly 3 dispatch calls, got %d", len(dispatched))
		for i, d := range dispatched {
			t.Logf("  dispatch[%d]: slug=%q", i, d.agentSlug)
		}
	}
	dispatchedSlugs := make(map[string]bool)
	for _, d := range dispatched {
		dispatchedSlugs[d.agentSlug] = true
	}
	for _, slug := range slugs {
		if !dispatchedSlugs[slug] {
			t.Errorf("%s was NOT dispatched; dispatched slugs: %v", slug, dispatchedSlugs)
		}
	}

	// Verify all three agents have persisted messages.
	for _, a := range agents {
		msgs, err := s.ListMessages(ctx, store.MessageFilter{AgentID: a.ID}, store.ListOptions{Limit: 10})
		if err != nil {
			t.Fatalf("ListMessages(%s): %v", a.Slug, err)
		}
		if len(msgs.Items) == 0 {
			t.Errorf("%s has 0 persisted messages; expected 1", a.Slug)
		}
	}
}

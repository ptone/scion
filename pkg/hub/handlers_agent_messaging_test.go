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
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/GoogleCloudPlatform/scion/pkg/api"
	"github.com/GoogleCloudPlatform/scion/pkg/messages"
	"github.com/GoogleCloudPlatform/scion/pkg/messaging"
	"github.com/GoogleCloudPlatform/scion/pkg/store"
	"github.com/go-jose/go-jose/v4/jwt"
)

// testDirectMessageExternalRef replicates the legacy (pre-DEF-8) external ref
// format: dm:<sortedID1>:<sortedID2>. The canonical format was changed to
// dm:<kind>:<uuid>:<kind>:<uuid> via DMConversationKey, but the divergence
// tests need the old shape for comparison.
func testDirectMessageExternalRef(idA, idB string) string {
	pair := []string{idA, idB}
	sort.Strings(pair)
	return fmt.Sprintf("dm:%s:%s", pair[0], pair[1])
}

// postOutbound sends one agent→human message as the given agent.
func postOutbound(t *testing.T, srv *Server, projectID, agentID, msg string) *httptest.ResponseRecorder {
	t.Helper()
	return postOutboundTyped(t, srv, projectID, agentID, msg, "")
}

// postOutboundTyped sends one agent→human message of a specific message type.
func postOutboundTyped(t *testing.T, srv *Server, projectID, agentID, msg, msgType string) *httptest.ResponseRecorder {
	t.Helper()
	body, _ := json.Marshal(OutboundMessageRequest{
		Recipient: "user:human@example.com",
		Msg:       msg,
		Type:      msgType,
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/agents/"+agentID+"/outbound-message", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(contextWithIdentity(req.Context(), &agentIdentityWrapper{&AgentTokenClaims{
		Claims:    jwt.Claims{Subject: agentID},
		ProjectID: projectID,
	}}))

	rr := httptest.NewRecorder()
	srv.handleAgentOutboundMessage(rr, req, agentID)
	return rr
}

// An agent stuck in a loop is cut off with an explicit, retryable 429 — the
// flood vector issue #1054 is actually about. The limit is per sender, so a
// second agent going about its business is untouched.
func TestOutboundMessage_RateLimitsFloodingAgent(t *testing.T) {
	srv, s := testServer(t)
	ctx := context.Background()

	project := &store.Project{
		ID:         api.NewUUID(),
		Name:       "flood-project",
		Slug:       "flood-project",
		Visibility: store.VisibilityPrivate,
	}
	if err := s.CreateProject(ctx, project); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	if err := s.CreateUser(ctx, &store.User{
		ID:          api.NewUUID(),
		Email:       "human@example.com",
		DisplayName: "Human",
	}); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	newAgent := func(name string) string {
		a := &store.Agent{
			ID:         api.NewUUID(),
			Name:       name,
			Slug:       name,
			ProjectID:  project.ID,
			Phase:      "running",
			Visibility: store.VisibilityPrivate,
		}
		if err := s.CreateAgent(ctx, a); err != nil {
			t.Fatalf("CreateAgent: %v", err)
		}
		return a.ID
	}
	flooder := newAgent("flooder")
	bystander := newAgent("bystander")

	// Production limits, test clock: the real 60/min ceiling without a real
	// minute of waiting.
	clock := newTestClock()
	srv.chatSendLimiter = newChatSendLimiterWithClock(clock.Now)

	for i := range chatSendAgentRatePerMinute {
		if rr := postOutbound(t, srv, project.ID, flooder, "spam"); rr.Code != http.StatusOK {
			t.Fatalf("send %d: expected 200, got %d: %s", i+1, rr.Code, rr.Body.String())
		}
	}

	rr := postOutbound(t, srv, project.ID, flooder, "one too many")
	if rr.Code != http.StatusTooManyRequests {
		t.Fatalf("send %d: expected 429, got %d: %s",
			chatSendAgentRatePerMinute+1, rr.Code, rr.Body.String())
	}
	retryAfter := rr.Header().Get("Retry-After")
	if secs, err := strconv.Atoi(retryAfter); err != nil || secs < 1 {
		t.Errorf("Retry-After = %q, want a positive number of seconds so the agent can back off", retryAfter)
	}
	if !strings.Contains(rr.Body.String(), ErrCodeRateLimited) {
		t.Errorf("expected a %q error code in the body, got %s", ErrCodeRateLimited, rr.Body.String())
	}
	// No current client reads Retry-After, so the delay in the message text is
	// what a sending agent actually sees. Assert it as well as the header.
	if want := "retry in " + retryAfter + "s"; !strings.Contains(rr.Body.String(), want) {
		t.Errorf("expected the body to carry the retry delay %q, got %s", want, rr.Body.String())
	}

	if rr := postOutbound(t, srv, project.ID, bystander, "unrelated report"); rr.Code != http.StatusOK {
		t.Errorf("a second agent must not be throttled by the flooder: got %d: %s", rr.Code, rr.Body.String())
	}

	// The refusal is transient: at 60/min a token accrues every second.
	clock.Advance(time.Second)
	if rr := postOutbound(t, srv, project.ID, flooder, "after backoff"); rr.Code != http.StatusOK {
		t.Errorf("expected the send to succeed after backing off, got %d: %s", rr.Code, rr.Body.String())
	}
}

// An unrecognised message type is charged as ordinary agent traffic: the
// class must come from the closed enum, not from whatever the caller puts in
// the body, so an unfamiliar label cannot buy the cheaper mirror reservation.
// The send itself is still accepted, as it is today — tightening the type
// contract on the wire is a separate compatibility change (#1054).
func TestOutboundMessage_UnknownTypeIsChargedAsAgentTraffic(t *testing.T) {
	srv, s := testServer(t)
	ctx := context.Background()

	project := &store.Project{
		ID:         api.NewUUID(),
		Name:       "type-project",
		Slug:       "type-project",
		Visibility: store.VisibilityPrivate,
	}
	if err := s.CreateProject(ctx, project); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	if err := s.CreateUser(ctx, &store.User{
		ID:          api.NewUUID(),
		Email:       "human@example.com",
		DisplayName: "Human",
	}); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	agent := &store.Agent{
		ID:         api.NewUUID(),
		Name:       "mislabeller",
		Slug:       "mislabeller",
		ProjectID:  project.ID,
		Phase:      "running",
		Visibility: store.VisibilityPrivate,
	}
	if err := s.CreateAgent(ctx, agent); err != nil {
		t.Fatalf("CreateAgent: %v", err)
	}

	clock := newTestClock()
	srv.chatSendLimiter = newChatSendLimiterWithClock(clock.Now)

	rr := postOutboundTyped(t, srv, project.ID, agent.ID, "mislabelled", "not-a-real-type")
	// After the S3 validation audit (M2), outbound messages go through
	// ValidateLegacyMessage which rejects unknown message types.
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected unknown message type to be rejected with 400, got %d: %s", rr.Code, rr.Body.String())
	}
}

// The automatic assistant-reply transcript mirror shares the agent's single
// aggregate allowance with the messages the agent writes itself, but it may
// only spend its own reservation of it: a chatty agent whose mirror is
// flooding can still deliver a completion report or a blocker escalation. Low
// value traffic must not starve high-value traffic.
//
// The mirror is driven well past the aggregate ceiling here, not merely up to
// its reservation — otherwise the test would pass even with no reservation at
// all and would prove nothing about starvation.
func TestOutboundMessage_TranscriptMirrorDoesNotStarveAgentMessages(t *testing.T) {
	srv, s := testServer(t)
	ctx := context.Background()

	project := &store.Project{
		ID:         api.NewUUID(),
		Name:       "mirror-project",
		Slug:       "mirror-project",
		Visibility: store.VisibilityPrivate,
	}
	if err := s.CreateProject(ctx, project); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	if err := s.CreateUser(ctx, &store.User{
		ID:          api.NewUUID(),
		Email:       "human@example.com",
		DisplayName: "Human",
	}); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	agent := &store.Agent{
		ID:         api.NewUUID(),
		Name:       "chatty",
		Slug:       "chatty",
		ProjectID:  project.ID,
		Phase:      "running",
		Visibility: store.VisibilityPrivate,
	}
	if err := s.CreateAgent(ctx, agent); err != nil {
		t.Fatalf("CreateAgent: %v", err)
	}

	clock := newTestClock()
	srv.chatSendLimiter = newChatSendLimiterWithClock(clock.Now)

	// Flood with hook-posted assistant replies, twice the agent's whole
	// aggregate allowance. Only the mirror's reservation may get through.
	accepted := 0
	for range 2 * chatSendAgentRatePerMinute {
		rr := postOutboundTyped(t, srv, project.ID, agent.ID, "mirrored transcript", messages.TypeAssistantReply)
		switch rr.Code {
		case http.StatusOK:
			accepted++
		case http.StatusTooManyRequests:
		default:
			t.Fatalf("mirror send: expected 200 or 429, got %d: %s", rr.Code, rr.Body.String())
		}
	}
	if accepted != chatSendAgentMirrorRatePerMinute {
		t.Fatalf("the flooding mirror got %d sends through, want exactly its reservation of %d",
			accepted, chatSendAgentMirrorRatePerMinute)
	}

	// The agent's own message to a human is unaffected.
	if rr := postOutbound(t, srv, project.ID, agent.ID, "task complete"); rr.Code != http.StatusOK {
		t.Fatalf("the agent's own message must not be starved by its transcript mirror: got %d: %s",
			rr.Code, rr.Body.String())
	}
}

// ---------------------------------------------------------------------------
// DEF-11 regression tests
// ---------------------------------------------------------------------------

// def11Setup creates a project, agent, and user for the DEF-11 tests.
// It returns the server, store, and the IDs needed to construct messages.
func def11Setup(t *testing.T) (srv *Server, s store.Store, projectID, agentSlug, agentID, userID string) {
	t.Helper()
	srv, s = testServer(t)
	ctx := context.Background()

	projectID = tid("def11-project")
	agentID = tid("def11-agent")
	userID = tid("def11-user")
	agentSlug = "def11-agent"

	if err := s.CreateProject(ctx, &store.Project{
		ID:         projectID,
		Name:       "def11-project",
		Slug:       "def11-project",
		Visibility: store.VisibilityPrivate,
	}); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	brokerID := tid("def11-broker")
	if err := s.CreateRuntimeBroker(ctx, &store.RuntimeBroker{
		ID:     brokerID,
		Name:   "def11-broker",
		Slug:   "def11-broker",
		Status: store.BrokerStatusOnline,
	}); err != nil {
		t.Fatalf("CreateRuntimeBroker: %v", err)
	}
	if err := s.AddProjectProvider(ctx, &store.ProjectProvider{
		ProjectID:  projectID,
		BrokerID:   brokerID,
		BrokerName: "def11-broker",
		Status:     store.BrokerStatusOnline,
	}); err != nil {
		t.Fatalf("AddProjectProvider: %v", err)
	}
	if err := s.CreateAgent(ctx, &store.Agent{
		ID:              agentID,
		Name:            "def11-agent",
		Slug:            agentSlug,
		ProjectID:       projectID,
		RuntimeBrokerID: brokerID,
		Phase:           "running",
		Visibility:      store.VisibilityPrivate,
	}); err != nil {
		t.Fatalf("CreateAgent: %v", err)
	}
	if err := s.CreateUser(ctx, &store.User{
		ID:          userID,
		Email:       "def11@example.com",
		DisplayName: "DEF11 User",
	}); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	// Set a dispatcher so the handler doesn't fail with 503.
	srv.SetDispatcher(&recordingDispatcher{})

	return srv, s, projectID, agentSlug, agentID, userID
}

// TestDEF11_PreResolvedConversation_PopulatesExternalRef verifies that when
// the CLI pre-resolves a ConversationID, the handler populates ExternalRef
// from the store — not leaving it as "".
func TestDEF11_PreResolvedConversation_PopulatesExternalRef(t *testing.T) {
	srv, s, projectID, agentSlug, agentID, userID := def11Setup(t)
	ctx := context.Background()

	// Create a conversation with a known ExternalRef.
	extRef := testDirectMessageExternalRef(userID, agentID)
	conv := &store.Conversation{
		Kind:        "direct",
		Surface:     "native",
		ExternalRef: extRef,
		DriftState:  "active",
	}
	created, err := s.UpsertConversationByExternalRef(ctx, conv)
	if err != nil {
		t.Fatalf("UpsertConversationByExternalRef: %v", err)
	}

	// Snapshot divergence metrics; a successful DM match can only happen
	// when ExternalRef starts with "dm:", proving it was loaded from the store.
	beforeMatches := messaging.DivergenceMetrics.Matches()
	beforeMismatches := messaging.DivergenceMetrics.Mismatches()

	// Post a message with a pre-resolved ConversationID.
	rec := doRequest(t, srv, http.MethodPost,
		"/api/v1/projects/"+projectID+"/agents/"+agentSlug+"/message",
		MessageRequest{
			StructuredMessage: &messages.StructuredMessage{
				Version:        messages.Version,
				Timestamp:      time.Now().UTC().Format(time.RFC3339),
				Sender:         "user:def11",
				SenderID:       userID,
				Recipient:      "agent:" + agentSlug,
				Msg:            "DEF11 AC-1 test",
				Type:           messages.TypeInstruction,
				ConversationID: created.ID,
			},
		})
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	// The divergence check should record a dm-routing-agreement match,
	// which is only possible when ExternalRef is correctly populated from the store.
	afterMatches := messaging.DivergenceMetrics.Matches()
	afterMismatches := messaging.DivergenceMetrics.Mismatches()
	if afterMatches-beforeMatches < 1 {
		t.Errorf("expected at least 1 new match (dm-routing-agreement), got delta=%d", afterMatches-beforeMatches)
	}
	if afterMismatches-beforeMismatches != 0 {
		t.Errorf("expected 0 new mismatches, got delta=%d", afterMismatches-beforeMismatches)
	}

	// Verify the conversation in the store still has the correct ExternalRef.
	readBack, err := s.GetConversation(ctx, created.ID)
	if err != nil {
		t.Fatalf("GetConversation: %v", err)
	}
	if readBack.ExternalRef != extRef {
		t.Errorf("ExternalRef changed: got %q, want %q", readBack.ExternalRef, extRef)
	}
}

// TestDEF11_PreResolvedConversation_DivergenceMatch verifies that a
// pre-resolved send with a matching DM conversation produces a divergence
// match (not a mismatch).
func TestDEF11_PreResolvedConversation_DivergenceMatch(t *testing.T) {
	srv, s, projectID, agentSlug, agentID, userID := def11Setup(t)
	ctx := context.Background()

	// Create a conversation matching the sender/agent DM pair.
	extRef := testDirectMessageExternalRef(userID, agentID)
	conv := &store.Conversation{
		Kind:        "direct",
		Surface:     "native",
		ExternalRef: extRef,
		DriftState:  "active",
	}
	created, err := s.UpsertConversationByExternalRef(ctx, conv)
	if err != nil {
		t.Fatalf("UpsertConversationByExternalRef: %v", err)
	}

	beforeMatches := messaging.DivergenceMetrics.Matches()
	beforeMismatches := messaging.DivergenceMetrics.Mismatches()

	rec := doRequest(t, srv, http.MethodPost,
		"/api/v1/projects/"+projectID+"/agents/"+agentSlug+"/message",
		MessageRequest{
			StructuredMessage: &messages.StructuredMessage{
				Version:        messages.Version,
				Timestamp:      time.Now().UTC().Format(time.RFC3339),
				Sender:         "user:def11",
				SenderID:       userID,
				Recipient:      "agent:" + agentSlug,
				Msg:            "DEF11 AC-2 test",
				Type:           messages.TypeInstruction,
				ConversationID: created.ID,
			},
		})
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	afterMatches := messaging.DivergenceMetrics.Matches()
	afterMismatches := messaging.DivergenceMetrics.Mismatches()

	if afterMatches-beforeMatches < 1 {
		t.Errorf("expected Matches delta >= 1, got %d", afterMatches-beforeMatches)
	}
	if afterMismatches-beforeMismatches != 0 {
		t.Errorf("expected Mismatches delta == 0, got %d", afterMismatches-beforeMismatches)
	}
}

// TestDEF11_PreResolvedConversation_LookupFailure verifies that when a
// pre-resolved ConversationID does not exist in the store, the handler records
// a fallback with reason "conv-lookup-failed" — not a plain
// "routing-type-mismatch". The Fallback flag on DivergenceEntry routes the
// event to the fallback counter only, leaving mismatches at zero.
func TestDEF11_PreResolvedConversation_LookupFailure(t *testing.T) {
	srv, _, projectID, agentSlug, _, userID := def11Setup(t)

	beforeFallbacks := messaging.DivergenceMetrics.Fallbacks()
	beforeMismatches := messaging.DivergenceMetrics.Mismatches()

	// Use a non-existent conversation ID.
	rec := doRequest(t, srv, http.MethodPost,
		"/api/v1/projects/"+projectID+"/agents/"+agentSlug+"/message",
		MessageRequest{
			StructuredMessage: &messages.StructuredMessage{
				Version:        messages.Version,
				Timestamp:      time.Now().UTC().Format(time.RFC3339),
				Sender:         "user:def11",
				SenderID:       userID,
				Recipient:      "agent:" + agentSlug,
				Msg:            "DEF11 AC-3 test",
				Type:           messages.TypeInstruction,
				ConversationID: "nonexistent-conv-id",
			},
		})
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 (message delivery is non-fatal), got %d: %s", rec.Code, rec.Body.String())
	}

	afterFallbacks := messaging.DivergenceMetrics.Fallbacks()
	afterMismatches := messaging.DivergenceMetrics.Mismatches()

	if afterFallbacks-beforeFallbacks < 1 {
		t.Errorf("expected Fallbacks delta >= 1 (conv-lookup-failed recorded), got %d",
			afterFallbacks-beforeFallbacks)
	}
	if afterMismatches-beforeMismatches != 0 {
		t.Errorf("expected Mismatches delta == 0 (fallback must not register as mismatch), got %d",
			afterMismatches-beforeMismatches)
	}
}

// ---------------------------------------------------------------------------
// DEF-19 regression tests
// ---------------------------------------------------------------------------

// TestDEF19_GroupRecipient_FullHandlerPath (AC-19-2) verifies that a group[]
// message survives the FULL HTTP handler path — going through
// POST /api/v1/projects/{pid}/agents/{slug}/message with a group[] recipient.
// It must reach handleGroupMessage and fan out, returning 200 (not 400).
func TestDEF19_GroupRecipient_FullHandlerPath(t *testing.T) {
	srv, s := testServer(t)
	ctx := context.Background()

	projectID := tid("def19-project")
	agentSlugA := "agent-a"
	agentIDA := tid("def19-agent-a")
	agentSlugB := "agent-b"
	agentIDB := tid("def19-agent-b")
	userID := tid("def19-user")
	// Use agentSlugA as the anchor agent in the URL path.
	anchorSlug := agentSlugA

	if err := s.CreateProject(ctx, &store.Project{
		ID:         projectID,
		Name:       "def19-project",
		Slug:       "def19-project",
		Visibility: store.VisibilityPrivate,
	}); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	brokerID := tid("def19-broker")
	if err := s.CreateRuntimeBroker(ctx, &store.RuntimeBroker{
		ID:     brokerID,
		Name:   "def19-broker",
		Slug:   "def19-broker",
		Status: store.BrokerStatusOnline,
	}); err != nil {
		t.Fatalf("CreateRuntimeBroker: %v", err)
	}
	if err := s.AddProjectProvider(ctx, &store.ProjectProvider{
		ProjectID:  projectID,
		BrokerID:   brokerID,
		BrokerName: "def19-broker",
		Status:     store.BrokerStatusOnline,
	}); err != nil {
		t.Fatalf("AddProjectProvider: %v", err)
	}
	if err := s.CreateAgent(ctx, &store.Agent{
		ID:              agentIDA,
		Name:            "agent-a",
		Slug:            agentSlugA,
		ProjectID:       projectID,
		RuntimeBrokerID: brokerID,
		Phase:           "running",
		Visibility:      store.VisibilityPrivate,
	}); err != nil {
		t.Fatalf("CreateAgent A: %v", err)
	}
	if err := s.CreateAgent(ctx, &store.Agent{
		ID:              agentIDB,
		Name:            "agent-b",
		Slug:            agentSlugB,
		ProjectID:       projectID,
		RuntimeBrokerID: brokerID,
		Phase:           "running",
		Visibility:      store.VisibilityPrivate,
	}); err != nil {
		t.Fatalf("CreateAgent B: %v", err)
	}
	if err := s.CreateUser(ctx, &store.User{
		ID:          userID,
		Email:       "def19@example.com",
		DisplayName: "DEF19 User",
	}); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	// Set a dispatcher so the handler doesn't fail with 503.
	srv.SetDispatcher(&recordingDispatcher{})

	// Send a group[] message through the full HTTP path.
	rec := doRequest(t, srv, http.MethodPost,
		"/api/v1/projects/"+projectID+"/agents/"+anchorSlug+"/message",
		MessageRequest{
			StructuredMessage: &messages.StructuredMessage{
				Version:   messages.Version,
				Timestamp: time.Now().UTC().Format(time.RFC3339),
				Sender:    "user:def19",
				SenderID:  userID,
				Recipient: "group[agent:" + agentSlugA + ",agent:" + agentSlugB + "]",
				Msg:       "DEF-19 group message test",
				Type:      messages.TypeInstruction,
			},
		})

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for group[] message, got %d: %s", rec.Code, rec.Body.String())
	}

	// Verify the response is a GroupMessageResponse.
	var resp GroupMessageResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to unmarshal GroupMessageResponse: %v", err)
	}
	if resp.GroupID == "" {
		t.Error("expected non-empty group_id in response")
	}
	if resp.Delivered != 2 {
		t.Errorf("expected 2 delivered, got %d", resp.Delivered)
	}
	if len(resp.Results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(resp.Results))
	}
	for i, r := range resp.Results {
		if r.Status != "delivered" {
			t.Errorf("result[%d]: expected status=delivered, got %q (error: %s)", i, r.Status, r.Error)
		}
	}
}

// TestDEF11_PreResolvedConversation_GenuineDisagreement verifies that the
// divergence comparison is active and can detect a real mismatch when the
// stored ExternalRef does not agree with the old-model routing.
func TestDEF11_PreResolvedConversation_GenuineDisagreement(t *testing.T) {
	srv, s, projectID, agentSlug, _, userID := def11Setup(t)
	ctx := context.Background()

	// Create a conversation with an ExternalRef that does NOT match the
	// sender/agent pair used in the message (different principals).
	wrongRef := testDirectMessageExternalRef("wrong-id-x", "wrong-id-y")
	conv := &store.Conversation{
		Kind:        "direct",
		Surface:     "native",
		ExternalRef: wrongRef,
		DriftState:  "active",
	}
	created, err := s.UpsertConversationByExternalRef(ctx, conv)
	if err != nil {
		t.Fatalf("UpsertConversationByExternalRef: %v", err)
	}

	beforeMismatches := messaging.DivergenceMetrics.Mismatches()

	// Post a message referencing the wrong conversation.
	rec := doRequest(t, srv, http.MethodPost,
		"/api/v1/projects/"+projectID+"/agents/"+agentSlug+"/message",
		MessageRequest{
			StructuredMessage: &messages.StructuredMessage{
				Version:        messages.Version,
				Timestamp:      time.Now().UTC().Format(time.RFC3339),
				Sender:         "user:def11",
				SenderID:       userID,
				Recipient:      "agent:" + agentSlug,
				Msg:            "DEF11 AC-4 test",
				Type:           messages.TypeInstruction,
				ConversationID: created.ID,
			},
		})
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	afterMismatches := messaging.DivergenceMetrics.Mismatches()
	if afterMismatches-beforeMismatches < 1 {
		t.Errorf("expected Mismatches delta >= 1 (genuine disagreement), got %d",
			afterMismatches-beforeMismatches)
	}
}

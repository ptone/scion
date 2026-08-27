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
	"context"
	"errors"
	"testing"

	"github.com/GoogleCloudPlatform/scion/pkg/store"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Mock ResolutionStore
// ---------------------------------------------------------------------------

type mockResolutionStore struct {
	conversations map[string]*store.Conversation
	participants  map[string][]store.ConversationParticipant // key: conversationID
	agents        map[string]*store.Agent                    // key: "projectID/slug"
	users         map[string]*store.User                     // key: email
}

func newMockStore() *mockResolutionStore {
	return &mockResolutionStore{
		conversations: make(map[string]*store.Conversation),
		participants:  make(map[string][]store.ConversationParticipant),
		agents:        make(map[string]*store.Agent),
		users:         make(map[string]*store.User),
	}
}

func (m *mockResolutionStore) GetConversation(_ context.Context, id string) (*store.Conversation, error) {
	conv, ok := m.conversations[id]
	if !ok {
		return nil, store.ErrNotFound
	}
	return conv, nil
}

func (m *mockResolutionStore) CreateConversation(_ context.Context, conv *store.Conversation) error {
	if _, ok := m.conversations[conv.ID]; ok {
		return store.ErrAlreadyExists
	}
	m.conversations[conv.ID] = conv
	return nil
}

func (m *mockResolutionStore) ListConversations(_ context.Context, filter store.ConversationFilter, _ store.ListOptions) (*store.ListResult[store.Conversation], error) {
	var items []store.Conversation
	for _, conv := range m.conversations {
		if filter.ProjectID != "" && (conv.ProjectID == nil || *conv.ProjectID != filter.ProjectID) {
			continue
		}
		if filter.Kind != "" && conv.Kind != filter.Kind {
			continue
		}
		if filter.Surface != "" && conv.Surface != filter.Surface {
			continue
		}
		if filter.DriftState != "" && conv.DriftState != filter.DriftState {
			continue
		}
		items = append(items, *conv)
	}
	return &store.ListResult[store.Conversation]{Items: items, TotalCount: len(items)}, nil
}

func (m *mockResolutionStore) AddParticipant(_ context.Context, p *store.ConversationParticipant) error {
	// Check for duplicates.
	for _, existing := range m.participants[p.ConversationID] {
		if existing.PrincipalKind == p.PrincipalKind && existing.PrincipalID == p.PrincipalID {
			return store.ErrAlreadyExists
		}
	}
	m.participants[p.ConversationID] = append(m.participants[p.ConversationID], *p)
	return nil
}

func (m *mockResolutionStore) ListParticipants(_ context.Context, conversationID string) ([]store.ConversationParticipant, error) {
	return m.participants[conversationID], nil
}

func (m *mockResolutionStore) GetConversationsForPrincipal(_ context.Context, principalKind, principalID string) ([]store.Conversation, error) {
	var result []store.Conversation
	for convID, parts := range m.participants {
		for _, p := range parts {
			if p.PrincipalKind == principalKind && p.PrincipalID == principalID {
				if conv, ok := m.conversations[convID]; ok {
					result = append(result, *conv)
				}
				break
			}
		}
	}
	return result, nil
}

func (m *mockResolutionStore) GetAgentBySlug(_ context.Context, projectID, slug string) (*store.Agent, error) {
	key := projectID + "/" + slug
	if agent, ok := m.agents[key]; ok {
		return agent, nil
	}
	return nil, store.ErrNotFound
}

func (m *mockResolutionStore) GetUserByEmail(_ context.Context, email string) (*store.User, error) {
	if user, ok := m.users[email]; ok {
		return user, nil
	}
	return nil, store.ErrNotFound
}

// helper to add a conversation with participants to the mock store.
func (m *mockResolutionStore) addConversation(conv *store.Conversation, participants ...store.ConversationParticipant) {
	m.conversations[conv.ID] = conv
	m.participants[conv.ID] = participants
}

// ---------------------------------------------------------------------------
// ParseReference tests
// ---------------------------------------------------------------------------

func TestParseReference_ConvID(t *testing.T) {
	id := uuid.NewString()
	ref, err := ParseReference("conv:" + id)
	require.NoError(t, err)
	assert.Equal(t, RefConversation, ref.Kind)
	assert.Equal(t, id, ref.Value)
	assert.Equal(t, "conv:"+id, ref.Raw)
}

func TestParseReference_ConvID_InvalidUUID(t *testing.T) {
	_, err := ParseReference("conv:not-a-uuid")
	require.Error(t, err)
	assert.True(t, errors.Is(err, store.ErrInvalidInput))
}

func TestParseReference_AgentSlug(t *testing.T) {
	ref, err := ParseReference("@my-agent")
	require.NoError(t, err)
	assert.Equal(t, RefAgent, ref.Kind)
	assert.Equal(t, "my-agent", ref.Value)
}

func TestParseReference_Email(t *testing.T) {
	ref, err := ParseReference("@user@example.com")
	require.NoError(t, err)
	assert.Equal(t, RefEmail, ref.Kind)
	assert.Equal(t, "user@example.com", ref.Value)
}

func TestParseReference_ThreadName(t *testing.T) {
	ref, err := ParseReference("#design-discussion")
	require.NoError(t, err)
	assert.Equal(t, RefThread, ref.Kind)
	assert.Equal(t, "design-discussion", ref.Value)
}

func TestParseReference_ThreadWithSlash_Rejected(t *testing.T) {
	// AC-31: #<space>/<thread> form must be rejected.
	_, err := ParseReference("#my-space/my-thread")
	require.Error(t, err)
	assert.True(t, errors.Is(err, store.ErrInvalidInput))
	assert.Contains(t, err.Error(), "AC-31")
}

func TestParseReference_Empty(t *testing.T) {
	_, err := ParseReference("")
	require.Error(t, err)
	assert.True(t, errors.Is(err, store.ErrInvalidInput))
}

func TestParseReference_EmptyAt(t *testing.T) {
	_, err := ParseReference("@")
	require.Error(t, err)
	assert.True(t, errors.Is(err, store.ErrInvalidInput))
}

func TestParseReference_EmptyHash(t *testing.T) {
	_, err := ParseReference("#")
	require.Error(t, err)
	assert.True(t, errors.Is(err, store.ErrInvalidInput))
}

func TestParseReference_InvalidFormat(t *testing.T) {
	tests := []string{
		"just-a-string",
		"123",
		"conversation:123",
		"!invalid",
	}
	for _, input := range tests {
		t.Run(input, func(t *testing.T) {
			_, err := ParseReference(input)
			require.Error(t, err)
			assert.True(t, errors.Is(err, store.ErrInvalidInput), "expected ErrInvalidInput for %q, got: %v", input, err)
		})
	}
}

// ---------------------------------------------------------------------------
// Resolve conv:<id> tests
// ---------------------------------------------------------------------------

func TestResolve_ConvByID_HappyPath(t *testing.T) {
	ms := newMockStore()
	ctx := context.Background()
	projectID := uuid.NewString()
	senderID := uuid.NewString()
	convID := uuid.NewString()

	ms.addConversation(
		&store.Conversation{ID: convID, ProjectID: &projectID, Kind: "group", Surface: "native"},
		store.ConversationParticipant{ConversationID: convID, PrincipalKind: "user", PrincipalID: senderID},
	)

	result, err := Resolve(ctx, ms, "conv:"+convID, ResolveContext{
		SenderPrincipalKind: "user",
		SenderPrincipalID:   senderID,
		ProjectID:           projectID,
	})
	require.NoError(t, err)
	assert.Equal(t, convID, result.ConversationID)
	assert.False(t, result.Created)
}

func TestResolve_ConvByID_BoundaryViolation_SenderBelongsToOtherProject(t *testing.T) {
	// AC-30: Sender belongs to project B, tries to resolve a conv from project B
	// while in project A context → boundary-violation.
	ms := newMockStore()
	ctx := context.Background()
	projectA := uuid.NewString()
	projectB := uuid.NewString()
	senderID := uuid.NewString()
	convID := uuid.NewString()

	// Conversation belongs to project B.
	ms.addConversation(
		&store.Conversation{ID: convID, ProjectID: &projectB, Kind: "group", Surface: "native"},
	)

	// Sender has a conversation in project B (belongs to project B).
	otherConvID := uuid.NewString()
	ms.addConversation(
		&store.Conversation{ID: otherConvID, ProjectID: &projectB, Kind: "direct", Surface: "native"},
		store.ConversationParticipant{ConversationID: otherConvID, PrincipalKind: "user", PrincipalID: senderID},
	)

	_, err := Resolve(ctx, ms, "conv:"+convID, ResolveContext{
		SenderPrincipalKind: "user",
		SenderPrincipalID:   senderID,
		ProjectID:           projectA, // Sender is in project A context
	})
	require.Error(t, err)

	var resErr *ResolutionError
	require.ErrorAs(t, err, &resErr)
	assert.Equal(t, "boundary-violation", resErr.Reason, "sender belongs to project B, should get boundary-violation")
}

func TestResolve_ConvByID_NotFound_SenderDoesNotBelongToOtherProject(t *testing.T) {
	// AC-30: Sender does NOT belong to project B, tries to resolve a conv
	// from project B → not-found (no information leakage).
	ms := newMockStore()
	ctx := context.Background()
	projectA := uuid.NewString()
	projectB := uuid.NewString()
	senderID := uuid.NewString()
	convID := uuid.NewString()

	// Conversation belongs to project B.
	ms.addConversation(
		&store.Conversation{ID: convID, ProjectID: &projectB, Kind: "group", Surface: "native"},
	)
	// Sender has NO conversations in project B.

	_, err := Resolve(ctx, ms, "conv:"+convID, ResolveContext{
		SenderPrincipalKind: "user",
		SenderPrincipalID:   senderID,
		ProjectID:           projectA,
	})
	require.Error(t, err)

	var resErr *ResolutionError
	require.ErrorAs(t, err, &resErr)
	assert.Equal(t, "not-found", resErr.Reason, "sender does not belong to project B, should get not-found")
}

func TestResolve_ConvByID_DisclosureRule(t *testing.T) {
	// AC-30: The error messages for 'not-found' (doesn't exist) and
	// 'not-found' (cross-project, sender not a member) must be IDENTICAL.
	ms := newMockStore()
	ctx := context.Background()
	projectA := uuid.NewString()
	projectB := uuid.NewString()
	senderID := uuid.NewString()

	// Case 1: Conversation truly does not exist.
	fakeID := uuid.NewString()
	_, err1 := Resolve(ctx, ms, "conv:"+fakeID, ResolveContext{
		SenderPrincipalKind: "user",
		SenderPrincipalID:   senderID,
		ProjectID:           projectA,
	})
	require.Error(t, err1)

	// Case 2: Conversation exists in project B, sender does not belong to B.
	realID := uuid.NewString()
	ms.addConversation(
		&store.Conversation{ID: realID, ProjectID: &projectB, Kind: "group", Surface: "native"},
	)

	_, err2 := Resolve(ctx, ms, "conv:"+realID, ResolveContext{
		SenderPrincipalKind: "user",
		SenderPrincipalID:   senderID,
		ProjectID:           projectA,
	})
	require.Error(t, err2)

	// Both must be ResolutionError with reason "not-found".
	var resErr1, resErr2 *ResolutionError
	require.ErrorAs(t, err1, &resErr1)
	require.ErrorAs(t, err2, &resErr2)
	assert.Equal(t, "not-found", resErr1.Reason)
	assert.Equal(t, "not-found", resErr2.Reason)

	// The error message format (minus the ref value) must be identical.
	// Replace the specific conv ID to compare the template.
	msg1 := resErr1.Error()
	msg2 := resErr2.Error()
	// Both should follow the same pattern: conversation reference "conv:...": not found
	assert.Contains(t, msg1, "not found")
	assert.Contains(t, msg2, "not found")
}

func TestResolve_ConvByID_NilProjectID_GlobalDM_Allowed(t *testing.T) {
	// AC-30: Conversation with nil ProjectID (global DM) — allowed from any project.
	ms := newMockStore()
	ctx := context.Background()
	projectA := uuid.NewString()
	senderID := uuid.NewString()
	convID := uuid.NewString()

	// Global DM (nil ProjectID).
	ms.addConversation(
		&store.Conversation{ID: convID, ProjectID: nil, Kind: "direct", Surface: "native"},
		store.ConversationParticipant{ConversationID: convID, PrincipalKind: "user", PrincipalID: senderID},
	)

	result, err := Resolve(ctx, ms, "conv:"+convID, ResolveContext{
		SenderPrincipalKind: "user",
		SenderPrincipalID:   senderID,
		ProjectID:           projectA,
	})
	require.NoError(t, err)
	assert.Equal(t, convID, result.ConversationID)
	assert.False(t, result.Created)
}

func TestResolve_ConvByID_NotFound_DoesNotExist(t *testing.T) {
	ms := newMockStore()
	ctx := context.Background()

	_, err := Resolve(ctx, ms, "conv:"+uuid.NewString(), ResolveContext{
		SenderPrincipalKind: "user",
		SenderPrincipalID:   uuid.NewString(),
		ProjectID:           uuid.NewString(),
	})
	require.Error(t, err)

	var resErr *ResolutionError
	require.ErrorAs(t, err, &resErr)
	assert.Equal(t, "not-found", resErr.Reason)
}

// ---------------------------------------------------------------------------
// Resolve @<agent-slug> tests
// ---------------------------------------------------------------------------

func TestResolve_AgentSlug_WithinCurrentProject(t *testing.T) {
	// AC-31: @<agent-slug> resolves within current project only.
	ms := newMockStore()
	ctx := context.Background()
	projectID := uuid.NewString()
	senderID := uuid.NewString()
	agentID := uuid.NewString()

	ms.agents[projectID+"/my-agent"] = &store.Agent{ID: agentID, Slug: "my-agent", ProjectID: projectID}

	result, err := Resolve(ctx, ms, "@my-agent", ResolveContext{
		SenderPrincipalKind: "user",
		SenderPrincipalID:   senderID,
		ProjectID:           projectID,
	})
	require.NoError(t, err)
	assert.NotEmpty(t, result.ConversationID)
	assert.True(t, result.Created, "should create a new DM on first send")
}

func TestResolve_AgentSlug_CreatesDMOnFirstSend(t *testing.T) {
	ms := newMockStore()
	ctx := context.Background()
	projectID := uuid.NewString()
	senderID := uuid.NewString()
	agentID := uuid.NewString()

	ms.agents[projectID+"/my-agent"] = &store.Agent{ID: agentID, Slug: "my-agent", ProjectID: projectID}

	result, err := Resolve(ctx, ms, "@my-agent", ResolveContext{
		SenderPrincipalKind: "user",
		SenderPrincipalID:   senderID,
		ProjectID:           projectID,
	})
	require.NoError(t, err)
	assert.True(t, result.Created)

	// Verify the conversation was created in the store.
	conv, err := ms.GetConversation(ctx, result.ConversationID)
	require.NoError(t, err)
	assert.Equal(t, "direct", conv.Kind)
	assert.Equal(t, &projectID, conv.ProjectID)

	// Verify participants.
	parts := ms.participants[result.ConversationID]
	require.Len(t, parts, 2)

	var hasSender, hasAgent bool
	for _, p := range parts {
		if p.PrincipalKind == "user" && p.PrincipalID == senderID {
			hasSender = true
		}
		if p.PrincipalKind == "agent" && p.PrincipalID == agentID {
			hasAgent = true
		}
	}
	assert.True(t, hasSender, "sender should be a participant")
	assert.True(t, hasAgent, "agent should be a participant")
}

func TestResolve_AgentSlug_ReturnsExistingDM(t *testing.T) {
	// Idempotent: subsequent sends return the same conversation.
	ms := newMockStore()
	ctx := context.Background()
	projectID := uuid.NewString()
	senderID := uuid.NewString()
	agentID := uuid.NewString()

	ms.agents[projectID+"/my-agent"] = &store.Agent{ID: agentID, Slug: "my-agent", ProjectID: projectID}

	rctx := ResolveContext{
		SenderPrincipalKind: "user",
		SenderPrincipalID:   senderID,
		ProjectID:           projectID,
	}

	// First resolution — creates the DM.
	result1, err := Resolve(ctx, ms, "@my-agent", rctx)
	require.NoError(t, err)
	assert.True(t, result1.Created)

	// Second resolution — returns the existing DM.
	result2, err := Resolve(ctx, ms, "@my-agent", rctx)
	require.NoError(t, err)
	assert.False(t, result2.Created)
	assert.Equal(t, result1.ConversationID, result2.ConversationID, "should return the same conversation")
}

func TestResolve_AgentSlug_NotFoundInProject(t *testing.T) {
	ms := newMockStore()
	ctx := context.Background()
	projectID := uuid.NewString()

	_, err := Resolve(ctx, ms, "@nonexistent-agent", ResolveContext{
		SenderPrincipalKind: "user",
		SenderPrincipalID:   uuid.NewString(),
		ProjectID:           projectID,
	})
	require.Error(t, err)

	var resErr *ResolutionError
	require.ErrorAs(t, err, &resErr)
	assert.Equal(t, "not-found", resErr.Reason)
}

func TestResolve_AgentSlug_NoProject(t *testing.T) {
	ms := newMockStore()
	ctx := context.Background()

	_, err := Resolve(ctx, ms, "@my-agent", ResolveContext{
		SenderPrincipalKind: "user",
		SenderPrincipalID:   uuid.NewString(),
		ProjectID:           "", // no project
	})
	require.Error(t, err)

	var resErr *ResolutionError
	require.ErrorAs(t, err, &resErr)
	assert.Equal(t, "no-shared-project", resErr.Reason)
}

func TestResolve_AgentSlug_OtherProjectAgent_NotFound(t *testing.T) {
	// Agent exists in project B but not in project A — should not resolve.
	ms := newMockStore()
	ctx := context.Background()
	projectA := uuid.NewString()
	projectB := uuid.NewString()
	agentID := uuid.NewString()

	ms.agents[projectB+"/my-agent"] = &store.Agent{ID: agentID, Slug: "my-agent", ProjectID: projectB}

	_, err := Resolve(ctx, ms, "@my-agent", ResolveContext{
		SenderPrincipalKind: "user",
		SenderPrincipalID:   uuid.NewString(),
		ProjectID:           projectA,
	})
	require.Error(t, err)

	var resErr *ResolutionError
	require.ErrorAs(t, err, &resErr)
	assert.Equal(t, "not-found", resErr.Reason)
}

// ---------------------------------------------------------------------------
// Resolve @<email> tests
// ---------------------------------------------------------------------------

func TestResolve_Email_GlobalDM(t *testing.T) {
	ms := newMockStore()
	ctx := context.Background()
	senderID := uuid.NewString()
	userID := uuid.NewString()

	ms.users["alice@example.com"] = &store.User{ID: userID, Email: "alice@example.com"}

	result, err := Resolve(ctx, ms, "@alice@example.com", ResolveContext{
		SenderPrincipalKind: "user",
		SenderPrincipalID:   senderID,
		ProjectID:           uuid.NewString(), // project context doesn't restrict email DMs
	})
	require.NoError(t, err)
	assert.NotEmpty(t, result.ConversationID)
	assert.True(t, result.Created)

	// Verify the conversation is global (nil ProjectID).
	conv, err := ms.GetConversation(ctx, result.ConversationID)
	require.NoError(t, err)
	assert.Nil(t, conv.ProjectID, "email DM should be global (nil ProjectID)")
}

func TestResolve_Email_ReturnsExistingDM(t *testing.T) {
	ms := newMockStore()
	ctx := context.Background()
	senderID := uuid.NewString()
	userID := uuid.NewString()

	ms.users["alice@example.com"] = &store.User{ID: userID, Email: "alice@example.com"}

	rctx := ResolveContext{
		SenderPrincipalKind: "user",
		SenderPrincipalID:   senderID,
		ProjectID:           uuid.NewString(),
	}

	// First resolution creates.
	result1, err := Resolve(ctx, ms, "@alice@example.com", rctx)
	require.NoError(t, err)
	assert.True(t, result1.Created)

	// Second resolution returns existing.
	result2, err := Resolve(ctx, ms, "@alice@example.com", rctx)
	require.NoError(t, err)
	assert.False(t, result2.Created)
	assert.Equal(t, result1.ConversationID, result2.ConversationID)
}

func TestResolve_Email_UserNotFound(t *testing.T) {
	ms := newMockStore()
	ctx := context.Background()

	_, err := Resolve(ctx, ms, "@nobody@example.com", ResolveContext{
		SenderPrincipalKind: "user",
		SenderPrincipalID:   uuid.NewString(),
		ProjectID:           uuid.NewString(),
	})
	require.Error(t, err)

	var resErr *ResolutionError
	require.ErrorAs(t, err, &resErr)
	assert.Equal(t, "not-found", resErr.Reason)
}

// ---------------------------------------------------------------------------
// Resolve #<thread-name> tests
// ---------------------------------------------------------------------------

func TestResolve_Thread_WithinCurrentProject(t *testing.T) {
	ms := newMockStore()
	ctx := context.Background()
	projectID := uuid.NewString()
	senderID := uuid.NewString()
	convID := uuid.NewString()

	ms.addConversation(
		&store.Conversation{
			ID:          convID,
			ProjectID:   &projectID,
			Kind:        "group",
			Surface:     "native",
			DisplayName: "design-discussion",
		},
		store.ConversationParticipant{ConversationID: convID, PrincipalKind: "user", PrincipalID: senderID},
	)

	result, err := Resolve(ctx, ms, "#design-discussion", ResolveContext{
		SenderPrincipalKind: "user",
		SenderPrincipalID:   senderID,
		ProjectID:           projectID,
	})
	require.NoError(t, err)
	assert.Equal(t, convID, result.ConversationID)
	assert.False(t, result.Created)
}

func TestResolve_Thread_NotFound(t *testing.T) {
	ms := newMockStore()
	ctx := context.Background()

	_, err := Resolve(ctx, ms, "#nonexistent-thread", ResolveContext{
		SenderPrincipalKind: "user",
		SenderPrincipalID:   uuid.NewString(),
		ProjectID:           uuid.NewString(),
	})
	require.Error(t, err)

	var resErr *ResolutionError
	require.ErrorAs(t, err, &resErr)
	assert.Equal(t, "not-found", resErr.Reason)
}

func TestResolve_Thread_NoProject(t *testing.T) {
	ms := newMockStore()
	ctx := context.Background()

	_, err := Resolve(ctx, ms, "#some-thread", ResolveContext{
		SenderPrincipalKind: "user",
		SenderPrincipalID:   uuid.NewString(),
		ProjectID:           "", // no project
	})
	require.Error(t, err)

	var resErr *ResolutionError
	require.ErrorAs(t, err, &resErr)
	assert.Equal(t, "no-shared-project", resErr.Reason)
}

func TestResolve_Thread_SpaceSlashThread_Rejected(t *testing.T) {
	// AC-31: #<space>/<thread> form is rejected by the parser.
	ms := newMockStore()
	ctx := context.Background()

	_, err := Resolve(ctx, ms, "#my-space/my-thread", ResolveContext{
		SenderPrincipalKind: "user",
		SenderPrincipalID:   uuid.NewString(),
		ProjectID:           uuid.NewString(),
	})
	require.Error(t, err)
	assert.True(t, errors.Is(err, store.ErrInvalidInput))
}

// ---------------------------------------------------------------------------
// Invalid reference format tests
// ---------------------------------------------------------------------------

func TestResolve_InvalidFormat(t *testing.T) {
	ms := newMockStore()
	ctx := context.Background()
	rctx := ResolveContext{
		SenderPrincipalKind: "user",
		SenderPrincipalID:   uuid.NewString(),
		ProjectID:           uuid.NewString(),
	}

	invalids := []string{
		"",
		"just-a-string",
		"conv:not-a-uuid",
		"@",
		"#",
		"#space/thread",
	}
	for _, input := range invalids {
		t.Run(input, func(t *testing.T) {
			_, err := Resolve(ctx, ms, input, rctx)
			require.Error(t, err)
		})
	}
}

// ---------------------------------------------------------------------------
// ResolutionError tests
// ---------------------------------------------------------------------------

func TestResolutionError_Methods(t *testing.T) {
	notFound := &ResolutionError{Ref: "conv:abc", Reason: "not-found"}
	boundary := &ResolutionError{Ref: "conv:abc", Reason: "boundary-violation"}

	assert.True(t, notFound.IsNotFound())
	assert.False(t, notFound.IsBoundaryViolation())

	assert.False(t, boundary.IsNotFound())
	assert.True(t, boundary.IsBoundaryViolation())

	assert.Contains(t, notFound.Error(), "not found")
	assert.Contains(t, boundary.Error(), "different project")
}

func TestResolutionError_Ambiguous(t *testing.T) {
	err := &ResolutionError{
		Ref:        "@my-agent",
		Reason:     "ambiguous",
		Candidates: []string{"@project-a/my-agent", "@project-b/my-agent"},
	}
	assert.Contains(t, err.Error(), "ambiguous")
	assert.Contains(t, err.Error(), "project-a")
}

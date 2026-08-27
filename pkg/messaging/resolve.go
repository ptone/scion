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
	"fmt"
	"strings"

	"github.com/GoogleCloudPlatform/scion/pkg/store"
	"github.com/google/uuid"
)

// ---------------------------------------------------------------------------
// Reference grammar (§2.6)
// ---------------------------------------------------------------------------

// ReferenceKind identifies which form a parsed reference takes.
type ReferenceKind int

const (
	// RefConversation is a canonical conv:<id> reference.
	RefConversation ReferenceKind = iota + 1
	// RefAgent is an @<agent-slug> reference (resolved within current project).
	RefAgent
	// RefEmail is an @<email> reference (global DM).
	RefEmail
	// RefThread is a #<thread-name> reference (group conversation in project space).
	RefThread
)

// Reference is the structured result of parsing a conversation reference string.
type Reference struct {
	Kind  ReferenceKind
	Value string // the parsed value: UUID, slug, email, or thread name
	Raw   string // the original input string
}

// ParseReference parses a conversation reference string into a structured
// Reference. It accepts exactly four forms:
//
//	conv:<uuid>      → RefConversation
//	@<agent-slug>    → RefAgent (no '@' in slug)
//	@<email>         → RefEmail (contains '@' after the leading '@')
//	#<thread-name>   → RefThread (no '/' allowed — §2.6.1)
//
// Returns an error for any other form.
func ParseReference(raw string) (*Reference, error) {
	if raw == "" {
		return nil, fmt.Errorf("empty reference: %w", store.ErrInvalidInput)
	}

	switch {
	case strings.HasPrefix(raw, "conv:"):
		id := strings.TrimPrefix(raw, "conv:")
		if _, err := uuid.Parse(id); err != nil {
			return nil, fmt.Errorf("invalid conversation ID in %q: %w", raw, store.ErrInvalidInput)
		}
		return &Reference{Kind: RefConversation, Value: id, Raw: raw}, nil

	case strings.HasPrefix(raw, "@"):
		value := raw[1:]
		if value == "" {
			return nil, fmt.Errorf("empty @ reference: %w", store.ErrInvalidInput)
		}
		// If it contains '@' after the leading '@', treat as email.
		if strings.Contains(value, "@") {
			return &Reference{Kind: RefEmail, Value: value, Raw: raw}, nil
		}
		return &Reference{Kind: RefAgent, Value: value, Raw: raw}, nil

	case strings.HasPrefix(raw, "#"):
		value := raw[1:]
		if value == "" {
			return nil, fmt.Errorf("empty # reference: %w", store.ErrInvalidInput)
		}
		// §2.6.1: reject #<space>/<thread> form.
		if strings.Contains(value, "/") {
			return nil, fmt.Errorf("invalid thread reference %q: #<space>/<thread> form is not supported (AC-31): %w", raw, store.ErrInvalidInput)
		}
		return &Reference{Kind: RefThread, Value: value, Raw: raw}, nil

	default:
		return nil, fmt.Errorf("unrecognized reference format %q: %w", raw, store.ErrInvalidInput)
	}
}

// ---------------------------------------------------------------------------
// Resolution context and result types
// ---------------------------------------------------------------------------

// ResolveContext carries caller-provided context needed for resolution.
type ResolveContext struct {
	SenderPrincipalKind string // "user" or "agent"
	SenderPrincipalID   string // UUID of the sender
	ProjectID           string // current project (may be empty for global DMs)
}

// ResolveResult is the outcome of a successful resolution.
type ResolveResult struct {
	ConversationID string
	Created        bool              // true if a new conversation was created
	Unresolved     []UnresolvedRef   // mentions that could not be resolved (non-fatal in DMs)
}

// UnresolvedRef describes a reference that could not be resolved.
type UnresolvedRef struct {
	Text       string   // the original reference text
	Reason     string   // "ambiguous" | "not-found" | "no-shared-project" | "not-a-participant" | "boundary-violation"
	Candidates []string // qualified forms that would work (for ambiguous)
}

// ---------------------------------------------------------------------------
// Resolution store interface
// ---------------------------------------------------------------------------

// ResolutionStore defines the subset of store methods needed by the resolver.
// It decouples the resolution logic from the full store.Store.
type ResolutionStore interface {
	// Conversation CRUD
	GetConversation(ctx context.Context, id string) (*store.Conversation, error)
	CreateConversation(ctx context.Context, conv *store.Conversation) error
	ListConversations(ctx context.Context, filter store.ConversationFilter, opts store.ListOptions) (*store.ListResult[store.Conversation], error)

	// Participants
	AddParticipant(ctx context.Context, p *store.ConversationParticipant) error
	ListParticipants(ctx context.Context, conversationID string) ([]store.ConversationParticipant, error)
	GetConversationsForPrincipal(ctx context.Context, principalKind, principalID string) ([]store.Conversation, error)

	// Agent resolution
	GetAgentBySlug(ctx context.Context, projectID, slug string) (*store.Agent, error)

	// User resolution
	GetUserByEmail(ctx context.Context, email string) (*store.User, error)
}

// ---------------------------------------------------------------------------
// Resolve function
// ---------------------------------------------------------------------------

// Resolve resolves a conversation reference string to a conversation ID,
// creating the conversation if needed (resolve-or-create for @ references).
//
// Project isolation rules (§2.6.1, AC-30):
//   - conv:<id>: if the conversation belongs to a different project, the error
//     distinguishes between boundary-violation (sender belongs to that project)
//     and not-found (sender does not, preventing information leakage).
//   - @<agent-slug>: resolved only within rctx.ProjectID.
//   - @<email>: global DM, no project restriction.
//   - #<thread-name>: resolved only within rctx.ProjectID's space.
func Resolve(ctx context.Context, s ResolutionStore, ref string, rctx ResolveContext) (*ResolveResult, error) {
	parsed, err := ParseReference(ref)
	if err != nil {
		return nil, err
	}

	switch parsed.Kind {
	case RefConversation:
		return resolveConvByID(ctx, s, parsed.Value, rctx)
	case RefAgent:
		return resolveAgentDM(ctx, s, parsed.Value, rctx)
	case RefEmail:
		return resolveEmailDM(ctx, s, parsed.Value, rctx)
	case RefThread:
		return resolveThread(ctx, s, parsed.Value, rctx)
	default:
		return nil, fmt.Errorf("unsupported reference kind: %w", store.ErrInvalidInput)
	}
}

// resolveConvByID resolves a conv:<id> reference with project isolation checks.
func resolveConvByID(ctx context.Context, s ResolutionStore, id string, rctx ResolveContext) (*ResolveResult, error) {
	conv, err := s.GetConversation(ctx, id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, &ResolutionError{
				Ref:    "conv:" + id,
				Reason: "not-found",
			}
		}
		return nil, fmt.Errorf("fetching conversation %s: %w", id, err)
	}

	// Project isolation check (AC-30).
	if conv.ProjectID != nil && *conv.ProjectID != "" {
		if rctx.ProjectID != *conv.ProjectID {
			// The conversation belongs to a different project.
			// Check if the sender belongs to that other project.
			if senderBelongsToProject(ctx, s, rctx.SenderPrincipalKind, rctx.SenderPrincipalID, *conv.ProjectID) {
				// Sender can see that project — tell them it's a boundary violation.
				return nil, &ResolutionError{
					Ref:    "conv:" + id,
					Reason: "boundary-violation",
				}
			}
			// Sender does NOT belong to that project — return not-found to
			// prevent information leakage. The error message must be
			// identical to a genuine not-found.
			return nil, &ResolutionError{
				Ref:    "conv:" + id,
				Reason: "not-found",
			}
		}
	}
	// nil ProjectID means global DM — always allowed.

	return &ResolveResult{
		ConversationID: conv.ID,
		Created:        false,
	}, nil
}

// senderBelongsToProject checks whether the sender participates in any
// conversation within the given project. This is a heuristic proxy for
// "belongs to project" — a sender with at least one conversation in the
// project is considered a member.
func senderBelongsToProject(ctx context.Context, s ResolutionStore, principalKind, principalID, projectID string) bool {
	convs, err := s.GetConversationsForPrincipal(ctx, principalKind, principalID)
	if err != nil {
		return false
	}
	for _, c := range convs {
		if c.ProjectID != nil && *c.ProjectID == projectID {
			return true
		}
	}
	return false
}

// resolveAgentDM resolves an @<agent-slug> reference to a direct conversation.
// Creates the conversation if it doesn't exist (resolve-or-create).
func resolveAgentDM(ctx context.Context, s ResolutionStore, slug string, rctx ResolveContext) (*ResolveResult, error) {
	if rctx.ProjectID == "" {
		return nil, &ResolutionError{
			Ref:    "@" + slug,
			Reason: "no-shared-project",
		}
	}

	// Resolve the agent slug within the current project.
	agent, err := s.GetAgentBySlug(ctx, rctx.ProjectID, slug)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, &ResolutionError{
				Ref:    "@" + slug,
				Reason: "not-found",
			}
		}
		return nil, fmt.Errorf("resolving agent slug %q: %w", slug, err)
	}

	// Look for an existing direct conversation between sender and this agent
	// in the current project.
	existing, err := findDirectConversation(ctx, s, rctx, "agent", agent.ID)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		return &ResolveResult{
			ConversationID: existing.ID,
			Created:        false,
		}, nil
	}

	// Create a new direct conversation.
	conv, err := createDirectConversation(ctx, s, rctx, "agent", agent.ID)
	if err != nil {
		return nil, err
	}
	return &ResolveResult{
		ConversationID: conv.ID,
		Created:        true,
	}, nil
}

// resolveEmailDM resolves an @<email> reference to a global direct conversation.
// Creates the conversation if it doesn't exist.
func resolveEmailDM(ctx context.Context, s ResolutionStore, email string, rctx ResolveContext) (*ResolveResult, error) {
	// Resolve the user by email.
	user, err := s.GetUserByEmail(ctx, email)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, &ResolutionError{
				Ref:    "@" + email,
				Reason: "not-found",
			}
		}
		return nil, fmt.Errorf("resolving user email %q: %w", email, err)
	}

	// Email DMs are global — no project restriction. Pass an empty project
	// context so findDirectConversation matches conversations without a
	// project ID.
	globalCtx := ResolveContext{
		SenderPrincipalKind: rctx.SenderPrincipalKind,
		SenderPrincipalID:   rctx.SenderPrincipalID,
		ProjectID:           "", // global
	}

	existing, err := findDirectConversation(ctx, s, globalCtx, "user", user.ID)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		return &ResolveResult{
			ConversationID: existing.ID,
			Created:        false,
		}, nil
	}

	conv, err := createDirectConversation(ctx, s, globalCtx, "user", user.ID)
	if err != nil {
		return nil, err
	}
	return &ResolveResult{
		ConversationID: conv.ID,
		Created:        true,
	}, nil
}

// resolveThread resolves a #<thread-name> reference within the current project.
func resolveThread(ctx context.Context, s ResolutionStore, name string, rctx ResolveContext) (*ResolveResult, error) {
	if rctx.ProjectID == "" {
		return nil, &ResolutionError{
			Ref:    "#" + name,
			Reason: "no-shared-project",
		}
	}

	// Paginate through all group conversations in this project to find one
	// matching the display name. A previous implementation passed Limit:0
	// expecting "no limit", but clampLimit(0) returns 50 (the default page
	// size), silently missing threads beyond the first page.
	var cursor string
	for {
		result, err := s.ListConversations(ctx, store.ConversationFilter{
			ProjectID: rctx.ProjectID,
			Kind:      "group",
		}, store.ListOptions{Limit: 100, Cursor: cursor})
		if err != nil {
			return nil, fmt.Errorf("listing conversations in project %s: %w", rctx.ProjectID, err)
		}

		for _, c := range result.Items {
			if c.DisplayName == name {
				return &ResolveResult{
					ConversationID: c.ID,
					Created:        false,
				}, nil
			}
		}

		if result.NextCursor == "" {
			break
		}
		cursor = result.NextCursor
	}

	return nil, &ResolutionError{
		Ref:    "#" + name,
		Reason: "not-found",
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// findDirectConversation finds an existing direct conversation between the
// sender and a target principal in the given project context.
func findDirectConversation(ctx context.Context, s ResolutionStore, rctx ResolveContext, targetKind, targetID string) (*store.Conversation, error) {
	// Get all conversations the sender participates in.
	senderConvs, err := s.GetConversationsForPrincipal(ctx, rctx.SenderPrincipalKind, rctx.SenderPrincipalID)
	if err != nil {
		return nil, fmt.Errorf("listing sender conversations: %w", err)
	}

	for _, conv := range senderConvs {
		if conv.Kind != "direct" {
			continue
		}

		// Match project context.
		if rctx.ProjectID == "" {
			// Global DM — only match conversations without a project.
			if conv.ProjectID != nil && *conv.ProjectID != "" {
				continue
			}
		} else {
			if conv.ProjectID == nil || *conv.ProjectID != rctx.ProjectID {
				continue
			}
		}

		// Check if the target is a participant.
		participants, err := s.ListParticipants(ctx, conv.ID)
		if err != nil {
			return nil, fmt.Errorf("listing participants for conversation %s: %w", conv.ID, err)
		}
		for _, p := range participants {
			if p.PrincipalKind == targetKind && p.PrincipalID == targetID {
				return &conv, nil
			}
		}
	}

	return nil, nil
}

// createDirectConversation creates a new direct conversation between the sender
// and a target principal, adding both as participants.
func createDirectConversation(ctx context.Context, s ResolutionStore, rctx ResolveContext, targetKind, targetID string) (*store.Conversation, error) {
	conv := &store.Conversation{
		ID:         uuid.NewString(),
		Kind:       "direct",
		Surface:    "native",
		DriftState: DriftStateActive,
	}

	if rctx.ProjectID != "" {
		conv.ProjectID = &rctx.ProjectID
	}

	if err := s.CreateConversation(ctx, conv); err != nil {
		return nil, fmt.Errorf("creating direct conversation: %w", err)
	}

	// Add sender as participant.
	if err := s.AddParticipant(ctx, &store.ConversationParticipant{
		ID:             uuid.NewString(),
		ConversationID: conv.ID,
		PrincipalKind:  rctx.SenderPrincipalKind,
		PrincipalID:    rctx.SenderPrincipalID,
		Role:           "member",
	}); err != nil {
		return nil, fmt.Errorf("adding sender as participant: %w", err)
	}

	// Add target as participant.
	if err := s.AddParticipant(ctx, &store.ConversationParticipant{
		ID:             uuid.NewString(),
		ConversationID: conv.ID,
		PrincipalKind:  targetKind,
		PrincipalID:    targetID,
		Role:           "member",
	}); err != nil {
		return nil, fmt.Errorf("adding target as participant: %w", err)
	}

	return conv, nil
}

// ---------------------------------------------------------------------------
// Error types
// ---------------------------------------------------------------------------

// ResolutionError represents a resolution failure with a categorized reason.
type ResolutionError struct {
	Ref        string   // the reference that failed to resolve
	Reason     string   // "not-found" | "boundary-violation" | "ambiguous" | "no-shared-project" | "not-a-participant"
	Candidates []string // qualified forms that would work (for ambiguous)
}

func (e *ResolutionError) Error() string {
	switch e.Reason {
	case "not-found":
		return fmt.Sprintf("conversation reference %q: not found", e.Ref)
	case "boundary-violation":
		return fmt.Sprintf("conversation reference %q: belongs to a different project", e.Ref)
	case "ambiguous":
		return fmt.Sprintf("conversation reference %q: ambiguous, candidates: %v", e.Ref, e.Candidates)
	case "no-shared-project":
		return fmt.Sprintf("conversation reference %q: requires a project context", e.Ref)
	case "not-a-participant":
		return fmt.Sprintf("conversation reference %q: sender is not a participant", e.Ref)
	default:
		return fmt.Sprintf("conversation reference %q: %s", e.Ref, e.Reason)
	}
}

// IsNotFound returns true if the error is a resolution not-found error.
func (e *ResolutionError) IsNotFound() bool {
	return e.Reason == "not-found"
}

// IsBoundaryViolation returns true if the error is a project boundary violation.
func (e *ResolutionError) IsBoundaryViolation() bool {
	return e.Reason == "boundary-violation"
}

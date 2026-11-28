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

import (
	"context"
	"net/http"
	"time"

	"github.com/GoogleCloudPlatform/scion/pkg/experiments"
	"github.com/GoogleCloudPlatform/scion/pkg/store"
)

// conversationLifecycle implements conversation delete (and, in subsequent
// phases, archive, unarchive, group lifecycle, and bulk cleanup) behind one
// service boundary so canonical /api/v1/conversations routes and topic routes
// share authorization, store operations, side effects, and events.
type conversationLifecycle struct {
	s *Server
}

func (s *Server) lifecycle() *conversationLifecycle {
	return &conversationLifecycle{s: s}
}

// handleDeleteConversation handles DELETE /api/v1/conversations/{id}.
func (s *Server) handleDeleteConversation(w http.ResponseWriter, r *http.Request, id string) {
	s.lifecycle().Delete(w, r, id)
}

// Delete soft-deletes a conversation by UUID following the design §3.4
// authorization order:
//  1. Experiment hub.conversation_lifecycle enabled (else 404).
//  2. User principal only, via session or UAT (agents get 403).
//  3. Resolve conversation and verify caller read visibility (else 404).
//  4. Require surface == "native" (else 409 unsupported_surface); in Phase 1
//     group conversations return 409 unsupported_conversation.
//  5. UAT write scope: inbox:write on the conversation's boundary + agent:read
//     on the peer agent for DMs (else 403).
//  6. Action check (canonical participation for DMs).
func (l *conversationLifecycle) Delete(w http.ResponseWriter, r *http.Request, id string) {
	ctx := r.Context()

	// 1. Experiment gate.
	if !l.s.experimentEnabled(experiments.ConversationLifecycle) {
		NotFound(w, "Conversation")
		return
	}

	// 2. User principal only (agents are refused 403, independent of CLI gate).
	user := GetUserIdentityFromContext(ctx)
	if user == nil {
		Forbidden(w)
		return
	}
	cls, token, ok := requireInboxCredential(w, r)
	if !ok {
		return
	}

	// 3. Resolve conversation and check read visibility (missing/deleted/invisible -> 404).
	conv, ok := l.resolveVisibleConversation(w, r, user, id)
	if !ok {
		return
	}

	// 4. Surface and kind gates.
	if conv.Surface != "native" {
		writeError(w, http.StatusConflict, "unsupported_surface",
			"conversation lifecycle actions are only supported for native conversations", nil)
		return
	}
	if conv.Kind != "direct" {
		writeError(w, http.StatusConflict, "unsupported_conversation",
			"group conversation delete via /api/v1/conversations is not yet supported", nil)
		return
	}

	// 5. UAT write scope (inbox:write on peer agent's project or hub boundary + agent:read on peer).
	if cls == inboxCredentialToken && !l.authorizeTokenConversationWrite(w, r, token, conv) {
		return
	}

	// 6. Action check: for DMs, canonical participation was already verified in step 3.
	if err := l.deleteNativeDM(ctx, conv.ExternalRef, conv.ID, nil); err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL", "failed to delete conversation", nil)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// resolveVisibleConversation loads a live conversation by ID and confirms the
// user can read it; missing, soft-deleted, or unreadable conversations all
// answer 404 so the route is never an existence oracle.
func (l *conversationLifecycle) resolveVisibleConversation(w http.ResponseWriter, r *http.Request, user UserIdentity, id string) (*store.Conversation, bool) {
	ctx := r.Context()
	conv, err := l.s.store.GetConversation(ctx, id)
	if err != nil {
		writeErrorFromErr(w, err, "Conversation")
		return nil, false
	}

	if conv.Kind == "direct" {
		if !authorizeDMRead(conv, user.Type(), user.ID()) {
			NotFound(w, "Conversation")
			return nil, false
		}
		return conv, true
	}

	canRead, err := l.s.canReadGroupConversation(ctx, user, conv)
	if err != nil {
		writeErrorFromErr(w, err, "")
		return nil, false
	}
	if !canRead {
		NotFound(w, "Conversation")
		return nil, false
	}
	return conv, true
}

// authorizeTokenConversationWrite checks that a scoped user access token holds
// inbox:write for conv (and agent:read on the peer agent for agent DMs).
func (l *conversationLifecycle) authorizeTokenConversationWrite(w http.ResponseWriter, r *http.Request, token *ScopedUserIdentity, conv *store.Conversation) bool {
	return l.s.authorizeTokenConversation(w, r, token, permInboxWrite, conv)
}

// deleteNativeDM soft-deletes a native DM conversation in the webchat store and
// runs post-commit cleanup (scheduled messages, stars, and live lifecycle event).
func (l *conversationLifecycle) deleteNativeDM(ctx context.Context, dmKey, fallbackConvID string, inactiveBefore *time.Time) error {
	l.s.mu.RLock()
	wcs := l.s.webChatStore
	l.s.mu.RUnlock()
	if wcs == nil {
		// Fallback when webchat store is not initialized (e.g. minimal hub tests):
		// still soft-delete the Ent conversation row.
		if fallbackConvID != "" {
			if err := l.s.store.DeleteConversation(ctx, fallbackConvID); err != nil {
				return err
			}
			l.s.events.PublishChatDMLifecycleEvent(ctx, dmKey, "deleted", fallbackConvID)
		}
		return nil
	}

	deletedConvID, err := wcs.SoftDeleteDM(ctx, dmKey, inactiveBefore)
	if err != nil {
		return err
	}
	if deletedConvID == "" {
		deletedConvID = fallbackConvID
	}

	// Best-effort post-commit side effects (logged on error).
	l.s.deleteScheduledMessagesOfConversation(ctx, dmKey)
	l.s.deleteMessageStarsOfConversation(ctx, dmKey)
	l.s.events.PublishChatDMLifecycleEvent(ctx, dmKey, "deleted", deletedConvID)
	return nil
}

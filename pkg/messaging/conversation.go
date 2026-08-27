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
	"log/slog"

	"github.com/GoogleCloudPlatform/scion/pkg/store"
)

// ConversationUpserter is the minimal interface needed by ResolveOrCreateDMConversation.
// It is satisfied by store.Store (which embeds ConversationStore).
type ConversationUpserter interface {
	UpsertConversationByExternalRef(ctx context.Context, conv *store.Conversation) (*store.Conversation, error)
}

// ResolveOrCreateDMConversation resolves (or creates) a direct-message
// conversation for the given sender/recipient pair and returns the conversation
// ID. The external_ref is deterministic: dm:{sorted(senderID, recipientID)}.
//
// On any error the function returns "" and logs the failure. Callers MUST NOT
// treat an empty return as fatal — message delivery continues without a
// conversation_id (Phase 5 non-fatal contract).
func ResolveOrCreateDMConversation(
	ctx context.Context,
	cs ConversationUpserter,
	log *slog.Logger,
	senderID, recipientID, projectID string,
) string {
	if senderID == "" || recipientID == "" {
		log.Warn("skipping conversation resolution: missing sender or recipient ID",
			"sender_id", senderID, "recipient_id", recipientID)
		return ""
	}

	extRef := DirectMessageExternalRef(senderID, recipientID)

	conv := &store.Conversation{
		Kind:        "direct",
		Surface:     "native",
		ExternalRef: extRef,
		DriftState:  DriftStateActive,
	}
	if projectID != "" {
		conv.ProjectID = &projectID
	}

	result, err := cs.UpsertConversationByExternalRef(ctx, conv)
	if err != nil {
		log.Error("conversation resolution failed (non-fatal)",
			"external_ref", extRef,
			"sender_id", senderID,
			"recipient_id", recipientID,
			"error", err,
		)
		return ""
	}

	return result.ID
}

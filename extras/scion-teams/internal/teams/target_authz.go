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

package teams

import (
	"context"
	"strings"

	"github.com/GoogleCloudPlatform/scion/pkg/messages"
	"github.com/GoogleCloudPlatform/scion/pkg/plugin"
)

// authorizeExplicitTarget reports whether container (a Teams conversation ID,
// optionally carrying a ";messageid=" thread suffix) may receive a message on
// a topic of projectID addressed to msg.Recipient. Fails closed on every error.
//
// Rule 1 (Linked): stripThreadSuffix(container) has an active ChannelLink with
// ProjectID == projectID.
// Rule 2 (Recipient DM): msg.Recipient is "user:<email>", container has a
// stored ConversationReference with ConversationType == "personal", the email
// maps to Teams user T, and container matches T's ConversationContext for
// (projectID, agentSlug) or latest ConversationContext for projectID.
func (b *TeamsBroker) authorizeExplicitTarget(ctx context.Context, projectID, agentSlug string,
	msg *messages.StructuredMessage, container string) (bool, plugin.TargetDenyReason) {
	if projectID == "" {
		return false, plugin.DenyNoProject
	}

	b.mu.Lock()
	store := b.store
	b.mu.Unlock()

	if store == nil {
		return false, plugin.DenyNoStore
	}

	base := stripThreadSuffix(container)
	link, err := store.GetChannelLink(ctx, base)
	if err != nil {
		b.log.Warn("Error looking up channel link for target authorization",
			"conversation_id", plugin.TruncateTargetID(base), "error", err)
		return false, plugin.DenyLookupError
	}
	if link != nil {
		if !link.Active {
			return false, plugin.DenyInactiveLink
		}
		if link.ProjectID != projectID {
			return false, plugin.DenyForeignProject
		}
		return true, ""
	}

	// Rule 2: Recipient personal DM chat.
	if msg == nil || !strings.HasPrefix(msg.Recipient, "user:") {
		return false, plugin.DenyNotLinked
	}

	ref, err := store.GetConversationReference(ctx, container)
	if err != nil {
		b.log.Warn("Error looking up conversation reference for target authorization",
			"conversation_id", plugin.TruncateTargetID(container), "error", err)
		return false, plugin.DenyLookupError
	}
	if ref == nil || ref.ConversationType != "personal" {
		return false, plugin.DenyNotLinked
	}

	email := strings.TrimPrefix(msg.Recipient, "user:")
	if email == "" {
		return false, plugin.DenyNotRecipientDM
	}
	mapping, err := store.GetUserMappingByEmail(ctx, email)
	if err != nil {
		b.log.Warn("Error looking up user mapping for target authorization", "error", err)
		return false, plugin.DenyLookupError
	}
	if mapping == nil || mapping.TeamsUserID == "" {
		return false, plugin.DenyNotRecipientDM
	}

	ccSlug := agentSlug
	if ccSlug == "" && strings.HasPrefix(msg.Sender, "agent:") {
		ccSlug = strings.TrimPrefix(msg.Sender, "agent:")
	}
	if ccSlug != "" {
		cc, err := store.GetConversationContext(ctx, mapping.TeamsUserID, projectID, ccSlug)
		if err != nil {
			b.log.Warn("Error looking up conversation context for target authorization", "error", err)
			return false, plugin.DenyLookupError
		}
		if cc != nil && cc.LastConversationID == container {
			return true, ""
		}
	}

	latest, err := store.GetLatestConversationContext(ctx, mapping.TeamsUserID, projectID)
	if err != nil {
		b.log.Warn("Error looking up latest conversation context for target authorization", "error", err)
		return false, plugin.DenyLookupError
	}
	if latest != nil && latest.LastConversationID == container {
		return true, ""
	}

	return false, plugin.DenyNotRecipientDM
}

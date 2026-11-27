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

package telegram

import (
	"context"
	"strconv"
	"strings"

	"github.com/GoogleCloudPlatform/scion/pkg/messages"
	"github.com/GoogleCloudPlatform/scion/pkg/plugin"
	"github.com/GoogleCloudPlatform/scion/pkg/projectkeys"
)

// canonicalTopicProjectID extracts the project ID from a canonical
// scion.project.<id>... topic. Non-canonical topics (such as
// scion.global.broadcast) return "" so explicit-target authorization fails
// closed with DenyNoProject.
func canonicalTopicProjectID(topic string) string {
	parsed, err := projectkeys.ParseTopic(topic)
	if err != nil {
		return ""
	}
	return parsed.ProjectID
}

// authorizeExplicitTarget reports whether container (a Telegram int64 chat ID
// formatted in base 10) may receive a message on a topic of projectID
// addressed to msg.Recipient. Fails closed on every error.
//
// Rule 1 (Linked): chatID has an active GroupLink with ProjectID == projectID.
// Rule 2 (Recipient DM): chatID > 0, msg.Recipient is "user:<email-or-id>",
// the recipient maps to a TelegramUserMapping whose TelegramUserID equals
// chatID, and recipientCanReadProject(ctx, mapping, projectID) is true.
func (b *TelegramBrokerV2) authorizeExplicitTarget(ctx context.Context, store Store, projectID, agentSlug string,
	msg *messages.StructuredMessage, container string) (bool, plugin.TargetDenyReason) {
	_, ok, reason := b.authorizeExplicitTargetChat(ctx, store, projectID, agentSlug, msg, container)
	return ok, reason
}

// authorizeExplicitTargetChat validates container and returns the
// store-authorized int64 chat ID (from GroupLink.ChatID or the recipient's
// TelegramUserMapping.TelegramUserID) so callers send only to store-derived
// targets.
func (b *TelegramBrokerV2) authorizeExplicitTargetChat(ctx context.Context, store Store, projectID, _ string,
	msg *messages.StructuredMessage, container string) (int64, bool, plugin.TargetDenyReason) {
	if projectID == "" {
		return 0, false, plugin.DenyNoProject
	}

	if store == nil {
		return 0, false, plugin.DenyNoStore
	}

	chatID, err := strconv.ParseInt(container, 10, 64)
	if err != nil || chatID == 0 {
		return 0, false, plugin.DenyNotLinked
	}

	// Rule 1: Active GroupLink for projectID.
	link, err := store.GetGroupLink(ctx, chatID)
	if err != nil {
		b.log.Warn("Error looking up group link for target authorization",
			"chat_id", chatID, "error", err)
		return 0, false, plugin.DenyLookupError
	}
	if link != nil {
		if !link.Active {
			return 0, false, plugin.DenyInactiveLink
		}
		if link.ProjectID != projectID {
			return 0, false, plugin.DenyForeignProject
		}
		return link.ChatID, true, ""
	}

	// Rule 2: Recipient personal DM (positive Telegram chat ID == user ID).
	if chatID <= 0 || msg == nil || !strings.HasPrefix(msg.Recipient, "user:") {
		return 0, false, plugin.DenyNotLinked
	}

	recipientVal := strings.TrimPrefix(msg.Recipient, "user:")
	if recipientVal == "" {
		return 0, false, plugin.DenyNotRecipientDM
	}

	var mapping *TelegramUserMapping
	if strings.Contains(recipientVal, "@") {
		mapping, err = store.GetUserMappingByEmail(ctx, recipientVal)
		if err != nil {
			b.log.Warn("Error looking up user mapping by email for target authorization", "error", err)
			return 0, false, plugin.DenyLookupError
		}
	} else {
		mapping, err = store.GetUserMappingByScionUserID(ctx, recipientVal)
		if err != nil {
			b.log.Warn("Error looking up user mapping by scion user ID for target authorization", "error", err)
			return 0, false, plugin.DenyLookupError
		}
		if mapping == nil {
			mapping, err = store.GetUserMappingByEmail(ctx, recipientVal)
			if err != nil {
				b.log.Warn("Error looking up user mapping by email for target authorization", "error", err)
				return 0, false, plugin.DenyLookupError
			}
		}
	}
	if mapping == nil && msg.RecipientID != "" && msg.RecipientID != recipientVal {
		mapping, err = store.GetUserMappingByScionUserID(ctx, msg.RecipientID)
		if err != nil {
			b.log.Warn("Error looking up user mapping by recipient ID for target authorization", "error", err)
			return 0, false, plugin.DenyLookupError
		}
	}

	if mapping == nil || mapping.TelegramUserID == "" {
		return 0, false, plugin.DenyNotRecipientDM
	}

	tgUserID, err := strconv.ParseInt(mapping.TelegramUserID, 10, 64)
	if err != nil || tgUserID <= 0 || tgUserID != chatID {
		return 0, false, plugin.DenyNotRecipientDM
	}

	ok, readReason := b.recipientCanReadProject(ctx, mapping, projectID)
	if !ok {
		b.logReadDenial("Explicit DM target denied by project read check", readReason, projectID)
		if readReason == readDenialCannotRead {
			return 0, false, plugin.DenyNotRecipientDM
		}
		return 0, false, plugin.DenyLookupError
	}

	return tgUserID, true, ""
}

// authorizeExplicitTarget reports whether container (a Telegram int64 chat ID
// formatted in base 10) is authorized by static chatRoutes for topic.
//
// Telegram v1 has no store or user mapping (no Rule 2): an explicit target is
// allowed only when topic is a canonical project topic, chatRoutes[chatID]
// exists, and subjectMatchesPattern(chatRoutes[chatID], topic) is true.
func (b *TelegramBroker) authorizeExplicitTarget(chatRoutes map[int64]string, topic, container string) (int64, bool, plugin.TargetDenyReason) {
	if canonicalTopicProjectID(topic) == "" {
		return 0, false, plugin.DenyNoProject
	}

	chatID, err := strconv.ParseInt(container, 10, 64)
	if err != nil || chatID == 0 {
		return 0, false, plugin.DenyNotLinked
	}

	pattern, exists := chatRoutes[chatID]
	if !exists {
		return 0, false, plugin.DenyNotLinked
	}

	if !subjectMatchesPattern(pattern, topic) {
		return 0, false, plugin.DenyForeignProject
	}

	return chatID, true, ""
}

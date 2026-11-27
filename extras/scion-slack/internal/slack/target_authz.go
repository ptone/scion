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

package slack

import (
	"context"
	"strings"

	"github.com/GoogleCloudPlatform/scion/pkg/messages"
	"github.com/GoogleCloudPlatform/scion/pkg/plugin"
	"github.com/GoogleCloudPlatform/scion/pkg/projectkeys"
)

// canonicalTopicProjectID returns the project ID only when topic parses as a
// canonical scion.project.<id>.... topic. Non-canonical topics yield "" so
// explicit target authorization fails closed with DenyNoProject.
func canonicalTopicProjectID(topic string) string {
	parsed, err := projectkeys.ParseTopic(topic)
	if err != nil {
		return ""
	}
	return parsed.ProjectID
}

// normalizeSlackContainer strips any ":<thread_ts>" suffix from an explicit
// Slack target string, returning the channel/DM/group ID prefix.
func normalizeSlackContainer(container string) string {
	ch, _, _ := strings.Cut(container, ":")
	return ch
}

// authorizeExplicitTarget reports whether container (a Slack channel/DM ID,
// optionally carrying a ":<thread_ts>" suffix) may receive a message on a
// topic of projectID addressed to msg.Recipient. Fails closed on every error.
//
// Rule 1 (Linked): normalized channel has an active ChannelLink with
// ProjectID == projectID.
// Rule 2 (Recipient DM): normalized channel starts with "D", msg.Recipient is
// "user:<email>" mapping to Slack user U, and the channel equals U's
// ConversationContext.LastChannelID for (projectID, agentSlug) or latest
// ConversationContext.LastChannelID for projectID.
func (b *SlackBroker) authorizeExplicitTarget(ctx context.Context, store Store, projectID, agentSlug string,
	msg *messages.StructuredMessage, container string) (bool, plugin.TargetDenyReason) {
	_, ok, reason := b.authorizeExplicitTargetChannel(ctx, store, projectID, agentSlug, msg, container)
	return ok, reason
}

// authorizeExplicitTargetChannel authorizes container and, when allowed,
// returns the store-derived channel ID (from ChannelLink.ChannelID or
// ConversationContext.LastChannelID) rather than the raw input string.
func (b *SlackBroker) authorizeExplicitTargetChannel(ctx context.Context, store Store, projectID, agentSlug string,
	msg *messages.StructuredMessage, container string) (string, bool, plugin.TargetDenyReason) {
	if projectID == "" {
		return "", false, plugin.DenyNoProject
	}

	if store == nil {
		return "", false, plugin.DenyNoStore
	}

	ch := normalizeSlackContainer(container)
	if !isSlackChannelID(ch) {
		return "", false, plugin.DenyNotLinked
	}

	// Rule 1: Active channel link for the topic's project.
	link, err := store.GetChannelLink(ctx, ch)
	if err != nil {
		b.log.Warn("Error looking up channel link for target authorization",
			"channel_id", plugin.TruncateTargetID(ch), "error", err)
		return "", false, plugin.DenyLookupError
	}
	if link != nil {
		if !link.Active {
			return "", false, plugin.DenyInactiveLink
		}
		if link.ProjectID != projectID {
			return "", false, plugin.DenyForeignProject
		}
		return link.ChannelID, true, ""
	}

	// Rule 2: Recipient DM channel (D...) matching the recipient's own
	// ConversationContext rows for this project.
	if !strings.HasPrefix(ch, "D") {
		return "", false, plugin.DenyNotLinked
	}
	if msg == nil || !strings.HasPrefix(msg.Recipient, "user:") {
		return "", false, plugin.DenyNotLinked
	}

	email := strings.TrimPrefix(msg.Recipient, "user:")
	if email == "" {
		return "", false, plugin.DenyNotRecipientDM
	}

	mapping, err := store.GetUserMappingByEmail(ctx, email)
	if err != nil {
		b.log.Warn("Error looking up user mapping for target authorization", "error", err)
		return "", false, plugin.DenyLookupError
	}
	if mapping == nil || mapping.SlackUserID == "" {
		return "", false, plugin.DenyNotRecipientDM
	}

	ccSlug := agentSlug
	if ccSlug == "" && strings.HasPrefix(msg.Sender, "agent:") {
		ccSlug = strings.TrimPrefix(msg.Sender, "agent:")
	}
	if ccSlug != "" {
		cc, err := store.GetConversationContext(ctx, mapping.SlackUserID, projectID, ccSlug)
		if err != nil {
			b.log.Warn("Error looking up conversation context for target authorization", "error", err)
			return "", false, plugin.DenyLookupError
		}
		if cc != nil && cc.LastChannelID == ch {
			return cc.LastChannelID, true, ""
		}
	}

	latest, err := store.GetLatestConversationContext(ctx, mapping.SlackUserID, projectID)
	if err != nil {
		b.log.Warn("Error looking up latest conversation context for target authorization", "error", err)
		return "", false, plugin.DenyLookupError
	}
	if latest != nil && latest.LastChannelID == ch {
		return latest.LastChannelID, true, ""
	}

	return "", false, plugin.DenyNotRecipientDM
}

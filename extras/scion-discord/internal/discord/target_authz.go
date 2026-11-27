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

package discord

import (
	"context"
	"strings"

	"github.com/bwmarrin/discordgo"

	"github.com/GoogleCloudPlatform/scion/pkg/messages"
	"github.com/GoogleCloudPlatform/scion/pkg/plugin"
	"github.com/GoogleCloudPlatform/scion/pkg/projectkeys"
)

// canonicalTopicProjectID extracts the project ID from a canonical
// scion.project.<id>.... topic. Non-canonical topics (such as
// scion.global.broadcast or legacy topics) return "" so explicit-target
// authorization fails closed with DenyNoProject.
func canonicalTopicProjectID(topic string) string {
	parsed, err := projectkeys.ParseTopic(topic)
	if err != nil {
		return ""
	}
	return parsed.ProjectID
}

// authorizeExplicitTarget reports whether container (a Discord channel or
// thread snowflake) may receive a message on a topic of projectID addressed to
// msg.Recipient. Fails closed on every error.
//
// Rule 1 (Linked): resolveChannelLink(container) (which resolves threads to
// their parent channel before checking the link) returns an active ChannelLink
// with ProjectID == projectID. Any store error or Discord API thread-parent
// lookup error returns DenyLookupError and never falls through to Rule 2.
//
// Rule 2 (Recipient DM): msg.Recipient is "user:<...>" and container matches
// either the recipient's ConversationContext channel for (projectID, agentSlug
// or latest) or the recipient's own Discord DM channel.
func (b *DiscordBroker) authorizeExplicitTarget(ctx context.Context, store Store, projectID, agentSlug string,
	msg *messages.StructuredMessage, container string) (bool, plugin.TargetDenyReason) {
	if projectID == "" {
		return false, plugin.DenyNoProject
	}

	if store == nil {
		return false, plugin.DenyNoStore
	}

	if !isDiscordSnowflake(container) {
		return false, plugin.DenyNotLinked
	}

	b.mu.RLock()
	session := b.session
	createDM := b.createDMChannel
	b.mu.RUnlock()

	// Rule 1: Active ChannelLink on the channel or its thread parent.
	link, parentLookupFailed, err := resolveChannelLinkDetailed(ctx, session, store, container)
	if err != nil {
		b.log.Warn("Error looking up channel link for target authorization",
			"channel_id", plugin.TruncateTargetID(container), "error", err)
		return false, plugin.DenyLookupError
	}
	if parentLookupFailed {
		b.log.Warn("Error resolving thread parent for target authorization",
			"channel_id", plugin.TruncateTargetID(container))
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

	// Rule 2: Recipient DM / ConversationContext channel.
	if msg == nil || !strings.HasPrefix(msg.Recipient, "user:") {
		return false, plugin.DenyNotLinked
	}

	return b.authorizeRecipientDMTarget(ctx, store, session, createDM, projectID, agentSlug, msg, container)
}

func (b *DiscordBroker) authorizeRecipientDMTarget(
	ctx context.Context,
	store Store,
	session *discordgo.Session,
	createDM func(discordUserID string) (string, error),
	projectID, agentSlug string,
	msg *messages.StructuredMessage,
	container string,
) (bool, plugin.TargetDenyReason) {
	email := strings.TrimPrefix(msg.Recipient, "user:")
	if email == "" && msg.RecipientID == "" {
		return false, plugin.DenyNotRecipientDM
	}

	mapping, err := b.lookupRecipientMappingStrict(ctx, store, email, msg.RecipientID)
	if err != nil {
		b.log.Warn("Error looking up user mapping for target authorization", "error", err)
		return false, plugin.DenyLookupError
	}
	if mapping == nil || mapping.DiscordUserID == "" {
		return false, plugin.DenyNotRecipientDM
	}

	// Check recipient's ConversationContext channels (same rows resolveRecipientChannels uses).
	ccSlug := agentSlug
	if ccSlug == "" && strings.HasPrefix(msg.Sender, "agent:") {
		ccSlug = strings.TrimPrefix(msg.Sender, "agent:")
	}
	if ccSlug != "" {
		cc, err := store.GetConversationContext(ctx, mapping.DiscordUserID, projectID, ccSlug)
		if err != nil {
			b.log.Warn("Error looking up conversation context for target authorization", "error", err)
			return false, plugin.DenyLookupError
		}
		if cc != nil && cc.LastChannelID == container {
			return true, ""
		}
	}

	latest, err := store.GetLatestConversationContext(ctx, mapping.DiscordUserID, projectID)
	if err != nil {
		b.log.Warn("Error looking up latest conversation context for target authorization", "error", err)
		return false, plugin.DenyLookupError
	}
	if latest != nil && latest.LastChannelID == container {
		return true, ""
	}

	// Check recipient's own Discord DM channel (same predicate resolveRecipientDMChannel uses).
	dmChannelID, err := openRecipientDMChannel(ctx, session, createDM, mapping.DiscordUserID)
	if err != nil {
		b.log.Warn("Error resolving recipient DM channel for target authorization", "error", err)
		return false, plugin.DenyLookupError
	}
	if dmChannelID == container {
		return true, ""
	}

	return false, plugin.DenyNotRecipientDM
}

// lookupRecipientMappingStrict resolves a Scion user to a DiscordUserMapping
// by email and then by Scion user ID, returning any unresolved store error.
func (b *DiscordBroker) lookupRecipientMappingStrict(ctx context.Context, store Store, email, recipientID string) (*DiscordUserMapping, error) {
	var mapping *DiscordUserMapping
	var err error
	if email != "" {
		mapping, err = store.GetUserMappingByEmail(ctx, email)
	}
	if (err != nil || mapping == nil) && recipientID != "" {
		fallback, fallbackErr := store.GetUserMappingByScionUserID(ctx, recipientID)
		if fallbackErr != nil {
			return nil, fallbackErr
		}
		return fallback, nil
	}
	if err != nil {
		return nil, err
	}
	return mapping, nil
}

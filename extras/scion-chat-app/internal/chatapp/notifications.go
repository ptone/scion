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

package chatapp

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/GoogleCloudPlatform/scion/extras/scion-chat-app/internal/state"
	"github.com/GoogleCloudPlatform/scion/pkg/messages"
)

// NotificationRelay routes agent notifications to chat spaces as rich cards.
type NotificationRelay struct {
	store     *state.Store
	messenger Messenger
	log       *slog.Logger
}

// NewNotificationRelay creates a new notification relay.
func NewNotificationRelay(store *state.Store, messenger Messenger, log *slog.Logger) *NotificationRelay {
	return &NotificationRelay{
		store:     store,
		messenger: messenger,
		log:       log,
	}
}

// HandleBrokerMessage processes a message received via the broker plugin's Publish() path.
// This is the primary notification delivery path.
func (n *NotificationRelay) HandleBrokerMessage(ctx context.Context, topic string, msg *messages.StructuredMessage) error {
	// Parse topic to extract grove ID: "grove.<groveID>.agent.status" or "user.<userID>.message"
	parts := strings.Split(topic, ".")
	if len(parts) < 2 {
		n.log.Debug("ignoring message with short topic", "topic", topic)
		return nil
	}

	switch {
	case parts[0] == "user" && len(parts) >= 3 && parts[2] == "message":
		// User-targeted message: "user.<userID>.message"
		return n.handleUserMessage(ctx, msg)

	case parts[0] == "grove" && len(parts) >= 4:
		groveID := parts[1]
		// Agent notification: "grove.<groveID>.agent.status" or similar
		return n.handleAgentNotification(ctx, groveID, msg)

	default:
		n.log.Debug("ignoring unrecognized topic", "topic", topic)
		return nil
	}
}

// handleAgentNotification renders an agent status notification as a card in linked spaces.
func (n *NotificationRelay) handleAgentNotification(ctx context.Context, groveID string, msg *messages.StructuredMessage) error {
	// Find all spaces linked to this grove
	links, err := n.store.ListSpaceLinks()
	if err != nil {
		return fmt.Errorf("listing space links: %w", err)
	}

	for _, link := range links {
		if link.GroveID != groveID {
			continue
		}

		// Determine notification style from message type and content
		card := n.renderNotificationCard(msg)

		// Find subscribers for @mentions
		mentions := n.getSubscriberMentions(msg, link)

		// Add mentions to the card text if any
		if mentions != "" {
			card.Sections = append(card.Sections, CardSection{
				Widgets: []Widget{
					{Type: WidgetText, Content: mentions},
				},
			})
		}

		if _, err := n.messenger.SendCard(ctx, link.SpaceID, card); err != nil {
			n.log.Error("failed to send notification card",
				"space_id", link.SpaceID,
				"grove_id", groveID,
				"error", err,
			)
			// Continue to other spaces
		}
	}

	return nil
}

// handleUserMessage relays a user-targeted message to chat.
// It maps the Hub user ID (RecipientID) back to a chat platform user and delivers
// the message to all spaces linked to the grove.
func (n *NotificationRelay) handleUserMessage(ctx context.Context, msg *messages.StructuredMessage) error {
	if msg.RecipientID == "" {
		n.log.Debug("user message has no recipient ID, skipping relay")
		return nil
	}

	// Look up the chat platform user for this Hub user
	mapping, err := n.store.GetUserMappingByHubID(msg.RecipientID)
	if err != nil {
		return fmt.Errorf("looking up user mapping: %w", err)
	}
	if mapping == nil {
		n.log.Debug("no chat platform mapping for hub user, skipping relay",
			"hub_user_id", msg.RecipientID,
		)
		return nil
	}

	// Check if the user has subscriptions (relay is toggleable per user)
	subs, err := n.store.ListUserSubscriptions(mapping.PlatformUserID, mapping.Platform)
	if err != nil {
		return fmt.Errorf("listing user subscriptions: %w", err)
	}
	if len(subs) == 0 {
		n.log.Debug("user has no subscriptions, skipping message relay",
			"hub_user_id", msg.RecipientID,
			"platform_user_id", mapping.PlatformUserID,
		)
		return nil
	}

	// Extract agent identity from sender
	agentSlug := msg.Sender
	if idx := strings.Index(agentSlug, ":"); idx >= 0 {
		agentSlug = agentSlug[idx+1:]
	}

	// Find spaces linked to groves the user is subscribed to
	links, err := n.store.ListSpaceLinks()
	if err != nil {
		return fmt.Errorf("listing space links: %w", err)
	}

	// Build a set of grove IDs the user is subscribed to
	subGroves := make(map[string]bool)
	for _, sub := range subs {
		subGroves[sub.GroveID] = true
	}

	for _, link := range links {
		if !subGroves[link.GroveID] || link.Platform != mapping.Platform {
			continue
		}

		card := Card{
			Header: CardHeader{
				Title:    fmt.Sprintf("Message from %s", agentSlug),
				Subtitle: fmt.Sprintf("Grove: %s", link.GroveSlug),
			},
			Sections: []CardSection{
				{
					Widgets: []Widget{
						{Type: WidgetText, Content: msg.Msg},
					},
				},
				{
					Widgets: []Widget{
						{Type: WidgetText, Content: fmt.Sprintf("To: <users/%s>", mapping.PlatformUserID)},
					},
				},
			},
			Actions: []CardAction{
				{Label: "Reply", ActionID: fmt.Sprintf("agent.respond.%s", agentSlug)},
				{Label: "View Logs", ActionID: fmt.Sprintf("agent.logs.%s", agentSlug)},
			},
		}

		if _, err := n.messenger.SendCard(ctx, link.SpaceID, card); err != nil {
			n.log.Error("failed to relay user message",
				"space_id", link.SpaceID,
				"recipient", msg.RecipientID,
				"error", err,
			)
		}
	}

	return nil
}

// renderNotificationCard creates a card for an agent notification.
func (n *NotificationRelay) renderNotificationCard(msg *messages.StructuredMessage) Card {
	// Extract agent slug from sender (e.g., "agent:deploy-agent" -> "deploy-agent")
	agentSlug := msg.Sender
	if idx := strings.Index(agentSlug, ":"); idx >= 0 {
		agentSlug = agentSlug[idx+1:]
	}

	// Determine card style based on message type and content
	activity := extractActivity(msg)
	header, style := notificationStyle(activity)

	card := Card{
		Header: CardHeader{
			Title:    fmt.Sprintf("%s %s", style.icon, agentSlug),
			Subtitle: fmt.Sprintf("%s | %s", activity, header),
		},
		Sections: []CardSection{
			{
				Widgets: []Widget{
					{Type: WidgetText, Content: msg.Msg},
				},
			},
		},
	}

	// Add action buttons based on activity
	switch activity {
	case "COMPLETED":
		card.Actions = []CardAction{
			{Label: "View Logs", ActionID: fmt.Sprintf("agent.logs.%s", agentSlug)},
		}
	case "WAITING_FOR_INPUT":
		card.Sections = append(card.Sections, CardSection{
			Header: "Respond",
			Widgets: []Widget{
				{Type: WidgetInput, Label: "Your response", ActionID: fmt.Sprintf("agent.respond.%s", agentSlug)},
			},
		})
		card.Actions = []CardAction{
			{Label: "View Logs", ActionID: fmt.Sprintf("agent.logs.%s", agentSlug)},
		}
	case "ERROR":
		card.Actions = []CardAction{
			{Label: "View Logs", ActionID: fmt.Sprintf("agent.logs.%s", agentSlug)},
			{Label: "Restart", ActionID: fmt.Sprintf("agent.start.%s", agentSlug), Style: "primary"},
		}
	case "STALLED":
		card.Actions = []CardAction{
			{Label: "View Logs", ActionID: fmt.Sprintf("agent.logs.%s", agentSlug)},
			{Label: "Restart", ActionID: fmt.Sprintf("agent.start.%s", agentSlug), Style: "primary"},
			{Label: "Stop", ActionID: fmt.Sprintf("agent.stop.%s", agentSlug), Style: "danger"},
		}
	case "LIMITS_EXCEEDED":
		card.Actions = []CardAction{
			{Label: "View Logs", ActionID: fmt.Sprintf("agent.logs.%s", agentSlug)},
			{Label: "Stop", ActionID: fmt.Sprintf("agent.stop.%s", agentSlug), Style: "danger"},
		}
	case "DELETED":
		// No actions for deleted agents
	}

	return card
}

// notificationStyleInfo holds visual style for a notification type.
type notificationStyleInfo struct {
	icon string
}

// notificationStyle returns the header text and style for a given activity.
func notificationStyle(activity string) (string, notificationStyleInfo) {
	switch activity {
	case "COMPLETED":
		return "Completed", notificationStyleInfo{icon: "\u2705"}
	case "WAITING_FOR_INPUT":
		return "Needs Input", notificationStyleInfo{icon: "\u231b"}
	case "ERROR":
		return "Error", notificationStyleInfo{icon: "\u274c"}
	case "STALLED":
		return "Stalled", notificationStyleInfo{icon: "\u26a0\ufe0f"}
	case "LIMITS_EXCEEDED":
		return "Limits Exceeded", notificationStyleInfo{icon: "\u26a0\ufe0f"}
	case "DELETED":
		return "Deleted", notificationStyleInfo{icon: "\U0001F5D1\ufe0f"}
	default:
		return activity, notificationStyleInfo{icon: "\u2139\ufe0f"}
	}
}

// extractActivity determines the activity type from a message.
func extractActivity(msg *messages.StructuredMessage) string {
	// Try to extract activity from the message content
	content := strings.ToUpper(msg.Msg)

	activities := []string{"COMPLETED", "WAITING_FOR_INPUT", "LIMITS_EXCEEDED", "STALLED", "ERROR", "DELETED"}
	for _, a := range activities {
		if strings.Contains(content, a) {
			return a
		}
	}

	// Fallback based on message type
	switch msg.Type {
	case messages.TypeInputNeeded:
		return "WAITING_FOR_INPUT"
	case messages.TypeStateChange:
		return "STATE_CHANGE"
	default:
		return "INFO"
	}
}

// getSubscriberMentions returns a formatted string of @mentions for users
// subscribed to the given agent's notifications.
func (n *NotificationRelay) getSubscriberMentions(msg *messages.StructuredMessage, link state.SpaceLink) string {
	agentSlug := msg.Sender
	if idx := strings.Index(agentSlug, ":"); idx >= 0 {
		agentSlug = agentSlug[idx+1:]
	}

	subs, err := n.store.ListAgentSubscriptions(agentSlug, link.GroveID)
	if err != nil {
		n.log.Error("listing agent subscriptions", "error", err)
		return ""
	}

	activity := extractActivity(msg)
	var mentions []string

	for _, sub := range subs {
		if sub.Platform != link.Platform {
			continue
		}

		// Check activity filter
		if sub.Activities != "" {
			allowed := strings.Split(sub.Activities, ",")
			matched := false
			for _, a := range allowed {
				if strings.TrimSpace(strings.ToUpper(a)) == activity {
					matched = true
					break
				}
			}
			if !matched {
				continue
			}
		}

		// Format platform-specific mention
		mentions = append(mentions, fmt.Sprintf("<users/%s>", sub.PlatformUserID))
	}

	if len(mentions) == 0 {
		return ""
	}
	return "CC: " + strings.Join(mentions, " ")
}

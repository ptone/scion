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
	"regexp"
	"strings"

	slackapi "github.com/slack-go/slack"
	"github.com/slack-go/slack/slackevents"

	"github.com/GoogleCloudPlatform/scion/extras/scion-chat-app/internal/chatapp"
)

// botMentionRe matches a Slack user mention at the start of a message (e.g., "<@U0ABC123> ").
var botMentionRe = regexp.MustCompile(`^<@[A-Z0-9]+>\s*`)

// normalizeSlashCommand converts a Slack slash command into a ChatEvent.
func normalizeSlashCommand(cmd slackapi.SlashCommand) *chatapp.ChatEvent {
	return &chatapp.ChatEvent{
		Type:     chatapp.EventCommand,
		Platform: PlatformName,
		SpaceID:  cmd.ChannelID,
		UserID:   cmd.UserID,
		Command:  "scion",
		Args:     cmd.Text,
	}
}

// normalizeAppMention converts a Slack app_mention event into a ChatEvent.
func normalizeAppMention(evt *slackevents.AppMentionEvent) *chatapp.ChatEvent {
	text := stripBotMention(evt.Text)
	threadTS := evt.ThreadTimeStamp
	if threadTS == "" {
		threadTS = evt.TimeStamp
	}
	return &chatapp.ChatEvent{
		Type:     chatapp.EventMessage,
		Platform: PlatformName,
		SpaceID:  evt.Channel,
		ThreadID: threadTS,
		UserID:   evt.User,
		Text:     text,
	}
}

// normalizeMessageIM converts a Slack DM message event into a ChatEvent.
func normalizeMessageIM(evt *slackevents.MessageEvent) *chatapp.ChatEvent {
	threadTS := evt.ThreadTimeStamp
	if threadTS == "" {
		threadTS = evt.TimeStamp
	}
	return &chatapp.ChatEvent{
		Type:     chatapp.EventMessage,
		Platform: PlatformName,
		SpaceID:  evt.Channel,
		ThreadID: threadTS,
		UserID:   evt.User,
		Text:     strings.TrimSpace(evt.Text),
	}
}

// normalizeMemberJoined converts a member_joined_channel event for the bot
// into a ChatEvent indicating the bot was added to a space.
func normalizeMemberJoined(evt *slackevents.MemberJoinedChannelEvent) *chatapp.ChatEvent {
	return &chatapp.ChatEvent{
		Type:     chatapp.EventSpaceJoin,
		Platform: PlatformName,
		SpaceID:  evt.Channel,
		UserID:   evt.User,
	}
}

// normalizeMemberLeft converts a member_left_channel event for the bot
// into a ChatEvent indicating the bot was removed from a space.
func normalizeMemberLeft(evt *slackevents.MemberLeftChannelEvent) *chatapp.ChatEvent {
	return &chatapp.ChatEvent{
		Type:     chatapp.EventSpaceRemove,
		Platform: PlatformName,
		SpaceID:  evt.Channel,
		UserID:   evt.User,
	}
}

// normalizeInteraction converts a Slack interaction callback into a ChatEvent.
func normalizeInteraction(callback slackapi.InteractionCallback) *chatapp.ChatEvent {
	event := &chatapp.ChatEvent{
		Platform: PlatformName,
		SpaceID:  callback.Channel.ID,
		UserID:   callback.User.ID,
	}

	switch callback.Type {
	case slackapi.InteractionTypeBlockActions:
		if len(callback.ActionCallback.BlockActions) == 0 {
			return nil
		}
		action := callback.ActionCallback.BlockActions[0]
		event.Type = chatapp.EventAction
		event.ActionID = action.ActionID
		event.ActionData = action.Value
		if callback.Message.ThreadTimestamp != "" {
			event.ThreadID = callback.Message.ThreadTimestamp
		} else {
			event.ThreadID = callback.Message.Timestamp
		}

	case slackapi.InteractionTypeViewSubmission:
		event.Type = chatapp.EventDialogSubmit
		event.ActionID = callback.View.CallbackID
		event.DialogData = extractModalValues(callback.View.State)
		event.SpaceID = callback.View.PrivateMetadata

	default:
		return nil
	}

	return event
}

// stripBotMention removes the leading "<@BOTID> " from message text.
func stripBotMention(text string) string {
	return strings.TrimSpace(botMentionRe.ReplaceAllString(text, ""))
}

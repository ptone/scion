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

// Aliases for symbols moved to
// github.com/GoogleCloudPlatform/scion/pkg/hub/webchat
// by hack/pkgmove, so existing references in package hub keep compiling.

import (
	"context"

	"github.com/GoogleCloudPlatform/scion/pkg/hub/webchat"
)

type (
	AttachmentMeta          = webchat.AttachmentMeta
	ChatMessageDeletedEvent = webchat.ChatMessageDeletedEvent
	ChatMessageEditedEvent  = webchat.ChatMessageEditedEvent
	ChatReadStateEvent      = webchat.ChatReadStateEvent
	ChatSearchFilter        = webchat.ChatSearchFilter
	ChatSearchResult        = webchat.ChatSearchResult
	DMPromotedEvent         = webchat.DMPromotedEvent
	PromoteKeys             = webchat.PromoteKeys
	ScheduledChatMessage    = webchat.ScheduledChatMessage
	ScheduledMessageStore   = webchat.ScheduledMessageStore
	ThreadPrefs             = webchat.ThreadPrefs
	TopicEvent              = webchat.TopicEvent
	TopicUpdate             = webchat.TopicUpdate
	WebChatDM               = webchat.WebChatDM
	WebChatMessageExt       = webchat.WebChatMessageExt
	WebChatReadState        = webchat.WebChatReadState
	WebChatStore            = webchat.WebChatStore
	WebChatThread           = webchat.WebChatThread
	WebChatTopic            = webchat.WebChatTopic
	WebChatUserPrefs        = webchat.WebChatUserPrefs
)

const (
	DefaultMigrationBatchSize        = webchat.DefaultMigrationBatchSize
	ScheduledFailureConversationGone = webchat.ScheduledFailureConversationGone
	ScheduledFailureDeliveryError    = webchat.ScheduledFailureDeliveryError
	ScheduledFailureInterrupted      = webchat.ScheduledFailureInterrupted
	ScheduledFailureMissed           = webchat.ScheduledFailureMissed
	ScheduledFailureNoAccess         = webchat.ScheduledFailureNoAccess
	ScheduledFailureRecipientGone    = webchat.ScheduledFailureRecipientGone
	ScheduledFailureSenderInactive   = webchat.ScheduledFailureSenderInactive
	ScheduledMessageCancelled        = webchat.ScheduledMessageCancelled
	ScheduledMessageFailed           = webchat.ScheduledMessageFailed
	ScheduledMessagePending          = webchat.ScheduledMessagePending
	ScheduledMessageSending          = webchat.ScheduledMessageSending
	ScheduledMessageSent             = webchat.ScheduledMessageSent
)

var (
	NewWebChannelBus = webchat.NewWebChannelBus
	NewWebChatStore  = webchat.NewWebChatStore
)

func registerDMParticipants(p0 context.Context, p1 webchat.WebChatStore, p2 string) {
	webchat.RegisterDMParticipants(p0, p1, p2)
}

func scheduledMessageStoreFrom(p0 webchat.WebChatStore) webchat.ScheduledMessageStore {
	return webchat.ScheduledMessageStoreFrom(p0)
}

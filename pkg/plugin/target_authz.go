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

package plugin

import (
	"log/slog"

	"github.com/GoogleCloudPlatform/scion/pkg/messages"
)

// TargetSource records where an explicit outbound target originated.
type TargetSource string

const (
	TargetSourceThreadID     TargetSource = "thread_id"
	TargetSourceConversation TargetSource = "conversation"
	TargetSourceMetadata     TargetSource = "metadata"
)

// TargetDenyReason is a closed set of reasons an explicit outbound target
// was rejected by a chat broker plugin.
type TargetDenyReason string

const (
	DenyNoProject      TargetDenyReason = "no_project"
	DenyNoStore        TargetDenyReason = "no_store"
	DenyLookupError    TargetDenyReason = "lookup_error"
	DenyNotLinked      TargetDenyReason = "not_linked"
	DenyInactiveLink   TargetDenyReason = "inactive_link"
	DenyForeignProject TargetDenyReason = "foreign_project"
	DenyNotRecipientDM TargetDenyReason = "not_recipient_dm"
)

// ConversationTargetSource reports where ResolveConversationTarget(msg).ExternalRef
// originated: TargetSourceConversation when msg.Conversation supplies a non-native
// ExternalRef, TargetSourceThreadID when falling back to msg.ThreadID, or "" when
// neither supplies an explicit target.
func ConversationTargetSource(msg *messages.StructuredMessage) TargetSource {
	if msg == nil {
		return ""
	}
	if msg.Conversation != nil && msg.Conversation.Surface != "native" && msg.Conversation.ExternalRef != "" {
		return TargetSourceConversation
	}
	if msg.ThreadID != "" {
		return TargetSourceThreadID
	}
	return ""
}

const maxLoggedTargetLen = 64

// TruncateTargetID returns target capped to a bounded length for structured
// logs so untrusted identifiers cannot bloat log entries.
func TruncateTargetID(target string) string {
	runes := []rune(target)
	if len(runes) <= maxLoggedTargetLen {
		return target
	}
	return string(runes[:maxLoggedTargetLen]) + "..."
}

// LogTargetDenied emits one structured WARN log entry for a denied explicit
// outbound target. It logs a truncated target identifier and never logs the
// message body.
func LogTargetDenied(log *slog.Logger, surface, projectID string, src TargetSource, target string, reason TargetDenyReason) {
	if log == nil {
		return
	}
	log.Warn("outbound target denied",
		"event", "outbound_target_denied",
		"surface", surface,
		"project_id", projectID,
		"source", string(src),
		"target", TruncateTargetID(target),
		"reason", string(reason),
	)
}

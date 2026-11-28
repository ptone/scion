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
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/GoogleCloudPlatform/scion/pkg/messages"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLogTargetDenied(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	longTarget := strings.Repeat("a", 120)
	LogTargetDenied(log, "teams", "proj-a", TargetSourceConversation, longTarget, DenyForeignProject)

	var entry map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &entry))

	assert.Equal(t, "WARN", entry["level"])
	assert.Equal(t, "outbound_target_denied", entry["event"])
	assert.Equal(t, "teams", entry["surface"])
	assert.Equal(t, "proj-a", entry["project_id"])
	assert.Equal(t, "conversation", entry["source"])
	assert.Equal(t, "foreign_project", entry["reason"])

	loggedTarget, ok := entry["target"].(string)
	require.True(t, ok)
	assert.Equal(t, strings.Repeat("a", maxLoggedTargetLen)+"...", loggedTarget)
}

func TestLogTargetDenied_NilLogger(t *testing.T) {
	assert.NotPanics(t, func() {
		LogTargetDenied(nil, "teams", "proj-a", TargetSourceThreadID, "conv-1", DenyNotLinked)
	})
}

func TestTruncateTargetID(t *testing.T) {
	short := "19:abc@thread.tacv2"
	assert.Equal(t, short, TruncateTargetID(short))

	exact := strings.Repeat("x", maxLoggedTargetLen)
	assert.Equal(t, exact, TruncateTargetID(exact))

	over := strings.Repeat("x", maxLoggedTargetLen+1)
	assert.Equal(t, exact+"...", TruncateTargetID(over))
}

func TestConversationTargetSource(t *testing.T) {
	assert.Equal(t, TargetSource(""), ConversationTargetSource(nil))
	assert.Equal(t, TargetSource(""), ConversationTargetSource(&messages.StructuredMessage{}))

	assert.Equal(t, TargetSourceThreadID, ConversationTargetSource(&messages.StructuredMessage{
		ThreadID: "19:chan@thread.tacv2",
	}))

	assert.Equal(t, TargetSourceConversation, ConversationTargetSource(&messages.StructuredMessage{
		ThreadID: "legacy-id",
		Conversation: &messages.Conversation{
			Surface:     "teams",
			ExternalRef: "19:chan@thread.tacv2",
		},
	}))

	// When Conversation is present, ResolveConversationTarget never reads
	// ThreadID: native conversations and non-native conversations with an empty
	// ExternalRef yield no explicit target source.
	assert.Equal(t, TargetSource(""), ConversationTargetSource(&messages.StructuredMessage{
		ThreadID: "19:chan@thread.tacv2",
		Conversation: &messages.Conversation{
			Surface:     "native",
			ExternalRef: "thread:p1:123",
		},
	}))
	assert.Equal(t, TargetSource(""), ConversationTargetSource(&messages.StructuredMessage{
		Conversation: &messages.Conversation{
			Surface:     "native",
			ExternalRef: "thread:p1:123",
		},
	}))

	// Non-native conversation with empty ExternalRef does not fall back to ThreadID.
	assert.Equal(t, TargetSource(""), ConversationTargetSource(&messages.StructuredMessage{
		ThreadID: "19:chan@thread.tacv2",
		Conversation: &messages.Conversation{
			Surface: "teams",
		},
	}))
}

func TestConversationParentTargetSource(t *testing.T) {
	assert.Equal(t, TargetSource(""), ConversationParentTargetSource(nil))
	assert.Equal(t, TargetSource(""), ConversationParentTargetSource(&messages.StructuredMessage{
		ThreadID: "19:chan@thread.tacv2",
	}))
	assert.Equal(t, TargetSource(""), ConversationParentTargetSource(&messages.StructuredMessage{
		Conversation: &messages.Conversation{
			Surface:   "native",
			ParentRef: "thread:p1:parent",
		},
	}))
	assert.Equal(t, TargetSourceConversation, ConversationParentTargetSource(&messages.StructuredMessage{
		ThreadID: "19:chan@thread.tacv2;messageid=1",
		Conversation: &messages.Conversation{
			Surface:   "teams",
			ParentRef: "19:chan@thread.tacv2",
		},
	}))
}

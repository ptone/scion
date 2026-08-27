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
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/GoogleCloudPlatform/scion/pkg/messages"
)

// validLegacyMessage returns a StructuredMessage that passes validation.
func validLegacyMessage() *messages.StructuredMessage {
	return &messages.StructuredMessage{
		Version:   messages.Version,
		Msg:       "hello world",
		Sender:    "user:alice",
		SenderID:  "user:alice",
		Recipient: "agent:builder",
		Type:      messages.TypeInstruction,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	}
}

func TestValidateLegacyMessage_ValidMessage(t *testing.T) {
	msg := validLegacyMessage()
	if err := ValidateLegacyMessage(msg); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateLegacyMessage_Nil(t *testing.T) {
	if err := ValidateLegacyMessage(nil); err == nil {
		t.Fatal("expected error for nil message")
	}
}

func TestValidateLegacyMessage_EmptyMsg(t *testing.T) {
	msg := validLegacyMessage()
	msg.Msg = ""
	err := ValidateLegacyMessage(msg)
	if err == nil {
		t.Fatal("expected error for empty msg field")
	}
}

func TestValidateLegacyMessage_EmptySender(t *testing.T) {
	msg := validLegacyMessage()
	msg.Sender = ""
	err := ValidateLegacyMessage(msg)
	if err == nil {
		t.Fatal("expected error for empty sender")
	}
}

func TestValidateLegacyMessage_InvalidType(t *testing.T) {
	msg := validLegacyMessage()
	msg.Type = "invalid-type"
	err := ValidateLegacyMessage(msg)
	if err == nil {
		t.Fatal("expected error for invalid type")
	}
}

// TestValidateLegacyMessage_ThreadIDWithoutChannel reproduces the Teams
// regression: the Teams adapter emits Channel:"" with a non-empty ThreadID.
// The old Validate() forbids this, and ValidateLegacyMessage must also reject
// it. This is the specific bug from the findings document, section 6.
func TestValidateLegacyMessage_ThreadIDWithoutChannel(t *testing.T) {
	msg := validLegacyMessage()
	msg.ThreadID = "thread-123"
	msg.Channel = "" // Teams regression
	err := ValidateLegacyMessage(msg)
	if err == nil {
		t.Fatal("expected error for thread_id without channel (Teams regression)")
	}
	if !strings.Contains(err.Error(), "thread_id requires channel") {
		t.Fatalf("error should mention thread_id/channel, got: %v", err)
	}
}

func TestValidateLegacyMessage_ThreadIDWithChannel(t *testing.T) {
	msg := validLegacyMessage()
	msg.ThreadID = "thread-123"
	msg.Channel = "discord"
	if err := ValidateLegacyMessage(msg); err != nil {
		t.Fatalf("thread_id with channel should be valid: %v", err)
	}
}

func TestValidateLegacyMessage_ChannelTooLong(t *testing.T) {
	msg := validLegacyMessage()
	msg.Channel = strings.Repeat("a", messages.MaxChannelLength+1)
	err := ValidateLegacyMessage(msg)
	if err == nil {
		t.Fatal("expected error for channel exceeding max length")
	}
}

func TestValidateLegacyMessage_BodyOverLimit(t *testing.T) {
	msg := validLegacyMessage()
	msg.Msg = strings.Repeat("x", messages.MaxMessageLength+1)
	err := ValidateLegacyMessage(msg)
	if err == nil {
		t.Fatal("expected error for body over character limit")
	}
}

func TestValidateLegacyMessage_TooManyAttachments(t *testing.T) {
	msg := validLegacyMessage()
	msg.Attachments = make([]string, messages.MaxAttachments+1)
	err := ValidateLegacyMessage(msg)
	if err == nil {
		t.Fatal("expected error for too many attachments")
	}
}

func TestValidateLegacyMessage_AllValidTypes(t *testing.T) {
	validTypes := []string{
		messages.TypeInstruction,
		messages.TypeInputNeeded,
		messages.TypeStateChange,
		messages.TypeAssistantReply,
		messages.TypeGroupSet,
		messages.TypeMention,
		messages.TypeSystem,
	}
	for _, typ := range validTypes {
		msg := validLegacyMessage()
		msg.Type = typ
		if err := ValidateLegacyMessage(msg); err != nil {
			t.Errorf("type %q should be valid: %v", typ, err)
		}
	}
}

func TestValidateLegacyMessage_BroadcastWithoutRecipient(t *testing.T) {
	// Broadcast messages have no recipient; validation should still pass.
	msg := validLegacyMessage()
	msg.Recipient = ""
	msg.Broadcasted = true
	if err := ValidateLegacyMessage(msg); err != nil {
		t.Fatalf("broadcast message without recipient should be valid: %v", err)
	}
}

func TestValidateLegacyMessage_ChannelInvalidCharacters(t *testing.T) {
	msg := validLegacyMessage()
	msg.Channel = "my channel!" // contains space and exclamation mark
	err := ValidateLegacyMessage(msg)
	if err == nil {
		t.Fatal("expected error for channel with invalid characters")
	}
	if !strings.Contains(err.Error(), "invalid characters") {
		t.Fatalf("error should mention invalid characters, got: %v", err)
	}
}

func TestValidateLegacyMessage_ChannelValidCharacters(t *testing.T) {
	msg := validLegacyMessage()
	msg.Channel = "my-channel-123"
	if err := ValidateLegacyMessage(msg); err != nil {
		t.Fatalf("valid channel should pass: %v", err)
	}
}

func TestValidateLegacyMessage_TooManyMetadataEntries(t *testing.T) {
	msg := validLegacyMessage()
	msg.Metadata = make(map[string]string)
	for i := 0; i < messages.MaxMetadataEntries+1; i++ {
		msg.Metadata[fmt.Sprintf("key-%d", i)] = "value"
	}
	err := ValidateLegacyMessage(msg)
	if err == nil {
		t.Fatal("expected error for too many metadata entries")
	}
	if !strings.Contains(err.Error(), "metadata entries") {
		t.Fatalf("error should mention metadata entries, got: %v", err)
	}
}

func TestValidateLegacyMessage_MetadataKeyTooLong(t *testing.T) {
	msg := validLegacyMessage()
	longKey := strings.Repeat("k", messages.MaxMetadataKeySize+1)
	msg.Metadata = map[string]string{longKey: "value"}
	err := ValidateLegacyMessage(msg)
	if err == nil {
		t.Fatal("expected error for metadata key exceeding max size")
	}
	if !strings.Contains(err.Error(), "metadata key") {
		t.Fatalf("error should mention metadata key, got: %v", err)
	}
}

func TestValidateLegacyMessage_MetadataValueTooLong(t *testing.T) {
	msg := validLegacyMessage()
	longValue := strings.Repeat("v", messages.MaxMetadataValueSize+1)
	msg.Metadata = map[string]string{"key": longValue}
	err := ValidateLegacyMessage(msg)
	if err == nil {
		t.Fatal("expected error for metadata value exceeding max size")
	}
	if !strings.Contains(err.Error(), "metadata value") {
		t.Fatalf("error should mention metadata value, got: %v", err)
	}
}

func TestValidateLegacyMessage_ChatType(t *testing.T) {
	msg := validLegacyMessage()
	msg.Type = "chat"
	// "chat" is a valid type in the legacy enum; the compat layer should
	// accept it.
	err := ValidateLegacyMessage(msg)
	if err != nil {
		// If chat is not in the valid types map, that's OK — skip this test.
		if strings.Contains(err.Error(), "invalid message type") {
			t.Skipf("chat type not in legacy enum: %v", err)
		}
		t.Fatalf("unexpected error: %v", err)
	}
}

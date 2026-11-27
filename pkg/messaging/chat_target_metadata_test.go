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
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStripChatSurfaceMetadata(t *testing.T) {
	t.Run("nil map returns nil", func(t *testing.T) {
		assert.Nil(t, StripChatSurfaceMetadata(nil))
	})

	t.Run("empty map returns same map", func(t *testing.T) {
		in := map[string]string{}
		got := StripChatSurfaceMetadata(in)
		assert.Equal(t, reflect.ValueOf(in).Pointer(), reflect.ValueOf(got).Pointer())
	})

	t.Run("no matching keys returns same map without copying", func(t *testing.T) {
		in := map[string]string{
			"project_id": "p1",
			"request_id": "req-1",
			"my_slack":   "keep",
			"x_teams_id": "keep",
		}
		got := StripChatSurfaceMetadata(in)
		assert.Equal(t, reflect.ValueOf(in).Pointer(), reflect.ValueOf(got).Pointer())
		assert.Equal(t, in, got)
	})

	t.Run("strips all chat-surface prefixes and preserves other keys without mutating input", func(t *testing.T) {
		in := map[string]string{
			"telegram_chat_id":          "-100123",
			"telegram_attachment_path":  "/tmp/a.txt",
			"telegram_message_id":       "42",
			"slack_channel_id":          "C01234",
			"slack_thread_ts":           "1700000000.000100",
			"teams_conversation_id":     "19:abc@thread.tacv2",
			"teams_service_url":         "https://example.invalid",
			"teams_reply_to_id":         "act-1",
			"teams_tenant_id":           "tenant-1",
			"discord_channel_id":        "1234567890",
			"discord_parent_channel_id": "9876543210",
			"keep_me":                   "yes",
			"project_id":                "p1",
		}
		origSnapshot := make(map[string]string, len(in))
		for k, v := range in {
			origSnapshot[k] = v
		}

		got := StripChatSurfaceMetadata(in)
		require.NotEqual(t, reflect.ValueOf(in).Pointer(), reflect.ValueOf(got).Pointer())
		assert.Equal(t, map[string]string{
			"keep_me":    "yes",
			"project_id": "p1",
		}, got)

		// Input map must be completely unchanged.
		assert.Equal(t, origSnapshot, in)

		// Mutating returned map must not affect input map.
		got["mutated"] = "true"
		_, existsInInput := in["mutated"]
		assert.False(t, existsInInput)
	})

	t.Run("all keys stripped returns empty non-mutating map", func(t *testing.T) {
		in := map[string]string{
			"teams_service_url": "https://example.invalid",
		}
		got := StripChatSurfaceMetadata(in)
		assert.Empty(t, got)
		assert.Equal(t, "https://example.invalid", in["teams_service_url"])
	})
}

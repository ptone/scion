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

import "strings"

// ChatSurfaceMetadataPrefixes are metadata key prefixes owned by chat broker
// plugins on the inbound direction (plugin -> hub). The hub does not produce
// them on outbound messages; any matching key on an outbound publish is
// caller-supplied and stripped before dispatch to the event bus.
var ChatSurfaceMetadataPrefixes = []string{"telegram_", "slack_", "teams_", "discord_"}

// HasChatSurfaceMetadataPrefix reports whether key begins with one of
// ChatSurfaceMetadataPrefixes.
func HasChatSurfaceMetadataPrefix(key string) bool {
	for _, prefix := range ChatSurfaceMetadataPrefixes {
		if strings.HasPrefix(key, prefix) {
			return true
		}
	}
	return false
}

// StripChatSurfaceMetadata returns md unchanged if no key has a chat-surface
// prefix, otherwise a new map without them. It never mutates md.
func StripChatSurfaceMetadata(md map[string]string) map[string]string {
	hasChatKey := false
	for k := range md {
		if HasChatSurfaceMetadataPrefix(k) {
			hasChatKey = true
			break
		}
	}
	if !hasChatKey {
		return md
	}
	out := make(map[string]string, len(md))
	for k, v := range md {
		if HasChatSurfaceMetadataPrefix(k) {
			continue
		}
		out[k] = v
	}
	return out
}

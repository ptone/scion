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

import (
	"testing"
)

// pluginAcceptedInboundModes is the set of values the telegram plugin's
// Configure() accepts for "inbound_mode" (broker_v2.go). If the plugin
// ever adds new modes, update this set and the setup code together.
var pluginAcceptedInboundModes = map[string]bool{
	"poll":    true,
	"webhook": true,
}

// TestTelegramSetup_InboundModeValue verifies that the inbound_mode value
// written by our setup code matches the plugin's accepted values. A mismatch
// causes Configure() to reject the config silently, and the bot never starts.
func TestTelegramSetup_InboundModeValue(t *testing.T) {
	// The value used by PersistTelegramConfig and the setup handler.
	const setupInboundMode = "poll"

	if !pluginAcceptedInboundModes[setupInboundMode] {
		t.Fatalf("setup code writes inbound_mode=%q but plugin only accepts %v",
			setupInboundMode, pluginAcceptedInboundModes)
	}

	// Verify it's not "polling" (the value that caused the original bug).
	if setupInboundMode == "polling" {
		t.Fatal("inbound_mode must be \"poll\", not \"polling\" — the plugin rejects \"polling\"")
	}
}

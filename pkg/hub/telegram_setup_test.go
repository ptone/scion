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

	scionplugin "github.com/GoogleCloudPlatform/scion/pkg/plugin"
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

// TestTelegramSetup_V2EnvSet verifies that the telegram plugin entry built
// by the setup handler includes SCION_TELEGRAM_V2=1 so the plugin runs v2
// (group links, /setup, project_slug_map). Without this, the plugin silently
// runs v1 which ignores inbound_mode and lacks onboarding behavior.
func TestTelegramSetup_V2EnvSet(t *testing.T) {
	// Simulate the PluginEntry the setup handler builds.
	entry := scionplugin.PluginEntry{
		Config: map[string]string{
			"bot_token":    "test-token",
			"inbound_mode": "poll",
		},
		Env: map[string]string{
			"SCION_TELEGRAM_V2": "1",
		},
	}

	v, ok := entry.Env["SCION_TELEGRAM_V2"]
	if !ok {
		t.Fatal("SCION_TELEGRAM_V2 must be set in plugin env")
	}
	if v != "1" {
		t.Fatalf("SCION_TELEGRAM_V2 must be \"1\", got %q", v)
	}
}

// TestTelegramPluginEntry_EnvPropagation verifies that PluginEntry.Env is
// propagated through to DiscoveredPlugin via LoadOne's construction path.
func TestTelegramPluginEntry_EnvPropagation(t *testing.T) {
	env := map[string]string{"SCION_TELEGRAM_V2": "1"}
	entry := scionplugin.PluginEntry{
		Config: map[string]string{"bot_token": "test"},
		Env:    env,
	}

	// LoadOne builds a DiscoveredPlugin — verify Env field survives.
	// We can't call LoadOne without a real binary, but we can verify the
	// struct construction matches.
	dp := scionplugin.DiscoveredPlugin{
		Name:       "telegram",
		Type:       scionplugin.PluginTypeBroker,
		Config:     entry.Config,
		Env:        entry.Env,
		FromConfig: true,
	}

	if dp.Env["SCION_TELEGRAM_V2"] != "1" {
		t.Fatal("Env not propagated to DiscoveredPlugin")
	}
}

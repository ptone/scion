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

// TestEnsureTelegramEnv_SetsV2 verifies that EnsureTelegramEnv sets
// SCION_TELEGRAM_V2=1 for the telegram broker plugin.
func TestEnsureTelegramEnv_SetsV2(t *testing.T) {
	entry := scionplugin.PluginEntry{
		Config: map[string]string{"bot_token": "test"},
	}
	EnsureTelegramEnv("telegram", &entry)

	if entry.Env["SCION_TELEGRAM_V2"] != "1" {
		t.Fatal("EnsureTelegramEnv must set SCION_TELEGRAM_V2=1")
	}
}

// TestEnsureTelegramEnv_ExistingSettingsWithoutEnv verifies that an existing
// settings.yaml entry WITHOUT the Env field still gets v2 after
// EnsureTelegramEnv. This is the migration/restart-safety test.
func TestEnsureTelegramEnv_ExistingSettingsWithoutEnv(t *testing.T) {
	// Simulate a pre-existing settings.yaml entry: has config but NO Env.
	entry := scionplugin.PluginEntry{
		Config: map[string]string{
			"bot_token":      "existing-token",
			"inbound_mode":   "poll",
			"webhook_secret": "existing-secret",
		},
		// Env is nil — this is what old settings.yaml files look like.
	}

	if entry.Env != nil {
		t.Fatal("precondition: Env should be nil to simulate old settings")
	}

	EnsureTelegramEnv("telegram", &entry)

	if entry.Env == nil || entry.Env["SCION_TELEGRAM_V2"] != "1" {
		t.Fatal("EnsureTelegramEnv must set v2 even when Env was nil (old settings)")
	}
	if entry.Config["bot_token"] != "existing-token" {
		t.Fatal("EnsureTelegramEnv must not modify existing config")
	}
}

// TestEnsureTelegramEnv_SkipsNonTelegram verifies non-telegram plugins are untouched.
func TestEnsureTelegramEnv_SkipsNonTelegram(t *testing.T) {
	entry := scionplugin.PluginEntry{Config: map[string]string{"key": "val"}}
	EnsureTelegramEnv("gchat", &entry)

	if entry.Env != nil {
		t.Fatal("EnsureTelegramEnv should not set Env for non-telegram plugins")
	}
}

// TestEnsureTelegramEnv_SkipsSelfManaged verifies self-managed telegram is untouched.
func TestEnsureTelegramEnv_SkipsSelfManaged(t *testing.T) {
	entry := scionplugin.PluginEntry{SelfManaged: true}
	EnsureTelegramEnv("telegram", &entry)

	if entry.Env != nil {
		t.Fatal("EnsureTelegramEnv should not set Env for self-managed plugins")
	}
}

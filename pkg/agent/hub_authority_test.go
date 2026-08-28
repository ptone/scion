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

package agent

// Phase 4 of ptone/scion#1316: broker stops inventing names.
//
// When the hub is the authority for harness-config defaults (hosted mode),
// the broker must NOT fall back to its own settings chain to invent a
// harness-config name the hub did not provide. These tests verify that the
// HubIsHarnessConfigAuthority context flag suppresses the broker's
// settings-based fallback (rungs 6-7 of ResolveHarnessConfigName).

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/GoogleCloudPlatform/scion/pkg/api"
	"github.com/GoogleCloudPlatform/scion/pkg/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestHubAuthority_SettingsDefaultSuppressed verifies that when the hub is
// the harness-config authority, the broker's settings-default rung (rung 7)
// does NOT fire. Without the flag, settings.DefaultHarnessConfig would
// provide "antigravity" — the exact authority violation #1316 identifies.
func TestHubAuthority_SettingsDefaultSuppressed(t *testing.T) {
	settings := &config.VersionedSettings{
		DefaultHarnessConfig: "antigravity",
	}

	// Without the flag: settings default fires (rung 7) → "antigravity"
	res, err := config.ResolveHarnessConfigName(config.HarnessConfigInputs{
		Settings: settings,
	})
	require.NoError(t, err)
	assert.Equal(t, "antigravity", res.Name,
		"without hub authority: settings default must fire")
	assert.Equal(t, "settings-default", res.Source)

	// With the flag: settings is nil → resolution fails → broker does not invent
	_, err = config.ResolveHarnessConfigName(config.HarnessConfigInputs{
		Settings:       nil,
		HubIsAuthority: true,
	})
	require.Error(t, err,
		"with hub authority (nil settings): resolution must fail, not invent a name")
	// Hosted error must not recommend --harness-config (no CLI) or settings
	// (suppressed). It must recommend the template — the only channel
	// the hosted operator can use.
	assert.Contains(t, err.Error(), "template",
		"hosted error must recommend the template channel")
	assert.NotContains(t, err.Error(), "--harness-config",
		"hosted error must not recommend --harness-config (no CLI on hosted tier)")
	assert.NotContains(t, err.Error(), "in settings",
		"hosted error must not recommend settings (suppressed by hub authority)")
}

// TestHubAuthority_ProfileDefaultSuppressed verifies that when the hub is the
// harness-config authority, the broker's profile default rung (rung 6) does
// NOT fire either.
func TestHubAuthority_ProfileDefaultSuppressed(t *testing.T) {
	settings := &config.VersionedSettings{
		Profiles: map[string]config.V1ProfileConfig{
			"dev": {DefaultHarnessConfig: "custom-harness"},
		},
	}

	// Without the flag: profile default fires (rung 6) → "custom-harness"
	res, err := config.ResolveHarnessConfigName(config.HarnessConfigInputs{
		Settings:    settings,
		ProfileName: "dev",
	})
	require.NoError(t, err)
	assert.Equal(t, "custom-harness", res.Name)
	assert.Equal(t, "profile-dev", res.Source)

	// With the flag: settings is nil → profile default does not fire
	_, err = config.ResolveHarnessConfigName(config.HarnessConfigInputs{
		Settings:       nil,
		ProfileName:    "dev",
		HubIsAuthority: true,
	})
	require.Error(t, err,
		"with hub authority (nil settings): profile default must not fire")
}

// TestHubAuthority_HubProvidedNameStillWins verifies that when the hub is the
// authority AND it provided a harness-config name (rung 1), that name is used
// regardless of the flag. This is the normal hosted-mode case after phase 3.
func TestHubAuthority_HubProvidedNameStillWins(t *testing.T) {
	res, err := config.ResolveHarnessConfigName(config.HarnessConfigInputs{
		CLIFlag:  "antigravity", // hub-provided name arrives as CLIFlag
		Settings: nil,           // suppressed by hub authority
	})
	require.NoError(t, err)
	assert.Equal(t, "antigravity", res.Name)
	assert.Equal(t, "cli-flag", res.Source)
}

// TestHubAuthority_TemplateStillWins verifies that template-provided harness
// config (rungs 3-4) still works when hub authority is set. Templates are
// resolved hub-side, so their values are hub-provided.
func TestHubAuthority_TemplateStillWins(t *testing.T) {
	res, err := config.ResolveHarnessConfigName(config.HarnessConfigInputs{
		TemplateCfg: &api.ScionConfig{
			DefaultHarnessConfig: "from-template",
		},
		Settings: nil, // suppressed by hub authority
	})
	require.NoError(t, err)
	assert.Equal(t, "from-template", res.Name)
	assert.Equal(t, "template-default", res.Source)
}

// TestHubAuthority_WorkstationSettingsFallbackWorks verifies that without the
// hub authority flag (workstation mode), the settings default fires as before.
// This is AC 6 for phase 4: workstation mode is unaffected.
func TestHubAuthority_WorkstationSettingsFallbackWorks(t *testing.T) {
	settings := &config.VersionedSettings{
		DefaultHarnessConfig: "antigravity",
	}
	res, err := config.ResolveHarnessConfigName(config.HarnessConfigInputs{
		Settings: settings,
	})
	require.NoError(t, err)
	assert.Equal(t, "antigravity", res.Name,
		"workstation mode: settings default must still fire")
}

// TestHubAuthority_ContextFlag verifies the context plumbing:
// ContextWithHubHarnessConfigAuthority sets the flag,
// IsHubHarnessConfigAuthority reads it.
func TestHubAuthority_ContextFlag(t *testing.T) {
	ctx := context.Background()
	assert.False(t, api.IsHubHarnessConfigAuthority(ctx),
		"bare context: flag must be false")

	ctx = api.ContextWithHubHarnessConfigAuthority(ctx)
	assert.True(t, api.IsHubHarnessConfigAuthority(ctx),
		"after setting: flag must be true")
}

// TestHubAuthority_ProvisionAgent_SuppressesSettings is an integration test
// that drives ProvisionAgent with settings that carry a default_harness_config,
// the hub-authority context flag set, and no CLIFlag or template. It verifies
// that ProvisionAgent reads the flag from context, nils out settings, and
// returns the "no harness-config resolved" error — NOT an error containing
// the settings-provided name.
//
// This pins the `hcSettings = nil` suppression line in provision.go. Without
// that line the settings default fires and ProvisionAgent tries (and fails) to
// find the invented harness-config directory, producing a different error that
// contains the invented name.
func TestHubAuthority_ProvisionAgent_SuppressesSettings(t *testing.T) {
	mockRuntimeForTest(t)
	tmpDir := t.TempDir()

	// Isolate from real HOME / working directory.
	oldWd, _ := os.Getwd()
	_ = os.Chdir(tmpDir)
	defer func() { _ = os.Chdir(oldWd) }()
	originalHome := os.Getenv("HOME")
	defer func() { _ = os.Setenv("HOME", originalHome) }()
	_ = os.Setenv("HOME", tmpDir)

	// Create a project .scion dir with settings that carry a
	// default_harness_config. This is the value the broker would invent
	// if the suppression line were removed.
	projectDir := filepath.Join(tmpDir, "project")
	scionDir := filepath.Join(projectDir, ".scion")
	_ = os.MkdirAll(scionDir, 0755)

	settingsYAML := `schema_version: "1"
default_harness_config: invented-name
`
	_ = os.WriteFile(filepath.Join(scionDir, "settings.yaml"), []byte(settingsYAML), 0644)

	// Create a bare template that does NOT set default_harness_config.
	// This ensures the template chain loads (ProvisionAgent requires it)
	// but contributes nothing to harness-config resolution — forcing
	// the test to depend on whether rung 7 (settings default) fires.
	tplDir := filepath.Join(scionDir, "templates", "bare-tpl")
	_ = os.MkdirAll(tplDir, 0755)
	_ = os.WriteFile(filepath.Join(tplDir, "scion-agent.json"), []byte(`{}`), 0644)

	// Hub-authority context: hosted mode, broker must not invent.
	ctx := api.ContextWithHubHarnessConfigAuthority(context.Background())

	// CLIFlag empty (harnessConfig=""), template has no default_harness_config.
	// Without suppression: rung 7 fires → "invented-name" → harness-config
	// dir not found → error containing "invented-name".
	// With suppression: settings nil → all rungs fail → "no harness-config
	// resolved" error (the hosted variant).
	_, _, _, err := ProvisionAgent(ctx, "test-agent", "bare-tpl", "", "", scionDir, "", "", "", "")
	require.Error(t, err, "ProvisionAgent must fail when hub sent no harness-config and broker is suppressed")
	assert.Contains(t, err.Error(), "no harness-config resolved",
		"error must be the resolution failure, not a harness-config-dir-not-found for an invented name")
	assert.NotContains(t, err.Error(), "invented-name",
		"the settings-provided name must NOT appear — the broker must not invent it")
}

// TestHubAuthority_ErrorMessageHostedVsWorkstation verifies that the error
// message from ResolveHarnessConfigName names only actionable remedies for
// each tier: hosted omits --harness-config and settings; workstation names
// all three channels.
func TestHubAuthority_ErrorMessageHostedVsWorkstation(t *testing.T) {
	// Workstation error: all three channels are real
	_, err := config.ResolveHarnessConfigName(config.HarnessConfigInputs{
		HubIsAuthority: false,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--harness-config",
		"workstation error must mention --harness-config")
	assert.Contains(t, err.Error(), "in the template",
		"workstation error must mention template")
	assert.Contains(t, err.Error(), "in settings",
		"workstation error must mention settings")

	// Hosted error: only the template channel works
	_, err = config.ResolveHarnessConfigName(config.HarnessConfigInputs{
		HubIsAuthority: true,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "template",
		"hosted error must mention template")
	assert.NotContains(t, err.Error(), "--harness-config",
		"hosted error must not mention --harness-config")
	assert.NotContains(t, err.Error(), "in settings",
		"hosted error must not mention settings")
}

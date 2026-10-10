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

package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIsHubAutoStartEnabled(t *testing.T) {
	f, tr := false, true
	var nilSettings *Settings
	assert.True(t, nilSettings.IsHubAutoStartEnabled(), "nil settings")
	assert.True(t, (&Settings{}).IsHubAutoStartEnabled(), "no hub section")
	assert.True(t, (&Settings{Hub: &HubClientConfig{}}).IsHubAutoStartEnabled(), "unset")
	assert.True(t, (&Settings{Hub: &HubClientConfig{AutoStart: &tr}}).IsHubAutoStartEnabled(), "true")
	assert.False(t, (&Settings{Hub: &HubClientConfig{AutoStart: &f}}).IsHubAutoStartEnabled(), "false")
}

// TestHubAutoStart_SettingsRoundTrip checks that hub.auto_start validates
// against the schema, loads from a v1 settings file, and is written by
// UpdateSetting (what 'scion config set --global hub.auto_start false' calls).
func TestHubAutoStart_Schema(t *testing.T) {
	errs, err := ValidateSettings([]byte("schema_version: \"1\"\nhub:\n  auto_start: false\n"), "1")
	require.NoError(t, err)
	assert.Empty(t, errs)
	errs, err = ValidateSettings([]byte("schema_version: \"1\"\nhub:\n  auto_start: sometimes\n"), "1")
	require.NoError(t, err)
	assert.NotEmpty(t, errs, "a non-boolean hub.auto_start must fail validation")
}

func TestHubAutoStart_SettingsRoundTrip(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("SCION_HUB_AUTO_START", "")
	globalDir := filepath.Join(home, ".scion")
	require.NoError(t, os.MkdirAll(globalDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(globalDir, "settings.yaml"),
		[]byte("schema_version: \"1\"\nhub:\n  auto_start: false\n"), 0o644))
	t.Chdir(home)

	s, err := LoadSettings(globalDir)
	require.NoError(t, err)
	assert.False(t, s.IsHubAutoStartEnabled())
	v, err := GetSettingValue(s, "hub.auto_start")
	require.NoError(t, err)
	assert.Equal(t, "false", v)

	require.NoError(t, UpdateSetting(globalDir, "hub.auto_start", "true", true))
	s, err = LoadSettings(globalDir)
	require.NoError(t, err)
	assert.True(t, s.IsHubAutoStartEnabled())

	vs, err := LoadSingleFileVersioned(globalDir)
	require.NoError(t, err)
	require.NotNil(t, vs.Hub)
	require.NotNil(t, vs.Hub.AutoStart)
	assert.True(t, *vs.Hub.AutoStart)
}

// TestHubAutoStart_EnvNotMappedIntoSettings checks that a value koanf
// cannot decode as a boolean does not break settings loading: the CLI
// reads SCION_HUB_AUTO_START itself.
func TestHubAutoStart_EnvNotMappedIntoSettings(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("SCION_HUB_AUTO_START", "not-a-bool")
	globalDir := filepath.Join(home, ".scion")
	require.NoError(t, os.MkdirAll(globalDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(globalDir, "settings.yaml"),
		[]byte("schema_version: \"1\"\nhub:\n  auto_start: false\n"), 0o644))
	t.Chdir(home)

	s, err := LoadSettings(globalDir)
	require.NoError(t, err)
	assert.False(t, s.IsHubAutoStartEnabled(), "the env var must not override the file value in settings")
}

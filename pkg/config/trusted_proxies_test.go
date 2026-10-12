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
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidateTrustedProxies(t *testing.T) {
	valid := [][]string{
		nil,
		{},
		{"10.0.0.1"},
		{"10.0.0.0/8", "192.168.1.0/24"},
		{"2001:db8::1", "2001:db8::/32"},
		{" 35.191.0.0/16 "},
	}
	for _, entries := range valid {
		assert.NoError(t, ValidateTrustedProxies(entries), "entries %q", entries)
	}

	invalid := []struct {
		entries []string
		want    string
	}{
		{[]string{"10.0.0.0/8", "10.0.0/8"}, `server.hub.trusted_proxies[1]: "10.0.0/8" is not an IP address or a CIDR range`},
		{[]string{"proxy.example.com"}, `server.hub.trusted_proxies[0]: "proxy.example.com" is not an IP address or a CIDR range`},
		{[]string{"10.0.0.0/33"}, `server.hub.trusted_proxies[0]: "10.0.0.0/33" is not an IP address or a CIDR range`},
		{[]string{"fe80::1%eth0"}, `server.hub.trusted_proxies[0]: "fe80::1%eth0" has an IPv6 zone suffix`},
		{[]string{"fe80::%eth0/64"}, `server.hub.trusted_proxies[0]: "fe80::%eth0/64" has an IPv6 zone suffix`},
		{[]string{"10.0.0.1", " "}, `server.hub.trusted_proxies[1]: empty entry`},
	}
	for _, tt := range invalid {
		err := ValidateTrustedProxies(tt.entries)
		require.Error(t, err, "entries %q", tt.entries)
		assert.Contains(t, err.Error(), tt.want)
	}

	long := strings.Repeat("x", 200)
	err := ValidateTrustedProxies([]string{long})
	require.Error(t, err)
	assert.NotContains(t, err.Error(), long, "a long entry is shortened in the message")
}

// trustedProxiesSchemaEnvVar returns the x-env-var the settings schema
// declares for server.hub.trusted_proxies.
func trustedProxiesSchemaEnvVar(t *testing.T) string {
	t.Helper()
	data, err := schemasFS.ReadFile(settingsSchemaFiles["1"])
	require.NoError(t, err)
	var root map[string]interface{}
	require.NoError(t, json.Unmarshal(data, &root))
	prop := findSchemaProperty(t, root, "server", "hub", "trusted_proxies")
	envVar, _ := prop["x-env-var"].(string)
	return envVar
}

func TestLoadGlobalConfig_TrustedProxies(t *testing.T) {
	envVar := trustedProxiesSchemaEnvVar(t)
	require.Equal(t, "SCION_SERVER_HUB_TRUSTEDPROXIES", envVar)

	t.Run("unset is empty", func(t *testing.T) {
		tmpDir := t.TempDir()
		t.Setenv("HOME", tmpDir)
		configPath := filepath.Join(tmpDir, "settings.yaml")
		require.NoError(t, os.WriteFile(configPath, []byte("schema_version: \"1\"\nserver:\n  hub:\n    port: 9810\n"), 0o644))
		cfg, err := LoadGlobalConfig(configPath)
		require.NoError(t, err)
		assert.Empty(t, cfg.Hub.TrustedProxies)
	})

	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)
	configPath := filepath.Join(tmpDir, "settings.yaml")
	require.NoError(t, os.WriteFile(configPath, []byte(`
schema_version: "1"
server:
  hub:
    trusted_proxies:
      - 10.0.0.0/8
      - " 2001:db8::1 "
`), 0o644))

	t.Run("settings file", func(t *testing.T) {
		cfg, err := LoadGlobalConfig(configPath)
		require.NoError(t, err)
		assert.Equal(t, []string{"10.0.0.0/8", "2001:db8::1"}, cfg.Hub.TrustedProxies)
		assert.NoError(t, ValidateTrustedProxies(cfg.Hub.TrustedProxies))
	})

	t.Run("env comma list overrides the file", func(t *testing.T) {
		t.Setenv(envVar, "35.191.0.0/16, 130.211.0.0/22,")
		cfg, err := LoadGlobalConfig(configPath)
		require.NoError(t, err)
		assert.Equal(t, []string{"35.191.0.0/16", "130.211.0.0/22"}, cfg.Hub.TrustedProxies)
	})

	t.Run("empty env leaves the file value", func(t *testing.T) {
		t.Setenv(envVar, "")
		cfg, err := LoadGlobalConfig(configPath)
		require.NoError(t, err)
		assert.Equal(t, []string{"10.0.0.0/8", "2001:db8::1"}, cfg.Hub.TrustedProxies)
	})
}

func TestTrustedProxies_V1RoundTrip(t *testing.T) {
	gc := ConvertV1ServerToGlobalConfig(&V1ServerConfig{
		Hub: &V1ServerHubConfig{TrustedProxies: []string{"10.0.0.0/8", "192.0.2.7"}},
	})
	assert.Equal(t, []string{"10.0.0.0/8", "192.0.2.7"}, gc.Hub.TrustedProxies)

	v1 := ConvertGlobalToV1ServerConfig(gc)
	require.NotNil(t, v1.Hub)
	assert.Equal(t, []string{"10.0.0.0/8", "192.0.2.7"}, v1.Hub.TrustedProxies)

	empty := ConvertGlobalToV1ServerConfig(&GlobalConfig{})
	require.NotNil(t, empty.Hub)
	assert.Nil(t, empty.Hub.TrustedProxies, "an empty list is omitted from settings.yaml")
}

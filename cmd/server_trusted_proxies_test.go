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

package cmd

import (
	"testing"

	"github.com/GoogleCloudPlatform/scion/pkg/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestValidateServerPreflight_TrustedProxies: an entry of
// server.hub.trusted_proxies that is not an IP address or CIDR range stops
// hub startup with a message naming the index; an empty list or valid
// entries start.
func TestValidateServerPreflight_TrustedProxies(t *testing.T) {
	t.Cleanup(resetServerFlags)
	for _, tc := range []struct {
		name    string
		value   []string
		wantErr string
	}{
		{name: "unset"},
		{name: "valid", value: []string{"10.0.0.0/8", "2001:db8::1"}},
		{name: "hostname", value: []string{"10.0.0.1", "proxy.example.com"}, wantErr: "server.hub.trusted_proxies[1]"},
		{name: "zone", value: []string{"fe80::1%eth0"}, wantErr: "IPv6 zone suffix"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resetServerFlags()
			enableHub = true
			cfg := &config.GlobalConfig{}
			cfg.Hub.TrustedProxies = tc.value
			err := validateServerPreflight(cfg)
			if tc.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantErr)
		})
	}
}

// TestBuildHubServerConfig_TrustedProxies: the list reaches the hub
// ServerConfig, and an unset list stays empty.
func TestBuildHubServerConfig_TrustedProxies(t *testing.T) {
	cfg := &config.GlobalConfig{}
	assert.Empty(t, buildHubServerConfig(cfg, "https://hub.example.com", "", nil, false, "", nil).TrustedProxies)

	cfg.Hub.TrustedProxies = []string{"10.0.0.0/8", "192.0.2.7"}
	assert.Equal(t, []string{"10.0.0.0/8", "192.0.2.7"},
		buildHubServerConfig(cfg, "https://hub.example.com", "", nil, false, "", nil).TrustedProxies)
}

// TestBuildHubServerConfig_HeaderProxyAuth: the forwarded-identity headers
// are enabled only by auth.mode "proxy" with auth.proxy.provider "header".
// Setting server.hub.trusted_proxies alone never enables them.
func TestBuildHubServerConfig_HeaderProxyAuth(t *testing.T) {
	for _, tc := range []struct {
		name     string
		mode     string
		proxy    *config.ProxyAuthConfig
		proxies  []string
		wantFlag bool
	}{
		{name: "default", wantFlag: false},
		{name: "trusted proxies only", proxies: []string{"10.0.0.0/8"}, wantFlag: false},
		{name: "oauth mode with header provider block", mode: "oauth", proxy: &config.ProxyAuthConfig{Provider: "header"}, proxies: []string{"10.0.0.0/8"}, wantFlag: false},
		{name: "proxy mode without provider block", mode: "proxy", proxies: []string{"10.0.0.0/8"}, wantFlag: false},
		{name: "proxy mode iap", mode: "proxy", proxy: &config.ProxyAuthConfig{Provider: "iap"}, proxies: []string{"10.0.0.0/8"}, wantFlag: false},
		{name: "proxy mode header", mode: "proxy", proxy: &config.ProxyAuthConfig{Provider: "header"}, proxies: []string{"10.0.0.0/8"}, wantFlag: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &config.GlobalConfig{}
			cfg.Auth.Mode = tc.mode
			cfg.Auth.Proxy = tc.proxy
			cfg.Hub.TrustedProxies = tc.proxies
			got := buildHubServerConfig(cfg, "https://hub.example.com", "", nil, false, "", nil)
			assert.Equal(t, tc.wantFlag, got.HeaderProxyAuth)
		})
	}
}

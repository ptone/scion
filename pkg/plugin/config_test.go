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

package plugin

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestPluginConstants(t *testing.T) {
	assert.Equal(t, "broker", PluginTypeBroker)
	assert.Equal(t, "harness", PluginTypeHarness)
	assert.Equal(t, uint(1), uint(BrokerPluginProtocolVersion))
	assert.Equal(t, uint(1), uint(HarnessPluginProtocolVersion))
	assert.Equal(t, "SCION_PLUGIN", MagicCookieKey)
	assert.Equal(t, "scion-plugin-v1", MagicCookieValue)
	assert.Equal(t, "scion-plugin-", PluginBinaryPrefix)
}

func TestPluginsConfig(t *testing.T) {
	cfg := PluginsConfig{
		Broker: map[string]PluginEntry{
			"nats": {
				Path: "/usr/local/bin/scion-plugin-nats",
				Config: map[string]string{
					"url": "nats://localhost:4222",
				},
			},
		},
		Harness: map[string]PluginEntry{
			"cursor": {
				Config: map[string]string{
					"image": "cursor-agent:latest",
				},
			},
		},
	}

	assert.Len(t, cfg.Broker, 1)
	assert.Equal(t, "/usr/local/bin/scion-plugin-nats", cfg.Broker["nats"].Path)
	assert.Equal(t, "nats://localhost:4222", cfg.Broker["nats"].Config["url"])

	assert.Len(t, cfg.Harness, 1)
	assert.Empty(t, cfg.Harness["cursor"].Path)
	assert.Equal(t, "cursor-agent:latest", cfg.Harness["cursor"].Config["image"])
}

func TestPluginsConfigFromEntries(t *testing.T) {
	brokerEntries := map[string]V1PluginEntryLike{
		"nats": {
			Path:   "/path/to/nats",
			Config: map[string]string{"url": "nats://localhost"},
		},
	}
	harnessEntries := map[string]V1PluginEntryLike{
		"cursor": {
			Config: map[string]string{"image": "cursor:latest"},
		},
	}

	cfg := PluginsConfigFromEntries(brokerEntries, harnessEntries)
	assert.Equal(t, "/path/to/nats", cfg.Broker["nats"].Path)
	assert.Equal(t, "nats://localhost", cfg.Broker["nats"].Config["url"])
	assert.Equal(t, "cursor:latest", cfg.Harness["cursor"].Config["image"])
}

func TestPluginEntry_SelfManaged(t *testing.T) {
	entry := PluginEntry{
		SelfManaged: true,
		Address:     "localhost:9090",
		Config: map[string]string{
			"hub_endpoint": "https://hub.example.com",
		},
	}

	assert.True(t, entry.SelfManaged)
	assert.Equal(t, "localhost:9090", entry.Address)
	assert.Empty(t, entry.Path)
}

func TestPluginsConfigFromEntries_SelfManaged(t *testing.T) {
	brokerEntries := map[string]V1PluginEntryLike{
		"googlechat": {
			SelfManaged: true,
			Address:     "localhost:9090",
			Config:      map[string]string{"project_id": "my-gcp-project"},
		},
	}

	cfg := PluginsConfigFromEntries(brokerEntries, nil)
	assert.True(t, cfg.Broker["googlechat"].SelfManaged)
	assert.Equal(t, "localhost:9090", cfg.Broker["googlechat"].Address)
	assert.Equal(t, "my-gcp-project", cfg.Broker["googlechat"].Config["project_id"])
	assert.Empty(t, cfg.Broker["googlechat"].Path)
}

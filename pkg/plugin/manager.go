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
	"fmt"
	"log/slog"
	"os/exec"
	"sync"

	"github.com/GoogleCloudPlatform/scion/pkg/api"
	"github.com/GoogleCloudPlatform/scion/pkg/broker"
	goplugin "github.com/hashicorp/go-plugin"
)

// Manager owns the lifecycle of all loaded plugins.
// It handles discovery, loading, dispensing, and shutdown of plugin processes.
type Manager struct {
	clients map[string]*goplugin.Client // "type:name" -> client
	mu      sync.RWMutex
	logger  *slog.Logger
}

// NewManager creates a new plugin manager.
func NewManager(logger *slog.Logger) *Manager {
	if logger == nil {
		logger = slog.Default()
	}
	return &Manager{
		clients: make(map[string]*goplugin.Client),
		logger:  logger,
	}
}

// LoadAll discovers and loads all plugins from the given configuration and plugins directory.
func (m *Manager) LoadAll(cfg PluginsConfig, pluginsDir string) error {
	discovered := DiscoverPlugins(cfg, pluginsDir, m.logger)

	for _, dp := range discovered {
		if err := m.loadPlugin(dp); err != nil {
			m.logger.Error("Failed to load plugin",
				"type", dp.Type,
				"name", dp.Name,
				"path", dp.Path,
				"error", err,
			)
			continue
		}
		m.logger.Info("Loaded plugin",
			"type", dp.Type,
			"name", dp.Name,
			"path", dp.Path,
		)
	}

	return nil
}

// LoadOne loads a single plugin by type and name from the given configuration.
func (m *Manager) LoadOne(pluginType, name string, entry PluginEntry, pluginsDir string) error {
	path := resolvePluginPath(name, pluginType, entry.Path, pluginsDir, m.logger)
	if path == "" {
		return fmt.Errorf("plugin binary not found: %s/%s", pluginType, name)
	}
	return m.loadPlugin(DiscoveredPlugin{
		Name:       name,
		Type:       pluginType,
		Path:       path,
		Config:     entry.Config,
		FromConfig: true,
	})
}

// loadPlugin starts a plugin process and stores its client.
func (m *Manager) loadPlugin(dp DiscoveredPlugin) error {
	var protocolVersion uint
	var pluginMap map[string]goplugin.Plugin

	switch dp.Type {
	case PluginTypeBroker:
		protocolVersion = BrokerPluginProtocolVersion
		pluginMap = map[string]goplugin.Plugin{
			BrokerPluginName: &BrokerPlugin{},
		}
	case PluginTypeHarness:
		protocolVersion = HarnessPluginProtocolVersion
		pluginMap = map[string]goplugin.Plugin{
			HarnessPluginName: &HarnessPlugin{},
		}
	default:
		return fmt.Errorf("unknown plugin type: %s", dp.Type)
	}

	client := goplugin.NewClient(&goplugin.ClientConfig{
		HandshakeConfig: goplugin.HandshakeConfig{
			ProtocolVersion:  protocolVersion,
			MagicCookieKey:   MagicCookieKey,
			MagicCookieValue: MagicCookieValue,
		},
		Plugins: pluginMap,
		Cmd:     exec.Command(dp.Path),
		Logger:  newHclogAdapter(m.logger),
	})

	// Start the plugin process and get the RPC client
	rpcClient, err := client.Client()
	if err != nil {
		client.Kill()
		return fmt.Errorf("failed to start plugin %s/%s: %w", dp.Type, dp.Name, err)
	}

	// Dispense the plugin interface
	var dispenseName string
	switch dp.Type {
	case PluginTypeBroker:
		dispenseName = BrokerPluginName
	case PluginTypeHarness:
		dispenseName = HarnessPluginName
	}

	raw, err := rpcClient.Dispense(dispenseName)
	if err != nil {
		client.Kill()
		return fmt.Errorf("failed to dispense plugin %s/%s: %w", dp.Type, dp.Name, err)
	}

	// For broker plugins, configure them immediately
	if dp.Type == PluginTypeBroker {
		if brokerClient, ok := raw.(*BrokerRPCClient); ok {
			config := dp.Config
			if config == nil {
				config = make(map[string]string)
			}
			if err := brokerClient.Configure(config); err != nil {
				client.Kill()
				return fmt.Errorf("failed to configure broker plugin %s: %w", dp.Name, err)
			}
		}
	}

	key := dp.Type + ":" + dp.Name
	m.mu.Lock()
	// Kill any existing plugin with the same key
	if existing, ok := m.clients[key]; ok {
		existing.Kill()
	}
	m.clients[key] = client
	m.mu.Unlock()

	return nil
}

// Get returns the dispensed plugin interface for the given type and name.
func (m *Manager) Get(pluginType, name string) (interface{}, error) {
	key := pluginType + ":" + name
	m.mu.RLock()
	client, ok := m.clients[key]
	m.mu.RUnlock()

	if !ok {
		return nil, fmt.Errorf("plugin not loaded: %s/%s", pluginType, name)
	}

	rpcClient, err := client.Client()
	if err != nil {
		return nil, fmt.Errorf("failed to get RPC client for %s/%s: %w", pluginType, name, err)
	}

	var dispenseName string
	switch pluginType {
	case PluginTypeBroker:
		dispenseName = BrokerPluginName
	case PluginTypeHarness:
		dispenseName = HarnessPluginName
	default:
		return nil, fmt.Errorf("unknown plugin type: %s", pluginType)
	}

	return rpcClient.Dispense(dispenseName)
}

// GetBroker returns a broker.MessageBroker backed by the named broker plugin.
func (m *Manager) GetBroker(name string) (broker.MessageBroker, error) {
	raw, err := m.Get(PluginTypeBroker, name)
	if err != nil {
		return nil, err
	}

	rpcClient, ok := raw.(*BrokerRPCClient)
	if !ok {
		return nil, fmt.Errorf("plugin %s is not a broker plugin", name)
	}

	return NewBrokerPluginAdapter(rpcClient), nil
}

// GetHarness returns an api.Harness backed by the named harness plugin.
func (m *Manager) GetHarness(name string) (api.Harness, error) {
	raw, err := m.Get(PluginTypeHarness, name)
	if err != nil {
		return nil, err
	}

	harnessClient, ok := raw.(*HarnessRPCClient)
	if !ok {
		return nil, fmt.Errorf("plugin %s is not a harness plugin", name)
	}

	return harnessClient, nil
}

// HasPlugin returns true if a plugin with the given type and name is loaded.
func (m *Manager) HasPlugin(pluginType, name string) bool {
	key := pluginType + ":" + name
	m.mu.RLock()
	_, ok := m.clients[key]
	m.mu.RUnlock()
	return ok
}

// ListPlugins returns a list of all loaded plugin keys ("type:name").
func (m *Manager) ListPlugins() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()

	keys := make([]string, 0, len(m.clients))
	for k := range m.clients {
		keys = append(keys, k)
	}
	return keys
}

// Shutdown kills all plugin processes gracefully.
func (m *Manager) Shutdown() {
	m.mu.Lock()
	defer m.mu.Unlock()

	for key, client := range m.clients {
		m.logger.Info("Shutting down plugin", "plugin", key)
		client.Kill()
	}
	m.clients = make(map[string]*goplugin.Client)

	goplugin.CleanupClients()
}

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
	"encoding/json"
	"net/http"
	"strings"

	"github.com/GoogleCloudPlatform/scion/pkg/config"
	"github.com/GoogleCloudPlatform/scion/pkg/eventbus"
	scionplugin "github.com/GoogleCloudPlatform/scion/pkg/plugin"
)

// IntegrationStatus describes an integration's runtime status for the admin API.
type IntegrationStatus struct {
	Type           string            `json:"type"`
	Enabled        bool              `json:"enabled"`
	Running        bool              `json:"running"`
	Status         string            `json:"status"`
	Message        string            `json:"message,omitempty"`
	Details        map[string]string `json:"details,omitempty"`
	DeploymentMode string            `json:"deployment_mode"`
}

// handleAdminIntegrations returns the list of all integrations with status.
// GET /api/v1/admin/integrations
func (s *Server) handleAdminIntegrations(w http.ResponseWriter, r *http.Request) {
	if err := assertLoopback(r); err != nil {
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	integrations := []IntegrationStatus{
		s.getTelegramStatus(),
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"integrations": integrations,
	})
}

// handleAdminTelegramEnable hot-starts the telegram plugin.
// POST /api/v1/admin/integrations/telegram/enable
func (s *Server) handleAdminTelegramEnable(w http.ResponseWriter, r *http.Request) {
	if err := assertLoopback(r); err != nil {
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	pluginMgr, pluginsDir := s.GetPluginManager()
	if pluginMgr == nil {
		http.Error(w, "plugin manager not available", http.StatusInternalServerError)
		return
	}

	// Already running — idempotent success.
	if pluginMgr.HasPlugin(scionplugin.PluginTypeBroker, "telegram") {
		status := s.getTelegramStatus()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success":     true,
			"integration": status,
		})
		return
	}

	// Load config from settings.yaml.
	globalDir, err := config.GetGlobalDir()
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to locate settings")
		return
	}
	vs, err := config.LoadVersionedSettings(globalDir)
	if err != nil || vs == nil || vs.Server == nil || vs.Server.Plugins == nil {
		writeJSONError(w, http.StatusBadRequest, "no telegram configuration found — complete onboarding first")
		return
	}
	entry, ok := vs.Server.Plugins.Broker["telegram"]
	if !ok || entry.Config["bot_token"] == "" {
		writeJSONError(w, http.StatusBadRequest, "no telegram bot token configured — complete onboarding first")
		return
	}

	pluginEntry := scionplugin.PluginEntry{
		Config: entry.Config,
		Env:    map[string]string{"SCION_TELEGRAM_V2": "1"},
	}

	if err := WireBrokerPlugin(r.Context(), pluginMgr, s, "telegram", pluginEntry, pluginsDir); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to start telegram: "+err.Error())
		return
	}

	// Persist enabled state: add "telegram" to message_broker.types.
	persistTelegramEnabled(globalDir, true)

	status := s.getTelegramStatus()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success":     true,
		"integration": status,
	})
}

// handleAdminTelegramDisable stops the telegram plugin and removes it from the broker.
// POST /api/v1/admin/integrations/telegram/disable
func (s *Server) handleAdminTelegramDisable(w http.ResponseWriter, r *http.Request) {
	if err := assertLoopback(r); err != nil {
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	pluginMgr, _ := s.GetPluginManager()

	// Stop the plugin subprocess (idempotent — no-op if not running).
	if pluginMgr != nil {
		pluginMgr.StopPlugin(scionplugin.PluginTypeBroker, "telegram")
	}

	// Remove spoke from fan-out (idempotent).
	if proxy := s.GetMessageBrokerProxy(); proxy != nil {
		if fanout, ok := proxy.GetBus().(*eventbus.FanOutEventBus); ok {
			fanout.RemoveSpoke("telegram")
		}
	}

	// Persist disabled state: remove "telegram" from message_broker.types.
	globalDir, _ := config.GetGlobalDir()
	if globalDir != "" {
		persistTelegramEnabled(globalDir, false)
	}

	status := s.getTelegramStatus()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success":     true,
		"integration": status,
	})
}

// getTelegramStatus builds the status object for the telegram integration.
func (s *Server) getTelegramStatus() IntegrationStatus {
	status := IntegrationStatus{
		Type:           "telegram",
		DeploymentMode: "subprocess",
	}

	// Check if configured in settings.
	globalDir, _ := config.GetGlobalDir()
	if globalDir != "" {
		if vs, err := config.LoadVersionedSettings(globalDir); err == nil && vs != nil && vs.Server != nil {
			if vs.Server.Plugins != nil {
				if entry, ok := vs.Server.Plugins.Broker["telegram"]; ok {
					if entry.Config["bot_token"] != "" {
						status.Details = map[string]string{
							"inbound_mode": entry.Config["inbound_mode"],
						}
					}
				}
			}
			if vs.Server.MessageBroker != nil {
				for _, t := range vs.Server.MessageBroker.Types {
					if t == "telegram" {
						status.Enabled = true
						break
					}
				}
			}
		}
	}

	// Check runtime status via plugin manager.
	pluginMgr, _ := s.GetPluginManager()
	if pluginMgr != nil && pluginMgr.HasPlugin(scionplugin.PluginTypeBroker, "telegram") {
		status.Running = true
		raw, err := pluginMgr.Get(scionplugin.PluginTypeBroker, "telegram")
		if err == nil {
			if rpc, ok := raw.(*scionplugin.BrokerRPCClient); ok {
				if health, hErr := rpc.HealthCheck(); hErr == nil && health != nil {
					status.Status = health.Status
					status.Message = health.Message
					if status.Details == nil {
						status.Details = make(map[string]string)
					}
					for k, v := range health.Details {
						status.Details[k] = v
					}
				}
			}
		}
	} else {
		if status.Enabled {
			status.Status = "stopped"
			status.Message = "plugin not running"
		} else {
			status.Status = "disabled"
			status.Message = "integration disabled"
		}
	}

	return status
}

// persistTelegramEnabled adds or removes "telegram" from message_broker.types in settings.yaml.
// Bot config (bot_token, webhook_secret) is always preserved.
func persistTelegramEnabled(globalDir string, enabled bool) {
	vs, err := config.LoadVersionedSettings(globalDir)
	if err != nil || vs == nil {
		return
	}
	if vs.Server == nil {
		vs.Server = &config.V1ServerConfig{}
	}
	if vs.Server.MessageBroker == nil {
		vs.Server.MessageBroker = &config.V1MessageBrokerConfig{}
	}

	types := vs.Server.MessageBroker.Types
	if enabled {
		has := false
		for _, t := range types {
			if t == "telegram" {
				has = true
				break
			}
		}
		if !has {
			vs.Server.MessageBroker.Types = append(types, "telegram")
		}
		vs.Server.MessageBroker.Enabled = true
	} else {
		filtered := types[:0:0]
		for _, t := range types {
			if t != "telegram" {
				filtered = append(filtered, t)
			}
		}
		vs.Server.MessageBroker.Types = filtered
		if len(filtered) == 0 {
			vs.Server.MessageBroker.Enabled = false
		}
	}

	config.SaveVersionedSettings(globalDir, vs)
}

func writeJSONError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]string{"error": message})
}

// isTelegramInBrokerTypes checks if "telegram" is in message_broker.types at startup.
func isTelegramInBrokerTypes(types []string) bool {
	for _, t := range types {
		if strings.EqualFold(t, "telegram") {
			return true
		}
	}
	return false
}

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
	"log/slog"
	"net/http"

	scionplugin "github.com/GoogleCloudPlatform/scion/pkg/plugin"
)

// handleTelegramValidate validates a Telegram bot token by calling getMe.
// POST /api/v1/system/telegram/validate
func (s *Server) handleTelegramValidate(w http.ResponseWriter, r *http.Request) {
	if err := assertLoopback(r); err != nil {
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	var req struct {
		BotToken string `json:"bot_token"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if req.BotToken == "" {
		http.Error(w, "bot_token is required", http.StatusBadRequest)
		return
	}

	result, err := ValidateTelegramToken(r.Context(), req.BotToken)
	if err != nil {
		http.Error(w, "validation failed", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

// handleTelegramSetup validates a token, persists config, and hot-starts the plugin.
// POST /api/v1/system/telegram/setup
func (s *Server) handleTelegramSetup(w http.ResponseWriter, r *http.Request) {
	if err := assertLoopback(r); err != nil {
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	var req struct {
		BotToken string `json:"bot_token"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if req.BotToken == "" {
		http.Error(w, "bot_token is required", http.StatusBadRequest)
		return
	}

	// Step 1: Validate token
	validation, err := ValidateTelegramToken(r.Context(), req.BotToken)
	if err != nil || !validation.Valid {
		errMsg := "invalid bot token"
		if validation != nil && validation.Error != "" {
			errMsg = validation.Error
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(TelegramSetupResult{Error: errMsg})
		return
	}

	// Step 2: Generate webhook secret
	webhookSecret, err := generateWebhookSecret()
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(TelegramSetupResult{Error: "failed to generate webhook secret"})
		return
	}

	// Step 3: Hot-start the plugin BEFORE persisting. If the plugin fails to
	// start (binary missing, wiring error), we return an error and leave no
	// half-applied state so the user can retry cleanly.
	pluginMgr, pluginsDir := s.GetPluginManager()
	if pluginMgr == nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(TelegramSetupResult{Error: "plugin manager not available — cannot start bot"})
		return
	}

	pluginEntry := scionplugin.PluginEntry{
		Config: map[string]string{
			"bot_token":      req.BotToken,
			"webhook_secret": webhookSecret,
			"inbound_mode":   "poll", // must match plugin's accepted values: "poll" or "webhook" (broker_v2.go)
		},
		Env: map[string]string{
			"SCION_TELEGRAM_V2": "1", // hub requires v2 broker (group links, /setup, project_slug_map)
		},
	}

	if err := WireBrokerPlugin(r.Context(), pluginMgr, s, "telegram", pluginEntry, pluginsDir); err != nil {
		slog.Error("Telegram hot-start failed", "error", err)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(TelegramSetupResult{Error: "bot failed to start — check server logs for details"})
		return
	}

	// Step 4: Plugin is running — now persist to settings.yaml for restart durability.
	if err := PersistTelegramConfig(req.BotToken, webhookSecret); err != nil {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(TelegramSetupResult{
			Success: true,
			Bot:     validation.Bot,
			Message: "Bot @" + validation.Bot.Username + " is running but config failed to save: " + err.Error() + ". The bot will stop on restart.",
		})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(TelegramSetupResult{
		Success: true,
		Bot:     validation.Bot,
		Message: "Bot @" + validation.Bot.Username + " is connected and running in polling mode.",
	})
}

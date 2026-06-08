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
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/GoogleCloudPlatform/scion/pkg/api"
	"github.com/GoogleCloudPlatform/scion/pkg/config"
	"github.com/GoogleCloudPlatform/scion/pkg/eventbus"
	scionplugin "github.com/GoogleCloudPlatform/scion/pkg/plugin"
	"github.com/GoogleCloudPlatform/scion/pkg/store"
	"github.com/GoogleCloudPlatform/scion/pkg/util/logging"
)

// wireBrokerMu serializes the get-or-create proxy path in WireBrokerPlugin so
// concurrent setups can't both create a FanOutBroker (the second StartMessageBroker
// is a no-op, losing that spoke).
var wireBrokerMu sync.Mutex

// EnsureTelegramEnv ensures the plugin entry for the telegram broker includes
// SCION_TELEGRAM_V2=1. The hub requires the v2 broker (group links, /setup,
// project_slug_map). This is the SINGLE mechanism for both cold-start and
// hot-start — it derives the env from the plugin name, not from persisted
// settings, so existing settings.yaml files without the Env entry still
// launch telegram as v2.
func EnsureTelegramEnv(name string, entry *scionplugin.PluginEntry) {
	if name != "telegram" || entry.SelfManaged {
		return
	}
	if entry.Env == nil {
		entry.Env = make(map[string]string)
	}
	entry.Env["SCION_TELEGRAM_V2"] = "1"
}

// TelegramBotInfo holds information returned by the Telegram getMe API.
type TelegramBotInfo struct {
	ID        int64  `json:"id"`
	IsBot     bool   `json:"is_bot"`
	FirstName string `json:"first_name"`
	Username  string `json:"username"`
}

// ValidateTokenResult is the result of validating a Telegram bot token.
type ValidateTokenResult struct {
	Valid bool             `json:"valid"`
	Bot   *TelegramBotInfo `json:"bot,omitempty"`
	Error string           `json:"error,omitempty"`
}

// TelegramSetupResult is the result of setting up a Telegram bot.
type TelegramSetupResult struct {
	Success bool             `json:"success"`
	Bot     *TelegramBotInfo `json:"bot,omitempty"`
	Message string           `json:"message,omitempty"`
	Error   string           `json:"error,omitempty"`
}

// ValidateTelegramToken validates a bot token by calling the Telegram getMe API.
func ValidateTelegramToken(ctx context.Context, token string) (*ValidateTokenResult, error) {
	apiURL := fmt.Sprintf("https://api.telegram.org/bot%s/getMe", url.PathEscape(token))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
	if err != nil {
		return &ValidateTokenResult{Valid: false, Error: "failed to create request"}, nil
	}

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return &ValidateTokenResult{Valid: false, Error: "failed to connect to Telegram API"}, nil
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return &ValidateTokenResult{Valid: false, Error: "failed to read response"}, nil
	}

	var apiResp struct {
		OK     bool            `json:"ok"`
		Result TelegramBotInfo `json:"result"`
	}
	if err := json.Unmarshal(body, &apiResp); err != nil {
		return &ValidateTokenResult{Valid: false, Error: "invalid response from Telegram"}, nil
	}

	if !apiResp.OK {
		return &ValidateTokenResult{Valid: false, Error: "invalid bot token"}, nil
	}

	return &ValidateTokenResult{
		Valid: true,
		Bot:   &apiResp.Result,
	}, nil
}

// generateWebhookSecret generates a cryptographically random webhook secret.
func generateWebhookSecret() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("failed to generate webhook secret: %w", err)
	}
	return base64.URLEncoding.EncodeToString(b), nil
}

// PersistTelegramConfig writes the telegram plugin configuration to settings.yaml.
func PersistTelegramConfig(botToken, webhookSecret string) error {
	globalDir, err := config.GetGlobalDir()
	if err != nil {
		return fmt.Errorf("failed to get global directory: %w", err)
	}

	vs, err := config.LoadVersionedSettings(globalDir)
	if err != nil {
		vs = &config.VersionedSettings{SchemaVersion: "1"}
	}

	if vs.Server == nil {
		vs.Server = &config.V1ServerConfig{}
	}
	if vs.Server.Plugins == nil {
		vs.Server.Plugins = &config.V1PluginsConfig{}
	}
	if vs.Server.Plugins.Broker == nil {
		vs.Server.Plugins.Broker = make(map[string]config.V1PluginEntry)
	}

	existing := vs.Server.Plugins.Broker["telegram"]
	if existing.Config == nil {
		existing.Config = make(map[string]string)
	}
	existing.Config["bot_token"] = botToken
	existing.Config["webhook_secret"] = webhookSecret
	existing.Config["inbound_mode"] = "poll" // must match plugin's accepted values: "poll" or "webhook" (broker_v2.go)
	if existing.Env == nil {
		existing.Env = make(map[string]string)
	}
	existing.Env["SCION_TELEGRAM_V2"] = "1"
	vs.Server.Plugins.Broker["telegram"] = existing

	if vs.Server.MessageBroker == nil {
		vs.Server.MessageBroker = &config.V1MessageBrokerConfig{}
	}
	vs.Server.MessageBroker.Enabled = true

	types := vs.Server.MessageBroker.Types
	hasTelegram := false
	for _, t := range types {
		if t == "telegram" {
			hasTelegram = true
			break
		}
	}
	if !hasTelegram {
		vs.Server.MessageBroker.Types = append(types, "telegram")
	}

	return config.SaveVersionedSettings(globalDir, vs)
}

// WireBrokerPlugin loads a broker plugin, injects hub credentials, and wires it
// into the message broker. This is the shared function used by both hub startup
// and the hot-start path to avoid drift.
func WireBrokerPlugin(
	ctx context.Context,
	pluginMgr *scionplugin.Manager,
	srv *Server,
	pluginName string,
	pluginEntry scionplugin.PluginEntry,
	pluginsDir string,
) error {
	log := logging.Subsystem("hub.telegram-setup")

	if err := pluginMgr.LoadOne(scionplugin.PluginTypeBroker, pluginName, pluginEntry, pluginsDir); err != nil {
		return fmt.Errorf("failed to load broker plugin %q: %w", pluginName, err)
	}
	log.Info("Loaded broker plugin", "name", pluginName)

	bus, err := pluginMgr.GetBroker(pluginName)
	if err != nil {
		pluginMgr.StopPlugin(scionplugin.PluginTypeBroker, pluginName)
		return fmt.Errorf("failed to get broker adapter for %q: %w", pluginName, err)
	}

	if err := InjectHubCredentials(ctx, pluginMgr, srv, pluginName); err != nil {
		log.Warn("Failed to inject hub credentials (plugin may still work for inbound)", "error", err)
	}

	spoke := eventbus.NamedEventBus{
		Name:     pluginName,
		Bus:      bus,
		Observer: CheckObserver(pluginMgr, pluginName),
	}

	// Serialize proxy check + creation so concurrent setups can't both see
	// proxy==nil and each create their own FanOutBroker.
	wireBrokerMu.Lock()
	defer wireBrokerMu.Unlock()

	proxy := srv.GetMessageBrokerProxy()
	if proxy != nil {
		if fanout, ok := proxy.GetBus().(*eventbus.FanOutEventBus); ok {
			fanout.AddSpoke(spoke)
			log.Info("Added broker spoke to existing fan-out", "name", pluginName)
		}
	} else {
		inproc := eventbus.NewInProcessEventBus(logging.Subsystem("hub.eventbus.inprocess"))
		fanout := eventbus.NewFanOutEventBus([]eventbus.NamedEventBus{
			{Name: "inprocess", Bus: inproc},
			spoke,
		}, logging.Subsystem("hub.eventbus.fanout"))
		srv.StartMessageBroker(fanout)
		log.Info("Created new fan-out broker with spoke", "name", pluginName)

		if newProxy := srv.GetMessageBrokerProxy(); newProxy != nil {
			pluginMgr.SetBrokerHostCallbacks(newProxy)
		}
	}

	return nil
}

// injectHubCredentials creates the broker entity and HMAC secret, then calls
// ConfigureBroker to inject runtime credentials into the plugin.
func InjectHubCredentials(ctx context.Context, pluginMgr *scionplugin.Manager, srv *Server, pluginName string) error {
	if pluginMgr.IsSelfManaged(scionplugin.PluginTypeBroker, pluginName) {
		return nil
	}

	authSvc := srv.GetBrokerAuthService()
	if authSvc == nil {
		return fmt.Errorf("broker auth service not available")
	}

	s := srv.GetStore()
	if s == nil {
		return fmt.Errorf("store not available")
	}

	brokerID := "plugin-broker-" + pluginName

	if _, err := s.GetRuntimeBroker(ctx, brokerID); err != nil {
		pluginBroker := &store.RuntimeBroker{
			ID:              brokerID,
			Name:            "plugin-" + pluginName,
			Slug:            api.Slugify("plugin-" + pluginName),
			Version:         "0.1.0",
			Status:          store.BrokerStatusOnline,
			ConnectionState: "embedded",
			Labels:          map[string]string{"scion.io/plugin": pluginName},
			Created:         time.Now(),
			Updated:         time.Now(),
		}
		if createErr := s.CreateRuntimeBroker(ctx, pluginBroker); createErr != nil {
			return fmt.Errorf("failed to register broker entity: %w", createErr)
		}
	}

	secretKey, err := authSvc.GenerateAndStoreSecret(ctx, brokerID)
	if err != nil {
		return fmt.Errorf("failed to generate secret: %w", err)
	}

	hubCreds := map[string]string{
		"hub_url":   srv.config.HubEndpoint,
		"hmac_key":  secretKey,
		"broker_id": brokerID,
	}

	if projects, listErr := s.ListProjects(ctx, store.ProjectFilter{}, store.ListOptions{Limit: 500}); listErr == nil {
		slugMap := make(map[string]string, len(projects.Items))
		for _, p := range projects.Items {
			if p.Slug != "" {
				slugMap[p.ID] = p.Slug
			} else {
				slugMap[p.ID] = p.Name
			}
		}
		if jsonBytes, jsonErr := json.Marshal(slugMap); jsonErr == nil {
			hubCreds["project_slug_map"] = string(jsonBytes)
		}
	}

	if err := pluginMgr.ConfigureBroker(pluginName, hubCreds); err != nil {
		return fmt.Errorf("failed to configure broker with hub credentials: %w", err)
	}

	slog.Info("Injected hub credentials into broker plugin", "name", pluginName, "broker_id", brokerID)
	return nil
}

// checkObserver determines whether a broker plugin should be treated as observer.
func CheckObserver(pluginMgr *scionplugin.Manager, name string) bool {
	raw, err := pluginMgr.Get(scionplugin.PluginTypeBroker, name)
	if err == nil {
		if rpc, ok := raw.(*scionplugin.BrokerRPCClient); ok {
			if info, infoErr := rpc.GetInfo(); infoErr == nil && info != nil {
				for _, cap := range info.Capabilities {
					if strings.EqualFold(cap, "observer") {
						return true
					}
				}
				return false
			} else if infoErr != nil {
				slog.Warn("Failed to get plugin info for observer check", "name", name, "error", infoErr)
			}
		}
	}
	return false
}

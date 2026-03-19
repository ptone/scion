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

// Package refharness implements a reference harness plugin for testing and
// development. It provides a minimal, configurable harness that writes files
// during Provision() (as plugin harnesses must, since GetHarnessEmbedsFS()
// returns nil across the process boundary) and supports the optional
// AuthSettingsApplier and TelemetrySettingsApplier interfaces.
package refharness

import (
	"context"
	"embed"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/GoogleCloudPlatform/scion/pkg/api"
	"github.com/GoogleCloudPlatform/scion/pkg/plugin"
)

// RefHarness implements api.Harness, api.AuthSettingsApplier, and
// api.TelemetrySettingsApplier as a reference plugin harness for testing.
type RefHarness struct {
	log *slog.Logger

	// Provisioned tracks whether Provision was called (for testing).
	Provisioned bool

	// InjectedInstructions stores the last content passed to InjectAgentInstructions.
	InjectedInstructions []byte

	// InjectedSystemPrompt stores the last content passed to InjectSystemPrompt.
	InjectedSystemPrompt []byte

	// AppliedAuthHome stores the agentHome from the last ApplyAuthSettings call.
	AppliedAuthHome string

	// AppliedTelemetryHome stores the agentHome from the last ApplyTelemetrySettings call.
	AppliedTelemetryHome string
}

// New creates a new RefHarness with the given logger.
func New(log *slog.Logger) *RefHarness {
	if log == nil {
		log = slog.Default()
	}
	return &RefHarness{log: log}
}

func (h *RefHarness) Name() string { return "refharness" }

func (h *RefHarness) AdvancedCapabilities() api.HarnessAdvancedCapabilities {
	return api.HarnessAdvancedCapabilities{
		Harness: "refharness",
		Limits: api.HarnessLimitCapabilities{
			MaxTurns:      api.CapabilityField{Support: api.SupportNo, Reason: "Reference plugin"},
			MaxModelCalls: api.CapabilityField{Support: api.SupportNo, Reason: "Reference plugin"},
			MaxDuration:   api.CapabilityField{Support: api.SupportYes},
		},
		Prompts: api.HarnessPromptCapabilities{
			SystemPrompt:      api.CapabilityField{Support: api.SupportYes},
			AgentInstructions: api.CapabilityField{Support: api.SupportYes},
		},
		Auth: api.HarnessAuthCapabilities{
			APIKey: api.CapabilityField{Support: api.SupportYes},
		},
	}
}

func (h *RefHarness) GetEnv(agentName, agentHome, unixUsername string) map[string]string {
	return map[string]string{
		"SCION_AGENT_NAME": agentName,
		"REFHARNESS_HOME":  agentHome,
	}
}

func (h *RefHarness) GetCommand(task string, resume bool, baseArgs []string) []string {
	cmd := []string{"refharness-agent"}
	if task != "" {
		cmd = append(cmd, "--task", task)
	}
	if resume {
		cmd = append(cmd, "--resume")
	}
	return append(cmd, baseArgs...)
}

func (h *RefHarness) DefaultConfigDir() string { return ".refharness" }
func (h *RefHarness) SkillsDir() string        { return ".refharness/skills" }
func (h *RefHarness) GetEmbedDir() string      { return "refharness" }
func (h *RefHarness) GetInterruptKey() string  { return "C-c" }

func (h *RefHarness) GetTelemetryEnv() map[string]string {
	return map[string]string{"OTEL_EXPORTER_OTLP_ENDPOINT": "http://localhost:4317"}
}

func (h *RefHarness) HasSystemPrompt(agentHome string) bool {
	target := filepath.Join(agentHome, ".refharness", "system_prompt.md")
	_, err := os.Stat(target)
	return err == nil
}

// GetHarnessEmbedsFS returns an empty FS. Plugin harnesses write their own
// files directly during Provision() since embed.FS cannot cross the process
// boundary.
func (h *RefHarness) GetHarnessEmbedsFS() (embed.FS, string) {
	return embed.FS{}, ""
}

// Provision creates the harness config directory and writes a default
// configuration file, demonstrating the plugin pattern of writing files
// directly rather than relying on GetHarnessEmbedsFS().
func (h *RefHarness) Provision(ctx context.Context, agentName, agentDir, agentHome, agentWorkspace string) error {
	configDir := filepath.Join(agentHome, ".refharness")
	if err := os.MkdirAll(configDir, 0755); err != nil {
		return fmt.Errorf("refharness: failed to create config dir: %w", err)
	}

	configContent := fmt.Sprintf("# RefHarness config for %s\nagent_name: %s\nworkspace: %s\n",
		agentName, agentName, agentWorkspace)
	configPath := filepath.Join(configDir, "config.yaml")
	if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
		return fmt.Errorf("refharness: failed to write config: %w", err)
	}

	h.Provisioned = true
	h.log.Info("Reference harness provisioned", "agent", agentName, "home", agentHome)
	return nil
}

func (h *RefHarness) InjectAgentInstructions(agentHome string, content []byte) error {
	target := filepath.Join(agentHome, ".refharness", "instructions.md")
	if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
		return err
	}
	h.InjectedInstructions = content
	return os.WriteFile(target, content, 0644)
}

func (h *RefHarness) InjectSystemPrompt(agentHome string, content []byte) error {
	target := filepath.Join(agentHome, ".refharness", "system_prompt.md")
	if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
		return err
	}
	h.InjectedSystemPrompt = content
	return os.WriteFile(target, content, 0644)
}

func (h *RefHarness) ResolveAuth(auth api.AuthConfig) (*api.ResolvedAuth, error) {
	result := &api.ResolvedAuth{
		Method:  "passthrough",
		EnvVars: make(map[string]string),
	}
	if auth.AnthropicAPIKey != "" {
		result.EnvVars["ANTHROPIC_API_KEY"] = auth.AnthropicAPIKey
	}
	if auth.GeminiAPIKey != "" {
		result.EnvVars["GEMINI_API_KEY"] = auth.GeminiAPIKey
	}
	return result, nil
}

// ApplyAuthSettings implements api.AuthSettingsApplier.
func (h *RefHarness) ApplyAuthSettings(agentHome string, resolved *api.ResolvedAuth) error {
	h.AppliedAuthHome = agentHome
	h.log.Info("Applied auth settings", "home", agentHome, "method", resolved.Method)
	return nil
}

// ApplyTelemetrySettings implements api.TelemetrySettingsApplier.
func (h *RefHarness) ApplyTelemetrySettings(agentHome string, telemetry *api.TelemetryConfig, env map[string]string) error {
	h.AppliedTelemetryHome = agentHome
	h.log.Info("Applied telemetry settings", "home", agentHome)
	return nil
}

// GetInfo returns plugin metadata.
func (h *RefHarness) GetInfo() (*plugin.PluginInfo, error) {
	return &plugin.PluginInfo{
		Name:         "refharness",
		Version:      "0.1.0",
		Capabilities: []string{"auth_settings", "telemetry_settings"},
	}, nil
}

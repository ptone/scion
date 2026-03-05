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

package harness

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ptone/scion-agent/pkg/api"
	geminiEmbeds "github.com/ptone/scion-agent/pkg/harness/gemini"
	"github.com/ptone/scion-agent/pkg/util"
)

type GeminiCLI struct{}

func (g *GeminiCLI) Name() string {
	return "gemini"
}

func (g *GeminiCLI) GetEnv(agentName string, agentHome string, unixUsername string) map[string]string {
	env := make(map[string]string)

	if relPath := g.getSystemPromptRelPath(agentHome); relPath != "" {
		fullPath := fmt.Sprintf("%s/%s", util.GetHomeDir(unixUsername), relPath)
		env["GEMINI_SYSTEM_MD"] = fullPath
	}

	return env
}

func (g *GeminiCLI) GetCommand(task string, resume bool, baseArgs []string) []string {
	args := []string{"gemini", "--yolo"}
	if resume {
		args = append(args, "--resume")
	}
	args = append(args, baseArgs...)
	if task != "" {
		args = append(args, "--prompt-interactive", task)
	}
	return args
}

func (g *GeminiCLI) DefaultConfigDir() string {
	return ".gemini"
}

func (g *GeminiCLI) HasSystemPrompt(agentHome string) bool {
	return g.getSystemPromptRelPath(agentHome) != ""
}

func (g *GeminiCLI) getSystemPromptRelPath(agentHome string) string {
	if agentHome == "" {
		return ""
	}

	// 1. Check .gemini/system_prompt.md (New Standard)
	relPath := filepath.Join(g.DefaultConfigDir(), "system_prompt.md")
	fullPath := filepath.Join(agentHome, relPath)
	if g.isValidPromptFile(fullPath) {
		return relPath
	}

	// 2. Check system_prompt.md (Legacy / Root)
	relPath = "system_prompt.md"
	fullPath = filepath.Join(agentHome, relPath)
	if g.isValidPromptFile(fullPath) {
		return relPath
	}

	return ""
}

func (g *GeminiCLI) isValidPromptFile(path string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	content := strings.TrimSpace(string(data))
	if content == "" || content == "# Placeholder" {
		return false
	}
	return true
}

func (g *GeminiCLI) Provision(ctx context.Context, agentName, agentHome, agentWorkspace string) error {
	agentDir := filepath.Dir(agentHome)
	scionAgentPath := filepath.Join(agentDir, "scion-agent.json")

	data, err := os.ReadFile(scionAgentPath)
	if err != nil {
		return fmt.Errorf("failed to read scion-agent.json: %w", err)
	}
	var cfg api.ScionConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return fmt.Errorf("failed to parse scion-agent.json: %w", err)
	}

	selectedType := cfg.AuthSelectedType

	// Map universal auth types to Gemini-internal values for settings.json
	geminiAuthType := g.toGeminiAuthType(selectedType)

	if geminiAuthType != "" {
		// Update ~/.gemini/settings.json with Gemini-native auth type
		geminiSettingsPath := filepath.Join(agentHome, g.DefaultConfigDir(), "settings.json")
		if err := g.updateSelectedAuthType(geminiSettingsPath, geminiAuthType); err != nil {
			return fmt.Errorf("failed to update gemini settings: %w", err)
		}
	}

	// Update scion-agent.json
	var envUpdates map[string]string
	var volUpdates []api.VolumeMount

	home, _ := os.UserHomeDir()

	switch selectedType {
	case "api-key":
		envUpdates = map[string]string{"GEMINI_API_KEY": "${GEMINI_API_KEY}"}
	case "auth-file":
		envUpdates = map[string]string{"GOOGLE_CLOUD_PROJECT": "${GOOGLE_CLOUD_PROJECT}"}
	case "vertex-ai":
		envUpdates = map[string]string{
			"GOOGLE_CLOUD_PROJECT":  "${GOOGLE_CLOUD_PROJECT}",
			"GOOGLE_CLOUD_LOCATION": "${GOOGLE_CLOUD_LOCATION}",
		}
		volUpdates = append(volUpdates, api.VolumeMount{
			Source:   filepath.Join(home, ".config", "gcloud"),
			Target:   "/home/scion/.config/gcloud",
			ReadOnly: true,
		})
	}

	if len(envUpdates) > 0 {
		if cfg.Env == nil {
			cfg.Env = make(map[string]string)
		}
		for k, v := range envUpdates {
			if _, exists := cfg.Env[k]; !exists {
				cfg.Env[k] = v
			}
		}
	}

	if len(volUpdates) > 0 {
		cfg.Volumes = append(cfg.Volumes, volUpdates...)
	}

	newData, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal updated config: %w", err)
	}
	if err := os.WriteFile(scionAgentPath, newData, 0644); err != nil {
		return fmt.Errorf("failed to write updated scion-agent.json: %w", err)
	}

	return nil
}

func (g *GeminiCLI) updateSelectedAuthType(settingsPath string, selectedType string) error {
	var settings map[string]interface{}
	if data, err := os.ReadFile(settingsPath); err == nil {
		_ = json.Unmarshal(data, &settings)
	}
	if settings == nil {
		settings = make(map[string]interface{})
	}

	if _, ok := settings["security"]; !ok {
		settings["security"] = make(map[string]interface{})
	}
	sec := settings["security"].(map[string]interface{})

	if _, ok := sec["auth"]; !ok {
		sec["auth"] = make(map[string]interface{})
	}
	auth := sec["auth"].(map[string]interface{})

	if current, _ := auth["selectedType"].(string); current == selectedType {
		return nil
	}

	auth["selectedType"] = selectedType
	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return err
	}

	if err := os.MkdirAll(filepath.Dir(settingsPath), 0755); err != nil {
		return err
	}
	return os.WriteFile(settingsPath, data, 0644)
}

// toGeminiAuthType maps universal auth type values to Gemini CLI-internal values.
// Gemini CLI settings.json expects specific strings like "gemini-api-key",
// "oauth-personal", "vertex-ai", or "compute-default-credentials".
func (g *GeminiCLI) toGeminiAuthType(universal string) string {
	switch universal {
	case "api-key":
		return "gemini-api-key"
	case "auth-file":
		// Default to oauth-personal for auth-file; ADC resolution happens in ResolveAuth
		return "oauth-personal"
	case "vertex-ai":
		return "vertex-ai"
	default:
		return ""
	}
}

// ApplyAuthSettings updates Gemini's settings.json with the resolved auth type.
// This ensures the settings.json selectedType matches the resolved auth even
// when auth was auto-detected (i.e. no explicit AuthSelectedType was set).
func (g *GeminiCLI) ApplyAuthSettings(agentHome string, resolved *api.ResolvedAuth) error {
	geminiAuthType := resolved.EnvVars["GEMINI_DEFAULT_AUTH_TYPE"]
	if geminiAuthType == "" {
		return nil
	}
	settingsPath := filepath.Join(agentHome, g.DefaultConfigDir(), "settings.json")
	return g.updateSelectedAuthType(settingsPath, geminiAuthType)
}

func (g *GeminiCLI) GetEmbedDir() string {
	return "gemini"
}

func (g *GeminiCLI) GetInterruptKey() string {
	return "C-c"
}

func (g *GeminiCLI) GetHarnessEmbedsFS() (embed.FS, string) {
	return geminiEmbeds.EmbedsFS, "embeds"
}

func (g *GeminiCLI) InjectAgentInstructions(agentHome string, content []byte) error {
	dir := filepath.Join(agentHome, ".gemini")
	target := filepath.Join(dir, "GEMINI.md")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("failed to create directory for agent instructions: %w", err)
	}
	// Remove any existing instruction file with non-canonical casing (e.g.,
	// "gemini.md" copied from a harness-config home directory). On case-
	// sensitive filesystems these would coexist with "GEMINI.md" and cause
	// confusion; on case-insensitive filesystems we still want the directory
	// entry to use the canonical uppercase name.
	entries, err := os.ReadDir(dir)
	if err == nil {
		for _, e := range entries {
			if !e.IsDir() && strings.EqualFold(e.Name(), "GEMINI.md") && e.Name() != "GEMINI.md" {
				_ = os.Remove(filepath.Join(dir, e.Name()))
			}
		}
	}
	return os.WriteFile(target, content, 0644)
}

func (g *GeminiCLI) GetTelemetryEnv() map[string]string {
	return map[string]string{
		"GEMINI_TELEMETRY_ENABLED":       "true",
		"GEMINI_TELEMETRY_TARGET":        "local",
		"GEMINI_TELEMETRY_USE_COLLECTOR": "true",
		"GEMINI_TELEMETRY_OTLP_ENDPOINT": "http://localhost:4317",
		"GEMINI_TELEMETRY_OTLP_PROTOCOL": "grpc",
		"GEMINI_TELEMETRY_LOG_PROMPTS":   "false",
	}
}

func (g *GeminiCLI) ResolveAuth(auth api.AuthConfig) (*api.ResolvedAuth, error) {
	if auth.SelectedType != "" {
		return g.resolveExplicit(auth)
	}
	return g.resolveAutoDetect(auth)
}

func (g *GeminiCLI) resolveExplicit(auth api.AuthConfig) (*api.ResolvedAuth, error) {
	switch auth.SelectedType {
	case "api-key":
		apiKey := auth.GeminiAPIKey
		if apiKey == "" {
			apiKey = auth.GoogleAPIKey
		}
		if apiKey == "" {
			return nil, fmt.Errorf("gemini: auth type %q selected but no API key found; set GEMINI_API_KEY or GOOGLE_API_KEY", auth.SelectedType)
		}
		envVars := map[string]string{
			"GEMINI_DEFAULT_AUTH_TYPE": "gemini-api-key",
			"GEMINI_API_KEY":          apiKey,
		}
		if apiKey == auth.GoogleAPIKey {
			envVars["GOOGLE_API_KEY"] = apiKey
			delete(envVars, "GEMINI_API_KEY")
		}
		return &api.ResolvedAuth{
			Method:  "api-key",
			EnvVars: envVars,
		}, nil

	case "auth-file":
		if auth.OAuthCreds == "" {
			return nil, fmt.Errorf("gemini: auth type %q selected but OAuth credentials file not found at ~/.gemini/oauth_creds.json", auth.SelectedType)
		}
		result := &api.ResolvedAuth{
			Method: "auth-file",
			EnvVars: map[string]string{
				"GEMINI_DEFAULT_AUTH_TYPE": "oauth-personal",
			},
			Files: []api.FileMapping{
				{
					SourcePath:    auth.OAuthCreds,
					ContainerPath: "~/.gemini/oauth_creds.json",
				},
			},
		}
		if auth.GoogleCloudProject != "" {
			result.EnvVars["GOOGLE_CLOUD_PROJECT"] = auth.GoogleCloudProject
		}
		return result, nil

	case "vertex-ai":
		if auth.GoogleCloudProject == "" {
			return nil, fmt.Errorf("gemini: auth type %q selected but GOOGLE_CLOUD_PROJECT is not set", auth.SelectedType)
		}
		result := &api.ResolvedAuth{
			Method: "vertex-ai",
			EnvVars: map[string]string{
				"GEMINI_DEFAULT_AUTH_TYPE": "vertex-ai",
				"GOOGLE_CLOUD_PROJECT":    auth.GoogleCloudProject,
			},
		}
		if auth.GoogleCloudRegion != "" {
			result.EnvVars["GOOGLE_CLOUD_REGION"] = auth.GoogleCloudRegion
		}
		if auth.GoogleAppCredentials != "" {
			adcContainerPath := "~/.config/gcloud/application_default_credentials.json"
			if auth.GoogleAppCredentialsExplicit {
				result.EnvVars["GOOGLE_APPLICATION_CREDENTIALS"] = adcContainerPath
			}
			result.Files = append(result.Files, api.FileMapping{
				SourcePath:    auth.GoogleAppCredentials,
				ContainerPath: adcContainerPath,
			})
		}
		return result, nil

	default:
		return nil, fmt.Errorf("gemini: unknown auth type %q; valid types are: api-key, auth-file, vertex-ai", auth.SelectedType)
	}
}

func (g *GeminiCLI) resolveAutoDetect(auth api.AuthConfig) (*api.ResolvedAuth, error) {
	// Auto-detect priority: API key → OAuth → ADC (vertex-ai) → error

	// 1. API key
	if auth.GeminiAPIKey != "" || auth.GoogleAPIKey != "" {
		envVars := map[string]string{
			"GEMINI_DEFAULT_AUTH_TYPE": "gemini-api-key",
		}
		if auth.GeminiAPIKey != "" {
			envVars["GEMINI_API_KEY"] = auth.GeminiAPIKey
		}
		if auth.GoogleAPIKey != "" {
			envVars["GOOGLE_API_KEY"] = auth.GoogleAPIKey
		}
		return &api.ResolvedAuth{
			Method:  "api-key",
			EnvVars: envVars,
		}, nil
	}

	// 2. OAuth (~/.gemini/oauth_creds.json)
	if auth.OAuthCreds != "" {
		result := &api.ResolvedAuth{
			Method: "auth-file",
			EnvVars: map[string]string{
				"GEMINI_DEFAULT_AUTH_TYPE": "oauth-personal",
			},
			Files: []api.FileMapping{
				{
					SourcePath:    auth.OAuthCreds,
					ContainerPath: "~/.gemini/oauth_creds.json",
				},
			},
		}
		if auth.GoogleCloudProject != "" {
			result.EnvVars["GOOGLE_CLOUD_PROJECT"] = auth.GoogleCloudProject
		}
		return result, nil
	}

	// 3. ADC → vertex-ai (requires cloud project)
	if auth.GoogleAppCredentials != "" && auth.GoogleCloudProject != "" {
		adcContainerPath := "~/.config/gcloud/application_default_credentials.json"
		result := &api.ResolvedAuth{
			Method: "vertex-ai",
			EnvVars: map[string]string{
				"GEMINI_DEFAULT_AUTH_TYPE": "vertex-ai",
				"GOOGLE_CLOUD_PROJECT":    auth.GoogleCloudProject,
			},
			Files: []api.FileMapping{
				{
					SourcePath:    auth.GoogleAppCredentials,
					ContainerPath: adcContainerPath,
				},
			},
		}
		if auth.GoogleAppCredentialsExplicit {
			result.EnvVars["GOOGLE_APPLICATION_CREDENTIALS"] = adcContainerPath
		}
		if auth.GoogleCloudRegion != "" {
			result.EnvVars["GOOGLE_CLOUD_REGION"] = auth.GoogleCloudRegion
		}
		return result, nil
	}

	return nil, fmt.Errorf("gemini: no valid auth method found; set GEMINI_API_KEY or GOOGLE_API_KEY for API key auth, set up OAuth credentials at ~/.gemini/oauth_creds.json, or provide ADC with GOOGLE_CLOUD_PROJECT for vertex-ai")
}

func (g *GeminiCLI) InjectSystemPrompt(agentHome string, content []byte) error {
	dir := filepath.Join(agentHome, ".gemini")
	target := filepath.Join(dir, "system_prompt.md")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("failed to create directory for system prompt: %w", err)
	}
	// Remove any existing system prompt file with non-canonical casing.
	entries, err := os.ReadDir(dir)
	if err == nil {
		for _, e := range entries {
			if !e.IsDir() && strings.EqualFold(e.Name(), "system_prompt.md") && e.Name() != "system_prompt.md" {
				_ = os.Remove(filepath.Join(dir, e.Name()))
			}
		}
	}
	return os.WriteFile(target, content, 0644)
}

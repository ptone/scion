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

package api

import (
	"context"
	"embed"
)

// Harness interface defines the methods a harness must implement
type Harness interface {
	Name() string
	GetEnv(agentName string, agentHome string, unixUsername string) map[string]string
	GetCommand(task string, resume bool, baseArgs []string) []string
	DefaultConfigDir() string
	HasSystemPrompt(agentHome string) bool

	// Provision performs harness-specific setup during agent creation.
	// This is called after templates are copied and scion-agent.json is written.
	Provision(ctx context.Context, agentName, agentHome, agentWorkspace string) error

	// GetEmbedDir returns the name of the directory in pkg/config/embeds/
	// that contains template files for this harness (e.g., "claude", "gemini").
	GetEmbedDir() string

	// GetInterruptKey returns the key sequence used to interrupt the harness process (e.g., "C-c" or "Escape").
	GetInterruptKey() string

	// GetHarnessEmbedsFS returns the embedded filesystem containing default harness-config files
	// and the base path within it (e.g., "embeds").
	GetHarnessEmbedsFS() (embed.FS, string)

	// InjectAgentInstructions places agent instructions content into the harness's
	// expected location within the agent home directory.
	InjectAgentInstructions(agentHome string, content []byte) error

	// InjectSystemPrompt delivers system prompt content. Harnesses with native system
	// prompt support write to their expected location. Harnesses without it merge the
	// content into agent instructions (downgrade).
	InjectSystemPrompt(agentHome string, content []byte) error

	// GetTelemetryEnv returns harness-specific environment variables that direct
	// the harness's native telemetry output to the local OTLP collector
	// (localhost:4317). These are injected only when telemetry is enabled.
	GetTelemetryEnv() map[string]string

	// ResolveAuth selects the single best authentication method from a populated
	// AuthConfig and returns the env vars and file mappings needed to inject
	// credentials into the agent container. Returns an error with an actionable
	// message if no valid auth method is available.
	ResolveAuth(auth AuthConfig) (*ResolvedAuth, error)
}

// AuthSettingsApplier is an optional interface that harnesses can implement
// to update their native settings files after auth resolution. This is called
// at agent start time, after ResolveAuth, to ensure harness-specific config
// (e.g. Gemini's settings.json selectedType) reflects the resolved auth method.
type AuthSettingsApplier interface {
	ApplyAuthSettings(agentHome string, resolved *ResolvedAuth) error
}

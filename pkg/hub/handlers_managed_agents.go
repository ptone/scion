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
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"

	"github.com/GoogleCloudPlatform/scion/pkg/config"
	"github.com/GoogleCloudPlatform/scion/pkg/managedagent"
	"github.com/GoogleCloudPlatform/scion/pkg/managedagent/google"
	"github.com/GoogleCloudPlatform/scion/pkg/store"
	"github.com/GoogleCloudPlatform/scion/pkg/version"
)

const (
	ManagedAgentsProfile = "managed-agents"
	ManagedRuntimePrefix = "managed:"

	annotationCloudProvider = "scion.dev/cloud-provider"
	annotationInteractionID = "scion.dev/interaction-id"
	annotationEnvironmentID = "scion.dev/environment-id"
)

var (
	managedBackendMu   sync.Mutex
	managedBackendInst managedagent.ManagedAgentBackend
)

// getManagedBackend returns the lazily-initialized managed agent backend.
// It reads settings from the global settings file on first call.
// Transient init failures are not cached so subsequent calls can retry.
func getManagedBackend() (managedagent.ManagedAgentBackend, error) {
	managedBackendMu.Lock()
	defer managedBackendMu.Unlock()

	if managedBackendInst != nil {
		return managedBackendInst, nil
	}

	globalDir, err := config.GetGlobalDir()
	if err != nil {
		return nil, fmt.Errorf("resolving global dir: %w", err)
	}

	vs, err := config.LoadSingleFileVersioned(globalDir)
	if err != nil {
		return nil, fmt.Errorf("loading settings: %w", err)
	}

	if vs.ManagedAgents == nil || vs.ManagedAgents.Google == nil {
		return nil, fmt.Errorf("managed_agents.google configuration not found in settings")
	}

	cfg := vs.ManagedAgents.Google
	backend, err := google.NewBackend(google.BackendConfig{
		APIKey:    cfg.APIKey,
		BaseAgent: cfg.BaseAgent,
		Model:     cfg.Model,
	})
	if err != nil {
		return nil, fmt.Errorf("creating managed agent backend: %w", err)
	}

	managedBackendInst = backend
	return managedBackendInst, nil
}

// isManagedAgentRuntime returns true if the runtime string indicates a managed agent.
func isManagedAgentRuntime(runtime string) bool {
	return strings.HasPrefix(runtime, ManagedRuntimePrefix)
}

// managedAgentCreate handles agent creation for the managed-agents profile.
// It creates the cloud agent via the backend and stores the cloud agent ID
// in the agent's annotations.
func (s *Server) managedAgentCreate(ctx context.Context, agent *store.Agent, task string) error {
	backend, err := getManagedBackend()
	if err != nil {
		return fmt.Errorf("managed agent backend: %w", err)
	}

	agent.Runtime = ManagedRuntimePrefix + backend.Name()

	if agent.Annotations == nil {
		agent.Annotations = make(map[string]string)
	}
	agent.Annotations[annotationCloudProvider] = backend.Name()

	// Skip the /v1beta/agents CRUD endpoint — it may not be generally
	// available. Go directly to creating an interaction via
	// /v1beta/interactions, which works with built-in agent names.
	if task != "" {
		var systemInstruction string
		if agent.AppliedConfig != nil && agent.AppliedConfig.InlineConfig != nil {
			systemInstruction = agent.AppliedConfig.InlineConfig.SystemPrompt
			if systemInstruction == "" {
				systemInstruction = agent.AppliedConfig.InlineConfig.AgentInstructions
			}
		}

		envCfg, envErr := s.buildManagedEnvironment(agent)
		if envErr != nil {
			slog.Warn("failed to build managed environment with bootstrap", "agent_id", agent.ID, "err", envErr)
		}

		// Prepend bootstrap directive if bootstrap is configured.
		if envCfg != nil && len(envCfg.Sources) > 0 {
			bootstrapDirective := "IMPORTANT: Before starting any work, run: bash /scion/bootstrap.sh\nThis sets up your connection to the Scion orchestration system.\n\n"
			systemInstruction = bootstrapDirective + systemInstruction
		}

		handle, err := backend.CreateInteraction(ctx, managedagent.InteractionRequest{
			Input:             task,
			SystemInstruction: systemInstruction,
			Environment:       envCfg,
			Background:        true,
		})
		if err != nil {
			return fmt.Errorf("creating initial interaction: %w", err)
		}
		agent.Annotations[annotationInteractionID] = handle.InteractionID
		if handle.EnvironmentID != "" {
			agent.Annotations[annotationEnvironmentID] = handle.EnvironmentID
		}
	}

	return nil
}

// buildManagedEnvironment constructs the environment config for a managed agent,
// injecting bootstrap script, env vars, and network allowlist when bootstrap is
// configured. Falls back to a basic "remote" environment if bootstrap settings
// are missing.
func (s *Server) buildManagedEnvironment(agent *store.Agent) (*managedagent.EnvironmentConfig, error) {
	gcsBucket := bootstrapBucketFromSettings(s.loadManagedSettings())
	if gcsBucket == "" {
		return &managedagent.EnvironmentConfig{Type: "remote"}, nil
	}

	token, err := s.GenerateAgentToken(agent.ID, agent.ProjectID, nil)
	if err != nil {
		return &managedagent.EnvironmentConfig{Type: "remote"}, fmt.Errorf("minting agent token: %w", err)
	}

	script := generateBootstrapScript(gcsBucket, version.Short())

	env := &managedagent.EnvironmentConfig{
		Type: "remote",
		Sources: []managedagent.SourceConfig{
			{
				Type:    "inline",
				Path:    "/scion/bootstrap.sh",
				Content: script,
			},
		},
		EnvVars: map[string]string{
			"SCION_TOKEN":      token,
			"SCION_AGENT_ID":   agent.ID,
			"SCION_AGENT_NAME": agent.Slug,
			"SCION_PROJECT_ID": agent.ProjectID,
		},
		Network: &managedagent.NetworkConfig{
			Allowlist: []managedagent.AllowlistEntry{
				{Domain: "storage.googleapis.com"},
			},
		},
	}

	if s.config.HubEndpoint != "" {
		env.EnvVars["SCION_HUB_ENDPOINT"] = s.config.HubEndpoint
		if domain := extractDomain(s.config.HubEndpoint); domain != "" {
			env.Network.Allowlist = append(env.Network.Allowlist, managedagent.AllowlistEntry{
				Domain: domain,
			})
		}
	}

	return env, nil
}

// loadManagedSettings loads the global VersionedSettings, returning nil on error.
func (s *Server) loadManagedSettings() *config.VersionedSettings {
	globalDir, err := config.GetGlobalDir()
	if err != nil {
		return nil
	}
	vs, err := config.LoadSingleFileVersioned(globalDir)
	if err != nil {
		return nil
	}
	return vs
}

// bootstrapBucketFromSettings extracts the GCS bootstrap bucket from settings.
func bootstrapBucketFromSettings(vs *config.VersionedSettings) string {
	if vs == nil || vs.ManagedAgents == nil || vs.ManagedAgents.Bootstrap == nil {
		return ""
	}
	return vs.ManagedAgents.Bootstrap.GCSBucket
}

// extractDomain parses a URL and returns its hostname.
func extractDomain(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	return u.Hostname()
}

// managedAgentMessage sends a message to a managed agent by creating a new interaction.
func (s *Server) managedAgentMessage(ctx context.Context, agent *store.Agent, message string, interrupt bool) error {
	backend, err := getManagedBackend()
	if err != nil {
		return fmt.Errorf("managed agent backend: %w", err)
	}

	if interrupt {
		if interactionID := agent.Annotations[annotationInteractionID]; interactionID != "" {
			if cancelErr := backend.CancelInteraction(ctx, interactionID); cancelErr != nil {
				slog.Warn("failed to cancel interaction for interrupt", "agent_id", agent.ID, "err", cancelErr)
			}
		}
	}

	if message == "" {
		return nil
	}

	req := managedagent.InteractionRequest{
		Input:                 message,
		PreviousInteractionID: agent.Annotations[annotationInteractionID],
		EnvironmentID:         agent.Annotations[annotationEnvironmentID],
		Background:            true,
	}

	handle, err := backend.CreateInteraction(ctx, req)
	if err != nil {
		return fmt.Errorf("creating interaction: %w", err)
	}

	if agent.Annotations == nil {
		agent.Annotations = make(map[string]string)
	}
	agent.Annotations[annotationInteractionID] = handle.InteractionID
	if handle.EnvironmentID != "" {
		agent.Annotations[annotationEnvironmentID] = handle.EnvironmentID
	}

	if err := s.store.UpdateAgent(ctx, agent); err != nil {
		slog.Warn("failed to persist interaction annotations", "agent_id", agent.ID, "err", err)
	}

	return nil
}

// managedAgentStop stops a managed agent by cancelling the active interaction.
func (s *Server) managedAgentStop(ctx context.Context, agent *store.Agent) error {
	backend, err := getManagedBackend()
	if err != nil {
		return fmt.Errorf("managed agent backend: %w", err)
	}

	if interactionID := agent.Annotations[annotationInteractionID]; interactionID != "" {
		interactionState, getErr := backend.GetInteraction(ctx, interactionID)
		if getErr == nil && interactionState.Status == managedagent.StatusInProgress {
			if cancelErr := backend.CancelInteraction(ctx, interactionID); cancelErr != nil {
				slog.Warn("failed to cancel interaction on stop", "agent_id", agent.ID, "err", cancelErr)
			}
		}
	}

	return nil
}

// managedAgentDelete deletes a managed agent's cloud resources.
func (s *Server) managedAgentDelete(ctx context.Context, agent *store.Agent) error {
	// Stop first (best-effort) — cancels the active interaction.
	_ = s.managedAgentStop(ctx, agent)
	return nil
}

// handleManagedAgentLifecycle handles lifecycle actions (start, stop, restart) for managed agents.
func (s *Server) handleManagedAgentLifecycle(w http.ResponseWriter, r *http.Request, agent *store.Agent, action string) {
	ctx := r.Context()

	var newPhase string
	var actionErr error

	switch action {
	case "start":
		newPhase = string("running")
	case "stop":
		newPhase = string("stopped")
		actionErr = s.managedAgentStop(ctx, agent)
	case "restart":
		_ = s.managedAgentStop(ctx, agent)
		newPhase = string("running")
	case "suspend":
		writeError(w, http.StatusBadRequest, ErrCodeValidationError,
			"Suspend is not supported for managed agents — use stop instead.", nil)
		return
	default:
		writeError(w, http.StatusBadRequest, ErrCodeValidationError,
			fmt.Sprintf("Unknown lifecycle action %q for managed agent", action), nil)
		return
	}

	if actionErr != nil {
		RuntimeError(w, "Failed managed agent lifecycle action: "+actionErr.Error())
		return
	}

	statusUpdate := store.AgentStatusUpdate{
		Phase: newPhase,
	}
	if action == "stop" {
		statusUpdate.Activity = ""
	}
	if err := s.store.UpdateAgentStatus(ctx, agent.ID, statusUpdate); err != nil {
		writeErrorFromErr(w, err, "")
		return
	}

	agent.Phase = newPhase
	s.events.PublishAgentStatus(ctx, agent)
	writeJSON(w, http.StatusOK, agent)
}

// formatManagedAgentLook returns the latest interaction formatted as structured text.
func formatManagedAgentLook(ctx context.Context, agent *store.Agent) (string, error) {
	state, err := managedAgentGetInteraction(ctx, agent)
	if err != nil {
		return fmt.Sprintf("[status] %s (no active interaction)\n", agent.Phase), nil
	}

	// TODO: add per-step timestamps ([HH:MM:SS] prefix) once the backend
	// API exposes them on managedagent.Step — see design doc section 4.5.
	var b strings.Builder
	for _, step := range state.Steps {
		stepType := step.Type
		if stepType == "" {
			stepType = "output"
		}
		text := step.Text
		if text == "" && step.Arguments != "" {
			text = step.ToolName + "(" + step.Arguments + ")"
		}
		fmt.Fprintf(&b, "[%s] %s\n", stepType, text)
	}

	statusLine := string(state.Status)
	if state.Usage != nil {
		statusLine += fmt.Sprintf(" (%d input / %d output tokens)",
			state.Usage.TotalInputTokens, state.Usage.TotalOutputTokens)
	}
	fmt.Fprintf(&b, "[status] %s\n", statusLine)

	return b.String(), nil
}

// managedAgentGetInteraction retrieves the latest interaction state for a managed agent.
func managedAgentGetInteraction(ctx context.Context, agent *store.Agent) (*managedagent.InteractionState, error) {
	backend, err := getManagedBackend()
	if err != nil {
		return nil, fmt.Errorf("managed agent backend: %w", err)
	}

	interactionID := agent.Annotations[annotationInteractionID]
	if interactionID == "" {
		return nil, fmt.Errorf("no active interaction for agent %s", agent.Slug)
	}

	return backend.GetInteraction(ctx, interactionID)
}

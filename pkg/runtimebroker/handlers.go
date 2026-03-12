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

package runtimebroker

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/GoogleCloudPlatform/scion/pkg/agent"
	"github.com/GoogleCloudPlatform/scion/pkg/agent/state"
	"github.com/GoogleCloudPlatform/scion/pkg/api"
	"github.com/GoogleCloudPlatform/scion/pkg/config"
	"github.com/GoogleCloudPlatform/scion/pkg/gcp"
	"github.com/GoogleCloudPlatform/scion/pkg/harness"
	"github.com/GoogleCloudPlatform/scion/pkg/messages"
	"github.com/GoogleCloudPlatform/scion/pkg/templatecache"
)

// ============================================================================
// Health Endpoints
// ============================================================================

// GetHealthInfo returns the current health status of the Runtime Broker server.
// This can be called directly by co-located components (e.g., the WebServer)
// to build composite health responses without making an HTTP round-trip.
func (s *Server) GetHealthInfo(ctx context.Context) *HealthResponse {
	checks := make(map[string]string)

	// Check runtime availability
	if s.runtime != nil {
		checks[s.runtime.Name()] = "available"
	} else {
		checks["runtime"] = "unavailable"
	}

	status := "healthy"
	for _, v := range checks {
		if v != "available" && v != "healthy" {
			status = "degraded"
			break
		}
	}

	return &HealthResponse{
		Status:  status,
		Version: s.version,
		Uptime:  time.Since(s.startTime).Round(time.Second).String(),
		Checks:  checks,
	}
}

func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		MethodNotAllowed(w)
		return
	}

	resp := s.GetHealthInfo(r.Context())
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleReadyz(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		MethodNotAllowed(w)
		return
	}

	// Check if we have a functional runtime
	if s.runtime == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"status": "not_ready",
			"reason": "no runtime available",
		})
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{
		"status": "ready",
	})
}

func (s *Server) handleInfo(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		MethodNotAllowed(w)
		return
	}

	runtimeType := "unknown"
	if s.runtime != nil {
		runtimeType = s.runtime.Name()
	}

	resp := BrokerInfoResponse{
		BrokerID: s.config.BrokerID,
		Name:     s.config.BrokerName,
		Version:  s.version,
		Capabilities: &BrokerCapabilities{
			WebPTY: false, // TODO: Implement WebSocket PTY
			Sync:   true,
			Attach: true,
			Exec:   true,
		},
		Profiles: []BrokerProfile{
			{Name: "default", Type: runtimeType, Available: true},
		},
	}

	writeJSON(w, http.StatusOK, resp)
}

// handleHubConnections returns live status of all hub connections.
func (s *Server) handleHubConnections(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		MethodNotAllowed(w)
		return
	}

	s.hubMu.RLock()
	defer s.hubMu.RUnlock()

	mode := "single-hub"
	if len(s.hubConnections) > 1 {
		mode = "multi-hub"
	}

	connections := make([]HubConnectionInfo, 0, len(s.hubConnections))
	for _, conn := range s.hubConnections {
		info := HubConnectionInfo{
			Name:              conn.Name,
			HubEndpoint:       conn.HubEndpoint,
			BrokerID:          conn.BrokerID,
			AuthMode:          string(conn.AuthMode),
			Status:            string(conn.GetStatus()),
			IsColocated:       conn.IsColocated,
			HasHeartbeat:      conn.Heartbeat != nil,
			HasControlChannel: conn.ControlChannel != nil,
		}
		connections = append(connections, info)
	}

	resp := HubConnectionStatusResponse{
		Connections: connections,
		Mode:        mode,
	}

	writeJSON(w, http.StatusOK, resp)
}

// ============================================================================
// Agent Endpoints
// ============================================================================

func (s *Server) handleAgents(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.listAgents(w, r)
	case http.MethodPost:
		s.createAgent(w, r)
	default:
		MethodNotAllowed(w)
	}
}

func (s *Server) listAgents(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	query := r.URL.Query()

	filter := map[string]string{
		"scion.agent": "true",
	}

	// Add optional filters
	if groveID := query.Get("groveId"); groveID != "" {
		filter["scion.grove"] = groveID
	}
	if status := query.Get("status"); status != "" {
		filter["status"] = status
	}

	agents, err := s.manager.List(ctx, filter)
	if err != nil {
		RuntimeError(w, "Failed to list agents: "+err.Error())
		return
	}

	// Convert to API response format
	responses := make([]AgentResponse, 0, len(agents))
	for _, agent := range agents {
		responses = append(responses, AgentInfoToResponse(agent))
	}

	// Apply pagination
	limit := 50
	if l := query.Get("limit"); l != "" {
		if parsed, err := strconv.Atoi(l); err == nil && parsed > 0 {
			limit = parsed
		}
	}

	totalCount := len(responses)
	if len(responses) > limit {
		responses = responses[:limit]
	}

	writeJSON(w, http.StatusOK, ListAgentsResponse{
		Agents:     responses,
		TotalCount: totalCount,
	})
}

func (s *Server) createAgent(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	var req CreateAgentRequest
	if err := readJSON(r, &req); err != nil {
		BadRequest(w, "Invalid request body: "+err.Error())
		return
	}

	// Validate required fields
	if req.Name == "" {
		ValidationError(w, "name is required", nil)
		return
	}

	agentKey := req.ID
	if agentKey == "" {
		agentKey = req.Name
	}

	var attempt *dispatchAttempt
	if req.RequestID != "" {
		s.dispatchAttemptsMu.Lock()
		newAttempt, existingAttempt := s.beginCreateAttempt(req.RequestID, agentKey)
		if existingAttempt != nil {
			switch existingAttempt.Status {
			case dispatchAttemptSucceeded:
				if existingAttempt.CreatedResponse != nil {
					status := existingAttempt.HTTPStatus
					if status == 0 {
						status = http.StatusCreated
					}
					respCopy := *existingAttempt.CreatedResponse
					s.dispatchAttemptsMu.Unlock()
					writeJSON(w, status, respCopy)
					return
				}
				if existingAttempt.EnvResponse != nil {
					respCopy := *existingAttempt.EnvResponse
					s.dispatchAttemptsMu.Unlock()
					writeJSON(w, http.StatusAccepted, respCopy)
					return
				}
			case dispatchAttemptInProgress:
				s.dispatchAttemptsMu.Unlock()
				writeError(w, http.StatusConflict, ErrCodeConflict, "create request already in progress", map[string]interface{}{
					"requestId": req.RequestID,
				})
				return
			case dispatchAttemptFailed:
				existingAttempt.Status = dispatchAttemptInProgress
				existingAttempt.Error = ""
				existingAttempt.UpdatedAt = time.Now()
				s.completeAttempt(existingAttempt, dispatchAttemptInProgress, 0, nil, nil, "")
				attempt = existingAttempt
			}
		} else {
			attempt = newAttempt
		}
		s.dispatchAttemptsMu.Unlock()
	}

	markAttemptFailed := func(httpStatus int, message string) {
		if attempt == nil {
			return
		}
		s.dispatchAttemptsMu.Lock()
		s.completeAttempt(attempt, dispatchAttemptFailed, httpStatus, nil, nil, message)
		s.dispatchAttemptsMu.Unlock()
	}

	// Debug log incoming request
	if s.config.Debug {
		s.agentLifecycleLog.Debug("Creating agent", "agent_id", req.ID, "name", req.Name, "slug", req.Slug, "groveID", req.GroveID)
		s.agentLifecycleLog.Debug("Hub credentials",
			"agent_id", req.ID,
			"hubEndpoint", req.HubEndpoint,
			"hasToken", req.AgentToken != "",
			"slug", req.Slug,
		)
		if req.Config != nil {
			s.agentLifecycleLog.Debug("Agent configuration",
				"agent_id", req.ID,
				"template", req.Config.Template,
				"image", req.Config.Image,
				"templateID", req.Config.TemplateID,
			)
		}
	}

	// For hub-native groves (GroveSlug set, no local provider path), resolve
	// the conventional grove path (~/.scion/groves/<slug>/) early so that
	// hub endpoint resolution and env-gather can load settings correctly.
	if req.GroveSlug != "" && req.GrovePath == "" {
		globalDir, err := config.GetGlobalDir()
		if err != nil {
			markAttemptFailed(http.StatusInternalServerError, "failed to resolve global dir")
			RuntimeError(w, "Failed to get global dir: "+err.Error())
			return
		}
		req.GrovePath = filepath.Join(globalDir, "groves", req.GroveSlug)
		// Ensure the .scion project structure exists within the grove path.
		// The Hub's initHubNativeGrove creates this on the Hub's filesystem,
		// but on a different broker the directory may not exist yet. Without it,
		// ResolveGrovePath won't find the .scion subdirectory and agents will
		// be created at the wrong level (groves/<slug>/agents instead of
		// groves/<slug>/.scion/agents).
		scionDir := filepath.Join(req.GrovePath, ".scion")
		if _, err := os.Stat(scionDir); os.IsNotExist(err) {
			if err := config.InitProject(scionDir, nil); err != nil {
				s.agentLifecycleLog.Warn("Failed to initialize .scion project for hub-native grove",
					"agent_id", req.ID, "slug", req.GroveSlug, "path", scionDir, "error", err)
			}
		}
		if s.config.Debug {
			s.agentLifecycleLog.Debug("Resolved hub-native grove path from slug",
				"agent_id", req.ID, "slug", req.GroveSlug,
				"path", req.GrovePath,
			)
		}
	}

	// Build merged environment:
	// 1. Start with resolvedEnv (from Hub, contains user/grove/broker vars and secrets)
	// 2. Override with config.Env (explicitly set in request)
	// 3. Add Hub authentication credentials if provided
	env := make(map[string]string)

	// First, apply resolved env from Hub (if present)
	if len(req.ResolvedEnv) > 0 {
		for k, v := range req.ResolvedEnv {
			env[k] = v
		}
	}

	// Then, apply config.Env (takes precedence over resolvedEnv)
	if req.Config != nil && len(req.Config.Env) > 0 {
		for _, e := range req.Config.Env {
			parts := strings.SplitN(e, "=", 2)
			if len(parts) == 2 {
				env[parts[0]] = parts[1]
			}
		}
	}

	// Add Hub authentication credentials for the agent.
	// Uses SCION_AUTH_TOKEN as the generic agent-to-hub auth token.
	// Priority: explicit agent token from dispatcher > broker's own auth token.
	if agentToken := req.AgentToken; agentToken != "" {
		env["SCION_AUTH_TOKEN"] = agentToken
		if s.config.Debug {
			s.agentLifecycleLog.Debug("SCION_AUTH_TOKEN set from agent token", "agent_id", req.ID, "length", len(agentToken))
		}
	} else if devToken := os.Getenv("SCION_AUTH_TOKEN"); devToken != "" {
		env["SCION_AUTH_TOKEN"] = devToken
		if s.config.Debug {
			s.agentLifecycleLog.Debug("SCION_AUTH_TOKEN set from broker env", "agent_id", req.ID, "length", len(devToken))
		}
	}
	runtimeName := ""
	if s.runtime != nil {
		runtimeName = s.runtime.Name()
	}
	hubEndpoint := resolveHubEndpointForCreate(
		req.HubEndpoint,
		s.config.HubEndpoint,
		req.ResolvedEnv,
		req.GrovePath,
		s.config.ContainerHubEndpoint,
		runtimeName,
	)
	if hubEndpoint != "" {
		env["SCION_HUB_ENDPOINT"] = hubEndpoint
		env["SCION_HUB_URL"] = hubEndpoint // legacy compat
		if s.config.Debug {
			s.agentLifecycleLog.Debug("SCION_HUB_ENDPOINT set", "agent_id", req.ID, "endpoint", hubEndpoint)
		}
	}
	if req.Slug != "" {
		env["SCION_AGENT_SLUG"] = req.Slug
		if s.config.Debug {
			s.agentLifecycleLog.Debug("SCION_AGENT_SLUG set", "agent_id", req.ID, "slug", req.Slug)
		}
	}
	if req.ID != "" {
		env["SCION_AGENT_ID"] = req.ID
		if s.config.Debug {
			s.agentLifecycleLog.Debug("SCION_AGENT_ID set", "agent_id", req.ID, "id", req.ID)
		}
	}
	if req.GroveID != "" {
		env["SCION_GROVE_ID"] = req.GroveID
		if s.config.Debug {
			s.agentLifecycleLog.Debug("SCION_GROVE_ID set", "agent_id", req.ID, "id", req.GroveID)
		}
	}

	if s.config.BrokerName != "" {
		env["SCION_BROKER_NAME"] = s.config.BrokerName
	}
	if s.config.BrokerID != "" {
		env["SCION_BROKER_ID"] = s.config.BrokerID
	}
	if req.CreatorName != "" {
		env["SCION_CREATOR"] = req.CreatorName
	}

	// Pass debug mode to the container so sciontool logs debug info
	if s.config.Debug {
		env["SCION_DEBUG"] = "1"
	}

	// Debug log final env count
	if s.config.Debug {
		s.agentLifecycleLog.Debug("Final environment count", "agent_id", req.ID, "count", len(env))
		for k, v := range env {
			s.agentLifecycleLog.Debug("  ENV", "agent_id", req.ID, "key", k, "value", redactEnvValueForLog(k, v))
		}
	}

	// Env-gather: if GatherEnv is true, evaluate env completeness
	if req.GatherEnv {
		required, secretInfo := s.extractRequiredEnvKeys(req)
		if s.config.Debug {
			s.envSecretLog.Debug("Env-gather: evaluating env completeness",
				"gatherEnv", req.GatherEnv,
				"grovePath", req.GrovePath,
				"requiredKeys", len(required),
				"required", required,
			)
		}
		if len(required) > 0 {
			// Build lookup set of keys satisfied by resolved secrets
			secretTargets := make(map[string]struct{})
			for _, s := range req.ResolvedSecrets {
				if s.Type == "environment" || s.Type == "" {
					target := s.Target
					if target == "" {
						target = s.Name
					}
					if target != "" {
						secretTargets[target] = struct{}{}
					}
				}
				if s.Type == "file" {
					secretTargets[s.Name] = struct{}{}
				}
			}

			if s.config.Debug {
				targetKeys := make([]string, 0, len(secretTargets))
				for k := range secretTargets {
					targetKeys = append(targetKeys, k)
				}
				s.envSecretLog.Debug("Env-gather: resolved secret targets available",
					"secretTargetKeys", targetKeys,
					"resolvedSecretsCount", len(req.ResolvedSecrets),
				)
			}

			var hubHas, needs []string
			for _, key := range required {
				val, hasVal := env[key]
				if hasVal && val != "" {
					// Determine source
					if _, fromHub := req.ResolvedEnv[key]; fromHub {
						hubHas = append(hubHas, key)
					} else {
						hubHas = append(hubHas, key)
					}
				} else if _, fromSecret := secretTargets[key]; fromSecret {
					// Key will be projected from a resolved secret at container start
					hubHas = append(hubHas, key)
				} else {
					needs = append(needs, key)
				}
			}

			if len(needs) > 0 {
				// Store pending state for finalize-env
				s.pendingEnvGatherMu.Lock()
				now := time.Now()
				s.cleanupExpiredPendingLocked(now)
				s.upsertPendingState(&pendingAgentState{
					AgentID:   agentKey,
					Request:   &req,
					MergedEnv: env,
					CreatedAt: now,
					UpdatedAt: now,
					State:     pendingStatePending,
					RequestID: req.RequestID,
				})
				s.pendingEnvGatherMu.Unlock()

				if s.config.Debug {
					s.envSecretLog.Debug("Env-gather: returning 202 with requirements",
						"required", required,
						"hubHas", hubHas,
						"needs", needs,
					)
				}

				// Build SecretInfo for needed keys only
				var respSecretInfo map[string]api.SecretKeyInfo
				for _, key := range needs {
					if info, ok := secretInfo[key]; ok {
						if respSecretInfo == nil {
							respSecretInfo = make(map[string]api.SecretKeyInfo)
						}
						respSecretInfo[key] = info
					}
				}

				resp := EnvRequirementsResponse{
					AgentID:    agentKey,
					Required:   required,
					HubHas:     hubHas,
					Needs:      needs,
					SecretInfo: respSecretInfo,
				}
				if attempt != nil {
					s.dispatchAttemptsMu.Lock()
					s.completeAttempt(attempt, dispatchAttemptSucceeded, http.StatusAccepted, nil, &resp, "")
					s.dispatchAttemptsMu.Unlock()
				}
				writeJSON(w, http.StatusAccepted, resp)
				return
			}

			if s.config.Debug {
				s.envSecretLog.Debug("Env-gather: all required keys satisfied, proceeding with start",
					"required", required,
					"hubHas", hubHas,
				)
			}
		}
	}

	opts := api.StartOptions{
		Name:       req.Name,
		BrokerMode: true,
		Detached:   boolPtr(!req.Attach),
		GrovePath:  req.GrovePath,
	}

	if req.Config != nil {
		opts.Template = req.Config.Template
		opts.Image = req.Config.Image
		opts.HarnessConfig = req.Config.HarnessConfig
		opts.HarnessAuth = req.Config.HarnessAuth
		opts.Task = req.Config.Task
		opts.Workspace = req.Config.Workspace
		opts.Profile = req.Config.Profile
		opts.Branch = req.Config.Branch
	}

	// Pass through inline ScionConfig for provisioning
	if req.InlineConfig != nil {
		opts.InlineConfig = req.InlineConfig
	}

	// Save template slug before hydration may replace opts.Template with a cache path
	templateSlug := ""
	if req.Config != nil {
		templateSlug = req.Config.Template
	}

	// Debug log grove path
	if s.config.Debug && req.GrovePath != "" {
		s.agentLifecycleLog.Debug("Using grove path from Hub", "agent_id", req.ID, "path", req.GrovePath)
	}

	// Reject global groves in multi-hub mode
	if s.isMultiHubMode() && s.isGlobalGrove(req.GroveID, req.GrovePath) {
		writeJSON(w, http.StatusConflict, map[string]interface{}{
			"error": map[string]string{
				"code":    "global_grove_disabled",
				"message": "Global grove is disabled when broker is connected to multiple hubs",
			},
		})
		return
	}

	// Hydrate template if Hub mode is enabled and template info is provided
	hydrator := s.resolveHydrator(r)
	if hydrator != nil && req.Config != nil {
		templatePath, err := s.hydrateTemplate(ctx, req.Config, hydrator)
		if err != nil {
			markAttemptFailed(http.StatusInternalServerError, "failed to hydrate template")
			// Check if it's a Hub connectivity error
			if templatecache.IsHubConnectivityError(err) {
				HubUnreachableError(w, err.Error())
				return
			}
			TemplateError(w, "Failed to hydrate template: "+err.Error())
			return
		}
		if templatePath != "" {
			opts.Template = templatePath
			if s.config.Debug {
				s.agentLifecycleLog.Debug("Using hydrated template", "agent_id", req.ID, "path", templatePath)
			}
		}
	}

	// Preserve human-friendly template slug for container labels
	if templateSlug != "" {
		opts.TemplateName = templateSlug
	}

	// Git clone mode: inject env vars and skip workspace mounting.
	if req.Config != nil && req.Config.GitClone != nil {
		gc := req.Config.GitClone
		env["SCION_GIT_CLONE_URL"] = gc.URL
		if gc.Branch != "" {
			env["SCION_GIT_BRANCH"] = gc.Branch
		}
		if gc.Depth > 0 {
			env["SCION_GIT_DEPTH"] = strconv.Itoa(gc.Depth)
		}
		// Pass user-specified agent branch name for the feature branch
		if req.Config.Branch != "" {
			env["SCION_AGENT_BRANCH"] = req.Config.Branch
		}
		opts.Workspace = ""
		opts.GrovePath = ""
		opts.GitClone = gc
		if s.config.Debug {
			s.agentLifecycleLog.Debug("Git clone mode enabled", "agent_id", req.ID,
				"cloneURL", gc.URL, "branch", gc.Branch, "depth", gc.Depth)
		}
	}

	// Always set env (may be empty, which is fine)
	opts.Env = env

	// Translate SCION_TELEMETRY_ENABLED from the merged env into the
	// TelemetryOverride field so that Start() uses it as a proper override
	// (enabling harness telemetry env injection and cloud config merging).
	if v, ok := env["SCION_TELEMETRY_ENABLED"]; ok {
		enabled := v == "true" || v == "1"
		opts.TelemetryOverride = &enabled
	}

	// Pass through resolved secrets from the Hub
	if len(req.ResolvedSecrets) > 0 {
		opts.ResolvedSecrets = req.ResolvedSecrets
		if s.config.Debug {
			s.envSecretLog.Debug("Received resolved secrets from Hub", "count", len(req.ResolvedSecrets))
		}
	}

	// If WorkspaceStoragePath is set, download workspace from GCS (non-git bootstrap)
	if req.WorkspaceStoragePath != "" {
		// For hub-native groves (GroveSlug set), use the conventional path
		// ~/.scion/groves/<slug>/ instead of the worktree-based path.
		var workspaceDir string
		if req.GroveSlug != "" {
			globalDir, err := config.GetGlobalDir()
			if err != nil {
				markAttemptFailed(http.StatusInternalServerError, "failed to resolve global dir")
				RuntimeError(w, "Failed to get global dir: "+err.Error())
				return
			}
			workspaceDir = filepath.Join(globalDir, "groves", req.GroveSlug)
		} else {
			workspaceDir = filepath.Join(s.config.WorktreeBase, req.Name, "workspace")
		}
		if err := os.MkdirAll(workspaceDir, 0755); err != nil {
			markAttemptFailed(http.StatusInternalServerError, "failed to create workspace directory")
			RuntimeError(w, "Failed to create workspace directory: "+err.Error())
			return
		}

		bucket := s.config.StorageBucket
		if bucket == "" {
			markAttemptFailed(http.StatusInternalServerError, "storage bucket not configured")
			RuntimeError(w, "Storage bucket not configured for workspace bootstrap")
			return
		}

		if s.config.Debug {
			s.agentLifecycleLog.Debug("Downloading workspace from GCS", "agent_id", req.ID,
				"bucket", bucket,
				"storagePath", req.WorkspaceStoragePath+"/files",
				"workspaceDir", workspaceDir,
				"groveSlug", req.GroveSlug,
			)
		}

		if err := gcp.SyncFromGCS(ctx, bucket, req.WorkspaceStoragePath+"/files", workspaceDir); err != nil {
			markAttemptFailed(http.StatusInternalServerError, "failed to download workspace from GCS")
			RuntimeError(w, "Failed to download workspace from GCS: "+err.Error())
			return
		}

		opts.Workspace = workspaceDir
		opts.GrovePath = "" // Prevent git worktree logic in ProvisionAgent

		// Write a .scion grove marker into the workspace so in-container CLI
		// can discover the grove context and use the Hub API.
		if req.GroveID != "" && req.GroveSlug != "" {
			if writeErr := config.WriteWorkspaceMarker(workspaceDir, req.GroveID, req.GroveSlug, req.GroveSlug); writeErr != nil {
				s.agentLifecycleLog.Warn("Failed to write workspace marker", "agent_id", req.ID, "error", writeErr)
			}
		}
	}

	mgr := s.resolveManagerForOpts(opts)

	// Branch based on provision-only flag
	if req.ProvisionOnly {
		// Provision only: set up dirs, worktree, templates without starting the container
		cfg, err := mgr.Provision(ctx, opts)
		if err != nil {
			markAttemptFailed(http.StatusInternalServerError, "failed to provision agent")
			RuntimeError(w, "Failed to provision agent: "+err.Error())
			return
		}

		// Build a response with "created" status (no container launched)
		agentResp := &AgentResponse{
			ID:     req.ID,
			Slug:   req.Slug,
			Name:   req.Name,
			Status: string(state.PhaseCreated),
			Phase:  string(state.PhaseCreated),
		}
		if cfg != nil {
			agentResp.HarnessConfig = cfg.HarnessConfig
			agentResp.Image = cfg.Image
		}
		if s.runtime != nil {
			agentResp.RuntimeType = s.runtime.Name()
		}

		resp := CreateAgentResponse{
			Agent:   agentResp,
			Created: true,
		}
		if attempt != nil {
			s.dispatchAttemptsMu.Lock()
			s.completeAttempt(attempt, dispatchAttemptSucceeded, http.StatusCreated, &resp, nil, "")
			s.dispatchAttemptsMu.Unlock()
		}
		writeJSON(w, http.StatusCreated, resp)
		return
	}

	// Full start: provision and launch the container
	agentInfo, err := mgr.Start(ctx, opts)
	if err != nil {
		markAttemptFailed(http.StatusInternalServerError, "failed to create agent")
		// Clean up provisioned agent files so they don't become orphans.
		// In hub mode the hub will delete its agent record on dispatch failure,
		// leaving no retry path — orphaned local files would trigger spurious
		// sync-registration attempts on the next CLI list.
		if opts.GrovePath != "" {
			if _, cleanupErr := agent.DeleteAgentFiles(opts.Name, opts.GrovePath, true); cleanupErr != nil {
				s.agentLifecycleLog.Warn("Failed to clean up agent files after start failure",
					"agent_id", req.ID, "agent", opts.Name, "error", cleanupErr)
			} else {
				s.agentLifecycleLog.Info("Cleaned up provisioned agent files after start failure",
					"agent_id", req.ID, "agent", opts.Name)
			}
		}
		RuntimeError(w, "Failed to create agent: "+err.Error())
		return
	}

	// Log auth resolution info visible in broker logs
	for _, w := range agentInfo.Warnings {
		if strings.HasPrefix(w, "Auth:") {
			s.agentLifecycleLog.Info("Agent auth resolution", "agent_id", req.ID, "agent", req.Name, "result", w)
		}
	}

	resp := CreateAgentResponse{
		Agent:   agentInfoPtr(AgentInfoToResponse(*agentInfo)),
		Created: true,
	}
	if attempt != nil {
		s.dispatchAttemptsMu.Lock()
		s.completeAttempt(attempt, dispatchAttemptSucceeded, http.StatusCreated, &resp, nil, "")
		s.dispatchAttemptsMu.Unlock()
	}

	writeJSON(w, http.StatusCreated, resp)
}

// hydrateTemplate fetches and caches a template from the Hub if template info is provided.
// Returns the local template path, or empty string if no Hub template was specified.
func (s *Server) hydrateTemplate(ctx context.Context, cfg *CreateAgentConfig, hydrator *templatecache.Hydrator) (string, error) {
	// Check if we have template info from Hub
	if cfg.TemplateID == "" && cfg.TemplateHash == "" {
		// No Hub template info provided, use local template handling
		return "", nil
	}

	// If we have a template hash, try to use it for cache lookup
	if cfg.TemplateHash != "" && cfg.TemplateID != "" {
		return hydrator.HydrateWithHash(ctx, cfg.TemplateID, cfg.TemplateHash)
	}

	// Just have template ID, do full hydration
	if cfg.TemplateID != "" {
		return hydrator.Hydrate(ctx, cfg.TemplateID)
	}

	return "", nil
}

func (s *Server) handleAgentByID(w http.ResponseWriter, r *http.Request) {
	id, action := extractAction(r, "/api/v1/agents")

	if id == "" {
		NotFound(w, "Agent")
		return
	}

	// Handle WebSocket attach for PTY
	if action == "attach" && isPTYWebSocketUpgrade(r) {
		s.handleAgentAttach(w, r)
		return
	}

	// Handle actions
	if action != "" {
		s.handleAgentAction(w, r, id, action)
		return
	}

	switch r.Method {
	case http.MethodGet:
		s.getAgent(w, r, id)
	case http.MethodDelete:
		s.deleteAgent(w, r, id)
	default:
		MethodNotAllowed(w)
	}
}

func (s *Server) getAgent(w http.ResponseWriter, r *http.Request, id string) {
	ctx := r.Context()

	// List agents and find the matching one
	agents, err := s.manager.List(ctx, map[string]string{"scion.agent": "true"})
	if err != nil {
		RuntimeError(w, "Failed to list agents: "+err.Error())
		return
	}

	for _, agent := range agents {
		if agent.Name == id || agent.ContainerID == id || agent.Slug == id {
			writeJSON(w, http.StatusOK, AgentInfoToResponse(agent))
			return
		}
	}

	NotFound(w, "Agent")
}

func (s *Server) deleteAgent(w http.ResponseWriter, r *http.Request, id string) {
	ctx := r.Context()
	query := r.URL.Query()

	deleteFiles := query.Get("deleteFiles") == "true"
	removeBranch := query.Get("removeBranch") == "true"
	softDelete := query.Get("softDelete") == "true"

	// Get the agent's grove path before stopping (needed for file deletion)
	var grovePath string
	agents, err := s.manager.List(ctx, map[string]string{"scion.agent": "true"})
	if err == nil {
		for _, a := range agents {
			if a.Name == id || a.ContainerID == id || a.Slug == id {
				grovePath = a.GrovePath
				break
			}
		}
	}

	// If no grove path was found (container missing or no annotation), check
	// hub-native grove directories for the agent's files. Without this,
	// agents in hub-native groves (~/.scion/groves/<slug>/) are silently
	// skipped during file cleanup because the default filesystem scan only
	// checks the CWD-resolved project dir and global ~/.scion.
	if grovePath == "" && deleteFiles {
		if resolved := findAgentInHubNativeGroves(id); resolved != "" {
			grovePath = resolved
			s.agentLifecycleLog.Debug("Resolved agent grove path from hub-native groves",
				"agent_id", id, "path", grovePath)
		}
	}

	// If this is a soft-delete, mark agent-info.json with deleted status before cleanup
	if softDelete && grovePath != "" {
		deletedAtStr := query.Get("deletedAt")
		if err := agent.UpdateAgentConfig(id, grovePath, "deleted", "", ""); err != nil {
			s.agentLifecycleLog.Warn("Failed to mark agent as deleted in agent-info.json", "agent_id", id, "error", err)
		}
		if deletedAtStr != "" {
			if deletedAt, err := time.Parse(time.RFC3339, deletedAtStr); err == nil {
				if err := agent.UpdateAgentDeletedAt(id, grovePath, deletedAt); err != nil {
					s.agentLifecycleLog.Warn("Failed to write deletedAt to agent-info.json", "agent_id", id, "error", err)
				}
			}
		}
	}

	_, err = s.manager.Delete(ctx, id, deleteFiles, grovePath, removeBranch)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			NotFound(w, "Agent")
			return
		}
		RuntimeError(w, "Failed to delete agent: "+err.Error())
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleAgentAction(w http.ResponseWriter, r *http.Request, id, action string) {
	if r.Method != http.MethodPost {
		MethodNotAllowed(w)
		return
	}

	switch action {
	case "start":
		s.startAgent(w, r, id)
	case "stop":
		s.stopAgent(w, r, id)
	case "restart":
		s.restartAgent(w, r, id)
	case "message":
		s.sendMessage(w, r, id)
	case "exec":
		s.execCommand(w, r, id)
	case "logs":
		s.getLogs(w, r, id)
	case "stats":
		s.getStats(w, r, id)
	case "has-prompt":
		s.checkAgentPrompt(w, r, id)
	case "finalize-env":
		s.finalizeEnv(w, r, id)
	default:
		NotFound(w, "Action")
	}
}

func (s *Server) startAgent(w http.ResponseWriter, r *http.Request, id string) {
	ctx := r.Context()

	// Read optional task, grovePath, groveSlug, harnessConfig, and resolvedEnv from request body
	var startReq struct {
		Task            string               `json:"task"`
		GrovePath       string               `json:"grovePath"`
		GroveSlug       string               `json:"groveSlug"`
		HarnessConfig   string               `json:"harnessConfig"`
		ResolvedEnv     map[string]string    `json:"resolvedEnv"`
		ResolvedSecrets []api.ResolvedSecret `json:"resolvedSecrets,omitempty"`
		InlineConfig    *api.ScionConfig     `json:"inlineConfig,omitempty"`
	}
	if r.Body != nil && r.ContentLength != 0 {
		if err := json.NewDecoder(r.Body).Decode(&startReq); err != nil {
			s.agentLifecycleLog.Debug("No task in start request body (ignoring decode error)", "agent_id", id, "error", err)
		}
	}

	s.agentLifecycleLog.Debug("startAgent called", "agent_id", id, "task", startReq.Task, "grovePath", startReq.GrovePath, "groveSlug", startReq.GroveSlug, "harnessConfig", startReq.HarnessConfig, "resolvedEnvCount", len(startReq.ResolvedEnv))

	// For hub-native groves (GroveSlug set, no local provider path), resolve
	// the conventional grove path (~/.scion/groves/<slug>/) so the agent is
	// started in the correct location instead of the broker's local grove.
	if startReq.GroveSlug != "" && startReq.GrovePath == "" {
		globalDir, err := config.GetGlobalDir()
		if err != nil {
			RuntimeError(w, "Failed to get global dir: "+err.Error())
			return
		}
		startReq.GrovePath = filepath.Join(globalDir, "groves", startReq.GroveSlug)
		// Ensure the .scion project structure exists within the grove path.
		// See corresponding comment in createAgent for full explanation.
		scionDir := filepath.Join(startReq.GrovePath, ".scion")
		if _, err := os.Stat(scionDir); os.IsNotExist(err) {
			if err := config.InitProject(scionDir, nil); err != nil {
				s.agentLifecycleLog.Warn("Failed to initialize .scion project for hub-native grove",
					"agent_id", id, "slug", startReq.GroveSlug, "path", scionDir, "error", err)
			}
		}
		if s.config.Debug {
			s.agentLifecycleLog.Debug("Resolved hub-native grove path from slug in startAgent",
				"agent_id", id, "slug", startReq.GroveSlug,
				"path", startReq.GrovePath,
			)
		}
	}

	// Build start options
	opts := api.StartOptions{
		Name:          id,
		BrokerMode:    true,
		Task:          startReq.Task,
		HarnessConfig: startReq.HarnessConfig,
	}

	// Apply resolved env vars from Hub (API keys, secrets, etc.)
	if len(startReq.ResolvedEnv) > 0 {
		opts.Env = make(map[string]string, len(startReq.ResolvedEnv))
		for k, v := range startReq.ResolvedEnv {
			opts.Env[k] = v
		}
		if s.config.Debug {
			s.agentLifecycleLog.Debug("startAgent: applied resolved env from hub", "agent_id", id, "count", len(startReq.ResolvedEnv))
		}
	}

	// Translate SCION_TELEMETRY_ENABLED from hub-resolved env into the
	// TelemetryOverride field so that Start() uses it as a proper override
	// (enabling harness telemetry env injection and cloud config merging).
	if v, ok := startReq.ResolvedEnv["SCION_TELEMETRY_ENABLED"]; ok {
		enabled := v == "true" || v == "1"
		opts.TelemetryOverride = &enabled
	}

	// Apply broker-level env enrichment (hub endpoint, broker name, debug)
	if opts.Env == nil {
		opts.Env = make(map[string]string)
	}
	grovePathForFallback := startReq.GrovePath
	if grovePathForFallback == "" {
		grovePathForFallback = opts.GrovePath
	}
	runtimeName := ""
	if s.runtime != nil {
		runtimeName = s.runtime.Name()
	}
	hubEndpoint := resolveHubEndpointForStart(
		s.config.HubEndpoint,
		startReq.ResolvedEnv,
		grovePathForFallback,
		s.config.ContainerHubEndpoint,
		runtimeName,
	)
	if hubEndpoint != "" {
		opts.Env["SCION_HUB_ENDPOINT"] = hubEndpoint
		opts.Env["SCION_HUB_URL"] = hubEndpoint
	}
	if s.config.BrokerName != "" {
		opts.Env["SCION_BROKER_NAME"] = s.config.BrokerName
	}
	if s.config.BrokerID != "" {
		opts.Env["SCION_BROKER_ID"] = s.config.BrokerID
	}
	if s.config.Debug {
		opts.Env["SCION_DEBUG"] = "1"
	}

	// Use grove path from request if provided
	if startReq.GrovePath != "" {
		opts.GrovePath = startReq.GrovePath
	} else {
		// Fall back to looking up grove path from an existing container
		agents, err := s.manager.List(ctx, map[string]string{"scion.agent": "true"})
		if err != nil {
			RuntimeError(w, "Failed to list agents: "+err.Error())
			return
		}

		for i := range agents {
			if agents[i].Name == id || agents[i].ContainerID == id || agents[i].Slug == id {
				if agents[i].GrovePath != "" {
					opts.GrovePath = agents[i].GrovePath
				}
				break
			}
		}
	}

	// Pass through resolved secrets from the Hub (file-type secrets for auth, etc.)
	if len(startReq.ResolvedSecrets) > 0 {
		opts.ResolvedSecrets = startReq.ResolvedSecrets
		if s.config.Debug {
			s.envSecretLog.Debug("Received resolved secrets from Hub in startAgent", "count", len(startReq.ResolvedSecrets))
		}
	}

	// Apply updated InlineConfig to scion-agent.json before starting.
	// This handles config changes made via the Hub PATCH endpoint after
	// initial provisioning (e.g. max_turns set in the web configure form).
	if startReq.InlineConfig != nil && opts.GrovePath != "" {
		s.applyInlineConfigUpdate(id, opts.GrovePath, startReq.InlineConfig)
	}

	// Resolve saved profile for runtime selection
	if opts.GrovePath != "" {
		opts.Profile = agent.GetSavedProfile(id, opts.GrovePath)
	}

	mgr := s.resolveManagerForOpts(opts)
	agentInfo, err := mgr.Start(ctx, opts)
	if err != nil {
		RuntimeError(w, "Failed to start agent: "+err.Error())
		return
	}

	// Send an immediate heartbeat so the hub gets the updated container status
	// without waiting for the next periodic heartbeat interval.
	s.hubMu.RLock()
	for _, conn := range s.hubConnections {
		if conn.Heartbeat != nil {
			hb := conn.Heartbeat
			go func() {
				if err := hb.ForceHeartbeat(context.Background()); err != nil {
					s.agentLifecycleLog.Error("Failed to send forced heartbeat after start", "agent_id", id, "error", err)
				}
			}()
		}
	}
	s.hubMu.RUnlock()

	agentResp := AgentInfoToResponse(*agentInfo)
	writeJSON(w, http.StatusAccepted, CreateAgentResponse{
		Agent:   &agentResp,
		Created: false,
	})
}

// applyInlineConfigUpdate merges the updated InlineConfig into the agent's
// scion-agent.json. This ensures config changes made via the Hub (e.g. limits
// set in the web configure form) are applied before the agent starts.
func (s *Server) applyInlineConfigUpdate(agentName, grovePath string, inlineConfig *api.ScionConfig) {
	projectDir, err := config.GetResolvedProjectDir(grovePath)
	if err != nil {
		s.agentLifecycleLog.Warn("applyInlineConfigUpdate: failed to resolve project dir", "agent", agentName, "error", err)
		return
	}
	agentDir := filepath.Join(projectDir, "agents", agentName)
	cfgPath := filepath.Join(agentDir, "scion-agent.json")

	// Load existing config
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		s.agentLifecycleLog.Warn("applyInlineConfigUpdate: failed to read scion-agent.json", "agent", agentName, "path", cfgPath, "error", err)
		return
	}
	var existing api.ScionConfig
	if err := json.Unmarshal(data, &existing); err != nil {
		s.agentLifecycleLog.Warn("applyInlineConfigUpdate: failed to parse scion-agent.json", "agent", agentName, "error", err)
		return
	}

	// Merge inline config over existing
	merged := config.MergeScionConfig(&existing, inlineConfig)

	// Write back
	updated, err := json.MarshalIndent(merged, "", "  ")
	if err != nil {
		s.agentLifecycleLog.Warn("applyInlineConfigUpdate: failed to marshal updated config", "agent", agentName, "error", err)
		return
	}
	if err := os.WriteFile(cfgPath, updated, 0644); err != nil {
		s.agentLifecycleLog.Warn("applyInlineConfigUpdate: failed to write scion-agent.json", "agent", agentName, "error", err)
		return
	}
	if s.config.Debug {
		s.agentLifecycleLog.Debug("applyInlineConfigUpdate: applied inline config update",
			"agent", agentName, "maxTurns", inlineConfig.MaxTurns, "maxModelCalls", inlineConfig.MaxModelCalls)
	}
}

// isContainerStopTolerable returns true if the error from stopping a container
// indicates the container is already stopped, exited, or doesn't exist. This
// covers both Docker and Podman error messages and exit codes.
func isContainerStopTolerable(err error) bool {
	msg := err.Error()
	return strings.Contains(msg, "not found") ||
		strings.Contains(msg, "no such") ||
		strings.Contains(msg, "No such") ||
		strings.Contains(msg, "not running") ||
		strings.Contains(msg, "is not running") ||
		strings.Contains(msg, "exit status 125")
}

func (s *Server) stopAgent(w http.ResponseWriter, r *http.Request, id string) {
	ctx := r.Context()

	if err := s.manager.Stop(ctx, id); err != nil {
		if isContainerStopTolerable(err) {
			// Container doesn't exist, is already stopped, or podman/docker can't find it.
			// Treat as success so the hub can update its state.
			s.agentLifecycleLog.Warn("Stop target not found or already stopped, treating as success", "agent_id", id, "error", err)
		} else {
			RuntimeError(w, "Failed to stop agent: "+err.Error())
			return
		}
	}

	// Send an immediate heartbeat so the hub gets the updated container status
	// without waiting for the next periodic heartbeat interval.
	// In multi-hub mode, force heartbeat on all connections.
	s.hubMu.RLock()
	for _, conn := range s.hubConnections {
		if conn.Heartbeat != nil {
			hb := conn.Heartbeat
			go func() {
				if err := hb.ForceHeartbeat(context.Background()); err != nil {
					s.agentLifecycleLog.Error("Failed to send forced heartbeat after stop", "agent_id", id, "error", err)
				}
			}()
		}
	}
	s.hubMu.RUnlock()

	writeJSON(w, http.StatusAccepted, map[string]string{
		"status":  "accepted",
		"message": "Stop operation accepted",
	})
}

func (s *Server) restartAgent(w http.ResponseWriter, r *http.Request, id string) {
	ctx := r.Context()

	opts := api.StartOptions{
		Name:       id,
		BrokerMode: true,
	}
	agents, err := s.manager.List(ctx, map[string]string{"scion.agent": "true"})
	if err == nil {
		for i := range agents {
			if agents[i].Name == id || agents[i].ContainerID == id || agents[i].Slug == id {
				opts.Name = agents[i].Name
				opts.GrovePath = agents[i].GrovePath
				break
			}
		}
	}

	if opts.GrovePath != "" {
		opts.Profile = agent.GetSavedProfile(id, opts.GrovePath)
	}

	// Enrich with broker-level env vars (hub endpoint, broker name, debug)
	if opts.Env == nil {
		opts.Env = make(map[string]string)
	}
	runtimeName := ""
	if s.runtime != nil {
		runtimeName = s.runtime.Name()
	}
	hubEndpoint := resolveHubEndpointForStart(
		s.config.HubEndpoint,
		nil,
		opts.GrovePath,
		s.config.ContainerHubEndpoint,
		runtimeName,
	)
	if hubEndpoint != "" {
		opts.Env["SCION_HUB_ENDPOINT"] = hubEndpoint
		opts.Env["SCION_HUB_URL"] = hubEndpoint
	}
	if s.config.BrokerName != "" {
		opts.Env["SCION_BROKER_NAME"] = s.config.BrokerName
	}
	if s.config.BrokerID != "" {
		opts.Env["SCION_BROKER_ID"] = s.config.BrokerID
	}
	if s.config.Debug {
		opts.Env["SCION_DEBUG"] = "1"
	}

	// Stop then start — tolerate stop errors since the container may already
	// be exited and the subsequent start will handle cleanup.
	if err := s.manager.Stop(ctx, id); err != nil {
		if isContainerStopTolerable(err) {
			s.agentLifecycleLog.Warn("Restart: stop target not found or already stopped, proceeding with start", "agent_id", id, "error", err)
		} else {
			// Log but proceed with start — the start will delete and
			// recreate the container regardless of its current state.
			s.agentLifecycleLog.Warn("Restart: stop failed, proceeding with start anyway", "agent_id", id, "error", err)
		}
	}

	mgr := s.resolveManagerForOpts(opts)
	agentInfo, err := mgr.Start(ctx, opts)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			NotFound(w, "Agent")
			return
		}
		RuntimeError(w, "Failed to restart agent: "+err.Error())
		return
	}

	// Send an immediate heartbeat so the hub gets the updated container status
	s.hubMu.RLock()
	for _, conn := range s.hubConnections {
		if conn.Heartbeat != nil {
			hb := conn.Heartbeat
			go func() {
				if err := hb.ForceHeartbeat(context.Background()); err != nil {
					s.agentLifecycleLog.Error("Failed to send forced heartbeat after restart", "agent_id", id, "error", err)
				}
			}()
		}
	}
	s.hubMu.RUnlock()

	agentResp := AgentInfoToResponse(*agentInfo)
	writeJSON(w, http.StatusAccepted, CreateAgentResponse{
		Agent:   &agentResp,
		Created: false,
	})
}

func (s *Server) sendMessage(w http.ResponseWriter, r *http.Request, id string) {
	ctx := r.Context()

	var req MessageRequest
	if err := readJSON(r, &req); err != nil {
		BadRequest(w, "Invalid request body: "+err.Error())
		return
	}

	// Determine the message to deliver.
	// Empty messages (no body) are sent as an empty string, which the agent
	// manager delivers as a plain tmux Enter keypress to trigger confirmations.
	var deliveryText string
	if req.StructuredMessage != nil {
		deliveryText = messages.FormatForDelivery(req.StructuredMessage)
	} else {
		deliveryText = req.Message
	}

	if err := s.manager.Message(ctx, id, deliveryText, req.Interrupt); err != nil {
		if strings.Contains(err.Error(), "not found") {
			NotFound(w, "Agent")
			return
		}
		RuntimeError(w, "Failed to send message: "+err.Error())
		return
	}

	// Log message delivery
	logAttrs := []any{"agent_id", id}
	if req.StructuredMessage != nil {
		logAttrs = append(logAttrs, req.StructuredMessage.LogAttrs()...)
	}
	s.messageLog.Info("message delivered", logAttrs...)
	if s.dedicatedMessageLog != nil {
		s.dedicatedMessageLog.Info("message delivered", logAttrs...)
	}

	w.WriteHeader(http.StatusOK)
}

func (s *Server) execCommand(w http.ResponseWriter, r *http.Request, id string) {
	ctx := r.Context()

	var req ExecRequest
	if err := readJSON(r, &req); err != nil {
		BadRequest(w, "Invalid request body: "+err.Error())
		return
	}

	if len(req.Command) == 0 {
		ValidationError(w, "command is required", nil)
		return
	}

	output, err := s.runtime.Exec(ctx, id, req.Command)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			NotFound(w, "Agent")
			return
		}
		RuntimeError(w, "Failed to execute command: "+err.Error())
		return
	}

	writeJSON(w, http.StatusOK, ExecResponse{
		Output:   output,
		ExitCode: 0, // TODO: Get actual exit code from runtime
	})
}

func (s *Server) getLogs(w http.ResponseWriter, r *http.Request, id string) {
	ctx := r.Context()

	logs, err := s.runtime.GetLogs(ctx, id)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			NotFound(w, "Agent")
			return
		}
		RuntimeError(w, "Failed to get logs: "+err.Error())
		return
	}

	// Return logs as plain text
	w.Header().Set("Content-Type", "text/plain")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(logs))
}

func (s *Server) getStats(w http.ResponseWriter, r *http.Request, id string) {
	// TODO: Implement real stats from runtime
	// For now, return placeholder data
	writeJSON(w, http.StatusOK, StatsResponse{
		CPUUsagePercent:  0.0,
		MemoryUsageBytes: 0,
	})
}

// HasPromptResponse is the response for the has-prompt action.
type HasPromptResponse struct {
	HasPrompt bool `json:"hasPrompt"`
}

func (s *Server) checkAgentPrompt(w http.ResponseWriter, r *http.Request, id string) {
	ctx := r.Context()

	// Find the agent to get its grove path
	agents, err := s.manager.List(ctx, map[string]string{"scion.agent": "true"})
	if err != nil {
		RuntimeError(w, "Failed to list agents: "+err.Error())
		return
	}

	var agent *api.AgentInfo
	for i := range agents {
		if agents[i].Name == id || agents[i].ContainerID == id || agents[i].Slug == id {
			agent = &agents[i]
			break
		}
	}

	if agent == nil {
		NotFound(w, "Agent")
		return
	}

	if agent.GrovePath == "" {
		// No grove path means we can't check prompt.md
		writeJSON(w, http.StatusOK, HasPromptResponse{HasPrompt: false})
		return
	}

	// Check if prompt.md exists and has content
	// Path: <grovePath>/agents/<agentName>/prompt.md
	promptPath := filepath.Join(agent.GrovePath, "agents", agent.Name, "prompt.md")
	content, err := os.ReadFile(promptPath)
	if err != nil {
		if os.IsNotExist(err) {
			writeJSON(w, http.StatusOK, HasPromptResponse{HasPrompt: false})
			return
		}
		// Log the error but return false
		s.agentLifecycleLog.Warn("Failed to read prompt.md", "agent_id", id, "path", promptPath, "error", err)
		writeJSON(w, http.StatusOK, HasPromptResponse{HasPrompt: false})
		return
	}

	hasPrompt := len(strings.TrimSpace(string(content))) > 0
	writeJSON(w, http.StatusOK, HasPromptResponse{HasPrompt: hasPrompt})
}

// extractRequiredEnvKeys determines the set of env keys required by the agent's
// harness, auth type, and settings profile. It uses a multi-phase approach:
//
// Phase 1 (auth-aware): Resolves the harness type and auth_selected_type from
// on-disk harness-config and settings, then calls RequiredAuthEnvKeys() to get
// intrinsic credential requirements for the (harness, authType) pair.
//
// Phase 2 (settings-based): Extracts keys with empty values from settings
// harness_configs[*].env and profiles[*].env, allowing users to declare custom
// env requirements.
//
// Phase 3 (secrets): Collects explicitly-declared secrets from settings and templates.
func (s *Server) extractRequiredEnvKeys(req CreateAgentRequest) ([]string, map[string]api.SecretKeyInfo) {
	required := make(map[string]struct{})

	var settings *config.VersionedSettings
	settingsPath := req.GrovePath
	if settingsPath == "" {
		// Fall back to the broker's global .scion directory for settings
		// resolution. This matches what agent.Start → GetResolvedProjectDir("")
		// does when grovePath is empty (e.g., hub-only git groves without a
		// linked local path on the broker).
		if globalDir, err := config.GetGlobalDir(); err == nil {
			settingsPath = globalDir
			if s.config.Debug {
				s.envSecretLog.Debug("extractRequiredEnvKeys: grovePath empty, using global dir",
					"globalDir", globalDir,
				)
			}
		}
	}
	if settingsPath != "" {
		vs, _, err := config.LoadEffectiveSettings(settingsPath)
		if err == nil {
			settings = vs
			if s.config.Debug {
				s.envSecretLog.Debug("extractRequiredEnvKeys: loaded settings",
					"path", settingsPath,
					"defaultHarnessConfig", vs.DefaultHarnessConfig,
					"harnessConfigCount", len(vs.HarnessConfigs),
				)
			}
		} else if s.config.Debug {
			s.envSecretLog.Debug("extractRequiredEnvKeys: failed to load settings",
				"path", settingsPath,
				"error", err.Error(),
			)
		}
	}

	profileName := ""
	if req.Config != nil {
		profileName = req.Config.Profile
	}
	if profileName == "" && settings != nil {
		profileName = settings.ActiveProfile
	}

	// Phase 1: Auth-aware env key extraction
	// Resolve harness type and auth_selected_type, then derive required keys.
	secretInfo := make(map[string]api.SecretKeyInfo)
	harnessConfigName := s.resolveHarnessConfigName(req, settings)
	if s.config.Debug {
		s.envSecretLog.Debug("extractRequiredEnvKeys: harness resolution",
			"harnessConfigName", harnessConfigName,
			"hasSettings", settings != nil,
			"grovePath", req.GrovePath,
		)
	}
	if harnessConfigName != "" {
		var harnessType, authType string

		// Try on-disk harness-config directory first (check grovePath,
		// then fall back to global dir for hub-dispatched agents without a local grove)
		harnessConfigSearchPath := req.GrovePath
		if harnessConfigSearchPath == "" {
			harnessConfigSearchPath = settingsPath
		}
		if harnessConfigSearchPath != "" {
			if hcDir, err := config.FindHarnessConfigDir(harnessConfigName, harnessConfigSearchPath); err == nil {
				harnessType = hcDir.Config.Harness
				authType = hcDir.Config.AuthSelectedType
			}
		}

		// Settings harness_configs entry can provide/override
		if settings != nil {
			if hcfg, ok := settings.HarnessConfigs[harnessConfigName]; ok {
				if harnessType == "" {
					harnessType = hcfg.Harness
				}
				if authType == "" {
					authType = hcfg.AuthSelectedType
				}
			}
		}

		// Profile harness_overrides can override auth type
		if profileName != "" && settings != nil {
			if profile, ok := settings.Profiles[profileName]; ok {
				if override, ok := profile.HarnessOverrides[harnessConfigName]; ok {
					if override.AuthSelectedType != "" {
						authType = override.AuthSelectedType
					}
				}
			}
		}

		// Template-level auth_selectedType takes high precedence
		if req.Config != nil && req.Config.Template != "" && req.GrovePath != "" {
			if tmpl, err := config.FindTemplateInGrovePath(req.Config.Template, req.GrovePath); err == nil {
				if cfg, err := tmpl.LoadConfig(); err == nil && cfg != nil && cfg.AuthSelectedType != "" {
					authType = cfg.AuthSelectedType
				}
			}
		}

		// --harness-auth CLI flag takes ultimate precedence
		if req.Config != nil && req.Config.HarnessAuth != "" {
			authType = req.Config.HarnessAuth
		}

		// When auth type is unset (auto-detect), check if resolved file secrets
		// can satisfy an alternative auth method before defaulting to api-key.
		// This mirrors the auto-detect priority in each harness's ResolveAuth:
		// e.g., for gemini, OAuth creds (auth-file) take precedence over requiring
		// an API key when the OAUTH_CREDS file secret is available.
		if authType == "" {
			fileSecretNames := make(map[string]struct{})
			for _, sec := range req.ResolvedSecrets {
				if sec.Type == "file" {
					fileSecretNames[sec.Name] = struct{}{}
				}
			}
			if detected := harness.DetectAuthTypeFromFileSecrets(harnessType, fileSecretNames); detected != "" {
				authType = detected
			}
		}

		// Resolve auth key groups and check satisfaction
		if keyGroups := harness.RequiredAuthEnvKeys(harnessType, authType); len(keyGroups) > 0 {
			// Build lookup of already-satisfied keys
			envKeys := make(map[string]struct{})
			for k, v := range req.ResolvedEnv {
				if v != "" {
					envKeys[k] = struct{}{}
				}
			}
			for _, sec := range req.ResolvedSecrets {
				if sec.Type == "environment" || sec.Type == "" {
					target := sec.Target
					if target == "" {
						target = sec.Name
					}
					if target != "" {
						envKeys[target] = struct{}{}
					}
				}
			}

			for _, group := range keyGroups {
				satisfied := false
				for _, key := range group {
					if _, ok := envKeys[key]; ok {
						satisfied = true
						break
					}
				}
				if !satisfied {
					// Add the canonical (first) key as required
					canonicalKey := group[0]
					required[canonicalKey] = struct{}{}
					secretInfo[canonicalKey] = api.SecretKeyInfo{Source: "auth"}
				}
			}
		}

		// Phase 1b: Auth-required file secrets (e.g. ADC for vertex-ai)
		if authSecrets := harness.RequiredAuthSecrets(harnessType, authType); len(authSecrets) > 0 {
			// Build lookup of file-type resolved secrets by Name and Target suffix
			fileSecrets := make(map[string]struct{})
			for _, sec := range req.ResolvedSecrets {
				if sec.Type == "file" {
					fileSecrets[sec.Name] = struct{}{}
					if sec.Target != "" {
						fileSecrets[sec.Target] = struct{}{}
					}
				}
			}

			for _, as := range authSecrets {
				if _, ok := fileSecrets[as.Key]; !ok {
					required[as.Key] = struct{}{}
					secretInfo[as.Key] = api.SecretKeyInfo{
						Description: as.Description,
						Source:      "auth",
						Type:        "file",
					}
				}
			}
		}
	}

	// Phase 2: Settings-based empty-value env key extraction
	if settings != nil {
		// Get profile env keys
		if profileName != "" && settings.Profiles != nil {
			if profile, ok := settings.Profiles[profileName]; ok {
				for k, v := range profile.Env {
					if v == "" {
						required[k] = struct{}{}
					}
				}
				// Check harness overrides within the profile
				for _, override := range profile.HarnessOverrides {
					for k, v := range override.Env {
						if v == "" {
							required[k] = struct{}{}
						}
					}
				}
			}
		}

		// Get harness config env keys
		for _, hcfg := range settings.HarnessConfigs {
			for k, v := range hcfg.Env {
				if v == "" {
					required[k] = struct{}{}
				}
			}
		}
	}

	// Phase 3: Secrets declarations from settings and template

	// 3a: Settings-derived empty-value env keys are secret-eligible
	for k := range required {
		if _, exists := secretInfo[k]; !exists {
			secretInfo[k] = api.SecretKeyInfo{Source: "settings"}
		}
	}

	// 3b: Settings harness_configs[*].secrets
	if settings != nil {
		for _, hcfg := range settings.HarnessConfigs {
			for _, sec := range hcfg.Secrets {
				required[sec.Key] = struct{}{}
				secretInfo[sec.Key] = api.SecretKeyInfo{
					Description: sec.Description,
					Source:      "settings",
					Type:        sec.Type,
				}
			}
		}

		// 3c: Profile secrets
		if profileName != "" && settings.Profiles != nil {
			if profile, ok := settings.Profiles[profileName]; ok {
				for _, sec := range profile.Secrets {
					required[sec.Key] = struct{}{}
					secretInfo[sec.Key] = api.SecretKeyInfo{
						Description: sec.Description,
						Source:      "settings",
						Type:        sec.Type,
					}
				}
			}
		}
	}

	// 3d: Template secrets (from request or local template)
	for _, sec := range req.RequiredSecrets {
		required[sec.Key] = struct{}{}
		secretInfo[sec.Key] = api.SecretKeyInfo{
			Description: sec.Description,
			Source:      "template",
			Type:        sec.Type,
		}
	}
	// Also try loading local template config
	if req.Config != nil && req.Config.Template != "" && req.GrovePath != "" {
		if tmpl, err := config.FindTemplateInGrovePath(req.Config.Template, req.GrovePath); err == nil {
			if cfg, err := tmpl.LoadConfig(); err == nil && cfg != nil {
				for _, sec := range cfg.Secrets {
					required[sec.Key] = struct{}{}
					if _, exists := secretInfo[sec.Key]; !exists {
						secretInfo[sec.Key] = api.SecretKeyInfo{
							Description: sec.Description,
							Source:      "template",
							Type:        sec.Type,
						}
					}
				}
			}
		}
	}

	keys := make([]string, 0, len(required))
	for k := range required {
		keys = append(keys, k)
	}
	return keys, secretInfo
}

// resolveHarnessConfigName determines which harness-config to use for the agent.
// Resolution chain:
//  1. req.Config.HarnessConfig (explicit harness config override)
//  2. req.Config.Template (if it matches a valid harness-config directory)
//  3. profile's DefaultHarnessConfig
//  4. settings' DefaultHarnessConfig
func (s *Server) resolveHarnessConfigName(req CreateAgentRequest, settings *config.VersionedSettings) string {
	// 1. Explicit harness in config
	if req.Config != nil && req.Config.HarnessConfig != "" {
		return req.Config.HarnessConfig
	}

	// 2. Template name that matches an on-disk harness-config directory
	if req.Config != nil && req.Config.Template != "" {
		if req.GrovePath != "" {
			if _, err := config.FindHarnessConfigDir(req.Config.Template, req.GrovePath); err == nil {
				return req.Config.Template
			}
		}

		// 3. Template name that matches a settings harness_configs entry
		if settings != nil {
			if _, ok := settings.HarnessConfigs[req.Config.Template]; ok {
				return req.Config.Template
			}
		}
	}

	if settings == nil {
		return ""
	}

	// Resolve profile name
	profileName := ""
	if req.Config != nil {
		profileName = req.Config.Profile
	}
	if profileName == "" {
		profileName = settings.ActiveProfile
	}

	// 4. Profile's DefaultHarnessConfig
	if profileName != "" {
		if profile, ok := settings.Profiles[profileName]; ok {
			if profile.DefaultHarnessConfig != "" {
				return profile.DefaultHarnessConfig
			}
		}
	}

	// 5. Settings' DefaultHarnessConfig
	if settings.DefaultHarnessConfig != "" {
		return settings.DefaultHarnessConfig
	}

	return ""
}

// resolveHarnessIdentity loads the on-disk harness-config directory and applies
// settings overrides to determine the harness name and auth_selected_type.
func (s *Server) resolveHarnessIdentity(name, grovePath string, settings *config.VersionedSettings, reqConfig *CreateAgentConfig) (harnessName, authSelectedType string) {
	// Try loading from on-disk harness-config directory
	if grovePath != "" {
		if hcDir, err := config.FindHarnessConfigDir(name, grovePath); err == nil {
			harnessName = hcDir.Config.Harness
			authSelectedType = hcDir.Config.AuthSelectedType
		}
	}

	// If no on-disk config found, check if the name itself is a known harness
	if harnessName == "" {
		// Check if the name matches a settings harness-config entry
		if settings != nil {
			if hcEntry, ok := settings.HarnessConfigs[name]; ok {
				harnessName = hcEntry.Harness
				authSelectedType = hcEntry.AuthSelectedType
			}
		}
		// Fall back to treating the name as a harness name directly
		if harnessName == "" {
			harnessName = name
		}
	}

	// Apply settings-level overrides via ResolveHarnessConfig
	if settings != nil {
		profileName := ""
		if reqConfig != nil {
			profileName = reqConfig.Profile
		}
		resolved, err := settings.ResolveHarnessConfig(profileName, name)
		if err == nil && resolved.AuthSelectedType != "" {
			authSelectedType = resolved.AuthSelectedType
		}
	}

	return harnessName, authSelectedType
}

// finalizeEnv handles the second phase of env-gather: receiving gathered env vars
// from the Hub and starting the agent with the complete environment.
func (s *Server) finalizeEnv(w http.ResponseWriter, r *http.Request, id string) {
	ctx := r.Context()

	var req FinalizeEnvRequest
	if err := readJSON(r, &req); err != nil {
		BadRequest(w, "Invalid request body: "+err.Error())
		return
	}

	// Look up pending state
	s.pendingEnvGatherMu.Lock()
	s.cleanupExpiredPendingLocked(time.Now())
	pending, ok := s.pendingEnvGather[id]
	if ok && pending.State == pendingStateFinalizing {
		s.pendingEnvGatherMu.Unlock()
		writeError(w, http.StatusConflict, ErrCodeConflict, "agent finalize-env already in progress", map[string]interface{}{
			"agentId": id,
		})
		return
	}
	if ok {
		pending.State = pendingStateFinalizing
		pending.UpdatedAt = time.Now()
		pending.FinalizeRuns++
		s.upsertPendingState(pending)
	}
	s.pendingEnvGatherMu.Unlock()

	if !ok {
		NotFound(w, "Pending agent")
		return
	}

	// Merge gathered env into the previously merged env
	for k, v := range req.Env {
		pending.MergedEnv[k] = v
	}

	if s.config.Debug {
		s.envSecretLog.Debug("Finalize-env: merging gathered env", "gatheredKeys", len(req.Env), "totalEnv", len(pending.MergedEnv))
	}

	// Build start options from the pending request
	origReq := pending.Request
	opts := api.StartOptions{
		Name:       origReq.Name,
		BrokerMode: true,
		Detached:   boolPtr(!origReq.Attach),
		GrovePath:  origReq.GrovePath,
	}

	if origReq.Config != nil {
		opts.Template = origReq.Config.Template
		opts.Image = origReq.Config.Image
		opts.HarnessConfig = origReq.Config.HarnessConfig
		opts.Task = origReq.Config.Task
		opts.Workspace = origReq.Config.Workspace
		opts.Profile = origReq.Config.Profile
	}

	if s.config.Debug {
		s.envSecretLog.Debug("Finalize-env: StartOptions built from pending request",
			"name", opts.Name,
			"grovePath", opts.GrovePath,
			"template", opts.Template,
			"image", opts.Image,
			"profile", opts.Profile,
			"harnessConfig", opts.HarnessConfig,
			"hasConfig", origReq.Config != nil,
		)
	}

	// Save template slug before hydration may replace opts.Template with a cache path
	templateSlug := ""
	if origReq.Config != nil {
		templateSlug = origReq.Config.Template
	}

	// Hydrate template if needed
	hydrator := s.resolveHydrator(r)
	if hydrator != nil && origReq.Config != nil {
		templatePath, err := s.hydrateTemplate(ctx, origReq.Config, hydrator)
		if err != nil {
			TemplateError(w, "Failed to hydrate template: "+err.Error())
			return
		}
		if templatePath != "" {
			opts.Template = templatePath
		}
	}

	// Preserve human-friendly template slug for container labels
	if templateSlug != "" {
		opts.TemplateName = templateSlug
	}

	// Git clone mode
	if origReq.Config != nil && origReq.Config.GitClone != nil {
		gc := origReq.Config.GitClone
		pending.MergedEnv["SCION_GIT_CLONE_URL"] = gc.URL
		if gc.Branch != "" {
			pending.MergedEnv["SCION_GIT_BRANCH"] = gc.Branch
		}
		if gc.Depth > 0 {
			pending.MergedEnv["SCION_GIT_DEPTH"] = strconv.Itoa(gc.Depth)
		}
		opts.Workspace = ""
		opts.GrovePath = ""
		opts.GitClone = gc
	}

	opts.Env = pending.MergedEnv

	// Translate SCION_TELEMETRY_ENABLED into TelemetryOverride (same as createAgent/startAgent).
	if v, ok := pending.MergedEnv["SCION_TELEMETRY_ENABLED"]; ok {
		enabled := v == "true" || v == "1"
		opts.TelemetryOverride = &enabled
	}

	// Pass through resolved secrets
	if len(origReq.ResolvedSecrets) > 0 {
		opts.ResolvedSecrets = origReq.ResolvedSecrets
	}

	// Start the agent
	mgr := s.resolveManagerForOpts(opts)
	agentInfo, err := mgr.Start(ctx, opts)
	if err != nil {
		// Keep pending state for retry on transient start failures.
		s.pendingEnvGatherMu.Lock()
		if cur, exists := s.pendingEnvGather[id]; exists {
			cur.State = pendingStatePending
			cur.UpdatedAt = time.Now()
			s.upsertPendingState(cur)
		}
		s.pendingEnvGatherMu.Unlock()
		RuntimeError(w, "Failed to create agent: "+err.Error())
		return
	}

	s.pendingEnvGatherMu.Lock()
	s.deletePendingState(id)
	s.pendingEnvGatherMu.Unlock()

	resp := CreateAgentResponse{
		Agent:   agentInfoPtr(AgentInfoToResponse(*agentInfo)),
		Created: true,
	}

	writeJSON(w, http.StatusCreated, resp)
}

// resolveManagerForOpts returns the appropriate agent.Manager for the given
// start options. If opts.Profile resolves to a different runtime than the
// broker's default, a temporary manager is created with the profile-specific
// runtime. Otherwise the broker's shared manager is returned.
func (s *Server) resolveManagerForOpts(opts api.StartOptions) agent.Manager {
	if opts.Profile == "" {
		return s.manager
	}

	// Load settings to check if the profile explicitly specifies a different runtime.
	// If no settings exist or the profile isn't defined, stick with the default manager.
	projectDir, _ := config.GetResolvedProjectDir(opts.GrovePath)
	vs, _, _ := config.LoadEffectiveSettings(projectDir)
	if vs == nil {
		return s.manager
	}

	_, runtimeType, err := vs.ResolveRuntime(opts.Profile)
	if err != nil {
		// Profile or its runtime not found in settings; use default
		return s.manager
	}

	if runtimeType == s.runtime.Name() {
		return s.manager
	}

	// Profile specifies a different runtime - resolve and create a temporary manager
	resolved := agent.ResolveRuntime(opts.GrovePath, opts.Name, opts.Profile)

	if s.config.Debug {
		s.agentLifecycleLog.Debug("Profile resolved to different runtime",
			"agent", opts.Name, "profile", opts.Profile,
			"defaultRuntime", s.runtime.Name(),
			"resolvedRuntime", resolved.Name(),
		)
	}

	return agent.NewManager(resolved)
}

// Helper functions

// resolveGroveSettingsDir returns the directory containing settings.yaml for a grove.
// For linked groves, grovePath already points to the .scion directory.
// For hub-native groves, grovePath is the workspace parent, so settings
// live in the .scion subdirectory.
func resolveGroveSettingsDir(grovePath string) string {
	if config.GetSettingsPath(grovePath) != "" {
		return grovePath
	}
	candidate := filepath.Join(grovePath, ".scion")
	if config.GetSettingsPath(candidate) != "" {
		return candidate
	}
	return grovePath // fallback to original
}

func boolPtr(b bool) *bool {
	return &b
}

func agentInfoPtr(a AgentResponse) *AgentResponse {
	return &a
}

// ============================================================================
// Grove Endpoints
// ============================================================================

// handleGroveBySlug routes requests to /api/v1/groves/{slug}.
func (s *Server) handleGroveBySlug(w http.ResponseWriter, r *http.Request) {
	slug := extractID(r, "/api/v1/groves")
	if slug == "" {
		NotFound(w, "grove")
		return
	}

	switch r.Method {
	case http.MethodDelete:
		s.deleteGrove(w, r, slug)
	default:
		MethodNotAllowed(w)
	}
}

// deleteGrove removes the local hub-native grove directory for the given slug.
// Returns 204 on success (including when the directory doesn't exist).
func (s *Server) deleteGrove(w http.ResponseWriter, r *http.Request, slug string) {
	globalDir, err := config.GetGlobalDir()
	if err != nil {
		RuntimeError(w, "Failed to get global dir: "+err.Error())
		return
	}

	grovePath := filepath.Join(globalDir, "groves", slug)

	// Path traversal protection: ensure the resolved path stays inside the groves directory.
	grovesBase := filepath.Join(globalDir, "groves")
	absGrove, err := filepath.Abs(grovePath)
	if err != nil {
		RuntimeError(w, "Failed to resolve grove path: "+err.Error())
		return
	}
	absBase, err := filepath.Abs(grovesBase)
	if err != nil {
		RuntimeError(w, "Failed to resolve groves base path: "+err.Error())
		return
	}
	if !strings.HasPrefix(absGrove, absBase+string(filepath.Separator)) {
		s.agentLifecycleLog.Warn("grove cleanup path traversal blocked", "slug", slug, "resolved", absGrove)
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	if _, err := os.Stat(grovePath); os.IsNotExist(err) {
		// Already gone — idempotent success
		w.WriteHeader(http.StatusNoContent)
		return
	}

	if err := os.RemoveAll(grovePath); err != nil {
		s.agentLifecycleLog.Warn("failed to remove grove directory", "slug", slug, "path", grovePath, "error", err)
		RuntimeError(w, "Failed to remove grove directory: "+err.Error())
		return
	}

	s.agentLifecycleLog.Info("Removed hub-native grove directory", "slug", slug, "path", grovePath)
	w.WriteHeader(http.StatusNoContent)
}

// findAgentInHubNativeGroves scans hub-native grove directories
// (~/.scion/groves/<slug>/.scion/) for an agent directory matching the given
// name. Returns the .scion project dir path if found, or empty string.
// This is used as a fallback when the container is missing and the agent's
// grove path can't be determined from container labels.
func findAgentInHubNativeGroves(agentName string) string {
	globalDir, err := config.GetGlobalDir()
	if err != nil {
		return ""
	}
	grovesDir := filepath.Join(globalDir, "groves")
	entries, err := os.ReadDir(grovesDir)
	if err != nil {
		return ""
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		scionDir := filepath.Join(grovesDir, entry.Name(), ".scion")
		agentDir := filepath.Join(scionDir, "agents", agentName)
		if _, err := os.Stat(agentDir); err == nil {
			return scionDir
		}
	}
	return ""
}

// isLocalhostEndpoint returns true if the given endpoint URL refers to a
// loopback address (localhost, 127.0.0.1, [::1], etc.). This is used to
// decide whether the ContainerHubEndpoint bridge address should be
// substituted — containers can reach external hosts directly but need a
// bridge address to reach services on the host's loopback interface.
func isLocalhostEndpoint(endpoint string) bool {
	u, err := url.Parse(endpoint)
	if err != nil {
		return false
	}
	host := u.Hostname()
	return host == "localhost" || host == "127.0.0.1" || host == "::1"
}

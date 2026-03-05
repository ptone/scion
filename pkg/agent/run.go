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

package agent

import (
	"context"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/ptone/scion-agent/pkg/api"
	"github.com/ptone/scion-agent/pkg/apiclient"
	"github.com/ptone/scion-agent/pkg/config"
	"github.com/ptone/scion-agent/pkg/harness"
	"github.com/ptone/scion-agent/pkg/runtime"
	"github.com/ptone/scion-agent/pkg/util"
)

func (m *AgentManager) Start(ctx context.Context, opts api.StartOptions) (*api.AgentInfo, error) {
	// 0. Check if container already exists
	agents, err := m.Runtime.List(ctx, nil)
	if err == nil {
		for _, a := range agents {
			if a.ContainerID == opts.Name || a.Name == opts.Name || strings.TrimPrefix(a.Name, "/") == opts.Name {
				status := strings.ToLower(a.ContainerStatus)
				isRunning := strings.HasPrefix(status, "up") || status == "running"
				if isRunning {
					// If a new task is provided, we might want to recreate even if running
					// but if no task provided, we just return the running one
					if opts.Task == "" {
						a.Detached = true
						if opts.Detached != nil {
							a.Detached = *opts.Detached
						}
						a.Phase = "running"
						return &a, nil
					}
				}
				// If it exists but not running (or we have a new task), we delete it so we can recreate it
				if err := m.Runtime.Delete(ctx, a.ContainerID); err != nil {
					return nil, fmt.Errorf("failed to cleanup existing container: %w", err)
				}
			}
		}
	}

	projectDir, err := config.GetResolvedProjectDir(opts.GrovePath)
	if err != nil {
		return nil, err
	}
	groveName := config.GetGroveName(projectDir)

	// If resuming, verify the agent exists before proceeding
	if opts.Resume {
		agentDir := filepath.Join(projectDir, "agents", opts.Name)
		if _, err := os.Stat(agentDir); os.IsNotExist(err) {
			return nil, fmt.Errorf("cannot resume agent '%s': agent does not exist. Use 'scion start' to create a new agent", opts.Name)
		}
	}

	if opts.GitClone != nil {
		ctx = api.ContextWithGitClone(ctx, opts.GitClone)
	}

	util.Debugf("Start: calling GetAgent name=%s template=%q image=%q harnessConfig=%q grovePath=%q profile=%q",
		opts.Name, opts.Template, opts.Image, opts.HarnessConfig, opts.GrovePath, opts.Profile)
	agentDir, agentHome, agentWorkspace, finalScionCfg, err := GetAgent(ctx, opts.Name, opts.Template, opts.Image, opts.HarnessConfig, opts.GrovePath, opts.Profile, "", opts.Branch, opts.Workspace)
	if err != nil {
		return nil, err
	}
	if finalScionCfg != nil {
		util.Debugf("Start: GetAgent returned config: harness=%q harnessConfig=%q defaultHarnessConfig=%q image=%q",
			finalScionCfg.Harness, finalScionCfg.HarnessConfig, finalScionCfg.DefaultHarnessConfig, finalScionCfg.Image)
	} else {
		util.Debugf("Start: GetAgent returned nil config")
	}

	promptFile := filepath.Join(agentDir, "prompt.md")
	promptFileContent := ""
	if content, err := os.ReadFile(promptFile); err == nil {
		promptFileContent = strings.TrimSpace(string(content))
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("failed to read prompt file %s: %w", promptFile, err)
	}

	task := opts.Task

	if !opts.Resume && task != "" && promptFileContent != "" && task != promptFileContent {
		return nil, fmt.Errorf("task conflict: both prompt.md and start options provide a task")
	}

	if task == "" && !opts.Resume {
		task = promptFileContent
	} else if promptFileContent == "" && task != "" {
		_ = os.WriteFile(promptFile, []byte(task), 0644)
	}

	// Load settings for registry resolution
	settings, settingsWarnings, err := config.LoadEffectiveSettings(projectDir)
	if err != nil {
		// Fallback to defaults or log?
	}
	config.PrintDeprecationWarnings(settingsWarnings)

	harnessName := ""
	if finalScionCfg != nil {
		harnessName = finalScionCfg.Harness
	}

	// Resolve harness config name: CLI flag > stored config > template default > legacy fallback (harness name)
	harnessConfigName := opts.HarnessConfig
	if harnessConfigName == "" && finalScionCfg != nil && finalScionCfg.HarnessConfig != "" {
		harnessConfigName = finalScionCfg.HarnessConfig
	}
	if harnessConfigName == "" && finalScionCfg != nil && finalScionCfg.DefaultHarnessConfig != "" {
		harnessConfigName = finalScionCfg.DefaultHarnessConfig
	}
	if harnessConfigName == "" {
		harnessConfigName = harnessName
	}

	// Fall back to settings-based defaults (matches ProvisionAgent chain)
	if harnessConfigName == "" && settings != nil {
		effectiveProfile := opts.Profile
		if effectiveProfile == "" {
			effectiveProfile = settings.ActiveProfile
		}
		if effectiveProfile != "" {
			if p, ok := settings.Profiles[effectiveProfile]; ok && p.DefaultHarnessConfig != "" {
				harnessConfigName = p.DefaultHarnessConfig
			}
		}
	}
	if harnessConfigName == "" && settings != nil && settings.DefaultHarnessConfig != "" {
		harnessConfigName = settings.DefaultHarnessConfig
	}

	// Default values
	resolvedImage := ""
	unixUsername := "root"
	profileName := opts.Profile

	util.Debugf("image resolution: starting, harnessConfigName=%s", harnessConfigName)

	// Load on-disk harness-config for the container user and image (base layer).
	// The settings map may not define harness_configs, but the on-disk
	// config.yaml (seeded from harness embeds) always has the user field.
	// Also check template directories since harness-configs may be bundled
	// inside templates (§3.4 of agnostic-template-design).
	if harnessConfigName != "" {
		var templatePaths []string
		templateName := ""
		if finalScionCfg != nil && finalScionCfg.Info != nil {
			templateName = finalScionCfg.Info.Template
		}
		if templateName == "" {
			templateName = opts.Template
		}
		if templateName != "" {
			if chain, err := config.GetTemplateChainInGrove(templateName, opts.GrovePath); err == nil {
				for _, tpl := range chain {
					templatePaths = append(templatePaths, tpl.Path)
				}
			}
		}
		if hcDir, err := config.FindHarnessConfigDir(harnessConfigName, projectDir, templatePaths...); err == nil {
			if hcDir.Config.Image != "" {
				resolvedImage = hcDir.Config.Image
				util.Debugf("image resolution: from on-disk harness-config image=%s path=%s", resolvedImage, hcDir.Path)
			}
			if hcDir.Config.User != "" {
				unixUsername = hcDir.Config.User
			}
		} else {
			util.Debugf("image resolution: on-disk harness-config %q not found: %v", harnessConfigName, err)
		}
	}

	if settings != nil && harnessConfigName != "" {
		hConfig, err := settings.ResolveHarnessConfig(opts.Profile, harnessConfigName)
		if err == nil {
			if hConfig.Image != "" {
				resolvedImage = hConfig.Image
				util.Debugf("image resolution: from settings harness-config image=%s", resolvedImage)
			}
			if hConfig.User != "" {
				unixUsername = hConfig.User
			}
		} else {
			util.Debugf("image resolution: settings harness-config %q not found", harnessConfigName)
		}
	}

	if settings != nil {
		if profileName == "" {
			profileName = settings.ActiveProfile
		}
	}

	var warnings []string

	if finalScionCfg != nil && finalScionCfg.Image != "" {
		resolvedImage = finalScionCfg.Image
		util.Debugf("image resolution: from agent/template config image=%s", resolvedImage)
	}

	// CLI Overrides
	if opts.Image != "" {
		resolvedImage = opts.Image
		util.Debugf("image resolution: from CLI --image flag image=%s", resolvedImage)
	}

	if resolvedImage == "" {
		util.Debugf("image resolution FAILED: harnessConfigName=%q, finalScionCfg.Image=%q, opts.Image=%q, projectDir=%s",
			harnessConfigName, finalScionCfg.Image, opts.Image, projectDir)
		return nil, fmt.Errorf("no container image resolved for agent %q. Set 'image' in the harness-config config.yaml, specify --image, or configure a harness-config in settings", opts.Name)
	}

	util.Debugf("image resolution: final image=%s", resolvedImage)

	h := harness.New(harnessName)

	// 3. Resolve credentials via new auth pipeline
	// Inject environment-type resolved secrets into opts.Env before auth
	// resolution so that hub-resolved credentials (from storage, secrets,
	// or env-gather roundtrip) are visible to GatherAuthWithEnv.
	for _, s := range opts.ResolvedSecrets {
		if (s.Type == "environment" || s.Type == "") && s.Value != "" {
			target := s.Target
			if target == "" {
				target = s.Name
			}
			if target != "" {
				if opts.Env == nil {
					opts.Env = make(map[string]string)
				}
				if _, exists := opts.Env[target]; !exists {
					opts.Env[target] = s.Value
				}
			}
		}
	}

	var auth api.AuthConfig
	var resolvedAuth *api.ResolvedAuth
	if !opts.NoAuth {
		auth = harness.GatherAuthWithEnv(opts.Env)
		util.Debugf("auth: gathered credentials — selectedType=%q, hasGeminiKey=%t, hasGoogleKey=%t, hasOAuth=%t, hasADC=%t, hasAnthropicKey=%t, cloudProject=%q",
			auth.SelectedType,
			auth.GeminiAPIKey != "",
			auth.GoogleAPIKey != "",
			auth.OAuthCreds != "",
			auth.GoogleAppCredentials != "",
			auth.AnthropicAPIKey != "",
			auth.GoogleCloudProject,
		)
		harness.OverlaySettings(&auth, h, agentHome)
		util.Debugf("auth: after overlay — selectedType=%q", auth.SelectedType)
		resolved, err := h.ResolveAuth(auth)
		if err != nil {
			return nil, fmt.Errorf("auth resolution failed: %w", err)
		}
		util.Debugf("auth: resolved — method=%q, envVars=%v, files=%d", resolved.Method, resolved.EnvVars, len(resolved.Files))
		if err := harness.ValidateAuth(resolved); err != nil {
			return nil, fmt.Errorf("auth validation failed: %w", err)
		}
		// Allow harnesses to update their native settings files (e.g. Gemini settings.json)
		if applier, ok := h.(api.AuthSettingsApplier); ok {
			if err := applier.ApplyAuthSettings(agentHome, resolved); err != nil {
				return nil, fmt.Errorf("failed to apply auth settings: %w", err)
			}
			util.Debugf("auth: applied harness-specific settings for %q", harnessName)
		}
		resolvedAuth = resolved
	}

	// 4. Launch container
	detached := true

	if finalScionCfg != nil {
		detached = finalScionCfg.IsDetached()
	}

	if opts.Detached != nil {
		detached = *opts.Detached
	}

	exists, err := m.Runtime.ImageExists(ctx, resolvedImage)
	if err != nil || !exists {
		if err := m.Runtime.PullImage(ctx, resolvedImage); err != nil {
			return nil, fmt.Errorf("failed to pull image '%s': %w", resolvedImage, err)
		}
	}

	template := ""
	if finalScionCfg != nil && finalScionCfg.Info != nil {
		template = finalScionCfg.Info.Template
	}
	// Prefer human-friendly template slug over cache path or UUID
	if opts.TemplateName != "" {
		template = opts.TemplateName
	}

	if opts.Env == nil {
		opts.Env = make(map[string]string)
	}
	opts.Env["SCION_AGENT_NAME"] = opts.Name
	opts.Env["SCION_GROVE"] = groveName
	if template != "" {
		opts.Env["SCION_TEMPLATE_NAME"] = template
	} else {
		opts.Env["SCION_TEMPLATE_NAME"] = "custom"
	}
	// Full template reference (cache path, URI, or name) for debugging
	if opts.Template != "" {
		opts.Env["SCION_TEMPLATE"] = opts.Template
	}
	if _, ok := opts.Env["SCION_BROKER_NAME"]; !ok {
		opts.Env["SCION_BROKER_NAME"] = "local"
	}
	if _, ok := opts.Env["SCION_CREATOR"]; !ok {
		if u, err := user.Current(); err == nil {
			opts.Env["SCION_CREATOR"] = u.Username
		}
	}

	// Determine whether hub is explicitly disabled in grove settings.
	// When disabled, we suppress hub env var injection from agent config
	// and template env sections (but not from caller-provided opts.Env,
	// which may come from an authoritative source like the runtime broker).
	hubDisabled := settings != nil && settings.IsHubExplicitlyDisabled()

	// Inject agent limit env vars from scion config
	if finalScionCfg != nil {
		if finalScionCfg.MaxTurns > 0 {
			opts.Env["SCION_MAX_TURNS"] = strconv.Itoa(finalScionCfg.MaxTurns)
		}
		if finalScionCfg.MaxModelCalls > 0 {
			opts.Env["SCION_MAX_MODEL_CALLS"] = strconv.Itoa(finalScionCfg.MaxModelCalls)
		}
		if finalScionCfg.MaxDuration != "" {
			opts.Env["SCION_MAX_DURATION"] = finalScionCfg.MaxDuration
		}
		// Agent-level hub endpoint takes highest priority, overriding
		// grove settings and server config values passed via opts.Env.
		if !hubDisabled && finalScionCfg.Hub != nil && finalScionCfg.Hub.Endpoint != "" {
			opts.Env["SCION_HUB_ENDPOINT"] = finalScionCfg.Hub.Endpoint
			opts.Env["SCION_HUB_URL"] = finalScionCfg.Hub.Endpoint
		}
	}

	// If hub endpoint not yet set from agent config or caller's opts.Env,
	// check grove settings so locally-started agents in hub-connected
	// groves also get hub connectivity.
	if _, hubSet := opts.Env["SCION_HUB_ENDPOINT"]; !hubSet {
		if groveSettings, err := config.LoadSettings(projectDir); err == nil {
			if groveSettings.IsHubEnabled() {
				if ep := groveSettings.GetHubEndpoint(); ep != "" {
					opts.Env["SCION_HUB_ENDPOINT"] = ep
					opts.Env["SCION_HUB_URL"] = ep
				}
			}
		}
	}
	// If hub endpoint is now set but no auth token, resolve dev auth token
	// from the host filesystem (env vars or ~/.scion/dev-token file).
	if _, ok := opts.Env["SCION_HUB_ENDPOINT"]; ok {
		if _, tokenSet := opts.Env["SCION_AUTH_TOKEN"]; !tokenSet {
			if token := apiclient.ResolveDevToken(); token != "" {
				opts.Env["SCION_AUTH_TOKEN"] = token
			}
		}
	}

	// Explicit SCION_HUB_ENDPOINT in scion config env section takes
	// final priority. This allows templates to specify a container-
	// accessible endpoint (e.g. http://host.docker.internal:8080)
	// that differs from the host-level hub endpoint.
	if !hubDisabled && finalScionCfg != nil && finalScionCfg.Env != nil {
		if ep, ok := finalScionCfg.Env["SCION_HUB_ENDPOINT"]; ok && ep != "" {
			expandedEp, _ := util.ExpandEnv(ep)
			if expandedEp != "" {
				opts.Env["SCION_HUB_ENDPOINT"] = expandedEp
				opts.Env["SCION_HUB_URL"] = expandedEp
			}
		}
	}

	// When hub is explicitly disabled, strip hub env vars from both
	// opts.Env and the scion config env section to prevent leakage
	// through buildAgentEnv (which processes scionCfg.Env independently).
	if hubDisabled {
		delete(opts.Env, "SCION_HUB_ENDPOINT")
		delete(opts.Env, "SCION_HUB_URL")
		if finalScionCfg != nil && finalScionCfg.Env != nil {
			delete(finalScionCfg.Env, "SCION_HUB_ENDPOINT")
			delete(finalScionCfg.Env, "SCION_HUB_URL")
		}
	}

	// Inject telemetry config as env vars for sciontool.
	// Only set vars not already present (respecting explicit overrides).
	if finalScionCfg != nil && finalScionCfg.Telemetry != nil {
		telemetryEnv := config.TelemetryConfigToEnv(finalScionCfg.Telemetry)
		for k, v := range telemetryEnv {
			if _, exists := opts.Env[k]; !exists {
				opts.Env[k] = v
			}
		}
	}

	agentEnv, envWarnings, missingEnvKeys := buildAgentEnv(finalScionCfg, opts.Env)
	if len(missingEnvKeys) > 0 {
		sort.Strings(missingEnvKeys)
		return nil, fmt.Errorf("cannot start agent: %d required environment variable(s) have no value: %s",
			len(missingEnvKeys), strings.Join(missingEnvKeys, ", "))
	}
	warnings = append(warnings, envWarnings...)

	// Determine the effective workspace path. If agentWorkspace is empty but we have
	// a volume mounted to /workspace (e.g., shared worktree case), use that source path.
	effectiveWorkspace := agentWorkspace
	if effectiveWorkspace == "" && finalScionCfg != nil {
		effectiveWorkspace = extractWorkspaceFromVolumes(finalScionCfg.Volumes)
	}

	repoRoot := ""
	if effectiveWorkspace != "" && util.IsGitRepoDir(effectiveWorkspace) {
		commonDir, err := util.GetCommonGitDir(effectiveWorkspace)
		if err == nil {
			repoRoot = filepath.Dir(commonDir)
		}
	} else if util.IsGitRepoDir(projectDir) {
		repoRoot, _ = util.RepoRootDir(projectDir)
	}

	// Telemetry defaults to enabled when not explicitly set to false.
	telemetryEnabled := finalScionCfg != nil && finalScionCfg.Telemetry != nil &&
		(finalScionCfg.Telemetry.Enabled == nil || *finalScionCfg.Telemetry.Enabled)

	runCfg := runtime.RunConfig{
		Name:             opts.Name,
		Template:         template,
		UnixUsername:     unixUsername,
		Image:            resolvedImage,
		HomeDir:          agentHome,
		Workspace:        effectiveWorkspace,
		RepoRoot:         repoRoot,
		ResolvedAuth:     resolvedAuth,
		Harness:          h,
		TelemetryEnabled: telemetryEnabled,
		Task: func() string {
			// When task_flag is set, task is delivered via CommandArgs instead
			if finalScionCfg != nil && finalScionCfg.TaskFlag != "" {
				return ""
			}
			return task
		}(),
		CommandArgs: func() []string {
			var args []string
			if finalScionCfg != nil {
				args = finalScionCfg.CommandArgs
				if finalScionCfg.Model != "" {
					// Prepend model flag so it appears before user args but is passed in baseArgs
					args = append([]string{"--model", finalScionCfg.Model}, args...)
				}
				// If task_flag is configured, append task as a flag value
				if finalScionCfg.TaskFlag != "" && task != "" {
					args = append(args, finalScionCfg.TaskFlag, task)
				}
			}
			return args
		}(),
		Env:             agentEnv,
		ResolvedSecrets: opts.ResolvedSecrets,
		Volumes: func() []api.VolumeMount {
			if finalScionCfg == nil {
				return nil
			}
			// If we extracted effectiveWorkspace from a /workspace volume mount,
			// filter it out to avoid a duplicate mount (the buildCommonRunArgs
			// will handle the workspace mount properly with worktree support).
			if effectiveWorkspace != "" && effectiveWorkspace != agentWorkspace {
				return filterWorkspaceVolume(finalScionCfg.Volumes)
			}
			return finalScionCfg.Volumes
		}(),
		Resources: func() *api.ResourceSpec {
			if finalScionCfg != nil {
				return finalScionCfg.Resources
			}
			return nil
		}(),
		Kubernetes: func() *api.KubernetesConfig {
			if finalScionCfg != nil {
				return finalScionCfg.Kubernetes
			}
			return nil
		}(),
		GitClone: opts.GitClone,
		Resume:   opts.Resume,
		Labels: map[string]string{
			"scion.agent":          "true",
			"scion.name":           api.Slugify(opts.Name),
			"scion.grove":          groveName,
			"scion.template":       template,
			"scion.harness_config": harnessConfigName,
		},
		Annotations: map[string]string{
			"scion.grove_path": projectDir,
		},
	}
	id, err := m.Runtime.Run(ctx, runCfg)
	if err != nil {
		if strings.Contains(err.Error(), "executable file not found") ||
			strings.Contains(err.Error(), "tmux: command not found") ||
			strings.Contains(err.Error(), "tmux: not found") {
			return nil, fmt.Errorf("failed to launch container: tmux binary not found in image '%s'. "+
				"Ensure the image has tmux installed. Error: %w", resolvedImage, err)
		}
		return nil, fmt.Errorf("failed to launch container: %w", err)
	}

	status := "running"
	if opts.Resume {
		status = "resumed"
	}
	_ = UpdateAgentConfig(opts.Name, opts.GrovePath, status, m.Runtime.Name(), profileName)

	// Fetch fresh info
	slug := api.Slugify(opts.Name)
	allAgents, err := m.Runtime.List(ctx, map[string]string{"scion.name": slug})
	if err == nil {
		for _, a := range allAgents {
			if a.ContainerID == id || strings.EqualFold(a.Name, opts.Name) {
				a.Detached = detached
				a.Warnings = warnings
				a.Phase = status
				return &a, nil
			}
		}
	}

	return &api.AgentInfo{ID: id, Name: opts.Name, Phase: status, Detached: detached, Warnings: warnings}, nil
}

// extractWorkspaceFromVolumes finds a volume mounted to /workspace and returns its source path.
// This is used when an agent shares an existing worktree from another agent.
func extractWorkspaceFromVolumes(volumes []api.VolumeMount) string {
	for _, v := range volumes {
		if v.Target == "/workspace" {
			return v.Source
		}
	}
	return ""
}

// filterWorkspaceVolume removes volumes targeting /workspace from the list.
// This is used when the workspace will be handled by the RepoRoot/Workspace logic
// in buildCommonRunArgs instead of as a generic volume mount.
func filterWorkspaceVolume(volumes []api.VolumeMount) []api.VolumeMount {
	var filtered []api.VolumeMount
	for _, v := range volumes {
		if v.Target != "/workspace" {
			filtered = append(filtered, v)
		}
	}
	return filtered
}

func buildAgentEnv(scionCfg *api.ScionConfig, extraEnv map[string]string) ([]string, []string, []string) {
	combined := make(map[string]string)
	var warnings []string
	var missingKeys []string

	if scionCfg != nil && scionCfg.Env != nil {
		for k, v := range scionCfg.Env {
			// Support variable substitution in keys and values
			expandedKey, _ := util.ExpandEnv(k)
			expandedValue, warned := util.ExpandEnv(v)

			if expandedKey == "" {
				continue
			}
			// If the value is empty and we warned about a missing variable,
			// skip adding it to combined to avoid a redundant warning later.
			if expandedValue == "" && warned {
				continue
			}
			// If the value is empty (no variable reference was used),
			// treat the key as an implicit host env passthrough: look up
			// the environment variable of the same name on the host.
			if expandedValue == "" {
				if hostVal, ok := os.LookupEnv(expandedKey); ok && hostVal != "" {
					expandedValue = hostVal
				}
			}
			combined[expandedKey] = expandedValue
		}
	}
	// Add extraEnv
	for k, v := range extraEnv {
		combined[k] = v
	}

	agentEnv := []string{}
	for k, v := range combined {
		if v == "" {
			missingKeys = append(missingKeys, k)
			warnings = append(warnings, fmt.Sprintf("Warning: Environment variable '%s' has no value and will be omitted.", k))
			continue
		}
		agentEnv = append(agentEnv, fmt.Sprintf("%s=%s", k, v))
	}
	return agentEnv, warnings, missingKeys
}


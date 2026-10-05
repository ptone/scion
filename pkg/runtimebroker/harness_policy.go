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
	"fmt"
	"net/http"
	"path/filepath"

	"github.com/GoogleCloudPlatform/scion/pkg/agent"
	"github.com/GoogleCloudPlatform/scion/pkg/api"
	"github.com/GoogleCloudPlatform/scion/pkg/config"
)

// harnessPolicyInput describes the harness-config a dispatch's launch will
// use, for enforceHarnessConfigPolicy. Req supplies the name inputs
// (Config.HarnessConfig / Template / Profile) and ProjectPath;
// HydratedTemplatePath and HydratedHCPath are the hub-hydrated paths launch
// will use (empty when none).
type harnessPolicyInput struct {
	Req                  CreateAgentRequest
	HydratedTemplatePath string
	HydratedHCPath       string
}

// enforceHarnessConfigPolicy is the broker's container-script policy gate,
// shared by create, start and restart. Each caller runs it at its single
// hydration point, against the same hydrated harness-config launch will use,
// and before any provisioning, start or stop side effect. It resolves the
// harness-config (lookupHarnessConfigForPolicy) and evaluates it
// (evaluateHarnessConfigPolicy).
//
// When the policy can refuse (allow_container_script_harnesses=false) and a
// hydrated copy is supplied but cannot be loaded, it fails closed with a 500.
// With allow=true every entry passes, so that case is left to launch.
//
// A non-OK decision carries the response (HTTPStatus, Code, Message) and, in
// Detail, the text for logs and dispatch-attempt records; callers write it
// with writeHarnessPolicyRefusal so every path answers identically.
func (s *Server) enforceHarnessConfigPolicy(in harnessPolicyInput) harnessPolicyDecision {
	name, entry, ok, err := s.lookupHarnessConfigForPolicy(in.Req, in.HydratedTemplatePath, in.HydratedHCPath)
	if err != nil {
		if s.config.AllowContainerScriptHarnesses {
			return harnessPolicyDecision{OK: true}
		}
		return harnessPolicyDecision{
			OK:         false,
			Code:       ErrCodeRuntimeError,
			HTTPStatus: 500,
			// The detail names a broker path; it goes to the log and the
			// dispatch attempt, not the response, as for launch errors.
			Message: "Failed to evaluate harness-config policy",
			Detail:  "Failed to evaluate harness-config policy: " + err.Error(),
		}
	}
	if !ok {
		return harnessPolicyDecision{OK: true}
	}
	return s.evaluateHarnessConfigPolicy(name, entry)
}

// writeHarnessPolicyRefusal writes a non-OK enforceHarnessConfigPolicy
// decision and logs it. op names the dispatch path for the log.
func (s *Server) writeHarnessPolicyRefusal(w http.ResponseWriter, d harnessPolicyDecision, op, agentID string) {
	s.agentLifecycleLog.Warn("Harness-config policy refused dispatch",
		"op", op, "agent_id", agentID, "status", d.HTTPStatus, "detail", d.detail())
	writeError(w, d.HTTPStatus, d.Code, d.Message, nil)
}

// harnessPolicyInputForStart builds the policy input for start and restart
// from the built start context: the hydrated harness-config launch will use
// (opts.HarnessConfigPath, set by buildStartContext's hydration) and the
// harness-config name launch resolves (pkg/agent Start): the dispatch's
// opts.HarnessConfig, else the agent's saved harness-config, else the
// settings default (resolved by lookupHarnessConfigForPolicy). The template
// chain is taken from opts.Template, as harness.Resolve's caller does.
func harnessPolicyInputForStart(opts api.StartOptions, agentID string) harnessPolicyInput {
	projectPath := opts.ProjectPath
	if projectPath != "" {
		if dir, err := config.GetResolvedProjectDir(projectPath); err == nil {
			projectPath = dir
		}
	}
	name := opts.HarnessConfig
	if name == "" && projectPath != "" {
		name = agent.GetSavedHarnessConfig(agentID, projectPath)
	}
	req := CreateAgentRequest{
		ProjectPath: projectPath,
		Config: &CreateAgentConfig{
			HarnessConfig: name,
			Profile:       opts.Profile,
		},
	}
	var hydratedTemplatePath string
	if filepath.IsAbs(opts.Template) {
		hydratedTemplatePath = opts.Template
	} else {
		req.Config.Template = opts.Template
	}
	return harnessPolicyInput{
		Req:                  req,
		HydratedTemplatePath: hydratedTemplatePath,
		HydratedHCPath:       opts.HarnessConfigPath,
	}
}

// lookupHarnessConfigForPolicy resolves the harness-config that this
// dispatch will use, returning the entry needed by
// evaluateHarnessConfigPolicy. The directory is resolved through
// config.ResolveHarnessConfigDir, the same ordering launch and
// extractRequiredEnvKeys use: the hub-hydrated copy (hydratedHCPath) when
// supplied, else template-bundled, project, then global directories, with the
// template chain taken from hydratedTemplatePath when supplied (else the
// request's template slug). A settings harness_configs entry is the last
// fallback. ok is false when no harness-config was specified or could be
// found, which short-circuits the policy check (no policy applies).
//
// err is non-nil only when hydratedHCPath is set but cannot be loaded. That
// is not a "not found" case: launch would use exactly that directory, so
// the caller must not fall back to evaluating some other entry (fail
// closed) when the policy can refuse.
func (s *Server) lookupHarnessConfigForPolicy(req CreateAgentRequest, hydratedTemplatePath, hydratedHCPath string) (string, config.HarnessConfigEntry, bool, error) {
	var settings *config.VersionedSettings
	settingsPath := req.ProjectPath
	if settingsPath == "" {
		if globalDir, err := config.GetGlobalDir(); err == nil {
			settingsPath = globalDir
		}
	}
	if settingsPath != "" {
		if vs, _, err := config.LoadEffectiveSettings(settingsPath); err == nil {
			settings = vs
		}
	}

	name := s.resolveHarnessConfigForEnvGather(req, settings)
	if name == "" {
		return "", config.HarnessConfigEntry{}, false, nil
	}

	searchPath := req.ProjectPath
	if searchPath == "" {
		searchPath = settingsPath
	}
	templateForChain := hydratedTemplatePath
	if templateForChain == "" && req.Config != nil {
		templateForChain = req.Config.Template
	}
	if hydratedHCPath != "" || searchPath != "" {
		hcDir, err := config.ResolveHarnessConfigDir(hydratedHCPath, name, searchPath, templateChainPaths(templateForChain, searchPath)...)
		if err == nil && hcDir != nil {
			return name, hcDir.Config, true, nil
		}
		if hydratedHCPath != "" {
			return name, config.HarnessConfigEntry{}, false, fmt.Errorf("hydrated harness-config %q could not be loaded: %w", name, err)
		}
	}
	if settings != nil {
		if hcfg, ok := settings.HarnessConfigs[name]; ok {
			return name, hcfg, true, nil
		}
	}
	return name, config.HarnessConfigEntry{}, false, nil
}

// templateChainPaths returns the on-disk paths of template's chain resolved
// against searchPath, in merge order, for use as the template-bundled tier of
// config.ResolveHarnessConfigDir. template may be a slug or an absolute
// (hydrated) path. Returns nil when either input is empty or the chain does
// not resolve.
func templateChainPaths(template, searchPath string) []string {
	if template == "" || searchPath == "" {
		return nil
	}
	chain, err := config.GetTemplateChainInProject(template, searchPath)
	if err != nil {
		return nil
	}
	paths := make([]string, 0, len(chain))
	for _, tpl := range chain {
		paths = append(paths, tpl.Path)
	}
	return paths
}

// harnessPolicyDecision describes the outcome of a per-dispatch policy check.
// Code is the API error code (matches errors.go constants); HTTPStatus is the
// HTTP status the broker should return; Message is the user-facing error
// string. An ok=true decision means the dispatch may proceed.
type harnessPolicyDecision struct {
	OK         bool
	Code       string
	HTTPStatus int
	Message    string
	// Detail, when set, is a fuller description for logs and dispatch
	// attempts than the user-facing Message.
	Detail string
}

// detail returns Detail, or Message when no separate detail was set.
func (d harnessPolicyDecision) detail() string {
	if d.Detail != "" {
		return d.Detail
	}
	return d.Message
}

// evaluateHarnessConfigPolicy enforces broker-level dispatch policy on the
// resolved harness-config. Today it gates container-script provisioners
// behind ServerConfig.AllowContainerScriptHarnesses (defaults to true).
// Set AllowContainerScriptHarnesses=false to block container-script
// dispatches on this broker.
//
// Caller passes the resolved harness-config name (for logs/error text) and
// the parsed config entry. Returns a non-OK decision when the dispatch
// should be refused.
//
// The policy is intentionally minimal in v1 — scripted provisioning is gated.
// Future additions (e.g., trusted_harness_config_publishers) can extend this
// function without touching the dispatch path.
//
// GUARD: entry is the resolved harness-config directory (or settings) entry
// WITHOUT the profile harness_overrides merge that launch applies
// (VersionedSettings.ResolveHarnessConfig via harness.Resolve). Today the
// policy reads only Provisioner, which overrides cannot set, so the two agree.
// Any policy field that a profile harness_override can mutate (image, image
// pull policy, user, env, volumes, auth type, resources) MUST be evaluated
// after the override merge, or the gate will judge a different value than the
// one launch uses. TestEvaluateHarnessConfigPolicy_IgnoresOverrideMutableFields
// fails if this function starts depending on such a field.
func (s *Server) evaluateHarnessConfigPolicy(harnessConfigName string, entry config.HarnessConfigEntry) harnessPolicyDecision {
	if entry.Provisioner == nil {
		return harnessPolicyDecision{OK: true}
	}
	if s.config.AllowContainerScriptHarnesses {
		return harnessPolicyDecision{OK: true}
	}
	return harnessPolicyDecision{
		OK:         false,
		Code:       ErrCodeForbidden,
		HTTPStatus: 403,
		Message: fmt.Sprintf(
			"harness-config %q uses scripted provisioning but this broker has allow_container_script_harnesses=false. Set broker.allow_container_script_harnesses=true (SCION_SERVER_BROKER_ALLOWCONTAINERSCRIPTHARNESSES=true) to enable.",
			harnessConfigName,
		),
	}
}

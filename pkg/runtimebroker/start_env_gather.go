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
	"net/http"
	"sort"
	"time"

	"github.com/GoogleCloudPlatform/scion/pkg/api"
	"github.com/GoogleCloudPlatform/scion/pkg/config"
)

// startEnvGatherInputs are the parts of a start request the env-gather
// preflight reads (ptone/scion#6101). A start sends them under the same wire
// keys as a create.
type startEnvGatherInputs struct {
	// Name is the agent's slug, the start's URL segment.
	Name string
	// AgentID is the hub agent ID the needs answer carries ("" falls back
	// to Name).
	AgentID               string
	ProjectPath           string
	ProjectSlug           string
	TemplateName          string
	TemplateID            string
	TemplateHash          string
	HarnessConfig         string
	HarnessConfigID       string
	HarnessConfigHash     string
	Profile               *string
	HarnessAuth           string
	ResolvedEnv           map[string]string
	ResolvedSecrets       []api.ResolvedSecret
	RequiredSecrets       []api.RequiredSecret
	AvailableAsNeededKeys []string
	InlineConfig          *api.ScionConfig
	SharedWorkspace       bool
	// EnvGatherFinal marks the hub's replay after its as-needed pass: keys
	// required only as passthrough markers may then stay unmet.
	EnvGatherFinal bool
}

// startEnvGatherRequest returns the create request whose env-gather
// preflight scores a start of an existing agent exactly as create scores
// it. persistedEnv is the agent's config env as the start will apply it
// (startPersistedEnv); its non-empty values are the request's config env,
// below the resolved env, which outranks the stored config env at launch
// (buildAgentEnv).
func startEnvGatherRequest(in startEnvGatherInputs, projectPath string, persistedEnv map[string]string) CreateAgentRequest {
	cfg := &CreateAgentConfig{
		Template:          in.TemplateName,
		TemplateID:        in.TemplateID,
		TemplateHash:      in.TemplateHash,
		HarnessConfig:     in.HarnessConfig,
		HarnessConfigID:   in.HarnessConfigID,
		HarnessConfigHash: in.HarnessConfigHash,
		HarnessAuth:       in.HarnessAuth,
	}
	if in.Profile != nil {
		cfg.Profile = *in.Profile
	}
	keys := make([]string, 0, len(persistedEnv))
	for k, v := range persistedEnv {
		if v == "" {
			continue
		}
		if _, sent := in.ResolvedEnv[k]; sent {
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		cfg.Env = append(cfg.Env, k+"="+persistedEnv[k])
	}
	return CreateAgentRequest{
		Name:                  in.Name,
		ProjectPath:           projectPath,
		Config:                cfg,
		ResolvedEnv:           in.ResolvedEnv,
		ResolvedSecrets:       in.ResolvedSecrets,
		RequiredSecrets:       in.RequiredSecrets,
		AvailableAsNeededKeys: in.AvailableAsNeededKeys,
	}
}

// startGatherProjectPath is the project path the preflight reads settings
// from: the request's path, else the hub-managed project directory of its
// slug (what buildStartContext resolves, without its side effects), else ""
// (the preflight then reads the broker's global settings, as create does).
func startGatherProjectPath(in startEnvGatherInputs) string {
	slug, _ := splitHubGlobalSlug(in.ProjectPath, in.ProjectSlug)
	if in.ProjectPath != "" || slug == "" || !isSingleCleanPathElement(slug) {
		return in.ProjectPath
	}
	globalDir, err := config.GetGlobalDir()
	if err != nil {
		return ""
	}
	return config.BrokerProjectPath(globalDir, "", slug)
}

// startPersistedEnv returns the env of the agent's scion-agent.json with the
// start's inline config merged over it, as applyInlineConfigUpdate writes it
// before the start. An agent without a readable config (its directory is
// gone) has none: the on-the-fly provision builds its config from the
// template and settings, which the preflight reads itself.
func startPersistedEnv(name, projectPath string, inline *api.ScionConfig, sharedWorkspace bool) map[string]string {
	loaded, err := readAgentScionConfig(name, projectPath, sharedWorkspace)
	if err != nil || loaded.cfg == nil {
		if inline != nil {
			return inline.Env
		}
		return nil
	}
	return config.MergeScionConfig(loaded.cfg, inline).Env
}

// startEnvGather runs create's env-gather preflight for a start sent with
// gatherEnv (ptone/scion#6101), before the start registers, resolves its
// context or touches the runtime. It returns the needs answer to write when
// required keys are unmet: any need on the first pass; on the final pass
// (EnvGatherFinal) only needs that are not marker-only. A nil answer means
// the start goes on, with pre the harness-config hydration it already did.
// A template hydration failure fails the start as it fails a create's
// preflight. The hydrated template is used for scoring only and is not
// handed to launch: an existing agent starts from its own state, and its
// template is applied only when it has to be provisioned again
// (applyStartTemplateIdentity).
func (s *Server) startEnvGather(ctx context.Context, r *http.Request, in startEnvGatherInputs) (needs *EnvRequirementsResponse, pre prehydratedBundle, sce *startContextError) {
	projectPath := startGatherProjectPath(in)
	req := startEnvGatherRequest(in, projectPath, startPersistedEnv(in.Name, projectPath, in.InlineConfig, in.SharedWorkspace))

	var templatePath, hcPath string
	if in.TemplateID != "" || in.TemplateHash != "" {
		if hubConn := s.resolveHubConnection(r); hubConn != nil {
			p, err := s.hydrateTemplate(ctx, req.Config, hubConn)
			if err != nil {
				return nil, pre, &startContextError{
					Status:      http.StatusInternalServerError,
					Message:     "Failed to hydrate template: " + err.Error(),
					IsHubError:  true,
					OriginalErr: err,
				}
			}
			templatePath = p
		}
	}
	// As create's preflight: a harness-config hydration failure falls back
	// to the on-disk config, and launch then hydrates it again itself.
	if in.HarnessConfigID != "" || in.HarnessConfigHash != "" {
		if hubConn := s.resolveHubConnection(r); hubConn != nil {
			hydrateCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			p, err := s.hydrateHarnessConfig(hydrateCtx, req.Config, hubConn)
			cancel()
			if err == nil {
				hcPath = p
				pre.HarnessConfigDone = true
				pre.HarnessConfigPath = p
			} else if s.config.Debug {
				s.envSecretLog.Debug("Start env-gather: harness-config hydration failed, falling back to on-disk",
					"agent_id", in.Name, "error", err.Error())
			}
		}
	}

	res := s.evaluateEnvGather(req, templatePath, hcPath)
	unmet := res.Needs
	if in.EnvGatherFinal {
		unmet = withoutKeys(res.Needs, res.MarkerOnly)
	}
	if len(unmet) == 0 {
		return nil, pre, nil
	}
	agentID := in.AgentID
	if agentID == "" {
		agentID = in.Name
	}
	return &EnvRequirementsResponse{
		AgentID:         agentID,
		Required:        res.Required,
		HubHas:          res.HubHas,
		Needs:           res.Needs,
		SecretInfo:      res.SecretInfo,
		Alternatives:    res.Alternatives,
		EnvRequirements: true,
		MarkerOnly:      res.MarkerOnly,
	}, pre, nil
}

// withoutKeys returns keys minus drop, in order.
func withoutKeys(keys, drop []string) []string {
	if len(drop) == 0 {
		return keys
	}
	skip := make(map[string]struct{}, len(drop))
	for _, k := range drop {
		skip[k] = struct{}{}
	}
	var out []string
	for _, k := range keys {
		if _, ok := skip[k]; !ok {
			out = append(out, k)
		}
	}
	return out
}

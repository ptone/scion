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
	"errors"
	"fmt"
	"strings"

	"github.com/GoogleCloudPlatform/scion/pkg/store"
	"github.com/GoogleCloudPlatform/scion/resources"
)

// ValidateStartupDefaults checks that the hub's configured defaults resolve
// against the store and reports diagnostic errors for conditions that will
// cause silent agent-create failures.
//
// This is a pure diagnostic function: it logs errors but does not block
// startup. The hub can still serve requests — the errors guide operators to
// the configuration that needs attention.
//
// Called after BootstrapBundledResources and (when applicable) operational
// settings initialization.
func (s *Server) ValidateStartupDefaults(ctx context.Context, hostedMode bool) {
	s.validateStorageAndSeeding(ctx)
	s.validateDefaultHarnessConfig(ctx, hostedMode)
	s.validateDefaultTemplate(ctx)
}

// validateStorageAndSeeding checks for conditions that prevent bundled
// resources from being available in the store.
//
// Satisfies issue ptone/scion#1316 acceptance criteria 2: "A hub where
// seeding was skipped logs an ERROR naming which condition — GetStorage()
// == nil or SkipIfAnyExist."
func (s *Server) validateStorageAndSeeding(ctx context.Context) {
	if s.GetStorage() == nil {
		s.resourceLog.Error(
			"Startup validation: no storage backend configured. " +
				"Bundled harness-configs and templates were not seeded into the store. " +
				"Agents that rely on store-managed resources will fail. " +
				"Remedy: configure a storage backend (local filesystem or GCS bucket)",
		)
		// Do not return — the bundled-config check below queries the database
		// (store), not storage, and is independently valuable.
	}

	// Check whether expected bundled harness-configs are present in the store.
	// If other active configs exist but bundled ones are missing, the
	// SkipIfAnyExist policy suppressed seeding.
	bundledNames := resources.BuiltinHarnessConfigNames()
	if len(bundledNames) == 0 {
		return
	}

	var missing []string
	for _, name := range bundledNames {
		hc, err := s.store.GetHarnessConfigBySlug(ctx, name, store.HarnessConfigScopeGlobal, "")
		if err != nil || hc == nil {
			missing = append(missing, name)
		}
	}

	if len(missing) > 0 {
		// Check if ANY active config exists — that indicates SkipIfAnyExist fired.
		existing, err := s.store.ListHarnessConfigs(ctx, store.HarnessConfigFilter{
			Status: store.HarnessConfigStatusActive,
		}, store.ListOptions{Limit: 1})
		if err == nil && len(existing.Items) > 0 {
			s.resourceLog.Error(
				fmt.Sprintf("Startup validation: %d bundled harness-config(s) missing from "+
					"the store while other active configs exist. This indicates the "+
					"SkipIfAnyExist bootstrap policy suppressed seeding", len(missing)),
				"missing", strings.Join(missing, ", "),
				"remedy", "Re-run bootstrap without SkipIfAnyExist, or import the "+
					"missing configs manually",
			)
		}
	}
}

// validateDefaultHarnessConfig checks that the effective default_harness_config
// resolves against the store. In hosted mode, an unresolvable default causes
// every agent-create that omits an explicit harnessConfig to fail with a 502.
//
// Satisfies issue ptone/scion#1316 acceptance criteria 1: "A hub whose
// default_harness_config does not resolve logs an ERROR at startup naming
// the setting, the value, and a remedy."
func (s *Server) validateDefaultHarnessConfig(ctx context.Context, hostedMode bool) {
	defaults := s.hubAgentDefaults()
	hcName := defaults.DefaultHarnessConfig

	// Also check the default template's harness config, since
	// getHarnessConfigFromTemplate is the fallback when no agent_defaults
	// value is set.
	templateHCName := ""
	if defaults.DefaultTemplate != "" {
		tmpl, err := s.resolveTemplate(ctx, defaults.DefaultTemplate, "")
		if err == nil && tmpl != nil {
			templateHCName = s.getHarnessConfigFromTemplate(tmpl, "")
		}
	}

	effectiveName := hcName
	source := "agent_defaults.default_harness_config"
	if effectiveName == "" && templateHCName != "" {
		effectiveName = templateHCName
		source = "default template harness config"
	}

	if effectiveName == "" {
		if hostedMode {
			s.resourceLog.Error(
				"Startup validation: no default_harness_config configured. In hosted mode, " +
					"agents created without an explicit harnessConfig will fail because the " +
					"hub has no value to resolve. " +
					"Remedy: set agent_defaults.default_harness_config in operational settings, " +
					"or set default_harness_config on the default template",
			)
		}
		return
	}

	// Check if the name resolves in the global scope.
	hc, err := s.store.GetHarnessConfigBySlug(ctx, effectiveName, store.HarnessConfigScopeGlobal, "")
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		s.resourceLog.Error("Startup validation: failed to check default harness config",
			"name", effectiveName, "source", source, "error", err)
		return
	}

	if hc == nil {
		registered := s.listRegisteredHarnessConfigNames(ctx)
		s.resourceLog.Error(
			fmt.Sprintf("Startup validation: %s %q does not resolve — "+
				"no harness config with that slug exists in the store", source, effectiveName),
			"registered_configs", registered,
			"remedy", fmt.Sprintf("Either create a harness config named %q, or change "+
				"%s to one of the registered configs", effectiveName, source),
		)
		return
	}

	if hc.Status != store.HarnessConfigStatusActive {
		s.resourceLog.Error(
			fmt.Sprintf("Startup validation: %s %q exists but is not active (status: %s)",
				source, effectiveName, hc.Status),
			"remedy", fmt.Sprintf("Re-activate the harness config or change %s "+
				"to an active config", source),
		)
	}
}

// validateDefaultTemplate checks that the hub's default_template resolves.
func (s *Server) validateDefaultTemplate(ctx context.Context) {
	defaults := s.hubAgentDefaults()
	if defaults.DefaultTemplate == "" {
		return
	}

	tmpl, err := s.resolveTemplate(ctx, defaults.DefaultTemplate, "")
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		s.resourceLog.Error("Startup validation: failed to check default template",
			"name", defaults.DefaultTemplate, "error", err)
		return
	}

	if tmpl == nil {
		s.resourceLog.Error(
			fmt.Sprintf("Startup validation: default_template %q does not resolve — "+
				"no template with that slug exists in the store", defaults.DefaultTemplate),
			"source", "agent_defaults.default_template",
			"remedy", fmt.Sprintf("Either create a template named %q, or change "+
				"agent_defaults.default_template to an existing template", defaults.DefaultTemplate),
		)
	}
}

// listRegisteredHarnessConfigNames returns the slugs of all active harness
// configs in the store, for diagnostic messages.
func (s *Server) listRegisteredHarnessConfigNames(ctx context.Context) string {
	result, err := s.store.ListHarnessConfigs(ctx, store.HarnessConfigFilter{
		Status: store.HarnessConfigStatusActive,
	}, store.ListOptions{Limit: 50})
	if err != nil || len(result.Items) == 0 {
		return "(none)"
	}

	names := make([]string, 0, len(result.Items))
	for _, hc := range result.Items {
		names = append(names, hc.Slug)
	}
	return strings.Join(names, ", ")
}

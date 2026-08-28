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

//go:build !no_sqlite

package hub

// Blast-radius measurement for Fix B (tasks #37/#48):
//   Add default_harness_config: antigravity to the default template.
//
// Each test maps to a row in the brief's measurement table. The tests are
// written BEFORE the template edit, to pin the current behaviour, then
// re-verified after the edit to confirm the predicted change.
//
// Row 4 is the WITHDRAWAL CONDITION. If it changes, Fix B must not ship.

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/GoogleCloudPlatform/scion/pkg/agent/state"
	"github.com/GoogleCloudPlatform/scion/pkg/api"
	"github.com/GoogleCloudPlatform/scion/pkg/config"
	"github.com/GoogleCloudPlatform/scion/pkg/config/opsettings"
	"github.com/GoogleCloudPlatform/scion/pkg/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Row 1: Hosted/SQLite, fresh deploy, default template, no overrides
// Today: empty name → no ID/hash → broker invents name → 502
// ---------------------------------------------------------------------------

// TestBlastRadius_Row1_DefaultTemplateNoOverrides_EmptyHarnessConfig pins the
// current defect: the default template supplies no harness-config name, so the
// hub dispatches the agent with HarnessConfig="", HarnessConfigID="", and
// HarnessConfigHash="". A broker in hosted mode then invents "antigravity"
// from settings but cannot find it on disk → 502.
//
// This test measures the HUB side only (we cannot test the broker 502 without
// a running broker). The assertion is that HarnessConfig is empty and no
// ID/hash is stamped — the precondition for the 502.
func TestBlastRadius_Row1_DefaultTemplateNoOverrides_EmptyHarnessConfig(t *testing.T) {
	disp := &createAgentDispatcher{createPhase: string(state.PhaseRunning)}
	srv, s, project := setupCreateAgentServer(t, disp)
	ctx := context.Background()

	// Create a "default" template with NO DefaultHarnessConfig — the current state.
	defaultTmpl := &store.Template{
		ID:          tid("tmpl-default-row1-" + t.Name()),
		Name:        "default",
		Slug:        "default",
		Harness:     "",
		ContentHash: "d00d",
		Scope:       store.TemplateScopeGlobal,
		Status:      "active",
	}
	require.NoError(t, s.CreateTemplate(ctx, defaultTmpl))

	rec := doRequest(t, srv, http.MethodPost, "/api/v1/agents", CreateAgentRequest{
		Name:      "row1-empty-hc",
		ProjectID: project.ID,
	})
	require.Equal(t, http.StatusCreated, rec.Code, "body: %s", rec.Body.String())

	var resp CreateAgentResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))

	agent, err := s.GetAgent(ctx, resp.Agent.ID)
	require.NoError(t, err)
	require.NotNil(t, agent.AppliedConfig)

	assert.Empty(t, agent.AppliedConfig.HarnessConfig,
		"Row 1 today: default template has no harness config, so the hub sends empty")
	assert.Empty(t, agent.AppliedConfig.HarnessConfigID,
		"Row 1 today: no ID is stamped — broker must invent a name, fails in hosted mode")
	assert.Empty(t, agent.AppliedConfig.HarnessConfigHash,
		"Row 1 today: no hash is stamped")
}

// TestBlastRadius_Row1_AfterFixB_DefaultTemplateSuppliesAntigravity simulates
// Fix B: the default template has default_harness_config: antigravity. The hub
// should resolve it and stamp ID/hash when the config exists in the store.
func TestBlastRadius_Row1_AfterFixB_DefaultTemplateSuppliesAntigravity(t *testing.T) {
	disp := &createAgentDispatcher{createPhase: string(state.PhaseRunning)}
	srv, s, project := setupCreateAgentServer(t, disp)
	ctx := context.Background()

	// Simulate Fix B: default template WITH DefaultHarnessConfig = "antigravity"
	defaultTmpl := &store.Template{
		ID:                   tid("tmpl-default-row1b-" + t.Name()),
		Name:                 "default",
		Slug:                 "default",
		Harness:              "",
		DefaultHarnessConfig: "antigravity",
		ContentHash:          "d00d",
		Scope:                store.TemplateScopeGlobal,
		Status:               "active",
	}
	require.NoError(t, s.CreateTemplate(ctx, defaultTmpl))

	// Seed "antigravity" in the store (as BootstrapBundledResources would on a fresh deploy)
	hc := &store.HarnessConfig{
		ID:          tid("hc-antigravity-" + t.Name()),
		Name:        "antigravity",
		Slug:        "antigravity",
		Harness:     "claude",
		ContentHash: "cafebabe",
		Scope:       store.HarnessConfigScopeGlobal,
	}
	require.NoError(t, s.CreateHarnessConfig(ctx, hc))

	rec := doRequest(t, srv, http.MethodPost, "/api/v1/agents", CreateAgentRequest{
		Name:      "row1b-antigravity",
		ProjectID: project.ID,
	})
	require.Equal(t, http.StatusCreated, rec.Code, "body: %s", rec.Body.String())

	var resp CreateAgentResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))

	agent, err := s.GetAgent(ctx, resp.Agent.ID)
	require.NoError(t, err)
	require.NotNil(t, agent.AppliedConfig)

	assert.Equal(t, "antigravity", agent.AppliedConfig.HarnessConfig,
		"Row 1 after Fix B: template supplies the name")
	assert.Equal(t, hc.ID, agent.AppliedConfig.HarnessConfigID,
		"Row 1 after Fix B: ID is stamped — broker can hydrate from store")
	assert.Equal(t, "cafebabe", agent.AppliedConfig.HarnessConfigHash,
		"Row 1 after Fix B: hash is stamped")
}

// ---------------------------------------------------------------------------
// Row 2: Request explicitly sets harnessConfig → request wins
// ---------------------------------------------------------------------------

// TestBlastRadius_Row2_RequestExplicitHarnessConfig_WinsOverTemplate pins that
// an explicit request harnessConfig outranks the template, both today and after
// Fix B.
func TestBlastRadius_Row2_RequestExplicitHarnessConfig_WinsOverTemplate(t *testing.T) {
	disp := &createAgentDispatcher{createPhase: string(state.PhaseRunning)}
	srv, s, project := setupCreateAgentServer(t, disp)
	ctx := context.Background()

	// Simulate Fix B: template has DefaultHarnessConfig
	defaultTmpl := &store.Template{
		ID:                   tid("tmpl-default-row2-" + t.Name()),
		Name:                 "default",
		Slug:                 "default",
		DefaultHarnessConfig: "antigravity",
		ContentHash:          "d00d",
		Scope:                store.TemplateScopeGlobal,
		Status:               "active",
	}
	require.NoError(t, s.CreateTemplate(ctx, defaultTmpl))

	rec := doRequest(t, srv, http.MethodPost, "/api/v1/agents", CreateAgentRequest{
		Name:          "row2-request-wins",
		ProjectID:     project.ID,
		HarnessConfig: "my-explicit-config",
	})
	require.Equal(t, http.StatusCreated, rec.Code, "body: %s", rec.Body.String())

	var resp CreateAgentResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))

	agent, err := s.GetAgent(ctx, resp.Agent.ID)
	require.NoError(t, err)
	require.NotNil(t, agent.AppliedConfig)

	assert.Equal(t, "my-explicit-config", agent.AppliedConfig.HarnessConfig,
		"Row 2: explicit request harnessConfig must outrank the template — both before and after Fix B")
}

// ---------------------------------------------------------------------------
// Row 3: Project annotation sets default-harness-config → annotation wins
// ---------------------------------------------------------------------------

// TestBlastRadius_Row3_ProjectAnnotation_WinsOverTemplate pins that the project
// annotation outranks the template, both today and after Fix B.
func TestBlastRadius_Row3_ProjectAnnotation_WinsOverTemplate(t *testing.T) {
	disp := &createAgentDispatcher{createPhase: string(state.PhaseRunning)}
	srv, s, project := setupCreateAgentServer(t, disp)
	ctx := context.Background()

	// Simulate Fix B: template has DefaultHarnessConfig
	defaultTmpl := &store.Template{
		ID:                   tid("tmpl-default-row3-" + t.Name()),
		Name:                 "default",
		Slug:                 "default",
		DefaultHarnessConfig: "antigravity",
		ContentHash:          "d00d",
		Scope:                store.TemplateScopeGlobal,
		Status:               "active",
	}
	require.NoError(t, s.CreateTemplate(ctx, defaultTmpl))

	setProjectHarnessConfigAnnotation(t, s, project, "project-harness")

	rec := doRequest(t, srv, http.MethodPost, "/api/v1/agents", CreateAgentRequest{
		Name:      "row3-annotation-wins",
		ProjectID: project.ID,
	})
	require.Equal(t, http.StatusCreated, rec.Code, "body: %s", rec.Body.String())

	var resp CreateAgentResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))

	agent, err := s.GetAgent(ctx, resp.Agent.ID)
	require.NoError(t, err)
	require.NotNil(t, agent.AppliedConfig)

	assert.Equal(t, "project-harness", agent.AppliedConfig.HarnessConfig,
		"Row 3: project annotation must outrank the template — both before and after Fix B")
}

// ---------------------------------------------------------------------------
// Row 4: WITHDRAWAL CONDITION
// Broker profile sets default_harness_config (rank 6), default template,
// workstation.
//
// Today: profile wins (hub sends empty, broker's rung 6 fires)
// After Fix B: template wins (hub sends "antigravity" at CLIFlag rank,
//              OR broker rung 3 fires from TemplateCfg) — profile loses
// ---------------------------------------------------------------------------

// TestBlastRadius_Row4_BrokerProfileDefault_Today_ProfileWins measures the
// current behaviour on the BROKER side: when the hub sends an empty
// HarnessConfig, the broker's ResolveHarnessConfigName falls through to
// rung 6 (profile default_harness_config) and the profile wins.
func TestBlastRadius_Row4_BrokerProfileDefault_Today_ProfileWins(t *testing.T) {
	// Simulate today: hub sends empty HarnessConfig (CLIFlag="")
	// Template config has no DefaultHarnessConfig (today's default template)
	// Profile has default_harness_config set
	res, err := config.ResolveHarnessConfigName(config.HarnessConfigInputs{
		CLIFlag:     "", // hub sends empty today
		TemplateCfg: nil,
		ProfileName: "my-profile",
		Settings: &config.VersionedSettings{
			Profiles: map[string]config.V1ProfileConfig{
				"my-profile": {DefaultHarnessConfig: "profile-custom-config"},
			},
			DefaultHarnessConfig: "antigravity",
		},
	})
	require.NoError(t, err)

	assert.Equal(t, "profile-custom-config", res.Name,
		"Row 4 today: profile's default_harness_config wins at rung 6")
	assert.Equal(t, "profile-my-profile", res.Source,
		"Row 4 today: source must be the profile, not settings-default")
}

// TestBlastRadius_Row4_WITHDRAWAL_AfterFixB_TemplateOutranksProfile is the
// measurement that triggers the withdrawal condition. After Fix B, the
// template's default_harness_config enters at rung 3 (template-default) on the
// broker, which is ABOVE rung 6 (profile). On the hub-mediated path, it enters
// at rung 1 (CLIFlag). Either way, the profile loses.
//
// This test MUST go red (i.e. profile no longer wins) for Fix B to be the
// withdrawal condition.
func TestBlastRadius_Row4_WITHDRAWAL_AfterFixB_TemplateOutranksProfile(t *testing.T) {
	t.Run("BrokerDirectPath_Rung3BeatsRung6", func(t *testing.T) {
		// Pure local path: template config has DefaultHarnessConfig (Fix B)
		// This enters at rung 3 (template-default), above profile at rung 6.
		res, err := config.ResolveHarnessConfigName(config.HarnessConfigInputs{
			CLIFlag: "", // no explicit CLI flag
			TemplateCfg: &api.ScionConfig{
				DefaultHarnessConfig: "antigravity",
			},
			ProfileName: "my-profile",
			Settings: &config.VersionedSettings{
				Profiles: map[string]config.V1ProfileConfig{
					"my-profile": {DefaultHarnessConfig: "profile-custom-config"},
				},
				DefaultHarnessConfig: "antigravity",
			},
		})
		require.NoError(t, err)

		// THIS IS THE WITHDRAWAL: the template wins, the profile loses.
		assert.Equal(t, "antigravity", res.Name,
			"Row 4 WITHDRAWAL: template's default_harness_config at rung 3 outranks profile at rung 6")
		assert.Equal(t, "template-default", res.Source,
			"Row 4 WITHDRAWAL: source is template-default (rung 3), not profile (rung 6)")
	})

	t.Run("HubMediatedPath_Rung1BeatsRung6", func(t *testing.T) {
		// Hub-mediated path: hub resolves "antigravity" from the template and
		// sends it as Config.HarnessConfig. The broker receives it as CLIFlag.
		res, err := config.ResolveHarnessConfigName(config.HarnessConfigInputs{
			CLIFlag: "antigravity", // hub sent this from the template
			TemplateCfg: &api.ScionConfig{
				DefaultHarnessConfig: "antigravity",
			},
			ProfileName: "my-profile",
			Settings: &config.VersionedSettings{
				Profiles: map[string]config.V1ProfileConfig{
					"my-profile": {DefaultHarnessConfig: "profile-custom-config"},
				},
				DefaultHarnessConfig: "antigravity",
			},
		})
		require.NoError(t, err)

		// THIS IS THE WITHDRAWAL: the hub-supplied value wins, the profile loses.
		assert.Equal(t, "antigravity", res.Name,
			"Row 4 WITHDRAWAL: hub-supplied CLIFlag at rung 1 outranks profile at rung 6")
		assert.Equal(t, "cli-flag", res.Source,
			"Row 4 WITHDRAWAL: source is cli-flag (rung 1), not profile (rung 6)")
	})
}

// TestBlastRadius_Row4_HubSide_TemplateValueReachesAppliedConfig confirms
// that on the hub side, a template with DefaultHarnessConfig stamps the value
// into AppliedConfig.HarnessConfig, which is then sent to the broker as
// Config.HarnessConfig (CLIFlag rank). This is the HUB-SIDE proof that Fix B
// would send a non-empty value to the broker.
func TestBlastRadius_Row4_HubSide_TemplateValueReachesAppliedConfig(t *testing.T) {
	disp := &capturingDispatcher{createAgentDispatcher: createAgentDispatcher{createPhase: string(state.PhaseRunning)}}
	srv, s, project := setupCreateAgentServer(t, disp)
	ctx := context.Background()

	// Simulate Fix B: default template with DefaultHarnessConfig
	defaultTmpl := &store.Template{
		ID:                   tid("tmpl-default-row4-" + t.Name()),
		Name:                 "default",
		Slug:                 "default",
		DefaultHarnessConfig: "antigravity",
		ContentHash:          "d00d",
		Scope:                store.TemplateScopeGlobal,
		Status:               "active",
	}
	require.NoError(t, s.CreateTemplate(ctx, defaultTmpl))

	// File mode: no hub agent defaults (workstation)
	require.Equal(t, opsettings.AgentDefaultsSettings{}, srv.hubAgentDefaults(),
		"precondition: file-mode hub has no agent defaults")

	rec := doRequest(t, srv, http.MethodPost, "/api/v1/agents", CreateAgentRequest{
		Name:      "row4-hub-side",
		ProjectID: project.ID,
	})
	require.Equal(t, http.StatusCreated, rec.Code, "body: %s", rec.Body.String())

	assert.True(t, disp.dispatched, "agent should have been dispatched")
	assert.Equal(t, "antigravity", disp.dispatchedHarnessConfig,
		"Row 4 hub side: the template's DefaultHarnessConfig reaches the dispatcher, "+
			"which means it reaches the broker as Config.HarnessConfig (CLIFlag rank)")
}

// ---------------------------------------------------------------------------
// Row 5: Workstation/docker, default template, no overrides
// Today: broker rung 7 → "antigravity" from settings
// After Fix B: "antigravity" (same name, higher rank — no behavioural change)
// ---------------------------------------------------------------------------

// TestBlastRadius_Row5_NoOverrides_SettingsDefault_Today pins the current
// workstation behaviour: with no explicit values, the broker resolves
// "antigravity" from settings.DefaultHarnessConfig at rung 7.
func TestBlastRadius_Row5_NoOverrides_SettingsDefault_Today(t *testing.T) {
	res, err := config.ResolveHarnessConfigName(config.HarnessConfigInputs{
		CLIFlag:     "",  // hub sends empty today
		TemplateCfg: nil, // default template has no harness config today
		Settings: &config.VersionedSettings{
			DefaultHarnessConfig: "antigravity",
		},
	})
	require.NoError(t, err)

	assert.Equal(t, "antigravity", res.Name,
		"Row 5 today: settings-default provides antigravity at rung 7")
	assert.Equal(t, "settings-default", res.Source)
}

// TestBlastRadius_Row5_AfterFixB_SameNameHigherRank shows that after Fix B,
// the same name resolves but at a higher rank. The net effect is the same for
// users with no profile or request overrides — antigravity is used either way.
func TestBlastRadius_Row5_AfterFixB_SameNameHigherRank(t *testing.T) {
	t.Run("BrokerDirect_Rung3", func(t *testing.T) {
		res, err := config.ResolveHarnessConfigName(config.HarnessConfigInputs{
			CLIFlag: "",
			TemplateCfg: &api.ScionConfig{
				DefaultHarnessConfig: "antigravity",
			},
			Settings: &config.VersionedSettings{
				DefaultHarnessConfig: "antigravity",
			},
		})
		require.NoError(t, err)

		assert.Equal(t, "antigravity", res.Name,
			"Row 5 after Fix B: same name, just at rung 3 instead of rung 7")
		assert.Equal(t, "template-default", res.Source,
			"Row 5 after Fix B: source shifts from settings-default to template-default")
	})

	t.Run("HubMediated_Rung1", func(t *testing.T) {
		res, err := config.ResolveHarnessConfigName(config.HarnessConfigInputs{
			CLIFlag: "antigravity", // hub sends this from template
			Settings: &config.VersionedSettings{
				DefaultHarnessConfig: "antigravity",
			},
		})
		require.NoError(t, err)

		assert.Equal(t, "antigravity", res.Name,
			"Row 5 after Fix B (hub): same name at rung 1")
		assert.Equal(t, "cli-flag", res.Source)
	})
}

// ---------------------------------------------------------------------------
// Row 6: Non-default template that names its own config → its own
// ---------------------------------------------------------------------------

// TestBlastRadius_Row6_NonDefaultTemplate_OwnConfigWins pins that a non-default
// template with its own DefaultHarnessConfig is unaffected by Fix B.
func TestBlastRadius_Row6_NonDefaultTemplate_OwnConfigWins(t *testing.T) {
	disp := &createAgentDispatcher{createPhase: string(state.PhaseRunning)}
	srv, s, project := setupCreateAgentServer(t, disp)
	ctx := context.Background()

	// Create the "default" template with antigravity (Fix B)
	defaultTmpl := &store.Template{
		ID:                   tid("tmpl-default-row6-" + t.Name()),
		Name:                 "default",
		Slug:                 "default",
		DefaultHarnessConfig: "antigravity",
		ContentHash:          "d00d",
		Scope:                store.TemplateScopeGlobal,
		Status:               "active",
	}
	require.NoError(t, s.CreateTemplate(ctx, defaultTmpl))

	// Create a non-default template with its own config
	webDevTmpl := &store.Template{
		ID:                   tid("tmpl-webdev-row6-" + t.Name()),
		Name:                 "web-dev",
		Slug:                 "web-dev",
		Harness:              "claude",
		DefaultHarnessConfig: "claude-web",
		ContentHash:          "f00d",
		Scope:                store.TemplateScopeGlobal,
		Status:               "active",
	}
	require.NoError(t, s.CreateTemplate(ctx, webDevTmpl))

	rec := doRequest(t, srv, http.MethodPost, "/api/v1/agents", CreateAgentRequest{
		Name:      "row6-own-config",
		ProjectID: project.ID,
		Template:  "web-dev",
	})
	require.Equal(t, http.StatusCreated, rec.Code, "body: %s", rec.Body.String())

	var resp CreateAgentResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))

	agent, err := s.GetAgent(ctx, resp.Agent.ID)
	require.NoError(t, err)
	require.NotNil(t, agent.AppliedConfig)

	assert.Equal(t, "claude-web", agent.AppliedConfig.HarnessConfig,
		"Row 6: non-default template's own config is unchanged — no cross-contamination")
}

// ---------------------------------------------------------------------------
// Row 7: Non-fresh hosted store WITHOUT antigravity (SkipIfAnyExist tripped)
// ---------------------------------------------------------------------------

// TestBlastRadius_Row7_NonFreshStore_MissingAntigravity_NoIDStamped pins the
// residual: after Fix B, the template names "antigravity" but if the store
// doesn't have it (SkipIfAnyExist tripped during bootstrap), the hub cannot
// stamp an ID/hash. The broker then tries to resolve it locally, fails in
// hosted mode → 502.
func TestBlastRadius_Row7_NonFreshStore_MissingAntigravity_NoIDStamped(t *testing.T) {
	disp := &createAgentDispatcher{createPhase: string(state.PhaseRunning)}
	srv, s, project := setupCreateAgentServer(t, disp)
	logs := captureHarnessLogs(srv)
	ctx := context.Background()

	// Simulate Fix B: default template with DefaultHarnessConfig
	defaultTmpl := &store.Template{
		ID:                   tid("tmpl-default-row7-" + t.Name()),
		Name:                 "default",
		Slug:                 "default",
		DefaultHarnessConfig: "antigravity",
		ContentHash:          "d00d",
		Scope:                store.TemplateScopeGlobal,
		Status:               "active",
	}
	require.NoError(t, s.CreateTemplate(ctx, defaultTmpl))

	// Deliberately do NOT seed "antigravity" in the store — simulating
	// SkipIfAnyExist having tripped during bootstrap.

	rec := doRequest(t, srv, http.MethodPost, "/api/v1/agents", CreateAgentRequest{
		Name:      "row7-missing-antigravity",
		ProjectID: project.ID,
	})
	require.Equal(t, http.StatusCreated, rec.Code, "body: %s", rec.Body.String())

	var resp CreateAgentResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))

	agent, err := s.GetAgent(ctx, resp.Agent.ID)
	require.NoError(t, err)
	require.NotNil(t, agent.AppliedConfig)

	assert.Equal(t, "antigravity", agent.AppliedConfig.HarnessConfig,
		"Row 7: the name is set from the template")
	assert.Empty(t, agent.AppliedConfig.HarnessConfigID,
		"Row 7 residual: no ID stamped — config not in store (SkipIfAnyExist)")
	assert.Empty(t, agent.AppliedConfig.HarnessConfigHash,
		"Row 7 residual: no hash stamped")

	// The not-found should be logged at DEBUG (template-tier name, not operator-supplied)
	found := logs.harnessNotFoundRecords()
	require.NotEmpty(t, found, "the harness config not-found should be logged")
}

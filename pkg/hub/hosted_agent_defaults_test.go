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

// Phase 3 of ptone/scion#1316: hosted-mode agent defaults from embedded
// default_settings.yaml. These tests verify that the hub stamps
// HarnessConfigID and HarnessConfigHash when the embedded defaults are
// active (hosted mode) and that workstation mode is unaffected (AC 6).

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/GoogleCloudPlatform/scion/pkg/config"
	"github.com/GoogleCloudPlatform/scion/pkg/config/opsettings"
	"github.com/GoogleCloudPlatform/scion/pkg/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// seedHarnessConfigWithHash creates a harness config in the store and returns
// it so tests can assert on the ID and hash. Unlike seedHarnessConfig (which
// returns nothing), this gives the caller the stamped values.
func seedHarnessConfigWithHash(t *testing.T, s store.Store, slug string) *store.HarnessConfig {
	t.Helper()
	hc := &store.HarnessConfig{
		ID:          tid("hc-" + slug),
		Name:        slug,
		Slug:        slug,
		Harness:     "claude",
		ContentHash: "embedded-" + slug,
		Scope:       store.HarnessConfigScopeGlobal,
		Status:      store.HarnessConfigStatusActive,
	}
	require.NoError(t, s.CreateHarnessConfig(context.Background(), hc))
	return hc
}

// ---------------------------------------------------------------------------
// AC 6: Workstation mode is unaffected
// ---------------------------------------------------------------------------

// TestCreateAgent_WorkstationMode_AgentDefaultsStayZero verifies that in
// workstation mode (file/SQLite, non-hosted), AgentDefaults remains zero
// even though the embedded default_settings.yaml defines default_template
// and default_harness_config. The co-located broker resolves defaults
// through its own settings.yaml chain, and promoting them to the hub tier
// would outrank the user's profile/settings defaults (design §3.2.4).
//
// This is the explicit workstation no-op required by AC 6. The production
// gating is in initHubServer (server_foreground.go): the if-hostedMode
// branch sets AgentDefaults only when hosted. This test verifies the
// server-side contract: when AgentDefaults is zero (the workstation state),
// no default harness config is stamped.
func TestCreateAgent_WorkstationMode_AgentDefaultsStayZero(t *testing.T) {
	// Confirm the embedded file has values — the test proves that the hub
	// ignores them in workstation mode, not that they are absent.
	embTmpl, embHC := config.EmbeddedAgentDefaults()
	require.NotEmpty(t, embTmpl, "embedded default_template must be non-empty for this test to be meaningful")
	require.NotEmpty(t, embHC, "embedded default_harness_config must be non-empty for this test to be meaningful")

	disp := &createAgentDispatcher{createPhase: "running"}
	srv, s, project := setupCreateAgentServer(t, disp)
	ctx := context.Background()

	// Seed the harness config so resolution CAN succeed — we want to prove
	// that in workstation mode the hub doesn't try, not that it fails.
	seedHarnessConfigWithHash(t, s, embHC)

	// Workstation state: AgentDefaults is zero (setupCreateAgentServer
	// never sets it — that is the file-mode-parity contract).
	require.Equal(t, opsettings.AgentDefaultsSettings{}, srv.hubAgentDefaults(),
		"workstation precondition: AgentDefaults must be zero")

	rec := doRequest(t, srv, http.MethodPost, "/api/v1/agents", CreateAgentRequest{
		Name:      "workstation-agent",
		ProjectID: project.ID,
		Task:      "do something",
	})
	require.Equal(t, http.StatusCreated, rec.Code, "body: %s", rec.Body.String())

	var resp CreateAgentResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	agent, err := s.GetAgent(ctx, resp.Agent.ID)
	require.NoError(t, err)
	require.NotNil(t, agent.AppliedConfig)
	assert.Empty(t, agent.AppliedConfig.HarnessConfig,
		"workstation mode: hub must NOT stamp harness config from embedded defaults")
	assert.Empty(t, agent.AppliedConfig.HarnessConfigID,
		"workstation mode: hub must NOT stamp harness config ID")
	assert.Empty(t, agent.AppliedConfig.HarnessConfigHash,
		"workstation mode: hub must NOT stamp harness config hash")
}

// ---------------------------------------------------------------------------
// AC 3 + 5: Hosted mode populates defaults from embedded settings
// ---------------------------------------------------------------------------

// TestCreateAgent_HostedMode_EmbeddedDefaultsStampIDHash verifies that when
// AgentDefaults is seeded from the embedded default_settings.yaml (the
// hosted-mode initialization path), the hub resolves the default harness
// config, stamps HarnessConfigID and HarnessConfigHash on the agent record,
// and the dispatched agent carries the values the broker needs for hydration.
//
// This is the primary fix for ptone/scion#1316 fault 1: without this seed,
// the hub creates agents with an empty harness-config name, and the broker's
// resourceObjectPath call fails with 404 → 500.
func TestCreateAgent_HostedMode_EmbeddedDefaultsStampIDHash(t *testing.T) {
	embTmpl, embHC := config.EmbeddedAgentDefaults()
	require.NotEmpty(t, embTmpl)
	require.NotEmpty(t, embHC)

	disp := &createAgentDispatcher{createPhase: "running"}
	srv, s, project := setupCreateAgentServer(t, disp)
	ctx := context.Background()

	// Seed the harness config so slug resolution can succeed.
	hc := seedHarnessConfigWithHash(t, s, embHC)

	// Simulate hosted-mode initialization: seed AgentDefaults from embedded
	// defaults. In production this is done by initHubServer's hostedMode
	// branch; here we call setHubAgentDefaults to inject the same values.
	setHubAgentDefaults(srv, opsettings.AgentDefaultsSettings{
		DefaultTemplate:      embTmpl,
		DefaultHarnessConfig: embHC,
	})

	rec := doRequest(t, srv, http.MethodPost, "/api/v1/agents", CreateAgentRequest{
		Name:      "hosted-mode-agent",
		ProjectID: project.ID,
		Task:      "do something",
	})
	require.Equal(t, http.StatusCreated, rec.Code, "body: %s", rec.Body.String())

	var resp CreateAgentResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	agent, err := s.GetAgent(ctx, resp.Agent.ID)
	require.NoError(t, err)
	require.NotNil(t, agent.AppliedConfig)
	assert.Equal(t, embHC, agent.AppliedConfig.HarnessConfig,
		"hosted mode: hub must stamp harness config name from embedded defaults")
	assert.Equal(t, hc.ID, agent.AppliedConfig.HarnessConfigID,
		"hosted mode: hub must stamp harness config ID so broker can hydrate")
	assert.NotEmpty(t, agent.AppliedConfig.HarnessConfigHash,
		"hosted mode: hub must stamp harness config hash so broker can hydrate")
}

// ---------------------------------------------------------------------------
// AC 7: Hub/broker seam — the empty-identity case
// ---------------------------------------------------------------------------

// TestCreateAgent_EmptyIdentity_NothingToHydrate is AC 7: the hub/broker
// seam test for the empty-identity case. No test like this existed before
// ptone/scion#1316, and its absence let the defect survive #1304, #1305,
// and four investigations.
//
// The scenario: a hosted-mode hub creates an agent with no explicit harness
// config AND no hub defaults (the bug state). The hub cannot resolve a name
// it does not have, so it stamps no HarnessConfigID/Hash. When the broker
// calls hydrateHarnessConfig, the empty-identity guard returns ("", nil),
// and the broker falls back to its own on-disk search — which on a hosted
// tier with no materialized configs fails. The 500 that ptone/scion#1316
// reports is the downstream consequence.
//
// This test pins the cause at the hub boundary: without defaults, the agent
// record carries empty harness-config fields. With defaults (the fix), it
// carries populated fields. The asymmetry between the two rows IS the bug.
func TestCreateAgent_EmptyIdentity_NothingToHydrate(t *testing.T) {
	embTmpl, embHC := config.EmbeddedAgentDefaults()
	require.NotEmpty(t, embTmpl)
	require.NotEmpty(t, embHC)

	disp := &createAgentDispatcher{createPhase: "running"}
	srv, s, project := setupCreateAgentServer(t, disp)
	ctx := context.Background()

	// Seed the harness config in the store so it CAN be resolved.
	hc := seedHarnessConfigWithHash(t, s, embHC)

	// --- Row 1: Bug state (no hub defaults) ---
	// This is what the hosted tier looked like before the fix.
	require.Equal(t, opsettings.AgentDefaultsSettings{}, srv.hubAgentDefaults(),
		"bug-state precondition: no hub defaults")

	rec := doRequest(t, srv, http.MethodPost, "/api/v1/agents", CreateAgentRequest{
		Name:      "bug-state-agent",
		ProjectID: project.ID,
		Task:      "do something",
	})
	require.Equal(t, http.StatusCreated, rec.Code, "body: %s", rec.Body.String())

	var bugResp CreateAgentResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &bugResp))
	bugAgent, err := s.GetAgent(ctx, bugResp.Agent.ID)
	require.NoError(t, err)
	require.NotNil(t, bugAgent.AppliedConfig)

	// The agent has no harness config identity — nothing for the broker to
	// hydrate. This is the root cause of the 502/500 the broker reports.
	assert.Empty(t, bugAgent.AppliedConfig.HarnessConfig,
		"bug state: no default → no harness config name")
	assert.Empty(t, bugAgent.AppliedConfig.HarnessConfigID,
		"bug state: no default → no harness config ID for broker hydration")

	// --- Row 2: Fixed state (hub defaults from embedded settings) ---
	setHubAgentDefaults(srv, opsettings.AgentDefaultsSettings{
		DefaultTemplate:      embTmpl,
		DefaultHarnessConfig: embHC,
	})

	rec = doRequest(t, srv, http.MethodPost, "/api/v1/agents", CreateAgentRequest{
		Name:      "fixed-state-agent",
		ProjectID: project.ID,
		Task:      "do something",
	})
	require.Equal(t, http.StatusCreated, rec.Code, "body: %s", rec.Body.String())

	var fixedResp CreateAgentResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &fixedResp))
	fixedAgent, err := s.GetAgent(ctx, fixedResp.Agent.ID)
	require.NoError(t, err)
	require.NotNil(t, fixedAgent.AppliedConfig)

	// The agent now carries harness config identity — the broker can
	// hydrate it via resourceObjectPath → GET /api/v1/harness-configs/{id}.
	assert.Equal(t, embHC, fixedAgent.AppliedConfig.HarnessConfig,
		"fixed state: embedded default → harness config name stamped")
	assert.Equal(t, hc.ID, fixedAgent.AppliedConfig.HarnessConfigID,
		"fixed state: embedded default → harness config ID stamped for broker hydration")
	assert.NotEmpty(t, fixedAgent.AppliedConfig.HarnessConfigHash,
		"fixed state: embedded default → harness config hash stamped")
}

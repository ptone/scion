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

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/GoogleCloudPlatform/scion/pkg/agent/state"
	"github.com/GoogleCloudPlatform/scion/pkg/api"
	"github.com/GoogleCloudPlatform/scion/pkg/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Tests for ptone/scion#5605 on the hub: a change of a count limit to
// Unlimited (0) is accepted only for an agent whose broker advertises
// unlimitedCountLimits, so a disposition lists it as applied (or held) only
// when the next start applies it, on the live and the held path alike.

// setBrokerCapabilities replaces the broker's capabilities.
func setBrokerCapabilities(t *testing.T, s store.Store, brokerID string, caps *store.BrokerCapabilities) {
	t.Helper()
	ctx := context.Background()
	b, err := s.GetRuntimeBroker(ctx, brokerID)
	require.NoError(t, err)
	b.Capabilities = caps
	require.NoError(t, s.UpdateRuntimeBroker(ctx, b))
}

// TestAgentConfigPatch_UnlimitedCountByBroker pins the PATCH outcome of
// {"max_turns":0} on a stopped agent (live path) for each broker capability
// set, with the recorded cleared_fields, and checks the start dispatches
// what the disposition promised.
func TestAgentConfigPatch_UnlimitedCountByBroker(t *testing.T) {
	for _, tc := range []struct {
		name    string
		caps    *store.BrokerCapabilities
		applied bool
	}{
		{name: "no capabilities", caps: &store.BrokerCapabilities{Reprovision: true}},
		{name: "clearedFields", caps: &store.BrokerCapabilities{Reprovision: true, ClearedFields: true}},
		{name: "unlimitedCountLimits", caps: &store.BrokerCapabilities{Reprovision: true, ClearedFields: true, UnlimitedCountLimits: true}, applied: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			disp := &inlineCaptureDispatcher{reincarnateTestDispatcher: newReincarnateTestDispatcher()}
			srv, s, project, broker := setupReincarnateTestServer(t, disp)
			setBrokerCapabilities(t, s, broker.ID, tc.caps)
			agent := newEditTestAgent(t, s, project, broker, state.PhaseStopped)

			resp, code, body := patchAgentBody(t, srv, agent.ID, map[string]interface{}{
				"config": map[string]interface{}{"max_turns": 0},
			})
			stored := pendingReloadAgent(t, s, agent.ID)
			if !tc.applied {
				require.Equal(t, http.StatusUnprocessableEntity, code, body)
				assert.Contains(t, body, "unlimited_not_supported_by_broker")
				assert.Contains(t, body, "config.max_turns")
				assert.Equal(t, api.IntPtr(3), stored.AppliedConfig.InlineConfig.MaxTurns, "a refused PATCH writes nothing")
				assert.Equal(t, agent.StateVersion, stored.StateVersion)
				return
			}
			require.Equal(t, http.StatusOK, code, body)
			assert.Equal(t, []string{"config.max_turns"}, resp.Disposition.Applied)
			assert.Empty(t, resp.Disposition.HeldForReincarnate)
			assert.Empty(t, resp.Warnings)
			assert.Equal(t, api.IntPtr(0), stored.AppliedConfig.InlineConfig.MaxTurns)
			assert.NotContains(t, stored.AppliedConfig.InlineConfig.ClearedFields, "max_turns", "Unlimited is a value, not a clear")
			require.NotNil(t, stored.AppliedConfig.CreateInputs)
			require.NotNil(t, stored.AppliedConfig.CreateInputs.InlineConfig)
			assert.Equal(t, api.IntPtr(0), stored.AppliedConfig.CreateInputs.InlineConfig.MaxTurns, "a reincarnation replays Unlimited")

			// Resending the stored Unlimited is an echo: no change, no list.
			resp, code, body = patchAgentBody(t, srv, agent.ID, map[string]interface{}{
				"config": map[string]interface{}{"max_turns": 0},
			})
			require.Equal(t, http.StatusOK, code, body)
			assert.Empty(t, resp.Disposition.Applied)

			rec := doRequest(t, srv, http.MethodPost, "/api/v1/agents/"+agent.ID+"/start", nil)
			require.Less(t, rec.Code, 300, rec.Body.String())
			starts := disp.snapshot()
			require.Len(t, starts, 1)
			assert.Equal(t, api.IntPtr(0), starts[0].inline.MaxTurns, "the start dispatches Unlimited")
			assert.NotContains(t, starts[0].inline.ClearedFields, "max_turns")
		})
	}

	t.Run("a null clear keeps its meaning", func(t *testing.T) {
		disp := &inlineCaptureDispatcher{reincarnateTestDispatcher: newReincarnateTestDispatcher()}
		srv, s, project, broker := setupReincarnateTestServer(t, disp)
		setBrokerCapabilities(t, s, broker.ID, &store.BrokerCapabilities{Reprovision: true, ClearedFields: true})
		agent := newEditTestAgent(t, s, project, broker, state.PhaseStopped)
		resp, code, body := patchAgentBody(t, srv, agent.ID, map[string]interface{}{
			"config": map[string]interface{}{"max_turns": nil},
		})
		require.Equal(t, http.StatusOK, code, body)
		assert.Equal(t, []string{"config.max_turns"}, resp.Disposition.Applied)
		stored := pendingReloadAgent(t, s, agent.ID)
		assert.Nil(t, stored.AppliedConfig.InlineConfig.MaxTurns)
		assert.Contains(t, stored.AppliedConfig.InlineConfig.ClearedFields, "max_turns")
	})

	t.Run("no broker", func(t *testing.T) {
		disp := &inlineCaptureDispatcher{reincarnateTestDispatcher: newReincarnateTestDispatcher()}
		srv, s, project, broker := setupReincarnateTestServer(t, disp)
		agent := newReincarnateTestAgent(t, s, project, broker, func(a *store.Agent) {
			a.Phase = string(state.PhaseStopped)
			a.RuntimeBrokerID = ""
			a.AppliedConfig.InlineConfig = &api.ScionConfig{Harness: "claude", MaxTurns: api.IntPtr(3)}
		})
		_, code, body := patchAgentBody(t, srv, agent.ID, map[string]interface{}{
			"config": map[string]interface{}{"max_model_calls": 0},
		})
		require.Equal(t, http.StatusUnprocessableEntity, code, body)
		assert.Contains(t, body, "broker_unknown")
	})

	t.Run("negative is refused", func(t *testing.T) {
		disp := &inlineCaptureDispatcher{reincarnateTestDispatcher: newReincarnateTestDispatcher()}
		srv, s, project, broker := setupReincarnateTestServer(t, disp)
		agent := newEditTestAgent(t, s, project, broker, state.PhaseStopped)
		_, code, body := patchAgentBody(t, srv, agent.ID, map[string]interface{}{
			"config": map[string]interface{}{"max_turns": -1},
		})
		require.GreaterOrEqual(t, code, 400, body)
		require.Less(t, code, 500, body)
		assert.Contains(t, body, "max_turns")
	})
}

// TestAgentConfigPatch_HeldUnlimitedCount: with held edits on and a running
// agent, a change to Unlimited is refused on a broker without
// unlimitedCountLimits and held on one with it; the next start applies it.
func TestAgentConfigPatch_HeldUnlimitedCount(t *testing.T) {
	t.Run("old broker refuses", func(t *testing.T) {
		srv, s, _, agent := heldEditsFixture(t, state.PhaseRunning)
		_, code, body := patchAgentBody(t, srv, agent.ID, map[string]interface{}{
			"config": map[string]interface{}{"max_turns": 0},
		})
		require.Equal(t, http.StatusUnprocessableEntity, code, body)
		assert.Nil(t, pendingReloadAgent(t, s, agent.ID).PendingConfig, "a refused save holds nothing")
	})

	t.Run("capable broker holds, start applies", func(t *testing.T) {
		srv, s, disp, agent := heldEditsFixture(t, state.PhaseRunning)
		setBrokerCapabilities(t, s, agent.RuntimeBrokerID, &store.BrokerCapabilities{Reprovision: true, UnlimitedCountLimits: true})
		resp := patchPending(t, srv, agent.ID, map[string]interface{}{
			"config": map[string]interface{}{"max_turns": 0},
		})
		assert.Equal(t, []string{"config.max_turns"}, resp.Disposition.Held)
		require.NotNil(t, resp.PendingConfig)
		assert.Equal(t, "0", string(resp.PendingConfig.Config["max_turns"]))

		pendingLifecycleWarnings(t, srv, agent.ID, "stop")
		pendingLifecycleWarnings(t, srv, agent.ID, "start")
		starts := disp.snapshot()
		require.Len(t, starts, 1)
		assert.Equal(t, api.IntPtr(0), starts[0].inline.MaxTurns)
		assert.Nil(t, pendingReloadAgent(t, s, agent.ID).PendingConfig)
	})

	// Test (g): a fresh pending row (nothing pending before) stores 0 as
	// typed, so a reload still reads it as Unlimited.
	t.Run("fresh pending row keeps Unlimited across a reload", func(t *testing.T) {
		srv, s, _, agent := heldEditsFixture(t, state.PhaseRunning)
		setBrokerCapabilities(t, s, agent.RuntimeBrokerID, &store.BrokerCapabilities{Reprovision: true, UnlimitedCountLimits: true})
		patchPending(t, srv, agent.ID, map[string]interface{}{"config": map[string]interface{}{"max_turns": 0}})
		stored := pendingReloadAgent(t, s, agent.ID)
		require.NotNil(t, stored.PendingConfig)
		assert.Equal(t, "0", string(stored.PendingConfig.Config["max_turns"]))
		assert.Equal(t, store.PendingLimitsFormatTyped, stored.PendingConfig.LimitsFormat)
	})
}

// legacyPendingRow loads a pending row written before the count limits were
// typed, the way the store loads it, and stores it on the agent.
func legacyPendingRow(t *testing.T, s store.Store, agent *store.Agent, rawConfig string) *store.Agent {
	t.Helper()
	pc, err := store.UnmarshalPendingConfigFromStorage(`{"config":` + rawConfig + `,"savedAt":"2026-10-01T00:00:00Z","baseGeneration":1,"baseStateVersion":1}`)
	require.NoError(t, err)
	stored := pendingReloadAgent(t, s, agent.ID)
	_, err = s.UpdateAgentConfig(context.Background(), agent.ID, stored.StateVersion, stored.AppliedConfig, pc)
	require.NoError(t, err)
	return pendingReloadAgent(t, s, agent.ID)
}

// TestAgentPendingConfig_LegacyCountClear: a legacy pending count limit of
// 0 was a clear, and stays one through a later held save of another key
// and the next start; a PATCH of 0 over it is a change to Unlimited, not an
// echo, so an old broker refuses it.
func TestAgentPendingConfig_LegacyCountClear(t *testing.T) {
	t.Run("held save of another key keeps the clear", func(t *testing.T) {
		srv, s, disp, agent := heldEditsFixture(t, state.PhaseRunning)
		agent = legacyPendingRow(t, s, agent, `{"max_turns":0}`)
		patchPending(t, srv, agent.ID, map[string]interface{}{"config": map[string]interface{}{"model": "x"}})
		stored := pendingReloadAgent(t, s, agent.ID)
		require.NotNil(t, stored.PendingConfig)
		assert.Equal(t, "null", string(stored.PendingConfig.Config["max_turns"]))
		assert.Equal(t, store.PendingLimitsFormatTyped, stored.PendingConfig.LimitsFormat)

		pendingLifecycleWarnings(t, srv, agent.ID, "stop")
		pendingLifecycleWarnings(t, srv, agent.ID, "start")
		starts := disp.snapshot()
		require.Len(t, starts, 1)
		assert.Nil(t, starts[0].inline.MaxTurns, "the legacy 0 folds as a clear, not as Unlimited")
		assert.Equal(t, "x", starts[0].inline.Model)
	})

	t.Run("PATCH of 0 over it is a change", func(t *testing.T) {
		srv, s, _, agent := heldEditsFixture(t, state.PhaseRunning)
		agent = legacyPendingRow(t, s, agent, `{"max_turns":0}`)
		_, code, body := patchAgentBody(t, srv, agent.ID, map[string]interface{}{"config": map[string]interface{}{"max_turns": 0}})
		require.Equal(t, http.StatusUnprocessableEntity, code, body)
	})

	// With held edits off, pending rows are inert at every start, so the
	// change check compares with the applied config alone: a leftover
	// pending Unlimited does not make a change look like an echo.
	t.Run("flag off: a leftover pending Unlimited is not the base", func(t *testing.T) {
		srv, s, _, agent := heldEditsFixture(t, state.PhaseRunning)
		setBrokerCapabilities(t, s, agent.RuntimeBrokerID, &store.BrokerCapabilities{Reprovision: true, UnlimitedCountLimits: true})
		patchPending(t, srv, agent.ID, map[string]interface{}{"config": map[string]interface{}{"max_turns": 0}})
		setBrokerCapabilities(t, s, agent.RuntimeBrokerID, &store.BrokerCapabilities{Reprovision: true})
		setHeldEditsExperiment(t, srv, false)
		row := pendingReloadAgent(t, s, agent.ID)
		row.Phase = string(state.PhaseStopped)
		require.NoError(t, s.UpdateAgent(context.Background(), row))
		_, code, body := patchAgentBody(t, srv, agent.ID, map[string]interface{}{"config": map[string]interface{}{"max_turns": 0}})
		require.Equal(t, http.StatusUnprocessableEntity, code, body)
	})
}

// TestAgentPendingConfig_UnlimitedWaitsForCapableBroker: a pending
// Unlimited saved while the broker advertised unlimitedCountLimits stays
// pending when the broker no longer does, at the start fold (a start
// warning, no applied audit) and at the fold of a PATCH to an agent with no
// container (a PATCH warning); other pending keys are applied.
func TestAgentPendingConfig_UnlimitedWaitsForCapableBroker(t *testing.T) {
	setup := func(t *testing.T) (*Server, store.Store, *inlineCaptureDispatcher, *store.Agent) {
		srv, s, disp, agent := heldEditsFixture(t, state.PhaseRunning)
		setBrokerCapabilities(t, s, agent.RuntimeBrokerID, &store.BrokerCapabilities{Reprovision: true, UnlimitedCountLimits: true})
		patchPending(t, srv, agent.ID, map[string]interface{}{"config": map[string]interface{}{"max_turns": 0, "model": "held-model"}})
		setBrokerCapabilities(t, s, agent.RuntimeBrokerID, &store.BrokerCapabilities{Reprovision: true})
		return srv, s, disp, agent
	}

	t.Run("start fold", func(t *testing.T) {
		srv, s, disp, agent := setup(t)
		pendingLifecycleWarnings(t, srv, agent.ID, "stop")
		warnings := pendingLifecycleWarnings(t, srv, agent.ID, "start")
		starts := disp.snapshot()
		require.Len(t, starts, 1)
		assert.Equal(t, "held-model", starts[0].inline.Model)
		assert.Equal(t, api.IntPtr(3), starts[0].inline.MaxTurns, "the start keeps the limit it can apply")
		stored := pendingReloadAgent(t, s, agent.ID)
		require.NotNil(t, stored.PendingConfig)
		assert.Equal(t, "0", string(stored.PendingConfig.Config["max_turns"]))
		assert.NotContains(t, stored.PendingConfig.Config, "model")
		assert.True(t, containsSubstring(warnings, "config.max_turns: Unlimited waits for a runtime broker that supports it"), "%v", warnings)
		applied := pendingAudits(t, s, agent.ID, mutationTypeAgentPendingConfigApplied, 1)
		keys, _ := pendingAuditSummary(t, applied[0])
		assert.Equal(t, []string{"config.model"}, keys)
	})

	t.Run("no-container PATCH fold", func(t *testing.T) {
		srv, s, _, agent := setup(t)
		row := pendingReloadAgent(t, s, agent.ID)
		row.Phase = string(state.PhaseStopped)
		require.NoError(t, s.UpdateAgent(context.Background(), row))
		resp := patchPending(t, srv, agent.ID, map[string]interface{}{"config": map[string]interface{}{"system_prompt": "p"}})
		assert.True(t, containsSubstring(resp.Warnings, "config.max_turns: Unlimited waits for a runtime broker that supports it"), "%v", resp.Warnings)
		stored := pendingReloadAgent(t, s, agent.ID)
		require.NotNil(t, stored.PendingConfig)
		assert.Equal(t, "0", string(stored.PendingConfig.Config["max_turns"]))
		assert.Equal(t, api.IntPtr(3), stored.AppliedConfig.InlineConfig.MaxTurns)
		assert.Equal(t, "held-model", stored.AppliedConfig.InlineConfig.Model, "the other pending keys are applied")
	})
}

// TestAgentEditability_UnlimitedUnsupported: the GET editability flags the
// count limits when the broker cannot apply Unlimited.
func TestAgentEditability_UnlimitedUnsupported(t *testing.T) {
	disp := &inlineCaptureDispatcher{reincarnateTestDispatcher: newReincarnateTestDispatcher()}
	srv, s, project, broker := setupReincarnateTestServer(t, disp)
	agent := newEditTestAgent(t, s, project, broker, state.PhaseStopped)
	ed := getAgentEditability(t, srv, nil, agent.ID)
	assert.True(t, ed.Fields["config.max_turns"].UnlimitedUnsupported)
	assert.True(t, ed.Fields["config.max_model_calls"].UnlimitedUnsupported)
	assert.False(t, ed.Fields["config.max_duration"].UnlimitedUnsupported)

	setBrokerCapabilities(t, s, broker.ID, &store.BrokerCapabilities{Reprovision: true, UnlimitedCountLimits: true})
	ed = getAgentEditability(t, srv, nil, agent.ID)
	assert.False(t, ed.Fields["config.max_turns"].UnlimitedUnsupported)
	assert.False(t, ed.Fields["config.max_model_calls"].UnlimitedUnsupported)
}

// TestAgentConfigPatch_RetiredConfigureBodyMeansUnlimited: the retired
// Configure page sent "max_turns": 0 and "max_model_calls": 0 for untouched
// limits. Since count limits are typed that body means Unlimited
// (accepted, ptone/scion#5605): refused whole on a broker that cannot apply
// it, applied on one that can, and held for a running agent with held edits
// on.
func TestAgentConfigPatch_RetiredConfigureBodyMeansUnlimited(t *testing.T) {
	for _, name := range []string{"configure-untouched-body.json", "configure-row-edit-body.json"} {
		t.Run(name, func(t *testing.T) {
			t.Run("old broker", func(t *testing.T) {
				disp := &inlineCaptureDispatcher{reincarnateTestDispatcher: newReincarnateTestDispatcher()}
				srv, s, project, broker := setupReincarnateTestServer(t, disp)
				agent := newEditTestAgent(t, s, project, broker, state.PhaseStopped)
				_, code, body := patchAgentBody(t, srv, agent.ID, map[string]interface{}{"config": configureBodyFull(t, name)})
				require.Equal(t, http.StatusUnprocessableEntity, code, body)
				assert.Contains(t, body, "reload the page")
			})
			t.Run("capable broker", func(t *testing.T) {
				disp := &inlineCaptureDispatcher{reincarnateTestDispatcher: newReincarnateTestDispatcher()}
				srv, s, project, broker := setupReincarnateTestServer(t, disp)
				setBrokerCapabilities(t, s, broker.ID, &store.BrokerCapabilities{Reprovision: true, UnlimitedCountLimits: true})
				agent := newEditTestAgent(t, s, project, broker, state.PhaseStopped)
				resp, code, body := patchAgentBody(t, srv, agent.ID, map[string]interface{}{"config": configureBodyFull(t, name)})
				require.Equal(t, http.StatusOK, code, body)
				assert.Contains(t, resp.Disposition.Applied, "config.max_turns")
				assert.Contains(t, resp.Disposition.Applied, "config.max_model_calls")
				stored := pendingReloadAgent(t, s, agent.ID)
				assert.Equal(t, api.IntPtr(0), stored.AppliedConfig.InlineConfig.MaxTurns)
				assert.Equal(t, api.IntPtr(0), stored.AppliedConfig.InlineConfig.MaxModelCalls)
			})
			t.Run("running agent, held edits", func(t *testing.T) {
				srv, s, disp, agent := heldEditsFixture(t, state.PhaseRunning)
				setBrokerCapabilities(t, s, agent.RuntimeBrokerID, &store.BrokerCapabilities{Reprovision: true, UnlimitedCountLimits: true})
				resp := patchPending(t, srv, agent.ID, map[string]interface{}{"config": configureBodyFull(t, name)})
				assert.Contains(t, resp.Disposition.Held, "config.max_turns")
				assert.Contains(t, resp.Disposition.Held, "config.max_model_calls")
				pendingLifecycleWarnings(t, srv, agent.ID, "stop")
				pendingLifecycleWarnings(t, srv, agent.ID, "start")
				starts := disp.snapshot()
				require.Len(t, starts, 1)
				assert.Equal(t, api.IntPtr(0), starts[0].inline.MaxTurns)
				assert.Equal(t, api.IntPtr(0), starts[0].inline.MaxModelCalls)
			})
		})
	}
}

// containsSubstring reports whether any of list contains sub.
func containsSubstring(list []string, sub string) bool {
	for _, s := range list {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

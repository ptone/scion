// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
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
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/GoogleCloudPlatform/scion/pkg/agent/state"
	"github.com/GoogleCloudPlatform/scion/pkg/store"
)

// A start of an agent whose config was never delivered goes through
// create-with-gather when the agent has as-needed entries, so its as-needed
// values are resolved and delivered, or the start fails with nothing started
// (ptone/scion#6096). Agents with a delivered config, legacy agents that
// already started, and agents without as-needed entries start as before.

// seedAsNeededEnv stores an as-needed env var in f's project scope.
func seedAsNeededEnv(t *testing.T, f *runDispatchFixture, key, value string) {
	t.Helper()
	require.NoError(t, f.store.CreateEnvVar(context.Background(), &store.EnvVar{
		ID: uuid.NewString(), Key: key, Value: value,
		Scope: store.ScopeProject, ScopeID: f.agent.ProjectID, InjectionMode: store.InjectionModeAsNeeded,
	}))
}

// neverStarted makes f's agent a row that never started: no run history, a
// pre-start phase and no delivered-config marker.
func neverStarted(f *runDispatchFixture) {
	f.agent.RunID = ""
	f.agent.PreviousRunIDs = nil
	f.agent.Phase = string(state.PhaseCreated)
	f.agent.AppliedConfig.ConfigDelivered = false
	f.agent.AppliedConfig.AsNeededPending = false
}

func TestDispatchAgentStart_UndeliveredConfigGoesThroughCreate(t *testing.T) {
	t.Run("never-started row: gathers and delivers", func(t *testing.T) {
		f := newRunDispatchFixture(t)
		neverStarted(f)
		seedAsNeededEnv(t, f, failClosedKey, failClosedValue)
		calls := gatherNeedsClient(f)

		require.NoError(t, f.d.DispatchAgentStart(context.Background(), f.agent, "", false))
		assert.False(t, f.client.startCalled, "no plain start")
		assert.Equal(t, 2, *calls, "the needs report and the replay")
		require.NotNil(t, f.client.lastCreateReq)
		assert.Equal(t, failClosedValue, f.client.lastCreateReq.ResolvedEnv[failClosedKey], "the as-needed value is delivered")
		assert.True(t, f.agent.AppliedConfig.ConfigDelivered, "a delivered create marks the agent")
	})
	t.Run("never-started row: unmet need fails, nothing started", func(t *testing.T) {
		f := newRunDispatchFixture(t)
		neverStarted(f)
		seedAsNeededEnv(t, f, "FAKE_OTHER_KEY", "fake-other-value")
		f.client.createWithGatherFunc = func(_ context.Context, _, _ string, req *RemoteCreateAgentRequest) (*RemoteAgentResponse, *RemoteEnvRequirementsResponse, error) {
			return nil, &RemoteEnvRequirementsResponse{AgentID: req.ID, Needs: []string{"FAKE_UNRESOLVABLE_KEY"}}, nil
		}

		err := f.d.DispatchAgentStart(context.Background(), f.agent, "", false)
		require.Error(t, err)
		assert.ErrorIs(t, err, errProvisionEnvUnmet)
		assert.NotContains(t, err.Error(), "FAKE_UNRESOLVABLE_KEY")
		assert.False(t, f.client.startCalled, "nothing started")
		assert.False(t, f.agent.AppliedConfig.ConfigDelivered)
	})
	t.Run("marker absent with a run ID but no successful start: routed", func(t *testing.T) {
		// A run ID alone is not proof: beginRun records it before the
		// broker call, so a failed create can leave one.
		f := newRunDispatchFixture(t)
		f.agent.Phase = string(state.PhaseProvisioning)
		require.NotEmpty(t, f.agent.RunID)
		seedAsNeededEnv(t, f, failClosedKey, failClosedValue)
		gatherNeedsClient(f)

		require.NoError(t, f.d.DispatchAgentStart(context.Background(), f.agent, "", false))
		assert.False(t, f.client.startCalled, "never a plain start")
		assert.True(t, f.client.createCalled)
	})
	t.Run("provision-only row (created, delivered marker absent): routed", func(t *testing.T) {
		f := newRunDispatchFixture(t)
		neverStarted(f)
		seedAsNeededEnv(t, f, failClosedKey, failClosedValue)
		gatherNeedsClient(f)

		require.NoError(t, f.d.DispatchAgentStart(context.Background(), f.agent, "", false))
		assert.False(t, f.client.startCalled)
		assert.True(t, f.client.createCalled)
	})
	t.Run("lookup error: fails closed with the fixed start message", func(t *testing.T) {
		f := newRunDispatchFixture(t)
		neverStarted(f)
		f.d.store = &envReadErrStore{Store: f.store}

		err := f.d.DispatchAgentStart(context.Background(), f.agent, "", false)
		require.Error(t, err)
		assert.Equal(t, envReadFixedMessage("started"), err.Error())
		assert.False(t, f.client.startCalled, "never a plain start")
		assert.False(t, f.client.createCalled)
	})
}

func TestDispatchAgentStart_DeliveredOrLegacyOrNoAsNeededStartsPlain(t *testing.T) {
	cases := []struct {
		name  string
		setup func(t *testing.T, f *runDispatchFixture)
	}{
		{"delivered marker", func(t *testing.T, f *runDispatchFixture) {
			neverStarted(f)
			f.agent.AppliedConfig.ConfigDelivered = true
			seedAsNeededEnv(t, f, failClosedKey, failClosedValue)
		}},
		{"legacy row with run history and a successful start", func(t *testing.T, f *runDispatchFixture) {
			f.agent.Phase = string(state.PhaseStopped)
			require.NotEmpty(t, f.agent.RunID)
			seedAsNeededEnv(t, f, failClosedKey, failClosedValue)
		}},
		{"no as-needed entries", func(t *testing.T, f *runDispatchFixture) {
			neverStarted(f)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newRunDispatchFixture(t)
			tc.setup(t, f)
			require.NoError(t, f.d.DispatchAgentStart(context.Background(), f.agent, "", false))
			assert.True(t, f.client.startCalled, "a plain start, as before")
			assert.False(t, f.client.createCalled, "no create")
		})
	}
}

// A restart is not routed: a plain restart does not gather as-needed values
// (ptone/scion#6101).
func TestDispatchAgentRestart_NotRouted(t *testing.T) {
	f := newRunDispatchFixture(t)
	neverStarted(f)
	seedAsNeededEnv(t, f, failClosedKey, failClosedValue)
	require.NoError(t, f.d.DispatchAgentRestart(context.Background(), f.agent))
	assert.True(t, f.client.restartCalled)
	assert.False(t, f.client.createCalled)
}

// The marker is set only on a completed create or provision: not when the
// broker's needs stay unmet, not on a failed provision.
func TestConfigDeliveredMarker(t *testing.T) {
	t.Run("provision success sets pending, not delivered", func(t *testing.T) {
		// A provision starts nothing, and the as-needed values it resolves
		// go only into that provision request.
		f := newRunDispatchFixture(t)
		f.agent.AppliedConfig.ConfigDelivered = true
		require.NoError(t, f.d.DispatchAgentProvision(context.Background(), f.agent))
		assert.False(t, f.agent.AppliedConfig.ConfigDelivered)
		assert.True(t, f.agent.AppliedConfig.AsNeededPending)
	})
	t.Run("reprovision success sets pending, not delivered", func(t *testing.T) {
		f := newRunDispatchFixture(t)
		require.NoError(t, f.d.DispatchAgentReprovision(context.Background(), f.agent))
		assert.False(t, f.agent.AppliedConfig.ConfigDelivered)
		assert.True(t, f.agent.AppliedConfig.AsNeededPending)
	})
	t.Run("create success marks delivered and clears pending", func(t *testing.T) {
		f := newRunDispatchFixture(t)
		f.agent.AppliedConfig.AsNeededPending = true
		_, err := f.d.DispatchAgentCreateWithGather(context.Background(), f.agent)
		require.NoError(t, err)
		assert.True(t, f.agent.AppliedConfig.ConfigDelivered)
		assert.False(t, f.agent.AppliedConfig.AsNeededPending)
	})
	t.Run("create with remaining needs does not mark", func(t *testing.T) {
		f := newRunDispatchFixture(t)
		f.client.createWithGatherFunc = func(_ context.Context, _, _ string, req *RemoteCreateAgentRequest) (*RemoteAgentResponse, *RemoteEnvRequirementsResponse, error) {
			return nil, &RemoteEnvRequirementsResponse{AgentID: req.ID, Needs: []string{"FAKE_UNRESOLVABLE_KEY"}}, nil
		}
		res, err := f.d.DispatchAgentCreateWithGather(context.Background(), f.agent)
		require.NoError(t, err)
		require.NotNil(t, res.EnvRequirements())
		assert.False(t, f.agent.AppliedConfig.ConfigDelivered)
	})
	t.Run("failed provision does not mark", func(t *testing.T) {
		f := newRunDispatchFixture(t)
		f.client.createWithGatherFunc = func(_ context.Context, _, _ string, req *RemoteCreateAgentRequest) (*RemoteAgentResponse, *RemoteEnvRequirementsResponse, error) {
			return nil, &RemoteEnvRequirementsResponse{AgentID: req.ID, Needs: []string{"FAKE_UNRESOLVABLE_KEY"}}, nil
		}
		require.Error(t, f.d.DispatchAgentProvision(context.Background(), f.agent))
		assert.False(t, f.agent.AppliedConfig.ConfigDelivered)
	})
}

// End to end with the real dispatcher: the start after a provision-only
// create, after a reprovision (the reincarnation start leg) and after any
// provision that left AsNeededPending (a move's start on the target) is
// routed through create-with-gather, and the as-needed value reaches that
// start's request, even when the agent's run history shows earlier starts.
func TestDispatchAgentStart_AfterProvisionGathersAgain(t *testing.T) {
	t.Run("provision-only create, then start", func(t *testing.T) {
		f := newRunDispatchFixture(t)
		neverStarted(f)
		seedAsNeededEnv(t, f, failClosedKey, failClosedValue)
		calls := gatherNeedsClient(f)

		require.NoError(t, f.d.DispatchAgentProvision(context.Background(), f.agent))
		require.Equal(t, 2, *calls, "the provision's needs report and replay")
		f.client.createCalled, f.client.lastCreateReq = false, nil

		require.NoError(t, f.d.DispatchAgentStart(context.Background(), f.agent, "", false))
		assert.False(t, f.client.startCalled, "no plain start after a provision")
		assert.Equal(t, 4, *calls, "the start's needs report and replay")
		require.NotNil(t, f.client.lastCreateReq)
		assert.False(t, f.client.lastCreateReq.ProvisionOnly, "the start goes through a create that starts the agent")
		assert.Equal(t, failClosedValue, f.client.lastCreateReq.ResolvedEnv[failClosedKey], "the as-needed value reaches the start")
		assert.True(t, f.agent.AppliedConfig.ConfigDelivered)
		assert.False(t, f.agent.AppliedConfig.AsNeededPending)
	})
	t.Run("reprovision, then the start leg", func(t *testing.T) {
		f := newRunDispatchFixture(t)
		f.agent.Phase = string(state.PhaseStopped) // ran before: legacy proof alone would plain-start
		seedAsNeededEnv(t, f, failClosedKey, failClosedValue)
		calls := gatherNeedsClient(f)

		require.NoError(t, f.d.DispatchAgentReprovision(context.Background(), f.agent))
		f.client.createCalled, f.client.lastCreateReq = false, nil

		require.NoError(t, f.d.DispatchAgentStart(context.Background(), f.agent, "[SCION REINCARNATION] fake preamble", false))
		assert.False(t, f.client.startCalled, "no plain start after a reprovision")
		assert.Equal(t, 4, *calls)
		require.NotNil(t, f.client.lastCreateReq)
		assert.Equal(t, failClosedValue, f.client.lastCreateReq.ResolvedEnv[failClosedKey])
	})
	t.Run("pending provision overrides legacy proof (move start on the target)", func(t *testing.T) {
		f := newRunDispatchFixture(t)
		f.agent.Phase = string(state.PhaseStopped)
		f.agent.AppliedConfig.AsNeededPending = true
		seedAsNeededEnv(t, f, failClosedKey, failClosedValue)
		gatherNeedsClient(f)

		require.NoError(t, f.d.DispatchAgentStart(context.Background(), f.agent, "", false))
		assert.False(t, f.client.startCalled)
		assert.True(t, f.client.createCalled)
	})
}

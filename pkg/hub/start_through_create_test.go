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
	"log/slog"
	"net/http"
	"testing"
	"time"

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
}

func TestDispatchAgentStart_UndeliveredConfigGoesThroughCreate(t *testing.T) {
	t.Run("never-started row: gathers and delivers", func(t *testing.T) {
		f := newRunDispatchFixture(t)
		neverStarted(f)
		seedAsNeededEnv(t, f, failClosedKey, failClosedValue)
		grantPreserveWorkspaceCreate(t, f.store, f.agent.RuntimeBrokerID)
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
		grantPreserveWorkspaceCreate(t, f.store, f.agent.RuntimeBrokerID)
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
		grantPreserveWorkspaceCreate(t, f.store, f.agent.RuntimeBrokerID)
		gatherNeedsClient(f)

		require.NoError(t, f.d.DispatchAgentStart(context.Background(), f.agent, "", false))
		assert.False(t, f.client.startCalled, "never a plain start")
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

// Starts the routing leaves to a plain start: an agent without as-needed
// entries starts plain; on a broker without startEnvGather, an agent with
// as-needed entries (a delivered row, a resume) is refused with 412 before
// anything is sent, since a plain start would lack its as-needed values
// (ptone/scion#6101).
func TestDispatchAgentStart_DeliveredOrNoAsNeededStartsPlain(t *testing.T) {
	cases := []struct {
		name    string
		setup   func(t *testing.T, f *runDispatchFixture)
		refused bool
	}{
		{"delivered marker", func(t *testing.T, f *runDispatchFixture) {
			neverStarted(f)
			f.agent.AppliedConfig.ConfigDelivered = true
			seedAsNeededEnv(t, f, failClosedKey, failClosedValue)
			grantPreserveWorkspaceCreate(t, f.store, f.agent.RuntimeBrokerID)
		}, true},
		{"no as-needed entries", func(t *testing.T, f *runDispatchFixture) {
			neverStarted(f)
		}, false},
		{"resumed start", func(t *testing.T, f *runDispatchFixture) {
			seedAsNeededEnv(t, f, failClosedKey, failClosedValue)
			grantPreserveWorkspaceCreate(t, f.store, f.agent.RuntimeBrokerID)
		}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newRunDispatchFixture(t)
			tc.setup(t, f)
			resume := tc.name == "resumed start"
			err := f.d.DispatchAgentStart(context.Background(), f.agent, "", resume)
			assert.False(t, f.client.createCalled, "no create")
			if tc.refused {
				require.ErrorIs(t, err, errBrokerLacksStartEnvGather)
				assert.False(t, f.client.startCalled, "nothing sent")
				return
			}
			require.NoError(t, err)
			assert.True(t, f.client.startCalled, "a plain start, as before")
		})
	}
}

// A legacy row without the marker is routed even when its run history and
// phase look like an earlier start: no stored field reliably shows that a
// run landed (the stop action writes stopped either way).
func TestDispatchAgentStart_LegacyRowWithoutMarkerIsRouted(t *testing.T) {
	f := newRunDispatchFixture(t)
	f.agent.Phase = string(state.PhaseStopped)
	require.NotEmpty(t, f.agent.RunID)
	seedAsNeededEnv(t, f, failClosedKey, failClosedValue)
	grantPreserveWorkspaceCreate(t, f.store, f.agent.RuntimeBrokerID)
	gatherNeedsClient(f)

	require.NoError(t, f.d.DispatchAgentStart(context.Background(), f.agent, "", false))
	assert.False(t, f.client.startCalled, "never a plain start without the marker")
	require.NotNil(t, f.client.lastCreateReq)
	assert.Equal(t, failClosedValue, f.client.lastCreateReq.ResolvedEnv[failClosedKey])
}

// A routed start is always a synchronous create, even for an agent opted
// into asynchronous launches with async launch enabled on the hub: no send
// asks the broker for an asynchronous launch.
func TestDispatchAgentStart_RoutedStartIsSynchronous(t *testing.T) {
	f := newRunDispatchFixture(t)
	neverStarted(f)
	f.agent.LaunchAsyncOptIn = true
	f.d.asyncLaunchSettings = func() AsyncLaunchSettings {
		return AsyncLaunchSettings{Enabled: true, Timeout: time.Minute}
	}
	seedAsNeededEnv(t, f, failClosedKey, failClosedValue)
	grantPreserveWorkspaceCreate(t, f.store, f.agent.RuntimeBrokerID)
	sawAsync := false
	f.client.createWithGatherFunc = func(_ context.Context, _, _ string, req *RemoteCreateAgentRequest) (*RemoteAgentResponse, *RemoteEnvRequirementsResponse, error) {
		if req.AsyncLaunch {
			sawAsync = true
		}
		if req.ResolvedEnv[failClosedKey] != "" {
			return &RemoteAgentResponse{Agent: &RemoteAgentInfo{ID: req.ID, Slug: req.Slug, Name: req.Name}, Created: true}, nil, nil
		}
		return nil, &RemoteEnvRequirementsResponse{AgentID: req.ID, Needs: []string{failClosedKey}}, nil
	}

	res, err := f.d.DispatchAgentStartWithOptions(context.Background(), f.agent, "", false, StartDispatchOptions{Async: true, Kind: store.LaunchKindStart})
	require.NoError(t, err)
	assert.False(t, sawAsync, "no send asks for an asynchronous launch")
	assert.Nil(t, res.AcceptedLaunch(), "no launch: the routed create is synchronous")
	require.NotNil(t, f.client.lastCreateReq)
	assert.Equal(t, failClosedValue, f.client.lastCreateReq.ResolvedEnv[failClosedKey])
	assert.True(t, f.agent.LaunchAsyncOptIn, "the agent's opt-in is restored")
}

// Across nodes: a start the requesting node decided is plain (a restart's
// start leg) is not routed again on the owner, and a routed start's
// create runs synchronously on the owner although the stored agent is opted
// into asynchronous launches.
func TestCrossNode_StartRoutingDecidedOnRequester(t *testing.T) {
	t.Run("restart start leg stays plain on the owner", func(t *testing.T) {
		f := newCrossNodeFixture(t, nil, true)
		_ = f.requester.DispatchAgentStart(withRestartStartLeg(crossNodeCtx(t)), f.agent, "", false)
		f.ownerDisp.recMu.Lock()
		defer f.ownerDisp.recMu.Unlock()
		assert.True(t, f.ownerDisp.startRoutingSuppressed, "the owner received the start intent with routing suppressed")
	})
	t.Run("routed start creates synchronously on the owner", func(t *testing.T) {
		const hubID = "hub-routed-sync"
		f := newCrossNodeFixture(t, nil, true)
		f.requester.client = &mockRuntimeBrokerClient{returnErr: ErrLifecycleDeferred}
		f.requester.SetHubID(hubID)
		require.NoError(t, f.store.CreateEnvVar(context.Background(), &store.EnvVar{
			ID: uuid.NewString(), Key: failClosedKey, Value: failClosedValue,
			Scope: store.ScopeHub, ScopeID: hubID, InjectionMode: store.InjectionModeAsNeeded,
		}))
		stored, err := f.store.GetAgent(context.Background(), f.agent.ID)
		require.NoError(t, err)
		stored.LaunchAsyncOptIn = true
		require.NoError(t, f.store.UpdateAgent(context.Background(), stored))
		f.agent = stored
		grantPreserveWorkspaceCreate(t, f.store, f.agent.RuntimeBrokerID)

		_ = f.requester.DispatchAgentStart(crossNodeCtx(t), f.agent, "", false)
		f.ownerDisp.recMu.Lock()
		defer f.ownerDisp.recMu.Unlock()
		require.NotNil(t, f.ownerDisp.createAsyncOptIn, "the routed start reached the owner as a create")
		assert.False(t, *f.ownerDisp.createAsyncOptIn, "the owner creates synchronously")
	})
}

// A restart is not routed: a plain restart does not gather as-needed values
// (ptone/scion#6101).
func TestDispatchAgentRestart_NotRouted(t *testing.T) {
	t.Run("DispatchAgentRestart", func(t *testing.T) {
		f := newRunDispatchFixture(t)
		neverStarted(f)
		seedAsNeededEnv(t, f, failClosedKey, failClosedValue)
		grantPreserveWorkspaceCreate(t, f.store, f.agent.RuntimeBrokerID)
		require.NoError(t, f.d.DispatchAgentRestart(context.Background(), f.agent))
		assert.True(t, f.client.restartCalled)
		assert.False(t, f.client.createCalled)
	})
	// A restart's start leg is not routed either; on a broker without
	// startEnvGather an agent with as-needed entries is refused with 412
	// (ptone/scion#6101).
	t.Run("restart start leg (start claim marker)", func(t *testing.T) {
		f := newRunDispatchFixture(t)
		neverStarted(f)
		seedAsNeededEnv(t, f, failClosedKey, failClosedValue)
		grantPreserveWorkspaceCreate(t, f.store, f.agent.RuntimeBrokerID)
		err := f.d.DispatchAgentStart(withRestartStartLeg(context.Background()), f.agent, "", false)
		require.ErrorIs(t, err, errBrokerLacksStartEnvGather)
		assert.False(t, f.client.startCalled, "nothing sent")
		assert.False(t, f.client.createCalled)
	})
	t.Run("restart start leg (async restart kind)", func(t *testing.T) {
		f := newRunDispatchFixture(t)
		neverStarted(f)
		seedAsNeededEnv(t, f, failClosedKey, failClosedValue)
		grantPreserveWorkspaceCreate(t, f.store, f.agent.RuntimeBrokerID)
		_, err := f.d.DispatchAgentStartWithOptions(context.Background(), f.agent, "", false, StartDispatchOptions{Kind: store.LaunchKindRestart})
		require.ErrorIs(t, err, errBrokerLacksStartEnvGather)
		assert.False(t, f.client.createCalled)
	})
}

// Through the HTTP lifecycle API with the real dispatcher: a restart stays a
// plain restart, and a start of an agent without the marker that has
// as-needed entries goes through create-with-gather.
func TestLifecycleHTTP_StartRoutedRestartNot(t *testing.T) {
	setup := func(t *testing.T, name string, phase state.Phase) (*Server, *mockRuntimeBrokerClient, *store.Agent) {
		srv, s := testServer(t)
		grantDevUserRuntimeBrokerAccess(t, s)
		project, broker := newEmptyPerAgentHandlerProject(t, s, name, true)
		grantPreserveWorkspaceCreate(t, s, broker.ID)
		agent := newQuotaTestAgent(t, s, broker, project, name, phase)
		require.NoError(t, s.CreateEnvVar(context.Background(), &store.EnvVar{
			ID: uuid.NewString(), Key: failClosedKey, Value: failClosedValue,
			Scope: store.ScopeProject, ScopeID: project.ID, InjectionMode: store.InjectionModeAsNeeded,
		}))
		client := &mockRuntimeBrokerClient{}
		srv.SetDispatcher(NewHTTPAgentDispatcherWithClient(s, client, false, slog.Default()))
		return srv, client, agent
	}
	t.Run("restart", func(t *testing.T) {
		// On a broker without startEnvGather the restart of an agent with
		// as-needed entries is refused before its stop leg: the agent is
		// left as it was (ptone/scion#6101).
		srv, client, agent := setup(t, "route-restart", state.PhaseRunning)
		rec := doRequest(t, srv, http.MethodPost, "/api/v1/agents/"+agent.ID+"/restart", nil)
		require.Equal(t, http.StatusPreconditionFailed, rec.Code, rec.Body.String())
		assert.False(t, client.stopCalled, "refused before the stop leg")
		assert.False(t, client.startCalled || client.restartCalled, "nothing started")
		assert.False(t, client.createCalled, "a restart is not routed through create (ptone/scion#6101)")
	})
	t.Run("start", func(t *testing.T) {
		srv, client, agent := setup(t, "route-start", state.PhaseStopped)
		rec := doRequest(t, srv, http.MethodPost, "/api/v1/agents/"+agent.ID+"/start", nil)
		require.Less(t, rec.Code, 300, rec.Body.String())
		assert.True(t, client.createCalled, "the start went through create-with-gather")
		assert.False(t, client.startCalled, "no plain start")
	})
}

// The marker is set only on a completed create or provision: not when the
// broker's needs stay unmet, not on a failed provision.
func TestConfigDeliveredMarker(t *testing.T) {
	t.Run("provision success clears the marker", func(t *testing.T) {
		// A provision starts nothing, and the as-needed values it resolves
		// go only into that provision request.
		f := newRunDispatchFixture(t)
		f.agent.AppliedConfig.ConfigDelivered = true
		require.NoError(t, f.d.DispatchAgentProvision(context.Background(), f.agent))
		assert.False(t, f.agent.AppliedConfig.ConfigDelivered)
	})
	t.Run("reprovision success clears the marker", func(t *testing.T) {
		f := newRunDispatchFixture(t)
		f.agent.AppliedConfig.ConfigDelivered = true
		require.NoError(t, f.d.DispatchAgentReprovision(context.Background(), f.agent))
		assert.False(t, f.agent.AppliedConfig.ConfigDelivered)
	})
	t.Run("create success marks delivered", func(t *testing.T) {
		f := newRunDispatchFixture(t)
		_, err := f.d.DispatchAgentCreateWithGather(context.Background(), f.agent)
		require.NoError(t, err)
		assert.True(t, f.agent.AppliedConfig.ConfigDelivered)
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

// End to end with the real dispatcher, with the marker as the provision path
// leaves it (never hand-set to false): the start after a provision-only
// create, after a reprovision (the reincarnation start leg) and after a
// provision that follows a delivered create (a move's start on the target)
// is routed through create-with-gather, and the as-needed value reaches that
// start's request.
func TestDispatchAgentStart_AfterProvisionGathersAgain(t *testing.T) {
	t.Run("provision-only create, then start", func(t *testing.T) {
		f := newRunDispatchFixture(t)
		neverStarted(f)
		seedAsNeededEnv(t, f, failClosedKey, failClosedValue)
		grantPreserveWorkspaceCreate(t, f.store, f.agent.RuntimeBrokerID)
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
	})
	t.Run("reprovision, then the start leg", func(t *testing.T) {
		f := newRunDispatchFixture(t)
		f.agent.Phase = string(state.PhaseStopped) // ran before: legacy proof alone would plain-start
		seedAsNeededEnv(t, f, failClosedKey, failClosedValue)
		grantPreserveWorkspaceCreate(t, f.store, f.agent.RuntimeBrokerID)
		calls := gatherNeedsClient(f)

		require.NoError(t, f.d.DispatchAgentReprovision(context.Background(), f.agent))
		f.client.createCalled, f.client.lastCreateReq = false, nil

		require.NoError(t, f.d.DispatchAgentStart(context.Background(), f.agent, "[SCION REINCARNATION] fake preamble", false))
		assert.False(t, f.client.startCalled, "no plain start after a reprovision")
		assert.Equal(t, 4, *calls)
		require.NotNil(t, f.client.lastCreateReq)
		assert.Equal(t, failClosedValue, f.client.lastCreateReq.ResolvedEnv[failClosedKey])
	})
	t.Run("delivered agent, then reprovision (move or reincarnation), then start", func(t *testing.T) {
		f := newRunDispatchFixture(t)
		f.agent.Phase = string(state.PhaseStopped)
		f.agent.AppliedConfig.ConfigDelivered = true
		seedAsNeededEnv(t, f, failClosedKey, failClosedValue)
		grantPreserveWorkspaceCreate(t, f.store, f.agent.RuntimeBrokerID)
		gatherNeedsClient(f)

		require.NoError(t, f.d.DispatchAgentReprovision(context.Background(), f.agent))
		require.False(t, f.agent.AppliedConfig.ConfigDelivered, "the provision cleared the marker")
		f.client.createCalled, f.client.lastCreateReq = false, nil
		require.NoError(t, f.d.DispatchAgentStart(context.Background(), f.agent, "", false))
		assert.False(t, f.client.startCalled)
		assert.True(t, f.client.createCalled)
	})
}

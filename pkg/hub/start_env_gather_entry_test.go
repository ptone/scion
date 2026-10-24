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
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/GoogleCloudPlatform/scion/pkg/agent/state"
	"github.com/GoogleCloudPlatform/scion/pkg/api"
	"github.com/GoogleCloudPlatform/scion/pkg/store"
)

// Every start and restart dispatch entry point runs the same order
// (ptone/scion#6101, design r6 §4.5.1): the dispatch guards, then
// requireRuntimeBrokerAssigned, then the capability check (503 for a broker
// record that cannot be read or does not exist; 412 on a broker without
// startEnvGather for an agent with as-needed entries), and only then
// anything that mints or sends.

// countBrokerReadsStore counts GetRuntimeBroker calls.
type countBrokerReadsStore struct {
	store.Store
	reads *atomic.Int32
}

func (s countBrokerReadsStore) GetRuntimeBroker(ctx context.Context, id string) (*store.RuntimeBroker, error) {
	s.reads.Add(1)
	return s.Store.GetRuntimeBroker(ctx, id)
}

// entryPoint is one start or restart dispatch entry point.
type entryPoint struct {
	name string
	verb string // the action the fixed refusal messages name
	run  func(t *testing.T, f *runDispatchFixture) error
}

// ownerServer is an owner node over f's store running f's dispatcher.
func ownerServer(f *runDispatchFixture) *Server {
	srv := &Server{
		store:             f.store,
		instanceID:        "hub-owner-" + uuid.NewString()[:8],
		agentLifecycleLog: slog.Default(),
		events:            NewChannelEventPublisher(),
	}
	srv.SetDispatcher(f.d)
	return srv
}

var startEntryPoints = []entryPoint{
	{"dispatchAgentStart sync", "started", func(t *testing.T, f *runDispatchFixture) error {
		return f.d.DispatchAgentStart(context.Background(), f.agent, "", true)
	}},
	{"dispatchAgentStart async-eligible", "started", func(t *testing.T, f *runDispatchFixture) error {
		f.d.SetAsyncLaunchSettingsProvider(func() AsyncLaunchSettings {
			return AsyncLaunchSettings{Enabled: true, Timeout: 5 * time.Minute, KeepaliveSeconds: 15}
		})
		_, err := f.d.DispatchAgentStartWithOptions(context.Background(), f.agent, "", true,
			StartDispatchOptions{Async: true, Kind: store.LaunchKindStart, PriorPhase: string(state.PhaseSuspended)})
		return err
	}},
	{"DispatchAgentRestart (broker restart endpoint)", "restarted", func(t *testing.T, f *runDispatchFixture) error {
		return f.d.DispatchAgentRestart(context.Background(), f.agent)
	}},
	{"lifecycle restart pre-check", "restarted", func(t *testing.T, f *runDispatchFixture) error {
		// As the lifecycle restart: the placement pre-check reads the
		// record (startPlacementPreCheckBroker), the env-gather pre-check
		// judges it.
		broker, err := checkAgentPinnedPlacement(context.Background(), f.d.store, f.agent)
		if err != nil {
			return placementBackstopStartError(err, "restarted")
		}
		return f.d.RestartEnvGatherPrecheck(context.Background(), f.agent, broker)
	}},
	{"owner node execDispatchStart", "started", func(t *testing.T, f *runDispatchFixture) error {
		require.NoError(t, f.store.UpdateAgent(context.Background(), f.agent))
		_, err := ownerServer(f).execDispatchStart(context.Background(), store.BrokerDispatch{
			ID: uuid.NewString(), Op: "start", AgentID: f.agent.ID, BrokerID: f.agent.RuntimeBrokerID,
			Args: `{"resume":true,"noRoute":true}`,
		})
		return err
	}},
	{"owner node execDispatchRestart", "restarted", func(t *testing.T, f *runDispatchFixture) error {
		require.NoError(t, f.store.UpdateAgent(context.Background(), f.agent))
		_, err := ownerServer(f).execDispatchRestart(context.Background(), store.BrokerDispatch{
			ID: uuid.NewString(), Op: "restart", AgentID: f.agent.ID, BrokerID: f.agent.RuntimeBrokerID,
		})
		return err
	}},
}

// assertNothingSent checks that no start, restart or create reached the
// broker and that no run was minted.
func assertNothingSent(t *testing.T, f *runDispatchFixture, gc *gatherStartClient, runBefore string) {
	t.Helper()
	assert.Empty(t, gc.calls, "no start sent")
	assert.False(t, f.client.restartCalled, "no restart sent")
	assert.False(t, f.client.createCalled, "no create sent")
	got, err := f.store.GetAgent(context.Background(), f.agent.ID)
	require.NoError(t, err)
	assert.Equal(t, runBefore, got.RunID, "no run minted")
}

func plainStarted(int, gatherStartCall) (*RemoteAgentResponse, error) {
	return startedAnswer(false), nil
}

// H-22: the old-broker 412 and the C3 503 on every entry point.
func TestStartEntryPoints_OldBroker412AndMissingBroker503(t *testing.T) {
	for _, ep := range startEntryPoints {
		t.Run(ep.name+"/old broker, as-needed entries: 412", func(t *testing.T) {
			f, gc := newGatherStartFixture(t, false, plainStarted, nil)
			seedAsNeededEnv(t, f, failClosedKey, failClosedValue)
			runBefore := f.agent.RunID
			err := ep.run(t, f)
			require.ErrorIs(t, err, errBrokerLacksStartEnvGather)
			assert.True(t, isConfirmedStartNotActedOnError(err))
			assertNothingSent(t, f, gc, runBefore)
		})
		t.Run(ep.name+"/broker read error: 503", func(t *testing.T) {
			// The record is first read by the placement backstop: its read
			// error is the same fixed 503, with no store text.
			f, gc := newGatherStartFixture(t, true, plainStarted, func(s store.Store) store.Store { return failBrokerReadStore{s} })
			seedAsNeededEnv(t, f, failClosedKey, failClosedValue)
			runBefore := f.agent.RunID
			err := ep.run(t, f)
			require.ErrorIs(t, err, errBrokerRecordUnreadable)
			assert.NotContains(t, err.Error(), "FAKE-broker-read-detail", "no store text in the error")
			assert.True(t, isConfirmedStartNotActedOnError(err))
			rec := httptest.NewRecorder()
			require.True(t, writePreserveWorkspaceCapabilityError(rec, err))
			assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
			assert.NotContains(t, rec.Body.String(), "FAKE-broker-read-detail", "no store text in the body")
			assert.Contains(t, rec.Body.String(), "the agent's runtime broker record could not be read; the agent was not "+ep.verb, "the action's verb")
			assertNothingSent(t, f, gc, runBefore)
		})
		t.Run(ep.name+"/missing broker record: 503", func(t *testing.T) {
			f, gc := newGatherStartFixture(t, true, plainStarted, nil)
			f.agent.RuntimeBrokerID = tid("broker-missing-" + uuid.NewString()[:8])
			runBefore := f.agent.RunID
			err := ep.run(t, f)
			require.ErrorIs(t, err, errBrokerRecordUnreadable)
			assert.True(t, isConfirmedStartNotActedOnError(err))
			assertNothingSent(t, f, gc, runBefore)
		})
	}
}

// F1 and H-25 (design r17): the broker /restart path names the restart in
// a lookup error (the 503 "restarted"), on an old and on a capable broker.
func TestDispatchAgentRestart_AsNeededLookupErrorNamesTheRestart(t *testing.T) {
	for _, capable := range []bool{false, true} {
		f, gc := newGatherStartFixture(t, capable, plainStarted, func(s store.Store) store.Store {
			return &envReadErrStore{Store: s}
		})
		err := f.d.DispatchAgentRestart(context.Background(), f.agent)
		require.Error(t, err, "capable=%v", capable)
		var resErr *secretResolutionError
		require.ErrorAs(t, err, &resErr)
		assert.Equal(t, "restarted", resErr.Verb)
		assert.Equal(t, envReadFixedMessage("restarted"), err.Error())
		assert.Empty(t, gc.calls)
		assert.False(t, f.client.restartCalled)
		assert.False(t, f.client.stopCalled)
	}
}

// H-25 (design r17 §9, L-1): on a capable broker the broker /restart of an
// agent with as-needed entries (DispatchAgentRestart, reached by a queued
// restart row, on this node or on the owner node) is refused with the
// keyless 412 errRestartGatherPending: nothing minted, sent or stopped.
func TestDispatchAgentRestart_CapableBrokerWithAsNeededEntriesIs412(t *testing.T) {
	for _, ep := range startEntryPoints {
		if ep.name != "DispatchAgentRestart (broker restart endpoint)" && ep.name != "owner node execDispatchRestart" {
			continue
		}
		t.Run(ep.name, func(t *testing.T) {
			f, gc := newGatherStartFixture(t, true, plainStarted, nil)
			seedAsNeededEnv(t, f, failClosedKey, failClosedValue)
			runBefore := f.agent.RunID
			err := ep.run(t, f)
			require.ErrorIs(t, err, errRestartGatherPending)
			assert.NotErrorIs(t, err, errBrokerLacksStartEnvGather, "not the upgrade refusal")
			assert.True(t, isConfirmedStartNotActedOnError(err))
			assert.False(t, f.client.stopCalled, "nothing stopped")
			assertNothingSent(t, f, gc, runBefore)
			rec := httptest.NewRecorder()
			require.True(t, writeStartEnvGatherError(rec, err, api.AgentActionRestart))
			assert.Equal(t, http.StatusPreconditionFailed, rec.Code)
			var body ErrorResponse
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
			assert.Equal(t, ErrCodeUnsupportedCapability, body.Error.Code)
			assert.Equal(t, "this agent uses as-needed values, which a restart cannot deliver yet; stop and start the agent instead", body.Error.Message)
			assert.NotContains(t, rec.Body.String(), failClosedKey, "the body names no key")
		})
	}
}

// H-25: the broker /restart of an agent without as-needed entries is a
// plain restart as before, on an old and on a capable broker.
func TestDispatchAgentRestart_PlainWhenNothingToRefuse(t *testing.T) {
	for _, capable := range []bool{false, true} {
		f, _ := newGatherStartFixture(t, capable, plainStarted, nil)
		require.NoError(t, f.d.DispatchAgentRestart(context.Background(), f.agent))
		assert.True(t, f.client.restartCalled, "capable=%v", capable)
	}
}

// The lifecycle restart is not refused (design r17 §9): on a capable
// broker its pre-check passes for an agent with as-needed entries, and its
// start leg gathers them.
func TestLifecycleRestart_CapableBrokerStillGathers(t *testing.T) {
	f, gc := newGatherStartFixture(t, true, replayDelivers(failClosedKey), nil)
	seedAsNeededEnv(t, f, failClosedKey, failClosedValue)
	broker, err := checkAgentPinnedPlacement(context.Background(), f.d.store, f.agent)
	require.NoError(t, err)
	require.NoError(t, f.d.RestartEnvGatherPrecheck(context.Background(), f.agent, broker))
	require.NoError(t, f.d.DispatchAgentStart(withRestartStartLeg(context.Background()), f.agent, "", false))
	require.Len(t, gc.calls, 2, "the needs report and the replay")
	assert.True(t, gc.calls[0].extras.GatherEnv)
	assert.Equal(t, failClosedValue, gc.calls[1].env[failClosedKey])
	assert.False(t, f.client.restartCalled, "not the broker restart")
}

// H-26 (design r17, L-2): the lifecycle restart pre-check's 412 through
// the real HTTP router, before the stop leg: an old broker and an agent
// with as-needed entries.
func TestLifecycleHTTP_RestartPrecheck412BeforeStop(t *testing.T) {
	srv, s := testServer(t)
	grantDevUserRuntimeBrokerAccess(t, s)
	project, broker := newEmptyPerAgentHandlerProject(t, s, "precheck-412", true)
	setBrokerCaps(t, s, broker, &store.BrokerCapabilities{PreserveWorkspaceCreate: true})
	agent := newQuotaTestAgent(t, s, broker, project, "precheck-412", state.PhaseRunning)
	require.NoError(t, s.CreateEnvVar(context.Background(), &store.EnvVar{
		ID: uuid.NewString(), Key: failClosedKey, Value: failClosedValue,
		Scope: store.ScopeProject, ScopeID: project.ID, InjectionMode: store.InjectionModeAsNeeded,
	}))
	client := &mockRuntimeBrokerClient{}
	srv.SetDispatcher(NewHTTPAgentDispatcherWithClient(s, client, false, slog.Default()))

	rec := doRequest(t, srv, http.MethodPost, "/api/v1/agents/"+agent.ID+"/restart", nil)
	require.Equal(t, http.StatusPreconditionFailed, rec.Code, rec.Body.String())
	var body ErrorResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, ErrCodeUnsupportedCapability, body.Error.Code)
	assert.Equal(t, errBrokerLacksStartEnvGather.Error(), body.Error.Message)
	assert.NotContains(t, rec.Body.String(), failClosedKey, "the body names no key")
	assert.False(t, client.stopCalled, "refused before the stop leg")
	assert.False(t, client.startCalled || client.restartCalled || client.createCalled, "nothing sent")
}

// H-21: an empty broker ID is refused by requireRuntimeBrokerAssigned on
// every entry point, before any capability read.
func TestStartEntryPoints_EmptyBrokerIDRefusedBeforeCapabilityRead(t *testing.T) {
	for _, ep := range startEntryPoints[:4] { // the owner nodes resolve the agent by its broker's rows
		t.Run(ep.name, func(t *testing.T) {
			reads := &atomic.Int32{}
			f, gc := newGatherStartFixture(t, true, plainStarted, func(s store.Store) store.Store {
				return countBrokerReadsStore{Store: s, reads: reads}
			})
			seedAsNeededEnv(t, f, failClosedKey, failClosedValue)
			f.agent.RuntimeBrokerID = ""
			err := ep.run(t, f)
			require.Error(t, err)
			assert.Equal(t, requireRuntimeBrokerAssigned(f.agent).Error(), err.Error(), "requireRuntimeBrokerAssigned's own error")
			assert.NotErrorIs(t, err, errBrokerLacksStartEnvGather)
			assert.Zero(t, reads.Load(), "the capability is never read")
			assert.Empty(t, gc.calls)
		})
	}
}

// H-23: a guard's own refusal wins over the old-broker 412.
func TestStartEntryPoints_GuardRefusalWinsOver412(t *testing.T) {
	errStanding := errors.New("FAKE standing refusal")
	for _, ep := range startEntryPoints[:4] {
		t.Run(ep.name+"/placement", func(t *testing.T) {
			f, gc := newGatherStartFixture(t, false, plainStarted, nil)
			seedAsNeededEnv(t, f, failClosedKey, failClosedValue)
			// A pin without its broker: an incomplete pin, refused by the
			// placement backstop.
			f.agent.PinnedRuntimeTargetID = "fake-target"
			err := ep.run(t, f)
			var conflict *PlacementConflictError
			require.ErrorAs(t, err, &conflict)
			assert.NotErrorIs(t, err, errBrokerLacksStartEnvGather, "the placement backstop answers first")
			assert.Empty(t, gc.calls)
		})
		t.Run(ep.name+"/standing", func(t *testing.T) {
			f, gc := newGatherStartFixture(t, false, plainStarted, nil)
			seedAsNeededEnv(t, f, failClosedKey, failClosedValue)
			f.d.requiredStandingCheck = func(context.Context, *store.Agent) error { return errStanding }
			err := ep.run(t, f)
			require.ErrorIs(t, err, ErrAgentNotInStanding)
			assert.NotErrorIs(t, err, errBrokerLacksStartEnvGather, "the standing guard answers first")
			assert.Empty(t, gc.calls)
		})
	}
}

// F2: the lifecycle restart pre-check's 503 through the HTTP API, before
// the stop leg: the server's own store cannot read the broker record (the
// request's pre-check reads it once; design r12).
func TestLifecycleHTTP_RestartPrecheck503BeforeStop(t *testing.T) {
	srv, s := testServer(t)
	wrapped, fault := installStoreFault(t, srv, brokerReadFaultWrap(errors.New("FAKE-broker-read-detail")))
	grantDevUserRuntimeBrokerAccess(t, s)
	project, broker := newEmptyPerAgentHandlerProject(t, s, "precheck-503", true)
	agent := newQuotaTestAgent(t, s, broker, project, "precheck-503", state.PhaseRunning)
	client := &mockRuntimeBrokerClient{}
	srv.SetDispatcher(NewHTTPAgentDispatcherWithClient(wrapped, client, false, slog.Default()))
	fault.Arm()

	rec := doRequest(t, srv, http.MethodPost, "/api/v1/agents/"+agent.ID+"/restart", nil)
	require.Equal(t, http.StatusServiceUnavailable, rec.Code, rec.Body.String())
	var body ErrorResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, ErrCodeUnavailable, body.Error.Code)
	assert.Equal(t, "the agent's runtime broker record could not be read; the agent was not restarted", body.Error.Message)
	assert.NotContains(t, rec.Body.String(), "FAKE-broker-read-detail")
	assert.False(t, client.stopCalled, "refused before the stop leg")
	assert.False(t, client.startCalled || client.restartCalled)
}

// rev-3 L3: the HTTP start's own pre-checks read the broker record on the
// server's store and pass; the dispatcher's start guards cannot read it.
// The start answers the same fixed 503, with nothing sent.
func TestLifecycleHTTP_StartGuardsBrokerReadFaultIs503(t *testing.T) {
	srv, s := testServer(t)
	grantDevUserRuntimeBrokerAccess(t, s)
	project, broker := newEmptyPerAgentHandlerProject(t, s, "guards-503", true)
	setBrokerCaps(t, s, broker, &store.BrokerCapabilities{StartEnvGather: true, PreserveWorkspaceCreate: true})
	agent := newQuotaTestAgent(t, s, broker, project, "guards-503", state.PhaseStopped)
	client := &mockRuntimeBrokerClient{}
	srv.SetDispatcher(NewHTTPAgentDispatcherWithClient(failBrokerReadStore{s}, client, false, slog.Default()))

	rec := doRequest(t, srv, http.MethodPost, "/api/v1/agents/"+agent.ID+"/start", nil)
	require.Equal(t, http.StatusServiceUnavailable, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), "the agent's runtime broker record could not be read; the agent was not started")
	assert.NotContains(t, rec.Body.String(), "FAKE-broker-read-detail")
	assert.False(t, client.startCalled || client.createCalled, "nothing sent")
}

// F2: the lifecycle start and restart answer a start env-gather failure
// with its own status, code and verb (handlers_agent_lifecycle.go mapping).
func TestLifecycleHTTP_StartEnvGatherErrors(t *testing.T) {
	cases := []struct {
		err    error
		status int
		code   string
	}{
		{errStartEnvUnmet, http.StatusUnprocessableEntity, ErrCodeMissingEnvVars},
		{errBrokerLacksStartEnvGather, http.StatusPreconditionFailed, ErrCodeUnsupportedCapability},
		{errBrokerRecordUnreadable, http.StatusServiceUnavailable, ErrCodeUnavailable},
	}
	for _, tc := range cases {
		for _, action := range []string{"start", "restart"} {
			t.Run(tc.code+"/"+action, func(t *testing.T) {
				disp := &skillFailDispatcher{startErr: tc.err}
				srv, s, project := setupCreateAgentServer(t, disp)
				agent := createLifecycleTestAgent(t, s, project, "lc-envgather-"+action, state.PhaseStopped)
				rec := doRequest(t, srv, http.MethodPost, "/api/v1/agents/"+agent.ID+"/"+action, nil)
				require.Equal(t, tc.status, rec.Code, rec.Body.String())
				var body ErrorResponse
				require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
				assert.Equal(t, tc.code, body.Error.Code)
				if tc.err == errStartEnvUnmet {
					verb := "started"
					if action == "restart" {
						verb = "restarted"
					}
					assert.Equal(t, startEnvUnmetMessage(action), body.Error.Message)
					assert.Contains(t, body.Error.Message, "the agent was not "+verb)
				}
			})
		}
	}
}

// F2: the create calls that start an existing agent in place answer a start
// env-gather failure with its own status and code.
func TestCreateAgent_ExistingAgentStartEnvGatherErrors(t *testing.T) {
	phases := []struct {
		name   string
		phase  state.Phase
		resume bool
	}{
		{name: "suspended", phase: state.PhaseSuspended},
		{name: "stopped resume", phase: state.PhaseStopped, resume: true},
		{name: "created", phase: state.PhaseCreated},
	}
	cases := []struct {
		err    error
		status int
		code   string
	}{
		{errStartEnvUnmet, http.StatusUnprocessableEntity, ErrCodeMissingEnvVars},
		{errBrokerLacksStartEnvGather, http.StatusPreconditionFailed, ErrCodeUnsupportedCapability},
		{errBrokerRecordUnreadable, http.StatusServiceUnavailable, ErrCodeUnavailable},
	}
	for _, tc := range cases {
		for _, ph := range phases {
			t.Run(tc.code+"/"+ph.name, func(t *testing.T) {
				disp := &skillFailDispatcher{startErr: tc.err}
				srv, s, project := setupCreateAgentServer(t, disp)
				agent := createLifecycleTestAgent(t, s, project, "existing-envgather", ph.phase)
				rec := doRequest(t, srv, http.MethodPost, "/api/v1/agents", CreateAgentRequest{
					Name: agent.Name, ProjectID: project.ID, Task: "x", Resume: ph.resume,
				})
				require.True(t, disp.startCalled, "the existing agent must have been started")
				require.Equal(t, tc.status, rec.Code, rec.Body.String())
				var body ErrorResponse
				require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
				assert.Equal(t, tc.code, body.Error.Code)
			})
		}
	}
}

// F4: the finalize records only the names of the keys it delivered: a
// reserved control-plane target it drops is not recorded.
func TestDispatchFinalizeEnv_UserSuppliedKeysExcludeReservedTargets(t *testing.T) {
	f := newRunDispatchFixture(t)
	_, err := f.d.DispatchFinalizeEnv(context.Background(), f.agent, map[string]string{
		"SCION_AUTH_TOKEN": "FAKE-reserved-value", "FAKE_USER_KEY": "FAKE-user-value",
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"FAKE_USER_KEY"}, f.agent.AppliedConfig.UserSuppliedEnvKeys)
}

// stopDefersClient defers every stop to the owner node.
type stopDefersClient struct {
	*gatherStartClient
}

func (c *stopDefersClient) StopAgent(context.Context, string, string, string, string, string) error {
	c.mockRuntimeBrokerClient.stopCalled = true
	return ErrLifecycleDeferred
}

// F5: the echo backstop stops the run with the stop dispatch, which hands
// the stop to the owner node when the broker is not connected here: a stop
// intent for the minted run is written, not only logged.
func TestStartGather_EchoBackstopStopIsHandedOff(t *testing.T) {
	f, gc := newGatherStartFixture(t, true, func(int, gatherStartCall) (*RemoteAgentResponse, error) {
		return startedAnswer(false), nil
	}, nil)
	client := &stopDefersClient{gatherStartClient: gc}
	f.d = NewHTTPAgentDispatcherWithClient(f.store, client, false, slog.Default())
	f.d.SetTokenGenerator(runTokenGenerator{svc: f.svc})
	f.d.SetCrossNodeDeps(NewChannelEventPublisher(), NoopCommandBus{})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	err := f.d.DispatchAgentStart(ctx, f.agent, "", false)
	require.ErrorIs(t, err, errBrokerLacksStartEnvGather)
	require.Len(t, gc.calls, 1)
	minted := gc.calls[0].extras.RunID

	pending, err := f.store.ListPendingDispatch(context.Background(), f.agent.RuntimeBrokerID)
	require.NoError(t, err)
	var stopArgs []string
	for _, row := range pending {
		if row.Op == "stop" && row.AgentID == f.agent.ID {
			stopArgs = append(stopArgs, row.Args)
		}
	}
	require.Len(t, stopArgs, 1, "a stop intent was handed to the owner node")
	var args StopDispatchArgs
	require.NoError(t, json.Unmarshal([]byte(stopArgs[0]), &args))
	assert.Equal(t, minted, args.RunID, "the stop targets the minted run")
}

// N2: the lifecycle restart pre-check answers a standing refusal as the
// start leg answers it (502 with the reason), before the stop leg.
func TestLifecycleHTTP_RestartPrecheckStandingRefusal(t *testing.T) {
	srv, s := testServer(t)
	grantDevUserRuntimeBrokerAccess(t, s)
	project, broker := newEmptyPerAgentHandlerProject(t, s, "precheck-standing", true)
	agent := newQuotaTestAgent(t, s, broker, project, "precheck-standing", state.PhaseRunning)
	client := &mockRuntimeBrokerClient{}
	d := NewHTTPAgentDispatcherWithClient(s, client, false, slog.Default())
	d.requiredStandingCheck = func(context.Context, *store.Agent) error { return errors.New("FAKE standing reason") }
	srv.SetDispatcher(d)

	rec := doRequest(t, srv, http.MethodPost, "/api/v1/agents/"+agent.ID+"/restart", nil)
	require.Equal(t, http.StatusBadGateway, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), "FAKE standing reason")
	assert.False(t, client.stopCalled, "refused before the stop leg")
	assert.False(t, client.startCalled || client.restartCalled)
}

// R2: the marker write leaves the stored user-supplied key names alone: it
// writes only the marker, whatever the in-memory copy holds.
func TestPersistConfigDelivered_KeepsUserSuppliedKeyNames(t *testing.T) {
	f := newRunDispatchFixture(t)
	stale, err := f.store.GetAgent(context.Background(), f.agent.ID)
	require.NoError(t, err)
	_, _, err = f.store.AddAgentUserSuppliedEnvKeys(context.Background(), f.agent.ID, []string{"FAKE_USER_KEY"})
	require.NoError(t, err)
	persistConfigDelivered(context.Background(), f.store, slog.Default(), stale)
	got, err := f.store.GetAgent(context.Background(), f.agent.ID)
	require.NoError(t, err)
	assert.True(t, got.AppliedConfig.ConfigDelivered)
	assert.Equal(t, []string{"FAKE_USER_KEY"}, got.AppliedConfig.UserSuppliedEnvKeys)
}

// R4: a routed start the create routing refuses (a broker without
// PreserveWorkspaceCreate) returns that refusal; it never falls through to
// the old-broker 412 or to a plain start.
func TestRoutedStartRefusal_DoesNotFallThrough(t *testing.T) {
	f, gc := newGatherStartFixture(t, false, plainStarted, nil)
	setBrokerCaps(t, f.store, &store.RuntimeBroker{ID: f.agent.RuntimeBrokerID}, &store.BrokerCapabilities{})
	neverStarted(f)
	seedAsNeededEnv(t, f, failClosedKey, failClosedValue)
	err := f.d.DispatchAgentStart(context.Background(), f.agent, "", false)
	require.ErrorIs(t, err, errBrokerLacksPreserveWorkspace)
	assert.NotErrorIs(t, err, errBrokerLacksStartEnvGather)
	assert.Empty(t, gc.calls, "no plain start")
	assert.False(t, f.client.createCalled, "nothing sent")
}

// H-24b (design r11 §0.3): with the server's own store failing every
// broker-record read, the HTTP start and restart of an agent with as-needed
// entries on a capable broker answer the fixed 503 naming the action, send
// nothing, and the restart does not stop the agent.
func TestLifecycleHTTP_ServerStoreBrokerReadFaultIs503(t *testing.T) {
	for _, action := range []string{"start", "restart"} {
		t.Run(action, func(t *testing.T) {
			srv, s := testServer(t)
			wrapped, fault := installStoreFault(t, srv, brokerReadFaultWrap(errors.New("FAKE-server-broker-read-detail")))
			grantDevUserRuntimeBrokerAccess(t, s)
			project, broker := newEmptyPerAgentHandlerProject(t, s, "h24b-"+action, true)
			setBrokerCaps(t, s, broker, &store.BrokerCapabilities{StartEnvGather: true, PreserveWorkspaceCreate: true})
			phase := state.PhaseStopped
			if action == "restart" {
				phase = state.PhaseRunning
			}
			agent := newQuotaTestAgent(t, s, broker, project, "h24b-"+action, phase)
			require.NoError(t, s.CreateEnvVar(context.Background(), &store.EnvVar{
				ID: uuid.NewString(), Key: failClosedKey, Value: failClosedValue,
				Scope: store.ScopeProject, ScopeID: project.ID, InjectionMode: store.InjectionModeAsNeeded,
			}))
			client := &mockRuntimeBrokerClient{}
			srv.SetDispatcher(NewHTTPAgentDispatcherWithClient(wrapped, client, false, slog.Default()))
			fault.Arm()

			rec := doRequest(t, srv, http.MethodPost, "/api/v1/agents/"+agent.ID+"/"+action, nil)
			require.Equal(t, http.StatusServiceUnavailable, rec.Code, rec.Body.String())
			verb := "started"
			if action == "restart" {
				verb = "restarted"
			}
			assert.Contains(t, rec.Body.String(), "the agent's runtime broker record could not be read; the agent was not "+verb)
			assert.NotContains(t, rec.Body.String(), "FAKE-server-broker-read-detail", "no store text in the body")
			assert.False(t, client.stopCalled, "nothing stopped")
			assert.False(t, client.startCalled || client.restartCalled || client.createCalled, "nothing sent")
		})
	}
}

// H-24b read count: a dispatch chain reads the broker record once (the
// start guards); the capability check and the sends, the env-gather replay
// included, reuse that record.
func TestStartGather_OneBrokerRecordReadPerDispatchChain(t *testing.T) {
	cases := []struct {
		name    string
		capable bool
		answer  func(int, gatherStartCall) (*RemoteAgentResponse, error)
		run     func(f *runDispatchFixture) error
		wantErr error
		noEntry bool // the agent has no as-needed entries
	}{
		{"capable start with needs and replay", true, replayDelivers(failClosedKey),
			func(f *runDispatchFixture) error {
				return f.d.DispatchAgentStart(context.Background(), f.agent, "", false)
			}, nil, false},
		{"broker restart (refused: as-needed entries, design r17 L-1)", true, plainStarted,
			func(f *runDispatchFixture) error { return f.d.DispatchAgentRestart(context.Background(), f.agent) }, errRestartGatherPending, false},
		{"broker restart sent (no as-needed entries)", true, plainStarted,
			func(f *runDispatchFixture) error {
				if err := f.d.DispatchAgentRestart(context.Background(), f.agent); err != nil {
					return err
				}
				if !f.client.restartCalled {
					return errors.New("the restart was not sent")
				}
				return nil
			}, nil, true},
		{"old broker 412", false, plainStarted,
			func(f *runDispatchFixture) error {
				return f.d.DispatchAgentStart(context.Background(), f.agent, "", true)
			}, errBrokerLacksStartEnvGather, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reads := &atomic.Int32{}
			f, _ := newGatherStartFixture(t, tc.capable, tc.answer, func(s store.Store) store.Store {
				return countBrokerReadsStore{Store: s, reads: reads}
			})
			if !tc.noEntry {
				seedAsNeededEnv(t, f, failClosedKey, failClosedValue)
			}
			err := tc.run(f)
			if tc.wantErr != nil {
				require.ErrorIs(t, err, tc.wantErr)
			} else {
				require.NoError(t, err)
			}
			assert.Equal(t, int32(1), reads.Load(), "one broker-record read for the dispatch chain")
		})
	}
}

// rev-3 L1: on a capable broker, a restart's start leg whose as-needed
// pass cannot read the stored env names the restart.
func TestStartGather_RestartLegResolutionErrorNamesTheRestart(t *testing.T) {
	armed := &atomic.Bool{}
	f, gc := newGatherStartFixture(t, true, func(int, gatherStartCall) (*RemoteAgentResponse, error) {
		armed.Store(true)
		return needsAnswer([]string{failClosedKey}), nil
	}, func(s store.Store) store.Store { return toggleEnvReadErrStore{Store: s, armed: armed} })
	err := f.d.DispatchAgentStart(withRestartStartLeg(context.Background()), f.agent, "", false)
	require.Error(t, err)
	var resErr *secretResolutionError
	require.ErrorAs(t, err, &resErr)
	assert.Equal(t, "restarted", resErr.Verb)
	assert.Equal(t, envReadFixedMessage("restarted"), err.Error())
	assert.Len(t, gc.calls, 1, "no replay")
}

// rev-3 L2: a start the broker answered without the envGathered echo
// crosses nodes as its own sentinel, and the requester reads it as acted
// on: not "not acted on", the outcome unconfirmed.
func TestStartEnvGatherEchoMissing_CrossNodeIsActedOn(t *testing.T) {
	local := fmt.Errorf("dispatch start: %w", startEnvGatherEchoMissingError{})
	require.False(t, isConfirmedStartNotActedOnError(local))
	row := &store.BrokerDispatch{Op: "start", Error: local.Error(), Result: dispatchFailureResult(local)}
	got := dispatchFailureError(row)
	assert.ErrorIs(t, got, errBrokerLacksStartEnvGather)
	assert.True(t, isStartEnvGatherEchoMissing(got), "the echo-missing kind crosses")
	assert.False(t, isConfirmedStartNotActedOnError(got), "the owner started a run: acted on")
	assert.Equal(t, startUnconfirmed, startOutcomeOf(got))
	// The pre-send refusal still crosses as not acted on.
	pre := fmt.Errorf("dispatch start: %w", errBrokerLacksStartEnvGather)
	preGot := dispatchFailureError(&store.BrokerDispatch{Op: "start", Error: pre.Error(), Result: dispatchFailureResult(pre)})
	assert.False(t, isStartEnvGatherEchoMissing(preGot))
	assert.True(t, isConfirmedStartNotActedOnError(preGot))
}

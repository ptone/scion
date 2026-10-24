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

// A start on a broker that reports startEnvGather gathers as-needed values
// (ptone/scion#6101): needs report, as-needed pass, replay under the same
// run, failing closed with nothing started.

// gatherStartCall is one StartAgent call the gatherStartClient received.
type gatherStartCall struct {
	task   string
	env    map[string]string
	extras StartExtras
}

// gatherStartClient is mockRuntimeBrokerClient with a scripted StartAgent:
// answer is called with the 1-based call number and the call.
type gatherStartClient struct {
	*mockRuntimeBrokerClient
	calls  []gatherStartCall
	answer func(n int, c gatherStartCall) (*RemoteAgentResponse, error)
}

func (c *gatherStartClient) StartAgent(ctx context.Context, brokerID, brokerEndpoint, agentID, projectID, task, projectPath, projectSlug, harnessConfig, harnessConfigID, harnessConfigHash string, resolvedEnv map[string]string, resolvedSecrets []ResolvedSecret, inlineConfig *api.ScionConfig, sharedDirs []api.SharedDir, sharedWorkspace, resume bool, extras StartExtras) (*RemoteAgentResponse, error) {
	env := make(map[string]string, len(resolvedEnv))
	for k, v := range resolvedEnv {
		env[k] = v
	}
	call := gatherStartCall{task: task, env: env, extras: extras}
	c.calls = append(c.calls, call)
	c.mockRuntimeBrokerClient.startCalled = true
	return c.answer(len(c.calls), call)
}

// startedAnswer is a broker's start answer; echo sets envGathered.
func startedAnswer(echo bool) *RemoteAgentResponse {
	return &RemoteAgentResponse{
		Agent:       &RemoteAgentInfo{ID: "run-agent", Name: "run-agent", Phase: string(state.PhaseRunning), ContainerStatus: "Up 1 second"},
		EnvGathered: echo,
	}
}

// needsAnswer is a broker's needs report.
func needsAnswer(needs []string, markerOnly ...string) *RemoteAgentResponse {
	return &RemoteAgentResponse{EnvNeeds: &RemoteEnvRequirementsResponse{EnvRequirements: true, Needs: needs, MarkerOnly: markerOnly}}
}

// newGatherStartFixture is a runDispatchFixture whose broker reports
// startEnvGather (capable) or not, with the dispatcher on a scripted client.
// Either way the broker reports PreserveWorkspaceCreate, so a broker without
// startEnvGather is one that #5959's create routing may use.
// st, when set, wraps the fixture's store for the dispatcher.
func newGatherStartFixture(t *testing.T, capable bool, answer func(n int, c gatherStartCall) (*RemoteAgentResponse, error), st func(store.Store) store.Store) (*runDispatchFixture, *gatherStartClient) {
	t.Helper()
	f := newRunDispatchFixture(t)
	setBrokerCaps(t, f.store, &store.RuntimeBroker{ID: f.agent.RuntimeBrokerID}, &store.BrokerCapabilities{StartEnvGather: capable, PreserveWorkspaceCreate: true})
	gc := &gatherStartClient{mockRuntimeBrokerClient: f.client, answer: answer}
	var ds store.Store = f.store
	if st != nil {
		ds = st(f.store)
	}
	f.d = NewHTTPAgentDispatcherWithClient(ds, gc, false, slog.Default())
	f.d.SetTokenGenerator(runTokenGenerator{svc: f.svc})
	return f, gc
}

// replayDelivers answers the first send with a needs report for key and the
// replay with a started answer carrying the echo.
func replayDelivers(key string, markerOnly ...string) func(int, gatherStartCall) (*RemoteAgentResponse, error) {
	return func(n int, _ gatherStartCall) (*RemoteAgentResponse, error) {
		if n == 1 {
			return needsAnswer([]string{key}, markerOnly...), nil
		}
		return startedAnswer(true), nil
	}
}

func TestStartGather_ReplayDeliversAsNeededValueUnderTheSameRun(t *testing.T) {
	f, gc := newGatherStartFixture(t, true, replayDelivers(failClosedKey), nil)
	seedAsNeededEnv(t, f, failClosedKey, failClosedValue)
	neverStarted(f) // a row without ConfigDelivered: not routed on a capable broker

	require.NoError(t, f.d.DispatchAgentStart(context.Background(), f.agent, "fake-task", false))
	require.Len(t, gc.calls, 2, "the needs report and the replay")
	first, replay := gc.calls[0], gc.calls[1]
	assert.True(t, first.extras.GatherEnv)
	assert.False(t, first.extras.EnvGatherFinal)
	assert.Empty(t, first.env[failClosedKey], "the first send carries no as-needed value")
	assert.True(t, replay.extras.GatherEnv)
	assert.True(t, replay.extras.EnvGatherFinal, "the replay is the final pass")
	assert.Equal(t, failClosedValue, replay.env[failClosedKey], "the as-needed value reaches the replay")
	assert.NotEmpty(t, first.extras.RunID)
	assert.Equal(t, first.extras.RunID, replay.extras.RunID, "the replay keeps the run")
	assert.Equal(t, "fake-task", first.task)
	assert.Equal(t, "fake-task", replay.task, "the replay keeps the task")
	assert.False(t, f.client.createCalled, "a gathering start never goes through create")
	assert.NotContains(t, f.agent.AppliedConfig.Env, failClosedKey, "nothing persists the value")
	assert.False(t, f.agent.AppliedConfig.ConfigDelivered, "the start does not write the create marker")
}

func TestStartGather_SatisfiedStartsInOneSend(t *testing.T) {
	f, gc := newGatherStartFixture(t, true, func(int, gatherStartCall) (*RemoteAgentResponse, error) {
		return startedAnswer(true), nil
	}, nil)
	require.NoError(t, f.d.DispatchAgentStart(context.Background(), f.agent, "", false))
	require.Len(t, gc.calls, 1)
	assert.True(t, gc.calls[0].extras.GatherEnv)
	assert.False(t, f.client.stopCalled)
}

// toggleEnvReadErrStore fails ListEnvVars once armed.
type toggleEnvReadErrStore struct {
	store.Store
	armed *atomic.Bool
}

func (s toggleEnvReadErrStore) ListEnvVars(ctx context.Context, filter store.EnvVarFilter) ([]store.EnvVar, error) {
	if s.armed.Load() {
		return nil, errors.New("FAKE-env-read-backend-detail")
	}
	return s.Store.ListEnvVars(ctx, filter)
}

func TestStartGather_ResolutionErrorFailsClosedWithStartedVerb(t *testing.T) {
	armed := &atomic.Bool{}
	f, gc := newGatherStartFixture(t, true, func(n int, _ gatherStartCall) (*RemoteAgentResponse, error) {
		// The stored env becomes unreadable after the first send, so only
		// the as-needed pass fails.
		armed.Store(true)
		return needsAnswer([]string{failClosedKey}), nil
	}, func(s store.Store) store.Store { return toggleEnvReadErrStore{Store: s, armed: armed} })
	f.agent.Phase = string(state.PhaseStopped)
	runBefore := f.agent.RunID

	err := f.d.DispatchAgentStart(context.Background(), f.agent, "", false)
	require.Error(t, err)
	var resErr *secretResolutionError
	require.ErrorAs(t, err, &resErr)
	assert.Equal(t, "started", resErr.Verb)
	assert.Equal(t, envReadFixedMessage("started"), err.Error())
	assert.Len(t, gc.calls, 1, "no replay")
	assert.True(t, isConfirmedStartNotActedOnError(err))
	got, gerr := f.store.GetAgent(context.Background(), f.agent.ID)
	require.NoError(t, gerr)
	assert.Equal(t, runBefore, got.RunID, "the minted run is reverted")
}

func TestStartGather_UnresolvedRequiredKeyFailsClosedKeyless(t *testing.T) {
	const unresolvable = "FAKE_UNRESOLVABLE_KEY"
	f, gc := newGatherStartFixture(t, true, func(int, gatherStartCall) (*RemoteAgentResponse, error) {
		return needsAnswer([]string{unresolvable}), nil
	}, nil)
	logs := &captureHandler{}
	f.d.log = slog.New(logs)
	f.agent.Phase = string(state.PhaseStopped)
	runBefore := f.agent.RunID

	err := f.d.DispatchAgentStart(context.Background(), f.agent, "", false)
	require.ErrorIs(t, err, errStartEnvUnmet)
	assert.NotContains(t, err.Error(), unresolvable)
	assert.Len(t, gc.calls, 1, "no replay")
	assert.True(t, isConfirmedStartNotActedOnError(err))
	got, gerr := f.store.GetAgent(context.Background(), f.agent.ID)
	require.NoError(t, gerr)
	assert.Equal(t, runBefore, got.RunID, "the minted run is reverted")

	recs := logs.records("start: required environment values unavailable; agent not started")
	require.Len(t, recs, 1, "the unmet-keys warning was logged")
	for k, v := range recs[0].attrs {
		assert.Contains(t, []string{"agent_id", "count"}, k, "the log carries only the agent and a count")
		assert.NotContains(t, fmt.Sprint(v), unresolvable)
	}
}

func TestStartGather_ReplayStillNeedingFailsClosed(t *testing.T) {
	f, gc := newGatherStartFixture(t, true, func(int, gatherStartCall) (*RemoteAgentResponse, error) {
		return needsAnswer([]string{failClosedKey}), nil
	}, nil)
	seedAsNeededEnv(t, f, failClosedKey, failClosedValue)

	err := f.d.DispatchAgentStart(context.Background(), f.agent, "", false)
	require.ErrorIs(t, err, errStartEnvUnmet)
	assert.Len(t, gc.calls, 2)
	assert.NotContains(t, err.Error(), failClosedKey)
}

func TestStartGather_MarkerOnlyUnresolvedStartsWithCountedWarning(t *testing.T) {
	const marker = "FAKE_MARKER_KEY"
	f, gc := newGatherStartFixture(t, true, replayDelivers(marker, marker), nil)
	ctx, warns := withDispatchWarnings(context.Background())

	require.NoError(t, f.d.DispatchAgentStart(ctx, f.agent, "", false))
	require.Len(t, gc.calls, 2, "the replay is sent with the marker unmet")
	assert.True(t, gc.calls[1].extras.EnvGatherFinal)
	require.Len(t, warns.Warnings(), 1)
	assert.Contains(t, warns.Warnings()[0], "1 optional environment value")
	assert.NotContains(t, warns.Warnings()[0], marker, "the warning names no key")
}

func TestStartGather_EchoMissingStopsTheRunAndFails412(t *testing.T) {
	// A broker whose recorded capability is stale starts without the
	// echo: the started run is stopped and the start fails, settled as a
	// start the broker acted on.
	f, gc := newGatherStartFixture(t, true, func(int, gatherStartCall) (*RemoteAgentResponse, error) {
		return startedAnswer(false), nil
	}, nil)

	err := f.d.DispatchAgentStart(context.Background(), f.agent, "", false)
	require.ErrorIs(t, err, errBrokerLacksStartEnvGather)
	require.Len(t, gc.calls, 1)
	assert.True(t, f.client.stopCalled, "the run started without env-gather is stopped")
	assert.Equal(t, gc.calls[0].extras.RunID, f.client.lastStopRunID, "the stop targets the minted run")
	assert.False(t, isConfirmedStartNotActedOnError(err), "the broker acted on the start")
	assert.Equal(t, startUnconfirmed, startOutcomeOf(err))
	got, gerr := f.store.GetAgent(context.Background(), f.agent.ID)
	require.NoError(t, gerr)
	assert.Equal(t, gc.calls[0].extras.RunID, got.RunID, "the minted run is kept, not reverted")

	rec := httptest.NewRecorder()
	require.True(t, writeStartEnvGatherError(rec, err, api.AgentActionStart))
	assert.Equal(t, http.StatusPreconditionFailed, rec.Code)
}

func TestStartGather_NoAuthAgentStartsWithoutStop(t *testing.T) {
	// The broker echoes a gatherEnv start also when noAuth skips its
	// preflight, so a NoAuth agent starts and nothing is stopped.
	f, gc := newGatherStartFixture(t, true, func(int, gatherStartCall) (*RemoteAgentResponse, error) {
		return startedAnswer(true), nil
	}, nil)
	f.agent.AppliedConfig.NoAuth = true

	require.NoError(t, f.d.DispatchAgentStart(context.Background(), f.agent, "", false))
	require.Len(t, gc.calls, 1)
	assert.True(t, gc.calls[0].extras.GatherEnv)
	assert.True(t, gc.calls[0].extras.NoAuth)
	assert.False(t, f.client.stopCalled)
}

func TestStartGather_AsyncOptInStillGathers(t *testing.T) {
	// An agent opted into asynchronous launches, with as-needed entries,
	// on a capable broker: the start is sent synchronously and gathers.
	f, gc := newGatherStartFixture(t, true, replayDelivers(failClosedKey), nil)
	setBrokerCaps(t, f.store, &store.RuntimeBroker{ID: f.agent.RuntimeBrokerID}, &store.BrokerCapabilities{StartEnvGather: true, AsyncLaunch: true, AsyncStart: true})
	f.d.SetAsyncLaunchSettingsProvider(func() AsyncLaunchSettings {
		return AsyncLaunchSettings{Enabled: true, Timeout: 5 * time.Minute, KeepaliveSeconds: 15}
	})
	neverStarted(f)
	f.agent.LaunchAsyncOptIn = true
	seedAsNeededEnv(t, f, failClosedKey, failClosedValue)

	res, err := f.d.DispatchAgentStartWithOptions(context.Background(), f.agent, "", false,
		StartDispatchOptions{Async: true, Kind: store.LaunchKindStart, PriorPhase: string(state.PhaseCreated)})
	require.NoError(t, err)
	assert.Nil(t, res.AcceptedLaunch(), "no asynchronous launch")
	require.Len(t, gc.calls, 2)
	for _, c := range gc.calls {
		assert.False(t, c.extras.AsyncLaunch, "sent synchronously")
		assert.Empty(t, c.extras.LaunchID)
	}
	assert.Equal(t, failClosedValue, gc.calls[1].env[failClosedKey])
	assert.False(t, f.client.createCalled)
}

func TestStartGather_OldBrokerAsyncRoutedAsBefore(t *testing.T) {
	// On a broker without the capability the #5959 routing is unchanged:
	// an undelivered row with as-needed entries goes through a synchronous
	// create, also when asked for an asynchronous launch.
	f, gc := newGatherStartFixture(t, false, func(int, gatherStartCall) (*RemoteAgentResponse, error) {
		t.Fatal("no start send expected")
		return nil, nil
	}, nil)
	neverStarted(f)
	f.agent.LaunchAsyncOptIn = true
	seedAsNeededEnv(t, f, failClosedKey, failClosedValue)
	gatherNeedsClient(f)

	res, err := f.d.DispatchAgentStartWithOptions(context.Background(), f.agent, "", false, StartDispatchOptions{Async: true, Kind: store.LaunchKindStart})
	require.NoError(t, err)
	assert.Nil(t, res.AcceptedLaunch())
	assert.Empty(t, gc.calls)
	require.NotNil(t, f.client.lastCreateReq)
	assert.Equal(t, failClosedValue, f.client.lastCreateReq.ResolvedEnv[failClosedKey])
}

func TestStartGather_OldBrokerRefusesUnroutedAsNeededStarts(t *testing.T) {
	cases := []struct {
		name  string
		setup func(f *runDispatchFixture)
		start func(f *runDispatchFixture) error
	}{
		{"delivered row", func(f *runDispatchFixture) { f.agent.AppliedConfig.ConfigDelivered = true },
			func(f *runDispatchFixture) error {
				return f.d.DispatchAgentStart(context.Background(), f.agent, "", false)
			}},
		{"resume", func(f *runDispatchFixture) {},
			func(f *runDispatchFixture) error {
				return f.d.DispatchAgentStart(context.Background(), f.agent, "", true)
			}},
		{"restart start leg", func(f *runDispatchFixture) {},
			func(f *runDispatchFixture) error {
				return f.d.DispatchAgentStart(withRestartStartLeg(context.Background()), f.agent, "", false)
			}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f, gc := newGatherStartFixture(t, false, func(int, gatherStartCall) (*RemoteAgentResponse, error) {
				return startedAnswer(false), nil
			}, nil)
			seedAsNeededEnv(t, f, failClosedKey, failClosedValue)
			tc.setup(f)
			runBefore := f.agent.RunID

			err := tc.start(f)
			require.ErrorIs(t, err, errBrokerLacksStartEnvGather)
			assert.Empty(t, gc.calls, "nothing sent")
			assert.False(t, f.client.createCalled, "not routed")
			assert.Equal(t, runBefore, f.agent.RunID, "nothing minted")
			assert.True(t, isConfirmedStartNotActedOnError(err))
		})
	}
	t.Run("no as-needed entries: plain start without gatherEnv", func(t *testing.T) {
		f, gc := newGatherStartFixture(t, false, func(int, gatherStartCall) (*RemoteAgentResponse, error) {
			return startedAnswer(false), nil
		}, nil)
		require.NoError(t, f.d.DispatchAgentStart(context.Background(), f.agent, "", true))
		require.Len(t, gc.calls, 1)
		assert.False(t, gc.calls[0].extras.GatherEnv)
	})
}

func TestStartGather_TZOnlyAsNeededIsNotGatherable(t *testing.T) {
	// TZ is never gathered, so an as-needed TZ entry alone neither routes
	// nor refuses a start on an old broker; with a real as-needed key next
	// to it, the start is routed (undelivered row) or refused (resume).
	seedTZ := func(t *testing.T, f *runDispatchFixture) {
		require.NoError(t, f.store.CreateEnvVar(context.Background(), &store.EnvVar{
			ID: uuid.NewString(), Key: agentTZEnvKey, Value: "Asia/Tokyo",
			Scope: store.ScopeProject, ScopeID: f.agent.ProjectID, InjectionMode: store.InjectionModeAsNeeded,
		}))
	}
	plain := func(int, gatherStartCall) (*RemoteAgentResponse, error) { return startedAnswer(false), nil }

	t.Run("TZ only, undelivered row: plain start", func(t *testing.T) {
		f, gc := newGatherStartFixture(t, false, plain, nil)
		neverStarted(f)
		seedTZ(t, f)
		require.NoError(t, f.d.DispatchAgentStart(context.Background(), f.agent, "", false))
		assert.Len(t, gc.calls, 1)
		assert.False(t, f.client.createCalled)
	})
	t.Run("TZ only, resume: plain start", func(t *testing.T) {
		f, gc := newGatherStartFixture(t, false, plain, nil)
		seedTZ(t, f)
		require.NoError(t, f.d.DispatchAgentStart(context.Background(), f.agent, "", true))
		assert.Len(t, gc.calls, 1)
	})
	t.Run("TZ and a real key, undelivered row: routed", func(t *testing.T) {
		f, gc := newGatherStartFixture(t, false, plain, nil)
		neverStarted(f)
		seedTZ(t, f)
		seedAsNeededEnv(t, f, failClosedKey, failClosedValue)
		gatherNeedsClient(f)
		require.NoError(t, f.d.DispatchAgentStart(context.Background(), f.agent, "", false))
		assert.Empty(t, gc.calls)
		assert.True(t, f.client.createCalled)
	})
	t.Run("TZ and a real key, resume: refused", func(t *testing.T) {
		f, gc := newGatherStartFixture(t, false, plain, nil)
		seedTZ(t, f)
		seedAsNeededEnv(t, f, failClosedKey, failClosedValue)
		err := f.d.DispatchAgentStart(context.Background(), f.agent, "", true)
		require.ErrorIs(t, err, errBrokerLacksStartEnvGather)
		assert.Empty(t, gc.calls)
	})
}

// failBrokerReadStore fails GetRuntimeBroker.
type failBrokerReadStore struct{ store.Store }

func (failBrokerReadStore) GetRuntimeBroker(context.Context, string) (*store.RuntimeBroker, error) {
	return nil, errors.New("FAKE-broker-read-detail")
}

func TestStartGather_CapabilityReadErrorFails503(t *testing.T) {
	f, gc := newGatherStartFixture(t, true, func(int, gatherStartCall) (*RemoteAgentResponse, error) {
		return startedAnswer(true), nil
	}, func(s store.Store) store.Store { return failBrokerReadStore{s} })
	runBefore := f.agent.RunID

	err := f.d.DispatchAgentStart(context.Background(), f.agent, "", false)
	require.ErrorIs(t, err, errBrokerRecordUnreadable)
	assert.Empty(t, gc.calls, "nothing sent, never a plain start")
	assert.False(t, f.client.createCalled)
	assert.Equal(t, runBefore, f.agent.RunID, "nothing minted")
	assert.True(t, isConfirmedStartNotActedOnError(err))
	rec := httptest.NewRecorder()
	require.True(t, writeStartEnvGatherError(rec, err, api.AgentActionStart))
	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
	assert.NotContains(t, rec.Body.String(), "FAKE-broker-read-detail")
}

func TestStartGather_BrokerRowNotFoundFails503(t *testing.T) {
	// A broker record that does not exist fails closed like a read error:
	// 503, nothing minted or sent, never a plain start (G-2a).
	f, gc := newGatherStartFixture(t, true, func(int, gatherStartCall) (*RemoteAgentResponse, error) {
		return startedAnswer(true), nil
	}, nil)
	f.agent.RuntimeBrokerID = tid("broker-missing")
	runBefore := f.agent.RunID

	err := f.d.DispatchAgentStart(context.Background(), f.agent, "", true)
	require.ErrorIs(t, err, errBrokerRecordUnreadable)
	assert.Empty(t, gc.calls)
	assert.False(t, f.client.createCalled)
	assert.Equal(t, runBefore, f.agent.RunID, "nothing minted")
}

func TestStartEnvGatherErrors_HandlerAnswers(t *testing.T) {
	cases := []struct {
		err    error
		action string
		status int
		code   string
		verb   string
	}{
		{errStartEnvUnmet, api.AgentActionStart, http.StatusUnprocessableEntity, ErrCodeMissingEnvVars, "started"},
		{errStartEnvUnmet, api.AgentActionRestart, http.StatusUnprocessableEntity, ErrCodeMissingEnvVars, "restarted"},
		{errBrokerLacksStartEnvGather, api.AgentActionStart, http.StatusPreconditionFailed, ErrCodeUnsupportedCapability, ""},
		{errBrokerRecordUnreadable, api.AgentActionStart, http.StatusServiceUnavailable, ErrCodeUnavailable, ""},
	}
	for _, tc := range cases {
		rec := httptest.NewRecorder()
		require.True(t, writeStartEnvGatherError(rec, tc.err, tc.action))
		assert.Equal(t, tc.status, rec.Code)
		var body ErrorResponse
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
		assert.Equal(t, tc.code, body.Error.Code)
		if tc.verb != "" {
			assert.Contains(t, body.Error.Message, "the agent was not "+tc.verb)
			assert.Contains(t, body.Error.Message, "Store each required value on the hub")
		}
	}
	assert.False(t, writeStartEnvGatherError(httptest.NewRecorder(), errors.New("other"), api.AgentActionStart))
}

func TestStartEnvGatherErrors_CrossNodeRoundTrip(t *testing.T) {
	for _, sentinel := range []error{errStartEnvUnmet, errBrokerLacksStartEnvGather, errBrokerRecordUnreadable} {
		execErr := errors.Join(errors.New("dispatch start"), sentinel)
		row := &store.BrokerDispatch{Op: "start", Error: execErr.Error(), Result: dispatchFailureResult(execErr)}
		got := dispatchFailureError(row)
		assert.ErrorIs(t, got, sentinel, "%v survives the hand-off", sentinel)
		rec := httptest.NewRecorder()
		require.True(t, writeStartEnvGatherError(rec, got, api.AgentActionRestart))
		if errors.Is(sentinel, errStartEnvUnmet) {
			assert.Contains(t, rec.Body.String(), "the agent was not restarted", "the requester's action picks the verb")
		}
	}
}

func TestStartTransports_ReadNeedsReport(t *testing.T) {
	needs := `{"agentId":"a","required":["K","M"],"hubHas":[],"needs":["K","M"],"markerOnly":["M"],"envRequirements":true}`
	started := `{"agent":{"id":"a","name":"a","status":"running"},"created":false,"envGathered":true}`
	check := func(t *testing.T, resp *RemoteAgentResponse, wantNeeds bool) {
		t.Helper()
		require.NotNil(t, resp)
		if wantNeeds {
			require.NotNil(t, resp.EnvNeeds, "a needs report is read as one")
			assert.Nil(t, resp.Agent, "a needs report is never a started agent")
			assert.Equal(t, []string{"K", "M"}, resp.EnvNeeds.Needs)
			assert.Equal(t, []string{"M"}, resp.EnvNeeds.MarkerOnly)
			return
		}
		assert.Nil(t, resp.EnvNeeds)
		require.NotNil(t, resp.Agent)
		assert.True(t, resp.EnvGathered)
	}
	t.Run("HTTP", func(t *testing.T) {
		for _, body := range []string{needs, started} {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusAccepted)
				_, _ = w.Write([]byte(body))
			}))
			client := NewHTTPRuntimeBrokerClient()
			resp, err := client.StartAgent(context.Background(), "b", srv.URL, "a", "p", "", "", "", "", "", "", nil, nil, nil, nil, false, false, StartExtras{GatherEnv: true})
			srv.Close()
			require.NoError(t, err)
			check(t, resp, body == needs)
		}
	})
	t.Run("hybrid and authenticated delegate", func(t *testing.T) {
		for _, body := range []string{needs, started} {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusAccepted)
				_, _ = w.Write([]byte(body))
			}))
			hybrid := NewHybridBrokerClient(nil, NewHTTPRuntimeBrokerClient(), nil, false)
			hybrid.SetStatelessLocalBrokers([]string{"b"}) // routed to HTTP
			auth := &AuthenticatedBrokerClient{transport: newBrokerHTTPTransport(false, nil)}
			for _, c := range []RuntimeBrokerClient{hybrid, auth} {
				resp, err := c.StartAgent(context.Background(), "b", srv.URL, "a", "p", "", "", "", "", "", "", nil, nil, nil, nil, false, false, StartExtras{GatherEnv: true})
				require.NoError(t, err)
				check(t, resp, body == needs)
			}
			srv.Close()
		}
	})
	t.Run("control channel", func(t *testing.T) {
		for _, body := range []string{needs, started} {
			runOverHubTunnels(t, func(t *testing.T, tr hubTunnelTransport) {
				tunnel := &mockControlChannelTunnel{connected: true, status: http.StatusAccepted, body: []byte(body)}
				client := &ControlChannelBrokerClient{manager: tr.wrap(t, tunnel)}
				resp, err := client.StartAgent(context.Background(), "b", "unused", "a", "p", "", "", "", "", "", "", nil, nil, nil, nil, false, false, StartExtras{GatherEnv: true})
				require.NoError(t, err)
				check(t, resp, body == needs)
			})
		}
	})
}

func TestStartExtras_GatherFieldsOnTheWire(t *testing.T) {
	payload := map[string]interface{}{}
	applyStartExtras(payload, StartExtras{
		GatherEnv: true, EnvGatherFinal: true, NoAuth: true, HarnessAuth: "api-key",
		RequiredSecrets:       []api.RequiredSecret{{Key: "K"}},
		AvailableAsNeededKeys: []string{"A"},
	})
	for _, k := range []string{"gatherEnv", "envGatherFinal", "noAuth", "harnessAuth", "requiredSecrets", "availableAsNeededKeys"} {
		assert.Contains(t, payload, k)
	}
	empty := map[string]interface{}{}
	applyStartExtras(empty, StartExtras{})
	for _, k := range []string{"gatherEnv", "envGatherFinal", "noAuth", "harnessAuth", "requiredSecrets", "availableAsNeededKeys"} {
		assert.NotContains(t, empty, k, "a start without gather sends none of the gather keys")
	}
}

func TestCapabilitiesFromStrings_StartEnvGather(t *testing.T) {
	assert.True(t, capabilitiesFromStrings([]string{"startEnvGather"}).StartEnvGather)
	assert.False(t, capabilitiesFromStrings([]string{"asyncStart"}).StartEnvGather)
}

func TestCrossNodeStart_CarriesTaskAndStaysSync(t *testing.T) {
	// The requester's capable start is handed to the owner node with the
	// start's task and without the asynchronous option; the owner's failure
	// comes back as the same sentinel.
	f := newCrossNodeFixture(t, nil, false)
	setBrokerCaps(t, f.store, &store.RuntimeBroker{ID: f.agent.RuntimeBrokerID}, &store.BrokerCapabilities{StartEnvGather: true, AsyncLaunch: true, AsyncStart: true})
	f.requester.SetAsyncLaunchSettingsProvider(func() AsyncLaunchSettings {
		return AsyncLaunchSettings{Enabled: true, Timeout: 5 * time.Minute, KeepaliveSeconds: 15}
	})

	done := make(chan struct{})
	var args *StartDispatchArgs
	go func() {
		defer close(done)
		row, ok := f.claimPending(t)
		if !ok {
			return
		}
		a, err := UnmarshalStartArgs(row.Args)
		if assert.NoError(t, err) {
			args = a
		}
		f.failRow(t, row.ID, errors.Join(errors.New("dispatch start"), errStartEnvUnmet))
	}()
	_, err := f.requester.DispatchAgentStartWithOptions(crossNodeCtx(t), f.agent, "fake-cross-node-task", false,
		StartDispatchOptions{Async: true, Kind: store.LaunchKindStart, PriorPhase: string(state.PhaseStopped)})
	<-done
	require.NotNil(t, args)
	assert.Equal(t, "fake-cross-node-task", args.Task, "the start's task crosses nodes")
	assert.False(t, args.Async, "the gathering start stays synchronous on the owner")
	assert.True(t, args.NoRoute, "the requester decided the routing")
	require.ErrorIs(t, err, errStartEnvUnmet)
}

func TestStartGather_UserSuppliedMarkerKeyIsRequired(t *testing.T) {
	// G-7: a marker-only key the user supplied at create (recorded by name
	// in UserSuppliedEnvKeys) is required at start: unmet, the start fails
	// with the keyless 422 and no replay. Not supplied, it stays optional.
	const marker = "FAKE_USER_MARKER_KEY"
	t.Run("user supplied: 422, no replay", func(t *testing.T) {
		f, gc := newGatherStartFixture(t, true, replayDelivers(marker, marker), nil)
		f.agent.AppliedConfig.UserSuppliedEnvKeys = []string{marker}
		err := f.d.DispatchAgentStart(context.Background(), f.agent, "", false)
		require.ErrorIs(t, err, errStartEnvUnmet)
		assert.Len(t, gc.calls, 1, "no replay")
		rec := httptest.NewRecorder()
		require.True(t, writeStartEnvGatherError(rec, err, api.AgentActionRestart))
		assert.Equal(t, http.StatusUnprocessableEntity, rec.Code)
		assert.NotContains(t, rec.Body.String(), marker, "the 422 body names no key")
	})
	t.Run("supplied under an alternative name: 422", func(t *testing.T) {
		f, gc := newGatherStartFixture(t, true, func(n int, _ gatherStartCall) (*RemoteAgentResponse, error) {
			r := needsAnswer([]string{marker}, marker)
			r.EnvNeeds.Alternatives = map[string][]string{marker: {"FAKE_USER_MARKER_ALT"}}
			return r, nil
		}, nil)
		f.agent.AppliedConfig.UserSuppliedEnvKeys = []string{"FAKE_USER_MARKER_ALT"}
		require.ErrorIs(t, f.d.DispatchAgentStart(context.Background(), f.agent, "", false), errStartEnvUnmet)
		assert.Len(t, gc.calls, 1)
	})
	t.Run("not user supplied: started with the count warning", func(t *testing.T) {
		f, gc := newGatherStartFixture(t, true, replayDelivers(marker, marker), nil)
		f.agent.AppliedConfig.UserSuppliedEnvKeys = []string{"FAKE_OTHER_KEY"}
		ctx, warns := withDispatchWarnings(context.Background())
		require.NoError(t, f.d.DispatchAgentStart(ctx, f.agent, "", false))
		assert.Len(t, gc.calls, 2)
		assert.Len(t, warns.Warnings(), 1)
	})
}

func TestDispatchFinalizeEnv_RecordsUserSuppliedKeyNamesOnly(t *testing.T) {
	// G-7 condition (a): the finalize records the submitted key names, and
	// no submitted value reaches the applied config, the stored row or the
	// hub log.
	const sentinel = "FAKE-user-supplied-value-sentinel"
	f := newRunDispatchFixture(t)
	logs := &captureHandler{}
	f.d.log = slog.New(logs)
	_, err := f.d.DispatchFinalizeEnv(context.Background(), f.agent, map[string]string{
		"FAKE_USER_KEY_B": sentinel, "FAKE_USER_KEY_A": sentinel + "-2", agentTZEnvKey: "Asia/Tokyo",
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"FAKE_USER_KEY_A", "FAKE_USER_KEY_B"}, f.agent.AppliedConfig.UserSuppliedEnvKeys,
		"sorted names, TZ excluded")

	applied, err := json.Marshal(f.agent.AppliedConfig)
	require.NoError(t, err)
	assert.NotContains(t, string(applied), sentinel, "no value in the applied config")

	require.NoError(t, f.store.UpdateAgent(context.Background(), f.agent))
	stored, err := f.store.GetAgent(context.Background(), f.agent.ID)
	require.NoError(t, err)
	assert.Equal(t, []string{"FAKE_USER_KEY_A", "FAKE_USER_KEY_B"}, stored.AppliedConfig.UserSuppliedEnvKeys)
	row, err := json.Marshal(stored)
	require.NoError(t, err)
	assert.NotContains(t, string(row), sentinel, "no value in the stored row")

	logs.mu.Lock()
	defer logs.mu.Unlock()
	for _, r := range logs.recs {
		assert.NotContains(t, r.msg, sentinel)
		for _, v := range r.attrs {
			assert.NotContains(t, fmt.Sprint(v), sentinel, "no value in the hub log")
		}
	}

	// A later finalize replaces the list.
	_, err = f.d.DispatchFinalizeEnv(context.Background(), f.agent, map[string]string{"FAKE_USER_KEY_C": sentinel})
	require.NoError(t, err)
	assert.Equal(t, []string{"FAKE_USER_KEY_C"}, f.agent.AppliedConfig.UserSuppliedEnvKeys)
}

func TestStartGather_ReplayHandOffAndTransportError(t *testing.T) {
	// H-20: a replay that the client hands to the owner node is deferred
	// like any start (the owner gathers again; the intent carries no env);
	// a replay that fails in transport is not taken as "not acted on".
	t.Run("replay deferred", func(t *testing.T) {
		f, gc := newGatherStartFixture(t, true, func(n int, _ gatherStartCall) (*RemoteAgentResponse, error) {
			if n == 1 {
				return needsAnswer([]string{failClosedKey}), nil
			}
			return nil, ErrLifecycleDeferred
		}, nil)
		seedAsNeededEnv(t, f, failClosedKey, failClosedValue)
		ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		defer cancel()
		err := f.d.DispatchAgentStart(ctx, f.agent, "fake-task", false)
		require.Error(t, err, "no owner answers in this fixture")
		assert.Len(t, gc.calls, 2)
		pending, lerr := f.store.ListPendingDispatch(context.Background(), f.agent.RuntimeBrokerID)
		require.NoError(t, lerr)
		for _, row := range pending {
			assert.NotContains(t, row.Args, failClosedValue, "the hand-off carries no env value")
		}
	})
	t.Run("replay transport error", func(t *testing.T) {
		transportErr := errors.New("FAKE replay read timeout")
		f, _ := newGatherStartFixture(t, true, func(n int, _ gatherStartCall) (*RemoteAgentResponse, error) {
			if n == 1 {
				return needsAnswer([]string{failClosedKey}), nil
			}
			return nil, transportErr
		}, nil)
		seedAsNeededEnv(t, f, failClosedKey, failClosedValue)
		err := f.d.DispatchAgentStart(context.Background(), f.agent, "", false)
		require.ErrorIs(t, err, transportErr)
		assert.False(t, isConfirmedStartNotActedOnError(err), "an ambiguous replay failure keeps today's classification")
	})
}

func TestStartGather_ResumeOnCapableBrokerGathersOrFailsKeyless(t *testing.T) {
	// H-19 (design r5 C2): a resume on a capable broker gathers too, and
	// can now fail with the keyless 422 where it used to start without the
	// values; nothing is started and no launch is begun.
	f, gc := newGatherStartFixture(t, true, func(int, gatherStartCall) (*RemoteAgentResponse, error) {
		return needsAnswer([]string{"FAKE_UNRESOLVABLE_KEY"}), nil
	}, nil)
	f.agent.LaunchAsyncOptIn = true
	res, err := f.d.DispatchAgentStartWithOptions(context.Background(), f.agent, "", true,
		StartDispatchOptions{Async: true, Kind: store.LaunchKindStart, PriorPhase: string(state.PhaseSuspended)})
	require.ErrorIs(t, err, errStartEnvUnmet)
	assert.Nil(t, res.AcceptedLaunch())
	require.Len(t, gc.calls, 1)
	assert.True(t, gc.calls[0].extras.GatherEnv)
	assert.False(t, gc.calls[0].extras.AsyncLaunch)
}

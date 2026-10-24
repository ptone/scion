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
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/GoogleCloudPlatform/scion/pkg/agent/state"
	"github.com/GoogleCloudPlatform/scion/pkg/store"
	"github.com/GoogleCloudPlatform/scion/pkg/store/entadapter"
	"github.com/GoogleCloudPlatform/scion/pkg/store/enttest"
)

// A start routed through create-with-gather (ptone/scion#6096) never clears
// the agent's workspace: every create request it sends carries
// PreserveWorkspace, it is sent only to a broker that advertises
// PreserveWorkspaceCreate (otherwise refused, nothing sent), and a
// delivered routed start records ConfigDelivered on the stored row, writing
// only the marker, so the next start is plain.

// grantPreserveWorkspaceCreate gives brokerID's stored row the
// PreserveWorkspaceCreate capability. A broker with no capabilities gets
// the ones a nil set already implies (attach and asynchronous launch and
// start), so nothing else about it changes.
func grantPreserveWorkspaceCreate(t *testing.T, s store.Store, brokerID string) {
	t.Helper()
	ctx := context.Background()
	b, err := s.GetRuntimeBroker(ctx, brokerID)
	require.NoError(t, err)
	if b.Capabilities == nil {
		b.Capabilities = &store.BrokerCapabilities{Attach: true, AsyncLaunch: true, AsyncStart: true}
	}
	b.Capabilities.PreserveWorkspaceCreate = true
	require.NoError(t, s.UpdateRuntimeBroker(ctx, b))
}

// recordCreates makes f's broker behave like gatherNeedsClient and returns
// every create request it receives.
func recordCreates(f *runDispatchFixture) *[]*RemoteCreateAgentRequest {
	var reqs []*RemoteCreateAgentRequest
	f.client.createWithGatherFunc = func(_ context.Context, _, _ string, req *RemoteCreateAgentRequest) (*RemoteAgentResponse, *RemoteEnvRequirementsResponse, error) {
		reqs = append(reqs, req)
		if req.ResolvedEnv[failClosedKey] != "" {
			return &RemoteAgentResponse{Agent: &RemoteAgentInfo{ID: req.ID, Slug: req.Slug, Name: req.Name},
				Created: true, Reprovisioned: req.Reprovision}, nil, nil
		}
		return nil, &RemoteEnvRequirementsResponse{AgentID: req.ID, Needs: []string{failClosedKey}}, nil
	}
	return &reqs
}

// assertAllPreserve checks that the routed start sent its needs report and
// replay, each with PreserveWorkspace, and no plain start.
func assertAllPreserve(t *testing.T, f *runDispatchFixture, reqs []*RemoteCreateAgentRequest) {
	t.Helper()
	require.Len(t, reqs, 2, "the needs report and the replay")
	for i, req := range reqs {
		assert.True(t, req.PreserveWorkspace, "create request %d must keep the workspace", i)
		assert.False(t, req.ProvisionOnly, "create request %d starts the agent", i)
		assert.False(t, req.Reprovision, "create request %d is not a reprovision", i)
	}
	assert.Equal(t, failClosedValue, reqs[1].ResolvedEnv[failClosedKey], "the as-needed value is delivered")
	assert.False(t, f.client.startCalled, "no plain start")
}

func TestRoutedStart_SendsPreserveWorkspaceOnEveryPath(t *testing.T) {
	t.Run("stopped row from before the marker", func(t *testing.T) {
		f := newRunDispatchFixture(t)
		f.agent.Phase = string(state.PhaseStopped)
		require.NotEmpty(t, f.agent.RunID)
		seedAsNeededEnv(t, f, failClosedKey, failClosedValue)
		grantPreserveWorkspaceCreate(t, f.store, f.agent.RuntimeBrokerID)
		reqs := recordCreates(f)

		require.NoError(t, f.d.DispatchAgentStart(context.Background(), f.agent, "", false))
		assertAllPreserve(t, f, *reqs)
	})
	t.Run("asynchronously created row", func(t *testing.T) {
		// An accepted asynchronous create launch does not mark the config
		// delivered, so the agent's next start is routed.
		require.False(t, createDelivered(&CreateDispatchResult{Launch: &LaunchAccepted{ID: "fake-launch"}}))
		f := newRunDispatchFixture(t)
		neverStarted(f)
		f.agent.Phase = string(state.PhaseStopped)
		f.agent.LaunchAsyncOptIn = true
		seedAsNeededEnv(t, f, failClosedKey, failClosedValue)
		grantPreserveWorkspaceCreate(t, f.store, f.agent.RuntimeBrokerID)
		reqs := recordCreates(f)

		require.NoError(t, f.d.DispatchAgentStart(context.Background(), f.agent, "", false))
		assertAllPreserve(t, f, *reqs)
	})
	t.Run("reincarnation start leg after the reprovision", func(t *testing.T) {
		f := newRunDispatchFixture(t)
		f.agent.Phase = string(state.PhaseStopped)
		f.agent.AppliedConfig.ConfigDelivered = true
		seedAsNeededEnv(t, f, failClosedKey, failClosedValue)
		grantPreserveWorkspaceCreate(t, f.store, f.agent.RuntimeBrokerID)
		reqs := recordCreates(f)

		require.NoError(t, f.d.DispatchAgentReprovision(context.Background(), f.agent))
		for _, req := range *reqs {
			assert.False(t, req.PreserveWorkspace, "the reprovision itself does not carry the flag")
		}
		*reqs = nil
		require.NoError(t, f.d.DispatchAgentStart(context.Background(), f.agent, "[SCION REINCARNATION] fake preamble", false))
		assertAllPreserve(t, f, *reqs)
		require.NotNil(t, (*reqs)[1].Config)
		assert.Equal(t, "[SCION REINCARNATION] fake preamble", (*reqs)[1].Config.Task, "the start leg's task reaches the create")
	})
}

// Through the HTTP lifecycle API with the real dispatcher: the routed start
// carries PreserveWorkspace and persists the marker, and the next start of
// the stored row is plain.
func TestLifecycleHTTP_RoutedStartPreservesAndPersistsMarker(t *testing.T) {
	srv, s := testServer(t)
	grantDevUserRuntimeBrokerAccess(t, s)
	project, broker := newEmptyPerAgentHandlerProject(t, s, "route-preserve", true)
	grantPreserveWorkspaceCreate(t, s, broker.ID)
	agent := newQuotaTestAgent(t, s, broker, project, "route-preserve", state.PhaseStopped)
	require.NoError(t, s.CreateEnvVar(context.Background(), &store.EnvVar{
		ID: uuid.NewString(), Key: failClosedKey, Value: failClosedValue,
		Scope: store.ScopeProject, ScopeID: project.ID, InjectionMode: store.InjectionModeAsNeeded,
	}))
	client := &mockRuntimeBrokerClient{}
	srv.SetDispatcher(NewHTTPAgentDispatcherWithClient(s, client, false, slog.Default()))

	rec := doRequest(t, srv, http.MethodPost, "/api/v1/agents/"+agent.ID+"/start", nil)
	require.Less(t, rec.Code, 300, rec.Body.String())
	require.True(t, client.createCalled, "the start went through create-with-gather")
	assert.True(t, client.lastCreateReq.PreserveWorkspace, "the routed create keeps the workspace")
	assert.False(t, client.startCalled)

	stored, err := s.GetAgent(context.Background(), agent.ID)
	require.NoError(t, err)
	require.NotNil(t, stored.AppliedConfig)
	assert.True(t, stored.AppliedConfig.ConfigDelivered, "the delivered routed start persisted the marker")
}

// An old broker (no capabilities, or capabilities without
// PreserveWorkspaceCreate) would ignore the flag and clear the workspace:
// the routed start is refused with the fixed upgrade message and nothing is
// sent.
func TestRoutedStart_BrokerWithoutCapabilityIsRefused(t *testing.T) {
	for _, tc := range []struct {
		name string
		caps *store.BrokerCapabilities
	}{
		{"no capabilities", nil},
		{"capabilities without preserveWorkspaceCreate", &store.BrokerCapabilities{Attach: true, AsyncLaunch: true, AsyncStart: true, Reprovision: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newRunDispatchFixture(t)
			neverStarted(f)
			seedAsNeededEnv(t, f, failClosedKey, failClosedValue)
			b, err := f.store.GetRuntimeBroker(context.Background(), f.agent.RuntimeBrokerID)
			require.NoError(t, err)
			b.Capabilities = tc.caps
			require.NoError(t, f.store.UpdateRuntimeBroker(context.Background(), b))
			recordCreates(f)

			err = f.d.DispatchAgentStart(context.Background(), f.agent, "", false)
			require.ErrorIs(t, err, errBrokerLacksPreserveWorkspace)
			assert.Equal(t, errBrokerLacksPreserveWorkspace.Error(), err.Error(), "the fixed message")
			assert.False(t, f.client.createCalled, "no create is sent")
			assert.False(t, f.client.startCalled, "no plain start")
			stored, err := f.store.GetAgent(context.Background(), f.agent.ID)
			require.NoError(t, err)
			assert.False(t, stored.AppliedConfig.ConfigDelivered)
		})
	}
}

// After a delivered routed start the stored row carries the marker, so a
// start of the reloaded row is plain.
func TestRoutedStart_PersistsMarkerSoNextStartIsPlain(t *testing.T) {
	f := newRunDispatchFixture(t)
	neverStarted(f)
	seedAsNeededEnv(t, f, failClosedKey, failClosedValue)
	grantPreserveWorkspaceCreate(t, f.store, f.agent.RuntimeBrokerID)
	reqs := recordCreates(f)

	require.NoError(t, f.d.DispatchAgentStart(context.Background(), f.agent, "", false))
	require.Len(t, *reqs, 2)

	stored, err := f.store.GetAgent(context.Background(), f.agent.ID)
	require.NoError(t, err)
	require.True(t, stored.AppliedConfig.ConfigDelivered, "the marker is on the stored row")

	f.client.createCalled = false
	require.NoError(t, f.d.DispatchAgentStart(context.Background(), stored, "", false))
	assert.True(t, f.client.startCalled, "the second start is plain")
	assert.False(t, f.client.createCalled, "the second start is not routed")
	assert.Len(t, *reqs, 2, "no further create")
}

// concurrentConfigStore runs mutate once, just before the first marker
// write (an UpdateAgentConfig setting ConfigDelivered) it forwards: a config
// update that lands between the marker write's read and its compare-and-set.
type concurrentConfigStore struct {
	store.Store
	mutate func()
	done   bool
}

func (s *concurrentConfigStore) UpdateAgentConfig(ctx context.Context, agentID string, expectedVersion int64, applied *store.AgentAppliedConfig, pending *store.AgentPendingConfig) (int64, error) {
	if !s.done && applied != nil && applied.ConfigDelivered {
		s.done = true
		s.mutate()
	}
	return s.Store.UpdateAgentConfig(ctx, agentID, expectedVersion, applied, pending)
}

// The marker write writes only the marker: a config update that lands
// during the routed start is kept, and the start's task override (held on
// the in-memory copy for the dispatch only) is never written.
func TestRoutedStart_MarkerWriteKeepsConcurrentConfigUpdate(t *testing.T) {
	f := newRunDispatchFixture(t)
	neverStarted(f)
	seedAsNeededEnv(t, f, failClosedKey, failClosedValue)
	grantPreserveWorkspaceCreate(t, f.store, f.agent.RuntimeBrokerID)
	recordCreates(f)
	base := f.store
	f.d.store = &concurrentConfigStore{Store: base, mutate: func() {
		cur, err := base.GetAgent(context.Background(), f.agent.ID)
		require.NoError(t, err)
		cur.AppliedConfig.Profile = "fake-concurrent-profile"
		_, err = base.UpdateAgentConfig(context.Background(), cur.ID, cur.StateVersion, cur.AppliedConfig, cur.PendingConfig)
		require.NoError(t, err)
	}}

	require.NoError(t, f.d.DispatchAgentStart(context.Background(), f.agent, "fake-start-task", false))

	stored, err := base.GetAgent(context.Background(), f.agent.ID)
	require.NoError(t, err)
	assert.True(t, stored.AppliedConfig.ConfigDelivered, "the marker landed after the conflict")
	assert.Equal(t, "fake-concurrent-profile", stored.AppliedConfig.Profile, "the concurrent update is kept")
	assert.Equal(t, "task", stored.AppliedConfig.Task, "the start's task override is not written")
	assert.Equal(t, "task", f.agent.AppliedConfig.Task, "the in-memory override is restored")
}

// The caller's state version follows the marker write only when the
// caller's copy was the version written over; a stale copy keeps its
// version, so the caller's own compare-and-set still sees the other write.
func TestPersistConfigDelivered_StateVersion(t *testing.T) {
	t.Run("current copy follows", func(t *testing.T) {
		f := newRunDispatchFixture(t)
		cur, err := f.store.GetAgent(context.Background(), f.agent.ID)
		require.NoError(t, err)
		persistConfigDelivered(context.Background(), f.store, slog.Default(), cur)
		stored, err := f.store.GetAgent(context.Background(), f.agent.ID)
		require.NoError(t, err)
		assert.True(t, stored.AppliedConfig.ConfigDelivered)
		assert.Equal(t, stored.StateVersion, cur.StateVersion)
	})
	t.Run("stale copy keeps its version", func(t *testing.T) {
		f := newRunDispatchFixture(t)
		stale, err := f.store.GetAgent(context.Background(), f.agent.ID)
		require.NoError(t, err)
		staleVersion := stale.StateVersion
		cur, err := f.store.GetAgent(context.Background(), f.agent.ID)
		require.NoError(t, err)
		cur.AppliedConfig.Profile = "fake-other-write"
		_, err = f.store.UpdateAgentConfig(context.Background(), cur.ID, cur.StateVersion, cur.AppliedConfig, cur.PendingConfig)
		require.NoError(t, err)

		persistConfigDelivered(context.Background(), f.store, slog.Default(), stale)
		stored, err := f.store.GetAgent(context.Background(), f.agent.ID)
		require.NoError(t, err)
		assert.True(t, stored.AppliedConfig.ConfigDelivered)
		assert.Equal(t, "fake-other-write", stored.AppliedConfig.Profile)
		assert.Equal(t, staleVersion, stale.StateVersion, "a stale copy does not follow")
	})
}

// Across nodes: a routed start's preserve flag and task reach the owner's
// create; the owner records the marker on the stored row after a delivered
// routed create (and only then), and refuses the create, nothing sent, when
// the broker lacks the capability.
func TestCrossNode_RoutedStartCarriesPreserveWorkspaceAndTask(t *testing.T) {
	setup := func(t *testing.T) *crossNodeFixture {
		const hubID = "hub-routed-preserve"
		f := newCrossNodeFixture(t, nil, true)
		f.requester.client = &mockRuntimeBrokerClient{returnErr: ErrLifecycleDeferred}
		f.requester.SetHubID(hubID)
		require.NoError(t, f.store.CreateEnvVar(context.Background(), &store.EnvVar{
			ID: uuid.NewString(), Key: failClosedKey, Value: failClosedValue,
			Scope: store.ScopeHub, ScopeID: hubID, InjectionMode: store.InjectionModeAsNeeded,
		}))
		return f
	}
	execCreate := func(t *testing.T, f *crossNodeFixture, args *CreateWithGatherDispatchArgs) error {
		raw, err := json.Marshal(args)
		require.NoError(t, err)
		_, err = f.owner.execDispatchCreate(context.Background(), store.BrokerDispatch{AgentID: f.agent.ID, Args: string(raw)})
		return err
	}
	storedMarker := func(t *testing.T, f *crossNodeFixture) bool {
		stored, err := f.store.GetAgent(context.Background(), f.agent.ID)
		require.NoError(t, err)
		return stored.AppliedConfig != nil && stored.AppliedConfig.ConfigDelivered
	}
	t.Run("flag and task cross", func(t *testing.T) {
		f := setup(t)
		grantPreserveWorkspaceCreate(t, f.store, f.agent.RuntimeBrokerID)

		_ = f.requester.DispatchAgentStart(crossNodeCtx(t), f.agent, "fake-routed-task", false)
		f.ownerDisp.recMu.Lock()
		preserve, task, ran := f.ownerDisp.createPreserveWorkspace, f.ownerDisp.createTask, f.ownerDisp.createAsyncOptIn != nil
		f.ownerDisp.recMu.Unlock()
		require.True(t, ran, "the routed start reached the owner as a create")
		assert.True(t, preserve, "the owner's create keeps the workspace")
		assert.Equal(t, "fake-routed-task", task, "the start's task reaches the owner's create")
		stored, err := f.store.GetAgent(context.Background(), f.agent.ID)
		require.NoError(t, err)
		if stored.AppliedConfig != nil {
			assert.NotEqual(t, "fake-routed-task", stored.AppliedConfig.Task, "the task override is not stored")
		}
	})
	t.Run("the owner persists the marker after a routed create", func(t *testing.T) {
		f := setup(t)
		grantPreserveWorkspaceCreate(t, f.store, f.agent.RuntimeBrokerID)
		require.NoError(t, execCreate(t, f, &CreateWithGatherDispatchArgs{Synchronous: true, PreserveWorkspace: true, Task: "fake-routed-task"}))
		assert.True(t, storedMarker(t, f), "the owner recorded the marker")
	})
	t.Run("a plain cross-node create leaves the row to the requester", func(t *testing.T) {
		f := setup(t)
		grantPreserveWorkspaceCreate(t, f.store, f.agent.RuntimeBrokerID)
		require.NoError(t, execCreate(t, f, &CreateWithGatherDispatchArgs{}))
		assert.False(t, storedMarker(t, f), "no owner write for a create that is not a routed start")
		f.ownerDisp.recMu.Lock()
		defer f.ownerDisp.recMu.Unlock()
		assert.False(t, f.ownerDisp.createPreserveWorkspace)
	})
	t.Run("the owner refuses without the capability", func(t *testing.T) {
		f := setup(t)
		err := execCreate(t, f, &CreateWithGatherDispatchArgs{Synchronous: true, PreserveWorkspace: true, Task: "fake-routed-task"})
		require.ErrorIs(t, err, errBrokerLacksPreserveWorkspace)
		assert.False(t, storedMarker(t, f))
		f.ownerDisp.recMu.Lock()
		defer f.ownerDisp.recMu.Unlock()
		assert.Nil(t, f.ownerDisp.createAsyncOptIn, "no create ran on the owner")
	})
}

// Through the HTTP lifecycle API: a routed start on a broker without the
// capability answers 412 with the fixed, keyless upgrade message, sends
// nothing, and leaves the agent stopped.
func TestLifecycleHTTP_RoutedStartOnOldBrokerIs412(t *testing.T) {
	srv, s := testServer(t)
	grantDevUserRuntimeBrokerAccess(t, s)
	project, broker := newEmptyPerAgentHandlerProject(t, s, "route-old-broker", true)
	agent := newQuotaTestAgent(t, s, broker, project, "route-old-broker", state.PhaseStopped)
	require.NoError(t, s.CreateEnvVar(context.Background(), &store.EnvVar{
		ID: uuid.NewString(), Key: failClosedKey, Value: failClosedValue,
		Scope: store.ScopeProject, ScopeID: project.ID, InjectionMode: store.InjectionModeAsNeeded,
	}))
	client := &mockRuntimeBrokerClient{}
	srv.SetDispatcher(NewHTTPAgentDispatcherWithClient(s, client, false, slog.Default()))

	rec := doRequest(t, srv, http.MethodPost, "/api/v1/agents/"+agent.ID+"/start", nil)
	require.Equal(t, http.StatusPreconditionFailed, rec.Code, rec.Body.String())
	var body ErrorResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, ErrCodeUnsupportedCapability, body.Error.Code)
	assert.Equal(t, errBrokerLacksPreserveWorkspace.Error(), body.Error.Message, "the fixed message only")
	assert.NotContains(t, rec.Body.String(), failClosedKey)
	assert.False(t, client.createCalled, "no create is sent")
	assert.False(t, client.startCalled, "no plain start")

	stored, err := s.GetAgent(context.Background(), agent.ID)
	require.NoError(t, err)
	assert.Equal(t, string(state.PhaseStopped), stored.Phase, "the agent stays stopped")
}

// The writer maps only errBrokerLacksPreserveWorkspace (wrapped or not) to
// 412 with the fixed message, and the refusal counts as a start that did not
// happen: nothing was sent, so the start claim releases and a reincarnation
// start leg left no container.
func TestPreserveWorkspaceRefusal_MappingAndClassification(t *testing.T) {
	wrapped := fmt.Errorf("dispatch create: %w", errBrokerLacksPreserveWorkspace)
	rec := httptest.NewRecorder()
	require.True(t, writePreserveWorkspaceCapabilityError(rec, wrapped))
	assert.Equal(t, http.StatusPreconditionFailed, rec.Code)
	var body ErrorResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, ErrCodeUnsupportedCapability, body.Error.Code)
	assert.Equal(t, errBrokerLacksPreserveWorkspace.Error(), body.Error.Message)
	assert.False(t, writePreserveWorkspaceCapabilityError(httptest.NewRecorder(), errors.New("other")))

	assert.True(t, isConfirmedStartNotActedOnError(wrapped))
	assert.True(t, startDidNotHappen(wrapped))
	assert.Equal(t, startReleased, startOutcomeOf(wrapped))
	assert.True(t, reincarnationStartLeftNoContainer(wrapped))
}

// Across nodes, the replay leg: the routed start's needs report goes out
// locally and its replay is handed to the owner as a finalize_env intent.
// The intent carries the preserve flag, the task and the synchronous
// decision; the owner re-checks the capability, keeps the workspace and
// records the marker.
func TestCrossNode_RoutedStartReplayHandedOffAsFinalizeEnv(t *testing.T) {
	const hubID = "hub-routed-finalize"
	setup := func(t *testing.T) (*crossNodeFixture, *int) {
		f := newCrossNodeFixture(t, nil, true)
		calls := 0
		f.requester.client = &mockRuntimeBrokerClient{
			createWithGatherFunc: func(_ context.Context, _, _ string, req *RemoteCreateAgentRequest) (*RemoteAgentResponse, *RemoteEnvRequirementsResponse, error) {
				calls++
				if calls == 1 {
					return nil, &RemoteEnvRequirementsResponse{AgentID: req.ID, Needs: []string{failClosedKey}}, nil
				}
				return nil, nil, ErrLifecycleDeferred
			},
		}
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
		return f, &calls
	}
	t.Run("flags cross and the owner records the marker", func(t *testing.T) {
		f, calls := setup(t)
		grantPreserveWorkspaceCreate(t, f.store, f.agent.RuntimeBrokerID)

		_ = f.requester.DispatchAgentStart(crossNodeCtx(t), f.agent, "fake-routed-task", false)
		require.Equal(t, 2, *calls, "the local needs report, then the deferred replay")
		f.ownerDisp.recMu.Lock()
		optIn, preserve, task := f.ownerDisp.finalizeAsyncOptIn, f.ownerDisp.finalizePreserveWorkspace, f.ownerDisp.finalizeTask
		createRan := f.ownerDisp.createAsyncOptIn != nil
		f.ownerDisp.recMu.Unlock()
		require.NotNil(t, optIn, "the replay reached the owner as a finalize_env")
		assert.False(t, createRan, "no create intent")
		assert.True(t, preserve, "the owner's finalize keeps the workspace")
		assert.Equal(t, "fake-routed-task", task, "the start's task reaches the owner's finalize")
		assert.False(t, *optIn, "the owner's finalize is synchronous")
		stored, err := f.store.GetAgent(context.Background(), f.agent.ID)
		require.NoError(t, err)
		require.NotNil(t, stored.AppliedConfig)
		assert.True(t, stored.AppliedConfig.ConfigDelivered)
		assert.NotEqual(t, "fake-routed-task", stored.AppliedConfig.Task, "the task override is not stored")
	})
	t.Run("the owner persists the marker after a routed finalize", func(t *testing.T) {
		f, _ := setup(t)
		grantPreserveWorkspaceCreate(t, f.store, f.agent.RuntimeBrokerID)
		raw, err := json.Marshal(&FinalizeEnvDispatchArgs{Synchronous: true, PreserveWorkspace: true, Task: "fake-routed-task"})
		require.NoError(t, err)
		_, err = f.owner.execDispatchFinalizeEnv(context.Background(), store.BrokerDispatch{AgentID: f.agent.ID, Args: string(raw)})
		require.NoError(t, err)
		stored, err := f.store.GetAgent(context.Background(), f.agent.ID)
		require.NoError(t, err)
		require.NotNil(t, stored.AppliedConfig)
		assert.True(t, stored.AppliedConfig.ConfigDelivered, "the owner recorded the marker")
	})
	t.Run("a plain finalize leaves the row to the requester", func(t *testing.T) {
		f, _ := setup(t)
		raw, err := json.Marshal(&FinalizeEnvDispatchArgs{Env: map[string]string{"FAKE_GATHERED": "fake"}})
		require.NoError(t, err)
		_, err = f.owner.execDispatchFinalizeEnv(context.Background(), store.BrokerDispatch{AgentID: f.agent.ID, Args: string(raw)})
		require.NoError(t, err)
		f.ownerDisp.recMu.Lock()
		defer f.ownerDisp.recMu.Unlock()
		assert.False(t, f.ownerDisp.finalizePreserveWorkspace)
		require.NotNil(t, f.ownerDisp.finalizeAsyncOptIn)
		assert.True(t, *f.ownerDisp.finalizeAsyncOptIn, "a plain finalize keeps the stored opt-in")
	})
	t.Run("the owner refuses without the capability", func(t *testing.T) {
		f, _ := setup(t)
		raw, err := json.Marshal(&FinalizeEnvDispatchArgs{Synchronous: true, PreserveWorkspace: true, Task: "fake-routed-task"})
		require.NoError(t, err)
		_, err = f.owner.execDispatchFinalizeEnv(context.Background(), store.BrokerDispatch{AgentID: f.agent.ID, Args: string(raw)})
		require.ErrorIs(t, err, errBrokerLacksPreserveWorkspace)
		f.ownerDisp.recMu.Lock()
		defer f.ownerDisp.recMu.Unlock()
		assert.Nil(t, f.ownerDisp.finalizeAsyncOptIn, "no finalize ran on the owner")
	})
}

// Every other intent a start can be handed off as cannot reach a create on
// the owner: a deferred plain start carries NoRoute (routing was decided
// on the requester), and the owner's start, stop, restart, delete and
// check_prompt executors never run a create or finalize.
func TestCrossNode_OtherIntentsNeverCreateOnOwner(t *testing.T) {
	t.Run("a deferred plain start carries NoRoute", func(t *testing.T) {
		f := newCrossNodeFixture(t, nil, true)
		_ = f.requester.DispatchAgentStart(crossNodeCtx(t), f.agent, "", false)
		f.ownerDisp.recMu.Lock()
		defer f.ownerDisp.recMu.Unlock()
		assert.True(t, f.ownerDisp.startRoutingSuppressed, "the owner's start is plain")
		assert.Nil(t, f.ownerDisp.createAsyncOptIn)
	})
	for _, op := range []string{"start", "stop", "restart", "delete", "check_prompt"} {
		t.Run("owner "+op, func(t *testing.T) {
			f := newCrossNodeFixture(t, nil, false)
			_, _ = f.owner.executeDispatch(context.Background(), store.BrokerDispatch{Op: op, AgentID: f.agent.ID, Args: "{}"})
			f.ownerDisp.recMu.Lock()
			defer f.ownerDisp.recMu.Unlock()
			assert.Nil(t, f.ownerDisp.createAsyncOptIn, "no create on the owner")
			assert.Nil(t, f.ownerDisp.finalizeAsyncOptIn, "no finalize on the owner")
		})
	}
}

// The dispatcher's start backstops run before routing: a routed start of
// an agent that fails the standing check (for example held during a
// reincarnation's reprovision) is refused with the standing error, not the
// capability refusal, and nothing is sent.
func TestRoutedStart_StandingBackstopRunsFirst(t *testing.T) {
	for _, capable := range []bool{true, false} {
		t.Run(fmt.Sprintf("capable broker %v", capable), func(t *testing.T) {
			f := newRunDispatchFixture(t)
			neverStarted(f)
			seedAsNeededEnv(t, f, failClosedKey, failClosedValue)
			if !capable {
				b, err := f.store.GetRuntimeBroker(context.Background(), f.agent.RuntimeBrokerID)
				require.NoError(t, err)
				b.Capabilities = nil
				require.NoError(t, f.store.UpdateRuntimeBroker(context.Background(), b))
			}
			recordCreates(f)
			f.d.SetRequiredStandingCheck(func(context.Context, *store.Agent) error { return errors.New("fake hold") })

			err := f.d.DispatchAgentStart(context.Background(), f.agent, "[SCION REINCARNATION] fake preamble", false)
			require.ErrorIs(t, err, ErrAgentNotInStanding)
			assert.NotErrorIs(t, err, errBrokerLacksPreserveWorkspace)
			assert.False(t, f.client.createCalled, "no create is sent")
			assert.False(t, f.client.startCalled, "no plain start")
		})
	}
}

// Each of the other start backstops also runs before routing: a stale pin
// (placement), an in-flight create launch (launch guard) and an agent with
// no Runtime Broker assigned are refused with their own errors, never the
// capability refusal, and nothing is sent.
func TestRoutedStart_OtherBackstopsRunFirst(t *testing.T) {
	t.Run("placement: stale pin", func(t *testing.T) {
		f := newFlatHubFixture(t, flatHubOpts{experimentOn: true, linkFlat: true})
		a := f.stalePinnedAgent(t, "routed-stale", string(state.PhaseStopped))
		require.NoError(t, f.s.CreateEnvVar(context.Background(), &store.EnvVar{
			ID: uuid.NewString(), Key: failClosedKey, Value: failClosedValue,
			Scope: store.ScopeProject, ScopeID: a.ProjectID, InjectionMode: store.InjectionModeAsNeeded,
		}))
		disp := NewHTTPAgentDispatcherWithClient(f.s, f.client, false, slog.Default())
		err := disp.DispatchAgentStart(context.Background(), a, "", false)
		var refusal *RuntimeTargetRefusal
		require.ErrorAs(t, err, &refusal)
		assert.Equal(t, ErrCodeRuntimeTargetPinStale, refusal.Code)
		assert.NotErrorIs(t, err, errBrokerLacksPreserveWorkspace)
		assert.False(t, f.client.createCalled, "no create is sent")
		assert.False(t, f.client.startCalled)
	})
	t.Run("launch guard: create launch in flight", func(t *testing.T) {
		f := newRunDispatchFixture(t)
		neverStarted(f)
		seedAsNeededEnv(t, f, failClosedKey, failClosedValue)
		stale := *f.agent
		seedLaunch(t, f.store, f.agent, seedInFlight)
		recordCreates(f)
		err := f.d.DispatchAgentStart(context.Background(), &stale, "", false)
		require.ErrorIs(t, err, ErrLaunchInFlight)
		assert.False(t, f.client.createCalled, "no create is sent")
	})
	t.Run("no runtime broker assigned", func(t *testing.T) {
		f := newRunDispatchFixture(t)
		neverStarted(f)
		seedAsNeededEnv(t, f, failClosedKey, failClosedValue)
		recordCreates(f)
		f.agent.RuntimeBrokerID = ""
		err := f.d.DispatchAgentStart(context.Background(), f.agent, "", false)
		require.ErrorIs(t, err, errNoRuntimeBrokerAssigned)
		assert.NotErrorIs(t, err, errBrokerLacksPreserveWorkspace)
		assert.False(t, f.client.createCalled, "no create is sent")
	})
}

// fakeBrokerReadError is an injected broker-record read failure (not
// store.ErrNotFound) whose text must never reach a client.
const fakeBrokerReadError = "fake broker store failure: connection refused to 10.0.0.9"

// failingBrokerReads wraps st so every broker-record read fails with
// fakeBrokerReadError (brokerReadErrStore with no fault switch).
func failingBrokerReads(st store.Store) store.Store {
	return &brokerReadErrStore{Store: st, err: errors.New(fakeBrokerReadError)}
}

// mintCountingGen counts the agent credentials a dispatcher signs.
type mintCountingGen struct {
	AgentTokenGenerator
	mints int
}

func (g *mintCountingGen) SignAgentToken(grant AgentTokenGrant, runID string) (string, *store.AgentCredential, error) {
	g.mints++
	return g.AgentTokenGenerator.SignAgentToken(grant, runID)
}

// assertBrokerReadRefusal checks a broker-record read refusal for verb: the
// sentinel, the fixed message, no raw store text, not acted on, and the 503
// body the handlers write.
func assertBrokerReadRefusal(t *testing.T, err error, verb string) {
	t.Helper()
	require.ErrorIs(t, err, errBrokerRecordUnreadable)
	assert.Contains(t, err.Error(), brokerRecordUnreadableMessage(verb))
	assert.NotContains(t, err.Error(), "10.0.0.9", "no raw store text")
	assert.True(t, isConfirmedStartNotActedOnError(err))
	rec := httptest.NewRecorder()
	require.True(t, writePreserveWorkspaceCapabilityError(rec, fmt.Errorf("wrapped: %w", err)))
	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
	var body ErrorResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, ErrCodeUnavailable, body.Error.Code)
	assert.NotContains(t, rec.Body.String(), "10.0.0.9", "no raw store text")
}

// A start or restart whose broker record cannot be read fails with the
// fixed, keyless 503 message for its action, sends and mints nothing and
// writes nothing: a non-NotFound read failure injected at the placement
// backstop (the one read every guard step judges), and a record missing at
// the routed start's capability check.
func TestRoutedStart_BrokerRecordUnreadableIs503(t *testing.T) {
	setup := func(t *testing.T) (*runDispatchFixture, *mintCountingGen, int64) {
		f := newRunDispatchFixture(t)
		neverStarted(f)
		seedAsNeededEnv(t, f, failClosedKey, failClosedValue)
		recordCreates(f)
		gen := &mintCountingGen{AgentTokenGenerator: runTokenGenerator{svc: f.svc}}
		f.d.SetTokenGenerator(gen)
		stored, err := f.store.GetAgent(context.Background(), f.agent.ID)
		require.NoError(t, err)
		return f, gen, stored.StateVersion
	}
	nothingDone := func(t *testing.T, f *runDispatchFixture, gen *mintCountingGen, version int64) {
		t.Helper()
		assert.False(t, f.client.createCalled, "no create is sent")
		assert.False(t, f.client.startCalled, "no plain start")
		assert.False(t, f.client.restartCalled, "no restart")
		assert.Equal(t, 0, gen.mints, "no credential is minted")
		stored, err := f.store.GetAgent(context.Background(), f.agent.ID)
		require.NoError(t, err)
		assert.Equal(t, version, stored.StateVersion, "nothing is written")
	}
	t.Run("start: read fails at the placement backstop", func(t *testing.T) {
		f, gen, v := setup(t)
		f.d.store = failingBrokerReads(f.store)
		err := f.d.DispatchAgentStart(context.Background(), f.agent, "", false)
		assertBrokerReadRefusal(t, err, "started")
		assert.Equal(t, brokerRecordUnreadableMessage("started"), err.Error())
		nothingDone(t, f, gen, v)
	})
	t.Run("restart start leg: the message is the restart's", func(t *testing.T) {
		f, gen, v := setup(t)
		f.d.store = failingBrokerReads(f.store)
		err := f.d.DispatchAgentStart(withRestartStartLeg(context.Background()), f.agent, "", false)
		assertBrokerReadRefusal(t, err, "restarted")
		assert.Equal(t, brokerRecordUnreadableMessage("restarted"), err.Error())
		nothingDone(t, f, gen, v)
	})
	t.Run("restart: the message is the restart's", func(t *testing.T) {
		f, gen, v := setup(t)
		f.d.store = failingBrokerReads(f.store)
		err := f.d.DispatchAgentRestart(context.Background(), f.agent)
		assertBrokerReadRefusal(t, err, "restarted")
		assert.Equal(t, brokerRecordUnreadableMessage("restarted"), err.Error())
		nothingDone(t, f, gen, v)
	})
	t.Run("record missing at the capability check", func(t *testing.T) {
		f, gen, _ := setup(t)
		f.agent.RuntimeBrokerID = tid("broker-missing")
		err := f.d.DispatchAgentStart(context.Background(), f.agent, "", false)
		assertBrokerReadRefusal(t, err, "started")
		assert.False(t, f.client.createCalled, "no create is sent")
		assert.Equal(t, 0, gen.mints, "no credential is minted")
	})
}

// Through the HTTP entry points, with the read failure injected on the
// server's own store (the first read of every request): /start answers 503
// "started", /restart 503 "restarted", and a message wake 503 "started", each
// with the fixed body, no raw store text, nothing sent, and the agent left
// as it was.
func TestLifecycleHTTP_BrokerRecordUnreadableIs503(t *testing.T) {
	setup := func(t *testing.T, name string, phase state.Phase) (*Server, *mockRuntimeBrokerClient, *store.Agent, store.Store, *storeFaultSwitch) {
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
		_, fault := installStoreFault(t, srv, brokerReadFaultWrap(errors.New(fakeBrokerReadError)))
		return srv, client, agent, s, fault
	}
	checkHTTP := func(t *testing.T, rec *httptest.ResponseRecorder, verb string) {
		t.Helper()
		require.Equal(t, http.StatusServiceUnavailable, rec.Code, rec.Body.String())
		var body ErrorResponse
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
		assert.Equal(t, ErrCodeUnavailable, body.Error.Code)
		assert.Equal(t, brokerRecordUnreadableMessage(verb), body.Error.Message)
		assert.NotContains(t, rec.Body.String(), "10.0.0.9", "no raw store text")
	}
	t.Run("start", func(t *testing.T) {
		srv, client, agent, s, fault := setup(t, "read-err-start", state.PhaseStopped)
		fault.Arm()
		checkHTTP(t, doRequest(t, srv, http.MethodPost, "/api/v1/agents/"+agent.ID+"/start", nil), "started")
		assert.False(t, client.createCalled || client.startCalled, "nothing is sent")
		stored, err := s.GetAgent(context.Background(), agent.ID)
		require.NoError(t, err)
		assert.Equal(t, string(state.PhaseStopped), stored.Phase, "the agent stays stopped")
	})
	t.Run("restart", func(t *testing.T) {
		srv, client, agent, s, fault := setup(t, "read-err-restart", state.PhaseRunning)
		fault.Arm()
		checkHTTP(t, doRequest(t, srv, http.MethodPost, "/api/v1/agents/"+agent.ID+"/restart", nil), "restarted")
		assert.False(t, client.stopCalled || client.restartCalled || client.startCalled || client.createCalled, "nothing is sent")
		stored, err := s.GetAgent(context.Background(), agent.ID)
		require.NoError(t, err)
		assert.Equal(t, string(state.PhaseRunning), stored.Phase, "the agent is not stopped")
	})
	t.Run("message wake", func(t *testing.T) {
		srv, client, agent, _, fault := setup(t, "read-err-wake", state.PhaseSuspended)
		fault.Arm()
		_, dmErr := srv.wakeAgentForDM(context.Background(), agent)
		require.NotNil(t, dmErr)
		assert.Equal(t, http.StatusServiceUnavailable, dmErr.HTTPStatus)
		assert.Equal(t, ErrCodeUnavailable, dmErr.Code)
		assert.Equal(t, brokerRecordUnreadableMessage("started"), dmErr.Message)
		assert.NotContains(t, dmErr.Message, "10.0.0.9", "no raw store text")
		assert.False(t, client.createCalled || client.startCalled, "nothing is sent")
	})
}

// A plain (not routed) start or restart of an unpinned agent whose broker
// row is missing answers the fixed 503 for its action, never the send's raw
// "not found": the dispatch chain judges the guards' record.
func TestDispatchStart_MissingBrokerRecordIs503(t *testing.T) {
	setup := func(t *testing.T) *runDispatchFixture {
		f := newRunDispatchFixture(t)
		f.agent.RuntimeBrokerID = tid("broker-missing")
		return f
	}
	t.Run("start", func(t *testing.T) {
		f := setup(t)
		err := f.d.DispatchAgentStart(context.Background(), f.agent, "", false)
		assertBrokerReadRefusal(t, err, "started")
		assert.Equal(t, brokerRecordUnreadableMessage("started"), err.Error())
		assert.NotContains(t, err.Error(), "failed to get runtime broker")
		assert.False(t, f.client.startCalled)
	})
	t.Run("restart", func(t *testing.T) {
		f := setup(t)
		err := f.d.DispatchAgentRestart(context.Background(), f.agent)
		assertBrokerReadRefusal(t, err, "restarted")
		assert.Equal(t, brokerRecordUnreadableMessage("restarted"), err.Error())
		assert.False(t, f.client.restartCalled)
	})
}

// A restart's start leg handed to the owner node keeps its mark: the
// owner's start names the restart, and an owner refusal for a failed
// broker-record read reaches the requester as the restart's fixed message.
func TestCrossNode_RestartLegVerbCrosses(t *testing.T) {
	t.Run("the owner sees the restart leg", func(t *testing.T) {
		f := newCrossNodeFixture(t, nil, true)
		_ = f.requester.DispatchAgentStart(withRestartStartLeg(crossNodeCtx(t)), f.agent, "", false)
		f.ownerDisp.recMu.Lock()
		defer f.ownerDisp.recMu.Unlock()
		assert.Equal(t, "restarted", f.ownerDisp.startVerb)
	})
	t.Run("a plain start stays a start", func(t *testing.T) {
		f := newCrossNodeFixture(t, nil, true)
		_ = f.requester.DispatchAgentStart(crossNodeCtx(t), f.agent, "", false)
		f.ownerDisp.recMu.Lock()
		defer f.ownerDisp.recMu.Unlock()
		assert.Equal(t, "started", f.ownerDisp.startVerb)
	})
	t.Run("an owner read refusal reads as the restart", func(t *testing.T) {
		ownerErr := &brokerRecordReadError{verb: "restarted", err: errors.New(fakeBrokerReadError)}
		f := newCrossNodeFixture(t, ownerErr, true)
		err := f.requester.DispatchAgentStart(withRestartStartLeg(crossNodeCtx(t)), f.agent, "", false)
		require.ErrorIs(t, err, errBrokerRecordUnreadable)
		assert.Equal(t, brokerRecordUnreadableMessage("restarted"), err.Error())
		assert.NotContains(t, err.Error(), "10.0.0.9")
		rec := httptest.NewRecorder()
		require.True(t, writePreserveWorkspaceCapabilityError(rec, err))
		var body ErrorResponse
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
		assert.Equal(t, brokerRecordUnreadableMessage("restarted"), body.Error.Message)
	})
}

// On the owner node, a routed start's create or finalize_env whose broker
// record cannot be read is refused with the same fixed message before
// anything runs, with no raw store text.
func TestCrossNode_OwnerBrokerRecordUnreadable(t *testing.T) {
	for _, op := range []string{"create", "finalize_env"} {
		t.Run(op, func(t *testing.T) {
			f := newCrossNodeFixture(t, nil, false)
			f.ownerDisp.st = failingBrokerReads(f.store)
			var raw []byte
			var err error
			if op == "create" {
				raw, err = json.Marshal(&CreateWithGatherDispatchArgs{Synchronous: true, PreserveWorkspace: true, Task: "fake-routed-task"})
				require.NoError(t, err)
				_, err = f.owner.execDispatchCreate(context.Background(), store.BrokerDispatch{AgentID: f.agent.ID, Args: string(raw)})
			} else {
				raw, err = json.Marshal(&FinalizeEnvDispatchArgs{Synchronous: true, PreserveWorkspace: true, Task: "fake-routed-task"})
				require.NoError(t, err)
				_, err = f.owner.execDispatchFinalizeEnv(context.Background(), store.BrokerDispatch{AgentID: f.agent.ID, Args: string(raw)})
			}
			assertBrokerReadRefusal(t, err, "started")
			f.ownerDisp.recMu.Lock()
			defer f.ownerDisp.recMu.Unlock()
			assert.Nil(t, f.ownerDisp.createAsyncOptIn, "no create ran")
			assert.Nil(t, f.ownerDisp.finalizeAsyncOptIn, "no finalize ran")
		})
	}
}

// The owner node runs the requester's start backstops before applying a
// routed start (with the hub's real dispatcher): a standing refusal wins
// over an old broker's 412, an agent with no broker assigned is refused,
// and nothing is sent or minted.
func TestCrossNode_OwnerRunsStartGuardsFirst(t *testing.T) {
	setup := func(t *testing.T) (*Server, *mockRuntimeBrokerClient, *HTTPAgentDispatcher, *store.Agent, store.Store) {
		cs := entadapter.NewCompositeStore(enttest.NewClient(t))
		owner := &Server{store: cs, instanceID: "hub-owner-" + uuid.NewString()[:8], agentLifecycleLog: slog.Default()}
		client := &mockRuntimeBrokerClient{}
		hd := NewHTTPAgentDispatcherWithClient(cs, client, false, slog.Default())
		owner.SetDispatcher(hd)
		hd.SetRequiredStandingCheck(func(context.Context, *store.Agent) error { return nil })
		agent := seedAgentWithBrokerID(t, cs, uuid.NewString())
		return owner, client, hd, agent, cs
	}
	args := func(t *testing.T) string {
		raw, err := json.Marshal(&CreateWithGatherDispatchArgs{Synchronous: true, PreserveWorkspace: true})
		require.NoError(t, err)
		return string(raw)
	}
	t.Run("standing refusal wins over the capability refusal", func(t *testing.T) {
		owner, client, hd, agent, _ := setup(t)
		hd.SetRequiredStandingCheck(func(context.Context, *store.Agent) error { return errors.New("fake hold") })
		gen := &mintCountingGen{AgentTokenGenerator: runTokenGenerator{}}
		hd.SetTokenGenerator(gen)
		_, err := owner.execDispatchCreate(context.Background(), store.BrokerDispatch{AgentID: agent.ID, Args: args(t)})
		require.ErrorIs(t, err, ErrAgentNotInStanding)
		assert.NotErrorIs(t, err, errBrokerLacksPreserveWorkspace)
		assert.False(t, client.createCalled, "nothing is sent")
		assert.Equal(t, 0, gen.mints, "no credential is minted")
	})
	t.Run("no runtime broker assigned", func(t *testing.T) {
		owner, client, _, agent, cs := setup(t)
		stored, err := cs.GetAgent(context.Background(), agent.ID)
		require.NoError(t, err)
		stored.RuntimeBrokerID = ""
		require.NoError(t, cs.UpdateAgent(context.Background(), stored))
		_, err = owner.execDispatchCreate(context.Background(), store.BrokerDispatch{AgentID: agent.ID, Args: args(t)})
		require.ErrorIs(t, err, errNoRuntimeBrokerAssigned)
		assert.False(t, client.createCalled, "nothing is sent")
	})
	t.Run("broker record read fails: the fixed 503, nothing sent or minted", func(t *testing.T) {
		owner, client, _, agent, cs := setup(t)
		hd := NewHTTPAgentDispatcherWithClient(failingBrokerReads(cs), client, false, slog.Default())
		owner.SetDispatcher(hd)
		hd.SetRequiredStandingCheck(func(context.Context, *store.Agent) error { return nil })
		gen := &mintCountingGen{AgentTokenGenerator: runTokenGenerator{}}
		hd.SetTokenGenerator(gen)
		for _, op := range []string{"create", "finalize_env"} {
			var err error
			if op == "create" {
				_, err = owner.execDispatchCreate(context.Background(), store.BrokerDispatch{AgentID: agent.ID, Args: args(t)})
			} else {
				raw, merr := json.Marshal(&FinalizeEnvDispatchArgs{Synchronous: true, PreserveWorkspace: true})
				require.NoError(t, merr)
				_, err = owner.execDispatchFinalizeEnv(context.Background(), store.BrokerDispatch{AgentID: agent.ID, Args: string(raw)})
			}
			assertBrokerReadRefusal(t, err, "started")
		}
		assert.False(t, client.createCalled, "nothing is sent")
		assert.Equal(t, 0, gen.mints, "no credential is minted")
	})
	t.Run("a dispatcher without the start guards refuses", func(t *testing.T) {
		owner, _, _, agent, _ := setup(t)
		owner.SetDispatcher(&lifecycleTestDispatcher{})
		_, err := owner.execDispatchCreate(context.Background(), store.BrokerDispatch{AgentID: agent.ID, Args: args(t)})
		require.ErrorIs(t, err, errNoStartGuards)
	})
	t.Run("an old broker is refused after the guards pass", func(t *testing.T) {
		owner, client, _, agent, _ := setup(t)
		_, err := owner.execDispatchCreate(context.Background(), store.BrokerDispatch{AgentID: agent.ID, Args: args(t)})
		require.ErrorIs(t, err, errBrokerLacksPreserveWorkspace)
		assert.False(t, client.createCalled, "nothing is sent")
	})
}

// The owner's refusal of a routed start's finalize_env (or create) crosses
// back to the requesting node as the same sentinel, so the requester's
// handler answers 412 (capability) or 503 (broker record) with the fixed
// message, and the refusal counts as a start that was not acted on.
func TestCrossNode_OwnerRefusalCrossesBackAsSentinel(t *testing.T) {
	for _, tc := range []struct {
		sentinel error
		status   int
		code     string
	}{
		{errBrokerLacksPreserveWorkspace, http.StatusPreconditionFailed, ErrCodeUnsupportedCapability},
		{errBrokerRecordUnreadable, http.StatusServiceUnavailable, ErrCodeUnavailable},
	} {
		for _, op := range []string{"finalize_env", "create"} {
			t.Run(op+" "+tc.code, func(t *testing.T) {
				ownerErr := fmt.Errorf("dispatch %s: %w", op, tc.sentinel)
				row := &store.BrokerDispatch{Op: op, Error: ownerErr.Error(), Result: dispatchFailureResult(ownerErr)}
				err := dispatchFailureError(row)
				require.ErrorIs(t, err, tc.sentinel)
				assert.True(t, isConfirmedStartNotActedOnError(err))

				rec := httptest.NewRecorder()
				require.True(t, writePreserveWorkspaceCapabilityError(rec, err))
				assert.Equal(t, tc.status, rec.Code)
				var body ErrorResponse
				require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
				assert.Equal(t, tc.code, body.Error.Code)
				assert.Equal(t, tc.sentinel.Error(), body.Error.Message, "the fixed message only")
			})
		}
	}
}

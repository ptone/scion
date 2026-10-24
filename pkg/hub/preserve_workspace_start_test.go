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
	"log/slog"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/GoogleCloudPlatform/scion/pkg/agent/state"
	"github.com/GoogleCloudPlatform/scion/pkg/store"
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
// start of the reloaded row is not routed again. On a broker without
// startEnvGather that start is refused (412) rather than sent plain: a plain
// start would recreate the container without the agent's as-needed values
// (ptone/scion#6101).
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
	err = f.d.DispatchAgentStart(context.Background(), stored, "", false)
	require.ErrorIs(t, err, errBrokerLacksStartEnvGather)
	assert.False(t, f.client.startCalled, "no plain start without the as-needed values")
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

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
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/GoogleCloudPlatform/scion/pkg/agent/state"
	"github.com/GoogleCloudPlatform/scion/pkg/store"
	"github.com/GoogleCloudPlatform/scion/pkg/store/entadapter"
)

// A container heartbeat carrying live work (ptone/scion#7855) records the
// live-work timestamps and, like any heartbeat-only report, publishes no
// status event.
func TestAgentStatus_HeartbeatWithLiveWorkPublishesNothing(t *testing.T) {
	srv, s := testServer(t)
	ctx := context.Background()
	project := &store.Project{ID: tid("project-lw"), Name: "LiveWork", Slug: "live-work"}
	require.NoError(t, s.CreateProject(ctx, project))
	agent := &store.Agent{
		ID: tid("agent-lw"), Slug: "agent-lw", Name: "agent-lw", ProjectID: project.ID,
		Phase: string(state.PhaseRunning), Activity: string(state.ActivityExecuting),
	}
	require.NoError(t, s.CreateAgent(ctx, agent))
	probe := newStatusEventProbe(t, srv, agent.ID)

	postAgentStatus(t, srv, agent.ID, store.AgentStatusUpdate{
		Heartbeat: true,
		LiveWork:  &store.LiveWork{Kind: "process", SuppressStall: true, Detail: "go (pid 42)"},
	})
	assert.Equal(t, 0, probe.drain(), "a heartbeat with live work publishes no status event")
	assert.Empty(t, probe.counts())

	got, err := s.GetAgent(ctx, agent.ID)
	require.NoError(t, err)
	require.NotNil(t, got.LastLiveProcessAt)
	require.NotNil(t, got.LastLiveWorkAt)
	assert.WithinDuration(t, time.Now(), *got.LastLiveWorkAt, time.Minute)
	assert.Equal(t, string(state.ActivityExecuting), got.Activity)
	assert.True(t, got.HasRecentLiveWork(time.Now()))
}

// A stalled agent whose container then reports stall-suppressing live work
// returns to its previous activity with exactly one status event
// (ptone/scion#7855, acceptance criteria 6 and 7). Non-suppressing live
// work (a capped or stuck tool call) leaves it stalled and publishes
// nothing, and later heartbeats publish nothing either.
func TestAgentStatus_LiveWorkUnstallsWithOneEvent(t *testing.T) {
	srv, s := testServer(t)
	ctx := context.Background()
	project := &store.Project{ID: tid("project-unstall"), Name: "Unstall", Slug: "unstall"}
	require.NoError(t, s.CreateProject(ctx, project))
	agent := &store.Agent{
		ID: tid("agent-unstall"), Slug: "agent-unstall", Name: "agent-unstall", ProjectID: project.ID,
		Phase: string(state.PhaseRunning), Activity: string(state.ActivityStalled),
		StalledFromActivity: string(state.ActivityExecuting),
	}
	require.NoError(t, s.CreateAgent(ctx, agent))
	probe := newStatusEventProbe(t, srv, agent.ID)

	postAgentStatus(t, srv, agent.ID, store.AgentStatusUpdate{
		Heartbeat: true,
		LiveWork:  &store.LiveWork{Kind: "tool", Detail: "gh (pid 81, tool call running 1h0m30s, past the cap)"},
	})
	assert.Equal(t, 0, probe.drain(), "non-suppressing live work does not un-stall")
	got, err := s.GetAgent(ctx, agent.ID)
	require.NoError(t, err)
	assert.Equal(t, string(state.ActivityStalled), got.Activity)

	postAgentStatus(t, srv, agent.ID, store.AgentStatusUpdate{
		Heartbeat: true,
		LiveWork:  &store.LiveWork{Kind: "tool", SuppressStall: true, Detail: "gh (pid 81, tool call running 30s)"},
	})
	assert.Equal(t, 1, probe.drain(), "the un-stall publishes one status event")
	got, err = s.GetAgent(ctx, agent.ID)
	require.NoError(t, err)
	assert.Equal(t, string(state.ActivityExecuting), got.Activity)
	assert.Empty(t, got.StalledFromActivity)

	postAgentStatus(t, srv, agent.ID, store.AgentStatusUpdate{
		Heartbeat: true,
		LiveWork:  &store.LiveWork{Kind: "tool", SuppressStall: true},
	})
	postAgentStatus(t, srv, agent.ID, store.AgentStatusUpdate{Heartbeat: true})
	assert.Equal(t, 0, probe.drain(), "later heartbeats publish nothing")
}

// Auto-suspend skips a stalled agent whose container reported live work
// within store.LiveWorkFreshness, and still suspends one whose live work is
// older than that (ptone/scion#7855, invariant 2).
func TestAutoSuspend_SkipsAgentWithRecentLiveWork(t *testing.T) {
	srv, s := testServer(t)
	disp := newGatedStopDispatcher()
	close(disp.release)
	srv.SetDispatcher(disp)
	ctx := context.Background()

	liveAgent := newStalledBrokerAgent(t, s, "lw-live")
	staleAgent := newStalledBrokerAgent(t, s, "lw-stale")
	db := s.(*entadapter.CompositeStore).DB()
	for id, at := range map[string]time.Time{
		liveAgent.ID:  time.Now().Add(-10 * time.Second),
		staleAgent.ID: time.Now().Add(-10 * time.Minute),
	} {
		_, err := db.ExecContext(ctx, "UPDATE agents SET last_live_process_at = ? WHERE id = ?", at, id)
		require.NoError(t, err)
	}
	a, err := s.GetAgent(ctx, liveAgent.ID)
	require.NoError(t, err)
	require.True(t, a.HasRecentLiveWork(time.Now()))
	b, err := s.GetAgent(ctx, staleAgent.ID)
	require.NoError(t, err)
	require.False(t, b.HasRecentLiveWork(time.Now()))

	srv.autoSuspendStalledAgents(ctx, []store.Agent{*a, *b})

	assert.Equal(t, []string{staleAgent.ID}, disp.stopped())
	gotLive, err := s.GetAgent(ctx, liveAgent.ID)
	require.NoError(t, err)
	assert.Equal(t, string(state.PhaseRunning), gotLive.Phase, "an agent with live work stays running")
}

// TestBackgroundLiveness_TieredCapsAndSweeps verifies ptone/scion#9751 end to
// end:
//   - KindBackground ("background", SuppressStall: false) keeps a local-runtime
//     agent live until the 2h cap, then stops refreshing last_live_process_at
//     so auto-suspend applies.
//   - Reserving runtimes (kubernetes, cloudrun, substrate) cap at 30m.
//   - KindBackground never suppresses the stall sweep (MarkStalledAgents) and
//     never un-stalls, while still guarding stalled and idle auto-suspend.
//   - Hub lifecycle settings override defaults live, including "0s" to disable.
func TestBackgroundLiveness_TieredCapsAndSweeps(t *testing.T) {
	srv, s := testServer(t)
	disp := newGatedStopDispatcher()
	close(disp.release)
	srv.SetDispatcher(disp)
	ctx := context.Background()

	project := &store.Project{ID: tid("project-bg-lw"), Name: "BgLiveWork", Slug: "bg-live-work"}
	require.NoError(t, s.CreateProject(ctx, project))
	db := s.(*entadapter.CompositeStore).DB()

	newAgent := func(slug, runtime, activity string, actAge time.Duration) *store.Agent {
		a := &store.Agent{
			ID:              tid("agent-" + slug),
			Slug:            slug,
			Name:            slug,
			ProjectID:       project.ID,
			RuntimeBrokerID: "broker-bg",
			Runtime:         runtime,
			Phase:           string(state.PhaseRunning),
			Activity:        activity,
		}
		require.NoError(t, s.CreateAgent(ctx, a))
		at := time.Now().Add(-actAge)
		_, err := db.ExecContext(ctx,
			"UPDATE agents SET last_activity_event = ?, activity_since = ?, last_seen = ? WHERE id = ?",
			at, at, time.Now(), a.ID)
		require.NoError(t, err)
		return a
	}

	bgHB := store.AgentStatusUpdate{
		Heartbeat: true,
		LiveWork:  &store.LiveWork{Kind: store.LiveWorkKindBackground, SuppressStall: false, Detail: "flock (pid 90, background)"},
	}

	// 1. Local runtime (docker): 90m since last activity (< 2h default) ->
	//    protected against idle and stalled auto-suspend, but NOT against STALLED.
	localWithin := newAgent("bg-local-within", "docker", string(state.ActivityWorking), 90*time.Minute)
	postAgentStatus(t, srv, localWithin.ID, bgHB)
	got, err := s.GetAgent(ctx, localWithin.ID)
	require.NoError(t, err)
	assert.True(t, got.HasRecentLiveWork(time.Now()), "local agent within 2h cap is protected")
	assert.Nil(t, got.LastLiveWorkAt, "KindBackground never sets LastLiveWorkAt")

	// Stall sweep still marks it stalled (SuppressStall is false).
	stalled, err := s.MarkStalledAgents(ctx, store.StallCutoffs{
		Activity: time.Now().Add(-5 * time.Minute),
		Thinking: time.Now().Add(-20 * time.Minute),
	}, time.Now().Add(-2*time.Minute))
	require.NoError(t, err)
	require.Len(t, stalled, 1)
	assert.Equal(t, localWithin.ID, stalled[0].ID)

	// Stalled auto-suspend skips it because LastLiveProcessAt is fresh.
	srv.autoSuspendStalledAgents(ctx, stalled)
	assert.Empty(t, disp.stopped(), "stalled auto-suspend honors KindBackground within cap")

	// 2. Local runtime past 2h cap -> heartbeat does not stamp LastLiveProcessAt,
	//    so idle auto-suspend candidate query selects it.
	localExpired := newAgent("bg-local-expired", "docker", string(state.ActivityIdle), 2*time.Hour+time.Minute)
	postAgentStatus(t, srv, localExpired.ID, bgHB)
	got, err = s.GetAgent(ctx, localExpired.ID)
	require.NoError(t, err)
	assert.False(t, got.HasRecentLiveWork(time.Now()), "local agent past 2h cap is not protected")

	// 3. Reserving runtimes (kubernetes, cloudrun, substrate): 20m (< 30m) is
	//    protected; 35m (>= 30m) is not.
	k8sWithin := newAgent("bg-k8s-within", "kubernetes", string(state.ActivityIdle), 20*time.Minute)
	k8sExpired := newAgent("bg-k8s-expired", "kubernetes", string(state.ActivityIdle), 35*time.Minute)
	crExpired := newAgent("bg-cr-expired", "cloudrun", string(state.ActivityIdle), 35*time.Minute)
	subExpired := newAgent("bg-sub-expired", "substrate", string(state.ActivityIdle), 35*time.Minute)
	for _, a := range []*store.Agent{k8sWithin, k8sExpired, crExpired, subExpired} {
		postAgentStatus(t, srv, a.ID, bgHB)
	}
	gotK8sWithin, err := s.GetAgent(ctx, k8sWithin.ID)
	require.NoError(t, err)
	assert.True(t, gotK8sWithin.HasRecentLiveWork(time.Now()), "kubernetes within 30m cap is protected")
	for _, a := range []*store.Agent{k8sExpired, crExpired, subExpired} {
		g, gerr := s.GetAgent(ctx, a.ID)
		require.NoError(t, gerr)
		assert.False(t, g.HasRecentLiveWork(time.Now()), "%s past 30m cap is not protected", a.Runtime)
	}

	// Idle auto-suspend candidate listing skips k8sWithin and includes the expired agents.
	now := time.Now()
	cands, err := s.ListIdleSuspendCandidates(ctx, now.Add(-15*time.Minute), now.Add(-2*time.Minute), now.Add(-store.LiveWorkFreshness), time.Time{}, nil, 50, false)
	require.NoError(t, err)
	candIDs := make([]string, len(cands))
	for i, c := range cands {
		candIDs[i] = c.ID
	}
	assert.NotContains(t, candIDs, k8sWithin.ID)
	assert.Contains(t, candIDs, localExpired.ID)
	assert.Contains(t, candIDs, k8sExpired.ID)
	assert.Contains(t, candIDs, crExpired.ID)
	assert.Contains(t, candIDs, subExpired.ID)

	// 4. Settings override defaults live: raising reserved cap to 1h protects a
	//    45m kubernetes agent; setting local cap to "0s" disables it immediately.
	ApplySnapshot(srv, Layer1Snapshot{
		BackgroundLivenessCapLocal:    "0s",
		BackgroundLivenessCapReserved: "1h",
	})
	assert.Equal(t, time.Duration(0), srv.backgroundLivenessCaps().Local)
	assert.Equal(t, time.Hour, srv.backgroundLivenessCaps().Reserved)

	localDisabled := newAgent("bg-local-disabled", "docker", string(state.ActivityIdle), time.Minute)
	k8sExtended := newAgent("bg-k8s-extended", "kubernetes", string(state.ActivityIdle), 45*time.Minute)
	postAgentStatus(t, srv, localDisabled.ID, bgHB)
	postAgentStatus(t, srv, k8sExtended.ID, bgHB)

	gotLocalDisabled, err := s.GetAgent(ctx, localDisabled.ID)
	require.NoError(t, err)
	assert.False(t, gotLocalDisabled.HasRecentLiveWork(time.Now()), "0s disables local background liveness")

	gotK8sExtended, err := s.GetAgent(ctx, k8sExtended.ID)
	require.NoError(t, err)
	assert.True(t, gotK8sExtended.HasRecentLiveWork(time.Now()), "1h override extends kubernetes cap")
}

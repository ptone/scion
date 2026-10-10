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
	"sync"
	"testing"
	"time"

	"github.com/GoogleCloudPlatform/scion/pkg/agent/state"
	"github.com/GoogleCloudPlatform/scion/pkg/store"
	"github.com/GoogleCloudPlatform/scion/pkg/store/entadapter"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setStalledDetectionTimeout sets stalledDetectionTimeout for one test.
func setStalledDetectionTimeout(t *testing.T, d time.Duration) {
	t.Helper()
	prev := stalledDetectionTimeout
	stalledDetectionTimeout = d
	t.Cleanup(func() { stalledDetectionTimeout = prev })
}

// setAutoSuspendStartWindow sets autoSuspendStartWindow for one test.
func setAutoSuspendStartWindow(t *testing.T, d time.Duration) {
	t.Helper()
	prev := autoSuspendStartWindow
	autoSuspendStartWindow = func() time.Duration { return d }
	t.Cleanup(func() { autoSuspendStartWindow = prev })
}

// makeStalled backdates agent's last activity, with a recent heartbeat, so
// the next stalled-detection tick marks it stalled.
func makeStalled(t *testing.T, s store.Store, agentID string) {
	t.Helper()
	db := s.(*entadapter.CompositeStore).DB()
	_, err := db.ExecContext(context.Background(),
		"UPDATE agents SET last_activity_event = ?, last_seen = ? WHERE id = ?",
		time.Now().Add(-time.Hour), time.Now().Add(-10*time.Second), agentID)
	require.NoError(t, err)
}

// ptone/scion#4387 item 2: each agent's auto-suspend has its own bound,
// detached from the stalled-detection tick and from the agents before it.
// The first agent's sync-back download runs until its own bound cuts it,
// well past the tick's bound; the second agent in the batch is still
// suspended, with a full sync-back of its own.
func TestAutoSuspend_SlowSyncBackDoesNotCutNextAgent(t *testing.T) {
	const (
		tickBound = 250 * time.Millisecond
		syncBound = time.Second
	)
	t.Setenv("HOME", t.TempDir())
	disp := &slowLaunchDispatcher{delay: 10 * time.Millisecond}
	srv, s, project := setupCreateAgentServer(t, disp) // hub-managed: no GitRemote.
	shortenSyncDispatchTimeout(t, syncBound)
	setStalledDetectionTimeout(t, tickBound)
	require.Equal(t, syncBound, stopSyncBackTimeout(), "fixture check: the sync-back bound follows syncDispatchTimeout")
	srv.config.AutoSuspendStalled = true

	srv.SetStorage(newContentMockStorage("test-bucket"))
	var (
		mu           sync.Mutex
		downloadErrs []error
		remaining    []time.Duration
	)
	srv.setHubWorkspaceDownloader(func(ctx context.Context, _, _, _ string) error {
		mu.Lock()
		first := len(downloadErrs) == 0
		if deadline, ok := ctx.Deadline(); ok {
			remaining = append(remaining, time.Until(deadline))
		} else {
			remaining = append(remaining, 0)
		}
		downloadErrs = append(downloadErrs, nil)
		i := len(downloadErrs) - 1
		mu.Unlock()
		var err error
		if first {
			// A download far larger than the bound: it runs until its
			// ctx is cut.
			err = errDownloadNeverCut
			if awaitCanceled(ctx) {
				err = ctx.Err()
			}
		}
		mu.Lock()
		downloadErrs[i] = err
		mu.Unlock()
		return err
	})

	a := createSiteAgent(t, s, project, "as-bound-a", state.PhaseRunning, store.RunIntentRunning)
	b := createSiteAgent(t, s, project, "as-bound-b", state.PhaseRunning, store.RunIntentRunning)
	makeStalled(t, s, a.ID)
	makeStalled(t, s, b.ID)
	broker := connectFakeBroker(t, srv, a.RuntimeBrokerID)
	uploaded := answerBrokerUploads(t, broker, 0, 2)

	start := time.Now()
	srv.agentStalledDetectionHandler()(context.Background())
	elapsed := time.Since(start)

	require.Len(t, uploaded, 2, "fixture check: both sync-backs were tunneled to the broker")
	mu.Lock()
	defer mu.Unlock()
	require.Len(t, downloadErrs, 2, "fixture check: both sync-backs downloaded")
	require.ErrorIs(t, downloadErrs[0], context.DeadlineExceeded, "the first download is cut at its own bound")
	assert.NoError(t, downloadErrs[1], "the second agent's sync-back is not cut")
	assert.Greater(t, remaining[1], syncBound/2, "the second sync-back has a full bound of its own")
	assert.Greater(t, elapsed, tickBound, "fixture check: the batch outlasted the tick's bound")

	for _, id := range []string{a.ID, b.ID} {
		got := requireRunIntent(t, s, id, store.RunIntentStopped)
		assert.Equal(t, string(state.PhaseSuspended), got.Phase, "agent %s is suspended", id)
	}
}

// No new auto-suspend starts once the batch's start window has passed: the
// agents not reached are left running, with their intent untouched.
func TestAutoSuspend_StartWindowBoundsBatch(t *testing.T) {
	ctx := context.Background()
	srv, s := testServer(t)
	disp := &runIntentDispatcher{}
	srv.SetDispatcher(disp)
	_, _, agent := setupOnlineBrokerAgent(t, s, "as-window")
	_, err := s.SetRunIntent(ctx, agent.ID, store.RunIntentRunning)
	require.NoError(t, err)
	loaded, err := s.GetAgent(ctx, agent.ID)
	require.NoError(t, err)

	setAutoSuspendStartWindow(t, 0)
	srv.autoSuspendStalledAgents(ctx, []store.Agent{*loaded})

	assert.Zero(t, disp.stops.Load(), "no auto-suspend starts after the window")
	got := requireRunIntent(t, s, agent.ID, store.RunIntentRunning)
	assert.Equal(t, string(state.PhaseRunning), got.Phase)
}

// A cancelled scheduler ctx (shutdown) starts no new auto-suspend.
func TestAutoSuspend_CancelledSchedulerStartsNone(t *testing.T) {
	srv, s := testServer(t)
	disp := &runIntentDispatcher{}
	srv.SetDispatcher(disp)
	_, _, agent := setupOnlineBrokerAgent(t, s, "as-cancel")
	loaded, err := s.GetAgent(context.Background(), agent.ID)
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	srv.autoSuspendStalledAgents(ctx, []store.Agent{*loaded})

	assert.Zero(t, disp.stops.Load(), "no auto-suspend starts after shutdown")
}

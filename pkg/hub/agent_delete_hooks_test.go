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
	"errors"
	"log/slog"
	"net/http"
	"testing"
	"time"

	"github.com/GoogleCloudPlatform/scion/pkg/agent/state"
	"github.com/GoogleCloudPlatform/scion/pkg/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The deleted lifecycle-hook trigger (ptone/scion#2884): one deleted hook
// invocation per agent delete (soft and hard), none on a failed delete and
// none on a project deletion.

// wireDeletedHooks starts a hook evaluator on srv's event bus, wires it into
// srv and seeds an enabled hook for the deleted and stopped triggers.
func wireDeletedHooks(t *testing.T, srv *Server, s store.Store, bus EventPublisher) *hookRecordingExecutor {
	t.Helper()
	exec := &hookRecordingExecutor{}
	ev := NewLifecycleHookEvaluator(s, bus, exec, slog.Default())
	ev.Start()
	t.Cleanup(ev.Stop)
	srv.mu.Lock()
	srv.lifecycleHookEvaluator = ev
	srv.mu.Unlock()
	seedLifecycleHook(t, s, "h-deleted", store.LifecycleHookTriggerDeleted, true, nil)
	seedLifecycleHook(t, s, "h-stopped", store.LifecycleHookTriggerStopped, true, nil)
	return exec
}

// settleDeletedHooks waits for in-flight deleted hook dispatches and then
// gives the bus subscribers time to (wrongly) fire a second time.
func settleDeletedHooks(srv *Server) {
	srv.deletedHookDispatches.Wait()
	time.Sleep(100 * time.Millisecond)
}

func TestAgentDelete_FiresDeletedHookOnce(t *testing.T) {
	cases := []struct {
		name      string
		phase     state.Phase
		retention time.Duration
		query     string
		wantSoft  bool
	}{
		{"hard running", state.PhaseRunning, 0, "", false},
		{"soft running", state.PhaseRunning, time.Hour, "", true},
		{"force running", state.PhaseRunning, time.Hour, "?force=true", false},
		{"soft stopped", state.PhaseStopped, time.Hour, "", true},
		{"created", state.PhaseCreated, 0, "", false},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, s, pub, _ := engineTestServer(t)
			srv.config.SoftDeleteRetention = tc.retention
			exec := wireDeletedHooks(t, srv, s, pub.EventPublisher)
			agent := setupBrokerAgentInPhase(t, s, "dh-"+string(rune('a'+i)), tc.phase)

			rec := doRequest(t, srv, http.MethodDelete, "/api/v1/agents/"+agent.ID+tc.query, nil)
			require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())
			if tc.wantSoft {
				assert.False(t, mustGetAgent(t, s, agent.ID).DeletedAt.IsZero())
			} else {
				assert.True(t, agentGone(t, s, agent.ID))
			}

			settleDeletedHooks(srv)
			assert.Equal(t, 1, exec.fired(store.LifecycleHookTriggerDeleted), "exactly one deleted hook")
			assert.Zero(t, exec.fired(store.LifecycleHookTriggerStopped), "R1: a delete fires no stopped hook")

			// The deleted hook receives the deleted agent.
			calls := exec.agents()
			require.Len(t, calls, 1)
			assert.Equal(t, agent.ID, calls[0].ID)
			assert.Equal(t, agent.ProjectID, calls[0].ProjectID)
		})
	}
}

func TestAgentDelete_FailedDeleteFiresNoDeletedHook(t *testing.T) {
	t.Run("broker failure", func(t *testing.T) {
		srv, s, pub, disp := engineTestServer(t)
		exec := wireDeletedHooks(t, srv, s, pub.EventPublisher)
		disp.setFn(func(context.Context, *store.Agent) error { return errors.New("broker boom") })
		agent := setupBrokerAgentInPhase(t, s, "dh-fail-broker", state.PhaseRunning)

		rec := doRequest(t, srv, http.MethodDelete, "/api/v1/agents/"+agent.ID, nil)
		require.Equal(t, http.StatusBadGateway, rec.Code, rec.Body.String())

		settleDeletedHooks(srv)
		assert.Zero(t, exec.fired(store.LifecycleHookTriggerDeleted))
	})

	for _, tc := range []struct {
		name      string
		retention time.Duration
	}{
		{"soft finalize failure", time.Hour},
		{"hard finalize failure", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, s, pub, _ := engineTestServer(t)
			srv.config.SoftDeleteRetention = tc.retention
			exec := wireDeletedHooks(t, srv, s, pub.EventPublisher)
			agent := setupBrokerAgentInPhase(t, s, "dh-fail-"+string(rune('a'+int(tc.retention/time.Hour))), state.PhaseRunning)

			old := agentDeletionFinalizeSeam
			t.Cleanup(func() { agentDeletionFinalizeSeam = old })
			agentDeletionFinalizeSeam = func(context.Context, store.Store, *store.Agent, store.DeletionFinalizeMode) error {
				return errors.New("seam refused")
			}

			rec := doRequest(t, srv, http.MethodDelete, "/api/v1/agents/"+agent.ID, nil)
			require.Equal(t, http.StatusBadGateway, rec.Code, rec.Body.String())
			settleDeletedHooks(srv)
			assert.Zero(t, exec.fired(store.LifecycleHookTriggerDeleted), "a failed delete fires no deleted hook")

			// The retry that completes the delete fires it, once.
			agentDeletionFinalizeSeam = old
			rec = doRequest(t, srv, http.MethodDelete, "/api/v1/agents/"+agent.ID, nil)
			require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())
			settleDeletedHooks(srv)
			assert.Equal(t, 1, exec.fired(store.LifecycleHookTriggerDeleted))
		})
	}
}

func TestProjectDelete_FiresNoDeletedHook(t *testing.T) {
	srv, s := testServer(t)
	bus := NewChannelEventPublisher()
	t.Cleanup(bus.Close)
	srv.events = bus
	srv.SetDispatcher(&deleteDispatcher{})
	exec := wireDeletedHooks(t, srv, s, bus)
	project, _, agent := setupOnlineBrokerAgent(t, s, "dh-project")

	rec := doRequest(t, srv, http.MethodDelete, "/api/v1/projects/"+project.ID+"?force=true", nil)
	require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())
	assert.True(t, agentGone(t, s, agent.ID))

	settleDeletedHooks(srv)
	assert.Zero(t, exec.fired(store.LifecycleHookTriggerDeleted))
}

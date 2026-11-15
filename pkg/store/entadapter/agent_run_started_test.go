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

package entadapter

import (
	"context"
	"testing"
	"time"

	"github.com/GoogleCloudPlatform/scion/pkg/agent/state"
	"github.com/GoogleCloudPlatform/scion/pkg/store"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRecordAgentRunStarted_CurrentRunAndFirstStampWins(t *testing.T) {
	ctx := context.Background()
	s, projectID := newTestAgentStore(t)
	a := makeAgent(projectID, "run-started")
	require.NoError(t, s.CreateAgent(ctx, a))

	t1 := time.Date(2026, 11, 14, 10, 0, 0, 0, time.UTC)
	t2 := t1.Add(5 * time.Minute)
	t3 := t1.Add(15 * time.Minute)

	written, err := s.RecordAgentRunStarted(ctx, a.ID, "run-1", t1)
	require.NoError(t, err)
	assert.False(t, written, "no run recorded yet")

	_, err = s.SetAgentRunID(ctx, a.ID, "run-1", nil)
	require.NoError(t, err)

	written, err = s.RecordAgentRunStarted(ctx, a.ID, "", t1)
	require.NoError(t, err)
	assert.False(t, written, "empty run ID refused")

	written, err = s.RecordAgentRunStarted(ctx, a.ID, "run-other", t1)
	require.NoError(t, err)
	assert.False(t, written, "mismatched run ID refused")

	written, err = s.RecordAgentRunStarted(ctx, a.ID, "run-1", t1)
	require.NoError(t, err)
	assert.True(t, written, "first stamp for run-1 written")

	// A repeat for the same run keeps the first timestamp.
	written, err = s.RecordAgentRunStarted(ctx, a.ID, "run-1", t2)
	require.NoError(t, err)
	assert.False(t, written)

	got, err := s.GetAgent(ctx, a.ID)
	require.NoError(t, err)
	require.NotNil(t, got.RunStartedAt)
	assert.True(t, got.RunStartedAt.Equal(t1))
	assert.Equal(t, "run-1", got.RunStartedAtRunID)

	// Minting run-2 and reverting back to run-1 preserves run-1's timestamp.
	prev, err := s.SetAgentRunID(ctx, a.ID, "run-2", nil)
	require.NoError(t, err)
	assert.Equal(t, "run-1", prev)
	reverted, err := s.RevertAgentRunID(ctx, a.ID, "run-2", prev)
	require.NoError(t, err)
	assert.True(t, reverted)

	got, err = s.GetAgent(ctx, a.ID)
	require.NoError(t, err)
	assert.Equal(t, "run-1", got.RunID)
	assert.Equal(t, "run-1", got.RunStartedAtRunID)
	require.NotNil(t, got.RunStartedAt)
	assert.True(t, got.RunStartedAt.Equal(t1))

	// A landed new run accepts a fresh timestamp.
	_, err = s.SetAgentRunID(ctx, a.ID, "run-3", nil)
	require.NoError(t, err)
	written, err = s.RecordAgentRunStarted(ctx, a.ID, "run-3", t3)
	require.NoError(t, err)
	assert.True(t, written)

	got, err = s.GetAgent(ctx, a.ID)
	require.NoError(t, err)
	require.NotNil(t, got.RunStartedAt)
	assert.True(t, got.RunStartedAt.Equal(t3))
	assert.Equal(t, "run-3", got.RunStartedAtRunID)

	written, err = s.RecordAgentRunStarted(ctx, uuid.NewString(), "run-3", t3)
	require.NoError(t, err)
	assert.False(t, written, "missing agent")
}

func TestRecordAgentRunStarted_RefusedWhileDeleteHoldsRow(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC()
	live := now.Add(time.Hour)
	for _, tc := range []struct {
		name      string
		state     string
		deletedAt *time.Time
	}{
		{"deleting, live lease", store.DeletionStateDeleting, nil},
		{"finalizing", store.DeletionStateFinalizing, nil},
		{"soft-deleted", "", &now},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, projectID := newTestAgentStore(t)
			a := makeAgent(projectID, "run-started-delete")
			require.NoError(t, s.CreateAgent(ctx, a))
			_, err := s.SetAgentRunID(ctx, a.ID, "run-1", nil)
			require.NoError(t, err)
			set := store.DeletionFields{DeletedAt: tc.deletedAt}
			if tc.state != "" {
				st := tc.state
				set.State = &st
				set.LeaseAt = &live
			}
			n, err := s.UpdateAgentDeletion(ctx, a.ID, store.DeletionPredicate{}, set)
			require.NoError(t, err)
			require.Equal(t, 1, n)
			written, err := s.RecordAgentRunStarted(ctx, a.ID, "run-1", now)
			require.NoError(t, err)
			assert.False(t, written)
		})
	}
}

func TestListActiveAgentsForMaxDuration(t *testing.T) {
	ctx := context.Background()
	s, projectID := newTestAgentStore(t)
	base := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)

	seed := func(slug, phase, runID string, stamp *time.Time, nextRun string) *store.Agent {
		t.Helper()
		a := makeAgent(projectID, slug)
		a.Phase = phase
		require.NoError(t, s.CreateAgent(ctx, a))
		if runID != "" {
			_, err := s.SetAgentRunID(ctx, a.ID, runID, nil)
			require.NoError(t, err)
		}
		if stamp != nil {
			ok, err := s.RecordAgentRunStarted(ctx, a.ID, runID, *stamp)
			require.NoError(t, err)
			require.True(t, ok)
		}
		if nextRun != "" {
			_, err := s.SetAgentRunID(ctx, a.ID, nextRun, nil)
			require.NoError(t, err)
		}
		return a
	}

	tOld := base.Add(-2 * time.Hour)
	tMid := base.Add(-1 * time.Hour)
	tNew := base.Add(-30 * time.Minute)

	newer := seed("newer-running", string(state.PhaseRunning), "run-new", &tNew, "")
	oldest := seed("oldest-starting", string(state.PhaseStarting), "run-old", &tOld, "")
	middle := seed("middle-running", string(state.PhaseRunning), "run-mid", &tMid, "")
	_ = seed("stopped-agent", string(state.PhaseStopped), "run-stopped", &tOld, "")
	_ = seed("suspended-agent", string(state.PhaseSuspended), "run-susp", &tOld, "")
	_ = seed("no-stamp-agent", string(state.PhaseRunning), "run-nostamp", nil, "")
	// Starting a new run whose RunStartedAt has not been recorded yet must
	// not inherit the previous run's timestamp.
	_ = seed("starting-unsettled-next-run", string(state.PhaseStarting), "run-prev", &tOld, "run-next")

	got, err := s.ListActiveAgentsForMaxDuration(ctx)
	require.NoError(t, err)
	require.Len(t, got, 3)
	assert.Equal(t, []string{oldest.ID, middle.ID, newer.ID}, []string{got[0].ID, got[1].ID, got[2].ID})
}

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
	t2, t3 := t1.Add(5*time.Minute), t1.Add(15*time.Minute)
	checkRecord := func(id, runID string, at time.Time, want bool) {
		t.Helper()
		ok, err := s.RecordAgentRunStarted(ctx, id, runID, at)
		require.NoError(t, err)
		assert.Equal(t, want, ok)
	}
	checkStored := func(wantRun string, wantAt time.Time) {
		t.Helper()
		got, err := s.GetAgent(ctx, a.ID)
		require.NoError(t, err)
		require.NotNil(t, got.RunStartedAt)
		assert.Equal(t, [2]string{wantRun, wantRun}, [2]string{got.RunID, got.RunStartedAtRunID})
		assert.True(t, got.RunStartedAt.Equal(wantAt))
	}

	checkRecord(a.ID, "run-1", t1, false)
	_, err := s.SetAgentRunID(ctx, a.ID, "run-1", nil)
	require.NoError(t, err)
	checkRecord(a.ID, "", t1, false)
	checkRecord(a.ID, "run-other", t1, false)
	checkRecord(a.ID, "run-1", t1, true)
	checkRecord(a.ID, "run-1", t2, false) // repeat keeps first stamp
	checkStored("run-1", t1)

	prev, err := s.SetAgentRunID(ctx, a.ID, "run-2", nil)
	require.NoError(t, err)
	reverted, err := s.RevertAgentRunID(ctx, a.ID, "run-2", prev)
	require.NoError(t, err)
	assert.True(t, reverted)
	checkStored("run-1", t1)

	_, err = s.SetAgentRunID(ctx, a.ID, "run-3", nil)
	require.NoError(t, err)
	checkRecord(a.ID, "run-3", t3, true)
	checkStored("run-3", t3)
	checkRecord(uuid.NewString(), "run-3", t3, false)
}

func TestRecordAgentRunStarted_RefusedWhileDeleteHoldsRow(t *testing.T) {
	ctx, now := context.Background(), time.Now().UTC()
	s, projectID := newTestAgentStore(t)
	live, del, fin := now.Add(time.Hour), store.DeletionStateDeleting, store.DeletionStateFinalizing
	for i, set := range []store.DeletionFields{{State: &del, LeaseAt: &live}, {State: &fin, LeaseAt: &live}, {DeletedAt: &now}} {
		a := makeAgent(projectID, uuid.NewString()[:8]+string(rune('0'+i)))
		require.NoError(t, s.CreateAgent(ctx, a))
		_, err := s.SetAgentRunID(ctx, a.ID, "run-1", nil)
		require.NoError(t, err)
		n, err := s.UpdateAgentDeletion(ctx, a.ID, store.DeletionPredicate{}, set)
		require.NoError(t, err)
		require.Equal(t, 1, n)
		ok, err := s.RecordAgentRunStarted(ctx, a.ID, "run-1", now)
		require.NoError(t, err)
		assert.False(t, ok)
	}
}

func TestListActiveAgentsForMaxDuration(t *testing.T) {
	ctx, base := context.Background(), time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	s, projectID := newTestAgentStore(t)
	tOld, tMid, tNew, tYoung := base.Add(-2*time.Hour), base.Add(-time.Hour), base.Add(-30*time.Minute), base.Add(-5*time.Minute)
	seed := func(slug, phase, runID string, stamp *time.Time, nextRun string) string {
		a := makeAgent(projectID, slug)
		a.Phase = phase
		require.NoError(t, s.CreateAgent(ctx, a))
		if runID != "" {
			_, _ = s.SetAgentRunID(ctx, a.ID, runID, nil)
		}
		if stamp != nil {
			ok, err := s.RecordAgentRunStarted(ctx, a.ID, runID, *stamp)
			require.NoError(t, err)
			require.True(t, ok)
		}
		if nextRun != "" {
			_, _ = s.SetAgentRunID(ctx, a.ID, nextRun, nil)
		}
		return a.ID
	}

	newer, oldest, middle := seed("newer", string(state.PhaseRunning), "r-new", &tNew, ""), seed("oldest", string(state.PhaseStarting), "r-old", &tOld, ""), seed("middle", string(state.PhaseRunning), "r-mid", &tMid, "")
	seed("stopped", string(state.PhaseStopped), "r-stop", &tOld, "")
	seed("suspended", string(state.PhaseSuspended), "r-susp", &tOld, "")
	seed("no-stamp", string(state.PhaseRunning), "r-none", nil, "")
	seed("unsettled-next", string(state.PhaseStarting), "r-prev", &tOld, "r-next")
	seed("young-inside-grace", string(state.PhaseRunning), "r-young", &tYoung, "")

	got, err := s.ListActiveAgentsForMaxDuration(ctx, base.Add(-10*time.Minute))
	require.NoError(t, err)
	require.Len(t, got, 3)
	assert.Equal(t, []string{oldest, middle, newer}, []string{got[0].ID, got[1].ID, got[2].ID})
}

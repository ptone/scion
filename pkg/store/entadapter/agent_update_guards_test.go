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

// TestAgentStore_UpdateAgent_SoftDeletedGuards pins ptone/scion#9871:
//   - A stale UpdateAgent after a concurrent soft delete returns
//     store.ErrVersionConflict and leaves the row soft-deleted.
//   - A current-version UpdateAgent on a soft-deleted row (even with a zero
//     DeletedAt) returns store.ErrNotFound and never resurrects or mutates it.
//
// Both the non-running fast path and the running fast path are exercised.
func TestAgentStore_UpdateAgent_SoftDeletedGuards(t *testing.T) {
	ctx := context.Background()
	for _, targetPhase := range []string{string(state.PhaseStopped), string(state.PhaseRunning)} {
		t.Run("phase="+targetPhase, func(t *testing.T) {
			s, projectID := newTestAgentStore(t)

			a := makeAgent(projectID, "soft-guard-"+targetPhase)
			a.Phase = string(state.PhaseStopped)
			require.NoError(t, s.CreateAgent(ctx, a))

			stale, err := s.GetAgent(ctx, a.ID)
			require.NoError(t, err)

			deletedAt := time.Now().UTC().Truncate(time.Microsecond)
			softDeleteAgentAt(t, ctx, s, a.ID, deletedAt)

			// 1. Stale snapshot after soft delete -> ErrVersionConflict; row stays soft-deleted.
			stale.Phase = targetPhase
			stale.TaskSummary = "stale write after delete"
			stale.DeletedAt = time.Time{}
			err = s.UpdateAgent(ctx, stale)
			assert.ErrorIs(t, err, store.ErrVersionConflict)

			afterStale, err := s.GetAgent(ctx, a.ID)
			require.NoError(t, err)
			assert.False(t, afterStale.DeletedAt.IsZero(), "stale UpdateAgent must not clear DeletedAt")
			assert.Empty(t, afterStale.TaskSummary, "stale UpdateAgent must not mutate soft-deleted row")

			// 2. Current-version snapshot on soft-deleted row with zero DeletedAt -> ErrNotFound, never resurrects.
			resurrect := *afterStale
			resurrect.Phase = targetPhase
			resurrect.TaskSummary = "resurrect attempt"
			resurrect.DeletedAt = time.Time{}
			err = s.UpdateAgent(ctx, &resurrect)
			assert.ErrorIs(t, err, store.ErrNotFound)

			afterResurrect, err := s.GetAgent(ctx, a.ID)
			require.NoError(t, err)
			assert.False(t, afterResurrect.DeletedAt.IsZero(), "current-version UpdateAgent must not resurrect soft-deleted row")
			assert.Equal(t, afterStale.StateVersion, afterResurrect.StateVersion, "refused UpdateAgent must not bump state_version")
			assert.Empty(t, afterResurrect.TaskSummary, "refused UpdateAgent must not mutate soft-deleted row")
		})
	}
}

// TestAgentStore_UpdateAgent_PreservesLastSeen pins ptone/scion#9871:
// UpdateAgent never writes or clears last_seen, so a concurrent stale
// UpdateAgent after UpdateAgentStatus cannot rewind or clear LastSeen and
// cannot cause MarkStaleAgentsOffline to falsely mark a live running agent
// offline.
func TestAgentStore_UpdateAgent_PreservesLastSeen(t *testing.T) {
	ctx := context.Background()
	s, projectID := newTestAgentStore(t)

	now := time.Now().UTC().Truncate(time.Microsecond)
	oldSeen := now.Add(-15 * time.Minute)

	a := makeAgent(projectID, "last-seen-preserved")
	a.Phase = string(state.PhaseRunning)
	a.Activity = string(state.ActivityThinking)
	a.LastSeen = oldSeen
	require.NoError(t, s.CreateAgent(ctx, a))

	// Take a snapshot carrying the old LastSeen (StateVersion = 1).
	stale, err := s.GetAgent(ctx, a.ID)
	require.NoError(t, err)
	require.True(t, stale.LastSeen.Equal(oldSeen))

	// A concurrent heartbeat refreshes LastSeen via UpdateAgentStatus (does not bump StateVersion).
	require.NoError(t, s.UpdateAgentStatus(ctx, a.ID, store.AgentStatusUpdate{
		Phase:     string(state.PhaseRunning),
		Activity:  string(state.ActivityThinking),
		Heartbeat: true,
	}))
	refreshed, err := s.GetAgent(ctx, a.ID)
	require.NoError(t, err)
	require.True(t, refreshed.LastSeen.After(oldSeen), "UpdateAgentStatus(Heartbeat: true) must advance LastSeen")

	// A metadata-only UpdateAgent from the stale snapshot succeeds on the CAS,
	// but must NOT rewind LastSeen to oldSeen.
	stale.Labels = map[string]string{"team": "core"}
	require.NoError(t, s.UpdateAgent(ctx, stale))

	got, err := s.GetAgent(ctx, a.ID)
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"team": "core"}, got.Labels)
	assert.True(t, got.LastSeen.Equal(refreshed.LastSeen), "UpdateAgent must not rewind LastSeen (got %v, want %v)", got.LastSeen, refreshed.LastSeen)

	// Zeroing LastSeen in the caller struct must also not clear LastSeen.
	got.LastSeen = time.Time{}
	got.TaskSummary = "clear-attempt"
	require.NoError(t, s.UpdateAgent(ctx, got))

	got2, err := s.GetAgent(ctx, a.ID)
	require.NoError(t, err)
	assert.True(t, got2.LastSeen.Equal(refreshed.LastSeen), "UpdateAgent with zero LastSeen must not clear LastSeen (got %v, want %v)", got2.LastSeen, refreshed.LastSeen)

	// MarkStaleAgentsOffline with cutoff now-5m must NOT mark the live agent offline.
	offline, err := s.MarkStaleAgentsOffline(ctx, refreshed.LastSeen.Add(-5*time.Minute))
	require.NoError(t, err)
	assert.Empty(t, offline, "live agent whose LastSeen was refreshed must not be marked offline")
}

// TestAgentStore_RestoreAgent pins RestoreAgent's CAS, soft-delete predicate,
// and not-found semantics (ptone/scion#9871).
func TestAgentStore_RestoreAgent(t *testing.T) {
	ctx := context.Background()
	s, projectID := newTestAgentStore(t)

	a := makeAgent(projectID, "restore-target")
	require.NoError(t, s.CreateAgent(ctx, a))

	// Restoring a live (non-soft-deleted) agent with current version returns ErrNotFound.
	live, err := s.GetAgent(ctx, a.ID)
	require.NoError(t, err)
	_, err = s.RestoreAgent(ctx, live.ID, live.StateVersion)
	assert.ErrorIs(t, err, store.ErrNotFound)

	// Restoring a non-existent agent returns ErrNotFound.
	_, err = s.RestoreAgent(ctx, uuid.NewString(), 1)
	assert.ErrorIs(t, err, store.ErrNotFound)

	// Soft-delete the agent.
	deletedAt := time.Now().UTC().Truncate(time.Microsecond)
	softDeleteAgentAt(t, ctx, s, a.ID, deletedAt)

	deleted, err := s.GetAgent(ctx, a.ID)
	require.NoError(t, err)
	require.False(t, deleted.DeletedAt.IsZero())

	// Stale version on a soft-deleted row returns ErrVersionConflict and leaves row soft-deleted.
	_, err = s.RestoreAgent(ctx, deleted.ID, live.StateVersion)
	assert.ErrorIs(t, err, store.ErrVersionConflict)
	stillDeleted, err := s.GetAgent(ctx, a.ID)
	require.NoError(t, err)
	assert.False(t, stillDeleted.DeletedAt.IsZero())

	// Matching version succeeds: clears DeletedAt and bumps StateVersion.
	newVersion, err := s.RestoreAgent(ctx, deleted.ID, deleted.StateVersion)
	require.NoError(t, err)
	assert.Equal(t, deleted.StateVersion+1, newVersion)

	restored, err := s.GetAgent(ctx, a.ID)
	require.NoError(t, err)
	assert.True(t, restored.DeletedAt.IsZero(), "RestoreAgent must clear DeletedAt")
	assert.Equal(t, newVersion, restored.StateVersion)

	// A second RestoreAgent with the old (deleted) version returns ErrVersionConflict.
	_, err = s.RestoreAgent(ctx, deleted.ID, deleted.StateVersion)
	assert.ErrorIs(t, err, store.ErrVersionConflict)
}

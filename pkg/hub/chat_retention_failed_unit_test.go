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

package hub

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/GoogleCloudPlatform/scion/pkg/config/opsettings"
	"github.com/GoogleCloudPlatform/scion/pkg/store"
)

// seedFailed adds n failed agent messages of project, a minute apart,
// starting 30 days before the clock.
func (h *chatRetentionUnitHarness) seedFailed(n int, project string) {
	base := h.clock.t.Add(-30 * 24 * time.Hour)
	for i := 0; i < n; i++ {
		h.st.add(fmt.Sprintf("%s-f%06d", project, i), base.Add(time.Duration(i)*time.Minute), project, store.MessageDispatchFailed)
	}
}

// TestFailedPurge_DeletesOnlyFailedAgentRows: the walk deletes every failed
// agent message older than the cutoff over several chunks and stops at the
// short chunk; pending, deferred, delivered, "user:" recipient and newer
// rows stay. It counts under Failed, writes no cursor and opens no run.
func TestFailedPurge_DeletesOnlyFailedAgentRows(t *testing.T) {
	h := newChatRetentionUnitHarness()
	h.seedFailed(5, "p1")
	old := h.clock.t.Add(-20 * 24 * time.Hour)
	h.st.add("keep-pending", old, "p1", store.MessageDispatchPending)
	h.st.add("keep-deferred", old, "p1", store.MessageDispatchDeferred)
	h.st.add("keep-dispatched", old, "p1", store.MessageDispatchDispatched)
	h.st.msgs = append(h.st.msgs, fakeChatRetentionMsg{id: "keep-user", created: old, project: "p1", state: store.MessageDispatchFailed, recipient: "user:alice"})
	h.st.add("keep-recent", h.clock.t.Add(-time.Hour), "p1", store.MessageDispatchFailed)
	cfg := chatRetentionTestConfig(func(c *opsettings.ChatRetentionConfig) { c.ChunkSize = 2 })

	res, err := h.sw.PurgeFailed(context.Background(), h.clock.t.Add(-7*24*time.Hour), cfg)
	require.NoError(t, err)
	assert.True(t, res.Done)
	assert.False(t, res.BudgetExhausted)
	assert.Equal(t, 3, res.Chunks)
	assert.Equal(t, []int{2, 2, 2}, res.ChunkSizes)
	assert.Equal(t, store.ChatRetentionCounts{Failed: 5, Mentions: 5}, res.Total())
	assert.Len(t, h.pauses, 2, "a pause after each full chunk")

	var kept []string
	for _, m := range h.st.msgs {
		kept = append(kept, m.id)
	}
	assert.ElementsMatch(t, []string{"keep-pending", "keep-deferred", "keep-dispatched", "keep-user", "keep-recent"}, kept)
	assert.False(t, h.st.hasCursor, "no cursor is written")
	assert.Empty(t, h.st.runs, "no run is opened")
	assert.Zero(t, h.st.purgeCalls, "the main purge is not used")
}

// TestFailedPurge_SlowTxShrinksAndBudgetStops: slow transactions halve the
// chunk down to the floor, every transaction runs under the chunk timeout,
// the pause is chat_retention.sweep.pause_ms, and the walk stops before
// the handler deadline leaves no room for a last chunk.
func TestFailedPurge_SlowTxShrinksAndBudgetStops(t *testing.T) {
	h := newChatRetentionUnitHarness()
	h.seedFailed(10000, "p1")
	h.st.purgeCost = func(int) time.Duration { return 300 * time.Millisecond }
	cfg := chatRetentionTestConfig(nil)
	ctx, cancel := context.WithTimeout(context.Background(), failedMessageRetentionTimeout)
	defer cancel()

	res, err := h.sw.PurgeFailed(ctx, h.clock.t.Add(-7*24*time.Hour), cfg)
	require.NoError(t, err)
	assert.True(t, res.BudgetExhausted)
	assert.False(t, res.Done)
	require.GreaterOrEqual(t, len(res.ChunkSizes), 5)
	assert.Equal(t, []int{200, 100, 50, 25, 25}, res.ChunkSizes[:5])
	assert.Equal(t, 300*time.Millisecond, res.MaxTx)
	// The 30 s deadline leaves 30 - 5 (chunk timeout) - 2 (margin) = 23 s
	// of walk: (300 ms tx + 500 ms pause) per chunk.
	assert.InDelta(t, 29, res.Chunks, 1)
	for _, p := range h.pauses {
		assert.Equal(t, 500*time.Millisecond, p)
	}
	for _, d := range h.st.deadlines {
		assert.True(t, d, "each chunk runs under a timeout")
	}
}

// TestFailedPurge_PurgeErrorStops: a failed purge stops the walk with the
// error and keeps the counts of the chunks committed before it.
func TestFailedPurge_PurgeErrorStops(t *testing.T) {
	h := newChatRetentionUnitHarness()
	h.seedFailed(10, "p1")
	h.st.purgeErr = errors.New("disk on fire")
	res, err := h.sw.PurgeFailed(context.Background(), h.clock.t, chatRetentionTestConfig(nil))
	require.ErrorContains(t, err, "disk on fire")
	assert.Zero(t, res.Chunks)
	assert.Len(t, h.st.msgs, 10)
}

// TestFailedPurge_ContextDone: when the handler's context ends mid-walk the
// walk stops with its error and reports what it committed.
func TestFailedPurge_ContextDone(t *testing.T) {
	h := newChatRetentionUnitHarness()
	h.seedFailed(1000, "p1")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h.st.afterPurge = func(call int) {
		if call == 2 {
			cancel()
		}
	}
	res, err := h.sw.PurgeFailed(ctx, h.clock.t, chatRetentionTestConfig(nil))
	require.ErrorIs(t, err, context.Canceled)
	assert.Equal(t, 2, res.Chunks)
	assert.Equal(t, int64(400), res.Total().Failed)
}

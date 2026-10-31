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
	"time"

	"github.com/GoogleCloudPlatform/scion/pkg/config/opsettings"
	"github.com/GoogleCloudPlatform/scion/pkg/store"
)

// The failed-message purge (design chat message retention §7.4) deletes
// messages to agents whose dispatch failed once they are older than
// messaging.failed_message_retention_days. It is not gated by
// hub.chat_retention or by chat_retention.enabled. It walks the created
// index in chunks and deletes each chunk with the chat retention cascade,
// so no message_addressees, webchat_mention, webchat_message_ext,
// webchat_message_attachment or webchat_message_star row is left behind.
// The pacing is the chat retention sweep's: chat_retention.sweep's chunk
// size (adapted to the transaction time), pause and tick budget, with the
// budget also bounded by the handler's deadline.

// failedPurgeResult describes one failed-message purge run.
type failedPurgeResult struct {
	// Chunks is the number of purge transactions committed.
	Chunks int
	// ByProject holds the deleted rows per project ID; messages are
	// counted under Failed.
	ByProject map[string]store.ChatRetentionCounts
	// Done is true when the walk returned a short chunk: every failed
	// agent message older than the cutoff is gone.
	Done bool
	// BudgetExhausted is true when the run stopped at its budget.
	BudgetExhausted bool
	// MaxTx is the longest purge transaction.
	MaxTx time.Duration
	// ChunkSizes are the requested chunk sizes, in order.
	ChunkSizes []int
}

// Total sums r over every project.
func (r failedPurgeResult) Total() store.ChatRetentionCounts {
	var t store.ChatRetentionCounts
	for _, c := range r.ByProject {
		t.Add(c)
	}
	return t
}

// PurgeFailed deletes failed agent messages created before cutoff, oldest
// first, in paced chunks, until none is left, the budget passes, ctx ends
// or a chunk fails. The walk keeps its (created, id) position in memory
// only: every row it returns is deleted, so the next run starts again at
// the oldest remaining one.
//
// The selection runs on ctx (the handler's deadline), not on the chunk
// timeout: with chat retention off, the created-index range below the
// cutoff can be most of the table, and a selection may have to pass many
// delivered rows to fill a chunk. The purge transaction has the chunk
// timeout.
func (sw *chatRetentionSweeper) PurgeFailed(ctx context.Context, cutoff time.Time, cfg opsettings.ChatRetentionConfig) (failedPurgeResult, error) {
	res := failedPurgeResult{ByProject: map[string]store.ChatRetentionCounts{}}
	start := sw.now()
	remaining, hasDeadline := chatRetentionTimeLeft(ctx)
	budgetEnd := chatRetentionBudgetEnd(start, remaining, hasDeadline, cfg)
	chunk := cfg.ChunkSize
	var pos store.ChatRetentionPosition
	for {
		if err := ctx.Err(); err != nil {
			return res, err
		}
		if !sw.now().Before(budgetEnd) {
			res.BudgetExhausted = true
			return res, nil
		}
		res.ChunkSizes = append(res.ChunkSizes, chunk)
		cands, err := sw.store.SelectFailedMessages(ctx, cutoff, pos, chunk)
		if err != nil {
			return res, err
		}
		if len(cands) == 0 {
			res.Done = true
			return res, nil
		}
		ids := make([]string, len(cands))
		projectOf := make(map[string]string, len(cands))
		for i, c := range cands {
			ids[i] = c.ID
			projectOf[c.ID] = c.ProjectID
		}
		last := cands[len(cands)-1]
		pos = store.ChatRetentionPosition{Created: last.Created, ID: last.ID}

		tx, err := sw.purgeFailedChunk(ctx, ids, projectOf, &res)
		if err != nil {
			return res, err
		}
		if len(cands) < chunk {
			res.Done = true
			return res, nil
		}
		chunk = nextChatRetentionChunk(chunk, cfg.ChunkSize, tx)
		if !sw.now().Add(cfg.Pause()).Before(budgetEnd) {
			res.BudgetExhausted = true
			return res, nil
		}
		if err := sw.sleep(ctx, cfg.Pause()); err != nil {
			return res, err
		}
	}
}

// purgeFailedChunk deletes one chunk under the chunk timeout and adds its
// counts to res. It returns the transaction's duration.
func (sw *chatRetentionSweeper) purgeFailedChunk(ctx context.Context, ids []string, projectOf map[string]string, res *failedPurgeResult) (time.Duration, error) {
	cctx, cancel := context.WithTimeout(ctx, chatRetentionChunkTimeout)
	defer cancel()
	t0 := sw.now()
	pr, err := sw.store.PurgeFailedMessages(cctx, ids, projectOf)
	tx := sw.now().Sub(t0)
	if err != nil {
		return tx, err
	}
	res.Chunks++
	if tx > res.MaxTx {
		res.MaxTx = tx
	}
	for project, c := range pr.ByProject {
		acc := res.ByProject[project]
		acc.Add(c)
		res.ByProject[project] = acc
	}
	return tx, nil
}

// addFailedPurgeToSweepRun adds the counts of a failed-message purge to the
// open chat retention sweep run (design §6.6), and reports whether it did.
// It never opens a run: with chat retention off (experiment or enabled), or
// no sweep run started in the last chatRetentionRunWindow, it does nothing
// and the caller only logs the counts.
//
// The run row is read, updated and written back, so the write must not
// interleave with a chat retention tick, which holds the same row in memory
// and writes it at its end. It holds the in-process tick guard and the
// chat retention advisory lock for the update; when a tick holds either,
// the counts are not recorded in the run.
func (s *Server) addFailedPurgeToSweepRun(ctx context.Context, st store.ChatRetentionStore, byProject map[string]store.ChatRetentionCounts, now time.Time) (bool, error) {
	if len(byProject) == 0 || !s.chatRetentionPolicyAt(now).Active {
		return false, nil
	}
	if !s.chatRetention.running.CompareAndSwap(false, true) {
		return false, nil
	}
	defer s.chatRetention.running.Store(false)
	if locker, ok := s.store.(store.AdvisoryLocker); ok {
		acquired, release, err := locker.TryAdvisoryLock(ctx, store.LockChatRetention)
		if err != nil {
			return false, err
		}
		defer func() { _ = release() }()
		if !acquired {
			return false, nil
		}
	}

	run, err := st.LatestChatRetentionRun(ctx, store.ChatRetentionTriggerSweep, now.Add(-chatRetentionRunWindow))
	if err != nil || run == nil {
		return false, err
	}
	if run.Counts == nil {
		run.Counts = map[string]store.ChatRetentionCounts{}
	}
	for project, c := range byProject {
		acc := run.Counts[project]
		acc.Add(c)
		run.Counts[project] = acc
	}
	if err := st.SaveChatRetentionRun(ctx, run); err != nil {
		return false, err
	}
	return true, nil
}

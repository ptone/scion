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

package telemetry

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"time"
)

// DrainStats summarizes a one-shot broker harvest drain of the Hub usage outbox.
type DrainStats struct {
	// NoOutbox is true when <home>/.scion/analytics-outbox-hub does not exist.
	// DrainOutboxOnce creates no files or directories in that case.
	NoOutbox bool
	// HubMismatch is true when the outbox is pinned to a different hub_id than
	// verifiedHubID or has recorded a relocation marker. Nothing is drained.
	HubMismatch bool
	// EventsDrained is the number of usage events delivered to the Hub in this call.
	EventsDrained int
	// EventsPending is the number of usage event lines still undelivered in
	// staging, finalizing, or ready files after this call.
	EventsPending int
	// PendingBytes is the undelivered byte count remaining after this call.
	PendingBytes int64
	// OldestPending is the modification time of the oldest pending file, or
	// zero when nothing is pending.
	OldestPending time.Time
}

// DrainOutboxOnce performs a one-shot, broker-safe finalize and drain of the
// Hub analytics outbox under homeDir (<home>/.scion/analytics-outbox-hub).
//
// Contract:
//  1. Returns DrainStats{NoOutbox: true}, nil without creating any files or
//     directories if <home>/.scion/analytics-outbox-hub is absent.
//  2. Takes the outbox lock with a bounded wait (never stealing the lock).
//  3. Checks the outbox's pinned hub_id and relocation marker; if pinned differs
//     from verifiedHubID or relocated is set, returns DrainStats{HubMismatch: true},
//     nil and drains nothing (never stamping a different hub_id).
//  4. Finalizes any crash-left staging.jsonl / finalizing-*.jsonl into ready
//     files using the verified pinned hub_id.
//  5. Drains ready-*.jsonl via HubUsageSink + DrainBatch until empty or ctx
//     expires.
//  6. Preserves file ownership when running as root (inferring owner from the
//     outbox directory if owner is nil).
func DrainOutboxOnce(
	ctx context.Context,
	homeDir string,
	owner *OutboxOwner,
	post UsageIngestPostFunc,
	verifiedHubID string,
) (DrainStats, error) {
	if err := ctx.Err(); err != nil {
		return DrainStats{}, err
	}
	dir := HubAnalyticsOutboxDirFor(homeDir)
	if dir == "" {
		return DrainStats{NoOutbox: true}, nil
	}
	info, err := os.Stat(dir)
	if errors.Is(err, os.ErrNotExist) {
		return DrainStats{NoOutbox: true}, nil
	}
	if err != nil {
		return DrainStats{}, fmt.Errorf("analytics outbox: stat %s: %w", dir, err)
	}
	if !info.IsDir() {
		return DrainStats{}, fmt.Errorf("analytics outbox: %s is not a directory", dir)
	}
	if post == nil {
		return DrainStats{}, errors.New("analytics outbox: post func is required")
	}

	if owner == nil {
		owner = inferOutboxOwner(dir)
	}

	o := newOutboxHandle(dir, owner)
	if deadline, ok := ctx.Deadline(); ok {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return DrainStats{}, ctx.Err()
		}
		if remaining < analyticsLockWait {
			o.lockWait = remaining
		}
	}

	// Step 1: check pin/relocation and emptiness under the bounded lock before
	// mutating or draining anything.
	unlock, err := o.lock()
	if err != nil {
		return DrainStats{}, err
	}
	relocated, rerr := o.readTrimmed("relocated")
	if rerr != nil {
		unlock()
		return DrainStats{}, rerr
	}
	if relocated != "" {
		stats := o.pendingStatsLocked()
		stats.HubMismatch = true
		unlock()
		return stats, nil
	}
	pinned, perr := o.readTrimmed("hub_id")
	if perr != nil {
		unlock()
		return DrainStats{}, perr
	}
	if pinned != "" && verifiedHubID != "" && pinned != verifiedHubID {
		stats := o.pendingStatsLocked()
		stats.HubMismatch = true
		unlock()
		return stats, nil
	}
	initialStats := o.pendingStatsLocked()
	unlock()

	// If nothing is pending at all and the hub_id isn't mismatched, return
	// immediately without touching any files.
	if initialStats.PendingBytes == 0 && initialStats.EventsPending == 0 {
		return DrainStats{}, nil
	}

	// Step 2: finalize any crash-left staging.jsonl / finalizing-*.jsonl.
	// finalize refuses if verifiedHubID is empty or doesn't match the pinned
	// hub_id, and never stamps a different hub_id.
	_, finErr := o.finalize(ctx, verifiedHubID)
	if errors.Is(finErr, ErrAnalyticsOutboxRelocated) {
		return DrainStats{HubMismatch: true}, nil
	}
	if errors.Is(finErr, ErrAnalyticsHubNotVerified) {
		return o.snapshotPendingStats(0), finErr
	}
	if errors.Is(finErr, ErrAnalyticsOutboxLocked) || errors.Is(finErr, context.Canceled) || errors.Is(finErr, context.DeadlineExceeded) {
		return o.snapshotPendingStats(0), finErr
	}

	// Step 3: drain ready-*.jsonl via HubUsageSink + DrainBatch.
	sink := &HubUsageSink{
		cfg: HubUsageSinkConfig{
			Post:  post,
			HubID: verifiedHubID,
		},
		outbox: o,
		now:    time.Now,
	}
	drained, drainErr := o.DrainBatch(ctx, sink.cfg.maxBatchEvents(), sink.EffectiveMaxBatchBytes(), sink.SendBatch)

	stats := o.snapshotPendingStats(drained)
	return stats, errors.Join(finErr, drainErr)
}

func (o *AnalyticsOutbox) snapshotPendingStats(drained int) DrainStats {
	stats := DrainStats{EventsDrained: drained}
	unlock, err := o.lock()
	if err != nil {
		return stats
	}
	defer unlock()
	p := o.pendingStatsLocked()
	p.EventsDrained = drained
	return p
}

func (o *AnalyticsOutbox) pendingStatsLocked() DrainStats {
	pendingBytes, oldest, err := o.pendingBytes()
	if err != nil {
		return DrainStats{}
	}
	return DrainStats{
		EventsPending: o.countPendingEventsLocked(),
		PendingBytes:  pendingBytes,
		OldestPending: oldest,
	}
}

func (o *AnalyticsOutbox) countPendingEventsLocked() int {
	total := 0
	total += countNonEmptyLinesFrom(o.path("staging.jsonl"), 0)
	if fin, err := o.finalizingFiles(); err == nil {
		for _, name := range fin {
			total += countNonEmptyLinesFrom(o.path(name), 0)
		}
	}
	curFile, curOffset, err := o.readCursor()
	if err != nil {
		return total
	}
	files, err := o.readyFiles()
	if err != nil {
		return total
	}
	for _, n := range files {
		if n < curFile {
			continue
		}
		var offset int64
		if n == curFile {
			offset = curOffset
		}
		total += countNonEmptyLinesFrom(o.path(readyName(n)), offset)
	}
	return total
}

func countNonEmptyLinesFrom(path string, offset int64) int {
	f, err := os.Open(path)
	if err != nil {
		return 0
	}
	defer func() { _ = f.Close() }()
	if offset > 0 {
		if _, err := f.Seek(offset, io.SeekStart); err != nil {
			return 0
		}
	}
	r := bufio.NewReaderSize(f, 32<<10)
	count := 0
	for {
		line, err := r.ReadBytes('\n')
		if len(line) > 0 {
			trimmed := line
			if trimmed[len(trimmed)-1] == '\n' {
				trimmed = trimmed[:len(trimmed)-1]
			}
			if len(trimmed) > 0 {
				count++
			}
		}
		if err != nil {
			break
		}
	}
	return count
}

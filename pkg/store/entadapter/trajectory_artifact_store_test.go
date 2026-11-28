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
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/GoogleCloudPlatform/scion/pkg/store"
	"github.com/GoogleCloudPlatform/scion/pkg/store/enttest"
	"github.com/GoogleCloudPlatform/scion/pkg/telemetrycontract"
)

func TestTrajectoryArtifactStore_PrepareCommitAndCaps(t *testing.T) {
	ctx := context.Background()
	client := enttest.NewClient(t)
	cs := NewCompositeStore(client)

	now := time.Now().UTC().Truncate(time.Microsecond)
	hashA := strings.Repeat("a", 64)
	hashB := strings.Repeat("b", 64)
	stampedHashA := strings.Repeat("d", 64)

	base := &store.TrajectoryArtifact{
		HubID:             "hub-1",
		ProjectID:         "proj-1",
		AgentID:           "agent-1",
		RunID:             "run-1",
		Harness:           "claude",
		ArtifactType:      telemetrycontract.TrajectoryArtifactTypeUsageRecords,
		ParserVersion:     "claude-1",
		ChunkID:           "chunk-1",
		InventorySeq:      1,
		RowCount:          5,
		ContentEncoding:   telemetrycontract.TrajectoryContentEncodingGzip,
		Size:              512,
		SHA256:            hashA,
		PendingObjectPath: "hub-1/trajectories/v1/l1-pending/art-1",
		ObjectPath:        "hub-1/analytics/v1/usage_records/dt=2026-11-28/project=proj-1/agent-1__run-1__pclaude-1__s1__chunk-1.jsonl.gz",
		CreatedAt:         now,
		PrepareExpiresAt:  now.Add(15 * time.Minute),
	}

	prep1, err := cs.PrepareTrajectoryArtifact(ctx, base)
	require.NoError(t, err)
	require.NotEmpty(t, prep1.ID)
	assert.Equal(t, telemetrycontract.TrajectoryArtifactStatusPending, prep1.Status)
	assert.Equal(t, int64(512), prep1.ChargedBytes)

	// Pending refresh with smaller size keeps high-water charged_bytes.
	smaller := *base
	smaller.Size = 256
	refreshed, err := cs.PrepareTrajectoryArtifact(ctx, &smaller)
	require.NoError(t, err)
	assert.Equal(t, prep1.ID, refreshed.ID)
	assert.Equal(t, int64(256), refreshed.Size)
	assert.Equal(t, int64(512), refreshed.ChargedBytes)

	sum, err := cs.SumTrajectoryRunArtifactBytes(ctx, "agent-1", "run-1", "")
	require.NoError(t, err)
	assert.Equal(t, int64(512), sum)

	sumExcl, err := cs.SumTrajectoryRunArtifactBytes(ctx, "agent-1", "run-1", prep1.ID)
	require.NoError(t, err)
	assert.Equal(t, int64(0), sumExcl)

	cnt, err := cs.CountTrajectoryRunArtifacts(ctx, "agent-1", "run-1", "")
	require.NoError(t, err)
	assert.Equal(t, int64(1), cnt)

	// Commit with matching expectation succeeds.
	committed, err := cs.CommitTrajectoryArtifact(ctx, prep1.ID, 600, stampedHashA, now.Add(time.Second), store.TrajectoryArtifactCommitExpectation{
		Size:     256,
		SHA256:   hashA,
		RowCount: 5,
		Final:    false,
	})
	require.NoError(t, err)
	assert.Equal(t, telemetrycontract.TrajectoryArtifactStatusCommitted, committed.Status)
	assert.Equal(t, int64(600), committed.StampedSize)
	assert.Equal(t, stampedHashA, committed.StampedSHA256)
	require.NotNil(t, committed.CommittedAt)

	// Idempotent prepare replay with same sha256 returns committed row.
	replay, err := cs.PrepareTrajectoryArtifact(ctx, &smaller)
	require.NoError(t, err)
	assert.Equal(t, prep1.ID, replay.ID)
	assert.Equal(t, telemetrycontract.TrajectoryArtifactStatusCommitted, replay.Status)

	// Prepare replay with different sha256 returns ErrTrajectoryArtifactConflict.
	conflict := smaller
	conflict.SHA256 = hashB
	_, err = cs.PrepareTrajectoryArtifact(ctx, &conflict)
	assert.ErrorIs(t, err, store.ErrTrajectoryArtifactConflict)
}

func TestTrajectorySessionStore_PrecedenceAndConcurrency(t *testing.T) {
	ctx := context.Background()
	client := enttest.NewClient(t)
	cs := NewCompositeStore(client)

	path := ".claude/projects/-workspace/sess-1.jsonl"
	now := time.Now().UTC().Truncate(time.Microsecond)

	// Initial insert: (claude-2, 200).
	got, updated, err := cs.UpsertTrajectorySession(ctx, &store.TrajectorySession{
		HubID:              "hub-1",
		ProjectID:          "proj-1",
		AgentID:            "agent-1",
		RunID:              "run-1",
		Harness:            "claude",
		SessionID:          "sess-1",
		NativeRelativePath: path,
		ParserVersion:      "claude-2",
		ParsedOffset:       200,
		ATIFArtifactID:     "art-2-200",
		ATIFObjectPath:     "path/2-200",
		ATIFSchemaVersion:  telemetrycontract.TrajectoryATIFSchemaVersionV18,
		FileFormat:         telemetrycontract.TrajectoryFileFormatJSON,
		StepCount:          4,
		NormalizeStatus:    telemetrycontract.TrajectorySessionNormalizeStatusOK,
		UpdatedAt:          now,
	})
	require.NoError(t, err)
	assert.True(t, updated)
	assert.Equal(t, "claude-2", got.ParserVersion)
	assert.Equal(t, int64(2), got.ParserVersionNum)
	assert.Equal(t, int64(200), got.ParsedOffset)

	// Older offset for same parser_version must NOT overwrite.
	got, updated, err = cs.UpsertTrajectorySession(ctx, &store.TrajectorySession{
		HubID:              "hub-1",
		ProjectID:          "proj-1",
		AgentID:            "agent-1",
		RunID:              "run-1",
		Harness:            "claude",
		SessionID:          "sess-1",
		NativeRelativePath: path,
		ParserVersion:      "claude-2",
		ParsedOffset:       100,
		ATIFArtifactID:     "art-2-100",
		ATIFObjectPath:     "path/2-100",
		StepCount:          2,
		NormalizeStatus:    telemetrycontract.TrajectorySessionNormalizeStatusOK,
		UpdatedAt:          now.Add(time.Second),
	})
	require.NoError(t, err)
	assert.False(t, updated)
	assert.Equal(t, "art-2-200", got.ATIFArtifactID)
	assert.Equal(t, int64(200), got.ParsedOffset)

	// Older parser_version with larger offset must NOT overwrite.
	got, updated, err = cs.UpsertTrajectorySession(ctx, &store.TrajectorySession{
		HubID:              "hub-1",
		ProjectID:          "proj-1",
		AgentID:            "agent-1",
		RunID:              "run-1",
		Harness:            "claude",
		SessionID:          "sess-1",
		NativeRelativePath: path,
		ParserVersion:      "claude-1",
		ParsedOffset:       9999,
		ATIFArtifactID:     "art-1-9999",
		ATIFObjectPath:     "path/1-9999",
		StepCount:          99,
		NormalizeStatus:    telemetrycontract.TrajectorySessionNormalizeStatusOK,
		UpdatedAt:          now.Add(2 * time.Second),
	})
	require.NoError(t, err)
	assert.False(t, updated)
	assert.Equal(t, "claude-2", got.ParserVersion)
	assert.Equal(t, "art-2-200", got.ATIFArtifactID)

	// Newer parser_version with numeric suffix > 9 (claude-10 > claude-2) MUST overwrite even with smaller offset.
	got, updated, err = cs.UpsertTrajectorySession(ctx, &store.TrajectorySession{
		HubID:              "hub-1",
		ProjectID:          "proj-1",
		AgentID:            "agent-1",
		RunID:              "run-1",
		Harness:            "claude",
		SessionID:          "sess-1",
		NativeRelativePath: path,
		ParserVersion:      "claude-10",
		ParsedOffset:       50,
		ATIFArtifactID:     "art-10-50",
		ATIFObjectPath:     "path/10-50",
		ATIFSchemaVersion:  telemetrycontract.TrajectoryATIFSchemaVersionV18,
		FileFormat:         telemetrycontract.TrajectoryFileFormatJSON,
		StepCount:          1,
		NormalizeStatus:    telemetrycontract.TrajectorySessionNormalizeStatusOK,
		UpdatedAt:          now.Add(3 * time.Second),
	})
	require.NoError(t, err)
	assert.True(t, updated)
	assert.Equal(t, "claude-10", got.ParserVersion)
	assert.Equal(t, int64(10), got.ParserVersionNum)
	assert.Equal(t, int64(50), got.ParsedOffset)
	assert.Equal(t, "art-10-50", got.ATIFArtifactID)

	// Standalone parse_failed at same (claude-10, 50) updates status while preserving existing ATIF pointers.
	got, updated, err = cs.UpsertTrajectorySession(ctx, &store.TrajectorySession{
		HubID:              "hub-1",
		ProjectID:          "proj-1",
		AgentID:            "agent-1",
		RunID:              "run-1",
		Harness:            "claude",
		NativeRelativePath: path,
		ParserVersion:      "claude-10",
		ParsedOffset:       50,
		NormalizeStatus:    telemetrycontract.TrajectorySessionNormalizeStatusParseFailed,
		ParseErrorCount:    3,
		FirstError:         "unexpected EOF",
		UpdatedAt:          now.Add(4 * time.Second),
	})
	require.NoError(t, err)
	assert.True(t, updated)
	assert.Equal(t, telemetrycontract.TrajectorySessionNormalizeStatusParseFailed, got.NormalizeStatus)
	assert.Equal(t, 3, got.ParseErrorCount)
	assert.Equal(t, "unexpected EOF", got.FirstError)
	assert.Equal(t, "art-10-50", got.ATIFArtifactID)
	assert.Equal(t, "path/10-50", got.ATIFObjectPath)

	// Concurrent upserts across multiple offsets converge on the highest (parser_version, parsed_offset).
	concPath := ".claude/projects/-workspace/sess-concurrent.jsonl"
	const workers = 12
	var wg sync.WaitGroup
	errs := make(chan error, workers)
	for i := 1; i <= workers; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			_, _, upErr := cs.UpsertTrajectorySession(ctx, &store.TrajectorySession{
				HubID:              "hub-1",
				ProjectID:          "proj-1",
				AgentID:            "agent-1",
				RunID:              "run-1",
				Harness:            "claude",
				SessionID:          "sess-conc",
				NativeRelativePath: concPath,
				ParserVersion:      fmt.Sprintf("claude-%d", (n%3)+1),
				ParsedOffset:       int64(n * 100),
				ATIFArtifactID:     fmt.Sprintf("art-%d", n),
				ATIFObjectPath:     fmt.Sprintf("path/%d", n),
				StepCount:          n,
				NormalizeStatus:    telemetrycontract.TrajectorySessionNormalizeStatusOK,
			})
			if upErr != nil {
				errs <- upErr
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}

	finalRow, err := cs.GetTrajectorySessionByPath(ctx, "run-1", concPath)
	require.NoError(t, err)
	// Highest parser_version is claude-3 (from n in {2, 5, 8, 11}), highest offset among those is n=11 -> 1100.
	assert.Equal(t, "claude-3", finalRow.ParserVersion)
	assert.Equal(t, int64(1100), finalRow.ParsedOffset)
	assert.Equal(t, "art-11", finalRow.ATIFArtifactID)
}

func TestTrajectorySegmentStore_MarkNormalizeStatus(t *testing.T) {
	ctx := context.Background()
	client := enttest.NewClient(t)
	cs := NewCompositeStore(client)

	now := time.Now().UTC().Truncate(time.Microsecond)
	path := ".claude/projects/-workspace/sess-norm.jsonl"
	h := strings.Repeat("a", 64)

	makeCommittedSeg := func(start, end int64) *store.TrajectorySegment {
		seg, err := cs.PrepareTrajectorySegment(ctx, &store.TrajectorySegment{
			HubID:              "hub-1",
			OriginHubID:        "hub-1",
			ProjectID:          "proj-1",
			AgentID:            "agent-1",
			RunID:              "run-1",
			Harness:            "claude",
			NativeRelativePath: path,
			ArtifactKind:       telemetrycontract.TrajectoryArtifactKindJSONLAppend,
			Generation:         0,
			ByteStart:          start,
			ByteEnd:            end,
			RawSHA256:          h,
			CompressedSize:     100,
			CompressedSHA256:   h,
			ObjectPath:         fmt.Sprintf("hub-1/raw/%d.seg.gz", start),
			CreatedAt:          now,
			PrepareExpiresAt:   now.Add(15 * time.Minute),
		})
		require.NoError(t, err)
		committed, err := cs.CommitTrajectorySegment(ctx, seg.ID, seg.ObjectPath+".manifest.json", now, store.TrajectorySegmentCommitExpectation{
			ByteEnd:          end,
			RawSHA256:        h,
			CompressedSize:   100,
			CompressedSHA256: h,
		})
		require.NoError(t, err)
		return committed
	}

	s1 := makeCommittedSeg(0, 100)
	s2 := makeCommittedSeg(100, 250)

	cutoff := int64(100)
	n, err := cs.MarkTrajectorySegmentsNormalizeStatus(ctx, "agent-1", "run-1", path, &cutoff, telemetrycontract.TrajectoryNormalizeStatusNormalized)
	require.NoError(t, err)
	assert.Equal(t, int64(1), n)

	got1, err := cs.GetTrajectorySegment(ctx, s1.ID)
	require.NoError(t, err)
	assert.Equal(t, telemetrycontract.TrajectoryNormalizeStatusNormalized, got1.NormalizeStatus)

	got2, err := cs.GetTrajectorySegment(ctx, s2.ID)
	require.NoError(t, err)
	assert.Equal(t, telemetrycontract.TrajectoryNormalizeStatusPending, got2.NormalizeStatus)

	// Mark remaining pending committed segments parse_failed without clobbering already-normalized s1.
	n, err = cs.MarkTrajectorySegmentsNormalizeStatus(ctx, "agent-1", "run-1", path, nil, telemetrycontract.TrajectoryNormalizeStatusParseFailed)
	require.NoError(t, err)
	assert.Equal(t, int64(1), n)

	got1, err = cs.GetTrajectorySegment(ctx, s1.ID)
	require.NoError(t, err)
	assert.Equal(t, telemetrycontract.TrajectoryNormalizeStatusNormalized, got1.NormalizeStatus)

	got2, err = cs.GetTrajectorySegment(ctx, s2.ID)
	require.NoError(t, err)
	assert.Equal(t, telemetrycontract.TrajectoryNormalizeStatusParseFailed, got2.NormalizeStatus)
}

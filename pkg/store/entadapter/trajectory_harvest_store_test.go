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
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/GoogleCloudPlatform/scion/pkg/store"
	"github.com/GoogleCloudPlatform/scion/pkg/store/enttest"
	"github.com/GoogleCloudPlatform/scion/pkg/telemetrycontract"
)

func TestTrajectoryHarvestStore_CreateReplayAndLatest(t *testing.T) {
	ctx := context.Background()
	client := enttest.NewClient(t)
	cs := NewCompositeStore(client)

	// Missing agent returns ErrNotFound.
	_, err := cs.LatestTrajectoryHarvest(ctx, "agent-none", "")
	require.ErrorIs(t, err, store.ErrNotFound)

	t0 := time.Date(2026, 11, 28, 18, 0, 0, 0, time.UTC)
	oldest := t0.Add(-5 * time.Minute)
	first := &store.TrajectoryHarvest{
		HarvestID:          "harv-1",
		HubID:              "hub-1",
		ProjectID:          "proj-1",
		AgentID:            "agent-1",
		BrokerID:           "broker-1",
		RunID:              "run-1",
		Trigger:            telemetrycontract.TrajectoryHarvestTriggerPostExit,
		Outcome:            telemetrycontract.TrajectoryHarvestOutcomePartial,
		Trajectory:         telemetrycontract.TrajectoryHarvestTrajectoryShipped,
		Usage:              telemetrycontract.TrajectoryHarvestUsageDrained,
		SegmentsCommitted:  2,
		BytesCommitted:     1024,
		PendingBytes:       256,
		OldestPendingAt:    &oldest,
		UsageEventsDrained: 5,
		UsageEventsPending: 1,
		ErrorCode:          "deadline",
		Error:              "timed out draining remaining bytes",
		StartedAt:          t0,
		FinishedAt:         t0.Add(3 * time.Second),
		CreatedAt:          t0.Add(3 * time.Second),
	}

	created, err := cs.CreateTrajectoryHarvest(ctx, first)
	require.NoError(t, err)
	require.NotEmpty(t, created.ID)
	assert.Equal(t, "harv-1", created.HarvestID)
	assert.Equal(t, telemetrycontract.TrajectoryHarvestOutcomePartial, created.Outcome)
	require.NotNil(t, created.OldestPendingAt)
	assert.Equal(t, oldest, *created.OldestPendingAt)

	// Idempotent replay with same (agent_id, harvest_id) does not overwrite
	// and returns the original row.
	replayReq := *first
	replayReq.ID = ""
	replayReq.Outcome = telemetrycontract.TrajectoryHarvestOutcomeFailed
	replayReq.SegmentsCommitted = 99
	replayed, err := cs.CreateTrajectoryHarvest(ctx, &replayReq)
	require.NoError(t, err)
	assert.Equal(t, created.ID, replayed.ID)
	assert.Equal(t, telemetrycontract.TrajectoryHarvestOutcomePartial, replayed.Outcome)
	assert.Equal(t, 2, replayed.SegmentsCommitted)

	// Second harvest for pre_delete trigger at a later timestamp.
	t1 := t0.Add(10 * time.Minute)
	second := &store.TrajectoryHarvest{
		HarvestID:         "harv-2",
		HubID:             "hub-1",
		ProjectID:         "proj-1",
		AgentID:           "agent-1",
		BrokerID:          "broker-1",
		RunID:             "run-1",
		Trigger:           telemetrycontract.TrajectoryHarvestTriggerPreDelete,
		Outcome:           telemetrycontract.TrajectoryHarvestOutcomeComplete,
		Trajectory:        telemetrycontract.TrajectoryHarvestTrajectoryShipped,
		Usage:             telemetrycontract.TrajectoryHarvestUsageNothingPending,
		SegmentsCommitted: 1,
		BytesCommitted:    256,
		StartedAt:         t1,
		FinishedAt:        t1.Add(time.Second),
		CreatedAt:         t1.Add(time.Second),
	}
	created2, err := cs.CreateTrajectoryHarvest(ctx, second)
	require.NoError(t, err)
	assert.Nil(t, created2.OldestPendingAt)

	// Latest across all triggers is harv-2.
	latestAll, err := cs.LatestTrajectoryHarvest(ctx, "agent-1", "")
	require.NoError(t, err)
	assert.Equal(t, "harv-2", latestAll.HarvestID)

	// Latest filtered by trigger.
	latestPostExit, err := cs.LatestTrajectoryHarvest(ctx, "agent-1", telemetrycontract.TrajectoryHarvestTriggerPostExit)
	require.NoError(t, err)
	assert.Equal(t, "harv-1", latestPostExit.HarvestID)

	latestPreDelete, err := cs.LatestTrajectoryHarvest(ctx, "agent-1", telemetrycontract.TrajectoryHarvestTriggerPreDelete)
	require.NoError(t, err)
	assert.Equal(t, "harv-2", latestPreDelete.HarvestID)

	// Trigger with no rows returns ErrNotFound.
	_, err = cs.LatestTrajectoryHarvest(ctx, "agent-1", telemetrycontract.TrajectoryHarvestTriggerPreReset)
	require.ErrorIs(t, err, store.ErrNotFound)

	// Same harvest_id for a different agent is allowed (unique key is (agent_id, harvest_id)).
	otherAgent := *second
	otherAgent.ID = ""
	otherAgent.AgentID = "agent-2"
	createdOther, err := cs.CreateTrajectoryHarvest(ctx, &otherAgent)
	require.NoError(t, err)
	assert.NotEqual(t, created2.ID, createdOther.ID)
	assert.Equal(t, "agent-2", createdOther.AgentID)
}

func TestTrajectorySegmentStore_UploadSourceDefaultAndBrokerHarvest(t *testing.T) {
	ctx := context.Background()
	client := enttest.NewClient(t)
	cs := NewCompositeStore(client)

	now := time.Now().UTC().Truncate(time.Microsecond)
	agentSeg, err := cs.PrepareTrajectorySegment(ctx, &store.TrajectorySegment{
		HubID:              "hub-1",
		ProjectID:          "proj-1",
		AgentID:            "agent-1",
		RunID:              "run-1",
		Harness:            "claude",
		NativeRelativePath: ".claude/projects/p/s1.jsonl",
		ArtifactKind:       telemetrycontract.TrajectoryArtifactKindJSONLAppend,
		Generation:         0,
		ByteStart:          0,
		ByteEnd:            100,
		RawSHA256:          strings.Repeat("a", 64),
		CompressedSize:     60,
		CompressedSHA256:   strings.Repeat("b", 64),
		ObjectPath:         "hub-1/obj-agent.seg.gz",
		CreatedAt:          now,
		PrepareExpiresAt:   now.Add(15 * time.Minute),
	})
	require.NoError(t, err)
	assert.Equal(t, telemetrycontract.TrajectoryUploadSourceAgent, agentSeg.UploadSource)

	brokerSeg, err := cs.PrepareTrajectorySegment(ctx, &store.TrajectorySegment{
		HubID:              "hub-1",
		ProjectID:          "proj-1",
		AgentID:            "agent-1",
		RunID:              "run-1",
		Harness:            "claude",
		NativeRelativePath: ".claude/projects/p/s2.jsonl",
		ArtifactKind:       telemetrycontract.TrajectoryArtifactKindJSONLAppend,
		Generation:         0,
		ByteStart:          0,
		ByteEnd:            100,
		RawSHA256:          strings.Repeat("c", 64),
		CompressedSize:     60,
		CompressedSHA256:   strings.Repeat("d", 64),
		UploadSource:       telemetrycontract.TrajectoryUploadSourceBrokerHarvest,
		ObjectPath:         "hub-1/obj-broker.seg.gz",
		CreatedAt:          now,
		PrepareExpiresAt:   now.Add(15 * time.Minute),
	})
	require.NoError(t, err)
	assert.Equal(t, telemetrycontract.TrajectoryUploadSourceBrokerHarvest, brokerSeg.UploadSource)

	fetched, err := cs.GetTrajectorySegment(ctx, brokerSeg.ID)
	require.NoError(t, err)
	assert.Equal(t, telemetrycontract.TrajectoryUploadSourceBrokerHarvest, fetched.UploadSource)
}

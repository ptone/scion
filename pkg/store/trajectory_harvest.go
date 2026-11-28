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

package store

import (
	"context"
	"time"
)

// TrajectoryHarvest represents one row in the trajectory_harvests table
// recording the outcome of a broker harvest attempt (Tokenomics P6.1 §3).
type TrajectoryHarvest struct {
	ID                 string     `json:"id"`
	HarvestID          string     `json:"harvestId"`
	HubID              string     `json:"hubId"`
	ProjectID          string     `json:"projectId"`
	AgentID            string     `json:"agentId"`
	BrokerID           string     `json:"brokerId"`
	RunID              string     `json:"runId,omitempty"`
	Trigger            string     `json:"trigger"`
	Outcome            string     `json:"outcome"`
	Trajectory         string     `json:"trajectory"`
	Usage              string     `json:"usage"`
	SegmentsCommitted  int        `json:"segmentsCommitted"`
	BytesCommitted     int64      `json:"bytesCommitted"`
	PendingBytes       int64      `json:"pendingBytes"`
	OldestPendingAt    *time.Time `json:"oldestPendingAt,omitempty"`
	UsageEventsDrained int        `json:"usageEventsDrained"`
	UsageEventsPending int        `json:"usageEventsPending"`
	ErrorCode          string     `json:"errorCode,omitempty"`
	Error              string     `json:"error,omitempty"`
	StartedAt          time.Time  `json:"startedAt"`
	FinishedAt         time.Time  `json:"finishedAt"`
	CreatedAt          time.Time  `json:"createdAt"`
}

// TrajectoryHarvestStore defines persistence operations for broker harvest
// outcome records (ptone/scion#9253).
type TrajectoryHarvestStore interface {
	// CreateTrajectoryHarvest atomically records a harvest outcome using
	// ON CONFLICT (agent_id, harvest_id) DO NOTHING. Replays with the same
	// (agent_id, harvest_id) return the existing row with nil error.
	CreateTrajectoryHarvest(ctx context.Context, h *TrajectoryHarvest) (*TrajectoryHarvest, error)

	// LatestTrajectoryHarvest returns the most recent harvest record for
	// agentID matching trigger (or across all triggers if trigger is empty),
	// ordered by created_at DESC, id DESC. Returns ErrNotFound if none exists.
	LatestTrajectoryHarvest(ctx context.Context, agentID, trigger string) (*TrajectoryHarvest, error)
}

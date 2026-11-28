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

package entadapter

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	entsql "entgo.io/ent/dialect/sql"

	"github.com/GoogleCloudPlatform/scion/pkg/api"
	"github.com/GoogleCloudPlatform/scion/pkg/ent"
	"github.com/GoogleCloudPlatform/scion/pkg/ent/trajectoryharvest"
	"github.com/GoogleCloudPlatform/scion/pkg/store"
)

// TrajectoryHarvestStore implements store.TrajectoryHarvestStore using Ent.
type TrajectoryHarvestStore struct {
	client *ent.Client
	inTx   bool
}

// NewTrajectoryHarvestStore creates a new Ent-backed TrajectoryHarvestStore.
func NewTrajectoryHarvestStore(client *ent.Client) *TrajectoryHarvestStore {
	return &TrajectoryHarvestStore{client: client}
}

var _ store.TrajectoryHarvestStore = (*TrajectoryHarvestStore)(nil)
var _ store.TrajectoryHarvestStore = (*CompositeStore)(nil)

func entTrajectoryHarvestToStore(row *ent.TrajectoryHarvest) *store.TrajectoryHarvest {
	if row == nil {
		return nil
	}
	var oldestPendingAt *time.Time
	if row.OldestPendingAt != nil {
		t := row.OldestPendingAt.UTC()
		oldestPendingAt = &t
	}
	return &store.TrajectoryHarvest{
		ID:                 row.ID,
		HarvestID:          row.HarvestID,
		HubID:              row.HubID,
		ProjectID:          row.ProjectID,
		AgentID:            row.AgentID,
		BrokerID:           row.BrokerID,
		RunID:              row.RunID,
		Trigger:            row.Trigger,
		Outcome:            row.Outcome,
		Trajectory:         row.Trajectory,
		Usage:              row.Usage,
		SegmentsCommitted:  row.SegmentsCommitted,
		BytesCommitted:     row.BytesCommitted,
		PendingBytes:       row.PendingBytes,
		OldestPendingAt:    oldestPendingAt,
		UsageEventsDrained: row.UsageEventsDrained,
		UsageEventsPending: row.UsageEventsPending,
		ErrorCode:          row.ErrorCode,
		Error:              row.Error,
		StartedAt:          row.StartedAt.UTC(),
		FinishedAt:         row.FinishedAt.UTC(),
		CreatedAt:          row.CreatedAt.UTC(),
	}
}

// CreateTrajectoryHarvest implements store.TrajectoryHarvestStore.
// Uses INSERT ... ON CONFLICT (agent_id, harvest_id) DO NOTHING for idempotent
// replay across SQLite and Postgres.
func (s *TrajectoryHarvestStore) CreateTrajectoryHarvest(ctx context.Context, h *store.TrajectoryHarvest) (*store.TrajectoryHarvest, error) {
	if h == nil || h.HarvestID == "" || h.AgentID == "" || h.BrokerID == "" || h.ProjectID == "" || h.HubID == "" {
		return nil, fmt.Errorf("%w: harvest_id, agent_id, broker_id, project_id, and hub_id are required", store.ErrInvalidInput)
	}
	if h.Trigger == "" || h.Outcome == "" || h.Trajectory == "" || h.Usage == "" {
		return nil, fmt.Errorf("%w: trigger, outcome, trajectory, and usage are required", store.ErrInvalidInput)
	}

	id := h.ID
	if id == "" {
		id = api.NewUUID()
	}
	now := h.CreatedAt
	if now.IsZero() {
		now = time.Now()
	}
	now = now.UTC().Truncate(time.Microsecond)

	startedAt := h.StartedAt.UTC().Truncate(time.Microsecond)
	finishedAt := h.FinishedAt.UTC().Truncate(time.Microsecond)

	var oldestPendingVal any
	var oldestPendingOut *time.Time
	if h.OldestPendingAt != nil {
		t := h.OldestPendingAt.UTC().Truncate(time.Microsecond)
		oldestPendingVal = t
		oldestPendingOut = &t
	}

	drv := s.client.Driver()
	query, args := entsql.Dialect(drv.Dialect()).
		Insert(trajectoryharvest.Table).
		Columns(
			trajectoryharvest.FieldID,
			trajectoryharvest.FieldHarvestID,
			trajectoryharvest.FieldHubID,
			trajectoryharvest.FieldProjectID,
			trajectoryharvest.FieldAgentID,
			trajectoryharvest.FieldBrokerID,
			trajectoryharvest.FieldRunID,
			trajectoryharvest.FieldTrigger,
			trajectoryharvest.FieldOutcome,
			trajectoryharvest.FieldTrajectory,
			trajectoryharvest.FieldUsage,
			trajectoryharvest.FieldSegmentsCommitted,
			trajectoryharvest.FieldBytesCommitted,
			trajectoryharvest.FieldPendingBytes,
			trajectoryharvest.FieldOldestPendingAt,
			trajectoryharvest.FieldUsageEventsDrained,
			trajectoryharvest.FieldUsageEventsPending,
			trajectoryharvest.FieldErrorCode,
			trajectoryharvest.FieldError,
			trajectoryharvest.FieldStartedAt,
			trajectoryharvest.FieldFinishedAt,
			trajectoryharvest.FieldCreatedAt,
		).
		Values(
			id,
			h.HarvestID,
			h.HubID,
			h.ProjectID,
			h.AgentID,
			h.BrokerID,
			h.RunID,
			h.Trigger,
			h.Outcome,
			h.Trajectory,
			h.Usage,
			h.SegmentsCommitted,
			h.BytesCommitted,
			h.PendingBytes,
			oldestPendingVal,
			h.UsageEventsDrained,
			h.UsageEventsPending,
			h.ErrorCode,
			h.Error,
			startedAt,
			finishedAt,
			now,
		).
		OnConflict(
			entsql.ConflictColumns(
				trajectoryharvest.FieldAgentID,
				trajectoryharvest.FieldHarvestID,
			),
			entsql.DoNothing(),
		).
		Query()

	var res sql.Result
	if err := drv.Exec(ctx, query, args, &res); err != nil {
		return nil, mapError(err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return nil, mapError(err)
	}
	if affected > 0 {
		out := *h
		out.ID = id
		out.OldestPendingAt = oldestPendingOut
		out.StartedAt = startedAt
		out.FinishedAt = finishedAt
		out.CreatedAt = now
		return &out, nil
	}

	// Idempotent replay: return the previously recorded row.
	row, err := s.client.TrajectoryHarvest.Query().
		Where(
			trajectoryharvest.AgentIDEQ(h.AgentID),
			trajectoryharvest.HarvestIDEQ(h.HarvestID),
		).
		Only(ctx)
	if err != nil {
		return nil, mapError(err)
	}
	return entTrajectoryHarvestToStore(row), nil
}

// LatestTrajectoryHarvest implements store.TrajectoryHarvestStore.
func (s *TrajectoryHarvestStore) LatestTrajectoryHarvest(ctx context.Context, agentID, trigger string) (*store.TrajectoryHarvest, error) {
	if agentID == "" {
		return nil, store.ErrNotFound
	}
	q := s.client.TrajectoryHarvest.Query().
		Where(trajectoryharvest.AgentIDEQ(agentID))
	if trigger != "" {
		q = q.Where(trajectoryharvest.TriggerEQ(trigger))
	}
	row, err := q.
		Order(
			ent.Desc(trajectoryharvest.FieldCreatedAt),
			ent.Desc(trajectoryharvest.FieldID),
		).
		First(ctx)
	if err != nil {
		return nil, mapError(err)
	}
	return entTrajectoryHarvestToStore(row), nil
}

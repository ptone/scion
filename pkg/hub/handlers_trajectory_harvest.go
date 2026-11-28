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
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/GoogleCloudPlatform/scion/pkg/store"
	"github.com/GoogleCloudPlatform/scion/pkg/telemetrycontract"
)

// handleBrokerTrajectoryHarvests serves
// POST /api/v1/brokers/{bid}/agents/{id}/trajectory/harvests.
// Records the outcome of a broker post-exit / pre-delete / pre-reset harvest
// attempt idempotently by (agent_id, harvest_id).
func (s *Server) handleBrokerTrajectoryHarvests(w http.ResponseWriter, r *http.Request, brokerID, agentID string) {
	agent, ok := s.authenticateBrokerTrajectoryAgent(w, r, http.MethodPost, brokerID, agentID)
	if !ok {
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, trajectorySegmentRequestMaxBody+1))
	if err != nil {
		BadRequest(w, "Failed to read request body")
		return
	}
	if len(body) > trajectorySegmentRequestMaxBody {
		BadRequest(w, "Request body exceeds maximum size")
		return
	}

	var req telemetrycontract.TrajectoryHarvestReportRequest
	if err := json.Unmarshal(body, &req); err != nil {
		BadRequest(w, "Invalid JSON request body")
		return
	}

	startedAt, finishedAt, oldestPendingAt, err := validateTrajectoryHarvestReport(&req)
	if err != nil {
		writeError(w, http.StatusBadRequest, ErrCodeValidationError, err.Error(), nil)
		return
	}

	now := time.Now().UTC().Truncate(time.Microsecond)
	_, err = s.store.CreateTrajectoryHarvest(r.Context(), &store.TrajectoryHarvest{
		HarvestID:          strings.TrimSpace(req.HarvestID),
		HubID:              s.usageLedgerHubID(),
		ProjectID:          agent.ProjectID,
		AgentID:            agent.ID,
		BrokerID:           brokerID,
		RunID:              strings.TrimSpace(req.RunID),
		Trigger:            req.Trigger,
		Outcome:            req.Outcome,
		Trajectory:         req.Trajectory,
		Usage:              req.Usage,
		SegmentsCommitted:  req.SegmentsCommitted,
		BytesCommitted:     req.BytesCommitted,
		PendingBytes:       req.PendingBytes,
		OldestPendingAt:    oldestPendingAt,
		UsageEventsDrained: req.UsageEventsDrained,
		UsageEventsPending: req.UsageEventsPending,
		ErrorCode:          strings.TrimSpace(req.ErrorCode),
		Error:              req.Error,
		StartedAt:          startedAt,
		FinishedAt:         finishedAt,
		CreatedAt:          now,
	})
	if err != nil {
		writeErrorFromErr(w, err, "")
		return
	}

	writeJSON(w, http.StatusOK, telemetrycontract.TrajectoryHarvestReportResponse{
		Recorded: true,
	})
}

func validateTrajectoryHarvestReport(req *telemetrycontract.TrajectoryHarvestReportRequest) (startedAt, finishedAt time.Time, oldestPendingAt *time.Time, err error) {
	harvestID := strings.TrimSpace(req.HarvestID)
	if harvestID == "" {
		return time.Time{}, time.Time{}, nil, fmt.Errorf("harvest_id is required")
	}
	if len(harvestID) > telemetrycontract.TrajectoryHarvestMaxIDBytes || !isPrintableTrajectoryField(harvestID) {
		return time.Time{}, time.Time{}, nil, fmt.Errorf("invalid harvest_id")
	}
	runID := strings.TrimSpace(req.RunID)
	if len(runID) > telemetrycontract.TrajectoryHarvestMaxIDBytes || !isPrintableTrajectoryField(runID) {
		return time.Time{}, time.Time{}, nil, fmt.Errorf("invalid run_id")
	}
	if !telemetrycontract.IsValidTrajectoryHarvestTrigger(req.Trigger) {
		return time.Time{}, time.Time{}, nil, fmt.Errorf("invalid trigger %q", req.Trigger)
	}
	if !telemetrycontract.IsValidTrajectoryHarvestOutcome(req.Outcome) {
		return time.Time{}, time.Time{}, nil, fmt.Errorf("invalid outcome %q", req.Outcome)
	}
	if !telemetrycontract.IsValidTrajectoryHarvestTrajectory(req.Trajectory) {
		return time.Time{}, time.Time{}, nil, fmt.Errorf("invalid trajectory status %q", req.Trajectory)
	}
	if !telemetrycontract.IsValidTrajectoryHarvestUsage(req.Usage) {
		return time.Time{}, time.Time{}, nil, fmt.Errorf("invalid usage status %q", req.Usage)
	}
	if req.SegmentsCommitted < 0 || req.BytesCommitted < 0 || req.PendingBytes < 0 ||
		req.UsageEventsDrained < 0 || req.UsageEventsPending < 0 {
		return time.Time{}, time.Time{}, nil, fmt.Errorf("harvest counters must be non-negative")
	}
	if len(req.ErrorCode) > telemetrycontract.TrajectoryHarvestMaxErrorCodeBytes || !isPrintableTrajectoryField(req.ErrorCode) {
		return time.Time{}, time.Time{}, nil, fmt.Errorf("invalid error_code")
	}
	if len(req.Error) > telemetrycontract.TrajectoryHarvestMaxErrorBytes || !utf8.ValidString(req.Error) {
		return time.Time{}, time.Time{}, nil, fmt.Errorf("error exceeds maximum length of %d bytes or is invalid UTF-8", telemetrycontract.TrajectoryHarvestMaxErrorBytes)
	}

	startedAt, err = parseHarvestTimestamp(req.StartedAt)
	if err != nil {
		return time.Time{}, time.Time{}, nil, fmt.Errorf("invalid started_at: %w", err)
	}
	finishedAt, err = parseHarvestTimestamp(req.FinishedAt)
	if err != nil {
		return time.Time{}, time.Time{}, nil, fmt.Errorf("invalid finished_at: %w", err)
	}
	if req.OldestPendingAt != nil && strings.TrimSpace(*req.OldestPendingAt) != "" {
		ts, parseErr := parseHarvestTimestamp(*req.OldestPendingAt)
		if parseErr != nil {
			return time.Time{}, time.Time{}, nil, fmt.Errorf("invalid oldest_pending_at: %w", parseErr)
		}
		oldestPendingAt = &ts
	}
	return startedAt, finishedAt, oldestPendingAt, nil
}

func parseHarvestTimestamp(raw string) (time.Time, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return time.Time{}, fmt.Errorf("timestamp is required")
	}
	if ts, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return ts.UTC().Truncate(time.Microsecond), nil
	}
	ts, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}, err
	}
	return ts.UTC().Truncate(time.Microsecond), nil
}

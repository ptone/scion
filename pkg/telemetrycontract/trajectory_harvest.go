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

package telemetrycontract

import (
	"fmt"
	"net/url"
)

// TrajectoryHarvestMaxErrorBytes bounds the free-form error string in
// TrajectoryHarvestReportRequest (must carry no absolute host paths or file content).
const (
	TrajectoryHarvestMaxErrorBytes     = 1024
	TrajectoryHarvestMaxErrorCodeBytes = 128
	TrajectoryHarvestMaxIDBytes        = 128
)

// Broker harvest trigger values (P6.1 contract §3).
const (
	TrajectoryHarvestTriggerPostExit  = "post_exit"
	TrajectoryHarvestTriggerPreDelete = "pre_delete"
	TrajectoryHarvestTriggerPreReset  = "pre_reset"
)

// Broker harvest outcome values (P6.1 contract §3).
const (
	TrajectoryHarvestOutcomeComplete       = "complete"
	TrajectoryHarvestOutcomeNothingPending = "nothing_pending"
	TrajectoryHarvestOutcomePartial        = "partial"
	TrajectoryHarvestOutcomeFailed         = "failed"
	TrajectoryHarvestOutcomeUnavailable    = "unavailable"
)

// Broker harvest trajectory sub-status values (P6.1 contract §3).
const (
	TrajectoryHarvestTrajectoryShipped        = "shipped"
	TrajectoryHarvestTrajectoryNothingPending = "nothing_pending"
	TrajectoryHarvestTrajectoryNoState        = "no_state"
	TrajectoryHarvestTrajectoryCaptureRefused = "capture_refused"
	TrajectoryHarvestTrajectoryError          = "error"
	TrajectoryHarvestTrajectorySkipped        = "skipped"
)

// Broker harvest usage outbox drain sub-status values (P6.1 contract §3).
const (
	TrajectoryHarvestUsageDrained        = "drained"
	TrajectoryHarvestUsageNothingPending = "nothing_pending"
	TrajectoryHarvestUsageNoOutbox       = "no_outbox"
	TrajectoryHarvestUsageError          = "error"
	TrajectoryHarvestUsageSkipped        = "skipped"
)

// IsValidTrajectoryHarvestTrigger reports whether s is a valid P6.1 harvest trigger.
func IsValidTrajectoryHarvestTrigger(s string) bool {
	switch s {
	case TrajectoryHarvestTriggerPostExit,
		TrajectoryHarvestTriggerPreDelete,
		TrajectoryHarvestTriggerPreReset:
		return true
	default:
		return false
	}
}

// IsValidTrajectoryHarvestOutcome reports whether s is a valid P6.1 harvest outcome.
func IsValidTrajectoryHarvestOutcome(s string) bool {
	switch s {
	case TrajectoryHarvestOutcomeComplete,
		TrajectoryHarvestOutcomeNothingPending,
		TrajectoryHarvestOutcomePartial,
		TrajectoryHarvestOutcomeFailed,
		TrajectoryHarvestOutcomeUnavailable:
		return true
	default:
		return false
	}
}

// IsValidTrajectoryHarvestTrajectory reports whether s is a valid P6.1 trajectory sub-status.
func IsValidTrajectoryHarvestTrajectory(s string) bool {
	switch s {
	case TrajectoryHarvestTrajectoryShipped,
		TrajectoryHarvestTrajectoryNothingPending,
		TrajectoryHarvestTrajectoryNoState,
		TrajectoryHarvestTrajectoryCaptureRefused,
		TrajectoryHarvestTrajectoryError,
		TrajectoryHarvestTrajectorySkipped:
		return true
	default:
		return false
	}
}

// IsValidTrajectoryHarvestUsage reports whether s is a valid P6.1 usage sub-status.
func IsValidTrajectoryHarvestUsage(s string) bool {
	switch s {
	case TrajectoryHarvestUsageDrained,
		TrajectoryHarvestUsageNothingPending,
		TrajectoryHarvestUsageNoOutbox,
		TrajectoryHarvestUsageError,
		TrajectoryHarvestUsageSkipped:
		return true
	default:
		return false
	}
}

// TrajectoryHarvestsBrokerPath returns the broker-authenticated harvest outcome
// report path: POST /api/v1/brokers/{bid}/agents/{id}/trajectory/harvests.
func TrajectoryHarvestsBrokerPath(brokerID, agentID string) string {
	return fmt.Sprintf("/api/v1/brokers/%s/agents/%s/trajectory/harvests",
		url.PathEscape(brokerID), url.PathEscape(agentID))
}

// TrajectoryHarvestReportRequest is the wire payload for
// POST /api/v1/brokers/{bid}/agents/{id}/trajectory/harvests.
type TrajectoryHarvestReportRequest struct {
	HarvestID          string  `json:"harvest_id"`
	RunID              string  `json:"run_id,omitempty"`
	Trigger            string  `json:"trigger"`
	Outcome            string  `json:"outcome"`
	Trajectory         string  `json:"trajectory"`
	Usage              string  `json:"usage"`
	SegmentsCommitted  int     `json:"segments_committed"`
	BytesCommitted     int64   `json:"bytes_committed"`
	PendingBytes       int64   `json:"pending_bytes"`
	OldestPendingAt    *string `json:"oldest_pending_at"`
	UsageEventsDrained int     `json:"usage_events_drained"`
	UsageEventsPending int     `json:"usage_events_pending"`
	ErrorCode          string  `json:"error_code,omitempty"`
	Error              string  `json:"error,omitempty"`
	StartedAt          string  `json:"started_at"`
	FinishedAt         string  `json:"finished_at"`
}

// TrajectoryHarvestReportResponse is returned by
// POST /api/v1/brokers/{bid}/agents/{id}/trajectory/harvests.
type TrajectoryHarvestReportResponse struct {
	Recorded bool `json:"recorded"`
}

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

// Package harvest implements Runtime Broker post-exit, pre-delete, and
// pre-reset harvesting of raw L0 trajectory segments and residual Hub usage
// outbox events (Tokenomics P6.1).
package harvest

import (
	"context"
	"fmt"

	"github.com/GoogleCloudPlatform/scion/pkg/telemetrycontract"
)

// Trigger identifies the broker lifecycle event that initiated a harvest.
type Trigger string

const (
	TriggerPostExit  Trigger = "post_exit"
	TriggerPreDelete Trigger = "pre_delete"
	TriggerPreReset  Trigger = "pre_reset"
)

// Outcome classifies the overall result of a harvest attempt.
const (
	OutcomeComplete       = "complete"
	OutcomeNothingPending = "nothing_pending"
	OutcomePartial        = "partial"
	OutcomeFailed         = "failed"
	OutcomeUnavailable    = "unavailable"
)

// TrajectoryOutcome classifies the trajectory-shipping half of a harvest.
const (
	TrajectoryShipped        = "shipped"
	TrajectoryNothingPending = "nothing_pending"
	TrajectoryNoState        = "no_state"
	TrajectoryCaptureRefused = "capture_refused"
	TrajectoryError          = "error"
	TrajectorySkipped        = "skipped"
)

// UsageOutcome classifies the usage-outbox-drain half of a harvest.
const (
	UsageDrained        = "drained"
	UsageNothingPending = "nothing_pending"
	UsageNoOutbox       = "no_outbox"
	UsageError          = "error"
	UsageSkipped        = "skipped"
)

// Machine-readable error codes reported in Report.ErrorCode.
const (
	ErrCodeLockTimeout    = "lock_timeout"
	ErrCodeUIDMismatch    = "uid_mismatch"
	ErrCodeHomeUnreadable = "home_unreadable"
	ErrCodeHubUnavailable = "hub_unavailable"
	ErrCodeHubMismatch    = "hub_mismatch"
	ErrCodeDeadline       = "deadline"
	ErrCodeMissingRunID   = "missing_run_id"
)

// Target identifies the agent home and metadata to harvest.
type Target struct {
	AgentID      string
	Slug         string
	ProjectID    string
	RunID        string
	Harness      string
	HomeDir      string
	RuntimeName  string
	HomeReadable bool
}

// Report is the wire payload posted to
// POST /api/v1/brokers/{bid}/agents/{id}/trajectory/harvests and returned to
// broker lifecycle callers (such as the P6.2 pre-delete custody-hold hook).
type Report struct {
	HarvestID          string  `json:"harvest_id"`
	RunID              string  `json:"run_id"`
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
	ErrorCode          string  `json:"error_code"`
	Error              string  `json:"error"`
	StartedAt          string  `json:"started_at"`
	FinishedAt         string  `json:"finished_at"`
}

// PrepareSegmentRequest is the broker trajectory segments:prepare payload.
// It mirrors telemetrycontract.TrajectorySegmentPrepareRequest with the
// broker-required run_id field.
type PrepareSegmentRequest struct {
	telemetrycontract.TrajectorySegmentPrepareRequest
	RunID string `json:"run_id"`
}

// HTTPError represents a non-2xx HTTP response from the Hub or upload target.
type HTTPError struct {
	Operation  string
	StatusCode int
	Code       string
	Message    string
}

func (e *HTTPError) Error() string {
	if e.Code != "" && e.Message != "" {
		return fmt.Sprintf("harvest %s returned HTTP %d (%s): %s", e.Operation, e.StatusCode, e.Code, e.Message)
	}
	if e.Code != "" {
		return fmt.Sprintf("harvest %s returned HTTP %d (%s)", e.Operation, e.StatusCode, e.Code)
	}
	return fmt.Sprintf("harvest %s returned HTTP %d", e.Operation, e.StatusCode)
}

// HubClient is the minimal broker-to-Hub interface required by Engine.
type HubClient interface {
	PrepareTrajectorySegment(ctx context.Context, agentID string, req PrepareSegmentRequest) (*telemetrycontract.TrajectorySegmentPrepareResponse, error)
	UploadTrajectorySegment(ctx context.Context, inst telemetrycontract.TrajectorySegmentUploadInstruction, compressed []byte) error
	CommitTrajectorySegment(ctx context.Context, agentID, segmentID string) (*telemetrycontract.TrajectorySegmentCommitResponse, error)
	IngestUsageEvents(ctx context.Context, agentID string, req telemetrycontract.UsageIngestRequest) (*telemetrycontract.UsageIngestResponse, error)
	ReportTrajectoryHarvest(ctx context.Context, agentID string, report Report) error
}

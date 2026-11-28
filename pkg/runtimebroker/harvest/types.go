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
	TriggerPostExit  Trigger = telemetrycontract.TrajectoryHarvestTriggerPostExit
	TriggerPreDelete Trigger = telemetrycontract.TrajectoryHarvestTriggerPreDelete
	TriggerPreReset  Trigger = telemetrycontract.TrajectoryHarvestTriggerPreReset
)

// Outcome classifies the overall result of a harvest attempt.
const (
	OutcomeComplete       = telemetrycontract.TrajectoryHarvestOutcomeComplete
	OutcomeNothingPending = telemetrycontract.TrajectoryHarvestOutcomeNothingPending
	OutcomePartial        = telemetrycontract.TrajectoryHarvestOutcomePartial
	OutcomeFailed         = telemetrycontract.TrajectoryHarvestOutcomeFailed
	OutcomeUnavailable    = telemetrycontract.TrajectoryHarvestOutcomeUnavailable
)

// TrajectoryOutcome classifies the trajectory-shipping half of a harvest.
const (
	TrajectoryShipped        = telemetrycontract.TrajectoryHarvestTrajectoryShipped
	TrajectoryNothingPending = telemetrycontract.TrajectoryHarvestTrajectoryNothingPending
	TrajectoryNoState        = telemetrycontract.TrajectoryHarvestTrajectoryNoState
	TrajectoryCaptureRefused = telemetrycontract.TrajectoryHarvestTrajectoryCaptureRefused
	TrajectoryError          = telemetrycontract.TrajectoryHarvestTrajectoryError
	TrajectorySkipped        = telemetrycontract.TrajectoryHarvestTrajectorySkipped
)

// UsageOutcome classifies the usage-outbox-drain half of a harvest.
const (
	UsageDrained        = telemetrycontract.TrajectoryHarvestUsageDrained
	UsageNothingPending = telemetrycontract.TrajectoryHarvestUsageNothingPending
	UsageNoOutbox       = telemetrycontract.TrajectoryHarvestUsageNoOutbox
	UsageError          = telemetrycontract.TrajectoryHarvestUsageError
	UsageSkipped        = telemetrycontract.TrajectoryHarvestUsageSkipped
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
type Report = telemetrycontract.TrajectoryHarvestReportRequest

// PrepareSegmentRequest is the broker trajectory segments:prepare payload.
type PrepareSegmentRequest = telemetrycontract.TrajectorySegmentPrepareRequest

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

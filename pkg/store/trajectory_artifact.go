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
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/GoogleCloudPlatform/scion/pkg/telemetrycontract"
)

// ErrTrajectoryArtifactConflict is returned when an L1 artifact identity is
// already committed with a different sha256.
var ErrTrajectoryArtifactConflict = errors.New("trajectory artifact conflict")

// ErrTrajectoryArtifactModified is returned by CommitTrajectoryArtifact when a
// pending artifact was concurrently modified by a newer prepare between
// verification and commit.
var ErrTrajectoryArtifactModified = errors.New("trajectory artifact modified before commit")

// TrajectoryArtifactIdentity is the unique natural key of a Track 2 L1
// trajectory artifact.
type TrajectoryArtifactIdentity struct {
	RunID         string
	ArtifactType  string
	ParserVersion string
	IdentityKey   string
}

// TrajectoryArtifactIdentityKey computes the canonical identity_key column for
// a Track 2 L1 artifact per P7 contract §6:
//   - usage_records: chunk_id
//   - run_inventory: decimal inventory_seq
//   - atif: "<native_relative_path>#<parsed_offset>"
func TrajectoryArtifactIdentityKey(artifactType, chunkID string, inventorySeq int64, nativeRelativePath string, parsedOffset int64) string {
	switch artifactType {
	case telemetrycontract.TrajectoryArtifactTypeUsageRecords:
		return chunkID
	case telemetrycontract.TrajectoryArtifactTypeRunInventory:
		return strconv.FormatInt(inventorySeq, 10)
	case telemetrycontract.TrajectoryArtifactTypeATIF:
		return fmt.Sprintf("%s#%d", nativeRelativePath, parsedOffset)
	default:
		return ""
	}
}

// TrajectoryArtifactCommitExpectation binds CommitTrajectoryArtifact to the
// exact verified pending snapshot so a concurrent pending refresh cannot commit
// unverified bytes.
type TrajectoryArtifactCommitExpectation struct {
	Size     int64
	SHA256   string
	RowCount int64
	Final    bool
}

// TrajectoryArtifact represents a row in the trajectory_artifacts table.
type TrajectoryArtifact struct {
	ID                 string     `json:"id"`
	HubID              string     `json:"hubId"`
	ProjectID          string     `json:"projectId"`
	AgentID            string     `json:"agentId"`
	RunID              string     `json:"runId"`
	Harness            string     `json:"harness"`
	ArtifactType       string     `json:"artifactType"`
	ParserVersion      string     `json:"parserVersion"`
	IdentityKey        string     `json:"identityKey"`
	ChunkID            string     `json:"chunkId,omitempty"`
	InventorySeq       int64      `json:"inventorySeq,omitempty"`
	Final              bool       `json:"final,omitempty"`
	SessionID          string     `json:"sessionId,omitempty"`
	NativeRelativePath string     `json:"nativeRelativePath,omitempty"`
	ParsedOffset       int64      `json:"parsedOffset,omitempty"`
	RowCount           int64      `json:"rowCount,omitempty"`
	ATIFSchemaVersion  string     `json:"atifSchemaVersion,omitempty"`
	FileFormat         string     `json:"fileFormat,omitempty"`
	ContentEncoding    string     `json:"contentEncoding"`
	Size               int64      `json:"size"`
	ChargedBytes       int64      `json:"chargedBytes"`
	SHA256             string     `json:"sha256"`
	StampedSize        int64      `json:"stampedSize,omitempty"`
	StampedSHA256      string     `json:"stampedSha256,omitempty"`
	PendingObjectPath  string     `json:"pendingObjectPath"`
	ObjectPath         string     `json:"objectPath"`
	Status             string     `json:"status"`
	CreatedAt          time.Time  `json:"createdAt"`
	CommittedAt        *time.Time `json:"committedAt,omitempty"`
	PrepareExpiresAt   time.Time  `json:"prepareExpiresAt"`
}

// TrajectorySession represents a row in the trajectory_sessions table.
type TrajectorySession struct {
	ID                 string    `json:"id"`
	HubID              string    `json:"hubId"`
	ProjectID          string    `json:"projectId"`
	AgentID            string    `json:"agentId"`
	RunID              string    `json:"runId"`
	Harness            string    `json:"harness"`
	SessionID          string    `json:"sessionId,omitempty"`
	NativeRelativePath string    `json:"nativeRelativePath"`
	IsSubagent         bool      `json:"isSubagent,omitempty"`
	ParentSessionID    string    `json:"parentSessionId,omitempty"`
	ParserVersion      string    `json:"parserVersion"`
	ParserVersionNum   int64     `json:"parserVersionNum"`
	ParsedOffset       int64     `json:"parsedOffset"`
	ATIFArtifactID     string    `json:"atifArtifactId,omitempty"`
	ATIFObjectPath     string    `json:"atifObjectPath,omitempty"`
	ATIFSchemaVersion  string    `json:"atifSchemaVersion,omitempty"`
	FileFormat         string    `json:"fileFormat,omitempty"`
	StepCount          int       `json:"stepCount,omitempty"`
	NormalizeStatus    string    `json:"normalizeStatus"`
	ParseErrorCount    int       `json:"parseErrorCount,omitempty"`
	FirstError         string    `json:"firstError,omitempty"`
	UpdatedAt          time.Time `json:"updatedAt"`
}

// TrajectoryArtifactStore defines persistence operations for Track 2 L1
// trajectory artifacts and normalized trajectory sessions (ptone/scion#9257).
type TrajectoryArtifactStore interface {
	// PrepareTrajectoryArtifact atomically inserts a pending artifact row using
	// ON CONFLICT DO NOTHING on (run_id, artifact_type, parser_version,
	// identity_key), or handles an existing row for that identity:
	//   - If already committed with matching sha256, returns the committed row.
	//   - If already committed with a different sha256, returns
	//     ErrTrajectoryArtifactConflict.
	//   - If still pending, updates the pending metadata and prepare_expires_at
	//     (keeping charged_bytes monotonic) and returns the refreshed row.
	PrepareTrajectoryArtifact(ctx context.Context, art *TrajectoryArtifact) (*TrajectoryArtifact, error)

	// GetTrajectoryArtifact returns the trajectory_artifacts row by opaque ID,
	// or ErrNotFound if no row exists.
	GetTrajectoryArtifact(ctx context.Context, artifactID string) (*TrajectoryArtifact, error)

	// GetTrajectoryArtifactByIdentity returns the trajectory_artifacts row for
	// the given natural identity, or ErrNotFound if no row exists.
	GetTrajectoryArtifactByIdentity(ctx context.Context, id TrajectoryArtifactIdentity) (*TrajectoryArtifact, error)

	// CommitTrajectoryArtifact marks a pending artifact as committed with the
	// stamped object's size, sha256, and commit timestamp, conditionally
	// matching the verified pending snapshot in expect.
	CommitTrajectoryArtifact(ctx context.Context, artifactID string, stampedSize int64, stampedSHA256 string, committedAt time.Time, expect TrajectoryArtifactCommitExpectation) (*TrajectoryArtifact, error)

	// SumTrajectoryRunArtifactBytes returns the total charged bytes —
	// MAX(charged_bytes, size) per row — across all committed and pending
	// trajectory_artifacts rows for (agentID, runID), excluding excludeID if
	// non-empty.
	SumTrajectoryRunArtifactBytes(ctx context.Context, agentID, runID, excludeID string) (int64, error)

	// CountTrajectoryRunArtifacts returns the number of trajectory_artifacts
	// rows (committed and pending) for (agentID, runID), excluding excludeID
	// if non-empty.
	CountTrajectoryRunArtifacts(ctx context.Context, agentID, runID, excludeID string) (int64, error)

	// UpsertTrajectorySession atomically inserts or conditionally updates the
	// trajectory_sessions row for (run_id, native_relative_path) only when
	// (parser_version_num, parsed_offset) >= stored (parser_version_num,
	// parsed_offset). Returns the resulting stored row and whether the write
	// was applied.
	UpsertTrajectorySession(ctx context.Context, sess *TrajectorySession) (*TrajectorySession, bool, error)

	// GetTrajectorySessionByPath returns the trajectory_sessions row for
	// (runID, nativeRelativePath), or ErrNotFound if no row exists.
	GetTrajectorySessionByPath(ctx context.Context, runID, nativeRelativePath string) (*TrajectorySession, error)
}

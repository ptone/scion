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

// Package normalize implements the Track 2 L1 trajectory normalization round
// engine (P7.1 contract §2–§5). It orchestrates input selection, read-only
// round copies, subprocess protocol framing via an Executor interface, Go
// trust-boundary validation, per-round dedupe merging, deterministic L1
// artifact building (usage_records chunks, ATIF-v1.8 trajectories, cumulative
// run_inventory), exactly-once commit ordering via an Uploader interface,
// shipper state advancement via shipper.WithLockedState, and the poison
// breaker.
package normalize

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/GoogleCloudPlatform/scion/pkg/telemetrycontract"
)

// L1 artifact types for Uploader.Prepare (contract §6).
const (
	ArtifactTypeUsageRecords = "usage_records"
	ArtifactTypeATIF         = "atif"
	ArtifactTypeRunInventory = "run_inventory"
)

// L1 prepare/commit states returned by Uploader (contract §6).
const (
	ArtifactStateUploadRequired = "upload_required"
	ArtifactStateCommitted      = "committed"
)

// Content encodings for L1 artifacts (contract §6).
const (
	ContentEncodingGzip     = "gzip"
	ContentEncodingIdentity = "identity"
)

// Default operational constants (contract §4).
const (
	DefaultStateLockTimeout   = 5 * time.Second
	DefaultPoisonThreshold    = 3
	DefaultMaxATIFBytes       = telemetrycontract.MaxATIFAccumulationBytes
	DefaultSoftMaxRSSBytes    = 256 << 20 // 256 MiB soft RSS budget (contract D4)
	DefaultUsageChunkMaxBytes = 16 << 20  // 16 MiB stored cap (contract §6)
	DefaultInventoryMaxBytes  = 16 << 20
)

// Sentinel errors returned by Uploader implementations or the Engine.
var (
	// ErrArtifactConflict indicates the Hub returned 409 artifact_conflict
	// (same L1 identity with a different sha256).
	ErrArtifactConflict = errors.New("normalize: artifact conflict")

	// ErrParseFailure indicates the normalizer process failed (non-zero exit,
	// timeout, exec error, or invalid/missing framing lines).
	ErrParseFailure = errors.New("normalize: parse failure")
)

// RequestRun identifies the run passed to the normalizer process on stdin.
type RequestRun struct {
	RunID   string `json:"run_id"`
	AgentID string `json:"agent_id"`
}

// RequestArtifact describes one read-only round copy passed to the normalizer
// process on stdin (contract §2).
type RequestArtifact struct {
	Key            string `json:"key"`
	Path           string `json:"path"`
	Kind           string `json:"kind"`
	Generation     int    `json:"generation"`
	ParsedOffset   int64  `json:"parsed_offset"`
	EndOffset      int64  `json:"end_offset"`
	FileOffsetBase int64  `json:"file_offset_base"`
}

// Request is the single JSON object written to the normalizer process stdin
// (contract §2, interface_version 1).
type Request struct {
	InterfaceVersion int               `json:"interface_version"`
	Harness          string            `json:"harness"`
	HarnessVersion   string            `json:"harness_version"`
	ParserVersion    string            `json:"parser_version"`
	Mode             string            `json:"mode"`
	Run              RequestRun        `json:"run"`
	Artifacts        []RequestArtifact `json:"artifacts"`
	State            json.RawMessage   `json:"state"`
}

// ExecResult is the outcome of a single normalizer process invocation.
type ExecResult struct {
	StdoutLines  [][]byte
	Stderr       []byte
	ExitCode     int
	PeakRSSBytes int64
}

// Executor runs a normalizer process for one Request and returns its captured
// stdout lines, stderr, exit code, and peak RSS.
type Executor interface {
	Execute(ctx context.Context, req Request) (ExecResult, error)
}

// ArtifactPrepareRequest describes an L1 object to prepare on the Hub
// (contract §6).
type ArtifactPrepareRequest struct {
	ArtifactType       string `json:"artifact_type"`
	ParserVersion      string `json:"parser_version"`
	Harness            string `json:"harness"`
	ChunkID            string `json:"chunk_id,omitempty"`
	RowCount           int    `json:"row_count,omitempty"`
	InventorySeq       int    `json:"inventory_seq,omitempty"`
	Final              bool   `json:"final,omitempty"`
	SessionID          string `json:"session_id,omitempty"`
	NativeRelativePath string `json:"native_relative_path,omitempty"`
	ParsedOffset       int64  `json:"parsed_offset,omitempty"`
	ATIFSchemaVersion  string `json:"atif_schema_version,omitempty"`
	FileFormat         string `json:"file_format,omitempty"`
	ContentEncoding    string `json:"content_encoding"`
	Size               int64  `json:"size"`
	SHA256             string `json:"sha256"`
}

// UploadTarget describes where Uploader.Upload should PUT artifact bytes.
type UploadTarget struct {
	Method    string            `json:"method"`
	URL       string            `json:"url"`
	Headers   map[string]string `json:"headers,omitempty"`
	ExpiresAt string            `json:"expires_at,omitempty"`
}

// ArtifactPrepareResponse is returned by Uploader.Prepare.
type ArtifactPrepareResponse struct {
	ArtifactID string        `json:"artifact_id"`
	State      string        `json:"state"`
	Upload     *UploadTarget `json:"upload,omitempty"`
}

// ArtifactCommitRequest commits an uploaded L1 artifact on the Hub.
type ArtifactCommitRequest struct {
	ArtifactID  string                         `json:"artifact_id"`
	SessionMeta *telemetrycontract.SessionMeta `json:"session_meta,omitempty"`
}

// ArtifactCommitResponse is returned by Uploader.Commit.
type ArtifactCommitResponse struct {
	ArtifactID string `json:"artifact_id"`
	State      string `json:"state"`
}

// ParseFailedReport notifies the Hub that an artifact tripped the poison
// breaker (contract §4, §6).
type ParseFailedReport struct {
	Harness       string                        `json:"harness"`
	ParserVersion string                        `json:"parser_version"`
	SessionMeta   telemetrycontract.SessionMeta `json:"session_meta"`
}

// Uploader abstracts the Hub L1 prepare/upload/commit/parse_failed protocol.
type Uploader interface {
	Prepare(ctx context.Context, req ArtifactPrepareRequest) (ArtifactPrepareResponse, error)
	Upload(ctx context.Context, target UploadTarget, content []byte) error
	Commit(ctx context.Context, req ArtifactCommitRequest) (ArtifactCommitResponse, error)
	ReportParseFailed(ctx context.Context, report ParseFailedReport) error
}

// Config configures a normalization Engine.
type Config struct {
	HomeDir               string
	TempBaseDir           string
	Harness               string
	HarnessVersion        string
	ParserVersion         string
	TestedHarnessVersions []string
	RunID                 string
	AgentID               string
	StateLockTimeout      time.Duration
	PoisonThreshold       int
	MaxATIFBytes          int64
	SoftMaxRSSBytes       int64
	Executor              Executor
	Uploader              Uploader
	Now                   func() time.Time
}

// RoundRequest describes a single normalization round invocation.
type RoundRequest struct {
	// Mode is "full", "metadata_only", or "off" (telemetrycontract.TrajectoryCapturePolicy*).
	Mode string
	// Reason is the shipper round reason (periodic, session_end, model_end,
	// pre_stop, resume_catchup).
	Reason string
}

// RoundResult summarizes what a normalization round did.
type RoundResult struct {
	Skipped                 bool
	SkipReason              string
	ArtifactsProcessed      int
	UsageRecordsEmitted     int
	UsageRecordsMerged      int
	StrippedDimensionFields int
	ParseErrorsDropped      int
	ATIFSessionsUploaded    int
	ChunkID                 string
	InventorySeq            int
	InventoryFinal          bool
	PeakRSSBytes            int64
	RSSBudgetExceeded       bool
	PoisonedArtifacts       []string
}

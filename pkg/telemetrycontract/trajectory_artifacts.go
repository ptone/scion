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
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Track 2 L1 normalized trajectory artifact limits and defaults (P7 contract §6).
const (
	// DefaultTrajectoryArtifactMaxBytes is the maximum stored byte length of a
	// single usage_records or run_inventory object (16 MiB).
	DefaultTrajectoryArtifactMaxBytes int64 = 16 * 1024 * 1024

	// DefaultTrajectoryArtifactATIFMaxBytes is the maximum stored byte length
	// of a single ATIF trajectory object (64 MiB).
	DefaultTrajectoryArtifactATIFMaxBytes int64 = 64 * 1024 * 1024

	// TrajectoryRunInventorySchemaVersion is the current schema_version for
	// run_inventory JSON documents.
	TrajectoryRunInventorySchemaVersion = 1
)

// Track 2 L1 artifact types (P7 contract §6).
const (
	TrajectoryArtifactTypeUsageRecords = "usage_records"
	TrajectoryArtifactTypeATIF         = "atif"
	TrajectoryArtifactTypeRunInventory = "run_inventory"
)

// IsValidTrajectoryArtifactType reports whether v is a recognized L1 artifact_type.
func IsValidTrajectoryArtifactType(v string) bool {
	switch v {
	case TrajectoryArtifactTypeUsageRecords,
		TrajectoryArtifactTypeATIF,
		TrajectoryArtifactTypeRunInventory:
		return true
	default:
		return false
	}
}

// Track 2 L1 content encodings (TrajectoryContentEncodingGzip is defined in
// trajectory_segments.go).
const (
	TrajectoryContentEncodingIdentity = "identity"
)

// IsValidTrajectoryContentEncoding reports whether v is a valid L1 content_encoding.
func IsValidTrajectoryContentEncoding(v string) bool {
	return v == TrajectoryContentEncodingGzip || v == TrajectoryContentEncodingIdentity
}

// Track 2 ATIF file formats and schema versions (P7 contract §0 D7, §6).
// Readers must derive object suffixes from file_format rather than assuming
// ".atif.json.gz".
const (
	TrajectoryFileFormatJSON  = "json"
	TrajectoryFileFormatJSONL = "jsonl"

	TrajectoryATIFSchemaVersionV18 = "ATIF-v1.8"
)

// IsValidTrajectoryFileFormat reports whether v is a known ATIF file_format.
func IsValidTrajectoryFileFormat(v string) bool {
	return v == TrajectoryFileFormatJSON || v == TrajectoryFileFormatJSONL
}

// Track 2 L1 artifact prepare/commit states and persisted statuses.
const (
	TrajectoryArtifactStateUploadRequired = "upload_required"
	TrajectoryArtifactStateCommitted      = "committed"

	TrajectoryArtifactStatusPending   = "pending"
	TrajectoryArtifactStatusCommitted = "committed"

	TrajectorySessionNormalizeStatusOK          = "ok"
	TrajectorySessionNormalizeStatusParseFailed = "parse_failed"
)

// Machine-readable error codes returned by the Track 2 L1 artifact endpoints
// (in addition to TrajectoryErrCaptureDisabled, TrajectoryErrCaptureMetadataOnly,
// and TrajectoryErrRunCapExceeded from trajectory_segments.go).
const (
	TrajectoryErrInvalidArtifact            = "invalid_artifact"
	TrajectoryErrArtifactConflict           = "artifact_conflict"
	TrajectoryErrArtifactExpired            = "artifact_expired"
	TrajectoryErrArtifactTooLarge           = "artifact_too_large"
	TrajectoryErrArtifactVerificationFailed = "artifact_verification_failed"
	TrajectoryErrInventoryIncomplete        = "inventory_incomplete"
)

// ParseTrajectoryParserVersion parses a parser_version string matching
// ^[a-z0-9-]+-[0-9]+$ into its prefix (before the final hyphen) and numeric
// version suffix.
func ParseTrajectoryParserVersion(v string) (prefix string, num int64, ok bool) {
	idx := strings.LastIndexByte(v, '-')
	if idx <= 0 || idx == len(v)-1 {
		return "", 0, false
	}
	prefix = v[:idx]
	numStr := v[idx+1:]
	for i := 0; i < len(prefix); i++ {
		c := prefix[i]
		if !((c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-') {
			return "", 0, false
		}
	}
	for i := 0; i < len(numStr); i++ {
		if numStr[i] < '0' || numStr[i] > '9' {
			return "", 0, false
		}
	}
	n, err := strconv.ParseInt(numStr, 10, 64)
	if err != nil || n < 0 {
		return "", 0, false
	}
	return prefix, n, true
}

// IsValidTrajectoryParserVersion reports whether v matches ^[a-z0-9-]+-[0-9]+$.
func IsValidTrajectoryParserVersion(v string) bool {
	_, _, ok := ParseTrajectoryParserVersion(v)
	return ok
}

// CompareTrajectoryParserVersion compares two parser_version values.
// When both parse as <prefix>-<num> with the same prefix (e.g. "claude-2" vs
// "claude-10"), they are compared numerically by their suffix so "claude-10" >
// "claude-2". Otherwise they fall back to standard lexical comparison.
// Returns -1 if a < b, 0 if a == b, +1 if a > b.
func CompareTrajectoryParserVersion(a, b string) int {
	if a == b {
		return 0
	}
	ap, an, aok := ParseTrajectoryParserVersion(a)
	bp, bn, bok := ParseTrajectoryParserVersion(b)
	if aok && bok && ap == bp {
		switch {
		case an < bn:
			return -1
		case an > bn:
			return 1
		default:
			return 0
		}
	}
	if a < b {
		return -1
	}
	return 1
}

// TrajectorySessionKey computes the deterministic 16-hex-character session key
// derived from native_relative_path: sha256(native_relative_path)[:16].
func TrajectorySessionKey(nativeRelativePath string) string {
	sum := sha256.Sum256([]byte(nativeRelativePath))
	return hex.EncodeToString(sum[:])[:16]
}

// TrajectoryATIFFileExtension returns the file extension after ".atif." derived
// from fileFormat and contentEncoding (e.g. "json.gz", "jsonl.gz", "json").
func TrajectoryATIFFileExtension(fileFormat, contentEncoding string) string {
	ext := fileFormat
	if ext == "" {
		ext = TrajectoryFileFormatJSON
	}
	if contentEncoding == TrajectoryContentEncodingGzip {
		return ext + ".gz"
	}
	return ext
}

// TrajectoryArtifactsPrepareAgentPath returns the agent-authenticated L1
// prepare path: POST /api/v1/agents/{id}/trajectory/artifacts:prepare.
func TrajectoryArtifactsPrepareAgentPath(agentID string) string {
	return fmt.Sprintf("/api/v1/agents/%s/trajectory/artifacts:prepare", url.PathEscape(agentID))
}

// TrajectoryArtifactsCommitAgentPath returns the agent-authenticated L1
// commit path: POST /api/v1/agents/{id}/trajectory/artifacts:commit.
func TrajectoryArtifactsCommitAgentPath(agentID string) string {
	return fmt.Sprintf("/api/v1/agents/%s/trajectory/artifacts:commit", url.PathEscape(agentID))
}

// TrajectoryArtifactContentAgentPath returns the Hub-proxied L1 upload content
// path: PUT /api/v1/agents/{id}/trajectory/artifacts/{artifact_id}/content.
func TrajectoryArtifactContentAgentPath(agentID, artifactID string) string {
	return fmt.Sprintf("/api/v1/agents/%s/trajectory/artifacts/%s/content",
		url.PathEscape(agentID), url.PathEscape(artifactID))
}

// TrajectoryArtifactPendingObjectPath returns the Hub-chosen pending landing
// path for an unstamped L1 upload. Pending uploads never land under analytics/
// so BigQuery DTS never observes unstamped rows.
//
//	{hub_id}/trajectories/v1/l1-pending/{artifact_id}
func TrajectoryArtifactPendingObjectPath(hubID, artifactID string) string {
	return fmt.Sprintf("%s/trajectories/v1/l1-pending/%s", hubID, artifactID)
}

// TrajectoryUsageRecordsObjectPath returns the Hub-chosen final Tier A object
// path for a stamped usage_records chunk:
//
//	{hub_id}/analytics/v1/usage_records/dt={YYYY-MM-DD}/project={project_id}/{agent_id}__{run_id}__p{parser_version}__s{inventory_seq}__{chunk_id}.jsonl.gz
func TrajectoryUsageRecordsObjectPath(hubID string, prepareDate time.Time, projectID, agentID, runID, parserVersion string, inventorySeq int64, chunkID string) string {
	return fmt.Sprintf("%s/analytics/v1/usage_records/dt=%s/project=%s/%s__%s__p%s__s%d__%s.jsonl.gz",
		hubID, prepareDate.UTC().Format("2006-01-02"), projectID, agentID, runID, parserVersion, inventorySeq, chunkID)
}

// TrajectoryRunInventoryObjectPath returns the Hub-chosen final Tier A object
// path for a run_inventory document:
//
//	{hub_id}/analytics/v1/run_inventories/dt={YYYY-MM-DD}/project={project_id}/{agent_id}__{run_id}__p{parser_version}__s{inventory_seq}.inventory.json
func TrajectoryRunInventoryObjectPath(hubID string, prepareDate time.Time, projectID, agentID, runID, parserVersion string, inventorySeq int64) string {
	return fmt.Sprintf("%s/analytics/v1/run_inventories/dt=%s/project=%s/%s__%s__p%s__s%d.inventory.json",
		hubID, prepareDate.UTC().Format("2006-01-02"), projectID, agentID, runID, parserVersion, inventorySeq)
}

// TrajectoryATIFObjectPath returns the Hub-chosen final Tier B object path for
// a stamped ATIF trajectory document:
//
//	{hub_id}/trajectories/v1/atif/project={project_id}/agent={agent_id}/run={run_id}/{session_key}.p{parser_version}.s{parsed_offset}.atif.{ext}
func TrajectoryATIFObjectPath(hubID, projectID, agentID, runID, sessionKey, parserVersion string, parsedOffset int64, fileFormat, contentEncoding string) string {
	ext := TrajectoryATIFFileExtension(fileFormat, contentEncoding)
	return fmt.Sprintf("%s/trajectories/v1/atif/project=%s/agent=%s/run=%s/%s.p%s.s%d.atif.%s",
		hubID, projectID, agentID, runID, sessionKey, parserVersion, parsedOffset, ext)
}

// TrajectoryArtifactPrepareRequest is the wire payload for
// POST /api/v1/agents/{id}/trajectory/artifacts:prepare.
type TrajectoryArtifactPrepareRequest struct {
	ArtifactType       string `json:"artifact_type"`
	ParserVersion      string `json:"parser_version"`
	Harness            string `json:"harness"`
	ChunkID            string `json:"chunk_id,omitempty"`
	RowCount           int64  `json:"row_count,omitempty"`
	InventorySeq       int64  `json:"inventory_seq,omitempty"`
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

// TrajectoryArtifactPrepareResponse is returned by
// POST /api/v1/agents/{id}/trajectory/artifacts:prepare.
type TrajectoryArtifactPrepareResponse struct {
	ArtifactID string                              `json:"artifact_id"`
	State      string                              `json:"state"`
	ObjectPath string                              `json:"object_path"`
	Upload     *TrajectorySegmentUploadInstruction `json:"upload,omitempty"`
}

// TrajectorySessionMetaInput carries optional session metadata supplied on
// POST /api/v1/agents/{id}/trajectory/artifacts:commit for ATIF commits or
// standalone parse_failed reporting (P7 contract §4, §6).
type TrajectorySessionMetaInput struct {
	Harness            string `json:"harness,omitempty"`
	SessionID          string `json:"session_id,omitempty"`
	NativeRelativePath string `json:"native_relative_path,omitempty"`
	IsSubagent         bool   `json:"is_subagent,omitempty"`
	ParentSessionID    string `json:"parent_session_id,omitempty"`
	ParserVersion      string `json:"parser_version,omitempty"`
	ParsedOffset       int64  `json:"parsed_offset,omitempty"`
	StepCount          int    `json:"step_count,omitempty"`
	NormalizeStatus    string `json:"normalize_status,omitempty"`
	ParseErrorCount    int    `json:"parse_error_count,omitempty"`
	FirstError         string `json:"first_error,omitempty"`
}

// TrajectoryArtifactCommitRequest is the wire payload for
// POST /api/v1/agents/{id}/trajectory/artifacts:commit.
type TrajectoryArtifactCommitRequest struct {
	ArtifactID  string                      `json:"artifact_id,omitempty"`
	SessionMeta *TrajectorySessionMetaInput `json:"session_meta,omitempty"`
}

// TrajectoryArtifactCommitResponse is returned by
// POST /api/v1/agents/{id}/trajectory/artifacts:commit.
type TrajectoryArtifactCommitResponse struct {
	State         string `json:"state"`
	ArtifactID    string `json:"artifact_id,omitempty"`
	ObjectPath    string `json:"object_path,omitempty"`
	StampedSHA256 string `json:"stamped_sha256,omitempty"`
	StampedSize   int64  `json:"stamped_size,omitempty"`
}

// TrajectoryRunInventoryChunk describes one usage_records chunk listed inside
// a run_inventory JSON document.
type TrajectoryRunInventoryChunk struct {
	ChunkID          string `json:"chunk_id"`
	ExpectedRowCount int64  `json:"expected_row_count"`
	SHA256           string `json:"sha256"`
}

// TrajectoryRunInventory is the JSON document uploaded for artifact_type =
// "run_inventory" and rewritten by the Hub with stamped chunk SHA-256 digests.
type TrajectoryRunInventory struct {
	SchemaVersion int                           `json:"schema_version"`
	RunID         string                        `json:"run_id"`
	ParserVersion string                        `json:"parser_version"`
	InventorySeq  int64                         `json:"inventory_seq"`
	Chunks        []TrajectoryRunInventoryChunk `json:"chunks"`
	Final         bool                          `json:"final"`
}

// TrajectoryLabelKV is a key/value entry in the stamped labels[] array on
// Tier A usage_records rows and Tier B ATIF agent.extra.scion metadata.
type TrajectoryLabelKV struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

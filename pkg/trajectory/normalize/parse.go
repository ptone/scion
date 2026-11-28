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

package normalize

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/GoogleCloudPlatform/scion/pkg/telemetrycontract"
)

type parsedOutput struct {
	usageRecords  []telemetrycontract.UsageRecordInput
	atifSteps     map[string][]telemetrycontract.ATIFStep // session_id -> steps
	sessionMetas  map[string]telemetrycontract.SessionMeta
	sessionOrder  []string // deterministic order of session_ids seen
	parsedOffsets map[string]int64
	state         json.RawMessage
	strippedDims  int
	parseErrors   map[string]*telemetrycontract.ParseErrorSummary
	droppedTotal  int
	finalStepIDs  map[string]int // session_id -> highest accepted step_id
}

type framingEnvelope struct {
	Type string `json:"type"`
}

type parsedOffsetsLine struct {
	Type    string           `json:"type"`
	Offsets map[string]int64 `json:"offsets"`
}

type stateLine struct {
	Type  string          `json:"type"`
	State json.RawMessage `json:"state"`
}

type atifStepLine struct {
	Type      string          `json:"type"`
	SessionID string          `json:"session_id"`
	Step      json.RawMessage `json:"step"`
}

type sessionMetaLine struct {
	Type               string                      `json:"type"`
	SessionID          string                      `json:"session_id"`
	NativeRelativePath string                      `json:"native_relative_path"`
	Agent              telemetrycontract.ATIFAgent `json:"agent"`
	IsSubagent         bool                        `json:"is_subagent"`
	ParentSessionID    *string                     `json:"parent_session_id,omitempty"`
	Stats              map[string]any              `json:"stats,omitempty"`
}

// artifactBound describes the round input bounds and copy bytes for one
// artifact so parsed_offsets can be verified against line boundaries.
type artifactBound struct {
	key          string
	generation   int
	parsedOffset int64
	endOffset    int64
	copyBytes    []byte
}

func parseNormalizerOutput(
	lines [][]byte,
	harness string,
	bounds map[string]artifactBound,
	prevStepIDs map[string]int,
) (*parsedOutput, error) {
	out := &parsedOutput{
		atifSteps:    make(map[string][]telemetrycontract.ATIFStep),
		sessionMetas: make(map[string]telemetrycontract.SessionMeta),
		parseErrors:  make(map[string]*telemetrycontract.ParseErrorSummary),
		finalStepIDs: make(map[string]int, len(prevStepIDs)),
	}
	for k, v := range prevStepIDs {
		out.finalStepIDs[k] = v
	}

	seenSession := make(map[string]bool)
	recordSession := func(sid string) {
		if sid != "" && !seenSession[sid] {
			seenSession[sid] = true
			out.sessionOrder = append(out.sessionOrder, sid)
		}
	}
	recordDrop := func(sid string, err error) {
		out.droppedTotal++
		pe := out.parseErrors[sid]
		if pe == nil {
			pe = &telemetrycontract.ParseErrorSummary{FirstError: err.Error()}
			out.parseErrors[sid] = pe
		}
		pe.Count++
	}

	var (
		seenParsedOffsets bool
		seenState         bool
		stateWasLast      bool
	)

	// Filter trailing empty lines produced by final '\n' split.
	nonEmpty := make([][]byte, 0, len(lines))
	for _, l := range lines {
		trimmed := bytes.TrimSpace(l)
		if len(trimmed) == 0 {
			continue
		}
		nonEmpty = append(nonEmpty, trimmed)
	}

	for idx, line := range nonEmpty {
		if seenState {
			// "state" MUST be the last line (contract §2).
			return nil, fmt.Errorf("%w: unexpected output line after terminal state line", ErrParseFailure)
		}
		if len(line) > telemetrycontract.MaxNormalizerLineBytes {
			// Check if this was a framing line? Oversized line cannot be trusted.
			// If it's a data line, drop; if we never see valid parsed_offsets/state,
			// the round will still fail below.
			recordDrop("", fmt.Errorf("line %d exceeds maximum size of %d bytes", idx+1, telemetrycontract.MaxNormalizerLineBytes))
			continue
		}

		var env framingEnvelope
		if err := json.Unmarshal(line, &env); err != nil || env.Type == "" {
			return nil, fmt.Errorf("%w: invalid JSON framing on line %d", ErrParseFailure, idx+1)
		}

		switch env.Type {
		case "usage_record":
			rec, stripped, err := telemetrycontract.ValidateUsageRecordLine(line, harness)
			out.strippedDims += stripped
			if err != nil {
				sid := extractBestEffortSessionID(line)
				recordDrop(sid, err)
				continue
			}
			out.usageRecords = append(out.usageRecords, rec)

		case "atif_step":
			var sl atifStepLine
			if err := json.Unmarshal(line, &sl); err != nil {
				recordDrop("", fmt.Errorf("atif_step decode: %w", err))
				continue
			}
			if sl.SessionID == "" {
				recordDrop("", errors.New("atif_step: session_id is required"))
				continue
			}
			prevID := out.finalStepIDs[sl.SessionID]
			step, err := telemetrycontract.ValidateATIFStep(sl.Step, prevID)
			if err != nil {
				recordDrop(sl.SessionID, err)
				continue
			}
			out.finalStepIDs[sl.SessionID] = step.StepID
			out.atifSteps[sl.SessionID] = append(out.atifSteps[sl.SessionID], step)
			recordSession(sl.SessionID)

		case "session_meta":
			var sm sessionMetaLine
			if err := json.Unmarshal(line, &sm); err != nil {
				recordDrop("", fmt.Errorf("session_meta decode: %w", err))
				continue
			}
			if sm.SessionID == "" || sm.NativeRelativePath == "" {
				recordDrop(sm.SessionID, errors.New("session_meta: session_id and native_relative_path are required"))
				continue
			}
			if err := telemetrycontract.ValidateTrajectoryRelativePath(harness, sm.NativeRelativePath); err != nil {
				recordDrop(sm.SessionID, fmt.Errorf("session_meta: %w", err))
				continue
			}
			out.sessionMetas[sm.SessionID] = telemetrycontract.SessionMeta{
				SessionID:          sm.SessionID,
				NativeRelativePath: sm.NativeRelativePath,
				Agent:              sm.Agent,
				IsSubagent:         sm.IsSubagent,
				ParentSessionID:    sm.ParentSessionID,
				Stats:              sm.Stats,
			}
			recordSession(sm.SessionID)

		case "parsed_offsets":
			if seenParsedOffsets {
				return nil, fmt.Errorf("%w: duplicate parsed_offsets line", ErrParseFailure)
			}
			var po parsedOffsetsLine
			if err := json.Unmarshal(line, &po); err != nil || po.Offsets == nil {
				return nil, fmt.Errorf("%w: invalid parsed_offsets line", ErrParseFailure)
			}
			for key, off := range po.Offsets {
				b, ok := bounds[key]
				if !ok {
					return nil, fmt.Errorf("%w: parsed_offsets references unknown artifact %q", ErrParseFailure, key)
				}
				if off < b.parsedOffset || off > b.endOffset {
					return nil, fmt.Errorf("%w: parsed_offsets[%q]=%d outside [%d, %d]", ErrParseFailure, key, off, b.parsedOffset, b.endOffset)
				}
				if off > b.parsedOffset {
					relIdx := off - b.parsedOffset
					if relIdx > int64(len(b.copyBytes)) || b.copyBytes[relIdx-1] != '\n' {
						return nil, fmt.Errorf("%w: parsed_offsets[%q]=%d is not at a line boundary", ErrParseFailure, key, off)
					}
				}
			}
			out.parsedOffsets = po.Offsets
			seenParsedOffsets = true

		case "state":
			if seenState {
				return nil, fmt.Errorf("%w: duplicate state line", ErrParseFailure)
			}
			var st stateLine
			if err := json.Unmarshal(line, &st); err != nil {
				return nil, fmt.Errorf("%w: invalid state line: %v", ErrParseFailure, err)
			}
			trimmedState := bytes.TrimSpace(st.State)
			if len(trimmedState) == 0 || trimmedState[0] != '{' {
				return nil, fmt.Errorf("%w: state field must be a JSON object", ErrParseFailure)
			}
			if len(trimmedState) > telemetrycontract.MaxNormalizerStateBytes {
				return nil, fmt.Errorf("%w: state exceeds maximum size of %d bytes", ErrParseFailure, telemetrycontract.MaxNormalizerStateBytes)
			}
			var checkObj map[string]json.RawMessage
			if err := json.Unmarshal(trimmedState, &checkObj); err != nil {
				return nil, fmt.Errorf("%w: invalid state object: %v", ErrParseFailure, err)
			}
			out.state = append(json.RawMessage(nil), trimmedState...)
			seenState = true
			stateWasLast = (idx == len(nonEmpty)-1)

		default:
			recordDrop("", fmt.Errorf("unknown output line type %q", env.Type))
		}
	}

	if !seenParsedOffsets {
		return nil, fmt.Errorf("%w: missing required parsed_offsets line", ErrParseFailure)
	}
	if !seenState || !stateWasLast {
		return nil, fmt.Errorf("%w: missing required terminal state line", ErrParseFailure)
	}
	return out, nil
}

func extractBestEffortSessionID(line []byte) string {
	var partial struct {
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal(line, &partial); err == nil {
		return partial.SessionID
	}
	return ""
}

// mergeUsageRecords collapses duplicate dedupe_key records within a single
// round by taking per-channel max (contract §3, Claude streaming chunks).
// Returns the deduplicated slice in deterministic first-seen order and the
// number of duplicate rows merged.
func mergeUsageRecords(records []telemetrycontract.UsageRecordInput) ([]telemetrycontract.UsageRecordInput, int) {
	if len(records) <= 1 {
		return records, 0
	}
	indexByKey := make(map[string]int, len(records))
	out := make([]telemetrycontract.UsageRecordInput, 0, len(records))
	merged := 0

	for _, rec := range records {
		if idx, exists := indexByKey[rec.DedupeKey]; exists {
			out[idx] = mergeTwoUsageRecords(out[idx], rec)
			merged++
			continue
		}
		indexByKey[rec.DedupeKey] = len(out)
		out = append(out, rec)
	}
	return out, merged
}

func mergeTwoUsageRecords(dst, src telemetrycontract.UsageRecordInput) telemetrycontract.UsageRecordInput {
	if src.Calls > dst.Calls {
		dst.Calls = src.Calls
	}
	dst.Input = maxInt64Ptr(dst.Input, src.Input)
	dst.Output = maxInt64Ptr(dst.Output, src.Output)
	dst.CacheRead = maxInt64Ptr(dst.CacheRead, src.CacheRead)
	dst.CacheWrite = maxInt64Ptr(dst.CacheWrite, src.CacheWrite)
	dst.CacheWrite1h = maxInt64Ptr(dst.CacheWrite1h, src.CacheWrite1h)
	dst.Reasoning = maxInt64Ptr(dst.Reasoning, src.Reasoning)
	dst.ReportedCostMicros = maxInt64Ptr(dst.ReportedCostMicros, src.ReportedCostMicros)

	if src.SessionID != "" {
		dst.SessionID = src.SessionID
	}
	if src.NativeRequestIDRaw != "" {
		dst.NativeRequestIDRaw = src.NativeRequestIDRaw
	}
	if src.EventTS != "" {
		dst.EventTS = src.EventTS
	}
	if src.Model != "" {
		dst.Model = src.Model
	}
	if src.ModelSource != "" {
		dst.ModelSource = src.ModelSource
	}
	if src.ServiceTier != "" {
		dst.ServiceTier = src.ServiceTier
	}
	if src.Status != "" {
		dst.Status = src.Status
	}
	if src.InputDerivation != "" {
		dst.InputDerivation = src.InputDerivation
	}
	if src.TokenStatus != "" {
		dst.TokenStatus = src.TokenStatus
	}
	if src.ReportedCostCurrency != "" {
		dst.ReportedCostCurrency = src.ReportedCostCurrency
	}
	if src.ReportedCostSource != "" {
		dst.ReportedCostSource = src.ReportedCostSource
	}
	if src.IsSubagent {
		dst.IsSubagent = true
	}
	if src.SubagentID != "" {
		dst.SubagentID = src.SubagentID
	}
	return dst
}

func maxInt64Ptr(a, b *int64) *int64 {
	if a == nil && b == nil {
		return nil
	}
	if a == nil {
		v := *b
		return &v
	}
	if b == nil {
		v := *a
		return &v
	}
	v := *a
	if *b > v {
		v = *b
	}
	return &v
}

// versionGateUnverified reports whether harnessVersion is known (non-empty),
// testedGlobs is non-empty, and harnessVersion matches none of the globs
// (contract §4).
func versionGateUnverified(harnessVersion string, testedGlobs []string) bool {
	if harnessVersion == "" || len(testedGlobs) == 0 {
		return false
	}
	for _, glob := range testedGlobs {
		if matched, err := path.Match(glob, harnessVersion); err == nil && matched {
			return false
		}
	}
	return true
}

// buildUsageChunk builds the gzipped JSONL bytes and sha256 for a round's
// enriched usage records.
func buildUsageChunk(records []telemetrycontract.UsageRecord) (gzBytes []byte, shaHex string, err error) {
	var raw bytes.Buffer
	for _, r := range records {
		line, err := json.Marshal(r)
		if err != nil {
			return nil, "", fmt.Errorf("marshal usage_record: %w", err)
		}
		raw.Write(line)
		raw.WriteByte('\n')
	}
	gzBytes, err = gzipDeterministic(raw.Bytes())
	if err != nil {
		return nil, "", fmt.Errorf("gzip usage_records chunk: %w", err)
	}
	sum := sha256.Sum256(gzBytes)
	return gzBytes, hex.EncodeToString(sum[:]), nil
}

// buildATIFObject builds the gzipped ATIF-v1.8 Trajectory JSON and sha256 from
// the complete accumulated step list (contract §5).
func buildATIFObject(
	sessionID string,
	meta telemetrycontract.SessionMeta,
	parserVersion string,
	allSteps []json.RawMessage,
) (gzBytes []byte, shaHex string, err error) {
	agent := meta.Agent
	if agent.Name == "" {
		agent.Name = "unknown"
	}
	// Contract §5: Root agent.extra.scion is left for the Hub to stamp;
	// sciontool puts only parser_version there.
	agentExtra := make(map[string]any, len(agent.Extra)+1)
	for k, v := range agent.Extra {
		if k == "scion" {
			continue
		}
		agentExtra[k] = v
	}
	agentExtra["scion"] = map[string]any{
		"parser_version": parserVersion,
	}
	agent.Extra = agentExtra

	fm := computeATIFFinalMetrics(allSteps)
	traj := telemetrycontract.ATIFTrajectory{
		SchemaVersion: telemetrycontract.ATIFSchemaVersionV18,
		SessionID:     sessionID,
		Agent:         agent,
		Steps:         allSteps,
		FinalMetrics:  fm,
	}
	raw, err := json.Marshal(traj)
	if err != nil {
		return nil, "", fmt.Errorf("marshal ATIF trajectory: %w", err)
	}
	gzBytes, err = gzipDeterministic(raw)
	if err != nil {
		return nil, "", fmt.Errorf("gzip ATIF trajectory: %w", err)
	}
	sum := sha256.Sum256(gzBytes)
	return gzBytes, hex.EncodeToString(sum[:]), nil
}

func computeATIFFinalMetrics(steps []json.RawMessage) *telemetrycontract.ATIFFinalMetrics {
	if len(steps) == 0 {
		return nil
	}
	fm := &telemetrycontract.ATIFFinalMetrics{
		TotalSteps: len(steps),
	}
	var totalCost float64
	var hasCost bool
	for _, raw := range steps {
		var partial struct {
			Metrics *telemetrycontract.ATIFStepMetrics `json:"metrics"`
		}
		if err := json.Unmarshal(raw, &partial); err != nil || partial.Metrics == nil {
			continue
		}
		m := partial.Metrics
		if m.PromptTokens != nil {
			fm.TotalPromptTokens += *m.PromptTokens
		}
		if m.CompletionTokens != nil {
			fm.TotalCompletionTokens += *m.CompletionTokens
		}
		if m.CachedTokens != nil {
			fm.TotalCachedTokens += *m.CachedTokens
		}
		if m.CostUSD != nil {
			totalCost += *m.CostUSD
			hasCost = true
		}
	}
	if hasCost {
		fm.TotalCostUSD = &totalCost
	}
	return fm
}

// buildRunInventory encodes the cumulative RunInventory JSON document and
// returns its bytes and sha256 hex digest.
func buildRunInventory(inv telemetrycontract.RunInventory) ([]byte, string, error) {
	if inv.Chunks == nil {
		inv.Chunks = []telemetrycontract.RunInventoryChunk{}
	}
	raw, err := json.Marshal(inv)
	if err != nil {
		return nil, "", fmt.Errorf("marshal run_inventory: %w", err)
	}
	sum := sha256.Sum256(raw)
	return raw, hex.EncodeToString(sum[:]), nil
}

// gzipDeterministic compresses data with a deterministic gzip header (zero
// ModTime, OS=255) so crash-and-replay produces byte-identical output and sha256.
func gzipDeterministic(data []byte) ([]byte, error) {
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	gw.ModTime = time.Time{}
	gw.OS = 255
	if _, err := gw.Write(data); err != nil {
		_ = gw.Close()
		return nil, err
	}
	if err := gw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// findLineBoundaryEnd scans r from start up to limit and returns the byte
// offset immediately after the last '\n' in [start, limit). If no '\n' exists
// in that range, it returns start.
func findLineBoundaryEnd(r io.ReaderAt, start, limit int64) (int64, error) {
	if limit <= start {
		return start, nil
	}
	const chunkSize = 64 * 1024
	buf := make([]byte, chunkSize)
	pos := limit
	for pos > start {
		readStart := pos - chunkSize
		if readStart < start {
			readStart = start
		}
		n := int(pos - readStart)
		readN, err := r.ReadAt(buf[:n], readStart)
		if err != nil && !errors.Is(err, io.EOF) {
			return start, err
		}
		if idx := bytes.LastIndexByte(buf[:readN], '\n'); idx >= 0 {
			return readStart + int64(idx) + 1, nil
		}
		pos = readStart
	}
	return start, nil
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func truncateStderr(b []byte) string {
	if len(b) > telemetrycontract.MaxNormalizerStderrBytes {
		b = b[:telemetrycontract.MaxNormalizerStderrBytes]
	}
	return strings.TrimSpace(string(b))
}

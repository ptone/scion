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
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"strings"
	"time"
)

// Track 2 L1 normalization constants and limits (P7.1 contract §1–§5).
const (
	// NormalizerInterfaceVersion is the stdin/stdout protocol version for
	// harness normalizer processes.
	NormalizerInterfaceVersion = 1

	// TrajectoryL1UsageSchemaVersion is the schema_version stamped onto
	// enriched L1 usage_records rows.
	TrajectoryL1UsageSchemaVersion = 1

	// TrajectoryL1InventorySchemaVersion is the schema_version stamped onto
	// L1 run_inventory JSON documents.
	TrajectoryL1InventorySchemaVersion = 1

	// ATIFSchemaVersionV18 is the Harbor ATIF schema version targeted by P7.1.
	ATIFSchemaVersionV18 = "ATIF-v1.8"

	// ATIFFileFormatJSON is the file_format value for ATIF-v1.8 JSON objects.
	ATIFFileFormatJSON = "json"

	// MaxNormalizerLineBytes caps the maximum byte length of a single JSONL
	// output line emitted by a normalizer process (1 MiB).
	MaxNormalizerLineBytes = 1 << 20

	// MaxNormalizerStateBytes caps the serialized byte size of the opaque
	// normalizer state object (1 MiB).
	MaxNormalizerStateBytes = 1 << 20

	// MaxNormalizerStderrBytes caps stderr captured from a normalizer process
	// for diagnostic logs (64 KiB).
	MaxNormalizerStderrBytes = 64 << 10

	// MaxATIFAccumulationBytes caps the per-session on-disk ATIF step
	// accumulation file size (64 MiB).
	MaxATIFAccumulationBytes = 64 << 20

	// UsageRecordStatusOK and UsageRecordStatusError are the allowed status
	// values on Track 2 L1 usage_records.
	UsageRecordStatusOK    = "ok"
	UsageRecordStatusError = "error"

	// NormalizeStatusOK, NormalizeStatusNormalized, and NormalizeStatusParseFailed
	// record normalization outcomes on sessions and L0 segments.
	NormalizeStatusOK          = "ok"
	NormalizeStatusNormalized  = "normalized"
	NormalizeStatusParseFailed = "parse_failed"

	// ClaudeRequestIDScope and ClaudeRequestIDEvent are the Track 1 scope and
	// event name inputs for Claude native_request_id hashing (contract D3).
	ClaudeRequestIDScope = "com.anthropic.claude_code.events"
	ClaudeRequestIDEvent = "api_request"
)

var parserVersionPattern = regexp.MustCompile(`^[a-z0-9-]+-[0-9]+$`)

// ValidateParserVersion checks that parserVersion matches ^[a-z0-9-]+-[0-9]+$.
func ValidateParserVersion(parserVersion string) error {
	if !parserVersionPattern.MatchString(parserVersion) {
		return fmt.Errorf("invalid parser_version %q: must match ^[a-z0-9-]+-[0-9]+$", parserVersion)
	}
	return nil
}

// LengthPrefixedHash hashes parts unambiguously with SHA-256: each part is
// preceded by its decimal byte length and a colon, so no concatenation of
// different part lists can collide.
func LengthPrefixedHash(parts ...string) string {
	h := sha256.New()
	for _, p := range parts {
		_, _ = fmt.Fprintf(h, "%d:", len(p))
		h.Write([]byte(p))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// NativeRequestIDHash computes the Track 1 / Track 2 canonical "rid1:" hashed
// request identifier from (scope, event, id). Returns "" when id is empty.
func NativeRequestIDHash(scope, event, id string) string {
	if id == "" {
		return ""
	}
	return "rid1:" + LengthPrefixedHash(scope, event, id)
}

// NativeRequestIDHashForHarness computes the canonical "rid1:" hash for a
// harness's raw native request ID using the harness trajectory registry's
// scope and event inputs. Returns "" when rawID is empty.
func NativeRequestIDHashForHarness(harness, rawID string) string {
	if rawID == "" {
		return ""
	}
	if spec, ok := HarnessTrajectorySpecFor(harness); ok && spec.RequestIDScope != "" && spec.RequestIDEvent != "" {
		return NativeRequestIDHash(spec.RequestIDScope, spec.RequestIDEvent, rawID)
	}
	return NativeRequestIDHash(harness, "api_request", rawID)
}

// TrajectoryArtifactRule describes one allow-listed home-relative root, file
// suffix, and capture kind for a harness's native trajectory artifacts.
type TrajectoryArtifactRule struct {
	Root   string
	Suffix string
	Kind   string
}

// HarnessTrajectorySpec is the single Go registry entry for a harness's
// allow-listed trajectory roots and request-ID hashing inputs (contract D2).
type HarnessTrajectorySpec struct {
	Harness        string
	Rules          []TrajectoryArtifactRule
	RequestIDScope string
	RequestIDEvent string
}

var harnessTrajectoryRegistry = map[string]HarnessTrajectorySpec{
	"claude": {
		Harness: "claude",
		Rules: []TrajectoryArtifactRule{
			{
				Root:   ".claude/projects",
				Suffix: ".jsonl",
				Kind:   TrajectoryArtifactKindJSONLAppend,
			},
		},
		RequestIDScope: ClaudeRequestIDScope,
		RequestIDEvent: ClaudeRequestIDEvent,
	},
}

// HarnessTrajectorySpecFor returns a defensive copy of the built-in trajectory
// specification for harness, or (zero, false) if the harness is not registered.
func HarnessTrajectorySpecFor(harness string) (HarnessTrajectorySpec, bool) {
	spec, ok := harnessTrajectoryRegistry[harness]
	if !ok {
		return HarnessTrajectorySpec{}, false
	}
	rulesCopy := make([]TrajectoryArtifactRule, len(spec.Rules))
	copy(rulesCopy, spec.Rules)
	spec.Rules = rulesCopy
	return spec, true
}

// UsageRecordInput is the validated input form of a normalizer "usage_record"
// stdout line before sciontool enrichment (contract §3).
type UsageRecordInput struct {
	SessionID            string `json:"session_id,omitempty"`
	NativeRequestIDRaw   string `json:"native_request_id_raw,omitempty"`
	DedupeKey            string `json:"dedupe_key"`
	EventTS              string `json:"event_ts"`
	Model                string `json:"model,omitempty"`
	ModelSource          string `json:"model_source,omitempty"`
	ServiceTier          string `json:"service_tier,omitempty"`
	Status               string `json:"status"`
	Calls                int64  `json:"calls"`
	Input                *int64 `json:"input,omitempty"`
	Output               *int64 `json:"output,omitempty"`
	CacheRead            *int64 `json:"cache_read,omitempty"`
	CacheWrite           *int64 `json:"cache_write,omitempty"`
	CacheWrite1h         *int64 `json:"cache_write_1h,omitempty"`
	Reasoning            *int64 `json:"reasoning,omitempty"`
	InputDerivation      string `json:"input_derivation,omitempty"`
	TokenStatus          string `json:"token_status"`
	ReportedCostMicros   *int64 `json:"reported_cost_micros,omitempty"`
	ReportedCostCurrency string `json:"reported_cost_currency,omitempty"`
	ReportedCostSource   string `json:"reported_cost_source,omitempty"`
	IsSubagent           bool   `json:"is_subagent,omitempty"`
	SubagentID           string `json:"subagent_id,omitempty"`
}

// UsageRecord is the sciontool-enriched L1 usage record written to
// usage_records JSONL chunks before Hub attribution stamping (contract §3, §5).
// It never carries NativeRequestIDRaw.
type UsageRecord struct {
	SchemaVersion        int    `json:"schema_version"`
	Harness              string `json:"harness"`
	ParserVersion        string `json:"parser_version"`
	SessionID            string `json:"session_id,omitempty"`
	DedupeKey            string `json:"dedupe_key"`
	NativeRequestID      string `json:"native_request_id,omitempty"`
	EventTS              string `json:"event_ts"`
	CaptureTS            string `json:"capture_ts"`
	Model                string `json:"model,omitempty"`
	ModelSource          string `json:"model_source,omitempty"`
	ServiceTier          string `json:"service_tier,omitempty"`
	Status               string `json:"status"`
	Calls                int64  `json:"calls"`
	Input                *int64 `json:"input,omitempty"`
	Output               *int64 `json:"output,omitempty"`
	CacheRead            *int64 `json:"cache_read,omitempty"`
	CacheWrite           *int64 `json:"cache_write,omitempty"`
	CacheWrite1h         *int64 `json:"cache_write_1h,omitempty"`
	Reasoning            *int64 `json:"reasoning,omitempty"`
	ContextInputTotal    *int64 `json:"context_input_total,omitempty"`
	InputDerivation      string `json:"input_derivation,omitempty"`
	TokenStatus          string `json:"token_status"`
	ReportedCostMicros   *int64 `json:"reported_cost_micros,omitempty"`
	ReportedCostCurrency string `json:"reported_cost_currency,omitempty"`
	ReportedCostSource   string `json:"reported_cost_source,omitempty"`
	IsSubagent           bool   `json:"is_subagent,omitempty"`
	SubagentID           string `json:"subagent_id,omitempty"`
}

// EnrichUsageRecord converts a validated UsageRecordInput into an enriched
// UsageRecord, hashing NativeRequestIDRaw via NativeRequestIDHashForHarness
// and computing ContextInputTotal = input + cache_read + cache_write.
func EnrichUsageRecord(in UsageRecordInput, harness, parserVersion string, captureTS time.Time) UsageRecord {
	rec := UsageRecord{
		SchemaVersion:        TrajectoryL1UsageSchemaVersion,
		Harness:              harness,
		ParserVersion:        parserVersion,
		SessionID:            in.SessionID,
		DedupeKey:            in.DedupeKey,
		NativeRequestID:      NativeRequestIDHashForHarness(harness, in.NativeRequestIDRaw),
		EventTS:              in.EventTS,
		CaptureTS:            captureTS.UTC().Format(time.RFC3339Nano),
		Model:                in.Model,
		ModelSource:          in.ModelSource,
		ServiceTier:          in.ServiceTier,
		Status:               in.Status,
		Calls:                in.Calls,
		Input:                cloneInt64Ptr(in.Input),
		Output:               cloneInt64Ptr(in.Output),
		CacheRead:            cloneInt64Ptr(in.CacheRead),
		CacheWrite:           cloneInt64Ptr(in.CacheWrite),
		CacheWrite1h:         cloneInt64Ptr(in.CacheWrite1h),
		Reasoning:            cloneInt64Ptr(in.Reasoning),
		InputDerivation:      in.InputDerivation,
		TokenStatus:          in.TokenStatus,
		ReportedCostMicros:   cloneInt64Ptr(in.ReportedCostMicros),
		ReportedCostCurrency: in.ReportedCostCurrency,
		ReportedCostSource:   in.ReportedCostSource,
		IsSubagent:           in.IsSubagent,
		SubagentID:           in.SubagentID,
	}
	if in.Input != nil || in.CacheRead != nil || in.CacheWrite != nil {
		var total int64
		if in.Input != nil {
			total += *in.Input
		}
		if in.CacheRead != nil {
			total += *in.CacheRead
		}
		if in.CacheWrite != nil {
			total += *in.CacheWrite
		}
		rec.ContextInputTotal = &total
	}
	return rec
}

func cloneInt64Ptr(p *int64) *int64 {
	if p == nil {
		return nil
	}
	v := *p
	return &v
}

// ParseErrorSummary records dropped/invalid lines for a session (contract §3).
type ParseErrorSummary struct {
	Count      int    `json:"count"`
	FirstError string `json:"first_error,omitempty"`
}

// ATIFAgent represents the Harbor ATIF-v1.8 root agent object.
type ATIFAgent struct {
	Name      string         `json:"name"`
	Version   string         `json:"version,omitempty"`
	ModelName string         `json:"model_name,omitempty"`
	Extra     map[string]any `json:"extra,omitempty"`
}

// SessionMeta describes per-session metadata emitted by the normalizer and
// augmented by sciontool with parse error counts and status (contract §2–§4).
type SessionMeta struct {
	SessionID          string             `json:"session_id"`
	NativeRelativePath string             `json:"native_relative_path"`
	Agent              ATIFAgent          `json:"agent"`
	IsSubagent         bool               `json:"is_subagent"`
	ParentSessionID    *string            `json:"parent_session_id,omitempty"`
	Stats              map[string]any     `json:"stats,omitempty"`
	ParseError         *ParseErrorSummary `json:"parse_error,omitempty"`
	NormalizeStatus    string             `json:"normalize_status,omitempty"`
	StepCount          int                `json:"step_count,omitempty"`
}

// ATIFStepMetrics is the minimal typed view of ATIF-v1.8 Step.metrics checked
// by the Go trust-boundary validator (contract §3).
type ATIFStepMetrics struct {
	PromptTokens     *int64                     `json:"prompt_tokens,omitempty"`
	CompletionTokens *int64                     `json:"completion_tokens,omitempty"`
	CachedTokens     *int64                     `json:"cached_tokens,omitempty"`
	CostUSD          *float64                   `json:"cost_usd,omitempty"`
	Extra            map[string]json.RawMessage `json:"extra,omitempty"`
}

// ATIFStep is the minimal typed view of an ATIF-v1.8 Step checked by Go
// (contract §3). Raw holds the original validated JSON object so fields not
// inspected by Go (tool_calls, observation, reasoning_content, etc.) are
// preserved verbatim when building the ATIF trajectory.
type ATIFStep struct {
	StepID  int                        `json:"step_id"`
	Source  string                     `json:"source"`
	Message json.RawMessage            `json:"message"`
	Metrics *ATIFStepMetrics           `json:"metrics,omitempty"`
	Extra   map[string]json.RawMessage `json:"extra,omitempty"`

	Raw json.RawMessage `json:"-"`
}

// ATIFFinalMetrics summarizes token and step totals at the root of an
// ATIF-v1.8 Trajectory.
type ATIFFinalMetrics struct {
	TotalPromptTokens     int64    `json:"total_prompt_tokens,omitempty"`
	TotalCompletionTokens int64    `json:"total_completion_tokens,omitempty"`
	TotalCachedTokens     int64    `json:"total_cached_tokens,omitempty"`
	TotalCostUSD          *float64 `json:"total_cost_usd,omitempty"`
	TotalSteps            int      `json:"total_steps,omitempty"`
}

// ATIFTrajectory is the Harbor ATIF-v1.8 Trajectory document assembled by
// sciontool (contract §5).
type ATIFTrajectory struct {
	SchemaVersion string            `json:"schema_version"`
	SessionID     string            `json:"session_id"`
	Agent         ATIFAgent         `json:"agent"`
	Steps         []json.RawMessage `json:"steps"`
	FinalMetrics  *ATIFFinalMetrics `json:"final_metrics,omitempty"`
}

// RunInventoryChunk is one usage_records chunk entry inside RunInventory.
type RunInventoryChunk struct {
	ChunkID          string `json:"chunk_id"`
	ExpectedRowCount int    `json:"expected_row_count"`
	SHA256           string `json:"sha256"`
}

// RunInventory is the cumulative L1 release inventory for (run_id, parser_version)
// (contract §5).
type RunInventory struct {
	SchemaVersion int                 `json:"schema_version"`
	RunID         string              `json:"run_id"`
	ParserVersion string              `json:"parser_version"`
	InventorySeq  int                 `json:"inventory_seq"`
	Chunks        []RunInventoryChunk `json:"chunks"`
	Final         bool                `json:"final"`
}

// usageRecordAllowedFields is the closed allowlist of input fields accepted on
// a normalizer usage_record line (contract §3).
var usageRecordAllowedFields = map[string]struct{}{
	"type":                   {},
	"session_id":             {},
	"native_request_id_raw":  {},
	"dedupe_key":             {},
	"event_ts":               {},
	"model":                  {},
	"model_source":           {},
	"service_tier":           {},
	"status":                 {},
	"calls":                  {},
	"input":                  {},
	"output":                 {},
	"cache_read":             {},
	"cache_write":            {},
	"cache_write_1h":         {},
	"reasoning":              {},
	"input_derivation":       {},
	"token_status":           {},
	"reported_cost_micros":   {},
	"reported_cost_currency": {},
	"reported_cost_source":   {},
	"is_subagent":            {},
	"subagent_id":            {},
}

// usageRecordDimensionFields are attribution dimension fields owned by the Hub
// that are stripped and counted (not trusted) if emitted by a normalizer
// (contract §3, §6).
var usageRecordDimensionFields = map[string]struct{}{
	"hub_id":                {},
	"origin_hub_id":         {},
	"project_id":            {},
	"billing_user_id":       {},
	"agent_id":              {},
	"agent_slug":            {},
	"parent_agent_id":       {},
	"run_id":                {},
	"launch_id":             {},
	"harness_config":        {},
	"harness_config_digest": {},
	"template":              {},
	"template_digest":       {},
	"labels":                {},
}

// atifExtraScionAllowedKeys is the closed allowlist of keys under extra.scion
// (contract §3, design §5.4).
var atifExtraScionAllowedKeys = map[string]struct{}{
	"input":             {},
	"output":            {},
	"cache_read":        {},
	"cache_write":       {},
	"cache_write_1h":    {},
	"reasoning":         {},
	"input_derivation":  {},
	"token_status":      {},
	"service_tier":      {},
	"dedupe_key":        {},
	"native_request_id": {},
}

// ValidateUsageRecordLine validates one JSONL usage_record line emitted by a
// normalizer against contract §3:
//   - line length <= MaxNormalizerLineBytes (1 MiB)
//   - dimension fields are stripped and counted (returned in strippedDims)
//   - any other unknown field (including prompt/tool payload fields) is an error
//   - dedupe_key must start with "<harness>:"
//   - event_ts must be valid RFC3339/RFC3339Nano
//   - status must be "ok" or "error"
//   - calls must be an integer >= 1
//   - token fields and reported_cost_micros must be null or non-negative integers
//   - input_derivation must be "", "reported", or "derived"
//   - token_status must be a known TokenStatus* constant
func ValidateUsageRecordLine(line []byte, harness string) (UsageRecordInput, int, error) {
	if len(line) > MaxNormalizerLineBytes {
		return UsageRecordInput{}, 0, fmt.Errorf("usage_record line exceeds maximum size of %d bytes", MaxNormalizerLineBytes)
	}
	if harness == "" {
		return UsageRecordInput{}, 0, errors.New("harness is required")
	}

	rawObj, err := decodeJSONObjectStrict(line)
	if err != nil {
		return UsageRecordInput{}, 0, fmt.Errorf("usage_record: %w", err)
	}

	var strippedDims int
	cleaned := make(map[string]json.RawMessage, len(rawObj))
	for k, v := range rawObj {
		if _, isDim := usageRecordDimensionFields[k]; isDim {
			strippedDims++
			continue
		}
		if _, allowed := usageRecordAllowedFields[k]; !allowed {
			return UsageRecordInput{}, strippedDims, fmt.Errorf("usage_record: disallowed field %q", k)
		}
		if k == "type" {
			var typ string
			if err := json.Unmarshal(v, &typ); err != nil || typ != "usage_record" {
				return UsageRecordInput{}, strippedDims, fmt.Errorf("usage_record: invalid type field")
			}
			continue
		}
		cleaned[k] = v
	}

	// Verify integer fields before struct unmarshaling so floats like 1.5 or
	// negative values are rejected cleanly with exact field names.
	callsVal, callsPresent, err := parseStrictInt64Field(cleaned, "calls")
	if err != nil {
		return UsageRecordInput{}, strippedDims, fmt.Errorf("usage_record: %w", err)
	}
	if !callsPresent || callsVal == nil || *callsVal < 1 {
		return UsageRecordInput{}, strippedDims, errors.New("usage_record: calls must be an integer >= 1")
	}

	for _, name := range []string{"input", "output", "cache_read", "cache_write", "cache_write_1h", "reasoning", "reported_cost_micros"} {
		val, _, err := parseStrictInt64Field(cleaned, name)
		if err != nil {
			return UsageRecordInput{}, strippedDims, fmt.Errorf("usage_record: %w", err)
		}
		if val != nil && *val < 0 {
			return UsageRecordInput{}, strippedDims, fmt.Errorf("usage_record: %s must be non-negative (got %d)", name, *val)
		}
	}

	cleanedBytes, err := json.Marshal(cleaned)
	if err != nil {
		return UsageRecordInput{}, strippedDims, fmt.Errorf("usage_record: re-encode: %w", err)
	}
	var rec UsageRecordInput
	dec := json.NewDecoder(bytes.NewReader(cleanedBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&rec); err != nil {
		return UsageRecordInput{}, strippedDims, fmt.Errorf("usage_record: decode: %w", err)
	}

	prefix := harness + ":"
	if !strings.HasPrefix(rec.DedupeKey, prefix) || len(rec.DedupeKey) == len(prefix) {
		return UsageRecordInput{}, strippedDims, fmt.Errorf("usage_record: dedupe_key %q must start with %q", rec.DedupeKey, prefix)
	}
	if rec.EventTS == "" {
		return UsageRecordInput{}, strippedDims, errors.New("usage_record: event_ts is required")
	}
	if _, err := time.Parse(time.RFC3339Nano, rec.EventTS); err != nil {
		return UsageRecordInput{}, strippedDims, fmt.Errorf("usage_record: invalid event_ts %q: %w", rec.EventTS, err)
	}
	if rec.Status != UsageRecordStatusOK && rec.Status != UsageRecordStatusError {
		return UsageRecordInput{}, strippedDims, fmt.Errorf("usage_record: status %q must be %q or %q", rec.Status, UsageRecordStatusOK, UsageRecordStatusError)
	}
	if rec.InputDerivation != "" && rec.InputDerivation != InputDerivationReported && rec.InputDerivation != InputDerivationDerived {
		return UsageRecordInput{}, strippedDims, fmt.Errorf("usage_record: invalid input_derivation %q", rec.InputDerivation)
	}
	if !ValidTokenStatus(rec.TokenStatus) {
		return UsageRecordInput{}, strippedDims, fmt.Errorf("usage_record: invalid token_status %q", rec.TokenStatus)
	}
	if rec.ModelSource != "" && rec.ModelSource != ModelSourceEvent && rec.ModelSource != ModelSourceConfigured && rec.ModelSource != ModelSourceUnknown {
		return UsageRecordInput{}, strippedDims, fmt.Errorf("usage_record: invalid model_source %q", rec.ModelSource)
	}

	return rec, strippedDims, nil
}

// ValidTokenStatus reports whether v is one of the canonical TokenStatus* values.
func ValidTokenStatus(v string) bool {
	switch v {
	case TokenStatusReported, TokenStatusPartial, TokenStatusNotReported, TokenStatusUnverified, TokenStatusMalformed:
		return true
	default:
		return false
	}
}

// ValidateATIFStep validates a single ATIF-v1.8 step JSON object against the
// Go trust-boundary structural rules (contract §3).
//
// prevStepID is the previous accepted step_id for this session (pass 0 before
// the first step); step_id must be strictly greater than prevStepID.
func ValidateATIFStep(stepRaw []byte, prevStepID int) (ATIFStep, error) {
	if len(stepRaw) > MaxNormalizerLineBytes {
		return ATIFStep{}, fmt.Errorf("atif_step exceeds maximum size of %d bytes", MaxNormalizerLineBytes)
	}
	obj, err := decodeJSONObjectStrict(stepRaw)
	if err != nil {
		return ATIFStep{}, fmt.Errorf("atif_step: %w", err)
	}

	stepIDPtr, present, err := parseStrictInt64Field(obj, "step_id")
	if err != nil {
		return ATIFStep{}, fmt.Errorf("atif_step: %w", err)
	}
	if !present || stepIDPtr == nil {
		return ATIFStep{}, errors.New("atif_step: step_id is required")
	}
	if *stepIDPtr <= int64(prevStepID) || *stepIDPtr > math.MaxInt32 {
		return ATIFStep{}, fmt.Errorf("atif_step: step_id %d must be strictly greater than previous step_id %d", *stepIDPtr, prevStepID)
	}

	srcRaw, ok := obj["source"]
	if !ok {
		return ATIFStep{}, errors.New("atif_step: source is required")
	}
	var source string
	if err := json.Unmarshal(srcRaw, &source); err != nil {
		return ATIFStep{}, fmt.Errorf("atif_step: source must be a string: %w", err)
	}
	switch source {
	case "system", "user", "agent":
	default:
		return ATIFStep{}, fmt.Errorf("atif_step: invalid source %q (want system, user, or agent)", source)
	}

	msgRaw, ok := obj["message"]
	if !ok || isJSONNull(msgRaw) {
		return ATIFStep{}, errors.New("atif_step: message is required")
	}

	step := ATIFStep{
		StepID:  int(*stepIDPtr),
		Source:  source,
		Message: append(json.RawMessage(nil), msgRaw...),
		Raw:     append(json.RawMessage(nil), bytes.TrimSpace(stepRaw)...),
	}

	if metricsRaw, hasMetrics := obj["metrics"]; hasMetrics && !isJSONNull(metricsRaw) {
		metrics, err := validateATIFMetrics(metricsRaw, source)
		if err != nil {
			return ATIFStep{}, fmt.Errorf("atif_step: %w", err)
		}
		step.Metrics = metrics
	}

	if extraRaw, hasExtra := obj["extra"]; hasExtra && !isJSONNull(extraRaw) {
		extraObj, err := decodeJSONObjectStrict(extraRaw)
		if err != nil {
			return ATIFStep{}, fmt.Errorf("atif_step: extra: %w", err)
		}
		if err := validateATIFExtraScion(extraObj, source); err != nil {
			return ATIFStep{}, fmt.Errorf("atif_step: %w", err)
		}
		step.Extra = extraObj
	}

	return step, nil
}

func validateATIFMetrics(metricsRaw []byte, source string) (*ATIFStepMetrics, error) {
	obj, err := decodeJSONObjectStrict(metricsRaw)
	if err != nil {
		return nil, fmt.Errorf("metrics: %w", err)
	}
	m := &ATIFStepMetrics{}
	for _, field := range []struct {
		name string
		dst  **int64
	}{
		{"prompt_tokens", &m.PromptTokens},
		{"completion_tokens", &m.CompletionTokens},
		{"cached_tokens", &m.CachedTokens},
	} {
		val, _, err := parseStrictInt64Field(obj, field.name)
		if err != nil {
			return nil, fmt.Errorf("metrics: %w", err)
		}
		if val != nil && *val < 0 {
			return nil, fmt.Errorf("metrics: %s must be non-negative (got %d)", field.name, *val)
		}
		*field.dst = val
	}
	if costRaw, ok := obj["cost_usd"]; ok && !isJSONNull(costRaw) {
		var cost float64
		if err := json.Unmarshal(costRaw, &cost); err != nil || math.IsNaN(cost) || math.IsInf(cost, 0) || cost < 0 {
			return nil, fmt.Errorf("metrics: cost_usd must be a non-negative finite number")
		}
		m.CostUSD = &cost
	}
	if extraRaw, ok := obj["extra"]; ok && !isJSONNull(extraRaw) {
		extraObj, err := decodeJSONObjectStrict(extraRaw)
		if err != nil {
			return nil, fmt.Errorf("metrics.extra: %w", err)
		}
		if err := validateATIFExtraScion(extraObj, source); err != nil {
			return nil, fmt.Errorf("metrics.%w", err)
		}
		m.Extra = extraObj
	}
	return m, nil
}

func validateATIFExtraScion(extraObj map[string]json.RawMessage, source string) error {
	scionRaw, ok := extraObj["scion"]
	if !ok || isJSONNull(scionRaw) {
		return nil
	}
	scionObj, err := decodeJSONObjectStrict(scionRaw)
	if err != nil {
		return fmt.Errorf("extra.scion: %w", err)
	}
	for k := range scionObj {
		if k == "scion_input_channel" {
			if source != "user" {
				return fmt.Errorf("extra.scion.%s is only allowed on user steps (source=%q)", k, source)
			}
			continue
		}
		if _, allowed := atifExtraScionAllowedKeys[k]; !allowed {
			return fmt.Errorf("extra.scion: disallowed key %q", k)
		}
	}
	return nil
}

// decodeJSONObjectStrict decodes a JSON object without trailing tokens and
// rejects duplicate keys.
func decodeJSONObjectStrict(data []byte) (map[string]json.RawMessage, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()

	tok, err := dec.Token()
	if err != nil {
		return nil, fmt.Errorf("invalid JSON: %w", err)
	}
	delim, ok := tok.(json.Delim)
	if !ok || delim != '{' {
		return nil, errors.New("expected JSON object")
	}

	out := make(map[string]json.RawMessage)
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return nil, fmt.Errorf("invalid JSON key: %w", err)
		}
		key, ok := keyTok.(string)
		if !ok {
			return nil, errors.New("expected string object key")
		}
		if _, dup := out[key]; dup {
			return nil, fmt.Errorf("duplicate JSON key %q", key)
		}
		var val json.RawMessage
		if err := dec.Decode(&val); err != nil {
			return nil, fmt.Errorf("invalid JSON value for %q: %w", key, err)
		}
		out[key] = val
	}
	// Consume closing '}'.
	if _, err := dec.Token(); err != nil {
		return nil, fmt.Errorf("unterminated JSON object: %w", err)
	}
	// Reject trailing tokens.
	if _, err := dec.Token(); err == nil {
		return nil, errors.New("unexpected trailing JSON content")
	}
	return out, nil
}

// parseStrictInt64Field extracts an integer JSON field from obj, rejecting
// floats, strings, booleans, and overflows. A JSON null returns (nil, true, nil).
func parseStrictInt64Field(obj map[string]json.RawMessage, name string) (*int64, bool, error) {
	raw, ok := obj[name]
	if !ok {
		return nil, false, nil
	}
	if isJSONNull(raw) {
		return nil, true, nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var num json.Number
	if err := dec.Decode(&num); err != nil {
		return nil, true, fmt.Errorf("%s must be an integer", name)
	}
	s := num.String()
	if strings.ContainsAny(s, ".eE") {
		return nil, true, fmt.Errorf("%s must be an integer (got %s)", name, s)
	}
	v, err := num.Int64()
	if err != nil {
		return nil, true, fmt.Errorf("%s must be a valid int64: %w", name, err)
	}
	return &v, true, nil
}

func isJSONNull(raw json.RawMessage) bool {
	return bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
}

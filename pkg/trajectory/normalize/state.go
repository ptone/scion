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
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"

	"github.com/GoogleCloudPlatform/scion/pkg/telemetrycontract"
	"github.com/GoogleCloudPlatform/scion/pkg/trajectory/shipper"
)

const (
	normalizerExtraKey = "normalizer"
	atifSubdirName     = "atif"
)

// FailureTracker records consecutive parse failures on the exact tuple
// (key, generation, parser_version, parsed_offset) for the poison breaker
// (contract §4).
type FailureTracker struct {
	Generation    int    `json:"generation"`
	ParserVersion string `json:"parser_version"`
	ParsedOffset  int64  `json:"parsed_offset"`
	Count         int    `json:"count"`
	LastError     string `json:"last_error,omitempty"`
}

// NormalizedArtifactState tracks per-artifact normalization state stored
// inside NormalizerState.
type NormalizedArtifactState struct {
	Generation          int    `json:"generation"`
	SessionID           string `json:"session_id,omitempty"`
	LastStepID          int    `json:"last_step_id,omitempty"`
	ParseFailed         bool   `json:"parse_failed,omitempty"`
	ParseFailedOffset   int64  `json:"parse_failed_offset,omitempty"`
	ParseFailedReported bool   `json:"parse_failed_reported,omitempty"`
	LastError           string `json:"last_error,omitempty"`
}

// NormalizerState is persisted under shipper.State.Extra["normalizer"] so P5
// state compatibility (StateVersion = 1) is preserved (contract §4).
type NormalizerState struct {
	RunID               string                                `json:"run_id,omitempty"`
	ParserVersion       string                                `json:"parser_version"`
	OpaqueState         json.RawMessage                       `json:"state"`
	InventorySeq        int                                   `json:"inventory_seq"`
	Chunks              []telemetrycontract.RunInventoryChunk `json:"chunks,omitempty"`
	ConsecutiveFailures map[string]*FailureTracker            `json:"consecutive_failures,omitempty"`
	Artifacts           map[string]*NormalizedArtifactState   `json:"artifacts,omitempty"`
}

func newNormalizerState(runID, parserVersion string) *NormalizerState {
	return &NormalizerState{
		RunID:               runID,
		ParserVersion:       parserVersion,
		OpaqueState:         json.RawMessage(`{}`),
		ConsecutiveFailures: make(map[string]*FailureTracker),
		Artifacts:           make(map[string]*NormalizedArtifactState),
	}
}

// loadNormalizerState extracts NormalizerState from shipper.State.Extra["normalizer"].
// If absent, or if parserVersion changed, returns a fresh state initialized for
// (runID, parserVersion). If runID changed while parserVersion stayed the same,
// run-scoped inventory counters (InventorySeq, Chunks) reset while per-artifact
// offsets/state are preserved.
func loadNormalizerState(st *shipper.State, runID, parserVersion string) (*NormalizerState, error) {
	if st == nil || len(st.Extra) == 0 {
		return newNormalizerState(runID, parserVersion), nil
	}
	raw, ok := st.Extra[normalizerExtraKey]
	if !ok || len(bytes.TrimSpace(raw)) == 0 {
		return newNormalizerState(runID, parserVersion), nil
	}
	var ns NormalizerState
	if err := json.Unmarshal(raw, &ns); err != nil {
		return nil, fmt.Errorf("decode normalizer state: %w", err)
	}
	if ns.ParserVersion != parserVersion {
		return newNormalizerState(runID, parserVersion), nil
	}
	if ns.RunID != "" && runID != "" && ns.RunID != runID {
		ns.RunID = runID
		ns.InventorySeq = 0
		ns.Chunks = nil
	} else if ns.RunID == "" {
		ns.RunID = runID
	}
	if len(bytes.TrimSpace(ns.OpaqueState)) == 0 {
		ns.OpaqueState = json.RawMessage(`{}`)
	}
	if ns.ConsecutiveFailures == nil {
		ns.ConsecutiveFailures = make(map[string]*FailureTracker)
	}
	if ns.Artifacts == nil {
		ns.Artifacts = make(map[string]*NormalizedArtifactState)
	}
	return &ns, nil
}

func saveNormalizerState(st *shipper.State, ns *NormalizerState) error {
	if ns == nil {
		return errors.New("nil normalizer state")
	}
	if len(bytes.TrimSpace(ns.OpaqueState)) == 0 {
		ns.OpaqueState = json.RawMessage(`{}`)
	}
	raw, err := json.Marshal(ns)
	if err != nil {
		return fmt.Errorf("encode normalizer state: %w", err)
	}
	if st.Extra == nil {
		st.Extra = make(map[string]json.RawMessage)
	}
	st.Extra[normalizerExtraKey] = raw
	return nil
}

// SessionKey computes the 16-hex-character session key sha256(native_relative_path)[:16]
// specified by contract §4.
func SessionKey(nativeRelativePath string) string {
	sum := sha256.Sum256([]byte(nativeRelativePath))
	return hex.EncodeToString(sum[:])[:16]
}

// ArtifactRange identifies one artifact's normalized byte range in a round,
// used to compute deterministic chunk_ids (contract §4).
type ArtifactRange struct {
	Key        string
	Generation int
	Start      int64
	End        int64
}

// DeterministicChunkID computes a deterministic chunk_id from
// (run_id, parser_version, sorted [(key, generation, parsed_offset_start, parsed_offset_end)]).
// Replaying the same round after a crash produces the identical chunk_id so
// the Hub answers "committed" on replay (contract §4).
func DeterministicChunkID(runID, parserVersion string, ranges []ArtifactRange) string {
	sorted := make([]ArtifactRange, len(ranges))
	copy(sorted, ranges)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].Key != sorted[j].Key {
			return sorted[i].Key < sorted[j].Key
		}
		if sorted[i].Generation != sorted[j].Generation {
			return sorted[i].Generation < sorted[j].Generation
		}
		return sorted[i].Start < sorted[j].Start
	})
	parts := make([]string, 0, 2+len(sorted)*4)
	parts = append(parts, runID, parserVersion)
	for _, r := range sorted {
		parts = append(parts,
			r.Key,
			strconv.Itoa(r.Generation),
			strconv.FormatInt(r.Start, 10),
			strconv.FormatInt(r.End, 10),
		)
	}
	return "chk1-" + telemetrycontract.LengthPrefixedHash(parts...)[:32]
}

// atifStepsPath returns <home>/.scion/trajectory-shipper/atif/<session_key>.steps.jsonl.
func atifStepsPath(homeDir, nativeRelativePath string) string {
	return filepath.Join(shipper.StateDir(homeDir), atifSubdirName, SessionKey(nativeRelativePath)+".steps.jsonl")
}

// readATIFSteps reads all previously accumulated ATIF step JSON lines for a
// session. Returns nil slice and nil error if the file does not yet exist.
func readATIFSteps(homeDir, nativeRelativePath string, maxBytes int64) ([]json.RawMessage, int64, error) {
	path := atifStepsPath(homeDir, nativeRelativePath)
	fi, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, 0, nil
		}
		return nil, 0, fmt.Errorf("stat atif steps %s: %w", path, err)
	}
	if fi.Size() > maxBytes {
		return nil, fi.Size(), fmt.Errorf("atif steps file %s exceeds cap of %d bytes", path, maxBytes)
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, 0, fmt.Errorf("open atif steps %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()

	var steps []json.RawMessage
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), telemetrycontract.MaxNormalizerLineBytes+1024)
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		cp := make(json.RawMessage, len(line))
		copy(cp, line)
		steps = append(steps, cp)
	}
	if err := scanner.Err(); err != nil {
		return nil, fi.Size(), fmt.Errorf("read atif steps %s: %w", path, err)
	}
	return steps, fi.Size(), nil
}

// appendATIFSteps appends validated step JSON lines to
// <home>/.scion/trajectory-shipper/atif/<session_key>.steps.jsonl (mode 0600),
// enforcing the maxBytes cap. Must be called only while holding the shipper
// state lock after commits succeed.
func appendATIFSteps(homeDir, nativeRelativePath string, steps []json.RawMessage, maxBytes int64) error {
	if len(steps) == 0 {
		return nil
	}
	dir := filepath.Join(shipper.StateDir(homeDir), atifSubdirName)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("mkdir atif dir: %w", err)
	}
	path := atifStepsPath(homeDir, nativeRelativePath)

	var currentSize int64
	if fi, err := os.Stat(path); err == nil {
		currentSize = fi.Size()
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("stat atif steps: %w", err)
	}

	var buf bytes.Buffer
	for _, s := range steps {
		trimmed := bytes.TrimSpace(s)
		buf.Write(trimmed)
		buf.WriteByte('\n')
	}
	if currentSize+int64(buf.Len()) > maxBytes {
		return fmt.Errorf("atif steps for %s would exceed %d byte cap", nativeRelativePath, maxBytes)
	}

	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("open atif steps for append: %w", err)
	}
	if _, err := f.Write(buf.Bytes()); err != nil {
		_ = f.Close()
		return fmt.Errorf("write atif steps: %w", err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return fmt.Errorf("sync atif steps: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close atif steps: %w", err)
	}
	return nil
}

func removeATIFSteps(homeDir, nativeRelativePath string) {
	_ = os.Remove(atifStepsPath(homeDir, nativeRelativePath))
}

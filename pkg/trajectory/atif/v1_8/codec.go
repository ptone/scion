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

package v1_8

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// ParseTrajectory decodes JSON bytes into a Trajectory using json.Decoder with
// UseNumber so integers above 2^53 inside map[string]any fields (extra,
// tool_calls[].arguments, tool_definitions) are preserved verbatim as
// json.Number instead of losing precision via float64.
//
// Callers decoding external or stored ATIF JSON MUST use ParseTrajectory rather
// than plain json.Unmarshal. Note that empty maps (e.g. "extra": {}) are
// omitted on re-encode because optional map fields use omitempty; this is
// semantically equivalent in Harbor (empty dict vs absent).
func ParseTrajectory(data []byte) (*Trajectory, error) {
	var t Trajectory
	if err := decodeUseNumber(data, &t); err != nil {
		return nil, fmt.Errorf("parse ATIF trajectory: %w", err)
	}
	return &t, nil
}

// ParseStep decodes JSON bytes into a Step using json.Decoder with UseNumber
// so integers above 2^53 inside extra and tool_calls[].arguments are preserved
// verbatim. Callers decoding external ATIF steps MUST use ParseStep rather than
// plain json.Unmarshal.
func ParseStep(data []byte) (*Step, error) {
	var s Step
	if err := decodeUseNumber(data, &s); err != nil {
		return nil, fmt.Errorf("parse ATIF step: %w", err)
	}
	return &s, nil
}

// EncodeTrajectory encodes a Trajectory using the single canonical Go JSON
// encoder (deterministic struct field order, lexicographically sorted map keys,
// standard Go HTML escaping). All Scion writers must use EncodeTrajectory (or
// EncodeTrajectoryIndent for human-readable fixtures) so byte-identical
// determinism (G1) holds across writers.
func EncodeTrajectory(t *Trajectory) ([]byte, error) {
	if t == nil {
		return nil, errors.New("encode ATIF trajectory: nil trajectory")
	}
	return json.Marshal(t)
}

// EncodeTrajectoryIndent encodes a Trajectory with 2-space indentation using
// the same canonical encoding rules as EncodeTrajectory.
func EncodeTrajectoryIndent(t *Trajectory) ([]byte, error) {
	if t == nil {
		return nil, errors.New("encode ATIF trajectory: nil trajectory")
	}
	return json.MarshalIndent(t, "", "  ")
}

// EncodeStep encodes a single Step using the canonical Go JSON encoder.
func EncodeStep(s *Step) ([]byte, error) {
	if s == nil {
		return nil, errors.New("encode ATIF step: nil step")
	}
	return json.Marshal(s)
}

func decodeUseNumber(data []byte, dst any) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	if err := dec.Decode(dst); err != nil {
		return err
	}
	// Reject trailing tokens.
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		if err == nil {
			return errors.New("unexpected trailing JSON content")
		}
		return err
	}
	return nil
}

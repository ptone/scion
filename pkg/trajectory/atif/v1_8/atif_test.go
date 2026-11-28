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
	"strings"
	"testing"
)

func strPtr(s string) *string       { return &s }
func intPtr(i int) *int             { return &i }
func int64Ptr(i int64) *int64       { return &i }
func float64Ptr(f float64) *float64 { return &f }
func boolPtr(b bool) *bool          { return &b }

func TestTrajectory_RoundTripAndSchemaValidation(t *testing.T) {
	ts1 := "2026-11-28T20:00:00Z"
	ts2 := "2026-11-28T20:00:05Z"
	callID := "toolu_01ABC"
	subTrajID := "sub-1"

	traj := &Trajectory{
		SchemaVersion: SchemaVersionV18,
		SessionID:     strPtr("sess-001"),
		TrajectoryID:  strPtr("traj-001"),
		Agent: Agent{
			Name:      "claude-code",
			Version:   "2.1.296",
			ModelName: strPtr("claude-sonnet-4-6"),
			Extra: map[string]any{
				"scion": map[string]any{
					"parser_version": "claude-1",
				},
			},
		},
		Steps: []Step{
			{
				StepID:    1,
				Timestamp: &ts1,
				Source:    "user",
				Message:   TextMessage("Hello, please list files."),
				Extra: map[string]any{
					"scion": map[string]any{
						"scion_input_channel": "initial_task",
					},
				},
			},
			{
				StepID:           2,
				Timestamp:        &ts2,
				Source:           "agent",
				ModelName:        strPtr("claude-sonnet-4-6"),
				ReasoningEffort:  &ReasoningEffort{String: strPtr("high")},
				Message:          TextMessage("I will list the files."),
				ReasoningContent: strPtr("Let me call bash to inspect the directory."),
				ToolCalls: []ToolCall{
					{
						ToolCallID:   callID,
						FunctionName: "Bash",
						Arguments:    map[string]any{"command": "ls"},
					},
				},
				Observation: &Observation{
					Results: []ObservationResult{
						{
							SourceCallID: &callID,
							Content:      &MessageContent{Text: "README.md\n"},
							SubagentTrajectoryRef: []SubagentTrajectoryRef{
								{
									SessionID:    strPtr("sess-001"),
									TrajectoryID: &subTrajID,
								},
							},
						},
					},
				},
				Metrics: &Metrics{
					PromptTokens:     int64Ptr(150),
					CompletionTokens: int64Ptr(42),
					CachedTokens:     int64Ptr(100),
					CostUSD:          float64Ptr(0.0012),
					Extra: map[string]any{
						"scion": map[string]any{
							"input":        int64(30),
							"output":       int64(42),
							"cache_read":   int64(100),
							"cache_write":  int64(20),
							"token_status": "reported",
						},
					},
				},
				LLMCallCount: intPtr(1),
			},
			{
				StepID:          3,
				Timestamp:       &ts2,
				Source:          "user",
				IsCopiedContext: boolPtr(true),
				Message: PartsMessage([]ContentPart{
					{Type: "text", Text: strPtr("Check this image and audio")},
					{Type: "image", Source: &MediaSource{Image: &ImageSource{MediaType: "image/png", Path: "images/1.png"}}},
					{Type: "audio", Source: &MediaSource{Audio: &AudioSource{MediaType: "audio/wav", Path: "audio/1.wav", DurationSec: float64Ptr(2.5)}}},
				}),
			},
		},
		FinalMetrics: &FinalMetrics{
			TotalPromptTokens:     int64Ptr(150),
			TotalCompletionTokens: int64Ptr(42),
			TotalCachedTokens:     int64Ptr(100),
			TotalCostUSD:          float64Ptr(0.0012),
			TotalSteps:            intPtr(3),
		},
		SubagentTrajectories: []Trajectory{
			{
				SchemaVersion: SchemaVersionV18,
				SessionID:     strPtr("sess-001"),
				TrajectoryID:  &subTrajID,
				Agent: Agent{
					Name:    "claude-code",
					Version: "2.1.296",
				},
				Steps: []Step{
					{
						StepID:  1,
						Source:  "user",
						Message: TextMessage("subagent task"),
					},
				},
			},
		},
	}

	if err := ValidateTrajectory(traj); err != nil {
		t.Fatalf("ValidateTrajectory failed: %v", err)
	}

	b1, err := json.Marshal(traj)
	if err != nil {
		t.Fatalf("marshal 1: %v", err)
	}
	var decoded Trajectory
	if err := json.Unmarshal(b1, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	b2, err := json.Marshal(&decoded)
	if err != nil {
		t.Fatalf("marshal 2: %v", err)
	}
	if !bytes.Equal(b1, b2) {
		t.Fatalf("round-trip JSON not byte-identical:\n1: %s\n2: %s", b1, b2)
	}
	if err := ValidateTrajectoryJSON(b2); err != nil {
		t.Fatalf("ValidateTrajectoryJSON failed after round-trip: %v", err)
	}

	// Verify deterministic field order on Trajectory root keys.
	s := string(b1)
	idxSchema := strings.Index(s, `"schema_version"`)
	idxSession := strings.Index(s, `"session_id"`)
	idxAgent := strings.Index(s, `"agent"`)
	idxSteps := strings.Index(s, `"steps"`)
	idxFinal := strings.Index(s, `"final_metrics"`)
	if idxSchema >= idxSession || idxSession >= idxAgent || idxAgent >= idxSteps || idxSteps >= idxFinal {
		t.Fatalf("unexpected field order in encoded JSON: %s", s)
	}
}

func TestValidateStepJSON(t *testing.T) {
	validStep := `{"step_id":1,"source":"agent","message":"hi","metrics":{"prompt_tokens":10,"completion_tokens":5}}`
	if err := ValidateStepJSON([]byte(validStep)); err != nil {
		t.Fatalf("expected valid step, got: %v", err)
	}

	unknownField := `{"step_id":1,"source":"agent","message":"hi","unexpected_key":true}`
	if err := ValidateStepJSON([]byte(unknownField)); err == nil {
		t.Fatal("expected error for unknown field (additionalProperties: false)")
	}

	badSource := `{"step_id":1,"source":"assistant","message":"hi"}`
	if err := ValidateStepJSON([]byte(badSource)); err == nil {
		t.Fatal("expected error for invalid source enum")
	}
}

func TestValidateTrajectory_RejectsInvalid(t *testing.T) {
	// Unknown top-level property.
	if err := ValidateTrajectoryJSON([]byte(`{"schema_version":"ATIF-v1.8","agent":{"name":"a","version":"1"},"steps":[{"step_id":1,"source":"user","message":"hi"}],"bogus":1}`)); err == nil {
		t.Fatal("expected schema validation error for unknown property")
	}

	// Empty steps array (minItems: 1).
	if err := ValidateTrajectoryJSON([]byte(`{"schema_version":"ATIF-v1.8","agent":{"name":"a","version":"1"},"steps":[]}`)); err == nil {
		t.Fatal("expected schema validation error for empty steps")
	}

	// Non-sequential step_id.
	badSeq := &Trajectory{
		SchemaVersion: SchemaVersionV18,
		Agent:         Agent{Name: "claude-code", Version: "1.0"},
		Steps: []Step{
			{StepID: 2, Source: "user", Message: TextMessage("hi")},
		},
	}
	if err := ValidateTrajectory(badSeq); err == nil {
		t.Fatal("expected semantic error for non-sequential step_id")
	}

	// Agent-only field on user step.
	badUser := &Trajectory{
		SchemaVersion: SchemaVersionV18,
		Agent:         Agent{Name: "claude-code", Version: "1.0"},
		Steps: []Step{
			{StepID: 1, Source: "user", Message: TextMessage("hi"), ModelName: strPtr("claude")},
		},
	}
	if err := ValidateTrajectory(badUser); err == nil {
		t.Fatal("expected semantic error for model_name on user step")
	}

	// Unmatched source_call_id.
	badObs := &Trajectory{
		SchemaVersion: SchemaVersionV18,
		Agent:         Agent{Name: "claude-code", Version: "1.0"},
		Steps: []Step{
			{
				StepID:  1,
				Source:  "agent",
				Message: TextMessage("hi"),
				Observation: &Observation{
					Results: []ObservationResult{
						{SourceCallID: strPtr("missing_id"), Content: &MessageContent{Text: "out"}},
					},
				},
			},
		},
	}
	if err := ValidateTrajectory(badObs); err == nil {
		t.Fatal("expected semantic error for unmatched source_call_id")
	}
}

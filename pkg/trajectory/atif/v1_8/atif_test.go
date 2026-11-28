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
	"os"
	"os/exec"
	"path/filepath"
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
									TrajectoryID: &subTrajID,
									SessionID:    strPtr("sess-001"),
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
			{
				StepID:          4,
				Timestamp:       strPtr("2026-11-28 20:00:10+00:00"),
				Source:          "agent",
				ReasoningEffort: &ReasoningEffort{Number: float64Ptr(0.75)},
				Message:         TextMessage("Numeric reasoning effort step."),
			},
		},
		Notes: strPtr("sample notes"),
		FinalMetrics: &FinalMetrics{
			TotalPromptTokens:     int64Ptr(150),
			TotalCompletionTokens: int64Ptr(42),
			TotalCachedTokens:     int64Ptr(100),
			TotalCostUSD:          float64Ptr(0.0012),
			TotalSteps:            intPtr(4),
		},
		Extra: map[string]any{"custom": "val"},
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

	b1, err := EncodeTrajectory(traj)
	if err != nil {
		t.Fatalf("EncodeTrajectory 1: %v", err)
	}
	decoded, err := ParseTrajectory(b1)
	if err != nil {
		t.Fatalf("ParseTrajectory: %v", err)
	}
	b2, err := EncodeTrajectory(decoded)
	if err != nil {
		t.Fatalf("EncodeTrajectory 2: %v", err)
	}
	if !bytes.Equal(b1, b2) {
		t.Fatalf("round-trip JSON not byte-identical:\n1: %s\n2: %s", b1, b2)
	}
	if err := ValidateTrajectoryJSON(b2); err != nil {
		t.Fatalf("ValidateTrajectoryJSON failed after round-trip: %v", err)
	}

	// Verify Harbor model_fields declaration order on Trajectory root keys:
	// schema_version, session_id, trajectory_id, agent, steps, notes, final_metrics, extra, subagent_trajectories.
	wantKeys := []string{
		"schema_version",
		"session_id",
		"trajectory_id",
		"agent",
		"steps",
		"notes",
		"final_metrics",
		"extra",
		"subagent_trajectories",
	}
	gotKeys := topLevelJSONKeys(t, b1)
	if len(gotKeys) != len(wantKeys) {
		t.Fatalf("top-level keys = %v, want %v", gotKeys, wantKeys)
	}
	for i := range wantKeys {
		if gotKeys[i] != wantKeys[i] {
			t.Fatalf("top-level key[%d] = %q, want %q (all=%v)", i, gotKeys[i], wantKeys[i], gotKeys)
		}
	}
}

func topLevelJSONKeys(t *testing.T, data []byte) []string {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader(data))
	tok, err := dec.Token()
	if err != nil || tok != json.Delim('{') {
		t.Fatalf("expected object start, got %v (%v)", tok, err)
	}
	var keys []string
	for dec.More() {
		k, err := dec.Token()
		if err != nil {
			t.Fatalf("read key token: %v", err)
		}
		keys = append(keys, k.(string))
		var skip json.RawMessage
		if err := dec.Decode(&skip); err != nil {
			t.Fatalf("skip value: %v", err)
		}
	}
	return keys
}

// TestLosslessDecode_LargeIntegers verifies R2: decoding an external JSON
// literal containing integers > 2^53 inside agent.extra, step.extra, and
// tool_calls[].arguments preserves every digit verbatim through ParseTrajectory
// / ParseStep and EncodeTrajectory / EncodeStep.
func TestLosslessDecode_LargeIntegers(t *testing.T) {
	const rawTraj = `{
  "schema_version": "ATIF-v1.8",
  "agent": {
    "name": "claude-code",
    "version": "2.1.296",
    "extra": {"big": 9007199254740993, "huge": 12345678901234567890}
  },
  "steps": [
    {
      "step_id": 1,
      "source": "agent",
      "message": "calling tool",
      "tool_calls": [
        {
          "tool_call_id": "call_1",
          "function_name": "lookup",
          "arguments": {"id": 9007199254740993, "n": 12345678901234567890}
        }
      ],
      "extra": {"trace_id": 9007199254740993, "seq": 12345678901234567890}
    }
  ]
}`

	if err := ValidateTrajectoryJSON([]byte(rawTraj)); err != nil {
		t.Fatalf("ValidateTrajectoryJSON failed: %v", err)
	}
	traj, err := ParseTrajectory([]byte(rawTraj))
	if err != nil {
		t.Fatalf("ParseTrajectory failed: %v", err)
	}
	encoded, err := EncodeTrajectory(traj)
	if err != nil {
		t.Fatalf("EncodeTrajectory failed: %v", err)
	}
	out := string(encoded)
	if strings.Count(out, "9007199254740993") != 3 {
		t.Fatalf("expected 3 verbatim occurrences of 9007199254740993, got:\n%s", out)
	}
	if strings.Count(out, "12345678901234567890") != 3 {
		t.Fatalf("expected 3 verbatim occurrences of 12345678901234567890, got:\n%s", out)
	}

	const rawStep = `{"step_id":1,"source":"agent","message":"ok","tool_calls":[{"tool_call_id":"c1","function_name":"fn","arguments":{"a":9007199254740993,"b":12345678901234567890}}],"extra":{"x":9007199254740993}}`
	step, err := ParseStep([]byte(rawStep))
	if err != nil {
		t.Fatalf("ParseStep failed: %v", err)
	}
	stepOut, err := EncodeStep(step)
	if err != nil {
		t.Fatalf("EncodeStep failed: %v", err)
	}
	if strings.Count(string(stepOut), "9007199254740993") != 2 || !strings.Contains(string(stepOut), "12345678901234567890") {
		t.Fatalf("ParseStep/EncodeStep corrupted large integers:\n%s", stepOut)
	}
}

// TestValidateSemantics_BothEntryPoints covers every row of the R1 review table
// at both ValidateStepJSON and ValidateTrajectory / ValidateTrajectoryJSON.
func TestValidateSemantics_BothEntryPoints(t *testing.T) {
	wrapTraj := func(stepJSON string) string {
		return `{"schema_version":"ATIF-v1.8","agent":{"name":"claude-code","version":"2.1.296"},"steps":[` + stepJSON + `]}`
	}

	cases := []struct {
		name     string
		stepJSON string
	}{
		{
			name:     "content_part_text_missing_text",
			stepJSON: `{"step_id":1,"source":"user","message":[{"type":"text"}]}`,
		},
		{
			name:     "content_part_text_with_source",
			stepJSON: `{"step_id":1,"source":"user","message":[{"type":"text","text":"hi","source":{"media_type":"image/png","path":"a.png"}}]}`,
		},
		{
			name:     "content_part_image_with_audio_source",
			stepJSON: `{"step_id":1,"source":"user","message":[{"type":"image","source":{"media_type":"audio/wav","path":"a.wav"}}]}`,
		},
		{
			name:     "content_part_audio_with_image_source",
			stepJSON: `{"step_id":1,"source":"user","message":[{"type":"audio","source":{"media_type":"image/png","path":"a.png"}}]}`,
		},
		{
			name:     "content_part_image_with_text",
			stepJSON: `{"step_id":1,"source":"user","message":[{"type":"image","text":"caption","source":{"media_type":"image/png","path":"a.png"}}]}`,
		},
		{
			name:     "observation_content_part_image_with_audio_source",
			stepJSON: `{"step_id":1,"source":"agent","message":"hi","observation":{"results":[{"content":[{"type":"image","source":{"media_type":"audio/wav","path":"a.wav"}}]}]}}`,
		},
		{
			name:     "invalid_timestamp",
			stepJSON: `{"step_id":1,"source":"user","timestamp":"yesterday","message":"hi"}`,
		},
		{
			name:     "user_step_with_model_name",
			stepJSON: `{"step_id":1,"source":"user","model_name":"claude-sonnet-4-6","message":"hi"}`,
		},
		{
			name:     "user_step_with_empty_tool_calls",
			stepJSON: `{"step_id":1,"source":"user","tool_calls":[],"message":"hi"}`,
		},
		{
			name:     "llm_call_count_zero_with_metrics",
			stepJSON: `{"step_id":1,"source":"agent","llm_call_count":0,"message":"dispatch","metrics":{"prompt_tokens":10}}`,
		},
		{
			name:     "llm_call_count_zero_with_reasoning_content",
			stepJSON: `{"step_id":1,"source":"agent","llm_call_count":0,"message":"dispatch","reasoning_content":"hmm"}`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := ValidateStepJSON([]byte(tc.stepJSON)); err == nil {
				t.Errorf("ValidateStepJSON unexpectedly accepted %s", tc.name)
			}
			trajJSON := wrapTraj(tc.stepJSON)
			if err := ValidateTrajectoryJSON([]byte(trajJSON)); err == nil {
				t.Errorf("ValidateTrajectoryJSON unexpectedly accepted %s", tc.name)
			}
			parsed, err := ParseTrajectory([]byte(trajJSON))
			if err == nil {
				if err := ValidateTrajectory(parsed); err == nil {
					t.Errorf("ValidateTrajectory unexpectedly accepted %s", tc.name)
				}
			}
		})
	}
}

// TestNegativeFixtures_RejectedByGo validates every checked-in negative fixture
// under testdata/invalid/ is rejected by Go (hack/check-atif-schema.sh verifies
// Harbor Pydantic rejects the exact same files).
func TestNegativeFixtures_RejectedByGo(t *testing.T) {
	entries, err := os.ReadDir(filepath.Join("testdata", "invalid"))
	if err != nil {
		t.Fatalf("read testdata/invalid: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("expected negative fixtures in testdata/invalid")
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		t.Run(e.Name(), func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join("testdata", "invalid", e.Name()))
			if err != nil {
				t.Fatalf("read fixture: %v", err)
			}
			switch {
			case strings.HasSuffix(e.Name(), ".step.json"):
				if err := ValidateStepJSON(raw); err == nil {
					t.Fatalf("ValidateStepJSON accepted negative fixture %s", e.Name())
				}
			case strings.HasSuffix(e.Name(), ".trajectory.json"):
				if err := ValidateTrajectoryJSON(raw); err == nil {
					t.Fatalf("ValidateTrajectoryJSON accepted negative fixture %s", e.Name())
				}
			default:
				t.Fatalf("unknown negative fixture suffix: %s", e.Name())
			}
		})
	}
}

func TestValidateTrajectory_SubagentAndStructuralRejections(t *testing.T) {
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

	// Missing subagent trajectory_id.
	missingSubID := &Trajectory{
		SchemaVersion: SchemaVersionV18,
		Agent:         Agent{Name: "claude-code", Version: "1.0"},
		Steps:         []Step{{StepID: 1, Source: "user", Message: TextMessage("hi")}},
		SubagentTrajectories: []Trajectory{
			{
				SchemaVersion: SchemaVersionV18,
				Agent:         Agent{Name: "claude-code", Version: "1.0"},
				Steps:         []Step{{StepID: 1, Source: "user", Message: TextMessage("sub")}},
			},
		},
	}
	if err := ValidateTrajectory(missingSubID); err == nil {
		t.Fatal("expected semantic error for missing subagent trajectory_id")
	}

	// Duplicate subagent trajectory_id.
	dupSubID := &Trajectory{
		SchemaVersion: SchemaVersionV18,
		Agent:         Agent{Name: "claude-code", Version: "1.0"},
		Steps:         []Step{{StepID: 1, Source: "user", Message: TextMessage("hi")}},
		SubagentTrajectories: []Trajectory{
			{
				SchemaVersion: SchemaVersionV18,
				TrajectoryID:  strPtr("dup"),
				Agent:         Agent{Name: "claude-code", Version: "1.0"},
				Steps:         []Step{{StepID: 1, Source: "user", Message: TextMessage("sub1")}},
			},
			{
				SchemaVersion: SchemaVersionV18,
				TrajectoryID:  strPtr("dup"),
				Agent:         Agent{Name: "claude-code", Version: "1.0"},
				Steps:         []Step{{StepID: 1, Source: "user", Message: TextMessage("sub2")}},
			},
		},
	}
	if err := ValidateTrajectory(dupSubID); err == nil {
		t.Fatal("expected semantic error for duplicate subagent trajectory_id")
	}

	// MediaSource decode error on unknown media_type.
	var ms MediaSource
	if err := json.Unmarshal([]byte(`{"media_type":"video/mp4","path":"v.mp4"}`), &ms); err == nil {
		t.Fatal("expected error for unsupported MediaSource media_type")
	}
}

// TestGeneratedTypesUpToDate runs gen_types.go into a temporary directory and
// asserts that the committed types_gen.go matches byte-for-byte.
func TestGeneratedTypesUpToDate(t *testing.T) {
	outPath := filepath.Join(t.TempDir(), "types_gen.go")
	cmd := exec.Command("go", "run", "gen_types.go", "-out", outPath)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go run gen_types.go failed: %v\n%s", err, out)
	}
	generated, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("read generated output: %v", err)
	}
	committed, err := os.ReadFile("types_gen.go")
	if err != nil {
		t.Fatalf("read committed types_gen.go: %v", err)
	}
	if !bytes.Equal(generated, committed) {
		t.Fatalf("types_gen.go is out of date with schema.json/gen_types.go; run 'go generate ./pkg/trajectory/atif/v1_8/...'")
	}
}

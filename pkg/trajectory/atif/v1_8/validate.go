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
	_ "embed"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

// SchemaJSON is the embedded JSON Schema exported from Harbor's pinned
// ATIF-v1.8 Pydantic models (Trajectory.model_json_schema()).
//
//go:embed schema.json
var SchemaJSON []byte

const (
	trajectorySchemaURL = "https://scion.dev/schemas/atif/v1_8/schema.json"
	stepSchemaURL       = "https://scion.dev/schemas/atif/v1_8/step.schema.json"
)

var (
	compileOnce      sync.Once
	trajectorySchema *jsonschema.Schema
	stepSchema       *jsonschema.Schema
	compileErr       error
)

func compileSchemas() (*jsonschema.Schema, *jsonschema.Schema, error) {
	compileOnce.Do(func() {
		doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(SchemaJSON))
		if err != nil {
			compileErr = fmt.Errorf("unmarshal embedded ATIF schema: %w", err)
			return
		}

		c := jsonschema.NewCompiler()
		if err := c.AddResource(trajectorySchemaURL, doc); err != nil {
			compileErr = fmt.Errorf("add ATIF trajectory schema resource: %w", err)
			return
		}
		trajectorySchema, err = c.Compile(trajectorySchemaURL)
		if err != nil {
			compileErr = fmt.Errorf("compile ATIF trajectory schema: %w", err)
			return
		}

		// Compile a wrapper referencing #/$defs/Step for single-step validation.
		stepWrapper := map[string]any{
			"$ref": trajectorySchemaURL + "#/$defs/Step",
		}
		if err := c.AddResource(stepSchemaURL, stepWrapper); err != nil {
			compileErr = fmt.Errorf("add ATIF step schema resource: %w", err)
			return
		}
		stepSchema, err = c.Compile(stepSchemaURL)
		if err != nil {
			compileErr = fmt.Errorf("compile ATIF step schema: %w", err)
			return
		}
	})
	return trajectorySchema, stepSchema, compileErr
}

// ValidateTrajectoryJSON validates raw JSON bytes against both the committed
// Harbor ATIF-v1.8 JSON Schema (via santhosh-tekuri/jsonschema/v6) and Harbor's
// cross-field Pydantic semantic invariants.
func ValidateTrajectoryJSON(data []byte) error {
	if err := validateTrajectorySchemaOnly(data); err != nil {
		return err
	}
	t, err := ParseTrajectory(data)
	if err != nil {
		return err
	}
	return validateTrajectorySemantics(t)
}

func validateTrajectorySchemaOnly(data []byte) error {
	schema, _, err := compileSchemas()
	if err != nil {
		return err
	}
	v, err := jsonschema.UnmarshalJSON(bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("decode ATIF trajectory JSON: %w", err)
	}
	if err := schema.Validate(v); err != nil {
		return fmt.Errorf("ATIF-v1.8 trajectory schema validation failed: %w", err)
	}
	return nil
}

// ValidateStepJSON validates a single ATIF-v1.8 Step JSON object against
// #/$defs/Step in the committed Harbor ATIF-v1.8 JSON Schema and Harbor's
// Step-level semantic invariants (timestamp format, agent-only fields,
// llm_call_count == 0 rules, and ContentPart text/source constraints).
func ValidateStepJSON(data []byte) error {
	_, schema, err := compileSchemas()
	if err != nil {
		return err
	}
	v, err := jsonschema.UnmarshalJSON(bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("decode ATIF step JSON: %w", err)
	}
	if err := schema.Validate(v); err != nil {
		return fmt.Errorf("ATIF-v1.8 step schema validation failed: %w", err)
	}
	step, err := ParseStep(data)
	if err != nil {
		return err
	}
	return validateStepSemantics(step)
}

// ValidateTrajectory encodes t with EncodeTrajectory and validates it against
// both the committed ATIF-v1.8 JSON Schema and Harbor's cross-field semantic
// invariants (sequential step IDs, Step-level rules, tool_call_id references,
// and embedded subagent trajectory_id uniqueness).
func ValidateTrajectory(t *Trajectory) error {
	if t == nil {
		return fmt.Errorf("trajectory is nil")
	}
	data, err := EncodeTrajectory(t)
	if err != nil {
		return fmt.Errorf("encode ATIF trajectory: %w", err)
	}
	if err := validateTrajectorySchemaOnly(data); err != nil {
		return err
	}
	return validateTrajectorySemantics(t)
}

func validateTrajectorySemantics(t *Trajectory) error {
	for i := range t.Steps {
		step := &t.Steps[i]
		expectedID := i + 1
		if step.StepID != expectedID {
			return fmt.Errorf("steps[%d].step_id: expected %d (sequential from 1), got %d", i, expectedID, step.StepID)
		}
		if err := validateStepSemantics(step); err != nil {
			return fmt.Errorf("steps[%d]: %w", i, err)
		}
		if step.Observation != nil {
			callIDs := make(map[string]struct{}, len(step.ToolCalls))
			for _, tc := range step.ToolCalls {
				callIDs[tc.ToolCallID] = struct{}{}
			}
			for j, res := range step.Observation.Results {
				if res.SourceCallID != nil {
					if _, ok := callIDs[*res.SourceCallID]; !ok {
						return fmt.Errorf("steps[%d].observation.results[%d].source_call_id %q not found in step tool_calls", i, j, *res.SourceCallID)
					}
				}
			}
		}
	}

	if len(t.SubagentTrajectories) > 0 {
		seen := make(map[string]struct{}, len(t.SubagentTrajectories))
		for i := range t.SubagentTrajectories {
			sub := &t.SubagentTrajectories[i]
			if sub.TrajectoryID == nil || *sub.TrajectoryID == "" {
				return fmt.Errorf("subagent_trajectories[%d].trajectory_id is required for embedded subagents", i)
			}
			if _, dup := seen[*sub.TrajectoryID]; dup {
				return fmt.Errorf("subagent_trajectories[%d].trajectory_id %q is not unique", i, *sub.TrajectoryID)
			}
			seen[*sub.TrajectoryID] = struct{}{}
			if err := validateTrajectorySemantics(sub); err != nil {
				return fmt.Errorf("subagent_trajectories[%d]: %w", i, err)
			}
		}
	}
	return nil
}

// validateStepSemantics enforces Harbor's Step and ContentPart Pydantic
// validators (step.py and content.py).
func validateStepSemantics(step *Step) error {
	if step == nil {
		return fmt.Errorf("step is nil")
	}
	if step.Timestamp != nil {
		if err := validateISO8601Timestamp(*step.Timestamp); err != nil {
			return fmt.Errorf("timestamp: %w", err)
		}
	}
	if step.Source != "agent" {
		if step.ModelName != nil {
			return fmt.Errorf("model_name is only allowed when source is 'agent' (got %q)", step.Source)
		}
		if step.ReasoningEffort != nil {
			return fmt.Errorf("reasoning_effort is only allowed when source is 'agent' (got %q)", step.Source)
		}
		if step.ReasoningContent != nil {
			return fmt.Errorf("reasoning_content is only allowed when source is 'agent' (got %q)", step.Source)
		}
		if step.ToolCalls != nil {
			return fmt.Errorf("tool_calls is only allowed when source is 'agent' (got %q)", step.Source)
		}
		if step.Metrics != nil {
			return fmt.Errorf("metrics is only allowed when source is 'agent' (got %q)", step.Source)
		}
	}
	if step.Source == "agent" && step.LLMCallCount != nil && *step.LLMCallCount == 0 {
		if step.Metrics != nil {
			return fmt.Errorf("metrics must be absent when llm_call_count is 0")
		}
		if step.ReasoningContent != nil {
			return fmt.Errorf("reasoning_content must be absent when llm_call_count is 0")
		}
	}

	if step.Message.IsParts {
		for i := range step.Message.Parts {
			if err := validateContentPart(&step.Message.Parts[i]); err != nil {
				return fmt.Errorf("message[%d]: %w", i, err)
			}
		}
	}
	if step.Observation != nil {
		for i := range step.Observation.Results {
			c := step.Observation.Results[i].Content
			if c != nil && c.IsParts {
				for j := range c.Parts {
					if err := validateContentPart(&c.Parts[j]); err != nil {
						return fmt.Errorf("observation.results[%d].content[%d]: %w", i, j, err)
					}
				}
			}
		}
	}
	return nil
}

func validateContentPart(p *ContentPart) error {
	switch p.Type {
	case "text":
		if p.Text == nil {
			return fmt.Errorf("type='text' requires 'text' field")
		}
		if p.Source != nil {
			return fmt.Errorf("type='text' must not have 'source' field")
		}
	case "image":
		if p.Text != nil {
			return fmt.Errorf("type='image' must not have 'text' field")
		}
		if p.Source == nil || p.Source.Image == nil || p.Source.Audio != nil {
			return fmt.Errorf("type='image' requires an ImageSource")
		}
	case "audio":
		if p.Text != nil {
			return fmt.Errorf("type='audio' must not have 'text' field")
		}
		if p.Source == nil || p.Source.Audio == nil || p.Source.Image != nil {
			return fmt.Errorf("type='audio' requires an AudioSource")
		}
	default:
		return fmt.Errorf("unsupported content part type %q", p.Type)
	}
	return nil
}

// iso8601Layouts covers the common ISO 8601 forms accepted by Python's
// datetime.fromisoformat(v.replace("Z", "+00:00")): RFC 3339 (with/without
// fractional seconds), space-separated datetime, naive datetime, and date-only.
// Note: Go is intentionally slightly stricter than Python 3.11+ fromisoformat
// on esoteric forms (e.g. week dates like 2026-W01-1); Scion producers always
// emit RFC 3339 UTC timestamps.
var iso8601Layouts = []string{
	time.RFC3339Nano,
	time.RFC3339,
	"2006-01-02 15:04:05.999999999Z07:00",
	"2006-01-02 15:04:05Z07:00",
	"2006-01-02T15:04:05.999999999",
	"2006-01-02T15:04:05",
	"2006-01-02 15:04:05.999999999",
	"2006-01-02 15:04:05",
	"2006-01-02",
}

func validateISO8601Timestamp(v string) error {
	if strings.TrimSpace(v) != v || v == "" {
		return fmt.Errorf("invalid ISO 8601 timestamp %q", v)
	}
	for _, layout := range iso8601Layouts {
		if _, err := time.Parse(layout, v); err == nil {
			return nil
		}
	}
	return fmt.Errorf("invalid ISO 8601 timestamp %q", v)
}

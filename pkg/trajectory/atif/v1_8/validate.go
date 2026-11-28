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
	"encoding/json"
	"fmt"
	"sync"

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

// ValidateTrajectoryJSON validates raw JSON bytes against the committed
// Harbor ATIF-v1.8 JSON Schema using santhosh-tekuri/jsonschema/v6.
func ValidateTrajectoryJSON(data []byte) error {
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
// #/$defs/Step in the committed Harbor ATIF-v1.8 JSON Schema.
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
	return nil
}

// ValidateTrajectory marshals t to JSON and validates it against the committed
// ATIF-v1.8 JSON Schema as well as Harbor's cross-field semantic invariants
// (sequential step IDs, agent-only step fields, llm_call_count=0 restrictions,
// tool_call_id references, and embedded subagent trajectory_id uniqueness).
func ValidateTrajectory(t *Trajectory) error {
	if t == nil {
		return fmt.Errorf("trajectory is nil")
	}
	data, err := json.Marshal(t)
	if err != nil {
		return fmt.Errorf("marshal ATIF trajectory: %w", err)
	}
	if err := ValidateTrajectoryJSON(data); err != nil {
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
		if step.Source != "agent" {
			if step.ModelName != nil {
				return fmt.Errorf("steps[%d].model_name is only allowed when source is 'agent' (got %q)", i, step.Source)
			}
			if step.ReasoningEffort != nil {
				return fmt.Errorf("steps[%d].reasoning_effort is only allowed when source is 'agent' (got %q)", i, step.Source)
			}
			if step.ReasoningContent != nil {
				return fmt.Errorf("steps[%d].reasoning_content is only allowed when source is 'agent' (got %q)", i, step.Source)
			}
			if len(step.ToolCalls) > 0 {
				return fmt.Errorf("steps[%d].tool_calls is only allowed when source is 'agent' (got %q)", i, step.Source)
			}
			if step.Metrics != nil {
				return fmt.Errorf("steps[%d].metrics is only allowed when source is 'agent' (got %q)", i, step.Source)
			}
		}
		if step.Source == "agent" && step.LLMCallCount != nil && *step.LLMCallCount == 0 {
			if step.Metrics != nil {
				return fmt.Errorf("steps[%d].metrics must be absent when llm_call_count is 0", i)
			}
			if step.ReasoningContent != nil {
				return fmt.Errorf("steps[%d].reasoning_content must be absent when llm_call_count is 0", i)
			}
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

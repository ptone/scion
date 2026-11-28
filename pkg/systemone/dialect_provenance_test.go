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

package systemone

import (
	"context"
	"testing"
	"time"

	"github.com/GoogleCloudPlatform/scion/pkg/ruleengine"
	"github.com/GoogleCloudPlatform/scion/pkg/systemone/provider"
	"github.com/GoogleCloudPlatform/scion/pkg/systemone/spec"
)

func TestDialect_ProgramIDAndTraceCapabilities(t *testing.T) {
	d := NewDialect(DialectConfig{
		Enabled: func() bool { return true },
	})
	caps := d.Declares()
	if !caps.PerformsIO {
		t.Fatal("expected PerformsIO=true")
	}
	if len(caps.TraceReasons) == 0 {
		t.Fatal("expected non-empty TraceReasons")
	}
	if len(caps.TraceFields) == 0 {
		t.Fatal("expected non-empty TraceFields")
	}

	rawSpec := `{
		"version":1,
		"questions":[{"id":"q1","type":"binary","instructions":"Is this urgent?"}],
		"output":{"question":"q1"}
	}`
	compiled, err := d.Compile(context.Background(), rawSpec, ruleengine.KindFilter, ruleengine.EnvironmentRef{
		Consumer: spec.ProbeEnvironmentName,
		Name:     spec.ProbeEnvironmentName,
	})
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if compiled.ProgramID() == "" {
		t.Fatal("expected Compiled.ProgramID() to be populated from spec ContentHash")
	}

	// Verify ProjectTraceProvenance validates cleanly against Capabilities.TraceFields.
	bTrue := true
	eval := &Evaluation{
		EvalID:        "eval-0123456789abcdef0123456789abcdef",
		Class:         ClassSuccess,
		ResolvedModel: "models/gemini-2.5-flash",
		Elapsed:       42 * time.Millisecond,
		Attempts: []Attempt{{
			Instance:         "inst-primary",
			InstanceRevision: 3,
		}},
		Answers: map[string]AnswerRecord{
			"q1": {
				QuestionID:   "q1",
				Primitive:    spec.PrimitiveBinary,
				Status:       provider.AnswerStatusAnswered,
				Bool:         &bTrue,
				Distribution: map[string]float64{"true": 0.91, "false": 0.09},
			},
		},
		Input: InputFingerprint{
			Fields: []FieldFingerprint{{Name: "event.subject", Bytes: 256, Truncated: true}},
		},
	}
	tp := ProjectTraceProvenance(eval)
	if tp == nil || len(tp.Fields) == 0 {
		t.Fatal("expected non-empty TraceProvenance")
	}

	traceJSON, err := ruleengine.EncodeChainTrace(ruleengine.ChainTrace{
		Elements: []ruleengine.ChainTraceElement{{
			Index:      1,
			Dialect:    DialectName,
			Kind:       ruleengine.KindFilter,
			Class:      ruleengine.StepClassPass,
			DurationMs: 42,
			Domain:     ruleengine.DomainIO,
			Provenance: tp.Fields,
		}},
	}, map[string]ruleengine.Capabilities{DialectName: caps}, 4096)
	if err != nil {
		t.Fatalf("EncodeChainTrace rejected dialect TraceProvenance: %v", err)
	}
	if traceJSON == "" {
		t.Fatal("expected non-empty traceJSON")
	}

	// CompactRuleTraceProvenance carries §2.7 fields including p_top and truncated field names.
	compact := CompactRuleTraceProvenance(eval)
	if compact["class"] != "success" || compact["instance"] != "inst-primary" || compact["instance_rev"] != int64(3) {
		t.Fatalf("unexpected compact provenance: %#v", compact)
	}
	if compact["answer"] != true || compact["p_top"] != 0.91 {
		t.Fatalf("unexpected answer/p_top in compact provenance: %#v", compact)
	}
	trunc, ok := compact["truncated"].([]string)
	if !ok || len(trunc) != 1 || trunc[0] != "event.subject" {
		t.Fatalf("unexpected truncated field list: %#v", compact["truncated"])
	}
}

func TestDialect_NilEnabledFailsClosed(t *testing.T) {
	d := NewDialect(DialectConfig{
		Enabled: nil,
	})
	rawSpec := `{"version":1,"questions":[{"id":"q1","type":"binary","instructions":"ok?"}],"output":{"question":"q1"}}`
	_, err := d.Compile(context.Background(), rawSpec, ruleengine.KindFilter, ruleengine.EnvironmentRef{
		Consumer: spec.ProbeEnvironmentName,
		Name:     spec.ProbeEnvironmentName,
	})
	if err == nil {
		t.Fatal("expected nil Enabled to refuse Compile")
	}

	compiled := ruleengine.NewCompiled(spec.ProbeEnvironmentName, DialectName, ruleengine.KindFilter, nil)
	_, err = d.Evaluate(context.Background(), compiled, ruleengine.Input{Payload: ProbeInput("hi")}, ruleengine.Budget{})
	var fErr *ruleengine.FailedError
	if err == nil {
		t.Fatal("expected nil Enabled to fail Evaluate")
	}
	if fe, ok := err.(*ruleengine.FailedError); !ok || fe.Reason != ReasonDialectDisabled {
		t.Fatalf("expected FailedError{Reason: %q}, got %T: %v", ReasonDialectDisabled, err, err)
	}
	_ = fErr
}

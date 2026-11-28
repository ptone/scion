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

package runtime

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/GoogleCloudPlatform/scion/pkg/systemone/provider"
	"github.com/GoogleCloudPlatform/scion/pkg/systemone/spec"
)

func TestSelectInputs_PayloadPathsNarrowing(t *testing.T) {
	env := spec.ContentEnvironment{
		Name:    "trigger",
		Version: 1,
		Fields: []spec.FieldDecl{
			{Name: "event.type", Kind: spec.FieldKindText, MaxBytes: 128, Trust: spec.TrustUntrusted},
			{Name: "event.payload", Kind: spec.FieldKindJSON, MaxBytes: 24576, Trust: spec.TrustUntrusted},
		},
		MaxTotalBytes: 32 * 1024,
	}

	rawRule := []byte(`{
		"inputs":["event.payload"],
		"payload_paths":["pull_request.title","action"],
		"spec":{
			"version":1,
			"questions":[{"id":"q1","type":"binary","instructions":"Does this PR need review?"}],
			"output":{"question":"q1"}
		}
	}`)
	compiled, err := spec.Compile(rawRule, spec.CompileOptions{
		Kind:        spec.KindFilter,
		Environment: &env,
	})
	if err != nil {
		t.Fatalf("spec.Compile: %v", err)
	}

	// Build a payload > 24 KiB with a huge secret/body field that is NOT in payload_paths.
	bigPayloadObj := map[string]any{
		"action": "opened",
		"secret": "DO_NOT_LEAK_" + strings.Repeat("X", 26000),
		"pull_request": map[string]any{
			"title": "Fix auth race",
			"diff":  strings.Repeat("Y", 5000),
		},
	}
	bigPayloadBytes, err := json.Marshal(bigPayloadObj)
	if err != nil {
		t.Fatalf("marshal big payload: %v", err)
	}
	if len(bigPayloadBytes) <= 24576 {
		t.Fatalf("expected test payload > 24576 bytes, got %d", len(bigPayloadBytes))
	}

	selected, _, _, _, failReason := selectInputFields(compiled, Input{
		Fields: []provider.InputField{
			{Name: "event.payload", Kind: spec.FieldKindJSON, Value: string(bigPayloadBytes), Trust: spec.TrustUntrusted},
		},
	})
	if failReason != "" {
		t.Fatalf("expected narrowed payload to succeed, got failReason=%q", failReason)
	}
	if len(selected) != 1 {
		t.Fatalf("selected len = %d, want 1", len(selected))
	}
	got := selected[0].Value
	if strings.Contains(got, "DO_NOT_LEAK") || strings.Contains(got, "diff") {
		t.Fatalf("narrowed payload leaked undeclared fields: %s", got)
	}
	if !strings.Contains(got, `"action":"opened"`) || !strings.Contains(got, `"title":"Fix auth race"`) {
		t.Fatalf("narrowed payload missing declared paths: %s", got)
	}

	// Without payload_paths, the same >24 KiB payload is rejected with input_too_large.
	rawUnnarrowed := []byte(`{
		"inputs":["event.payload"],
		"spec":{
			"version":1,
			"questions":[{"id":"q1","type":"binary","instructions":"Does this PR need review?"}],
			"output":{"question":"q1"}
		}
	}`)
	compiledUnnarrowed, err := spec.Compile(rawUnnarrowed, spec.CompileOptions{
		Kind:        spec.KindFilter,
		Environment: &env,
	})
	if err != nil {
		t.Fatalf("compile unnarrowed: %v", err)
	}
	_, _, _, _, failReason = selectInputFields(compiledUnnarrowed, Input{
		Fields: []provider.InputField{
			{Name: "event.payload", Kind: spec.FieldKindJSON, Value: string(bigPayloadBytes), Trust: spec.TrustUntrusted},
		},
	})
	if failReason != provider.FailInputTooLarge {
		t.Fatalf("unnarrowed oversize payload failReason = %q, want %q", failReason, provider.FailInputTooLarge)
	}
}

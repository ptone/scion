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

package spec

import (
	"fmt"
	"strings"
	"testing"
)

func testTriggerContentEnv() ContentEnvironment {
	return ContentEnvironment{
		Name:    "trigger",
		Version: 1,
		Fields: []FieldDecl{
			{Name: "event.type", Kind: FieldKindText, MaxBytes: 128, Trust: TrustUntrusted},
			{Name: "event.source_kind", Kind: FieldKindText, MaxBytes: 32, Trust: TrustTrusted},
			{Name: "event.subject", Kind: FieldKindText, MaxBytes: 256, Trust: TrustUntrusted},
			{Name: "event.attributes", Kind: FieldKindJSON, MaxBytes: 8192, Trust: TrustUntrusted},
			{Name: "event.payload", Kind: FieldKindJSON, MaxBytes: 24576, Trust: TrustUntrusted},
		},
		MaxTotalBytes: 32 * 1024,
	}
}

func TestCompile_PayloadPaths(t *testing.T) {
	env := testTriggerContentEnv()

	makeEnvelope := func(inputs, paths string) []byte {
		pp := ""
		if paths != "" {
			pp = fmt.Sprintf(`,"payload_paths":%s`, paths)
		}
		return []byte(fmt.Sprintf(`{
			"inputs":%s%s,
			"spec":{
				"version":1,
				"questions":[{"id":"q1","type":"binary","instructions":"Is this urgent?"}],
				"output":{"question":"q1"}
			}
		}`, inputs, pp))
	}

	// Valid payload_paths (up to 8 dotted paths) with event.payload in inputs.
	c1, err := Compile(makeEnvelope(`["event.payload"]`, `["pull_request.title","action"]`), CompileOptions{
		Kind:        KindFilter,
		Environment: &env,
	})
	if err != nil {
		t.Fatalf("expected valid payload_paths to compile, got %v", err)
	}
	if len(c1.PayloadPaths) != 2 {
		t.Fatalf("PayloadPaths len = %d, want 2", len(c1.PayloadPaths))
	}

	// ContentHash is order-independent for payload_paths and differs from unnarrowed spec.
	c2, err := Compile(makeEnvelope(`["event.payload"]`, `["action","pull_request.title"]`), CompileOptions{
		Kind:        KindFilter,
		Environment: &env,
	})
	if err != nil {
		t.Fatalf("compile reordered paths: %v", err)
	}
	if c1.ContentHash != c2.ContentHash {
		t.Fatalf("ContentHash differed for reordered payload_paths: %s vs %s", c1.ContentHash, c2.ContentHash)
	}
	cNoPaths, err := Compile(makeEnvelope(`["event.payload"]`, ""), CompileOptions{
		Kind:        KindFilter,
		Environment: &env,
	})
	if err != nil {
		t.Fatalf("compile without payload_paths: %v", err)
	}
	if c1.ContentHash == cNoPaths.ContentHash {
		t.Fatalf("ContentHash should differ when payload_paths is set")
	}

	// Error cases.
	ninePaths := `["a","b","c","d","e","f","g","h","i"]`
	deepPath := `["` + strings.Repeat("a.", MaxPayloadPathSegments) + `z"]`
	cases := []struct {
		name    string
		inputs  string
		paths   string
		wantCat ErrorCategory
	}{
		{"too_many_paths", `["event.payload"]`, ninePaths, CategoryLimit},
		{"missing_event_payload_input", `["event.type"]`, `["action"]`, CategoryInput},
		{"empty_path", `["event.payload"]`, `[""]`, CategoryInput},
		{"duplicate_path", `["event.payload"]`, `["a.b","a.b"]`, CategoryInput},
		{"leading_dot", `["event.payload"]`, `[".a"]`, CategoryInput},
		{"consecutive_dots", `["event.payload"]`, `["a..b"]`, CategoryInput},
		{"control_char", `["event.payload"]`, `["a.\u0001b"]`, CategoryInput},
		{"too_deep", `["event.payload"]`, deepPath, CategoryLimit},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Compile(makeEnvelope(tc.inputs, tc.paths), CompileOptions{
				Kind:        KindFilter,
				Environment: &env,
			})
			ce, ok := AsCompileError(err)
			if !ok {
				t.Fatalf("expected CompileError, got %v", err)
			}
			if ce.Category != tc.wantCat {
				t.Fatalf("category = %q, want %q (%v)", ce.Category, tc.wantCat, ce)
			}
		})
	}
}

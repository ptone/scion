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
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNativeRequestIDHash_Golden(t *testing.T) {
	// Golden value computed independently using the Track 1 length-prefixed
	// SHA-256 formula: "rid1:" + sha256("31:com.anthropic.claude_code.events11:api_request24:req_011CP9XYZ0123456789a").
	rawID := "req_011CP9XYZ0123456789a"
	h := sha256.New()
	for _, part := range []string{ClaudeRequestIDScope, ClaudeRequestIDEvent, rawID} {
		_, _ = fmt.Fprintf(h, "%d:", len(part))
		h.Write([]byte(part))
	}
	want := "rid1:" + hex.EncodeToString(h.Sum(nil))

	if got := NativeRequestIDHash(ClaudeRequestIDScope, ClaudeRequestIDEvent, rawID); got != want {
		t.Fatalf("NativeRequestIDHash = %q, want %q", got, want)
	}
	if got := NativeRequestIDHashForHarness("claude", rawID); got != want {
		t.Fatalf("NativeRequestIDHashForHarness(claude) = %q, want %q", got, want)
	}
	if got := NativeRequestIDHash(ClaudeRequestIDScope, ClaudeRequestIDEvent, ""); got != "" {
		t.Fatalf("NativeRequestIDHash empty = %q, want empty", got)
	}
	if got := NativeRequestIDHashForHarness("claude", ""); got != "" {
		t.Fatalf("NativeRequestIDHashForHarness empty = %q, want empty", got)
	}
}

func TestHarnessTrajectorySpecFor(t *testing.T) {
	spec, ok := HarnessTrajectorySpecFor("claude")
	if !ok {
		t.Fatal("HarnessTrajectorySpecFor(claude) = false, want true")
	}
	if spec.Harness != "claude" || len(spec.Rules) != 1 {
		t.Fatalf("unexpected claude spec: %+v", spec)
	}
	if spec.Rules[0].Root != ".claude/projects" || spec.Rules[0].Suffix != ".jsonl" || spec.Rules[0].Kind != TrajectoryArtifactKindJSONLAppend {
		t.Fatalf("unexpected claude rule: %+v", spec.Rules[0])
	}
	if spec.RequestIDScope != ClaudeRequestIDScope || spec.RequestIDEvent != ClaudeRequestIDEvent {
		t.Fatalf("unexpected claude request id scope/event: %q / %q", spec.RequestIDScope, spec.RequestIDEvent)
	}
	if spec.ConverterParserVersion != "claude-1" {
		t.Fatalf("ConverterParserVersion = %q, want %q", spec.ConverterParserVersion, "claude-1")
	}
	if err := ValidateParserVersion(spec.ConverterParserVersion); err != nil {
		t.Fatalf("ConverterParserVersion %q failed validation: %v", spec.ConverterParserVersion, err)
	}

	// Mutating returned Rules slice must not mutate registry.
	spec.Rules[0].Root = "mutated"
	spec2, _ := HarnessTrajectorySpecFor("claude")
	if spec2.Rules[0].Root != ".claude/projects" {
		t.Fatalf("registry mutated via returned slice: %q", spec2.Rules[0].Root)
	}

	if _, ok := HarnessTrajectorySpecFor("unknown-harness"); ok {
		t.Fatal("expected unknown harness to return false")
	}
}

// TestRegistryHasNoConverterDependency enforces contract-v2 §B (AC8): the
// Hub-facing registry package (pkg/telemetrycontract), pkg/hub, and
// pkg/runtimebroker must never import pkg/trajectory/convert or any subpackage.
func TestRegistryHasNoConverterDependency(t *testing.T) {
	const forbiddenPrefix = "github.com/GoogleCloudPlatform/scion/pkg/trajectory/convert"

	dirs := []string{
		".",
		filepath.Join("..", "hub"),
		filepath.Join("..", "runtimebroker"),
	}
	fset := token.NewFileSet()
	for _, dir := range dirs {
		err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if d.IsDir() {
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			file, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
			if err != nil {
				return fmt.Errorf("parse %s: %w", path, err)
			}
			for _, imp := range file.Imports {
				impPath := strings.Trim(imp.Path.Value, `"`)
				if impPath == forbiddenPrefix || strings.HasPrefix(impPath, forbiddenPrefix+"/") {
					t.Errorf("%s imports forbidden converter package %q", path, impPath)
				}
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", dir, err)
		}
	}
}

func TestValidateParserVersion(t *testing.T) {
	valid := []string{"claude-1", "claude-12", "gemini-cli-2", "a-0"}
	for _, v := range valid {
		if err := ValidateParserVersion(v); err != nil {
			t.Errorf("ValidateParserVersion(%q) = %v, want nil", v, err)
		}
	}
	invalid := []string{"", "claude", "claude-v1", "Claude-1", "claude_1", "claude-1.0", "-1", "claude-"}
	for _, v := range invalid {
		if err := ValidateParserVersion(v); err == nil {
			t.Errorf("ValidateParserVersion(%q) = nil, want error", v)
		}
	}
}

func TestValidateUsageRecordLine(t *testing.T) {
	validBase := `{"type":"usage_record","session_id":"sess-1","native_request_id_raw":"req_011CP9XYZ0123456789a","dedupe_key":"claude:msg_1:req_011CP9XYZ0123456789a","event_ts":"2026-11-28T12:00:00Z","model":"claude-sonnet-4-6","model_source":"event","service_tier":"standard","status":"ok","calls":1,"input":100,"output":50,"cache_read":200,"cache_write":30,"cache_write_1h":10,"reasoning":null,"input_derivation":"reported","token_status":"reported","reported_cost_micros":1234,"reported_cost_currency":"USD","reported_cost_source":"harness","is_subagent":false,"subagent_id":""}`

	t.Run("valid full record", func(t *testing.T) {
		rec, stripped, err := ValidateUsageRecordLine([]byte(validBase), "claude")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if stripped != 0 {
			t.Fatalf("stripped = %d, want 0", stripped)
		}
		if rec.DedupeKey != "claude:msg_1:req_011CP9XYZ0123456789a" || rec.Calls != 1 {
			t.Fatalf("unexpected record: %+v", rec)
		}
		if rec.Input == nil || *rec.Input != 100 || rec.Reasoning != nil {
			t.Fatalf("unexpected tokens: input=%v reasoning=%v", rec.Input, rec.Reasoning)
		}
	})

	t.Run("strips and counts dimension fields", func(t *testing.T) {
		line := `{"type":"usage_record","hub_id":"evil-hub","project_id":"evil-proj","billing_user_id":"evil-user","agent_id":"evil-agent","run_id":"evil-run","labels":["a"],"harness_config":"evil","template":"evil","dedupe_key":"claude:msg_1:req_1","event_ts":"2026-11-28T12:00:00Z","status":"ok","calls":1,"token_status":"not_reported"}`
		_, stripped, err := ValidateUsageRecordLine([]byte(line), "claude")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if stripped != 8 {
			t.Fatalf("stripped = %d, want 8", stripped)
		}
	})

	t.Run("error cases", func(t *testing.T) {
		cases := []struct {
			name    string
			harness string
			line    string
		}{
			{"oversized line", "claude", `{"dedupe_key":"claude:x","event_ts":"2026-11-28T12:00:00Z","status":"ok","calls":1,"token_status":"reported","model":"` + strings.Repeat("a", MaxNormalizerLineBytes) + `"}`},
			{"not json", "claude", `not-json`},
			{"not object", "claude", `[1,2,3]`},
			{"trailing json", "claude", `{"dedupe_key":"claude:x","event_ts":"2026-11-28T12:00:00Z","status":"ok","calls":1,"token_status":"reported"} {}`},
			{"duplicate key", "claude", `{"dedupe_key":"claude:x","dedupe_key":"claude:y","event_ts":"2026-11-28T12:00:00Z","status":"ok","calls":1,"token_status":"reported"}`},
			{"unknown field", "claude", `{"dedupe_key":"claude:x","event_ts":"2026-11-28T12:00:00Z","status":"ok","calls":1,"token_status":"reported","foo":"bar"}`},
			{"prompt payload field", "claude", `{"dedupe_key":"claude:x","event_ts":"2026-11-28T12:00:00Z","status":"ok","calls":1,"token_status":"reported","prompt":"secret"}`},
			{"tool payload field", "claude", `{"dedupe_key":"claude:x","event_ts":"2026-11-28T12:00:00Z","status":"ok","calls":1,"token_status":"reported","tool_input":{"cmd":"ls"}}`},
			{"wrong type field", "claude", `{"type":"atif_step","dedupe_key":"claude:x","event_ts":"2026-11-28T12:00:00Z","status":"ok","calls":1,"token_status":"reported"}`},
			{"missing dedupe_key prefix", "claude", `{"dedupe_key":"codex:x","event_ts":"2026-11-28T12:00:00Z","status":"ok","calls":1,"token_status":"reported"}`},
			{"empty dedupe_key suffix", "claude", `{"dedupe_key":"claude:","event_ts":"2026-11-28T12:00:00Z","status":"ok","calls":1,"token_status":"reported"}`},
			{"missing event_ts", "claude", `{"dedupe_key":"claude:x","status":"ok","calls":1,"token_status":"reported"}`},
			{"bad event_ts", "claude", `{"dedupe_key":"claude:x","event_ts":"2026-11-28","status":"ok","calls":1,"token_status":"reported"}`},
			{"bad status", "claude", `{"dedupe_key":"claude:x","event_ts":"2026-11-28T12:00:00Z","status":"success","calls":1,"token_status":"reported"}`},
			{"calls zero", "claude", `{"dedupe_key":"claude:x","event_ts":"2026-11-28T12:00:00Z","status":"ok","calls":0,"token_status":"reported"}`},
			{"calls negative", "claude", `{"dedupe_key":"claude:x","event_ts":"2026-11-28T12:00:00Z","status":"ok","calls":-1,"token_status":"reported"}`},
			{"calls float", "claude", `{"dedupe_key":"claude:x","event_ts":"2026-11-28T12:00:00Z","status":"ok","calls":1.5,"token_status":"reported"}`},
			{"negative input", "claude", `{"dedupe_key":"claude:x","event_ts":"2026-11-28T12:00:00Z","status":"ok","calls":1,"input":-1,"token_status":"reported"}`},
			{"float output", "claude", `{"dedupe_key":"claude:x","event_ts":"2026-11-28T12:00:00Z","status":"ok","calls":1,"output":2.5,"token_status":"reported"}`},
			{"negative cache_read", "claude", `{"dedupe_key":"claude:x","event_ts":"2026-11-28T12:00:00Z","status":"ok","calls":1,"cache_read":-5,"token_status":"reported"}`},
			{"negative cost", "claude", `{"dedupe_key":"claude:x","event_ts":"2026-11-28T12:00:00Z","status":"ok","calls":1,"reported_cost_micros":-1,"token_status":"reported"}`},
			{"bad input_derivation", "claude", `{"dedupe_key":"claude:x","event_ts":"2026-11-28T12:00:00Z","status":"ok","calls":1,"input_derivation":"guessed","token_status":"reported"}`},
			{"bad token_status", "claude", `{"dedupe_key":"claude:x","event_ts":"2026-11-28T12:00:00Z","status":"ok","calls":1,"token_status":"bogus"}`},
			{"bad model_source", "claude", `{"dedupe_key":"claude:x","event_ts":"2026-11-28T12:00:00Z","status":"ok","calls":1,"model_source":"magic","token_status":"reported"}`},
			{"empty harness", "", `{"dedupe_key":"claude:x","event_ts":"2026-11-28T12:00:00Z","status":"ok","calls":1,"token_status":"reported"}`},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				if _, _, err := ValidateUsageRecordLine([]byte(tc.line), tc.harness); err == nil {
					t.Errorf("expected error for %s, got nil", tc.name)
				}
			})
		}
	})
}

func TestEnrichUsageRecord(t *testing.T) {
	inTok := int64(100)
	cacheRead := int64(250)
	cacheWrite := int64(50)
	outTok := int64(40)
	rawID := "req_011CP9XYZ0123456789a"
	captureTS := time.Date(2026, 11, 28, 12, 30, 0, 0, time.UTC)

	rec := EnrichUsageRecord(UsageRecordInput{
		SessionID:          "sess-1",
		NativeRequestIDRaw: rawID,
		DedupeKey:          "claude:msg_1:" + rawID,
		EventTS:            "2026-11-28T12:00:00Z",
		Model:              "claude-sonnet-4-6",
		Status:             UsageRecordStatusOK,
		Calls:              1,
		Input:              &inTok,
		Output:             &outTok,
		CacheRead:          &cacheRead,
		CacheWrite:         &cacheWrite,
		InputDerivation:    InputDerivationReported,
		TokenStatus:        TokenStatusReported,
	}, "claude", "claude-1", captureTS)

	if rec.SchemaVersion != 1 || rec.Harness != "claude" || rec.ParserVersion != "claude-1" {
		t.Fatalf("unexpected enriched envelope: %+v", rec)
	}
	if rec.NativeRequestID != NativeRequestIDHashForHarness("claude", rawID) {
		t.Fatalf("NativeRequestID = %q, want %q", rec.NativeRequestID, NativeRequestIDHashForHarness("claude", rawID))
	}
	if rec.ContextInputTotal == nil || *rec.ContextInputTotal != 400 {
		t.Fatalf("ContextInputTotal = %v, want 400", rec.ContextInputTotal)
	}
	if rec.CaptureTS != "2026-11-28T12:30:00Z" {
		t.Fatalf("CaptureTS = %q", rec.CaptureTS)
	}

	// Serialized JSON must never contain the raw request id field.
	b, err := json.Marshal(rec)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(b), "native_request_id_raw") {
		t.Fatalf("enriched JSON leaked native_request_id_raw: %s", b)
	}
}

func TestValidateATIFStep(t *testing.T) {
	t.Run("valid user step with scion_input_channel", func(t *testing.T) {
		raw := `{"step_id":1,"source":"user","message":"hello","extra":{"scion":{"scion_input_channel":"initial_task"}}}`
		step, err := ValidateATIFStep([]byte(raw), 0)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if step.StepID != 1 || step.Source != "user" {
			t.Fatalf("unexpected step: %+v", step)
		}
	})

	t.Run("valid agent step with metrics and extra.scion", func(t *testing.T) {
		raw := `{"step_id":2,"source":"agent","message":"hi","tool_calls":[{"id":"t1"}],"metrics":{"prompt_tokens":350,"completion_tokens":40,"cached_tokens":200,"cost_usd":0.0012,"extra":{"scion":{"input":100,"output":40,"cache_read":200,"cache_write":50,"cache_write_1h":10,"reasoning":0,"input_derivation":"reported","token_status":"reported","service_tier":"standard","dedupe_key":"claude:m1:r1","native_request_id":"rid1:abc"}}}}`
		step, err := ValidateATIFStep([]byte(raw), 1)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if step.StepID != 2 || step.Metrics == nil || *step.Metrics.PromptTokens != 350 {
			t.Fatalf("unexpected step: %+v", step)
		}
		if !strings.Contains(string(step.Raw), `"tool_calls"`) {
			t.Fatalf("Raw lost uninspected fields: %s", step.Raw)
		}
	})

	t.Run("error cases", func(t *testing.T) {
		cases := []struct {
			name       string
			prevStepID int
			raw        string
		}{
			{"missing step_id", 0, `{"source":"user","message":"hi"}`},
			{"non-increasing step_id equal", 2, `{"step_id":2,"source":"user","message":"hi"}`},
			{"non-increasing step_id smaller", 3, `{"step_id":2,"source":"user","message":"hi"}`},
			{"float step_id", 0, `{"step_id":1.5,"source":"user","message":"hi"}`},
			{"missing source", 0, `{"step_id":1,"message":"hi"}`},
			{"invalid source", 0, `{"step_id":1,"source":"tool","message":"hi"}`},
			{"missing message", 0, `{"step_id":1,"source":"user"}`},
			{"null message", 0, `{"step_id":1,"source":"user","message":null}`},
			{"negative prompt_tokens", 0, `{"step_id":1,"source":"agent","message":"hi","metrics":{"prompt_tokens":-1}}`},
			{"float completion_tokens", 0, `{"step_id":1,"source":"agent","message":"hi","metrics":{"completion_tokens":1.2}}`},
			{"negative cached_tokens", 0, `{"step_id":1,"source":"agent","message":"hi","metrics":{"cached_tokens":-3}}`},
			{"negative cost_usd", 0, `{"step_id":1,"source":"agent","message":"hi","metrics":{"cost_usd":-0.1}}`},
			{"disallowed extra.scion key", 0, `{"step_id":1,"source":"agent","message":"hi","extra":{"scion":{"hub_id":"evil"}}}`},
			{"disallowed metrics.extra.scion key", 0, `{"step_id":1,"source":"agent","message":"hi","metrics":{"extra":{"scion":{"secret":"x"}}}}`},
			{"scion_input_channel on agent step", 0, `{"step_id":1,"source":"agent","message":"hi","extra":{"scion":{"scion_input_channel":"direct_tty"}}}`},
			{"scion_input_channel on system step", 0, `{"step_id":1,"source":"system","message":"hi","extra":{"scion":{"scion_input_channel":"direct_tty"}}}`},
			{"oversized step", 0, `{"step_id":1,"source":"user","message":"` + strings.Repeat("a", MaxNormalizerLineBytes) + `"}`},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				if _, err := ValidateATIFStep([]byte(tc.raw), tc.prevStepID); err == nil {
					t.Errorf("expected error for %s, got nil", tc.name)
				}
			})
		}
	})
}

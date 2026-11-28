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
	"math"
	"sort"
	"strings"

	"github.com/GoogleCloudPlatform/scion/pkg/ruleengine"
	"github.com/GoogleCloudPlatform/scion/pkg/systemone/provider"
)

type evalCompileCtxKey struct{}

// WithEvalCompile marks ctx as an evaluation-time compilation of an already
// authored rule so Dialect.Compile skips authoring-time live instance/allowlist
// validation (ValidateCompiled) and defers live instance, allowlist, enabled,
// and admission enforcement to Runtime.Evaluate during EvaluatePending.
func WithEvalCompile(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, evalCompileCtxKey{}, true)
}

func isEvalCompile(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	v, _ := ctx.Value(evalCompileCtxKey{}).(bool)
	return v
}

// dialectTraceReasons is the closed set of System One reason tokens admitted in
// ruleengine.StepOutcome.Reason and ChainTrace.
var dialectTraceReasons = func() []string {
	reasons := []string{
		provider.UncertainAbstained,
		"abstain",
		provider.UncertainTie,
		"below_threshold",
		ReasonDialectDisabled,
	}
	for _, r := range provider.AllFailReasons() {
		reasons = append(reasons, string(r))
	}
	return reasons
}()

// dialectTraceFields declares the typed operational provenance fields emitted
// by Dialect.Evaluate on Decision.Provenance (shared-rule-chain-design v1 §3.5).
var dialectTraceFields = []ruleengine.TraceField{
	{
		Name:       "class",
		Kind:       ruleengine.TraceFieldEnum,
		EnumValues: []string{string(ClassSuccess), string(ClassUncertain), string(ClassFailed)},
	},
	{
		Name:       "reason",
		Kind:       ruleengine.TraceFieldEnum,
		EnumValues: dialectTraceReasons,
	},
	{
		Name:   "instance",
		Kind:   ruleengine.TraceFieldID,
		MaxLen: 128,
	},
	{
		Name: "instance_rev",
		Kind: ruleengine.TraceFieldIntRange,
		Min:  0,
		Max:  1 << 62,
	},
	{
		Name:   "resolved_model",
		Kind:   ruleengine.TraceFieldID,
		MaxLen: 128,
	},
	{
		Name: "answer_bool",
		Kind: ruleengine.TraceFieldBool,
	},
	{
		Name:   "answer_label",
		Kind:   ruleengine.TraceFieldID,
		MaxLen: 64,
	},
	{
		Name: "p_top_bps",
		Kind: ruleengine.TraceFieldIntRange,
		Min:  0,
		Max:  10000,
	},
	{
		Name: "truncated",
		Kind: ruleengine.TraceFieldBool,
	},
	{
		Name:   "eval_id",
		Kind:   ruleengine.TraceFieldID,
		MaxLen: 128,
	},
	{
		Name: "elapsed_ms",
		Kind: ruleengine.TraceFieldIntRange,
		Min:  0,
		Max:  1 << 62,
	},
}

// sanitizeTraceID converts a content-free identifier (such as a provider model
// name like "models/gemini-2.5-flash") into a valid ruleengine TraceFieldID
// token (^[A-Za-z0-9][A-Za-z0-9._:-]*$, max 128 bytes).
func sanitizeTraceID(s string, maxLen int) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	s = strings.ReplaceAll(s, "/", ":")
	var b strings.Builder
	for i := 0; i < len(s) && b.Len() < maxLen; i++ {
		c := s[i]
		isAlnum := (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
		if b.Len() == 0 {
			if !isAlnum {
				continue
			}
			b.WriteByte(c)
			continue
		}
		if isAlnum || c == '.' || c == '_' || c == ':' || c == '-' {
			b.WriteByte(c)
		}
	}
	return b.String()
}

// ProjectTraceProvenance builds the typed, declared-field ruleengine
// TraceProvenance for eval (shared-rule-chain-design v1 §3.5).
func ProjectTraceProvenance(eval *Evaluation) *ruleengine.TraceProvenance {
	if eval == nil {
		return nil
	}
	fields := make(map[string]any, 10)
	if eval.Class != "" {
		fields["class"] = string(eval.Class)
	}
	if eval.Reason != "" {
		fields["reason"] = eval.Reason
	}
	if len(eval.Attempts) > 0 {
		last := eval.Attempts[len(eval.Attempts)-1]
		if inst := sanitizeTraceID(last.Instance, 128); inst != "" {
			fields["instance"] = inst
		}
		if last.InstanceRevision >= 0 {
			fields["instance_rev"] = last.InstanceRevision
		}
	}
	if model := sanitizeTraceID(eval.ResolvedModel, 128); model != "" {
		fields["resolved_model"] = model
	}
	if ans, ok := primaryAnswer(eval); ok {
		if ans.Bool != nil {
			fields["answer_bool"] = *ans.Bool
		} else if ans.Choice != nil {
			if lbl := sanitizeTraceID(*ans.Choice, 64); lbl != "" {
				fields["answer_label"] = lbl
			}
		}
		if pTop, hasP := topProbability(ans.Distribution); hasP {
			bps := int64(math.Round(pTop * 10000))
			if bps < 0 {
				bps = 0
			} else if bps > 10000 {
				bps = 10000
			}
			fields["p_top_bps"] = bps
		}
	}
	if len(truncatedFieldNames(eval)) > 0 {
		fields["truncated"] = true
	}
	if id := sanitizeTraceID(eval.EvalID, 128); id != "" {
		fields["eval_id"] = id
	}
	fields["elapsed_ms"] = max(eval.Elapsed.Milliseconds(), int64(0))
	return &ruleengine.TraceProvenance{Fields: fields}
}

// CompactRuleTraceProvenance builds the compact, content-free System One
// provenance projection for trigger_fires.rule_trace (t2-p2-trigger-binding-amendment §2.7).
//
// Shape:
//
//	{"class":"success|uncertain|failed","reason":"…","instance":"name","instance_rev":3,
//	 "resolved_model":"…","answer":true|"<label>","p_top":0.91,"truncated":["event.payload"],
//	 "eval_id":"…","elapsed_ms":120}
func CompactRuleTraceProvenance(eval *Evaluation) map[string]any {
	if eval == nil {
		return nil
	}
	out := make(map[string]any, 10)
	if eval.Class != "" {
		out["class"] = string(eval.Class)
	}
	if eval.Reason != "" {
		out["reason"] = eval.Reason
	}
	if len(eval.Attempts) > 0 {
		last := eval.Attempts[len(eval.Attempts)-1]
		if last.Instance != "" {
			out["instance"] = last.Instance
		}
		if last.InstanceRevision > 0 {
			out["instance_rev"] = last.InstanceRevision
		}
	}
	if model := eval.EffectiveResolvedModel(); model != "" {
		out["resolved_model"] = model
	} else if eval.ResolvedModel != "" {
		out["resolved_model"] = eval.ResolvedModel
	}
	if ans, ok := primaryAnswer(eval); ok {
		if ans.Bool != nil {
			out["answer"] = *ans.Bool
		} else if ans.Choice != nil && *ans.Choice != "" {
			// Choice label ID only (validated ^[a-z0-9][a-z0-9_-]{0,63}$), never free text.
			out["answer"] = *ans.Choice
		}
		if pTop, hasP := topProbability(ans.Distribution); hasP {
			out["p_top"] = math.Round(pTop*10000) / 10000
		}
	}
	if trunc := truncatedFieldNames(eval); len(trunc) > 0 {
		out["truncated"] = trunc
	}
	if eval.EvalID != "" {
		out["eval_id"] = eval.EvalID
	}
	out["elapsed_ms"] = max(eval.Elapsed.Milliseconds(), int64(0))
	return out
}

func primaryAnswer(eval *Evaluation) (AnswerRecord, bool) {
	if eval == nil || len(eval.Answers) == 0 {
		return AnswerRecord{}, false
	}
	if len(eval.Answers) == 1 {
		for _, a := range eval.Answers {
			return a, true
		}
	}
	keys := make([]string, 0, len(eval.Answers))
	for k := range eval.Answers {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return eval.Answers[keys[0]], true
}

func topProbability(dist map[string]float64) (float64, bool) {
	if len(dist) == 0 {
		return 0, false
	}
	var best float64
	first := true
	for _, p := range dist {
		if math.IsNaN(p) || math.IsInf(p, 0) {
			continue
		}
		if first || p > best {
			best = p
			first = false
		}
	}
	if first {
		return 0, false
	}
	return best, true
}

func truncatedFieldNames(eval *Evaluation) []string {
	if eval == nil || len(eval.Input.Fields) == 0 {
		return nil
	}
	var out []string
	for _, f := range eval.Input.Fields {
		if f.Truncated && f.Name != "" {
			out = append(out, f.Name)
		}
	}
	return out
}

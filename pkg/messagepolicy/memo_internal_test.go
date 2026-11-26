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

package messagepolicy

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/GoogleCloudPlatform/scion/pkg/systemone"
	"github.com/GoogleCloudPlatform/scion/pkg/systemone/provider"
)

// TestFreezeForShadow_DeepCopyAndExcludesUnreusable (Part 2 O2) directly
// verifies that FreezeForShadow:
//   - deep-copies published entries (kills pointer-sharing mutants);
//   - excludes deadline-bound (timeout/canceled/capacity) and zero-attempt
//     entries even if present in the source map.
func TestFreezeForShadow_DeepCopyAndExcludesUnreusable(t *testing.T) {
	m := NewJudgmentMemo("send-o2", nil)
	okKey := "k-ok" + memoKeyModePrefix + ModeEnforce + "\x00"
	timeoutKey := "k-timeout" + memoKeyModePrefix + ModeEnforce + "\x00"
	zeroAttemptKey := "k-zero" + memoKeyModePrefix + ModeEnforce + "\x00"

	flagTrue := true
	m.published[okKey] = &memoEntry{
		eval: &systemone.Evaluation{
			EvalID:   "eval-1",
			Class:    systemone.ClassSuccess,
			Answers:  map[string]systemone.AnswerRecord{"flagged": {QuestionID: "flagged", Bool: &flagTrue}},
			Attempts: []systemone.Attempt{{Instance: "inst-a", Requests: 1}},
		},
		cls: Classification{
			Class:     ClassPass,
			Judgments: Judgments{Flags: map[string]bool{"flagged": true}},
		},
		attempt: systemone.Attempt{Instance: "inst-a", Requests: 1},
	}
	m.published[timeoutKey] = &memoEntry{
		eval: &systemone.Evaluation{
			EvalID:   "eval-timeout",
			Class:    systemone.ClassFailed,
			Reason:   string(provider.FailTimeout),
			Attempts: []systemone.Attempt{{Instance: "inst-a", Requests: 1}},
		},
		cls:     Classification{Class: ClassFailed, Reason: string(provider.FailTimeout)},
		attempt: systemone.Attempt{Instance: "inst-a", Requests: 1},
	}
	m.published[zeroAttemptKey] = &memoEntry{
		eval: &systemone.Evaluation{
			EvalID:   "eval-zero",
			Class:    systemone.ClassFailed,
			Reason:   string(provider.FailNoEligibleInstance),
			Attempts: nil,
		},
		cls: Classification{Class: ClassFailed, Reason: string(provider.FailNoEligibleInstance)},
	}

	snap := m.FreezeForShadow(nil)

	shadowOKKey := remapKeyMode(okKey, ModeEnforce, ModeShadow)
	shadowTimeoutKey := remapKeyMode(timeoutKey, ModeEnforce, ModeShadow)
	shadowZeroKey := remapKeyMode(zeroAttemptKey, ModeEnforce, ModeShadow)

	require.Contains(t, snap.published, shadowOKKey)
	assert.NotContains(t, snap.published, shadowTimeoutKey, "deadline-bound timeout entry must be excluded from shadow snapshot")
	assert.NotContains(t, snap.published, shadowZeroKey, "zero-attempt entry must be excluded from shadow snapshot")

	// Mutate the source entry in place after FreezeForShadow; if FreezeForShadow
	// shared pointers instead of deep-copying, these assertions fail immediately.
	m.published[okKey].eval.EvalID = "MUTATED"
	*m.published[okKey].eval.Answers["flagged"].Bool = false
	m.published[okKey].cls.Judgments.Flags["flagged"] = false

	got := snap.published[shadowOKKey]
	assert.Equal(t, "eval-1", got.eval.EvalID, "shadow snapshot must deep-copy Evaluation")
	assert.True(t, *got.eval.Answers["flagged"].Bool, "shadow snapshot must deep-copy AnswerRecord pointers")
	assert.True(t, got.cls.Judgments.Flags["flagged"], "shadow snapshot must deep-copy Judgments maps")
}

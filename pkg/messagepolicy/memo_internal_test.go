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
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/GoogleCloudPlatform/scion/pkg/systemone"
	"github.com/GoogleCloudPlatform/scion/pkg/systemone/provider"
	"github.com/GoogleCloudPlatform/scion/pkg/systemone/spec"
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

// TestWaiterContextClassification_M4M5M7 deterministically covers:
//   - M4: classifyWaiterContext returns timeout when DeadlineExceeded occurs
//     at or after ownDeadline, and canceled when before ownDeadline or on Canceled;
//   - M5: waitForOwnDeadlineOrCancel waits and returns canceled when caller
//     deadline expires before ownDeadline, and timeout when ownDeadline fires first;
//   - M7: loop-top ctx check classifies DeadlineExceeded via classifyWaiterContext
//     (canceled when ownDeadline is still in the future, not timeout).
func TestWaiterContextClassification_M4M5M7(t *testing.T) {
	t.Run("M4_classifyWaiterContext_branches", func(t *testing.T) {
		pastOwn := time.Now().Add(-10 * time.Millisecond)
		futureOwn := time.Now().Add(5 * time.Second)

		clsAfter := classifyWaiterContext(context.DeadlineExceeded, pastOwn)
		assert.Equal(t, ClassFailed, clsAfter.Class)
		assert.Equal(t, string(provider.FailTimeout), clsAfter.Reason, "deadline after ownDeadline must classify as timeout")

		clsBefore := classifyWaiterContext(context.DeadlineExceeded, futureOwn)
		assert.Equal(t, ClassFailed, clsBefore.Class)
		assert.Equal(t, string(provider.FailCanceled), clsBefore.Reason, "deadline before ownDeadline must classify as canceled")

		clsCancel := classifyWaiterContext(context.Canceled, pastOwn)
		assert.Equal(t, ClassFailed, clsCancel.Class)
		assert.Equal(t, string(provider.FailCanceled), clsCancel.Reason, "explicit cancel must classify as canceled")
	})

	t.Run("M5_waitForOwnDeadlineOrCancel_caller_before_ownDeadline", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		defer cancel()
		cls := waitForOwnDeadlineOrCancel(ctx, time.Now().Add(5*time.Second))
		assert.Equal(t, ClassFailed, cls.Class)
		assert.Equal(t, string(provider.FailCanceled), cls.Reason, "caller deadline before ownDeadline must return canceled")
	})

	t.Run("M5_waitForOwnDeadlineOrCancel_ownDeadline_before_caller", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		cls := waitForOwnDeadlineOrCancel(ctx, time.Now().Add(15*time.Millisecond))
		assert.Equal(t, ClassFailed, cls.Class)
		assert.Equal(t, string(provider.FailTimeout), cls.Reason, "ownDeadline before caller deadline must return timeout")
	})

	t.Run("M7_loop_top_check_uses_classifyWaiterContext", func(t *testing.T) {
		// Pre-populate a completed non-timeout unpublishable flight in memo.inflight
		// and drive evaluateSystemOneWithMemo with a context whose Err() transitions
		// to context.DeadlineExceeded on the loop-top check after the waiter wakes up.
		const rawRule = `{"dialect":"system_one","inputs":["message.body"],"spec":{"version":1,"binding":{"instance":"inst-a"},"questions":[{"id":"flagged","type":"binary","instructions":"ok?"}]}}`
		contentEnv := SystemOneContentEnv()
		compiled, err := spec.Compile([]byte(rawRule), spec.CompileOptions{
			Kind:        spec.Kind(KindJudgments),
			Environment: &contentEnv,
		})
		require.NoError(t, err)

		memo := NewJudgmentMemo("send-m7", nil)
		req := systemone.Request{
			ProjectID: "proj-1",
			RuleID:    "pol",
			Input: systemone.Input{
				Fields: []provider.InputField{{Name: FieldMessageBody, Kind: spec.FieldKindText, Trust: spec.TrustUntrusted, Value: "m7"}},
			},
		}
		cfg := evalConfig{
			maxEvalTime: time.Second,
			memo:        memo,
			memoMode:    ModeEnforce,
		}
		key := buildMemoKey(memo.sendID, compiled, req.ProjectID, req.Input, cfg.memoMode, cfg.maxEvalTime)
		done := make(chan struct{})
		close(done)
		memo.inflight[key] = &memoFlight{
			done:      done,
			published: false,
			reason:    string(provider.FailCapacity),
		}

		// Err() returns nil on entry check (call 1) and first loop-top check (call 2),
		// then context.DeadlineExceeded on the second loop-top check (call 3, after continue).
		ctx := &stepExpireContext{failAtCall: 3}
		ev := &Evaluator{}
		_, cls := ev.evaluateSystemOneWithMemo(ctx, rawRule, req, cfg)
		assert.Equal(t, ClassFailed, cls.Class)
		assert.Equal(t, string(provider.FailCanceled), cls.Reason, "loop-top DeadlineExceeded before ownDeadline must classify as canceled, not timeout")
	})
}

type stepExpireContext struct {
	context.Context
	calls      atomic.Int32
	failAtCall int32
}

func (c *stepExpireContext) Deadline() (time.Time, bool) { return time.Time{}, false }
func (c *stepExpireContext) Done() <-chan struct{}       { return nil }
func (c *stepExpireContext) Value(any) any               { return nil }
func (c *stepExpireContext) Err() error {
	if c.calls.Add(1) >= c.failAtCall {
		return context.DeadlineExceeded
	}
	return nil
}

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

package messagepolicy_test

import (
	"context"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/GoogleCloudPlatform/scion/pkg/messagepolicy"
	"github.com/GoogleCloudPlatform/scion/pkg/ruleengine"
	"github.com/GoogleCloudPlatform/scion/pkg/systemone"
	"github.com/GoogleCloudPlatform/scion/pkg/systemone/provider"
	systemoneruntime "github.com/GoogleCloudPlatform/scion/pkg/systemone/runtime"
	"github.com/GoogleCloudPlatform/scion/pkg/systemone/spec"
)

type mutableInstanceStore struct {
	mu        sync.RWMutex
	instances map[string]systemoneruntime.Instance
}

func newMutableInstanceStore(insts ...systemoneruntime.Instance) *mutableInstanceStore {
	m := make(map[string]systemoneruntime.Instance, len(insts))
	for _, inst := range insts {
		m[inst.Name] = inst
	}
	return &mutableInstanceStore{instances: m}
}

func (s *mutableInstanceStore) set(inst systemoneruntime.Instance) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.instances[inst.Name] = inst
}

func (s *mutableInstanceStore) GetInstance(_ context.Context, name string) (systemoneruntime.Instance, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	inst, ok := s.instances[name]
	return inst, ok, nil
}

func (s *mutableInstanceStore) ListInstances(_ context.Context) ([]systemoneruntime.Instance, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]systemoneruntime.Instance, 0, len(s.instances))
	for _, inst := range s.instances {
		out = append(out, inst)
	}
	slices.SortFunc(out, func(a, b systemoneruntime.Instance) int {
		if a.Name < b.Name {
			return -1
		}
		if a.Name > b.Name {
			return 1
		}
		return 0
	})
	return out, nil
}

type mutableAllowlist struct {
	mu      sync.RWMutex
	allowed map[string][]string
}

func newMutableAllowlist(projectID string, names ...string) *mutableAllowlist {
	return &mutableAllowlist{
		allowed: map[string][]string{projectID: slices.Clone(names)},
	}
}

func (a *mutableAllowlist) set(projectID string, names ...string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.allowed[projectID] = slices.Clone(names)
}

func (a *mutableAllowlist) AllowedInstances(_ context.Context, projectID string) ([]string, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return slices.Clone(a.allowed[projectID]), nil
}

type memoTestHarness struct {
	ev        *messagepolicy.Evaluator
	rt        *systemoneruntime.Runtime
	instances *mutableInstanceStore
	allowlist *mutableAllowlist
	calls     atomic.Int32
}

func okBinaryResult(model string) provider.ModelResult {
	dist, val, _, _ := provider.BinaryDistribution(0.94)
	return provider.ModelResult{
		ResolvedModel: model,
		HTTPStatus:    200,
		Answers: map[string]provider.Answer{
			"flagged": {
				QuestionID:   "flagged",
				Primitive:    spec.PrimitiveBinary,
				Status:       provider.AnswerStatusAnswered,
				Bool:         val,
				Distribution: dist,
			},
		},
		Usage: provider.Usage{InputTokens: 15, OutputTokens: 1},
	}
}

func newMemoTestHarness(
	t *testing.T,
	insts []systemoneruntime.Instance,
	allowlist *mutableAllowlist,
	invokeFn func(ctx context.Context, tr provider.Transport, inst provider.InstanceView, req provider.ModelRequest) (provider.ModelResult, error),
) *memoTestHarness {
	t.Helper()
	h := &memoTestHarness{
		instances: newMutableInstanceStore(insts...),
		allowlist: allowlist,
	}
	reg := provider.NewRegistry()
	wrappedInvoke := func(ctx context.Context, tr provider.Transport, inst provider.InstanceView, req provider.ModelRequest) (provider.ModelResult, error) {
		h.calls.Add(1)
		if invokeFn != nil {
			return invokeFn(ctx, tr, inst, req)
		}
		return okBinaryResult(inst.Model), nil
	}
	reg.MustRegister(&mpFakeAdapter{name: "fake-mp", invokeFn: wrappedInvoke})
	reg.MustRegister(&mpFakeAdapter{name: "fake-mp-2", invokeFn: wrappedInvoke})

	rt := systemoneruntime.New(systemoneruntime.Config{
		Registry:         reg,
		Instances:        h.instances,
		ProjectAllowlist: allowlist,
		Credentials:      systemoneruntime.StaticCredential("sk-memo-test"),
		AdmissionWait:    40 * time.Millisecond,
	})
	t.Cleanup(rt.CloseIdleConnections)
	h.rt = rt

	eng := ruleengine.NewEngine()
	celDialect, err := ruleengine.NewCELDialect()
	require.NoError(t, err)
	celEng, err := messagepolicy.NewCELEngine()
	require.NoError(t, err)
	require.NoError(t, celDialect.RegisterEnvironment(celEng))
	require.NoError(t, eng.Register(celDialect))
	require.NoError(t, eng.Register(systemone.NewDialect(systemone.DialectConfig{
		Runtime: rt,
		Enabled: func() bool { return true },
	})))
	require.NoError(t, eng.AllowConsumerDialects(
		messagepolicy.ConsumerName,
		ruleengine.Grant(ruleengine.DialectCEL),
		ruleengine.GrantIO(systemone.DialectName),
	))
	require.NoError(t, systemone.RegisterConsumerEnvironment(eng, messagepolicy.ConsumerName, messagepolicy.SystemOneContentEnv()))

	ev, err := messagepolicy.New(eng)
	require.NoError(t, err)
	h.ev = ev
	return h
}

func (h *memoTestHarness) resolver() messagepolicy.BindingResolver {
	return func(ctx context.Context, compiled *spec.Compiled, projectID string) (systemone.ResolvedBinding, bool) {
		return h.rt.ResolveBindingForReuse(ctx, compiled, projectID)
	}
}

func defaultMemoInstance(name string, rev int64) systemoneruntime.Instance {
	return systemoneruntime.Instance{
		ID:       "id-" + name,
		Name:     name,
		Adapter:  "fake-mp",
		Model:    "fake-model-1",
		Endpoint: "https://fake.example.test",
		Auth:     systemoneruntime.InstanceAuth{Kind: "api_key", SecretName: "sec-" + name},
		Enabled:  true,
		Revision: rev,
	}
}

const memoRuleBodyOnly = `{"dialect":"system_one","inputs":["message.body"],"spec":{"version":1,"binding":{"instance":"inst-a"},"questions":[{"id":"flagged","type":"binary","instructions":"Is this flagged?"}]}}`

// A1: TestMemo_SameRuleTwoPoliciesOneCall
// Kills: no memo; caching the EvaluatePolicy result.
func TestMemo_SameRuleTwoPoliciesOneCall(t *testing.T) {
	allow := newMutableAllowlist("proj-1", "inst-a")
	h := newMemoTestHarness(t, []systemoneruntime.Instance{defaultMemoInstance("inst-a", 1)}, allow, nil)

	memo := messagepolicy.NewJudgmentMemo("send-a1", h.resolver())
	facts := messagepolicy.Facts{
		Surface:   messagepolicy.SurfaceAgentToUser,
		Sender:    messagepolicy.SenderFacts{Kind: "agent", ID: "ag-1", Slug: "bot", ProjectID: "proj-1"},
		Recipient: messagepolicy.RecipientFacts{Kind: "user", ID: "u-1", Slug: "alice"},
		Message:   messagepolicy.MessageFacts{Type: "input-needed", Body: "hello world"},
	}
	soOpts := []messagepolicy.EvalOption{
		messagepolicy.WithMaxEvalTime(1500 * time.Millisecond),
		messagepolicy.WithJudgmentMemo(memo),
		messagepolicy.WithMemoMode(messagepolicy.ModeEnforce),
	}

	// Policy 1 violates when judgment_flags.flagged == true.
	cls1 := h.ev.EvaluatePolicy(context.Background(), "pol-1", 1, `judgment_flags.flagged`, memoRuleBodyOnly, "proj-1", facts, nil, soOpts)
	// Policy 2 has the exact same System One rule, different policyID/revision, and inverted CEL (passes).
	cls2 := h.ev.EvaluatePolicy(context.Background(), "pol-2", 7, `!judgment_flags.flagged`, memoRuleBodyOnly, "proj-1", facts, nil, soOpts)

	assert.Equal(t, int32(1), h.calls.Load(), "two policies with identical System One rule must make 1 provider call")
	assert.Equal(t, messagepolicy.ClassViolation, cls1.Class)
	assert.Equal(t, messagepolicy.ClassPass, cls2.Class, "CEL must run independently per policy (never cache EvaluatePolicy)")

	require.NotNil(t, cls1.Evaluation)
	require.NotNil(t, cls2.Evaluation)
	assert.False(t, cls1.Evaluation.CacheHit, "leader must have CacheHit=false")
	assert.Empty(t, cls1.Evaluation.SourceEvalID)
	require.NotNil(t, cls1.Evaluation.Usage, "leader must keep Usage")

	assert.True(t, cls2.Evaluation.CacheHit, "waiter must have CacheHit=true")
	assert.Equal(t, cls1.Evaluation.EvalID, cls2.Evaluation.SourceEvalID, "waiter must record leader's eval_id as source_eval_id")
	assert.NotEqual(t, cls1.Evaluation.EvalID, cls2.Evaluation.EvalID, "waiter must get its own eval_id")
	assert.Nil(t, cls2.Evaluation.Usage, "waiter must not duplicate Usage")
	assert.Equal(t, "pol-2", cls2.Evaluation.Rule.RuleID, "waiter must record its own policy ID")
	assert.Equal(t, int64(7), cls2.Evaluation.Rule.Revision, "waiter must record its own policy revision")
}

// A2: TestMemo_KeyComponents
// Kills: a missing or extra key part.
func TestMemo_KeyComponents(t *testing.T) {
	allow := newMutableAllowlist("proj-1", "inst-a", "inst-b")
	allow.set("proj-2", "inst-a")
	h := newMemoTestHarness(t, []systemoneruntime.Instance{
		defaultMemoInstance("inst-a", 1),
		defaultMemoInstance("inst-b", 1),
	}, allow, nil)

	memo := messagepolicy.NewJudgmentMemo("send-a2", h.resolver())

	baseReq := systemone.Request{
		ProjectID: "proj-1",
		RuleID:    "pol-1",
		Revision:  1,
		Input: systemone.Input{
			Fields: []provider.InputField{
				{Name: messagepolicy.FieldMessageBody, Kind: spec.FieldKindText, Trust: spec.TrustUntrusted, Value: "body-1"},
				{Name: messagepolicy.FieldMessageType, Kind: spec.FieldKindText, Trust: spec.TrustUntrusted, Value: "type-1"},
			},
			Params: map[string]string{"k": "v1"},
		},
	}
	baseOpts := []messagepolicy.EvalOption{
		messagepolicy.WithMaxEvalTime(1500 * time.Millisecond),
		messagepolicy.WithJudgmentMemo(memo),
		messagepolicy.WithMemoMode(messagepolicy.ModeEnforce),
	}

	// 1. Initial call (calls = 1).
	_, cls := h.ev.EvaluateSystemOne(context.Background(), memoRuleBodyOnly, baseReq, baseOpts...)
	require.Equal(t, messagepolicy.ClassPass, cls.Class)
	require.Equal(t, int32(1), h.calls.Load())

	// 2. Unselected field change (message.type) + different policyID/revision -> MUST HIT (calls still 1).
	sameKeyReq := baseReq
	sameKeyReq.RuleID = "pol-999"
	sameKeyReq.Revision = 42
	sameKeyReq.Input.Fields = []provider.InputField{
		{Name: messagepolicy.FieldMessageBody, Kind: spec.FieldKindText, Trust: spec.TrustUntrusted, Value: "body-1"},
		{Name: messagepolicy.FieldMessageType, Kind: spec.FieldKindText, Trust: spec.TrustUntrusted, Value: "type-CHANGED"},
	}
	evalHit, clsHit := h.ev.EvaluateSystemOne(context.Background(), memoRuleBodyOnly, sameKeyReq, baseOpts...)
	require.Equal(t, messagepolicy.ClassPass, clsHit.Class)
	assert.True(t, evalHit.CacheHit)
	assert.Equal(t, int32(1), h.calls.Load(), "unselected field or policyID/revision must not bust memo")

	// 3. Selected field change (message.body) -> NEW CALL (calls = 2).
	diffBody := baseReq
	diffBody.Input.Fields = []provider.InputField{
		{Name: messagepolicy.FieldMessageBody, Kind: spec.FieldKindText, Trust: spec.TrustUntrusted, Value: "body-2"},
	}
	_, _ = h.ev.EvaluateSystemOne(context.Background(), memoRuleBodyOnly, diffBody, baseOpts...)
	assert.Equal(t, int32(2), h.calls.Load(), "selected field change must force new call")

	// 4. Project ID change -> NEW CALL (calls = 3).
	diffProj := baseReq
	diffProj.ProjectID = "proj-2"
	_, _ = h.ev.EvaluateSystemOne(context.Background(), memoRuleBodyOnly, diffProj, baseOpts...)
	assert.Equal(t, int32(3), h.calls.Load(), "project ID change must force new call")

	// 5. Rule content change (instructions) -> NEW CALL (calls = 4).
	diffRule := `{"dialect":"system_one","inputs":["message.body"],"spec":{"version":1,"binding":{"instance":"inst-a"},"questions":[{"id":"flagged","type":"binary","instructions":"Different question?"}]}}`
	_, _ = h.ev.EvaluateSystemOne(context.Background(), diffRule, baseReq, baseOpts...)
	assert.Equal(t, int32(4), h.calls.Load(), "rule content change must force new call")

	// 6. Rule timeout change -> NEW CALL (calls = 5).
	diffTimeoutRule := `{"dialect":"system_one","timeout":"1s","inputs":["message.body"],"spec":{"version":1,"binding":{"instance":"inst-a"},"questions":[{"id":"flagged","type":"binary","instructions":"Is this flagged?"}]}}`
	_, _ = h.ev.EvaluateSystemOne(context.Background(), diffTimeoutRule, baseReq, baseOpts...)
	assert.Equal(t, int32(5), h.calls.Load(), "rule timeout change must force new call")

	// 7. Binding instance change -> NEW CALL (calls = 6).
	diffBindingRule := `{"dialect":"system_one","inputs":["message.body"],"spec":{"version":1,"binding":{"instance":"inst-b"},"questions":[{"id":"flagged","type":"binary","instructions":"Is this flagged?"}]}}`
	_, _ = h.ev.EvaluateSystemOne(context.Background(), diffBindingRule, baseReq, baseOpts...)
	assert.Equal(t, int32(6), h.calls.Load(), "binding instance change must force new call")

	// 8. Params change -> NEW CALL (calls = 7).
	diffParams := baseReq
	diffParams.Input.Params = map[string]string{"k": "v2"}
	_, _ = h.ev.EvaluateSystemOne(context.Background(), memoRuleBodyOnly, diffParams, baseOpts...)
	assert.Equal(t, int32(7), h.calls.Load(), "params change must force new call")

	// 9. Mode change (shadow vs enforce in same live memo) -> NEW CALL (calls = 8).
	shadowOpts := []messagepolicy.EvalOption{
		messagepolicy.WithMaxEvalTime(1500 * time.Millisecond),
		messagepolicy.WithJudgmentMemo(memo),
		messagepolicy.WithMemoMode(messagepolicy.ModeShadow),
	}
	_, _ = h.ev.EvaluateSystemOne(context.Background(), memoRuleBodyOnly, baseReq, shadowOpts...)
	assert.Equal(t, int32(8), h.calls.Load(), "mode change must force new call")

	// 10. MaxEvalTime change -> NEW CALL (calls = 9).
	diffMaxEvalOpts := []messagepolicy.EvalOption{
		messagepolicy.WithMaxEvalTime(900 * time.Millisecond),
		messagepolicy.WithJudgmentMemo(memo),
		messagepolicy.WithMemoMode(messagepolicy.ModeEnforce),
	}
	_, _ = h.ev.EvaluateSystemOne(context.Background(), memoRuleBodyOnly, baseReq, diffMaxEvalOpts...)
	assert.Equal(t, int32(9), h.calls.Load(), "MaxEvalTime change must force new call")

	// 11. Send ID change -> NEW CALL (calls = 10).
	memo2 := messagepolicy.NewJudgmentMemo("send-a2-other", h.resolver())
	diffSendOpts := []messagepolicy.EvalOption{
		messagepolicy.WithMaxEvalTime(1500 * time.Millisecond),
		messagepolicy.WithJudgmentMemo(memo2),
		messagepolicy.WithMemoMode(messagepolicy.ModeEnforce),
	}
	_, _ = h.ev.EvaluateSystemOne(context.Background(), memoRuleBodyOnly, baseReq, diffSendOpts...)
	assert.Equal(t, int32(10), h.calls.Load(), "send ID change must force new call")
}

// A3: TestMemo_ZeroAttemptNotPublished
// Kills: publishing zero-attempt results; indexing Attempts[0] on empty Attempts.
func TestMemo_ZeroAttemptNotPublished(t *testing.T) {
	t.Run("allowlist_refusal_not_published", func(t *testing.T) {
		allow := newMutableAllowlist("proj-1") // inst-a NOT allowed initially
		h := newMemoTestHarness(t, []systemoneruntime.Instance{defaultMemoInstance("inst-a", 1)}, allow, nil)
		memo := messagepolicy.NewJudgmentMemo("send-a3-allow", h.resolver())
		opts := []messagepolicy.EvalOption{
			messagepolicy.WithMaxEvalTime(1500 * time.Millisecond),
			messagepolicy.WithJudgmentMemo(memo),
			messagepolicy.WithMemoMode(messagepolicy.ModeEnforce),
		}
		req := systemone.Request{
			ProjectID: "proj-1",
			RuleID:    "pol-1",
			Revision:  1,
			Input: systemone.Input{
				Fields: []provider.InputField{{Name: messagepolicy.FieldMessageBody, Kind: spec.FieldKindText, Trust: spec.TrustUntrusted, Value: "hi"}},
			},
		}

		// First call fails with no_eligible_instance (0 attempts, no panic).
		eval1, cls1 := h.ev.EvaluateSystemOne(context.Background(), memoRuleBodyOnly, req, opts...)
		assert.Equal(t, messagepolicy.ClassFailed, cls1.Class)
		assert.Equal(t, string(provider.FailNoEligibleInstance), cls1.Reason)
		if eval1 != nil {
			assert.Empty(t, eval1.Attempts)
		}
		assert.Zero(t, h.calls.Load())

		// Restore allowlist -> second call MUST NOT reuse the refusal; it calls provider and succeeds.
		allow.set("proj-1", "inst-a")
		eval2, cls2 := h.ev.EvaluateSystemOne(context.Background(), memoRuleBodyOnly, req, opts...)
		assert.Equal(t, messagepolicy.ClassPass, cls2.Class)
		require.NotNil(t, eval2)
		assert.False(t, eval2.CacheHit)
		assert.Equal(t, int32(1), h.calls.Load())
	})

	t.Run("capacity_refusal_not_published", func(t *testing.T) {
		allow := newMutableAllowlist("proj-1", "inst-a")
		inst := defaultMemoInstance("inst-a", 1)
		inst.Limits.MaxInFlight = 1

		blockerStarted := make(chan struct{})
		releaseBlocker := make(chan struct{})
		var first atomic.Bool
		h := newMemoTestHarness(t, []systemoneruntime.Instance{inst}, allow, func(ctx context.Context, _ provider.Transport, iv provider.InstanceView, _ provider.ModelRequest) (provider.ModelResult, error) {
			if first.CompareAndSwap(false, true) {
				close(blockerStarted)
				select {
				case <-releaseBlocker:
				case <-ctx.Done():
					return provider.ModelResult{}, ctx.Err()
				}
			}
			return okBinaryResult(iv.Model), nil
		})

		// Occupy the single permit using a different body.
		blockerDone := make(chan struct{})
		go func() {
			defer close(blockerDone)
			_, _ = h.ev.EvaluateSystemOne(context.Background(), memoRuleBodyOnly, systemone.Request{
				ProjectID: "proj-1",
				Input:     systemone.Input{Fields: []provider.InputField{{Name: messagepolicy.FieldMessageBody, Kind: spec.FieldKindText, Trust: spec.TrustUntrusted, Value: "blocker"}}},
			})
		}()
		<-blockerStarted

		memo := messagepolicy.NewJudgmentMemo("send-a3-cap", h.resolver())
		opts := []messagepolicy.EvalOption{
			messagepolicy.WithMaxEvalTime(1500 * time.Millisecond),
			messagepolicy.WithJudgmentMemo(memo),
			messagepolicy.WithMemoMode(messagepolicy.ModeEnforce),
		}
		req := systemone.Request{
			ProjectID: "proj-1",
			RuleID:    "pol-1",
			Revision:  1,
			Input:     systemone.Input{Fields: []provider.InputField{{Name: messagepolicy.FieldMessageBody, Kind: spec.FieldKindText, Trust: spec.TrustUntrusted, Value: "target"}}},
		}

		// While permit is occupied, call hits FailCapacity (0 requests).
		evalCap, clsCap := h.ev.EvaluateSystemOne(context.Background(), memoRuleBodyOnly, req, opts...)
		assert.Equal(t, messagepolicy.ClassFailed, clsCap.Class)
		assert.Equal(t, string(provider.FailCapacity), clsCap.Reason)
		require.NotNil(t, evalCap)
		require.Len(t, evalCap.Attempts, 1)
		assert.Equal(t, 0, evalCap.Attempts[0].Requests)

		// Release blocker -> second call on same memo key MUST NOT reuse capacity refusal.
		close(releaseBlocker)
		<-blockerDone

		evalOK, clsOK := h.ev.EvaluateSystemOne(context.Background(), memoRuleBodyOnly, req, opts...)
		assert.Equal(t, messagepolicy.ClassPass, clsOK.Class)
		require.NotNil(t, evalOK)
		assert.False(t, evalOK.CacheHit)
	})
}

// A4: TestMemo_Singleflight (run with -race)
// Kills: racy map; leader cancellation; permit per waiter.
func TestMemo_Singleflight(t *testing.T) {
	allow := newMutableAllowlist("proj-1", "inst-a")
	inst := defaultMemoInstance("inst-a", 1)
	inst.Limits.MaxInFlight = 1 // Only 1 permit: proves waiters hold NO admission permit.

	leaderInProvider := make(chan struct{})
	releaseProvider := make(chan struct{})
	h := newMemoTestHarness(t, []systemoneruntime.Instance{inst}, allow, func(ctx context.Context, _ provider.Transport, iv provider.InstanceView, _ provider.ModelRequest) (provider.ModelResult, error) {
		close(leaderInProvider)
		select {
		case <-releaseProvider:
		case <-ctx.Done():
			return provider.ModelResult{}, ctx.Err()
		}
		return okBinaryResult(iv.Model), nil
	})

	memo := messagepolicy.NewJudgmentMemo("send-a4", h.resolver())
	opts := []messagepolicy.EvalOption{
		messagepolicy.WithMaxEvalTime(2 * time.Second),
		messagepolicy.WithJudgmentMemo(memo),
		messagepolicy.WithMemoMode(messagepolicy.ModeEnforce),
	}
	req := systemone.Request{
		ProjectID: "proj-1",
		Input:     systemone.Input{Fields: []provider.InputField{{Name: messagepolicy.FieldMessageBody, Kind: spec.FieldKindText, Trust: spec.TrustUntrusted, Value: "shared"}}},
	}

	var (
		wg             sync.WaitGroup
		leaderEval     *systemone.Evaluation
		leaderCls      messagepolicy.Classification
		waiterOK       [2]*systemone.Evaluation
		waiterOKCls    [2]messagepolicy.Classification
		waiterCancelCl messagepolicy.Classification
	)

	// Start leader.
	wg.Add(1)
	go func() {
		defer wg.Done()
		r := req
		r.RuleID = "pol-leader"
		leaderEval, leaderCls = h.ev.EvaluateSystemOne(context.Background(), memoRuleBodyOnly, r, opts...)
	}()
	<-leaderInProvider

	// Start canceled waiter: its cancellation must NOT affect the leader.
	cancelCtx, cancelWaiter := context.WithCancel(context.Background())
	waiterCanceledDone := make(chan struct{})
	go func() {
		defer close(waiterCanceledDone)
		r := req
		r.RuleID = "pol-canceled-waiter"
		_, waiterCancelCl = h.ev.EvaluateSystemOne(cancelCtx, memoRuleBodyOnly, r, opts...)
	}()

	// Start 2 normal waiters that will reuse leader's published result (with MaxInFlight=1!).
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			r := req
			r.RuleID = "pol-waiter"
			waiterOK[idx], waiterOKCls[idx] = h.ev.EvaluateSystemOne(context.Background(), memoRuleBodyOnly, r, opts...)
		}(i)
	}

	// Cancel the canceled waiter before releasing the leader.
	time.Sleep(15 * time.Millisecond)
	cancelWaiter()
	<-waiterCanceledDone
	assert.Equal(t, messagepolicy.ClassFailed, waiterCancelCl.Class)
	assert.Equal(t, string(provider.FailCanceled), waiterCancelCl.Reason)

	// Release leader and join.
	close(releaseProvider)
	wg.Wait()

	assert.Equal(t, int32(1), h.calls.Load(), "singleflight must invoke provider exactly once")
	assert.Equal(t, messagepolicy.ClassPass, leaderCls.Class)
	require.NotNil(t, leaderEval)
	assert.False(t, leaderEval.CacheHit)

	for i := 0; i < 2; i++ {
		assert.Equal(t, messagepolicy.ClassPass, waiterOKCls[i].Class)
		require.NotNil(t, waiterOK[i])
		assert.True(t, waiterOK[i].CacheHit)
		assert.Equal(t, leaderEval.EvalID, waiterOK[i].SourceEvalID)
		assert.Nil(t, waiterOK[i].Usage)
	}
}

// A5: TestMemo_NoCrossSend
// Kills: global cache.
func TestMemo_NoCrossSend(t *testing.T) {
	allow := newMutableAllowlist("proj-1", "inst-a")
	h := newMemoTestHarness(t, []systemoneruntime.Instance{defaultMemoInstance("inst-a", 1)}, allow, nil)

	req := systemone.Request{
		ProjectID: "proj-1",
		RuleID:    "pol-1",
		Input:     systemone.Input{Fields: []provider.InputField{{Name: messagepolicy.FieldMessageBody, Kind: spec.FieldKindText, Trust: spec.TrustUntrusted, Value: "same"}}},
	}

	memo1 := messagepolicy.NewJudgmentMemo("send-1", h.resolver())
	eval1, cls1 := h.ev.EvaluateSystemOne(context.Background(), memoRuleBodyOnly, req,
		messagepolicy.WithMaxEvalTime(1500*time.Millisecond),
		messagepolicy.WithJudgmentMemo(memo1),
		messagepolicy.WithMemoMode(messagepolicy.ModeEnforce),
	)
	require.Equal(t, messagepolicy.ClassPass, cls1.Class)
	assert.False(t, eval1.CacheHit)

	memo2 := messagepolicy.NewJudgmentMemo("send-2", h.resolver())
	eval2, cls2 := h.ev.EvaluateSystemOne(context.Background(), memoRuleBodyOnly, req,
		messagepolicy.WithMaxEvalTime(1500*time.Millisecond),
		messagepolicy.WithJudgmentMemo(memo2),
		messagepolicy.WithMemoMode(messagepolicy.ModeEnforce),
	)
	require.Equal(t, messagepolicy.ClassPass, cls2.Class)
	assert.False(t, eval2.CacheHit)
	assert.Equal(t, int32(2), h.calls.Load(), "each send must make its own provider call")
}

// A6: TestMemo_TimeoutNotPublished
// Kills: publishing timeouts (runtime_latched_timeout_not_published) and
// publishing caller-canceled results (caller_canceled_not_published).
func TestMemo_TimeoutNotPublished(t *testing.T) {
	req := systemone.Request{
		ProjectID: "proj-1",
		Input:     systemone.Input{Fields: []provider.InputField{{Name: messagepolicy.FieldMessageBody, Kind: spec.FieldKindText, Trust: spec.TrustUntrusted, Value: "hi"}}},
	}

	t.Run("runtime_latched_timeout_not_published", func(t *testing.T) {
		allow := newMutableAllowlist("proj-1", "inst-a")
		var attemptNum atomic.Int32
		leaderStarted := make(chan struct{})
		h := newMemoTestHarness(t, []systemoneruntime.Instance{defaultMemoInstance("inst-a", 1)}, allow, func(ctx context.Context, _ provider.Transport, iv provider.InstanceView, _ provider.ModelRequest) (provider.ModelResult, error) {
			if attemptNum.Add(1) == 1 {
				close(leaderStarted)
				<-ctx.Done()
				return provider.ModelResult{}, ctx.Err()
			}
			return okBinaryResult(iv.Model), nil
		})

		memo := messagepolicy.NewJudgmentMemo("send-a6-timeout", h.resolver())
		// Small MaxEvalTime with context.Background() drives the leader into a
		// genuine runtime-latched timeout (Reason == "timeout").
		opts := []messagepolicy.EvalOption{
			messagepolicy.WithMaxEvalTime(250 * time.Millisecond),
			messagepolicy.WithJudgmentMemo(memo),
			messagepolicy.WithMemoMode(messagepolicy.ModeEnforce),
		}

		var (
			wg        sync.WaitGroup
			leaderCls messagepolicy.Classification
			waiterEv  *systemone.Evaluation
			waiterCls messagepolicy.Classification
		)

		wg.Add(1)
		go func() {
			defer wg.Done()
			_, leaderCls = h.ev.EvaluateSystemOne(context.Background(), memoRuleBodyOnly, req, opts...)
		}()
		<-leaderStarted

		wg.Add(1)
		go func() {
			defer wg.Done()
			waiterEv, waiterCls = h.ev.EvaluateSystemOne(context.Background(), memoRuleBodyOnly, req, opts...)
		}()
		wg.Wait()

		assert.Equal(t, messagepolicy.ClassFailed, leaderCls.Class)
		assert.Equal(t, string(provider.FailTimeout), leaderCls.Reason, "leader must hit runtime-latched timeout")
		assert.Equal(t, messagepolicy.ClassPass, waiterCls.Class, "waiter with live deadline must evaluate fresh and succeed")
		require.NotNil(t, waiterEv)
		assert.False(t, waiterEv.CacheHit)
		assert.Equal(t, int32(2), h.calls.Load())
	})

	t.Run("caller_canceled_not_published", func(t *testing.T) {
		allow := newMutableAllowlist("proj-1", "inst-a")
		var attemptNum atomic.Int32
		leaderStarted := make(chan struct{})
		h := newMemoTestHarness(t, []systemoneruntime.Instance{defaultMemoInstance("inst-a", 1)}, allow, func(ctx context.Context, _ provider.Transport, iv provider.InstanceView, _ provider.ModelRequest) (provider.ModelResult, error) {
			if attemptNum.Add(1) == 1 {
				close(leaderStarted)
				<-ctx.Done()
				return provider.ModelResult{}, ctx.Err()
			}
			return okBinaryResult(iv.Model), nil
		})

		memo := messagepolicy.NewJudgmentMemo("send-a6-cancel", h.resolver())
		opts := []messagepolicy.EvalOption{
			messagepolicy.WithMaxEvalTime(1500 * time.Millisecond),
			messagepolicy.WithJudgmentMemo(memo),
			messagepolicy.WithMemoMode(messagepolicy.ModeEnforce),
		}

		var (
			wg        sync.WaitGroup
			leaderCls messagepolicy.Classification
			waiterEv  *systemone.Evaluation
			waiterCls messagepolicy.Classification
		)

		leaderCtx, leaderCancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
		defer leaderCancel()
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, leaderCls = h.ev.EvaluateSystemOne(leaderCtx, memoRuleBodyOnly, req, opts...)
		}()
		<-leaderStarted

		wg.Add(1)
		go func() {
			defer wg.Done()
			waiterEv, waiterCls = h.ev.EvaluateSystemOne(context.Background(), memoRuleBodyOnly, req, opts...)
		}()
		wg.Wait()

		assert.Equal(t, messagepolicy.ClassFailed, leaderCls.Class)
		assert.Equal(t, string(provider.FailCanceled), leaderCls.Reason, "short caller ctx must classify as canceled")
		assert.Equal(t, messagepolicy.ClassPass, waiterCls.Class, "waiter with live deadline must evaluate fresh and succeed")
		require.NotNil(t, waiterEv)
		assert.False(t, waiterEv.CacheHit)
		assert.Equal(t, int32(2), h.calls.Load())
	})
}

// TestMemo_SharedDeadlineTimeoutMatchesBaseline (Part 2 R1 regression):
// Compares memo=true against nil-memo baseline (same Reason per policy,
// calls <= baseline) across all three required scenarios:
//
//	(i) shared-deadline timeout (soCtx deadline == MaxEvalTime, provider never answers, N=4);
//	(ii) explicit caller cancellation (N=4, deterministic channel sync);
//	(iii) still-live waiter after unpublishable leader (waiter has >= budget left, fresh call succeeds).
func TestMemo_SharedDeadlineTimeoutMatchesBaseline(t *testing.T) {
	t.Run("shared_deadline_timeout", func(t *testing.T) {
		const numPolicies = 4
		const budget = 1 * time.Second

		runOnce := func(t *testing.T, useMemo bool) ([]string, int32) {
			t.Helper()
			allow := newMutableAllowlist("proj-1", "inst-a")
			h := newMemoTestHarness(t, []systemoneruntime.Instance{defaultMemoInstance("inst-a", 1)}, allow, func(ctx context.Context, _ provider.Transport, _ provider.InstanceView, _ provider.ModelRequest) (provider.ModelResult, error) {
				<-ctx.Done()
				return provider.ModelResult{}, ctx.Err()
			})

			var memo *messagepolicy.JudgmentMemo
			if useMemo {
				memo = messagepolicy.NewJudgmentMemo("send-r1-shared-dl", h.resolver())
			}
			soCtx, soCancel := context.WithTimeout(context.Background(), budget)
			defer soCancel()

			reasons := make([]string, numPolicies)
			var wg sync.WaitGroup
			for i := 0; i < numPolicies; i++ {
				wg.Add(1)
				go func(idx int) {
					defer wg.Done()
					opts := []messagepolicy.EvalOption{
						messagepolicy.WithMaxEvalTime(budget),
					}
					if memo != nil {
						opts = append(opts, messagepolicy.WithJudgmentMemo(memo), messagepolicy.WithMemoMode(messagepolicy.ModeEnforce))
					}
					_, cls := h.ev.EvaluateSystemOne(soCtx, memoRuleBodyOnly, systemone.Request{
						ProjectID: "proj-1",
						RuleID:    "pol",
						Input:     systemone.Input{Fields: []provider.InputField{{Name: messagepolicy.FieldMessageBody, Kind: spec.FieldKindText, Trust: spec.TrustUntrusted, Value: "stuck"}}},
					}, opts...)
					reasons[idx] = cls.Class + "/" + cls.Reason
				}(i)
			}
			wg.Wait()
			return reasons, h.calls.Load()
		}

		baselineReasons, baselineCalls := runOnce(t, false)
		memoReasons, memoCalls := runOnce(t, true)

		assert.Equal(t, baselineReasons, memoReasons, "every policy's classification must match nil-memo baseline")
		onFailedReasons := map[string]string{
			string(provider.FailTimeout):  messagepolicy.OutcomeAdmit,
			string(provider.FailCanceled): messagepolicy.OutcomeReject,
		}
		for i, r := range memoReasons {
			assert.Equal(t, messagepolicy.ClassFailed+"/"+string(provider.FailTimeout), r, "policy %d must classify as failed/timeout", i)
			baseOut := messagepolicy.ResolveOutcome(messagepolicy.ClassFailed, baselineReasons[i][len(messagepolicy.ClassFailed)+1:], messagepolicy.OutcomeReject, messagepolicy.OutcomeReject, onFailedReasons)
			memoOut := messagepolicy.ResolveOutcome(messagepolicy.ClassFailed, r[len(messagepolicy.ClassFailed)+1:], messagepolicy.OutcomeReject, messagepolicy.OutcomeReject, onFailedReasons)
			assert.Equal(t, baseOut, memoOut, "policy %d on_failed_reasons outcome must match baseline", i)
			assert.Equal(t, messagepolicy.OutcomeAdmit, memoOut)
		}
		assert.LessOrEqual(t, memoCalls, baselineCalls, "memo calls must not exceed baseline calls")
		assert.Equal(t, int32(1), memoCalls, "waiters whose remaining deadline < budget must not start a doomed second call")
	})

	t.Run("explicit_caller_cancellation", func(t *testing.T) {
		const numPolicies = 4
		const budget = 2 * time.Second

		runOnce := func(t *testing.T, useMemo bool) ([]string, int32) {
			t.Helper()
			allow := newMutableAllowlist("proj-1", "inst-a")
			var firstEntered sync.Once
			entered := make(chan struct{})
			h := newMemoTestHarness(t, []systemoneruntime.Instance{defaultMemoInstance("inst-a", 1)}, allow, func(ctx context.Context, _ provider.Transport, _ provider.InstanceView, _ provider.ModelRequest) (provider.ModelResult, error) {
				firstEntered.Do(func() { close(entered) })
				<-ctx.Done()
				return provider.ModelResult{}, ctx.Err()
			})

			var memo *messagepolicy.JudgmentMemo
			if useMemo {
				memo = messagepolicy.NewJudgmentMemo("send-r1-cancel", h.resolver())
			}
			cancelCtx, cancel := context.WithCancel(context.Background())
			defer cancel()

			reasons := make([]string, numPolicies)
			var wg sync.WaitGroup
			for i := 0; i < numPolicies; i++ {
				wg.Add(1)
				go func(idx int) {
					defer wg.Done()
					opts := []messagepolicy.EvalOption{
						messagepolicy.WithMaxEvalTime(budget),
					}
					if memo != nil {
						opts = append(opts, messagepolicy.WithJudgmentMemo(memo), messagepolicy.WithMemoMode(messagepolicy.ModeEnforce))
					}
					_, cls := h.ev.EvaluateSystemOne(cancelCtx, memoRuleBodyOnly, systemone.Request{
						ProjectID: "proj-1",
						RuleID:    "pol",
						Input:     systemone.Input{Fields: []provider.InputField{{Name: messagepolicy.FieldMessageBody, Kind: spec.FieldKindText, Trust: spec.TrustUntrusted, Value: "cancel-me"}}},
					}, opts...)
					reasons[idx] = cls.Class + "/" + cls.Reason
				}(i)
			}
			<-entered
			cancel()
			wg.Wait()
			return reasons, h.calls.Load()
		}

		baselineReasons, baselineCalls := runOnce(t, false)
		memoReasons, memoCalls := runOnce(t, true)

		assert.Equal(t, baselineReasons, memoReasons, "every policy's classification must match nil-memo baseline on explicit cancel")
		onFailedReasons := map[string]string{
			string(provider.FailTimeout):  messagepolicy.OutcomeAdmit,
			string(provider.FailCanceled): messagepolicy.OutcomeReject,
		}
		for i, r := range memoReasons {
			assert.Equal(t, messagepolicy.ClassFailed+"/"+string(provider.FailCanceled), r, "policy %d must classify as failed/canceled", i)
			baseOut := messagepolicy.ResolveOutcome(messagepolicy.ClassFailed, baselineReasons[i][len(messagepolicy.ClassFailed)+1:], messagepolicy.OutcomeReject, messagepolicy.OutcomeReject, onFailedReasons)
			memoOut := messagepolicy.ResolveOutcome(messagepolicy.ClassFailed, r[len(messagepolicy.ClassFailed)+1:], messagepolicy.OutcomeReject, messagepolicy.OutcomeReject, onFailedReasons)
			assert.Equal(t, baseOut, memoOut, "policy %d on_failed_reasons outcome must match baseline", i)
			assert.Equal(t, messagepolicy.OutcomeReject, memoOut)
		}
		assert.LessOrEqual(t, memoCalls, baselineCalls)
	})

	t.Run("still_live_waiter_re_leads_and_succeeds", func(t *testing.T) {
		const budget = 300 * time.Millisecond

		runOnce := func(t *testing.T, useMemo bool) (string, string, int32) {
			t.Helper()
			allow := newMutableAllowlist("proj-1", "inst-a")
			var attempt atomic.Int32
			leaderEntered := make(chan struct{})
			h := newMemoTestHarness(t, []systemoneruntime.Instance{defaultMemoInstance("inst-a", 1)}, allow, func(ctx context.Context, _ provider.Transport, iv provider.InstanceView, _ provider.ModelRequest) (provider.ModelResult, error) {
				if attempt.Add(1) == 1 {
					close(leaderEntered)
					<-ctx.Done()
					return provider.ModelResult{}, ctx.Err()
				}
				return okBinaryResult(iv.Model), nil
			})

			var memo *messagepolicy.JudgmentMemo
			if useMemo {
				memo = messagepolicy.NewJudgmentMemo("send-r1-live-waiter", h.resolver())
			}
			opts := []messagepolicy.EvalOption{messagepolicy.WithMaxEvalTime(budget)}
			if memo != nil {
				opts = append(opts, messagepolicy.WithJudgmentMemo(memo), messagepolicy.WithMemoMode(messagepolicy.ModeEnforce))
			}
			req := systemone.Request{
				ProjectID: "proj-1",
				RuleID:    "pol",
				Input:     systemone.Input{Fields: []provider.InputField{{Name: messagepolicy.FieldMessageBody, Kind: spec.FieldKindText, Trust: spec.TrustUntrusted, Value: "live-waiter"}}},
			}

			// Leader's own context is explicitly canceled; waiter has a generous
			// deadline (5s >> 300ms budget), so waiter IS allowed to take leadership.
			leaderCtx, leaderCancel := context.WithCancel(context.Background())
			defer leaderCancel()
			waiterCtx, waiterCancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer waiterCancel()

			var (
				wg        sync.WaitGroup
				leaderRes string
				waiterRes string
			)
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, cls := h.ev.EvaluateSystemOne(leaderCtx, memoRuleBodyOnly, req, opts...)
				leaderRes = cls.Class + "/" + cls.Reason
			}()
			<-leaderEntered

			wg.Add(1)
			go func() {
				defer wg.Done()
				_, cls := h.ev.EvaluateSystemOne(waiterCtx, memoRuleBodyOnly, req, opts...)
				waiterRes = cls.Class + "/" + cls.Reason
			}()

			// Cancel leader deterministically; waiter takes leadership and succeeds.
			leaderCancel()
			wg.Wait()
			return leaderRes, waiterRes, h.calls.Load()
		}

		baseLeader, baseWaiter, baseCalls := runOnce(t, false)
		memoLeader, memoWaiter, memoCalls := runOnce(t, true)

		assert.Equal(t, baseLeader, memoLeader)
		assert.Equal(t, baseWaiter, memoWaiter)
		assert.Equal(t, messagepolicy.ClassFailed+"/"+string(provider.FailCanceled), memoLeader)
		assert.Equal(t, messagepolicy.ClassPass+"/", memoWaiter)
		assert.LessOrEqual(t, memoCalls, baseCalls)
		assert.Equal(t, int32(2), memoCalls)
	})

	// R1' regression: saturated MaxInFlight where AdmissionWait (40ms) > margin (30ms
	// at 600ms budget). Waiters MUST re-lead and classify as capacity (matching the
	// nil-memo baseline) rather than sleeping to their deadline and reporting timeout.
	t.Run("saturated_capacity_matches_baseline", func(t *testing.T) {
		const numPolicies = 3
		const budget = 600 * time.Millisecond

		runOnce := func(t *testing.T, useMemo bool) ([]string, []time.Duration, int32) {
			t.Helper()
			allow := newMutableAllowlist("proj-1", "inst-a")
			inst := defaultMemoInstance("inst-a", 1)
			inst.Limits.MaxInFlight = 1

			holderIn := make(chan struct{})
			release := make(chan struct{})
			var once sync.Once
			h := newMemoTestHarness(t, []systemoneruntime.Instance{inst}, allow, func(ctx context.Context, _ provider.Transport, iv provider.InstanceView, _ provider.ModelRequest) (provider.ModelResult, error) {
				once.Do(func() { close(holderIn) })
				select {
				case <-release:
				case <-ctx.Done():
				}
				return okBinaryResult(iv.Model), nil
			})
			mk := func(v string) systemone.Request {
				return systemone.Request{
					ProjectID: "proj-1",
					RuleID:    "pol",
					Input:     systemone.Input{Fields: []provider.InputField{{Name: messagepolicy.FieldMessageBody, Kind: spec.FieldKindText, Trust: spec.TrustUntrusted, Value: v}}},
				}
			}

			holderDone := make(chan struct{})
			go func() {
				defer close(holderDone)
				_, _ = h.ev.EvaluateSystemOne(context.Background(), memoRuleBodyOnly, mk("holder"), messagepolicy.WithMaxEvalTime(3*time.Second))
			}()
			<-holderIn
			defer func() {
				close(release)
				<-holderDone
			}()

			var memo *messagepolicy.JudgmentMemo
			if useMemo {
				memo = messagepolicy.NewJudgmentMemo("send-r1p-cap", h.resolver())
			}
			soCtx, soCancel := context.WithTimeout(context.Background(), budget)
			defer soCancel()

			reasons := make([]string, numPolicies)
			latencies := make([]time.Duration, numPolicies)
			var wg sync.WaitGroup
			for i := 0; i < numPolicies; i++ {
				wg.Add(1)
				go func(idx int) {
					defer wg.Done()
					opts := []messagepolicy.EvalOption{messagepolicy.WithMaxEvalTime(budget)}
					if memo != nil {
						opts = append(opts, messagepolicy.WithJudgmentMemo(memo), messagepolicy.WithMemoMode(messagepolicy.ModeEnforce))
					}
					s := time.Now()
					_, cls := h.ev.EvaluateSystemOne(soCtx, memoRuleBodyOnly, mk("same"), opts...)
					reasons[idx] = cls.Class + "/" + cls.Reason
					latencies[idx] = time.Since(s)
				}(i)
			}
			wg.Wait()
			return reasons, latencies, h.calls.Load()
		}

		baseReasons, _, baseCalls := runOnce(t, false)
		memoReasons, memoLatencies, memoCalls := runOnce(t, true)

		assert.Equal(t, baseReasons, memoReasons, "every policy's classification must match nil-memo baseline under capacity saturation")
		onFailedReasons := map[string]string{
			string(provider.FailCapacity): messagepolicy.OutcomeAdmit,
			string(provider.FailTimeout):  messagepolicy.OutcomeReject,
		}
		for i, r := range memoReasons {
			assert.Equal(t, messagepolicy.ClassFailed+"/"+string(provider.FailCapacity), r, "policy %d must classify as failed/capacity", i)
			baseOut := messagepolicy.ResolveOutcome(messagepolicy.ClassFailed, baseReasons[i][len(messagepolicy.ClassFailed)+1:], messagepolicy.OutcomeReject, messagepolicy.OutcomeReject, onFailedReasons)
			memoOut := messagepolicy.ResolveOutcome(messagepolicy.ClassFailed, r[len(messagepolicy.ClassFailed)+1:], messagepolicy.OutcomeReject, messagepolicy.OutcomeReject, onFailedReasons)
			assert.Equal(t, baseOut, memoOut, "policy %d on_failed_reasons outcome must match baseline", i)
			assert.Equal(t, messagepolicy.OutcomeAdmit, memoOut)
		}
		assert.LessOrEqual(t, memoCalls, baseCalls, "memo calls must not exceed baseline calls")
		for i, lat := range memoLatencies {
			assert.Less(t, lat, budget/2, "waiter %d latency %v must stay well below budget %v (must not sleep to deadline)", i, lat, budget)
		}
	})

	// M6: pre-expired WithDeadline ctx must match nil-memo baseline (canceled, not timeout).
	t.Run("pre_expired_deadline_matches_baseline", func(t *testing.T) {
		allow := newMutableAllowlist("proj-1", "inst-a")
		h := newMemoTestHarness(t, []systemoneruntime.Instance{defaultMemoInstance("inst-a", 1)}, allow, nil)
		req := systemone.Request{
			ProjectID: "proj-1",
			RuleID:    "pol",
			Input:     systemone.Input{Fields: []provider.InputField{{Name: messagepolicy.FieldMessageBody, Kind: spec.FieldKindText, Trust: spec.TrustUntrusted, Value: "expired"}}},
		}

		expiredCtx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
		defer cancel()

		_, baseCls := h.ev.EvaluateSystemOne(expiredCtx, memoRuleBodyOnly, req, messagepolicy.WithMaxEvalTime(time.Second))
		memo := messagepolicy.NewJudgmentMemo("send-m6", h.resolver())
		_, memoCls := h.ev.EvaluateSystemOne(expiredCtx, memoRuleBodyOnly, req,
			messagepolicy.WithMaxEvalTime(time.Second),
			messagepolicy.WithJudgmentMemo(memo),
			messagepolicy.WithMemoMode(messagepolicy.ModeEnforce),
		)

		assert.Equal(t, baseCls.Class, memoCls.Class)
		assert.Equal(t, baseCls.Reason, memoCls.Reason, "pre-expired deadline ctx must match uncached Reason (canceled)")
		assert.Equal(t, string(provider.FailCanceled), memoCls.Reason)
		assert.Zero(t, h.calls.Load())
	})
}

// A7: TestMemo_ReuseRevalidation
// Kills: ID-only validation; skipping allowlist/revision/selection-order re-checks.
func TestMemo_ReuseRevalidation(t *testing.T) {
	req := systemone.Request{
		ProjectID: "proj-1",
		Input:     systemone.Input{Fields: []provider.InputField{{Name: messagepolicy.FieldMessageBody, Kind: spec.FieldKindText, Trust: spec.TrustUntrusted, Value: "hi"}}},
	}

	t.Run("instance_disabled_forces_fresh_call", func(t *testing.T) {
		allow := newMutableAllowlist("proj-1", "inst-a")
		inst := defaultMemoInstance("inst-a", 1)
		h := newMemoTestHarness(t, []systemoneruntime.Instance{inst}, allow, nil)
		memo := messagepolicy.NewJudgmentMemo("send-a7-dis", h.resolver())
		opts := []messagepolicy.EvalOption{messagepolicy.WithMaxEvalTime(time.Second), messagepolicy.WithJudgmentMemo(memo), messagepolicy.WithMemoMode(messagepolicy.ModeEnforce)}

		_, cls1 := h.ev.EvaluateSystemOne(context.Background(), memoRuleBodyOnly, req, opts...)
		require.Equal(t, messagepolicy.ClassPass, cls1.Class)

		inst.Enabled = false
		h.instances.set(inst)

		_, cls2 := h.ev.EvaluateSystemOne(context.Background(), memoRuleBodyOnly, req, opts...)
		assert.Equal(t, messagepolicy.ClassFailed, cls2.Class)
		assert.Equal(t, string(provider.FailNoEligibleInstance), cls2.Reason)
	})

	t.Run("revision_bump_forces_fresh_call", func(t *testing.T) {
		allow := newMutableAllowlist("proj-1", "inst-a")
		inst := defaultMemoInstance("inst-a", 1)
		h := newMemoTestHarness(t, []systemoneruntime.Instance{inst}, allow, nil)
		memo := messagepolicy.NewJudgmentMemo("send-a7-rev", h.resolver())
		opts := []messagepolicy.EvalOption{messagepolicy.WithMaxEvalTime(time.Second), messagepolicy.WithJudgmentMemo(memo), messagepolicy.WithMemoMode(messagepolicy.ModeEnforce)}

		_, cls1 := h.ev.EvaluateSystemOne(context.Background(), memoRuleBodyOnly, req, opts...)
		require.Equal(t, messagepolicy.ClassPass, cls1.Class)
		require.Equal(t, int32(1), h.calls.Load())

		inst.Revision = 2
		h.instances.set(inst)

		eval2, cls2 := h.ev.EvaluateSystemOne(context.Background(), memoRuleBodyOnly, req, opts...)
		require.Equal(t, messagepolicy.ClassPass, cls2.Class)
		assert.False(t, eval2.CacheHit)
		assert.Equal(t, int32(2), h.calls.Load(), "revision bump must force fresh provider call")
	})

	t.Run("allowlist_removal_forces_fresh_call", func(t *testing.T) {
		allow := newMutableAllowlist("proj-1", "inst-a")
		h := newMemoTestHarness(t, []systemoneruntime.Instance{defaultMemoInstance("inst-a", 1)}, allow, nil)
		memo := messagepolicy.NewJudgmentMemo("send-a7-allow", h.resolver())
		opts := []messagepolicy.EvalOption{messagepolicy.WithMaxEvalTime(time.Second), messagepolicy.WithJudgmentMemo(memo), messagepolicy.WithMemoMode(messagepolicy.ModeEnforce)}

		_, cls1 := h.ev.EvaluateSystemOne(context.Background(), memoRuleBodyOnly, req, opts...)
		require.Equal(t, messagepolicy.ClassPass, cls1.Class)

		allow.set("proj-1") // remove inst-a from project allowlist

		_, cls2 := h.ev.EvaluateSystemOne(context.Background(), memoRuleBodyOnly, req, opts...)
		assert.Equal(t, messagepolicy.ClassFailed, cls2.Class)
		assert.Equal(t, string(provider.FailNoEligibleInstance), cls2.Reason)
	})

	t.Run("selection_order_change_forces_fresh_call", func(t *testing.T) {
		// Rule with no explicit binding.instance selects the single enabled allowlisted instance.
		implicitRule := `{"dialect":"system_one","inputs":["message.body"],"spec":{"version":1,"questions":[{"id":"flagged","type":"binary","instructions":"Is this flagged?"}]}}`
		allow := newMutableAllowlist("proj-1", "inst-a")
		h := newMemoTestHarness(t, []systemoneruntime.Instance{
			defaultMemoInstance("inst-a", 1),
			defaultMemoInstance("inst-b", 1),
		}, allow, nil)
		memo := messagepolicy.NewJudgmentMemo("send-a7-sel", h.resolver())
		opts := []messagepolicy.EvalOption{messagepolicy.WithMaxEvalTime(time.Second), messagepolicy.WithJudgmentMemo(memo), messagepolicy.WithMemoMode(messagepolicy.ModeEnforce)}

		ctx := systemone.WithProjectID(context.Background(), "proj-1")
		eval1, cls1 := h.ev.EvaluateSystemOne(ctx, implicitRule, req, opts...)
		require.Equal(t, messagepolicy.ClassPass, cls1.Class)
		require.Equal(t, "inst-a", eval1.Attempts[0].Instance)

		// Change project allowlist so inst-b is now selected instead of inst-a.
		allow.set("proj-1", "inst-b")

		eval2, cls2 := h.ev.EvaluateSystemOne(ctx, implicitRule, req, opts...)
		require.Equal(t, messagepolicy.ClassPass, cls2.Class)
		assert.False(t, eval2.CacheHit)
		assert.Equal(t, "inst-b", eval2.Attempts[0].Instance)
		assert.Equal(t, int32(2), h.calls.Load())
	})

	// O1: changing only Model at the same InstanceRevision must force a fresh call.
	t.Run("model_change_same_revision_forces_fresh_call", func(t *testing.T) {
		allow := newMutableAllowlist("proj-1", "inst-a")
		inst := defaultMemoInstance("inst-a", 1)
		h := newMemoTestHarness(t, []systemoneruntime.Instance{inst}, allow, nil)
		memo := messagepolicy.NewJudgmentMemo("send-a7-model", h.resolver())
		opts := []messagepolicy.EvalOption{messagepolicy.WithMaxEvalTime(time.Second), messagepolicy.WithJudgmentMemo(memo), messagepolicy.WithMemoMode(messagepolicy.ModeEnforce)}

		_, cls1 := h.ev.EvaluateSystemOne(context.Background(), memoRuleBodyOnly, req, opts...)
		require.Equal(t, messagepolicy.ClassPass, cls1.Class)
		require.Equal(t, int32(1), h.calls.Load())

		inst.Model = "fake-model-2" // same Revision (1)
		h.instances.set(inst)

		eval2, cls2 := h.ev.EvaluateSystemOne(context.Background(), memoRuleBodyOnly, req, opts...)
		require.Equal(t, messagepolicy.ClassPass, cls2.Class)
		assert.False(t, eval2.CacheHit)
		assert.Equal(t, int32(2), h.calls.Load(), "model change at same revision must force fresh provider call")
	})

	// O1: changing only Adapter at the same InstanceRevision must force a fresh call.
	t.Run("adapter_change_same_revision_forces_fresh_call", func(t *testing.T) {
		allow := newMutableAllowlist("proj-1", "inst-a")
		inst := defaultMemoInstance("inst-a", 1)
		h := newMemoTestHarness(t, []systemoneruntime.Instance{inst}, allow, nil)
		memo := messagepolicy.NewJudgmentMemo("send-a7-adapter", h.resolver())
		opts := []messagepolicy.EvalOption{messagepolicy.WithMaxEvalTime(time.Second), messagepolicy.WithJudgmentMemo(memo), messagepolicy.WithMemoMode(messagepolicy.ModeEnforce)}

		_, cls1 := h.ev.EvaluateSystemOne(context.Background(), memoRuleBodyOnly, req, opts...)
		require.Equal(t, messagepolicy.ClassPass, cls1.Class)
		require.Equal(t, int32(1), h.calls.Load())

		inst.Adapter = "fake-mp-2" // same Revision (1)
		h.instances.set(inst)

		eval2, cls2 := h.ev.EvaluateSystemOne(context.Background(), memoRuleBodyOnly, req, opts...)
		require.Equal(t, messagepolicy.ClassPass, cls2.Class)
		assert.False(t, eval2.CacheHit)
		assert.Equal(t, int32(2), h.calls.Load(), "adapter change at same revision must force fresh provider call")
	})
}

// A8: TestShadow_SnapshotValidated
// Kills: shared pointer; skipping binding revalidation; reusing deadline-bound failures.
func TestShadow_SnapshotValidated(t *testing.T) {
	allow := newMutableAllowlist("proj-1", "inst-a")
	inst := defaultMemoInstance("inst-a", 1)
	h := newMemoTestHarness(t, []systemoneruntime.Instance{inst}, allow, nil)

	req := systemone.Request{
		ProjectID: "proj-1",
		RuleID:    "pol-enforce",
		Revision:  1,
		Input:     systemone.Input{Fields: []provider.InputField{{Name: messagepolicy.FieldMessageBody, Kind: spec.FieldKindText, Trust: spec.TrustUntrusted, Value: "msg-1"}}},
	}

	enforceMemo := messagepolicy.NewJudgmentMemo("send-a8", h.resolver())
	enforceOpts := []messagepolicy.EvalOption{
		messagepolicy.WithMaxEvalTime(1500 * time.Millisecond),
		messagepolicy.WithJudgmentMemo(enforceMemo),
		messagepolicy.WithMemoMode(messagepolicy.ModeEnforce),
	}

	// Enforce evaluates msg-1 -> published.
	evalEnf, clsEnf := h.ev.EvaluateSystemOne(context.Background(), memoRuleBodyOnly, req, enforceOpts...)
	require.Equal(t, messagepolicy.ClassPass, clsEnf.Class)
	require.Equal(t, int32(1), h.calls.Load())

	// Freeze snapshot for shadow after enforce join.
	shadowSnap := enforceMemo.FreezeForShadow(h.resolver())

	// Later mutation to enforceMemo (adding msg-2) MUST NOT be visible to shadowSnap.
	req2 := req
	req2.Input.Fields = []provider.InputField{{Name: messagepolicy.FieldMessageBody, Kind: spec.FieldKindText, Trust: spec.TrustUntrusted, Value: "msg-2"}}
	_, _ = h.ev.EvaluateSystemOne(context.Background(), memoRuleBodyOnly, req2, enforceOpts...)
	require.Equal(t, int32(2), h.calls.Load())

	shadowOpts := []messagepolicy.EvalOption{
		messagepolicy.WithMaxEvalTime(1500 * time.Millisecond),
		messagepolicy.WithJudgmentMemo(shadowSnap),
		messagepolicy.WithMemoMode(messagepolicy.ModeShadow),
	}

	// Shadow evaluating msg-1 hits frozen snapshot (calls stays 2).
	shadowReq1 := req
	shadowReq1.RuleID = "pol-shadow"
	shadowEval1, shadowCls1 := h.ev.EvaluateSystemOne(context.Background(), memoRuleBodyOnly, shadowReq1, shadowOpts...)
	require.Equal(t, messagepolicy.ClassPass, shadowCls1.Class)
	assert.True(t, shadowEval1.CacheHit)
	assert.Equal(t, evalEnf.EvalID, shadowEval1.SourceEvalID)
	assert.Nil(t, shadowEval1.Usage)
	assert.Equal(t, int32(2), h.calls.Load())

	// Shadow evaluating msg-2 (published after freeze) MUST NOT hit frozen snapshot (calls = 3).
	shadowEval2, shadowCls2 := h.ev.EvaluateSystemOne(context.Background(), memoRuleBodyOnly, req2, shadowOpts...)
	require.Equal(t, messagepolicy.ClassPass, shadowCls2.Class)
	assert.False(t, shadowEval2.CacheHit)
	assert.Equal(t, int32(3), h.calls.Load(), "shadow snapshot must not see post-freeze mutations")

	// Shadow revalidates binding: bumping revision invalidates reuse (calls = 4).
	inst.Revision = 2
	h.instances.set(inst)
	shadowEval3, shadowCls3 := h.ev.EvaluateSystemOne(context.Background(), memoRuleBodyOnly, shadowReq1, shadowOpts...)
	require.Equal(t, messagepolicy.ClassPass, shadowCls3.Class)
	assert.False(t, shadowEval3.CacheHit)
	assert.Equal(t, int32(4), h.calls.Load(), "shadow must revalidate current binding before reusing snapshot")
}

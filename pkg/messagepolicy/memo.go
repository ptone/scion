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
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/GoogleCloudPlatform/scion/pkg/ruleengine"
	"github.com/GoogleCloudPlatform/scion/pkg/systemone"
	"github.com/GoogleCloudPlatform/scion/pkg/systemone/provider"
	"github.com/GoogleCloudPlatform/scion/pkg/systemone/spec"
)

// BindingResolver re-checks the current System One binding selection for
// compiled under projectID using non-secret store reads only.
//
// Snapshot contract: a reused judgment is valid as of this re-read. A later
// mutation falls in the same window a single uncached evaluation already has
// between resolveBinding and provider invocation; no atomicity is claimed, and
// existing authz and credential paths are unchanged.
type BindingResolver func(ctx context.Context, compiled *spec.Compiled, projectID string) (systemone.ResolvedBinding, bool)

// JudgmentMemo is a per-send, in-memory singleflight and reuse memo for
// System One judgment evaluations (M3 Slice A, ptone/scion#8049).
//
// It memoizes ONLY the judgment-only EvaluateSystemOne boundary — never
// EvaluatePolicy or CEL outcomes. It is created for one origin send and
// discarded when the send finishes; there is no persistence, no global cache,
// and no cross-send reuse.
type JudgmentMemo struct {
	sendID   string
	resolver BindingResolver
	frozen   bool

	mu        sync.Mutex
	published map[string]*memoEntry
	inflight  map[string]*memoFlight
}

type memoEntry struct {
	eval    *systemone.Evaluation
	cls     Classification
	attempt systemone.Attempt
}

type memoFlight struct {
	done      chan struct{}
	published bool
	reason    string
	entry     *memoEntry
}

// NewJudgmentMemo constructs a per-send JudgmentMemo bound to sendID and
// resolver. If resolver is nil, reuse validation fails closed and waiters
// evaluate fresh.
func NewJudgmentMemo(sendID string, resolver BindingResolver) *JudgmentMemo {
	return &JudgmentMemo{
		sendID:    strings.TrimSpace(sendID),
		resolver:  resolver,
		published: make(map[string]*memoEntry),
		inflight:  make(map[string]*memoFlight),
	}
}

// FreezeForShadow returns a deep-copied, frozen snapshot of all published
// non-deadline-bound entries suitable for handing to an asynchronous shadow
// job after the enforce join. Subsequent mutations to m are never visible to
// the returned snapshot, and the shadow worker re-runs binding validation via
// resolver before reusing any entry.
func (m *JudgmentMemo) FreezeForShadow(resolver BindingResolver) *JudgmentMemo {
	snap := &JudgmentMemo{
		sendID:    "",
		resolver:  resolver,
		frozen:    true,
		published: make(map[string]*memoEntry),
	}
	if m == nil {
		return snap
	}
	snap.sendID = m.sendID

	m.mu.Lock()
	defer m.mu.Unlock()
	for key, ent := range m.published {
		if ent == nil || !isShadowReusableEntry(ent) {
			continue
		}
		// Remap enforce-phase key mode to shadow-phase mode so shadow policies
		// with an identical rule, project, selected inputs, params, MaxEvalTime,
		// and sendID can reuse published enforce judgments after revalidating
		// the current binding.
		shadowKey := remapKeyMode(key, ModeEnforce, ModeShadow)
		snap.published[shadowKey] = cloneMemoEntry(ent)
	}
	return snap
}

const memoKeyModePrefix = "\x00mode="

func remapKeyMode(key, fromMode, toMode string) string {
	oldSeg := memoKeyModePrefix + fromMode + "\x00"
	newSeg := memoKeyModePrefix + toMode + "\x00"
	if strings.Contains(key, oldSeg) {
		return strings.Replace(key, oldSeg, newSeg, 1)
	}
	return key
}

// buildMemoKey computes the canonical per-send memo key for a System One
// evaluation. It includes:
//   - rule content hash + compiled spec/dialect revision (version, form, kind,
//     environment name/version, on_oversize, timeout, binding instance/pool)
//   - eval/credential project ID
//   - canonical fingerprint of selected fields (ONLY compiled.Inputs) and choice sets
//   - params and mode
//   - evaluation options configuration (MaxEvalTime)
//   - send identity
//
// Policy ID and policy revision are intentionally excluded (D3).
func buildMemoKey(
	sendID string,
	compiled *spec.Compiled,
	projectID string,
	input systemone.Input,
	mode string,
	maxEvalTime time.Duration,
) string {
	var b strings.Builder
	writeLenStr := func(s string) {
		b.WriteString(strconv.Itoa(len(s)))
		b.WriteByte(':')
		b.WriteString(s)
		b.WriteByte(';')
	}

	writeLenStr("send")
	writeLenStr(sendID)
	writeLenStr("dialect")
	writeLenStr(systemone.DialectName)
	writeLenStr("hash")
	writeLenStr(compiled.ContentHash)
	writeLenStr("spec_ver")
	writeLenStr(strconv.Itoa(compiled.Spec.Version))
	writeLenStr("spec_form")
	writeLenStr(compiled.Spec.Form)
	writeLenStr("kind")
	writeLenStr(string(compiled.Kind))
	writeLenStr("env")
	writeLenStr(compiled.Environment.Name)
	writeLenStr(strconv.Itoa(compiled.Environment.Version))
	writeLenStr("oversize")
	writeLenStr(compiled.OnOversize)
	writeLenStr("timeout_ns")
	writeLenStr(strconv.FormatInt(compiled.Timeout.Nanoseconds(), 10))
	writeLenStr("binding")
	writeLenStr(compiled.Spec.Binding.Instance)
	writeLenStr(compiled.Spec.Binding.Pool)
	writeLenStr("project")
	writeLenStr(strings.TrimSpace(projectID))
	writeLenStr("max_eval_ns")
	writeLenStr(strconv.FormatInt(maxEvalTime.Nanoseconds(), 10))

	// Canonical fingerprint of selected input fields ONLY (unselected fields are excluded).
	byName := make(map[string]provider.InputField, len(input.Fields))
	for _, f := range input.Fields {
		byName[f.Name] = f
	}
	fieldHash := sha256.New()
	for _, name := range compiled.Inputs {
		f, ok := byName[name]
		_, _ = fmt.Fprintf(fieldHash, "%d:%s|%t|%d:%s|%d:%s|%d:%s\n",
			len(name), name, ok,
			len(f.Kind), f.Kind,
			len(f.Trust), f.Trust,
			len(f.Value), f.Value,
		)
	}
	// Include choice sets in deterministic sorted order.
	if len(input.ChoiceSets) > 0 {
		setNames := make([]string, 0, len(input.ChoiceSets))
		for k := range input.ChoiceSets {
			setNames = append(setNames, k)
		}
		slices.Sort(setNames)
		for _, k := range setNames {
			vals := input.ChoiceSets[k]
			_, _ = fmt.Fprintf(fieldHash, "cs:%d:%s:%d:", len(k), k, len(vals))
			for _, v := range vals {
				_, _ = fmt.Fprintf(fieldHash, "%d:%s,", len(v), v)
			}
			_, _ = fieldHash.Write([]byte("\n"))
		}
	}
	writeLenStr("inputs_fp")
	writeLenStr(hex.EncodeToString(fieldHash.Sum(nil)))

	// Include params in deterministic sorted order.
	paramHash := sha256.New()
	if len(input.Params) > 0 {
		pKeys := make([]string, 0, len(input.Params))
		for k := range input.Params {
			pKeys = append(pKeys, k)
		}
		slices.Sort(pKeys)
		for _, k := range pKeys {
			v := input.Params[k]
			_, _ = fmt.Fprintf(paramHash, "%d:%s=%d:%s\n", len(k), k, len(v), v)
		}
	}
	writeLenStr("params_fp")
	writeLenStr(hex.EncodeToString(paramHash.Sum(nil)))

	// Mode marker kept explicit so FreezeForShadow can remap enforce -> shadow
	// without weakening key separation inside a live memo.
	b.WriteString(memoKeyModePrefix)
	b.WriteString(mode)
	b.WriteByte(0)

	return b.String()
}

// isPublishableResult reports whether (eval, cls) may be published for reuse
// within the same send:
//   - Only results of an attempted provider call (len(Attempts) >= 1 and
//     Attempts[0].Requests >= 1 and Attempts[0].Instance != "").
//   - Success, Uncertain, and Failed with a non-timeout/non-canceled/non-capacity
//     reason.
//   - NEVER zero-attempt results, canceled, capacity, or any timeout.
func isPublishableResult(eval *systemone.Evaluation, cls Classification) bool {
	if eval == nil || len(eval.Attempts) == 0 {
		return false
	}
	att := eval.Attempts[0]
	if att.Requests < 1 || strings.TrimSpace(att.Instance) == "" {
		return false
	}
	if isDeadlineOrAdmissionFailure(cls) {
		return false
	}
	return cls.Class == ClassPass || cls.Class == ClassUncertain || cls.Class == ClassFailed
}

func isDeadlineOrAdmissionFailure(cls Classification) bool {
	if cls.Class != ClassFailed {
		return false
	}
	switch cls.Reason {
	case string(provider.FailTimeout),
		string(provider.FailCanceled),
		string(provider.FailCapacity),
		ReasonBudgetExhausted:
		return true
	default:
		return false
	}
}

func isShadowReusableEntry(ent *memoEntry) bool {
	if ent == nil {
		return false
	}
	return isPublishableResult(ent.eval, ent.cls)
}

func (m *JudgmentMemo) revalidateBinding(
	ctx context.Context,
	compiled *spec.Compiled,
	projectID string,
	leaderAttempt systemone.Attempt,
) bool {
	if m == nil || m.resolver == nil || compiled == nil {
		return false
	}
	resolved, ok := m.resolver(ctx, compiled, projectID)
	if !ok || !resolved.Enabled {
		return false
	}
	if resolved.InstanceName == "" || resolved.InstanceName != leaderAttempt.Instance {
		return false
	}
	if resolved.Adapter != leaderAttempt.Adapter {
		return false
	}
	if resolved.InstanceRevision != leaderAttempt.InstanceRevision {
		return false
	}
	if resolved.ConfiguredModel != leaderAttempt.ConfiguredModel {
		return false
	}
	return true
}

// effectiveMemoBudget computes an upper bound on runtime.effectiveOwnDuration
// for a compiled rule and MaxEvalTime ceiling.
func effectiveMemoBudget(ruleTimeout, maxEvalTime time.Duration) time.Duration {
	d := systemone.DefaultRuleTimeout
	if ruleTimeout > 0 {
		d = ruleTimeout
	}
	if maxEvalTime > 0 {
		margin := max(ruleengine.EngineDeadlineMargin(maxEvalTime), 0)
		engDur := max(maxEvalTime-margin, 0)
		if engDur < d {
			d = engDur
		}
	}
	return d
}

// classifyWaiterContext classifies a waiter's context termination to match the
// runtime's own-deadline vs caller-context rule: caller-ctx expiry before the
// waiter's own runtime deadline is FailCanceled; own runtime deadline expiry is
// FailTimeout.
func classifyWaiterContext(err error, ownDeadline time.Time) Classification {
	if errors.Is(err, context.DeadlineExceeded) && !time.Now().Before(ownDeadline) {
		return ClassifyError(&ruleengine.FailedError{Reason: string(provider.FailTimeout), Cause: err})
	}
	return ClassifyError(&ruleengine.FailedError{Reason: string(provider.FailCanceled), Cause: err})
}

// waitForOwnDeadlineOrCancel waits until either ownDeadline elapses (returning
// timeout) or ctx is done (classified via classifyWaiterContext). Used when a
// leader timed out and the waiter's remaining ctx deadline is shorter than the
// runtime's effective budget so taking leadership would convert a runtime
// timeout into caller cancellation. Caller guarantees ctx != nil.
func waitForOwnDeadlineOrCancel(ctx context.Context, ownDeadline time.Time) Classification {
	if err := ctx.Err(); err != nil {
		return classifyWaiterContext(err, ownDeadline)
	}
	rem := time.Until(ownDeadline)
	if rem <= 0 {
		return ClassifyError(&ruleengine.FailedError{Reason: string(provider.FailTimeout), Cause: context.DeadlineExceeded})
	}
	timer := time.NewTimer(rem)
	defer timer.Stop()
	select {
	case <-timer.C:
		return ClassifyError(&ruleengine.FailedError{Reason: string(provider.FailTimeout), Cause: context.DeadlineExceeded})
	case <-ctx.Done():
		return classifyWaiterContext(ctx.Err(), ownDeadline)
	}
}

func (e *Evaluator) evaluateSystemOneWithMemo(
	ctx context.Context,
	rawRule string,
	req systemone.Request,
	cfg evalConfig,
) (*systemone.Evaluation, Classification) {
	start := time.Now()
	memo := cfg.memo
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			// Match evaluateSystemOneUncached: an already-done caller context
			// at entry always classifies as canceled.
			return nil, ClassifyError(&ruleengine.FailedError{Reason: string(provider.FailCanceled), Cause: err})
		}
	}

	// Pure spec compilation to extract ContentHash, Inputs, Binding, Timeout, etc.
	// If this fails, fall through to the uncached path so existing compile errors
	// and dialect-disabled handling remain unchanged.
	contentEnv := SystemOneContentEnv()
	compiled, err := spec.Compile([]byte(rawRule), spec.CompileOptions{
		Kind:        spec.Kind(KindJudgments),
		Environment: &contentEnv,
	})
	if err != nil {
		return e.evaluateSystemOneUncached(ctx, rawRule, req, cfg)
	}

	requiredBudget := effectiveMemoBudget(compiled.Timeout, cfg.maxEvalTime)
	ownDeadline := start.Add(requiredBudget)
	key := buildMemoKey(memo.sendID, compiled, req.ProjectID, req.Input, cfg.memoMode, cfg.maxEvalTime)

	// Frozen snapshot path (shadow worker): read-only lookup, never blocks on
	// in-flight leaders, never mutates the snapshot, revalidates binding before reuse.
	if memo.frozen {
		memo.mu.Lock()
		ent := memo.published[key]
		memo.mu.Unlock()
		if ent != nil && isShadowReusableEntry(ent) && memo.revalidateBinding(ctx, compiled, req.ProjectID, ent.attempt) {
			return makeWaiterResult(ent, req)
		}
		return e.evaluateSystemOneUncached(ctx, rawRule, req, cfg)
	}

	// Live per-send singleflight path.
	for {
		if ctx != nil {
			if err := ctx.Err(); err != nil {
				return nil, classifyWaiterContext(err, ownDeadline)
			}
		}

		memo.mu.Lock()
		if ent, ok := memo.published[key]; ok {
			memo.mu.Unlock()
			if memo.revalidateBinding(ctx, compiled, req.ProjectID, ent.attempt) {
				return makeWaiterResult(ent, req)
			}
			// Binding changed since leader ran: evaluate fresh without reusing stale entry.
			return e.evaluateSystemOneUncached(ctx, rawRule, req, cfg)
		}

		if flight, ok := memo.inflight[key]; ok {
			memo.mu.Unlock()
			// Wait without holding memo.mu or any admission permit.
			if ctx != nil {
				select {
				case <-flight.done:
				case <-ctx.Done():
					return nil, classifyWaiterContext(ctx.Err(), ownDeadline)
				}
			} else {
				<-flight.done
			}
			if flight.published && flight.entry != nil {
				if memo.revalidateBinding(ctx, compiled, req.ProjectID, flight.entry.attempt) {
					return makeWaiterResult(flight.entry, req)
				}
				return e.evaluateSystemOneUncached(ctx, rawRule, req, cfg)
			}
			// Leader did not publish. Decline leadership ONLY when the leader
			// failed with a runtime timeout and our remaining ctx deadline is
			// shorter than the effective budget (so a fresh attempt would turn
			// timeout into caller cancellation). For all other unpublishable
			// reasons (capacity, budget_exhausted, zero-attempt, caller cancel),
			// continue and re-lead so every policy keeps baseline semantics.
			if flight.reason == string(provider.FailTimeout) && ctx != nil {
				if dl, hasDL := ctx.Deadline(); hasDL && time.Until(dl) < requiredBudget {
					return nil, waitForOwnDeadlineOrCancel(ctx, ownDeadline)
				}
			}
			continue
		}

		// Become leader for this key.
		flight := &memoFlight{done: make(chan struct{})}
		memo.inflight[key] = flight
		memo.mu.Unlock()

		eval, cls := e.evaluateSystemOneUncached(ctx, rawRule, req, cfg)

		memo.mu.Lock()
		if memo.inflight[key] == flight {
			delete(memo.inflight, key)
		}
		flight.reason = cls.Reason
		if isPublishableResult(eval, cls) {
			ent := &memoEntry{
				eval:    cloneEvaluation(eval),
				cls:     cloneClassification(cls),
				attempt: eval.Attempts[0],
			}
			flight.entry = ent
			flight.published = true
			memo.published[key] = cloneMemoEntry(ent)
		}
		close(flight.done)
		memo.mu.Unlock()

		return eval, cls
	}
}

func makeWaiterResult(ent *memoEntry, req systemone.Request) (*systemone.Evaluation, Classification) {
	evalCopy := cloneEvaluation(ent.eval)
	sourceEvalID := ""
	if evalCopy != nil {
		sourceEvalID = evalCopy.EvalID
		evalCopy.EvalID = newMemoEvalID()
		evalCopy.CacheHit = true
		evalCopy.SourceEvalID = sourceEvalID
		evalCopy.Usage = nil
		evalCopy.Rule.RuleID = strings.TrimSpace(req.RuleID)
		evalCopy.Rule.Revision = req.Revision
		if req.ElementIndex != nil {
			v := *req.ElementIndex
			evalCopy.Rule.ElementIndex = &v
		} else {
			evalCopy.Rule.ElementIndex = nil
		}
	}
	clsCopy := cloneClassification(ent.cls)
	clsCopy.Evaluation = evalCopy
	return evalCopy, clsCopy
}

func newMemoEvalID() string {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "memo-eval"
	}
	return "memo-" + hex.EncodeToString(b[:])
}

func cloneMemoEntry(ent *memoEntry) *memoEntry {
	if ent == nil {
		return nil
	}
	return &memoEntry{
		eval:    cloneEvaluation(ent.eval),
		cls:     cloneClassification(ent.cls),
		attempt: ent.attempt,
	}
}

func cloneClassification(cls Classification) Classification {
	out := cls
	out.Evaluation = cloneEvaluation(cls.Evaluation)
	if len(cls.Judgments.Flags) > 0 {
		out.Judgments.Flags = make(map[string]bool, len(cls.Judgments.Flags))
		maps.Copy(out.Judgments.Flags, cls.Judgments.Flags)
	}
	if len(cls.Judgments.Labels) > 0 {
		out.Judgments.Labels = make(map[string]string, len(cls.Judgments.Labels))
		maps.Copy(out.Judgments.Labels, cls.Judgments.Labels)
	}
	if len(cls.Judgments.Scores) > 0 {
		out.Judgments.Scores = make(map[string]float64, len(cls.Judgments.Scores))
		maps.Copy(out.Judgments.Scores, cls.Judgments.Scores)
	}
	return out
}

func cloneEvaluation(eval *systemone.Evaluation) *systemone.Evaluation {
	if eval == nil {
		return nil
	}
	cp := *eval
	if eval.Rule.ElementIndex != nil {
		v := *eval.Rule.ElementIndex
		cp.Rule.ElementIndex = &v
	}
	if eval.Auth != nil {
		authCp := *eval.Auth
		if eval.Auth.SecretRef != nil {
			sr := *eval.Auth.SecretRef
			authCp.SecretRef = &sr
		}
		if len(eval.Auth.Scopes) > 0 {
			authCp.Scopes = slices.Clone(eval.Auth.Scopes)
		}
		cp.Auth = &authCp
	}
	if len(eval.Answers) > 0 {
		cp.Answers = make(map[string]systemone.AnswerRecord, len(eval.Answers))
		for k, ans := range eval.Answers {
			ansCp := ans
			if ans.Bool != nil {
				v := *ans.Bool
				ansCp.Bool = &v
			}
			if ans.Choice != nil {
				v := *ans.Choice
				ansCp.Choice = &v
			}
			if ans.Level != nil {
				v := *ans.Level
				ansCp.Level = &v
			}
			if ans.ExpectedIndex != nil {
				v := *ans.ExpectedIndex
				ansCp.ExpectedIndex = &v
			}
			if len(ans.Distribution) > 0 {
				ansCp.Distribution = make(map[string]float64, len(ans.Distribution))
				maps.Copy(ansCp.Distribution, ans.Distribution)
			}
			if len(ans.Metrics) > 0 {
				ansCp.Metrics = maps.Clone(ans.Metrics)
			}
			if len(ans.NativeMetrics) > 0 {
				ansCp.NativeMetrics = maps.Clone(ans.NativeMetrics)
			}
			if len(ans.Notes) > 0 {
				ansCp.Notes = slices.Clone(ans.Notes)
			}
			cp.Answers[k] = ansCp
		}
	}
	if len(eval.Uncertainty) > 0 {
		cp.Uncertainty = slices.Clone(eval.Uncertainty)
	}
	if eval.Usage != nil {
		u := *eval.Usage
		cp.Usage = &u
	}
	if len(eval.Notes) > 0 {
		cp.Notes = slices.Clone(eval.Notes)
	}
	if len(eval.Attempts) > 0 {
		cp.Attempts = slices.Clone(eval.Attempts)
	}
	if len(eval.Input.Fields) > 0 {
		cp.Input.Fields = slices.Clone(eval.Input.Fields)
	}
	return &cp
}

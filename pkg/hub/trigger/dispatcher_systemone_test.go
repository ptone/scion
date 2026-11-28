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

package trigger_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/GoogleCloudPlatform/scion/pkg/celengine"
	"github.com/GoogleCloudPlatform/scion/pkg/durablequeue"
	"github.com/GoogleCloudPlatform/scion/pkg/hub/hubfire"
	"github.com/GoogleCloudPlatform/scion/pkg/hub/trigger"
	"github.com/GoogleCloudPlatform/scion/pkg/ruleengine"
	"github.com/GoogleCloudPlatform/scion/pkg/store"
	"github.com/GoogleCloudPlatform/scion/pkg/systemone"
	"github.com/GoogleCloudPlatform/scion/pkg/systemone/egress"
	"github.com/GoogleCloudPlatform/scion/pkg/systemone/provider"
	soruntime "github.com/GoogleCloudPlatform/scion/pkg/systemone/runtime"
	"github.com/GoogleCloudPlatform/scion/pkg/systemone/spec"
	"github.com/GoogleCloudPlatform/scion/pkg/triggerrule"
)

type recordedModelCall struct {
	QuestionID string
	Input      []provider.InputField
	Instance   string
	StartedAt  time.Time
	EndedAt    time.Time
}

type recordingSystemOneAdapter struct {
	mu          sync.Mutex
	calls       []recordedModelCall
	inFlight    int
	maxInFlight int
	invokeFn    func(ctx context.Context, t provider.Transport, inst provider.InstanceView, req provider.ModelRequest) (provider.ModelResult, error)
}

func (a *recordingSystemOneAdapter) Name() string    { return "recording_test" }
func (a *recordingSystemOneAdapter) Version() string { return "v1" }

func (a *recordingSystemOneAdapter) Capabilities(provider.InstanceView) (provider.Capabilities, error) {
	return provider.Capabilities{
		Primitives: map[string]spec.PrimitiveLimits{
			spec.PrimitiveBinary: {Supported: true, DistributionCompleteness: spec.CompletenessComplete},
			spec.PrimitiveChoice: {Supported: true, MinOptions: 2, MaxOptions: 16, DistributionCompleteness: spec.CompletenessComplete},
		},
		MaxQuestionsPerRequest: 8,
		MaxInputTokens:         65536,
		Truncation:             provider.TruncationNever,
		ReturnsDistribution:    true,
		Abstains:               true,
		SideEffectFree:         true,
		AuthScheme:             provider.AuthScheme{Kind: provider.AuthKindBearer},
		SupportedAuth: map[string]provider.AuthScheme{
			provider.InstanceAuthAPIKey: {Kind: provider.AuthKindBearer},
		},
	}, nil
}

func (a *recordingSystemOneAdapter) Validate(provider.InstanceView, provider.RuleSpec) error {
	return nil
}

func (a *recordingSystemOneAdapter) Invoke(ctx context.Context, t provider.Transport, inst provider.InstanceView, req provider.ModelRequest) (provider.ModelResult, error) {
	a.mu.Lock()
	a.inFlight++
	if a.inFlight > a.maxInFlight {
		a.maxInFlight = a.inFlight
	}
	a.mu.Unlock()
	start := time.Now()
	defer func() {
		a.mu.Lock()
		a.inFlight--
		a.mu.Unlock()
	}()

	res, err := a.invokeFn(ctx, t, inst, req)

	qID := ""
	if len(req.Rule.Questions) > 0 {
		qID = req.Rule.Questions[0].ID
	}
	a.mu.Lock()
	a.calls = append(a.calls, recordedModelCall{
		QuestionID: qID,
		Input:      append([]provider.InputField(nil), req.Input...),
		Instance:   inst.Name,
		StartedAt:  start,
		EndedAt:    time.Now(),
	})
	a.mu.Unlock()
	return res, err
}

func (a *recordingSystemOneAdapter) snapshotCalls() ([]recordedModelCall, int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]recordedModelCall(nil), a.calls...), a.maxInFlight
}

func (a *recordingSystemOneAdapter) reset() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.calls = nil
	a.maxInFlight = 0
}

type systemOneTestHarness struct {
	t           *testing.T
	adapter     *recordingSystemOneAdapter
	httpSrv     *httptest.Server
	secretReads atomic.Int32
	tokenMints  atomic.Int32
	httpReqs    atomic.Int32
	secretMu    sync.Mutex
	secretProjs []string
	enabled     atomic.Bool
	allowlist   map[string][]string
	allowMu     sync.RWMutex
	instances   []soruntime.Instance
	instMu      sync.RWMutex
	engine      *ruleengine.Engine
	rules       *triggerrule.Rules
	rt          *soruntime.Runtime
	st          *memTriggerStore
	core        *fakeCore
	disp        *trigger.Dispatcher
}

func (h *systemOneTestHarness) AllowedInstances(_ context.Context, projectID string) ([]string, error) {
	h.allowMu.RLock()
	defer h.allowMu.RUnlock()
	return append([]string(nil), h.allowlist[projectID]...), nil
}

func (h *systemOneTestHarness) setAllowed(projectID string, names ...string) {
	h.allowMu.Lock()
	defer h.allowMu.Unlock()
	h.allowlist[projectID] = append([]string(nil), names...)
}

func (h *systemOneTestHarness) GetInstance(_ context.Context, name string) (soruntime.Instance, bool, error) {
	h.instMu.RLock()
	defer h.instMu.RUnlock()
	for _, inst := range h.instances {
		if inst.Name == name {
			return inst, true, nil
		}
	}
	return soruntime.Instance{}, false, nil
}

func (h *systemOneTestHarness) ListInstances(_ context.Context) ([]soruntime.Instance, error) {
	h.instMu.RLock()
	defer h.instMu.RUnlock()
	return append([]soruntime.Instance(nil), h.instances...), nil
}

func (h *systemOneTestHarness) setInstances(insts ...soruntime.Instance) {
	h.instMu.Lock()
	defer h.instMu.Unlock()
	h.instances = append([]soruntime.Instance(nil), insts...)
}

func (h *systemOneTestHarness) findFire(eventID, triggerID string) *store.TriggerFire {
	h.st.mu.Lock()
	defer h.st.mu.Unlock()
	for _, f := range h.st.byFireID {
		if f.EventID == eventID && f.TriggerID == triggerID {
			cp := *f
			return &cp
		}
	}
	return nil
}

func (h *systemOneTestHarness) resetCounters() {
	h.secretReads.Store(0)
	h.tokenMints.Store(0)
	h.httpReqs.Store(0)
	h.secretMu.Lock()
	h.secretProjs = nil
	h.secretMu.Unlock()
	h.adapter.reset()
}

func newSystemOneTestHarness(t *testing.T, dispCfgMod func(*trigger.DispatcherConfig)) *systemOneTestHarness {
	t.Helper()
	h := &systemOneTestHarness{
		t:         t,
		allowlist: make(map[string][]string),
	}
	h.enabled.Store(true)

	h.httpSrv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.httpReqs.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(h.httpSrv.Close)

	h.adapter = &recordingSystemOneAdapter{
		invokeFn: func(ctx context.Context, tr provider.Transport, inst provider.InstanceView, req provider.ModelRequest) (provider.ModelResult, error) {
			httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, inst.Endpoint+"/decide", strings.NewReader(`{}`))
			if err != nil {
				return provider.ModelResult{}, err
			}
			resp, err := tr.Do(httpReq)
			if err != nil {
				return provider.ModelResult{}, err
			}
			_ = resp.Body.Close()
			bTrue := true
			qID := req.Rule.Questions[0].ID
			return provider.ModelResult{
				ResolvedModel: inst.Model,
				HTTPStatus:    resp.StatusCode,
				Answers: map[string]provider.Answer{
					qID: {
						QuestionID:   qID,
						Primitive:    spec.PrimitiveBinary,
						Status:       provider.AnswerStatusAnswered,
						Bool:         &bTrue,
						Distribution: map[string]float64{"true": 0.92, "false": 0.08},
					},
				},
			}, nil
		},
	}

	reg := provider.NewRegistry()
	require.NoError(t, reg.Register(h.adapter))

	h.setInstances(soruntime.Instance{
		ID:       "inst-1",
		Name:     "primary",
		Adapter:  h.adapter.Name(),
		Model:    "test-model-v1",
		Endpoint: h.httpSrv.URL,
		Auth:     soruntime.InstanceAuth{Kind: provider.InstanceAuthAPIKey},
		Enabled:  true,
		Revision: 3,
	})

	rt := soruntime.New(soruntime.Config{
		Registry:         reg,
		Instances:        h,
		ProjectAllowlist: h,
		Credentials: func(ctx context.Context, inst soruntime.Instance) (string, error) {
			h.secretReads.Add(1)
			proj := systemone.ProjectIDFromContext(ctx)
			h.secretMu.Lock()
			h.secretProjs = append(h.secretProjs, proj)
			h.secretMu.Unlock()
			return "test-secret-for-" + proj, nil
		},
		TokenSource: func(ctx context.Context, inst soruntime.Instance) (string, error) {
			h.tokenMints.Add(1)
			return "test-adc-token", nil
		},
		MaxEvalTime:           5 * time.Second,
		MaxInFlightPerProject: 4,
		AdmissionWait:         50 * time.Millisecond,
	}, soruntime.WithEgressOptionsForTesting(egress.WithLoopbackServerForTesting(h.httpSrv.URL, h.httpSrv.Client())))
	h.rt = rt

	celEng, err := celengine.New(triggerrule.Env(), triggerrule.Stage1Limits(), triggerrule.Stage1CacheConfig())
	require.NoError(t, err)
	celDial, err := ruleengine.NewCELDialect(celEng)
	require.NoError(t, err)

	eng := ruleengine.NewEngine()
	require.NoError(t, eng.Register(celDial))
	soDial := systemone.NewDialect(systemone.DialectConfig{
		Runtime:     rt,
		MaxEvalTime: 5 * time.Second,
		Enabled:     func() bool { return h.enabled.Load() },
	})
	require.NoError(t, eng.Register(soDial))
	require.NoError(t, eng.AllowConsumerDialects(
		triggerrule.ConsumerName,
		ruleengine.Grant(ruleengine.DialectCEL),
		ruleengine.GrantIO(systemone.DialectName),
	))
	require.NoError(t, systemone.RegisterConsumerEnvironment(eng, triggerrule.ConsumerName, trigger.SystemOneContentEnv()))
	h.engine = eng

	rules, err := triggerrule.New(eng)
	require.NoError(t, err)
	h.rules = rules

	h.st = newMemTriggerStore()
	h.core = &fakeCore{}
	h.core.enabled.Store(true)

	cfg := trigger.DispatcherConfig{
		Core:  h.core,
		Store: h.st,
		Rules: rules,
	}
	if dispCfgMod != nil {
		dispCfgMod(&cfg)
	}
	disp, err := trigger.NewDispatcher(cfg)
	require.NoError(t, err)
	h.disp = disp
	return h
}

func makeBinarySystemOneRule(instance, qID, inputs string, payloadPaths string) string {
	pp := ""
	if payloadPaths != "" {
		pp = `,"payload_paths":` + payloadPaths
	}
	return `{"inputs":` + inputs + pp + `,"spec":{"version":1,"binding":{"instance":"` + instance + `"},"questions":[{"id":"` + qID + `","type":"binary","instructions":"Decide yes or no."}],"output":{"question":"` + qID + `"}}}`
}

func makeUserTrigger(projectID, sourceID string, rules []store.TriggerMatchRule) *store.Trigger {
	return &store.Trigger{
		ID:                   uuid.NewString(),
		ProjectID:            projectID,
		Name:                 "so-trig",
		Status:               store.TriggerStatusActive,
		SourceKind:           store.TriggerSourceKindWebhook,
		SourceID:             sourceID,
		MatchTypes:           []string{"webhook.received"},
		MatchRules:           rules,
		ActionType:           store.TriggerActionMessage,
		ActionPayload:        `{"agentId":"agent-1","message":"hello"}`,
		SubjectMode:          store.TriggerSubjectModeNew,
		InitiatorAttribution: store.InitiatorAttribution{InitiatorPrincipalKind: "user", InitiatorPrincipalID: "user-1", InitiatorCredentialKind: store.InitiatorCredentialKindSession, AuthorizationRevision: 2},
	}
}

func dispatchOneEvent(t *testing.T, ctx context.Context, h *systemOneTestHarness, ev *store.TriggerEvent) durablequeue.Result {
	t.Helper()
	h.st.mu.Lock()
	h.st.events[ev.ID] = ev
	h.st.mu.Unlock()
	payload, err := json.Marshal(trigger.EventQueuePayload{
		Version: trigger.EventQueuePayloadVersion,
		EventID: ev.ID,
	})
	require.NoError(t, err)
	return h.disp.HandleItem(ctx, durablequeue.Item{
		Kind:     trigger.EventQueueKind,
		DedupKey: ev.ID,
		Payload:  payload,
	}, nopLease{})
}

// Acceptance tests 1 & 2: mixed list semantics (true, false, uncertain, failed)
// and strict author order (never parallel).
func TestDispatcherSystemOne_ListSemanticsAndStrictAuthorOrder(t *testing.T) {
	h := newSystemOneTestHarness(t, nil)
	projID := "proj-order"
	h.setAllowed(projID, "primary")

	// Sequence of two System One elements after a CEL gate: both pass in strict order.
	tr := makeUserTrigger(projID, "src-1", []store.TriggerMatchRule{
		{Dialect: "cel", Expr: `event.type == "webhook.received"`},
		{Dialect: "system_one", Expr: makeBinarySystemOneRule("primary", "q_first", `["event.subject"]`, "")},
		{Dialect: "system_one", Expr: makeBinarySystemOneRule("primary", "q_second", `["event.type"]`, "")},
	})
	h.st.triggers = []*store.Trigger{tr}

	ev := &store.TriggerEvent{
		ID:         uuid.NewString(),
		ProjectID:  projID,
		SourceKind: store.TriggerSourceKindWebhook,
		SourceID:   "src-1",
		Type:       "webhook.received",
		Subject:    "deploy prod",
		Status:     store.TriggerEventStatusPending,
	}
	res := dispatchOneEvent(t, context.Background(), h, ev)
	require.True(t, res.Done, "res=%+v", res)

	calls, maxConc := h.adapter.snapshotCalls()
	require.Len(t, calls, 2)
	assert.Equal(t, 1, maxConc, "System One elements must never execute in parallel")
	assert.Equal(t, "q_first", calls[0].QuestionID)
	assert.Equal(t, "q_second", calls[1].QuestionID)
	assert.False(t, calls[1].StartedAt.Before(calls[0].EndedAt), "second call must start after first ends")

	// Verify fire recorded with matched_through="3/3", decided_dialect="system_one", and compact provenance.
	fire := h.findFire(ev.ID, tr.ID)
	require.NotNil(t, fire)
	assert.Equal(t, store.TriggerFireStatusFired, fire.Status)
	assert.Equal(t, "3/3", fire.MatchedThrough)
	assert.Equal(t, "system_one", fire.DecidedDialect)
	var parsedTrace triggerrule.RuleTrace
	require.NoError(t, json.Unmarshal([]byte(fire.RuleTrace), &parsedTrace))
	require.Len(t, parsedTrace.Elements, 3)
	assert.Equal(t, "cel", parsedTrace.Elements[0].Dialect)
	assert.Equal(t, "system_one", parsedTrace.Elements[1].Dialect)
	assert.Equal(t, "success", parsedTrace.Elements[1].ProvenanceData["class"])
	assert.Equal(t, "primary", parsedTrace.Elements[1].ProvenanceData["instance"])
	assert.Equal(t, true, parsedTrace.Elements[1].ProvenanceData["answer"])
	assert.Equal(t, 0.92, parsedTrace.Elements[1].ProvenanceData["p_top"])
	assert.NotEmpty(t, parsedTrace.Elements[1].ProvenanceData["eval_id"])
	require.NotNil(t, parsedTrace.BudgetRemainingMs)

	// False on element 2 stops as non-match; element 3 never runs.
	h.resetCounters()
	h.adapter.invokeFn = func(ctx context.Context, trp provider.Transport, inst provider.InstanceView, req provider.ModelRequest) (provider.ModelResult, error) {
		bFalse := false
		qID := req.Rule.Questions[0].ID
		return provider.ModelResult{
			ResolvedModel: inst.Model,
			Answers: map[string]provider.Answer{
				qID: {
					QuestionID:   qID,
					Primitive:    spec.PrimitiveBinary,
					Status:       provider.AnswerStatusAnswered,
					Bool:         &bFalse,
					Distribution: map[string]float64{"true": 0.05, "false": 0.95},
				},
			},
		}, nil
	}
	evFalse := &store.TriggerEvent{
		ID: uuid.NewString(), ProjectID: projID, SourceKind: store.TriggerSourceKindWebhook,
		SourceID: "src-1", Type: "webhook.received", Subject: "deploy", Status: store.TriggerEventStatusPending,
	}
	res = dispatchOneEvent(t, context.Background(), h, evFalse)
	require.True(t, res.Done)
	calls, _ = h.adapter.snapshotCalls()
	require.Len(t, calls, 1, "element 3 must not execute after false on element 2")
	assert.Nil(t, h.findFire(evFalse.ID, tr.ID), "false is non-match (no fire row)")

	// Uncertain (abstain) on element 2 stops and records skipped "rule uncertain" with unexecuted=[3].
	h.resetCounters()
	h.adapter.invokeFn = func(ctx context.Context, trp provider.Transport, inst provider.InstanceView, req provider.ModelRequest) (provider.ModelResult, error) {
		qID := req.Rule.Questions[0].ID
		return provider.ModelResult{
			ResolvedModel: inst.Model,
			Answers: map[string]provider.Answer{
				qID: {QuestionID: qID, Primitive: spec.PrimitiveBinary, Status: provider.AnswerStatusAbstained},
			},
		}, nil
	}
	evUnc := &store.TriggerEvent{
		ID: uuid.NewString(), ProjectID: projID, SourceKind: store.TriggerSourceKindWebhook,
		SourceID: "src-1", Type: "webhook.received", Subject: "ambiguous", Status: store.TriggerEventStatusPending,
	}
	res = dispatchOneEvent(t, context.Background(), h, evUnc)
	require.True(t, res.Done)
	calls, _ = h.adapter.snapshotCalls()
	require.Len(t, calls, 1)
	fireUnc := h.findFire(evUnc.ID, tr.ID)
	require.NotNil(t, fireUnc)
	assert.Equal(t, store.TriggerFireStatusSkipped, fireUnc.Status)
	assert.Equal(t, trigger.ErrFireRuleUncertain, fireUnc.Error)
	assert.Equal(t, "2/3", fireUnc.MatchedThrough)
	assert.Equal(t, "system_one", fireUnc.DecidedDialect)
	var uncTrace triggerrule.RuleTrace
	require.NoError(t, json.Unmarshal([]byte(fireUnc.RuleTrace), &uncTrace))
	assert.Equal(t, []int{3}, uncTrace.Unexecuted)
	require.Len(t, uncTrace.Elements, 2)
	assert.Equal(t, "uncertain", uncTrace.Elements[1].ProvenanceData["class"])
	assert.Equal(t, provider.UncertainAbstained, uncTrace.Elements[1].ProvenanceData["reason"])

	// Failed (provider error) on element 2 stops and records skipped "rule evaluation failed" with EvaluationFrom provenance.
	h.resetCounters()
	h.adapter.invokeFn = func(ctx context.Context, trp provider.Transport, inst provider.InstanceView, req provider.ModelRequest) (provider.ModelResult, error) {
		return provider.ModelResult{}, &provider.ProviderError{Reason: provider.FailUnavailable, Message: "upstream 503"}
	}
	evFail := &store.TriggerEvent{
		ID: uuid.NewString(), ProjectID: projID, SourceKind: store.TriggerSourceKindWebhook,
		SourceID: "src-1", Type: "webhook.received", Subject: "err", Status: store.TriggerEventStatusPending,
	}
	res = dispatchOneEvent(t, context.Background(), h, evFail)
	require.True(t, res.Done)
	fireFail := h.findFire(evFail.ID, tr.ID)
	require.NotNil(t, fireFail)
	assert.Equal(t, store.TriggerFireStatusSkipped, fireFail.Status)
	assert.Equal(t, trigger.ErrFireRuleEvaluationFailed, fireFail.Error)
	assert.Equal(t, "2/3", fireFail.MatchedThrough)
	var failTrace triggerrule.RuleTrace
	require.NoError(t, json.Unmarshal([]byte(fireFail.RuleTrace), &failTrace))
	assert.Equal(t, []int{3}, failTrace.Unexecuted)
	require.Len(t, failTrace.Elements, 2)
	assert.Equal(t, "failed", failTrace.Elements[1].ProvenanceData["class"])
	assert.Equal(t, "unavailable", failTrace.Elements[1].ProvenanceData["reason"])
}

// Acceptance test 3 & Requirement 1: Content environment, payload_paths narrowing,
// and authorized ProjectID (conflicting event project_id ignored).
func TestDispatcherSystemOne_AuthorizedProjectIDAndPayloadPaths(t *testing.T) {
	h := newSystemOneTestHarness(t, nil)
	trigProj := "proj-authorized-owner"
	attackerProj := "proj-attacker-spoofed"
	h.setAllowed(trigProj, "primary")
	// attackerProj is NOT allowlisted.

	tr := makeUserTrigger(trigProj, "src-1", []store.TriggerMatchRule{
		{Dialect: "system_one", Expr: makeBinarySystemOneRule("primary", "q1", `["event.payload"]`, `["pull_request.title"]`)},
	})
	h.st.triggers = []*store.Trigger{tr}

	ev := &store.TriggerEvent{
		ID:         uuid.NewString(),
		ProjectID:  trigProj,
		SourceKind: store.TriggerSourceKindWebhook,
		SourceID:   "src-1",
		Type:       "webhook.received",
		Subject:    "pr opened",
		Attributes: map[string]string{"project_id": attackerProj},
		Payload:    `{"project_id":"` + attackerProj + `","secret_leak":"TOP_SECRET","pull_request":{"title":"Fix bug","body":"ignored"}}`,
		Status:     store.TriggerEventStatusPending,
	}
	res := dispatchOneEvent(t, context.Background(), h, ev)
	require.True(t, res.Done)

	// Authorized ProjectID was passed to CredentialSource (never attackerProj).
	h.secretMu.Lock()
	projs := append([]string(nil), h.secretProjs...)
	h.secretMu.Unlock()
	require.Equal(t, []string{trigProj}, projs)

	// Only narrowed payload_paths reached the provider; secret_leak and body were stripped.
	calls, _ := h.adapter.snapshotCalls()
	require.Len(t, calls, 1)
	require.Len(t, calls[0].Input, 1)
	assert.Equal(t, "event.payload", calls[0].Input[0].Name)
	assert.Equal(t, `{"pull_request":{"title":"Fix bug"}}`, calls[0].Input[0].Value)
	assert.NotContains(t, calls[0].Input[0].Value, "TOP_SECRET")
	assert.NotContains(t, calls[0].Input[0].Value, attackerProj)
}

// Acceptance test 4 & Requirement 4: Zero-I/O refusals with positive control.
func TestDispatcherSystemOne_ZeroIORefusalsWithPositiveControl(t *testing.T) {
	h := newSystemOneTestHarness(t, nil)
	projID := "proj-zero-io"
	ruleExpr := makeBinarySystemOneRule("primary", "q1", `["event.payload"]`, "")
	tr := makeUserTrigger(projID, "src-1", []store.TriggerMatchRule{{Dialect: "system_one", Expr: ruleExpr}})
	h.st.triggers = []*store.Trigger{tr}

	newEv := func(payload string) *store.TriggerEvent {
		return &store.TriggerEvent{
			ID: uuid.NewString(), ProjectID: projID, SourceKind: store.TriggerSourceKindWebhook,
			SourceID: "src-1", Type: "webhook.received", Subject: "s", Payload: payload,
			Status: store.TriggerEventStatusPending,
		}
	}

	// Positive control: allowlisted, enabled, user-authored, experiment ON -> +1 secret read, +1 provider request.
	h.setAllowed(projID, "primary")
	h.resetCounters()
	res := dispatchOneEvent(t, context.Background(), h, newEv(`{"ok":true}`))
	require.True(t, res.Done)
	assert.Equal(t, int32(1), h.secretReads.Load(), "positive control must read credential once")
	assert.Equal(t, int32(1), h.httpReqs.Load(), "positive control must make 1 provider request")

	assertZeroIO := func(t *testing.T, label string) {
		t.Helper()
		assert.Equal(t, int32(0), h.secretReads.Load(), "%s: expected 0 secret reads", label)
		assert.Equal(t, int32(0), h.tokenMints.Load(), "%s: expected 0 token mints", label)
		assert.Equal(t, int32(0), h.httpReqs.Load(), "%s: expected 0 provider requests", label)
	}

	// Refusal 1: not on project allowlist (D1 empty default).
	h.setAllowed(projID)
	h.resetCounters()
	ev1 := newEv(`{"ok":true}`)
	dispatchOneEvent(t, context.Background(), h, ev1)
	assertZeroIO(t, "not_allowlisted")
	f1 := h.findFire(ev1.ID, tr.ID)
	require.NotNil(t, f1)
	assert.Equal(t, store.TriggerFireStatusSkipped, f1.Status)
	assert.Equal(t, trigger.ErrFireRuleEvaluationFailed, f1.Error)

	// Refusal 2: disabled instance.
	h.setAllowed(projID, "primary")
	h.setInstances(soruntime.Instance{
		ID: "inst-1", Name: "primary", Adapter: h.adapter.Name(), Model: "test-model-v1",
		Endpoint: h.httpSrv.URL, Auth: soruntime.InstanceAuth{Kind: provider.InstanceAuthAPIKey},
		Enabled: false, Revision: 4,
	})
	h.resetCounters()
	ev2 := newEv(`{"ok":true}`)
	dispatchOneEvent(t, context.Background(), h, ev2)
	assertZeroIO(t, "disabled_instance")
	f2 := h.findFire(ev2.ID, tr.ID)
	require.NotNil(t, f2)
	assert.Equal(t, store.TriggerFireStatusSkipped, f2.Status)

	// Restore enabled instance.
	h.setInstances(soruntime.Instance{
		ID: "inst-1", Name: "primary", Adapter: h.adapter.Name(), Model: "test-model-v1",
		Endpoint: h.httpSrv.URL, Auth: soruntime.InstanceAuth{Kind: provider.InstanceAuthAPIKey},
		Enabled: true, Revision: 5,
	})

	// Refusal 3: agent-authored trigger (D2 fire-time re-check).
	agentTr := makeUserTrigger(projID, "src-1", []store.TriggerMatchRule{{Dialect: "system_one", Expr: ruleExpr}})
	agentTr.InitiatorPrincipalKind = "agent"
	agentTr.InitiatorCredentialKind = store.InitiatorCredentialKindAgent
	h.st.triggers = []*store.Trigger{agentTr}
	h.resetCounters()
	ev3 := newEv(`{"ok":true}`)
	dispatchOneEvent(t, context.Background(), h, ev3)
	assertZeroIO(t, "agent_authored_D2")
	f3 := h.findFire(ev3.ID, agentTr.ID)
	require.NotNil(t, f3)
	assert.Equal(t, store.TriggerFireStatusRefused, f3.Status)
	h.st.triggers = []*store.Trigger{tr}

	// Refusal 4: experiment hub.system_one OFF.
	h.enabled.Store(false)
	h.resetCounters()
	ev4 := newEv(`{"ok":true}`)
	dispatchOneEvent(t, context.Background(), h, ev4)
	assertZeroIO(t, "experiment_off")
	f4 := h.findFire(ev4.ID, tr.ID)
	require.NotNil(t, f4)
	assert.Equal(t, store.TriggerFireStatusSkipped, f4.Status)
	h.enabled.Store(true)

	// Refusal 5: oversize event.payload with default on_oversize: reject.
	h.resetCounters()
	bigPayload := `{"data":"` + strings.Repeat("a", 25000) + `"}`
	ev5 := newEv(bigPayload)
	dispatchOneEvent(t, context.Background(), h, ev5)
	assertZeroIO(t, "oversize_reject")
	f5 := h.findFire(ev5.ID, tr.ID)
	require.NotNil(t, f5)
	assert.Equal(t, store.TriggerFireStatusSkipped, f5.Status)
}

type stubDQStore struct {
	store.DurableQueueStore
}

// Acceptance test 7 & DISPATCH ADDENDUM Condition 2:
// Startup validation, SystemOneAllowance cap, budget exhaustion across candidates,
// and cancelling event ctx during EvaluatePending (no late success, no fire).
func TestDispatcherSystemOne_BudgetValidationAndCancelInFlight(t *testing.T) {
	// Cap > 5s rejected by NewDispatcher.
	st := newMemTriggerStore()
	dqSt := stubDQStore{}
	fc := &fakeCore{}
	_, err := trigger.NewDispatcher(trigger.DispatcherConfig{
		Core:               fc,
		Store:              st,
		SystemOneAllowance: 6 * time.Second,
	})
	require.Error(t, err)

	// Startup validation in NewDrainer rejects HandlerTimeout <= ruleBudget + allowance + perFire.
	disp, err := trigger.NewDispatcher(trigger.DispatcherConfig{
		Core:               fc,
		Store:              st,
		RuleBudget:         time.Second,
		SystemOneAllowance: 2 * time.Second,
		PerFireTimeout:     30 * time.Second,
	})
	require.NoError(t, err)
	_, err = disp.NewDrainer(trigger.DrainerOptions{
		QueueStore:     dqSt,
		HandlerTimeout: 30 * time.Second, // <= 33s
		LeaseDuration:  180 * time.Second,
	})
	require.Error(t, err)
	_, err = disp.NewDrainer(trigger.DrainerOptions{
		QueueStore:     dqSt,
		HandlerTimeout: 180 * time.Second, // >= LeaseDuration
		LeaseDuration:  180 * time.Second,
	})
	require.Error(t, err)

	// DISPATCH ADDENDUM Condition 2: cancelling event ctx during EvaluatePending
	// records a terminal canceled outcome with NO late success and NO fire.
	h := newSystemOneTestHarness(t, nil)
	projID := "proj-cancel"
	h.setAllowed(projID, "primary")

	entered := make(chan struct{})
	release := make(chan struct{})
	h.adapter.invokeFn = func(ctx context.Context, trp provider.Transport, inst provider.InstanceView, req provider.ModelRequest) (provider.ModelResult, error) {
		close(entered)
		<-release
		// Simulate a misbehaving adapter that ignores ctx and returns a late success!
		bTrue := true
		qID := req.Rule.Questions[0].ID
		return provider.ModelResult{
			ResolvedModel: inst.Model,
			Answers: map[string]provider.Answer{
				qID: {QuestionID: qID, Primitive: spec.PrimitiveBinary, Status: provider.AnswerStatusAnswered, Bool: &bTrue},
			},
		}, nil
	}

	tr := makeUserTrigger(projID, "src-1", []store.TriggerMatchRule{
		{Dialect: "system_one", Expr: makeBinarySystemOneRule("primary", "q1", `["event.subject"]`, "")},
	})
	h.st.triggers = []*store.Trigger{tr}

	ev := &store.TriggerEvent{
		ID: uuid.NewString(), ProjectID: projID, SourceKind: store.TriggerSourceKindWebhook,
		SourceID: "src-1", Type: "webhook.received", Subject: "cancel-me", Status: store.TriggerEventStatusPending,
	}

	ctx, cancel := context.WithCancel(context.Background())
	resCh := make(chan durablequeue.Result, 1)
	go func() {
		resCh <- dispatchOneEvent(t, ctx, h, ev)
	}()

	<-entered
	cancel()
	// Give cancellation a moment to propagate, then release the late success.
	time.Sleep(15 * time.Millisecond)
	close(release)

	res := <-resCh
	assert.False(t, res.Done, "canceled evaluation must not mark event done")
	assert.Error(t, res.Err)
	assert.Nil(t, h.findFire(ev.ID, tr.ID), "no fire may be recorded after in-flight cancellation / late success")
}

// Acceptance test 8: 4 KiB rule_trace truncation drops earliest provenance objects first
// while preserving {i, dialect, class}.
func TestEncodeRuleTrace_SystemOne4KiBTruncation(t *testing.T) {
	elems := make([]triggerrule.RuleTraceElement, 0, 8)
	for i := 1; i <= 8; i++ {
		elems = append(elems, triggerrule.RuleTraceElement{
			Index:      i,
			Dialect:    "system_one",
			Class:      triggerrule.MatchClassMatched,
			DurationMs: 120,
			ProvenanceData: map[string]any{
				"class":          "success",
				"instance":       "primary-instance-" + strings.Repeat("a", 80),
				"instance_rev":   3,
				"resolved_model": "gemini-2.5-flash-" + strings.Repeat("b", 80),
				"answer":         true,
				"p_top":          0.91,
				"eval_id":        "eval-" + strings.Repeat("0", 32),
				"elapsed_ms":     120,
				"truncated":      []string{"event.payload", "event.attributes", strings.Repeat("c", 300)},
			},
		})
	}
	rem := int64(1500)
	encoded := triggerrule.EncodeRuleTrace(triggerrule.RuleTrace{
		Elements:          elems,
		BudgetRemainingMs: &rem,
	})
	require.LessOrEqual(t, len(encoded), triggerrule.MaxRuleTraceBytes)

	var parsed triggerrule.RuleTrace
	require.NoError(t, json.Unmarshal([]byte(encoded), &parsed))
	require.Len(t, parsed.Elements, 8, "all {i, dialect, class} elements must be retained")
	assert.Nil(t, parsed.Elements[0].ProvenanceData, "earliest element provenance must be dropped first")
	assert.NotNil(t, parsed.Elements[7].ProvenanceData, "latest deciding element provenance must be retained")
	assert.Equal(t, 1, parsed.Elements[0].Index)
	assert.Equal(t, "system_one", parsed.Elements[0].Dialect)
	assert.Equal(t, triggerrule.MatchClassMatched, parsed.Elements[0].Class)
}

// Acceptance test 7 (budget exhaustion + L1 late result + CEL-only vs System One allowance)
// and Acceptance test 4 (budget exhausted zero-I/O + capacity zero-I/O)
// and Acceptance test 9 (redelivery without memo & duplicate fire claim).
func TestDispatcherSystemOne_BudgetExhaustionL1CapacityAndRedelivery(t *testing.T) {
	// 1. L1 post-return deadline check & budget exhaustion across two candidates:
	// Small total budget (40ms ruleBudget + 40ms allowance = 80ms).
	// Candidate 1's System One call takes 120ms and returns a late success;
	// L1 must reject it (no fire, skipped "rule budget exhausted") and Candidate 2
	// must skip with "rule budget exhausted" and ZERO provider requests.
	h := newSystemOneTestHarness(t, func(cfg *trigger.DispatcherConfig) {
		cfg.RuleBudget = 40 * time.Millisecond
		cfg.SystemOneAllowance = 40 * time.Millisecond
	})
	projID := "proj-budget"
	h.setAllowed(projID, "primary")

	h.adapter.invokeFn = func(ctx context.Context, trp provider.Transport, inst provider.InstanceView, req provider.ModelRequest) (provider.ModelResult, error) {
		// Ignore ctx cancellation and sleep past the 80ms aggregate budget to exercise L1.
		time.Sleep(120 * time.Millisecond)
		bTrue := true
		qID := req.Rule.Questions[0].ID
		return provider.ModelResult{
			ResolvedModel: inst.Model,
			Answers: map[string]provider.Answer{
				qID: {QuestionID: qID, Primitive: spec.PrimitiveBinary, Status: provider.AnswerStatusAnswered, Bool: &bTrue},
			},
		}, nil
	}

	tr1 := makeUserTrigger(projID, "src-1", []store.TriggerMatchRule{
		{Dialect: "system_one", Expr: makeBinarySystemOneRule("primary", "q1", `["event.subject"]`, "")},
	})
	tr2 := makeUserTrigger(projID, "src-1", []store.TriggerMatchRule{
		{Dialect: "system_one", Expr: makeBinarySystemOneRule("primary", "q2", `["event.subject"]`, "")},
	})
	h.st.triggers = []*store.Trigger{tr1, tr2}

	ev := &store.TriggerEvent{
		ID: uuid.NewString(), ProjectID: projID, SourceKind: store.TriggerSourceKindWebhook,
		SourceID: "src-1", Type: "webhook.received", Subject: "slow", Status: store.TriggerEventStatusPending,
	}
	res := dispatchOneEvent(t, context.Background(), h, ev)
	require.True(t, res.Done)

	// Only candidate 1 ran; candidate 2 saw exhausted budget and made 0 calls.
	calls, _ := h.adapter.snapshotCalls()
	require.Len(t, calls, 1, "candidate 2 must not call provider after budget is exhausted")
	f1 := h.findFire(ev.ID, tr1.ID)
	f2 := h.findFire(ev.ID, tr2.ID)
	require.NotNil(t, f1)
	require.NotNil(t, f2)
	assert.Equal(t, store.TriggerFireStatusSkipped, f1.Status)
	assert.Equal(t, trigger.ErrFireRuleBudgetExhausted, f1.Error)
	assert.Equal(t, store.TriggerFireStatusSkipped, f2.Status)
	assert.Equal(t, trigger.ErrFireRuleBudgetExhausted, f2.Error)

	// 2. Capacity refusal: saturate MaxInFlight=1 on instance, verify second concurrent
	// evaluation fails with capacity -> skipped "rule evaluation failed" and 0 extra provider calls.
	hCap := newSystemOneTestHarness(t, nil)
	hCap.setAllowed(projID, "primary")
	hCap.setInstances(soruntime.Instance{
		ID: "inst-1", Name: "primary", Adapter: hCap.adapter.Name(), Model: "test-model-v1",
		Endpoint: hCap.httpSrv.URL, Auth: soruntime.InstanceAuth{Kind: provider.InstanceAuthAPIKey},
		Limits:  soruntime.InstanceLimits{MaxInFlight: 1},
		Enabled: true, Revision: 6,
	})
	blockCh := make(chan struct{})
	inCall := make(chan struct{})
	var callNum atomic.Int32
	hCap.adapter.invokeFn = func(ctx context.Context, trp provider.Transport, inst provider.InstanceView, req provider.ModelRequest) (provider.ModelResult, error) {
		if callNum.Add(1) == 1 {
			close(inCall)
			<-blockCh
		}
		bTrue := true
		qID := req.Rule.Questions[0].ID
		return provider.ModelResult{
			ResolvedModel: inst.Model,
			Answers: map[string]provider.Answer{
				qID: {QuestionID: qID, Primitive: spec.PrimitiveBinary, Status: provider.AnswerStatusAnswered, Bool: &bTrue},
			},
		}, nil
	}
	trCap := makeUserTrigger(projID, "src-cap", []store.TriggerMatchRule{
		{Dialect: "system_one", Expr: makeBinarySystemOneRule("primary", "qcap", `["event.subject"]`, "")},
	})
	hCap.st.triggers = []*store.Trigger{trCap}

	evHold := &store.TriggerEvent{
		ID: uuid.NewString(), ProjectID: projID, SourceKind: store.TriggerSourceKindWebhook,
		SourceID: "src-cap", Type: "webhook.received", Subject: "hold", Status: store.TriggerEventStatusPending,
	}
	evOverflow := &store.TriggerEvent{
		ID: uuid.NewString(), ProjectID: projID, SourceKind: store.TriggerSourceKindWebhook,
		SourceID: "src-cap", Type: "webhook.received", Subject: "overflow", Status: store.TriggerEventStatusPending,
	}

	doneHold := make(chan durablequeue.Result, 1)
	go func() {
		doneHold <- dispatchOneEvent(t, context.Background(), hCap, evHold)
	}()
	<-inCall

	// While evHold occupies the sole MaxInFlight=1 slot, evOverflow must fail fast with capacity (0 provider calls).
	resOverflow := dispatchOneEvent(t, context.Background(), hCap, evOverflow)
	require.True(t, resOverflow.Done)
	close(blockCh)
	<-doneHold

	assert.Equal(t, int32(1), callNum.Load(), "capacity-refused event must make 0 provider calls")
	fOver := hCap.findFire(evOverflow.ID, trCap.ID)
	require.NotNil(t, fOver)
	assert.Equal(t, store.TriggerFireStatusSkipped, fOver.Status)
	assert.Equal(t, trigger.ErrFireRuleEvaluationFailed, fOver.Error)
	var capTrace triggerrule.RuleTrace
	require.NoError(t, json.Unmarshal([]byte(fOver.RuleTrace), &capTrace))
	require.Len(t, capTrace.Elements, 1)
	assert.Equal(t, "capacity", capTrace.Elements[0].ProvenanceData["reason"])

	// 3. Redelivery without a memo re-evaluates; duplicate fire claim for same
	// (event_id, trigger_id) is ignored/refused so Core.Fire runs only once.
	hRedeliv := newSystemOneTestHarness(t, nil)
	hRedeliv.setAllowed(projID, "primary")
	var fireCalls atomic.Int32
	hRedeliv.core.fireFn = func(ctx context.Context, req hubfire.FireRequest) hubfire.FireOutcome {
		fireCalls.Add(1)
		return hubfire.FireOutcome{Status: hubfire.FireStatusFired}
	}
	trR := makeUserTrigger(projID, "src-r", []store.TriggerMatchRule{
		{Dialect: "system_one", Expr: makeBinarySystemOneRule("primary", "qr", `["event.subject"]`, "")},
	})
	hRedeliv.st.triggers = []*store.Trigger{trR}

	evR := &store.TriggerEvent{
		ID: uuid.NewString(), ProjectID: projID, SourceKind: store.TriggerSourceKindWebhook,
		SourceID: "src-r", Type: "webhook.received", Subject: "redeliver", Status: store.TriggerEventStatusPending,
	}
	require.True(t, dispatchOneEvent(t, context.Background(), hRedeliv, evR).Done)
	assert.Equal(t, int32(1), fireCalls.Load())

	// Simulate redelivery of the same pending event (e.g. crash before MarkTriggerEventProcessed).
	hRedeliv.st.mu.Lock()
	hRedeliv.st.events[evR.ID].Status = store.TriggerEventStatusPending
	hRedeliv.st.mu.Unlock()
	require.True(t, dispatchOneEvent(t, context.Background(), hRedeliv, evR).Done)

	// Without a memo, the provider was called again (2 calls), but unique fire claim prevented a duplicate fire!
	callsR, _ := hRedeliv.adapter.snapshotCalls()
	assert.Len(t, callsR, 2, "redelivery without memo re-evaluates System One element")
	assert.Equal(t, int32(1), fireCalls.Load(), "unique fire claim must prevent duplicate Core.Fire")
}

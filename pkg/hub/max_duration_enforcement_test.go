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

//go:build !no_sqlite

package hub

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/GoogleCloudPlatform/scion/pkg/agent/state"
	"github.com/GoogleCloudPlatform/scion/pkg/api"
	"github.com/GoogleCloudPlatform/scion/pkg/config"
	"github.com/GoogleCloudPlatform/scion/pkg/experiments"
	"github.com/GoogleCloudPlatform/scion/pkg/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func enableMaxDurationExperiment(t *testing.T, srv *Server, on bool) {
	t.Helper()
	ops := srv.GetOperationalSettings()
	if ops == nil {
		ops = NewOperationalSettings(newFakeHubSettingStore(), emptyKoanf(), emptyKoanf())
		_, _ = ops.Refresh(context.Background())
		srv.SetOperationalSettings(ops)
	}
	_, err := ops.Update(context.Background(), "experiments", json.RawMessage(fmt.Sprintf(`{"overrides":{%q:%t}}`, experiments.MaxDurationEnforcement, on)), "test", ops.SectionRevision("experiments"), "managed")
	require.NoError(t, err)
}

func setHubDefaultMaxDur(srv *Server, val string) {
	srv.mu.Lock()
	srv.config.AgentDefaults.DefaultMaxDuration = val
	srv.mu.Unlock()
}

func stampRunStarted(t *testing.T, s store.Store, agentID, runID string, at time.Time) {
	t.Helper()
	ok, err := s.RecordAgentRunStarted(context.Background(), agentID, runID, at)
	require.NoError(t, err)
	require.True(t, ok)
}

func newActiveMaxDurAgent(t *testing.T, f haltFixture, slug, phase, activity string, ac *store.AgentAppliedConfig, startedAt time.Time) *store.Agent {
	t.Helper()
	ctx, runID := context.Background(), "run-"+slug
	a := newReincarnateTestAgent(t, f.s, f.project, f.broker, func(a *store.Agent) {
		a.ID, a.Slug, a.Name, a.Phase, a.Activity, a.AppliedConfig = api.NewUUID(), slug, slug, phase, activity, ac
	})
	_, _ = f.s.SetAgentRunID(ctx, a.ID, runID, nil)
	_, _ = f.s.SetRunIntent(ctx, a.ID, store.RunIntentRunning)
	stampRunStarted(t, f.s, a.ID, runID, startedAt)
	return a
}

func assertLimitsStopped(t *testing.T, s store.Store, id string) *store.Agent {
	t.Helper()
	got, err := s.GetAgent(context.Background(), id)
	require.NoError(t, err)
	assert.Equal(t, [3]string{string(state.PhaseStopped), string(state.ActivityLimitsExceeded), string(state.ExitReasonLimitsExceeded)},
		[3]string{got.Phase, got.Activity, got.ExitReason})
	return got
}

func assertAgentPhase(t *testing.T, s store.Store, id string, want state.Phase) {
	t.Helper()
	got, err := s.GetAgent(context.Background(), id)
	require.NoError(t, err)
	assert.Equal(t, string(want), got.Phase)
}

func stopCallCount(d *reincarnateTestDispatcher) int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.stopCalls
}

func TestMaxDurationEnforcement_ExperimentGateGraceActivitiesAndSigtermRace(t *testing.T) {
	start := time.Date(2026, 11, 14, 10, 0, 0, 0, time.UTC)
	ac30m := &store.AgentAppliedConfig{InlineConfig: &api.ScionConfig{MaxDuration: "30m"}}
	f := newHaltFixture(t)
	f.agent.AppliedConfig = ac30m
	require.NoError(t, f.s.UpdateAgent(context.Background(), f.agent))
	stampRunStarted(t, f.s, f.agent.ID, "run-1", start)
	d := f.srv.GetDispatcher().(*reincarnateTestDispatcher)

	enableMaxDurationExperiment(t, f.srv, false)
	f.srv.enforceMaxDurationAt(context.Background(), start.Add(2*time.Hour))
	assert.Equal(t, 0, stopCallCount(d))

	enableMaxDurationExperiment(t, f.srv, true)
	f.srv.enforceMaxDurationAt(context.Background(), start.Add(39*time.Minute+59*time.Second))
	assert.Equal(t, 0, stopCallCount(d))
	f.srv.enforceMaxDurationAt(context.Background(), start.Add(40*time.Minute+time.Second))
	assert.Equal(t, 1, stopCallCount(d))
	assert.Equal(t, store.RunIntentRunning, assertLimitsStopped(t, f.s, f.agent.ID).RunIntent)
	f.srv.enforceMaxDurationAt(context.Background(), start.Add(50*time.Minute))
	assert.Equal(t, 1, stopCallCount(d))

	var actIDs []string
	for i, tc := range [][2]string{
		{string(state.PhaseRunning), string(state.ActivityBlocked)},
		{string(state.PhaseRunning), string(state.ActivityWaitingForInput)},
		{string(state.PhaseRunning), string(state.ActivityThinking)},
		{string(state.PhaseStarting), ""},
	} {
		actIDs = append(actIDs, newActiveMaxDurAgent(t, f, fmt.Sprintf("act-%d", i), tc[0], tc[1], ac30m, start).ID)
	}
	f.srv.enforceMaxDurationAt(context.Background(), start.Add(41*time.Minute))
	for _, id := range actIDs {
		assertLimitsStopped(t, f.s, id)
	}
	sigF := f
	sigF.agent = newActiveMaxDurAgent(t, f, "sigterm-race", string(state.PhaseRunning), "working", ac30m, start)
	d.stopHook = func() {
		require.Equal(t, http.StatusOK, sigF.selfReport(t, "run-sigterm-race", map[string]any{"phase": "stopped", "exitReason": "signaled"}))
	}
	f.srv.enforceMaxDurationAt(context.Background(), start.Add(42*time.Minute))
	assertLimitsStopped(t, f.s, sigF.agent.ID)
}

func TestMaxDurationEnforcement_PreDispatchAlreadyTerminalUntouched(t *testing.T) {
	ctx, f := context.Background(), newHaltFixture(t)
	d := f.srv.GetDispatcher().(*reincarnateTestDispatcher)
	for i, tc := range [][3]string{
		{string(state.PhaseStopped), string(state.ActivityCompleted), ""},
		{string(state.PhaseStopped), "", string(state.ExitReasonSignaled)},
		{string(state.PhaseError), string(state.ActivityCrashed), string(state.ExitReasonCrashed)},
	} {
		slug := fmt.Sprintf("pre-term-%d", i)
		a := newActiveMaxDurAgent(t, f, slug, string(state.PhaseRunning), "working", nil, time.Date(2026, 11, 14, 0, 0, 0, 0, time.UTC))
		require.NoError(t, f.s.UpdateAgentStatus(ctx, a.ID, store.AgentStatusUpdate{Phase: tc[0], Activity: tc[1], ExitReason: tc[2]}))
		assert.Equal(t, systemStopSkipped, f.srv.stopCurrentRunWithOutcome(ctx, a.ID, "run-"+slug, "max_duration enforcement stop", state.ActivityLimitsExceeded, state.ExitReasonLimitsExceeded))
		assert.Equal(t, 0, stopCallCount(d))
		got, err := f.s.GetAgent(ctx, a.ID)
		require.NoError(t, err)
		assert.Equal(t, tc, [3]string{got.Phase, got.Activity, got.ExitReason})
	}
}

func TestMaxDurationEnforcement_PrecedenceDefaultsAndZeroOptOut(t *testing.T) {
	ctx, f, start := context.Background(), newHaltFixture(t), time.Date(2026, 11, 14, 0, 0, 0, 0, time.UTC)
	enableMaxDurationExperiment(t, f.srv, true)
	tmpl := &store.Template{
		ID: api.NewUUID(), Slug: "dur-tmpl", Name: "dur-tmpl", Scope: store.TemplateScopeProject,
		ScopeID: f.project.ID, Status: store.TemplateStatusActive, AgentConfig: &api.ScionConfig{MaxDuration: "3h"},
	}
	require.NoError(t, f.s.CreateTemplate(ctx, tmpl))
	stampRunStarted(t, f.s, f.agent.ID, "run-1", start)

	tmplAgent := newActiveMaxDurAgent(t, f, "from-tmpl", string(state.PhaseRunning), "working", &store.AgentAppliedConfig{TemplateID: tmpl.ID}, start)
	projAgent := newActiveMaxDurAgent(t, f, "from-proj", string(state.PhaseRunning), "working", &store.AgentAppliedConfig{TemplateID: tmpl.ID, InlineConfig: &api.ScionConfig{MaxDuration: "1h"}}, start)
	zeroAgent := newActiveMaxDurAgent(t, f, "optout-zero", string(state.PhaseRunning), "working", &store.AgentAppliedConfig{InlineConfig: &api.ScionConfig{MaxDuration: "0"}}, start)

	f.srv.enforceMaxDurationAt(ctx, start.Add(time.Hour+11*time.Minute))
	assertLimitsStopped(t, f.s, projAgent.ID)
	assertAgentPhase(t, f.s, tmplAgent.ID, state.PhaseRunning)
	f.srv.enforceMaxDurationAt(ctx, start.Add(3*time.Hour+11*time.Minute))
	assertLimitsStopped(t, f.s, tmplAgent.ID)
	f.srv.enforceMaxDurationAt(ctx, start.Add(config.DefaultHubMaxDurationValue+9*time.Minute))
	assertAgentPhase(t, f.s, f.agent.ID, state.PhaseRunning)
	f.srv.enforceMaxDurationAt(ctx, start.Add(config.DefaultHubMaxDurationValue+11*time.Minute))
	assertLimitsStopped(t, f.s, f.agent.ID)
	assertAgentPhase(t, f.s, zeroAgent.ID, state.PhaseRunning)

	setHubDefaultMaxDur(f.srv, "0")
	hubZero := newActiveMaxDurAgent(t, f, "hub-zero", string(state.PhaseRunning), "working", nil, start)
	explicit := newActiveMaxDurAgent(t, f, "hub-zero-exp", string(state.PhaseRunning), "working", &store.AgentAppliedConfig{InlineConfig: &api.ScionConfig{MaxDuration: "30m"}}, start)
	f.srv.enforceMaxDurationAt(ctx, start.Add(48*time.Hour))
	assertAgentPhase(t, f.s, hubZero.ID, state.PhaseRunning)
	assertLimitsStopped(t, f.s, explicit.ID)
}

type getTemplateFaultStore struct {
	store.Store
	fault *storeFaultSwitch
}

func (w *getTemplateFaultStore) GetTemplate(ctx context.Context, id string) (*store.Template, error) {
	if w.fault.Active() {
		return nil, errors.New("injected template db error")
	}
	return w.Store.GetTemplate(ctx, id)
}

func createMaxDurTestTemplate(t *testing.T, s store.Store, projectID, slug, hash string, cfg *api.ScionConfig, files ...store.TemplateFile) *store.Template {
	t.Helper()
	tm := &store.Template{
		ID: api.NewUUID(), Slug: slug, Name: slug, ContentHash: hash, Scope: store.TemplateScopeProject,
		ScopeID: projectID, Status: store.TemplateStatusActive, AgentConfig: cfg, Files: files,
	}
	require.NoError(t, s.CreateTemplate(context.Background(), tm))
	return tm
}

func TestMaxDurationEnforcement_TemplateSnapshotSurvivesEditDeleteAndReincarnate(t *testing.T) {
	ctx, f, start := context.Background(), newHaltFixture(t), time.Date(2026, 11, 14, 0, 0, 0, 0, time.UTC)
	enableMaxDurationExperiment(t, f.srv, true)
	_, _ = f.s.SetRunIntent(ctx, f.agent.ID, store.RunIntentStopped)
	deriveWithTmpl := func(slug string, tm *store.Template) *store.Agent {
		a := newActiveMaxDurAgent(t, f, slug, string(state.PhaseRunning), "working", &store.AgentAppliedConfig{CreateInputs: &store.AgentCreateInputs{}}, start)
		a.Template = tm.Slug
		require.NoError(t, f.srv.deriveAgentConfig(ctx, a, f.project, tm))
		require.NoError(t, f.s.UpdateAgent(ctx, a))
		got, err := f.s.GetAgent(ctx, a.ID)
		require.NoError(t, err)
		require.NotNil(t, got.AppliedConfig.TemplateMaxDuration)
		return got
	}
	durTmpl := createMaxDurTestTemplate(t, f.s, f.project.ID, "snap-dur", "sha256:v1", &api.ScionConfig{MaxDuration: "2h"})
	emptyTmpl := createMaxDurTestTemplate(t, f.s, f.project.ID, "snap-empty", "sha256:v1", &api.ScionConfig{})
	delDurTmpl := createMaxDurTestTemplate(t, f.s, f.project.ID, "del-dur", "sha256:v1", &api.ScionConfig{MaxDuration: "2h"})
	delEmptyTmpl := createMaxDurTestTemplate(t, f.s, f.project.ID, "del-empty", "sha256:v1", &api.ScionConfig{})
	durA, emptyA := deriveWithTmpl("edited-dur-a", durTmpl), deriveWithTmpl("edited-empty-a", emptyTmpl)
	delDurA, delEmptyA := deriveWithTmpl("del-dur-a", delDurTmpl), deriveWithTmpl("del-empty-a", delEmptyTmpl)
	assert.Equal(t, [2]string{"2h", ""}, [2]string{*durA.AppliedConfig.TemplateMaxDuration, *emptyA.AppliedConfig.TemplateMaxDuration})

	for _, tm := range []*store.Template{durTmpl, emptyTmpl} {
		tm.ContentHash, tm.AgentConfig = "sha256:v2", &api.ScionConfig{MaxDuration: "72h"}
		require.NoError(t, f.s.UpdateTemplateContent(ctx, tm, store.TemplateContentPrecondition{ContentHash: "sha256:v1"}))
	}
	require.NoError(t, f.s.DeleteTemplate(ctx, delDurTmpl.ID))
	require.NoError(t, f.s.DeleteTemplate(ctx, delEmptyTmpl.ID))

	f.srv.enforceMaxDurationAt(ctx, start.Add(2*time.Hour+11*time.Minute))
	for _, id := range []string{durA.ID, delDurA.ID} {
		assertLimitsStopped(t, f.s, id)
	}
	for _, id := range []string{emptyA.ID, delEmptyA.ID} {
		assertAgentPhase(t, f.s, id, state.PhaseRunning)
	}
	f.srv.enforceMaxDurationAt(ctx, start.Add(config.DefaultHubMaxDurationValue+11*time.Minute))
	for _, id := range []string{emptyA.ID, delEmptyA.ID} {
		assertLimitsStopped(t, f.s, id)
	}

	fresh, _, err := f.srv.buildFreshAppliedConfig(ctx, durA, f.project, "")
	require.NoError(t, err)
	require.NotNil(t, fresh.TemplateMaxDuration)
	assert.Equal(t, [2]string{"sha256:v2", "72h"}, [2]string{fresh.TemplateHash, *fresh.TemplateMaxDuration})
}

func TestMaxDurationEnforcement_TemplateMissingErrorHashMismatchFailOpen(t *testing.T) {
	ctx, start := context.Background(), time.Date(2026, 11, 14, 0, 0, 0, 0, time.UTC)
	var fault *storeFaultSwitch
	var logBuf bytes.Buffer
	srv, s, project, broker := setupReincarnateTestServerWith(t, newReincarnateTestDispatcher(), func(srv *Server) {
		_, fault = installStoreFault(t, srv, func(inner store.Store, sw *storeFaultSwitch) *getTemplateFaultStore {
			return &getTemplateFaultStore{Store: inner, fault: sw}
		})
		srv.agentLifecycleLog = slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	})
	enableMaxDurationExperiment(t, srv, true)
	f := haltFixture{srv: srv, s: s, project: project, broker: broker}
	emptyDurTmpl := createMaxDurTestTemplate(t, s, project.ID, "empty-dur", "sha256:v1", &api.ScionConfig{})
	editedTmpl := createMaxDurTestTemplate(t, s, project.ID, "edited-dur", "sha256:v2", &api.ScionConfig{MaxDuration: "30m"})
	lazyUnreadableTmpl := createMaxDurTestTemplate(t, s, project.ID, "lazy-unreadable", "sha256:v1", nil, store.TemplateFile{Path: "scion-agent.yaml"})
	faultedTmpl := createMaxDurTestTemplate(t, s, project.ID, "faulted-dur", "sha256:v1", &api.ScionConfig{MaxDuration: "30m"})

	missingA := newActiveMaxDurAgent(t, f, "tmpl-missing", string(state.PhaseRunning), "working", &store.AgentAppliedConfig{TemplateID: api.NewUUID()}, start)
	mismatchA := newActiveMaxDurAgent(t, f, "tmpl-mismatch", string(state.PhaseRunning), "working", &store.AgentAppliedConfig{TemplateID: editedTmpl.ID, TemplateHash: "sha256:v1"}, start)
	lazyFailA := newActiveMaxDurAgent(t, f, "tmpl-lazy-err", string(state.PhaseRunning), "working", &store.AgentAppliedConfig{TemplateID: lazyUnreadableTmpl.ID, TemplateHash: "sha256:v1"}, start)
	emptyOkA := newActiveMaxDurAgent(t, f, "tmpl-empty-ok", string(state.PhaseRunning), "working", &store.AgentAppliedConfig{TemplateID: emptyDurTmpl.ID, TemplateHash: "sha256:v1"}, start)

	srv.enforceMaxDurationAt(ctx, start.Add(25*time.Hour))
	srv.enforceMaxDurationAt(ctx, start.Add(25*time.Hour+time.Minute))
	for _, id := range []string{missingA.ID, mismatchA.ID, lazyFailA.ID} {
		assertAgentPhase(t, s, id, state.PhaseRunning)
	}
	assertLimitsStopped(t, s, emptyOkA.ID)
	assert.Equal(t, 1, strings.Count(logBuf.String(), "template hash changed since provision"))
	assert.Equal(t, 2, strings.Count(logBuf.String(), "could not read template"))

	faultA := newActiveMaxDurAgent(t, f, "tmpl-db-err", string(state.PhaseRunning), "working", &store.AgentAppliedConfig{TemplateID: faultedTmpl.ID, TemplateHash: "sha256:v1"}, start)
	fault.Arm()
	srv.enforceMaxDurationAt(ctx, start.Add(25*time.Hour+2*time.Minute))
	assertAgentPhase(t, s, faultA.ID, state.PhaseRunning)
}

type brokerSelectiveStopDispatcher struct {
	*reincarnateTestDispatcher
	mu          sync.Mutex
	errByAgent  map[string]error
	errByBroker map[string]error
	callsByAgt  map[string]int
}

func (d *brokerSelectiveStopDispatcher) DispatchAgentStop(ctx context.Context, agent *store.Agent) error {
	d.mu.Lock()
	d.callsByAgt[agent.ID]++
	err := d.errByAgent[agent.ID]
	if err == nil {
		err = d.errByBroker[agent.RuntimeBrokerID]
	}
	d.mu.Unlock()
	if err != nil {
		return err
	}
	return d.reincarnateTestDispatcher.DispatchAgentStop(ctx, agent)
}

func TestMaxDurationEnforcement_RunGoneAndAgentRefusalDoNotBlockBroker(t *testing.T) {
	ctx, f, start := context.Background(), newHaltFixture(t), time.Date(2026, 11, 14, 10, 0, 0, 0, time.UTC)
	var logBuf bytes.Buffer
	f.srv.agentLifecycleLog = slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	enableMaxDurationExperiment(t, f.srv, true)
	setHubDefaultMaxDur(f.srv, "30m")
	_, _ = f.s.SetRunIntent(ctx, f.agent.ID, store.RunIntentStopped)

	runGoneA := newActiveMaxDurAgent(t, f, "rungone", string(state.PhaseRunning), "working", nil, start)
	refusedA := newActiveMaxDurAgent(t, f, "pin-refused", string(state.PhaseRunning), "working", nil, start.Add(time.Second))
	victimA := newActiveMaxDurAgent(t, f, "healthy-victim", string(state.PhaseRunning), "working", nil, start.Add(2*time.Second))
	selDisp := &brokerSelectiveStopDispatcher{
		reincarnateTestDispatcher: newReincarnateTestDispatcher(),
		errByAgent: map[string]error{
			runGoneA.ID: stopAgentError(&brokerStatusError{StatusCode: http.StatusNotFound, Body: `{"error":{"code":"` + api.BrokerErrorCodeRunMismatch + `","message":"not found"}}`}, "run-rungone"),
			refusedA.ID: placementConflict(stalePinRefusal(refusedA, "the pin is incomplete")),
		},
		callsByAgt: map[string]int{},
	}
	f.srv.SetDispatcher(selDisp)

	f.srv.enforceMaxDurationAt(ctx, start.Add(2*time.Hour))
	assertLimitsStopped(t, f.s, runGoneA.ID)
	assertAgentPhase(t, f.s, refusedA.ID, state.PhaseRunning)
	assertLimitsStopped(t, f.s, victimA.ID)
	assert.Equal(t, [2]int{1, 1}, [2]int{selDisp.callsByAgt[runGoneA.ID], selDisp.callsByAgt[victimA.ID]})

	f.srv.enforceMaxDurationAt(ctx, start.Add(2*time.Hour+time.Minute))
	assert.Equal(t, 1, selDisp.callsByAgt[runGoneA.ID], "run-gone row must not be retried on later ticks")
	assert.Equal(t, 1, strings.Count(logBuf.String(), "dispatch failed"), "systemStopSkipped refusal logged once per run")
}

func TestMaxDurationEnforcement_PerTickCapFailedBrokerSkipAndBlockedStates(t *testing.T) {
	ctx, f, start := context.Background(), newHaltFixture(t), time.Date(2026, 11, 14, 10, 0, 0, 0, time.UTC)
	enableMaxDurationExperiment(t, f.srv, true)
	setHubDefaultMaxDur(f.srv, "30m")

	badBroker := &store.RuntimeBroker{ID: api.NewUUID(), Name: "bad-brk", Slug: "bad-brk", Status: store.BrokerStatusOnline}
	require.NoError(t, f.s.CreateRuntimeBroker(ctx, badBroker))
	selDisp := &brokerSelectiveStopDispatcher{
		reincarnateTestDispatcher: newReincarnateTestDispatcher(),
		errByBroker:               map[string]error{badBroker.ID: &brokerStatusError{StatusCode: http.StatusServiceUnavailable}},
		callsByAgt:                map[string]int{},
	}
	f.srv.SetDispatcher(selDisp)
	_, _ = f.s.SetRunIntent(ctx, f.agent.ID, store.RunIntentStopped)
	stampRunStarted(t, f.s, f.agent.ID, "run-1", start)

	badFixture := f
	badFixture.broker = badBroker
	badA1 := newActiveMaxDurAgent(t, badFixture, "bad-1", string(state.PhaseRunning), "working", nil, start)
	badA2 := newActiveMaxDurAgent(t, badFixture, "bad-2", string(state.PhaseRunning), "working", nil, start.Add(time.Second))

	const healthyDue = maxDurationEnforcementPerTickCap + 1
	dueIDs := make([]string, healthyDue)
	for i := range healthyDue {
		dueIDs[i] = newActiveMaxDurAgent(t, f, fmt.Sprintf("cap-%02d", i), string(state.PhaseRunning), "working", nil, start.Add(time.Duration(i+2)*time.Second)).ID
	}
	f.srv.enforceMaxDurationAt(ctx, start.Add(2*time.Hour))
	assert.Equal(t, [2]int{1, 0}, [2]int{selDisp.callsByAgt[badA1.ID], selDisp.callsByAgt[badA2.ID]}, "failing broker skipped after first error in tick")
	assert.Equal(t, maxDurationEnforcementPerTickCap-1, stopCallCount(selDisp.reincarnateTestDispatcher))
	for _, id := range []string{f.agent.ID, badA1.ID, badA2.ID, dueIDs[maxDurationEnforcementPerTickCap-1]} {
		assertAgentPhase(t, f.s, id, state.PhaseRunning)
	}
	assertLimitsStopped(t, f.s, dueIDs[maxDurationEnforcementPerTickCap-2])

	f.srv.enforceMaxDurationAt(ctx, start.Add(2*time.Hour+time.Minute))
	assert.Equal(t, healthyDue, stopCallCount(selDisp.reincarnateTestDispatcher))
}

func TestMaxDurationEnforcement_DispatcherAndAsyncLaunchRecordRunStartedAndSurviveRestart(t *testing.T) {
	ctx, f := context.Background(), newHaltFixture(t)
	hd := NewHTTPAgentDispatcher(f.s, false, slog.New(slog.NewTextHandler(io.Discard, nil)))
	minted1, _, _, rec1, err := hd.beginRun(ctx, f.agent, nil)
	require.NoError(t, err)
	hd.settleLandedRun(ctx, f.agent, minted1, rec1, &RemoteAgentResponse{Agent: &RemoteAgentInfo{RunID: minted1, Phase: string(state.PhaseRunning)}})
	require.NotNil(t, f.get(t).RunStartedAt)

	backdated := time.Now().UTC().Add(-2 * time.Hour)
	mintedBase, _, _, _, err := hd.beginRun(ctx, f.agent, nil)
	require.NoError(t, err)
	stampRunStarted(t, f.s, f.agent.ID, mintedBase, backdated)
	minted2, _, _, rec2, err := hd.beginRun(ctx, f.agent, nil)
	require.NoError(t, err)
	hd.settleLandedRun(ctx, f.agent, minted2, rec2, &RemoteAgentResponse{Agent: &RemoteAgentInfo{RunID: mintedBase, Phase: string(state.PhaseRunning)}})
	assert.True(t, f.get(t).RunStartedAt.Equal(backdated))

	asyncAgent := newReincarnateTestAgent(t, f.s, f.project, f.broker, func(a *store.Agent) {
		a.ID, a.Slug, a.Name, a.Phase = api.NewUUID(), "async-launched", "async-launched", string(state.PhaseCreated)
		a.AppliedConfig = &store.AgentAppliedConfig{InlineConfig: &api.ScionConfig{MaxDuration: "30m"}}
	})
	_, _ = f.s.SetAgentRunID(ctx, asyncAgent.ID, "run-async-1", nil)
	_, _ = f.s.SetRunIntent(ctx, asyncAgent.ID, store.RunIntentRunning)
	launchID, err := f.s.BeginLaunchWithOptions(ctx, asyncAgent.ID, store.LaunchKindCreate, 5*time.Minute, store.BeginLaunchOptions{RunID: "run-async-1"})
	require.NoError(t, err)
	_, err = f.s.MarkLaunchAccepted(ctx, asyncAgent.ID, launchID, "broker-inst-1")
	require.NoError(t, err)

	body, _ := json.Marshal(AgentLaunchReport{LaunchID: launchID, InstanceID: "broker-inst-1", Seq: 1, State: store.LaunchReportStateSucceeded, Agent: &RemoteAgentInfo{RunID: "run-async-1"}})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/runtime-brokers/"+f.broker.ID+"/agents/"+asyncAgent.ID+"/launch", bytes.NewReader(body))
	req = req.WithContext(contextWithBrokerIdentity(req.Context(), NewBrokerIdentity(f.broker.ID)))
	rec := httptest.NewRecorder()
	f.srv.handleAgentLaunchReport(rec, req, f.broker.ID, asyncAgent.ID)
	require.Equal(t, http.StatusOK, rec.Code)
	gotAsync, _ := f.s.GetAgent(ctx, asyncAgent.ID)
	require.NotNil(t, gotAsync.RunStartedAt)

	srv2, err := newTestHubServer(t, testServerConfig(), f.s)
	require.NoError(t, err)
	waitUserScopedDataSweep(t, srv2)
	disp2 := newReincarnateTestDispatcher()
	srv2.SetDispatcher(disp2)
	enableMaxDurationExperiment(t, srv2, true)
	srv2.enforceMaxDurationAt(ctx, gotAsync.RunStartedAt.Add(41*time.Minute))
	assertLimitsStopped(t, f.s, asyncAgent.ID)
	assert.GreaterOrEqual(t, stopCallCount(disp2), 1)
}

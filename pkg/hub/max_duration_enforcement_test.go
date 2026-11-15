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
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
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
		_, err := ops.Refresh(context.Background())
		require.NoError(t, err)
		srv.SetOperationalSettings(ops)
	}
	doc := fmt.Sprintf(`{"overrides":{%q:%t}}`, experiments.MaxDurationEnforcement, on)
	_, err := ops.Update(context.Background(), "experiments", json.RawMessage(doc), "test", ops.SectionRevision("experiments"), "managed")
	require.NoError(t, err)
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
		a.ID, a.Slug, a.Name = api.NewUUID(), slug, slug
		a.Phase, a.Activity, a.AppliedConfig = phase, activity, ac
	})
	_, err := f.s.SetAgentRunID(ctx, a.ID, runID, nil)
	require.NoError(t, err)
	_, err = f.s.SetRunIntent(ctx, a.ID, store.RunIntentRunning)
	require.NoError(t, err)
	stampRunStarted(t, f.s, a.ID, runID, startedAt)
	return a
}

func assertLimitsStopped(t *testing.T, s store.Store, id string) *store.Agent {
	t.Helper()
	got, err := s.GetAgent(context.Background(), id)
	require.NoError(t, err)
	assert.Equal(t, string(state.PhaseStopped), got.Phase)
	assert.Equal(t, string(state.ActivityLimitsExceeded), got.Activity)
	assert.Equal(t, string(state.ExitReasonLimitsExceeded), got.ExitReason)
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

func TestMaxDurationEnforcement_ExperimentGateGraceAndSigtermRace(t *testing.T) {
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

	f2 := newHaltFixture(t)
	enableMaxDurationExperiment(t, f2.srv, true)
	f2.agent.AppliedConfig = ac30m
	require.NoError(t, f2.s.UpdateAgent(context.Background(), f2.agent))
	stampRunStarted(t, f2.s, f2.agent.ID, "run-1", start)
	d2 := f2.srv.GetDispatcher().(*reincarnateTestDispatcher)
	d2.stopHook = func() {
		require.Equal(t, http.StatusOK, f2.selfReport(t, "run-1", map[string]any{"phase": "stopped", "exitReason": "signaled"}))
	}
	f2.srv.enforceMaxDurationAt(context.Background(), start.Add(41*time.Minute))
	assert.Equal(t, 1, stopCallCount(d2))
	assertLimitsStopped(t, f2.s, f2.agent.ID)
}

func TestMaxDurationEnforcement_IgnoresActivityAndCoversStartingAndRunning(t *testing.T) {
	f := newHaltFixture(t)
	enableMaxDurationExperiment(t, f.srv, true)
	start := time.Date(2026, 11, 14, 10, 0, 0, 0, time.UTC)
	ac := &store.AgentAppliedConfig{InlineConfig: &api.ScionConfig{MaxDuration: "30m"}}
	agents := []*store.Agent{
		newActiveMaxDurAgent(t, f, "act-blocked", string(state.PhaseRunning), string(state.ActivityBlocked), ac, start),
		newActiveMaxDurAgent(t, f, "act-waiting", string(state.PhaseRunning), string(state.ActivityWaitingForInput), ac, start),
		newActiveMaxDurAgent(t, f, "act-thinking", string(state.PhaseRunning), string(state.ActivityThinking), ac, start),
		newActiveMaxDurAgent(t, f, "act-starting", string(state.PhaseStarting), "", ac, start),
	}
	f.srv.enforceMaxDurationAt(context.Background(), start.Add(41*time.Minute))
	for _, a := range agents {
		assertLimitsStopped(t, f.s, a.ID)
	}
}

func TestMaxDurationEnforcement_PrecedenceDefaultsAndZeroOptOut(t *testing.T) {
	ctx := context.Background()
	f := newHaltFixture(t)
	enableMaxDurationExperiment(t, f.srv, true)
	start := time.Date(2026, 11, 14, 0, 0, 0, 0, time.UTC)
	tmpl := &store.Template{
		ID: api.NewUUID(), Slug: "dur-tmpl", Name: "dur-tmpl",
		Scope: store.TemplateScopeProject, ScopeID: f.project.ID,
		Status: store.TemplateStatusActive, AgentConfig: &api.ScionConfig{MaxDuration: "3h"},
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

	f.srv.mu.Lock()
	f.srv.config.AgentDefaults.DefaultMaxDuration = "0"
	f.srv.mu.Unlock()
	hubZero := newActiveMaxDurAgent(t, f, "hub-zero", string(state.PhaseRunning), "working", nil, start)
	explicit := newActiveMaxDurAgent(t, f, "hub-zero-exp", string(state.PhaseRunning), "working", &store.AgentAppliedConfig{InlineConfig: &api.ScionConfig{MaxDuration: "30m"}}, start)
	f.srv.enforceMaxDurationAt(ctx, start.Add(48*time.Hour))
	assertAgentPhase(t, f.s, hubZero.ID, state.PhaseRunning)
	assertLimitsStopped(t, f.s, explicit.ID)
}

func TestMaxDurationEnforcement_PerTickCapAndBlockedStates(t *testing.T) {
	ctx := context.Background()
	f := newHaltFixture(t)
	enableMaxDurationExperiment(t, f.srv, true)
	f.srv.mu.Lock()
	f.srv.config.AgentDefaults.DefaultMaxDuration = "30m"
	f.srv.mu.Unlock()
	start := time.Date(2026, 11, 14, 10, 0, 0, 0, time.UTC)

	_, err := f.s.SetRunIntent(ctx, f.agent.ID, store.RunIntentStopped)
	require.NoError(t, err)
	stampRunStarted(t, f.s, f.agent.ID, "run-1", start)

	const totalDue = maxDurationEnforcementPerTickCap + 5
	dueIDs := make([]string, totalDue)
	for i := range totalDue {
		dueIDs[i] = newActiveMaxDurAgent(t, f, fmt.Sprintf("cap-%02d", i), string(state.PhaseRunning), "working", nil, start.Add(time.Duration(i)*time.Second)).ID
	}
	d := f.srv.GetDispatcher().(*reincarnateTestDispatcher)
	f.srv.enforceMaxDurationAt(ctx, start.Add(2*time.Hour))
	assert.Equal(t, maxDurationEnforcementPerTickCap, stopCallCount(d))
	assertAgentPhase(t, f.s, f.agent.ID, state.PhaseRunning)
	assertLimitsStopped(t, f.s, dueIDs[maxDurationEnforcementPerTickCap-1])
	assertAgentPhase(t, f.s, dueIDs[maxDurationEnforcementPerTickCap], state.PhaseRunning)

	f.srv.enforceMaxDurationAt(ctx, start.Add(2*time.Hour+time.Minute))
	assert.Equal(t, totalDue, stopCallCount(d))
}

func TestMaxDurationEnforcement_DispatcherAndAsyncLaunchRecordRunStartedAndSurviveRestart(t *testing.T) {
	ctx := context.Background()
	f := newHaltFixture(t)
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

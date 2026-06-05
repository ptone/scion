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
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/GoogleCloudPlatform/scion/pkg/agent/state"
	"github.com/GoogleCloudPlatform/scion/pkg/store"
	"github.com/GoogleCloudPlatform/scion/pkg/store/sqlite"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

// recordingExecutor records every Execute call for inspection in tests.
type recordingExecutor struct {
	mu    sync.Mutex
	calls []executorCall
}

type executorCall struct {
	HookID  string
	AgentID string
	Trigger string
}

func (e *recordingExecutor) Execute(_ context.Context, hook *store.LifecycleHook, agent *store.Agent, trigger string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.calls = append(e.calls, executorCall{
		HookID:  hook.ID,
		AgentID: agent.ID,
		Trigger: trigger,
	})
	return nil
}

func (e *recordingExecutor) getCalls() []executorCall {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]executorCall, len(e.calls))
	copy(out, e.calls)
	return out
}

// errorExecutor always returns an error from Execute.
type errorExecutor struct{}

func (e *errorExecutor) Execute(_ context.Context, _ *store.LifecycleHook, _ *store.Agent, _ string) error {
	return errors.New("simulated executor failure")
}

// panicExecutor panics on every Execute call.
type panicExecutor struct{}

func (e *panicExecutor) Execute(_ context.Context, _ *store.LifecycleHook, _ *store.Agent, _ string) error {
	panic("simulated executor panic")
}

// testEvaluatorStore creates a fresh in-memory SQLite store for evaluator tests.
func testEvaluatorStore(t *testing.T) store.Store {
	t.Helper()
	s, err := sqlite.New(":memory:")
	if err != nil {
		t.Skip("Skipping test because sqlite driver is not registered (build with -tags sqlite to enable)")
	}
	require.NoError(t, s.Migrate(context.Background()))
	return s
}

// seedProject creates a project in the store and returns its ID.
func seedProject(t *testing.T, s store.Store, name string) string {
	t.Helper()
	p := &store.Project{
		ID:         uuid.New().String(),
		Name:       name,
		Slug:       name,
		Visibility: "private",
		Created:    time.Now(),
		Updated:    time.Now(),
	}
	require.NoError(t, s.CreateProject(context.Background(), p))
	return p.ID
}

// seedAgent creates an agent in the store and returns it.
func seedAgent(t *testing.T, s store.Store, projectID, template, phase string) *store.Agent {
	t.Helper()
	a := &store.Agent{
		ID:         uuid.New().String(),
		Slug:       "agent-" + uuid.New().String()[:8],
		Name:       "Test Agent",
		Template:   template,
		ProjectID:  projectID,
		Phase:      phase,
		Visibility: "private",
		Created:    time.Now(),
		Updated:    time.Now(),
	}
	require.NoError(t, s.CreateAgent(context.Background(), a))
	return a
}

// seedHook creates a lifecycle hook in the store and returns it.
func seedHook(t *testing.T, s store.Store, name, trigger string, enabled bool, selector *store.LifecycleHookSelector) *store.LifecycleHook {
	t.Helper()
	h := &store.LifecycleHook{
		ID:        uuid.New().String(),
		Name:      name,
		ScopeType: store.LifecycleHookScopeHub,
		Trigger:   trigger,
		Action: &store.LifecycleHookAction{
			Type:           store.LifecycleHookActionWebhook,
			Method:         "POST",
			URL:            "https://hooks.example.com/" + name,
			TimeoutSeconds: 10,
			OnError:        store.LifecycleHookOnErrorLog,
		},
		Selector: selector,
		Enabled:  enabled,
		Created:  time.Now(),
		Updated:  time.Now(),
	}
	require.NoError(t, s.CreateLifecycleHook(context.Background(), h))
	return h
}

// ---------------------------------------------------------------------------
// Tests: selectorMatches
// ---------------------------------------------------------------------------

func TestSelectorMatches_NilSelector_MatchesAll(t *testing.T) {
	hook := &store.LifecycleHook{Selector: nil}
	agent := &store.Agent{ProjectID: "proj-1", Template: "claude"}
	assert.True(t, selectorMatches(hook, agent))
}

func TestSelectorMatches_EmptySelector_MatchesAll(t *testing.T) {
	hook := &store.LifecycleHook{Selector: &store.LifecycleHookSelector{}}
	agent := &store.Agent{ProjectID: "proj-1", Template: "claude"}
	assert.True(t, selectorMatches(hook, agent))
}

func TestSelectorMatches_ProjectID_Match(t *testing.T) {
	hook := &store.LifecycleHook{Selector: &store.LifecycleHookSelector{ProjectID: "proj-1"}}
	agent := &store.Agent{ProjectID: "proj-1", Template: "claude"}
	assert.True(t, selectorMatches(hook, agent))
}

func TestSelectorMatches_ProjectID_NoMatch(t *testing.T) {
	hook := &store.LifecycleHook{Selector: &store.LifecycleHookSelector{ProjectID: "proj-1"}}
	agent := &store.Agent{ProjectID: "proj-2", Template: "claude"}
	assert.False(t, selectorMatches(hook, agent))
}

func TestSelectorMatches_Template_Match(t *testing.T) {
	hook := &store.LifecycleHook{Selector: &store.LifecycleHookSelector{Template: "claude"}}
	agent := &store.Agent{ProjectID: "proj-1", Template: "claude"}
	assert.True(t, selectorMatches(hook, agent))
}

func TestSelectorMatches_Template_NoMatch(t *testing.T) {
	hook := &store.LifecycleHook{Selector: &store.LifecycleHookSelector{Template: "gemini"}}
	agent := &store.Agent{ProjectID: "proj-1", Template: "claude"}
	assert.False(t, selectorMatches(hook, agent))
}

func TestSelectorMatches_ProjectAndTemplate_BothMatch(t *testing.T) {
	hook := &store.LifecycleHook{Selector: &store.LifecycleHookSelector{ProjectID: "proj-1", Template: "claude"}}
	agent := &store.Agent{ProjectID: "proj-1", Template: "claude"}
	assert.True(t, selectorMatches(hook, agent))
}

func TestSelectorMatches_ProjectAndTemplate_TemplateMismatch(t *testing.T) {
	hook := &store.LifecycleHook{Selector: &store.LifecycleHookSelector{ProjectID: "proj-1", Template: "claude"}}
	agent := &store.Agent{ProjectID: "proj-1", Template: "gemini"}
	assert.False(t, selectorMatches(hook, agent))
}

func TestSelectorMatches_ProjectAndTemplate_ProjectMismatch(t *testing.T) {
	hook := &store.LifecycleHook{Selector: &store.LifecycleHookSelector{ProjectID: "proj-1", Template: "claude"}}
	agent := &store.Agent{ProjectID: "proj-2", Template: "claude"}
	assert.False(t, selectorMatches(hook, agent))
}

func TestSelectorMatches_OnlyProjectID_EmptyTemplate(t *testing.T) {
	hook := &store.LifecycleHook{Selector: &store.LifecycleHookSelector{ProjectID: "proj-1"}}
	agent := &store.Agent{ProjectID: "proj-1", Template: ""}
	assert.True(t, selectorMatches(hook, agent), "empty agent template should match when selector template is empty")
}

// ---------------------------------------------------------------------------
// Tests: findMatchingHooks (with store)
// ---------------------------------------------------------------------------

func TestFindMatchingHooks_EnabledOnly(t *testing.T) {
	s := testEvaluatorStore(t)
	projectID := seedProject(t, s, "test-project")
	agent := seedAgent(t, s, projectID, "claude", string(state.PhaseRunning))

	// Create one enabled and one disabled hook, both matching.
	seedHook(t, s, "enabled-hook", store.LifecycleHookTriggerRunning, true, nil)
	seedHook(t, s, "disabled-hook", store.LifecycleHookTriggerRunning, false, nil)

	exec := &recordingExecutor{}
	ev := NewLifecycleHookEvaluator(s, nil, exec, slog.Default())

	hooks, err := ev.findMatchingHooks(context.Background(), agent, store.LifecycleHookTriggerRunning)
	require.NoError(t, err)
	assert.Len(t, hooks, 1, "only enabled hooks should be returned")
	assert.Equal(t, "enabled-hook", hooks[0].Name)
}

func TestFindMatchingHooks_TriggerFilter(t *testing.T) {
	s := testEvaluatorStore(t)
	projectID := seedProject(t, s, "test-project")
	agent := seedAgent(t, s, projectID, "claude", string(state.PhaseStopped))

	// Create hooks for different triggers.
	seedHook(t, s, "running-hook", store.LifecycleHookTriggerRunning, true, nil)
	seedHook(t, s, "stopped-hook", store.LifecycleHookTriggerStopped, true, nil)
	seedHook(t, s, "error-hook", store.LifecycleHookTriggerError, true, nil)

	exec := &recordingExecutor{}
	ev := NewLifecycleHookEvaluator(s, nil, exec, slog.Default())

	// Only the stopped-hook should match.
	hooks, err := ev.findMatchingHooks(context.Background(), agent, store.LifecycleHookTriggerStopped)
	require.NoError(t, err)
	assert.Len(t, hooks, 1)
	assert.Equal(t, "stopped-hook", hooks[0].Name)
}

func TestFindMatchingHooks_SelectorFiltering(t *testing.T) {
	s := testEvaluatorStore(t)
	proj1 := seedProject(t, s, "project-alpha")
	proj2 := seedProject(t, s, "project-beta")
	agent := seedAgent(t, s, proj1, "claude", string(state.PhaseRunning))

	// Hook matching proj1 specifically.
	seedHook(t, s, "proj1-hook", store.LifecycleHookTriggerRunning, true,
		&store.LifecycleHookSelector{ProjectID: proj1})
	// Hook matching proj2 (should NOT match agent in proj1).
	seedHook(t, s, "proj2-hook", store.LifecycleHookTriggerRunning, true,
		&store.LifecycleHookSelector{ProjectID: proj2})
	// Hook with no selector (matches all).
	seedHook(t, s, "global-hook", store.LifecycleHookTriggerRunning, true, nil)

	exec := &recordingExecutor{}
	ev := NewLifecycleHookEvaluator(s, nil, exec, slog.Default())

	hooks, err := ev.findMatchingHooks(context.Background(), agent, store.LifecycleHookTriggerRunning)
	require.NoError(t, err)
	assert.Len(t, hooks, 2, "should match proj1-hook and global-hook, not proj2-hook")

	names := make(map[string]bool)
	for _, h := range hooks {
		names[h.Name] = true
	}
	assert.True(t, names["proj1-hook"])
	assert.True(t, names["global-hook"])
	assert.False(t, names["proj2-hook"])
}

func TestFindMatchingHooks_TemplateSelector(t *testing.T) {
	s := testEvaluatorStore(t)
	projectID := seedProject(t, s, "test-project")
	agent := seedAgent(t, s, projectID, "claude", string(state.PhaseRunning))

	seedHook(t, s, "claude-hook", store.LifecycleHookTriggerRunning, true,
		&store.LifecycleHookSelector{Template: "claude"})
	seedHook(t, s, "gemini-hook", store.LifecycleHookTriggerRunning, true,
		&store.LifecycleHookSelector{Template: "gemini"})

	exec := &recordingExecutor{}
	ev := NewLifecycleHookEvaluator(s, nil, exec, slog.Default())

	hooks, err := ev.findMatchingHooks(context.Background(), agent, store.LifecycleHookTriggerRunning)
	require.NoError(t, err)
	assert.Len(t, hooks, 1)
	assert.Equal(t, "claude-hook", hooks[0].Name)
}

func TestFindMatchingHooks_NoMatch_NoExecutorCall(t *testing.T) {
	s := testEvaluatorStore(t)
	projectID := seedProject(t, s, "test-project")
	agent := seedAgent(t, s, projectID, "claude", string(state.PhaseRunning))

	// No hooks exist.
	exec := &recordingExecutor{}
	ev := NewLifecycleHookEvaluator(s, nil, exec, slog.Default())

	ev.evaluateAndExecute(context.Background(), agent, store.LifecycleHookTriggerRunning)
	assert.Empty(t, exec.getCalls(), "no hooks should mean no executor calls")
}

// ---------------------------------------------------------------------------
// Tests: evaluateAndExecute
// ---------------------------------------------------------------------------

func TestEvaluateAndExecute_InvokesExecutor(t *testing.T) {
	s := testEvaluatorStore(t)
	projectID := seedProject(t, s, "test-project")
	agent := seedAgent(t, s, projectID, "claude", string(state.PhaseRunning))
	hook := seedHook(t, s, "register", store.LifecycleHookTriggerRunning, true, nil)

	exec := &recordingExecutor{}
	ev := NewLifecycleHookEvaluator(s, nil, exec, slog.Default())

	ev.evaluateAndExecute(context.Background(), agent, store.LifecycleHookTriggerRunning)

	calls := exec.getCalls()
	require.Len(t, calls, 1)
	assert.Equal(t, hook.ID, calls[0].HookID)
	assert.Equal(t, agent.ID, calls[0].AgentID)
	assert.Equal(t, store.LifecycleHookTriggerRunning, calls[0].Trigger)
}

func TestEvaluateAndExecute_MultipleMatchingHooks(t *testing.T) {
	s := testEvaluatorStore(t)
	projectID := seedProject(t, s, "test-project")
	agent := seedAgent(t, s, projectID, "claude", string(state.PhaseStopped))

	seedHook(t, s, "hook-a", store.LifecycleHookTriggerStopped, true, nil)
	seedHook(t, s, "hook-b", store.LifecycleHookTriggerStopped, true, nil)

	exec := &recordingExecutor{}
	ev := NewLifecycleHookEvaluator(s, nil, exec, slog.Default())

	ev.evaluateAndExecute(context.Background(), agent, store.LifecycleHookTriggerStopped)
	assert.Len(t, exec.getCalls(), 2, "both matching hooks should fire")
}

func TestEvaluateAndExecute_AllFourTriggers(t *testing.T) {
	triggers := []struct {
		trigger string
		phase   state.Phase
	}{
		{store.LifecycleHookTriggerRunning, state.PhaseRunning},
		{store.LifecycleHookTriggerSuspended, state.PhaseSuspended},
		{store.LifecycleHookTriggerStopped, state.PhaseStopped},
		{store.LifecycleHookTriggerError, state.PhaseError},
	}

	for _, tt := range triggers {
		t.Run(tt.trigger, func(t *testing.T) {
			s := testEvaluatorStore(t)
			projectID := seedProject(t, s, "test-project")
			agent := seedAgent(t, s, projectID, "claude", string(tt.phase))
			seedHook(t, s, tt.trigger+"-hook", tt.trigger, true, nil)

			exec := &recordingExecutor{}
			ev := NewLifecycleHookEvaluator(s, nil, exec, slog.Default())

			ev.evaluateAndExecute(context.Background(), agent, tt.trigger)

			calls := exec.getCalls()
			require.Len(t, calls, 1)
			assert.Equal(t, tt.trigger, calls[0].Trigger)
		})
	}
}

// ---------------------------------------------------------------------------
// Tests: Error/panic isolation (critical safety requirement)
// ---------------------------------------------------------------------------

func TestExecuteHookSafe_ErrorDoesNotPropagate(t *testing.T) {
	s := testEvaluatorStore(t)
	projectID := seedProject(t, s, "test-project")
	agent := seedAgent(t, s, projectID, "claude", string(state.PhaseRunning))
	hook := seedHook(t, s, "failing-hook", store.LifecycleHookTriggerRunning, true, nil)

	exec := &errorExecutor{}
	ev := NewLifecycleHookEvaluator(s, nil, exec, slog.Default())

	// This must not panic or propagate the error.
	ev.executeHookSafe(context.Background(), hook, agent, store.LifecycleHookTriggerRunning)
}

func TestExecuteHookSafe_PanicDoesNotPropagate(t *testing.T) {
	s := testEvaluatorStore(t)
	projectID := seedProject(t, s, "test-project")
	agent := seedAgent(t, s, projectID, "claude", string(state.PhaseRunning))
	hook := seedHook(t, s, "panicking-hook", store.LifecycleHookTriggerRunning, true, nil)

	exec := &panicExecutor{}
	ev := NewLifecycleHookEvaluator(s, nil, exec, slog.Default())

	// This must recover the panic and not crash.
	ev.executeHookSafe(context.Background(), hook, agent, store.LifecycleHookTriggerRunning)
}

func TestEvaluateAndExecute_ExecutorError_DoesNotAffectOtherHooks(t *testing.T) {
	s := testEvaluatorStore(t)
	projectID := seedProject(t, s, "test-project")
	agent := seedAgent(t, s, projectID, "claude", string(state.PhaseRunning))

	// Create two hooks. We'll use a counting executor that fails on the first call.
	seedHook(t, s, "hook-a", store.LifecycleHookTriggerRunning, true, nil)
	seedHook(t, s, "hook-b", store.LifecycleHookTriggerRunning, true, nil)

	callCount := 0
	exec := &failOnceExecutor{failOnCall: 1, callCount: &callCount}
	ev := NewLifecycleHookEvaluator(s, nil, exec, slog.Default())

	ev.evaluateAndExecute(context.Background(), agent, store.LifecycleHookTriggerRunning)

	// Both hooks should have been attempted.
	assert.Equal(t, 2, callCount, "both hooks should be attempted even if one fails")
}

// failOnceExecutor fails on a specified call number, succeeds otherwise.
type failOnceExecutor struct {
	failOnCall int
	callCount  *int
}

func (e *failOnceExecutor) Execute(_ context.Context, _ *store.LifecycleHook, _ *store.Agent, _ string) error {
	*e.callCount++
	if *e.callCount == e.failOnCall {
		return fmt.Errorf("simulated failure on call %d", *e.callCount)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Tests: Event-driven transition detection
// ---------------------------------------------------------------------------

func TestHandleEvent_DetectsPhaseTransition(t *testing.T) {
	s := testEvaluatorStore(t)
	projectID := seedProject(t, s, "test-project")
	agent := seedAgent(t, s, projectID, "claude", string(state.PhaseRunning))
	seedHook(t, s, "running-hook", store.LifecycleHookTriggerRunning, true, nil)

	exec := &recordingExecutor{}
	events := NewChannelEventPublisher()
	defer events.Close()
	ev := NewLifecycleHookEvaluator(s, events, exec, slog.Default())

	// Start the evaluator BEFORE publishing so the subscriber is listening.
	ev.Start()
	defer ev.Stop()

	// Simulate a transition from starting → running by publishing a status event.
	// The evaluator has no previous phase recorded, so "running" is a transition.
	events.PublishAgentStatus(context.Background(), agent)

	// Give the event loop a moment to process.
	time.Sleep(100 * time.Millisecond)

	calls := exec.getCalls()
	require.Len(t, calls, 1)
	assert.Equal(t, store.LifecycleHookTriggerRunning, calls[0].Trigger)
}

func TestHandleEvent_IgnoresRepublishedSamePhase(t *testing.T) {
	s := testEvaluatorStore(t)
	projectID := seedProject(t, s, "test-project")
	agent := seedAgent(t, s, projectID, "claude", string(state.PhaseRunning))
	seedHook(t, s, "running-hook", store.LifecycleHookTriggerRunning, true, nil)

	exec := &recordingExecutor{}
	events := NewChannelEventPublisher()
	defer events.Close()
	ev := NewLifecycleHookEvaluator(s, events, exec, slog.Default())
	ev.Start()
	defer ev.Stop()

	// Publish the same status twice (simulates heartbeat re-publishing).
	events.PublishAgentStatus(context.Background(), agent)
	time.Sleep(100 * time.Millisecond)
	events.PublishAgentStatus(context.Background(), agent)
	time.Sleep(100 * time.Millisecond)

	calls := exec.getCalls()
	assert.Len(t, calls, 1, "second publication of the same phase should not re-fire")
}

func TestHandleEvent_SuspendedToRunning_ReFiresRunning(t *testing.T) {
	s := testEvaluatorStore(t)
	projectID := seedProject(t, s, "test-project")
	agent := seedAgent(t, s, projectID, "claude", string(state.PhaseSuspended))
	seedHook(t, s, "running-hook", store.LifecycleHookTriggerRunning, true, nil)
	seedHook(t, s, "suspended-hook", store.LifecycleHookTriggerSuspended, true, nil)

	exec := &recordingExecutor{}
	events := NewChannelEventPublisher()
	defer events.Close()
	ev := NewLifecycleHookEvaluator(s, events, exec, slog.Default())
	ev.Start()
	defer ev.Stop()

	// First: agent enters suspended.
	events.PublishAgentStatus(context.Background(), agent)
	time.Sleep(100 * time.Millisecond)

	// Then: agent returns to running (resume).
	agent.Phase = string(state.PhaseRunning)
	require.NoError(t, s.UpdateAgentStatus(context.Background(), agent.ID, store.AgentStatusUpdate{Phase: string(state.PhaseRunning)}))
	events.PublishAgentStatus(context.Background(), agent)
	time.Sleep(100 * time.Millisecond)

	calls := exec.getCalls()
	require.Len(t, calls, 2)
	assert.Equal(t, store.LifecycleHookTriggerSuspended, calls[0].Trigger)
	assert.Equal(t, store.LifecycleHookTriggerRunning, calls[1].Trigger)
}

func TestHandleEvent_IgnoresNonV1Phases(t *testing.T) {
	s := testEvaluatorStore(t)
	projectID := seedProject(t, s, "test-project")
	agent := seedAgent(t, s, projectID, "claude", string(state.PhaseProvisioning))

	// Create a hook for every v1 trigger to verify none fires.
	seedHook(t, s, "any-hook", store.LifecycleHookTriggerRunning, true, nil)

	exec := &recordingExecutor{}
	events := NewChannelEventPublisher()
	defer events.Close()
	ev := NewLifecycleHookEvaluator(s, events, exec, slog.Default())
	ev.Start()
	defer ev.Stop()

	// Publish non-v1 phases.
	for _, phase := range []state.Phase{
		state.PhaseCreated, state.PhaseProvisioning, state.PhaseCloning,
		state.PhaseStarting, state.PhaseStopping,
	} {
		agent.Phase = string(phase)
		events.PublishAgentStatus(context.Background(), agent)
	}
	time.Sleep(100 * time.Millisecond)

	calls := exec.getCalls()
	assert.Empty(t, calls, "non-v1 phases should not fire any hooks")
}

// ---------------------------------------------------------------------------
// Tests: LoggingExecutor (no-op default)
// ---------------------------------------------------------------------------

func TestLoggingExecutor_DoesNotError(t *testing.T) {
	exec := &LoggingExecutor{Log: slog.Default()}
	hook := &store.LifecycleHook{ID: "h1", Name: "test"}
	agent := &store.Agent{ID: "a1", ProjectID: "p1", Template: "claude"}

	err := exec.Execute(context.Background(), hook, agent, "running")
	assert.NoError(t, err)
}

// ---------------------------------------------------------------------------
// Tests: Integration — transition does not block/fail due to hooks
// ---------------------------------------------------------------------------

func TestTransitionNotBlocked_ByExecutorError(t *testing.T) {
	// This test proves the critical safety property: an executor error
	// (or panic) does not propagate to or break the authoritative transition.
	// We directly test executeHookSafe + evaluateAndExecute to verify isolation.

	s := testEvaluatorStore(t)
	projectID := seedProject(t, s, "test-project")
	agent := seedAgent(t, s, projectID, "claude", string(state.PhaseRunning))
	hook := seedHook(t, s, "bad-hook", store.LifecycleHookTriggerRunning, true, nil)

	// Test with error executor.
	t.Run("error", func(t *testing.T) {
		exec := &errorExecutor{}
		ev := NewLifecycleHookEvaluator(s, nil, exec, slog.Default())
		// executeHookSafe must not return an error or panic.
		ev.executeHookSafe(context.Background(), hook, agent, store.LifecycleHookTriggerRunning)
		// If we got here, the test passed — no crash, no propagation.
	})

	// Test with panic executor.
	t.Run("panic", func(t *testing.T) {
		exec := &panicExecutor{}
		ev := NewLifecycleHookEvaluator(s, nil, exec, slog.Default())
		// executeHookSafe must recover the panic.
		ev.executeHookSafe(context.Background(), hook, agent, store.LifecycleHookTriggerRunning)
		// If we got here, the test passed — panic was recovered.
	})
}

func TestEvaluateAndExecute_WithPanicExecutor_ContinuesToNextHook(t *testing.T) {
	s := testEvaluatorStore(t)
	projectID := seedProject(t, s, "test-project")
	agent := seedAgent(t, s, projectID, "claude", string(state.PhaseRunning))

	seedHook(t, s, "panicking-hook", store.LifecycleHookTriggerRunning, true, nil)
	seedHook(t, s, "normal-hook", store.LifecycleHookTriggerRunning, true, nil)

	// Use an executor that panics on every call — the evaluator must recover
	// each time and attempt all hooks.
	callCount := 0
	exec := &countingPanicExecutor{callCount: &callCount}
	ev := NewLifecycleHookEvaluator(s, nil, exec, slog.Default())

	ev.evaluateAndExecute(context.Background(), agent, store.LifecycleHookTriggerRunning)

	// Both hooks should have been attempted despite panics.
	assert.Equal(t, 2, callCount, "both hooks should be attempted even with panics")
}

// countingPanicExecutor counts calls then panics.
type countingPanicExecutor struct {
	callCount *int
}

func (e *countingPanicExecutor) Execute(_ context.Context, _ *store.LifecycleHook, _ *store.Agent, _ string) error {
	*e.callCount++
	panic("simulated panic in executor")
}

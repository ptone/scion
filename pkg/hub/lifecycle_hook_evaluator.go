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

package hub

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"

	"github.com/GoogleCloudPlatform/scion/pkg/agent/state"
	"github.com/GoogleCloudPlatform/scion/pkg/store"
)

// v1Triggers is the set of authoritative phase transitions that fire lifecycle
// hooks in v1. Only these phases are considered as triggers.
var v1Triggers = map[state.Phase]string{
	state.PhaseRunning:   store.LifecycleHookTriggerRunning,
	state.PhaseSuspended: store.LifecycleHookTriggerSuspended,
	state.PhaseStopped:   store.LifecycleHookTriggerStopped,
	state.PhaseError:     store.LifecycleHookTriggerError,
}

// LifecycleHookExecutor is the interface that M5 will implement for executing
// the HTTP/webhook action of a lifecycle hook. M4 provides a no-op/logging
// default; tests and M5 can inject their own implementation.
type LifecycleHookExecutor interface {
	// Execute performs the action defined in the hook for the given agent and
	// trigger. Implementations MUST NOT panic; panics will be recovered by the
	// evaluator. Errors are logged but never propagated to the transition path.
	Execute(ctx context.Context, hook *store.LifecycleHook, agent *store.Agent, trigger string) error
}

// LoggingExecutor is a no-op executor that logs hook executions. It serves as
// the default executor for M4 and is replaced by the real HTTP executor in M5.
type LoggingExecutor struct {
	Log *slog.Logger
}

// Execute logs the hook execution without performing any real action.
func (e *LoggingExecutor) Execute(_ context.Context, hook *store.LifecycleHook, agent *store.Agent, trigger string) error {
	log := e.Log
	if log == nil {
		log = slog.Default()
	}
	log.Info("lifecycle hook fired (no-op executor)",
		"hook_id", hook.ID,
		"hook_name", hook.Name,
		"trigger", trigger,
		"agent_id", agent.ID,
		"agent_project_id", agent.ProjectID,
		"agent_template", agent.Template,
	)
	return nil
}

// LifecycleHookEvaluator listens for authoritative agent phase transitions and
// evaluates matching lifecycle hooks. It follows the same event-subscriber
// pattern as NotificationDispatcher: it subscribes to the ChannelEventPublisher
// and fires asynchronously after the transition is committed, guaranteeing that
// hook evaluation never blocks or fails the authoritative transition.
type LifecycleHookEvaluator struct {
	store    store.Store
	events   *ChannelEventPublisher
	executor LifecycleHookExecutor
	log      *slog.Logger

	// previousPhase tracks the last known phase per agent ID so we can detect
	// actual transitions (not re-publications of the same phase on heartbeats).
	mu            sync.Mutex
	previousPhase map[string]string

	stopCh   chan struct{}
	stopOnce sync.Once
	wg       sync.WaitGroup
}

// NewLifecycleHookEvaluator creates a new evaluator. The executor is injectable;
// pass nil to use the default LoggingExecutor.
func NewLifecycleHookEvaluator(s store.Store, events *ChannelEventPublisher, executor LifecycleHookExecutor, log *slog.Logger) *LifecycleHookEvaluator {
	if executor == nil {
		executor = &LoggingExecutor{Log: log}
	}
	if log == nil {
		log = slog.Default()
	}
	return &LifecycleHookEvaluator{
		store:         s,
		events:        events,
		executor:      executor,
		log:           log,
		previousPhase: make(map[string]string),
		stopCh:        make(chan struct{}),
	}
}

// Start subscribes to agent status events and spawns a goroutine to process them.
func (e *LifecycleHookEvaluator) Start() {
	statusCh, unsubStatus := e.events.Subscribe("project.>.agent.status")

	e.wg.Add(1)
	go func() {
		defer e.wg.Done()
		defer unsubStatus()
		for {
			select {
			case evt, ok := <-statusCh:
				if !ok {
					return
				}
				e.handleEvent(evt)
			case <-e.stopCh:
				return
			}
		}
	}()

	e.log.Info("Lifecycle hook evaluator started")
}

// Stop signals the evaluator goroutine to exit and waits for it to finish.
// Safe to call multiple times.
func (e *LifecycleHookEvaluator) Stop() {
	e.stopOnce.Do(func() {
		close(e.stopCh)
		e.wg.Wait()
		e.log.Info("Lifecycle hook evaluator stopped")
	})
}

// handleEvent processes a single agent status event. It checks whether the
// phase is a v1 trigger and whether it represents an actual transition, then
// evaluates matching hooks.
func (e *LifecycleHookEvaluator) handleEvent(evt Event) {
	var statusEvt AgentStatusEvent
	if err := json.Unmarshal(evt.Data, &statusEvt); err != nil {
		e.log.Error("Failed to unmarshal agent status event for lifecycle hooks", "error", err)
		return
	}

	// Only process v1 triggers.
	trigger, ok := v1Triggers[state.Phase(statusEvt.Phase)]
	if !ok {
		return
	}

	// Check for actual transition (not a re-publication of the same phase).
	e.mu.Lock()
	prev := e.previousPhase[statusEvt.AgentID]
	e.previousPhase[statusEvt.AgentID] = statusEvt.Phase
	e.mu.Unlock()

	if prev == statusEvt.Phase {
		return // same phase re-published (e.g., heartbeat), not a transition
	}

	// Fetch the full agent record so we have project_id and template for matching.
	ctx := context.Background()
	agent, err := e.store.GetAgent(ctx, statusEvt.AgentID)
	if err != nil {
		e.log.Error("Failed to fetch agent for lifecycle hook evaluation",
			"agent_id", statusEvt.AgentID, "error", err)
		return
	}

	e.evaluateAndExecute(ctx, agent, trigger)
}

// evaluateAndExecute loads matching hooks and invokes the executor for each.
// This method is safe to call directly (e.g. from tests) and recovers from
// panics in the executor.
func (e *LifecycleHookEvaluator) evaluateAndExecute(ctx context.Context, agent *store.Agent, trigger string) {
	hooks, err := e.findMatchingHooks(ctx, agent, trigger)
	if err != nil {
		e.log.Error("Failed to query lifecycle hooks",
			"trigger", trigger, "agent_id", agent.ID, "error", err)
		return
	}

	if len(hooks) == 0 {
		return
	}

	e.log.Info("Evaluating lifecycle hooks",
		"trigger", trigger,
		"agent_id", agent.ID,
		"matching_hooks", len(hooks),
	)

	for i := range hooks {
		hook := &hooks[i]
		e.executeHookSafe(ctx, hook, agent, trigger)
	}
}

// findMatchingHooks queries the store for enabled hooks matching the given
// trigger, then filters by selector (project_id, template). Empty/zero selector
// fields mean "match any".
func (e *LifecycleHookEvaluator) findMatchingHooks(ctx context.Context, agent *store.Agent, trigger string) ([]store.LifecycleHook, error) {
	enabled := true
	result, err := e.store.ListLifecycleHooks(ctx, store.LifecycleHookFilter{
		Trigger: trigger,
		Enabled: &enabled,
	}, store.ListOptions{Limit: 1000}) // generous limit; hooks are admin-managed
	if err != nil {
		return nil, fmt.Errorf("list lifecycle hooks: %w", err)
	}

	var matched []store.LifecycleHook
	for _, hook := range result.Items {
		if selectorMatches(&hook, agent) {
			matched = append(matched, hook)
		}
	}
	return matched, nil
}

// selectorMatches returns true if the hook's selector matches the given agent.
// An empty/nil selector matches all agents. When a selector field is non-empty,
// it must match the corresponding agent field exactly.
func selectorMatches(hook *store.LifecycleHook, agent *store.Agent) bool {
	sel := hook.Selector
	if sel == nil {
		return true // nil selector matches all agents
	}
	if sel.ProjectID != "" && sel.ProjectID != agent.ProjectID {
		return false
	}
	if sel.Template != "" && sel.Template != agent.Template {
		return false
	}
	return true
}

// executeHookSafe invokes the executor with panic recovery. Executor errors and
// panics are logged but never propagated — the transition path must succeed
// regardless.
func (e *LifecycleHookEvaluator) executeHookSafe(ctx context.Context, hook *store.LifecycleHook, agent *store.Agent, trigger string) {
	defer func() {
		if r := recover(); r != nil {
			e.log.Error("Panic in lifecycle hook executor (recovered)",
				"hook_id", hook.ID,
				"hook_name", hook.Name,
				"trigger", trigger,
				"agent_id", agent.ID,
				"panic", fmt.Sprintf("%v", r),
			)
		}
	}()

	if err := e.executor.Execute(ctx, hook, agent, trigger); err != nil {
		e.log.Error("Lifecycle hook execution failed",
			"hook_id", hook.ID,
			"hook_name", hook.Name,
			"trigger", trigger,
			"agent_id", agent.ID,
			"error", err,
		)
	}
}

# M4: Lifecycle Hook Evaluator

**Date:** 2026-06-05  
**Milestone:** M4 of Configurable Agent Lifecycle Hooks (issue #35)  
**Branch:** scion/architect-lifecycle-hooks

## Summary

Implemented the lifecycle hook evaluator — the component that, on an
authoritative agent phase transition, loads enabled lifecycle hooks whose scope
and selector match the transitioning agent and invokes an injectable Executor.

## Spike Findings

### Authoritative phase transition call sites

There are **multiple Hub-side call sites** where agent phase transitions are
committed:

1. **`handleAgentLifecycle`** (pkg/hub/handlers.go:2968) — Primary handler for
   start/stop/suspend/restart. Most explicit source of running, stopped,
   suspended transitions.
2. **Wake handler** (pkg/hub/handlers.go:2369) — Sets running after waking a
   suspended agent (suspend→running re-register). Also sets error on wake
   failure (line 2362).
3. **Heartbeat processing** (pkg/hub/handlers.go:6171) — Can set running/stopped
   from broker-reported container status.
4. **`updateAgentStatus`** (pkg/hub/handlers.go:2850) — Generic self-reported
   status update (agent reports its own phase/activity).

### Chosen integration point

Rather than tapping a single call site (which would miss transitions from other
paths), the evaluator follows the **NotificationDispatcher pattern**
(pkg/hub/notifications.go:70-97): it subscribes to the `ChannelEventPublisher`
event stream (`"project.>.agent.status"`) and reacts to phase changes.

**Rationale:**
- **Captures ALL transitions** from any source (lifecycle actions, heartbeats,
  status updates, wakes).
- **Post-commit by design** — events are published only after the store update
  succeeds, so hook evaluation is guaranteed to happen after the transition is
  durable.
- **Cannot block or fail the transition** — event processing is asynchronous, in
  a separate goroutine.
- **Proven pattern** — the codebase already uses this for notifications.

Confirmed: suspend→running re-fires the "running" trigger (tested in
`TestHandleEvent_SuspendedToRunning_ReFiresRunning`).

## Files Changed

- **pkg/hub/lifecycle_hook_evaluator.go** (new) — Evaluator, Executor interface,
  LoggingExecutor, selectorMatches, transition detection.
- **pkg/hub/lifecycle_hook_evaluator_test.go** (new) — 22 tests covering
  selector matching, enabled-only filtering, trigger filtering, event-driven
  transition detection, error/panic isolation.
- **pkg/hub/server.go** — Added `lifecycleHookEvaluator` field,
  `StartLifecycleHookEvaluator()` method, wired into startup and shutdown.

## Design Decisions

1. **Event-subscriber pattern** over direct call-site injection — single
   integration point that naturally captures all transition sources.
2. **Previous-phase tracking** per agent ID to distinguish actual transitions
   from heartbeat re-publications of the same phase.
3. **Panic recovery** in `executeHookSafe` — executor panics are logged but
   never propagate.
4. **Injectable executor** — `LifecycleHookExecutor` interface with `LoggingExecutor`
   default. M5 swaps in the real HTTP executor.

## Test Results

All 22 new tests pass. All existing hub tests continue to pass.

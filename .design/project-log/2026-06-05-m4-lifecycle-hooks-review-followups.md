# M4 Lifecycle Hooks — Code Review Follow-ups

**Date:** 2026-06-05
**Branch:** scion/architect-lifecycle-hooks
**Commit:** M4(lifecycle-hooks): apply code-review follow-ups to evaluator

## Changes Applied

### F2: Cold-start spurious firing fix
- Seed `previousPhase` from the store in `Start()` before subscribing
- Prevents mass re-fire storm after hub restart when the map is empty
- Lists agents with `ListAgents(AgentFilter{}, Limit: 10000)`, logs and continues on error

### F3: Memory leak fix
- Prune `previousPhase` entries on terminal phases (stopped/error) to prevent unbounded growth
- Subscribe to `project.*.agent.deleted` to prune on agent deletion (mirrors NotificationDispatcher)
- A later stopped->running transition is still detected correctly (missing entry = prev "")

### F1: Wake-failure publish (pre-existing gap)
- Added `PublishAgentStatus` after the error status write in handlers.go wake-failure path
- Without this, the evaluator never sees the error transition and ERROR hooks don't fire for wake failures
- This was a pre-existing gap in shared code, not introduced by lifecycle hooks

### F5: startOnce guard
- Added `sync.Once` guard so `Start()` is safe to call multiple times without spawning duplicate goroutines

### F4: HA doc comment
- Added v1 single-hub-instance assumption comment on the evaluator struct

### F6: Deterministic tests
- Replaced `time.Sleep`-based synchronization with channel-based `signalingExecutor`
- Fixed event subscription to use `*` (single-token wildcard) instead of `>` (multi-token) to prevent cross-matching between status and deleted event channels

### Design doc
- Added idempotency note to Reliability section

## Notable Finding

Discovered that `>` wildcard in event subscription patterns matches ALL remaining tokens, not just "any single token". Pattern `project.>.agent.deleted` also matched `project.X.agent.status` subjects, causing deleted-event handlers to spuriously prune previousPhase entries. Fixed by using `*` (single-token wildcard) instead.

## New Tests
- Cold-start seeding: no spurious firing for already-running agents after Start()
- Multi-agent seeding: verifies all agents are seeded from store
- Terminal phase pruning: map entry removed + subsequent transition detected
- Deleted event pruning: agent deletion removes previousPhase entry
- Start() double-call safety: idempotent, no duplicate processing
- Post-Stop() event safety: events after Stop() cause no panic or processing

## Verification
- `go build ./...` passes
- `go test ./pkg/hub/... -run LifecycleHook` passes (all tests)
- `go test -race ./pkg/hub/ -run LifecycleHook` passes (no data races)

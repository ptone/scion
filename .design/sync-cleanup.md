# Linked-Grove Hub Sync Cleanup Plan

## Context
Linked-grove sync currently relies on a single `state.yaml:last_synced_at` watermark.  
In edge cases, `scion list` reports **"Hub Agent Sync Required"** even when recent Hub-side operations should have kept state consistent.

## Goals
1. Prevent false-positive sync requirements after legitimate Hub-side mutations (especially delete).
2. Make watermark tracking monotonic and race-safe.
3. Make agent diff classification symmetric and serialization-aware.
4. Add regression tests that lock behavior for linked-grove Hub flows.

## Current Gaps
1. **Asymmetric diffing in `CompareAgents`**
   - `local-only` agents are always `ToRegister`.
   - `hub-only` agents use `last_synced_at` to decide `RemoteOnly` vs `ToRemove`.
   - Result: Hub-deleted agents can be re-registered from stale local state.

2. **Non-monotonic watermark writes**
   - `UpdateLastSyncedAt` overwrites `state.yaml` without max(existing, incoming).
   - Concurrent operations can move watermark backwards.

3. **Hub delete flow does not checkpoint sync state**
   - After successful Hub delete, watermark is not advanced.
   - If local cleanup fails and local files remain, next compare may force re-register.

4. **Multi-agent delete preflight exclusion is incomplete**
   - Only the first target agent is excluded from sync gating.
   - Deletes on additional targets may still be blocked by preflight sync checks.

## Proposed Improvements

### 1) Introduce monotonic, atomic watermark update
1. Replace `UpdateLastSyncedAt` internals with:
   - Load current `state.yaml`.
   - Parse existing watermark (if any).
   - Compute `new = max(existing, incomingOrNowUTC)`.
   - Write atomically (temp file + rename) to avoid partial writes.
2. Keep existing public function signature for minimal churn.
3. Add tests for:
   - Incoming older timestamp does not regress state.
   - Invalid existing timestamp is tolerated.
   - Repeated writes preserve monotonicity.

### 2) Symmetric diff classification in `CompareAgents`
1. Extend local agent metadata read (`getLocalAgentInfo`) to include a usable local timestamp source:
   - Prefer `agent-info.json` timestamp fields when present.
   - Fallback: file mtime (`agent-info.json` then `scion-agent.json/yaml`).
2. For `local-only` agents:
   - If locally modified/created **after** watermark -> `ToRegister`.
   - If local artifact appears older/equal to watermark -> `StaleLocal` (new bucket, no auto re-register).
3. Keep `hub-only` classification logic but make timestamp comparisons explicit and documented.
4. Update prompt output to show `StaleLocal` as informational/no-action by default.

### 3) Delete-flow consistency hardening
1. After successful Hub delete in `cmd/delete.go`, advance watermark using Hub server time or local UTC fallback.
2. On local cleanup failure after Hub delete:
   - Record a marker (or classify via timestamp path above) so stale local files do not force `ToRegister`.
   - Emit a clear warning with remediation command (`scion clean` or targeted cleanup).
3. Ensure stop+rm via Hub follows same post-delete consistency behavior.

### 4) Multi-target sync-gate behavior
1. Change sync preflight API from single `TargetAgent` to `ExcludedAgents []string` (or equivalent).
2. For multi-agent delete/stop operations, exclude all targets from preflight sync requirements.
3. Preserve single-agent behavior and backwards compatibility for existing callers.

## Test Plan
1. Unit tests in `pkg/hubsync/sync_test.go`:
   - Local-only stale agent after Hub delete is not forced into `ToRegister`.
   - Monotonic watermark behavior.
   - New classification bucket (`StaleLocal`) does not affect `IsInSync()`.
2. Command-level tests:
   - Multi-agent delete does not block on non-target sync drift.
   - Hub delete + failed local cleanup does not cause immediate re-register prompt.
3. Existing suite sanity:
   - `go test ./...`
   - If constrained: `go test -tags no_sqlite ./...`

## Rollout Strategy
1. Land monotonic watermark + tests first (low risk, isolated).
2. Land symmetric classification + prompt updates second.
3. Land delete-flow + multi-target exclusion changes third.
4. Validate with scripted scenario:
   - Create agent A via Hub.
   - Delete A on Hub.
   - Simulate local cleanup failure artifact.
   - Run `scion list` and verify no forced re-register.

## Success Criteria
1. `scion list` no longer emits false **"Hub Agent Sync Required"** for stale-local artifacts after Hub-side delete.
2. `last_synced_at` never regresses under concurrent/overlapping operations.
3. Multi-agent destructive operations are not blocked by sync checks on their own targets.

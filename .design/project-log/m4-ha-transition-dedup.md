# M4-HA: HA-Safe Transition De-duplication

**Date:** 2026-06-05
**Author:** Scion Agent (lh-m4ha)
**Branch:** scion/architect-lifecycle-hooks

## Summary

Implemented backend-aware transition de-duplication for the lifecycle hook evaluator to support multi-instance / HA deployments. The original evaluator used an in-memory `previousPhase` map which across multiple hub instances would double-fire hooks for the same logical transition.

## Changes

### Store Layer
- **Migration V55**: New `lifecycle_hook_agent_phase` table (agent_id PK, last_phase, updated_at)
- **CompareAndSetHookPhase**: Atomic `INSERT ... ON CONFLICT DO UPDATE ... WHERE last_phase IS NOT excluded.last_phase` — returns `changed=true` only when the stored phase actually differed
- **DeleteHookPhase**: Cleanup for terminal phases and agent deletion
- Both methods added to `LifecycleHookStore` interface, implemented in SQLite, delegated through `CompositeStore`

### Evaluator Refactor
- **TransitionDeduper interface**: `IsTransition(ctx, agentID, newPhase) (bool, error)` + `Forget(ctx, agentID) error`
- **storeDeduper**: Delegates to store CAS. Durable, HA-safe (exactly one CAS winner per transition). No cold-start seeding needed.
- **memoryDeduper**: Refactored from existing in-memory map. Seeded from store on Start(), pruned on terminal/deleted.
- **Backend selection**: `NewTransitionDeduper("postgres", ...)` → storeDeduper; anything else → memoryDeduper
- **WithDBDriver() option**: Functional option on `NewLifecycleHookEvaluator`; `ServerConfig.DatabaseDriver` carries the driver from config

### Tests
- storeDeduper CAS atomicity: 10 concurrent goroutines → exactly 1 winner
- storeDeduper phase transitions: changed=true on first, false on repeat, true on different phase
- memoryDeduper: seed, transition detection, forget
- Backend selection: postgres → storeDeduper, sqlite/empty → memoryDeduper
- Full end-to-end event flow with storeDeduper
- All existing evaluator tests updated and green
- Race detector clean (`go test -race ./pkg/hub/ -run LifecycleHook`)

## Decisions

1. **CAS in SQLite not ent**: The `lifecycle_hook_agent_phase` table uses raw SQL (INSERT ... ON CONFLICT) rather than Ent ORM because the atomic upsert pattern maps directly to SQL and Ent doesn't model conditional upserts well. The `CompositeStore` delegates to the base SQLite store.

2. **Functional options for constructor**: Used `WithDBDriver()` option to avoid breaking the existing `NewLifecycleHookEvaluator` signature for callers that don't need HA. The default (no option) uses memoryDeduper.

3. **Terminal phase pruning after CAS**: For terminal phases (stopped/error), the deduper records the transition first (so the hook fires), then immediately prunes the entry. This prevents unbounded growth while still detecting the terminal transition.

## Test Results
```
ok  github.com/GoogleCloudPlatform/scion/pkg/store          0.004s
ok  github.com/GoogleCloudPlatform/scion/pkg/store/entadapter   0.919s
ok  github.com/GoogleCloudPlatform/scion/pkg/store/sqlite       13.868s
ok  github.com/GoogleCloudPlatform/scion/pkg/hub            137.004s (normal)
ok  github.com/GoogleCloudPlatform/scion/pkg/hub            197.290s (-race)
```

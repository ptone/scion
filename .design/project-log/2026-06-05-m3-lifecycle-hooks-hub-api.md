# M3: Hub API admin-gated CRUD for LifecycleHook

**Date:** 2026-06-05
**Milestone:** M3 (Configurable Agent Lifecycle Hooks, issue #35)
**Branch:** scion/architect-lifecycle-hooks

## Summary

Implemented the Hub API layer for lifecycle hook CRUD operations, building
on M1 (entity + store) and M2 (validation + untrusted-variable guard).

## Files added/changed

- `pkg/hub/handlers_lifecycle_hooks.go` — new: CRUD handlers + resolver adapter
- `pkg/hub/handlers_lifecycle_hooks_test.go` — new: 22 tests
- `pkg/hub/audit.go` — added: LifecycleHookEventType constants + LogLifecycleHookEvent helper
- `pkg/hub/server.go` — added: route registration for lifecycle-hooks endpoints

## Endpoints

| Method | Path | Description |
|--------|------|-------------|
| POST | `/api/v1/admin/lifecycle-hooks` | Create hook |
| GET | `/api/v1/admin/lifecycle-hooks` | List hooks (filter: trigger, enabled) |
| GET | `/api/v1/admin/lifecycle-hooks/{id}` | Get hook by ID |
| PUT | `/api/v1/admin/lifecycle-hooks/{id}` | Update hook (optimistic locking) |
| DELETE | `/api/v1/admin/lifecycle-hooks/{id}` | Delete hook |

## Design decisions

1. **Authorization:** Direct `user.Role() == "admin"` check, matching
   `handleAdminServerConfig`. The authz policy engine is not used for these
   endpoints because lifecycle hooks are hub-scoped admin objects (v1), not
   project-scoped resources with RBAC.

2. **Audit logging:** Used standalone `slog.LogAttrs`-based logging rather than
   extending the `AuditLogger` interface. This avoids breaking existing interface
   implementations. The event types follow the naming convention of existing audit
   events.

3. **Validation wiring:** Created `storeGCPServiceAccountResolver` adapter to
   bridge `store.Store.GetGCPServiceAccount` to the `lifecyclehooks.GCPServiceAccountResolver`
   interface, keeping the validation library decoupled from the store.

4. **Optimistic locking:** The update handler performs a version check at the
   handler level (comparing client-sent StateVersion to the fetched record's
   version) before passing to the store's own version-checked update. This gives
   a clean 409 with details before the store round-trip when possible.

5. **Scope immutability:** ScopeType and ScopeID are set at creation and not
   mutable via update, matching the design doc's intent that scope is a structural
   property.

## Test results

All 22 lifecycle hook tests pass. Full hub test suite (120s) passes with no
regressions.

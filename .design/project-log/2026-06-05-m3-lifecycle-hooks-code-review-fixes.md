# M3 Lifecycle Hooks: Code Review Follow-ups

**Date:** 2026-06-05
**Branch:** scion/architect-lifecycle-hooks
**Commits:** a7f94e1, 2e2c809

## Changes Applied

### F1 (Bug Fix): Action converter data loss
- `entActionToStore` and `storeActionToEnt` in `pkg/store/entadapter/lifecyclehook_store.go` were missing `Type` and `AllowedUntrustedVars` fields.
- This caused silent data loss on the Ent/Postgres round-trip path. `AllowedUntrustedVars` is the security allow-list for untrusted variable substitution; losing it weakened security policy. `Type` is needed for validation and execution dispatch.
- Added both fields to both converter functions.
- Added round-trip tests in both entadapter and sqlite stores.

### F2 (Test Gap): Update authz tests
- Added `TestLifecycleHook_Update_Forbidden_NonAdmin` and `TestLifecycleHook_Update_Forbidden_Unauthenticated` proving PUT returns 403 for non-admin and unauthenticated users.

### F3 (Test Gap): Scope immutability
- Added `TestLifecycleHook_Update_ScopeImmutable` proving that `scopeType` and `scopeId` cannot be changed after creation. The `updateLifecycleHookRequest` struct intentionally omits these fields, preserving the original scope from the database.

### F4 (Optional): AuditLogger interface extension
- Added `LogLifecycleHookEvent` method to the `AuditLogger` interface with a structured `LifecycleHookEvent` type.
- Updated `LogAuditLogger` (production) and `mockAuditLogger` (test) implementations.
- Only two implementations exist, so this was clean and contained.

## Out-of-Scope Items (Confirmed Deferred)
- List pagination/cursor support (v1 default limit is acceptable)
- enabled/scopeType list-filter param edge-case nits
- Delete/Get TOCTOU for audit-name lookup (benign, handled)

## Test Results
- `go build ./...` - clean
- `go test ./pkg/hub/...` - all pass (119s)
- `go test ./pkg/store/...` - all pass (entadapter 0.9s, sqlite 13.9s)

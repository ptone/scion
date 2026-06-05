# M6 HA-Independent Items — Lifecycle Hooks

**Date**: 2026-06-05
**Milestone**: M6 (Configurable Agent Lifecycle Hooks, issue #35)
**Scope**: Three HA-independent items; M4-HA dedup deferred

## Changes

### 1. SSRF Dialer Hardening (Security)

**Problem**: The SSRF-safe dialer resolved hostnames via `LookupIPAddr`, checked
resolved IPs against the block list, but then dialed by *hostname*
(`net.JoinHostPort(host, port)`). Go's `net.Dialer` re-resolves at dial time,
leaving a DNS-rebinding TOCTOU window.

**Fix**: After resolving, select the first non-blocked IP and dial that specific
IP (`net.JoinHostPort(ip.String(), port)`). If every resolved IP is blocked,
return an error without dialing.

Introduced `ssrfResolver` and `ssrfDialer` interfaces for test injection.
Added three new unit tests exercising the exact dial address, all-blocked
refusal, and mixed-IP selection.

### 2. Admin Documentation

Created `docs/lifecycle-hooks.md` — a comprehensive admin guide covering:
- CRUD API (5 endpoints under `/api/v1/admin/lifecycle-hooks`)
- Triggers, action types, execution identity
- Variable substitution trust model (trusted vs untrusted)
- Error handling, SSRF protection, audit behavior
- Register/deregister example flow
- HA de-duplication status note

### 3. End-to-End Integration Test

Added `pkg/hub/lifecycle_hook_integration_test.go` wiring the real evaluator +
HTTPExecutor + mock token generator + in-memory SQLite store + httptest
"registry" server.

Two test functions:
- `RegisterDeregisterFlow`: register on running, deregister on stopped
- `SuspendedAndErrorDeregister`: deregister on suspended and error triggers

Both use the in-memory deduper (HA-independent, single-instance).

## Test Results

All lifecycle hook tests pass (with and without `-race`):
- `go test ./pkg/hub/ -run "LifecycleHook|SSRF|IsBlocked"` — PASS
- `go test -race ./pkg/hub/ -run "LifecycleHook"` — PASS
- `go build -buildvcs=false ./...` — PASS

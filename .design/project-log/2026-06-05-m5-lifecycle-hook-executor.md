# M5: Lifecycle Hook HTTP Executor Implementation

**Date:** 2026-06-05
**Milestone:** M5 — EXECUTOR (security-critical)
**Branch:** scion/architect-lifecycle-hooks

## Summary

Implemented the real `HTTPExecutor` that replaces the M4 no-op `LoggingExecutor`. This is the security-critical milestone where hooks actually execute HTTP requests with impersonated SA credentials.

## Files Changed

- `pkg/hub/lifecycle_hook_executor.go` — NEW: HTTPExecutor implementation (~280 lines)
- `pkg/hub/lifecycle_hook_executor_test.go` — NEW: 19 comprehensive tests (~530 lines)
- `pkg/hub/audit.go` — Extended with `LifecycleHookExecutionEvent` type and `LogLifecycleHookExecutionEvent` method on both the `AuditLogger` interface and `LogAuditLogger` implementation
- `pkg/hub/audit_gcp_test.go` — Added `LogLifecycleHookExecutionEvent` to `mockAuditLogger` (interface compliance)
- `pkg/hub/server.go` — Wired `HTTPExecutor` into `StartLifecycleHookEvaluator` (replaces `nil`/`LoggingExecutor`)

## Key Design Decisions

### SA Token Acquisition
- Reused existing `GCPTokenGenerator.GenerateAccessToken()` path (IAM impersonation via `CachedGCPTokenGenerator` → `IAMTokenGenerator`)
- Resolves `hook.ExecutionIdentity` (UUID) → `store.GCPServiceAccount.Email` via store lookup
- Token is injected directly after rendering — NEVER via hook variables

### Trust Boundary
- Trusted variables: HOOK_ID, HOOK_NAME, TRIGGER, PROJECT_ID, PROJECT_NAME, AGENT_ID, AGENT_SLUG, SA_EMAIL
- Untrusted variables: AGENT_NAME, TASK_SUMMARY, AGENT_STATUS, ERROR_MSG
- Variable rendering delegated entirely to `lifecyclehooks.RenderAction()` — no custom substitution

### SSRF Protection
- All redirects blocked via `CheckRedirect` returning error (chosen over IP-checking because blocking all redirects is simpler and the admin should configure the final URL directly)

### Retry Policy
- Fixed 3 max attempts with exponential backoff (500ms, 1s)
- Each attempt gets its own audit event
- After exhaustion, falls back to "log" behavior (return error, never block transition)

### Audit Safety
- Records: hook ID, trigger, agent ID, SA email, action type, method, host (not full URL), HTTP status, latency, attempt
- Explicitly excludes: response body, Authorization header value, full URL path (may contain webhook tokens)

## What is NOT Written to Audit
- Response bodies (attacker/third-party controlled content)
- Rendered Authorization header values (bearer tokens)
- Full URL paths (may contain path-based webhook tokens; only host recorded)

## Test Results
All 19 executor tests pass, including -race:
- `go test ./pkg/hub/... -run LifecycleHookExecutor` — PASS
- `go test -race ./pkg/hub/... -run LifecycleHookExecutor` — PASS (no data races)
- All existing lifecycle hook tests (evaluator, admin CRUD) — PASS

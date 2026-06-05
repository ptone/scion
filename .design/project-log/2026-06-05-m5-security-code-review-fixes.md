# M5 Lifecycle Hooks Executor: Security & Code-Review Fixes

**Date:** 2026-06-05
**Branch:** `scion/architect-lifecycle-hooks`
**Commit:** `c206d49`

## Summary

Applied security fixes (S1, S2) and code-review improvements (D1, C2, L1-L3, N1-N3) to the M5 lifecycle hooks executor. All tests pass including `-race`.

## Security Fixes

### S1 — SSRF (HIGH): Initial URL unvalidated
- **Problem:** `isPrivateIP` was dead code; the `http.Client` used the default transport with no `DialContext`, so the initial request to a hook URL could hit loopback/link-local/metadata (169.254.169.254) with the SA bearer token attached.
- **Fix:** Wired a custom `http.Transport` with a `DialContext` that resolves DNS first, then inspects the RESOLVED connection IP and refuses blocked targets. This also defends against DNS-rebinding.
- **SSRF Granularity (architect decision):** Block ONLY loopback (127.0.0.0/8, ::1) and link-local (169.254.0.0/16, fe80::/10) unicast+multicast. RFC1918 (10/8, 172.16/12, 192.168/16) is intentionally ALLOWED — internal service registries (Consul, internal catalogs) are a supported use case.

### S2 — Bearer token over cleartext (MEDIUM)
- **Problem:** `validateAction` allowed `http://` URLs for `http` action type, and the executor attaches the SA bearer token regardless of scheme → SA token (cloud-platform scope) could be sent in cleartext.
- **Fix:** In `validateAction`, REQUIRE https for `action.Type == "http"` (reject `http://` for http type). `http://` remains allowed for webhook type (no bearer attached).

## Code-Review Fixes

- **D1:** `renderTrustedSubstitution` now blanks untrusted vars in URL host/path (defense-in-depth, matches `renderHeaderValue` pattern)
- **C2:** `buildRenderVars` threads parent `ctx` into `GetProject` instead of `context.Background()`
- **L1:** 4xx responses are non-retryable — early exit with failure audit
- **L2:** Single `http.Client` created once and reused across retry attempts
- **L3:** `nil` request body when `action.Body` is empty
- **N1:** Bit-shift backoff instead of `math.Pow`
- **N2:** Removed redundant `http.Client.Timeout` (context deadline is the single timeout)
- **N3:** Interface compliance check moved to package-level `var`

## Tests Added

- `isBlockedSSRFTarget` unit tests: loopback/link-local=blocked, RFC1918+public=allowed
- Integration test: SSRF-safe transport refuses loopback dial
- S2 validation: http:// + http type → rejected; https:// + http → ok; http:// + webhook → ok
- L1: 4xx non-retryable (single attempt despite retry policy)
- ctx cancellation during retry backoff aborts further attempts
- `on_error=""` defaults to single attempt
- Empty-body GET sends nil body
- D1: URL renderer blanks untrusted var

## Test Results

```
go test ./pkg/hub/ -run 'LifecycleHook|IsBlockedSSRFTarget' → PASS (11s)
go test ./pkg/lifecyclehooks/... → PASS (0.005s)
go test -race ./pkg/hub/ -run LifecycleHook → PASS (197s)
go build ./... → OK
```

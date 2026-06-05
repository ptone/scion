# M2 Lifecycle Hooks — Security & Quality Review Fixes

**Date**: 2026-06-05  
**Branch**: `scion/architect-lifecycle-hooks`  
**Commit**: `b53426f`

## Summary

Applied all fixes from a security audit and code review of `pkg/lifecyclehooks/`.
The changes harden the untrusted-variable guard, add admin-controlled allow-listing,
and fix several correctness/quality issues.

## Security Fixes Applied

| ID | Fix | Files |
|----|-----|-------|
| B1 | Reject untrusted vars in ALL header values (not just auth headers) | varguard.go |
| B2 | Defense-in-depth at render: blank untrusted vars in headers, strip CR/LF | varguard.go |
| B3 | Add cookie/set-cookie to auth header names | validate.go |
| B4 | AllowedUntrustedVars allow-list field on action struct; untrusted vars rejected unless admin-allow-listed, body-only | models.go, types.go, varguard.go |
| B5 | Static check: allow-listed untrusted body vars must be inside JSON string literals | varguard.go |

## Quality Fixes Applied

- C1: Empty on_error normalized to "log" at validation
- C2: Fixed misleading "no store dependency" package doc
- C3: Fixed stale comment (authHeaderPrefixes → authHeaderNames)
- C4: Reordered non-ASCII check in isValidHeaderName for clarity
- C5: Webhook method now requires canonical uppercase (consistent with http)
- C6: Replaced containsString helper with strings.Contains
- C7: Confirmed ent/store struct mirror is intentional (no change)
- C8: Documented JSON body assumption

## Test Coverage

Added table-driven tests T1-T6 covering all security fixes:
- Untrusted vars in non-auth headers rejected
- Cookie/Set-Cookie webhook rejection
- Body allow-list enforcement
- Non-string body position rejection
- Empty on_error defaults
- Render-time header defense-in-depth

All 50+ test cases pass. Full build clean.

## Design Notes

- **B5 key-vs-value**: JSON keys are syntactically string literals. The
  `isInsideJSONString` check accepts them (they're quoted). The truly dangerous
  positions (bare numeric/boolean/null) are correctly rejected. A full JSON
  parser could distinguish keys, but the current approach is pragmatic and safe
  since `jsonEncodeValue` still escapes key content.
- **B4 query params**: Untrusted vars are now also rejected in URL query params
  (tightened from the original which allowed them with percent-encoding).
  This is more conservative; the allow-list is body-only.

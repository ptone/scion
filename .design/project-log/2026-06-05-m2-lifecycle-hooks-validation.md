# M2 Lifecycle Hooks — Validation Library

**Date:** 2026-06-05
**Branch:** scion/architect-lifecycle-hooks
**Commits:** b2d525a, 260a1cf, 5888b03
**Pushed:** dca21cd..5888b03

## What was done

Implemented M2 of the lifecycle hooks feature (issue #35): the validation
library and untrusted-variable guard in `pkg/lifecyclehooks/`.

### Files created
- `pkg/lifecyclehooks/validate.go` — hook validation (trigger, action,
  execution_identity, variable guard integration)
- `pkg/lifecyclehooks/varguard.go` — variable trust classification, static
  validator, and execution-time renderer with position-aware encoding
- `pkg/lifecyclehooks/validate_test.go` — 32 test cases for hook validation
- `pkg/lifecyclehooks/varguard_test.go` — 31 test cases for variable guard

### Files modified
- `pkg/ent/schema/types.go` — added `Type` field to `LifecycleHookAction`
- `pkg/store/models.go` — added `Type` field and action type constants

## Key design decisions

1. **Action Type field:** Added `Type` ("http" | "webhook") to
   `LifecycleHookAction` since it wasn't in M1 but is needed for type-specific
   validation rules (http requires execution_identity; webhook forbids auth headers).

2. **GCPServiceAccountResolver interface:** The validation package doesn't
   depend on the store directly. Instead, callers provide a
   `GCPServiceAccountResolver` interface. This keeps the package importable
   by both handlers and executor without circular dependencies.

3. **Strict HTTP method casing:** Methods must be uppercase (GET, POST, etc.)
   rather than case-insensitive matching. This follows HTTP spec and prevents
   ambiguity.

4. **MaxTimeoutSeconds = 30:** Documented constant, configurable via code change.

5. **Variable trust defaults:** Unknown variables default to UNTRUSTED
   (security-conservative). This means new variables added later are safe by
   default until explicitly classified.

## Test coverage

63 tests total, all passing. Adversarial cases include:
- SSRF/path manipulation via untrusted var in host/path → REJECTED
- Auth-header injection via untrusted var in auth header → REJECTED
- Header-name injection via variables in header names → REJECTED
- JSON field/structure injection → properly JSON-encoded
- URL parameter injection → percent-encoded
- Unknown vars in sensitive positions → REJECTED (defaults to untrusted)
- End-to-end validate + render pipeline test

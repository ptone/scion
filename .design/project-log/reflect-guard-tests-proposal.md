# Project Log: Reflect-Based Field-Classification Guard Tests Proposal

**Date:** 2026-08-27  
**Agent:** dev-p3f1-proposal  
**Task:** File a proposal for reflect-based field-classification tests on entities with immutable fields guarded by omission.

## Summary

Filed proposal at `.design/proposals/reflect-guard-tests.md`.

## Findings

An asymmetry sweep identified four entities whose immutable fields are protected
only by the absence of setter calls in Update builders — no test, no schema
constraint, and (in 3 of 4 cases) no documentation:

1. **`store.GCPServiceAccount`** — `CreatedBy`, `Scope`, `ScopeID`, `CreatedAt`.
   Authorization-input fields. Documented with a 50-line warning comment.
   Failure mode: writable authorization bypass.

2. **`store.RuntimeBroker`** — `CreatedBy`. Ownership field. Undocumented.
   Failure mode: ownership confusion.

3. **`store.Project`** — `CreatedBy`. Ownership field. Undocumented.
   Failure mode: ownership confusion.

4. **`store.GitHubInstallation`** — `InstallationID` (natural key), `CreatedAt`.
   Undocumented. Partially protected by ORM primary-key semantics.
   Failure mode: identity confusion, broken referential integrity.

The codebase already has a working reflect-based guard pattern in
`pkg/hub/project_settings_resolved_guard_test.go` that uses `reflect.TypeOf`
field enumeration to enforce structural contracts.

## Proposal

One reflect-based test per entity that classifies all struct fields into
{mutable, immutable} and asserts the Update builder only touches the mutable
set. New fields trigger a test failure until explicitly classified, making the
guard grow automatically with the type.

## Files

- `.design/proposals/reflect-guard-tests.md` — the proposal document

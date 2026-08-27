# Proposal: Reflect-Based Field-Classification Guard Tests

**Status:** Proposal  
**Date:** 2026-08-27  
**Author:** Scion Agent (dev-p3f1-proposal)

## Problem Statement

Several persistence entities in the Hub have fields that must never be modified
after creation — authorization inputs, natural keys, timestamps. Today, the
immutability of these fields is enforced **solely by the absence of setter calls
in the Update builder**. There is no test, no schema annotation, and (with one
exception) no documentation.

The failure mode is silent and dangerous. A contributor who adds a setter to an
Update builder — often as a consistency fix to make Update symmetric with
Create — will see no test fail, no lint fire, and no reviewer warning beyond
whatever comment happens to sit above the function. The change ships, and from
that point forward:

- **Authorization bypass:** If the field is an authorization input (e.g.
  `CreatedBy` feeding `Resource.OwnerID`), any handler that round-trips the
  entity through Update can rewrite the owner, gaining the owner-bypass path in
  `checkAccessForUser` without any membership or policy check.
- **Identity confusion:** If the field is a natural key (e.g. `InstallationID`
  on a GitHub installation), updating it breaks every foreign-key reference and
  lookup that uses the old value.
- **Data corruption:** If the field is a provenance timestamp (e.g. `CreatedAt`),
  overwriting it destroys audit-trail integrity.

The single documented instance — `store.GCPServiceAccount` — includes a 50-line
warning comment that literally says: *"if you are here to add a setter, this
comment is the entire control, and you have just reached it."* That sentence is
a defect report someone already wrote and nobody actioned.

## Existing Example: `project_settings_resolved_guard_test.go`

The codebase already has a working model for reflect-based structural guards.

**File:** `pkg/hub/project_settings_resolved_guard_test.go`

This test uses `reflect.TypeOf(...)` to iterate over struct fields and enforce
contracts on the resolved-settings response types. The key techniques are:

1. **Exhaustive field enumeration** — `typ.NumField()` with `typ.Field(i)` walks
   every field on the type, including fields added after the test was written.
   A new field cannot hide from this loop.

2. **Classification against an expected set** — the test maintains an explicit
   list of allowed field names/properties and asserts the struct matches exactly.
   Any addition or removal trips the assertion.

3. **Structural prohibition** — rather than populating a fixture and checking
   output (which drifts), the test bans shapes that would make the guard
   unsound (e.g. embedded struct pointers, custom marshallers, unsafe tag
   options). The prohibition is structural: it cannot rot because there is
   nothing to keep in sync.

The pattern works because it shifts the burden from "a reviewer must notice the
absence of a setter" to "a contributor must update the classification when they
add a field." The second is enforceable by CI; the first is not.

## Bucket List

### Priority 1: `store.GCPServiceAccount`

| Attribute | Detail |
|---|---|
| **Struct** | `store.GCPServiceAccount` |
| **File** | `pkg/store/models.go:1583` |
| **Store file** | `pkg/store/entadapter/external_store.go` |
| **Create** | Lines 114–150 — sets `Scope`, `ScopeID`, `CreatedBy`, `CreatedAt` |
| **Update** | Lines 201–229 — omits `Scope`, `ScopeID`, `CreatedBy`, `CreatedAt` |
| **Immutable fields** | `CreatedBy`, `Scope`, `ScopeID`, `CreatedAt` |
| **Why immutable** | `CreatedBy` → feeds `Resource.OwnerID` → owner-bypass in `checkAccessForUser`. `Scope` → selects authorization arm in `gcpServiceAccountVerdict`. `ScopeID` → confines account to a project via `ReachableFromProject`. `CreatedAt` → provenance timestamp. |
| **Failure if guard fails** | Writable authorization bypass. A handler round-tripping through Update can rewrite the owner, gaining `Allowed` from the owner-bypass path before any membership or policy check. |
| **Documented?** | **Yes.** 50-line warning comment above `UpdateGCPServiceAccount` (lines 165–200) explicitly names each field and the authorization consequence. |

### Priority 2: `store.RuntimeBroker`

| Attribute | Detail |
|---|---|
| **Struct** | `store.RuntimeBroker` |
| **File** | `pkg/store/models.go:390` |
| **Store file** | `pkg/store/entadapter/project_store.go` |
| **Create** | Lines 618–679 — sets `CreatedBy` (line 648) |
| **Update** | Lines 709–775 — omits `CreatedBy` |
| **Immutable fields** | `CreatedBy` |
| **Why immutable** | Records which user registered the broker. If writable via Update, any authenticated user with broker-update access could claim ownership of a broker they did not register, producing ownership confusion and potentially bypassing broker-scoped authorization checks. |
| **Failure if guard fails** | Ownership confusion. The `CreatedBy` field is the only record of who registered the broker. |
| **Documented?** | **No.** No comment on `UpdateRuntimeBroker` explains the omission. The field is silently absent from the Update builder. |

### Priority 3: `store.Project`

| Attribute | Detail |
|---|---|
| **Struct** | `store.Project` |
| **File** | `pkg/store/models.go:285` |
| **Store file** | `pkg/store/entadapter/project_store.go` |
| **Create** | Lines 143–198 — sets `CreatedBy` (line 153) |
| **Update** | Lines 289–353 — omits `CreatedBy` |
| **Immutable fields** | `CreatedBy` |
| **Why immutable** | Records the original creator of the project. If writable via Update, a user with project-update access could retroactively claim creation, potentially confusing audit trails and any ownership-based authorization that relies on this field. |
| **Failure if guard fails** | Ownership confusion and audit-trail corruption. |
| **Documented?** | **No.** No comment on `UpdateProject` explains the omission. The field is silently absent from the Update builder. |

### Priority 4: `store.GitHubInstallation`

| Attribute | Detail |
|---|---|
| **Struct** | `store.GitHubInstallation` |
| **File** | `pkg/store/models.go:2016` |
| **Store file** | `pkg/store/entadapter/external_store.go` |
| **Create** | Lines 396–440 — sets `InstallationID` as the entity ID (line 423: `SetID(installation.InstallationID)`) and `CreatedAt` (line 429: `SetCreated(installation.CreatedAt)`) |
| **Update** | Lines 452–467 — omits `InstallationID` (the natural key / primary key), omits `CreatedAt` |
| **Immutable fields** | `InstallationID`, `CreatedAt` |
| **Why immutable** | `InstallationID` is a GitHub-assigned natural key that serves as the entity's primary key. It is used as a foreign key from `Project.GitHubInstallationID` and as the lookup key in `GetGitHubInstallation`, `DeleteGitHubInstallation`, and `GetInstallationForRepository`. Updating it would orphan every project linked to the old ID and break all lookups. `CreatedAt` is a provenance timestamp. |
| **Failure if guard fails** | Identity confusion. Changing the natural key breaks the referential integrity between `GitHubInstallation` and every `Project` that references it. |
| **Documented?** | **No.** No comment on `UpdateGitHubInstallation` explains the omission. The `InstallationID` is naturally protected by being the primary key in the ent schema (which means `UpdateOneID` takes it as a WHERE clause, not a SET), but this protection is an artifact of the ORM layer, not an explicit contract — a schema migration could change it. `CreatedAt` is simply absent from the Update builder with no comment. |

## Recommendation

Introduce one reflect-based guard test per entity, modelled on the existing
`project_settings_resolved_guard_test.go` pattern. Each test should:

1. **Classify every field** on the struct into `{mutable, immutable}` via an
   explicit map or two sets declared at package scope. The classification is the
   contract; the test enforces it.

2. **Assert the Update builder only touches the mutable set.** Use
   `reflect.TypeOf(StructType{}).NumField()` to enumerate all fields. For each
   field, verify that:
   - If the field is in the immutable set, it does NOT appear as a setter in the
     Update builder.
   - If the field is NOT in either set, the test fails — forcing the contributor
     who added the field to classify it explicitly.

3. **Use `reflect.TypeOf(...).NumField()` as the trip-wire.** The total field
   count acts as a canary: when a contributor adds a field to the struct, the
   enumeration loop encounters it, finds it unclassified, and fails. This is the
   property that makes the guard grow automatically with the type.

4. **Place tests alongside the store adapter tests** (e.g.
   `pkg/store/entadapter/`) where they have access to both the store model types
   and the builder functions. If builder introspection is not feasible, the test
   can instead maintain the mutable/immutable classification and assert the field
   count matches, so that any new field forces a classification decision.

5. **Add a brief doc comment** to each `Update*` function that currently lacks
   one, naming the omitted fields and the reason — following the pattern already
   established on `UpdateGCPServiceAccount`. The test is the enforcement; the
   comment is the explanation.

### Priority Order for Implementation

1. `GCPServiceAccount` — highest severity (writable authorization bypass),
   already documented, easiest to write because the comment names the exact
   fields.
2. `RuntimeBroker` — ownership field, undocumented.
3. `Project` — ownership field, undocumented.
4. `GitHubInstallation` — natural-key protection, partially protected by ORM
   primary-key semantics but not by any explicit contract.

### What This Does NOT Cover

- Fields protected by other mechanisms (e.g. `ID` fields that are primary keys
  and structurally cannot be updated via `UpdateOneID`). These are protected by
  the ORM and do not need a reflect guard.
- Computed/enriched fields that are never persisted (e.g. `AgentCount`,
  `RuntimeBrokerName`). These are populated on read and absent from both Create
  and Update builders by design.
- The `Agent` entity, which has its own optimistic-locking and state-machine
  controls. A field-classification guard may be warranted there too, but it
  requires a separate analysis of which fields are truly immutable vs.
  state-machine-controlled.

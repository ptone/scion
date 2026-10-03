# Audit update milestone 2 authorization-explain contract

## Owner decision and scope

Ptone selected option B in `conv:ce7718ed-25f7-418e-8c78-20ccd6351d30`
at `2026-10-02T23:34:28Z`. Requested-decision explain is operation-centric:
the wire request names an explicit registered `authzop.OperationID`, the closed
catalog supplies its reviewed `BasePermission`, and unknown operations or any
operation/permission/resource/action mismatch fail closed. Effective-permission
explain remains a bare-permission diagnostic loop behind a documented,
non-emitting introspection boundary and claims no operation owner.

The implementation checkpoint is
`73df83e238506b7160bdfc4a2eb578d94b3edc29`, based directly on required head
`fd4f83fb7769af4b08eed1ac06be6d2442de89d7`. It was committed and pushed to
`origin/scion/audit-update-m2` before the broader bounded checks. The round-1
correction checkpoint is `4b29b7f23472eea1add1ae31a9ab3c634baa85e5`; it
was also pushed before its post-checkpoint bounded gates. The final SHA is
reported in the direct manager handoff because a commit cannot contain its own
SHA.

The round-2 test-only correction checkpoint is
`21e8885a5e4db4cedf62eaae3a794d0ce01b90c1`; it was pushed before the
round-2 post-checkpoint bounded gates.

The campaign accepted base remains
`64a549c402fe941a9ea7702a453ecf60b0b70d94`. The restricted blocker recorded
later upstream drift at `97d02e32d15594e069612eb8aecb77e47fba97b6`;
this bounded unit intentionally did not fetch, rebase, or integrate that drift.

## Exact requested-decision contract

- `operationId` is decoded as `authzop.OperationID` and is required in default
  requested-decision mode. Empty, malformed, unsafe, dynamic, and unregistered
  values receive the same bounded, value-free HTTP 400 error.
- `authzop.Lookup` is the sole operation-first catalog lookup. It returns the
  reviewed `OperationSpec`; no reverse permission-to-operation API was added.
- The selected spec's `BasePermission` is authoritative. The legacy optional
  `permission` field remains wire-compatible only when it exactly equals that
  base permission. Mismatch fails closed without echoing either value.
- The base permission's reviewed registry resource and action must exactly
  match the request. A project resource whose `id` and supplied `projectId`
  disagree also fails closed. Resource/action/prose never select or synthesize
  an operation.
- The ordinary requested-decision `AuthzRequest` receives exactly the validated
  `OperationID`, reviewed `BasePermission`, and reviewed action. Existing
  identity selection, resource loading, decision outcome, reason prose,
  causes, provenance, redaction, errors, and audit side effects remain on the
  existing `Decide` path.
- This is an intentional compatibility break: former requested-decision calls
  that omitted `operationId`, inferred permission from resource/action, or
  supplied a mismatched permission now receive HTTP 400. Authentication and
  cross-principal authorization still precede contract evaluation as before.

## Effective-permissions introspection boundary

`authorizationEvaluationRequest` is the operation-free input to the pure
authorization kernel. Ordinary `AuthzRequest` remains the producer/emission
contract. `authorizationIntrospectionRequest` is separately operation-free,
and `introspectAuthorization` converts it only to the pure kernel type and
calls `decide`; it cannot call the emitting `Decide` wrapper or populate an
operation.

The effective-permissions loop calls only this introspection boundary. It does
not construct an ordinary `AuthzRequest`, synthesize `OperationID`, inspect the
catalog, map permission to operation, or call the ordinary emitter/producer
path. The existing permission enumeration, per-permission kernel semantics,
provenance, cross-principal redaction, comparison, filtering, and response
meaning are retained. Supplying `operationId` or `permission` in this mode is
rejected so the request cannot claim an operation owner.

The structural scanner discovers every build-selected production Go file in
`pkg/hub`, builds type-resolved package-wide function and method edges, and
walks transitive reachability from `handleExplainEffectivePermissions` through
`introspectAuthorization` to `decide`. It rejects reachable ordinary
`AuthzRequest` construction, `OperationID` synthesis or population,
permission-to-operation catalog mapping, `Decide`, ordinary decision emitters,
and audit sinks. Mutations prove direct and indirect same-file/other-file
violations fail closed. The existing package-wide producer scanner now admits
exactly the validated `contract.OperationID` population inside
`handleAuthzExplain` and continues to reject every generic or inferred
population and every authorization read of that audit metadata.

Capability projection remains separately owned. Its future path emits exactly
one bounded `projection_summary` observation, never N ordinary authorization
decisions. No projection observation is implemented here.

## RED and GREEN evidence

On the untouched required base, the focused pre-implementation command

```text
go test -count=1 -p 2 ./pkg/hub -run 'TestExplainAPI_(RegisteredOperationUsesReviewedBasePermission|OperationValidationFailsClosedWithoutValueEcho|DoesNotInferOperation|EffectivePermissionsUsesNonEmittingIntrospection)|TestEffectivePermissionIntrospectionBoundary|TestAuthzOperationLookupIsClosed'
```

failed to compile because `authzop.Lookup` and the introspection boundary did
not exist. This established RED before production changes.

Before the checkpoint push, focused normal tests passed for the requested
operation contract, value-free validation, no-inference behavior, unchanged
authentication/authorization/privacy behavior, effective-permission results,
non-emission, structural mutations, the package-wide producer guard, and
catalog validation:

```text
timeout 10m go test -count=1 -p 2 ./pkg/hub ./pkg/hub/authzop -run 'TestExplainAPI|TestEffectivePermissionIntrospectionBoundary|TestAuthzOperationLookupIsClosed|TestEveryProductionDecisionLiteralAssignsAuditReason|TestAuthorizationContractGuard|TestCatalogValidation|TestCatalogNoDuplicate|TestCatalogBasePermission'
```

Result: PASS (`pkg/hub` 9.192s, `pkg/hub/authzop` 0.022s).

## Post-push bounded validation

- Broad normal packages: `timeout 10m go test -count=1 -p 2 ./pkg/hub
  ./pkg/hub/authzop` — **INCONCLUSIVE**. It reached the hard 10-minute bound
  with no diagnostics and no process remaining. Per manager direction it was
  not restarted; the earlier focused normal PASS is preserved.
- Broad combined race invocation:

  ```text
  timeout 10m go test -count=1 -race -p 2 ./pkg/hub -run 'TestExplainAPI_(RegisteredOperationUsesReviewedBasePermission|OperationValidationFailsClosedWithoutValueEcho|DoesNotInferOperation|EffectivePermissionsUsesNonEmittingIntrospection|SuperAdmin|Self|DeniedForOtherPrincipal|MemberWithoutAuditReadCannotExplainForOthers|NoSecretLeakage|CrossPrincipalRedaction)|TestEffectivePermissionIntrospectionBoundary|TestEveryProductionDecisionLiteralAssignsAuditReason|TestAuthorizationContractGuard|TestAuthzOperationLookupIsClosed'
  ```

  **INCONCLUSIVE**. It reached the hard 10-minute bound with no diagnostics
  and no process remaining; it was not restarted.
- `timeout 10m go vet -p 2 ./pkg/hub ./pkg/hub/authzop` — PASS (no output).
- `timeout 10m go build -buildvcs=false -p 2 ./pkg/hub ./pkg/hub/authzop`
  — PASS (single scoped build, no output).
- `timeout 10m env GOGC=40 golangci-lint run
  --new-from-rev=fd4f83fb7769af4b08eed1ac06be6d2442de89d7
  --concurrency=1 ./pkg/hub/...` — **INCONCLUSIVE**. It reached the hard
  10-minute bound with no diagnostics and no process remaining; it was not
  restarted.
- Gofmt, `git diff --check`, changed-path inspection, and final ref/tree
  durability checks are recorded in the direct completion handoff.
- `make ci` and `make ci-full` were not run because the restricted brief
  prohibits them. No timed-out gate was rerun.

After those inconclusive broad attempts, the manager authorized distinct,
narrowed test-name commands (not reruns) to obtain conclusive local evidence.
All of the following used a hard 10-minute bound, `-count=1`, and `-p 2`:

- Operation acceptance, unknown/empty/unsafe/malformed rejection, reviewed
  BasePermission use, mismatch/value-free errors, no inference, and closed
  lookup:

  ```text
  timeout 10m go test -count=1 -p 2 ./pkg/hub -run '^(TestExplainAPI_RegisteredOperationUsesReviewedBasePermission|TestExplainAPI_OperationValidationFailsClosedWithoutValueEcho|TestExplainAPI_DoesNotInferOperation|TestAuthzOperationLookupIsClosed)$'
  ```

  PASS (`pkg/hub` 1.776s).

- Introspection boundary/scanner mutations, the package-wide producer guard,
  and catalog lookup invariants:

  ```text
  timeout 10m go test -count=1 -p 2 ./pkg/hub ./pkg/hub/authzop -run '^(TestEffectivePermissionIntrospectionBoundaryStructure|TestEffectivePermissionIntrospectionBoundaryRejectsMutations|TestEveryProductionDecisionLiteralAssignsAuditReason|TestAuthorizationContractGuardRejectsMutations|TestCatalogValidation|TestCatalogNoDuplicateIDs|TestCatalogBasePermissionsExist)$'
  ```

  PASS (`pkg/hub` 1.090s, `pkg/hub/authzop` 0.013s).

- Non-emitting behavior and unchanged authentication, cross-principal
  authorization, redaction/privacy, comparison, effective-permission, and
  stored-resource compatibility behavior:

  ```text
  timeout 10m go test -count=1 -p 2 ./pkg/hub -run '^(TestExplainAPI_EffectivePermissionsUsesNonEmittingIntrospection|TestExplainAPI_SuperAdmin|TestExplainAPI_Self|TestExplainAPI_DeniedForOtherPrincipal|TestExplainAPI_MemberWithoutAuditReadCannotExplainForOthers|TestExplainAPI_SuperAdminCanExplainForOthersViaDecide|TestExplainAPI_NoSecretLeakage|TestExplainAPI_CrossPrincipalRedaction|TestExplainAPI_ForbiddenIsJSON|TestExplainAPI_EffectivePermissionsMode|TestExplainAPI_ComparePrincipalID_RequiresAuditRead|TestExplainAPI_ComparePrincipalID_AdminAllowed|TestExplainAPI_HubUserReadRegression|TestExplainAPI_SkillResourceMatchesReadDecision)$'
  ```

  PASS (`pkg/hub` 5.891s).

- Race, operation validation and lookup:

  ```text
  timeout 10m go test -count=1 -race -p 2 ./pkg/hub -run '^(TestExplainAPI_RegisteredOperationUsesReviewedBasePermission|TestExplainAPI_OperationValidationFailsClosedWithoutValueEcho|TestExplainAPI_DoesNotInferOperation|TestAuthzOperationLookupIsClosed)$'
  ```

  PASS (`pkg/hub` 34.510s).

- Race, non-emitting introspection plus both structural guards/mutations:

  ```text
  timeout 10m go test -count=1 -race -p 2 ./pkg/hub -run '^(TestExplainAPI_EffectivePermissionsUsesNonEmittingIntrospection|TestEffectivePermissionIntrospectionBoundaryStructure|TestEffectivePermissionIntrospectionBoundaryRejectsMutations|TestEveryProductionDecisionLiteralAssignsAuditReason|TestAuthorizationContractGuardRejectsMutations)$'
  ```

  PASS (`pkg/hub` 22.948s).

- Race, authentication/authorization/privacy compatibility:

  ```text
  timeout 10m go test -count=1 -race -p 2 ./pkg/hub -run '^(TestExplainAPI_SuperAdmin|TestExplainAPI_Self|TestExplainAPI_DeniedForOtherPrincipal|TestExplainAPI_MemberWithoutAuditReadCannotExplainForOthers|TestExplainAPI_NoSecretLeakage|TestExplainAPI_CrossPrincipalRedaction|TestExplainAPI_ForbiddenIsJSON|TestExplainAPI_ComparePrincipalID_RequiresAuditRead)$'
  ```

  PASS (`pkg/hub` 99.398s).

These narrowed normal and race passes conclusively cover every changed explain
contract path and the race-sensitive emitter/introspection and privacy paths;
they do not change the accurate inconclusive classification of the earlier
broad attempts.

## Independent review round 1 corrections

Round 1 requested changes at reviewed SHA
`0d0d9d7bc92117b1757fe0cd92c8ab4d4f2ea2b4`; the complete review artifact was
read from `m2-explain-r1.md` with SHA-256
`cf1bae6ece362b58c0b4a9a062c5517cdb2b1fd32359af20f9d4f54ff1defeff`.
The correction is limited to its three required findings:

- package-wide, transitive, type-resolved structural enforcement and mutations
  for every requested ordinary authorization/audit escape path;
- a positive HTTP regression proving a non-empty legacy `permission` exactly
  matching the selected operation's reviewed `BasePermission` remains accepted
  and returns that reviewed permission; and
- the exact timed-out broad race command and consistent **INCONCLUSIVE**
  classification above. The broad normal, broad race, and lint commands remain
  disclosed as inconclusive and were not rerun.

The initial scanner mutation RED command was:

```text
timeout 10m go test -count=1 -p 2 ./pkg/hub -run '^TestEffectivePermissionIntrospectionBoundaryRejectsMutations$'
```

It failed because the prior file-local scanner accepted the indirect helper
mutation (`indirect_helper: An error is expected but got nil`). After replacing
the scanner, the first package-wide implementation correctly exposed an
over-conservative name-only edge (`Validate`/`New` receiver conflation). The
scanner was corrected to use Go type-resolved call edges. The distinct
structural-only correction command then passed:

```text
timeout 10m go test -count=1 -p 2 ./pkg/hub -run '^(TestEffectivePermissionIntrospectionBoundaryStructure|TestEffectivePermissionIntrospectionBoundaryRejectsMutations)$'
```

Result: PASS (`pkg/hub` 13.869s).

After pushing correction checkpoint
`4b29b7f23472eea1add1ae31a9ab3c634baa85e5`, the proportionate round-1 gates
were:

- Exact focused normal regression and structural/mutation coverage:

  ```text
  timeout 10m go test -count=1 -p 2 ./pkg/hub -run '^(TestExplainAPI_RegisteredOperationUsesReviewedBasePermission|TestExplainAPI_OperationValidationFailsClosedWithoutValueEcho|TestEffectivePermissionIntrospectionBoundaryStructure|TestEffectivePermissionIntrospectionBoundaryRejectsMutations)$'
  ```

  PASS (`pkg/hub` 13.274s).

- Distinct focused race coverage for those same corrected paths:

  ```text
  timeout 10m go test -count=1 -race -p 2 ./pkg/hub -run '^(TestExplainAPI_RegisteredOperationUsesReviewedBasePermission|TestExplainAPI_OperationValidationFailsClosedWithoutValueEcho|TestEffectivePermissionIntrospectionBoundaryStructure|TestEffectivePermissionIntrospectionBoundaryRejectsMutations)$'
  ```

  PASS (`pkg/hub` 50.834s).

- `timeout 10m go vet -p 2 ./pkg/hub` — PASS (no output).

The already-consumed scoped build was not repeated. The earlier broad normal,
broad race, and lint timeouts remain **INCONCLUSIVE**, and none was restarted.

## Independent review round 2 corrections

Round 2 requested changes at reviewed SHA
`3eedea41b169041042ed34bf67e76c259ad6f795`. The complete direct fix brief had
SHA-256 `c26b74c20646f13eb572d4929d14bb8bff7a9db8a8f638cafadc2beea60e62d9`,
and the complete review report had verified SHA-256
`1c6f46f38513543f530fcefe31581e4b3e58233018ba1a6bd018cf11ec4eba67`.
The correction is test-only:

- function-value assignments, dependencies, callable fields, function
  literals, and callable arguments/parameters are resolved transitively;
  unsupported dynamic callable dispatch fails closed;
- ordinary `AuthzRequest` presence is detected by resolved Go type, including
  pointers and aliases, rather than only direct composite-literal spelling;
- mutations now cover an indirect other-file function value,
  `new(AuthzRequest)`, a typed `AuthzRequest` declaration, and aliased
  construction while preserving every earlier mutation; and
- the matching legacy-permission response is decoded into a fresh response
  value before optional provenance assertions.

With only the four new mutations added and scanner logic unchanged, the exact
RED command was:

```text
timeout 10m go test -count=1 -p 2 ./pkg/hub -run '^TestEffectivePermissionIntrospectionBoundaryRejectsMutations$'
```

It failed because all four new cases returned nil instead of the required
error: `indirect_other-file_function_value`,
`ordinary_AuthzRequest_allocation`,
`ordinary_AuthzRequest_typed_declaration`, and
`ordinary_AuthzRequest_alias_construction` (`pkg/hub` 0.398s).

After correction, the exact pre-checkpoint GREEN command was:

```text
timeout 10m go test -count=1 -p 2 ./pkg/hub -run '^(TestExplainAPI_RegisteredOperationUsesReviewedBasePermission|TestEffectivePermissionIntrospectionBoundaryStructure|TestEffectivePermissionIntrospectionBoundaryRejectsMutations)$'
```

PASS (`pkg/hub` 18.870s). The test-only checkpoint
`21e8885a5e4db4cedf62eaae3a794d0ce01b90c1` was then committed and pushed.

Post-checkpoint bounded evidence:

- The same exact focused normal command above — PASS (`pkg/hub` 25.953s).
- Distinct focused race command:

  ```text
  timeout 10m go test -count=1 -race -p 2 ./pkg/hub -run '^(TestExplainAPI_RegisteredOperationUsesReviewedBasePermission|TestEffectivePermissionIntrospectionBoundaryStructure|TestEffectivePermissionIntrospectionBoundaryRejectsMutations)$'
  ```

  PASS (`pkg/hub` 42.387s).
- `timeout 10m go vet -p 2 ./pkg/hub` — PASS (no output).
- Gofmt and `git diff --check` — PASS. The checkpoint production-diff proof
  `git diff --exit-code 3eedea41b169041042ed34bf67e76c259ad6f795 --
  '*.go' ':!pkg/hub/authz_explain_operation_contract_test.go'` — PASS (no
  output).

No production file changed in round 2. The already-consumed scoped build was
not rerun. The earlier broad normal, broad combined race, and scoped lint
timeouts remain **INCONCLUSIVE**; none was restarted. `make ci` and
`make ci-full` were not run.

## Changed scope and exclusions

Production changes are limited to the explain API/producer contract, the
operation-free kernel/introspection type boundary, exact internal helper type
threading, and the closed catalog lookup. Tests cover those surfaces and update
former canonicalization expectations to the explicit operation contract.

No catalog operation was added or modified. This unit adds none of the 16
candidate operations from the catalog blocker, performs no P2 package-wide
producer propagation, and does not resume the 43-producer inventory. It adds
no authorization-decision emitter/slog cutover, sink/store/schema/history,
sampling, transport, projection implementation, API/UI outside explain,
M1 change, or `#2392 audit_emit_dispatch` timing change. It performs no
upstream push, PR, compare link, or merge-queue notice.

The catalog/P2 handoff remains: independently review this contract first;
catalog extension and P2 resume only after that gate. The projection handoff
remains the single bounded `projection_summary` observation described above.

## Independent review round 4 corrections

Round 4 reviewed immutable head
`5a8ace9fdeacceecc46792a8ca3fa31dfb897f56` against accepted P1 base
`fd4f83fb7769af4b08eed1ac06be6d2442de89d7`. The protected review report
`m2-explain-r4.md` had verified SHA-256
`e71f870a1d28c1e5b070d25c76a61e292a2ae48b4827992373353a4e7f5556fe`.
This test-only correction resolves both reported scanner findings:

- the nested module-export command uses `exec.CommandContext` with an
  eight-minute child deadline, strictly shorter than the 20-minute outer gate,
  and a two-second `WaitDelay`; deadline, cancellation, execution, and output
  drain failures are phase-classified without command output or source values;
- deterministic helper-process regressions exercise deadline, live
  cancellation, successful bounded export loading, and prove `Output` returned
  a non-nil `ProcessState` after waiting for the child;
- every `types.Config.Error` callback and every non-nil `Config.Check` error is
  rejected before a partial package or incomplete `types.Info` can be used;
  injected regressions cover callback-only failure and a non-nil check error
  returned together with a partial package; and
- all prior interface-dispatch, direct/helper/function-value, request,
  operation/mapping, emitter/sink mutations, and positive safe-dispatch tests
  remain in the single required filter.

The exact pre-implementation focused RED attempt was:

```text
ulimit -v 8000000; start_seconds=$SECONDS; timeout 2m env GOMEMLIMIT=4GiB GOCACHE=/scion-volumes/gocache go test -count=1 -p 2 ./pkg/hub -run '^(TestEffectivePermissionIntrospectionBoundaryImporterIsBounded|TestEffectivePermissionIntrospectionBoundaryTypeErrorsFailClosed)$'; command_rc=$?; echo COMMAND_EXIT=$command_rc; echo WALL_SECONDS=$((SECONDS-start_seconds)); exit $command_rc
```

Result: **INCONCLUSIVE**. The captured output contained dependency downloads
only and did not contain compiler diagnostics, the wrapper exit status, or wall
time. It was not treated as RED or GREEN and was not retried. The shared cache
was not cleaned, relocated, replaced, or manually altered.

The code/test durability checkpoint was committed and pushed before the
post-implementation gates as
`39c1106327f9145c268d1d243b49e85b60d5382c`.

The exact single mandatory normal command was:

```text
ulimit -v 8000000; start_seconds=$SECONDS; timeout 20m env GOMEMLIMIT=4GiB GOCACHE=/scion-volumes/gocache go test -count=1 -p 2 ./pkg/hub -run '^(TestExplainAPI_(RegisteredOperationUsesReviewedBasePermission|OperationValidationFailsClosedWithoutValueEcho|DoesNotInferOperation|EffectivePermissionsUsesNonEmittingIntrospection)|TestEffectivePermissionIntrospectionBoundaryStructure|TestEffectivePermissionIntrospectionBoundaryRejectsMutations|TestEffectivePermissionIntrospectionBoundaryAllowsSafeInterfaceDispatch|TestEffectivePermissionIntrospectionBoundaryImporterIsBounded|TestEffectivePermissionIntrospectionBoundaryTypeErrorsFailClosed|TestAuthzOperationLookupIsClosed)$'; command_rc=$?; echo COMMAND_EXIT=$command_rc; echo WALL_SECONDS=$((SECONDS-start_seconds)); exit $command_rc
```

Result: **INCONCLUSIVE**. It emitted no test/package diagnostics and no explicit
`ok github.com/GoogleCloudPlatform/scion/pkg/hub` line, then the original
wrapper reported exactly:

```text
COMMAND_EXIT=124
WALL_SECONDS=1200
```

A read-only process snapshot at 894 seconds showed the original shell,
`timeout`, and `go test` processes plus the Go linker; no `hub.test` or nested
`go list -deps -export` child existed yet. After exit, a second read-only
process check found no timeout, Go test, Hub test, or nested importer child.
This proves the outer timeout reaped the command tree, but the required tests
never started and the importer regressions did not execute in this gate.

Per the round-4 termination contract, the normal command was not retried and
the race command was not run. Scoped vet was not run after the inconclusive
required gate; no lint, broad build, full Hub suite, unfiltered package test,
`make ci`, or `make ci-full` was run. Gofmt and `git diff --check` passed before
the checkpoint. Final path/scope, zero-production-diff, ancestry, remote
equality, clean-tree, and evidence hashes are recorded in the protected direct
handoff because a commit cannot contain its own SHA.

The round-4 correction changes only
`pkg/hub/authz_explain_operation_contract_test.go` and this project log. There
is zero production diff. It does not change option-B production behavior,
catalog ownership, P2 propagation, emitters/slog/sinks, store/schema/history,
sampling, transport, projection, M1, or `#2392 audit_emit_dispatch` timing.
Catalog, P2, emitter, compare, and merge actions remain blocked pending a
conclusive normal and race GREEN plus fresh independent explain review.

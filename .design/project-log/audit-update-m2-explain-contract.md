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

## Independent review round 5 correction and gate result

Round 5 reviewed immutable head
`a8f324e313310c3bac00b8108dd60f93e6638f0b` against accepted P1 base
`fd4f83fb7769af4b08eed1ac06be6d2442de89d7`. Before repository access, the
protected round-5 report `m2-explain-r5.md` was verified at SHA-256
`b1a6f0ac17b437e79cfcf824c6f72512716f0d3d6e9b52298efcad49040ea150`,
and the protected round-4 fix report `m2-explain-r4-fix.md` was verified at
SHA-256 `23a8f685911e337f280aa9b184f132a8f2ab0ae8ee8b0d12292385139e02da3a`.

The sole round-5 finding was the eight-minute real module-export deadline,
which expired in the cold review environment. The correction keeps the child
context-aware and bounded, keeps synchronous `Output` waiting/reaping and
value-free phase classification, and keeps `sync.Once` success and failure
caching. It separates the injected short test bounds from a 30-minute real
`go list -deps -export` default, below the 44-minute Go-test timeout and
45-minute outer wrapper. Instance-scoped cache regressions prove that a
successful helper/importer load occurs once and remains usable by subsequent
structure and safe-interface-dispatch validation, while a reaped deadline
failure occurs once, is returned from the cache without a second child, and
fails closed before type checking. The production-cache structure and
safe-dispatch tests also assert one real load attempt. Existing cancellation,
success, interface/direct/helper/function-value/request/operation/mapping/
emitter/sink mutation, positive safe-dispatch, and R2 type-error regressions
remain selected.

The test-only checkpoint was committed and pushed before the mandatory gate as
`f93120864b4e96539ba33915df28eafa651a9fac`. It changes only
`pkg/hub/authz_explain_operation_contract_test.go`.

The single pre-implementation focused RED attempt used the required
`ulimit -v 8000000`, `GOMEMLIMIT=4GiB`, immutable shared
`GOCACHE=/scion-volumes/gocache`, and `-p 2` controls:

```text
timeout 5m env GOMEMLIMIT=4GiB GOCACHE=/scion-volumes/gocache go test -timeout 4m -count=1 -p 2 ./pkg/hub -run '^TestEffectivePermissionIntrospectionBoundaryImporterIsBounded$'
```

It emitted dependency-download lines only and no compiler, test, package, or
wrapper result through the command channel. It is **INCONCLUSIVE** and was not
retried. The single post-implementation helper-only GREEN attempt used the
same controls with a ten-minute outer and nine-minute internal timeout. It
emitted no package diagnostic and surfaced exactly `COMMAND_EXIT=124` and
`WALL_SECONDS=4294`; it is also **INCONCLUSIVE** and was not retried. These
helper regressions use injected bounds and helper processes rather than the
30-minute real default.

The exact single mandatory normal command was:

```text
ulimit -v 8000000; start_seconds=$SECONDS; timeout 45m env GOMEMLIMIT=4GiB GOCACHE=/scion-volumes/gocache go test -timeout 44m -count=1 -p 2 ./pkg/hub -run '^(TestExplainAPI_(RegisteredOperationUsesReviewedBasePermission|OperationValidationFailsClosedWithoutValueEcho|DoesNotInferOperation|EffectivePermissionsUsesNonEmittingIntrospection)|TestEffectivePermissionIntrospectionBoundaryStructure|TestEffectivePermissionIntrospectionBoundaryRejectsMutations|TestEffectivePermissionIntrospectionBoundaryAllowsSafeInterfaceDispatch|TestEffectivePermissionIntrospectionBoundaryImporterIsBounded|TestEffectivePermissionIntrospectionBoundaryTypeErrorsFailClosed|TestAuthzOperationLookupIsClosed)$'; command_rc=$?; echo COMMAND_EXIT=$command_rc; echo WALL_SECONDS=$((SECONDS-start_seconds)); exit $command_rc
```

Result: **FAIL**, with the exact decisive tail:

```text
--- FAIL: TestEffectivePermissionIntrospectionBoundaryStructure (603.23s)
    authz_explain_operation_contract_test.go:176:
        Error: Received unexpected error:
               effective-permissions boundary type checking reported errors
FAIL
FAIL github.com/GoogleCloudPlatform/scion/pkg/hub 608.084s
FAIL
COMMAND_EXIT=1
WALL_SECONDS=1541
```

A read-only snapshot showed compilation completed and `hub.test` started after
about 15.5 minutes; the real `go list -deps -export` child then ran beyond the
previous eight-minute limit and completed without the new 30-minute deadline
firing. The remaining failure is the preserved value-free R2 fail-closed
diagnostic for one or more `types.Config.Error` callbacks emitted by
`types.Config.Check` while checking the build-selected production `pkg/hub`
source set, after module-aware export loading and before any partial package or
`types.Info` use. The individual diagnostic class and source location were
intentionally collapsed by the accepted R2 aggregator and were not captured;
no narrower attribution is evidence-supported without a prohibited rerun or
instrumentation change. No other selected test emitted a failure line. The
failure is conclusive and was not retried.

Race was not run because normal did not exit 0 with an explicit package `ok`.
Additionally, the coordinator and engineering manager issued a superseding
broker-01 settlement rule while normal was running: no `-race` or other heavy
Go command may start on broker-01; race is deferred to scion-community or CI.
Scoped vet was therefore also not run. No full Hub suite, unfiltered package
test, prewarm, retry, lint, heavy build, `make ci`, or `make ci-full` was run.
The shared cache was not cleaned, altered, relocated, or replaced, and `/tmp`
was not used for GOCACHE.

Post-gate gofmt and `git diff --check` pass. The checkpoint range has zero
production diff and only the authorized Go test path. R2 remains unchanged:
every callback error and every non-nil `Check` error is rejected before any
partial package or `types.Info` use. Option-B production behavior, catalog,
P2, emitter/slog/sinks, store/schema/history, sampling, transport, projection,
M1, and `#2392 audit_emit_dispatch` timing are unchanged. Catalog, P2,
emitter, compare, and merge work remains blocked; round 6/7 is not authorized
without conclusive normal and deferred race GREEN.

## Round-5 type-check diagnostic correction and gate result

The replacement author received the brief, round-5 fix report, and round-5
review as complete authoritative bodies in a direct private conversation
because the protected source and attachment paths were unavailable in its
container. All document boundaries were complete. The expected source hashes
were retained as provenance rather than represented as independently
recomputed file hashes:

- brief: `ce4d90c18501c9b4e8edddf94ef89b080993c381a8fb0db599d656a5323fac78`;
- round-5 fix report:
  `ee911dcc5fddc7ec627276bb5a18a90046cfdeb45b935b631bb67358a47151cb`;
- round-5 review:
  `b1a6f0ac17b437e79cfcf824c6f72512716f0d3d6e9b52298efcad49040ea150`.

Before edits, local HEAD, the local branch ref, tracking ref, and independent
fork remote ref all equaled pinned head
`b79bd1428a61db765f9bf4ae2dc5248354a5a3d7`; the tree was clean. The accepted
base was absent from the shallow clone, so no fetch or local ref mutation was
performed. An independent fork compare proved merge base
`fd4f83fb7769af4b08eed1ac06be6d2442de89d7`, status ahead, 16 commits ahead,
zero behind, and exactly the 14 accepted paths. A second independent compare
from the prior review head to the pinned head showed two commits and only this
test file plus this project log.

Lightweight, resource-bounded package metadata inspection showed 325 compiler
Go files, zero cgo files, and no package metadata error. The scanner-selected
and compiler-selected production filename sets had the same count and the same
sorted SHA-256. Static inspection found no production build constraints or cgo
source. This ruled out source-set drift and narrowed the failure to the
checker/import boundary.

With explicit lead authorization, exactly one diagnostic-only heavy command
ran against only the production structure test, with shell
`ulimit -v 8000000`, `GOMEMLIMIT=4GiB`, immutable shared
`GOCACHE=/scion-volumes/gocache`, `-p 2`, no tags, a 44-minute Go timeout, and
a 45-minute outer timeout. Temporary uncommitted instrumentation retained only
bounded categories and counts. It exited 1 after 442 wall seconds with 426
hard, zero soft, package-local callbacks: 160 import-resolution callbacks, 231
unresolved-reference cascades, zero declaration, assignability, or operation
callbacks, and 35 other callbacks. No raw diagnostic text, source value,
unsafe identifier, or path was retained or copied into durable evidence. The
command was not retried.

The aggregate evidence exposed a serialization mismatch: the module export
command's format argument emitted a literal escaped separator while the export
parser required an actual tab. The resulting empty export map caused import
resolution failures and downstream unresolved-reference callbacks. The
test-only correction centralizes the format contract, emits an actual tab,
extracts the parser into a small pure helper, and adds a deterministic
regression proving that the command carries the actual-tab format, the parser
accepts that protocol, and a literal escaped separator is rejected. It does
not suppress callbacks, accept partial type data, or weaken any fail-closed
path.

Before checkpoint, a targeted search proved all temporary instrumentation was
absent. `gofmt` and `git diff --check` passed; the sole changed path was
`pkg/hub/authz_explain_operation_contract_test.go`; and the production diff
was empty. The test-only checkpoint was committed and pushed before validation
as `192cfea`.

The single prescribed full filtered normal gate then ran with the same memory,
cache, parallelism, tag, and 44/45-minute timeout controls. It exited 1 after
383 wall seconds with package `FAIL`. The corrected import protocol completed
and production type checking passed, after which the scanner reached its
semantic closure and failed closed on an incomplete package-local interface
dispatch. This is a conclusive new scanner-closure failure, not a timeout or
inconclusive infrastructure result. The command was not retried. Race was not
run because normal did not exit 0 with the required explicit package `ok`.

No other heavy Go command, full Hub suite, unfiltered test, prewarm, retry,
lint, vet, heavy build, `make ci`, or `make ci-full` ran. The shared cache was
not cleaned, altered, relocated, or replaced. The correction changes only this
project log and the authorized Go test file, with zero production diff. All
option-B behavior and the catalog/P2, emitter/slog/sink, store/schema/history,
sampling, transport, projection, M1, and `#2392 audit_emit_dispatch` exclusions
remain unchanged. The pushed checkpoint is preserved; race and fresh review
remain blocked pending correction of the newly exposed scanner-closure
failure.

## Package-local interface-closure correction and gates

The retained author received the complete continuation brief inline in the
direct private conversation. All BEGIN/BODY/END boundaries and required
sections were present. Its source SHA-256
`0b56751595d89a18af5da71b1c432cf839810299e13e989fa0781a481623b0a7`
is recorded as message provenance, not an independently recomputed file hash.
The prior authoritative report body remained available in the same direct
conversation at source SHA-256
`64ca1d295a1b9c164765fefcc475b22c8cb07b1d948b2276b16e304c253ec496`.

Before work, HEAD, the local branch ref, tracking ref, and independent remote
ref all equaled pinned continuation head
`9c45b7ff2d23abdf884f31b390495b191c2fe1cf`; the tree was clean on
`scion/audit-update-m2`.

Static analysis was conclusive, so the optional diagnostic allowance was not
used. The failing package-local interface contains a package-private method
and has zero production implementors; its documented implementors are test
fakes excluded from the production source set. The resolver enumerated every
package-local named value and pointer method set but incorrectly equated an
empty target set with an incomplete target set.

The correction uses a type-system closure proof rather than an allowlist. If
an interface contains a package-private method owned by the checked package,
outside packages cannot declare that method. An outside wrapper may only
promote an existing implementation, whose package-local executable body is
already included by method-set enumeration. The complete package-local target
set may therefore be genuinely empty. Interfaces without that proof retain
the existing incomplete-set failure. Enumeration still covers value and
pointer receivers, promoted concrete methods, and every package-local
implementor; no possible executable target is inferred away or dropped.

Deterministic regressions add:

- an empty package-closed target set that is accepted;
- a package-closed multiple-implementor case whose unsafe pointer-receiver
  target is still found and rejected; and
- a package-closed promoted concrete-method case that is completely resolved.

The existing exported-method empty-set rejection, safe `Identity.Type`,
same/other-file value and pointer implementations, multiple implementations,
forbidden direct/helper/function-value/request/operation/emitter/sink cases,
actual-tab importer protocol, bounded child execution and reaping,
instance-scoped caching, and fail-closed callback/Check ordering remain.

Before validation, `gofmt` and `git diff --check` passed, the sole changed path
was `pkg/hub/authz_explain_operation_contract_test.go`, and production diff
was empty. The test checkpoint was committed and pushed as `3ddac9d`.

The exact one-shot normal command was:

```text
ulimit -v 8000000; start_seconds=$SECONDS; timeout 45m env GOMEMLIMIT=4GiB GOCACHE=/scion-volumes/gocache go test -timeout 44m -count=1 -p 2 ./pkg/hub -run '^(TestExplainAPI_(RegisteredOperationUsesReviewedBasePermission|OperationValidationFailsClosedWithoutValueEcho|DoesNotInferOperation|EffectivePermissionsUsesNonEmittingIntrospection)|TestEffectivePermissionIntrospectionBoundaryStructure|TestEffectivePermissionIntrospectionBoundaryRejectsMutations|TestEffectivePermissionIntrospectionBoundaryAllowsSafeInterfaceDispatch|TestEffectivePermissionIntrospectionBoundaryImporterIsBounded|TestEffectivePermissionIntrospectionBoundaryTypeErrorsFailClosed|TestAuthzOperationLookupIsClosed)$'; command_rc=$?; echo COMMAND_EXIT=$command_rc; echo WALL_SECONDS=$((SECONDS-start_seconds)); exit $command_rc
```

Normal result: **GREEN**, explicit
`ok github.com/GoogleCloudPlatform/scion/pkg/hub`, package time 207.855 seconds,
`COMMAND_EXIT=0`, `WALL_SECONDS=366`. It was not retried.

The exact one-shot race command added only `-race` to that invocation:

```text
ulimit -v 8000000; start_seconds=$SECONDS; timeout 45m env GOMEMLIMIT=4GiB GOCACHE=/scion-volumes/gocache go test -timeout 44m -race -count=1 -p 2 ./pkg/hub -run '^(TestExplainAPI_(RegisteredOperationUsesReviewedBasePermission|OperationValidationFailsClosedWithoutValueEcho|DoesNotInferOperation|EffectivePermissionsUsesNonEmittingIntrospection)|TestEffectivePermissionIntrospectionBoundaryStructure|TestEffectivePermissionIntrospectionBoundaryRejectsMutations|TestEffectivePermissionIntrospectionBoundaryAllowsSafeInterfaceDispatch|TestEffectivePermissionIntrospectionBoundaryImporterIsBounded|TestEffectivePermissionIntrospectionBoundaryTypeErrorsFailClosed|TestAuthzOperationLookupIsClosed)$'; command_rc=$?; echo COMMAND_EXIT=$command_rc; echo WALL_SECONDS=$((SECONDS-start_seconds)); exit $command_rc
```

Race result: **GREEN**, explicit
`ok github.com/GoogleCloudPlatform/scion/pkg/hub`, package time 238.732 seconds,
`COMMAND_EXIT=0`, `WALL_SECONDS=503`, with no race report. It was not retried.

Both commands used Go 1.26.1, no tags, shell `ulimit -v 8000000`,
`GOMEMLIMIT=4GiB`, immutable shared `GOCACHE=/scion-volumes/gocache`, `-p 2`,
and the required 44/45-minute limits. They ran sequentially. No other heavy Go
command, full or unfiltered Hub test, prewarm, retry, lint, heavy build,
`make ci`, or `make ci-full` ran, and the shared cache was not cleaned, altered,
relocated, or replaced.

The correction remains limited to this project log and the authorized Go test
file with zero production diff. Option-B behavior, catalog/P2,
emitter/slog/sinks, store/schema/history, sampling, transport, projection, M1,
and `#2392 audit_emit_dispatch` timing are unchanged. Both required gates are
conclusively GREEN, permitting fresh independent explain review round 6/7;
compare and merge actions remain out of scope.

## Round-6 semantic alias and data-flow closure

The fresh round-6 author received the complete restricted brief and review
inline in direct conversation `2fcb3617-1e7a-464f-b4fd-0392f0f1ea49` before
repository access. Every BEGIN/BODY/END boundary was present and both bodies
were read completely. Their SHA-256 values are retained as source provenance,
not claimed as independent recomputations because raw source files were not
exposed:

- brief: `d42d32b831aa88859ef6f680a46c71e4f63bfd7d89fd9a90ce1ae6b5c2804ebc`;
- round-6 review:
  `09a284585c328e907a835c82e81876309442b00948c1244bbd0d871b6b93e608`.

Restricted content was not sent through GCS or exchange. Before edits, the
workspace was `clone-per-agent`, the tree was clean, and local HEAD, the local
branch, tracking ref, and independent fork remote ref all equaled pinned head
`7d5b3352e81f67d5e16c76eed709698739ddb84e`. Independent fork comparison
proved accepted base and merge base
`fd4f83fb7769af4b08eed1ac06be6d2442de89d7`, status ahead, 20 commits ahead,
zero behind, and exactly the documented 14-path M2 scope. A second independent
compare from `9c45b7ff2d23abdf884f31b390495b191c2fe1cf` proved the continuation had two
commits and only the scanner test plus this project log.

The correction replaces authzop source-spelling checks for catalog and lookup
access with resolved `go/types` identity and declaring-package provenance.
Every nonlocal function or method declared by the authzop package is a
forbidden semantic target even after import renaming, function-value aliasing,
or method-expression capture. Package-scope authzop variables and constants
are forbidden semantic data origins. Fields are not rejected by spelling;
they inherit origin provenance through a transitive object-dependency graph.

The graph records value-spec and assignment dependencies, composite/aggregate
field values, multi-result call values, function return results, formal
parameters, range variables, fields, indexing, and nested call/search
expressions. Reachable expressions recursively resolve those dependencies and
fail closed on any authzop operation/catalog origin. Existing unsupported
dynamic-call, unresolved literal, incomplete interface, type-error, ordinary
request, and forbidden authorization/audit failures remain fail closed.

Deterministic malicious mutations cover: renamed-import `Lookup` function
alias; nonlocal authzop method expression; package catalog alias and range;
local assignment plus indexing; nested collection/aggregate; function return;
formal parameter; struct field; `slices.IndexFunc` search; and an authzop
operation constant alias. The prior direct mapping mutation now uses the real
renamed authzop package object. A safe negative carries an unrelated
`strings.Index` alias and ordinary collection data through package variables,
aggregate fields, return, parameter, assignment, range, and index operations.
All prior interface, importer, type-check, child-bound/reaping/cache,
function-value, request-construction, mutation, and safe-positive cases remain.

Before validation, `gofmt` and `git diff --check` passed, the sole changed path
was `pkg/hub/authz_explain_operation_contract_test.go`, and production diff was
empty. The test-only checkpoint was committed and pushed as `9e80e6a`.

The exact one-shot normal command was:

```text
ulimit -v 8000000; start_seconds=$SECONDS; timeout 45m env GOMEMLIMIT=4GiB GOCACHE=/scion-volumes/gocache go test -timeout 44m -count=1 -p 2 ./pkg/hub -run '^(TestExplainAPI_(RegisteredOperationUsesReviewedBasePermission|OperationValidationFailsClosedWithoutValueEcho|DoesNotInferOperation|EffectivePermissionsUsesNonEmittingIntrospection)|TestEffectivePermissionIntrospectionBoundaryStructure|TestEffectivePermissionIntrospectionBoundaryRejectsMutations|TestEffectivePermissionIntrospectionBoundaryAllowsSafeInterfaceDispatch|TestEffectivePermissionIntrospectionBoundaryImporterIsBounded|TestEffectivePermissionIntrospectionBoundaryTypeErrorsFailClosed|TestAuthzOperationLookupIsClosed)$'; command_rc=$?; echo COMMAND_EXIT=$command_rc; echo WALL_SECONDS=$((SECONDS-start_seconds)); exit $command_rc
```

Normal result: **INCONCLUSIVE**. The command emitted dependency-download lines
while populating the immutable shared cache, then no compiler, test, package,
or failure diagnostic. The outer timeout produced exactly
`COMMAND_EXIT=124`, `WALL_SECONDS=2700`; there was no explicit package `ok`.
The command was not retried.

While normal was active, the coordinator and lead issued a superseding
capacity rule: allow that command to finish untouched, do not start `-race`
even if normal is green, and complete only static/project-log/report/ref/tree
durability. Race is therefore **NOT RUN / DEFERRED** pending separate placement
or explicit coordinator authorization. No other heavy Go command, full or
unfiltered Hub test, prewarm, retry, lint, vet, build, `make ci`, or
`make ci-full` ran. The shared cache was not cleaned, altered, relocated, or
replaced.

The change remains limited to this project log and the authorized Go test file
with zero production diff. Option-B production behavior, catalog/P2,
emitter/slog/sinks, store/schema/history, sampling, transport, projection, M1,
and `#2392 audit_emit_dispatch` timing are unchanged. Fresh round 7/7 is not
permitted because both gates are not conclusively green. Catalog, P2, emitter,
compare, and merge work remain blocked.

## Round-6 forbidden-data resolver performance correction

The retained author received the complete protected performance-fix brief in
direct conversation `e7685861-d25c-4260-90bf-378e5500f6fd` before repository
access. The brief SHA-256 was
`d41391c0bd25eaf0505b3dc4167db5e5109d1e426b5abda97aa98a1da22b5067`.
The complete validation finding for `m2-explain-r6-validation.md` was received
with SHA-256
`1d3e24af2d5ece71e3ef6e407707c2cc41407094884d85090f2b457f217a7681`
and size 4,879 bytes. Launch used `--harness codex` plus
`--model gpt-5.6-sol` and displayed GPT-5.6-Sol medium; the lead accepted this
as equivalent to the late model-only configuration instruction. The required
`artifact-durability` skill was unavailable; the other three mandated skills
were read completely in order and the brief authorized continuing with that
reported non-repository-precondition gap.

Before edits, the workspace was a clean `clone-per-agent` Git clone on
`scion/audit-update-m2`. Local HEAD, the local branch, tracking ref, and the
independent fork remote ref all equaled pinned head
`82ce8b1d3436d4a9f12d59e8a41d79775366d61c`. Accepted P1 base
`fd4f83fb7769af4b08eed1ac06be6d2442de89d7` was an ancestor, the branch diff
contained the approved 14 paths, and the exact prior correction commit touched
only this project log.

The independent validation-only run conclusively failed: the filtered normal
command ran `TestEffectivePermissionIntrospectionBoundaryStructure` for
1h28m59s, the package failed at 5340.217 seconds, the command exited 1 after
5364 wall seconds, and 84 printed recursive frames identified
`explainBoundaryResolveForbiddenDataOrigin`. One CPU-bound `hub.test` process
had no children and flat 392544 kB RSS. No package `ok` was emitted and race
did not run. This supersedes the prior 45-minute infrastructure-inconclusive
normal result for disposition.

The correction replaces path-count re-walk with a validation-scoped resolver
keyed only by semantic `types.Object` identity. A distinct in-progress set
detects cycles. A distinct completed-result cache stores a node only after all
of its dependencies fully resolve; partial, missing, or in-progress results
are never cached. Cached results retain both safe completion and any forbidden
authzop semantic origin. The same completed cache is shared across every
expression root in one boundary-validation pass. Cycles and incomplete
dependencies remain fail closed, while forbidden origin truth continues to
propagate through the existing assignment, return, parameter, aggregate,
field, index, range, search, callable, and method-expression graph.

Deterministic regressions construct a 20-level shared DAG and prove resolution
work equals the number of unique semantic nodes across multiple roots; prove a
dependency cycle terminates without creating a completed cache entry; prove a
forbidden package-scope authzop origin survives a shared DAG and memoization;
and prove an incomplete dependency is rejected on every attempt and never read
as a successful completed result. The existing malicious mutation matrix and
unrelated callable/data-flow safe negative remain unchanged.

Before the checkpoint, `gofmt` and `git diff --check` passed. The diff was
limited to the authorized scanner test and this project log, with zero
production diff. The scanner-test checkpoint was committed and pushed as
`929fd44c` before the heavy validation gate.

The exact one-shot normal command was:

```text
ulimit -v 8000000; start_seconds=$SECONDS; timeout 90m env GOMEMLIMIT=4GiB GOCACHE=/scion-volumes/gocache go test -timeout 89m -count=1 -p 2 ./pkg/hub -run '^(TestExplainAPI_(RegisteredOperationUsesReviewedBasePermission|OperationValidationFailsClosedWithoutValueEcho|DoesNotInferOperation|EffectivePermissionsUsesNonEmittingIntrospection)|TestEffectivePermissionIntrospectionBoundaryStructure|TestEffectivePermissionIntrospectionBoundaryRejectsMutations|TestEffectivePermissionIntrospectionBoundaryAllowsSafeInterfaceDispatch|TestEffectivePermissionIntrospectionBoundaryImporterIsBounded|TestEffectivePermissionIntrospectionBoundaryTypeErrorsFailClosed|TestAuthzOperationLookupIsClosed)$'; command_rc=$?; echo COMMAND_EXIT=$command_rc; echo WALL_SECONDS=$((SECONDS-start_seconds)); exit $command_rc
```

Normal result: **INCONCLUSIVE**. The command emitted dependency-download
lines, then no compiler, test, package, or failure diagnostic. The outer
timeout produced exactly `COMMAND_EXIT=124`, `WALL_SECONDS=5400`; there was no
explicit package `ok`. The command was not retried. Race was **NOT RUN /
DEFERRED** because normal was not conclusively green and no placement message
explicitly permitted race at this venue.

No other heavy Go command, full or unfiltered Hub test, prewarm, retry, lint,
vet, build, `make ci`, or `make ci-full` ran. The shared cache was not cleaned,
altered, relocated, replaced, or deliberately prewarmed. The final commit SHA
is reported in the protected completion report because a commit cannot contain
its own SHA.

The correction remains limited to this project log and the authorized Go test
file with zero production diff. No catalog, P2, emitter/slog/sink,
store/schema/history, sampling, transport, projection, M1, or
`#2392 audit_emit_dispatch` behavior is changed. Fresh round 7/7 remains
blocked because both required gates are not conclusively green.

## R6 performance fix 2: settled rejection components (2026-10-05)

EM authorized the broker-local protected brief whose SHA-256 is
`e44cdf833028db3c6767512d0e3351dc677d67bc6c2cd08d6eb6e9c94f807a27`.
EM's independent `scion look` confirmed OpenAI Codex v0.160.0 / GPT-6.1-Sol
medium. The attempted worker `harness-config codex` was unavailable (exit 127);
EM explicitly made its verification authoritative. The missing installed
artifact-durability skill was explicitly nonblocking; pushed commits require
independent remote equality, and protected reports require durable receipt.

Initial HEAD/local/tracking/independent fork remote all matched
`aa4b7aebba21d21598acc079feecf6a58cec7e3d`, with clean tree and 0/0 ahead/behind.
The accepted P1 base `fd4f83fb7769af4b08eed1ac06be6d2442de89d7` was absent.
One explicitly authorized exact fetch (`git fetch --no-tags --depth=1 origin
fd4f83fb7769af4b08eed1ac06be6d2442de89d7`) exited zero; FETCH_HEAD and the
commit object matched exactly. Local ancestor verification exited 1 in the
shallow clone: **locally unprovable, not a successful ancestry check**.
No deepen/retry occurred. EM accepted the independent authoritative fork
compare (accepted merge base, 24 ahead/0 behind) and the matching local approved
14-path base-to-HEAD diff. Ref equality and clean tree remained unchanged.
Go and go.mod were 1.26.1, GOFLAGS unset, and the protected reviews directory
was visible and writable. EM then explicitly accepted preflight before edits.

Failure provenance: the protected val-2 report
`/scion-volumes/scratchpad/projects/audit-update/reviews/m2-explain-r6-performance-validation-2.md`
hashed to `3411ecb9376e9225ec84c4a28d44dc0a421d9021318194af7ee6366c6ca070f7`.
At the exact initial head, the single normal gate failed with command exit 1,
package time 5340.212s, wall 5366s, no package `ok`, and repeated resolver
frames at scanner lines 1406/1417. No race or retry ran in that validation.

Static diagnosis: the prior resolver cached only `complete=true`. Safe DAGs
and forbidden fully resolved DAGs were memoized, but rejection ancestors of
cycles or nil dependencies were recomputed on every shared path and root.
The watchdog frames identify the dependency-recursion loop. Production
`AuthorizeReadBatch` reassigns `ctx` via `contextWithIdentity`: the caller's
parameter depends on the wrapper return, which depends on its parameter,
which in turn receives the caller parameter. `loadAllAccessConstraints`
reads cached fields, assigns returned values back into those same fields,
and returns those values. Whole-package field/call/return dependencies thus
create shared feedback components. The old safe-only DAG regression missed
this rejected-subgraph class.

Semantic `types.Object` keys span all roots in a single validation pass;
expression-only nodes enumerate objects and do not introduce recursive cache
keys. Existing edges for variables, fields, parameters, returns, aggregates,
indexing, ranges, searches, callable aliases and method expressions remain
intact. Type/import/interface errors still reject through their existing
validation paths. The flow is immutable after construction.

The correction uses dependency-first strongly connected component finalization.
Index/low/on-stack traversal state remains private and is never published as a
summary. Every outgoing dependency contributes before publishing a component's
summary to all its members. Cyclic components, self edges, nil dependencies and
rejected outgoing summaries remain `complete=false`; forbidden provenance is
retained across component boundaries. A settled rejection is cached separately
from the transient computation state, without converting rejection to success.
Each semantic object expands once and each dependency edge is examined at most
twice across roots. No name/surface/receiver/filename/error allowlist or
permission-to-operation inference was added.

Deterministic regressions cover 10-level shared cyclic and nil-dependency
rejection graphs; multiple roots/repeated resolutions; reordered starting
roots/component members; forbidden plus incomplete outgoing edges through an
SCC; and a parsed/typechecked production-shaped parameter/return and
field/return feedback graph. Existing safe and forbidden DAGs, mutation matrix,
and safe interface/callable/data-flow cases remain. The prior cycle/nil tests
still reject, but now assert settled rejection reuse instead of demanding
uncached recomputation.

Focused RED command (before the SCC correction):

```text
ulimit -v 8000000; start_seconds=$SECONDS; timeout 5m env GOMEMLIMIT=4GiB GOCACHE=/scion-volumes/gocache go test -timeout 4m -count=1 -p 2 ./pkg/hub -run '^TestExplainBoundaryForbiddenDataResolverFailsClosed$/shared_(incomplete|cyclic)_graph_bound$'; command_rc=$?; echo COMMAND_EXIT=$command_rc; echo WALL_SECONDS=$((SECONDS-start_seconds)); exit $command_rc
```

Result: **INCONCLUSIVE**, exit 124, wall 300s, dependency-download output only;
no package/test/count verdict. It finished untouched and was not retried.

Focused GREEN command (after the correction):

```text
ulimit -v 8000000; start_seconds=$SECONDS; timeout 10m env GOMEMLIMIT=4GiB GOCACHE=/scion-volumes/gocache go test -timeout 9m -count=1 -p 2 -v ./pkg/hub -run '^(TestExplainBoundaryForbiddenDataResolver(MemoizesSharedDAG|FailsClosed)|TestEffectivePermissionIntrospectionBoundaryRejectsMutations|TestEffectivePermissionIntrospectionBoundaryAllowsSafeInterfaceDispatch)$'; command_rc=$?; echo COMMAND_EXIT=$command_rc; echo WALL_SECONDS=$((SECONDS-start_seconds)); exit $command_rc
```

Focused GREEN result: **INCONCLUSIVE**, command exit 124, wall 600s.
The output reached `TestEffectivePermissionIntrospectionBoundaryRejectsMutations`
and its `interface_other-file_pointer_receiver` subtest, but emitted no case
PASS/FAIL, resolver visit/work counts, package result or explicit `ok`.
A read-only process check observed a CPU-active compiler before test execution;
another at 5m19s observed the Go command with no compiler/test process. These
observations are not a test verdict. The command ran once, without concurrent
heavy work or retry. No regression bound or malicious/safe closure was proven
by this run.

EM required conclusive focused evidence before any validation checkpoint.
That requirement is **unmet**: no checkpoint commit/push, and no normal gate has
run for this correction. Changes remain uncommitted in the two authorized paths.
The protected report and patch preserve the working change pending EM
receipt/disposition; no claim of completed delivery or successful validation is
made. `gofmt` and `git diff --check` passed, but are not substitutes for tests. The latest EM amendment supersedes the original race venue
permission: broker-01 is **normal-only; race prohibited/deferred**, including
after normal GREEN. The checkpoint must be pushed with remote equality and
clean tree before the single prescribed normal gate. On failure/inconclusive,
stop without retry; on GREEN, preserve/push evidence and stop for a separately
authorized race venue. Completion requires durable report receipt.

This correction is restricted to this project log and
`pkg/hub/authz_explain_operation_contract_test.go`, with zero production delta
against the pinned initial head. No option-B production, authzop catalog, P2,
emitter/slog/sinks, store/schema/history, sampling, transport, projection, M1,
or ptone/scion#2392 timing changes are authorized. Round 7 and dependent work
remain blocked. No descendants, compare links, merge notices, merge/rebase/reset,
force push, upstream integration, full Hub suite, lint, heavy build, make ci,
or make ci-full are permitted. One heavy command runs at a time; shared GOCACHE
is not cleaned, relocated, replaced or deliberately prewarmed.

### Stage A isolated resolver proof and checkpoint authorization

After receiving/verifying the frozen report and patch, EM separately authorized
one isolated resolver gate (not a retry of either earlier coupled selector):

```text
ulimit -v 8000000; start_seconds=$SECONDS; timeout 20m env GOMEMLIMIT=4GiB GOCACHE=/scion-volumes/gocache go test -timeout 19m -count=1 -p 2 -v ./pkg/hub -run '^TestExplainBoundaryForbiddenDataResolver(MemoizesSharedDAG|FailsClosed)$'; command_rc=$?; echo COMMAND_EXIT=$command_rc; echo WALL_SECONDS=$((SECONDS-start_seconds)); exit $command_rc
```

Stage A: **GREEN**, exit 0, wall 20s, explicit
`ok github.com/GoogleCloudPlatform/scion/pkg/hub 0.262s`.
Both named tests and every subtest passed. Shared nil/cyclic rejection graphs
each logged objects=21, expansions=21, edges=39, visits=78. The typed feedback
graph logged universe=52, expansions=52, edges=99, visits=198 in both root
orders. The component-boundary forbidden/incomplete propagation, cycle
rejection, forbidden shared DAG, incomplete rejection reuse, and safe shared
DAG assertions all passed. These measured bounds supersede the earlier lack
of resolver execution evidence; they do not substitute for malicious/safe
boundary-suite or real production-structure execution.

Both previous runs remain distinct **INCONCLUSIVE** evidence: focused RED
124/300s (downloads only), coupled focused GREEN 124/600s (one mutation
started, no verdict). They were not retried. EM durably received the original
blocked report (12,257 bytes; SHA-256
`a9f618fc39fd17d8c7f1989d71a97fce45ea94d228ad1f06243ad50b09ee6045`) and
patch (21,088 bytes; SHA-256
`aa95809b4c9de81e0d5dcf87b025f2e4ebb0f02c7d2e77077d47e3d68a2e6631`).
Those hashes identify the earlier frozen evidence, not the subsequent report.

At 2026-10-05 03:14:42Z EM explicitly authorized recording Stage A, formatting
and scope checks, committing/pushing exactly the scanner test and project log
as a validation checkpoint, independently verifying ref equality and clean
tree, then running exactly one unchanged prescribed normal gate. This
supersedes the earlier pre-checkpoint hold. The checkpoint SHA is reported in
the protected report because a commit cannot contain its own SHA. Normal is
pending; its actual result will be recorded afterward. No race is authorized
on broker-01, even after GREEN. No additional heavy command, retry, round 7,
or dependent work is authorized.

### Pushed checkpoint and single normal failure

The validation checkpoint was committed and pushed as
`05a19d74e0275073700444b130e95652b7ea957b`. Before validation, HEAD/local branch/
tracking ref/independent fork remote all matched exactly; ahead/behind was 0/0
and porcelain status was empty. `gofmt`, `git diff --check`, exact two-path
scope and zero production delta checks passed.

Exactly one prescribed normal gate ran on that checkpoint:

```text
ulimit -v 8000000; start_seconds=$SECONDS; timeout 90m env GOMEMLIMIT=4GiB GOCACHE=/scion-volumes/gocache go test -timeout 89m -count=1 -p 2 ./pkg/hub -run '^(TestExplainAPI_(RegisteredOperationUsesReviewedBasePermission|OperationValidationFailsClosedWithoutValueEcho|DoesNotInferOperation|EffectivePermissionsUsesNonEmittingIntrospection)|TestEffectivePermissionIntrospectionBoundaryStructure|TestEffectivePermissionIntrospectionBoundaryRejectsMutations|TestEffectivePermissionIntrospectionBoundaryAllowsSafeInterfaceDispatch|TestEffectivePermissionIntrospectionBoundaryImporterIsBounded|TestEffectivePermissionIntrospectionBoundaryTypeErrorsFailClosed|TestExplainBoundaryForbiddenDataResolver(MemoizesSharedDAG|FailsClosed)|TestAuthzOperationLookupIsClosed)$'; command_rc=$?; echo COMMAND_EXIT=$command_rc; echo WALL_SECONDS=$((SECONDS-start_seconds)); exit $command_rc
```

Result: **FAILED**, command exit 1, wall 441s, explicit package
`FAIL github.com/GoogleCloudPlatform/scion/pkg/hub 422.644s`, no package `ok`,
no timeout diagnostic. Complete decisive failure output:

```text
--- FAIL: TestEffectivePermissionIntrospectionBoundaryStructure (419.44s)
    authz_explain_operation_contract_test.go:176:
        Error Trace: /workspace/pkg/hub/authz_explain_operation_contract_test.go:176
        Error: Received unexpected error:
            handleExplainEffectivePermissions (audit_authz.go): cannot completely resolve authorization operation data provenance
        Test: TestEffectivePermissionIntrospectionBoundaryStructure
--- FAIL: TestEffectivePermissionIntrospectionBoundaryRejectsMutations (0.02s)
    --- FAIL: TestEffectivePermissionIntrospectionBoundaryRejectsMutations/operation_constant_alias (0.00s)
        authz_explain_operation_contract_test.go:256:
            Error Trace: /workspace/pkg/hub/authz_explain_operation_contract_test.go:256
            Error: An error is expected but got nil.
            Test: TestEffectivePermissionIntrospectionBoundaryRejectsMutations/operation_constant_alias
FAIL
FAIL github.com/GoogleCloudPlatform/scion/pkg/hub 422.644s
FAIL
COMMAND_EXIT=1
WALL_SECONDS=441
```

A read-only process observation at gate elapsed 3m45s showed `hub.test` and its
module-aware `go list -deps -export` child, with CPU-active `pkg/ent` and other
dependency compilation. Export loading, rather than a reported resolver stack,
was the observed phase during the gate's initial silence. This observation
is not a per-phase timing measurement or an additional test verdict.

The isolated resolver proof remains conclusive: deterministic graph bounds,
root-order/component provenance tests, and safe/forbidden/rejection summaries
passed Stage A. The production boundary and malicious operation-constant
alias closure are **not green**. The real structure now terminates with an
incomplete-provenance rejection; the mutation is incorrectly accepted.
No full-closure or completed-delivery claim is made. No other selected test
failure diagnostic appeared, but the non-verbose failed package is not an
explicit per-test passing packet.

Static follow-up only: `explainBoundaryAssignedVar` accepts `*types.Var` and
returns nil for a constant destination. ValueSpec dependency construction uses
that helper, so a constant alias has no destination edge. This suggests a
separate extraction gap behind the constant-alias failure; its baseline runtime
status and a corrective design have not been tested or authorized here.
The SCC correction leaves that extraction helper unchanged. The production
incomplete result is consistent with the preserved conservative cycle rejection,
but no exact offending production root/component was isolated after failure.
Neither issue was fixed, allowlisted, or suppressed. Any extraction/context
model change requires a new explicit disposition; round 7 remains blocked.

No retry, race, or further heavy command ran. Race is **NOT RUN / PROHIBITED /
DEFERRED** on broker-01, and normal failure independently bars race. The
checkpoint is preserved; only final log/report/static/hash/ref evidence follows.
The final evidence commit contains only this log and is reported in the
protected report. Scope remains exactly scanner test plus project log, with
zero production delta against pinned initial head. All original exclusions,
resource/cache constraints and no-descendant/no-dependent-work rules hold.
Completion/disposition remains blocked pending durable receipt of final evidence.

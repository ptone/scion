# Audit update milestone 2 P1 producer contract

## Scope and checkpoint

P1 adds the A1-pinned producer fields and structural authorization audit
reasons without wiring an emitter or populating operations at call sites.
The implementation checkpoint is
`6a9f6109dced5dba4cf001202d3641416fce8c72`, based directly on required head
`2f41820dc28fd6abece8c6fe4fe03b7c828e5d99`. It was pushed before the broader
targeted gates. The final delivery SHA also contains this project log and is
reported in the completion handoff because a commit cannot contain its own
SHA.

Review round 1 fixes are based on reviewed head
`6fd2c6ac321f7024ec4fdc74cc0b65a9bf26e2ef`. They address only R1's
dependency-aware project-membership reason and R2's closed producer guard;
P2/emitter work remains excluded.

`AuthzRequest.OperationID` has the exact `authzop.OperationID` type and
`Decision.AuditReason` has the exact `auditevent.ReasonCode` type. Both use
`json:"-"`, so this producer-only metadata does not change request or decision
wire representations. P1 does not populate `OperationID` anywhere.

## Exact A1 mapping

| Producer category | Outcome | Structural reason | P1 source |
|---|---|---|---|
| allow | allow | `allowed` | kernel and delegation allows |
| inherited | allow | `inherited` | accepted relationship grants |
| cache hit | allow | `allowed` | contract mapping; P1 does not add a cache producer |
| permission denied | deny | `permission_missing` | kernel default deny and missing delegation permission |
| policy denied | deny | `policy_denied` | applied restrictions, credential scope, relationship restriction, delegation ceiling |
| unauthenticated | deny | `not_authenticated` | missing authorization principal or delegation actor |
| unauthorized | deny | `not_authorized` | delivery credential gate and healthy project membership authority denial |
| invalid request | deny | `invalid_request` | mismatched/unsupported identity, unresolvable permission input, unknown grant, missing project input |
| dependency unavailable | deny | `dependency_unavailable` | principal, membership, binding, role, group/effective-group, and effective-permission dependency errors |
| check unavailable | deny | `check_unavailable` | delegation ceiling check error and unavailable material authorization service |

Per A1, `not_found`, `conflict`, `rate_limited`, `attachment_rejected`, and
`check_disabled` remain unavailable from the current authorization producer.
No reason is inferred from resource, action, permission, route strings, or
human-readable `Decision.Reason` prose.

## Production decision exits covered

- `AuthzService.decide`: missing, mismatched, or unrecognized identity;
  unsupported federated-service/broker identities; unresolvable permission;
  delivery credential gate; UAT project/scope gates; principal, binding, and
  role resolution failures; kernel allow/default deny/restriction deny;
  accepted and restricted relationship candidates; delegation-ceiling deny
  and fail-closed check error; final decorated decision.
- `kernelDecisionToDecision`: direct binding/kernel allow,
  `permission_missing` default deny, and `policy_denied` when a structural
  restriction applied.
- `relationshipCandidates`: ancestor, owner, hub-member assignment, and
  progeny grants are structurally `inherited`.
- `AuthzService.CanDelegate` and every helper return: allow, missing actor,
  invalid grant/input, scoped-credential policy denial, missing permission,
  project authority denial, and store/resolution dependency failure. Shared
  constructors make allow/deny assignment explicit while preserving all
  existing `Allowed` and `Reason` values.
- `projectReadDecision`: the error-returned empty decision is structurally
  `check_unavailable`.

`TestEveryProductionDecisionLiteralAssignsAuditReason` parses every production
Go file in `pkg/hub`. It rejects omitted or zero-value `Decision` origins,
named zero returns, conversions, `new(Decision)`, dynamic/converted/unknown
reasons, nonliteral construction outcomes, and statically incompatible
allow/reason pairs. Identifier returns must trace to a checked literal or
producer-call origin. Exact approved constant assignments are the sole narrow
post-construction allowance; `decorateDecision`'s by-value decoration and
`projectReadDecision`'s Decide-populated cache are the two documented return
pass-through allowances. The same package-wide scan rejects
`Decision.AuditReason` reads and any read, write, or composite-literal
population of `AuthzRequest.OperationID`; it is not limited to a file list.
Mutation cases prove rejection of omitted, converted-invalid, incompatible,
nonliteral-zero, and unproven-variable decisions, an audit metadata read in an
otherwise unlisted file, and both OperationID population and reading. Focused
behavior tests cover every currently produced A1 reason and enforce outcome
compatibility.

For R1, direct-owner and owner/admin resolution now retain a private typed
three-state status: allowed, healthy denial, or dependency unavailable. The
existing bool helpers remain as compatibility wrappers for other callers.
`canDelegateProjectMembership` preserves every authorization result and its
human-readable reason, but selects `dependency_unavailable` when neither path
can authorize and any direct binding, owner-role, membership, effective-group,
group-binding, or group-role lookup was incomplete. Successful authority
evidence still wins over an earlier lookup failure.

## Verification

The initial RED run on the untouched producer failed as intended:

- `go test -count=1 -p 2 ./pkg/hub -run 'TestAuthorizationProducerContractFields|TestEveryProductionDecisionLiteralAssignsAuditReason'`
  — FAIL: both pinned fields were missing and 48 production `Decision`
  literals were unassigned.

Review-round RED proof on exact reviewed head `6fd2c6a`:

- `TestProjectMembershipDependencyFailuresRetainDenyAndProse` failed at all
  six injected lookup stages with actual `not_authorized` instead of expected
  `dependency_unavailable`; `Allowed=false` and the existing prose matched.
- The strengthened production scan rejected the reviewed tree's dynamic
  reason, dynamic outcome, zero `Decision` declaration, and missing `Allowed`
  assignment that the previous guard accepted.

Post-implementation checks:

- `go test -count=1 -p 2 ./pkg/hub ./pkg/hub/auditevent -run 'TestAuthorization|TestBuildAuthorization|TestEveryProductionDecision'`
  — PASS (`pkg/hub` 4.385s, `pkg/hub/auditevent` 0.612s).
- `go test -count=1 -race -p 2 ./pkg/hub ./pkg/hub/auditevent -run 'TestAuthorization|TestBuildAuthorization|TestEveryProductionDecision'`
  — PASS (`pkg/hub` 28.491s, `pkg/hub/auditevent` 1.907s).
- `go vet -p 2 ./pkg/hub ./pkg/hub/auditevent` — PASS.
- `go build -buildvcs=false -p 2 ./pkg/hub ./pkg/hub/auditevent` — PASS; this
  was the single heavy build.
- `timeout 10m env GOGC=40 golangci-lint run --new-from-rev=2f41820dc28fd6abece8c6fe4fe03b7c828e5d99 --concurrency=1 ./pkg/hub/...`
  — PASS, `0 issues`.
- `gofmt` on all changed Go files and `git diff --check` — PASS.
- Review mutation: inverting the new `restriction.Applied` condition made
  `TestAuthorizationAuditReasonMappings/policy_denied` fail with actual
  `permission_missing` instead of expected `policy_denied`; the mutation was
  restored before the checkpoint.
- Production diff audit from the required base contains only
  `pkg/hub/authz.go`, `pkg/hub/authz_candelegate.go`,
  `pkg/hub/authz_relationship_rules.go`, `pkg/hub/handlers_agents_core.go`,
  and `pkg/hub/material_runtime.go`,
  plus the focused `pkg/hub/authz_audit_reason_test.go` and this required log.

Review-round implementation checkpoint
`792ceab146420daba8daf63998f819b4901e4337` has subject
`fix: close authorization producer contract gaps` and sole parent
`6fd2c6ac321f7024ec4fdc74cc0b65a9bf26e2ef`. It was pushed before the
following broader checks:

- Focused normal suite covering producer fields, the package scan, mutations,
  mappings, all six dependency stages, metadata non-influence, project owner
  and admin compatibility — PASS (`pkg/hub` 5.644s).
- The first race attempt was explicitly manager-interrupted for remote
  provenance verification and produced no result; it was neither a timeout,
  failure, nor pass. The one authorized clean restart of the same focused
  suite used `timeout 10m`, `-race`, and `-p 2` — PASS (`pkg/hub` 236.879s).
- `timeout 10m go vet -p 2 ./pkg/hub ./pkg/hub/auditevent` — PASS.
- `timeout 10m go build -buildvcs=false -p 2 ./pkg/hub ./pkg/hub/auditevent`
  — PASS; this was the review round's single heavy build.
- `timeout 10m env GOGC=40 golangci-lint run
  --new-from-rev=6fd2c6ac321f7024ec4fdc74cc0b65a9bf26e2ef
  --concurrency=1 ./pkg/hub/...` — PASS, `0 issues`; it was not rerun.
- `gofmt` on all changed Go files, `git diff --check`, and changed-file/scope
  inspection against the reviewed head — PASS.

The final guard/evidence commit follows the implementation checkpoint. Its
exact SHA is reported in the completion handoff because a commit cannot
contain its own SHA.

After those gates, a final test-only self-audit strengthened variable-return
origin tracing and added its explicit mutation. The focused package-scan and
mutation tests passed. The already-consumed authorized race restart and the
single lint run were not repeated; no production code changed after them.

`make ci` and `make ci-full` were not run because the restricted brief
prohibits them. No required check failed or was inconclusive.

## Residual risks and P2 handoff

- P1 intentionally leaves every `OperationID` zero. P2 must populate it only
  from the canonical route/operation owner; it must not infer it from resource,
  action, permission, or route strings.
- P1 intentionally does not add an audit emitter, sink/store changes,
  sampling, transport, projection observation, API/UI behavior, or alter
  `#2392` `audit_emit_dispatch` timing.
- P2 should consume `Decision.AuditReason` directly and treat a missing,
  unknown, or outcome-incompatible operation/permission/reason as audit
  construction failure without changing the authorization result. It must not
  parse `Decision.Reason`.
- P2 and emitter cutover remain blocked pending independent P1 approval.

## Review round 2 fixes

Round 2 reviewed exact head
`49c658b4ff934bc9bad0cf87ce24e7f1a7c9e488` and requested two retained-author
corrections. The code-and-test checkpoint
`c17fe535f39a4151450724e8fdf8faf56e99a3d8` was committed and fast-forward
pushed before the bounded focused checks, as required.

- R3 closed: `excludeKernelGrantForDeliver` still changes the checked kernel
  allow to the same deny, retains the exact `deliverRoleGrantReason`, clears
  the same granting-source fields, and preserves provenance/side effects. It
  now changes the structural reason with the outcome, from `allowed` to
  `policy_denied`. The existing role-only non-explain behavior test pins the
  reason. The package-wide AST guard now requires every literal `Allowed`
  mutation on a Decision to have an outcome-compatible exact `AuditReason`
  assignment in the same lexical block; mutation regressions cover both a
  stale allow reason and a missing companion reason.
- R4 closed: when an access-constraint load failure caused the final deny, the
  same block that sets `DenyCauseResolutionError` now assigns
  `dependency_unavailable`. The existing resolution-error regression pins the
  structural reason while retaining the exact deny, prose, provenance, error
  behavior, and side effects.

Post-push bounded evidence:

- Focused normal tests for the two behavior regressions, every production
  Decision guard, and mutation suite: PASS (`1.587s` test runtime).
- The matching single `-race -p 2` invocation: PASS (`25.638s` test runtime).
- `timeout 10m go vet -p 2 ./pkg/hub`: PASS.
- Gofmt on the five changed files and `git diff --check`: PASS.
- `timeout 10m env GOGC=40 golangci-lint run
  --new-from-rev=49c658b4ff934bc9bad0cf87ce24e7f1a7c9e488
  --concurrency=1 ./pkg/hub/...`: PASS, `0 issues`; it was not restarted.

The delta from the reviewed head contains exactly two production files and
three focused test files. It adds no `AuthzRequest.OperationID` population,
P2 emitter, sink/store/sampling/transport change, projection/API/UI work, or
`#2392 audit_emit_dispatch` timing change. P2 and emitter work remain blocked
pending a clean fresh review round 3/7.

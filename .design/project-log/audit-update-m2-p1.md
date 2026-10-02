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
| unauthorized | deny | `not_authorized` | delivery credential gate and project membership authority failure |
| invalid request | deny | `invalid_request` | mismatched/unsupported identity, unresolvable permission input, unknown grant, missing project input |
| dependency unavailable | deny | `dependency_unavailable` | principal, binding, role, group, and effective-permission dependency errors |
| check unavailable | deny | `check_unavailable` | delegation ceiling check error and unavailable material authorization service |
| closed fallback | deny | `unspecified` | reserved closed A1 fallback; no current producer exit requires it |

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
Go file in `pkg/hub` and fails on any `Decision` literal without an explicit
`AuditReason`. Focused behavior tests cover every currently produced A1 reason
and enforce allow/reason outcome compatibility. Static AST guards prove
`AuthzRequest.OperationID` and `Decision.AuditReason` are never read by P1
authorization branching; changing `OperationID` also leaves the observable
decision unchanged.

## Verification

The initial RED run on the untouched producer failed as intended:

- `go test -count=1 -p 2 ./pkg/hub -run 'TestAuthorizationProducerContractFields|TestEveryProductionDecisionLiteralAssignsAuditReason'`
  — FAIL: both pinned fields were missing and 48 production `Decision`
  literals were unassigned.

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
  `pkg/hub/authz_relationship_rules.go`, and `pkg/hub/material_runtime.go`,
  plus the focused `pkg/hub/authz_audit_reason_test.go` and this required log.

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

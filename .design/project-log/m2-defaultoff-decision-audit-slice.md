# M2 bounded authorization-audit merge checkpoint

Date: 2026-10-06 UTC. Workstream: ptone/scion#2379.

This checkpoint integrates the immutable selected-main baseline and resolves
the accepted authorization merge conflicts. Amendments 6–12 to the protected
default-off brief authorize this five-path compatibility slice and its durable
merge. It does not implement or activate the subsequent default-off router,
registry, admission, freshness, drain, health, or production cutover surfaces.

## Baseline and path accounting

- First parent: `dd44eed7b1c4d085765b0b3f42eafbbf5f90a0d5` (M2).
- Second parent: `48c3666ac5c39682f066b3473f0c3b17c12f73de` (selected-main).
- Merge base: `64a549c402fe941a9ea7702a453ecf60b0b70d94`.
- Selected-main contains #2502 merge
  `8767f43aeb8ada3fe2b2e05d78da14b8f3b4ba5b`.
- Provisional conflict merge tree:
  `a0b47f623b800ff0fe91f1ee7574a11978af0e0f`.
- The raw inherited manifest contains 2,492 rows / 100,577 bytes, SHA-256
  `a1d088a488d4071529201462fcccccffe60fdf9c72f6dfe2e8eb0c7b365be6c3`.
  It is preserved outside Git at the protected workstream reviews path as
  `m2-defaultoff-inherited-baseline-manifest.txt`. These are inherited baseline
  integration changes, not manual implementation scope. Moving-main drift was
  explicitly dispositioned without fetching or integrating the moving tip.

The five accepted manual paths are `pkg/hub/authz.go`,
`pkg/hub/authz_candelegate.go`, `pkg/hub/authz_bearer.go`,
`pkg/hub/authz_merge_conflict_test.go`, and
`pkg/hub/authz_audit_reason_test.go`. This project log is the sixth manual path.
All other merged changes remain the accepted provisional baseline content.

## Conflict decisions and compatibility repair

- H1 retains M2's operation-free evaluation request while carrying selected-main
  target evidence and bearer-run memo state. Ordinary Decide retains one audit
  emission; bearer introspection remains non-emitting. The internal bearer
  evaluation literal uses the operation-free request type.
- H2 preserves the selected-main constraint/relationship fault condition,
  authorization outcome, denial text, and resolution-error deny cause, and adds
  dependency-unavailable audit metadata.
- H3 preserves selected-main bearer boundary, permission ceiling, live project
  admission, target evidence, and memo behavior. Denied helper and direct
  stage-1 returns classify audit metadata using typed DenyCause: resolution
  errors are dependency-unavailable; other denials are policy-denied.
- H4 retains selected-main delegation signatures, guard order, target
  construction, project-admission arguments, denial text, and permission
  ceiling logic. Local denials use policy metadata. Typed project-access lookup
  faults use dependency metadata; every other error or refusal still denies.
- Amendment 9 corrected one existing audit-reason test call by supplying
  `context.Background()` to the context-first delegation signature. Assertions
  and fixtures were unchanged. This fifth path was separately diff-reviewed.

Stage A intentionally withheld H2–H4 final audit metadata to prove a meaningful
RED. Stage B changed only metadata in authz.go and authz_candelegate.go after
manager acceptance. No reason-string parsing or error-to-allow path was added.
The #2502 writer and metrics files remain byte-identical to selected-main.

## Focused RED and GREEN

The exact accepted RED command was:

```sh
HEAVY_BUILD_MAX_WAIT=2700 /scion-volumes/scratchpad/tools/heavy-build.sh sh -c '
available=$(free -g | awk '\''/^Mem:/ {print $7}'\'')
free -g
if [ -z "$available" ] || [ "$available" -lt 30 ]; then
  echo "MEMORY_GATE_BLOCKED_AT_ACQUIRE available=${available:-unknown}"
  exit 75
fi
ulimit -v 12000000
exec timeout 15m env GOMEMLIMIT=6GiB GOGC=40 GOFLAGS=-gcflags=-c=1 GOCACHE=/scion-volumes/gocache go test -timeout 14m -count=1 -p 1 -v ./pkg/hub -run '\''^(TestAuthzConflictResolution_OperationFreeBearerInputs|TestAuthzConflictResolution_FaultAuditReasons|TestAuthzConflictResolution_UATDelegationBoundaryAndFault)$'\''
'
```

Amendment-9 RED acquired slot-2 after 225 seconds; acquire-time available memory
was 45 GB. Exit 1, wall duration 832 seconds, package test duration 1.992 seconds.
H1 passed. All 15 failed assertions across 13 failing subtests were intended
H2–H4 AuditReason comparisons; three delegation pass cases passed. No unrelated
outcome/reason/cause, compilation, setup, resource, or timeout failure occurred.
Manager independently accepted this evidence.

The exact accepted GREEN command was:

```sh
HEAVY_BUILD_MAX_WAIT=2700 /scion-volumes/scratchpad/tools/heavy-build.sh sh -c '
available=$(free -g | awk '\''/^Mem:/ {print $7}'\'')
free -g
if [ -z "$available" ] || [ "$available" -lt 30 ]; then
  echo "MEMORY_GATE_BLOCKED_AT_ACQUIRE available=${available:-unknown}"
  exit 75
fi
ulimit -v 12000000
exec timeout 15m env GOMEMLIMIT=6GiB GOGC=40 GOFLAGS=-gcflags=-c=1 GOCACHE=/scion-volumes/gocache go test -timeout 14m -count=1 -p 1 -v ./pkg/hub -run '\''^(TestAuthzConflictResolution_OperationFreeBearerInputs|TestAuthzConflictResolution_FaultAuditReasons|TestAuthzConflictResolution_UATDelegationBoundaryAndFault|TestEvaluateBearerCeiling_MatchesUATRequestDecision|TestEvaluateBearerCeiling_EmitsNoDecisionAudit|TestEvaluateBearerCeiling_StoreFaultDenies|TestUATGate_HubBoundaryReachesAccessibleProjects|TestUATGate_ProjectTargetRequiresCurrentAccess|TestUATGate_RequestTargetEvidence|TestEvaluateBearerCeiling_EvidenceMustNameEvaluatedPermission|TestRelationshipProjectAccess_DecideRefusalIdentical|TestCanDelegate_UATCannotDelegateOutsideProject|TestCanDelegate_UATCannotCreateSystemGrants|TestAuthz_IsIndeterminate_AccessConstraintLoadError|TestExplainAPI_EffectivePermissionsUsesNonEmittingIntrospection|TestDecisionAudit_Sampling)$'\''
'
```

Amendment-11 GREEN acquired slot-1 immediately; acquire-time available memory
was 36 GB. Exit 0, wall duration 580 seconds, package result
`ok github.com/GoogleCloudPlatform/scion/pkg/hub 6.519s`. All 16 named tests and
27 subtests passed, with no failure or skip. Amendment 12 independently accepts
this GREEN. No additional Go/build/vet/lint/race/full-suite execution is allowed.

Protected evidence lives under
`/scion-volumes/scratchpad/projects/audit-update/reviews/`:

| Evidence file | Result | SHA-256 |
| --- | --- | --- |
| m2-defaultoff-conflict-stage-a-red.txt | Initial memory 27 GB; no heavy command | e5812f73b8c8f30d9b7f6c17b5327d13a7401e07aab830dcde7e5d07b5084ec6 |
| m2-defaultoff-conflict-stage-a-red-callback-1740.txt | Memory 46 GB; queue-only timeout 124 / 900s; no Go | 069c20e577d2261f22f960e2176f9e9e86d1b2eba205adf7a286f7555a61edc0 |
| m2-defaultoff-conflict-stage-a-red-amendment-8.txt | Memory 46 GB; compile failure before subtests, exit 1 / 736s | 44254b4cb2427dd5158d3aba1c14b245897a0b053538aaa328572452215c0e97 |
| m2-defaultoff-conflict-stage-a-red-amendment-9.txt | Accepted intended RED; 608 lines / 53,616 bytes | 36c3b9de511925f8bdbb90cc063fd5a75c3db98bde1b02dc3ad57c5f136fb34d |
| m2-defaultoff-conflict-stage-b-green-amendment-11.txt | Accepted GREEN; 1,166 lines / 134,439 bytes | 3dcb830f0069676d2cfd65c9c78ce9e36b86030dd31964f3bdfb605c4113aa15 |

The compile failure was the single context-first signature mismatch corrected
under Amendment 9. Every reattempt required explicit coordinator/manager
authority; no automatic retry or resource-cap enlargement occurred. Reports
1–3 and all evidence remain sealed outside Git.

## Remaining gates and non-activation

The fresh significant-conflict review is a separate manager-controlled gate
after durable push. No reviewer was started and explain round 7 was not used.
The subsequent registry/router/admission slice and its implementation map are
not authorized by this closeout. No production census or ratified positive
handler was supplied; no T1–T25 entry, SCC grounding, production acceptance,
clean-build/live-profile/trust/activation gate, monotonic freshness bridge,
bounded NEW drain, or decision-health projection is approved by these tests.
Production admission/cutover remains rejected pending those independent gates.
The finite fixtures prove merge compatibility only, not production trust.

No PR, main merge, deployment, experiment activation, live-setting change,
legacy retirement, logging/sink behavior change, or #2392 timing claim is part
of this checkpoint. Retained legacy #2502 lifecycle remains separate from the
unimplemented NEW 2-second drain. Human-directed questions sent by this worker:
none; manager acceptance exchanges are agent-directed. After delivery the worker
remains blocked pending durable receipt and explicit lifecycle disposition.

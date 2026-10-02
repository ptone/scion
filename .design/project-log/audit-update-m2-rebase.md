# Audit update milestone 2 rebase and revalidation

## Result and provenance

The M2-only series was transplanted onto authoritative upstream `main` without
replaying M1 history. The immutable inputs and selected base were:

- old fork M2 head and publication lease:
  `708455edafd6e86f6bccb7befc9a9773be0ad2c8`;
- old M1 boundary: `ff62f8cb2ed95877e8e3598049e6408c579ed18a`;
- selected authoritative upstream `main`:
  `64a549c402fe941a9ea7702a453ecf60b0b70d94`, committed
  `2026-10-02T18:45:02Z`;
- full upstream M1 squash:
  `6cf5f29e825e9ed9552ea4cf8116ed90008b9683`.

Before mutation, local HEAD, `origin/scion/audit-update-m2`, and an independent
`git ls-remote origin refs/heads/scion/audit-update-m2` all equaled the old M2
head. The clone was shallow and did not initially contain the pinned boundary;
`git fetch --unshallow origin` recovered it from M2 history. No deleted M1 ref
was required or recreated. `git merge-base --is-ancestor` proved the boundary
was below old M2, the minimum main SHA equaled the selected main, and the full
M1 squash was below selected main.

The validated code/integration checkpoint after the transplant and upstream
drift closure is `951738ddb0cb3f15ffd0edff62ffa3a2f4aa66f6`.
The final publication head additionally contains this evidence file and is
reported in the restricted report and handoff because a commit cannot embed
its own SHA.

## Exact M2-only mapping

`git range-diff ff62f8cb..708455ed 64a549c4..<candidate>` mapped all eleven old
M2 commits in order:

| Old M2 commit | Rebased commit | Subject | Stable patch relation |
|---|---|---|---|
| `1434c7c30725c539c7a0fe3b5fc23d8575226686` | `7c645d820351406f1c91e38ecd3333a4a45c3922` | feat(audit): add authorization decision contract | identical, `837cebbb...` |
| `4e80d7f51e87c8311199126050309d9223240d58` | `26fec474d5186be3fee1d4bd891ec458d973e2dc` | docs(audit): record authorization contract evidence | identical, `c22b79e1...` |
| `1024fd4be38b6c695a058b8d580263676ce51b51` | `2ca11c78eb66c0b6e41b87fd93f1837d34ede2f3` | fix(audit): enforce authorization semantic relationships | identical, `6b8e431d...` |
| `d848ecfe2f27949865a292df3ee8f6cbdcebfd3b` | `ac72edadadf938d500116d385a5ef773817f76b9` | docs(audit): close A1 review findings | identical, `0bf21f90...` |
| `8c099c6343cad320fdc61848f84fc5b34a8db5d1` | `ed259c7c9ecd5511a82c965fde90c5ad8cd3d926` | test(audit): exhaust authorization contract matrices | identical, `45f64363...` |
| `2f41820dc28fd6abece8c6fe4fe03b7c828e5d99` | `b166628bca17ba1cc39048944110f0362c93921d` | docs(audit): record A1 round-2 closure evidence | identical, `62edd6d7...` |
| `6a9f6109dced5dba4cf001202d3641416fce8c72` | `9a803f669f84a059355a0d8259b7e8900e6bb7e8` | feat: add structural authorization audit reasons | adapted to upstream, old patch `447e160a...`, new patch `f4d968ad...` |
| `6fd2c6ac321f7024ec4fdc74cc0b65a9bf26e2ef` | `b868aa03b76c617b616b79ab73b948b394a8434b` | docs: record authorization producer contract evidence | identical, `c104c4d4...` |
| `792ceab146420daba8daf63998f819b4901e4337` | `be239d11957e6e0523c224af78de406afe56013d` | fix: close authorization producer contract gaps | adapted to upstream context, old patch `a2227a1e...`, new patch `09170f23...` |
| `01a548d6dedcd2618226570015362363b2c8b5ae` | `658b93341a617f1dc2b70a09fa95c6be55482250` | docs: record P1 review fix evidence | identical, `e82fb231...` |
| `708455edafd6e86f6bccb7befc9a9773be0ad2c8` | `c95210bd16d228854698a0aa9765336662db1bae` | test: trace authorization decision return origins | identical, `f3cdbc43...` |

Commit `951738ddb0cb3f15ffd0edff62ffa3a2f4aa66f6` is the rebase-only integration
closure. It adds the six lifecycle operations that landed in upstream's
canonical `authzop.Catalog`, refreshes the catalog digest, and assigns the
approved `not_authorized` reason to two upstream-added hub-delivery denial
exits. It does not add a P2 producer callsite or emitter behavior.

## Conflict record

The rebase stopped once, applying old commit `6a9f6109d`, with four content
hunks in `pkg/hub/authz.go`:

- Three principal/binding/role resolution-error literals: upstream added
  `DenyCauseResolutionError`; M2 added
  `AuditReason=dependency_unavailable`. The resolution retained both, without
  changing `Allowed`, prose, provenance, or return flow.
- Delegation-ceiling handling: upstream made check errors fail closed for all
  operations, added `DeniedByDelegationCeiling`, preserved typed `DenyCause`,
  constructed the normalized `ceilingReq`, and called the ceiling through
  `maskAuthzInputs(ctx)`. The resolution retained all of that upstream logic
  and added only `check_unavailable` for an error and `policy_denied` for a
  healthy ceiling denial. The obsolete old read-only exception was not
  restored.

No blanket ours/theirs resolution was used. The remaining commits replayed
without a textual conflict. Range-diff explains the other changed patch as
upstream import/context movement around the same producer contract.

## Semantic and scope audit

- The merge base of selected main and the candidate is exactly selected main.
  The upstream M1 squash is an ancestor of the candidate; the old M1 boundary
  is not. Thus no duplicate M1 commit history was replayed.
- All eleven old M2 commits map in order. Nine have identical stable patch IDs;
  the two adapted commits are accounted for above, and no old M2 commit became
  an unexplained no-op.
- The production diff is limited to the A1 contract under
  `pkg/hub/auditevent` and P1 producer files `authz.go`,
  `authz_candelegate.go`, `authz_relationship_rules.go`,
  `handlers_agents_core.go`, and `material_runtime.go`; tests and the M2
  project logs are the only other paths.
- A package-wide AST guard rejects any production `AuthzRequest.OperationID`
  read or population, any unauthorized `Decision.AuditReason` read/write, any
  missing/zero/dynamic/incompatible Decision origin, and unproved variable
  returns. It passes on this tree.
- No production `OperationID` population, authorization-decision emitter
  callsite, audit sink/store/sampling/transport cutover, API/UI change, or
  `audit_emit_dispatch` timing change is present. The A1 builder definition is
  contract foundation, not an emitter callsite.
- Authorization results, prose, errors, and side effects are unchanged. The
  only rebased production additions outside the original M2 patch semantics
  are structural audit classification of upstream-added deny exits and
  declarations for upstream-owned canonical operations.

## Bounded revalidation

No `make ci` or `make ci-full` command was run.

- Initial `go test -count=1 -p 2 ./pkg/hub/auditevent` and race equivalents
  correctly failed because upstream's canonical catalog had grown from 97 to
  103 operations. After adding exactly the six upstream lifecycle IDs and
  refreshing the snapshot digest to `da1554f8...`, normal passed in `0.322s`
  and race passed in `2.437s`. This covers the exhaustive 103 by 131
  operation/permission matrix (13,493 combinations; 103 exact mappings and
  13,390 rejected non-mappings), the two-outcome by 15-reason matrix, and
  catalog snapshot mutation/alias protection.
- The first P1 guard run correctly found two upstream-added Decision exits
  without structural reasons. After assigning `not_authorized` while retaining
  their exact deny behavior/prose, the focused normal suite passed (`4.010s`
  test runtime) and its single bounded `-race -p 2` invocation passed
  (`81.974s` test runtime, about eight minutes including compilation). This
  covers producer fields, every production origin, mutation fixtures,
  mappings, all six project-membership dependency-failure stages, metadata
  non-influence, and variable-return tracing.
- Focused existing integration behavior passed (`6.672s`): hub-delivery entry
  and delegation denial, delegation ceiling/error handling, owner/admin
  behavior, agent-target deny detail, and agent subroute catalog drift.
- `timeout 10m go vet -p 2 ./pkg/hub ./pkg/hub/auditevent`: PASS.
- The single heavy build,
  `timeout 10m go build -buildvcs=false -p 2 ./pkg/hub ./pkg/hub/auditevent`:
  PASS.
- `timeout 10m env GOGC=40 golangci-lint run
  --new-from-rev=64a549c402fe941a9ea7702a453ecf60b0b70d94
  --concurrency=1 ./pkg/hub/...`: PASS, `0 issues`.
- `gofmt` over every changed Go file and `git diff --check`: PASS.

No final required gate is inconclusive.

## P1 round-2 handoff

Commission a fresh P1 review round 2/7 against the final published head. The
review should focus on the four conflict-resolution properties above, the six
new canonical operation mappings, the two upstream-added hub-delivery reason
assignments, and the package-wide origin/metadata guard. P2 operation
propagation and all emitter work remain blocked until that independent review
is clean.

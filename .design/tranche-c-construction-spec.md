# Tranche C — construction spec (NOT a rebase)

Author: ca-msg-arch. Computed 2026-08-28 02:10Z against `upstream/main` = `1befe923`.
Source of material: `origin/scion/ca-msg-em9-unify` @ `25fad0a2`.

## Why this is a construction job and not a rebase

`ca-msg-em9-unify`'s merge base is `6268bac4` — **before tranche A**. The branch therefore contains a
**second, divergent copy of the same foundation** that tranche A already landed. Rebasing means
resolving a duplicate foundation against itself: 17 conflicts, including add/add on `dm_key_test.go`
and `backfill_test.go`. That is why `git cherry` reported all 103 commits outstanding — patch-id is
blind here, and the positive control against a known-merged SHA confirmed the tool, not the branch.

**Treat `em9-unify` as a quarry, not a base.** Cut a new branch from `upstream/main` and move
material onto it file by file.

## The exclusion list — the whole reason C was blocked

Seven files that `em9-unify` adds are **already delivered by work in flight**. `em9-unify`'s copies
are OLDER. Porting any of them silently reverts a fix that is currently in review.

**DO NOT PORT:**

| File | Delivered by | Reverting it would… |
|---|---|---|
| `pkg/messaging/divergence.go` | tranche B | undo the B4 divergence-format fix |
| `pkg/messaging/divergence_test.go` | tranche B | restore the false-green test (rule 73) |
| `pkg/messaging/dm_migration.go` | tranche B | undo B1 over-grant, B2 data-loss, B3 canonicality, B14 |
| `pkg/messaging/dm_migration_test.go` | tranche B | drop the fail-without-fix tests |
| `pkg/messaging/key_consolidation_test.go` | tranche B | — |
| `pkg/messaging/resolve_test.go` | DEF-26 (`bd5e492c`) | restore the misleading test name |
| `hack/check-conversation-upsert-guard.sh` | CI guard PR#1339 | restore GNU-only grep (rule 74) |

Note `resolve_test.go` and the guard script appear as **adds** in `em9-unify` because they did not
exist at its merge base. They exist on main now. An "add" that overwrites a newer file is the most
dangerous shape here because it does not look like a revert in review.

## The conflict set — five files touched by C *and* by in-flight work

These must be reconciled against **post-merge main**, not against `1befe923`. **C cannot be built
until PR#1338, PR#1339, DEF-26 and tranche B have landed** — or C must explicitly rebase onto each as
it lands.

| File | C's delta | Also touched by |
|---|---|---|
| `pkg/hub/handlers_agent_messaging.go` | +281 −0 | tranche B (dual-write; B5 security fix lands here) |
| `pkg/hub/messagebroker.go` | +83 −0 | tranche B |
| `pkg/hub/handlers_chat_v2.go` | +56 −4 | DEF-31 / PR#1338 |
| `Makefile` | +7 −3 | CI guard PR#1339 |
| `.github/workflows/ci.yml` | +3 −0 | em9's sqlite-gap job (option B) |

`handlers_agent_messaging.go` is the dangerous one: **+281/−0 from C, and it is the file containing
the B5 client-supplied-sender defect.** If C is applied over an un-fixed copy, or B5's fix is
reconciled by taking C's side, the security fix disappears with no deletion showing in the diff.
**Requirement: after reconciling this file, re-run tranche B's B5 test. It must still fail without
the fix.**

## Safe to port — 44 files

24 `pkg/messaging`, 8 `pkg/hub`, 8 `cmd` (incl. `broadcast.go`, `keys.go`), 2 `pkg/store`,
2 `pkg/messages`. These exist in neither main nor any in-flight branch.

Plus **74 modified files** touched only by C (44 in `pkg/hub`, remainder spread across
`extras/`, `docs-site/`, `pkg/config`, `pkg/hubclient`). These are ordinary reconciles: **main's
version is the base; port only C's genuine delta.**

Also 44 `.design/` project-log files — **noise. One docs commit at the end, or drop entirely.**

## Verification requirements

1. **Three-dot only.** `git diff main...branch`. Two-dot on a stale base looks like a catastrophic
   revert (rule 67).
2. **Deletion check (rule 31):** localise every deletion to a file before reacting. A new entity
   fails loudly; a *modified aggregate file* reverts silently. **The only reliable proof a prior
   tranche survived is an empty diff over the files main changed most recently.**
3. **Explicitly re-run, on the constructed branch:** B5's spoofed-sender test, B1/B2 migration
   probes, the DEF-31 mutation, and the four CI-guard probes. Each must still fail without its fix.
4. Remember `pkg/hub` tests **do not run in CI** (rule 76). Green CI is not evidence for anything in
   the conflict set. Mutation runs by hand are the only verification.

## Sequencing

C is **blocked** until the four in-flight items land. Do not start porting the conflict set before
then. The 44 safe adds *could* be staged early, but doing so creates a long-lived branch that will
itself go stale — **prefer to wait.**

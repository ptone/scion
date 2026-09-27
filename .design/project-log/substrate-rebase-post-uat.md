# Substrate post-UAT rebase (scion/substrate-integration-reworded onto origin/main)

**Date:** 2026-09-27
**Branch (in progress):** `scion/substrate-integration-rebased` (worktree, not yet complete)
**Status:** Blocked on a design decision; escalated to substrate-lead via sb-em.

## Task

Rebase `scion/substrate-integration-reworded` (174 substrate commits since merge-base `992de0a1`)
onto `origin/main` pinned at `d9b9e6a2e` (113 upstream commits since the same merge-base, includes
`df188970` / #1926 hub project-route authorization fix), and push to a new branch
`scion/substrate-integration-rebased` without touching the reworded branch.

## Progress

- Verified all pinned SHAs/merge-base/commit counts from the brief against the repo (initial shallow
  clone briefly made `origin/main` look like a 1-commit orphan; resolved with `git fetch --unshallow`
  before any destructive action — not a real history rewrite).
- Rebased 11/174 commits cleanly or with resolved conflicts:
  - `pkg/projectcompat/labels.go`: textual conflict between upstream's removal of two dead functions
    (`CanonicalFieldAliases`, `DeprecatedGroveRoute`, both lost their last caller in upstream's grove-removal
    commits #1944/#1960) and substrate's unrelated addition of two new functions at the same file
    location. Resolved by keeping upstream's deletions and re-adding substrate's functions.
  - `pkg/runtimebroker/errors.go`: substrate-internal ordering artifact (two substrate commits' error-code
    additions merging). Resolved by combining both sets of constants.
- Pushed a durability backup of this progress to `origin/scion/substrate-integration-rebased-wip`
  (not the final branch) in case the container is lost before the blocker resolves.

## Blocker

At commit 12/174 (`f9eb4f2b7`, "decide a stop's identity-unknown outcome from a single lookup, not a
re-derived one"), hit a substantive conflict in `pkg/runtimebroker/handlers.go`'s `stopAgent`/
`projectScopedTarget`: upstream's `fecc29222` (#1972, landed 2026-09-26 — after the reworded branch was
cut) independently and generically fixed the identical bug substrate's commit fixed specially (via a
`hasRecordlessProber`-gated re-implementation of the lookup, `projectScopedTargetErr`). Upstream's fix
makes substrate's specialized machinery look redundant, but deciding whether to drop it, keep it, or
partially merge it is a design call outside the "keep upstream behavior, reapply substrate intent"
mechanical rebase policy. Escalated to substrate-lead via sb-em; rebase paused in place
(`~/worktrees/substrate-rebase`) pending the decision. Full detail in
`projects/substrate-integration/sb-dev-rebase-report.md`.

## Remaining

162 more commits to rebase after the blocker resolves, then build/CI, hub-only isolation re-test, the
56-file upstream-changed list under pkg/runtime, pkg/runtimebroker, cmd/sciontool, pkg/sciontool, and
independent review. This log entry will be updated on completion.

# Substrate post-UAT rebase (scion/substrate-integration-reworded onto origin/main)

**Date:** 2026-09-27
**Branch:** `scion/substrate-integration-rebased` — pushed, complete (176 commits: 174 rebased +
2 follow-up commits described below).
**Status:** Done. Build green, substrate/sciontool/runtimebroker/hub packages all green, hub-only
isolation re-test complete. Awaiting independent review.

## Task

Rebase `scion/substrate-integration-reworded` (174 substrate commits since merge-base `992de0a1`)
onto `origin/main` pinned at `d9b9e6a2e` (113 upstream commits since the same merge-base, includes
`df188970` / #1926 hub project-route authorization fix), and push to a new branch
`scion/substrate-integration-rebased` without touching the reworded branch.

## Mechanical rebase

Verified all pinned SHAs/merge-base/commit counts from the brief against the repo (an initial shallow
clone briefly made `origin/main` look like a 1-commit orphan; resolved with `git fetch --unshallow`
before any destructive action — a local fetch artifact, not a real history rewrite). Rebased all
174 commits: most applied cleanly; two textual conflicts resolved mechanically (`pkg/projectcompat/labels.go`
— upstream's grove-removal deleting two dead functions vs. substrate adding two unrelated ones at the
same location; `pkg/runtimebroker/errors.go` — a substrate-internal commit-ordering artifact). No commit
became empty; no leftover conflict markers; `gofmt` clean throughout.

## Design decision #1: projectScopedTarget vs. projectScopedTargetErr

Hit a substantive conflict at commit 12/174 (`f9eb4f2b7`) in `pkg/runtimebroker/handlers.go`'s
`stopAgent`: upstream's `fecc29222` (#1972, landed after the reworded branch was cut) independently and
generically fixed the same restart-safety bug (`ptone/scion#1808`) substrate's own
`f9eb4f2b7`/`fb42f9317`/`9b7164c9d` chain fixed specially. Escalated to substrate-lead via sb-em rather
than guess, since reconciling them changes behavior on a restart-safety path.

**Ruling:** take upstream's fix verbatim for the primary-list/non-prober case; keep substrate's
specialized `projectScopedTargetErr`/`auxListAgentsSorted`/`hasRecordlessProber` machinery only for the
two properties upstream's fix doesn't cover — (a) auxiliary-runtime list-failure strictness (fixed scan
order, a match found elsewhere is authoritative over an earlier aux error) and (b) manager coherence
(`Stop` dispatches through the manager that actually produced the matched entry, not a second,
independently-resolved one). Dropped the substrate-only `errLookupListFailed` sentinel in favor of
upstream's `ErrAgentListUnavailable`/`ErrAgentNotFound`. Implemented across three of the rebase's own
commits (`f9eb4f2b7`, `fb42f9317`, `9b7164c9d` — each needed its own conflict resolution as they built on
each other), plus test updates (one renamed/re-asserted test for a since-corrected pre-#1985 assumption,
one new test pinning the non-prober path is upstream-verbatim).

## Design decision #2: ExecWithStdin (build gate)

Once the rebase completed, `go build -buildvcs=false ./...` failed: `SubstrateRuntime` didn't implement
`ExecWithStdin`, a method upstream's `5b1c8cf25` (#1894) added to the `Runtime` interface as a security
fix (reset-auth token via stdin, not exec argv). Substrate's own exec wire protocol
(`pkg/sciontool/substrate`) had no stdin field to extend it with — not a mechanical fix. Escalated;
substrate-lead ruled **the real fix, as its own commit** (rejected a stub as a regression, rejected
folding stdin into argv as reopening the argv-leak). Implemented: `ExecRequest.Stdin`/
`ExecResponse.StdinSupported` added to the wire protocol (server and independently-mirrored client
types), the control server pipes stdin into the exec'd command bounded by the existing body-size cap,
the client caps and forwards it, and a response missing `StdinSupported` while stdin was sent is a hard
client-side error (fails loud on version skew rather than silently running without input). Landed as its
own commit with real-component tests (real httptest handler + real subprocess exec on the server side;
real httptest-backed fake actor server on the client side) proving: stdin reaches the command, the
secret never appears in the spawned argv, oversize input is rejected without echoing, and a missing
`stdin_supported` flag is a hard client error.

Running the full test suites (not just the git rebase) surfaced two more pieces of fallout invisible to
git's textual merge: a `pkg/runtimebroker` test pinning the pre-#1985 "silently succeed" behavior that
design decision #1 deliberately changed (updated), and a file:line-keyed exec-safety guard in
`pkg/sciontool/rootexec` whose tracked line number shifted because the `ExecWithStdin` change added
lines above it (updated the allowlist entry, no new unresolved exec site).

## Build/test gates

`go build -buildvcs=false ./...`: green. `make test-hub-sqlite` (the project's own pkg/hub CI gate,
proper 15-minute timeout and documented skip-list — a bare `go test ./pkg/hub/...` hits the default
10-minute timeout on this suite's size regardless of these changes): green, ~13 minutes. `pkg/runtimebroker`,
`pkg/sciontool/...`, `cmd/sciontool/...`, `pkg/runtime/...`: all green.

## Isolation re-test (hub-only)

(a) df188970's own hub authorization tests (`TestListProjectAgentsRequiresAuthorization`,
`TestProjectAgentGet_AuthorizationGap`, `TestProjectAgentUpdate_AuthorizationGap`, and the env-hiding
response-body tests): all pass on the rebased head.

(b) Live repro on a real, standalone hub server (built from the rebased head) against a real SQLite
file: two projects, two project-scoped PATs, one agent seeded into project B (no docker/podman
available in this environment, so seeded the row directly via the store layer rather than a full
container-backed create — the isolation property lives in the HTTP authorization layer, not container
lifecycle). `patB` positive control sees its own project's agent (200); `patA` gets 403 on both list and
point-read of project B's agent — not the 404 the brief's example cites, because a PAT (no
`agentIdent` in context) hits the generic `s.authorize()` RBAC gate rather than the
`agentIdent.ProjectID()` check that specifically returns 404 for an *agent's own* bearer token. Both
gates cited by file:line in the report; the security property (no cross-project leak) holds either way.

## Deliverables

Pushed: `origin/scion/substrate-integration-rebased` (176 commits over `d9b9e6a2e`).
`scion/substrate-integration-reworded` untouched (rollback anchor intact). Full conflict table, both
design-decision writeups, build/test results, isolation re-test results, and the 56-file upstream-changed
list (scoping the cluster re-UAT substrate-lead schedules separately) are in
`projects/substrate-integration/sb-dev-rebase-report.md`. Awaiting independent review (fresh reviewer,
per the brief).

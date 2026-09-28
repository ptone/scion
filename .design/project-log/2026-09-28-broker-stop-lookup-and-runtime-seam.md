# Broker: unified stop-target lookup, per-profile-instances seam, runtime_scope, resolved profile types

**Date:** 2026-09-28
**Branch:** scion/substrate-refactor-r11r6 (off scion/substrate-refactor @ d2316f10c)
**Author:** dev-refactor-broker (Wave-2 refactor unit: R11 + R6, plus folded-in items)

## Commits

| SHA | Subject | Placement |
|---|---|---|
| 1e19610ff | runtimebroker: unify agent target lookup and make it strict about unlistable runtimes | R11 + runtime_scope (PR1) |
| 7d96319b0 | runtimebroker, hubclient: describe logs-unsupported handling generically | guard-grep cleanups (PR1) |
| 7d1b574d1 | runtime, runtimebroker: add a per-profile-instances runtime capability | R6 seam + broker (PR1) |
| 7194381fc | runtime: substrate reports per-profile instances | R6 substrate side (PR3) |
| b48af967b | runtimebroker: advertise each profile's resolved runtime type | buildInfoProfiles, general/upstream fix |
| e82d39512 | runtimebroker: pin the runtime_scope key on identity-unknown log lines | test pin for runtime_scope |

## R11 — stop-target lookup, general fix

**What was broken (all brokers):** `LookupContainerID` scanned auxiliary
runtimes in Go map order. It silently skipped an auxiliary runtime whose
`List` failed, and it reported "not found" when nothing matched.

- A transient `List` failure on the one runtime holding the agent looked
  like a genuinely absent agent, so `stopAgent` returned the idempotent 202
  without stopping anything.
- Two auxiliary runtimes reporting the same slug gave a different pick on
  different calls.
- `stopAgent`/`restartAgent` re-derived the manager through a second,
  independent lookup (`resolveManagerForAgent`). The container ID and the
  manager it was sent to could therefore come from different runtimes.

A strict twin (`projectScopedTargetErr`) already existed, but was used only
on brokers that have a `RecordlessActorProber`.

**Change:**
- There is now one implementation, `Server.lookupAgentTarget` (server.go):
  - auxiliary runtimes are scanned in sorted order;
  - a match is authoritative over a `List` error elsewhere;
  - no match plus any `List` error returns `ErrAgentListUnavailable`, not
    `ErrAgentNotFound`;
  - the matching manager is returned alongside the ID.
- `LookupContainerID` wraps it, and `projectScopedTarget` returns the
  manager too.
- `stopAgent` has lost its prober branch. `restartAgent` uses the matched
  manager for the stop half.
- `projectScopedTargetErr` is deleted, and `auxListAgentsSorted` moved to
  server.go.

**Broader reach, which is intended:** every `LookupContainerID` caller now
gets the strict semantics.
- exec and reset-auth map any lookup error to 404, so there is no external
  change.
- restart now aborts on "cannot determine" instead of proceeding.
- This matches `LookupAgent`'s existing list-unavailable semantics. It also
  closes the followups-pending item "Generalize stop-target strictness into
  LookupContainerID".

**Tests** (`stop_target_lookup_test.go`: plain docker default plus plain
auxiliary MockRuntimes, no capability):
- dispatch to the matching auxiliary manager;
- two auxiliary matches pick the sorted-first runtime, with the target and
  manager agreeing (30 iterations);
- auxiliary List error plus a match elsewhere → 202 on the matching runtime
  (both sort orders);
- auxiliary List error with no match → 5xx, not 202;
- `LookupContainerID` returns ListUnavailable (not NotFound) versus a clean
  NotFound.

**Mutation check:**
- Mutation: make `auxListAgentsSorted` swallow the recorded `List` error
  (the old lax behaviour).
- Result: `TestStop_AuxListErrorWithNoMatch_ExplicitErrorNot202` went red
  (202 instead of 5xx), as did
  `TestLookupContainerID_AuxListErrorWithNoMatch_IsListUnavailable`.
- Reverted; green again.
- The same test file against base d2316f10c (detached worktree) also fails
  `TestStop_TwoAuxMatches_TargetAndManagerAgree` (the base sent `c-b` to
  aux-b on the first iteration).

## runtime_scope log key

- The identity-unknown WARN lines (stop and delete) log the prober-supplied
  scope under `runtime_scope` (`logKeyRuntimeScope`) instead of `atespace`.
- `agentIdentityUnknownError.Atespace` is renamed to `.Scope`, and
  `recordlessActorProbe` is written generically.
- The value comes from `RecordlessActorProber.RecordlessActors` (the prober
  supplies it). The broker holds only generic text.
- The gaps tests pin `"runtime_scope":"<scope>"` in the JSON log.

## R6 — manager resolution: A/B/C narrative

**Brief:** resolve managers by profile, not by runtime type. Remove the
literal `runtimeType != "substrate"` exception in `resolveManagerForOpts`.

**Problem found:**
- The broker does not retain the default runtime's profile identity.
  `cmd/server_foreground.go` builds the default via
  `runtime.GetRuntime("", "")` and passes only the runtime and its manager
  to `New`.
- Every heuristic tried had a cost:
  - comparing the requested profile against the global `active_profile`
    moved docker behaviour in ways the tests do not bound (a project
    `active_profile` differs from the one the broker started with);
  - deep-comparing runtime configs changed docker behaviour for
    differently-keyed same-type entries, and re-broke the substrate
    second-profile fix for identical-looking configs.
- Per the stop rule, I stopped and reported to sb-em with three options:
  - **(A)** plumb a `DefaultProfile` through `ServerConfig` for true
    profile-based resolution;
  - **(B)** a capability seam;
  - **(C)** leave the literal and document it.

**Ruling (substrate-lead via sb-em):** (B).

**Implementation:**
- Re-scoped from "profile-based resolution" to a "capability seam", because
  the broker does not retain the default runtime's profile identity.
- `pkg/runtime/capabilities.go` gains `PerProfileInstancesRuntime`
  (`PerProfileInstances() bool`) plus `HasPerProfileInstances(rt)`.
- The broker takes the type-only shortcut unless the default runtime reports
  true, and the literal is gone.
- `SubstrateRuntime` implements it (true) in a separate commit.

**Placement note:** at 7d1b574d1 alone,
`TestResolveManagerForOpts_SubstrateProfilesGetTheirOwnConfig` is red. The
substrate implementation lives in its own commit per the placement ruling,
and it is green from 7194381fc on.

**Tests:**
- A docker default with two docker profiles (one keyed `docker-alt` with its
  own host), plus the active profile: all resolve to the default manager and
  no auxiliary runtime is created. The same test passes on base, so behaviour
  is identical.
- A capability reporting false keeps the shortcut.
- A capability reporting true gives a same-type profile its own manager.
- The existing substrate two-profile test (distinct egress policies) passes.

**Mutation check:**
- Mutation: force `HasPerProfileInstances` to return false.
- Result: `TestResolveManagerForOpts_SubstrateProfilesGetTheirOwnConfig`
  went red ("returned the default manager"), as did
  `TestResolveManagerForOpts_CapabilityTrue_SameTypeProfileGetsOwnManager`.
- Reverted; green again.

### Follow-up design note (option A)

True profile-based resolution would need the broker to know which profile
its default runtime was built from. Two parts are required:
- `cmd/server_foreground.go` records the profile name it resolved (or the
  settings' `active_profile` at startup) in a new
  `ServerConfig.DefaultProfile`;
- `resolveManagerForOpts` compares the requested (or effective) profile name
  against it before falling back to the type check.

The seam can stay as the rule for runtimes whose same-type profiles are
interchangeable. Not scheduled.

## buildInfoProfiles: resolved type

- `buildInfoProfiles` advertised the runtimes-map key as `Type` and ran the
  local-only filter on the key.
- Its effect on key≠type entries:
  - `substrate-nip` (type substrate) was advertised as "substrate-nip";
  - `k8s-staging` (type kubernetes) was advertised as "k8s-staging";
  - `workstation-docker` (type docker) escaped the local-only filter on a
    non-local broker.
- Now each profile resolves through `VersionedSettings.ResolveRuntime`,
  using the new helper `resolveInfoProfileRuntime`. Type, filter and
  Context/Namespace all come from the resolved runtime.
- Profiles naming no runtime, or a missing entry, keep their previous
  output.
- **Upstream check:** `git show d9b9e6a2e:pkg/runtimebroker/handlers.go`
  contains the identical function (fetched from origin; local main not
  used), so this is a general fix and the commit body says so.
- The key≠type test fails on base and passes here. The key==type test passes
  on both.
- The helper leaves one resolution point for per-profile attach (R2) to
  extend.

## Guard-grep cleanups

- errors.go:72-73 (plus :141 and :150-151) now point at
  `pkg/runtime.ErrLogsNotSupported` in capabilities.go generically.
- All seven handlers.go base sites (1834, 1904, 1957, 2007, 2048, 3135,
  3172) are rewritten generically or removed.
- The hubclient fixture (`TestAgentService_GetLogs_RuntimeLogsUnsupported`)
  now uses the current fixed `ErrLogsNotSupported` text, with the comment
  made generic.
- `grep -il 'substrate\|atespace'` over non-test pkg/runtimebroker/*.go
  finds only `pty_handlers.go`, which belongs to R2 and was deliberately
  untouched.

## Gates

All gates ran with an `env -i` scrub, via this wrapper:

```
env -i PATH=/usr/local/go/bin:/usr/local/bin:/usr/bin:/bin HOME=/home/scion TMPDIR=/tmp \
  GOCACHE=/scion-volumes/gocache GOPATH=/home/scion/go GOMODCACHE=/home/scion/go/pkg/mod GOFLAGS=-buildvcs=false
```

This leaves no `SCION_*` variables.

| Gate | Result |
|---|---|
| `go build -buildvcs=false ./...` | OK |
| `gofmt -l` on every touched .go file | empty |
| `go test -count=1` on ./pkg/runtimebroker/..., ./pkg/hubclient/..., ./pkg/runtime/..., ./pkg/config/..., ./pkg/agent/... | all ok |
| `make ci` | CI passed |
| `make ci-full` | fmt-check, web, web-typecheck, lint and check-custom pass. web-test fails nondeterministically: run 1 had 4 failing files (hook timeouts, terminal-transport); run 2 had 3 different ones (agent-create-projects, role-binding, terminal-transport). The branch has no diff under web/. golangci-lint reports 19 findings, all in files untouched by this branch or on lines from base (the handlers.go:1111 HydrateWithHash deprecation is blamed to d2316f10c). test-fast and build are covered by `make ci`. |
| R11 mutation | red → reverted → green |
| R6 seam mutation | red → reverted → green |

## Addendum: restart coverage and two small fixes

Review found that the restart consequences of the unified lookup were
correct but untested: reverting them left the suite green. Three restart
tests were supplied, landed as `pkg/runtimebroker/restart_target_lookup_test.go`
(16bc5ee5b). The header and names were made generic, and the cases are
unchanged:
- `TestRestart_AuxListErrorWithNoMatch_AbortsWithoutStart`: an auxiliary
  List error with no match → 5xx, no Start and no Stop.
- `TestRestart_FallbackStageListError_AbortsWithoutStart`: a default List
  failure in the unlabelled fallback stage → 5xx, no Start.
- `TestRestart_TwoAuxMatches_StopTargetAndManagerAgree`: over 30
  iterations, only aux-a's manager stops, and it stops `c-a`.

Verification (all run under the `env -i` wrapper):

| Check | Result |
|---|---|
| On the branch | all 3 pass |
| On base d2316f10c (detached worktree) | all 3 fail; on base, aux-b stopped `c-b` in the two-match case |
| Mutation: restart lets `ErrAgentListUnavailable` through | both abort tests red; reverted |
| Mutation: restart's Stop through `resolveManagerForAgent` instead of the lookup's manager | two-match test red (aux-b told to stop `c-a`); reverted |

Other fixes:
- d8e7f5ce8: `auxListAgentsSorted` takes the slug and stage again. Its
  debug line is once more `"Agent found via auxiliary runtime"` /
  `"... (fallback)"` with the `slug` and `runtime` attributes, as on base.
- de67bfe70: reflowed the ~105-column doc comment in
  `pkg/hubclient/agents_test.go`.

Clarification of "Broader reach" above:
- exec and reset-auth resolve their target ID through `LookupContainerID`
  (now strict; any error → 404).
- The runtime/manager they act on still comes from the separate, unsorted
  `resolveAgentRuntimeTarget` (`resolveRuntimeForAgent`).
- Extending the strict lookup to that half is a separate follow-up and is
  deliberately not done here.

Gates (`env -i` wrapper as above; no `SCION_*` set):

| Gate | Result |
|---|---|
| `go build -buildvcs=false ./...` | OK |
| `gofmt -l` on touched files | empty |
| `go vet` on runtimebroker and hubclient | OK |
| `go test -count=1` on ./pkg/runtimebroker/..., ./pkg/hubclient/..., ./pkg/runtime/... | ok |
| `make ci` | CI passed |

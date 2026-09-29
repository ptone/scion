# Broker: exec and reset-auth dispatch through the strict lookup's matching manager

**Date:** 2026-09-28
**Branch:** scion/substrate-refactor-exec-lookup (off scion/substrate-refactor @ 9f176b817b52d36afe30b99688ab27e3c9641a73)
**Author:** dev-refactor-exec-lookup

## Background

The project-log entry recording the earlier work that made the stop/restart
target lookup strict and sorted, and had stop/restart dispatch through the
manager whose `List` call produced the match
(`2026-09-28-broker-stop-lookup-and-runtime-seam.md`), flagged this
explicitly under "Clarification of Broader reach":

> exec and reset-auth resolve their target ID through `LookupContainerID`
> (now strict; any error → 404). The runtime/manager they act on still
> comes from the separate, unsorted `resolveAgentRuntimeTarget`
> (`resolveRuntimeForAgent`). Extending the strict lookup to that half is a
> separate follow-up and is deliberately not done here.

`execCommand` and `resetAuth` (`pkg/runtimebroker/handlers.go`) get that
same extension here.

## What was broken

`execCommand` and `resetAuth` each made two independent lookup calls:

- `resolveRuntimeForAgent` (-> `resolveAgentRuntimeTarget`) to pick the
  `scionrt.Runtime` to dispatch `Exec`/`ExecWithStdin` through — unsorted
  (Go map order) over auxiliary runtimes, and it never reports a `List`
  failure: an unlistable auxiliary runtime is silently treated as "no
  match" and the walk falls through to its unconditional final fallback,
  the default runtime.
- `LookupContainerID` (-> the strict, sorted `lookupAgentTarget` introduced
  when the stop/restart lookup was unified) to resolve the container id to
  act on.

Because these are two separate calls, the manager the operation is
dispatched through and the container id it is given can come from
different runtimes:

- with two auxiliary runtimes both matching the slug, the strict lookup
  deterministically picks the sorted-first one for the container id, while
  the unsorted lookup can independently land on either one for the
  manager;
- a transient `List` failure on the auxiliary runtime that actually holds
  the agent makes the unsorted lookup fall back to the default runtime for
  the manager, while the strict lookup (called moments later, or simply
  written to fail closed on the one attempt it makes) resolves the target
  id from the auxiliary runtime — sending that runtime's container id to
  the *default* runtime's `Exec`.

## Change

Both handlers now call `s.lookupAgentTarget` once and derive the runtime
from the manager it returns, via a new small helper:

```go
func (s *Server) runtimeForManager(mgr agent.Manager) scionrt.Runtime {
	if am, ok := mgr.(*agent.AgentManager); ok && am.Runtime != nil {
		return am.Runtime
	}
	return s.runtime
}
```

Every registered manager (default and auxiliary) is built by
`agent.NewManager`, so the assertion always succeeds in practice; the
`s.runtime` fallback is defensive only, matching the existing pattern in
`hasRecordlessProber`/`recordlessActorProbe`.

`resolveRuntimeForAgent`/`resolveAgentRuntimeTarget` are untouched and
still used by `getLogs`, which was out of scope here.

**HTTP status mapping is unchanged.** Both handlers still map any
`lookupAgentTarget` error (or an empty target) to the existing 404 —
exactly the "exec and reset-auth already map any lookup error to 404"
behaviour described in the background entry above. No change was needed
because nothing here alters what counts as an error; it only makes the
manager come from the same call that resolved the target.

## Tests (`pkg/runtimebroker/exec_lookup_test.go`)

1. `TestExecCommand_TwoAuxMatches_DispatchesToMatchingManager` — two
   auxiliary runtimes both match; registered in reverse name order and run
   for 30 iterations (mirrors `TestStop_TwoAuxMatches_TargetAndManagerAgree`)
   so Go's randomized map order can't hide a mismatch. Only aux-a's `Exec`
   is ever called, with `c-a`.
2. `TestResetAuth_TwoAuxMatches_DispatchesToMatchingManager` — same setup;
   both the token-write (`ExecWithStdin`, which falls back to `ExecFunc` on
   the mock) and the PID-1 signal (`Exec`) land on aux-a only, never aux-b
   or the default runtime.
3. `TestExecCommand_AuxListErrorThenMatch_DoesNotDispatchToDefaultRuntime` —
   a single auxiliary runtime whose `List` fails on its first call and
   matches on any later call. With `projectID` empty (so neither lookup's
   backward-compatibility fallback stage masks the failure behind a second
   attempt), the single fixed lookup sees only the failing call, fails
   closed with `ErrAgentListUnavailable`, and exec returns 404 without
   calling `Exec` on the default runtime *or* the auxiliary one. This is
   the "aborts rather than acting on the wrong runtime" case: before the
   fix, `resolveRuntimeForAgent`'s internal walk treated the same failure
   as "no match" and returned the default runtime, while the separate
   `LookupContainerID` call resolved the container id from the auxiliary
   runtime — sending that id to the default runtime's `Exec`.

**Verified against base** (`9f176b817b52d36afe30b99688ab27e3c9641a73`, via
`git stash` of just `handlers.go` — same test file, unmodified server
code): all three new tests fail on base and pass on the branch. Every
pre-existing exec/reset-auth test (`handlers_exec_test.go`,
`handlers_reset_auth_test.go`) is unaffected on both.

Naming note: `doExec`/`doResetAuth` were already taken by
`handlers_exec_test.go`/`handlers_reset_auth_test.go` with different
signatures, so this file's helpers are `doExecLookupTest`/
`doResetAuthLookupTest`.

## Gates

All gates ran with a full `env -i` scrub (`PATH`, `HOME`, `GOCACHE` only —
no `SCION_*` passed through):

| Gate | Result |
|---|---|
| `go build -buildvcs=false ./...` | OK |
| `gofmt -l` on `handlers.go` and `exec_lookup_test.go` | empty |
| `go test -count=1 ./pkg/runtimebroker/...` | ok (63.9s) |
| `make ci` | CI passed |
| 3 new tests vs. base (`9f176b817b52d36afe30b99688ab27e3c9641a73`) | all 3 fail on base, all 3 pass on branch |

## Pairing the runtime with the manager at match time

Further review found that `runtimeForManager` (the helper above that turned
a matched `agent.Manager` into a `scionrt.Runtime`) fell back to the
default runtime if its type assertion ever failed, which is exactly the
cross-runtime dispatch exec/reset-auth's own fix exists to remove —
currently unreachable (every registered manager is an `*agent.AgentManager`
with `Runtime` set), but silent if that ever changed.

`lookupAgentTarget` (`pkg/runtimebroker/server.go`) now returns the
matched `scionrt.Runtime` as a fourth value, paired with the
`agent.Manager` at the exact stage that produced the match — the default
runtime's own manager and runtime together, or a single `auxiliaryRuntime`
entry's registered manager and runtime together (via `auxListAgentsSorted`,
which now also returns the paired runtime). There is no longer an
intermediate step that re-derives a runtime from a manager after the fact:

- `execCommand` and `resetAuth` take the runtime directly from
  `lookupAgentTarget`'s return value.
- `runtimeForManager` is deleted.
- If the returned runtime is ever nil (unreachable today, since every
  manager/runtime pair is registered together), both handlers fail closed
  with `RuntimeUnavailable(w, "Agent runtime unavailable")` — 503 — rather
  than falling back to a default runtime that could differ from the one
  the container id came from. This is a new, distinct condition from the
  existing "lookup failed or returned an empty target" path, which still
  maps to 404 and is unchanged.
- `LookupContainerID` and `projectScopedTarget` (used by stop/restart)
  discard the new runtime value; their own behaviour is unchanged.

## Tests covering the runtime pairing

`pkg/runtimebroker/exec_lookup_gap_test.go` covers cases the tests above do
not reach: reset-auth's own List-error fail-closed path, a match on an
auxiliary runtime that is not the sorted-first one, and a match on the
default runtime while an auxiliary runtime also reports the slug. The
positional cases assert directly on `lookupAgentTarget`'s returned
manager/runtime pair, not only on which mock's `Exec` was called, and a
pair of tests covers the defensive guard: a matched auxiliary manager
registered with a nil paired runtime must return 503 from both handlers,
with zero `Exec` calls anywhere.

Verification:
- The reset-auth List-error case is a genuine regression guard: it fails
  against the pre-existing base (`9f176b817b52d36afe30b99688ab27e3c9641a73`,
  with `exec_lookup_test.go` carried over so the file compiles) and passes
  from the manager-dispatch fix above onward.
- The positional cases (a match on a later-sorted auxiliary runtime, and a
  match on the default runtime alongside an auxiliary match) pass
  unchanged against the manager-dispatch fix's own code — they exist to
  cover a mutation that code's own tests happened not to exercise, not to
  catch a bug that had already shipped. Confirmed by reintroducing that
  exact mutation: a stand-in for the deleted `runtimeForManager` that
  ignores the matched manager and always returns the sorted-first
  registered auxiliary runtime's runtime. The positional cases go red
  under it; every other test in the package stays green, showing the
  earlier coverage did not already catch this shape of bug.
- Mutation check on the paired-runtime return itself: changed
  `lookupAgentTarget`'s final return to always report `s.runtime` instead
  of the paired `matchRuntime`. Went red:
  `TestExecCommand_TwoAuxMatches_DispatchesToMatchingManager`,
  `TestResetAuth_TwoAuxMatches_DispatchesToMatchingManager`,
  `TestExecCommand_MatchOnLaterSortedAux_DispatchesToMatchingManager`,
  `TestResetAuth_MatchOnLaterSortedAux_DispatchesToMatchingManager`,
  `TestExecCommand_NilPairedRuntime_FailsClosedWithoutAnyExec`,
  `TestResetAuth_NilPairedRuntime_FailsClosedWithoutAnyExec`, and
  `TestLookupAgentTarget_TwoAuxMatches_SortedFirstDeterministic`. Reverted;
  green again.

## Tests covering the backward-compatibility fallback stage and the reset-auth call sequence

`pkg/runtimebroker/exec_lookup_fallback_gap_test.go` adds two more kinds of
coverage.

First, `lookupAgentTarget`'s unlabelled backward-compatibility fallback
stage (the retry that accepts a container carrying no project label, for
pre-label or solo/CLI agents) has to re-pair its own manager and runtime
just like the two stages above it, and none of the existing tests ever
reached that stage. Dropping the `matchRuntime = s.runtime` reset there
would make a legacy unlabelled agent on the default runtime unreachable
via exec/reset-auth (since stage one's auxiliary scan leaves a nil runtime
behind when nothing matches there); dropping the equivalent auxiliary
assignment would send an auxiliary container id to the default runtime.
Both are now covered: a legacy unlabelled agent on the default runtime,
and one on a non-first-sorted auxiliary runtime, each checked directly
against `lookupAgentTarget`'s return value and against the handlers'
actual dispatch.

Second, the existing reset-auth tests record only container ids, and
`MockRuntime.ExecWithStdin` falls back to `ExecFunc`, so they cannot tell
the stdin token write from the `kill -USR2 1` signal apart, or notice if
one were dropped or the two were swapped. A new test records which method
was called, the command, and the stdin payload, and checks the exact
two-call sequence (token via `ExecWithStdin`, then the signal via `Exec`,
both against the same container) for a match on a non-first-sorted
auxiliary runtime.

Verified by reverting the fallback stage's `matchRuntime = s.runtime`
reset: the three fallback-on-default tests go red, and the two
fallback-on-a-later-aux tests and the reset-auth call-sequence test stay
green, showing each test exercises the specific line it is meant to.
Reverted; green again.

## getLogs: deferred

`getLogs` still resolves its manager via `resolveManagerForAgent` and its
runtime via `resolveRuntimeForAgent` — both wrapping the unsorted,
fail-open `resolveAgentRuntimeTarget` — left untouched.

- `resolveAgentRuntimeTarget` was **not deleted**: `resolveManagerForAgent`
  has three other live (non-test) callers besides `getLogs` —
  `getAgent`, `sendMessage`, and `projectScopedTarget`'s own solo/CLI
  fallback branch (`pkg/runtimebroker/handlers.go`) — all out of scope
  here. `getLogs` is not its last live caller, so the condition for
  deletion does not hold regardless of whether `getLogs` itself is
  migrated.
- Migrating `getLogs`'s own call site to `lookupAgentTarget` was not
  attempted because it is not a small, in-this-file change: unlike
  exec/reset-auth, `getLogs` needs the full matched `api.AgentInfo` record
  (`ProjectPath` and `Slug`/`Name`) to build the on-disk `agent.log` path
  before it ever falls back to container logs. `lookupAgentTarget` returns
  only a container id plus the manager/runtime pair, not the matched
  entry. Giving `getLogs` that entry from a single lookup would mean
  broadening `lookupAgentTarget`'s return contract, which ripples into
  every other caller (stop, restart, exec, reset-auth,
  `projectScopedTarget`) — a change of much larger scope than a fold in
  this file. Left for separate follow-up work if wanted.

## Aux-error precedence for exec/reset-auth: left unchanged

The precedence rule — a match found on one auxiliary runtime is
authoritative over an earlier auxiliary runtime's `List` error — lives in
`auxListAgentsSorted`, shared by every `lookupAgentTarget` caller: stop,
restart, exec, reset-auth, and `projectScopedTarget`. Making exec/reset-auth
fail closed on any aux `List` error, even with a match found elsewhere,
would require either a second, stricter lookup variant used only by the
privileged operations — reintroducing the exact per-caller lookup
duplication the stop/restart unification above was written to eliminate —
or changing the shared semantics broker-wide, which would break the
intentionally-pinned stop behaviour in
`TestStop_AuxListErrorWithMatchElsewhere_Succeeds` (a 202 success in
exactly this scenario). Left as a cross-cutting precedence question for
separate follow-up work if the aux-error precedence rule for privileged
operations needs to be revisited broker-wide.

## Leaving the no-match runtime reset alone

Also considered and declined: changing the lookup so a stage that finds no
match leaves `matchRuntime` untouched instead of resetting it (the
mirror-image of the fallback-stage fix above, applied defensively even
where nothing currently depends on it). The current code is correct as
written, and the fallback-stage tests above pin the reset line directly —
reverting it turns three tests red, so a future regression here fails the
test suite rather than surfacing silently. Declined as unnecessary
defensive churn on code already covered by a direct test.

## Gates

All gates ran with a full `env -i` scrub (`PATH`, `HOME`, `GOCACHE` only —
no `SCION_*` passed through), `GOCACHE=/scion-volumes/gocache`:

| Gate | Result |
|---|---|
| `go build -buildvcs=false ./...` | OK |
| `gofmt -l` on `server.go`, `handlers.go`, `exec_lookup_test.go`, `exec_lookup_gap_test.go`, `exec_lookup_fallback_gap_test.go`, `substrate_restart_isolation_test.go`, `substrate_restart_lookup_edge_test.go` | empty |
| `go test -count=1 ./pkg/runtimebroker/...` | ok (~61s) |
| `make ci` | CI passed |
| Paired-runtime mutation check (always return `s.runtime`) | red on 7 tests, reverted, green again |
| Positional-pick mutation check (stand-in for the deleted `runtimeForManager`, sorted-first regardless of match) | red on the 3 positional gap tests only, reverted, green again |
| Fallback-stage reset mutation check (drop `matchRuntime = s.runtime` in the unlabelled fallback stage) | red on the 3 fallback-on-default tests only, reverted, green again |
| Reset-auth List-error case vs. the pre-existing base (`9f176b817`) | fails, as intended (genuine regression guard) |
| Positional gap tests vs. the manager-dispatch fix's own commit (`d5dd16bc2`) | pass unchanged (coverage for a mutation that commit's own tests did not exercise) |

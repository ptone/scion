# Broker: exec and reset-auth dispatch through the strict lookup's matching manager

**Date:** 2026-09-28
**Branch:** scion/substrate-refactor-exec-lookup (off scion/substrate-refactor @ 9f176b817b52d36afe30b99688ab27e3c9641a73)
**Author:** dev-refactor-exec-lookup

## Follow-up this closes

R11's own project-log entry
(`2026-09-28-broker-stop-lookup-and-runtime-seam.md`) flagged this
explicitly under "Clarification of Broader reach":

> exec and reset-auth resolve their target ID through `LookupContainerID`
> (now strict; any error → 404). The runtime/manager they act on still
> comes from the separate, unsorted `resolveAgentRuntimeTarget`
> (`resolveRuntimeForAgent`). Extending the strict lookup to that half is a
> separate follow-up and is deliberately not done here.

This unit does that extension, for both `execCommand` and `resetAuth`
(`pkg/runtimebroker/handlers.go`).

## What was broken

`execCommand` and `resetAuth` each made two independent lookup calls:

- `resolveRuntimeForAgent` (-> `resolveAgentRuntimeTarget`) to pick the
  `scionrt.Runtime` to dispatch `Exec`/`ExecWithStdin` through — unsorted
  (Go map order) over auxiliary runtimes, and it never reports a `List`
  failure: an unlistable auxiliary runtime is silently treated as "no
  match" and the walk falls through to its unconditional final fallback,
  the default runtime.
- `LookupContainerID` (-> the strict, sorted `lookupAgentTarget` from R11)
  to resolve the container id to act on.

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
still used by `getLogs`, which this unit was not scoped to touch.

**HTTP status mapping is unchanged.** Both handlers still map any
`lookupAgentTarget` error (or an empty target) to the existing 404 —
exactly the "exec and reset-auth already map any lookup error to 404"
behaviour R11 documented. No change was needed because nothing here alters
what counts as an error; it only makes the manager come from the same call
that resolved the target.

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

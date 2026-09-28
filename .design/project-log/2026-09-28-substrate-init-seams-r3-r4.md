# Project Log: Explicit Init Options Replace a Runtime Sniff and a Core-Owned Privilege-Drop Probe

**Date:** 2026-09-28
**Component:** `cmd/sciontool/commands/init.go`, `cmd/sciontool/commands/substrate_serve.go`, `cmd/sciontool/commands/substrate_privilege_drop.go` (new), `pkg/sciontool/rootexec/guard_test.go`, `cmd/sciontool/commands/substrate_rootfs_test.go`

## A runtime name sniff contradicted the option struct's own doc comment

`RunInit`'s decision to skip the hub port-forward tunnel manager and
auto-expose read `os.Getenv("SCION_RUNTIME") == "substrate"` directly,
while `InitRunOptions`'s own doc comment for a neighboring field claimed
"RunInit never derives [behaviour] here from SCION_RUNTIME or any other
sniffing." Replaced the sniff with an explicit `InitRunOptions.DisablePortForwarding
bool` that substrate-serve's `InitRunner` sets, following the same "set
only by substrate-serve, never by an env var a workload could set itself"
pattern the existing `RequirePrivilegeDrop`/`WorkingDir` fields already use.
The log line no longer names substrate or the env var. Default `false`
preserves every other caller's behaviour unchanged; the doc-comment
contradiction is now actually true instead of aspirational.

## A substrate-only feasibility probe lived in core, unreachable from RunInit itself

`checkPrivilegeDropFeasible`, `findSetuidRootSudo`, their
dependency-injection struct, and the `pkg/substratecaps` import lived in
`init.go`, even though the probe exists only for substrate-serve's
synchronous `/bootstrap` precondition (wired independently into
`pkg/sciontool/substrate`'s `Server`, never called from `RunInit`'s own
code path at all). Moved all of it — plus `hasCapBit`, its only remaining
caller once the move happened — into a new substrate-owned file,
`substrate_privilege_drop.go`, in the same Go package (so no import churn
at any call site); `init.go` no longer imports `pkg/substratecaps` or
declares any of these symbols.

Rather than leave the move purely mechanical, added a genuine second call
site: `InitRunOptions.PrivilegeDropPrecheck func() error`, invoked by
`RunInit` right after `setupHostUser` runs, gated on
`RequirePrivilegeDrop && PrivilegeDropPrecheck != nil`. This runs *before*
the existing `requirePrivilegeDropOrFail` check, which is itself completely
unmodified — the two are independent: one checks `setupHostUser`'s actual
result, the other re-verifies the preconditions a drop depends on
(capabilities, the "scion" user, a usable home directory). substrate-serve
wires in the same `substrateServePrivilegeDropChecker` function already
serving the HTTP bootstrap gate, so `RunInit` now runs the identical
feasibility probe as a second, independent layer of defense in depth —
not a new mechanism, no behaviour change beyond that added layer, and a
nil precheck (every caller but substrate-serve) is a no-op that falls
straight through to the pre-existing check.

## Fail-closed proof and its mutation check

Three tests: enforced mode with a nil precheck still fails closed via the
unmodified `requirePrivilegeDropOrFail` (proving the new nil-tolerant call
site doesn't short-circuit it); a supplied precheck is actually invoked and
its error is honored even when the underlying drop otherwise succeeded
(proving the hook isn't dead wiring); and the precheck is never invoked
when `RequirePrivilegeDrop` is false (proving it's gated the same way the
existing check is). Mutation check: removed the new call site from
`RunInit`, confirmed the "invoked when present" test fails while the other
two still pass (they don't exercise that path), then restored it and
reconfirmed green.

## Two other guard tests needed updates, neither a behaviour change

Moving ~175 lines out of `init.go` shifted the line numbers two pre-existing
static guards key off of by exact position:
`pkg/sciontool/rootexec/guard_test.go`'s `execSiteAllowlist` (every allowed
root-context `exec.Command` call site in `init.go`, keyed by line) needed
its 16 tracked entries recomputed by content-matching against their
pre-edit positions. `substrate_rootfs_test.go`'s
`TestSudoHardeningOnlyReachableFromItsOwnWiring` needed one new allowlist
entry (`substrateServeInitOptions` now legitimately references
`substrateServePrivilegeDropChecker` to build the new field) — a real,
intentional reference the design calls for, not a regression the guard was
right to catch and flag first.

## Verification

`go build -buildvcs=false ./...`, `go vet ./...`, and `gofmt -l .` clean
across the full repo. `go test -count=1` green with zero failures across
`pkg/config/...`, `cmd/sciontool/commands/...`, `pkg/sciontool/rootexec/...`,
`pkg/runtime/...`, `pkg/runtimebroker/...`, `pkg/sciontool/...`, and
`cmd/...` — including zero recurrence of the environment-only failures an
earlier round on this same tree reported, confirming those were
environment-dependent rather than something this change touches. `git diff
--stat` against the base commit shows no change under `deploy/`. Two
commits on `scion/substrate-refactor-init`
(`0939664d382b387480d492a714584a5387b233f0`,
`cd1e57277c8446a8cd3e94ff3e9f0a088eb1205e`), both pushed; `scion/substrate-refactor`
itself untouched.

# Project Log: R4 Corrected to a Pure Move After a Design-Authority Ruling

**Date:** 2026-09-28
**Component:** `cmd/sciontool/commands/init.go`, `cmd/sciontool/commands/substrate_serve.go`, `cmd/sciontool/commands/substrate_privilege_drop.go`, `cmd/sciontool/commands/substrate_rootfs_test.go`, `pkg/sciontool/rootexec/guard_test.go`

## A relocation grew a second invocation of the thing it relocated

The first pass at R4 (moving `checkPrivilegeDropFeasible` and its supporting
code out of `init.go` into a substrate-owned file) also added a new call
site: `InitRunOptions.PrivilegeDropPrecheck func() error`, invoked from
`RunInit` right after `setupHostUser` runs. The reasoning at the time was
defense-in-depth — the same feasibility probe substrate-serve already runs
synchronously before `/bootstrap` responds, run again from inside `RunInit`
itself as a second, independent check.

Design-authority review caught that this doesn't fit the letter of what R4
asked for. At base, `checkPrivilegeDropFeasible` has exactly one caller:
substrate-serve's synchronous `/bootstrap` precondition, which runs *before*
`RunInit` ever starts (before `setupHostUser`'s home realignment). Adding a
second call from *inside* `RunInit`, *after* realignment, is a new
fail-closed mode with a materially different timing — not a side effect of
moving code between files. Ruling: R4 must be a pure move; the new call site
doesn't belong in it.

## The fix

Removed, entirely: the `InitRunOptions.PrivilegeDropPrecheck` field and its
doc comment, the new block in `RunInit` that called it, the
`exitCodePrivilegeDropRequired` doc-comment sentence describing it, and the
three tests built around it
(`TestRunInit_Enforced_NilPrecheck_StillFailsClosed`,
`TestRunInit_Enforced_PrecheckInvokedWhenPresent`,
`TestRunInit_PrecheckNotCalledWhenPrivilegeDropNotRequired`).
`substrate_serve.go`'s `substrateServeInitOptions` is back to exactly base's
`RequirePrivilegeDrop`/`ResolveWorkingDir` wiring, plus R3's
`DisablePortForwarding` (a separate, unaffected item) —
`substrateServePrivilegeDropChecker` itself is untouched and still wires the
same feasibility check into the `Server`'s synchronous `/bootstrap`
precondition, exactly as it did at base.

What remains, and is the actual R4: `checkPrivilegeDropFeasible`,
`findSetuidRootSudo`, `privilegeDropPreconditionDeps`,
`defaultPrivilegeDropPreconditionDeps`, `errPrivilegeDropPrecondition`,
`hasCapBit`, and the `pkg/substratecaps` import, relocated from `init.go`
into `substrate_privilege_drop.go` (same package, no import churn at any
call site), reachable only from their pre-existing substrate-serve wiring —
confirmed by grep that `init.go` declares none of these symbols and no
longer imports `pkg/substratecaps`.

## Collateral guard-test reverts

The removed hook had required two static-guard updates in the first pass;
both reverted:
`substrate_rootfs_test.go`'s `TestSudoHardeningOnlyReachableFromItsOwnWiring`
allowlist entry for `substrateServeInitOptions` (that file is now
byte-identical to base again — confirmed with `diff`), and
`pkg/sciontool/rootexec/guard_test.go`'s `execSiteAllowlist`, whose 16
tracked line numbers were re-synced to `init.go`'s new (smaller) layout by
content-matching each call site against its prior position.

## Verification

`go build -buildvcs=false ./...`, `go vet ./...`, `gofmt -l .` clean across
the full repo. `go test -count=1` green across
`cmd/sciontool/commands/...`, `pkg/sciontool/rootexec/...`,
`pkg/runtime/...`, `pkg/runtimebroker/...`, `pkg/sciontool/...`,
`pkg/config/...`, and `cmd/...`. `git diff --stat` against the base commit
confirms no change under `deploy/`, and that `substrate_serve.go`'s only
remaining diff from base is R3's unrelated `DisablePortForwarding` field.
Commit `4ea459969ad34e4e2d12de3da3d04a0e710052e7`, pushed to
`scion/substrate-refactor-init`; `scion/substrate-refactor` itself
untouched.

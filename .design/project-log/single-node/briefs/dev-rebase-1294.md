# Brief: dev-rebase-1294 — rebase integration branch onto upstream main

**Dispatched by** `sn-impl-arch` on ptone's instruction, 2026-08-25.
`sn-impl-em` has completed and exited; the integration branch has no owner, so this
is a standalone task.

## What to do

Rebase **`scion/sn-impl-em`** (currently `a7d5918`, carrying P0–P3 of the Cloud Run
Instances + Sandboxes runtime) onto **upstream** `main`.

**Note the remote.** `origin` is the fork `ptone/scion`, whose main is at `aedf89e`
and does **not** yet contain the change we want. The target is
`https://github.com/GoogleCloudPlatform/scion.git` `main`, currently `a34deb91`:

```
a34deb91 feat: add ExitCode and ExitReason structured fields (#1294)
```

That PR is issue #1257 Phases 1+2: `ExitCode *int` and `ExitReason string` on the
heartbeat, `hubclient.Agent` and `api.AgentInfo`; `exit_code`/`exit_reason` persisted;
22+ `ContainerStatus` string-matching call sites migrated.

**Expect conflicts in `pkg/hub/handlers_runtime_brokers.go`, `pkg/agent/list.go`, and
the runtime files** — #1294 touches the same surfaces P3 does. That is why we are
rebasing now rather than later.

## What NOT to do — read this before you touch the stopgap

`pkg/runtime/cloudrun_sandbox_runtime.go:707` carries a deliberate stopgap that
reports `ExitCode=nil` for stopped sandboxes. Its comment says to start reporting the
real exit code once #1257 Phase 2 lands.

**Phase 2 has landed and the stopgap must still stay.** I checked; the reason it
exists is intact:

- `handlers_runtime_brokers.go:718-724` still promotes `PhaseStopped → PhaseError`
  whenever `ExitCode != 0`, **without consulting `ExitReason` at all**.
- `state.ExitReason` (`pkg/agent/state/state.go:134-148`) has only `crashed` and
  `limits_exceeded`. There is **no** `platform_eviction` value.

On this tier, Instance teardown SIGKILLs every sandbox at once and teardown is the
*normal* lifecycle (Tier 0 is pure ephemeral). Reporting real exit codes today would
put the **entire fleet into PhaseError on every routine redeploy**.

**So: keep the stopgap. Do not "finish the TODO."** It looks like an obvious cleanup
and it is a trap.

**Do update its comment**, because it currently points at the wrong blocker. Replace
the "#1260 / Phase 2 of #1257" reference with the actual remaining conditions, both
of which must hold before it can be unwound:

1. `state.ExitReason` gains a `platform_eviction`-style value, **and**
2. the phase-promotion logic in `handlers_runtime_brokers.go` consults `ExitReason`
   before promoting `PhaseStopped → PhaseError`.

Condition 2 is the load-bearing one — adding the enum value alone changes nothing.

## Scope

- The rebase.
- The stopgap comment correction above.
- Whatever mechanical fixes the rebase requires to build and pass tests.

**Nothing else.** Do not start P4. Do not implement `ExitReason` reporting. If the
rebase surfaces a design question, message `sn-impl-arch` rather than deciding it.

## Conventions

- Work on your own branch; **push your own branch, never force-push
  `scion/sn-impl-em`** without saying so explicitly in your report.
- `go build ./...` and `go test ./...` must pass before you report done.
- Report to `sn-impl-arch` with: the conflicts you hit, how you resolved each, and
  test status. A rebase report that just says "done, tests pass" is not reviewable.

## Termination

Complete when the rebase is pushed, builds, tests pass, and you have reported. A
fresh reviewer will follow.

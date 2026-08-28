# PR #1352 follow-up — the environment predicate is gated behind a caller preference

Author: sn-impl-arch (architect). Date: 2026-08-28. Branch `scion/task-92-runtime-profile-fix`
@ `dc729e2`. **Dispatched. Start now.**

**GoogleCloudPlatform/scion#1352 is OPEN upstream.** Additive commits update it. **Do not rebase, do
not amend, do not force-push.** `dc729e2` is approved; the reviewer will be given your delta only.

**You are not executing `briefs/sn-runtimeprofile-dev-shapeb-HELD.md`.** That brief is held pending a
decision from ptone and touches a different file. If you find yourself in
`pkg/runtimebroker/handlers.go`, you are in the wrong task.

## The finding

`pkg/config/init.go:588`:

```go
if opt.SkipRuntimeCheck && isCloudRunSandboxEnvironment() {
    // seed embeds/default_settings_cloudrun_sandbox.yaml
} else {
    var detectedRuntime string
    if !opt.SkipRuntimeCheck {
        detectedRuntime, err = DetectLocalRuntime()   // <-- cannot succeed on an Instance
        if err != nil { return err }
    } else {
        detectedRuntime = "docker"
    }
    ...
}
```

**State the asymmetry, because it is the reason the fix is right and not merely tidier.**
`isCloudRunSandboxEnvironment()` is a **fact about the machine**: `CLOUD_RUN_INSTANCE` is set and
`/usr/local/gcp/bin/sandbox` exists. `SkipRuntimeCheck` is a **caller preference**. Gating the fact
behind the preference means a caller who asks for runtime detection, on a platform where detection
*cannot* succeed, gets a hard error and no seeded template — the exact task #92 failure the branch was
written to remove.

The environment predicate should dominate. Roughly:

```go
if isCloudRunSandboxEnvironment() {
    // seed the cloudrun template; the machine settles this, not the caller
} else if ... existing logic unchanged ...
```

## What I do NOT know, and you must answer with an assertion rather than an opinion

**Is this reachable in production, or only in principle?** That is the difference between a defect and
a tidy-up, and it decides what the commit message may claim.

> **Enumerate every caller of `InitMachine`** and report, per caller, what it passes for
> `SkipRuntimeCheck`. If every production path on this tier already passes `true`, say so plainly — the
> fix still stands as defence in depth, but the commit message must not imply it fixes a live break.

## The assertions I want, named exactly

There is a test seam: `sandboxBinExists` (`init.go:683`). Use it.

1. **The one that matters.** `CLOUD_RUN_INSTANCE` set, `sandboxBinExists` true, **`SkipRuntimeCheck:
   false`**. Call `InitMachine`. Then **read the seeded `settings.yaml` off disk and assert its content
   equals `embeds/default_settings_cloudrun_sandbox.yaml`.**
   **Do not assert merely that no error was returned.** "It did not fail" is satisfied by seeding the
   wrong template, which is the actual bug.
2. **The negative, and it is the one that protects everyone else.** `CLOUD_RUN_INSTANCE` unset,
   `sandboxBinExists` false, `SkipRuntimeCheck: false`. Assert the seeded content is the **workstation**
   default and that `DetectLocalRuntime` is still consulted. Removing a guard is how you accidentally
   seed the cloudrun template on a laptop.
3. Keep the existing `SkipRuntimeCheck: true` cases green, unchanged.

**Mutate every pin and read WHY it went red** — a red is necessary, not sufficient. Specifically:
invert the new condition to `if false` and confirm assertion 1 goes red *on the template content*, not
on an error string.

## Constraints

- Additive commit on `scion/task-92-runtime-profile-fix`. Do not remove, weaken, or "simplify away"
  anything in `54cc98b`.
- Push to `ptone/scion` only. **No upstream PR, no merge** — ptone's gate.
- Announce the push to me with the file list.
- Never print an access token. Touch no Instance: `e2e-omni`, `e2e-walk-r2`, `iap-demo`, `q2-control`,
  `sn-adminseed-t`, `sn-adminfix-t`, `sn-step6`, `sn-walk`, `sn-ready`, `sn-harness-lab`.
  **A restart IS a deletion.**
- Fully qualify issue numbers: local is `task #92`; GitHub is `owner/repo#NNNN`.

## Tell me what in here is wrong

If the caller survey shows every path already passes `SkipRuntimeCheck: true`, **lead with that** — it
downgrades this from a live defect to hardening, and I would rather know before it reaches a reviewer.

# Substrate PR3 — round-4 code review (H-77 fix + fix3 delta)

Re-reviewed only the three commits added on top of the round-3-approved PR3
tip (`644293a2c..975812a` on `scion/substrate-pr3-fix3`, based on current
main). This was done as a fresh reviewer, and it covered the delta's security
review as well. Verdict: **APPROVE**. There are no Critical, High or Medium
findings, plus one Low item and two Info notes.

## What was checked
- **Startup refusal scoped to config validation.** Only deterministic config
  failures mark a substrate runtime as invalid at boot: missing required
  endpoints, or rejection by the substrate config validator. Construct-time
  dependency failures, such as the cluster client or the API dial, now leave
  the broker running in a degraded state instead of refusing to start. The
  operator-only profile check still runs on every runtime resolution,
  including per-request resolution. Narrowing the boot marker therefore
  opens no path for a project-scoped profile to define or override an
  operator-only runtime.
- **No leak on the degraded path.** The startup log line shows only the
  constant runtime name. Clients still get a fixed, opaque error message.
  The change does not alter the text of any error.
- **gid-0 exec guard.** `/exec` now refuses a target user whose primary group
  is root. The check sits in the same place as the existing uid-0 refusal,
  before the same-identity shortcut. Supplementary groups are always cleared
  for the child, so checking only the primary group is enough.
- **Docs.** The startup-refusal docs, the egress-allowlist summary, the
  timeout-invariant note and the exec-redaction-after-restart note in
  `deploy/substrate/OPERATIONS.md` all match the code.

## Verification
- Build, vet and scoped golangci-lint are clean. Scoped tests in the runtime,
  broker, sciontool and cmd packages are green.
- **Mutation checks:**
  - Each half of the startup-refusal test pair goes red independently.
  - Each endpoint row goes red only when its own check is removed.
  - The gid-0 negative test goes red when the guard is removed, and the
    positive control goes red when the guard is made over-broad.
- The guard's position before the shortcut cannot be pinned by a portable
  test. Code review and the doc comment cover it.

## Low item raised (non-blocking)
- **A degraded default runtime is never retried.** The broker resolves its
  default runtime once, at boot. If that runtime ends up degraded, the
  broker's health endpoint still reports healthy. So no probe restarts the
  broker, even though the docs call "a restart" the retry.
- **Suggested follow-up:** have the health endpoint report a degraded
  default runtime, or reword the docs and add an operator alerting note.
- **Scope:** this only affects an operator who makes substrate the default
  runtime, which the design does not intend.

# Review `e858e917` — delta only, on `scion/task-92-runtime-profile-fix`

Author: sn-impl-arch (architect). Date: 2026-08-28. **Dispatched. Start now.**

**Review the DELTA only: `e858e917`.** Everything below it (`dc729e2`, `1c22442`, `54cc98b`) was
reviewed and approved in earlier rounds. Do not re-open it. `git show e858e917` is your surface: two
files, `pkg/config/init.go` (+comment) and `pkg/config/init_test.go` (two new tests).

**GoogleCloudPlatform/scion#1352 is OPEN upstream.** Do not rebase, amend, force-push, merge, or open
a PR.

## What the change is

`pkg/config/init.go:594` (was `:588`):

```go
- if opt.SkipRuntimeCheck && isCloudRunSandboxEnvironment() {
+ if isCloudRunSandboxEnvironment() {
```

The reasoning, which you should check as hard as the code: `isCloudRunSandboxEnvironment()` is a **fact
about the machine** (`CLOUD_RUN_INSTANCE` set **and** `/usr/local/gcp/bin/sandbox` present).
`SkipRuntimeCheck` is a **caller preference**. Gating the fact behind the preference sends a caller on
an Instance into `DetectLocalRuntime()`, which cannot succeed there, producing a hard error and no
seeded template — the task #92 failure this branch exists to remove.

## The four things I want you to actually check

1. **Is the negative direction safe?** This *widens* when the cloudrun template gets seeded. The
   developer's negative test covers `CLOUD_RUN_INSTANCE` unset **and** seam false. **The case I want
   named is env set, seam false** — a Cloud Run *service* (the multi-node tier), not an Instance. My
   reading is that the predicate is an unchanged two-condition conjunction, so that case still takes
   the else branch. **Confirm it rather than agreeing with me.** Seeding the cloudrun template on a
   tier that cannot use it would be a worse defect than the one being fixed.

2. **The caller survey, and one word in it.** The developer surveyed three `InitMachine` callers and
   concluded every production Cloud Run path already passes `SkipRuntimeCheck: true`, so this is
   **defence in depth, not a live break**. I asked for that answer and I believe it. But
   `pkg/hub/system_handlers.go:514` passes `false` and was described as *"normally finds settings
   already seeded by server startup."* **"Normally" is load-bearing and unmeasured.** Is there an
   ordering where `/api/system/init` runs before server startup has seeded — first boot, a race, a
   wiped `~/.scion`? If yes, this is a live break after all and the commit message understates it.
   Either answer is fine; I want it settled.

3. **The tests assert content, not absence of error.** I required that, because seeding the *wrong*
   template also returns no error. The developer reports it could not do a byte comparison because
   `ensureBrokerID()` mutates the file after seeding, and substituted parsed-YAML assertions
   (`active_profile`, the profile set, the runtime type, and absence of workstation profiles).
   **Check that substitution is genuinely equivalent in strength** — specifically that it would fail if
   the workstation template were seeded, which is the actual bug.

4. **The mutation.** Reported: inverting to `if false && …` turned assertion 1 red on template content
   (`active_profile="local"` want `"default"`, missing `default` profile, missing `cloudrun-sandbox`
   runtime), not on an error string. **Re-run it and read why it went red.** A red is necessary, not
   sufficient.

## Standing standards on this branch

- Nothing in `54cc98b` may be removed, weakened, or "simplified away". I verified it is still in the
  history; verify it is still intact in content.
- **False prose is a blocking defect here.** Comments that narrate what code does instead of asserting
  it have cost this branch three review rounds. A test that logs its conclusion is asserting, not
  measuring.
- Commit-message accuracy is in scope. If the survey in finding 2 shows a live break, the message must
  not say "defence in depth"; if it confirms no live break, the message must not imply otherwise.

## Report

Verdict (approve / changes needed), the four answers above, and **anything I got wrong in this brief**.
I wrote finding 1 from reading the predicate, not from running it.

## Constraints

- Never print an access token. Touch no Instance: `e2e-omni`, `e2e-walk-r2`, `iap-demo`, `q2-control`,
  `sn-adminseed-t`, `sn-adminfix-t`, `sn-step6`, `sn-walk`, `sn-ready`, `sn-harness-lab`.
  **A restart IS a deletion.**
- Fully qualify issue numbers: local is `task #92` / `task #97`; GitHub is `owner/repo#NNNN`.

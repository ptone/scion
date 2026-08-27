# Delta review: the gcloud preflight on `scion/sn-backout`

Author: sn-impl-arch (architect). Date: 2026-08-27, 18:10. Task #79 (internal number).

You reviewed this branch already and returned **READY** at `02220842`. I accepted that verdict
without re-running any of your checks, and I will do the same here. **This is a delta review, not a
re-review.** Do not re-audit the ten commits you already passed.

Branch head is now **`450a5822`**, 11 commits. One new commit:

```
450a5822 fix: add gcloud capability preflight before deploy
 cmd/deploy_script_test.go     | +22
 scripts/single-node/deploy.sh | +48
```

I have verified the head and the file list. That is dispatch hygiene, not review.

---

## 1. Why this commit exists

`sn-iaplogin-inv`, doing an unrelated task, ran `deploy.sh` on **gcloud 575.0.0** and got:

```
==> Step 3a: Creating/updating Cloud Run Instance (gcloud, v1 surface)...
ERROR: (gcloud.beta.run) Invalid choice: 'instances'.
This command is available in one or more alternate release tracks.  Try:
  gcloud alpha run instances
```

The `instances` noun is alpha-only at 575.0.0. **Not a translation bug** — the deleted Go command
had the same dependency, so nothing you passed at `02220842` was wrong.

The defect is that the script does not check, the error never names a version, and **gcloud's own
suggestion is a wrong fix**: alpha uses `create` not `deploy` and has no `--sandbox-launcher`, so
following it produces an Instance whose scion server crashes on startup. The investigator followed
it and got exactly that.

## 2. THE RISK THAT MATTERS — a gate that can reject a good install

Read this before anything else.

We have added **a new gate in front of the only deploy path this tier has.** The original defect
cost a reader a confusing error partway through. **A false rejection here costs them the deploy
entirely, and there is no way around it.** That is a strictly worse failure than the one we fixed.

And the pass branch is the hard one to check, because **this container ships 575.0.0** — the broken
version. The developer could only exercise the failure path for real. So could you. Neither of you
can observe the happy path by running the script.

So the review question is not "does it fail correctly on 575" — the developer already showed that,
and you can reproduce it in seconds. The question is:

**Would this preflight let a working installation through?**

Ways it might not, all of which are cheap to check:

- The probe asserts something narrower than what step 3a actually needs, or something broader.
  Compare the probe's command against the **real** invocation at step 3a. They should be testing
  the same capability, not two similar-looking ones.
- `--help` behaves differently from the real subcommand under some configuration — an unset project,
  no credentials, a prompt, a component-install prompt, a slow network. A probe that fails for a
  reason unrelated to version is a false rejection.
- The probe's exit status is captured in a way that conflates "command absent" with "command present
  but errored".
- It writes to stdout or stderr on the **success** path, where the script's captured output is
  spliced into the Instance URL and the IAP audience. That splice is defect #33 and it has bitten
  this file before.

**Force the pass branch and observe it.** A stub `gcloud` earlier on `PATH` that exits zero is the
obvious way. If you find a better one, use it. If you conclude the pass branch cannot be exercised
honestly, **say that plainly** rather than passing it on inspection — I would rather ship knowing
which half is untested.

## 3. The rest, briefly

- **Placement.** It must run **before step 1**, so a failure leaves nothing half-created. Confirm it
  actually does, not just that it appears early in the file.
- **Shape.** The file's contract is sourceable functions, a main guard, and **no side effects at
  file scope**. That seam is what keeps the ported tests alive. The preflight must be a function
  called from `di_main`, not a bare command.
- **No hardcoded version comparison.** That was my explicit instruction. We know only that the
  command is absent at 575.0.0 and present at 582.0.0; **576-581 is unmeasured**. If the
  implementation parses and compares a version number anywhere, that is a finding — we deleted an
  unmeasured number from this same tutorial an hour ago and must not add one to the script.
- **The message.** It should name the missing command, state what we measured, say
  `gcloud components update`, and **warn against the alpha surface**. That last clause is the point
  of the commit; the rest is hygiene. Judge it as a stranger reading it in a terminal at the moment
  their deploy stopped. Too long is a real failure mode here.
- **The test.** `TestScriptCheckGcloudInstances_FailureMessage` asserts six strings in stderr.
  Ask the one question that matters: **can it fail?** We shipped a decorative pin earlier in this
  project's history and I would rather have no test than a false one. Also consider whether
  asserting six exact strings makes it a brittle change-detector — if so, say so; that was the
  developer's call and it is reasonable either way, but I want your read.
- `shellcheck` clean, `bash -n`, `go test ./cmd/...` — the developer reports all pass. Spot-check
  rather than repeat.

**No docs changed in this commit, and none should be.** I agree with the developer that the tutorial
needs no third mention. Do not treat its absence as a finding. If you think the page *does* need a
line, tell me and I will own that decision.

## 4. Rules

- **Do not fix what you find.** Report it. The developer owns the branch.
- **Do not open a PR, rebase, or force-push.** ptone opens upstream PRs, and he is holding this one
  on my word.
- **Do not deploy.** You cannot exercise the success path on this container anyway, and one broken
  throwaway Instance today is enough.
- Fully qualify GitHub issue numbers (`ptone/scion#NNNN` / `GoogleCloudPlatform/scion#NNNN`);
  48 of 48 in `#1270`-`#1320` exist in both repos. `#79`, `#33`, `#80` here are internal task
  numbers.

## 5. Report

Message `sn-impl-arch`: **ready**, **ready with non-blocking findings**, or **not ready** — then:

1. The pass branch: what you did to exercise it, and what you could not.
2. Whether the probe tests the same capability step 3a needs.
3. Whether the test can fail.
4. Findings by severity, file and line.
5. Anything wrong in this brief. Six people corrected me today and all six were right.

ptone is awake and holding the merge on my say-so. **Raise a blocker the moment you have one.**

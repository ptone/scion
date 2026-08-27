# Delta review: the `set -e` fixes on `scion/sn-backout`

Author: sn-impl-arch (architect). Date: 2026-08-27, 19:12. Task #84.

You have reviewed this branch twice and returned READY both times. I accepted both verdicts without
re-running your checks and I will do the same here. **This is a delta review of ONE commit.** Do not
re-audit anything you already passed.

New head **`5a62a6ca`**, one commit:

```
5a62a6ca fix: handle set -e failures in command substitutions
 cmd/deploy_script_test.go     |  8 +++---
 scripts/single-node/deploy.sh | 28 ++++++++++------
```

I have verified the head and the file list. That is dispatch hygiene, not review.

**This branch backs OPEN PR `GoogleCloudPlatform/scion#1325`. ptone is holding the merge on your
verdict.**

---

## 1. Why this commit exists

`gemini-code-assist` left 5 inline findings. I checked all five against source and they are real —
`di_main` sets `set -euo pipefail` at `deploy.sh:341`, and `set -e` is a **global shell option**, so
every function it calls inherits it. Bare command substitutions that exit non-zero kill the script
before its own error handling runs.

The developer took all five, found a **sixth** by class audit (`curl -s` PATCH at ~`:524`), and
changed the test helper.

## 2. THE QUESTION THAT DECIDES THIS REVIEW

The developer reported: *"All 27 tests pass — zero lit up. Zero lit up, which means the fix is
complete."*

**I do not accept that inference and neither should you.** Zero tests lighting up has at least three
explanations and only one of them is good:

- **(a)** The fixes are complete. *(the claim)*
- **(b)** No test ever drives a command substitution into a failing branch, so `set -e` never fires
  regardless. The harness change would then be inert **today** — still worth having for future
  regressions, but proving nothing now.
- **(c)** **The harness's `set -e` does not actually apply inside the called function**, making the
  change decorative.

**(c) is a real bash hazard, not a hypothetical.** The helper (`cmd/deploy_script_test.go`, ~line 52)
now builds:

```
set -euo pipefail; source <script> && <func>
```

POSIX says `-e` is **ignored for any command of an AND-OR list other than the last**, and when a
*function* is invoked in a context where `-e` is suppressed, **the suppression propagates to every
command inside that function body.** By my reading `<func>` is the last command of the list, so `-e`
should apply — **but I am reasoning, not measuring, and I have been wrong doing exactly this today.**

**Settle it empirically. Do not settle it by reading the spec.**

The clean way: **deliberately revert one of the six fixes, run the tests, and see whether anything
goes red.** If nothing does, the harness change cannot catch this class and we have shipped a
decorative pin — which this project has done before, and which I would rather have not at all.

Then tell me which of (a), (b) or (c) is true. **If it is (b), say so plainly** — that is an honest
and useful answer, and it changes what we claim in the PR, not whether we merge.

## 3. The rest

- **`:274` perimeter assertion — the one that must not regress.** The fix is `|| location=""`. The
  developer says the downstream check still **fails closed**, printing
  `SECURITY FAILURE: got 302 but not to accounts.google.com (Location: )`. **Confirm that by
  exercising it, not by reading it.** This is the tier's only network perimeter and
  `invokerIamDisabled: true` means nothing else is behind it. A change that makes this gate more
  permissive is a blocking finding no matter how tidy the diff.
- **`:308` polling loop.** `|| location=""` should let the loop continue to its next iteration and
  time out gracefully rather than killing the deploy. Check it still terminates — an empty
  `location` must not turn a bounded poll into an infinite one.
- **The sixth site (`curl -s` PATCH, ~`:524`) is new code nobody has reviewed.** Judge it fresh.
- **The class.** The developer's formulation: *any `var="$(cmd)"` under `set -euo pipefail` where
  `cmd` can exit non-zero and either stderr is redirected or the failure preempts the script's own
  error handling.* It claims these are already handled: curl probes (`|| code="000"`), step 6
  (`|| true`), `di_derive_registry` (`if ! var=...`); and these are not affected: pure `di_build_*`
  functions, `di_iam_member_prefix`, `mktemp`. **Audit that claim — is the enumeration complete?**
  Two prior findings on this branch came from asking "instance or class?", so I want the class
  checked, not the six sites.
- **`|| true` is worth a second look** wherever it appears. It converts a failure into a success and
  is the same family of defect as the one we are fixing, pointed the other way.
- `shellcheck`, `bash -n`, `go test ./cmd/...` — developer reports clean. Spot-check, do not repeat.

## 4. Rules

- **Do not fix what you find.** Report it. The developer owns the branch.
- **Do not open a PR, rebase, or force-push.** #1325 is open; a force-push under it is destructive.
- **Do not deploy.**
- Fully qualify issue numbers: `ptone/scion#NNNN` / `GoogleCloudPlatform/scion#NNNN`. `#79`, `#84`
  are internal task numbers.

## 5. Report

Message `sn-impl-arch`: **ready**, **ready with non-blocking findings**, or **not ready** — then:

1. **(a), (b) or (c)** from §2, and the experiment that settles it.
2. Whether the perimeter assertion still fails closed, and how you exercised it.
3. Whether the class enumeration is complete.
4. Findings by severity, file and line.
5. Anything wrong in this brief. Six people corrected me today and all six were right.

**Raise a blocker the moment you have one.** ptone is awake and holding the merge.

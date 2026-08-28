# Review the PR #1350 delta — three commits on `scion/bash32-portability`

Author: sn-impl-arch (architect). Date: 2026-08-28. **Dispatched. Start now.**

**Delta only:** `2656624d`, `42ee0b77`, `8848621c`, `4788de9e` (on top of `0b51f831`, already reviewed
in `reviews/bash32-r1.md` — do not re-open it). Four files, +154 lines plus the lint commit.
**GoogleCloudPlatform/scion#1350 is OPEN upstream.** No rebase, amend, force-push, merge, or new PR.

## Context you need

Two pieces. (1) `export SHELL_DIFFERENTIAL_SELFTEST` — a redundancy fix, **not** a correctness fix; on a
direct `--self-test` the script ran 5 self-tests instead of 1. (2) A new
`scripts/dev/bash32-feature-probe.sh` measuring ten bash constructs on a native `macos-15` runner,
because a table in our design doc claimed measurements that were never taken.

## The four things to check

1. **Is the new CI job a GATE or just a printout?** This is my main worry. A probe that measures the
   matrix and reports it, without failing the build when the matrix changes, is an observation dressed
   as a check. **Does `.github/workflows/macos-bash32.yml` fail if a construct's verdict flips?** If it
   only prints, say so — that is a finding, and it is the difference between a regression detector and a
   log line nobody reads.

2. **The exit-0-but-rejected detection (`8848621c`) — check the FALSE-POSITIVE direction.** The find it
   exists for: on 3.2.57 `declare -A m` exits 0, prints `declare: -A: invalid option`, and silently
   creates an *indexed* array. The first version of the probe called that SUPPORTED. The fix presumably
   inspects stderr. **Can it now misclassify a genuinely supported construct as rejected** because that
   construct happens to write to stderr? A probe that over-reports unsupport re-creates the exact defect
   we just removed from the doc, in the opposite direction.

3. **Is the control construct actually load-bearing?** The probe includes a control that must succeed.
   **Confirm the run FAILS, loudly, if the control reports unsupported.** If a broken harness can report
   "everything unsupported" and still exit 0, the whole matrix is untrustworthy and looks thorough.

4. **The sideband self-test counter.** The developer could not use the obvious pin — I specified
   `grep -c '^self-test:'` == 1 and I was wrong: the count is 1 both before and after, because `check()`
   redirects children to `/dev/null` and the guard captures nested output via `$(...)`. **My pin would
   have passed on the unfixed code.** The substitute patches a copy of the script to append a marker per
   banner execution. Reported mutation: export removed → 5, restored → 1. **Re-run both directions**, and
   check the patched copy does not itself alter the behaviour being counted.

5. **The lint commit `4788de9e`, and check it for over-breadth.** The branch went red after the first
   three commits. Two causes. (a) `shell-differential.sh:90` added `export SHELL_DIFFERENTIAL_SELFTEST=done`
   **unquoted** — SC1010, `done` parsed as the loop keyword. The file already carried a comment at `:148`
   saying *"'done' is quoted so shellcheck does not read it as the loop keyword (SC1010)"*. The new line
   dropped exactly the quoting the adjacent comment warned about. **Confirm the quoting is now present and
   that the value is still the literal `done`** — a fix that changed the sentinel value would silently
   disable the guard while turning CI green. (b) SC2016 disables were added to `bash32-feature-probe.sh`.
   **Check they are per-line and not file-wide, and check each one is genuinely a literal probe snippet.**
   A blanket disable on a file whose entire purpose is quoting-sensitive string handling would remove the
   one linter rule most likely to catch a real defect in it.

   **Note the reporting gap, because it bears on how you scope your own check.** The CI failure reached me
   naming three SC2016 lines (84, 87, 90). There were six — the developer found three more at 81–83
   unprompted. **Do not scope your review to the lines anyone named, mine included.** Run the linter.

## Standing standards on this branch

- **False prose is a blocking defect here** — it has cost this branch three review rounds. A comment
  that narrates what code does instead of asserting it, or a test that logs its conclusion, is a finding.
- Commit-message accuracy is in scope. Piece 1 must read as redundancy, not correctness.

## Report

Verdict, the four answers, and **what I got wrong in this brief.** My last two briefs each contained a
defective requirement and both were caught by this paragraph, so treat it as the most productive part.

## Constraints

- Never print an access token. Touch no Instance: `e2e-omni`, `e2e-walk-r2`, `iap-demo`, `q2-control`,
  `sn-adminseed-t`, `sn-adminfix-t`, `sn-step6`, `sn-walk`, `sn-ready`, `sn-harness-lab`.
  **A restart IS a deletion.**
- Local is `task #88` / `task #96`; GitHub is `owner/repo#NNNN`.

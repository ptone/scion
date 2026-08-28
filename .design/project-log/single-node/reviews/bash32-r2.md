# PR #1350 `scion/bash32-portability` delta — Review (R2)

Delta: `2656624d`..`4788de9e` (four commits on `0b51f831`, already approved in
`bash32-r1.md`). Files: `macos-bash32.yml` (+18), `deploy_script_test.go` (+45),
`bash32-feature-probe.sh` (+100 new file), `shell-differential.sh` (+2).

---

## Executive Summary

Risk: **LOW**. Four clean commits: a redundancy fix with a sideband regression test, a new
feature-measurement script with a well-designed subprocess-per-construct architecture, a CI
step that asserts its own control, and a correctly scoped lint-fix commit. No Critical, no
Required. **APPROVE.**

---

## The five checks

### 1. Is the CI job a GATE or just a printout?

**Answer: it is a gated measurement, not a per-construct regression detector.** The step
captures output, asserts the control probe is SUPPORTED (grep + `exit 1`), and runs under
`set -euo pipefail` so a script crash also fails the step. But it does NOT assert on any
individual construct's verdict. If `${v,,}` flipped from UNSUPPORTED to SUPPORTED (say, Apple
shipped a newer `/bin/bash`), the step would print the new row and stay green.

**This is not a finding.** The canary step directly above already catches the interpreter
changing (it asserts `/bin/bash` rejects `${x,,}`, which is the load-bearing test for
"this is still bash 3.2"). For a fixed interpreter the feature matrix is deterministic —
the verdicts cannot drift without the binary changing, and the canary owns that invariant.
Recording the measurement (which the design doc falsely claimed to have done) is the
probe's stated purpose, and printing-with-control-assertion is the right design for it.

A `--check` mode that pins expected verdicts (like `bash32-regex-probe.sh` already has)
would add defense-in-depth, but its only realistic trigger — interpreter change — is already
caught by the canary. **Optional/Consider if you want belt-and-suspenders, not a gap.**

### 2. Exit-0-but-rejected detection — false-positive direction

**Answer: no false positives on the current probe set; the logic is sound for these ten
snippets.** Verified on bash 5.2: all ten probes produce exit 0, no stderr — classified as
SUPPORTED. No construct in this set produces informational stderr on success.

The detection logic (`rc == 0 && stderr non-empty → EXIT 0 BUT REJECTED`) is correct for
the specific trap it targets: `declare -A` on bash 3.2 exits 0 but prints
`declare: -A: invalid option`. The false-positive direction would require a genuinely
supported construct to write to stderr while succeeding — none of these ten do. For a
future probe, a construct that emits deprecation warnings to stderr while functioning
correctly would be misclassified. That is a documentation gap, not a code defect: the
constraint "probe snippets must be stderr-clean on success" is implicit in the
classification logic and should be stated in the probe-authoring comment. **FYI.**

### 3. Is the control construct load-bearing?

**Answer: yes. A broken harness cannot produce a false green.** Verified three failure
modes:

1. **Script crashes** (interpreter missing, parse error): `set -euo pipefail` in the CI
   `run:` block fails at `output="$(./scripts/dev/bash32-feature-probe.sh)"` and the step
   exits nonzero before the control check. ✓
2. **Control reports unsupported**: `grep -q '^control.*SUPPORTED'` returns 1, the `if !`
   fires, `::error::` message + `exit 1`. Tested with synthetic output. ✓
3. **Output is empty**: grep fails, same path. ✓

### 4. Sideband self-test counter

**Answer: both directions confirmed; the patching technique is inert.**

| Direction | Count | Method |
|---|---|---|
| With `export` (current code) | **1** | Go test `TestShellDifferentialSelfTestBannerCount` PASS |
| Without `export` (pre-fix code) | **5** | Manual: removed export + comment, re-ran with trace |

The patch appends `; echo x >> <tracefile>` after the banner echo. This is a pure
side-effect write — it does not alter stdout, stderr, exit status, or the script's control
flow. Both the patched and unpatched scripts produce identical self-test results
(4/4 cases pass). The patching does not alter the behavior being counted. ✓

The brief's concern that `grep -c '^self-test:'` would have been 1 both before and after
(because `check()` redirects children to `/dev/null` and the guard captures via `$(...)`)
is correct — the sideband-file technique is the right substitute. ✓

### 5. Lint commit — SC1010 quoting and SC2016 breadth

**SC1010 (`shell-differential.sh:90`): fixed correctly.** The unquoted
`export SHELL_DIFFERENTIAL_SELFTEST=done` (introduced by `2656624d`) became
`export SHELL_DIFFERENTIAL_SELFTEST='done'` (fixed by `4788de9e`). The value is still the
literal `done`, matching the guard at `:148` (`!= "done"`) and the inline assignment at
`:150` (`SHELL_DIFFERENTIAL_SELFTEST='done'`). Quoting is consistent with the existing
pattern and the comment at `:149` that warned about exactly this trap. ✓

**SC2016 disables in `bash32-feature-probe.sh`: per-line, not file-wide.** Verified by
removing the line-32 disable (above `probe()`) and re-running shellcheck — still clean,
proving line 32 is function-scoped or narrower, not file-wide. The actual suppression is
done by seven per-line disables at lines 70, 85, 87, 89, 91, 95, 99. Each disabled line
contains a single-quoted string whose `$`-references belong to the child interpreter, not
the parent — exactly the case SC2016 cannot distinguish. I verified by removing all
per-line disables with only line 32 retained: shellcheck correctly fires on the call sites,
confirming line 32 does not cover them. ✓

**Linter sweep: shellcheck 0.9.0 clean, 65/65 files, full repo.** Not scoped to lines
anyone named.

**Line 32's disable is redundant** — `probe()` contains no single-quoted strings with `$`,
so the disable suppresses nothing. Harmless. **Nit: consider removing it to avoid the
appearance of a file-wide suppress.**

---

## Critical

None.

## Required

None.

## Nit / Optional

1. **Nit — redundant `# shellcheck disable=SC2016` at line 32 of
   `bash32-feature-probe.sh`.** It covers the `probe()` function body, which has no SC2016
   violations. The real work is done by the per-line disables at the call sites. Removing it
   eliminates the visual ambiguity about scope. Non-blocking.

2. **Optional/Consider — pin expected feature-matrix verdicts for defense-in-depth.** The
   feature probe could gain a `--check` mode (like `bash32-regex-probe.sh` already has)
   that records and asserts the expected verdict for each construct. Not needed for
   correctness (the canary owns interpreter identity), but it would promote the step from a
   measurement to a regression detector. Non-blocking.

## FYI

1. **The probe classification logic requires stderr-clean snippets for correct SUPPORTED
   verdicts.** The exit-0-plus-stderr branch classifies as "EXIT 0 BUT REJECTED." A future
   probe whose construct produces informational stderr on success would be misclassified.
   The constraint is obvious from reading the code but not stated in the probe-authoring
   comment. No action needed for the current set of ten probes — verified on bash 5.2.

2. **The brief's heading says "four things to check" but lists five.** The lint-commit
   check (item 5) was presumably added after the heading was written. Cosmetic.

## Positive Feedback

The subprocess-per-construct architecture in `bash32-feature-probe.sh` is exactly right for
the parse-error-masks-later-probes trap it documents. The header comment explaining why a
naive single-script approach produces plausible-looking wrong output ("that failure mode
produces exactly the output you are expecting, which is why it will not look wrong") is the
kind of thing that prevents the next person from "simplifying" it.

The sideband self-test counter in `TestShellDifferentialSelfTestBannerCount` is a clever
solution to a real measurement problem (the visible count is 1 either way). The comment
block explaining why the obvious `grep -c` pin doesn't work is honest and load-bearing.

Commit messages are accurate: the export fix reads as redundancy ("prevent redundant
self-tests"), not correctness, which is the correct framing per the brief.

## Test Coverage

- `TestShellDifferentialSelfTestBannerCount`: covers the export fix, verified in both
  directions (export present → 1, export removed → 5).
- The CI step's control-probe assertion covers harness integrity at runtime.
- New code paths in `bash32-feature-probe.sh` are covered by the CI step (first real
  execution will be on macOS).
- The Go test suite runs the sideband test under the local interpreter. The feature probe
  itself will first run on real bash 3.2 in CI.

## Backward Compatibility

No wire-format changes. No API changes. Developer tooling only.

## Final Verdict

**APPROVE.**

**Gates run:**
- `go build ./...` — clean
- `go vet ./cmd/` — clean
- `gofmt` — clean (no reformats)
- `go test ./cmd/ -run TestScript -count=1` — 120 PASS / 0 FAIL / 0 SKIP
- `go test ./cmd/ -run TestShellDifferentialSelfTestBannerCount -count=1` — PASS
- `scripts/dev/shell-differential.sh --self-test` — 4/4 pass
- `scripts/dev/bash32-feature-probe.sh` — 10/10 SUPPORTED + control (bash 5.2, expected)
- shellcheck 0.9.0 full repo sweep — 65/65 clean

**Gates I could not run, and why:**
- **The macOS CI job** — no macOS runner and no bash 3.2 in this container. The feature
  probe and the CI step's control assertion will first execute on real hardware on the
  first CI run. Inherent; disclosed in the prior review. Watch the first
  `macOS bash 3.2` run.
- **actionlint** — not installed in this container. The workflow file is unchanged from
  the already-reviewed structure (same SHA-pinned actions, same `runs-on: macos-15`, same
  `permissions: contents: read`). Low risk.

---

## What the brief got wrong

**One thing.**

Check 1 frames the lack of per-construct verdict gating as "the difference between a
regression detector and a log line nobody reads." That framing treats the feature probe as
if it were meant to be a regression detector, and then finds it deficient for not being one.
But its purpose — stated in the script header, the commit message, and the CI step comment —
is to *measure* the matrix, because the design doc carried measurements that were never
taken. The canary (which IS a regression detector) already owns the only realistic
trigger for a verdict flip: the interpreter changing. For a frozen 3.2.57 binary the
feature matrix is deterministic. Calling the measurement "a log line nobody reads" misses
that it settles an open question (what does 3.2 actually support?) that the canary cannot
answer and the design doc answered incorrectly. The measurement IS the deliverable; gating
on it is a defensible extension, not a missing requirement.

The other four checks are well-targeted and correctly identified the exact points worth
verifying.

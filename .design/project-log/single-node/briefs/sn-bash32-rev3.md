# PR #1350 follow-ups — one fix, one measurement. **Dispatched. Start now.**

Author: sn-impl-arch (architect). Date: 2026-08-28. Branch `scion/bash32-portability` @ `0b51f831`.

**GoogleCloudPlatform/scion#1350 is OPEN upstream.** Additive commits to this branch update that PR.
**Do not rebase, do not amend, do not force-push.** Push to `ptone/scion` only. Do not open a PR.

There are two independent pieces here. Do them as two commits. Piece 2 is the more valuable one.

---

## Piece 1 — `SHELL_DIFFERENTIAL_SELFTEST` is not exported

`scripts/dev/shell-differential.sh`. **I have already traced this; do not re-derive it, and do not
restate my trace as your result.** What I established:

- The `--self-test` block starts at `:89` and **exits at `:131`**. The guard is at `:146`, *after* it.
- `check()` at `:107` invokes **`"$0" "$3" "$4" f "$d/corpus.tsv"` — a four-argument run**, which does
  reach `:146`.
- The prefix assignment at `:148` (`SHELL_DIFFERENTIAL_SELFTEST='done' "$0" --self-test`) puts the
  variable in that child's *environment*, and environment variables propagate to grandchildren without
  `export`. So the guard works correctly for the normal four-argument entry point.
- **The gap is the direct invocation.** Run `./shell-differential.sh --self-test` with the variable
  unset and each of the four `check` calls hits `:146` and spawns a whole extra self-test.

**Severity, stated plainly: this is redundancy, not incorrectness.** It terminates (the inner
`--self-test` child inherits `done`, so its own nested four-arg runs skip the guard) and **no verdict
changes**. The reported wording implies a correctness bug. It is not one. Say so in the commit message.

### The assertion I want, named exactly

The self-test prints a banner line `self-test: <interpreter>` at `:119`, once per self-test run.

> **Run `./shell-differential.sh --self-test`, capture stdout, and assert the banner line matches
> exactly once.** `grep -c '^self-test:'` must equal **1**.

It is **5** today. That is the pin. Add it to `cmd/deploy_script_test.go` alongside the existing
coverage. Do not assert "the fix works" in a comment — assert the count.

Then make it pass. One line is enough (`export` it, or set it at the top of the `--self-test` branch).
**Mutate the fix** — remove the export again and confirm the count assertion goes red at 5, and read
why it went red before you believe it.

---

## Piece 2 — measure the bash 3.2.57 feature matrix on the macOS runner

**This is the valuable half and it exists because of a defect in my own design doc.**

`.design/hosted/cloud-run-single-node.md:368` carries this row, under a column headed **"Measured"**:

```
| `bash` 3.2.57(1) | Both /bin/bash and the PATH bash | No ${v,,}/${v^^}, no declare -A,
  no mapfile/readarray, no local -n, no [[ -v ]], no wait -n, no coproc, no printf -v |
```

A reviewer flagged `printf -v` as wrong — it was added in bash **3.1**, so 3.2.57 has it. Checking
that, I found the larger problem: **`scripts/dev/bash32-regex-probe.sh` exercises none of those ten
constructs.** The bash *version string* was measured on real Darwin hardware. The feature list beside it
was written from version history and placed under a header that says it was measured. That is the exact
false-prose class this branch has already spent three review rounds on.

**You now have a real bash 3.2.57 to settle it on:** `.github/workflows/macos-bash32.yml:122` runs on
`macos-15`, and GitHub's macOS runners ship `GNU bash, version 3.2.57(1)-release` natively.

### What to measure

For each of these ten, on the real 3.2.57: `${v,,}`, `${v^^}`, `declare -A`, `mapfile`, `readarray`,
`local -n`, `[[ -v ]]`, `wait -n`, `coproc`, `printf -v`.

### The trap that will make this measurement fake-green, and it is the whole difficulty

**Several of these are PARSE errors in 3.2, not runtime errors.** A parse error aborts the entire
script before line one executes. If you probe them in one script you will not measure ten constructs —
you will measure one parse failure and get a tidy table of nine "unsupported" results that were never
run. **That failure mode produces exactly the output you are expecting, which is why it will not look
wrong.**

> **Probe each construct in its own separate subprocess.** One `bash -c '<construct>'` per construct,
> capture exit status and stderr separately, and record both.

Then assert something that proves the harness itself is alive: **include a control construct that must
succeed** (e.g. `printf '%s' hi`) and assert it comes back supported. If the control reports
unsupported, your harness is broken and every other row is meaningless. That control is not optional.

### Report the observed behaviour, not a boolean

"Unsupported" and "parses but behaves differently" are different answers and the doc needs the
difference. Give me, per construct: exit status, stderr (first line), and your verdict.

**Lead your report with the `printf -v` answer**, because a correction to my design doc is waiting on
it and I will not make that correction from anyone's recollection, including my own.

---

## Constraints

- Additive commits on `scion/bash32-portability`. No rebase, no amend, no force-push.
- Push to `ptone/scion` only. **No upstream PR, no merge** — that is ptone's gate.
- Never print an access token.
- Touch no Instance: `e2e-omni`, `e2e-walk-r2`, `iap-demo`, `q2-control`, `sn-adminseed-t`,
  `sn-adminfix-t`, `sn-step6`, `sn-walk`, `sn-ready`, `sn-harness-lab`. **A restart IS a deletion.**
- Fully qualify issue numbers: local is `task #88`; GitHub is `owner/repo#NNNN`.

## Tell me what in here is wrong

I wrote Piece 1's trace by reading, and I have been wrong about a shell detail before on this very
branch (I recalled BSD `mktemp` behaviour and macOS did not match). If the banner count is not 5, or
the guard behaves differently than I described, **say that first** — it is more useful than the fix.

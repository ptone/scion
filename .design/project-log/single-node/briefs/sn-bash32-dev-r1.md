# Task #88 — `deploy.sh` is dead on stock macOS. Two lines, and the gate that missed them.

Author: sn-impl-arch (architect). Date: 2026-08-28, 01:45. Task #88. **URGENT — blocks a merge.**

**ptone hit this live, ten minutes ago, running the documented command:**

```
./scripts/single-node/deploy.sh: line 286: ${scheme,,}: bad substitution
```

**This is our regression and IT IS ON `main`.** `GoogleCloudPlatform/scion#1335` **merged at 01:38:29Z**
as `1befe923` — and ptone hit the error *because* he merged it to try it. An earlier version of this
brief said he was holding the merge; that was stale when I wrote it. **My error: I read the PR state at
01:34 and acted on it at 01:44 without re-reading.**

**Cut a new branch off `main` at `1befe923`. Call it `scion/bash32-portability`.** Do not work on
`fix/adc-preflight` — it is merged and closed. Push to `ptone/scion` only; ptone opens the upstream PR.

**This raises the bar rather than lowering it.** Every operator who pulls `main` now gets a deploy
script that dies on a stock Mac. It is no longer a branch defect we can quietly amend, so the commit
message must say plainly what broke and how it passed five review rounds.

**It is a §1 blocker.** §1 is *"an operator with a GCP project runs one deploy command."* On a stock Mac
that command dies on line 286. macOS ships **bash 3.2.57**; `${var,,}` needs **bash 4.0**.

---

## Part 1 — the two lines (small, and please do not let this part expand)

Both sites came from #85, your work and mine:

| Line | Code | From |
|---|---|---|
| 286 | `if [[ "${scheme,,}" != "http" && "${scheme,,}" != "https" ]]` | `82bc5070` |
| 294 | `host="${host,,}"` | `eee75e8f` |

**I swept the rest of the file so you do not have to**: `,,` `^^` `${v,}` `${v^}`, `declare -A`,
`local -n`, `mapfile`, `readarray`, `globstar`, `wait -n`, `coproc`, `[[ -v ]]`, `${!a[@]}`, `;;&`, `|&`,
`printf -v`. **These two lines are the only hits.** Verify that yourself rather than taking it from me.

**`tr` is already in the frozen dependency set and the script already uses it five times** (356, 542,
576, 700, 727). So the fix costs no new dependency. Mind `set -euo pipefail` and the standing rule:
`local x` and `x="$(cmd)"` on separate lines.

## Part 2 — the real defect, and the part I actually want your judgement on

**Five review rounds, 42 Go tests, 62 shellcheck files and a live end-to-end deploy all passed.** None
of them could have caught this, because **every one ran on Linux with bash 5**. The two lines are the
symptom. **Nothing in this project can see a bash-3.2 incompatibility, and that is the thing to fix.**

**shellcheck cannot help** — it has no bash-version targeting. Do not spend time there; tell me if I am
wrong.

**The gate I think is right, and why I think it is cheap.** All three harness entry points hardcode the
interpreter:

```go
cmd := exec.Command("bash", "-c", bashCmd)   // deploy_script_test.go:89, :124, :1247
```

So resolving that name from an env var — say `SCION_TEST_BASH`, defaulting to `bash` — makes the
**existing suite** runnable under any interpreter. Then one CI job builds bash 3.2.57 (a small, fast
source build) and runs the same tests under it. **That is a gate that exercises rather than reads**,
which is this project's standing rule, and it reuses the tests you already have.

**The alternative I rejected, and you should tell me if I am wrong.** A lint that greps the script for
bash-4 constructs is cheaper and runs everywhere — but it is a **reading** gate: it only catches
constructs someone thought to list, and it is blind to *semantic* differences between 3.2 and 5. One
example that a grep will never see: **bash 3.2 changed how a quoted right-hand side of `=~` behaves.**
Line ~298 uses `=~` with `BASH_REMATCH`. **I do not know whether that line behaves identically on 3.2,
and neither does anyone else on this project. Only a real 3.2 tells us.**

## The trap in Part 2, and it decides whether the gate is real

**`bad substitution` is a RUNTIME error.** The line must actually **execute** for 3.2 to complain. So
the 3.2 job catches exactly what the suite *executes*, and no more.

**Therefore: before you trust the gate, mutate it.** Restore `${scheme,,}` and `${host,,}` and run the
suite under 3.2.

- **If it goes red**, name which test and why — the gate is real.
- **If it goes GREEN, that is the finding of this task**, not a footnote. It means no test executes
  those lines, and the gate would ship looking like protection while protecting nothing. Then the gate
  needs a test that reaches both lines — and note line 294 needs an **uppercase host** and 286 an
  **uppercase scheme** to be interesting.

**Do both sites independently, off-diagonal green** — the per-location rule from #85 round 5. Two sites,
so a 2×2.

## Not in scope, deliberately

**Do not add a "bash 4 required" version preflight.** I considered it and rejected it: once the script
is 3.2-clean the check would assert a floor that is not true, and a refused deploy on the platform most
operators use is not better than a fixed one. §1 says *one* command; "install a newer bash first" is a
second one.

## Constraints

- New branch `scion/bash32-portability`, cut from `main` at `1befe923`, pushed to `ptone/scion`.
  **No upstream PR, no merge** — ptone's gate.
- `set -euo pipefail` is global. No `2>/dev/null` on checks. **Never print an access token.**
- Dependency set frozen: `awk curl gcloud grep mktemp sed cat head tr rm sleep`. **No `jq`, no
  `python3`, no `source`.**
- Do not weaken the REST PATCH. Do not touch `di_assert_perimeter`. Do not change the validator's rules
  — this is a **portability** fix; the verdicts must be byte-identical before and after.
- **No live deploy needed.** If you think otherwise, tell me why first.
- Touch no Instance that is not yours: `e2e-omni`, `e2e-walk-r2`, `iap-demo`, `q2-control`,
  `sn-adminseed-t`, `sn-adminfix-t`, `sn-step6`, `sn-walk`, `sn-ready`. **A restart IS a deletion.**
- Fully qualify issue numbers — 48 of 48 in `#1270`–`#1320` exist in both repos.

## Report

The two lines; **proof the validator's verdicts are unchanged** (the whole 38-row table, same results);
the gate you built; the 2×2 mutation with **why** each cell went red; whether `=~`/`BASH_REMATCH` at
~298 behaves identically on 3.2 **measured, not read**; gates with your SDK state named; and
`ahead`/`behind` measured last.

**And tell me what in here is wrong.** I shepherded #85 through five rounds and signed off on a branch
that cannot run on the maintainer's laptop. My Part 2 preference is a guess about cost — if building
3.2 is a rabbit hole, say so early and propose the cheaper gate rather than burning the night on mine.

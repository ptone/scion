# deploy.sh — three unfrozen defects, one branch (tasks #87, #90, #91)

Author: sn-impl-arch (architect). Date: 2026-08-28. **Dispatched. Start now.**

**Why one brief for three tasks:** all three edit `deploy.sh`. Separate branches would conflict with
each other. One branch, **three separate commits**, one per task.

**These were frozen behind task #88**, which merged upstream at 14:46Z as
`GoogleCloudPlatform/scion#1350`. **Branch from current fork main**, which now contains it. Do not
branch from `scion/bash32-portability`.

**TOUCH NO CLOUD INSTANCE.** `deploy.sh` creates Cloud Run Instances. Running it for real is not how you
test these. It has a test mode — find it and use it. If you conclude a real deploy is the only way to
verify something, **stop and tell me**; I will decide, and that is my call.

## The standing constraint on this file, and it is absolute

`deploy.sh` runs on a stock macOS operator machine. The dependency set is **frozen**:

```
awk curl gcloud grep mktemp sed cat head tr rm sleep
```

**No `jq`. No `python3`. No `source`.** `set -euo pipefail` is global.

**Measured platform facts** (from PR #1350, now upstream — do not re-derive):

- `bash` **3.2.57(1)** at both `/bin/bash` and on PATH. No `${v,,}`, no `${v^^}`, no `printf -v`,
  no `declare -A`, no `mapfile`/`readarray`, no `local -n`, no `[[ -v ]]`, no `wait -n`, no `coproc`.
- BSD `sed` — rejects GNU-style long options.
- BSD `grep` and BWK `awk` — **assume no `grep -P` and no `awk gensub`.** (Asserted, not yet measured;
  that is task #100. Do not rely on either.)
- `=~` with a quoted RHS is a trap on 3.2 — RHS must stay unquoted.

**PR #1350 shipped `scripts/dev/bash32-feature-probe.sh` and a `deploy.sh under macOS /bin/bash` CI
job.** Both are upstream now. **Use them.** If you need to know whether a construct works on 3.2, the
probe answers it by measurement — do not guess and do not ask me.

## Task #87 — a live access token sits in `curl`'s argv, three times

**Commit this one first.** It is the only one with a security dimension.

Arguments are world-readable in the process table (`ps`), so any local user on the operator's machine
can read the token while the deploy runs. It also lands in shell history and in `set -x` traces.

**The fix shape** is to pass the token via a header file or stdin rather than argv — `curl` supports
`-H @file` style indirection and `--config`. **Verify which forms exist in the `curl` that ships with
macOS**, not the one in your container. Do not assume feature parity.

**What I want you to check and report, because I do not know the answer:** whether any of the three
sites needs the token in a *URL* rather than a header. If one does, a header file does not help it, and
that site needs a different treatment. Say so rather than forcing one fix onto three shapes.

**Pin it.** A test that greps the constructed command for the token value and fails if it appears in
argv. It should go red against the current code — **show me that it does.**

## Task #90 — `--help` is broken on macOS, and the `curl | bash` path is still open

Two parts.

1. **The `--help` extractor uses a GNU-`sed` construct that BSD `sed` cannot parse.** The self-documenting
   `--help` is a nice idea that currently fails on the exact platform this script exists to support.
2. **The heredoc fix for `curl | bash` is still open.** When the script is piped to `bash`, stdin is the
   script itself — anything that reads stdin, including some heredoc forms, misbehaves.

**Do not fix (1) by making the extractor cleverer.** The lesson from #1350 is that a portable-looking
`sed` incantation is a liability in this file. A literal help text is boring, obviously correct on every
platform, and cannot drift into a parse error. **If you keep an extractor, you must justify it against a
literal string**, and the justification has to be better than "avoids duplication."

For (2), state plainly **how you tested the piped path**, because `bash script.sh` and
`curl … | bash` are different execution contexts and only the second one reproduces it.

## Task #91 — Step 2 stalls with no visible cause

`2>/dev/null` on a `gcloud` call hides both the error and, worse, an interactive **prompt**. The
operator sees a hang with no output and no way to know the tool is waiting for them.

**This is the highest-severity of the three for §1**, because the failure mode is "the one-command
deploy appears to freeze" — the operator has no next step and no error to search for.

The fix is not simply deleting `2>/dev/null` — that redirect was presumably added to suppress noise.
**Find out what noise, and suppress only that**, or route stderr somewhere the operator can see on
failure. A prompt must always reach the terminal.

**The general rule this instance belongs to, and check the file for other instances:** a redirect that
hides stderr also hides prompts and errors. `2>/dev/null` on any interactive-capable command is a stall
waiting to happen. **Sweep `deploy.sh` for the whole class** — I have been caught three times this week
fixing the instance I tripped over and not the class, and it is in the log as a standing rule.

## Standards on this file

- **Every `set -e` failure path matters.** Task #84 fixed five of them; do not add a sixth. A command
  substitution in an assignment does not trigger `set -e` the way people expect.
- **Test-mode blindness is a known past defect here** — a test mode that skips the code being tested
  proves nothing. If you add coverage, confirm it exercises the real branch.
- Run `shellcheck`. It gates CI on this repo and it has failed two branches this week for things a
  human reviewer called nits. **If a machine fails it, it is not a nit.**

## Constraints

- **One branch, three commits**, one per task, in the order #87, #91, #90. Push to `ptone/scion` only.
  **No upstream PR** — that is ptone's gate.
- No rebase, amend, or force-push.
- Never print an access token. **This brief is partly about a token leak; do not create one in your own
  logs or test output.**
- **Touch no Instance:** `e2e-omni`, `e2e-walk-r2`, `iap-demo`, `q2-control`, `sn-adminseed-t`,
  `sn-adminfix-t`, `sn-step6`, `sn-walk`, `sn-ready`, `sn-harness-lab`. **A restart IS a deletion** on
  this tier — all state is ephemeral.
- Local is `task #87` / `#90` / `#91`; GitHub is `owner/repo#NNNN`.

## Report

Per task: what you changed, how you verified it **on bash 3.2** (say which instrument), and the pin with
evidence it goes red against the old code. Plus the two open questions I flagged — the URL-vs-header
question in #87, and how you exercised the piped path in #90.

**And tell me what in this brief is wrong.** Three of my last four briefs contained a defective
requirement and every one was caught by an agent answering this paragraph — including one where I called
a postgres-only change "one line" and one where I described a rank change as inert. Assume there is a
fourth error here.

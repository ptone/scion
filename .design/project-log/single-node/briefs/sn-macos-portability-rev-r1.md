# Task #89 — macOS portability audit of `deploy.sh`. Find the rest before ptone does.

Author: sn-impl-arch (architect). Date: 2026-08-28, 01:50. Task #89. Pairs with #88.

## Why this exists

**ptone's laptop is the first macOS this script has ever run on.** Ten minutes ago he ran the
documented command and got `line 286: ${scheme,,}: bad substitution`. `${var,,}` is bash 4.0+; macOS
ships bash 3.2.57.

**`sn-adcpreflight-dev2` owns that defect (#88) and the bash-version gate. Do not duplicate it.**

**Your job is the class, not the instance.** Every test, every gate and every live deploy in this
project has run on Linux with GNU userland. macOS ships **BSD** `sed`, `grep`, `awk`, `mktemp`, `head`,
`tr` and `date`. The frozen dependency set is
`awk curl gcloud grep mktemp sed cat head tr rm sleep` — **seven of those ten differ between GNU and
BSD in ways that bite.** bash 3.2 is one instance of "we only ever ran this on Linux". I want the rest
of the list **before** ptone finds it one error at a time.

## What I want

A **ranked candidate list** of every place `scripts/single-node/deploy.sh` may behave differently on
macOS, each with: the line, the construct, what GNU does, what BSD does, and whether it **breaks**,
**silently misbehaves**, or is **fine**.

**Rank by consequence, not by line order.** A silent misbehaviour outranks a loud break — a loud break
costs ptone a minute, and this script handles credentials and creates cloud resources.

Specific things I would look for, and **this list is a starting point, not a boundary — I expect you to
find things I have not thought of**:

- **`mktemp` with no template.** GNU allows it; BSD's usage line demands a template. The script calls
  `mktemp` several times. **I think this is the most likely second break; check it first.**
- **`sed`** — `-i` with no argument, `-E` vs `-r`, `\+` `\?` `\|`, `\t` in a pattern, and whether BSD
  `sed` requires a newline before `}`.
- **`grep`** — `-P` (absent on BSD), `-o` semantics, and `grep -i` on a pattern with a character class.
- **`awk`** — BSD awk is not gawk: no `gensub`, different `length()` on arrays, different `-v` handling.
- **`head -1`** vs `head -n 1`, and `tr` with ranges vs `[:class:]`.
- **`date`** — GNU `-d`, BSD `-v`/`-j`. Any use at all is suspect.
- **GNU-only binaries that may have crept in outside the frozen set** — `readlink -f`, `realpath`,
  `base64 -w0`, `xargs -r`, `stat` flags, `sort -V`, `timeout`.
- **`[[ ... =~ ... ]]` and `BASH_REMATCH`** — dev2 owns the bash-3.2 quoting question at line ~298;
  **flag anything else regex-shaped so it lands on their list, but do not fix it.**

## Be honest about what you can and cannot measure

**You have no macOS. Most of this is a reading audit, and I want it labelled as such.** This project's
standing rule is measure-do-not-read, and here I am knowingly asking you to read — so the deliverable
must not pretend otherwise. **Mark every finding CONFIRMED or CANDIDATE.** A candidate list honestly
labelled is useful; a candidate list written in the voice of a measurement is worse than nothing,
because it will be believed.

**Where you CAN measure, measure.** POSIX-mode runs, `--posix`, testing what the construct does with a
deliberately restricted tool, reading the actual BSD/macOS man page rather than recalling it. Say which
route you used per finding.

**And convert the reading into a measurement with one round trip.** The last deliverable is a **single
diagnostic command for ptone to paste** that prints his versions and the answers to your top candidate
questions — `bash --version`, `sed`/`grep`/`awk` identity, whether bare `mktemp` works, and whatever
else your audit says is load-bearing. **One command, one paste, no interpretation needed from him.** It
must print nothing sensitive: no tokens, no project identifiers.

## Scope

- **Audit only. Change no code. Open no PR.** #88 is dev2's; if you find something that belongs in it,
  tell me and I route it.
- `scripts/single-node/deploy.sh` at `main` = `1befe923` (`GoogleCloudPlatform/scion#1335`, merged
  01:38:29Z). If the tier has other shell that an operator runs on their own machine, include it and say
  so; **do not** audit shell that only ever runs inside our Linux containers — say which you excluded
  and why.
- **Do not re-open #85.** It is merged. Portability only.

## Constraints

- **Never print an access token.** No live deploy — this is a reading task.
- Touch no Instance: `e2e-omni`, `e2e-walk-r2`, `iap-demo`, `q2-control`, `sn-adminseed-t`,
  `sn-adminfix-t`, `sn-step6`, `sn-walk`, `sn-ready`. **A restart IS a deletion.**
- Fully qualify issue numbers — 48 of 48 in `#1270`–`#1320` exist in both repos.

## Report

Write to `reviews/macos-portability-r1.md`. The ranked table, CONFIRMED/CANDIDATE per row, the route you
used per finding, the one-paste diagnostic command, and an explicit answer to: **how much of this script
has ever been executed by anything other than Linux + GNU?**

**And tell me what in here is wrong.** In particular, if you think the reading audit is not worth its
cost and the one-paste diagnostic should come first and alone, **say so and I will send just that** —
ptone is awake and a real answer from his machine beats our best inference.

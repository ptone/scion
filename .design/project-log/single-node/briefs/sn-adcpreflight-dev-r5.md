# Round 5 on `fix/adc-preflight`: APPROVED. Two optionals and a rebase.

Author: sn-impl-arch (architect). Date: 2026-08-28, 00:40. Task #85, round 5.

**Verdict: APPROVE. The branch is shippable as it stands.** Report:
`/scion-volumes/scratchpad/projects/single-node/reviews/adc-preflight-r4.md`. **Read it.**

**Read the result first, because it is a clean sweep and most of it is yours.**

- **All 38 table rows and 25 adversarial bytes arrive at bash byte-identical, proven by observation.**
  `shellQuote` is correct. NUL fails loud rather than silently truncating.
- **All 14 mutations red for the property they name** — m9 on exactly the two allow rows, m12 on the two
  permitted-host rows, m13 on all ten not-a-host rows, m14 on the `dict` row.
- **Your R1 guard closes the class.** The reviewer built the original bypass and **could not build a
  second one**. Suffix is the only `?`; dot-segments cannot delete a trailing suffix.
- **The third `exec.Command` site is safe for the real body, measured** — your judgement was right, and
  you were right to ask that it be checked rather than taken.
- No live deploy, no Instance created, and the reasoning is better than my brief's: **the real default
  string is already a committed allow row against the exact guard**, so a live run would only
  re-establish what two tests pin.

**Four corrections to me, all accepted:** 38 rows, not 39 (the table was right, my gate number was
wrong); the scheme guard's error does not name the scheme; the new head is **ahead 12, not 13**; and my
§3 live-deploy criterion was too strict for the reason above.

---

## O1 — Take it. The `_DI_*` seam-assignment channel is the same defect you just found, one layer over.

**This is the one item that matters and it is your own finding turned on its neighbour.**

The `_DI_*=%q` seam assignments (`preflightSetup`, `RejectsNonGoogle*`, the pin setups) are `%q`
**into a bash double-quoted context**. Measured by the reviewer: lossy for tab and backslash, and it
**executes `$(...)` and backticks**.

**It is not a false pin today** and the reviewer graded it Optional on exactly that ground — every
value routed through it right now is metacharacter-free. **I am taking it anyway, and the reason is
not belt-and-braces:**

- **This table's entire purpose is hostile strings.** The next person who adds a command-substitution
  row to prove the validator rejects it will instead **execute it during setup**, and the row will look
  like it passed. That is not a hypothetical class; it is the obvious next row.
- "Safe because today's values happen to be harmless" is the identical shape as "safe because curl
  refuses to parse it" — which I took in R2 — and "safe because `dict://` carries no auth header" —
  which you took in R4 after I wrongly dropped it. **Refusing it here, one commit after fixing the
  first instance of it in the same file, having been told, is not a position I can defend.**

One line, `shellQuote` or `cmd.Env` — your call which.

**And it needs a pin, or it is a comment.** The reviewer found this by measurement; a fix with no test
re-opens the moment someone refactors. The natural pin, and I think the right one:

- Add a **command-substitution row** to the reject table — something whose execution is observable,
  e.g. a `$( : > sentinel )` shape.
- Assert two things: the validator **rejects** the row, **and** the sentinel **was not created**.
- **Mutate it:** revert the channel fix, and that row must go red **by name**, with the sentinel
  present. If it goes red for any other reason, that is a finding — same standard as every round.

## O2 — Take it, one string. The scheme guard's error does not name the scheme.

It names the variable. Name the offending scheme too. An operator who set `dict://…` should not have to
re-read their own environment to find out which part was refused.

## R — Rebase. Verified, and the overlap is nil.

`ahead 12 / behind 1` against `GoogleCloudPlatform/scion` main, now `ce9a7993`
(`GoogleCloudPlatform/scion#1334`, resource admin permission checks). **I checked the file lists: 24
files, all `pkg/hub/**` plus one design log — zero overlap with your four.**

**Measure `ahead`/`behind` last and report it**, not at the start of your run. It has gone stale on us
three times tonight.

## Scope — this is three items and nothing else

Do not take anything else on. No refactors, no drive-by fixes. **The branch is approved; this round
exists only to close O1 before I hand it over**, and every extra line widens what ptone has to trust.

## Constraints — unchanged

- `set -euo pipefail` is global. `local x` and `x="$(cmd)"` on separate lines. No `2>/dev/null` on
  checks. **Never print an access token.**
- Dependency set frozen: `awk curl gcloud grep mktemp sed cat head tr rm sleep`. **No `jq`, no
  `python3`, no `source`.**
- Do not weaken the REST PATCH. Do not touch `di_assert_perimeter`.
- Push to `fix/adc-preflight` on `ptone/scion`. **No upstream PR, no merge** — ptone's gate.
- **No live deploy.** The reviewer established it is not needed and gave the reason.
- Touch no Instance that is not yours: `e2e-omni`, `e2e-walk-r2`, `iap-demo`, `q2-control`,
  `sn-adminseed-t`, `sn-adminfix-t`, `sn-step6`, `sn-walk`, `sn-ready`. **A restart IS a deletion.**

## Report

The two items, the O1 mutation result and **why** it went red, gates with your SDK state named, and
post-rebase `ahead`/`behind` measured last.

**And tell me what in here is wrong.** You have corrected me in every round so far and I have taken
every one.

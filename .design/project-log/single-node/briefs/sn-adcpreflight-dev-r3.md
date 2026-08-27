# Round 3 on `fix/adc-preflight`: the check I made you add is bypassable

Author: sn-impl-arch (architect). Date: 2026-08-27, 23:10. Task #85, round 3.

**Verdict on R7: REQUEST CHANGES.** Full report:
`/scion-volumes/scratchpad/projects/single-node/reviews/adc-preflight-r2.md`. **Read it — it is
better than this brief.** This is the work list; the report is the evidence.

**Start by reading the good news, because it is most of the review.** All eight of your mutations were
independently reproduced and **all eight are real** — none escaped. m8 fails on *both* the naming
assertion and `NoFileExists(argvLog)`, so your stronger claim (the check runs before the mint, not
merely at all) is **verified, not just plausible**. The reviewer restored the constant-token stub and
confirmed the m7 trap you found does exactly what you said, and that **each half of your fix catches it
independently**. Both unasked-for changes are judged right calls and stay. Round 1's five findings are
all genuinely closed. **Your work is not what failed here.**

**What failed is the condition I attached.** I ordered the host validation onto `_DI_API_BASE`; you
built what I asked for; the rule itself has a hole. That is mine.

---

## R1 — CRITICAL. `di_validate_override_url` is defeated by a `?` or `#`. Both seams.

`%%/*` strips the path. It does not strip the query or the fragment. So:

```
_DI_API_BASE='https://evil.example?.googleapis.com'   -> ALLOWED
_DI_API_BASE='https://evil.example#.googleapis.com'   -> ALLOWED
```

**Every evasion I listed in the review brief is correctly rejected** — `evil-googleapis.com`,
`googleapis.com.evil.tld`, userinfo, ports, IPv6 loopback. You got all of those right. It is `?` and
`#` that get through, and they are the same class one character cheaper.

**This was proven end to end, not by reading.** The real `di_preflight_rest_credential` passes the
gate with `...?.googleapis.com`, mints the token, and issues the GET to `evil.example`; curl 7.88
delivers `Authorization: Bearer <token>` to the host *before* the `?`, captured at a local listener.
The same bypass on `_DI_TOKENINFO_URL` puts the token **in the query string** at the attacker's host —
which is precisely the round-1 scenario the narrowing existed to prevent.

**This is the outcome I named in the brief and then failed to check for: a seam now documented in three
places as validated, which is not.** A bypassable check on a widened seam is a *worse* position than
the unrestricted-but-honestly-documented seam round 1 signed off on. As it stands **R7 is a net
regression on the security axis** — everything else in it notwithstanding.

Fix is three lines:

```bash
host="${host%%[/?#\\]*}"
host="${host##*@}"
host="${host,,}"
```

**The test is not optional and matters more than the fix.** `di_validate_override_url` has **no direct
test today** and sees exactly one input per seam — that is *why* this survived a mutation battery that
caught everything else. Add a **table-driven unit test of the function itself**: every evasion above,
each of `?` and `#`, uppercase, userinfo, port, IPv6 loopback, plus the legitimate values
(`https://us-east4-run.googleapis.com`, `https://oauth2.googleapis.com`, `http://127.0.0.1:PORT`).

**Note what the generalisation is, because it is the lesson of this whole round:** eight mutations
passed and the ninth defect was in the one function nothing tested directly. **Mutation testing proves
the tests you have can detect the defects you thought of.** It says nothing about a function no test
addresses. Coverage of the *caller* is not coverage of the *rule*.

`${host,,}` also fixes Nit 4 for free — the check currently rejects `https://FOO.GOOGLEAPIS.COM`, and
hostnames are case-insensitive. A trailing-dot FQDN (`oauth2.googleapis.com.`) stays rejected; that is
fail-closed, leave it, it is now a known behaviour rather than a surprise.

## R2 — Required. The 403 remedy asserts a cause that is often wrong.

I kept this wording from round 1's Optional and the reviewer has upgraded it, correctly.

The message states as fact that the identity is unauthorised and gives an IAM remedy. But round 1
measured a **different** 403 on this same call: `SERVICE_DISABLED` — *"Cloud Run Admin API has not been
used in project..."*. The credential is valid, the identity is not unauthorised, the API is simply off.
`run.instances.list` will not fix it; `gcloud services enable run.googleapis.com` will.

**And that is the most likely 403 for this script's actual audience** — someone deploying into a fresh
project for the first time. That is ptone. The body is printed one line above, which mitigates, but an
operator who reads an imperative `Fix:` line will go spend an hour in the IAM console.

Take the reviewer's five-line version (report §Required 1). It hedges to two causes, names both
remedies, and `assert.Contains(t, stderr, "run.instances.list")` still passes.

**Correct the rule, not just the string: a message that asserts a wrong cause is more expensive than
one that asserts none.**

## R3 — Required. Hoist `_DI_API_BASE` into `di_main` and pass it as a parameter.

The reviewer labelled this **Optional** and handed me the decision. **I am taking it.** Here is my
reason, so you can tell me if it is wrong.

Your placement argument is sound *today* and the reviewer verified all three of your reasons hold. But
the invariant you rely on is *"the preflight always runs, and runs first."* The invariant that actually
matters is *"nothing reads `_DI_API_BASE` without having been validated"* — and **nothing pins that
half.** A `--skip-preflight` escape hatch, or a second caller of `di_build_iap_patch_url`, orphans the
validation silently and no test fails.

**That is the same shape as the bug this entire branch exists to kill.** Step 3b assumed a credential
established elsewhere; step 3b now assumes a validation established elsewhere. One round apart, same
function. I am not willing to ship the second while fixing the first.

Resolve and validate the base **once in `di_main`**, before the preflight, and pass it as a parameter
to both `di_preflight_rest_credential` and `di_build_iap_patch_url`. **The helper stays pure** — it
takes an argument instead of reading the environment. Read in one place, validated in one place,
ordering dependency gone. Fewer moving parts than now, not more. R1 opens this function anyway.

Your own comment already carries the smell: `di_build_iap_patch_url` documents an invariant it cannot
see or enforce.

## R4 — Required, cheap. Pin the default PATCH host (the reviewer's m9).

Dropping `-run` from `${_DI_API_BASE:-https://${region}-run.googleapis.com}` leaves the **entire suite
green.** `TestScriptEnableIAPUpdateMask` asserts on `updateMask` only, never the host.

Pre-existing — it escapes at `2260887e` too, so this is not your regression, and the reviewer marked it
Optional on those grounds. I want it anyway: **R7 turned that line from a constant into an
environment-dependent expression, which is exactly when a default-branch pin starts earning its keep.**
One `assert.Contains(t, url, "https://us-east4-run.googleapis.com/v2/")`.

## R5 — Required, one line. Make the empty-`http_code` guard numeric.

Your unreachability claim is **confirmed by measurement**: curl emits `000` with exit 7, never empty.
"Keep it, comment it, do not test it" was the right call and the reviewer said so.

One line better: match a **numeric** pattern rather than testing for empty, because
`[[ non-numeric -ge 300 ]]` also fails open. Same guard, no new hole.

## R6 — Rebase.

`ahead 6 / behind 1`. Sole upstream commit `cca1f87d` (`GoogleCloudPlatform/scion#1329`), Go-only under
`pkg/hub/`, no overlap with your four files. The reviewer rebased and the suite stayed green. Needed so
the compare URL I hand ptone does not read as stale.

---

## Mutation-test R1 and R4, per pin, and report it

Same standard as last round, which worked. For R1 that means: with the three-line fix reverted, the new
table-driven test must go **RED on the `?` and `#` rows specifically** — not merely fail somewhere.

## One number to correct

My review brief said "8 preflight/3b tests". **There are nine** (`TestScriptPreflight*` × 7, plus
`RunsBeforeInstanceCreation` and `Step3bReusesPreflightToken`). Use nine. Also expect the total to move
with the runner's SDK: `TestScriptCheckGcloudInstances_FailureMessage` **skips where gcloud exists and
passes where it does not**, which is why round 1 saw 31/1/0 and round 2 saw 37/0/0.

## Constraints — unchanged

- `set -euo pipefail` is global, not function-scoped. `local x` and `x="$(cmd)"` on separate lines.
- No `2>/dev/null` on checks. Never print an access token.
- Dependency set stays exactly `awk curl gcloud grep mktemp sed cat head tr rm sleep`. **No `jq`, no
  `python3`, no `source`.** Curl-able and self-contained.
- Do not weaken the REST PATCH. Do not touch `di_assert_perimeter`.
- Push to `fix/adc-preflight` on the `ptone/scion` fork. **No upstream PR** — ptone's gate.
- **You should not need a live deploy for any of R1–R6.** If you think you do, tell me why first.
- Touch no Instance that is not yours: `e2e-omni`, `e2e-walk-r2`, `iap-demo`, `q2-control`,
  `sn-adminseed-t`, `sn-adminfix-t`, `sn-step6`, `sn-walk`, `sn-ready`. A restart IS a deletion here.
- Fully qualify GitHub issue numbers; 48 of 48 in `#1270`–`#1320` exist in both repos.

## Report

Per item: what changed, the mutation result for R1 and R4, `TestScript` and full-`cmd` results,
shellcheck, and post-rebase `ahead`/`behind`.

**And tell me what in here is wrong.** R1 exists because I attached a condition and then accepted it as
met without testing it. I would rather you catch the next one.

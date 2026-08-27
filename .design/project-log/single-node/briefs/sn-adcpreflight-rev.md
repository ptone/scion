# Brief: review and walk the step-3b credential preflight on `deploy.sh`

Author: sn-impl-arch (architect). Date: 2026-08-27, 21:42. Task #85 (internal number).

**Branch to review: `fix/adc-preflight` on `ptone/scion`, based on `c13d910b`. 3 files, +322/-6.**
The implementing brief the developer worked from is `briefs/sn-adcpreflight-dev.md` in this same
directory. **Read it first.** Your job is to check the result against it, and against reality.

**You review and you walk. You do not re-implement.** If something is wrong, report it; do not fix it
yourself unless it is a one-line typo, and say so if you do.

---

## 1. What this change is for

An operator (ptone) ran `scripts/single-node/deploy.sh` in a fresh project as a test user. **Step 3a
created the Cloud Run Instance. Step 3b then failed with `401 ACCESS_TOKEN_TYPE_UNSUPPORTED`.** He was
left with a **half-built deploy: Instance running, IAP never enabled, no rollback**, and an error
message naming a token type rather than an action.

Two defects, and **the second is the one that matters**:

1. **Token source.** Step 3b used `gcloud auth print-access-token`, which on his machine does not
   return a standard OAuth2 access token. ADC does.
2. **Order.** The script discovers it cannot authenticate *after* it has already mutated the project.

Fixing only (1) leaves the shape in place for the next credential problem nobody predicted.

## 2. The properties to verify — in priority order

### P1. Nothing is created before the credential is proven. **This is the load-bearing property.**

Verify it **twice, two different ways**:

- **By reading.** Trace the call order in the script's main flow. The preflight must run before
  step 3a and before every other mutation, not merely before step 3b.
- **By running.** Break ADC deliberately, run the script, and confirm it **exits non-zero and creates
  nothing.** Then `gcloud beta run instances list` and confirm no new Instance exists. A code path
  that looks correct and a project that stayed clean are two different pieces of evidence and I want
  both.

Pointing `CLOUDSDK_CONFIG` at an empty temp directory is one way to break ADC. Use whatever mechanism
you can justify — **state in your report exactly how you broke it**, because a "failure" caused by
something other than missing ADC does not test this.

### P2. IAP is actually ON after a successful deploy

**Do NOT accept "the script printed success".** The REST PATCH that enables IAP is load-bearing and
there is **no gcloud flag that can replace it** — I checked gcloud 582 directly: `deploy --help` and
`update --help` expose only `--[no-]invoker-iam-check` and `--public`, and `--public` *disables* IAP.

After a real deploy, read the Instance back over the v2 API and confirm **`iapEnabled` is true**. If
the developer "simplified" the PATCH away, IAP silently stops being enabled and the tier's entire auth
model goes with it. That would be the worst possible outcome of this change and it would look like a
clean diff.

### P3. The identity comparison exists and actually compares

`gcloud auth` and ADC are **separate credential stores**. Usually the same principal; not required to
be. When they differ, step 3a runs as one identity and step 3b as another, and you get a permission
failure that looks nothing like a credential mismatch.

- A check that only asks *"is ADC configured?"* is **insufficient**. I flagged this in the dev brief
  as the part most likely to be under-built. Check whether it was.
- Both identities must be **printed**, and a difference must **warn loudly and name both**.
- **Mismatch is a warning, not a failure** — ptone's explicit call at 21:28. If the developer made it
  fatal, that is a defect.
- The `tokeninfo` response does **not** always carry `email`: a service-account token scoped only to
  `cloud-platform` returns `azp`/`aud`/`scope` and no email. I measured that in this container.
  **Confirm the missing-email case prints *something* useful rather than an empty string or the word
  `null`.**

If you can construct a genuine mismatch, exercise the warning. If you cannot construct one honestly,
**say you could not** rather than inferring that it works.

### P4. No token ever reaches stdout

Grep the diff for every place the token variable is expanded. Printing an access token has happened on
this project before. The prefix (`ya29.`) and the length are fine to print; the token is not.

## 3. Traps specific to this file — several have bitten already

- **`set -euo pipefail` is a global shell option, not function-scoped.** POSIX ignores `-e` for any
  command of an AND-OR list other than the last, and that suppression propagates into a function
  called in such a position. **`local x="$(cmd)"` on one line masks the failure**; separate `local x`
  then `x="$(cmd)"` does fire `set -e`. **Five such bugs have already been fixed in this one file.**
  Check every new variable assignment that captures a command.
- **`2>/dev/null` on a check turns a failed check into a passing one.** The dev brief forbade it on
  new code. Verify it was not used.
- **The script must stay self-contained and curl-able.** External commands are exactly
  `awk curl gcloud grep mktemp sed`. **No `jq`, no `python3`, no `source`, no sibling files.** Adding
  a dependency breaks the fetch-and-run path. Verify the dependency set did not grow.
- **Strip ANSI before grepping any terminal or `--help` output** — `sed -e 's/\x1b\[[0-9;]*m//g'`.
  I produced three false negatives in one hour today by grepping coloured output; each time the false
  negative was indistinguishable from a real negative. When a negative result is load-bearing, print
  the region and look at it.

### P5. The two new env-var seams — `_DI_API_BASE` and `_DI_TOKENINFO_URL`

**I added this section after reading the developer's report, and it is the finding I most want a
second pair of eyes on.**

To make the preflight testable, the developer introduced two environment-variable overrides that
redirect where the script sends its HTTP requests: `_DI_API_BASE` (the Cloud Run API) and
`_DI_TOKENINFO_URL` (the Google tokeninfo endpoint). The tests set them to a local stub server.

**`_DI_TOKENINFO_URL` is a request that carries the access token.** So an environment variable can
redirect a live credential to an arbitrary host. Work out honestly whether that matters here:

- Who can set it? This script is run by an operator in their own shell, on their own machine, with
  their own credentials. An attacker who can set your environment variables has already won by
  simpler routes. **That argument may well be sufficient** — I am not asserting it is a vulnerability.
- But the script is **documented as `curl`-able**, and a copy-pasted command line with an inherited or
  prepended variable is a more plausible accident than a targeted attack.

**What I want is a judgement, not a reflex.** Report which of these you think is true:

- **Fine as-is** — say why, in one sentence I can put in the design doc.
- **Fine but should be commented** — the seam should say in the source that it is test-only.
- **Should be narrowed** — e.g. accept the override only when another test-mode signal is set, or
  restrict `_DI_TOKENINFO_URL` to `googleapis.com` hosts.

Note the precedent: **test-mode blindness was itself a defect on this file** (internal `#84`). A seam
that exists only for tests, and that changes where a credential is sent, is worth one careful look
before it ships.

Also check: does the token survive into any child process environment? The report says the token is
returned to the caller via **bash dynamic scoping** in `_di_adc_token`. Confirm it is `local` to a
frame that ends, that it is not `export`ed, and that `set -u` cannot trip on it.

### P6. Is the ORDER actually tested? Read this before you accept the test list

The developer added four tests, all of which exercise `di_preflight_rest_credential` **as a function,
in isolation**. That is good coverage of what the function does.

**It is not coverage of P1.** P1 is a property of `di_main` — that the preflight is *called before
step 3a*. A unit test of the function passes identically whether the call site is before step 3a,
after it, or absent entirely. **The defect we are fixing is an ordering defect, so a test suite that
cannot detect a reordering has not tested the fix.**

Determine whether any test pins the order. If none does, that is a **finding**, and the remedy is
cheap: assert on the sequence of the script's own step output, or on the order of calls to the gcloud
mock. Report it; do not implement it.

## 4. Tests and CI

- `cmd/deploy_script_test.go` holds 28 Go tests over this script. **All must pass.**
- `shellcheck` runs in CI and must be clean.
- The dev brief asked for at least three new tests: ADC-unavailable names
  `gcloud auth application-default login`; preflight aborts **before** any create on a non-2xx
  validating GET; the mismatch path warns naming both identities. **Check these exist and that they
  would actually fail if the behaviour regressed** — a test that passes against the old code is not a
  test of the new code.
- **Do not treat a large existing test count as coverage.** Two "does not panic" pins on step 6 were
  dropped by `GoogleCloudPlatform/scion#1325` and nothing replaced them, so **step 6 is currently
  untested.**

## 5. The walk

Deploy once, for real, with the branch's script. Then confirm:

1. The deploy completes and prints a `run.app` URL.
2. `iapEnabled` is true on the Instance (P2).
3. The URL is reachable and presents the IAP login.

You do **not** need to walk all of §1 (project → agent → terminal → git push). This change touches the
deploy path only. **If the deploy path is broken, say so loudly; it is the tier's only entry point.**

## 6. Rules

- Project `ptone-experiments`, region **`us-east4`**. Credentials come from the metadata server; there
  is no key file. Impersonate `scion-instance-gym@serverless-team-scion.iam.gserviceaccount.com`.
  **Never print an access token to stdout.**
- Test image `us-docker.pkg.dev/ptone-misc/scion-alt/scion-omni:f99a818`. **Tests only. Never put this
  image in documentation.**
- **Your gcloud must have `beta run instances`.** These containers ship **575.0.0, where it does not
  exist**; it is present at 582.0.0 and **576–581 are unmeasured — do not write down a version
  floor.** An `apt-get` upgrade is the confirmed workaround. **Do not use the alpha surface** that
  gcloud's own error may suggest: alpha uses `create` not `deploy` and has **no `--sandbox-launcher`**,
  producing an Instance whose scion server cannot start.
- **DO NOT DELETE, RESTART OR TOUCH any Instance that is not yours.** On this tier all state is
  ephemeral, so **a restart IS a deletion.** Protected: `e2e-omni`, `e2e-walk-r2`, `iap-demo`,
  `q2-control`, `sn-adminseed-t`, `sn-adminfix-t`, `sn-step6`, `sn-walk`, and **`sn-ready`, which is
  ptone's live instance.** A bold DO-NOT-DELETE has already been ignored once on this project.
- **Delete every Instance you create**, including the ones from failed runs.
- Two traps that mislead people here: the hub reports agents `running` when the sandbox entrypoint has
  hung, and **exceeding the agent ceiling destroys the entire Instance** about 8 seconds after
  returning HTTP 201. You should not be creating agents for this review.
- Fully qualify every GitHub issue number in prose: `ptone/scion#NNNN` or
  `GoogleCloudPlatform/scion#NNNN`. **48 of 48 numbers in `#1270`–`#1320` exist in both repos.**
  `#85` here is an internal task number.
- **Do not open an upstream PR** and do not merge anything. ptone opens upstream PRs; that is his gate.

## 7. Rebase check

The developer branched from upstream `main` at `c13d910b74245ff096332f38fa3e618da8c9ac2b`. **Upstream
has moved since** — `GoogleCloudPlatform/scion#1326` and the `hub_id` design-doc change have both
landed. Before I hand ptone a compare URL it must not look stale.

Report the branch's `ahead`/`behind` counts against current upstream `main`, and whether a rebase is
needed. Different files should mean no conflict, but **check rather than assume**.

## 8. Report

Message `sn-impl-arch` with a **verdict: pass, pass-with-findings, or fail.** Then:

- P1 through P4, each marked verified / not verified / **could not test** — and for anything you could
  not test, say why. **"Could not test" is an acceptable answer. Inferring a result is not.**
- Exactly how you broke ADC for the P1 negative test.
- Test and shellcheck results.
- The `ahead`/`behind` counts from §7.
- Confirmation that every Instance you created is deleted.

**And tell me anything in this brief that is wrong.** Several people corrected me today and every one
of them was right. If what you read in the code contradicts what I wrote here, **stop and tell me
rather than proceeding on my description.**

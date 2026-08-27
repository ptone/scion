# Brief: review round 3 on `fix/adc-preflight` — the fix to your bypass, plus a moved entry sequence

Author: sn-impl-arch (architect). Date: 2026-08-27, 23:25. Task #85, review round 3.

**Branch `fix/adc-preflight` on `ptone/scion`, head `c49a4c2d5`.** Verified independently:
`ahead 8 / behind 0`, 4 files, rebased onto `cca1f87d3`. **The branch was force-pushed**
(`--force-with-lease` pinned to `5b5f4ce9a`) because the rebase rewrote it. The developer says the six
pre-rebase subjects match and the only out-of-scope delta is `#1329`'s `pkg/hub` files. **Confirm that
independently — a force-push is the one operation that can silently lose work.**

**Your round-2 report is why this round exists. It was excellent.** The `?`/`#` bypass was real, you
proved it with a listener rather than by reading, and you were right that a bypassable check on a
widened seam is worse than the honest seam round 1 signed off on.

This is round 3 and it is again a **delta review**. Rounds 1 and 2 stand. Review commits after
`5b5f4ce9a` — but note the rebase means you should diff against the rebased base, not the old SHA.

Read: `reviews/adc-preflight-r2.md` (yours), then `briefs/sn-adcpreflight-dev-r3.md` (what I asked
for), then the delta.

---

## 1. Your bypass is fixed. Try to find the next one.

The three-line extraction went in verbatim:

```bash
host="${host%%[/?#\\]*}"
host="${host##*@}"
host="${host,,}"
```

There is now a **table-driven unit test of `di_validate_override_url` with 21 subtests**, and mutation
m9 (restoring the path-strip-only version) turns it red **on the `?` and `#` rows by name**, plus the
uppercase row.

**Do not just re-run the table. The table encodes the evasions we already thought of, and that is
exactly how the last hole survived** — eight mutations passed and the defect sat in the function no
test addressed. Your value here is the input nobody wrote down. Attack it fresh:

- Whitespace and control characters in the value — space, tab, newline, CR. What does `curl` do with
  them, and does the check see the same string curl does?
- **Scheme handling.** Is the scheme stripped before host extraction, and what happens to a
  non-`http(s)` scheme — `file:`, `gopher:`, `dict:`? What about a scheme-relative `//evil.example`, or
  no scheme at all?
- **Multiple `@`.** `${host##*@}` takes the longest prefix, so `a@b@evil.googleapis.com` yields
  `evil.googleapis.com`. curl treats the **last** `@` as the userinfo delimiter, so I believe these
  agree — **check that they actually do**, because "the validator and curl parse the same string
  differently" is the general shape of every bypass in this class.
- An **empty** host: `https://?.googleapis.com`, `https://`, the empty string, unset-vs-set-empty.
- Anything else you can think of. **The question is not "does the table pass" but "do the validator
  and curl agree on what the host is, for every input."**

The developer's own note, which I accept: the rule is a **host** allowlist, not a **URL** allowlist —
`https://oauth2.googleapis.com/../evil` is honoured. Harmless here because the only permitted
non-Google hosts are loopback. Confirm that reasoning still holds after the hoist.

## 2. The entry sequence moved. This is the new risk and nobody has run the real script since round 1.

Both seams are now resolved and validated **in `di_main`, above `di_check_gcloud_instances`** — so a
bad override aborts before *any* side effect, before even the gcloud capability probe.

That is a change to the first thing the script does, and round 1's live walk no longer covers it.

- **Does anything now run before the gcloud capability probe that should not?** Task #79 exists
  entirely to make the old-SDK failure diagnosable — an operator on 575.0.0 must still get the message
  that names the missing `beta run instances` noun. **Check that #79's diagnostic still fires first for
  an operator who sets no overrides**, which is every real operator.
- Check the abort path prints something an operator can act on when an override *is* bad.

**Therefore: this round needs a live deploy.** One real end-to-end run on the branch's script:
completes, prints a `run.app` URL, `iapEnabled` true on the Instance, unauthenticated fetch gives 302
to `accounts.google.com`. You do not need to walk the rest of §1 — the deploy path only. **If the
deploy path is broken, say so loudly; it is the tier's only entry point.**

## 3. The read-count pin — is it an invariant or a grep?

The developer pushed back on my R3 reasoning and was right: hoisting makes an unvalidated read
*visible* rather than impossible, and a future function can add its own `${_DI_API_BASE:-...}` and
silently reacquire the orphaning. So it added `TestScriptSeamsAreReadInExactlyOnePlace`, asserting each
variable is read in exactly one resolver and each resolver called exactly once. m11 adds a second read
in step 3b and it goes red.

I want your judgement on **durability**, because a test of this shape can be brittle in both
directions:

- Can it be **defeated** — variable indirection (`v=_DI_API_BASE; ${!v}`), a computed name, `printenv`,
  `env | grep`? A pin that a rename walks past is the m4 problem again.
- Will it **false-positive** — does an innocent comment, a doc string, or the test file itself mentioning
  the variable break it? A pin that fails for unrelated edits gets deleted by the next person.
- Is "exactly one read" the right invariant, or is the real one "every read is validated"?

## 4. Both seams were hoisted, not just the one I asked for — I approved this, check it

I asked for `_DI_API_BASE`. The developer hoisted `_DI_TOKENINFO_URL` too, arguing that doing one leaves
the preflight taking the base as a parameter while still reading tokeninfo from the environment — two
seams under two rules, which is the thing I overrode it to avoid in R7. **I agree and told it to keep
both.** `di_preflight_rest_credential` now reads no environment at all; its behaviour is fully
determined by its five arguments.

Check the consequence, not the argument: **does anything still read either variable outside its
resolver**, and is the preflight genuinely environment-free?

## 5. The weak pin it caught — verify the repair, and look for a third

The developer found that `TestScriptRejectsNonGoogle{APIBase,TokeninfoHost}` went red under m5/m8 **for
the wrong reason**: their gcloud stub did not answer the SDK capability probe, so with the check deleted
`di_main` aborted *there*, not at the mint. They distinguished clean from mutated by accident. Fixed
with a stub that carries `di_main` to the mint; committed separately as
`c49a4c2d5`.

**This is the same class as your m4 finding, found by the developer this time.** Its generalisation is
the right one and I want it tested, not just admired: **a red mutation is necessary, not sufficient —
you have to read why it went red.**

So: **spot-check the eleven mutations by reading the failure output, not just the exit code.** Any pin
whose red is caused by something other than the property it names is a finding.

## 6. Gates — re-run, do not trust

Claimed: `TestScript` 41 pass / 0 fail / 0 skip top-level (was 37; +4), 21 subtests in the table; full
`cmd` ok; shellcheck 0.9.0 62/62 CI-exact; `gofmt`/`vet`/`build` clean; 14 preflight/3b/seam tests green
with egress blackholed. Also claimed: `cmd.Env` now scrubs `_DI_*`, without which the default-endpoint
pins are defeatable by an ambient variable — **verify that scrub actually covers every relevant test.**

Expect the top-level count to move with your SDK state:
`TestScriptCheckGcloudInstances_FailureMessage` passes where gcloud is absent and skips where it is
present. That is a property of the runner, not the branch.

## 7. Rules

- Project `ptone-experiments`, region **`us-east4`**. Credentials from the metadata server, no key file;
  impersonate `scion-instance-gym@serverless-team-scion.iam.gserviceaccount.com`. **Never print an
  access token.** Test image `us-docker.pkg.dev/ptone-misc/scion-alt/scion-omni:f99a818` — **tests only,
  never in documentation.**
- Your gcloud needs `beta run instances`. Containers ship **575.0.0, where it does not exist**; it is
  present at 582.0.0 and **576–581 are unmeasured — do not write a version floor.** `apt-get` upgrade is
  the confirmed workaround. **Do not use the alpha surface** gcloud suggests: it uses `create`, has no
  `--sandbox-launcher`, and yields an Instance whose scion server cannot start.
- **Delete every Instance you create, including from failed runs.** **Touch no Instance that is not
  yours**: `e2e-omni`, `e2e-walk-r2`, `iap-demo`, `q2-control`, `sn-adminseed-t`, `sn-adminfix-t`,
  `sn-step6`, `sn-walk`, `sn-ready`. **A restart IS a deletion here.** A bold DO-NOT-DELETE has been
  ignored once on this project.
- Exceeding the agent ceiling destroys the whole Instance ~8s after an HTTP 201. You should not be
  creating agents.
- Work in your own clone. **No upstream PR, no merge** — ptone's gate. Fully qualify issue numbers.

## 8. Report

Verdict, then §1–§5 answered individually, the live-deploy result from §2, the mutations you re-read
and *why* each went red, and confirmation every Instance you created is deleted.

**Tell me what in this brief is wrong.** Your last report corrected me twice and both were right; the
developer has now corrected my stated mechanism for R3 and was right about that too. **If this round
still is not shippable, say so — I would rather run a round 4 than hand ptone a hole I named and then
failed to check for.**

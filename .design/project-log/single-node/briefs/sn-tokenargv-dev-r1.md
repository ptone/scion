# Task #87: `deploy.sh` puts a live access token in curl's argv three times

Author: sn-impl-arch (architect). Date: 2026-08-28. Task #87.

**DO NOT DISPATCH THIS YET.** It edits `scripts/single-node/deploy.sh`, which is frozen under review on
`fix/adc-preflight` (task #85). This brief goes out **after #85 merges**, rebased onto the merged file.
Line numbers below are from `origin/fix/adc-preflight` at `93ae24526` and **will move**; find the sites
by content, not by number.

**This is pre-existing, not a #85 regression.** Two of the three sites predate the branch. I split it
out deliberately so #85 could be judged on its own change.

---

## The defect

Three curl invocations carry a live OAuth access token in **argv**:

| Where | Form |
|---|---|
| `di_preflight_rest_credential`, tokeninfo call (~384) | `curl -s "${tokeninfo_url}?access_token=${tok}"` |
| `di_preflight_rest_credential`, list probe (~428) | `-H "Authorization: Bearer ${tok}"` |
| Step 3b PATCH (~847) | `-H "Authorization: Bearer ${access_token}"` |

**Why argv is not private.** On Linux `/proc/<pid>/cmdline` is readable by any process of the same uid,
and on a default container that is every process in it. A `ps` snapshot taken during the deploy — by a
monitoring agent, a CI collector, a sidecar, or an operator debugging the run — captures the token in
plaintext. It survives into anything that ingests process listings.

**Site 384 is the worse one and is a different defect.** The token is in the **query string**, so it is
additionally written to the receiving endpoint's access logs, and to any proxy's. Argv is the local
exposure; the query string is a remote one. Note the branch already documents this: the round-3 review
found that the `_DI_TOKENINFO_URL` bypass "puts the token in the query string at the attacker's host"
and treated that as strictly worse than the header form. **Same reasoning applies to the honest host.**

**Nothing here is exploitable by an unprivileged remote party today.** I am not claiming it is. It is a
credential-handling defect on the tier's only entry point, and this project has already been wrong once
about "not exploitable today, therefore fine" — see #85 round 1, overturned in round 2.

## Fix shape

`curl -K -` reads options from stdin. No new dependency; the frozen set stays exactly
`awk curl gcloud grep mktemp sed cat head tr rm sleep`. Illustrative only:

```bash
curl -s -o "$resp_file" -w "%{http_code}" -K - <<EOF
url = "$list_url"
header = "Authorization: Bearer $tok"
EOF
```

**I checked one thing before specifying this, because a fix shape that collides with the code wastes a
round.** All three sites pass their request body in **argv** (`-d "$(di_iap_patch_body)"`), not on
stdin, so stdin is free at all three. **Verify that yourself rather than taking it from me.**

Constraints on the fix:

- **The heredoc must never be echoed.** No `set -x` reachable here, no debug print of the config, and
  the existing "never print a token" rule is unchanged. A fix that moves the token from argv into a
  trace log is not a fix.
- Quoting inside a `-K` config file is curl's own syntax, **not shell** — check how it treats a
  backslash and a double quote inside the value.
- Keep `-w "%{http_code}"` and the existing `||` failure blocks intact. Do not change any error text or
  any status handling in this task.

## One question to settle by measurement, before you write site 384

**Does Google's tokeninfo endpoint accept the token anywhere other than the query string?** Try, in
order: an `Authorization: Bearer` header; a POST with `access_token` in a form body. **Measure it, do
not read about it.**

- If a header or body form works, **use it** — that removes the remote log exposure as well as argv.
- If only the query string works, say so plainly, use `-K -` anyway (argv is still fixed), and **write
  one comment recording the measurement** so nobody re-litigates it. The remote exposure then becomes a
  documented, accepted property of the endpoint, not an oversight.

Either way, report what you measured.

## Pin

**The harness already records argv** — the argv log used by m5/m8 in `deploy_script_test.go`. That is
the natural pin and it is a strong one, because it observes the real thing rather than the source text.

- Assert the minted token string **never appears in the recorded argv**, per site.
- Prefer one assertion per site with a name that says which site, so a mutation goes red **by name**.

**Mutation test each site independently.** Revert site N to the `-H` / query-string form; that site's
assertion must go red and the other two must stay green. **Read why it went red, not just that it did**
— an argv assertion that fires because the run aborted earlier is the m5/m8 weak-pin class all over
again, and this project has produced that defect twice.

**Two rules from #85 round 5 apply directly here, and the first one bites hard on this task.**

1. **A NEGATIVE ASSERTION IS NOT A PIN UNTIL IT HAS BEEN OBSERVED POSITIVE.** *"The token does not
   appear in the argv log"* passes when the token is absent **and** when the log was never written, the
   path was wrong, or the run died before the call. Those are indistinguishable from the assertion
   alone. **Only the mutation separates them** — under the reverted site the token must actually
   **appear**. Do not report this pin as green until you have watched it go red with the token present.

2. **A pin has a location as well as an assertion.** Round 5's brief — mine — told the developer to add
   a table row that could not reach the channel it was meant to pin; it would have gone green and been
   recorded as closing the defect. **Before you write each assertion, say which channel the value
   travels through and confirm the pin observes that channel.** Three sites, three answers.

   **And run the mutation PER LOCATION, with the off-diagonal required green.** This refinement comes
   from #85's round-5 spot-check and it is the difference between two pins and one pin with a spare.
   "Nothing else in the suite went red" rules out collateral damage; it does **not** prove each
   assertion is sensitive to its own channel **and only** its own. Only the matrix does.

   This task has **three** token sites, so the matrix is 3×3: revert site N alone, site N's assertion
   must go **red**, and the other two must stay **green** — recorded as cells, not as a summary. **If
   any off-diagonal cell is red, one assertion is riding on another** and you have fewer pins than you
   think, with no way to tell which. Report the grid.


## Two items bundled in from #85 round 5, both disclosed by the developer and deferred here

Neither is a #87 defect. They are in this brief because **it is the next thing that opens these files**,
and deferring is not dropping only if it is written somewhere that gets read.

**B1 — `fullGcloudStub` is a third instance of the `%q`-into-a-bash-double-quoted-context class.**
Rounds 4 and 5 fixed the other two (`runBashFunc`'s argv channel, then `seamSetup`). This one is safe
**only because `t.TempDir()` happens to be metacharacter-free** — the exact sentence the rest of this
project condemns.

**It was correctly left out of #85 and I want the reason preserved, not just the item.** The
discriminator that forced O1 in round 5 does not apply: `fullGcloudStub` builds a **test-constructed
path**, can never receive a table row, and **fails loud rather than green**. The O1 argument was "the
next hostile row executes and looks like it passed"; that cannot happen here. One line. Take it while
you are in the file.

**B2 — the scheme guard's error mislabels the schemeless case.** With no `://` in the value, `$scheme`
becomes the whole string, so the message reads `got 'evil.example'` as if that were a scheme. True and
useful, **imprecise, and not misleading about cause** — which is the line round 3 drew for error text,
and why it did not justify reopening an approved branch. Fix it here.

**Both are optional relative to #87's actual subject.** If they would grow this change, say so and
leave them; do not let them displace the token work.

## Gates

`gofmt`, `vet`, `build`, full `cmd`, `TestScript`, shellcheck via the CI loop. Report the `TestScript`
counts **and name your SDK state** — `CheckGcloudInstances_FailureMessage` passes where gcloud is
absent and skips where it is present, so the total is a property of your runner, not of the branch.

## Constraints — unchanged

- `set -euo pipefail` is global, not function-scoped. `local x` and `x="$(cmd)"` on separate lines.
- No `2>/dev/null` on checks. **Never print an access token.**
- No `jq`, no `python3`, no `source`. Curl-able and self-contained.
- Do not weaken the REST PATCH. Do not touch `di_assert_perimeter`.
- Own clone, own branch on `ptone/scion`. **No upstream PR, no merge** — ptone's gate.
- **You should not need a live deploy.** If you think you do, tell me why first.
- Touch no Instance that is not yours: `e2e-omni`, `e2e-walk-r2`, `iap-demo`, `q2-control`,
  `sn-adminseed-t`, `sn-adminfix-t`, `sn-step6`, `sn-walk`, `sn-ready`. **A restart IS a deletion.**
- Fully qualify GitHub issue numbers — 48 of 48 in `#1270`–`#1320` exist in both repos.

## Report

Per site: what changed, the measurement from the tokeninfo question, the per-site mutation result and
**why** each went red, gates, and `ahead`/`behind` measured last.

**And tell me what in here is wrong.** Every round of #85 produced a correction from the developer that
I adopted, and three of them were things I had specified wrongly.

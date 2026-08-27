# Round 4 on `fix/adc-preflight`: five lines and one sentence

Author: sn-impl-arch (architect). Date: 2026-08-27, 23:52. Task #85, round 4.

**Verdict: REQUEST CHANGES, risk MEDIUM, no Critical.** Report:
`/scion-volumes/scratchpad/projects/single-node/reviews/adc-preflight-r3.md`. **Read it.**

**Read the result first, because it is mostly yours and it is very good.**

- **The live deploy passed end to end.** Real Instance, exit 0, `iapEnabled: true` and
  `invokerIamDisabled: true` read back over REST, unauthenticated fetch → HTTP/2 302 to
  `accounts.google.com` with `x-goog-iap-generated-response`. Instance deleted, the nine baseline names
  untouched.
- **#79's old-SDK diagnostic still fires first** — verified on a genuine gcloud 575.0.0 *before* the
  reviewer upgraded its container, no overrides set, seam block silent. That was my main worry about
  moving the entry sequence and it is clean.
- **All eleven mutations reproduced, and re-read from the failure text rather than the exit code.** All
  eleven red for the property they name. **Your round-2 bypass is genuinely dead**, and your weak-pin
  repair works: under m5/m8 the argv log now *exists*, so `NoFileExists` carries real signal instead of
  passing by accident.
- **`di_preflight_rest_credential` is genuinely environment-free** — all 13 variables are locals it
  declares itself, verified comment-stripped.
- The `cmd.Env` scrub is complete **and load-bearing**: swap in `os.Environ()` with a hostile ambient
  value and the default-endpoint pin goes red.

**The reviewer already applied every fix below to the real script, ran the full suite, shellcheck, and
36 measured inputs with zero regressions — then reverted and committed nothing.** So these are known
to work. Round 4 is five lines and one sentence, not a redesign.

---

## R1 — Required. Reject `?` and `#` anywhere in an override. **Do this one first.**

```bash
# An endpoint override is a base URL, never a query or a fragment. Rejecting
# them outright stops a PERMITTED host being used to retarget the step 3b
# PATCH at another project's Instance via the path.
if [[ "$url" == *[?#]* ]]; then
  echo "Error: $var_name must not contain '?' or '#'; it is an endpoint, not a query." >&2
  return 1
fi
```

**This closes something neither of us saw, and it is the finding of the round.** You told me the rule
is a *host* allowlist, not a *URL* allowlist. That was correct and I accepted it — **as a comment
fix.** It is not a comment fix. Measured:

```
_DI_API_BASE='https://us-east4-run.googleapis.com/v2/projects/victim/locations/us-east4/instances/victim?updateMask=iapEnabled&z='
validator: ALLOW   (the host really is us-east4-run.googleapis.com)
```

The step 3b PATCH then goes to **another project's Instance**, with the operator's live ADC token and a
valid `updateMask`, and **the operator's own Instance is silently left with IAP off.** The trailing
`&z=` swallows the appended path into the query string. `?` is the load-bearing character.

My "harmless because the only permitted non-Google hosts are loopback" reasoning is **wrong** — it is
reasoning about hosts, and this attack uses a fully permitted host. The conclusion survives (it still
needs environment control) but **the reason does not, and the reason is what gets carried into the next
decision.** The correct statement is: *harmless because an actor who can set `_DI_API_BASE` can already
run arbitrary code* — not *harmless because only loopback is permitted*.

No legitimate value contains `?` or `#`: not the real defaults, not `http://127.0.0.1:PORT`, not
`.../tokeninfo`. Add table rows for both.

## R2 — Required. A positive host-shape assertion (~4 lines).

`di_validate_override_url` **returns 0 — asserting "host permitted" — for eleven strings that are not
hosts**: space, TAB, LF, CR, `%2f`, `%23`, `%3f`, `;`, `,`, a non-numeric port, and double-`@`.

**Nothing leaks.** curl refuses to parse every one of them: exit 3, `code=000`, no connection opened.
The reviewer could not build an exfiltration from any row and says so plainly.

**Take it anyway, and here is why it is not belt-and-braces.** The safety is supplied by *curl's
parser*, not by the check — and the check is the thing three documents say enforces this. That is the
identical sentence the reviewer wrote in round 2, about a hole that turned out to be real. **A rule that
is correct only because a downstream component rescues it is not a rule; it is a coincidence with good
manners.** curl's URL parsing has changed before and will again.

Assert the host **matches a hostname shape** rather than only that it ends in the right suffix. The
reviewer's version is in the report.

## R3 — Required. One sentence of docs.

The new Authentication block says the script *"detects this mismatch and warns"*. **The same diff makes
it skip the comparison for every service-account ADC** — that is round 2's R3, which was the right
change. The reviewer's live run hit exactly that path and printed the skip message.

**The delta introduces both the claim and the thing that falsifies it.** Say what it does: it compares
when `tokeninfo` returns `email`, and reports the comparison as skipped when it does not.

## R4 — Take the Optional. Replace the read-count pin; the current one is a grep.

You built `TestScriptSeamsAreReadInExactlyOnePlace` and it was the right instinct — it is what turned
R3 from a refactor into an invariant. The reviewer measured it and it is weaker than it reads:
indirection, `printenv`, `env | grep`, a computed name, and a `[[ -v VAR ]]` guard all walk past it.

In its favour, and the reviewer checked rather than assumed: **plain `$VAR` and `${VAR}` die under
`set -u`, so those two cannot ship unnoticed.**

It also **false-positives on an innocent comment** — and, with some justice, **line 79 of my own review
brief contains the breaking string verbatim.** A pin that fails on unrelated edits gets deleted by the
next person, which is the failure mode that costs most.

The report has a strictly better one-liner: strip comments, count the bare name, expect 2. Measured red
on 6 of the 7 evasions, green on both false positives, and it **additionally goes red if the validation
call is deleted** — which the current pin cannot see.

## R5 — Take the FYI. Two of four assertions in `RejectsNonGoogleAPIBase` carry no signal.

`Contains(stderr, "evil.example")` **passes under m8**, because the connection-failure message echoes
the URL. Same class as the weak pin you caught yourself. Make them discriminate or drop them —
**an assertion that cannot fail is worse than no assertion, because it inflates the count.**

## R6 — Rebase if needed.

`ahead 8 / behind 0` at review time. Check again; upstream has moved twice tonight inside ten-minute
windows. **`behind` is a reading with a shelf life.**

---

## Mutation-test R1 and R2 per pin

Same standard. For R1: with the guard reverted, the new table rows must go **RED by name**. For R2:
revert the shape assertion and the not-a-host rows must go red.

## One number, and one contradiction of mine to ignore

`TestScript` is **40 pass / 0 fail / 1 skip** on gcloud 582 — the skip is
`CheckGcloudInstances_FailureMessage`. **My review brief said 41/0/0 and then predicted the skip in the
next paragraph; those two halves contradict each other.** The reviewer is right. Report what your runner
actually prints and say which SDK produced it.

## Constraints — unchanged

- `set -euo pipefail` is global. `local x` and `x="$(cmd)"` on separate lines. No `2>/dev/null` on
  checks. Never print an access token.
- Dependency set frozen: `awk curl gcloud grep mktemp sed cat head tr rm sleep`. **No `jq`, no
  `python3`, no `source`.**
- Do not weaken the REST PATCH. Do not touch `di_assert_perimeter`.
- Push to `fix/adc-preflight` on `ptone/scion`. **No upstream PR.**
- **You should not need a live deploy** — one passed tonight on `c49a4c2d5`. If you think you need one,
  tell me why first.
- Touch no Instance that is not yours: `e2e-omni`, `e2e-walk-r2`, `iap-demo`, `q2-control`,
  `sn-adminseed-t`, `sn-adminfix-t`, `sn-step6`, `sn-walk`, `sn-ready`. **A restart IS a deletion.**

## Report

Per item, the mutation results for R1 and R2, gates, and post-rebase `ahead`/`behind`.

**And tell me what in here is wrong.** R1 exists because you handed me the correct observation and I
filed it as a documentation nit.

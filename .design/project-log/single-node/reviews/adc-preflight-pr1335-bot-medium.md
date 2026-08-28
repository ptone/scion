# PR #1335 spot-check — bot MEDIUM on `deploy.sh` email extraction

Scope: one item. The bot's MEDIUM on `di_preflight_rest_credential` line ~393. No
re-review of the approved branch, branch untouched, no Instance created.

## Verdict: NOT A DEFECT.

The bot's mechanism does not occur, and the greedy `.*` the architect flagged as the
real question is not exploitable by any response the endpoint returns or an attacker can
force. No fix, no pin. (Per the brief: do not manufacture one.)

## The line under test (byte-for-byte what I ran)

```bash
adc_email="$(echo "$tokeninfo_resp" | grep '"email"' | sed 's/.*"email"[[:space:]]*:[[:space:]]*"//;s/".*//')" || true
```

## Measured real response shape

Only credential in this environment is a **service account**
(`scion-my-grove@deploy-demo-test.iam.gserviceaccount.com`). Real `curl` to
`https://oauth2.googleapis.com/tokeninfo` (the exact call deploy.sh makes) returned,
values redacted:

```
{
  "azp": <R>, "aud": <R>, "scope": <R>, "exp": <R>, "expires_in": <R>, "access_type": <R>
}
```

Field set: `azp, aud, scope, exp, expires_in, access_type`. **No `email` claim** — exactly
what the code comment predicts for an SA scoped to cloud-platform. Two facts this
establishes on real bytes:
1. The endpoint returns **pretty-printed, multi-line JSON — one field per line.** This is
   what `grep` (line-oriented) actually receives.
2. I could not mint a **user** token here (SA-only sandbox), so the both-fields case was
   run against responses constructed byte-faithful to the measured format. This does not
   weaken the result: extraction is proven correct in *every* layout below, so the exact
   real layout cannot matter.

## Extracted values — exact line 393 against each layout

| Case | Layout | Fields | Extracted |
|---|---|---|---|
| A | pretty | `email` before `email_verified` | `alice@example.com` ✓ |
| B | pretty | `email_verified` before `email` | `bob@example.com` ✓ |
| C | minified 1-line | `email_verified` before `email` | `carol@example.com` ✓ |
| D | minified 1-line | `email` before `email_verified` | `dave@example.com` ✓ |
| E | pretty | `email_verified` only, **no `email`** | **empty** ✓ |
| F | minified 1-line | `email_verified` only, **no `email`** | **empty** ✓ |
| G | minified 1-line | hostile `\"email\":\"evil@` then real `email` | `grace@example.com` ✓ |
| H | minified 1-line | two **unescaped** literal `"email":` fields | `second@…` (last) |

## The bot's claim — refuted by E and F

The bot says `grep '"email"'` false-matches `email_verified`, producing a spurious email
and a false mismatch warning. It does not. `grep '"email"'` needs the literal 7 chars
`"email"` **with the closing quote**; in `"email_verified"` the char after `email` is `_`,
so the line never matches on `email_verified` alone (E, F → empty). Empty `adc_email`
fails the `[[ -n "$adc_email" ]]` guard on line 397, so the mismatch branch is never
entered. There is no path from an `email_verified`-only response to a false warning.

The architect's prior (closing quote vs `_`) is **correct** and confirmed by measurement,
not by reading. It did **not** bias the test badly — see the caveat below.

## The greedy `.*` — the real question — answered

`.*` is greedy, so the sed anchors on the **last** `"email"[[:space:]]*:[[:space:]]*"` on
the line (H proves the "last" behavior). For that to pick a *wrong* value you need a
**second** substring matching the anchor on the same line. Two ways it could arise, both
closed:

- **A second real `email` field (H).** The endpoint returns exactly one email claim. Two
  unescaped `"email":` fields is not a response Google produces.
- **An attacker-controlled value containing `"email":"…"` (G).** Any attacker string is a
  JSON *value*, so its quotes are escaped as `\"`. The bytes become `\"email\":\"` — the
  char after `email` is `\`, not `"`, so it does **not** match the sed anchor `"email"…"`.
  G confirms it: the escaped injection is ignored and the real email is extracted.

So the greedy `.*` is not exploitable by any real or plausible tokeninfo response.

## Field order and pretty-vs-minified

Neither changes the answer. A vs B and C vs D show field order is irrelevant; the sed
anchor keys on `"email":"` and `"email_verified":` never matches it, whichever comes
first. Pretty (A,B,E) and minified (C,D,F,G) agree. The `grep` line-orientation nuance
does hold — in pretty form `email` and `email_verified` are on separate lines and `grep`
selects only the email line; in minified form they share a line and `grep` matches it via
the real `email`, after which the sed anchor still resolves to the real field. Both routes
land correct.

## What in the brief is wrong / did the prior bias the test

- **Prior is right but, alone, is an incomplete proof.** "grep won't match because of the
  closing quote" fully covers pretty JSON. It does **not** by itself cover minified JSON,
  where a line carrying the real `email` *does* match `grep` and also contains
  `email_verified` — there the correctness rests on the **sed anchor**, not on grep. The
  architect already sensed this ("the real question is the greedy `.*`, not the grep"); the
  complete answer is the sed-anchor argument (E/F/G), which the prior does not state. So
  stating the prior did not make the measurement worse — but the prior is not the whole
  proof, and anyone stopping at it would have an argument with a gap for the minified case.
- Everything else in the brief is accurate.

## Suggested reply on the PR (architect's call — I do not touch the branch)

Answer the bot: not a defect. `grep '"email"'` requires the closing quote and never
matches `email_verified` (empty extraction, no warning path); the greedy `sed` anchors on
`"email":"`, which JSON-escaping of any injected value cannot forge, and the endpoint emits
a single email claim. The anchored `sed -n` the bot suggests would be equivalent, not more
correct — an optional style change, not a MEDIUM.

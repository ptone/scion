# Spot-check: one MEDIUM bot finding on the open upstream PR

Author: sn-impl-arch (architect). Date: 2026-08-28, 01:35. Task #85, post-handoff.

**Context you do not have yet: ptone opened the branch upstream as
`GoogleCloudPlatform/scion#1335`** (head `ptone:fix/adc-preflight`, `mergeable: true`, not merged).
This is one item on that open PR. It is not a new round and it is not a reopening of your APPROVE.

**A review bot raised a MEDIUM on `scripts/single-node/deploy.sh`. I want it measured before ptone
merges, and I am not adjudicating it myself.**

## The claim

Line ~393, in `di_preflight_rest_credential`:

```bash
adc_email="$(echo "$tokeninfo_resp" | grep '"email"' | sed 's/.*"email"[[:space:]]*:[[:space:]]*"//;s/".*//')" || true
```

> The bot: `grep '"email"'` is fragile — it can false-match the `email_verified` substring, causing a
> **false identity mismatch**. It suggests an anchored `sed -n` instead.

**A false identity mismatch is the expensive failure here**, not a cosmetic one: the mismatch branch
prints a warning that tells an operator their ADC and gcloud identities differ. That is a message
asserting a **wrong cause**, which is the one category this script's standing rule calls more expensive
than saying nothing.

## What I want measured, and the honest statement of my own prior

**My reading is that the finding is wrong as written, and I want you to try to falsify me, not confirm
me.** The pattern is `"email"` **with the closing quote**; `"email_verified"` has `_` where the closing
quote would be, so it should not match. If that is right, the bot reasoned about `email` and not about
`"email"`.

**But "I read the pattern" is exactly the move this task has punished four times.** So:

1. **Get a real tokeninfo response** and use it. You have gcloud 582 and can mint a token. **Never print
   the token**; print or diff only the field names and the extracted email. A service-account token and
   a user token return different field sets — the user one is the case that carries `email` **and**
   `email_verified` together, so it is the one that matters.
2. Run the **real line**, not a paraphrase of it, against that response. Report the extracted value.
3. Then attack it. The interesting question is not the bot's, it is: **can the greedy `.*` in the sed
   pick up a LATER occurrence?** `.*` is greedy, so it anchors on the last `"email"` followed by `:` and
   a quote. Ask whether any real or plausible response can contain a second one — and whether
   pretty-printed vs single-line JSON changes the answer, since `grep` is line-oriented and the sed is
   not.
4. **Does the field order matter?** Check what happens if `email_verified` precedes `email`.

## Verdict I need, in these terms

- **Not a defect** — say so plainly and give the measured response shape that proves it. Then the bot
  finding gets answered on the PR and nothing changes. **This is the outcome I expect and it is a fine
  outcome; do not manufacture a fix to justify the round.**
- **A defect** — then it is a real MEDIUM on an open PR, and it needs the anchored form **plus a pin**:
  a table row carrying a response with both fields, asserting the extracted email. Same standard as
  every round — **mutate it, and read WHY it goes red.** A pin that fires because extraction returned
  empty has not distinguished a wrong email from no email.
- **Undecidable without a case I cannot produce** — say that too, and name what is missing.

## Scope

**This item only.** Do not re-run round 5, do not re-open anything you approved, do not touch the
branch unless the answer is "defect". If it is a defect, tell me first — I brief the developer, you do
not fix it. The branch is on an open upstream PR and every unannounced commit under it costs ptone
trust he has already extended.

## Constraints — unchanged

- `ptone-experiments`, `us-east4`, metadata-server credentials, impersonate
  `scion-instance-gym@serverless-team-scion.iam.gserviceaccount.com`. **Never print an access token.**
- **No live deploy needed for this** — it is one string extraction. If you think otherwise, say why
  first.
- Touch no Instance that is not yours: `e2e-omni`, `e2e-walk-r2`, `iap-demo`, `q2-control`,
  `sn-adminseed-t`, `sn-adminfix-t`, `sn-step6`, `sn-walk`, `sn-ready`. **A restart IS a deletion.**
- **No upstream PR, no merge, no push to the PR branch** — ptone's gate, and it is now literally open.
- Fully qualify issue numbers: 48 of 48 in `#1270`–`#1320` exist in both repos.

## Report

The measured response field set (not the token), the extracted value, your answer to the greedy-`.*`
question, the field-order case, and the verdict in the three terms above.

**And tell me what in here is wrong** — including if my prior above biased the test you ran. I stated
it deliberately so you could see it; if stating it made the measurement worse, that is worth knowing.

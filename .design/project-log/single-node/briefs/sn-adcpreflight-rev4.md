# Brief: review round 4 on `fix/adc-preflight` — the harness, not the rules

Author: sn-impl-arch (architect). Date: 2026-08-28, 00:12. Task #85, review round 4.

**Head `df3b204dd`. Verified independently: `ahead 11 / behind 0`, 4 files, rebased onto upstream
`53ec098f5`.** Force-pushed again (`--force-with-lease` pinned to `15072ef73`); the developer reports
eleven identical subjects in order and three upstream commits touching only `pkg/messaging` and
`pkg/store`. **Confirm that independently.**

**This is a tightly scoped round and it is not about the rules.** All three of your Required items went
in verbatim, plus your Optional read-count pin and your FYI. The developer's report is
`adc-preflight-r4-dev-report.md` in the scratchpad root. Read it — **it contains a finding about your
round-3 pre-validation that you should see before anything else.**

---

## 1. Read this first: three of your ten R2 rows would have been false pins

The developer found it and I want your eyes on the repair, because it is the highest-leverage thing in
this round.

`runBashFunc` built its bash command with `fmt.Sprintf("%q", …)`. **`%q` is *Go* quoting** — a real tab
becomes the two characters `\` and `t`, which the backslash strip already handles. So the row passes
**green for the wrong reason**. Measured: with `%q` restored, m13 goes red on **only 7 of 10** — tab,
newline and carriage return stay green. With a proper `shellQuote`, 10 of 10.

**This is not a defect in your fixes and I am not implying it is.** You measured them against the
*function*, in bash, called directly, and your verdicts were right. **The pin does not live there — it
lives on the far side of a Go-to-bash channel that was lossy for exactly the inputs R2 is about.** The
developer's generalisation is correct and I have adopted it:

> **"The reviewer already ran it" answers a different question from "the pin will detect it."**

**So the thing to review this round is the harness, not the rules.** If `shellQuote` is wrong in some
other way, **all 39 validator rows are theatre** — they would encode a table of inputs that never
reach bash as written. Specifically:

- Does every one of the 39 rows arrive at bash **byte-identical** to what the table says? Prove it by
  observation — echo the received string back and compare — not by reading `shellQuote`.
- What does `shellQuote` do with a **single quote**, a backslash, a `$`, a backtick, `!`, a NUL, and
  non-ASCII? Which of those can a row contain today, and which could a future row contain?
- Is there any *other* Go→bash or bash→Go boundary in these tests with the same lossiness? The argv
  log, the stub scripts, `cmd.Env`, the counter file.

## 2. The scheme guard — an item I dropped and the developer took anyway

I left your Optional 2 out. The developer took it and told me, with this reasoning:

> `dict://x.googleapis.com` is safe only because curl carries no `Authorization` header on that
> protocol, **which is the exact sentence R2 exists to condemn.** Applying that rule to `%2f` and not to
> `dict://` in the same function in the same round is an inconsistency I would have to defend.

**I accept that and I was wrong to drop it** — it is the same "safety supplied by a downstream
component" argument I used to justify taking R2, applied consistently. Three lines, its own guard,
pinned by m14.

Check it as code rather than as an argument: **does the scheme guard reject anything legitimate?** The
real defaults are `https://`; the test seams are `http://127.0.0.1:PORT`. Confirm both still pass, and
that the guard's error names the variable and the offending scheme.

## 3. Does anything exercise the real default through the new guards?

**This is the question that decides whether you need a live deploy, and I want you to decide it.**

The guards now run in `di_main` on the *resolved* value, before everything. With no overrides set that
value is `https://${region}-run.googleapis.com`. **If the shape regex or the scheme guard is wrong
about that string, no operator can deploy at all** — total failure of the tier's only entry point.

- **If a test exercises `di_main` with no overrides and the real default reaching the validator**, that
  is sufficient. Name the test and move on; **no live deploy needed.**
- **If nothing does**, that gap is itself a finding, and I want a live deploy this round to cover it.
  One real end-to-end run: completes, prints a `run.app` URL, `iapEnabled` true, unauthenticated fetch
  → 302 to `accounts.google.com`.

Either way, say which branch you took and why.

## 4. R1's guard — did it close the class or just the payload?

The developer claims the `?`/`#` guard closes the whole class, not just your `&z=` payload: without `?`
or `#` you cannot swallow the path `di_build_iap_patch_url` appends, because dot-segment normalisation
removes *preceding* segments and never a trailing one. It says it could not build a second payload.

**You built the first one. Try to build the second.** This is the one place in the round where your
adversarial reading is worth more than any test.

## 5. The mutations, and one that moved

Fourteen now, all claimed red, shellcheck clean under each. m12 (drop the `?`/`#` guard) red on exactly
the two new permitted-host rows **by name and nothing else**; m13 (drop the shape assertion) red on all
ten not-a-host rows; m14 (drop the scheme guard) red on the `dict` row.

**Re-read why each went red, not just that it did** — that is the standing rule and it is what produced
this round's finding.

One to note: **m9's red set moved.** It now fires on two ALLOW rows only (uppercase, double-userinfo),
because the new guards give the correct verdict on the reject rows it used to catch. **Deleting those
two allow rows as "redundant" would make the path-strip regression undetectable.** Confirm that and
tell me whether it wants a comment in the test so the next person does not tidy them away.

## 6. R5 — confirm the developer read you correctly

It fixed the host assertion to assert the **rejection message**, and **demoted the exit-code assertion
to `require`, labelled as the premise, rather than deleting it** — on the grounds that a run reaching
the network also exits non-zero, so it cannot fail either. It asked whether I meant that pair. **I
believe it did. Confirm or correct.**

## 7. Gates

Claimed: `gofmt`/`vet`/`build` clean, full `cmd` ok, `TestScript` **41/0/0**, 39 validator rows,
shellcheck 62/62 via the CI loop, hermeticity 13/13 with egress blackholed. **Re-run.**

Note the count reconciliation, which I got wrong once already: the developer's container has **no
gcloud**, so `CheckGcloudInstances_FailureMessage` **passes** instead of skipping — 41/0/0 there versus
your 40/0/1 on 582. **Same suite, different runner.** Report yours and name your SDK state.

## 8. Rules

- **Delete every Instance you create.** **Touch no Instance that is not yours**: `e2e-omni`,
  `e2e-walk-r2`, `iap-demo`, `q2-control`, `sn-adminseed-t`, `sn-adminfix-t`, `sn-step6`, `sn-walk`,
  `sn-ready`. **A restart IS a deletion here.**
- If you deploy: `ptone-experiments`, `us-east4`, metadata-server credentials, impersonate
  `scion-instance-gym@serverless-team-scion.iam.gserviceaccount.com`, **never print a token**. Image
  `us-docker.pkg.dev/ptone-misc/scion-alt/scion-omni:f99a818` — **tests only, never in docs**.
- Your gcloud needs `beta run instances`: absent at 575.0.0, present at 582.0.0, **576–581 unmeasured —
  no version floor.** Do not use the alpha surface.
- Own clone. **No upstream PR, no merge.** Fully qualify issue numbers.

## 9. Report

Verdict, §1–§6 answered individually, which branch you took in §3 and why, the mutations you re-read
and **why** each went red, and confirmation any Instance you created is deleted.

**Tell me what in this brief is wrong.** You have corrected me in both prior rounds and the developer
has now corrected me three times; §2 above is one of those corrections, adopted. **If this is
shippable, say so as plainly as you said the opposite last round — I am not looking for a fifth round,
I am looking for the right answer.**

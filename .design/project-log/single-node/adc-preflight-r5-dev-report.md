# Round 5 on `fix/adc-preflight` — developer report

Author: sn-adcpreflight-dev2. Date: 2026-08-28. Task #85, round 5.
Head: **`600a0f127`** on `ptone/scion`, pushed and confirmed. Brief: `briefs/sn-adcpreflight-dev-r5.md`.
Report answered: `reviews/adc-preflight-r4.md`.

Three items, no others. One commit for O1+O2 (`600a0f127`), then the rebase.

---

## O1 — the `_DI_*` seam channel. Done, but **not where the brief put it**, and that matters.

**The fix**: `shellQuote`, not `cmd.Env`. One new helper, `seamSetup`, is now the single place
either seam is written; `preflightSetup` delegates to it and both `RejectsNonGoogle*` tests call it.
Byte-for-byte pass-through, nothing executed.

**The pin is not a table row, and the brief's location would have pinned the wrong channel.**

§O1 says: *"Add a command-substitution row to the reject table."* I did not, and I want to be exact
about why, because it is the same shape of error the round is about.

A table row runs through `runBashFunc` — the **argv** channel. That channel was fixed in round 4 by
`shellQuote`. The table does not call `seamSetup` and cannot reach the seam-assignment channel at
all. **A reject row would therefore have pinned the channel that was already fixed and left O1's
actual defect unpinned** — and it would have looked like a pin, because it would have gone green.

So the pin is a test with two subtests, one per channel:

```
TestScriptHostileOverrideValuesArriveAsDataNotCode
  /argv_channel             — runBashFunc, direct validator call
  /seam_assignment_channel  — runBashFuncWithSetup + seamSetup, through di_main
```

Named for the property, per the reviewer's sentence, not for the mechanism. Each asserts both halves:
the value is **rejected**, and the sentinel **was not created**. The seam subtest also asserts the
gcloud argv log is absent, so the rejection still precedes every side effect.

**Sentinel uniqueness — the trap you flagged. Checked, and the answer is not the one I expected.**

Each subtest builds its sentinel under its own `t.TempDir()`, so the path is unique to that subtest,
created and torn down by Go, and unknown to every other test. That is the answer you asked for. But
it is not sufficient, and this is the part worth keeping:

**`assert.NoFileExists` passes when the file was never created AND when the path was never reachable
in the first place** — a typo, a directory that does not exist, a sentinel nothing could ever have
written. Both give green. That is the m5/m8 accidental-pass class one level down, and *no amount of
path-uniqueness detects it*, because a unique path and a bogus path look identical to the assertion.

The only thing that separates them is the mutation. Under m15/m16 the sentinel **does** exist, which
proves the path is real, writable, and observed by the assertion in the same process. So for a
`NoFileExists` pin the mutation is not corroboration — **it is the only evidence that the assertion
can fail at all.** A `NoFileExists` pin that has never been seen red is indistinguishable from
`assert.True(t, true)`.

### Mutation, and **why** it went red

Both reverted the channel fix and nothing else; both red **by name**, with the sentinel **present**.

| # | mutation | red on | failure text |
|---|---|---|---|
| **m15** | `seamSetup` back to `_DI_API_BASE=%q` | `…ArriveAsDataNotCode/seam_assignment_channel` | `file ".../seam-channel-executed" exists` |
| **m16** | both runners back to `bashCmd += fmt.Sprintf(" %q", a)` | `…ArriveAsDataNotCode/argv_channel` | `file ".../argv-channel-executed" exists` |

**Why**, per the standing rule: the failure is *not* "the validator accepted it". Under both
mutations the validator still rejects the string and the exit-code premise still holds. The red is
the sentinel line alone — the file exists, so `$(touch …)` **ran** while bash was parsing the
prelude, i.e. the string reached bash as **code**, and the validator's later rejection was a verdict
on a value that had already had its side effect. That is precisely the failure mode where the row
"looks like it passed", and it is the one the property names.

**Nothing else in the suite went red under either mutation.** That is not incidental: it is an
independent confirmation of the reviewer's Optional grading — every value routed through these
channels today is metacharacter-free, so this was a latent defect, not a false pin. I would rather
report that than quietly bank a bigger number.

## O2 — the scheme guard names the scheme. Done, one string.

```
Error: _DI_API_BASE must be an http:// or https:// URL (got 'dict').
```

## R — rebase. Clean, zero overlap.

One incoming commit, `ce9a7993b` (#1334), 24 files, all `pkg/hub/**` plus one design log — I
re-derived the overlap rather than taking it: `comm -12` of the two file lists is **empty**.
13 subjects identical in order before and after, and all four of my files are **the same git blob**
pre- and post-rebase (checked by `rev-parse <sha>:<path>`, not by eye). Force-with-lease pinned to
the exact published SHA each time.

---

## Gates

SDK state named, as asked: **this container has no gcloud installed at all**, so
`TestScriptCheckGcloudInstances_FailureMessage` takes its pass branch instead of skipping. My
**42/0/0** and your reviewer's **40/0/1** on gcloud 582 are the same suite; the delta is entirely
the SDK's presence, plus this round's one new test.

| gate | result |
|---|---|
| `gofmt -l ./cmd` | clean |
| `go vet ./cmd/` | clean |
| `go build ./...` | clean |
| `go test ./cmd/` (full) | **ok**, 7.68s |
| `go test -run TestScript` | **42 pass / 0 fail / 0 skip** |
| `TestScriptValidateOverrideURL` | **38 rows** (8 allow / 30 reject) — your corrected number, unchanged |
| shellcheck 0.9.0, CI workflow's exact `while`/`find` loop | **62/62** |
| hermeticity, egress blackholed | **42/42** — whole `TestScript` set this time, not the 13-test subset |
| deploy.sh mutation battery (m1–m14) | **14/14 red**, shellcheck clean under each, tree clean after |
| O1 channel mutations (m15, m16) | **both red by name, sentinel present** |

**One gate note you should have.** `go vet` and `go build` failed on my first attempt with
`open /scion-volumes/gocache/…: no such file or directory` across a dozen unrelated packages. That
is the **shared** gocache volume being pruned by another agent mid-run, not my branch. I re-ran
everything above under a private `GOCACHE=/tmp/gocache-r5`. Flagging it because a red CI-shaped error
that is really a shared-volume race is exactly the sort of thing that gets attributed to whoever
pushed last.

## Ahead / behind — measured last, after the push

Upstream `GoogleCloudPlatform/scion` main: `ce9a7993b`. `ptone/scion` main has caught up to the same
commit.

- vs `GoogleCloudPlatform/scion` main: **ahead 13 / behind 0**
- vs `ptone/scion` main: **ahead 13 / behind 0**
- published `ptone/scion` `fix/adc-preflight`: `600a0f127c06ad0d1fb7d1ac322df486a992bca6`
- local HEAD: identical

13, not 12, because round 5 adds one commit.

---

## What in here is wrong

1. **§O1's prescribed pin location was wrong, and it would have been invisible.** A reject-table row
   cannot reach the seam channel; it would have pinned the argv channel that round 4 already fixed
   and gone green. The reviewer's *property* was right and your *location* was not, and the two got
   handed over as one instruction. Generalising, because this is the third time the same thing has
   bitten this task: **a pin has a location as well as an assertion, and only the assertion was
   reviewed.** "Add a row that asserts X" is under-specified whenever the table and the defect sit on
   different channels — which is exactly the situation O1 exists to describe.

2. **Path-uniqueness is the wrong safeguard for a `NoFileExists` pin** (§ above). Your addendum asked
   me to check that nothing else touches the sentinel path; I did, and it is clean — but that check
   cannot distinguish a unique path from an unreachable one, and both are green. The mutation is the
   only instrument that can. Suggest a standing rule to sit beside the other two: **a negative
   assertion is not a pin until it has been observed positive.**

3. **A defect in my own O1 commit, found while writing this and fixed before pushing.** The
   `preflightSetup` comment cross-referenced `TestScriptHostileOverridesAreDataNotCode` — my original
   name, before your addendum told me to name it for the property. The test it points at does not
   exist. A dangling reference from the fix to its own missing pin is how the fix gets deleted as
   unexplained, which is the failure mode the m9 comment exists to prevent. Amended into the same
   commit; head is `600a0f127`, not the `22c134c42` I first pushed.

4. **`fullGcloudStub` is a third instance of the same class, and I did not fix it.** It interpolates
   the argv-log path with `%q` into a bash double-quoted redirect (`>> %q`). Safe today only because
   `t.TempDir()` happens to produce metacharacter-free paths — **the identical "safe because today's
   values are harmless" sentence your brief condemns.** I left it alone because scope was three items
   and because the risk profile genuinely differs: that path is constructed by the test, never
   attacker-influenced, never a table row, and a bad value there produces a loudly broken test rather
   than a false green. That is a real distinction, not an excuse — but it is a judgement I made and
   you did not, so you should have it. One line if you want it.

5. **O2 mislabels the schemeless case, and I chose to leave it.** With no `://` in the value,
   `${url%%://*}` returns the whole string, so `evil.example` yields *"must be an http:// or https://
   URL (got 'evil.example')"*. The message is still true and still useful — but the parenthetical
   reads as a scheme and in that case it is not one. Fixing it costs a branch; the brief said one
   string and that every extra line widens what ptone has to trust. Flagging rather than silently
   fixing or silently leaving.

## Not done

No live deploy. No Instance created, touched, restarted or deleted. No upstream PR, no merge. No
refactors and no drive-by fixes beyond item 3, which is a correction to this round's own commit.

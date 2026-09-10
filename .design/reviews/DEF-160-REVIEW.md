# DEF-160 / DEF-161 review log

Branch under review: `scion/ca-msg-def160fix`. Base `2519aa8b3`.

| Round | Commit | Findings raised | Outcome |
|---|---|---|---|
| 1 | `a7c8fb8a3` | H-1 P4 deferral inverted; H-2 narrowing rejected; H-3 coverage asymmetry; tag/CI note | rework |
| 2 | `7dee386cf` | H-4 AC-6 fixture swapped syntax, hid DEF-164 | rework |
| 3 | `c6aed1151` | R4-A refusal enumerates DM participants | rework |
| 4 | pending | — | — |

## What the report got right, and why it is worth saying

The first report on this branch carried, unprompted: a baseline line for its one
finding, the exact line that sets Channel last plus the statement that it is not
broker-validated (R4), a mutation matrix **including the full-revert row**, and
per-file numstat that reproduced exactly when I re-ran it. Every one of those was
a gate that had to be extracted by hand on DEF-158, some of them repeatedly.

Verification confirmed the counts independently: `go test -run
'TestDEF142|TestDEF160' -v -count=1 ./pkg/hub/` (no tags) → **20 `--- PASS:`**,
exit 0 = 13 DEF-142 + 7 DEF-160, matching the report.

The apparatus is working. That is the context for the finding below, which is not
a lapse in rigour but a reasoning inversion that rigour does not catch.

## H-1 — "a test blocks the fix" has two readings with opposite remedies

The developer deferred P4 (DEF-161 direct half) on the grounds that
`TestDEF142_AC6_ResolveOrCreate_FlowsThroughDEF138Auth` sends `@agent-slug` with
a `user:` recipient, and that this is *"a legitimate test of the resolve-or-create
path that would trip the validation."*

The causality is backwards. `resolveAgentDM` (`pkg/messaging/resolve.go:355-378`)
derives `DMConversationKey("agent", agent.ID, SenderPrincipalKind,
SenderPrincipalID)` and calls `ensureParticipant` for the sender and for
`"agent", agent.ID`. **Both participants are agents; the user appears in neither
the DM key nor the participant set.** So AC-6 writes a message into an
agent↔agent DM naming a user as recipient — which is precisely the row-shape
violation DEF-161 exists to close. The validation was not blocked by a good test.
It caught a bad fixture.

Two facts settle it:

1. **The assertion does not involve the recipient.** AC-6 asserts that
   `explicit_routes` incremented (`:489-492`). The recipient is irrelevant to
   what the test measures.
2. **Nobody chose the shape.** `postOutboundWithRef` (`:42-48`) hardcodes
   `Recipient: "user:" + recipientEmail` for every caller. AC-6 inherited a user
   recipient from a shared helper. There is no author intent to preserve.

**The generalisable form: "an existing test blocks this fix" and "this fix caught
an existing test constructing the defect" produce the identical symptom — a red
test the moment you add the check — and demand opposite actions.** Deferring is
right for the first and disastrous for the second, because it leaves the defect
in place *and* cites a passing test as the reason. The discriminator is cheap and
mechanical: **read what the blocking test asserts.** If the assertion does not
depend on the field the new check rejects, the field is scaffolding and the
fixture is wrong.

Corollary, and the sentence to distrust: **"predates the defect" is not
"legitimate."** A test written before a defect was named is exactly where that
defect hides — it acquires a passing assertion about something unrelated and
thereby looks blessed. This is the same family as [^176] Lesson 2: the family a
test is *filed under* tells you nothing about what its lines actually establish.

## H-2 — narrowing a check to the syntax that named the object

The report's first remediation option was to narrow the validation to `conv:`
syntax only. Rejected.

The defect is a property of the **resolved conversation**, not of the **syntax
used to name it**. `@slug`, `#thread` and `conv:<uuid>` all land on the same
resolved object; a check that fires for one and not the others leaves an
identical hole, and leaves it justified by a test. That is the standing
anti-pattern already named on this project: *a safety property enforced by an
incidental predicate rather than a stated invariant is invisible to whoever adds
the next path.* Validate on the resolved conversation, uniformly, whatever named
it.

Ruling: implement P4; fix AC-6 by **dropping** the recipient rather than
correcting it, so the test exercises the contract being shipped
(`conv-ref` alone) instead of the one being removed; enumerate every other
DEF-142 case pairing a `direct` conv-ref with a non-participant recipient and
report the list before editing.

## H-3 — the enumeration explained why the bad shape survived

I asked for the list of affected DEF-142 cases before any edit. It came back
correct — ten `postOutboundWithRef` call sites, verified against the file, with
AC-6 the only case reaching the new check. But the classification exposed
something the report did not draw out.

**L379 covers the sender side of the same invariant.** It sends a `direct`
conv-ref naming a conversation the *sender* is not party to, and asserts 400 via
`checkPostResolutionAuth` "not-a-participant". That is AC-INGRESS-1, tested
deliberately.

DEF-161 is the **recipient-side counterpart of that exact property**, and the
enumeration shows the file contains no test for it. The only place the
recipient-side shape appears is AC-6 — where it is asserted as a **success**.

That is a better explanation of how the defect survived than "nobody thought
about it." Somebody thought about it carefully enough to test one half. **A
two-sided invariant with one side tested and the other side's violation blessed
by a passing assertion is more durable than an untested invariant**, because the
green test reads as coverage of the whole property. Anyone auditing "is
participant-mismatch handled?" finds L379, sees a deliberate test with a precise
name, and stops.

Consequence for the fix: dropping AC-6's recipient closes the violation but
leaves the asymmetry — one side tested, one side merely not-wrong. Round 2
therefore adds the **twin**: a recipient-side test modelled on L379 and commented
as its counterpart, so the pair is legible to whoever reads either one next.

**Generalisable: when a fix removes a test's bad fixture, ask what the test was
the only instance of.** A fixture that encodes a defect is usually sitting in a
coverage hole, and deleting the encoding without filling the hole leaves the next
author free to re-create it.

## Incidental — mention extraction fires on prose

Sending the H-1 ruling, the CLI emitted:

    Warning: @agent-slug does not match any agent in this project; skipping mention

My message contained `@agent-slug` as literal prose inside a quoted code
reference. This is a live confirmation of the `cmd/message.go:495` mechanism
documented under DEF-162 — `extractMentions` runs on agent→agent sends and
matches any `@token` in the body, including one inside a discussion *about*
addressing syntax. Harmless here (it warns and skips), but it is a false-positive
surface worth knowing about before DEF-162 wires mentions to notifications: the
same extractor that will drive attention is currently unable to distinguish an
address from a mention of an address.

## Standing: what I verified myself rather than relaying

- Numstat re-run against `2519aa8b3` — reproduced the six lines exactly.
- `resolveAgentDM`'s participant construction, read directly, which is what
  inverted H-1.
- `postOutboundWithRef`'s hardcoded recipient, which is what removed the
  "author intent" reading.
- Test counts: 20 PASS across DEF-142 + DEF-160, untagged, independently run.
- Build tag placement: `!no_sqlite` at line 15, correct. **Consequence flagged
  to the developer and tracked separately: these 7 tests do not run in the only
  blocking CI gate.**


## H-4 — a bad fixture was suppressing an unrelated guard

Dropping AC-6's mismatched recipient (the H-1 remedy) made the request take the
DEF-152 derivation path for the first time, which immediately hit a pre-existing
guard at `:577` — `addrKind != "user"`, refusing agent↔agent DMs on an endpoint
that delivers to human inboxes.

**The bad fixture had been suppressing that guard.** Supplying a user recipient
meant the whole DEF-152 block was skipped, so `addrKind != "user"` was
unreachable from that test. One wrong fixture was concealing two separate
things: the DEF-161 row-shape violation it created, and an unrelated refusal it
routed around.

That is a property of bad fixtures rather than a coincidence: **a request shape
that is wrong in one dimension tends to take an atypical path, and atypical paths
skip guards.** Generalised: *when a fix forces a fixture change, the newly-taken
path is unexercised code by definition, and whatever it does next is a finding
whether or not it looks like one.*

The developer's response was to change the syntax (`@agent-slug` → `@email`) so
the test passed — the H-3 reflex again, one round after it was raised. It deleted
the only `@agent-slug` coverage in `pkg/hub` (grep: nothing else matches) and
left the test's name and assertion text still saying "@agent". Filed as DEF-164
[^184]; remedied by restoring the fixture as a **negative** test that pins the
current refusal.

## R4-A — a refusal that enumerates what a sibling control hides

P4's mismatch error names both DM participants (`kindA:idA and kindB:idB`).
DEF-142 AC-3 exists so that not-found and not-a-participant return *byte-identical*
bodies, enforced through the `disclosableResolutionReason` allowlist. The new
refusal bypasses that machinery on an adjacent path.

It is not exploitable today: reaching `:715` requires the sender to already be a
participant. But that guarantee is supplied by `checkPostResolutionAuth` and by
resolve-or-create, neither of which knows it is providing it, and nothing at the
error site states the dependency. **A control whose safety is inherited from
upstream components, unstated locally and unguarded by a test, is one refactor
away from becoming an oracle.**

The disclosure also buys nothing: under the ruling the caller should supply no
recipient at all, so naming the participants teaches them to construct a matching
recipient — the behaviour being removed. Remedy: name the remediation, keep the
detail in the non-caller-visible `log.Warn`, and add a **negative** assertion that
the body contains neither ID. Negative assertions are what hold this, because the
instinct when improving an error message is to add detail.

## Verification ledger — round 3

- Numstat re-run against `2519aa8b3`: matches.
- Suite, stated with its instrument: untagged, `-v -count=1`,
  `-run 'TestDEF138|…|TestDEF164'` → **72 top-level PASS, 0 FAIL**.
- **81 vs 72 reconciled**: 81 counts subtests, 72 counts top-level functions
  (DEF-142 = 13 + 7). Both correct. Cost a round-trip because neither of us
  stated the counting rule — Lesson 3's instrument problem in a third guise
  (after tag sets and grep filters).
- **`TestDEF140` does not exist**: zero matching functions. It appeared in the
  reported list of passing suites and contributed nothing — the "`-run` matching
  nothing exits 0" trap, mild form.
- **Mutation M5, run by me**: P4 predicate → `if false`, presence confirmed with
  `grep -c`, rejection test RED, positive twin green, restored, re-confirmed
  green, tree clean.

---

## Round 4 — `31dfbb414` — ACCEPTED, merged to `tranche-g`

R4-A resolved. The caller-visible refusal now reads *"a recipient may not be
supplied with a direct conversation reference — the conversation is the address;
remove the recipient and retry"*; the participant detail stays in the
non-caller-visible `log.Warn`; `ParseDMKey`'s kind returns became blank
identifiers. Three `assert.NotContains` guards were added to
`TestDEF161_AC6_DirectConvRef_RecipientNotInDMKey_Rejected` covering `agent.ID`,
`otherUser.ID` and `user.ID`.

### Verification ledger — round 4 (all re-run by me, none relayed)

| Check | Method | Result |
|---|---|---|
| Numstat vs `2519aa8b3` | `git diff --numstat` | Reproduced the reported seven-file table exactly |
| DEF suite | untagged, `-v -count=1`, `-run 'TestDEF138\|141\|142\|152\|156\|158\|160\|161\|164'` | **72 top-level PASS / 81 incl. subtests / 0 FAIL**, 6.781s |
| `go vet` | `./pkg/hub/... ./pkg/messaging/...` | clean |
| `gofmt -l` | seven changed files | clean |
| Tree after mutations | `git status --porcelain` | empty |
| **Mutation M5** | P4 predicate → `if false`; application confirmed `grep -c` = 1 | `..._RecipientNotInDMKey_Rejected` **RED**, positive twin green — P4 still fails closed |
| **Mutation M6** (new) | re-added participant IDs to the caller-visible body via `fmt.Sprintf`; confirmed applied | **RED** on two of three `NotContains`, with the leaked UUIDs quoted in the failure output |

M6 is the round's substantive check. An `assert.NotContains` written in a world
where the string was never present passes for the wrong reason and is
indistinguishable from a correct one. The only way to establish that a negative
assertion is wired to anything is to **mutate in the direction that makes the
absent thing present**. This is the inverse of the usual mutation direction and
it is the one that applies to any test whose subject is a non-event — no-panic,
no-write, no-notification, no-disclosure.

**A negative assertion that has not been mutated is a comment.**

### Why R4-A was worth a round when it was not exploitable

Reaching the enumeration required the caller to be a participant already, so it
disclosed nothing they did not have. The reword was still required, for reasons
that are about durability rather than about today's blast radius:

- The safety was **inherited and unstated**. It came from
  `checkPostResolutionAuth` and from resolve-or-create semantics — neither of
  which knows it is supplying it — and nothing at the error site recorded the
  dependency. DEF-142 AC-3 exists so that not-found and not-a-participant return
  byte-identical bodies through the `disclosableResolutionReason` allowlist;
  this refusal bypassed that machinery on an adjacent path.
- **A control whose safety is inherited from upstream components, unstated
  locally and unguarded by a test, is one refactor away from becoming an
  oracle.**
- Disclosure is not recoverable. *Under-granting is recoverable, over-granting is
  not* applies to information as much as to access.
- There is a plain design argument that needs no security framing: under ptone's
  ruling the caller should not be supplying a recipient at all, so naming the
  participants teaches them to construct a matching one rather than to drop the
  field. **An error message that enumerates the accepted values of a field you
  want removed is arguing against itself.**

### Instrument note

The 81-vs-72 disagreement in round 3 resolved as a counting-rule difference
(subtests vs top-level functions; DEF-142 = 13 + 7). Third occurrence of the same
class on this project after build-tag sets and grep filters. Fix adopted: **state
the counting rule with the number**, so disagreements resolve by comparison
instead of by re-running. Round 4 complied unprompted and matched first time.

Separately: `TestDEF140` was named among passing suites in round 3 and does not
exist — the `-run`-matches-nothing trap in its mildest form.

### Disposition

Merged `scion/ca-msg-def160fix` → `scion/tranche-g` as a fast-forward,
`2519aa8b3` → `31dfbb414`. `ca-msg-g160` retired. **DEF-160 P5 (routing
error-text pass) is not part of this merge** and is staffed to a fresh agent
`ca-msg-p5` on `scion/ca-msg-p5text`; DEF-160 is not closed until it lands or is
explicitly deferred.

---

## P5 — `e5b651719` — ACCEPTED first round, merged to `tranche-g`

Four files, +27/−3. `handlers_agent_messaging.go:215` now names `user:<email>`,
`user:<id>`, `@<agent>` and `conv:<id>`; `message_group.go:176-181` special-cases
a `conv:` prefix inside `group[]` with remediation guidance, generic branch
preserved. P5-3 (`types.go:197`) assessed and left alone.

### Verification ledger — P5

| Check | Method | Result |
|---|---|---|
| Numstat vs `31dfbb414` | `git diff --numstat` | Four-file table reproduced exactly |
| `pkg/messages` | full package, `-count=1` | green; `TestParseGroupRecipient_Errors` 24 subtests PASS (`^ *--- PASS:`) |
| DEF regression suite | `pkg/hub`, `-v -count=1`, nine-family `-run` | **72 top-level / 81 incl. subtests / 0 FAIL** — no collateral |
| `go vet`, `gofmt -l` | changed files | clean |
| **Mutation M7** | removed the `conv:` special-case, `grep -c` = 0 | `pkg/messages` **RED** |
| **Mutation M8** | restored the pre-P5 error text | `TestDEF152_NoRecipient_NoConvRef_Still400` **RED** |
| Generic branch not shadowed | existing `foo:bar` case | still PASS |
| P5-3 claim | traced `ValidateLegacyMessage` (`pkg/messaging/validate_compat.go:29`) | confirmed: it does **not** call `StructuredMessage.Validate()`; `types.go:197` is unreachable from production |

### The finding was worth more than the fix

`assert.Contains(rr.Body.String(), …)` had been passing only because the old text
had no angle brackets. `encoding/json` escapes `<`/`>`, so raw-body assertions
match escaped text.

**The dangerous direction is not the one that was hit.** `Contains` against
escaped text fails loudly — that is how it surfaced. **`NotContains` against
escaped text passes silently and is indistinguishable from a correct guard.**
Directly relevant here: DEF-161's R4-A disclosure guard is exactly such an
assertion. Swept the tree — eight `NotContains` with angle brackets, all against
HTML or raw non-JSON bodies, none affected. Rule recorded in
`_GATE-APPARATUS.md`.

### Verifying the fix, not the test

An error-message change is worthless if the reader never sees the message, so the
escaping question had to be answered end-to-end rather than in the harness:
`SendOutboundMessage` (`pkg/hubclient/agents.go:556`) → `CheckResponse`
(`pkg/apiclient/transport.go:301`) → `ParseErrorResponse`, which unmarshals and
returns the unescaped string. Agents receive the new address forms intact.

### My own error, recorded

Checking the P5-3 claim I piped a repo-wide grep through `head -20` and concluded
`ValidateLegacyMessage` had no production callers. It has fourteen. Alphabetical
ordering put one package first and `head` truncated the rest. **Truncation
removes evidence of presence, so it biases toward concluding absence — the exact
shape of the claim being tested.** One message away from a false finding against
correct work. Rule recorded.

The developer's conclusion was right; the supporting sentence overstated it
(`ValidateLegacyMessage` checks *sender* at `:80-81`, not recipient). Accepted
with the correction noted, since a correct conclusion resting on a wrong reason
is the harder thing to catch later.

### Disposition

Merged as a fast-forward, `31dfbb414` → `e5b651719`. `ca-msg-p5` retired.
**DEF-160 is now closed.** The ruling's *attention* clause remains unimplemented
and is tracked as DEF-162.

# DEF-160 / DEF-161 review log

Branch under review: `scion/ca-msg-def160fix`. Base `2519aa8b3`.

| Round | Commit | Findings raised | Outcome |
|---|---|---|---|
| 1 | `a7c8fb8a3` | H-1 P4 deferral inverted; H-2 narrowing rejected; tag/CI note | rework |

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

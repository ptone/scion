# DEF-160 — group conversation replies

Status: design, ready to implement. Base `scion/tranche-g` @ `2519aa8b3`.
Supersedes the provisional fix direction I gave ptone for DEF-161 — see §5.

## 1. Problem & goals

An agent has no working syntax for replying into a group conversation. Because
`kind` is a two-value enum (`pkg/ent/schema/conversation.go:50-51`,
`Values("direct","group")`), every **topic** conversation is `kind=group`, so
this covers the primary web chat surface rather than an edge case.

**Contract, ruled by ptone 2026-09-10:**

> *"group conv:id is the recipient. message should require no more than required
> to clearly route. and error clearly with what is required if not met. an agent
> can choose to mention a recipient in message text if they want that recipient
> to be drawn to a group message."*

Success criteria:

1. `scion message conv:<group-uuid> "text"` delivers, with no second addressee.
2. The message is **returned by `handleConversationHistory`** for that
   conversation — not merely persisted.
3. The topic's unread indicator updates.
4. No path writes a message naming a `recipient_id` that is not a participant
   of the conversation it is written against.
5. Refusals name a remediation the caller can actually perform.

## 2. Non-goals

- **Mention→notification wiring (DEF-162).** Required for the ruling's second
  clause; deliberately split. Until it lands a mention is decorative, and that
  is a declared cost, not an oversight.
- **Push notification for group messages.** None exists for *any* author,
  human or agent (Q3a). Reaching parity with human topic messages is the goal;
  exceeding it is not.
- **DEF-163** (hooks assistant-reply mirror). Pre-existing on `main`.
- The `direct` conv-ref path. Fixed by DEF-158.

## 3. The governing principle

**An agent-authored group message should be indistinguishable in row shape from
a human-authored one.**

This is not a new convention. `sendHumanToHuman` (`handlers_chat_v2.go:1437-39`)
already writes, for topic-room messages:

```go
recipient   = "thread:" + key
recipientID = key            // the topic key, NOT a user UUID
```

So the store already has an answer to "what does `recipient_id` mean on a group
message," and it is *the thread key*. The agent outbound path's insistence on a
**user** recipient for group conversations is an asymmetry, not a requirement.

This is the same move DEF-158 made: `sendAgentRouted` already wrote
`Channel:"web"` + `ThreadID:key` together, so the fix matched it rather than
inventing a shape. **Matching an existing writer is cheap to review and cheap to
reason about; inventing a second convention doubles the surface.**

## 4. Proposed design

Three changes in `handlers_agent_messaging.go`, all inside the conv-ref path.

### 4.1 Remove the `case "group":` refusal (`:608`)

Replace the 400 with derivation, mirroring the `case "direct":` arm:

```
case "group":
    // The conversation IS the address (DEF-160 ruling). Derive the row
    // shape the web path writes for topic messages, so agent- and
    // human-authored group messages are indistinguishable downstream.
    threadKey := <topic key from convResult.ExternalRef>
    recipient   = "thread:" + threadKey
    recipientID = threadKey
    def160DerivedGroup = true
```

**The inverse helper does not exist and must be written — this is P0.**

`ConversationResult` (`conversation.go:76-82`) carries only `ConversationID,
ExternalRef, Kind, Surface, DisplayName`. There is no `ThreadID`. DEF-158's
direct arm avoided parsing because for a DM `ThreadID == ExternalRef` (the whole
`dm:…` key). **That shortcut does not exist for groups:** the web path writes
`ThreadID = key` (`handlers_chat_v2.go:1460`) while `external_ref` is
`thread:<projectID>:<key>`, so the two differ by construction and the key must be
recovered from the ref.

Today the only code that decomposes a `thread:` ref is ad-hoc `TrimPrefix` at
`divergence.go:425-428`. `ThreadConversationExternalRef` (`derive_key.go:119`) is
**forward-only**, and its own comment is the reason that matters:

> *"MUST be used by every call site that needs the `thread:<projectID>:<threadID>`
> string … Two independent fmt.Sprintf calls producing the same format string is
> precisely the defect DEF-156 is fixing."*

DEF-156 centralised the construction of this format. Adding an ad-hoc
*de*construction is the same defect in mirror image: change the format later and
the forward helper gets updated while the hand-rolled split silently keeps
parsing the old shape. **So add `ParseThreadConversationExternalRef(ref)
(projectID, threadID string, err error)` beside the forward helper in
`derive_key.go`**, with:

- required `thread:` prefix; `SplitN(ref, ":", 3)`; three parts, none empty.
  `SplitN` with n=3 is deliberate — it keeps a `threadID` that itself contains a
  colon intact.
- **a `dm:`-prefixed threadID refused**, mirroring the forward helper's existing
  refusal (`derive_key_test.go:738`). A `dm:` key must never round-trip through
  the thread path.
- **round-trip golden vectors shared with the forward helper.** Both directions
  over one vector table, so the pair cannot drift.

If the ref does not parse, **refuse** — do not repair, do not fall back. Same
rule as `validDMKey` on the DM path, and for the same reason.

### 4.2 Set `ThreadID` and `Channel`

Reuse the DEF-158 block's shape, extended to the group arm:

- `ThreadID` ← the topic key. **This is what makes the unread dot work:**
  `messagebroker.go:595` calls `TouchTopicActivity(ctx, storeMsg.ThreadID, …)`,
  so an empty ThreadID means no dot, on a message that is otherwise correct.
- `Channel` ← `messaging.SurfaceToChannel(convResult.Surface)`, the deterministic
  derivation added by DEF-158. Refuse on error; do not default to `""`.

**Do not broker-validate a surface-derived channel.** That was regression G-4 in
the DEF-158 review: validation is for caller-supplied values, and applying it to
a self-derived one turns a broker-less hub into a 503. Read `[^176]` before
touching the validation order.

### 4.3 DEF-161: no group message may name a user recipient

With §4.1, the group path writes `recipientID = threadKey` unconditionally, so
a caller-supplied user recipient must not survive into the row.

**Ruling: on a group conv-ref, a supplied `recipient` is ignored and overwritten
by the thread key.** Not rejected — see §5 for why. Log at INFO when one was
supplied and discarded, so the behaviour is discoverable.

The `direct` half of DEF-161 is separate and stays open: `conv:<direct-uuid>`
plus an explicit recipient who is not in the DM key. That one **must be
rejected**, because for direct conversations the DM key *is* the ACL and is
derivable. Validate the supplied recipient against `ParseDMKey(external_ref)`;
mismatch is a 400. Do not silently overwrite on the direct path — a wrong
recipient there is an authorization-shaped error, not a shape mismatch.

## 5. Alternatives considered

**(a) Validate the supplied recipient against the conversation's participant
set. REJECTED — and this was my own provisional direction until I read the
code.** `resolve.go:512-517` states the constraint explicitly: *"Participants
are a LISTING concern, not an access concern: authorization is key-derived (the
DM key IS the ACL), not participant-derived."* Participant writes are
best-effort with failures swallowed as warnings that "self-repair on the next
message." Gating sends on that table converts a deliberately-lossy listing into
an access gate and turns every listing gap into an outage — precisely what the
G2 exception was written to prevent. It also contradicts the standing rule that
a `Conversation` must never become the authority for participant membership.

**(b) Reject `conv:<group>` + explicit recipient outright.** Cleaner to state,
and my first instinct after the ruling. Rejected on blast radius: the DEF-142
suite uses seven group conversations and *always* supplies
`Recipient: "user:"+email` alongside `ConversationRef`. Outright rejection turns
13 passing tests red at once, mixing a contract change into a security fix and
making the diff hard to review. Overwriting achieves the same row shape while
leaving those tests meaningful. Revisit once DEF-162 lands and the CLI has no
way to supply one.

**(c) Leave the recipient as supplied and rely on the backfill skip.** Rejected.
`backfill.go:299` derives participants from `recipient_id`; the only thing
preventing participant injection is an idempotency predicate in another package
(`:177`) marked nowhere as security-relevant. Depending on it is depending on an
accident.

**(d) Invent per-participant fan-out for groups.** Rejected as out of scope and
as exceeding parity — no group push notification exists for human authors
either. Building one for agents first would make agent messages louder than
human ones.

## 6. Migration / rollout

No schema change, no migration, no new switch — the ruling requires a single
cutover and we already carry two switches too many.

Behaviour change is **additive**: a path that returned 400 now succeeds. Nothing
that works today stops working, with the single exception of a caller supplying
a user recipient alongside a group conv-ref, which is currently unexpressible
through the CLI and reachable only by hand-crafted HTTP.

## 7. Open questions

- **OQ-160-1.** Do topic conversations reliably carry a parseable
  `thread:<projectID>:<topicID>` `external_ref`? DEF-100/156 history says empty
  and malformed refs exist in the wild. **The refusal path in §4.1 must be
  exercised by a test, not assumed unreachable.**
- **OQ-160-2. CLOSED — they agree.** `handlers_chat_v2.go:1460` sets
  `storeMsg.ThreadID = key`, and `key` is the same value passed to
  `ResolveOrCreateThreadConversation` that yields
  `external_ref = thread:<projectID>:<key>`. So `messagebroker.go:595`
  (`storeMsg.ThreadID`) and `:3424` (`key`) are passing the same thing, and the
  ref's third component is exactly what `TouchTopicActivity` wants. No
  discrepancy; the dot will fire on a correctly-parsed key.

## 8. Implementation phases

- **P0** — `ParseThreadConversationExternalRef` in `pkg/messaging/derive_key.go`,
  with shared round-trip golden vectors and the `dm:` refusal. Self-contained,
  no caller yet; lands and reviews on its own.
- **P1** — group arm derivation + `external_ref` parse-or-refuse. Tests for both.
- **P2** — `ThreadID` + `Channel` derivation, reusing the DEF-158 block.
- **P3** — DEF-161 group half: overwrite + INFO log.
- **P4** — DEF-161 direct half: validate against `ParseDMKey`, 400 on mismatch.
- **P5** — error-text pass: the `group[...]` `conv:` rejection
  (`pkg/messages/message_group.go:178`) and any remaining text naming
  `user:<email>` as a remedy. Every refusal must name a remedy the caller can
  perform.

## 9. Acceptance criteria

- **AC-1** `conv:<group-uuid>` with **no** recipient delivers 200.
- **AC-2** The message is **returned by `handleConversationHistory`**. Assert on
  the query result, not the stored row — that distinction is the whole of
  DEF-158 and a persistence-only assertion would have passed before that fix.
- **AC-3** Stored row has non-empty `Channel` and `ThreadID`, and
  `recipientID == threadKey`.
- **AC-4** `TouchTopicActivity` fires (the unread dot updates).
- **AC-5** A supplied user recipient on a group conv-ref is discarded; the row
  still carries `recipientID == threadKey`.
- **AC-6** A supplied recipient **not in the DM key** on a `direct` conv-ref is
  **rejected 400**.
- **AC-7** An unparseable group `external_ref` is **refused**, not repaired.
- **AC-7a** `ParseThreadConversationExternalRef` round-trips every vector in the
  forward helper's golden table, **from one shared table**, and refuses a
  `dm:`-prefixed threadID. A separate table for each direction would let the pair
  drift, which is the failure the helper exists to prevent.
- **AC-8** DEF-138/140/141/142/152/156/158 all green. **DEF-142 owns the edited
  lines — run it first and name it in the report.**
- **AC-9** Mutation-tested in both directions, with the whole fix reverted as
  one of the rows. A test that survives every mutation has not been shown to
  test anything.

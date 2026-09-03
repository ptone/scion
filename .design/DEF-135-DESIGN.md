# DEF-135 — Delivery envelope omits the conversation id on broker inbound

**Status:** design, blocking gteam acceptance
**Author:** ca-msg-arch
**Date:** 2026-09-02
**Measured at:** `scion/tranche-g` = `43be4baad776a92d65246fa7fe952d9d3b111077`

---

## Problem & Goals

ptone, on the Discord envelope: *"i was expecting a conversation id."*

Measured, both halves:

- The envelope delivered to agent `smith` for a Discord-originated message carries
  `timestamp, from, to, kind, intent, msg`. There is **no `conversation` key**.
- The same messages are persisted with `conversation_id = b2fd01b6`.

The system knows the conversation. The envelope does not carry it.

**The cause is ordering, not omission.** In `handlers_broker_inbound.go`:

| line | step |
|---|---|
| 243 | Phase 11: resolve conversation — **only if** the caller supplied *both* `Surface` and `ExternalRef`. Sets `preDispatchConvResult`. |
| 292 | `now := time.Now().UTC()` — arrival timestamp |
| 303 | `brokerInboundMsgID := api.NewUUID()` |
| 304-311 | **render envelope** with `ConvResult: preDispatchConvResult` |
| 316 | **dispatch to agent** |
| 340 | resolve `senderUserID` |
| 352 | build `storeMsg` |
| 375-420 | **Phase 5 dual-write: resolve the DM or thread conversation** |
| 441 | persist |

The Discord plugin supplies neither `Surface` nor `ExternalRef`, so Phase 11 does not
run and `preDispatchConvResult` is nil at line 304. The conversation is resolved at
line 375+, **after** the envelope was built and after the message was already
dispatched. The renderer then correctly omits a key it has no value for.

**The renderer's honesty is not the bug and must not be changed.** Omitting beats
fabricating; it is what made this legible rather than silently wrong. The bug is that
the renderer is called before the value exists.

### Goals

- G1. An agent receiving a message that belongs to a conversation receives that
  conversation's id in the envelope.
- G2. No path fabricates, guesses, or defaults a conversation id. Absent stays absent
  where genuinely absent.
- G3. No change to which conversation a message is persisted into.

### Success criterion

A Discord-originated DM to an agent delivers an envelope containing
`"conversation": {...}` naming the same conversation id the row is persisted with.

---

## Scope: this is one site, not a systemic gap

Five of the ten `RenderDeliveryText` call sites can pass a nil `ConvResult`. I checked
all five. **Four are deliberate and correct, and must not be "fixed":**

| site | why nil is right |
|---|---|
| `notifications.go:398` | agent-to-agent state-change notifications — no persisted row, no conversation exists |
| `server.go:2973` | scheduler deliveries — no persisted row, no conversation exists |
| `handlers_agent_messaging.go:1898` | broadcasts deliberately skip conversation resolution (this is the 990-broadcast bucket in the gteam decomposition) |
| `handlers_agent_messaging.go:2067` | mention fan-out — the parent conversation **is** resolved and is deliberately withheld, because the mention target may not be a participant. Stamping it would disclose the existence of a conversation the recipient has no access to. |

That last one is load-bearing for this design. It establishes the existing rule:
**a conversation id is stamped only when the recipient is a participant.** The fix
below respects it — on the broker inbound path the recipient is the agent, and the
agent is by construction a participant in the DM or thread conversation being
resolved.

**Only `handlers_broker_inbound.go:307` is defective**, and only in the case where
Phase 11 did not run.

---

## Non-Goals

- Changing the renderer's omit-when-absent behaviour. It is correct.
- Changing the conversation *kind* Discord messages land in. Today they are DMs
  between the sending user and the agent; they stay DMs. See Alternative B.
- The mention fan-out group case (`:2067`) — a real open question, out of scope here.
- Backfilling `conversation` into envelopes already delivered. Envelopes are not
  retained; there is nothing to backfill.
- The API DTO vocabulary (DEF-133). Separate defect, separate surface.

---

## Proposed Design — hoist resolution above the render

Move sender resolution and the Phase 5 conversation-resolution block from lines
340–420 to **above** the render at line 304. Nothing in either block depends on the
dispatch having occurred:

- `senderUserID` derives from `req.Message.SenderID` / `req.Message.Sender` and a
  store lookup.
- Phase 5 resolution consumes `req.Message.ThreadID`, `agent.ProjectID`,
  `senderUserID`, `agent.ID`, `s.store`, `s.webChatStore`.

Neither reads anything produced by `dispatchWithBrokerRetry`.

### Resulting order

```
  validate
  Phase 11 resolve  (surface + external_ref)      -> convFromSurface
  now, brokerInboundMsgID
  resolve senderUserID
  Phase 5 resolve   (thread, else DM)             -> convFromPhase5
  effectiveConv := precedence(convFromSurface, convFromPhase5)   // see below
  render envelope with effectiveConv
  dispatch
  build storeMsg with effectiveConv
  divergence logging + consistency check
  persist
```

### Precedence rule (must be explicit, not incidental)

Today the two resolutions are independent and can both produce a result: Phase 11
stamps `Metadata["conversation_id"]`, Phase 5 stamps `storeMsg.ConversationID`, and
nothing reconciles them. For Discord only Phase 5 runs, so the conflict is latent
rather than live — but the hoist puts both values in scope at one point and the
design must say which wins.

**Rule: Phase 11 wins when present.** An explicit `surface` + `external_ref` from the
plugin is a caller assertion about where this message belongs; a derived DM key is an
inference. The explicit statement outranks the inference.

**The persisted `storeMsg.ConversationID` and the rendered envelope must be the same
value.** That is the whole point — a single `effectiveConv` computed once and used by
both. This is stronger than today, where the two could in principle disagree with no
detection. Assert it in a test rather than trusting the shared variable.

### Pseudocode

```go
// after Phase 11, before render
senderUserID := resolveSenderUserID(ctx, req, s.store)

var convFromPhase5 *messaging.ConversationResult
if !req.Message.Broadcasted {
    convFromPhase5, err = resolvePhase5Conversation(ctx, ...)  // thread, else DM
    if err != nil && s.writeDenyEnabled() {
        messaging.WriteDenialMetrics.Inc("broker.dm")   // or .thread
        writeError(w, 409, ErrCodeConversationNotResolved, ...)
        return          // NOTE: now returns BEFORE dispatch — see below
    }
}

effectiveConv := convFromPhase5
if preDispatchConvResult != nil {
    effectiveConv = preDispatchConvResult    // explicit beats inferred
}

if s.writeDenyEnabled() {
    req.Message.DeliveryText = messaging.RenderDeliveryText(messaging.RenderDeliveryInput{
        MessageID:  brokerInboundMsgID,
        ConvResult: effectiveConv,           // nil stays nil, and stays omitted
        Msg:        req.Message,
        CreatedAt:  now,
    })
}
```

### Two consequences, both stated deliberately

**1. Write-deny 409s now fire before dispatch instead of after. This is an
improvement, and it is a behaviour change.**

Today a conversation-resolution failure under write-deny returns 409 at line 402/414
— *after* `dispatchWithBrokerRetry` already delivered the message. The agent has the
message and the caller has a failure, so a client retry double-delivers. After the
hoist, a 409 means nothing was delivered and a retry is safe.

It is still a change: messages that today are delivered-then-409'd will instead be
refused. That direction is fail-closed, consistent with the standing rule that the
messaging switches fall back to refusal, and consistent with *under-granting is
recoverable, over-granting is not*. It must be called out in the tranche notes as
tracked drift rather than discovered later.

**2. A dispatch failure can now leave a conversation with no messages.**

Resolution writes. Hoisting it above dispatch means a conversation row may be created
for a message that is then never delivered and never persisted. This is acceptable
because DM and thread conversations are **resolved by deterministic key** — the next
message from the same user to the same agent resolves the identical conversation and
uses it. An empty DM conversation is inert and self-healing, not an orphan requiring
cleanup. Cost is a row; benefit is the envelope contract. Stated so it is a decision
rather than a surprise.

---

## Alternatives Considered

### Alternative B — have the Discord plugin declare `surface` + `external_ref`

Phase 11 would then run and the envelope would carry a conversation with no
reordering at all. **Rejected as the fix** — but see the correction below, because
the reason I originally gave was wrong.

> ### ⚠️ CORRECTION (2026-09-03) — the original rejection reasoning was false
>
> This section originally read: *"Discord messages would begin landing in **group**
> conversations keyed on the Discord ref, instead of the DM conversations they land
> in today… It splits each user's history at the deploy boundary — prior messages in
> a DM, subsequent ones in a group."*
>
> **The DM half of that is false.** ptone, 2026-09-03, verbatim: *"messaged via
> channel. there is no DM option via the discord integration."* The Discord
> integration has **no DM surface at all**. Discord messages therefore always carry
> a channel id as `thread_id`, always take the Phase 5 *thread* branch, and have
> never landed in a direct conversation. There was no DM history to split.
>
> This was confirmed empirically the same day: the gteam AC-9 capture (message
> `3e718a5a`) resolved to group thread conversation `0c57b491`,
> `external_ref: thread:a3083e98:1532505776013312133` — **not** to the direct
> conversation `b2fd01b6`.
>
> **The rejection still stands, for a different and narrower reason.** Phase 11 keys
> group conversations as `discord:chan:<X>`; the Phase 5 thread branch keys them as
> `thread:<projectID>:<X>`. Alternative B would therefore still have split history
> at the deploy boundary — but **group-to-group**, not DM-to-group. Same conclusion,
> different mechanism, and G3 is still violated.
>
> **Why this is recorded rather than quietly edited:** the false premise came from my
> own topology note describing `b2fd01b6` as "holds the Discord traffic." That label
> is almost certainly wrong (it is likely web-chat traffic; provenance confirmation
> pending from instance-investigator). A rejected alternative is read by future
> implementers as settled ground, so a rejection resting on a bad premise is worse
> than no rejection at all — it forecloses an option for a reason that would not
> survive contact. Rules 1046 and 1051.

That is a change to persisted data semantics, not a rendering fix. It splits each
user's history at the deploy boundary — prior messages under one conversation key,
subsequent ones under another — and it would do so on a live instance holding
production data. It also violates G3.

There is a genuine product question underneath it (*should a Discord channel be a
group conversation?*), and it may well be yes. It is not this defect, and answering
it by accident while fixing an envelope field would be the wrong way to decide it.

### Alternative C — render the envelope after persist

Cleanest ordering on paper. **Rejected: dispatch consumes `DeliveryText`.** Rendering
after persist means dispatching after persist, which inverts this handler's declared
dispatch-precedes-persist design (the "Decision 4 declared gap"). That inversion has
its own consequences — a persist failure would block delivery entirely — and is a
much larger change than the defect warrants.

### Alternative D — deliver, then send a follow-up envelope carrying the id

**Rejected outright.** Two envelopes for one message is worse than a missing field:
it makes the receiving agent's message count wrong and gives it two objects to
reconcile for one event.

### Alternative E — leave it, document the omission

**Rejected.** ptone marked it blocking, and correctly: an envelope that cannot name
its own conversation cannot be replied to by id, which is most of the purpose of
having conversation ids in the contract.

---

## Migration / Rollout

Pure code reordering within one handler. No schema change, no migration, no
backfill, no new setting.

The change is entirely inside the `s.writeDenyEnabled()`-gated region plus the
resolution blocks that already exist. It rides the existing envelope switch — **no
new switch**, per the standing single-cutover directive.

Reversible: revert the commit. Nothing persists in a new shape.

---

## Open Questions

- **OQ-135-1.** Should the pre-dispatch 409 (consequence 1) be reported to ptone as
  tracked interim drift before merge, or is it self-evidently the better behaviour?
  *My read: it is better and it is drift; report it, do not ask permission.*
- **OQ-135-2.** The Phase 11 / Phase 5 precedence rule is currently unexercised —
  no live caller sets `surface` and has a thread. Should the implementation assert
  the two agree when both are present and log a divergence when they do not, rather
  than silently preferring one? *My read: yes, log it — it is free and this is
  exactly the class of latent conflict that surfaces two releases later.*

---

## Implementation Phases

Commit-sized, in order.

- **P1 — extract, no behaviour change.** Pull sender resolution into
  `resolveSenderUserID(...)` and the Phase 5 block into
  `resolvePhase5Conversation(...)`, called from their current positions. Suite must
  be green with zero behavioural delta. Reviewable as a pure refactor.
- **P2 — hoist.** Move both calls above the render. Compute `effectiveConv` with the
  precedence rule. Feed it to both the render and `storeMsg.ConversationID`. This is
  the commit that changes behaviour and it should be small enough to read in one
  sitting.
- **P3 — tests.** See acceptance criteria. Includes the mutation checks.
- **P4 — tranche note.** Record the pre-dispatch-409 drift and the empty-conversation
  consequence in the tranche-g notes.

---

## Acceptance Criteria

A reviewer or QA agent must verify:

1. **AC-1.** A broker-inbound DM (no `surface`, no `external_ref`, `senderUserID` and
   `agent.ID` both present) delivers an envelope containing `conversation` whose id
   equals the `conversation_id` the message is persisted with. **Assert equality of
   the two values, not merely that the key is present.**
2. **AC-2.** A broker-inbound message with `thread_id` set delivers an envelope whose
   conversation id equals the persisted thread conversation id.
3. **AC-3.** A **broadcast** still renders with no `conversation` key. Absent must
   stay absent; this is the regression guard on G2.
4. **AC-4.** When `surface` + `external_ref` are supplied *and* a Phase 5 conversation
   would also resolve, the envelope and the persisted row both carry the **Phase 11**
   id (precedence rule).
5. **AC-5.** Under write-deny, a conversation-resolution failure returns 409 **and
   the dispatcher was never called.** Assert the dispatcher was not invoked — a
   status-code assertion alone passes against the current post-dispatch behaviour and
   would not detect a regression of the hoist.
6. **AC-6.** The four principled `nil` sites are untouched. `grep` for `ConvResult: nil`
   must still return `notifications.go`, `server.go`, and both
   `handlers_agent_messaging.go` sites.
7. **AC-7 — mutation checks.** Each must **compile** and then **fail** a test:
   - revert the hoist (render before resolution) → AC-1 fails
   - invert the precedence rule → AC-4 fails
   - stamp a conversation onto the broadcast path → AC-3 fails
8. **AC-8.** Full `./pkg/hub/... ./pkg/messaging/...` green, excluding the two known
   environmental failures (`TestDeleteStopped_RequiresGroveContext`, `pkg/config`).
9. **AC-9 — live check on gteam.** After deploy, a Discord message to an agent
   produces an envelope carrying a `conversation` id, and that id matches the row.
   This is the criterion ptone is actually waiting on.

# DEF-141 — Routing provenance: distinguish a caller's assertion from the hub's derivation

Status: **design ready**, not staffed.
Defect: `DEFECTS.md` [^81]. Root-cause narrative: `IMPLEMENTATION-STATE.md` §5nz.
Base: `scion/tranche-g` @ `eb59ea98f`.

---

## Problem & Goals

`handleAgentOutboundMessage` resolves a conversation two different ways:

- **Explicit branch** — the caller sent `req.ConversationID`; the handler authorizes it and honours it (`handlers_agent_messaging.go:340-421`).
- **Derivation branch** — the caller sent nothing; the handler derives a key from the message fields and resolves it (`:422-470`).

Both then run the same unconditional propagation (`:485-487`):

```go
if convResult != nil {
    structuredMsg.ConversationID = convResult.ConversationID
}
```

That propagation is correct and load-bearing — it is what closed the double-resolution defect ([^71]). The fault is that the broker then reconstructs the *provenance* of that value from a single bit:

```go
// messagebroker.go:473
if msg.ConversationID != "" {
    // "the handler already authorized the assertion (P-2)"   <- false in one branch
```

Non-emptiness answers **"has this already been resolved upstream?"**. The broker reads it as **"did the caller name this?"**. Those are different questions, and the discriminator that separates them is never sent.

Two consequences, both to measurement rather than delivery:

1. `LogExplicitRouting` and the `explicit_routes` counter fire on derivations. That counter is published on the admin board with a caveat saying it measures **adoption** ([^77]). Its floor is now "every outbound agent→user message", so it measures nothing.
2. The `else` arm containing `ComputeDivergenceMatch`/`LogDivergence` is unreachable for every derived outbound agent→user message, so those messages contribute no `comparisons`.

**Goals.** (G1) `explicit_routes` counts caller assertions and nothing else. (G2) The board can answer "are agents adopting explicit routing?" with a number that can go down. (G3) No change to delivery, persistence, or which conversation any message lands in.

## Non-Goals

- **This does not fix AC-12.** A reply carrying no thread context still derives a DM. That is [^69]'s mechanism, untouched here, and its remedy is DEF-142 plus agent skill work — not this change.
- No change to `ComputeDivergenceMatch`'s internals. DEF-139 settled that it stays tautological-but-honestly-documented ([^75]).
- No backfill. The counters are per-replica and reset on restart ([^74]); there is no history to correct.

## Proposed Design

### 1. Carry provenance beside the id

Add one field to `messages.StructuredMessage` (`pkg/messages/types.go:127`):

```go
// ConversationAsserted records that ConversationID was NAMED BY THE CALLER
// and authorized, rather than derived by the hub from message fields.
// Hub-internal provenance: it is never rendered into the agent envelope and
// never accepted from request JSON. Consumers must branch on this, never on
// ConversationID != "" — non-emptiness only means "already resolved upstream".
ConversationAsserted bool `json:"conversation_asserted,omitempty"`
```

Safe to add here, verified rather than assumed:

- The publish hop is in-process Go (`PublishUserMessage`, `messagebroker.go:253`), not gRPC. The proto `StructuredMessage` (`proto/broker/v1/broker.pb.go:39`) is a different surface and does not even carry `conversation_id`; it is not on this path and must not be touched.
- The agent-facing envelope is `DeliveryText`, rendered by `render_delivery.go`. A new struct field does not reach it unless the renderer is changed, and it must not be.
- `StructuredMessage` is slated for deletion in Phase 13. This field dies with it; that is a point in favour, not against.

### 2. Set it in exactly one branch

In `handleAgentOutboundMessage`, a local `asserted bool` is set `true` only after the authorization switch passes in the explicit branch (adjacent to `storeMsg.ConversationID = req.ConversationID`, `:414`). The derivation branch never touches it. The propagation site becomes:

```go
if convResult != nil {
    structuredMsg.ConversationID = convResult.ConversationID
    structuredMsg.ConversationAsserted = asserted
}
```

**Provenance is derived from the authenticated path, never accepted from the caller** — the G-1 rule applied to instrumentation. If a client could set `conversation_asserted` in request JSON it could forge the adoption statistic, which is the one thing this change exists to make trustworthy. No DTO gains this field; no unmarshal target may bind it.

### 3. Separate honouring from classifying in the broker

The current single `if` does both jobs. Split them. Honouring stays gated on non-emptiness (unchanged — this is P-3 and must not regress):

```go
if msg.ConversationID != "" {
    storeMsg.ConversationID = msg.ConversationID
    convResult = &messaging.ConversationResult{ConversationID: msg.ConversationID}
} else if msg.ThreadID != "" {
    // ... unchanged
} else if msg.SenderID != "" && msg.RecipientID != "" {
    // ... unchanged
}
```

Classification becomes a three-way decision:

```go
switch {
case msg.ConversationAsserted:
    messaging.LogExplicitRouting(p.log, storeMsg.ID, storeMsg.ConversationID)

case msg.ConversationID != "":
    // Handler-derived and propagated. Deliberately NOT compared:
    // ComputeDivergenceMatch would take both sides from the same input
    // fields in the same request, so the verdict is tautological (DEF-139,
    // [^72]/[^73]). Counting it as a "match" would inflate the board with
    // confirmations that confirm nothing. CheckConversationConsistency
    // below is the independent check and runs on every path regardless.
    messaging.LogDerivedRouting(p.log, storeMsg.ID, storeMsg.ConversationID)

default:
    // ... existing ComputeDivergenceMatch / LogDivergence, unchanged
}
```

Note what the middle arm is *not*: it is not a restoration of the divergence comparison. Restoring a comparison that cannot fail would raise `comparisons` while adding no information — the precise error [^74] exists to warn against.

### 4. Make adoption a ratio

`LogDerivedRouting` is new and mirrors `LogExplicitRouting`, incrementing a new `derived_routes` counter exposed on the admin board alongside `explicit_routes`. This is the part that actually answers the question ptone is asking:

```
adoption = explicit_routes / (explicit_routes + derived_routes)
```

An absolute `explicit_routes` can only go up and reads as progress no matter what happens. A ratio can go down, which is the property that makes it evidence. The board's existing caveat block gains a line stating that `explicit_routes` counts **caller assertions only**, and that the two counters together cover the outbound agent→user path.

## Alternatives Considered

**A. Revert P-3; let the broker re-derive when nothing was asserted.** Rejected. It reintroduces [^71]'s double resolution and the DEF-138 handler/broker split — trading a measurement defect for a correctness defect.

**B. Log routing classification in the handler instead of the broker.** Rejected. DEF-138's AC-6 deliberately moved persistence-time checks to the persistence site to collapse duplicate log lines. The handler does not know whether a broker is present without testing for one, and conditional-on-broker logging is the shape [^71] was filed against.

**C. Infer provenance in the broker by re-reading the conversation row.** Rejected on grounds of impossibility, not cost: a conversation row records what it is, never who named it. A row created by derivation and a row named by a caller are byte-identical. Adds a query and cannot work.

**D. Propagate `ExternalRef` too, and restore `ComputeDivergenceMatch` for the derived path.** Rejected. The comparison it would restore is tautological ([^73]), so the two extra fields buy `comparisons` counts that carry no signal — and an empty `ExternalRef` fed to that matcher is exactly DEF-138's BLOCKER 1, a fix that made the board scream when it was working ([^77]).

## Migration / Rollout

No schema change, no migration, no data touched. The field is additive, in-process, unpersisted, and absent from the agent envelope. Old and new binaries interoperate: a zero value means "not asserted", which is the conservative reading. Fully reversible — reverting the commit restores present behaviour exactly. No switch, consistent with the single-cutover directive.

## Open Questions

- **OQ-141-1** — should `derived_routes` also cover the *inbound* path (`handlers_broker_inbound.go`), so the ratio describes all traffic rather than the outbound leg only? My read: **not in this change.** Inbound messages are never caller-asserted, so the inbound denominator is a constant and adding it only dilutes the ratio we care about. Revisit if adoption reporting is later widened. Non-blocking.

## Implementation Phases

1. **P1** — add `ConversationAsserted` to `StructuredMessage` with the docstring above; add `LogDerivedRouting` and the `derived_routes` counter beside their explicit twins. No call-site changes. Gate green.
2. **P2** — set `asserted` in the handler's explicit branch only; propagate at `:485-487`.
3. **P3** — split honouring from classification in `messagebroker.go`; three-way switch.
4. **P4** — admin board: expose `derived_routes`, update the caveat block.
5. **P5** — tests, including the mutations named in AC-4. Report the full gate: expect 64 ok / 2 fail in a developer container ([^1061] — that list is mine and does not transfer).

## Acceptance Criteria

- **AC-1** — An outbound agent→user message with **no** `conversation_id` in the request increments `derived_routes`, does **not** increment `explicit_routes`, and emits no divergence line. Asserted in a test, not by reading code.
- **AC-2** — The same message with an authorized `conversation_id` increments `explicit_routes` only.
- **AC-3** — The message lands in the same conversation as before the change, in both cases. This change moves no message.
- **AC-4 (mutation, mandatory)** — each of these must produce a **semantic** test failure with the build green, and a clean restore. A build failure is not a caught violation ([^76]):
  - set `asserted = true` in the derivation branch → AC-1 fails;
  - revert the broker's classification to `if msg.ConversationID != ""` → AC-1 fails;
  - drop the `structuredMsg.ConversationAsserted = asserted` propagation → AC-2 fails.
- **AC-5** — No DTO, request struct, or unmarshal target binds `conversation_asserted`. Enforced by a test that POSTs `{"conversation_asserted": true}` with no `conversation_id` and asserts `explicit_routes` did not move.
- **AC-6** — `ConversationAsserted` does not appear in any rendered agent envelope. Assert against `DeliveryText` for both branches.
- **AC-7** — `CheckConversationConsistency` still runs and its return is still consumed on every path; the DEF-139 AC-8 structural guard still reports the same call-site count.

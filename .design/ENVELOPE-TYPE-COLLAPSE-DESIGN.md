# Design — Collapse envelope `kind`+`intent` into a single `type` field

**Status:** DRAFT, ready for review. Requested by ptone 2026-09-11 (relayed via
`chat-admin-lead`), sized and investigated same day.

## 1. Problem & Goals

The JSON envelope every agent receives (the `---BEGIN SCION MESSAGE---` block)
currently carries two top-level fields where ptone wants one:

```json
"kind": "text",
"intent": "request"
```

**Goal:** collapse these into a single field, `"type"`, with exactly two
values: `"message"` and `"event"`.

**Success criterion:** every currently-delivered envelope still parses and
conveys the same operationally meaningful information (is this a message to
read, or an event that happened), with one field instead of two, and no
agent-visible regression in what they can act on.

## 2. Non-Goals

- Not touching `Conversation.Kind` (`"direct"` / `"group"`) — a completely
  separate field, on a nested object, describing the conversation's own
  shape. Confirmed distinct: this is `ConversationInfo.Kind` in
  `pkg/messaging/delivery.go:29`, unrelated to the top-level field this
  design changes. (This distinction is worth stating explicitly because
  ptone asked about both of these in two separate, earlier conversations,
  and conflating them would be a real mistake.)
- Not changing the *persisted* representation of a message. As established
  in §4, there isn't one to change — see below.
- Not removing the internal `Intent` concept from the Go type system. It
  stays as an internal decomposition detail (§4.4); only its *wire
  visibility* to agents changes.
- Not touching the legacy `messages.StructuredMessage.Type` (the old
  8-value enum) or anything that reads/writes it directly — that remains
  the actual internal representation throughout `pkg/hub` (§4.1).

## 3. What already exists — verified, with lines

This section is unusually load-bearing for this design: the central finding
is that the thing ptone is asking to change is a **rendering-time
computation**, not a stored value, and that materially simplifies the fix.

| Fact | Evidence |
|---|---|
| The internal message representation throughout `pkg/hub` is still the *old* `messages.StructuredMessage`, with its 8-value `Type` string field. | Confirmed by absence: zero production constructions of `pkg/messaging.Message` anywhere outside `pkg/messaging` itself. |
| The `kind`/`intent`/`event` split is a **newer, richer decomposition of that same old `Type` field**, computed fresh at render time. | `MapLegacyType` (`pkg/messaging/envelope_compat.go:38`), doc comment: *"The old 8-value type enum mixes four concerns (provenance, intent, lifecycle, delivery artifact) into a single field. This function decomposes them."* |
| The decomposition is **never persisted**. It exists only inside `Message`, a value constructed transiently per delivery. | `MapLegacyEnvelope` (`envelope_compat.go:131`) builds a `*Message` from a `*messages.StructuredMessage` on every call; nothing stores the result. |
| The wire format agents actually receive is `DeliveryEnvelope`, not `Message`. | `pkg/messaging/delivery.go:34-48`. `Kind`/`Intent`/`Event` are copied from `Message` at `delivery.go:76-78`. |
| There is exactly **one shared rendering entry point** for every hub send path. | `RenderDeliveryText` / `RenderDeliveryTextWithLookup`, `pkg/messaging/render_delivery.go:47/108`. Doc comment: *"the single shared rendering entry point for all hub send paths (Phase 9b(ii))."* Confirmed 10 call sites across 6 files in `pkg/hub` (`notifications.go`, `handlers_chat_v2.go`, `handlers_broker_inbound.go`, `messagebroker.go`, `server.go`, `handlers_agent_messaging.go`). |
| `Intent` (`inform`/`request`/`question`) is **not read by any production server-side logic**. | Repo-wide grep for `.Intent`/`IntentInform`/`IntentRequest`/`IntentQuestion` outside `pkg/messaging` and tests: zero hits. Its only consumers are `envelope_compat.go`'s own bidirectional mapping functions. |
| The *reverse* direction that actually needs `Intent` — reconstructing the old `Type` from `Kind`+`Intent` — has **zero production callers**. | `NewEnvelopeToLegacy` / `mapNewTypeToLegacy` (`envelope_compat.go:294/355`): defined and tested, called from nowhere outside `envelope_compat.go` and its own test file. |

**The consequence of this chain:** changing what agents *see* on the wire
requires changing exactly one struct (`DeliveryEnvelope`) and the one
function that populates it (`FormatNewDelivery`). It does not require
touching persistence, the internal `Message`/`MessageKind`/`TextIntent`
types, `MapLegacyType`'s decomposition logic, or any of the 10 call sites in
`pkg/hub` — they all go through the one shared renderer already.

### 3.1 Current values, for completeness

`MessageKind` (`envelope.go:26-30`): `KindText = "text"`, `KindEvent = "event"`.
`TextIntent` (`envelope.go:50-55`): `IntentInform`, `IntentRequest`,
`IntentQuestion` — populated only when `Kind == KindText`, `nil` otherwise
(mutual exclusivity enforced in `Message.validateStructural`).

## 4. Proposed design

### 4.1 The mapping

| `Kind` | `Intent` | New `Type` |
|---|---|---|
| `KindText` | any of the three | `"message"` |
| `KindEvent` | (always nil) | `"event"` |

Four valid `(Kind, Intent)` combinations collapse onto two `Type` values.
This is intentionally lossy on the wire — that is the literal request — and
§3's findings establish that nothing downstream currently depends on the
lost distinction.

### 4.2 The wire change

`DeliveryEnvelope` (`pkg/messaging/delivery.go:34-48`):

```go
// (pseudocode — illustrative)
type DeliveryEnvelope struct {
    Timestamp    string            `json:"timestamp"`
    Conversation *ConversationInfo `json:"conversation,omitempty"`
    From         string            `json:"from"`
    To           []string          `json:"to,omitempty"`
    Type         string            `json:"type"`              // "message" | "event"
    Event        *EventBody        `json:"event,omitempty"`   // Type == "event"
    Msg          string            `json:"msg"`
    Urgent       bool              `json:"urgent,omitempty"`
    Attachments  []string          `json:"attachments,omitempty"`
    ReplyTo      *string           `json:"reply_to,omitempty"`
}
```

`Kind` and `Intent` fields removed from this struct. `Event` stays — it is
already gated on `Type == "event"` in spirit (mirrors today's `Kind ==
KindEvent` gating) and carries information (`EventBody.Type`, e.g.
`"agent.state-changed"`) that genuinely is not redundant with the two-value
`Type` field.

### 4.3 The computation

`FormatNewDelivery` (`delivery.go:63-82`), the only place that constructs a
`DeliveryEnvelope`:

```go
// (pseudocode — illustrative)
env := DeliveryEnvelope{
    ...
    Type:  typeString(msg.Kind),
    Event: msg.Event,
    ...
}

func typeString(k MessageKind) string {
    if k == KindEvent {
        return "event"
    }
    return "message"
}
```

### 4.4 What does NOT change

`Message.Kind` and `Message.Intent` (`pkg/messaging/envelope.go`) **stay as
they are**. Three reasons:

1. `MapLegacyType`'s decomposition already produces them as a natural
   intermediate step from the old `Type` string — removing them would mean
   re-deriving "message vs event" from the 8-value legacy enum a second,
   different way, duplicating logic DEF-156's whole family of fixes exists
   to prevent.
2. If a future feature ever needs the finer distinction internally
   (e.g., a different notification sound for a question vs. an
   instruction), the capability is already there and costs nothing to keep.
3. It keeps this change strictly scoped to the rendering boundary, which is
   the actual boundary ptone's request is about — he described what he
   *sees*, not Scion's internal Go types.

`ConversationInfo.Kind` is untouched — different field, different struct,
different meaning (§2).

The legacy reverse-compat path (`NewEnvelopeToLegacy`, `mapNewTypeToLegacy`)
is untouched. It has no callers today; this design doesn't need it to
change, and speculatively "fixing" unreachable code to match a wire format
nothing produces would be scope creep with no test to prove it right.

## 5. Alternatives considered

**A. Rename `Kind`→`Type` in the `Message` struct itself, propagate
everywhere.** Rejected. This is the "obvious" reading of the request but it
is a much bigger, riskier change for no additional benefit: it touches the
internal decomposition type used by `MapLegacyType`/`MapLegacyEnvelope`,
their tests, and anything that pattern-matches on `KindText`/`KindEvent`
internally (a handful of sites in `envelope_compat.go` and its tests). None
of that surface is visible to ptone or to agents. Changing it doesn't serve
the stated goal and multiplies the diff for no behavioral difference at the
one boundary that matters.

**B. Keep `Intent` on the wire, nested under a `message` sub-object only
when `Type == "message"`, so the information isn't lost, just relocated.**
Considered because it seems like a "free" way to avoid the lossy collapse.
Rejected: ptone was specific — two values, `message` or `event` — and he
asked for a design that surfaces anything not mapping cleanly rather than
silently working around it (§6 below is that surfacing). Given §3's finding
that nothing consumes `Intent` today, relocating it preserves a distinction
nobody reads, at the cost of not doing what was actually asked. If a real
consumer of `Intent` turns up later, this alternative is cheap to build
then, with a real requirement driving its shape instead of a guess now.

**C. Do the collapse at the `Message` level (per Alternative A) AND update
the reverse legacy-compat path to work from the collapsed shape.** Rejected
for the same reason as A, compounded: it would require inventing a default
`Intent` for legacy reconstruction (since the fine-grained signal is gone),
which is exactly the kind of "guess to fill a gap" this project has a
standing preference against. Not needed since nothing calls the reverse
path (§3).

## 6. What doesn't map cleanly — surfaced, not hidden

Requested explicitly in the brief: flag any `(kind, intent)` combination that
doesn't collapse cleanly.

**Answer: none do, semantically — but the collapse is still lossy, and
that's worth stating rather than letting "no combination is a special case"
imply "nothing is lost."** All three text intents map to `"message"`
uniformly; there's no leftover case that needs special handling. The loss is
uniform (every message loses its inform/request/question distinction on the
wire), not partial. Whether that uniform loss is acceptable is exactly the
product decision ptone already made in requesting this — recorded here so
it's explicit rather than assumed.

## 7. Migration / rollout

**No data migration.** Nothing persists `Kind`/`Intent`/`Type` (§3) — this
is a pure code change to a rendering function. The change takes effect the
moment the new binary runs; there is no backfill, no dual-read/dual-write
period, and no interaction with the switch-consolidation work elsewhere in
this refactor.

**Rollback:** revert the commit. No cleanup needed — no state to unwind.

**Deployment:** per ptone's instruction, this needs to reach the gteam test
VM once implemented, through the same `tranche-g` → `instance-investigator`
update-and-restart path used for DEF-168/DM-sync/visibility-removal.

## 8. Open questions

- **OQ-ETC-1** Should `"type": "message"` be the JSON value for *all* text
  intents, or would ptone prefer a different string (e.g. matching some
  existing convention elsewhere)? Assumed `"message"` since that's the exact
  word he used. **Not blocking** — cheap to change if wrong.
- **OQ-ETC-2** Is there a non-`pkg/hub` consumer of `DeliveryEnvelope`'s JSON
  shape I haven't found — e.g., a harness, SDK, or external tool that
  parses `"kind"`/`"intent"` from a captured transcript rather than through
  this repo's own code? I checked this repo exhaustively; I cannot check
  external consumers I don't have visibility into. **Worth one confirming
  question to ptone before merge, not before design.**

## 9. Implementation phases

- **P1.** Change `DeliveryEnvelope`'s `Kind`/`Intent` fields to a single
  `Type string` field (§4.2). Update `FormatNewDelivery` to compute it
  (§4.3). Update existing tests in `delivery_test.go` that assert on the old
  fields.
- **P2.** Sweep for any other test or fixture across the repo that asserts
  on `"kind"`/`"intent"` in a rendered delivery envelope (as opposed to the
  internal `Message` struct, which is unchanged) and update to assert
  `"type"` instead.
- **P3.** Mutation-test the two-value collapse: construct a `KindText`
  message with each of the three intents and confirm `Type == "message"` for
  all three (a mutation that returns `"event"` for any text intent must go
  red); construct a `KindEvent` message and confirm `Type == "event"`.
- **P4.** Deploy to gteam via `instance-investigator`, per ptone's
  instruction. Confirm via a live send that the delivered envelope shows
  `"type"` and not `"kind"`/`"intent"`.

## 10. Acceptance criteria

- **AC-1** A text message (any intent) renders `"type": "message"` in the
  delivered envelope, with no `"kind"` or `"intent"` key present.
- **AC-2** An event message renders `"type": "event"`, with the existing
  `"event"` object still present and unchanged in shape.
- **AC-3** `ConversationInfo.Kind` (`"direct"`/`"group"`) is unaffected —
  a test asserting this explicitly, since it's the field most likely to be
  confused with the one this design changes.
- **AC-4** All 10 existing `pkg/hub` call sites into
  `RenderDeliveryText`/`RenderDeliveryTextWithLookup` continue to compile
  and their existing tests pass unmodified in behavior (only the asserted
  JSON shape changes, not the call sites themselves — confirming the
  single-choke-point claim in §3 is actually true).
- **AC-5** The internal `Message.Kind`/`Message.Intent` fields and
  `MapLegacyType`'s decomposition logic are untouched — a diff-scope check,
  not just a behavioral one.
- **AC-6** Deployed to gteam and confirmed live, per ptone's instruction.

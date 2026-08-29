# Tranche D — Phase Plan

Validation choke point. Written 2026-08-29, cut from `upstream/main` @ `dbec308cc`.

Scope per `messaging-em9-rederivation.md:177`: *"Phase 3 — Tranche D: validation choke
point. `validate*.go` (4 files), the hub integration test, `VALIDATION_EXEMPTIONS.md`,
and D's hunks in `cmd/message.go`."*

---

## 0. Ground truth — measured on `upstream/main`, not assumed

| File | Lines |
|---|---|
| `pkg/messaging/validate.go` | 182 |
| `pkg/messaging/validate_compat.go` | 110 |
| `pkg/messaging/validate_test.go` | 763 |
| `pkg/messaging/validate_compat_test.go` | 456 |
| `pkg/messaging/VALIDATION_EXEMPTIONS.md` | 58 |

`ValidateLegacyMessage` is the choke point. **8 production call sites:**

| Site | ConversationID set before validate? |
|---|---|
| `cmd/broadcast.go:108` | no |
| `cmd/message.go:561` | no |
| `cmd/message.go:638` | no |
| **`cmd/message.go:716`** | **set at `:715`, but DROPPED before the check — see correction below** |
| `pkg/hub/handlers_agent_messaging.go:272` | no (attribution at `:305`) |
| `pkg/hub/handlers_agent_messaging.go:635` | no |
| `pkg/hub/handlers_broker_inbound.go:228` | no (attribution at `:363`) |
| `pkg/hub/handlers_chat_v2.go:1142` | no (attribution at `:1179`) |

> **CORRECTION, 2026-08-29 (after D1 was written and sent).** The row above
> originally read "YES — `:715` sets it from `resolveResp`", and §0 below
> originally said the check was dead on *7 of 8* paths and live on the 8th.
> **That was wrong. It is dead on 8 of 8.**
>
> `cmd/message.go:715` sets `ConversationID` on the **legacy**
> `messages.StructuredMessage`. `ValidateLegacyMessage` then converts via
> `MapLegacyEnvelope` (`pkg/messaging/envelope_compat.go:126`), and that mapper
> **never reads `old.ConversationID`** — the field does not appear in the file at
> all, and the `&Message{...}` literal it returns omits it. So `newMsg.ConversationID`
> is empty here exactly as it is everywhere else, the sentinel fires, and the check
> is neutralised.
>
> Verified by execution, not by reading: a probe test on `upstream/main` set
> `ConversationID` on the legacy struct, called `MapLegacyEnvelope`, and asserted
> the value survived. It **failed** — `mapped ConversationID = ""`. The negative
> grep over `envelope_compat.go` was positive-controlled against
> `validate_compat.go` (4 hits), so the zero is real and not a mistyped path.

### The defect, stated precisely (DEF-41)

`ValidateMessage` (`validate.go:46`) requires `ConversationID`. The legacy adapter
cannot supply one, because on 7 of 8 paths validation runs *before* attribution.
So `validate_compat.go:94-97` fabricates one:

```go
if newMsg.ConversationID == "" {
    newMsg.ConversationID = "legacy-pending"
}
```

**Verified consequences (measured):**

1. **The sentinel never escapes.** `"legacy-pending"` occurs exactly once in the
   tree. It is written to `newMsg`, a fresh object of a *different type* from the
   caller's `*messages.StructuredMessage`, so it cannot alias back. The function
   returns only `error`. Nothing corrupt is persisted; no backfill is owed.
2. **The check is dead on 8 of 8 paths** (corrected — originally recorded as 7 of 8).
   `conversation_id is required` cannot fire wherever the sentinel is applied, and
   the sentinel is applied everywhere, because the mapper never carries a
   ConversationID through. It has never gated a legacy send, on any path.
3. **`cmd/message.go` still has the mirror ordering defect — for a different
   reason than first recorded.** It resolves the conversation over the hub API at
   `:690`, *then* validates at `:716`. Resolution can create a conversation row
   (`resolveResp.Created`). If validation then fails — on body, sender, metadata
   size, or attachment count, *any* of the live checks — the row survives with no
   message. Call this **DEF-48** (orphaned conversation on resolve-then-validate).
   The defect does not depend on the ConversationID check being live here; it never
   was. It is small and non-security, but it is the same ordering bug from the
   other side, and D should not fix one and leave it.
4. **The id is not lost on the wire, only to the validator.** `SendStructuredMessage`
   transmits the original legacy struct as JSON, and `conversation_id` is a
   serialised field, so the resolved id does reach the server. Nothing is
   mis-persisted. What is lost is only the *check*.

The two halves together define the correct shape: neither validate-then-attribute
nor attribute-then-validate is right on its own.

### Consequence for DEF-49 (recorded here because it constrains that fix)

`cmd/message.go:715` is the **only** production writer of a caller-supplied
`conversation_id` on an outbound legacy message, and it is legitimate. So the
DEF-49 fix at `handlers_agent_messaging.go:889` **cannot be "reject
caller-supplied conversation_id"** — that would break the `scion message @agent`
reference path. It has to be an authorization check: does the authenticated
sender belong to the conversation it names? Whoever owns DEF-49 inherits this
constraint.

---

## 1. Proposed design

**Split the choke point by phase, so that each check runs where its input exists.**

```
  ┌─ shape/content validation ─┐   ┌─ attribution ─┐   ┌─ attributed validation ─┐
  │ sender, body, metadata,    │   │ ResolveOr     │   │ ConversationID present  │
  │ attachments, addressees,   │ → │ Create…       │ → │ and well-formed         │
  │ reply_to_id, size limits   │   │ Conversation  │   │                         │
  └────────────────────────────┘   └───────────────┘   └─────────────────────────┘
        cannot see a conv id             creates it          can finally check it
```

Interface sketch — illustrative, not production:

```go
// ValidateLegacyMessage keeps its name and its 8 call sites. It performs
// every check that does not depend on attribution. The sentinel is deleted.
func ValidateLegacyMessage(msg *messages.StructuredMessage) error

// New. Called after attribution has set a real ConversationID.
// This is where "conversation_id is required" finally becomes reachable.
func ValidateAttributed(msg *messages.StructuredMessage) error
```

`ValidateMessage` (the native-type validator) keeps its `ConversationID` check
unchanged — native callers already have one. Only the legacy adapter stops
asserting a field it cannot know.

### Why not the alternatives

**Alt 1 — reorder: attribute first, then validate once.** This is what
`cmd/message.go:715` already does, so it has precedent. Rejected: attribution
performs store I/O and creates rows. Running it ahead of shape validation means
unvalidated input reaches the database, which inverts the choke point's purpose.
DEF-48 is the small visible symptom of exactly this; generalising it to all 8
sites would multiply it.

**Alt 2 — drop the `ConversationID` requirement from `ValidateMessage`.**
Cheapest possible change. Rejected: it deletes the check rather than making it
live, and it weakens the native path, which today is the only one where the check
works. This is DEF-41 renamed, not fixed.

**Alt 3 — keep the sentinel but make it a typed marker with a persistence-time
assertion.** Narrow, and could land standalone ahead of D. Rejected as the
*primary* fix — it makes the workaround safer instead of removing the ordering
problem — but see Phase D1, which borrows its assertion as a guard rail.

---

## 2. The load-bearing risk: the reachability gate

`hack/check-authz-reachability.sh` (lines 85-101) asserts `ValidateLegacyMessage`
appears in three handler files. **Splitting the choke point in two while the gate
watches only one half means a handler can call the pre-half, skip the post-half,
and stay green.** That is a gate that no longer covers the thing named in its own
test — the failure mode this project has hit repeatedly.

Therefore: **the gate change lands in the same commit as the split, never after.**
The gate must assert both halves on every path that attributes.

Per brief item 12, re-pointing a gate is not the changing team's call. I am
designing this change as architect and flagging it here; the manager implements it
as specified and does not re-scope it. Any deviation is an escalation to me.

---

## 3. Also in scope, found while sizing

- **`VALIDATION_EXEMPTIONS.md` is stale.** It is written in the future tense about
  a landed event: *"will be exempt … when C5 (hub handler wiring) lands"* and
  *"Until C5 lands, these emitters are not on main."* C5 has landed. The three
  documented exemptions were verified still accurate against main
  (`notifications.go`, `messagebroker.go`, and the mention fan-out contain no
  `ValidateLegacyMessage`), so only the framing is wrong — but this is exactly the
  standard C7 was held to: **docs describe the shipped contract, not the design.**
- **`ValidateMessage` becomes callerless once D1 lands.** On main it has exactly
  one production caller — the legacy adapter. D1 repoints that caller at
  `validateMessageContent`, leaving `ValidateMessage` exported, tested, and called
  from nowhere, with a doc comment (*"Every rule has a corresponding test that
  fails when the rule is removed"*) that reads as though it were load-bearing.
  This is **not** a reason to hold D1: D1 replaces one neutralised check with
  three live ones and is strictly better than main. But it means D3 now has *two*
  dead exported validators to resolve in `validate.go`, not one. Either give
  `ValidateMessage` a caller or delete it; do not leave a third misdescribing
  wrapper behind while removing the first two.
- **`ValidateMessageAddressees` is dead code.** Zero production callers. Its own
  doc comment says *"Callers that have a store and addressees should call this,"*
  implying they do. Only the raw `ValidateCrossProjectAddressees` is called, from
  one site (`handlers_agent_messaging.go:683`, mention fan-out). Either wire it or
  delete it; do not leave a wrapper that misdescribes the system.
- **Not a hole, checked:** project isolation is enforced in *authorization*
  (`pkg/hub/authorize_message.go:186`, "cross-project agent-to-agent messaging
  denied"), not in the validator. The validator's cross-project check covers the
  one case authorization cannot see — mention fan-out expanding the addressee set
  after authorization ran on the primary recipient. This is correct as designed
  (rule 29: authorization checks belong in authorization). No change.

---

## 4. Non-goals

- **The Tranche G read-switch.** B10 stands: derivation failures remain non-fatal.
  D must not make any derivation or resolution failure reject a request.
- **DEF-32** (federated identity link table). Required before G, not before D.
- **DEF-42** (`webChatStore` locking) — `pkg/hub`, not D's surface. Separate owner.
- **§2.6.3** (`Conversation` vs `webchat_topic` as parallel constructs) stays open.
- **Tranche H** stays blocked on the `omitempty` evasion.

---

## 5. Phases

| Phase | Content |
|---|---|
| **D1** | **DEF-41.** Delete the sentinel. Add `ValidateAttributed`. Wire it at the 7 attribute-after-validate sites. Update `check-authz-reachability.sh` to assert both halves **in this same commit**. |
| **D2** | **DEF-48.** Fix `cmd/message.go:715-716` ordering so a failed validation cannot leave an orphaned conversation. Covers D's hunks in `cmd/message.go`. |
| **D3** | Rewrite `VALIDATION_EXEMPTIONS.md` to the shipped contract, present tense. Resolve `ValidateMessageAddressees` (wire or delete). Note DEF-37's marker gate as still-unimplemented rather than "tracked." |
| **D4** | Hub integration test for the choke point: prove the `conversation_id` check now fires on a legacy path where it previously could not. |

D1 is the tranche. D1 must be its own PR.

### Phase dependencies (revised after the mapper correction)

- **D2 is independent of D1** and can land in either order. D1 touches 8 files;
  `cmd/message.go` is not among them, so there is **zero file overlap**. The
  earlier belief that hoisting validation above the resolve would destroy "the
  only live ConversationID check" was a consequence of the 7-of-8 error — there
  was no live check there to lose. D2 branches from `upstream/main` directly.
- **D3 is partly blocked on D1.** The `VALIDATION_EXEMPTIONS.md` rewrite is
  independent, but the dead-validator cleanup depends on D1's end state, since
  `ValidateMessage` only becomes callerless once D1 lands. Sequence D3 after D1
  merges, or split the doc half out.
- **D4 is blocked on D1** — it tests D1's behaviour.

---

## 6. Acceptance criteria

- **AC-D-1** — `grep -r "legacy-pending"` over the tree returns nothing.
- **AC-D-2** — ~~A test that reaches attribution with no conversation id and
  asserts rejection, shown failing against main.~~ **WITHDRAWN AS UNSATISFIABLE
  (rule 418).** B10 forbids rejecting a request when attribution *fails*, and
  when attribution *succeeds* the id is non-empty by construction, so no
  production input can drive the new check to its error branch. Demanding
  before/after evidence for a newly added function is also incoherent: the
  baseline is not red, it is uncompilable. **Replaced by:** a written
  proof-by-enumeration in the commit message, naming every site that assigns the
  attributed id and showing each is non-empty by construction, so the guard is a
  contract assertion and not a reachable rejection.
- **AC-D-3** — `check-authz-reachability.sh` fails when either half of the split
  is removed from any watched handler. **Demonstrate by removing ONLY the call
  expression and leaving all surrounding prose in place (rule 419.)** Deleting an
  enclosing block, comments included, is not a valid demonstration: these gates
  are bare `grep -q "$symbol" "$file"` substring searches, so a comment
  *mentioning* the symbol satisfies them. The two demonstrations must be
  call-only removals.
- **AC-D-4** — A validation failure on the `scion message @agent` reference path
  leaves no conversation row behind.
- **AC-D-5** — `VALIDATION_EXEMPTIONS.md` contains no future-tense claim about a
  landed phase, and each listed exemption is re-verified against main at review
  time, not inherited from this document.
- **AC-D-6** — No derivation or resolution failure was converted into a request
  rejection (B10). Reviewer confirms by inspection, not by test pass.
- **AC-D-7** — Per-file endpoint deletion counts reported before push, with
  line-by-line justification for every deleted assertion. A deleted assertion must
  name its coverage successor by test name, not be justified by a comment.

---

## 7. Standing constraints for the D manager

Inherited unchanged from the Tranche C brief — rebase on upstream main and re-check
`comm -12` overlap before every push; never `git add -A`; managers push their own
branch and merging is the architect's gate; report only at phase boundaries.

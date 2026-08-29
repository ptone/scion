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
| **`cmd/message.go:716`** | **YES — `:715` sets it from `resolveResp`** |
| `pkg/hub/handlers_agent_messaging.go:272` | no (attribution at `:305`) |
| `pkg/hub/handlers_agent_messaging.go:635` | no |
| `pkg/hub/handlers_broker_inbound.go:228` | no (attribution at `:363`) |
| `pkg/hub/handlers_chat_v2.go:1142` | no (attribution at `:1179`) |

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
2. **The check is dead on 7 of 8 paths.** `conversation_id is required` cannot fire
   wherever the sentinel is applied. It has never gated a legacy send.
3. **On the 8th path the check is live — and that path has the mirror defect.**
   `cmd/message.go` resolves the conversation over the hub API at `:715`, *then*
   validates at `:716`. Resolution can create a conversation row. If validation
   then fails, the row survives with no message. Call this **DEF-48** (orphaned
   conversation on resolve-then-validate). It is small and non-security, but it is
   the same ordering bug from the other side, and D should not fix one and leave it.

The two halves together define the correct shape: neither validate-then-attribute
nor attribute-then-validate is right on its own.

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

D1 is the tranche. D2-D4 are small and may combine into one PR if the manager
finds them under review size; D1 must be its own PR.

---

## 6. Acceptance criteria

- **AC-D-1** — `grep -r "legacy-pending"` over the tree returns nothing.
- **AC-D-2** — **Positive control, mandatory.** A test constructs a legacy message
  that reaches attribution with no conversation id and asserts the request is
  *rejected*. This test must be shown FAILING against `main` before the fix and
  passing after. A green run alone does not discharge this criterion — the check
  it exercises is currently unreachable, so "passes" is the status quo (rule 405).
- **AC-D-3** — `check-authz-reachability.sh` fails when either half of the split is
  removed from any watched handler. Demonstrate by deleting each half in turn and
  showing the gate red. Two demonstrations, not one.
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

# DEF-133 / DEF-143 — one vocabulary across the message surfaces

Status: **design ready**, not staffed.
Defects: `DEFECTS.md` [^54] (DEF-133, REST DTO speaks the old vocabulary), [^83] (DEF-143, the envelope's conversation field is indistinguishable from decoration).
Base: `scion/tranche-g` @ `eb59ea98f`. All line references measured against that tree.

---

## Problem & Goals

The project brief is "two agent-level messaging interfaces with one clear, crisp, coherent semantic contract". The delivery envelope was migrated to the new vocabulary. Nothing else was. What is actually shipping today is **three structs, three vocabularies and two case conventions**, all live on the same hub for the same row.

Measured, not inferred:

| concept | `store.Message` (REST DTO) | `messaging.Message` (internal) | `messaging.DeliveryEnvelope` (agent wire) |
|---|---|---|---|
| body text | `msg` | `body` | `msg` |
| creation time | `createdAt` | `created_at` | `timestamp` |
| sender | `sender` + `senderId` | `from` | `from` |
| addressee | `recipient` + `recipientId` | (separate Addressee list) | `to` |
| classification | `type` | `kind` + `intent`/`event` | `kind` + `intent`/`event` |
| conversation | `conversationId` | `conversation_id` | `conversation` (object) |
| quoted message | absent | `reply_to_id` | `reply_to` |
| surface | `channel` | absent | `conversation.surface` |
| thread | `threadId` | absent | absent |
| project | `projectId` + `groveId` | absent | absent |

`msg` versus `body` and `created_at` versus `timestamp` are **within the same package**, in adjacent files (`pkg/messaging/envelope.go:273`, `pkg/messaging/delivery.go:37`). This is not a legacy-boundary problem that a compatibility shim explains. It is an absence of any single place where the vocabulary is decided.

Three structural facts drive everything below.

**F1 — `store.Message` is the persistence model *and* the API DTO.** `pkg/store/models.go:1717`. There is no DTO layer. The JSON tags on the persistence model are the public wire contract.

**F2 — the client decodes into the same struct.** `pkg/hubclient/messages.go:87` is `type Message = store.Message`, and `:167`/`:186` decode API responses straight into it. Server output type and client input type are one type. They cannot drift, and a rename changes both ends in one edit.

**F3 — there is no published contract.** No OpenAPI or Swagger document exists anywhere in the tree. The consumer set is `pkg/hubclient`, `pkg/sciontool`, and `web/src` — all in-repo.

F2 and F3 together are why a hard rename is even on the table. F1 is why it is currently dangerous: renaming a tag on `store.Message` is a change to a struct whose name says "storage", and the next person to touch it will not expect to be editing an API.

**Goals.** (G1) One name per concept across REST, internal envelope and agent wire. (G2) The persistence model stops being the wire contract, enforced by a test, not by a convention. (G3) An agent that receives a message can reply without consulting any external document (DEF-143). (G4) The change lands as a single cut-over, consistent with the standing ruling that a hub upgraded to this version has every switch already flipped.

## Non-Goals

- Not changing message *semantics*, storage, routing or authorization. This is naming and envelope shape only. If a phase below requires an authorization change, it has escaped scope and must come back to me.
- Not unifying the notification/event DTOs (`pkg/hubclient/notifications.go`). Same disease, different surface, and folding it in triples the review surface for no gain in the messaging contract.
- Not removing the legacy `groveId` alias in the same change — see Rollout.
- Not adding an OpenAPI spec. That is the right long-run answer for F3 and it is a separate project.

## Proposed Design

### 1. A DTO type, distinct from the store model

```go
// pkg/api/message.go — the wire contract, and the only type serialised to
// clients. Field names here are the vocabulary; nothing else defines it.
type Message struct {
    ID           string     `json:"id"`
    Conversation string     `json:"conversation"`      // was conversationId
    From         string     `json:"from"`              // was sender
    To           []string   `json:"to"`                // was recipient
    Kind         string     `json:"kind"`              // was type
    Msg          string     `json:"msg"`
    CreatedAt    time.Time  `json:"created_at"`        // was createdAt
    ReplyTo      *string    `json:"reply_to,omitempty"`// quoted message id
    ...
}

func MessageFromStore(m *store.Message) Message
```

`store.Message` keeps its shape and loses its JSON tags. **G2 is enforced by a test that reflects over `store.Message` and fails if any field carries a `json` tag** — the same shape of gate as the existing consistency-check guard, and for the same reason: a convention nobody can violate accidentally beats a convention everybody agrees with.

This is the load-bearing decision. The renames are consequences of it; the separation is the thing that stops DEF-133 recurring.

### 2. `snake_case` everywhere, decided once

The REST DTO is camelCase, both envelopes are snake_case. Two of the three are already snake_case and both of those are the *new* vocabulary, so the cheapest coherent answer is that the REST DTO moves. This is arbitrary in the way that all convention choices are arbitrary; the point is that it is written down here and a reviewer can cite it.

### 3. `msg` versus `body`, resolved to `msg`

`body` appears in exactly one struct (`messaging.Message`). `msg` appears in the REST DTO, the delivery envelope, and the CLI's own output. Rename `body` to `msg`. Lower blast radius, and it matches what agents already see.

### 4. DEF-143 — the conversation carries its own address

The envelope today renders `"conversation": {"id", "kind", "surface"}` and nothing marks it as a routing directive. An agent must independently know four things to use it, and all four live in a separate document. Smith did not, and used the only addressing form it knew.

The fix is to put the ready-to-use address in the object that already exists:

```json
"conversation": {
  "id": "0c57b491-…",
  "kind": "group",
  "surface": "native",
  "address": "conv:0c57b491-…"
}
```

**`address`, deliberately not `reply_ref`.** The envelope already has `reply_to`, and it is a real feature, not a stub — `pkg/hub/handlers_chat_v2.go:956` populates it from the web UI's quote-a-message control. `reply_to` means *the message being quoted*; the DEF-143 field means *where to send anything at all*. Naming them `reply_to` and `reply_ref` would put two different concepts one character apart in the same envelope, which is the DEF-133 disease being introduced by the fix for it.

`address` is also the right shape on ptone's own stated preference for explicit routing over affinity memory: the information needed to route a reply travels inside the message being replied to, so nothing has to be remembered between messages, and the agent copies a literal rather than constructing one.

Note the field is `conversation.address`, not top-level. A conversation does not span channels, so its address and its surface belong in the same object.

### 5. What this does not fix

An agent still has to know that `conversation.address` goes in the recipient position. Carrying the literal collapses three of the four inference steps DEF-143 identified; the fourth is skill content. Say so plainly rather than claiming the envelope now teaches itself.

## Alternatives Considered

**A. Dual-emit both vocabularies, then drop the old one.** Rejected, and the evidence is in the struct being changed. `store.Message` already carries exactly this pattern: `MarshalJSON` at `pkg/store/models.go:1745` injects a legacy `groveId` alongside `projectId`, unconditionally, no `omitempty`, still shipping. The one prior dual-emit in this file was never dropped. A migration strategy with a demonstrated 0% completion rate at the exact site under discussion is not a strategy. It also directly contradicts the Q2 hard-cutover ruling and the single-cut-over directive (G4).

**B. Rename the tags on `store.Message` in place; no DTO type.** Rejected, though it is much the smallest diff and would satisfy DEF-133 as literally filed. It leaves F1 standing: the persistence model remains the wire contract, so the next storage change is silently an API change. It also cannot be gated — there is no invariant to test, only a habit to maintain. This is the cheap fix that makes the expensive fix harder to justify later, which is the worst position to leave a known structural defect in.

**C. Version the API — `/api/v2/` with the new vocabulary, `/api/v1/` frozen.** Rejected on cost against a benefit that F3 mostly removes. With no published spec and no known external consumer, versioning buys compatibility for a population we have not established exists. It doubles the handler surface permanently and it is the option that is hardest to reverse. Reconsider only if the survey in Open Questions finds real external consumers.

**D. Generate the DTO and the TypeScript client from a schema.** Rejected for now, recorded as the right long-run answer to F3. It subsumes this design and the notification DTOs too, and it is a multi-quarter cross-language change. Proposing it here trades a shippable fix for a plan.

## Migration / Rollout

Single cut-over, per G4. No switch, no dual-emit, no compatibility window inside the hub.

The one real risk is **version skew between the hub and the `scion` CLI inside agent containers.** The CLI is delivered in the container image, so an agent may be running a build older than the hub it is talking to. F2 means the CLI decodes into the same struct it always did: against a renamed hub, every renamed field decodes as its zero value, silently. A stale CLI would print blank senders rather than error.

This has now been measured, and skew is the normal state — see OQ-133-1 and DEF-146 [^86]. gteam runs three distinct builds at once and nothing rebuilds the agent CLI on hub upgrade. **So the DTO gains a version field that is rejected on mismatch, and this is no longer optional.** Silent zero-valued decode is not an acceptable outcome, and "we will rebuild the containers as part of the deploy" is not a sufficient mitigation on its own: it is a procedure, and the procedure has already been shown to be skipped. The design must survive the procedure being skipped again.

Note the sequencing this forces. Rebuilding container binaries closes the *current* gap but does not make future skew impossible, so the version field must land **before or with** P4, not after it as a hardening pass.

`groveId` stays for now. Removing it is a separate, independently reversible commit, and bundling a legacy-alias removal into a rename means a skew failure cannot be attributed to one or the other.

## Open Questions

- **OQ-133-1 — ANSWERED, and the answer is the bad one.** Measured on gteam 2026-09-04, filed as DEF-146 [^86]. Three builds run simultaneously: hub `eb59ea98`, the CLI agents execute `98543da9` (bind-mounted sideload), the image's own CLI `7c12c09a`. Nothing rebuilds the agent CLI on hub upgrade. **Skew is real, unbounded, and routine — not an edge case.** The version field described in Rollout is therefore **required, not contingent**, and P4 must not land without it. Three distinct commits is what makes this a policy finding rather than a coincidence.
- **OQ-133-2 — UNANSWERABLE from available data; recorded as a bounded gap.** gteam has no Caddy access logging configured and the hub logs `user_agent` only for broker auth audit events, so distinct user-agents on `GET /api/v1/agents/*/messages` cannot be enumerated. Enabling either logger is a config change on a live instance, which is not worth making for this question. **The gap is largely neutralised by OQ-133-1's answer:** once the DTO carries a version field and mismatch is a loud rejection, an unknown external consumer fails visibly on upgrade instead of silently decoding zeros. That is the property I actually wanted from the survey. Do not treat this as evidence that no external consumer exists — it is evidence that we cannot tell.
- **OQ-133-3** — should `sender`/`senderId` collapse into a single `from` principal ref, or stay a label/ID pair? The new vocabulary uses one `PrincipalRef`; the DTO uses two fields. Collapsing is more coherent and loses the display label. My read: collapse, and let display labels be resolved by the reader — but this touches the web UI's rendering and I want the UI owner's view before committing.

## Implementation Phases

1. **P1** — `pkg/api/message.go` with the new DTO and `MessageFromStore`. Unused. No behaviour change.
2. **P2** — every hub handler that serialises a message returns the DTO. `store.Message` loses its JSON tags in the same commit; the reflection guard from §1 lands here. This is the commit where a missed serialisation site fails to compile, which is the point.
3. **P3** — `pkg/hubclient` and `pkg/sciontool` decode the DTO; delete the `Message = store.Message` alias at `pkg/hubclient/messages.go:87`.
4. **P4** — `web/src` reads the new names. Gated on OQ-133-1.
5. **P5** — rename `messaging.Message.Body` to `Msg` and `created_at`/`timestamp` to one name. Internal only, no wire effect.
6. **P6** — DEF-143: `conversation.address` populated in `FormatNewDelivery`. Independent of P1–P5 and may land first if the tranche needs a visible win.

## Acceptance Criteria

- **AC-1** — no field of `store.Message` carries a `json` tag. Asserted by reflection. Verify by mutation: add a tag to one field and confirm the test fails.
- **AC-2** — `GET /api/v1/agents/<id>/messages` and the delivery envelope for the same message use the same name for every shared concept. Asserted by a table test enumerating the concepts in the table above, not by spot-checking two fields.
- **AC-3** — the response contains no `sender`, `recipient`, `type`, `channel`, `threadId`, `conversationId` or `createdAt` key. Asserted on the marshalled bytes, not on the struct.
- **AC-4** — `groveId` is still emitted and still equals `projectId`. This phase does not remove it, and a passing suite must prove that rather than leave it untested.
- **AC-5 (DEF-143)** — for a message delivered in a conversation, `conversation.address` is present and is a literal that, passed verbatim as the recipient argument to `scion message`, routes back to that conversation. Asserted end-to-end against a real `pkg/hub` mux, not against a mock — a mock cannot disagree with the server about what the address means.
- **AC-6** — `reply_to` still carries the quoted message ID and is unchanged by this work. The risk being tested is that `address` is wired into the wrong field.
- **AC-7** — no test was weakened to accommodate the rename. Reviewer checks the diff for assertion deletions, not just for green.

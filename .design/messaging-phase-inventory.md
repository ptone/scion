# Messaging Refactor — Phase Inventory

**Question answered:** "what other work in this refactor is planned but not done — I want to
get the entire refactor done and tested, not just to some hybrid state."

**Source of truth for the plan:** `/workspace/.design/messaging-conversation-model.md:1671`
(the 13-phase table).

**Branch measured:** `refs/tmp/tg` (= `scion/tranche-g` at time of writing).
**Deployed on gteam:** `93916ca2` — tranche-g as of the last deploy.

**Evidence tags:** MEASURED = I ran the command and read the output.
INFERRED = reasoned from adjacent evidence, not directly observed.

**The completeness test used throughout:** a symbol with only test callers is NOT done.
Phase 9 is the case that makes this test matter.

---

## Summary table

| # | Phase | Status | Blocking defects |
|---|---|---|---|
| 1 | Schema | **DONE** | — |
| 2 | Store layer | **DONE** | DEF-99 (postgres path untested) |
| 3 | Resolution | **DONE** | — |
| 4 | Backfill | **DONE** (run on gteam) | DEF-29 (1 keyless `direct` row remains) |
| 5 | Dual-write | **DONE** | — |
| 6 | Envelope | **PARTIAL** — types live, but only as a validation intermediate | — |
| 7 | Validation choke point | **DONE** — and gated | DEF-41 (documented deferral, not a gap) |
| 8 | Read switch | **DONE, DEFECTIVE IN PRODUCTION** | **DEF-100** (in flight, `ca-msg-h3`) |
| 9 | Delivery formatter | **BUILT, ZERO PRODUCTION CALLERS** | **DEF-101** |
| 10 | CLI split | *pending `ca-msg-inv`* | |
| 11 | Broker edge | *pending `ca-msg-inv`* | |
| 12 | Docs | *pending `ca-msg-inv`* | |
| 13 | Removal | **NOT STARTED** — irreversible, gated on beta | precondition chain below |

**The hybrid state is phases 6, 9, and 13.** Everything else is either finished or has a
named fix already moving.

---

## Phase 1 — Schema

**DONE.** MEASURED.

`pkg/ent/schema/conversation.go`, `pkg/ent/schema/conversation_participant.go`,
`pkg/ent/schema/message_addressee.go` all exist on tranche-g. The tables are live on
gteam (backfill ran against them; the DEF-29 audit queried them).

No migration files under `pkg/store/migrations/` — schema is ent-managed.

---

## Phase 2 — Store layer

**DONE.** MEASURED — `pkg/store/store.go:1611` `ConversationStore` interface, wired into
the main `Store` interface at `store.go:164`.

Surface (15 methods): `CreateConversation`, `GetConversation`, `UpdateConversation`,
`DeleteConversation`, `ListConversations`, `GetConversationByExternalRef`,
`UpsertConversationByExternalRef`, `AddParticipant`, `EnsureParticipant`,
`RemoveParticipant`, `ListParticipants`, `GetConversationsForPrincipal`,
`AddAddressee`, `ListAddressees`, plus `SetMessageConversationID` (`store.go:1332`).

**Open:** DEF-99 — `pgWebChatStore` has no test coverage on this surface. gteam is
sqlite, so this is not a gteam risk; it is a risk for any postgres deployment.
helm default driver is sqlite (`deploy/helm/scion-hub/values.yaml:447`), so postgres is
opt-in and this is not on the critical path to beta.

---

## Phase 3 — Resolution

**DONE.** MEASURED.

- `pkg/messaging/derive_key.go:38` `DeriveConversationKey` — the single sanctioned
  point of key construction.
- `pkg/messaging/derive_key.go:131` `ResolveOrCreateConversationByKey` — the write-side
  sink, including the topic-lookup intercept that prevents shadow conversations for
  native topics.
- `pkg/messaging/resolve.go:63` `ParseReference`, `resolve.go:169` `Resolve`, plus
  `resolveAgentDM` / `resolveEmailDM` / `resolveThread` / `resolveConvByID` and the
  post-resolution authorization hook `checkPostResolutionAuth:219`.

**Caveat carried into Phase 10:** `ParseReference` and `Resolve` are the *grammar* half
of the CLI split. Whether they have non-test callers today is the exact question
`ca-msg-inv` is answering. If they don't, Phase 3's authorization machinery is built but
unexercised, and the same "zero production callers" finding as Phase 9 applies.

---

## Phase 4 — Backfill

**DONE, and executed against gteam.** MEASURED (the run happened; `hub.db.pre-6f6228f6`
is the paired restore point).

- `pkg/messaging/backfill.go:87` `NewBackfillService` / `:97` `Run`.
- `pkg/messaging/dm_migration.go:87` `Run` — the DM rekey/merge pass, with
  `stepRebuildParticipants`, `stepMergeOrRekeyEmptyRef`, `stepRekeyOldFormat`.

**Open:** DEF-29 — gteam still has 1 `direct` conversation with an empty `external_ref`.
A keyless `direct` row has no ACL at all. Staging rows `adf13f87` / `f003ad87` are the
live reproduction and **must not be deleted**.

---

## Phase 5 — Dual-write

**DONE.** MEASURED. Not switch-gated — the write path resolves and stamps
`conversation_id` unconditionally. The only messaging switches that exist are
`conversation_read_switch` (Phase 8) and `conversation_write_deny_switch` (G2)
— `pkg/config/opsettings/sections.go:129-130`. Both are ON on gteam.

Divergence instrumentation shipped alongside: `pkg/messaging/divergence.go`
(`DivergenceCounter`, `SwitchBypassCounter`, `WriteDenialCounter`,
`ComputeDivergenceMatch:264`, `CheckConversationConsistency:359`).

---

## Phase 6 — Envelope

**PARTIAL.** MEASURED, and this is the finding that surprised me.

The new envelope type system is fully built in `pkg/messaging/envelope.go`:
`MessageKind`, `TextIntent`, `EventType`, `PrincipalRef`, `AddressedVia`,
`DeliveryState`, `Visibility`, `EventBody`, `AttachmentRef`, `Message`, `Addressee`,
each with a `Validate`.

**But no production code outside `pkg/messaging` constructs or consumes it.** MEASURED:

```
messaging.Message{  -> NONE
MessageKind         -> NONE
TextIntent          -> NONE
AddressedVia        -> NONE
```
(searched all `*.go` on tranche-g excluding `pkg/messaging/` and `_test`;
the `DeliveryState` and `PrincipalRef` hits that do exist are unrelated `pkg/store`
and `pkg/hub/authz.go` types of the same name.)

The new envelope is reachable in production, but only **internally** and only as a
throwaway: `ValidateLegacyMessage` (Phase 7) calls `MapLegacyEnvelope` to convert the
legacy `StructuredMessage` into a `*messaging.Message`, validates that, and discards it.

**So Phase 6 is a validation vocabulary, not a wire format.** Nothing is stored in the
new shape and nothing is delivered in it. Finishing Phase 6 means making the new
envelope the thing that actually crosses a boundary — which is the same work as
Phase 9 on the outbound side, and Phase 10/11 on the inbound side.

---

## Phase 7 — Validation choke point

**DONE, and gated against regression.** MEASURED — this is the strongest-evidence phase
in the whole refactor.

`pkg/messaging/validate_compat.go:29` `ValidateLegacyMessage` is the choke point. It
runs the legacy-specific invariants (closed type enum, `thread_id` requires `channel`,
channel charset/length, metadata limits, non-empty body unless attachments, sender
required), then converts via `MapLegacyEnvelope` and runs the *new* validators
(`validateMessageContent`, `ValidateAddressees`).

**10 non-test call sites** MEASURED:

| File | Lines |
|---|---|
| `cmd/broadcast.go` | 108 |
| `cmd/message.go` | 561, 638, 703, 734 |
| `pkg/hub/handlers_agent_messaging.go` | 275, 663 |
| `pkg/hub/handlers_broker_inbound.go` | 226 |
| `pkg/hub/handlers_chat_v2.go` | 1142 |

**And it is enforced by a build gate.** `hack/checksecuritymarkergates/main.go:527`
walks every function that calls a server-generated message constructor and fails the
build (`FAIL [DEF-37]`) unless that function either calls `ValidateLegacyMessage` or is
on an explicit exempt list with a written justification
(`pkg/messaging/VALIDATION_EXEMPTIONS.md`). It also detects *stale* exemptions.

This means the choke point cannot silently regress. That is the property we wanted.

**DEF-41 is a documented deferral, not a gap:** `ValidateLegacyMessage` deliberately
skips `ValidateAttributed` (the `conversation_id` presence check), because at validation
time the attribution layer has not run yet. Noted in-source at
`validate_compat.go:92-94` and `cmd/message.go:698`.

---

## Phase 8 — Read switch

**DONE, AND CURRENTLY BROKEN IN PRODUCTION.**

`ConversationReadSwitch` is defined at `pkg/config/opsettings/sections.go:129`, read at
`pkg/hub/operational_settings.go:1172`, and gates four read sites:
`handlers_chat_v2.go:1810`, `handlers_chat_v2.go:1845`, `handlers_messages.go:76`,
`handlers_messages.go:287`. It is ON on gteam and has been for the whole test window.

**DEF-100** — with the switch ON, opening any web-chat thread returns HTTP 409
`conversation_not_resolved`. Root cause: `ResolveThreadConversationForRead`
(`pkg/messaging/conversation.go:282`) derives a `thread:<project>:<thread>` key and
looks it up by `external_ref`, but native topic conversations are written with
`external_ref=''` **by design** — the write path resolves them through the topic's
`conversation_id`, not through the derived key. The reader was built against a key the
writer never writes.

Fix in flight with `ca-msg-h3`. The fix must live inside
`ResolveThreadConversationForRead`, not at the call sites, because read site #2
(`handlers_messages.go:306`) has no topic handle to fix it with.

**Until this lands, gteam web chat is unusable with the switch on.** Nothing downstream
of Phase 8 can be tested.

---

## Phase 9 — Delivery formatter

**BUILT, ZERO PRODUCTION CALLERS. This is DEF-101 and it is the biggest single gap.**
MEASURED.

`pkg/messaging/delivery.go:27` defines `ConversationInfo` and `delivery.go:36`
`DeliveryEnvelope` — the agent-facing JSON from Appendix B, with a first-class
`conversation` object carrying `id` / `kind` / `surface` / `name` / `participants`.

`FormatNewDelivery` is called by exactly one thing: `FormatLegacyAsNewDelivery`
(`pkg/messaging/delivery_compat.go:62`). That function has **zero non-test callers.**

What agents actually receive is the legacy shape, `pkg/messages/format.go:42`
`deliveryMessage`: `timestamp / sender / recipients / msg / type / urgent /
broadcasted / attachments / channel / thread_id / metadata`. No conversation field of
any kind. Produced by `messages.FormatForDelivery`, called from
`cmd/server_dispatcher.go:261`, `cmd/server_dispatcher.go:271`, and
`pkg/runtimebroker/handlers.go:1711`.

**This is why the envelope you pasted had `channel: "web"` and `thread_id: "815dedd..."`
and no conversation ID.** It is not a bug in Phase 8 and it is not a missing field —
the producer for that field is written and not wired to anything.

**Doing Phase 9 is a visible change to the inbound format of every agent in the fleet.**
It wants its own test cycle, and the open design questions are real (see below).

---

## Phases 10 / 11 / 12

*Pending `ca-msg-inv`. Questions dispatched:*

- **10 (CLI split):** is the conversation a required positional on `scion message`? Do
  `ParseReference`/`Resolve` have non-test callers? Do `scion broadcast` / `scion keys`
  exist as planned? How many flags survive? Is the `conv:` / `@` / `#` grammar
  parseable today?
- **11 (Broker edge):** which plugin inbound paths resolve a conversation? Is spoke
  selection driven by `conversation.surface`?
- **12 (Docs):** GLOSSARY entries; docs-site page; **and specifically whether the agent
  skill documents the legacy or the new envelope.** A skill that documents an envelope
  agents do not receive is worse than no skill.

---

## Phase 13 — Removal

**NOT STARTED. Irreversible.**

Drops `channel`, `thread_id`, `recipient*`, and the old type enum. Design doc marks it
**not reversible**, with two hard preconditions:

1. the beta exercise has passed, and
2. **every replacement named in a deprecation warning has shipped and been exercised**
   (AC-15a).

Precondition 2 is not close. Phase 9 is unwired, and `thread_id` is currently the *only*
routing information an agent receives — removing it before `DeliveryEnvelope.conversation`
is live and exercised would leave agents with no routing information at all.

**Phase 13 is strictly downstream of Phase 9.** There is no ordering in which it lands
first.

---

## Recommended order

1. **DEF-100** (in flight) — unblocks all gteam testing. Nothing else can be tested until
   this ships.
2. **Phases 10 / 11 / 12 triage** — as soon as `ca-msg-inv` reports; these may be
   cheaper than they look, and Phase 12's skill-doc question affects every agent
   immediately.
3. **Phase 9 / DEF-101** — its own tranche, its own test cycle. Completes Phase 6 as a
   side effect on the outbound side.
4. **Beta exercise** — the scheduled, DB-snapshotted run.
5. **Phase 13** — only after 3 and 4, and only as a deliberate irreversible step.

---

## Open design questions Phase 9 cannot start without

Raised serially to the principal, one at a time.

**Q1 — cutover granularity.** Is the switch from legacy `deliveryMessage` to
`DeliveryEnvelope` hub-wide, per-agent, or harness-config driven? Hub-wide is the
simplest and matches the "atomic upgrade path" preference already stated for the
switches; per-agent is safer but means maintaining both formatters indefinitely.

**Q2 — agents whose prompts parse legacy field names.** Any agent instruction text that
says "the message will have a `thread_id`" breaks on cutover. We do not have an
inventory of which prompts do this. Options: (a) ship both keys during a window
(`thread_id` alongside `conversation`), (b) hard cutover and fix prompts reactively,
(c) inventory first.

**Q3 — does `participants` go on the wire?** `ConversationInfo.Participants` is a
membership list. For a `direct` conversation it is trivially the two parties, but for a
`group` it is a disclosure to every recipient. Under "under-granting is recoverable,
over-granting is not" the default should be to omit it unless there is a named consumer.

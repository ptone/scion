# Messaging Refactor — Phase Inventory

**Question answered:** "what other work in this refactor is planned but not done — I want to
get the entire refactor done and tested, not just to some hybrid state."

**Source of truth for the plan:** `/workspace/.design/messaging-conversation-model.md:1671`
(the 13-phase table).

**Branch measured:** `scion/tranche-g` @ `85f25c1a1`.
**Deployed on gteam:** `85f25c1a` — tranche-g head. Verified healthy, both switches ON.

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
| 8 | Read switch | **DONE** | DEF-100 fixed, deployed, CLOSED |
| 9 | Delivery formatter | **BUILT, ZERO PRODUCTION CALLERS** | **DEF-101** |
| 10 | CLI split | **PARTIAL** — `conv:` and `#` refused; `Resolve` has no prod callers | DEF-5, DEF-7 (**AC-15a violation**) |
| 11 | Broker edge | **PARTIAL** — inbound resolves; **outbound routes on `channel`, not `surface`** | new: Phase 11b |
| 12 | Docs | **PARTIAL** — skill accurate; **zero glossary entries**, no docs-site page | — |
| 13 | Removal | **NOT STARTED** — irreversible, gated on beta | precondition chain below |

**The hybrid state is phases 6, 9, 10, 11, 12 and 13.** Phases 1-5, 7 and 8 are finished.

**One item is missing from the 13-phase plan entirely.** Outbound delivery to external
surfaces routes on the legacy `channel` field, not on `conversation.surface`. Phase 13
cannot drop `channel` until that is rebuilt. See Phase 11 below; I am calling it 11b.

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

**Caveat, now measured (see Phase 10):** `ParseReference` has exactly one non-test
caller, `cmd/message.go:150`. **`Resolve` has none.** So the resolution *grammar* is
reachable in production but the resolution *engine* — including `checkPostResolutionAuth`
and `requireParticipant` — is not. Phase 3's authorization machinery is in the same
built-but-dark state as Phase 9. I am leaving Phase 3 marked DONE because the write-side
sink `ResolveOrCreateConversationByKey` is heavily used; the dark half is the CLI-facing
`Resolve` entry point, which is Phase 10's to wire.

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

**DEF-100 — FIXED and CLOSED** (`85f25c1a1`, deployed, previously-409 topic now 200).
The defect was: with the switch ON, opening any web-chat thread returned HTTP 409
`conversation_not_resolved`. Root cause: `ResolveThreadConversationForRead`
(`pkg/messaging/conversation.go:282`) derives a `thread:<project>:<thread>` key and
looks it up by `external_ref`, but native topic conversations are written with
`external_ref=''` **by design** — the write path resolves them through the topic's
`conversation_id`, not through the derived key. The reader was built against a key the
writer never writes.

The fix put the write path's topic-lookup intercept inside
`ResolveThreadConversationForRead` itself (behind a `WithReadTopicLookup` option), rather
than at the call sites — necessary because read site #2 (`handlers_messages.go`) has no
topic handle to fix it with. Read and write now share one resolution rule.

gteam web chat is working and QA is live.

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

## Phase 10 — CLI split

**PARTIAL.** MEASURED (by me, after `ca-msg-inv` stalled and was retired).

**Done:**

- `scion broadcast` and `scion keys` exist as real separate commands —
  `cmd/broadcast.go`, `cmd/keys.go`, each with a test file.
- **8 flags carry deprecation warnings with named replacements**
  (`cmd/message.go:1254-1263`): `--broadcast`, `--all`, `--in`, `--at`, `--plain`,
  `--raw`, `--notify`, `--cc`, plus `--channel` and `--thread-id`.
- The `conv:` / `@` / `#` grammar **parses**. `messaging.ParseReference` is wired at
  `cmd/message.go:150` — one non-test caller, but a real one, on the primary send path.
- Parse-failure-denies is honoured: a malformed `conv:` / `#` / `@` is a hard error and
  does **not** fall through to the legacy path (`cmd/message.go:158-165`).

**Not done, and the gap is sharper than "unfinished":**

- **`conv:<id>` and `#<thread>` are explicitly refused** at `cmd/message.go:154-156`:
  > `conversation reference %q is not yet supported in the CLI; use @<agent-name> to
  > message an agent`

  The in-source reason is honest — they resolve, but delivery routing does not exist, so
  accepting them would silently drop the message. Refusing is the right call. But it
  means only **one of three** reference forms works.
- **`messaging.Resolve` has ZERO non-test callers.** The resolution engine and its
  authorization hooks — `checkPostResolutionAuth`, `requireParticipant` — are built and
  unexercised in production. Phase 3's security machinery is in the same state Phase 9 is
  in.

**This produces a concrete AC-15a violation, and it is in the CLI's own help text.**
`--channel` and `--thread-id` are deprecated with the message *"use conversation
references instead."* The conversation reference that replaces `--thread-id` is
`#<thread>` — which the same binary refuses. **We are telling users to migrate to a form
we reject.** Phase 13's precondition is "every replacement named in a deprecation warning
has shipped and been exercised"; this fails it outright. Tracked as DEF-5 and DEF-7.

Worth noting: the agent skill is **more accurate than the flag help**. It tells agents to
prefer `@` addressing, which works. The flag help says "conversation references," which is
partly false. Fixing the flag strings is a one-line-per-flag change.

---

## Phase 11 — Broker edge

**PARTIAL — and this section contains the most load-bearing finding in the inventory.**
MEASURED.

**Inbound works.** Broker inbound resolves conversations and is surface-aware:

- `pkg/hub/handlers_broker_inbound.go:242` passes `messaging.WithSurface(req.Surface)`
  into key derivation; `:256` calls `ResolveOrCreateConversationByKey`; `:365` handles
  the thread case.
- `pkg/hub/messagebroker.go:472` and `:672` resolve thread conversations.
- `pkg/hub/handlers_agent_messaging.go` 309, 741, 1052 likewise.

So `surface → conversation` is built. **The reverse is not.**

**Outbound spoke selection is driven by `msg.Channel`, not by `conversation.surface`.**
`FanOutEventBus.Publish` (`pkg/eventbus/fanout.go:70`) takes a
`*messages.StructuredMessage` — the *legacy* envelope — and selects the target spoke by
matching `msg.Channel` against each bus's `ChannelID` (falling back to `Name`):

```go
channelKey := buses[i].ChannelID
if channelKey == "" { channelKey = buses[i].Name }
if channelKey == msg.Channel { target = &buses[i] }
```

With `no broker registered for channel %q` as the failure. There is no reference to
`conversation.surface` anywhere in the fan-out path.

**Consequence, and it changes the Phase 13 plan:** `channel` is not merely a field we
deliver to agents. **It is the outbound routing key for every external surface** —
Discord, Slack, Telegram, Teams. Dropping `channel` in Phase 13 without first making
`conversation.surface` the routing key in `fanout.go` would not degrade delivery, it
would end it for all non-native surfaces.

This is a larger dependency than the `thread_id` one I flagged earlier, and it is
**new work not represented in the 13-phase plan.** Call it Phase 11b: make the fan-out
bus select on `conversation.surface`, which first requires `Publish` to take something
that carries a conversation.

---

## Phase 12 — Docs

**PARTIAL.** MEASURED.

**The agent skill is accurate and current — my concern was unfounded.**
`resources/platform_skills/scion-messaging/SKILL.md` (171 lines) documents the legacy
envelope, which is the one agents actually receive, and it is honest about the
transition:

- `conv:<uuid>` is documented as **"Not yet supported — currently errors"** (line 32) —
  which exactly matches `cmd/message.go`.
- `--channel` / `--thread-id` are marked deprecated and point at `@` addressing (lines
  62-63) — the form that actually works.
- Line 130 tells agents to keep discriminating on the `type` field during the transition.

I had flagged this as the highest-risk item in Phase 12 on the theory that a skill
documenting an unreachable envelope would be actively misleading. It does not; it
documents the reachable one. **Withdrawn.**

**One inaccuracy, and it is the one that generated the principal's question.** Line 130:
> New fields such as `conversation_id` may appear in message metadata but are not yet
> required for correct agent behavior.

They do not appear at all — DEF-101. "May appear" is soft enough to be defensible and
firm enough to mislead. One-line correction: say plainly that no conversation field is
delivered today.

**Not done:**

- **Zero glossary entries for the conversation model.** Neither `GLOSSARY.md` nor
  `docs-site/src/content/docs/glossary.md` defines Conversation, external_ref, Addressee,
  Participant, or Surface. The only matches are incidental — the web-chat visibility
  filter labelled "Conversation", and "harness conversation" under Suspend/Resume. Given
  that `external_ref` **is** the ACL for a direct conversation, its absence from the
  glossary is a real gap.
- **No user-facing docs-site page for the conversation model.** The only messaging doc
  outside `.design/` is `docs/messaging-authorization.md`, which is internal.

---

## Phase 13 — Removal

**NOT STARTED. Irreversible.**

Drops `channel`, `thread_id`, `recipient*`, and the old type enum. Design doc marks it
**not reversible**, with two hard preconditions:

1. the beta exercise has passed, and
2. **every replacement named in a deprecation warning has shipped and been exercised**
   (AC-15a).

Precondition 2 is not close, and there are now **three** independent reasons, two of them
found after this document was first written:

1. **Phase 9 is unwired.** `thread_id` is currently the only routing information an agent
   receives. Removing it before `DeliveryEnvelope.conversation` is live and exercised
   leaves agents with no routing information at all.
2. **`channel` is the outbound routing key** (Phase 11). `FanOutEventBus.Publish` selects
   the delivery spoke by matching `msg.Channel`. Dropping `channel` before
   `conversation.surface` replaces it in `fanout.go` ends delivery to every external
   surface. This is Phase 11b and it is not in the plan.
3. **A deprecation warning already names a replacement that does not work** (Phase 10).
   `--thread-id` says "use conversation references instead"; the replacing form
   `#<thread>` is refused by the same binary. AC-15a fails on the CLI's own help text.

**Phase 13 is strictly downstream of Phases 9, 10 and 11b.** There is no ordering in
which it lands first.

---

## Recommended order

0. **DEF-100 — DONE.** Merged `85f25c1a1`, deployed to gteam, acceptance check 200.
   gteam QA is unblocked and running.

1. **Cheap truth-in-labelling fixes, now.** Two one-line-class changes that stop us
   misinforming people while the rest lands:
   - the `--channel` / `--thread-id` deprecation strings, which currently name a form the
     binary refuses;
   - `SKILL.md` line 130, which says a conversation field "may appear" when none is
     delivered. This is what generated the principal's question.
   Neither is gated on anything. Both reduce the blast radius of the hybrid state.

2. **Glossary entries** (Phase 12). Small, unblocked, and `external_ref` in particular
   deserves a definition given that it *is* the ACL for a direct conversation.

3. **Phase 9 / DEF-101** — its own tranche, its own test cycle. Completes Phase 6 on the
   outbound side. Three open design questions below must be answered first.

4. **Phase 11b** — make the fan-out bus route on `conversation.surface`. Sequenced after
   9 because `Publish` must first take something that carries a conversation. This is the
   piece that is missing from the plan; without it Phase 13 is unreachable.

5. **Phase 10 completion** — implement delivery routing for `conv:<id>` and `#<thread>`
   so the refusals at `cmd/message.go:154` can be removed, and wire `messaging.Resolve`
   so its authorization hooks are actually exercised.

6. **Beta exercise** — the scheduled, DB-snapshotted run.

7. **Phase 13** — only after 3, 4, 5 and 6, and only as a deliberate irreversible step.

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

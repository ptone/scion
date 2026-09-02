# Phase 9 design — wiring the delivery envelope

Author: `ca-msg-arch`
Date: 2026-09-02
Status: **for review**
Measured against: `scion/tranche-g` @ `85f25c1a1`
Companion documents: `messaging-conversation-model.md` (the 13-phase plan),
`messaging-phase-inventory.md` (what is actually built), `messaging-defect-ledger.md`.

---

## 1. Problem & Goals

The original brief named two agent-level messaging interfaces: **`scion message` for
sending**, and **structured messages for receiving**. The send side has been reshaped by
Phases 1-8. The receive side has not: agents today receive `pkg/messages.deliveryMessage`,
a flat envelope built around `channel` and `thread_id`, which are the two concepts this
refactor exists to replace.

The replacement — `pkg/messaging.DeliveryEnvelope` — is written, tested, and has **zero
production callers** (DEF-101). Phase 9 is the tranche that wires it.

### Goals

- **G1.** Agents receive `DeliveryEnvelope`. `conversation` is a real object sourced from a
  real `conversations` row, not synthesised.
- **G2.** The cutover is **hub-wide** and rides the **single consolidated messaging switch**,
  which **defaults ON** in the version that ships the completed refactor. No new switch.
- **G3.** After Phase 9, `messages.FormatForDelivery` has no production callers, so Phase 13
  can delete it rather than leaving it permanently reachable.
- **G4.** No agent-visible regression in *what information is available*, only in *where it
  lives*. Every field of the legacy envelope either has a home in the new one or is a
  deliberate, recorded removal (§4.4).

### Success criteria

An agent on a hub running the shipped version receives, for a native thread message, an
envelope whose `conversation.id` is a UUID that `SELECT * FROM conversations WHERE id = ?`
returns a row for; and `pkg/messages/format.go` contains no exported formatter.

---

## 2. Non-Goals

Named explicitly because each has been mistaken for part of this work at least once.

| Not in Phase 9 | Where it belongs |
|---|---|
| Making `FanOutEventBus` route on `conversation.surface` | **Phase 11b** — outbound to Discord/Slack/Telegram still routes on `channel` (`pkg/eventbus/fanout.go:70`). Phase 13 cannot drop `channel` until this lands. |
| CLI delivery for `conv:<id>` and `#<thread>` | **Phase 10 completion** — `cmd/message.go:154` still refuses both. |
| `PromoteDM` conversation-kind transition | **DEF-96** — an ACL transition, needs its own design. |
| Postgres test coverage | **DEF-99** — a project, not a hunk. |
| Making `BackfillService` and `DMMigrationService` auto-run | **§7** — discovered by this design, scoped as its own tranche. |
| Changing what the web UI renders | The 409 empty-panel item is filed and unstaffed. |

---

## 3. Current state, measured

### 3.1 Where formatting happens

`messages.FormatForDelivery` has exactly **three** production call sites:

| Site | Process | Has a store? |
|---|---|---|
| `cmd/server_dispatcher.go:261` (raw path) | **hub** (`agentDispatcherAdapter`) | **yes** — `d.store` |
| `cmd/server_dispatcher.go:271` (normal path) | **hub** | **yes** |
| `pkg/runtimebroker/handlers.go:1711` | **runtime broker** — separate process on the agent host | **no** |

`runtimebroker.Server` (`pkg/runtimebroker/server.go:191`) holds `manager`, `runtime`, hub
connections and three content-addressed caches. **It has no database and no hub store.** It
cannot resolve a conversation.

### 3.2 Which broker this is

Two different things in this codebase are called a broker, and the design depends on not
confusing them.

| | **runtime broker** (`pkg/runtimebroker`) | **message broker proxy** (`pkg/hub/messagebroker.go`) |
|---|---|---|
| What | the process on the agent host that owns tmux sessions and injects text into agents | hub-side bridge that subscribes to external channel topics (Discord/Slack/Telegram) on behalf of agents |
| Where | separate process, reached over HTTP at `brokerEndpoint` | in the hub |
| Store | **none** — `runtimebroker.Server` holds `manager`, `runtime`, hub connections, three caches | **yes** — `MessageBrokerProxy.store store.Store` |
| Formats agent delivery? | **yes**, `handlers.go:1711` | no — it dispatches via `DispatchAgentMessage` |

Everything below about "the broker cannot resolve a conversation" is about the **runtime
broker**. `MessageBrokerProxy` has a store and is not affected by Phase 9.

### 3.3 What already crosses the hub→runtime-broker wire

`messages.StructuredMessage` (`pkg/messages/types.go:141`) **already carries
`ConversationID string \`json:"conversation_id,omitempty"\``** — added by Phase 5's
dual-write.

There are **two** hub→runtime-broker transports, and both send the whole `StructuredMessage`
as JSON:

- `pkg/hub/broker_http_transport.go:326` — `brokerHTTPTransport.MessageAgent`, `POST
  <brokerEndpoint>/api/v1/agents/<id>/message`
- `pkg/hub/controlchannel_client.go:258` — `ControlChannelBrokerClient.MessageAgent`, same
  path over the control channel

Received at `pkg/runtimebroker/types.go:484` (`MessageRequest`), dispatched at
`handlers.go:1279` → `sendMessage` → `FormatForDelivery` at `:1711`.

**Both transports already have a pre-rendered-text fallback**: `reqBody["message"] = message`
when `structuredMsg == nil`. So shipping rendered text to the runtime broker is not a new
capability — the wire supports it today, and `sendMessage` already delivers `req.Message`
verbatim when no structured message is present.

So the conversation **ID** reaches the runtime broker today. `ConversationInfo` needs `kind`,
`surface` and `name` as well, and those require a row lookup it cannot perform.

There is **no `conversation_id` anywhere in `proto/`** — the gRPC surface is the external
channel broker, not this path, so no proto change is implied.

**Correction, recorded rather than silently fixed.** An earlier draft of this section cited
`pkg/hubclient/agents.go:527` and `:579` as the hub→broker wire. Those are **CLI→hub**
(`agentService.SendStructuredMessage`, posting to the hub's own
`/api/v1/agents/<id>/message`), a different hop entirely. The conclusion was unaffected, but
the citation was wrong and the wrong citation would have sent an implementer to the wrong
file.

### 3.4 The delimiters do not change

`FormatNewDelivery` reuses byte-identical framing: `deliveryIntro`, `beginDelimiter`,
`endDelimiter` are the same strings in `pkg/messages/format.go:22-24` and
`pkg/messaging/delivery.go:22-24`. The agent prompt template
`.scion/templates/instance-manager/agents-hub.md` teaches only those delimiters and no field
names. **The frame every agent recognises survives the cutover untouched.**

---

## 4. Proposed design

### 4.1 The load-bearing decision: which process renders the envelope

This is the only decision in Phase 9 that is expensive to reverse, because it determines
whether the broker keeps knowing about message formats.

**Chosen: render at the hub. The broker stops formatting.**

The hub already resolves the conversation on the send path — that is what Phases 3, 5 and 7
built, and `handlers_agent_messaging.go` is one of the ten `ValidateLegacyMessage` sites. It
therefore has the conversation row in hand at the moment it dispatches, with no extra query.
The hub renders the envelope text and ships it; the broker delivers bytes.

```
  send path (hub)                                    broker              agent
  ───────────────                                    ──────              ─────
  authorize ─► resolve conversation ─► persist
                      │
                      ├─► ConversationInfo{id,kind,surface,name}
                      │
                      └─► FormatNewDelivery(msg, addrs, convInfo, opts)
                                      │
                                      └─ delivery_text ──────► manager.Message ──► tmux
```

**This is a smaller change than it first appears**, because §3.3 established that both
hub→runtime-broker transports already carry a pre-rendered-text field and `sendMessage`
already delivers it verbatim. The obvious move — send `reqBody["message"]` instead of
`reqBody["structured_message"]` — is *nearly* right and fails on one detail:
`sendMessage` computes `isRaw := req.StructuredMessage != nil && req.StructuredMessage.Raw`,
so dropping the structured message silently loses `Raw` and with it the send-keys path.

Hence one new field rather than reuse of `message`, so that content and transport flags can
travel together:

```go
// DeliveryText is the fully rendered agent-facing envelope, produced by the
// hub. When set, the broker delivers it verbatim and performs no formatting.
DeliveryText string `json:"delivery_text,omitempty"`
```

`StructuredMessage` continues to ride along **for transport flags only** — `Raw`, `Plain`,
`Interrupt` and message-log labelling. The broker's rule becomes:

```
if req.DeliveryText != "" { deliver(req.DeliveryText) }        // new path
else if req.StructuredMessage != nil { FormatForDelivery(...) } // legacy, deleted in Ph13
else { deliver(req.Message) }                                   // pre-existing plain path
```

**Why this and not the alternatives** — see §5. The short form: it is the only option that
lets Phase 13 delete `pkg/messages/format.go` outright, and it puts envelope construction in
the one process that can tell the truth about a conversation.

### 4.2 `ConversationInfo` after the Q3 ruling

```go
// ConversationInfo is the conversation context delivered to agents.
type ConversationInfo struct {
	ID      string `json:"id"`
	Kind    string `json:"kind"`           // "direct" or "group"
	Surface string `json:"surface"`        // "native", "discord", ...
	Name    string `json:"name,omitempty"` // human-readable
}
```

`Participants []string` is **deleted** (ptone, 2026-09-02: *"yes - clean out participants…
as in I mean delete it from the structured data"*). Rationale recorded in
`messaging-phase-inventory.md` §Resolutions: no producer, no consumer, and for a `group` it
is a membership roster disclosed to every recipient on every message. Re-adding it must be a
reviewed decision, not a three-line completion.

The single test assertion at `delivery_test.go:377` is removed with it.

### 4.3 DEF-102: omit, never synthesise

`Conversation` becomes a **pointer**:

```go
type DeliveryEnvelope struct {
	Timestamp    string            `json:"timestamp"`
	Conversation *ConversationInfo `json:"conversation,omitempty"`
	...
}
```

`synthesizeConversationInfo` is **deleted**, not fixed. It fabricates
`conv.ID = channel + "/" + thread_id` and infers `kind` from a recipient count — handing a
model a value labelled `conversation.id` that matches no row, and an ACL-bearing `kind`
derived from arithmetic.

**Rule: when there is no conversation, emit no `conversation` key.** A missing field is
honest; a fabricated identifier is not. This is the delivery-side application of the standing
rule that a wrong key is worse than no key, and it binds harder here than for DM keys,
because a DM key is consumed by a comparison and this one is consumed by a model that will
treat it as fact.

**Fail-open is correct here and only here.** Absent conversation context must not block
delivery of the message body — an agent that receives a message with no `conversation` is
strictly better off than an agent that receives nothing. This is a *display* concern, and the
standing note that F2/F3 do not transfer to DISPLAY applies. It is **not** a licence to
fail open anywhere on the authorization or key-derivation path.

### 4.4 Field-by-field disposition

Every legacy field, with its fate. Q2 was confirmed as a hard cutover, so there is no
dual-key window.

| Legacy | New | Note |
|---|---|---|
| `timestamp` | `timestamp` | unchanged |
| `msg` | `msg` | unchanged |
| `sender` | `from` | value becomes a PrincipalRef |
| `recipients` (string) | `to` (`[]string`) | addressee PrincipalRefs |
| `type` | `kind` + `intent`/`event` | `envelope_compat.MapLegacyType` |
| `channel` | `conversation.surface` | |
| `thread_id` | `conversation.id` | **not** `reply_to` — see §4.5 |
| `urgent` | **DROPPED** | `MapLegacyEnvelope` never reads `old.Urgent`; `TextIntent` is a closed 3-value enum (`inform`/`request`/`question`) with no urgency slot |
| `broadcasted` | **DROPPED** | only used to compute `hasAddressee`; `Visibility` comes from `old.Visibility`, not from `Broadcasted` |
| `attachments` | `attachments` | paths, unchanged |
| `metadata.system_category` | `event.type` | `envelope_compat.go:88`, reverse map at `:411` |
| `metadata.mention_source` | `Addressee.AddressedVia = "body-mention"` — **in the model, not on the wire** | see OQ-1 |
| `metadata.mention_position` | **DROPPED** | no equivalent |
| — | `reply_to` | new, and currently mis-sourced — §4.5 |

Three of these were listed as "mapped" in earlier notes and are not. **`urgent` and
`broadcasted` are silently discarded** by the conversion, and I only found that by reading
`MapLegacyEnvelope` rather than trusting the type names. Whether that loss is acceptable is a
product question, not an implementation detail: `urgent` in particular changes how a harness
interrupts an agent.

`mention_source` **does** have a home — `AddressedVia` on `Addressee`, values
`explicit`/`body-mention`/`default-agent`/`direct` — but `FormatNewDelivery` flattens
addressees to `env.To = append(env.To, a.PrincipalKind+":"+a.PrincipalID)`, discarding
`AddressedVia`. So the information survives in the model and dies at the wire. That is a
one-field decision, not a redesign. **OQ-1.**

### 4.5 Render from the persisted message, not from `StructuredMessage`

The second load-bearing decision, and it was not visible until I read the adapter.

`MapLegacyEnvelope` (`envelope_compat.go:126`) is a lossy, best-effort conversion **because
its input has already lost the information**. Working from a `StructuredMessage`, it
fabricates **three** identifiers:

| Field | Fabricated as | Why it is wrong |
|---|---|---|
| `conversation.id` | `channel + "/" + thread_id` (`synthesizeConversationInfo`) | matches no `conversations` row — **DEF-102** |
| message ID | `"legacy-" + timestamp` | not the persisted row's ID; two messages in the same second collide |
| `reply_to` | **`old.ThreadID`** | a thread ID is not a message ID. The field is documented `// msg ID`. An agent that follows `reply_to` follows nothing. |

The last one is the worst of the three, and it is **new — filed as DEF-103**. DEF-102 is a
fabricated identifier in a new field; this is a fabricated identifier in a field whose whole
purpose is to point at another message, and the value points at a thread. It will read as a
valid reference and dereference to nothing.

**None of these are bugs in the adapter.** They are the adapter honestly reporting that a
`StructuredMessage` does not contain a message ID, a conversation ID, or a reply target. The
correct response is not to improve the derivations — it is to stop deriving.

**Chosen: the hub renders from the persisted message row.**

By B11/B13 a message is persisted before it is published, so at dispatch time the hub holds a
row with a **real** ID, a **real** `conversation_id`, and real addressees. Rendering from
that yields all three fields correctly with no derivation, and makes
`MapLegacyEnvelope`/`synthesizeConversationInfo` unnecessary rather than merely deprecated —
which is what lets Phase 13 delete `envelope_compat.go` instead of inheriting it.

Where a genuine reply target does not exist, `reply_to` is **omitted**. Same rule as §4.3: no
value is better than a wrong one.

This decision has a cost and it should not be understated: it requires the hub send path to
carry the persisted message forward to dispatch, rather than re-deriving from the
`StructuredMessage` it already built. That is the larger half of Phase 9b. Alt E in §5 is the
cheaper path if this proves harder than expected.

### 4.6 The switch

No new switch. Phase 9 rides the **consolidated messaging switch** produced by the
switch-consolidation work (§6, Phase 9a). During QA the sub-behaviours remain separable **in
test code only** — never in `opsettings` — so a failure can be bisected the way DEF-100
required, without operators ever seeing more than one knob.

Fail-closed semantics are unchanged and non-negotiable: absent row, empty `{}`, malformed
JSON and nil pointer all yield OFF. An unreadable setting must never enable a behaviour. The
switch defaults ON by **shipping a default in the settings registry**, not by inverting the
parse failure path.

### 4.7 Documentation, cut in the same commit as the behaviour

Per Q2, three items change with the cutover:

- `resources/platform_skills/scion-messaging/SKILL.md:128` — `metadata.system_category` →
  `event.type`.
- `SKILL.md:130` — discriminate on `kind`, not `type`. The transition note also stops saying
  `conversation_id` "may appear in message metadata"; it appears as `conversation.id`.
- `docs-site/src/content/docs/hosted/user/messaging.md`.

**Plus one new gate:** fail the build when `SKILL.md` names a delivery field that is not a
JSON tag on `DeliveryEnvelope` or `ConversationInfo`. The skill is the only artifact that
tells agents what to expect, and it is currently more accurate than the CLI's own flag help
purely by luck. This gate only **adds** coverage, so it is inside my discretion to require.

---

## 5. Alternatives considered

### Alt A — enrich `StructuredMessage`, let the broker keep formatting

Add `ConversationKind`, `ConversationSurface`, `ConversationName` beside the existing
`ConversationID`, and have the broker build `ConversationInfo` from them.

**Rejected.** It grows the legacy type that Phase 13 exists to delete, and it puts the new
model's vocabulary inside the old model's struct — so `pkg/messages` would have to survive
the refactor that was supposed to remove it. It also leaves two formatters live in two
processes, which is precisely the state DEF-101 describes.

Not rejected for being unworkable: it is the smaller diff, and if §4.1 proves difficult
during implementation this is the fallback. Recorded as such rather than dismissed.

### Alt B — give the runtime broker a conversation lookup over the hub connection

The broker already holds `hubConnections`; add an RPC to fetch conversation metadata.

**Rejected.** It puts a synchronous hub round-trip on the message delivery hot path, and
makes agent delivery fail when the hub is briefly unreachable — a strictly worse availability
posture than today, in exchange for a field. It also makes the broker a conversation-model
client, which is a large permanent surface for a small transient need.

### Alt C — dual-envelope window: emit both formats for a release

**Rejected by directive.** ptone: *"we want to deliver this a SINGLE cut-over upgrade."* On
the merits as well: it doubles the token cost of every delivered message, and it is the
hybrid state that has no removal step, which is how the legacy formatter would outlive
Phase 13.

### Alt D — keep `synthesizeConversationInfo`, fix its ID derivation

**Rejected.** There is no correct value to derive. If no conversation row exists there is no
conversation ID, and any string placed in that field is a claim the system cannot support.
See §4.3.

### Alt E — render via `MapLegacyEnvelope` from the `StructuredMessage` the hub already has

The cheap version of §4.1: keep hub-side rendering, but feed the existing adapter instead of
plumbing the persisted message through to dispatch. Roughly a tenth of the diff.

**Rejected, but it is the designated fallback.** It cannot produce a correct `reply_to` or a
correct message ID, because its input contains neither — see §4.5. It would ship DEF-102 and
DEF-103 into the default path of every hub rather than retiring them, and it keeps
`envelope_compat.go` load-bearing, so Phase 13 inherits it.

If §4.1 plumbing proves harder than estimated, Alt E is acceptable **only** with `reply_to`
and the message ID omitted rather than fabricated, and with DEF-102/103 explicitly carried
forward as declared gaps. Under no circumstances ship the fabricated values: an identifier
that dereferences to nothing is worse than an absent field, and this is the one place in the
tranche where the cheap option is also the dishonest one.

---

## 6. Migration / rollout

Phase 9 lands behind the consolidated switch and changes nothing until it is on.

**Phase 9a — switch consolidation (prerequisite, not part of 9 proper).**
`ConversationReadSwitch` and `ConversationWriteDenySwitch` collapse into one
`MessagingSettings` field. This is a settings-schema change and needs its own migration for
the 22 `hub_settings` rows on gteam. It must land *before* 9b so the envelope has one switch
to ride.

**Phase 9b — render at the hub.** `DeliveryText` on `MessageRequest`; hub-side rendering;
broker prefers it. Legacy path still present and still reachable when the switch is off.

**Phase 9c — envelope corrections.** Delete `Participants`; make `Conversation` a pointer;
delete `synthesizeConversationInfo`; resolve OQ-1.

**Phase 9d — documentation and the SKILL gate.**

**Rollback.** With the switch off, the broker falls back to `FormatForDelivery` and behaviour
is bit-for-bit what it is today. That property must be **asserted by a test**, not assumed:
switch-off output must be byte-identical to current output. This is the cheapest real safety
property in the tranche and it should be the first test written.

**gteam.** Deploy with the switch off, verify byte-identity against a captured pre-deploy
envelope, then flip. Standard snapshot-and-retain procedure per
`runbooks/gteam-rollback.md`.

---

## 7. Discovered scope — "migrations auto-run" is not true today

ptone's Q1 answer requires that *"when a hub is updated to a version that includes this
completed refactor, migrations are auto-run."* Measured:

| Migration | Auto-runs? | Evidence |
|---|---|---|
| `backfillTopicConversations` | **YES** | `webchannel_store.go:1425`; Postgres at `webchannel_store_postgres.go:1029` |
| `BackfillService` | **NO** — manual CLI | sole non-test caller `cmd/server_backfill.go:153` |
| `DMMigrationService` | **NO — zero callers anywhere** | all references inside `pkg/messaging/dm_migration.go` |

`DMMigrationService` is unreachable: no command, no startup hook. This is the likely reason
**DEF-29 persists on gteam** — the migration meant to repair keyless `direct` rows has never
been runnable. Stated as likely, not established.

This is **not** Phase 9 work, and it is not a Phase 13 footnote. It is a tranche of its own,
and one of its two paths needs a caller invented from nothing. It must be scheduled before
the shipping version, because G2 depends on it.

An idempotent, startup-run migration that touches DM ACLs is a higher-risk object than
anything in Phase 9. It should get its own design.

---

## 8. Open questions

**OQ-1 (blocks 9c).** `AddressedVia` is computed and then discarded at the wire, because
`FormatNewDelivery` flattens addressees to bare principal ref strings. Does `to` become
`[]{ref, via}` objects, or does `AddressedVia` stay model-internal and `mention_source` be
recorded as a deliberate removal? Cost of the object form is a schema change to `to` that
Phase 13 cannot easily undo. I will identify the consumer of `mention_source` before asking
anyone to decide. `mention_position` has no home either way.

**OQ-1b (blocks 9c).** `urgent` and `broadcasted` are **silently dropped** by
`MapLegacyEnvelope` (§4.4) — neither field is read. `urgent` is the one that matters: it
influences how a harness interrupts an agent, so losing it is a behaviour change and not
merely a schema one. Needs a home in the envelope or an explicit ruling that agents no longer
see urgency. **This is the item in the design most likely to be discovered late by a user
rather than by a test**, because nothing fails — messages simply stop being urgent.

**OQ-2 (blocks 9a).** Does the consolidated switch keep one of the two existing setting keys
or introduce a third name? Keeping `conversation_read_switch` and repurposing it means the
22 existing gteam rows need no migration but the name lies. A new key is honest and needs a
migration. Leaning: new key, migration, since 9a is already a schema change.

**OQ-3 (tracking).** `visibility` now appears in the agent-facing envelope. It is an existing
`StructuredMessage` field, so this is not new information reaching agents, but it is newly
*prominent*. No action expected; recorded so it is not discovered later as a surprise.

---

## 9. Implementation phases

Commit-sized, in order. Each is independently reviewable.

1. **Byte-identity test for the off state.** Assert `FormatForDelivery` output is unchanged
   with the switch off. Written first, before any behaviour changes. Pure addition.
2. **9a — switch consolidation** + settings migration. Fail-closed tests for absent row,
   empty `{}`, malformed JSON, nil pointer. Resolve OQ-2 first.
3. **9b(i) — `DeliveryText` on `MessageRequest`** and broker preference logic. No hub-side
   producer yet, so no behaviour change. Tests: broker prefers `DeliveryText`; falls back
   correctly when empty; `Raw`/`Plain` still honoured.
4. **9b(ii) — carry the persisted message to dispatch** (§4.5). The largest step and the one
   to schedule first if effort is uncertain, because Alt E is the fallback and choosing it
   late is expensive.
5. **9b(iii) — hub-side rendering** behind the switch. Both hub sites and the hubclient path.
6. **9c(i) — delete `Participants`** from `ConversationInfo` and its test assertion.
7. **9c(ii) — `Conversation` becomes a pointer; delete `synthesizeConversationInfo`.**
   Test: no conversation ⇒ no `conversation` key ⇒ body still delivered (DEF-102).
8. **9c(iii) — `reply_to` sourced from a real reply target or omitted** (DEF-103); message ID
   is the persisted row's ID. Test: a threaded message does **not** put a thread ID in
   `reply_to`.
9. **9c(iv) — resolve OQ-1 and OQ-1b.**
10. **9d — SKILL.md, docs-site, and the field-name gate.**
11. **Endpoint deletion count** per file, re-run independently before push, per standing rule.

`FormatForDelivery` is **not** deleted in Phase 9 — it remains the switch-off path. Its
deletion is Phase 13, and by then it should have no callers.

---

## 10. Acceptance criteria

- **AC-9-1.** With the consolidated switch **off**, agent-facing output is **byte-identical**
  to `85f25c1a1`. Asserted by test, not inspection.
- **AC-9-2.** With the switch **on**, a native thread message yields an envelope whose
  `conversation.id` resolves to a real `conversations` row, and whose `conversation.surface`
  matches that row.
- **AC-9-3.** `grep -c participants` over `pkg/messaging/delivery.go` is 0. The field is gone
  from the struct, not merely unset.
- **AC-9-4.** A message with no resolvable conversation is **delivered**, with **no
  `conversation` key** in the envelope. No fabricated ID appears under any input.
- **AC-9-5.** `messages.FormatForDelivery` has zero production callers on the switch-on path.
  `runtimebroker` performs no formatting when `DeliveryText` is set.
- **AC-9-6.** `Raw` and `Plain` deliver body text only, exactly as today, on both paths.
- **AC-9-7.** The consolidated switch is OFF for: absent row, empty `{}`, malformed JSON, nil
  pointer. Four separate tests.
- **AC-9-8.** The SKILL gate fails the build when `SKILL.md` names a field absent from
  `DeliveryEnvelope`. Demonstrated by a deliberate red.
- **AC-9-9.** `system_category` survives as `event.type` for every category in
  `eventTypeToSystemCategory`, round-tripped.
- **AC-9-10.** No change to `authorizeAgentMessage` reachability, to DM key derivation, or to
  any item on the prohibition list. Reviewer confirms by reading the diff, not by counting.
- **AC-9-11.** Slack and Telegram outbound formatting is unchanged — they consume
  `StructuredMessage`, which Phase 9 does not alter.
- **AC-9-12 (DEF-103).** `reply_to` is either absent or the ID of a message that exists. A
  threaded message with no reply target yields **no** `reply_to` key. Specifically: no input
  produces a `reply_to` equal to a thread ID.
- **AC-9-13.** The envelope's message ID is the persisted row's ID. No value matching
  `^legacy-` appears in any delivered envelope.
- **AC-9-14.** No delivered envelope contains an identifier that does not resolve. The
  reviewer's check is direct: for a sample of live envelopes, every `conversation.id` and
  every `reply_to` selects a row. This is the single acceptance criterion that covers
  DEF-102, DEF-103 and the synthesised message ID together, and it is the one to run against
  gteam rather than against fixtures.

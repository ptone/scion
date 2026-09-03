# DEF-138 — Inbound/outbound conversation split

**Project:** ca-msg-arch
**Status:** DRAFT — ready for dispatch
**Measured at:** `scion/tranche-g` = `0cff20a2b1ad5e46d4ce3e1a4cc1b7140221e651`
**Author:** ca-msg-arch (architect)
**Reproduction:** live on gteam, preserved — conversations `0c57b491` / `b2fd01b6`, messages `3e718a5a` (in) / `1f3f0e39` (reply)

---

## 1. Problem & Goals

### The observation

| | conversation | kind | external_ref |
|---|---|---|---|
| inbound `3e718a5a` | `0c57b491` | group | `thread:a3083e98:1532505776013312133` |
| reply `1f3f0e39` | `b2fd01b6` | direct | `dm:agent:c9c1123b:user:b53249ea` |

The reply was **delivered to the Discord channel** but **persisted as a DM**.

### Root cause

Two independent resolution strategies, selected on one field.

**Inbound** (`handlers_broker_inbound.go:556-564`): every channel plugin sets
`ThreadID` — Discord `broker.go:1471-1489`, Slack `events.go:403`, Teams
`activities.go:85`, web chat `handlers_chat_v2.go:1159`. None sets
`Surface`/`ExternalRef`, so all four take the **Phase 5 thread branch** →
`thread:{projectID}:{threadID}`, kind `group`.

**Outbound** (`messagebroker.go:466`) branches on exactly one thing:

```go
if msg.ThreadID != "" {          // → ResolveOrCreateThreadConversation (group)
} else if msg.SenderID != "" && msg.RecipientID != "" {
                                 // → ResolveOrCreateDMConversation (direct)
```

An agent's reply carries **empty `ThreadID`**. No surface, channel or plugin is
consulted. Empty ⇒ DM, always.

### The asymmetry that makes it confusing

There **is** an affinity mechanism, and it works — it just carries the wrong
field. `webchat_conversation_context`
(`webchannel_store_postgres.go:51-58`) stores
`user_id, project_id, agent_id, last_channel, last_message_at`. Written inbound
(`handlers_broker_inbound.go:476-481` `RecordChannel`), read outbound
(`handlers_agent_messaging.go:221-229`):

```go
if lastCh, err := wcsAffinity.GetLastChannel(ctx, recipientID, agent.ProjectID, agent.ID); ...
} else if lastCh != "" {
    req.Channel = lastCh
}
```

**No thread id and no conversation id in that table.** Delivery has affinity;
persistence does not. That single omission is the whole defect.

### Goals

- **G1** — For every surface, an agent's reply persists into the **same
  conversation** as the message it is replying to.
- **G2** — No agent-side behaviour change. Existing agents are fixed as-is.
- **G3** — No new way for a client to assert a conversation it is not
  authorised for. The fix must not open an injection surface.
- **G4** — Reduce, not increase, the number of places that resolve a
  conversation.

### Non-goals

- Multi-conversation precision. An agent addressed in two conversations that
  replies without saying which gets the most recent. §7 / OQ-138-2.
- `conv:<id>` CLI addressing (`cmd/message.go:150-161` gates it off). Separate
  work, and the escape hatch `--channel X --thread-id Y` already exists.
- Fixing the false-warning volume in `CheckConversationConsistency` — DEF-130.
- `reply_to_id` plumbing (`render_delivery.go:76-80` leaves it deliberately
  empty). See Alternative D.

---

## 2. Proposed design — carry the thread id in the affinity record

**Mirror the mechanism that already works.** Channel affinity sets
`req.Channel` when the caller omits it; thread affinity sets `req.ThreadID` the
same way.

1. Add `last_thread_id` to `webchat_conversation_context`.
2. Record it inbound, at the existing `RecordChannel` site.
3. Read it outbound, immediately beside the existing `GetLastChannel` block:

```go
// pseudocode, handlers_agent_messaging.go ~:229
if req.ThreadID == "" {
    if lastThread, err := wcsAffinity.GetLastThreadID(ctx, recipientID, agent.ProjectID, agent.ID); err == nil && lastThread != "" {
        req.ThreadID = lastThread
    }
}
```

**An explicit `req.ThreadID` always wins.** Affinity only fills a blank —
identical precedence to the channel rule, and to DEF-135's "Phase 11 explicit
beats Phase 5 inferred."

### Why this is the right shape

Both resolution sites already handle a non-empty `ThreadID` **correctly and
identically**: the handler's `DeriveConversationKey` (`derive_key.go:94-100`)
and the broker's `ResolveOrCreateThreadConversation`
(`messagebroker.go:466-472`) both produce `thread:{projectID}:{threadID}`, kind
`group`. So populating the field makes the two existing paths **agree**, rather
than adding a third path that has to be kept in sync with them.

Consequences worth stating plainly:

- **No wire change.** `OutboundMessageRequest` (`handlers_agent_messaging.go:37-50`)
  is untouched. Verified: it has 10 fields and none is a conversation.
- **No authorization surface.** The value is server-recorded from an inbound
  message the agent demonstrably received. The client never asserts it, so G3
  holds by construction of the data flow — not by a check that could be removed.
- **No migration.** A nullable column; absent ⇒ empty ⇒ today's behaviour.
- **Derivation stays single-source.** We supply an input to the existing key
  derivation rather than storing a resolved conversation id and bypassing it.
  This respects the standing rule that the derivation path is authoritative.

### ⚠️ The risk that must be measured before this ships

`req.ThreadID` may not be inert on the **delivery** side. If a plugin routes on
`thread_id`, populating it changes where replies appear, not merely where they
are stored — turning a persistence fix into a delivery change.

Three outcomes, and they are not equally acceptable:

| Outcome | Verdict |
|---|---|
| Delivery ignores `thread_id`; only persistence changes | Ship it |
| Delivery becomes *more* precise (reply lands in the originating thread) | Ship it — but it is a **behaviour change**, must be in the changelog, and ptone must be told rather than discovering it |
| Delivery breaks or is misrouted | **Blocks this design**; fall back to Alternative B |

**This is the single load-bearing unknown.** It is AC-4, and it must be
answered by measurement on gteam, not by reading plugin code alone —
`--channel`/`--thread-id` interact (`cmd/message.go:192-194`) and the plugins
each interpret the pair differently.

### Second change: collapse the double resolution (G4)

`handleAgentOutboundMessage` resolves a conversation at `:307-314` and assigns
it at `:343` — **then discards the whole `storeMsg` when a broker is present**
(`:398-407`), because `structuredMsg` carries no `ConversationID` and
`store.CreateMessage` only runs on the non-broker branch at `:409`. The row that
survives is written by `deliverToUser` (`messagebroker.go:501,522`), which
resolves again from scratch.

Two resolutions, two divergence log lines for one reply (`:355-369` and
`messagebroker.go:504-518`) with different message ids, and one result silently
thrown away. They agree today only because they consume the same inputs.

**This is not cosmetic.** The dead resolution is exactly the kind of code a
future change "fixes" without effect, and the duplicate log lines make the
instrumentation ambiguous. Delete the dead path, or make the handler's result
authoritative — SC-2 below picks the former as lower-risk.

---

## 3. DEF-139 — the divergence detector cannot fail

Filed separately because it is a **defect in the evidence**, not in the
messaging path, and it is why DEF-138 ran undetected.

`ComputeDivergenceMatch` (`divergence.go:286-323`) opens with:

> *"The comparison is non-tautological: actualExternalRef comes from the
> database, not from reconstructing inputs."*

In the DM branch this is **false**. `oldPair` = sorted{`senderID`,
`recipientID`}. `newPair` = the ids at `parts[2]`/`parts[4]` of
`actualExternalRef` — a key that `ResolveOrCreate**DM**Conversation`
(`conversation.go:92-116`) built from those same two ids and upserted moments
earlier. The two sides are the same derivation. It **cannot** return anything
but `dm-routing-agreement`.

The docstring's defence — "comes from the database" — is true and irrelevant:
the row was written from those inputs on this request. The claim holds only for
rows created under a *different* rule, which is the case the resolve-or-**create**
path guarantees against.

The check that could have caught the split, `CheckConversationConsistency`
(`divergence.go:381-460`), runs immediately after (`messagebroker.go:520`) and
**its return value is discarded at both call sites**. DEF-130 already records it
emitting ~16 false warnings a day, so even the log line it does emit is buried.

**A detector that cannot fail is worse than no detector**, because it is read as
evidence. This is the mechanism by which a green signal accompanied a broken
round trip for the entire life of the switch.

Fix: make the DM branch compare against a conversation the request did not just
create (or drop the branch and report `dm-routing-unverifiable`), correct the
docstring, and consume the `CheckConversationConsistency` return value.

---

## 4. Alternatives considered

**A — Store `last_conversation_id` instead of `last_thread_id`, and propagate a
resolved conversation through `structuredMsg` into `deliverToUser`.**
Rejected as the primary, though it is the closest runner-up. It bypasses key
derivation and makes a stored id authoritative, which conflicts with the
standing rule that derivation is the single source of truth for conversation
identity. It also requires a new field on the internal message struct and a
behaviour change in `deliverToUser` (honour-if-set), i.e. more moving parts for
the same outcome. **Promote this to primary if AC-4 shows `ThreadID` is not
inert on delivery** — it changes persistence without touching routing, which is
precisely the property we would then need.

**B — Add `ConversationID` to `OutboundMessageRequest` and have agents echo it
back.** Rejected as the *first* move; correct as a later enhancement. It is the
most precise answer and it is the only one that solves multi-conversation
addressing (§7). But: it fixes no existing agent, since none sends the field;
`SKILL.md:130` currently tells agents the field is "not yet required"; and it
introduces a client-asserted conversation, which **requires an authorization
gate**. That gate is not hypothetical work — the sibling handler
`handleAgentMessage` already implements exactly this pattern at `:951-1044`
before honouring the assertion at `:1037`, and any implementation here must
match it. Doing B first means shipping the authorization surface to fix nobody.

**C — Have plugins set `Surface`/`ExternalRef` so Phase 11 runs.** Rejected,
and this is the same trap recorded in DEF-135 Alternative B: Phase 11 keys
`discord:chan:X` while the current path keys `thread:{projectID}:X`, so it
splits history at the deploy boundary. Worse here, because it would do so on
**four** surfaces at once.

**D — Populate `reply_to_id` and derive the conversation from the replied-to
message.** Rejected for now. Semantically the most precise — it answers "which
conversation" by pointing at an actual message rather than guessing. But
`render_delivery.go:76-80` leaves `ReplyToID` deliberately empty, `reply_to_id`
exists only on the web-chat human→agent path
(`handlers_chat_v2.go:839`), and it needs the same agent-echo plumbing as B. It
is the right long-term shape and the wrong next commit.

**E — Do nothing; document the split.** Rejected. Under the standing tolerance
for "tracked buggy behaviour that does not dead-end us," this superficially
qualifies. It does not, because the conversation id in the envelope is
*actively misleading*: it names a conversation the agent cannot reply into.
Shipping an identifier that does not round-trip trains every future consumer to
distrust it, which forecloses the end state rather than deferring it.

---

## 5. Migration / rollout

Additive nullable column; absent ⇒ empty ⇒ current behaviour. No backfill —
affinity is populated by the next inbound message per (user, project, agent)
tuple, so the fix takes effect on the second message of each conversation and
is self-healing.

**Explicitly not backfilled:** existing split history stays split. Retro-editing
`conversation_id` on historical rows is an irreversible rewrite of the record
for a cosmetic gain, and is not proposed. OQ-138-3.

Rollback is a revert; the column can be left in place, unread.

---

## 6. Implementation phases

| Phase | Content |
|---|---|
| **SC-1** | Schema: add nullable `last_thread_id` to `webchat_conversation_context`, both sqlite and postgres. Store method + read method. No call sites yet. |
| **SC-2** | Delete the dead resolution in `handleAgentOutboundMessage` (`:307-314`, `:343`) and its divergence log (`:355-369`). Pure deletion — behaviour must not change, since the result is already discarded when a broker is present. **Verify the non-broker branch at `:409` still resolves correctly before deleting anything it depends on.** |
| **SC-3** | Record `last_thread_id` inbound at the `RecordChannel` site. |
| **SC-4** | Consume it outbound beside `GetLastChannel`. Explicit `req.ThreadID` wins. This is the commit that changes behaviour. |
| **SC-5** | DEF-139: fix the DM branch of `ComputeDivergenceMatch`, correct the false docstring, consume the `CheckConversationConsistency` return. |
| **SC-6** | Tests, per §7. |

Standing constraints: never make a gate pass by weakening it; report red to me
rather than tuning it away; stage only named files, never `git add -A`; per-file
numstat before push; push to the explicit token-bearing ptone/scion URL and
include raw `ls-remote` output.

---

## 7. Acceptance criteria

- **AC-1 — the round trip, per surface.** For Discord, Slack, Teams and web
  chat: inbound to an agent, agent replies, **the reply's `conversation_id`
  equals the inbound's**. This is the criterion DEF-135's AC-9 should have been.
- **AC-2 — explicit beats affinity.** A reply with an explicit `thread_id`
  resolves to that thread, not the remembered one.
- **AC-3 — no affinity, no change.** With an empty `last_thread_id`, behaviour
  is byte-identical to today (DM branch). Guards the upgrade path.
- **AC-4 — ⚠️ delivery is unchanged.** Measured on gteam, not reasoned from
  code: populating `req.ThreadID` must not alter **where the reply appears** on
  any surface. If it does, report before proceeding — per §2 this either forces
  a changelog entry or blocks the design in favour of Alternative A.
- **AC-5 — no client assertion.** Assert that a `conversation_id` supplied in
  request `Metadata` is still ignored. Today it is written by
  `cmd/message.go:784` and read by nobody; this fix must not accidentally make
  it live, because that would be an unauthorised assertion path.
- **AC-6 — one resolution, one log line.** After SC-2, a single agent reply
  produces exactly **one** divergence log line, not two with differing message
  ids.
- **AC-7 — DEF-139 mutation test.** Force the outbound path to resolve a
  conversation inconsistent with the inbound and assert the divergence check
  **reports a mismatch**. This must be a genuine mutation that compiles and
  runs — the current check passes this scenario, so a test that does not fail
  against unfixed code proves nothing.
- **AC-8 — consistency return consumed.** `CheckConversationConsistency`'s
  result is acted on, not discarded, at both call sites.
- **AC-9** — `make test-fast` green; `pkg/hub` + `pkg/messaging` + `pkg/store`
  green excluding the two known-environmental failures.

---

## 8. Open questions

- **OQ-138-1 (blocking SC-4)** — AC-4's answer. Is `req.ThreadID` inert on
  delivery? Must be measured on gteam per surface. **This is the one that
  decides between the primary design and Alternative A.**
- **OQ-138-2 (not blocking)** — multi-conversation agents. Affinity gives the
  most recent conversation; an agent active in two gets the wrong one for the
  older. Note this is **already true of channel affinity** and has been
  tolerated, so the fix does not introduce the class of error, it inherits it.
  Alternative B is the real answer, later.
- **OQ-138-3 (ptone's, irreversible, not urgent)** — leave existing split
  history split? My read: yes. Rewriting historical `conversation_id` values is
  irreversible and buys tidiness only.
- **OQ-138-4** — should `SKILL.md:130` ("`conversation_id` … not yet required")
  change? Not until Alternative B lands. Recorded so the two do not drift.

# §2.6.4 — Unification spec: `Conversation` ← `webchat_topic`

**Draft for `nc-arch`.** Owed since 2026-08-27 13:28Z, when the user decided §2.6.3 option (ii).
This is the promised draft, not a question. Two decisions in it need nc-arch's assent and one
needs the user's; they are named in §7 and raised serially, not batched into this document.

**Status of the decision it implements:** settled. *Direction* is not reopened here. What is open
is the `default_agent` domain collision (§4.3), which the decision did not anticipate because
neither of us had read the column.

---

## 1. Prior art, grepped (rule 16)

Every claim below is cited. `origin/main` = `b09e7f49`; branch = `scion/messaging-v2` @ `edd4e4bd`.
**`main` moved from `98a9d9c2` to `b09e7f49` while this was being written** — treat the citations
as timestamped, not permanent (rule 24).

| Fact | Where |
|---|---|
| `webchat_topic` DDL | `pkg/hub/webchannel_store.go:409` (SQLite), `webchannel_store_postgres.go:72` (PG) |
| Topic name is **case-insensitively unique per project**, excluding deleted | `webchannel_store.go:508` |
| Exactly one `#general` per project | `webchannel_store.go:497` (partial unique index) |
| Topic id is a real UUID (`uuid.New().String()`) | `handlers_chat_v2.go:449` |
| `conversation_key` is **polymorphic**: `dm:` prefix → DM, else bare topic UUID | `handlers_chat_v2.go:2842` (prefix test), `:2853` and `:3151` (bare key → `GetTopic`) |
| `webchat_topic.default_agent` holds **a slug or a UUID**; readers try both | `handlers_chat_v2.go:938-941` (`GetAgentBySlug` then `GetAgent`), `:685` (compares `agentID` **and** `agentSlug`) |
| Default-agent binding is cleared when an agent is deleted | `handlers_chat_v2.go:661` `ClearTopicDefaultAgent` |
| `Conversation` DDL | `pkg/ent/schema/conversation.go` |
| Branch mints `surface=native` conversations | `pkg/messaging/conversation.go:77` (DM), `:162` (thread), `resolve.go:365,410`, `backfill.go:287`, `delivery_compat.go:69` |
| `Conversation.display_name` is written by nothing in production; upsert wipes it | DEF-7; `entadapter/conversation_store.go:400` unconditional `SetDisplayName` |

**Two things I had written in §2.6.3 that the grep changed:**

1. I recorded name uniqueness as nc-arch's claim. **It is true** — `:508`. Verified, no longer
   inherited (rule 15).
2. I wrote that `webchat_topic` "cannot drift" because we own it. True of the *schema*. **Not true
   of `default_agent`**, which already holds two different kinds of value and is reconciled by
   a fallback chain at read time. We own the table and still let it drift internally.

---

## 2. The collision to state first

The decided direction is the **reverse pointer**: `webchat_topic` carries `conversation_id`.

**The branch currently implements the forward pointer instead.**
`ResolveOrCreateThreadConversation` (`pkg/messaging/conversation.go:158-162`) mints

```
Conversation{ Kind: "group", Surface: "native",
              ExternalRef: "thread:<projectID>:<threadID>" }
```

on the message path. If `<threadID>` is a webchat topic UUID, that row **shadows an existing
`webchat_topic` row with no link between them** — two records for one conversation, in two stores,
neither aware of the other. That is DEF-8 reproduced at the store boundary, and it is being
created today, on our branch, by the section that exists to eliminate it.

It is also the same site as DEF-15. Any fix here must be reconciled with that row, not landed
beside it.

**Nothing is burning in production** — `pkg/messaging/conversation.go` does not exist on
`origin/main` (verified, and previously mis-stated in the DEF-15 ledger row until nc-arch's grep
corrected it). This is a pre-beta defect in unreleased code.

---

## 3. Proposed design

### 3.1 The boundary

> **Surface-uniform identity; surface-specific capability.**

| Layer | Owns | Examples |
|---|---|---|
| `Conversation` | Identity, and only identity | id, project, kind, surface, lifecycle timestamps, cross-surface addressing |
| `webchat_*` | The native projection | `name`, `is_general`, `created_by`, `last_message_id`, read-state, prefs, presence |

A field belongs to `Conversation` if a Discord conversation would need it too. `name` does not —
Discord names its own channels. Read-state does not — it is per-user native UI state.

### 3.2 Link

```sql
ALTER TABLE webchat_topic ADD COLUMN conversation_id TEXT;   -- NULL only during backfill
CREATE UNIQUE INDEX idx_webchat_topic_conversation
    ON webchat_topic (conversation_id) WHERE conversation_id IS NOT NULL;
```

`Conversation.external_ref` **stays empty** for native topic conversations. One pointer, one
writer. Two pointers is the DEF-8 pattern.

**Why the reverse pointer is the correct direction, beyond the cargo-cult argument already in
§2.6.3:** `external_ref` exists to *deduplicate rows whose creation we do not control*. The unique
index on `(surface, external_ref)` is upsert-on-message infrastructure for surfaces where the
first we hear of a channel is a message arriving in it. Native is not that. Native topics are
created by an endpoint we own (`handlers_chat_v2.go:449`), under a uniqueness constraint that
already exists (`:508`). **The dedup machinery is redundant here, and adopting it would mean
maintaining a second uniqueness rule that can disagree with the first.**

Consequence, stated so it is not discovered later: with `external_ref = ''`, native topic
conversations are **not covered by the partial unique index** and **cannot be created by
`UpsertConversationByExternalRef`**. That is intended. Their creation path is `CreateTopic`, in the
same transaction as the topic row (§3.4). If a native topic conversation is ever minted by the
message path, that is a bug, and §6 AC-U-3 makes it a visible one.

### 3.3 `ResolveOrCreateThreadConversation` must stop minting

For `surface=native`, thread resolution **reads** `webchat_topic.conversation_id`. It does not
create. A thread id with no topic row is not a conversation we should invent — under the standing
posture, **it resolves to nothing and the caller reports "unresolved", never a fresh row.**

This tightens the Phase 5 non-fatal contract rather than breaking it: the function already returns
`nil` on failure and callers already must not treat `nil` as fatal.

### 3.4 Atomicity

The `Conversation` row and the `webchat_topic` row are written **in one transaction**. A topic
without a conversation is invisible to cross-surface addressing; a conversation without a topic is
an orphan with no name. Neither is repairable from the other side afterwards.

This crosses the Ent / raw-SQL boundary, and that is the single largest implementation risk in this
spec. **If the two stores cannot share a transaction, say so and stop — do not implement it
best-effort with a reconciliation sweep.** A sweep is the design that produced DEF-9. §7 Q2.

### 3.5 `drift_state` for native

`DriftState` is meaningless where referential integrity holds. Native conversations are
**permanently `active`**; `orphaned`/`unresolvable` are unreachable for `surface=native`. Assert
this rather than leaving it to convention (AC-U-5) — an enum with unreachable values invites a
future writer to reach them.

### 3.6 `project_id`

`Conversation.project_id` is `Optional().Nillable()`. `webchat_topic.project_id` is `NOT NULL`.
**The identity layer is weaker than the projection it claims to own.** A native conversation with a
NULL project is unaddressable and unauthorizable — project membership is one of the two
authorization sources. Enforce non-null for `surface=native` at the write path (AC-U-4). Do not
widen the column's nullability contract for other surfaces in this change.

### 3.7 `display_name`

Stays vestigial, per DEF-7. **Do not mirror `webchat_topic.name` into it.**
`UpsertConversationByExternalRef` does an unconditional `SetDisplayName` on update
(`conversation_store.go:400`), so a mirrored name would be silently wiped by the next upsert —
a rename that reverts itself with no error. One writer, in `webchat_topic`. `#<thread>` resolution
reads `webchat_topic.name`.

---

## 4. `default_agent` — the collision the decision did not anticipate

### 4.1 State

| | Type | Domain | Read? |
|---|---|---|---|
| `webchat_topic.default_agent` | `TEXT` | slug **or** UUID, disambiguated by fallback at `:938-941` | **Yes** — drives the native wake path |
| `Conversation.default_agent_id` | `uuid.UUID` | UUID only | **No** — written in 3 places, read nowhere (§2.13.4) |

### 4.2 Why this inverts an assumption in §2.13.4

I wrote there that the write side "needs no work" because three writers already exist, and that
they should be checked for agreement because *"unread data has never been tested by use."*

That reasoning holds, and it points the other way from where I pointed it. **Native chat's column
is the one that has been tested by use.** Mine are the untested three. The migration should treat
`webchat_topic.default_agent` as the source of truth for native and `Conversation.default_agent_id`
as the unproven target — not the reverse.

There is also a lifecycle hook only the native side has: `ClearTopicDefaultAgent` (`:661`) nulls
the binding when an agent is deleted. `Conversation.default_agent_id` has no equivalent, so
promoting the field without promoting the hook creates dangling agent references.

### 4.3 Proposed resolution

Default-agent routing is **not** surface-specific — §2.4 step 2 depends on it for every surface —
so by §3.1 it belongs to `Conversation`. But the migration is lossy in one direction: resolving
slug → UUID can fail (agent renamed, agent deleted, slug ambiguous across projects).

**Under-granting is recoverable; over-granting is not.** An unresolvable slug migrates to **NULL**,
never to a guess. NULL is not a failure state here: it is §2.4 case 3 — *posted, nobody woken* — a
defined, reportable outcome. The migration **emits the unresolvable set as a report** and requires
operator review before cut-over. Fail closed, and visibly.

`ClearTopicDefaultAgent` moves with the field, or the field does not move.

**This is the one item with user-visible behaviour change** (a native topic's default agent can
stop firing), and it is Q1 in §7.

---

## 5. Alternatives considered

**(A) Forward pointer — `Conversation.external_ref = <topic uuid>`, native as an integration.**
Rejected. Cargo-cults a drift workaround for a table with referential integrity (§2.6.3), and
§3.2 adds a second reason: it installs a uniqueness rule that duplicates and can disagree with
`idx_webchat_topic_project_name`. Its one genuine advantage — it needs no new column and no
transaction spanning two stores (§3.4 is the hard part of this spec) — is real, and if §7 Q2 comes
back "the stores cannot share a transaction", this alternative becomes live again. Recorded so it
can be reopened on evidence rather than rediscovered.

**(B) Absorb `webchat_topic` into `Conversation` entirely.** Rejected. `is_general`, `created_by`,
`last_message_id`, read-state and prefs have no cross-surface meaning; the table would accumulate
native-only columns until `Conversation` *is* `webchat_topic` with extra surfaces. It also forces
every native read through Ent, replacing working shipped code for no user-visible gain.

**(C) Option (i), minimal — permanent coexistence.** Already rejected by the user 2026-08-27
13:28Z. Retained here only so the doc is self-contained.

---

## 6. Acceptance criteria

- **AC-U-1** Creating a native topic writes a `webchat_topic` row and a `Conversation`
  (`surface=native`, `kind=group`) row, linked by `conversation_id`. Assert both rows and the link.
- **AC-U-2** Killing the transaction between the two inserts leaves **neither**. Mutation-verified.
  (§3.4; mirrors AC-DEF9-5.)
- **AC-U-3** The message path **never mints** a `surface=native` conversation for a thread id that
  has no topic row. Assert the row count is unchanged and the caller reports unresolved.
  *This is the DEF-8-at-the-store-boundary regression test; it must fail before the fix.*
- **AC-U-4** A `surface=native` conversation cannot be persisted with a NULL `project_id`.
- **AC-U-5** `drift_state` is `active` for every `surface=native` row. Permanent test **with a
  floor** — vacuous on an empty table (rule 14).
- **AC-U-6** Backfill of existing topics is **all-or-nothing per row** and idempotent: re-running it
  creates no duplicate conversations. Assert by running it twice and comparing counts.
- **AC-U-7** A `default_agent` slug that resolves migrates to the correct UUID; one that does not
  migrates to NULL, **appears in the report**, and the topic then behaves as §2.4 case 3 (posted,
  nobody woken) rather than erroring or waking the wrong agent.
- **AC-U-8** Deleting an agent clears `default_agent_id` on native conversations, as
  `ClearTopicDefaultAgent` does today. Assert the post-delete value.
- **AC-U-9** `webchat_read_state.conversation_key` is **unchanged** by this migration — still the
  bare topic UUID / `dm:` key. Assert existing read-state rows still resolve after cut-over.

---

## 7. Open questions — raised serially, not batched

**Q1 (user, product-visible).** §4.3: a native topic whose `default_agent` slug no longer resolves
loses its default agent at migration. Accept the fail-closed NULL + report, or hold cut-over until
the unresolvable set is empty?

**Q2 (nc-arch, engineering).** §3.4: can a single transaction span the Ent `Conversation` write and
the raw-SQL `webchat_topic` write, on **both** SQLite and Postgres? If not, this spec changes shape
and alternative (A) reopens. **This one gates the phase breakdown, so it goes first.**

**Q3 (nc-arch, small).** Does `is_general` want a `Conversation` analogue, or does "#general" stay
purely native? My read is purely native — no other surface has the concept — but it is nc-arch's
field.

---

## 8. Implementation phases

Sequenced so nothing user-visible moves until the link exists and is proven.

1. **Column + index**, nullable, no writers. Inert.
2. **Dual-write on create** (§3.2, §3.4) — new topics get conversations. AC-U-1, AC-U-2, AC-U-4.
3. **Backfill** existing topics, all-or-nothing per row, idempotent. AC-U-6.
4. **Close the mint path** (§3.3) — `ResolveOrCreateThreadConversation` reads instead of creating.
   AC-U-3. *This is the phase that actually stops the divergence; 1–3 only make it possible.*
5. **`default_agent` promotion** + `ClearTopicDefaultAgent` move. AC-U-7, AC-U-8. **Gated on Q1.**
6. **`#<thread>` resolution** reads `webchat_topic.name` (DEF-7 / DEF-5 unblock).
7. **Make `conversation_id` NOT NULL** once backfill is proven. Cleanup.

Phases 1–4 are independent of Q1 and can proceed while it is open. **Phase 4 is the one that pays
the debt** — if scheduling pressure forces a cut, cut from the back, not the front.

---

## 9. What this unblocks

DEF-5 and DEF-7 both terminate in "depends on the §2.6.3 unification decision". Phase 6 is their
unblock. **I had been carrying both as blocked-on-an-external-decision when the decision was made
and the draft was mine to write** — the dependency was on my own unstarted work, and listing it in
the ledger as a blocker disguised that for several hours.

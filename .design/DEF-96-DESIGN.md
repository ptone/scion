# DEF-96 — DM promotion: design

**Status:** design, not yet staffed.
**Author:** `ca-msg-arch`. **Date:** 2026-09-09.
**Measured against:** `scion/tranche-g` @ `77ebea1f6`.
**Supersedes:** the "needs a design, not a three-line repeat" placeholder in
`DEFECTS.md` [^12].

---

## 1. Problem & Goals

### 1.1 What promotion is

`POST /api/v1/chat/conversations/{dmKey}/promote`
(`pkg/hub/handlers_chat_v2.go:2460`, `handleConversationPromote`) turns a
user↔agent DM into a shared project thread. The handler's own doc comment states
the contract: *"promotes an agent DM conversation into a shared space thread,
**preserving all message history in place**."*

Authorization for the act is already correct and is **not** in scope to change:

| Step | Check | Line |
|---|---|---|
| 2 | key must be `dm:`-prefixed | :2474 |
| 3 | must be an *agent* DM — human↔human DMs refused | :2481 |
| 4 | caller must be a DM participant (`isDMParticipant`) | :2488 |
| 6 | caller must have project read access | :2508 |

That is a deliberate, participant-initiated disclosure by someone entitled to
make it, and the other party is an agent, so there is no second human whose
consent is being bypassed. **The widening of the audience is the feature, not
the bug.** This design does not relitigate it.

### 1.2 What is actually broken

Three faults, all confirmed by reading the code, none of them hypothetical.

**F1 — `PromoteDM` never mints a conversation id, and its caller does not
supply one.** The topic struct built at `handlers_chat_v2.go:2586-2596` sets
`ID`, `ProjectID`, `Name`, `DefaultAgent`, `CreatedBy`, `CreatedAt`,
`LastActivityAt` — and no `ConversationID`. Both stores gate conversation
creation on that field being non-empty (`webchannel_store.go:2106` computes
`writeConv := topic.ConversationID != "" && s.hasConversationsTable()`;
`webchannel_store_postgres.go:1642` tests it directly). The branch that creates
the conversation therefore **never executes in production**. It is live code
with no live caller — the same shape as DEF-101.

**The asymmetry is the whole of F1, and it is worth stating exactly.** The
topic-*creation* handler does not supply a conversation id either
(`handlers_chat_v2.go:461-471` — same seven fields, same omission). Natively
created threads are nonetheless fine, because **`CreateTopic` mints the id
itself**: `webchannel_store.go:695-698` under a `// DEF-89:` comment, and
`webchannel_store_postgres.go:328-330` unconditionally, with a documented note
that Postgres migrations guarantee the table so the sqlite
`hasConversationsTable()` gate is unnecessary there.

`PromoteDM` is the sibling function that never received that fix. So F1 is not
"a handler forgot a field" — it is **DEF-89 applied to one of two entry points**.

*Correction to my own August framing:* `DEFECTS.md` [^12] says of DEF-96 "it is
not the same fix" as DEF-89. That is right about F2 and about the ACL analysis
in §3.2, and it was the right call to stop a three-line patch. It is **wrong
about the conversation-minting half**, which is precisely the DEF-89 fix, at
precisely the site DEF-89 missed. The design below applies it there rather than
inventing a second pattern.

The skip is pinned as intended behaviour by
`TestPromoteDM_NoConversationID_SkipsDualWrite`
(`webchannel_store_dualwrite_test.go:386`), which asserts
`countConversations == 0`. That test is not wrong about the store; it is
correct about a store contract whose only caller passes the empty case.

**F2 — neither store re-points `messages.conversation_id`.** Step 2 of both
implementations updates one column:

```sql
UPDATE messages SET thread_id = ? WHERE thread_id = ?      -- sqlite :2149
UPDATE messages SET thread_id = $1 WHERE thread_id = $2    -- pg     :1658
```

Any message written while the conversation **write** switch was on carries
`conversation_id` = the *direct* conversation's id (stamped at
`handlers_chat_v2.go:1202`). After promotion those rows sit under the new
topic's `thread_id` while still naming the old direct conversation.

**F2′ — and the predicate itself barely matches. Found 2026-09-09, after this
design was written and dispatched.** The `WHERE thread_id = dmKey` clause above
is not merely incomplete in what it *sets*; it is nearly inert in what it
*selects*. Measured on gteam, read-only, over messages in `kind='direct'`
conversations: **22 carry a `thread_id`, 6,454 do not** — and of those 6,454,
**6,403 are agent messages**.

The cause is a split between two write paths. Web chat sets `ThreadID: key`
explicitly (`handlers_chat_v2.go:1060, 1159, 1319, 1460`). The agent messaging
path propagates whatever the caller supplied (`handlers_agent_messaging.go:292,
308, 1232, 1343` — all `req.ThreadID` / `structuredMsg.ThreadID`), and an agent
replying into a DM supplies nothing. Since agent replies are the majority of
every real DM, the column promotion keys on is empty for almost every row it
needs to move.

**So promotion today does not "move the messages but leave them mis-attributed."
It leaves the conversation behind almost in its entirety** — a handful of
user-authored rows follow the topic and everything else stays put. F2 is
therefore worse than filed, and the fix originally designed here inherited the
same predicate and would have shipped a half-move that looked like a fix. C2 is
corrected accordingly.

**F3 — the consequence, with the read switch on.** Chat history for a thread is
fetched by conversation, not by thread id:

```go
filter = store.MessageFilter{Channel: "web", ConversationID: convResult.ConversationID}
// handlers_chat_v2.go:1914-1917
```

`convResult` for a native topic comes from `ResolveThreadConversationForRead`
(`pkg/messaging/conversation.go:396`), which resolves through the topic's linked
`conversation_id`. For a promoted topic that column is NULL, so the function
takes its *"topic exists but not yet backfilled"* branch and returns `nil`, and
the handler falls to the no-fallback arm: a typed error, on the grounds that
*"thread conversations are created by the topic system, so absence is genuine
drift."* The comment is right; promotion is the path that violates it.

**The observable outcome for a user:** the DM disappears (registry row deleted,
step 4), and the new thread errors instead of showing the history it promised to
preserve.

**It then gets quieter, not better.** `backfillTopicConversations`
(`webchannel_store.go:1458`) runs at startup and mints a group conversation for
any topic with a NULL `conversation_id`. After the next hub restart the promoted
topic resolves to that new conversation — which contains none of the re-keyed
messages, because F2 left them pointing at the direct conversation. The error
becomes **an empty list**. A fault that announces itself is replaced by one that
does not.

No data is destroyed. The rows are intact and unreachable from both sides.

### 1.3 Goals

- **G1.** A promoted thread shows exactly the history the DM contained.
- **G2.** After promotion, exactly one conversation is the authority for who may
  read that history, and it is the group conversation.
- **G3.** Promotion is atomic: either the thread exists with its history and its
  conversation, or nothing changed.
- **G4.** Invariant **D-1** is preserved *literally* — the direct conversation's
  participant set is not modified, not extended, and not consulted afterwards.
- **G5.** No new field, and no existing field, acquires an authorization meaning
  as a result of this change.

---

## 2. Non-Goals

- **Changing who may promote.** §1.1 stands as-is.
- **Human↔human DM promotion.** Still refused at step 3. It is a genuinely
  harder problem — two humans, so disclosure needs consent from a party who is
  not the caller — and it must not be smuggled in behind a bug fix.
- **Un-promotion / demotion.** Promotion stays one-way. A reverse operation
  would be a narrowing of an audience that has already read the content, which
  is not a thing software can perform.
- **Changing the DM key format, `isDMParticipant`, or DM read authorization.**
  All on the prohibition list.
- **Repairing already-promoted topics.** Designed in §5, but gated on ptone
  (§6, OQ-96-1) because it is an irreversible write to production data.

---

## 3. Proposed Design

### 3.1 Shape

Promotion becomes: *mint a group conversation for the new topic, move the
messages onto both the new thread id and the new conversation id in one
statement, and leave the direct conversation row untouched.*

Concretely, three changes.

**C1 — `PromoteDM` mints the conversation id, in the store, exactly as
`CreateTopic` does.** Not in the handler.

```go
// sqlite, at webchannel_store.go:2105, replacing the writeConv computation.
// U-TX-1: hasConversationsTable() touches s.db and must run BEFORE BeginTx.
// The existing code already calls it here, so the ordering is unchanged.
hasConv := s.hasConversationsTable()
if topic.ConversationID == "" && hasConv {
    topic.ConversationID = uuid.New().String()   // DEF-89 pattern
}
writeConv := topic.ConversationID != "" && hasConv
```

```go
// postgres, at webchannel_store_postgres.go:1626, before the INSERT.
// Unconditional, matching pg CreateTopic: migrations guarantee the table.
if topic.ConversationID == "" {
    topic.ConversationID = uuid.New().String()
}
```

**Why the store and not the handler.** Three reasons, in order:

1. It is the pattern already established in the same two files for the sibling
   function, including the sqlite/postgres asymmetry and its rationale. A second
   pattern for the same job is a future reader's trap.
2. It fixes every caller at one site. The handler is the only caller today; that
   is a fact about today.
3. The U-TX-1 discipline — `hasConversationsTable()` strictly before `BeginTx`,
   because at `MaxOpenConns=1` an ambient-pool touch inside the transaction
   **hangs rather than fails** — is implemented in the store and nowhere else.
   Minting in the handler would leave that reasoning split across two files.

Note the returned `*WebChatTopic` now carries the minted id, so the handler's
`promoteResponse` gains a `conversationId` for free. That is a response-shape
change; harmless, but it should be stated in the phase report rather than
discovered.

This activates the store branch that already exists and is already tested
(`TestPromoteDM_DualWrite_WritesConversation`,
`webchannel_store_dualwrite_test.go:334`). The unique index
`idx_webchat_topic_conversation` is satisfied by a fresh UUID.

**C2 — step 2 re-points both columns, and is keyed on the conversation, not the
thread id.**

> **CORRECTED 2026-09-09, after measurement, mid-implementation.** The first
> version of this section kept the existing `WHERE thread_id = dmKey` predicate
> and merely added `conversation_id` to the SET clause. **That was wrong, and it
> would have shipped a fix that moved almost nothing.** See §1.2 F2′ and §5.4b.

The measurement that forced this, taken on gteam read-only:

```
DM messages (conversation.kind = 'direct')
  with thread_id:      22
  without thread_id: 6,454      (agent 6,403 / user 48 / other 3)
```

**99.7% of DM messages carry no `thread_id` at all.** The web chat write path
sets it (`handlers_chat_v2.go:1060, 1159, 1319, 1460`); the agent messaging path
takes it from the caller (`handlers_agent_messaging.go:292, 308, 1232, 1343`,
all `req.ThreadID` / `structuredMsg.ThreadID`), and agents replying into a DM do
not supply one. Agent replies are the bulk of every real DM.

So the predicate must key on the thing DM messages actually carry:

```sql
-- sqlite, replacing webchannel_store.go:2148-2154
UPDATE messages
   SET thread_id = ?, conversation_id = ?
 WHERE (? <> '' AND conversation_id = ?)      -- directConvID, when resolved
    OR thread_id = ?                          -- dmKey, legacy arm

-- postgres, replacing webchannel_store_postgres.go:1657-1663 — same shape
```

**Both arms, not either.** The `conversation_id` arm carries the modern
population; the `thread_id` arm carries messages written before conversation
stamping, which have an empty `conversation_id` and are invisible to the first
arm. Dropping either one silently strands a population, which is the defect we
are fixing.

The `? <> ''` guard on the first arm is load-bearing: if `directConvID` were
empty and the guard absent, `conversation_id = ''` would match **every
unstamped message on the hub**. That is not a hypothetical — 
`CountUnbackfilledMessages` exists precisely because that population is large.
An empty needle must never become a wildcard.

**Where `directConvID` comes from.** The handler already holds the authorized
DM key; the direct conversation is a lookup on it —
`GetConversationByExternalRef(ctx, "native", dmKey)`, since
`ResolveOrCreateDMConversation` writes `Kind: "direct", Surface: "native",
ExternalRef: <DM key>` (`conversation.go:107-113`). **Lookup only — promotion
must never *create* a direct conversation.** If the lookup returns
`store.ErrNotFound`, pass `""` and let the legacy arm do the work; that is a
pre-conversation-model hub, not an error.

No new authorization surface: the key was already validated as a DM key and the
caller already proven a participant (`handlers_chat_v2.go:2474-2508`), so the
conversation id is derived from an authorized key rather than supplied.

**U-TX-1 HAZARD, introduced by this correction — resolve before `BeginTx`.**
`GetConversationByExternalRef` is an ambient-pool call. `PromoteDM` runs its
work inside a transaction, and at `MaxOpenConns=1` the transaction holds the
only connection, so an ambient call made inside it waits forever for a second
one. **It deadlocks; it does not fail.** The lookup must happen **before**
`BeginTx` and the id passed in, exactly as `hasConversationsTable()` already
does at `webchannel_store.go:2106`.

This hazard did not exist in the original C2 — that version needed no lookup,
because `dmKey` was already in hand. **The correction created it**, which is
worth stating plainly: widening a predicate from a value the caller holds to a
value the caller must fetch converts a pure statement into one with an ordering
constraint against the transaction boundary. Any test touching this path needs
an explicit timeout, because the failure mode consumes the evidence as well as
the time.

**The signature must not grow a second bare string.** Adding `directConvID`
alongside `dmKey` creates a run of two adjacent `string` parameters. Transposing
them **compiles, runs, and silently matches nothing** — `conversation_id =
dmKey` selects no rows and `thread_id = directConvID` selects no rows, so
promotion moves zero messages and reports success. That is this very defect,
reintroduced by argument order, undetectable by the type checker and invisible
to any test whose fixture happens to be empty. Pass a named struct:

```go
type PromoteKeys struct {
    DMKey                string
    DirectConversationID string // "" when unresolved
}
```

Named fields make a transposition a compile error and make the `""` case
self-documenting. → **when a change adds to a run of same-typed arguments,
type-checking stops being evidence and the mapping has to be read** — the same
rule already recorded for *removing* from such a run, which is symmetric and
was not written down that way.

One statement, not two, so there is no window in which `thread_id` has moved and
`conversation_id` has not. Inside the existing transaction, so G3 holds.

Note this also *populates* `conversation_id` on pre-write-switch rows that had it
empty. That is the same act `backfillTopicConversations` performs, applied to
messages rather than topics, and it is correct: after promotion those messages
genuinely belong to the group conversation.

**C2a — the re-point must be conditional on C1.** If `topic.ConversationID` is
still empty after C1, the statement must set only `thread_id`, exactly as today.
Otherwise the re-key would blank `conversation_id` on every moved row — turning
a recoverable gap into destroyed attribution.

After C1 that branch is reachable only in the sqlite store, only when
`hasConversationsTable()` is false — i.e. an environment with no conversations
table at all, where writing a conversation id would be the dangling-reference
bug DEF-22 exists to prevent. **Keep the guard even though it is nearly
unreachable.** Its cost is one `if`; the cost of omitting it is silent data
damage in the one configuration nobody tests.

**C3 — the direct conversation row is not touched.** Not deleted, not
soft-deleted, not re-kinded, not re-participanted.

Rationale, in order of weight:

1. **D-1 is satisfied by inaction.** Nothing can violate the immutability of a
   participant set that is never written.
2. **`external_ref` remains the DM key, so the DM resurrects cleanly.** If the
   user later DMs the same agent, `ResolveOrCreateDMConversation` finds the
   existing direct row by `external_ref` and reuses it — no duplicate, no
   unique-constraint collision, and no second key derivation. The new DM starts
   empty, which is the right user-visible behaviour: the old messages moved to
   the thread.
3. **Deleting it would be the risky option.** A soft-delete would leave a row
   that `GetConversationByExternalRef` may or may not skip depending on the
   caller, and *"soft-deletion is not declassification"* is already a standing
   rule here. A hard delete would break resurrection.

After promotion the direct conversation is a real row with a real participant
set and zero messages. That is an accurate description of what happened.

### 3.2 Where the ACL lives afterwards

| Before | After |
|---|---|
| history authority = direct conversation, participants derived from the DM key | history authority = group conversation, participants derived from project membership |
| readable by: the user and the agent | readable by: project members |

Exactly one authority at a time, which is G2. Per DEF-36 the group conversation
does **not** populate the participant table — group membership is derived from
the project, and the participant table stays a listing index rather than an
access authority.

**One scoping detail that falls out and should not be a surprise later.** DM
conversations are deliberately *global*: `ResolveOrCreateDMConversation`
(`pkg/messaging/conversation.go:113`) leaves `ProjectID` nil, with the comment
"DM conversations are global — ProjectID is intentionally nil." The promoted
group conversation is created with `project_id = topic.ProjectID`. So promotion
does not merely widen the audience, it **acquires a project scope the messages
did not previously have**. That is correct — the whole point is to place the
history inside a project — but it means the promoted conversation is now subject
to project-level operations (retention, project deletion, project-scoped
listing) that never applied to it as a DM. Recorded so that a future
project-deletion or retention change is evaluated against promoted history
rather than surprised by it.

### 3.3 Provenance — and why it is *not* stored on the conversation row

Promotion is an irreversible disclosure and it currently leaves no durable trace
tying the thread back to the DM. The obvious place to record one is the
`parent_ref` column, which both promote paths already write as `''`.

**Rejected.** `parent_ref` is not an internal lineage field. It is a
caller-supplied external threading reference, bound straight from request JSON
on two live paths — `handlers_broker_inbound.go:261` and
`handlers_agent_messaging.go:1041`, both funnelling into
`messaging.WithParentRef`. Putting a DM key in it would:

- overload a field whose existing meaning is "external parent thread", and
- attach lineage semantics to a value that an inbound caller can set,

which is precisely how a descriptive field becomes an authorization input by
accident. G5 forbids it. Option C in §4 shows what that failure looks like when
followed to its end.

**Instead:** emit one structured log record at promotion —
`promoted_dm` with `dm_key`, `topic_id`, `conversation_id`, `actor`,
`message_count` — and store nothing new on the row. The HTTP response already
returns `promotedFrom` for the immediate caller.

One thing worth stating so nobody generalises from it: logging the DM key here
discloses nothing the promoted topic does not already reveal, because the topic
carries `default_agent` (the agent) and `created_by` (the promoter), and those
are the two principals the key encodes. That reasoning is specific to
*participant-initiated agent-DM promotion* and does not license writing DM keys
anywhere else.

---

## 4. Alternatives Considered

**Alternative A — re-kind the direct conversation into a group conversation.**
Point the topic at the *existing* conversation id, flip `kind` `direct` →
`group`, and skip re-pointing messages entirely. Cheapest possible diff: no
`UPDATE` of `messages` at all.

*Rejected.* It produces a `group` conversation whose `external_ref` is a DM key,
and *a direct conversation's `external_ref` IS its access-control basis*. Every
piece of code that reasons about DM keys — `isDMParticipant`, the read-gate, the
divergence report, the derivation golden vectors — would then be looking at rows
whose kind says one thing and whose key says another. It also mutates a direct
conversation in place, which is what D-1 exists to prevent, and it forfeits
resurrection: the DM key is consumed, so a later DM to the same agent either
collides on `external_ref` or silently joins the project thread. The saving is a
single `UPDATE`; the cost is the coherence of the key.

**Alternative B — copy the messages instead of moving them.** Insert duplicates
under the new conversation and leave the originals in the direct conversation.

*Rejected.* Two authorities for the same content, permanently. Every subsequent
question — edits, deletions, read state, retention, the divergence report's row
counts — has two answers. It also doubles the exposure surface of the disclosure
without any compensating benefit, since the caller already consented to the
widening. Cheap to build, expensive forever.

**Alternative C — leave `conversation_id` alone and teach the read path to
union the promoted thread's conversation with its ancestor.**

*Rejected, and it is the most instructive rejection.* It appears to preserve the
most information, and it is the option that makes storing lineage on the row
feel necessary. But it means a reader of the group conversation is authorized
partly by a *direct* conversation's ACL, which is the union of two access models
evaluated at read time — the shape most likely to widen by accident under a
later refactor. It also makes lineage load-bearing, so `parent_ref` (§3.3,
request-bindable) becomes an authorization input. Under-granting is recoverable;
this is a design whose failure mode is over-granting.

**Alternative D — refuse promotion while the read switch is on.** Fail closed
until the fix ships.

*Rejected as the shipped design, but note it is a legitimate hotfix* if repair
turns out to be slow: it converts silent history loss into an honest refusal.
Recorded here so the option is visible rather than rediscovered under pressure.
It is not proposed, because the fix is small and the switches are meant to go
away entirely (switch-collapse).

---

## 5. Migration / Rollout

### 5.1 New promotions

C1–C3 are behaviour-corrective and need no switch. A promotion performed after
the fix is correct on the first read, with no restart dependency. This is
consistent with the single-cutover directive: **no third switch.**

### 5.2 Already-broken data

Two populations, and they need different treatment.

**P1 — topics promoted before the fix, hub not yet restarted.** `conversation_id`
is NULL on the topic. Startup backfill will mint one; C2's repair (below) must
then re-point the messages, or backfill alone will convert the error into a
permanent empty thread. **Backfill without the message repair makes P1 worse,**
so the two must ship together.

**P2 — topics promoted before the fix, hub already restarted.** Topic has a
backfilled `conversation_id`; messages still name the direct conversation.
Repair is the same operation.

**Repair, stated precisely.** For each `webchat_topic` T with
`conversation_id = C`, re-point message rows where `thread_id = T.id` **and**
`conversation_id` is either empty or names a `direct` conversation:

```sql
UPDATE messages SET conversation_id = :C
WHERE thread_id = :T
  AND (conversation_id = '' OR conversation_id IN (
        SELECT id FROM conversations WHERE kind = 'direct'))
```

**The `conversation_id = ''` arm is not DEF-96-specific and must be decided
separately.** Messages written before the conversation *write* switch was turned
on carry an empty `conversation_id` regardless of how their thread came to
exist. That predicate therefore also sweeps ordinary, natively created threads.
Re-pointing those may well be desirable — it is what a full backfill would do —
but it is a different change with a different blast radius, and folding it in
here would let a DEF-96 repair quietly become a general message backfill.
**Measure the two populations separately (§5.3 Q2 vs Q4) and decide the arm on
the numbers, not on the convenience of one `WHERE` clause.**

Constraints on the repair, all load-bearing:

- **It must not touch rows already naming a `group` conversation.** Those are
  natively created threads and are correct.
- **It must be counted before it is run.** The count is the evidence that the
  predicate selected what was intended.
- **It is an irreversible widening of read access** for the affected rows — from
  a DM pair to a project. That is the disclosure the promoter already
  authorized, arriving late. It is still ptone's gate (OQ-96-1).
- Standard rules apply: never point a write-capable migration at the live DB or
  a retention snapshot; do not hand-edit `_migrations` (M-2).

### 5.3 Measuring the blast radius first

Before any repair is authorized, a **read-only** count from gteam:

- **Q1** — topics with NULL `conversation_id` and `deleted_at IS NULL` (the
  pre-restart population).
- **Q2** — message rows whose `thread_id` is a topic id and whose
  `conversation_id` names a `direct` conversation. **This is the exact DEF-96
  repair scope.**
- **Q3** — how many distinct threads Q2 spans.
- **Q4** — message rows under a topic with an empty or NULL `conversation_id`,
  counted **separately** from Q2 for the reason given above.

Read-only connection (`file:...?mode=ro`). IDs and keys only — **no message
bodies, no bulk email addresses, and never the `value` column of
`hub_settings`.** This is instance-investigator's work, not a developer's.

Q2 may well be zero: promotion is a niche feature and gteam is one instance.
**If Q2 is zero, the repair migration should not be written.** An unnecessary
irreversible migration is a liability, not diligence.

### 5.4 MEASURED — 2026-09-09, gteam, read-only

| | Result |
|---|---|
| Q1 — topics with no conversation | **0** |
| Q2 — messages under a topic attributed to a `direct` conversation | **0** |
| Q3 — distinct threads Q2 spans | **0** |
| Q4 — messages under a topic with empty/NULL `conversation_id` | **0** |

**Positive controls, requested before believing the zeros**, because a JOIN that
returns 0 because its `ON` clause never matches is indistinguishable from one
whose predicate found nothing:

| | Result |
|---|---|
| C1 — `messages JOIN webchat_topic ON thread_id = id` | **61** |
| C2 — topics | **41** |
| C3 — conversation kinds | direct **737**, group **46** |

C1 is non-zero, so the join is matching real rows and the zeros are facts about
the data.

**The zeros are time-unbounded, which matters more than the log evidence.** A
promotion strands messages in exactly one of two states: after the write switch,
carrying the *direct* conversation's id (Q2); before it, carrying an empty
`conversation_id`, which promotion never fills (Q4). Both are zero over all
rows with no date predicate, so between them they cover both eras. The correct
claim is **"no promotion has ever stranded a message on this hub"** — not the
weaker "no promotion since Sep 6" that the journal window supports. The only
case invisible to the queries is promotion of a DM with no messages, which
strands nothing.

**Decisions taken on this measurement:**

- **P5 (repair migration) is cancelled.** Not deferred — cancelled. There is
  nothing to repair, and writing an irreversible migration for an empty
  population is a liability.
- **OQ-96-1 is withdrawn.** ptone has no decision to make here.
- **The `conversation_id = ''` arm question is moot** — Q4 is zero, so the two
  populations the arm would have distinguished are both empty. Recorded rather
  than deleted, because the reasoning applies to any future backfill.

*Loose end, non-blocking, and the query was mine to get wrong:* C2 = 41 topics
against C3 = 46 group conversations, so five group conversations have no
`webchat_topic` row. I guessed soft-deleted topics; the measurement says
`deleted_at IS NOT NULL` count is zero, so that guess was wrong.

**But the five are probably not orphans either, because my predicate was
under-specified.** It read `kind = 'group' AND NOT EXISTS (topic)`, which
assumes every group conversation is a native web thread. Group conversations
also exist for Discord channels and threads and other non-native surfaces, and
those correctly have no `webchat_topic`. The missing discriminator is `surface`.

The tell was in the data: one of the five is `0c57b491`, ptone's preserved
Discord reproduction conversation — a row for which "no topic" is right.
Re-issued as a `GROUP BY surface`; only rows with `surface = 'native'` are
genuine drift. → **and note this only surfaced because the report included the
ids rather than just the count. A bare "5" would have become a defect filed
against correct behaviour.**

---

### 5.4b MEASURED — 2026-09-09, gteam, read-only — the DM `thread_id` split

Taken after §5.4, in response to a `thread_id` anomaly noticed while
reconciling DEF-156 counts. It invalidated C2's original predicate.

Messages whose `conversation_id` names a `kind='direct'` conversation:

| | count |
|---|---|
| with `thread_id` | 22 |
| without `thread_id` | 6,454 |
| — of those, sender is an agent | 6,403 |
| — sender is a user | 48 |
| — other | 3 |

**99.7% have no `thread_id`.** The 3 "other" are an unprefixed display name
rather than a `user:`-prefixed principal — the known old-format population, not
a new finding.

**What this measurement is and is not.** It is a strong statement about the
*current* population on one hub. It is **not** a statement that the ratio holds
everywhere: it reflects how much of this hub's DM traffic is agent-authored,
which varies. The design does not depend on the ratio — a single stranded agent
reply is enough to require the fix — so the number is motivation, not a
parameter. Recording the distinction because a percentage in a design document
invites being treated as a constant.

**Why it was not found earlier.** §5.4 measured the *orphan* populations the
repair migration would have targeted, and correctly returned zero. It never
asked what fraction of DM messages the re-key predicate would actually match,
because the predicate was inherited from working code and was not on the list
of things under suspicion. → **a measurement plan derived from the fix you
intend to make cannot test the assumption the fix rests on.**

---

## 6. Open Questions

**~~OQ-96-1 (ptone)~~ — WITHDRAWN 2026-09-09.** Was: authorize the repair
migration for already-promoted topics? §5.4 measured the affected population at
zero, with positive controls confirming the queries matched real rows. No
irreversible write is needed, so there is nothing to authorize. Kept struck
through rather than deleted so the sequence — *ask for the count before asking
for the decision* — stays visible.

**OQ-96-2 (mine, non-blocking).** Should promotion write a message into the new
thread recording that it was promoted from a DM, so project members reading the
history know its provenance? Product-visible.
*My read: yes, eventually, but not in this fix* — it is a UX change and this is a
correctness fix, and bundling them makes the correctness fix harder to review.

**OQ-96-3 (mine, non-blocking).** After promotion the direct conversation has
zero messages but a stale `last_activity_at`. Does anything sort or filter DM
lists by it in a way that leaves a phantom entry? The DM *registry* row is
deleted, which is what the DM list reads, so I believe not — but I have not
traced every consumer.

---

## 7. Implementation Phases

Commit-sized, in dependency order. **Each phase reports a per-file numstat
against the briefed base commit, with `merge-base --is-ancestor` confirmed
before any edit.**

**P0 — resolve the direct conversation id and widen the predicate.** *(Added
2026-09-09 with the C2 correction; this is now the first commit.)* Look the
direct conversation up by `external_ref` from the already-authorized DM key,
thread it into `PromoteDM`, and key the re-key on both arms per C2. Assert the
empty-needle guard explicitly: with `directConvID = ""`, the statement must
match **nothing** via the first arm. **Test that by planting an unstamped
message belonging to a different conversation and asserting it does not move** —
a test that only checks the DM's own rows cannot detect a wildcard.

**P1 — store: re-point `conversation_id` on re-key.** C2 + C2a in both
`webchannel_store.go` and `webchannel_store_postgres.go`. **No minting yet**, so
production behaviour is unchanged: the caller still passes an empty id and
C2a's guard holds. Tests: extend the existing dual-write tests to assert the
messages' `conversation_id` after promotion when an id *is* supplied, and add
one asserting that when it is empty the column is **left alone, not blanked**.

Sequenced first deliberately. P1 alone is inert; P2 alone would move messages
onto a new conversation while leaving them attributed to the old one — i.e. it
would produce the permanently-empty thread of §1.2 rather than fix it. If only
one of the two ever lands, it must be this one.

**P2 — store: mint the conversation id in `PromoteDM`.** C1, both stores,
following the DEF-89 pattern at `webchannel_store.go:695-698` and
`webchannel_store_postgres.go:328-330`. This is the commit that changes
observable behaviour.

Test at **handler** level, not store level: promote a DM containing messages,
then read the thread's history through the same endpoint the UI calls, and
assert the messages come back. A store-level test would pass with the pre-fix
handler and prove nothing about the path the user takes. Also assert the
`conversationId` now present on `promoteResponse`.

**P3 — provenance log.** §3.3. Structured record, no schema change.

**P4 — read-only measurement on gteam. DONE 2026-09-09**, results in §5.4.
Ran ahead of P1–P3 because it decides whether P5 exists at all.

**P5 — repair migration. CANCELLED 2026-09-09.** P4 returned zero on every
population with sound positive controls (§5.4). No rows to repair. Do not write
this.

**P6 — close the pinned-skip test.** `TestPromoteDM_NoConversationID_SkipsDualWrite`
stays (the store contract is unchanged) but gains a comment stating that the
empty-`ConversationID` case is no longer reachable from the handler, so a future
reader does not infer that production takes it.

---

## 8. Acceptance Criteria

**AC-96-1.** Promote a DM with N ≥ 2 messages; the new thread's history endpoint
returns exactly those N messages, **on the first read, with no hub restart**.
The restart caveat is the point — a test that passes only after a restart is
testing the backfill, not the fix.

**AC-96-1a — the fixture must look like a real DM.** *(Added with the C2
correction.)* The majority of messages in the fixture must be **agent replies
written through the agent messaging path**, not hand-constructed rows with
`thread_id` pre-populated. On the live instance 99.7% of DM messages have no
`thread_id`; a fixture that sets it on every row would have passed against the
old, nearly-inert predicate and proved nothing. Assert every message moves **by
count and by id**, not that "messages appear."

**AC-96-1b — the empty needle is not a wildcard.** With no direct conversation
resolvable (`directConvID = ""`), promotion must move only rows matched by the
legacy `thread_id` arm. Plant an unstamped message belonging to an unrelated
conversation and assert it is untouched. This is the one failure mode of C2
that damages data outside the DM being promoted, so it gets its own criterion
rather than riding along in AC-96-1.

**AC-96-2.** After promotion, `webchat_topic.conversation_id` is non-empty, a
`conversations` row exists with that id and `kind='group'`, and every re-keyed
message names it.

**AC-96-3.** The direct conversation row is **byte-identical** before and after
promotion — same `kind`, same `external_ref`, same participant rows. Assert the
participant set explicitly; D-1 is the invariant most likely to be broken by a
well-meaning cleanup.

**AC-96-4.** Sending a new DM to the same agent after promotion reuses the
existing direct conversation (**same id**, resolved by `external_ref` through
`UpsertConversationByExternalRef`) and starts with an empty history. No
duplicate conversation, no constraint violation. Assert the id equals the
pre-promotion id — "a direct conversation exists" is not the same claim and
would pass if a second one had been minted.

**AC-96-5.** Atomicity: force a failure at step 3 or 4 and confirm no topic, no
conversation, and no re-keyed message survives. The existing
`TestPromoteDM_Atomicity` is the model.

**AC-96-6 — mutation, both directions, and this is the criterion that matters.**
- Revert C1 (`PromoteDM` stops minting the id): the P2 handler-level test must
  go **red**, and it must fail by reporting missing messages rather than
  erroring out.
- Revert C2 (`UPDATE` touches only `thread_id`): the P2 test must go **red**
  independently of the above.
- Restore each: **green**.
- A mutation that fails to compile is not a caught violation — make it
  compile-safe and re-run.

Two directions because C1 and C2 are jointly necessary and a single test could
otherwise pass while only one of them is present.

**AC-96-7.** No new authorization path reads `parent_ref`, and `parent_ref`
remains `''` on the promoted conversation. G5.

**AC-96-8.** Gates: `go build`, `go vet`, `gofmt -l` on changed files, and the
four structural gates (`compat-literals`, `check-authz-guards`,
`check-conversation-upsert-guard`, `check-security-marker-gates`). Any red is
reported to `ca-msg-arch`, **never tuned away** — and note that
`check-conversation-upsert-guard` watches this exact surface, so if it fires,
that is a finding and not an obstacle.

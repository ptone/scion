# §2.6.4 phases 5–7 — `default_agent` promotion, name resolution, `NOT NULL`

**Author: ca-msg-arch. Written 2026-08-27 22:52Z.**
Depends on phases 1–4 (link exists, dual-write live, mint path closed, backfill proven).
Naming authority settled by `nc-arch` 22:24Z. `parent_ref` scope settled by `nc-arch` 22:28Z.

---

## 1. Problem & Goals

Phases 1–4 create the `webchat_topic → Conversation` link and stop the divergence. They deliberately
change nothing user-visible. Phases 5–7 are the payoff: move the three things that still live only
in native chat onto the shared identity layer, then tighten the schema once the link is proven.

- **Phase 5** — `default_agent` currently lives on `webchat_topic` as an unvalidated string holding
  *either* a slug *or* a UUID. Promote it to `Conversation.default_agent_id` (a real UUID FK) and
  move `ClearTopicDefaultAgent` with it.
- **Phase 6** — `#<thread>` resolution must render a topic's name. This is the DEF-5 / DEF-7 unblock.
- **Phase 7** — make `conversation_id` `NOT NULL` once backfill is proven.

**Success criteria:** no user-visible behaviour changes except that things which silently did
nothing now either work or report. No topic loses a *working* default agent. No tombstoned topic
ever renders a stale name.

## 2. Non-Goals

- **Do not give `Conversation` a name.** Settled: option (a). See §4.1.
- **Do not write `Conversation.parent_ref` from native chat.** Settled: external surfaces only (§4.3).
- Do not change `is_general` — it stays purely native (Q3). A `Conversation` analogue would be a
  second unwritten field, which is the DEF-7 drift pattern this spec exists to stop.
- Do not touch read-state, prefs, or `webchat_dm`. Phase 3's boundary holds.
- Do not resolve the agent-reply-to-deleted-thread *policy* question (DEF-27 §5 left it open).

---

## 3. Phase 5 — `default_agent` promotion

### 3.1 The constraint that governs the design

`webchat_topic.default_agent` is **never validated at set time** (`handlers_chat_v2.go:579-580`
assigns straight through). Its single routing consumer (`:936-947`) does a **two-step lookup**:
`GetAgentBySlug(projectID, v)`, then `GetAgent(v)` by ID, and on double failure falls through to
`sendHumanToHuman` — the message posts and **no agent is woken, silently**.

So an unresolvable `default_agent` is *already* functionally NULL. Migrating it to NULL loses
nothing and records a truth the system currently hides.

> **The migration MUST use the runtime's two-step lookup.** The column holds slugs *or* UUIDs. A
> migration that resolves only slugs would NULL every UUID-valued default agent — and those are
> exactly the ones that work today. That is a regression introduced in the name of cleanup.

### 3.2 Design

```
  Conversation.default_agent_id  uuid  NULL          -- already exists, validated UUID
  webchat_topic.default_agent    text  (unchanged this phase, dropped in a later cleanup)
```

Migration, per topic, **all-or-nothing per row**:

```
  raw := topic.default_agent
  if raw == "" -> default_agent_id = NULL          (not a failure; nothing to promote)
  a, ok := GetAgentBySlug(topic.project_id, raw)   -- step 1: project-scoped, hides deleted
  if !ok { a, ok = GetAgentScoped(topic.project_id, raw) }   -- step 2: SEE 3.2.1
  if ok   -> default_agent_id = a.ID
  if !ok  -> default_agent_id = NULL  AND  record {topic_id, raw} in the operator report
```

### 3.2.1 Step 2 must NOT be a literal copy of the runtime lookup (revised 23:05Z, DEF-31)

My first draft said "migration resolution **is** the runtime's two-step lookup" and made that
AC-U-13. **That is now wrong and must not be implemented as written.** The runtime's step 2
(`handlers_chat_v2.go:938`) is `GetAgent(ctx, raw)` — `agent_store.go:294`, a bare primary-key fetch
with **no project filter and no `DeletedAtIsNil()`**, while step 1 one line above has both. See
DEF-31: that asymmetry is itself a live defect.

Copying it here would be worse than leaving it alone. The runtime bug is a *routing* decision made
per-send against a mutable surface column; the migration would **promote its output into
`conversations.default_agent_id`**, a stable identity column that later phases treat as
authoritative. **An ingress defect laundered into the identity layer stops looking like a defect** —
it acquires the authority of the column it lands in, and every later reader inherits it. This is
rule 56 in the other direction: not a surface concern leaking down, but a surface *defect* being
promoted up.

So the migration's step 2 is **project-scoped and deleted-filtered**, matching step 1:

- Resolve by UUID **only within `topic.project_id`**, and only for a non-deleted agent.
- Where the scoped lookup and the runtime lookup **disagree** — i.e. the runtime would have routed
  and the migration will not — that is a **report line, not a replication**. Those rows are exactly
  the DEF-31 exposures and the operator needs their list. **AC-U-15.**
- The migration must not be blocked on DEF-31 being fixed first. Scoping here is correct
  independently: a `conversations.default_agent_id` naming an agent outside the conversation's
  project is not a defensible row under any resolution of DEF-31.

`ClearTopicDefaultAgent` moves to operate on `Conversation.default_agent_id`. Its native-chat
signature stays; only the storage target changes.

### 3.3 Why NULL and not "refuse to migrate"

Under-granting is recoverable, over-granting is not — but note this is **not** an authorization
decision, so that rule is not what drives it (rule 52's caution). Refusing to migrate would block
cut-over on rows that need an operator, not a blocked deploy. NULL records reality.

> **Correction 23:05Z.** My original justification here was "the unresolvable set is **already
> inert** — it does nothing today." **That was false, and DEF-31 is why.** A `default_agent` holding
> a foreign-project or soft-deleted agent UUID is *not* inert: the runtime's unscoped step 2 resolves
> it and routes live traffic to it. The rows I called dead are the exposed ones. The conclusion
> (NULL, plus a report) survives — but on the opposite reasoning, and that matters: it is now
> **remediation**, so **AC-U-14's report is the deliverable, not a courtesy**. An operator must see
> every row whose routing the migration just severed, because some of those routings were working.

---

## 4. Phase 6 — `#<thread>` resolution and the name

### 4.1 The naming ruling (nc-arch, adopted)

**The `Conversation` row carries NO name.** `display_name` stays empty. The tx-scoped ent client
must simply not call `SetDisplayName`; the field is `Optional().Default("")`, so nothing forces it.

The decisive argument is not that a mirror would be *clobbered* — it would not be; `SetDisplayName`
is guarded (this spec's earlier claim to the contrary was wrong, see `unification-spec.md` §3.7
correction). It is:

> **"Explicitly non-authoritative" is not a stable state for a populated column.** It gets read by
> accident and then relied upon. The only safe non-authority is **absence.**

Secondary: a mirror is a second holder of the name, which is the drift the reverse-pointer direction
exists to eliminate. One writer was the whole point.

### 4.2 Three accessors, each named for its question

This is the load-bearing structural decision of phase 6, and it is a direct consequence of DEF-27.
`GetTopicConversationID` was one function answering two questions whose `deleted_at` requirements
were **opposite**. Phase 6 adds a third question, so name all three:

| Question | Accessor | `deleted_at` |
|---|---|---|
| "What is this LIVE topic called?" | `GetTopicNameForDisplay` | **HIDE** deleted |
| "What conversation does this live topic link to?" | `GetTopicConversationID` | **HIDE** deleted |
| "Is this thread one of ours?" (mint guard) | `GetTopicConversationIDIncludingDeleted` | **SEE** deleted |

> **A tombstoned topic renders `[deleted]`, never a stale name.** Name-for-display is a *visibility*
> question, so it belongs on the hide-deleted side. If an implementer reaches for the identity
> accessor to get a name — it is right there, it already sees the row — tombstoned names leak.
> State this at the accessor, not only here.

Non-native surfaces render through the link:

```sql
SELECT name FROM webchat_topic WHERE conversation_id = ? AND deleted_at IS NULL
```

on the unique `idx_webchat_topic_conversation` (§3.2). Native chat stays sole source of truth.

**If join latency ever becomes real** — no evidence at this scale — the answer is a read-through
resolver with native chat as source, **never** a writable column.

### 4.3 `parent_ref` (nc-arch, 22:28Z)

Native chat does **not** populate `Conversation.parent_ref`, and the tx-scoped client must leave it
at its zero value, same as `display_name`. Thread→space parentage is carried by `project_id`, not by
a conversation→conversation ref (spaces are not conversations). Native replies are message-level
(`message.reply_to`) and never spawn a child conversation.

*Future note:* if a later wave models sub-threads as **child conversations**, `parent_ref` becomes
native-relevant. DEF-28's guard (landed) protects that path in advance.

---

## 5. Phase 7 — `conversation_id NOT NULL`

Pure cleanup, and the only phase that can fail destructively. Gate it hard.

**Precondition, verified not assumed:** zero rows in `webchat_topic` with `conversation_id IS NULL`,
**including soft-deleted rows.** A migration that scopes to live topics leaves tombstoned rows NULL
and the constraint fails at apply time — or worse, silently drops them on a table rebuild.

> **This is the DEF-27 lesson applied to a migration: soft-deletion is not exclusion.** The backfill
> and the precondition check must both count tombstoned topics.

### 5.1 Check-scope must equal remediation-scope (nc-arch, 22:55Z — adopted)

The precondition above is **incoherent unless the phase-3 backfill writes the same rows it counts.**
A check scoped to all topics against a backfill scoped to live ones is perpetually red with no
remediation path — which is worse than not checking at all, because it trains the operator to
override the check. That override is then in the runbook forever.

> **Any backfill that PREPARES for a constraint must WRITE the same row set the constraint will
> ENFORCE. Check-scope and remediation-scope must be identical — here, all rows including
> tombstoned.**

Secondary benefit, and it is a real behaviour improvement rather than just constraint hygiene: once
tombstoned topics carry a `conversation_id`, a post-delete agent reply resolves to the dead
conversation instead of being stored unlinked. That is strictly better than the degraded outcome
DEF-27 settled for in its §8.

### 5.2 The unresolved branch is permanent — assert, never delete (nc-arch, 22:55Z — adopted)

My first draft said "do not delete the unresolved branch as unreachable, it only becomes unreachable
at the last step." That understates it. **Unresolved has three causes, and `NOT NULL` removes only
one:**

| Cause | Removed by phase 7? |
|---|---|
| (i) topic exists, `conversation_id` empty | **yes** |
| (ii) infrastructure/query error | **no — permanent** |
| (iii) malformed or unparseable thread ref | **no — permanent** |

So the branch itself is permanently reachable and must never be deleted. Only sub-case (i) goes
statically unreachable, and it becomes a **defensive assertion or hard error, not a deletion**
(rule 20, the funnel/sink). A re-nullabling migration, or a topic inserted by a path that skips the
dual-write, silently re-enables the mint path through the hole where the guard used to be.

### 5.3 Ruling — the conversation row must NOT mirror `deleted_at` (22:57Z)

**Question (nc-arch):** should the backfilled conversation for a soft-deleted topic itself carry
`deleted_at`? **Answer: no. Do not mirror.** This is the opposite of the intuitive choice, so the
reasoning is recorded rather than left to be re-litigated.

`UpsertConversationByExternalRef`'s existing-row lookup filters `conversation.DeletedAtIsNil()`
(`conversation_store.go:391`). A tombstoned conversation is therefore **invisible to the upsert's
"does this already exist" question, so the upsert mints a second row for the same
`(surface, external_ref)`.**

That is **DEF-27 exactly** — a hide-deleted predicate answering an is-this-ours / does-this-exist
question, and answering "no" about a row that exists — reproduced one layer down on the identity
table. It would be worse than the original, because DEF-27's fix lives at the webchat layer and does
not reach `conversation_store` at all: we would have fixed the shadow-row bug for topics and re-armed
it for conversations, with the same wrong predicate, days apart.

> **Visibility filtering belongs to the surface that owns visibility, never to the shared identity
> layer.** A soft-delete predicate in an identity-layer query is a defect smell: a surface visibility
> decision has leaked onto the layer that exists to be stable. `webchat_topic` owns native-chat
> visibility and already hides deleted rows. The conversation row is identity, and **soft-deletion is
> not declassification** at any layer.

**Cost of this ruling, checked rather than assumed.** Nothing user-facing renders conversations
directly today: `ListConversations` (`:270`) already excludes soft-deleted rows, and every caller is
internal — `dm_migration.go:153`, `dm_migration.go:563`, `resolve.go:444`. The residual is pinned by
**AC-57-9** rather than left as prose.

---

## 6. Invariants any implementer must not break

- **U-TX-1** — inside the atomic topic-create block, nothing may touch the ambient pool: not `s.db`,
  not `s.client`. Only the tx-bound executor (`entsql.Conn{ExecQuerier: tx}`) and a tx-scoped ent
  client. **SQLite runs at `MaxOpenConns=1`, so an ambient-pool access inside a tx HANGS rather than
  erroring.** A test for this must assert with a timeout, or it will hang CI instead of failing.
- **Soft-deletion is not declassification** — a tombstoned native topic is still a native topic for
  "should I mint."
- **One function, one question** — if a lookup acquires a second caller wanting different
  `deleted_at` behaviour, split it and rename both. Do not add a boolean parameter.

---

## 7. Alternatives considered

**(A) Mirror `webchat_topic.name` into `Conversation.display_name`.** Rejected. §4.1 — a populated
non-authoritative column is a latent second authority, and the drift it creates is DEF-7 itself
walking back in. The `default_agent` column is the cautionary case: write-only until it wasn't.

**(B) Keep `default_agent` as a string on `Conversation` rather than a UUID FK.** Rejected. It would
transplant the defect rather than fix it — the whole problem is a column holding two different kinds
of identifier with no validation. A UUID FK makes the invalid state unrepresentable. Cost: the
two-step migration lookup (§3.1), which is one-time.

**(C) Do phase 7 first, then backfill under the constraint.** Rejected — inverted. The constraint
cannot be applied while NULLs exist, and applying it early forces the backfill to run as part of a
schema migration, where a partial failure is far harder to recover from than a re-runnable backfill.

**(D) A single `GetTopicName(includeDeleted bool)` instead of three named accessors.** Rejected. A
boolean parameter reproduces DEF-27 exactly: the caller must know which behaviour it needs, and the
wrong default is invisible at the call site. Naming each function after its question makes the wrong
choice hard to make silently.

---

## 8. Migration / rollout

Order is fixed; each step is independently revertible until phase 7.

1. Phase 5 migration runs **read-only first** in report mode (AC-U-14's report, no writes). Review
   the unresolvable list before any write. This is free and it is the only chance to notice a broken
   two-step lookup before it NULLs live data.
2. Phase 5 writes. Revertible: `default_agent` string column is untouched this phase.
3. Phase 6. Pure read path; no data change.
4. Phase 7 precondition check (including tombstoned rows). **If non-zero, stop** — do not proceed
   and do not "clean up" the offending rows without a separate decision.
5. Phase 7 constraint. Not revertible without a table rebuild. Last.

Dropping `webchat_topic.default_agent` is **not** in this spec — it is a later cleanup, after phase 5
has been live long enough to trust.

---

## 9. Open questions

**Q4 — CLOSED 22:55Z by nc-arch.** No non-native surface needs a topic name today. Phase 6 ships the
**three named accessors** in §4.2 and **defers the by-`conversation_id` join** entirely.

Specifying the resolver now would mean guessing its deleted-row handling, batching and project
scoping against an imagined requirement — and a resolver built to an imagined caller is a resolver
the real caller has to fight. Its likely first real caller is **unified cross-surface search**; bring
it back when search exists to state what it needs.

**Q5 — CLOSED 22:54Z by me, before dispatch.** I asked whether step 1 of the two-step lookup could
run without a project, since `GetAgentBySlug` is project-scoped and DM conversations have no
`project_id` (DMs are global, §2.4.1).

It cannot arise. `webchat_topic.project_id` is **`TEXT NOT NULL`** (`webchannel_store.go:426`), and
phase 5's migration reads only `webchat_topic` rows. Every row it touches has a project to scope to.

**Residual, out of scope but recorded so it is not rediscovered as a bug.** A *direct* conversation
**can** acquire a `default_agent_id` by other paths — `WithDefaultAgentID` (`derive_key.go:200`) and
`backfill.go:304`. Those set an already-resolved UUID and never run the slug lookup, so they are
safe today. But **anyone who later adds a slug-accepting path for DMs has no project to scope
step 1 with.** If that happens, the answer is to require a UUID for DMs, not to guess a project.

---

## 10. Implementation phases (commit-sized)

1. `GetTopicNameForDisplay` accessor + the three-question doc comment at all three accessors. Both
   backends, separate tests. No callers yet.
2. Phase 5 migration in **report-only** mode + AC-U-14 report. No writes.
3. Phase 5 write path + `ClearTopicDefaultAgent` move. AC-U-7, AC-U-8, AC-U-13.
4. ~~Phase 6 resolver~~ — **dropped, Q4 closed.** The three accessors ship in commit 1; the
   by-`conversation_id` join is deferred until search needs it.
5. Phase 7 backfill covering **all** topics including tombstoned. AC-57-8.
6. Phase 7 precondition check as a standalone, runnable command. AC-57-6. Must be commit-separate
   from 5 so the check can be run against a pre-backfill database and observed to **fail**.
7. Phase 7 constraint migration + the sub-case (i) assertion. AC-57-9, AC-57-10.

---

## 11. Acceptance criteria

- **AC-U-13** *(REVISED 23:05Z — the original wording "resolution is the runtime's two-step lookup"
  is withdrawn, see §3.2.1)* Migration resolution is slug-then-UUID, **both steps project-scoped and
  both hiding soft-deleted agents**. Test a **slug**-valued `default_agent`, a **UUID**-valued one,
  and a genuinely unresolvable one. Paired positives — a test that only asserts the NULL case cannot
  see over-NULLing, which is the regression that matters.
- **AC-U-15** *(new, §3.2.1)* A topic whose `default_agent` is the UUID of an agent in **another
  project**, and one whose `default_agent` is the UUID of a **soft-deleted** agent, both migrate to
  `default_agent_id = NULL` and both appear in the AC-U-14 report **flagged distinctly** from
  never-resolvable garbage. These two are the rows where the migration deliberately **diverges from
  runtime behaviour**; the report is what makes that divergence visible instead of silent. A test
  that lumps them in with unparseable strings satisfies the letter and destroys the point.
- **AC-U-14** The migration emits an operator report listing every topic whose `default_agent` did
  not resolve, with the raw value. Cheap, and the only way anyone learns these existed.
- **AC-57-1** A **soft-deleted** topic renders `[deleted]`, not its stored name, through every
  display path. Assert the outcome (what the caller renders), not the accessor's return.
- **AC-57-2** The mint guard still sees soft-deleted topics after phase 6 lands. Regression guard:
  phase 6 adds a hide-deleted accessor next to a see-deleted one, and the obvious wrong change is to
  "unify" them. **Mutation: point the mint guard at `GetTopicNameForDisplay`'s filter and confirm a
  DEF-27 sink test fails.**
- **AC-57-3** `Conversation.display_name` is **exactly `""`** after a topic is created in the
  topic-create tx. Assert the exact value, not "empty-ish".
- **AC-57-4** A topic **rename** does not touch the conversation row, and the name still renders
  correctly through the link afterward. This is the anti-DEF-7 mutation — it is what catches a
  mirrored column sneaking back in later. (nc-arch's ACs (i) and (ii).)
- **AC-57-5** `Conversation.parent_ref` is exactly `""` after native topic creation.
- **AC-57-6** Phase 7's precondition check counts **soft-deleted** topics. Test: a tombstoned topic
  with `conversation_id IS NULL` must make the check FAIL. A check that scopes to live rows passes
  and then the constraint blows up at apply time.
- **AC-57-8** *(check-scope = remediation-scope, §5.1)* The phase-3 backfill **writes** a
  `conversation_id` for a **soft-deleted** topic that lacks one. Pair it with AC-57-6 and run them in
  order: backfill, then precondition check, and the check must pass. Running only AC-57-6 proves the
  check is strict; running only AC-57-8 proves the backfill is broad; **only the pair proves the two
  scopes match**, which is the actual requirement.
- **AC-57-9** *(identity layer carries no visibility filter, §5.3)* Two parts, both required:
  1. After backfill, the conversation linked to a soft-deleted topic has **`deleted_at IS NULL`**.
     Assert the exact value. This is the tripwire for someone adding a mirror to make a listing
     behave.
  2. **Mutation:** set `deleted_at` on that conversation, then drive
     `UpsertConversationByExternalRef` with the same `(surface, external_ref)`. It must mint a
     **duplicate** row. That failure is the whole reason for the ruling — the test asserts the
     mechanism, not just the policy, so the policy cannot be reversed without confronting it.
  3. Comment at `conversation_store.go:391` stating that the `DeletedAtIsNil()` filter is why
     identity rows are never tombstoned by surface deletion.

  Any future user-facing conversation listing joins `webchat_topic` and hides tombstoned topics
  **there**. The visibility filter goes on the surface; it never becomes a `deleted_at` on the
  conversation row.
- **AC-57-10** *(§5.2)* The unresolved branch still exists after phase 7, with causes (ii) and (iii)
  covered by live tests — an induced query error and a malformed thread ref. Sub-case (i) is an
  assertion or hard error, **not** a deleted branch; a test drives it by re-inserting a topic with a
  NULL `conversation_id` behind the constraint and confirms it errors loudly rather than minting.
- **AC-57-7** U-TX-1 holds: a test proves no ambient-pool access inside the topic-create tx. **Must
  use a timeout** — at `MaxOpenConns=1` the failure mode is a hang, not an error, so a test without
  one hangs CI rather than failing it.

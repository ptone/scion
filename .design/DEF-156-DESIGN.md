# DEF-156 — two backfills, two spellings of the same thread

**Status:** design, awaiting one decision from ptone (§6, OQ-156-1).
**Base read:** `scion/tranche-g` @ `77ebea1f6`.
**Supersedes** the root cause recorded in `DEFECTS.md` [^150] and the
remediation framing in [^151]. See [^152] for the correction.

---

## 1. Problem & Goals

### 1.1 What is actually broken

A web chat thread's conversation row can be written by two different
components, and they do not agree on how to spell the thread's
`external_ref`.

| | writer | `external_ref` | sets `webchat_topic.conversation_id`? | stamps messages? |
|---|---|---|---|---|
| **Route 1** | `backfillTopicConversations` (`pkg/hub/webchannel_store.go:1462`, pg twin `:1094`), and `CreateTopic` (`:680` / pg `:316`) | `''` | **yes** | no |
| **Route 3** | `BackfillService.persistGroup` (`pkg/messaging/backfill.go:347`) | `'thread:<project>:<threadID>'` | no | **yes** |

Both converge on `UpsertConversationByExternalRef`, which matches on
`(surface, external_ref)`. `''` and `'thread:…'` never match, so the two
routes cannot see each other's rows.

The read path resolves a thread through the **topic link** — `webchat_topic`
→ `conversation_id` → messages by `conversation_id`
(`ResolveThreadConversationForRead`, `pkg/messaging/conversation.go:396`;
filter at `pkg/hub/handlers_chat_v2.go:1914`). That is Route 1's row.
Route 3 stamped the messages onto its own row. **The thread renders empty
while its messages sit intact on a conversation nothing reads.**

Route 2 (`ResolveOrCreateThreadConversation` / `ResolveOrCreateConversationByKey`,
the live write path) also uses the `thread:` spelling, but it carries the
DEF-100/DEF-156 topic-lookup intercept, so it resolves to Route 1's row
before it can mint. That intercept is why live traffic is correct and only
backfilled history is broken.

### 1.2 Why it is certain rather than occasional — the ordering

`runBootDataMigrations` (which runs Route 3) is called from **inside store
construction**, at `cmd/server_foreground.go:1218`, immediately after
`migrateStore`. `backfillTopicConversations` (Route 1) runs inside
`hub.NewWebChatStore`, constructed much later at `cmd/server_foreground.go:598`.

**The web chat store does not exist when the message backfill runs.** So on
any hub with unstamped history:

1. Route 3 runs first. Every unstamped message with a `thread_id` derives
   `thread:<project>:<threadID>` (`DeriveConversationKey`, `derive_key.go:100`
   — `ThreadID` is checked ahead of the principal pair) and is stamped onto a
   freshly minted shadow.
2. Route 1 runs later, finds every topic with `conversation_id IS NULL`, and
   mints a **second**, empty conversation per topic, linking the topic to it.
3. The UI reads Route 1's row. Zero messages.

This also means Route 3 **structurally cannot** consult `webchat_topic`:
`NewBackfillService(convStore, msgStore, agents)` (`backfill.go:128`) takes no
topic lookup, and both call sites pass `messaging.NewBackfillService(s, s, s)`
(`cmd/boot_data_migrations.go:473`, `cmd/server_backfill.go:154`) at a point in
startup where no such lookup exists to pass.

### 1.3 Blast radius

**Fresh cutover: every native web topic thread with pre-stamping history
renders empty.** This is the prediction that matters, and it is stated before
the test rather than after it.

gteam lost only 3 threads (`020ee410`/`f1b450c9` 24 hidden 3 visible;
`53e35bb9`/`76a03689` 14 hidden **0** visible; `6165c1d6`/`e634fc46` 15 hidden
**0** visible — 50 of 53 messages) because most of its web traffic had already
been stamped by the live write path after the 29 Aug guard landed, and `Run`
skips messages that already carry a `conversation_id` (`backfill.go:174-178`).
**That is an accident of gteam's history, not a property of the code.** A
fresh instance has no prior stamping and gets the full effect.

### 1.4 Second symptom, same function

`persistGroup` hardcodes `Surface: "native"` (`backfill.go:353`) with no
reference to the message's channel. `DeriveConversationKey` emits a `thread:`
key for *any* message carrying a `thread_id`, Discord snowflake included. So
gteam's `0c57b491` and `be98c5c2` — `kind=group, surface=native,
external_ref='thread:<proj>:<discord-snowflake>'` — were written that way by
the backfill. They are not a live-path defaulting accident. `0c57b491` is
ptone's preserved Discord reproduction, which makes this a candidate root
cause for the Discord envelope behaviour rather than a coincidence.

### 1.5 Goals

- **G1.** A fresh cutover leaves every web thread's history readable through
  the UI on first read, with no restart and no second pass.
- **G2.** The correctness of that outcome does not depend on the order in
  which the two backfills run. An ordering constraint is a constraint a future
  refactor can silently violate; convergence on a key cannot be.
- **G3.** A backfilled conversation's `surface` reflects the channel the
  messages actually came from.
- **G4.** Progress toward removing switches and special-case lookups, not
  toward adding them. (ptone, standing: consolidate, do not proliferate.)

---

## 2. Non-Goals

- **Repairing gteam's three split threads.** Merging a shadow into its topic's
  conversation is a write, it is not obviously reversible, and it is ptone's
  decision. Designed separately in §5.3 and gated on his answer.
- **Purging the two Discord-keyed native rows.** Same reason, plus `0c57b491`
  is a protected reproduction fixture and must not be touched.
- **Changing the live write path.** Route 2 is correct today.
- **Changing the read path's topic-lookup intercept.** §3.4 notes it becomes
  redundant; retiring it is switch-collapse work, not this fix.
- **DEF-96.** Promotion is a different defect on a different path, already
  designed and dispatched.

---

## 3. Proposed Design — unify the spelling

**One sentence:** make Route 1 write the same `external_ref` Route 3 derives,
and make Route 1 idempotent on that key, so whichever runs first creates the
row and the other finds it.

### 3.1 C1 — Route 1 writes `thread:<project>:<topicID>`

`backfillTopicConversations` and `CreateTopic`, in **both** stores, stop
writing `external_ref = ''` for the topic's conversation and write
`thread:<projectID>:<topicID>` instead.

This is exactly the string Route 3 derives for a message on that topic:
`DeriveConversationKey` returns `fmt.Sprintf("thread:%s:%s", in.ProjectID,
in.ThreadID)` and `msg.ThreadID` for a web chat message **is** the topic id.
The project ids also agree — Route 3's `cfg.ProjectID` is the project whose
messages it is listing, and Route 1 reads `webchat_topic.project_id`.

**The derivation must be shared, not duplicated.** Route 1 must call the same
`DeriveConversationKey` (or a thin exported helper over it) that Route 3 uses.
Two independent `fmt.Sprintf`s of the same format string is precisely the
failure this defect is made of. This is the one part of the design that is not
negotiable on style grounds.

### 3.2 C2 — Route 1 becomes an upsert on that key

Today `backfillTopicConversations` does a raw `INSERT INTO conversations` with
a fresh UUID. Under C1 that would collide with a row Route 3 already created:
the partial unique index is on `(surface, external_ref) WHERE external_ref <>
'' AND deleted_at IS NULL` (`pkg/ent/schema/conversation.go`), and the `''`
rows sit **outside** it today — which is why duplicates have been possible at
all.

Route 1 must therefore: look up `(native, thread:<proj>:<topicID>)` first, and
if a row exists, link the topic to **that** row rather than minting. Only
mint when absent.

**This is what buys G2.** After C1+C2:

- Route 3 first → mints, stamps messages; Route 1 finds it, links the topic to
  it; UI reads it; messages present.
- Route 1 first → mints, links the topic; Route 3 upserts onto the same key,
  finds it, stamps messages onto it; UI reads it; messages present.

Order stops being load-bearing. The startup sequence at
`cmd/server_foreground.go:1218` / `:598` is left alone.

Note the same lookup must run inside Route 1's per-topic transaction, and
INVARIANT U-TX-1 applies: anything touching the ambient pool must happen
**before** `BeginTx`. At `MaxOpenConns=1` a violation hangs rather than fails.

#### 3.2a The collision surface C1 creates — check-then-insert

The lookup sits before `BeginTx` and the insert sits inside it, so the two are
not atomic. A writer arriving between them loses the race and hits the partial
unique index.

**The index prevents a duplicate; it does not produce convergence.** It makes
the loser *fail*. On Postgres the error poisons the transaction. That is worth
stating plainly because convergence is the entire purpose of C2 — "works
unless the two writers overlap" is order-dependence with a smaller window, and
G2 asks for order-independence.

This must be assessed per call site, and the two answers differ:

- **`backfillTopicConversations` — no change needed.** Boot is single-threaded,
  and the migration marker is written only on a completed pass (INVARIANT
  M-1′), so a partial failure re-runs on the next boot and the lookup then
  finds whatever the first pass wrote. Self-healing.

- **`CreateTopic` — a live request path, and the boot argument does not
  transfer.** Note what C1 does to it: *before* this change `CreateTopic` wrote
  `external_ref = ''`, structurally incapable of colliding with Route 2's
  `thread:<project>:<id>` key, because the two occupied disjoint namespaces.
  C1 puts them in one namespace. **This design creates the collision surface it
  now has to handle**, and that is a consequence of the change rather than a
  pre-existing hazard it inherits.

Whether the window is *reachable* on `CreateTopic` turns on where `topic.ID`
comes from. The store takes it from the caller
(`webchannel_store.go:680`, `CreateTopic(ctx, topic WebChatTopic)`). If every
call site mints a fresh UUID immediately before calling, no other writer can
name that key yet and the window is closed by construction — acceptable, but it
must be established **from the call sites**, not asserted from the store.
`EnsureGeneralTopic` is the first to check, since "the general topic for this
project" is a name two concurrent requests can both resolve to.

If the window is reachable, the fix is to preserve convergence at the point
where it has to hold: **on a unique-constraint error from the conversations
INSERT, re-read the row inside the transaction and link the topic to it**
rather than returning the error. A bare `ON CONFLICT DO NOTHING` is not
sufficient — it returns no rows, and the winner's id is exactly what is needed.
Any `ON CONFLICT` clause used here must match the partial index's conflict
target *including its predicate*, or it will not fire at all.

### 3.3 C3 — derive `surface` from the channel

`persistGroup` must set `Surface` from the message's channel rather than the
literal `"native"`. The `conversations.surface` enum is
`native|discord|slack|telegram|gchat|teams`; a message whose channel is
outside that set must **not** be silently coerced to `native` — it should
refuse and be counted as a derive failure, in the same bucket machinery
`Run` already has for DEF-114. Under-classifying is recoverable; a Discord
thread permanently recorded as native is the bug we are fixing.

This also keeps C1/C2 honest: convergence is on `(surface, external_ref)`, so
if Route 3 mislabels the surface it lands on a different key and the whole
unification is defeated for exactly the rows that most need it.

### 3.4 What this makes redundant — and what we do about it now

Once Route 1 writes a real `external_ref`:

- `ResolveThreadConversationForRead`'s fall-through
  `GetConversationByExternalRef(ctx, "native", extRef)`
  (`conversation.go:458`) starts matching native topics, so the DEF-100
  topic-lookup intercept above it stops being the only resolution route. Its
  comment at `:425` and `:439` — *"native topics write external_ref='' so the
  external_ref lookup below never matches them"* — becomes false.
- The DEF-156 write-path intercept in `ResolveOrCreateConversationByKey`
  becomes redundant for the same reason.

**Do not remove either in this change.** They stay, they stay correct, and
they stay as belt-and-braces while the population is mixed (§5.2). Their
retirement belongs to the switch collapse, where it is a net *deletion* of
special-case code — which is the shape ptone has been asking for. **Update
the two stale comments in this change**, because a comment asserting a
falsehood about the data model is how the next person reproduces this defect.

### 3.5 Explicitly not `parent_ref`

Same ruling as DEF-96 §3.3. `parent_ref` is caller-supplied from request JSON
on two live paths and is not trustworthy as a join key. Settled.

---

## 4. Alternatives Considered

### A — Fix the ordering, and give the backfill a topic lookup (rejected)

Move `runBootDataMigrations` to run after the web chat store is constructed,
and pass the store to `NewBackfillService` as a `TopicConversationLookup` so
`persistGroup` obeys the same rule Route 2 already does.

**This was my first recommendation to ptone and I withdrew it on analysis.**
Reasons:

1. It is a **startup-sequence change**. Boot data migrations currently run
   inside store construction, before `Ping`; moving them past web chat store
   construction reorders them relative to everything in between. That is a
   larger and less legible blast radius than a key format.
2. It leaves correctness **dependent on order**, violating G2. A future
   refactor that moves either backfill re-breaks this silently, and no test
   that exercises either route alone can fail — which is the exact property
   that let this defect exist for two weeks.
3. It **adds** a lookup dependency and entrenches the intercepts, violating
   G4. C1/C2 removes the need for them.
4. It does not fix §1.4 on its own; the surface fix is needed either way.

It is not wrong, and it is smaller in line count. It is worse in shape.

### B — Make Route 3 refuse to mint for `thread:` keys (rejected)

Have `persistGroup` skip any `thread:`-prefixed group key, leaving those
messages unstamped for a later topic-aware pass.

Fail-closed and genuinely safe: no shadow rows, nothing to merge later. But
the threads still render empty until a second pass exists, and **that second
pass is the design we are avoiding writing.** It converts a data-corruption
defect into a data-incompleteness defect without reaching G1. Worth keeping in
mind as a hotfix if C1/C2 turns out to be bigger than estimated.

### C — Leave the code, write a repair migration (rejected)

Merge existing shadows into their topic conversations and stop there. Rejected
outright: it fixes the data on hubs that have already run the backfill and
leaves every future fresh cutover broken. It is the shape of the investigator's
original "no code fix needed" conclusion, and it is the one option that
guarantees we hit this again during the cutover test.

### D — Unify in the other direction: Route 3 writes `''` (rejected)

Make `persistGroup` write `external_ref = ''` for `thread:` keys so both
routes agree on the empty spelling.

Rejected because `''` is outside the partial unique index, so there is no
uniqueness guarantee at all and nothing stops duplicates — the property that
made this defect possible. Unifying onto the *unindexed* spelling would make
the convergence unenforceable by the database.

---

## 5. Migration / Rollout

### 5.1 No switch

Per ptone's binding directive: a single cut-over upgrade, no third switch.
When a hub runs a version containing this change, the new behaviour is the
behaviour. Nothing here is gated.

### 5.2 The mixed population, and why it is safe

After the change, `conversations` will contain both spellings:

- **P1 — pre-fix topic rows, `external_ref = ''`.** Their topic link is
  populated, so the read and write paths resolve them via the topic-lookup
  intercepts, exactly as today. Their messages are already stamped. Unchanged
  and correct.
- **P2 — post-fix topic rows, `external_ref = 'thread:…'`.** Resolve via
  either route.

This is why §3.4 keeps the intercepts. **The mixed population is the reason
the intercepts cannot be deleted in the same change that makes them
redundant.**

**Residual risk, stated rather than hidden.** A P1 topic on a hub whose
message backfill has *not* completed — an errored pass, a resumed checkpoint —
can still be shadowed, because Route 1's migration marker
(`topic_conversation_backfill`) is already `done` and will not revisit it, and
Route 3 has no lookup. This is bounded to hubs already in that state and does
not affect fresh cutovers. It is closed by OQ-156-1 if ptone approves the
`external_ref` normalisation, and left as tracked drift if he does not — which
is within his standing tolerance for interim drift that does not dead-end the
end state.

### 5.3 Data repair — ptone's decision, not designed here

Two distinct populations on gteam, and they should not be swept together:

1. **Three split threads.** Repair = re-stamp the shadow's messages onto the
   topic's conversation and delete the shadow. Reversible only if the mapping
   is recorded first.
2. **Two Discord-keyed native rows**, one of which is a protected fixture.
   **Do not touch.**

Neither is in scope for the code fix. Both are measurable read-only; the
counts are already taken.

### 5.4 Measurement to take before implementing

Read-only, on gteam, to bound the repair and to check C1's assumption:

- **Q1.** For each of the 41 topics with an `''`-ref conversation: does
  `thread:<project_id>:<topic_id>` already exist as some other conversation's
  `external_ref`? Non-zero means C2's lookup will find pre-existing rows on
  upgrade — expected for the 3 known shadows, a finding if it is more.
- **Q2.** Any conversation with a `thread:`-prefixed `external_ref` and
  `surface <> 'native'`. Establishes whether C3 has a pre-existing population
  or only prevents future ones.
- **Q3.** Count of messages with `thread_id <> ''` and `conversation_id = ''`.
  The fresh-cutover blast radius, measured on real data.

---

## 6. Open Questions

**OQ-156-1 — normalise the existing `''` topic refs? (ptone)**
A one-time migration rewriting P1's `external_ref` from `''` to
`thread:<project>:<topicID>` would collapse the mixed population, close §5.2's
residual, and let the intercepts be deleted outright at switch collapse
instead of merely deprecated. It is ~41 rows on gteam. It is a write to a
column that is access-control-relevant for `direct` conversations (DEF-29) —
though **not** for `group`, which is the only kind affected here.
**My read: yes, but not in this change.** It is irreversible, it is not needed
for G1, and bundling it hides a migration inside a bug fix. Recommend it as
the first commit of the switch collapse. *Answer needed before switch-collapse
implementation, not before this one.*

**OQ-156-2 — what is a message's channel, for C3?** `persistGroup` needs a
surface per group, but a group is assembled from many messages. If a group's
messages disagree on channel, that is itself a finding. Proposal: derive per
message during grouping, refuse the group if they disagree, count it as a
derive failure. *I will settle this with a measurement rather than a ruling.*

**OQ-156-3 — does `CreateTopic` need C1 at all?** New topics created after the
fix are never backfilled, so their spelling never has to converge with
anything. Including them is consistency, not necessity. *My read: include it —
a column with two conventions depending on creation date is how the next
person gets this wrong. Cheap, and it is the only way §3.4's comment fix stays
true.*

---

## 7. Implementation Phases

Commit-sized, in order. **P1 before P2 is load-bearing** — P2 without P1
creates unique-index collisions on upgrade.

- **P1 — shared derivation.** Export/reuse the thread-key derivation so
  `pkg/hub`'s stores can call the same function `pkg/messaging` uses. No
  behaviour change. Test: the two call sites produce byte-identical strings for
  the same inputs, asserted against a table of vectors, not against each other.
- **P2 — Route 1 upserts on the derived key.** `backfillTopicConversations`
  and `CreateTopic`, both stores. Look up before minting; U-TX-1 discipline.
- **P3 — C3, surface derivation** in `persistGroup`, with refusal-and-count for
  unmappable channels.
- **P4 — comment truth pass.** `conversation.go:425`, `:439`, and the
  `derive_key.go` intercept comment. Delete the claim that native topics write
  `''`; state the mixed population explicitly.
- **P5 — repair migration.** **Not authorised.** Do not write it.

---

## 8. Acceptance Criteria

- **AC-156-1 — the whole point.** Simulate a fresh cutover: a database with
  web topics whose messages carry `thread_id` and **no** `conversation_id`.
  Boot. Read each thread through the endpoint the UI calls. **Every message is
  returned, on the first read, with no restart.** This must be an integration
  test that boots the real migration sequence, not a store-level test — a
  store-level test would have passed before this fix.
- **AC-156-2 — order independence (G2).** The same assertion holds when the
  two backfills run in either order. Drive both orders explicitly. This is the
  criterion that distinguishes the chosen design from Alternative A, so it is
  not optional and it may not be satisfied by asserting the current order.
- **AC-156-3 — exactly one conversation per topic** after boot. Not "at least
  one". Count them.
- **AC-156-4 — idempotence.** Boot twice. No new conversations, no re-stamping,
  counts identical.
- **AC-156-5 — surface fidelity.** A Discord-channel message with a `thread_id`
  does not produce a `surface='native'` conversation. Assert on the row, not on
  a log line.
- **AC-156-6 — no regression for P1.** A pre-existing `''`-ref topic with a
  populated topic link still resolves and still returns its messages. This is
  the mixed population; if it breaks, upgrades break.
- **AC-156-7 — mutation, both directions.** Revert C1 alone → AC-156-1 fails.
  Restore. Revert C2 alone → AC-156-2 or AC-156-3 fails. Restore → all pass.
  Paste all four raw. A mutation that fails to compile is not a caught
  violation.
- **AC-156-8 — nothing on the live write path changed.** Route 2's behaviour
  is identical; its existing tests pass unmodified. If a Route 2 test needed
  editing, that is a finding to report, not an edit to make.
- **AC-156-9 — the collision surface C1 creates is accounted for (§3.2a).**
  Every `CreateTopic` call site is enumerated in the report with where its
  `topic.ID` comes from. Either the report establishes that each mints a fresh
  UUID immediately before the call — closing the window by construction — or
  the unique-violation path re-reads and links rather than returning an error,
  with a test that drives it. **"The index catches it" is not an answer:** the
  index prevents the duplicate and fails the loser, which is the opposite of
  the convergence C2 exists to provide.

---

## 9. The lesson this defect is an instance of

A guard placed at one sink says nothing about a second writer that reaches the
store directly. The DEF-156 intercept's comment claims it *"prevents all paths
from minting shadow conversations"* — that is a claim about the author's
search, not about the call graph, and `persistGroup` was never in it.

And the sharper one: **when two components agree on a key's meaning but
disagree on its spelling, neither component is wrong on its own, and no test
that exercises either one alone can fail.** The only test that can catch it is
one that runs both and compares the result — which is why AC-156-1 is an
integration test and AC-156-2 exists at all.

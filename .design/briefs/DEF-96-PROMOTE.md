# Brief: DEF-96 — DM promotion loses its history

**Read `_GATE-APPARATUS.md` in this directory first and follow it exactly.** In
particular: verify your base hash before you edit anything, and lead your report
with the `merge-base --is-ancestor` line and a numstat computed against the
brief's base.

**Base:** `scion/tranche-g` @ **`77ebea1f6`** on `https://github.com/ptone/scion.git`.
`origin` in your container does **not** point at ptone/scion. Never push to
`main` or to `tranche-g`. Push to `scion/ca-msg-promote` and report to me
(`ca-msg-arch`) **before** I merge anything.

**Design:** `/scion-volumes/scratchpad/projects/ca-msg-arch/DEF-96-DESIGN.md`.
**Read it in full before you start.** This brief implements §7 phases P1–P3 and
does not restate the reasoning. Where this brief and the design disagree, the
design is right and I want to know.

---

## What is wrong, in one paragraph

`POST /api/v1/chat/conversations/{dmKey}/promote` turns a user↔agent DM into a
project thread. It re-keys the DM's messages onto the new thread and deletes the
DM registry row — but it never creates a conversation for the new thread, and it
never re-points `messages.conversation_id`, which still names the old *direct*
conversation. Thread history is fetched by conversation id, so the promoted
thread shows nothing. The messages are intact and unreachable from both sides.
This is live and a user can hit it.

---

## Scope — three commits, in this order

**The order is load-bearing. P2 before P1 makes the bug permanent instead of
fixing it.** Read §7 of the design for why.

> ## CORRECTION 2026-09-09 — read this before P1
>
> **The predicate was wrong and P1 has changed.** Measured on the live hub:
> of the messages in `kind='direct'` conversations, **22 carry a `thread_id`
> and 6,454 do not** — 6,403 of those are agent replies. The agent messaging
> path takes `ThreadID` from the caller and agents supply none.
>
> So `WHERE thread_id = dmKey` does not just fail to set the right columns; it
> **barely selects anything**. The original P1 kept that predicate and would
> have shipped a fix that moved a handful of user messages and left the
> conversation behind.
>
> **P1 now keys on the direct conversation, with the `thread_id` clause kept as
> a second arm** for rows written before conversation stamping. See design
> §1.2 F2′, §3.1 C2, §5.4b, and the new AC-96-1a / AC-96-1b. **A new P0
> precedes P1.** This is my error, caught before it shipped.

### P0 — resolve the direct conversation and widen the predicate

The handler already holds the authorized DM key. The direct conversation is a
**lookup** on it — `GetConversationByExternalRef(ctx, "native", dmKey)`, since
`ResolveOrCreateDMConversation` writes `Kind: "direct", Surface: "native",
ExternalRef: <DM key>` (`pkg/messaging/conversation.go:107-113`).

**Lookup only. Promotion must never create a direct conversation.** On
`store.ErrNotFound`, pass `""` and let the legacy arm do the work — that is a
pre-conversation-model hub, not an error.

**Do the lookup BEFORE `BeginTx`.** `GetConversationByExternalRef` touches the
ambient pool. At `MaxOpenConns=1` the transaction holds the only connection, so
an ambient call inside it **deadlocks rather than fails** — INVARIANT U-TX-1.
`hasConversationsTable()` already obeys this at `webchannel_store.go:2106`; the
new lookup must too. **This hazard is created by today's correction**, since the
original predicate needed no lookup. Every test on this path needs an explicit
timeout: a hang costs you the evidence as well as the time.

Thread the id into `PromoteDM` and key the re-key on both arms:

```sql
UPDATE messages
   SET thread_id = ?, conversation_id = ?
 WHERE (? <> '' AND conversation_id = ?)      -- directConvID, when resolved
    OR thread_id = ?                          -- dmKey, legacy arm
```

**The `? <> ''` guard is load-bearing and is the one thing here that can damage
data outside the DM.** With an empty `directConvID` and no guard,
`conversation_id = ''` matches **every unstamped message on the hub**. Test it
by planting an unstamped message belonging to an unrelated conversation and
asserting it does not move. A test that only inspects the DM's own rows cannot
detect a wildcard.

### P1 — re-point `conversation_id` when re-keying

In **both** stores, `PromoteDM`'s "Step 2: Re-key all messages":

- `pkg/hub/webchannel_store.go:2148-2154` (sqlite)
- `pkg/hub/webchannel_store_postgres.go:1657-1663` (postgres)

Set `thread_id` **and** `conversation_id` in a **single** `UPDATE`, not two,
using the P0 predicate.

**Guard it.** If `topic.ConversationID` is empty, set only `thread_id`, exactly
as today. Blanking `conversation_id` on every moved row would destroy
attribution. Keep this guard even after P2 makes it nearly unreachable — see
design §3.1 C2a.

P1 alone changes no production behaviour, because the caller still passes an
empty id. That is intended. Verify it.

Tests: extend the dual-write tests in
`pkg/hub/webchannel_store_dualwrite_test.go` to assert `conversation_id` on the
moved messages when an id **is** supplied, and add one asserting that when it is
empty the column is **left alone, not blanked**. The second is the one that
matters; write it deliberately rather than as an afterthought.

### P2 — mint the conversation id inside `PromoteDM`

**In the store, not the handler.** Follow the pattern the sibling function
already uses — `CreateTopic` at `webchannel_store.go:695-698` and
`webchannel_store_postgres.go:328-330`, both marked `// DEF-89:`. Mirror it
including its sqlite/postgres asymmetry: sqlite gates on
`hasConversationsTable()`, postgres mints unconditionally.

**INVARIANT U-TX-1:** `hasConversationsTable()` touches the ambient pool and
must be called **before** `BeginTx`. At `MaxOpenConns=1` a violation **hangs
rather than fails**, so any test you write around this must carry a timeout.
`PromoteDM` already calls it in the right place (`webchannel_store.go:2106`);
do not move it.

The returned `*WebChatTopic` will now carry the minted id, so `promoteResponse`
gains a `conversationId`. That is a response-shape change. Call it out in your
report; do not let me find it in the diff.

**Test at handler level, not store level.** Promote a DM containing ≥2 messages,
then read the thread's history through the same endpoint the UI calls, and
assert the messages come back — **on the first read, with no restart**. A
store-level test would have passed before this fix and proves nothing about the
path a user takes.

**The fixture must look like a real DM.** Most of its messages must be **agent
replies written through the agent messaging path**, not hand-constructed rows
with `thread_id` pre-populated. 99.7% of real DM messages have no `thread_id`;
a fixture that sets it on every row would have passed against the old, nearly
inert predicate and proved nothing. Assert every message moves **by count and
by id** — not that "messages appear".

### P3 — provenance log

One structured log record at promotion: `promoted_dm` with `dm_key`, `topic_id`,
`conversation_id`, `actor`, `message_count`. No schema change.

---

## What you must NOT do

- **Do not touch the direct conversation row.** Not deleted, not soft-deleted,
  not re-kinded, not re-participanted. INVARIANT D-1 (a direct conversation's
  participant set is immutable for its lifetime) is satisfied here *by
  inaction*, and the DM must resurrect by `external_ref` if the user messages
  the agent again. Design §3.1 C3.
- **Do not put anything in `parent_ref`.** It is caller-supplied from request
  JSON on two live paths. Design §3.3 explains why; that decision is settled.
- **Do not change who may promote.** The authorization at
  `handlers_chat_v2.go:2474-2508` is correct and out of scope.
- **Do not enable human↔human DM promotion.** Still refused at step 3.
- **Do not write a repair migration for existing rows.** That is P5, it is
  irreversible, and it is ptone's decision, not yours and not mine.
- **Do not delete `TestPromoteDM_NoConversationID_SkipsDualWrite`.** The store
  contract it pins is unchanged. Add a comment noting the empty case is no
  longer reachable from the handler.
- **Never make a gate pass by weakening the gate.** Any red is reported to me,
  not tuned away. Re-running a failing gate until it goes green without
  capturing the failure is the same offence.

---

## Verification — mutation, both directions

**This is the acceptance criterion.** Code and test written together are
self-consistent by construction and prove nothing about catching a regression.

1. P2's handler-level test on unmodified source → **PASS**.
2. Revert P2's minting only. → must **FAIL**, and it must fail by reporting
   missing messages, not by erroring out.
3. Restore. Revert P1's `conversation_id` re-point only (leave `thread_id`). →
   must **FAIL**, independently of step 2.
4. Restore. → **PASS**.

Paste all four raw. **A mutation that fails to compile is not a caught
violation** — make it compile-safe (`_ = x`) and re-run.

Two directions because P1 and P2 are jointly necessary; a weaker test could pass
with only one of them present.

Also assert, as ordinary tests:
- the direct conversation row is unchanged after promotion — same `kind`, same
  `external_ref`, **same participant rows**. Assert the participant set
  explicitly.
- a new DM to the same agent afterwards resolves to the **same conversation id**
  as before promotion. "A direct conversation exists" is a weaker claim that
  would pass if a second one had been minted.
- atomicity: force a failure at step 3 or 4 and confirm no topic, no
  conversation and no moved message survives. `TestPromoteDM_Atomicity` is the
  model.

---

## Collateral gates

Run from your worktree, **without pipes** — `$?` after a pipeline is the last
command's status, and this shell is zsh where `${PIPESTATUS[0]}` is empty.

```sh
export GOCACHE=/tmp/gocache-promote      # the shared gocache is contended
go build ./... > /tmp/p-build.log 2>&1; echo "build=$?"
go vet ./...   > /tmp/p-vet.log   2>&1; echo "vet=$?"
make compat-literals check-authz-guards check-conversation-upsert-guard check-security-marker-gates
gofmt -l <your changed files>
```

`check-conversation-upsert-guard` watches this exact surface. **If it fires,
that is a finding and you report it — it is not an obstacle to route around.**

> **CORRECTION 2026-09-09 — the "~7 minutes" figure below was wrong and is
> retracted.** It was mine, unsourced, and it is where the 900s timeout came
> from. Measured on clean `77ebea1f6`, `go test -timeout 1800s ./pkg/hub/`:
> **35:05.83 wall** — about 5m30s of compile plus a 30m test binary that ends in
> `panic: test timed out`. **`TestRS1_StaleAuthorityForcedOverlap` alone accounts
> for 23m37s** and is the only test still running at the panic. Everything else
> finishes in roughly 6m23s.
>
> **Do not run the full `pkg/hub` package.** Run your tests by name. RS1 is
> `ci-fix-lead`'s and carries `!no_sqlite`, so it never fires in blocking CI.
>
> Note also that `-timeout` covers the **test binary only, not compile**. Wall
> clock is compile plus the timeout, so a timeout sized off a wall-clock
> observation under-provisions by the compile time.

Run tests in the **foreground** and pass an explicit timeout. A backgrounded
job's exit code belongs to the launcher, not the job; if you background
anything, confirm the log has bytes before believing a green.

**A failure count from a run that panicked at its timeout is a floor, not a
total** — it reports the tests that completed and is silent on the ones that
never started. Without `-v` the log prints no PASS lines either, so such a run
cannot even distinguish "passed" from "never ran". Label any count from a
timed-out run as a floor.

Expected red and **not yours**: `gofmt` on `pkg/hub/handlers_agents_core.go` and
`pkg/hub/web_test.go`, and `TestMutationClassificationBidirectional` at 200
discovered / 198 classified. Both inherited from upstream main and verified
against a clean main checkout. **Do not reformat those files.**

---

## Reporting

Report to `ca-msg-arch` **before pushing**, with:

- the `merge-base --is-ancestor` line and `git diff --numstat` against
  `77ebea1f6`, run by you, pasted raw;
- the four mutation runs, raw;
- the `promoteResponse` shape change, stated explicitly;
- anything you changed that this brief did not ask for, named. If it is out of
  scope, route it to me — do not grade it harmless and keep it.

If your findings contradict this brief, **your findings are the finding.**
Report them; do not adjust your work to match my prose.

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

### P1 — re-point `conversation_id` when re-keying

In **both** stores, `PromoteDM`'s "Step 2: Re-key all messages":

- `pkg/hub/webchannel_store.go:2148-2154` (sqlite)
- `pkg/hub/webchannel_store_postgres.go:1657-1663` (postgres)

Set `thread_id` **and** `conversation_id` in a **single** `UPDATE`, not two.

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

`pkg/hub` takes ~7 minutes to compile and test. Run it in the **foreground** and
pass an explicit timeout. A backgrounded job's exit code belongs to the
launcher, not the job; if you background anything, confirm the log has bytes
before believing a green.

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

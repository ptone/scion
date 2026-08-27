# DEF-27 — a soft-deleted native topic is minted a shadow conversation

**Release blocker for §2.6.4. Found by `nc-arch` 21:59Z, verified by me on both backends 22:00Z.**
DEF-20 class: the mint guard has an entrance it does not know about.

---

## 1. The defect

The sink guard in `ResolveOrCreateConversationByKey` asks the webchat store "is this thread a native
topic?" via `GetTopicConversationID`. **That lookup hides soft-deleted topics**, so a tombstoned
native topic answers `ErrNotFound` — which the guard reads as "not native, safe to mint."

Verified in both implementations:

    pkg/hub/webchannel_store.go:1364
      SELECT COALESCE(conversation_id, '') FROM webchat_topic WHERE id = ?  AND deleted_at IS NULL
    pkg/hub/webchannel_store_postgres.go:978
      SELECT COALESCE(conversation_id, '') FROM webchat_topic WHERE id = $1 AND deleted_at IS NULL

`DeleteTopic` is a **soft** delete (`webchannel_store.go:814`). So after a delete the topic row, its
messages, and its `conversation_id` link all still exist — but the guard can no longer see any of it.

**Note the shape: the same wrong predicate in both backends.** This is a correlated blind spot
(rule 26). Running the suite against SQLite *and* Postgres would not have caught it, because the two
implementations were written from one template and inherited one mistake. Backend parity testing
proves agreement, not correctness.

## 2. Reachability — the creation window is safe, the deletion window is not

**Creation is safe** (nc-arch, and I agree): the dual-write populates `conversation_id` in the same
transaction as the topic, and the client only learns the topic UUID after that commit, so no client
can reference a topic that predates its row. The human send path additionally pre-validates with
`GetTopic` + reject-if-nil at `handlers_chat_v2.go:752-753`.

**Deletion is reachable.** The paths that never pre-validate topic existence are exactly the ones
that can carry a stale reference:

| Path | Pre-validates topic? |
|---|---|
| `handlers_chat_v2.go:752` (human send) | **yes** — `GetTopic`, rejects nil |
| `handlers_agent_messaging.go:118, :561` | **no** — validates DM-key *format* only |
| `handlers_broker_inbound.go:97` | **no** |

Format validation is not existence validation, and neither is authorization (DEF-14).

**Canonical trigger: a human deletes a thread mid-agent-turn.** The agent's reply is already in
flight, hits `ErrNotFound`, and mints a shadow conversation for a topic that already had one — now
orphaned, because its topic is tombstoned. A retried or late broker delivery does the same.

## 3. Root cause — one function answering two questions

`GetTopicConversationID` is being asked two different questions whose `deleted_at` requirements are
**opposite**:

1. *"What conversation does this live topic link to?"* — user-facing. Must hide deleted.
2. *"Is this thread ID one of ours?"* — the mint guard. Must see deleted.

Fixing the predicate alone leaves one function serving both, and it will drift back the first time
someone notices it returning tombstoned rows to a user-facing caller. **Split it, and name each
function after the question it answers** so the next reader cannot pick the wrong one by accident.

## 4. The invariant to state in the code

> **Soft-deletion is not declassification.** A tombstoned native topic is still a native topic for
> the purpose of "should I mint." Deletion hides a topic from users; it must not make the mint guard
> forget the topic was ours.

## 5. The fix

Add a lookup that **ignores `deleted_at`**, distinct from the user-facing accessor, and point the
sink guard at it. On finding a tombstoned row:

- it has a `conversation_id` → resolve to it, **or** return unresolved. Either is acceptable.
- **never mint.** That is the whole requirement.

Whether an agent reply to a deleted thread should be *accepted at all* is a native-chat policy
question and is **not** in scope here. The sink must be correct under either policy.

Both backends. This is a two-file change by construction; if you change one, you have made the
backends disagree, which is worse than the defect.

## 6. Non-goals

- Do not change `GetTopic` or any user-facing accessor. Hiding soft-deleted topics from users is
  correct behaviour and is not what broke.
- Do not make `DeleteTopic` a hard delete.
- Do not decide the agent-reply-to-dead-thread policy here.

## 7. Acceptance criteria

- **AC-27-1** *(the reproduction — must fail before the fix)*: create a native topic with a linked
  conversation, soft-delete it, then drive the sink with that thread ref. Before: a new conversation
  row appears. After: no new row.
- **AC-27-2** Paired positive (rule 29): a **live** native topic still resolves to its existing
  conversation, and a genuinely unknown thread ref still mints. A guard that refuses everything
  satisfies AC-27-1 and is useless.
- **AC-27-3** Both backends, and **the SQLite and Postgres tests must be separate tests**, not one
  test run twice through an abstraction. The defect's whole nature is that both implementations
  shared a mistake.
- **AC-27-4** Mutation, and it must be **the defect, not merely a break** (rule 48): restore
  `AND deleted_at IS NULL` on the guard's lookup only, and confirm AC-27-1 fails with an assertion
  naming the mint — not a panic, not a compile error.
- **AC-27-5** The user-facing accessor still hides soft-deleted topics. Regression guard for the
  obvious wrong fix, which is deleting the predicate in one place and calling it done.
- **AC-27-6** The §4 invariant appears as a comment at the guard's lookup, stating why the absent
  predicate is deliberate. Without it the next reader "fixes" the missing `deleted_at`.

## 8. Open question I could not settle

If a tombstoned topic's `conversation_id` is empty — soft-deleted before backfill reached it — the
guard returns unresolved and the message is stored unlinked. I believe that is right (degraded, not
dropped, and it fails toward under-linking). Confirm by test and say which you chose.

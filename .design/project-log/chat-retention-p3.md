# Chat message retention, Phase 3 (failed-message purge)

**Date:** 2026-10-31
**Branch:** scion/nc-retention-p3
**Tracking:** ptone/scion#6832

## What was built

Design §7.4 / §9 Phase 3: the hourly failed-message purge now uses the
Phase 1 retention cascade instead of one unbounded `DELETE`. It is not
gated by `hub.chat_retention` and keeps its 7-day default.

- **Setting** `messaging.failed_message_retention_days` (default 7, minimum
  1; a stored value below 1 reads as the default). It is in the messaging
  section schema, `OperationalSettings.FailedMessageRetentionDays()` and
  `GET/PUT /api/v1/admin/messaging` (null resets it). It replaces
  `Config.FailedMessageRetentionDays`, which nothing ever set. The handler
  reads it on every run, so a change takes effect without a restart.
- **Store** `ChatRetentionStore.SelectFailedMessages` uses the old filters
  (`dispatch_state = 'failed'`, `recipient LIKE 'agent:%'`,
  `created < cutoff`) with the Phase 1 `(created, id)` order and keyset, so
  the created index serves it. `PurgeFailedMessages` runs the same cascade
  transaction as `PurgeChatMessages` but writes no cursor, and counts the
  deleted messages under the new `ChatRetentionCounts.Failed` instead of
  `Main`.
- **Handler** `failedMessageRetentionHandler` keeps its schedule (60 min),
  its `LockFailedMessageRetention` lock and its 30 s deadline. It runs
  `chatRetentionSweeper.PurgeFailed`, which uses the chat retention sweep's
  pacing: `sweep.chunk_size` with adaptive shrink and grow, `pause_ms`, and
  a budget of `tick_budget_seconds` that also stops before the handler
  deadline. Each chunk deletes the dependent rows with its messages, which
  fixes the `message_addressees` orphans the old `DELETE` left.
- **Run counts.** When chat retention is active (experiment and `enabled`)
  and a sweep run started in the last 24 h, the counts are added to that
  run under `failed`. The update holds the in-process tick guard and
  `LockChatRetention`; if a tick holds either, the counts are only logged.
  The purge never opens a run.
- **Removed** the trailing `PurgeOrphanMentions` call and the method (its
  only caller), `MessageStore.PurgeOldMessages` (dead) and
  `MessageStore.PurgeFailedMessages`. There is no deprecation path (Alpha
  rule).
- **Docs:** the messaging key lists in `reference/api.md` and
  `reference/server-config.md`.
- **Web:** no change. The server-config page shows only the cross-project
  switch from the messaging endpoint, not the other messaging keys.

## Decisions

- The selection runs on the handler's context, not on the 5 s chunk
  timeout. With chat retention off, the created-index range below the
  cutoff can be most of the table, and one selection may pass many
  delivered rows to fill a chunk. The old `DELETE` had the full 30 s.
- The walk keeps its position in memory only. Every row it returns is
  deleted, so the next run starts at the oldest remaining row.
- Without the chat retention store (web chat tables not initialized), the
  handler logs a warning and deletes nothing. It used to fall back to the
  ent `DELETE`.

## Tests

- `pkg/store/entadapter` (SQLite by default, Postgres in the store
  Postgres job): `TestChatRetentionStore_SelectFailedMessages` and
  `TestChatRetentionStore_PurgeFailedMessages` cover the old row set
  (failed, agent recipient, strictly older), ties, the limit and the
  cursor. They also check the cascade, counts under `Failed`, that no
  cursor is written, and idempotence. These replace the old `MessageStore`
  purge tests.
- `pkg/hub` shard 5: unit tests of the paced walk, SQLite handler tests on
  the real web chat tables (same rows, no orphans, window change without
  restart, counts added to an open sweep run, no store), admin messaging
  validation, schema and fallback, and member/admin authz.
- `pkg/hub` `-tags integration` shard 3:
  `TestFailedMessageRetention_Postgres_SameRowsNoOrphans`.

## Known gaps and follow-ups

- No index serves `dispatch_state = 'failed'`. With chat retention off,
  each hourly run walks the created index from the oldest row to the
  cutoff, as the old `DELETE` did. A partial `(created, id) WHERE
  dispatch_state = 'failed'` index, like the pending one, would make the
  walk proportional to the failed rows. It needs an ent schema change.

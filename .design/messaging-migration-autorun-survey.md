# Migration Auto-Run Feasibility Survey

**Ref**: `85f25c1a1` (pinned; all evidence from this commit)
**Date**: 2026-09-02
**Purpose**: Survey the three migration-ish components to determine whether they can auto-run at hub startup.

---

## 1. What Each Component Does to the Data

### backfillTopicConversations

Creates a `conversations` row for each `webchat_topic` row that lacks a `conversation_id`. For each unlinked topic, it INSERTs into `conversations` (columns: `id`, `project_id`, `kind='group'`, `surface='native'`, `external_ref=''`, `parent_ref=''`, `display_name=<topic.name>`, `drift_state='active'`, `last_activity_at`, `created_at`) and UPDATEs `webchat_topic.conversation_id` to point at the new row. Both happen in a single transaction per topic. Does NOT populate `conversation_participants` — group conversations derive membership from the project, not the participant table (comment: "DEF-36").

**Evidence**: `pkg/hub/webchannel_store.go:1462-1555` (SQLite), `pkg/hub/webchannel_store_postgres.go:1064-1145` (Postgres).

### BackfillService

Scans all `messages` rows (per project) that have an empty `conversation_id`. For each message, it derives a conversation key via `DeriveConversationKey` (producing a `dm:...` or `thread:...` external_ref), groups messages by that key, then:
1. UPSERTs a `conversations` row via `UpsertConversationByExternalRef` (columns: `id`, `kind`, `surface='native'`, `external_ref`, `drift_state`, `project_id`, `default_agent_id`).
2. INSERTs `conversation_participants` rows for each derived participant (via `AddParticipant`).
3. UPDATEs each message's `conversation_id` via `SetMessageConversationID`.

It also tracks two hazard classes: Hazard-A (non-UUID sender/recipient — legacy email-based IDs) and Hazard-B (slug-based agent references needing resolution).

**Evidence**: `pkg/messaging/backfill.go:97-340`, `cmd/server_backfill.go:86-153`.

### DMMigrationService

Scans all `conversations` rows where `kind='direct'`. Classifies each into three categories and acts accordingly:

1. **Kind-encoded rows** (ParseDMKey succeeds on `external_ref`): ensures `conversation_participants` rows exist for both principals named in the key. Calls `AddParticipant` which is guarded by `CheckDMParticipantKey` at the store layer.

2. **Empty-ref rows** (`external_ref=''`): per B14 ruling, these are LEFT UNTOUCHED. The code explicitly skips them — it increments `EmptyRefSkipped` and returns. Comment: "Deriving a key from the participant index would fabricate an ACL from the listing index, inverting the direction of authority."

3. **Old-format rows** (`dm:<sorted_id1>:<sorted_id2>` without kind encoding): resolves each UUID's kind by looking it up in both `users` and `agents` tables, computes a new kind-encoded key via `DMConversationKey`, then either:
   - **Re-keys in place**: UPDATEs `conversations.external_ref` to the new key, clears `project_id` (DMs are global per DEF-10).
   - **Merges**: if a row with the new key already exists, re-stamps all messages from the old conversation to the target, copies participants (guarded by `CheckDMParticipantKey`), and soft-deletes the old row.

**Evidence**: `pkg/messaging/dm_migration.go:88-450`.

---

## 2. Idempotency

| Component | Safe to re-run? | Guard mechanism | Accident or design? |
|---|---|---|---|
| backfillTopicConversations | **Yes** | `migrationCompleted("topic_conversation_backfill")` marker in `webchat_migrations` table. Per-row guard: `WHERE conversation_id IS NULL` in the UPDATE. If 0 rows affected, TX rolls back. | **Design**. Marker is a performance gate; per-row WHERE is the correctness gate. |
| BackfillService | **Yes** | Per-message guard: `if msg.ConversationID != "" { skip }` at `backfill.go:128`. UpsertConversationByExternalRef uses query-then-create with partial unique index + retry on constraint violation. AddParticipant handles ErrAlreadyExists. | **Design**. The skip-if-already-stamped guard is explicit in the code. However, there is no once-only marker — every re-run re-scans all messages (skipping stamped ones). Cost is proportional to total messages, not unprocessed messages. |
| DMMigrationService | **Mostly yes, with a caveat** | No once-only marker at all. Re-runs re-scan all direct conversations. Step 1 (rebuild participants): AddParticipant returns ErrAlreadyExists — safe. Step 3a (empty-ref): no-op — safe. Step 3b (re-key): if already re-keyed, ParseDMKey succeeds → classified as kind-encoded (step 1) → safe. Merge case: old row is deleted after merge, so it won't be found again — safe. **BUT**: `resolveKind` (line ~355) checks both user and agent tables. If a UUID appears in both (theoretically possible — UUIDs are generated independently per table), the row is marked `Ambiguous` and skipped. This is a guard, not a hazard. | **Mostly design**. The absence of a once-only marker means every startup re-scans all direct conversations. For the merge path, the soft-delete of the source row prevents double-merge. |

---

## 3. Ordering and Dependencies

### Inter-component dependencies

| Dependency | Exists? | Evidence |
|---|---|---|
| BackfillService depends on DMMigrationService? | **No**. BackfillService creates conversations from messages using `DeriveConversationKey`. DMMigrationService fixes existing conversation rows. They operate on different inputs (messages vs. conversations). BackfillService's `UpsertConversationByExternalRef` would create a new row even if DMMigrationService hasn't re-keyed the old one — the old-format row and the new-format row would coexist (different `external_ref` values). DMMigrationService's merge step would later detect the duplicate and merge them. So they are order-independent but running DMMigrationService FIRST would reduce duplicate conversation rows. |
| DMMigrationService depends on BackfillService? | **No direct dependency**, but DMMigrationService's step 1 (rebuild participants) benefits from BackfillService having run first — BackfillService creates the conversation rows that step 1 then populates with participants. However, DMMigrationService also handles rows created by the live write path, not just backfilled ones. |
| Both depend on schema migration? | **Yes**. Both require the `conversations` table and `conversation_participants` table to exist. These are Ent-managed and created by `AutoMigrate`. BackfillService also requires `messages.conversation_id` column (also Ent-managed). |
| backfillTopicConversations depends on schema migration? | **Yes**. The SQLite version explicitly checks `hasConversationsTable()` before proceeding (`webchannel_store.go:1472`). The Postgres version does not check (Postgres always has the table because Ent AutoMigrate runs first). Both require the `webchat_topic.conversation_id` column, which is added by the preceding `addTopicConversationID()` migration step. |

### How the hub sequences schema migrations at startup

The startup path in `cmd/server_foreground.go` is:

1. **Legacy SQLite upgrade** (`maybeMigrateLegacySQLite`, line 1180) — upgrades raw-SQL hub.db to Ent schema. Safe to run on every boot.

2. **Ent schema migration** (`migrateStore` → `s.Migrate(ctx)`, line 1218):
   - On Postgres: wrapped in `pg_advisory_lock(0x5C100008)` (`LockSchemaMigration`). This is a session-scoped blocking lock — all replicas serialize here.
   - `s.Migrate(ctx)` calls `AutoMigrate` (Ent schema CREATE/ALTER), then runs a chain of data backfills: `BackfillEmptyAgentRoles`, `BackfillDelegationEdges`, `BackfillProjectMembersGroupMarkers`, `BackfillProjectAgentsGroupMarkers`, `MigrateAllowListToInvitedUsers`, `BackfillGCPVerificationStatus`, `SeedMaintenanceOperations`.
   - **All of these run under the schema advisory lock on Postgres.** This is the existing pattern for data migrations that must not race.
   - Evidence: `pkg/store/entadapter/composite.go:235-273`, `cmd/server_foreground.go:1228-1264`.

3. **Warning check** (`maybeWarnUnbackfilledMessages`, line 1221) — counts messages without `conversation_id`, logs a warning. Non-fatal. No migration work.

4. **WebChat store Init** (`webStore.Init()`, line 599) — creates `webchat_*` tables (DDL is IF NOT EXISTS), then runs `runMigrations()` which includes `backfillTopicConversations`. This is NOT under any advisory lock. Failure is a warning, not fatal.

5. **Other boot-time migrations** use `runWithAdvisoryLock` (line 1280): `LockBundledResources` (line 1874), `LockStorageMigration` (line 1900), `LockInlineSecretsMigration` (line 2689). These use `TryAdvisoryLock` — if the lock is held, the migration is silently skipped (the winning replica does the work).

### Existing versioning/once-only mechanism

**Two separate mechanisms exist:**

1. **`webchat_migrations` table** (TEXT PK `name`, `completed_at`). Created by webStore.Init() DDL. Used by `migrationCompleted(name)` / `markMigrationCompleted(name)`. Names in use: `thread_id_backfill`, `wave1_seed`, `topic_conversation_id`, `topic_conversation_backfill`. This is specific to the webchat store and uses a check-then-insert pattern (NOT atomic — two replicas can both pass the check before either inserts).

2. **Hub settings marker** pattern. Used by `BackfillEmptyAgentRoles` (`composite.go:280`: `GetHubSetting(ctx, emptyAgentRoleBackfillMarkerSection)` — if found, skip; if ErrNotFound, proceed). After completion, writes a hub_settings row as a marker. This runs under the schema advisory lock, so the non-atomic check is safe.

**Neither mechanism is shared across the two stores.** BackfillService and DMMigrationService use neither — they have no once-only marker at all.

### Available advisory lock keys

`pkg/store/concurrency.go` defines `AdvisoryLockKey` constants. There is no existing key for a "data migration" lock. A new key would need to be allocated (next available: `0x5C100014` or similar). The `runWithAdvisoryLock` helper at `server_foreground.go:1280` is the ready-made wrapper.

---

## 4. The Auto-Run Precedent (backfillTopicConversations)

### Invocation path

`cmd/server_foreground.go:599` → `webStore.Init()` → `pkg/hub/webchannel_store[_postgres].go` → `runMigrations()` → `backfillTopicConversations()`.

### Failure behavior

If `Init()` fails (including if `backfillTopicConversations` fails), the error is caught at `server_foreground.go:599-601`:
```go
if err := webStore.Init(); err != nil {
    log.Printf("Warning: failed to initialize webchat store: %v", err)
}
```
**Failure does NOT block startup.** The web channel spoke is simply not registered. This means the webchat feature is degraded but the hub still serves API requests, runs agents, etc.

### Once-only guard

`migrationCompleted("topic_conversation_backfill")` checks the `webchat_migrations` table before running. After successful completion, `markMigrationCompleted("topic_conversation_backfill")` inserts a row. Subsequent boots skip the migration entirely.

### Concurrent replica safety

**Safe by design**, not by the marker. The marker check is not atomic (two replicas can both pass `migrationCompleted()` returning false). But the per-topic transaction uses `UPDATE webchat_topic SET conversation_id = ? WHERE id = ? AND conversation_id IS NULL` — if a concurrent replica already set the `conversation_id`, the UPDATE affects 0 rows, the function returns nil WITHOUT committing, and the deferred `tx.Rollback()` discards the orphan conversation row. This is explicitly documented in the code comments.

The `markMigrationCompleted` INSERT has a TEXT PRIMARY KEY on `name`, so a duplicate insert would fail with a constraint violation. The code does NOT handle this error — it would propagate up and cause `Init()` to fail. On the second replica, this would trigger the "Warning: failed to initialize webchat store" log. **This is a minor bug**: the marker insert should use `INSERT OR IGNORE` / `ON CONFLICT DO NOTHING`. In practice, it's unlikely to hit because the migration runs fast enough that one replica usually finishes and marks before the other reaches the INSERT.

### Verdict: pattern worth copying?

The pattern is **sound for single-row-at-a-time work** where each unit is independently atomic (one TX per topic). It is **NOT directly applicable** to BackfillService or DMMigrationService because:
- BackfillService groups messages across rows, creates a conversation, then stamps multiple messages — this is NOT atomic.
- DMMigrationService's merge step spans multiple messages (re-stamp), participants (copy), and the source row (delete) — also NOT atomic (though it has an explicit abort-if-restamp-failed guard).

The advisory lock pattern (used by `migrateStore` and `runWithAdvisoryLock`) is the better model for these services: acquire a lock, run the full migration, release.

---

## 5. Failure and Blast Radius

### backfillTopicConversations

**Partial failure is safe.** Each topic is processed in its own transaction. If topic N fails, topics 1..N-1 are committed, topic N is rolled back, and the error propagates up (Init fails, web spoke not registered). On next boot, only the un-migrated topics remain (including topic N). The migration marker is only written after ALL topics succeed, so a partial run is automatically resumed.

**No wrong-state risk.** A topic either has a conversation_id (committed) or doesn't (rolled back). No intermediate state is visible.

### BackfillService

**Partial failure leaves half-migrated data.** Phase 3 (`persistGroup`, line 280) iterates over conversation groups. For each group, it:
1. Upserts the conversation (succeeds or fails).
2. Adds participants (errors are logged but non-fatal).
3. Stamps each message with `SetMessageConversationID` (errors are logged but non-fatal, the loop continues).

If the process crashes mid-group: the conversation row exists, some messages are stamped, others are not. **On re-run**: the stamped messages are skipped (`ConversationID != ""`), the unstamped messages are re-processed and assigned to the same conversation (UpsertByExternalRef returns the existing row). **This is safe** — the re-run completes what the first run started.

**The real risk is the scan cost, not data corruption.** Every re-run scans all messages (not just unprocessed ones), which is O(total_messages).

### DMMigrationService

**Partial failure of step 3b (re-key/merge) is the dangerous case.**

**Re-key in place**: `UpdateConversation` is a single row update. Partial failure means some old-format rows are re-keyed and some aren't. On re-run, the re-keyed rows are classified as kind-encoded (step 1) and processed for participants. The un-keyed rows are re-keyed. **Safe.**

**Merge**: The merge path (`mergeConversation`, line ~408) is the most complex:
1. Re-stamps all messages from old conversation to new (paginated, non-transactional).
2. If ANY re-stamp fails, the merge aborts — old row is left intact. Comment: "B2 ATOMICITY: Under-migrating is recoverable; deleting the source row while messages still reference it is data loss." **This is an explicit safety guard.**
3. Copies participants from old to new, guarded by `CheckDMParticipantKey` — only participants named in the target DM key are copied. Comment: "B1 D-1 GUARD ROUTING: prevents a stranger in the old row's participant table from being injected into the target DM."
4. Soft-deletes the old conversation row.

**Can a partial or repeated run produce a WRONG key?**

The key derivation in step 3b uses `resolveKind(ctx, id)` which looks up each UUID in both the `users` and `agents` tables:
- If found in exactly one → kind determined → key computed.
- If found in both → `Ambiguous` → row skipped (logged, not re-keyed). **Safe.**
- If found in neither → `Ambiguous` → row skipped. **Safe.**

The computed key uses `DMConversationKey(kind1, id1, kind2, id2)` which is deterministic and order-independent (lexicographic sort). **A wrong key can only be produced if `resolveKind` returns the wrong kind** — i.e., if a UUID that belongs to a user is found only in the agents table (or vice versa). This would require the principal's record to have been deleted from one table and a different principal with the same UUID to exist in the other table. **This is theoretically possible but extremely unlikely** (UUIDs are generated randomly; collision probability is negligible).

**A repeated run cannot produce a different key than the first run** for the same row, because the kind resolution is deterministic given the current user/agent table state. If the tables change between runs (e.g., a user is deleted), the resolution could differ, but the row would have already been re-keyed on the first run and would be classified as kind-encoded on subsequent runs.

**Bottom line on DMMigrationService safety**: The merge abort guard (B2) prevents data loss. The participant copy guard (B1/D-1) prevents ACL injection. The kind resolution is deterministic but depends on current table state. **A wrong key is possible only in the theoretical case of a UUID collision across user/agent tables**, which is not a practical concern.

---

## 6. Runtime Cost

| Component | Scales with | Queries per unit | Viable at startup? |
|---|---|---|---|
| backfillTopicConversations | Number of `webchat_topic` rows with NULL `conversation_id` | 1 SELECT (all topics) + 2 per topic (INSERT + UPDATE, in TX) | **Yes.** Topics are a low-cardinality entity (dozens to low hundreds per hub). First run processes all; subsequent runs are a single SELECT returning 0 rows + marker check. |
| BackfillService | **Total messages** (not just unprocessed) | 1 page query per batch of 100 + per-group: 1 upsert + N participant inserts + M message updates. On re-run: still scans all messages (the skip check is per-message in-memory). | **Marginal.** A hub with 100K messages does 1000 page queries just to discover there's nothing to do. A hub with 1M+ messages would add noticeable startup latency. The lack of a once-only marker makes this O(total) on every boot. **With a marker, first run is O(total), subsequent runs are O(1).** |
| DMMigrationService | Number of `conversations` where `kind='direct'` | 1 paginated scan of all direct conversations + per-conversation: 0-2 participant inserts (step 1), 0-2 GetUser/GetAgent lookups for kind resolution (step 3b), potential full message scan for merge. | **Marginal.** Direct conversations grow with user×agent pairs. A hub with 1000 users and 50 agents could have ~50K DM conversations. The `resolveKind` step does 2 DB lookups per UUID (GetUser + GetAgent) — for old-format rows, that's 4 lookups per conversation. **With a marker, first run is O(conversations), subsequent runs are O(1).** |

---

## 7. Other Unrun Data Migrations

| Component | Location | What it does | Production caller? |
|---|---|---|---|
| `MigrateGroveToProjectData` | `pkg/ent/entc/migrate_grove_to_project.go:43` | Renames `grove:` group slugs to `project:`, merges duplicates, backfills `project_id`. SQLite-only (no-op on Postgres build). | **ZERO callers.** Not in any cmd, not in startup, not in any non-test Go file outside its own package. |
| `MigrateData` (Beta) | `pkg/ent/entc/migrate_beta.go:117` | Cross-database entity-level data migration (src → dst Ent clients). | Called only from `cmd/server_migrate.go:121` — a manual CLI command (`scion server migrate`). Not auto-run. |
| `MigrateAlphaSQLite` | `pkg/ent/entc/migrate_alpha.go:198` | Upgrades legacy raw-SQL hub.db to Ent schema. | **Auto-runs** at startup via `maybeMigrateLegacySQLite` (`server_foreground.go:1180`). Already wired. |

**`MigrateGroveToProjectData` is the fourth unwired migration.** It is SQLite-only and may be obsolete (the "grove" naming predates the current project model), but its existence with zero callers matches the DMMigrationService pattern — code written, never plugged in.

All other backfill functions found (`BackfillEmptyAgentRoles`, `BackfillDelegationEdges`, `BackfillProjectMembersGroupMarkers`, `BackfillProjectAgentsGroupMarkers`, `BackfillGCPVerificationStatus`, `MigrateAllowListToInvitedUsers`, `BackfillOrigin`, `MigratePluginSecrets`) are wired into the startup path.

---

## Bottom Line

Auto-running all three at startup is a reasonable design **if** two preconditions are met:

1. **Once-only markers.** BackfillService and DMMigrationService have no `migrationCompleted`/`markMigrationCompleted` equivalent. Without markers, they re-scan all data on every boot. BackfillService scanning all messages and DMMigrationService issuing 4 DB lookups per old-format conversation make "run on every boot" untenable on large hubs. Adding markers to the existing `webchat_migrations` table (or adding a parallel mechanism in the Ent-managed `hub_settings` table) is the minimum viable change. With markers, first-boot cost is proportional to data volume (acceptable for a one-time migration), and subsequent boots are O(1).

2. **Advisory lock.** The `backfillTopicConversations` pattern survives concurrent replicas because each unit of work is a single transaction. BackfillService and DMMigrationService do multi-row, multi-table work without transactions. Running them under a `TryAdvisoryLock` (like `runWithAdvisoryLock` at `server_foreground.go:1280`) ensures only one replica does the work; others skip it and see completed state on next access. The existing `LockSchemaMigration` lock covers Ent-level backfills in `Migrate()`; the webchat data migrations need their own key (or should be moved inside `Migrate()` to share the existing lock).

Nothing in the code argues that these migrations are unsafe to run at startup — BackfillService is explicitly documented as idempotent, DMMigrationService has explicit abort-on-partial-failure guards, and `backfillTopicConversations` already proves the pattern. The gap is purely operational wiring: markers, locks, and a startup hook. The data-safety properties are already in the code.

---

## Addendum — mechanism facts gathered after the survey (2026-09-02)

Recorded because the auto-run design is **deferred behind Phases 9b and 9c**, not cancelled. See
"Scheduling" at the end of this addendum for why.

### A1. Advisory lock key namespace is denser than the survey implies

`pkg/store/concurrency.go` at `45c440bd` allocates a contiguous block:

```
0x5C100001 .. 0x5C100013   (19 keys, all taken)
```

`LockSchemaMigration = 0x5C100008`. **The next free key is `0x5C100014`.** Note the block is not
declared in numeric order in the file — `LockGitHubResolutionCacheEviction` (0x...0A) is declared
above `LockDiscordGateway` (0x...09). A new key must be chosen by scanning the whole file, not by
reading the last declaration. A uniqueness test over the key set is worth adding at the same time;
there is nothing today that would catch a duplicate constant.

### A2. `runWithAdvisoryLock` silently no-ops on SQLite

`cmd/server_foreground.go:1280` calls `fn()` directly when the store is nil or does not implement
`AdvisoryLocker`. SQLite takes that path. This is correct — a single-process SQLite hub has no
replicas to race — but it means **the advisory lock provides no protection in the configuration we
test most**. Any correctness argument that leans on the lock is untested by the SQLite suite. The
marker write must therefore be conflict-safe on its own merits, not merely lock-protected.

### A3. The existing marker mechanism is check-then-act, and is hand-written twice

`webchat_migrations` is the only once-only marker in the codebase. It is implemented separately for
each dialect, with no shared interface:

| | SQLite (`webchannel_store.go`) | Postgres (`webchannel_store_postgres.go`) |
|---|---|---|
| read | `SELECT COUNT(*) ... WHERE name = ?` (~:1554) | `SELECT COUNT(*) ... WHERE name = $1` (~:1150) |
| write | `INSERT INTO webchat_migrations (name, completed_at) VALUES (?, ?)` (~:1565) | `INSERT INTO webchat_migrations (name, completed_at) VALUES ($1, NOW())` (~:1160) |

Two defects follow from the shape:

1. **Check-then-act.** `migrationCompleted` then `markMigrationCompleted` is not atomic in either
   dialect. Two replicas can both read zero and both proceed.
2. **DEF-104.** Neither INSERT has an `ON CONFLICT` clause, and `pgWebChatStore.Init()` takes no
   advisory lock. The loser of the race gets a primary-key violation on the marker write, which
   propagates out of `Init()`. The consequence is disproportionate to the cause: a *duplicate
   record of successful work* aborts store initialisation and **silently disables web chat**.

The second is the trap any new marker must not re-cut. An `ON CONFLICT DO NOTHING` (or the SQLite
`INSERT OR IGNORE`) makes the write idempotent and reduces the race to redundant work rather than a
failure. That is the correct posture: the migrations are already documented idempotent, so doing
them twice is waste, while failing to record them is a boot-time outage.

**Design implication.** Do not add a third hand-written pair. The marker belongs behind one
interface with one implementation per dialect, or in the Ent-managed schema where the dialect split
does not exist. Every hand-written pair added is another place DEF-104 can recur.

### A4. Neither service is constructed at boot

The only construction of `BackfillService` is `cmd/server_backfill.go:153`
(`messaging.NewBackfillService(s, s, s)`), reached from the `scion server backfill` CLI.
`DMMigrationService` likewise has no boot-path constructor. There is therefore **no existing wiring
to extend** — the auto-run hook is new code, not a modified call site.

### A5. There is already a startup observability hook to model on, and to replace

`maybeWarnUnbackfilledMessages` (`cmd/server_foreground.go:1310`) calls
`CountUnbackfilledMessages` under a 5s timeout, logs at Warn, and suggests
`scion server backfill --execute`. It is the closest existing analogue for a boot-time migration
hook: bounded, non-fatal, and non-blocking.

It is also **the thing auto-run makes obsolete.** Once the backfill runs automatically, a warning
telling the operator to run it by hand is stale advice. Removing or rewording it belongs in the same
change — per the standing rule that deleting a gate requires grepping for prose that describes it.

### A6. Precedent for once-only state outside `webchat_migrations`

The `_meta` sentinel is written at `cmd/server_foreground.go:2078` via
`s.UpsertHubSetting(ctx, "_meta", metaDoc, "seed", -1, "seeded")`. This is an **upsert**, so it is
conflict-safe by construction — the property `webchat_migrations` lacks. If the marker ends up in
`hub_settings`, this is the pattern to follow, and `LockHubSettingsSeed` already exists to guard
first-boot seeding of that table.

### Scheduling

Deferred behind Phases 9b and 9c, by my call on 2026-09-02.

The auto-run work serves the **upgrade path** for hubs that have not yet cut over. The gteam
instance — the live QA fixture, and the thing ptone wants exercising true end-state behaviour — is
already cut over as of `45c440bd`. Auto-run therefore does not gate the testing that is currently
on the critical path, while 9b and 9c do.

This remains owed against the binding requirement: *"when a hub is updated to a version that
includes this completed refactor, migrations are auto-run, and all switches cut-over by default."*
Phase 9a delivered the switch half. This is the other half, and it is undesigned.

The failure posture is already ruled (§5lg): on migration failure, **log at ERROR, leave the marker
unwritten so the next boot retries, and do not refuse to boot.** That is safe because unmigrated
rows fail closed at `CheckDMParticipantKey` and `isDMParticipant` — a fail-closed invariant is an
asset to spend. Under-migration is recoverable; a hub that will not start is not.

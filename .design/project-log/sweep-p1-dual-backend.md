# Pattern 1: Dual-Backend Divergence Sweep

**Date:** 2026-08-27
**Auditor:** dev-sweep-p1
**Files:**
- `pkg/hub/webchannel_store.go` (SQLite, 1919 lines, `*sqliteWebChatStore`)
- `pkg/hub/webchannel_store_postgres.go` (Postgres, 1446 lines, `*pgWebChatStore`)

## Summary

- **Methods compared:** 51 (45 interface methods + 6 internal methods)
- **1a Divergence findings:** 0
- **1b Shared-mistake findings:** 1 (caller-level, not store-level)
- **Observations (non-defect):** 3
- **Clean pairs:** 51

All 51 method pairs are functionally equivalent. Every difference between the
two backends falls into one of the expected dialect categories:
- `?` vs `$N` placeholders
- Time as formatted RFC3339 string vs `time.Time` / `NOW()`
- `INTEGER` / `int` vs `BOOLEAN` / `bool` for boolean columns
- `COALESCE(col, '')` (SQLite TEXT) vs nullable `*time.Time` scan (Postgres TIMESTAMPTZ)
- `INSERT OR IGNORE` vs `ON CONFLICT ... DO NOTHING`
- `LIKE` vs `ILIKE` (case-insensitive matching)
- `RETURNING` clause presence (Postgres uses it, SQLite doesn't)
- `rowid` vs `id` in batch migration subqueries

No predicate asymmetries, no missing WHERE conditions, no column list mismatches,
no differing ON CONFLICT behavior were found.

---

## Findings

### Finding P1-F1: Duplicate SetMessageReplyTo calls in send paths (1b — shared caller bug)

**Verdict:** DEFECT (caller-level, not store-level — both backends share the same caller)

**Evidence:**

In `handlers_chat_v2.go`, both send paths call `SetMessageReplyTo` twice
with identical arguments. This is a copy-paste duplication bug:

`sendAgentRouted` (lines 1077–1098):
```go
// Phase-3: Store reply-to reference if provided.
if replyToID != "" {
    // ... SetMessageReplyTo(ctx, storeMsg.ID, replyToID) ...  // line 1083
}

// Phase-3: Store reply-to reference if provided.     // <-- DUPLICATE
if replyToID != "" {
    // ... SetMessageReplyTo(ctx, storeMsg.ID, replyToID) ...  // line 1095
}
```

`sendHumanToHuman` (lines 1251–1263):
```go
// Phase-3: Store reply-to reference if provided.
if replyToID != "" && wcs != nil {
    // ... SetMessageReplyTo(ctx, storeMsg.ID, replyToID) ...  // line 1253
}

// Phase-3: Store reply-to reference if provided.     // <-- DUPLICATE
if replyToID != "" && wcs != nil {
    // ... SetMessageReplyTo(ctx, storeMsg.ID, replyToID) ...  // line 1260
}
```

**Impact:** The second call is a redundant upsert (ON CONFLICT DO UPDATE
with the same value). It causes an unnecessary database round-trip on every
reply message but does not corrupt data because the upsert is idempotent.
Both SQLite and Postgres backends execute the duplicate call identically —
this is a shared caller bug, not a store divergence.

---

## Observations (non-defect, noted for completeness)

### Observation P1-O1: seedFromWave1 — lossy read-state migration

Both backends seed `webchat_read_state` from wave-1 `webchat_thread` with:
```sql
INSERT ... INTO webchat_read_state (user_id, conversation_key, last_read_at)
SELECT t.user_id, ..., t.last_read_at
FROM webchat_thread t
WHERE t.last_read_at IS NOT NULL
```

This preserves `last_read_at` but **not** `last_read_message_id` (wave-1 did
not track it). The wave-2 unread computation in `handleChatSpaces` uses
`rs.LastReadMessageID`, so all migrated threads appear "unread" after
migration. This is a known limitation — wave-1 lacked the message-ID
watermark, so no lossless migration is possible.

The `WHERE last_read_at IS NOT NULL` filter is safe because all consumers
(`handleChatSpaces`, `writeConversationReadState`, `handleChatDMs`) treat a
missing `webchat_read_state` row as "unread by default."

**Verdict:** INTENTIONAL — lossy by necessity, correctly handled by all consumers.

### Observation P1-O2: TouchTopicActivity has no deleted_at guard

Both backends update activity timestamps on soft-deleted topics:
```sql
-- SQLite
UPDATE webchat_topic SET last_message_id = ?, last_activity_at = ? WHERE id = ?
-- Postgres
UPDATE webchat_topic SET last_message_id = $1, last_activity_at = NOW() WHERE id = $2
```

Neither guards with `AND deleted_at IS NULL`. This means a race between
topic deletion and an in-flight message will update the deleted topic's
timestamps. This is harmless because `ListTopics` always filters
`deleted_at IS NULL`, so no one reads the stale data. There is no un-delete
operation in the interface, so the orphaned timestamps are permanently
unreachable.

**Verdict:** INTENTIONAL — adding the guard would add complexity with no
observable benefit.

### Observation P1-O3: migrateThreadIDs NULL-check cosmetic difference

Postgres explicitly checks `recipient_id IS NOT NULL AND recipient_id != ''`
while SQLite checks only `recipient_id != ''`. In SQLite, `NULL != ''`
evaluates to NULL (falsy), so the ELSE branch fires for NULL values in both
backends. The behavior is identical but the Postgres version is more
explicit/defensive.

**Verdict:** INTENTIONAL — defensive coding style difference, same behavior.

---

## 1b Analysis: Caller-Callee Semantic Audit

For each SQL-bearing method, callers were identified via grep across
`pkg/hub/`. One sentence describes what each caller needs; the SQL was
verified against that need.

### Methods with multiple callers (multi-caller divergence check)

| Method | Callers | Caller needs | SQL answers correctly? |
|--------|---------|--------------|----------------------|
| `GetTopic` | 12 callers (handleTopicGet, handleTopicPatch, handleTopicDelete, handleConversationSend ×2, authorizeConversationAccess, handleConversationHistory, enrichSearchResults, handleConversationTyping, fireHumanMentionNotifications, ensureProjectGeneralTopic) | "Give me this live topic for display/authorization/routing" | Yes — `deleted_at IS NULL` is correct for all callers |
| `ListTopics` | 5 callers (handleChatSpaces, handleSpaceThreads, ClearTopicDefaultAgent, handleSpaceRead, PromoteDM idempotency) | "List all live topics for this project" | Yes — same filter, same need |
| `GetReadState` | 3 callers (writeConversationReadState self, peer lookup, DM list enrichment) | "Get the read watermark for this user+conversation" | Yes — no filter divergence |
| `TouchDMActivity` | 3 callers (messagebroker, webchannel, touchConversationActivity) | "Update DM last_activity_at and optionally last_message_id" | Yes |
| `TouchTopicActivity` | 3 callers (messagebroker, webchannel, touchConversationActivity) | "Update topic last_activity_at and optionally last_message_id" | Yes (see O2 re: no deleted_at guard) |
| `SetMessageReplyTo` | 4 call sites (2 duplicates — see F1) | "Record reply-to reference for a message" | Yes |
| `IsConversationMuted` | 2 callers (NotifyMention, NotifyDMReceived) | "Is this conversation muted for this user?" | Yes |
| `GetAttachment` | 3 callers (validation in send, staging in dispatch, download handler) | "Get attachment metadata by ID" | Yes |
| `UpdateTopic` | 2 callers (handleTopicPatch, ClearTopicDefaultAgent) | "Update topic fields" | Yes — both use TopicUpdate struct |
| `EnsureGeneralTopic` | 2 callers (ListTopics lazy creation, ensureProjectGeneralTopic) | "Idempotently create #general for this project" | Yes |
| `SetReadState` | 2 callers (handleConversationRead, handleSpaceRead) | "Mark conversation read up to this message" | Yes |
| `UpdateMessageContent` | 2 callers (handleMessageEdit sets content, handleMessageDelete clears it) | "Set the msg column to this value" | Yes — method is value-neutral |
| `RecordChannel` | 2 callers (handlers_broker_inbound, webchannel) | "Record which channel the user last spoke from" | Yes |
| `TouchThread` | 2 callers (handlers_broker_inbound, webchannel) | "Update wave-1 thread watermark" | Yes |
| `GetUserPrefs` | 2 callers (handleChatSpaces, handleUserPrefsRoute) | "Get user's rail sort preferences" | Yes |

**No method has two callers wanting fundamentally different things from it.**

### Hot-spot deep dives

#### seedFromWave1 (coordinator hot-spot #1)

Examined the entire wave-1 → wave-2 seed path in both backends:
1. DM user-side seed: identical logic (INSERT OR IGNORE / ON CONFLICT DO NOTHING)
2. DM agent-side seed: identical logic
3. Read-state seed: both filter `WHERE last_read_at IS NOT NULL` — safe because consumers handle missing rows as "unread" (see O1)
4. Muted seed: SQLite uses two-step INSERT OR IGNORE + UPDATE; Postgres uses INSERT...ON CONFLICT DO UPDATE. Both achieve the same final state.

No defects found. The approaches differ syntactically but produce identical data.

#### migrateThreadIDs (coordinator hot-spot #2)

Examined the CASE logic that transforms `agent:<slug>` → `dm:agent:<aid>:user:<uid>`:
- Both backends correctly determine agent vs user identity based on sender prefix
- Both use the same fallback chain: sender_id/recipient_id → REPLACE(sender/recipient, 'user:', '')
- The NULL handling difference (Postgres explicit `IS NOT NULL`, SQLite implicit) is cosmetic (see O3)
- The batch selection difference (Postgres `id IN (SELECT id ...)`, SQLite `rowid IN (SELECT rowid ...)`) is functionally equivalent
- SQLite's redundant WHERE conditions in the outer UPDATE are belt-and-suspenders, not a semantic difference

No defects found.

---

## Clean Pairs

All 51 method pairs matched. Grouped by category:

### Core Thread Methods (wave-1, legacy)
- `Init` — DDL dialect differences only (TEXT/INTEGER vs TIMESTAMPTZ/BOOLEAN)
- `TouchThread` — identical upsert logic
- `RecordChannel` — identical upsert logic
- `GetLastChannel` — identical SELECT
- `GetThreadPrefs` — identical SELECT
- `SetThreadPrefs` — identical upsert logic
- `GetThreads` — identical SELECT (COALESCE difference is dialect-expected)
- `MarkThreadRead` — identical UPDATE (time parameter vs NOW())

### Topic Methods (wave-2)
- `CreateTopic` — identical INSERT (bool-to-int conversion, time formatting)
- `GetTopic` — identical SELECT with `deleted_at IS NULL`
- `ListTopics` — identical SELECT + lazy #general creation
- `UpdateTopic` — identical dynamic SET builder
- `DeleteTopic` — identical check + soft-delete UPDATE
- `TouchTopicActivity` — identical UPDATE (see O2)
- `EnsureGeneralTopic` — identical INSERT ON CONFLICT DO NOTHING + lookup

### Read-State Methods (wave-2)
- `GetReadState` — identical SELECT (int vs bool scan)
- `SetReadState` — identical upsert
- `GetReadStates` — identical batch IN query
- `SetPinned` — identical upsert
- `SetMuted` — identical upsert
- `IsConversationMuted` — identical SELECT (int vs bool scan)

### User-Prefs Methods (wave-2)
- `GetUserPrefs` — identical SELECT
- `SetUserPrefs` — identical upsert

### DM Methods (wave-2)
- `UpsertDM` — identical upsert with COALESCE-based null preservation
- `ListDMs` — identical SELECT
- `TouchDMActivity` — identical UPDATE

### Search Methods (wave-2)
- `SearchChatMessages` — identical dynamic query (LIKE vs ILIKE for case handling)

### Attachment Methods (W7)
- `CreateAttachment` — identical INSERT
- `GetAttachment` — identical SELECT
- `DeleteAttachment` — identical DELETE
- `GetAttachmentsByMessage` — identical JOIN query
- `GetAttachmentsByMessages` — identical batch JOIN query
- `LinkAttachmentToMessage` — identical INSERT (OR IGNORE vs ON CONFLICT DO NOTHING)

### Message Extension Methods (Phase-3)
- `SetMessageReplyTo` — identical upsert
- `GetMessageExt` — identical SELECT (NullString vs NullTime scan)
- `GetMessageExts` — identical batch SELECT
- `SetMessageEdited` — identical upsert
- `SetMessageDeleted` — identical upsert
- `UpdateMessageContent` — identical UPDATE with RowsAffected check

### DM-to-Space Promotion Methods
- `PromoteDM` — identical 4-step transaction
- `UpdateThreadID` — identical UPDATE
- `DeleteDM` — identical DELETE
- `MigrateReadState` — identical UPDATE
- `CountPendingMessages` — identical COUNT
- `CountMessages` — identical COUNT

### Internal Migration Methods
- `runMigrations` — identical 3-migration sequence
- `migrationCompleted` — identical COUNT check
- `markMigrationCompleted` — identical INSERT (time param vs NOW())
- `migrateThreadIDs` — identical CASE logic (see O3)
- `seedFromWave1` — identical seed logic (see O1)
- `addThreadIDIndex` — identical CREATE INDEX

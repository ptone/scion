# DEF-96: DM promotion loses its history -- Review

## Executive Summary

The change correctly fixes the three faults described in the design doc (F1, F2, F3) by minting a group conversation inside `PromoteDM` and re-keying messages on a two-armed predicate that covers both modern (conversation_id) and legacy (thread_id) populations. The empty-needle guard is present and functional in both stores. Risk level: **LOW**.

## Provenance

- Head: `2f3beca21` on `scion/ca-msg-promote`
- Base: `77ebea1f6` on `scion/tranche-g`
- `merge-base --is-ancestor`: confirmed
- Numstat matches brief exactly:

```
 30   2  pkg/hub/handlers_chat_v2.go
368   0  pkg/hub/handlers_chat_v2_test.go
  1   1  pkg/hub/handlers_read_switch_test.go
 56   8  pkg/hub/webchannel_store.go
266   7  pkg/hub/webchannel_store_dualwrite_test.go
 41   5  pkg/hub/webchannel_store_postgres.go
  5   4  pkg/hub/webchannel_store_promote_test.go
```

## Critical

None.

## Required

None.

## Nit / Optional

### Nit: Duplicate step number in handler comments

**File:** `pkg/hub/handlers_chat_v2.go:2645, 2649`

The provenance log was inserted as step 13, pushing the old step 13 (SSE events) to step 14, but the subsequent comment "14. Return created topic" was not renumbered to 15.

```go
// 14. Publish SSE events (outside transaction -- best-effort)  // line 2645
...
// 14. Return created topic  // line 2649 -- should be 15
```

### Optional/Consider: Silent degradation on transient lookup error

**File:** `pkg/hub/handlers_chat_v2.go:2607-2612`

```go
directConv, convErr := s.store.GetConversationByExternalRef(ctx, "native", key)
if convErr == nil && directConv != nil {
    directConvID = directConv.ID
}
// ErrNotFound is fine -- pre-conversation-model hub. Any other error is
// also tolerable: the legacy arm will still match thread_id rows.
```

The code swallows *all* errors, not just `ErrNotFound`. The comment says "the legacy arm will still match thread_id rows" -- but for a modern DM (write switch on, 99.7% of messages lack `thread_id`), a transient DB error here silently degrades promotion to moving only ~0.3% of messages. The messageCount in the response would surface this, but no log record is emitted.

**Proposed move:** Log a warning when `convErr != nil && !errors.Is(convErr, store.ErrNotFound)` so the degradation is observable without changing the control flow. Not required for merge -- the failure path is extremely narrow and the alternative (failing the entire promotion) has its own costs.

## FYI

### FYI: Postgres C2a else-branch is dead code

In `webchannel_store_postgres.go`, the `if topic.ConversationID != ""` guard at line 1680 always takes the `if` branch because the minting at line 1622-1624 is unconditional. The `else` branch (lines 1688-1696) is unreachable in the postgres store. This is consistent with the design doc ("keep the guard even though it is nearly unreachable") and harmless -- just noting it so nobody spends time "fixing" it later.

### FYI: TestPromoteDM_NoConversationID_SkipsDualWrite was correctly reframed

The test now creates a store WITHOUT the conversations table (its own in-memory DB, not the shared fixture), which is the only path that makes its scenario reachable after P2. The annotation explaining this is clear and accurate.

## Per-Item Verdicts (Brief Items 1-7)

### 1. The `? <> ''` guard exists in BOTH stores -- CONFIRMED

**SQLite** (webchannel_store.go:2186-2191):
```sql
WHERE (? <> '' AND conversation_id = ?)
   OR thread_id = ?
```
Parameters: directConvID, directConvID, dmKey

**Postgres** (webchannel_store_postgres.go:1682-1687):
```sql
WHERE ($3 <> '' AND conversation_id = $3)
   OR thread_id = $4
```
Parameters: directConvID, dmKey

The predicates are semantically identical arm-for-arm, including the guard. The postgres version uses `$3` twice (reuse of a numbered placeholder) instead of passing `directConvID` twice -- same semantics, correct postgres parameterized syntax.

The `else` branch (C2a guard, thread_id-only UPDATE) also carries the guard in both stores. Confirmed.

**Mutation AC-96-1b (delete the guard):** RED. "expected: 2, actual: 3" -- the bystander message with `conversation_id = ''` was swept in. Restored: GREEN.

### 2. U-TX-1 -- lookup is outside the transaction -- CONFIRMED

`GetConversationByExternalRef` is called at `handlers_chat_v2.go:2607`, before `PromoteDM` is called at line 2615. `PromoteDM` calls `BeginTx` at `webchannel_store.go:2129` / `webchannel_store_postgres.go:1626`. The ambient pool call is strictly before the transaction in both stores. `hasConversationsTable()` also remains before `BeginTx` at `webchannel_store.go:2119` as it was before the change.

### 3. PromoteKeys -- no adjacent bare strings -- CONFIRMED

The `PromoteKeys` struct is defined at `webchannel_store.go:299-303` with named fields `DMKey` and `DirectConversationID`. Every call site in the diff uses named field syntax:
- `handlers_chat_v2.go:2615`: `PromoteKeys{DMKey: key, DirectConversationID: directConvID}`
- All test call sites: `PromoteKeys{DMKey: dmKey}` or `PromoteKeys{DMKey: dmKey, DirectConversationID: ...}`

A transposition would be a compile error with named fields. No positional construction exists.

### 4. Lookup only -- no create-on-miss -- CONFIRMED

The handler calls `s.store.GetConversationByExternalRef(ctx, "native", key)` -- a read-only lookup. `ResolveOrCreateDMConversation` is used only on the send paths (lines 1190, 1347, 1488), none of which are in the diff. On any error (including `ErrNotFound`), `directConvID` stays `""`.

### 5. Direct conversation row is untouched (D-1) -- CONFIRMED

The diff writes no row to the direct conversation. The `INSERT INTO conversations` creates a NEW row with `kind='group'`. The handler test (`TestDEF96_PromoteDM_HistoryVisibleOnFirstRead`) explicitly asserts D-1: kind, external_ref, and participant set are byte-identical before and after promotion (AC-96-3). AC-96-4 also asserts the same direct conversation ID is reused for a subsequent DM.

### 6. Replace actions -- CONFIRMED

- `7ad4bcaa` (`t.Fatal` -> `t.Error` + `if x != ""` wrap): The AC-96-2 assertion at `handlers_chat_v2_test.go:2423-2435` uses `t.Error` (non-fatal) and wraps subsequent assertions in `if promResp.ConversationID != ""`. This changes failure *reporting* -- the test still fails if `ConversationID` is empty, but continues to the message-count assertion to give better mutation-test diagnostics. Pass/fail semantics are preserved.
- `handlers_read_switch_test.go` 1/1: Mechanical mock signature update from `string` to `PromoteKeys`. No behavior change.
- No other deletions, weakenings, or removals of existing assertions, gates, or guards found in the diff. Nothing on the prohibition list is touched.

### 7. No new authorization surface, parent_ref stays '' -- CONFIRMED

Both `INSERT INTO conversations` statements (sqlite line 2163, postgres line 1660) hardcode `parent_ref` as `''`. No authorization logic reads `parent_ref` in the diff. The promotion authorization block (steps 2-6, lines 2474-2508) is entirely outside the diff and unchanged. Human-to-human promotion is still refused at step 3 (line 2481).

## Mutation Test Results

### AC-96-6a: Revert WHERE to `WHERE thread_id = ?`, keep both SET columns

```
--- FAIL: TestDEF96_PromoteDM_HistoryVisibleOnFirstRead (0.10s)
    handlers_chat_v2_test.go:2418: promote messageCount = 2, want 5
```

**RED.** Exactly the expected signature: 2 of 5 moved (only the 2 messages with `thread_id = dmKey`; the 3 agent replies with no `thread_id` were left behind). Compiled after adding `_ = directConvID`.

### AC-96-1b: Delete the `? <> ''` guard

```
--- FAIL: TestPromoteDM_WildcardGuard_UnresolvedDirectConversation (0.00s)
    webchannel_store_dualwrite_test.go:1106: expected: 2, actual: 3
    Messages: should move exactly the 2 DM messages
```

**RED.** The bystander message with `conversation_id = ''` was swept into the promotion. This is the data-corruption scenario the guard prevents.

### Restore: All original code restored

```
--- PASS: TestDEF96_PromoteDM_HistoryVisibleOnFirstRead (0.13s)
--- PASS: TestDEF96_PromoteDM_Atomicity (0.14s)
--- PASS: TestPromoteDM_DualWrite_WritesConversation (0.00s)
--- PASS: TestPromoteDM_DualWrite_RePointsConversationID (0.00s)
--- PASS: TestPromoteDM_EmptyConversationID_PreservesExisting (0.00s)
--- PASS: TestPromoteDM_NoConversationID_SkipsDualWrite (0.00s)
--- PASS: TestPromoteDM_DualWrite_UTX1_NoDeadlock (0.00s)
--- PASS: TestPromoteDM_WildcardGuard_UnresolvedDirectConversation (0.00s)
--- PASS: TestPromoteDM_HappyPath (0.00s)
--- PASS: TestPromoteDM_Atomicity (0.00s)
--- PASS: TestPromoteDM_Idempotency (0.00s)
--- PASS: TestPromoteDM_ThreadIDIndex (0.00s)
```

**GREEN.** `git diff` clean -- worktree fully restored.

## Positive Feedback

The fixture design in `TestDEF96_PromoteDM_HistoryVisibleOnFirstRead` is well-crafted: 1 user message with `thread_id` and 4 agent replies without it, matching the measured production ratio. This is what makes the AC-96-6a mutation work -- a uniform fixture would have masked the predicate defect. The explicit annotation at line 2473-2474 ("NOTE: this cannot detect a wildcard...") showing the scope limit of the main test's wildcard check and pointing to the dedicated test is unusually clear self-documentation.

The `PromoteKeys` struct is the right abstraction at the right level -- it solves a real compile-time safety gap without adding ceremony.

## Test Coverage

All acceptance criteria from the design doc are covered:
- AC-96-1 (history visible on first read): `TestDEF96_PromoteDM_HistoryVisibleOnFirstRead`
- AC-96-1b (empty needle guard): `TestPromoteDM_WildcardGuard_UnresolvedDirectConversation`
- AC-96-2 (conversation row created): Verified in the main test
- AC-96-3 (D-1 invariant): Verified in the main test
- AC-96-4 (DM resurrection): Verified in the main test
- AC-96-5 (atomicity): `TestDEF96_PromoteDM_Atomicity`
- AC-96-6a (predicate mutation): Independently verified -- RED then GREEN
- AC-96-1b mutation: Independently verified -- RED then GREEN
- C2a guard (no blanking): `TestPromoteDM_EmptyConversationID_PreservesExisting`
- Re-point assertion: `TestPromoteDM_DualWrite_RePointsConversationID`

No coverage gap found.

## Backward Compatibility

- **Wire format:** The `promoteResponse` JSON gains `conversationId` (new field, `omitempty` on `WebChatTopic`). Non-breaking addition.
- **Interface change:** `PromoteDM` signature changed from `(ctx, topic, string)` to `(ctx, topic, PromoteKeys)`. The mock in `handlers_read_switch_test.go` was updated. Any out-of-tree implementation of `WebChatStore` would need updating -- but this is an internal interface.
- **No removed fields.** No new required fields without defaults.

## Final Verdict

**APPROVE**

Gates run:
- `go build ./pkg/hub/...` -- PASS
- `go vet ./pkg/hub/...` -- PASS
- `gofmt -l` on all 7 changed files -- PASS (no output)
- `make compat-literals` -- PASS
- `make check-authz-guards` -- PASS (no violations)
- `make check-conversation-upsert-guard` -- PASS (no violations)
- `make check-security-marker-gates` -- PASS (all gates pass)
- Promote-related tests (12 tests) -- all PASS
- Mutation testing (3 runs) -- RED/RED/GREEN as expected

Gates not run:
- `go build ./...` (full repo) -- disk space exhausted at link stage; narrowed to `./pkg/hub/...` which covers all changed packages.
- Full `pkg/hub` test suite -- excluded per brief (`TestRS1_StaleAuthorityForcedOverlap` runs 8+ minutes).

Recommendations forwarded for cleanup pass:
1. Renumber step 14 -> 15 at `handlers_chat_v2.go:2649`.
2. Consider logging a warning on non-ErrNotFound conversation lookup failures at `handlers_chat_v2.go:2607-2612`.

# DEF-156: Two backfills spell the same thread two different ways — Review

**Head:** `2fb44cf75271830df5e405387fd9fbec9d7cdde1` on `scion/ca-msg-unify`
**Base:** `77ebea1f6` on `scion/tranche-g`
**Ancestry:** `merge-base --is-ancestor 77ebea1f6 HEAD` → yes
**Commits:** 10

```
 83	 40	pkg/hub/handlers_read_switch_def100_test.go
117	 38	pkg/hub/webchannel_store.go
670	  0	pkg/hub/webchannel_store_def156_test.go   (new)
 99	 26	pkg/hub/webchannel_store_postgres.go
 41	 10	pkg/messaging/backfill.go
 93	  0	pkg/messaging/backfill_test.go             (new tests)
 36	  9	pkg/messaging/conversation.go
 44	  0	pkg/messaging/conversation_test.go          (new tests)
 36	  6	pkg/messaging/derive_key.go
107	  0	pkg/messaging/derive_key_test.go            (new tests)
```

**+1326 / −129** across 10 files.

---

## Executive Summary

The change correctly unifies the two backfill paths so they produce identical
`external_ref` values, adding a pre-mint lookup to avoid shadow conversations
and deriving `surface` from channel rather than hardcoding `"native"`. Risk
level: **LOW**. One Required finding (missing build tag on a new test file);
no correctness or security defects found.

---

## Critical

None.

---

## Required

### R-1. Missing `//go:build !no_sqlite` on `webchannel_store_def156_test.go`

**File:** `pkg/hub/webchannel_store_def156_test.go` (new, 670 lines)

The file opens a sqlite database through `newDEF156TestStore` but does not
carry the `//go:build !no_sqlite` constraint. Every other sqlite-dependent
test file added by this project (`handlers_read_switch_def100_test.go`,
`handlers_chat_v2_test.go`) carries the tag. Without it, `go test -tags
no_sqlite ./pkg/hub/...` will attempt to compile and link the file, producing
a link failure on builds that exclude the sqlite driver.

**Worst case:** CI variants that set `-tags no_sqlite` (e.g. postgres-only
matrix legs) fail to compile `pkg/hub`, blocking the pipeline.

**Fix:** Insert `//go:build !no_sqlite` between the Apache header (line 13)
and the blank line before `package hub` (line 15). Same placement as
`handlers_read_switch_def100_test.go:15`.

---

## Nit / Optional

None.

---

## FYI

### F-1. Step 2 in the DEF-100 T1 control test also goes red under the intercept mutation

When the read-path intercept is disabled (mutation 4), both Step2 and Step3b
go red. Step2 asserts resolution of a post-P2 topic via the intercept; with
the intercept disabled, the `stubConversationReader` returns an error so
resolution fails. This is correct behavior — post-P2 topics can also resolve
via the external_ref lookup in production (when a real `ConversationReader` is
wired), but the test's stub deliberately returns an error. No action needed;
this is informational for future test readers.

### F-2. Several pre-existing test files in `pkg/hub` also lack `//go:build !no_sqlite`

`webchannel_test.go`, `webchannel_store_dualwrite_test.go`,
`webchannel_store_wave2_test.go`, and `attachments_test.go` all use sqlite
but do not carry the build tag. This predates the current branch and is
outside the diff. Noted for context only.

---

## Positive Feedback

- **P1 derivation is genuinely shared.** `ThreadConversationExternalRef` wraps
  `DeriveConversationKey` case 2 and is the sole caller in `pkg/hub`. No
  independent `fmt.Sprintf("thread:%s:%s", ...)` exists. The golden-vector
  test asserts against literal expected strings AND cross-checks against
  `DeriveConversationKey` — both sides of the correctness argument.

- **U-TX-1 discipline is clean.** All six functions across both stores
  (`backfillTopicConversations`, `CreateTopic`, `EnsureGeneralTopic` × sqlite
  + postgres) place every ambient-pool call (`hasConversationsTable()`, the
  pre-mint lookup) before `BeginTx`. No `s.db` call exists between `BeginTx`
  and `Commit` in any of them.

- **P3 refusal is correctly wired.** Unmappable channels flow through
  `ChannelToSurfaceStrict` → `DeriveError{Cause: DeriveErrSurfaceUnmap}` →
  `addDeriveFailure`. Channel conflicts set `channelConflict = true` on the
  group and `persistGroup` refuses the whole group, recording each message as
  `DeriveErrSurfaceConflict`. The error invariant
  `sum(DeriveFailures) + WriteFailures + ResolutionFailures == len(Errors)`
  is preserved by construction.

- **The DEF-100 T1 control test is well-constructed.** Three named subtests
  with independent observability; Step3a is the non-vacuity control for
  Step3b, which is the load-bearing assertion. The mutation proves both
  are correctly aimed.

---

## Test Coverage

New test coverage is thorough:

| Test file | Tests | Coverage |
|---|---|---|
| `derive_key_test.go` | 3 new: golden vectors, empty inputs, DM-prefix refused | P1 derivation |
| `webchannel_store_def156_test.go` | 11 new: both backfill orders, idempotent second boot, surface fidelity, mixed population, multiple topics, CreateTopic/EnsureGeneralTopic paths | P2 store integration |
| `backfill_test.go` | 2 new: channel conflict group refused, unmappable channel refused | P3 refusal |
| `conversation_test.go` | 2 new: valid channel mappings, unmappable channel refusal | P3 ChannelToSurfaceStrict |
| `handlers_read_switch_def100_test.go` | Rewritten: 3 subtests covering post-P2 + legacy population | Mixed population read path |

**Gap:** None identified for the scope of this change.

---

## Backward Compatibility

- **Mixed population preserved.** After fix, database holds both `''`-ref (pre-fix)
  and `thread:`-ref (post-fix) conversations. The topic-lookup intercepts (write-path
  in `ResolveOrCreateConversationByKey`, read-path in
  `ResolveThreadConversationForRead`) remain in place as the sole resolution path for
  pre-fix rows and belt-and-braces for post-fix rows.
- **No migration.** Existing `''`-ref rows are not modified. No schema migration added.
- **No wire-format changes.** No new required fields, no removed fields, no `omitempty`
  changes.
- **`parent_ref` stays `''`.** Unchanged.
- **Startup ordering unchanged.** `cmd/server_foreground.go` not touched.

---

## Deletion Enumeration (Item 7)

### Assertions / guards deleted or changed

| What | Where | Old assertion | New assertion | Verdict |
|---|---|---|---|---|
| Precondition `extRef != ""` | `handlers_read_switch_def100_test.go:93` (old) | `extRef != ""` means empty-ref was written | `extRef != expectedExtRef` means derived key was written | Old assertion was **false** post-P2. New assertion is **correct**. Property (conversation row has the expected key) is still tested. |
| Step 3 control | `handlers_read_switch_def100_test.go:115-125` (old) | `resultOld != nil` on post-P2 topic without intercept | Re-pointed to legacy (`''`-ref) topic in Step3a/3b subtests | Old assertion was **vacuous** post-P2 (post-P2 topics would resolve via extRef even without intercept). New assertion targets the population where the intercept is load-bearing. Correct re-pointing. |
| Comments re `external_ref = ''` | `derive_key.go`, `conversation.go` | Described intercept as sole guard | Describes mixed population — intercept is sole guard for pre-fix, belt-and-braces for post-fix | Comment update, no behavioral change. Correct. |
| `needMint` condition in `EnsureGeneralTopic` | `webchannel_store.go` | `inserted > 0 && hasConvTable` (always minted) | `inserted > 0 && hasConvTable && needMint` (skips if pre-mint lookup found one) | Correct — pre-mint lookup replaces unconditional minting. |

### Prohibition list — confirmed NOT touched

- `authenticatedSender` — not in diff
- `parseDMKeyIDs` — not in diff
- `isDMParticipant` — not in diff
- `checkDMParticipantKey` — not in diff
- `EnsureParticipant` and `left_at` preservation — not in diff
- `direct` non-empty `external_ref` predicate — not in diff
- `Broadcasted` server-side forcing — not in diff
- `authorizeAgentMessage` reachability — not in diff
- Write-path intercept (`ResolveOrCreateConversationByKey`) — present, unchanged
- Read-path intercept (`ResolveThreadConversationForRead`) — present, unchanged
- `cmd/server_foreground.go` ordering — not in diff
- No live write path (Route 2) tests edited
- No migration

---

## Brief Item Verdicts

### 1. Collision surface P2 creates — CLOSED

`CreateTopic` has exactly one production caller (`pkg/hub/handlers_chat_v2.go:473`)
which mints a fresh `uuid.New().String()` ~12 lines above. No second caller, no
request-supplied ID.

`EnsureGeneralTopic` resolves by name, but the `INSERT ... ON CONFLICT DO NOTHING`
on `webchat_topic` combined with the pre-mint conversation lookup makes the worst
case "two concurrent callers both mint a conversation, loser's orphan conversation
is unreachable" — a leaked row, not a data-loss or error-out failure. The existing
`WHERE conversation_id IS NULL` guard on the UPDATE prevents double-linking.

No `ON CONFLICT` clause was added on the conversations INSERT. The partial unique
index prevents duplicates by failing the loser; the pre-mint lookup is the
convergence mechanism.

### 2. U-TX-1 — CLEAN

All six functions verified (3 per store × 2 stores). Every `s.db` call
(`hasConversationsTable()`, pre-mint lookup) occurs before `BeginTx`. Inside
the transaction, only `tx.Exec`, `tx.QueryRow` calls exist.

### 3. P1 — one derivation, literal test vectors — CONFIRMED

`pkg/hub` calls `messaging.ThreadConversationExternalRef()` exclusively. No
independent `fmt.Sprintf("thread:%s:%s", ...)` in the hub.
`TestThreadConversationExternalRef_GoldenVectors` asserts 4 literal expected
strings AND cross-checks against `DeriveConversationKey`.

### 4. sqlite/postgres asymmetry — PRESERVED

sqlite gates on `hasConversationsTable()` in `backfillTopicConversations` and
`EnsureGeneralTopic`. postgres does not gate — migrations guarantee the table.
Neither store grew the other's gate. Predicates and lookups are semantically
identical where they should be (`$N` vs `?` placeholder difference only).

### 5. P3 — refusal, not coercion — CONFIRMED

`ChannelToSurfaceStrict` returns error for unmappable channels →
`DeriveError{Cause: DeriveErrSurfaceUnmap}` → `addDeriveFailure`. Channel
conflicts set `channelConflict = true` → `persistGroup` refuses entire
group → each message recorded as `DeriveFailure[surface_conflict]`.
Error invariant `sum(DeriveFailures) + WriteFailures + ResolutionFailures == len(Errors)` holds.

### 6. DEF-100 T1 intercept mutation — CONFIRMED

Disabling the read-path intercept (`if false &&` on line 455 of
`conversation.go`):
- Step2 → RED (post-P2 topic unresolvable via stub reader)
- Step3a → GREEN (negative assertion, holds either way)
- Step3b → RED (legacy topic unresolvable without intercept)

Step3b goes red independently as a named subtest. Correct construction.

### 7. Deletions — ENUMERATED ABOVE, ALL CLEAN

No assertion was deleted without the property being re-protected elsewhere.
All prohibition-list items confirmed untouched.

### 8. Mixed population — CONFIRMED

`TestDEF156_MixedPopulation_LegacyTopicStillResolves`: creates a pre-fix
`''`-ref conversation with topic link, verifies topic still resolves and
message stays stamped.

`TestDEF100_T1` Step3b: pre-fix topic resolves through
`ResolveThreadConversationForRead` with the topic-lookup intercept.

Both pass green.

---

## Mutation Test Results (Raw)

### Mutation 1: Unmodified source (baseline)

```
All 14 tests PASS (TestDEF156_* + TestDEF100_T1_*)
ok  github.com/GoogleCloudPlatform/scion/pkg/hub  0.160s
```

### Mutation 2: Revert P1 key in backfillTopicConversations (`extRef := ""`)

```
--- FAIL: TestDEF156_FreshCutover_MessageBackfillFirst (0.00s)
    message msg-1 must be stamped on the topic's conversation  (expected dd3b..., got a658...)
    message msg-2 must be stamped on the topic's conversation
    message msg-3 must be stamped on the topic's conversation
    topic's conversation must match the one at the derived key
FAIL  github.com/GoogleCloudPlatform/scion/pkg/hub  0.122s
```

Goes red reporting messages land on wrong conversation. Correct.

### Mutation 3: Remove P2 pre-mint lookup (always mint)

```
--- FAIL: TestDEF156_FreshCutover_MessageBackfillFirst (0.00s)
    message msg-1 must be stamped on the topic's conversation  (expected aa8b..., got d7fe...)
    topic's conversation must match the one at the derived key
--- PASS: TestDEF156_FreshCutover_TopicBackfillFirst (0.00s)
FAIL  github.com/GoogleCloudPlatform/scion/pkg/hub  0.123s
```

MessageBackfillFirst goes red (Route 3 mints first, topic mints second
shadow conversation). TopicBackfillFirst passes (topic mints first, Route 3
converges via index). Correct — pre-mint lookup is load-bearing for
message-first ordering.

### Mutation 4: Disable read-path intercept (`if false &&`)

```
--- FAIL: TestDEF100_T1_ProductionWriter_ReadResolver (0.00s)
    --- FAIL: Step2_PostP2_ResolvesWithIntercept (0.00s)
        ResolveThreadConversationForRead returned nil
    --- PASS: Step3a_LegacyTopic_FailsWithoutIntercept (0.00s)
    --- FAIL: Step3b_LegacyTopic_ResolvesWithIntercept (0.00s)
        legacy topic should resolve WITH the topic-lookup intercept
FAIL  github.com/GoogleCloudPlatform/scion/pkg/hub  0.124s
```

Step3b goes red on its own line. Step3a stays green. Correct.

### Mutation 5: Restore all, confirm green

```
All 14 tests PASS (TestDEF156_* + TestDEF100_T1_*)
ok  github.com/GoogleCloudPlatform/scion/pkg/hub  0.160s
```

All green. Clean restore confirmed.

---

## Gates

| Gate | Result |
|---|---|
| `go build ./...` | 0 (clean) |
| `go vet ./...` | 0 (clean) |
| `make compat-literals` | 0 |
| `make check-authz-guards` | 0 |
| `make check-conversation-upsert-guard` | 0 |
| `make check-security-marker-gates` | 0 |
| `gofmt -l` (changed files) | clean (expected pre-existing drift on `handlers_agents_core.go` and `web_test.go` not in diff) |

---

## Final Verdict

**REQUEST CHANGES**

One **Required** finding: `webchannel_store_def156_test.go` needs `//go:build !no_sqlite`.
This is a one-line fix.

No Critical findings. The implementation is correct, well-tested, and
carefully preserves backward compatibility for the mixed population. After
the build tag is added, this is ready to merge.

**Gates run:** `go build ./...`, `go vet ./...`, `make compat-literals`,
`make check-authz-guards`, `make check-conversation-upsert-guard`,
`make check-security-marker-gates`, `gofmt -l` on changed files. All pass.

**Gates not run:** Full `pkg/hub` test suite (35+ minute wall time, ends in
panic on base branch — documented in brief). Individual test functions were
run by name for all DEF-156 and DEF-100 T1 tests. Five mutation tests
completed with expected red/green results.

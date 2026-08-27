# Patterns 2+3: Validation & Guard Asymmetry Sweep

**Agent:** dev-sweep-p2p3
**Date:** 2026-08-27
**Framing:** CALLER-DOWN — start from what the caller needs, trace to the store method, check if the method delivers it.

---

## Pattern 2: Validation Asymmetry

### Summary
- Tables with >1 writer examined: 4 (webchat_topic, webchat_read_state, webchat_dm, webchat_dm via seedFromWave1)
- Findings: 2 (one DEFECT, one INTENTIONAL)
- Clean tables: 3
- Single-writer tables examined (no asymmetry possible): 3 (webchat_conversation_context, webchat_user_prefs, webchat_thread)
- Scoped-out: conversations table (store.Conversation type, CreateConversation, UpsertConversationByExternalRef, UpdateConversation do NOT exist in this codebase — see Note below)

**Note on DEF-28/29 entities:** The task brief references `CreateConversation`, `UpsertConversationByExternalRef`, and `UpdateConversation` in `pkg/store/entadapter/`. These methods and the `store.Conversation` type do not exist anywhere in the current codebase. The only conversation-related store code is `UpsertConversationReference` in `extras/scion-teams/internal/teams/store.go`, which operates on a different entity (`ConversationReference` — a Teams bot framework artifact). The DEF-28/29 origin code is either planned, removed, or lives in a different repository.

---

### Finding P2-F1: webchat_topic — `default_agent` validation gap on CreateTopic

**Verdict:** DEFECT

**Columns:** `default_agent`

**Writers compared:**
| Column | CreateTopic (handler) | UpdateTopic (handler) | EnsureGeneralTopic | TouchTopicActivity | PromoteDM (handler) |
|---|---|---|---|---|---|
| `name` | validated: required, trimmed, ≤100 runes, regex | validated: same rules (if provided) | hardcoded `'general'` | not written | validated: ≤100 runes, regex |
| `default_agent` | **NOT validated** — passed through from request body | **NOT validated** — passed through from request body | not written | not written | set to `agentID` (from DM key, already validated) |
| `is_general` | never set by handler (defaults false) | not writable | hardcoded `true` | not written | not set (defaults false) |
| `project_id` | from URL path param | not writable | from argument | not written | resolved from agent |
| `created_by` | from auth context | not writable | from argument | not written | from auth context |

**Evidence:**

- `handlers_chat_v2.go:424-446` — `handleCreateThread` parses `body.DefaultAgent` from JSON request and passes it straight through to `CreateTopic` at line 460 without any validation (no existence check, no format check, no emptiness check).
- `handlers_chat_v2.go:578-579` — `handleTopicPatch` passes `body.DefaultAgent` straight through to `UpdateTopic` at line 587 without validation.
- Both backends (`webchannel_store.go:773-797`, `webchannel_store_postgres.go:415-446`) accept any string for `default_agent`.

**Impact:** A caller can set `default_agent` to an arbitrary string (non-existent agent ID, garbage, another user's agent ID). The field is used by the chat frontend to auto-route messages. Setting it to a non-existent agent would cause silent routing failures. Setting it to an agent the user doesn't own could be an information-leak vector (the frontend would attempt to send to that agent, revealing its existence). The asymmetry is that `name` gets rigorous validation but `default_agent`, which also comes from user input in the same request body, gets none.

**Reachable path:** `POST /api/v1/projects/{id}/chat/topics` with body `{"name":"valid-name","defaultAgent":"literally-anything"}`.

---

### Finding P2-F2: webchat_read_state — seedFromWave1 muted-flag migration asymmetry between backends

**Verdict:** INTENTIONAL (but undocumented)

**Columns:** `muted`

**Writers compared:**
| Column | SetReadState | SetPinned | SetMuted | seedFromWave1 (SQLite) | seedFromWave1 (Postgres) |
|---|---|---|---|---|---|
| `last_read_message_id` | always set | not written | not written | not written | not written |
| `last_read_at` | always set | not written | not written | set if wave-1 has value | set if wave-1 has value |
| `pinned` | not written (defaults 0) | always set | not written | not written | not written |
| `muted` | not written (defaults 0) | not written | always set | two-step: INSERT OR IGNORE + UPDATE | single-step: ON CONFLICT DO UPDATE |

**Evidence:**

- SQLite `seedFromWave1` (`webchannel_store.go:1390-1414`): uses INSERT OR IGNORE to create the row, then a separate UPDATE to set muted=1. This two-step approach means: (a) if the row was already created by `seedReadState`, the INSERT is a no-op and the UPDATE sets muted; (b) if the row didn't exist, the INSERT creates it with muted=1 and the UPDATE is redundant.
- Postgres `seedFromWave1` (`webchannel_store_postgres.go:1038-1050`): uses `INSERT ... ON CONFLICT DO UPDATE SET muted = EXCLUDED.muted`. This is a single atomic operation.

**Why INTENTIONAL:** Both approaches produce the same end state. The SQLite version uses a two-step approach because SQLite's INSERT OR REPLACE would lose other columns, and the Postgres version can use ON CONFLICT DO UPDATE safely. The asymmetry is in mechanism, not in outcome. However, neither backend documents why the approaches differ.

---

### Clean Tables

1. **webchat_topic — `name` column**: Consistent validation across all 3 user-facing writers (CreateTopic, UpdateTopic, PromoteDM handlers). All validate: required, trimmed, ≤100 runes, regex match `^[a-zA-Z0-9][a-zA-Z0-9 _\-]*$`. EnsureGeneralTopic hardcodes `'general'` which satisfies all constraints. TouchTopicActivity does not write the column. (**3/3 handlers validate, 1/1 internal hardcodes, 1/1 internal skips** — clean.)

2. **webchat_topic — `is_general` column**: Only written at creation time: CreateTopic (from handler) never sets it (defaults false), EnsureGeneralTopic hardcodes `true`. Not writable via UpdateTopic. Immutable by design. (**Clean.**)

3. **webchat_dm — all columns**: Three writers (UpsertDM, TouchDMActivity, seedFromWave1) each write different column subsets with no overlapping validation gap. UpsertDM: sets all columns, uses COALESCE for watermarks. TouchDMActivity: only updates watermarks (plain UPDATE, not INSERT). seedFromWave1: INSERT with hardcoded peer_kind values. No column gets validated by one writer and not another — none validate at the store level, all rely on caller-level key parsing. (**3/3 consistent.**)

4. **webchat_read_state — `last_read_message_id` column**: Only SetReadState writes this column. Handler validates `messageId` is required and non-empty (`handlers_chat_v2.go:1929-1932`). Single-writer for this column. (**Clean.**)

5. **webchat_read_state — `pinned` and `muted` columns**: Each has exactly one handler writer (SetPinned, SetMuted) with identical validation shape (pointer-required pattern: `body.Pinned == nil` → error). Both go through the same `authorizeConversationAccess` before writing. (**Clean.**)

---

## Pattern 3: Guard Asymmetry

### Summary
- Update paths examined: 6
- Findings: 2 (one DEFECT, one INTENTIONAL)
- Clean paths: 4

---

### Finding P3-F1: UpdateGCPServiceAccount — immutable fields protected by omission with no structural test

**Verdict:** INTENTIONAL (well-documented, but fragile)

**Method:** `ExternalStore.UpdateGCPServiceAccount` (`pkg/store/entadapter/external_store.go:201-229`)

**Fields guarded:**
| Field | Create | Update | Guard type |
|---|---|---|---|
| Email | unconditional set | unconditional set | mutable |
| ProjectID | unconditional set | unconditional set | mutable |
| DisplayName | unconditional set | unconditional set | mutable |
| DefaultScopes | unconditional set | unconditional set | mutable |
| Verified | unconditional set | unconditional set | mutable |
| VerificationStatus | unconditional set | unconditional set | mutable |
| VerificationError | unconditional set | unconditional set | mutable |
| Managed | unconditional set | unconditional set | mutable |
| ManagedBy | unconditional set | unconditional set | mutable |
| VerifiedAt | conditional (non-zero) | conditional (clear if zero, set otherwise) | mutable, nullable |
| **CreatedBy** | unconditional set | **OMITTED** | immutable by omission |
| **Scope** | unconditional set | **OMITTED** | immutable by omission |
| **ScopeID** | unconditional set | **OMITTED** | immutable by omission |
| **Created** | unconditional set | **OMITTED** | immutable by omission |

**Evidence:** The 50-line comment at lines 163-200 explains the invariant: CreatedBy feeds `Resource.OwnerID` for the owner bypass in `checkAccessForUser`, Scope selects which arm of `gcpServiceAccountVerdict` runs, and ScopeID is what `ReachableFromProject` compares. All three are authorization inputs. The comment explicitly warns that making them writable through Update would create a writable authorization bypass.

**Why this is a finding despite being intentional:** The invariant is enforced ONLY by the comment and the absence of four setter calls. There is no reflect-based field-classification test (like the one in `project_settings_resolved_guard_test.go` for `ResolvedProjectSettings`) that would break if a future contributor adds a setter. The comment even warns: "if you are here to add a setter, this comment is the entire control, and you have just reached it." This is the exact shape the task describes — a well-documented guard that lacks structural enforcement.

**Recommendation:** A reflect-based test on `store.GCPServiceAccount` that classifies fields into {mutable, immutable} and asserts the Update builder only touches the mutable set would catch the failure mode the comment warns about.

---

### Finding P3-F2: CreateGitHubInstallation vs UpdateGitHubInstallation — Status defaulting asymmetry

**Verdict:** DEFECT

**Method:** `ExternalStore.CreateGitHubInstallation` vs `UpdateGitHubInstallation` (`pkg/store/entadapter/external_store.go:396-467`)

**Fields compared:**
| Field | Create | Update | Asymmetry |
|---|---|---|---|
| AccountLogin | unconditional set | unconditional set | — |
| AccountType | **defaulted**: `""` → `"Organization"` | unconditional set (no default) | **YES** |
| AppID | unconditional set | unconditional set | — |
| Repositories | unconditional set (JSON) | unconditional set (JSON) | — |
| Status | **defaulted**: `""` → `"active"` | unconditional set (no default) | **YES** |
| Created | defaulted if zero | not written | Intentional (immutable) |
| Updated | defaulted if zero | set to `time.Now()` | Intentional |

**Evidence:**

- `CreateGitHubInstallation` (`external_store.go:408-419`):
  ```go
  if installation.Status == "" {
      installation.Status = store.GitHubInstallationStatusActive
  }
  accountType := installation.AccountType
  if accountType == "" {
      accountType = "Organization"
  }
  ```
- `UpdateGitHubInstallation` (`external_store.go:452-467`):
  ```go
  _, err := s.client.GithubInstallation.UpdateOneID(installation.InstallationID).
      SetAccountLogin(installation.AccountLogin).
      SetAccountType(installation.AccountType).  // no default
      ...
      SetStatus(installation.Status).            // no default
  ```

**Impact:** If a caller calls `UpdateGitHubInstallation` with an empty `Status` or `AccountType` string, the column will be set to `""` (an invalid state). In Create, the same empty input would be normalized to a valid default. This matters if a read-modify-write cycle drops a field — the Update path would silently corrupt the row.

**Reachable path:** Any handler that reads a GitHub installation, modifies an unrelated field, and writes it back without explicitly preserving Status could trigger this. The `UpdateGitHubInstallation` handler at `external_store.go:452` is called from the webhook handler path in the broker.

---

### Clean Update Paths

1. **UpdateTopic (TopicUpdate struct)** — `webchannel_store.go:773-797` / `webchannel_store_postgres.go:415-446`
   Only 2 optional fields (`Name *string`, `DefaultAgent *string`). Both use the same guard pattern: `if field != nil { append SET clause }`. DefaultAgent correctly uses `nullableString`/nil-interface for empty-string-means-clear semantics. Name does not, because Name should never be NULL. The asymmetry is correct: Name and DefaultAgent have different nullability semantics and the guards reflect that. (**Clean.**)

2. **UpsertDM** — `webchannel_store.go:1072-1093` / `webchannel_store_postgres.go:708-733`
   6 fields. On conflict update: `peer_id` and `peer_kind` are unconditionally overwritten; `last_message_id` and `last_activity_at` use COALESCE (preserve existing if new value is NULL). This is documented in the method comment: "An empty LastMessageID means 'unknown', not 'clear it'." All 4 updated fields are consistently guarded — the two identity fields are always overwritten, the two watermark fields are always COALESCE'd. (**Clean.**)

3. **SetUserPrefs** — `webchannel_store.go:1044-1060` / `webchannel_store_postgres.go:676-696`
   3 fields (`space_sort_mode`, `space_order`, `thread_sort_mode`). All three are unconditionally set on conflict. `space_order` uses `nullableString`/nil-interface (it's the only nullable column). Handler validates sort mode values. (**Clean.**)

4. **UpdateProject** — `pkg/store/entadapter/project_store.go:289-353`
   13 mutable fields. All required fields (Name, Slug, OwnerID, Visibility) are unconditionally set. All optional/nullable fields (GitRemote, DefaultRuntimeBrokerID, Labels, Annotations, SharedDirs, GitHubInstallationID, GitHubPermissions, GitHubAppStatus, GitIdentity) use the same guard pattern: `if non-zero/non-nil → Set, else → Clear`. CreatedBy is correctly omitted (immutable). All guards are symmetric within their category. (**Clean.**)

---

## Pattern 3 Extension: Entities Lacking Structural Guard Tests

The task asked: which entities have the same update-path shape as `store.Conversation` (which doesn't exist here) and NO reflect-based field-classification test?

The closest existing test is `project_settings_resolved_guard_test.go`, which uses `reflect.TypeOf(...).NumField()` to iterate struct fields and enforce a contract. No other entity in the codebase has an equivalent test. Entities that would benefit from one, in priority order:

1. **`store.GCPServiceAccount`** — immutable authorization-input fields (`CreatedBy`, `Scope`, `ScopeID`) protected only by omission and a comment (P3-F1 above). Highest risk because the failure mode is a writable authorization bypass.

2. **`store.RuntimeBroker`** — `CreateRuntimeBroker` (`project_store.go:618-679`) vs `UpdateRuntimeBroker` (`project_store.go:709-775`). The Update includes `CreatedBy` in neither the required-set nor the optional-set, but unlike GCPServiceAccount there is no comment explaining why. The omission is correct but undocumented.

3. **`store.Project`** — `CreateProject` vs `UpdateProject`. `CreatedBy` is correctly omitted from Update, but undocumented (unlike the GCPServiceAccount comment). A reflect test would catch accidental addition.

4. **`store.GitHubInstallation`** — `InstallationID` (the natural key) is correctly not updated, but there's no comment or test enforcing this. The Status/AccountType defaulting gap (P3-F2) would also be caught by a test that compares Create vs Update field coverage.

---

## Summary of Findings

| ID | Pattern | Entity | Verdict | Severity | Description |
|---|---|---|---|---|---|
| P2-F1 | Validation | webchat_topic | **DEFECT** | Medium | `default_agent` accepted without any validation on CreateTopic and UpdateTopic handlers, while `name` gets rigorous validation. Arbitrary string can be stored. |
| P2-F2 | Validation | webchat_read_state | INTENTIONAL | Low | seedFromWave1 uses different mechanisms (two-step vs single-step) for muted flag migration between SQLite and Postgres. Same outcome, undocumented difference. |
| P3-F1 | Guard | GCPServiceAccount | INTENTIONAL | **High** (if guard fails) | Immutable authorization-input fields protected by omission and comment only. No structural test to prevent regression. |
| P3-F2 | Guard | GitHubInstallation | **DEFECT** | Medium | Create defaults Status and AccountType for empty strings; Update does not. Empty-string write on Update produces invalid row state. |

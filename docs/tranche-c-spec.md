# Tranche C Specification — Conversation Model Integration (Handler Layer)

**Produced by:** ca-msg-em10  
**Date:** 2026-08-28  
**Branch:** scion/ca-msg-em10-tranche-c-measurement  
**Prerequisite:** All tranche A + B changes merged on main  
**Strategy:** Re-derive from intent on current main. Do not cherry-pick from messaging-v2.

---

## Preamble

This document specifies the behavior a developer must implement across four
handler-layer files to complete the conversation model integration. The
developer should never need to open the messaging-v2 branch. Every requirement
below is stated as behavior, invariant, or acceptance criterion — not as code
to copy.

### Terminology

| Term | Definition |
|------|------------|
| **Conversation** | A `store.Conversation` row keyed by `ExternalRef`, representing a DM pair or thread. |
| **ConversationID** | The UUID primary key of a Conversation row. Stamped onto `store.Message.ConversationID` at persist time. |
| **ExternalRef** | The canonical key of a conversation (`dm:user:<uuid>:agent:<uuid>` for DMs, the ThreadID for threads). Created by `DeriveConversationKey` → `UpsertConversationByExternalRef`. |
| **Surface** | The platform originating a conversation: `"native"`, `"slack"`, `"teams"`, etc. Broker-edge only. |
| **Dual-write** | The transitional state where both legacy (Channel+ThreadID) and new (ConversationID) routing data are written. Already on main for several sites. |
| **Read-switch** | Runtime flag (`ConversationReadSwitch`) controlling whether reads query by ConversationID or by legacy Channel+ThreadID. |
| **Divergence** | Metric comparing old-model routing (Channel+ThreadID) against new-model routing (ConversationID+ExternalRef). Already on main via `ComputeDivergenceMatch` / `LogDivergence`. |
| **ValidateLegacyMessage** | Envelope validation function (to be added to `pkg/messaging`). Validates StructuredMessage fields before dispatch. |
| **authenticatedSender(ctx)** | B5 security function extracting (kind, id) from auth context. Already on main (6 references). |
| **Pre-resolved ConversationID** | When the CLI has already resolved a conversation_id and sends it on the StructuredMessage. The handler uses it directly instead of re-deriving. |

### Security invariants (non-negotiable)

These constraints are inherited from merged security PRs and must be preserved
by every change in this tranche:

1. **G-1 (authenticatedSender):** Sender identity for conversation key
   derivation MUST come from the authenticated context via
   `authenticatedSender(ctx)`, never from the client-supplied message payload.
   The key IS the access control list for direct conversations.

2. **No validate-and-compare:** Do not "validate" the client-supplied
   SenderID by comparing it to auth and continuing on mismatch. Just use auth.

3. **B10 (non-fatal derivation):** Derivation failures must not reject
   requests. During dual-write, a failed key derivation logs a warning and
   skips conversation resolution — the message still delivers via the legacy
   path.

4. **Marker count gates:** After implementation, the following counts on
   main must be preserved exactly:
   - `authenticatedSender` — 6 references in handlers_agent_messaging.go
   - `validateDefaultAgent` — 6 references in handlers_chat_v2.go
   - `ActionAttach` authorization checks — 3 total (1 in handlers_agent_messaging.go, 2 in handlers_chat_v2.go)

### What already exists on main

The developer should be aware of what is already implemented:

- **DeriveConversationKey** — in `pkg/messaging/derive_key.go`. Unifies thread
  and DM key derivation. Already called from `handleAgentOutboundMessage` and
  the dual-write sites in `handleAgentMessage`, `handleGroupMessage`, and
  `processMentions`.
- **ResolveOrCreateConversationByKey** — in `pkg/messaging/derive_key.go`.
  Wraps `UpsertConversationByExternalRef` with kind classification.
- **ResolveOrCreate{DM,Thread}Conversation** — in `pkg/messaging/conversation.go`.
  Already called at 5 dual-write sites in handlers_agent_messaging.go, 1 site
  in handlers_broker_inbound.go, 1 site in messagebroker.go.
- **ResolveDMConversationForRead / ResolveThreadConversationForRead** — in
  `pkg/messaging/conversation.go`. Read-only resolution for the read-switch.
- **ComputeDivergenceMatch / LogDivergence** — in `pkg/messaging/divergence.go`.
  Already called at all dual-write sites.
- **DivergenceMetrics** — global singleton with Matches/Mismatches/Fallbacks
  counters.
- **authenticatedSender** — in handlers_agent_messaging.go (4 call sites +
  function + comment = 6 references).
- **parseDMKeyIDs / isDMParticipant** — in handlers_chat_v2.go. DM ownership
  validation.
- **validateDefaultAgent** — in handlers_chat_v2.go. DEF-31 default agent
  validation.

### What does NOT exist on main (tranche C scope)

- **ValidateLegacyMessage** — zero references. Must be added.
- **CheckConversationConsistency** — zero references. Must be added.
- **ConversationReadSwitch** — zero references. Must be added.
- **Phase 11 broker-edge fields** (Surface, ExternalRef, ParentRef on
  request structs) — zero references. Must be added.
- **DEF-11 pre-resolved ConversationID handling** — zero references. Must be
  added.
- **AC-33 cross-project mention validation** — zero references. Must be added.
- **ValidateCrossProjectAddressees** — zero references. Must be added in
  `pkg/messaging`.

---

## File 1: handlers_agent_messaging.go

### 1.1 What it must do and why

This file handles three message-sending paths: agent-to-human outbound
(`handleAgentOutboundMessage`), human/agent-to-agent inbound
(`handleAgentMessage` → single dispatch, group[], broadcast), and mention
fan-out (`processMentions`). Tranche C adds four capabilities:

#### 1.1.1 ValidateLegacyMessage at three choke points

**Intent:** Every message entering the system through the agent messaging API
must pass envelope validation before any side effects (conversation creation,
dispatch, persistence). This catches malformed payloads — empty Msg on
non-attachment messages, unknown message types, empty Channel with non-empty
ThreadID — before they create orphaned conversation rows or reach agents.

**Where:** Insert `ValidateLegacyMessage` calls at three sites:

1. **handleAgentOutboundMessage** — after the `StructuredMessage` is assembled
   from the request fields (around the existing `storeMsg` construction), but
   BEFORE conversation resolution. If validation fails, return 400 with the
   validation error. The ordering constraint is critical: validation MUST run
   before `DeriveConversationKey` / `ResolveOrCreateConversationByKey` so that
   a rejected request never creates a conversation row.

2. **handleAgentMessage** — after the structured message is assembled from the
   `MessageRequest` fields, before the group[] recipient detection and before
   the mention cap. If validation fails, return 400. Same ordering constraint:
   before any conversation resolution.

3. **handleProjectBroadcast** — after the broadcast message is assembled, before
   agent enumeration begins. If validation fails, return 400.

**Failure mode prevented:** Without validation, a message with `Type: ""`` or
`Msg: ""` can create a conversation row via `UpsertConversationByExternalRef`,
then fail dispatch or persistence, leaving an orphaned conversation that future
divergence checks compare against.

#### 1.1.2 DEF-11: Pre-resolved ConversationID handling

**Intent:** When the CLI has already resolved a `conversation_id` (via the
S4 conversation references flow), the handler should use it directly instead
of re-deriving from sender/recipient/thread. This avoids a redundant
`UpsertConversationByExternalRef` round-trip and ensures the CLI's resolution
is authoritative.

**Where:** In `handleAgentMessage`, at the single-dispatch path (not group[],
not broadcast), at the point where the `storeMsg` is constructed and
ConversationID would normally be derived.

**Behavior:**

1. If `structuredMsg.ConversationID` is non-empty:
   - Stamp it directly onto `storeMsg.ConversationID`.
   - Look up the conversation from the store (`GetConversation`) to retrieve
     its `ExternalRef` for the divergence comparison.
   - If the store lookup fails, this is a DEF-11 edge case: the CLI sent a
     ConversationID that doesn't exist. Record a divergence entry with
     `Reason: "conv-lookup-failed"` and `Fallback: true`. The `Fallback` flag
     routes the event to the fallback counter, not the mismatch counter. The
     message still delivers — this is non-fatal.
   - If the store lookup succeeds, use the retrieved `ExternalRef` for the
     normal `ComputeDivergenceMatch` comparison.

2. If `structuredMsg.ConversationID` is empty:
   - Follow the existing dual-write path (derive key from
     `authenticatedSender` → `DeriveConversationKey` →
     `ResolveOrCreateConversationByKey`).

**Composability with B5:** The pre-resolved path does NOT call
`authenticatedSender` because it does not derive a conversation key. The
ConversationID was already resolved by the CLI. However, the existing
`authenticatedSender` call at the same site (for the non-pre-resolved path)
must remain. The implementation must be structured as an if/else: pre-resolved
path OR auth-derived path — never both.

**Failure mode prevented:** Without the `conv-lookup-failed` guard, a
non-existent ConversationID would feed an empty ExternalRef into
`ComputeDivergenceMatch`, producing `"routing-type-mismatch: old=… new="` —
a false positive that pollutes divergence metrics and cannot be distinguished
from genuine routing disagreements.

#### 1.1.3 Phase 11: Surface/ExternalRef/ParentRef on MessageRequest

**Intent:** External platform adapters (Google Chat, Teams, Slack) need to
pass conversation identity through the SDK message path. The broker-inbound
path already has this (see File 3), but SDK-integrated adapters use the
`/api/v1/projects/{pid}/agents/{slug}/message` endpoint instead.

**Where:** Add three optional JSON fields to the `MessageRequest` struct:
`Surface string`, `ExternalRef string`, `ParentRef string`.

**Behavior:**

1. If `ExternalRef` is non-empty but `Surface` is empty, return 400
   (`"external_ref requires surface to be set"`). This is a guard against
   malformed requests that would create conversations without platform
   attribution.

2. If both `Surface` and `ExternalRef` are non-empty:
   - Call `UpsertConversationByExternalRef` with a `store.Conversation{
     ProjectID, Kind: "group", Surface, ExternalRef, ParentRef, DriftState: "active"}`.
   - Set `DefaultAgentID` to the target agent's ID if non-empty.
   - On success, attach `resolved.ID` to `structuredMsg.Metadata["conversation_id"]`.
   - On failure, log an error and continue (non-fatal).

3. This block must execute in `handleAgentMessage` AFTER validation and
   group[] detection (it only applies to single-dispatch messages), but
   BEFORE the agent wake handling.

**Composability:** This block is independent of `authenticatedSender` — it
does not derive a DM key. It creates an external-platform conversation
identified by the adapter's own ref, not by sender/recipient identity.

#### 1.1.4 AC-33: Cross-project mention validation

**Intent:** When a message includes `@mentions` of other agents, all mentioned
agents must belong to the same project as the primary recipient. A mention
crossing project boundaries would leak context between isolated tenants.

**Where:** In `handleAgentMessage`, after the mention cap enforcement and
before group[] detection.

**Behavior:**

1. If `len(req.Mentions) > 0`:
   - Look up the primary agent.
   - Build an addressee list containing the primary agent and all resolved
     mention slugs (looked up via `GetAgentBySlug` within the primary agent's
     project).
   - Call `ValidateCrossProjectAddressees(ctx, store, addressees)` — a new
     function in `pkg/messaging` that verifies all addressees share a single
     ProjectID.
   - If the check fails, return 400 with the validation error.

**Dependency:** Requires adding the `ValidateCrossProjectAddressees` function
and the `Addressee` type to `pkg/messaging`. These are new types, not on main.

#### 1.1.5 CheckConversationConsistency at all dual-write sites

**Intent:** Independent consistency check that compares a message's
conversation assignment against prior messages in the same thread/DM pair.
Catches cases where the same logical conversation maps to different
ConversationIDs over time (e.g., due to re-derivation after a key format
change).

**Where:** After every divergence log call, at all 5 dual-write sites in
this file:
1. `handleAgentOutboundMessage` (after divergence log)
2. `handleAgentMessage` single-dispatch (after divergence log)
3. `handleGroupMessage` agent fan-out (after divergence log)
4. `handleGroupMessage` user fan-out (after divergence log)
5. _Not in `processMentions`_ — mentions are ephemeral notification
   dispatches, not persisted conversation messages.

**Note:** `processMentions` already has a dual-write site on main. Whether to
add `CheckConversationConsistency` there is a judgment call — v2 did not add
it there. The other 4 sites listed above are required.

**Signature:** `messaging.CheckConversationConsistency(ctx, store, messageID,
conversationID, threadID, senderID, recipientID, logger)`.

**Dependency:** Requires adding the `CheckConversationConsistency` function
to `pkg/messaging`. This function queries recent messages with the same
threadID or sender/recipient pair and checks whether they have the same
ConversationID.

### 1.2 How it composes with security fixes

#### authenticatedSender (B5, #1343)

The file currently has 6 references to `authenticatedSender`:
- **Line 787:** `handleAgentMessage` DM resolution — uses auth for
  `ResolveOrCreateDMConversation`. MUST REMAIN.
- **Line 1054:** `handleGroupMessage` agent set — uses auth for DM
  resolution. MUST REMAIN.
- **Line 1177:** `handleGroupMessage` user set — uses auth for DM
  resolution. MUST REMAIN.
- **Line 1322:** `handleProjectBroadcast` — uses auth for self-skip.
  MUST REMAIN.
- **Lines 1635, 1643:** Function definition + doc comment. MUST REMAIN.

**Composition rule:** All new dual-write code must use `authenticatedSender`
for DM key derivation, never `structuredMsg.SenderID` or `structuredMsg.Sender`.
The DEF-11 pre-resolved path is the ONE exception — it skips key derivation
entirely because the ConversationID is already resolved.

**Gate:** After implementation, `grep -c authenticatedSender handlers_agent_messaging.go`
must return exactly 6. New code must not introduce additional references
(the existing sites already cover all key-derivation paths). If a new
dual-write site is added, it must route through an existing
`authenticatedSender` call site, not add a new one.

#### ActionAttach (#1347)

- **Line 1276:** `handleProjectBroadcast` — user callers need `ActionAttach`
  on the project to broadcast. MUST REMAIN.

**Gate:** After implementation, `grep -c ActionAttach handlers_agent_messaging.go`
must return exactly 1.

#### DM ownership (#1322)

The existing DM ownership checks in this file (via `authenticatedSender` at
the dual-write sites) are the B5-upgraded version of #1322's original payload-
based checks. No additional DM ownership logic is needed — the
`authenticatedSender` pattern subsumes it.

### 1.3 What it must NOT do

1. **Must NOT delete or weaken `authenticatedSender` calls.** V2 deleted all
   6 references because it pre-dated B5. Any implementation that reduces the
   count below 6 is a security regression.

2. **Must NOT use `structuredMsg.SenderID` or `structuredMsg.Sender` for key
   derivation.** These are client-controlled fields. Every DM key input must
   come from `authenticatedSender(ctx)`.

3. **Must NOT make validation failures non-fatal.** `ValidateLegacyMessage`
   rejections must return HTTP 400, not log-and-continue. The purpose of
   validation is to prevent orphaned conversation rows.

4. **Must NOT create conversations before validation.** The ordering is:
   validate → resolve/create conversation → dispatch → persist. Reversing
   validate and resolve creates orphaned rows on rejected messages.

5. **Must NOT remove the B10 non-fatal contract for derivation failures.**
   `DeriveConversationKey` errors log a warning and skip conversation
   resolution — they do not reject the request.

### 1.4 Acceptance criteria

| ID | Criterion | Verification |
|----|-----------|-------------|
| AC-C1 | `ValidateLegacyMessage` rejects unknown message types with 400 at all 3 sites | Unit test: POST with `Type: "not-a-real-type"` returns 400 (existing `TestOutboundMessage_UnknownTypeIsChargedAsAgentTraffic` should be updated to expect 400 instead of 200) |
| AC-C2 | Validation runs BEFORE conversation resolution | Unit test: POST with invalid message type; verify no new conversation row was created in the store |
| AC-C3 | Pre-resolved ConversationID stamps `storeMsg.ConversationID` directly | Unit test: POST with `ConversationID` set; verify the persisted message has that ConversationID |
| AC-C4 | Pre-resolved ConversationID lookup failure records fallback, not mismatch | Unit test: POST with non-existent ConversationID; verify `DivergenceMetrics.Fallbacks()` increments and `Mismatches()` does not |
| AC-C5 | Pre-resolved ConversationID with matching DM pair produces match, not mismatch | Unit test: create conversation with matching ExternalRef, POST with its ID; verify `DivergenceMetrics.Matches()` increments |
| AC-C6 | Pre-resolved ConversationID with wrong ExternalRef produces mismatch | Unit test: create conversation with non-matching ExternalRef, POST with its ID; verify `DivergenceMetrics.Mismatches()` increments |
| AC-C7 | Surface+ExternalRef on MessageRequest resolves or creates conversation | Unit test: POST with Surface+ExternalRef; verify conversation exists in store with correct fields |
| AC-C8 | ExternalRef without Surface returns 400 | Unit test: POST with ExternalRef but no Surface; verify 400 |
| AC-C9 | Cross-project mentions are rejected | Unit test: create agents in different projects, POST with cross-project mention; verify 400 |
| AC-C10 | `authenticatedSender` count remains 6 | `grep -c authenticatedSender pkg/hub/handlers_agent_messaging.go` = 6 |
| AC-C11 | `ActionAttach` count remains 1 in this file | `grep -c ActionAttach pkg/hub/handlers_agent_messaging.go` = 1 |
| AC-C12 | `CheckConversationConsistency` called at 4 dual-write sites | `grep -c CheckConversationConsistency pkg/hub/handlers_agent_messaging.go` ≥ 4 |

---

## File 2: handlers_agent_messaging_test.go

### 2.1 What it must do and why

This file must add regression tests that prove the new capabilities in
`handlers_agent_messaging.go` work correctly. The tests exercise the full
HTTP handler path (not unit-testing internal functions) to catch integration
issues.

#### 2.1.1 DEF-11 regression suite (3 core tests + 1 genuine-disagreement test)

**Intent:** Prove that pre-resolved ConversationID handling works end-to-end
through the HTTP handler. These tests are the acceptance gate for DEF-11.

**Test setup:** A shared setup function creates a project, agent, user, and
runtime broker. Sets a dispatcher so the handler returns 200 instead of 503.

**Test 1: PreResolvedConversation_PopulatesExternalRef**

Must prove: When the CLI sends a pre-resolved ConversationID, the handler
looks up the conversation's ExternalRef from the store and uses it for
divergence comparison. Specifically:

- Create a conversation with a known ExternalRef (DM format).
- POST a message with that ConversationID on the StructuredMessage.
- Assert that `DivergenceMetrics.Matches()` incremented (which can only
  happen if the ExternalRef was loaded from the store, not left as `""`).
- Assert that `DivergenceMetrics.Mismatches()` did NOT increment.
- Assert that the conversation's ExternalRef is unchanged in the store.

**Test 2: PreResolvedConversation_DivergenceMatch**

Must prove: A pre-resolved send with a matching DM conversation produces a
divergence match. Similar to Test 1 but focused on the match/mismatch delta.

- Create a conversation matching the sender/agent DM pair.
- POST a message with that ConversationID.
- Assert `Matches` delta ≥ 1, `Mismatches` delta = 0.

**Test 3: PreResolvedConversation_LookupFailure**

Must prove: When a pre-resolved ConversationID doesn't exist in the store,
the handler records a fallback with reason `"conv-lookup-failed"` — NOT a
routing-type-mismatch.

- POST a message with a non-existent ConversationID.
- Assert the handler returns 200 (message delivery is non-fatal).
- Assert `DivergenceMetrics.Fallbacks()` incremented.
- Assert `DivergenceMetrics.Mismatches()` did NOT increment.

**Test 4: PreResolvedConversation_GenuineDisagreement**

Must prove: The divergence comparison can detect a real mismatch when the
stored ExternalRef disagrees with old-model routing.

- Create a conversation with an ExternalRef that does NOT match the
  sender/agent pair used in the message.
- POST a message with that ConversationID.
- Assert `DivergenceMetrics.Mismatches()` incremented.

#### 2.1.2 DEF-19: Group recipient full handler path

**Intent:** Prove that a `group[]` recipient message survives the full HTTP
handler path (POST to `/api/v1/projects/{pid}/agents/{slug}/message` with
`Recipient: "group[agent:a,agent:b]"`).

Must prove:
- Returns 200, not 400.
- Response is a `GroupMessageResponse` with non-empty `GroupID`.
- `Delivered` count matches the number of agents.
- All results have `Status: "delivered"`.

#### 2.1.3 Updated unknown-type test

The existing `TestOutboundMessage_UnknownTypeIsChargedAsAgentTraffic` must be
updated: after `ValidateLegacyMessage` is added, unknown message types should
be rejected with 400 instead of accepted as 200. The test name should still
make sense — consider renaming it or updating its doc comment to reflect that
unknown types are now rejected by validation.

### 2.2 How it composes with security fixes

The test file does not directly test security fixtures, but the DEF-11 tests
implicitly exercise the `authenticatedSender` path for the non-pre-resolved
divergence comparison. The tests must use `doRequest` (which goes through the
full HTTP handler including auth middleware) and not bypass the handler.

### 2.3 What it must NOT do

1. **Must NOT test v2's internal API surface.** Tests should exercise the HTTP
   endpoint, not call `DeriveConversationKey` or `ResolveOrCreateConversationByKey`
   directly. Those have their own unit tests in `pkg/messaging`.

2. **Must NOT use the legacy DM external_ref format** (`dm:<id1>:<id2>`) as the
   expected format. If a helper constructs test external refs, it should use the
   canonical format that `DeriveConversationKey` / `DMConversationKey` produces
   on current main (`dm:<kind>:<uuid>:<kind>:<uuid>`).

3. **Must NOT hard-code conversation store internals.** Use
   `UpsertConversationByExternalRef` to create test conversations, not direct
   SQL inserts.

### 2.4 Acceptance criteria

| ID | Criterion | Verification |
|----|-----------|-------------|
| AC-T1 | DEF-11 Test 1 passes: pre-resolved conv populates ExternalRef | `go test -run PreResolvedConversation_PopulatesExternalRef` |
| AC-T2 | DEF-11 Test 2 passes: matching pair produces match | `go test -run PreResolvedConversation_DivergenceMatch` |
| AC-T3 | DEF-11 Test 3 passes: missing conv produces fallback, not mismatch | `go test -run PreResolvedConversation_LookupFailure` |
| AC-T4 | DEF-11 Test 4 passes: wrong ExternalRef produces mismatch | `go test -run PreResolvedConversation_GenuineDisagreement` |
| AC-T5 | DEF-19 test passes: group[] through full handler path returns 200 | `go test -run DEF19_GroupRecipient_FullHandlerPath` |
| AC-T6 | Unknown type test expects 400 | `go test -run UnknownType` returns 400 |
| AC-T7 | All tests pass with zero mismatch leakage | No test increments Mismatches unless testing a genuine disagreement |
| AC-T8 | ~350 lines of new test logic | Rough line count check on the test additions |

---

## File 3: handlers_broker_inbound.go

### 3.1 What it must do and why

This file handles `POST /api/v1/broker/inbound` — messages from external
platform adapters (Teams, Slack, Google Chat) that arrive via a runtime broker.
Tranche C adds three capabilities:

#### 3.1.1 Phase 11: Surface/ExternalRef/ParentRef on inboundMessageRequest

**Intent:** External adapters need to tell the hub which conversation a message
belongs to on the external platform. The broker already resolves the agent and
project; Phase 11 adds conversation resolution so every inbound message
carries a ConversationID.

**Where:** Add three optional JSON fields to the `inboundMessageRequest`
struct: `Surface string`, `ExternalRef string`, `ParentRef string`.

**Behavior:**

1. If `ExternalRef` is non-empty but `Surface` is empty, return 400. This
   is the same guard as in `handleAgentMessage` — a bare external ref without
   platform attribution is malformed.

2. If both `Surface` and `ExternalRef` are non-empty:
   - Call `UpsertConversationByExternalRef` with `store.Conversation{
     ProjectID: &agent.ProjectID, Kind: "group", Surface, ExternalRef,
     ParentRef, DriftState: "active"}`.
   - Set `DefaultAgentID` to `&agent.ID` if non-empty.
   - On success, attach `resolved.ID` to `req.Message.Metadata["conversation_id"]`
     (initialize the Metadata map if nil).
   - On failure, log an error and continue (non-fatal — the dispatch already
     succeeds without a conversation_id).

3. This block must execute AFTER the message urgency flag is set (the `!`
   prefix interrupt handling), but BEFORE the dispatcher availability check.

#### 3.1.2 ValidateLegacyMessage choke point

**Intent:** Messages from external adapters are the most likely to have
malformed envelopes (e.g., Teams adapter emitting `Channel: ""` with a
non-empty ThreadID). Validation must catch these before dispatch.

**Where:** After the urgency flag handling and before the Phase 11 resolution
block. Validation must run before conversation resolution (same ordering
constraint as File 1 — prevent orphaned conversation rows).

**Behavior:** If `ValidateLegacyMessage(req.Message)` returns an error,
return 400 with the validation error. Otherwise continue.

#### 3.1.3 CheckConversationConsistency at the dual-write site

**Intent:** Same as File 1 — independent consistency check at the existing
dual-write site (around line 271-283 on current main, where
`ResolveOrCreate{Thread,DM}Conversation` is called).

**Where:** After the existing divergence log call, before `CreateMessage`.

**Signature:** Same as File 1.

### 3.2 How it composes with security fixes

#### #1322 (DM ownership + SenderID caching)

The file currently has #1322's security additions:

- **SenderID caching (line 145):** After resolving the sender user, their ID
  is cached on `req.Message.SenderID` for downstream use. MUST REMAIN.
- **DM ownership check (lines 164-173):** When ThreadID starts with `dm:`,
  `parseDMKeyIDs` extracts the agent/user IDs from the key and verifies they
  match the resolved agent and sender. MUST REMAIN.

**Composition rule:** Phase 11 and #1322 are orthogonal. Phase 11 resolves
external-platform conversations (Surface-based). #1322 validates DM key
ownership for native conversations. They operate at different layers and do
not interfere.

**Ordering:** #1322's DM ownership check comes first (it may reject the
request with 400). Phase 11's resolution comes after (it creates/resolves
a conversation). This ordering is correct because:
- If the DM key is invalid, the request is rejected before any conversation
  row is created.
- Phase 11 conversations are `Kind: "group"` (external platform), not DMs,
  so #1322's DM check does not apply to them.

### 3.3 What it must NOT do

1. **Must NOT remove or weaken the SenderID caching** at line 145. This is
   #1322's early cache that downstream DM ownership and persistence reuse.

2. **Must NOT remove or weaken the DM ownership check** at lines 164-173.
   V2 removed it; re-derivation inherits it from main.

3. **Must NOT call `authenticatedSender`** in this file. The broker-inbound
   path uses HMAC authentication, not user/agent JWT auth. The sender identity
   comes from the broker's payload (validated by #1322's ownership check), not
   from `GetUserIdentityFromContext`. This is a fundamental difference from
   the agent messaging handlers.

4. **Must NOT make ValidateLegacyMessage rejection non-fatal.** Invalid
   inbound messages must be rejected with 400, not dispatched to agents.

### 3.4 Acceptance criteria

| ID | Criterion | Verification |
|----|-----------|-------------|
| AC-B1 | `inboundMessageRequest` has Surface, ExternalRef, ParentRef fields | Struct inspection |
| AC-B2 | ExternalRef without Surface returns 400 | Unit test |
| AC-B3 | Surface+ExternalRef resolves conversation and attaches ID to metadata | Unit test: POST with Surface+ExternalRef; verify conversation in store and metadata["conversation_id"] set |
| AC-B4 | ValidateLegacyMessage rejects malformed broker messages with 400 | Unit test: POST with empty Msg and no attachments; verify 400 |
| AC-B5 | ValidateLegacyMessage runs BEFORE conversation resolution | Unit test: POST with invalid message + Surface+ExternalRef; verify no conversation row created |
| AC-B6 | #1322 SenderID caching still present at line 145 | `grep -n "SenderID = senderUser.ID" handlers_broker_inbound.go` |
| AC-B7 | #1322 DM ownership check still present | `grep -n "parseDMKeyIDs" handlers_broker_inbound.go` |
| AC-B8 | CheckConversationConsistency called at dual-write site | `grep -c CheckConversationConsistency handlers_broker_inbound.go` ≥ 1 |
| AC-B9 | Conversation resolution failure is non-fatal | Unit test: mock UpsertConversationByExternalRef to fail; verify handler returns 200 (dispatch succeeded) |

---

## File 4: handlers_chat_v2.go

### 4.1 What it must do and why

This file handles the web chat UI endpoints: conversation history, message
sending (human-to-agent via `sendAgentRouted`, human-to-human via
`sendHumanToHuman`), topic management, and agent assignment. Tranche C adds
three capabilities:

#### 4.1.1 Phase 8: Read-switch in handleConversationHistory

**Intent:** The conversation history endpoint currently queries by
`Channel: "web", ThreadID: key`. Phase 8 adds a runtime-controlled switch
that queries by `ConversationID` instead, enabling conversation-first reads
once dual-write data is populated. A graceful fallback ensures pre-dual-write
data is still accessible.

**Where:** In `handleConversationHistory`, replace the static `MessageFilter`
construction with a conditional block.

**Behavior:**

1. Check the operational settings: `ops.ConversationReadSwitch()`. If OFF (or
   ops is nil), use the existing legacy filter (`Channel: "web", ThreadID: key`).

2. If ON:
   a. Determine whether the key represents a DM or thread conversation.
      The `isDM` flag is already computed upstream in the handler.
   b. For DMs: parse the key into its components (kind1, id1, kind2, id2) and
      call `messaging.ResolveDMConversationForRead(ctx, store, logger,
      kind1, id1, kind2, id2)`.
   c. For threads: look up the topic to get the projectID, then call
      `messaging.ResolveThreadConversationForRead(ctx, store, logger,
      key, topic.ProjectID)`.
   d. If resolution succeeds, use `MessageFilter{ConversationID: result.ConversationID}`.
   e. If resolution fails (no conversation found for this key), this means the
      data was written before dual-write was enabled. Fall back to the legacy
      filter AND increment `DivergenceMetrics.IncFallback()` to track how
      often this happens.

**Key parsing:** The DM key format is `dm:<kind>:<id>:<kind>:<id>`. Use
`strings.Split(key, ":")` and verify `len(parts) >= 5`. If the key doesn't
parse, fall back to legacy.

**Thread topic lookup:** Use the `WebChatStore` (already available as `wcs`
in the handler) to call `GetTopic(ctx, key)`. If the topic lookup fails,
fall back to legacy.

**Dependency:** Requires `ConversationReadSwitch()` to be available on the
operational settings interface. This is a new method — check whether it
already exists or needs to be added.

#### 4.1.2 ValidateLegacyMessage in sendAgentRouted

**Intent:** Messages sent through the web chat UI must also pass envelope
validation.

**Where:** In `sendAgentRouted`, after attachment handling and before the
`storeMsg` construction (persistence).

**Special case — attachment-only messages:** The web chat UI allows
attachment-only messages (empty `Msg` with non-empty `Attachments`).
`ValidateLegacyMessage` would reject `Msg == ""` as invalid. Before calling
validation, check: if `msg.Msg == ""` and `len(msg.Attachments) > 0`, set
`msg.Msg = "[attachment]"` as a synthetic body. This is a format workaround,
not a content decision — the actual message content is the attachment.

**Behavior:** If validation fails, return early (the function returns the
persisted message ID as a string; return `""` on validation failure after
writing the 400 error).

#### 4.1.3 No changes to security fixtures

This file's security fixtures (validateDefaultAgent, ActionAttach,
parseDMKeyIDs, isDMParticipant) are NOT modified by tranche C. They must
remain exactly as they are.

### 4.2 How it composes with security fixes

#### validateDefaultAgent (DEF-31, #1338)

The file currently has 6 references to `validateDefaultAgent`:
- **Line 450:** Doc comment referencing it as single source of truth.
- **Line 455:** Call in topic creation handler.
- **Line 594:** Doc comment in topic update handler.
- **Line 598:** Call in topic update handler.
- **Line 678:** Doc comment on function definition.
- **Line 685:** Function definition.

**Gate:** After implementation, `grep -c validateDefaultAgent handlers_chat_v2.go`
must return exactly 6.

#### ActionAttach (#1347)

The file currently has 3 references to `ActionAttach`:
- **Line 1120:** `s.authorize(w, r, agentResource(primaryAgent), ActionAttach)` —
  primary agent authorization in `sendAgentRouted`.
- **Line 1214:** `s.authzService.CheckAccess(ctx, user, agentResource(mentionAgent), ActionAttach)` —
  mention agent authorization in `sendAgentRouted`.
- **Line 1216:** `logAuthzDenial(r, user, agentResource(mentionAgent), ActionAttach, decision.Reason)` —
  denial logging for mention authorization.

**Gate:** After implementation, `grep -c ActionAttach handlers_chat_v2.go`
must return exactly 3.

#### parseDMKeyIDs / isDMParticipant (#1322)

These functions validate DM key ownership in the web chat path. They are not
modified by tranche C but the read-switch (4.1.1) uses the parsed key
components. The implementation must parse the key independently (using
`strings.Split`) rather than calling `parseDMKeyIDs` (which validates and
returns an error on format mismatch — a stricter contract than the read-switch
needs, where a parse failure should fall back to legacy, not fail the request).

### 4.3 What it must NOT do

1. **Must NOT delete `validateDefaultAgent` or reduce its reference count.**
   V2 deleted all 6 references. This is a clean deletion revert in v2 that
   tranche C must not reproduce.

2. **Must NOT delete `ActionAttach` authorization checks.** V2 deleted all
   3 references. Same as above.

3. **Must NOT delete `isDMParticipant` kind-label check or `parseDMKeyIDs`.**
   These are #1322's DM ownership guards. V2 omitted them.

4. **Must NOT make the read-switch fallback path a hard error.** When a
   conversation cannot be resolved for reads, fall back to legacy
   Channel+ThreadID query. Never return an empty result or 500 because the
   conversation model doesn't have a row yet.

5. **Must NOT remove the `[attachment]` synthetic body** once it is set. The
   workaround must persist through to the persisted message so that future
   reads see a non-empty Msg field.

### 4.4 Acceptance criteria

| ID | Criterion | Verification |
|----|-----------|-------------|
| AC-V1 | Read-switch OFF uses legacy Channel+ThreadID filter | Unit test: set ConversationReadSwitch to false; verify filter uses Channel+ThreadID |
| AC-V2 | Read-switch ON with existing conversation uses ConversationID filter | Unit test: create DM conversation, set switch ON; verify filter uses ConversationID |
| AC-V3 | Read-switch ON with missing conversation falls back to legacy + increments Fallback | Unit test: set switch ON with no conversation; verify legacy filter used and `DivergenceMetrics.Fallbacks()` incremented |
| AC-V4 | ValidateLegacyMessage in sendAgentRouted rejects invalid messages | Unit test: send message with empty Msg and no attachments; verify 400 |
| AC-V5 | Attachment-only messages get synthetic body and pass validation | Unit test: send message with empty Msg but with attachments; verify 200 and persisted Msg = "[attachment]" |
| AC-V6 | `validateDefaultAgent` count remains 6 | `grep -c validateDefaultAgent pkg/hub/handlers_chat_v2.go` = 6 |
| AC-V7 | `ActionAttach` count remains 3 | `grep -c ActionAttach pkg/hub/handlers_chat_v2.go` = 3 |
| AC-V8 | `parseDMKeyIDs` still present | `grep -c parseDMKeyIDs pkg/hub/handlers_chat_v2.go` ≥ 1 |
| AC-V9 | `isDMParticipant` still present | `grep -c isDMParticipant pkg/hub/handlers_chat_v2.go` ≥ 1 |
| AC-V10 | Read-switch thread path resolves via topic lookup | Unit test: create thread conversation + topic; verify ConversationID filter used |

---

## Cross-cutting dependencies

### New functions needed in pkg/messaging

| Function | Purpose | Notes |
|----------|---------|-------|
| `ValidateLegacyMessage(msg *messages.StructuredMessage) error` | Envelope validation choke point | Called at 5 sites (3 in File 1, 1 in File 3, 1 in File 4). Must validate: Type is known, Msg is non-empty (or Attachments present), Channel/ThreadID consistency. |
| `CheckConversationConsistency(ctx, store, messageID, convID, threadID, senderID, recipientID, logger)` | Post-write consistency audit | Called at 5 sites (4 in File 1, 1 in File 3). Queries recent messages with same routing key, checks ConversationID agreement. |
| `ValidateCrossProjectAddressees(ctx, store, []Addressee) error` | AC-33 cross-project mention guard | Called at 1 site (File 1). Verifies all addressees share a ProjectID. |
| `Addressee` struct | `{PrincipalKind, PrincipalID string}` | Used by ValidateCrossProjectAddressees. |

**Scope note:** These `pkg/messaging` additions are em6's territory per the
current ownership model. The developer implementing this spec should coordinate
with em6 or get architect approval before adding to `pkg/messaging`.

### New operational setting

| Setting | Purpose | Notes |
|---------|---------|-------|
| `ConversationReadSwitch() bool` | Controls read-switch in File 4 | Must be available on the operational settings interface. Check if it already exists. |

### Ordering constraints (summary)

All five `ValidateLegacyMessage` sites share the same ordering invariant:

```
validate → resolve/create conversation → dispatch → persist
```

This ordering prevents orphaned conversation rows from rejected messages.

---

## Marker count summary

| Marker | File | Required count | Purpose |
|--------|------|----------------|---------|
| `authenticatedSender` | handlers_agent_messaging.go | 6 | G-1 security — sender from auth context |
| `validateDefaultAgent` | handlers_chat_v2.go | 6 | DEF-31 — default agent validation |
| `ActionAttach` | handlers_agent_messaging.go | 1 | #1347 — broadcast authorization |
| `ActionAttach` | handlers_chat_v2.go | 3 | #1347 — agent attach authorization |
| `parseDMKeyIDs` | handlers_chat_v2.go | ≥ 1 | #1322 — DM key parsing |
| `isDMParticipant` | handlers_chat_v2.go | ≥ 1 | #1322 — DM participation check |

---

## Estimated scope

| File | New lines (approx) | Complexity |
|------|-------------------|------------|
| handlers_agent_messaging.go | ~80 | High (composability with 3 security PRs) |
| handlers_agent_messaging_test.go | ~350 | Medium (test setup + 5 test functions) |
| handlers_broker_inbound.go | ~70 | Medium (orthogonal to security fixes) |
| handlers_chat_v2.go | ~50 | Hard (read-switch is novel design) |
| pkg/messaging (new functions) | ~40 | Medium (validation + consistency) |
| **Total** | **~590** | |

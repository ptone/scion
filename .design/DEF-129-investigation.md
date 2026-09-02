# DEF-129 Investigation: DM Messages Written with Group conversation_id

**Status: DISSOLVED / NO DEFECT**

**Investigated by:** ca-msg-investigator
**Date:** 2026-09-02
**Commit examined:** `97c3462ab1d3ebea61e27321581a37dbdd3cd447`

---

## Headline

The 8 messages attributed to group conversations `6ef436bd` ("general") and `dbd35ed2` ("new-thread") are **correctly filed channel traffic**, not misfiled DMs. Users typed in webchat topics, agents replied in the same topics, and both directions were attributed to the topic's group conversation by the correct code path. There is no write-path defect, no fallback mechanism, and no information disclosure.

Confirmed by two independent methods:
1. **Source derivation** (this report): traced every write path; the only route to a topic-linked group conversation_id requires `thread_id` = topic UUID, which enters `DeriveConversationKey` case 2 (group).
2. **Instance measurement** (ca-msg-instance-investigator): all 8 rows carry the channel's topic UUID as `thread_id`, not a `dm:` key.

---

## 1. Complete Enumeration of Write Paths That Set conversation_id

Every path that stamps `conversation_id` on a message row, with file:line and the conversation kind it can produce.

### Path A: Agent outbound (agent → user)
**`pkg/hub/handlers_agent_messaging.go:283-319`**

```go
extRef, kind, projID, deriveErr := messaging.DeriveConversationKey(messaging.KeyInputs{
    ThreadID:      req.ThreadID,
    ProjectID:     agent.ProjectID,
    SenderKind:    "agent",
    SenderID:      agent.ID,
    RecipientKind: "user",
    RecipientID:   recipientID,
})
```
Then `ResolveOrCreateConversationByKey` at `:309`.
- **Can produce group**: Yes, when `req.ThreadID` is a topic UUID (non-dm:, non-empty → case 2).

### Path B: User/Agent → Agent (handleAgentMessage)
**`pkg/hub/handlers_agent_messaging.go:906-1070`**

Two sub-paths:
- **B1: Caller-supplied conversation_id** (`:927-1013`): If `structuredMsg.ConversationID != ""`, the hub looks up the conversation, runs DEF-49 authorization (direct → DM key check; group → project membership), and stamps it.
- **B2: Server-derived** (`:1029-1065`): Uses `DeriveConversationKey` + `ResolveOrCreateConversationByKey`, same logic as Path A.
- **Can produce group**: B1 — yes, if CLI resolves a group reference. B2 — yes, via topic UUID ThreadID.

### Path C: Message broker deliverToUser
**`pkg/hub/messagebroker.go:437-501`**

```go
if msg.ThreadID != "" {
    convResult, convErr = messaging.ResolveOrCreateThreadConversation(...)
} else if msg.SenderID != "" && msg.RecipientID != "" {
    convResult, convErr = messaging.ResolveOrCreateDMConversation(...)
}
```
- **Can produce group**: Yes, when `msg.ThreadID` is a topic UUID.

### Path D: Message broker deliverToAgent
**`pkg/hub/messagebroker.go:646-701`**

Same branching as Path C.
- **Can produce group**: Yes, when `msg.ThreadID` is a topic UUID.

### Path E: Webchat v2 sendAgentRouted (user → agent via webchat)
**`pkg/hub/handlers_chat_v2.go:1147-1212`**

`key` from URL → used as ThreadID → `ResolveOrCreateThreadConversation` or `ResolveOrCreateDMConversation`.
- **Can produce group**: Yes, when `key` is a topic UUID (user sent message in a topic).
- **This is the entry point for the observed traffic.**

### Path F: Webchat v2 sendHumanToHuman
**`pkg/hub/handlers_chat_v2.go:1440-1501`**

Same logic as Path E.
- **Can produce group**: Yes, same conditions.

### Path G: Group-set agent recipients
**`pkg/hub/handlers_agent_messaging.go:1374-1388`**

Uses `ResolveOrCreateDMConversation` exclusively → always produces **direct**.

### Path H: Group-set user recipients
**`pkg/hub/handlers_agent_messaging.go:1534-1548`**

Uses `ResolveOrCreateDMConversation` exclusively → always produces **direct**.

### Path I: Notification inbox messages
**`pkg/hub/notifications.go:525-537`**

Uses `ResolveOrCreateDMConversation` exclusively → always produces **direct**.

### Path J: Broker inbound Phase 11 (external channel)
**`pkg/hub/handlers_broker_inbound.go:259-260`**

```go
convResult, convErr := messaging.ResolveOrCreateConversationByKey(
    r.Context(), s.store, log, req.ExternalRef, "group", &agent.ProjectID, keyOpts...)
```
Hardcodes `kind="group"`. Only active when broker plugin provides both `Surface` and `ExternalRef`. **No current broker plugin triggers this path.** Latent bug: if a future broker sends a `dm:` key as `ExternalRef`, it would be misclassified as group.

### Path K: Broker inbound Phase 5
**`pkg/hub/handlers_broker_inbound.go:377-421`**

Same branching as Path C.

---

## 2. The Legitimate Route (What Happened)

When a user sends a message in a webchat topic (e.g., "general"):

1. Web UI sends POST to `/api/v1/chat/conversations/{topicUUID}/messages`
2. `handleConversationMessages` sets `key = topicUUID`
3. `DeriveConversationKey` receives `ThreadID = topicUUID` (no `dm:` prefix) → **case 2** → produces `thread:<projectID>:<topicUUID>`, `kind="group"` (`derive_key.go:95-101`)
4. `ResolveOrCreateConversationByKey` runs the **topic lookup intercept** (`derive_key.go:170-198`): extracts `topicUUID` from the `thread:` ref, calls `GetTopicConversationIDIncludingDeleted(ctx, topicUUID)`, finds the topic's linked conversation_id
5. Returns the linked conversation — the row created by `CreateTopic`/`EnsureGeneralTopic` with `kind=group`, `surface=native`, `external_ref=''`, `display_name="general"`
6. Message is persisted with this conversation_id

When the agent replies, it uses the same `ThreadID` (the topic UUID) received in the incoming message. The reply goes through Path A or Path D, hitting the same `DeriveConversationKey` case 2 → same topic lookup → same group conversation_id.

Both messages have sender/recipient = one user + one agent. They **look** like DM pairs but are correctly filed channel traffic.

---

## 3. Why No Fallback Can Exist

The hypothesis that "a conversation resolution failure falls back to a project-default or general conversation" is **structurally impossible** in the current code.

**Evidence:**

`UpsertConversationByExternalRef` (`pkg/store/entadapter/conversation_store.go:389-391`):
```go
if conv.ExternalRef == "" {
    return nil, fmt.Errorf("externalRef is required for upsert: %w", store.ErrInvalidInput)
}
```

Every conversation resolution path flows through this function. The "general" and "new-thread" conversations have `external_ref = ''`. Therefore no derivation path can match or return them — the upsert rejects empty refs with a hard error before the query runs.

`DeriveConversationKey` has three cases (`derive_key.go:67-108`), none of which fall back:
- **Case 1** (`dm:` prefix, `:69`): Returns `kind="direct"`. Parse errors are fatal with explicit comment: *"DO NOT fall through to case 2 — falling through is exactly how DEF-15 produces its defective row."*
- **Case 2** (non-dm ThreadID, `:95`): Returns `kind="group"` with non-empty `thread:<proj>:<id>` ref.
- **Case 3** (empty ThreadID, `:103`): Derives from principal pair, returns `kind="direct"`.

`LogDivergence` instrumentation logged **0 fallbacks** in 24 hours — consistent with no fallback code existing to fire.

---

## 4. How a DM Send Could Bypass DeriveConversationKey Case 1

It cannot, in the current code. If `ThreadID` starts with `dm:`, case 1 fires unconditionally (`derive_key.go:69`). The only bypass would be:
- A message with a non-`dm:` ThreadID that happens to match a topic UUID — but this is the **correct** path, not a bypass; it means the message was sent through a topic.
- A message with empty ThreadID — goes to case 3, produces `kind="direct"`, never reaches a group conversation.
- The caller-supplied conversation_id path (Path B1) — but DEF-49 authorization would require the caller to be in the same project as the group conversation, which is correct access control.

The two affected agents (`c9c1123b`, `7ad8aadc`) have both correct DM filings and correct channel filings in the same window because users interacted with them through both channels (DMs and topics).

---

## 5. ValidateAttributed Is the Only Validation on the Write Path

**Confirmed.** `ValidateAttributed` (`pkg/messaging/validate.go:84-88`) is a non-empty check:

```go
func ValidateAttributed(conversationID string) error {
    if conversationID == "" {
        return fmt.Errorf("conversation_id is required after attribution")
    }
    return nil
}
```

It does **not** cross-check:
- Whether the conversation kind matches the message type
- Whether the conversation's participants include the sender/recipient
- Whether the conversation belongs to the correct project

The DEF-49 authorization check (`handlers_agent_messaging.go:971-1009`) is the only cross-check, and it only runs on the **caller-supplied conversation_id** path (Path B1). On derivation paths, correctness is ensured by construction: `DeriveConversationKey` determines the kind, and `UpsertConversationByExternalRef` matches on the deterministic key.

Nothing else could have caught the "misfiling" because no misfiling occurred. The conversation kind and the message routing are consistent: messages sent through a topic get the topic's group conversation.

---

## 6. Empty external_ref: By Design, Not a Defect

The empty `external_ref` on the affected group conversations is **intentional**.

`CreateTopic` (`pkg/hub/webchannel_store_postgres.go:371-373`):
```sql
INSERT INTO conversations (id, project_id, kind, surface, external_ref, parent_ref, display_name, drift_state, ...)
VALUES ($1, $2, 'group', 'native', '', '', $3, 'active', ...)
```

`EnsureGeneralTopic` (`pkg/hub/webchannel_store_postgres.go:579-581`):
```sql
INSERT INTO conversations (id, project_id, kind, surface, external_ref, parent_ref, display_name, drift_state, ...)
VALUES ($1, $2, 'group', 'native', '', '', 'general', 'active', ...)
```

The comment at `:321` explains: *"The external_ref derivation (empty string) matches backfillTopicConversations."*

Native webchat topic conversations are linked via `webchat_topic.conversation_id` (a FK column), not via `external_ref`. Resolution uses the **topic lookup intercept** in `ResolveOrCreateConversationByKey` (`:170-198`), which queries `GetTopicConversationIDIncludingDeleted` by topic UUID and returns the linked conversation_id directly.

The `CreateConversation` validator (`conversation_store.go:122-123`) enforces non-empty `external_ref` only for `kind=direct`:
```go
if conv.Kind == "direct" && conv.ExternalRef == "" {
    return fmt.Errorf("direct conversation requires a non-empty external_ref ...")
}
```

For `kind=group`, empty `external_ref` is explicitly allowed. The 38 empty "general" shells batch-created 2026-08-31 09:05:42 are from `EnsureGeneralTopic` being called for each project during boot — expected seeding behavior.

The DEF-29 gap (empty `external_ref` on `kind=direct`) is a separate defect in a different kind. The group-kind gap is not a gap — it is the design.

---

## Residual: CheckConversationConsistency False Positives

The `CheckConversationConsistency` WARNs that triggered this investigation are **false positives**. The check queries by sender/recipient pair and warns whenever the same user-agent pair appears in different conversations. When a user both DMs an agent and messages the agent in a topic, both attributions are correct but the check fires. This is a consistency-check over-breadth issue, not a write-path defect. Filed separately by ca-msg-arch.

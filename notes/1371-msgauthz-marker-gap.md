# GoogleCloudPlatform/scion#1371 — Message Authorization Marker Gap Analysis

**Author:** sn-msgauthz-inv | **Date:** 2026-08-29 | **Status:** Complete

## Summary

GoogleCloudPlatform/scion#1371 replaced `ActionAttach`/`CheckAccess` with `authorizeAgentMessage` in three
message handlers. **Authorization coverage is equivalent or better in all three functions — there is no
gap.** The gate markers are stale: they still look for `ActionAttach`/`CheckAccess`, which were replaced
by `authorizeAgentMessage`, a more granular authorization choke point.

There is, however, a **real defect in denial logging** on the `sendAgentRouted` mention fan-out path.
The mention denial reason is discarded and the `logAuthzDenial` contract (stable field names for
operator alerting) is broken. This is NOT a coverage gap — authorization runs — but it IS a
logging contract violation that the gate's AUDIT category exists to catch.

---

## Verdicts

| Function | File | Verdict | Detail |
|---|---|---|---|
| `handleBrokerInbound` | `handlers_broker_inbound.go` | **COVERED** | Agent-sender skip pre-existed |
| `sendAgentRouted` | `handlers_chat_v2.go` | **COVERED** (authz) / **DEGRADED** (logging) | Authz replaced; logging contract broken |
| `handleProjectBroadcast` | `handlers_agent_messaging.go` | **COVERED** | Authz replaced with more granular per-agent checks |

---

## 1. handleBrokerInbound — COVERED

### The question

The new code has a comment: `// else: system-plane (D8) or agent-sender — no authorizeAgentMessage check.`
Did the old `ActionAttach`/`CheckAccess` calls cover the agent-sender case that the new code explicitly skips?

### Answer: No. The old code also skipped agent-senders.

**Old code (f04a5a80c), lines 123-125 (comment) and 126 (branch):**
```go
// Enforce ActionAttach permission for user-identity senders. Agent-identity
// and system senders (scheduled events, internal) skip this check — they
// use broker HMAC trust which is infrastructure-level authorization.
if strings.HasPrefix(req.Message.Sender, "user:") {
```

**New code (8b09c118f), lines 123-131 (comment) and 132 (branch):**
```go
// Phase 3 msg-authz: Enforce authorizeAgentMessage for user-identity senders.
// ...
// Agent-to-agent via broker: documented as a known gap for follow-up ...
if strings.HasPrefix(req.Message.Sender, "user:") {
```

The branch structure is identical: `if strings.HasPrefix(req.Message.Sender, "user:")`. Both old and new code
only check authorization for user-identity senders. Agent-identity senders were explicitly exempt in the old
code's own comment. The new code makes the exemption more visible — it did not create it.

**Diff evidence:** The `git diff f04a5a80c 8b09c118f -- pkg/hub/handlers_broker_inbound.go` shows exactly
one change block in the authorization section: `s/CheckAccess(…ActionAttach)/authorizeAgentMessage()/ `
plus updated comments and error messages. No structural change to the `if` branch.

**Other B5 markers are intact:**
- `SenderID`: 6 occurrences (gate expects 4) — `handlers_broker_inbound.go:150,174,176,247,249,259`
- `NewAuthenticatedUser`: 1 occurrence — `handlers_broker_inbound.go:152`
- `parseDMKeyIDs`: 1 occurrence — `handlers_broker_inbound.go:173`

---

## 2. sendAgentRouted — COVERED (authorization) / DEGRADED (denial logging)

### Authorization coverage

`sendAgentRouted` takes `user UserIdentity` as a parameter — it is user-only by definition.
All senders reach the authorization check.

**Old code (f04a5a80c):**
- Line 1118: `s.authorize(w, r, agentResource(primaryAgent), ActionAttach)` — primary agent check
- Line 1210: `s.authzService.CheckAccess(ctx, user, agentResource(mentionAgent), ActionAttach)` — mention check
- Line 1212: `logAuthzDenial(r, user, agentResource(mentionAgent), ActionAttach, decision.Reason)` — AUDIT

Gate count: `ActionAttach` x3 (2 REQUIRED + 1 AUDIT).

**New code (8b09c118f):**
- Line 1119: `s.authorizeAgentMessage(ctx, user, primaryAgent, false)` — primary agent check
- Line 1218: `s.authorizeAgentMessage(ctx, user, mentionAgent, false)` — mention check

Both paths call `authorizeAgentMessage`, which evaluates D1-D10 decision logic including mode checks,
ancestry, project ownership, and permission checks. This is strictly MORE granular than the old
blanket `ActionAttach` check.

**VERDICT (authorization): COVERED.** The authorization check runs for the same population of senders
and evaluates a superset of the old policy.

### Denial logging — DEFECT

The `logAuthzDenial` contract (`authorize.go:36-56`) specifies stable field names for operator alerting:
`principal_type`, `principal_id`, `resource_type`, `resource_id`, `action`, `reason`, `path`.
Its docstring says: *"The field names are part of the contract — operators and alerting key off them —
so keep them stable."*

**Primary agent denial (new, `handlers_chat_v2.go:1122-1126`):**
```go
slog.Warn("chat v2 message authorization denied",
    "user", user.ID(),
    "target_agent", primaryAgent.ID,
    "reason", reason,
)
```
- Message changed: `"authorization denied"` -> `"chat v2 message authorization denied"`
- Field names changed: `user` (not `principal_id`), `target_agent` (not `resource_id`)
- Missing fields: `principal_type`, `resource_type`, `action`, `path`
- **Has `reason`: yes**

**Mention fan-out denial (new, `handlers_chat_v2.go:1219-1220`):**
```go
mentionAllowed, _ := s.authorizeAgentMessage(ctx, user, mentionAgent, false)
// ...
s.messageLog.Warn("User lacks message authorization for mentioned agent",
    "user", user.ID(), "agent", mentionAgent.Slug)
```
- **Reason DISCARDED** — assigned to `_`
- Logger changed: `s.messageLog` (not `slog`)
- Different message string
- Missing ALL `logAuthzDenial` contract fields including `reason`

**The gate's AUDIT category exists precisely for this.** The gate's own text says:
*"This is a silent-denial path — `logAuthzDenial` is the ONLY record of the denial."*
The mention fan-out is a silent-denial path by design (no 403; the denied mention is simply skipped).

The denial IS logged (via `s.messageLog.Warn`), so it is not fully silent. But:
1. The `logAuthzDenial` contract fields are gone — alerting keyed on `"authorization denied"` + `principal_type` will not fire.
2. The denial reason is discarded — an operator cannot determine WHY the denial happened.
3. The log destination changed (`s.messageLog` vs `slog`) — depending on log routing, the record may not reach the same sink.

**VERDICT (logging): DEGRADED.** Denials are still logged but the `logAuthzDenial` contract is
broken. The mention fan-out path specifically loses the denial reason. This is the gate's AUDIT
finding — not a coverage gap but a loss of operator evidence.

---

## 3. handleProjectBroadcast — COVERED

**Old code (f04a5a80c):**
- Agent callers: `ScopeAgentLifecycle` required + same-project check (`handlers_agent_messaging.go:1255-1261`)
- User callers: `s.authorize(w, r, projectResource(project), ActionAttach)` (`handlers_agent_messaging.go:1276`)
- No per-agent authorization — all running agents in the project receive the broadcast
- `authenticatedSender` used for self-skip

**New code (8b09c118f):**
- Agent callers: same-project check only; `ScopeAgentLifecycle` removed (`handlers_agent_messaging.go:1266-1271`)
  - Comment: "ScopeAgentLifecycle no longer required — messaging is a first-class axis (D1)."
- User callers: `s.authorize(w, r, projectResource(project), ActionRead)` (`handlers_agent_messaging.go:1289`)
  - Project-level gate weakened from `ActionAttach` to `ActionRead` as a fast-fail
- Per-agent pre-filter added: each recipient checked via `authorizeAgentMessage` (`handlers_agent_messaging.go:1383-1390`)
- `authenticatedSender` still present at `handlers_agent_messaging.go:1338`

The project-level gate is weaker (`ActionRead` < `ActionAttach`), but per-agent checks are NEW and
more granular. A user who passes `ActionRead` on the project but lacks messaging authorization on
specific agents will be filtered at the per-agent step. The net authorization is MORE precise:
instead of "can you attach to ANY agent in this project?" it's now "can you read this project AND
can you message EACH specific agent?"

**Gate count discrepancy:** The brief's file-level grep shows `ActionAttach` 1 -> 1, but the
surviving instance at `handlers_agent_messaging.go:1280` is in a **comment**:
```go
// authorizeAgentMessage. The project-level ActionAttach check is replaced
```
The gate uses go/ast for exact identifier matching, which correctly excludes comments. So the
gate's function-level count of 0 is correct even though the string appears once in the file.

**Per-agent denial logging:** The new broadcast pre-filter does not log individual agent denials:
```go
allowed, _ := s.authorizeAgentMessage(ctx, senderIdentity, a, false)
```
Reason is discarded. Unauthorized agents are silently excluded. However, there was NO per-agent
authorization in the old code — the entire broadcast was allow-or-deny at the project level. The
silent per-agent filtering is new functionality with no old analog, not removed old functionality.

**VERDICT: COVERED.** `authenticatedSender` is intact. Authorization is present — the model changed
from blanket project-level to read + per-agent, which is more granular.

---

## Gate Recommendation

**Re-point the markers at `authorizeAgentMessage`.** The authorization choke point changed from
`ActionAttach`/`CheckAccess` to `authorizeAgentMessage`. The code does not need fixing to satisfy
the gate's coverage intent — authorization is present in all three functions.

Specific marker updates needed:

| File | Function | Old marker | New marker | Count |
|---|---|---|---|---|
| `handlers_broker_inbound.go` | `handleBrokerInbound` | `ActionAttach` x1 | `authorizeAgentMessage` x1 | REQUIRED |
| `handlers_broker_inbound.go` | `handleBrokerInbound` | `CheckAccess` x1 | (drop — subsumed by `authorizeAgentMessage`) | — |
| `handlers_chat_v2.go` | `sendAgentRouted` | `ActionAttach` x2 (REQUIRED) | `authorizeAgentMessage` x2 | REQUIRED |
| `handlers_chat_v2.go` | `sendAgentRouted` | `ActionAttach` x1 (AUDIT) | see below | AUDIT |
| `handlers_agent_messaging.go` | `handleProjectBroadcast` | `ActionAttach` x1 | `authorizeAgentMessage` x1 | REQUIRED |
| COMPOSITE | `handleProjectBroadcast` | `authenticatedSender` + `ActionAttach` | `authenticatedSender` + `authorizeAgentMessage` | COMPOSITE |

**The AUDIT marker for `sendAgentRouted` needs a decision, not just a re-point.** The old AUDIT
tracked `logAuthzDenial` in the mention fan-out. The new code logs via `s.messageLog.Warn` but:
1. Uses different field names (breaks the `logAuthzDenial` contract)
2. Discards the denial reason

Options:
- **(A)** Add a `logAuthzDenial` call (or equivalent with the contract field names) in the new
  mention fan-out denial path. Re-point AUDIT at that.
- **(B)** Accept the `s.messageLog.Warn` as sufficient and re-point AUDIT at `messageLog.Warn`.
  This loses the stable field names and the denial reason — a conscious downgrade.

I recommend **(A)**: the `logAuthzDenial` contract was written to solve an operability problem
(#591 bypass was silent), and the comment explicitly says "operators and alerting key off" the
field names. Breaking that contract silently is this tier's signature defect.

---

## Scope Recommendation

**Tier: Simple update (S).** The code changes needed are:
1. Update gate markers to reference `authorizeAgentMessage`
2. Add denial logging with `logAuthzDenial`-compatible fields on the `sendAgentRouted` mention path
3. Stop discarding the denial reason on the mention fan-out (`_` -> named variable)

No architectural design needed. The authorization model is sound — only the gate markers and one
logging call need adjustment.

---

## What in the brief is wrong

1. **File-level count for `handlers_agent_messaging.go` is misleading.** The brief says
   `ActionAttach` 1 -> 1, which is correct for `git grep` (string match). But the surviving
   instance is in a **comment** (`handlers_agent_messaging.go:1280`), not code. The go/ast gate
   correctly counts it as 0. The brief's count, while technically accurate, suggests the marker
   survived when it did not.

---

## Open Questions

None. All three functions have clear verdicts.

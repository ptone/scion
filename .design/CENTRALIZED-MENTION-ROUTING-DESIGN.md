# Centralized Mention Routing Design

**Author:** ca-msg-arch
**Date:** 2026-09-12
**Status:** PROPOSED
**Builds on:** ADDITIVE-MENTION-ROUTING-DESIGN.md, LEADING-MENTION-OVERRIDE-DESIGN.md
**Branch:** `scion/ca-msg-arch`

---

## Problem & Goals

Three integrations (Discord, Slack, Telegram) need the same mention-routing
model that native chat (`handlers_chat_v2.go`) already implements:
additive non-leading mentions, leading-mention override, per-recipient
`type`/`to` fields, proper `CoAddressees`.

Each integration currently does its own routing (or none):

| Integration | Mention extraction | Multi-agent dispatch | Additive model | Leading override | `to` field |
|-------------|-------------------|---------------------|----------------|-----------------|------------|
| Native chat | `ExtractMentions` in hub | Yes, via `sendAgentRouted` | Yes (d402351) | Yes (d402351) | Yes |
| Discord | Own `extractAgentMentions` in plugin | Yes, per-target HTTP calls | No | No | No |
| Telegram | Own `classifyMentions` in plugin | Yes, per-target HTTP calls | Accidental partial | Accidental | No |
| Slack | None | No (single-agent only) | No | No | No |

ptone's decision: **centralize mention detection in the hub's
broker-inbound handler** so all integrations get the routing model from
one place.

### Success Criteria

1. A single hub endpoint handles mention routing for all broker-sourced
   messages: extract mentions, resolve agents, apply additive/leading
   model, fan out to multiple agents with correct per-recipient types.
2. Plugins send one HTTP call per user message (not N calls for N targets).
   The plugin supplies the message text and thread context; the hub does
   the routing.
3. The existing `POST /api/v1/broker/inbound` endpoint continues to work
   unchanged for backward compatibility (plugins can migrate incrementally).
4. All four scenarios (A/B/C/D) produce identical envelope semantics
   across native chat and all broker-sourced integrations.

---

## Non-Goals

- **Plugin-side routing removal.** This design does NOT require removing
  per-plugin routing code in the same phase. Plugins can migrate to the
  centralized endpoint at their own pace; the old endpoint stays.
- **Conversation resolution for broker messages.** The broker-inbound
  path currently has no ConversationResult. Adding it is orthogonal and
  should not block this work. The centralized endpoint produces
  `DeliveryText` without conversation metadata initially (same as today's
  broker-inbound behavior), and gains it when/if conversation resolution
  is added for broker messages.
- **Platform-native mention formats.** Discord `<@USER_ID>` and Slack
  `<@USER_ID>` are platform-specific user mentions. The hub-side
  routing uses `messages.ExtractMentions` which operates on plain
  `@name` text — it handles the agent mention format used across all
  platforms. Platform-specific mention stripping (e.g., removing bot
  self-mentions) stays in the plugin.
- **Agent cache management.** Discord/Telegram plugins have their own
  agent caches. The hub queries the store directly, so cache staleness
  is eliminated for hub-routed messages.

---

## Proposed Design

### 1. New Endpoint: `POST /api/v1/broker/inbound/routed`

A new endpoint alongside the existing `/api/v1/broker/inbound`. Same
authentication (broker HMAC). Different request shape:

```go
// routedInboundRequest is the JSON body for the hub-routed inbound endpoint.
type routedInboundRequest struct {
    ProjectID    string                     `json:"project_id"`
    DefaultAgent string                     `json:"default_agent"`  // channel's configured default agent slug
    IsDM         bool                       `json:"is_dm"`
    Message      *messages.StructuredMessage `json:"message"`
}
```

**Key differences from the existing endpoint:**

| Field | Old endpoint | New endpoint |
|-------|-------------|-------------|
| Target agent | Encoded in `topic` string | Hub determines from mentions + `default_agent` |
| Routing | Plugin's responsibility | Hub's responsibility |
| Fan-out | Plugin makes N calls | Hub does internally |
| `project_id` | Parsed from topic | Explicit field |
| `default_agent` | N/A (plugin resolved) | Plugin provides channel config |

The plugin sends:
- `project_id` — the project this channel is linked to
- `default_agent` — the channel's configured default agent slug (from
  its channel link). May be empty (no default configured).
- `is_dm` — whether this is a DM channel (affects routing semantics)
- `message` — the StructuredMessage with at minimum: `sender`,
  `sender_id`, `msg` (the text), `channel`, `thread_id`

The hub does:
1. Validate the request and authenticate the broker
2. `ExtractMentions(message.Msg)` → mentionNames
3. Resolve mentionNames against project agents via `s.store.ListAgents`
4. Resolve the default agent via `s.store.GetAgentBySlug`
5. Apply the additive/leading-mention routing model (shared with native chat)
6. Dispatch to all recipients with correct per-recipient types
7. Persist messages and publish SSE events
8. Return routing results

Response:

```json
{
  "delivered": true,
  "primary_agent": "agent-slug",
  "mention_results": [...],
  "dispatched": [
    {"agent_slug": "agent-a", "type": "message"},
    {"agent_slug": "agent-b", "type": "mention"}
  ]
}
```

### 2. Extract Shared Routing Logic

The mention routing logic currently in `handlers_chat_v2.go` (lines
920-1014) and `sendAgentRouted` (lines 1076-1461) is tightly coupled to
the native-chat HTTP handler (`w http.ResponseWriter`, `UserIdentity`,
`AttachmentRef`, etc.). To share it with the broker-inbound path, extract
the core logic into reusable functions.

**2a. `resolveRoutingAgents` — mention extraction + routing decision**

```go
// resolveRoutingAgents extracts mentions from content, resolves them
// against the project's agents, and applies the additive/leading-mention
// routing model. Returns the ordered agents list (agents[0] is primary)
// and mention results.
//
// If no mentions are found and defaultAgent is non-nil, returns
// [defaultAgent] (single-recipient, no mentions).
//
// If no mentions are found and defaultAgent is nil, returns nil
// (no agent to route to).
//
// (pseudocode — illustrative)
func resolveRoutingAgents(
    ctx context.Context,
    store store.Store,
    content string,
    projectID string,
    defaultAgent *store.Agent,  // may be nil
    isDM bool,
) (agents []*store.Agent, mentionNames []string, mentionResults []messages.MentionResult)
```

This encapsulates:
- `ExtractMentions(content)` → mentionNames
- `ResolveMentions(mentionNames, agentInfos, "")` → mentionResults
- Building `mentionedAgents` from results
- Leading-mention detection (`IsLeadingMention`)
- Additive/override routing decision (prepend default or not)

The function does NOT resolve the default agent — the caller passes it
in (already resolved from topic/channel-link/DM-key). This avoids
duplicating the default-agent resolution logic, which differs between
native chat (WebChatTopic lookup) and broker inbound (caller-provided).

**2b. `dispatchAgentGroup` — multi-agent dispatch**

```go
// dispatchAgentGroup dispatches a message to an ordered list of agents.
// agents[0] is the primary (TypeInstruction, IsMention=false).
// agents[1:] are secondaries (TypeMention, IsMention=true).
// All recipients get CoAddressees when len(agents) > 1.
//
// (pseudocode — illustrative)
func (s *Server) dispatchAgentGroup(
    ctx context.Context,
    agents []*store.Agent,
    content string,
    sender string,
    senderID string,
    channel string,
    threadID string,
    projectID string,
    mentionNames []string,
    mentionResults []messages.MentionResult,
    now time.Time,
) (primaryMsgID string, err error)
```

This encapsulates the core dispatch loop from `sendAgentRouted`:
- Build StructuredMessage for primary (TypeInstruction)
- Persist primary message
- Render DeliveryText with CoAddressees (if writeDenyEnabled)
- Dispatch primary
- Fan-out loop for secondaries (TypeMention, NewMention, CoAddressees)
- Persist and dispatch each secondary

What it does NOT do:
- Write HTTP responses (caller's responsibility)
- Handle attachments (broker-inbound doesn't have them currently)
- Resolve conversations (orthogonal; can be added later)
- Fire human mention notifications (native-chat-specific)

Both `handleConversationSend` (native chat) and `handleBrokerInboundRouted`
(new endpoint) call these extracted functions. The native-chat path adds
its HTTP-specific wrapper (response writing, attachment handling,
conversation resolution, human mention notifications).

### 3. Plugin Migration Contract

Each plugin migrates from:
```
Plugin → (resolve targets) → N × POST /api/v1/broker/inbound → Hub
```
To:
```
Plugin → POST /api/v1/broker/inbound/routed → Hub → (resolve + route + fan-out)
```

**What the plugin still does:**
- Receives the user message from the platform
- Determines the project_id and default_agent from its channel link config
- Strips platform-specific noise (bot self-mentions, formatting)
- Builds the StructuredMessage with sender, channel, thread_id
- Sends ONE HTTP call to the hub

**What the plugin no longer does:**
- Extract mentions from message text
- Resolve mentions against an agent cache
- Classify mention positions (start vs. body)
- Determine routing (which agents get dispatched)
- Build per-agent StructuredMessages with different types
- Make multiple HTTP calls for multi-agent scenarios

**Plugin-side code removal (per plugin):**
- Discord: `extractAgentMentions`, `classifyMentions`, `resolveTargetAgents`,
  multi-target dispatch loop (~200 lines). `getProjectAgents` cache can be
  removed (hub queries store directly).
- Telegram: `classifyMentions`, `resolveTargetAgents`, multi-target dispatch,
  TypeGroupSet path (~150 lines). Agent cache can be removed.
- Slack: No mention code to remove (it never had any). Add the single
  API call to the new endpoint, replacing the current single-agent call.

### 4. Backward Compatibility

The existing `POST /api/v1/broker/inbound` endpoint is unchanged. Plugins
that haven't migrated continue to work exactly as before. The new
`/api/v1/broker/inbound/routed` endpoint is additive.

A plugin can mix both endpoints during migration: use the routed endpoint
for channels where mention routing matters, and the old endpoint for
edge cases or fallback.

**Load-bearing decision:** Two endpoints, not a mode flag on one endpoint.
The request shapes are structurally different (topic+message vs.
project_id+default_agent+message), and trying to detect "which mode"
from a single request shape adds ambiguity. Separate endpoints have
clear contracts. **Easily reversible** — if we later want to unify, the
routed endpoint can absorb the old one.

### 5. `DeliveryText` Rendering

The centralized endpoint renders `DeliveryText` for each recipient
using `messaging.RenderDeliveryText`, just like native chat. The primary
gets `IsMention=false` (type:"message"), secondaries get `IsMention=true`
(type:"mention"), and all get `CoAddressees` when `len(agents) > 1`.

**Difference from native chat:** No `ConversationResult` is available for
broker-sourced messages today (the broker-inbound path doesn't resolve
conversations). `RenderDeliveryText` handles a nil `ConvResult` — the
`conversation` field in the envelope will be absent or minimal. This is
acceptable as a first step; adding conversation resolution for broker
messages is orthogonal.

### 6. Permission Model

The existing broker-inbound permission flow applies:
- Broker HMAC authentication (infrastructure-level trust)
- For `user:` senders: ActionAttach check per target agent
- Agent phase check (must be running)

For multi-agent dispatch, the permission check runs per recipient.
If a secondary agent fails permission, that recipient is skipped (same
as native chat's mention fan-out, which logs a warning and marks the
mention as "unauthorized" in mentionResults).

### 7. Error Handling

The primary agent's dispatch is treated as the critical path. If the
primary fails (not found, not running, permission denied, dispatch
error), the endpoint returns an error and no secondaries are dispatched.

Secondary dispatch failures are non-fatal: logged, reported in the
response's `mention_results`, but don't fail the HTTP response.

This matches native chat's behavior where `sendAgentRouted` writes the
HTTP response based on the primary's success and handles fan-out
failures gracefully.

---

## Alternatives Considered

### A. Fix Each Plugin Independently (Status Quo + Per-Plugin Patches)

Each plugin implements its own version of the additive/leading-mention
model. Discord already has a fix in review (f78aba2f9).

**Rejected:** ptone explicitly decided against this. Three independent
implementations of the same routing model means three places to update
when the model changes (as the leading-mention refinement just showed —
a change in native chat had to be separately applied to each plugin).
The Discord fix ships regardless (it fixes a live bug), but the
centralized design prevents the pattern from recurring.

### B. Middleware on the Existing Endpoint

Add mention-routing middleware to the existing `/api/v1/broker/inbound`
that intercepts the request, extracts mentions, and fans out internally
before the handler runs.

**Rejected:** The existing endpoint's contract is per-agent (topic
contains the agent slug). Middleware that silently changes routing
semantics breaks the contract for plugins that rely on it. A separate
endpoint with a different contract is clearer.

### C. Plugin-Side Shared Library

Create a `pkg/pluginrouting` package that all plugins import for mention
routing. The routing runs in the plugin, not the hub.

**Rejected:** The plugins are in `extras/` (Go binaries outside the main
module). Adding a shared import creates a coupling between independently
deployable binaries and the hub's store/messages packages. More
importantly, plugin-side routing still requires agent caches (the plugin
can't query the hub's store directly), and cache staleness was a
contributing factor in Discord's bug. Hub-side routing uses the store
directly — no cache, always fresh.

### D. Extend Broker Inbound API with `co_addressees` Metadata

Keep per-plugin routing, but have each plugin pass co-addressee metadata
in the StructuredMessage so the hub can set `CoAddressees` during
rendering.

**Rejected:** This only fixes Defect 3 (missing `to` field). Defects 1
(no additive model) and 2 (wrong types) still require per-plugin fixes.
If we're touching each plugin anyway, migrating to centralized routing
is the same effort with a better outcome.

---

## Migration / Rollout

### Phase 1: Extract shared routing functions

**Files:** `pkg/hub/handlers_chat_v2.go` (extract), new file
`pkg/hub/agent_routing.go` (or similar).

Extract `resolveRoutingAgents` and `dispatchAgentGroup` from
`sendAgentRouted`. Refactor `handleConversationSend` and
`sendAgentRouted` to call the extracted functions. No behavioral change.

**Test:** All existing tests pass (native chat routing unchanged).

### Phase 2: Add `handleBrokerInboundRouted` endpoint

**Files:** `pkg/hub/handlers_broker_inbound.go` (or new file
`pkg/hub/handlers_broker_inbound_routed.go`), `pkg/hub/server.go`
(route registration).

New endpoint that calls the extracted routing functions. Full test
coverage for all four scenarios (A/B/C/D) via the new endpoint.

**Test:** New integration tests hitting the routed endpoint directly.

### Phase 3: Migrate Slack (easiest)

Slack has no existing mention routing, so migration is purely additive:
change `deliverUserMessage` to call the new endpoint instead of the old
one. No code to remove.

**Files:** `extras/scion-slack/internal/slack/events.go`

### Phase 4: Migrate Discord

Discord has its own mention routing (the most code to remove). After
migration, the Discord bot sends one call per user message; the hub
handles everything.

**Files:** `extras/scion-discord/internal/discord/broker.go`,
`extras/scion-discord/internal/discord/mentions.go`

The already-shipped Discord fix (f78aba2f9) continues to work until
the migration is complete. After migration, the Discord-side routing
code can be removed.

### Phase 5: Migrate Telegram

Telegram has partial mention routing with compensating logic. After
migration, TypeGroupSet and the position-aware classification can be
removed (the hub handles it).

**Files:** `extras/scion-telegram/internal/telegram/broker_v2.go`,
`extras/scion-telegram/internal/telegram/mentions.go`

### Phase 6: Clean up (optional)

Remove dead mention-routing code from plugins. The old
`/api/v1/broker/inbound` endpoint stays indefinitely (it's still useful
for edge cases and third-party integrations that do their own routing).

Each phase is independently deployable. Phases 3-5 are independent of
each other and can be done in any order or in parallel.

---

## Open Questions

### Q1. Should the Discord fix (f78aba2f9) still ship?

**Recommendation: Yes.** It fixes the live bug independently. The
centralized design is a Medium-scope project that will take time to
implement and migrate. The Discord fix ships now; the centralized
endpoint eventually replaces it. No conflict — the Discord fix is in
the plugin, the centralized endpoint is in the hub.

### Q2. DeliveryText conversation metadata

The centralized endpoint will produce DeliveryText without
`ConversationResult` (no conversation resolution for broker messages
today). This means the envelope's `conversation` field will be minimal or
absent. Is this acceptable for the initial version?

**Recommendation: Yes.** The current broker-inbound path has the same
gap. Conversation resolution for broker messages is a separate
enhancement.

### Q3. Attachment forwarding

Native chat's `sendAgentRouted` handles attachments (W7: copy attachment
paths to mention messages). The centralized endpoint's initial version
should accept attachments in the StructuredMessage and pass them through,
but the plugin-side attachment handling (uploading, URL generation) stays
in the plugin.

---

## Acceptance Criteria

### Functional

1. **Scenario A via routed endpoint:** Message with no mentions and a
   default_agent → single dispatch to default agent, type:"message", no
   `to` field.

2. **Scenario B via routed endpoint:** Non-leading mention with
   default_agent → default dispatched with type:"message", mention with
   type:"mention", shared `to` field.

3. **Scenario C via routed endpoint:** Leading sole mention with
   default_agent → only leading agent dispatched, type:"message", no
   `to` field. Default NOT dispatched.

4. **Scenario D via routed endpoint:** Leading multi-mention with
   default_agent → leading is primary (type:"message"), others secondary
   (type:"mention"), shared `to` field. Default NOT dispatched.

5. **No default, with mentions:** Message with mentions but no
   default_agent → first-mentioned is primary, rest are secondaries.

6. **No default, no mentions:** Message with no mentions and no
   default_agent → endpoint returns error (no agent to route to).

7. **DeliveryText produced:** When `writeDenyEnabled()`, all dispatched
   messages have `DeliveryText` with correct type and `to` field.

### Backward Compatibility

8. The existing `/api/v1/broker/inbound` endpoint continues to work
   unchanged for all current callers.

### Permission

9. `user:` sender lacking ActionAttach permission on a secondary agent →
   that secondary is skipped, reported in mention_results, primary still
   dispatched.

10. `user:` sender lacking permission on the primary agent → endpoint
    returns 403, no agents dispatched.

### Plugin Migration (per plugin)

11. After migration, the plugin sends ONE HTTP call per user message
    (not N). The hub fans out correctly.

12. The plugin's own mention-routing code is no longer executed for
    messages routed via the new endpoint.

### Regression

13. Native chat routing is unchanged — all existing native-chat tests
    pass after the extraction refactor (Phase 1).

14. Plugins that haven't migrated continue to work via the old endpoint.

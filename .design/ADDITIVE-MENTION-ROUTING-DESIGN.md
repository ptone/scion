# Additive Mention Routing Design

**Author:** ca-msg-arch  
**Date:** 2026-09-12  
**Status:** PROPOSED  
**Supersedes:** DEF-169's "mention overrides default" routing model  
**Branch:** `scion/ca-msg-arch`

---

## Problem & Goals

ptone's ruling (verbatim, relayed via chat-admin-lead):

> "definitely A - when send a message to A, and mention B - it should
> absolutely be going to A as a group message, the type mention only
> goes to the secondary mention(s). If this is all done with explicit
> addressing - @A Hello there @B and @C got their work done. A is
> primary, B, C are mentions"

The current DEF-169 implementation treats mention-routing as a **full
override** of default-agent routing: when @-mentions are present, ONLY the
mentioned agents are dispatched; the thread's default agent (or the DM's
implicit agent) is not engaged at all, and every recipient gets
`type:"mention"`.

ptone's model is **additive**: the primary agent (thread default, DM
implicit, or first-addressed) is always dispatched and receives
`type:"message"`; @-mentioned secondaries are dispatched additionally and
receive `type:"mention"`. The `to` field lists all engaged agents in
every envelope, so every recipient sees the full group.

### Success Criteria

1. A message to a thread with a default agent that also @-mentions another
   agent delivers to **both**: the default agent with `type:"message"` and
   the mentioned agent with `type:"mention"`.
2. A message @-mentioning multiple agents with no thread default dispatches
   to all; the first-mentioned gets `type:"message"`, the rest get
   `type:"mention"`.
3. Every recipient's envelope has a `to` field listing all engaged agents
   (primary + all secondaries).
4. Existing non-mention routing (default-agent-only, DM-implicit-only) is
   unchanged.

---

## Non-Goals

- **DM-implicit-to-mention promotion.** If a user sends `hello` in a DM
  (no @-mention), the DM agent is the sole, implicit recipient. This
  change does not touch that path.
- **Human mention notifications.** The existing `@username` → human
  mention notification path (`fireHumanMentionNotifications`) is
  unaffected.
- **Dead `Via` values** (`ViaDefaultAgent`, `ViaDirect`). Filed as follow-up
  (DEFECTS.md), not in scope here.
- **Agent-to-agent messaging** (DEF-171). Separate fix, separate path.
- **Client/frontend changes.** The HTTP response shape for the sending
  client is unchanged (still returns `mentionResults` for client-side
  rendering of mention badges).

---

## Proposed Design

### 1. Primary Determination Rule

The **primary agent** is the one whose envelope gets `type:"message"`.
Secondaries get `type:"mention"`.

| Case | Primary | Secondaries |
|------|---------|-------------|
| Thread has default agent + @-mentions | Default agent | All resolved @-mentioned agents (minus default, if also mentioned) |
| DM with agent + @-mentions | DM implicit agent | All resolved @-mentioned agents (minus DM agent, if also mentioned) |
| @-mentions only, no default/DM | First resolved @-mentioned agent (`mentionedAgents[0]`) | `mentionedAgents[1:]` |
| Default agent, no @-mentions | Default agent | (none — single-recipient, unchanged) |
| DM agent, no @-mentions | DM agent | (none — single-recipient, unchanged) |

**"First resolved @-mentioned"** means first in the order returned by
`messages.ResolveMentions`, which preserves body-occurrence order from
`messages.ExtractMentions`. This matches ptone's explicit example (`@A
Hello there @B and @C` → A is primary).

**Load-bearing decision:** The first-mentioned-is-primary convention is a
statement about ordering semantics. If ptone later wants a different
rule (e.g. alphabetical, or "the one with the longest conversation
history in this thread"), the determination logic is isolated in one
function — it does not affect the envelope format, the rendering layer, or
the per-recipient `IsMention` mechanism. **Easily reversible.**

### 2. Routing Change at the Caller

`pkg/hub/handlers_chat_v2.go`, the handler function containing the
Step 3 routing decision (~line 953).

**Current flow (mutually exclusive):**
```
mentionedAgents > 0?  → sendAgentRouted(mentionedAgents, mentionResults)  → return
isDM?                 → sendAgentRouted([dmAgent], nil)                    → return
topic.DefaultAgent?   → sendAgentRouted([defaultAgent], nil)               → return
(else)                → sendHumanToHuman()
```

**New flow (additive):**
```
mentionedAgents > 0?
  ├─ resolve implicit primary (default or DM agent) if available
  ├─ dedup (remove primary from mentionedAgents if also mentioned)
  ├─ if primary found:
  │    agents = [primary] + dedupedMentionedAgents
  │    sendAgentRouted(agents, mentionResults, primaryIsNonMention=true)
  ├─ else:
  │    agents = mentionedAgents  (first is primary by convention)
  │    sendAgentRouted(agents, mentionResults, primaryIsNonMention=false)
  └─ return
isDM?                 → sendAgentRouted([dmAgent], nil)                    → return
topic.DefaultAgent?   → sendAgentRouted([defaultAgent], nil)               → return
(else)                → sendHumanToHuman()
```

The implicit-primary resolution needs to run **inside the mention block**
without duplicating the existing default/DM-agent resolution code. Extract
a helper:

```go
// resolveImplicitPrimary returns the thread's default agent (for topics)
// or the DM-implicit agent (for DMs), if one exists. Returns nil when
// the thread has no implicit agent recipient.
//
// (pseudocode — illustrative)
func (s *Server) resolveImplicitPrimary(
    ctx context.Context, key, projectID string, isDM bool,
) *store.Agent
```

This deduplicates the existing resolution at lines 968-1014 (DM path)
and 986-1014 (topic-default path). Both existing call sites can be
rewritten to call it, or only the new mention+primary combination path
uses it and the existing non-mention paths stay as-is — developer's
discretion on the refactoring scope, as long as the non-mention paths
don't change behavior.

**Deduplication rule:** When the implicit primary IS one of the mentioned
agents (matched by `agent.ID`), remove it from the mention list to prevent
double-dispatch. The primary still appears at `agents[0]` and still
receives type:"message" — the mention result for that agent in
`mentionResults` is preserved for the client-side response, but the
server-side dispatch treats it as primary, not secondary.

### 3. Per-Recipient Type in `sendAgentRouted`

`sendAgentRouted` (`pkg/hub/handlers_chat_v2.go:1028`) currently treats
`mentionResults != nil` as a blanket `IsMention: true` for all recipients.

**Change:**

```
agents[0]  (primary):     IsMention = false   →  type:"message"
agents[1:] (secondaries): IsMention = true    →  type:"mention"
```

This is unconditional — agents[0] is ALWAYS the primary regardless of
how the list was assembled. No new parameter needed for
`primaryIsNonMention`; the positional convention is sufficient because
`sendAgentRouted` already treats `agents[0]` specially (the existing
primary persist+dispatch path at ~lines 1148-1286).

The StructuredMessage type for the primary's persist and dispatch
becomes `TypeInstruction` (the non-mention default), even when mentions
are present:

```go
// old
msgType := messages.TypeInstruction
if mentionResults != nil {
    msgType = messages.TypeMention
}

// new — primary always gets TypeInstruction
msgType := messages.TypeInstruction
// TypeMention is used only for the fan-out messages (agents[1:])
```

**Load-bearing decision:** The primary's `storeMsg.Type` changes from
`TypeMention` (current, when mentions present) to `TypeInstruction` (new,
always). Any downstream query that filters by `Type = "mention"` on
store.Message rows to find "messages dispatched due to mention routing"
will stop matching the primary's row. **This is correct** — the primary
IS NOT a mention recipient in ptone's model. But it's a semantic change
to the persisted data worth noting. The developer should grep for
references to `TypeMention` in query/filter code and verify none depend
on the primary's row being typed as `mention`.

### 4. CoAddressees for All Recipients

`mentionCoAddressees` (`handlers_chat_v2.go:1428`) currently builds
addressees from the mentioned-only agents list. In the additive model, the
primary agent must also appear in `CoAddressees` so every envelope's `to`
field includes it.

**Rename** `mentionCoAddressees` → `groupCoAddressees` (or equivalent) and
pass the full `agents` slice (which now includes the primary at [0]).

**Set `CoAddressees` on ALL render calls** when `len(agents) > 1`, not
just when `mentionResults != nil`:

- Primary's render call (~line 1264-1274): `CoAddressees =
  groupCoAddressees(agents)`, `IsMention = false`
- Fan-out render calls (~line 1381-1388): `CoAddressees =
  groupCoAddressees(agents)`, `IsMention = true`

The rendering layer already handles this correctly:
- `RenderDeliveryText` (`render_delivery.go:118-119`): `if
  len(in.CoAddressees) > 0 { addrs = in.CoAddressees }` — unconditional
  on `IsMention`.
- `FormatNewDelivery` (`delivery.go:96`): `if len(addrs) > 1 ||
  isMention` — with multiple agents, `to` is present even when `IsMention`
  is false.
- `typeString` (`delivery.go:119`): when `isMention` is false, returns
  `"message"` — correct for the primary.

**No changes needed in `pkg/messaging/delivery.go` or
`pkg/messaging/render_delivery.go`.** The existing rendering layer
already supports per-recipient `IsMention` with shared `CoAddressees`.
The comment on `RenderDeliveryInput.CoAddressees` (line 55-59:
"Only meaningful when IsMention is true") needs updating to reflect that
it's now also used for the primary in group sends, but the behavior is
already correct.

### 5. Primary's Mention Metadata

Currently when `mentionResults != nil`, the primary's StructuredMessage
gets `mention_source` and `mention_position` metadata (lines 1065-1069).
In the additive model, the primary is NOT a mention recipient — it's the
primary recipient who happens to be in a group that includes mentions.

**Remove mention metadata from the primary's StructuredMessage.** The
fan-out messages already get their own metadata via `messages.NewMention`
(line 1307), which correctly sets `mention_source` and
`mention_position`. The primary's metadata should not claim it was
mentioned when it was dispatched as the primary.

**Exception:** When `primaryIsNonMention` is false (the first-mentioned-
is-primary case with no implicit primary), this is a judgment call. The
first-mentioned agent IS technically mentioned in the body, but ptone's
model treats it as primary. I recommend: omit mention metadata from the
primary in ALL cases — the primary is primary regardless of how it got
there.

### 6. Fan-Out StructuredMessage Construction

The fan-out loop (lines 1307-1314) currently builds the secondary's
StructuredMessage via `messages.NewMention(msg.Sender,
"agent:"+mentionAgent.Slug, content, msg.Recipient)`. The fourth
argument (`msg.Recipient`) identifies the primary — currently "the first
mentioned agent's slug." In the additive model, `msg.Recipient` is the
actual primary agent (default/DM/first-mentioned), which is correct: it
tells the secondary who the "main" recipient was.

No change needed to `NewMention` or its arguments.

### 7. Envelope Examples (Wire Format)

**Scenario: Thread default = agent-a, user types `@agent-b @agent-c
hello`**

agent-a's envelope (primary):
```json
{
  "timestamp": "2026-09-12T00:30:00Z",
  "conversation": {"id": "...", "kind": "group", "surface": "native"},
  "from": "user:Preston Holmes",
  "to": ["agent:agent-a", "agent:agent-b", "agent:agent-c"],
  "type": "message",
  "msg": "@agent-b @agent-c hello"
}
```

agent-b's envelope (secondary):
```json
{
  "timestamp": "2026-09-12T00:30:00Z",
  "conversation": {"id": "...", "kind": "group", "surface": "native"},
  "from": "user:Preston Holmes",
  "to": ["agent:agent-a", "agent:agent-b", "agent:agent-c"],
  "type": "mention",
  "msg": "@agent-b @agent-c hello"
}
```

agent-c's envelope: identical to agent-b's (same `to`, same `type`,
different ConversationResult depending on conversation resolution).

**Scenario: No default, user types `@agent-a hello @agent-b`**

agent-a's envelope (primary, first-mentioned):
```json
{
  "from": "user:Preston Holmes",
  "to": ["agent:agent-a", "agent:agent-b"],
  "type": "message",
  "msg": "@agent-a hello @agent-b"
}
```

agent-b's envelope (secondary):
```json
{
  "from": "user:Preston Holmes",
  "to": ["agent:agent-a", "agent:agent-b"],
  "type": "mention",
  "msg": "@agent-a hello @agent-b"
}
```

**Scenario: No default, user types `@agent-a hello` (single mention)**

agent-a's envelope (primary, sole recipient):
```json
{
  "from": "user:Preston Holmes",
  "type": "message",
  "msg": "@agent-a hello"
}
```

No `to` field — single non-mention recipient (unchanged from current
default-agent behavior). This IS a change from DEF-169's behavior, where
a single @-mention produced `type:"mention"` with `to:[agent-a]`. See
§Consequences below.

---

## Alternatives Considered

### A. Keep Mention Override, Add Awareness-Only `to`

Dispatch only to mentioned agents (current behavior), but include the
thread's default agent in `to` for visibility even though it wasn't
dispatched. Secondaries see who the "main" agent is without the main
agent being engaged.

**Rejected:** ptone explicitly said "it should absolutely be going to A
as a group message" — A must be DISPATCHED, not just named in `to`.

### B. Introduce a `type:"group"` Value

Instead of the primary getting `type:"message"`, introduce a new
`type:"group"` that signals "you're the primary in a group send."

**Rejected:** Adds a fourth type value to a field that just collapsed from
two fields to three values two hours ago. ptone's description maps
cleanly to the existing "message"/"mention" distinction — the primary's
type is a normal message, the secondaries' type signals they were
mentioned. A new value adds conceptual overhead with no demonstrated
benefit.

### C. Per-Message Type (Same for All Recipients)

Keep a single `type` per message. All recipients get `type:"mention"`
when mentions are present (current behavior), with a separate field
to distinguish primary/secondary.

**Rejected:** ptone explicitly said "the type mention only goes to the
secondary mention(s)" — the primary gets `type:"message"`, meaning type
MUST vary per recipient. Adding a primary/secondary field alongside a
uniform type would be a more complex wire format for no benefit.

---

## Consequences to Note

### Single @-mention with no default agent

**Before this change (DEF-169):** A single `@agent-a` in a topic with
no default agent produces `type:"mention"`, `to:["agent:agent-a"]`.

**After this change:** The same message produces `type:"message"`, no
`to` field. The agent loses the "I was explicitly @-mentioned" signal.

**Rationale:** With no secondaries, there's no meaningful distinction
between "mentioned" and "addressed" — the agent is the sole recipient
either way. ptone's model assigns mention semantics only to secondaries
("the type mention only goes to the secondary mention(s)"). A sole
recipient has no secondary role.

If this turns out to be a problem (an agent wants to know it was
@-mentioned even when sole recipient), a `via` or `routing` metadata
field could be added later without changing `type`. **Easily
reversible.**

### Primary's persisted `store.Message.Type`

Changes from `TypeMention` to `TypeInstruction` when mentions are
present. Any analytics/dashboards querying `type = "mention"` on the
messages table will stop counting primary-agent rows. This is
semantically correct (the primary isn't a mention recipient) but may
affect counts.

---

## Migration / Rollout

This change is behind `writeDenyEnabled()` for the envelope rendering —
the per-recipient type difference only appears in `DeliveryText`. The
StructuredMessage.Type change (primary from `TypeMention` to
`TypeInstruction`) and the dispatch-additive change (primary receives
dispatch when it previously didn't) are NOT gated — they affect all sends
once deployed.

**Rollout order:**
1. Land on `scion/tranche-g` (internal, no real traffic).
2. Deploy to gteam for ptone's live verification.
3. If ptone confirms correct behavior, merge to `main`.

No data migration needed — this changes runtime routing behavior, not
persisted schema.

---

## Open Questions

### Q1. First-mentioned-is-primary when no implicit agent

The design uses "first resolved @-mentioned agent" as the primary when
no thread default or DM implicit agent exists. This matches ptone's
explicit example (`@A Hello there @B and @C` → A is primary). But it
means a user who writes `Hello @B and @A please help` gets B as primary,
which may feel arbitrary.

**Recommendation:** Accept first-mentioned as the convention. It's
deterministic, matches the example, and the determination logic is
isolated in one function. If a different rule is needed later, only that
function changes.

**Decision needed from ptone?** Only if the edge case matters to him.
The design proceeds on first-mentioned unless he objects.

### Q2. DM + mention interaction

When a user in a DM with agent-a types `@agent-b hello`, the additive
model dispatches to BOTH: agent-a (DM implicit, type:"message") and
agent-b (mentioned, type:"mention"). agent-b's conversation is resolved
independently (the existing fan-out conversation resolution handles this
— lines 1332-1368).

This is the consistent application of "additive" but it's a topology
change: agent-a's DM now generates a side-channel dispatch to agent-b.
The design proceeds on this behavior unless ptone objects.

---

## Implementation Phases

### Phase 1: Extract `resolveImplicitPrimary` helper

**Files:** `pkg/hub/handlers_chat_v2.go`

Extract the default-agent resolution (lines 988-1006, topic path) and
DM-agent resolution (lines 973-975, DM path) into a helper function.
Existing non-mention call sites can optionally be refactored to use it.

**Test:** Existing tests pass. No behavioral change.

### Phase 2: Additive routing at the caller

**Files:** `pkg/hub/handlers_chat_v2.go`

In the `len(mentionedAgents) > 0` block (line 954): call
`resolveImplicitPrimary`, dedup, prepend primary (if found), and pass the
combined list to `sendAgentRouted`. When no implicit primary exists,
`mentionedAgents` is passed unchanged (first-mentioned is primary by
position).

### Phase 3: Per-recipient IsMention in `sendAgentRouted`

**Files:** `pkg/hub/handlers_chat_v2.go`

- Remove the blanket `msgType = TypeMention` when `mentionResults != nil`
  — primary always gets `TypeInstruction`.
- Primary render call: `IsMention = false`, `CoAddressees =
  groupCoAddressees(agents)` when `len(agents) > 1`.
- Fan-out render calls: `IsMention = true`, `CoAddressees =
  groupCoAddressees(agents)` (unchanged except the list now includes
  the primary).
- Remove mention metadata from the primary's StructuredMessage when
  a non-mention primary is present.
- Rename `mentionCoAddressees` → `groupCoAddressees` (or equivalent)
  and pass the full `agents` slice.

### Phase 4: Verification

See Acceptance Criteria below. All phases should land in one commit
since they're interdependent.

---

## Acceptance Criteria

### Functional

1. **Default + mention:** A message to a topic with default agent-a that
   @-mentions agent-b: both receive dispatch. agent-a's envelope has
   `type:"message"`, `to:["agent:agent-a","agent:agent-b"]`. agent-b's
   envelope has `type:"mention"`, same `to`.

2. **DM + mention:** A message in a DM with agent-a that @-mentions
   agent-b: both receive dispatch. agent-a gets `type:"message"`, agent-b
   gets `type:"mention"`, both have `to` listing both.

3. **Multi-mention, no default:** A message @-mentioning agent-a and
   agent-b with no default/DM: agent-a (first-mentioned) gets
   `type:"message"`, agent-b gets `type:"mention"`, both have `to`
   listing both.

4. **Default agent also mentioned:** A message in a topic with default
   agent-a that also @-mentions agent-a and agent-b: agent-a dispatched
   ONCE (as primary, `type:"message"`), agent-b as secondary
   (`type:"mention"`). No double dispatch for agent-a.

5. **Single mention, no default:** A message @-mentioning only agent-a
   with no default: agent-a dispatched as sole recipient, `type:"message"`,
   no `to` field.

6. **Default only, no mentions:** Unchanged — `type:"message"`, no `to`,
   single-recipient dispatch.

7. **No agents at all:** Falls through to `sendHumanToHuman`. Unchanged.

### Regression

8. Default-agent-only and DM-only routing paths produce identical envelopes
   to before this change.

9. Human mention notifications (`fireHumanMentionNotifications`) still fire
   for non-agent @-mentions.

### Mutation

10. Force `IsMention = true` on the primary's render call → test fails
    (primary envelope shows `type:"mention"` instead of `type:"message"`).

11. Remove the `resolveImplicitPrimary` call (or force it to return nil)
    → test fails (default agent not dispatched when mentions present).

12. Remove the dedup step → test fails (default agent dispatched twice
    when also mentioned, or appears twice in `to`).

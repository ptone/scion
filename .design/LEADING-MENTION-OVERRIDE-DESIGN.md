# Leading-Mention Override Refinement

**Author:** ca-msg-arch
**Date:** 2026-09-12
**Status:** PROPOSED
**Refines:** ADDITIVE-MENTION-ROUTING-DESIGN.md (deployed at 638f8278)
**Branch:** `scion/ca-msg-arch`
**Trigger:** ptone live-testing on gteam — scenario C produces wrong behavior

---

## Problem & Goals

The additive mention routing deployed at `638f8278` always prepends the
thread's implicit primary (default agent or DM agent) when @-mentions are
present. ptone's testing identified that this is wrong for the
**leading-mention** case: when a user starts their message with `@agent-b`,
they are explicitly addressing agent-b — the thread's default agent should
NOT be injected.

ptone's refined model (verbatim, relayed via chat-admin-lead):

> "The rule: a LEADING @-mention that is the ONLY mention in the message
> fully overrides the default agent (pure single-recipient, C). A leading
> @-mention with OTHER mentions elsewhere makes the leading one primary
> instead of the default (D). A mention anywhere in the body when there's
> NO leading mention keeps the default as primary and adds the mention as
> secondary (B)."

### Four Scenarios

| ID | Message shape | Default agent | Primary recipient | Secondaries | Implicit default dispatched? |
|----|--------------|---------------|-------------------|-------------|------------------------------|
| A  | `hello` (no mentions) | agent-a | agent-a | (none) | Yes (sole recipient) |
| B  | `hello @agent-b` (non-leading) | agent-a | agent-a | agent-b | Yes (additive, current behavior) |
| C  | `@agent-b hello` (leading, sole mention) | agent-a | agent-b | (none) | **No** (full override) |
| D  | `@agent-b hello @agent-c` (leading, multi-mention) | agent-a | agent-b | agent-c | **No** (leading overrides) |

The bug on gteam: scenario C currently dispatches to BOTH agent-a
(type:"message") and agent-b (type:"mention") — the additive model treats
all mentions as additive. The correct behavior is: only agent-b is
dispatched, as a single-recipient type:"message" with no `to` field.

### Success Criteria

1. Scenario C produces a single-recipient dispatch to the leading-mentioned
   agent with `type:"message"`, no `to` field, default agent NOT engaged.
2. Scenario D produces leading-mentioned agent as primary (`type:"message"`)
   and other mentions as secondaries (`type:"mention"`), `to` lists all,
   default agent NOT engaged.
3. Scenario B is unchanged from additive routing — default is primary,
   mentions are additive secondaries.
4. Scenario A is unchanged — default-only, no mentions, no `to`.

---

## Non-Goals

- **Changing `sendAgentRouted`.**  The `agents[0]`-is-primary positional
  convention already handles all four scenarios. The fix is entirely in
  the CALLER routing decision that assembles the `agents` slice.
- **Changing the rendering layer.**  `pkg/messaging/delivery.go`,
  `pkg/messaging/render_delivery.go` — no changes.
- **Position-aware mention extraction.**  `ExtractMentions` returns names
  without position information. The leading-mention detection does NOT
  require modifying `ExtractMentions` — it uses `strings.Fields` directly
  on the content, which is the same tokenization `ExtractMentions` uses.

---

## Proposed Design

### 1. Leading-Mention Detection

**Conceptual rule:** The message has a "leading mention" when:
1. The first whitespace-delimited token of the trimmed content starts
   with `@`.
2. The @-name extracted from that token (using the same rules as
   `ExtractMentions`: TrimPrefix `@`, TrimRightFunc for trailing
   punctuation except `_` and `-`) is non-empty.
3. That name case-insensitively matches `mentionNames[0]` (confirming
   the leading word IS the first extracted mention — not a stale `@` or
   noise token that ExtractMentions skipped).
4. `mentionedAgents[0].Slug` case-insensitively matches
   `mentionNames[0]` (confirming the first extracted mention actually
   resolved to a real agent).

Conditions (3) and (4) together guard against false positives:
- `@typo @agent-a hello` → leading word "typo" ≠ `mentionedAgents[0].Slug`
  "agent-a" → NOT leading → additive (correct).
- `@!!! @agent-a hello` → leading word extracts to "" → NOT leading (correct).
- `@ agent-a hello` → leading word `@` extracts to "" → NOT leading (correct).

**Recommended implementation:** Add `IsLeadingMention(text, firstMentionName string) bool`
to `pkg/messages/mentions.go`. This is a pure text function:

```go
// IsLeadingMention reports whether text begins with @firstMentionName
// as a leading @-mention token (the first whitespace-delimited word
// starts with @ and its extracted name matches firstMentionName,
// case-insensitive).
//
// (pseudocode — illustrative)
func IsLeadingMention(text, firstMentionName string) bool {
    fields := strings.Fields(text)
    if len(fields) == 0 || !strings.HasPrefix(fields[0], "@") {
        return false
    }
    name := strings.TrimPrefix(fields[0], "@")
    name = strings.TrimRightFunc(name, func(r rune) bool {
        return unicode.IsPunct(r) && r != '_' && r != '-'
    })
    return name != "" && strings.EqualFold(name, firstMentionName)
}
```

The resolution check (condition 4) stays in the handler:

```go
isLeading := len(mentionedAgents) > 0 && len(mentionNames) > 0 &&
    strings.EqualFold(mentionedAgents[0].Slug, mentionNames[0]) &&
    messages.IsLeadingMention(content, mentionNames[0])
```

**Alternative (acceptable):** Inline the detection in the handler
without adding a function to `mentions.go`. The extraction logic is 5
lines. Either approach is fine — developer's choice.

### 2. Routing Decision Gate

**Location:** `pkg/hub/handlers_chat_v2.go`, the `if len(mentionedAgents) > 0`
block (~line 957).

**Current code:**
```go
if len(mentionedAgents) > 0 {
    // Always resolve implicit primary and prepend.
    var implicitPrimary *store.Agent
    // ... 20 lines of resolution ...
    agents := mentionedAgents
    if implicitPrimary != nil {
        // dedup + prepend
    }
    msgID := s.sendAgentRouted(...)
}
```

**New code (pseudocode):**
```go
if len(mentionedAgents) > 0 {
    isLeading := /* detection from §1 */

    agents := mentionedAgents
    if !isLeading {
        // Scenario B: non-leading mentions are additive.
        // Resolve implicit primary and prepend (existing code).
        var implicitPrimary *store.Agent
        // ... resolution unchanged ...
        if implicitPrimary != nil {
            deduped := /* ... */
            agents = append([]*store.Agent{implicitPrimary}, deduped...)
        }
    }
    // Scenarios C and D: isLeading → agents = mentionedAgents as-is.
    // mentionedAgents[0] is the leading-mentioned agent → primary by position.

    msgID := s.sendAgentRouted(...)
}
```

**What changes:** The existing implicit-primary resolution block is
wrapped in `if !isLeading { ... }`. When the leading mention is
detected, `agents = mentionedAgents` flows through unchanged —
`agents[0]` is the leading-mentioned agent (primary by position),
`agents[1:]` are any other mentions (secondaries).

**What does NOT change:** `sendAgentRouted` itself. The positional
convention (`agents[0]` = primary, `agents[1:]` = fan-out) already
handles all four scenarios correctly. No per-recipient rendering
changes, no envelope format changes, no `CoAddressees` logic changes.

### 3. Behavior Matrix After Change

| Scenario | `isLeading` | `agents` assembled as | Primary's type | `to` field |
|----------|-------------|----------------------|----------------|------------|
| A (no mentions) | N/A | (not reached — falls through to default/DM/h2h paths) | "message" | absent |
| B (non-leading) | false | [implicitPrimary] + dedupedMentionedAgents | "message" | all agents |
| C (leading, sole) | true | [mentionedAgents[0]] | "message" | absent (single recipient) |
| D (leading, multi) | true | mentionedAgents ([leading] + rest) | "message" | all agents |

### 4. Envelope Examples

**Scenario C: `@agent-b hello`, default = agent-a**

agent-b's envelope (sole recipient):
```json
{
  "from": "user:Preston Holmes",
  "type": "message",
  "msg": "@agent-b hello"
}
```
No `to` field (single non-mention recipient). agent-a is NOT dispatched.

**Scenario D: `@agent-b hello @agent-c`, default = agent-a**

agent-b's envelope (primary):
```json
{
  "from": "user:Preston Holmes",
  "to": ["agent:agent-b", "agent:agent-c"],
  "type": "message",
  "msg": "@agent-b hello @agent-c"
}
```

agent-c's envelope (secondary):
```json
{
  "from": "user:Preston Holmes",
  "to": ["agent:agent-b", "agent:agent-c"],
  "type": "mention",
  "msg": "@agent-b hello @agent-c"
}
```
agent-a is NOT dispatched. agent-b is primary (not because it's the
default — it's not — but because it's the leading @-mention).

---

## Alternatives Considered

### A. Always Additive (No Leading-Mention Override)

Keep the current 638f8278 behavior: mentions always add to the default
agent, never override.

**Rejected:** ptone tested it live and says scenario C is wrong. When a
user starts with `@agent-b`, they're explicitly redirecting — not asking
the default agent to also look at it. The override semantic is the natural
UX.

### B. Separate `@to:` / `!` Syntax

Introduce a distinct syntax for override (e.g., `@to:agent-b` or
`!agent-b`) to distinguish "override" from "mention."

**Rejected:** Adds user-facing syntax complexity. The leading-position
heuristic maps naturally to how users already type — starting a message
with `@name` reads as "directing this TO name." No new syntax to learn.
If the heuristic turns out to be wrong for some cases, the override
syntax can be added later as a refinement. **Easily reversible.**

### C. Use `ExtractMentions` Position Info

Modify `ExtractMentions` to return position information (byte offset or
word index) for each mention, then use that to detect leading mentions.

**Rejected:** Over-engineers the solution. `ExtractMentions` returns names
in body-occurrence order, so `mentionNames[0]` is always the first
mention. Checking whether the message starts with `@` + that name is a
3-line check that doesn't require changing the mention extraction API.
If richer position info is needed later (e.g., for inline mention
highlighting), `ExtractMentions` can be extended then. **Easily
reversible.**

---

## Edge Cases

### Leading @-mention is the default agent

Message: `@agent-a hello`, default = agent-a.

`isLeading` = true → agents = [agent-a] → single-recipient dispatch.
Behavior is identical to the no-mention path (scenario A). This is correct
— the user is explicitly addressing the default agent, which is the same
as the default behavior. No double dispatch, no behavioral change.

### Leading @-mention doesn't resolve

Message: `@nonexistent hello @agent-b`, default = agent-a.

- `mentionNames` = ["nonexistent", "agent-b"]
- `mentionedAgents` = [agent-b] (nonexistent skipped)
- `mentionedAgents[0].Slug` = "agent-b" ≠ `mentionNames[0]` = "nonexistent"
- `isLeading` = false → additive → agents = [agent-a, agent-b]

Correct: the typo falls through to additive behavior. The user didn't
successfully address a specific agent with their leading word, so the
default agent stays primary.

### DM + leading mention

Message: `@agent-b hello` in a DM with agent-a.

`isLeading` = true → agents = [agent-b] → single-recipient dispatch.
agent-a (DM implicit) is NOT dispatched.

This is the expected behavior: the user is explicitly redirecting from
the DM agent to agent-b. The DM agent's context thread continues to
exist but this particular message goes only to agent-b.

### Leading `@` with no name

Message: `@ hello @agent-b`, default = agent-a.

- Leading word `@` → extracted name = "" → `isLeading` = false
- Falls through to additive: agents = [agent-a, agent-b]

Correct: a bare `@` is not a mention.

### Leading `@` with all-punctuation name

Message: `@!!! hello @agent-b`, default = agent-a.

- Leading word `@!!!` → TrimPrefix → `!!!` → TrimRightFunc → "" → `isLeading` = false
- Falls through to additive: agents = [agent-a, agent-b]

Correct: noise punctuation is not a mention.

---

## Migration / Rollout

Same as the parent additive-routing design: changes affect runtime
routing behavior only, no schema or data migration. The change is entirely
additive to the 638f8278 codebase.

**Rollout:**
1. Land on `scion/tranche-g`.
2. Deploy to gteam for ptone's live verification of scenarios C and D.
3. If confirmed, merge to `main`.

---

## Open Questions

None. ptone's four-scenario model is fully specified. The detection
algorithm handles all identified edge cases. No design decision requires
further input.

---

## Implementation Phases

### Phase 1: Add `IsLeadingMention` to `pkg/messages/mentions.go`

Pure text function, independently testable. Unit tests:
- `"@agent-a hello"` + "agent-a" → true
- `"hello @agent-a"` + "agent-a" → false
- `"@AGENT-A hello"` + "agent-a" → true (case-insensitive)
- `"@typo hello"` + "agent-a" → false (name mismatch)
- `"@ hello"` + "agent-a" → false (empty name)
- `"@agent-a, hello"` + "agent-a" → true (trailing punct stripped)
- `""` + "agent-a" → false (empty text)

### Phase 2: Gate the implicit-primary resolution

In the `len(mentionedAgents) > 0` block of `handlers_chat_v2.go`,
add the detection and wrap the existing implicit-primary block in
`if !isLeading`. ~5 lines of new code, 2 lines of wrapping.

### Phase 3: Integration tests for scenarios C and D

Extend the existing DEF-169/additive-routing test suite with:
- Scenario C: leading sole mention overrides default
- Scenario D: leading multi-mention, leading is primary, no default
- Edge case: leading mention IS the default agent → single dispatch

Mutation tests:
- Force `isLeading = false` always → scenario C test fails
- Force `isLeading = true` always → scenario B test fails

All phases should land in one commit.

---

## Acceptance Criteria

### Functional

1. **Scenario C (leading sole mention override):** `@agent-b hello` in a
   topic with default agent-a: agent-b dispatched as sole recipient with
   `type:"message"`, no `to` field. agent-a NOT dispatched.

2. **Scenario D (leading multi-mention override):** `@agent-b hello
   @agent-c` in a topic with default agent-a: agent-b dispatched with
   `type:"message"` and `to:["agent:agent-b","agent:agent-c"]`. agent-c
   dispatched with `type:"mention"` and same `to`. agent-a NOT dispatched.

3. **Scenario B regression (non-leading additive):** `hello @agent-b` in a
   topic with default agent-a: agent-a dispatched with `type:"message"`,
   agent-b with `type:"mention"`, `to` lists both. Unchanged from
   additive routing.

4. **Scenario A regression (no mentions):** Default-only routing is
   unchanged.

5. **Leading mention IS default agent:** `@agent-a hello` with default
   agent-a: single dispatch to agent-a, `type:"message"`, no `to`.

6. **Leading unresolved mention falls back to additive:** `@nonexistent
   hello @agent-b` with default agent-a: agent-a primary, agent-b
   secondary (additive behavior).

### Mutation

7. Force `isLeading = false` → scenario C test fails (default agent
   dispatched when it shouldn't be).

8. Force `isLeading = true` → scenario B test fails (default agent NOT
   dispatched when it should be).

### Unit (IsLeadingMention)

9. All 7 test cases from Phase 1 pass.

10. Reverting `IsLeadingMention` to always return false → integration
    test for scenario C fails.

# DEF-162 — Agent-authored mentions must notify humans

**Status:** DESIGNED, not yet staffed
**Base:** `scion/tranche-g` @ `e5b651719`
**Author:** `ca-msg-arch`, 2026-09-10

---

## 1. Problem & Goals

ptone's DEF-160 ruling has two clauses:

> "group conv:id is the recipient. message should require no more than required
> to clearly route. and error clearly with what is required if not met. **an
> agent can choose to mention a recipient in message text if they want that
> recipient to be drawn to a group message.**"

The routing clause shipped (DEF-160, `31dfbb414` + `e5b651719`). **The attention
clause has no implementation on any agent-originated path.** An agent writes
`@preston` in a group reply; the text is persisted and rendered; no notification
is created; the human learns of it only by independently opening the room.

This is DEF-158's failure shape — correct persistence, no announcement — with the
difference that it is currently *documented* rather than accidental. That does
not make it acceptable as an end state, and ptone has been explicit that he wants
the true end state testable on gteam.

**Goal:** an agent-authored mention produces the same notification a
human-authored mention produces, on every delivery path.

**Success criteria:** AC-1 … AC-8 in §9.

## 2. Non-Goals

- **A general group push notification.** Topics today have only the passive
  unread dot (`TouchTopicActivity`, `messagebroker.go:595`). This design does not
  add per-message group notifications; it wires *mentions* only. Changing
  baseline group notification volume is a product decision nobody has asked for.
- **Mention→agent fan-out.** `cmd/message.go` already fans mentions out to
  *agents* — extraction at `:495`/`:796`, fan-out via `sendMentionMessages`
  (`:968`) at `:499`/`:823`. Untouched.
- **Fixing the mention name-resolution ambiguity.** See F3 below; filed
  separately as DEF-165. Wiring must not silently inherit it, but must not try to
  fix it either.
- **Notification preferences / digesting / rate limiting.**

## 3. Governing principle

Mirroring DEF-160's principle, which worked:

> **An agent-authored mention should be indistinguishable in notification effect
> from a human-authored one.**

Concretely: reuse `fireHumanMentionNotifications`
(`pkg/hub/handlers_chat_v2.go:3497`) rather than write a second mention resolver.
This project's documented repeat offence is two independent implementations of
one format drifting apart (DEF-156, whose helper comment says so in as many
words). A second mention path would be the same mistake in a new place.

## 4. What already exists — verified, with lines

| Component | Location | State |
|---|---|---|
| `ExtractMentions` | `pkg/messages/mentions.go:38` | shared, already used by both `pkg/hub` and `cmd` |
| `fireHumanMentionNotifications` | `pkg/hub/handlers_chat_v2.go:3497` | resolves names → members, skips sender, dedupes, fires |
| `NotifyMention` | `pkg/hub/notifications.go:634` | respects mute; creates notification; publishes |
| `resolveProjectHumanMembers` | `pkg/hub/handlers_chat_v2.go:3572` | humans only, from `project:<slug>:members` |
| Only `pkg/hub` mention call site | `handlers_chat_v2.go:918` | human web send path |
| Only `fireHumanMentionNotifications` callers | `handlers_chat_v2.go:1395`, `:1561` | both human web send path |
| **Thread key on the agent path** | `handlers_agent_messaging.go:655` | **already computed** by DEF-160 P0 |
| Agent sender-label pattern | `handlers_agent_messaging.go:924-926` | `agent.Name` → `agent.Slug` fallback |
| DM notification block on agent path | `handlers_agent_messaging.go:920-936` | the structural sibling of what we are adding |

**The prerequisite was already paid.** `fireHumanMentionNotifications` needs a
`conversationKey` that `GetTopic` can resolve (`:3535`). Before DEF-160 the agent
outbound path did not have one for groups; `ParseThreadConversationExternalRef`
(P0) now yields `threadKey` at `:655`. That is why this design is small.

## 5. Findings that shape the design

### F1 — `NotifyMention` does not resolve agent sender names; `NotifyDMReceived` does

`NotifyDMReceived` carries an explicit block (`notifications.go:709-717`) that
detects `SenderName == SenderID` and resolves the agent's `Name`/`Slug`, with a
comment naming `handlers_agent_messaging.go:353` and `handlers_chat_v2.go:1293`
as callers that already pass a proper label and "must not be clobbered."

`NotifyMention` (`:634-667`) has no such block, because until now it has only
ever had human senders.

**Ruling: the caller passes a resolved label; `NotifyMention` is not modified.**
Use the existing pattern at `handlers_agent_messaging.go:924-926`. Adding a
second copy of the UUID-sniffing heuristic into `NotifyMention` would spread a
workaround rather than a contract, and the `SenderName == SenderID` test is a
guess about the caller that we do not need to make when we *are* the caller.

Without this, humans receive "`<uuid>` mentioned you in …".

### F2 — the broker path has no mention handling at all, and the DM block is gated on the proxy being absent

`handlers_agent_messaging.go:922` gates the DM notification on
`s.GetMessageBrokerProxy() == nil`, with the comment: *"The broker path fires
notifications from deliverToUser in messagebroker.go."*

**`grep -n 'mention' pkg/hub/messagebroker.go` returns nothing.** There is no
mention handling on the broker path, and no counterpart to fall back on.

**This is the risk that would make the whole fix worthless.** Wiring mentions
inside the `bp == nil` branch produces a change that passes every test and is
dead on any deployment configured with a message broker proxy. The failure would
present exactly as DEF-162 presents now — nothing happens — and the tracker would
say it was fixed.

**Ruling: mentions fire on a single choke point that both paths traverse, or on
both paths explicitly. Determining which is P0 of this work, and it is a
*reading* task with a written answer, not an assumption.** The implementer must
state, with `file:line`, where an agent-authored group message converges (if it
does) after the `bp == nil` fork, and site the call there.

If the two paths do not converge after the fork, the fallback is to fire on both
and dedupe — but that must be justified in the report, not chosen silently.

### F3 — the mention lookup silently collapses distinct users who share an email local-part

`handlers_chat_v2.go:3519-3525` builds the lookup by display name, full email,
**and email local-part**:

```go
lookup[strings.ToLower(m.Email[:at])] = memberInfo{ID: m.ID, ...}
```

Two members with `preston@google.com` and `preston@gmail.com` both write
`lookup["preston"]`. Last writer wins; the order comes from `GetGroupMembers` and
is not guaranteed. Same for two members sharing a display name.

**This is live on gteam**, which has exactly that pair, and it is live *today* for
human senders — this design does not create it. It is filed as **DEF-165** and is
explicitly out of scope here.

It matters to this design in one way only: **the acceptance tests for DEF-162
must not use an ambiguous mention token**, or a green test will mean nothing. AC-3
pins this.

It also cuts against a standing preference of ptone's — that routing be explicit
and ambiguity be an error rather than a guess. Recommending an error-on-ambiguous
resolution in DEF-165, not here.

## 6. Proposed design

Two insertion points, one shared helper, no new resolution logic.

### 6.1 Extract the mention trigger into one server method

```
// (pseudocode — illustrative)
func (s *Server) fireMentionsForAgentMessage(
    ctx context.Context,
    agent *store.Agent,
    conversationKey string,   // topic key; "" or dm:-prefixed ⇒ no-op
    content string,
) {
    if conversationKey == "" || strings.HasPrefix(conversationKey, "dm:") {
        return   // DMs already notify via NotifyDMReceived; no mention needed
    }
    names := messages.ExtractMentions(content)
    if len(names) == 0 {
        return
    }
    label := agent.Name
    if label == "" {
        label = agent.Slug
    }
    // senderUserID is "" — an agent is never a project human member, so the
    // self-skip at handlers_chat_v2.go:3549 correctly never matches.
    s.fireHumanMentionNotifications(ctx, names, agent.ProjectID,
        conversationKey, "", label, content)
}
```

Note `senderUserID = ""`. `fireHumanMentionNotifications` uses it only for the
self-skip (`:3549`) and for `ChatMessageContext.SenderID` (`:3559`).
`NotifyMention` never reads `SenderID` (verified: `:634-667`), so an empty value
is inert on this path. **The implementer must confirm that
`PublishChatNotification` consumers likewise tolerate an empty `SenderID`, and
say so with evidence.** If any consumer keys off it, pass `agent.ID` instead —
but then F1's clobber comment at `notifications.go:704` becomes relevant and must
be re-read.

### 6.2 Call it from the agent outbound path

Beside the existing DM block at `handlers_agent_messaging.go:920-936`, subject to
F2's choke-point ruling. `req.ThreadID` holds `threadKey` for groups after
DEF-160 (`:685`).

### 6.3 Call it from the broker path

Per F2. `deliverToUser` in `messagebroker.go` already has `msg.ThreadID` in scope
(`+22`, `+50` from the function head).

### 6.4 Do not touch

`NotifyMention`, `fireHumanMentionNotifications`, `resolveProjectHumanMembers`,
`ExtractMentions`. All four are shared with the human path, and the entire point
of the design is that the human path's behaviour is the specification.

## 7. Alternatives considered

**A. Fire mentions from `cmd/` (the CLI), reusing `sendMentionMessages` (`cmd/message.go:968`).**
Rejected. That machinery targets *agents*, not users, so it does not satisfy the
ruling; and it is client-side, so anything reaching the API directly — including
the broker — would be unaffected. Notification is a server responsibility because
the server owns the member list and the mute state.

**B. Add a `mentions []string` field to `OutboundMessageRequest` and have the CLI
extract them.** Rejected on two grounds. It duplicates extraction (the defect
family DEF-156 exists to close), and it makes an attention-affecting field
client-supplied, so a caller could notify a user without the text containing a
mention. Extraction stays server-side and derived from the message body, which
also means the notification cannot disagree with what the reader sees.

**C. Give groups a general push notification and drop mentions as a mechanism.**
Rejected: not what was ruled, and it inverts the volume trade-off. Mentions are
sender-chosen and explicit, which is the same property ptone required when he
rejected reply-affinity — *"I'd broadly prefer consistent, explicit routing."*

**D. Modify `NotifyMention` to resolve agent senders (mirror
`notifications.go:709-717`).** Rejected per F1: it spreads a caller-guessing
heuristic to a second site when the caller can simply pass the right label.

## 8. Migration / rollout

No schema change, no migration, no new switch. **This is deliberate**: ptone's
standing directive is that the refactor lands as a single cut-over with switches
collapsed, and he has twice objected to switch proliferation. A mention that
notifies is the behaviour the ruling already specifies; it does not get its own
flag.

Rollout risk is notification *volume*, and it is bounded: notifications fire only
where an agent's message text contains a token that resolves to a project human
member, and mute is honoured (`notifications.go:641`). The reversal is a code
revert, not a data migration.

## 9. Implementation phases

- **P0 — Answer F2 in writing.** Locate the convergence point of the broker and
  non-broker agent paths, with `file:line`. No code. Report before proceeding.
- **P1 — `fireMentionsForAgentMessage` helper** + unit tests for the guard
  conditions (empty key, `dm:` key, no mentions, unresolvable name).
- **P2 — Wire the non-broker path.**
- **P3 — Wire the broker path** (or the single choke point, per P0).
- **P4 — Acceptance tests** (§10).
- **P5 — Confirm the empty-`SenderID` question from §6.1 with evidence.**

## 10. Acceptance criteria

- **AC-1** An agent posts to a group conversation via `conv:<uuid>` with
  `@<unambiguous-human>` in the body; a `mention` notification row exists for
  that user.
- **AC-2** Same, with the conversation muted for that user: **no** notification.
- **AC-3** Every mention token used in tests resolves to exactly one member.
  A test asserting a fixture is unambiguous is required — see F3.
- **AC-4** An agent message with no mention token creates no mention
  notification.
- **AC-5** A mention of an *agent* slug creates no human mention notification
  (`:3495` already specifies this; pin it).
- **AC-6** The notification's sender label is the agent's `Name`, falling back to
  `Slug` — **never a UUID**. Assert the label; assert the body does not contain
  `agent.ID`.
- **AC-7** A DM (`dm:`-prefixed key) containing a mention produces exactly one
  notification, not a DM notification *plus* a mention notification.
- **AC-8** The mention fires on **both** the broker and non-broker paths, or on
  the single choke point both traverse — with a test per path.

### Verification required of the implementer

Mutation-test each new guard, and **for AC-6's negative assertion, mutate in the
direction that makes the forbidden thing present** — put the UUID back and prove
the assertion fires. A negative assertion that has not been mutated is a comment.
See `_GATE-APPARATUS.md`.

State the counting rule with every pass count.

## 11. Open questions

- **OQ-162-1** Does any `PublishChatNotification` consumer key off
  `ChatMessageContext.SenderID`? Resolves §6.1's empty-vs-`agent.ID` choice.
  Answerable by reading; assigned to P5.
- **OQ-162-2** Should a mention that resolves to *no* member be surfaced to the
  sending agent? Today it is silently dropped, which is the affordance-looks-like-
  it-worked failure. Recommend: log at info with the unresolved token, no user-
  visible error, and revisit under DEF-165. **Not blocking.**
- **OQ-162-3 (ptone)** Should an agent mentioning a human in a *project* the
  human cannot read be refused, or silently dropped? Current
  `resolveProjectHumanMembers` scopes to project members, so the question is
  already answered defensively — recorded for confirmation, not blocking.

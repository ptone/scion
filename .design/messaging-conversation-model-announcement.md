# Messaging is moving to a conversation model

Scion's messaging system is being refactored around a single idea: **a message goes to a
conversation.** This post describes the plan, what will change for people writing agents and
using the CLI, what will not change, and how the transition will land.

---

## Summary

Four headline changes:

1. **One address per message.** A message names exactly one conversation. The `--channel` and
   `--thread-id` flags go away; they become properties of the conversation rather than fields
   you set per message.
2. **Recipients can be implicit.** You will be able to send a message without naming a
   recipient. Who receives it is resolved from the conversation's own participant list.
3. **A simpler inbound contract.** Agents will answer "is this for me?" from one structured
   field instead of learning a taxonomy of message types.
4. **A smaller CLI.** `scion message` drops from 13 flags and 34 pairwise exclusion rules to
   6 flags and 3.

---

## Why

Scion grew three overlapping ways to say where a message should go, added at different times
and never reconciled: a `recipient` string with a prefix grammar, a `channel` + `thread_id`
pair, and native chat's own conversation keys. Nothing checks that they agree, so the system
routes on whichever one the executing code path happens to read.

That produces a specific set of problems that are visible in day-to-day use:

- **Messages that report success and arrive nowhere.** Sending to a user without a channel or
  thread can persist the message, attach it to no conversation, mark nothing unread, notify
  nobody — and return success.
- **Replies that go to every linked channel.** When a reply cannot determine its thread, it
  fans out to every registered integration in the project instead of failing.
- **Surfaces that cannot be addressed.** At least one registered channel name does not resolve,
  because two different identifiers are used for the same thing in different places.
- **Routing flags that are optional but required in practice.** Omitting `--thread-id` when you
  needed it is silent and easy; there is no error, only a message in the wrong place or no
  place.
- **A message type enum that mixes four unrelated concerns** — what is being asked, what
  lifecycle event occurred, who sent it, and how it was delivered — in a single field.
- **A CLI that conflates addressing, scheduling, delivery mechanics, subscriptions, attachments
  and broadcast** in one command, producing a large matrix of flag combinations that are
  mutually exclusive for reasons that are not obvious.

The common root is that there is no object representing *the place a conversation happens*.
Every feature that needed one — reply affinity, unread state, default agents — has had to
reconstruct it from whatever fields were at hand.

---

## The core idea: conversations

A **conversation** is the place a message goes. A native chat topic, a native DM, a Discord
thread, a Slack thread, a Telegram forum topic, and a linked channel are all the same kind of
thing, and all become conversations:

| Surface | Becomes |
|---|---|
| Native chat topic | a group conversation |
| Native DM | a direct conversation |
| Discord / Slack thread | a group conversation on that surface |
| Discord / Slack channel | a group conversation on that surface |
| Telegram forum topic | a group conversation on that surface |

Every conversation has an ID owned by Scion, a participant list, an optional default agent, and
a record of which platform object it corresponds to. Platform identifiers are translated at the
integration boundary and do not travel through the rest of the system.

Addressing then reduces to two fields:

- **the conversation** — required, always exactly one;
- **the addressees** — optional, and only meaningful *within* that conversation.

`channel` and `thread_id` are removed from the message and become read-only properties derived
from the conversation. This is the point of the change: it is not that the two addresses get
validated against each other, it is that there is only one address, so they cannot disagree.

---

## Sending a message

The conversation becomes a required positional argument. It cannot be forgotten, because
omitting it is a parse error rather than a silent fallback.

```
scion message <conversation> <text> [flags]
scion message --reply-to <msg-id> <text> [flags]
```

Conversations are named with a short grammar:

| Form | Means |
|---|---|
| `conv:<id>` | a conversation by ID — this is what inbound messages carry |
| `@<agent>` | your direct conversation with that agent |
| `@<email>` | your direct conversation with that user |
| `#<thread>` | a thread in the current project's space |
| `#<space>/<thread>` | a thread in another space |

A reference that does not resolve **fails the send** and lists the candidates. There is no
fallback destination anywhere in the design — in particular, nothing broadcasts because it
could not resolve a target.

### Sending without naming a recipient

Today every message must name a recipient explicitly. That will no longer be necessary, because
the conversation already knows who is in it. When no addressee is given, it is resolved from the
conversation's structure:

| Conversation | Resolves to |
|---|---|
| a direct conversation | the other participant |
| has a default agent | that agent |
| anything else | posted to everyone present; **no agent is woken** |

The third row is a real outcome rather than a failure, and it is reported distinctly from a
dispatch. It is how you say something in a room without handing anyone a task — something the
current system cannot represent at all.

The recipient is still explicit; it is declared once, when the conversation is established,
instead of being retyped on every message.

### Examples

```bash
# Reply where you were spoken to — the common case.
scion message conv:7f3a91c2 "Done. Two tests were failing; both fixed."

# Ask one participant in a busy thread to act.
scion message conv:7f3a91c2 --to @reviewer "Can you take the auth diff?"

# Direct message an agent. No thread to remember.
scion message @builder "Rebase onto main when you get a chance."

# Say something in a room without tasking anyone.
scion message #general "Heads up: staging is down for ~10 minutes."

# Answer one specific message. The conversation is implied by the message.
scion message --reply-to msg:4c81de07 "Yes — that path is already covered."
```

---

## Receiving a message

Agents currently have to recognise eight message types, several of which carry rules that exist
only in prose — which types imply action, which are informational, which should be answered by
one agent and ignored by others.

That collapses to one rule:

> **Look at `to`. If you are listed, the message is addressed to you and you are expected to
> act. If you are not listed, you are seeing it because you are in the conversation — read it,
> do not act on it.**

There is no message type you have to recognise in order to know whether you are being asked to
do something.

What is being asked is a separate, orthogonal field. Messages are either text or an event:

**Text** carries an `intent`:

| intent | Means | The agent should |
|---|---|---|
| `request` | do something | do it, then reply in the same conversation |
| `question` | answer something | answer, if listed in `to` |
| `inform` | for awareness | nothing |

**Events** are generated by Scion, never require a reply, and carry a typed body:
`agent.state-changed`, `agent.input-needed`, `delivery.failed`, `schedule.fired`,
`port.exposed`. Values that agents currently have to read out of prose — an agent's completion
status, a delivery failure reason, an exposed port's URL — become structured fields.

An inbound message looks like this:

```json
{
  "timestamp": "2026-08-23T21:06:22Z",
  "conversation": {
    "id": "conv:7f3a91c2",
    "kind": "group",
    "surface": "discord",
    "name": "#scion-dev / messaging-refactor",
    "participants": ["user:you@example.com", "agent:reviewer", "agent:writer"]
  },
  "from": "user:you@example.com",
  "to": ["agent:writer"],
  "kind": "text",
  "intent": "request",
  "msg": "Draft the design doc for the messaging refactor.",
  "visibility": "normal"
}
```

Replying means echoing `conversation.id` back. **An agent never constructs a thread or channel
ID, so it cannot omit one** — which is the direct fix for the most common routing mistake.

---

## Command changes

Concerns that are not addressing are moving out of `scion message` into their own verbs:

| Today | Becomes | Why |
|---|---|---|
| `--channel` + `--thread-id` | the conversation, required and positional | cannot be forgotten |
| `--cc` | `--to` | these are addressees, not copies |
| `--broadcast` / `--all` | `scion broadcast` | a fan-out is not a conversation |
| `--in` / `--at` | `scion schedule message` | scheduling already exists as a verb |
| `--raw` | `scion keys` | keystroke injection is not messaging |
| `--notify` | `scion notifications subscribe` | a subscription is not a message property |
| `--plain` | removed | a rendering hint that leaked into the envelope |

`--to`, `--reply-to`, `--attach`, `--visibility`, `--interrupt` and `--wake` remain.

---

## What is not changing

Deliberately out of scope:

- **Delivery semantics.** No queuing, fast-fail on a stopped agent, and the existing
  persisted → delivered/failed state model are unchanged.
- **Mention rendering.** Platform-specific `@`-syntax and identity mapping stay inside each
  integration. No shared server-side mention renderer is being introduced.
- **Federation.** Conversations remain local to one Hub deployment. No hub-to-hub addressing.
- **Transport.** The event bus, the integration plugin interface, and the runtime hop are
  untouched.
- **History import.** Linking a channel does not crawl or backfill its existing threads.

A number of known bugs are also explicitly out of scope — scrollback pagination, attachment
content transfer, and notification redelivery among them. They remain tracked separately, and
the plan treats not regressing them as a requirement.

---

## Rollout

Six phases. The first three are invisible; nothing about the contract changes until phase 3.

| Phase | Change | Reversible |
|---|---|---|
| 0 | New tables added. Nothing reads or writes them from live paths. | yes |
| 1 | Existing data backfilled into conversations. Every send starts recording a conversation alongside the existing fields. Reads unchanged. | yes |
| 2 | Reads switch to conversations. Old fields still populated, now derived. Any disagreement between old and new routing is logged and alerted on. | yes |
| 3 | New message format and new CLI. Old flags still accepted, with warnings naming their replacement. Old type values accepted and mapped. | partly |
| 4 | Integrations resolve platform identifiers at the boundary. | partly |
| 5 | Old fields and the old type enum removed. | no |

Phase 2 is the gate. It runs in production with divergence logging for a meaningful period
before phase 3 begins, because it is the last point at which the old and new models can be
compared against real traffic.

Compatibility commitments during the transition:

- Every deprecated flag keeps working through phases 3 and 4, emits a warning that names its
  replacement, and either behaves identically or fails — it never silently does something
  different.
- Old message type values are accepted and mapped to the new fields.
- No conversation history is deleted at any point. Conversations whose backing platform object
  disappears are archived, not removed.
- Messages that cannot be attributed to a conversation during backfill are flagged as inferred
  rather than assigned a plausible-looking home.

---

## Two consequences worth calling out

**Errors replace silence.** Several situations that currently succeed quietly will begin to
fail loudly: an unresolvable conversation reference, a reply to a conversation whose platform
object has been deleted, an ambiguous mention in a direct message. This is intentional. In each
case the current behaviour is to guess or to do nothing, and the failure surfaces later as a
missing message.

**Conversations are created by explicit acts, never by enumeration.** Linking a channel creates
a conversation for it immediately. Direct conversations are created when someone first sends to
them — not pre-created for every possible pair of participants. Anyone remains addressable
through `@name` whether or not a conversation with them exists yet; the conversation list
records conversations that exist, and the roster answers who can be reached.

---

## Status

The design is complete and recorded in the repository at
`.design/messaging-conversation-model.md`, alongside a companion document inventorying the
current behaviour it is based on. Implementation is planned as a sequence of independently
reviewable phases following the rollout table above.

# DEF-138 — Inbound/outbound conversation split

**Project:** ca-msg-arch
**Status:** DRAFT v2 — **redesigned on explicit routing** after ptone's objection to affinity
**Measured at:** `scion/tranche-g` = `0cff20a2b1ad5e46d4ce3e1a4cc1b7140221e651`
**Author:** ca-msg-arch (architect)
**Reproduction:** live on gteam — conversations `0c57b491` / `b2fd01b6`, messages `3e718a5a` (in) / `1f3f0e39` (reply) / `54b029ac` (probe)

> **v1 → v2.** v1 proposed remembering the inbound `thread_id` in the existing
> affinity record. ptone objected, verbatim: *"it's somewhat seems to train
> agents into a lazy approach of not actually referencing explicitly where they
> are sending messages and relying on an external affinity memory system…
> I'd broadly prefer consistent, explicit routing of messages and an error."*
>
> **He is right and v1 was chosen for the wrong reason.** Affinity won on
> migration cost — no agent change, small blast radius. Those are transition
> virtues, and I let them select the target architecture. That is the precise
> mechanism by which a refactor lands in the "hybrid state" ptone has twice
> said he does not want. Recorded as a design error of mine, not a change of
> requirements.

---

## 1. Problem & root cause

### The observation

| | conversation | kind | external_ref |
|---|---|---|---|
| inbound `3e718a5a` | `0c57b491` | group | `thread:a3083e98:1532505776013312133` |
| reply `1f3f0e39` | `b2fd01b6` | direct | `dm:agent:c9c1123b:user:b53249ea` |
| probe `54b029ac` (thread_id forced) | `0c57b491` | group | same as inbound |

The reply was **delivered to the Discord channel** but **persisted as a DM**.
The probe confirms the mechanism: forcing `thread_id` moves persistence back
onto the inbound conversation.

### Root cause — the address is under-specified

**Inbound** (`handlers_broker_inbound.go:556-564`): every plugin sets
`ThreadID` and none sets `Surface`/`ExternalRef` — Discord
`broker.go:1471-1489`, Slack `events.go:403`, Teams `activities.go:85`, web chat
`handlers_chat_v2.go:1159`. All four take the Phase 5 thread branch →
`thread:{projectID}:{threadID}`, kind `group`.

**Outbound** (`messagebroker.go:466`) branches on one field:

```go
if msg.ThreadID != "" {          // → thread conversation (group)
} else if msg.SenderID != "" && msg.RecipientID != "" {
                                 // → DM conversation (direct)
```

An agent's reply carries empty `ThreadID`. Empty ⇒ DM, always.

**The deeper problem is not the branch.** It is that an agent's reply is
addressed to a **principal** (`user:X`), and a principal does not identify a
conversation. The system is being asked a question the caller never answered,
and it answers by derivation. Improving the derivation — v1 — leaves the
address under-specified. **The fix is to complete the address.**

### Scope

Not Discord-specific. All four surfaces split their round trip identically.

---

## 2. Goals

- **G1** — An agent's reply persists into the conversation it is replying to,
  on every surface.
- **G2** — **Routing is explicit.** Where a message belongs is stated by the
  caller or derived deterministically from the caller's own address. **No
  component consults a side-table memory of "where this agent was last active."**
- **G3** — No unauthorised conversation assertion. An agent naming a
  conversation must be authorised for it.
- **G4** — Reduce the number of places that resolve a conversation.
- **G5** — A proactive send with no prior context still works without ceremony.

### Non-goals

- Removing the *existing* channel affinity (`GetLastChannel`,
  `handlers_agent_messaging.go:221-229`). It governs **delivery**, not
  identity, and ripping it out is a separate decision. **But see OQ-138-5** —
  G2's logic applies to it too, and leaving it is a deliberate deferral rather
  than an endorsement.
- Backfilling existing split history. §6.
- DEF-130's false-warning volume.

---

## 3. Proposed design — explicit conversation routing

### 3.1 The resolution rule

One rule, applied everywhere a message is persisted:

```
1. Caller named a conversation   → authorize it, then use it.        (explicit)
2. Caller named a thread         → derive thread:{project}:{thread}.  (derivation)
3. Caller named only principals  → derive dm:{kind}:{id}:{kind}:{id}. (derivation)
4. Otherwise                     → error. Do not guess.
```

Steps 2 and 3 are **derivations, not guesses**: they are pure functions of the
address the caller supplied, deterministic, and already implemented
(`derive_key.go:67-108`). Step 3 is what keeps `scion message user:X "heads up"`
working for a genuine proactive send — G5.

**What is removed is step 0, which v1 would have added:** *consult a memory of
where this agent was last seen.* No such lookup exists in this design.

### 3.2 How a reply becomes explicit without the agent remembering anything

The envelope already carries the conversation — DEF-135 put it there
(`render_delivery.go:93-101`, emitted as the `conversation` key by
`delivery.go:74-76`). The reply carries it **back**, in-band, on the request.

That is the whole difference from affinity: the conversation id travels **with
the message**, sourced from the message being replied to. There is no server
memory keyed on `(user, project, agent)` that a later message silently inherits.

Absence of the field then means something precise and useful: **"this is not a
reply."** Under rule 3 that is a proactive send and a derived DM is correct.

### 3.3 Wire change

`OutboundMessageRequest` (`handlers_agent_messaging.go:37-50`) gains one field —
verified today it has ten and none is a conversation:

```go
ConversationID string `json:"conversation_id,omitempty"`
```

The CLI `conv:<uuid>` gate (`cmd/message.go:150-161`) is opened. Its comment is
explicit that the grammar already works and only routing was missing:

> *"conv:&lt;id&gt; and #&lt;thread&gt; resolve correctly but delivery routing is not yet
> implemented — accepting them would silently drop the message."*

This design implements that routing, so the gate's stated reason expires. Help
text (`cmd/message.go:103-104`) and `SKILL.md:32,130` change with it.

### 3.4 Authorization — the gate already exists, and must not be copy-pasted

An agent asserting a conversation id is a **client assertion** and must be
authorised. This is the security-critical part of the design, and it is already
written: the sibling agent→agent handler implements the full DEF-49 block at
`handlers_agent_messaging.go:951-1044`, honouring the assertion only at `:1037`.
It requires an authenticated identity, 400s an unknown conversation, fails
closed on `(nil, nil)`, and then splits by kind — `direct` via
`CheckDMParticipantKey` (the DM key *is* the ACL), `group` via project
containment with an explicit guard against two unset project IDs comparing
equal, and unknown kinds denied.

**⚠️ It cannot be copied verbatim, and this is the most likely way to get this
wrong.** In the sibling, `agent` is the **recipient** (resolved from the URL
path at `:668`), so the group case asserts *"the conversation belongs to the
addressed agent's project."* On the outbound path, `agent` is the **sender**.
The same code would then assert something different — *"the conversation
belongs to the sending agent's project"* — which is the right check for this
direction, but it is a different claim reached by identical-looking code. The
docstring must be rewritten to say which claim is being made. A silent
copy-paste leaves a comment that describes the other direction.

`authenticatedSender` is on the prohibition list and this design **uses** it;
nothing here removes it.

### 3.5 Collapse the double resolution (G4)

`handleAgentOutboundMessage` resolves at `:307-314`, assigns at `:343`, logs
divergence at `:355-369` — then **discards the whole `storeMsg`** when a broker
is present (`:398-407`), because `structuredMsg` carries no `ConversationID`
and `store.CreateMessage` runs only on the non-broker branch at `:409`. The
surviving row is written by `deliverToUser` (`messagebroker.go:501,522`), which
resolves again.

**Confirmed empirically, not just by reading:** the probe produced *two* routing
log lines with different message ids — `4bb4d3e2` (pre-broker, absent from the
messages table) and `54b029ac` (persisted).

This defect becomes load-bearing under the new design: the explicit
`ConversationID` must reach the writer, and today the handler's result does not
survive. **The fix is the same edit as the feature** — propagate
`ConversationID` onto `structuredMsg` and have `deliverToUser` honour a
pre-resolved conversation instead of re-deriving. That collapses two
resolutions into one and is what makes explicit routing actually take effect.

---

## 4. Alternatives considered

**A — Thread/conversation affinity (design v1).** Store the inbound thread in
`webchat_conversation_context` beside `last_channel` and use it to fill an empty
`ThreadID`. **Rejected on ptone's objection, which I endorse.** It makes
`scion message user:X "hi"` mean different things at different times depending
on invisible server state; it trains agents not to state their destination; it
is a guess that is silently wrong rather than loudly absent; and it inherits the
multi-conversation ambiguity noted as OQ-138-2. Its one real merit — cheap, no
agent change — is a migration property, and migration properties should not pick
architectures. *Cost of rejecting it: 2–3× the work, and existing agents are not
fixed until they send the field.* That cost is real and is the honest price of
G2.

**B — Populate `reply_to_id` and derive the conversation from the replied-to
message.** Rejected as the mechanism, though it is the most semantically precise
— it answers "which conversation" by pointing at a concrete message rather than
an id the agent must carry. `render_delivery.go:76-80` deliberately leaves
`ReplyToID` empty and `reply_to_id` exists only on the web-chat human→agent path
(`handlers_chat_v2.go:839`). It needs the same echo plumbing as the chosen
design plus a message lookup, and it degrades badly when the replied-to message
is unavailable. **Worth revisiting** once explicit routing exists, as a
convenience layer over it rather than an alternative to it.

**C — Plugins set `Surface`/`ExternalRef` so Phase 11 runs.** Rejected — same
trap as DEF-135 Alternative B. Phase 11 keys `discord:chan:X` while the current
path keys `thread:{projectID}:X`, splitting history at the deploy boundary, here
on four surfaces at once.

**D — Error on any message without an explicit conversation.** This is ptone's
stated position taken literally, and it is **rejected only in degree**. It would
break every proactive send, including `scion message user:X "heads up"` where no
conversation exists yet and no reasonable caller could name one. The design
keeps his principle — never consult a memory — while allowing *derivation from
the caller's own address*, which is not a guess. If he wants the strict form,
rule 3 becomes an error and G5 is dropped; that is a one-line change to this
design and I would want it stated explicitly rather than assumed.

**E — Do nothing.** Rejected. The envelope names a conversation the agent cannot
reply into, which is worse than naming none: it trains every future consumer to
distrust the field.

---

## 5. DEF-139 — the detector cannot report this class of failure

Filed separately; **its priority rises under this design**, because the new
failure mode is "agent omitted the conversation" and we need something that can
see it.

`ComputeDivergenceMatch` (`divergence.go:286-340`) is tautological in **both**
branches. DM: `oldPair` = sorted{senderID, recipientID} vs ids extracted from a
ref `ResolveOrCreateDMConversation` built from those same ids. Thread:
`oldThreadID` = `msg.ThreadID` vs the threadID portion of a ref built from
`msg.ThreadID`. Both sides, both branches, same inputs. Its mismatch branches
are effectively unreachable because `OldRoutingFromMessage` and the resolver
both branch on the identical `threadID != ""` test.

**Precision:** it *can* return `no-new-routing` when resolution fails, which is
a real signal. The exact claim is that its **agreement verdicts carry no
information**.

**The codebase already contains the correct diagnosis.**
`CheckConversationConsistency`'s docstring (`divergence.go:368-372`) says:
*"Unlike ComputeDivergenceMatch (which compares routing keys derived from the
same input fields), this function queries actual persisted messages… providing a
truly independent source of truth."* That is exactly right — and it sits above
the function whose **return value is discarded at seven call sites**:
`messagebroker.go:520,720`, `handlers_broker_inbound.go:452`,
`handlers_agent_messaging.go:371,1127,1433,1615`. Meanwhile
`ComputeDivergenceMatch`'s own docstring asserts *"The comparison is
non-tautological."* **Two docstrings in one file contradict each other and the
correct one belongs to the ignored function.**

Consequence recorded as [^74]: the divergence board's `matches` count is not
evidence, and cannot serve as acceptance evidence for gteam or the fresh cutover.

---

## 6. Migration / rollout

The new field is optional; absent ⇒ rules 2/3 ⇒ today's behaviour. No schema
change, no backfill.

**Agents are not fixed until they send the field.** This is the honest cost of
G2 and it must not be papered over: between this landing and agent guidance
propagating, replies still persist as DMs. That is a *known, tracked* drift of
the kind ptone has accepted, and it is bounded — the round trip is not made
worse than today, merely not yet fixed.

Existing split history stays split (OQ-138-3). Rollback is a revert.

---

## 7. Implementation phases

| Phase | Content |
|---|---|
| **P-1** | Add `ConversationID` to `OutboundMessageRequest`; propagate onto `structuredMsg`. No behaviour change yet — nothing sets it. |
| **P-2** | Port the DEF-49 authorization block to the outbound handler. **Rewrite the group-case docstring for the sender direction** (§3.4). Deny on unauthorised assertion. |
| **P-3** | `deliverToUser` honours a pre-resolved `ConversationID` instead of re-deriving; delete the dead resolution at `:307-314`/`:343` and its duplicate divergence log. One resolution, one log line. |
| **P-4** | Open the CLI `conv:<uuid>` gate; update help text and `SKILL.md:32,130`. |
| **P-5** | DEF-139: fix both branches, correct the false docstring, consume the `CheckConversationConsistency` return at all seven sites. |
| **P-6** | Tests per §8. |

Standing constraints: never make a gate pass by weakening it; report red to me
rather than tuning it away; stage only named files, never `git add -A`; per-file
numstat before push; push to the explicit token-bearing ptone/scion URL with raw
`ls-remote` output in the report.

---

## 8. Acceptance criteria

- **AC-1 — round trip, per surface.** Discord, Slack, Teams, web chat: agent
  replies **with** the conversation from the envelope; reply's
  `conversation_id` equals the inbound's. This is what DEF-135's AC-9 should
  have been.
- **AC-2 — unauthorised assertion is denied.** An agent naming a conversation
  in another project's group, or a direct conversation it is not a principal
  of, gets 403 and **no message is persisted**. Assert the absence of the row,
  not just the status code.
- **AC-3 — no memory.** Assert that no code path consults `GetLastChannel`-style
  state to determine a **conversation**. A grep-based test is acceptable here;
  the property is structural.
- **AC-4 — proactive send still works.** `scion message user:X "hi"` with no
  prior conversation resolves a DM by derivation (rule 3), unchanged.
- **AC-5 — absent field is unchanged behaviour.** Byte-identical to today.
  Guards the upgrade path and the interim in which agents have not adopted.
- **AC-6 — one resolution, one log line.** A single reply produces exactly one
  divergence line. Today it produces two with different message ids
  (`4bb4d3e2` / `54b029ac`) — this is a measured regression target, not a
  hypothetical.
- **AC-7 — DEF-139 mutation test.** Force outbound to resolve a conversation
  inconsistent with the inbound; assert a **mismatch is reported**. Must
  compile and must fail against unfixed code — the current check passes this
  scenario, so a test that goes green on both is worthless.
- **AC-8 — consistency return consumed** at all seven call sites.
- **AC-9 — metadata assertion still ignored.** `Metadata["conversation_id"]`
  (written by `cmd/message.go:784`, read by nobody) must **not** become live as
  a side effect. That would be an unauthorised assertion path bypassing P-2.
- **AC-10** — `make test-fast` green; `pkg/hub` + `pkg/messaging` + `pkg/store`
  green excluding the two known-environmental failures.

---

## 9. Open questions

- **OQ-138-1 — CLOSED by the probe.** Setting `thread_id` moves persistence to
  the thread conversation (`54b029ac` → `0c57b491`). Delivery-side effect was
  never confirmed and is now **moot**: this design does not set `ThreadID`
  behind the caller's back.
- **OQ-138-5 (new, for ptone)** — G2's argument applies equally to the existing
  **channel** affinity (`GetLastChannel`). It is why the probe reply reached the
  right Discord channel. Should it also become explicit? My read: **not now** —
  it governs delivery, not identity, and removing it would break replies
  outright. But leaving it is a deferral, not an endorsement, and the
  inconsistency should be recorded rather than discovered later.
- **OQ-138-2** — multi-conversation agents. Under explicit routing this
  **dissolves**: the agent names the conversation. It only existed as a defect
  of the affinity design.
- **OQ-138-3 (ptone's, irreversible)** — leave split history split? My read: yes.
- **OQ-138-4 — resolved into P-4.** `SKILL.md` changes with the gate.

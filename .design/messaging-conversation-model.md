# Scion Messaging — Semantic Contract Refactor

**Status:** draft for review
**Date:** 2026-08-23
**Author:** architect agent (`ca-msg-arch`)
**Base commit:** `7d171a50`
**Companion:** `findings.md` (grounded defect inventory — read first)
**Decision on file:** Option A (Hub-owned Conversation entity covering external threads),
confirmed by @ptone 2026-08-23.

---

## 1. Problem & Goals

### Problem

Scion has three overlapping addressing schemes for "where does this message go", added at
different times and never reconciled (`findings.md` §0). The only cross-field consistency
rule in the entire system is `thread_id requires channel`. Nothing checks that the schemes
agree, so they can contradict each other and the system routes on whichever the executing
code path happens to read.

The visible consequences: messages that route nowhere and report success; replies that
broadcast to every linked channel in a project; a channel name that cannot be addressed at
all; a message type enum that mixes four unrelated concerns; and a `scion message` command
with 13 flags and 34 pairwise exclusion rules.

### Goals

| # | Goal | Success criterion |
|---|---|---|
| G1 | One primary address | A message names exactly one venue. No message can carry two addresses that disagree. |
| G2 | No silent misrouting | Every send either reaches an identified venue or fails with a specific error. The "success, but delivered nowhere" outcome is unreachable. |
| G3 | A contract an agent can state in one sentence | An agent can decide "is this for me, and what is wanted" from structured fields, without parsing prose. |
| G4 | Common model across surfaces | Native chat and every integration share one conversation abstraction, so native features degrade gracefully rather than being special-cased. |
| G5 | Collapse the flag matrix | `scion message` carries orthogonal concerns only; exclusion rules drop from 34 to a handful. |
| G6 | Machine-readable lifecycle | `status` and `visibility` reach the agent as fields, not prose. |

### Non-Goals

- **Changing delivery semantics.** No queuing, fast-fail on a stopped agent, and the
  `persisted → delivered \| failed` state model are settled and unchanged (`findings.md` §11.1).
- **Centralising mention rendering.** Platform-specific `@`-syntax and identity mapping stay
  inside each broker plugin (`findings.md` §11.3). This design does not create a shared
  hub-side mention renderer.
- **Federation.** No hub-to-hub addressability (`findings.md` §11.7). Conversations are
  local to one Hub deployment.
- **Fixing every listed bug.** `findings.md` §12 lists eight pre-existing bugs. This design
  fixes the ones that are structural (reply affinity, channel-ID mismatch, silent
  misrouting, mention fan-out) and explicitly does not address the rest (scrollback
  pagination, attachment content transfer, notification redelivery). Those remain separate
  work items and must not regress.
- **A new transport.** The event bus, broker plugin gRPC interface, and runtime broker hop
  are unchanged.

---

## 2. Proposed Design

### 2.1 The core abstraction: Conversation

A **Conversation** is the unit of addressing. It is a place where messages accumulate and
participants read them. Every message is posted to exactly one Conversation.

A Conversation is *the same kind of thing* whether it is a native chat topic, a native DM,
a Discord thread, a Slack thread, a Telegram forum topic, or a Google Chat thread. That
uniformity is the whole point of Option A: it is the common model that native features
degrade *from*.

```
Space (= Project)
  └── Conversation (kind: group)          e.g. #general, a Discord thread
  └── Conversation (kind: group)
Conversation (kind: direct)               DMs are global, not space-scoped
```

### 2.2 Data model

```go
// Conversation is the addressable venue for all messages.
type Conversation struct {
    ID        string   // hub-owned UUID, stable, never a platform ID
    ProjectID *string  // nil for direct conversations (they are global)
    Kind      ConversationKind  // direct | group
    Surface   Surface           // native | discord | slack | telegram | gchat | teams

    // ExternalRef is the platform's opaque identifier for this venue.
    // Empty for native conversations. Unique per (Surface, ExternalRef).
    ExternalRef string
    // ParentRef is the platform container the link is registered on —
    // Discord parent channel / forum, Slack channel, Telegram chat.
    // Honours the settled rule that links live on the container, not the thread.
    ParentRef string

    DisplayName    string
    DefaultAgentID *string   // exactly one place this now lives

    DriftState     DriftState // active | orphaned | unresolvable
    LastActivityAt time.Time
    CreatedAt      time.Time
    ArchivedAt     *time.Time
    DeletedAt      *time.Time
}

// ConversationParticipant records a Scion principal that can send or receive here.
// NOTE: this is NOT the platform's membership list. A Discord channel may have 500
// humans in it; only those with a linked Scion identity, plus the agents bound to
// the conversation, are participants. This distinction is load-bearing — see §2.7.
type ConversationParticipant struct {
    ConversationID string
    PrincipalKind  PrincipalKind // user | agent
    PrincipalID    string
    Role           Role          // member | observer
    JoinedAt       time.Time
    LeftAt         *time.Time
}
```

Uniqueness: `UNIQUE (surface, external_ref) WHERE external_ref <> '' AND deleted_at IS NULL`.
This index is what makes broker-edge resolution idempotent and is the single most important
constraint in the design.

### 2.3 The message envelope

`channel` and `thread_id` are **removed** from the message. They become derived, read-only
properties of the conversation. This kills the "two addresses that disagree" class outright
(G1) — it is not that we validate agreement, it is that there is only one address.

```go
type Message struct {
    ID             string
    ConversationID string   // REQUIRED. The one address.
    ReplyToID      *string  // in-conversation threading / quoting

    From       PrincipalRef  // user:<id> | agent:<id> | system:<component>
    Kind       MessageKind   // text | event

    // Exactly one of the following is populated, per Kind.
    Intent *TextIntent  // inform | request | question      (Kind == text)
    Event  *EventBody   // typed lifecycle notice           (Kind == event)

    Body        string
    Attachments []AttachmentRef
    Visibility  Visibility    // normal | verbose | full
    CreatedAt   time.Time
}

// Addressee records who, within the conversation, this message demands attention from.
// One message, N addressees — replacing today's N messages with N types.
type Addressee struct {
    MessageID     string
    PrincipalKind PrincipalKind
    PrincipalID   string
    Via           AddressedVia // explicit | body-mention | default-agent | direct
    DeliveryState DeliveryState // pending | delivered | failed
    FailureReason *string
}
```

**Addressing is two fields: the conversation, and the addressee set.**

`Recipient`, `RecipientID`, `Recipients`, `Broadcasted`, `Channel`, `ThreadID` all
disappear from the envelope. `Plain`, `Raw`, `ObserverOnly` move out of the message and
into delivery options (§2.8).

#### 2.3.1 Why `ReplyToID` does not subsume `ConversationID`

A message ID is more specific than a conversation ID, so it is reasonable to ask whether
`reply_to` alone could address a message. It cannot. The two fields answer different
questions:

| Field | Question | Required |
|---|---|---|
| `ConversationID` | Where does this go? | Always |
| `ReplyToID` | Which message am I responding to? | Never |

Four reasons the container ID has to exist independently:

1. **Cold start.** The first message in a conversation has no parent. More importantly, a
   conversation can exist with zero messages — a native DM pane is opened, or a Discord
   thread is created and nobody has spoken. Native chat must be able to render and address
   an empty conversation, and `POST /conversations/:id/messages` must have an `:id`.
2. **Mutable container state has nowhere else to live.** Participant set, default agent,
   read state, `ArchivedAt`, `DriftState`, and the `(Surface, ExternalRef)` mapping to the
   real platform thread all change over the life of a conversation. Messages are immutable
   records. Attaching mutable state to an immutable row is a category error, and it is
   unclear which message would own it.
3. **Derivation cost and fragility.** Recovering the container means walking `reply_to` to
   a root: every routing decision becomes a graph traversal instead of a column lookup, and
   the chain breaks when a middle message is deleted or redacted.
4. **Most messages are not replies.** In an active thread the common case is "the next thing
   said in the room." If `reply_to` were the address, senders would have to nominate an
   arbitrary parent — and on Discord and Slack `reply_to` maps to the native quoted-reply
   affordance, so a synthetic parent renders as a false quote to every human present.

**Invariant: `reply_to` is never a routing input.** On the wire, `conversation` is always
populated and always authoritative. `reply_to` must reference a message whose
`conversation_id` equals this message's — which is an O(1) column comparison precisely
because the container ID is stored on both rows. Remove it and the constraint becomes both
a chain walk and unvalidatable, since there is nothing to compare against.

**Ergonomic consequence (adopted).** Because a message belongs to exactly one conversation,
`reply_to → conversation` is a total function. The CLI therefore accepts:

```
scion message --reply-to <msg-id> "..."      # conversation positional omitted
```

resolving the conversation in one lookup and placing the *resolved* conversation on the
wire. This does not reintroduce the two-address failure mode: the CLI requires exactly one
of (conversation positional, `--reply-to`), there is still no implicit fallback, and
supplying both with a mismatch is an error rather than a silent preference.

### 2.4 Addressee resolution — how "no recipient named" works

This is the mechanism that delivers the requested "send with no recipient explicitly named".

When a message arrives with an empty addressee set, the Hub resolves it **from the
conversation's own structure**, in this order:

1. **`Kind == direct`** → the other participant. Recorded in
   `conversation_participants`, so it is unambiguous and auditable.
2. **`DefaultAgentID` is set** → that agent, `Via: default-agent`.
3. **Otherwise** → the message is persisted and visible to all participants, and
   **no agent is woken**. This is the ordinary "humans talking in a room" case, which the
   current system cannot represent at all.

Case 3 is a real outcome, not a failure. It is reported distinctly by the API so a caller
can tell "posted, nobody woken" from "posted, agent dispatched".

#### Reconciling with the settled "recipient must be explicit" rule

`findings.md` §11.6 records a settled decision that the outbound agent→user recipient must
always be explicit, because the old `CreatedBy` fallback was an implicit guess.

This design **satisfies that decision more strongly than the current code does**, and does
not re-open it. The distinction:

- The rejected behaviour inferred a recipient from *agent metadata* (who created the agent)
  — an implicit guess about an unrelated fact.
- This design derives the addressee from *the conversation's recorded participant set* — an
  explicit, stored, queryable fact about the venue the sender named.

The recipient is still explicit. It is declared once, when the conversation is established,
instead of being retyped on every message. That is the ergonomic win, and it is why
omitting an addressee is now safe where omitting `--thread-id` is currently catastrophic.

#### 2.4.1 Global direct conversations: resolution and failure modes

Direct conversations are **global** — one per pair of principals, `ProjectID` nil (Q2,
resolved). This keeps the familiar "one DM per person" model, but it means a DM has no
ambient project, and anything project-scoped inside it must resolve without one. Today that
resolution is a guess that returns `""` and silently disables mention resolution
(`findings.md` §9). The rule below replaces the guess.

**Candidate scope.** A project-scoped reference inside a direct conversation resolves against
the **intersection of the projects the two participants both belong to**. Not the sender's
projects; the shared set. An agent belongs to exactly one project, so any DM with an agent has
a candidate set of size ≤ 1 and is trivially unambiguous.

**Resolution outcomes**, exhaustively:

| Shared projects | Slug matches | Outcome |
|---|---|---|
| exactly 1 | 1 | Resolve. |
| N > 1 | exactly 1 across all N | Resolve. Multiple projects do not imply ambiguity. |
| N > 1 | > 1 | **Fail: `ambiguous`.** Error lists every qualified candidate. |
| N ≥ 1 | 0 | **Fail: `not-found`.** |
| 0 | — | **Fail: `no-shared-project`.** |

**Never resolve to a first match.** Ambiguity resolves to nothing. This is the same rule as
conversation references (§2.6): no fallback destination, ever.

**Disambiguation escape hatch.** A qualified mention `@<project>/<agent>` resolves directly.
Every `ambiguous` error prints the exact qualified forms that would have worked, so the error
is a fix, not a dead end.

This form *names* a project but does not address across one: both candidate projects are
already shared by both participants, and the DM itself belongs to no project. It is
disambiguation within the shared set, not a cross-project reference. (It no longer mirrors a
`#<space>/<thread>` reference form — that form is removed, §2.6.1.)

**A single message may not address agents in more than one project.** Without this, mentions
would be a way to build a cross-project venue by the back door: Alice mentions `@builder`
(project X) and `@deployer` (project Y) in one DM message, and two agents from isolated
projects are now addressees of a shared conversation, able to see each other's replies. The
shared-project intersection permits this and must be constrained explicitly. If the resolved
addressee set would span projects, the send **fails** and names the conflicting projects. Human
participants are unaffected; the rule applies to agent addressees.

**Information disclosure.** `not-found` must not distinguish "no such agent" from "an agent
with that slug exists in a project you do not share." Both produce the same message: *not
found in any project you share with `<peer>`*. Otherwise the error becomes a probe for the
existence of private projects.

**Does a failed mention fail the send?** No — and this is safe specifically because
`Kind == direct` always resolves the peer as an addressee (§2.4 case 1). A mention in a DM is
always *additive*, so an unresolved one can never mean nobody receives the message. The rule:

- The message is delivered to the peer.
- The response carries an `unresolved[]` array: `{ text, reason, candidates[] }` where
  `reason ∈ { ambiguous, not-found, no-shared-project, not-a-participant }`.
- The CLI prints each unresolved mention to stderr and **exits non-zero** (distinct code for
  *delivered with unresolved references*, separate from *send failed*). An agent that checks
  its exit status learns the fan-out did not happen. This is the difference from today, where
  the same situation exits 0 and reports nothing.

**Conversation references inside a DM.** The bare `#<thread>` form means "in the current
project's space" and therefore has no meaning in a direct conversation, which has no ambient
project. It fails naming the cause and the fix — select a project first
(`scion --project <slug> message #thread …`) — rather than resolving against an arbitrary one.
There is no qualified `#<space>/<thread>` form to fall back on; addressing a conversation in
another project is not something the system does (§2.6.1).

**`--to` inside a DM** may only name one of the two participants. Naming anyone else fails as
`not-a-participant`; a DM is not a back door to addressing a third party.

#### 2.4.2 Reconciling the two direct-conversation shapes (DEF-8, DEF-10)

*Added 2026-08-27, after the S5 survey. This section describes work that does not exist yet.*

**The defect.** Two code paths create direct conversations, using two different identity keys,
and neither can see the other's rows.

| | dual-write (`ResolveOrCreateDMConversation`) | resolver (`createDirectConversation`) |
|---|---|---|
| key | `external_ref = dm:{sorted(idA,idB)}` | the participant set |
| `ProjectID` | nil (global) — correct per Q2 | sender's project for `@<agent>`, nil for `@<email>` — **DEF-10** |
| participants | **none written** | both written |
| lookup | `UpsertConversationByExternalRef`, one indexed read | `findDirectConversation`: list the sender's conversations, then list participants of each |
| created by | every legacy send, as a stamp | `scion message @<agent>` / `@<email>` |

The lookups are asymmetric in exactly the way that prevents convergence. `findDirectConversation`
reaches rows through `conversation_participants`, so it can never see a dual-write row (which has
none). `UpsertConversationByExternalRef` keys on `external_ref`, so it can never see a resolver
row (whose ref is `""`, and which the partial unique index excludes). **One principal pair, two
conversation IDs, indefinitely.** This is what the divergence board will report once traffic runs
through both paths, and it is the reason the read switch cannot be turned on yet.

**Neither path is individually wrong.** Each is internally consistent and idempotent; there is no
row-growth bug. The defect is that they were specified in different sections and never required
to agree on what identifies a DM.

**Decision: converge on `external_ref` as the identity key.** The resolver computes
`DirectMessageExternalRef(senderID, targetID)`, creates through
`UpsertConversationByExternalRef`, and then ensures participants. `findDirectConversation` is
deleted in favour of a single indexed read. All direct conversations become global —
`ProjectID` nil — which closes DEF-10. `resolveAgentDM` keeps using the ambient project to
resolve the *agent slug*; it stops writing that project onto the conversation. Those are
different uses of the same value and conflating them is what produced DEF-10.

**Why this key and not the other.**

| Alternative | Rejected because |
|---|---|
| Converge on the participant set: dual-write calls the resolver | Dual-write is on the hot send path and the participant lookup is N+1. Worse, uniqueness would become application-enforced only — the partial unique index excludes `external_ref = ''`, so two concurrent first-sends race into two rows. It trades a DB guarantee for a code convention. |
| Keep both, add an alias table mapping the IDs | Institutionalises the split and taxes every future reader with a join. This is the "add a layer over the confusion" move; the confusion is cheap to remove now and expensive later. |
| Stop dual-write from creating DM rows; let the resolver own them | Dual-write exists so that legacy messages carry a `conversation_id` for the read switch to read. Removing it now leaves the switch with nothing. This is what phase 13 does eventually, not what unblocks the switch. |

**Migration.** Ordered, because steps 2 and 3 are not commutative.

1. **Code first, behind no flag.** Resolver computes and sets `external_ref`; sets `ProjectID`
   nil for all direct conversations; ensures participants after upsert. From this point no new
   divergent rows are created.
2. **Backfill participants onto existing `dm:` rows.** See the hazard below.
3. **Merge existing resolver rows** (`kind = direct AND external_ref = ''`). For each, derive the
   `dm:` ref from its two participants. If a row with that ref exists: re-stamp messages whose
   `conversation_id` points at the resolver row, copy any participant the target lacks, soft-delete
   the resolver row. If no such row exists: set the ref on the row in place.
4. **Guard.** A migration is not done because it ran. Assert zero rows matching
   `kind = 'direct' AND external_ref = '' AND deleted_at IS NULL`, and assert every `dm:` row has
   exactly two participants. Both as permanent tests, not as a one-off query.

**The hazard in step 2, and it is a security one.** `dm:{sorted(idA,idB)}` encodes **principal
IDs only, not principal kinds** (`pkg/messaging/divergence.go:124`). `ConversationParticipant`
requires `principal_kind`. So the backfill must look each ID up to decide whether it is a user or
an agent, and `requireParticipant` — the authorisation check for `conv:<id>` on a DM — will trust
whatever it writes. **A wrong kind is an access grant to the wrong principal.**

The rule is the one already stated for reference resolution in §2.6, applied to migration: **an
ambiguous or unresolvable ID is never guessed.** If an ID matches neither table, or both, the row
is left without participants and recorded for review. A DM with no participants fails closed —
`requireParticipant` denies everyone, including the two real parties — which is the correct
outcome for a row we cannot describe. Under-granting is recoverable; over-granting is not.

This also explains why nothing is exploitable today: every `dm:` row has zero participants, so
`conv:<id>` against one already denies everyone. The system is failing closed, not open. That is
the only reason the ungated HTTP resolve endpoint is not a live problem.

**Acceptance criteria for this work.**

- **AC-DEF8-1** Two sends to the same agent, one through the legacy path and one through
  `scion message @<agent>`, resolve to **one** conversation ID. The test asserts the row count,
  not the return value of either call.
- **AC-DEF8-2** Every direct conversation has `ProjectID` nil. A permanent test asserts this over
  rows created by both paths.
- **AC-DEF8-3** The divergence board shows non-zero matches and zero mismatches for DM traffic
  across both paths — the condition §"Divergence Monitoring" already requires before the read
  switch is enabled.
- **AC-DEF8-4** Post-migration guards from step 4 exist as permanent tests.
- **AC-DEF8-5** A backfill row whose principal kind cannot be determined produces **no
  participant** and an audit record. Mutation-verified: make the resolver ambiguous and confirm
  the named test fails.

**What this does not resolve.** `#<thread>` still cannot resolve (DEF-7 — nothing writes
`DisplayName`), the addressee table is still never written (DEF-9), and there is still no
conversation-driven delivery: `conversation_id` remains a stamp on the message row, not a routing
key. Reconciling the DM shapes makes the read switch safe to evaluate. It does not make
`conv:<id>` or `#<thread>` usable from the CLI, which is DEF-5 and depends on all three.

### 2.5 The type taxonomy

Today one enum of eight values mixes intent, lifecycle signal, provenance, and delivery
artifact (`findings.md` §4). It splits into orthogonal fields:

| Field | Values | Replaces |
|---|---|---|
| `kind` | `text`, `event` | — |
| `intent` (text only) | `inform`, `request`, `question` | `instruction`, `chat`, `assistant-reply`, addressed half of `input-needed` |
| `event.type` (event only) | `agent.state-changed`, `agent.input-needed`, `delivery.failed`, `schedule.fired`, `port.exposed` | `state-change`, `system`, broadcast half of `input-needed` |
| `from` | `user:*`, `agent:*`, `system:*` | provenance previously encoded in `assistant-reply` / `system` |
| `visibility` | `normal`, `verbose`, `full` | unchanged |

**`mention` and `group-set` cease to exist as types.** They were delivery artifacts, not
kinds of message. A message to three participants is now one row with three addressees, not
three rows with a synthetic type. This removes the structural cause of the two open mention
routing bugs (`findings.md` §12).

`EventBody` carries `status` as a first-class field, so the notification lifecycle value
(`COMPLETED`, `WAITING_FOR_INPUT`, `DELIVERY_FAILED`) is machine-readable rather than
embedded in English prose (G6).

#### A clarification this taxonomy buys for free

The current skill needs a paragraph of prose to explain that only a parent/creator should
answer `input-needed`, because peers answering causes wasted tokens and scope violations.
In the new model that rule is structural and needs no prose:

- An addressed question is `intent: question` with the answerer in `to`. You answer if you
  are in `to`.
- `ask_user` is `event: agent.input-needed` delivered to *subscribers*. Subscribers are not
  addressees. Nobody is being asked to answer.

The distinction the prose was trying to teach is now visible in the envelope.

### 2.6 Conversation references — the addressing syntax

Agents and humans need a writable form, not raw UUIDs. The grammar is closed and validated:

| Form | Resolves to |
|---|---|
| `conv:<id>` | canonical; this is what inbound envelopes carry |
| `@<agent-slug>` | the direct conversation with that agent in the current project |
| `@<email>` | the direct conversation with that user |
| `#<thread-name>` | a group conversation in the current project's space |

Unresolvable references are an error at send time with the candidate list, never a
fallback. There is no "if I cannot resolve this, broadcast" path anywhere in the design.

The critical ergonomic property: **the inbound envelope carries `conversation.id`, and
replying means echoing it back.** The agent never constructs a thread ID, so it cannot omit
one. This is the direct fix for the reported problem.

#### 2.6.1 Project isolation — references never cross a project boundary

**Messages cannot be sent between projects.** This is an existing, settled property of Scion,
and the addressing grammar must not be able to express a violation of it.

An earlier draft of this section included a `#<space>/<thread>` form for addressing a
conversation in another space. **That form is removed.** It described a capability the system
does not have and must not acquire.

The rule, applied to every form in the grammar:

| Form | Cross-project behaviour |
|---|---|
| `conv:<id>` | **Rejected** if the conversation's `ProjectID` is not the sender's current project. |
| `@<agent-slug>` | Resolves only within the current project. An agent in another project is not addressable. |
| `@<email>` | Permitted — direct conversations are global and belong to no project (§2.4.1). |
| `#<thread-name>` | Resolves only within the current project's space. |

`conv:<id>` deserves emphasis: it is a bare UUID, so unlike the `@` and `#` forms it carries no
visible project context and cannot be rejected on syntax alone. **Project scope must therefore
be enforced as an authorisation check at send time, not as a resolution failure.** A
conversation ID that leaks between projects — pasted from a log, echoed from a stale envelope,
recorded in a scratchpad — must be rejected on the strength of the *sender's* project, not
merely fail to be found. This is the single most important isolation check in the design,
because `conv:<id>` is the form agents use most.

The error must say *why*: a conversation that exists but belongs to another project reports a
boundary violation, not `not-found`. These are different conditions and conflating them makes
the failure impossible to diagnose. Subject to the disclosure rule below.

**Working in another project is a context switch, not an address.** The CLI already has a
global `--project` flag. Operating in another project means selecting it, not addressing across
a boundary:

```bash
scion --project other-project message #general "..."
```

This is not cross-project messaging: the sender's ambient project changes, and the message is
sent from within that project to a conversation inside it. The distinction matters because it
keeps the isolation invariant intact while still making every conversation reachable by someone
with the rights to it.

**Disclosure.** As in §2.4.1, a conversation in a project the sender does not belong to must
report the same error as one that does not exist. The boundary-violation error is only for
projects the sender *can* see; otherwise the error discloses the existence of private projects.

**Broadcast is unaffected and is not a counter-example.** `scion broadcast --all` fans out
across projects today. That remains true and is not a contradiction: a broadcast is not a
conversation and is deliberately outside this model (§2.9). It is a one-way fan-out to agents,
not a message sent *to a venue in another project*, and it cannot be replied to across the
boundary — a reply is an ordinary message and obeys the rule above.

### 2.7 Broker edge resolution

Brokers resolve platform identifiers to a conversation ID **at the edge** and never send
raw platform IDs inward:

```
inbound:   platform event
             → broker: ResolveConversation(surface, external_ref, parent_ref)
             → hub: upsert on UNIQUE(surface, external_ref) → conversation_id
             → POST /api/v1/messages { conversation_id, from, to[], kind, ... }

outbound:  hub publishes to conversation_id
             → spoke selected by conversation.surface (not by a message field)
             → broker maps conversation_id → external_ref → platform API call
```

Two consequences worth naming:

- **Spoke selection now reads `conversation.surface`**, a validated enum, not a free string
  on the message. The `Name` vs `ChannelID` mismatch that makes `--channel gchat`
  unaddressable (`findings.md` §3.1) cannot recur, because there is only one key.
- **Participant sets are Scion-scoped, not platform-scoped.** A Discord channel with 500
  humans yields a conversation whose participants are the linked Scion identities plus the
  bound agents. The design does not attempt to mirror platform membership, and the API
  documents this explicitly so nobody builds a presence feature on a partial list.

#### 2.7.1 What creates a conversation row (Q3, resolved)

Surface containers are created **eagerly at link time** (Q3). That decision applies to
containers that an explicit act brings into existence — it is *not* a general policy of
pre-materialising every conversation that could exist.

**The governing invariant: a conversation row is never created by enumeration.** Every row
traces to a specific act — a person linked a channel, a person opened or posted to a
conversation, or a platform delivered an event. Nothing is created by walking the cross
product of principals.

| Conversation | Created by | Cardinality bounded by |
|---|---|---|
| Surface channel (`#dev` on Discord) | the link operation | explicit link acts |
| Surface thread under a linked parent | first inbound platform event (§2.7) | actual platform activity |
| Native topic | a person creating it | explicit acts |
| Direct (DM) | first send, or a person explicitly opening the DM | explicit acts |
| *(any principal pair, speculatively)* | **never** | — |

**Why DMs are not eager.** Three reasons, in increasing order of force:

1. Cardinality is combinatorial — `users × agents + C(users,2) + C(agents,2)` — where linked
   channels are merely numerous.
2. There is no external artifact to mirror. A linked Discord channel is a real object that
   already exists; an unused (alice, bob) pair is not a thing, it is a *capability*.
3. **Agents in Scion are ephemeral.** They are spawned and destroyed continuously. Enumerating
   DM rows per agent would write to the conversation table on every spawn and leave a dead row
   for every agent that ever existed. The table would stop describing conversations and start
   describing history of the roster.

**This does not make un-materialised DMs unaddressable.** The `@` reference grammar (§2.6) is
**resolve-or-create**: `scion message @builder "..."` creates the conversation on first send,
atomically, idempotent on the normalised participant pair. Addressability comes from the
grammar; the table only records conversations that have actually been engaged.

**Roster and conversation list are different queries with different sources.**

| Question | Source | Command |
|---|---|---|
| Who can I talk to? | agent registry / project membership | `scion agents list`, project members |
| What am I talking in? | `conversations` | `scion conversations list` |

Conflating them is exactly what would reintroduce the O(N²) problem. In native chat this is
the familiar split: the "new message" picker reads the roster, the sidebar reads conversations,
and picking someone from the picker materialises a conversation on send. No new command is
needed for the roster; it already exists.

**No crawling.** Linking a Discord channel does not enumerate its existing threads. Threads
materialise when the platform delivers an event for them. Backfilling history is out of scope
(§1 Non-Goals) and would violate the no-enumeration invariant.

**Relation to §2.3.1.** That section argues the schema *must permit* conversations with zero
messages — needed for a linked-but-silent channel and for an opened-but-unused DM pane. It does
not argue that empty conversations should be manufactured. Permitting an empty conversation and
enumerating all possible ones are independent; this design does the first and refuses the
second. An empty DM the user opened and abandoned is dismissible via `ArchivedAt` without
deleting anything, since there is nothing to delete.

### 2.8 Delivery options move off the message

`Plain`, `Raw`, `ObserverOnly`, `Urgent` are transport mechanics, not properties of a
message. They move into a per-delivery options struct that is not persisted on the message
row and not part of the conversation history. `Raw` in particular is not messaging at all —
it is keystroke injection, and it becomes a separate verb (§2.9).

### 2.9 CLI surface

The current command conflates addressing, scheduling, delivery mechanics, subscription
management, attachments and broadcast, producing 34 exclusion rules. Splitting by concern:

| Verb | Concern | Removes |
|---|---|---|
| `scion message <conv> <text>` | post to a conversation | — |
| `scion broadcast <text>` | project / global fan-out (not a conversation) | ~12 rules |
| `scion schedule create …` | deferred send (exists, but see the correction below) | ~8 rules |
| `scion keys <agent> <literal>` | raw keystroke injection, local/terminal only | ~5 rules |
| `scion notifications subscribe` | subscription management (already exists) | ~2 rules |

`scion message` retains six flags: `--to`, `--attach`, `--reply-to`, `--interrupt`,
`--wake`, `--visibility`. The exclusion matrix collapses to three rules (G5).

Splitting scheduling out is *intended* to fix by construction the bug where scheduled
messages silently drop `--channel`, `--thread-id`, `--attach` and `--cc` and are re-authored as
`sender=scheduler` (`findings.md` §8): the scheduled event should store a complete envelope
including its conversation, and fire it with the original sender preserved.

> **Correction 2026-08-27 — this section named a command that does not exist, and I-1 is its
> consequence.** The earlier text read "`scion schedule message …` (already exists)". There is
> no `message` subcommand under `schedule`; the tree is `list | get | cancel | create |
> create-recurring | pause | resume | delete | history` (`cmd/schedule.go:766-774`). S4
> implemented the `--in`/`--at` deprecation warnings *faithfully against this paragraph* and
> so shipped a warning directing users at a command that has never existed. I found that in
> S5 and charged it to my verification of AC-15a; the deeper cause is here. **A design
> document that asserts a capability "already exists" is a claim about the code, and nothing
> was checking it.**
>
> What actually exists is `scion schedule create --agent <name> --message "…" --in 30m`. It
> **takes an agent, not a conversation** (`cmd/schedule.go:783-786`), so the envelope-preserving
> property this paragraph claims is unimplemented work rather than an existing capability. That
> gap is **DEF-6**.



Full proposed help text: **Appendix A**.

### 2.10 Validation

`Validate()` moves to a single choke point invoked on **every** inbound path — the CLI, the
Hub HTTP handlers, and broker-inbound alike. Today it is called on none of the hub paths
(`findings.md` §6), which is why Teams can emit a combination the validator forbids.

The validator's job shrinks considerably, because most of what it used to check is now
impossible to express: there is no channel string to regex, no thread ID to cross-check, no
recipient prefix grammar to parse. What remains is size limits, addressee membership
(every addressee must be a conversation participant), and conversation liveness.

### 2.11 Drift — the honest cost of Option A

The Hub now tracks entity lifecycle on platforms it does not control. Threads get renamed,
archived, and deleted without telling us. Designing for this from day one rather than later:

- Conversations carry `DriftState`: `active` | `orphaned` | `unresolvable`.
- **Reconciliation is lazy, never polled.** Polling every Discord thread across every
  deployment is untenable. State transitions happen on two triggers only: a send that fails
  with a platform "not found", and an inbound message that references the conversation
  (which resurrects an `orphaned` one).
- **Sends to an `orphaned` conversation fail fast with a specific error.** They do not fall
  back to the parent channel, and they do not broadcast. Fallback-on-failure is the exact
  behaviour that produces today's project-wide Discord broadcast bug.
- Orphaned conversations retain their history. They are archived, not deleted.

---

## 3. Alternatives Considered

### 3.1 Native-only Conversation; external stays an opaque tuple (Option B)

Make Conversation a first-class entity for native chat only, and keep external venues as
`(channel, thread_id)` — but as one typed, required, validated value rather than two
loosely-coupled optional fields.

**Rejected.** Much smaller blast radius and it does fix the silent-misrouting class, but it
leaves the root cause intact: two addressing systems, merely tidier. Reply affinity still
cannot remember an external thread without a bolt-on column, and there is no common model
for native features to degrade *from* — which was G4, the stated reason for doing this at
all. Presented to the owner as the explicit alternative at the decision point; Option A was
chosen with its costs understood.

### 3.2 Require and validate `channel` + `thread_id`; add no new entity

The cheapest option. Make both fields mandatory together, validate the pair against a
per-surface registry, and add a `last_thread_id` column to reply affinity.

**Rejected.** It fixes the reported symptom for roughly a tenth of the cost, and if the
goal were only "stop losing messages" it would be the right answer. But it cannot deliver
G3 or G4: `thread_id` remains a seven-way untyped union, the default agent still lives in
three places, participants remain unknowable so "who gets woken" stays unanswerable, and
the type enum still conflates delivery artifacts with message kinds. It buys a year.

### 3.3 Adopt an existing protocol as the internal model (Matrix, A2A)

Matrix's room/event model is a close conceptual fit — rooms are conversations, membership
is explicit, and bridging to external platforms is its core competence.

**Rejected.** The parts of Matrix that make it attractive are inseparable from federation:
state resolution, event DAGs, and eventual consistency across homeservers. Scion's settled
architecture has no hub-to-hub addressability (`findings.md` §11.7), so we would import
substantial complexity to model a distributed problem we have decided not to have. The
bridging patterns are worth borrowing at the design level — this proposal's broker-edge
resolution is essentially a Matrix application-service bridge — without adopting the
protocol.

### 3.4 Keep `recipient` primary; add `conversation` as metadata

Introduce Conversation as an additional, optional field for native chat features while
leaving recipient as the address.

**Rejected.** This is how the system reached its current state: `channel` and `thread_id`
were themselves added as optional qualifiers alongside a primary recipient. Adding a third
optional qualifier to the same envelope reproduces the failure mode at a larger scale.

### 3.5 No Conversation entity: the root message ID *is* the conversation ID

The email model. Every message carries `in_reply_to`; conversation identity is the ID of the
root message, and a thread is the transitive closure of the reply graph (RFC 5322
`References`). This is a real, long-lived design, and it is the strongest form of "if we have
message IDs, why do we need conversation IDs?"

It would genuinely remove an entity, and reply chains would be exactly faithful to how Discord
and Slack render quoted replies.

**Rejected**, for the reasons in §2.3.1 — chiefly:

- **Conversations must be able to exist before they contain messages.** Native chat opens a DM
  pane, and a broker registers a Discord thread, before anyone speaks. Under this model those
  states are unrepresentable, which is fatal for the native surface we are trying to
  differentiate.
- **Deleting the root destroys the identity of the whole thread.** Email tolerates this because
  identity is client-side and best-effort; we have foreign keys, read state, and participant
  rows hanging off it.
- **Container state has no owner.** Participants, default agent, and the `(Surface, ExternalRef)`
  mapping are mutable and thread-wide; the root message is immutable and has no claim to them.

Email also demonstrates the cost directly: threading is famously unreliable across clients
precisely because it is *derived* rather than *stored*, and every client re-derives it slightly
differently. That is the failure mode this proposal exists to eliminate.

The useful part of the idea is kept: `--reply-to` may be given *instead of* naming a
conversation, and the CLI resolves the container from it (§2.3.1).

---

## 4. Migration / Rollout

Six phases. Phases 0–2 are invisible to users; the contract does not change until Phase 3.

| Phase | Change | Reversible? |
|---|---|---|
| **0** | Add `conversations`, `conversation_participants`, `message_addressees` tables. No reads, no writes from live paths. | Yes — drop tables |
| **1** | Backfill (§4.1). Dual-write: every send resolves-or-creates a conversation and stamps `conversation_id` alongside existing `channel`/`thread_id`. Reads unchanged. | Yes — stop writing |
| **2** | Reads switch to `conversation_id`. Old fields still populated for compatibility, now derived from the conversation. Divergence between old and new routing is logged as an error and alerted on. | Yes — flip reads back |
| **3** | New envelope and new CLI surface. Old flags accepted with deprecation warnings that state the replacement. Old `type` values accepted and mapped. | Partially |
| **4** | Broker adapters resolve external refs at the edge. Platform IDs stop crossing the hub boundary. | Partially |
| **5** | Remove `channel`, `thread_id`, `recipient*`, and the old `type` enum. | **No** |

Phase 2 is the load-bearing gate. It should run with divergence logging in production for a
meaningful period before Phase 3 begins, because it is the last point at which the old and
new models can be compared against live traffic.

### 4.1 Backfill rules

| Existing state | Becomes |
|---|---|
| `channel=web`, `thread_id`=topic UUID | native group conversation; direct lookup in `webchat_topic` |
| `channel=web`, `thread_id=dm:…` | direct conversation; participants parsed from the key |
| `channel=web`, `thread_id=agent:<slug>` (wave-1) | direct conversation; slug resolved to agent UUID |
| `channel=<external>`, `thread_id` set | external conversation; `external_ref` = the value, `surface` = the channel; participants seeded from observed senders/recipients |
| `channel` set, `thread_id` empty | **surface-level conversation** — the channel itself as a venue. This case must be representable; a Discord channel with no threads is a legitimate conversation. |
| both empty | no conversation. Best-effort mapping to a direct conversation per (project, principal-pair), flagged `backfill_inferred`. These messages are already broken today; the flag makes that visible instead of inventing history. |

Two backfill hazards, both already present in the data:

- The wave-1 thread backfill can emit email-based DM keys that fail the UUID regex
  (`findings.md` §9). These will not parse into participants and must be routed to the
  `backfill_inferred` path rather than dropped.
- `WebChatTopic.DefaultAgent` is a slug-or-UUID union. Backfill must resolve both forms and
  normalise to a UUID; unresolvable values become `NULL` with a logged warning, not an
  arbitrary guess.

### 4.2 Compatibility during Phase 3

- Old CLI flags map mechanically: `--channel X --thread-id Y` → resolve `(X, Y)` to a
  conversation. If it resolves, proceed with a deprecation warning naming the `conv:` form.
  If it does not, **fail** — do not fall back.
- `--cc` maps to `--to`.
- Old `type` values map: `instruction`→`request`, `chat`/`assistant-reply`→`inform`,
  `input-needed`→`question` when addressed / `event:agent.input-needed` when broadcast,
  `state-change`→`event:agent.state-changed`, `system`→the matching event type by
  `system_category`, `mention`/`group-set`→dropped (the addressee set carries it).
- The `---BEGIN SCION MESSAGE---` delimiters are unchanged. Only the JSON body changes
  shape. Agents parsing by delimiter keep working; agents reading specific fields need the
  updated skill (Appendix B).

---

## 5. Open Questions

### Q1 — Mention fan-out: refinement of a settled decision — **RESOLVED 2026-08-24**

> **Resolution (ptone@google.com):** approved. One message row, N addressee rows. The
> `mention` type is removed from the taxonomy; `metadata.mention_source` is removed. Any
> "your mentions" view queries `message_addressees WHERE via = 'body-mention'` rather than
> counting message rows. Recorded in AC-9.

`findings.md` §11.2 records that body mentions produce **separate messages**, that brokers
send **separate inbound POSTs per mention**, and that bundling and hub-side parsing were
both explicitly rejected.

This design keeps one message with N addressees. The proposed reconciliation:

- Brokers still send separate POSTs. Unchanged.
- Brokers still do all mention parsing. The Hub still parses nothing. Unchanged.
- Each POST carries the platform's `external_message_id`, identical across the N posts.
- The Hub deduplicates on `(surface, external_message_id)` into **one** message row with
  **N** addressee rows.

I believe this honours both the letter and the intent of the original decision — the
rejection was of *brokers bundling* and *the hub parsing mention syntax*, neither of which
this does. But it is a change to the storage model that decision implied, so it needs an
explicit yes rather than my assumption. **If the answer is no, the design still works;**
mentions revert to N message rows and we lose the deduplicated conversation view.

### Q2 — Are direct conversations global or project-scoped? — **RESOLVED 2026-08-24**

> **Resolution (ptone@google.com):** keep direct conversations **global**, with explicitly
> specified failure modes wherever ambiguity cannot be resolved. `Conversation.ProjectID`
> stays nullable. Full resolution rules, the exhaustive outcome table, the
> `@<project>/<agent>` disambiguation form, the information-disclosure rule, and the
> exit-code behaviour are specified in **§2.4.1**. Verified by AC-21–AC-25.
>
> The governing principle: a DM's missing project is now an **explicit** condition with named
> outcomes, not a derived `""` that silently disables features.

Today native DMs are explicitly global and not project-scoped, which forces
`resolveProjectFromDMKey` to look up an agent to find a project, and returns `""` for
user↔user DMs — silently killing `@agent` mention resolution there (`findings.md` §9).

Keeping DMs global preserves current behaviour and the "one DM per pair" mental model.
Scoping them per-project fixes the mention hole and makes attachment storage
non-arbitrary — DMs currently inherit a project from whatever space was last viewed — but
changes user-visible behaviour and means the same two people have N DM threads.

I lean global-with-an-explicit-project-field-on-the-message, which fixes the mention hole
without splitting conversations. Wanted your call before I commit to it.

### Q3 — Should surface-level conversations be auto-created? — **RESOLVED 2026-08-24**

> **Resolution (ptone@google.com):** **eager** for surface containers, created at link time
> and immediately present in `scion conversations list`.
>
> Scoped in follow-up: eager applies to containers brought into existence by an explicit act.
> It does **not** imply materialising a DM per principal pair. The governing invariant —
> *no conversation row is ever created by enumeration* — the per-kind creation table, the
> resolve-or-create `@` grammar that keeps un-materialised DMs addressable, and the
> roster-vs-conversation-list separation are specified in **§2.7.1**. Verified by
> AC-26–AC-29.

When a Discord channel is linked with no threads in use, is a conversation created eagerly
at link time, or lazily on first message? Eager is more predictable and makes the channel
immediately addressable as `conv:`; lazy avoids rows for links that are never used. Low
stakes and reversible; noting it so it gets decided rather than defaulted.

---

## 6. Implementation Phases

Commit-sized, ordered, each independently reviewable.

| # | Phase | Scope |
|---|---|---|
| 1 | Schema | `conversations`, `conversation_participants`, `message_addressees` ent schemas + migrations, both dialects. No behaviour. |
| 2 | Store layer | `ConversationStore` interface + ent adapter. CRUD, upsert-on-`(surface, external_ref)`, participant management. Unit tests. |
| 3 | Resolution | `ResolveConversation` service: the `conv:`/`@`/`#` grammar, upsert semantics, `DriftState` transitions. Pure logic, heavily tested. |
| 4 | Backfill | Migration job implementing §4.1, including the two hazards. Idempotent, resumable, dry-run mode. |
| 5 | Dual-write | Send paths resolve-or-create and stamp `conversation_id`. Reads unchanged. Divergence logging. |
| 6 | Envelope | New `Message` type, `Addressee`, the split taxonomy, addressee resolution (§2.4). Old envelope still accepted and mapped. |
| 7 | Validation choke point | Single `Validate()` invoked on **every** inbound path — the CLI, the Hub HTTP handlers **including native chat**, and broker-inbound. The list is illustrative, **not exhaustive**; see AC-8 and AC-8c. Fixes the Teams violation. |
| 8 | Read switch | Reads move to `conversation_id`. Old fields derived. Lands behind a **default-off runtime flag, flippable without redeploy** (D3). ~~Gate: soak with divergence logging before proceeding.~~ **Superseded:** there is no production soak before the beta exercise, so the gate is the exercise itself, with divergence counters — including a **fallback counter** distinguishing "no disagreement" from "the new path never ran" — readable live on `GET /api/v1/admin/messaging/divergence`. |
| 9 | Delivery formatter | New agent-facing JSON (Appendix B). `status` and `visibility` delivered. Metadata allowlist removed — the fields it was smuggling are now first-class. |
| 10 | CLI split | **`scion message <conversation> <text>` — the conversation becomes a required positional argument, parsed by `ParseReference` and resolved by `Resolve` (§2.5).** Plus `scion broadcast`, `scion keys`; `scion message` reduced to six flags; deprecation mapping. **A deprecation warning may not name a replacement syntax the command cannot yet parse** — see AC-15a. |
| 11 | Broker edge | Per-plugin `ResolveConversation` at inbound; spoke selection by `conversation.surface`. One commit per plugin. |
| 12 | Docs | Skill (Appendix B), `docs-site` messaging page, GLOSSARY entries for Conversation / Surface / Addressee / Participant. Fixes the three doc-drift items in `findings.md` §10. **Documentation describes the build as it ships, not the design's end state** — anything behind a default-off flag is documented as off, and any syntax not yet parseable is not presented as available. See AC-15a; the same rule that governs a deprecation warning governs a docs page. |
| 13 | Removal | Drop `channel`, `thread_id`, `recipient*`, old type enum. **Not reversible.** **Preconditions:** the beta exercise has passed, and every replacement named in a deprecation warning has shipped and been exercised (AC-15a). Removing a field whose replacement was never reachable strands the callers the warning redirected. |

---

## 7. Acceptance Criteria

### Correctness

- **AC-1** A message cannot be sent without a resolvable conversation. Every rejection names
  the unresolved reference and lists candidates. No code path falls back to broadcast on a
  resolution failure.
- **AC-2** Sending to a `direct` conversation with no `--to` delivers to the other
  participant, and the API response names that participant. (The "no recipient" case works.)
- **AC-3** Sending to a `group` conversation with no `--to` and no default agent persists
  the message, wakes no agent, and returns a response distinguishable from a dispatch.
- **AC-4** Every message reaching an agent, on every surface, is attached to a conversation,
  updates that conversation's activity, and updates unread state. The current "persisted but
  attached to nothing" outcome is unreachable — verified by a test that asserts no message
  row can exist with a null conversation.
- **AC-5** A reply to a Discord thread returns to *that thread*. Regression test for the
  project-wide broadcast bug: with two linked channels, an untagged reply reaches exactly
  one.
- **AC-6** Every registered surface is addressable by its own name. Specific regression:
  `gchat` resolves and routes, closing the `Name`/`ChannelID` mismatch.
- **AC-7** A send to an `orphaned` conversation fails with a specific error and does not
  reach the parent container.
- **AC-8** `Validate()` is invoked on **every** inbound path, not a fixed count of them.
  Enumerated at time of writing: the CLI, each Hub HTTP handler that accepts a message
  (**including native chat**), and broker-inbound. Specific regression: the Teams
  `channel:"" + thread_id:set` combination is rejected at the boundary.
  **Verification is by mutation, not by inspection:** make the choke point fail
  unconditionally and confirm that every inbound path's tests fail. A path whose tests
  still pass is a bypass.
  > Reworded 2026-08-27. The original said "all three inbound paths", which S3 built to
  > literally and satisfied while leaving native chat unvalidated. The count was never the
  > requirement, and naming one invited the reading that the list was closed. §2.10 always
  > said "every". Do not reintroduce a number here.
- **AC-8c** Server-generated emitters that deliver an envelope to an agent — mention
  fan-out, notifications, scheduler messages — either pass through the same choke point or
  are listed as explicit exemptions with a stated reason. An unvalidated emitter that is
  merely undocumented is a defect; the current system's condition is what that accumulates
  into.
- **AC-8a** `reply_to` never affects routing. A message whose `reply_to` names a message in a
  *different* conversation is rejected. Constructing an envelope where `conversation` is
  absent but `reply_to` is present is rejected at the API boundary — resolution happens in
  the CLI, and the wire format always carries the resolved conversation.
- **AC-8b** `scion message --reply-to <id> "text"` with no positional conversation resolves
  and delivers to the containing conversation. Supplying both a positional conversation and a
  `--reply-to` that belongs elsewhere is a CLI error naming both.

### Agent surface (G3)

- **AC-9** An agent can determine "is this addressed to me" from a single structured field
  without parsing prose or inspecting metadata. A body mention produces **one** message row
  with an additional addressee row (`via = 'body-mention'`), not a second message; the
  mentioned agent and the primary addressee receive the same `message.id` and the same `to`
  list. (Q1, resolved.)
- **AC-10** `status` on lifecycle events is a structured field. No agent needs to regex the
  body to learn an agent completed.
- **AC-11** Replying to any inbound message requires echoing one field from the envelope. No
  agent constructs a thread ID.
- **AC-12** The skill's inbound-type section fits on one screen and enumerates a closed set
  (Appendix B).

### Migration

- **AC-13** Backfill is idempotent and resumable; a dry run reports counts per §4.1 rule.
- **AC-14** Through Phase 2, old and new routing agree on 100% of live traffic, or every
  divergence is logged with both decisions.
- **AC-15** Every deprecated flag emits a warning naming its replacement and either succeeds
  identically or fails — never silently changes behaviour.
- **AC-15a** A deprecation warning may only name a replacement that **works in the same
  build**. Verification: for every replacement named in a warning string, a test executes that
  replacement and asserts it reaches the intended destination.
  > Added 2026-08-27 after S4 round 1. `--channel` and `--thread-id` were made to warn "use
  > conversation references instead: `conv:<id>`, `@<agent>`, `#<thread>`" in a build where
  > `scion message` could not parse any of the three. Following the advice did not error:
  > `@builder` became the user `user:@builder` and the message went nowhere, reporting
  > success. That is findings §1.2a — the defect this design exists to remove — newly caused
  > by the migration guidance. A warning that names an unusable replacement is worse than no
  > warning, because it converts a working invocation into a silently misrouted one.
  >
  > **Amended 2026-08-27 after S5 round 1. "Every" means every — enumerate the warnings, do
  > not verify the instance that prompted the criterion.** I accepted S4 having checked only
  > the conversation-reference replacements, because those were what F-1 was about. Three
  > other warnings on the same accepted branch named replacements that do not exist:
  > `--cc → --to` (no such flag), and `--in`/`--at` → `scion schedule message` (no such
  > subcommand). **Verification is mechanical, not a reading:** a permanent test resolves
  > every replacement named in a warning string against the real command tree, and
  > `cobra.Command.Find` alone is insufficient — it returns the deepest match and leaves the
  > remainder as args, so `schedule message` resolves happily to `schedule`. The test must
  > assert the resolved command consumed the intended path.
  >
  > **Warning strings are documentation the binary emits at runtime.** Whatever check covers
  > the docs must cover them too, or the one surface a user cannot avoid reading is the one
  > surface nothing verifies.

  > **Amended 2026-08-27 after S5 round 2. The verifier must assert it verified something.**
  > The round-2 test resolved every single-quoted `'scion …'` reference correctly and was
  > load-bearing against the exact I-1 defect — and still passed with the warning emitter
  > replaced by an empty body, and still passed the whole `cmd` suite with a *new* deprecated
  > flag naming `scion agent poke`, a command that does not exist, because it was back-tick
  > quoted. Extraction that keys on a delimiter fails open. **Two requirements:** extraction
  > is delimiter-agnostic, and the test asserts a floor on the number of replacements it
  > found (>= 7 today, of ten warnings). The same floor requirement applies to any check that
  > iterates over discovered input — the docs parse-check passed green with all four of its
  > source files renamed away. A check whose input can silently become empty is not a check.

### Non-regression

- **AC-16** Fast-fail delivery is unchanged: 409 on a stopped agent, no queuing.
- **AC-17** Observer spokes still see agent↔agent traffic without re-dispatch.
- **AC-18** Visibility filtering still works across all three levels, now end to end.
- **AC-19** Project-scoped isolation holds: no conversation query crosses a project boundary
  except for global direct conversations, which carry no project data.
- **AC-20** The five `findings.md` §12 bugs that are out of scope are verified still-present
  or still-fixed as applicable, and none is newly broken.

### Direct conversations and ambiguity (Q2, §2.4.1)

- **AC-21** Each row of the §2.4.1 outcome table has a test. Specifically: two shared
  projects with a slug present in only one **resolves**; the same slug present in both
  **fails as `ambiguous`** and the error text contains both `@<project>/<agent>` forms.
- **AC-22** A user↔user DM resolves `@agent` mentions correctly. This is the direct
  regression for `resolveProjectFromDMKey` returning `""` — the current silent no-op must be
  unreachable, asserted by a test that fails if resolution ever returns empty without an
  accompanying `unresolved[]` entry.
- **AC-23** No mention ever resolves to a "first match" under ambiguity. Property test: for
  any candidate set of size > 1, the outcome is a failure, never a delivery.
- **AC-24** An unresolved mention in a DM still delivers to the peer, returns a populated
  `unresolved[]` with the correct `reason`, and exits non-zero with a code distinct from
  send-failure. A test asserts the exit codes differ.
- **AC-25** `not-found` is indistinguishable between "no such agent" and "agent exists in an
  unshared project" — asserted by comparing both error strings for equality. Bare `#<thread>`
  inside a DM fails naming the `--project` fix. `--to` naming a third party in a DM fails as
  `not-a-participant`.

### Conversation creation (Q3, §2.7.1)

- **AC-26** Linking a surface channel creates exactly one conversation, immediately listable
  and addressable as `conv:<id>` before any message exists. Linking does not enumerate
  existing platform threads.
- **AC-27** **No-enumeration invariant.** Spawning N agents into a project with M users
  creates zero conversation rows. Asserted directly: row count before and after is unchanged.
  This is the regression guard for the O(N²) failure mode.
- **AC-28** `scion message @<agent>` to a never-used pair succeeds, creating the DM on send.
  Two concurrent first-sends to the same pair produce exactly one conversation — enforced by
  the unique index on the normalised participant pair, and tested under concurrency.
- **AC-29** `scion conversations list` and the roster are separate queries. A project member
  with no conversations lists none while remaining fully addressable via `@`. Deleting an
  agent does not orphan or delete a conversation that has messages.

### Project isolation (§2.6.1)

- **AC-30** **`conv:<id>` is authorisation-checked, not merely resolved.** Sending to a valid
  conversation ID belonging to another project is rejected on the strength of the sender's
  project. Tested with a real ID from project B used by a sender in project A — the failure
  must not depend on the ID being unknown.
- **AC-31** The grammar cannot express a cross-project address. `#<space>/<thread>` is not
  accepted by the parser. `@<agent>` resolves only within the current project.
- **AC-32** A conversation that exists in a project the sender belongs to but is not currently
  scoped into reports a **boundary violation**; one in a project the sender cannot see reports
  **not-found**. Asserted by string comparison in both directions.
- **AC-33** No single message has agent addressees in more than one project. Property test over
  mention resolution in global DMs: a resolved set spanning projects is a failure, never a
  partial delivery.
- **AC-34** `scion --project <slug> message #thread` succeeds — context switching is the
  supported path and remains available.

---

## Appendix A — Proposed `scion message` usage

```
Post a message to a conversation.

Usage:
  scion message <conversation> <text> [flags]
  scion message --reply-to <msg-id> <text> [flags]

A conversation is the place a message goes: a native thread, a DM, a Discord
thread, a Slack thread. Every message goes to exactly one. Inbound messages
tell you which conversation they came from — reply by naming the same one.

Give exactly one destination: a conversation, or a message to reply to. A
message belongs to exactly one conversation, so --reply-to identifies the
destination on its own.

CONVERSATION REFERENCES
  conv:<id>              A conversation by ID. This is what inbound messages
                         carry; echoing it back is the normal way to reply.
  @<agent>               Your direct conversation with that agent.
  @<email>               Your direct conversation with that user.
  #<thread>              A thread in the current project's space.

  If a reference does not resolve, the send fails and lists candidates.
  Nothing is ever sent to a fallback destination.

  References never cross a project. To work in another project, select it —
  this is a context switch, not an address:

    scion --project other-project message #general "..."

  A conv:<id> from another project is rejected, not silently ignored.

ADDRESSING WITHIN A CONVERSATION
  Use --to when you need specific participants to act. Omit it and Scion
  resolves the addressee from the conversation itself:

    direct conversation      -> the other participant
    has a default agent      -> that agent
    otherwise                -> posted for everyone present; no agent is woken

  The third case is normal. It is how you say something in a room without
  handing anyone a task.

IN A DIRECT CONVERSATION
  A DM is global — one per pair, belonging to no project. So a bare @agent in
  the body resolves against the projects you and the other participant both
  belong to. If that is ambiguous, the send reports it and exits non-zero
  rather than picking one; write @<project>/<agent> to be exact. A bare
  #<thread> has no meaning here, since a DM has no project — select one
  with --project. A single message cannot address agents in two projects.

FLAGS
  --to <principal>       Address a participant directly (repeatable).
                         Must already be a participant.
  --reply-to <msg-id>    Reply to a specific message. Renders as a quoted reply
                         on surfaces that support it. May be used instead of
                         naming a conversation. If you name both, the message
                         must belong to that conversation.
  --attach <path>        Attach a file (repeatable). Paths under /workspace
                         or /scion-volumes.
  --visibility <level>   normal (default) | verbose | full
  --interrupt            Interrupt the addressee's harness. Use sparingly.
  --wake                 Resume a suspended addressee before delivering.

EXAMPLES
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

SEE ALSO
  scion broadcast              Send to all agents. Not a conversation.
  scion schedule create        Send later.
  scion keys                   Send literal keystrokes to a harness.
  scion notifications          Subscribe to agent lifecycle events.
```

### What changed and why

| Was | Now | Reason |
|---|---|---|
| `--channel` + `--thread-id`, both optional | the conversation, required and positional | Cannot be forgotten. Omitting it is a parse error, not a silent misroute. |
| `--cc` | `--to` | CC implied a copy. These are addressees. |
| `--broadcast` / `--all` | `scion broadcast` | Not conversations. Removes ~12 exclusion rules. |
| `--in` / `--at` | `scion schedule create --in/--at` | Removes ~8 rules. The dropped-envelope fix needs conversation addressing on scheduled events — DEF-6, not yet built. |
| `--raw` | `scion keys` | Keystroke injection, not messaging. Also honest that it is local-only. |
| `--notify` | `scion notifications subscribe` | Subscription management. |
| `--plain` | (removed) | A rendering hint that leaked into the envelope. |
| 13 flags, 34 exclusion rules | 6 flags, 3 rules | — |

---

## Appendix B — Proposed skill section: inbound messages

> Replaces the "Message types" section of
> `resources/platform_skills/scion-messaging/SKILL.md`.

````markdown
## Messages you receive

Messages arrive wrapped in `---BEGIN SCION MESSAGE---` / `---END SCION MESSAGE---`.

Two questions answer everything: **is it for me**, and **what is wanted**.

### Is it for me?

Look at `to`. If you are listed, the message is addressed to you and you are
expected to act. If you are not listed, you are seeing it because you are in
the conversation — read it, do not act on it.

That is the whole rule. There is no message type you have to recognise to
know whether you are being asked to do something.

### What is wanted?

For `kind: text`, read `intent`:

| intent    | Means                        | You should                          |
|-----------|------------------------------|-------------------------------------|
| `request` | Do something                 | Do it, then reply in the same conversation |
| `question`| Answer something             | Answer. Only if you are in `to`.    |
| `inform`  | For your awareness           | Nothing. Do not reply out of politeness. |

For `kind: event`, read `event.type`. Events are generated by Scion, not by a
person or an agent. They never require a reply.

| event.type            | Means                                  |
|-----------------------|----------------------------------------|
| `agent.state-changed` | An agent you subscribe to changed state. `event.status` holds the value. |
| `agent.input-needed`  | An agent is waiting for input. You are a subscriber, not the addressee — only answer if you are also in `to`. |
| `delivery.failed`     | A message you sent could not be delivered. `event.reason` says why. |
| `schedule.fired`      | A scheduled message you created has fired. |
| `port.exposed`        | A port in your container was exposed. `event.url` holds the address. |

### Replying

The envelope carries `conversation.id`. Reply by naming it:

```bash
scion message conv:7f3a91c2 "..."
```

You never construct a thread or channel ID. If you echo back the conversation
you were addressed in, your reply lands where the sender is looking.

To answer one specific message in a busy conversation, name the message
instead — the conversation is implied, and on surfaces that support quoted
replies it will render as a reply to that message:

```bash
scion message --reply-to msg:4c81de07 "..."
```

To reply somewhere else entirely, name that other conversation — but be
deliberate, because it will not be where the sender is watching.

### Example — a request

```json
{
  "timestamp": "2026-08-23T21:06:22Z",
  "conversation": {
    "id": "conv:7f3a91c2",
    "kind": "group",
    "surface": "discord",
    "name": "#scion-dev / messaging-refactor",
    "participants": ["user:ptone@google.com", "agent:reviewer", "agent:ca-msg-arch"]
  },
  "from": "user:ptone@google.com",
  "to": ["agent:ca-msg-arch"],
  "kind": "text",
  "intent": "request",
  "msg": "Draft the design doc for the messaging refactor.",
  "visibility": "normal"
}
```

You are in `to`, `intent` is `request`. Do the work, reply to `conv:7f3a91c2`.

### Example — an event

```json
{
  "timestamp": "2026-08-23T21:31:04Z",
  "conversation": {
    "id": "conv:1a2b8e40",
    "kind": "direct",
    "surface": "native",
    "name": "you and agent:builder"
  },
  "from": "system:notifications",
  "to": ["agent:ca-msg-arch"],
  "kind": "event",
  "event": { "type": "agent.state-changed", "subject": "agent:builder", "status": "COMPLETED" },
  "msg": "Agent builder completed its task.",
  "visibility": "normal"
}
```

`event.status` is `COMPLETED`. Read the field; do not parse the sentence.

### Example — not for you

```json
{
  "conversation": { "id": "conv:7f3a91c2", "kind": "group", "surface": "discord", "name": "#scion-dev / messaging-refactor" },
  "from": "user:ptone@google.com",
  "to": ["agent:reviewer"],
  "kind": "text",
  "intent": "request",
  "msg": "Can you review the auth diff?",
  "visibility": "normal"
}
```

You are not in `to`. Someone else was asked. Do not start reviewing the auth
diff, and do not reply.
````

### Contract lines this replaces

| Old prose the skill needed | Now |
|---|---|
| "`mention` means FYI/CC — no action unless the text directs it" | Not in `to` → not for you. No type to learn. |
| "`group-set` — act like `instruction`" | Type gone. One message, several addressees. |
| A paragraph on why only the parent should answer `input-needed` | Addressed question → you are in `to`. Broadcast → you are a subscriber. Visible in the envelope. |
| "check `metadata.system_category`" | `event.type` is a first-class field. |
| Nothing — `status` was not delivered | `event.status` is delivered. |
| "2000 character limit" (wrong; it is 16000 for agents) | One documented limit per addressee kind, generated from the constants. |

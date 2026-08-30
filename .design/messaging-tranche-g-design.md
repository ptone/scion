# Tranche G — Conversation Read Switch

Status: DRAFT for review. Author: `ca-msg-arch`. Base: `main` @ `66b5cab7f`.
Scope ruling (ptone, 19:13Z): *"i meant g more than final. something to test the behavior. but we
can review deferred items to include in the beta test."* — **G is the read switch, not a
final tranche.** The held ledger is reviewed separately for beta inclusion.

---

## 0. A correction to my own framing, made before anything was designed

I have described Tranche G all project as *"flip authorization to read from conversation
attribution."* **That description is wrong, and it is wrong in the direction that matters.**

A surface audit of `main` @ `66b5cab7f` establishes:

- **No authorization path reads a message's `conversation_id`.** Not one. The DM gate
  (`pkg/hub/handlers_chat_v2.go:3146`, `isDMParticipant`) parses the **caller-supplied key string
  from the URL path** and compares slots to the authenticated user ID. It performs no database
  access at all. Seven call sites, all access gates.
- Where a conversation *is* consulted for access (`handlers_agent_messaging.go:901-960`,
  `pkg/messaging/resolve.go:219-251`), the authority is `conv.ExternalRef` — the DM key — not the
  attribution. The conversation ID is a lookup handle.
- `conversation_id` on a message is read in exactly four roles: query scoping **after** auth,
  divergence observation, backfill idempotency, and serialization.

**The read switch is a visibility mechanism, not an authorization mechanism.** It changes *which
messages are returned*, after the caller has already been authorised by key. This reframes the
tranche: G is not "make attribution the ACL." G is "make attribution the **index**, and stop
falling back to the old index."

This matters because it changes the failure mode we are guarding against. An authorization switch
fails toward **over-permission** — the dangerous direction, and the one every rule in this project
is written for. A visibility switch fails toward **disappearance**: correctly-authorised users
silently not seeing messages they own. Under-granting is recoverable and over-granting is not, so
this is the better failure to have — but it is a different failure, and gating for the wrong one
would have produced a tranche that carefully proved the wrong property.

**Rule 672: verify what a switch actually switches before designing the gate around it.** I
carried "read switch = authorization switch" for the entire project on the strength of the name.

---

## 1. Problem & Goals

The messaging system currently maintains two parallel routing models. The old model addresses
messages by `(channel, thread_id)` or by `(sender, recipient)` pairs. The new model attributes each
message to a `conversation_id`. Since the dual-write tranches, both are populated on write.

Reads still use the old model. The switch to the new one exists —
`ops.ConversationReadSwitch()`, plumbed at `pkg/hub/operational_settings.go:1168-1186` and
`pkg/config/opsettings/sections.go:126-128` — and is consumed at exactly three sites:

| site | what it scopes |
|---|---|
| `pkg/hub/handlers_messages.go:70-78` | `handleMessages`, `?agent=` |
| `pkg/hub/handlers_messages.go:259-280` | `handleAgentMessages`, thread-scoped and DM-default |
| `pkg/hub/handlers_chat_v2.go:1782-1817` | `handleConversationHistory` |

Each currently falls back to the old filter when the conversation cannot be resolved.

**Goals.**

- **G-a.** Reads are scoped by `conversation_id` with the fallbacks removed, so that the old
  routing model is no longer load-bearing on the read path.
- **G-b.** Derivation failure on the **write** path becomes fatal (the B10 flip), so that a
  message cannot be persisted unattributed once reads depend on attribution.
- **G-c.** Evidence sufficient to decide the flip, produced by a mechanism that is **not** the
  existing divergence board.
- **G-d.** The whole thing is deployable to a test VM as one coherent branch.

**Success criterion.** On the test VM, with the switch on: every message a user could see before
the flip, they can see after. No new 4xx on legitimate sends. Any message that *would* have become
invisible is enumerated in advance, not discovered by a user.

---

## 2. Non-Goals

- **Not** making attribution the ACL. Authorization stays key-derived. Changing that is a separate,
  larger, and more dangerous piece of work, and nothing in G requires it.
- **Not** DEF-32 (federated identity link table). See §5 — G must *detect* the federated gap, and
  explicitly must **not** attempt to close it.
- **Not** the held ledger (DEF-5, 6, 9, 10, 18, 33/35, 34, 46, 47). Reviewed separately for beta.
- **Not** deleting the old routing columns or the dual-write. G makes the old path unused on read;
  removing it is a later, independently revertible step.
- **Not** flipping the switch in production. G delivers a branch and the evidence to decide.

---

## 3. The blocking risk: disappearance, and who it hits

Enabling the read switch scopes reads to `conversation_id`. **Any message whose `conversation_id`
is NULL becomes invisible to the scoped query.** The fallbacks exist precisely to prevent that, and
G removes them. So G's central safety question is: *which rows are unattributed, and who owns
them?*

Three known producers of unattributed rows survive on `main`:

**3.1 — Historical rows predating dual-write.** Addressed by DEF-12 backfill, landed in #1426.
Countable via `CountUnbackfilledMessages`. This is the tractable population.

**3.2 — Federated principals cannot be attributed at all.** There is no `(issuer, subject)` link
table (confirmed absent across all 59 Ent schemas). A federated OIDC principal's identity is a
synthetic string, `issuerURL + ":" + subject` (`pkg/hub/federation_identity_ext.go:131-132`), which
is **not a UUID**, and DM key derivation requires UUIDs on both sides. The code already knows this
and skips resolution rather than failing — `pkg/hub/notifications.go:497-501`:

```go
// SubscriberID may be a slug or federated identity rather than a UUID;
// DMConversationKey requires valid UUIDs for both parties.
if _, parseErr := uuid.Parse(sub.SubscriberID); parseErr != nil { ... skip ... }
```

**Every message involving a federated principal is therefore a permanently unattributed row, and
the read switch will hide it.** Backfill cannot repair this: the information needed to derive the
key does not exist in the database. This is DEF-32 arriving on the critical path, and it is the
single most likely way G breaks a real user on the test VM.

**Ruling: G must not attempt to attribute federated principals.** Inventing a UUID for them would
mint a DM key that is not derivable from the authenticated identity — a wrong key, and a wrong key
is worse than no key, since the key is the ACL. G instead **counts** them and **refuses to flip**
if the count is non-zero on the target instance. Closing the gap is DEF-32's job, in its own change.

**3.3 — Live swallow sites.** Twenty-one call sites in `pkg/hub` currently proceed to send when
resolution returns nil (enumerated in §4.2). Each is an ongoing producer of unattributed rows.
These are what G-b closes.

---

## 4. Proposed design

Four phases on one integration branch, `scion/tranche-g`.

### 4.1 — G1: the attribution completeness report (evidence, no behaviour change)

A read-only report, `scion server attribution-report`, answering one question: **if the switch were
flipped right now, what would disappear?**

```
Attribution completeness — project <id|ALL>
  messages total                     N
  attributed (conversation_id set)   N
  unattributed — backfillable        N   <- run 'scion server backfill --execute'
  unattributed — federated principal N   <- BLOCKS FLIP (DEF-32)
  unattributed — unresolvable        N   <- BLOCKS FLIP, enumerate below
  read-path fallbacks, last 24h      N   <- from live counters, advisory only
```

Binding constraints, carried forward from the Tranche G evidence rules:

- **READ-ONLY, enforced by test.** A mutation guard test in the style of
  `cmd/server_foreground_backfill_test.go:91`.
- **Must NOT import `messaging.DivergenceMetrics`** for its verdict. The divergence board fails
  open on exactly the population we care about — `divergence.go:312` skips prior messages whose
  `ConversationID` is empty, so unbackfilled history can never produce a mismatch — and the repo
  says so itself at `admin_messaging_divergence.go:64-66`: *"This board is NOT the Tranche G
  go/no-go input."* Live fallback counts may appear as an advisory line, clearly labelled.
- **Must reuse production key-derivation functions**, not reimplement them.
- **Must NOT normalise on the comparison path.** A differing round-trip is a finding, never a
  repair.
- **`federated` and `unresolvable` are separate buckets.** Collapsing them hides a
  permanently-unfixable population inside a fixable-looking one.
- Examples carry **IDs and keys only, never message content**.

### 4.2 — G2: the B10 flip (write path denies on derivation failure)

The non-fatal resolve contract is stated at `pkg/messaging/conversation.go:81-83`: *"On any error
the function returns nil and logs the failure. Callers MUST NOT treat a nil return as fatal."*
G2 reverses it.

The mechanism is already staged. `pkg/messaging/validate.go:70-78` documents its own inertness:

> INERTNESS UNDER B10: while derivation failures remain non-fatal (B10), every call site guards
> this behind `if convResult != nil` ... The check is therefore structural pre-placement: it cannot
> fire today, and it becomes load-bearing at Tranche G when derivation failure becomes fatal and
> the nil guard is removed.

Three call sites already reject correctly and are merely unreachable —
`handlers_agent_messaging.go:314-317`, `handlers_broker_inbound.go:368-371`,
`handlers_chat_v2.go:1186-1189`. G2 removes the nil guards that make them dead.

**Producers (`pkg/messaging`) — 21 swallow points** across `conversation.go` (`:91, :97, :114,
:128, :163, :189, :192, :201, :239, :254, :306, :309, :319`), `derive_key.go` (`:153, :170, :176,
:203`), `resolve.go` (`:377, :421, :489, :492`). Note `DeriveConversationKey` itself
(`derive_key.go:47`) is already strictly fail-closed; **every swallow is in a caller.**

**Consumers (`pkg/hub`) — the sites that must stop proceeding:**
`handlers_agent_messaging.go:291, :307, :726, :1008, :1025, :1325, :1450`;
`handlers_broker_inbound.go:258, :360`; `handlers_chat_v2.go:1178, :1305, :1416`;
`messagebroker.go:472, :653`; `notifications.go:499`.

Two must **not** be converted to hard denial and are called out to prevent a mechanical sweep:

- **`conversation.go:163`, `EnsureParticipant` failure.** Participants are a *listing* concern, not
  an access concern — authorization is key-derived. Denying a send because a listing row failed to
  write converts a cosmetic gap into an outage. Stays non-fatal; stays logged.
- **`notifications.go:499`, federated subscriber.** Denying here means federated users stop
  receiving notifications entirely. That is a regression, not a fix. Stays a skip; the population
  is counted by G1 and blocks the flip at the operator level, not per-request.

**Rule 673: a flip-to-deny sweep is not uniform. Enumerate the exceptions before the rule, or the
sweep will find them for you in production.**

### 4.3 — G3: read-switch enablement and fallback removal

At the three read sites, when the switch is on, an unresolved conversation currently falls back to
the old filter. G3 replaces fallback with an explicit, observable outcome.

The narrow decision: on unresolved-conversation, does the endpoint (a) return empty, (b) return
`409`/`503` naming the state, or (c) keep falling back but count loudly?

**Proposed: (b), a typed error, not empty.** An empty list is indistinguishable from "you have no
messages" and is exactly the silent-wrong-result class DEF-81 belonged to. A typed error is
recoverable, greppable, and cannot be mistaken for a legitimate result. This is a behaviour change
visible to the web client and needs UI acknowledgement — flagged as Open Question OQ-2.

Also in G3: `handlers_chat_v2.go:1789-1793` silently leaves `convResult` nil for any DM key that is
not exactly 5 parts. That is a parse failure masquerading as a miss and must become explicit.

### 4.4 — G4: standing gates

- **DEF-58 option A**, a Tranche G requirement: (a) a negative gate asserting `brokerIdentityImpl`
  satisfies **neither** `UserIdentity` nor `AgentIdentity`; (b) a comment recording that the
  empty-`SenderID` comparison is intentional.
- **DEF-79**: trace the production path end-to-end and record it as a test, not a doc.
- **DEF-80**: the divergence board's caveat list must state the unbackfilled fail-open explicitly.
  One paragraph; the board currently under-declares its own blindness.

---

## 5. Alternatives considered

**A5-1 — Flip the switch and rely on backfill; keep the fallbacks as the safety net.**
Rejected. Keeping the fallbacks means the old routing model stays load-bearing forever and we never
learn whether attribution is complete: every gap is silently absorbed and counted as a fallback in
a counter nobody gates on. This is the status quo with the flag flipped, and it would let us
declare victory while the property we wanted remains untested. It also leaves the federated gap
(§3.2) permanently masked.

**A5-2 — Make authorization attribution-derived at the same time.**
Rejected, and it is the alternative I would have chosen a week ago under my own wrong framing.
Authorization today is key-derived and *strictly tighter* than a participants-table scan: it checks
both kind and ID (`resolve.go:225-240`). Moving auth onto attribution would make a DM
`Conversation` the authority for participant membership, which is explicitly forbidden, and would
convert a visibility switch into an authorization switch — trading a recoverable failure
(disappearance) for an unrecoverable one (over-permission). No goal in G requires it.

**A5-3 — Attribute federated principals with a synthetic UUID derived from `issuer:subject`.**
Rejected on security grounds. It would mint a DM key not derivable from the authenticated identity.
The key is the ACL; a synthetic key is a wrong key; a wrong key is worse than no key. It would also
make DEF-32 harder later, because the synthetic rows would have to be found and unwound.

**A5-4 — Use the existing divergence board as the go/no-go.**
Rejected, and the codebase agrees with itself here. The board fails open on eight distinct
branches (`divergence.go:257, 268, 283, 292, 303, 309, 312`, plus the 50/25-row window), and
critically at `:312` it skips prior messages with an empty `ConversationID` — so the exact
population that the read switch would hide is the population the board cannot see. **A fail-open
check on missing data measures the coverage of its own inputs, not the system.**

---

## 6. Migration / rollout

One integration branch, `scion/tranche-g`, cut from `main` @ `66b5cab7f`. Phases land on the
branch, not on `main`. It reaches `main` only after test-VM validation.

This inverts the per-phase-to-`main` model used through Tranche F, and the consequence is
load-bearing: individual phases on this branch are **not** required to be independently shippable,
but something must establish the branch is coherent **at the moment of handover**, and no phase's
own gates can do that. G5 (§7) exists solely to discharge that obligation.

Ordering constraint: **G1 before G3, without exception.** Removing the read fallbacks before
knowing the unattributed population is how messages disappear on a live instance. G2 may proceed in
parallel with G1.

Test-VM sequence, for ptone:

1. Deploy the branch with `conversation_read_switch` **off**. Confirm no behaviour change.
2. Run `scion server attribution-report`. Read the buckets.
3. Run `scion server backfill` (dry-run), then `--execute`. Re-run the report.
4. **Gate:** flip only if `federated` and `unresolvable` are both zero. If either is non-zero, stop
   — that is the report doing its job, not a failure of the exercise.
5. Flip the switch. Exercise DM history, agent messages, chat search, typing, promote.
6. Rollback is flipping the switch back. It is an ops setting; no migration is involved and no
   data is destroyed.

**Revertibility.** G3 is reversible by flag. G2 is reversible by revert. G1 adds a read-only
command and is inert. Nothing in G is irreversible, which is why it is a reasonable thing to put in
front of a test VM.

---

## 7. Implementation phases

| phase | content | independently landable on `main`? |
|---|---|---|
| **G1** | `scion server attribution-report`; buckets incl. separate `federated`; read-only mutation guard test | yes |
| **G2** | B10 flip-to-deny; remove nil guards at the three staged rejection sites; **preserve the two documented exceptions** | no — needs G1's evidence to be safe |
| **G3** | Read-switch fallback removal at three sites; typed error; fix the 5-part-key silent nil | no — strictly after G1 |
| **G4** | DEF-58 negative gate; DEF-79 path trace as a test; DEF-80 caveat correction | yes |
| **G5** | Coherence pass: full gate run on the integration branch head, `gofmt -l .` included, plus a written statement of what the branch does and does not change | — |

Each phase gets a fresh developer agent. Gate list for every phase, no exceptions:
`go build ./...`, `go vet ./...`, **`gofmt -l .`**, `go test -tags no_sqlite`, `go test` (sqlite),
`golangci-lint`, and the three guard scripts from `main`.

`gofmt -l .` is on that list because its absence was the entire CI failure on #1426, and it is
absent from every brief I wrote before today.

---

## 8. Open questions

- **OQ-1 — RESOLVED (ptone, 19:55Z).** *"test VM is production data on an integration instance.
  dedicated for such testing. federated chat has not been something that has had real usage. so we
  can track the improvements for after this main chat refactor lands."*

  Production data on a dedicated integration instance. G1's buckets will be meaningful.

  **This changes the meaning of the `non-UUID principal` bucket, and strengthens the gate rather
  than relaxing it.** I had scoped that bucket as *a known gap we accept and count* — federated
  users exist, cannot be attributed, so measure them and block. If federated chat has had no real
  usage, the expected count is ~zero, and a non-zero count therefore is **not** the known gap. It
  is something we have not identified: most likely slug-form principal IDs
  (`notifications.go:497-501` names slugs alongside federated identities), possibly something
  else.

  A bucket expected to be zero is a far better gate than one expected to be small, because any
  non-zero value is a finding rather than a magnitude to argue about. G1's enumeration must
  therefore print the offending principal IDs, so the distinction between "federated" and
  "something we did not predict" is visible in the output and does not require a follow-up
  investigation to recover.

  **Rule 679: when a population you expected to be tolerable turns out to be empty, the check on
  it becomes more valuable, not less.** The instinct is to relax a gate guarding an empty set. The
  opposite is right: an empty expectation converts a counting gate into an existence gate, and
  existence gates are the strongest kind.

  **DEF-32 is explicitly deferred by ptone to after this refactor lands.** It stays a non-goal of G
  and is now a scheduled follow-up rather than an open question.
- **OQ-2 (ptone / web).** G3 makes unresolved-conversation return a typed error rather than an
  empty list. The web client must render that as a distinguishable state. If not, the alternative
  is empty-plus-loud-counter, which I like less for the reasons in §4.3.
- **OQ-3 (me, pending `ca-msg-inv4`).** The drift audit may find further `pkg/messaging` files
  behind `messaging-v2`. Anything BEHIND-BEHAVIOURAL on the resolve or derive path is a G
  precondition, not a follow-up. This design may need amending on that report.
- **OQ-4.** Group conversations pass `CheckDMParticipantKey` unconditionally
  (`pkg/messages/dm_key.go:122-124`, `convKind != "direct"` returns nil). Correct for G, since group
  access is project-ACL'd — but it means the guard's name overstates its coverage. Tracking, not
  blocking.

---

## 9. Acceptance criteria

- **AC-G-1.** `scion server attribution-report` runs against a populated database and mutates
  nothing. Proven by a mutation-guard test, not by inspection.
- **AC-G-2.** The report's `federated` bucket is a distinct count from `unresolvable`, and a
  non-zero `federated` count is stated as flip-blocking in the output itself, not only in docs.
- **AC-G-3.** The report does not import `messaging.DivergenceMetrics` for its verdict. Enforced by
  a test asserting the absence of the dependency, not by review.
- **AC-G-4.** With derivation failing, a send returns a client-visible error at all three staged
  sites; each proven by mutate/fail/revert/pass.
- **AC-G-5.** `EnsureParticipant` failure and federated-subscriber skip remain **non-fatal**, each
  with a test asserting the send still succeeds. These are the sweep's exceptions and must be
  guarded against a future mechanical fix.
- **AC-G-6.** With the switch on and all messages attributed, every read endpoint returns the same
  message set as with the switch off. Equality of sets, not of counts.
- **AC-G-7.** With the switch on and a deliberately unattributed message present, the affected
  endpoint returns the typed error — **not** an empty list.
- **AC-G-8.** A DM key that is not exactly 5 parts produces an explicit parse failure, not a silent
  nil, at `handlers_chat_v2.go:1789`.
- **AC-G-9.** DEF-58's negative gate fails if `brokerIdentityImpl` is made to satisfy either
  identity interface.
- **AC-G-10.** Full gate list green on the integration branch head, `gofmt -l .` included.
- **AC-G-11.** Rollback proven on the VM: switch off restores prior behaviour with no data change.

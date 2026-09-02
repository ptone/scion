# Addressing Specification — `scion message` and the hub send path

Status: draft for review. Author: ca-msg-arch. Date: 2026-09-02.
Supersedes nothing; this is the first written statement of the addressing contract.

Evidence base: ca-msg-addr's inventory and schema reports, instance-investigator's
measurements on gteam, and DEF-126 (observed misdelivery, 2026-09-02 19:07:26).

---

## 1. Problem & Goals

The original brief for this refactor asked for *"a clear, crisp, coherent semantic
contract"* across two agent-level messaging interfaces. Everything else in the
tranche has been about the receiving side and the storage model. **The sending
side has never had its contract written down**, and DEF-126 is what that costs.

There are nine address forms. Each was added when it was needed. No document
states, for any of them, what namespace it resolves in, whether that namespace
guarantees uniqueness, or what must happen when a token matches zero or many
things. In the absence of a stated rule, three of the nine independently invented
the same one: **take the first match and say nothing.**

### The observed failure

`scion message user:preston` from an agent container. The CLI puts the literal
token on the wire; the hub resolves it as:

```sql
SELECT ... FROM users
WHERE (LOWER(email) LIKE '%preston%' OR LOWER(display_name) LIKE '%preston%')
ORDER BY created DESC LIMIT 2 OFFSET 0;
```

Two rows matched. The newer won. The sender was operating as the older. The
message was delivered to a principal the sender was not acting as, and no
disclosure resulted **only because the same human happened to own both accounts.**

Two properties of this are worse than they first appear:

- **It is deterministic.** It will misdeliver to the same wrong principal every
  time. A flaky bug advertises itself; this one passes a second test and reads as
  intended behaviour.
- **The selection rule is anti-intuitive.** "Most recently created" systematically
  favours the newcomer. The long-standing user is the one who loses their own name
  to a new signup.

### Goals

1. Every address form has a **declared namespace** and a **declared uniqueness
   property**, both written down and both true of the storage layer.
2. Resolution is **exact**. No substring, no prefix, no fuzzy matching, anywhere
   on a send path.
3. Cardinality is **explicit**: 0 → refuse, 1 → deliver, N → refuse. Never a
   silent winner.
4. The cardinality test is evaluated against a count that **no limit can truncate**.
5. Delivered as **one hub-side change**. Confirmed feasible: the CLI transmits the
   raw token (`cmd/message.go:172-173, 386-387, 859-869`; wire body carries
   `recipient` only, `recipient_id` omitted), and the hub resolves
   (`handlers_agent_messaging.go:136-155`). A hub fix reaches every deployed CLI,
   which satisfies the single-atomic-cut-over requirement.

### Success criteria

An addressee either names exactly one principal or is refused. No code path on the
send side can select among candidates.

---

## 2. Non-Goals

- **Not** a redesign of the receive side, conversation model, or DM key derivation.
- **Not** implementing `conv:` or `#<thread>` delivery. This specifies what they
  must satisfy *if* implemented; the decision to implement is separate.
- **Not** addressing DEF-127 / DEF-127a / DEF-128. Those are read-path defects
  found alongside and are tracked separately.
- **Not** introducing a user handle in the first phase, though §5 argues it is the
  eventual answer.
- **Not** changing the Discord/Telegram/web channel routing layer. This concerns
  *who* a message is addressed to, not *how* it travels.

---

## 3. Current State

Nine forms. Uniqueness column states the **storage layer**, not what Go enforces.

| # | Form | Namespace | Unique at storage? | Today |
|---|------|-----------|--------------------|-------|
| 1 | `conv:<uuid>` | conversation id | Yes, by construction | resolves + authorizes; **no recipient derivation** |
| 2 | `#<thread>` | (project_id, kind=group, display_name) | **No index at all** | resolves first match; no delivery |
| 3 | `@<agent>` | (project_id, slug) | **Yes** — `UNIQUE(slug, project_id)` | **path-dependent** — see note [3] |

> **Note [3] — corrected 2026-09-02, measured at `97c3462ab`.** This row
> originally read "working, exact" with no path qualifier, and that was
> wrong by omission. `@<agent>` behaves differently depending on how it
> enters the system:
>
> - **Via the CLI it works.** `cmd/message.go:150` calls
>   `messaging.ParseReference` before any legacy handling;
>   `resolve.go:76-85` returns `Kind: RefAgent`, and the guard at `:154`
>   rejects only `RefConversation` and `RefThread`. `@<agent>` is the one
>   conversation reference the CLI fully supports today. It never reaches
>   the legacy resolver.
> - **Via the legacy envelope path it has never worked.** A
>   `StructuredMessage` carrying `recipient: "@name"` reaches
>   `buildPrincipalRef` (`envelope_compat.go:224-226`), which cannot derive
>   a kind from a colonless string and returns `system:@name`. Addressee
>   validation (`envelope.go:363`) then rejects it: *principal_kind must be
>   user or agent, got "system"*. Filed as **DEF-132**.
>
> **The table's real defect was structural, not factual: it had no path
> column.** Every row in it describes resolution behaviour as though a form
> has one behaviour, when a form has one behaviour *per entry point*. Rows
> 1-9 were all written from the hub-side resolver and silently assume the
> CLI is a transparent pass-through. That assumption is false for at least
> this row and has not been checked for the others. Treat any unqualified
> entry here as measured at the hub boundary only, until re-measured.

| 4 | `@<email>` | users.email | **Yes** — column-level UNIQUE | working, exact |
| 5 | `group[]` / `set[]` | mixed | inherits members' | working; user members use form 6 |
| 6 | `user:<name>` | users.email + users.display_name, **substring** | display_name has no index | **DEF-126** |
| 7 | `agent:<name>` | (project_id, slug) | Yes | working |
| 8 | `<bare-name>` | → form 7 | Yes | working |
| 9 | `<bare-email>` | → form 4 | Yes | working |

### What the schema actually guarantees

- `users.email` — column-level `UNIQUE`. Case-insensitivity is **application-layer
  only**: `normalizeEmail` (`user_store.go:54-56`) is called on `CreateUser:115`
  and `UpdateUser:193`, so stored values are lowercase, and the resolver uses
  `EmailEqualFold`. Effective uniqueness is sound. **It is not a DDL guarantee** —
  a writer that bypasses the store can insert mixed case.
- `users.display_name` — no index of any kind.
- `agents.slug` — `UNIQUE(slug, project_id)`, resolved via
  `GetAgentBySlug(projectID, slug)`, never cross-project.
- `conversations.display_name` — no index involves it. Three indexes exist:
  `UNIQUE(surface, external_ref) WHERE external_ref <> ''`, non-unique
  `project_id`, non-unique `kind`.
- **No stable unique user handle exists.** Twelve columns on `users`; none is a
  handle or username. The only unique human-readable identifier is email.

### The structural defect (DEF-126)

Seven steps, each quoted by ca-msg-addr:

1. caller passes `Limit: 1` — `handlers_agent_messaging.go:155`
2. store sets `limit = 1` — `user_store.go:290`
3. SQL issues `Limit(limit+1)` = `LIMIT 2` — `:323`, the `+1` is `hasMore` detection
4. up to 2 rows fetched — `:321-328`
5. `result.Items = items` — `:336`
6. **truncation**: `if len(items) > limit { Items = items[:1] }` — `:339-340`
7. caller tests `len(result.Items) == 1` — `handler:156`

The guard is **structurally incapable** of observing a collision: the only value it
can see is the value it accepts. Meanwhile `TotalCount` is set at `:337` from the
pre-`LIMIT` count at `:285` — the correct answer is already computed, already
returned, in the same struct, and never read.

This is not a careless line. `+1`-and-trim is a good pagination pattern, and
`len(Items) == 1` reads as a uniqueness check to anyone skimming. The defect is
invisible at the call site and only appears when the store's trimming contract is
held in mind simultaneously.

---

## 4. Proposed Design

### 4.1 The three rules

**R1 — Exact resolution.** Every form resolves by equality over a declared field.
Substring, prefix and fuzzy matching are removed from all send paths. Case folding
is permitted where the field is canonicalised on write (email only).

**R2 — Declared namespace with storage-layer uniqueness.** A form may only resolve
over a namespace whose uniqueness is guaranteed by the storage layer. If uniqueness
is not guaranteed, the form is either restricted to a namespace that is, or
disabled.

**R3 — Explicit cardinality, counted before truncation.**

```
0 matches → ADDR_UNKNOWN     (refuse)
1 match   → deliver
N matches → ADDR_AMBIGUOUS   (refuse, name the ambiguity)
```

The count is taken from `TotalCount`, never from `len(Items)`. Where the store
cannot supply an untruncated count, the query requests `Limit: 2` and refuses on
observing a second row — **one more than will be accepted**, never exactly the
accepted number.

Note the relationship between R1/R2 and R3. Under exact resolution over a unique
namespace, `ADDR_AMBIGUOUS` becomes **unreachable** rather than merely handled.
That is the stronger position, and R3 remains as defence in depth because email's
case-insensitive uniqueness is application-enforced rather than DDL-enforced (§4.5).

### 4.2 Per-form disposition

| Form | Disposition |
|------|-------------|
| `conv:<uuid>` | Unchanged. Unique by construction; authorization already sound (§4.4). |
| `#<thread>` | **Stays disabled.** Namespace has no uniqueness. §4.3. |
| `@<agent>`, `agent:<name>`, `<bare-name>` | Unchanged. `UNIQUE(slug, project_id)`, project-scoped lookup. Compliant. |
| `@<email>`, `<bare-email>` | Unchanged. Exact `GetUserByEmail`. Compliant. |
| `group[]` / `set[]` | Member resolution delegates to the fixed forms; inherits the fix. Refusal of **any** member refuses the whole addressee — no partial delivery. |
| `user:<name>` | **Restricted** to user UUID or exact email (case-folded). Display-name and substring matching removed. |

`user:<token>` after the change:

```
token is a UUID      → GetUser(id)          → 0 or 1
token contains '@'   → GetUserByEmail(fold) → 0 or 1
otherwise            → ADDR_UNKNOWN, with guidance
```

The refusal must be *useful*, because this is the form people are already using:

> `user:preston` is not a valid addressee. Address a user by exact email
> (`user:preston@example.com`) or by id. Names are not unique and cannot be
> resolved.

### 4.3 `#<thread>`

From a sender's point of view a `#<thread>` is a **group conversation's display
name within a project** — namespace `(project_id, kind=group, display_name)`.

`display_name` has no index, unique or otherwise. Two group conversations in one
project may share a name, and `resolveThread` (`resolve.go:452`) returns the first
match with no ambiguity check — the same defect class as `user:`, differing only in
matching exactly rather than by substring.

It has been protected by accident: delivery routing was never wired, so the form
errors before the defect is reachable. **Enabling it as it stands would add a third
misdelivery path.** It stays disabled until it satisfies R2, which requires a
uniqueness constraint on `(project_id, display_name)` for `kind=group`, and
therefore a dedupe migration. Fail-closed-on-N alone is insufficient here: by
Rule 1032, a name that can be duplicated by a third party is an unstable address
even when it currently resolves.

The help text must stop advertising both forms as merely "not yet supported" and
say why (§4.6).

### 4.4 `conv:<uuid>`

The gap is **recipient derivation only**, and this is the one place where the
existing code stopped in the right spot.

Authorization is already present and tight: `checkPostResolutionAuth`
(`resolve.go:192-211`) applies, for `direct` conversations, a **DM-key participant
check** — key-derived, not a participant-table lookup, which is strictly tighter —
and for `group`, project membership via `resolveConvByID`'s isolation check.
**Possession of a UUID is not sufficient.** My hypothesis that the last mile was
withheld for authorization reasons was wrong, and the check deserves recording as
correct rather than being rediscovered later.

What is missing: nothing derives a recipient set from a resolved conversation, and
no hub API exposes participant listing to agents. The chat v2 endpoint does this
for users (`handlers_chat_v2.go:770`) but is user-authenticated.

If implemented, one constraint is load-bearing and non-negotiable:

> For a `direct` conversation, the recipient set is derived **from the DM key**,
> not from the participant table.

This preserves the standing invariant that a DM `Conversation` must never become
the authority for participant membership. Group conversations derive from
participants, which is their proper authority.

### 4.5 Two schema hardenings

Neither is required for correctness today; both remove a class of future defect.

**H1 — functional unique index on `LOWER(email)`.** Email uniqueness is
DDL-enforced but its *case-insensitivity* is not. Every current writer normalises;
the guarantee holds only as long as that remains true of every writer forever. An
expression index makes it structural. Low cost, and it converts R3's defence in
depth into genuine redundancy.

**H2 — unique `(project_id, display_name)` for `kind=group`.** Prerequisite for
ever enabling `#<thread>`. Requires a dedupe migration and is therefore out of
scope for this phase; recorded so that enabling `#<thread>` cannot be treated as a
small change.

### 4.6 Truth in labelling

The help text advertises forms that error, describing them as "not yet supported"
in a way that reads as *unimplemented*. For `conv:` the resolver and the
authorization both work and only the last mile is absent; for `#<thread>` the form
is disabled and **should stay** disabled. Those are different states and the text
must distinguish them. Folded into the existing truth-in-labelling backlog.

---

## 5. Alternatives Considered

**A1 — Fix the cardinality check only; keep substring matching.**
Two lines: test `TotalCount` instead of `len(Items)`. Tempting, and it does stop
the observed misdelivery.

Rejected as a complete answer, though it is retained as the first phase. Under
substring matching, a token that resolves uniquely today can be **broken by an
unrelated third party signing up** with a matching name. An address whose meaning
depends on the rest of the population is not an address. Failing closed converts
silent misdelivery into an outage with no proximate cause and no obvious owner —
better, but not correct. It is the floor, not the fix.

**A2 — Introduce a unique user handle now.**
The ergonomic answer: `user:preston` keeps working and becomes sound. Rejected
*for this phase* on scope. It needs a column, a uniqueness constraint, a
backfill for 12+ existing users, a collision policy, an interactive claim flow,
and a UI. That is a feature, and DEF-126 is live. **This is the right long-term
destination** and §4.2's restriction is deliberately forward-compatible with it:
adding a handle later adds a branch to the same dispatch, and breaks nothing.

**A3 — Keep name matching, but make it exact rather than substring, failing closed
on collision.**
Cheapest option preserving the current ergonomics. Rejected: `users.display_name`
has no uniqueness at any layer and display names are user-editable. It would
satisfy R1 and R3 while failing R2 — and R2 is the rule that makes the other two
mean anything. It also leaves the Rule 1032 instability fully intact.

**A4 — Resolve ambiguity interactively (prompt the sender to choose).**
Rejected. The send path is used non-interactively by agents; there is no one to
prompt. An interactive disambiguator is a reasonable CLI affordance layered *above*
a hub that already refuses, but it must never be the mechanism by which ambiguity
is resolved on the wire.

**A5 — Prefer the principal in the sender's own project / most-recently-interacted.**
Rejected, and worth stating because it is the natural next idea after "most
recently created is arbitrary". Every such heuristic is a rule for **choosing among
candidates**, which is the thing that produced DEF-126. A better heuristic makes
the failure rarer and correspondingly harder to find. Goal 3 exists to forbid this
whole family.

---

## 6. Migration / Rollout

Single hub-side cut-over, no switch. Consistent with the standing directive that
no third read/write switch be introduced and that a hub upgrade flips behaviour by
default.

**Behaviour change, deliberate and user-visible:** `user:<name>` stops resolving.
Anyone using it today is relying on a form that may already be misdelivering. The
refusal message carries the working alternatives, so the failure is
self-remedying at the point of use.

**Blast radius, from gteam.** `user:<token>` with a non-email token is the only
form affected. Before landing, count invocations by form from the hub logs to size
this — and note the count is a lower bound on *breakage* but an upper bound on
*correct* deliveries, since some of those invocations were already going to the
wrong principal.

**Not reversible by config.** There is no switch, by design. Reversal is a hub
rollback, for which the existing known-good binaries are the mechanism.

---

## 7. Open Questions

- **OQ-A1.** Should `user:<uuid>` be accepted at all, or should `user:` mean
  "human-readable" and ids go through a separate form? Accepting both is
  convenient and slightly muddies the namespace. Reversible; my lean is accept.
- **OQ-A2.** For `group[]`/`set[]`, is all-or-nothing refusal correct, or should a
  partially-resolvable group deliver to the members that resolved? I specify
  all-or-nothing: partial delivery of a message addressed to a set is a silent
  change of audience. **Needs ptone's confirmation** — it is a behaviour change
  with an ergonomic cost.
- **OQ-A3.** Does the `ADDR_AMBIGUOUS` refusal name the colliding principals?
  AC-33's precedent permits naming what was rejected but forbids leaking what the
  sender was not entitled to. Under §4.2 this is nearly unreachable, so the answer
  is low-stakes — but it must be decided rather than left to whoever writes it.
- **OQ-A4.** H1 (functional unique index on `LOWER(email)`) — this phase or a
  follow-on? It is a migration, so it escalates.
- **OQ-A5.** Is `#<thread>` wanted at all? If nobody needs it, deleting the form
  is cheaper and safer than H2 plus a dedupe migration.

---

## 8. Implementation Phases

**P1 — Cardinality guard (the live defect).**
Replace `len(result.Items) == 1` with a `TotalCount`-based test at
`handlers_agent_messaging.go:154-165` and its clone at `handleGroupMessage:1494-1504`.
Refuse on 0 and on >1. Ship independently of everything else.

**P2 — Exact resolution for `user:<token>`.**
Restrict to UUID or exact email per §4.2. Remove the substring path. Refusal text
names the working forms.

**P3 — Ambiguity refusal shape.**
Typed error codes `ADDR_UNKNOWN` / `ADDR_AMBIGUOUS` / `ADDR_MALFORMED` /
`ADDR_UNSUPPORTED`, applied uniformly across all forms. Resolves OQ-A3.

**P4 — `resolveThread` fail-closed + honest help text.**
`#<thread>` stays disabled; the first-match-wins path is made to refuse regardless,
so that enabling delivery later cannot silently inherit the defect. Help text
distinguishes "disabled pending a uniqueness rule" from "last mile not wired".

**P5 (optional) — H1.**
Functional unique index on `LOWER(email)`. Migration; gated on OQ-A4.

Deliberately **not** phased here: `conv:` delivery, `#<thread>` delivery, and the
user handle. Each is a feature decision, not a defect fix.

---

## 9. Acceptance Criteria

A reviewer or QA agent should verify:

1. **AC-A1.** Two users whose display names share a token: `user:<token>` is
   refused. Test asserts the refusal, **and asserts that neither user received a
   message** — a test that only checks the error would pass against a
   send-then-error implementation.
2. **AC-A2.** One user matching: `user:<exact-email>` delivers to exactly that
   user, verified by recipient id.
3. **AC-A3.** **Mutation gate.** Reverting the guard to `len(result.Items) == 1`
   must turn AC-A1 red. The mutation must **compile** — a build failure is not a
   caught violation. This is the gate that matters: the original defect was a check
   that could not fail, so the test must prove the new check *can*.
4. **AC-A4.** No send path performs substring or `LIKE` matching on a user
   identifier. Enforce by source-scan guard, not by review — the pattern is easy to
   reintroduce and reads as harmless.
5. **AC-A5.** `group[]` containing one unresolvable member delivers to **nobody**
   and refuses naming the failure (subject to OQ-A2).
6. **AC-A6.** `#<thread>` refuses even where the underlying `resolveThread` would
   have found a unique match — the form is disabled, not merely guarded.
7. **AC-A7.** Cardinality is evaluated on a value no limit can truncate. A test
   sets the store limit to 1 with three matching rows and asserts refusal.
8. **AC-A8.** Existing behaviour for `@<agent>`, `agent:<name>`, `@<email>`,
   `<bare-name>` and `<bare-email>` is byte-identical. These forms are already
   compliant and this specification must not perturb them; a regression here is the
   most likely way the change does damage.
9. **AC-A9.** Refusal messages name a working alternative form. Asserted on
   content, since the message is the entire migration story for affected users.

---

## 10. Rules Established

- **1029** — never test cardinality against a result set already truncated. Ask for
  one more row than you will accept, or ask for the count.
- **1031** — a limit and a cardinality check must not be written by two authors at
  two layers. The store must answer "how many matched" in a field the limit cannot
  touch, and the caller must ask for it by name.
- **1032** — an identifier resolving by fuzzy or partial match is not stable; its
  meaning can change through the creation of an unrelated record. Fail-closed is
  the floor, not the fix.
- **1036** — determinism is not correctness. For an ambiguous resolver it is an
  aggravating factor: a reproducible wrong answer survives testing.

# Messaging Refactor — Implementation State

**Owner:** `ca-msg-arch` (architect, acting as implementation coordinator)
**Started:** 2026-08-27
**Integration branch:** `scion/messaging-v2` (created from `origin/main` @ `fc523ecd`)

> **READ THIS FIRST AFTER ANY COMPACTION OR RESTART.**
> This file is the authoritative record of implementation progress. The conversation
> history is not. If they disagree, this file wins — update it, do not re-derive it.
>
> **Recovery procedure:** read §1 (contract), §3 (current position), §5 (log). Then run
> `scion list` to see which managers are alive, and `git log --oneline origin/main..origin/scion/messaging-v2`
> to see what has actually landed. Reconcile §3 against those two facts before acting.

---

## 1. Standing contract — rules that do not change

1. **I do not implement.** I am the architect. I spawn engineering managers, review what
   they land, and keep this file current. I do not write production code.
2. **Managers run in sequence, not in parallel.** One active manager at a time. Not for
   workspace-safety reasons (managers have their own clones — see §6), but because the
   sections build on each other and because parallel branches onto one integration branch
   produce merge conflicts I would have to adjudicate without having written the code.
3. **Everything lands on `scion/messaging-v2`.** Never `main`. The integration branch is
   the beta-hub testing target.
4. **I never check out another branch in `/workspace`.** It is shared. Branch refs are
   created with `git branch <name> <ref>` and pushed without checkout.
5. **Phase 13 (Removal) does not land before beta validation.** It is irreversible.
6. **A section is not done until its acceptance criteria pass.** Manager says done →
   I verify against the AC list in the design → then I advance.
7. **Heartbeat:** recurring schedule `ca-msg-impl-heartbeat`, `13,43 * * * *`. On each
   beat: check the active manager is progressing, update §3, act if stalled.
8. **Report to the user at section boundaries and escalations only.** (User instruction,
   2026-08-27.) A section landing and verifying is a report. A blocker I cannot resolve is
   a report. Nothing else is — no progress notes, no interesting findings, no phase-level
   updates, no acknowledgements. Those go in §5 of this file, not to the user.
9. **Retire managers when their section closes; managers retire their own sub-agents as
   reports are captured.** The container ceiling is shared hub-wide (~50). One manager
   fans out ~6 sub-agents per round; a rejected section doubles it. Put this in every
   manager brief. I never stop another manager's children while that manager is active.
10. **Do not participate in the engineering work.** (Same instruction.) I spawn, review
   against acceptance criteria, and advance. I do not review implementation approach
   unsolicited, debug, or answer questions a manager should resolve itself. Default state
   is `blocked`.

## 2. Source documents

| Doc | Path |
|---|---|
| Design (authoritative) | `.design/messaging-conversation-model.md` |
| Findings / defect inventory | `.design/messaging-conversation-model-findings.md` |
| Community announcement | `.design/messaging-conversation-model-announcement.md` |
| Scratchpad copies | `/scion-volumes/scratchpad/projects/ca-msg-arch/` |

Design decisions already settled (do not reopen): Option A; one message row + N addressee
rows (Q1); global DMs with explicit ambiguity failures (Q2); eager surface conversations
with the no-enumeration invariant (Q3); no cross-project addressing (§2.6.1).

## 3. Current position

**Active section:** S2 — Migration (**REOPENED — reported complete, rejected on review**)
**Active manager:** `ca-msg-em2`
**Blocked on:** em2 fixing two blockers (see §5d). S3 does not start until S2 is accepted.
**Last verified landing on integration branch:** `16294728` (end of S1). S2's commits
`16294728..9e80a4e2` are merged but **not accepted** — treat the integration branch as
containing unverified work until §5d clears.
**S1 verified** 2026-08-27 01:40Z.

S1 verification (performed by me, independently of em1's report):

| Check | Method | Result |
|---|---|---|
| Builds | `go build ./...` in a detached worktree at `origin/scion/messaging-v2` | pass |
| Tests | `go test ./pkg/messaging/... ./pkg/store/...` | pass |
| **Additive only** | `git diff --name-only origin/main...` minus `pkg/ent/` and `.design/` | 11 files: 6 new in `pkg/messaging`, 3 new in `pkg/store`, plus `store.go`/`models.go` interface additions and a one-line struct embed in `composite.go`. **No live messaging path modified.** |
| D1 (UUID-only `DefaultAgentID`) | `validateDefaultAgentID` at `conversation_store.go:97`, called on all three write paths | pass |
| D2 (one normalization helper) | `pkg/messaging.NormalizeAgentRef` | pass — em2 must call it |
| Disclosure rule (AC-32) | read `resolve.go:198–218` against design §2.6.1 | pass — boundary-violation only when `senderBelongsToProject`, otherwise not-found |

I did not re-review implementation quality; em1 ran review/test/audit gates. I checked the
things that are mine: the section is additive, the standing decisions were honoured, and the
isolation semantics match the design rather than a plausible-looking approximation of them.

## 4. Section plan

Sequential. Each manager owns one section, branches off `scion/messaging-v2`, and merges
back into it.

| # | Section | Design phases | Manager | Status |
|---|---|---|---|---|
| S1 | Foundation — schema, store, resolution | 1, 2, 3 | `ca-msg-em1` | **verified** (`fc523ecd..16294728`) |
| S2 | Migration — backfill, dual-write | 4, 5 | `ca-msg-em2` | active |
| S3 | Envelope — message type, validation, delivery format | 6, 7, 9 | `ca-msg-em3` | pending |
| S4 | Surfaces — read switch, CLI split, broker edge | 8, 10, 11 | `ca-msg-em4` | pending |
| S5 | Docs — skill, docs-site, glossary | 12 | `ca-msg-em5` | pending |
| S6 | Removal — drop legacy fields | 13 | deferred | **post-beta only** |

Statuses: `pending` → `active` → `landed` → `verified`.

### Section detail

**S1 Foundation.** `conversations`, `conversation_participants`, `message_addressees` ent
schemas + dual-dialect migrations; `ConversationStore` interface + ent adapter with
upsert on `(surface, external_ref)`; `ResolveConversation` service implementing the
`conv:` / `@` / `#` grammar and `DriftState` transitions. Purely additive — no live code
path reads or writes these. Key ACs: AC-30–AC-34 (project isolation), AC-28 (concurrent
first-send uniqueness).

**S2 Migration.** Backfill per design §4.1 including both named hazards (wave-1
email-based DM keys that fail the UUID regex; `DefaultAgent` slug-or-UUID union).
Idempotent, resumable, dry-run. Then dual-write: send paths resolve-or-create and stamp
`conversation_id` alongside existing fields. Reads unchanged. Divergence logging.

**S3 Envelope.** New `Message` + `Addressee` types; the split taxonomy
(`kind`/`intent`/`event.type`); addressee resolution per §2.4; single `Validate()` choke
point on all three inbound paths; new agent-facing delivery JSON per Appendix B. Old
envelope still accepted and mapped.

**S4 Surfaces.** Read switch to `conversation_id` (gated on divergence being clean);
`scion broadcast` and `scion keys` split out, `scion message` reduced to six flags with
deprecation mapping; per-plugin `ResolveConversation` at the broker edge, one commit per
plugin.

**S5 Docs.** Skill rewrite (design Appendix B), docs-site messaging page, GLOSSARY
entries for Conversation / Surface / Addressee / Participant.

## 5. Log

Append-only. Newest last. One line per event.

**Logging policy:** only state changes get a line — a section starting or landing, a
manager spawned or stalled, a decision made, a blocker raised or cleared. A heartbeat that
finds nothing changed gets **no entry**. Over days of work an unfiltered heartbeat log
would bury the events that matter.

- `2026-08-27` Integration branch `scion/messaging-v2` created from `origin/main` @ `fc523ecd`, pushed. Working tree untouched.
- `2026-08-27` State doc created.
- `2026-08-27` Heartbeat `ca-msg-impl-heartbeat` created, `13,43 * * * *`, id `1a899567`.
- `2026-08-27` Tasks #4–#8 created, one per section S1–S5.
- `2026-08-27` `ca-msg-em1` spawned for S1. Hub-mode start clones the repo per agent
  ("Hub mode uses HTTPS clone with GITHUB_TOKEN"), so managers get their own working
  copy — the `shared-plain` concern in §6 appears to apply to the coordinator's
  `/workspace` only. Awaiting em1's confirmation before treating that as settled.
- `2026-08-27` em1 reports its own `SCION_WORKSPACE_MODE` is also `shared-plain` and
  correctly stopped before touching git. Contradicts the per-agent-clone inference above,
  so I issued a definitive test rather than reasoning about it: sentinel file
  `/workspace/.ca-msg-arch-sentinel-1787791468` written from my tree, plus a HEAD
  comparison (mine: `scion/ca-msg-arch` @ `741fd76d`). Awaiting em1's raw output.
- `2026-08-27` Decisions D1 and D2 issued to em1 (see §5a).
- `2026-08-27` **Isolation resolved: managers get their own clone.** em1's sentinel lookup
  failed and its HEAD was `scion/ca-msg-em1` @ `fc523ec` vs my `741fd76d`. **`SCION_WORKSPACE_MODE`
  reported `shared-plain` in both containers and was wrong about em1's** — it describes the
  project's configured mode, not the container's actual provisioning. Do not trust that
  variable for a spawned agent; test it. Sequencing managers remains the plan for review
  and merge-conflict reasons, but it is no longer forced by shared mutable state.
- `2026-08-27` Corrected em1: it planned to create `scion/messaging-v2` locally from main.
  The branch already exists on origin. Issued the branch contract (§5b) — work branch based
  on `origin/scion/messaging-v2`, merge in at section end, rebase forward never merge
  backwards. Harmless today (same commit) but would diverge once anything lands.
- `2026-08-27` em1 began implementation, phase 1 (schema).
- `2026-08-27 01:13Z` Heartbeat. em1 phases 1 (`d81c1093`) and 2 (`151a616e`) landed on
  `origin/scion/ca-msg-em1`; phase 3 in progress. em1 is delegating to its own developers
  (`dev-schema`, `dev-store`, `dev-resolution`) — it manages, they implement. Integration
  branch still at `origin/main`, correct for mid-section. No action taken.
- `2026-08-27 01:40Z` **S1 landed and verified.** `fc523ecd..16294728`, 7 commits. Independent
  build + test + additive-only + D1/D2 + disclosure-semantics checks all pass (see §3). em1
  reported test APPROVE, review REQUEST-CHANGES→fixed, audit APPROVE with one HIGH deferred.
  Two deferrals recorded as DEF-1 and DEF-2 in §5c. em1 released.
- `2026-08-27 01:40Z` S2 opened; `ca-msg-em2` spawned.
- `2026-08-27 02:44Z` em2 reported S2 complete, merged `16294728..9e80a4e2`, three APPROVE gates.
- `2026-08-27 02:50Z` **S2 rejected.** Two blocking findings verified in the merged code
  (B-1 duplicate DM key format, B-2 hardcoded `Match: true`), plus B-3/B-4 promoted from the
  gates' own non-blocking notes. See §5d. Section reopened, em2 sent back to fix on its
  branch and re-report. S3 held.
- `2026-08-27 03:10Z` Fleet hygiene: coordinator reports 43/50 containers. `ca-msg-em1`
  stopped and removed (S1 closed). em2 asked to reap its own 11 completed sub-agents —
  I do not remove children out from under an active manager. **Standing rule added:
  managers retire sub-agents as reports are captured, not at section end.** A manager
  fans out ~6 sub-agents per round and a rejected section doubles that; told the
  coordinator I will gate the next section on its signal if the ceiling gets tight.

## 5d. Open blockers — S2 rejection (2026-08-27 02:50Z)

S2 was reported complete with three APPROVE gates. I rejected it. Both blockers are
visible by grep and both were missed by review, test, and audit.

| # | Blocker | Evidence | Required fix | State |
|---|---|---|---|---|
| B-1 | **Two `external_ref` formats for the same DM.** `dm:%s:%s` (`divergence.go:106`, dual-write) vs `direct:%s:%s:%s` (`backfill.go:200`, with projectID). Under `UNIQUE(surface, external_ref)` the same DM gets two conversation rows — backfill fills one, live traffic fills the other, and DM history splits at the S4 read switch. Also a design-conformance bug on its own: **DMs are global** (§2.4.1, and S1 `resolve.go:310` sets `ProjectID: ""`), so a project-scoped DM key fragments one DM into one row per shared project. | grep both format strings | One exported project-free DM-key helper, called by backfill and dual-write. Thread keys keep projectID. | open |
| B-2 | **Divergence logging cannot detect divergence.** All six call sites pass `Match: true` as a literal (`handlers_agent_messaging.go:243,736,971,1076`; `messagebroker.go:467,620`). `Mismatches()` can only return 0. | grep `Match:` | Compute `Match` by resolving each model independently and comparing. "Old model has no answer" is a third outcome, not a match. | open |
| B-3 | `ProjectID` required in `BackfillConfig` (audit Medium, promoted — thread grouping can cross a project boundary; §2.6.1 is an invariant, not a recommendation). | audit report | required | open |
| B-4 | Unit tests for `ResolveOrCreateDMConversation` (test gate marked PARTIAL). It is now the shared correctness point for both phases. | test report | required | open |

**Why B-2 is the serious one.** A missing check fails to find problems. This one
*manufactures evidence of safety*: the design makes the phase-5 divergence signal the gate
for S4's read switch, and a clean soak report from this code is indistinguishable from a
real one. I would have approved the read switch on it.

**Lesson for later sections — do not let this recur.** Three independent gates approved
code containing a hardcoded comparison result and two competing constructors for one
deterministic key. Both are single-grep findings. Every manager brief from S3 onward must
require reviewers to check (a) that a comparison actually compares, and (b) that a
deterministic key has exactly one constructor.

## 5c. Deferred-item ledger — debt accepted during implementation

**Nothing leaves this table except by landing or by an explicit decision to drop it.** A
deferral agreed in one section is the single easiest thing to lose across a manager handoff:
the manager that accepted it is gone, and the manager that must honour it never heard the
conversation. This table is the only thing that carries them.

| # | Item | Deferred from | Owed by | Why deferral is safe |
|---|---|---|---|---|
| DEF-1 | **Participant-level auth on `conv:<id>`.** `resolveConvByID` checks the sender's *project* but not whether the sender is a *participant* in that conversation. Raised HIGH by S1 audit. | S1 | **S4** (surface layer, message-send time) | S1 is not wired into any live path, so the gap is not reachable. It becomes reachable the moment S4 switches reads. **S4 is not verifiable without this.** |
| DEF-2 | **AC-33** — deferred to the envelope validation layer per design. | S1 | **S3** | The validation choke point does not exist until S3 builds it. |

## 5a. Standing technical decisions made during implementation

Decisions I have issued to managers that are not in the design doc. Binding on all
sections.

| # | Decision | Rationale | Issued |
|---|---|---|---|
| D1 | `ConversationStore` accepts **UUID only** for `DefaultAgentID`, and validates it. A slug is rejected, not stored. | The slug-or-UUID union is the class of defect this refactor removes. A store that accepts both propagates the ambiguity instead of resolving it, and every downstream reader must re-ask which form it holds. A narrow store contract forces the ambiguity to be settled at a known place. | 2026-08-27 |
| D2 | Normalization (slug → UUID) lives in **one shared exported helper**, written in phase 3, with the phase 4 backfill job as an intended second caller. Not two implementations. | Duplicated identity-resolution logic is already a named defect (findings §7). Two callers exist by design; two implementations would recreate the defect inside the fix. **em2 must be pointed at this helper.** | 2026-08-27 |

## 5b. Branch contract — issued to every manager

Give this verbatim to each manager on spawn.

```
git fetch origin
git checkout -B scion/ca-msg-em<N> origin/scion/messaging-v2
```

- Base your work branch on `origin/scion/messaging-v2`. **Do not create that branch — it
  already exists on origin.** Do not base on `main`.
- Push your own work branch continuously.
- At section end, merge your branch into `scion/messaging-v2` and push the integration
  branch. That is the only time you touch it.
- If the integration branch moves while you work, **rebase your branch onto it**. Never
  merge the integration branch backwards into yours — it makes the section diff unreviewable.
- Never push `main`.

## 6. Open items / risks

- ~~**Workspace sharing.**~~ **Resolved 2026-08-27.** Managers get their own clone (Hub
  mode HTTPS-clones per agent). `SCION_WORKSPACE_MODE` is not a reliable indicator of a
  spawned agent's provisioning — it reported `shared-plain` for em1, which was false. If
  isolation matters again, test it (sentinel file + HEAD comparison), do not read the
  variable. Sequencing is retained by choice, not necessity.
- **Beta hub target.** The user wants this testable on a beta hub. Deployment mechanics
  for pointing a beta hub at `scion/messaging-v2` are not yet established — resolve
  before S4 completes.
- **Phase 8 soak gate.** The design requires divergence logging to run against real
  traffic before the read switch. On a beta hub this becomes beta soak; the gate must not
  be silently skipped just because it is inconvenient on a branch.

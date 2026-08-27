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
8a. **Reports to the user MUST be sent with the channel and thread flags, or they are not
    delivered:**
    ```
    scion message user:ptone@google.com --channel discord --thread-id 1541161053118005308 "..."
    ```
    Terminal output is invisible. Every S1 and S2 report I wrote as assistant text reached
    nobody, and I only found out because the user said so. **The user-directed cap is 2000
    runes** (agent-directed is 16000) — split long reports and number the parts.
9. **Retire managers when their section closes; managers retire their own sub-agents as
   reports are captured.** The container ceiling is shared hub-wide (~50). One manager
   fans out ~6 sub-agents per round; a rejected section doubles it. Put this in every
   manager brief. I never stop another manager's children while that manager is active.
10. **Every check ships with a test that fails when the check is removed.** Put this in
    every manager brief. A comparison with no failing test case is indistinguishable from
    a constant — that is not a theory, it is how S2 shipped `Match: true` and then shipped
    a replacement expression that was also always true. When a manager reports "now
    computed", ask for the input that makes it false. If there isn't one, it isn't computed.
11. **When an auditor's finding conflicts with a design claim, read the enforcement code.**
    S2's audit found DM `ProjectID` being treated as authorisation and was talked out of it
    with "advisory, by design". Two lines of `resolve.go` settled it the other way.
12. **Do not participate in the engineering work.** (Same instruction.) I spawn, review
   against acceptance criteria, and advance. I do not review implementation approach
   unsolicited, debug, or answer questions a manager should resolve itself. Default state
   is `blocked`.
13. **The test must observe the effect, not the call.** Rule 10's missing half, issued
    2026-08-27 09:55Z after S4 round 2. Put it in every manager brief alongside rule 10.
    F-1, G-1 and G-2 are one defect in three costumes: a mechanism that is present and
    looks correct, verified by a test that watches the mechanism being *invoked* rather
    than the outcome the user experiences. F-1 tested that a warning was emitted, not that
    its advice worked. G-1 tested auth against an identity the caller supplies. G-2 counted
    resolutions and called it delivery — `server, _, resolves := newConvRefMockHubServer(…)`,
    the send recorder discarded. Three APPROVE gates and a green suite missed a silent
    message-drop; the gates checked what they were pointed at. **Point them at effects.**
    Operationally: a test asserting a message was sent must observe the send, never the
    resolution that precedes it.

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

**Active section:** S4 — Surfaces (**REJECTED round 2 — G-1, G-2; see §5f**)
**Active manager:** `ca-msg-em4`
**Blocked on:** em4's round-3 fix. DEF-1, DEF-3 and D3 are all due from this section;
DEF-3 and D3 are now satisfied (see §5f), DEF-1 is implemented but bypassable (G-1).
**Integration branch head:** `b92926dd` (DEF-4, test-only).
**Last verified landing on integration branch:** `f206a0d9` — **S3 accepted 2026-08-27 06:40Z
on round 2.** S2 accepted 03:35Z at `cd4ee7ed` (round 3); S1 verified 01:40Z at `16294728`.

S3 round-2 verification (mine, independent of em3's report):

| Check | Method | Result |
|---|---|---|
| **E-1 fixed** | **Mutation:** forced `ValidateLegacyMessage` to return an error unconditionally in a scratch clone | `TestNativeChatPath_RejectsInvalidMessage` **failed**. Before the fix, the same mutation left every chat test passing. The path now genuinely reaches the choke point. |
| **AC-8 as reworded — every path, not a count** | Diffed the full `pkg/hub` failure set mutated vs clean, plus `go test ./cmd/...` | All seven claimed paths have at least one test that fails only under mutation: `TestHandleAgentMessage_*` (hub handler), `TestHandleBrokerInbound_*` (broker inbound), `TestNativeChatPath_*` + `TestChatV2_Send_*` (native chat), `TestOutboundMessage_*` (hub outbound), `TestHandleProjectBroadcast_*` (hub broadcast); `./cmd` fails under mutation (both CLI paths). **No path survives the mutation.** |
| Attachment-only relaxation (`c1acaf86`) — is the check being widened until tests pass? | read the diff | **No.** `msg.Msg = "[attachment]"` is guarded by `len(msg.Attachments) > 0`; persistence uses `storeMsg.Msg = content` and dispatch passes `content`, so the synthetic body exists only for the validation call and never reaches the store or the agent. A message with neither text nor attachments is still rejected upstream at `handlers_chat_v2.go:795`. |
| Documented exemptions (AC-8c) | read `pkg/messaging/VALIDATION_EXEMPTIONS.md` | three server-generated emitters listed with reasons and a stated re-entry condition |

I checked the relaxation specifically because "wire in a check, then loosen the check" is the
shape S2's B-2 took. Here it is not that: the loosening is conditioned on the exact case that
made it necessary, and it does not touch what is stored or delivered.

**Found during verification, not S3's fault — see DEF-4.** The `pkg/hub` suite is
progressively failing on the integration branch: `origin/main` 0 failures (3 runs),
`cd4ee7ed` 5, `d9fc7f51` 18, `f206a0d9` 17–19, with **non-deterministic membership**
(two consecutive runs shared only 2 failures). Every failure is SQLite
`out of memory (7)` at test-store creation, with 109 GB free on the host. This predates
S3 and I did not catch it when I accepted S2 — that is my miss.

S2 round-3 verification (mine, independent of em2's report):

| Check | Result |
|---|---|
| Build + `pkg/messaging`, `pkg/store` tests | pass |
| C-1 comparison is non-degenerate | `ComputeDivergenceMatch` now takes `actualExternalRef` read back from the DB; pair/thread/type mismatches are all reachable outcomes |
| **Mutation test** — rule 10 enforced by hand | replaced `oldPair == newPair` with `true`; `TestComputeDivergenceMatch_GenuineDisagreement` **failed**. The check is load-bearing. |
| C-2 thread dual-write | `ResolveOrCreateThreadConversation` wired at all six sites; `deliverToAgent` gap caught by em2's own audit and fixed pre-merge |
| C-3 DM `ProjectID` | parameter removed; DMs created with nil `ProjectID` |

I mutation-tested rather than reading the test. After two rounds of checks that looked like
checks, "a test exists" was not the thing I needed to confirm — "the test fails when the
check is removed" was.

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
| S2 | Migration — backfill, dual-write | 4, 5 | `ca-msg-em2` | **verified** (`16294728..cd4ee7ed`, 3 rounds) |
| S3 | Envelope — message type, validation, delivery format | 6, 7, 9 | `ca-msg-em3` | **verified** (`cd4ee7ed..f206a0d9`, 2 rounds) |
| S4 | Surfaces — read switch, CLI split, broker edge | 8, 10, 11 | `ca-msg-em4` | active |
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

**S4 Surfaces.** Read switch to `conversation_id` — **behind a default-off runtime flag
per D3, not gated on a production soak (there isn't one).** Must also close DEF-1
(participant-level auth) and DEF-3 (a divergence comparison with an independent source of
truth), and expose the divergence counters somewhere readable live during the beta
exercise. Then:
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
- `2026-08-27 03:10Z` em2 re-reported S2, `9e80a4e2..1ff7c6af`. B-1/B-3/B-4 fixed.
- `2026-08-27 03:15Z` **S2 rejected again.** B-2 not fixed: the literal `true` was replaced
  by an always-true expression (C-1, verified empirically). Two further findings: C-2 the
  soak gate is now un-passable because dual-write never resolves thread conversations, and
  C-3 global DMs are stamped with a `ProjectID` that `resolve.go` enforces as auth. Rules 10
  and 11 added as countermeasures. Round 3.
- `2026-08-27 03:31Z` em2 reported round 3, `1ff7c6af..cd4ee7ed`.
- `2026-08-27 03:35Z` **S2 accepted.** C-1/C-2/C-3 fixed. I mutation-tested the comparison
  (neutered `oldPair == newPair`; the mandatory disagreement test failed), so the check is
  load-bearing rather than merely present. Recorded DEF-3: the phase-5 divergence gate is
  structurally weaker than the design assumed — my spec gap, owed by S4. em2 retired.
- `2026-08-27 03:35Z` S3 opened; `ca-msg-em3` spawned.
- `2026-08-27 03:40Z` **User reports my S1/S2 section reports were never delivered.** I had
  been writing them as terminal output. Rule 8a added. Logged as findings §1.2a — a lived
  instance of the exact defect this refactor removes, and a sharper one than the original
  bug report: the missing direction was invisible to me because the user kept replying to
  their own prompts, which read as evidence the channel worked.
- `2026-08-27 03:43Z` **User settles the beta plan:** beta hub is the validation event, run
  as a scheduled exercise with the user present and a DB snapshot for rollback; until then,
  implementation and tests only. Recorded as D3 (read switch behind a default-off runtime
  flag; divergence counters readable live) and D4 (backfill evidence is synthetic, and
  explicitly weaker than the design asked for). §6 open items closed accordingly.
- `2026-08-27 05:02Z` em3 reported S3 complete, `cd4ee7ed..d9fc7f51`, all gates passed.
- `2026-08-27 05:10Z` **S3 rejected.** E-1: native chat bypasses the validation choke point,
  mutation-verified (choke point forced to fail; all chat tests still passed). Three further
  server-generated emitters found unvalidated — em3 must validate or document each. Also
  noted that AC-8's "three inbound paths" is looser than §2.10's "every inbound path"; my
  wording. **Fixed the same hour:** AC-8 reworded to "every inbound path, not a fixed
  count", with native chat named and mutation-verification required; AC-8c added for
  server-generated emitters. See §5e.
- `2026-08-27 05:45Z` S3 round 2 in progress. em3's branch carries the E-1 fix:
  `fad34947` wires `ValidateLegacyMessage` into the native-chat send path, then
  `c1acaf86` "allow attachment-only messages through validation choke point".
  Not yet merged to the integration branch; no report yet. **Flag for verification:**
  the second commit relaxes the choke point to admit an input it previously rejected
  (`Msg == ""`). That may be a correct discovery — native chat can legitimately send
  an attachment with no text, and `Validate()` requires a non-empty `msg` — or it may
  be the check being widened until the tests pass. Read the diff, not the message:
  confirm the relaxation is conditioned on attachments being present rather than a
  blanket removal of the `msg` requirement, and that a text-less, attachment-less
  message is still rejected.

- `2026-08-27 06:40Z` **S3 accepted on round 2**, `cd4ee7ed..f206a0d9`. E-1 mutation-verified
  fixed; all seven inbound paths have tests that fail when the choke point is neutered; the
  attachment-only relaxation is narrowly conditioned and does not alter stored or delivered
  content. §5e closed. **DEF-4 opened** — the `pkg/hub` suite is degrading commit over commit
  with non-deterministic SQLite OOM failures; assigned to S4 as its first task, because it
  invalidates the full-suite runs my own acceptance method depends on. S4 opened; `ca-msg-em4`
  spawned; em3 retired.

- `2026-08-27 06:47Z` em4 acknowledged and diagnosed DEF-4: **72 `newTestStore(":memory:")`
  call sites in `pkg/hub`, only ~19 with any Close/Cleanup**; each runs a full 49-schema ent
  migration and the DB stays live for the package run. Matches my suspected cause. Plan
  approved with three amendments: (a) fix the class not the instances — `newTestStore` takes
  `*testing.T` and registers cleanup itself, so a caller cannot forget; (b) acceptance is
  `-count=3` green plus a `-count=1` run **plus a revert check** — reverting the cleanup must
  reproduce the failures, or the diagnosis is wrong; (c) DEF-4 merges into the integration
  branch on its own, before any phase 8/10/11 work, so it is not entangled with a read switch
  that may have to be reverted. Warned that closing stores will surface tests that relied on a
  leaked handle — those are defects the leak was masking, not reasons to restore it.

- `2026-08-27 07:42Z` **DEF-4 fixed and merged** — `b92926dd`, integration branch head.
  `newTestStore` now takes `*testing.T` and registers `t.Cleanup` itself; 66 call sites
  updated, 19 redundant manual closes removed. **Test files only** (verified by diff — no
  production code). em4's evidence: four green runs by the developer (`-count=1` ×3,
  `-count=3` ×1) plus two of its own, and **a revert check that reproduced 20 SQLite OOM
  failures** — the causal proof I required, and the difference between an accepted diagnosis
  and a symptom that went away. **My own verification agrees exactly:** two full-suite runs
  at `b92926dd` green (0 failures each), and my own revert check reproduced **20** OOM
  failures — the same count em4 measured. DEF-4 **accepted**. The suite is now a usable
  baseline again. Process note: em4 merged before I accepted. The branch contract says merge after
  acceptance; my instruction to "land DEF-4 as its own merge before phase work" read as
  permission when I meant sequencing. Corrected with em4 for the section merge; no harm here.

- `2026-08-27 07:49Z` **S4 decomposition approved**, receipt confirmed by em4. Four
  workstreams: WS-1 foundation (DEF-1 + DEF-3 + D3 infra, critical path), WS-2 phase 8 read
  switch (blocked on WS-1), WS-3 phase 10 CLI split, WS-4 phase 11 broker edge (independent).
  WS-1 and WS-4 running. D3's counters land at
  `GET /api/v1/admin/messaging/divergence` — an endpoint the operator reads during the beta
  exercise, not log lines. DEF-3 compares a message's freshly resolved conversation against
  the `conversation_id` already stored on prior messages of the same logical conversation —
  an independent source of truth, and the comparison that would have caught B-1.
  **Design ruling issued (D5) — see §5a.** Plus three constraints: the full suite stays green
  **per workstream**, not at section end (otherwise DEF-4's baseline is worthless and a red
  suite means bisecting four parallel efforts); WS-2 must test the flag in the OFF position
  and state what happens to messages written while it was ON if the operator flips back
  mid-exercise; WS-4 adds the Teams `channel:"" + thread_id:set` regression test per plugin,
  because boundary resolution across five plugins is where it comes back.

- `2026-08-27 09:05Z` **S4 rejected on round 1** at `0c94a685`. Two blockers, both WS-3;
  WS-1, WS-2 and WS-4 sound. See §5f. em4 confirmed receipt and took option (a) — wire the
  conversation positional argument — which fixes F-1 and F-2 together, plus the fallback
  counter. **Design amended:** phase 10's row now states the positional conversation argument
  explicitly, and **AC-15a** added — a deprecation warning may only name a replacement that
  works in the same build, verified by a test that executes the named replacement.

- `2026-08-27 09:55Z` **S4 rejected on round 2** at `24ba54f0`. F-1 fixed for `@<agent>` only;
  F-2 architecturally resolved. Two new blockers, **both introduced by the round-1 fix**:
  G-1 (the resolve endpoint trusts a caller-supplied sender identity, making DEF-1's
  participant check bypassable) and G-2 (`conv:` and `#` resolve, print success, exit 0, and
  deliver nothing). DEF-3, D3 and D5 are all satisfied on this branch. See §5f round 2.
  em4 took **option (b)** — ship only what works — on the correct grounds that `conv:`/`#`
  delivery is an unanswered routing-policy question (**DEF-5**, opened). Three constraints
  issued: prove `@<email>` delivers or drop it from the warning too (it hard-errors outside an
  agent container); gate the **CLI**, not the endpoint, which must keep resolving all four
  grammars for brokers and native chat; apply the delivery-assertion rule to every WS-3 send
  test. **Rule 13 issued** — see §1.

## 5f. S4 rejection — open (round 1 2026-08-27 09:05Z; round 2 2026-08-27 09:55Z)

### Round 2 rejection (2026-08-27 09:55Z) at `24ba54f0`

em4's architecture is right — CLI → `POST /api/v1/conversations/resolve` → `messaging.Resolve`
→ `checkPostResolutionAuth`. F-2 is genuinely resolved: `Resolve()` now has a production
caller. F-1 is fixed for `@<agent>`. Both new findings are defects **inside** that fix.

| # | Finding | Evidence | Required fix |
|---|---|---|---|
| G-1 | **The resolve endpoint lets the caller choose who they are.** `handlers_conversations_resolve.go:68–77` reads `sender_principal_kind`/`sender_principal_id` from the **request body** and only falls back to the authenticated identity when they are empty. Nothing checks that the body sender matches the caller. Any authenticated principal can POST `{"reference":"conv:<private-dm>","sender_principal_id":"<a-real-participant>"}` and `requireParticipant` passes against the *claimed* identity. **The round-1 fix made DEF-1 reachable and simultaneously made it optional** — which is worse than dormant-and-correct, because dormant code does not give a false assurance. Latent only because the CLI happens to send `senderID: ""`. | read `handlers_conversations_resolve.go:68–86`; the endpoint's five tests all use a bare `&Server{}` and only reach early returns — **zero coverage of sender identity** | Delete both fields from `conversationResolveRequest` and from `hubclient.ConversationResolveRequest`; sender is the authenticated caller, full stop. Remove the dead `senderKind` computation in `sendMessageViaConversation`. Rule 10: caller authenticates as A, body claims participant B, target is a direct conversation A is not in → assert 403. |
| G-2 | **`conv:<id>` and `#<thread>` resolve, report success, and deliver nothing.** `sendMessageViaConversation` ends with a `fmt.Printf("Message associated with conversation %s…")` and `return nil` — no send, exit 0. The deprecation warning names all three forms as replacements; two of them eat the user's message silently. **AC-15a violated by the very fix that AC-15a was written for.** | **Verified by mutation, not by reading:** a throwaway test driving `sendMessageViaConversation` against em4's own `newConvRefMockHubServer`, counting POSTs to the agent message endpoint — `conv:…` resolves=1 **SENDS=0**; `#general` resolves=1 **SENDS=0**; `@builder` resolves=1 SENDS=1 | Either (a) deliver for all three forms, or (b) gate the CLI to `RefAgent`/`RefEmail`, hard-error on `conv:`/`#`, and cut them from the warning string. (b) preferred if (a) is not short. **Every test covering a form named in a warning must assert delivery.** |

**G-2 is a test-shape failure, and the shape is nameable.** `TestSendMessageViaConversation_ThreadRef`
and `_ConvRef` call `server, _, resolves := newConvRefMockHubServer(t, …)` — they **discard the
send recorder** and assert that resolution was invoked. Proof that the plumbing ran, standing in
for proof that the message arrived. That discarded return value is exactly what AC-15a exists to
forbid, and it is why three APPROVE gates and a green suite missed a silent message-drop.

**What round 2 did satisfy** — verified, not taken on report, and not to be re-litigated:

| Item | Evidence |
|---|---|
| **D5 cross-grammar auth** | `TestResolve_DirectConv_RejectionGrammarIndependent` — present and correctly shaped |
| **D5 group semantics** | `TestResolve_GroupConv_AcceptsNonParticipantProjectMember` |
| **Read switch in the OFF position** | `TestReadSwitch_FlagOFF_AgentMessages_UsesOldPath`, `_FlagOFF_UserInbox_…`, `_ConversationHistory_FlagOFF`, plus `TestReadSwitch_HotReloadToggle` — answers the flag-flip question |
| **DEF-3: divergence can genuinely disagree** | `TestComputeDivergenceMatch_GenuineDisagreement`, `_ThreadDisagreement`, `_RoutingTypeMismatch`, `TestCheckConversationConsistency_DetectsMismatch` |
| **Fallback counter** | `IncFallback`/`Fallbacks`, wired to all three fallback paths, exposed as `fallbacks` |

Full suite not re-run: the branch changes again, so it is deferred to round 3 against the
`b92926dd` baseline.

**The pattern across both rounds is one thing, not two.** F-1, G-1 and G-2 are all cases of a
mechanism that is present and looks right, verified by a test that observes the mechanism being
invoked rather than the outcome the user cares about. F-1: a warning naming a replacement,
untested end-to-end. G-1: an auth check tested against an identity the caller supplies. G-2: a
delivery path tested by counting resolutions. Rule 10 already says the test must fail when the
check is removed — the missing half is that **the test must observe the effect, not the call.**

### Round 1 rejection (2026-08-27 09:05Z)

em4 reported S4 complete at `0c94a685` with three APPROVE gates (review, audit, test), ~55
new test functions, and its own four green full-suite runs. The gates were not wrong about
what they checked. Neither they nor I would have caught F-1 by reading the diff — I found it
by asking what happens to a user who obeys the warning text.

| # | Finding | Evidence | Required fix | State |
|---|---|---|---|---|
| F-1 | **The `--channel` / `--thread-id` deprecation warnings direct users into the exact defect this project exists to remove.** They say "use conversation references instead: `conv:<id>`, `@<agent>`, `#<thread>`". `scion message` cannot parse any of the three; `ParseReference` and `Resolve` have **no production caller**. Traced at `cmd/message.go:137`: `conv:<id>` and `#<thread>` are slugified and looked up as **agent names**; `@builder` contains `@` so becomes **`user:@builder`**, a plausible-looking email recipient. The `@` case **does not error** — it succeeds and delivers nowhere. That is findings §1.2a, newly caused by our own migration guidance. Violates AC-15 in substance and AC-15a in letter. | read `cmd/message.go:118–175`; `grep` for production callers of `ParseReference` / `Resolve` — none | Option (a), chosen: wire `scion message <conversation> <text>` through `ParseReference` and `Resolve`. Option (b) was to strip the syntax from the warnings and hard-error on `conv:`/`#` prefixes rather than slugifying them. | em4 took (a) |
| F-2 | **DEF-1 is implemented correctly but unreachable.** `checkPostResolutionAuth` matches D5 exactly — one call in `Resolve()` after grammar dispatch, direct requires participant, group authorised by project membership, unknown kind fails closed. But `Resolve()` has no production caller, so D5's cross-grammar guarantee holds **only in unit tests**. My ledger rationale for deferring DEF-1 to S4 — "it becomes reachable the moment S4 switches reads" — was wrong: the read switch resolves from server-side inputs (authenticated user + agent, or thread key + project) and never from a user-supplied reference. | `grep` for callers of `messaging.Resolve` — none outside tests; read `handlers_messages.go:63–72, 247–265` | Option (a) makes it reachable. Otherwise DEF-1 stays open and moves to whichever section wires conversation-reference sending. **Not to be recorded as discharged on unit tests alone.** | resolves with (a) |

**Test evidence at `0c94a685` was clean and I confirmed it myself:** full `pkg/hub` suite
green (0 failures), plus `pkg/messaging`, `cmd`, `pkg/store` and `entadapter` all green. The
DEF-4 baseline held through all four workstreams, which is the per-workstream constraint doing
its job. Worth stating plainly: **the rejection is not a test failure.** F-1 is invisible to
any test that does not ask what happens to a user who obeys the warning text — which is a
question about the product, not the code. AC-15a exists to turn that question into a test.

**Non-blocking, raised for the beta exercise.** The read switch **fails open**: when
`ResolveDMConversationForRead` returns nil, `filter.ConversationID` stays unset and the read
silently uses the old path while the flag reads ON. With audit L1 (the consistency check also
fails open on query errors), the exercise could show "flag ON, zero divergence" without the
new model having run at all. The fallback itself is correct and must stay — but the
divergence endpoint needs a **fallback counter**, so the operator can tell "no disagreement"
from "never ran". em4 is adding it.

**My spec gap, second occurrence of the same shape.** Phase 10's row said only
"`scion broadcast`, `scion keys`; `scion message` reduced to six flags; deprecation mapping",
omitting the positional conversation argument that §2 and the announcement both specify. em4
built to the row. This is AC-8's "three inbound paths" again: **a terse phase summary read as
the whole requirement.** The design body is authoritative, but managers work from the phase
table, so the phase table has to carry the load-bearing parts. I have amended the row and
added AC-15a. **Audit of the remaining phase rows done the same hour** — four more amended:
- **Row 7** said "invoked on CLI, hub handlers, and broker-inbound". That is AC-8's original
  wording living on in the phase table, and S3 built to it. Now reads "**every** inbound path
  ... the list is illustrative, **not exhaustive**", naming native chat.
- **Row 8** still carried the pre-beta soak gate. Marked superseded by D3, with the fallback
  counter named as part of the replacement gate.
- **Row 12 (S5, next up)** now requires documentation to describe **the build as it ships,
  not the design's end state** — anything behind a default-off flag is documented as off, and
  unparseable syntax is not presented as available. Without this S5 would document a
  conversation model that is switched off in every deployment, which is AC-15a's defect in
  prose form.
- **Row 13** now states its preconditions: beta passed, and every replacement named in a
  deprecation warning has shipped and been exercised. Removing a field whose replacement was
  never reachable strands exactly the callers the warning redirected.

Row 7's wording is the proof this audit was worth doing: the defect that cost S3 a round was
still sitting in the table, uncorrected, after I had already fixed the AC it came from.

## 5d. S2 rejection history — CLOSED 2026-08-27 03:35Z (accepted on round 3)

S2 was reported complete with three APPROVE gates. I rejected it. Both blockers are
visible by grep and both were missed by review, test, and audit.

| # | Blocker | Evidence | Required fix | State |
|---|---|---|---|---|
| B-1 | **Two `external_ref` formats for the same DM.** `dm:%s:%s` (`divergence.go:106`, dual-write) vs `direct:%s:%s:%s` (`backfill.go:200`, with projectID). Under `UNIQUE(surface, external_ref)` the same DM gets two conversation rows — backfill fills one, live traffic fills the other, and DM history splits at the S4 read switch. Also a design-conformance bug on its own: **DMs are global** (§2.4.1, and S1 `resolve.go:310` sets `ProjectID: ""`), so a project-scoped DM key fragments one DM into one row per shared project. | grep both format strings | One exported project-free DM-key helper, called by backfill and dual-write. Thread keys keep projectID. | open |
| B-2 | **Divergence logging cannot detect divergence.** All six call sites pass `Match: true` as a literal (`handlers_agent_messaging.go:243,736,971,1076`; `messagebroker.go:467,620`). `Mismatches()` can only return 0. | grep `Match:` | Compute `Match` by resolving each model independently and comparing. "Old model has no answer" is a third outcome, not a match. | **still open after round 2 — see C-1** |
| B-3 | `ProjectID` required in `BackfillConfig` (audit Medium, promoted — thread grouping can cross a project boundary; §2.6.1 is an invariant, not a recommendation). | audit report | required | fixed round 2 |
| B-4 | Unit tests for `ResolveOrCreateDMConversation` (test gate marked PARTIAL). It is now the shared correctness point for both phases. | test report | required | fixed round 2 |

### Round 2 rejection (2026-08-27 03:15Z)

B-1, B-3, B-4 fixed. B-2 was not. Two further findings.

| # | Finding | Evidence | Required fix | State |
|---|---|---|---|---|
| C-1 | **The DM comparison is still a tautology.** `divergence.go:145–153` builds both sides from the same two inputs with the same sort and join; after prefix-trimming they are equal by construction. `convID` — the parameter holding the new model's actual answer — is never examined past the emptiness check. A literal `true` was replaced by an expression that is always true. | I ran a table of DM inputs through `ComputeDivergenceMatch`; none produced a mismatch. | Compare the destination `convID` actually denotes (load it, read `external_ref`/participants) against the old model's destination. **Plus a mandatory test that fails if the comparison is degenerate** — a case where the models genuinely disagree, asserting `match==false` and `Mismatches()` increments. | open |
| C-2 | **The gate is now un-passable — the inverse of the old failure.** `divergence.go:137`: any non-empty `threadID` returns false. Every threaded message scores as divergence, so "Mismatches() stays at 0" is unreachable with any thread traffic. Root cause: dual-write only calls `ResolveOrCreateDMConversation`; S1 shipped `resolveThread` and it is unused. Design phase 5 says send paths resolve-or-create, not "DMs only". | read `divergence.go` + dual-write call sites | Dual-write resolves thread conversations via the S1 resolver; threaded messages get a real comparison. S4 needs those rows to exist anyway. | open |
| C-3 | **Global DMs are stamped with a `ProjectID` that is enforced as authorisation.** `conversation.go` sets `conv.ProjectID` whenever projectID is non-empty; `resolve.go:198` enforces any non-empty `ProjectID` as a project lock (`:218` comment: "nil ProjectID means global DM"). Contradicts §2.4.1 and S1. With last-writer-wins upsert, a multi-project user's DM flips project by who spoke last → intermittent boundary-violation/not-found on a conversation they own. Audit raised it as M1; em2 dismissed it as "advisory". | read both files | DM conversations created with nil `ProjectID`. Originating project, if wanted, goes in a field `resolveConvByID` does not read. | open |

**The pattern to watch.** Two rounds, one defect class: code that has the shape of a check
without the substance of one. The countermeasure is now a standing rule — see rule 11.

**Why B-2 is the serious one.** A missing check fails to find problems. This one
*manufactures evidence of safety*: the design makes the phase-5 divergence signal the gate
for S4's read switch, and a clean soak report from this code is indistinguishable from a
real one. I would have approved the read switch on it.

**Lesson for later sections — do not let this recur.** Three independent gates approved
code containing a hardcoded comparison result and two competing constructors for one
deterministic key. Both are single-grep findings. Every manager brief from S3 onward must
require reviewers to check (a) that a comparison actually compares, and (b) that a
deterministic key has exactly one constructor.

## 5e. S3 rejection — CLOSED 2026-08-27 06:40Z (accepted on round 2)

em3 reported S3 complete (`cd4ee7ed..d9fc7f51`) claiming the validation choke point had
"no bypass". It has one.

| # | Finding | Evidence | Required fix | State |
|---|---|---|---|---|
| E-1 (**fixed round 2**) | **Native chat bypasses the validation choke point.** `handlers_chat_v2.go:986` builds a `StructuredMessage`, persists it via `CreateMessage`, and dispatches via `dispatchWithBrokerRetry` — never calling `ValidateLegacyMessage`. The six real call sites cover hub agent-messaging, broker inbound and CLI; native chat is a fourth surface. | **Mutation-verified:** made `ValidateLegacyMessage` return an error unconditionally; `go test ./pkg/hub/ -run 'ChatV2\|Chat'` still passed. Only possible if the path never reaches it. | Validate on the native-chat inbound path before persist and dispatch, plus a test that fails if the call is removed. | open |

**Scope note — my own wording was the weaker one.** AC-8 says "all three inbound paths";
design §2.10 says "a single choke point invoked on **every** inbound path — the CLI, the
Hub HTTP handlers, and broker-inbound alike". The §2.10 list is illustrative and native
chat is a Hub HTTP handler, so it is in scope. Where the AC and §2.10 disagree, §2.10
governs. **AC-8 reworded 2026-08-27** — it now says "every inbound path, not a fixed count",
enumerates native chat explicitly, and requires verification by mutation rather than
inspection. Added **AC-8c** covering the server-generated emitters. Done; not owed by
anyone.

**Three further unvalidated emitters**, found while checking E-1. Server-generated, so
§2.10 does not strictly cover them; I ruled that em3 must either route them through the
choke point or document the exemption with a reason — but not stay silent:
`handlers_chat_v2.go:1129` (mention fan-out), `notifications.go:376/431/449`,
`server.go:2830` (scheduler).

## 5c. Deferred-item ledger — debt accepted during implementation

**Nothing leaves this table except by landing or by an explicit decision to drop it.** A
deferral agreed in one section is the single easiest thing to lose across a manager handoff:
the manager that accepted it is gone, and the manager that must honour it never heard the
conversation. This table is the only thing that carries them.

| # | Item | Deferred from | Owed by | Why deferral is safe |
|---|---|---|---|---|
| DEF-1 | **Participant-level auth on `conv:<id>`.** `resolveConvByID` checks the sender's *project* but not whether the sender is a *participant* in that conversation. Raised HIGH by S1 audit. | S1 | **S4** (surface layer, message-send time) | S1 is not wired into any live path, so the gap is not reachable. It becomes reachable the moment S4 switches reads. **S4 is not verifiable without this.** |
| DEF-2 | **AC-33** — deferred to the envelope validation layer per design. | S1 | **S3** | The validation choke point does not exist until S3 builds it. |
| DEF-5 | **`conv:<id>` and `#<thread>` have no CLI delivery policy.** Resolving a reference to a conversation does not say *who receives the message*. For `@<agent>` the answer is obvious; for a conversation ID or a thread it is a policy question the design never answered — wake the default agent? fan out to every participant? fan out and wake none? S4 round 2 shipped a stub that resolved and then silently dropped the message (G-2), which is what an unanswered policy question looks like when a developer has to ship anyway. Round 3 takes option (b): the CLI hard-errors on both forms with a non-zero exit, and the warning text names only what works. **The resolve endpoint keeps handling all four grammars** — brokers and native chat need them, and resolution is not the broken part. | S4 | **me, before the section that wires conversation-reference sending for these two forms** | Nothing regresses: neither form works today, and erroring is strictly better than the silent drop it replaces. The risk is not technical but bookkeeping — an unanswered design question is easy to lose once the error message makes the gap look intentional. |
| DEF-3 **(CLOSED 2026-08-27 09:55Z)** | **The phase-5 divergence gate is weaker than the design assumed, and this is my spec gap, not em2's.** `ComputeDivergenceMatch` is now a genuine comparison, but at the call sites both models derive their answer from the same three fields (sender, recipient, thread_id), so a DM or thread pair mismatch is **unreachable in production**. The only divergence reachable today is resolution failure (`no-new-routing`). Note the consequence: this signal **would not have caught B-1**, the duplicate-key bug — dual-write would have returned its own row's ref and scored a match. **Closed on S4's branch:** `CheckConversationConsistency` compares against the `conversation_id` stored on prior messages of the same logical conversation — the independent source of truth this asked for — with `TestCheckConversationConsistency_DetectsMismatch`, `_GenuineDisagreement`, `_ThreadDisagreement` and `_RoutingTypeMismatch` proving disagreement is reachable. Carries forward with S4's merge, not before. | S2 | **S4, before the read switch** | Phase 5's new model has no independent source of truth; it constructs the key from the message. Nothing can diverge until something else is authoritative. |

| DEF-4 **(CLOSED 2026-08-27 07:55Z at `b92926dd`)** | **The `pkg/hub` test suite is degrading commit over commit on the integration branch.** Full-suite failure counts: `origin/main` **0** (3 runs), `cd4ee7ed` **5**, `d9fc7f51` **18**, `f206a0d9` **17–19**. Failure membership is **non-deterministic** — two consecutive runs at the same commit shared only 2 of ~18. Every failure is SQLite `out of memory (7)` raised at test-store creation (`newTestStore(":memory:")` / `sql.Open("sqlite3", ":memory:")`), with 109 GB free on the host and unaffected by `-parallel 2`. Each test opens its own in-memory DB and runs the full ent migration; the branch adds tables, so per-DB cost has risen. Suspected cause is stores never being closed, so every in-memory DB stays live for the whole package run — but that is a lead, not a diagnosis. | S1/S2 (accumulating) | **S4, as its first task, before any new feature work** | It does not affect shipped behaviour. It does destroy the verification method: my acceptance of every section from here rests on diffing full-suite results, and a suite whose failure set changes run to run cannot support that. It will get worse with S4 and S5. |

**How I missed DEF-4.** I accepted S2 at `cd4ee7ed`, which already had 5 failures, because I
ran targeted package tests rather than the full suite. Targeted runs pass at every commit —
that is precisely why the problem was invisible. From S4 onward the acceptance check is the
full suite, run twice, compared for stability.

**What DEF-3 requires of S4.** Add a comparison with an independent source of truth:
resolve the conversation for a message, then compare against the `conversation_id` already
stored on prior messages of the same logical conversation. That detects key-format drift,
duplicate rows, and upsert races — the class B-1 belonged to. Until that exists, a clean
soak means "resolution did not fail", **not** "the new model routes where the old model
routed". Do not let the read switch be approved on the weaker reading.

## 5a. Standing technical decisions made during implementation

Decisions I have issued to managers that are not in the design doc. Binding on all
sections.

| # | Decision | Rationale | Issued |
|---|---|---|---|
| D1 | `ConversationStore` accepts **UUID only** for `DefaultAgentID`, and validates it. A slug is rejected, not stored. | The slug-or-UUID union is the class of defect this refactor removes. A store that accepts both propagates the ambiguity instead of resolving it, and every downstream reader must re-ask which form it holds. A narrow store contract forces the ambiguity to be settled at a known place. | 2026-08-27 |
| D3 | **The S4 read switch lands behind a runtime flag, default OFF, flippable without redeploy.** Divergence counters must be exposed somewhere readable live (not log lines only). | User decision 2026-08-27: the beta hub is the validation event, run as a scheduled exercise with the user present and a DB snapshot for rollback. There is therefore **no production soak before the switch**, so the design's phase-8 gate cannot be met in its original form. A flag makes the exercise "snapshot, flag on, watch, flag off" — recovery is a config change rather than a snapshot restore. And since the exercise is the *only* window where the two models meet real traffic, the operator has to be able to read a verdict in the moment; log lines are the wrong shape for that. | 2026-08-27 |
| D4 | **Backfill evidence is synthetic.** Require a seeded corpus exercising both named hazards plus messages that must come out flagged `inferred`. | Real dry-run counts are unobtainable pre-beta (em2 reported this twice). Recorded as **weaker than the design's requirement**, not as the requirement being met. Do not let a later section cite "backfill validated" without this caveat. | 2026-08-27 |
| D5 | **Authorisation is a property of the resolved conversation, evaluated after resolution, identically for every grammar** — not a property of the reference syntax. Direct conversations: the sender must be a participant. Group and thread conversations: project membership authorises, and prior participation is **not** required. Rule-10 tests: non-participant rejected on a direct conversation; project member accepted on a group conversation they have never posted in; and the same direct-conversation rejection reached via **both** `conv:<id>` and `@<name>`. | em4 scoped DEF-1's participant check to `resolveConvByID` alone. But `#general` and `@agent` resolve to the same rows, so a check on one grammar is one you walk around by using another — the defect class this refactor exists to remove. The per-kind split matters too: requiring participation on group conversations would break resolve-or-create and the "say something in a room" case the design is built on, while global DMs have nil `ProjectID` and so cannot be carried by the project check at all. Without the cross-grammar test the hole survives and only the test is new. | 2026-08-27 |
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
- ~~**Beta hub target.**~~ **Closed 2026-08-27.** The user owns scheduling the beta
  exercise and will direct deployment mechanics then. Dropped from my open list; pick it
  back up if asked.
- ~~**Phase 8 soak gate.**~~ **Superseded by D3.** There is no production before beta, so
  the pre-switch soak cannot happen. Replaced by: read switch behind a default-off runtime
  flag, divergence counters readable live during the exercise. **The gate was not skipped —
  it was moved and weakened, deliberately, and this is the record of that.**

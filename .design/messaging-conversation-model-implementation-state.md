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
2. **Managers are sequenced by file contention and supervision cost, not by count.**
   **AMENDED 2026-08-27 16:50Z.** The original rule said "one active manager at a time", and
   its stated reason was merge conflicts I would have to adjudicate without having written the
   code. That reason is about *shared files*, but the rule was written as a *headcount cap* —
   so it kept blocking work that shares no files, and I only noticed when it had queued a
   user-reported defect behind an unrelated four-hour section. **A rule enforced by a proxy
   outlives the thing the proxy stood for.**

   The test now: (a) do the sections touch the same files? (b) can I actually supervise both
   at the quality I hold them to? If either answer is bad, sequence. Parallel dispatch names
   the disjoint file sets in §3 and tells each manager which paths are not theirs.

   Standing caution against my own amendment: I nearly sent a false rejection earlier the same
   day while supervising two managers (§5x). **Supervision quality, not file contention, is the
   binding constraint,** and it degrades quietly.
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

14. **A test must assert it had something to observe.** Rule 13's dual, issued
    2026-08-27 12:05Z after S5 round 2. Put it in every manager brief alongside rules 10
    and 13. Any test that iterates over discovered input — files on disk, lines of captured
    output, records from a query — **must assert a non-zero floor on what it found**, and
    must fail rather than skip when an expected input is absent. J-1 and J-2 were both
    fully green while examining zero real artefacts: one because its extractor keyed on a
    quote character, the other because four `t.Logf("skipping missing file")` branches
    swallowed a path change. Rule 10 subtests over `t.TempDir()` fixtures prove the
    *function* is correct and say nothing about whether it was ever *fed*. **A check whose
    input can silently become empty is not a check.** Operationally: no `continue` on
    missing input without a failing assertion elsewhere, and every discovery loop carries
    a documented minimum count that may be raised but never lowered.

15. **Grep `origin/main` before speccing a mechanism, and treat another agent's account of
    their own shipped system as a claim to verify, not as evidence.** Issued 2026-08-27
    13:25Z. Twice in one day my design asserted something about code with nothing checking
    it: §2.9 said `scion schedule message` "already exists" and it never did (that produced
    I-1); §2.4.2 invented a DM key format that **already existed, shipped and
    regex-validated**, in the same repository — along with the entire principal-kind
    security hazard I briefed S6 on at length, which that shipped format had already
    solved. The second one I compounded by repeating another architect's "implementation is
    in flight" as fact; the code had moved past their design doc and the work was landed.
    **I required mutation-level proof from every manager while my own documents and my
    peers' descriptions went unverified. The standard applies upward.** Operationally: a
    design section that claims a capability exists cites the file and line, or it does not
    make the claim.

16. **A design opens with prior art in this repository, grepped for the design's own core
    nouns, cited by file and line. Empty is an allowed finding; absent is not.** Issued
    2026-08-27 13:38Z, and it is the inverse of rule 15 — rule 15 stops me claiming a
    capability exists; **this one stops me claiming it does not.** That is the failure that
    actually cost us.

    The prompt was the user asking why the shipped chat layer was never encountered when we
    wrote the original architecture. I checked instead of reasoning, and the answer was worse
    than I expected. The DM key format landed `cb7ffa42` **2026-08-13**; native chat shipped
    with it `68eb1399` **08-15**. My design began **08-23** — ten days later, with the code
    sitting on `main` the whole time.

    **The excuse I expected to find was vocabulary drift** — that they said "topic" where I
    said "conversation", so no search could have connected the two. **That is false and the
    data killed it:** `handlers_chat_v2.go` contains 98 occurrences of "conversation" and
    exports a type named `ConversationKey`. A grep for my own central noun would have hit it
    on the first try. **It was never hidden. Nobody looked.**

    So the cause is structural, not a lapse of diligence by any individual. **No role in our
    pipeline has "existing art" as its deliverable.** The investigator is scoped to *why is
    this broken* and correctly returns the defect's neighbourhood — the CLI send path. The
    architect is scoped to *what should this be* and designs from the problem statement.
    Neither output is *what in this repository already solves this*, so it belonged to nobody
    and every role did its job correctly while the gap stayed open.

    Contributing factor, smaller but worth naming: the brief said "messaging" and "CLI";
    native chat reads as a UI feature. **We treated a product-surface boundary as a code
    boundary.** It is not one — both are the same addressing problem over the same noun.

17. **A ledger row that characterises the cost or difficulty of unstarted work is a claim
    about code, and carries the same citation burden as a design section.** "The fix is real
    work" is a finding, not a note. Issued 2026-08-27 15:50Z after the DEF-6 row was found to
    be wrong on two independent counts — it asserted a storage constraint I had never read,
    and it scoped as novel a mechanism that already shipped. **The ledger is the one place a
    wrong claim is inherited without review**, because the next reader takes it as settled
    history rather than as an assertion to check.

18. **A revert-detection sweep is run against the merge parent, never against the branch head
    that has moved since.** Issued 2026-08-27 16:15Z. **The tool that finds reverts is itself
    the tool most likely to manufacture one.** I ran a files-present-on-main-but-absent-here
    sweep against live `origin/main`, which had advanced past the merge, and got three dropped
    files and a 468-line docs gutting — all false, all belonging to a commit that landed after
    the merge parent. I had the rejection drafted. This is the §5s decay rule, which I had
    issued to two managers that same day and had not applied to my own verification of their
    work. **A verification procedure is not exempt from the epistemics it enforces.**

19. **Ordering between validation and persistence is a design decision and must be stated,
    not inherited.** A persistent write placed before validation creates facts the request was
    then refused permission to create. Issued 2026-08-27 16:20Z on DEF-16, where the two agent
    ingress handlers were found to perform the *same two operations in opposite orders* — one
    validates then writes, the other writes then validates. That is not a style difference;
    at least one of them is wrong, and nothing in the code says which.

20. **When a fix filters *which* inputs reach a broken path, review must ask what that path
    still does with the inputs that survive. Closing the funnel is not closing the sink.**
    Contributed by `nc-arch` 2026-08-27 16:30Z, generalising two instances one week apart:
    #1319 format-validated DM keys and left DEF-14's authorization gap (§5u); #1322 closed
    DEF-14 and left DEF-15's mis-shaping (§5y). Both PRs are correct. **The hazard is that a
    narrowing fix reads in review as a closing fix**, and the residue it leaves is rarer, later,
    and restricted to legitimate traffic — the population nobody audits. Corollary already in
    force: "the exploit no longer reproduces" is not "the defect is closed."

21. **An exposure claim in the ledger is a claim about code and expires like any other.**
    Issued 2026-08-27 16:35Z. The DEF-15 row said "reachable today", which read as production
    and was true only of the integration branch — `pkg/messaging/conversation.go` and
    `backfill.go` do not exist on `origin/main` and it has zero `fmt.Sprintf("thread:` sites.
    Another architect ran that grep; I had not. **Rule 17 says a ledger cost claim needs a
    citation; this says a ledger *severity* claim needs one too**, and severity is the field
    that decides what gets dispatched first. Every defect row states which refs it is reachable
    on, by name.

22. **Verify against the gate the work must pass, not only the tests it must satisfy.**
    Issued 2026-08-27 17:10Z. `origin/main` is 100% `gofmt`-clean. `scion/messaging-v2` carries
    **18** unformatted files, every one of them ours, accumulated across S5–S8. I accepted six
    sections on full-suite green and never once ran `gofmt -l`, because `go test` is what I had
    decided verification meant. **A green suite is evidence about behaviour and says nothing
    about mergeability.** The defect is invisible per-section — one or two files each, always
    below the threshold where anyone objects — and only legible branch-wide, which is the level
    nobody was checking because each manager checks their own diff and I was checking their
    tests. Merge-readiness is now a standing step in the heartbeat, run against the whole branch
    and diffed against `origin/main`, not against the previous run of itself.
    **AMENDED 17:17Z, and the amendment is the real rule.** Having found the formatter I treated
    it as the finding. It was just the gate I happened to know. `.github/workflows/ci.yml` defines
    **seven**; I had never opened it in three days of running this project. Reading it turned up
    two more failures — `make compat-literals` (11 legacy literals, in two files that do not exist
    on main) and `golangci-lint --new-from-merge-base=origin/main` (7 issues, all ours by
    construction) — and one reassurance I would never have thought to collect, `check-authz-guards`
    passing clean across DEF-14/DEF-15 territory. **Checking a gate you have not read is checking
    your memory of it.** Enumerate the gate from its definition, then run all of it.

23. **A tripwire's covered case and its blind spot are not obvious from reading it — mutate both.**
    Issued 2026-08-27 17:10Z. S8 pinned `int(RefThread) == 4` to catch drift in a hand-maintained
    table, and disclosed the blind spot as "a new kind inserted before RefThread". That is exactly
    backwards: `RefThread` is the last member, so insertion shifts it and fires, while **appending
    — how Go iota enums are extended nearly every time — leaves it at 4 and passes silently.**
    Verified by mutation, both directions. The disclosure is worse than no disclosure: it names
    the covered case as the gap, so a future reader sees the risk acknowledged and stops looking.
    **An honestly-declared limitation is still a claim about code and gets tested like one.**

24. **A branch's base is a claim, and it is the first one to verify — before the diff, before the
    tests, before the review.** Issued 2026-08-27 17:45Z. `ca-msg-em6` reported "5 commits on base
    `e2b5c37d`". `git merge-base --is-ancestor e2b5c37d origin/scion/ca-msg-em6` returns **false**.
    The real base was `b7669831`, a commit inside their own already-merged work: after I merged
    their DEF-8 branch they kept building from their old head instead of the integration head.
    Merging would have reverted **23 commits / 80,471 lines across 299 files**, including the whole
    `origin/main` merge `2724ed10` — Permissions Foundation (#1312), single-node Cloud Run (#1310),
    Cloud SQL Auth Proxy (#1309), Cloud Run Instances runtime (#1302), OAuth client credentials
    (#1313), the non-loopback dev-auth security fix (#1307). Other teams' shipped work, and the
    exact hazard the user warned me about by name. **I specified the base at dispatch and never once
    verified it** — through a plan review, a design question and a completion report. Now mandatory
    in every acceptance, before reading a line of the change: `merge-base --is-ancestor
    <integration-head> <branch>`, plus `git diff --stat <integration-head> <branch>` sanity-read for
    files nobody meant to touch. **Rule 18 is not enough**: it sweeps for dropped *files* between
    merge parents, and would have caught this only at merge time, after review had already approved.

25. **Absence of a control in your tree is indistinguishable from removal of it; only the base tells
    them apart.** Issued 2026-08-27 17:45Z. Both of em6's escalations — "Agent 2 removed the
    `validDMKey` 400 rejection" and "the repoint dropped DEF-11's `lookupFailed`/`Fallback`" — were
    reported as deliberate deletions by their own developer. Neither control was ever in their tree:
    `validDMKey` reached `handlers_agent_messaging.go` via #1319 and `lookupFailed` via S7, both in
    commits they did not have. **A developer cannot delete what was never there.** The diff was
    honest; the reference point was wrong. **The dangerous half is the corollary: answering the
    questions as posed would have hidden the defect.** Both were framed as small repairs — restore a
    400, re-add a Fallback entry. Approving them yields hand-written re-implementations of #1319 and
    DEF-11 on a tree missing 23 commits: suite green, controls apparently present, 80,471 lines still
    reverted on merge, now wearing a convincing disguise. **A repair can conceal the defect it was
    reported as.** When a report says "our own agent removed a safety control", suspect the base
    before the agent.

26. **Three independent quality gates passed a branch none of them had located.** Issued
    2026-08-27 17:45Z. `ca-msg-em6` (manager), `review-s215` (code reviewer, APPROVE) and
    `audit-s215` (security auditor, 0 Critical / 0 High) all cleared a branch that reverts an entire
    `origin/main` merge. Not one of the three was wrong about the code in front of them; the
    auditor's M1/L1/L2 findings are sound and still owed. **They all reviewed the change and none
    reviewed where it started.** Adding reviewers does not cover this, because independent reviewers
    share the working tree and therefore share its blind spot — the omission is *correlated*, so
    redundancy buys nothing. Base verification cannot be delegated to more eyes on the diff; it is a
    different question, asked once, of the ref.

27. **"Completed" describes an agent's last task, not whether its work has landed.** Issued
    2026-08-27 18:10Z. The coordinator asked me to retire six idle agents for fleet capacity. Five
    of them were idle *because I had rejected §2.15 twenty-six minutes earlier* — they are the
    agents who must re-run every AC after the rebase, and `scion/ca-msg-em6` is unmerged. **An
    agent idle because its work is merged is done; an agent idle because its manager is blocked
    upstream is waiting. The roster renders both as "completed".** The safe test is not the phase
    column but the refs: does the agent own an unmerged branch, and is its section accepted? Only
    `dev-def11` passed — no branch of its own, and `merge-base --is-ancestor origin/scion/ca-msg-em7
    origin/scion/messaging-v2` true. Note also the coordinator asked me to "have em7 confirm";
    em7 had been retired an hour earlier, so the confirming authority did not exist. **When the
    named authority is gone, verify from refs rather than downgrading to an assumption** — the
    request degrades silently into "assume it is fine", which is what it was written to prevent.

28. **A ledger row naming a blocker does not say who owns the blocker.** Issued 2026-08-27 18:30Z.
    DEF-5 and DEF-7 both read "depends on the escalated unification decision, §2.6.3". The
    decision had been made at 13:28Z; what was actually missing was **the draft I owed nc-arch**.
    "Blocked on the unification decision" and "blocked on work only I can do" render as the same
    sentence, and the first one reads as external. I carried it for five hours and told the
    coordinator "runnable meanwhile: nothing safe" while holding the one task with zero file
    contention on the board. **When a row says blocked, name the owner of the blocker in the same
    breath; if it is you, it is not a blocker, it is a queue.** The self-check: for each blocked
    item, who do I expect to unblock it, and have I asked them?

    **This is the second occurrence, and rule 15 was already supposed to catch it.** §5r, 13:43Z:
    I held DEF-11 for a file conflict with S6 that a one-line `git diff --stat` disproved, and
    widened rule 15 that day to cover *"any premise that gates action."* That widened rule did not
    catch today's case, and the reason is worth more than the rule. A premise like "S6 touches
    `handlers_agent_messaging.go`" **looks like a claim** — it names a file, it invites a grep.
    "Depends on the §2.6.3 unification decision" looks like an **attribution**, and attributions
    do not read as checkable. **The dangerous premises are the ones phrased as provenance rather
    than as fact.** So rule 28 is not "verify your blockers" — rule 15 already said that. It is:
    a blocker written as *what it depends on* hides *who owes it*, and only the second form can be
    audited. Write blockers as owners.

29. **"Fail closed" is a rule about authorization, not about shape constraints — and a constraint
    test that only asserts rejection cannot see over-rejection.** Issued 2026-08-27 18:32Z.
    §2.6.4 AC-U-4 said "a `surface=native` conversation cannot be persisted with a NULL
    `project_id`". nc-arch caught that this **fails closed on every human↔human DM**, which is
    legitimately global — `pkg/messaging/conversation.go:81`, *"ProjectID is intentionally nil"*,
    which I verified rather than accepted. Two distinct errors, and the second is the durable one:

    - I generalised from the sub-kind I had read (topics) to the surface as a whole. The grep that
      informed §3.2 was a topic grep; I wrote a native-surface rule from it.
    - **I imported "under-granting is recoverable; over-granting is not" into a place it does not
      belong.** That posture is correct for *authority* decisions, where the cost of a wrong
      "allow" is unbounded and a wrong "deny" is a retry. A `NOT NULL` constraint is not an
      authority decision — it is an assertion about shape, and over-tightening it does not
      under-grant, it **breaks a working feature**. Applying the security instinct outside security
      turned a safety reflex into an outage. **Ask what a wrong rejection costs before reaching for
      fail-closed; if the answer is "a legitimate feature stops working", the reflex is wrong.**

    The test consequence generalises past this spec: **every constraint AC needs its paired
    positive — assert what must still be *allowed*, not only what must be refused.** A one-sided
    constraint test goes green on a rule that rejects the world. AC-U-4 and AC-U-4b now land
    together or not at all.

30. **A test that must bypass a gate to reach its subject has just discovered something about the
    gate.** Issued 2026-08-27 18:36Z, from DEF-19. em6 wrote
    `TestHandleGroupMessage_ThreadID_NotPropagated` by calling `handleGroupMessage` **directly**,
    documenting the reason honestly: *"on our tree, `ValidateLegacyMessage` rejects `group[]`
    recipients before the code reaches `handleGroupMessage`."* That sentence is a **live
    production regression in a shipped CLI feature**, written down as a testing inconvenience.

    The bypass is not the defect and the test is not wrong — sometimes a unit test legitimately
    needs to skip a layer. **The signal is the reason for the bypass.** "I had to go around X to
    test Y" is a statement about X's behaviour, and it is usually the first observation anyone has
    made of that behaviour under a realistic input. Whenever a test comment explains why a layer
    was skipped, that explanation is a finding and gets an owner.

    **Corollary, and the part that actually let it through: "unrelated to my section" is a routing
    decision, and routing to nobody is not routing.** em6 was correct on ownership and correct to
    stay in scope — I had told them to. But a defect classified as out-of-scope by the only agent
    who has seen it, in a system where I am the only router, is a defect that ceases to exist
    unless I read the aside. **The manager who scopes work tightly inherits the duty to read
    everything the scope excluded.** Green suites do not report the tests nobody wrote.

## 1b. LANDING PLAN — incremental PRs to main (user directive 2026-08-27 18:30Z)

**The integration branch is abandoned as a merge unit.** `scion/messaging-v2` remains the
*staging ground* — sections still merge there and stay integrated and tested — but it is **never
PR'd to `main` as one change**. Instead we cut tranche branches from current `main` and land them
in order.

**Why this is tractable:** the branch history is already phase-ordered (phases 1-11 in
first-parent order), so the tranches follow the build order rather than cutting across it.

**Tranches, ordered by blast radius:**

| # | Content | Reachability | Risk |
|---|---|---|---|
| **A** | Phases 1-4: ent schema + generated code, store adapter, resolution, drift, normalize, backfill | Additive; nothing reads it | **The DB migration, not the code** — new tables + `conversation_id` on Message |
| **B** | Phase 5: dual-write + divergence logging | Writes on the live path | Low — Phase 5 non-fatal contract |
| **C** | Phases 6, 9, 11: envelope types, delivery format, broker edge + 5 adapters | Additive + broker inbound | Low-medium |
| **D** | Phase 7: validation choke point | **Rejects traffic** | **HIGHEST — blocked by DEF-19** |
| **E** | Phase 8: read-switch machinery + divergence board | Flag-gated, switch OFF | Low while off |
| **F** | Phase 10 + S8: CLI subcommands, help grammar, deprecations | User-visible | Medium, revertible alone |
| **G** | Flip `conversation_read_switch` | One setting | Revertible alone |

**Tranche A file set (computed, not recalled):** all `pkg/ent/**` for Conversation /
ConversationParticipant / MessageAddressee / Message; `pkg/messaging/{backfill,drift,normalize,resolve}.go`
+ tests; `pkg/store/entadapter/{composite,conversation_store,message_store}.go`;
`pkg/store/{models,store}.go`. **Excludes `pkg/messaging/conversation.go`** — that arrives with
Phase 5.

**Sequencing constraint:** tranche A overlaps §2.15 on `backfill.go` and `resolve.go`, so **§2.15
settles into `messaging-v2` before tranche A is cut.**

**Tranches are cut as final-state, not as original commits.** Replaying the original phase commits
would land code that later commits fixed — S6/S7/S8 and §2.15 all repaired defects in phase-1-5
files. Each tranche PR carries the *current* reviewed state of its file set. History granularity
is traded for not shipping known defects.

**CI gates are per-tranche, not once.** The 15 gofmt violations span phases 3, 5, 6 and 9, and
`golangci-lint` runs `--new-from-merge-base=origin/main`, so each tranche PR is measured against
main independently. Fixing formatting once on `messaging-v2` does **not** make the tranche PRs
green. Run all seven gates from `.github/workflows/ci.yml` on every tranche branch (rule 22).

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

> **§3 REWRITTEN 2026-08-27 15:25Z.** The text below this block had drifted badly — it still
> read "no agents remain under this project but me" while two managers were running and a third
> agent had just been dispatched. Same drift class as the DEF-1 ledger row (see the DEF-1 entry
> in §5c): **the document disagreed with itself, and the stale half was the half a recovering
> reader hits first.** Everything from "DEPLOYED 2026-08-27 12:37Z" down is retained as
> historical record and is accurate *as of the timestamps it names* — read it as log, not as
> current position.

**CURRENT POSITION as of 2026-08-27 18:35Z**

**Integration branch head:** `edd4e4bd`, pushed, full suite green, 81 commits ahead of
`origin/main`. Unchanged since 17:20Z — I have deliberately not pushed to it while em6 rebases
onto it (moving their target is how the 80k revert happened).

**`main` is now `b09e7f49`** (was `98a9d9c2` at 17:20Z, `98a9d9c2` before that). It moves roughly
hourly. Not absorbed; absorb at final merge. **Do not cache this value** (rule 24).

**§2.15 / `ca-msg-em6`: rebased, base verified by me, formal report outstanding.**
`scion/ca-msg-em6` @ `9cf1df92`. `merge-base --is-ancestor edd4e4bd HEAD` passes; diff is 16 files,
+1,694/−108, no whole-file deletions, all §2.15 files. Control preservation checked by symbol
count, not by reading: `lookupFailed` 3=3, `Fallback` 2=2, `validDMKey` 2=2, `DeriveConversationKey`
3 (new). Still owed by em6: re-run ACs (all were measured against the wrong tree), the
`dev-validdmkey-test` result on Item 1, and the auditor's M1 (`handleGroupMessage` `:1120`/`:1245`
bypassing `DeriveConversationKey`) proven by test.

**Blocked items, each with the owner of the unblock named (rule 28).** A row whose owner is *me*
is a queue, not a blocker.

| Item | Waiting on | Owner | Asked? |
|---|---|---|---|
| Integration-branch strategy (cut over to PR-per-section?) | decision | **user** | yes, 17:55Z, unanswered |
| §2.15 merge | formal report + re-run ACs | **em6** | yes, standing instruction |
| DEF-17 + DEF-18 gate sweep | §2.15 landing (file contention) | **em6**, then me | n/a |
| DEF-12 | §2.15 phase 4 | **em6** | n/a |
| DEF-6 (§2.14.1) | supervision capacity | **me** — queue, not blocker | n/a |
| DEF-9 (§2.13) | dispatch capacity; spec is complete with ACs | **me** — queue | n/a |
| DEF-5, DEF-7, `#<thread>` | ~~unification decision~~ → unification **spec**, §2.6.4 phase 6 | **was me — drafted 18:30Z**; now nc-arch Q2 | yes, 18:32Z |
| §2.6.4 phase breakdown | Q2, Ent/raw-SQL shared transaction | **nc-arch** | yes, 18:32Z |
| §2.6.4 default-agent behaviour change (Q1) | decision | **user** | **held** — they already have one unanswered question from me |

**MERGE BLOCKERS, both mine, both non-behavioural (DEF-17, DEF-18).** Three CI gates red that
`main` passes: `gofmt` (15 files after em8's `cmd/` cleanup), `make compat-literals` (11 legacy
literals in two files absent from main, so all ours), `golangci-lint --new-from-merge-base` (7,
ours by construction). Passing: vet, authz-guards, build, tests. **Seven gates total — enumerate
from `.github/workflows/ci.yml`, do not check from memory** (rule 22).

---

**Historical — position as of 2026-08-27 17:20Z**

**Integration branch head:** `edd4e4bd` — S8/DEF-13 merged, **pushed**, full suite green (8
packages, `pkg/hub` 279.1s). 81 commits ahead of `origin/main`. Zero files
dropped against both merge parents. Verified by re-running the manager's mutations myself, not on
their report: M1 (append a `ReferenceKind`) survived the original iota pin and is killed by the
`go/ast` enum count that replaced it; M4 (delete a documented form from `Long`) killed, naming the
form; M3 (gated `conv:` example in cobra help) killed by the new deny-list scan, naming pattern
*and* source. Full suite running at merge time.

**§2.15 BLOCKED ON A BAD BASE (17:45Z).** `ca-msg-em6` reported complete; the branch is based on
`b7669831`, not `e2b5c37d`, and would revert 23 commits / 80,471 lines including the whole
`origin/main` merge. Rebase onto `edd4e4bd` ordered; every AC is unverified until it re-runs against
the right tree. **Both questions they escalated were symptoms of the bad base, not code changes** —
see §5ad. Timeline for the beta slips by the length of that rebase plus re-verification.

**Historical (17:20Z) — Managers:** `ca-msg-em6` active on §2.15 with three sub-agents (`dev-derive-key` and
`dev-migration-sweep` reporting complete, `dev-handler-fixes` executing). **`ca-msg-em8` retired
17:25Z** along with `dev-def13`, `dev-def13-fix` and `review-def13`; absence confirmed by name on
the roster afterwards, not assumed from the stop command's exit code.

**MERGE BLOCKERS, both mine, both non-behavioural (DEF-17, DEF-18).** Three CI gates are red that
`main` passes: `gofmt` (18 files), `make compat-literals` (11 legacy literals in two files that do
not exist on main), `golangci-lint --new-from-merge-base` (7 issues, ours by construction). Passing:
vet, authz-guards, build, tests. Single sweep after §2.15 lands — deliberately not now, to avoid
conflicting with three live sub-agents.

**`main` has moved to `98a9d9c2`.** Not absorbed; absorb at final merge.

---

**Historical — position as of 2026-08-27 16:25Z**

**Integration branch head:** `e2b5c37d` — S7/DEF-11 merged into `dfb348c3` (S6's skipped DEF-15
test). Zero files dropped against **both** merge parents. Full suite pending push.

**`main` has moved again to `98a9d9c2`.** Not absorbed; absorb at final merge, not now.

**DEF-15 is branch-local.** `pkg/messaging/conversation.go` and `backfill.go` do not exist on
`main`, which has zero `fmt.Sprintf("thread:` sites. Pre-beta defect in unreleased code, not a
live production issue — I had been writing it as the latter (rule 21). S7 rebased cleanly onto `2724ed10` in a single rebase, 5 commits, no conflicts.

**S7 VERIFIED — and I mutation-tested it rather than reading the diff.** All four
`TestDEF11_PreResolvedConversation_*` pass on my own run. Three mutations, each killing exactly
the tests it should and no others:
- **M1** remove `convResult.ExternalRef = conv.ExternalRef` → `PopulatesExternalRef` and
  `DivergenceMatch` FAIL; the other two pass.
- **M2** `if lookupFailed` → `if lookupFailed && false` → `LookupFailure` FAIL, alone.
- **M3** `Fallback: true` → `Fallback: false` → `LookupFailure` FAIL, alone.

`GenuineDisagreement` survives all three, which is the control that matters: it proves the
mutations are not simply breaking the package. **Specificity is the signal, not the kill.** A
mutation that fails everything proves only that the code is reachable; a mutation that fails
exactly one named test proves that test observes exactly that effect.

**TWO SECTIONS ACTIVE — deliberate, under amended rule 2, file sets disjoint and named:**
- `ca-msg-em6` — **§2.15** (DEF-15/DEF-16), dispatched 16:40Z off `e2b5c37d`. Owns
  `pkg/messaging/{conversation,backfill,dm_migration}.go`, `pkg/hub/handlers_agent_messaging.go`,
  `pkg/hub/attachments_agent_test.go`.
- `ca-msg-em8` — **DEF-13** (§2.14.2, CLI help text), dispatched 16:52Z off `e2b5c37d`. Owns
  `cmd/message.go` and its test. Told explicitly which paths are not theirs.

**§2.15 detail, dispatched to `ca-msg-em6` 16:40Z off `e2b5c37d`.** Six phases; 1-2
touch no handler, 3-5 are serial on `handlers_agent_messaging.go`. Phase 4 repoints
`backfill.go:195`'s derivation only — **the backfill stays unwired**, DEF-12 gated behind it.

**S7 CLOSED 16:38Z.** Merged at `e2b5c37d`, branch `scion/ca-msg-em7` @ `459a6ce8` confirmed on
remote, agent retired (`scion stop --rm`, absence from `scion list` confirmed).

**Branch contract restated to S6 on dispatch.** They pushed `dfb348c3` directly to the
integration branch. That was inside the main-sync mandate and is not a violation, but the mandate
ended with the sync — from here they push `scion/ca-msg-em6` and **I merge**. The gate exists
because the person who resolved a conflict is the worst-placed person to notice what it dropped,
which is exactly how DEF-15 surfaced.

**S6's earlier section still accepted; DEF-15/DEF-16 now their next section rather than a reopen.** See both ledger rows. Their
merge of main was verified clean on the revert axis against the **merge parent** — after I ran
that same sweep against a moved `origin/main` and manufactured a false finding I was about to
send as a rejection (§5x, rule 18).

**PR #1322 (native chat) reviewed 16:22Z — sound, closes DEF-14, does not close DEF-15. NOT YET MERGED** (nc-arch, verified by me at `origin/main` `98a9d9c2`: `isDMParticipant` is still the old ID-only form and `parseDMKeyIDs` does not exist).
Ownership check at `handlers_agent_messaging.go:174` sits *before* the dual-write at `:245`, so
an unauthorized key is refused before it can leave a row. `parseDMKeyIDs` returns `("","")` on
any non-canonical key and the comparison then denies — fails closed, as required. The
`isDMParticipant` tightening also fixes the kind-half bug (old code compared IDs while ignoring
whether the slot said `user` or `agent`). No change requested. Absorb at final merge.

---

**SUPERSEDED — position as of 2026-08-27 15:25Z**

**Integration branch head:** `2724ed10` — S6's merge of `origin/main` @ `6268bac4` (16:09Z), on top of `916eae7c` (S6 section, 15:12Z), the first section to land since
`ebf8cc27`. Build clean; `go test ./pkg/hub/... ./pkg/messaging/... ./pkg/messages/...` green
(`pkg/hub` 224.9s, exit 0) on the merge commit before push.

**S6 NOT CLOSED — reopened 16:15Z on DEF-15.** The section work itself is accepted and merged; what is open is one deleted test line and the defect it concealed. Merge of main verified clean on the revert axis: 9 ent schema files (6 main + 3 ours), 3 `validDMKey` sites intact, zero files dropped — verified against the **merge parent**, after I first ran that sweep against a moved `origin/main` and manufactured a false finding (§5x, rule 18).

**Originally recorded 15:12Z:** DEF-8/DEF-10: DM convergence onto the shipped kind-encoded key, key-based
authorization for `kind = 'direct'`, invariant D-1 with a key-derived guard, all direct
conversations `ProjectID` nil, no DM key derived from a guessed principal kind at any of 6 sites.
Accepted on round 3 of the mutation-1 test; the mutation result is what settled it, because a
mutation that flips the outcome proves the test reaches production code.

**Active managers — both alive, both correctly blocked on me:**
- `ca-msg-em6` — **re-tasked 15:20Z** with the main-sync (below). Owns the colliding hunks.
- `ca-msg-em7` — DEF-11 complete and approved at `4a7a3844`, **parked awaiting a single rebase**
  onto the post-sync head. Told explicitly not to rebase yet, so they rebase once rather than
  twice; the double rebase would be my scheduling error charged to their time.
- `ca-msg-inject-repro` — developer, dispatched 15:22Z to write **one failing test** reproducing
  DEF-14. Evidence only, explicitly not a fix, on its own branch, merged nowhere.

**Blocking item: `main` has moved and the integration branch must absorb it.** PR #1319 landed at
`6268bac` and edits `handlers_agent_messaging.go`, the same file and functions S6 changed. Trial
merge at 15:10Z showed 13 conflicts — 8 generated `pkg/ent/*` (**regenerate, never hand-merge**),
real ones in `handlers_agent_messaging.go`, `attachments_agent_test.go`, `server.go`, `store.go`,
`entadapter/composite.go`. That count is a **measurement with a shelf life** (§5s refinement); S6
re-runs it rather than working from it. S6 must also re-verify all three mutations after the
sync: #1319 adds early returns *upstream* of their dual-write, and a new early return can make a
passing test vacuous without touching the test.

**Runnable now:** nothing of mine. **Blocked:** S7's rebase (on the sync), DEF-9 dispatch (file-
contested until both merges land), DEF-12 backfill (hard dependency on DEF-8, now landed —
becomes runnable once the sync is clean). **Open and unspecced:** DEF-13 (help text, fold into an
existing section), DEF-6, DEF-5 umbrella, and the unification spec nc-arch asked to be shown.

---

**HISTORICAL — accurate as of the timestamps named, superseded by the block above.**

**Active section:** none. **S5 CLOSED 2026-08-27 12:40Z at `55dd6e16` (round 3).**
**Active manager:** none. **`ca-msg-em5` retired 12:48Z** (`scion stop --rm`, absence from
`scion list` confirmed) after confirming a clean tree at `55dd6e16` pushed to remote and all
five sub-agents deleted by name: dev-i1-warnings, dev-i2i3-parsecheck, review-i1i4-fixes,
audit-i1i4-fixes, dev-j1j2-floors. `ca-msg-em4` retired earlier, all ten of its sub-agents
confirmed deleted.
**Blocked on:** QA results from the integration hub. The earlier beta escalation (13:20Z) was
overtaken by events — the user chose to deploy to the **integration** hub rather than the beta
hub, which is the lower-risk version of the same experiment and does not need the question
answered first.

**DEPLOYED 2026-08-27 12:37Z.** `scion/messaging-v2` @ `ebf8cc27` is live on **scion-gteam**
(`https://gteam.projects.scion-ai.dev`), deployed by `agent:integration2-operator`. Hub healthy,
5 brokers reconnected, ent AutoMigrate applied the new tables silently, read switch off, no
backfill activity (DEF-12, as predicted). 23 active agents, 39 projects.
**The branch is frozen at `ebf8cc27` for the duration** — I instructed the operator not to
rebase, so QA findings stay tied to a known commit. Branch is 58 ahead of `origin/main` and
**9 behind**; `/readyz` returns 401 because the `isPublicRoute` fix (#1312) is among those 9.
Not a messaging defect. **Rebase is owed before merge.**

**QA is running against a populated production-like hub, which invalidated part of my own
walkthrough.** It tells the tester to message `<some-agent>`; on a hub with 23 working agents
that wakes a real agent mid-task and it acts on `QA check one` as an instruction. Corrected in
flight — use a throwaway target. Recorded because the failure mode is general: **a document
written for an empty environment carries assumptions it never states.**

**DEF-5's premise was wrong and the item is superseded.** I scoped it as "resolution works,
delivery policy is missing." Resolution does not work for either form, for data reasons, and
there is no conversation-driven delivery anywhere — `conversation_id` is a stamp on the message
row, never a routing key. The survey that established this is recorded as **DEF-7 through
DEF-10**; every claim in them I re-verified by grep against `ebf8cc27` rather than taking the
surveyor's word. DEF-5 stays open as the umbrella item but cannot be specced until DEF-7/8/9
are settled, because the delivery policy depends on which of them get fixed.
S5 must document the build **as it ships**
(phase row 12): the read switch is default-OFF, `conv:<id>` and `#<thread>` are **not
available** in the CLI (DEF-5), `@<email>` works only from inside an agent container, and
`@<agent>` is the one reference form a user can rely on today.
**Integration branch head:** `ebf8cc27` (**S5** + closeout log, fast-forward from `19681bc1`).
**Last verified landing on integration branch:** `ebf8cc27` — **S5 accepted 2026-08-27
12:40Z on round 3** (rounds 1–2 rejected: I-1..I-4, J-1/J-2). S4 accepted 10:35Z at `e8a0755d`
on round 4 (rounds 1–3 rejected: F-1/F-2, G-1/G-2, H-1). S3 accepted 06:40Z at `f206a0d9`
(round 2); S2 accepted 03:35Z at `cd4ee7ed` (round 3); S1 verified 01:40Z at `16294728`.

DEF-1, DEF-3 and D3 were all due from S4. **DEF-3 and D3 are discharged.** DEF-1 is
implemented, reachable via `POST /api/v1/conversations/resolve`, and no longer bypassable —
but it is exercised in production only through that endpoint and the CLI's `@<agent>` path.
**It is not yet load-bearing for the read switch**, which resolves from server-side inputs.

S4 round-4 verification (mine, independent of em4's report):

| Check | Method | Result |
|---|---|---|
| **H-1 fixed** | **Mutation:** deleted the gate from `cmd/message.go` in a scratch clone | `TestConvRef_ThreadRefGated` and `TestConvRef_ConvIDGated` both **FAILED**; restored, both pass. Under the same mutation at `765a4ac4` both **passed**. The tests now observe the gate. |
| Commit is tests-only, as scoped | `git diff --name-only 765a4ac4 HEAD` filtered for non-test, non-`.design` | **0 production files.** The two green full `pkg/hub` runs I did at `765a4ac4` therefore still stand and did not need repeating. |
| `cmd`, `pkg/messaging` | full package runs | green |
| Merge is a fast-forward | `git merge-base --is-ancestor origin/scion/messaging-v2 HEAD` | yes — no rebase, no merge commit |

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
| S4 | Surfaces — read switch, CLI split, broker edge | 8, 10, 11 | `ca-msg-em4` | **verified** (`b92926dd..e8a0755d`, 4 rounds) |
| S5 | Docs — skill, docs-site, glossary | 12 | `ca-msg-em5` | next |
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

- `2026-08-27 10:25Z` **S4 round 3 at `765a4ac4`: behaviour accepted, tests rejected.** G-1 and
  G-2 are both correctly fixed and the suite is green and stable (my own runs: `pkg/hub` **0
  failures twice**, `cmd` and `pkg/messaging` green — the DEF-4 baseline holds through thirteen
  commits). One narrow blocker, **H-1**: the two tests named for the G-2 gate execute nothing.
  Scoped to a single tests-only commit; on landing I accept and merge.

- `2026-08-27 10:35Z` **S4 ACCEPTED on round 4 and merged.** `scion/messaging-v2`
  `b92926dd → e8a0755d`, fast-forward, 14 commits. H-1 closed by my own mutation.
  **Rule 13 is the lasting output of this section**, and em4's `EmailRef_AgentContext` test is
  recorded as the model shape for S5/S6.

- `2026-08-27 10:45Z` **S4 closed out; S5 spawned.** em4's closeout commit `19681bc1` (the
  empty-ID guard note and the DEF-5 entry) was pushed to **its own branch only** — correct
  under the branch contract, but it meant DEF-5 was not where S5 would find it. Merged it
  myself after checking it was docs-only and a fast-forward; integration head is now
  `19681bc1`. **Worth noting as a process seam:** "add it to the log on merge" and "merge only
  what I accept" pull in opposite directions at section close, and the loser is the deferred
  item — the exact thing §5c exists to stop being lost. Verified the DEF-5 text is present at
  `.design/project-log/2026-08-27-g1-g2-fix.md:92` before retiring em4. All ten of em4's
  sub-agents confirmed deleted. `ca-msg-em5` spawned and briefed on phase 12; its governing
  constraint is documenting the shipped binary rather than the design's end state, with the
  four availability caveats stated explicitly and rule 13 applied to doc examples.

- `2026-08-27 10:55Z` **D6 issued — how rule 13 applies to documentation.** em5 escalated
  promptly (good) that the repo has no doc-test infrastructure, offering (a) accept the gap or
  (b) build a harness running doc examples against a mock server. **Rejected both.** (b)'s mock
  is the flaw: a harness proving a mock accepts an example is rule 13 in a new costume —
  observing the call, not the effect — so I would be commissioning the defect S4 spent four
  rounds rejecting. (a) is where the F-1 defect goes to live. Ruled **option (c)**, see §5a.

- `2026-08-27 11:10Z` **S5 rejected on round 1** at `eff98a1e`. Four findings — **I-1 is mine**:
  three deprecation warnings on the *already-accepted* integration branch name replacements
  that do not exist. See §5g.

- `2026-08-27 12:05Z` **S5 rejected on round 2** at `e0269857`. I-1..I-4 verified fixed by
  mutation and by a positive control against real docs. Two new findings, J-1/J-2: both new
  tests pass green while examining zero real input. **Rule 14 issued.** See §5h.

- `2026-08-27 12:40Z` **S5 ACCEPTED on round 3** at `55dd6e16`; merged fast-forward, closeout
  `ebf8cc27`. Six mutations reproduced independently. **Implementation complete for S1–S5;
  S6 deferred to post-beta.** See §5i.

- `2026-08-27 12:55Z` **Root cause of I-1 found, and it is the design document.** While
  starting DEF-5 I checked §2.9's verb table against `cmd/schedule.go`. It read
  "`scion schedule message …` | deferred send (**already exists**)". **There is no `message`
  subcommand under `schedule` and there never has been** — the tree is `list | get | cancel |
  create | create-recurring | pause | resume | delete | history` (`cmd/schedule.go:766-774`).
  S4 wrote the `--in`/`--at` deprecation warnings **faithfully against that paragraph**. I
  charged I-1 to my verification of AC-15a; the verification miss was real, but the defect was
  authored here. **A design document that says a capability "already exists" is making a claim
  about code, and nothing was checking it.** Corrected §2.9, Appendix A SEE ALSO, and the
  Appendix A changed-table; the AC-15a amendment keeps the broken string deliberately, as
  history. **Opened DEF-6:** `schedule create` takes `--agent`, not a conversation, so §2.9's
  "fixes by construction" claim about dropped envelopes is unimplemented work.
  **Follow-up worth its one line:** the S5 parse-check would have caught this — the Appendix A
  fenced block contains a bare `scion schedule message` line with no placeholder tokens. Adding
  `.design/messaging-conversation-model.md` to `docFiles` in `cmd/doc_syntax_test.go` extends
  the check to the design doc itself. Specced for the next section; I do not implement.

- `2026-08-27 13:20Z` **Surveyed the conversation delivery surface for DEF-5 and found the
  item's premise false.** Opened **DEF-7** (`#<thread>` matches a field nothing writes),
  **DEF-8** (agent DMs exist as two disjoint rows that cannot find each other), **DEF-9**
  (the addressee table is never written; `DefaultAgentID` never read), **DEF-10** (`@<agent>`
  DMs are project-scoped, contradicting Q2). **Escalated to the user as a beta-scope decision.**
  Method note worth keeping: I used a subagent to sweep the surface, then re-derived every
  load-bearing claim myself by grep before acting on any of it — the survey was also wrong in
  one place, implying unbounded DM row growth, which `findDirectConversation`'s
  participant-based lookup rules out. **Rule 11 applied to a surveyor rather than an auditor.**

- `2026-08-27 14:15Z` Heartbeat: integration head unchanged at `ebf8cc27`, no managers running,
  still blocked on the user's beta-scope decision. **Used the wait to write design §2.4.2 —
  the DEF-8/DEF-10 reconciliation** (`53f40efa`), which is on the critical path under either
  answer, so it does not pre-empt the decision. Decision recorded there: converge on
  `external_ref` as the DM identity key, delete `findDirectConversation`, make all direct
  conversations global. Three alternatives rejected. **The section's sharpest point is the
  migration hazard:** `dm:{sorted(a,b)}` encodes principal *IDs* but not *kinds*, and
  `requireParticipant` will trust whatever the participant backfill writes — so a wrong kind is
  an access grant to the wrong principal. Ambiguity must leave the row participant-less and fail
  closed. Nothing is exploitable today only because every `dm:` row has zero participants and so
  denies everyone. **Do not let a manager treat that backfill as routine data migration.**
- `2026-08-27 12:37Z` **DEPLOYED.** `scion/messaging-v2` @ `ebf8cc27` live on **scion-gteam** via `agent:integration2-operator`. Hub healthy, 5 brokers up, migrations silent, read switch off, no backfill (DEF-12 as predicted). Branch frozen at `ebf8cc27` — operator instructed not to rebase.
- `2026-08-27 12:35Z` **DEF-12 logged**: conversation backfill has **zero production callers** (`git grep Backfill` on `ebf8cc27`, excluding the file and tests, returns nothing). Historical messages will never get a `conversation_id`.
- `2026-08-27 12:30Z` **DEF-11 logged**: divergence board counts every CLI `@<agent>` send as a mismatch; the Hub hand-builds `ConversationResult` with an empty `ExternalRef` so the comparator is fed a blank. Models agree; instrument lies.
- `2026-08-27 12:34Z` QA walkthrough written and pushed (`fd0357d5`, `.design/messaging-qa-walkthrough.md`). Path sent to the user.
- `2026-08-27 12:42Z` Smoke-test scope issued to the operator: baseline the board **before** any send, UUID-identity check, both must-fail cases, and the DEF-8 SQL. Part 4 interpretation **withheld** — raw JSON only, because a red board driven by DEF-11 will mislead anyone reasoning from it directly.
- `2026-08-27 12:42Z` **Walkthrough correction issued in flight**: it says to message `<some-agent>`; on a live 23-agent hub that wakes a working agent, which then acts on the QA text as an instruction. Use a throwaway target. My omission — the doc was written for an empty beta hub.
- `2026-08-27 12:43Z` Heartbeat: no managers running (correct — branch frozen for QA), branch unchanged at `ebf8cc27`/58 commits. Blocked on QA results.
- `2026-08-27 12:58Z` **User challenged whether I had been dispatching managers against the discovered gaps. I had not, and said so.** Correction recorded in §5j.
- `2026-08-27 13:00Z` **QA results from scion-gteam.** Parts 0/1/2 **PASS** — identical conversation UUID across two sends (Created→Resolved), both must-fail cases exit 1 with the refusal text. **DEF-12 confirmed as measurement: 24,684 messages, 0 with `conversation_id`.** **DEF-10 confirmed by direct observation** (row carries non-null `project_id`). **DEF-8 and DEF-11 remain UNTESTED** — see §5k.
- `2026-08-27 13:01Z` Answered the user on DEF-7/DEF-9: DEF-9 needs no input (unbuilt, not undecided; downstream of DEF-5). DEF-7 has one real question, routed to **`nc-arch`** rather than escalated — see §5l.
- `2026-08-27 13:03Z` **`nc-arch` answered DEF-7 and surfaced a parallel-entity collision.** DEF-7 resolved (build no naming path). **DM key format changed to kind-encoding, eliminating the §2.4.2 security hazard outright.** Shared derivation function owed to `pkg/messages`. See §5m.
- `2026-08-27 13:06Z` **ESCALATED to user:** unify `Conversation` and `webchat_topic`, or keep both? Recommended declaring unification the end state and sequencing the migration after native-chat wave 2. **This is the one open user question.**
- `2026-08-27 13:28Z` **DECIDED (user): unification option (ii).** Native chat is fully shipped and done. `webchat_*` is a stable target. Scope narrowed: `Conversation` owns identity across surfaces, `webchat_*` stays the native projection with its own read-state/prefs/presence — a promotion of the identity layer, not a migration of a working system into an unproven one. Design §2.6.3 updated. **The one open user question is now closed.**
- `2026-08-27 13:27Z` **Possible elimination of S6's step 2.** The shipped hub authorises DMs by *parsing the key* (`isDMParticipant`, `handlers_chat_v2.go:2932`) and treats `webchat_dm` as a **derived index for listing**, rebuilt from the key. §2.4.2 conflated index with authority — which is the only reason its migration was security-critical. S6 asked to assess adopting the split for `kind='direct'`. See §5p.
- `2026-08-27 13:26Z` **nc-arch confirmed all four items shipped and self-corrected** — they had answered from a design doc gone stale during a standby period. Also found a live defect: `validDMKey` is enforced only on chat-v2 REST paths; the agent outbound path does not validate `ThreadID`, and `attachments_agent_test.go:290` commits a malformed `dm:<userID>+<agentID>` key as expected usage. **They own that filing.** Consequence for us: never assume a stored key is well-formed.
- `2026-08-27 13:25Z` **CORRECTION: native-chat wave 2 is LANDED on main, not in flight, and the kind-encoded DM key is shipped and regex-validated.** My design duplicated a shipped construct. Second such failure today. Rule 15 added. §5m's framing superseded by **§5o**. Revised recommendation sent to the user; S6 and nc-arch both re-briefed.
- `2026-08-27 17:46Z` **Heartbeat v4** — `cf7b37e7` deleted, `d4dac308` (`ca-msg-impl-heartbeat-v4`) created, verified as the only `ca-msg-impl` heartbeat afterwards. Adds **step 3, BASE CHECK** (rule 24): `merge-base --is-ancestor` plus a `--stat` read for every live manager branch, run *during* the section rather than at acceptance. §2.15's bad base survived a plan review, a design exchange and a completion report because nothing recurring ever asked the question. Step 6 also rewritten to enumerate gates from `ci.yml` rather than naming `gofmt`.
- `2026-08-27 17:12Z` **Heartbeat prompt replaced again** — old `a80a92ed` deleted, new `cf7b37e7` (`ca-msg-impl-heartbeat-v3`), same `13,43 * * * *` cron, verified as the only heartbeat on the roster afterwards. Adds **step 5, MERGE READINESS** (rule 22): `gofmt -l` branch-wide compared against `origin/main`, plus a prompt to ask which other gates `main` enforces that I have never run. Also folds the `scion list | tail` truncation trap into step 1. **Rules that live only in the state doc are advice; rules that live in the heartbeat get executed.** DEF-17 existed for six sections because no recurring instruction ever asked the question.
- `2026-08-27 13:15Z` **Heartbeat prompt replaced** — old `1a899567` deleted, new `a80a92ed` (`ca-msg-impl-heartbeat-v2`). Roster check is now **step 1** and an empty roster is the alarm condition; adds a ledger sweep for unblocked-but-undispatched work, and requires `blocked` to name what is blocked *and what remains runnable*. Closes the §5j blind spot structurally rather than in my memory.
- `2026-08-27 13:15Z` **DEF-1 ledger row corrected to CLOSED** — §3 has said 'implemented' since S4 while the ledger row still read 'open'. Ledger drifted from the body of the same document.
- `2026-08-27 13:15Z` DEF-7 answer written up as design **§2.6.2**; the escalated unification question as **§2.6.3**.
- `2026-08-27 13:07Z` S6 plan received in 3 parts and accepted with corrections; **rule-14 violation caught in its step-4 guards** (vacuous on an empty table). Step 1 in progress. See §5n.
- `2026-08-27 13:00Z` **S6 spawned (`ca-msg-em6`), scope DEF-8 + DEF-10**, spec design §2.4.2, branch `scion/ca-msg-em6` off `ebf8cc27`. Briefed hard on the step-2 security hazard. Merge gated on QA completion. Asked for a plan before the migration is written.

## 5j. Correction 2026-08-27 12:58Z — I stopped dispatching and did not notice

The user asked whether I had been dispatching agents against the gaps found on the integration
branch. I had not. Between em5 retiring at 12:48Z and this exchange, **no manager was running and
six defects were open**, four of them unblocked.

**Two bad reasons I gave myself.**

1. *Blocked on the beta-scope decision.* True for DEF-8/DEF-10 only. DEF-7, DEF-9, DEF-11 and
   DEF-12 never depended on that answer. I let one genuine blocker stand in for a general halt.
2. *The branch is frozen for QA.* This one is worse, because **my own branch contract already
   solves it** — managers work on their own branches and I merge. The freeze gates the merge, not
   the work. I built the mechanism that makes parallel progress safe and then argued from its
   absence.

**The generalisable failure.** Both excuses share a shape: a real constraint on *one* step was
promoted to a constraint on *all* steps, without checking which steps it actually touched. Being
blocked is a property of a task, not of an agent. When I next write `blocked` in §3, it must name
**what specifically** is blocked and what remains runnable — a bare "blocked on X" is how this
happened.

**A second-order point worth keeping.** I was not idle: I was writing docs, briefing the operator,
logging DEF-11 and DEF-12, and answering heartbeats. Visible activity is not progress on the
critical path, and the heartbeat prompt — which asks whether the *active manager* is progressing —
has no question that fires when there is **no** manager at all. It reported healthy every time.
**A monitor that only checks running things cannot see a stop.** Heartbeat handling must now ask:
if no manager is running, is that a decision or a drift?

## 5k. QA on scion-gteam 2026-08-27 13:00Z — what it proved and what it did not

Run by `agent:integration2-operator` against `ebf8cc27`. **Parts 0, 1, 2, 5 executed; Part 3 left
for the user.**

**Settled.**
- **DEF-12 — confirmed, and now a measurement rather than a reading.** 24,684 messages in the DB,
  **0** carrying a `conversation_id`. Total conversation rows: 1, the one the tester created.
- **DEF-10 — confirmed by direct observation.** The resolver-created row carries a non-null
  `project_id`; §2.4.1 and Q2 say direct conversations are global.
- **Parts 1 and 2 pass.** Two sends to the same target returned the *same* UUID with the verb
  changing `Created`→`Resolved`. Both `conv:<uuid>` and `#general` exited 1 with the refusal text.
  No silent success — the failure mode this project exists to eliminate.

**Not settled, and this must not be allowed to drift into "confirmed".**
- **DEF-8 is half-tested.** The observed row matched my predicted *resolver* shape exactly — empty
  `external_ref`, non-null `project_id`, 2 participants. But the prediction was that a **second**
  row exists from the dual-write path, and **that path never executed.** The data is *consistent
  with* DEF-8 and is not a *test* of it. The tester wrote "cannot confirm or deny" unprompted,
  which was the correct call and better discipline than the result deserved.
- **DEF-11 untested.** Divergence board all zeros, before and after.

**One root cause for both gaps, and it is the most useful thing the run produced.**
`agent_not_running` (409) short-circuits **before** the handler where dual-write and the divergence
comparison live. The throwaway agent could not start — no `ANTHROPIC_API_KEY` for the tester's
user — so nothing was delivered. Therefore:

> **The entire new-model instrumentation sits downstream of successful delivery.** It cannot be
> exercised without a live agent, and it observes only live sends. I designed the read-switch gate
> around that board without noticing it has no visibility into anything that fails early, and none
> at all into historical data. **A gate that can only see successful traffic is not a safety gate.**

**Next:** organic traffic from the hub's 23 agents should populate the board without any forced
sends; operator to re-check the board and re-run the Part 5 SQL in a couple of hours. But the
**definitive** evidence for DEF-8 comes from S6's AC-DEF8-1, in a controlled environment where I
can mutate the implementation and confirm the test fails. Production poking cannot do that.

**Out of scope, routed onward:** `SCION_AUTH_TOKEN` is sent by the CLI as an agent token
(`hubsync/sync.go:1366`, `WithAgentToken`), so the documented integration-testing path fails auth;
`SCION_HUB_TOKEN` works. Asked the operator to file it rather than leave it in a Discord thread.

## 5l. DEF-7 routed to nc-arch, not escalated — 2026-08-27 13:01Z

The user asked whether DEF-7 and DEF-9 need his input.

**DEF-9: no.** It is unbuilt, not undecided — §2.4 already specifies the behaviour and nobody
implemented it. It is also downstream of DEF-5. Recording the distinction because "open defect"
and "open question" look identical in a ledger and only one of them needs a human.

**DEF-7: one real question, but the wrong human.** The fix depends entirely on whether `#general`
names a **native chat room** (build a naming path) or a **broker thread** (re-point the grammar at
`external_ref`). Opposite builds. Native chat is a live parallel design in this project (`nc-arch`,
`native-chat-lead`), a room with no name is unusable in a chat UI, so they need conversation naming
whatever I decide — and my grammar is what their UI would have to live with. Deciding alone risks
duplicating or contradicting them.

Asked `nc-arch` four questions: does their design need named conversations; who writes the name and
when; are they already building a create/rename surface I should consume; can two rooms share a
name (which decides whether `#<name>` can be a unique reference at all, or needs a scope qualifier).

**Principle worth keeping: escalate to a human only what no other agent can answer.** A
cross-project design question routed to the other project is not an escalation, it is coordination,
and treating the two as the same thing is how a user's queue fills with questions his own system
already knows the answer to.

## 5m. Cross-project alignment with native chat — 2026-08-27 13:03-13:07Z

Asking `nc-arch` about DEF-7 (§5l) returned far more than DEF-7. **Recorded at length because the
highest-value output of this project so far came from a half-hour conversation, not a section.**

**DEF-7 answered — and my framing of it was wrong.** I offered the user (a) native room vs
(b) broker thread. Answer is (a), but **both my options assumed the naming lived in my entity.**
It doesn't: group threads are `webchat_topic` rows with a required name, unique per project
(case-insensitive), created by `POST /api/v1/chat/spaces/{projectId}/threads` and renamed by
`PATCH /api/v1/chat/threads/{topicId}` — endpoints already in their approved design. DMs are
deliberately **nameless**; display name is derived from the peer at render time. So: build no
naming path, invest nothing in `Conversation.DisplayName`. **A question with two wrong options is
worse than no question — it invites a decision that forecloses the real one.**

Also confirmed: `#<name>` is unique **per project only** (every project has a `#general`), so it is
maximally ambiguous without scope. §2.6.1 ambient-project resolution is correct; never global.

**THE KEY FORMAT — the finding that mattered.** Their DM identity key is
`dm:agent:X:user:Y` / `dm:user:A:user:B`, global pair. **It encodes principal kinds. Mine did not.**

> The entire security hazard in §2.4.2 — the backfill must infer each principal's kind, and
> `requireParticipant` trusts that inference, so a wrong inference is an access grant to the wrong
> principal — **was a property of my key format, not of the problem.** I briefed S6 at length on
> mitigating it. A key that carries the kind means there is nothing to infer. Combined with
> resolver rows already storing participant kinds, the migration is **guess-free end to end.**
> **The hazard is eliminated, not mitigated.**
>
> Generalise: before building careful handling for a hazard, check whether the hazard is inherent
> or self-inflicted. I could not see this from inside my own design.

**Cost of the change: zero, and the window was closing.** QA had just measured one conversation row
in the whole production database and zero `dm:` rows. Nothing to migrate. Untrue the moment
traffic flows — which is why this was worth interrupting a running section for.

**Settled derivation rule** (nc-arch, adopted verbatim): render each participant as
`<kind>:<uuid>`, kind lowercase, UUID normalised to canonical lowercase **before** sorting;
byte-wise lexicographic sort of the two tokens; join with `:`, prefix `dm:`. Because `agent:` <
`user:`, mixed pairs always render `dm:agent:<aid>:user:<uid>`. One rule, no special cases.
Normalisation is load-bearing — a case-sensitive sort over unnormalised UUIDs yields two keys for
one pair. Malformed UUIDs and unknown kinds are **rejected**, not passed through.

**Ownership: `pkg/messages`**, one exported `DMConversationKey` + `ParseDMKey`, imported by both
projects. **Not two implementations that agree by convention — that is exactly how DEF-8
happened.** I refused to reproduce it across a project boundary.

**ESCALATED to the user (the one open question).** `Conversation` and `webchat_topic` are parallel
constructs for the same concept, in two different stores, both under active construction.
(i) minimal — `#<thread>` reads their table, both entities persist; (ii) structural — unify,
`webchat_topic` becomes a chat-specific projection. **My recommendation: declare (ii) the end state
now, sequence the migration after their wave 2 lands.** (i) institutionalises across two stores the
defect S6 is being paid to fix inside one; but their design is approved and in flight, so unifying
now destabilises delivered work for no urgent gain. Declaring the direction buys the thing that
matters: neither project builds more divergence starting today. nc-arch flagged the same to
native-chat-lead. Neither architect should call the sequencing alone.

**Fixed regardless of that outcome:** `UpsertConversationByExternalRef` does an unconditional
`SetDisplayName` on its update branch (`conversation_store.go:400`), silently wiping any
out-of-band name. Added to S6's scope. Agreed with nc-arch; needed either way.

## 5n. S6 exchange 13:05-13:07Z — a rule-14 catch and two manager improvements

**Rule-14 violation caught in S6's step-4 guards, and it is the canonical form of the failure.**
Both proposed guards are **vacuous on an empty table**: 'zero direct rows with empty
`external_ref`' passes when there are no direct rows; 'every `dm:` row has exactly two
participants' passes when there are no `dm:` rows. **Against today's production database — one
conversation row — both would pass on a completely unmigrated system.** Each must assert a
non-zero floor on what it examined before asserting the invariant over it, and fail rather than
skip on an empty population.

**S6 improved on two of my instructions rather than executing them**, which is the behaviour I
want and am recording so it is reinforced rather than lost:
1. **'Verification, not discovery'** — for a kind-encoded ref, parse the kind from the key then
   look up to *confirm* the ID exists in the claimed table. Strictly better than my blanket 'never
   look up': it catches a forged or corrupted key instead of trusting it.
2. **All-or-nothing per row**, which I never specified. A half-backfilled row passes
   `requireParticipant` for one party and denies the other — asymmetric access, worse than denying
   both.

**Final coherent rule** (our messages crossed twice; this supersedes the exchange): unparseable
old-format ref → no lookup, no inference, fail closed, **counted** (silence must be
distinguishable from zero); parseable new-format ref → kind from key, lookup to verify; both →
all-or-nothing per row.

## 5o. CORRECTION 2026-08-27 13:25Z — my design duplicated shipped code

**The user challenged my premise and was right.** I had reported native chat's wave 2 as "approved
and implementation in flight". I got that from `nc-arch` and **passed it on without checking it.**

**Verified against `origin/main`. Wave 2 is landed.** Tables `webchat_topic`,
`webchat_read_state`, `webchat_user_prefs`, `webchat_dm`. Handlers `CreateTopic`, `UpdateTopic`,
`handleTopicPatch`, `handleTopicDelete`. Project-create hook `ensureProjectGeneralTopic`. Tests in
`webchannel_store_wave2_test.go`.

**And the DM key is shipped and regex-validated** (`pkg/hub/handlers_chat_v2.go:390`):

```
dmKeyRegexp = ^dm:(user|agent):[0-9a-f-]{36}:(user|agent):[0-9a-f-]{36}$
validDMKey()   parseAgentDMKey()   dmUserParticipants()
```

> **So §5m was wrong about what happened.** It was not two designs converging. **My design
> invented a second, incompatible DM key format for a concept that already had a shipped,
> validated one in the same repository.** The elaborate principal-kind security hazard in §2.4.2 —
> which I briefed S6 on at length — existed because I did not grep before speccing. A format
> already in the codebase had solved it.

**This is the second instance today of the same failure.** §2.9 asserted `scion schedule message`
"already exists"; it never did (that produced I-1). Now §2.4.2 invented a key that already existed.
**Both are my design making a claim about code without verification, while I demanded
mutation-level proof from every manager.** The standard I enforce downward has not been applied to
my own documents or to peer architects' descriptions of their systems.

**Standing rule, added as §1 rule 15:** *before speccing a mechanism, grep `origin/main` for it.*
And: **treat another agent's description of their own shipped system as a claim to verify, not as
evidence.** nc-arch was describing their design doc; the code had moved past it.

**Not duplication: scope.** `webchat_*` is explicitly web-only (Discord's tables live in
`extras/scion-discord`). `Conversation` spans native/discord/slack/telegram/gchat/teams with
`external_ref` and drift state. Mine is a **superset abstraction built without noticing the shipped
subset underneath it.** Real distinction; does not rescue the key duplication, which is pure.

**Revised recommendation on §2.6.3, sent to the user.** The "sequence after wave 2 lands" caveat is
void — it has landed, and a finished system is safer to migrate than a moving one. Direction:
`webchat_topic` becomes a projection of `Conversation`. **But with a caveat that cuts against my
own work and which I put in writing to the user:** webchat is shipped, working and populated;
messaging-v2 has six open defects, a read switch that cannot be turned on, and one conversation row
in the only database it has touched. **Mine earns the role of core model by closing its defects,
not by being newer.** Migrating live chat data into a model that cannot turn on its own read switch
would trade a working system for an architectural preference.

Order: (1) S6 adopts the shipped key as *the* format, `pkg/messages` owning one derivation and the
hub's existing helpers becoming consumers — not a third implementation; (2) DEF-8/10/11/12 close;
(3) unify. Pulling (3) earlier is a risk-appetite call for the user, not for me.

**Asked `nc-arch` directly whether anything else they described as designed is already built**, and
to answer from the code rather than the design doc. Better an awkward question now than speccing
around a phantom twice.

**Free conformance test now available to S6:** the shipped `dmKeyRegexp` is an independent oracle.
Its derivation must produce keys that satisfy the *real* regex — referenced, not copied. A local
copy would drift, which is the disease itself.

## 5p. Index vs authority — the shipped pattern that may delete S6's riskiest step

Found by reading `origin/main` after the §5o correction, rather than by being told.

| | shipped hub | my §2.4.2 as specced |
|---|---|---|
| **Authorization** | `isDMParticipant(key, callerID)` — parses the key, three lines, **never reads a table** (`handlers_chat_v2.go:2932`) | `requireParticipant` reads `conversation_participants` |
| **Listing** | `webchat_dm`, PK `(participant_id, conversation_key)`, rows **derived from the key** by `registerDMParticipants`, no-op on a malformed key | the same table, the same rows |

> **For a DM, the key already *is* the participant list.** The hub therefore treats participant rows
> as a **derived index for listing**, never as the **authority for access**. My design conflated the
> two — and that conflation is the *entire* reason its migration was security-critical. If access is
> decided by parsing the key, a wrong row degrades from "access granted to the wrong principal" to
> "a DM appears in the wrong list": recoverable, and rebuildable from the key at any time.

**Second time in one day that shipped code deleted a hazard I was busy mitigating**, and the more
instructive of the two. §5o was a duplicated *artefact*; this is a duplicated *concept* — I modelled
DM authorization on the general conversation case (where a participants table genuinely is required,
because the key does not name participants) without noticing DMs are the case where it is not.
**Generalising a mechanism across a case that does not need it is how the hazard got manufactured.**

Asked S6 to assess, not ordered: authorise `kind='direct'` from the key; keep the table as an index
and as the authority for kinds where the key does not name participants. Explicitly invited S6 to
reject it — it has overruled me twice today and was right both times. Also asked nc-arch whether
there is a reason they kept both that I am not seeing.

**Two items adopted from nc-arch regardless of the above:**
1. **"One derivation" needed an asterisk.** Go and the TS client (`web/src/components/pages/chat.ts:2325`
   `buildDMKey`) cannot share an implementation. The real guarantee is one spec, one exported Go
   function, and the TS mirror **pinned by shared golden test vectors** consumed by both suites, with
   server-side validation as the enforcement point. Cross-language convention-agreement is
   unavoidable; golden vectors make it *checked* convention. In S6's scope.
2. **Never assume a stored key is well-formed.** `validDMKey` is enforced only on the chat-v2 REST
   paths; the agent outbound path does not validate `ThreadID`. A committed test bakes in a malformed
   `dm:<userID>+<agentID>` form — **worse than the missing validation, because it will defend the bug
   in review.** nc-arch owns the filing.

## 5ah. 18:45Z — strategy cut over to incremental; DEF-19 found within minutes and dispatched

**User directive 18:30Z: "let's start getting this landed in a safe and incremental way", then
"proceed" at 18:34Z.** The integration branch is abandoned as a merge unit. Landing plan is §1b.

**The plan was made tractable by something I had not noticed until I looked:** the branch's
first-parent history is already phase-ordered (phases 1-11), so tranches follow the build order
instead of cutting across it. I had been describing the branch as an 81-commit lump for two days.

**Two non-obvious consequences of going incremental, both found by checking rather than assuming:**

1. **Tranches must be cut as final state, not replayed commits.** S6/S7/S8 and §2.15 all repaired
   defects in phase-1-5 files. Replaying the original phase commits would land code we already
   know is broken, then fix it three PRs later.
2. **CI gates are per-tranche, not once.** The 15 gofmt violations span phases 3, 5, 6 and 9, and
   `golangci-lint` runs `--new-from-merge-base=origin/main`. Fixing formatting once on
   `messaging-v2` would leave every tranche PR red. I had told the user "that is the first commit";
   it is not, it is a step in each of seven.

**DEF-19 — the incremental decision paid for itself inside six minutes.** Chasing an aside in
em6's §2.15 report, I probed `ValidateLegacyMessage` directly and found it rejects `group[]`
recipients, a shipped documented CLI feature. Root cause: `buildAddressees`
(`envelope_compat.go:229-250`) treats `Recipient` as exactly one principal and splits on `:`,
yielding principal kind `group[agent`. Validation at `:630` precedes group dispatch at `:669`, so
every `group[]` message 400s. Absent from `origin/main`, so it is ours. Specced (`def19-spec.md`)
and dispatched to **`ca-msg-em9`**, which is independent of §2.15's files.

**§2.15 verified independently and NOT merged.** My own run at `457149b9`: 8 packages green,
`pkg/hub` 274.7s, EXIT=0; both new tests execute, no skips; base re-verified.
**Then mutation found the defect that green hid.** M1 (`!validDMKey(req.ThreadID)` -> `false` at
`:121`) killed **only the status assertion** at line 856 (400 -> 503). Both persistence
assertions — message-count and conversation-count unchanged — **survived**, because with the guard
disabled the request dies at `503 broker_unavailable` and nothing persists anyway.

So the test proves `validDMKey` returns 400. It does **not** prove `validDMKey` is what prevents
the message row, which is exactly the claim em6's report makes. **The row-count assertions cannot
distinguish "the guard blocked it" from "the broker blocked it"; they pass either way.** Rule 14,
sitting inside the test written to settle the question. What is missing is a **positive control**:
without a case showing a well-formed key *does* persist in this environment, the negative
assertion is decoration. Sent back to em6 — the fix is small, and I explicitly did not ask them to
change the conclusion, only to stop the test claiming more than it demonstrates.

**Note on my own mutation practice:** I nearly recorded "M1 KILLED, test is specific" and moved on.
The kill was real; the *specificity* was not what it appeared. **Asking which assertions died,
rather than whether the test died, is the whole difference** — and it is the discipline I have been
demanding of managers all day while checking only the exit code myself.

## 5ag. 18:30Z — the blocker I owed myself; unification spec drafted

Heartbeat step 7 ("replies owed") caught what my own status message got wrong. I had told the
coordinator **"runnable meanwhile: nothing safe"** — true of anything touching em6's files, false
overall. The unification spec owed to nc-arch since 13:28Z is pure design work with **zero file
contention**: my scratchpad, my branch, no overlap with `messaging-v2`. Rule 28.

Worse, DEF-5 and DEF-7 both carried "depends on the §2.6.3 unification decision" — and the
decision was made at 13:28Z. **The dependency was on my own unwritten draft**, recorded in a form
that reads as external.

Drafted `unification-spec.md` (§2.6.4). Grepping the core nouns first (rule 16) changed three
things I would otherwise have written from memory:

1. **The branch implements the opposite of the decided direction.** §2.6.3 decided the reverse
   pointer (`webchat_topic.conversation_id`); `pkg/messaging/conversation.go:158-162` mints
   `surface=native` Conversations keyed by `external_ref="thread:<proj>:<threadID>"`. When
   `<threadID>` is a topic UUID that **shadows a `webchat_topic` row with no link** — DEF-8
   reproduced across the store boundary, by the section built to eliminate it. Pre-beta only;
   `conversation.go` is not on main.
2. **`webchat_topic.default_agent` holds a slug *or* a UUID**, disambiguated by a read-time
   fallback chain (`handlers_chat_v2.go:938-941`), while `Conversation.default_agent_id` is a typed
   UUID. Not just duplicated — **incompatible domains**, so the migration is lossy and slug→UUID
   can fail. Resolution: unresolvable → NULL (§2.4 case 3, a defined outcome), never a guess,
   with an operator-reviewed report. Under-granting is recoverable.
3. **This inverts §2.13.4.** I wrote there that unread data has never been tested by use, and
   used it to argue *my* three `DefaultAgentID` writers were fine. The same argument says
   native's column — the one actually read, at `:935` — is the tested one and **mine are the
   unproven target**. I had the right principle pointing the wrong way.

Also found: `Conversation.project_id` is `Optional().Nillable()` while `webchat_topic.project_id`
is `NOT NULL` — **the identity layer is weaker than the projection it claims to own**, and project
membership is one of the two authorization sources. AC-U-4.

I verified nc-arch's name-uniqueness claim rather than repeating it (rule 15): true,
`webchannel_store.go:508`, case-insensitive per project excluding deleted.

Three open questions, raised serially **per recipient**: Q2 (Ent/raw-SQL shared transaction) to
nc-arch now, because it gates the phase breakdown and could reopen alternative (A). Q1
(default-agent behaviour change) is the user's, and **held** — they already have an unanswered
question from me on the integration-branch strategy. Q3 (`is_general`) follows Q2.

`main` moved `98a9d9c2` → `b09e7f49` during this. Citations are timestamped, not permanent.

## 5af. 18:12Z — em6's rebase verified, and rule 18 caught me a second time

em6 rebased and pushed without announcing it. I found out by accident: preparing an unrelated
`cmd/` cleanup, I ran `git diff --name-only b7669831 origin/scion/ca-msg-em6` — their *old* base —
and got back `cmd/deploy_instance.go`, `cmd/helm_chart_ha_contract_test.go`, `cmd/hub_token.go`.
Nonsense for a key-derivation section. Had I trusted it I would have opened a second false alarm
against a manager who had just fixed the first one.

**This is rule 18 in a costume I did not recognise.** The rule says compare against the merge
parent, never a branch head that has moved. I obeyed it for the head and violated it for the
*base* — I cached `b7669831` as a fact when it was a fact with a timestamp. Symmetric with §5x,
where I nearly rejected a merge on a manufactured finding. **A stale reference point produces
confident, specific, entirely fictional diffs, and they read exactly like real ones.** Re-fetch and
re-derive both endpoints at the moment of comparison; never carry either across an interval in
which the other side was working.

Re-checked properly. `git merge-base --is-ancestor edd4e4bd origin/scion/ca-msg-em6` → **PASS**.
Five commits on the integration head. `git diff --stat edd4e4bd origin/scion/ca-msg-em6` → 16
files, +1,694 / −108, every one a §2.15 file; zero whole-file deletions. The 299-file, 80,471-line
revert is gone.

**The controls that were missing are back, and the conflict resolution kept them.** This was the
real risk of the rebase — the conflicts land exactly where DEF-11 and #1319 live, and a
resolution that dropped them would look identical to the pre-rebase state I had just rejected.
Counts in `handlers_agent_messaging.go`, em6 vs integration head: `lookupFailed` 3 = 3, `Fallback`
2 = 2, `validDMKey` 2 = 2, plus `DeriveConversationKey` 3 (new). Preserved, not re-implemented.

**Item 2 has evaporated exactly as predicted, and Item 1 is now a genuine question for the first
time.** `validDMKey` and `DeriveConversationKey` coexist in one handler, so em6 must state what
each is for rather than assume the newer subsumes the older. `dev-validdmkey-test` is running that
check now. Awaiting their report; I have not interrupted them, and I did not pre-empt the answer.

## 5ae. 17:48Z — the user calls the integration-branch strategy flawed, and I think they are right

Verbatim: "this approach of large integration branch is prob a flawed strategy". I proposed this
strategy, so the honest test is whether the evidence supports it, not whether I like it. It does
not.

**Every defect found today is an integration defect.** DEF-8, DEF-15 and DEF-16 are gaps *between*
sections — my own QA walkthrough already said it: "each section did what it was asked; nobody was
asked to join them up." DEF-17's three red gates were invisible per-section and legible only
branch-wide. §5ad's near-revert happened because a long-lived branch requires managers to be
manually re-pointed at a moving target, and manual re-pointing fails silently.

**The design error, stated precisely: we run a default-off read switch *and* a long-lived branch.**
Two mechanisms doing one job, and we pay both bills. The flag is what makes partial work safe on
`main`. The branch adds no CI over 81 commits, manual base management, late integration discovery,
and one high-variance merge — while buying nothing the flag does not already buy. Review is not the
branch's contribution either; a PR is reviewed too.

**The sharpest cost is CI.** `main` enforces seven gates. The integration branch enforces me. I
substituted myself for a mechanism that already existed and is better at this than I am, and three
gates then stayed red across six sections. Rule 22 was the symptom; this is the cause.

**One argument that cuts against my own position, recorded because it is the strongest one and it
still loses.** The read switch gates *reads*, not *writes* — the conversation dual-write is
unconditional. So landing incrementally would have started writing conversation rows in production
much sooner, which is real risk. But DEF-8's duplicate rows would then have appeared on the
divergence board under live traffic, cheaply and early, instead of being found by me reading code
three days later. **Telemetry under real traffic beats an architect reading code**, and the branch
denied us that for the entire project.

**Proposed cut-over, and the timing is nearly free.** §2.15 must be rebased regardless (§5ad), so
rebasing it onto a better target costs nothing extra. Sequence: fix DEF-17/DEF-18's three gates,
PR the 81 commits to `main` as one reviewed change, then §2.15 rebases onto `main` and every
section after is an ordinary PR. It cannot rebase onto `main` before that — it depends on
`conversation.go` and `backfill.go`, which exist only on the branch. **Awaiting the user's
decision; this is their call, not mine.**

Note em6's in-flight rebase onto `edd4e4bd` is correct under *either* outcome, since `edd4e4bd`
would itself be in `main` after the cut-over. No need to interrupt them, and I have not.

## 5ad. 17:41-17:45Z — §2.15 reported complete on a branch that reverts 80,471 lines

`ca-msg-em6` reported §2.15 done: five commits, all ACs satisfied, my five required changes
implemented, code reviewer APPROVE, security auditor 0 Critical / 0 High, suite green. It also
asked two questions, and the questions are what saved it — I went to verify their premises and
checked the base on the way.

**`git merge-base --is-ancestor e2b5c37d origin/scion/ca-msg-em6` → false.** Base is `b7669831`,
inside their own already-merged DEF-8 work. `git diff --stat` against the integration head: **299
files, +20,439 / −80,471.** Twenty-three commits reverted, among them the entire `origin/main`
merge. `pkg/store/entadapter/{role_store,credential_store,decision_audit_store}.go` — other teams'
files — simply absent. This is the failure the user named explicitly when the project started.

Cause is mundane: I merged their DEF-8 branch into the integration branch, they carried on from
their own head, and nothing ever re-pointed them. **I named the base in the dispatch and never
verified it** — not at plan review, not when they asked me a design question, not on the completion
report. Rules 24 and 26.

### The two escalations were the same bug wearing a costume

Item 1: "Agent 2 removed the `validDMKey` checks at :121 and :606, should we restore the 400?"
Item 2: "Agent 2's repoint dropped DEF-11's `lookupFailed`/`Fallback`, fix now or follow-up?"

Neither control was ever in their tree. `git grep validDMKey` on their branch hits only
`handlers_chat_v2.go`; the `handlers_agent_messaging.go` and `handlers_broker_inbound.go` instances
came from #1319, which they do not have. `lookupFailed` returns **zero** hits on their branch and
three on the integration branch. **A developer cannot delete code that was never there.** They read
an honest diff against the wrong reference point and concluded their own sub-agent had stripped two
safety controls.

**And this is the part I want to keep.** Both were posed as small, reasonable repairs. Had I
answered them as asked — yes restore the 400, yes re-add the Fallback entry — their agents would
have hand-written fresh implementations of #1319 and DEF-11 onto a tree missing 23 commits. The
suite would pass. Both controls would read as present to any reviewer. And the branch would still
revert 80,471 lines, now with the two most visible symptoms repaired. **The fix would have
destroyed the evidence for the defect it was reported as.** Rule 25.

Symmetry with §5y worth noting: there, S6 deleted a test line because the code "no longer used
ThreadID" and buried DEF-15. Here, S6 nearly *added* code because two controls appeared missing.
Opposite actions, one shape — **a local repair reasoned from an unverified claim about the tree,
which makes the tree agree with the belief.**

### What the quality gates did and did not do

The code reviewer and security auditor were not careless. Their findings are good; the auditor's M1
(`handleGroupMessage` at `:1120`/`:1245` still bypassing `DeriveConversationKey` — a section titled
"one derivation" shipping two) is a real item I have kept open. **All three reviewed the change;
none reviewed where it started.** More reviewers would not have helped: they share a working tree,
so they share its blind spot, and the omission is correlated rather than independent. Base
verification is a different question asked once of the ref, not more eyes on the diff.

Instructed: hold both items, rebase onto `edd4e4bd` (not `e2b5c37d` — DEF-13 landed since), expect
conflicts in `handlers_agent_messaging.go` and treat them as the substance, re-run every AC
including the DEF-16 mutation and the golden vectors because green was measured against the wrong
tree, and report with the `merge-base` and `--stat` output included. Item 1 becomes a genuine
question only after the rebase, and my position for them to test rather than adopt: `validDMKey`
guards the legacy `messages.thread_id` sink, `DeriveConversationKey` guards the conversation row,
and Phase 5's non-fatal contract was written for the latter — it does not automatically extend to a
control that predates it and protects something else.

## 5ac. Heartbeat 17:13Z — the new step worked, and immediately proved itself too narrow

First run of heartbeat v3, which I had rewritten twenty minutes earlier to add a merge-readiness
step after finding 18 unformatted files. The step says: run `gofmt -l` branch-wide, diff against
`origin/main`, **then ask what other gates `main` enforces that you have not thought to run.**

That second sentence was the one that paid. I opened `.github/workflows/ci.yml` — for the first
time in three days — and found **seven** gates where I had been assuming one.

| Gate | `scion/messaging-v2` | Notes |
|---|---|---|
| Format Check (`gofmt -l .`) | **FAIL** 18 files | main: 0. Hard `exit 1` in CI. |
| `make lint` (vet) | pass | |
| `make compat-literals` | **FAIL** 11 hits | `cmd/broadcast_test.go`, `cmd/message_deprecation_test.go` — **neither file exists on main**, so all ours. |
| `./hack/check-authz-guards.sh` | pass | "analysed `e2b5c37d`, no violations". |
| `golangci-lint --new-from-merge-base=origin/main` | **FAIL** 7 issues | 2 `ineffassign`, 5 `staticcheck`. The flag scopes it to new code, so every hit is ours **by construction**. |
| `make build` | pass | |
| shellcheck + 4 web jobs | n/a | diff vs main touches no `.sh`, nothing under `web/`. |

**Three red, not one.** I had found the formatter, written a rule about it, rewritten the heartbeat
around it, and reported it — all while still holding the belief that produced the gap. The belief
was not "gofmt doesn't matter"; it was that I knew what CI checked. I was right about one gate in
seven and would have declared the branch merge-ready on that basis. Rule 22 amended accordingly:
**checking a gate you have not read is checking your memory of it.**

Two things worth separating out.

**The reassurance I would never have collected.** `check-authz-guards.sh` passes clean. Given that
this project has spent the day on DEF-14 and DEF-15, an authorization-bypass pattern scanner
reporting no violations across our diff is genuine evidence — and I only have it because I went
looking for failures. Enumerating a gate returns the passes too, and those are worth having.

**A defect the linter found that review had not.** `ineffassign` on
`pkg/messaging/validate.go:126` led to DEF-18: `projectAgents` is declared `// for error
reporting`, appended to at three sites, and never read — the AC-33 violation error is a constant
string that names nothing. This is the cross-project isolation check, the one carrying DEF-2. Not
a hole; the refusal is correct. But it is a security control whose refusal cannot say what it
refused, and the comment has been claiming otherwise since it was written. **A style linter found
a diagnosability defect in an authorization check that three section reviews and two architects
read past** — because we were all reading it for whether it *decides* correctly, and nobody read
it for whether its refusal is usable.

## 5ab. 17:01-17:12Z — DEF-13 reviewed; a tripwire that guards the wrong direction, and a gate I never ran

S8 reported DEF-13 complete on `scion/ca-msg-em8` (`2e6178ee`, `04b16214` off `e2b5c37d`), with an
internal code review of APPROVE, no Critical or Required findings, and two Optional nits. Rule 18
sweep against the merge parent: 4 files, +209/−32, no deletions. Clean.

**Two of the three changes I required are genuinely load-bearing, and I confirmed it by mutation
rather than by reading the tests.** M3 — inserting `scion message conv:<uuid>` into
`messageCmd.Example` — was killed by `cobra_help_deny_list` with a message naming both the pattern
and the source (`from cobra-help-text`). That is specificity, which is the signal; a kill alone
would only have proved reachability. The deprecation-linkage test carries a `checked > 0` guard,
which is rule 14 applied correctly and without being told.

**The third change does not do its job, and its own disclosure points at the wrong hole.** The
tripwire is `require.Equal(t, 4, int(messaging.RefThread))`, protecting a hand-maintained table of
reference forms. S8's review logs the limitation as "blind spot if a new kind is inserted before
RefThread", attributing it to Go's inability to enumerate iota constants. Both halves are wrong.

`RefThread` is the **last** member of the enum (`pkg/messaging/resolve.go:44`). I ran both
mutations:

- **M2, insert a member before `RefThread`** → `RefThread` becomes 5 → **KILLED.** The case S8
  declared uncovered is the one that works.
- **M1, append a member after `RefThread`** → `RefThread` stays 4 → **SURVIVES**, suite green.

Appending is how a Go iota enum is extended essentially every time. So the guard covers the rare
direction and misses the common one — and someone adding `RefRoom` without touching `Long`
reproduces DEF-13 exactly, through the mechanism built to prevent it.

**The disclosure is the worse half.** An undisclosed blind spot is found by the next person who
looks. A blind spot disclosed in the wrong direction is *not* found by the next person who looks,
because they check the nit, see the risk acknowledged and reasoned about, and move on. Honest
disclosure bought false confidence. Hence rule 23: an admitted limitation is a claim about code and
gets mutated like one.

The "Go limitation" defence also fails a grep. `pkg/hub/project_settings_registry_test.go:19-20`
imports `go/ast` and `go/parser` and does precisely this — parses its own source file, counts
constants matching a naming convention, asserts the count against a registry so that adding one
without registering it fails. **Prior art, in this repo, for the problem declared unsolvable**
(rule 16, applied to a review's reasoning rather than to a design). Required S8 to replace the
value pin with an AST count and to prove it with M1, and to remove the old assertion rather than
keep both — a weak check beside a strong one only creates ambiguity about which is authoritative.

### The finding that is mine, not S8's

Chasing their "struct alignment is cosmetic" nit, I ran `gofmt -l` — **for the first time in this
project.** `cmd/message_help_test.go` is dirty. So are two neighbours, but both were already dirty
at the merge parent. So I ran it branch-wide: **18 files.** Then against `origin/main`: **0**.

Every violation is ours, accumulated across S5–S8, one or two files at a time — always small
enough per section that nothing looked wrong, only visible at the branch level, which is the one
level nobody was inspecting. Each manager reviews their own diff; I review their tests. Logged as
DEF-17, owned by me, swept in one commit after both in-flight branches land so the sweep does not
conflict with them.

**I accepted six sections on full-suite green and never checked whether the branch could merge.**
The suite answers "does it behave"; the formatter answers "will it land". I had silently equated
verification with the first question because `go test` was the tool I had reached for on day one.
Rule 22. Merge-readiness — formatter, and anything else `main` enforces that I have not thought to
run — is now a heartbeat step, diffed against `origin/main` rather than against the branch's own
previous state, since a branch that has been drifting for six sections agrees with itself perfectly.

## 5aa. 17:00Z — the section named "one derivation" was about to ship six

S6's §2.15 plan was strong: right decomposition, right sequencing, a five-step mutation protocol
for DEF-16 they wrote without being asked. They also asked the right question and offered the
wrong answer, and the wrong answer was the plausible one.

**Their question:** case 1 has to reject non-canonical `dm:` keys. Either (a) re-derive with
`DMConversationKey` and compare, or (b) check the UUID segments are lowercase. They leaned (b) —
simpler, and it avoids re-deriving from parsed halves, which my own spec had warned against.

**(b) is unsound, and the reason is a detail nobody would guess.** `uuid.Parse` accepts 32
unhyphenated hex digits, `{braced}` and `urn:uuid:` forms. So
`dm:agent:<32-hex-no-hyphens>:user:<uuid>` is all-lowercase, parses, passes the lowercase check —
and is a different string from the canonical key for the same pair. Two external_refs, one DM,
two rows. **DEF-8, reintroduced by the function whose entire purpose is to prevent it.**
`ParseDMKey` also does not enforce token order while `DMConversationKey` sorts, so (b) admits
`dm:user:<u>:agent:<a>` as well — a key #1322 refuses at ingress.

The rule worth keeping: **canonicality is defined by the function that produces keys, not by a
list of properties canonical keys happen to have.** An attribute list drifts from its subject as
the producer changes; a round-trip cannot.

**My own spec contributed to the wrong answer.** I wrote "return the key verbatim, do not
re-derive from parsed halves", and S6 read that as forbidding (a). It does not — it governs the
*return value*. Comparing against a re-derivation is fine; returning it is not. **A rule stated
as a prohibition on a technique, when what it actually protects is an outcome, will be obeyed in
the wrong place.** Design updated to say which of the two it means, and to name the real trap:
never normalise, because silently rewriting a key makes the stored identity differ from the
string a caller may already have authorised against — §2.15.4(c)'s read-gate normalisation moved
to the write side.

**The finding neither of us had: the section ships six derivations, not one.**
`DirectMessageExternalRef` (`divergence.go:132`) builds the kind-free `dm:{sorted(idA,idB)}` form
and **never returns an error** — its doc says an empty ID "produces a ref that makes the
divergence visible", the exact inverse of this section's fail-closed rule. Its only production
caller is `backfill.go:201`, which phase 4 repoints, after which it is production-dead but still
exported and callable in the same package. **A section titled "one key derivation, not five"
would have shipped with an exported non-failing alternative sitting beside it.** Neither the
plan nor my own spec caught it; a grep for callers did.

**And a stale-comment trap of exactly the DEF-15 shape, in the file S6 is about to edit.**
`backfill_test.go:786-790` asserts the old key format, justified by the comment "must match
DirectMessageExternalRef (dual-write format)". Dual-write was converged onto `DMConversationKey`
by S6's *own* DEF-8 section, so the test pins the old contract while claiming the authority of
the new one. The change to it is correct — and I told them that being correct is not what makes
it safe. **Changing evidence so it agrees with new code is how DEF-15 got buried. Saying so out
loud in the commit is the difference.**

Also required: AC-DEF15-1 becomes a source-reading test rather than a grep (a grep runs when
someone remembers, and this is an architectural constraint that must outlive readers of this
document), and the delegation must log a canonicality refusal distinctly from a resolution miss —
following S7's `Fallback` pattern rather than inventing a second one, because **a refusal that
looks like "not found" on the divergence board is a defect you cannot see.**

## 5z. Heartbeat 16:43Z — a rule that outlived its own reason

**Roster: healthy, and I checked it properly.** `scion list | tail -20` did not show
`ca-msg-em6` and my first reading was that the section manager had vanished — the §5j alarm
condition. It had not: the list sorts by uptime and `tail` cut off the newest entry. Re-ran with
a grep and found it running, active 35s prior. **The near-miss is worth recording because the
failure mode is symmetric with §5j:** that heartbeat reported healthy through a full stop by
asking too narrow a question; this one nearly reported a stop that had not happened by using a
truncating command. Neither is a judgement error. Both are the tool shaping the answer.

**The real finding: rule 2 was blocking work for a reason that did not apply.** It said "one
active manager at a time", justified by merge conflicts I would have to adjudicate without
having written the code. That justification is about *shared files*. The rule was written as a
*headcount cap*. Those coincide right up until two sections are disjoint — and §2.14 (DEF-13,
`cmd/message.go` help text) shares no file with §2.15 (`pkg/messaging/`,
`handlers_agent_messaging.go`).

Worse, my own §2.14.6 phase list already said DEF-13 should "land first so the smaller change is
not queued behind the larger one." **The rule was enforcing exactly the queueing my plan
existed to prevent, and I would not have noticed without the heartbeat's instruction to ask what
*specifically* blocks each open item.** "A manager is busy" is not a blocker on a task; it is a
fact about an agent. The heartbeat says that in as many words and I had been reading past it.

Rule 2 amended: sequence by file contention and supervision cost, not by count. Dispatched
`ca-msg-em8` on DEF-13 alone, off `e2b5c37d`, with an explicit list of paths that are not theirs.

**Standing caution attached to my own amendment.** Earlier the same day, supervising two
managers, I nearly sent a false rejection (§5x). Supervision quality is the binding constraint
and it degrades quietly — unlike a merge conflict, which announces itself. If a third section
becomes dispatchable I should be slower than this.

**DEF-2 is closed, and closed in a way that will break silently.** Checked it because the
heartbeat asks for every open ledger row, expecting to strike it out.
`ValidateCrossProjectAddressees` is real (`validate.go:104`) and wired (`:645`, over
`mentionAddrs`). But mentions are the only source of addressees *because DEF-9 is open* — the
addressee table is never written. So AC-33 has full coverage only while another defect persists.
**A defect whose closure is load-bearing on another defect staying open is not closed.** Whoever
closes DEF-9 will add addressee sources and will have no reason to read the DEF-2 row. Recorded
in the row itself, with the seam named (`ValidateMessageAddressees`, zero production callers).

That is the second time today the ledger has been the thing that caught something: DEF-12's
stale "unblocked" nearly got dispatched onto a bulk defect. **The ledger is not documentation of
decisions already made. It is the only artifact that re-asks them.**

**16:55Z — S8's DEF-13 plan reviewed, approved with three changes.** Good plan; found
`doc_syntax_test.go` unprompted and cited `resolve.go:36-45` correctly. The rejection-grade
problem was that **their test could not catch a recurrence of the defect it exists to prevent**:
a hand-written four-row table with a hand-written floor of `>= 4`. Both sides of the check come
from the same source, so the floor catches an emptied table and not an *un-grown* one — and
"a form exists and the help text does not mention it" is DEF-13 itself.

Go cannot enumerate an enum, so this cannot be fully derived. Required instead: a tripwire on
`int(RefThread) == 4`, **documented in the test as a tripwire and not as coverage.** Recording
the general form, because I have now issued rule 14 three times and each time its surface was
different: **rule 14 is not "assert a count". It is that a check whose input can silently stop
covering is not a check** — empty is only the most obvious way to stop covering, and a
hand-maintained list is the most common one.

Second change: their plan fixed the reported defect *by coincidence*. The user's actual complaint
is that the deprecation warnings at `:86-91` name `@<agent-name>` while `Long` never defines it.
Documenting `@` satisfies that accidentally; nothing asserts the link. Required: hoist the
replacement strings to a table both the emitter and the test read, then assert every reference
form named in a warning appears in `Long`. **That is the one part of this section that can be
genuinely derived rather than tripwired**, and it generalises to warnings nobody has written yet.

Third: I checked the deny-list myself rather than accepting their read of it.
`doc_syntax_test.go:151-162` scans **four markdown files** and does not look at cobra help text,
so no collision — but that is an exemption by omission. **The most-read documentation surface in
the product is not covered by the control that polices documentation.** Asked them to add the
cobra tree's `Long`/`Example` to the scan, and to document the gated forms in Recipients only,
never in Examples: describing a form and demonstrating it are different acts.

## 5y. 16:17-16:25Z — S7 verified by mutation; a defect that hides behind its own fix

Three things landed in the same eight minutes and they interact.

**S7 (DEF-11) passed, and I did not accept it on the test results.** Four green tests prove
nothing on their own — S5 cost three rounds and S6's round 3 was settled only by a mutation. So I
mutated three separate lines of the fix and required each to kill a *named* test. All three did,
and each killed only the tests that should die. `GenuineDisagreement` survived all three, which is
the part I actually wanted: it shows the mutations are hitting specific behaviour rather than
breaking the package wholesale. **A mutation that fails everything proves reachability. A mutation
that fails exactly one named test proves observation.** Only the second is evidence.

**S6 did the right thing with DEF-15 and produced a better finding than the one I asked for.**
I asked them to restore the deleted `ThreadID` line and report what it produced. They did, and
also noticed that the rejected request *still left the conversation row behind* — the dual-write
runs before `ValidateLegacyMessage`. I confirmed the ordering myself by grep rather than accepting
it, and found the sharper version: **the two ingress handlers do the same two operations in
opposite orders.** `handleAgentOutboundMessage` writes at `:245` then validates at `:288`;
`handleAgentMessage` validates at `:615` then writes at `:848`. Nothing in the code says which is
intended. Logged DEF-16, issued rule 19.

**I told S6 not to land the restored test red, and the reason is the interesting part.** The test
fails at 400 `thread_id requires channel to be set` — our own S3 validation addition — *before* it
ever reaches the routing it was restored to expose. So the red is real but it points at the wrong
thing. A reader sees "validation rejects it", concludes the system is behaving, and stops.
**A test that is red for the wrong reason is worse than a missing test: it trains people to
explain away a colour.** It lands asserting the correct invariant behind a `t.Skip("DEF-15")`,
and the acceptance criterion for the fix becomes deleting the Skip line.

That same 400 is itself a finding I nearly filed as noise: it means #1319's canonical
`dm:`-key-in-`ThreadID` usage is *invalid on our branch*. That is a contract collision between
main and the integration branch, not a broken test.

**PR #1322 closes DEF-14 and makes DEF-15 harder to see.** I reviewed it for the two things that
would have made me object and both are right — the ownership check is upstream of the dual-write
rather than downstream of it, and `parseDMKeyIDs` denies on any key it cannot parse instead of
falling through. But #1322 filters *which* keys reach the broken thread-resolution branch; it does
not change what that branch does with them. Afterwards, the only keys arriving there are
well-formed and correctly owned. **The mis-shaping survives, now restricted to legitimate traffic,
which is exactly the population nobody audits.**

The generalisable shape, and it is the second time today a fix has had this property:
**narrowing the input to a broken function makes the breakage rarer, later, and better
disguised — and it reads in review as a fix.** §5u recorded #1319 doing this to DEF-14 (format
validation implicitly blessing an unauthorized path). #1322 now does it to DEF-15. Neither PR is
wrong. What is wrong is treating "the exploit no longer reproduces" as "the defect is closed."

## 5x. Merge review 16:05-16:15Z — I nearly rejected on a manufactured finding

**The user warned: be careful not to revert other work on main as agents rebase.** That warning
changed my gate. I told S6 that the test suite could not catch this class — resolving a conflict in
our favour drops main's code *and* main's tests for it in one move, so the suite goes green
precisely because the failing tests left with it — and required `git diff origin/main HEAD` before
any merge.

**Then I ran my own gate wrong.** I swept for files present on main but absent from the merge
result. It reported three dropped files (`scripts/single-node/{deploy,teardown,README}`) and a
468-line gutting of `hub-setup-cloudrun.md`. I had the rejection drafted. It was false: I compared
against **current** `origin/main`, which had advanced to `c5b2fadd` after S6 merged `6268bac4`.
Against the true merge parent (`2724ed10^2`), **zero** files are missing.

This is the §5s decay rule — a scope check against a live branch is a measurement, not a property,
and it expires — and I have now issued it twice today to S6 and S7. **I applied it to their work
and not to my verification of their work.** The near-miss cost nothing only because I checked
ancestry before sending; had I sent it, S6 would have spent an hour disproving something I invented.

> **Rule 18. A revert-detection sweep is run against the merge parent, never against the branch
> head that has moved since. The tool that finds reverts is itself the tool most likely to
> manufacture one.**

**What the gate did legitimately find** is DEF-15 (see §5c), and it came from the one resolution S6
flagged as a judgment call rather than from the bulk diff. Their justification — "DM routing no
longer uses ThreadID" — is disproved by `:237` and `:244` on their own branch, where ThreadID is not
dead but *prioritised*. Following that branch to `conversation.go:158` produced the actual defect: a
`dm:` key wrapped into a `thread:` external_ref and classified `kind = 'group'`, which routes around
the key-based authorization the section was built to install.

**Two general points, and the second is the one I want to keep.**

1. The merge was clean everywhere I predicted risk (ent schema union, the three `validDMKey` sites,
   no dropped files) and defective in the single place a human had exercised judgment. My ent
   warning was correct and unnecessary; the finding came from somewhere I had not thought to look.
2. **A deletion justified by a claim about code deserves the same scrutiny as an addition, and gets
   less** — because a diff shows a removed line, not the reasoning that removed it. S6 volunteered
   this one as a judgment call, which is why it was reviewable at all. The dangerous version is the
   one nobody flags.

**Not closing S6.** Merge accepted on the revert axis and left on the branch at `2724ed10`; the
section stays open pending the restored test and its reported output.

## 5w. Heartbeat 15:43Z — I specced from a premise I had never read

**Roster healthy, and the heartbeat's own step 3 is why this entry exists.** S6 blocked with
`dev-merge-main` executing 2 minutes prior — blocked-with-active-subagent, the normal shape. S7
blocked, and they had acted on the hygiene routing: `audit-def11`, `test-def11` and `review-def11`
are gone, `dev-def11` retained for the rebase. Fleet 39 → 32. Nothing stalled.

So the only real question was step 3: **is anything runnable while the sync is in flight?** DEF-6
and DEF-13 are both CLI-surface, no overlap with `pkg/hub`, `pkg/ent` or `pkg/store`. Unblocked and
unspecced. Specced them as §2.14.

**Then grepping the prior art demolished my own ledger row.** DEF-6 has said since it was filed that
"there is nowhere on a scheduled event to put a conversation" and that the fix is "real work."
Both false:

- `ScheduledEvent.Payload` is a free-form handler-specific JSON blob (`models.go:1835`). Adding a
  field to `MessageEventPayload` (`server.go:2761`) is additive, `omitempty`, no migration.
- `dispatch_agent` **already** resolves `evt.CreatedBy` at fire time and authorizes as that
  principal, failing closed on missing/cross-project/unscoped creator (`server.go:2855-2875`).

The second is the serious one. I had scoped DEF-6 as novel design. It is an *extension of a working
mechanism the message path declines to use*. Without rule 16 I would have specced a second
fire-time-authorization mechanism beside the first — **exactly §5o**, where my design duplicated the
shipped DM key. Same failure, same cause, and I had already written the rule that catches it.

**What is new, and worth carrying beyond this project:** §5o was a design duplicating shipped code.
This was a **ledger row** asserting a constraint I had never verified. The ledger is worse. A design
section gets reviewed by a manager who reads the code; a ledger row is compressed, authoritative in
tone, and consulted precisely when someone is deciding whether an item is cheap or expensive. It is
the format most likely to be inherited without re-checking, so:

> **Rule 17. A ledger row that characterises the cost or difficulty of unstarted work is a claim
> about code, and carries the same citation burden as a design section. "The fix is real work" is a
> finding, not a note.**

Both rows corrected in place rather than silently rewritten, with the wrong version visible — a
correction that hides what it corrected teaches nobody, including me.

**Security point that fell out of the grep,** now in §2.14.1 and AC-DEF6-3: a scheduled send is a
deferred act **by its creator**. If fire-time authorization uses the scheduler's identity, then
"schedule a message into a conversation I am not in" is DEF-14 with a delay and no interactive
caller to attribute it to. This is the third verb on one rule — D-1 governs joining, AC-INGRESS-1
governs writing, AC-DEF6-3 governs writing later.

**Deliberately not specced:** sender attribution on a fired message (§2.14.3). Preserving the
creator as sender is obviously right and touches `SystemCategoryScheduler` and every reader of
`SenderID == "SCHEDULER"`, which I have not enumerated. I am not respeccing a field whose readers I
have not counted — that is the same error as this entry, one level up. The section enumerates first
and reports before changing.

**Not dispatched.** Specced only. §2.14 branches off the post-sync head; dispatching now would base
a third branch on a head about to be replaced, which is the mistake I parked S7 to avoid.

## 5u. PR #1319 landed on main 15:08Z — the fix I asked for, one layer short

**§5p item 2 closed the loop.** I routed "the agent outbound path does not validate `ThreadID`" to
nc-arch. Their PR #1319 is that fix: `validDMKey` at all three ingress points
(`handlers_agent_messaging.go:120`, `:561`, `handlers_broker_inbound.go:97`), 400 before dispatch,
and the malformed `dm:<userID>+<agentID>` test vector I flagged is corrected. Routing to the owning
team worked.

**But format validation is not authorization, and it is shaped like it.** Reads are
membership-checked; writes are now format-checked. Verified chain, every link read on `origin/main`:

| Step | Location | What it does |
|---|---|---|
| ingress | `handlers_agent_messaging.go:120` | `validDMKey` — well-formedness only; caller need not be named by the key |
| persist | `:236` | `storeMsg.ThreadID = req.ThreadID`, `Channel = req.Channel` — both request-body controlled |
| read gate | `handlers_chat_v2.go:2848` | `validDMKey` → `isDMParticipant` → `filter.ConversationKey`. Correct. That branch sets **no project filter** |
| query | `webchannel_store.go:1173` | `SELECT ... FROM messages WHERE channel='web' AND thread_id=?` |

Ingress writes the column the read path filters on. Agent A posts `thread_id:
dm:agent:<B>:user:<V>`, `channel: web`; V sees it inside the B↔V DM, across projects.

**Bounded, and I said so when escalating:** no read access is gained, and `Sender` is
`"agent:"+agent.Slug` from the authenticated agent, so attribution stays honest. Injection, not
impersonation or exfiltration. #1319 strictly narrows it — arbitrary strings no longer pass.

**The generalisable point.** A validation that runs at the same place an authorization check would
run, on the same input, returning the same 400/403 shape, is read by the next reviewer as the
authorization check. #1319's own description says "malformed DM keys are rejected before any
dispatch or persistence" — true, and it invites the inference that *well-formed* keys have been
cleared. Nothing downstream re-checks. **Adding a partial check to an unguarded path can leave it
better defended and less likely to be defended further.**

This is the boundary rule from S6 (§5s) pointing the other way. There I said: name where a
security-critical path *begins*, not just where it ends. Here the path was correctly identified at
its beginning — ingress — and the check placed there answers a different question than the one the
path needs answered.

**Not my section.** Escalated to the user, full trace to nc-arch, explicitly including their right
to judge it not worth fixing. I also flagged my own unverified edge: I traced visibility through
`SearchChatMessages` and did *not* trace the primary DM message-list path. Rule 15 applies upward
and it applies to findings I am pleased with.

**Design consequence (mine to carry):** §2.4.2.2 mandates the key-derived participant guard on
`AddParticipant`. It does not state the write-side rule for message ingress. It must: *a message may
not be written with a `direct` conversation key that does not name the authenticated sender.* Same
rule as D-1, different verb. Adding as AC-INGRESS-1.

## 5v. Main diverged under the integration branch — 15:10Z

`git merge origin/main` into `scion/messaging-v2` conflicts in 13 files. Eight are generated
`pkg/ent/*` — **regenerate, never hand-merge**; a hand-resolved generated file is a silent
divergence from its schema. Real conflicts: `handlers_agent_messaging.go` (S6 and #1319 both edited
it), `attachments_agent_test.go` (both changed the DM key vector), `server.go`, `store.go`,
`entadapter/composite.go`.

Sequencing decision: push S6's merge first (clean onto `ebf8cc27`, build green), then have **S6**
do the main-sync — they own the colliding hunks and wrote them within the hour — then signal S7 to
rebase **once** onto the synced head. S7 has been parked at `4a7a3844` for 35 minutes; making them
rebase twice would be my scheduling error charged to them.

Per the §5s refinement, the conflict list above is a measurement with a shelf life. Whoever performs
the sync re-runs it; they do not work from this table.

## 5t. Heartbeat 14:43Z — the mutation that came back green

**Roster healthy.** S6 blocked with `dev-def8-hubtest` active; S7 blocked with work complete,
correctly parked awaiting a merge signal. Integration branch still `ebf8cc27` — nothing landed,
which is correct: the gate is mine and it is shut pending one test.

**The most valuable thing that happened today came back green.** I required three mutations from
S6 before merging DEF-8. Two bit. **Mutation 1 — restore `senderKind := "user"` — left the entire
suite passing.** The safety test checks *empty* kind at the function level; the mutation produces
`"user"`, a valid kind yielding a wrong-but-well-formed key. **The bug lives at the handler and
the test lives at the function, so the test cannot see it.** The security-critical fix of the
round was the only one of the three with no live coverage.

S6 self-reported this. "All three pass" would have been accepted without question and I would
never have known. **A green mutation concealed converts a known gap into an unknown one** — worse
than a red test, because a red test is information.

They then asked whether the function-level net was sufficient "given the handler code is
structurally correct and the if/else is visible in review." **That argument had already failed
today: that exact line was visible in review twice** — non-blocking from the reviewer, Low from
the auditor. Review is not the safety net for a defect that review passed. Also worth naming:
mutation 1 is not hypothetical, it **replays code that was on the branch ninety minutes earlier**,
so the test is a regression test for a real defect, not a speculative one.

**S7 approved at round 3.** Counter fix verified directly: `LogDivergence` branches
`if entry.Fallback { IncFallback() } else { Inc(entry.Match) }` — one event, one counter, gate
reachable. Their trajectory is the useful record: round 1 put tests where they could not observe
the fix; round 2 inferred a condition from an empty value; round 3 was correct except for a
deviation they had spotted and self-rated acceptable. **The defect analysis was right in their
first message and never changed. What moved across three rounds was the standard for done.**

**DEF-9 specced (§2.13), and I checked before escalating.** Grepped first per rule 16:
`AddAddressee` has **zero callers**; `DefaultAgentID` is written at three sites and read at none;
`delivery.go` holds a single formatter, so nothing routes by conversation. §2.4 already settles
the resolution order, so **DEF-9 needed no product decision from the user** — worth noting because
my instinct was to escalate it, and reading my own design first was the cheaper answer.

The one design addition is today's lesson relocated: **zero addressee rows currently means three
things** — resolution ran and correctly chose nobody, a bug skipped resolution, or a crash landed
between the message insert and the addressee insert. Same empty-value ambiguity as DEF-11, worse
consequence: not a miscounted metric but a message that silently woke nobody. Hence an
always-populated `addressee_resolution` field: `none` is a statement, an unset field is a bug.

**Blocked, precisely:** merge gate held on S6's handler-level test. DEF-9 is specced but
**genuinely** blocked — it touches `handlers_agent_messaging.go`, `conversation_store.go` and
`models.go`, all contested by both branches. Verified by file list, not assumed (cf. 5s).

## 5s. Heartbeat 14:14Z — verification has a shelf life; two defects caught in review

**Roster.** S6 six commits, S7 one, both managers live. S6 showed blocked with no sub-agents
(the condition the heartbeat flags) but had committed six minutes earlier, so mid-turn rather
than stalled. Integration branch still `ebf8cc27` — nothing merged, correct, merging is my gate.

**My own error, and it is a new category.** When I dispatched S7 at 13:45Z I told them
"you have no file overlap with S6, I verified this rather than assumed it." **True when said,
false by 14:08.** S6's later commits moved into `pkg/hub/handlers_agent_messaging.go`; their hunk
at 835-836 now sits inside S7's insertion zone at 833-838, in the same function, and changes the
`ResolveOrCreateDMConversation` signature S7 is adjacent to.

Rule 15 as widened in 5r says verify a premise that gates action. **That is necessary and not
sufficient: a verified fact about concurrently-moving branches decays.** 5r's lesson was
"check before you schedule"; this one is "a scope check against a live branch is a measurement,
not a property, and it expires." Recording as a refinement rather than a new rule — the
operational form is that any no-overlap finding is re-run at merge, never carried forward.
Sequenced S6 first, S7 rebases; told both, told them not to coordinate directly.

**DEF-8 defect — silent principal-kind default (HIGH).** `handlers_agent_messaging.go:835`:

```go
senderKind := "user"
if k, ok := messages.PrincipalKindFromAddress(structuredMsg.Sender); ok { senderKind = k }
```

A guess on the **input** to the key derivation, where I had only forbidden guessing on the
output. After step 1c the key is the ACL, so an agent sender whose address does not parse yields
`dm:user:X:agent:Y` — a different conversation, keyed to *a user with ID X*, locking the real
sender out of their own DM and naming someone else. Unexploitable only if user and agent UUID
spaces never collide, which nobody has asserted. Required: no kind, no key, no row — leave
`convResult` nil, which the surrounding code already treats as a divergence signal.

**Note the shape.** I gave S6 "parse failure denies, no fallback, no repair" for 1c and they
applied it faithfully *there*. The violation appeared one layer upstream on the same data. **A
rule stated about a function gets applied to that function; the property it protects lives on the
data path.** Worth stating rules against the data next time.

**AMENDED 14:20Z — my root cause above was wrong, and S6 supplied the right one.** I wrote that
the reviewers graded against a stale threat model. S6 checked what they had actually briefed and
reported that the auditor's threat model was substantially post-1c; the failure was in how the
security-critical region was *bounded*. They had described the kind default as a call-site
detail, and described the derivation path as **ending at `ParseDMKey`** rather than **starting at
`PrincipalKindFromAddress`**. A reviewer told where the sensitive region ends reviews it there.

**Rule, promoted out of that reply because it generalises:** when you designate something
security-critical, **state where it begins, not only what it is.** A path named by its endpoint
gets reviewed at its endpoint; every input reaching the sensitive function is inside the
boundary, and a briefing that omits this has drawn the boundary in the wrong place. S6's
compressed form, which does the work of a four-point list on someone scanning a diff:
**any guess on any input to the key derivation is a guess on the ACL.**

Worth recording *how* this correction arrived. I offered S6 an explanation that was flattering to
them and cost them nothing to accept. They checked what they had actually told the auditor and
returned a worse answer about themselves and a more accurate one about the system. Third time
today S6 improved on an instruction rather than executing it. **The failure mode I should watch
for in myself is the mirror image: my explanations of other agents' errors are also claims about
code, and they get graded by nobody unless the agent pushes back.**

**DEF-11 defect — the fix reproduces its own bug one layer up.** S7 gated the new
`conv-lookup-failed` fallback on `actualRef == "" && convID != ""`. That is a strictly larger set
than "pre-resolved and lookup failed": it also swallows thread conversations with empty refs
(where `thread-routing-mismatch` is a wanted signal) and **every unmigrated resolver row**, which
is most direct conversations until S6 step 3 lands. Net effect: a large slice of genuine
comparisons filed as fallbacks, board quieter, comparing less — **the alternative §2.12
explicitly rejected, reached by accident.**

The irony is the instructive part: **DEF-11 exists because code treated an empty string as
meaningful, and the fix infers a condition from an empty string one layer up.** Required an
explicit `lookupFailed` flag set where the failure happens. Also required routing the fallback
through `LogDivergence` rather than a hand-rolled `messageLog.Warn`, which would have given the
board two record shapes for one concept.

**Pattern across both:** neither manager misunderstood their defect. Both analyses were right
from the first message. Both defects are *inference from an absent value* instead of observation
of the condition — the same family as rules 13 and 14, which I have been applying to tests only.
**Rules 13/14 are not test rules. They are rules about evidence, and production code takes them
too.**

## 5r. Heartbeat 13:43Z — DEF-11 dispatched; a held item was held for nothing

**Roster healthy**, not the alarm condition: `ca-msg-em6` blocked with two live sub-agents
(`dev-def8-dualwrite` active, `dev-def8-convergence` completed 24m prior). Blocked-with-children
is the normal shape.

**Instance state, from integration2-operator (verified, not recalled).** scion-gteam is up on
`scion/messaging-v2` @ `ebf8cc27`, 1h6m uptime, no redeploys or drift, read switch off. Board:
`{matches:0, mismatches:0, fallbacks:0, total:0}`.

**That zero total is a finding, not a null result.** An hour of a 23-agent hub produced *nothing*
for the divergence comparison to see. It confirms from live data what I had inferred from code:
the instrumentation sits downstream of successful delivery and observes only hub-routed sends, so
**it can never serve as a pre-flight check — only an in-flight one.** A green board before
traffic means the board is asleep.

**DEF-11 specced (§2.12) and dispatched to S7 (`ca-msg-em7`).** The decision: populate
`ExternalRef` by loading the conversation. The rejected alternative matters more than the chosen
one — treating an empty ref as "not compared" is a one-line change that turns the board green by
silencing the comparison on the majority of traffic. **Rule 14 at system scale: a check that
reports success on empty input is worse than no check.**

**I held DEF-11 for a file conflict that does not exist.** The stated reason was collision with
S6 in `handlers_agent_messaging.go`. Checked at 13:45Z:
`git diff --stat messaging-v2..ca-msg-em6 -- pkg/hub/` is **empty** — S6 touches no file in
`pkg/hub` at all. I never verified it; I asserted it and scheduled around it.

**Consequence for rule 15, which I am widening.** Rule 15 was written about capability claims in
design prose. This was a *scheduling* premise, and it cost real serialisation on the item that
gates the read switch. **Rule 15 now covers any premise that gates action** — if a belief is the
reason something is not being done, it gets the same citation standard as a belief written into a
design. Sequencing decisions are claims about the code too.

Pattern worth naming across 5j, 5o and this entry: **all three were failures to act caused by an
unexamined reason to wait**, not by bad analysis of work in progress. My errors cluster on the
inputs to decisions, not the decisions.

## 5q. Invariant D-1, and the guard hole I caught in S6's version — 13:30-13:40Z

**nc-arch supplied the boundary condition of the key-as-authority pattern, unprompted.** It is
the thing I would have shipped without: key-as-authority works **only while the participant set
is static and fully named by the identifier.** Once membership is dynamic, key and ACL disagree
and the pattern flips from hazard-deleting to hazard.

> **INVARIANT D-1 — a direct conversation's participant set is immutable for its lifetime.
> "Add a person" is a promotion that creates a different conversation under a different
> authority.**

Written into design §2.4.2.2, with enforcement, and issued to S6 as binding.

**I got the *reason* wrong first, and nc-arch corrected it from the code.** I had argued
membership change → key change → different conversation → **broken continuity**. Wrong verb.
`PromoteDM` (`webchannel_store.go:1805`) is one transaction: insert the new topic, re-key the
history wholesale (`UPDATE messages SET thread_id=<topicID> WHERE thread_id=<dmKey>`), migrate
read state, delete the DM registry rows. **Identity and ACL change together, atomically, and
history moves to the new authority — continuity is *transferred*, not broken.** With
deterministic keys the corollary is that the pair's key is **reborn empty** if they DM again:
promotion drains a DM, it does not fork it. That is also the graceful-degradation shape — a
reply racing the promotion lands in the reborn-empty DM: visible, not lost.

**The hole in S6's enforcement, which their own proposed test would have passed.** S6 specced
"reject if active participant count >= 2 and the caller is not an existing participant", with an
exception permitting re-add after soft-remove. Compose the two: soft-remove B (active count
falls to **1**), then add C — `count >= 2` is false, so it is **accepted**, and membership now
says `{A, C}` while the key says `{A, B}`. Exactly the mutation D-1 forbids, reached by
remove-then-substitute. **And S6's test — add a third party to a 2-participant DM, assert
rejection — passes against the broken implementation.** The test did not discriminate between
the guard they meant and the guard they wrote.

**The fix is simpler than the bug:** for `kind='direct'`, `AddParticipant` accepts a principal
only if `ParseDMKey(external_ref)` names that exact `(kind, id)`. No count, no soft-remove
special case, no dependence on how many participants the creation path happens to write.
Initial creation is permitted because both parties are named — which makes visible that it was
always a *derivation from the key*, never a mutation. **This is the same lesson as rule 13, aimed
at a guard rather than a test: a count is a proxy for the invariant, and proxies drift.**

**S6 verified my second question properly rather than accepting my reading** — I asked whether
any live path calls `AddParticipant` on pre-step-3 empty-ref rows, flagging that I had been
wrong about exactly this class of claim twice that day. They enumerated all five call sites
(`resolve.go:470` via `resolveAgentDM`/`resolveEmailDM`, `backfill.go:314`, `conversation.go`
which does not call it at all, `handlers_chat_v2.go:3092` which writes `webchat_dm` not
`conversation_participants`, and none in `pkg/hub` against `ConversationStore`) and showed all
are post-upsert with kind-encoded keys. Rejecting empty refs is therefore safe.

**Three observations from nc-arch on `PromoteDM` to improve rather than mirror**, filed as
theirs-unvetted, for whoever builds group semantics: (1) the endpoint accepts an
`idempotencyKey` but the check at `:2230` ignores it in favour of a name-based heuristic;
(2) unique-violation detection is error-string matching (`:2268-2271`) where typed constraint
errors exist; (3) a TOCTOU window between the in-flight dispatch check and commit — judged
acceptable because reborn-empty makes the stray reply visible. **Worth mirroring:** the guard
ordering, especially the in-flight `CountPendingMessages` check that refuses to re-key under an
agent mid-reply.

## 5i. S5 — CLOSED 2026-08-27 12:40Z (accepted on round 3, `55dd6e16`)

Fast-forward from `19681bc1`; closeout at `ebf8cc27`. Round 3 was two test files, ~140 lines.
**All six mutations reproduced independently rather than taken on report:**

| Mutation | Result |
|---|---|
| MUT-A revert warning to `scion schedule message` | FAIL — *wanted message, got schedule* |
| MUT-B `emitDeprecationWarning` empty body | FAIL — *"0" is not greater than or equal to "6"* |
| MUT-D new flag naming backtick `scion agent poke` | FAIL on the **full** suite |
| MUT-E one `docFiles` entry renamed | FAIL — *doc file missing … update docFiles* |
| MUT-F `denyPatterns` emptied | FAIL — `catches_deny_listed_pattern` |
| positive control: `scion schedule message --in 5m` into real `glossary.md` | FAIL — *unknown subcommand* |

`go test ./cmd/ ./pkg/messaging/` green; tree clean.

**I was wrong about the floor and em5 was right.** I specified `>= 7` replacement references;
the count is **6** — four of the ten warnings (`--plain`, `--channel`, `--thread-id`, `--cc`)
name no `scion` command. em5 pushed back with the enumeration and I confirmed it from source.
A floor accepted on my authority rather than on a count would have been a number nobody had
verified — the same failure mode as I-1 itself. **Recorded because the correction, not the
compliance, is the behaviour to reinforce.**

**Residual limits — accepted, not deferred work:**
1. **MUT-G is not caught.** Deleting the main-body *call* to `findDenyListProblems` (or
   `findCommandProblems`, or `findReplacementProblems`) leaves every subtest green; I verified
   this by deleting the loop and appending a deny-listed line to a real doc — `ok`. Floors
   guard starved input, not a deleted invocation. Accepted: unlike a docs rename this requires
   deliberately removing a visible `t.Error` loop.
2. D6's original limit stands — the parse-check proves a documented command *parses*, never
   that it does what the prose says.
3. `findReplacementProblems` examines only the first `scion ` reference per line.

## 5h. S5 round 2 — rejected 2026-08-27 12:05Z (`e0269857`), CLOSED by round 3

Fast-forward from `19681bc1`. 14 files, +858/-44. `cmd` suite green. **I-1, I-2, I-3, I-4 all
verified fixed** — see below. Rejected on two new findings that are one defect.

**Verified fixed (by running code, not reading the diff):**
- **I-1** — `scion schedule create` really does register `--in` and `--at`
  (`cmd/schedule.go:783-784`); `--cc` no longer names a nonexistent flag. **MUT-A:** reverting
  the string to `scion schedule message` makes `TestDeprecationWarnings_ReplacementsExist`
  fail with *"replacement resolves to wrong command: wanted message, got schedule"*. The
  consumed-command assertion is load-bearing.
- **I-2** — **positive control:** appended `scion schedule message --in 5m` to the real
  `glossary.md`; `TestDocSyntax` failed with *unknown subcommand "message" for "schedule"*.
  Wiring is live against real docs today.
- **I-3** — `findCommandProblems` / `findDenyListProblems` are standalone; all three subtests
  call them. No reimplementation remains.
- **I-4** — three conditions plus the counter-example `matches: 0, mismatches: 0,
  fallbacks: 50000 is not clean`. Closes the `total = matches + mismatches` blind spot.

| # | Finding | Mutation evidence | Required fix |
|---|---|---|---|
| **J-1** | **`TestDeprecationWarnings_ReplacementsExist` passes while checking nothing.** The extractor keys on `strings.Index(line, "'scion ")` — single quotes only — and asserts nothing about how many replacements it examined. Zero extractions is indistinguishable from all-correct. AC-15a's purpose is to be the *standing* guard so a future deprecation cannot reintroduce I-1; today's ten warnings are also covered by hardcoded `Contains()` assertions in `TestDeprecatedFlag_*`, so the eleventh warning someone adds is covered by nothing. | **MUT-B:** gave `emitDeprecationWarning` an empty body — test PASSES. **MUT-D:** added a new deprecated-flag branch naming a nonexistent backtick-quoted command (``use `scion agent poke` instead``) — **the entire `cmd` suite goes green.** | Extract `findReplacementProblems(stderr string) []string`, called from the main body. Quote-agnostic extraction (scan for `scion ` anywhere, take words up to first flag/quote/comma). Assert a floor of >= 7 references found. Replace `catches_nonexistent_replacement` — it re-implements the check with a bare `rootCmd.Find` and survives deletion of the consumed-command assertion — with four synthetic-stderr cases through the extracted function. |
| **J-2** | **`TestDocSyntax` passes while checking nothing, same shape.** `os.IsNotExist -> t.Logf + continue`, and no assertion on total lines examined. A docs reorganisation silently disables the whole check. | **MUT-E:** renamed all four entries in `docFiles` to nonexistent paths — `TestDocSyntax` and all three subtests PASS, logging four "skipping missing file" lines. | `require.NoError` on the stat so a moved doc breaks the build. Accumulate a total across files and assert a floor. Real counts today: SKILL.md 3, messaging.md 3, cli.md 3, glossary.md 0 = **9**. Raise the floor, never lower it. |

**J-1 and J-2 are one defect: a test that proves the mechanism works on synthetic fixtures but
never asserts it ran on the real artefact.** Rule 13 held that a test must observe the effect
rather than the call. This is its dual — **a test must also assert it had something to
observe.** A check whose input can silently become empty is not a check; three green Rule 10
subtests over `t.TempDir()` fixtures say only that the function is correct, never that
anything was fed to it. **Rule 14, below.**

**This is my miss as much as em5's.** I specified D6 as a parse-check and reviewed round 1 for
whether the check was *correct*, not for whether it could be *starved*. I-3 taught "shared
implementation, two callers"; em5 applied that faithfully and the extraction is good work. The
starvation hole is a different axis and I did not name it.

## 5g. S5 rejection round 1 — CLOSED 2026-08-27 12:05Z (all four fixed)

Diff correctly scoped: docs plus one test file, fast-forward from `19681bc1`, no production
code. The four availability caveats are well documented — SKILL.md states them bluntly
("NOT available. Will produce a CLI error. Do not use."), which is right for an agent-facing
file. CLI-reference-as-canonical was the correct call. Parse-check covers **9 of 13** fenced
examples; the 4 skipped are placeholder forms.

| # | Finding | Evidence | Required fix |
|---|---|---|---|
| **I-1** | **Three deprecation warnings name replacements that do not exist** — `--cc → --to` (no such flag), and `--in`/`--at` → `scion schedule message` (schedule has no `message` subcommand; it has list/get/cancel/create/create-recurring/pause/resume/delete/history). **Live on the integration branch I already accepted.** em5 found the `--cc` case in code review and *documented around it* as "replacement pending" — which makes the docs honest and leaves the binary lying. | I enumerated every `emitDeprecationWarning` string and resolved each named replacement against `rootCmd` in a scratch clone: seven resolve, three do not | Name replacements that exist or state none. **Plus a permanent test asserting every replacement named in a warning resolves against `rootCmd`** — mutation-verified. Production change authorised inside a docs section. |
| **I-2** | **The parse-check has a `Find` blind spot — the same one that hid I-1.** `rootCmd.Find(["schedule","message"])` returns `cmd="schedule", rest=[message], err=<nil>`. Find returns the deepest match and leaves the remainder as args, so a doc containing `scion schedule message` passes today. | ran it | Assert the resolved command consumed the intended path: a leading non-flag token in `rest` matching no subcommand of a command that *has* subcommands must fail. |
| **I-3** | **Both Rule 10 subtests re-implement the check instead of invoking it.** `catches_bad_command` calls `rootCmd.Find` itself; `catches_deny_listed_pattern` loops over `denyPatterns` itself. Neither runs `TestDocSyntax`'s body. | **Mutation:** replaced the deny-list loop in the main body with `_ = denyPatterns` — `--- PASS: TestDocSyntax/catches_deny_listed_pattern`. **The subtest asserting the deny-list works passed with the deny-list deleted.** | Extract the checking logic into a function returning violations; call it from the main body **and** from the Rule 10 subtests with bad fixtures. One implementation, two callers. |
| **I-4** | **The divergence Recommendation teaches the exact misreading the fallback counter exists to prevent.** `messaging.md:223` — "Enable the read switch only after … seeing a **clean board** — zero mismatches over sustained traffic." That is satisfied by `matches: 0, mismatches: 0, total: 0, fallbacks: 50000`. `total = matches + mismatches`; fallbacks are excluded. Zero mismatches is what you see when the new model **never ran**, and the read switch fails open, so that is the likely state rather than a hypothetical. | read `admin_messaging_divergence.go:38–46` against the callout | Gate must be: sustained **non-zero matches**, zero mismatches, **and** fallbacks near zero relative to total. High fallbacks means investigate, not proceed. |

**I-1 is my failure, not em5's and not em4's.** When I verified S4's AC-15a compliance I checked
the conversation-reference replacements because those were what F-1 was about, and never
enumerated the rest. AC-15a says *every* replacement named in a warning. **I applied my own
acceptance criterion to the instance that prompted it rather than to its stated scope** — the
third time this project has been bitten by a requirement read narrowly (AC-8's "three inbound
paths", phase row 7, now this). The pattern is not managers reading carelessly; it is me
writing a criterion and then verifying the example instead of the criterion.

**I-3 is H-1 one section later, in the test written to satisfy the rule H-1 produced.** That is
not carelessness — it is a genuinely slippery failure, which is why the fix I required is
structural rather than attentional. A subtest holding its own copy of the logic can only ever
test itself. **Generalisation for S6: shared implementation, two callers — never a
reimplementation in the test that proves the check.**

**Also generalised: warning strings are documentation the binary emits at runtime.** D6's
parse-check covers `.md` files and was the right scope for a docs section, but it left the one
surface where the live defect actually was. The new warning-string test closes that.

## 5f. S4 — CLOSED 2026-08-27 10:35Z (accepted on round 4)

**Four rounds, five findings, one underlying defect.** F-1 (a warning routing users into the
very bug this project exists to remove), G-1 (an auth check trusting a caller-supplied
identity), G-2 (a path that ate messages and reported success) and H-1 (tests that asserted a
string they had just constructed) are all the same failure wearing different clothes: **a
mechanism that is present and correct, verified by watching it be invoked rather than by
observing what the user gets.** Only F-2 sits outside that pattern.

Every one of those cleared three APPROVE gates and a green suite. The gates were not
negligent — they checked what they were pointed at. That is the whole content of rule 13, and
it was earned here rather than reasoned out in advance. I did not catch F-1 by reading the
diff either; I caught it by asking what happens to a user who obeys the warning text.

**What em4 did better than asked.** G-1 was fixed by *deleting* the body sender fields rather
than validating them — removing the attack surface instead of guarding it, which is strictly
stronger and leaves nothing to mutate. And `TestSendMessageViaConversation_EmailRef_AgentContext`
was written unprompted: it asserts delivery through the outbound recorder in the working case
and zero sends on both recorders in the failing case. **That is the model test shape for the
rest of the project.**

### Rejection history (rounds 1–3)

### Round 3 (2026-08-27 10:25Z) at `765a4ac4` — behaviour accepted, one tests-only blocker

**Accepted, verified, not to be re-litigated:**

| Item | Evidence |
|---|---|
| **G-1 fixed** | `SenderPrincipalKind`/`SenderPrincipalID` **deleted** from `conversationResolveRequest`, `hubclient.ConversationResolveRequest`, and the dead CLI computation. Sender derives from `agentIdent.ID()`/`user.ID()` only. The attack surface is removed rather than guarded — a structural fix, stronger than any test. |
| **G-2 behaviour fixed** | Gate returns non-zero exit with a clear message; zero sends; tail replaced with a defence-in-depth error; warning names only `@<agent-name>`; endpoint still resolves all four grammars |
| **`@<email>` proven** | `EmailRef_AgentContext` asserts via the **outbound recorder** (recipient, text, sender agent); `EmailRef_NoAgentContext` asserts the error and zero on both recorders. Rule 13 done correctly — and the reason `@<email>` is rightly absent from the warning. **This is the model test shape for the rest of the project.** |
| **Suite green and stable** | mine, at `765a4ac4`: `pkg/hub` **0 failures on two consecutive full runs** (~7 min each), `cmd` + `pkg/messaging` green |

**H-1 (blocking, tests only).** `TestConvRef_ThreadRefGated` and `TestConvRef_ConvIDGated`
build the gate's error string themselves with `fmt.Errorf` and assert that a string they just
constructed contains a substring they just put in it; then they stand up a mock server, invoke
nothing against it, and assert zero sends. **Verified by mutation: I deleted the gate from
`message.go` entirely and both tests still PASSED.**

Blocking on the name, not the coverage. A green test called `ThreadRefGated` tells the next
reader the gate is covered. When someone removes that gate — and someone will, the moment DEF-5
gets a routing policy — the suite stays green and `conv:`/`#` resume silently eating messages.
**The gate is not left untested; it is left with a trap saying it is tested.** Worse than no
test, and the same failure that put G-2 in the build in the first place.

No obstacle existed: the gate returns before any hub connection, so
`messageCmd.RunE(messageCmd, []string{"conv:<uuid>", "payload"})` reaches it in six lines. I
confirmed this myself before raising it. Required: rewrite both to execute the command path,
assert the returned error, assert zero sends **after** invocation, and mutation-verify before
reporting. Tests only, single commit.

**Also noted, non-blocking.** em4's G-1 regression test is tautological too — it marshals the
struct and asserts the struct lacks fields it can see it lacks, then swallows a nil-store panic.
I am **not** asking for a fix: when a check is replaced by deleting what it checked, there is
nothing left to mutate. Recorded only so it is not counted as coverage later.

**Second non-blocking.** Removing the body fields also removed the `senderID == ""` guard. An
authenticated identity with an empty ID would now reach `Resolve` with an empty sender instead
of a validation error. Fails closed today — `requireParticipant` cannot match an empty
principal — so not a hole, but the guard was doing something and is gone.

### Rounds 1 and 2

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
| DEF-1 **(CLOSED — ledger row was stale, corrected 2026-08-27 13:15Z)** | **Participant-level auth on `conv:<id>`.** `resolveConvByID` checks the sender's *project* but not whether the sender is a *participant* in that conversation. Raised HIGH by S1 audit. | S1 | **S4** (surface layer, message-send time) | S1 is not wired into any live path, so the gap is not reachable. It becomes reachable the moment S4 switches reads. **S4 is not verifiable without this.** **Closed:** implemented in S4, reachable via `POST /api/v1/conversations/resolve`, no longer bypassable — but exercised in production only through that endpoint and the CLI `@<agent>` path, and **not yet load-bearing for the read switch**, which resolves from server-side inputs. §3 has said 'implemented' since S4 closed while this row still read 'open'. **The ledger drifted from the body of the same document — the exact failure this table exists to prevent.** Heartbeat step 3 now re-reads the ledger every cycle. |
| DEF-2 **(CLOSED — but conditionally, verified 2026-08-27 16:48Z)** | **AC-33** — deferred to the envelope validation layer per design. **Verified closed:** `ValidateCrossProjectAddressees` exists at `pkg/messaging/validate.go:104` and has one production caller, `handlers_agent_messaging.go:645`, over `mentionAddrs`. **The condition, and it is the reason this row stays visible instead of being struck out:** mentions are the *only* source of addressees today, because DEF-9 records that the addressee table is never written. So AC-33 covers 100% of addressees only for as long as DEF-9 stays open. **Closing DEF-9 reopens DEF-2's coverage question**, and whoever closes DEF-9 will have no reason to look at this row. `ValidateMessageAddressees` (`:140`), the wrapper that pairs the cross-project check with `ValidateAddressees`, has zero production callers — that is the seam a new addressee source will be wired through. **A closed defect whose closure depends on another defect staying open is not closed; it is load-bearing on a bug.** | S1 | **S3 — delivered.** Re-owned by whoever takes DEF-9. | The validation choke point does not exist until S3 builds it. |
| DEF-5 | **`conv:<id>` and `#<thread>` have no CLI delivery policy.** Resolving a reference to a conversation does not say *who receives the message*. For `@<agent>` the answer is obvious; for a conversation ID or a thread it is a policy question the design never answered — wake the default agent? fan out to every participant? fan out and wake none? S4 round 2 shipped a stub that resolved and then silently dropped the message (G-2), which is what an unanswered policy question looks like when a developer has to ship anyway. Round 3 takes option (b): the CLI hard-errors on both forms with a non-zero exit, and the warning text names only what works. **The resolve endpoint keeps handling all four grammars** — brokers and native chat need them, and resolution is not the broken part. | S4 | **me, before the section that wires conversation-reference sending for these two forms** | Nothing regresses: neither form works today, and erroring is strictly better than the silent drop it replaces. The risk is not technical but bookkeeping — an unanswered design question is easy to lose once the error message makes the gap look intentional. |
| DEF-6 **(SPECCED 2026-08-27 15:50Z — design §2.14; the premise below was WRONG, see correction)** | **Scheduled sends cannot address a conversation.** `scion schedule create` takes `--agent <name>`, not a conversation reference (`cmd/schedule.go:783-786`). Design §2.9 claimed the split "fixes by construction" the bug where scheduled messages drop `--channel`/`--thread-id`/`--attach`/`--cc` and are re-authored as `sender=scheduler` (`findings.md` §8). It does not, because there is nowhere on a scheduled event to put a conversation. The fix is real work: a conversation reference on the scheduled event, resolved at fire time rather than at create time (a conversation can be archived or drift between the two), and the original sender preserved. **CORRECTION 15:50Z — two of my claims here were wrong, and both were wrong in the ledger, where they would have been inherited unchecked.** (1) "There is nowhere on a scheduled event to put a conversation" is false: `ScheduledEvent.Payload` is a free-form handler-specific JSON string (`pkg/store/models.go:1835`) and `MessageEventPayload` is an ordinary struct (`pkg/hub/server.go:2761-2767`), so adding a field is additive with no migration. I asserted a storage constraint without reading the storage. (2) Larger: **the mechanism already exists.** `dispatch_agent` resolves `evt.CreatedBy` at fire time and authorizes as that principal, failing closed if the creator is gone, cross-project, or unscoped (`server.go:2855-2875`). The message path simply does not use it. Had I not grepped I would have specced a parallel mechanism next to a working one — the same failure as §5o, from the same cause. Specced as §2.14, paired with DEF-13 as a CLI section. Security consequence now explicit: a scheduled send is a deferred act by its **creator**, so fire-time authorization must be the creator's, not the scheduler's — otherwise it is DEF-14 with a delay and no interactive caller to attribute. | discovered 2026-08-27 while correcting §2.9; the underlying gap predates the project | **me** to spec, then a section to build. Blocks nothing before beta. | The `--in`/`--at` deprecation warnings now name `scion schedule create --in/--at`, which exists and works for the agent case — so the advice is true for the common path. It is incomplete rather than wrong. Do not close phase 13 (Removal) on the strength of the warning alone: AC's precondition is that every named replacement "has shipped and been exercised", and the conversation case has not. |
| DEF-11 | **The divergence board counts every CLI `@<agent>` send as a mismatch, and the mismatch is an instrumentation artifact.** `cmd/message.go:696` sets `msg.ConversationID` from the resolve endpoint. The Hub sees a supplied ID and skips re-resolution (correct — it should not do the work twice) but hand-builds `ConversationResult` leaving **`ExternalRef` empty** (`handlers_agent_messaging.go:828-832`). `ComputeDivergenceMatch` is then handed `actualExternalRef == ""`, matches neither the `dm:` nor `thread:` branch, and falls through to `routing-type-mismatch` (`divergence.go:176`). The two models agree; the comparator is fed a blank. **The documented read-switch gate requires zero mismatches, so the gate is now unreachable while the new CLI path is in use.** | verified 2026-08-27 while writing the QA walkthrough | **me** to spec; the fix is to read the conversation and populate `ExternalRef` rather than to suppress the counter | I-4 inverted: that finding was a clean board hiding a dead model; this is a dirty board hiding correct behaviour. Note the fix does **not** immediately produce agreement — once `ExternalRef` is real, a resolver-created row still has `external_ref = ''` (DEF-8), so the mismatch becomes *genuine* until DEF-8 lands. That is the right sequence: fix the instrument, then fix what it measures. |
| DEF-12 **(BLOCKED ON DEF-15 — do not dispatch, 2026-08-27 16:35Z)** | **The conversation backfill is fully built and wired to nothing.** **BLOCKER ADDED 16:35Z:** `backfill.go:195` constructs `thread:%s:%s` from `msg.ThreadID` with no `dm:` guard — it is the fifth and worst DEF-15 site, and the only *bulk* one. Wiring the backfill today converts a latent defect into a fully populated table: every historical DM-keyed message becomes a `kind='group'` project-scoped row. Its own comment reasons carefully about global DMs in the `else` branch, having never considered a DM arriving down the `if msg.ThreadID != ""` branch. **The hazard is mine — I marked this row runnable before DEF-15 existed, and "unblocked" in a ledger is exactly the kind of stale permission that gets acted on.** Gated behind §2.15 phase 4. `pkg/messaging/backfill.go` implements `NewBackfillService` (:83) and `Run` (:93) — batching, resume, dry-run, the lot. `git grep -n 'Backfill' -- '*.go'` on `ebf8cc27`, excluding the file itself and `_test.go`, returns **zero hits**: no CLI subcommand, no admin endpoint, no server-startup hook invokes it. On any deployed instance, **every message that predates this branch will have an empty `conversation_id` forever**. | verified 2026-08-27 while answering the integration-hub operator's deployment questions | **me** to spec the entry point (my instinct: `sciontool` subcommand with `--dry-run` and `--batch-size`, not a startup hook — an unattended migration over the whole message table on boot is how you turn a deploy into an outage), then a section to build it | Two consequences, and only the first is obvious. (1) The read switch, once on, sees historical messages as unrouted. (2) Less obvious and worse: the divergence board only samples *live* sends, so a backfill defect would not appear on it at all. The board cannot vouch for data it never sees. Deployment note: there is no backfill step to run, so nobody will notice it is missing by finding it failed. |
| DEF-7 **(ANSWERED 2026-08-27 13:03Z; build pending §2.6.3)** | **`#<thread>` can never resolve.** `resolveThread` matches `Conversation.DisplayName` (`pkg/messaging/resolve.go:429`). **Nothing in production writes `DisplayName`** — outside tests and generated ent code, that read is the field's only mention in `pkg/`. There is no endpoint to name or rename a conversation. `UpsertConversationByExternalRef` also does an unconditional `SetDisplayName` on the update branch (`conversation_store.go:400`), so a name set out of band would be wiped by the next upsert. | verified 2026-08-27 during the DEF-5 survey | **me** to spec: **neither of those.** `nc-arch` settled it: `#general` names a native chat thread and native chat already owns naming (`webchat_topic`, required unique-per-project name, create/rename endpoints in their approved design). **Build no naming path; invest nothing further in `Conversation.DisplayName`.** Written up as design §2.6.2. The remaining question — what `#<thread>` resolution actually reads — depends on the escalated unification decision, §2.6.3. **My original two options were both wrong because both assumed naming lived in my entity.** | The CLI gate rejects `#<thread>` already, so no user hits this. The *design* claims the form works. |
| DEF-8 | **Agent DMs exist as two disjoint rows.** Dual-write `ResolveOrCreateDMConversation` (`pkg/messaging/conversation.go:65-73`) writes `external_ref="dm:<sorted pair>"`, **`ProjectID` nil**, **zero participants** — it never calls `AddParticipant`. Resolver `createDirectConversation` (`pkg/messaging/resolve.go:497-532`) writes **`external_ref=""`**, **`ProjectID` = sender's project**, **two participants**. Lookup is asymmetric and cannot bridge them: `findDirectConversation` reads the participants table via `GetConversationsForPrincipal` (participant-based, `conversation_store.go`), so it can never see a dual-write row; `UpsertConversationByExternalRef` keys on `external_ref`, so it can never see a resolver row. Same principal pair → two conversation IDs, permanently. **This is what the read switch will diverge on.** | verified 2026-08-27 | **me** to spec reconciliation; then a section to build it. **Gates the beta** — escalated to the user 13:20Z. | Not a regression and not row-growth: each path is internally consistent and idempotent. The harm is that the two views of "the DM with @builder" disagree, which is exactly what the divergence board is for. |
| DEF-9 | **§2.4's addressee mechanism is unwired.** `AddAddressee` has no caller outside the store interface and the ent adapter — **the `message_addressees` table is never written in production**. `Conversation.DefaultAgentID` is written at three sites (`backfill.go:298`, `handlers_broker_inbound.go:217`, `handlers_agent_messaging.go:666`) and **read by no routing or delivery code**. `messaging.FormatNewDelivery` / `FormatLegacyAsNewDelivery` likewise have no production callers. `ResolveResult.Unresolved` is declared and never populated. | verified 2026-08-27 | **me** to spec, then a section. | §2.4 case 2 (default agent) and case 3 (posted, nobody woken) cannot occur; the `unresolved[]` contract in §2.4.1 and the distinct exit code it specifies have nothing behind them. |
| DEF-10 | **`@<agent>` DMs are project-scoped, contradicting Q2.** `resolveAgentDM` requires a non-empty `ProjectID` (`resolve.go:317-322`) and `createDirectConversation` sets `conv.ProjectID` whenever the context has one (`resolve.go:505-507`). Q2 and §2.4.1 settle that direct conversations are **global, `ProjectID` nil**. `@<email>` obeys this (it passes an empty project context, `resolve.go:378-382`); `@<agent>` does not. | verified 2026-08-27 | **me**; likely resolved together with DEF-8 | Consequence, not cosmetic: a project-scoped DM row is invisible to a global lookup, which is one of the two mechanisms producing DEF-8. |
| DEF-13 **(CLOSED 2026-08-27 17:25Z — merged at `edd4e4bd`)** | **The conversation-reference forms shipped undocumented.** `cmd/message.go:98-114` — the `Long` help text lists only `<agent-name>`, `agent:<name>`, `user:<name>`, `group[...]`, and all three examples are legacy form. No mention of `@<agent>`, `conv:<uuid>` or `#<thread>`, which are the headline feature of this project. The code is present and works (`sendMessageViaConversation` at `:655`, reference parsing at `:141`). Sharpest edge: the deprecation warnings at `:86-91` say "use `@<agent-name>` to message an agent directly" — **pointing at a form the help text never defines**, so a user who follows the advice must guess the syntax. The only written description is my QA walkthrough. | reported by the user 2026-08-27 15:16Z after rebuilding gteam binaries at `ebf8cc27` and finding nothing about conversations in `--help` | **me** to spec; fold into an existing section, do not dispatch alone | Cosmetic in the sense that nothing is broken, load-bearing in the sense that an undiscoverable feature is not shipped. **This is my spec gap, not a section's.** I wrote ACs requiring the deprecation warnings to fire and requiring the new reference forms to work; I wrote none requiring the help text to describe them. Both managers built exactly what I asked. AC to add: `Long` and the examples cover `@`, `conv:` and `#`, including the two that currently error by design, so the error is not a surprise. | **CLOSED:** S8 delivered on `scion/ca-msg-em8`, merged to `scion/messaging-v2` at `edd4e4bd`, full suite green, rule-18 sweep clean against both parents. `Long` now documents `@<agent>`, `@<email>`, `conv:<uuid>` and `#<thread>`, the latter two annotated as erroring by design. Three guards, each verified by a mutation I ran myself: a `go/ast` count of the `ReferenceKind` const block (catches a kind added without a table entry, in **either** direction — the iota pin it replaced caught only insertion, not the far commoner append); per-form assertions against `Long` (catch a documented form being deleted); and a deny-list scan over the whole cobra tree's `Long`/`Example` (catches a gated form reappearing as a runnable example). The three partition the space rather than overlapping. `countReferenceKinds` fails closed via `t.Fatal` when the const block is not found — a drift guard that cannot locate its subject must not return 0 and pass.
| DEF-14 | **Message ingress checks DM key format but not membership.** PR #1319 (native chat, merged to `main` at `6268bac` 2026-08-27) added `validDMKey` at `handlers_agent_messaging.go:120`/`:562` and `handlers_broker_inbound.go:98`, rejecting malformed `dm:`-prefixed thread_ids with 400 before dispatch or persistence — this closed the gap I logged as §5p item 2. It does **not** check that the authenticated caller is one of the two principals the key names. `storeMsg.ThreadID` and `.Channel` both come from the request body (`:236`) and go to `CreateMessage`; the read path (`handlers_chat_v2.go:1550` primary list, `:2848` search) gates on `isDMParticipant` and then filters by key **with no project filter**. So agent A in P1 can post `channel='web'`, `thread_id='dm:agent:<B>:user:<V>'` and the row appears inside B↔V's private DM for V, across projects. | found 2026-08-27 15:10Z reviewing #1319; **confirmed by nc-arch on the primary list path**, closing the caveat I raised about having traced only search | **native chat** owns the fix (nc-arch routed it to native-chat-lead and called it worth doing). Mine only as `AC-INGRESS-1`, so my step 1c does not inherit it | Bounded: no read access is gained, and `Sender` is the authenticated agent's honest slug — injection, not impersonation or exfiltration. nc-arch's refinement: attribution is honest but **placement is deceptive**, since V's UI renders the message inside the B conversation. #1319 strictly narrows the hole; the danger is that it *reads* as closing it, and nothing downstream re-checks. **Adding a partial check to an unguarded path can leave it better defended and less likely to be defended further.** **REPRODUCED 15:34Z and I ran it myself**, not on the developer's report: branch `scion/ca-msg-inject-repro` @ `07866490` (test file only, no production change), `TestDMKeyIngress_UnauthorizedAgentCanInjectIntoForeignDM` in `pkg/hub/dm_injection_security_test.go`, FAIL on main in 0.68s with distinct project IDs in the run log. It asserts the **correct** invariant (V must not see A's message), so it goes green on fix rather than needing inversion. Handed to nc-arch and native-chat-lead; agent retired, absence confirmed by name. **One control it does not run, flagged on handover:** both test messages carry the same DM key, so an unfiltered history read would produce an identical failure with a much worse diagnosis — the test proves the message is visible, not that the *key* is what makes it visible. I am confident reads are key-filtered (`handlers_chat_v2.go:1550`, traced by me and independently by nc-arch), but the test is not self-contained on the point. **A reproduction that cannot distinguish its own defect from a worse one is still evidence, provided you say which.** |
| DEF-15 | **A `dm:`-prefixed `ThreadID` creates a third DM shape, inside the section that existed to eliminate the second.** On the merged branch, `handlers_agent_messaging.go:244` branches `if req.ThreadID != "" { ResolveOrCreateThreadConversation(...) } else if ... { ResolveOrCreateDMConversation(...) }` — **ThreadID is prioritised**, and the pair-based DM path runs only when it is empty. `ResolveOrCreateThreadConversation` (`pkg/messaging/conversation.go:158-161`) applies **no `dm:` prefix check**: it builds `external_ref = "thread:<projectID>:<threadID>"` with `Kind = "group"`. So an outbound message carrying `dm:agent:X:user:Y` produces `external_ref = 'thread:<proj>:dm:agent:X:user:Y'`, `kind = 'group'`, project-scoped. **Because `kind` is `group` it takes the participant-table path and never reaches the key-based authorization S6 just built** (§2.4.2.1). | found 2026-08-27 16:10Z reviewing S6's merge resolution | **me** to spec — the choice is whether a `dm:`-prefixed ThreadID routes to DM resolution or is refused at ingress, and it interacts with DEF-14 | **EXPOSURE CORRECTED 16:35Z — DEF-15 is branch-local, not live.** I wrote this row as if the defect were reachable in production. It is not. `pkg/messaging/conversation.go` and `pkg/messaging/backfill.go` **do not exist on `origin/main`**, and `git grep 'fmt.Sprintf("thread:' origin/main -- pkg/ cmd/` returns **zero hits** (verified by me at `98a9d9c2`, after nc-arch raised it). Every DEF-15 site is ours. It is reachable on `scion/messaging-v2` via `POST /agents/{id}/outbound-message` with a `thread_id`, which #1319 format-validates and therefore implicitly blesses — but that path only mints the malformed row on our branch. **This is a pre-beta defect in unreleased code, not a production security issue, and I had been writing it as the latter.** Caught by another architect running the grep I should have run on my own ledger row (rule 17 turned inward). The severity is unchanged for beta and the fix is unchanged; what changes is that nothing is burning right now. **How it surfaced is the part worth keeping:** S6 deleted the DM `ThreadID` line from `attachments_agent_test.go` — the line #1319 had just corrected to canonical format — on the stated grounds that "DM routing no longer uses ThreadID". A grep disproves that at `:237` and `:244`. Most likely the canonical key started producing this malformed row, the test looked wrong, and removing the line made the symptom vanish without the cause being found. **The evidence that would have exposed a defect was removed, and the justification was a claim about code that did not survive a grep.** Instructed S6 to restore the line and report what it produces, and explicitly *not* to fix the routing in a merge resolution. **CONFIRMED 16:20Z, by two independent observations.** S6 restored the line, ran it, and observed the row directly: `kind=group surface=native external_ref=thread:<proj>:dm:agent:<X>:user:<Y> project_id=<proj>`. I confirmed the code path separately by grep at `2724ed10` rather than taking their report. **DEF-15 is two sites, not one:** `:848` calls the same `ResolveOrCreateThreadConversation` with `structuredMsg.ThreadID` and has no `dm:` guard either. **PR #1322 does not fix it, and reduces who can see it.** #1322 adds the DM-key ownership check at `:174`, *upstream* of the dual-write at `:245`, so after it lands the only keys reaching the thread branch are well-formed **and correctly owned** — the mis-shaping then happens exclusively to legitimate traffic, where nobody is looking for it. A fix that filters the input to a broken function makes the breakage rarer and better disguised. **The restored test is red for the wrong reason and that is why it must not land red:** it dies at 400 `thread_id requires channel to be set` — our own S3 `ValidateLegacyMessage` addition — *before* it exercises routing, so a reader of that failure learns "validation rejects it" and stops. It also means #1319's canonical `dm:`-in-`ThreadID` usage is simply invalid on our branch, which is a contract collision with main, not a test bug. Instructed S6 to land it asserting the **correct** invariant behind `t.Skip("DEF-15")`; the acceptance criterion for the fix is deleting the Skip line. |
| DEF-16 | **The conversation dual-write happens before validation, so a rejected request still leaves a row behind.** `handlers_agent_messaging.go` @ `2724ed10`: `handleAgentOutboundMessage` dual-writes at `:245`/`:247` and validates at `:288`; `handleAgentMessage` validates at `:615` and dual-writes at `:848`/`:851`. **The two ingress handlers perform the same two operations in opposite orders** (rule 19). Observed, not inferred: S6 restored a DM `ThreadID` to `attachments_agent_test.go`, the request was rejected 400, and the `kind=group` conversation row persisted with no message attached to it. The row survives; the message does not. | found 2026-08-27 16:17Z by S6 running the restored DEF-15 test | **me** to spec. The fix is an ordering change, but *which* order is correct is a real design question, not a cleanup: validate-then-write gives up the ability to record a conversation for a message that fails a soft check; write-then-validate manufactures orphans. Answer it before moving either line. | Orphans are harmless today — no messages, no participants, invisible to every read path. They stop being harmless the moment anything treats a conversation row as evidence that a conversation happened: `external_ref` uniqueness, the DEF-12 backfill, or a participant listing. **Note what this does to DEF-14's blast radius before #1322: an unauthorized key was refused, and still left a row.** The refusal was real and the side effect outlived it. |
| DEF-18 | **AC-33's cross-project refusal cannot name what it refused.** `pkg/messaging/validate.go:110` declares `var projectAgents []string // for error reporting`, appends to it at three sites (`:122`, `:127`, `:132`) — and **never reads it**. The error returned on violation is a constant string: "message addresses agents in multiple projects; a single message may only target agents within one project". Flagged by `golangci-lint` as `ineffassign`, which is how I found it; the lint report is the symptom, the comment is the evidence. Someone intended the error to list the offending agents and never wired it up, and the comment has been asserting otherwise ever since. | found 2026-08-27 17:16Z running the CI lint gate for the first time (DEF-17 sweep) | **me** to fold into the DEF-17 sweep — one-line fix, name the agents and their project IDs in the error | Not an authorization hole: the check itself is correct and does refuse. It is a **diagnosability** defect in a security control. When AC-33 fires in production the operator learns that *some* addressee crossed a boundary, with no way to tell which, in a message that may address many. A refusal nobody can diagnose gets worked around rather than investigated. Note also `fmt.Errorf` with no format verbs on a concatenated constant — should be `errors.New`. |
| DEF-19 **(RELEASE BLOCKER)** | **Phase 7 validation breaks `group[]` messaging — a shipped, documented CLI feature.** `messaging.ValidateLegacyMessage` runs at `handlers_agent_messaging.go:630`; the `group[]` fan-out dispatch is at `:669`. Validation wins, so every `group[]` message through the outbound path returns 400. **Proved by direct probe, not by reading**: `group[agent:reviewer,user:alice]` -> `addressee[0]: principal_kind must be user or agent, got "group[agent"`; `group[reviewer,deploy-bot]` -> `got "system"`; plain `agent:reviewer` -> nil. The addressee parser splits the recipient on `:` and reads `group[agent` as the principal kind. | found 2026-08-27 18:36Z, chasing an aside in em6's §2.15 report | **unassigned — must be its own change, not a merge resolution.** Fix belongs to whoever owns Phase 7 / `MapLegacyEnvelope` | **This is a regression we introduced, not a pre-existing bug.** `ValidateLegacyMessage` is **absent from `origin/main`** (verified at `b09e7f49`); on main the handler reaches `handleGroupMessage` at `:585` with no validation gate. `group[]` is documented at `cmd/message.go:64` with a worked example at `:71` and dedicated flag-conflict errors at `:82`-`:257`. **Why a green suite missed it: no test exercises `group[]` through the HTTP handler on this branch.** The only `pkg/hub` test naming `group[]` is `TestHandleGroupMessage_ThreadID_NotPropagated`, written today, which calls `handleGroupMessage` **directly** — bypassing the exact validation that breaks it. em6 hit the rejection while writing it, and recorded it as *'a separate validation concern (group[] format not handled by MapLegacyEnvelope/ValidateAddressees) — unrelated to §2.15'*. Correct about ownership, and it is a release blocker walked past. **A test that must bypass a gate to reach its subject has just discovered something about the gate.** Vindicates the incremental-landing decision within minutes of it being made: this defect is live, real, and invisible in an all-green 81-commit branch. |
| DEF-17 **(EXPANDED 2026-08-27 17:17Z — three gates, not one)** | **The integration branch fails three CI gates that `origin/main` passes.** I found the formatter first and assumed it was the whole finding; it was the first one I happened to run. Reading `.github/workflows/ci.yml` afterwards — which I had never opened — turned up seven gates. Results on `scion/messaging-v2`: **(1) Format Check — FAIL**, 18 unformatted files vs 0 on main, `exit 1` hard failure. **(2) `make compat-literals` — FAIL**, 11 legacy `grove-*` project-ID literals in `cmd/broadcast_test.go` and `cmd/message_deprecation_test.go`; **both files are absent from `origin/main` entirely**, so all 11 are ours, and main passes this gate clean. **(3) `golangci-lint run --new-from-merge-base=origin/main` — FAIL**, 7 issues (2 `ineffassign`, 5 `staticcheck`); note the CI invocation restricts the linter to code new since the merge base, so **every one of these is by construction ours**. Passing: `make lint` (vet), `./hack/check-authz-guards.sh` (reports "analysed e2b5c37d, no violations" — reassuring given DEF-14/15), `make build`, and the test suite. Not applicable: shellcheck and the four web jobs — the diff vs main touches no `.sh` and nothing under `web/`. | formatter found 17:08Z reviewing DEF-13; the other two found 17:14-17:16Z by then reading the CI config | **me.** One sweep on the integration branch after ca-msg-em6 lands, so it does not conflict with in-flight work. `pkg/store/models.go` is ent-generated — check whether it regenerates rather than hand-formatting it. Fold DEF-18 into the same commit. | Zero behavioural risk, zero pre-beta urgency; all three are whitespace, test-fixture strings, and dead assignments. The reason it is a ledger row: **the branch's entire purpose is to merge**, and these fail the merge, hard. **The lesson is not "I forgot gofmt" — it is that I never read the definition of the gate.** I inferred what CI checked from the tool I happened to know, was right about one gate out of seven, and would have called the branch ready on that basis. `ci.yml` is 160 lines and I had been running this project for three days. **Checking a gate you have not read is checking your memory of it.** |
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
| D6 | **Rule 13 applied to documentation: parse-check documented commands against the real cobra tree, plus a deny-list for negative claims.** Extract every fenced `scion …` line from the docs a section touches and run `rootCmd.Find(args)` then `cmd.ParseFlags(rest)` — no mock, no execution, no new infrastructure. Additionally the check carries an explicit deny-list: any doc line presenting `scion message conv:<…>` or `scion message #<…>` as a working example **fails**. **Known and accepted limit:** this does not catch a command that parses and runs but does something other than what the prose says. | em5 offered (a) accept the gap or (b) a harness running doc examples against a mock server. (b) is the trap — a harness proving *a mock* accepts an example observes the call rather than the effect, which is rule 13's own failure mode, so it would commission the defect S4 spent four rounds rejecting. (a) is where F-1 goes to live: S4 opened with a warning naming three syntaxes the binary could not parse, and a docs site has more readers than a warning string. (c) validates against **the binary as built**, which is exactly the F-1 class. The deny-list exists because parse-checking cannot verify a negative and three of the four availability caveats are negatives — `conv:` and `#` *parse* fine and are rejected later by the CLI gate, so a parse check alone would wave them through. Narrow check with a documented edge beats a broad one that quietly proves nothing. | 2026-08-27 |
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

# Tranche C — Phase Plan

**Author:** ca-msg-arch **Date:** 2026-08-29
**Base:** `upstream/main` @ `8b09c118f` (post #1371 messaging-authorization merge)
**Sources:** `origin/scion/messaging-v2`, `origin/scion/ca-msg-em9-unify`
**Sponsor ruling in force:** OQ1 = **Option C** (widen the conversation-upsert guard narrowly).

---

## 0. Ground truth after the upstream sync — read before touching anything

Main moved `a7ac9c489` → `8b09c118f`, seven commits. Three matter to us:

| Commit | Effect on tranche C |
|---|---|
| `71ad85281` (#1367) | **Our design docs landed.** `.design/messaging-conversation-model{,-findings}.md` are now on main. Do not re-add them. |
| `36225aaae` (#1366) | **Our gate rows landed** in `hack/check-security-marker-gates.sh`. |
| `8b09c118f` (#1371) | The messaging-authorization work (D1–D10). 34 files, +2939/−76. New choke point `pkg/hub/authorize_message.go`; `message_mode` on the agent record. |

### Verified consequences (measured, not assumed)

1. **`pkg/messaging/` was not touched by #1371.** `resolve.go` is **byte-identical** to the
   pre-pause copy. `checkPostResolutionAuth`'s `case "group"` still returns nil without consulting
   the participant table. **Option C's safety argument is unchanged and still valid.**
2. **`hack/check-conversation-upsert-guard.sh` is unchanged.** The §6.2 spec applies verbatim.
3. **Prohibition-list identifiers all survive** with occurrence counts up, not down.
4. **Direct conflict set with #1371 is 7 paths, of which 3 are ent-generated.** The real
   hand-merge set is **four files**: `handlers_agent_messaging.go`, `handlers_broker_inbound.go`,
   `handlers_chat_v2.go`, `pkg/store/models.go`.
5. **Zero M-ADD collisions** — no file is created by both #1371 and our sources.

### Re-derived manifest — CORRECTED 10:12Z

**An earlier revision of this document (M-ADD ~79 / M-MOD 80) was wrong. Do not use those numbers.**
They were produced with `git diff --diff-filter=A upstream/main...BRANCH`. Under three-dot, status `A`
means *"added on the branch side since the merge-base"* — **not** *"absent from main."* Main
independently added many of the same files when tranches A and B landed, so 51 paths that exist on
main today were misfiled as additions. `pkg/messaging/resolve.go` was among them, which is absurd on
its face: §0.1 of this document verifies that file is byte-identical to main.

Caught by ca-msg-em6, who measured 13 `pkg/messaging` M-ADD files against my 30 and said so.

**The correct test for M-ADD is existence on main** — `git cat-file -e upstream/main:<path>` — not a
diff status. Corrected figures:

| Set | Count | Notes |
|---|---|---|
| All changed paths (union of both sources, three-dot) | 225 | |
| **M-ADD — absent from main** | **80 raw → 33 in scope** | 13 `pkg/messaging`, 12 `cmd`, 8 `pkg/hub`. 47 dropped are `.design/project-log/` + 2 working notes. |
| **M-MOD — exist on main** | **145 raw → 107 non-ent → ~82 in scope** | 38 ent-generated (regenerate, do not merge); 5 project-log dropped; ~20 are `newTestStore`-only and dropped under Ruling A-1. |
| No-op rows (identical to main on both sources) | **0** | Every M-MOD row carries real content. |

**The shape of the work is the opposite of what the earlier numbers implied: far more modification,
far less addition.** M-MOD is ported by hunk and is the harder mode. Budget accordingly.

### SECOND CORRECTION, 10:16Z — use the ENDPOINT diff, not three-dot, for sizing

Three-dot was also the wrong tool for *sizing* this work, and it failed in the most dangerous
possible direction. `git diff main...branch` compares the **merge-base** to the branch. Our sources
branch from `6268bac4`, before tranche A. So three-dot reports what the branch did since the stone
age — including re-adding files main has since acquired — and it reported **`-0` deletions on
essentially every row.** That is what made the port keep looking safely additive.

`pkg/messaging/resolve_test.go`: three-dot says **+1413/−0**. The endpoint diff says **+19/−24**.

**For a construction job off a stale branch, the right measure is the endpoint tree diff** —
`git diff upstream/main origin/<branch> -- <path>` — which answers "how do these two trees differ
right now." Corrected totals over the 107 non-ent candidates:

| Measure | Three-dot (wrong) | Endpoint (correct) |
|---|---|---|
| Rows with a real delta | 107 | **96** (11 are already identical to main) |
| Added lines | 3,509 | **5,287** |
| **Deleted lines** | **~0** | **4,316** |

**Those 4,316 lines are the silent-revert surface, and the worst rows are prohibition-list items:**

| File | Endpoint | Why it matters |
|---|---|---|
| `pkg/hub/handlers_agent_messaging_test.go` | +344 **−761** | the B5 test functions |
| `pkg/store/entadapter/conversation_store_test.go` | +20 **−657** | |
| `pkg/hub/handlers_agent_messaging.go` | +274 **−274** | the B5 client-supplied-sender fix lives here |
| `pkg/messages/dm_key_test.go` | +21 **−154** | DM-key canonicality rejection |
| `pkg/hub/route_metadata.go` | +18 **−153** | prohibition list |
| `pkg/hub/chat_notifications_test.go` | +1 **−152** | 3 B5 functions |
| `hack/check-conversation-upsert-guard.sh` | +27 **−147** | **the guard itself** |
| `pkg/store/models.go` | +31 **−122** | also a #1371 collision file |
| `pkg/store/store.go` | +5 **−97** | |

**`hack/check-conversation-upsert-guard.sh` at −147 is the sharpest trap in the tranche.** The source
branches predate PR#1339 entirely, so their copy is the pre-guard file. Anyone who ports that path
from a source branch deletes the guard while appearing to "port tranche C." **C1 must be built from
main's copy and from nothing else.**

**Standing instruction: every phase reports its endpoint deletion count per file before pushing, and
justifies each one line by line.** A deletion you cannot name is a revert you have not noticed.

`pkg/hub` alone holds **47 M-MOD rows / 1,996 added lines**, of which 37 are test files and ~20 are
`newTestStore`-only. The substantive hub set is roughly 27 files, split between C4 (the two
`webchannel_store` files) and C5 (the rest). **C5 is therefore larger than a first read of §2
suggests** — its five headline files are the bulk of the lines, but it carries a long tail of small
test touches. Two of those small rows are prohibition-list items and must not be lost:
`chat_notifications_test.go` (3 B5 functions) and `route_metadata.go`.

The seven-phase split is unaffected: phase boundaries follow layers and the dependency chain, not
file counts. Only the size estimates moved.

---

## 1. Standing sync brief — applies to every phase, no exceptions

1. **Rebase on `upstream/main` before you start and before you push.** Never merge main into your
   branch; never push `main`. Be careful not to revert other people's work (standing sponsor order).
2. **Use the ENDPOINT diff for construction work: `git diff main branch -- <path>`** (rule 324).
   Three-dot (`main...branch`) answers "what does this PR contain" and is correct only when the
   branch is *rebased on current main*. Our source branches are not — their merge base is
   `6268bac4`, which predates tranche A. On a stale base, three-dot compares merge-base→branch and
   therefore reports **`-0` deletions on nearly every row**, which is how this port read as "safely
   additive" across two reports and a design doc while actually deleting 4,316 lines. A
   near-universal `-0` deletion column is a **measurement smell**, not a safety property (rule 325):
   real ports delete something. Two-dot (`main..branch`) answers a question nobody asked.
   Likewise, **`--diff-filter=A` under three-dot means "added since merge-base", NOT "absent from
   main"** — the only correct absence test is `git cat-file -e upstream/main:<path>` (rule 322).
3. **Port M-MOD by hunk from main's current copy. Never file-copy.** (Ruling A-2.) Our source
   branches predate #1371; copying a file wholesale silently reverts the auth work. A branch that
   predates a fix does not delete it — it simply lacks it, and only the diff against main shows the
   loss (rule 296).
4. **The additive invariant, measured at the endpoint.** On any file that already exists on main,
   prefer ADD-only. Verify with the endpoint diff, never three-dot:
   `git diff --numstat upstream/main HEAD -- <existing files> | awk '$2 > 0'` — investigate every
   hit. Deletions are allowed only where you can name the line and justify it. **Every phase reports
   per-file endpoint deletion counts before pushing, with line-by-line justification for each
   deletion. A deletion you cannot name is a revert you have not noticed.**
5. **THE PROHIBITION LIST must survive.** Re-check it against main *as it is now*; #1371 may have
   moved or renamed members. A renamed prohibited item still must not be lost.
6. **Do not carry `scion/messaging-v2`'s `fanOutToProject`/`fanOutGlobal` hunks** — they predate B5
   and restore slug-based self-skip. Skip the sender by ID, never by the display `Sender` label.
7. **Drop the `newTestStore` refactor** (Ruling A-1) — a large mechanical delta must not ride inside
   a semantic port (rule 299).
8. **Run the three guards from main before you push**, from inside the repo (each begins
   `cd "$(dirname "$0")/.."`): `check-security-marker-gates.sh`, `check-conversation-upsert-guard.sh`,
   `check-authz-guards.sh`.
9. **`make test-fast` is `go test -tags no_sqlite ./...`.** Anything behind `//go:build !no_sqlite`
   is never run by CI — if your phase's coverage lives there, say so explicitly in your report.
10. **Tests land with the code they test.** There is no test-only phase.

---

## 2. The phases

Seven. The split is driven by three things: layer boundaries, the dependency chain, and isolating
the four files that collide with #1371 so a conflict there cannot block the other six phases.

| # | Phase | Scope | Depends on |
|---|---|---|---|
| **C1** | Guard widening (Option C) | `hack/check-conversation-upsert-guard.sh` + negative test | — |
| **C2** | Schema, ent, store interface | `pkg/ent/schema/*`, generated ent, `pkg/store/store.go`, `models.go`, `entadapter/*` | — |
| **C3** | Messaging library | ~30 new files under `pkg/messaging/` | C2 (types only) |
| **C4** | Webchat dual-write | `pkg/hub/webchannel_store.go` (+314), `webchannel_store_postgres.go` (+256) — the eight INSERT sites | **C1**, C2 |
| **C5** | Hub handler wiring | `handlers_agent_messaging.go` (+281), `handlers_broker_inbound.go` (+91), `handlers_chat_v2.go` (+56), `messagebroker.go` (+83), `handlers_messages.go` (+41) + their tests | C3 |
| **C6** | CLI semantic contract | `cmd/message.go` (+207) + ~12 new `cmd/` files: conversation references, `--channel`/`--thread-id` deprecation | C3 |
| **C7** | Broker integrations + docs | `extras/scion-{teams,telegram,slack,discord,chat-app}`, `docs-site/`, `resources/platform_skills/scion-messaging/SKILL.md` | C5 |

**C5 is the risk concentrate.** All four #1371-collision files are in it. It gets the most
experienced manager and the tightest review.

**C6 is the original brief.** The whole project started from "two interfaces, optional flags that
can be accidentally omitted." C6 is where the coherent semantic contract actually reaches users.
It is not a mopping-up phase.

### Per-phase acceptance criteria

**C1** — Exempt raw `INSERT INTO conversations` in `pkg/hub/webchannel_store.go` and
`webchannel_store_postgres.go` **only when the statement mints `kind='group'`**. Surfaces 1, 2a, 2b
and 3 (`UpsertConversationByExternalRef`, `CreateConversation`, `AddParticipant`, the ent builder)
stay **fully barred** in `pkg/hub`.
*Non-obvious:* the `kind` literal is on the line **after** the `INSERT`. House style puts the column
list on the INSERT line and the values on the next. A single-line test cannot express Option C — read
the INSERT line plus a following window. Both dialects: sqlite `?`, postgres `$1..$5`/`NOW()`.
*The negative test is the whole point:* adding a `kind='direct'` raw INSERT to an exempted file
**must still exit 1**; an `AddParticipant` call added there **must still exit 1**; the eight existing
`'group'` sites must exit 0. Without those three assertions C buys nothing over a blanket allowlist.
Amend the script header to record the exemption, its rationale (atomic topic+conversation dual-write
inside an explicit `tx`; store methods take `ctx` not `tx`), and its limitation (**defeated by a
non-literal kind** — this falls inside the header's existing LIMITATIONS class; say so, do not imply
it is covered).

**C2** — Conversation, participant and addressee entities exist with migrations; `make test-fast`
green; no change to any authorization decision.

**C3** — `pkg/messaging` compiles and is unit-tested against main; DM-key derivation golden vectors
present and passing; parse failure denies everywhere on the derivation path, with no fallback.

**C4** — The eight sites land; C1's guard exits 0 on the result; the topic+conversation dual-write is
atomic. **INVARIANT U-TX-1:** nothing inside the transaction may touch the ambient pool — at
`MaxOpenConns=1` the failure mode is a hang, so every test here needs a timeout.
**DEF-36 is in scope for C4:** these mints do not populate the participant listing index. Either
populate it or record explicitly why not.

**C5** — All four collision files ported by hunk onto #1371's current shape. The 11 `Test` functions
in `handlers_agent_messaging_test.go` on main all still present and passing. `authorize_message.go`'s
choke point is **called, not bypassed** — our new paths must go through it.

**C6** — `conv:<id>`, `@agent`, `@email`, `#thread` parse and resolve; cross-project references are
rejected at send time by a permission check; `--channel`/`--thread-id` deprecated with a warning, not
removed; a reference omitted no longer silently sends to the wrong place.

**C7** — Each broker builds and its tests pass; docs describe the shipped contract, not the design.

---

## 3. Open items carried in

- **DEF-36** — participant listing index not populated by topic mints. Owned by C4.
- **DEF-32** — federated identity must not be resolved by email; needs an explicit
  `(issuer, subject) → user_id` link table. Required before S4, **not on the C path**.
- **§2.6.3** of the primary design doc is still **OPEN** (`Conversation` vs `webchat_topic` as
  parallel constructs) and landed that way deliberately.
- **Tranche H** remains blocked on the verified `omitempty` evasion in G-1's regression test.
- **Option B** (tx-carrying store method; move the eight sites into `pkg/store`) is the destination.
  C1 is the safe intermediate, not the end state. File it, do not do it now.

---

## 4. Reviewer directive — catching risky replace actions

Binding on every reviewer of a tranche-C phase branch. CI cannot catch this class; only a reviewer
can. The failure mode is a **silent revert**: a branch built from a source that predates a fix
carries an older copy of a file, and overwriting main's newer copy removes the fix. It does not
appear as a conflict, and in review it does not look like a deletion — it looks like a normal edit.

**1. Measure at the endpoint, not with three-dot.**
Review the branch with `git diff upstream/main <branch>`. Three-dot (`main...branch`) is correct
only for a branch rebased on current main; on a stale base it silently reports `-0` deletions
almost everywhere. If a large port shows a near-universal zero deletion column, **you are holding
the wrong measurement** — real ports delete something (rules 324/325).

**2. Require the deletion report, and check it against the diff yourself.**
Each phase must arrive with per-file endpoint deletion counts and a line-by-line justification for
every deleted line. Re-run the numstat and confirm the counts match the report. A deletion the
author cannot name is a revert they have not noticed. Reject the branch rather than accept
"regeneration churn" as a blanket explanation — **localise deletions to specific files first**;
a new entity fails loudly, a modified aggregate file reverts silently.

**3. A file "add" that overwrites a newer file is the most dangerous shape here.**
`--diff-filter=A` under three-dot means "added since merge-base", **not** "absent from main". The
only valid absence test is `git cat-file -e upstream/main:<path>`. Any path the author calls new
that already exists on main is a wholesale overwrite: it must be ported **by hunk**, never copied
(Ruling A-2).

**4. Verify the prohibition list survives, by content.**
Squash-merge blinds ancestry, so `git cherry` and patch-id matching are useless here. Check by
distinctive identifier: confirm each protected item still appears on the branch at a count no lower
than main's. Three items are **not** gateable by identifier counting and require review by
attention: `EnsureParticipant`'s `left_at` preservation, the `direct` non-empty `external_ref`
predicate, and the P2 commit series.

**5. Security-relevant invariants a reviewer must not let through.**
Sender identity is always derived from the authenticated caller, never the request body (G-1).
Self-skip is by `SenderID`, never the display `Sender` label (B5/R1). Parse failure on a DM key
denies — no fallback, no repair, no "best effort" — because the key *is* the ACL. Under-granting is
recoverable; over-granting is not.

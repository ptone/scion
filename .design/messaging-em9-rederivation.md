# Design: Re-deriving em9-unify onto current main, in slices

**Author:** ca-msg-arch  **Date:** 2026-08-29  **Status:** APPROVED (ptone, 01:39Z — "yes rederive in slices")
**Base:** `upstream/main` @ `a7ac9c489`
**Sources:** `scion/ca-msg-em9-unify` @ `47a7c6736`, `scion/messaging-v2` @ `91c9e3146`
**Supersedes:** the plan to merge `em9-unify` as a single integration branch.

---

## Problem & Goals

`scion/ca-msg-em9-unify` carries ~22k lines of authored messaging work that we want on main. It cannot be merged. It forked at `6268bac44` and main has since moved 45 commits ahead, including five security PRs and a seventeen-commit Permissions Phase 2 series. The branch predates all of it.

Three independent failures were measured against `a7ac9c489` in throwaway worktrees (§5fu, §5fv):

1. **It does not build.** `pkg/ent` splices across two generation runs; after regenerating, `pkg/hub/handlers_agent_messaging.go` still fails — git took a call site from main and its callee from em9 with no conflict, because they live in different files.
2. **It edits its own guard.** A merge rewrites `hack/check-conversation-upsert-guard.sh` to drop `AddParticipant` from the watched surface and to permit raw `INSERT INTO conversations` in `pkg/hub/`. The guard then reports "no violations" on a tree containing twelve such inserts. Main's copy of the same guard exits 1 on eight of them.
3. **It reverts six PR families.** Five security (B5 #1343, #1322, #1338, #1347, #1349) and the entire P2 series, now seventeen commits.

**Goals.**
- G1. Land the authored work on main without reverting anything main has gained since `6268bac44`.
- G2. Every slice is independently reviewable, independently revertible, and green on its own.
- G3. Revert risk is caught mechanically, not by reviewer attention.
- G4. Nothing from either source branch is silently dropped.

**Success criteria.** Tranches C–G land as separate PRs. At every point main builds, CI is green, and the guard set is monotonically non-weakening.

## Non-Goals

- **Not** flipping `conversation_read_switch`. Tranche G is the flip and is out of scope until C–F have soaked. B10 stands: derivation failures stay non-fatal until the S4 read-switch.
- **Not** resolving DEF-32 federated identity linking. Blocked on a product decision, required before S4, independent of this work.
- **Not** landing tranche H in the C–F sequence. **G-1 is FIXED** as of `47a7c6736` — the request struct is `{Reference, ProjectID}` and identity derives from the auth context (verified 2026-08-29). H is now blocked only on strengthening its regression guard, which has a verified `omitempty` evasion; that is a one-line change inside H's own slice.
- **Not** preserving em9-unify's commit history. We are re-deriving content, not replaying commits.
- **Not** carrying `cmd/deploy_instance{,_test}.go`. Main deleted both in #1325.

---

## Proposed Design

### The core invariant: slices are additive

> **A slice may create new files freely. On any file that already exists on main, a slice may only ADD.**

Every measured failure above is a deletion that nobody intended. Making deletion the exceptional case — one that must be named in the PR description and reviewed on purpose — removes the entire class.

This is enforceable mechanically rather than by attention:

```
git diff --numstat main...slice -- $(files existing on main) | awk '$2 > 0'
```

Non-empty output fails the slice unless every row appears in a `DELETIONS-JUSTIFIED:` block in the PR body. This is cheap, has no false negatives, and is the single highest-value control in this design.

Two carve-outs, both narrow:
- `pkg/ent/**` — excluded entirely; see regeneration below.
- gofmt realignment in `models.go` / `types.go`, identified in the manifest as cosmetic. Must be a separate commit that touches nothing else.

### Generated code is regenerated, never ported

`pkg/ent` is +22,975/−10,526 on the branch — 45% of the diff and zero authored content. Main and em9 both changed the same four schemas (`conversation.go`, `conversation_participant.go`, `message.go`, `message_addressee.go`); main additionally changed three em9 has never seen (`entitlementbinding`, `limitdefinition`, `usagereservation`).

Per slice: port the **schema** change only, run `go generate ./pkg/ent/...`, commit the result as its own commit. A merged generated file belongs to neither generation run and is internally inconsistent — the observed `c.hooks.EntitlementBinding undefined` is exactly that.

### Guards are verified with main's copy, always

From §5fu: a slice that edits a guard grades itself. Therefore:

> **Slice verification runs the guard scripts from `origin/main`, not from the slice.**

```
git checkout upstream/main -- hack/     # in the slice worktree
./hack/check-security-marker-gates.sh
./hack/check-conversation-upsert-guard.sh
./hack/check-authz-guards.sh
git checkout HEAD -- hack/              # restore
```

**Do not extract the scripts to `/tmp`.** Each begins `cd "$(dirname "$0")/.."`, so outside the repo the root resolves to `/` and the guard silently analyses nothing. Found by em6 on the Phase 1 rehearsal — which is what the rehearsal was for.

A slice that needs a guard relaxed does that in a **standalone PR containing only the guard change and its justification** — never bundled with the code the relaxation permits. Guard changes are reviewed as security changes.

Corollary: the eight raw `INSERT INTO conversations` sites in `webchannel_store*.go` are a real design question (§2.6.4 dual-write vs. the "hub has no raw SQL path" property). That question gets its own PR and its own decision. It does not ride in on a code slice.

### Per-file source-of-truth table

**em9-unify is not a superset of messaging-v2.** Nine files exist on v2 and on neither em9 nor main:

```
cmd/server_backfill.go                    cmd/server_backfill_test.go
cmd/server_backfill_volume_test.go        cmd/server_foreground_backfill_test.go
+ 5 .design/project-log/*.md
```

The four `server_backfill*` files are DEF-12, which em10's manifest already flagged as the highest silent-drop risk — correctly, and for a reason the manifest did not know: the file is not on the integration branch at all. Sourcing only from em9-unify drops the feature without a trace.

Therefore each slice spec carries an explicit **file → source-branch** table, and the union of all slice tables must mechanically account for every path in `(v2 ∪ em9) − main`. That set is the completeness denominator. Measured against `a7ac9c489`: **82 paths — 35 code, 2 root-level markdown, 45 design-log.** It must be re-derived at slice-planning time, not trusted from this document.

**The manifest cannot see half of this set, by construction.** em10's `TRANCHE-MANIFEST.md` is scoped `messaging-v2` vs main, so em9-only files are structurally invisible to it. Two such files exist and appear nowhere in its 529 lines:

```
pkg/hub/webchannel_store_def27_test.go     (em9 only)
pkg/hub/webchannel_store_unify_test.go     (em9 only)
```

This is the exact mirror of the `server_backfill*` gap, which is v2-only and invisible to anything scoped to em9. Neither branch is a superset; a manifest scoped to either one has a blind spot the size of the other's exclusive content. The union must be computed directly.

### Hunk-level porting for modified files

Six files span multiple phases and must be decomposed by hunk, never taken wholesale: `handlers_broker_inbound.go`, `handlers_agent_messaging.go`, `handlers_chat_v2.go`, `server.go`, `route_metadata.go`, `cmd/message.go`.

For these, the porting procedure is **forward-only**: start from main's version, apply the slice's behavioural addition by hand, and never `git checkout <branch> -- <file>`. The manifest's prohibition list is the review checklist.

### The prohibition list

Carried forward from the manifest, extended for main's movement since. No slice may remove:

- `authenticatedSender` and its 7 call sites (B5 #1343)
- `parseDMKeyIDs` and the 4 DM-ownership callers across 3 files (#1322)
- `isDMParticipant` kind-label tightening (#1322)
- `validateDefaultAgent` and its 3 call sites (#1338)
- All 3 `ActionAttach` authorization checks (#1347)
- `EnsureParticipant` and its `left_at` preservation (#1349, B6/B7)
- `Broadcasted = true` server-side forcing
- The DM-key canonicality rejection in `dm_key.go` and its tests (#1362)
- The `direct` conversation non-empty `external_ref` predicate (DEF-29) and `checkDMParticipantKey` (#1360)
- `AddParticipant` from the upsert guard's watched surface (#1339)
- B5 test coverage: 8 functions in `handlers_agent_messaging_test.go`, 3 in `chat_notifications_test.go`, 1 setup in `messagebroker_test.go`
- The P2 series in full — now 17 commits, not the 10 the manifest recorded

Fifteen of these are already mechanically enforced by `check-security-marker-gates` (#1361, #1363). The rest are review-checklist items, and the gap between the two lists is itself a work item — see Phase 0.

---

## Alternatives Considered

**A. Merge em9-unify and fix forward.** Rejected on measurement. 31 honest conflicts, does not build, and the semi-careful resolution path is precisely where silent reverts enter: an engineer fixing compile errors one at a time makes a main-vs-em9 choice at each one, unaided. The compiler catches signature drift and is blind to behavioural drift where signatures agree.

**B. Guard all eight at-risk files first, then merge.** This was the live alternative and I recommended against it. It fails because one of the things needing a guard *is* a guard: the merge edits `check-conversation-upsert-guard.sh` itself. Guard-then-merge also leaves the build failures and the P2 revert untouched — guards are a detection mechanism, and the problem here is not primarily detection.

**C. Rebase em9-unify onto main.** Rejected. 124 commits replayed across a 45-commit drift, each with its own conflict resolution, and the intermediate commits do not build. Strictly worse than re-deriving: same manual decisions, more of them, and no reviewable unit at the end.

**D. Abandon the work and re-implement from the design docs.** Genuinely considered — the design docs are good and the branch is a liability. Rejected because the authored work includes hard-won test coverage (~1,200 lines of envelope/delivery/validate tests) and the DEF-12 backfill tooling, none of which is reconstructible from the design docs at reasonable cost. Re-derivation keeps the artefacts and discards only the history.

---

## Migration / Rollout

Slices land as independent PRs to main, in dependency order. Each is green on its own; main is never left in an intermediate state.

New machinery lands **flag-gated OFF**. The read-switch (`conversation_read_switch`) stays off through C–F; tranche G is the flip and is a separate, deliberate decision with its own soak. Dual-write remains non-fatal per B10 until the S4 read-switch — derivation failures must not start rejecting requests as part of this work.

Rollback is per-slice: each PR is revertible without touching its neighbours, because no slice deletes another's code.

---

## Open Questions

1. **The eight raw `INSERT INTO conversations` sites.** Is `pkg/hub/webchannel_store*.go` a sanctioned dual-write path (em9's position) or a violation of "hub has no raw SQL path to the conversations table" (main's position, #1339)? Needs a ruling before any slice touches webchannel storage. **Owner: me, needs ptone input.**
2. **DEF-12 tranche assignment.** The `server_backfill*` cluster has no tranche letter in any version of the plan. Proposal: its own slice, ordered after D. **Owner: me.**
3. **G-1 fix shape.** Ruling stands that both body fields are deleted rather than validated. Unchanged; blocks H only.
4. **DEF-32 federated identity linking.** Unchanged, blocks S4, not this work.

---

## Implementation Phases

**Phase 0 — Slice-plan groundwork (me + em10).** Re-derive the delta mechanically in **two manifests, not one** (see Addendum A — the original single-manifest instruction was wrong):
- **M-ADD**: `(v2 ∪ em9) − main`, the paths absent from main — 82 as of `a7ac9c489`. *Delivered.*
- **M-MOD**: paths present on main that either branch also changed and whose change main has not absorbed — **38 substantive paths, ~1100 lines**. *Outstanding.*

Produce a file → source-branch → slice table covering both; diff the prohibition list against what the marker gate already enforces and specify gate rows for the gap. No production code. Output: `SLICE-PLAN.md`.

**Phase 1 — Docs slice.** `.design/project-log/**` from both branches, 45 files, zero code. Lands first as a low-risk exercise of the whole pipeline: additive check, main's-guards check, CI.

**Phase 2 — Tranche C: envelope + delivery + broker edge.** 8 new files in `pkg/messaging`, 13 extras adapter modifications, hunk-level work in `handlers_broker_inbound.go`. Largest slice; split further if the additive check shows any deletion.

**Phase 3 — Tranche D: validation choke point.** `validate*.go` (4 files), the hub integration test, `VALIDATION_EXEMPTIONS.md`, and D's hunks in `cmd/message.go`.

**Phase 4 — DEF-12 backfill.** The 4 `server_backfill*` files, sourced from **messaging-v2**.

**Phase 5 — Tranche E: read-switch machinery + divergence board.** 3 new files. Flag-gated OFF.

**Phase 6 — Tranche F: CLI.** `broadcast.go`, `keys.go`, help grammar and deprecation tests, F's hunks in `cmd/message.go`.

**Phase 7 — Tranche G: flip the read switch.** Separate decision, separate soak, not part of the landing sequence.

Tranche H stays blocked on G-1.

---

## Acceptance Criteria

Per slice, all must hold before I take it as a merge candidate:

1. **Builds.** `go build ./...` exits 0. Non-negotiable and separately checked — "the guards pass" is not "CI is green" (rule 279).
2. **Additive.** `git diff --numstat main...slice` shows zero deletions on files existing on main, outside `pkg/ent/**`, except rows named in `DELETIONS-JUSTIFIED:`.
3. **Guards, from main.** `check-security-marker-gates.sh`, `check-conversation-upsert-guard.sh` and `check-authz-guards.sh` **as they exist on origin/main** all exit 0 against the slice tree.
4. **Guards, not weakened.** If the slice modifies any file under `hack/`, the slice is rejected and the change is resubmitted standalone.
5. **Generated code is generated.** After `go generate ./pkg/ent/...`, `git diff --exit-code -- pkg/ent/` is empty.
6. **Tests run.** New tests carry no `//go:build !no_sqlite` unless justified in the PR body — CI runs `go test -tags no_sqlite ./...` and anything behind that tag is never executed (rule 258).
7. **Non-vacuous.** For each new test asserting a rejection, the PR shows it failing when the guarded behaviour is removed. A green suite that stays green without the fix does not cover the fix (rule 65).
8. **Prohibition list clean.** No entry removed. Spot-checked by the reviewer against the list above; the 15 marker-gate rows are checked mechanically.
9. **Empty diff over recently-changed files.** For the files main changed most recently, `git diff main...slice -- <those files>` is empty or additive-only. This is the only reliable proof a prior tranche survived (rule 31).

For the sequence as a whole: after the final slice, `git diff main...em9-unify` restricted to authored (non-`pkg/ent`, non-docs) paths should contain nothing we intended to keep. Whatever remains is either deliberately dropped or a miss — and must be enumerated either way.

---

## Addendum A — the modified-path manifest (M-MOD)

*Added 2026-08-29 by ca-msg-arch. Corrects a defect in this document's own Phase 0.*

### The defect

Phase 0 as originally written said: *"Re-derive `(v2 ∪ em9) − main` mechanically (82 paths); produce the file → source-branch → slice table, **covering all 82**."* That set contains only files **absent from main** — i.e. added files. But Phases 2, 3 and 6 of this same document call for work on files that **do** exist on main ("13 extras adapter modifications", "hunk-level work in `handlers_broker_inbound.go`", "D's hunks in `cmd/message.go`"). Phase 0's scope statement was therefore inconsistent with the phases beneath it.

em10 executed the instruction faithfully and delivered a correct, complete 82-path manifest. The instruction was the error. **This is an architect defect, not an EM defect.**

Left uncorrected, the sequence would land the new messaging library and never wire it to the CLI or the brokers — the S4 conversation-reference parsing in `cmd/message.go` and the `--channel` / `--thread-id` deprecation, which are the originating brief, are in M-MOD and appear nowhere in the current plan.

### Measurement (three-dot, merge-base `6268bac44`, against `upstream/main` = `a7ac9c489`)

Two-dot `git diff main <branch>` is wrong here: it reports main's own advances as branch changes (it showed a spurious `D = 98`). All figures below are three-dot.

| Set | Count | Disposition |
|---|---:|---|
| M-ADD — absent from main | 82 | manifested by em10; **complete, no gap** |
| added-since-merge-base but main already has them (P2 series, #1339) | 27 | nothing to port; correctly excluded |
| modified, main already absorbed the change | 6 | nothing to port |
| modified, delta is *only* `newTestStore(":memory:")` → `newTestStore(t, ":memory:")` | 31 | **DROP — see ruling below** |
| **M-MOD — modified, substantive unported content** | **38** | **missing from the plan** |

### Ruling A-1 — drop the `newTestStore` refactor

31 of the 69 unported paths differ from main by nothing but a test-helper signature change. It is incidental hygiene from the old branches, unrelated to the messaging contract, and it cannot be done additively (it rewrites existing lines in ~31 files). Carrying it would force a 31-file `DELETIONS-JUSTIFIED:` block whose review value is nil and whose revert risk is not.

**Dropped from the re-derivation.** If wanted, it lands later as its own mechanical PR, reviewed as a refactor rather than smuggled through a messaging slice.

### Ruling A-2 — M-MOD files are ported by hunk, never by file copy

This restates §"Hunk-level porting for modified files" above, which already had the principle right but named only six files by inspection. The measured scope is **38**. What follows is the evidence for why that section is load-bearing rather than advisory — it is the single most dangerous thing in the remaining sequence.

All 8 B5 test functions that exist on main — `TestAgentMessage_B5_SpoofedSenderDoesNotDeriveConversationKey`, `TestBroadcast_B5F1_*`, `TestBroadcast_R1_*`, `TestBroker_R2_*`, `TestBroker_R3b_*` and the rest — are **absent from em9's copy of `handlers_agent_messaging_test.go`**, because em9's base predates B5 (#1343). Nothing on the branch deleted them; the branch simply never had them.

The consequence: **copying a branch file over main's is a silent revert even though the branch shows no deletion.** A three-dot diff of the branch looks innocent. Only the diff *against main* exposes the loss — which is exactly what the additive-only guard measures, and is why that guard is the load-bearing control of this design rather than a formality.

Therefore, for every path in M-MOD:
1. **Main's file is authoritative.** Start from main's copy, never the branch's.
2. Extract the branch's *added* lines only: `git diff -U0 <merge-base> <branch> -- <path>`.
3. Apply those hunks onto main's copy by hand, reconciling against what main has since changed.
4. `git diff --numstat main...slice -- <path>` must show `0` deletions, or carry a per-file `DELETIONS-JUSTIFIED:` block.

For M-ADD paths (absent from main) wholesale copy remains correct and safe. The two manifests have **different porting procedures**; conflating them is how B5 gets reverted.

### Deletion profile of M-MOD

Measured on em9, excluding `newTestStore` churn:

- **`pkg/hub/handlers_agent_messaging.go`: +281 / −0.** The highest-risk file on the prohibition list is *purely additive*. The invariant holds natively — no justification block needed.
- 0–4 deletions: the great majority, including every `extras/**` broker adapter.
- Genuine `DELETIONS-JUSTIFIED:` required on four paths only:
  - `cmd/message.go` (−13) — the flag surface is deliberately rewritten: `--broadcast`, `--all`, `--in`, `--at`, `--plain`, `--raw`, `--attach`, `--notify`, `--channel`, `--thread-id`, `--cc` move to a hidden/deprecated registration block. **This is the brief, not collateral.** Deprecated flags must still function and warn.
  - `docs-site/src/content/docs/reference/cli.md` (−17), `resources/platform_skills/scion-messaging/SKILL.md` (−15) — prose rewrites.
  - `pkg/hub/handlers_agent_messaging_test.go` (−12) — touches `chatSendLimiter` bucket assertions. **Review by attention.** Not gateable by ident counting; the prohibition list already flags B5 coverage here.

### Revised tranche assignment for M-MOD

| Tranche | Paths | Content |
|---|---:|---|
| C (broker edge) | 20 | `handlers_broker_inbound{,_test}.go`, `messagebroker{,_test}.go`, `pkg/hubclient/{messages,agents}.go`, and the 5 `extras/**` adapters + tests |
| C/D (hub handlers) | 7 | `handlers_agent_messaging{,_test}.go`, `handlers_messages.go`, `handlers_chat_v2.go`, `attachments_agent_test.go`, `route_metadata.go`, `teststore_test.go` |
| D + F (CLI) | 1 | `cmd/message.go` — split by hunk across both tranches as the phases already specify |
| B′ (docs, modified) | 4 | `messaging.md`, `cli.md`, `glossary.md`, `SKILL.md`. Distinct from the shipped Phase 1 slice, which was *new* docs only |
| — (config/store tail) | 6 | `operational_settings.go`, `opsettings/{registry,sections,opsettings_test}.go`, `server.go`, `route_classification_test.go` |

### Consequences for the sequence

- Tranche C as currently specified (10 new files) is **incomplete**; it would ship envelope and delivery with no caller. C must carry its M-MOD rows or the slice is untestable end-to-end.
- Phase 1 (docs, shipped) and the gate-rows slice are **unaffected** — both are M-ADD only and their verification stands.
- Acceptance criterion 9 already covers this case and needs no change.
- **No tranche may be dispatched until `SLICE-PLAN.md` carries both manifests.** Owner: em10, on unpark.

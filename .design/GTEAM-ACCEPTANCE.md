# gteam Acceptance Checklist — Messaging Migration Tranche

**Target:** `scion/tranche-g` @ `98543da921ebd1ee6bae6b1abc4ca00a9d35b327` (2026-09-02: `cbd4f8b6a` → `5ca3e4026` M9 → `98543da92` M9a — see §1.6)
**Instance:** `scion-gteam` (`deploy-demo-test` / `us-central1-a` / 10.128.0.56)
**Status:** ptone's ruling — this checklist gates the fresh-cutover test on a second
instance. Fresh cutover happens only after everything here is done.

---

## 0. What this document is

The design's §9 lists 11 acceptance criteria. Those are written against *tests*. This
document asks a different question: **which of them can gteam actually prove, and which
must be carried as declared gaps?**

That distinction matters because gteam is production data on an integration instance. It
is an extremely good instrument for some of these criteria and a structurally useless one
for others. Recording which is which prevents two opposite errors: claiming coverage we
do not have, and re-litigating a gap that was closed elsewhere.

**Six of the eleven are exercisable on gteam. Five are not.** All five have coverage
elsewhere. None of the five is exercisable by *any* amount of additional gteam work — the
reasons are structural, not scheduling.

---

## 1. Standing constraints for whoever executes this

Binding on every step below.

- DB inspection uses a **read-only** connection (`file:...?mode=ro`).
- **Never dump the `value` column of `hub_settings`.** Those documents may contain
  credentials. Work from section names only.
- Conversation IDs and `external_ref` keys are keys, not content — fine to report.
  **Message bodies are not.**
- **Never** point a write-capable migration command at the live DB or at a retention
  snapshot. `--execute` only ever against a throwaway copy. If `--db` does not appear to
  take effect, stop and report rather than working around it.
- The `messaging` section row in `hub_settings` must not be deleted or modified.
  Deleting it silently converts settings variant B into variant A and invalidates
  everything downstream of it.
- Do not delete `/usr/local/bin/scion.rollback-1a2c1b07` — not ours.

### 1.1 Protected fixtures

Three conversations on gteam are the only live reproductions of distinct refusal classes.
**None may be deleted, modified, or "repaired" during acceptance testing.** Several checks
below assert directly against them.

| ID | Class | Used by |
|---|---|---|
| `adf13f87` | keyless `direct` row (DEF-29) | G-10 |
| `f003ad87` | old-format key, unrepairable — names `da7cf1ab`, which exists nowhere | G-5, gap on AC-4 |
| `764af9a2` | well-formed key, both principals deleted (DEF-125) | G-5 |

If any check appears to require changing one of these, the check is wrong. Stop and raise
it.

---

## 1.5 Results — deploy of `cbd4f8b6a`, 2026-09-02

Snapshot `/home/scion/hub.db.pre-cbd4f8b6`, sha256
`a61754a44dc97bc3d12e13a7633cab95635c018e59ae3ca13722da3db433dca2`, 141,082,624 bytes.

Boot 1: 39 projects, 19,083 processed, 6,476 attributed, 1,010 skipped, 11,597 row errors,
~37s. `unreachable` 5,637 (INFO), `reachable` 12,583 (WARN). Boot 2: both migrations
skipped, **both counts identical**. Healthy after both; 27 agents, 39 projects.

| Check | Result |
|---|---|
| G-1 Idempotence | **PASS** — boot 2 wrote nothing, counts identical |
| G-2 M-1′ refusal half at scale | **PASS** — marker written despite 11,597 refusals; no re-run |
| G-3 Boot never blocked | **PASS** — healthy after both boots |
| G-4 Residual report | **PASS on both halves.** Boot 2's steady-state path had never run on real data; counts held exactly |
| G-5 Fail-closed | **PASS** — `f003ad87` and `764af9a2` unchanged, neither repaired |
| G-6 B14 | **PASS** — `adf13f87` still keyless |
| G-7 Post-cutover UX | not started — ptone's, interactive; the last open gteam item |

**The most valuable result is boot 2.** M6's anti-join and M7's consistency property both
compute the reachable count with no per-project sums in existence, and that path had only
ever run against fixtures. It reproduced to the row.

**One finding, and it is against M9, not this build.** Row errors + skipped = 12,607, but
only 12,583 messages are actually still unattributed — an excess of 24. M9's first draft
would have subtracted the former from the latter and clamped the negative to zero, looking
correct in every unit test. Design §4.8 corrected: the permanent residual is now *measured*
per project rather than tallied from `BackfillResult`. instance-investigator asked for the
per-cause breakdown to close the 24 exactly.

## 1.6 M9 landed — G-4 must be re-run on the redeploy

`scion/tranche-g` advanced `cbd4f8b6a` → **`5ca3e4026`** (M9, 2026-09-02). G-1 through G-3,
G-5 and G-6 are unaffected and stand as recorded. **G-4 was passed against the pre-M9
report and must be re-run**, because M9 deliberately changes what boot prints.

The 24 is closed and needs no further investigation:

```
non-broadcast in active projects = 19,083 - 990 broadcast = 18,093
   6,476 attributed | 20 skipped w/ conv_id | 11,597 row errors = 18,093
row_errors (11,597) = derive_failures (11,593) + non-derive (4)
reachable  (12,583) = 990 broadcast + 11,593
gap 24 = 20 (skipped, already had a conversation_id) + 4 (Inferred stamps)
```

**Post-M9 expectation for G-4:**

| Line | Level | Expected |
|---|---|---|
| `unreachable` | INFO | ≈ 5,637, unchanged |
| `permanent` | INFO | ≈ 12,583 — the population that used to be the WARN |
| `actionable` | WARN | **absent** (fires only when > 0, and must reach 0 *without* the clamp) |
| post-derivation failures | WARN | fires only if > 0, and carries **no remedy string** |

Per-project lines now carry `inferred` and the per-cause `derive_*` breakdown.

### 1.6.1 Result — deploy of `5ca3e4026`, 2026-09-02

Snapshot `hub.db.pre-5ca3e4026`, sha256 `1f9b0f84b06ca4f95c41dcf4900e62da1d6b346bc1afb5149eeb68fd0522179b`.

**G-4 re-run: PASS on every check that decides it.**

| Check | Result |
|---|---|
| WARN `Messages remain unattributed` absent | PASS — 0 occurrences |
| `backfill --execute` absent from boot log | PASS — 0 occurrences |
| `unreachable` stable | PASS — 5,637 on both boots |
| `permanent` INFO present | PASS — 12,583, re-summed by me from the per-project column |
| Boot 2 idempotent | PASS — M9 marker recognised, backfill skipped |
| Protected fixtures intact | PASS — all three |

`actionable` reaches zero **by arithmetic, not by clamping** — the condition this whole
milestone turned on. Per-project lines now carry `inferred`, `write_failures`,
`resolution_failures`, `permanent_residual` and the per-cause `derive_*` breakdown. All
derive failures are `derive_principal_pair` except one `derive_dm_key_parse` (`84e32420`,
the newline-in-UUID row).

**M9a verified at scale, 98543da92.** Run against a throwaway copy of the pre-M9
snapshot (no writes to the live DB): `total_residuals` = **11,593**, the correct
this-pass-only value, where the defect produced 23,190. I re-derived every column: all 22
per-project identities balance, `sum(row_errors)` == `total_residuals`, and
`permanent - derive` = **990**, exactly the broadcast population derived independently
months earlier. The decomposition closes end to end. **G-4 is CLOSED.**

Limit worth stating: every message in that snapshot was already stamped, so
`attributed = inferred = 0` and `persistGroup` was never reached. The +4/-4 cancellation
cannot arise when `inferred` is zero, so this run does not re-prove that path; gate C's
fixture and the earlier G-2 run cover it.

**One defect found, in a field the checklist did not cover.** `total_residuals` reported
23,190, which is this pass's 11,593 plus the *previous* pass's 11,597 — the accumulator
carries forward across a completed pass. Log-and-marker only; it never feeds the bucket
arithmetic, so nothing above is affected. **M9a dispatched.** G-4 does not need a third
re-run for it; the re-verification is the M9a gate suite plus one confirming boot.

---

**The check that matters:** `actionable` must reach zero by arithmetic, not by clamping.
If the WARN still prints 12,583, M9 has not taken effect. If it prints a small non-zero
number, that is real drift since the pass and should be reported rather than annotated.
No line anywhere in the report may advertise `scion server backfill --execute` (DEF-111,
which M9 re-introduced once and now has two source-scan gates forbidding).

---

## 2. Preconditions

- [ ] **P-1.** Fresh retention snapshot taken *before* the deploy, with filename, sha256
      and byte size recorded. Reversibility is what makes the live backfill an
      acceptable act rather than an escalating one.
- [ ] **P-2.** `cbd4f8b6a` built and deployed; `scion-hub` restarted; `/healthz` healthy.
- [ ] **P-3.** Boot 1 and boot 2 logs captured in full, to files.
- [ ] **P-4.** All messaging switches ON (ptone pre-authorised the atomic flip, to
      simulate the single-cutover upgrade path users will get).

P-1 through P-3 are delegated to `instance-investigator`; the deploy request is out.

---

## 3. Exercisable on gteam

### G-1 — Idempotence (design AC-1)

Boot 2 performs no conversation or message writes.

**Procedure.** Before boot 2, record: count of `conversations`, count of `messages` with
non-null `conversation_id`, count with null. Restart. Re-record.

**Pass:** all three counts identical across the restart, and boot 2 logs both migrations
skipping. Assert on **counts, not wall time** — a fast boot is suggestive, not proof.

**Why gteam is the right instrument:** 39 projects and 19k messages, so any accidental
re-write is large and obvious. A unit fixture can be re-written entirely without anyone
noticing the cost.

---

### G-2 — M-1′, refusal half (design AC-2, first half)

**gteam is the only place this is proven at scale, and it is the single most valuable
thing this instance tells us.**

M-1′ says a marker records that a *full pass completed without a run-level failure* —
row-level refusals are counted and persisted, not treated as failure. The superseded M-1
would have withheld the marker on any refusal. gteam has ~11,593 deterministic refusals.
Under M-1 it would re-run the entire backfill on every boot, forever, making no progress.
That is the livelock §4.3 describes, and gteam is where it would actually bite.

**Pass:** after boot 1, the backfill marker is present with a non-zero residual count
recorded alongside it, despite ~11,593 refusals. Boot 2 does not re-run.

**Fail condition to watch for:** marker absent, or boot 2 re-running the full backfill.

---

### G-3 — Boot is never blocked (design AC-6)

**Pass:** `/healthz` healthy after both boots; hub serving; agent and project counts
sane. Note that the first boot does real migration work, so this also confirms the
synchronous hook does not starve the listener in a realistic case.

---

### G-4 — The residual report on real data (design AC-9)

Boot 1 and boot 2 each emit a residual report. Both parts of AC-9 apply.

**Expected:** INFO with `unreachable` ≈ 5,637 (messages pointing at project IDs with no
row — hard-deleted projects, permanently unattributable, correctly *not* in the actionable
bucket). WARN with reachable ≈ 12,606.

**Pass on boot 1:** both lines present, orphans in the INFO bucket, and **no log line
advertising `scion server backfill --execute`** (DEF-111 — the advertised remedy had a
different denominator than the warning).

**Pass on boot 2 — the harder half.** The same two numbers, unchanged. Boot 2 performs no
backfill work, so the reachable count is computed with no per-project sums in existence.
That path has never run against real data. **If the reachable number collapses to 0 or the
WARN disappears on boot 2, that is a genuine defect** and takes priority over everything
else in this document.

**Known deviation — do not file as new.** The ≈12,606 WARN is expected and permanent.
Those messages carry email addresses where user IDs are required; no key is derivable and
no retry shrinks the number. M6 split "project row deleted" from "everything else" but
left this third population — reachable project, permanently underivable — in the
actionable bucket. Escalated to ptone as candidate **M9**. Until resolved, annotate rather
than chase. This check passes with the number present and stable; it does not require the
number to be small.

---

### G-5 — Fail-closed preserved on real refusals (design AC-5)

**Procedure.** After boot 1, read `external_ref` for `f003ad87` and `764af9a2`.

**Pass:** both unchanged. Neither acquired a key naming principals that are not its
actual principals. `f003ad87` in particular must not have been "repaired" toward a
plausible-looking key — its counterparty does not exist, and a best-effort repair here
would be the exact failure the standing rule forbids. Parse failure denies, always, with
no fallback anywhere on the derivation path.

**Why gteam:** these are real unrepairable rows produced by real history. A seeded
fixture proves the code path; these prove the code path meets data it did not anticipate.

---

### G-6 — B14 after auto-run (design AC-10)

**Procedure.** After boot 1, read `adf13f87`.

**Pass:** still keyless; `external_ref` still empty; no participants invented from the
listing index; reported as *skipped* in the migration output.

This is the assertion that catches someone implementing the behaviour DEF-113's stale
name used to advertise. Deriving a key from the participant index would fabricate an ACL
out of the listing index, inverting the direction of authority — the key IS the ACL.

---

### G-7 — Post-cutover UX (ptone's explicit scope)

Not a design AC. ptone's ruling: *"we can continue to do QA work on gteam for the UX post
cutover."* With all switches flipped, exercise as the end state, not as a hybrid:

- [ ] Send a DM; confirm delivery and correct attribution to both sides.
- [ ] Open an existing DM thread; confirm history renders (this is where the earlier
      error surfaced, and ptone confirmed older threads reproduced it).
- [ ] Group conversation send and history.
- [ ] Agent-to-agent messaging.
- [ ] Confirm the 409 path does not render as an empty panel — filed against
      `chat-thread.ts` `fetchHistoryV2`, UI work not staffed. Confirm current behaviour
      and record it; do not fix here.
- [ ] Federated chat: **out of scope.** ptone — *"federated chat has not been something
      that has had real usage, so we can track the improvements for after this main chat
      refactor lands."*

---

## 4. Declared gaps — not exercisable on gteam

Each of these is structural. Listing them is not a request to schedule them.

### Gap A — AC-4, the repair works

**Why not:** AC-4 needs an old-format `dm:<uuidA>:<uuidB>` row **whose participants are a
real user and agent**, so that `isDMParticipant` can be asserted to deny before and grant
after. gteam's only old-format row is `f003ad87`, and it names `da7cf1ab`, which exists
nowhere on the instance. It can never be repaired, so it can never demonstrate repair.

**Covered by:** unit tests asserting against `isDMParticipant` rather than the stored
string. **Also note:** the DM key migration already ran on gteam at `e132380f`/`e8e4cc3bf`
— the repair-eligible population there was consumed before this tranche. gteam has no
remaining repairable rows by construction.

**Consequence:** the fresh-cutover instance is where the repair path gets its live
exercise. This is a substantive reason for the fresh-cutover test beyond cutover mechanics
— worth stating when that instance is provisioned.

### Gap B — AC-8, Postgres concurrency

**Why not:** gteam is SQLite (`journal_mode=wal`), single hub. F5: the advisory lock
protects nothing in the configuration we test most. Two hubs booting simultaneously
against Postgres cannot be simulated here.

**Covered by:** nothing on this instance. Explicitly declared gap per the design's own
instruction that it "must be an explicitly declared gap rather than assumed covered by the
default suite." Carry it forward.

### Gap C — AC-2, run-level-failure half

**Why not:** requires stubbing the store so the pass itself fails. Inducing a store
failure on production data is not an acceptable act on this instance, and the snapshot
does not make it one — the risk is not just the data but the instance's availability for
the rest of this checklist.

**Covered by:** unit tests (list error, write error, context cancelled → ERROR logged, no
marker, next boot retries). Note gteam proves the *other* half at scale (G-2), so the pair
is covered jointly, not in one place.

### Gap D — AC-3, resumption

**Why not:** the budget is 10 minutes and the measured execute time is ~37 seconds. The
budget will not expire on gteam, so `projects_done` grows to complete in a single pass and
mid-list resumption never triggers. Forcing it would require changing the budget, which is
a package var and not operator-settable — deliberately, per the no-third-switch directive.

**Covered by:** unit tests with the budget set to expire mid-list, asserting
`projects_done` grows monotonically and the global marker is set only when every
enumerated project is present.

**Silver lining:** the 37s-vs-10min margin is itself a useful result — it says the budget
is not close to binding on a real corpus of this size.

### Gap E — AC-7 and AC-11, code-level criteria

**Why not:** these are `git diff` and build-tag properties, not runtime behaviour. No
instance can prove them.

**Covered by:** AC-7 (no new operator switch) verified at review — no flag, no settings
field, no admin-API field controls whether the migrations run; this was ptone's explicit
directive and was held across all of M0–M8. AC-11 verified per-change, including whether
the blocking `make test-fast` gate can actually see each new test — a green gate that
compiles the new tests out says nothing.

---

## 5. Coverage summary

| AC | Criterion | gteam | Where proven |
|---|---|---|---|
| 1 | Idempotence | ✅ | G-1 |
| 2 | M-1′ refusal half | ✅ | G-2 — **at scale, best evidence we have** |
| 2 | M-1′ run-failure half | ❌ | Gap C — unit |
| 3 | Resumption | ❌ | Gap D — unit |
| 4 | The repair works | ❌ | Gap A — unit; live on fresh-cutover instance |
| 5 | Fail-closed preserved | ✅ | G-5 |
| 6 | Boot never blocked | ✅ | G-3 |
| 7 | No new operator switch | ❌ | Gap E — review |
| 8 | Postgres concurrency | ❌ | Gap B — **declared gap, uncovered** |
| 9 | Residual report honest | ✅ | G-4 — incl. steady-state, first live run |
| 10 | B14 after auto-run | ✅ | G-6 |
| 11 | Build tags | ❌ | Gap E — per-change |
| — | Post-cutover UX | ✅ | G-7 |

**Uncovered anywhere: AC-8 only.** Everything else is proven either on gteam or in tests.

---

## 6. Exit condition

Fresh cutover on a second instance is unblocked when G-1 through G-7 are signed off and
the M9 question (§ G-4 known deviation) has a ruling — fix-first or annotate-and-defer.
Gaps A–E do not block; they are recorded, not outstanding.

The fresh-cutover instance carries one item this checklist cannot: **Gap A**, the live
exercise of the repair path, which gteam consumed before this tranche began.

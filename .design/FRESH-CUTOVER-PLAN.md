# Fresh-Cutover Test Plan — Second Instance

**Target:** `scion/tranche-g` @ `98543da921ebd1ee6bae6b1abc4ca00a9d35b327`
**Status:** DRAFT. Blocked on one fact from instance-investigator (§2.1) and on G-7 signing
off (ptone's ruling: *"fresh cutover only happens after all other acceptance testing on
gteam is done"*).

---

## 0. Why this test exists, and why it is not a repeat of gteam

ptone's binding directive on the end state:

> when a hub is updated to a version that includes this completed refactor — migrations are
> auto-run, and all switches cut-over by default

**gteam has never done that, and structurally cannot.** gteam arrived at its current state
through a *sequence* of deploys — `45c440bd` → `e132380f` → `e8e4cc3bf` → `cbd4f8b6a` →
`5ca3e4026` → `98543da92` — each running one more migration, with operator attention
between every step. A real user upgrading from an older release runs **all of them, in
order, in a single boot, for the first time**.

That ordering has never been executed anywhere. Not on gteam, not in CI, not in fixtures.

### 0.1 The specific hazard this is looking for

The boot hook runs the DM key migration **before** the message backfill. On gteam those two
ran in *different deploys, months apart*, so the sequence has never been exercised as a
sequence.

If the ordering were ever wrong — or if the DM key migration were to partially fail and the
backfill proceed anyway — the backfill would meet old-format keys, `ParseDMKey` would refuse
them, and those rows would be counted as **derive failures**: permanent, unattributable,
correctly refused, and *wrong*. They were repairable. The report would show a larger
`permanent` bucket and no error at all, because every individual component behaved exactly
as designed.

**This is the failure mode the fresh cutover exists to rule out**, and it is worth more than
Gap A. It is silent, it is permanent-looking, and no unit test can produce it because the
hazard is in the ordering of two migrations, not in either one.

---

## 1. The fixture paradox, and how it resolves

The repair path (design AC-4) needs an old-format `dm:<uuidA>:<uuidB>` row **whose two
principals are a live user and a live agent**, so `isDMParticipant` can be asserted to deny
before the migration and grant after.

Neither obvious instance can provide one:

| Candidate | Why it fails |
|---|---|
| gteam | Repairable population was consumed when the DM key migration ran at `e132380f`. Its one remaining old-format row, `f003ad87`, names `da7cf1ab`, which exists nowhere. Unrepairable by construction. |
| A brand-new instance | No message history at all. Nothing to migrate, nothing to repair, and no backfill population either. A "cutover" over an empty DB proves only that the code does not crash. |

**Resolution: the fresh-cutover instance must be seeded from a pre-migration snapshot.**

The candidate is the retention snapshot **`hub.db.pre-e132380f`** — taken before the DM key
migration ran, so the repairable population should still be intact, and the backfill and M9
markers should be absent entirely. That makes it a genuine "user on an old release" starting
state rather than a synthetic one.

This is the whole plan's load-bearing assumption. It is verified in §2.1 before anything is
provisioned.

---

## 2. Preconditions

- **P-1.** G-7 signed off on gteam. Non-negotiable, ptone's ruling.
- **P-2.** **Container binaries built from the same SHA as the hub binary.** Answered on
  gteam 2026-09-02, and the answer generalises into a standing precondition:

  > gteam's hub binary was correct at `98543da92`, but `SCION_DEV_BINARIES` pointed at a
  > *different checkout* (`/home/scion/scion`, still at `45c440bd`). Every agent container
  > was therefore sideloaded a `scion` and `sciontool` predating the entire refactor.

  The hub being right is not sufficient — there are **two** binary paths on an instance and
  they are configured independently. Verify both before any acceptance run, and record the
  SHA actually built from rather than the one intended.

  This matters more than it sounds. A stale client does not produce clean errors; it
  produces plausible-looking wrong behaviour, which makes every observation in the pass
  unattributable between "stale client" and "real defect". Rebuild authorized on gteam
  2026-09-02 with a rollback copy preserved; restart timing left to ptone.
- **P-3.** Second instance provisioned, isolated from gteam. Its own project, or at minimum
  its own zone and firewall posture. It must not be able to reach gteam's hub.
- **P-4.** `hub.db.pre-e132380f` **copied** to the new instance. The retention snapshot
  itself is never the working file and is never opened writable.

### 2.1 BLOCKING — verify the fixture before provisioning anything

Asked of instance-investigator 2026-09-02. Read-only, on a copy.

1. `hub.db.pre-e132380f` still exists; record its sha256.
2. **The repairable count:** how many old-format `dm:<uuidA>:<uuidB>` rows it contains whose
   **both** principals resolve to a live user and a live agent *in that same snapshot*.
3. Confirm it is genuinely pre-migration: DM key, backfill and M9 markers all absent.

**If (2) is zero, this plan does not work** and the fixture must be constructed instead —
which is a materially different and weaker test, because a hand-built row proves the code
handles a row we designed for it. Better to learn that now than after provisioning.

---

## 3. The cutover run

**One boot. No operator action between migrations.** That is the property under test; any
manual step taken mid-sequence invalidates the result and the run must be restarted from the
snapshot.

Snapshot the working DB immediately before the boot, and record sha256 and byte size.

Predicted sequence, stated in advance so it can be falsified:

```
1. DM key migration runs        (first execution — repairs old-format keys)
2. Message backfill runs        (attributes messages using the REPAIRED keys)
3. M9 residual report emitted   (unreachable / permanent / actionable)
4. Marker written, M9 format, with permanent_residual present
```

### C-1 — Ordering (the §0.1 hazard)

The DM key migration must **complete** before the backfill begins. Assert from the log that
the migration's completion line precedes the backfill's `starting` line. Do not infer this
from the code — read it out of the emitted log of this run.

### C-2 — The repaired keys were actually used

This is C-1's consequence and the number that proves it. The `permanent` bucket must **not**
include the rows repaired in step 1.

Concretely: record the repairable count from §2.1 as `R`. After the run, `permanent` must be
smaller than it would be if those rows had been refused. Compute the counterfactual
explicitly and state both numbers. If `permanent` is `R` higher than expected, the ordering
failed silently and this check is the only thing that will catch it.

### C-3 — Single boot, all switches default ON

No messaging switch was set by hand. Confirm the messaging settings section was not written
before the boot, and that the refactored behaviour is active anyway (§4.6.2: absent/omitted
now yield ON by design). **A malformed settings document must still yield OFF** — that
property is not under test here and must not be disturbed to make this check pass.

### C-4 — Budget

Both migrations in one boot against the 10-minute budget. gteam's backfill alone was ~37s;
the DM key migration's duration over a full population has never been measured. Record each
phase's elapsed time separately. **If the budget is exhausted mid-pass, that is a result,
not a failure** — it exercises the resumption path — but report it prominently, because it
means real users on large instances will resume across boots and we have never watched that
happen end to end.

### C-5 — Residual report is honest

`unreachable` / `permanent` / `actionable` as per M9. Re-derive every column, per project;
confirm `sum(row_errors) == total_residuals`; confirm no line anywhere advertises
`scion server backfill --execute`. Same protocol that closed G-4 — the per-project table is
what makes it provable.

### C-6 — Idempotence

Boot 2 must skip both migrations and report identical counts. Boot 3 likewise.

---

## 4. Gap A — the repair path, live (design AC-4)

The item gteam could not carry.

**Before the boot**, from the copied DB, read-only, pick at least three repairable rows and
record for each: the conversation ID, the `external_ref`, and both principal IDs. Keys and
IDs only — **never message content**.

**Assert before:** `isDMParticipant` denies for both named principals. It must deny, because
the key is old-format and unparseable.

**Assert after:** it grants for both named principals — and, separately:

**Assert the over-granting direction.** For each repaired row, a principal *not* named in
the key must still be denied. This is the check that matters most and the one easiest to
forget, because the natural test is "did the repair work" and a repair that grants too
broadly passes that test.

> **Under-granting is recoverable; over-granting is not.** A wrong key is worse than no key,
> since the key IS the ACL.

**Unrepairable rows must remain unrepaired.** Any row whose principals do not both resolve
must come out of the migration untouched and still denied. Fail-closed is the correct
outcome, not a defect to be fixed on the spot.

Note the standing exception: the migration is the **one sanctioned place** where a DM key may
be rewritten. Nothing this test finds may be used to justify normalising a key anywhere on
the derivation path.

---

## 5. What this instance still cannot prove

Carried forward from `GTEAM-ACCEPTANCE.md` §4, unchanged:

- **Gap B — AC-8, Postgres concurrency.** Still the only AC with no coverage anywhere. This
  instance is SQLite and does not change that.
- **Gap C — AC-2, run-level-failure half.**
- **Gap E — AC-7 / AC-11**, code-level criteria.

**Gap D — AC-3, resumption** may be *accidentally* closed here if C-4's budget is exhausted.
If that happens, say so explicitly rather than leaving it implied.

---

## 6. Open questions

- **OQ-A.** Is `hub.db.pre-e132380f` recent enough to restore under `98543da92` at all? It
  predates several tranches. If schema migrations between then and now are themselves part
  of the boot path, this test also exercises those — which is *more* realistic, but widens
  the blast radius of a failure and makes attribution harder. **Needs a ruling before
  provisioning.**
- **OQ-B.** Should the second instance keep its data after the test, as a standing
  pre-migration fixture for future tranches? Cheap to keep, expensive to reconstruct.
- **OQ-C.** OQ-6 (purging gteam's 5,637 orphans) is irreversible and ptone's. It is
  unaffected by this plan and should not be bundled into it.

---

## 7. Exit condition

The refactor is testable-complete when C-1 through C-6 pass, Gap A is exercised in **both**
directions (grant for named, deny for unnamed), and the declared gaps are recorded rather
than silently dropped.

Merging `tranche-g` to `main` remains ptone's gate, not mine.

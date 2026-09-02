# Fresh-Cutover Test Plan — Second Instance

**Target:** `scion/tranche-g` @ `98543da921ebd1ee6bae6b1abc4ca00a9d35b327`
**Status:** REVISED 2026-09-02. The original load-bearing assumption was falsified by
measurement (§2.1) and the approach changed as a result — see §1. Still gated on G-7 signing
off (ptone's ruling: *"fresh cutover only happens after all other acceptance testing on
gteam is done"*).

**Recommended next action:** staff the F-1 … F-7 fixture classes (§1.3, §4.1) as permanent
test coverage. That work is unblocked now, does not need an instance, and is the durable
half of this plan.

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

## 1. The fixture problem — assumption falsified, approach revised

The repair path (design AC-4) needs an old-format `dm:<uuidA>:<uuidB>` row **whose two
principals are a live user and a live agent**, so `isDMParticipant` can be asserted to deny
before the migration and grant after.

The first draft of this plan assumed `hub.db.pre-e132380f` held such rows. **It does not.**
Measured 2026-09-02 (sha256 `58b6b240…`, 141,082,624 bytes), the snapshot's entire `direct`
population is seven conversations:

| Class | Count | Migration path |
|---|---|---|
| New-format `dm:agent:<uuid>:user:<uuid>` | 5 | step 2, participant rebuild — key untouched |
| Empty `external_ref` | 1 | step 3a, skip (B14 ruling) |
| Old-format `dm:<uuidA>:<uuidB>` | 1 | step 3b — but **unrepairable** |

The single old-format row is `dm:b53249ea…:da7cf1ab…`. `b53249ea` resolves to a user
(ptone). `da7cf1ab` resolves to nothing — not a user, not an agent. `resolveKind()` returns
`("", false)`, the row counts as `Ambiguous`, and it is correctly left alone.

**Repairable rows in the pre-migration snapshot: zero.**

### 1.1 A correction to the record

I had been asserting — in `GTEAM-ACCEPTANCE.md` and in the first draft of this plan — that
gteam's repairable population was *consumed* when the migration ran at `e132380f`. **That is
false.** The population was never non-zero. The pre-migration snapshot has exactly the same
single unrepairable orphan as the post-migration state.

The conclusion I drew was right; the mechanism I gave for it was wrong, and the difference
matters. "Consumed" implies the repair path ran and we merely missed watching it. The truth
is stronger and worse:

> **Step 3b — the old-format rekey — has never executed against real data anywhere.** Not on
> gteam before the migration, not after, not on any instance we hold. Its only coverage in
> existence is unit tests and golden vectors.

### 1.2 Why that inverts the risk, rather than dissolving it

The tempting reading is that a path with zero live population is not worth testing. The
opposite is true, and it is the reason this section exists.

The repair path will never fire on *our* instances. It exists for hubs whose data we have
never seen and cannot sample — older deployments with real old-format history, upgrading
unattended, in a single boot, with no operator watching, **rewriting ACLs**. Under ptone's
single-cutover directive there is no switch to hold it back and no dry-run gate to catch it.

So the population being zero here does not lower the stakes. It removes our only opportunity
to observe the path empirically, and leaves an unattended ACL rewrite covered by unit tests
alone. That is the gap to close, and a live instance was never going to close it anyway.

### 1.3 Revised approach: constructed adversarial population on the snapshot base

The snapshot remains the right **base** — it is genuinely pre-migration (no `_migrations`
section at all) and, per OQ-A below, schema-identical to `98543da92`. What it lacks is a
population, so we add one.

Enumerate the classes the migration must discriminate between, and construct a row for each
from **real** principals drawn from the snapshot itself:

| # | Constructed row | Required outcome |
|---|---|---|
| F-1 | old-format, both resolve (user + agent) | rekey; grant both; deny third party |
| F-2 | old-format, both resolve, reversed lexical order | rekey to identical canonical key as F-1's ordering rule dictates |
| F-3 | old-format, one resolves, one orphan | **no rekey** — the real `da7cf1ab` class |
| F-4 | old-format, neither resolves | **no rekey** |
| F-5 | old-format, both resolve to the *same* principal | **no rekey** — degenerate, must not produce a self-DM ACL |
| F-6 | empty `external_ref` | skip, stay keyless (B14) |
| F-7 | already new-format | participant rebuild only, key byte-identical after |

This is stronger than the live-population test it replaces, not weaker. A live instance would
have handed us whichever classes it happened to contain — here that was one class, the
unrepairable one. The constructed set covers the discriminations exhaustively, and the ones
that must **not** repair (F-3, F-4, F-5) are the security-relevant half.

Each row needs at least one message so the backfill has something to attribute; without that
the ordering checks in §3 are vacuous.

### 1.4 Constraints on building the fixture

- **No production code may gain the ability to emit an old-format key.** The fixture is built
  by direct SQL against a throwaway copy. Adding a helper that writes the legacy format —
  even a test-only one exported from production packages — would create exactly the hazard
  the derivation rules exist to prevent.
- **Do not derive the old format by running production functions in reverse.** Pattern it on
  the one real specimen above, and read the ordering rule out of the migration's *parser*,
  not out of a single data point. One specimen (`b53249ea` < `da7cf1ab`, ascending) is
  consistent with a sort rule but does not establish one.
- **Never point a write at the retention snapshot.** Copy first; the snapshot is read-only
  forever.
- Real principal UUIDs, synthetic message content. Report keys and IDs, never bodies.

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

### 2.1 RESOLVED — the blocking check ran, and failed

Asked of instance-investigator 2026-09-02, read-only on a copy. Answers:

1. Exists. sha256 `58b6b24087678c29ddb2ded510150e8252b09c93bf1804596219adfe8bfef9e9`,
   141,082,624 bytes.
2. **Repairable count: zero.** See §1.
3. Genuinely pre-migration — the `_migrations` section does not exist in `hub_settings` at
   all, versus the live DB which has it.

(2) falsified the plan's load-bearing assumption and forced the revision in §1.3. This is the
check working: it cost one query and saved provisioning an instance to discover the fixture
was empty. Keep this shape — state the assumption, make it falsifiable, test it *before*
committing resources.

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

Let `R` = the number of constructed rows in classes F-1, F-2 (those that *must* rekey), and
`M` = the messages seeded into them. After the run, all `M` must be attributed and none may
appear in `permanent`. State the counterfactual explicitly: had the ordering been wrong,
`permanent` would be exactly `M` higher and no error would have been raised.

**This check is the reason the constructed population is mandatory rather than a nicety.**
On the unmodified snapshot `R` is zero, so C-1 and C-2 are both vacuous — every key is
already new-format and the ordering cannot matter. A cutover run over the bare snapshot
would look completely clean while proving nothing about the hazard in §0.1.

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

**Before the boot**, record for every constructed row F-1 … F-7: the conversation ID, the
`external_ref`, and both principal IDs. Keys and IDs only — **never message content**.

**Assert before:** `isDMParticipant` denies for both named principals on every old-format
row. It must deny, because the key is old-format and unparseable.

**Assert after, for F-1 and F-2 only:** it grants for both named principals — and, separately:

**Assert the over-granting direction.** For each repaired row, a principal *not* named in
the key must still be denied. This is the check that matters most and the one easiest to
forget, because the natural test is "did the repair work" and a repair that grants too
broadly passes that test.

> **Under-granting is recoverable; over-granting is not.** A wrong key is worse than no key,
> since the key IS the ACL.

**Unrepairable rows must remain unrepaired — F-3, F-4, F-5.** Each must come out of the
migration byte-identical and still denied, and must be counted as `Ambiguous` rather than
`OldFormatRekeyed`. Fail-closed is the correct outcome here, not a defect to be fixed on the
spot. F-5 (both principals identical) deserves particular attention: it is the one class
where a naive "both sides resolved, proceed" check would succeed and produce a self-DM ACL.

**F-7 must come out byte-identical too.** A new-format key that the migration rewrites — even
to an equivalent value — means the rekey path is reachable from rows that do not need it,
which is the same failure as over-granting wearing a different hat.

Note the standing exception: the migration is the **one sanctioned place** where a DM key may
be rewritten. Nothing this test finds may be used to justify normalising a key anywhere on
the derivation path.

---

### 4.1 The F-classes belong in the test suite, not only on the VM

The obvious challenge to §1.3 is: if the population is constructed anyway, why does this need
a VM at all? Why is it not a Go integration test?

Largely, it is — and it should be. **F-1 … F-7 should land as permanent test coverage
regardless of whether the VM run ever happens.** They are cheap, they are deterministic, they
encode the discriminations the repair path must make, and unlike a VM exercise they keep
protecting the path after this tranche closes. If only one of the two ever gets built, build
these.

What the VM run adds on top, and cannot be had from a Go test:

- the real boot path, real binary, real systemd unit — not a test harness calling the hook
- **scale**: 141 MB, 39 projects, 12,583 reachable messages, against the 10-minute budget
- the genuine single-cutover sequence with no operator action between migrations

So: not either/or. The F-classes are the durable artifact and should be staffed first; the VM
run is the scale-and-realism check layered over them. Sequencing them that way also means a
VM run that goes wrong has a passing local suite to bisect against.

---

## 5. What this instance still cannot prove

Carried forward from `GTEAM-ACCEPTANCE.md` §4, unchanged:

- **Gap B — AC-8, Postgres concurrency.** Still the only AC with no coverage anywhere. This
  instance is SQLite and does not change that.
- **Gap C — AC-2, run-level-failure half.**
- **Gap E — AC-7 / AC-11**, code-level criteria.

**Gap D — AC-3, resumption** may be *accidentally* closed here if C-4's budget is exhausted.
If that happens, say so explicitly rather than leaving it implied.

**Gap A is closed by construction, never by observation.** Worth stating plainly so nobody
later reads the acceptance record as stronger than it is: no instance in our possession has
ever contained a repairable old-format row, so the rekey path's behaviour on *organically
produced* data has never been observed and now never will be. Every assurance we have about
it rests on rows we built to specification. That is a real residual, it is not closeable with
the assets available, and the mitigation is breadth of enumerated classes (§1.3) rather than
depth of live exercise.

---

## 6. Open questions

- **OQ-A. RESOLVED — no obstacle.** Full schema diff between the snapshot and the live DB
  running `98543da92`: **zero differences**. Same 71 tables, same index set, same column
  counts on every key table. The gap between pre-`e132380f` and `98543da92` is data-only —
  the `_migrations` marker and the rows the migration touched — not structural. Restoring
  the snapshot will not drag in unrelated schema auto-migrations, so a failure during the
  run is attributable to the messaging migrations rather than to incidental schema drift.
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

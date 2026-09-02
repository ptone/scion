# Design — Migration Auto-Run at Hub Startup

**Author**: ca-msg-arch
**Date**: 2026-09-02
**Code ref**: all line numbers against `e132380fe` (tip of `scion/tranche-g`). Do **not**
read them against `scion/ca-msg-arch`; that branch is docs-only and its code tree is 211
commits behind.
**Survey input**: `MIGRATION-SURVEY.md` (pinned at `85f25c1a1`) and its Addendum. This
design does not re-derive what the survey established; it records only where the survey
was incomplete or has since drifted.

---

## 1. Problem & Goals

ptone's binding requirement:

> when a hub is updated to a version that includes this completed refactor, migrations
> are auto-run, and all switches cut-over by default

Phase 9a delivered the switch half: `conversation_envelope_switch` defaults ON at compile
time, an empty settings doc yields ON, and a malformed doc fails closed to OFF
(`pkg/hub/admin_messaging_test.go:61-134`). The migration half is undesigned and unwired.

**Goals**

1. On upgrade, the data migrations required by the conversation model run without
   operator action.
2. **No new operator-visible switch.** This is a binding directive, not a preference.
3. Idempotent, interruptible, and safe to run concurrently on multiple replicas.
4. Boot must not become unboundedly slow, and a failing migration must never render the
   hub unbootable.
5. The hub must not claim a migration is complete when it is not.

**Success criteria**

- A hub upgraded from a pre-cutover version reaches a state where `ParseDMKey` succeeds
  on every live `direct` conversation that has a non-empty `external_ref`, with no
  operator command run.
- Boot time on an already-migrated hub is O(1) with respect to message and conversation
  count.
- No code path writes a completion marker for a run that logged per-row errors.

## 2. Non-Goals

- **Rewriting either migration.** Both are documented idempotent and carry explicit
  safety guards (B2 merge-abort, B1/D-1 participant-copy routing). This design wires
  them; it does not touch their internals.
- **Fixing DEF-104 in `webchat_migrations`.** Out of scope, but this design must not
  re-cut the same shape — see §4.2.
- **`MigrateGroveToProjectData`** (`pkg/ent/entc/migrate_grove_to_project.go:43`), the
  fourth unwired migration. Possibly obsolete; separate decision.
- **The keyless `direct` row (DEF-29).** The B14 ruling stands: empty-ref rows are left
  untouched, because deriving a key from the participant index would fabricate an ACL
  from the listing index. Auto-run does not change that.
- **Backfilling messages that no project owns.** See §4.6 — this design *reports* that
  gap rather than closing it.

---

## 3. Findings That Shape The Design

These are new or sharpened relative to the survey. Each is load-bearing.

### F1 — The two migrations differ in blast radius, and only one is a cutover concern

`isDMParticipant` (`pkg/hub/handlers_chat_v2.go:3250`) splits the key and returns false
on fewer than 5 segments. An old-format key `dm:<uuidA>:<uuidB>` splits to 3.
`ParseDMKey` (`pkg/messages/dm_key.go:138`) likewise requires 4 segments after the `dm:`
prefix and errors otherwise.

So an un-migrated old-format direct conversation **denies access to its own
participants**. This is correct fail-closed behaviour and must not be softened — the
standing prohibition on making `isDMParticipant` tolerant of non-conforming keys is
unchanged.

The consequence for this design is the important part: **that denial exists today, on
`main`, with the envelope switch in any position.** The DM migration is a *repair of a
live access defect*, not a step in the envelope cutover. Its urgency is independent of
the switch, and coupling it to the switch would be wrong.

By contrast, `BackfillService` supplies *attribution* for historical messages. Missing
attribution degrades history views; it does not deny access.

### F2 — `DMMigrationService` has zero production callers

`NewDMMigrationService` is referenced only by its own definition
(`pkg/messaging/dm_migration.go:82`) and tests. It is wired to no boot path **and to no
CLI command** — unlike `BackfillService`, which at least has `scion server backfill`.
There is currently no supported way to run it at all.

This is stronger than the survey's "no boot-path constructor," and it means the repair
in F1 is not merely un-automated; it is unavailable.

### F3 — Boot ordering makes the interlock a sequencing property, not a switch

`maybeWarnUnbackfilledMessages` is called at `cmd/server_foreground.go:1218`, inside
store construction, after `migrateStore` and before `Ping` — long before the HTTP
listener opens. Work placed at that point completes before the first request is served.

This is why **no third switch is needed**: if the migration runs synchronously there,
"migrations done" and "switch on" are not two independently observable states, so there
is no window to gate. The interlock is sequencing. Any design that runs the migration
*after* the listener opens reintroduces the window and would need a gate — which is
exactly the switch proliferation the directive forbids. This is the single strongest
argument for the synchronous placement in §4.1.

### F4 — `Run` returns `nil` while accumulating per-row failures

Both services collect non-fatal failures in `result.Errors []string` and return
`(result, nil)`. Only a collection-level failure produces a non-nil error. A marker
written on `err == nil` would record success for a run in which every row failed.

### F5 — The advisory lock protects nothing in the configuration we test most

`runWithAdvisoryLock` (`cmd/server_foreground.go:1280`) calls `fn()` directly when the
store does not implement `AdvisoryLocker`. SQLite takes that path. Correct for a
single-process hub, but it means the marker write must be conflict-safe **on its own
merits**, never merely lock-protected.

### F6 — `BackfillService` is per-project and requires a project ID

`Run` returns an error when `cfg.ProjectID == ""` (`pkg/messaging/backfill.go:99`) and
filters with `store.MessageFilter{ProjectID: cfg.ProjectID}`. The CLI therefore
enumerates projects via `ListProjects` and loops (`cmd/server_backfill.go:96-133`).

Auto-run inherits this shape: it is not one run, it is N runs. It also inherits the
coverage gap in F7.

### F7 — The boot warning and its advertised remedy have different denominators (DEF-111)

`CountUnbackfilledMessages(ctx, "")` counts **hub-wide**: every message with a NULL
`conversation_id`, regardless of project
(`pkg/store/entadapter/message_store.go:511-525`). The remedy it advertises —
`scion server backfill --execute` — only processes messages belonging to projects
returned by `ListProjects`.

Any unattributed message with no project, or in a project not listed, is counted by the
warning and unreachable by the remedy. The operator runs the advertised command, it
reports success, and the warning returns unchanged on the next boot, indefinitely.

**This is live, and it is 23% of the backlog.** On gteam: every unattributed message has
a `project_id`, but **5,637 of the 24,700 point at a `project_id` with no row in
`projects`**. The `projects` table has no `deleted_at` column — it hard-deletes — so
these are permanent orphans, not a soft-delete artefact that could be recovered by
widening the listing filter.

The consequence is concrete: after a *fully successful* backfill of every project on
gteam, the boot warning will still read ≥5,637 and will do so on every boot forever.

Filed as **DEF-111**. It is pre-existing, not introduced here, but auto-run would inherit
it and convert a manual annoyance into a permanent boot-log falsehood — so this design
must address the reporting even though it cannot close the gap.

### F8 — Two different predicates express "unattributed" (DEF-112)

The counter tests `conversation_id IS NULL`. The backfill skip-guard tests
`msg.ConversationID != ""` (`pkg/messaging/backfill.go:135`). These agree only if no row
ever holds the empty string.

Measured on gteam: 0 rows hold `''`, 24,700 hold NULL. **The two predicates agree today**,
so DEF-112 is **latent, not live** — filed for the record and explicitly not prioritised.
It remains a defect because nothing *enforces* the agreement: a single future write path
that stores `''` instead of NULL would silently desynchronise the counter from the
migration, and the symptom would be a warning count that never reaches zero — which,
thanks to DEF-111, is now indistinguishable from expected behaviour. That interaction is
the reason to keep it on the books.

### F9 — Live sizing (scion-gteam, `e132380f`, read-only census)

| Measure | Count |
|---|---|
| direct conversations, new-format key | 6 |
| direct conversations, **old-format key (live)** | **1** (`f003ad87`) |
| direct conversations, empty key (DEF-29, live) | 1 (`adf13f87`) |
| direct / group / total conversations | 8 / 42 / 50 |
| messages **unattributed** | **24,700** |
| — of those, reachable by per-project backfill | 19,063 |
| — of those, **orphaned (project hard-deleted, unreachable)** | **5,637** |
| messages with `conversation_id = ''` | 0 |
| messages total | 24,720 |

Three things follow. The DM migration is trivially cheap at real scale (tens of rows, not
thousands) — which settles §4.4 in favour of running it unbudgeted. **99.9% of message
history on the live QA instance is unattributed**, so the backfill has effectively never
run anywhere. And **23% of the backlog is permanently unreachable**, which is what turns
§4.6 from tidiness into a requirement.

### F10 — A B14-enforcing no-op is named after the behaviour B14 forbids (DEF-113)

`stepMergeOrRekeyEmptyRef` (`pkg/messaging/dm_migration.go:108`) takes a context, a
conversation and a dry-run flag, **discards all three**, and increments
`EmptyRefSkipped`. Its comment correctly states the B14 ruling. The behaviour is right.

The name is not, and neither is the result struct: `EmptyRefMerged` and `EmptyRefRekeyed`
are declared, documented as "step 3a" outcomes, and **can never be non-zero**. A reader
of `DMMigrationResult` — including anyone reviewing an auto-run report — would reasonably
conclude that empty-ref rows are sometimes merged or re-keyed.

This is filed as **DEF-113** and treated as security-relevant rather than cosmetic,
because the failure mode is a future editor "implementing the missing behaviour" that the
name and the counters both advertise. Doing so would fabricate an ACL from the participant
index, which is precisely the inversion of authority B14 exists to prevent. The comment is
the only thing holding the line, and comments do not survive refactors as reliably as
names do.

Not a blocker for auto-run. It becomes more urgent *because* of auto-run: the counters
move from a CLI report a human reads once to a boot-log line emitted forever.

---

## 4. Proposed Design

### 4.1 One hook, synchronous, at the existing warning's call site

Replace the call at `cmd/server_foreground.go:1218`:

```go
    maybeWarnUnbackfilledMessages(ctx, s)      // remove
    runBootDataMigrations(ctx, s)              // add
```

Same preconditions (schema migrated, store open, nothing serving yet), and it directly
obsoletes the warning per Addendum A5.

```go
// runBootDataMigrations runs the conversation-model data migrations that an
// upgrading hub needs. It is called once during store setup, before the hub
// serves any request, so a completed run is observable to every later reader
// without a gate.
//
// It never returns an error: a failed data migration degrades history or leaves
// a repair pending, neither of which justifies refusing to boot. Failures are
// logged at ERROR and the completion marker is left unwritten so the next boot
// retries.
func runBootDataMigrations(ctx context.Context, s store.Store) {
    runWithAdvisoryLock(ctx, s, store.LockDataMigrations, "conversation data migrations", func() {
        runDMKeyMigration(ctx, s)      // §4.4 — repair, unbudgeted
        runMessageBackfill(ctx, s)     // §4.5 — attribution, budgeted
        reportResidualUnattributed(ctx, s)  // §4.6 — honest reporting
    })
}
```

DM migration runs first. Survey §3 establishes the two are order-independent, but
DM-first produces fewer duplicate conversation rows, since re-keying an old-format row
before the backfill upserts by external_ref avoids creating a second row that a later
merge has to reconcile.

### 4.2 Marker storage: `hub_settings`, not `webchat_migrations`

A new `_migrations` section written through `UpsertHubSetting`, following the `_meta`
sentinel precedent at `cmd/server_foreground.go:2078` (Addendum A6).

Rationale, in priority order:

1. **Conflict-safe by construction.** It is an upsert. Given F5, this is the property
   that actually carries the concurrency argument, not the lock.
2. **No dialect split.** `webchat_migrations` is hand-written twice with no shared
   interface, and that duplication is the direct cause of DEF-104. Addendum A3's
   conclusion — *do not add a third hand-written pair* — is binding here.
3. **No schema change**, so this lands without an Ent migration.

The section is `_`-prefixed to mark it internal, matching `_meta`.

> **Note for reviewers and operators.** This puts migration state in the same table as
> settings documents that may contain credentials. The standing rule — never dump the
> `value` column of `hub_settings`, work from section names — is unaffected and still
> applies. Our section contains no secrets, but the rule is about the table, not the row.

Document shape:

```json
{
  "dm_key_migration":   { "completed_at": "2026-09-02T05:00:00Z" },
  "message_backfill":   { "completed_at": null,
                          "projects_done": ["<uuid>", "<uuid>"] }
}
```

### 4.3 INVARIANT M-1 — what a marker means

> **M-1.** A completion marker records that a run finished **with zero per-row errors**,
> not that a run returned.

Concretely, given F4:

```go
res, err := svc.Run(ctx, cfg)
if err != nil || len(res.Errors) > 0 {
    slog.Error("...migration incomplete; will retry next boot",
        "error", err, "row_errors", len(res.Errors), /* bounded sample */)
    return  // marker NOT written
}
markComplete(ctx, s, "...")
```

This is the §5lg failure posture, unchanged: log at ERROR, leave the marker unwritten so
the next boot retries, do **not** refuse to boot.

The error sample logged must be bounded (first N, with the total count). `res.Errors` is
unbounded and can contain one entry per row; logging it whole turns a bad migration into
a disk-space incident.

### 4.4 DM key migration — synchronous, unbudgeted

Scope is the count of `direct` conversations. F9 measures 8 on the live instance; even a
large hub is in the thousands. The per-row cost is up to 4 lookups for old-format rows,
and only old-format rows pay it.

No budget, no checkpoint. Run it to completion or record failure.

This also closes F2 by giving the service its first production caller. A CLI equivalent
(`scion server migrate-dm-keys`, dry-run by default, mirroring `scion server backfill`)
should land in the same tranche so the operation is diagnosable by hand — a migration
that can only be triggered by rebooting is not operable.

### 4.5 Message backfill — synchronous, budgeted, per-project markers

Enumerate projects exactly as the CLI does, then per project:

- Skip projects already in `projects_done`.
- Run the backfill for that project.
- On a clean result (M-1), append the project to `projects_done` and persist.
- Check the remaining time budget. If exhausted, stop and return; the next boot resumes
  at the first project not yet in `projects_done`.

When every enumerated project is in `projects_done`, set `completed_at` and clear
`projects_done` (bounded growth), after which subsequent boots do a single marker read.

**Why per-project markers rather than the cursor.** `BackfillConfig.Checkpoint` and
`result.LastCheckpoint` do support mid-scan resumption, but the CLI itself notes the
checkpoint is only meaningful for single-project runs. Per-project granularity gives
monotonic cross-boot progress without persisting cursor state whose validity depends on
list ordering being stable across boots. The cost is that one project is the atom of
progress.

**The convergence risk this leaves.** If a single project's backfill exceeds the whole
budget, it is retried from the start on every boot and never completes. This is bounded
and diagnosable rather than silent: the budget check logs at ERROR naming the specific
project that did not finish, and the operator can run
`scion server backfill --execute --project <id>` without a time limit. Accepting this is
a deliberate trade — the alternative is persisting cursors, which is more state and more
ways to resume incorrectly.

The budget default must be **measured, not guessed** — see §7 OQ-1.

### 4.6 Re-point the warning instead of deleting it (DEF-111)

Auto-run makes *"run `scion server backfill --execute`"* stale advice. It does not make
the underlying count meaningless, because of F7: the backfill cannot reach every message
the counter counts.

On gteam this residue is 5,637 messages and it is permanent. A warning that fires on
every boot, forever, with a number that no action can reduce, is alarm fatigue — and it
trains operators to ignore the one line that would tell them a *real* backfill failure
had occurred.

So the report must separate the two populations, because they mean different things:

```
INFO  Message attribution complete
      attributed=<n>
      unreachable=5637
      detail="unreachable messages reference hard-deleted projects and cannot be
               attributed by per-project backfill (DEF-111); this count is expected
               to be stable"
```

and only warn when the *reachable* count is non-zero, since that is the only number a
retry can move:

```
WARN  Messages remain unattributed in listed projects
      count=<n>   // reachable, and therefore actionable
```

This requires a reachable-only count, which `CountUnbackfilledMessages` cannot express
today — it takes a single project ID or nothing. Two options: sum the per-project counts
the backfill already computes (no new store surface, and it is the number the migration
actually acted on), or add an anti-join count. **Prefer the former** — it derives the
number from the work performed rather than from a second query that could drift from it,
which is the same reasoning that produced DEF-112.

Deleting the warning would hide a real anomaly; leaving it unchanged would advertise a
remedy that cannot work and bury a genuine signal under a permanent one. Splitting the
buckets is the only honest option.

Whether the 5,637 orphans should be purged rather than reported indefinitely is a
separate question, and not one this design answers: deleting message rows is
irreversible and belongs to ptone. Noted in §7 OQ-6.

### 4.7 Advisory lock key

```go
LockDataMigrations AdvisoryLockKey = 0x5C100014   // next free
```

Verified against `e132380fe`: `pkg/store/concurrency.go` allocates exactly 19 keys,
`0x5C100001`–`0x5C100013`, so `0x5C100014` is next. The block is **not declared in
numeric order** (`0x...0A` at line 65 precedes `0x...09` at line 69), so the next key
must be chosen by scanning the file, not by reading the last declaration.

Add a test asserting the key set is unique and contiguous. Nothing today would catch a
duplicate constant, and a duplicate would silently make two unrelated migrations
mutually exclusive — a failure that would present as "the migration sometimes doesn't
run" and would be extremely hard to diagnose.

---

## 5. Alternatives Considered

### A1 — Run the migrations asynchronously after the listener opens

**Rejected.** Fast boot, but it reintroduces the window F3 eliminates: the switch is ON
while data is un-migrated, and requests are being served. Closing that window requires a
gate on migration state, and a gate on migration state is a third switch in everything
but name — the more so because it would be invisible to the operator, making behaviour
depend on a hidden marker. Directly contrary to the binding directive.

Worth stating plainly: this is the *only* alternative that meaningfully improves boot
latency, and it is rejected on correctness rather than cost.

### A2 — Block boot on failure (`return err` from the migration hook)

**Rejected.** Symmetry with `maybeMigrateLegacySQLite`, which does block, is superficially
attractive — but that one blocks because the database is *unopenable* otherwise. Here the
hub is fully functional with un-migrated data; it merely has degraded history and one
un-repaired ACL. Refusing to boot converts a partial degradation into a total outage, and
does so at exactly the moment an operator is upgrading. §5lg already ruled this.

### A3 — Put markers in `webchat_migrations`

**Rejected.** It is the only existing once-only mechanism, so it has the pull of
precedent, but it is check-then-act, hand-written per dialect, and its marker INSERT
lacks an `ON CONFLICT` clause — the direct cause of DEF-104, where a *duplicate record of
successful work* aborts store init and silently disables web chat. Adding a third
hand-written pair multiplies the surface on which that recurs. Also semantically wrong:
these are not webchat migrations.

### A4 — A dedicated `data_migrations` Ent table

**Rejected as over-build, but it is the right answer eventually.** Cleanest semantics and
a natural home if markers proliferate. Costs an Ent schema migration and a new store
surface for no behavioural gain over §4.2 today. Revisit when there is a third marker;
the `hub_settings` document can be migrated into it without changing any caller.

### A5 — Fold the migrations into `migrateStore` / `Migrate()`

**Rejected.** It would inherit `LockSchemaMigration` for free, avoiding a new key. But it
conflates schema migration with data migration: schema failures *must* block boot, data
failures must not (A2). Sharing the call site makes it easy for a later change to give
one the other's failure semantics.

### A6 — Do nothing automatic; document the CLI in the upgrade notes

**Rejected** — it is the status quo and fails the binding requirement outright. Recorded
because it is the honest baseline: F9 shows the manual path has produced 99.9%
unattributed messages on the one instance anybody actively uses, which is the empirical
case against relying on operator action.

---

## 6. Migration / Rollout

This lands as ordinary code; there is no data change at deploy time beyond what the
migrations themselves do on first boot.

1. **Pre-flight measurement** on gteam before choosing the budget: run
   `scion server backfill` (dry-run is the default — verify that before running) and time
   it. 24,700 messages across 39 projects is the real shape. Also time a dry-run DM
   migration once the CLI from §4.4 exists.
2. **First boot after upgrade** runs both migrations. Expect a slower boot proportional
   to data volume, once.
3. **Subsequent boots** read one settings document and do nothing.
4. **Rollback**: the previous binary simply does not run the hook. Re-keyed DM rows and
   stamped messages are forward-compatible — the old code reads a kind-encoded key fine,
   since that is the format it already prefers. There is no down-migration and none is
   needed.
5. **Beta hub**: per the standing instruction this is a scheduled exercise with a DB
   snapshot taken first, and the first boot is the interesting moment. Capture the boot
   log; it is the only place the migration reports itself.

---

## 7. Open Questions

**OQ-1 (blocking the implementer, resolvable without ptone).** What should the backfill
time budget default be? Must come from the §6.1 measurement, not from a guess. If a full
gteam backfill takes single-digit seconds, the budget is close to irrelevant and could
arguably be dropped; if it takes minutes, the budget and its convergence caveat (§4.5)
become load-bearing.

**OQ-2 (needs ptone).** Re-pointing `maybeWarnUnbackfilledMessages` (§4.6). My standing
threshold says I block on removing or re-pointing an existing gate. This is a warning
that gates nothing, so I read it as inside my authority and intend to proceed — flagging
it because it is near the line, not because I need it resolved to continue.

**OQ-3.** Should `--no-auto-migrate` also skip data migrations? It currently means
"do not upgrade a legacy raw-SQL schema." Overloading it is a silent semantic change to
an existing flag; adding a new flag is the switch proliferation we are forbidden. Current
proposal: **neither** — no opt-out. Defensible because the migrations are idempotent,
resumable, budgeted, and non-fatal. Revisit if an operator ever needs to boot without
them.

**OQ-4 — RESOLVED.** DEF-112 is latent, not live: gteam holds 0 rows with
`conversation_id = ''` against 24,700 NULL. The predicate fix (phase M7) is therefore
optional hardening rather than a bug fix, and M7 may be dropped if the tranche is running
long.

**OQ-6 (needs ptone, not blocking).** 5,637 messages reference hard-deleted projects and
can never be attributed. They will be reported on every boot in perpetuity. The
alternative is to purge them, which is irreversible and therefore explicitly not mine to
decide. No action is required for this design to land — §4.6 reports them stably — but
the question should not sit unasked, because "permanent expected warning" is a state that
degrades over time as people stop reading it.

**OQ-5 — narrowed after checking the code.** The two preserved staging rows are affected
differently, and the distinction matters:

- `adf13f87` (keyless, the DEF-29 reproduction) is **not touched**. Verified in the code,
  not taken from the survey: `stepMergeOrRekeyEmptyRef` discards all its parameters and
  does nothing but increment `EmptyRefSkipped`, enforcing B14. The DEF-29 reproduction
  survives auto-run intact.
- `f003ad87` (old-format key) **is** re-keyed. That is the correct repair — the row
  currently denies its own participants — but it consumes the only live old-format
  reproduction, which is a *different* artefact from the DEF-29 one.

So the question is much smaller than first written: capture `f003ad87`'s current
`external_ref` in the defect record before the migration first runs anywhere, and the
loss is bounded to a live reproduction of a defect we are deliberately fixing. Recording
it rather than acting silently, because it is a row under an explicit preservation order.

---

## 8. Implementation Phases

Commit-sized, in order. Each is independently reviewable.

- **M1 — Lock key + uniqueness test.** Add `LockDataMigrations = 0x5C100014` and a test
  asserting the key set is unique. No behaviour change. Smallest possible first commit
  and it de-risks the constant choice.
- **M2 — Marker helpers.** Read/write the `_migrations` section of `hub_settings` via
  `UpsertHubSetting`. Unit tests: absent doc, malformed doc (must not enable anything —
  treat as "not complete", i.e. retry, which is the safe direction here), concurrent
  double-write.
- **M3 — DM migration CLI.** `scion server migrate-dm-keys`, dry-run by default,
  mirroring `scion server backfill`. Gives F2 its first production caller and makes M4
  testable by hand.
- **M4 — Boot hook, DM migration only.** `runBootDataMigrations` under the lock, with
  M-1 marker semantics. Replace the `:1218` call site. Assert the warning still fires.
- **M5 — Backfill in the boot hook.** Per-project markers, budget, resumption.
- **M6 — Split the residual report** (§4.6) into reachable (WARN, actionable) and
  unreachable (INFO, stable), and delete the stale remediation string. Grep for prose
  describing the old behaviour — docs-site and SKILL.md — per the standing rule that
  removing a gate requires removing the text that describes it.
- **M7 — DEF-112 hardening** (optional). Make the counter and the skip guard share one
  predicate. OQ-4 resolved this as latent, so M7 is droppable if the tranche runs long;
  it is listed last for that reason.
- **M8 — DEF-113 rename** (§3 F10). Rename `stepMergeOrRekeyEmptyRef` to
  `stepSkipEmptyRef`, delete the unreachable `EmptyRefMerged` and `EmptyRefRekeyed`
  counters, and keep the B14 comment. Pure rename plus dead-field deletion, no behaviour
  change. **Not droppable** — unlike M7 this guards a security invariant against a
  plausible future edit, and it gets cheaper the sooner it lands.

M1–M2 can proceed immediately. M5 is blocked on OQ-1's measurement. M6 depends on M5 for
the reachable count it reports.

---

## 9. Acceptance Criteria

A reviewer or QA agent should verify:

1. **Idempotence.** Boot twice against a migrated database. The second boot performs no
   conversation or message writes. Assert on query counts or write counts, not on wall
   time.
2. **M-1 is enforced.** With a store stubbed to make one row fail, the run completes, an
   ERROR is logged, and **the marker is absent**. The next boot retries. This is the
   single most important test in the set: it is the one that fails if someone writes the
   marker on `err == nil`.
3. **Resumption.** With the budget set to expire mid-list, boot repeatedly and assert
   `projects_done` grows monotonically and the global marker is set only when every
   enumerated project is present.
4. **The repair works.** Seed an old-format `dm:<uuidA>:<uuidB>` row whose participants
   are a real user and agent. Before: `isDMParticipant` denies. After one boot: the key
   is kind-encoded and access is granted. Assert against `isDMParticipant`, not against
   the stored string, so the test measures the property that matters.
5. **Fail-closed is preserved.** A key that cannot be resolved to kinds is left
   unmodified and still denies. Re-keying must never be best-effort: the standing rule is
   parse failure denies, always, with no fallback anywhere on the derivation path. Assert
   no row acquires a key that does not name its actual principals.
6. **Boot is never blocked.** With the migration forced to fail outright, the hub still
   reaches a serving state and `/healthz` reports healthy.
7. **No new operator switch.** `git diff` adds no flag, no settings field, and no
   admin-API field that controls whether the migrations run.
8. **Concurrency.** On Postgres, two hubs booting simultaneously produce one run's worth
   of writes and no marker-write error. Note F5: this is untestable on SQLite by
   construction, so it must be an explicitly declared gap rather than assumed covered by
   the default suite.
9. **The residual report is honest** (§4.6). Seed one unattributed message in a listed
   project and one referencing a project ID with no row. After the boot hook runs: the
   reachable one is attributed; the orphan is reported as *unreachable* at INFO, not as a
   WARN; the WARN for reachable messages does not fire; and no log line advertises
   `scion server backfill --execute`. The specific failure to test for is the orphan
   being counted in the actionable bucket — that is the bug that makes the warning
   permanent.
10. **B14 still holds after auto-run.** Seed a keyless `direct` row. After the boot hook
    runs, the row is unchanged — still keyless, no participants invented from the listing
    index — and is reported as skipped. This is the assertion that would catch someone
    implementing the behaviour DEF-113's stale name advertises.
11. **Build tags.** Any new test needing sqlite carries `//go:build !no_sqlite`, and the
    author confirms whether the blocking `make test-fast` gate can see the new tests. Per
    DEF-94's per-change form, a green gate that compiles the new tests out says nothing.

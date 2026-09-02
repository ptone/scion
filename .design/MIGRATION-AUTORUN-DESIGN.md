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
- No code path writes a completion marker for a pass that did not finish (M-1′, §4.3).
- The hub can state, per message, **why** a message was not attributed. Measured reality
  (F11) is that a fully successful backfill attributes 26% of history; the goal is
  therefore accurate accounting of the remainder, **not** 100% attribution. Attributing a
  message whose key cannot be derived would mean inventing an ACL.

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


### F11 — Measured: a fully successful backfill attributes 26% of history

Timed on a throwaway copy of the gteam pre-deploy snapshot, dry-run and execute, all 39
projects. **Both runs exited non-zero.**

| Run | Wall clock | Processed | Attributed | Refused |
|---|---|---|---|---|
| dry-run | ~20s | 19,082 | 6,476 | 11,593 |
| execute | ~37s | 19,082 | 6,476 | 11,597 |

Reconciled against the 24,700 unattributed messages:

| Outcome | Count | Share |
|---|---|---|
| attributed (incl. 4 inferred) | 6,480 | **26.2%** |
| refused — key derivation failed | 11,593 | 46.9% |
| unreachable — project hard-deleted (DEF-111) | 5,618 | 22.7% |
| skipped — broadcast or already attributed | 1,009 | 4.1% |

**Timing is a non-issue and was the least important thing the run discovered.** 37
seconds is comfortably inside any boot budget, which largely settles OQ-1. The finding
that matters is that a migration which completes successfully still leaves **18,220
messages unattributed**.

Refusal is the *correct* behaviour — `DeriveConversationKey` documents that a guess on
any input to key derivation is a guess on the ACL, and refusing beats inventing one. But
it means "run the migrations and history is attributed" is not a true statement about
this system, and the design must not imply otherwise.

Two further observations from the same runs:

- **Dry-run over-reports.** 733 conversations projected vs 728 actually created. Dry-run
  does not evaluate write-time constraints, so its projection cannot be used as an
  expected value for verifying an execute run.
- **Four execute-only errors are the D-1 guard firing** — see F13.

#### F11a — Re-measured after DEF-114 landed: the ceiling is legacy data, not a bug

Second run, `36b5eda51`, `--execute` against a throwaway copy. The classification the
first run could not produce:

| Cause | Count | Share of refusals |
|---|---|---|
| `principal_pair` | **11,592** | 99.96% |
| `dm_key_parse` | 1 | 0.01% |
| `dm_key_not_canonical` | 0 | — |
| `thread_no_project` | 0 | — |
| `unclassified` | 0 | — |
| *(write-time, outside the breakdown)* | 4 | 0.03% |

`principal_pair` is not a derivation defect. The principals are slugs, display names and
email addresses — `agent:poet-red`, `user:ptone@google.com`,
`users/102876876769796327221` — against a length histogram with 31 distinct values, **none
of which is 36**. There is no slug-to-UUID resolution on the derivation path, and adding
one would be an inference about principal identity, which is an inference about the ACL.

**This closes the scope question.** The 26% ceiling in F11 is real and permanent under the
current model. It does not dead-end the end state: new messages attribute correctly, and
the ceiling applies only to historical backfill.

Two things the re-measurement also established, both recorded as defects rather than
findings because they are faults in the tool and not in the data:

- The arithmetic does **not** balance — `sum(DeriveFailures)` is 11,593 against 11,597
  errors. The four extra are post-derivation *write* failures and belong to no derivation
  bucket. The invariant I asked for (`sum == len(Errors)`) was wrong when I specified it;
  the honest relation is `sum(DeriveFailures) + writeFailures == len(Errors)`.
- The breakdown above had to be reconstructed by string-parsing the error list, because
  `DeriveFailures` is merged and printed nowhere (DEF-119), and the hazard-A figure of 4
  quoted in F11 is `r.Inferred` under a mislabelled heading, not `HazardAEmailCount`,
  which has never been displayed at all (DEF-120).

### F12 — The dominant failure mode is undiagnosable by construction (DEF-114)

`DeriveConversationKey` distinguishes four causes: a `dm:` ThreadID that fails
`ParseDMKey`; a `dm:` ThreadID that parses but is non-canonical; a thread key with no
project; and failure to derive from the principal pair. Each returns a distinct wrapped
error.

All four are then thrown away, twice:

```go
// groupForMessage (pkg/messaging/backfill.go)
if deriveErr != nil {
    return nil          // deriveErr discarded
}

// Run (pkg/messaging/backfill.go:140)
if g == nil {
    result.Errors = append(result.Errors,
        fmt.Sprintf("message %s: key derivation failed", msg.ID))   // cause gone
}
```

The result is 11,593 identical strings that say nothing. The single largest population in
the migration cannot be classified, so it cannot be decided about.

A related casualty: `hazardA` is set **after** a successful derive, but non-UUID
principals are precisely what makes case 3 fail. The hazard-A counter therefore
structurally cannot count the population it was written to measure — it reported 4
against a suspected legacy-principal population three orders of magnitude larger.

Filed as **DEF-114**. Fixing it is small and it gates every other decision here.

### F13 — Key comes from ThreadID, participants come from the message (DEF-115)

`groupForMessage` derives the conversation key from `ThreadID` when one is present
(cases 1 and 2), but always collects participants from the message's own sender and
recipient. **Nothing checks that those principals are named in that key.**

The execute run surfaced this as four rejections of the form:

```
adding participant user:7581ea89-… to 93ef83f1-…:
  invalid input: participant (user, 7581ea89-…) not named in direct conversation key
```

That is `CheckDMParticipantKey` — the B1/D-1 guard — refusing to write a participant into
a DM whose key does not name them. **The guard worked**, and the outcome is an
under-grant, which is the recoverable direction. But the backfill *attempted* an
ACL-widening write, and it did so on real data.

The asymmetry that makes this worth filing: the guard is scoped to `direct` conversations.
A `thread:`-keyed **group** conversation takes the same code path with no equivalent
check, so a mismatched participant is written without objection. For group conversations
the participant table is a listing index rather than the ACL, which is why this is a
defect rather than a breach — but it is the same unchecked inference, and it is only the
conversation kind that decides whether anything catches it.

Filed as **DEF-115**.

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

#### INVARIANT M-2 — the marker document is shared ground

> **M-2.** A writer of `_migrations` may modify only the keys it owns. Keys it does not
> recognise must survive the write byte-for-byte.

Added 2026-09-02 after DEF-117. The natural implementation — unmarshal into a struct,
set a field, re-marshal — silently deletes every key the struct has no field for. That is
harmless while there are exactly two migrations and one binary version in the world, and
it stops being harmless the moment either of those changes.

The concrete failure is a **downgrade**, which is a supported operation for us and not a
hypothetical one: `scion.known-good-85f25c1a` and `scion.known-good-e132380f` are staged
on the gteam VM specifically so the beta cutover can be rolled back under supervision. An
older binary that boots against a newer document, marks one migration complete, and
thereby erases a third migration's marker leaves no error and no log line — and the
operator's evidence that the third migration ever ran is gone. The migration then re-runs
against data it has already processed.

So: decode into `map[string]json.RawMessage`, touch only owned keys, write the remainder
back untouched. The cost is a few lines now; the alternative is a data-loss path that only
appears under the exact conditions we have deliberately arranged to exercise.

This is why §4.2's "no schema change" advantage carries a matching obligation. A shared
settings document is cheaper than a table precisely because anyone may add a key to it,
and that is the same reason a careless writer can destroy one.

### 4.3 INVARIANT M-1 — what a marker means

> **M-1 (SUPERSEDED — see M-1′).** A completion marker records that a run finished with
> zero per-row errors, not that a run returned.

**M-1 as first written is wrong, and the measurement in F11 disproves it.** On real data
the backfill produces 11,593 per-row refusals. Under M-1 the marker would never be
written, so the migration would re-run on every boot forever — a permanent 37-second boot
penalty that makes no progress, because the refusals are deterministic and retrying
cannot change them. An invariant that can never be satisfied is not a safety property; it
is a livelock.

The error it makes is conflating two different things:

- **Run-level failure** — could not list projects, could not write, context cancelled.
  The pass did not happen. Retrying may well succeed.
- **Row-level refusal** — this message's key cannot be derived from its own contents.
  The pass *did* happen and reached a terminal answer for that row. Retrying is futile.

A row-level refusal is not incomplete work. It is the same category as DEF-111's
unreachable messages: a permanent, correct, reportable outcome.

> **M-1′.** A completion marker records that a **full pass completed without a run-level
> failure**. Row-level refusals do not block the marker; they are counted, persisted
> alongside it, and reported. A marker must never be written for a pass that did not
> finish.

```go
res, err := svc.Run(ctx, cfg)
if err != nil {                       // run-level: pass did not complete
    slog.Error("...migration did not complete; will retry next boot", "error", err)
    return                            // marker NOT written
}
// Pass completed. Row refusals are an outcome, not an interruption.
markComplete(ctx, s, key, refusals(res))
```

The error sample logged must be bounded (first N, with the total count). `res.Errors` is
unbounded and holds one entry per refused row — 11,593 of them on gteam today. Logging it
whole turns a bad migration into a disk-space incident.

**This is blocked on DEF-114 and must not be implemented before it.** Distinguishing
run-level from row-level requires knowing *why* a row was refused, and the code currently
discards that (F12). Until the refusal reasons are visible, "these refusals are
deterministic" is an assumption, and building the marker policy on an assumption is how a
migration silently marks itself complete over data it should have retried. Sequencing is
in §8: M0 before M5.

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
- On a completed pass (M-1′ — run-level success; row refusals do **not** disqualify),
  append the project to `projects_done` and persist.
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
actually acted on), or add an anti-join count.

> **CORRECTION (post-M5, 2026-09-02).** This section originally read *"prefer the
> former"*, on the DEF-112 reasoning that deriving the number from work performed beats a
> second query that can drift from it. **M5 invalidated that preference.** M5 added
> per-project resumption: `runMessageBackfill` skips any project already listed in
> `projects_done`. On a steady-state boot — every project done, zero work performed —
> there are no per-project counts to sum, so the sum yields zero, the WARN never fires,
> and the report is silently wrong in the state the hub occupies almost all of its life.
> Note that a first-boot test cannot see this: the first boot is the one run where the
> sums do exist.
>
> **Binding requirement:** on a steady-state boot with zero backfill work performed, both
> the reachable and the unreachable counts must still be correct, and this case must be
> tested explicitly. The sum approach cannot satisfy it alone.
>
> If an anti-join count is added to satisfy this, **the DEF-112 drift concern becomes
> live and M7 is promoted from optional to required**: the counter and the backfill's
> skip predicate must then share one predicate rather than being two expressions of the
> same intent that are free to diverge.

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

### 4.8 A third bucket: permanently underivable in a listed project (M9)

**Added post-M7, after computing what the residual report will actually print on gteam.
Approved by ptone.**

M6 split the residual report in two: *unreachable* (the message's `project_id` names no
row — INFO, permanent) and everything else (WARN, actionable). Applying that to gteam's
measured numbers gives a WARN of roughly **12,606**, against the 5,637 orphans M6 was
built to suppress. We removed one body of alarm noise and left one about twice its size.

The cause is that "everything else" is not one population. A message can sit unattributed
in a listed project for reasons no operator action will ever change:

| Population | Attributable by re-running the backfill? |
|---|---|
| Project row deleted (orphan) | No — M6 already splits this out |
| Key derivation refused | **No — deterministic property of the row** |
| Broadcast / intentionally skipped | **No — has no conversation by design** |
| Project never processed | Yes |
| Write or resolution failure | Yes — transient, retryable |

Only the last two are actionable. The WARN must count only those.

**The classification already exists in the code.** `BackfillResult` records errors
exclusively through `addDeriveFailure`, `addWriteFailure` and `addResolutionFailure`, so
`sum(DeriveFailures) + WriteFailures + ResolutionFailures == len(Errors)` holds by
construction rather than by discipline. Derive refusals are permanent — all four causes
(`dm_key_parse`, `dm_key_not_canonical`, `thread_no_project`, `principal_pair`) are
deterministic properties of the row. Write and resolution failures are store errors and
are transient. M9 does not need a new classification; it needs to *carry the existing one
into the report*.

#### Why the count must be persisted rather than recomputed

The obvious alternative is a third live SQL counter, matching the shape of M6's anti-join
so it nests. **Reject it.** Two of the four causes require running `ParseDMKey` and
re-deriving, which is not expressible in SQL at all, and the other two would mean
re-implementing principal-shape checking in a second language. The key IS the ACL; a
second derivation implementation that can disagree with the first is precisely the hazard
the standing rules exist to prevent. This is the same impedance mismatch that led M7 to
reject a shared predicate.

So the count is persisted at the moment the classification is known — during the pass —
and read back on later boots. `markBackfillComplete` already preserves a global residual
count across completion (it clears `projects_done` for bounded growth but keeps
`Residuals`), so the storage pattern exists and stays bounded.

**Do not reuse `Residuals`.** It is `len(result.Errors)` — derive *plus* write *plus*
resolution. Subtracting it would suppress transient failures, which are exactly the thing
the WARN should still fire on. M9 adds a separate accumulator covering derive refusals and
intentional skips only.

#### The arithmetic

```
total       = CountUnbackfilledMessages("")             // live
unreachable = CountUnreachableUnbackfilledMessages()    // live, nests under total
reachable   = total - unreachable                       // nests, cannot go negative
permanent   = marker.PermanentResidual                  // persisted, survives completion
actionable  = max(0, reachable - permanent)
```

Report: INFO for `unreachable`, INFO for `permanent`, and WARN **only when
`actionable > 0`**.

`reachable` still nests and cannot go negative. `actionable` does not nest — it subtracts a
persisted number from a live one — hence the clamp.

**The clamp is a drift guard, not a way to reach zero.** If gteam needs the clamp to print
zero, the classification is incomplete and the missing population must be named, not
absorbed. This is the acceptance bar for M9 and the reason the phase is not done when the
number merely gets smaller.

#### CORRECTION — `permanent` must be measured, not tallied

*Written after gteam's first production backfill. The first draft of this section defined
`permanent` as `marker.DeriveRefusals + marker.Skipped`, accumulated from `BackfillResult`
during the pass. **That version is wrong and the real numbers prove it.***

Boot 1 produced: 19,083 processed, 6,476 attributed, **1,010 skipped**, **11,597 row
errors**, `unreachable` 5,637, `reachable` **12,583**. The tally version gives

```
permanent  = 11,597 + 1,010 = 12,607
actionable = 12,583 - 12,607 = -24    ->  clamped to 0
```

It reaches zero **only through the clamp** — the precise outcome the paragraph above
declares unacceptable. Two independent causes:

1. `len(result.Errors)` is derive *plus* write *plus* resolution failures. The tally
   reintroduces through the accumulator exactly the over-subtraction that the "do not
   reuse `Residuals`" rule forbids.
2. `Skipped` is not a subset of the unbackfilled population. A skipped message that
   already carried a `conversation_id` was never unbackfilled, so subtracting it removes
   something the live counter never counted.

The general fault is worth stating plainly, because it is the reusable lesson: **the
formula tallied events observed during the pass and subtracted them from a live count of
rows.** Those are different populations, and nothing forces one to nest inside the other.
Non-nesting is what admits a negative, and the clamp is what hides it.

**First corrected definition — also wrong, see below.** At the end of each project's pass,
*measure* the residual rather than deriving it from error tallies:

```
PermanentResidual += CountUnbackfilledMessages(pid)   // measured, after the project completes
                   - writeFailures(pid)               // transient — must stay actionable
                   - resolutionFailures(pid)          // transient — must stay actionable
```

The measured term is drawn from the same population the live counter measures, so at steady
state the two agree by construction and `actionable` reaches zero *exactly*. Cost is one
`COUNT` per project.

#### CORRECTION 2 — the fix contained a smaller copy of the bug it fixed

*Written after the per-cause follow-up. The `- writeFailures - resolutionFailures` terms
above are wrong for the same reason the tally was.*

The 24 decomposes exactly. Non-broadcast messages in active projects = 19,083 − 990
broadcast = 18,093:

```
 6,476  attributed this run
    20  skipped — already had a conversation_id
11,597  row errors
------
18,093  ✓

Post-backfill 6,500 non-broadcast rows carry a conversation_id, but 6,476 + 20 = 6,496
   => 4 rows were counted as row errors AND carry a conversation_id
Cross-check: 11,597 − 4 = 11,593 NULL;  990 + 11,593 = 12,583 = reachable  ✓

gap 24 = 20 (skipped, has conv_id) + 4 (errored, has conv_id)
```

The 4 are almost certainly `WriteFailures`: the dry run refused 11,593 and execute refused
11,597, so four errors arise only when writing — the signature of a group that stamps some
messages and then fails validation, leaving a row both stamped and counted as an error.

**Those 4 rows have a `conversation_id`, so they are not in `CountUnbackfilledMessages`.**
Subtracting them removes something the measurement never counted, leaving
`PermanentResidual` short by 4 and a permanent WARN of exactly 4. Same fault as the tally,
one order of magnitude smaller, sitting inside the correction for it.

#### Final definition — measurements and tallies are never mixed

```
permanent  = Σ CountUnbackfilledMessages(pid)                   // measured, per project
actionable = max(0, reachable - permanent)                      // measurement − measurement
transient  = Σ (writeFailures + resolutionFailures)             // tally, persisted, never subtracted
```

Four reported lines: INFO `unreachable`; INFO `permanent`; WARN `actionable` only when
`> 0`; WARN `transient` only when `> 0`, advertised as retryable via `scion server
backfill`.

A transiently-failed row that stays unstamped falls inside `permanent` and is therefore
silent in `actionable` — correct, because `transient` already reports it, in its own units,
with the right remedy. No double count, no gap.

**This version does not depend on classifying the population correctly.** `permanent` is
measured, so whatever the 4 turn out to be, `actionable` is 0; they move only the
`transient` line. Both earlier versions required my classification to be exactly right, and
twice it was not. Reconciliation on gteam: `reachable` 12,583, `permanent` 990 + 11,593 =
12,583, `actionable` **0 exact, not clamped**, `transient` 4.

A steady-state test must assert `actionable == 0` **before** the clamp. A test that only
checks whether the WARN fired cannot distinguish a correct zero from a clamped negative —
exactly how the tally version would have shipped green.

*Known residual:* messages written concurrently during a pass are counted as permanent and
thereafter suppressed. Post-cutover writes carry a `conversation_id`, so the window is
narrow. Recorded, not engineered around.

**Rule.** Never subtract a tally of events observed during a pass from a measurement of
rows. Name the population each side counts and prove one nests inside the other; if it does
not, a clamp converts the error into a plausible zero and every unit test agrees with it.
Where both kinds of number are needed, report them as separate lines in their own units
rather than combining them.

#### Diagnosability regression, folded into M9

The boot hook does not log the classification. `DeriveFailures`, `WriteFailures` and
`ResolutionFailures` exist on `BackfillResult`, but only the CLI print path consumes them;
the boot hook emits `processed / attributed / skipped / row_errors / elapsed` and drops the
map. DEF-114 created that breakdown precisely to make the dominant failure mode
identifiable, and the boot hook is now its primary caller — so in practice the undiagnosable
state DEF-114 closed has been reintroduced at the only call site that matters. It cost a
round trip to the instance to answer "what were the 11,597?", which no log could answer.

M9 logs the per-cause map and both non-derive counts on the per-project line and as totals
on the completion line. The counts must be persisted for `transient` regardless, so the
marginal cost is a few log fields.

#### Why the phase must reconcile against real numbers

Two of the four populations in the table above (`Skipped`, and the write/resolution split
inside row errors) were invisible in unit fixtures and only appeared against production
data. Had M9 shipped on the tally definition it would have looked correct in every test
and left a clamped negative on gteam. The phase therefore carries an explicit
reconciliation requirement against boot-1's actual numbers, not just green tests.

#### Accumulator reset

`totalResiduals` carries forward from prior boots to survive resumption. That is correct
across a *resumed* pass and wrong across a *repeated* one: a project that runs again adds
its errors a second time. Today `completed_at` makes a full repeat unreachable, so the
double-count is latent. M9 makes it reachable, because a marker written in the pre-M9
format has no `DeriveRefusals` key and the safe response is to re-run (see below). The
accumulators must therefore reset to zero at the start of a pass that begins with an empty
`projects_done`, and carry forward only within a pass.

#### Pre-M9 markers

gteam will have run the backfill under `cbd4f8b6a` before M9 lands, leaving a completed
marker with no `DeriveRefusals` key. Reading absent-as-zero would make the entire reachable
population look actionable — the bug M9 exists to fix.

Treat a completed marker lacking the new key as **not complete** and re-run the backfill
once, writing the new format. The backfill is idempotent and takes ~37s on gteam, so the
upgrade is self-healing and cheap. Per **INVARIANT M-2**, the rewrite must preserve every
`_migrations` key it does not own, byte for byte.

#### Not in scope

M9 splits permanent from actionable. It does **not** split per-cause reporting
(DEF-124/125), which is a different axis and carries its own constraint that
`unresolvable` stay a separate bucket from `disagree`/non-UUID principal. Keep them apart.

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

**OQ-1 — RESOLVED by measurement.** Full execute run on a copy of the gteam snapshot:
**37 seconds**, all 39 projects, 19,082 messages, 6,476 writes (F11). Dry-run was 20s, so
write overhead is ~17s. Timing is a non-issue at this scale and the budget is not
load-bearing — set a generous default (10 minutes) purely as a runaway guard rather than
as a tuning parameter, and do not let it become one. The convergence caveat in §4.5 stays
in the design because it protects against a hub far larger than gteam, but it is no longer
an expected operating mode.

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

- **M0 — Surface the derivation reason** (DEF-114, §3 F12). Propagate `deriveErr` out of
  `groupForMessage` and record it in `result.Errors` instead of the fixed string. Also fix
  the `hazardA` classification so it can see rows that failed to derive. **This is now the
  first commit in the tranche and blocks M5**, because the marker policy in M-1′ depends
  on distinguishing run-level from row-level failure, and no one can currently tell the
  difference. Small change; re-measure immediately after it lands.
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
  **M-1′** marker semantics (§4.3 — *not* M-1, which is superseded and would livelock).
  Replace the `:1218` call site. Assert the warning still fires.
- **M5 — Backfill in the boot hook.** Per-project markers, budget, resumption.
- **M6 — Split the residual report** (§4.6) into reachable (WARN, actionable) and
  unreachable (INFO, stable), and delete the stale remediation string. Grep for prose
  describing the old behaviour — docs-site and SKILL.md — per the standing rule that
  removing a gate requires removing the text that describes it.
- **M7 — DEF-112 hardening** (*conditionally* optional). Make the counter and the skip
  guard share one predicate. OQ-4 resolved this as latent, so M7 was droppable if the
  tranche ran long, and it is listed last for that reason. **But if M6 satisfies the
  steady-state requirement with an anti-join count (see the correction in §4.6), the
  drift it guards against stops being latent and M7 becomes required.** M6 must report
  which approach it took.
- **M8 — DEF-113 rename** (§3 F10). Rename `stepMergeOrRekeyEmptyRef` to
  `stepSkipEmptyRef`, delete the unreachable `EmptyRefMerged` and `EmptyRefRekeyed`
  counters, and keep the B14 comment. Pure rename plus dead-field deletion, no behaviour
  change. **Not droppable** — unlike M7 this guards a security invariant against a
  plausible future edit, and it gets cheaper the sooner it lands.

- **M9 — Third residual bucket** (§4.8). Separate *permanently underivable in a listed
  project* from *actionable*, so the boot WARN counts only what re-running the backfill
  can fix. Added after the tranche was code-complete, once the arithmetic against gteam's
  measured numbers showed M6's WARN landing at ~12,606 — larger than the 5,637 it was
  built to suppress. Persist derive refusals and intentional skips as accumulators that
  survive completion; report three buckets; WARN only on the actionable remainder.
  **Acceptance is reconciliation against gteam boot-1's real numbers reaching ~0, not
  merely a smaller number**, and not a number the clamp produced.

M0 first, then re-measure. M1–M3 are independent of it and already dispatched. **M5 is
blocked on M0**, not on OQ-1 — that one is now resolved. M6 depends on M5 for the
reachable count it reports. DEF-115 (§3 F13) is not yet scheduled: it needs the M0
classification to size, since it may be a large population or exactly the four rows seen.

---

## 9. Acceptance Criteria

A reviewer or QA agent should verify:

1. **Idempotence.** Boot twice against a migrated database. The second boot performs no
   conversation or message writes. Assert on query counts or write counts, not on wall
   time.
2. **M-1′ is enforced — both halves.** This is the most important pair of tests in the
   set, and it is a *pair* because the invariant is a distinction, not a threshold.
   - **Row-level refusal → marker IS written.** With a store stubbed so that one row
     cannot be derived, the run completes, the refusal is counted and logged (bounded
     sample), and **the marker is present** with a non-zero residual count. The next boot
     does not re-run. A test asserting the marker is *absent* here encodes superseded M-1
     and reintroduces the livelock of §4.3 — on gteam that is 11,593 deterministic
     refusals and a permanent boot penalty that makes no progress.
   - **Run-level failure → marker is ABSENT.** With the store stubbed so the pass itself
     fails (list error, write error, context cancelled), an ERROR is logged and **no
     marker is written**. The next boot retries.

   The failure this pair guards against is writing the marker on `err == nil` *without*
   distinguishing the two, in either direction.
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

   **And the steady-state case** (added post-M5, see the correction in §4.6): run the
   boot hook a second time, so every project is already in `projects_done` and no
   backfill work is performed. Both counts must still be correct and the WARN must still
   not fire. The first boot is the only run on which per-project sums exist, so a
   single-boot test passes over the defect entirely.
10. **B14 still holds after auto-run.** Seed a keyless `direct` row. After the boot hook
    runs, the row is unchanged — still keyless, no participants invented from the listing
    index — and is reported as skipped. This is the assertion that would catch someone
    implementing the behaviour DEF-113's stale name advertises.
11. **Build tags.** Any new test needing sqlite carries `//go:build !no_sqlite`, and the
    author confirms whether the blocking `make test-fast` gate can see the new tests. Per
    DEF-94's per-change form, a green gate that compiles the new tests out says nothing.

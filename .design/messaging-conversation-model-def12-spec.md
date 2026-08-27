# DEF-12 — the conversation backfill has no entry point

Spec written 2026-08-27 19:20Z. Prior art grepped and cited (rule 16).
**Unblocked:** DEF-15 closed on `messaging-v2` @ `14b3ba7c` — `backfill.go:196` now derives through
`DeriveConversationKey`, and `conversation.go:139-161` refuses non-canonical `dm:` keys rather than
resolving them. The reason this row said "do not dispatch" is gone. Verified, not assumed.

## 1. The defect

`pkg/messaging/backfill.go` is a complete, careful implementation — `NewBackfillService` (:83),
`Run` (:93), with batching, resume, and dry-run — and **nothing calls it**. `git grep 'Backfill'`
outside the file itself and its tests returns zero production hits: no CLI subcommand, no admin
endpoint, no startup hook.

Consequence: on any deployed instance, **every message predating this branch keeps an empty
`conversation_id` forever.** The read switch (tranche G) then serves a conversation view with a
hole in it exactly as large as the instance's history.

## 2. Correct my recorded instinct before you act on it

The ledger row says my instinct was a `sciontool` subcommand. **That is wrong and I checked.**
`cmd/sciontool/commands/` is agent-side tooling — status, hooks, credential helper, provisioning.
It has no server-database surface and no migration command. Putting an operator database job there
would be the first of its kind and in the wrong binary.

## 3. Prior art — the pattern this should follow

`cmd/server_foreground.go:1309` `maybeMigrateLegacySQLite` is the closest existing thing, and it is
worth copying the *shape* of:

- **detect first, act second** — `entc.IsLegacyRawSQLSchema(path)` decides whether there is
  anything to do, and the function is a no-op when there is not;
- **automatic backup before mutation**;
- **operator opt-out flag** (`--no-auto-migrate`);
- **fail loudly with guidance** when opted out but action is required — it does not silently skip;
- **a report struct**, logged: tables, rows, backup path.

`migrateStore` (`:1226`) is the second instance of the same shape.

## 4. The design question, and my reading of it

**Do not run the backfill automatically at startup.** The service was built with batching, resume
and dry-run. Those three features only make sense for a job that is long, interruptible, and worth
rehearsing — which is exactly a job you must not block server startup on. The existing
auto-migration is bounded by schema size; this one is bounded by message history.

**But "operator-invoked" is how DEF-12 happened.** A job nobody is told to run is a job nobody
runs. So split the two halves of the prior-art pattern:

- **Detection is automatic and unavoidable.** At startup, count messages with a NULL/empty
  `conversation_id`. If non-zero, log a warning naming the count and the exact command to run. This
  is cheap, read-only, and cannot corrupt anything.
- **Execution is explicit.** An operator command, defaulting to dry-run.

**Where the command lives is the one thing I am not settling for you.** There is no `admin`, `db`
or `maintenance` group in `cmd/` today, so you are either creating one or attaching to an existing
noun. Survey `cmd/hub*.go` — the hub commands are the closest existing operator-facing surface —
pick one, and **say in your report what you chose and what you rejected.** I would rather you make
this call with the code in front of you than have me guess at it from a grep.

## 5. Non-goals

- Do not modify `backfill.go`'s algorithm. It is reviewed and it is not what is broken.
- Do not wire the backfill into the read switch. Ordering between them is tranche sequencing, not
  this change.
- Do not make the backfill a hard prerequisite for server startup.

## 6. Acceptance criteria

- **AC-12-1** With unbackfilled messages present, server startup logs a warning naming the count
  and the command. With none present, startup logs nothing. **Both halves tested** (rule 29 — a
  constraint test that only asserts the warning cannot see a spurious warning).
- **AC-12-2** The command defaults to dry-run. Running it with no flags **mutates nothing** —
  assert by row comparison before and after, not by reading the output.
- **AC-12-3** A real run populates `conversation_id` for historical messages, and re-running is a
  no-op rather than a duplicate.
- **AC-12-4** Interrupt and resume: kill mid-run, re-run, end state equals the uninterrupted end
  state. This is the feature `backfill.go` already claims; prove it through the new entry point.
- **AC-12-5** A malformed historical `ThreadID` leaves that row unbackfilled and is **reported**,
  not guessed and not silently dropped. Parse failure denies. **The count of skipped rows appears
  in the report** — an operator must be able to see that the backfill was partial.
- **AC-12-6** Exercised against a **populated** database, not a fixture-only one.
  `integration2-operator` has a working snapshot/restore for scion-gteam; take a snapshot, run it,
  inspect, restore.
- **AC-12-7** Mutation-verified: break the detection count so it always returns zero, and confirm
  AC-12-1's positive case fails.

## 7. Tranche placement

Depends on tranche A (schema + `backfill.go`). Does not belong *in* A — A is already the tranche
carrying the DB migration and I am not adding an operator command to it. Land after A, before G.

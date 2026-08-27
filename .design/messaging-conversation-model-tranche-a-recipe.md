# Tranche A — cut recipe (validated, not proposed)

**Validated end to end 2026-08-27 19:10Z in a throwaway worktree off `origin/main` (`b09e7f49`).**
Result: `go build ./...` clean, `go test ./pkg/... ./cmd/...` = **66 packages ok, EXIT=0**,
including `pkg/hub` (301.9s) and main's P2-A1 admin tests.

Source branch: `scion/messaging-v2` @ `14b3ba7c`.

---

## Why this is a recipe and not a file list

**Do not cut this tranche with `git checkout messaging-v2 -- <files>`. I tried; it reverts main.**

Three classes of file, three different methods. See rule 31 in IMPLEMENTATION-STATE.md §1.

| Class | Files | Method | Why |
|---|---|---|---|
| **Generated ent code** | everything under `pkg/ent/` except `pkg/ent/schema/*` | **Regenerate** | `predicate.go`, `client.go`, `mutation.go`, `tx.go`, `ent.go`, `runtime.go`, `migrate/schema.go` each enumerate **every entity in the schema**. messaging-v2's copies predate main's `EntitlementBinding`/`LimitDefinition`/`UsageReservation`. Copying them deletes ~1000 references **including three tables in `migrate/schema.go`**, the migration definition. |
| **Hand-written aggregate** | `pkg/store/models.go`, `pkg/store/store.go`, `pkg/store/entadapter/composite.go`, `pkg/messages/types.go` | **`git apply --3way` the diff vs merge-base** | Grab-bag files every feature appends to. Transplanting removes `store.LimitDefinition`, `SystemRoleHubAdmin`, `GetLimitDefinitionByName`, `CountActiveReservations`, … |
| **Feature-specific** | the rest, below | Transplant whole | Safe — nothing else writes to them. |

**The build failed loudly here only because those three entities were brand new. If main had
*modified* an existing entity instead, the transplant would have compiled and reverted it in
silence.** Assume the silent case is the normal one.

## Steps

```bash
# 0. Fresh worktree off CURRENT origin/main. Re-fetch; main moves ~hourly.
git fetch origin main
git worktree add --detach /tmp/trA origin/main
cd /tmp/trA

# 1. Feature-specific files + the 4 hand-written ent schemas.
git checkout 14b3ba7c -- \
  pkg/ent/schema/conversation.go \
  pkg/ent/schema/conversation_participant.go \
  pkg/ent/schema/message.go \
  pkg/ent/schema/message_addressee.go \
  pkg/messaging/backfill.go      pkg/messaging/backfill_test.go \
  pkg/messaging/drift.go         pkg/messaging/drift_test.go \
  pkg/messaging/normalize.go     pkg/messaging/normalize_test.go \
  pkg/messaging/resolve.go       pkg/messaging/resolve_test.go \
  pkg/messaging/derive_key.go    pkg/messaging/derive_key_test.go \
  pkg/messaging/conversation.go  pkg/messaging/conversation_test.go \
  pkg/messages/dm_key.go         pkg/messages/dm_key_test.go \
  pkg/store/entadapter/conversation_store.go \
  pkg/store/entadapter/conversation_store_test.go \
  pkg/store/entadapter/message_store.go

# 2. Aggregate files: diff, never file.
MB=$(git merge-base origin/main 14b3ba7c)
git -C <repo> diff $MB 14b3ba7c -- \
  pkg/store/models.go pkg/store/store.go \
  pkg/store/entadapter/composite.go pkg/messages/types.go > /tmp/agg.patch
git apply --3way /tmp/agg.patch     # applied cleanly at 14b3ba7c; if it conflicts, main moved — resolve, do not force

# 3. Regenerate ent from schema.
(cd pkg/ent && go generate ./...)

# 4. Verify BOTH symbol sets survive. This is the anti-revert check; do not skip it.
#
# CORRECTED 19:26Z. The first version of this step printed approximate expected
# values ("~234"). They were measured with `grep -ci` while the command below is
# `grep -c`, so they were all ~10% high and invited exactly the wrong response:
# eyeballing a mismatch and calling it the right order of magnitude. An AC with an
# approximate expected value is not an AC.
#
# The check is now self-calibrating: compare against main's OWN counts, computed
# now, with the same command. Equality is required, not similarity.
ADMIN='EntitlementBinding|LimitDefinition|UsageReservation'
OURS='ConversationParticipant|MessageAddressee'
for f in pkg/ent/client.go pkg/ent/migrate/schema.go pkg/ent/predicate/predicate.go; do
  before=$(git show origin/main:$f | grep -Ec "$ADMIN")
  after=$(grep -Ec "$ADMIN" $f)
  ours=$(grep -Ec "$OURS" $f)
  printf '%-34s admin main=%-4s ours=%-4s  new=%s\n' "$f" "$before" "$after" "$ours"
  [ "$before" = "$after" ] || echo "  *** ADMIN COUNT CHANGED — STOP. This is the revert. ***"
  [ "$ours" -gt 0 ]        || echo "  *** OUR ENTITIES ABSENT — the regeneration did not take. ***"
done

# Reference values at origin/main b09e7f49, case-SENSITIVE, for sanity only —
# if main has moved these will differ legitimately and the loop above is authoritative:
#   client.go            admin 211   ours 130
#   migrate/schema.go    admin  31   ours  21

go build ./... && go test ./pkg/... ./cmd/...
```

## Dependency corrections to §1b

§1b excluded `pkg/messaging/conversation.go`. **Wrong** — `derive_key.go` needs
`ConversationUpserter` and `ConversationResult` from it. §1b also omitted `derive_key.go` and
`pkg/messages/dm_key.go`, which `backfill.go` and `resolve.go` require. All now included.

## Acceptance criteria

- **AC-A-1** Step 4's loop reports **`admin main == admin new` exactly** for every file, and
  **`ours > 0`** for every file. Not "the right magnitude" — equal. **This is the tranche's most
  important AC; it is the revert check.** If a count differs by even one, stop and report it rather
  than judging whether the difference looks acceptable. That judgement is not available to you: a
  revert and a legitimate regeneration difference look identical from inside the tree.
- **AC-A-2** `go build ./...` clean; `go test ./pkg/... ./cmd/...` EXIT=0.
- **AC-A-3** All seven CI gates in `.github/workflows/ci.yml` pass. **Read the file; do not work
  from memory of it** (rule 22). gofmt in particular — the branch has ~15 pre-existing violations
  and `golangci-lint` runs `--new-from-merge-base=origin/main`, so each tranche pays its own share.
- **AC-A-4** The ent migration applies cleanly to a **populated** SQLite DB, not just an empty one.
  Three new tables, no destructive change to existing ones. Prove it against a real snapshot —
  `integration2-operator` has a snapshot/restore for scion-gteam.
- **AC-A-5** No file under `pkg/ent/` other than `pkg/ent/schema/*` is hand-edited. Generated code
  is generated. Assert by regenerating and confirming a clean tree.
- **AC-A-6** Diff reviewed for *deletions* specifically: `git diff origin/main --stat` and inspect
  every file where deletions exceed additions. Rule 25.

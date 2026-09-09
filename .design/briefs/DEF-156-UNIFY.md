# Brief: DEF-156 — two backfills spell the same thread two different ways

**Read `_GATE-APPARATUS.md` in this directory first and follow it exactly.**
In particular: verify your base hash before you edit anything, and lead your
report with the `merge-base --is-ancestor` line and a numstat computed against
this brief's base.

**Base:** `scion/tranche-g` @ **`77ebea1f6`** on `https://github.com/ptone/scion.git`.
`origin` in your container does **not** point at ptone/scion. Never push to
`main` or to `tranche-g`. Push to `scion/ca-msg-unify` and report to me
(`ca-msg-arch`) **before** you push.

**Design:** `/scion-volumes/scratchpad/projects/ca-msg-arch/DEF-156-DESIGN.md`.
**Read it in full before you start.** This brief implements §7 phases P1–P4 and
does not restate the reasoning. Where this brief and the design disagree, the
design is right and I want to know.

---

## What is wrong, in one paragraph

A web chat thread's conversation row can be created by two different
components. `backfillTopicConversations` (and `CreateTopic`) write
`external_ref = ''` and set `webchat_topic.conversation_id` — that is the row
the UI reads. `BackfillService.persistGroup` writes
`external_ref = 'thread:<project>:<threadID>'`, sets no topic link, and stamps
every unstamped message onto it. Both converge on
`UpsertConversationByExternalRef`, which matches on `(surface, external_ref)`,
so `''` and `thread:…` never match. The messages land on the row nobody reads
and the thread renders empty. The message backfill runs during store
construction, before the web chat store exists, so it always goes first. **On
a fresh cutover this empties every web thread that has pre-stamping history.**

---

## Scope — four commits, in this order

**P1 before P2 is load-bearing.** P2 without P1 duplicates a format string,
which is the exact substance of this defect.

### P1 — one derivation, called from both sides

`pkg/messaging/derive_key.go:100` builds `fmt.Sprintf("thread:%s:%s",
in.ProjectID, in.ThreadID)` inside `DeriveConversationKey`. `pkg/hub`'s stores
need the same string.

Export a thin helper over the existing logic — do **not** copy the format
string, and do **not** reimplement it "for the hub's simpler case". Two
independent spellings of one key is the bug.

No behaviour change in this commit. Verify that.

Test: a table of `(projectID, topicID)` vectors asserting the helper and
`DeriveConversationKey` produce byte-identical output. **Assert against literal
expected strings in the table, not against each other** — two functions
compared to one another are self-consistent by construction and would both
drift together.

### P2 — the topic backfill upserts on that key

`backfillTopicConversations` (`pkg/hub/webchannel_store.go:1462`, pg twin
`webchannel_store_postgres.go:1094`) and `CreateTopic`
(`webchannel_store.go:680`, pg `:316`), **both stores**:

- Write `external_ref = <P1 helper>(projectID, topicID)` instead of `''`.
- **Look up `(native, that key)` before minting.** If a conversation already
  exists on that key, link the topic to **it** and do not mint. Only mint when
  absent.

That lookup is what makes the fix order-independent, which is the whole point
of choosing this design over the alternative. It is not an optimisation.

**INVARIANT U-TX-1:** anything touching the ambient pool must be called
**before** `BeginTx`. `hasConversationsTable()` already obeys this at
`webchannel_store.go:2106` — your new lookup must too. **At `MaxOpenConns=1` a
violation HANGS rather than fails**, so every test you write around this must
carry an explicit timeout. If a test of yours hangs, that is a finding to
report, not a flake to retry.

Mirror the existing sqlite/postgres asymmetry that `CreateTopic` already
documents at `webchannel_store.go:693` and `webchannel_store_postgres.go:321`:
sqlite gates on `hasConversationsTable()`, postgres does not. Do not
"harmonise" it.

> ## ADDENDUM 2026-09-09 — the collision surface P2 creates
>
> Lookup before `BeginTx`, insert inside it: check-then-insert is not atomic.
> A writer arriving in between loses to the partial unique index.
>
> **The index prevents the duplicate by *failing* the loser — that is the
> opposite of convergence, which is the whole reason P2's lookup exists.**
> "The index catches it" is not an answer here. Assess per call site:
>
> - **`backfillTopicConversations` — no change needed.** Boot is
>   single-threaded and the marker is written only on a completed pass, so a
>   partial failure re-runs next boot and the lookup finds the first pass's
>   rows. Self-healing.
> - **`CreateTopic` — live request path, boot argument does not transfer.**
>   And note: before P2 it wrote `''` and was structurally incapable of
>   colliding with Route 2's `thread:` key — disjoint namespaces. **P2 merges
>   them and creates this contention.** Say so in your report.
>
> **What I need (AC-156-9):** enumerate every `CreateTopic` call site and where
> its `topic.ID` comes from. If each mints a fresh UUID immediately before
> calling, the window is closed by construction and I accept that — but
> establish it from the call sites, not from the store. Check
> `EnsureGeneralTopic` first: "the general topic for this project" is a name
> two concurrent requests can both resolve to.
>
> If the window is reachable: **on unique-constraint error from the
> conversations INSERT, re-read inside the transaction and link the topic to
> the winner** rather than returning the error. Not a bare `ON CONFLICT DO
> NOTHING` — it returns no rows and the winner's id is what you need. Any
> `ON CONFLICT` must match the partial index's conflict target **including its
> predicate** or it will not fire at all.
>
> Also: `webchannel_store.go:690-693` says *"The external_ref derivation (empty
> string) matches backfillTopicConversations."* P2 falsifies it. Fold it into
> P4 with the other stale comments.

### P3 — derive `surface` from the channel

`persistGroup` (`pkg/messaging/backfill.go:353`) hardcodes `Surface: "native"`
with no reference to the message's channel. Derive it.

The enum is `native|discord|slack|telegram|gchat|teams`. **A channel you cannot
map must refuse, not default.** Count it as a derive failure through the
machinery `Run` already has for DEF-114 (`result.addDeriveFailure`). Do not
coerce to `native` — a Discord thread permanently recorded as native is a bug
we are already fixing, and silently widening it would be worse than leaving
the row unstamped.

A group is assembled from many messages. If a group's messages disagree on
channel, **refuse the group and report the count to me** — do not pick one.
I want to know the number before I decide the rule.

### P4 — make the comments true

`pkg/messaging/conversation.go:425` and `:439` both assert *"native topics
write external_ref = '' so the external_ref lookup below never matches them"*.
After P2 that is false for new rows and true for old ones. Rewrite both to
state the mixed population explicitly. Same for the intercept comment in
`derive_key.go` if it makes the same claim.

Comment-only. A comment asserting a falsehood about the data model is how the
next person reproduces this defect.

---

## What you must NOT do

- **Do not remove either topic-lookup intercept** — not the write-path one in
  `ResolveOrCreateConversationByKey`, not the read-path one in
  `ResolveThreadConversationForRead`. They become redundant for new rows and
  stay load-bearing for old ones. Their retirement is switch-collapse work.
- **Do not change the live write path.** Route 2 is correct today. If one of
  its existing tests needs editing to make your change pass, **stop and report
  it** — that is a finding, not an edit.
- **Do not write a migration.** Not for the existing `''` rows, not to merge
  the known shadow conversations. That is design §7 P5, it is explicitly not
  authorised, and it is ptone's decision.
- **Do not touch gteam.** Nothing on that instance is yours to change.
- **Do not reorder the startup sequence.** `cmd/server_foreground.go:1218` and
  `:598` stay exactly where they are. The design rejects the ordering fix on
  purpose; re-introducing it defeats AC-156-2.
- **Do not put anything in `parent_ref`.** Caller-supplied from request JSON on
  two live paths. Settled.
- **Never make a gate pass by weakening the gate.** Any red is reported to me,
  not tuned away. Re-running a failing gate until it goes green without
  capturing the failure is the same offence.

---

## Verification

### The acceptance test — integration, not store-level

Build a database that looks like a hub about to cut over: web topics whose
messages carry `thread_id` and **no** `conversation_id`. Boot the real
migration sequence. Read each thread through the endpoint the UI calls.
**Every message comes back, on the first read, with no restart.**

**A store-level test would have passed before this fix and proves nothing.**

Then the criterion that distinguishes this design from the one I rejected:

**Run the same assertion with the two backfills in both orders.** Topic
backfill first, then message backfill; and message backfill first, then topic
backfill. Both must pass. Drive both explicitly — asserting the current order
twice does not satisfy this.

### Mutation, both directions

1. Both order-tests on unmodified source → **PASS**.
2. Revert P1's shared key only (put the old `''` back in the topic backfill).
   → the fresh-cutover test must **FAIL**, reporting missing messages.
3. Restore. Remove P2's pre-mint lookup only. → the order-independence test
   must **FAIL**, or the "exactly one conversation per topic" assertion must.
4. Restore. → **PASS**.

Paste all four raw. **A mutation that fails to compile is not a caught
violation** — make it compile-safe (`_ = x`) and re-run.

### Ordinary assertions

- **Exactly one** conversation per topic after boot. Not "at least one".
- Boot twice: no new conversations, no re-stamping, counts identical.
- A Discord-channel message with a `thread_id` does not produce a
  `surface='native'` row. Assert the row, not a log line.
- A pre-existing `''`-ref topic with a populated topic link still resolves and
  still returns its messages. This is the mixed population — if it breaks,
  upgrades break.

---

## Collateral gates

Run from your worktree, **without pipes** — `$?` after a pipeline is the last
command's status, and this shell is zsh where `${PIPESTATUS[0]}` is empty.

```sh
export GOCACHE=/tmp/gocache-unify      # the shared gocache is contended
go build ./... > /tmp/u-build.log 2>&1; echo "build=$?"
go vet ./...   > /tmp/u-vet.log   2>&1; echo "vet=$?"
make compat-literals check-authz-guards check-conversation-upsert-guard check-security-marker-gates
gofmt -l <your changed files>
```

`check-conversation-upsert-guard` watches this exact surface. **If it fires,
that is a finding and you report it — it is not an obstacle to route around.**

**SUPERSEDED 2026-09-09 — this paragraph is wrong. See `_GATE-APPARATUS.md`
§ `//go:build !no_sqlite`, which is the authority.** "A test file needs sqlite,
therefore it needs the tag" is false: `no_sqlite` gates only `pkg/store/sqlite`
and the `pkg/ent/entc` driver, and `mattn/go-sqlite3` is ungated. **The tag is
required iff the file reaches a `!no_sqlite`-only package.** Adding it otherwise
silently removes the tests from `make test-fast`, the only gate in ci.yml that
can fail a build. Probe both directions and compare `--- PASS:` counts before
deciding. **Never strip the tag from an existing file to make something run** —
that part was always right.

~~If a test file needs sqlite it **must** carry `//go:build !no_sqlite`. Adding
that tag to a new sqlite-dependent test file is correct and expected. **Never
strip the tag from an existing file to make something run.** The directive sits
below a 14-line Apache header — locate it with grep, not `head -n`.

> **CORRECTION 2026-09-09 — the "~7 minutes" figure below was wrong and is
> retracted.** It was mine and unsourced. Measured on clean `77ebea1f6`,
> `go test -timeout 1800s ./pkg/hub/`: **35:05.83 wall** — about 5m30s of compile
> plus a 30m test binary ending in `panic: test timed out`.
> **`TestRS1_StaleAuthorityForcedOverlap` alone accounts for 23m37s** and is the
> only test still running at the panic. Everything else finishes in roughly
> 6m23s, which is probably where the old figure came from before RS1 grew.
>
> **Do not run the full `pkg/hub` package.** Run your tests by name. RS1 is
> `ci-fix-lead`'s and carries `!no_sqlite`, so it never fires in blocking CI.
>
> `-timeout` covers the **test binary only, not compile**. Wall clock is compile
> plus the timeout.

Run tests in the **foreground** with an explicit timeout. A backgrounded job's
exit code belongs to the launcher, not the job; if you background anything,
confirm the log has bytes before believing a green.

**A failure count from a run that panicked at its timeout is a floor, not a
total.** Without `-v` the log prints no PASS lines, so it cannot distinguish
"passed" from "never ran". Label any such count as a floor.

Expected red and **not yours**: `gofmt` on `pkg/hub/handlers_agents_core.go`
and `pkg/hub/web_test.go`, and `TestMutationClassificationBidirectional` at 200
discovered / 198 classified. Both inherited from upstream main and verified
against a clean main checkout. **Do not reformat those files.**

---

## Reporting

Report to `ca-msg-arch` **before pushing**, with:

- the `merge-base --is-ancestor` line and `git diff --numstat` against
  `77ebea1f6`, run by you, pasted raw;
- both order-tests and all four mutation runs, raw;
- the count of groups whose messages disagreed on channel (P3);
- any response-shape or store-interface change, stated explicitly — do not let
  me find it in the diff;
- anything you changed that this brief did not ask for, named. If it is out of
  scope, route it to me — do not grade it harmless and keep it.

If your findings contradict this brief, **your findings are the finding.**
Report them; do not adjust your work to match my prose.

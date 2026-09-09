# Review brief: DEF-156 — two backfills spell the same thread two different ways

You are reviewing, not implementing. **Do not fix what you find** — report it to
`ca-msg-arch`. If something is trivially wrong and you are certain, still report
it; I decide whether it gets fixed here or filed.

**Read `_REVIEW-SEVERITY-RUBRIC.md` in this directory before you assign a
severity to anything.** It is short and it is not optional. The last review on
this project found a real data-loss defect and filed it under *Nit / Optional*;
the rubric exists because of that.

## What to fetch

```sh
export GITHUB_TOKEN=$(cat /scion-volumes/scratchpad/transition-github-token.txt)
git -C /workspace fetch -q "https://x-access-token:${GITHUB_TOKEN}@github.com/ptone/scion.git" \
    scion/ca-msg-unify:refs/rt/rev156 --force
git -C /workspace worktree add --detach /tmp/wt-rev156 refs/rt/rev156 -q
```

Base is **`77ebea1f6`** on `scion/tranche-g`. Head is whatever
`scion/ca-msg-unify` points at — **confirm it and lead your report with the
SHA**, plus your own `merge-base --is-ancestor` line and `git diff --numstat`
against the base. `origin` in your container does **not** point at ptone/scion.
**Never push to `main` or to `tranche-g`.** You are not pushing at all.

Ten commits. Read them individually — several later ones revise earlier ones,
and the interesting content is in the revisions.

**Design:** `/scion-volumes/scratchpad/projects/ca-msg-arch/DEF-156-DESIGN.md`.
**Implementation brief:** `briefs/DEF-156-UNIFY.md`, including its ADDENDUM under
P2, which is current instruction.

---

## What the change does, in one paragraph

A web chat thread's conversation row can be minted by two different components
that spell its `external_ref` differently: the topic backfill writes `''`, the
message backfill writes `thread:<project>:<threadID>`. They converge on an upsert
keyed on `(surface, external_ref)`, so they never match, the messages land on the
row nobody reads, and the thread renders empty. On a fresh cutover this empties
every web thread with pre-stamping history. The fix exports one derivation
(P1), makes the topic side write and **look up** that key before minting (P2),
derives `surface` from the channel instead of hardcoding `native` (P3), and
corrects comments that assert the old model (P4).

---

## The eight things I most want checked

### 1. The collision surface P2 creates — and whether it is actually closed

This is the highest-severity item and it is subtle, so read it twice.

Before P2, the topic side wrote `external_ref = ''` and the message side wrote
`thread:…`. Disjoint namespaces: **structurally incapable of colliding.** P2 puts
them in one namespace. There is now a partial unique index over
`(surface, external_ref)` with predicate `external_ref <> '' AND deleted_at IS
NULL`, and the new code does **check-then-insert** with the lookup before
`BeginTx` and the insert inside it. Those are not atomic.

**The index does not produce convergence. It prevents the duplicate by failing
the loser** — which is the opposite of what P2's lookup exists to achieve.

The developer's answer, which I provisionally accepted, is that the window is
closed by construction because every `CreateTopic` caller mints a fresh UUID
immediately before calling. **Verify that independently**, repo-wide:

```sh
grep -rn '\.CreateTopic(' . --include='*.go' | grep -v '_test.go' | grep -v vendor/
```

Expected: exactly one production hit, `pkg/hub/handlers_chat_v2.go:473`, with a
fresh `uuid.New().String()` minted ~12 lines above and no reuse. If you find a
second caller anywhere, or one that takes its `topic.ID` from a request, a
config, or a name-based lookup, **that is a Critical finding** — the window is
open and the failure mode is a live request erroring out.

Do the same for `EnsureGeneralTopic`. "The general topic for this project" is a
name two concurrent requests can both resolve to, which is exactly the shape that
makes a fresh-UUID argument fail.

If any `ON CONFLICT` clause was added on the conversations insert, check that its
conflict target matches the partial index **including the predicate**. A partial
index does not fire for an `ON CONFLICT` that omits its `WHERE`.

### 2. U-TX-1 — every ambient call before `BeginTx`, in every store

`hasConversationsTable()` and the new pre-mint lookup both touch the ambient
pool. **At `MaxOpenConns=1` an ambient call between `BeginTx` and `Commit`
deadlocks rather than fails.** Inside the transaction, `tx.QueryRow` and
`tx.ExecContext` are safe; `s.db.anything` or any helper that reaches the pool is
not.

Check all three functions in **both** stores: `backfillTopicConversations`,
`CreateTopic`, `EnsureGeneralTopic`. If you write any test here, **give it an
explicit timeout** — a violation hangs, and a hang costs the evidence as well as
the time.

### 3. P1 — one derivation, and a test that could actually fail

The point of P1 is that the `thread:%s:%s` format string exists **once**. Confirm
`pkg/hub` calls the exported helper and does not re-implement the format "for the
hub's simpler case".

Then check the test the way I care about: it must assert against **literal
expected strings** in a table. **Two functions compared to one another are
self-consistent by construction and would drift together silently.** If the test
asserts `helper(x) == DeriveConversationKey(x)` and nothing else, it is not a
test of either.

### 4. The sqlite/postgres asymmetry is preserved, not harmonised

sqlite gates on `hasConversationsTable()`; postgres does not, because its
migrations guarantee the table. That asymmetry is deliberate and documented. A
reviewer or a bot "cleaning it up" breaks one of the two stores. Confirm both
still behave as documented and that neither grew the other's gate.

Separately, confirm the two stores' new predicates and lookups are **semantically
identical** where they are supposed to be. Postgres uses `$N` placeholders and is
historically the one that drifts.

### 5. P3 — refusal, not coercion

`persistGroup` hardcoded `Surface: "native"`. It must now derive from the
channel, and **a channel it cannot map must refuse, not default**. A Discord
thread permanently recorded as `native` is the bug we are fixing; silently
widening it would be worse than leaving the row unstamped.

Check: unmappable channel → counted as a derive failure through the existing
DEF-114 machinery, not coerced. A group whose messages disagree on channel →
group refused entirely, zero conversations, zero messages stamped.

The developer reclassified this path from `WriteFailure` to
`DeriveFailure[surface_conflict]`. Confirm the error invariant still holds:
`sum(DeriveFailures) + WriteFailures + ResolutionFailures == len(Errors)`.

### 6. The DEF-100 T1 control — re-run its mutation yourself

`handlers_read_switch_def100_test.go` had a Step 3 control proving the
topic-lookup intercept is load-bearing. P2 made its original assertion false, the
developer deleted it, I required it back re-pointed at the legacy (`''`-ref)
population, and it is now three `t.Run` subtests.

**Re-run the mutation.** Disable the intercept in
`ResolveThreadConversationForRead` (`if false &&` on the guard, or equivalent)
and confirm **`Step3b_LegacyTopic_ResolvesWithIntercept` goes red on its own
line**, as a named subtest, independently of Step 2. Restore, confirm green.

`Step3a` is expected to stay **green** under that mutation — it asserts a
negative that holds either way, and it is there as 3b's non-vacuity control.
That is correct construction, not a gap. Do not recommend deleting it.

### 7. Deletions — this is where I most want a second pair of eyes

Across ten commits, **enumerate every assertion, gate, guard or test that was
removed or weakened**, and give me the list even if you judge each one fine. The
standing directive on this project is that risky replace actions get caught in
review, and this branch has already had one assertion deleted and reinstated.

For each deletion ask both questions separately: **is the assertion false now,
and is the property it protected still true somewhere?** Only the first licenses
deletion. On this branch the answers were yes and yes, and the correct move was
re-pointing.

Specifically confirm **not** touched:
- Either topic-lookup intercept — the write-path one in
  `ResolveOrCreateConversationByKey`, the read-path one in
  `ResolveThreadConversationForRead`. They are redundant for new rows and
  load-bearing for old ones; retiring them is separate, deliberate work.
- The live write path (Route 2). It is correct today. **If one of its existing
  tests was edited to make this change pass, that is a Critical finding.**
- Startup ordering: `cmd/server_foreground.go:1218` and `:598` stay put. The
  design rejects the ordering fix deliberately.
- `parent_ref` — stays `''`, caller-supplied from request JSON on two live paths.
- No migration. Not for existing `''` rows, not to merge shadow conversations.
  That is ptone's decision and it is not authorised.

The prohibition list applies: `authenticatedSender`, `parseDMKeyIDs`,
`isDMParticipant`, `checkDMParticipantKey`, `EnsureParticipant` and its `left_at`
preservation, the `direct` non-empty `external_ref` predicate, `Broadcasted`
server-side forcing. **The invariant is reachability: no messaging path may reach
send without passing `authorizeAgentMessage`.**

### 8. The mixed population still works

After this change the database holds both `''`-ref conversations (existing) and
`thread:`-ref ones (new). Confirm a pre-existing `''`-ref topic with a populated
topic link **still resolves and still returns its messages**. If that breaks,
upgrades break, and it will not show up in any test written around the new path.

---

## Independently re-run these

Do not accept the developer's runs.

1. **Order independence.** The acceptance test asserted with topic-backfill-first
   and with message-backfill-first. Both must pass. Confirm both orders are
   **driven explicitly** — asserting the current order twice does not satisfy
   this, and it is an easy thing to get wrong in a way that looks right.
2. **Revert P1's key** in the topic backfill only (put `''` back). The
   fresh-cutover test must go **red reporting missing messages**.
3. **Restore. Remove P2's pre-mint lookup only.** The order-independence test
   must go red, or the "exactly one conversation per topic" assertion must.
4. **The intercept mutation from item 6.**
5. Restore all, confirm green.

**A mutation that fails to compile is not a caught violation** — make it
compile-safe (`_ = x`) and re-run. **Mutate in both directions.**

Also confirm as ordinary assertions: **exactly one** conversation per topic after
boot, not "at least one"; boot twice yields no new conversations and no
re-stamping.

## Gates

```sh
export GOCACHE=/tmp/gocache-rev156
go build ./... > /tmp/r156-build.log 2>&1; echo "build=$?"
go vet ./...   > /tmp/r156-vet.log   2>&1; echo "vet=$?"
make compat-literals check-authz-guards check-conversation-upsert-guard check-security-marker-gates
gofmt -l <changed files>
```

No pipes — `$?` after a pipeline is the last command's status, and this shell is
zsh where `${PIPESTATUS[0]}` is empty.

`check-conversation-upsert-guard` watches this exact surface. **If it fires, that
is a finding you report — not an obstacle to route around.**

If a test file needs sqlite it must carry `//go:build !no_sqlite`. Adding that
tag to a new sqlite-dependent test file is correct. **Never strip it from an
existing file to make something run.** The directive sits below a 14-line Apache
header — find it with grep, not `head -n`.

**Do not run the full `pkg/hub` package.** Measured on clean `77ebea1f6`:
**35:05 wall**, being ~5m30s compile plus a 30m test binary ending in `panic:
test timed out`, with `TestRS1_StaleAuthorityForcedOverlap` alone at **23m37s**.
Run tests by name. RS1 is upstream's, carries `!no_sqlite`, and is with
`ci-fix-lead`.

The clean-base run shows **32 distinct pre-existing failures in `pkg/hub`, all
sub-second**. Those are not this branch's. That count is a **floor** — the binary
panicked, so nothing scheduled after RS1 ran, and without `-v` the log cannot
distinguish "passed" from "never ran".

Also expected red and **not yours**: `gofmt` on `pkg/hub/handlers_agents_core.go`
and `pkg/hub/web_test.go`, and `TestMutationClassificationBidirectional` at 200
discovered / 198 classified. Both inherited from upstream main. **Do not
reformat those files.**

Before debugging any odd build or link failure, run `df -h /`. This host has hit
99% today and an out-of-space link failure does not always announce itself as
one.

**Never make a gate pass by weakening the gate.** Any red is reported to me. So
is re-running a failing gate until it goes green without capturing the failure.

## Report

To `ca-msg-arch`, using the rubric's headings (Critical / Required / Nit-Optional
/ FYI), with: the head SHA, your own numstat and `merge-base --is-ancestor` line;
all five mutation runs raw; a verdict per numbered item; the full deletion
enumeration from item 7; and anything you found that this brief did not ask
about.

For every item you file under *Nit / Optional*, include one sentence: **what is
the worst state the system can end up in if this ships?**

**If your findings contradict this brief, your findings are the finding.** Report
them; do not adjust your review to match my prose.

# Review brief: DEF-96 — DM promotion loses its history

You are reviewing, not implementing. **Do not fix what you find** — report it to
`ca-msg-arch`. If something is trivially wrong and you are certain, still report
it; I decide whether it gets fixed here or filed.

## What to fetch

```sh
export GITHUB_TOKEN=$(cat /scion-volumes/scratchpad/transition-github-token.txt)
git -C /workspace fetch -q "https://x-access-token:${GITHUB_TOKEN}@github.com/ptone/scion.git" \
    scion/ca-msg-promote:refs/rt/rev96 --force
git -C /workspace worktree add --detach /tmp/wt-rev96 refs/rt/rev96 -q
```

Head is **`2f3beca21`**, base is **`77ebea1f6`** on `scion/tranche-g`. Confirm
both before reading anything. `origin` in your container does **not** point at
ptone/scion. **Never push to `main` or to `tranche-g`.** You are not pushing at
all — this is a read-and-report task.

Verified numstat (I ran this myself; confirm it independently):

```
 30   2  pkg/hub/handlers_chat_v2.go
368   0  pkg/hub/handlers_chat_v2_test.go
  1   1  pkg/hub/handlers_read_switch_test.go
 56   8  pkg/hub/webchannel_store.go
266   7  pkg/hub/webchannel_store_dualwrite_test.go
 41   5  pkg/hub/webchannel_store_postgres.go
  5   4  pkg/hub/webchannel_store_promote_test.go
```

**Design:** `/scion-volumes/scratchpad/projects/ca-msg-arch/DEF-96-DESIGN.md`.
**Read §3.1, §8, and the AC list in full.** The implementation brief is
`briefs/DEF-96-PROMOTE.md` and carries a correction banner partway down; the
banner is the current instruction and the text above it is superseded.

## What the change does

`PromoteDM` turns a user↔agent DM into a project thread. It used to re-key
messages with `UPDATE messages SET thread_id = ? WHERE thread_id = ?` and never
touch `conversation_id`, so the promoted thread rendered empty. The fix mints a
group conversation inside `PromoteDM`, and re-keys on a two-armed predicate:

```sql
UPDATE messages
   SET thread_id = ?, conversation_id = ?
 WHERE (? <> '' AND conversation_id = ?)      -- directConvID, when resolved
    OR thread_id = ?                          -- dmKey, legacy arm
```

---

## The seven things I most want checked

### 1. The `? <> ''` guard exists in BOTH stores

This is the highest-severity item in the change. With an empty `directConvID`
and no guard, `conversation_id = ''` matches **every unstamped message on the
hub** and the statement rewrites `thread_id` and `conversation_id` on all of
them in one shot.

Check `pkg/hub/webchannel_store.go` **and**
`pkg/hub/webchannel_store_postgres.go`. The postgres twin uses `$1`-style
placeholders and is the one most likely to have drifted. **Confirm the two
predicates are semantically identical**, arm for arm, including the guard.

A test exists (`TestPromoteDM_WildcardGuard_UnresolvedDirectConversation`).
**Re-run its mutation yourself** — delete the guard, confirm red, restore,
confirm green. Do not take the report's word for it.

### 2. U-TX-1 — the lookup must be outside the transaction

`GetConversationByExternalRef` touches the ambient pool. At `MaxOpenConns=1`
the transaction holds the only connection, so an ambient call between `BeginTx`
and `Commit` **deadlocks rather than fails**. `hasConversationsTable()` obeys
this at `webchannel_store.go:2106`.

Confirm the new lookup is called **before** `BeginTx`, in both stores. If you
write any test on this path, give it an explicit timeout — a violation hangs,
and a hang costs the evidence as well as the time.

### 3. `PromoteKeys` — no adjacent bare strings

The signature carries `DMKey` and `DirectConversationID`. Passed as two bare
`string` parameters, a transposition **compiles, runs, matches nothing, and
reports success** — it silently reintroduces the exact defect under repair.
Confirm the struct is used at every call site and that neither field is
positionally interchangeable with anything nearby.

### 4. Lookup only — promotion must never CREATE a direct conversation

On `store.ErrNotFound` the code must pass `""` and let the legacy arm work.
Confirm there is no `ResolveOrCreateDMConversation` or any create-on-miss on
this path. A direct conversation's `external_ref` **is** its access-control
basis; minting one during promotion would fabricate an ACL.

### 5. The direct conversation row is untouched (INVARIANT D-1)

Not deleted, not soft-deleted, not re-kinded, not re-participanted. A direct
conversation's participant set is immutable for its lifetime, and the DM must
resurrect by `external_ref` if the user messages the agent again. Check the
diff for any write to that row, and check that
`TestPromoteDM_NoConversationID_SkipsDualWrite` still exists.

### 6. Risky replace actions — the standing directive

**Scan the diff for anything that removes or weakens an existing assertion,
gate, or guard**, whether or not it looks harmless. Deletions are what I most
want a second pair of eyes on. Two known, both already cleared by me — confirm
they are what I think they are and flag anything else:

- `7ad4bcaa` — `t.Fatal` → `t.Error` plus an `if x != ""` wrap. I read this as
  changing failure *reporting* and not pass/fail. Verify.
- `handlers_read_switch_test.go` 1/1 — a mock signature updated for
  `PromoteKeys`.

The prohibition list applies. Do not accept any change touching:
`authenticatedSender`, `parseDMKeyIDs`, `isDMParticipant`, `checkDMParticipantKey`,
`EnsureParticipant` and its `left_at` preservation, the `direct` non-empty
`external_ref` predicate, or `Broadcasted` server-side forcing. **The invariant
is reachability: no messaging path may reach send without passing
`authorizeAgentMessage`.**

### 7. No new authorization surface, and nothing in `parent_ref`

`parent_ref` is caller-supplied from request JSON on two live paths. Confirm it
stays `''` on the promoted conversation and that no authorization decision
reads it. Confirm promotion authorization
(`handlers_chat_v2.go:2474-2508`) is unchanged, and human↔human promotion is
still refused.

---

## Independently re-run these

Do not accept the report's runs. The developer's four-direction matrix came back
clean on a nearly-inert fix earlier today; the run that caught it is AC-96-6a.

1. **AC-96-6a** — revert **only** the `WHERE` clause to `WHERE thread_id = ?`,
   keeping both SET columns. The AC-96-1 test must go **red** reporting missing
   messages. Expected signature: 2 of 5 moved.
2. **AC-96-1b** — delete the `? <> ''` guard. The wildcard test must go **red**.
3. Restore both, confirm green.

**A mutation that fails to compile is not a caught violation** — make it
compile-safe (`_ = x`) and re-run.

## Gates

```sh
export GOCACHE=/tmp/gocache-rev96       # the shared cache is contended
go build ./... > /tmp/r-build.log 2>&1; echo "build=$?"
go vet ./...   > /tmp/r-vet.log   2>&1; echo "vet=$?"
make compat-literals check-authz-guards check-conversation-upsert-guard check-security-marker-gates
gofmt -l <changed files>
```

No pipes — `$?` after a pipeline is the last command's status, and this shell is
zsh where `${PIPESTATUS[0]}` is empty.

**Do not run the full `pkg/hub` package.** `TestRS1_StaleAuthorityForcedOverlap`
runs 8+ minutes and times the package out; it is upstream's and it is with
`ci-fix-lead`. Run the promote-related tests by name.

Expected red and **not yours**: `gofmt` on `pkg/hub/handlers_agents_core.go` and
`pkg/hub/web_test.go`, and `TestMutationClassificationBidirectional` at 200
discovered / 198 classified. Both inherited from upstream main. **Do not
reformat those files.**

**Never make a gate pass by weakening the gate.** Any red is reported to me.

## Report

To `ca-msg-arch`, with: your own numstat and `merge-base --is-ancestor` line;
the three mutation runs raw; a verdict per numbered item above; and anything
you found that this brief did not ask about.

**If your findings contradict this brief, your findings are the finding.**
Report them; do not adjust your review to match my prose.

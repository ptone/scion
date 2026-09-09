# DEF-157 — `PromoteDM` is the fourth `INSERT INTO conversations` and it still writes `external_ref = ''`

You are implementing. Small change, high care. Read this whole brief before you
touch anything.

## Why this exists, and why it is nobody's mistake

Two branches were developed in parallel against the same base and merged with
**zero textual conflicts**. Both were correct in isolation. The merge is not.

- **DEF-156** (`ca-msg-unify`) exists because thread conversations were written
  with `external_ref = ''` and read via a derived `thread:<projectID>:<threadID>`.
  It converted **every** `INSERT INTO conversations` in the web-chat store to
  the derived key. At the time there were **three**: `CreateTopic`,
  `EnsureGeneralTopic`, `backfillTopicConversations`. It got all three, in both
  stores, with a golden-vector test. The enumeration was complete *for its base*.
- **DEF-96** (`ca-msg-promote`) added a **fourth**: `PromoteDM` now mints a
  topic-and-conversation pair, copied in shape from `CreateTopic` **as it stood
  before DEF-156**, hardcoding `external_ref = ''`. Also correct *for its base*.

After the merge, three sites write the derived key and one writes `''`.

Do not go looking for who got it wrong. Nobody did. A test can only assert over
the call sites in the tree it compiles against, so DEF-156's "every site derives
its key" test enumerates three because three is all it could see. The property is
not false on either branch. It becomes false at the merge.

## What actually breaks

A promoted thread becomes the **only** population still exhibiting DEF-156:

1. Its conversation row sits at `external_ref = ''`, outside the partial unique
   index (`external_ref <> '' AND deleted_at IS NULL`).
2. On the next boot, the message backfill (`BackfillService.persistGroup`)
   derives `thread:<projectID>:<topicID>` for its messages, finds no match, and
   **mints a shadow conversation**.
3. `backfillTopicConversations` cannot heal it — it selects
   `WHERE conversation_id IS NULL`, and a promoted topic already has one.

So the user promotes a DM, it looks right, and after the next hub restart the
thread's history is split across two conversations. That is DEF-156's exact
signature, which is the defect DEF-96 and DEF-156 were both dispatched to end.

## The four sites, measured

I ran this on the merged tree. Confirm it yourself on your base before you edit —
if your enumeration differs from mine, **your enumeration is the finding**:

```sh
for F in pkg/hub/webchannel_store.go pkg/hub/webchannel_store_postgres.go; do
  echo "### $F"
  for L in $(grep -n "INSERT INTO conversations" $F | cut -d: -f1); do
    awk -v n=$L 'NR<=n && /^func /{f=$0} NR==n{print "  FUNC: " f}
                 NR>=n && NR<=n+4 && /VALUES/{print "    " $0}' $F
  done
done
```

What I got (both stores identical in shape):

```
CreateTopic                → VALUES (?, ?, 'group', 'native', ?,  '', ?, ...)
EnsureGeneralTopic         → VALUES (?, ?, 'group', 'native', ?,  '', ..., ...)
backfillTopicConversations → VALUES (?, ?, 'group', 'native', ?,  '', ?, ...)
PromoteDM                  → VALUES (?, ?, 'group', 'native', '', '', ?, ...)   ← the defect
```

## The change

In `PromoteDM`, **both** `pkg/hub/webchannel_store.go` and
`pkg/hub/webchannel_store_postgres.go`:

```go
// before BeginTx — U-TX-1
extRef, err := messaging.ThreadConversationExternalRef(topic.ProjectID, topic.ID)
if err != nil {
    return nil, fmt.Errorf("webchat store: derive conversation key for promoted topic %s: %w", topic.ID, err)
}
```

then pass `extRef` where the literal `''` is today.

`topic.ProjectID` and `topic.ID` are both in scope and already used by the topic
INSERT a few lines above.

Follow `CreateTopic`'s post-DEF-156 shape for the derivation and the error
wrapping. **Use `messaging.ThreadConversationExternalRef`. Do not write
`fmt.Sprintf("thread:%s:%s", ...)`.** The single derivation function is the
whole point of DEF-156 P1, and a second speller of the same string is how this
class of defect regenerates.

### Three rulings, so you do not have to guess

**1. On a derivation error, refuse. Never fall back to `''`.**
An empty `projectID` or `topicID` makes `ThreadConversationExternalRef` return an
error. Return it. Do not log-and-continue, do not substitute `''`. Falling back
would produce exactly the row this change exists to eliminate, and it would do so
silently on the input most likely to be malformed. **Under-doing an operation is
recoverable; over-doing it is not** — a refused promotion costs the user a retry,
a committed one with a `''` ref costs them a split thread they cannot see.

**2. Do NOT add a pre-mint lookup. Let the unique index refuse.**
`CreateTopic` has one (DEF-156 P2) because the message backfill may legitimately
have minted that conversation already. `PromoteDM` is different: `topic.ID` is a
fresh UUID minted by the handler at request time, so no message can yet carry
that thread ID and no conversation can carry that ref. Adding a lookup would add
a *reuse* branch to a path where reuse is the wrong answer — if a row with that
ref somehow exists, the safe response is to fail, not to adopt it. The partial
unique index on `(surface, external_ref)` gives that for free, inside the
transaction, which rolls back.

**But that argument is a claim, and I want it tested, not asserted** — see
AC-157-4. If the index turns out not to exist in the path this code actually
runs against, come back to me before writing a lookup. That decision is mine.

**3. Leave `surface` as `'native'`.** DEF-156 derives surface from channel in the
*backfill*, where the channel varies. A promoted DM is a native web-chat thread.
Changing it here is out of scope. If you find a way for a non-native DM to reach
`PromoteDM`, report it — do not fix it.

## Acceptance criteria

- **AC-157-1** — All four `INSERT INTO conversations` sites in **both** stores
  write a derived `external_ref`. Zero hardcoded `''` in the `external_ref`
  column position. Prove it with the enumeration command above, pasted raw.
- **AC-157-2** — A promotion creates a conversation whose `external_ref` equals
  `messaging.ThreadConversationExternalRef(projectID, topicID)`. Assert against a
  **literal expected string** as well as against the function, the way DEF-156's
  golden-vector test does. Asserting only `got == f(x)` passes even if `f` is
  wrong.
- **AC-157-3** — The round trip: after promotion, resolving
  `thread:<projectID>:<topicID>` returns the **same** conversation ID the promoted
  topic points at. This is the assertion that actually encodes "no shadow" — it
  is the one to write first and the one to mutate against.
- **AC-157-4** — Establish, and state in your report, whether the partial unique
  index on `(surface, external_ref)` exists in the schema `PromoteDM` runs
  against in production **and** in the test harness. Answer with the migration or
  schema line, not with an inference from the ent schema file. If it is absent in
  either, say so plainly — that changes ruling 2 and it is my call, not yours.
- **AC-157-5** — U-TX-1: the derivation and any ambient-pool call happen
  **before** `BeginTx`, in both stores. At `MaxOpenConns=1` a violation **hangs**
  rather than fails, so any test on this path carries an explicit timeout.
- **AC-157-6** — DEF-96's existing promote tests still pass unchanged. If you
  need to edit one, stop and tell me which and why. **Never make a gate pass by
  weakening the gate.**

## Mutations — run these, paste them raw

A green means nothing until you have seen the red.

1. Revert `extRef` → `''` in the **sqlite** `PromoteDM`. AC-157-3 must go red.
2. Revert `extRef` → `''` in the **postgres** `PromoteDM`. Whatever covers pg
   must go red — and if nothing does, **that is a finding, report it**; do not
   quietly skip the mutation. DEF-99 says `pgWebChatStore` has zero coverage, so
   I expect this one to expose a gap rather than a failure.
3. Make the derivation error path fall back to `''` instead of returning.
   Something must go red.
4. Restore all three, confirm green with `-v -count=1` and **count the
   `--- PASS:` lines**. A `-run` pattern that matches nothing prints `ok` and
   exits 0; a filtered green is not evidence unless you can name the tests.

**A mutation that fails to compile is not a caught violation.** Make it
compile-safe (`_ = x`) and re-run.

## Gates

```sh
export GOCACHE=/tmp/gocache-def157
go build ./... > /tmp/b.log 2>&1; echo "build=$?"
go vet ./...   > /tmp/v.log 2>&1; echo "vet=$?"
go build -tags no_sqlite ./pkg/hub/ ; echo "notag=$?"
make compat-literals check-authz-guards check-conversation-upsert-guard check-security-marker-gates
gofmt -l pkg/hub/webchannel_store.go pkg/hub/webchannel_store_postgres.go
```

No pipes — this shell is **zsh**, `$?` after a pipeline is the last command's
status and `${PIPESTATUS[0]}` is empty. Quote your globs (`--include="*.go"`).

**Do not run the full `pkg/hub` package.** Measured on a clean base: **35 minutes
wall**, ending in `panic: test timed out`, because
`TestRS1_StaleAuthorityForcedOverlap` alone takes 23m37s. It is upstream's and it
is with `ci-fix-lead`. Run your tests by name.

Inherited red, **not yours, do not fix**: `gofmt` on
`pkg/hub/handlers_agents_core.go` and `pkg/hub/web_test.go`;
`TestMutationClassificationBidirectional`. Any *other* red comes to me.

If you add a new test file that opens sqlite, it **must** carry
`//go:build !no_sqlite` below the 14-line Apache header, with a blank line before
`package hub`. Check with `grep`, never `head -n`. Do **not** strip that tag from
anywhere to make something run.

## Branch and push

Your base is **`scion/ca-msg-stage157` @ `6c32ff491`**, not `tranche-g`.

That branch is the merge commit itself: `tranche-g` (`f4326b727`, carrying DEF-96)
with `scion/ca-msg-unify` (`2fb44cf75`, DEF-156) merged in. It exists precisely
because I would not put this defect on `tranche-g`, which is what gteam tests
from — and the very next thing we are asking for on gteam is a **promotion**
re-test, which is the one action that triggers DEF-157. `tranche-g` advances only
once your fix is reviewed and in.

Fetch and confirm before reading anything:

```sh
export GITHUB_TOKEN=$(cat /scion-volumes/scratchpad/transition-github-token.txt)
git -C /workspace fetch -q "https://x-access-token:${GITHUB_TOKEN}@github.com/ptone/scion.git" \
    scion/ca-msg-stage157:refs/rt/st157 --force
git -C /workspace worktree add --detach /tmp/wt-157 refs/rt/st157 -q
git -C /workspace rev-parse --short refs/rt/st157        # expect 6c32ff491
git -C /workspace merge-base --is-ancestor f4326b727 refs/rt/st157 && echo "tranche-g: ancestor OK"
git -C /workspace merge-base --is-ancestor 2fb44cf75 refs/rt/st157 && echo "unify:     ancestor OK"
```

Both ancestor checks must print. If either does not, stop and tell me — you are
not on the tree this brief describes and nothing below applies.

`origin` in your container does **not** point at ptone/scion. Push with:

```sh
export GITHUB_TOKEN=$(cat /scion-volumes/scratchpad/transition-github-token.txt)
git push "https://x-access-token:${GITHUB_TOKEN}@github.com/ptone/scion.git" \
    HEAD:refs/heads/scion/ca-msg-promoteref
```

Then verify separately — a push that prints nothing is not a push that landed:

```sh
git ls-remote "https://x-access-token:${GITHUB_TOKEN}@github.com/ptone/scion.git" \
    scion/ca-msg-promoteref
```

**Never push to `main` or to `tranche-g`.** You do not open PRs. I merge.

Your working tree may be shared. **Never `git add -A`** — stage by explicit path.

## Report

To `ca-msg-arch`: your `merge-base --is-ancestor` line, per-file numstat, the
enumeration output, all four mutation runs raw, a verdict per AC, and the head
SHA you pushed and verified.

**If your findings contradict this brief, your findings are the finding.**
Report them; do not adjust your work to match my prose.

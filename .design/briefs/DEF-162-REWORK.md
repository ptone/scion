# DEF-162 rework — replace two tautological tests with real ones

You are a **developer** agent. Fresh context. A previous agent implemented
DEF-162 and its container is gone; the work is pushed and intact. Your job is a
narrow, well-specified rework of **two tests**. Do not redesign anything.

## Read first

1. `/scion-volumes/scratchpad/projects/ca-msg-arch/DEF-162-DESIGN.md` — the spec.
2. `/scion-volumes/scratchpad/projects/ca-msg-arch/briefs/_GATE-APPARATUS.md` —
   standing verification rules, binding.

## Base

Branch `scion/ca-msg-def162` @ `f459328ed`. Continue on that branch.
Its base is `scion/tranche-g` @ `e5b651719`.

Fetch it explicitly — `origin` in your container is the wrong repo:

```sh
export GITHUB_TOKEN=$(cat /scion-volumes/scratchpad/transition-github-token.txt)
git fetch "https://x-access-token:${GITHUB_TOKEN}@github.com/ptone/scion.git" \
  scion/ca-msg-def162
git checkout -B scion/ca-msg-def162 FETCH_HEAD
```

## What is already accepted — do not touch it

The production change (`pkg/hub/handlers_agent_messaging.go:920-944`) is
**accepted as written**, including its comment. Do not edit it except as a
temporary mutation that you restore.

These tests are accepted: `AC1`, `AC2`, `AC3`, `AC4`, `AC5`, both `AC6` tests,
and both `AC8` tests. `TestDEF162_AC8_Broker_MentionFires` in particular is
well built — real `InProcessEventBus`, real `MessageBrokerProxy`, real
subscription, asserts persistence as well as notification. Leave it alone.

## The defect you are fixing

`handlers_outbound_def162_test.go` contains this shape at **:361** and **:494**:

```go
guardPasses := threadID != "" && !hasPrefix(threadID, "dm:") && !hasPrefix(threadID, "agent:")
assert.False(t, guardPasses, ...)
```

using a test-local `hasPrefix` defined at **:527**.

This is a copy of the production condition, written in the test file, asserted
against itself. **Delete the guard at `handlers_agent_messaging.go:934` entirely
and both assertions still pass**, because neither executes that line. It reads
as coverage and is not.

The previous agent's mutation run already showed this: removing the `dm:` clause
produced no red. That was the guard reporting it is uncovered.

Also at the end of the `agent:` test:

```go
if len(notifs) > 0 { t.Log("confirmed: ...") }
```

A `t.Log` inside a conditional is not an assertion; that test cannot fail. What
it demonstrates — that `fireHumanMentionNotifications` called directly with an
`agent:` key *does* notify — is genuinely useful and proves the guard is
**necessary**. Keep that. It is not the same claim as the guard being
**present**, and only the second one protects us.

## Required work

### 1. AC-7 driven through the handler

The reachable production path is the conv-ref DM backfill at
`handlers_agent_messaging.go:773-786`: on the `def152DerivedRecipient` path with
`req.ThreadID == ""`, the handler sets `req.ThreadID` to the conversation's DM
key. That is how a `dm:`-prefixed `ThreadID` reaches the mention guard at `:934`.

Write the test to go through it: create a **direct** conversation, POST with
`ConversationRef: "conv:<uuid>"` and **no** `ThreadID`, and an unambiguous human
mention in the body. Assert **zero** mention notifications.

Then mutate: delete `&& !strings.HasPrefix(req.ThreadID, "dm:")` from `:934` and
show the test goes **RED**.

**If it does not go red, stop and report.** That would mean the path is not what
the design and I both believe it is, and I need to know before this merges.

### 2. AC-9 driven through the handler

`ThreadID` is caller-settable (`OutboundMessageRequest`, `thread_id` at `:45`),
so post one with `ThreadID: "agent:<agent-id>"`.

The previous agent wrote that the handler "requires channel validation infra
that is orthogonal." Set `Channel` as well and try. If it still cannot be driven
end to end, **report the exact error you hit** and wait — I will rule on a
substitute. Do not substitute a re-implementation of the condition; that is the
precise failure being fixed.

Then mutate: delete `&& !strings.HasPrefix(req.ThreadID, "agent:")` and show RED.

### 3. Delete the dead scaffolding

Remove `hasPrefix` (`:527`) and both `guardPasses` blocks.

## Method constraints

1. **Every symbol you name in a report carries `file:line`.**
2. **`grep -c` to confirm each mutation actually applied** before trusting a
   result. A mutation that fails to apply also produces a green.
3. **Restore after every mutation**; finish on a clean tree and a full green.
4. **Never make a gate pass by weakening the gate.** Any red comes to me.
5. **Do not put a result in a mutation table that the mutation did not produce.**
   A blank row costs me less than a wrong one. If a mutation does not go red,
   that is a finding — report it as one.
6. **Do not strip `!no_sqlite` build tags.**
7. **Do not `git add -A`.** Shared workspace, named paths only.
8. `GOCACHE=/tmp/gocache-162b`.
9. **Never run the full `pkg/hub` package** — a clean run is ~35 min and ends in
   a timeout panic. Use `-run 'TestDEF162'`.
10. A `-run` pattern matching nothing prints `ok` and exits 0. Confirm it matched.
11. **State the counting rule with every pass count.** `grep -cE '^--- PASS:'` is
    top-level; `grep -cE '^ *--- PASS:'` includes subtests.
12. `gofmt -l` clean on touched files. Pre-existing, not yours:
    `pkg/hub/handlers_agents_core.go`, `pkg/hub/web_test.go`.

## Report to me (`ca-msg-arch`)

- Branch and SHA; `git diff --numstat e5b651719 HEAD` per file.
- Test command verbatim; counts with the counting rule.
- **Both mutations**, with the `grep -c` confirmation that each applied.
- `gofmt -l` and `go vet`.
- Anything you found that is not in this brief.

## Push

```sh
git push "https://x-access-token:${GITHUB_TOKEN}@github.com/ptone/scion.git" \
  HEAD:refs/heads/scion/ca-msg-def162
```

Verify separately:

```sh
git ls-remote "https://x-access-token:${GITHUB_TOKEN}@github.com/ptone/scion.git" \
  scion/ca-msg-def162
```

Never push to `scion/tranche-g` or `main`. I do the merge.

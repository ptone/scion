# DEF-158 — `conv:<uuid>` persists a message that no read path can see

Base: `scion/tranche-g` @ `f38f3ba18`. Read `_GATE-APPARATUS.md` in this
directory first and follow it — it is the authority on build tags, timeouts and
gate scope, and it overrides anything in this brief that contradicts it.

## The defect

**`Channel=""` means "fan out to all spokes" on the write path and "match
nothing" on the read path.**

Write side, `pkg/hub/handlers_agent_messaging.go:238`:

```go
if req.Channel == "" && recipientID != "" && wcsAffinity != nil && ... {
    // reply-affinity lookup sets req.Channel
}
```

Its own comment: *"If no row exists, leave channel empty so the message fans out
to all spokes (today's default behavior)."*

On the `conv:<uuid>` path `recipientID` is empty at :238 — it is not derived from
the DM key until **:569**, in the DEF-152 block, 330 lines later. So the guard
fails, affinity is skipped, and `Channel` stays `""`.

Read side, `pkg/hub/handlers_chat_v2.go`, in `handleConversationHistory`:

```go
filter = store.MessageFilter{Channel: "web", ConversationID: convResult.ConversationID}  // switch ON,  :107
filter = store.MessageFilter{Channel: "web", ThreadID: key}                              // switch OFF, :132
```

`store.MessageFilter.Channel` is an exact match (`message.ChannelEQ`,
`pkg/store/entadapter/message_store.go:334`). A row with `Channel=""` is
excluded from **every** history query, on both sides of the switch.

Net: the message is persisted with the **correct** `conversation_id` and the
**correct** derived `recipient_id`, returns HTTP 200, the CLI exits 0 — and no
read path will ever return it. `ThreadID` is empty too, so no unread watermark
(`TouchDMActivity`) and no notification (`NotifyDMReceived`) fire either. The
recipient gets no signal of any kind.

This is user-visible message loss with a success return. Treat it accordingly.

## Rulings — these are decided, do not relitigate them

**R1. Do not weaken the read filter.** `Channel: "web"` on the history endpoint
stays exactly as it is. Its comment (*"G3-d: preserve Channel:'web' — this
endpoint serves the web"*) is deliberate. Widening a read surface to compensate
for a write-path bug is the wrong direction and would surface non-web messages
in web history. **This is the tempting one-line fix and it is rejected.**

**R2. Channel validation must run after Channel is finally set — on every path.**
Immediately after the affinity block there is a validation step ("Validate
channel against registered channels. Fail closed…"). **The obvious fix —
hoisting the affinity block down past :569 — would leave validation running
against `""` and then set a channel that is never validated.** That is a
fail-open, and it is worse than the bug you are fixing. Whatever ordering you
land, state explicitly in your report which line sets Channel last and which
line validates it, and show that the validator is downstream.

**R3. Recipient derivation stays security-preserving.** The :569 block derives
the addressee from the conversation's DM key, never from request input — the
comment marks it SECURITY and it is load-bearing. Do not add a path that takes
the recipient from `req`. Do not widen it to non-`direct` conversations; the
group case must keep failing closed.

## Approach — investigate, then recommend before you write the fix

Two shapes are plausible and I am not ruling between them sight-unseen:

- **(A) Resolve the recipient before channel affinity.** One ordering for all
  paths: recipient → channel → validate. Structurally correct, but it requires
  the conversation-ref resolution at :339 to move above :238 as well, so the
  blast radius is larger than it first appears.
- **(B) Re-run affinity + validation after :569, guarded on `Channel == ""`.**
  Smaller and more local, at the cost of a second invocation.

Read both, then **message me with your recommendation and the reason before
implementing.** I would rather spend one round-trip than review the wrong shape.
If you find a third option that satisfies R1–R3, propose it.

## Scope

In scope: the empty-`Channel` defect on the conv-ref path.

**`ThreadID` is a second gap on the same path** — empty, so no watermark and no
notification. Fixing Channel makes the message *visible when someone looks*;
fixing ThreadID is what makes them *know to look*. Assess it, and tell me
whether it belongs in this change or a separate one. If it is riskier than the
Channel fix, say so and I will split it. Do not silently bundle it.

Out of scope: the CLI's argument grammar (`args[1:]` joined into the body), the
`user:<email>` vs UUID error text, and the missing group-reply syntax. Those are
tracked under DEF-158 and are not yours.

## Acceptance criteria

- **AC-1** A message sent via `conv:<uuid>` to a **direct** conversation, with no
  explicit recipient, is persisted with a non-empty `Channel`.
- **AC-2** That same message is **returned by `handleConversationHistory`**.
  Assert on the query result, not on the stored row. The whole defect is that
  those two differ — a test that only checks persistence would have passed
  before your fix.
- **AC-3** The **group** conv-ref case still fails closed with the existing 400.
  No change in behaviour.
- **AC-4** The channel validator still rejects an invalid channel on the conv-ref
  path (R2). Prove it with a test that sets an unregistered channel.
- **AC-5** Existing DEF-138/142/152 tests unchanged and green.

**Coverage note that explains how this survived:** the DEF-142 suite
(`handlers_outbound_def142_test.go`) **always** passes an explicit `Recipient`
alongside the `ConversationRef`. The no-recipient path — the only path the CLI
actually produces — has never had a test. Your regression test must cover the
no-recipient path specifically, or it will pass for the same reason the existing
suite does.

There is a probe file from the investigation at
`pkg/hub/handlers_outbound_def158_probe_test.go` on branch `scion/ca-msg-def158`.
Reuse what is useful; the probes assert on the stored row, so they do **not**
satisfy AC-2 as written.

## Mutation testing — required, in both directions

Do not report green without this. For each of AC-1 and AC-2, revert your fix,
confirm the test goes **red**, restore it, confirm **green**. A test that passes
against the unfixed code is not a regression test.

Verify each mutation actually applied — re-read the file and confirm the change
is present before trusting the run. A mutation that fails to apply also produces
a green, and that has bitten this project.

## Reporting and push

Report per-file `git diff --numstat` against `f38f3ba18`, the full `go vet ./...`
output, and your mutation results.

Never make a gate pass by weakening the gate. Any red comes to me with the full
log, not tuned away. A real failure is more welcome than a tuned-green one.

`origin` in your container does **not** point at ptone/scion:

```sh
export GITHUB_TOKEN=$(cat /scion-volumes/scratchpad/transition-github-token.txt)
git push "https://x-access-token:${GITHUB_TOKEN}@github.com/ptone/scion.git" HEAD:refs/heads/scion/ca-msg-def158fix
git ls-remote "https://x-access-token:${GITHUB_TOKEN}@github.com/ptone/scion.git" refs/heads/scion/ca-msg-def158fix
```

Never push to `main` or to `tranche-g`. I do the merge.

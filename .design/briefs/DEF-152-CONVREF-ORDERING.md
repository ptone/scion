# DEF-152 — `conv:<uuid>` from an agent is rejected before it reaches the resolver

**Base branch:** `scion/tranche-g` @ `a29d75f77`
**Your branch:** `scion/ca-msg-convref`
**Priority:** high — this is the headline feature of DEF-142 and it does not work in production.

## The observed failure

Reported by ptone from inside an agent container on gteam, CLI `dev (commit a29d75f7)` (branch tip — this is NOT version skew):

```
Error: failed to send message to conv:6d0f17f6-41be-4f0d-bcfa-861a958b3e06:
validation_error: recipient is required — specify a user with 'user:<name>' or 'user:<email>' (status: 400)
```

## Root cause — an ordering defect

In `pkg/hub/handlers_agent_messaging.go`, `handleAgentOutboundMessage`:

- **line 210** — guard: `if recipientID == "" && recipient == "" { ValidationError("recipient is required …") }`
- **line 334** — `if req.ConversationRef != "" { … messaging.Resolve(…) }`

The `conversation_ref` resolver sits **124 lines after** the guard that rejects the request. A
`conv:` request carries no recipient *by design* — the conversation is the address — so it can
never reach the resolver it was built to reach.

Client side confirms the shape. `cmd/message.go:677` `sendMessageViaConversation` sets
`Recipient` **only** when `ref.Kind == messaging.RefEmail`:

```go
outMsg := &hubclient.OutboundMessageRequest{ Msg: …, Type: "instruction", Urgent: …, ConversationRef: ref.Raw }
if ref.Kind == messaging.RefEmail { outMsg.Recipient = "user:" + ref.Value }
```

So every non-email ref kind posts with an empty recipient.

## Why no test caught it

Every DEF-142 test passes a recipient *alongside* the conversation ref. The helper in
`pkg/hub/handlers_outbound_def142_test.go:42` hardcodes it:

```go
body, _ := json.Marshal(OutboundMessageRequest{
    Recipient:       "user:" + recipientEmail,   // <-- always present
    Msg:             msg,
    ConversationRef: convRef,
})
```

The guard is therefore always satisfied in tests and the resolver is always reached. The suite
validated the resolver in isolation but never the addressing mode the feature exists to provide.
**A test that supplies both the new address and the old one has not tested the new address.**

## Scope of the break — verify, do not assume

`conv:<uuid>` is confirmed broken. `#<thread>` takes the same no-recipient path and is very
likely broken identically. `@agent` also sets no `Recipient` — **determine whether it reaches
this endpoint at all** before claiming anything about it. Report what you find for all three;
do not generalise from one.

## Required fix shape

Preferred: **keep `messaging.Resolve` exactly where it is (line 334) and relax the line-210
guard**, so that request ordering relative to the DEF-138 authorization block is unchanged.
Moving resolution earlier reorders it against authz and is not acceptable without a design
review from me.

Concretely:

1. The guard's condition becomes "no addressing information *at all*" rather than "no recipient".
   A request with neither `recipient` nor `conversation_ref` **must still 400 with the same
   message.** Do not delete the guard.
2. After resolution succeeds, the handler must **derive the addressee from the resolved
   conversation** and populate `Recipient` / `RecipientID`. Today those are read at ~line 280
   when building the message; if they stay empty the message may resolve and then deliver
   nowhere. Confirm end-to-end delivery, not just a 200.
3. Re-check each use of `recipientID` between 210 and 334 tolerates empty. Current reading:
   the DM `thread_id` check (:219) is gated on `req.ThreadID != ""` and the reply-affinity
   lookup (:233) on `recipientID != ""`, so both skip — **verify this rather than trusting it.**

## Hard constraints — security relevant

- Derive the addressee from the conversation's **participant set**. Do **not** weaken
  `isDMParticipant`, do **not** make it tolerant of non-UUID principals, and do **not**
  normalise a DM key anywhere on the derivation path. A differing round-trip is an error,
  never a rewrite.
- A non-`direct` conversation may have no single recipient. Handle that case **explicitly**
  with a clear refusal. Do not default, do not guess, do not pick the first participant.
- Sender identity stays derived from the authenticated caller (G-1). Nothing here may make
  any part of the envelope bindable from request JSON.
- Under-granting is recoverable; over-granting is not. If the addressee cannot be determined
  unambiguously, **fail closed.**
- Never make a gate pass by weakening the gate. Any red goes to me, not tuned away.

## Tests you must add

1. A hub test posting `conversation_ref` with **no recipient at all** — the exact production
   shape — asserting 2xx and correct routing. This is the test the suite was missing.
2. The same for `#<thread>`.
3. Negative: neither recipient nor conversation_ref → still 400, message unchanged.
4. Negative: a conversation the sender is not a participant of → refused, no disclosure of
   project IDs in the error.
5. Fix the existing helper, or add a sibling helper, so the no-recipient shape is the default
   for ref-based tests rather than the exception.

New sqlite-dependent test files need the `//go:build !no_sqlite` tag (line 15, below the
14-line Apache header). **Do not strip build tags to make tests run.**

## Gates

`make ci` and **`go vet ./...`** (mandatory — `make test-fast` excludes 31% of test files and
will not catch a broken test compile). Report full output and per-file `git diff --numstat`.

## Push

```
export GITHUB_TOKEN=$(cat /scion-volumes/scratchpad/transition-github-token.txt)
git push "https://x-access-token:${GITHUB_TOKEN}@github.com/ptone/scion.git" HEAD:scion/ca-msg-convref
```
`origin` in your container points at a **different** repo — use the command above verbatim, then
verify with `git ls-remote`. Do not open a PR. Do not push to `main` or `scion/tranche-g`.

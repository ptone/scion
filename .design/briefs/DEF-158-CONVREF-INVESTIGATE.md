# DEF-158 — `conv:<uuid>` addressing returns exit 0 and delivers nothing

**You are investigating. Do not fix anything.** Produce findings; I decide what
gets changed and by whom. If you find yourself editing a non-test file, stop.

## What happened

ptone ran an agent against gteam on `f38f3ba1` (our `tranche-g`). It tried six
ways to reply into a group conversation. The raw report is at
`/scion-volumes/scratchpad/.attachments/_discord/discord_1788993847363148184_issue-report-group-conv-replies.md`
— **read it first, in full.** Summary of its claims:

| # | Command | Claimed result |
|---|---|---|
| 1 | `scion message @user:<uuid> "m"` | error: `conversation_ref could not be resolved` |
| 2 | `scion message "conv:<G>" "m"` | error: `group conversations require an explicit recipient — add 'user:<email>' alongside the conversation_ref` |
| 3 | `scion message "group[conv:<G>,user:<U>]" "m"` | error: `unknown recipient prefix "conv" in group[] element` |
| 4 | `scion message "user:<uuid>" "m"` | **sent**, but to the user's DM, not the group |
| 5 | `scion message "conv:<G>" "user:<U>" "m"` | **exit 0, never delivered** |
| 6 | `scion message "conv:<D>" "user:<U>" "m"` (direct conv, also with `--attach`) | **exit 0, never delivered** |

`<G>` = `fd9710a1-056d-4a0c-bff6-505b76feb324` (group),
`<D>` = `6d0f17f6-…` (direct), `<U>` = `b53249ea-b8ce-4e75-99f1-883ec0f5e967`.

**Rows 5 and 6 are why this is Critical.** Silent success on total delivery
failure: the agent believes it replied, so it never retries, and the loss surfaces
only when a human says "I never got that."

## Two things I established myself — confirm or refute them, do not assume them

**A. The CLI joins every argument after the first into the message body.**
`cmd/message.go` ~line 146: `message = strings.Join(args[1:], " ")`, with
`Args: cobra.MinimumNArgs(1)` and `Use: "message [recipient] <message>"`. So row
5 sends to `conv:<G>` with the body `"user:<U> m"`. The second "recipient" was
never parsed as one — it was swallowed into the text.

**B. Our own error text instructs exactly that syntax.** The row-2 error is
raised server-side at `pkg/hub/handlers_agent_messaging.go:638` and says *"add
`user:<email>` alongside the conversation_ref"*, which reads as "pass another
argument." Following our guidance silently eats the recipient. The report's
point 2 compounds it: inbound messages carry only UUIDs and the error demands an
email, so an agent cannot comply even in principle.

If either of these is wrong, **your finding is the finding** — say so plainly.

## The questions, in priority order

### Q1 (highest) — where do the row 5/6 messages actually GO?

"Silently dropped" is three different defects with three different severities and
three different fixes. Distinguish them with evidence:

- **(a)** never persisted at all — the message is gone;
- **(b)** persisted, but into a conversation nobody reads (wrong `conversation_id`,
  orphaned, or a freshly minted shadow);
- **(c)** persisted correctly but never dispatched/fanned out.

(b) is recoverable data and a *display* problem. (a) is loss. **(b) is also a
potential disclosure**, see Q5. Answer with rows, not inference.

### Q2 — why do rows 2 and 5 differ at all?

Same recipient token, same conv ref. One errors from the hub, one exits 0. The
only difference is the message body. **Something is branching on something it
should not**, or the report is imprecise about what was run. Both are worth
knowing. Trace `cmd/message.go` → `sendMessageViaConversation` (line ~351) →
the hub handler, and find the branch. Do not guess.

### Q3 — is this ours?

**This is the question that decides whether it blocks the branch.** `conv:`
addressing is DEF-138 territory; the work we just landed (DEF-96/156/157) is
backfill and topic-conversation plumbing. My expectation is that this predates
`tranche-g`, but expectation is not evidence.

Establish it against baselines. At minimum: `tranche-g` (`f38f3ba18`), the
`tranche-g` base (`77ebea1f6`), and upstream `main`. A `git log -S` on the
relevant symbols across those three, plus the code path at each, will settle it
faster than reasoning about intent. **State the method you used and its limits.**

### Q4 — what is the intended contract?

Read `/scion-volumes/scratchpad/projects/ca-msg-arch/.design/ADDRESSING-SPEC.md`
(or `.design/ADDRESSING-SPEC.md` in the repo worktree) and
`DEF-138-DESIGN.md`. Report **where the code disagrees with the spec**, in both
directions — spec says X and code does Y, and code does Z that the spec never
mentions.

Standing rule on this project: **where a design document disagrees with the code,
the code wins the test and the disagreement gets reported.** Do not "fix" the
spec by reinterpreting it.

### Q5 — is there an authorization hole? Check this even though nobody asked.

`conv:<uuid>` lets a caller name a conversation directly. Two things must hold
and I want them verified, not assumed:

- **AC-INGRESS-1**: a message may not be written with a conversation key that
  does not name the authenticated sender. Can an agent write into a conversation
  it is not a participant of by naming its UUID?
- **G-1**: sender identity is always derived from the authenticated caller and
  `ConversationAsserted` must never be bindable from request JSON.

If row 5/6 messages are landing *somewhere* (Q1b), the question of **whose**
conversation that is becomes urgent. Report anything here to me immediately and
separately — do not batch it into the final report.

### Q6 — the UUID/email gap

Is there any way for an agent to resolve a user UUID to an email? The report says
no. Confirm, and note what *is* available. This shapes the fix but is the lowest
priority of the six.

## Method constraints

**Reproduce locally, not on gteam.** Build a local hub or use the test harness.
Do not send test messages to real users, and do not create conversations on
gteam.

If you need to read gteam to understand the shape of `<G>` and `<D>`, use a
**read-only** connection (`file:...?mode=ro`). Conversation IDs, kinds, surfaces,
`external_ref`s and participant *IDs* are keys and are fine to report. **Do not
report message bodies. Do not dump email addresses or display names in bulk.
Never read the `value` column of `hub_settings`** — the Discord bot token is in
it. Change nothing on gteam; conversations `0c57b491` and `b2fd01b6` are ptone's
preserved reproduction.

Shell is **zsh**: quote your globs (`--include="*.go"`), `${PIPESTATUS[0]}` is
empty, and `$?` after a pipeline is the last command's status.

**Do not run the full `pkg/hub` package** — 35 minutes wall, ends in
`panic: test timed out`, `TestRS1_StaleAuthorityForcedOverlap` alone is 23m37s.
Run by name.

If you write a probe test to establish behaviour, that is fine and encouraged —
it is evidence. Do not commit a fix. If you add a test file, see
`_GATE-APPARATUS.md` § `//go:build !no_sqlite` before adding any build tag; the
rule is narrower than the convention suggests and adding the tag wrongly removes
your test from the only blocking gate.

## Report

To `ca-msg-arch`. Structure it as: **Q1–Q6, each with a verdict and the evidence
that supports it**, then anything you found that this brief did not ask about.

For every claim, give me the command or the file:line. A mechanism reported
without its evidence cannot be contradicted, only disbelieved — it occupies the
space where evidence should be and sends me hunting a reconciliation that does
not exist.

**Raise Q5 immediately if it is positive.** Everything else can wait for the
report.

Base: `scion/tranche-g` @ `f38f3ba18`. `origin` in your container does **not**
point at ptone/scion. You are not pushing production code; if you push probe
tests, push to `scion/ca-msg-def158` and never to `main` or `tranche-g`.

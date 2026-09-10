# DEF-160 / DEF-161 — group conversation replies

Base: `scion/tranche-g` @ `2519aa8b3`. Branch: `scion/ca-msg-def160fix`.

**Read first, in this order:**
1. `_GATE-APPARATUS.md` in this directory — authority on build tags, timeouts,
   gate scope, baselines. Overrides anything here that contradicts it.
2. `.design/DEF-160-DESIGN.md` in the repo worktree — the design you are
   implementing. This brief does not restate it; it adds the rulings and the
   traps.

## The contract, ruled by ptone — do not relitigate

> *"group conv:id is the recipient. message should require no more than required
> to clearly route. and error clearly with what is required if not met. an agent
> can choose to mention a recipient in message text if they want that recipient
> to be drawn to a group message."*

`scion message conv:<group-uuid> "text"` must work with **no** second addressee.

Note `kind` is a two-value enum (`pkg/ent/schema/conversation.go:50-51`) — there
is no `topic` kind, so **every topic conversation is `kind=group`**. This is the
main web chat surface, not an edge case. Size your care accordingly.

## Rulings — decided, do not relitigate

**R1. Match the web path's row shape; do not invent one.**
`handlers_chat_v2.go:1437-1439` already writes, for topic messages:
`recipient = "thread:" + key`, `recipientID = key`. Agent-authored group messages
must be indistinguishable from human-authored ones. This is the same move DEF-158
made for DMs.

**R2. Do not validate a supplied recipient against the participant set.**
`resolve.go:512-517` is explicit: *"Participants are a LISTING concern, not an
access concern."* Participant writes are best-effort with swallowed failures.
Gating a send on that table turns every listing gap into an outage. This was my
own first instinct and it is **rejected** — if you find yourself reaching for
`AddParticipant` or a participant query on the send path, stop and message me.

**R3. Do not weaken the read filter.** Same as DEF-158 R1. `Channel: "web"` on
`handleConversationHistory` stays exactly as it is.

**R4. Provenance decides validation.** A surface-derived channel is **not**
caller input and must not be broker-validated — that was regression G-4 on
DEF-158 and it turned a broker-less hub (a supported config) into a 503. Read
the comment block at `handlers_agent_messaging.go:723+` before touching
validation order. State in your report which line sets Channel last and which
validates it.

## P0 is a prerequisite and it is not optional

`ParseThreadConversationExternalRef` **does not exist**. I checked. The forward
helper is `pkg/messaging/derive_key.go:119`; the only code that decomposes a
`thread:` ref today is ad-hoc `TrimPrefix` at `divergence.go:425-428`.

Do **not** hand-roll a split at the call site. The forward helper's own comment
says two independent constructions of this format is the exact defect DEF-156
fixed; an uncentralised deconstruction is that defect mirrored. Write the
inverse next to the forward one, per the design §4.1, with **one shared golden
vector table driving both directions** and the `dm:` refusal mirrored from
`derive_key_test.go:738`.

Land P0 as its own commit with no callers. It reviews on its own.

## Why the DEF-142 suite matters here

`handlers_outbound_def142_test.go` uses **seven** group conversations and
**always** supplies `Recipient: "user:"+email` alongside `ConversationRef`
(lines 45, 66, 557 supply recipients; 109, 142, 187, 230, 285, 544 are group).

That suite is why the design says *overwrite* a supplied group recipient rather
than *reject* it — rejection turns 13 tests red at once and mixes a contract
change into a security fix.

**DEF-142 owns the lines you are editing.** Run it first and name it in your
report. Do not report "DEF-138 green" as evidence of no regression — that was
finding G-5 on DEF-158, where the developer reported the adjacent family three
rounds running while both regressions sat in the family that owned the edit.

## Acceptance criteria

Design §9, AC-1 through AC-9 and AC-7a. Two worth repeating:

- **AC-2 asserts on `handleConversationHistory`'s result, not on the stored
  row.** The whole of DEF-158 was that those two differ. A persistence-only
  assertion passes against unfixed code.
- **AC-9** mutation-test in both directions, **including a row where the entire
  fix is reverted**. A matrix that mutates each half while the other half is
  live can stay green throughout — that happened on DEF-158 round 2.

Verify each mutation actually applied (`grep -c`) before trusting its result. A
mutation that silently fails to apply also produces a green.

## Method constraints

- **Do not run the full `pkg/hub` package.** 35 min wall, ends in
  `panic: test timed out`; `TestRS1_StaleAuthorityForcedOverlap` alone is 23m37s.
  Run by name.
- **`go test -run` with a non-matching pattern exits 0 and prints `ok`.** Always
  `-v -count=1`, and count the `--- PASS:` lines. **State your build tags with
  every result** — the same command under two tag sets means two things.
- Shell is **zsh**: quote globs, `${PIPESTATUS[0]}` is empty.
- `GOCACHE=/tmp/gocache-<yourname>` — `/scion-volumes/gocache` is shared.
- Reproduce locally. Nothing on gteam is to be touched.

## Reporting

Per-file `git diff --numstat` against `2519aa8b3`, full `go vet ./...`, mutation
results, and the DEF-138/140/141/142/152/156/158 run with tags and counts.

**Baseline is required on any defect or breakage you find along the way**: state
whether it is present on `main` and on the branch base, with the command you
ran. See `_GATE-APPARATUS.md`. Two of the last three findings had their severity
change once the baseline was checked — in opposite directions.

**Never make a gate pass by weakening the gate.** Any red comes to me with the
full log. A real failure is more welcome than a tuned-green one. If a claim in
the design turns out to be wrong, **your finding is the finding** — say so
plainly; that has already happened twice on this defect and both times the code
was right and the sentence about it was wrong.

`origin` in your container does **not** point at ptone/scion:

```sh
export GITHUB_TOKEN=$(cat /scion-volumes/scratchpad/transition-github-token.txt)
git push "https://x-access-token:${GITHUB_TOKEN}@github.com/ptone/scion.git" HEAD:refs/heads/scion/ca-msg-def160fix
git ls-remote "https://x-access-token:${GITHUB_TOKEN}@github.com/ptone/scion.git" refs/heads/scion/ca-msg-def160fix
```

Never push to `main` or `tranche-g`. I do the merge.

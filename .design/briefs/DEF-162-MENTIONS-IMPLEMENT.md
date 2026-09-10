# DEF-162 — wire agent-authored mentions to human notifications

You are a **developer** agent. Fresh context.

## Read first, in this order

1. `/scion-volumes/scratchpad/projects/ca-msg-arch/DEF-162-DESIGN.md` — the
   design. It is the specification. Phases, findings F1–F3, acceptance criteria
   AC-1…AC-8 and open questions are all there; this brief does not repeat them.
2. `/scion-volumes/scratchpad/projects/ca-msg-arch/briefs/_GATE-APPARATUS.md` —
   standing verification rules. Several were written after specific failures on
   this project and they are binding.

## Base

Branch from `scion/tranche-g` @ `e5b651719`. Work on `scion/ca-msg-def162`.

## P0 comes before any code — this is the whole point

The design's F2 says the sibling DM notification block
(`pkg/hub/handlers_agent_messaging.go:920-936`) is gated on
`s.GetMessageBrokerProxy() == nil`, delegating the other case to `deliverToUser`
in `pkg/hub/messagebroker.go` — and that `messagebroker.go` contains **no mention
handling whatsoever**.

**Your first deliverable is a written answer, no code:** where does an
agent-authored group message converge after that fork, if it converges at all?
Answer with `file:line`. Then say where the mention call should be sited so that
it fires on **both** topologies.

Report that to me and wait. If you site the call inside the `bp == nil` branch
because that is where the DM block lives, you will produce a change that passes
every test you write and does nothing on a broker deployment — and the symptom
will be identical to the bug you were sent to fix.

**A guard copied along with the line it guards is an assumption that two features
have the same topology.** Re-verify it; do not inherit it.

## Rulings already made — do not relitigate

- **Do not modify** `NotifyMention`, `fireHumanMentionNotifications`,
  `resolveProjectHumanMembers`, or `ExtractMentions`. All four are shared with
  the human web send path, and that path's behaviour is the specification.
- **The caller passes a resolved sender label** (`agent.Name`, falling back to
  `agent.Slug` — the existing pattern at `handlers_agent_messaging.go:924-926`).
  Do **not** copy the UUID-sniffing heuristic from `notifications.go:709-717`
  into `NotifyMention`.
- **No new setting, flag or switch.** ptone has twice directed that switches be
  consolidated and that this refactor land as a single cut-over. A mention that
  notifies is the behaviour already ruled; it does not get a flag.
- **Do not fix the name-ambiguity collision** (design F3, filed as DEF-165). It
  is live on the test instance and belongs in its own diff. But note AC-3: every
  mention fixture in your tests must resolve to exactly one member, or your green
  is measuring a coin flip.

## Method constraints

1. **Every function or symbol you name in a report carries its `file:line`.** If
   you cannot produce the line without searching, you know the naming convention,
   not the function.
2. **Never make a gate pass by weakening the gate.** Any red comes to me, not
   tuned away. That includes a pre-existing test that your change breaks —
   report it, propose, wait.
3. **Do not strip `!no_sqlite` build tags** to make tests run.
4. **Do not `git add -A`.** Shared workspace. Named paths only.
5. `GOCACHE=/tmp/gocache-162`.
6. **Never run the full `pkg/hub` package.** A clean run takes ~35 minutes and
   ends in a 30-minute timeout panic; `TestRS1_StaleAuthorityForcedOverlap` alone
   is ~24 minutes. Run by `-run` pattern.
7. `go test -run 'Pattern'` with a pattern matching nothing prints `ok` and exits
   0. Confirm your pattern matches before trusting a green.
8. **State the counting rule with every pass count.** `grep -cE '^--- PASS:'`
   counts top-level functions; `grep -cE '^ *--- PASS:'` includes subtests.
9. `gofmt -l` clean on every file you touch. Pre-existing and not yours:
   `pkg/hub/handlers_agents_core.go`, `pkg/hub/web_test.go`.
10. **Assert on decoded JSON fields, not on `rr.Body.String()`**, wherever the
    asserted text could contain `<` or `>` — Go's encoder escapes them.

## Mutation testing is required, and AC-6 needs the inverted direction

For each new guard, mutate it and show the test goes red. Confirm with `grep -c`
that your mutation actually applied before trusting a green — a mutation that
failed to apply also produces a green.

**AC-6 carries a negative assertion** (the notification label must never be a
UUID; the body must not contain `agent.ID`). A negative assertion passes
trivially in a world where the forbidden string was never present. **Mutate in
the direction that makes it present** — make the label the raw agent ID — and
show the assertion fires. A negative assertion that has not been mutated is a
comment.

Restore after every mutation and re-confirm green plus a clean tree.

## Report to me (`ca-msg-arch`)

P0 first, on its own. Then, per phase:

- Branch and SHA.
- `git diff --numstat <base> HEAD`, per file.
- Test command verbatim including tags and `-run`; counts with the counting rule.
- Mutation results, including the AC-6 inverted mutation.
- `gofmt -l` and `go vet`.
- Your answer to OQ-162-1 (does any `PublishChatNotification` consumer key off
  `ChatMessageContext.SenderID`?) with the evidence behind it.
- Anything you found that is not in the design. That section is usually the
  valuable one, and on this project it has been twice running.

## Push

`origin` in your container does **not** point at the right repo:

```sh
export GITHUB_TOKEN=$(cat /scion-volumes/scratchpad/transition-github-token.txt)
git push "https://x-access-token:${GITHUB_TOKEN}@github.com/ptone/scion.git" \
  HEAD:refs/heads/scion/ca-msg-def162
```

Verify separately — do not trust the push output alone:

```sh
git ls-remote "https://x-access-token:${GITHUB_TOKEN}@github.com/ptone/scion.git" \
  scion/ca-msg-def162
```

Never push to `scion/tranche-g` or `main`. I do the merge.

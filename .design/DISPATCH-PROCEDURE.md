# Agent dispatch procedure (ca-msg-arch)

**Use the tool. Do not run these by hand.**

```sh
/scion-volumes/scratchpad/tools/dispatch.sh <name> <brief-path> ["extra task text"]
```

It refuses if the brief file does not exist, and it runs step 3 unconditionally.

The manual steps below are kept for reference and for the cases the tool does not
cover. Reaching for them is the failure mode described in "Why this is a tool now".

```sh
# 1. Brief goes in a FILE. There is NO --brief-file flag; the task text points at the path.
#    Brief MUST open with a Section 0 clone+auth block: /workspace is an EMPTY DIRECTORY.
scion create <name> "Read your brief at /scion-volumes/scratchpad/briefs/<name>.md and follow it exactly. Report to ca-msg-arch when done." --yes --non-interactive

# 2. Start.
scion start <name> --yes --non-interactive

# 3. IMMEDIATELY clear the folder-trust dialog. Do not wait to be told the agent stalled.
scion message <name> "1"

# 4. Confirm it is actually executing, not merely heartbeating.
scion look <name>
```

## Why step 3 is unconditional

Claude Code's folder-trust prompt is **not** suppressed by `--dangerously-skip-permissions`. An
agent sits at `phase=running`, emits healthy 30s heartbeats, and has executed **nothing**. It is
reported later as a *stall*, which is a misleading name for a process that never started.

This has now happened three times, and each time I diagnosed it from scratch. The diagnosis is
cheap (`scion look`) and the remedy is one message, so the correct place for both is *before* the
symptom, not after it. Sending `"1"` to an agent that did not need it is harmless.

**Rule 674: when the same remedy resolves the same symptom three times, stop treating it as an
incident and make it a step.** An incident recurring on a schedule is a missing line in a
procedure. The tell is that the fix is always identical and always cheap — expensive fixes get
automated early because they hurt; cheap ones get re-derived forever because each individual
re-derivation is affordable.

## Gate list every brief must specify

`go build ./...`, `go vet ./...`, **`gofmt -l .`**, `go test -tags no_sqlite`, `go test` (sqlite),
`golangci-lint`, and the three guard scripts from `main`.

`gofmt -l .` was absent from every brief I wrote before 2026-08-30 and was the entire CI failure on
#1426. **`gofmt` ignores build tags**, so a `!no_sqlite` test file passes every test-lane gate
while unformatted.

## Landing links

Never hand-assemble. `python3 /scion-volumes/scratchpad/tools/compare-link.py <branch> <title>
<body-file>`; send only its output, to the dedicated thread, nothing else in the message.


## Why this is a tool now

Rule 674 turned three recurrences into a written step. On the fourth (DEF-160,
`ca-msg-g160`, 2026-09-10) I dispatched from memory anyway, skipped step 3, got
the stall, and then diagnosed it by reading this file — which had said exactly
what was wrong, in bold, since the third occurrence.

**A written procedure only fires if something makes you read it at the moment of
use.** Nothing did. I had internalised "create then start" — the *first* two
steps, the ones that existed before step 3 was added — and my memory of the
procedure was a snapshot from before the fix. That is the specific hazard of
amending a document you already believe you know: the amendment is invisible to
everyone who has stopped reading it, and the author is the person most likely to
have stopped.

So the escalation is the same one that worked for the Discord 2000-char cap,
which I also blew twice while knowing the number: **stop writing the rule down
and start making the rule executable.** `pt-send.sh` refuses to send an
over-length message; `dispatch.sh` refuses to skip step 3. Neither depends on me
remembering anything at the moment it matters.

**The generalisable form: if a rule has failed once after being written down, the
next iteration is not a clearer sentence, it is a tool.** Prose competes with
memory and loses, because memory feels like knowing. A script does not care what
I think I remember.

Corollary worth keeping: the three cheap re-derivations that produced Rule 674
were affordable individually, and so was this one. Cost is not the signal —
*recurrence* is. Something that recurs on a schedule while staying cheap will
never force its own fix, which is precisely why it needs a rule that fires on
the count rather than on the pain.

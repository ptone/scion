# Agent dispatch procedure (ca-msg-arch)

Do not dispatch from memory. Follow this.

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

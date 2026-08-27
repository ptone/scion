# Brief: respond to the automated review on #1310 — take four, decline two

Author: sn-impl-arch (architect). Date: 2026-08-27. Task #58.

You are the developer. I designed this; I do not implement it. **Read the whole brief before you
start.** If a step contradicts what you find on disk, **stop and message me** — do not improvise
around it.

**Do not start until `sn-cloudbuild-dev` has finished pushing to `scion/sn-tier`.** I will tell you.
One developer at a time on a branch under upstream review.

---

## 1. What this is

`gemini-code-assist[bot]` left **six** comments on `GoogleCloudPlatform/scion#1310`, all rated
medium. There is **no human review yet**. None of the six is blocking.

I have read all six against the actual code on the branch and made a call on each. **You are taking
four and declining two.** The declines are deliberate and I have written the reasoning so you can
paste it into the PR thread — a declined bot comment that nobody answers just sits there looking
unaddressed.

**Do not simply apply the bot's suggested code.** One of the four suggestions contains a real bug.
See §3.

## 2. Take these three as written (they are correct)

### R1 — `cmd/deploy_instance.go:404`, `diRESTCall` misses context

Change `http.NewRequest` to `http.NewRequestWithContext` and thread a `ctx` parameter through to the
call sites. Correct, conventional, cheap.

### R2 — `cmd/deploy_instance.go:708`, `diRunGcloud` misses context

Change `exec.Command` to `exec.CommandContext`, thread `ctx` through.

**Take the bot's stderr improvement too.** Its version extracts stderr from `*exec.ExitError`:

```go
var exitErr *exec.ExitError
stderrMsg := ""
if errors.As(err, &exitErr) && len(exitErr.Stderr) > 0 {
    stderrMsg = "\n" + string(exitErr.Stderr)
}
```

That is worth more than the context change. We have an open defect — ptone/scion#1274 area, and our
own register entry — about `gcloud` failures on this path being undiagnosable. This directly improves
that. Keep its existing arg-truncation, which exists so tokens are not printed.

### R5 — `pkg/runtimebroker/pty_handlers.go:711`, duplicated tmux resize

The `tmux resize-window` block is duplicated **exactly** between `LocalPTYSession.readFromWebSocket`
and `StreamPTYHandler.handleResize`. Extract to a shared helper as the bot suggests. Straightforward,
low risk.

## 3. Take R4's intent — but the bot's code is WRONG. Do not paste it.

**R4 — `pkg/runtime/cloudrun_sandbox_runtime.go:785`, `time.Sleep` ignores cancellation.**

The complaint is valid. Current code:

```go
    time.Sleep(delay)
    probeCtx, probeCancel := context.WithTimeout(ctx, 5*time.Second)
```

A cancelled `ctx` still waits out the full sleep. Worth fixing.

**But the bot's replacement has a control-flow bug:**

```go
    select {
    case <-ctx.Done():
        probeErr = ctx.Err()
        break          // <-- breaks the SELECT, not the FOR
    case <-time.After(delay):
    }
    probeCtx, probeCancel := context.WithTimeout(ctx, 5*time.Second)
    _, probeErr = runSimpleCommand(...)   // <-- runs anyway, and OVERWRITES probeErr
```

In Go, `break` inside a `select` terminates the `select`, not the enclosing `for`. So on
cancellation it sets `probeErr`, falls straight through, runs the probe anyway, and **overwrites**
`probeErr` with the probe's result. The cancellation is silently discarded. That is worse than the
`time.Sleep` it replaces, because it looks like it handles cancellation.

**Use a labelled break or an early return instead.** Whichever you pick, make sure a cancelled
context genuinely exits the loop and that `probeErr` still carries `ctx.Err()` when it does — the
block at line 787 onward reads `probeErr` to decide whether to emit the "dead on arrival"
diagnostics, and a cancellation is not a dead sandbox. Consider whether the diagnostic path should
distinguish the two. **Tell me what you chose and why.**

## 4. Decline these two. Post the reasoning on the PR.

### R3 — "expose the hardcoded `/scion` root dir in config" — DECLINE

The bot's stated motivation is *"makes local testing or running in non-root environments difficult
because `/scion` cannot be created without root privileges."*

**The premise is wrong, and I verified it.** This runtime shells out to a `sandbox` binary at
`defaultSandboxBin = "/usr/local/gcp/bin/sandbox"` (line 39), and line 86 does an `os.Stat` on it as
an availability check. That binary exists only inside a Cloud Run Instance. **You cannot run this
runtime locally at any `rootDir`**, so making `rootDir` configurable does not enable local testing.

The bot even hedges its own suggestion — *"Assuming RootDir is added to V1CloudRunSandboxConfig"*.
It is proposing a config-schema change, which is load-bearing and hard to remove later, to serve a
scenario that does not exist.

Suggested reply:

> Declining this one. `/scion` is a path inside the omni container, not a host path. The runtime
> requires the `sandbox` binary at `/usr/local/gcp/bin/sandbox`, which only exists inside a Cloud
> Run Instance — there is an `os.Stat` availability check on it — so this runtime cannot run locally
> regardless of `rootDir`. Adding a config field would widen the schema without enabling the
> scenario that motivates it. Happy to revisit if a local sandbox shim ever exists.

### R6 — "precompute parsed IPs before `sort.Slice`" — DECLINE

`pkg/sciontool/metadata/server.go:343`. The observation is technically true: `net.ParseIP` is called
inside the comparator, so it runs O(N log N) times instead of O(N).

**But N is the number of IPv4 link-local addresses on the host's interfaces — realistically 1 to 3 —
and the function has already returned early for N of 0 and 1.** So this optimises a sort of two or
three elements, once, at startup. The saving is unmeasurable.

Against that: this is the link-local selection logic that was **§1 BLOCKER task #25** — auto-discovery
failing on this platform cost us real time to diagnose. Rewriting hard-won code on a cold path for an
unmeasurable gain is a bad trade. The bot's version also precomputes `net.ParseIP(s).To4()`, which
yields `nil` on a parse failure and then feeds that `nil` to `metadataNet.Contains` and
`bytes.Compare` — the same hazard exists inline today, but materialising it into a struct field makes
it easier to reuse wrongly later.

Suggested reply:

> Declining on cost/benefit. N here is the count of link-local addresses on the host — the function
> returns early for 0 and 1, so this is a 2-3 element sort that runs once at startup. The redundant
> parses are unmeasurable, and this selection logic was the root cause of a hard-won startup bug, so
> I would rather not rewrite it for a micro-optimisation. Flagging rather than hiding it: the
> observation itself is correct.

## 5. What you must NOT do

- **Do not rebase and do not force-push.** #1310 is open with review comments attached to lines; a
  force-push detaches them.
- **Do not touch `.github/`** — those files were deliberately removed at ptone's direction.
- **Do not touch anything under `image-build/`** — `sn-cloudbuild-dev` owns that.
- **Do not try to fix `cla/google`.** Known non-blocker; merged #1304 had the same agent author.
- **Do not delete any Instance or agent.** `e2e-omni`, `e2e-walk-r2`, `iap-demo`, `q2-control`,
  `sn-ready`, `sn-adminseed-t`, `sn-adminfix-t` are all do-not-delete.
- **Do not accept a bot suggestion you have not read.** That is how R4's bug would have shipped.

## 6. Verify

1. Build passes; `go test ./...` passes (`internal/fixturegen` fails identically on `main` —
   pre-existing, ignore it).
2. `golangci-lint` clean — CI runs it and it currently passes; do not regress it.
3. For R4 specifically: satisfy yourself that a cancelled context **exits the retry loop**. If you
   can add a cheap unit test that proves it, do — this is exactly the class of bug that a test pins
   and a code read misses.
4. File count against upstream main is unchanged from whatever `sn-cloudbuild-dev` leaves it at.

## 7. Report back

Message `sn-impl-arch` with the commit SHA, what you chose for R4's control flow and why, whether
you added a test for it, and confirmation that both decline replies are posted on the PR thread.

If you think one of my two declines is wrong, **say so before implementing.** I would rather argue
about it now than have you silently comply with a call you think is bad. I have been wrong twice on
this PR already today.

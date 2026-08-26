# P4 / P4a / P5 (71fd320..e6db196) — Review

## Executive Summary

Four commits implementing sandbox exec control plane (P4), timeout-bounded delete
(P4a), Tier 0 ephemeral-storage honesty (P5), and a test update. Risk level: **LOW**.
The implementation matches the design doc, existing Docker/K8s paths are structurally
untouched, and tests are adequate.

## Critical

None.

## Required

None.

## Nit / Optional

### 1. [Optional] `deleteWithTimeout`: watcher cancel not restored on `cmd.Start()` failure

**File:** `pkg/runtime/cloudrun_sandbox_runtime.go:704-720`

`deleteWithTimeout` cancels the watcher goroutine (lines 706-711) *before*
calling `cmd.Start()` (line 718). If `cmd.Start()` fails (binary missing,
permission denied), the method returns an error — but the watcher has already
been cancelled and removed from `watchCancels`. The sandbox is now unmonitored
but still in the state store.

In practice this requires the sandbox binary to become unavailable between
`Run()` and `Delete()`, which is extremely unlikely on a Cloud Run Instance
(the binary is platform-injected and immutable). The `reconcile()` call at
next startup would also clean up the orphan. Not blocking, but worth a
defensive `// TODO` comment acknowledging the gap.

**Suggested fix:** Add a comment documenting the accepted gap, or move the
watcher cancel to after `cmd.Start()` succeeds (slightly more complex but
closes the window).

### 2. [Nit] `reapOrphanedRunsc`: substring match on sandbox name

**File:** `pkg/runtime/cloudrun_sandbox_runtime.go:976-978`

```go
if strings.Contains(args, "runsc") &&
    strings.Contains(args, "delete") &&
    strings.Contains(args, sandboxName) {
```

A sandbox named `foo` would match an orphaned `runsc` process for sandbox
`foobar`. The three-way conjunction plus the fact that the matched process is
already being `runsc delete`'d makes the blast radius near-zero (killing a
delete-in-progress process for a *different* sandbox is at worst a retry). Not
worth blocking on, but if it ever matters, matching on the path component
(`/run/sandbox/<name>/`) would be precise.

### 3. [Nit] Mixed `slog` vs `runtimeLog` in the same file

**File:** `pkg/runtime/cloudrun_sandbox_runtime.go`

New helpers (`killProcessGroup`, `reapOrphanedRunsc`, `deleteWithTimeout`) use
the stdlib `slog` directly. The watcher and state store code in the same file
uses the package-level `runtimeLog` logger. The project log documents this as
intentional per the brief, but the resulting file has two logging conventions.

Not blocking — can be unified in a follow-up if the team prefers consistency.

## FYI

### 4. [FYI] `Attach()` does not check `entry.Stopped`

**File:** `pkg/runtime/cloudrun_sandbox_runtime.go:828-843`

`Attach()` verifies the sandbox exists in the state store but does not check
`entry.Stopped`. If the sandbox has already exited, the `sandbox exec`
command will fail with a runtime error rather than a clear "sandbox is
stopped" message. This matches Docker's `Attach()` behavior (which also
does not pre-check status), so it is consistent.

### 5. [FYI] `exec.CommandContext` vs `killProcessGroup` on context cancellation

**File:** `pkg/runtime/cloudrun_sandbox_runtime.go:713,741-744`

When `ctx.Done()` fires, `exec.CommandContext` sends SIGKILL to the process
leader, and then `killProcessGroup` sends SIGKILL to the whole process group
(`-pid`). Both are needed: `exec.CommandContext` only kills the leader, while
`Setpgid: true` isolates children in the group. The current code is correct
and the ordering (killProcessGroup then `<-done` for reaping) is safe.

### 6. [FYI] Duplicate `cloudRunSandboxBin` constant

**File:** `pkg/runtimebroker/pty_handlers.go:60` vs
`pkg/runtime/cloudrun_sandbox_runtime.go:42`

`cloudRunSandboxBin` in `pty_handlers.go` duplicates `defaultSandboxBin` in
the runtime package. Both are unexported, so sharing is impossible without
either exporting or adding a cross-package dependency. The project log
explicitly documents this as a deliberate decision. Acceptable as-is.

### 7. [FYI] `startCloudRunSandboxExec` is duplicated across `LocalPTYSession` and `StreamPTYHandler`

**File:** `pkg/runtimebroker/pty_handlers.go:580-611` and `996-1058`

Both methods are nearly identical. This matches the existing codebase pattern —
`startDockerExec` is similarly duplicated across the two handler types. The
design doc (§4.8) notes this is option (b), with option (c) (promoting
interactive exec into the Runtime interface) recorded as the right future
refactor.

## Positive Feedback

- **Excellent documentation quality.** Every load-bearing decision has a
  comment that links back to the design doc section, names the failure mode it
  prevents, and explains *why* (not just *what*). The TERM=xterm-256color
  comments, the delete timeout rationale, and the watcher cancellation
  ordering are all clear and would survive a cold-read six months from now.

- **`deleteWithTimeout` is well-structured.** The three-way select
  (completion / timeout-as-success / context cancellation) is the right
  pattern for bounding a known-hanging platform command. Process-group killing
  and orphan reaping are defensive and appropriate.

- **Test coverage for P4a is thorough.** The eight `deleteWithTimeout` tests
  cover the key behavioral axes: normal completion, timeout-as-success,
  context cancellation, process-error-non-fatal, watcher cancel interaction,
  default timeout fallback, nil-process safety, and running-process kill.

- **P5 is minimal and precise.** Three well-scoped changes (explicit write
  permission, health API warning, frontend banner) with six tests, and the
  existing `warnEphemeralProjectPath` is confirmed untouched. The
  `workspaceWriteBlocked` comment explaining *why* the explicit check
  prevents a future accident is a model for defensive coding.

- **No regression to existing paths.** The Docker/Podman and K8s branches in
  `pty_handlers.go` are structurally unchanged. The if/else restructuring is
  clean and each branch remains self-contained.

## Test Coverage

**Adequate.** Key coverage:

| Area | Tests | Gaps |
|---|---|---|
| `deleteWithTimeout` | 8 tests covering all select arms | OQ-16 (concurrent delete) explicitly out of scope |
| `killProcessGroup` | Nil process + running process | — |
| `reapOrphanedRunsc` | No-op on absent sandbox | No test for actual reaping (would require spawning a fake runsc) |
| P4 methods (GetLogs, Attach, Exec) | Verify no longer stub; verify Attach checks state | No integration test (requires sandbox binary) |
| P5 `isCloudRunInstance` | Set / unset | — |
| P5 `workspaceWriteBlocked` | CRI alone, CRI + local backend | No test for `K_SERVICE` + `CLOUD_RUN_INSTANCE` both set |
| P5 health warnings | Present on CRI / absent otherwise | — |
| pty_handlers.go | No new unit tests | Existing patterns have no unit tests either; integration-tested |

The gap in `reapOrphanedRunsc` testing is acceptable — the function is
best-effort defensive cleanup and the no-op path is tested. The
pty_handlers.go changes follow the existing pattern of no direct unit tests
(the handler types are integration-tested through the broker).

## Backward Compatibility

No wire-format breakage. The `DeploymentWarnings` field in `HealthResponse`
is `omitempty`, so existing clients that don't know about it see no change.
No removed or renamed fields. The `deploymentWarnings` key in the frontend
TypeScript interface is additive and optional.

## Final Verdict

**APPROVE**

All remaining findings are Nit, Optional, or FYI — none block merge.

**Gates run:**
- `go build ./pkg/runtime/... ./pkg/runtimebroker/... ./pkg/hub/...` — **PASS**
- `go vet ./pkg/runtime/... ./pkg/runtimebroker/... ./pkg/hub/...` — **PASS**
- `go test ./pkg/runtime/... -run TestCloudRunSandbox -count=1` — **PASS** (all 22 tests)
- `go test ./pkg/runtime/... -run "TestKillProcessGroup|TestReapOrphanedRunsc" -count=1` — **PASS**
- `go test ./pkg/hub/... -run "TestIsCloudRunInstance|TestWorkspaceWriteBlocked_CloudRun|TestHealthCheck_Deployment" -count=1` — **PASS** (6 tests)

**Gates not run:**
- Full `go test ./...` — not run to avoid timeout on full test suite in review container. The three targeted test runs above cover all changed packages and all new test functions.
- Frontend lint/build (`npm run lint`, `tsc`) — not run; no Node.js toolchain in this container. The TypeScript change is additive (new optional interface field + new render method) and follows existing lit-html patterns.

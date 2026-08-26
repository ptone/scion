# `0a1536b` refactor(runtime): isolate delete --force workaround for clean removal -- Review

## Executive Summary

Clean isolation of a platform-bug workaround into a single removable file, with a
correct safety fix (conditional reaping) and an improved matcher. Risk level: **LOW**.

## Critical Check: Does the reaper run when the delete succeeded?

**NO -- this is correct.**

The parent commit (`a515490`, lines 747-748) called `reapOrphanedRunsc(id)` **after**
the select statement, outside any branch -- meaning it ran unconditionally on every
delete, including normal completions:

```go
// a515490 -- BEFORE (dangerous)
select {
case err := <-done:
    // ...normal completion...
case <-time.After(timeout):
    killProcessGroup(cmd)
    <-done
case <-ctx.Done():
    killProcessGroup(cmd)
    <-done
    return ctx.Err()
}

// Defensively reap any orphaned runsc processes for this sandbox.
reapOrphanedRunsc(id)       // <-- runs in ALL branches
```

The new code (`cloudrun_sandbox_delete_workaround.go:131-141`) calls
`reapOrphanedRunsc` **only** inside the `case <-time.After(timeout)` branch:

```go
case <-time.After(timeout):
    // ...
    killProcessGroup(cmd)
    <-done
    reapOrphanedRunsc(id)   // <-- only when timeout fired
```

The CRITICAL comment at line 139-140 documents the invariant explicitly. This is the
correct fix -- when the platform bug is fixed, a working delete returns promptly and
the reaper never runs.

## Critical

None.

## Required

None.

## Nit / Optional

### Nit: File header removal instructions slightly imprecise
**File:** `cloudrun_sandbox_delete_workaround.go:35-36`

The header says "revert Stop()/Delete() in cloudrun_sandbox_runtime.go to a plain
exec". In practice, `Stop()` and `Delete()` themselves are one-liners that call
`deleteOrWorkaround()` -- the actual removal is to simplify `deleteOrWorkaround` to
always call `deletePlain` (or inline it). The intent is clear; the description is
slightly misleading about which function changes.

**Suggested fix:** Replace "revert Stop()/Delete()" with "simplify deleteOrWorkaround()"
or "remove the deleteWorkaroundEnabled dispatch".

### Optional: `deleteWithTimeout` treats process errors as non-fatal; `deletePlain` does not

**File:** `cloudrun_sandbox_delete_workaround.go:118-121` vs
`cloudrun_sandbox_runtime.go:700-703`

When the workaround is active and delete returns exit code 1, `deleteWithTimeout`
logs a warning and returns nil (line 118-121). When the kill switch is set (`deletePlain`),
the same exit code 1 returns an error. This is a behavioral difference the operator
will encounter when flipping the switch.

This is arguably correct -- with the workaround off, a real delete failure should
surface -- but it is worth documenting in the kill-switch log message or in the
`deletePlain` comment so the operator understands the different error semantics.

## FYI

### FYI: The old matcher was dead code

**File:** `cloudrun_sandbox_runtime.go` (removed lines 979-980)

The old `reapOrphanedRunsc` matched on `/run/sandbox/<name>/` as a path segment, but
the captured orphan argv from the defect investigation shows
`--root=/tmp/runsc-root`, not `/run/sandbox/<name>/runc`. The old matcher would
never have matched a real orphan. The new `isOrphanedRunscProcess` correctly
NUL-splits cmdline and matches on exact equality of the final argv element, which is
how `runsc delete --force <name>` actually presents in `/proc/<pid>/cmdline`.

### FYI: `sync.Once` means only the first normal return triggers the WARN

**File:** `cloudrun_sandbox_delete_workaround.go:123-127`

If a fleet teardown triggers many deletes and the platform bug is partially fixed
(some return, some hang), only the first normal return fires the WARN log. Subsequent
normal returns get the quieter INFO at line 128. This is fine for a removal signal
but worth knowing when reading logs.

## Positive Feedback

1. **The dispatch/isolation pattern is well-designed.** Watcher cancel lives in
   `deleteOrWorkaround` (the permanent dispatch function), not in the workaround
   file. This means `git rm`-ing the workaround cannot lose watcher cancel logic.
   That is a deliberate design choice and it is the right one.

2. **The matcher fix is grounded in evidence.** The test cases include the real
   captured argv from the defect investigation, and the near-miss cases (substring,
   flag value, wrong subcommand) are well-chosen. The 7-case table-driven test is
   thorough.

3. **Self-detection is a good idea.** For a workaround with no public bug to watch,
   having the code announce its own obsolescence is a practical signal.

4. **The defect evidence file is well-written.** Clear control matrix, reproducible
   setup, honest about limits. Good provenance for the workaround.

## Test Coverage

All new code paths are covered:

- `deleteWithTimeout`: 6 tests covering normal completion, timeout, context
  cancellation, process error, watcher cancel, and default timeout.
- `killProcessGroup`: 2 tests (nil process, running process).
- `reapOrphanedRunsc`: 1 smoke test (no-panic on nonexistent name).
- `isOrphanedRunscProcess`: 7-case table-driven test covering the genuine captured
  orphan, substring near-miss, flag-value near-miss, wrong subcommand, short
  cmdline, empty cmdline, and non-runsc binary.

Tests were moved cleanly from `cloudrun_sandbox_runtime_test.go` to
`cloudrun_sandbox_delete_workaround_test.go` with minor improvements (extracted
`writeMockBin` and `newWorkaroundTestRuntime` helpers).

No test gap for the `deletePlain` path specifically, but it is a 4-line function
(`exec.CommandContext` + `cmd.Run` + state remove) and the dispatch through
`deleteOrWorkaround` is tested by `TestCloudRunSandboxDeleteWithTimeout_CancelsWatcher`.

## Backward Compatibility

No wire-format changes. No API changes. The `SCION_CLOUDRUN_DELETE_WORKAROUND`
env var is new but defaults to the existing behavior (workaround enabled). No
breaking change.

## Final Verdict

**APPROVE**

All six architect requirements are met:

| Requirement | Status |
|---|---|
| 1. File isolation | All workaround code in `cloudrun_sandbox_delete_workaround.go` |
| 2. Conditional reaping | Only in the timeout branch; confirmed by diff against parent |
| 3. Self-detecting | `sync.Once` WARN with `deleteDefectRef` on normal return |
| 4. Runtime kill switch | `SCION_CLOUDRUN_DELETE_WORKAROUND=off` dispatches to `deletePlain` |
| 5. Bug reference | `deleteDefectRef` constant citing defect-sandbox-delete-hang.md |
| 6. Exit criteria in file header | Anchored on `runsc google-958767651`, 4 exit criteria |

Evidence file `defect-sandbox-delete-hang.md` is in `.design/project-log/`.

**Gates run:**
- `go build ./pkg/runtime/...` -- PASS
- `go vet ./pkg/runtime/...` -- PASS
- `go test ./pkg/runtime/... -run "TestCloudRunSandbox|TestKillProcessGroup|TestReapOrphanedRunsc|TestIsOrphanedRunscProcess" -count=1 -v` -- PASS (all 25 tests, 0.463s)

**Gates not run:** None -- all specified gates were executed successfully.

**Recommendations (non-blocking, for a cleanup pass):**
- Tighten the removal instructions in the file header (Nit above).
- Consider documenting the error-semantics difference between workaround-on and
  workaround-off paths (Optional above).

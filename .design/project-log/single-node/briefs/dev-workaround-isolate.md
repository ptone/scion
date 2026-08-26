# Brief: Isolate delete workaround for clean removal

## Task

Restructure the `sandbox delete --force` workaround code in `pkg/runtime/` so it lives in a single file that can be `git rm`'d when the upstream platform bug is fixed. This includes moving code, fixing a critical safety issue (unconditional reaping), adding a runtime kill switch, and making the workaround self-detecting.

## Context

`sandbox delete --force` on Cloud Run Sandboxes never returns (platform bug, now filed upstream). Our workaround bounds the wait with a timeout and treats timeout as success. The workaround code is currently interleaved with the main runtime logic. ptone (project lead) wants it isolated so that when the platform fix ships, someone can:
1. Set `SCION_CLOUDRUN_DELETE_WORKAROUND=off` to validate
2. Then `git rm cloudrun_sandbox_delete_workaround.go` + revert the Delete/Stop call

There is also a **critical safety issue**: `reapOrphanedRunsc` currently runs unconditionally after every delete. When the platform bug is fixed, a working delete would return promptly and the reaper would SIGKILL a healthy in-flight `runsc` process matching the same pattern. The reaper must only run when the timeout actually fired.

## Pre-existing state

A previous developer (dev-reap-fix) has already made these changes in the working tree (NOT committed):
- Extracted `isOrphanedRunscProcess(cmdline []byte, sandboxName string) bool` helper from `reapOrphanedRunsc`
- Fixed the stale `args` variable reference in the slog call
- Fixed import ordering (`regexp` before `slices`)
- Wrote a 7-case table-driven test `TestIsOrphanedRunscProcess`

**WARNING: The previous developer's work was INCOMPLETE.** The working tree has partial changes:
- NUL-split argv matching logic IS applied (correct)
- Import: `bytes` removed, `slices` added (but BEFORE `regexp` -- wrong order, fix this)
- Comment citing captured argv IS updated (correct)
- **NOT done:** `isOrphanedRunscProcess` was NOT extracted
- **NOT done:** stale `args` reference on line ~996 was NOT fixed (BUILD FAILS)
- **NOT done:** table-driven test was NOT written

You must do ALL of the following yourself as part of the restructuring:
- The `isOrphanedRunscProcess` helper takes raw `/proc/<pid>/cmdline` bytes and a sandbox name, returns bool
- It splits on NUL, checks argv[0] basename contains "runsc", argv contains "delete", argv last element == sandboxName (exact equality)
- Test cases: genuine captured orphan, near-miss substring, near-miss flag value, unrelated runsc process, short cmdline, empty cmdline, non-runsc binary

## Deliverables — the 6 changes

### 1. Create `pkg/runtime/cloudrun_sandbox_delete_workaround.go`

New file containing ALL workaround code. File structure:

```go
// Copyright 2026 Google LLC ... (standard header)

// This file is a workaround for an upstream Cloud Run Sandbox defect:
// `sandbox delete --force` never returns. The deletion IS effective --
// the sandbox really is gone -- but the CLI process hangs indefinitely.
//
// See defect-sandbox-delete-hang.md for the full investigation.
//
// KNOWN-BAD BUILD: runsc google-958767651 (spec 1.2.1, 2026-08-04).
//
// EXIT CRITERIA -- remove this file when ALL of the following hold
// on a runsc build NEWER than google-958767651:
//   1. `sandbox delete --force` returns within DefaultDeleteTimeout on a
//      sandbox with a live process (not just idle sandboxes).
//   2. No orphaned `runsc delete` process remains after the command returns.
//   3. The above holds across concurrent deletes (our actual access pattern).
//   4. The self-detecting WARN log ("upstream defect may be fixed") fires
//      on normal delete returns -- this is the primary removal trigger
//      since there is no public bug to watch.
//
// To remove: delete this file, revert Stop()/Delete() in
// cloudrun_sandbox_runtime.go to a plain exec of `sandbox delete --force`,
// and drop the SCION_CLOUDRUN_DELETE_WORKAROUND env var check.

package runtime

import (...)

// deleteDefectRef identifies the platform defect this file works around.
// Tracked internally by the Cloud Run team -- there is no public issue to cite.
// Observed on runsc google-958767651 (spec 1.2.1, 2026-08-04).
// Evidence and control matrix: .design/project-log/defect-sandbox-delete-hang.md
const deleteDefectRef = "cloudrun sandbox: 'sandbox delete --force' never returns; " +
    "see .design/project-log/defect-sandbox-delete-hang.md"

// DefaultDeleteTimeout is the timeout for sandbox delete --force.
// ... (moved from cloudrun_sandbox_runtime.go)
const DefaultDeleteTimeout = 10 * time.Second
```

Move these functions/methods into this file:
- `deleteWithTimeout` (method on `*CloudRunSandboxRuntime`)
- `killProcessGroup`
- `reapOrphanedRunsc`
- `isOrphanedRunscProcess` (the extracted helper)

### 2. Conditional reaping (CRITICAL)

In `deleteWithTimeout`, move the `reapOrphanedRunsc(id)` call so it ONLY runs inside the timeout branch:

```go
case <-time.After(timeout):
    slog.Warn("sandbox delete --force timed out, treating as success",
        "sandbox", id, "timeout", timeout,
        "defect", deleteDefectRef)
    killProcessGroup(cmd)
    <-done // reap the zombie
    reapOrphanedRunsc(id)  // <-- ONLY here, not unconditionally
```

Remove line 748 (`reapOrphanedRunsc(id)` after the select).

When the delete completes normally (the `case err := <-done` branch), do NOT call reapOrphanedRunsc.

### 3. Self-detecting (sync.Once WARN)

Add a package-level `sync.Once` that logs a WARN when delete returns normally:

```go
var deleteWorkaroundFixDetected sync.Once

// In the normal-return branch of the select:
case err := <-done:
    if err != nil {
        slog.Warn("sandbox delete --force returned with error",
            "sandbox", id, "error", err)
    } else {
        deleteWorkaroundFixDetected.Do(func() {
            slog.Warn("sandbox delete --force returned normally -- "+
                "upstream defect may be fixed; this workaround is a candidate for removal",
                "sandbox", id, "defect", deleteDefectRef)
        })
        slog.Info("sandbox delete --force completed normally",
            "sandbox", id)
    }
```

### 4. Runtime kill switch

Add an `init()` or startup check for `SCION_CLOUDRUN_DELETE_WORKAROUND`:

```go
// deleteWorkaroundEnabled controls whether the timeout/reaper workaround
// is active. Set SCION_CLOUDRUN_DELETE_WORKAROUND=off to bypass.
var deleteWorkaroundEnabled = true

func init() {
    if os.Getenv("SCION_CLOUDRUN_DELETE_WORKAROUND") == "off" {
        deleteWorkaroundEnabled = false
        slog.Warn("Cloud Run delete workaround DISABLED via SCION_CLOUDRUN_DELETE_WORKAROUND=off",
            "defect", deleteDefectRef)
    }
}
```

Then in `cloudrun_sandbox_runtime.go`, update `Stop()` and `Delete()`:

```go
func (r *CloudRunSandboxRuntime) Stop(ctx context.Context, id string) error {
    return r.deleteOrWorkaround(ctx, id)
}

func (r *CloudRunSandboxRuntime) Delete(ctx context.Context, id string) error {
    return r.deleteOrWorkaround(ctx, id)
}

// deleteOrWorkaround dispatches to the workaround or the plain path based
// on the kill switch.
func (r *CloudRunSandboxRuntime) deleteOrWorkaround(ctx context.Context, id string) error {
    if deleteWorkaroundEnabled {
        return r.deleteWithTimeout(ctx, id)
    }
    return r.deletePlain(ctx, id)
}

// deletePlain is the non-workaround path: plain `sandbox delete --force`.
// Used when SCION_CLOUDRUN_DELETE_WORKAROUND=off.
func (r *CloudRunSandboxRuntime) deletePlain(ctx context.Context, id string) error {
    // Cancel the watcher goroutine.
    r.watchMu.Lock()
    if cancel, ok := r.watchCancels[id]; ok {
        cancel()
        delete(r.watchCancels, id)
    }
    r.watchMu.Unlock()

    cmd := exec.CommandContext(ctx, r.bin, "delete", "--force", id)
    if err := cmd.Run(); err != nil {
        return fmt.Errorf("cloudrun-sandbox: delete --force failed: %w", err)
    }
    r.state.remove(id)
    return nil
}
```

### 5. Bug reference constant

Already shown above. There is NO public bug ID -- the defect is tracked internally by the Cloud Run team. Do NOT use `TODO(bug-id)` or invent a placeholder. The `deleteDefectRef` constant cites our own evidence file instead.

### 5a. Copy defect report into the repo

Copy `/scion-volumes/scratchpad/projects/single-node/defect-sandbox-delete-hang.md` into `.design/project-log/defect-sandbox-delete-hang.md`. The workaround file's comments reference this path -- the evidence must be in the repo, not on a scratchpad volume that resolves only for our agents.

### 6. Move tests to `pkg/runtime/cloudrun_sandbox_delete_workaround_test.go`

Create a new test file and move ALL workaround-related tests there:
- `TestCloudRunSandboxDeleteWithTimeout_NormalCompletion`
- `TestCloudRunSandboxDeleteWithTimeout_Timeout`
- `TestCloudRunSandboxDeleteWithTimeout_ContextCancellation`
- `TestCloudRunSandboxDeleteWithTimeout_ProcessErrorNonFatal`
- `TestCloudRunSandboxDeleteWithTimeout_CancelsWatcher`
- `TestCloudRunSandboxDeleteWithTimeout_DefaultTimeout`
- `TestKillProcessGroup_NilProcess`
- `TestKillProcessGroup_RunningProcess`
- `TestReapOrphanedRunsc_NoProc`
- `TestIsOrphanedRunscProcess` (the new table-driven test)

Also move `newTestRuntime` — it is used by deleteWithTimeout tests. If other tests in the original file also use it, leave a copy in both files (both are in `package runtime`).

## Files to modify

1. **CREATE** `pkg/runtime/cloudrun_sandbox_delete_workaround.go` — all workaround code
2. **CREATE** `pkg/runtime/cloudrun_sandbox_delete_workaround_test.go` — all workaround tests
3. **MODIFY** `pkg/runtime/cloudrun_sandbox_runtime.go` — remove moved code, add `deleteOrWorkaround`/`deletePlain`, update Stop/Delete
4. **MODIFY** `pkg/runtime/cloudrun_sandbox_runtime_test.go` — remove moved tests
5. **COPY** `/scion-volumes/scratchpad/projects/single-node/defect-sandbox-delete-hang.md` to `.design/project-log/defect-sandbox-delete-hang.md` — evidence must be in the repo

## Verification

After making all changes, run:
1. `go build ./pkg/runtime/...` — must pass
2. `go vet ./pkg/runtime/...` — must pass
3. `go test ./pkg/runtime/... -run "TestCloudRunSandbox|TestKillProcessGroup|TestReapOrphanedRunsc|TestIsOrphanedRunscProcess" -count=1 -v` — ALL tests must pass

## Boundaries

- Do NOT modify any file outside `pkg/runtime/`
- Do NOT change the matching logic in `isOrphanedRunscProcess` — it is correct
- Do NOT use `TODO(bug-id)` or invent a bug ID -- there is no public issue
- The defect is tracked internally by the Cloud Run team; cite our own evidence file
- Do NOT change the `CloudRunSandboxRuntime` struct fields (except you may add `deleteTimeout` comments if helpful)
- Do NOT modify `pty_handlers.go` or any hub code
- The `deleteTimeout` field stays on the struct — it is used by tests and may be needed post-workaround for general timeout purposes

## Important: watcher cancel duplication

Both `deleteWithTimeout` (workaround) and `deletePlain` (non-workaround) need the watcher cancel logic. You can either:
- Duplicate the 5-line watcher cancel block in both methods (cleaner separation)
- Extract a `cancelWatcher(id string)` helper in the workaround file (called by both)

Either approach is fine. The goal is that removing the workaround file does not lose the watcher cancel.

Recommended: put the watcher cancel in `deleteOrWorkaround` before dispatching, so neither `deleteWithTimeout` nor `deletePlain` needs it.

## Deliverable

- All 4 files created/modified as described
- All verification commands passing
- One commit on the current branch (`scion/dev-rebase-1294`) with message:
  `refactor(runtime): isolate delete --force workaround for clean removal`
- Push the branch
- Write a project log entry to `/workspace/.design/project-log/` documenting the restructuring and why

## Reporting

Report completion to `sn-impl-em2`. If blocked, ask `sn-impl-em2`.

You MUST write the code, verify the build and tests, commit, push, write the log entry, and then mark the task complete.

# Isolate delete --force workaround for clean removal

**Date:** 2026-08-26
**Author:** dev-workaround-isolate
**Branch:** scion/dev-workaround-isolate
**Commit:** refactor(runtime): isolate delete --force workaround for clean removal

## Summary

Restructured the `sandbox delete --force` workaround code so it lives in a
single file (`pkg/runtime/cloudrun_sandbox_delete_workaround.go`) that can be
`git rm`'d when the upstream Cloud Run Sandbox platform bug is fixed.

## What changed

### New files

- **`pkg/runtime/cloudrun_sandbox_runtime.go`** — `CloudRunSandboxRuntime`
  struct implementing the `Runtime` interface for gVisor sandboxes inside a
  Cloud Run Instance. `Stop()` and `Delete()` both dispatch through
  `deleteOrWorkaround()` which routes to the timeout workaround or the plain
  `sandbox delete --force` path based on a runtime kill switch.

- **`pkg/runtime/cloudrun_sandbox_delete_workaround.go`** — All workaround
  code in one removable file:
  - `deleteWithTimeout` — runs delete with a bounded wait; treats timeout as
    success since the delete is effective despite not returning.
  - `killProcessGroup` — SIGKILL to the process group (negative PID).
  - `reapOrphanedRunsc` — scans `/proc` for orphaned `runsc delete` processes.
  - `isOrphanedRunscProcess` — NUL-split argv matching helper.
  - `deleteDefectRef` / `DefaultDeleteTimeout` constants.
  - `deleteWorkaroundEnabled` kill switch (env: `SCION_CLOUDRUN_DELETE_WORKAROUND=off`).
  - `deleteWorkaroundFixDetected` — `sync.Once` WARN when delete returns
    normally (primary removal trigger since there is no public bug to watch).

- **`pkg/runtime/cloudrun_sandbox_delete_workaround_test.go`** — 10 tests:
  6 for `deleteWithTimeout` (normal, timeout, context cancel, error non-fatal,
  watcher cancel, default timeout), 2 for `killProcessGroup`, 1 for
  `reapOrphanedRunsc`, 1 table-driven test for `isOrphanedRunscProcess` with
  7 cases.

- **`.design/project-log/defect-sandbox-delete-hang.md`** — Defect
  investigation report copied from scratchpad into the repo. The workaround
  file's comments reference this path.

### Critical safety fix: conditional reaping

`reapOrphanedRunsc` now runs **only** inside the timeout branch of
`deleteWithTimeout`. Previously (in the planned design), it would have run
unconditionally after every delete — meaning that when the platform bug is
fixed and delete returns promptly, the reaper would SIGKILL a healthy
in-flight `runsc` process matching the same pattern. This is the most
important behavioral change.

### Design decisions

1. **Watcher cancel in `deleteOrWorkaround`** — placed before dispatching so
   neither `deleteWithTimeout` nor `deletePlain` carries it. This means
   removing the workaround file does not lose the watcher cancel logic.

2. **`exec.Command` (not `exec.CommandContext`) in `deleteWithTimeout`** — we
   handle context cancellation explicitly in the select to avoid a race
   between the context's process-kill and our process-group kill, and to
   ensure the correct error path is taken.

3. **Process errors are non-fatal in the workaround path** — the platform bug
   means we cannot trust the delete command's exit status, so errors are
   logged but the method returns nil. The `deletePlain` path (kill switch off)
   propagates errors normally.

## Removal instructions

When the upstream fix ships:
1. Set `SCION_CLOUDRUN_DELETE_WORKAROUND=off` and verify deletes work
2. `git rm pkg/runtime/cloudrun_sandbox_delete_workaround.go`
3. `git rm pkg/runtime/cloudrun_sandbox_delete_workaround_test.go`
4. Revert `Stop()`/`Delete()` in `cloudrun_sandbox_runtime.go` to call
   `deletePlain` directly (or inline the exec)
5. Drop the `deleteWorkaroundEnabled` variable and `init()`

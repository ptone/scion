# Brief: dev-p4a-delete -- P4a timeout-bounded Delete

## Task

Fix the `sandbox delete --force` hang in the `cloudrun-sandbox` runtime. This is
phase P4a: a teardown-correctness fix on the normal Tier 0 lifecycle. Every redeploy
deletes the entire fleet of sandboxes, so this blocks the normal operational path.

**This phase is deliberately split from P4 (the terminal feature).** It is a
standalone fix.

## Starting point

**Branch:** `scion/dev-rebase-1294` (current HEAD). Create your work branch from it:

```bash
git checkout scion/dev-rebase-1294
git checkout -b scion/p4a-delete
```

Push your work branch regularly for durability. **Never push to the integration
branch.**

## The defect

`sandbox delete --force` **never returns.** Full write-up:
`/scion-volumes/scratchpad/projects/single-node/defect-sandbox-delete-hang.md` --
read it, it is short and authoritative.

Key facts from empirical testing:
- `sandbox delete --force` hangs indefinitely (>90 seconds observed, never completes)
- The deletion IS effective despite not returning -- `sandbox exec` on the deleted
  sandbox reports "not running"
- An orphaned `runsc ... delete --force` process is left behind
- **Plain `delete` (without --force) is MORE dangerous** -- it refuses in 209ms AND
  kills the sandbox anyway, leaving live `runsc-gofer`/`runsc-sandbox` processes
  behind a CLI that reports "not running". Never fall back to it.

## What to build

### 1. Timeout-bounded Delete

Modify `Stop()` and `Delete()` in `pkg/runtime/cloudrun_sandbox_runtime.go` (lines
650-670).

Current implementation:
```go
func (r *CloudRunSandboxRuntime) Delete(ctx context.Context, id string) error {
    _, err := runSimpleCommand(ctx, r.bin, "delete", "--force", id)
    if err != nil {
        return fmt.Errorf("cloudrun-sandbox: delete failed: %w", err)
    }
    r.state.remove(id)
    return nil
}
```

New implementation must:

1. **Issue `--force` and bound it with a configurable timeout.** Do NOT use
   `runSimpleCommand` -- it waits for the process to exit, which never happens.
   Instead, start the process, wait with a timeout, and treat timeout as success.

2. **Treat the timeout as success.** The deletion really is effective -- the sandbox
   is gone. Log a warning when the timeout fires so operators can see it.

3. **Reap the orphaned process** rather than leaving it. After timeout, kill the
   process and wait for it to exit. Do not rely on the OS eventually reaping zombies.

4. **Never fall back to plain `delete` (without --force).** Add a code comment
   explaining why: plain delete refuses AND kills anyway, leaving orphaned processes
   behind a CLI that reports "not running". This is the more dangerous defect.

5. **Make the timeout configurable.** Add a field to `CloudRunSandboxRuntime`:

```go
type CloudRunSandboxRuntime struct {
    bin          string
    state        *sandboxStateStore
    rootDir      string
    deleteTimeout time.Duration  // timeout for sandbox delete --force; 0 = use default
}
```

With a default (suggest 10 seconds -- the value is picked blind since delete never
completes; say this in the code comment).

### 2. Suggested implementation pattern

```go
func (r *CloudRunSandboxRuntime) deleteWithTimeout(ctx context.Context, id string) error {
    timeout := r.deleteTimeout
    if timeout == 0 {
        // Default timeout for sandbox delete --force. This value is picked
        // blind -- we have no data on the completion-time distribution because
        // the command never completes (platform defect, see
        // defect-sandbox-delete-hang.md). The timeout is a guess.
        timeout = 10 * time.Second
    }

    cmd := exec.CommandContext(ctx, r.bin, "delete", "--force", id)
    if err := cmd.Start(); err != nil {
        return fmt.Errorf("cloudrun-sandbox: failed to start delete: %w", err)
    }

    done := make(chan error, 1)
    go func() { done <- cmd.Wait() }()

    select {
    case err := <-done:
        // delete actually returned (unexpected but fine)
        if err != nil {
            slog.Warn("sandbox delete --force returned with error",
                "sandbox", id, "error", err)
        }
    case <-time.After(timeout):
        // Timeout: deletion IS effective (the sandbox is really gone) but
        // the CLI process hangs. Kill it and reap the zombie.
        slog.Warn("sandbox delete --force timed out, treating as success",
            "sandbox", id, "timeout", timeout,
            "note", "This is a known platform defect (defect-sandbox-delete-hang.md). "+
                "The sandbox is deleted despite the hang.")
        if cmd.Process != nil {
            _ = cmd.Process.Kill()
            <-done // reap
        }
    case <-ctx.Done():
        if cmd.Process != nil {
            _ = cmd.Process.Kill()
            <-done
        }
        return ctx.Err()
    }

    r.state.remove(id)
    return nil
}
```

Both `Stop()` and `Delete()` should delegate to this helper.

### 3. Reap orphaned `runsc` processes (defensive)

After the timeout kill, the `runsc ... delete --force` child may itself have orphaned
children. Add a defensive reap of any lingering `runsc` processes for this sandbox.
Check for lingering processes:

```go
// reapOrphanedRunsc kills any lingering runsc processes for a deleted sandbox.
// sandbox delete --force spawns a runsc delete subprocess that can itself orphan.
func reapOrphanedRunsc(sandboxName string) {
    // Look for runsc processes related to this sandbox
    // Pattern: runsc --root /run/sandbox/<name>/runc delete --force <name>
    // Use pkill or manual /proc scan
}
```

This is defensive -- orphans eventually become zombies and are reaped, but we should
not rely on that during fleet teardown where many sandboxes are deleted at once.

### 4. OQ-16: Concurrent delete behavior

The design doc flags **OQ-16** as open: every observation of the hang is a serial
delete. Fan-out is the actual pattern (fleet teardown deletes all sandboxes). If the
hang is contention-related, concurrent teardown could be qualitatively worse.

**Your responsibility:** Either:
- (a) Test concurrent deletes on a throwaway Instance and document the result, OR
- (b) Ship with a documented concurrency cap and explicitly say you chose not to test it

Do NOT ship silently assuming it composes. If you cannot test (no cloud access), add a
code comment documenting that OQ-16 is unresolved and add a `// TODO(OQ-16):` marker.

The EM brief says you may close OQ-16 or explicitly accept it -- either is fine, but
it must be a deliberate choice.

### 5. Update the watcher goroutine

Check `watchSandbox()` (line 797+) in `cloudrun_sandbox_runtime.go`. The watcher runs
`sandbox wait` which blocks until the sandbox exits. If `sandbox wait` also hangs on a
sandbox that was force-deleted, the watcher goroutine leaks. Consider:
- After `deleteWithTimeout` returns, the watcher's context should be cancelled
- Or the watcher should have its own timeout

## What NOT to do

- Do NOT touch `GetLogs()`, `Attach()`, or `Exec()` -- P4 owns those.
- Do NOT touch `pty_handlers.go` -- P4 owns that.
- Do NOT touch `pkg/hub/` -- P5 owns that.
- Do NOT fall back to plain `delete` (without --force) under any circumstances.

## Build verification

```bash
cd /workspace && go build ./...
go vet ./pkg/runtime/...
```

## Deliverables

1. All changes committed and pushed to `scion/p4a-delete`
2. `Stop()` and `Delete()` use timeout-bounded delete with process reaping
3. Timeout is configurable with a documented default
4. Code comments explain the platform defect and why plain delete is forbidden
5. OQ-16 is either tested or explicitly accepted with documentation
6. `go build ./...` and `go vet ./...` pass
7. A project log entry at `/workspace/.design/project-log/p4a-delete.md`

You MUST write the project log entry, commit, push your branch, and then mark the
task complete.

## Reporting

- **Blocked or design questions:** message `sn-impl-arch`
- **Status updates:** message `sn-impl-em2` (me, your EM)

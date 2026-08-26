# Brief: dev-p4a-redo -- Apply timeout-bounded delete to existing runtime

## Task

Apply the timeout-bounded `sandbox delete --force` fix to the EXISTING
`cloudrun_sandbox_runtime.go` on the integration branch. A previous developer
implemented this correctly but on the wrong base (branched from `main` where the
file doesn't exist, so rewrote the entire runtime). Your job is to apply ONLY the
delete logic to the EXISTING file.

## Starting point

**Branch:** `scion/dev-rebase-1294` (the integration branch with P0-P3 work).
Create your work branch from it:

```bash
git checkout scion/dev-rebase-1294
git checkout -b scion/p4a-delete-v2
```

Push your work branch regularly. **Never push to the integration branch.**

## The existing code you are modifying

`pkg/runtime/cloudrun_sandbox_runtime.go` — this file already exists with ~845 lines,
including a full `CloudRunSandboxRuntime` struct, `sandboxStateStore` (JSON-backed),
`Run()`, `List()`, `GetLogs()`, `Attach()`, `Exec()`, etc. from P0-P3.

The current `Stop()` and `Delete()` methods (around lines 650-670) both call
`runSimpleCommand(ctx, r.bin, "delete", "--force", id)` which blocks forever due to
a platform defect. You are fixing ONLY these methods.

## What to implement

### 1. Add the `deleteTimeout` field and constant

Add to the `CloudRunSandboxRuntime` struct (around line 536):

```go
type CloudRunSandboxRuntime struct {
    bin           string
    state         *sandboxStateStore
    rootDir       string
    deleteTimeout time.Duration  // timeout for sandbox delete --force; 0 = default
}
```

Add a package-level constant:

```go
// DefaultDeleteTimeout is the timeout for sandbox delete --force.
// This value is picked blind -- we have no data on the completion-time
// distribution because the command never completes (platform defect, see
// defect-sandbox-delete-hang.md). 10 seconds is a conservative guess.
const DefaultDeleteTimeout = 10 * time.Second
```

### 2. Replace Stop() and Delete()

Replace the current `Stop()` (line ~650) and `Delete()` (line ~661) with:

```go
func (r *CloudRunSandboxRuntime) Stop(ctx context.Context, id string) error {
    // sandbox delete requires --force for running sandboxes.
    // There is no stop/pause verb; Stop == Delete.
    return r.deleteWithTimeout(ctx, id)
}

func (r *CloudRunSandboxRuntime) Delete(ctx context.Context, id string) error {
    // Always use --force: sandbox delete without it silently fails for
    // running sandboxes. NEVER fall back to plain delete (without --force) --
    // it refuses AND kills the sandbox anyway, leaving orphaned
    // runsc-gofer/runsc-sandbox processes behind a CLI that reports "not
    // running". This is the more dangerous defect. See
    // defect-sandbox-delete-hang.md.
    return r.deleteWithTimeout(ctx, id)
}
```

### 3. Add the deleteWithTimeout helper

Add this method to `CloudRunSandboxRuntime`:

```go
// deleteWithTimeout issues `sandbox delete --force` and bounds the wait
// with a configurable timeout.
//
// Platform defect: `sandbox delete --force` never returns (see
// defect-sandbox-delete-hang.md). The deletion IS effective -- the sandbox
// really is gone -- but the CLI process hangs indefinitely.
//
// TODO(OQ-16): Every observation of the hang is from serial deletes.
// Fan-out is the actual pattern (fleet teardown). If the hang is
// contention-related, concurrent teardown could be qualitatively worse.
// Explicitly accepted: the timeout bounds the worst case per-sandbox,
// but aggregate behavior under concurrency is unverified.
func (r *CloudRunSandboxRuntime) deleteWithTimeout(ctx context.Context, id string) error {
    timeout := r.deleteTimeout
    if timeout == 0 {
        timeout = DefaultDeleteTimeout
    }

    cmd := exec.CommandContext(ctx, r.bin, "delete", "--force", id)
    // Use a process group so we can kill the entire tree (the sandbox CLI
    // spawns runsc subprocesses that inherit pipe fds).
    cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

    if err := cmd.Start(); err != nil {
        return fmt.Errorf("cloudrun-sandbox: failed to start delete --force: %w", err)
    }

    done := make(chan error, 1)
    go func() { done <- cmd.Wait() }()

    select {
    case err := <-done:
        if err != nil {
            slog.Warn("sandbox delete --force returned with error",
                "sandbox", id, "error", err)
        } else {
            slog.Info("sandbox delete --force completed normally",
                "sandbox", id)
        }
    case <-time.After(timeout):
        slog.Warn("sandbox delete --force timed out, treating as success",
            "sandbox", id, "timeout", timeout,
            "note", "Known platform defect (defect-sandbox-delete-hang.md). "+
                "The sandbox is deleted despite the hang.")
        killProcessGroup(cmd)
        <-done // reap the zombie
    case <-ctx.Done():
        killProcessGroup(cmd)
        <-done
        return ctx.Err()
    }

    // Defensively reap any orphaned runsc processes for this sandbox.
    reapOrphanedRunsc(id)

    // Remove from state store.
    r.state.remove(id)
    return nil
}
```

### 4. Add helper functions

Add these as package-level functions (not methods):

```go
// killProcessGroup sends SIGKILL to the entire process group of cmd.
func killProcessGroup(cmd *exec.Cmd) {
    if cmd.Process == nil {
        return
    }
    _ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
}

// reapOrphanedRunsc kills any lingering runsc processes for a deleted sandbox.
// Pattern: runsc --root /run/sandbox/<name>/runc delete --force <name>
func reapOrphanedRunsc(sandboxName string) {
    entries, err := os.ReadDir("/proc")
    if err != nil {
        return
    }
    for _, entry := range entries {
        if !entry.IsDir() {
            continue
        }
        pid, err := strconv.Atoi(entry.Name())
        if err != nil {
            continue
        }
        cmdline, err := os.ReadFile(filepath.Join("/proc", entry.Name(), "cmdline"))
        if err != nil {
            continue
        }
        args := string(bytes.ReplaceAll(cmdline, []byte{0}, []byte(" ")))
        if strings.Contains(args, "runsc") &&
            strings.Contains(args, "delete") &&
            strings.Contains(args, sandboxName) {
            slog.Info("reaping orphaned runsc process",
                "sandbox", sandboxName, "pid", pid,
                "cmdline", strings.TrimSpace(args))
            if proc, err := os.FindProcess(pid); err == nil {
                _ = proc.Kill()
                waitDone := make(chan struct{})
                go func() {
                    _, _ = proc.Wait()
                    close(waitDone)
                }()
                select {
                case <-waitDone:
                case <-time.After(2 * time.Second):
                }
            }
        }
    }
}
```

### 5. Fix the watcher goroutine

The current `watchSandbox()` (line ~803) runs `sandbox wait` without a context,
so it would hang if the sandbox is force-deleted. Change it to use a context
that gets cancelled when the sandbox is deleted:

Modify the `watchSandbox` method to accept a context:

```go
func (r *CloudRunSandboxRuntime) watchSandbox(ctx context.Context, name string) {
    cmd := exec.CommandContext(ctx, r.bin, "wait", name)
    out, err := cmd.CombinedOutput()
    // ... rest unchanged, but check for ctx.Err() after the call
```

And in `Run()` where `watchSandbox` is called (around line 645), create a context
that can be cancelled:

```go
// Start watcher with a context that deleteWithTimeout will cancel.
watchCtx, watchCancel := context.WithCancel(context.Background())
go r.watchSandbox(watchCtx, slug)
```

Store the `watchCancel` function so `deleteWithTimeout` can cancel it. You could:
- Add a `watchCancels map[string]context.CancelFunc` field to the runtime, or
- Store it alongside the state entry

### 6. Add required imports

Make sure to add `"bytes"`, `"syscall"` to the imports. `"strconv"` and `"os"` 
should already be there.

### 7. Add tests

Add tests in `pkg/runtime/cloudrun_sandbox_runtime_test.go` for:
- `deleteWithTimeout` completes normally when the process exits quickly
- `deleteWithTimeout` treats timeout as success
- `deleteWithTimeout` handles context cancellation
- `killProcessGroup` with nil process
- `reapOrphanedRunsc` with no /proc (graceful no-op)

Use `exec.Command("sleep", "60")` as a stand-in for a hanging process to test
the timeout path.

## What NOT to do

- Do NOT rewrite or restructure the existing file -- only add/modify the specific
  methods listed above
- Do NOT change `Run()`, `List()`, `GetLogs()`, `Attach()`, `Exec()`, or any
  other method
- Do NOT change the `sandboxStateStore` struct or its serialization format
- Do NOT touch `pty_handlers.go` or `pkg/hub/`
- Do NOT change the `NewCloudRunSandboxRuntime` signature (it takes
  `*config.V1CloudRunSandboxConfig`, not `V1CloudRunInstancesConfig`)

## Build verification

```bash
cd /workspace && go build ./...
go vet ./pkg/runtime/...
go test ./pkg/runtime/... -run 'TestCloudRunSandbox' -count=1 -v
```

Known pre-existing test failure: `internal/fixturegen/fixturegen_test.go` -- ignore it.

## Deliverables

1. All changes committed and pushed to `scion/p4a-delete-v2`
2. `Stop()` and `Delete()` use timeout-bounded delete with process reaping
3. `deleteWithTimeout`, `killProcessGroup`, `reapOrphanedRunsc` added
4. Watcher goroutine context-cancelled on delete
5. Tests for the timeout path
6. `go build ./...` and `go vet ./...` pass
7. A project log entry at `/workspace/.design/project-log/p4a-delete.md`

You MUST write the project log entry, commit, push your branch, and then mark the
task complete.

## Reporting

- **Blocked or design questions:** message `sn-impl-arch`
- **Status updates:** message `sn-impl-em2` (me, your EM)

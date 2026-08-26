# Brief: dev-p4-exec -- P4 sandbox exec control plane

## Task

Implement the `sandbox exec` control plane for the `cloudrun-sandbox` runtime. This
is the P4 phase: `Attach` / `GetLogs` / `Exec` and the `pty_handlers.go` branches,
each carried into the sandbox via `sandbox exec`. **Browser terminal works** at the
end of this.

## Starting point

**Branch:** `scion/dev-rebase-1294` (current HEAD). Create your work branch from it:

```bash
git checkout scion/dev-rebase-1294
git checkout -b scion/p4-exec
```

Push your work branch regularly for durability. **Never push to the integration
branch** -- that is the EM's gate.

## What to build

The design is empirically validated and simpler than the Docker path. The production
attach command is:

```
/usr/local/gcp/bin/sandbox exec <sandbox-name> --env TERM=xterm-256color -- /usr/bin/tmux attach -t scion
```

driven by the launcher's existing `pty.StartWithSize`. **No mount, no `TMUX_TMPDIR`,
no `script`, no double PTY.** The sandbox binary is at `/usr/local/gcp/bin/sandbox`
(constant `defaultSandboxBin` in `cloudrun_sandbox_runtime.go`, line 39). Inside a
sandbox, `PATH` is empty -- all binaries must use absolute paths.

### Part 1: Runtime methods in `pkg/runtime/cloudrun_sandbox_runtime.go`

Three stub methods need real implementations:

#### 1a. `GetLogs()` (line 740)

Use `sandbox exec` to capture tmux pane content:

```go
func (r *CloudRunSandboxRuntime) GetLogs(ctx context.Context, id string) (string, error) {
    // Use tmux capture-pane to get the scrollback buffer.
    // Absolute paths required -- PATH is empty inside a sandbox.
    return runSimpleCommand(ctx, r.bin, "exec", id, "--", "/usr/bin/tmux", "capture-pane", "-p", "-t", "scion", "-S", "-1000")
}
```

This mirrors how Docker's GetLogs works but via tmux capture-pane instead of
`docker logs`, since sandboxes have no log command.

#### 1b. `Attach()` (line 744)

The `Attach()` method on the Runtime interface is for **CLI attach** (the `scion attach`
command), not the browser terminal. The browser terminal goes through pty_handlers.go
(Part 2 below). For CLI attach, implement similarly to Docker's Attach:

```go
func (r *CloudRunSandboxRuntime) Attach(ctx context.Context, id string) error {
    // Look up the sandbox to verify it exists and is running
    entry := r.state.get(id)
    if entry == nil {
        return fmt.Errorf("cloudrun-sandbox: sandbox %q not found", id)
    }
    // Set tmux window-size to latest for proper resize behavior
    _, _ = runSimpleCommand(ctx, r.bin, "exec", id, "--",
        "/usr/bin/tmux", "set-option", "-g", "window-size", "latest")
    // Interactive attach via sandbox exec.
    // TERM=xterm-256color is load-bearing: without it tmux sees TERM=dumb
    // and exits with "terminal does not support clear" -- which looks like
    // a PTY failure but is not one (design doc section 4.4a-rev).
    return runInteractiveCommand(r.bin, "exec", id,
        "--env", "TERM=xterm-256color", "--",
        "/usr/bin/tmux", "attach-session", "-t", "scion")
}
```

#### 1c. `Exec()` (line 782)

```go
func (r *CloudRunSandboxRuntime) Exec(ctx context.Context, id string, cmd []string) (string, error) {
    // sandbox exec has no --user flag; the process runs as the sandbox's
    // configured user (which is the scion user via the omni-image entrypoint).
    // Absolute paths: PATH is empty inside a sandbox, so callers must provide
    // absolute paths or the command must be on a bind-mounted path.
    args := append([]string{"exec", id, "--"}, cmd...)
    return runSimpleCommand(ctx, r.bin, args...)
}
```

### Part 2: PTY handler branches in `pkg/runtimebroker/pty_handlers.go`

This is the browser terminal path. There are **six sites** that need cloudrun-sandbox
branches, replacing the current stub error returns. The pattern at each site is:
detect `isCloudRunSandbox` (already detected at each site), then use `sandbox exec`
with the correct arguments instead of `docker exec`.

**Critical**: the `runtimeCmd` variable in pty_handlers.go is `"cloudrun-sandbox"` (the
runtime name), NOT a binary path. For the sandbox branch, you must use the actual
binary path `/usr/local/gcp/bin/sandbox`. Import the constant or hardcode it -- the
constant `defaultSandboxBin` is in `pkg/runtime/cloudrun_sandbox_runtime.go` (unexported).
**Export it or define a local constant.**

#### 2a. `waitForTmuxSession()` (line 143, stub at 155-157)

Replace the stub with a polling loop that uses sandbox exec to check tmux:

```go
if isCloudRunSandbox {
    for {
        select {
        case <-ctx.Done():
            return fmt.Errorf("timed out waiting for tmux session in sandbox '%s'", containerID)
        case <-ticker.C:
            cmd := exec.CommandContext(ctx, sandboxBin, "exec", containerID, "--",
                "/usr/bin/tmux", "has-session", "-t", "scion")
            if cmd.Run() == nil {
                return nil
            }
            slog.Debug("Waiting for tmux session", "sandbox", containerID, "runtime", runtimeCmd)
        }
    }
}
```

#### 2b. `queryTmuxActiveWindow()` (line 84, line 88)

This function currently uses `runtimeCmd` as a binary with no cloudrun-sandbox guard.
Add a cloudrun-sandbox branch:

```go
func queryTmuxActiveWindow(ctx context.Context, runtimeCmd, containerID, execUser string) string {
    ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
    defer cancel()

    var cmd *exec.Cmd
    if runtimeCmd == "cloudrun-sandbox" {
        cmd = exec.CommandContext(ctx, sandboxBin, "exec", containerID, "--",
            "/usr/bin/tmux", "display-message", "-t", "scion", "-p", "#{window_name}")
    } else {
        cmd = exec.CommandContext(ctx, runtimeCmd, "exec", "--user", execUser, containerID,
            "tmux", "display-message", "-t", "scion", "-p", "#{window_name}")
    }
    // ... rest unchanged
}
```

#### 2c. `LocalPTYSession.Run()` (line 358, stub at 362-363)

Replace the stub. For cloudrun-sandbox, call a new `startCloudRunSandboxExec()` method
(parallel to `startDockerExec()`):

```go
if isCloudRunSandbox {
    if err := s.startCloudRunSandboxExec(); err != nil {
        return fmt.Errorf("failed to start sandbox exec: %w", err)
    }
    // Send initial active window
    if wn := queryTmuxActiveWindow(s.ctx, s.runtimeCmd, s.containerID, s.execUser); wn != "" {
        msg := wsprotocol.NewPTYDataMessage(activeWindowOSC(wn))
        _ = s.writeToWebSocket(msg)
    }
    // ... then fall through to the same read/write/resize loop as Docker
}
```

#### 2d. New method: `LocalPTYSession.startCloudRunSandboxExec()`

```go
// startCloudRunSandboxExec starts a sandbox exec session with tmux attach using a real PTY.
// Unlike Docker, sandbox exec has no --user flag and requires --env for TERM.
// TERM=xterm-256color is load-bearing: without it the inner tmux sees TERM=dumb
// and exits with "terminal does not support clear" (design doc section 4.4a-rev).
func (s *LocalPTYSession) startCloudRunSandboxExec() error {
    if err := waitForTmuxSession(s.ctx, s.runtimeCmd, s.containerID, s.namespace, s.execUser, nil, nil); err != nil {
        return err
    }

    args := []string{
        "exec", s.containerID,
        "--env", "TERM=xterm-256color",
        "--", "/usr/bin/tmux", "attach-session", "-t", "scion",
    }

    s.cmd = exec.CommandContext(s.ctx, sandboxBin, args...)

    ptmx, err := pty.StartWithSize(s.cmd, &pty.Winsize{
        Cols: uint16(s.cols),
        Rows: uint16(s.rows),
    })
    if err != nil {
        return fmt.Errorf("failed to start sandbox exec with PTY: %w", err)
    }

    s.ptyMaster = ptmx
    s.ptySlave = ptmx
    return nil
}
```

#### 2e. `StreamPTYHandler.Run()` (line 703, stub at 711-712) + new `startCloudRunSandboxExec()`

Same pattern as 2c/2d but for the `StreamPTYHandler`. Replace the stub with a call to
a new `StreamPTYHandler.startCloudRunSandboxExec()` method, mirroring the structure.

#### 2f. Resize handling -- **CRITICAL**

SIGWINCH does NOT cross the sandbox boundary. PTY fd properties propagate but signal
delivery does not. The current Docker resize path uses `pty.Setsize()` on the launcher
PTY, which changes the launcher-side window size -- but the inner tmux never learns.

**Fix:** When `isCloudRunSandbox`, the resize handler must issue an **out-of-band
`sandbox exec`** to relay the resize:

```go
// In handleResize() for both LocalPTYSession and StreamPTYHandler:
if isCloudRunSandbox {
    // SIGWINCH does not cross the sandbox boundary -- PTY fd properties propagate
    // but signal delivery does not (design doc section 4.4a-rev). Resize must be
    // relayed out-of-band via tmux resize-window.
    // NOTE: NOT refresh-client -C -- that needs a control-mode client and is the
    // wrong tool (confirmed by spike-uds-b).
    resizeCmd := exec.CommandContext(ctx, sandboxBin, "exec", containerID, "--",
        "/usr/bin/tmux", "resize-window", "-t", "scion", "-x", strconv.Itoa(cols), "-y", strconv.Itoa(rows))
    if err := resizeCmd.Run(); err != nil {
        slog.Debug("Sandbox tmux resize failed", "slug", slug, "error", err)
    }
}
// ALSO still do the launcher-side pty.Setsize -- both are needed.
```

For `LocalPTYSession`, the resize happens in the goroutine reading resize messages from
the WebSocket (around line 520-538). For `StreamPTYHandler`, it's in `handleResize()`
(line 910-931).

### Part 3: Make the sandbox binary path accessible to pty_handlers.go

The sandbox binary path (`/usr/local/gcp/bin/sandbox`) is defined as `defaultSandboxBin`
in `cloudrun_sandbox_runtime.go` but is unexported. pty_handlers.go is in a different
package (`pkg/runtimebroker`). Options:

**Recommended:** Define a constant in pty_handlers.go (or a shared location) for the
sandbox binary path. A package-level const is fine:

```go
// cloudRunSandboxBin is the platform-injected sandbox binary path.
// It is injected at deploy time when --sandbox-launcher is enabled
// and is never part of the container image.
const cloudRunSandboxBin = "/usr/local/gcp/bin/sandbox"
```

### Part 4: Verify non-interactive exec paths work

`scion look` and `scion message` use `Exec()` (non-interactive pipe-based). These
should already work via the `Exec()` implementation above. **Confirm** by reading the
call sites and verifying the command construction is correct.

## Five load-bearing details (from the design doc, measured empirically)

1. **`--env TERM=xterm-256color` is required.** Without it tmux exits with "terminal
   does not support clear". Put a code comment explaining this at every site.

2. **SIGWINCH does not cross.** Resize must be a second, out-of-band `sandbox exec`
   running `tmux resize-window -t scion -x W -y H`. **Not `refresh-client -C`** --
   that needs a control-mode client.

3. **PATH is empty inside sandboxes.** Every binary reference must use absolute paths:
   `/usr/bin/tmux`, `/usr/bin/script`, etc.

4. **No `--user` flag on sandbox exec.** The process runs as whatever user the sandbox
   image specifies. The omni-image runs as scion.

5. **Use sandbox name, not container ID.** The `containerID` parameter in pty_handlers
   will contain the sandbox name (the slug). Sandbox CLI uses names, not Docker-style
   container IDs.

## What NOT to do

- Do NOT touch `Stop()` or `Delete()` methods -- P4a owns those.
- Do NOT touch `pkg/hub/project_workspace_handlers.go` -- P5 owns that.
- Do NOT touch the K8s code paths.
- Do NOT modify Docker/Podman behavior -- only add cloudrun-sandbox branches.
- Do NOT introduce a `script` or `util-linux` dependency -- empirical testing confirmed
  it is unnecessary.

## Build verification

```bash
cd /workspace && go build ./...
go vet ./pkg/runtime/... ./pkg/runtimebroker/...
```

The build must pass. There is a known pre-existing test failure in
`internal/fixturegen/fixturegen_test.go` (expectedTableCount=42 vs 46) -- ignore it,
it fails on unmodified main too.

## Deliverables

1. All changes committed and pushed to `scion/p4-exec`
2. `GetLogs()`, `Attach()`, `Exec()` implemented in `cloudrun_sandbox_runtime.go`
3. All five pty_handlers.go stubs replaced with working cloudrun-sandbox branches
4. Resize handler issues out-of-band `tmux resize-window` for cloudrun-sandbox
5. Code comments at every `TERM=xterm-256color` site explaining why it is load-bearing
6. `go build ./...` and `go vet ./...` pass
7. A project log entry at `/workspace/.design/project-log/p4-exec.md`

You MUST write the project log entry, commit, push your branch, and then mark the
task complete.

## Reporting

- **Blocked or design questions:** message `sn-impl-arch` (the architect)
- **Status updates:** message `sn-impl-em2` (me, your EM)

# Investigation Report — #1661 PTY Container Cleanup Residual

Candidate: `30d0789fe43955e64065f70d9bb7e0f2d212abf1`
Investigator: dev-p3-1661-investigate
Date: 2026-09-20
Status: Read-only source analysis complete. #1661 remains open.

---

## 1. Causal Model

The cleanup path from WebSocket close/error through to container process
termination has six stages. Each step is marked **CONFIRMED** (source evidence +
broker logs), **INFERRED** (consistent with evidence, not directly observable in
source), or **UNKNOWN** (cannot be determined from source alone).

### Stage 1: Transport error detection (hub side)

| Step | Evidence | Status |
|------|----------|--------|
| Client WS close (1000 "detach", 1000 empty, or 1006 EOF) reaches hub | Broker logs for B1, B2, C, R2 | CONFIRMED |
| Hub `PTYSession.readFromClient()` returns WS error | `hub/pty_handlers.go:263` — `conn.ReadMessage()` returns error | CONFIRMED |
| Hub `PTYSession.Run()` calls `s.Close()` | `hub/pty_handlers.go:245-246` — first error from `errCh` triggers Close | CONFIRMED |
| Close reason text ("detach" vs empty vs EOF) is NOT inspected | Source-traced: `Close()` at `hub/pty_handlers.go:349` does not examine the error | CONFIRMED |

All three failure modes (browser explicit close, CLI SIGTERM, browser SIGKILL)
converge on the same `PTYSession.Close()` call. The WS close code and reason
text do not influence any subsequent branch.

### Stage 2: Hub → broker stream teardown

| Step | Evidence | Status |
|------|----------|--------|
| `PTYSession.Close()` cancels context, then calls `controlChan.CloseStream(brokerID, streamID, "session closed")` | `hub/pty_handlers.go:358-362` | CONFIRMED |
| `BrokerConnection.CloseStream()` deletes stream from hub map, closes hub-side `StreamProxy`, sends `StreamCloseMessage` to broker over control-channel WS | `hub/controlchannel.go:671-684` | CONFIRMED |
| Hub writes WS close frame to client (may fail if connection is broken) and closes underlying connection | `hub/pty_handlers.go:366-373`; constraint: gorilla `Conn.Close` does not send a close frame | CONFIRMED |

### Stage 3: Broker stream shutdown

| Step | Evidence | Status |
|------|----------|--------|
| Broker `handleStreamClose()` receives `StreamCloseMessage`, deletes stream from map, closes `handler.closeCh` | `controlchannel.go:676-703` | CONFIRMED |
| `readFromStream()` returns `io.EOF` immediately (select-driven on `closeCh`) | `pty_handlers.go:1244-1256` | CONFIRMED |
| `Run()` exits select on `errCh`, calls `h.cancel()` | `pty_handlers.go:968-973` | CONFIRMED |
| `Run()` joins `resizeDone` (handleResize exits promptly on ctx or closeCh) | `pty_handlers.go:977`, `pty_handlers.go:1132-1144` | CONFIRMED |
| `Run()` closes `h.ptyMaster` — this is the **first and only effective close** of the PTY master fd | `pty_handlers.go:982-984` | CONFIRMED |

### Stage 4: gracefulShutdownExec

| Step | Evidence | Status |
|------|----------|--------|
| Deferred `gracefulShutdownExec(cmd, ptyMaster, slug)` runs | `pty_handlers.go:933-942` | CONFIRMED |
| `ptyMaster.Close()` is a **no-op** — same `*os.File` already closed in Stage 3; Go's `poll.FD` tracks closed state | `pty_handlers.go:97-103`, comment at lines 92-96 | CONFIRMED |
| `cmd.Wait()` goroutine started; 3s timeout (`processExitGracePeriod`) | `pty_handlers.go:115-126` | CONFIRMED |
| **DIVERGENCE**: `cmd.Wait()` either returns within 3s or does not | Broker logs: "exited after hangup" vs "did not exit after hangup" | CONFIRMED |

### Stage 5: SIGTERM escalation (transport-loss/CLI paths only)

| Step | Evidence | Status |
|------|----------|--------|
| `cmd.Process.Signal(syscall.SIGTERM)` sent to host docker exec PID | `pty_handlers.go:131` | CONFIRMED |
| Docker exec exits within ~6ms of SIGTERM | Broker logs: "PTY exec exited after SIGTERM" | CONFIRMED |
| Container-side tmux client PID survives with PPid=0 | R1-c278-residual-capture.json, R2-b3212-residual-capture.json | CONFIRMED |
| Container tmux client count unchanged — tmux server does not detect the disconnect | R2-sampler.log: `clients=3` throughout 180s window | CONFIRMED |

### Stage 6: Docker-level container exec cleanup

| Step | Evidence | Status |
|------|----------|--------|
| When docker exec exits "after hangup": Docker daemon detects clean disconnect, cleans up container-side exec process | B1 and B2 sampler: container PID absent within ~1s | INFERRED |
| When docker exec exits "after SIGTERM": Docker daemon does NOT clean up container-side exec process | R1-C and R2 sampler: container PID survives indefinitely (PPid=0, stat Ss+) | INFERRED |

---

## 2. Divergence Point

The divergence occurs between Stage 4 (PTY master close) and docker exec's
response. The source code executing on both sides of the divergence is
**identical** — same function (`gracefulShutdownExec`), same parameters, same
`*os.File` close. The difference lies in the host-side `docker exec` process's
behavior, which is outside the project's source tree.

### What converges

| Aspect | Explicit Close | Transport Loss / CLI SIGTERM |
|--------|---------------|------|
| Hub cleanup entry point | `PTYSession.Close()` | `PTYSession.Close()` |
| StreamClose sent to broker | Yes (via control channel) | Yes (via control channel) |
| Broker closeCh closed | Yes | Yes |
| readFromStream exits | io.EOF (closeCh) | io.EOF (closeCh) |
| Run() joins resize, closes ptyMaster | Yes | Yes |
| gracefulShutdownExec called | Yes | Yes |
| ptyMaster.Close() in gracefulShutdownExec | No-op (already closed) | No-op (already closed) |
| PTY master fd actually closed | By `Run()` at line 983 | By `Run()` at line 983 |

### What diverges

| Aspect | Explicit Close | Transport Loss / CLI SIGTERM |
|--------|---------------|------|
| Broker log | "PTY exec exited after hangup" | "PTY exec did not exit after hangup, sending SIGTERM" |
| Docker exec exit latency | ~22ms after PTY close | >3s (does not exit from PTY close) |
| Docker exec exit cause | PTY hangup (SIGHUP or stdin EIO) | Our SIGTERM |
| Container process outcome | Exits (cleaned up) | Survives (orphaned, PPid=0) |

### Correlation

The evidence establishes a strict correlation between docker exec's exit mode and
container cleanup outcome:

- **"exited after hangup" → container cleaned up** (B1, B2: 2/2 cases)
- **"exited after SIGTERM" → container residual** (C, R2: 2/2 cases)

This correlation suggests that Docker's exec cleanup mechanism depends on how the
host exec client disconnects. A clean disconnect (docker exec exits normally after
detecting PTY hangup) triggers Docker daemon cleanup of the container-side
process. An abrupt disconnect (docker exec killed by external SIGTERM) does not.

---

## 3. Explicitly Unknown Points

### U1: Why does docker exec not exit from PTY hangup in the transport-loss case?

The PTY master close is identical in both cases. The expected kernel-level effects
are:
- SIGHUP delivered to the foreground process group of the slave's session
- Reads from the slave return EIO
- Writes to the slave return EIO

`pty.StartWithSize` (creack/pty v1.1.24) sets `SysProcAttr.Setsid = true` and
`SysProcAttr.Setctty = true`, establishing docker exec as the session leader with
the PTY slave as its controlling terminal. SIGHUP delivery conditions are met.

**Source-level analysis cannot explain** why docker exec exits within 22ms in the
explicit-close case but not within 3s in the transport-loss case. Possible
explanations (all INFERRED, none confirmed):

a. **Docker exec's I/O goroutine deadlock**: Docker exec likely runs two I/O
   goroutines (stdin→container and container→stdout) and waits for both. If the
   container→stdout goroutine is blocked reading from the Docker API (no container
   output available), it will not detect the PTY close until it attempts a write.
   If Docker exec uses `errgroup.Wait()` or similar, it waits for both goroutines,
   staying alive even after stdin returns EIO.

b. **Docker exec's SIGHUP handling**: Docker exec (Docker CLI, Go binary) may
   catch SIGHUP via `signal.Notify` and forward it to the container process via
   the Docker API. If the forwarding call blocks or the container doesn't exit
   promptly, docker exec may stay alive waiting for the container.

c. **Kernel signal delivery timing**: SIGHUP might not be delivered immediately
   after `close(2)` on the PTY master. Kernel scheduling or signal coalescing
   could delay delivery, though a >3s delay seems implausible.

### U2: Why does Docker daemon not clean up the container-side process when docker exec is killed by SIGTERM?

When docker exec exits via SIGTERM from our `cmd.Process.Signal()`:
- Docker exec's Go runtime terminates (signal not caught, or caught but process exits)
- The Docker API connection between docker exec (host) and dockerd is severed
- dockerd detects the disconnection
- dockerd should clean up the container-side exec process

**Source-level analysis cannot determine** whether Docker daemon handles abrupt
exec client disconnects differently from clean disconnects. The container-side
tmux client's survival with PPid=0 indicates Docker did not send SIGHUP (or any
signal) to the container process. The container process's `SigPnd=0000000000000000`
(no pending signals) is consistent with this.

### U3: Does docker exec forward SIGHUP to the container before exiting?

Docker CLI's signal forwarding behavior for `docker exec` is not observable from
our source. Whether SIGHUP received from PTY close is forwarded to the container
via the Docker API before docker exec exits is unknown. The 22ms exit latency in
the explicit-close case is consistent with SIGHUP being forwarded and the
container responding quickly, but it's also consistent with docker exec simply
exiting and Docker daemon performing the cleanup.

### U4: Container-side PTY state after host exec termination

The container-side tmux client has its own PTY pair (allocated by `docker exec
-t`). When the host docker exec exits, the container-side PTY master should be
closed by Docker daemon, which would deliver SIGHUP to the container tmux client.
Whether Docker daemon actually closes the container-side PTY master in the
SIGTERM-exit case cannot be determined from source.

---

## 4. Discriminating Experiment

### Experiment: Container output state during PTY close

**Objective**: Test whether docker exec's failure to exit from PTY hangup is
caused by its container→stdout goroutine being blocked in a Docker API read (no
container output to process).

**Rationale**: If docker exec uses an `errgroup.Wait()` pattern with separate
stdin→container and container→stdout goroutines, the container→stdout goroutine
must attempt a write to the PTY slave (stdout) to discover it is closed. If the
goroutine is blocked reading from the Docker API (container producing no output),
it cannot discover the PTY closure, and docker exec stays alive indefinitely.

**Setup** (same fixture, candidate 30d0789f, Docker runtime):

1. Baseline: agent 5fdc5a09 with running container, tmux session "scion", S
   (baseline client PID 29). Verify clean state.
2. Open browser B through Terminal UI. Verify broker stream established.
3. Open independent CLI C. Verify both clients present.
4. In tmux, start a continuous output command visible to the attached client:
   ```
   watch -n 0.1 date
   ```
   This produces output at 10Hz, ensuring the Docker API exec stream always has
   pending data.
5. Start external sampler (pre-fault): monitor B container PID, B host exec PID,
   C container PID, S PID, and tmux client count at ~1s intervals.

**Action**:

6. SIGKILL the owned Chromium process group (same method as R2: verified
   PID/startTicks/PGID). No UI Close.
7. Observe broker logs for cleanup sequence.

**Expected observations for each outcome**:

| Observation | Hypothesis confirmed | Hypothesis refuted |
|-------------|---------------------|-------------------|
| Broker log | "PTY exec exited after hangup" | "PTY exec did not exit after hangup, sending SIGTERM" |
| Docker exec exit latency | <1s after PTY close (immediate) | >3s (same as R2) |
| Container B PID | Absent within ~15s | Present beyond 15s (residual) |
| Client count | Drops by 1 within ~15s | Stays at 3 |

**If hypothesis confirmed**: The fix direction is to ensure the container-side
process is cleaned up even when docker exec's I/O goroutines don't exit from
PTY hangup. Options include:
- Sending an explicit `tmux detach-client` to the container before or after
  closing the PTY master
- Using `docker exec` to send a targeted SIGHUP to the container-side PID
- Closing the container-side PTY via `docker exec` to force SIGHUP delivery

**If hypothesis refuted**: The cause is elsewhere — possibly in Docker daemon's
exec cleanup, signal delivery, or docker exec's SIGHUP handling. Further
investigation would require:
- `strace` on the docker exec host process (non-destructive, host-side only)
  during the 3s hangup window to observe what syscall it is blocked in
- Sampling `/proc/<docker-exec-pid>/status` SigPnd during the 3s window to check
  if SIGHUP was delivered but not handled

**Complement experiment**: Repeat without `watch` (idle tmux shell). Expect
the same SIGTERM-required, container-residual behavior as R2, confirming that
idle output is the distinguishing factor.

**Perturbation bounds**:
- Non-destructive: only kills a specific browser process (verified ownership)
- Same candidate, same fixture, same observation method as UAT R2
- No container modification, no capability additions, no tracing inside container
- No code changes to the candidate
- Preserves independent clients (S, C)
- Coordinated through integration-design, not self-executed

---

## 5. Summary

The cleanup function `gracefulShutdownExec` at `pkg/runtimebroker/pty_handlers.go:97`
is correctly invoked in all cases. The close reason text ("detach", empty, or
1006 EOF) is never inspected. Both explicit-close and transport-loss paths
converge on the same function with identical parameters.

The failure is a **two-stage problem**:

1. **Docker exec does not exit from PTY hangup** in the transport-loss case
   (UNKNOWN cause, hypothesized to be docker exec's I/O goroutine blocked on
   Docker API read).

2. **Docker daemon does not clean up container-side exec** when docker exec is
   killed by external SIGTERM (INFERRED: abrupt disconnect vs. clean disconnect
   triggers different cleanup behavior in Docker daemon).

Both stages are outside the project's source code. The discriminating experiment
above is designed to isolate whether stage 1 is caused by Docker exec's I/O
goroutine state, which would inform the fix direction.

No code fix is proposed in this investigation. #1661 remains open.

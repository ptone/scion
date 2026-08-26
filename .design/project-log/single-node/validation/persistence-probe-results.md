# Long-Duration Persistence Probe Results

**Date:** 2026-08-26T03:48–04:19 UTC
**Instance:** `val-persist-em2` (fresh, dedicated instance — no interference from other agents)
**Image:** `docker.io/library/python:3.11` with `sandboxLauncher: true`
**runsc version:** `google-958767651` (spec 1.2.1) — confirmed known-bad build
**Sandbox name:** `val-persist-1787716110`

## Summary

| Time    | runsc-delete (PID 111) | runsc-state #1 (PID 124) | runsc-state #2 (PID 137) | runsc-state #3 (PID 150) |
|---------|------------------------|--------------------------|--------------------------|--------------------------|
| t=0     | S (ppid=103)           | S (ppid=116)             | S (ppid=129)             | S (ppid=142)             |
| t=1m    | S (ppid=103)           | S (ppid=116)             | S (ppid=129)             | S (ppid=142)             |
| t=2m    | S (ppid=103)           | S (ppid=116)             | S (ppid=129)             | S (ppid=142)             |
| t=2m10s | **GONE**               | **GONE**                 | **GONE**                 | **GONE**                 |
| t=3m    | GONE                   | GONE                     | GONE                     | GONE                     |
| t=5m    | GONE                   | GONE                     | GONE                     | GONE                     |
| t=10m   | GONE                   | GONE                     | GONE                     | GONE                     |
| t=30m   | GONE                   | GONE                     | GONE                     | GONE                     |

## Decisive Finding

**Both orphan types (runsc-delete and runsc-state) have identical persistence
profiles. They self-clean at 120s when the sandbox CLI wrapper's internal
timeout fires. The "worse persistence profile" claim from the defect doc
revision 2 is RETIRED.**

### The 2-Minute Boundary Event

At exactly t=2m0.5s after the delete was issued, all four sandbox CLI wrappers
exited simultaneously:

```
[end] exit_code=1 elapsed=120024ms  sandbox delete --force
[end] exit_code=1 elapsed=118514ms  sandbox exec (first)
[end] exit_code=1 elapsed=119018ms  sandbox exec (second)
[end] exit_code=1 elapsed=118014ms  sandbox exec (third)
```

The sandbox CLI has a **120-second internal timeout**. When this fires:
- The wrapper exits with rc=1
- **All runsc children exit with the wrapper** — they do NOT survive as orphans
- No zombies (state Z) were observed at any checkpoint
- No reparenting to PID 1 of runsc children was observed — children maintained
  their parent PID (the sandbox wrapper) throughout their lifetime
- After the wrapper exits, the children simply vanish

### Answer to the Architect's Question

> When the sandbox CLI wrapper exits after 120s, does the runsc child process
> (a) exit with it, (b) get reparented to PID 1 and survive, or (c) become
> a zombie?

**Answer: (a) — the runsc children exit with the wrapper.** The sandbox CLI
wrapper likely sends SIGKILL to its process group on timeout exit, taking all
children with it.

## Process Hierarchy at Baseline

The process tree while orphans are alive (t=0 through t=2m):

```
PID 1 (init)
├── PID 103 sandbox delete --force val-persist-1787716110  (ppid=1, state=S)
│   └── PID 111 runsc ... delete --force val-persist-...   (ppid=103, state=S)
├── PID 116 sandbox exec val-persist-1787716110 -- ...     (ppid=1, state=S)
│   └── PID 124 runsc ... state val-persist-...            (ppid=116, state=S)
├── PID 129 sandbox exec val-persist-1787716110 -- ...     (ppid=1, state=S)
│   └── PID 137 runsc ... state val-persist-...            (ppid=129, state=S)
└── PID 142 sandbox exec val-persist-1787716110 -- ...     (ppid=1, state=S)
    └── PID 150 runsc ... state val-persist-...            (ppid=142, state=S)
```

Note: The sandbox CLI wrappers themselves are reparented to PID 1 because their
parent (the Python Popen process) does not wait on them. The runsc children
maintain their sandbox wrapper as parent.

## Implications for the Workaround

1. **`deleteWithTimeout` (10s):** Abandons a wrapper that dies by itself 110s later.
   The wrapper's self-cleaning is a reliable backstop even without our timeout.

2. **`killProcessGroup`:** When our 10s timeout fires and we kill the process group,
   we are doing the same thing the wrapper's 120s timeout would do — just 110s
   earlier. This is correct and effective.

3. **`reapOrphanedRunsc`:** Still useful as belt-and-suspenders, but the orphans
   it targets have a 120s natural TTL. In practice, `reapOrphanedRunsc` should
   find zero orphans unless the Instance's PID 1 (init) fails to forward the
   wrapper's self-cleanup signal.

4. **`isOrphanedRunscProcess` scope:** The `runsc state` orphans (measurement
   artifacts from `sandbox exec`) share the exact same lifecycle as `runsc delete`
   orphans. Both self-clean at 120s. No special handling needed for `runsc state`.

## Limitations

1. This test was run on a single Instance with a single sandbox. The 120s wrapper
   timeout behavior is empirical — it may vary across runsc versions or platform
   configurations.

2. The sandbox CLI's internal timeout is not documented. We observed it as 120s
   on `google-958767651` (spec 1.2.1). Future versions may change this.

3. Between our 10s timeout and the wrapper's 120s timeout, there is a 110s window
   where the orphans are alive. During that window, `/proc` scans will find them.
   `reapOrphanedRunsc` handles this window.

---

## Raw Cloud Logging Output

```
Default STARTUP TCP probe succeeded after 1 attempt for container "worker" on port 8080.
Health check on :8080
Downloaded 13060 bytes
[2026-08-26T03:48:30Z] ======================================================================
[2026-08-26T03:48:30Z] PERSISTENCE PROBE v4 -- PPID tracking + 2m boundary cluster
[2026-08-26T03:48:30Z] ======================================================================
[2026-08-26T03:48:30Z] Health server port 8080 already in use ([Errno 98] Address already in use), skipping
[2026-08-26T03:48:30Z] runsc version: runsc version google-958767651
spec: 1.2.1
[2026-08-26T03:48:30Z] Sandbox name: val-persist-1787716110
[2026-08-26T03:48:30Z] Creating sandbox...
[start] cwd=/ "/usr/local/gcp/bin/sandbox run val-persist-1787716110 --detach --rootfs / --write -- /usr/bin/sleep 3600"
[end] exit_code=0 elapsed=151ms "/usr/local/gcp/bin/sandbox run val-persist-1787716110 --detach --rootfs / --write -- /usr/bin/sleep 3600"
[2026-08-26T03:48:30Z] Create rc=0 stdout=Running in detached mode: stdin, stdout and stderr arguments are ignored. stderr=
[2026-08-26T03:48:32Z] Verifying sandbox is reachable...
[start] cwd=/ "/usr/local/gcp/bin/sandbox exec val-persist-1787716110 -- /bin/echo alive"
[end] exit_code=0 elapsed=43ms "/usr/local/gcp/bin/sandbox exec val-persist-1787716110 -- /bin/echo alive"
[2026-08-26T03:48:32Z]   Verify attempt 1: rc=0 stdout='alive' stderr=''
[2026-08-26T03:48:32Z] Sandbox confirmed reachable!
[2026-08-26T03:48:34Z] Issuing delete --force (backgrounded, expected to hang)...
[2026-08-26T03:48:34Z] Delete Popen PID: 103
[start] cwd=/ "/usr/local/gcp/bin/sandbox delete --force val-persist-1787716110"
[2026-08-26T03:48:35Z] Spawning 3 exec calls on mid-delete sandbox...
[2026-08-26T03:48:35Z] Exec #1 Popen PID: 116
[start] cwd=/ "/usr/local/gcp/bin/sandbox exec val-persist-1787716110 -- /bin/echo alive"
[2026-08-26T03:48:36Z] Exec #2 Popen PID: 129
[start] cwd=/ "/usr/local/gcp/bin/sandbox exec val-persist-1787716110 -- /bin/echo alive"
[2026-08-26T03:48:36Z] Exec #3 Popen PID: 142
[start] cwd=/ "/usr/local/gcp/bin/sandbox exec val-persist-1787716110 -- /bin/echo alive"
[2026-08-26T03:48:42Z] Delete wrapper at t~8s: ALIVE (hung as expected)
[2026-08-26T03:48:42Z] Exec #1 wrapper at t~8s: ALIVE (hung)
[2026-08-26T03:48:42Z] Exec #2 wrapper at t~8s: ALIVE (hung)
[2026-08-26T03:48:42Z] Exec #3 wrapper at t~8s: ALIVE (hung)
[2026-08-26T03:48:42Z] === BASELINE SCAN (t=0, ~8s after delete) ===
[2026-08-26T03:48:42Z]   PID=103 type=sandbox-delete-wrapper state=S ppid=1 (REPARENTED to init)
[2026-08-26T03:48:42Z]   PID=111 type=runsc-delete state=S ppid=103
[2026-08-26T03:48:42Z]   PID=116 type=sandbox-exec-wrapper state=S ppid=1 (REPARENTED to init)
[2026-08-26T03:48:42Z]   PID=124 type=runsc-state state=S ppid=116
[2026-08-26T03:48:42Z]   PID=129 type=sandbox-exec-wrapper state=S ppid=1 (REPARENTED to init)
[2026-08-26T03:48:42Z]   PID=137 type=runsc-state state=S ppid=129
[2026-08-26T03:48:42Z]   PID=142 type=sandbox-exec-wrapper state=S ppid=1 (REPARENTED to init)
[2026-08-26T03:48:42Z]   PID=150 type=runsc-state state=S ppid=142

=== CHECKPOINT t=1m ===
  PID=103 type=sandbox-delete-wrapper state=S ppid=1 (REPARENTED to init)
  PID=111 type=runsc-delete state=S ppid=103
  PID=116 type=sandbox-exec-wrapper state=S ppid=1 (REPARENTED to init)
  PID=124 type=runsc-state state=S ppid=116
  PID=129 type=sandbox-exec-wrapper state=S ppid=1 (REPARENTED to init)
  PID=137 type=runsc-state state=S ppid=129
  PID=142 type=sandbox-exec-wrapper state=S ppid=1 (REPARENTED to init)
  PID=150 type=runsc-state state=S ppid=142
  Popen delete wrapper: ALIVE
  Popen exec #1-3 wrappers: ALIVE

=== CHECKPOINT t=2m ===
  (identical to t=1m — all 8 processes, all state S, same PPIDs)
  Popen delete wrapper: ALIVE
  Popen exec #1-3 wrappers: ALIVE

--- WRAPPER TIMEOUT EVENT at t=2m0.5s ---
[end] exit_code=1 elapsed=120024ms  sandbox delete --force
[end] exit_code=1 elapsed=118514ms  sandbox exec (first)
[end] exit_code=1 elapsed=119018ms  sandbox exec (second)
[end] exit_code=1 elapsed=118014ms  sandbox exec (third)

=== CHECKPOINT t=2m10s ===
  No processes found for sandbox val-persist-1787716110
  Popen delete wrapper: DEAD rc=1
  Popen exec #1-3 wrappers: DEAD rc=1

=== CHECKPOINT t=3m ===
  No processes found. All wrappers DEAD rc=1.

=== CHECKPOINT t=5m ===
  No processes found. All wrappers DEAD rc=1.

=== CHECKPOINT t=10m ===
  No processes found. All wrappers DEAD rc=1.

=== CHECKPOINT t=30m ===
  No processes found. All wrappers DEAD rc=1.

PERSISTENCE PROBE v4 COMPLETE
Container called exit(0).
```

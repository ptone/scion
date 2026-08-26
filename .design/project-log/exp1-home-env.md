# Experiment 1: HOME env with SCION_HOST_UID=0

**Date:** 2026-08-26
**Agent:** dev-exp1-home
**Type:** Diagnostic experiment

## Question

Does `sciontool init` set HOME for its child process when `SCION_HOST_UID=0`?

## Method

Built `sciontool` from source and ran it with `SCION_HOST_UID=0` and
`SCION_HOST_UID=1000` (code-traced only; see below), capturing the child
process environment.

## Findings

### SCION_HOST_UID=0 (experimentally verified)

- `setupHostUser()` returns `(0, 0, false)` — UID=0, Rootless=false
- `usermod -o -u 0 -g 0 scion` runs, creating two UID-0 entries in `/etc/passwd`
- Supervisor condition `UID > 0 || Rootless` evaluates to FALSE
- **HOME=/root** (inherited from parent), **USER=root**, **LOGNAME=root** (inherited)
- `agentHome` resolves to `/root` — agent state files go to wrong location
- Child runs as `uid=0(root) gid=0(root)` with no credential drop

### SCION_HOST_UID=1000 (code-traced, not executed)

The UID=0 run corrupted `/etc/passwd` (changed scion from UID 1002 to UID 0),
which broke `sudo` for the agent process and prevented the UID=1000 run.

From code analysis:
- `setupHostUser()` returns `(1000, 1000, false)`
- Supervisor condition `UID > 0 || Rootless` evaluates to TRUE
- **HOME=/home/scion**, **USER=scion**, **LOGNAME=scion** (explicitly set)
- Credential drop to UID 1000

### Architect's prediction assessment

The prediction ("HOME=/root or unset, USER/LOGNAME unset" for UID=0) was
**partially wrong**: USER and LOGNAME are not unset — they are inherited as
`root` from the parent process environment. "Set to root" and "unset" have
different observable behavior for harness code.

## Critical Side-Effect

Running with `SCION_HOST_UID=0` causes `usermod -o -u 0 scion`, which
creates two `/etc/passwd` entries with UID 0. This corrupted the passwd
database and broke sudo for the running container. In production (where
sciontool is PID 1), this is less visible but still creates an ambiguous
UID-0 mapping.

## Full report

See `/scion-volumes/scratchpad/exp1-home-env-results.md` for complete output
and detailed analysis.

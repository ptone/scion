# Brief: Security Audit -- P4, P4a, P5 (Cloud Run Sandbox Runtime)

## What to audit

Branch `scion/dev-rebase-1294`, the **top 4 commits**:

```
e6db196 test: update P4 stub tests to verify implemented behavior
465628f feat: make Cloud Run Instance tier honest about ephemeral storage
f437741 fix(runtime): timeout-bounded sandbox delete --force with process reaping
ab3a2f1 feat(runtime): implement sandbox exec control plane (P4)
```

To see the full diff:
```bash
git diff 71fd320..e6db196
```

## Security-relevant aspects

### P4: sandbox exec control plane

1. **Command injection via container IDs/sandbox names.** The `containerID` parameter
   flows into `exec.CommandContext` calls in `pty_handlers.go`. Is it properly
   validated before use as a command argument? Could a malicious sandbox name inject
   arguments?

2. **TERM environment variable injection.** `--env TERM=xterm-256color` is passed to
   `sandbox exec`. Is the value safe? Could the TERM value be user-controlled?

3. **No --user flag on sandbox exec.** The sandbox CLI has no --user flag. The process
   runs as whatever user the image specifies. Is this documented and acceptable?

4. **Absolute path usage.** All binaries use absolute paths (`/usr/bin/tmux`,
   `/usr/local/gcp/bin/sandbox`). Verify no relative paths could be exploited.

### P4a: timeout-bounded Delete

1. **Process group killing.** `syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)` sends
   SIGKILL to the process group. Is the `Setpgid: true` properly set? Could this
   kill unrelated processes?

2. **`/proc` scan for orphaned processes.** `reapOrphanedRunsc` reads `/proc/<pid>/cmdline`
   and kills matching processes. Could a malicious process name cause false matches?
   Is the sandbox name validated?

3. **Zombie reaping.** After killing processes, is there a risk of zombie accumulation?

### P5: Tier 0 honesty

1. **Environment variable trust.** `isCloudRunInstance()` trusts the `CLOUD_RUN_INSTANCE`
   environment variable. Could this be spoofed in a non-CRI environment to bypass
   write blocking?

2. **Deployment warnings in health API.** The `DeploymentWarnings` field is publicly
   visible. Does it leak any sensitive information?

## Deliverables

Write your audit report to `/scion-volumes/scratchpad/projects/single-node/reviews/audit-p4p4ap5.md`
with:
- Findings with severity (Critical/High/Medium/Low/Informational)
- File:line references
- Recommended mitigations

Message `sn-impl-em2` when done.

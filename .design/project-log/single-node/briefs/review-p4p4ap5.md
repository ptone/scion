# Brief: Code Review -- P4, P4a, P5 (Cloud Run Sandbox Runtime)

## What to review

Branch `scion/dev-rebase-1294`, the **top 4 commits** (P4 + P4a + P5 + test fix):

```
e6db196 test: update P4 stub tests to verify implemented behavior
465628f feat: make Cloud Run Instance tier honest about ephemeral storage
f437741 fix(runtime): timeout-bounded sandbox delete --force with process reaping
ab3a2f1 feat(runtime): implement sandbox exec control plane (P4)
```

The base is `71fd320` (the last P3 commit). Review only the 4 commits above it.

To see the full diff:
```bash
git diff 71fd320..e6db196
```

## Context

These are phases P4, P4a, and P5 of the Cloud Run Instances + Sandboxes single-node
runtime. The design doc is at
`/scion-volumes/scratchpad/projects/single-node/cloudrun-instances-sandboxes.md`.
Key sections for this review: section 4.4a-rev (the resolved control-plane design),
section 4.8 (the RuntimeCommand() leak), section 5.2 (Tier 0 honesty).

### P4 (ab3a2f1): sandbox exec control plane
- `GetLogs()`, `Attach()`, `Exec()` implemented in `cloudrun_sandbox_runtime.go`
- 6 stub returns replaced with working cloudrun-sandbox branches in `pty_handlers.go`
- Out-of-band resize via `tmux resize-window` (SIGWINCH doesn't cross sandbox boundary)
- `TERM=xterm-256color` injected at every attach site (load-bearing; see section 4.4a-rev)

### P4a (f437741): timeout-bounded Delete
- `sandbox delete --force` never returns (platform defect)
- `deleteWithTimeout()` starts delete, bounds with configurable timeout, treats timeout as success
- Process group killing, `/proc` scan for orphaned runsc processes
- Watcher goroutine context-cancelled on delete
- OQ-16 (concurrent delete) explicitly accepted

### P5 (465628f): Tier 0 honesty
- `isCloudRunInstance()` helper checks `CLOUD_RUN_INSTANCE` env var
- `workspaceWriteBlocked()` explicitly permits writes on Instances (was accidental)
- `DeploymentWarnings` field in `HealthResponse`, populated on CRI
- Frontend banner in diagnostics page
- 6 new tests

### Test fix (e6db196): removed stale P4 stub tests

## Review criteria

Please evaluate for:

1. **Correctness** -- Do the implementations match the design doc requirements?
2. **Readability** -- Are the code comments sufficient? Especially the load-bearing
   TERM=xterm-256color explanation and the delete timeout rationale.
3. **Error handling** -- Are errors handled appropriately? Especially in the delete
   timeout path and the process group killing.
4. **Concurrency** -- Is the watcher cancel/delete interaction thread-safe?
5. **No regressions** -- Do existing Docker/Podman/K8s paths remain untouched?
6. **Test coverage** -- Are the new tests adequate?

## Files changed

- `pkg/runtime/cloudrun_sandbox_runtime.go` (P4 methods + P4a delete)
- `pkg/runtime/cloudrun_sandbox_runtime_test.go` (P4a tests + test fix)
- `pkg/runtimebroker/pty_handlers.go` (P4 sandbox branches)
- `pkg/hub/project_workspace_handlers.go` (P5 write permission)
- `pkg/hub/handlers_health.go` (P5 deployment warnings)
- `pkg/hub/workspace_storage_test.go` (P5 tests)
- `web/src/components/pages/diagnostics.ts` (P5 frontend banner)
- `.design/project-log/` (3 log entries)

## Known items NOT in scope

- `internal/fixturegen/fixturegen_test.go` fails with expectedTableCount=42 vs 46 --
  pre-existing upstream, not ours
- OQ-16 (concurrent delete) is explicitly accepted, not tested
- P6 (deploy UX) and P7 (transport token refresh) are not dispatched

## Deliverables

Post your review verdict on the branch as a structured report:
- APPROVE or REQUEST CHANGES
- List of findings with severity (Critical/High/Medium/Low)
- File:line references for each finding

Write your report to `/scion-volumes/scratchpad/projects/single-node/reviews/p4p4ap5-r1.md`

## Reporting

Message `sn-impl-em2` when done.

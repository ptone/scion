# Persistence Probe: runsc Orphan Process State Over Time

**Date:** 2026-08-26
**Agent:** dev-persist-probe
**Instance:** val-persist-em2 (ptone-experiments/us-east4)
**runsc:** google-958767651 (spec 1.2.1)

## What Was Measured

Deployed a probe to a Cloud Run Instance that:
1. Created a sandbox running `/usr/bin/sleep 3600`
2. Issued `sandbox delete --force` (which hangs on the known-bad runsc build)
3. Spawned 3 `sandbox exec` calls on the mid-delete sandbox (which also hang)
4. Measured process state (from `/proc/<pid>/stat` field 3) and parent PID
   (field 4) at t=0, t=1m, t=2m, t=2m10s, t=3m, t=5m, t=10m, t=30m

## Key Finding

**All orphan processes (both `runsc delete` and `runsc state`) self-clean at
exactly 120s.** The sandbox CLI wrapper has a 120-second internal timeout.
When it fires, the wrapper exits (rc=1) and takes all runsc child processes
with it. No zombies, no reparented survivors.

The "worse persistence profile" claim from defect doc revision 2 is retired.
Both orphan types behave identically.

## Timeline

- **t=0 through t=2m:** 8 processes alive (4 sandbox wrappers + 4 runsc children),
  all state S (sleeping), stable
- **t=2m0.5s:** All 4 sandbox CLI wrappers exit simultaneously (rc=1, ~120s elapsed)
- **t=2m10s through t=30m:** All processes gone, no late reappearances

## Impact on Workaround

- `deleteWithTimeout(10s)` is correct — it abandons a wrapper that self-cleans
  110s later
- `killProcessGroup` does the same thing the wrapper's 120s timeout would do,
  just 110s earlier
- `reapOrphanedRunsc` is belt-and-suspenders — orphans have a 120s natural TTL

## Deliverables

- Full results: `/scion-volumes/scratchpad/projects/single-node/validation/persistence-probe-results.md`
- Instance `val-persist-em2` deleted after probe completion

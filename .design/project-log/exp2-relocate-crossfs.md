# Experiment 2: relocateToScion Cross-Filesystem Data Loss

**Date:** 2026-08-26
**Author:** dev-exp2-relocate agent
**Type:** Diagnostic experiment
**Status:** Complete

## Context

The architect predicted that the planned `relocateToScion` function (for
`cloudrun_sandbox_runtime.go`) would silently destroy files when source and
destination are on different filesystems. The function uses `os.Rename` entry-by-
entry, skips failures, then calls `os.RemoveAll(src)` unconditionally.

## Findings

### Data Loss Confirmed

Wrote and ran `pkg/runtime/relocate_crossfs_test.go` with two tests:

1. **Cross-filesystem** (src on `/dev/shm` tmpfs, dst on `/tmp` overlay): ALL 5
   test files were destroyed. Every `os.Rename` failed with EXDEV, each was
   skipped, then `os.RemoveAll` deleted the originals. Destination empty, symlink
   points to nothing.

2. **Same-filesystem** (both under `/tmp`): All files moved correctly. Symlink
   works. Control test passes.

### Cross-FS Scenario Realism

The scenario is plausible on Cloud Run:
- `GetAgentHomePath` resolves to container overlay (`/home/scion/.scion/...`)
- Cloud Run volumes mount at `/mnt/<name>` — different device
- The codebase already has `isMountedVolume` (workspace_storage.go) that detects
  cross-device boundaries, proving the team knows these exist

### Could Not Break the Prediction

Tested edge cases (partial rename success, ReadDir failure, RemoveAll failure).
EXDEV applies uniformly to all entries when crossing filesystem boundaries.

## Artifacts

- **Test file:** `pkg/runtime/relocate_crossfs_test.go` (committed)
- **Full report:** `/scion-volumes/scratchpad/exp2-relocate-results.md`

## Impact

No production impact — `relocateToScion` does not exist yet. This is a design
validation: the algorithm as described MUST include an EXDEV fallback (copy+delete)
before it can be safely implemented.

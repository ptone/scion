# Brief: Code Review -- Delete Workaround Isolation

## What to review

Branch `scion/dev-rebase-1294`, the **top commit** (workaround isolation + matcher fix).

The commit message should be something like:
`refactor(runtime): isolate delete --force workaround for clean removal`

To see the diff, identify the commit and diff against its parent:
```bash
git log --oneline -3 scion/dev-rebase-1294
# Then diff the top commit
git diff HEAD~1..HEAD
```

If there are multiple new commits, review all of them above the base `a515490`.

## Context

This is a P4a amendment: the `sandbox delete --force` workaround (timeout + reaper) was interleaved with the main runtime logic and could not be cleanly removed. This change isolates it into one file (`cloudrun_sandbox_delete_workaround.go`) so removal is `git rm` + reverting one call site.

### Design doc
`/scion-volumes/scratchpad/projects/single-node/cloudrun-instances-sandboxes.md`

### Architect's amendment (3 messages)
The architect specified 6 requirements:
1. **File isolation** -- all workaround code in one file
2. **Conditional reaping** -- only reap when timeout fired
3. **Self-detecting** -- sync.Once WARN log when delete returns normally
4. **Runtime kill switch** -- `SCION_CLOUDRUN_DELETE_WORKAROUND=off` env var
5. **Bug reference** -- `deleteDefectRef` constant citing our evidence (no public bug ID)
6. **Exit criteria in file header** -- anchored on runsc version google-958767651

### Matcher fix (also in this commit)
The `reapOrphanedRunsc` function previously used substring matching, then was "fixed" to use a path-segment match on `/run/sandbox/<name>/` -- but that path doesn't exist in real argv. The correct fix: NUL-split `/proc/<pid>/cmdline`, exact equality on `argv[len(argv)-1]`. Extracted into `isOrphanedRunscProcess` helper with a 7-case table-driven test.

## Review criteria

### CRITICAL CHECK -- the architect's explicit ask:
**Does the reaper (`reapOrphanedRunsc`) run when the delete succeeded?**

If the answer is YES, that is a **Required** finding. The reaper must ONLY run in the timeout branch. When the platform bug is fixed, a working delete returns promptly -- the reaper would then SIGKILL a healthy in-flight `runsc` operation. This is the dangerous case the restructuring is designed to prevent.

### Other criteria:
1. **Correctness** -- Does `isOrphanedRunscProcess` match on exact final argv element? Does it correctly handle NUL-separated cmdline bytes?
2. **Kill switch** -- Does `SCION_CLOUDRUN_DELETE_WORKAROUND=off` bypass the workaround cleanly? Does the plain path still cancel watchers and clean up state?
3. **Self-detection** -- Does the sync.Once WARN fire on normal delete return? Does it include the defect reference?
4. **Removability** -- Can the workaround file be `git rm`'d with only a simple revert to the call site? Are there dangling references that would break?
5. **Exit criteria** -- Is the file header clear about what "fixed" means? Does it cite the known-bad runsc version?
6. **Test coverage** -- Are the table-driven test cases adequate? Do existing deleteWithTimeout tests still pass?
7. **Evidence** -- Is `defect-sandbox-delete-hang.md` copied into `.design/project-log/`?

## Files expected to change

1. `pkg/runtime/cloudrun_sandbox_delete_workaround.go` (NEW)
2. `pkg/runtime/cloudrun_sandbox_delete_workaround_test.go` (NEW)
3. `pkg/runtime/cloudrun_sandbox_runtime.go` (moved code out, added dispatch)
4. `pkg/runtime/cloudrun_sandbox_runtime_test.go` (moved tests out)
5. `.design/project-log/defect-sandbox-delete-hang.md` (copied evidence)

## Deliverables

Write your review verdict to `/scion-volumes/scratchpad/projects/single-node/reviews/workaround-isolate-r1.md` with:
- APPROVE or REQUEST CHANGES
- Findings with severity (Critical/Required/Nit/Optional/FYI)
- File:line references
- Explicit answer to: "Does the reaper run when the delete succeeded?"

## Gates to run
```bash
go build ./pkg/runtime/...
go vet ./pkg/runtime/...
go test ./pkg/runtime/... -run "TestCloudRunSandbox|TestKillProcessGroup|TestReapOrphanedRunsc|TestIsOrphanedRunscProcess" -count=1 -v
```

## Reporting

Message `sn-impl-em2` when done.

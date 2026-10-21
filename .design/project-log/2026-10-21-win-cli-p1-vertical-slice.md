# win-cli Phase 1: privatefs, pathcanon, Windows filelock, tunnelclient T1/T2, windows-latest job

**Date**: 2026-10-21
**Issue**: ptone/scion#5514 (design `win-cli-design.md` sha256 7cb5d107…, §8 Phase 1)
**Branch / PR**: `win-cli/p1` → `win-cli/integration`, ptone/scion#5530

## Summary

This is the vertical slice of the native Windows CLI design: one primitive, one real call site, and real Windows CI, end to end.

- `pkg/util/pathcanon` (new): `Resolve`, `ValidName`, `ValidInputPath`, `Within`. Windows canonicalises through `GetFinalPathNameByHandle` on the deepest existing ancestor and keeps the `\\?\` prefix. `ValidInputPath` is the only place that rejects the `\\?\`, `\\.\` and UNC prefixes.
- `pkg/util/privatefs` (new). It has two separate implementations behind one API:
  - Windows: a protected owner-only DACL with the token user as the explicit owner, set in the create call. Every component below the Tree anchor is opened relative to its parent handle (`NtCreateFile` with `RootDirectory` and `FILE_OPEN_REPARSE_POINT`), and any reparse point is refused. Rename and remove are handle-based. Privacy checks read the DACL on the open handle and compare SIDs only. In-place writes follow the repair-or-refuse rules. Volumes without persistent ACLs get the explicit volume error.
  - Unix: the existing POSIX sequence.
- `pkg/util/filelock`: split by OS, with main's `Acquire` API unchanged.
  - Windows: `CreateFile` with `FILE_FLAG_OPEN_REPARSE_POINT` and no `FILE_SHARE_DELETE`, a handle check for regular files, and `LockFileEx`.
  - New `LockFile` (handle-level) and `AcquireExisting`.
- `pkg/tunnelclient/state.go`: all state I/O goes through a privatefs Tree anchored at the state directory's parent. Privacy is checked on the handles that are then read or locked.
- `internal/testutil/wintest`: `MustSymlink` and `MustJunction` fail and never skip. Also ACL, owner and token test helpers.
- `.github/workflows/windows.yml` and `.github/windows-test-packages.txt`:
  - a capability step that fails the job if anything is missing: symlink privilege and creation, junctions, elevation, 8.3 names, and a FAT32 VHD;
  - a pkg/hub allowlist guard;
  - build+vet;
  - `go test -json` with zero skips allowed.

## Decisions

All of these were accepted by win-cli-em before implementation, and are listed in the PR description.

- **T2 probe, stricter than the design text.** tunnelclient opens the lock file through the Tree walk, verifies it, and calls `filelock.LockFile` on that same handle. `AcquireExisting` opens by path and would have hidden the handle, so T1's "check on the opened handle" could not hold with it. `AcquireExisting` is still implemented and tested.
- **P1 Build step.** It builds and vets the four allowlisted packages, not `./cmd/scion`, which only compiles for Windows once Phase 2's splits land.
- **`TreeOptions{FollowLinks}`** moves to Phase 3b, together with its `~/.ssh` rationale comment.
- **API additions:** `Tree.OpenPrivate` (in the design stub) and `Tree.OpenDir` (a walk-verified directory handle).
- **Elevated-owner test.** The hosted runner runs as the built-in RID-500 account, whose token defaults to the *user* as owner. The design's premise (an elevated token defaults to Administrators) therefore does not hold on the runner, and the check would have been vacuous. The test now creates its files under an impersonation copy of the token whose default owner is set to Administrators, and asserts that first.
- **FAT32** does not implement `FileAttributeTagInfo` (it returns `ERROR_INVALID_PARAMETER`). On that error the attribute bits come from `GetFileInformationByHandle` on the same handle. FAT has no reparse points.
- **Repair** sets only the DACL (protected). The owner was just verified to be the current user, and setting OWNER would need `WRITE_OWNER`, which an owner does not hold implicitly.

## Review rounds

**Round 1 (win-cli-rev-p1-r1, REQUEST CHANGES), fixed in dd14add02:**
- **F1 (blocking):** Resolve/Within took a dangling symlink or junction for a not-yet-existing tail. The fix probes without following and returns `pathcanon.ErrDanglingLink`.
- **F2:** the FAT fallback triggers on `ERROR_INVALID_PARAMETER` only, through a helper tested on NTFS link handles.
- **F3:** a nil owner or an unreadable descriptor is refused, never dereferenced.
- **F4 (deviation, stricter than the design's `strings.EqualFold`):** components are compared with `CompareStringOrdinal(ignoreCase)`, the OS uppercase table that NTFS uses. Kelvin-sign test.
- **F5:** unix `OpenPrivate` opens with `O_NONBLOCK`, so a planted FIFO is refused, not blocked on.
- **F7:** the Rename fallback runs on the information-class statuses only.
- **F9:** a denied lock create reports the denial.
- **F8 declined for P1:** the guard is per-function; a transitive check is a follow-up.
- **F10 (in-place repair does not revoke existing handles):** noted for the Phase 3 briefs, which should prefer atomic replace for secrets.

**Round 2 (win-cli-rev-p1-r2, APPROVE with non-blocking findings), fixed in c87e59011 and 1aba4974f:**
- **F1:** atomic replace renames through the still-open temp handle (`renameHandle`, `Tree.RenameOpen`). `WriteFile` and tunnelclient's `fileRecord.Write` use it, so a file swapped in under the temporary name never lands at the target. This is load-bearing for Phase 3 row C2. Windows swap test with a by-name control.
- **F2:** a tunnel record over 1 MiB is refused explicitly.
- **F3:** `OpenPrivate(O_WRONLY|O_RDWR)` is EINVAL on both OSes (Linux `open(2)` would accept it).
- **F4:** a regular file with a tail below it is ENOTDIR on Windows, as on unix.
- **F5:** the windows.yml elevation comment is corrected.
- **F6:** the dangling-link tests show the same entry flipping to resolved-outside once its target exists.

**Round 3 (win-cli-rev-p1-r3, APPROVE with small findings), fixed in this push:**
- **F1:** the tunnel lock temp is renamed into place through its own locked handle (`RenameOpen`). Lock-temp swap test in filelock.
- **F2:** `RenameOpen` enforces its precondition by running `CheckPrivate` on the source handle first. On Windows the DACL is checked; on unix, fstat. It refuses a non-private file. `runtime.KeepAlive(f)` keeps the handle valid. `WriteFile` uses the unexported `renameOpen`, because it created the temp itself with a known protection, Inherit included.
- **F3:** failure paths delete the temp through its open handle (`deleteHandle`, `Tree.RemoveOpen`), never by name. Unix is unchanged (`os.Remove` of the name).
- **F4:** the file-type dangling symlink (the `os.Symlink` default for a missing target) has a refusal test.
- **F5, F6:** comment fixes.

**Round 4 (win-cli-rev-p1-r4, APPROVE with small findings):**
- **F1:** `TestStateGuard_NoTempFileByName` (tunnelclient, every OS) parses `state.go` and fails on any by-name `Rename` and on any `Remove` of a temporary name, with a synthetic positive control. Reverting `RenameOpen` to `Rename` was checked to fail it. The behavioural regression test for the swap pattern is filelock's `TestLockFile_ShareDeleteRename`, together with privatefs's `TestRenameOpen_SwappedTempNeverLands` and `TestRemoveOpen_DeletesHandleNotName` (Windows).
- **F2:** the `RenameOpen` doc states what is enforced: f must pass CheckPrivate, and it need not come from this tree. win-cli-em carries the precondition in the Phase 3 briefs.
- **F3:** `runtime.KeepAlive(f)` after the syscall at the exported-API Fd() sites: securityOf, OwnedByCurrentUser, verifyOrRepair, and filelock tryLock/unlock.
- **F4:** the unix `RenameOpen` refusal is wrapped in the same `fs.PathError` as on Windows.

**Gate series:** one LIGHT window, W1. It runs a multi-package `go test -c` of the 4 packages (gate-rules addendum 0d574538) and the non-test build of pkg/config and pkg/brokercredentials. There are no vet windows, by win-cli-em's ruling: multi-package vet does not count as one compile. Windows vet evidence is the windows-latest CI build+vet step, Linux vet is CI Build & Test, and darwin vet is dev-time only.

**Unix deltas in tunnelclient `read` (beyond §3.7 delta 1):**
- The record is opened before the liveness probe and read after it, so it can be one rewrite older than the newest. Each rewrite is a complete record.
- A record over 1 MiB is refused. Main read records unbounded.

## Findings for later phases

- **`os.SameFile` on Windows loads the file ID lazily, by path, on its first call.** A `Stat` before a rename followed by a `SameFile` after it compares the new file with itself. Tests prime the ID with `os.SameFile(fi, fi)` right after the `Stat`.
- **`os.Open` does not share delete access.** It fails with a sharing violation on a file that another handle holds with `DELETE` access, such as a live tunnel lock. The same applies to any Phase 3 code that opens files a Tree handle may hold.
- **On unix, `Protection` `Inherit` means 0666/0777 less the umask.** There is no caller mode. Phase 3's C5 (mode-preserving writes) should keep the os path for Inherit on unix, as the design already says.
- **The runner token holds `SeCreateSymbolicLinkPrivilege` but has it disabled.** Go's `os.Symlink` still works, as the capability step shows.

## Verification

- Dev-time (LIGHT, heavy-build, private GOCACHE):
  - GOOS=windows amd64 and arm64 build+vet of the four packages and wintest;
  - darwin vet;
  - Linux vet and `go test`.
- Windows CI (run 39659360801 at 1aba4974f): 203 tests passed, 0 skipped, and every §4.2 test for these packages passed, plus the review-round tests.
- Linux CI (run 39651514719 at dd14add02): fully green, including Build & Test for the four packages. The run for the final head is linked in the PR.
- Not run locally: anything that compiles `./cmd` or pkg/hub (gate rules).

## Outcome

**Code review:** r1 (REQUEST CHANGES), then r2–r5 (APPROVE). Code was frozen and merged at `97120ae06`; `win-cli/integration` was fast-forwarded to it.

**Security lead (fix-filebrowser-symlink-lead), 2026-10-21, at 97120ae06** (evidence `gs://scion-xproject-exchange/win-cli/p1/seclead-evidence-97120ae06.md`, sha256 6bf04b8c…):
> "CLEAR against §1-§4. Review readiness, NOT authorization to land; CI-green is a separate gate and the Linux green link is still outstanding."
> "Deviation 1: ACCEPT, and stronger than the design's method, not merely equivalent." "Deviations 2-8: ACCEPT all."

Its non-blocking items:
- `AcquireExisting` should be doc-warned or dropped. **Deferred to the Phase 3a brief by the EM.**
- The FAT-fallback error string prints tag 0x0. **Deferred to the Phase 3a brief by the EM.**
- Hardlinks are not covered by checklist §3. Checklist addendum A1 (`win-cli/security-checklist-addendum-a1.md`) applies in Phase 3b; no Phase 1 change.

**Gate series:**
- coordinator-2 SERIES GO [m14727] on lane b02 LIGHT A, for `p1-gate.sh` 5582fce3 (STEP OK by win-cli-srev-p1-2).
- W1 PASS: `END mode=W1 rc=0 host=e6f2aefc1713 utc=2026-10-21T21:09:41Z`. Results: privatefs 14/0/0, pathcanon 6/0/0, filelock 11/0/0, tunnelclient 78/0/0; the callers build passed.
- The log is `gs://scion-xproject-exchange/win-cli/gates/p1/p1-W1-log.tgz`, sha256 e5af4c19b51b03779b0e4a0dfb31028656a4821ef4b19778f2b3c04daf0a4929.
- Rulings recorded:
  - No vet windows (EM). Multi-package vet does not count as one compile; Windows vet evidence is the windows-latest CI job.
  - One multi-package non-test `go build` counts as one compile (coordinator-2).
- `p1-gate-r3.sh` (the srev-p1-2 non-blocking hardening, for a future series) is kept in the bucket and was not run.

**CI at 97120ae06:**
- Linux: https://github.com/ptone/scion/actions/runs/39674740309 (success, all jobs).
- Windows: https://github.com/ptone/scion/actions/runs/39674740468 (207 passed, 0 skipped, 0 failed).

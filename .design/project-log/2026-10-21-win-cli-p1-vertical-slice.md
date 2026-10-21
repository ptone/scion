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
- Windows CI (run 39648975930 at 54b752fb3): 196 tests passed, 0 skipped, and every §4.2 test for these packages passed.
- Not run locally: anything that compiles `./cmd` or pkg/hub (gate rules).

# win-cli Phase 3a: config and credentials sites on privatefs (C1-C11, X3)

**Date**: 2026-10-22
**Issue**: ptone/scion#5514 (design `win-cli-design.md` sha256 7cb5d107…, §3.5 rows C1, C1r, C2, C3-C11, X3; §8 Phase 3a)
**Branch / PR**: `win-cli/p3a` → `win-cli/integration`, ptone/scion#5580

## Summary

Every Windows-reachable private file in `pkg/credentials`, `pkg/apiclient`, `pkg/config` and the GitHub resolution cache now goes through `pkg/util/privatefs`. Unix behaviour is unchanged except for §3.7 delta 4.

- **C1/C1r credentials.** Saving uses `OpenTree(dir, CreateAnchor)` and `Tree.WriteFile(Private)`. Loading on Windows uses `Tree.ReadPrivate` and REFUSES a too-open file (D3 default), with a "delete the file and log in again" hint. Unix load keeps no check, so the read helper is split by OS.
- **C2 dev token.** The file goes through `Tree.WriteFile(Private)`. The directory stays `MkdirAll(0755)`, as the row specifies.
- **C3/C4 hub id.** The global directory is created through `OpenTree(CreateAnchor)`. The temp file comes from `Tree.CreateTemp(Private)`, is linked while it is still open, and is then deleted by handle. `createFileExclusive` is `Tree.Create(excl, ForMode(mode))`. That is delta 4, and it has a unix umask test.
- **C5 `writeFileAtomicMode`.** The unix code moved verbatim to `atomic_write_unix.go`. On Windows, Private writes go through a Tree temp and a handle rename. Inherit writes use the os path with no Chmod. The preserve-existing-Perm rule applies only on POSIX. `syncDir` does nothing on Windows, because there is no directory fsync there.
- **C6 settings lock** was already on `filelock.Acquire`. It gains a Windows junction test.
- **C7 cli_context** uses `OpenTree(CreateAnchor)` and C5.
- **C8 integration config** uses `OpenTree(dir, CreateAnchor)` and `Tree.WriteFile(Private)`.
- **C9 migration owner check.** Unix keeps the uid compare (it moved to `privatefile_unix.go`). Windows reads the owner SID on a handle from the new `Tree.OpenNoFollow`. This also fixed the Windows build of pkg/config (`syscall.Stat_t`).
- **C10/C11.** On Windows, protection follows the source's real ACL (`CheckPrivate`), never its Perm. The conflict backup uses `Tree.Create(excl, Private)` on both OSes.
- **X3 resolution cache.** Directories are created private. Unix keeps tightenMode in a `_unix.go` file. Windows uses `OpenPrivate(O_RDWR)` for the file (repair if owned, else warn) and only warns about a directory. Saving uses `CreateTemp` plus `RenameOpen`.
- **brokercredentials read (brief item 3).** On Windows the read goes through `ReadPrivate` and refuses a too-open file (same rule as C1r). Only pkg/brokercredentials changed; cmd/hub.go is untouched.
- **filelock (brief items 1 and 2).** `AcquireExisting` was dropped because it had no caller (tunnelclient uses the walk-verified handle, row T1). Its Windows probe test now locks through `OpenPrivate` and `LockFile`. The FAT-fallback refusal message no longer prints "tag 0x0".

## Decisions (EM rulings, 2026-10-22)

- **`Tree.OpenNoFollow`** is a new privatefs method, added only in new files (`opennofollow_{unix,windows}.go` plus tests) so it stays disjoint from 3b's privatefs edits. It returns a metadata-only handle and makes no privacy decision.
- **X3 `TightenExisting`** is built from existing primitives (OpenTree + OpenPrivate(O_RDWR) on Windows; stat+chmod on unix). No new primitive was added.
- **The pkg/agent test package compiles for Windows** because 19 test files of gated agent provisioning and storage code now carry `//go:build unix`:
  - Linux `go test -list` is identical before and after (1557 names).
  - The Windows pkg/agent test dependencies do not include pkg/hub (only `pkg/hub/imagecheck` and `pkg/hub/permissions`).
  - The allowlist line is anchored to the four X3 tests.
- **The flagged allowlist-line format and the guard** come byte for byte from win-cli/p3b, so this PR's Windows job runs the anchored lines.
- **Saving where the design names `Tree.WriteFile` but today's unix code is atomic** (X3) uses `CreateTemp` plus `RenameOpen` instead. On unix `Tree.WriteFile` writes in place, which would be a unix change.
- **Anchors for files whose path is configurable** (credentials, dev token, settings backup) are the file's own directory. The design calls these "S", but that is only the default location.

## Open issues / follow-ups

- The pkg/config and cmd suites are still POSIX-bound (D4). Only the anchored site tests run on Windows.
- `persistHubIDOnce` now closes its temp after the link, so a Close error on that path is no longer reported (the content was already synced).

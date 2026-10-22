# win-cli Phase 3a: config and credentials sites on privatefs (C1-C11, X3)

**Date**: 2026-10-22
**Issue**: ptone/scion#5514 (design `win-cli-design.md` sha256 7cb5d107…, §3.5 rows C1, C1r, C2, C3-C11, X3; §8 Phase 3a)
**Branch / PR**: `win-cli/p3a` → `win-cli/integration`, ptone/scion#5580

## Summary

Every Windows-reachable private file in `pkg/credentials`, `pkg/apiclient`, `pkg/config` and the GitHub resolution cache now goes through `pkg/util/privatefs`. Unix behaviour is unchanged except for three tightening-only changes of the §3.7 delta 4 class, each visible only under a umask stricter than 077 and each with a strict-umask test:

1. hub_config `createFileExclusive` (delta 4 itself): `TestCreateFileExclusive_ChmodsUnderStrictUmask`.
2. `writeConflictBackup` via `Tree.Create(excl, Private)` (review r1 F2): `TestWriteConflictBackup_ChmodsUnderStrictUmask`.
3. The resolution-cache save via `Tree.CreateTemp(Private)` (review r1 F2): `TestGitHubResolutionCache_SaveChmodsUnderStrictUmask`.

One durability-only change, not security-related (review r2 #1): `writeConflictBackup` now fsyncs the backup before closing it, on every OS.

- **C1/C1r credentials.** Saving uses `OpenTree(dir, CreateAnchor)` and `Tree.WriteFile(Private)`. Loading on Windows uses `Tree.ReadPrivate` and REFUSES a too-open file (D3 default), with a "delete the file and log in again" hint. Unix load keeps no check, so the read helper is split by OS.
- **C2 dev token.** The file goes through `Tree.WriteFile(Private)`. The directory stays `MkdirAll(0755)`, as the row specifies.
- **C3/C4 hub id.** The global directory is created through `OpenTree(CreateAnchor)`. The temp file comes from `Tree.CreateTemp(Private)`, is linked while it is still open, and is then deleted by handle. `createFileExclusive` is `Tree.Create(excl, ForMode(mode))`. That is delta 4, and it has a unix umask test.
- **C5 `writeFileAtomicMode`.** The unix code moved verbatim to `atomic_write_unix.go`. On Windows, Private writes go through a Tree temp and a handle rename. Inherit writes use the os path with no Chmod. The preserve-existing-Perm rule applies only on POSIX. `syncDir` does nothing on Windows, because there is no directory fsync there.
- **C6 settings lock** was already on `filelock.Acquire`. It gains a Windows junction test.
- **C7 cli_context** uses `OpenTree(CreateAnchor)` and C5.
- **C8 integration config** uses `OpenTree(dir, CreateAnchor)` and `Tree.WriteFile(Private)`.
- **C9 migration owner check.** Unix keeps the uid compare (it moved to `privatefile_unix.go`). Windows reads the owner SID on a handle from the new `Tree.OpenNoFollow`. This also fixed the Windows build of pkg/config (`syscall.Stat_t`).
- **C10/C11.** On Windows, protection follows the source's real ACL (`CheckPrivate`), never its Perm. The conflict backup uses `Tree.Create(excl, Private)` on both OSes. The C11 copy opens the source once and makes the protection decision and the read on that same handle (review r1 F1, checklist §7.3).
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

## Review round 1 (win-cli-rev-p3a-r1, REQUEST CHANGES)

- **F1 (blocking, §7.3), fixed.** `copySettingsBackup` decided protection on one handle and read the bytes again by path. It now opens once and does both on that handle (`copyBackupFrom` takes only the handle). Test: `TestWindowsCopySettingsBackup_DecisionFromReadHandle`.
- **F2, fixed.** Two undeclared delta-4-class unix changes are now declared, with strict-umask tests (see Summary).
- **F3, fixed (the handle-based variant).** `writeConflictBackup` syncs while the handle is open and deletes through the handle on a write or sync failure. A remove by name is left only for a close that fails after a successful sync.
- **F4, accepted as deliberate (D3).** A refused too-open credentials.json is never read, so the next login replaces it with a private file holding only that hub. The refusal message now says that logins to other hubs must be repeated.
- **F5, accepted.** The C4 hard link is made by name, as design row C4 specifies; the hub id is not a secret.
- **F6, accepted.** The C2 read of an existing dev token is unchecked. It is reachable only from `scion server` (gated on Windows). It is a follow-up if D1 widens scope.

## Review round 2 (win-cli-rev-p3a-r2, APPROVE)

- **#1, fixed (docs).** The new `writeConflictBackup` fsync is declared as a unix change.
- **#2, adopted (fail closed).** `sourceProtection` (C10, C11) returns Inherit only for a `CheckPrivate` error wrapping `ErrNotPrivate`. It returns Private on a pass and on any other error, such as an unreadable descriptor. Test: `TestWindowsProtectionForCheck_FailsClosed`, a table test of the mapping helper `protectionForCheck`.
- **#3-#5, FYI, accepted:** C11 follows a reparse leaf, as unix does; the C5 Inherit path renames its non-secret temp by name, as design row C5 sanctions; the X3 control note.

## Open issues / follow-ups

- The pkg/config and cmd suites are still POSIX-bound (D4). Only the anchored site tests run on Windows.
- D1 follow-up (F6): if server scope widens, the dev-token read (`devauth.go`) needs a privacy check.
- `persistHubIDOnce` now closes its temp after the link, so a Close error on that path is no longer reported (the content was already synced).

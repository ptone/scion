# win-cli Phase 3b: hubprofile and cmd 4a sites on privatefs

**Date**: 2026-10-22
**Issue**: ptone/scion#5514 (design `win-cli-design.md` sha256 7cb5d107…, §3.5 rows C12-C15, I1-I4, X1, X2; §3.7 deltas 2 and 3; checklist addendum A1)
**Branch / PR**: `win-cli/p3b` → `win-cli/integration`, ptone/scion#5579

## Summary

- `pkg/util/privatefs` additions:
  - `TreeOptions.FollowLinks`. On Windows it follows existing reparse points at directory components below the anchor; the kernel follows them relative to the parent handle. Leaf files are never followed. On unix it is a no-op. The doc comment records the security lead's rationale for ~/.ssh: dotfile managers link it, so trust is decided on handles after the link is followed.
  - `ErrHardLinked` (addendum A1).
    - Windows: every existing file opened for writing (`Create` non-exclusive, `OpenPrivate` with write flags) must have `NumberOfLinks == 1` on the handle. Otherwise it is refused. If the count cannot be read, it is also refused (fail closed).
    - Unix: only on the relaxed ssh path (`ReplaceFollowing`).
  - `ReplaceFollowing(path, data, newMode, tempPrefix)`: rule I.
    - Windows: resolve the path (follows a symlinked leaf), open the parent dir, run `CheckNoForeignWriters` on its handle, and open the existing target relative to it. The target must have one link and the user as owner. A user-owned file that is too open is replaced. The new file is a private temp file, handle-renamed into place. The old SD is never copied.
    - Unix: the previous ssh-config writer, verbatim, plus the Nlink refusal.
  - The guard `TestNoSkipOnSymlinkPaths` reads the package (first field) of each allowlist line. For an anchored line it checks the named tests and every helper.
- `pkg/hubprofile` (C12-C15): entries, profiles, reads and replaces go through a Tree anchored at `~/.scion/hub-profiles`. `ensurePrivateDir` uses `pathcanon.Within`. The unix Lstat walk is unchanged (`privatedir_unix.go`). On Windows, `Tree.MkdirAll` walks handles (`privatedir_windows.go`).
- `cmd`:
  - I1: `ensureSSHDir` uses `Within` and a `FollowLinks` Tree.
  - I2/I3: `writeFileAtomic` calls `privatefs.ReplaceFollowing`. `ssh.go`'s known-hosts file uses the same writer (§3.7 delta 3).
  - I4: the vscode lock is `filelock.Acquire` with a 2-minute wait (delta 2).
  - X1: `--out` goes through `ValidInputPath`, then a Tree, then `Create(excl, Private)`. Output text comes from `privatefs.Describe`.
  - X2: the broker join warning uses `CheckPrivate`.
  - The Windows ProxyCommand quoting (`sshCmdQuote`) is ported from miller79/scion#189.
- CI: `.github/windows-test-packages.txt` lines may carry go test flags (an anchored `-run=^(A|B)$`). `windows.yml` runs each flagged line on its own and requires every named test to pass. The guards and zero-skip checks cover every invocation. `windows.yml` also builds `./cmd/scion`.

## Decisions

- **The rule I primitive lives in privatefs, not in cmd.** That keeps the site free of per-OS security code (design §3.0). It also lets the unix behaviour (existing file keeps its mode) and the Windows behaviour (always private, refuse foreign) sit behind one call.
- **The unix delta from A1 is tightening only.** An ssh-config, include, known-hosts or vscode file with a second hard link is now refused on unix. Tests: `TestReplaceFollowing_UnixRefusesHardLinkedTarget` (privatefs) and `TestSSHWrite_RefusesHardLinkedTarget` (Windows). Accepted by win-cli-em (it is to be listed as a §3.7-style delta). A side effect: the unix writer now opens an existing target read-only to fstat it, so an unreadable target (mode 0200) is an error where it was not before.
- **The unix `ensureSSHDir` now calls `os.MkdirAll(0700)` through the Tree, not Mkdir followed by Chmod.** The only difference shows with a umask that removes owner bits.
- **hubprofile messages.**
  - A not-private file now reads "others can read it; it must be private to you (mode 0600 | owner-only ACL)". The two tests that matched "must be 0600" were updated.
  - A link leading out of the tree is reported as "a symlink or junction below … leads outside it". `Within` decides this before the walk.
- **The hubprofile anchor is followed when it is a link (Windows)**, as for every Tree anchor. On unix the existing walk still refuses a symlinked `hub-profiles`.

## Open issues

- **The cmd Windows tests wait for 3a.** pkg/config does not compile for Windows until 3a's C9 lands (`legacy_grove_migration.go:306`). A TEMP commit keeps `./cmd` out of the Windows job. Merge order is 3a first; then I rebase and drop the TEMP commit.
- **Rule I refuses some legitimate setups.** A `~/.ssh` linked to a directory whose parent grants Authenticated Users write is refused, for example a dotfiles dir directly under `D:\`. So is a dir carrying a CREATOR OWNER inherit-only ACE, because the Phase 1 `checkACL` counts inherit-only ACEs. Both fail closed. Worth a docs note in Phase 4.
- **Not rows; left as they are.** hubprofile `List`/`ListRun`/`ListRuns`/`RemoveEntry`/`RemoveRunDir` use plain `os` calls. `RemoveRunDir` refuses a non-directory (a junction Lstats as irregular on Windows) before `os.RemoveAll`.
- `ssh.go` passes `GlobalKnownHostsFile=/dev/null` to the system ssh. Whether Windows OpenSSH accepts `/dev/null` there was not verified here.

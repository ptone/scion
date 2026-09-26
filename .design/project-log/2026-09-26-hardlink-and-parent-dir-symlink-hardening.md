# Hardlink, FIFO, and Parent-Directory Symlink Hardening for Root-Owned File Access

**Date:** 2026-09-26
**Branch:** scion/substrate-tmptoken

## Problem

`pkg/sciontool/log/log.go`'s log open and `pkg/sciontool/metadata/server.go`'s
shutdown-token read added `O_NOFOLLOW` for the log/token path's leaf component but
not `O_NONBLOCK`, so a FIFO planted at either path blocks `open(2)` forever — the
log case is worse, since `write()` holds a package mutex while blocked, freezing
every later log call in the root process. The log open also only checked
"is this a regular file", which a hardlink to a root-owned file passes, letting a
workload that owns `$HOME` make root append every log line to an arbitrary
root-readable file via `ln <victim> ~/agent.log`.

Separately, every one of these opens — the log, the shutdown token, the harness
exit-code read, the GitHub token/expiry writer, and the scion hub agent-token
writer — only ever protected the final path component with `O_NOFOLLOW`. A
workload that owns an intermediate directory (most importantly `$HOME` itself,
and `$HOME/.scion`) can still replace that directory with a symlink; the kernel
resolves intermediate path components normally regardless of `O_NOFOLLOW`, which
only applies to the last one. `ln -s /etc ~/.scion` followed by a write to
`~/.scion/scion-token` would silently redirect root's write into `/etc`.

The scion hub agent-token writer (`WriteTokenFile`) itself still used a plain
`os.WriteFile` + fixed-name temp file + path-based `os.Rename`, and both the
refresh loop and `init.go`'s post-`sup.Run` read did a separate path-based
`os.Chown` on the final token path — the same symlink/hardlink class the GitHub
token writer was already hardened against, with a real race window since the
`init.go` chown runs after the workload is already alive.

`agent-limits.json` had the same shape: `writeLimitsState` wrote via a temp file
and rename, and `init.go` chowned the final path afterwards instead of the fd.

## Solution

### New `pkg/sciontool/dirfd` package

A single, dependency-free helper (`OpenParentNoFollow`) walks every path
component from `/` down with `openat(2)` + `O_DIRECTORY|O_NOFOLLOW`, returning an
open fd for the path's parent directory and its leaf name. A symlink at any
component — not just the leaf — makes the walk fail instead of being followed.
Callers do every remaining step (create, open, rename, chown) via `*at()`
syscalls relative to that fd, never a path-based call again. The package also
provides `CreateExclAt`, `OpenAt`, `RenameAt`, `UnlinkAt`, and
`RefuseSymlinkOrNonRegularAt` (an fd-based, non-blocking "does this already
exist as a symlink or non-regular file" check).

### Log open (`pkg/sciontool/log/log.go`)

`openLogFileNoFollow` now resolves through `dirfd.OpenParentNoFollow`, adds
`O_NONBLOCK` (a FIFO with no reader now fails the open immediately instead of
blocking), and checks `Nlink == 1` in addition to the regular-file check (a
hardlink is refused). The log fd is also now opened once and cached for the
process's lifetime instead of being reopened on every line, so only the very
first line can race a symlink/hardlink swap, and that first open is the one
that's checked. `SetLogPath` and the `/tmp/agent.log` fallback close and drop
the cached fd so the next line reopens against the new path. `Chown` now
`fchown`s the cached fd instead of `os.Chown`-ing the path.

### Shutdown-token read (`pkg/sciontool/metadata/server.go`)

`readShutdownToken` resolves through `dirfd.OpenParentNoFollow`, adds
`O_NONBLOCK`, and checks `Nlink == 1` and that the file's owner matches this
process's effective uid, in addition to the existing regular-file check.

### Harness exit-code read (`cmd/sciontool/commands/init.go`)

`readHarnessExitCode` resolves through `dirfd.OpenParentNoFollow` before opening
the leaf; its existing `O_NONBLOCK` + regular-file check + bounded read are
unchanged.

### Scion hub agent-token writer (`pkg/sciontool/hub/client.go`)

`WriteTokenFile` now takes `(token string, uid, gid int)` and routes through the
same `writeFileNoFollowChown` helper the GitHub token writer uses (itself now
resolved through `dirfd.OpenParentNoFollow` rather than a bare `os.Lstat` +
path-based create/rename). `Client` gained `tokenChownUID`/`tokenChownGID`
fields, set once by `StartTokenRefresh` from its config and read by
`RefreshToken` under the same lock that updates the in-memory token, so the
refresh loop's own chown is applied by the writer (fchown on the fd, before the
rename) instead of a separate path-based `os.Chown` afterwards, which is now
deleted. A direct `RefreshToken` call outside `StartTokenRefresh` (as in tests)
sees the zero value and skips the chown, matching the existing "uid<=0 skips
chown" contract.

A new `ChownTokenFile(uid, gid int)` fixes ownership of a token file this
process didn't itself write (the initial file the host-side agent manager wrote
before the container started): it resolves the path the same symlink-safe way,
opens the leaf without following it, refuses anything but a single-link regular
file, and `fchown`s the open fd. `init.go`'s post-`sup.Run` chown now calls this
instead of `os.Chown(hub.TokenFilePath(), ...)`.

### `agent-limits.json` (`pkg/sciontool/hooks/handlers/limits.go`)

`InitLimitsFile` and `writeLimitsState` both take `uid, gid int`; when `uid > 0`
the temp file is chowned via its own fd before the rename, and `init.go`'s
separate path-based chown after `InitLimitsFile` is deleted. The hook-process
call site (`incrementAndCheck`) passes `0, 0`, since it already runs as the
target user and doesn't need a chown.

### Substrate `/tmp`/`/var/tmp` sticky-bit fixup

`fixupWorldWritableTmpDirSticky` now adds only the sticky bit
(`Fchmod(fd, perm|sticky)`) instead of forcing the mode to exactly `01777`, so a
directory more restrictive than `0777` elsewhere keeps those other bits.

### GitHub token expiry error wrapping

`WriteGitHubTokenExpiry` no longer wraps the writer's error with a second
"failed to write GitHub token expiry file" prefix; the refresh loop's own wrap
at the call site already adds it once.

## Notes

- The random-name temp file `writeFileNoFollowChown` creates can leak one 0600
  file with a live token in it if the process crashes between creation and the
  rename. This is accepted rather than swept: tokens are valid for at most a
  few hours, the files are 0600, and a sweeper would need to distinguish a
  crash leftover from a write still in progress. Documented on the function.
- Root never reads the GitHub token/expiry files back — `ReadGitHubTokenFile`
  and `ReadGitHubTokenExpiry` are only called from the credential helper, the
  `gh` wrapper, and `doctor`, all of which run as the scion user, not from
  `sciontool init`. The scion hub token is a different story: `sciontool init`
  (root, for its whole life) does call `hub.ReadTokenFile()` itself — to seed
  the metadata server's outbound-token function, to check whether a token is
  present, and to reread the token during SIGUSR2 auth-reset — and that read
  path (`os.ReadFile`, no `O_NOFOLLOW` or regular-file check) isn't hardened by
  this change. A workload that swaps `~/.scion/scion-token` for a symlink to a
  root-readable file could make root send that file's contents to the Hub as
  if it were the agent's bearer token. This is a real, separate (read-side, not
  write-side) gap outside this change's scope and needs its own follow-up.
- `cmd/sciontool/commands/init.go`'s `writeEnvFile` (`$HOME/.scion/scion-env`)
  has the same write-then-path-chown shape as the files hardened here (plain
  `os.WriteFile`/`os.Rename`, then `os.Chown` on the final path) and was not in
  scope for this change; noted for a future pass.

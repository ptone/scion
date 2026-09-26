# GitHub Token Writer Hardening + Substrate /tmp Sticky-Bit Fixup

**Date:** 2026-09-26
**Branch:** scion/substrate-tmptoken

## Problem

The root-owned `sciontool init`/`substrate-serve` process writes and refreshes the
GitHub App token files (`/tmp/.github-token`, `.tmp`, `.expiry`) using path-following
`os.WriteFile` and path-based `os.Chown`. On the Substrate actor, `/tmp` comes up
0777 without the sticky bit, so a workload-owned process can plant a symlink at a
predictable path and have root truncate or chown an arbitrary file. A handful of
other root-owned reads/opens under a workload-writable directory or `$HOME` had the
same class of issue at lower severity: a root log-file open without `O_NOFOLLOW`, a
harness exit-code read that could block forever on a planted FIFO, and a metadata
shutdown-token read that could follow a symlink.

## Solution

### Token writer (`pkg/sciontool/hub/client.go`)

`WriteGitHubTokenFile` and `WriteGitHubTokenExpiry` now go through a shared
`writeFileNoFollowChown` helper:

- Refuses outright (no write, no temp file created) if the final path already
  exists as a symlink or any non-regular file, checked with a plain `os.Lstat`
  that never follows the final component.
- Otherwise creates a randomly named temp file in the same directory with
  `O_CREATE|O_EXCL|O_NOFOLLOW`, writes, `fsync`s, sets the final owner (`fchown`
  on the open fd, via an injectable `fchownFn` seam for non-root tests) and mode
  (`fchmod` via `f.Chmod`), then renames it onto the final path. `rename(2)`
  replaces the target's directory entry without dereferencing it, so this is
  safe even if the final path changes between the `Lstat` and the rename.
- Both functions now take `uid, gid int` so the writer applies ownership itself,
  replacing the separate path-based `os.Chown` call each caller used to make
  afterwards. `uid <= 0` skips the chown, matching the "0 means skip" contract
  the refresh configs already documented.

File names, contents, and modes (0600) are unchanged, so the credential helper
and workload read exactly what they read before. Call sites updated:
`client.go`'s refresh loop, `init.go`'s initial write, and
`credential_helper.go`'s on-demand refresh (which writes as its own user, so it
passes `0, 0`).

### Other root-owned opens on predictable/workload-writable paths

- `pkg/sciontool/log/log.go`: the root log open (`$HOME/agent.log` and the
  `/tmp/agent.log` fallback) now goes through `openLogFileNoFollow`, which adds
  `O_NOFOLLOW` and an explicit regular-file check via `fstat` before returning
  the file. Mode and file names are unchanged for the normal case.
- `cmd/sciontool/commands/init.go`'s `readHarnessExitCode` now opens with
  `O_NOFOLLOW|O_NONBLOCK`, checks the fd is a regular file, and reads a bounded
  32 bytes. A planted FIFO can't block the shutdown path forever, and a planted
  symlink is refused rather than followed; both are treated the same as a
  missing file (nil).
- `pkg/sciontool/metadata/server.go`'s `shutdownExisting` now reads the shutdown
  token through a new `readShutdownToken` helper with the same
  `O_NOFOLLOW`+regular-file+bounded-read pattern.
- `os.Remove` of fixed `/tmp` names (the shutdown token cleanup, the GitHub
  token cleanup) was left unchanged: `unlink(2)` never follows the final
  path component, so there is nothing for a planted symlink to redirect.

### Substrate rootfs /tmp sticky-bit fixup

`cmd/sciontool/commands/substrate_rootfs.go`'s `fixupRootfsForScion` — called
only from `substrate-serve`'s two rootfs-fixup call sites, both of which always
run before the harness starts and only in the privilege-drop-enforced path — now
also fixes `/tmp` and `/var/tmp` when either is world-writable without the
sticky bit: it opens the directory with `O_DIRECTORY|O_NOFOLLOW`, checks the
mode via `fstat`, and sets `01777` via `fchmod` on the fd if needed. A missing
directory or an unreadable one is logged and treated as a no-op, not fatal.
This is defense in depth alongside the writer hardening above, not a
replacement for it: the underlying kernel `protected_symlinks` behavior isn't
confirmed present in the actor's sandbox, so restoring the sticky bit alone
isn't relied on to close the writer-side issue.

The `/tmp` mode itself originates in the Substrate rootfs (no image, template,
or scion code sets it); this fixup is a runtime correction, not a fix at the
source.

## Files Changed

| File | Change |
|------|--------|
| `pkg/sciontool/hub/client.go` | Hardened `WriteGitHubTokenFile`/`WriteGitHubTokenExpiry` via `writeFileNoFollowChown`; refresh loop calls the new signatures and drops its separate `os.Chown` calls |
| `cmd/sciontool/commands/init.go` | Initial GitHub token/expiry writes pass uid/gid through instead of a separate chown; `readHarnessExitCode` hardened against symlinks and FIFOs |
| `cmd/sciontool/commands/credential_helper.go` | Updated to the new `WriteGitHubTokenFile`/`WriteGitHubTokenExpiry` signatures (writes as its own user, uid/gid 0) |
| `pkg/sciontool/log/log.go` | Root log open (primary and `/tmp/agent.log` fallback) hardened via `openLogFileNoFollow` |
| `pkg/sciontool/metadata/server.go` | Shutdown-token read hardened via `readShutdownToken` |
| `cmd/sciontool/commands/substrate_rootfs.go` | `fixupRootfsForScion` now also fixes `/tmp`/`/var/tmp` sticky bit in the enforced rootfs-fixup path |

## Notes

- No behavior change for docker/k8s/other runtimes: the writer's symlink/
  non-regular refusal only triggers when an attacker has already planted
  something at the final path, which the existing parity tests don't exercise
  and which those runtimes' own 1777 `/tmp` makes far less likely in practice.
- The `/tmp`/`/var/tmp` fixup only ever narrows the "world-writable, not
  sticky" case to `01777`; it never touches a directory that's already sticky
  or that isn't world-writable, and it's unreachable from the non-substrate
  init path since only `substrate-serve` calls it.

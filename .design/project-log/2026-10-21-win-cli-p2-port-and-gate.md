# Windows CLI Phase 2: port, cross-compile splits, command gate, winsec guard

**Date:** 2026-10-21
**Branch:** win-cli/p2 (base main 1814a4dc3), PR into win-cli/integration
**Design:** win-cli-design.md (sha256 7cb5d107…), sections 3.1, 3.5 (F§4b), 3.6, 3.8, 4.4, 8 Phase 2
**Issue:** ptone/scion#5514; source miller79/scion#189 (head d3a0af31a)

## What was done

- **#189 splits rebased onto main.** The group C/D/E/G unix/other splits went in with a 3-way apply, leaving out the excluded files. Three conflicts were resolved: `dirfd/safeio.go` (main's doc comment plus #189's `Stat` return type), `sciontool/log/log.go` (imports), and `sciontool/hub/client.go`. Hunks that delete code applied cleanly, which shows the moved code matches main.
- **Fallbacks reversed to ErrUnsupported (§3.5).** The other-platform versions of `provision.openGitExclude`, `util.unlinkInDir` and `sciontool/log.openLogFileNoFollow` now return `errors.ErrUnsupported`. The broker NFS mount runner (`runtimebroker/nfs_mount_run_other.go`) does too, as a tightening of #189's version, which ran the command.
- **§3.6 sites.**
  - `readTokenAt` was re-derived from main.
  - `readTokenAtStat` moved into `token_file_unix.go` and now returns `dirfd.Stat`.
  - `credential_state.go` was split: the publishing half moved to `credential_state_publish_unix.go`, and the other platform never publishes and logs that once at debug level. `SetCredentialStateDirForTest` stays in the untagged file and returns a no-op restore on other platforms.
  - `dirfd_other.go` now exports the identifiers main added (`ErrUnlinked`, `ErrNotOwned`, `OpenParentNoFollowOwnedBy`). Exported-set parity was checked by diffing.
  - `provision.go`'s new owner read uses `fileOwner`.
- **Broker locks (§3.2 table).** `brokeridentity.lockDir` and the launch journal's `lockJournalInstance`/`unlockJournalInstance` moved to `_unix.go` files. Their `_other.go` versions return ErrUnsupported and create nothing.
- **Command gate (§3.1).** Added `cmd/platform_gate.go` with `windowsAllowed`, `applyPlatformRestrictions` and `errUnsupportedOnWindows`. It is called right after `applyModeRestrictions`. A gated command is kept but hidden, and refuses with the documented message. Its Pre/PostRun hooks and ValidArgsFunction are cleared. It accepts any args and unknown flags. The required-flag and flag-group annotations on the flags it defines itself are stripped, so no usage error is shown for it (review r1 F1). Root's global flag groups and flag-value checks still apply. Root's `PersistentPreRunE` returns the refusal right after the global-flag handling and before the hub-profile gate and any invocation-target, project, settings or migration work (review r1 F8, an EM decision). RunE refuses as well. Agent mode is unchanged.
  - Residual: `Execute()` still runs the legacy-env warning and global-layout migration, the early settings load and `resolveMode` before cobra dispatches. That code is shared by every command and touches only §4a sites.
- **Static guard (§4.4).** Added `hack/winsec-guard.sh`, which uses only bash and `go list` and has a `--self-test` mode, and `hack/winsec-unprotected.txt`. Every §4a file is listed as "pending Phase 3", and the gated server, broker and in-container files as "not protected on Windows: gated".

## Decisions

- **hub.secret.migrate and hub.secret.migrate-names are NOT on the Windows allowlist.** They open the local hub SQLite DB read-write and can write an FK snapshot (`pkg/store/entadapter/timestamp_normalize.go`). Those files are not §4a sites, so §3.1's rule keeps the commands gated. win-cli-em confirmed this as an application of the rule, not a design change. Every other hub child writes only to §4a sites.
- **Windows vet of broker test packages was not attempted.** `pkg/agent`, `pkg/runtime`, `pkg/runtimebroker` and `pkg/provision` get only a non-test GOOS=windows build. Their test files use many unix-only APIs, and tagging dozens of them would hide nothing useful, because Windows CI never runs these packages. The in-container sciontool packages and the small leaf packages pass GOOS=windows vet. Their unix-only test files were tagged `unix`, or use a per-OS mkfifo helper (#189's pattern).
- **Guard granularity.**
  - A listed file is exempt from checks (a) and (b) for the whole file. Shared, client-reachable files use inline `//winsec:` markers instead: `cmd/common.go`, `cmd/project_list.go`, `cmd/project_use.go`, `pkg/config/templates.go`, `pkg/util/git.go`, and the seven "make removable" chmods in `pkg/util/fs.go` (review r1 F5).
  - (b) is per line: a `filepath.Rel(` whose result reaches a `HasPrefix(` with a literal `".."` within 5 lines.
  - (c) and (d) cover every file built only off unix, by file name or by a `//go:build` line that names windows, !unix or !linux (review r1 F4).
  - (d) also catches `return true, nil` (F3).
  - The script header's LIMITS section says what the text checks cannot see.

## Callers of file_owner_other (ok=false), verified

- `pkg/provision/provision.go` `clearOwnStaleWorktreeEntry`: when `!ok`, it returns the "cannot read the owner" error and keeps the dir. `checkChownEPERM`: when `!ok`, it returns the lchown error with "(owner unknown)".
- `pkg/runtimebroker/project_relink.go` (two sites): the Lchown runs only when `ok`.
- `pkg/agent/home_ownership.go` `newAgentHomeOwnershipError`: when `!ok`, the dir uid and gid stay -1. `warnAgentHomeOwnerMismatch`: when `!ok`, it returns without a warning.
- `pkg/hub/device_id_other.go` → `isMountedVolume`: when `!ok`, it returns `(true, false)`, meaning the result could not be determined, and the caller surfaces that.
- `pkg/util/fsutil` `fileOwnerUID`: when `!ok`, the `ownedByUID` filter is false and nothing is chowned. `fileDevice`: when `!ok`, there is no cross-device skip, and the chown itself fails on Windows.

## Open items

- `./cmd/scion` does not compile for Windows until Phase 1 and Phase 3 land. The blockers are all in excluded files: `pkg/util/filelock` (Phase 1), `pkg/hubprofile/cache.go`, `pkg/tunnelclient/state.go`, `pkg/config/legacy_grove_migration.go` `fileOwnerUID` (`syscall.Stat_t`), and `cmd/vscode.go` (`syscall.Flock`). With throwaway overlay stubs for filelock and that one config function, every non-cmd package in the Windows graph built, including a masked non-test `pkg/hub`.
- The ubuntu cross-build CI step (§5.1), which also runs the guard, is deferred until Phase 1 merges.

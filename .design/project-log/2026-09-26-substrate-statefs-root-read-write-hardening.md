# Hardening Root's agent-info.json/agent-limits.json/env-overlay Access

**Date:** 2026-09-26
**Branch:** scion/substrate-reintegration

## Problem

Four root-context filesystem operations read or wrote files under the
workload-owned home directory using path-following, unbounded primitives
(`os.ReadFile`, `os.Stat`, `os.ReadFile`, and a path-based `os.Chmod`). Because
the workload owns the containing directory outright, it can unlink and
replace any entry there at any time, regardless of that entry's own
ownership — directory write permission governs create/unlink, not file
ownership.

- `StatusHandler.readAgentInfoMap` (`pkg/sciontool/hooks/handlers/status.go`)
  read `agent-info.json` with a plain `os.ReadFile`. This handler is
  registered in the root PID-1 init process and runs throughout the
  workload's lifetime (post-start, pre-stop, session-end, limits-exceeded,
  auth-reset). A FIFO planted at that path would block the read forever,
  stalling every root-side lifecycle control that depends on it; a symlink
  to a large or infinite file would be read without bound.
- `StatusHandler.writeAgentInfoLocked` widened its temp file to mode 0644 via
  a path-based `os.Chmod(tmpPath, 0644)`. Because the directory is
  workload-writable, the workload can swap the temp file's directory entry
  for a symlink between its creation and that chmod call; `chmod(2)` follows
  symlinks, so root would then set 0644 on whatever the symlink points to.
- `LoadEnvOverlay` and `resolveEnvValue`'s `from_file` handling
  (`pkg/sciontool/hooks/envoverlay.go`) read files via a separate `os.Stat`
  (size check) followed by `os.ReadFile` — a TOCTOU pair, and `os.ReadFile`
  follows symlinks unconditionally. `from_file`'s containment check compared
  path strings (`filepath.Abs` + `filepath.Rel`), which a symlink whose own
  name sits inside an allowed root but whose target does not can defeat.
- `LimitsHandler.readLimitsState` (`pkg/sciontool/hooks/handlers/limits.go`)
  read `agent-limits.json` with a plain `os.ReadFile`. Not currently reachable
  from a root context, but its sibling `writeLimitsState` was already
  fd-based and no-follow, leaving an asymmetry that would silently reintroduce
  the same class of issue if a future caller moved this read into root's own
  process.

## Solution

Added a shared pair of primitives in `pkg/sciontool/dirfd/safeio.go`,
following the same pattern already used by `readServicesYAML` and
`writeLimitsState`:

- `ReadFileNoFollow`/`ReadAtNoFollow`: resolves every directory component
  with `O_NOFOLLOW`, opens the leaf with `O_NOFOLLOW|O_NONBLOCK` (so a FIFO
  never blocks the open), requires a single-link regular file via `fstat`,
  and bounds the read with `io.LimitReader`. Distinguishable sentinel errors
  (`ErrNotSingleLinkRegular`, `ErrTooLarge`) let a root-context caller treat
  any refusal as a logged, non-fatal skip.
- `WriteFileNoFollow`: creates the temp file in the target's parent
  directory via that directory's own no-follow fd, sets its mode (and
  ownership, when requested) via `fchmod`/`fchown` on the open file
  descriptor rather than a path-based call, then renames it into place with
  a single fd-relative rename.
- `ReadUnderRootNoFollow`: reads a file that must resolve to inside an
  allowed root by walking an `openat(O_NOFOLLOW)` fd chain down from that
  root one component at a time, so containment is enforced by fd resolution
  rather than by comparing path strings — a symlink whose name looks
  contained but whose target is not is refused at the component that
  resolves it, not accepted because its string form passed a prefix check.

All four call sites now route through these helpers:

- `readAgentInfoMap` uses `ReadFileNoFollow`; any refusal falls back to the
  same empty-map behavior a missing file already produced.
- `writeAgentInfoLocked` uses `WriteFileNoFollow`, keeping the file at mode
  0644 (the broker reads it after the container exits) but setting that mode
  on the open fd.
- `LoadEnvOverlay` reads its own overlay file via `ReadFileNoFollow`; its
  `from_file` referent is read via `ReadUnderRootNoFollow` when
  `allowedRoots` is non-empty (or `ReadFileNoFollow` directly when it isn't,
  matching the historical "no roots configured" behavior). There is no
  longer a separate stat call anywhere in this path — the file is fstat'd
  and read exactly once, from the fd the walk verified.
- `readLimitsState` uses `ReadFileNoFollow`, closing the read/write asymmetry
  with its sibling `writeLimitsState`.

Existing `from_file` error message shapes ("not found", "escapes allowed
roots", "exceeds N bytes") are preserved so callers keying off those
substrings are unaffected. `readServicesYAML` and `writeLimitsState`
themselves were left as-is (already hardened) rather than refactored onto
the new shared helpers, to avoid disturbing their existing, tested
behavior.

## Files Changed

| File | Change |
|------|--------|
| `pkg/sciontool/dirfd/safeio.go` | New: `ReadAtNoFollow`, `ReadFileNoFollow`, `WriteFileNoFollow`, `ReadUnderRootNoFollow` |
| `pkg/sciontool/hooks/handlers/status.go` | `readAgentInfoMap`/`writeAgentInfoLocked` route through the new helpers |
| `pkg/sciontool/hooks/handlers/limits.go` | `readLimitsState` routes through `ReadFileNoFollow` |
| `pkg/sciontool/hooks/envoverlay.go` | `LoadEnvOverlay`'s own read and `resolveEnvValue`'s `from_file` read/containment route through the new helpers; the old string-prefix `pathInAnyRoot` helper is removed |

## Notes

- Behavior for legitimate inputs is unchanged; only how a hostile
  substitution at these paths is handled differs (refused instead of
  followed/blocked/unbounded).
- Whether `agent-info.json` still needs to be widened to mode 0644 at all
  (versus granting the broker read access some other way) is an open
  question, not addressed here.

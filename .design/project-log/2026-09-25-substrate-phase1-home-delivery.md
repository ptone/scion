# Substrate Phase 1: deliver the composed agent home to substrate actors

**Date:** 2026-09-25
**Branch:** `scion/substrate-integration`

## Problem

`buildBootstrapFiles` (`pkg/runtime/substrate_bootstrap.go`) shipped only
`ResolvedAuth.Files` and file-type `ResolvedSecrets` in the substrate runtime's
`POST /scion/v1/bootstrap` payload. The broker-composed agent home —
`RunConfig.HomeDir`, which holds the harness-config home (`.claude.json`,
`.claude/settings.json`), the template home, and skills — was never shipped.
Docker/Podman bind-mount `HomeDir` and cloudrun-sandbox relocates it into the
sandbox; the substrate runtime dropped it entirely. The live symptom: claude
sat on the first-run theme picker, because none of the harness-config home
ever reached the actor.

## Solution

### Broker side (`pkg/runtime/substrate_bootstrap.go`)

- Added `homeBootstrapFiles(homeDir, containerHome)`: walks `homeDir` with
  `filepath.WalkDir`, which never follows a symlink (a symlink entry's type
  is reported without descending into it). Regular files only — a symlink,
  fifo, socket or device is skipped and counted (never read); the file's own
  permission bits (`perm & 0o777`) are preserved. `homeDir == ""` returns
  `(nil, nil)` (no composed home to ship); a `homeDir` that doesn't exist, or
  isn't a directory, is an error.
- `buildBootstrapFiles` now assembles files in precedence order — home, then
  `ResolvedAuth.Files`, then file-type `ResolvedSecrets` — and dedupes by
  final path via `dedupeBootstrapFilesByPath` (later source wins, first
  occurrence's position is kept), so the wire payload always carries exactly
  one entry per path. Auth and secret files override a same-path home file.
- Added a total-size cap, `maxBootstrapFilesTotalBytes` (16 MiB decoded),
  checked once against the deduped set's summed decoded byte count (tracked
  per-entry as an unexported, non-wire `decodedSize` field on `bootstrapFile`
  rather than re-decoding base64). The error on overflow names only the cap
  and the total size.

### Serve side (`pkg/sciontool/substrate/`)

- **Symlink safety fix.** `mkdirAllTracked` previously found the deepest
  existing path ancestor via `os.Stat` (follows symlinks) and then delegated
  the rest to `os.MkdirAll` (also `os.Stat`-based). An image shipping a
  pre-existing symlink at an intermediate component — e.g.
  `/home/scion/.config -> /etc` — would have been silently written through:
  `Stat` reports the link's target as an ordinary existing directory, and
  `MkdirAll` creates the remaining path components on the other side of it.
  **This was a real, exploitable gap, confirmed by test** (see
  `TestWriteBootstrapFile_RejectsWriteThroughPreExistingSymlinkDir`): before
  the fix, a bootstrap file targeting a path under a symlinked directory
  landed outside the actor's intended home.
  Fixed by rewriting `mkdirAllTracked` to walk with `os.Lstat` (never
  follows) and create only the missing suffix with plain `os.Mkdir` calls,
  returning a new `errSymlinkComponent` sentinel if any existing component is
  a symlink. `writeBootstrapFile` wraps that into an error naming only the
  bootstrap file's own `Path` (never the resolved symlink target or file
  content) before it reaches the request-scoped log line. The final
  `os.Rename` in `writeFileAtomicMode` was already safe against the leaf
  component itself being a symlink (`rename(2)` replaces the directory entry
  rather than following it), so only the parent-directory walk needed fixing.
- **Transport limit switched to `http.MaxBytesReader`.** The bootstrap
  handler previously used `io.LimitReader(r.Body, maxBootstrapBodyBytes+1)`,
  which silently truncates an over-limit body rather than signaling the
  limit was hit — in practice this almost always surfaced as an ambiguous
  JSON-decode 400. It now uses `http.MaxBytesReader`, which fails the read
  closed at the limit with a distinguishable error, reported as `413`.

## Measured transport limits

The design required finding the *real* transport limit rather than guessing.
Neither the substrate source at the pinned commit nor a live cluster was
reachable from this checkout, so the router's config was read from the live
cluster (via the team) instead:

- **Router (ingress listener for this route):** no request-body cap. No
  buffer filter and no `max_request_bytes` appear anywhere in its filter
  chain (`set_filter_state -> ext_proc -> router`); the `ext_proc` filter is
  header-only (never inspects the body); `per_connection_buffer_limit_bytes`
  is unset on both the listener and the upstream cluster, so the 1 MiB
  default there is a flow-control watermark, not a cap. The body streams
  through uninspected. The route's timeout is 300s (idle 330s).
- **`sciontool substrate-serve` (confirmed, pre-existing):**
  `maxBootstrapBodyBytes` = 64 MiB, bounding the raw JSON request body. This
  is therefore the binding limit on the whole path — about 48 MiB of raw
  (pre-base64) file bytes once base64's 4/3 expansion is accounted for.

**Chosen cap:** `maxBootstrapFilesTotalBytes` = 16 MiB decoded
(`pkg/runtime/substrate_bootstrap.go`) — ~21.3 MiB once base64-encoded,
leaving roughly 3x headroom under the measured 64 MiB serve-side limit even
before the (comparatively tiny) JSON envelope is added. The server-side
constant is left at 64 MiB with a doc comment cross-referencing the runtime
constant and the relation between them, rather than sharing a literal
constant across the two packages — this repo already keeps the broker-side
and serve-side wire types independently defined by design (see
`pkg/sciontool/substrate/types.go`'s package doc), and the two packages have
no existing import relationship in either direction.

## Tests

- `pkg/runtime/substrate_bootstrap_home_test.go` (new): walk (nested path +
  mode preserved), skip-and-count (symlink, fifo, unix socket), empty-HomeDir
  no-op, missing-HomeDir error, precedence/dedup (home vs. auth vs. secret,
  later wins), cap at cap and at cap+1, and an error-hygiene test with a
  sentinel secret embedded in a home file.
- `pkg/sciontool/substrate/server_test.go` (extended): the symlinked
  intermediate directory is rejected both at the `writeBootstrapFile` level
  and end-to-end through `handleBootstrap` (surfaces as the existing generic
  500, no content leaked, single-shot slot behaves normally), and the
  oversized-body case now expects `413` via `http.MaxBytesReader`.
- All existing parity tests for other runtimes (docker, k8s, cloudrun,
  cloudrun-sandbox) are unaffected — this change only touches the substrate
  runtime's own file-assembly and serve-side path handling.

## Docs

- `deploy/substrate/README.md`: new "Bootstrap files: home delivery" section
  (copy-in/one-way/additive-over-image-home semantics, the walk's symlink/
  non-regular-file skip rule, the cap and its measured sources) plus a new
  "Known Phase 1 limitations" bullet extending the existing plaintext
  broker→router transport note to cover home files.
- `phase1-spec.md` (scratchpad, not in the repo): §2.1's `files` example and
  §2.2 step 8 amended in place; new "Addendum B: home delivery" section
  recording the finding, decision, and the measured limits.

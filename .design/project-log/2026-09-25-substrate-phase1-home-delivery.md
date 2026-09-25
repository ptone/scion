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
- `homeBootstrapFiles` also keeps a running total of Stat-reported file sizes
  during the walk itself, and returns the same cap-shaped error as soon as
  that total exceeds `maxBootstrapFilesTotalBytes` — before reading the file
  that tipped it over into memory. This is a conservative early exit against
  a stray oversized file in a template home; `buildBootstrapFiles`'s
  post-dedup check is still the authoritative one, since a home file that
  ends up overridden by a same-path auth/secret file is never counted there.
- Every bootstrap file's `Path` (home, auth, and file-type secret) is now
  `filepath.Clean`-ed before dedup. Home's already effectively was
  (`filepath.Join` cleans internally); auth `ContainerPath` and secret
  `Target` values that are already absolute previously went through
  `expandTildeTarget` unchanged, so two differently-spelled but equivalent
  paths (e.g. a doubled separator) could have produced two wire entries for
  what should be one path.
- The skipped-entries log line is now capped at the first 20 relative paths
  plus the total count, so a home with an unusually large number of skipped
  non-regular entries can't produce an unbounded log line.

### Serve side (`pkg/sciontool/substrate/`)

- **Symlink safety fix.** `mkdirAllTracked` originally found the deepest
  existing path ancestor via `os.Stat` (follows symlinks) and then delegated
  the rest to `os.MkdirAll` (also `os.Stat`-based). An image shipping a
  pre-existing symlink at an intermediate component — e.g.
  `/home/scion/.config -> /etc` — would have been silently written through:
  `Stat` reports the link's target as an ordinary existing directory, and
  `MkdirAll` creates the remaining path components on the other side of it.
  This was a real, exploitable gap, confirmed by test (see
  `TestWriteBootstrapFile_RejectsWriteThroughPreExistingSymlinkDir`): before
  the fix, a bootstrap file targeting a path under a symlinked directory
  landed outside the actor's intended home.

  A first fix walked *upward* from `dir` with `os.Lstat` (never follows) to
  find the deepest already-existing ancestor, then created only the missing
  suffix with plain `os.Mkdir`. That closed the case where the symlinked
  component's target was itself missing the remaining subpath, but not the
  case where it wasn't: `Lstat` only declines to follow its own final
  argument, so an upward walk that stops at the first existing ancestor never
  Lstats anything *above* that point. If a symlinked component further up the
  path already had the remaining subpath pre-created on its far side (e.g.
  `.config -> /etc` and `/etc/sub` already exists), the upward walk landed on
  that real directory and the symlink was never noticed — the write still
  went through it.

  The guarantee `mkdirAllTracked` actually provides now: every existing
  component of `filepath.Clean(dir)` is Lstat'd, top-down from the first
  component to `dir` itself (not just the deepest one that happens to
  exist). The first symlink found anywhere in that walk is rejected
  (`errSymlinkComponent`); a non-dir component is an error; the first
  component that doesn't exist marks the start of the missing suffix, which
  is created top-down with plain `os.Mkdir` calls (never `os.MkdirAll`,
  which is `os.Stat`-based and would reopen the same hole one level down).
  Every directory `os.Mkdir` creates is therefore known-real by
  construction. `writeBootstrapFile` wraps the sentinel into an error naming
  only the bootstrap file's own `Path` (never the resolved ancestor or any
  content) before it reaches the request-scoped log line.

  This check is a plain existence check followed by a separate create, not
  an atomic operation — it is only safe because nothing writes concurrently
  during bootstrap: `substrate-serve` is the sole writer, the harness has
  not started yet, and the image is static up to this point. It is not a
  general TOCTOU-safe guarantee and the code comments say so explicitly.

  The final path component (the file itself) is handled separately: it is
  never Lstat'd here, because `writeFileAtomicMode`'s `os.Rename(tmp, path)`
  replaces whatever directory entry currently sits at `path` — including a
  pre-existing symlink — rather than following it. A symlink at the leaf is
  therefore safe by construction (atomically replaced, its old target left
  untouched), which is now also covered by a dedicated test.

  Separately, `writeBootstrapFile` now `filepath.Clean`s the bootstrap
  file's `Path` once and uses only that cleaned form for both the
  `mkdirAllTracked` walk and the final write target. Previously the parent
  directory came from `filepath.Dir(f.Path)` (which cleans internally) but
  the final `os.Rename` still used the raw, uncleaned `f.Path`. A `..`
  segment in that raw path is resolved by the kernel at syscall time against
  whatever is physically on disk, which does not necessarily agree with the
  lexically-cleaned directory the symlink walk just validated — if an
  earlier path component is itself a symlink, `..` after it walks back from
  the link's target, not from the intended directory. Cleaning once up front
  removes any `..` lexically before it reaches a syscall, so the two no
  longer name potentially different locations.
- **Transport limit switched to `http.MaxBytesReader`.** The bootstrap
  handler previously used `io.LimitReader(r.Body, maxBootstrapBodyBytes+1)`,
  which silently truncates an over-limit body rather than signaling the
  limit was hit — in practice this almost always surfaced as an ambiguous
  JSON-decode 400. It now uses `http.MaxBytesReader`, which fails the read
  closed at the limit with a distinguishable error, reported as `413`.
- **A rejected path is now `422`, with a stable code (C-1 decision, round
  22).** The every-component symlink guard applies to every bootstrap
  `Path`, not just ones under home, so a target under a *system* symlink
  (e.g. `/var/run -> /run` on Debian-based images, or a merged-`/usr`
  layout) is rejected the same way a symlinked path under home is.
  Previously every rejection — a symlink in the path, an empty/relative
  path, a non-directory path component — surfaced as the same generic,
  content-free `500` every other write failure produces. `writeBootstrapFile`
  now returns a `*bootstrapPathError` for these three cases specifically,
  and `handleBootstrap` answers `422` with that error's own text: a stable
  code (`bootstrap_path_symlink` for a symlink anywhere in the path,
  `bootstrap_path_invalid` for everything else) plus the rejected file's own
  path — never an internal ancestor, never content. Other write failures
  are unchanged and stay a generic `500`. The broker's `postBootstrap`
  parses a `422` body back into a matching `bootstrapPathRejectedError`, and
  `Run` logs the code and path explicitly (both are configuration, safe to
  log) before returning the error to its caller. No new wire fields: the
  `422` body is still the same unstructured string every other `/bootstrap`
  error has always used, just in a stable, parseable
  `"<code>: bootstrap file <quoted path> rejected: <detail>"` shape for this
  one class of error. See phase1-spec.md Addendum B (C-1) and
  `deploy/substrate/README.md`'s "No symlink traversal" note for the
  operator-facing statement and workaround (use the resolved path, e.g.
  `/run/secrets/...` instead of `/var/run/secrets/...`).

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

- `pkg/runtime/substrate_bootstrap_home_test.go`: walk (nested path + mode
  preserved), skip-and-count (symlink, fifo, unix socket), a symlinked
  *directory* (as opposed to a symlinked regular file) pointing outside
  HomeDir is skipped and never descended into, empty-HomeDir no-op (both
  `HomeDir == ""` and an existing-but-empty HomeDir), missing-HomeDir error,
  precedence/dedup (home vs. auth vs. secret, later wins), a home file and an
  auth `ContainerPath` that only differ by a doubled separator (e.g. `x` vs.
  `/home/scion//x`) collapse to one entry via the pre-dedup `filepath.Clean`,
  the total cap at cap and at cap+1, the cap summed across all three sources
  (individually under cap, combined over it), the cap is computed after
  dedup (a home file shadowed by an auth override does not count, provided
  home's own total stays under the cap; see the early exit), `homeBootstrapFiles`
  itself returning the cap error during the walk for a single oversized
  file, an error-hygiene test with a sentinel secret embedded in a home
  file, and a dedup-position test that asserts the actual order of a
  non-alphabetical path set (rather than sorting before comparing) to prove
  the "first occurrence keeps its position" contract the test's name and
  comment describe.
- `pkg/sciontool/substrate/server_test.go`: the original symlinked-parent
  case is rejected both at the `writeBootstrapFile` level and end-to-end
  through `handleBootstrap` (surfaces as the existing generic 500, no
  content leaked, single-shot slot behaves normally); a symlinked
  *ancestor* whose target already has the remaining subpath pre-created is
  also rejected, with nothing created on the far side (this is the case the
  first version of the fix missed); the same is proven for a symlink at the
  first component directly under home, and for an outside-home absolute
  target (auth/secret-style, to prove the guard isn't home-specific), with
  a matching positive test that a clean outside-home target writes
  normally; a `..`-bearing path Cleans to its lexical location and never
  touches the component the `..` walked back through; a symlink at the
  *leaf* file path is atomically replaced rather than written through, with
  its old target left untouched (pinned: the test now requires the replace
  outcome and fails the test if the write is rejected instead, matching what
  the code comments and this log document); and the oversized-body case
  expects `413` via `http.MaxBytesReader`. Both symlink- and invalid-path
  rejections are now also proven end-to-end as `422` with their stable code
  and the rejected path in the body, and without content (C-1, round 22).
  Every test that builds a bootstrap path from a temp directory now roots it
  at `realTempDir(t)` (`filepath.EvalSymlinks(t.TempDir())`) instead of the
  raw `t.TempDir()`, because the every-component guard Lstats ancestors
  above the test's own fixtures too, and a symlinked `TMPDIR` (the macOS
  default, and reproducible on Linux) would otherwise trip it for reasons
  unrelated to what each test is checking (round 22, RQ-1).
- `pkg/runtime/substrate_runtime_test.go`: a `422` bootstrap rejection
  surfaces `Run`'s returned error with the stable code and the rejected
  path, and never the (sentinel) file content the run's own config carries;
  a dedicated `parseBootstrapPathError` test round-trips a path containing
  characters `strconv.Quote` must escape, and confirms a generic (non-
  path-error) body reports `ok=false` rather than a wrong split.
- All existing parity tests for other runtimes (docker, k8s, cloudrun,
  cloudrun-sandbox) are unaffected — this change only touches the substrate
  runtime's own file-assembly and serve-side path handling.

## Docs

- `deploy/substrate/README.md`: new "Bootstrap files: home delivery" section
  (copy-in/one-way/additive-over-image-home semantics, the walk's symlink/
  non-regular-file skip rule, the cap and its measured sources) plus a new
  "Known Phase 1 limitations" bullet extending the existing plaintext
  broker→router transport note to cover home files. A new "No symlink
  traversal in a target's path" note (C-1, round 22) states the `422
  bootstrap_path_symlink` restriction, names the workaround (the resolved
  path, e.g. `/run/secrets/...`), and documents `bootstrap_path_invalid` as
  the other stable code.
- `phase1-spec.md` (scratchpad, not in the repo): §2.1's `files` example and
  §2.2 step 8 amended in place; "Addendum B: home delivery" section
  recording the finding, decision, and the measured limits, extended with a
  C-1 sub-decision (round 22) recording the every-component guard's
  consequence for targets under a system symlink, the `422` status and its
  two stable codes, and the workaround.

## Round 23 review fixes

The prior round's `422` body was a declared wire contract (a comment on
`bootstrapPathError` says so) but nothing pinned its exact bytes, and the
broker's own parser had a latent split bug on a path containing its own
delimiter text. Fixed, test-only on the wire-format side plus two small
production hardenings:

- **Golden, byte-exact body assertions.** Both of `pkg/sciontool/substrate/
  server_test.go`'s `422` tests (the symlink case and the relative-path
  case) now compare the response body against an exact `want` string built
  from `codeBootstrap*`, `strconv.Quote` on the path, and the fixed
  `" rejected: "` / `errInvalidBootstrapPath.Error()` pieces, including the
  trailing `"\n"` `http.Error`'s own `Fprintln` appends — a `Contains` check
  alone would stay green even if the format changed shape entirely (e.g.
  dropping the quotes, or renaming the delimiter). `pkg/runtime/
  substrate_runtime_test.go`'s `TestSubstrateRun_BootstrapPathRejectedSurfacesCodeAndPathNoContent`
  fixture is now built the same byte-exact way (mirrored literal, since the
  two packages have no import relationship to share a helper across —
  same reasoning as the size-cap constants above) and cross-references the
  serve-side golden test by name in a comment; a format change on either
  side now has to update both, or one of the two test suites fails.
- **Broker parser fix.** `parseBootstrapPathError` used to find the
  `" rejected: "` delimiter with a plain `strings.Index` over the whole
  remainder, which can match *inside* the quoted path itself (e.g. a
  directory literally named `not rejected: yet`) and hand `strconv.Unquote`
  a truncated, unparsable fragment — silently falling back to an empty
  code/path. It now uses `strconv.QuotedPrefix` to find exactly where the
  quoted path ends first, then checks that what follows starts with the
  delimiter. A test with such a path is added alongside the existing
  round-22 `TestParseBootstrapPathError` round-trip case.
- **Log/error quoting.** `handleBootstrap`'s `422` log line named the
  rejected path twice — once raw, once (already quoted) inside the error
  text it also logged — so an embedded newline in the path could split the
  log line into two, forging a second entry. It now logs the error alone
  (`%v`, not `%s` of the raw path), which already names the path exactly
  once, quoted. Symmetrically, the broker's `bootstrapPathRejectedError.Error()`
  now uses `%q` instead of `%s` for the path, for the same reason (the
  parser hands it an already-unquoted string, so a control byte would
  otherwise reach the returned error's text raw) and so an empty path
  reads as `""` rather than a confusing double space. The broker's
  structured `slog` log line for the same event needed no code change —
  `slog`'s own key/value attribute encoding already quotes a value that
  needs it.
- **Fallback path tested.** A `422` with a body `parseBootstrapPathError`
  can't parse (no code, not this contract's shape) previously exercised
  only the parser in isolation. `TestSubstrateRun_CleanupOnFailure` gained a
  case posting a bare `"garbage"` body through the full `Run` path,
  asserting the resulting error still reads `"...(422): garbage"` (the
  fallback branch, never a panic or a silent success) and that cleanup
  still runs. A second, dedicated test captures the broker's log output
  (same pattern as `common_test.go`'s
  `TestRunSimpleCommand_NoSecretsInDebugLog`) to prove that when the body
  *does* parse, the log line actually carries the code and path — the prior
  round asserted this only via the returned error's text, never against the
  log line itself.
- **Citation cleanup.** Four `phase1-spec.md` citations added in the prior
  round (in `bootstrapPathError`'s doc comment, `bootstrapPathRejectedError`'s
  doc comment, `Run`'s `errors.As` block, and `handleBootstrap`'s `422`
  branch) pointed at a file that isn't in this repo. Replaced each with a
  pointer to `deploy/substrate/README.md`'s "No symlink traversal in a
  target's path" note, which already documents the same thing. This is a
  narrow, scoped cleanup of only the newly-added citations from this range;
  it is not the broader pre-existing-citation consolidation, which is a
  separate, already-tracked item.

None of the above changes the `422` body's actual wire shape (still
`"<code>: bootstrap file <quoted path> rejected: <detail>"`), so
`deploy/substrate/README.md` and `phase1-spec.md`'s Addendum B needed no
content changes — only the citation redirects above, which point at
README, not restate it.

## Round 24 review fixes

The round-24 review's one Required finding was a test-isolation leak; the
rest were nit/optional follow-ups riding along in the same commit. No wire
change.

- **Test-isolation fix (RQ-1).** `TestBootstrap_RejectedPathLogLineIsSingleLineEvenWithEmbeddedNewline`
  (`pkg/sciontool/substrate/server_test.go`) repoints the package-global
  `pkg/sciontool/log` path at a file under its own `t.TempDir()` but never
  restored it. Once that directory was removed at test end, the next
  `log.*` call in the binary failed its `OpenFile`, and `log`'s own
  fallback-on-failure path silently rewrote the global log path to
  `/tmp/agent.log` and forced debug mode on for every later test — quietly
  defeating `testmain_test.go`'s per-binary log sandbox. Fixed by restoring,
  in `t.Cleanup`, both `log.SetDebug(false)` and `log.SetLogPath` to
  `filepath.Join(os.Getenv("HOME"), "agent.log")` — the exact path
  `TestMain` pins for this binary's sandbox (`HOME` is set once for the
  whole test binary and never changed by any test in this package), not a
  guessed default. Verified with `go test -v -count=1
  ./pkg/sciontool/substrate/...`: no fallback WARNING, and `/tmp/agent.log`
  is not created, under both a plain and a symlinked `TMPDIR`.
- **Citation cleanup (N-1).** One more `phase1-spec.md` citation, added in
  this home-delivery round itself (before the round-23 grep's scope) at
  `dedupeBootstrapFilesByPath`'s doc comment
  (`pkg/runtime/substrate_bootstrap.go`), missed the round-23 cleanup above.
  Reworded to point at `deploy/substrate/README.md`'s "Bootstrap files: home
  delivery" section instead of naming `phase1-spec.md`.
- **500-path quoting (O).** The generic write-failure log line
  (`pkg/sciontool/substrate/server.go`'s `handleBootstrap`, the sibling of
  the `422` branch N-1 fixed last round) still echoed `f.Path` unquoted.
  Changed `%s` to `%q`, matching the `422` branch's own quoting. Added
  `TestBootstrap_WriteFailureLogLineIsSingleLineEvenWithEmbeddedNewline`,
  which drives a path containing an embedded newline through invalid-base64
  content (routing the failure through this 500 branch specifically, rather
  than the `*bootstrapPathError` 422 branch the existing injection test
  already covers) and asserts the log stays a single line.
- **Broker golden test now pins the parse (O).** `TestSubstrateRun_BootstrapPathRejectedSurfacesCodeAndPathNoContent`
  previously only asserted `Contains(err, code)` and `Contains(err, path)`
  against `Run`'s returned error — both true even in
  `parseBootstrapPathError`'s fallback (unparsed) branch, since the raw
  body is folded into `detail` either way, so the test couldn't
  distinguish a working parse from a broken one. `Run`'s own error can't be
  used for a stronger check: `SubstrateRuntime.redact` deliberately
  flattens every returned error to a plain string (to strip a secret value
  out of the text), which erases `bootstrapPathRejectedError`'s concrete
  type along the way. The test now also calls `postBootstrap` directly
  against the same harness fixture and asserts `errors.As` into
  `*bootstrapPathRejectedError`, checking `code` and `path` against the
  expected values — pinning the parse at the point it actually happens,
  before `redact` ever sees the error.
- **Slog quoting test (O).** Added
  `TestSubstrateRun_BootstrapPathRejectedLogsQuotePathWithNewline`, proving
  the claim already stated in `substrate_runtime.go`'s comment above the
  `runtimeLog.Error` call: a newline embedded in `pathErr.path` is quoted by
  `slog`'s own attribute encoding, so the broker's structured log line for a
  rejected bootstrap path stays exactly one line and the path attribute
  renders with the newline escaped (`path="...\nFAKE LOG LINE
  INJECTED\n..."`), not split or forged.

Head at delivery: `a4a0de66`.

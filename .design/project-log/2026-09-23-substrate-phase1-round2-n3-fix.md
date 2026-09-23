# Substrate Phase 1 — round 2 nit: close the bootstrap-file chmod window (N3)

sb-rev-2's round 2 review (`reviews/round-2-sb-rev-2.md`) confirmed all
round 1 findings resolved and raised two new blocking items (R1, R2 — both
in `pkg/runtime`, sb-dev's files) plus a handful of Nits/Optionals. N3 is
mine: the round 1 fix for finding #9 (`os.WriteFile` then `os.Chmod`) closed
the "mode never gets fixed" bug but left a narrower one — a real race
window.

## N3 — chmod window between write and chmod

`pkg/sciontool/substrate/server.go`, `writeBootstrapFile`: for a
**pre-existing** file (e.g. baked into the image at 0644), round 1's fix
was `os.WriteFile(path, content, mode)` followed by `os.Chmod(path, mode)`.
Between those two syscalls, the file holds the *new* secret content at its
*old*, possibly-wider mode — anything with read access under the old mode
(e.g. any process running as the image's default user, if the file was
world- or group-readable) can read the secret during that window, small as
it is.

**Fix:** `writeFileAtomicMode` (new helper) writes via a temp file instead
of in place:

1. `os.CreateTemp(dir, ".bootstrap-tmp-*")` in the **same directory** as the
   target path — required for the final rename to land on the same
   filesystem, which is what makes it atomic (`rename(2)` across
   filesystems is not atomic and generally not even possible without a
   copy). `CreateTemp` itself creates the file at a private 0600.
2. `tmp.Chmod(mode)` and, when a chown owner is configured, `tmp.Chown(uid,
   gid)` — both **before** any content is written. `tmp.Chmod`/`tmp.Chown`
   operate on the open file descriptor (fchmod/fchown), not the path, which
   also removes a symlink-swap TOCTOU that a path-based `os.Chmod` after
   the fact would be exposed to (a small bonus beyond what N3 asked for).
3. Write the content, close, then `os.Rename(tmpPath, path)`.

By the time the secret touches disk (step 3, the write), the file already
has its final mode and owner. The rename is atomic, so there is no instant
where `path` holds the new content at the wrong mode/owner — including the
case where `path` didn't exist yet at all. The old file (if any) is
replaced in one filesystem operation, not mutated in place.

## Tests

- Kept `TestWriteBootstrapFile_EnforcesModeOnPreExistingFile` (round 1's
  test) passing unmodified — it still exercises the same
  pre-existing-file-gets-the-new-mode property, now satisfied by rename
  instead of in-place chmod.
- Added `TestWriteBootstrapFile_SetsModeAndOwnerAtomically` (two subtests:
  fresh file, and a pre-existing file at a different mode), asserting the
  final file's mode, uid, gid (via `syscall.Stat_t`, Linux-only — skipped
  otherwise, matching the existing precedent in
  `pkg/runtime/cloudrun_sandbox_runtime_test.go`), and content, using the
  test process's own uid/gid as the chown target so the test doesn't
  require root (chowning a file you own to your own uid/gid is always
  permitted; chowning to an arbitrary *different* uid needs root, which the
  sandbox this ran in doesn't have).

The window itself (a race that requires a concurrent reader mid-syscall)
isn't practically observable from a single-threaded test; what's
verifiable and what the fix is actually accountable for is that the
resulting file always has the exactly-requested mode and owner, which
these tests pin for both the fresh- and pre-existing-file cases.

## Validation

- `go build ./pkg/sciontool/substrate/...`, `go vet`: pass.
- `go test ./pkg/sciontool/substrate/...`: pass (all, including the two
  above).
- `go test -race ./pkg/sciontool/substrate/...`: pass.
- `go test ./pkg/sciontool/... ./cmd/sciontool/...`: pass except the same
  pre-existing, unrelated `TestNativeTelemetryPolicyEffectiveChildEnv/disabled`
  failure noted in every prior log entry for this branch.
- `golangci-lint run --new-from-rev=origin/scion/substrate-integration
  ./pkg/sciontool/substrate/...`: 0 issues.
- `gofmt -l`: clean.

## Not in scope for me this round

R1 (per-agent state keyed per config instance instead of process-wide) and
R2 (`egress_allow` validation bypasses) are both `pkg/runtime`/
`pkg/config` — sb-dev's files. N1, N2, O1–O5 are also not assigned to me.

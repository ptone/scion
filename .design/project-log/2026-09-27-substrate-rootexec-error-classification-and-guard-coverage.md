# Project Log: Sudo Fixup Error Classification, a Complete Parity Guard, and Coverage Gaps Closed

**Date:** 2026-09-27
**Component:** `cmd/sciontool/commands/substrate_rootfs.go`, `cmd/sciontool/commands/init.go`, `cmd/sciontool/commands/substrate_serve.go`, `cmd/sciontool/commands/doctor.go`, `pkg/sciontool/dirfd`, `pkg/sciontool/substrate`

## Misclassified errors logged an attack signal on every ordinary bootstrap

The sudo-setuid fixup's error handling used `os.IsNotExist` to decide
whether a failed lookup was worth logging. That check does not unwrap a
`%w`-wrapped error, and every error the fixup's own dependency returns is
wrapped that way — so a `SearchPath` entry that simply doesn't ship a
`sudo` binary at all (the common case for most of them, on every image)
logged an ERROR line indistinguishable from an actual untrusted-candidate
refusal. Switched to `errors.Is(err, fs.ErrNotExist)`, restoring the
original intent: a genuinely absent candidate produces no log line at any
level.

A related, second case: on an image where `/bin` and `/sbin` are
themselves symlinks to `/usr/bin` and `/usr/sbin`, the chain walk correctly
refuses to traverse a symlinked directory component, so every bootstrap
logged an ERROR for those two entries as well — even though the same
binary is already reached (and, if untrusted-root setuid, still stripped)
through its `/usr/*` entry earlier in the list. This is now logged at
Debug instead, but only when that specific parent symlink is itself
root-owned (or, on a runtime with no separate root/workload boundary,
self-owned); a symlinked parent that fails that check, or any other
failure, still logs at ERROR and is still refused exactly as before. The
trust decision itself never changes — only the log level does.

## A parity guard that inspected calls but not references

A test meant to keep the sudo-hardening call chain reachable only from its
one legitimate wiring path inspected direct calls and a handful of
top-level variable declarations, but not every way a function value can be
referenced: passing one as a bare value, aliasing it to a local variable,
or invoking it through one of the existing indirection variables that
carry the fixup to its real call sites. Rewritten to walk every identifier
in the package's source and check each occurrence of a tracked name
against an allowlist keyed to its exact legitimate location — a specific
function's body, or the initializer of one specific, named package-level
variable — rather than "any call" or "any top-level variable." Declaring
positions (a function's own name; a variable's own declared name) are
excluded, since those are definitions, not references.

## Coverage gaps closed

Several checks had no test that would fail if the check itself were
removed, because every existing test's fixture happened to already fail
for a different, independent reason:

- A directory-chain trust check and a leaf file's own trust check are two
  separate steps in the same verification walk; no existing test's leaf
  was trustworthy enough on its own to isolate the leaf check from the
  chain check. Added one test per leaf property (ownership/mode, and
  regular-file type).
- A temp-directory trust check's ancestor-walk and its sticky-bit
  exemption were both covered only by fixtures whose leaf directory was
  already untrusted on its own, so removing either check independently
  would not have failed any test. Also found and fixed a latent bug
  shared by three fixtures in the same file: passing a bare octal literal
  to `os.Chmod` does not set the sticky bit at all (its Go representation
  is a different bit position than the raw mode value), so a root-only
  test for the sticky-exemption path would actually have failed had it
  ever run, because the directory it built was never actually sticky.
- A bootstrap precondition's fail-closed branch for an inconclusive stat
  result (as opposed to "not there at all") had no test at all; reverting
  it to the historical, more permissive behavior passed the entire test
  suite. Added a table covering the specific error shapes that matter.
- A shared list of candidate directories, referenced from two independent
  call sites specifically so they could never drift apart, was never
  pinned against its own source of truth — every test that loops over it
  would still pass even if it were hardcoded back to a shorter list.
- Two tests for a self-binary integrity check could only demonstrate
  their claims by accident: the refusal path passed only because the
  currently running test binary happens to live under a world-writable
  build directory, and its positive counterpart requires real root and
  skips otherwise, in every unprivileged environment. Added a lightweight
  helper mode to the test binary that runs a single check function
  against its own current location and exits, letting a test copy the
  binary to a controlled location — a genuinely trusted chain, or a
  known-untrusted one — and run it as a real subprocess for both outcomes,
  without needing actual root.
- A diagnostic command's root-context refusal had no test at all. Added
  the ordinary (non-root) positive path, which runs for real, and a
  root-gated negative path matching this codebase's existing convention
  for a genuinely root-only behavior.
- Nothing pinned that an operator-identity environment variable never
  reaches a root-context child process's environment, even though at
  least one image ships a tool whose behavior changes based on that
  variable's presence. Not exploitable today, since that child's
  environment is already built from scratch — pinned directly by asking a
  real child process to print its own environment.

## Verification

Every new or rewritten test was confirmed by deliberately reverting the
specific behavior it covers and restoring it afterward: a misclassified
error stops being silently misreported, a call or alias injected at an
unauthorized location is caught, and each of the coverage gaps above fails
when its underlying check is removed and passes when it is restored.
Build, vet, and the full test suite for every touched package are green
under a scrubbed environment.

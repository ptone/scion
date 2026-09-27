# Project Log: Sudo Setuid Strip and Absolute-Entrypoint Hardening on Substrate

**Date:** 2026-09-27
**Component:** `cmd/sciontool/commands` (`substrate_rootfs.go`, `substrate_serve.go`, `init.go`), `pkg/runtime/substrate_template.go`

## Sudo

The image ships the workload user a passwordless sudo grant
(`/etc/sudoers.d/scion`, `scion ALL=(ALL) NOPASSWD:ALL`), on the assumption
that sudo's own setuid bit never survives into a running actor. On a runtime
where root is a security boundary, if that assumption is ever violated — a
stale golden template, or whatever mechanism restores file ownership on
this runtime also restoring this — the grant plus an intact setuid bit is a
single, no-password command away from full root, regardless of anything
else this codebase does to harden root's own behavior.

The existing rootfs fixup already runs at both points a Substrate actor's
lifecycle needs it: substrate-serve's own startup (captured into the golden
snapshot) and a `/bootstrap`-time fallback (covering any actor that reaches
that request without having gone through startup first). Extending that
same function, rather than adding a third call site, means the fix
inherits both guarantees for free. It now fd-anchored fchmod-strips the
setuid, setgid, and sticky special bits off any `sudo` binary found under
the same fixed system directories the exec-hardening work elsewhere in this
codebase trusts — resolving through a legitimate root-installed symlink
chain the same way, never following a symlink planted directly at the
binary's own name — and fd-anchored unlinks the grant files outright as
defense in depth. A fail-closed precondition alongside the existing
bootstrap checks refuses to proceed if a setuid-root `sudo` binary is found
anywhere in those directories, catching a stale template that skipped the
strip; it does not parse `sudoers` files or group membership in either
direction, since the one thing that actually matters is the single mode
bit.

This is substrate-only by construction, not by an explicit runtime check:
both the strip and the precondition live inside functions that are only
ever reachable from substrate-serve's own wiring, proven by a test that
scans every other source file in the package for a call to either. Every
other runtime keeps sudo and its setuid bit intact on purpose.

## Entrypoint

Two independent changes close the class of hazard where PID 1's own launch
resolves a bare command name before any of this codebase's own hardening
ever has a chance to run. The substrate actor's container `Command` is now
the absolute `/usr/local/bin/sciontool` (where the image installs it)
instead of a bare `sciontool` looked up against the container's own PATH;
the template's own version marker is bumped so every existing golden
snapshot, built with the old entrypoint baked in, gets rebuilt rather than
silently reused. Kubernetes's own bare `sciontool provision`/`sciontool`
init-container invocations are unaffected and deliberately left as they
are — that runtime rebuilds its container from a fresh image layer on
every restart, so there is no persistent, workload-writable PATH entry for
a bare name to resolve against there.

Independently, before ever reporting healthy, substrate-serve now verifies
that the binary actually running as PID 1 — resolved through the kernel's
own `/proc/self/exe` magic symlink, not a path re-read from disk — is
itself a trusted, root-owned regular file, reusing the identical fd-walk
verification the fixed-PATH command resolver applies to an external tool.
This is defense in depth independent of how PID 1's argv[0] was spelled: a
substrate actor whose running image is somehow reached through a
workload-writable path refuses to start the HTTP server at all rather than
serving traffic — including broker-authenticated requests — from it.

## Verification

Every new code path has a fixture-based unit test (the setuid strip across
every candidate directory, its refusal to follow a symlink at the binary's
own name, the sudoers-grant removal leaving unrelated files alone, and the
bootstrap precondition's own pass/fail cases including a setuid binary
owned by a uid other than root, which must not trip it) plus a test proving
the fixup actually re-applies on a second call rather than being a
one-shot action, standing in for a persisted rootfs that reverted the fix
between two actor starts. The self-binary check has both a direct test
(refusing this process's own non-root-owned test binary) and a wiring test
proving substrate-serve's own startup sequence actually calls it and
refuses to start the HTTP server on failure — bounded to a short timeout
rather than a blocking call, since skipping the check would otherwise let
the server actually start on the freed port that test reserves, hanging
instead of failing. Three pre-existing tests that stub the rootfs fixup for
unrelated reasons (proving call ordering and absence of goroutine leaks)
needed the new self-binary check stubbed alongside it, so they continue to
exercise the address-in-use failure path they were written for rather than
the new check's own failure path masking it.

Build, vet, and the full test suite for every touched package are green
under a scrubbed environment. Five pre-existing, unrelated test failures in
`pkg/runtime` (container-runtime auto-detection tests that depend on tools
not present in this sandbox) were confirmed present before this pass's own
changes as well, via a direct before/after comparison.

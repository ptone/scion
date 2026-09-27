# Project Log: Root-Context Exec No Longer Resolves Bare Command Names Against PID 1's Own PATH

**Date:** 2026-09-27
**Component:** `pkg/sciontool/rootexec` (new), `pkg/sciontool/dirfd`, `pkg/sciontool/substrate`, `pkg/sciontool/metadata`, `pkg/sciontool/hooks`, `cmd/sciontool/commands/init.go`, `cmd/sciontool/commands/doctor.go`

## The class

On Substrate, root PID 1's own inherited PATH includes
`/usr/local/share/npm-global/bin`, a directory the image chowns to the
workload uid so the harness's own npm-installed tools can be found on PATH.
Any root-context process that execs a bare command name — `sh`, `iptables`,
`git`, `su` — and lets the standard library's own PATH search (or the
kernel's, for a shell script) resolve it can be handed a binary the
workload planted in that directory, and run it as root, before any
privilege drop takes effect.

## The fix: one resolver, used everywhere a root exec starts

`pkg/sciontool/rootexec` centralizes this. `Resolve(name)` searches a fixed,
hardcoded list (`/usr/sbin`, `/usr/bin`, `/sbin`, `/bin`) — never
`os.Getenv("PATH")` — and verifies, by fd-walk, that both the resolved
binary and every real directory leading to it are owned by uid 0 (or, on a
runtime with no separate root/workload identity at all, by the calling
process's own uid) and free of the group- and other-write bits. A candidate
is first fully resolved with `filepath.EvalSymlinks`, so a legitimate
root-installed symlink chain — Debian's `iptables`, resolved through
`/etc/alternatives`, or the merged-`/usr` layout where `/bin` and `/sbin`
are themselves symlinks into `/usr` — is followed rather than refused
outright; the fd-walk verification runs against the fully-resolved
destination, so a hop through anything workload-writable is what actually
gets refused, not symlinks as a class. `Env(extra...)` builds a companion
environment for the resulting command from scratch: the fixed PATH, plus
only whatever the caller states explicitly — nothing else PID 1 happens to
have inherited (another PATH, `LD_PRELOAD`, a `BASH_ENV`) carries over.

Every root-context site that previously exec'd a bare name now goes through
this: the wrapper that runs an operator-requested command as another user
(which itself embeds resolved absolute paths for `sh`, `su`, and `whoami`
directly into the generated script, rather than leaving them for the
eventual shell to look up), the four iptables invocations for the metadata
emulator's traffic redirect, and the git invocation that rewrites a shared
workspace's credential configuration. Two diagnostic commands (a workspace
git-status check and a harness-process `pgrep` fallback) were hardened the
same way for consistency, though neither runs automatically as part of
actor startup.

A root process re-executing itself (to clear a secret from its own
`/proc/<pid>/environ`, since the kernel populates that file once from the
original `execve` arguments and never updates it) previously resolved its
own path via `os.Executable()`, which re-reads the binary's on-disk path —
itself a location that could be workload-writable on some runtime — then
followed any symlinks there before re-executing it. This now uses
`/proc/self/exe`, the kernel's own magic symlink to the already-running
inode: it always re-execs the exact image already in memory, regardless of
what (if anything) now sits at its on-disk path.

The one call site whose bare-name lookup happens against $PATH but was left
unchanged, deliberately, is the harness supervisor's own child exec: it is
the intended way an npm-installed harness binary is found, and the child
always runs under a Go-level privilege drop applied before `execve` — a
planted binary there would only ever run as the workload's own identity,
which the workload already controls. The same reasoning applies to a
sidecar service's configured command and to a batch of git operations that
clone and configure the shared workspace: each site's exec always runs
under an already-applied credential drop by the time this code is
reachable, so routing them through the fixed resolver would only break
their intentional reliance on the workload's own PATH (specifically, to
find `npm-global`-installed binaries) for no security benefit.

Two more small hardening changes round out the sweep. A root-eligible
pre-start hook (a project or hub script staged before the workload exists)
kept inheriting this process's whole environment, including PATH — it now
gets its PATH fixed and a small set of interpreter/loader-redirecting
variables stripped, while keeping every other inherited variable (the
staged workload's own `$HOME` above all) unchanged, since that hook
genuinely needs those. And bootstrap's own environment-setting request
handler stopped letting a broker-supplied variable set PATH, `LD_*`,
`BASH_ENV`, `ENV`, or `IFS` directly on substrate-serve's own PID 1 process
environment at all — those influence how a still-root process (and
anything it execs while inheriting its environment) resolves and runs
code, not merely what one lookup sees, so they are refused outright before
that loop applies anything, while every other workload variable still
passes through exactly as before.

A static test walks every root-context package's source and fails if any
`exec.Command`/`exec.CommandContext` call's command-name argument is a bare
literal, or a local variable last assigned from one, that isn't provably
routed through the shared resolver; anything else must be on a short,
individually-justified allowlist. This is the belt-and-suspenders layer: a
future regression that reintroduces a bare name at any of these sites fails
a build-time check, not just a runtime one.

## A second, independent fix: a shared-workspace credential helper that stopped working everywhere except Substrate

A prior hardening pass anchored the private, root-only working copy of a
shared workspace's rewritten git configuration under a directory that only
Substrate's own bootstrap sequence creates. On every other runtime —
Docker, Podman, Kubernetes, Apple containers, and rootless deployments —
that parent directory never existed, so the verification step failed
closed and no credential helper or git identity was ever installed there;
neither existing test suite's setup code exercised the absent-parent case,
since both pre-create the full chain unconditionally before any test in
either binary runs.

The fix picks among three strategies depending on how the process is
actually running, rather than a single one-size-fits-all path. A rootless
deployment (where this process already runs as the workload's own uid, with
no separate root identity to protect against at all) keeps the plain,
historical temporary directory. Substrate's enforced mode is unchanged —
its bootstrap sequence already creates and verifies the dedicated directory
ahead of time, and this still relies on exactly that, failing closed with
no fallback. Every other case (a real root process on a runtime that never
runs Substrate's bootstrap) self-heals onto the same hardened location
Substrate uses, verifying the standard system directory it lives under is
itself root-owned before creating anything beneath it; if that isn't
possible, it falls back to the ordinary temporary directory only after
independently confirming that directory can't be used to stage a symlink
swap (either it carries the sticky bit, or it is itself root-owned and not
group- or other-writable), and refuses outright, rather than ever falling
open into an unverified location, if neither check holds.

## Verification

Two previously-untested guards in the fd-based, ownership-verifying
directory-chain check (a leaf that already exists at a bad mode; an
ancestor that is itself a symlink, even to an otherwise-trusted directory)
now have dedicated tests, anchored under a real, already-trusted directory
rather than the world-writable system temp directory so each test actually
isolates the property it claims to cover. The bootstrap-time creation and
clearing of the private working directory described above, and its wiring
into the real bootstrap request handler, are now exercised end to end: a
stale file and a bad mode left over from an earlier bootstrap of a reused
actor are both gone by the time a real bootstrap request's response
commits to success, and a symlinked ancestor in that same chain is rejected
before anything is written or the workload starts.

Every fixed exec site that can plausibly be exercised without real root
privilege has a regression test in the shape of the underlying threat: a
fake binary is placed first on the process's own PATH, and the test
confirms it never runs while the real, resolved command still does. Build,
vet, and the full test suite for every touched package are green under a
scrubbed environment; the Python harness suite (updated for an unrelated
docstring/test-comment accuracy fix found during this pass) is unaffected
and green.

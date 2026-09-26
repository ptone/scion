# Lifecycle hook privilege enforcement on substrate

**Date:** 2026-09-26
**Branch:** scion/substrate-hooks

## Summary

`sciontool init`'s lifecycle hook scripts (`pre-start`, `post-start`,
`pre-stop`, `session-end`) previously ran in the root init process with no
ownership check, on every runtime. On most runtimes root there is only ever
a container-local convention that maps back to an unprivileged host
identity, so this was not a real privilege boundary. On substrate it is: a
workload that plants an executable `~/.scion/hooks/session-end` (or any
other event's directory) and exits gets it executed as root, since
post-start/pre-stop/session-end all fire while or after the harness — and so
the workload — controls `$HOME`.

This change makes hook execution a function of the script's own fstat'd
ownership in privilege-drop-enforced mode (`InitRunOptions.RequirePrivilegeDrop`,
set only by substrate-serve): a hook runs as root only if the script and
every directory in its chain up to `/` are root-owned and not group- or
world-writable; otherwise it runs dropped to the workload identity, with the
same env/cwd handling the harness child itself gets. Every other runtime is
unaffected — the enforced path is only taken when that flag is set.

## Design

- `pkg/sciontool/hooks.DecideExecAsRoot` — a pure function over fstat results
  (uid + permission bits), so the decision logic is unit-testable without
  root and without touching a real filesystem.
- `pkg/sciontool/hooks/exec_enforced.go` — opens the script's directory chain
  and the script itself with `O_NOFOLLOW` at every component, then execs the
  script via its own open file descriptor (`/proc/self/fd/<n>`), so the file
  the ownership check inspects is provably the file that runs. A symlinked
  hook or directory anywhere in the chain is refused, never followed.
- `LifecycleManager` gained `EnforcePrivilegeDrop`, `WorkloadUID/GID`,
  `WorkloadUsername`, and `WorkloadWorkingDir`, wired from the same values
  `RunInit` already resolves for the harness child process.

### Where trusted hook content lives on substrate

The container-script harness's pre-start wrapper (`20-harness-provision`)
and any project/hub pre-start hook (`30-project-custom`) are staged
host-side under `$HOME/.scion/hooks/pre-start.d/`. On substrate this content
travels through `POST /scion/v1/bootstrap`'s `files` array, and
`writeBootstrapFile` chowns everything it writes to the scion user — which
would make this broker-delivered content workload-owned before the
ownership check ever runs, dropping it even though it is not workload
content.

`writeBootstrapFile` now redirects any path under exactly
`$HOME/.scion/hooks/` to a dedicated directory, `hooks.EnforcedHooksDir`
(`/run/scion/hooks` — a single named constant), root-owned and never
chowned. Directories along the way are created by the existing
`mkdirAllTracked` (an `Lstat`-then-`os.Mkdir` walk that refuses a symlinked
or non-directory component — not an `openat`/`mkdirat` sequence with
`O_NOFOLLOW`), safe under bootstrap's own documented trust model:
substrate-serve is the sole writer and this all runs before the workload
exists. `$HOME/.scion/hooks` itself stays registered with the lifecycle
manager and subject to the same ownership rule, so anything the workload
plants there after the fact is still dropped. The redirect directory is
cleared before each bootstrap writes into it, and — this is what actually
makes stale content unable to survive, not the clear attempt alone — a
clear failure now aborts the bootstrap before any file is written or init
starts, rather than logging and continuing.
`cmd/sciontool/commands/init.go`'s abort-on-failure check for
`30-project-custom` looks in the redirected location in enforced mode.

`fixupEnforcedHooksDirChain` (its own file,
`cmd/sciontool/commands/substrate_enforced_hooks.go`, called from the same
site as the existing rootfs fixup) hardens any *pre-existing* ancestor of
the hooks directory (`/`, `/run`) that is group/world-writable or not
root-owned — `mkdirAllTracked` only fixes directories it creates itself. If
a bad mode or owner survives regardless, the affected hook simply runs
dropped instead of as root; the ownership check is what's authoritative, not
the fixup.

## Tests

- `pkg/sciontool/hooks`: table-driven coverage of `DecideExecAsRoot`
  (root-owned+protected chain, non-root owner, group/world-writable script
  or directory, empty chain, setuid bits not affecting the write-bit check);
  symlinked script/directory refusal; the constructed `exec.Cmd`
  (Credential/env/cwd) for both branches without needing root to run it; an
  end-to-end dropped-hook run (self-drop, skipped without `CAP_SETGID`); an
  end-to-end root-owned-chain run (skipped without root); non-enforced mode
  unchanged.
- `cmd/sciontool/commands`: the ancestor chain `fixupEnforcedHooksDirChain`
  walks; mode-stripping (no root needed); chown-to-root and no-op cases
  (skipped without root); symlinked ancestor refusal; missing-path no-op;
  `resolveProjectHookPath`'s enforced/non-enforced redirect.
- `pkg/sciontool/substrate`: the bootstrap path redirect (including a `..`
  trick resolved by `Clean` before matching, and one that escapes the prefix
  entirely); a redirected file is never chowned while an unrelated bootstrap
  file on the same server still is; clearing stale content before a write
  (unit-level and through the real HTTP handler); refusing a symlinked hooks
  root.

## Open item

The redirect above assumes `/run` is a real, root-owned, non-writable
directory on the root overlay (confirmed live on the current actor image).
If that ever stops holding for a given deployment, `hooks.EnforcedHooksDir`
is a single package var to repoint (e.g. to `/etc/scion/broker-hooks`).

## Follow-up hardening

Further scrutiny of the initial change surfaced a correctness gap and a
latent security gap, both fixed on the same branch:

- **Fail-closed stale-hook clear.** `handleBootstrap` used to log and
  continue if clearing `hooks.EnforcedHooksDir` failed, which could let a
  hub-removed hook survive (still root-owned) and run on the next bootstrap.
  A clear failure now aborts the bootstrap the same way a `writeBootstrapFile`
  failure does — before any file is written or init starts.
- **Root-hook environment hardening.** A root-eligible hook at any event
  after pre-start (post-start/pre-stop/session-end) used to inherit
  `HOME=<agent home>` — the workload's own, workload-owned directory — which
  would let a root-run python/bash/git/pip hook load workload-planted
  rc/site/config files and execute them as root. Such a hook now gets
  `HOME=/root`, `PYTHONNOUSERSITE=1`, and a fixed minimal `PATH`
  (`LifecycleManager.hardenedRootHookEnv`). Pre-start is exempt: its only
  root-eligible hooks run once, before the workload exists, and the
  provisioner specifically needs the agent home to find its bundle.
- **Skip, don't abort, on a refused workload-owned entry.** A symlink or
  non-regular entry a workload plants under a hooks directory other than
  `hooks.EnforcedHooksDir` used to abort every later hook for that event.
  It is now logged and skipped instead — `DecideExecAsRoot` would drop such
  an entry anyway, so refusing to run it was already correct; aborting the
  rest of the event on top of that was an availability regression the
  workload could trigger against its own later hooks. A refusal under
  `hooks.EnforcedHooksDir` itself still hard-fails, since broker-delivered
  content is never expected to contain one.
- **`SCION_HOOK_PATH`.** A hook run via `/proc/self/fd/<n>` sees `$0` as
  that magic path, not its own location. `SCION_HOOK_PATH` is now set to the
  real path so a script relying on it (e.g. `dirname "$0"`) still works.
- Added unprivileged tests exercising the exec mechanism directly (a
  shebang script run through `execViaFd`, a swap-after-open proving the
  original inode still runs, a non-executable skip) and the decision fed
  from real fstat results on an unprivileged fixture (`prepareEnforcedExec`,
  split out from `executeScriptEnforced` for exactly this purpose) — none of
  this previously needed root to test, and now it is.

## Writerless-FIFO hang fix

`openScriptNoFollow`'s leaf open used plain `O_RDONLY|O_NOFOLLOW`. Opening a
FIFO with no writer blocks that syscall indefinitely, so a workload that
plants one under a hooks directory (e.g. `~/.scion/hooks/session-end`) could
hang all hook processing. The open now also sets `O_NONBLOCK`, which returns
immediately regardless of file type; the existing fstat-and-reject-non-regular
check then refuses the FIFO the same way it already refuses a symlink, via
`ErrScriptRefused`. A socket special file fails the open itself with `ENXIO`
before there is an fd to fstat, so that errno is now also mapped to
`ErrScriptRefused` rather than surfacing as a generic I/O error. Neither
change touches the rest of the open/exec construction (the `O_NOFOLLOW`
chain walk, the fd-relative script open, or `execViaFd`).

A regression test drives `openScriptNoFollow` against a writerless FIFO and
a bound Unix socket, each wrapped in a goroutine with a hard timeout so a
regression back to blocking behavior fails the test instead of hanging the
binary.

## Working-directory hardening, a closed env allowlist, and the fd-3 exec construction

A hardened root hook after pre-start (post-start/pre-stop/session-end) still
inherited init's own working directory, which — nothing in `sciontool` ever
calls `Chdir` — is whatever the image sets as its `WORKDIR` (the workload's
own, writable git workspace). A root hook invoking a tool that resolves code
relative to its cwd (`python3 -c`/`-m`, `node -e`, `make`, a dotenv loader)
would load workload-planted content and run it as root, the same escalation
class `hardenedRootHookEnv`'s `HOME=/root` exists to close. `buildEnforcedCmd`
now pins `cmd.Dir = "/"` for every root-eligible hook at an event after
pre-start; pre-start itself is unaffected (its cwd stays whatever init's own
already is), and it now also gets `PYTHONNOUSERSITE=1` — cheap, and the only
guard available for the one scenario (a re-bootstrap over a `$HOME` a
workload already touched — see the design doc's Phase 2 section) where its
own "no workload yet" premise would not hold.

`hardenedRootHookEnv` no longer inherits the process environment and strips
a denylist; it now builds the environment from a closed allowlist of exact
variable names only (`LANG`, `TERM`) plus its own fixed overrides (`HOME`,
`PATH`, `PYTHONNOUSERSITE`, `PYTHONDONTWRITEBYTECODE`, `SCION_HOOK_PATH`).
Deliberately excluded: `TZ` (looks equally safe but is not on the list), and
every `SCION_*` variable, by name or by prefix — no root-eligible hook today
reads any of them, and passing an entire namespace through on the assumption
that none of its values is ever a workload-controlled path is exactly the
kind of inherited trust this hardening exists to remove. A future hook that
genuinely needs one adds it by name, with its own justification.

The fd the script is opened on is now `O_CLOEXEC`. It reaches the child not
by surviving the fork non-CLOEXEC at its own (arbitrary) descriptor number,
but through `exec.Cmd.ExtraFiles`, which duplicates it into a fresh,
independently-flagged descriptor at the fixed slot 3 in that one child —
`ExtraFiles`' own dup clears close-on-exec on the duplicate regardless of the
source descriptor's flag, which is what lets a shebang interpreter's own
re-exec of `/proc/self/fd/3` still resolve it. The net effect: the script
(which can carry secrets — `30-project-custom` is `0700` for exactly that
reason) is never inheritable by any OTHER, unrelated child this process
might fork while it happens to be open, only by the one child that is
actually supposed to run it. Verified end to end for both a `#!/bin/sh` and
a `#!/usr/bin/env python3` hook, on the as-root branch unprivileged and on
the dropped branch under the existing root/`CAP_SETGID` gate.

The stale-hook-clear fail-closed test previously covered only the
`bootstrapPathError` (422) branch; a second test now covers a generic
clear error (a permission failure removing a stale entry — the failure mode
that actually motivated the fix) and its 500 response, skipped as root since
root bypasses the DAC check the fixture depends on. The `initCalled`
assertions in both tests now use a buffered channel with a bounded wait
instead of reading a plain bool immediately after the response returns,
since `handleBootstrap` starts init in a goroutine.

## Working-directory hardening, environment allowlist, and fd-passing

Further scrutiny of the hardened-root-hook change surfaced one more gap in
the same escalation class and two hardening improvements, all fixed on the
same branch:

- **Working directory.** The `asRoot` branch of `buildEnforcedCmd` never set
  `cmd.Dir` for events after pre-start, so a hardened root hook inherited
  init's own cwd — the image's `WORKDIR`, a workload-writable location (e.g.
  the git workspace). Many interpreters resolve code relative to cwd
  (`python3 -c`/`-m` puts `''` first on `sys.path`, `node -e` reads
  `./node_modules`, `make` reads `./Makefile`, dotenv loaders read `./.env`),
  so a root hook that merely ran one of those tools would load
  workload-planted content. `cmd.Dir` is now pinned to `/` for every asRoot
  event after pre-start; pre-start is unaffected (its own cwd requirement is
  unchanged).
- **Environment allowlist, not a denylist.** `hardenedRootHookEnv` used to
  start from the full inherited process environment and override only
  `HOME`/`PATH`/`PYTHONNOUSERSITE`, leaving every other inherited variable —
  including anything an operator's harness/template configuration set via
  `cfg.Env` — to pass through untouched. It now builds the environment from
  a closed allowlist of exact variable names (`LANG`, `TERM`) plus its own
  explicit overrides; nothing else from the inherited environment reaches an
  asRoot post-pre-start hook. No shipped harness or hook consumes any
  `SCION_*` variable, so none is allowlisted, by name or by a blanket
  prefix — a future hook that needs one adds it explicitly. The dropped
  branch is unaffected: it keeps the harness's own, unfiltered environment.
- **Pre-start also gets `PYTHONNOUSERSITE=1`.** Costs the provisioner
  nothing (it needs `HOME`, not Python's per-user site-packages lookup) and
  removes one vector for the scenario recorded in the design doc's Phase 2
  section — a re-bootstrap over a `$HOME` a workload already touched — where
  the pre-start exception's own "no workload yet" premise would not hold.
- **Fixed-descriptor exec via `ExtraFiles`.** The script's own open file is
  now opened `O_CLOEXEC` and handed to the child through
  `exec.Cmd.ExtraFiles`, landing as a fresh, independently-flagged duplicate
  at a fixed descriptor (3) in that child only, which then execs
  `/proc/self/fd/3`. This replaces opening the fd without `O_CLOEXEC` and
  executing it at its own, arbitrary parent-side number: that construction
  relied on an invariant ("nothing else forks while a hook runs") rather
  than enforcing it, so the fd could in principle leak into an unrelated
  child this process forked concurrently. `ExtraFiles`'s per-child duplicate
  means the source fd never has to leave `O_CLOEXEC` to survive the target
  child's own exec.
- Added a 500-branch test for the stale-hook clear (a generic, non-path
  error — e.g. a permission failure removing a stale entry — must abort the
  bootstrap exactly like the already-covered 422 path) and tightened the
  existing 422 test's status assertion. Both now signal init-start through a
  buffered channel with a short poll instead of reading a plain bool
  immediately after the response, which could race the goroutine
  `handleBootstrap` starts `runInit` in.
- Added end-to-end coverage (unprivileged where possible; the same
  root/`CAP_SETGID`-gated skips as before otherwise) for both a `#!/bin/sh`
  and a `#!/usr/bin/env python3` hook through the fixed-descriptor exec, in
  both the root and dropped branches.

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
(`/run/scion/hooks` — a single named constant), created with `O_NOFOLLOW` at
every level, root-owned, and never chowned. `$HOME/.scion/hooks` itself
stays registered with the lifecycle manager and subject to the same
ownership rule, so anything the workload plants there after the fact is
still dropped. The redirect directory is cleared before each bootstrap
writes into it, so stale content from an earlier bootstrap can never
survive. `cmd/sciontool/commands/init.go`'s abort-on-failure check for
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
is a single constant to repoint (e.g. to `/etc/scion/broker-hooks`).

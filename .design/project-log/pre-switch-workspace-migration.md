# Pre-switch agent workspace migration (parked)

**Date**: 2026-11-03
**Issue**: follow-up to ptone/scion#7224 (docker per-agent workspaces on the shared layout)
**Status**: parked before any compile; the work is paused with the storage switch it serves.

## Summary

After ptone/scion#7224, a docker or podman clone-per-agent or empty-per-agent
agent on a shared project layout works in `<AgentRoot>/workspace` on the
share. Agents created before the switch keep their workspace in the
broker-local agents root, and start refuses them (`ErrAgentWorkspaceOffShare`).
This branch adds an explicit operator migration for them, leaving the
workspace source guard unwidened:

- `MigrateAgentWorkspaces` (pkg/agent) copies the broker-local workspace into
  `<AgentRoot>/.migrating-<agent>` (created with the NFS leaf helper, then
  opened from the `<AgentRoot>` descriptor and checked to be the same empty
  directory, owned by the migrator), verifies it against the source with a
  byte manifest (path, type, size, mode, mapped owner, link text, sha256;
  `.git`, untracked and ignored files included; no git run), renames it into
  place without replacing anything, and only then removes the local copy
  with the no-follow, contained delete.
- It refuses running agents (checked three times through the runtime),
  conflicts, special files, foreign-owned entries the agent could not read,
  an unsafe `agents/` parent, low space, and unreadable sources. Every result
  has a status and a fixed cause; no workspace-internal name is reported.
- `scion runtime-broker migrate-workspaces` is the human-only, unix-only
  entry point; the pre-switch start refusal names it.

## Findings

- At #7224's head a start creates the share leaf only after
  `checkLocalRuntimeAgentWorkspace`, which refuses while the share leaf is
  absent and the local workspace holds work, so a start cannot race the
  migration into a half state. The residual window is the fallback rename on
  NFS (no `RENAME_NOREPLACE`): an empty directory created at `workspace` by
  another creator between the fstatat and the renameat.
- When the share leaf exists and the local workspace differs, the migration
  refuses and start runs the agent on the share leaf with the local work
  unmounted; deleting the agent removes that work. The docs tell operators to
  resolve conflicts first.
- The agent's directory on the share is creator-owned with an inherited
  group that other agents of the layout can share; hence the staging
  descriptor checks.
- A new `runtime-broker` subcommand must be added to
  `brokerMachineLevelCommands` (cmd/project_gate.go), or the root hook
  resolves a project from the current directory.
- Guard wrappers that fail closed on a non-numeric cgroup `memory.max` cannot
  be selftested in a host container without a memory limit.

## State

Never compiled. The parking note (gs
`k8s-runtime/patches/kr-7224b/park/`) lists the rulings, commits, kit pins,
open items and resume steps.

---
name: workspace
description: >-
  Orientation for the agent's /workspace directory - how it is shared with other
  agents, what is safe to change, and the git protocol for each workspace sharing
  mode (shared-plain, worktree-per-agent, clone-per-agent), plus shared
  directories under /scion-volumes.
---

# Workspace Protocol

Your working directory is `/workspace`. How it is shared with other agents — and
therefore what is safe to change — is decided per project and reported to you in two
environment variables:

```bash
echo "$SCION_WORKSPACE_MODE"   # shared-plain | worktree-per-agent | clone-per-agent
echo "$SCION_WORKSPACE_GIT"    # "true" when /workspace is a git repository
```

`SCION_WORKSPACE_MODE` is set for every hub-managed agent; it can be absent on an
older or locally-started runtime. `SCION_WORKSPACE_GIT` is set **only** when the
workspace is a git repository — it is absent otherwise, never the string `false`, so
test for presence:

```bash
if [ -n "$SCION_WORKSPACE_GIT" ]; then ...
```

Read the reference for your mode before your first write to `/workspace`.

| `SCION_WORKSPACE_MODE` | Who else sees your working tree | Reference |
|---|---|---|
| `shared-plain` | Every agent in the project, immediately | `references/shared-plain.md` |
| `worktree-per-agent` | Only an agent on the same branch name — but the clone, its history and its branch names are shared | `references/worktree-per-agent.md` |
| `clone-per-agent` | No one | `references/clone-per-agent.md` |

Two things hold in **every** mode:

- **Shared directories stay shared**, including in the two isolated modes. See
  `references/shared-directories.md`.
- **Git must never open an editor.** When `SCION_WORKSPACE_GIT` is set, follow
  `references/git-non-interactive.md`. A command that opens `vi` will hang until
  something kills it.

Should `SCION_WORKSPACE_MODE` be empty or hold a value not in the table — an older
hub, or a runtime that predates the variable — treat the workspace as `shared-plain`.
That is the platform's own default for an unrecognized mode, and the cautious
choice: the isolated modes forgive mistakes that `shared-plain` does not.

# Shared directories

Shared directories are storage mounted into several agents at once. They are
**independent of `SCION_WORKSPACE_MODE`**: they are shared in every mode, including
`worktree-per-agent` and `clone-per-agent`, where the workspace itself is private.
Workspace isolation gives you no isolation here.

## Where they are

A project may define any number of shared directories. Each one is mounted at
**exactly one** of these locations — never both:

| Location | When |
|---|---|
| `/scion-volumes/<name>` | default |
| `/workspace/.scion-volumes/<name>` | when the directory is configured in-workspace |

`$SCION_VOLUMES` is usually set to `/scion-volumes` when the project defines shared
directories, including ones mounted in-workspace. It names the default location; it
does not tell you what is mounted there, and it is absent in some configurations.
Do not infer from it — list the directories:

```bash
ls /scion-volumes/ 2>/dev/null
ls /workspace/.scion-volumes/ 2>/dev/null
```

In-workspace directories sit inside a git workspace but are mount points, not repo
content. **Git does not ignore them** — `.scion-volumes/` shows up as an ordinary
untracked directory, so `git add -A` will happily stage another agent's data into
your commit. Never stage it.

## Using them safely

Treat every shared directory as concurrent-access storage.

- **Give files unique names.** Prefix with your agent name, or use a per-agent
  subdirectory. `notes.md` at the root of a shared directory will be overwritten.
- **Append rather than rewrite** when adding to a shared log or list; a
  read-modify-write cycle loses anything another agent wrote in between.
- **Never assume exclusivity.** A file you wrote may have been changed or removed by
  the time you read it back. Re-read before acting on prior contents.
- **Do not delete or reorganize another agent's files** — including tidying stale
  ones. What looks abandoned is often in use.
- **Do not store secrets** you were not explicitly told to place there. Every agent
  with the mount can read them, and shared directories usually outlive your agent.

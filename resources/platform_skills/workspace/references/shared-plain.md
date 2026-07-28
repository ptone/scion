# Workspace mode: `shared-plain`

Every agent in the project works in this same directory. There is no isolation of
any kind: your writes appear in other agents' view of the tree the moment you make
them, and theirs appear in yours.

## Working alongside other agents

- **Do not assume a file is unchanged between reading it and writing it.** When
  correctness depends on the previous contents, re-read immediately before writing.
- **Prefer targeted edits to whole-file rewrites.** A rewrite silently discards a
  concurrent edit made by another agent between your read and your write.
- **Announce broad or structural changes before you make them** — renames, moves,
  dependency upgrades, repo-wide formatting, or edits to files you were not asked to
  own. Use `scion message` (see the `scion-messaging` skill).
- **Leave other agents' work in place.** Do not delete, move, or revert changes you
  did not make merely because they are unfamiliar or appear unfinished; another
  agent is probably mid-task.
- **Write scratch output to your own path.** Use a unique filename or your own
  subdirectory rather than a shared name like `notes.md` or `/tmp/out.json`.

## When the workspace is also a git repository

If `$SCION_WORKSPACE_GIT` is set, all agents share **one checkout**. Git commands
that touch the working tree or `HEAD` do so for everyone at once, with no warning to
them. Unless the project has explicitly given you ownership of repository state,
avoid:

| Command | Effect on other agents |
|---|---|
| `git checkout <branch>`, `git switch` | Swaps the tree out from under every agent |
| `git reset --hard`, `git stash` | Discards their uncommitted work |
| `git rebase`, `git merge` | Moves `HEAD` and writes conflict markers into their files |
| `git clean -fd` | Deletes untracked files they are still using |

Read-only inspection is always safe: `git status`, `git diff`, `git log`, and
`git show <ref>:<path>`.

Committing is safe **only if you stage explicit paths**. The index is shared too, so
`git add -A` and `git commit -a` will sweep up other agents' in-progress changes and
attribute them to your commit:

```bash
git add path/to/file.go path/to/other.go   # never -A, never -a
git commit -m "message"
```

If you need to work without disturbing anyone — switching branches, rebasing, or
running an experiment — ask the project for a `worktree-per-agent` or
`clone-per-agent` workspace instead of improvising isolation inside the shared one.

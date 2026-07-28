# Non-interactive git

This applies in every workspace mode whenever `$SCION_WORKSPACE_GIT` is set.

You have no terminal a human is watching. Any git command that opens an editor —
`vi`, `vim`, `nano` — blocks forever waiting for input that will never arrive, and
the orchestration system reads you as stalled. Prevent it rather than recovering
from it.

Set this once, before your first git command:

```bash
git config --global core.editor true
```

`true` is the command that exits 0 immediately, so git accepts the default message
and moves on.

Use `--global`, which writes your own `$HOME`. A plain `git config` writes the
repository's `.git/config` — shared by every agent in `shared-plain`, and by every
worktree of the base clone in `worktree-per-agent`. Exporting `GIT_EDITOR=true`
works too and changes nothing on disk.

## Commands that would otherwise open an editor

| Instead of | Use |
|---|---|
| `git commit` | `git commit -m "message"` |
| `git merge <ref>` | `git merge <ref> --no-edit` |
| `git revert <sha>` | `git revert <sha> --no-edit` |
| `git rebase --continue` | `GIT_EDITOR=true git rebase --continue` |
| `git rebase -i` | avoid — it drives a second editor for the todo list; use `git rebase`, `git commit --amend -m`, or `git reset --soft` instead |

Pagers hang the same way. If a command's output could be long, disable the pager:

```bash
git --no-pager log --oneline -20
```

Authentication prompts hang too. Export `GIT_TERMINAL_PROMPT=0` so a command that
needs credentials fails immediately instead of waiting:

```bash
export GIT_TERMINAL_PROMPT=0
```

## Resolving conflicts

Check which mode you are in before resolving anything — in `shared-plain` a rebase
or merge writes conflict markers into files every other agent is reading, so
coordinate first.

1. List the conflicts: `git status --short` (`UU` marks a conflicted path).
2. Edit the files to resolve them, removing every `<<<<<<<`, `=======`, `>>>>>>>`
   marker.
3. Stage the resolved paths: `git add <path>...`
4. Continue: `GIT_EDITOR=true git rebase --continue`, or `git commit --no-edit` for
   a merge.

To abandon the attempt and return to where you started, use `git rebase --abort` or
`git merge --abort`. Prefer aborting over forcing a resolution you are unsure of,
then report the conflict rather than guessing.

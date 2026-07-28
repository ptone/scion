# Workspace mode: `worktree-per-agent`

`/workspace` is a git worktree attached to a base clone that is **shared with the
other agents in the project**. Only the working tree is yours:

| Yours | Shared with every agent in the project |
|---|---|
| The files on disk and your uncommitted changes | Commit history and all git objects |
| The branch you have checked out | The branch namespace and every ref |
| | Repository config, remotes, and tags |

The base clone is a **full clone**, so history here is complete.

You start on your own branch, created when the workspace was provisioned. It is
named after your agent, unless the project configured a branch explicitly. Confirm
rather than assume:

```bash
git branch --show-current
```

Your worktree is keyed by that **branch name**, not by your identity. An agent that
resolves to the same branch name — you after a restart, or another agent given the
same name — attaches to this same worktree and sees your uncommitted files. Privacy
here means "private from agents on other branches."

## Branches

- **Do not check out `main`.** The base clone's HEAD is deliberately detached, so it
  owns no branch and `git checkout main` will usually *succeed* — and that is the
  problem. Taking `main` locks it against every other agent and against the
  provisioning of new ones. Stay on your own branch and reference `main` directly:

  ```bash
  git diff main...HEAD        # what your branch changed
  git log main..HEAD          # your commits
  git show main:path/to/file  # a file as it exists on main
  ```

- **You cannot check out a branch another agent's worktree holds.** That checkout
  fails with "already checked out." It is a git restriction on shared clones, not a
  permissions problem — do not try to work around it.
- **Branch names are a shared namespace.** A name like `fix-auth` may already exist
  or be held by another agent. Prefix new branches with your agent name.
- **Do not run `git worktree add`, `remove`, or `prune`.** Scion registers and
  reclaims worktrees; manual changes corrupt that bookkeeping.

## Committing and history

Commit as often as you like. Commits write objects and move only your own branch;
they never disturb another agent's working tree.

Do not rewrite history that is not exclusively yours. Every agent resolves refs from
the same object store, so `git reset`, `git rebase`, `git commit --amend`, or a
force-update applied to a shared branch such as `main` changes what other agents
see. Rebasing *your own* branch onto `main` is fine:

```bash
git rebase main
```

Repository config is shared too, so avoid `git config` without `--global` — see
`git-non-interactive.md`.

## Remotes

Whether `origin` is reachable depends on how the project was configured; it is not
guaranteed either way. Test instead of assuming, and fall back to treating the local
`main` as the source of truth:

```bash
git ls-remote origin HEAD >/dev/null 2>&1 && echo reachable || echo local-only
```

Never push to a shared branch unless you were asked to. Pushing your own branch is
usually the right way to publish work — but confirm the project's convention first,
because the remote is shared by every agent.

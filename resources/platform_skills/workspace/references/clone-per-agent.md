# Workspace mode: `clone-per-agent`

`/workspace` is a git repository that belongs to you alone. No other agent can see
its working tree, history, refs, index, or stash, and nothing you do here affects
them.

This is the least constrained mode. Branch, check out, rebase, reset, stash, and
rewrite history freely — the restrictions that apply to `shared-plain` and
`worktree-per-agent` do not apply to you.

## What you start with

The workspace is built in-container: `git init`, then a **shallow fetch** of a
single branch, then two checkouts.

| | |
|---|---|
| Upstream branch fetched | `$SCION_GIT_BRANCH`, defaulting to `main` |
| Fetch depth | `$SCION_GIT_DEPTH`, defaulting to **1** |
| Branch you are on | `$SCION_AGENT_BRANCH`, defaulting to `scion/<agent-name>` |
| Remote URL | `$SCION_GIT_CLONE_URL` |

A local branch for the upstream branch exists (normally `main`), and your own branch
was created from it. `$SCION_GIT_BRANCH` is the branch that was *fetched*, not the
one you are on — check with `git branch --show-current`.

## The clone is shallow by default

Depth defaults to 1, so `git log` shows roughly one commit and anything that needs
older history is unreliable: `git blame`, `git bisect`, `git describe`, and
comparisons against an older tag or commit. Comparing against `main` still works,
because `main` and your branch share the fetched tip.

```bash
git rev-parse --is-shallow-repository   # true | false
```

If you need real history, deepen first — otherwise `git merge-base` exits non-zero
and `git diff A...B` fails with "no merge base":

```bash
git fetch --unshallow origin    # or: git fetch --depth=50 origin
```

Only the one branch was fetched, but the remote refspec is unrestricted, so other
branches are one fetch away:

```bash
git fetch origin <branch>
```

## Your work is invisible until you publish it

Isolation cuts both ways: no one can review, test, or build on a commit that exists
only in your clone. When the task is done, push your branch or produce a patch — do
not assume a coordinator can see your commits. Reachability of `origin` is not
guaranteed, so test it:

```bash
git ls-remote origin HEAD >/dev/null 2>&1 && echo reachable || echo local-only
```

## Shared directories are still shared

Workspace isolation says nothing about directories mounted under `/scion-volumes` or
`/workspace/.scion-volumes`. Other agents read and write those concurrently even
though your clone is private. See `shared-directories.md`.

# Upstream drift check: held-edits PR head ec8a95af vs main 181bbc50

Read-only drift gate: no code changed and no commit to the PR branch. Head
`ec8a95af` stays frozen.

## Result

- Upstream main tip at report time: `181bbc50b712`. Merge base: `d087626e4cce`.
  Upstream range: 14 commits.
- Four files overlap: `docs-site/.../reference/api.md`,
  `pkg/experiments/registry.go`, `pkg/hub/handlers_agent_lifecycle.go` and
  `pkg/hub/handlers_agents_core.go`. They hold 17 head hunks and 7 upstream
  hunks, and no two hunks share a line.
- `git merge-tree` is clean (tree `98400845`). In each file, both sides' hunks
  survive byte-exact (patch-id equal).
- The dupscan v3a pass over the merge tree finds 0 duplicates in pkg/hub and
  in pkg/experiments, in all three tag sets.
- The removed-identifier check, run both ways, finds 0 hits. The head turns
  the free function `redactAgentConfigsForResponse` into a method, and the
  merge tree has no free-call site left (the same grep finds 13 at the
  upstream tip).
- secret-shape-scan v3 is clean, and its canary is detected.
- All 24 per-hunk negative-control plants produce a merge conflict.
- Decision: WAIVER for every hunk, and no hunk is COUPLED.
  `handleAgentLifecycle` is the only function both sides edit. The head side
  is mechanical call renames on response lines. The upstream side adds an
  early return on the dispatch-error path, which already returned.
- Record (bucket, create-only):
  `ae/ae6/r274-drift-181bbc50b71217bc9b3a4922e34f48d60183653e.md`, sha256
  `9ff37794…f06a`.

## Notes for the next drift check

- If the checkout is shallow, `git merge-base` returns nothing. Run
  `git fetch --unshallow` first.
- `git merge-file` with process substitution (`<(git show …)`) reads empty
  input in zsh. Write the three blobs to temp files.
- Keep hunk-term greps narrow. "held" also matches agent standing holds
  (`pp-held-agent`), which are a separate concept from held edits.
- Main moved twice during this check. Run every step from one script that
  takes the tip as an argument and checks that the tip is unchanged at both
  START and END.
- The upstream pull request's green CI ran on a merge with the old base. The
  merge-ref CI at the current tip is still owed before merge.

# Brief: sn-pr — get the single-node work proposed for merge

**Dispatched by** `sn-impl-arch`, 2026-08-26, with ptone away 7–10 h.

## The problem

`scion/dev-rebase-1294` is **18 commits ahead of `main`, 0 behind, and has never
been proposed for merge.** All of P0–P5 of the single-node tier lives there.
Everything in it has been implemented and validated against **live Cloud Run
Instances** — not local substitutes — but none of it is reachable by a reviewer.

## What to do

**Two PRs. Not one, not five.**

### PR 1 — the security fix, on its own, first

Commit `f0b84e12` — *fix(security): refuse dev auth on non-loopback interfaces
(P0-S1)*. Touches `cmd/server_foreground.go` (+10) and its test (+67).

Cherry-pick it onto a fresh branch off `origin/main` and open it as a standalone
PR. **Rationale to put in the PR body:** a ten-line fix closing a real exposure
(dev auth answering on a non-loopback interface) should not wait behind a
4000-line review.

### PR 2 — the rest

The remaining 17 commits as a single PR from `scion/dev-rebase-1294`.

**Do not split it by phase.** I considered that and rejected it: the phases are
sequential on one subsystem and none of them can be independently exercised, so
five PRs would be *worse* review, not better. Say so in the PR body, along with
why the size is acceptable:

- It is overwhelmingly **additive**. `pkg/runtime/cloudrun_sandbox_runtime.go`
  (910 lines) and `cloudrun_sandbox_delete_workaround.go` (243) are new files,
  reachable only when the `cloudrun-sandbox` runtime is selected.
- **1556 of the ~4263 added lines are tests.**
- The only real integration points are `pkg/runtime/factory.go` (+33) and
  `pkg/runtimebroker/pty_handlers.go` (+189).

**Structure the PR body by phase** — P1 registration, P3 runtime, P4 exec control
plane, P4a delete workaround, P5 ephemeral honesty, plus the omni image — with the
commit SHAs, so a reviewer can go commit-by-commit.

**Call out the two things a reviewer must not miss:**

1. **The delete workaround is deliberately isolated for removal.** It is a shim
   around a platform defect ptone has filed with the Cloud Run team, kept in its
   own file behind `SCION_CLOUDRUN_DELETE_WORKAROUND=off`, and it references
   `.design/project-log/defect-sandbox-delete-hang.md`. It is temporary by design.
   Validation: sandboxes unreachable **<1 s** serially and under 5-way fan-out, so
   the 10 s timeout carries ~10× margin.
2. **The `List()` stopgap** (§9.1b) reports `ExitCode=nil` for stopped sandboxes.
   There is a comment naming both conditions required to remove it. It is not an
   oversight, and the comment is load-bearing — don't let a reviewer "clean it up".

### Third deliverable — consolidate the branches

ptone, 2026-08-26: *"we should be consolidating onto one or just a few integration
branches for this pattern."* This is the anti-pattern he means:

| Branch | Status |
|---|---|
| `scion/dev-rebase-1294` | **The integration branch.** Contains P4a (`0a1536b3`) and P5 (`465628f1`). |
| `scion/p4a-delete` | Superseded duplicate — earlier P4a attempt |
| `scion/p4a-delete-v2` | Superseded duplicate — second P4a attempt |
| `scion/p5-ephemeral` | Superseded duplicate — P5, already in the integration branch as `465628f1` |

**Verify each of the three really is contained** in `dev-rebase-1294`'s tree
content before deleting — the SHAs differ because the work was rebased/cherry-picked,
so `--contains` will say no. Compare the *diffs*, not the commits. **If any carries
work that is not in the integration branch, stop and tell me** rather than deleting it.

Once verified, delete the three remote branches, and state in PR 2's body that
`scion/dev-rebase-1294` is the single integration branch for this tier so the next
agent does not start a fourth.

Note PR 1 does create one new branch. That is a **PR branch, not an integration
branch** — short-lived, deleted on merge — so it is consistent with the instruction.

## Hard constraints

- **Do not merge anything.** Opening a PR is proposing; merging is ptone's gate and
  he is away. Not even PR 1.
- **Do not rebase, squash, force-push, or otherwise rewrite `dev-rebase-1294`.**
  It is the validated integration branch and other agents' work references its
  SHAs. Create new branches; leave that one alone.
- Do not change code to make CI pass. If CI fails, **report it to me** — a failure
  here is a finding, not a chore.

## Report

Message `sn-impl-arch` with both PR numbers and CI status. If anything about the
two-PR split turns out to be wrong once you see the diff, say so rather than
forcing it — I'd rather revise the plan than have you implement a bad one.

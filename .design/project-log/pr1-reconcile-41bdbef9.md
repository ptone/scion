# PR1 rebase: pick-7 (41bdbef9) reconciliation and rebase completion

**Date:** 2026-10-01
**Agent:** dev-pr1-reconcile
**Branch:** scion/dev-pr1-reconcile (this entry); work performed on a local `pr1` branch tracking `scion/substrate-pr1`

## Context

PR1 (`scion/substrate-pr1`, a root-context filesystem/exec hardening series) needed rebasing onto `upstream-main` after upstream independently introduced `pkg/sciontool/procreap` (a managed-PID reaper fixing a SIGCHLD race) covering the same problem space PR1's own `pkg/sciontool/rootexec` hardening and reaper logic targeted. A prior dev rebased picks 1-6 and escalated the first genuine reconciliation point (commit `79efae4b0`) without resolving it. This task picked up from that handover, resolved `79efae4b0` and every later pick, and finished the rebase (31 original commits + 2 new ones from this reconciliation).

## What changed

- **Commit `79efae4b0` (init.go gitconfig hardening) and its recurrences** (`008d5535` iptables.go, `40589a69` init.go's own later gitconfig redesign): kept PR1's symlink/root-owned-tmpdir/rootexec-resolved hardening in every case, but routed each site's actual git/iptables invocation through `procreap.CombinedOutputManaged` instead of a raw `cmd.CombinedOutput()`. Carried this "every exec reachable from `RunInit`'s PID-1 path while the reaper is active must be managed" rule through the whole rebase, re-running and regenerating `pkg/sciontool/rootexec`'s line-numbered exec-site allowlist (`guard_test.go`) twice as init.go's line numbers shifted.
- **Commit `41bdbef9` (runtimebroker: unify agent-target lookup)** — a genuine architectural collision, escalated rather than resolved alone: HEAD's `Phase`-staleness fix and PR1's `Runtime`-pairing race fix touched the same agent-lookup path with different designs. Lead ruling (H-40/H-43/H-44, relayed via sb-em) merged both: `lookupAgentTarget` is the sole lookup for exec/reset-auth/stop/restart; `AgentLookupResult` carries both `Phase` and `Runtime`; every handler uses the same three-way error classification (`ErrAgentListUnavailable`→503, other real/ambiguous error→500 generic-message, not-found/empty→404), with the underlying error always logged server-side only, never in a response body. Two PR1-authored tests pinned to the pre-ruling 404 behavior were updated to match (exact 503 + no-leaked-error-text assertions).
- Full rebase completed: 31 original PR1 commits replayed, 3 new commits added (a project-log wording fix matching the merged design, a test-assertion fix for the H-44 ruling, and a test-infra synchronization fix for H-45 — see below).

## Verification

Full gate suite green (linux + darwin amd64/arm64 build, vet, gofmt, targeted + full package tests, `-race -count=5` on the reaper/clone-cleanup tests, `-race -count=3` on `pkg/runtimebroker`). Two adjacent bugs were found along the way:

- A `pkg/sciontool/log` initialization race — pre-existing, confirmed present at the merge-base and on current real upstream `main`; the lead routed it to roadmap-lead as a separate fork issue. Not fixed here; worked around in this reconciliation's own new test only.
- A data race in the shared `mockManager` test double (`pkg/runtimebroker/handlers_test.go`), introduced by the combination of an upstream test-mock addition (`lastListFilter` tracking) and PR1's own pre-existing heartbeat-concurrency tests — confirmed absent on the original pre-rebase PR1 tip, confirmed present after the rebase. The lead (H-45) clarified the same underlying field is already unsynchronized on upstream `main` too (it just doesn't surface there without a test exercising it concurrently the way PR1's does) and asked for it to be fixed here as its own commit, since doing so resolves it on upstream as well once PR1 lands. Fixed: added a `sync.Mutex` to `mockManager`, locked every write, and routed every test-file read through a locked accessor (commit `0742a587`). `go test -race -count=3 ./pkg/runtimebroker/...` is now clean.

## Outcome

Reconciled PR1 pushed to `scion/substrate-pr1-reconcile-wip` for a fresh code review + security audit. The real `scion/substrate-pr1` branch was deliberately left untouched pending that review and the lead's clearance.

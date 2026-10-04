# Substrate restart durability, Phase 3: per-id lock, stale takeover, reconciler

**Date:** 2026-10-04
**Branch:** scion/substrate-restart-durability-p3
**Issue:** ptone/scion#2818 (Phase 3)

## Problem

Phase 1 made agent state durable: there is one state object per agent, and it moves through the phases pending → committed → deleting. That left three gaps.

- **Crash leftovers.** If a broker died mid-Run or mid-Delete, it left a pending or deleting object, and sometimes an actor, that nothing would ever clean up.
- **Orphaned committed objects.** If an actor vanished while its committed state object survived, the name stayed "already in use" forever.
- **No serialisation within a process.** Run and Delete for the same id were serialised only by CAS conflicts in the store.

## Solution

- **Per-id lock** (`pkg/runtime/substrate_reconcile.go`).
  - Run and Delete hold an in-process lock per agent id for their whole duration. It is a process-wide set by default, and tests can override it per runtime.
  - The reconciler only `tryLock`s, so it skips ids that a live operation is using.
  - Effect: a Delete in the same process as an in-flight Run now waits for the Run and then deletes the agent. Across processes, the Phase 1 CAS semantics still apply.
- **Stale takeover in Run.** When `Create` finds an existing object, Run removes it only if it is provably orphaned, then retries its claim once. Orphaned means one of:
  - committed, with its actor absent (NotFound, or a different UID);
  - pending for longer than `2 × (template-ready timeout + healthz timeout)` (30m with the defaults), with no actor.

  The removal is a delete with a resourceVersion precondition. A fresh pending object is never taken over, and a failed lookup fails closed.
- **Reconciler.**
  - It runs in the background, 1m after startup and then every `state_reconcile_interval`, with one sweeper per state namespace.
  - Each sweep is bounded to 256 objects and resumes from a cursor.
  - It applies the six rows of the design table:

    | State | Actor | Action |
    |---|---|---|
    | pending, stale | absent | delete the state |
    | pending, stale | present | mark deleting (CAS), delete the actor, then delete the state |
    | deleting | present | retry the actor delete, then delete the state |
    | deleting | absent | delete the state |
    | committed | absent | delete the state |
    | none (legacy, no state object) | present | leave it alone |

  - It logs counts and object names only.
- **Setting.** `runtimes.<name>.substrate.state_reconcile_interval` is an optional Go duration, default 10m, minimum 1m. It is validated at startup, and an invalid value refuses broker start.
- **Object age** comes from the server-set `metadata.creationTimestamp`. The persisted schema and the store interface are unchanged.

## Testing

- One test per reconciler row, plus negative tests: live agents are left alone, a lookup failure fails closed, held ids are skipped, sweeps are bounded and resume, and the loop timing is checked through a timer seam.
- M1: a committed object whose actor is gone is taken over right away, and is also reusable after a sweep. Negative control: a fresh pending object from another instance makes Run fail and is left untouched.
- Fault injection: a wrapper around the real store kills the process (a panic) or fails at every store step of Run and Delete, and at the CreateActor and ResumeActor points. A later process's sweep always converges to no orphaned state and no orphaned actor, and leaves committed agents manageable.
- Cross-process tests: two runtimes with separate lock sets share one store and cluster. Delete is interleaved at each step of Run, Run is interleaved into Delete, two Runs race for one id, and a parallel Run-plus-Delete stress test runs under `-race`.
- No-leak: reconciler and takeover logs and errors were checked against tokens and secrets embedded in control-plane and store errors.
- Non-vacuity was shown by breaking the takeover (M1 and its negative control go red) and by neutering rows 3 and 5 (their tests go red).

## Notes

- The process-wide caches mean the two-runtime tests share a cache. Correctness relies on the store, not the cache.
- `pkg/runtime/cloudrun` `TestStreamLogsPropagatesListingErrors` has a data race under `-race` that already exists on the base commit. It is unrelated to this change.

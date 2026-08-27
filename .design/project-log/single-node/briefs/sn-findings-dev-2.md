# Brief: file two more measured defects from the stress test

Author: sn-impl-arch (architect). Date: 2026-08-27. Follow-on to `sn-findings-dev.md`. Tasks #67, #69.

You already filed `ptone/scion#1303`–`#1306` from the first brief. Same rules apply and I am not
restating them in full. In particular: **§1 of the first brief, the reference trap, still governs.**
Fully qualify every cross-repo reference. Fork only. No `Fixes #N`. Search before filing each.

There are now **nine** known fork/upstream collisions — the table in `ptone/scion#1297` is current.
**Read it before you write a reference.** The collision list can never be complete, because both
repos are active and share a number space. The habit is the fix, not the table.

---

## 1. Issue 1 — hub DELETE removes the agent from the hub database but does NOT kill the sandbox

**Measured, and verified twice independently.**

On the `sn-stress-max` Instance (8 CPU / 32 GiB), a test agent `test-claude` was created at
07:28:13Z and then deleted through the hub API. The sandbox `retest--test-claude` **stayed alive**.
Broker liveness probes (`sandbox exec <name> -- /bin/true`) returned `exit_code=0` for it
continuously until 07:50:33Z — **twenty-two minutes after the hub believed it was gone**, and right
through the whole of the Phase B ladder.

Verification: `sn-stress-max` found it in Cloud Logging. I then ran my own enumeration over the same
window without using its numbers, and got the identical name and the identical first/last
timestamps. Two independent derivations, one result.

Facts that must be in the body:

- The sandbox survives hub deletion and **keeps consuming the Instance's CPU and memory budget**.
- It is **invisible to the operator**. It is not in the hub agent list — it was deleted from there.
  And `getStats` returns hardcoded zeros (`ptone/scion#1304`), so no instrument shows it either.
- **It corrupted a measurement.** The Phase B ladder undercounted alive sandboxes by one at every
  step, because the leaked sandbox was consuming budget while nothing counted it. A stress test with
  an explicit instrument-validation step still missed it. That is how invisible this is.
- **This is the exact mirror of task #17 on this project**, where the hub reported an agent as
  `running` after its sandbox had died. That was hub-alive/sandbox-dead. This is
  hub-dead/sandbox-alive. **Both are the same missing reconciliation between hub state and broker
  liveness, in opposite directions.** Say this — it is the most useful sentence in the issue,
  because it tells the reader the fix is one mechanism and not two patches.
- Combined with `ptone/scion#1303` (exceeding the ceiling destroys the Instance), a leaked sandbox
  moves the operator closer to a cliff they cannot see, for reasons they cannot discover.

Do **not** assert the leak rate, or that every delete leaks. **We have one observation.** Say it is
one observation. Whether the leak is universal or conditional is unmeasured, and that is a fine
thing for an issue to say.

## 2. Issue 2 — a sandbox can die leaving NO log entry at all

On the `sn-stress-def` Instance (4 CPU / 8 GiB), the sandbox behind agent `idle-1` died with **no
log record of its death**: no signal, no exit code, no `sandbox wait` end event, nothing. The only
evidence it had gone was that the next `sandbox exec` against it failed — and failed in about
**10 ms**, which says the CLI knew immediately that the sandbox was absent.

So the death is *detectable on demand* but *never reported*. Nothing polls, so nothing notices.

- Source evidence is on the shared volume, not in an agent that still exists:
  `/scion-volumes/scratchpad/projects/single-node/sn-stress-def-phase-a.csv` and the
  `sn-stress-max-final-report.md` alongside it. **Read them; do not ask the agents — both are being
  torn down.**
- Relate it to `ptone/scion#1281` (session metrics lost, `exit_code` never persisted) — **related,
  not identical.** `#1281` is about a recorded exit code failing to persist. This is about there
  being **no event at all** to record. Distinguish them explicitly or the next reader will close one
  as a duplicate of the other.
- A similar-looking case: `w-1` on the max Instance stopped appearing in logs at 07:38:37 while
  every other agent ran to 07:50+. **Mention it as consistent, and do NOT claim it is the same
  mechanism.** Symptom identity is not cause identity — that error has been made three times on this
  project, once by me.

## 3. What you must NOT do

- **Do not fix either of these.** File only.
- **Do not merge these two into one issue.** They surfaced together; there is no evidence they share
  a cause.
- **Do not open anything upstream.** Fork only.
- **Do not touch any branch, PR, or code.** Do not delete any Instance or agent. Both stress
  Instances are already gone; `e2e-omni`, `e2e-walk-r2`, `iap-demo`, `q2-control`, `sn-ready`,
  `sn-adminseed-t`, `sn-adminfix-t` remain **do-not-delete**.

## 4. Report back

Message `sn-impl-arch` with the issue numbers as `ptone/scion#NNNN`, any duplicate you found and did
not file, and **anything here you think I have got wrong.** You corrected me on the
`server_dispatcher.go` path this morning and I would rather that happened again than not.

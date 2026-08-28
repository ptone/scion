# Task #92 delta 4 / task #93 — Shape B. **HELD: DO NOT START.**

Author: sn-impl-arch (architect). Date: 2026-08-28, 04:05. Branch `scion/task-92-runtime-profile-fix`
@ `dc729e2` (**APPROVED** — do not disturb what is there).

**THIS BRIEF IS NOT DISPATCHED.** It is written ahead of ptone's decision so that if he approves, work
starts immediately instead of after I write it. **If you are reading this without a message from me
saying "Shape B is approved, execute the held brief", stop here and do nothing.** I have caused a
deletion once tonight by a message that was clearer than I intended (rule 21); this header is the
counterweight.

---

## The decision this is waiting on

Task #93: the UI still offers `remote (kubernetes)`, which this tier cannot run. Two ways to close it:

- **(a) Shape B — fix the filter.** Correct, and it changes `buildInfoProfiles`, which is **shared with
  the multi-node cloudrun tier and the kubernetes tier**.
- **(b) Decline.** Task #92 lands as approved; the broken option stays in the menu; task #93 closes as
  won't-fix-for-now.

Only ptone can weigh (a)'s blast radius against a menu with a trap in it. **If (b), skip to the last
section — there is a one-line tidy and nothing else.**

---

## What Shape B is

`pkg/runtimebroker/handlers.go`, in `buildInfoProfiles`. Replace:

```go
if !isLocalOnlyRuntime(defaultRuntimeType) && isLocalOnlyRuntime(rtType) {
    continue
}
```

with a positive predicate:

```go
// canBrokerServeRuntime reports whether a broker running brokerType can dispatch
// an agent to a profile declaring profileType.
//
// A non-local broker serves only its own runtime type: it has no mechanism to
// reach any other. A local broker is the exception — it may dispatch to remote
// runtimes as a client.
func canBrokerServeRuntime(brokerType, profileType string) bool {
    if brokerType == profileType {
        return true
    }
    if isLocalOnlyRuntime(brokerType) {
        return true
    }
    return false
}
```

**That function doc is the invariant you asked me to state in a comment rather than design around.**
It is now the whole of the function, which is the strongest form of stating it.

## Why the shape changed and why the reason matters

I rejected Shape B as *dominant* earlier tonight, for one reason: on a broker whose type matches no
declared profile it empties the list, and the `len(profiles) == 0` tail then synthesises a profile named
`default` that **is not in settings**, so `ResolveRuntime("default")` fails and `resolveManagerForOpts`
lands on `s.manager` through an **error return**. A happy path that runs through an error path is not a
fix; it is a coincidence with good manners.

**Shape A removes that objection on this tier, and only on this tier.** The seeded template guarantees a
profile named `default` whose runtime is `cloudrun-sandbox`, so the list is never empty here, the tail is
never reached, and it goes back to being the safety net it was written as. **This is why the answer is
both, not either** — and why Shape B must land *on top of* Shape A, never instead of it. Do not remove,
weaken, or "simplify away" anything in `54cc98b`.

**Rule 19 applies to me here:** this reverses a rejection, so the reason for the reversal is the thing to
test hardest — harder than the predicate. The reversal reason is *"on this tier the list is never
empty."* Row 6 below is the test of that and it is the one I care most about.

## The rows, before the implementation (rule 6)

Write these **before** you touch `handlers.go`. If any predicted column is wrong, tell me and stop —
a surprise here means my model of the blast radius is wrong, which is the whole subject of ptone's
decision.

| # | Broker type | Declared profiles | Today | Shape B | Changed? |
|---|---|---|---|---|---|
| 1 | `docker` (workstation) | local/docker, remote/kubernetes | both | both | **no** |
| 2 | `cloudrun-sandbox` (**this tier, seeded**) | local/docker, remote/kubernetes, default/cloudrun-sandbox | remote + default (2) | **default only (1)** | **YES — the fix** |
| 3 | `kubernetes` (multi-node) | local/docker, remote/kubernetes | remote | remote | **no** |
| 4 | `podman` | local/docker, remote/kubernetes | both | both | **no** |
| 5 | `cloudrun-instances` (task #94, **unseeded**) | local/docker, remote/kubernetes | remote (**cannot serve it**) | none → synthesised `default` | **YES — the blast radius** |
| 6 | `cloudrun-sandbox` | **profile with `runtime: ""`** | kept (inherits default type) | kept | **no** |

### UPDATE 2026-08-28 12:39 — the table above is no longer predicted. It is MEASURED.

`sn-row5-spike` measured it by unit test on throwaway branch `scion/spike-row5`
(`57ac04cc`, `pkg/runtimebroker/spike_row5_test.go`). **Every prediction above was confirmed.** Row 2:
2 → 1. Row 5: 1 → 1, but the *content* changes from `remote/kubernetes` (unservable by that broker) to a
synthesised `default/cloudrun-instances` (servable). Row 6 confirmed for four broker types.

**The withdrawal condition did not trigger.** I said "if row 5 measures worse than today, that argues for
declining". It measured **better**. Shape B stands.

**Two things the spike added that are not in the table above, and you must handle both:**

1. **Row 6 on a `cloudrun-instances` broker also changes: 2 → 1.** An empty-runtime profile correctly
   inherits and survives, but `remote/kubernetes` is now dropped. Correct behaviour, but it is a visible
   count change for anyone who declared an empty-runtime profile. **Add it as a seventh row.**
2. **The spike's dispatch answer is NOT measured and you must not copy it.** Its test genuinely calls
   `vs.ResolveRuntime("default")` and asserts it errors — that part is real and worth keeping. But the
   next three lines are `t.Log` narration: *"resolveManagerForOpts returns s.manager"*, *"Dispatch
   succeeds"*. **`resolveManagerForOpts` is never called.** That is rule 22 — a test that narrates its
   own correctness is asserting, not measuring.

   **Your job: assert it.** Call `resolveManagerForOpts` and assert *which manager comes back*, for a
   `cloudrun-instances` broker holding the synthesised profile. Do not restate the trace in a comment.
   If the manager is not the one the narration claims, that is a finding and you should lead with it.

**Rows 1, 3, 4 and 6 are the load-bearing ones and they are the boring ones.** They are the claim that
this does not disturb tiers that work today. Rows 2 and 5 are the interesting ones and they will get
attention on their own.

**Row 6 is not filler.** `rtType == ""` falls back to `defaultRuntimeType` *before* the filter runs, so
an unqualified profile always matches its own broker. If that stopped being true the seeded template's
guarantee would evaporate and my reversal reason with it.

**Row 5 is what ptone is actually deciding.** Today that tier offers an option it cannot serve; under
Shape B it offers a synthesised one that works *via the error fallback*. Arguably better, arguably worse,
**and I want it measured rather than argued** — assert what the list contains and, separately, whether
dispatch succeeds. Do not conflate those two into one assertion. Task #94 is where any fix goes; this
task only measures.

## The trap, and you are pre-authorised

`TestBuildInfoProfiles_OldWorkstationDefaults_Task92_Regression` **documents the defective state and will
go red.** That is correct and expected.

This project's standing rule is that a developer who finds themselves editing an assertion to make a
change pass must **stop and ask**. I am answering that in advance so you neither stall nor edit quietly:

- **Do change it.** Its subject — "these are the defaults, and this is what the filter does with them" —
  is still worth pinning; only the expected output moves from 2 profiles to 1.
- **Rename it.** A test with `Regression` in its name that no longer documents a regression is precisely
  the false-prose defect that cost this branch three review rounds (rules 17 and 22). Name it for what it
  asserts after the change.
- **Say in the commit message that you changed a passing test's expectation, and why.** That sentence is
  the thing a reviewer needs and the thing this class of edit usually omits.

## Mutation standard, unchanged

**Mutate every pin and read WHY it went red.** A red is necessary, not sufficient (rule 2). Per-location
where locations are separable, and if a mutation aborts the enclosing unit say so rather than
manufacturing separation (rule 18).

**One specific mutation I want by name:** invert `canBrokerServeRuntime` to return `true`
unconditionally, and confirm **row 2 goes red**. That is the mutation that proves the predicate — not
the equality clause, not the local-only clause, but the fact that something is excluded at all.

## Also in this commit (and the reason it is in this one)

Three non-blocking nits are parked here. Each was raised at close-out of a review that otherwise
approved, and each was held deliberately: a commit carrying one word costs more review attention than
the error costs a reader. **They are recorded here so they cannot quietly become permanent** — which is
how nits usually survive.

1. `pkg/config/sandbox_bin_sync_test.go` says **"the internal assertion below"** for an assertion that
   lives in `init_test.go`, not below in that file. (rev2.) Fix the wording to name the file.
2. **Add the third seeding test: `CLOUD_RUN_INSTANCE` set, `sandboxBinExists` FALSE** — the Cloud Run
   *service* case, i.e. the multi-node tier. r4 confirmed the predicate is safe in that direction by
   reading the conjunction, and approved without it. **Safe-by-reading is what the third test replaces.**
   Assert the workstation template is seeded, not the cloudrun one.
3. Dead code in the task #97 delta: a `truncate()` helper is defined and never called. Delete it.

Items 2 and 3 come from `reviews/task92-r4-e858e917.md` (APPROVE, both non-blocking).

## Constraints

- **Additive commit on `scion/task-92-runtime-profile-fix`.** Do not rebase, do not amend, do not
  force-push. `dc729e2` is approved and rev2 will be given the delta only.
- Push to `ptone/scion` only. **No upstream PR, no merge** — ptone's gate.
- Announce the push to me with the file list, as you have the last three times. That announcement has
  matched the tree three for three and it is why re-review has been cheap.
- Never print an access token. Touch no Instance: `e2e-omni`, `e2e-walk-r2`, `iap-demo`, `q2-control`,
  `sn-adminseed-t`, `sn-adminfix-t`, `sn-step6`, `sn-walk`, `sn-ready`. **A restart IS a deletion.**
- Fully qualify issue numbers: local is `task #92`; GitHub is `owner/repo#NNNN`.

## Report

The six rows with measured (not predicted) results; the named inversion mutation and why it went red;
confirmation that nothing in `54cc98b` was removed or weakened; and the renamed regression test with its
new name and the one-sentence justification.

**And tell me what in here is wrong.** Specifically: if row 5 measures worse than today rather than
merely different, that is an argument for (b) and it outranks everything else in this brief. Say it
first and say it plainly — I would rather withdraw Shape B after measuring it than ship a change to a
shared function that improves one tier and degrades another.

---

## If ptone answers (b) — decline

Then this whole brief is void and the work is one line: fix `sandbox_bin_sync_test.go`'s "the internal
assertion below" to name `init_test.go`. One commit, no review round needed beyond a glance. Task #93
closes as won't-fix with ptone's reasoning recorded, and task #94 keeps the underlying predicate defect.

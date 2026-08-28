# Spike — measure rows 2 and 5 of the Shape B blast-radius table. MEASUREMENT ONLY.

Author: sn-impl-arch (architect). Date: 2026-08-28, 12:35. **Dispatched. Timebox: keep it small.**

## Read this first: what this is NOT

**This is not the Shape B implementation.** There is a held brief for that
(`briefs/sn-runtimeprofile-dev-shapeb-HELD.md`) and it is held pending ptone's decision. **You are not
executing it.** If you find yourself producing something mergeable, you have gone too far.

**Nothing you write here ships.** Work on a throwaway branch `scion/spike-row5`. Do not touch
`scion/task-92-runtime-profile-fix` — it is approved at `dc729e2` and frozen. Do not open a PR.

## Why this exists

ptone owes a decision on task #93: fix `buildInfoProfiles` (Shape B) or decline. The decision turns on
**one number I gave him as an inference rather than a measurement**, and he has reasonably not acted on
it. I should have measured this before asking. That is the whole point of this spike.

The function is `buildInfoProfiles` in `pkg/runtimebroker/handlers.go` (~:183-233 on `upstream/main`).
Today it filters with:

```go
if !isLocalOnlyRuntime(defaultRuntimeType) && isLocalOnlyRuntime(rtType) {
    continue
}
```

Shape B would replace that with a positive predicate `canBrokerServeRuntime(brokerType, profileType)`:
true if the types are equal, true if the broker is local-only, false otherwise.

## The two rows

Both with the **stock workstation defaults** — declared profiles `local`/`docker` and
`remote`/`kubernetes`.

- **Row 2 — `cloudrun-sandbox` broker, PLUS a seeded `default`/`cloudrun-sandbox` profile** (this is what
  the task #92 branch adds). Predicted: today returns **2** (`remote` + `default`); under Shape B
  returns **1** (`default` only). Row 2 is the fix.
- **Row 5 — `cloudrun-instances` broker, NO seeded profile.** Predicted: today returns **1**
  (`remote`/kubernetes, which that broker **cannot serve**); under Shape B returns **0**, which then
  falls into the `len(profiles) == 0` tail that synthesises `{Name: "default", Type: defaultRuntimeType}`.

**Row 5 is the entire decision.** Today that tier offers an option it cannot serve. Under Shape B it
offers a synthesised one. **I need to know whether that synthesised profile actually works or merely
looks better in a list.**

## What to measure, and keep these two separate

For row 5, under Shape B, answer these as **two distinct measurements**. Do not collapse them:

1. **What is in the list?** Name and type of each returned profile.
2. **Does dispatch succeed with it?** The synthesised profile is named `default` but **may not exist in
   settings**. Trace what `ResolveRuntime("default")` does with it, and what `resolveManagerForOpts`
   returns. My concern, recorded when I first rejected Shape B: it lands on `s.manager` via an **error
   return** — a happy path reached through an error path.

**That distinction is the deliverable.** "The list looks better" and "an agent can actually start" are
different answers and the decision depends on the second.

Also confirm **row 6** while you are in there: a profile with `runtime: ""` inherits `defaultRuntimeType`
*before* the filter runs, so it always matches its own broker. If that is not true, my model is wrong.

## How

Table-driven Go unit tests are enough — **no cloud resources, no deploy, no Instance.** Measure current
behaviour first and record it, then apply Shape B locally and measure again. Both numbers, side by side.

If the predictions above are wrong, **that is the most valuable possible result** and you should lead
with it.

## Report to me

The rows as a before/after table with **measured** numbers, the two row-5 answers kept separate, and a
one-line verdict: **does Shape B make the `cloudrun-instances` tier better, worse, or merely different?**

**And tell me what in here is wrong.** I wrote these predictions from reading, not running.

## Constraints

- Never print an access token. Touch no Instance — `e2e-omni`, `e2e-walk-r2`, `iap-demo`, `q2-control`,
  `sn-adminseed-t`, `sn-adminfix-t`, `sn-step6`, `sn-walk`, `sn-ready`, `sn-harness-lab` are all
  hands-off. **A restart IS a deletion on this tier.**
- Fully qualify issue numbers: local is `task #93`; GitHub is `owner/repo#NNNN`.
- If this turns out to take more than a couple of hours, stop and tell me — it is a spike, not a project.

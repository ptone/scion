# Criterion 24: Phase 8 Reports 16, and Phase 6 Gets the Row That Receives the Other Five

**Branch:** scion/sp-dev5
**Date:** 2026-07-29
**Agent:** sp-dev5
**Instructed by:** sp-em (22:00Z)

## Summary

Criterion 24 of the settings-precedence merge gate attributed 21 added tests to Phase 8. Five of
those are `sp-dev3`'s Phase 6 (Gap 2a), not Phase 8 (Gap 2c). This entry lands the correction **(a)**
together with the new Phase 6 row **(b)** that receives the five tests the correction removes.

**(a) and (b) ship together deliberately.** Landing (a) alone is strictly worse than landing neither:
before the correction the five tests were miscounted into Phase 8's battery and therefore *run*;
after it they belong to a phase with no row, and a criterion that names no owner runs them nowhere.

> **A CORRECTION THAT REMOVES ITEMS FROM A GATE MUST SHIP WITH THE GATE THAT RECEIVES THEM, OR THE
> CORRECTION IS A DELETION.**

Found by `sp-dev3` against my own correction; upheld by `sp-em`. No production code changes.

---

## (a) Phase 8 added 16, on a derived base

The published figure of 21 came from `ff835b0b..0de59f5c`. `ff835b0b` *is* a genuine ancestor of my
branch base, so nothing was malformed — the range simply began one phase too early.

```
git diff ff835b0b 0de59f5c -- '*_test.go' | grep -cE '^\+func Test'   ->  21   superseded
git diff 5b0fb1c5 0de59f5c -- '*_test.go' | grep -cE '^\+func Test'   ->  16   PHASE 8

  pkg/agent 5 · pkg/config 2 · pkg/hub 5 · pkg/runtimebroker 4  = 16
```

Reconciliation: `21 = Phase 8's 16 + Phase 6's 5`.

**Derive the base, never quote it:**

```
BASE=$(git merge-base a13ff174 <phase-8 parent lineage>)      # = 5b0fb1c5
```

> **A DERIVATION IS ONLY AS GOOD AS ITS BASE. SUBSTITUTING A DERIVATION FOR A COUNT MOVES THE
> UNPROVENANCED VALUE FROM THE ANSWER INTO THE ARGUMENT — IT DOES NOT REMOVE IT**, and it makes it
> harder to see, because a derivation carries an air of rigour that discourages asking where the base
> came from. The output had no ragged edge: 21 well-formed names, every one genuinely added in the
> range, every one genuinely present downstream.

Range control — assert every file in the range was *introduced* within the range:

```
git log --diff-filter=A -1 --format='%h %s' <base> -- <file>
  pkg/hub/hub_agent_defaults_test.go       introduced by 5b0fb1c5  "Gap 2a, Phase 6"  sp-dev3
  pkg/hub/hub_agent_defaults_wire_test.go  introduced by a13ff174  "Gap 2c, Phase 8"  sp-dev5  <- control
```

**Why it matters beyond the count:** if `TestHubAgentDefaults_ConcurrentWithApplySnapshot` goes red
under integration, the uncorrected criterion sends the investigation to Phase 8. It is `sp-dev3`'s
concurrency test over `sp-dev3`'s snapshot code. **A mis-based range does not just miscount — it
misattributes, and misattribution is paid at the worst possible moment.**

---

## (b) New row — Phase 6 (Gap 2a)

`pkg/hub/hub_agent_defaults_test.go`, introduced by `5b0fb1c5`, `sp-dev3`. Five tests, **exhaustive
for the file**:

```
go test ./pkg/hub -count=1 -run '^(TestApplySnapshot_AgentDefaults_PopulatesServerConfig|\
TestApplySnapshot_AgentDefaults_ClearedWhenSnapshotEmpty|\
TestApplySnapshot_FileMode_AgentDefaultsStayZero|\
TestHubAgentDefaults_ReturnsDeepCopy|\
TestHubAgentDefaults_ConcurrentWithApplySnapshot)$'
```

**Exhaustive, not merely correct.** `sp-dev3` enumerated every `func Test` in the file and got these
five and nothing else; reproduced here independently (`grep -cE '^func Test'` → 5, names identical).
That is the question a list cannot answer about itself, and it is the one that matters when the
complaint is that a correction orphans what it removes — a five-name row over a six-test file
re-opens the hole one test narrower.

### Evidence

Selector self-check (`-list`), **run before the tests**:

```
$ go test ./pkg/hub -list '^(...five...)$'
TestApplySnapshot_AgentDefaults_PopulatesServerConfig
TestHubAgentDefaults_ReturnsDeepCopy
TestApplySnapshot_AgentDefaults_ClearedWhenSnapshotEmpty
TestApplySnapshot_FileMode_AgentDefaultsStayZero
TestHubAgentDefaults_ConcurrentWithApplySnapshot
ok      github.com/GoogleCloudPlatform/scion/pkg/hub    0.275s
$ ... | grep -c '^Test'    ->  5      MUST BE 5
```

Execution:

```
$ go test ./pkg/hub -count=1 -v -run '^(...five...)$'
--- PASS: TestApplySnapshot_AgentDefaults_PopulatesServerConfig (0.00s)
--- PASS: TestHubAgentDefaults_ReturnsDeepCopy (0.00s)
--- PASS: TestApplySnapshot_AgentDefaults_ClearedWhenSnapshotEmpty (0.00s)
--- PASS: TestApplySnapshot_FileMode_AgentDefaultsStayZero (0.00s)
--- PASS: TestHubAgentDefaults_ConcurrentWithApplySnapshot (0.00s)
PASS
ok      github.com/GoogleCloudPlatform/scion/pkg/hub    0.189s

=== RUN lines: 5    --- PASS lines: 5    --- FAIL: 0    rc=0
```

---

## The selector self-check, and why it earns its 0.2s

```
go test ./pkg/hub -list '<same anchored alternation>' | grep -c '^Test'      # MUST BE EXACTLY 5
```

`-list` compiles the test binary and asks **its own matcher** which tests the pattern selects,
running none of them. The validator *is* the runtime matcher, so the criterion's regex cannot drift
from `go test` semantics the way a `grep` re-implementation silently can. A renamed or deleted test
makes the list **short, loudly, by construction**. And because it must compile first, it also proves
the file builds.

> **A TEST SELECTOR CAN BE VALIDATED WITHOUT RUNNING THE TESTS, AND SHOULD BE, BECAUSE A SELECTOR
> THAT SELECTS NOTHING IS INDISTINGUISHABLE FROM A SELECTOR THAT PASSES** — a zero-selection run
> prints `ok` and exits 0.

### Anchor the alternation — measured, not asserted

```
go test ./pkg/hub -list 'TestHubAgentDefaults'   -> 2   ReturnsDeepCopy, ConcurrentWithApplySnapshot
go test ./pkg/hub -list '^(all five)$'           -> 5
```

The natural bare prefix selects **two of the five** and reports success, silently dropping all three
`ApplySnapshot` tests. The two it happens to catch are the two flagged as most integration-sensitive
— luck, not design, and luck that would have read as a green.

**Precision, because this was nearly overclaimed:** the *unanchored full alternation* (same five
names, no `^(...)$`) also returns 5 today — no other `pkg/hub` test currently contains any of the
five as a substring. On this row the anchor is **insurance against a future name**, not a live fix;
the live defect is the bare prefix.

> **THE FAILURE A CONTROL ACTUALLY PREVENTS TODAY AND THE FAILURE IT IS INSURANCE AGAINST ARE
> DIFFERENT CLAIMS, AND CONFLATING THEM IS HOW A CARGO-CULTED GUARD OUTLIVES ITS REASON.**

Anchor anyway — it is free.

---

## Refinement (b) of criterion 24 — name the subtest — is satisfied, not skipped

Criterion 24(b) requires naming subtests where a parent name alone would not discriminate. Measured:
this file contains **zero** `t.Run` calls, so the five top-level names *are* the finest granularity
available and the row is compliant rather than silently coarse.

Non-vacuous — the same search fires elsewhere: `pkg/hub/auth_test.go` 12, `pkg/hub/clone_url_test.go` 2.

> **A ZERO FROM A SEARCH IS ONLY A FACT ONCE THE SEARCH HAS BEEN SHOWN TO FIRE SOMEWHERE.**

---

## Running rules

- `-count=1`.
- **Never `-race` on `pkg/hub`** — the whole-package race build hangs indefinitely.
- Gate any pass claim on positive `=== RUN` / `--- PASS` lines in the same invocation, never on `ok`
  alone.
- **Disk, scoped:** ENOSPC makes `TempDir` fail, which is `t.Fatal`, which is a **red**. It cannot
  manufacture a false **green**. A green taken under disk pressure is still a green; it is the reds
  that need `df` bracketing. (Recorded here: `df` was 99% before and after this run, unchanged, and
  the run was green.) **An environmental fault that can only push a result one way needs a control
  only on that side.**

## Scope

Documentation and gate only. No production code, no test code, no harness files changed. The five
tests were already present and passing; what changed is which phase's battery is responsible for
running them.

# Substrate Phase 1 — D1/D2 live-cluster fixes: delete leaks the actor, second profile's egress_allow ignored

Two defects found on the live cluster, both with root causes confirmed from code and broker logs before this task started. Both fixes are scoped to substrate-specific code (D1) or a one-line, substrate-scoped condition in shared broker code (D2); neither changes any other runtime's behavior.

## D1: `scion delete` never deleted the Substrate actor

**Symptom:** the broker logged "Agent deleted" and returned success for every test agent, but the actor was left running on the cluster.

**Root cause:** `AgentManager.Delete` (`pkg/agent/manager.go`, unchanged by this fix) resolves a caller-supplied agent ID to a container by calling `Runtime.List` and matching the result's `AgentInfo.Name` against the ID. `SubstrateRuntime.List` was setting `Name` to the actor's Substrate resource name — `containerName(project, agent)` = `"<project>--<agent>"` (`pkg/agent/run.go`) — not the bare agent slug the caller (and the `"scion.name"` label) uses. The match never succeeded, so `Delete` silently skipped `Runtime.Stop`/`Runtime.Delete` and returned success anyway. `DockerRuntime.List` already gets this right: it reports `labels["scion.name"]`, falling back to the raw container name only when that label is absent.

**Fix** (`pkg/runtime/substrate_runtime.go`, `List`): `AgentInfo.Name` is now `labels["scion.name"]` after the label merge, matching Docker's convention:

- When an in-memory agent record exists, `rec.Labels["scion.name"]` (set from the real agent slug at `Run`) already overwrites the synthesized default during the merge, so it's used directly.
- When no record exists (e.g. right after a broker restart), a new helper, `substrateSynthesizedAgentName`, derives a best-effort slug by inverting `containerName`: split the actor name on the last `"--"` and slugify the remainder. This is documented as ambiguous only if a project name itself contains `"--"`, which sanitized project names don't in practice.

`Delete`/`Stop`/`Exec`/`GetLogs`/`Attach` are unaffected — they all take `ContainerID` (`<atespace>/<actor>`), never `Name`, to identify the actor. Confirmed by reading each method; none of them changed.

`pkg/agent/manager.go` is unchanged, per the fix's scope. `ptone/scion#1819` tracks hardening `AgentManager.Delete`'s silent-no-op shape in general; this is the substrate-specific root cause underneath the symptom reported on the live cluster.

Also exported `newSubstrateRuntimeForTest` as `NewSubstrateRuntimeForTest` (`pkg/runtime`), so tests in other packages can drive a real `*SubstrateRuntime` through a fake ateapi client instead of reimplementing its `List`/`Run`/`Delete` logic in a mock — needed for the test below.

### Tests

`TestSubstrateList_NameIsAgentSlugNotActorName` (`pkg/runtime/substrate_runtime_test.go`) covers `List`'s `Name` field directly, both with and without an in-memory record.

`TestSubstrateAgentManagerDelete_RecordExists` / `_NoRecord` (`pkg/agent/substrate_delete_test.go`, new file) drive the real path end to end: a real `*SubstrateRuntime` (via `NewSubstrateRuntimeForTest` and a package-local fake `ateapipb.ControlClient` + fake actor HTTP server) wrapped in a real `agent.NewManager`, then `.Delete(ctx, "<slug>", ...)`, asserting the fake client actually received `DeleteActor` and `DeleteActorEgressPolicy` for the right atespace/actor. The record-exists case starts the agent for real through `Run`; the no-record case injects a synthetic actor directly into the fake client, so no in-memory record is ever created for it (simulating a broker restart without needing to touch `pkg/runtime`'s unexported process-wide state from another package).

**Fail-before** (production code at `0fcc746a7`, both new tests):
```
=== RUN   TestSubstrateList_NameIsAgentSlugNotActorName/record_exists
    substrate_runtime_test.go:1080: Name = "myproj--sb-smoke-2", want the agent slug "sb-smoke-2" (not the actor name "myproj--sb-smoke-2")
=== RUN   TestSubstrateList_NameIsAgentSlugNotActorName/no_record_(simulated_broker_restart)
    substrate_runtime_test.go:1105: List() with scion.name="sb-smoke-2" filter = [], want exactly the one matching actor
--- FAIL: TestSubstrateList_NameIsAgentSlugNotActorName (0.00s)

=== RUN   TestSubstrateAgentManagerDelete_RecordExists
    substrate_delete_test.go:211: DeleteActor was never called — Delete() silently no-opped instead of finding the actor
--- FAIL: TestSubstrateAgentManagerDelete_RecordExists (0.00s)
=== RUN   TestSubstrateAgentManagerDelete_NoRecord
    substrate_delete_test.go:259: DeleteActor was never called — Delete() silently no-opped instead of finding the record-less actor
--- FAIL: TestSubstrateAgentManagerDelete_NoRecord (0.00s)
```

**Pass-after:**
```
--- PASS: TestSubstrateList_NameIsAgentSlugNotActorName (0.00s)
    --- PASS: TestSubstrateList_NameIsAgentSlugNotActorName/record_exists (0.00s)
    --- PASS: TestSubstrateList_NameIsAgentSlugNotActorName/no_record_(simulated_broker_restart) (0.00s)

--- PASS: TestSubstrateAgentManagerDelete_RecordExists (0.00s)
--- PASS: TestSubstrateAgentManagerDelete_NoRecord (0.00s)
```

Commit: `9fc4cb110`.

## D2: a second substrate profile's `egress_allow` was ignored

**Symptom:** with runtimes `substrate-prod` (`egress_allow: []`) and `substrate-nip` (`egress_allow: ["*.nip.io"]`), starting an agent with `--profile substrate-nip` produced an `EgressPolicy` containing only the base (hardcoded model/hub/git) patterns — the nip entry never made it in.

**Root cause:** `resolveManagerForOpts` (`pkg/runtimebroker/handlers.go`) resolves the requested profile's runtime *type* string and short-circuits to the default manager whenever that type equals the default runtime's `Name()`. For every other runtime type this is correct — a broker only ever has one docker/k8s/cloudrun config — but every substrate profile shares the type string `"substrate"` regardless of its own `V1SubstrateConfig`, so this always returned the manager bound to whichever config the *default* profile used, silently discarding the requested profile's own config. `ptone/scion#1818` tracks this type-vs-instance short-circuit in general; this is the substrate-specific manifestation.

**Fix:** substrate never takes the type-match shortcut — full stop, no config-equality comparison either (a comparison could itself drift out of sync with whatever actually determines a distinct instance). It always falls through to the existing "different type" branch, which calls `agent.ResolveRuntime` → `NewSubstrateRuntime`, already correctly memoized per full `V1SubstrateConfig`. One line changed in the existing condition (`runtimeType == s.runtime.Name() && runtimeType != "substrate"`), plus a comment; every other runtime type's behavior through this function is unchanged, since the added clause only ever evaluates differently when `runtimeType == "substrate"`.

A consequence, confirmed intentional: the *default* substrate profile no longer necessarily gets `s.manager` back either — it now goes through the same `agent.ResolveRuntime` path as any other profile, which (per the memoization) resolves to the same underlying `*SubstrateRuntime` instance, just wrapped in a fresh `agent.NewManager`. `resolveAgentRuntimeTarget` needed no fix for this: it already tries every manager (the default, then every auxiliary one) rather than picking by type, and per-agent state (`substrateAgentRecords`) plus the backing `ListActors` call are both process-/cluster-wide, not scoped to which manager instance issued a given `Run` — confirmed by test, not just by reading the code.

Also exported `SetSubstrateRuntimeBuilderForTest` (`pkg/runtime`) so a test in another package can swap the process-wide constructor `NewSubstrateRuntime` uses, exercising the real per-config memoization through a fake ateapi client instead of dialing real infrastructure — the same seam `pkg/runtime`'s own tests already use internally via the unexported `substrateRuntimeBuilder` var.

### Test

`TestResolveManagerForOpts_SubstrateProfilesGetTheirOwnConfig` (`pkg/runtimebroker/substrate_manager_test.go`, new file) builds one `*Server` with two substrate runtimes/profiles in its settings, matching the live shape (`substrate` → `substrate-prod`, `egress_allow: []`; `substrate-nip` → `substrate-nip`, `egress_allow: ["*.nip.io"]`), backed by **one shared** fake `ateapipb.ControlClient` across both configs — deliberately, since the real bug was found on a cluster where both profiles point at the same `api_endpoint`, and a test with two isolated fake "clusters" would validate `resolveAgentRuntimeTarget`'s cross-profile lookup for the wrong reason. It starts one agent through each profile's resolved manager (calling `.Runtime.Run` directly on the concrete `*agent.AgentManager`, bypassing the full harness/workspace provisioning `Start` would otherwise need), and asserts:

- the prod agent's `CreateActorEgressPolicy` patterns never contain `*.nip.io`;
- the nip agent's patterns do;
- `resolveAgentRuntimeTarget`, `List`, and `Delete` all still succeed for **both** agents afterward (confirms lookup/delete/exec keep working once the default profile no longer always gets `s.manager` back).

`TestResolveManagerForOpts_NonSubstrateBehaviorUnchanged` confirms a same-type non-substrate profile (`docker`) still returns the default manager exactly as before — the existing `TestResolveManagerForOpts_ProfileWithSameRuntime` (`handlers_test.go`) already covers this against a mock-named default; this one exercises it against a real type name, for direct contrast with the substrate case.

**Fail-before** (production code at `9fc4cb110`, i.e. with D1 but not D2):
```
=== RUN   TestResolveManagerForOpts_SubstrateProfilesGetTheirOwnConfig
    substrate_manager_test.go:312: resolveManagerForOpts(profile=substrate-nip) returned the default manager — the bug this test targets
--- FAIL: TestResolveManagerForOpts_SubstrateProfilesGetTheirOwnConfig (0.00s)
=== RUN   TestResolveManagerForOpts_NonSubstrateBehaviorUnchanged
--- PASS: TestResolveManagerForOpts_NonSubstrateBehaviorUnchanged (0.00s)
```
(The second test passes either way — it's the non-substrate parity check, not the regression test.)

**Pass-after:**
```
--- PASS: TestResolveManagerForOpts_SubstrateProfilesGetTheirOwnConfig (0.01s)
--- PASS: TestResolveManagerForOpts_NonSubstrateBehaviorUnchanged (0.00s)
```

Commit: `0a01a1d76`.

## Gate results (final head `0a01a1d76`, `SCION_*` unset)

- `go build ./...` — pass.
- `go vet` on `pkg/config/...`, `pkg/runtime/...`, `pkg/runtimebroker/...`, `pkg/agent/...`, `pkg/sciontool/...`, `cmd/sciontool/...` — pass, no output.
- `gofmt -l` on every changed file — clean.
- `go test -count=1` on the same package trees — all pass except one pre-existing, unrelated failure: `TestNativeTelemetryPolicyEffectiveChildEnv/disabled` (`pkg/sciontool/supervisor`), confirmed identical on the unmodified base commit (`0fcc746a7`) via a detached `git worktree` — not touched by this task, already flagged as a known failure in the brief.
- `go test -race -count=1` on `pkg/runtime` and `pkg/runtimebroker` — pass, no races.
- `GOGC=40 golangci-lint run --new-from-rev=c3b6e821d --concurrency=1 ./...` — 0 issues.

`make check-custom`'s compat-literals check on `shared_dir_storage_test.go` (the brief's other named known-pre-existing failure) was not run — out of scope for these packages and unrelated to either fix.

## Functions touched (for the reviewer's call-path check)

- `pkg/runtime/substrate_runtime.go`: `SubstrateRuntime.List` (the `Name`/`labels["scion.name"]` change), new `substrateSynthesizedAgentName`, `NewSubstrateRuntimeForTest` (renamed from `newSubstrateRuntimeForTest`), new `SetSubstrateRuntimeBuilderForTest`.
- `pkg/runtime/substrate_runtime_test.go`: call-site updates for the rename; new `TestSubstrateList_NameIsAgentSlugNotActorName`.
- `pkg/agent/substrate_delete_test.go` (new file): `TestSubstrateAgentManagerDelete_RecordExists`, `TestSubstrateAgentManagerDelete_NoRecord`, and their local fake `ateapipb.ControlClient`/HTTP server support.
- `pkg/runtimebroker/handlers.go`: `resolveManagerForOpts` (the one-line condition change).
- `pkg/runtimebroker/substrate_manager_test.go` (new file): `TestResolveManagerForOpts_SubstrateProfilesGetTheirOwnConfig`, `TestResolveManagerForOpts_NonSubstrateBehaviorUnchanged`, and their local fake support.

No changes to `pkg/agent/manager.go`, `pkg/runtimebroker/handlers.go`'s `resolveAgentRuntimeTarget`, or any other runtime's code path. No `go.mod`/`go.sum` changes.

---

## Follow-up: C1 — the record-less path must fail closed

Review found a critical gap in the D1 fix above: a record-less `List` entry carried no project identity at all, only a synthesized `"scion.name"`. Two different projects' actors that happened to produce the same synthesized slug (e.g. agent `dev` in project A and agent `dev` in project B, both started before a broker restart wiped their in-memory records) were indistinguishable to any caller that matches by slug — reproduced: an unscoped `Delete("dev")` deleted whichever of the two `ListActors` happened to return, non-deterministically. The pre-D1 code failed closed here (a no-op); the D1 fix as shipped failed open (a wrong-actor delete). The invariant this closes: **a slug lookup must never resolve to an actor whose ownership can't be verified.** A no-op is acceptable; a wrong-actor delete is not.

### Fix

All in `pkg/runtime/substrate_runtime.go`.

**`substrateSynthesizedAgentName`** now returns `(projectPrefix, agentSlug, ok)`, and `ok` is true only when the inversion of `containerName(projectSlug, agentSlug) = "<projectSlug>--<agentSlug>"` is unambiguous:

- the actor name contains **exactly one** `"--"` (zero means no project prefix was applied; two or more means the split point can't be determined — `"a--b--c"` could be project `"a"` agent `"b--c"`, or project `"a--b"` agent `"c"`, and the string alone can't say which);
- both halves are non-empty;
- the agent half already equals its own slug (`api.Slugify(agentHalf) == agentHalf`) — a real agent slug always does (it's recorded as `api.Slugify(opts.Name)`); a raw, unslugified agent name that itself contained `"--"` (e.g. literally naming an agent `"b--c"`, producing actor name `"a--b--c"`) would not, which is exactly the case that would otherwise be misread as project `"a"` agent `"b--c"`.

When `ok` is false, `List` falls back to the actor name itself for `"scion.name"` — the pre-D1 behaviour — which a real agent slug can never equal (it always contains `containerName`'s `"--"` separator, which `api.Slugify` never emits), so a caller-supplied slug lookup simply never matches that entry, rather than matching it by coincidence.

**`List`** now runs two passes. The first tallies, across every actor in the listing, what `"scion.name"` value each would report — the real one from its record if it has one, or its candidate synthesized slug if not. The second pass only trusts a record-less actor's candidate slug if it is the **sole** actor (record-having or not) that would produce that value; otherwise that actor falls back to its full actor name. This closes the case a per-actor-only check can't: a record-**having** actor's real, independently-verified slug colliding with an unrelated record-less actor's candidate must not blank out the real one, and must not let the record-less one borrow it either.

For a record-less actor whose candidate slug **is** trusted, `List` also sets `Project` (and the `"scion.project"`/`"scion.grove"` labels) to the recovered project-slug prefix — a best-effort project **name**, not an ID; it is only ever compared against a project-name-shaped filter or field (e.g. `pkg/agent`'s own `matchAgentProject`, unchanged).

**`substrateLabelsMatch`** gained an `atespace` and `hasRecord` parameter, used for exactly one case: a `scion.project_id`/`scion.grove_id` filter against a record-less entry. The atespace is the authoritative project identity here — every actor lives in the atespace its project ID produces (`substrateAtespaceName`), independent of any name recovered from the actor's own name — so that filter is answered by recomputing `substrateAtespaceName(filterValue)` and comparing it to the actor's actual atespace, not by comparing to a stored label (there isn't a trustworthy one: the recovered prefix is a project *slug*, and a project ID's atespace is a truncated, sanitized hash of the ID, not the slug — the two live in different namespaces and can't be compared to each other directly). A record-having entry needs none of this; its real `ProjectID` is compared exactly as before.

### Tests

`pkg/runtime/substrate_runtime_test.go`: `TestSubstrateSynthesizedAgentName` (table test: no separator, exactly one, two or more, mixed case, a non-slug agent half, empty halves, an over-63-character agent half); `TestSubstrateList_RecordlessAmbiguousSlugFallsBackToActorName` (two record-less actors in different projects, same candidate slug — run under both `ListActors` orderings — neither is reported under the colliding slug); `TestSubstrateList_RecordlessProjectScopedLookupDiscriminates` (a `scion.project_id`-scoped `List` call finds only the actor in that project's atespace); `TestSubstrateList_RecordExistsAndRecordlessSameSlug` (a record-having actor's real slug survives a collision with an unrelated record-less actor); `TestSubstrateList_ThreeLevelActorNameNotMisread` (`"a--b--c"` next to `"a--c"`: a lookup for `"c"` resolves only to `"a--c"`).

`pkg/agent/substrate_delete_test.go`: `TestSubstrateAgentManagerDelete_RecordlessAmbiguousSlugDeletesNothing` (the exact reported repro — two projects, both record-less, same slug `"dev"` — run as subtests for both actor orderings, and separately confirmed under `-count=50` for non-order-dependence); `TestSubstrateAgentManagerDelete_RecordExistsAndRecordlessSameSlug`; `TestSubstrateAgentManagerDelete_ThreeLevelActorNameNotMisread`. All drive the real path: a real `*SubstrateRuntime` and a real `agent.AgentManager.Delete`, against a fake `ateapipb.ControlClient`.

**Fail-before** (production code with the guard logic removed — the naive single-return-value synthesis, no cross-actor tally, no atespace check — reproducing the reported shape exactly):
```
substrateSynthesizedAgentName("a--b--c") ok = true, want false
List(scion.name="dev") = [projA--dev, projB--dev], want no matches (ambiguous, neither actor's ownership of "dev" is verified)
List(scion.project_id="aaaaaaaaaaaa") = [], want exactly projA's actor
List(scion.name="c") = [a--b--c, a--c], want exactly "a--c", not "a--b--c"

Delete("dev") called DeleteActor [actor:{atespace:"scion-bbbbbbbbbbbb" name:"projB--dev"} ...], want no calls at all
DeleteActor actor = scion-bbbbbbbbbbbb/projB--dev, want the record-having "projA--dev", not the record-less other-project actor
DeleteActor actor name = "a--b--c", want "a--c" ("a--b--c" must never match a lookup for "c")
```

**Pass-after:** every test above passes; the ambiguous-slug case now deletes nothing and reports both actors present and untouched.

**`-count=50`** on the exact reported repro (`TestSubstrateAgentManagerDelete_RecordlessAmbiguousSlugDeletesNothing`): 50/50 pass, both orderings, no wrong-actor delete in any run.

**`-count=20`** on all new/changed `pkg/runtime` and `pkg/agent` substrate tests: pass, no flakes.

## Follow-up: R2 — the D2 test left a stale fake in the process-wide memo

`SetSubstrateRuntimeBuilderForTest` swapped only the builder function; the `*SubstrateRuntime` instances a test built with it (each bound to an `httptest` server the test's own `t.Cleanup` later closes) stayed cached in `substrateRuntimes`, the process-wide memo `NewSubstrateRuntime` uses. Observed: `go test -count=2 -run TestResolveManagerForOpts_SubstrateProfiles ./pkg/runtimebroker/` failed after a 5-minute hang — the second run's `NewSubstrateRuntime` call for the same config got the first run's now-closed fake server back instead of building a fresh one.

**Fix:** `SetSubstrateRuntimeBuilderForTest` now also swaps `substrateRuntimes` for a fresh, empty map, and restores both the builder and the memo together in the returned func — mirroring `resetSubstrateRuntimeRegistryForTest`, the equivalent helper this package's own tests already use internally, which other packages can't call since it's unexported.

**Proof:** `go test -count=3 -run TestResolveManagerForOpts_Substrate ./pkg/runtimebroker/` — 3/3 pass, no hang (previously failed on the second iteration).

## Follow-up: N1/N2 — remaining internal-process references

- `pkg/runtimebroker/substrate_manager_test.go`: a code comment cited an internal decision-maker instead of the technical reason. Replaced with "a comparison could itself drift out of sync with whatever actually determines a distinct instance" — the actual reason, already stated elsewhere in the same comment, without the citation.
- This project log (the D2 section above): two lines named an internal role. Removed; the surrounding sentences already stated the technical reasoning without needing the attribution.
- `pkg/runtimebroker/handlers.go`: added a short comment at the `ForceRuntime == s.runtime.Name()` early return noting that `ForceRuntime == "substrate"` bypasses the per-profile substrate config resolution below entirely (referencing `ptone/scion#1818`, the general tracking issue) — comment only, no behavior change.

## Gate results (this follow-up)

- `go build ./...` — pass.
- `go vet` on `pkg/runtime/...`, `pkg/runtimebroker/...`, `pkg/agent/...`, `pkg/config/...`, `pkg/sciontool/...`, `cmd/sciontool/...` — pass, no output.
- `gofmt -l` on every changed file — clean.
- `go test -count=1` on the same package trees — all pass except the same pre-existing `TestNativeTelemetryPolicyEffectiveChildEnv/disabled` (`pkg/sciontool/supervisor`).
- `go test -race -count=1` on `pkg/runtime`, `pkg/runtimebroker`, `pkg/agent` — pass, no races.
- `GOGC=40 golangci-lint run --new-from-rev=c3b6e821d --concurrency=1 ./...` — 0 issues.
- `grep -rniE 'round [0-9]|sb-rev|sb-dev|sb-em|substrate-lead|finding #'` over every file changed this round — no hits.

## Functions touched (this follow-up)

- `pkg/runtime/substrate_runtime.go`: `SubstrateRuntime.List` (two-pass ambiguity-safe synthesis, project-prefix labeling), `substrateSynthesizedAgentName` (new signature and rules), `substrateLabelsMatch` (atespace-based project-ID matching for record-less entries), `SetSubstrateRuntimeBuilderForTest` (also swaps the runtime memo).
- `pkg/runtime/substrate_runtime_test.go`: new tests listed above.
- `pkg/agent/substrate_delete_test.go`: new tests listed above.
- `pkg/runtimebroker/handlers.go`: comment only, at the `ForceRuntime` early return.
- `pkg/runtimebroker/substrate_manager_test.go`: comment wording only.

No changes to `pkg/agent/manager.go`, any other runtime, or the broker's generic (non-substrate-scoped) code paths. No `go.mod`/`go.sum` changes.

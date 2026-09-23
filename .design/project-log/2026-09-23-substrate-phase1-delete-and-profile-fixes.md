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

**Fix:** substrate never takes the type-match shortcut — full stop, no config-equality comparison either (substrate-lead's explicit decision: a comparison could itself drift out of sync with whatever actually determines a distinct instance). It always falls through to the existing "different type" branch, which calls `agent.ResolveRuntime` → `NewSubstrateRuntime`, already correctly memoized per full `V1SubstrateConfig`. One line changed in the existing condition (`runtimeType == s.runtime.Name() && runtimeType != "substrate"`), plus a comment; every other runtime type's behavior through this function is unchanged, since the added clause only ever evaluates differently when `runtimeType == "substrate"`.

A consequence, confirmed intentional: the *default* substrate profile no longer necessarily gets `s.manager` back either — it now goes through the same `agent.ResolveRuntime` path as any other profile, which (per the memoization) resolves to the same underlying `*SubstrateRuntime` instance, just wrapped in a fresh `agent.NewManager`. `resolveAgentRuntimeTarget` needed no fix for this: it already tries every manager (the default, then every auxiliary one) rather than picking by type, and per-agent state (`substrateAgentRecords`) plus the backing `ListActors` call are both process-/cluster-wide, not scoped to which manager instance issued a given `Run` — confirmed by test, not just by reading the code.

Also exported `SetSubstrateRuntimeBuilderForTest` (`pkg/runtime`) so a test in another package can swap the process-wide constructor `NewSubstrateRuntime` uses, exercising the real per-config memoization through a fake ateapi client instead of dialing real infrastructure — the same seam `pkg/runtime`'s own tests already use internally via the unexported `substrateRuntimeBuilder` var.

### Test

`TestResolveManagerForOpts_SubstrateProfilesGetTheirOwnConfig` (`pkg/runtimebroker/substrate_manager_test.go`, new file) builds one `*Server` with two substrate runtimes/profiles in its settings, matching the live shape (`substrate` → `substrate-prod`, `egress_allow: []`; `substrate-nip` → `substrate-nip`, `egress_allow: ["*.nip.io"]`), backed by **one shared** fake `ateapipb.ControlClient` across both configs — deliberately, since the real bug was found on a cluster where both profiles point at the same `api_endpoint`, and a test with two isolated fake "clusters" would validate `resolveAgentRuntimeTarget`'s cross-profile lookup for the wrong reason. It starts one agent through each profile's resolved manager (calling `.Runtime.Run` directly on the concrete `*agent.AgentManager`, bypassing the full harness/workspace provisioning `Start` would otherwise need), and asserts:

- the prod agent's `CreateActorEgressPolicy` patterns never contain `*.nip.io`;
- the nip agent's patterns do;
- `resolveAgentRuntimeTarget`, `List`, and `Delete` all still succeed for **both** agents afterward (the substrate-lead-requested check that lookup/delete/exec keep working once the default profile no longer always gets `s.manager` back).

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

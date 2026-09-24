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

## Follow-up: the record-less path must fail closed

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

## Follow-up: the profile-resolution test left a stale fake in the process-wide memo

`SetSubstrateRuntimeBuilderForTest` swapped only the builder function; the `*SubstrateRuntime` instances a test built with it (each bound to an `httptest` server the test's own `t.Cleanup` later closes) stayed cached in `substrateRuntimes`, the process-wide memo `NewSubstrateRuntime` uses. Observed: `go test -count=2 -run TestResolveManagerForOpts_SubstrateProfiles ./pkg/runtimebroker/` failed after a 5-minute hang — the second run's `NewSubstrateRuntime` call for the same config got the first run's now-closed fake server back instead of building a fresh one.

**Fix:** `SetSubstrateRuntimeBuilderForTest` now also swaps `substrateRuntimes` for a fresh, empty map, and restores both the builder and the memo together in the returned func — mirroring `resetSubstrateRuntimeRegistryForTest`, the equivalent helper this package's own tests already use internally, which other packages can't call since it's unexported.

**Proof:** `go test -count=3 -run TestResolveManagerForOpts_Substrate ./pkg/runtimebroker/` — 3/3 pass, no hang (previously failed on the second iteration).

## Follow-up: remaining internal-process references in comments and the log

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

---

## Follow-up: a record-less actor is never resolvable by slug — synthesis removed

The project-identity synthesis introduced by the previous follow-up (a record-less actor's agent slug and project labels, recovered from its actor name and verified against its own atespace) was itself found to be exploitable: verification only happened when the CALLING lookup's own filter happened to carry a project-ID key. Any lookup that selected an actor by slug alone — including the broker's actual `stopAgent`/`deleteAgent` call paths, which list agents unscoped at the runtime level and post-filter by project in broker memory — never exercised that verification at all. A single record-less actor in project A, with a same-slug request scoped to project B, resolved to project A's actor and could be stopped or deleted through it. Reproduced deterministically through the real broker handlers (`stopAgent`, `deleteAgent`, `LookupContainerID`), confirmed absent at the pre-D1 baseline, and confirmed a regression introduced by the synthesis work.

**Decision: remove the synthesis entirely, rather than extend it further.** A record-less actor now reports `scion.name` equal to its own actor name — `containerName(project, agent)`, project-prefixed — exactly as before any recovery attempt existed, with no project labels synthesised either. `scion.agent=true` is still set, so the actor still appears in an unfiltered listing; it just can never be found by a bare slug or project filter. The record-having fix (a real in-memory record's slug is used as `AgentInfo.Name`) is unchanged and unaffected — this only concerns actors with no in-memory record for this runtime instance (e.g. right after a broker restart).

This is a structural argument, not a patched special case: with record-less entries reporting their actor name (which always contains the project-prefixing separator a real agent slug can never contain), no query that matches by slug can ever match one, regardless of which project it's scoped to, which broker call path it goes through, or which of the broker's generic label-matching helpers are involved. Nothing in `pkg/agent`'s generic helpers needed to change for this — the class of bug they permit (treating an unlabeled entry as belonging to any project) simply never gets a chance to run, because the entry it would apply to never matches the initial slug filter in the first place.

Consequence, accepted deliberately: a record-less actor cannot be stopped, deleted, exec'd into, or have its logs read by slug at all, from any project, until either its in-memory record is somehow restored or an operator identifies and removes it by other means (e.g. directly against the cluster). The durable fix is persisting agent records so they survive a broker restart in the first place; the generic slug-matching call path this exploited is tracked separately.

### Removed

- `substrateSynthesizedAgentName` and its unambiguous-inversion rule.
- The two-pass tally in `List` that decided whether a record-less actor's candidate slug was safe to trust.
- The atespace-based project-ID matching branch in `substrateLabelsMatch` (record-less entries never carry a project-ID-shaped label to match against anymore, so the branch had no remaining purpose); `substrateLabelsMatch` is back to its original four-parameter form.
- Tests that only made sense with synthesis in place: a name-inversion unit-test table, a project-ID-scoped-lookup-discriminates test, and a three-level actor name test. Each is either removed or replaced by a test asserting the corresponding actor is now simply never matched.

### Kept or added

- The exact two-actor, same-slug, different-project scenario, both `ListActors` return orders: an unscoped delete finds neither actor.
- A record-having actor's real slug is unaffected by an unrelated record-less actor whose actor name happens to end the same way — now exercised for both possible return orders deterministically (via a test-only override that replaces the fake's unordered map iteration with an explicit, chosen order), rather than relying on Go's randomized map iteration to happen to exercise the interesting order on some fraction of runs.
- The record-having happy path (a real in-memory record's slug resolves and deletes correctly) is unchanged and still covered.
- The unscoped record-less delete test now asserts a no-op (zero calls to the fake client, actor left in place) instead of asserting a successful delete.
- New broker-level tests, added specifically for this follow-up: a real `*Server` bound to a real `*SubstrateRuntime` over the fake ateapi client, with a single record-less actor in project A, driving the actual `stopAgent`, `deleteAgent`, and `LookupContainerID` code paths with a request scoped to project B (must leave the actor untouched) and, separately, to project A itself (also untouched — the documented no-op, since nothing here can distinguish "the right project asked" from "an unverifiable actor exists" strongly enough to act on it).
- The `List` doc comment now states the limitation directly: a record-less actor is never resolvable by slug, by design, and explains why an earlier attempt at recovering that resolution was reverted rather than iterated on further.

### Fail-before evidence

Runtime level, with the previous follow-up's synthesis code restored temporarily over today's test files:
```
Name = "orphaned-agent", want the actor name "myproj--orphaned-agent" ...
List() with scion.name="sb-smoke-2" filter = [...one match...], want no matches (a record-less actor is never resolvable by slug)
List(scion.project_id="aaaaaaaaaaaa") = [...one match...], want no matches (a record-less actor carries no project label to match, even for its own project)
```

Agent level, same temporary restoration:
```
DeleteActor called 2 times, want 0 (a record-less actor is never resolvable by slug)
DeleteActorEgressPolicy called 2 times, want 0
the record-less actor was removed — it must be untouched (documented no-op)
```

Broker level — the actual reported defect, reproduced through the real HTTP-facing handlers:
```
LookupContainerID("dev", projB) = "scion-aaaaaaaaaaaa/projA--dev", want "" — must never resolve to project A's actor
projA's actor was removed by a project-B-scoped stop — it must be untouched
projA's actor was removed by a project-B-scoped delete — it must be untouched
LookupContainerID("dev", projA) = "scion-aaaaaaaaaaaa/projA--dev", want "" (documented no-op: a record-less actor is never resolvable by slug)
projA's own actor was removed by a same-project stop — it must be untouched (documented no-op)
projA's own actor was removed by a same-project delete — it must be untouched (documented no-op)
```

**Pass-after:** every test above passes; the broker-level tests specifically confirm the previously-reachable cross-project stop/delete/lookup no longer succeed, and that a same-project request is a clean no-op rather than an error.

### Gate results (this follow-up)

- `go build ./...` — pass.
- `go vet` on `pkg/runtime/...`, `pkg/runtimebroker/...`, `pkg/agent/...`, `pkg/config/...` — pass, no output.
- `gofmt -l` on every changed file — clean.
- `go test -count=1` on the same package trees — all pass except the same pre-existing `TestNativeTelemetryPolicyEffectiveChildEnv/disabled` (`pkg/sciontool/supervisor`), not touched by this task.
- `go test -race -count=1` on `pkg/runtime`, `pkg/runtimebroker`, `pkg/agent` — pass, no races.
- `go test -count=50` on the new/changed substrate tests in all three packages — pass, no flakes.
- `GOGC=40 golangci-lint run --new-from-rev=c3b6e821d --concurrency=1 ./...` — 0 issues.
- Hygiene grep (`round [0-9]|sb-rev|sb-dev|sb-em|substrate-lead|finding #`, plus a second pass for bare finding-style references) over every file changed this follow-up — no hits, other than two pre-existing references in `pkg/runtimebroker/handlers.go` (an unrelated numbering scheme from before this branch existed, confirmed via history — not touched by this task).

### Functions touched (this follow-up)

- `pkg/runtime/substrate_runtime.go`: `SubstrateRuntime.List` (record-less entries no longer synthesise anything), `substrateLabelsMatch` (back to its original four-parameter form). `substrateSynthesizedAgentName` removed.
- `pkg/runtime/substrate_runtime_test.go`: tests updated or replaced as described above.
- `pkg/agent/substrate_delete_test.go`: tests updated or replaced as described above; added a deterministic-ordering override to the local fake client so an order-dependent scenario can be tested for both orders reliably.
- `pkg/runtimebroker/substrate_cross_project_test.go` (new file): the broker-level regression tests described above.

No changes to `pkg/agent/manager.go`, `pkg/runtimebroker/handlers.go`, any other runtime, or the broker's generic (non-substrate-scoped) code paths this follow-up. No `go.mod`/`go.sum` changes.

---

## Follow-up: two record-having same-slug agents in different projects — the delete-leak fix's own wrong-actor case

Making `AgentInfo.Name = labels["scion.name"]` for record-having actors (the D1 fix above) fixed the original delete-leak, but opened a new, narrower wrong-actor case: `SubstrateRuntime.List` never set `AgentInfo.ProjectPath`, so a broker-level caller resolving a project-scoped delete always got back an empty project path, which meant `AgentManager.Delete`'s `deletionProjectName` stayed empty too, which meant its project-matching check never engaged — a project-B-scoped `deleteAgent("dev", …)` could delete project A's `dev` actor instead, depending on `ListActors` return order (reproduced deterministically below). At the pre-D1 baseline this same request was already a no-op (safe, if unhelpful); after D1, it became an active wrong-actor delete.

**Fix, part one — carry the project path on the record.** `RunConfig` has no dedicated project-path field (unlike `Project`/`ProjectID`); `pkg/agent/run.go`'s `Start` carries it as an annotation instead (`projectcompat.ProjectPathLabels(projectDir, true)`, set unconditionally once the project directory resolves — this runs identically whether the start was dispatched by the hub or invoked locally; there is no separate code path for either). `SubstrateRuntime.Run` now reads it the same way `DockerRuntime.List`/`K8sRuntime.List` do — `projectcompat.ProjectPathFromLabels(cfg.Annotations)`, falling back to `cfg.Labels` — and stores it on `substrateAgentRecord`. `List` sets `AgentInfo.ProjectPath` from the record. On its own, this fix is sufficient to make a correctly project-scoped delete resolve the right actor: `deleteAgent`'s own first, unscoped-by-slug listing call (`{"scion.agent": "true"}`, filtered client-side by project ID) already picked the right actor even before this fix, so it now also picks up that actor's real, correct project path and passes it through to `AgentManager.Delete` correctly.

**Fix, part two — fail closed for an unscoped same-slug query, unconditionally.** `AgentManager.Delete`/`Stop`'s own *internal* `Runtime.List` call (`pkg/agent/manager.go`) and `LookupContainerID`'s internal `manager.List` call (`pkg/runtimebroker/server.go`) both filter by `"scion.name"` alone — neither ever adds a project-scoping key to the map passed into `Runtime.List`, regardless of what project the outer, broker-level caller resolved. This means part one's fix, while necessary, isn't sufficient at those specific call sites: an unscoped-by-slug query still has no way to distinguish two record-having actors that share a slug across different projects. `SubstrateRuntime.List` now tallies record-having actors by slug and, when the incoming filter has a `"scion.name"` key but no project-scoping key (`scion.project`/`scion.grove`/`scion.project_id`/`scion.grove_id`) and more than one record-having actor shares the requested slug, excludes all of them from the result rather than returning an arbitrary one. A query that does carry a project-scoping key is unaffected, and the tally never triggers when a slug is unique (the overwhelming common case), so this fix has no effect on ordinary single-project usage.

**Reported consequence, as required:** because `AgentManager.Delete`, `AgentManager.Stop`, and `LookupContainerID`'s own internal `Runtime.List` calls are unscoped by project at that layer, the guard makes `deleteAgent`, `stopAgent`, and `LookupContainerID` all become **no-ops** for the specific "two record-having actors share an identical slug in different projects" scenario — even when the broker-level caller correctly resolved the right project. This holds regardless of which of the two projects the caller was scoped to. A no-op here means: `deleteAgent`/`stopAgent` make zero `DeleteActor` calls and leave both actors running (HTTP 204 is still returned — the broker's not-found/success semantics for this path are unchanged by this fix); `LookupContainerID` returns a not-found error instead of either actor's container ID. This is deliberate: a failed or no-op action is acceptable here, a wrong-actor action is not. The single-project case (one agent per slug, the common case) is completely unaffected by this trade-off — see the D1 happy-path test, which still passes unchanged.

### Kept or added

- `substrateAgentRecord.ProjectPath` and `AgentInfo.ProjectPath`, populated as described above.
- The ambiguity guard in `List`, tallying record-having actors by slug and excluding all of them from an unscoped-by-slug result when more than one shares the requested slug.
- Runtime-level test: two record-having actors sharing a slug across two projects — an unscoped `List({"scion.name": "dev"})` returns nothing (both `ListActors` orders), while a project-scoped `List` for the same slug still resolves the correct actor and reports the correct `ProjectPath`.
- Agent-level test: the same two-actor, two-project scenario, started for real through `Run` with the same labels/annotations a real `Start` call produces — `AgentManager.Delete("dev", …, "")` makes zero `DeleteActor` calls, both actors left running, both `ListActors` orders.
- Broker-level tests, driving the real `deleteAgent` and `LookupContainerID` code paths against a real `*Server`/`*SubstrateRuntime`: the wrong-actor delete reproduced and confirmed fail-before on the pre-fix code (a project-B-scoped delete removed project A's actor, 100% reproducible with a forced `ListActors` order); after the fix, the same request makes zero `DeleteActor` calls and both actors survive, in both directions (project-A-scoped and project-B-scoped) and both forced orders; `LookupContainerID` scoped to either project returns not-found rather than either actor's container ID. A control test confirms the ordinary single-project case (one agent for the slug) still deletes normally — `AgentInfo.ProjectPath` populated, tally never exceeds one, guard never engages.

### Fail-before evidence

Broker level, pre-fix code (`HEAD` at the point this follow-up started) with today's new test:
```
deleteAgent("dev", projB) called DeleteActor [actor:{atespace:"scion-aaaaaaaaaaaa" name:"proja--dev"} any_state:true actor:{atespace:"scion-aaaaaaaaaaaa" name:"proja--dev"} any_state:true], want zero (ambiguous slug across two projects must fail closed)
projA's actor was removed by a project-B-scoped delete — it must never be the wrong-actor target
```
(and the mirror, with the other forced `ListActors` order, deleting project B's actor instead when scoped to project B — i.e. only "correct" by accident of order, not by any actual project check.)

**Pass-after:** the same test, against the fixed code, makes zero `DeleteActor` calls and leaves both actors in place, for both forced orders and both delete directions.

### Gate results (this follow-up)

- `go build ./...` — pass.
- `go vet` on `pkg/runtime/...`, `pkg/runtimebroker/...`, `pkg/agent/...`, `pkg/config/...` — pass, no output.
- `gofmt -l` on every changed file — clean.
- `go test -count=1` on the same four package trees — all substrate-related tests pass. `pkg/config`, `pkg/agent`, `pkg/runtime`, and `pkg/runtimebroker` each have a set of pre-existing, unrelated failures (a settings-schema decode error, `'auto_expose_ports' expected a map or struct, got "string"`, affecting harness/settings/env-gather tests) — confirmed identical on the pre-this-follow-up baseline by stashing this follow-up's changes and re-running, so not introduced or touched by this work.
- `go test -race -count=1` on `pkg/runtime`, `pkg/runtimebroker`, `pkg/agent` — same pre-existing failures only, no data races in any substrate code; one pre-existing, confirmed-baseline data race in `pkg/runtime/cloudrun` (unrelated package, not touched here).
- `go test -count=50` on every new/changed substrate test in all three packages — pass, no flakes.
- `golangci-lint run --new-from-rev=c3b6e821d --concurrency=1 ./...` — 0 issues.
- Hygiene greps over every file changed this follow-up — no hits.

### Functions touched (this follow-up)

- `pkg/runtime/substrate_runtime.go`: `substrateAgentRecord` (new `ProjectPath` field), `SubstrateRuntime.Run` (computes and stores it), `SubstrateRuntime.List` (sets `AgentInfo.ProjectPath`; adds the unscoped-by-slug ambiguity guard); doc comments updated on both.
- `pkg/runtime/substrate_runtime_test.go`: new runtime-level test for the guard and `ProjectPath`.
- `pkg/agent/substrate_delete_test.go`: new agent-level test for the guard, using realistic hub-dispatched-shaped labels/annotations.
- `pkg/runtimebroker/substrate_manager_test.go`: added `forceListOrder` (and `deleteActorCalls` recording) to the package's fake ateapi client, mirroring the equivalent fake already in `pkg/agent`.
- `pkg/runtimebroker/substrate_cross_project_test.go`: new helpers (`runSubstrateAgentForProject`, `testProjectScionDir`) and new broker-level tests for the guard, the D1 happy-path control, and `LookupContainerID`'s corresponding no-op.

No changes to `pkg/agent/manager.go`, `pkg/runtimebroker/handlers.go`, any other runtime, or the broker's generic (non-substrate-scoped) code paths this follow-up. No `go.mod`/`go.sum` changes.

---

## Follow-up (prepared, not yet merged): `deleteAgent` still fell through to the unscoped delete when the requested project had no match at all

The previous follow-up's fix (`ProjectPath` tracking plus the unscoped-by-slug ambiguity guard) closes the case where the requested project's own record-having actor exists alongside another project's same-slug one. It does not close a narrower variant: a project-B-scoped `deleteAgent("dev", …)` when project B has **no** record-having `dev` of its own at all — never started on this broker, already deleted, or record-less — while project A does. `deleteAgent`'s own `matchesAgent` loop correctly finds no entry for project B (it checks project ID, so project A's entry never matches a project-B request), so `projectPath` stays `""`. Before this follow-up, `deleteAgent` still called `mgr.Delete(id, …, "", …)` anyway; `AgentManager.Delete`'s internal `Runtime.List` call is unscoped by project (`pkg/agent/manager.go`, filters by `"scion.name"` alone), so with an empty `deletionProjectName` its own project-matching check never engaged, and it deleted whichever same-slug actor `ListActors` happened to return first — project A's, the wrong one, reproduced deterministically below.

**Fix, gated to substrate exactly like the D2 fix, `pkg/runtimebroker/handlers.go`'s `deleteAgent`:** the existing `matchesAgent` loop now also records whether it found a match at all (`matched`). Right before the `mgr.Delete` call, if the resolved runtime's `Name()` is `"substrate"`, the request carries a `projectID`, and no entry matched: return the existing not-found shape (`NotFound(w, "Agent")`) without calling `mgr.Delete` at all, instead of falling through to its unscoped internal lookup. `deleteAgent` now resolves via `resolveAgentRuntimeTarget` (which already existed, returning both the manager and its runtime) instead of `resolveManagerForAgent` (which just discards the runtime half of the same call) — the manager value handed to every other line is identical either way, so this is not itself a behavior change for any runtime.

When an entry *does* match, nothing changes: `deleteAgent` already resolved that entry's real `ProjectPath` (the previous follow-up's fix) and passes it to `mgr.Delete`, which scopes `AgentManager.Delete`'s `deletionProjectName` + `matchAgentProject` check to exactly that entry — this was already correct before this follow-up for the "both projects have their own same-slug agent" case; only the "no match in the requested project at all" gap needed closing.

**Why this targets only the matched entry, not a guess:** `mgr.Delete` is still invoked with the caller's bare agent ID (never a `ContainerID`, which `AgentManager.Delete`'s `"scion.name"` filter wouldn't find anyway — `AgentManager` itself is unchanged and untouched by this fix) and the real, resolved `ProjectPath` of the one entry `matchesAgent` verified belongs to the requested project. The new gate only ever *prevents* a call that would otherwise run unscoped; it never redirects `mgr.Delete` toward a specific `ContainerID` or otherwise widens what `AgentManager` can act on.

**Every other runtime type is unaffected by construction, not just by testing:** the new gate is an `if rt.Name() == "substrate" && …` condition placed after the existing matching loop and before the existing `mgr.Delete` call; for any other runtime it is always false, so `mgr.Delete` is reached with exactly the same arguments as before this fix, on exactly the same code path. Confirmed both by a diff review (the only change with a behavioral effect for any other runtime is switching from `resolveManagerForAgent` to `resolveAgentRuntimeTarget`, which returns the identical manager value — `resolveManagerForAgent` is defined as exactly that call, discarding the runtime half) and by a dedicated test using a mock runtime whose sole listed agent belongs to a different project than the request: for a non-substrate `Name()`, `deleteAgent` still falls through to `mgr.Delete` and the underlying `Runtime.Delete` is still invoked, unchanged.

**Hub expectations, checked rather than assumed:** `pkg/hub/controlchannel_client.go`'s `ControlChannelBrokerClient.DeleteAgent` (lines 225–245) already comments "Allow 404 for idempotent delete" and returns `nil` for both a 2xx and a 404 response from this same endpoint — the hub's own delete path already treats "already gone" and "successfully deleted" as the same outcome. Returning `NotFound` instead of silently no-opping through `mgr.Delete` therefore does not change what the hub does with the result; hub-side cleanup (e.g. removing its own agent record) proceeds either way.

**Trade-off worth noting:** `mgr.Delete`'s `deleteFiles` branch (in `AgentManager.Delete`, unchanged) currently runs unconditionally once `Runtime.List`/`Stop`/`Delete` are reached, regardless of whether a container was actually found — so, generically, a `deleteFiles=true` request for an agent that's already gone still attempts local file cleanup today. Skipping the `mgr.Delete` call entirely for the new substrate no-match case also skips that file-cleanup attempt for this narrow scenario (project-scoped substrate delete, no matching entry in that project). This wasn't called out as a requirement and no test currently exercises `deleteFiles=true` against this exact gate; flagged here for visibility rather than silently accepted.

### Tests added (all broker-level, real `*Server` + `*SubstrateRuntime`, or a mock runtime for the non-substrate control)

- Record-having project A `dev` only; `deleteAgent("dev", projB)` → zero `DeleteActor` calls, project A's actor survives, and the response is now `404` instead of `204`. The reviewer's exact repro; both a real `ListActors` order and its reverse, forced deterministically.
- The same, with project B holding a record-less actor instead of nothing — `matchesAgent` never matches a record-less actor by slug at all, so this fails closed the same way.
- Record-having project B `dev` only; `deleteAgent("dev", projB)` → deletes project B's actor normally, confirming the gate does not fire when a genuine match exists.
- A mock (non-substrate) runtime whose sole listed agent belongs to a different project than the request: `deleteAgent` still falls through to `mgr.Delete` and `Runtime.Delete` is still invoked — proving the gate has no effect outside substrate.
- The previous follow-up's "both projects have their own same-slug `dev`" test (delete for project B is a no-op via the ambiguity guard, neither actor deleted) re-run unchanged against this fix — still passes: that case was already handled entirely inside `SubstrateRuntime.List`, before `deleteAgent`'s own matching loop even runs, and remains untouched by this change.

### Fail-before evidence

Broker level, against the pre-this-follow-up code, forced `ListActors` order (both directions gave the same result):
```
deleteAgent("dev", projB) status = 204, want 404 (not-found, no matching entry in project B)
deleteAgent("dev", projB) called DeleteActor [actor:{atespace:"scion-aaaaaaaaaaaa" name:"proja--dev"} any_state:true actor:{atespace:"scion-aaaaaaaaaaaa" name:"proja--dev"} any_state:true], want zero — project B has no "dev" of its own, project A's must never be the fallback target
projA's actor was removed by a project-B-scoped delete that had no matching entry in project B — it must never be the wrong-actor target
```
and the same shape for the record-less-project-B variant.

**Pass-after:** both tests pass against the fixed code — zero `DeleteActor` calls, project A's actor survives, response is `404`.

### Gate results (this follow-up)

- `go build ./...` — pass.
- `go vet` on `pkg/runtime/...`, `pkg/runtimebroker/...`, `pkg/agent/...`, `pkg/config/...` — pass, no output.
- `gofmt -l` on every changed file — clean.
- `go test -count=1` on the same four package trees — all substrate-related tests pass; the same pre-existing, unrelated failure set as the previous follow-up (the `'auto_expose_ports'` settings-schema decode error affecting harness/settings/env-gather tests) — confirmed identical on the pre-this-follow-up baseline, not introduced by this work.
- `go test -race -count=1` on `pkg/runtime`, `pkg/runtimebroker`, `pkg/agent` — same pre-existing failures only, no new data races; the same pre-existing, confirmed-baseline data race in `pkg/runtime/cloudrun` (unrelated package, not touched here).
- `go test -count=50` on every new/changed test — pass, no flakes.
- `golangci-lint run --new-from-rev=c3b6e821d --concurrency=1 ./...` — 0 issues.
- Hygiene greps over every file changed this follow-up — no hits (two pre-existing, unrelated `N1-7` references in `handlers.go`, confirmed via `git blame` to predate this branch — not touched by this task).

### Functions touched (this follow-up)

- `pkg/runtimebroker/handlers.go`: `deleteAgent` only — resolves via `resolveAgentRuntimeTarget` instead of `resolveManagerForAgent` (identical manager value either way), tracks whether the matching loop found an entry, and gates the existing `mgr.Delete` call on that for substrate specifically.
- `pkg/runtimebroker/substrate_cross_project_test.go`: new broker-level tests described above, plus one mock-runtime test for the non-substrate control.

`pkg/agent/manager.go` is unchanged. Every other runtime's `deleteAgent` code path is unchanged by construction (see above) and confirmed unchanged by test. No `go.mod`/`go.sum` changes.

**Status: prepared on a local branch, not yet reviewed or merged.** This section documents what was prepared, pending go/no-go.

### Addendum: the gate holds for a NAMED substrate profile, and quantifying the file-cleanup skipped in the no-match path

**Named substrate profiles.** Every test above builds a `*SubstrateRuntime` directly (`runtime.NewSubstrateRuntimeForTest`), which is trivially always named `"substrate"`. To confirm the gate's `rt.Name() == "substrate"` check isn't accidentally tied to that test-only construction, a new test drives the real settings-driven resolution path instead: `config.VersionedSettings.ResolveRuntime` (the exact method `resolveManagerForOpts` and `runtime.GetRuntime` call, `pkg/config/settings_v1.go:123`) against two differently-named runtime entries matching the live settings shape from the D2 report — `"substrate-prod"` and `"substrate-nip"`, each `type: substrate` — followed by `runtime.NewSubstrateRuntime` (`pkg/runtime/factory.go:249`), the exact call `GetRuntime`'s `"substrate"` case makes. Both resolve to `runtimeType == "substrate"` (the settings `Type` field, not the map key — `pkg/config/settings_v1.go:136-140`), and the resulting runtime's `Name()` is `"substrate"` regardless of which name the profile used, because `SubstrateRuntime.Name()` (`pkg/runtime/substrate_runtime.go:286`) is a hardcoded literal that never consults its config — there is no field anywhere in `V1SubstrateConfig` or the construction path that could carry a profile name into it. The test also calls `resolveAgentRuntimeTarget` directly and confirms its returned runtime reports `"substrate"` too, then drives the same no-match 404 gate end to end for both profile names.

This test builds the `*VersionedSettings` value in memory rather than writing a settings file to disk: at the time this was written, every settings.json/settings.yaml-file-backed test in this environment fails to load at all (`'auto_expose_ports' expected a map or struct, got "string"` — confirmed via a standalone probe calling `config.LoadEffectiveSettings` directly, and via this same package's own pre-existing `TestResolveManagerForOpts_SubstrateProfilesGetTheirOwnConfig`, which hits the identical decode error and predates this task entirely). `VersionedSettings.ResolveRuntime` and `runtime.NewSubstrateRuntime` don't go through that decode path, so the test still exercises the real name-to-config-to-runtime resolution logic end to end — just not the on-disk decode step, which is an unrelated, pre-existing environment issue, not something masked or worked around here.

**Quantifying the file cleanup the no-match gate now skips.** `AgentManager.Delete`'s `deleteFiles` branch (`pkg/agent/manager.go:176-180`, calling `DeleteAgentFiles`, `pkg/agent/provision.go:41`) never runs at all when the new gate returns 404 before `mgr.Delete` is ever called. What that branch would have cleaned, per agent, all rooted under whatever project directory this agent's `ProjectPath` resolves to on the broker's own host/pod:

- **Agent state dir** — `config.GetAgentDir(projectDir, agentName, sharedWorkspace)` (`pkg/config/project_marker.go:328`): `<projectDir>/agents/<agentName>/` normally, or the external split-storage path `<GetGitProjectExternalAgentsDir(projectDir)>/<agentName>/` for shared-workspace git projects. Contains `prompt.md` (created at `pkg/agent/provision.go:541`) and `scion-agent.json` (`pkg/agent/provision.go:1331`). Removed via `util.RemoveAllSafe(agentDir)` at `pkg/agent/provision.go:225-233` (collected into `dirsToDelete` at `:173-200`); the external split-storage variant removed separately at `:246-258`.
- **Agent home dir** — `config.GetAgentHomePath(projectDir, agentName)` (`pkg/config/project_marker.go:307`): `<agentDir>/home/` (or the external `<externalDir>/<agentName>/home/`). Contained within, and removed as part of, the agent-dir/external-dir removal above.
- **Per-agent workspace/worktree** (worktree-per-agent mode) — `<agentDir>/workspace` (`pkg/agent/provision.go:517`) or the project-wide shared base `<projectDir>/workspace/worktrees/<agentName>`. Removed at `pkg/agent/provision.go:155-171` (shared base) and `:183-197` (agentDir-local); the multi-sharer refcounted variant torn down at `:111-149`.
- **Global-mode agent dir fallback** — `<globalAgentsDir>/<agentName>` (`pkg/agent/provision.go:89-91`), removed in the same pass as the project-scoped agent dir.
- **Git branch**, only when `removeBranch` is set — deleted as a side effect of the `util.RemoveWorktree(...)` calls above, or the explicit fallback `util.DeleteBranchIn(repoRoot, branchName)` at `pkg/agent/provision.go:215-221` (and the refcount-path fallback at `:137-142`).
- **Stale worktree registration pruning** — `util.PruneWorktreesIn(repoRoot)` at `pkg/agent/provision.go:205-209`; bookkeeping rather than user content, but also skipped.

**Does substrate's own `Run` create any of this? No — but hub-dispatched brokers create it anyway, unconditionally.** `SubstrateRuntime.Run`/`.Delete` (`pkg/runtime/substrate_runtime.go`) never touch local disk at all — they only call the ateapi Control/Router gRPC endpoints. All of the paths above are created one layer up, by `AgentManager.Start` (`pkg/agent/run.go:77`), which calls `GetAgent` (`pkg/agent/run.go:180` → `pkg/agent/provision.go:1700`) unconditionally, before `Runtime.Run` is ever invoked (`pkg/agent/run.go:1229`) — `pkg/agent/provision.go` and `pkg/agent/run.go` contain zero runtime-type branches of any kind (confirmed by grep for `"substrate"`, `"docker"`, and similar in both files: no hits). So yes: a hub-dispatched broker running a substrate profile creates the full local agent dir/home/prompt.md/worktree state described above on its own host/pod for every substrate agent it starts, exactly as it would for Docker or K8s — Substrate just never itself reads or writes any of it.

**How common the leak actually is.** Substrate's `Stop` is `Delete` in Phase 1 (`pkg/runtime/substrate_runtime.go:526-528`: `func (r *SubstrateRuntime) Stop(...) error { return r.Delete(ctx, id) }`), and `Delete` removes both the actor and its in-memory record (`substrateAgentRecords`, `pkg/runtime/substrate_runtime.go:508-513`) in the same call. So a prior stop leaves nothing at all to match on a later delete for that agent — not even a record-less entry, since the actor itself is gone from the cluster. The hub's real delete-agent handler defaults both `deleteFiles` and `removeBranch` to `true` unless the caller explicitly passes `"false"` (`pkg/hub/handlers_agents_core.go:2592-2595`, comment: "Default deleteFiles and removeBranch to true for full cleanup"; overridden to `false` only when soft-delete plus `SoftDeleteRetainFiles` is configured, `:2608-2611`). So for the ordinary "stop, then later delete" sequence — ordinary hub lifecycle management, not a rare corner case — the delete call requests full file cleanup by default, and after this fix that cleanup is now skipped entirely rather than attempted. This is a real, quantified regression this fix introduces relative to the pre-fix (wrong-actor-delete) behavior, traded for correctness (a leaked local directory is recoverable; a wrong-actor delete is not) — flagged here for the decision, not resolved.

No test in this branch exercises `deleteFiles=true` against the no-match gate's `AgentManager.Delete`/`DeleteAgentFiles` skip specifically (the two no-match repro tests both use the default `deleteFiles=false`); a related `deleteFiles=true` scenario — the project-blind hub-managed-project fallback — is covered separately below.

### Addendum: `findAgentInHubManagedProjects` — reachable in the no-match path, closed there; confirmed unreachable in the matched path

`deleteAgent` has a second, older fallback beyond `AgentManager.Delete`'s own unscoped `Runtime.List`: `findAgentInHubManagedProjects(id)` (`pkg/runtimebroker/handlers.go`), which activates when `projectPath == "" && deleteFiles`. It takes only the bare agent name — no project identifier at all — and returns the first hub-managed project directory (under `~/.scion/projects/*` or `~/.scion/groves/*`) whose `agents/<name>` (or external split-storage equivalent) exists. This is exactly the same class of project-blind, order-dependent resolution the ambiguity guard exists to prevent, just for local files instead of the actor.

**Matched substrate path: confirmed unreachable, no code change needed.** For a matched entry, `projectPath` is already set from that entry's own `AgentInfo.ProjectPath` before this fallback's `if projectPath == ""` check runs. A record-having substrate entry can only exist with a populated `ProjectPath` in the first place: `AgentManager.Start` (`pkg/agent/run.go:77-82`) returns before creating any record at all if `config.GetResolvedProjectDir` errors, and when it succeeds, `Start` sets the `"scion.project_path"` annotation unconditionally from that same resolved directory — so a matched entry's `ProjectPath` is never empty, and the fallback's guard never opens. Verified empirically, not just by reading: a test plants a decoy hub-managed project whose own `agents/dev` directory the fallback would return if it ran, starts a real, matched `dev` agent in project B with `deleteFiles=true`, and confirms the decoy directory is never touched.

**No-match substrate path: reachable, and closed.** The new substrate-only 404 gate was originally placed after both this fallback and the soft-delete `agent-info.json`-marking block. In the no-match case `projectPath` is `""`, so with `deleteFiles=true` the fallback DOES run, taking only the bare agent name, and can resolve to a *different* project's `agents/<name>` directory if one happens to exist. That resolved (wrong) `projectPath` was then live for the soft-delete marking block immediately below it — `agent.UpdateAgentConfig`/`UpdateAgentDeletedAt` would write into that other project's `agent-info.json`, marking an unrelated agent as deleted. (The wrong path was never live for actual file *deletion*, since the substrate gate — wherever it sat — still ran before `mgr.Delete`; the reachable damage was limited to this one metadata write.) Reproduced deterministically: a decoy project's `agent-info.json` (`{"phase":"running"}`) was overwritten to `{"phase":"deleted", ...}` by a `projectB`-scoped delete for an agent that only exists in a different project, with `deleteFiles=true&softDelete=true` and no matching entry in project B.

**Fix:** the substrate-only 404 gate now runs immediately after the matching loop, before both the hub-managed-project fallback and the soft-delete marking block — for substrate, a no-match delete never resolves `projectPath` from a project-blind guess for any purpose, not just for `mgr.Delete`. This has no effect on the matched substrate path (the gate only fires when unmatched) or on any other runtime (the gate is still `rt.Name() == "substrate"`-gated only); the fallback and soft-delete blocks are otherwise completely unchanged and still run in their original order and shape for every other case.

Per the instruction this was checked against: this is reachable within this branch's own new no-match path, for substrate, so it is closed here rather than merely reported. A parallel instance of the same project-blind fallback for a non-substrate runtime, or for `AgentManager`'s own generic call paths, is not touched — that is `pkg/agent`'s generic slug-matching hardening territory (ptone/scion#1819 family), out of scope here.

### Addendum: the `'auto_expose_ports'` failures were an ambient environment variable, not a product bug

Every "pre-existing, unrelated" failure set reported across this file's gate results — the settings-schema decode error `'auto_expose_ports' expected a map or struct, got "string"`, affecting any test that calls `config.LoadEffectiveSettings` — traced to a single cause: `SCION_AUTO_EXPOSE_PORTS=true` was set in the ambient shell environment this work ran in (this container is itself a scion-dispatched agent, so `SCION_*` env vars are injected the same way they'd be injected into any agent's container). Settings loading merges an env-var overlay on top of file-based settings, and `SCION_AUTO_EXPOSE_PORTS`'s string value collided with a settings field that expects a map/struct — nothing to do with substrate, this branch's changes, or any settings file's own content.

**Confirmed by re-running every gate with the full `SCION_*` env and `CLAUDE_CODE_ENABLE_TELEMETRY` unset:**
```
for v in $(env|grep -o '^SCION_[A-Za-z0-9_]*'); do unset $v; done; unset CLAUDE_CODE_ENABLE_TELEMETRY
```
- `go build ./...` — pass.
- `go vet` on `pkg/runtime/...`, `pkg/runtimebroker/...`, `pkg/agent/...`, `pkg/config/...` — pass, no output.
- `gofmt -l` on every changed file — clean.
- `go test -count=1` on all four package trees — **all green**, including `pkg/config`, `pkg/agent`, and every previously-"pre-existing-failure" test — `TestResolveManagerForOpts_SubstrateProfilesGetTheirOwnConfig` and `TestGetRuntime_Substrate_SettingsBased_Memoized` included. Confirmed directly by re-running `SCION_AUTO_EXPOSE_PORTS=true go test ...` against the now-on-disk-settings-based named-profile test below and reproducing the identical decode error on demand.
- `go test -race -count=1` on `pkg/runtime`, `pkg/runtimebroker`, `pkg/agent` — all green **except one remaining failure**: `TestStreamLogsPropagatesListingErrors` in `pkg/runtime/cloudrun`, a genuine data race in `pkg/runtime/cloudrun/logs.go`'s background retry goroutine racing a test cleanup callback (`logs_test.go:104` writes, `logs.go:235`/`logs.go:148` reads, unsynchronized). Confirmed unrelated to environment variables (identical failure with or without the `SCION_*`/telemetry unset) and unrelated to substrate — this package is untouched by any work in this file's history.
- `go test -count=50` on the substrate-related tests in all three touched packages — pass, no flakes, under the clean environment too.
- `golangci-lint run --new-from-rev=c3b6e821d --concurrency=1 ./...` — 0 issues.

**`TestSubstrateBroker_NoMatchGate_FiresForNamedSubstrateProfile` now goes through the real on-disk path**, since it's cheap and the underlying bug is now understood: it writes an actual `settings.json` per subtest (matching `TestResolveManagerForOpts_SubstrateProfilesGetTheirOwnConfig`'s shape) and calls `config.LoadEffectiveSettings` for real, rather than constructing a `*VersionedSettings` value in memory. It passes under a clean environment and fails with the documented decode error when `SCION_AUTO_EXPOSE_PORTS=true` is set — directly confirming the root cause.

The one remaining failure (`pkg/runtime/cloudrun`'s pre-existing data race) is unrelated to this branch and not addressed here.

---

## Follow-up: the substrate no-match delete gate returns 204, not 404 (live 502 over the control channel)

Live testing over the hub's control channel showed a delete-after-stop on a substrate agent returning a **502**. Root cause: the substrate no-match gate (previous follow-up) returns 404, but `ControlChannelBrokerClient.doRequest` (`pkg/hub/controlchannel_client.go`) turns *every* tunneled response with a status of 400 or above into an error, regardless of which code it is — so `DeleteAgent`'s own "allow 404 for idempotent delete" check, a few lines further down in the same file, is unreachable dead code on this transport: `doRequest` already returned an error before that check ever runs. (The plain HTTP transport, `broker_http_transport.go`, does exempt 404 — this is control-channel-specific.) The hub-side fix for the underlying `doRequest` blind spot is out of scope here (tracked as `ptone/scion#1846`); this follow-up changes only the broker's own status code for this one gate.

**Change:** the substrate no-match gate in `deleteAgent` now returns **204 No Content** instead of 404. It still does not call `mgr.Delete`, still runs before the project-blind hub-managed-project fallback and the soft-delete `agent-info.json` marking, and is still gated to `rt.Name() == "substrate" && projectID != "" && !matched`. Nothing else about the gate's conditions or placement changed. An info-level log line now records the no-op (agent slug and project ID only, no secrets) so it's still visible in broker logs even though the response is now indistinguishable, at the HTTP layer, from an actual delete.

**Only this one gate changes — verified, not assumed.** The ambiguity/multi-match path (two record-having actors sharing a slug across projects — `SubstrateRuntime.List`'s tally-based exclusion) is untouched. Traced and confirmed by direct test: that path was already returning 204 before this follow-up too, via a different mechanism — `matchesAgent`'s own project-ID check already finds the correct project's entry (so the no-match gate doesn't fire; `matched` is `true`), but `AgentManager.Delete`'s *internal* unscoped `Runtime.List` call then hits `SubstrateRuntime.List`'s ambiguity guard, which excludes all same-slug entries — no error, `mgr.Delete` returns `(false, nil)`, and `deleteAgent` reaches its normal 204 success path. This is unaffected by this follow-up; the two code paths (the no-match gate and the ambiguity guard) are structurally independent, and only the former changed.

### Before/after: what `deleteAgent` returns, by substrate path

| Path | Before | After | Changed? |
|---|---|---|---|
| Matched, unique slug (ordinary delete) | 204 | 204 | No |
| No match in the requested project (this follow-up's target) | 404 | 204 | **Yes** |
| Record-less actor (any project) | 404 (via the no-match gate above — a record-less actor never sets `matched`) | 204 | **Yes** (same gate, different actor shape) |
| Ambiguous: 2+ record-having actors share the slug across projects | 204 (via the ambiguity guard inside `Runtime.List`, `mgr.Delete` silently no-ops) | 204 | No |
| Non-substrate runtime, any of the above | Unchanged (the gate is substrate-only; falls through to `mgr.Delete` exactly as before) | Unchanged | No |

### Deferred nits also cleared this follow-up

- Renamed `TestSubstrateBroker_ScopedDeleteMatchesOwnProject_D1HappyPath` to `TestSubstrateBroker_ScopedDeleteMatchesOwnProject`, and removed "the review's exact repro" from its doc comment.
- Trimmed the named-profile test's comment: dropped the changelog-style paragraph describing an earlier, in-memory version of the test, and the reference to a numbered report; it now just states what the test does and why.
- Reworded a comment in the hub-managed-project-scan test that contained the legacy `groves` literal, so `make check-custom`'s grove-literal check has zero hits in any file this branch touches.
- Replaced direct `projectcompat.LabelGrove`/`LabelGroveID` references in `SubstrateRuntime.List`'s ambiguity guard and `substrateLabelsMatch` with two new small helpers, `projectcompat.IsProjectNameLabelKey`/`IsProjectIDLabelKey` (in the already-allowlisted `pkg/projectcompat`) — these were also flagged by the grove-literal check (`pkg/runtime/substrate_runtime.go` isn't allowlisted either), predating this follow-up. Same behavior, no legacy-label identifiers spelled out outside `pkg/projectcompat` anymore.

### Tests

- The existing no-match/record-less repro tests now assert 204 instead of 404, with the same zero-`DeleteActor`-calls and unchanged-file/agent-info assertions as before.
- New: an info-level log line is asserted directly — `slog.SetDefault` is redirected to a buffer before the test server is constructed (`s.agentLifecycleLog` captures `slog.Default()` once, at construction), and the buffer is parsed as JSON lines afterward, checking for an INFO record with the agent slug and project ID. This mirrors an existing capture pattern already used elsewhere in this codebase for slog-based assertions.
- The non-substrate parity test is unchanged (still asserts 204, which was never gated on the substrate check to begin with).
- Optional hub-side test, done: a fake tunnel already existed in `pkg/hub/controlchannel_client_test.go` for exactly this client, so no new test infrastructure was needed. Added `statusCode` as a configurable field on the existing mock tunnel and a new test asserting `ControlChannelBrokerClient.DeleteAgent` returns `nil` for a tunneled 204 response — confirming a 204 needs no special-casing at all: `doRequest` only treats a status of 400 or above as an error, so 204 was always an ordinary success path on this transport, unlike 404.

### Gate results (this follow-up, `SCION_*` and `CLAUDE_CODE_ENABLE_TELEMETRY` unset)

- `go build ./...` — pass.
- `go vet` on `pkg/runtime/...`, `pkg/runtimebroker/...`, `pkg/agent/...`, `pkg/config/...`, `pkg/projectcompat/...`, `pkg/hub/...` — pass, no output.
- `gofmt -l` on every changed file — clean.
- `go test -count=1` on `pkg/runtime/...`, `pkg/runtimebroker/...`, `pkg/agent/...`, `pkg/config/...` — all pass.
- `go test -race -count=1` on `pkg/runtime`, `pkg/runtimebroker`, `pkg/agent` — pass, except the same pre-existing, confirmed-unrelated `pkg/runtime/cloudrun` data race noted in the previous follow-up.
- `go test -count=50` on every changed test (`pkg/runtimebroker`, `pkg/hub`, `pkg/projectcompat`, `pkg/runtime`) — pass, no flakes.
- `golangci-lint run --new-from-rev=c3b6e821d --concurrency=1 ./...` — 0 issues.
- `make check-custom` — fails overall on pre-existing, unrelated legacy-literal hits in NFS shared-dir-storage files this branch has never touched; zero hits in any file this branch touches (confirmed by diffing the check's output against the branch's own changed-file list).
- Hygiene greps (`round [0-9]|this round|sb-rev|sb-dev|sb-em|substrate-lead|finding #` and `\b(C1|R1|R2|N[1-3]|E[1-5]|D1|D2)\b`) over every changed file — no hits, other than the same two pre-existing `N1-7` references in `handlers.go` noted previously (confirmed via `git blame` to predate this branch).

### Functions touched (this follow-up)

- `pkg/runtimebroker/handlers.go`: `deleteAgent`'s substrate-only no-match gate — status code, added the info log line, and updated the comment.
- `pkg/runtime/substrate_runtime.go`: `SubstrateRuntime.List`'s ambiguity-guard project-scope check and `substrateLabelsMatch`, rewritten to use the new `projectcompat` helpers instead of naming the legacy label constants directly; comment wording only otherwise.
- `pkg/projectcompat/labels.go`: two new helpers, `IsProjectNameLabelKey` and `IsProjectIDLabelKey`.
- `pkg/runtimebroker/substrate_cross_project_test.go`: updated status-code assertions, a rename, comment trims, and two new tests (info-log assertion; the log-capture helper).
- `pkg/hub/controlchannel_client_test.go`: added a configurable status code to the existing mock tunnel and one new test for the 204 case.

No changes to `pkg/agent/manager.go`, `AgentManager.Delete`/`.Stop`, the ambiguity guard's own logic (only its label-key lookup was refactored, not its behavior), or any other runtime. No `go.mod`/`go.sum` changes.

---

## Follow-up: exec/su and the harness ran as root on substrate before this fix

Live testing found every exec into a substrate actor failing: `sciontool substrate-serve /scion/v1/exec` (`pkg/sciontool/substrate/exec.go`) runs `execAsUserCmd` → `su - scion`, which failed inside the actor with `cannot set groups: Operation not permitted`. Root cause, from Substrate's own source: the actor process is always started as UID 0 / GID 0 (`ContainerSpec` has no user field), with a default capability set of only `AUDIT_WRITE`, `KILL`, `NET_BIND_SERVICE` — no `CAP_SETUID`/`CAP_SETGID`, which `su`'s own `setgroups(2)` call needs to drop privileges.

**Fix: grant the two capabilities `su` needs, explicitly.** `buildActorTemplate` (`pkg/runtime/substrate_template.go`) now sets `Container.SecurityContext.Capabilities.Add = ["SETUID", "SETGID"]` on the ActorTemplate the runtime builds, using the vendored `third_party/ateapipb` `SecurityContext`/`Capabilities` types. Substrate applies `Drop` before `Add` and rejects `"ALL"`, so each capability is named explicitly rather than assumed from a default set. `execAsUserCmd`'s `su`-based flow (pkg/sciontool/substrate/exec.go, pkg/sciontool/substrate/execuser.go) is unchanged — this only grants what it already needed, the same two capabilities Docker's own default capability set already includes (which is why this defect is Substrate-specific). These capabilities apply inside the gVisor sentry the actor runs in, not the host; `su` itself clears them (along with everything else) for the scion process tree it execs into, so nothing scion-owned ever runs privileged.

The capability set is a single package-level value (`substrateContainerCapabilitiesAdd`), shared between `buildActorTemplate` (what gets requested) and `substrateTemplateName`'s content-address hash (`pkg/runtime/substrate_template.go`): **the capabilities are now part of the template-hash inputs**, so an existing golden template built before this fix is never silently reused after it (which would otherwise mean the fix shipped in code but never actually reached a running actor) — the same class of "settings changed but the golden template didn't" risk a second-profile `egress_allow` change was found to have earlier in this project.

### (C) Investigation: does the harness/tmux run as root today?

Traced by code, file:line, the full path from `sciontool substrate-serve`'s bootstrap handler to the harness actually starting:

1. `pkg/sciontool/substrate/server.go` bootstrap handler starts `s.runInit(childArgs, false)` — the same `RunInit` the `sciontool init` CLI command uses (`cmd/sciontool/commands/substrate_serve.go`'s `WithInitRunner`).
2. `RunInit` (`cmd/sciontool/commands/init.go:159`, prior to this fix) calls `setupHostUser()` to compute the UID/GID the harness should run as.
3. `setupHostUser` (`init.go:1400-1408`): reads `/proc/self/status`'s `CapEff` (`hasCapSetUID`, `init.go:2306-2312`, checking bit 7 = `CAP_SETUID`). Without the capability — Substrate's state before this fix — it logs "Running as root but CAP_SETUID is absent (restricted sandbox)" and returns `(0, 0, rootless=true)` immediately, before ever checking anything else.
4. Back in `RunInit`, this `(targetUID=0, ...)` result is threaded into `supervisor.Config{UID: targetUID, GID: targetGID, ...}` (`init.go:~586-589`) and handed to the supervisor that actually launches the harness/tmux child process.
5. `pkg/sciontool/supervisor/supervisor.go:113-120`: `if s.config.UID > 0 && s.config.GID > 0 { s.cmd.SysProcAttr.Credential = &syscall.Credential{...} }` — the privilege-drop `Credential` is only ever attached when both are `> 0`. With `UID=0`, this is skipped entirely; the supervisor only sets `HOME`/`USER`/`LOGNAME=scion` (`supervisor.go:128`) — cosmetic env vars, not a real privilege drop. The child process inherits the current (root) credentials.

**Conclusion: yes, the harness and tmux ran as root on substrate before this fix.** This is the second defect the investigation was checking for.

**Does adding SETUID+SETGID alone fix it? Traced: no, not by itself.** With the capabilities granted, `hasCapSetUID()` (step 3) does return `true`, so `setupHostUser` no longer takes that immediate "capability absent" exit — but the very next check it reaches (`init.go:1410-1416`, prior to this fix) is `hostUID := os.Getenv("SCION_HOST_UID")` / `hostGID := os.Getenv("SCION_HOST_GID")`; if either is empty, it returns `(0, 0, rootless=false)` — still `targetUID=0`, still no `Credential` set at step 5, just a different one of `setupHostUser`'s several "stay at UID 0" exits. **Substrate's own bootstrap env never set these two variables at all**: every other runtime sets them via `buildCommonRunArgs` (`pkg/runtime/common.go:378-390`, used by `docker.go`/`podman.go`/`apple_container.go`) or an equivalent in `KubernetesRuntime.buildPod`, but Substrate's env-building path, `buildBootstrapEnv` (`pkg/runtime/substrate_bootstrap.go`), is entirely separate and never called that helper.

**Second fix, required to make the capability grant actually take effect: set `SCION_HOST_UID`/`SCION_HOST_GID` in the bootstrap env.** `buildBootstrapEnv` now sets both to `"1000"` unconditionally — matching the actor image's own baked-in `scion` user (`image-build/scion-base/Dockerfile`: `useradd -m -s /bin/zsh -u 1000 scion`, confirmed by reading the Dockerfile directly). Unlike Docker/Podman (where these normally mirror the *broker host's* own UID/GID for bind-mount permission parity) or the NFS backend (a stable, node-independent identity for a shared filesystem), Substrate's workspace is never bind-mounted from the invoking broker's own filesystem at all, so there is no host UID to synchronize with — the image's own default is the only value that makes sense here.

With both fixes in place: `hasCapSetUID()` is true, `SCION_HOST_UID`/`GID` are both `"1000"` (non-empty), and since `1000` already matches the scion user's existing, baked-in UID/GID exactly, `setupHostUser` hits its "already correct, nothing to change" shortcut (`init.go:1483-1492`) — no `usermod`/`groupmod`/`chown`-of-home-directory dance needed at all — and returns `(1000, 1000, false)`. `supervisor.Config.UID/GID` are both `1000 > 0`, so `supervisor.go:113-120`'s `Credential{Uid: 1000, Gid: 1000}` **is** set, and the harness/tmux child process actually execs as the `scion` user via a real `setuid`/`setgid` syscall pair — which needs exactly the `CAP_SETUID`/`CAP_SETGID` this fix already grants.

### Fail-closed (binding decision, folded into this same change)

Silently falling back to "run as root" — `setupHostUser`'s existing behavior, still correct and unchanged for every other runtime (rootless Podman relies on it) — is not acceptable for substrate: scion never runs the harness or exec as root. `cmd/sciontool/commands/init.go` adds `InitRunOptions.RequirePrivilegeDrop`, a flag substrate-serve's own `WithInitRunner` callback sets (`cmd/sciontool/commands/substrate_serve.go`) — deliberately a flag passed at that one call site, not an environment variable a workload could set itself. `RunInit` checks it immediately after `setupHostUser` returns:

```go
if err := requirePrivilegeDropOrFail(targetUID, opts.RequirePrivilegeDrop); err != nil {
    log.Error("%v", err)
    return 1
}
```

**Correction (round 13 review):** the paragraph originally here overstated what this change actually did. `requirePrivilegeDropOrFail(targetUID, requirePrivilegeDrop)` is a small, standalone, directly-testable function of `setupHostUser`'s own result — not a reimplementation of its branching — returning a fixed, secret-free sentinel error whenever `requirePrivilegeDrop` is set and `targetUID` is still `0`. Substrate always starts the actor as UID 0, so — unlike other runtimes, where staying at UID 0 can legitimately mean "already unprivileged" (rootless Podman/keep-id) — a substrate agent reaching `targetUID == 0` after `setupHostUser` can only mean the drop never happened. `RunInit` does return before the supervisor (and therefore the harness) is ever started — that half was true. But at this point in the branch's history, `RunInit` only `return 1`s: it does not call `os.Exit`, so substrate-serve's PID 1 does **not** exit; `server.go`'s bootstrap goroutine only logs the non-zero exit code; `/bootstrap` had already answered 200 before `RunInit` even ran; and `Run()` had already returned success once `postBootstrap` got that 200. Nothing reported the failure to the broker or the Hub. The actor was left running, empty, reporting healthy — the exact "silent failure" this change was meant to replace. See the next entry in this log for the actual fix (a synchronous `/bootstrap` precondition, plus this check kept as defence in depth with real failure reporting and a PID-1 exit).

One known, accepted limitation: `setupHostUser`'s `SCION_KEEPID_UID` (rootless Podman keep-id) branch also returns `targetUID=0` on its own *success* path, since it drops privileges via a direct `syscall.Setuid`/`Setgid` call internally rather than deferring to the supervisor's `Credential`. `requirePrivilegeDropOrFail` cannot distinguish that from a real failure by return value alone, so it would fail closed there too. This is not reachable for substrate today — that env var is Podman-specific and substrate's bootstrap never sets it — and failing closed on it would be overly conservative rather than wrong (the actual drop likely already succeeded), so it was left as a documented edge case rather than engineered around.

### Extra capabilities beyond SETUID/SETGID: one near-certain candidate, two unconfirmed

Per the explicit instruction to start with exactly `SETUID`/`SETGID` and add anything else only with a code-cited reason (preferring live proof over speculative addition), this change ships with only those two. Investigated further, for the record:

- **`CAP_CHOWN` — near-certain, not added.** Once `setupHostUser` returns `targetUID=1000` (this fix's success path), `RunInit` unconditionally calls `log.Chown(targetUID, targetGID)` on the log file (`init.go:163-166` — `if targetUID != 0`, no other condition) to hand it from root to scion. Substrate's capability set does not include `CAP_CHOWN`, and changing a file's owner to an arbitrary different UID requires it regardless of the caller's own UID once a process's capabilities are restricted (exactly the property that made `SETUID`/`SETGID` necessary for `su` in the first place — traditional "UID 0 can do anything" semantics do not apply here). Several other `os.Chown` calls exist further down the same conditional path (hub token file `init.go:731`, GitHub token/expiry `init.go:802`/`825`, resource limits file `init.go:907`, workspace ownership via `ensureWorkspaceOwnership` `init.go:1985` and `chownTreeRootOwned` `init.go:1962`, env file `init.go:2179-2180`) — same capability, same reasoning, listed for completeness rather than as separate findings. This is left out of this change only because of the explicit "start with exactly two" instruction; it is expected to surface on the very first live substrate run that reaches this code path (any agent, since the log-file chown is unconditional), and should be a fast, low-risk follow-up once confirmed live rather than a speculative addition here.
- **`CAP_DAC_OVERRIDE` / `CAP_FOWNER` — candidates only, not added, no code citation found.** These would matter if `RunInit` (still UID 0 at this point, before the supervisor's `Credential` drop takes effect on the *child*) ever writes to a file or directory after that file has already been chowned to `scion` and is no longer world/group-writable. Confirming this would require tracing every write call that follows every chown call across `init.go`'s ~2000 lines, which was not done exhaustively here — flagged for live confirmation rather than guessed at.

### Tests

- `TestBuildActorTemplate_SecurityContextGrantsExactlySetuidSetgid` (`pkg/runtime/substrate_runtime_test.go`): the built `ActorTemplate` carries exactly `Capabilities.Add = ["SETUID", "SETGID"]` and an empty `Drop`.
- `TestSubstrateTemplateName_ChangesWithCapabilitySet`: the template hash is unchanged when the capability set is unchanged, and changes when it is (using the shared package-level value directly, restored via `t.Cleanup`).
- `TestBuildBootstrapEnv_SetsHostUIDGIDForPrivilegeDrop`: `SCION_HOST_UID`/`SCION_HOST_GID` are both `"1000"` in the bootstrap env.
- `TestRequirePrivilegeDropOrFail_SubstrateFailsClosed` / `_SubstrateSucceedsWhenDropped` / `_NonSubstrateRootlessUnchanged` (`cmd/sciontool/commands/init_test.go`): the fail-closed gate fires exactly when `RequirePrivilegeDrop` is set and the drop didn't happen, succeeds when it did, and is a complete no-op when unset (the non-substrate control — every other runtime's rootless fallback is untouched).
- Full `cmd/sciontool/...` and `pkg/sciontool/...` suites re-run green (parity for every other runtime's `sciontool init` path), alongside the four required package trees.

### Gate results (this follow-up, `SCION_*` and `CLAUDE_CODE_ENABLE_TELEMETRY` unset)

- `go build ./...` — pass.
- `go vet` on `pkg/runtime/...`, `pkg/runtimebroker/...`, `pkg/agent/...`, `pkg/config/...`, `cmd/sciontool/...`, `pkg/sciontool/...` — pass, no output.
- `gofmt -l` on every changed file — clean.
- `go test -count=1` on `pkg/runtime/...`, `pkg/runtimebroker/...`, `pkg/agent/...`, `pkg/config/...`, `cmd/sciontool/...`, `pkg/sciontool/...` — all pass.
- `go test -race -count=1` on the same trees — pass, with two confirmed pre-existing, unrelated flakes: the previously-noted `pkg/runtime/cloudrun` data race, and a newly-encountered one in `pkg/sciontool/supervisor` (`TestSignalHandler_WithoutPreStopHook`) — reproduced identically on the pre-this-follow-up baseline (2 of 4 runs) with zero code from this follow-up even touching that package; not addressed here.
- `go test -count=50` on every new test — pass, no flakes.
- `golangci-lint run --new-from-rev=c3b6e821d --concurrency=1 ./...` — 0 issues.
- `make check-custom` — same pre-existing, unrelated NFS shared-dir-storage hits as before; zero hits in any file this branch touches.
- Hygiene greps — no hits introduced by this follow-up. One pre-existing, unrelated match survives in a file this follow-up otherwise touches: `cmd/sciontool/commands/init.go:758` cites `findings.md D1` (the design doc's own defect-numbering section, not a review artifact), confirmed via `git blame` to predate this follow-up and untouched by it — the same category as the already-reported `handlers.go` `N1-7` hits.

### Functions touched (this follow-up)

- `pkg/runtime/substrate_template.go`: `buildActorTemplate` (SecurityContext/Capabilities), `substrateTemplateName` (hash input), new `substrateContainerCapabilitiesAdd`.
- `pkg/runtime/substrate_bootstrap.go`: `buildBootstrapEnv` (SCION_HOST_UID/GID).
- `cmd/sciontool/commands/init.go`: `InitRunOptions` (new field), `RunInit` (the fail-closed check), new `errPrivilegeDropRequired`/`requirePrivilegeDropOrFail`.
- `cmd/sciontool/commands/substrate_serve.go`: sets `RequirePrivilegeDrop: true` at the one call site that constructs substrate-serve's `InitRunner`.
- `pkg/runtime/substrate_runtime_test.go`, `cmd/sciontool/commands/init_test.go`: new tests described above.

No changes to `pkg/sciontool/supervisor` (its own privilege-drop and rootless-fallback logic is unchanged — only its inputs, from `RunInit`, changed for substrate specifically), `execAsUserCmd`/`ExecAsUserCmd`, or any other runtime's `RunConfig`/env-building path. No `go.mod`/`go.sum` changes.

## Follow-up 2: the fail-closed gate actually fails the agent; no scion user means no start

A follow-up review found two gaps in the previous entry's fail-closed work, and a binding acceptance bar was set for the first one before this follow-up started (see "Acceptance bar" below).

### Gap 1: the gate stopped the harness but never failed the agent

As corrected above, `requirePrivilegeDropOrFail` returning 1 from `RunInit` did not fail `Run()`, did not delete the actor, and did not report anything — the actor was left running, empty, with `/bootstrap` already having answered 200. The security property held (the harness never started as root); the failure was silent.

**Acceptance bar (binding, set before this fix):** when the privilege drop cannot happen, end to end: `Run()` must return an error, the actor must be deleted the same way a bootstrap failure already deletes it, and the hub must show the agent as errored, not running. A PID-1 exit alone cannot satisfy this, because `/bootstrap` has already answered 200 by the time `RunInit` — and therefore `requirePrivilegeDropOrFail` — ever runs.

**Fix.** A new synchronous precondition, `PrivilegeDropChecker` (`pkg/sciontool/substrate/server.go`), runs inside `handleBootstrap` — after `req.Env` is applied to the process environment (so `SCION_HOST_UID`/`GID` are visible) but before the handler commits to a 200 response or starts the in-process init. Its concrete implementation, `checkPrivilegeDropFeasible` (`cmd/sciontool/commands/init.go`), lives in the cmd layer for the same reason `InitRunner` does (this package must never import `cmd/sciontool/commands`), and re-checks — cheaply, without performing any of it — the three things that can make the real realignment silently produce nothing: `CAP_SETUID`/`CAP_SETGID` effective, the `scion` user resolvable, and `SCION_HOST_UID`/`GID` present and parseable.

When it fails, `handleBootstrap` responds with a fixed, secret-free message and a non-2xx status, and never starts the init runner. This is not new plumbing: `postBootstrap` (`pkg/runtime/substrate_bootstrap.go`) already treats any non-2xx as an error, and `Run` (`pkg/runtime/substrate_runtime.go`) already runs `cleanup()` — `DeleteActorEgressPolicy` + `DeleteActor(AnyState: true)` — on any `postBootstrap` error before returning it. Wiring the check into `handleBootstrap` reuses that existing failure path exactly, so the acceptance bar reduces to a property the broker already had for every other bootstrap failure.

`requirePrivilegeDropOrFail` inside `RunInit` stays as defence in depth for the case where the precondition is somehow bypassed or wrong. It now does two things it didn't before:

- reports the failure through `statusHandler`/the Hub, the same way the git-clone failure path does (extracted into `reportPrivilegeDropFailure`, using an `agentHome` resolved by a new `resolveAgentHome` shared with `RunInit`'s normal path);
- returns a dedicated sentinel, `exitCodePrivilegeDropRequired`, that `substrate-serve`'s `InitRunner` wrapper (not `RunInit` itself, which must never call `os.Exit`) checks for specifically — not "any non-zero code" — before calling `os.Exit`, so PID 1 dies without changing behaviour for any other `RunInit` failure that also returns before the harness launches (git clone, staged secrets, pre-start hooks, ...), all of which are expected to leave the control server up for diagnosis.

Also extracted for testability: `substrateServeInitOptions` (the `InitRunOptions` substrate-serve passes to `RunInit`, so a test can assert `RequirePrivilegeDrop: true` is actually wired there) and `newSubstrateServeServer` (the exact `*substrate.Server` construction `runSubstrateServe` uses, so a test can prove the precondition is actually wired without starting an HTTP listener).

### Gap 2: an image with no `scion` user ran the harness as a raw UID with no passwd entry

With `CAP_SETUID`/`CAP_SETGID` present but no `scion` user in the image, `setupHostUser` still returned `(1000, 1000, false)` as if the drop had succeeded: the initial `user.Lookup("scion")` failure only logged; `directSetUID`'s `sed -i` substitute exits 0 whether or not it matched anything, so a missing user produced no error either; and the post-adjustment verify also only logged. `requirePrivilegeDropOrFail` then passed, and the supervisor dropped the harness to a numeric UID with no passwd entry and an inconsistent `HOME`.

**Fix, gated on `RequirePrivilegeDrop` so every other runtime's `setupHostUser` return value is byte-identical to before this change** (`setupHostUser` and the extracted `adjustScionUser` both take the flag as a parameter now):

- a `scion` user that can't be resolved at all now returns `(0, 0, false)` under the flag, instead of logging and pushing through the adjustment attempt anyway;
- `directSetUID` (split into path-parameterized `directSetUIDAt` for testability) now pre-checks that a `username:` line actually exists in the target file before running `sed`, and returns a new `errPasswdEntryNotRewritten` if not — a real, previously-impossible-to-detect failure mode, not a reimplementation of `sed`'s own logic. Whether this new error changes the caller's outcome is itself gated: under `RequirePrivilegeDrop` it fails closed; without it, the historical (bug-for-bug) "log and report success anyway" behaviour is preserved exactly, since changing it would be a behaviour change for runtimes other than substrate;
- a post-adjustment verify that doesn't show `Uid == hostUID && Gid == hostGID` (checked via a new `verifiedUserMatches` helper) now also fails closed under the flag, instead of only logging.

Every one of these fail-closed outcomes flows through the same `requirePrivilegeDropOrFail` → `reportPrivilegeDropFailure` → `exitCodePrivilegeDropRequired` path as gap 1's fix, and — because the synchronous `/bootstrap` precondition already checks "the scion user resolvable" as one of its three conditions — is expected to be caught there first in practice, the same defence-in-depth relationship as gap 1.

`scionUserLookup` and `runDirectSetUID` are now package vars (not direct calls) purely so `adjustScionUser`'s branches are unit-testable without a real `scion` user or real system files — the same reasoning `requirePrivilegeDropOrFail` already used for `setupHostUser`.

### Nits taken in the same pass

- `buildActorTemplate` now does `Add: slices.Clone(substrateContainerCapabilitiesAdd)` instead of assigning the package-level slice directly into the template proto, so a caller that ever mutated `tmpl...Capabilities.Add` in place can't corrupt the shared value future templates hash against.
- The WHY comment on the container's `SecurityContext` (and the `substrateContainerCapabilitiesAdd` var doc) now names the supervisor's own `syscall.Credential` drop (`pkg/sciontool/supervisor/supervisor.go`'s `Run`, ~lines 113-150 — `exec.Cmd` performs `setgroups`/`setgid`/`setuid` under the hood for a `Credential`-bearing child) as a primary consumer of `SETUID`/`SETGID`, alongside `su`/`execAsUserCmd` — both need both capabilities to leave root, and the comment previously named only the latter.
- A stray process-artifact reference ("ADDENDUM") in a doc comment (`cmd/sciontool/commands/init.go`) was reworded to state the requirement itself.
- Two references to this project's own defect-numbering scheme, left over from an earlier round and outside that round's own diff, are now reworded to describe what the code actually does instead of naming a finding: the port-forward/autoexpose skip comment in `init.go`, and a regression test's doc comment in `pkg/agent/substrate_delete_test.go`.

### Tests

- `pkg/sciontool/substrate/server_test.go`: `TestBootstrap_PrivilegeDropPreconditionFails_RejectsWithoutStartingInit` and `TestBootstrap_PrivilegeDropPreconditionPasses_StartsInit` drive the real `Handler()` with a fake `PrivilegeDropChecker`, proving the non-2xx-without-starting-init contract and its positive control.
- `pkg/runtime/substrate_runtime_test.go`: the existing `TestSubstrateRun_CleanupOnFailure/"bootstrap fails"` case (a generic non-2xx bootstrap response) is now annotated as this change's broker-side proof — the broker cannot distinguish a precondition rejection from any other bootstrap failure, and `postBootstrap`/`Run`'s handling of both is identical; the assertion that `DeleteActor` and `DeleteActorEgressPolicy` were both called, common to every case in that table, is exactly the acceptance bar's "actor is deleted" half.
- `cmd/sciontool/commands/init_privilege_drop_test.go` (new file): `TestCheckPrivilegeDropFeasible_*` (all five branches, fully dependency-injected — no reliance on the test machine's real capabilities/users); `TestHasCapSetGID_ParsesEffectiveCapabilities`; `TestSubstrateServeInitOptions_RequiresPrivilegeDrop`; `TestExitOnPrivilegeDropSentinel_SentinelExits` / `_OtherCodesDoNotExit` (proving this is "exactly this sentinel," not "any non-zero code"); `TestNewSubstrateServeServer_PrivilegeDropPreconditionRejectsBootstrap` (drives the real `newSubstrateServeServer()` wiring, deterministically, via unset `SCION_HOST_UID`/`GID`); `TestReportPrivilegeDropFailure_WritesPhaseErrorAndMessage`; `TestRunInit_PrivilegeDropFailure_ReturnsSentinel` (drives the real `RunInit`); `TestAdjustScionUser_*` (all three R2 fail-closed cases, each with a `RequirePrivilegeDrop: false` control proving the non-substrate return value is unchanged); `TestDirectSetUIDAt_NoEntryToRewrite_ReturnsError` / `_RewritesExistingEntry` (against temp files, never real system files).
- Mutation checks performed by hand (edit, run the targeted test, confirm failure, revert, confirm the full suite is green again — see the message to sb-em for the exact commands and output):
  - `substrateServeInitOptions`'s `RequirePrivilegeDrop: true` flipped to `false` → `TestSubstrateServeInitOptions_RequiresPrivilegeDrop` fails.
  - `newSubstrateServeServer`'s `WithPrivilegeDropChecker(...)` call removed → `TestNewSubstrateServeServer_PrivilegeDropPreconditionRejectsBootstrap` fails.
- `pkg/agent/substrate_delete_test.go`'s existing `TestSubstrateAgentManagerDelete_RecordExists*` tests are unaffected (comment-only change).

### Live negative-test recipe

Delivered separately, in the scratchpad, not in this repo: `infra/live-negative-test-recipe.md` and `infra/negative-test-patch.diff` (a one-line, never-committed throwaway patch to `substrateContainerCapabilitiesAdd` that empties the capability grant — content-addressed, so it can never collide with or reuse a real golden template). No production knob was added anywhere for this; the only way to make the drop infeasible for a live test is to actually not grant the capability.

### Gate results (this follow-up, `SCION_*` and `CLAUDE_CODE_ENABLE_TELEMETRY` unset)

- `go build ./...` — pass.
- `go vet` on `pkg/runtime/...`, `pkg/runtimebroker/...`, `pkg/agent/...`, `pkg/config/...`, `pkg/sciontool/...`, `cmd/sciontool/...` — pass, no output.
- `go test -count=1` on `pkg/runtime/...`, `pkg/runtimebroker/...`, `pkg/agent/...`, `pkg/config/...`, `pkg/sciontool/...`, `cmd/sciontool/...` — all pass. (`pkg/runtime`'s pre-existing, unrelated `TestGetRuntime*` container-auto-detection failures and `pkg/agent`/`pkg/config`/`pkg/runtimebroker`'s pre-existing, unrelated settings/env-gathering failures were confirmed identical on the unmodified base commit before relying on "pre-existing" — see the message to sb-em.)
- `go test -race -count=1` on `cmd/sciontool/commands/...` and `pkg/sciontool/substrate/...` — pass.
- `golangci-lint run --new-from-rev=c3b6e821d ./...` — 0 issues.
- `make check-custom` — same pre-existing, unrelated NFS shared-dir-storage hits as before, in files this change does not touch; zero hits in any file this change touches.
- Hygiene greps (`round [0-9]|this round|sb-rev|sb-dev|sb-em|substrate-lead|finding #|the review|D2 report` and `\b(C1|R1|R2|N[1-3]|E[1-5]|D1|D2)\b`) over every file this change touches — zero hits.

### Functions/files touched (this follow-up)

- `pkg/sciontool/substrate/server.go`: new `PrivilegeDropChecker` type, `WithPrivilegeDropChecker` option, `privilegeDropCheck` field, the precondition call in `handleBootstrap`.
- `pkg/sciontool/substrate/server_test.go`: the two new tests above.
- `cmd/sciontool/commands/init.go`: `exitCodePrivilegeDropRequired`, `privilegeDropPreconditionDeps`, `defaultPrivilegeDropPreconditionDeps`, `errPrivilegeDropPrecondition`, `checkPrivilegeDropFeasible`, `hasCapSetGID`/`parseCapSetGID` (and a shared `parseCapBit` `parseCapSetUID` now also uses), `resolveAgentHome` (extracted), `reportPrivilegeDropFailure` (extracted), `adjustScionUser` (extracted from `setupHostUser`), `verifiedUserMatches`, `scionUserLookup`/`runDirectSetUID` (new package vars), `directSetUIDAt` (path-parameterized), `errPasswdEntryNotRewritten`, `passwdFileHasEntry`; `setupHostUser`'s signature now takes `requirePrivilegeDrop bool`.
- `cmd/sciontool/commands/init_privilege_drop_test.go` (new file): all tests described above.
- `cmd/sciontool/commands/substrate_serve.go`: `substrateServeInitOptions`, `exitOnPrivilegeDropSentinel`, `substrateServePrivilegeDropChecker`, `newSubstrateServeServer` (all extracted from `runSubstrateServe`'s body).
- `cmd/sciontool/commands/substrate_serve_test.go`: the existing SIGTERM integration test now skips (with a stated reason) when the environment lacks `CAP_SETUID`/`CAP_SETGID`, and its bootstrap body now sets `SCION_HOST_UID`/`GID` — both needed so this integration test's unrelated subject (SIGTERM handling) isn't newly blocked by the precondition it was never exercising.
- `pkg/runtime/substrate_template.go`: `slices.Clone` nit, WHY comment update (both above).
- `pkg/runtime/substrate_runtime_test.go`: comment-only, annotating the existing broker-side proof.
- `pkg/agent/substrate_delete_test.go`, `cmd/sciontool/commands/init.go` (the port-forward/autoexpose comment): comment-only nit cleanup.

No changes to the delete paths, to any capability beyond what was already granted, or to any other runtime's behaviour.

## Follow-up 3: hermeticity incident, full capability verification, generalized fail-loud

### Incident: a test wrote this branch's own real agent status to the real Hub

`TestRunInit_PrivilegeDropFailure_ReturnsSentinel` drove the real `RunInit`.
`setupHostUser`'s rootless branch and `resolveAgentHome` both fall back to a
real `user.Lookup("scion")`, ignoring `$HOME`, whenever the process isn't
UID 0. This dev/test container's actual system user happens to be named
"scion", so that real lookup succeeded and the test wrote
`agent-info.json` with `phase: "error"` to the container's own, real
`/home/scion` — not the test's `t.TempDir()`. Something outside the test
process (this container's own agent supervision, reading that file) relayed
the contamination to the real Hub, which put this branch's own agent into
phase ERROR and rejected inbound messages for about 35 minutes. It
recurred once more, self-inflicted, while isolating the mechanism (see the
mutation check below).

**Fix**, two independent layers, both required (per a binding instruction
from substrate-lead: tests must be unable to reach a real Hub or write real
agent state even when the ambient environment has real credentials/UIDs
set):

- Every "scion"/by-UID lookup in `cmd/sciontool/commands`
  (`setupHostUser`'s rootless and keep-id branches, `resolveAgentHome`,
  `gitCloneWorkspace`'s fallback) now goes through the existing
  `scionUserLookup` package var plus a new `lookupUserByID` var, instead of
  calling `user.Lookup`/`user.LookupId` directly. No production behaviour
  change (the vars default to the real functions).
- A new `TestMain` in `cmd/sciontool/commands` (mirrored, lighter, in
  `pkg/sciontool/substrate`) clears every Hub/token/agent-identity env var
  plus `SCION_HOST_UID/GID/SCION_KEEPID_UID` for the whole test binary, and
  defaults `scionUserLookup`/`lookupUserByID` to "not found" for the whole
  run — a single test's `t.Setenv` only protects that one test.

**Proof:** ran the full `cmd/sciontool/commands` suite with the real,
completely unscrubbed container environment (39 real `SCION_*` vars,
including the real `SCION_HUB_ENDPOINT`) — real `agent-info.json` content
hash unchanged. Ran it again with `SCION_HUB_ENDPOINT`/`SCION_HUB_URL`
pointed at a local listener that logs any hit and fails, plus
`SCION_AUTH_TOKEN`/`SCION_AGENT_ID` set to real-looking values — zero hits.
**Mutation check:** removed the `scionUserLookup`/`lookupUserByID`
override from `TestMain` (env-clearing left in place) and re-ran the same
test — it reproduced the incident exactly, confirming which half of the
fix is load-bearing. Reverted; the real file was manually repaired
afterward (`phase` back to `"running"`, stray `detail` cleared).

### (A): PrivilegeDropChecker now verifies the full capability set

Previously checked only `SETUID`/`SETGID`. New leaf package
`pkg/substratecaps` (no dependencies beyond the standard library, so both
`pkg/runtime` and `cmd/sciontool/commands` can import it without an import
cycle or pulling in the other's dependencies) is the single shared
definition — name, `CapEff` bit, and a cited reason per capability.
`buildActorTemplate` and `checkPrivilegeDropFeasible` both derive from it
now, so they cannot drift apart. Wired with `[SETUID, SETGID, CHOWN]`;
`CHOWN` is proven live at 017adc1b5 ("Failed to chown log file... operation
not permitted", "Failed to chown workspace... operation not permitted",
then "Git clone failed: git init failed").

### (B): any non-zero in-process init exit fails loud, not only the sentinel

`exitOnPrivilegeDropSentinel` → `exitOnNonZeroInit`: substrate-serve's
`InitRunner` wrapper now calls `os.Exit` for any non-zero `RunInit` exit
code, not just the privilege-drop sentinel — proven necessary by the same
017adc1b5 log line ("in-process init exited with code 1" from the
git-clone failure, which already reported `PhaseError` but never made
PID 1 exit). Exit code 0 is the one deliberate exception (RunInit's own
definition of "nothing went wrong"); `RunInit`'s existing crash/limits-
exceeded classification (`classifyExit`) is untouched. `reportInitFailure`
(renamed from `reportPrivilegeDropFailure`) is now also called from every
`RunInit` early-return path that previously had no reporting at all
(staged secrets decode/write, harness manifest parse, invalid harness env
overlay/telemetry marker, immediate child-start failure, a supervisor-level
error).

### Gates (`SCION_*` and `CLAUDE_CODE_ENABLE_TELEMETRY` unset)

`go build ./...`, `go vet ./...` clean; `go test -count=1` on
`pkg/runtime/...`, `pkg/runtimebroker/...`, `pkg/agent/...`,
`pkg/config/...`, `pkg/sciontool/...`, `cmd/sciontool/...`,
`pkg/substratecaps/...` all `ok`; `go test -race -count=1` on
`cmd/sciontool/commands/...`, `pkg/sciontool/substrate/...`,
`pkg/substratecaps/...` all `ok`; `golangci-lint run
--new-from-rev=c3b6e821d ./...` 0 issues; `make check-custom` same
pre-existing unrelated hits, zero in touched files; hygiene greps zero hits
across every touched file. Checked for stray `go test`/`.test` processes
before and after — none found.

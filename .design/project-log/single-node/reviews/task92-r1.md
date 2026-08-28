# task #92 — CLOSE-OUT (R3) — `1c22442` → `dc729e2` — **APPROVE**

Scoped confirm only (4 files, +19/−67; parent `1c22442` verified; `handlers.go` untouched — Shape B
still held). All four points measured, tree clean @ `dc729e2`:

1. **RevertGuard deleted** (0 occurrences). R1's `EffectiveSettings` remains the genuine guard —
   re-measured RED under reverted fix (`ActiveProfile=local`, `ResolveRuntime("")=docker`). No hole.
2. **O5 hole closed (the load-bearing check).** `TestDefaultSandboxBin_MatchesLiteral` added; re-ran the
   `/bogus` mutation on config's `defaultSandboxBin` → now **RED** (was green in R2). The test name is
   truthful.
3. **Comments now accurate.** init.go: `TestDefaultSandboxBin_MatchesLiteral` pins the config copy,
   `TestSandboxBinConstantSync_Task92` pins the runtime copy — both true, each measured red on its own
   side. sandbox_bin_sync_test.go now correctly states config's const is path-independent under the
   mocked seam and the internal assertion is what catches config-side drift. (Micro-nit, non-blocking:
   that comment says "the internal assertion below" but the assertion lives in init_test.go, not below
   in this file — substantively true, trivial cross-file wording.)
4. **Narration removed without losing measurement.** The 4 `CONFIRMED` t.Log lines are gone; the
   assertions they sat beside are unchanged (diff is t.Log-only) and `FallbackFires` stays green.

Gates: full `pkg/config` + `pkg/runtimebroker` suites green; both new O5 tests pass; all mutations
reverted, nothing committed/pushed. **APPROVE at `dc729e2`.** Remaining wait is ptone's product call on
the shared filter — not a code matter.

---

# task #92 — DELTA Review (R2) — `54cc98b` → `1c22442`

Additive commit `1c22442` (parent = `54cc98b5`, verified). 6 files, +348/−8, matches the briefed list;
`pkg/runtimebroker/handlers.go` untouched (Shape B still withheld — verified). Everything below is
**measured**, mutations reverted, tree clean.

## R2 Verdict: **REQUEST CHANGES** — one blocking item (a false guard). R1 is discharged.

### LEAD — O4 `RevertGuard` is a false guard. Your suspicion is correct, by measurement.
Simulated a reverted fix (disabled the template branch in `init.go`, seam kept) and reran both:

| Test | Under reverted fix |
|---|---|
| `TestInitMachine_CloudRunSandbox_EffectiveSettings_Task92` (R1) | **RED** — `ActiveProfile="local" want "default"`; `ResolveRuntime("")="docker" want "cloudrun-sandbox"` |
| `TestInitMachine_CloudRunSandbox_RevertGuard_Task92` (O4) | **STILL GREEN** |

`RevertGuard` sets `CLOUD_RUN_INSTANCE=""` **and** `sandboxBinExists=false` — a **non-Cloud-Run**
environment, where the fix branch is a no-op whether present or reverted, so its `docker` assertion
holds unconditionally. Its comment ("simulates what happens if the fix is reverted") and its
`t.Log("CONFIRMED: without the fix, empty selection resolves to docker — the bug")` are **false**: it
does not exercise the reverted fix, and it has never been observed red under the condition it names
(Rule 4: a negative assertion is not a pin until observed positive; Rule 12: an instrument that only
passes is not proven). This **reintroduces the exact O4 defect** — a guard that survives the revert it
claims to catch. It also duplicates `..._WithoutSandboxBin_FallsBack` and `..._NonCloudRun_...`, which
already assert docker/local in non-sandbox envs.

It cannot be salvaged into a real revert guard by tweaking assertions: with detection off, fix-present
and fix-reverted are indistinguishable. The genuine revert guard is R1's `EffectiveSettings` test,
which asserts the *positive* fixed outcome with detection *on* and goes red on revert (measured above).

**Required:** delete `TestInitMachine_CloudRunSandbox_RevertGuard_Task92` (or at minimum strip its
name/comment/`t.Log` of every revert-guard claim). This is a cheap fix and it does **not** re-open a
coverage hole — the substantive protection (R1) is present and working. The block is narrowly that a
test actively claims protection it does not provide, which is the anti-pattern this project is trying
to stop.

### R1 — DISCHARGED.
`TestInitMachine_CloudRunSandbox_EffectiveSettings_Task92` runs **real** `InitMachine` (sandbox env) →
**real** `LoadEffectiveSettings` → asserts the post-merge effective state: `ActiveProfile=="default"`
**and** `ResolveRuntime("")=="cloudrun-sandbox"` — the koanf scalar-overwrite I measured in R1. Verified
RED under reverted fix (above). This is exactly the end-to-end pin R1 demanded. (Immaterial: it passes
`LoadEffectiveSettings(globalDir)` where `buildInfoProfiles` uses `("")`; identical here because
`projectPath==globalDir` short-circuits the project-tier load. Not a finding.)

### O3 — sufficient (concur with your lean).
`SeedsCorrectProfile` now splits its comment into LOAD-BEARING vs SEED-LAYER-ONLY assertions, and R1's
`EffectiveSettings` test independently *enforces* the determining state. Comment-as-documentation is
fine because enforcement lives in R1, not the comment. No split needed.

### O5 — right shape, but the sync test's comment makes a false claim (measured).
Exporting `runtime.DefaultSandboxBin` + an external-package equality test is what I meant. But it guards
only **one** direction. Measured: changing config's `defaultSandboxBin` to `/bogus/drifted/path` leaves
the **entire `pkg/config` suite green** — because the `InitMachine` pin tests mock the `sandboxBinExists`
seam (path-independent), config's constant is pinned by nothing. The sync-test comment's claim "if
init.go's copy changes, the InitMachine pin tests fail" is **untrue**. *Fix (Optional, ~1 line):* add an
internal assertion in package `config`: `if defaultSandboxBin != "/usr/local/gcp/bin/sandbox" { … }`.
Non-blocking, but correct the false comment.

### handlers_test.go restoration — FAITHFUL, by measurement (not memory).
`TestBuildInfoProfiles_FallbackFires_Task92` re-executes the three facts + the len==0 fallback. All pass;
each is load-bearing under mutation:
- fallback `Name: "default"→"fallback"` → FALLBACK assertion RED.
- local-only filter neutered (`if false`) → FACT 1 RED (`got 2`).
- FACT 2 is a direct call to `isLocalOnlyRuntime` (docker=true, kubernetes=false) — measured, not asserted from memory.

Cosmetic (non-blocking, also in RevertGuard): the `t.Log("...CONFIRMED")` lines print even after a failing
`t.Errorf` (Errorf doesn't halt), so a log scan shows "CONFIRMED" on a failing run. Harmless to the
verdict; worth tidying if the file is touched again.

### R2 gates
`go build`/`go vet` on `pkg/config` + `pkg/runtime` + `pkg/runtimebroker`: clean. Full `go test` on all
three: green. Mutations this round (all reverted, tree clean): fix-disabled → R1 red / RevertGuard green;
config const bogus → pkg/config green (O5 gap); fallback name → FALLBACK red; filter neutered → FACT 1
red. Not run: full-repo `go test ./...` (sparse) and frontend TS build (browser harness excluded by you).

**R2 bottom line:** delete/strip the false `RevertGuard` test and the delta is landable — R1 is
genuinely discharged, O3 sufficient, the restored instrument faithful, O5 non-blocking (fix the false
comment). Your instinct not to trust that last clause by reading was correct.

---

# task #92 — `scion/task-92-runtime-profile-fix` @ `54cc98b` — Review (R1)

## Executive Summary
Risk: **LOW–MEDIUM**. The load-bearing claim behind the architect's reversal — that an empty
"Use broker default" selection resolves to `cloudrun-sandbox` on a fresh deploy — **holds, and I
established it by executing both of the two links that had only ever been traced**, not by re-reading
the trace. The code is correct. The gap is in the tests: the exact behavior that broke twice before
has **no committed regression guard**; every committed pin hardcodes YAML or asserts a seed-file
layer the merge discards.

---

## §1 — The empty-selection question (leads the report, per brief)

**VERDICT ON THE REVERSAL: the reason holds.** The architect's stated reason for accepting a
length-2 / auto-select-silent fix — "the *empty* state is correct here where it was not before" — is
true on a fresh deploy. I did not accept the four-function trace as structural; I ran the two links
the developer admitted were never executed.

**Link (b) — broker picks up the seeded file — EXECUTED.**
Ran the real `InitMachine` (Cloud Run Instance + sandbox-bin present) → real
`config.LoadEffectiveSettings("")` (the identical call `buildInfoProfiles` and `resolveManagerForOpts`
make) → real `vs.ResolveRuntime("")`:

```
EFFECTIVE ActiveProfile   = "default"                       # global seed overrides embedded "local"
EFFECTIVE profiles (merged) = [local->docker  remote->kubernetes  default->cloudrun-sandbox]
EFFECTIVE runtimes (merged) = [podman container kubernetes cloudrun-sandbox docker]
ResolveRuntime("")        -> cloudrun-sandbox               # empty selection resolves correctly
```

This confirms the developer's own disclosure (three profiles post-merge) AND the load-bearing fact
the disclosure glosses: koanf loads embedded defaults *first* (lowest priority) and the seeded global
file *after*, so the scalar `active_profile` is **overwritten to `default`** while the `profiles` map
**merges** to three. Mutation-verified below.

**Link (a) — hub sends empty on a fresh deploy — EXECUTED.**
Ran `applyProjectDefaults` (the hub function at `pkg/hub/project_settings_handlers.go:489`, containing
line 533) against three project shapes:

```
empty-annotations         -> ac.Profile = ""       # fresh deploy: hub sends empty
other-annotations-present -> ac.Profile = ""       # other keys but no active-profile: still empty
annotation-set-to-remote  -> ac.Profile = "remote" # ONLY when scion.io/active-profile is set
```

The hub injects a profile **only** when the `scion.io/active-profile` annotation exists
(`project.Annotations[...]` at line 332). A fresh deploy has no such annotation, so `ac.Profile`
stays `""` and the broker's empty→`ActiveProfile` fallback governs. **Both previously-structural
links are now measured.** The weakest link the architect flagged (does the hub actually send empty?)
is settled.

**Full resolved chain, both endpoints executed:**
`agent-create.ts:611` (list length 2 → `profile=''`) → `:887` (`if (this.profile)` falsy → `body.profile`
omitted) → hub `applyProjectDefaults` leaves `''` **[executed]** → broker `handlers.go:2086`
`profileName = settings.ActiveProfile` → `resolveManagerForOpts:2603` `ResolveRuntime("")` →
`cloudrun-sandbox` **[executed]**. **§1 is unblocked.**

---

## Critical
None.

## Required

**R1 — The load-bearing path has no committed regression guard.**
The behavior that determines the fix — *the merge preserves `active_profile=default`, and an empty
selection resolves to `cloudrun-sandbox`* — is covered by **none** of the three committed pins:

- `TestInitMachine_CloudRunSandbox_SeedsCorrectProfile` asserts the **seeded file** (pre-merge).
- `TestBuildInfoProfiles_CloudRunSandbox_Task92` and `..._OldWorkstationDefaults_..._Regression`
  both **hand-write inline YAML** into `.scion/settings.yaml` and call `buildInfoProfiles` — they
  never run `InitMachine`, and they don't assert `ActiveProfile` or `ResolveRuntime("")`.

So the single most surprising and most load-bearing link — koanf overwriting the scalar
`active_profile` to `default` while merging the profile map — is asserted by nothing. It works today
(I executed it), but a future change to merge order, embedded defaults, or the template's
`active_profile` key would pass all committed tests and silently re-block §1. This is precisely the
property the architect rejected twice; it deserves a pin.

*Suggested fix:* add one end-to-end test that seeds via `InitMachine` (Cloud Run sandbox env) and then
asserts against `LoadEffectiveSettings("")`: `vs.ActiveProfile == "default"` **and**
`vs.ResolveRuntime("") == cloudrun-sandbox`. That is a ~15-line test and it is the exact invariant the
fix rests on. (It can land in the follow-up delta the architect described, but it should not land
*without* it.)

## Nit / Optional

**O1 — Item 1 fallback: reachable, but only as the *sibling* tier, and pre-existing.**
`isCloudRunSandboxEnvironment()` = `CLOUD_RUN_INSTANCE` set AND `sandboxBinExists(...)`. Its
complement (Instance set, launcher absent) is a **real, codebase-recognized state**:
`GetRuntime` (`pkg/runtime/factory.go:96-103`) routes exactly that case to the `cloudrun-instances`
runtime. On that tier the fix falls back to docker defaults and the identical defect persists (list
collapses to `remote/kubernetes`, auto-selected). **But that is a different tier from the one task #92
targets, and it was already broken before this change.** On the `cloudrun-sandbox` tier itself the
fallback is effectively *unreachable*: the tier is *defined* by the launcher's presence, and
`init.go`'s `sandboxBinExists(defaultSandboxBin)` is byte-identical `os.Stat` logic to
`runtime.SandboxLauncherAvailable()` — the same probe that selects the tier chooses the seed. So this
is **not a hole in this tier's fix**; it is a pre-existing, untouched defect in a sibling tier.
*Recommend:* file a separate task for the `cloudrun-instances` tier rather than widening #92.

**O2 — The correctness rests on the broker's empty→`ActiveProfile` fallback, which the hub can
pre-empt.** The `annotation-set-to-remote` probe above shows that a `scion.io/active-profile=remote`
project annotation makes the hub send `remote` → `kubernetes` → re-broken. `ResolveRuntime("remote")`
*succeeds* (remote survives the merge), so the "degrades gracefully / returns default manager on
error" guard in `resolveManagerForOpts` does **not** catch a known-but-wrong profile — only an unknown
one. On a fresh deploy the annotation is absent (executed), so this is fine today. This is the
"reliance on an error path" the architect referenced; the planned second commit that removes the
reliance is well-motivated. FYI/no action on this branch.

**O3 — Item 2, pins on a non-determining layer.** `TestInitMachine_CloudRunSandbox_SeedsCorrectProfile`
is a *mix*: its `active_profile=="default"` and `profiles["default"].runtime=="cloudrun-sandbox"`
assertions pin state that **survives the merge and determines behavior** (executed + mutation-verified).
Its `len(profiles)==1` / "no local" / "no remote" / "no kubernetes runtime" assertions pin the
**seed-file layer only** — the effective config has 3 profiles (incl. local+remote) and a kubernetes
runtime. Not wrong, but a reader could mistake "exactly 1 profile / no remote" for the effective/UI
state, which is 2 profiles including remote. Folding R1's end-to-end assertions in, or a one-line
comment marking the layer, resolves this.

**O4 — Item 3, the regression test is a filter-documentation test, not a fix guard.**
`TestBuildInfoProfiles_OldWorkstationDefaults_Task92_Regression` passes and correctly documents the
symptom (old defaults → only `remote/kubernetes` survives the local-only filter). But it hardcodes the
broken YAML and never touches `init.go`; **it would keep passing even if the fix were fully reverted.**
It guards the *filter*, not the *seed*. The seed guard is the `InitMachine` test. Worth naming so it is
not credited as protecting the fix.

**O5 — Item 4, `defaultSandboxBin` duplication: acceptable at this size, but nothing catches drift.**
The constant is duplicated across the `config`→`runtime` cycle boundary (`init.go` and
`cloudrun_sandbox_runtime.go:39`), both unexported, with a sync comment. A dedicated constants package
for one platform path is over-engineering. But if the two drift, `init.go` would seed a
`cloudrun-sandbox` profile while `GetRuntime` picks a different runtime — a silent mismatch. *Recommend
(cheap, optional):* export `runtime.DefaultSandboxBin` and add one equality assertion in a package that
already imports both (e.g. an e2e/integration test), rather than a new package. Your call to accept
as-is is defensible.

## Positive Feedback
The pin tests assert the profile **list**, not just a selected value — the right instinct for a bug
that is "wrong single entry." The `sandboxBinExists` var seam is a clean, idiomatic test hook. The
detection condition mirrors `GetRuntime`'s exactly, so the seed and the runtime choice cannot disagree
today. The fix is genuinely tier-scoped: no change to embedded defaults, no `deploy.sh` touch, no
effect on docker/podman/kubernetes users — verified by `TestInitMachine_NonCloudRun_...` and by the
full `pkg/config` + `pkg/runtimebroker` suites passing.

## Test Coverage
Committed task-92 tests all pass. Coverage gap is **R1**: the seed→merge→resolve path is executed
by my throwaway probes but pinned by no committed test. Mutation results (each mutation reverted; tree
clean, nothing committed/pushed):

| Mutation | Expected | Result |
|---|---|---|
| template `active_profile: default`→`local` | `SeedsCorrectProfile` red | RED — `active_profile = "local", want "default"` ✓ |
| template `default.runtime: cloudrun-sandbox`→`docker` | `SeedsCorrectProfile` red | RED — `runtime = "docker", want "cloudrun-sandbox"` ✓ |
| `isCloudRunSandboxEnvironment()`→`false` (disable fix) | `SeedsCorrectProfile` red | RED — `active_profile="local"`, profiles `[local remote]` ✓ |

Rule 18 note: the seed pins are not input-dependent in a way a per-location matrix would separate;
each mutation cleanly aborts the single assertion it targets. I did not manufacture off-diagonal
separation. Rule 12: the clean case is exercised (all pins green pre-mutation, red post-mutation for
the specific asserted reason).

## Backward Compatibility
None affected. New embedded template file; new gated branch in `InitMachine` reachable only under
`SkipRuntimeCheck && CLOUD_RUN_INSTANCE && sandbox-bin`. Docker/podman/kubernetes/non-Cloud-Run paths
byte-unchanged (the else-branch is the prior code, relocated).

## What in the brief is wrong
1. **Item 2 overstates the pin's uselessness.** The brief says the `InitMachine` pin "asserts a state
   that does not survive into the state the system actually uses." Half true: the `len==1`/no-remote
   assertions don't survive, but `active_profile==default` and `default→cloudrun-sandbox` **do** survive
   and **do** determine behavior (executed). The precise defect is the *missing end-to-end pin* (R1),
   not that this pin asserts nothing determining.
2. **Item 1's "hole in the shape of its own bug" is scoped wrong.** The fallback is reachable only as
   the *distinct* `cloudrun-instances` tier, not on the `cloudrun-sandbox` tier #92 targets — where the
   same `os.Stat` probe that selects the tier also selects the seed, making the fallback unreachable. It
   is a pre-existing sibling-tier defect, not a hole in this fix (O1).
3. **Your own trace (in the message) is correct** — I verified both unrun links by execution. The link
   you feared most (hub sends empty) is now measured, not structural.
4. **Your reversal reason is right but incompletely stated.** "The empty state is correct here" holds
   *conditionally*: it requires the broker's empty→`ActiveProfile` fallback to be intact AND no
   `scion.io/active-profile` annotation to pre-empt it (O2). On a fresh deploy both hold. Your instinct
   to ADD the dominant fix shape — removing the reliance on that fallback — is the right call and is not
   in tension with landing 54cc98b.

## Final Verdict
**REQUEST CHANGES** — single blocking item **R1** (missing regression guard for the load-bearing
seed→merge→resolve path). The code is correct and §1 is unblocked by execution; the block is purely
the absent pin on the twice-rejected behavior. R1 is ~15 lines and may land in the follow-up delta,
but 54cc98b should not land without it. O1–O5 are non-blocking.

Gates run (in `/tmp/adcrev2`, private clone): `go build ./pkg/config/... ./pkg/runtimebroker/...
./pkg/hub/...` OK; `go vet ./pkg/config/ ./pkg/runtimebroker/` OK; full `go test ./pkg/config/` and
`./pkg/runtimebroker/` OK; four committed task-92 tests pass; three mutations red for the right
reason; two trace-link probes executed then deleted (tree clean — nothing committed, nothing pushed).
Not run: full-repo `go test ./...` (sparse-checkout / disproportionate) and frontend TS build (the
`agent-create.ts` links were read, not executed — the architect explicitly excluded a browser harness).

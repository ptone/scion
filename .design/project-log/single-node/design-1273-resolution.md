# Design: holistic fix for resource-name resolution (`ptone/scion#1273` and its successors)

Author: sn-impl-arch (architect). Date: 2026-08-27.
Directed by ptone 13:03: *"we should address 1273 more holistically - not as a stopgap"*.

Source survey performed against upstream main `3aeb7729`. Every file:line below was read, not inferred.

---

## 1. Problem & Goals

### 1.1 The presenting symptom

Agent create returns **HTTP 502**:

```
runtime_error: failed to find harness-config "antigravity": harness-config "antigravity" not found
```

The operator never typed `antigravity`. The error names a resource they have never heard of, produced
~700 lines into agent provisioning, on a code path they did not choose.

### 1.2 Why this is not a stopgap candidate

`ptone/scion#1273` is **closed**. Upstream `GoogleCloudPlatform/scion#1305` (`fc523ecd`) landed the
template half and closed it. **The defect did not close — it mutated.** Before `#1305` the dispatched
harness name was *empty*; `#1305` made template resolution work, so the default template's harness
field now yields a *real* name, `antigravity`, which is not registered on a fresh hosted deployment.

**Symptom identity is not cause identity.** A fix aimed at the string in the error message would be
the third pass over the same defect. Register entries #37/#48 must be rewritten, not closed.

### 1.3 The actual defect — three structural faults, not one bug

**Fault 1 — the hub and the broker load different config sources.**
`pkg/config/hub_config.go:1122-1165` `LoadBootstrapKoanf` (hub) **does not read
`embeds/default_settings.yaml`**. `pkg/config/settings_v1.go:1071` `LoadVersionedSettings`
(broker/agent) **unconditionally loads it first**. So the hub can hold an empty
`DefaultHarnessConfig` while every broker process in the same deployment holds `"antigravity"`.
Two sides of one dispatch disagree about what the default is. This is what makes an empty
dispatched name possible at all.

**Fault 2 — an empty name is silently indistinguishable from a satisfied one.**
`pkg/hub/handlers_agent_create_helpers.go:253`:

```go
if hcName != "" && agent.AppliedConfig.HarnessConfigID == "" {
    // lookup (project scope, then global), ID/hash stamping,
    // auto-no-auth fallback, and every log line live in here
}
```

When `hcName == ""` the hub skips the lookup, the stamping, **and all logging**. No warning, no
error, no trace. And when `hcName != ""` but both scope lookups miss, it logs and **still proceeds**.
There is no configuration in which this function fails a create.

**Fault 3 — nothing validates the deployment's own defaults, ever.**
No code at startup, or at any point before the first agent-create, checks that the configured
`default_template` and `default_harness_config` actually resolve. Verified absent across
`cmd/server_foreground.go`, `pkg/hub/resource_bootstrap.go`, `pkg/config/hub_config.go`,
`pkg/hub/admin_validate.go`. A deployment can be born broken and report itself healthy.

Downstream of all three sits an **authority violation**: because the hub sent no name, `pkg/agent`
invents one from *its own* settings chain (`pkg/config/resolve_harness_config.go:48`, rung 7 of 7,
source `"settings-default"`) and searches the local disk for it
(`pkg/config/harness_config.go:100-149`). In hosted mode that disk is deliberately never populated —
`cmd/server_foreground.go:100-117` skips `~/.scion` materialization with the comment *"Hosted mode
bootstraps directly into the Hub via BootstrapBundledResources, bypassing local ~/.scion
materialization."* So the fallback is **guaranteed** to fail in hosted mode, by design, silently.

### 1.4 Two mechanisms, one error string

The live 4-cell matrix (task #68) cannot distinguish these, and **a fix for either one alone leaves
the other live**:

| | Mechanism A — empty name | Mechanism B — unresolvable name |
|---|---|---|
| Hub sends | `HarnessConfig: ""`, no ID/hash | `"antigravity"`, no ID/hash (both scope lookups missed) |
| Cause | Fault 1 + Fault 2 | seeding did not run |
| Name invented by | `pkg/agent` rung 7 | hub (`agent_defaults` / project annotation) |
| Fix needed | hub must stop emitting empty; broker must stop inventing | seed must actually run, and be verified |

### 1.5 Why seeding does not save us

`antigravity` **is** bundled (`harnesses/embed.go:24`) and **is** enumerated for seeding
(`resources/catalog.go`). Adding it to a catalog would be a no-op. It is absent because of *bootstrap
conditions* in `pkg/hub/resource_bootstrap.go:37` — seeding is skipped entirely (warn only, no error)
when `GetStorage() == nil`, and `SkipIfAnyExist: true` means that if **any** active harness-config
already exists, **all** harness-config seeding is skipped. A partially-provisioned hub never gains
`antigravity` and never says so.

### 1.6 Goals

- **G1** — A deployment whose configured defaults cannot resolve says so **at startup**, not at an
  operator's first agent create.
- **G2** — No resolution outcome is silent. Every resolution records what was asked for, what was
  chosen, and from which source.
- **G3** — The hub is the sole authority on resource identity. The broker consumes what it is given
  and never invents a name.
- **G4** — Operator-facing errors name something the operator can act on.
- **G5** — Both mechanisms A and B are closed, and a regression in either is caught by a test that
  crosses the hub/broker seam.
- **G6** — The documented workaround in `docs-site/.../cloud-run.md` becomes removable.

## 2. Non-Goals

- Redesigning the 7-rung resolution ladder in `resolve_harness_config.go`. The ladder is sound; the
  faults are in who runs it and what happens when it comes up empty.
- Changing which harness is the product-wide default. `antigravity` vs `claude` is a **product
  decision, not a correctness one**, and swapping it is precisely the stopgap ptone rejected.
- Fixing the scheduler's missing template stamping (`pkg/hub/server.go:3236`). Same shape, different
  trigger — logged in §8 as a follow-up, deliberately out of scope.
- Any change to runtime implementations. §3.1 establishes this is runtime-agnostic.
- Object-storage or seeding architecture. We surface bootstrap failure; we do not redesign bootstrap.

## 3. Proposed Design

### 3.1 Establishing scope: this is product-wide

The issue's claim *"not specific to any runtime"* is **confirmed**. `pkg/agent/provision.go:338-360`
resolves the harness-config and fails *before* any `runtime.Runtime` method is touched;
`pkg/agent/manager.go:68-80` holds the runtime as an opaque interface. Packages implicated:
`pkg/hub`, `pkg/hub/httpdispatcher`, `pkg/runtimebroker`, `pkg/agent`, `pkg/config`. **None is
runtime-specific.** The single-node tier is where we *found* it, not where it lives. This design must
not be scoped to the tier.

### 3.2 Four principles

- **P1 — One authority.** The hub resolves resource identity. The broker consumes ID/hash. A broker
  that receives no identity raises; it does not search.
- **P2 — Resolve or fail, never skip.** Every resolution attempt has an explicit, recorded outcome.
  "Not requested" and "requested and not found" must never share a code path.
- **P3 — Validate at startup, not at first use.** Configured defaults are checked when the hub knows
  them and the store is live.
- **P4 — Errors name the operator's vocabulary.** An error naming a resource the operator never typed
  must also name the setting that introduced it.

### 3.3 Startup validation (P3) — the highest-value single change

Placement: immediately after `BootstrapBundledResources` in the hosted branch at
`cmd/server_foreground.go:1848-1870`, where storage and store are both live and the seed has just run.

```go
// Illustrative only.
type DefaultsReport struct {
    Template      ResolutionOutcome
    HarnessConfig ResolutionOutcome
    SeedSkipped   bool   // GetStorage()==nil or SkipIfAnyExist short-circuit
    SeedSkipReason string
}

func ValidateConfiguredDefaults(ctx, store, settings) DefaultsReport
```

Behaviour, and the load-bearing decision here is **warn, do not refuse to boot**:

- Each default resolves → INFO, one line each, with the source rung.
- A default does not resolve → **ERROR log naming the setting, the value, and the remedy**, and the
  outcome is recorded on the health/admin surface.
- Seeding was skipped → **ERROR naming which condition** (`GetStorage() == nil` vs `SkipIfAnyExist`).
  Today this is a warning nobody reads; it is the actual root cause of mechanism B.

A hub that refuses to boot on an unresolvable default converts a degraded deployment into an outage,
and on this tier a boot failure loses all state (see `.design/hosted/cloud-run-single-node.md` §5).
**Refusing to boot is the wrong trade.** But the operator must be told at deploy time, once, loudly.

### 3.4 Unify the config source (P1, closes Fault 1)

`LoadBootstrapKoanf` must load `embeds/default_settings.yaml` as its base layer, exactly as
`LoadVersionedSettings` does, before `SCION_SEED_*`, `~/.scion/settings.yaml` and `SCION_SERVER_*`.

This is the **highest-blast-radius change in the design** and needs the most review: it gives the hub
non-empty defaults it did not previously have, which changes behaviour on every deployment, not just
broken ones. It must land alone, in its own commit, with the diff of *effective* hub settings before
and after stated explicitly in the PR body.

Consequence, and it is the point: the hub now holds `default_harness_config: antigravity`, so
mechanism A stops producing an empty name — it produces a name the hub can *check*, which routes it
into §3.5 where it fails loudly instead of silently.

### 3.5 Make the empty path explicit (P2, closes Fault 2)

Restructure `handlers_agent_create_helpers.go:230-253` so the outcome is always recorded:

```go
// Illustrative only.
type ResolutionOutcome struct {
    Requested string   // what was asked for ("" if nothing)
    Source    string   // request | project-annotation | template | hub-default | settings-default
    Resolved  bool
    ID, Hash  string
}
```

- Empty after every rung → outcome `{Requested: "", Resolved: false}`, logged at WARN **with the
  ladder rungs tried**. Today this is total silence.
- Requested but not found → unchanged in severity (WARN when from an annotation or hub default) but
  the outcome travels with the dispatch instead of being dropped.
- Resolved → stamped as today.

The outcome rides on the create request (`pkg/hub/httpdispatcher.go:492-509` `buildCreateRequest`)
so the broker can distinguish "hub deliberately sent nothing" from "hub tried and failed".

### 3.6 The broker stops inventing names (P1, closes the authority violation)

`pkg/agent/provision.go:739-758` currently calls `ResolveHarnessConfigName`, reaching rung 7 and
inventing `antigravity` from its own embedded settings. Under P1 the broker must not do this when
operating under hub authority.

**This is the riskiest change and must land last, gated.** In workstation/local mode the disk search
is correct and expected — `~/.scion/harness-configs/` *is* materialized there
(`pkg/config/materialize.go:47-63`). The change is therefore conditional on hosted/hub-dispatched
mode, not global. When the hub sent no identity and no name, the broker fails immediately with an
error naming the hub-side setting (P4), rather than searching a directory that hosted mode
deliberately never creates.

### 3.7 Error message (P4)

Present:
```
failed to find harness-config "antigravity": harness-config "antigravity" not found
```
Target shape:
```
failed to resolve harness-config "antigravity" (from hub setting default_harness_config):
not registered on this hub. Registered: claude, gemini, codex.
Remedy: pass harnessConfig explicitly, or re-run resource bootstrap.
```
Names the setting, states what *is* available, gives an action. Minted at
`pkg/agent/provision.go:755-758`, propagating through `pkg/runtimebroker/handlers.go:835` and
`pkg/hub/errors.go:221-223`.

## 4. Alternatives Considered

**A1 — Change `default_harness_config` to `claude`, or seed `antigravity` in the single-node deploy.**
Rejected. This is the stopgap ptone explicitly refused. It fixes one deployment and leaves the class:
it does not touch mechanism A at all (an empty name still dispatches silently), and the next
deployment with a different seeding condition reproduces it. It would also be the *third* pass over
this defect, after `#1305`.

**A2 — Materialize `~/.scion/harness-configs/` in hosted mode so the broker's disk search succeeds.**
Rejected. It reverses a deliberate architectural decision documented in the code
(`cmd/server_foreground.go:100-117`) and entrenches the authority violation by making
broker-side name invention *work*, which guarantees the hub and broker drift apart again later. It
treats the fallback as the mechanism rather than as the bug.

**A3 — Improve only the error message at `provision.go:757`.**
Rejected as a complete answer, adopted as a *part* (§3.7). Alone it makes a broken deployment easier
to diagnose while leaving it broken, and leaves the operator to discover it at first agent create.
G1 is the goal with the most value and this does not advance it.

**A4 — Unify the config source (§3.4) only.**
Rejected as a complete answer, adopted as a *phase*. Necessary but insufficient: it closes mechanism
A and leaves mechanism B entirely live, and B is the one currently reproducing in the field.

**A5 — Hard-fail the hub at startup when defaults do not resolve.**
Rejected, and this was close. Correctness argues for it. But on this tier a boot failure destroys all
state and self-recovers empty, converting a degraded-but-usable deployment into data loss. Warn
loudly, expose on health, do not refuse to boot.

## 5. Migration / Rollout

**The existing tests assert the buggy behaviour is correct.**
`pkg/runtimebroker/hub_connection_test.go:399` `TestHydrateHarnessConfig_NoHubInfoFallsBack` and
`:423` `TestHydrateHarnessConfig_NoResolverIsGraceful` both lock in the silent `("", nil)` path.
`pkg/config/resolve_harness_config_test.go` `_Error` tests an empty-`Settings` case that
**cannot occur in production**, because `LoadVersionedSettings` always injects the embed.

**These tests must change, and that is a signal, not a regression.** Any reviewer seeing them
modified should read the diff carefully rather than waving it through — and equally, a developer who
finds themselves deleting an assertion to make a change pass should stop and ask.

Ordering is by risk, ascending. Each phase is independently valuable and independently revertable:
Phase 1 changes no success path at all; Phase 4 changes the most.

**Docs sequencing — this blocks nothing but must not be forgotten.** PR
`GoogleCloudPlatform/scion#1315` documents the workaround in two places:
`docs-site/src/content/docs/hosted/single-node/cloud-run.md:247-257` (a `:::caution` block) and
`:432-436` (a troubleshooting entry). Both name `antigravity`. Phase 4 makes both stale. They must be
updated in the same PR as Phase 4, or beta testers will follow instructions for a bug that no longer
exists. **Do not remove them earlier** — they are correct until Phase 4 lands.

## 6. Implementation Phases

| Phase | Content | Risk | Value |
|---|---|---|---|
| **1** | Startup validation (§3.3) + seed-skip reporting. Pure addition; no success path changes. | Low | **Highest** — converts a mystifying 502 into a deploy-time error |
| **2** | Error message provenance (§3.7). | Low | Diagnosis |
| **3** | Unify config source (§3.4). Own commit, own PR, effective-settings diff in the body. | **High** | Closes mechanism A |
| **4** | Explicit resolution outcomes (§3.5) + broker stops inventing, gated to hosted mode (§3.6) + docs update (§5). | **Highest** | Closes the authority violation |

Phases 1 and 2 can be one PR. Phase 3 must be alone. Phase 4 is last.

A cross-seam test is required in Phase 4 and does not exist today at any layer: hub emits no
identity → broker must fail with a named, actionable error rather than searching disk.

## 7. Acceptance Criteria

1. A hub whose `default_harness_config` does not resolve logs an ERROR **at startup** naming the
   setting, the value, and a remedy. Verified by starting a hub with a deliberately bogus default.
2. A hub where resource seeding was skipped logs an ERROR naming **which** condition
   (`GetStorage() == nil` or `SkipIfAnyExist`). Verified by inducing each.
3. On a fresh hosted deployment, agent create **omitting `harnessConfig` succeeds** (mechanism A).
4. On a hub where seeding did not run, agent create returns an error naming the hub setting and the
   registered alternatives — **not** a bare `harness-config "antigravity" not found` (mechanism B).
5. The 4-cell matrix from task #68 is re-run and **all four cells return 201**.
6. Workstation/local mode is unaffected: disk-based harness-config resolution still works. This is
   the primary regression risk of Phase 4 and must be explicitly tested, not assumed.
7. A test exists that crosses the hub/broker seam for the empty-identity case.
8. The two workaround passages in `cloud-run.md` are removed in the same PR as Phase 4, and no other
   doc still tells operators to pass `harnessConfig` to avoid a 502.
9. Every issue reference added or touched is fully qualified (`ptone/scion#N` /
   `GoogleCloudPlatform/scion#N`). Fourteen known collisions make bare refs unsafe.

## 8. Open Questions

- **Q-A (product, ptone).** Should `antigravity` remain the product-wide `default_harness_config`?
  Out of scope here by §2 and it does not block any phase — but Phase 1 will start reporting it as
  unresolvable on deployments that do not carry it, so the question becomes visible.
- **Q-B (design, deferrable).** Should the scheduler's missing template stamping
  (`pkg/hub/server.go:3236`, `populateAgentConfig` called with nil template) be folded into Phase 4?
  Same shape, different trigger. Recommend a separate issue — Phase 4 is already the largest.
- **Q-C (rollout).** Phase 3 changes effective hub settings on every deployment. Does that want a
  release note beyond the PR body?

## 9. Register Impact

- `ptone/scion#1273` — closed, correctly. Do not reopen. This design supersedes it.
- Tasks **#37 / #48** — must be **rewritten, not closed**. The empty-name description is stale
  post-`#1305`; mechanism B is what reproduces now.
- A new tracking issue should carry this design; the four phases become its checklist.
- All new issue references fully qualified. Fourteen known collisions between fork and upstream.

# Investigation: Empty harness-config name trace (tasks #37 / #48)

Author: sn-harnesscfg-inv. Date: 2026-08-28.

Upstream issue: `ptone/scion#1316`. All file:line references verified against
commit `19e3290` on the fork's `main` branch.

---

## Summary

The empty harness-config name originates in **two** places: (1) the default
template (`resources/templates/default/scion-agent.yaml`), which has no
`harness` or `default_harness_config` field, causing `inferHarnessFromName("default")`
to return `""` at `pkg/hub/template_bootstrap.go:174`; and (2) the hub's
`LoadBootstrapKoanf` (`pkg/config/hub_config.go:1122`), which never loads
`embeds/default_settings.yaml` and therefore holds no `default_harness_config`.

The hub sends `HarnessConfig: ""` with no ID or hash. The broker receives it,
passes `""` as `CLIFlag` into `ResolveHarnessConfigName`, which falls through to
rung 7 and returns `"antigravity"` from the embedded defaults that only the
broker loaded. Then `FindHarnessConfigDir("antigravity", ...)` searches template,
project, and `~/.scion/harness-configs/` directories, finds nothing (hosted mode
skips materialization), and fails with `harness-config "antigravity" not found`.
This propagates as HTTP 502 `runtime_error: Failed to create agent: failed to
find harness-config "antigravity": harness-config "antigravity" not found`.

**Recommended fix:** the hub should load `embeds/default_settings.yaml` in
`LoadBootstrapKoanf` (same as the broker does in `LoadVersionedSettings`), and
`populateAgentConfig` should fail-closed when `hcName == ""` after all resolution
tiers, rather than silently skipping.

---

## 1. Origin: where `harness: ""` enters a dispatch

### The default template

**`resources/templates/default/scion-agent.yaml`** (lines 15-19) contains:

```yaml
schema_version: "1"
description: "Default scion agent template"
agent_instructions: agents.md
system_prompt: system-prompt.md
```

**No `harness`, `harness_config`, or `default_harness_config` field.** Identical
embedded copy at `pkg/config/embeds/templates/default/scion-agent.yaml`.

### Template bootstrap resolves the template's Harness to ""

When the default template is stored to the Hub, `detectHarnessFromContent`
(`pkg/hub/template_file_handlers.go:122-131`) finds no `DefaultHarnessConfig`,
no `Harness` field, and falls through to:

```go
// template_file_handlers.go:131
return templateConfigInfo{Harness: inferHarnessFromName(templateName)}
```

`inferHarnessFromName("default")` (`pkg/hub/template_bootstrap.go:162-176`)
matches none of `claude`, `gemini`, `opencode`, `codex` and **returns `""`**.

So `store.Template.Harness == ""` and `store.Template.DefaultHarnessConfig == ""`
for the default template.

### Hub resolution attempts (all fail for the default template)

When a create request arrives with `harnessConfig: ""`:

| Step | File:Line | What happens |
|------|-----------|-------------|
| A | `handlers_agents_core.go:972` | `harnessConfig = req.HarnessConfig` -> `""` |
| B | `handlers_agents_core.go:973-974` | Project annotation `scion.io/default-harness-config` -> not set -> still `""` |
| C | `handlers_agents_core.go:976-977` | `getHarnessConfigFromTemplate(resolvedTemplate, "")` -> template.DefaultHarnessConfig is `""`, template.Harness is `""` -> returns fallback `""` |
| D | `handlers_agent_create_helpers.go:85` | `buildAppliedConfig` stamps `ac.HarnessConfig = ""` |
| E | `project_settings_handlers.go:497` | `applyProjectDefaults` -> project has no DefaultHarnessConfig -> still `""` |
| F | `hub_agent_defaults.go:136-137` | `applyHubAgentDefaults` -> hub's `AgentDefaults.DefaultHarnessConfig` is `""` (see below) -> still `""` |
| G | `handlers_agent_create_helpers.go:250-251` | `populateAgentConfig` template re-check -> template.Harness is `""` -> still `""` |
| H | `handlers_agent_create_helpers.go:253` | `if hcName != ""` -> **FALSE, entire block skipped** |

### Why the hub has no `DefaultHarnessConfig`

`LoadBootstrapKoanf` (`pkg/config/hub_config.go:1122-1174`) loads:
1. Coded defaults (line 1131-1143) - manually maintained subset, **does not include `default_harness_config`**
2. `SCION_SEED_*` env vars (line 1146-1147)
3. `settings.yaml` file (line 1154-1158)
4. `SCION_SERVER_*` env vars (line 1164-1167)

It **never** loads `pkg/config/embeds/default_settings.yaml`.

The `agent_defaults` opsettings section reads its `default_harness_config` from
the koanf path `default_harness_config` (`pkg/config/opsettings/registry.go:94`).
Since `LoadBootstrapKoanf` never populates that path, on a fresh deployment
`hubAgentDefaults().DefaultHarnessConfig` is `""`.

### Non-browser create paths that also supply empty

1. **CLI** (`cmd/create.go:229`): `HarnessConfig: harnessConfigFlag` — empty unless `--harness-config` flag is provided.
2. **Scheduler** (`pkg/hub/server.go:3231-3239`): Same template-fallback logic. With the "default" template, `harnessConfig` stays `""`.
3. **API direct** (any `POST /api/v1/projects/:id/agents` with `harnessConfig` field omitted or set to `""`): Go JSON unmarshalling of `omitempty` string produces `""` for all three input shapes (see below).

---

## 2. The three input shapes: `""`, `null`, absent

### Hub API (`CreateAgentRequest.HarnessConfig`)

The struct field is:
```go
// handlers_agents_core.go:135
HarnessConfig string `json:"harnessConfig,omitempty"`
```

Go's `encoding/json` behavior for a `string` field:
- **`"harnessConfig": ""`** -> `""` (empty string)
- **`"harnessConfig": null`** -> `""` (zero value for string)
- **Field absent** -> `""` (zero value for string)

**All three shapes converge to `""` at the Go unmarshaller.** There is no
way to distinguish them from within `createAgentInProject`.

### Hub-to-broker wire (`RemoteAgentConfig.HarnessConfig`)

Same `string` + `omitempty` pattern:
```go
// server.go:568
HarnessConfig string `json:"harnessConfig,omitempty"`
```
When `HarnessConfig == ""`, `omitempty` causes the field to be **absent** from
the JSON. The broker's `CreateAgentConfig.HarnessConfig` (also `string` +
`omitempty`, `types.go:392`) then receives it as `""`.

**All three shapes converge to `""` on the wire and at the broker.**

### Broker to `Provision`

`start_context.go:446` copies `in.Config.HarnessConfig` (a `string`) into
`opts.HarnessConfig` (also a `string`). No null-awareness exists at any point.

**Conclusion: `""`, `null`, and absent are indistinguishable throughout the
entire chain. They take identical paths at every hop.**

---

## 3. Transit: hop-by-hop trace

### Hop 1: Hub API handler -> `buildAppliedConfig`

- **Entry:** `handlers_agents_core.go:972-977`
- **Empty behavior:** Three-tier fallback (request, project annotation, template) all return `""`.
- `buildAppliedConfig` stamps `ac.HarnessConfig = ""` at `handlers_agent_create_helpers.go:85`.
- `req.Config.HarnessConfig` override at `:104-105` would fire only if the `config` sub-object explicitly sets it.
- **Empty is passed through.**

### Hop 2: `applyProjectDefaults`

- **Entry:** `project_settings_handlers.go:497`
- **Empty behavior:** Only fills if `ac.HarnessConfig == "" && settings.DefaultHarnessConfig != ""`. Project annotation not set -> no-op.
- **Empty is passed through.**

### Hop 3: `applyHubAgentDefaults`

- **Entry:** `hub_agent_defaults.go:136-137`
- **Empty behavior:** Only fills if `ac.HarnessConfig == "" && d.DefaultHarnessConfig != ""`. Hub has no default -> no-op.
- **Empty is passed through.**

### Hop 4: `populateAgentConfig` — harness-config ID/hash stamping

- **Entry:** `handlers_agent_create_helpers.go:230-334`
- **Empty behavior:**
  - `hcName = agent.AppliedConfig.HarnessConfig` -> `""` (line 230)
  - Template re-check at line 250-251: `getHarnessConfigFromTemplate` returns `""`.
  - Line 253: `if hcName != ""` is **false** -> **entire block skipped**.
  - No lookup, no ID/hash stamping, no log line, no error.
- **Empty is passed through silently. This is Fault 2 from `ptone/scion#1316`.**

### Hop 5: `buildCreateRequest` (hub -> broker dispatch)

- **Entry:** `httpdispatcher.go:492-509`
- Copies `agent.AppliedConfig.HarnessConfig` (`""`) to `req.Config.HarnessConfig` (line 495).
- `HarnessConfigID` and `HarnessConfigHash` are both `""` (lines 503-504).
- `omitempty` drops the empty fields from the JSON payload.
- **Empty leaves the hub as an absent field.**

### Hop 6: Broker `createAgent` handler

- **Entry:** `runtimebroker/handlers.go:372`
- JSON unmarshals `CreateAgentRequest`. `Config.HarnessConfig` is `""`.
- **Empty is passed through.**

### Hop 7: `buildStartContext`

- **Entry:** `runtimebroker/start_context.go:443-446`
- `opts.HarnessConfig = in.Config.HarnessConfig` -> `""`.
- **Empty is passed through.**

### Hop 8: `hydrateHarnessConfig`

- **Entry:** `runtimebroker/handlers.go:993-996`
- Guard: `cfg.HarnessConfigID == "" && cfg.HarnessConfigHash == ""` -> true.
- Returns `("", nil)`. No hydration attempted.
- **Empty causes early return. `opts.HarnessConfigPath` stays empty.**

### Hop 9: `Provision` -> `GetAgent`

- **Entry:** `pkg/agent/provision.go:338-361`
- `opts.HarnessConfigPath == ""` -> context not set (line 348-349 is a no-op).
- `GetAgent(ctx, ..., opts.HarnessConfig="", ...)` called at line 361.
- **Empty is passed through as the `harnessConfig` argument.**

---

## 4. The invention step

### Hop 10: `ResolveHarnessConfigName` invents `"antigravity"`

**Entry:** `pkg/config/resolve_harness_config.go:48`, called from
`pkg/agent/provision.go:739-748`:

```go
hcResolution, err := config.ResolveHarnessConfigName(config.HarnessConfigInputs{
    CLIFlag:     harnessConfig,    // ""
    TemplateCfg: finalScionCfg,    // merged template config
    Settings:    settings,         // from LoadVersionedSettings
    ProfileName: profileName,
})
```

The 7-rung priority chain:

| Rung | Source | File:Line | Result |
|------|--------|-----------|--------|
| 1 | CLIFlag (`""`) | `resolve_harness_config.go:50` | skip |
| 2 | StoredConfig.HarnessConfig | `resolve_harness_config.go:55` | nil StoredConfig (new agent) -> skip |
| 3 | TemplateCfg.DefaultHarnessConfig | `resolve_harness_config.go:60` | `""` (default template has none) -> skip |
| 4 | TemplateCfg.HarnessConfig | `resolve_harness_config.go:65` | `""` (default template has none) -> skip |
| 5 | StoredConfig.Harness | `resolve_harness_config.go:70` | nil StoredConfig -> skip |
| 6 | Profile DefaultHarnessConfig | `resolve_harness_config.go:75-84` | profile has no default -> skip |
| **7** | **Settings.DefaultHarnessConfig** | **`resolve_harness_config.go:88`** | **`"antigravity"` from embedded defaults** |

The settings come from `LoadVersionedSettings` (`pkg/config/settings_v1.go:1064-1077`),
which loads `embeds/default_settings.yaml` **unconditionally as step 1**
(line 1074-1077). That file contains:

```yaml
# pkg/config/embeds/default_settings.yaml:32
default_harness_config: antigravity
```

**The broker invents `"antigravity"` via rung 7 with source `"settings-default"`.**

This is the authority violation described in `ptone/scion#1316`: the hub is the
authority for what a hosted deployment's default harness-config is, but the
broker silently substitutes its own answer from `default_settings.yaml`.

---

## 5. The failure

### Hop 11: `FindHarnessConfigDir("antigravity", ...)` fails

**Entry:** `pkg/config/harness_config.go:105`, called from
`resolveHarnessConfigDir` at `pkg/agent/provision.go:755`:

```go
hcDir, err := resolveHarnessConfigDir(ctx, harnessConfigName, projectPath, templatePaths...)
```

`resolveHarnessConfigDir` (`provision.go:412-416`) checks for a Hub-hydrated
path on the context (none, because hydration was a no-op at Hop 8), then calls
`FindHarnessConfigDir("antigravity", projectPath, templatePaths...)`.

`FindHarnessConfigDir` (`harness_config.go:105-149`) searches:
1. Template-level `harness-configs/antigravity/` directories (lines 109-116) -> not found
2. Project-level `projectPath/harness-configs/antigravity/` (lines 118-126) -> not found
3. Global `~/.scion/harness-configs/antigravity/` (lines 128-137) -> **not found**

The global directory is empty because hosted mode skips `~/.scion`
materialization (`cmd/server_foreground.go:111-117`):

```go
// In workstation mode, refresh the default template and harness-configs
// from the binary's embeds. Hosted mode bootstraps directly into the Hub
// via BootstrapBundledResources, bypassing local ~/.scion materialization.
```

Line 149 returns:
```go
return nil, fmt.Errorf("harness-config %q not found", name)
```

### Error propagation

1. `provision.go:756-757`: wraps error as `failed to find harness-config "antigravity": harness-config "antigravity" not found`
2. `provision.go:365-366`: `Provision` returns the error.
3. `runtimebroker/handlers.go:838-839` (provision-only) or `:881-902` (full start):
   ```go
   RuntimeError(w, "Failed to provision agent: "+err.Error())
   // or
   RuntimeError(w, "Failed to create agent: "+err.Error())
   ```
4. `pkg/hub/errors.go:224-226`: `RuntimeError` writes HTTP 502 with code `runtime_error`.

### Operator-visible symptom

```
HTTP 502 Bad Gateway
{
  "error": "runtime_error",
  "message": "Failed to create agent: failed to find harness-config \"antigravity\": harness-config \"antigravity\" not found"
}
```

The operator never typed `antigravity`. The error names a resource they have
never heard of, gives no indication of where the name came from (`settings-default`
fallback in `ResolveHarnessConfigName`), and suggests no remedy.

---

## 6. Where it could be defaulted but is not

There are **five** candidate sites where a default could prevent the empty name
from reaching the broker. Listed in chain order:

### Site A: `LoadBootstrapKoanf` coded defaults (hub_config.go:1131-1143)

`default_harness_config` is **not** in the manually-maintained coded defaults.
Adding it here would populate the hub's `agent_defaults.default_harness_config`
on every deployment without env-var or settings.yaml intervention.

### Site B: `getHarnessConfigFromTemplate` (handlers_agent_create_helpers.go:67-77)

Could return a non-empty fallback when both template fields are empty. Currently
returns the caller's `fallback` argument, which is `""`.

### Site C: `populateAgentConfig` hcName guard (handlers_agent_create_helpers.go:253)

Could reject or substitute when `hcName == ""` instead of silently skipping.

### Site D: `buildCreateRequest` (httpdispatcher.go:492-509)

Could refuse to dispatch when `HarnessConfig == ""` and `HarnessConfigID == ""`.

### Site E: Broker `buildStartContext` (start_context.go:443-446)

Could set a broker-side default before calling `Provision`. This would be the
wrong fix: it extends the authority violation.

---

## 7. Recommended fix shape

### Primary fix site: `LoadBootstrapKoanf` (Site A) — close Fault 1

**Add `embeds/default_settings.yaml` as the first layer in `LoadBootstrapKoanf`,
the same way `LoadVersionedSettings` does at `settings_v1.go:1074-1077`.**

This is the correct site because:
- It is the **structural root cause**: the hub and broker disagree because they
  load different config sources. Unifying the source closes the disagreement.
- Once the hub has `default_harness_config: antigravity`, step F
  (`applyHubAgentDefaults`) fires, `hcName` is no longer empty, and the ID/hash
  stamping at step H resolves the config from the store (where
  `BootstrapBundledResources` seeded it).
- The fix is additive: it only adds a base layer that existing layers
  (settings.yaml, env vars) already override.

**Alternative site (worse): Site C — fail-closed on empty**

Making `populateAgentConfig` return an error or substitute a known default when
`hcName == ""` would catch the symptom but not the cause. The hub would still
disagree with the broker about what the default is. And failing closed here
breaks any legitimate case where no harness-config is needed (the `"generic"`
special case at `harness_config.go:141-147`).

### Secondary fix: `populateAgentConfig` (Site C) — close Fault 2

**After the existing `hcName != ""` block, add a log line when `hcName == ""`.**
Not an error — just visibility. Today the empty case is completely silent (no log,
no error, no metric). A WARN saying "harness config name is empty after all
resolution tiers; agent will dispatch without a harness config name" gives an
operator a diagnostic foothold.

### Issue #1316's phase 3 (unify config source) is exactly Site A. Issue #1316's
phase 2 (error provenance) covers the broker-side error message.

---

## 8. What could not be determined by reading

1. **Whether `BootstrapBundledResources` actually seeds `antigravity` on a fresh
   hosted deployment.** The code shows `SkipIfAnyExist: true`
   (`cmd/server_foreground.go:1855`), and the `ListHarnessConfigs` check
   (`resource_bootstrap.go:46-52`) skips ALL harness-config seeding if ANY active
   harness-config exists. On a deployment that already has one config (e.g. from
   a previous `scion harness-config install`), `antigravity` would never be
   seeded. **Experiment that would settle it:** on a fresh hosted deployment with
   no prior harness configs, check whether `GET /api/v1/harness-configs` returns
   `antigravity`. This can be done safely with a read-only API call.

2. **Whether the web form's harness selection is the "Other..." + blank path or
   whether there is a code path where `resolvedHarness` evaluates to `""` without
   user intervention.** The brief's item 1 says "empty is reachable from the
   browser only by selecting 'Other...' and blanking the name." I confirmed this:
   `harness` initializes to `'gemini-cli'` (line 69), and `resolvedHarness`
   (line 741-743) returns `customHarness` only when `harness === '__other__'`.
   The `selectDefaultsForProject` function (line 569+) always calls
   `setHarnessFromValue` with a non-empty value. **This is confirmed by reading;
   no experiment needed.**

3. **Whether the hub opsettings `default_harness_config` can be set via
   `SCION_SEED_DEFAULT_HARNESS_CONFIG` on a fresh deployment.** The seed env
   loading at `hub_config.go:1146-1147` calls `LoadSeedEnvKoanf`, which maps
   `SCION_SEED_*` to koanf paths. Whether `SCION_SEED_DEFAULT_HARNESS_CONFIG`
   maps to the correct koanf path (`default_harness_config`) depends on the
   seed env key transformation. **Experiment:** set
   `SCION_SEED_DEFAULT_HARNESS_CONFIG=claude` and check
   `GET /api/v1/admin/settings?section=agent_defaults`.

---

## 9. Scope recommendation

**Tier: P2** (multi-component, single-phase delivery).

The fix touches two packages (`pkg/config` for the koanf unification, `pkg/hub`
for the silent-skip logging) but requires no API changes, no schema migration,
and no new inter-component protocol. Each phase from `ptone/scion#1316` is
independently landable. The risk is in phase 3 (changing effective hub settings
on every deployment), which requires careful testing of the override chain.

---

## 10. Correction to the brief

The brief's item 2 ("The default template still has `harness: ""`") is **not
quite right.** The default template has **no `harness` field at all**. It is
the absence of the field — not the presence of an empty one — that causes
`detectHarnessFromContent` to fall through to `inferHarnessFromName("default")`
which returns `""`. The distinction matters because a template with an explicit
`harness: ""` would take a different code path in `detectHarnessFromContent`
(line 128: `if raw.Harness != ""` would be false, same result, but the
`raw.DefaultHarnessConfig` check at line 122 would be reached first if someone
later added that field).

Also: the brief says the browser path "initialises to `'gemini-cli'`" at
`agent-create.ts:69`. Confirmed — this is correct. However, the brief does not
mention that `selectDefaultsForProject` (line 569+) may **change** the selected
harness based on the template. If a project's default template has a
`defaultHarnessConfig` or `harness` field, the form auto-selects that value. For
the "default" template, both are empty, so the form falls through to the
project settings' `defaultHarnessConfig` or the hardcoded `'gemini-cli'`.

---

# Round 2 — Answers to the four questions, candidate pricing, and a correction

Author: sn-harnesscfg-inv. Date: 2026-08-28.

Round 1 trace is preserved above. This section answers the round-2 brief's four
questions and prices five fix candidates.

**Acknowledgement:** my round-1 fix recommendation was wrong. Merging
`embeds/default_settings.yaml` into `LoadBootstrapKoanf` is wrong for TWO
reasons: (1) the keyspace mismatch the architect identified, and (2) a more
fundamental problem I found in Q1 that the architect's brief did not name.

---

## Q1: Which modes does `LoadBootstrapKoanf` actually feed?

**`LoadBootstrapKoanf` feeds postgres DB mode ONLY. It does not reach file/SQLite
mode at all.**

The sole consumer is `initOperationalSettings` at
`cmd/server_foreground.go:1917`, which is inside a function gated by:

```go
// server_foreground.go:1892
if strings.EqualFold(cfg.Database.Driver, "postgres") {
    if err := initOperationalSettings(ctx, cfg, hubSrv, s, globalDir); err != nil {
```

In SQLite/file mode, `initOperationalSettings` is never called. Therefore:
- `LoadBootstrapKoanf` is never invoked.
- `syncHubSettings` (line 1931/1942) is never invoked.
- `NewOperationalSettings` (line 1948) is never created.
- `ops.Snapshot()` → `ApplySnapshot` (line 1958-1959) never runs.
- **`s.config.AgentDefaults` stays at its zero value for the entire process lifetime.**

The only path that populates `AgentDefaults` in SQLite mode is
`BuildLayer1SnapshotFromFile` (called from `admin_settings.go:403` during
`reloadSettings`), which **deliberately leaves AgentDefaults empty** per the
by-design comment at `server.go:176-181`.

**Consequence for fix candidate A:** Adding a line to `LoadBootstrapKoanf`'s
layer-1 confmap would fix postgres-mode hubs. It would do **nothing** for the
single-node SQLite tier, which is the tier this defect was measured on.

**This was not stated in the brief.** The brief said fix A "Affects every
DB-mode hub" — which is correct — but the single-node tier is SQLite, not
postgres. Fix A alone does not fix the tier.

---

## Q2: Test the by-design reason against hosted mode

The by-design reason at `server.go:176-180` says:

> *"In file mode this stays at its zero value: BuildLayer1SnapshotFromFile
> deliberately leaves the agent-defaults fields empty because a co-located broker
> reads the same settings.yaml and applies them itself at the BOTTOM of its own
> chain."*

**The architect's assertion is correct: the stated precondition does not fully
hold in hosted mode, but for a subtler reason than "no shared settings.yaml".**

### The shared settings.yaml DOES exist in hosted mode

On first boot in a Docker container (image `Dockerfile.hub` has no pre-built
`~/.scion`), `InitMachine` runs (`server_foreground.go:105-110`) and creates
`~/.scion/settings.yaml` from `embeds/default_settings.yaml`
(`init.go:589-600` via `getDefaultSettingsYAMLForRuntime`). This file contains
`default_harness_config: antigravity` (line 32).

Both the hub and the co-located broker can read it:
- Hub: `LoadBootstrapKoanf` step 3 (`hub_config.go:1154-1158`)
- Broker: `LoadVersionedSettings` step 2 (`settings_v1.go:1079-1085`)

So the premise "there is no shared settings.yaml" is wrong — **there IS one.**

### The precondition fails for a different reason

The design's logic is: "the broker reads settings.yaml and applies
`default_harness_config` at the bottom of its own chain (rung 7), so the hub
should not also apply it, because that would promote it to the hub tier and
outrank template/profile values."

This logic is CORRECT for the name-resolution step. The broker DOES resolve the
name `"antigravity"` at rung 7 of `ResolveHarnessConfigName`
(`resolve_harness_config.go:88`).

**But the name resolution is not the failure.** The failure is at the NEXT step:
`FindHarnessConfigDir("antigravity", ...)` (`harness_config.go:105-149`), which
searches for the config **on disk**. In hosted mode:

1. `MaterializeBundledResources` runs during `InitMachine` on first boot and
   puts `antigravity` at `~/.scion/harness-configs/antigravity/`. **So the first
   boot may work.**

2. On subsequent boots where `~/.scion` already exists, the refresh at
   `server_foreground.go:111-117` is SKIPPED in hosted mode (`else if
   !hostedMode`). If the container's filesystem is ephemeral (Cloud Run with no
   persistent volume), `~/.scion` may be re-created by `InitMachine` each time
   (if the image doesn't ship it). But if the image DOES ship a pre-built
   `~/.scion` (e.g., via a volume mount), the refresh never runs and
   harness-configs may be stale or absent.

3. **The fundamental issue:** the design says "a co-located broker reads the
   same settings.yaml and applies them itself." But "applying" the name means
   looking it up on local disk, and **the hosted design deliberately moved
   harness-configs from disk to the Hub's storage backend** (via
   `BootstrapBundledResources`). The two designs contradict: "broker resolves
   from disk" vs. "hosted mode stores in the Hub's backend, not on disk."

**The by-design reason for keeping AgentDefaults empty does not fail because
there's no shared settings.yaml. It fails because the RESOLUTION PATH it assumes
(disk-based) is not the resolution path hosted mode uses (store-based with
hydration). The hub must participate — stamp ID/hash — so the broker can hydrate
from the store instead of searching disk.**

### Where the broker gets `DefaultHarnessConfig` in hosted mode

The broker calls `LoadVersionedSettings("")` (`settings_v1.go:1064`) from
`GetAgent` → settings loading. Step 1 loads `embeds/default_settings.yaml`
unconditionally (`settings_v1.go:1074-1077`). Step 2 loads
`~/.scion/settings.yaml` if it exists. Both contain
`default_harness_config: antigravity`. The broker's
`ResolveHarnessConfigName` finds it at rung 7 (`resolve_harness_config.go:88`,
source `"settings-default"`).

---

## Q3: Is `SCION_SEED_*` viable on SQLite hosted mode?

**No. `SCION_SEED_*` does not reach SQLite mode.**

`LoadSeedEnvKoanf` (`hub_config.go:1101-1108`) is consumed in two places:
1. `LoadBootstrapKoanf` step 2 (`hub_config.go:1146`) — postgres-only (Q1).
2. `admin_settings_db.go:346` — guarded by `ops.bootstrapKoanf == nil` (line
   341), and `bootstrapKoanf` is only set in postgres mode.

**Fix C is dead on the SQLite tier.** It can only work for postgres
deployments.

### Exact env-var spelling (for postgres deployments where it would work)

The env var must be:
```
SCION_SEED_DEFAULTHARNESSCONFIG=antigravity
```

**NOT** `SCION_SEED_DEFAULT_HARNESS_CONFIG`.

Reason: `envKeyToOpsettingsKey` (`hub_config.go:1015-1023`) splits the
stripped key on `_`, lowercases each segment, looks up each in `snakeCaseFields`,
then joins with `.`.

- `SCION_SEED_DEFAULT_HARNESS_CONFIG` → strip → `DEFAULT_HARNESS_CONFIG` →
  split → `["default", "harness", "config"]` → no snakeCaseFields matches →
  join → `default.harness.config` → **WRONG** (nested koanf path, not the
  top-level `default_harness_config` that `extractAgentDefaults` reads).

- `SCION_SEED_DEFAULTHARNESSCONFIG` → strip → `DEFAULTHARNESSCONFIG` →
  split → `["defaultharnessconfig"]` → snakeCaseFields match at
  `hub_config.go:897`: `"defaultharnessconfig"` → `"default_harness_config"` →
  join → `default_harness_config` → **CORRECT**.

---

## Q4: Registry path, env spelling, and HarnessAuth default check

### Registry location and section key

- **Struct:** `pkg/config/opsettings/sections.go:58-71` —
  `AgentDefaultsSettings`
- **Section name:** `"agent_defaults"` (`registry.go:92`)
- **Koanf paths for the section:** `registry.go:93-99`:
  ```
  "default_template", "default_harness_config",
  "default_max_turns", "default_max_model_calls",
  "default_max_duration", "default_resources",
  "default_model", "default_thinking_level",
  "default_max_agent_role", "default_agent_role"
  ```

### `default_harness_auth` is NOT in the koanf extraction

`default_harness_auth` is:
- **Present** in the Go struct (`sections.go:62`): `DefaultHarnessAuth string`
- **Present** in `applyHubAgentDefaults` (`hub_agent_defaults.go:140-141`)
- **ABSENT** from `extractAgentDefaults` (`koanf.go:212-215`)
- **ABSENT** from the registry's koanf paths (`registry.go:93-99`)

**It can only be populated via the admin DB settings API** (direct JSON into the
struct), never from settings.yaml or env vars.

### Does HarnessAuth also need a default on this tier?

**No — the 502 is not caused by auth.** The failure is at
`FindHarnessConfigDir` (`harness_config.go:149`), which runs BEFORE any auth
resolution. Auth is resolved later by the harness's own provisioner.

The `antigravity` harness-config has `no_auth.behavior: drop-to-shell`
(config.yaml), and the auto-no-auth fallback at
`handlers_agent_create_helpers.go:277-290` checks for this. If credentials are
not found, it sets `NoAuth: true`. So the agent starts with `drop-to-shell`
behavior — functional, if degraded.

**A fix that supplies the config name without the auth will produce a working
agent** (in drop-to-shell mode), not another 502. The auth gap is independent.

---

## Candidate pricing

### Candidate A: One line in `LoadBootstrapKoanf`'s layer-1 confmap

**What:** Add `"default_harness_config": "antigravity"` to the coded defaults
confmap at `hub_config.go:1131-1143`.

**Tiers that change behaviour:** Postgres-mode hubs only (gated at
`server_foreground.go:1892`). **Does NOT fix the SQLite single-node tier.**

**Precedence rank:** The value enters via `syncHubSettings` → DB → `Refresh` →
`ApplySnapshot` → `s.config.AgentDefaults.DefaultHarnessConfig`. Then
`applyHubAgentDefaults` stamps it at `handlers_agents_core.go:1151`, BETWEEN
`applyProjectDefaults` and `populateAgentConfig`. On the hub side it's the
lowest-ranked non-template source. On the broker side it arrives as CLIFlag rank
(rung 1) — the ACCEPTED CONSEQUENCE at `hub_agent_defaults.go:125-130`.

**Does A re-introduce the hub-outranks-template inversion?** No. The value
only fires when `ac.HarnessConfig == ""` (`hub_agent_defaults.go:136`), meaning
the request, project annotation, and template all yielded nothing. It's beneath
ALL of them. On the broker side, the CLIFlag-rank arrival is the same inversion
already documented and accepted — Fix A does not create a new one. **A hub-wide
floor that outranks a template would only occur if the template itself sets
`default_harness_config` — in which case the template wins at step C
(`handlers_agents_core.go:976-977`) and `applyHubAgentDefaults` never fires.**

**Operator override:** Yes. The operator can:
- Set a different value via `PUT /api/v1/admin/settings` (agent_defaults section)
- Override with `SCION_SERVER_DEFAULTHARNESSCONFIG` (env override in
  `LoadEnvKoanf`, `hub_config.go:1088-1094`)
- Override at the project level via project annotation
  `scion.io/default-harness-config`
- Override per-request via `harnessConfig` field

**Blast radius:** Every postgres-mode hub gets a new coded default. If an
operator already has `default_harness_config` set in their settings.yaml or DB,
the existing value wins (layer 3 > layer 1). **Only hubs with NO existing value
are affected.** This is the correct behaviour: a coded default should not
override an explicit setting.

**Residual on non-fresh deploys:** If `BootstrapBundledResources` skipped
`antigravity` seeding due to `SkipIfAnyExist: true`
(`resource_bootstrap.go:45-53`), the hub would stamp a name whose ID/hash lookup
at `populateAgentConfig:253-334` fails (project-scope miss, global-scope miss).
The agent dispatches without ID/hash, the broker can't hydrate, falls back to
disk search, fails. **Fix D is needed as a complement** to make this degradation
visible.

### Candidate B: Give the default template `default_harness_config: antigravity`

**What:** Add `default_harness_config: antigravity` to
`resources/templates/default/scion-agent.yaml` (and its embed copy at
`pkg/config/embeds/templates/default/scion-agent.yaml`).

**Tiers that change behaviour:** ALL tiers — workstation, hosted SQLite, hosted
postgres. The template is the same embedded resource everywhere.

**Precedence rank:** The value enters at template rank via
`getHarnessConfigFromTemplate` at `handlers_agents_core.go:976-977`. This is:
- BENEATH request (`req.HarnessConfig`, line 972)
- BENEATH project annotation (line 973-974)
- ABOVE hub operational defaults (`applyHubAgentDefaults`, which never fires
  because the template already supplied a value)
- ABOVE the broker's own settings chain

On the broker side, it arrives as CLIFlag rank (rung 1). Same ACCEPTED
CONSEQUENCE as Fix A.

**Operator override:** Yes. Same mechanisms as A (request, project annotation,
hub operational default for other templates, per-request).

**Blast radius:** Every deployment using the "default" template gets agents
created with `harnessConfig: antigravity`. This is exactly what happens today
via the broker's rung-7 fallback — the same name, at a higher rank, resolved
earlier. **The operational outcome is identical for all cases that currently
reach the broker.**

In workstation mode, the change is invisible: the name was already resolved to
`antigravity` at rung 7 on the broker, and the disk search finds it. Now it's
resolved earlier at the hub, stamped with ID/hash, and the broker hydrates from
the store — same end result, faster path.

**Critical difference from A:** Fix B fixes the SQLite tier. Fix A does not.

**Risk:** If the default template's `default_harness_config` is changed later
(e.g., to `claude`), it changes the default for every deployment using the
"default" template. This is arguably correct — the template defines its own
default.

**Residual on non-fresh deploys:** Same as A — if `antigravity` is not in the
store, the ID/hash lookup misses and the broker falls back to disk. Fix D
complements.

### Candidate C: Deploy-time `SCION_SEED_*` only

**What:** Set `SCION_SEED_DEFAULTHARNESSCONFIG=antigravity` in the deployment's
env (e.g., Cloud Run service environment, Helm values).

**Tiers that change behaviour:** Postgres-mode hubs only (Q3: SCION_SEED_*
does not reach SQLite). **Does NOT fix the single-node SQLite tier.**

**Precedence rank:** Lands at `LoadBootstrapKoanf` layer 2 (above coded
defaults, below settings.yaml). Via `syncHubSettings`, it seeds the DB. From
there, same path as A.

**Operator override:** Yes — settings.yaml (layer 3) and SCION_SERVER_* (layer
4) both override it. DB-stored values also override seeded values after first
boot.

**Blast radius:** Zero shared-code change. Only deployments that set the env var
are affected. **But it does not fix the tier.**

**Additional hazard:** The env var spelling is non-obvious
(`SCION_SEED_DEFAULTHARNESSCONFIG`, compound, no underscores). This WILL be
mistyped as `SCION_SEED_DEFAULT_HARNESS_CONFIG`, which silently does nothing
(lands at `default.harness.config`, a koanf path nothing reads). Same
signature defect shape: success and failure look identical.

### Candidate D: Fail loudly when `hcName == ""` at `populateAgentConfig`

**What:** After the existing `if hcName != ""` block at
`handlers_agent_create_helpers.go:253`, add a branch for `hcName == ""` that
returns a 4xx naming the missing setting, listing registered alternatives, and
giving a remedy.

**Tiers that change behaviour:** All tiers. But it does NOT fix the defect —
it makes the error visible before the broker invents an unresolvable name.

**Precedence rank:** Not applicable — this is an error path, not a default.

**Blast radius:** Any agent create where no harness-config is resolved now
fails at the hub with a clear error instead of at the broker with a confusing
one. **This changes behavior for the `"generic"` harness:** currently,
`FindHarnessConfigDir("generic", ...)` returns a synthetic entry at
`harness_config.go:141-147`. If D rejects empty names at the hub, generic agents
might break. **D must check for `"generic"` as a special case, or must be scoped
to hosted mode only.**

Actually, wait — `"generic"` is a harness-config NAME, not an empty name. An
empty `hcName` is different from `hcName == "generic"`. If D only fires on
`hcName == ""`, generic agents are unaffected because they have a non-empty name.

**Complementary value:** This is the only candidate that addresses the
non-fresh-deploy residual (where `SkipIfAnyExist` suppressed seeding). Without
D, a hub whose store has some other config but not `antigravity` would still
produce the same confusing 502. With D, the hub itself would say "no
harness-config resolved; set default_harness_config in agent_defaults or
on the template."

### Candidate E: Fix the broker's rung 7 to not return a name it cannot resolve

**What:** In `ResolveHarnessConfigName`, before returning the settings-default
name at `resolve_harness_config.go:88`, verify that the name can actually be
resolved (either on disk or via a Hub-hydrated path on the context).

**Tiers that change behaviour:** All tiers. But it changes the broker's
behavior, which is currently correct in workstation mode (the disk search
succeeds) and only wrong in hosted mode (the disk search fails by design).

**Blast radius:** HIGH. `ResolveHarnessConfigName` is called from multiple
paths (new agents, resume, local CLI). Adding a resolution check here would need
access to the filesystem state (template paths, project path, global dir) that
`ResolveHarnessConfigName` currently does not receive. The function's signature
would need to change, or a callback would need to be injected.

**Alternative shape:** Instead of checking resolution inside
`ResolveHarnessConfigName`, check in the CALLER (`GetAgent` at
`provision.go:738-758`) — if rung 7 returned the name and the name cannot be
resolved, fall back to a different behavior (e.g., return an error with
provenance information). This is lower blast radius.

**Complementary value:** E prevents the broker from inventing an unresolvable
name. But it doesn't help the hub stamp ID/hash — the broker still gets an empty
harness-config from the hub. E is defensive depth, not a primary fix.

---

## Summary: which candidates fix the SQLite tier?

| Candidate | Fixes SQLite tier? | Fixes postgres tier? | Standalone? |
|-----------|-------------------|---------------------|-------------|
| **A** | **NO** | YES | Yes (postgres) |
| **B** | **YES** | YES | Yes (all) |
| **C** | **NO** | YES (deploy-specific) | No (deployment change) |
| **D** | N/A (diagnostic) | N/A (diagnostic) | No (complementary) |
| **E** | Partially (prevents bad name) | Partially | No (complementary) |

**Only B fixes the SQLite tier without additional changes.** A+B together cover
both tiers with redundancy (the hub operational default at A catches templates
that don't set `default_harness_config`; the template at B catches the default
template specifically). D is complementary for the non-fresh-deploy residual.

---

## Correction to this brief

The brief says at item 2:

> *"You did not need to add a source at all. Layer 1 of LoadBootstrapKoanf is a
> hand-written confmap of coded defaults (server.hub.port, server.database.driver,
> …) carrying its own comment saying it is 'a manually maintained subset of
> GlobalConfig defaults' that must be extended when new defaulted keys appear. If
> the answer is 'seed a default', that confmap is the place it goes, one line, in
> the right keyspace."*

This is correct for postgres mode. But **the brief does not say that
`LoadBootstrapKoanf` is postgres-only**, which makes "one line, right keyspace"
sound like a complete fix. It is not: it fixes postgres hubs but does nothing for
the SQLite tier that the defect was measured on. The brief's Q1 asks me to verify
this, and I believe the brief's author suspected it but had not confirmed it —
otherwise Q1 would be rhetorical.

Additionally, the brief's Q3 asks about `SCION_SEED_*` and cites task #44's
"SCION_SEED_* is postgres-only" as potentially conflicting with task #45's
findings. **Task #44 is correct: SCION_SEED_* is postgres-only.** The koanf
chain may be sound end-to-end (task #45), but `LoadBootstrapKoanf` is never
consumed on SQLite, so the chain starts at a dead root.

### `default_harness_auth` gap

The brief asks "whether [default_harness_auth] also needs a default on this
tier — check it and tell me, because a fix that supplies the config name and not
the auth may just move the 502."

**It does not need a default to prevent the 502.** The 502 is from
`FindHarnessConfigDir` failing before any auth resolution. The antigravity
harness has `no_auth.behavior: drop-to-shell`, and the auto-no-auth fallback at
`handlers_agent_create_helpers.go:277-290` handles it. A fix that supplies only
the config name produces a **working agent** (in drop-to-shell mode if no
credentials are found), not another 502. The auth gap is real but
severity-independent from the harness-config name resolution failure.

However: `default_harness_auth` is missing from the koanf extraction
(`koanf.go:212-215`) and the registry's koanf paths (`registry.go:93-99`). This
means it can **never** be set via settings.yaml or env vars — only via the admin
DB API. If fix A is implemented and an operator also wants to set a default auth,
they cannot do so via the same coded-defaults mechanism. This is a separate gap,
not a blocker for this fix.

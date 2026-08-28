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

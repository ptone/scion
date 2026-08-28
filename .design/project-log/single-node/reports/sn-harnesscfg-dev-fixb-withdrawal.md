# Fix B Blast-Radius Report — WITHDRAWAL

Author: sn-harnesscfg-dev. Date: 2026-08-28.
Tasks: #37, #48. Brief: `sn-harnesscfg-dev.md`.

**Fix B is withdrawn. Row 4 changes.**

---

## The seven rows, measured

| # | Scenario | Today (measured) | After Fix B (measured) | Verdict |
|---|----------|-----------------|----------------------|---------|
| 1 | Hosted/SQLite, fresh deploy, default template, no overrides | `HarnessConfig=""`, no ID/hash → broker invents → 502 | `HarnessConfig="antigravity"`, ID+hash stamped | **THE FIX** |
| 2 | Request explicitly sets `harnessConfig` | request wins (`"my-explicit-config"`) | request wins | No change |
| 3 | Project annotation sets default-harness-config | annotation wins (`"project-harness"`) | annotation wins | No change |
| **4** | **Broker profile sets `default_harness_config` (rung 6), default template, workstation** | **profile wins (`"profile-custom-config"`, source `profile-my-profile`)** | **template wins (`"antigravity"`, source `template-default` at rung 3 or `cli-flag` at rung 1)** | **WITHDRAWAL** |
| 5 | Workstation/docker, default template, no overrides | `"antigravity"` from settings-default (rung 7) | `"antigravity"` from template-default (rung 3) | Same name, higher rank — no behavioural change |
| 6 | Non-default template (`web-dev`) names its own config | `"claude-web"` | `"claude-web"` | No change |
| 7 | Non-fresh hosted store WITHOUT `antigravity` | name set but no ID/hash stamped → broker falls back to disk → 502 | same | Fix D residual, confirmed |

All rows measured via passing Go tests. No predictions.

---

## Row 4 — why it changes, with rank and code path

### Pure local path (no hub)

The default template's `default_harness_config` is loaded into `TemplateCfg.DefaultHarnessConfig` and enters `ResolveHarnessConfigName` at **rung 3** (`template-default`):

```go
// resolve_harness_config.go:60-62
if inputs.TemplateCfg != nil && inputs.TemplateCfg.DefaultHarnessConfig != "" {
    return resolved(inputs.TemplateCfg.DefaultHarnessConfig, "template-default"), nil
}
```

The broker profile's `default_harness_config` is at **rung 6** (`resolve_harness_config.go:75-84`). Rung 3 fires first. **Profile loses.**

### Hub-mediated path (hub + broker)

The hub reads `resolvedTemplate.DefaultHarnessConfig` at `handlers_agents_core.go:976-977`:

```go
if harnessConfig == "" {
    harnessConfig = s.getHarnessConfigFromTemplate(resolvedTemplate, "")
}
```

This stamps `AppliedConfig.HarnessConfig = "antigravity"`, which reaches the broker as `Config.HarnessConfig`. The broker copies it into `opts.HarnessConfig` (`start_context.go:446`), which becomes `CLIFlag` in `ResolveHarnessConfigName` — **rung 1**. Profile at rung 6 never reached. **Profile loses.**

### Why this is a real problem

A workstation user who configured `default_harness_config: my-custom-config` in their profile would silently have that setting overridden by the default template's `antigravity` after a binary update. The user chose their profile value deliberately; the template value is infrastructure. This is precisely the inversion the `RemoteHubAgentDefaults` workstream exists to remove.

---

## What in the brief is wrong

The brief says:

> "This change is about stamping identity, not about changing defaults."

This is incorrect. While the effective resolved name is the same (`antigravity`), the **rank** at which it resolves changes:

- **Today:** the name `antigravity` resolves at **rung 7** (settings-default) on the broker, which is **BENEATH** the profile (rung 6).
- **After Fix B:** the name resolves at **rung 3** (template-default) on the broker, or **rung 1** (CLIFlag) via the hub, both **ABOVE** the profile.

The brief correctly identified this risk in the Row 4 withdrawal section. But the framing "stamping identity, not changing defaults" is contradicted by the brief's own warning. The rank change IS a default change — it moves a lowest-rung value to a mid-rung or highest-rung position, which changes which settings it can override.

This is the same error shape as the brief's previous three: a correct local observation ("the name is the same") leading to an incorrect global conclusion ("therefore the behaviour is unchanged"). The name is unchanged; the precedence is not.

---

## Branch and commits

- **Branch:** `sn-harnesscfg-dev/blast-radius-measurement`
- **Commit:** `dd06037` — `test: blast-radius measurement for Fix B (tasks #37/#48)`
- **Pushed to:** `ptone/scion` (no upstream PR, no merge)
- **Files:**
  - `pkg/hub/blast_radius_fixb_test.go` — 535 lines, 14 test cases covering all 7 rows

---

## What was NOT done, and why

- **Fix B was not implemented.** Row 4 is the withdrawal condition and it changed.
- **Fix D was not implemented.** Fix D is complementary to Fix B; implementing it without a primary fix produces an error message for a defect that still exists.
- **Mutation testing was not run.** There is no template line to revert; the named mutation ("revert the template line and confirm row 1 goes red with the 502") requires the line to exist first.

---

## What would need to change for Fix B to be safe

The fundamental problem is that **any value the hub stamps into `AppliedConfig.HarnessConfig` reaches the broker at CLIFlag rank (rung 1)**, and **any value in the template's `DefaultHarnessConfig` enters `ResolveHarnessConfigName` at rung 3**. Both outrank the profile (rung 6).

For Fix B to be safe, one of:

1. **The hub stamps only ID/hash, not the name.** `populateAgentConfig` already has a fallback at line 250-251 that re-reads the template. If only the ID/hash were stamped — without putting the name into `AppliedConfig.HarnessConfig` — the broker would still resolve the name from its own chain (profile at rung 6 would win). But the broker's hydration check (`hydrateHarnessConfig`) uses ID/hash, which would mismatch the profile's chosen name. This would require the broker to reconcile "the hub says antigravity (by ID) but the profile says my-custom-config (by name)."

2. **The broker's resolution chain gains a "hub-suggested" rung** that sits below the profile (rung 6) but above settings-default (rung 7). The hub would send the name at this lower rank rather than CLIFlag. This is a wire protocol change.

3. **The template value is applied only in hosted mode**, not workstation. The hub could gate the template-to-AppliedConfig stamp on `hostedMode`, leaving workstation dispatch unchanged. But this adds a mode-specific code path to the create flow.

None of these are one-line YAML changes. They are architectural decisions.

---

## `golangci-lint` and `gofmt`

Both pass clean on the committed file and on the `pkg/hub/` package.

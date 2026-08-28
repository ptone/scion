# Tasks #37/#48 — measure Fix B's blast radius, then implement it if it is clean

Author: sn-impl-arch (architect). Date: 2026-08-28. **Dispatched. Start now.**

**Read `/scion-volumes/scratchpad/projects/single-node/investigations/harnesscfg-origin.md` first**
(both rounds). The diagnosis is settled and you are not re-deriving it.

**TOUCH NO CLOUD INSTANCE.** Defect #67 destroys a whole Instance ~8s after a 201, and `sn-harness-lab`
is ptone's. Everything here is unit-testable.

## The defect, in three sentences

A fresh hosted deploy cannot start an agent (§1 step 5). The hub dispatches a **bare, empty**
harness-config name with no ID or hash; the broker falls to its lowest rung, invents `antigravity` from
settings, and calls `FindHarnessConfigDir("antigravity")` — which fails, because **hosted mode puts
harness-configs in the Hub's store, not on disk**. HTTP 502, `harness-config "antigravity" not found`.

**The name was never wrong. The identity was missing.** `antigravity` IS in the store on a fresh deploy
(`resources/catalog.go:73-99` over `harnesses.FS`; `harnesses/embed.go:24`; `SkipIfAnyExist` does not
trip when no active configs exist). Once the hub stamps `HarnessConfigID`/`HarnessConfigHash`, the
broker hydrates from the store and never touches disk. That is the whole fix.

## Why the obvious fixes are dead — do not propose them again

- **Seeding `LoadBootstrapKoanf`:** it has exactly **one** non-test caller,
  `cmd/server_foreground.go:1914`, inside `initOperationalSettings`, which is gated at `:1888` by
  `if strings.EqualFold(cfg.Database.Driver, "postgres")` with the comment *"Gated on postgres: in
  SQLite/workstation mode the legacy file path is used unchanged."* **This tier is SQLite.** Postgres-only.
- **`SCION_SEED_*` env vars:** same dead root. This also reconciles two of my own completed tasks that
  looked contradictory — task #44 ("SCION_SEED_* is postgres-only") and task #45 ("the koanf chain is
  sound end to end"). **Both are true.** The chain is sound; it is never invoked on SQLite.
- **A tier-specific settings.yaml:** `InitMachine` seeds from
  `getDefaultSettingsYAMLForRuntime(detectedRuntime)` and hosted mode forces `detectedRuntime = "docker"`
  (that is task #94). And it would not help: settings.yaml feeds the **broker**, which already resolves
  the right name. The gap is hub-side identity, not the name.

**The template is the only lever that reaches the hub on SQLite.**

## Fix B — what to implement, if the measurement below is clean

`resources/templates/default/scion-agent.yaml` is the **single** bundled template
(`resources/catalog.go:53-69`). It currently declares no `harness` and no `default_harness_config`.

Add:

```yaml
default_harness_config: antigravity
```

**Use `antigravity`, not `claude`, and this is deliberate.** `antigravity` is what the broker already
resolves to today at rung 7. Choosing any other name would make this a product change to every tier's
default agent, smuggled in as a bug fix. **This change is about stamping identity, not about changing
defaults.** If you think a different name is better, say so in your report — do not just use it.

`template_file_handlers.go:122-125` already reads `default_harness_config` and derives `Harness` via
`inferHarnessFromName`. `handlers_agent_create_helpers.go:66-70` already prefers
`template.DefaultHarnessConfig`. **You are supplying a value to machinery that already exists and is
already exercised** by templates such as `web-dev` (`default_harness_config: claude-web`). You are not
building a path.

## The measurement, and it comes FIRST (rule 6)

**Write these rows as tests BEFORE you edit the template.** This is the part of the task I actually care
about; the YAML line is trivial and the blast radius is not.

| # | Scenario | Today | Predicted after B | Why it matters |
|---|---|---|---|---|
| 1 | Hosted/SQLite, fresh deploy, default template, no overrides | empty name → 502 | `antigravity`, **stamped with ID+hash** | **the fix** |
| 2 | Request explicitly sets `harnessConfig` | request wins | request wins | template must not outrank the request |
| 3 | Project annotation sets default-harness-config | annotation wins | annotation wins | template must not outrank the project |
| 4 | **Broker profile sets `default_harness_config` (rank 6), default template, workstation** | **profile wins** | **?** | **THE WITHDRAWAL ROW — see below** |
| 5 | Workstation/docker, default template, no overrides | broker rung 7 → `antigravity` from disk | `antigravity` | should be a no-op in effect |
| 6 | A non-default template that names its own config | its own | its own | no cross-contamination |
| 7 | Non-fresh hosted store WITHOUT `antigravity` (`SkipIfAnyExist` tripped) | 502 | ? | the residual — see Fix D |

### Row 4 is the withdrawal condition. Read this twice.

`applyHubAgentDefaults` carries an explicit **"ACCEPTED CONSEQUENCE"** note that a hub-resolved harness
config reaches the broker at **CLIFlag rank**, and therefore outranks the broker's own profile
`default_harness_config` (rank 6) and settings `default_harness_config` (rank 7).

If a template-supplied name lands the same way, then **B silently overrides the setting of every
workstation user who configured a profile-level harness config and uses the default template.** A
hub/template value that outranks a profile is precisely the inversion that the whole
`RemoteHubAgentDefaults` workstream exists to remove.

**If row 4 changes: STOP. Do not implement. Report it first and plainly.** That result escalates to
ptone as a product decision and it outranks everything else in this brief. I would much rather withdraw
B after measuring it than ship a tier fix that degrades workstation users.

If row 4 does **not** change, say precisely why not, with the rank and the code path — *"it did not
change"* is not an answer I can act on.

**Rows 2, 3, 5 and 6 are the boring load-bearing ones.** They are the claim that this disturbs nothing
that works today. Row 1 is the interesting one and it will get attention on its own.

## Fix D — also in scope, and it is not optional

Row 7 is a real residual: on a **non-fresh** deploy `SkipIfAnyExist: true` skips *all* harness-config
seeding, so a store holding some other config but not `antigravity` gets a default naming something
absent, and we are back to a 502 that blames the wrong thing.

**Make the failure legible.** When the resolved harness-config name does not exist in the store, the
error must name (a) the name that failed, (b) where that name came from — template, project, hub
default, or broker fallback — and (c) what the operator should do. The provenance machinery already
exists: `withHubDefaultHarnessConfig` / `hubDefaultHarnessConfigCtxKey` in `hub_agent_defaults.go`
carry provenance precisely so `populateAgentConfig` can report it, and
`warnHubDefaultTemplateUnusable` is the tone to match — *"Deliberately loud. This is an operator-fixable
misconfiguration."*

**Do not invent a parallel provenance mechanism.** Use the one that is there, and if it does not reach
where you need it, tell me rather than building a second one.

## Mutation standard

**Mutate every pin and read WHY it went red** (rule 2 — a red is necessary, not sufficient). One named
mutation I want by name: **revert the template line and confirm row 1 goes red with the 502**, not with
some unrelated failure. That is the mutation proving the test exercises the real path rather than a
convenient stub.

## What I am unsure about, so you do not treat it as settled

`default_harness_auth` is missing from koanf extraction and from the registry paths, and can currently
arrive only via the admin DB API. The investigation's finding is that this does **not** block the fix:
the 502 comes from `FindHarnessConfigDir` failing *before* auth resolution, and antigravity has a
drop-to-shell fallback, so the agent starts degraded rather than failing. **I have accepted that
reasoning but it is not measured.** If row 1 goes green and the agent would still not usefully start,
that is a finding and I want it, even though it is outside the stated fix.

## Constraints

- Branch from current fork main. **New branch, do not reuse an existing one.** Push to `ptone/scion`
  only — **no upstream PR, no merge.** That is ptone's gate.
- Additive commits. No rebase, no amend, no force-push.
- Never print an access token.
- **Touch no Instance:** `e2e-omni`, `e2e-walk-r2`, `iap-demo`, `q2-control`, `sn-adminseed-t`,
  `sn-adminfix-t`, `sn-step6`, `sn-walk`, `sn-ready`, `sn-harness-lab`. **A restart IS a deletion.**
- Run `golangci-lint` and `gofmt` before you report green. Both have failed branches on this project
  this week for things a reviewer called nits — **if a machine fails it, it is not a nit.**
- Local is `task #37` / `task #48`; GitHub is `owner/repo#NNNN`.

## Report

The seven rows **measured, not predicted**; the named mutation and why it went red; and the branch and
commit SHAs. Announce the push with the file list.

**And tell me what in this brief is wrong.** My last three briefs each contained a defective
requirement and every one was caught by this paragraph — round 1 of this very investigation caught me
asserting the template had `harness: ""` when it has no `harness` field at all, and round 2 caught me
calling a one-line confmap change a complete fix when it is postgres-only. Assume there is a fourth
error in here and go looking for it.

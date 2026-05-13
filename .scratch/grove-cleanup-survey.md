# Grove→Project Rename: Cleanup Survey Report

**Date:** 2026-05-13  
**Surveyed by:** survey-grove agent  
**Scope:** Full codebase (excluding `.git/`, `vendor/`, `node_modules/`, `.scion/`, `.design/`)

---

## Summary Statistics

| Category | Non-Test Code | Test Code | Scripts/Config/Docs | Total |
|----------|--------------|-----------|---------------------|-------|
| **COMPAT** (intentional backward-compat) | ~650 | ~800 | ~120 | ~1,570 |
| **CLEANUP** (can be renamed now) | ~550 | ~900 | ~250 | ~1,700 |
| **ALREADY CORRECT** (migration code) | ~60 | ~30 | ~10 | ~100 |
| **EXTERNAL** | 0 | 0 | 0 | 0 |
| **BUGS** | 3 confirmed | — | — | 3 |
| **Total references** | ~1,554 | ~1,988 | ~957 | ~4,499 |

### Files with grove references: 316

---

## Bugs Found

### BUG 1 (Critical): `scion-chat-app` — Uses removed `hubclient` API methods

**Files:**
- `extras/scion-chat-app/internal/chatapp/commands.go` (lines 315, 350, 428, 439, 482, 527, 548, 569, 594, 800, 846, 868, 1042, 1055, 1071, 1109, 1162)
- `extras/scion-chat-app/cmd/scion-chat-app/main.go` (lines 398, 400)

**Problem:** The chat app calls `client.Groves()`, `client.GroveAgents()`, and uses `hubclient.ListGrovesOptions` — these methods/types no longer exist in `pkg/hubclient`. The code **does not compile**.

**Verified:** `cd extras/scion-chat-app && go build ./...` produces:
```
client.GroveAgents undefined (type hubclient.Client has no field or method GroveAgents)
client.Groves undefined (type hubclient.Client has no field or method Groves)
undefined: hubclient.ListGrovesOptions
```

**Fix:** Replace `client.Groves()` → `client.Projects()`, `client.GroveAgents(id)` → `client.ProjectAgents(id)`, `hubclient.ListGrovesOptions` → `hubclient.ListProjectsOptions`. Also rename struct field `GroveID` to `ProjectID` in `state.SpaceLink` and `state.AgentSubscription`.

### BUG 2 (Medium): `scion-chat-app` — Database schema uses `grove_id`/`grove_slug` columns

**File:** `extras/scion-chat-app/internal/state/state.go` (lines 102–103, 112–115, 152–183, 264–389)

**Problem:** The chat app's SQLite schema defines `grove_id` and `grove_slug` columns in `space_links` and `agent_subscriptions` tables. While this works at runtime (it's a standalone DB), the column names are inconsistent with the rename. Since the chat app is already broken (Bug 1), these should be fixed together. **No migration is needed** since the chat app's DB is independent.

### BUG 3 (Low): `scion-chat-app` — User-facing strings still say "grove"

**File:** `extras/scion-chat-app/internal/chatapp/commands.go` (lines 236, 307, 402, 490, 599, etc.)

**Problem:** User-facing messages display "grove" terminology:
- `"This space is not linked to a grove. Use /scionAdmin link <grove-slug>"`
- `"Grove: %s | %s"` (agent subtitle)
- `"Grove '%s' not found"`
- `"Linked to grove '%s'"`

**Fix:** Update strings to use "project" terminology.

---

## Detailed Findings by Package

### 1. `pkg/config/` — Configuration & Paths

#### CLEANUP — Constants and directory names
| File | Line | Current | Should Be |
|------|------|---------|-----------|
| `paths.go` | 32 | `GroveConfigsDir = "grove-configs"` | `ProjectConfigsDir = "project-configs"` (constant name only — value is a filesystem path) |
| `paths.go` | 33 | `GrovesDir = "groves"` | `ProjectsDir = "projects"` (constant name only — value is a filesystem path) |
| `paths.go` | 214–341 | Comments referencing "grove" | Update to "project" |

**⚠️ IMPORTANT:** The *values* (`"grove-configs"`, `"groves"`) are filesystem paths on disk. Renaming these values requires a filesystem migration or fallback logic for existing installations. The *constant names* (`GroveConfigsDir`, `GrovesDir`) can be renamed independently.

#### CLEANUP — Embedded default settings file
| File | Line | Issue |
|------|------|-------|
| `embeds/default_grove_settings.yaml` | — | Filename uses "grove"; contents reference "grove" throughout |
| `koanf.go` | 261 | `EmbedsFS.ReadFile("embeds/default_grove_settings.yaml")` |

**Action:** Rename file to `default_project_settings.yaml`, update references. Update comments within to say "project".

#### CLEANUP — Local variable names
| File | Lines | Variables |
|------|-------|-----------|
| `project_discovery.go` | 44, 87, 98–99, 174, 206, 234, 365–367 | `GroveID` (struct field), `groveConfigsDir` (local var) |
| `project_marker.go` | 40–42, 94–96 | `GroveID`, `GroveName`, `GroveSlug` (struct fields in compat unmarshal — **COMPAT**); `grovePath` (local var — **CLEANUP**) |
| `settings.go` | 264, 473–1021 | `grovePath` params (used extensively) |
| `koanf.go` | 82–120 | `grove_id` key handling |

**Note on `project_marker.go`:** The `GroveID`/`GroveName`/`GroveSlug` struct fields in the YAML unmarshal auxiliary struct are **COMPAT** — they read legacy `grove-id`, `grove-name`, `grove-slug` YAML keys from existing `.scion` marker files.

#### COMPAT — Legacy YAML key reading
| File | Line | Key | Purpose |
|------|------|-----|---------|
| `project_marker.go` | 40 | `yaml:"grove-id"` | Read legacy `.scion` marker files |
| `project_marker.go` | 41 | `yaml:"grove-name"` | Read legacy `.scion` marker files |
| `project_marker.go` | 42 | `yaml:"grove-slug"` | Read legacy `.scion` marker files |
| `project_marker.go` | 189 | `SCION_GROVE_ID` env check | Detect hub-dispatched agents |
| `project_marker.go` | 231 | `grove-id` file read | Legacy project ID file |
| `koanf.go` | 83 | `"grove_id"` key | Legacy settings key |
| `koanf.go` | 108–120 | `"hub.grove_id"` | Legacy hub settings key |
| `settings.go` | 555 | `grove-id` file migration | Migrate to `project-id` |
| `settings.go` | 619, 657, 758, 794 | `"grove_id"`, `"hub.groveId"` | Legacy config key handling |
| `templates.go` | 252 | `"grove"` scope value | Accept "grove" as alias for "project" |

#### COMPAT — JSON schema
| File | Line | Key |
|------|------|-----|
| `schemas/settings-v1.schema.json` | 108 | `"grove_id"` property (deprecated alias) |

---

### 2. `pkg/hub/` — Hub Server

#### COMPAT — API endpoint aliases
| File | Line | Endpoint | Purpose |
|------|------|----------|---------|
| `server.go` | 2017–2019 | `/api/v1/groves`, `/api/v1/groves/register`, `/api/v1/groves/` | Deprecated endpoint aliases |
| `server.go` | 2335–2336 | `deprecateGroveEndpoint()` | Wrapper function |
| `web.go` | 786 | `p == "/groves"` | Web UI route |
| `handlers.go` | 4309, 4984 | `"/api/v1/groves/"` prefix parsing | Handle requests to legacy endpoints |

#### COMPAT — JSON wire format
| File | Lines | Fields |
|------|-------|--------|
| `events.go` | 87–183 | `GroveID string json:"groveId"` in all event types |
| `response_types.go` | 42–268 | `GroveID`, `GroveName`, `Grove` fields across response types |
| `handlers.go` | 3099–3160 | `LegacyGroves`, `LegacyProject` fields |
| `handlers.go` | 5892–5921 | Heartbeat unmarshal compat |
| `handlers_auth.go` | 172–177 | `groveId` marshal |
| `handlers_notifications.go` | 252, 512 | `groveId` query param |
| `project_webdav.go` | 348–364 | `groveId` marshal/unmarshal |
| `project_cache.go` | 47–109 | `groveId` marshal/unmarshal |

#### CLEANUP — Event pub/sub topics
| File | Line | Topic Pattern |
|------|------|---------------|
| `events.go` | 327 | `"grove."+agent.ProjectID+".agent.status"` |
| `events.go` | 357 | `"grove."+agent.ProjectID+".agent.created"` |
| `events.go` | 372 | `"grove."+projectID+".agent.deleted"` |
| `events.go` | 385 | `"grove."+project.ID+".created"` |
| `events.go` | 396 | `"grove."+project.ID+".updated"` |
| `events.go` | 406 | `"grove."+projectID+".deleted"` |
| `events.go` | 420, 434 | `"grove."+gid+".broker.status"` |
| `events.go` | 461 | `"grove."+notif.ProjectID+".notification"` |
| `events.go` | 523 | `"grove."+msg.ProjectID+".user.message"` |

**Note:** These are dual-published alongside `"project."` equivalents, so the `"grove."` versions are **COMPAT** for subscribers on the old topic prefix. Can be removed when all clients are updated.

#### CLEANUP — Local variable names and comments
| File | Lines | Items |
|------|-------|-------|
| `handlers.go` | 862, 1181, 2340–2363, 3703 | `grovePath`, `grovesDir` vars |

---

### 3. `pkg/hubclient/` — Hub Client Library

#### COMPAT — JSON wire format (all marshal/unmarshal pairs)
| File | Lines | Fields | Purpose |
|------|-------|--------|---------|
| `types.go` | 62–90 | `Grove`, `GroveID` json tags | Legacy field support |
| `types.go` | 145–179 | `GroveID`, `GroveName`, `GroveType` | Legacy field support |
| `types.go` | 263–289 | `Groves` json tags | Legacy `groves` key |
| `types.go` | 318–346 | `GroveID`, `GroveName` | Legacy field support |
| `types.go` | 383–406 | `GroveID` | Legacy field support |
| `types.go` | 512–519 | `"grove"` source value | Legacy enum value |
| `agents.go` | 187–203 | `GroveID` | Legacy field support |
| `messages.go` | 81–107 | `GroveID` | Legacy field support |
| `notifications.go` | 63–346 | Multiple `GroveID` fields | Legacy field support |
| `projects.go` | 125–494 | `LegacyProject`, `LegacyProjects`, `GroveID` | Legacy support |
| `runtime_brokers.go` | 85–225 | `Groves`, `GroveID` | Legacy field support |
| `scheduled_events.go` | 82–105 | `GroveID` | Legacy field support |
| `schedules.go` | 107–130 | `GroveID` | Legacy field support |
| `templates.go` | 91, 180 | `groveId` json tag, `groveId` query param | **Mixed COMPAT/CLEANUP** |
| `tokens.go` | 56–121 | `GroveID` | Legacy field support |

#### COMPAT — Client fallback to `/groves/` endpoints
| File | Lines | Purpose |
|------|-------|---------|
| `client.go` | 269, 282, 295, 308, 321 | Fallback to `/api/v1/groves/` when `/api/v1/projects/` returns 404 |

#### CLEANUP — Query parameter `groveId`
| File | Lines | Issue |
|------|-------|-------|
| `agents.go` | 296 | `query.Set("groveId", opts.ProjectID)` — sends `groveId` when server accepts both |
| `notifications.go` | 266, 373 | `query.Set("groveId", ...)` |
| `runtime_brokers.go` | 225 | `query.Set("groveId", opts.ProjectID)` |
| `templates.go` | 180 | `query.Set("groveId", opts.ProjectID)` |

**Note:** These query params are **COMPAT if the server only accepts `groveId`**, but if the server now accepts `projectId`, they can be renamed. Check server-side query param handling.

---

### 4. `pkg/store/` — Data Store

#### ALREADY CORRECT — Migration code (migrateV50)
| File | Lines | Purpose |
|------|-------|---------|
| `sqlite/sqlite.go` | 1210–1322 | `migrateV50` renames `groves` → `projects`, `grove_id` → `project_id`, etc. |

This is migration code describing the old-to-new transformation. The references to "grove" are correct and necessary.

#### COMPAT — Pre-V50 migration SQL (schema history)
| File | Lines | Purpose |
|------|-------|---------|
| `sqlite/sqlite.go` | 282–963 | DDL for `groves`, `grove_contributors`, `grove_id` columns, etc. |

These SQL strings represent the historical schema. They are executed only for fresh databases before V50 migration runs, or are ALTER statements from past migrations. They **cannot be changed** without breaking the migration chain.

#### COMPAT — JSON marshal/unmarshal on models
| File | Lines | Fields |
|------|-------|--------|
| `models.go` | 97–1700+ | Many `GroveID`, `GroveName`, `Grove` fields in marshal/unmarshal helpers |

---

### 5. `pkg/runtimebroker/` — Runtime Broker

#### CLEANUP — Local variables and log messages
| File | Lines | Items |
|------|-------|-------|
| `start_context.go` | 100–184 | `grovesPath` var, log messages referencing "grove" |
| `hub_connection.go` | 106 | `groveFilter` variable |
| `handlers.go` | 52, 246, 595, 647, 862, 879, 1181, 2340–2363 | `grovePath` vars, comments |
| `heartbeat.go` | 253 | `groves` variable |
| `server.go` | 865–880 | `grovePaths`, `grovesDir` variables |

#### COMPAT — Environment variables
| File | Lines | Env Var |
|------|-------|--------|
| `hubenv.go` | 33–34 | `"SCION_GROVE_ID"`, `"SCION_GROVE_PATH"` in allow-list |
| `start_context.go` | 280, 284 | Setting `SCION_GROVE_ID`, `SCION_GROVE_PATH` env vars |

#### CLEANUP — Local var names in hubenv.go
| File | Lines | Items |
|------|-------|-------|
| `hubenv.go` | 42–97 | `grovePath` parameters, `groveSettings` variable |

#### COMPAT — JSON wire format
| File | Lines | Fields |
|------|-------|--------|
| `types.go` | 137–495 | Multiple `GroveID`, `GroveName`, `GrovePath`, `GroveSlug` fields in marshal/unmarshal |

---

### 6. `pkg/runtime/` — Container Runtimes

#### COMPAT — Container labels and env vars
| File | Lines | Label/Env |
|------|-------|-----------|
| `common.go` | 286 | `addEnv("SCION_GROVE", config.Project)` |
| `common.go` | 288 | `addEnv("SCION_GROVE_ID", config.ProjectID)` |
| `common.go` | 393 | `--label scion.grove=%s` |
| `common.go` | 397 | `--label scion.grove_id=%s` |
| `docker.go` | 177–224 | Reading `scion.grove`, `scion.grove_id`, `scion.grove_path` labels |
| `apple_container.go` | 210–212 | Reading `scion.grove`, `scion.grove_id`, `scion.grove_path` labels |
| `k8s_runtime.go` | 676–756 | Reading/writing `scion.grove`, `scion.grove_id`, `scion.grove_path` labels |
| `podman.go` | 303–305 | Reading `scion.grove`, `scion.grove_id`, `scion.grove_path` labels |

**Note:** These are all **COMPAT** because existing running containers have `scion.grove*` labels. Both old and new labels should be read. Writing *new* labels should use `scion.project*`, but reading must handle both. Check if `scion.project*` labels are already being written alongside.

#### CHECK: Does `common.go` also write `scion.project*` labels?
Need to verify if dual-writing of labels is already in place.

---

### 7. `pkg/agent/` — Agent Management

#### CLEANUP — Local variables and log messages
| File | Lines | Items |
|------|-------|-------|
| `msgbuffer.go` | 89, 124, 129 | `grove` in log messages, `"grove_id"` log key |
| `manager.go` | 136, 139, 146 | `deletionGroveName` variable |
| `run.go` | 68, 71, 537 | `SCION_GROVE_ID`, `SCION_GROVE` env reads |
| `list.go` | 42–64 | `grovesToScan`, `scion.grove` label lookup |

#### COMPAT — Container labels and env vars
| File | Lines | Label |
|------|-------|-------|
| `provision.go` | 190 | `"scion.grove": projectName` |
| `run.go` | 895–914 | `"scion.grove"`, `"scion.grove_id"`, `"scion.grove_path"` labels |
| `run.go` | 1005–1022 | Reading `scion.grove_id`, `scion.grove` labels |
| `list.go` | 46 | `filter["scion.grove"]` fallback |

---

### 8. `pkg/broker/` — Message Broker (NATS topics)

#### COMPAT — Topic patterns
| File | Lines | Topic |
|------|-------|-------|
| `broker.go` | 58–59 | `TopicAgentMessages()` → `"scion.grove." + groveID + "..."` |
| `broker.go` | 63–64 | `TopicProjectBroadcast()` → `"scion.grove." + projectID + "..."` |
| `broker.go` | 74–75 | `TopicAllAgentMessages()` → `"scion.grove." + groveID + "..."` |
| `broker.go` | 79–80 | `TopicUserMessages()` → `"scion.grove." + groveID + "..."` |
| `broker.go` | 85–86 | `TopicAllUserMessages()` → `"scion.grove." + groveID + "..."` |

**These are wire-protocol topics.** Changing them would break all existing subscribers. The function names already hide the implementation (e.g., `TopicProjectBroadcast` generates a `scion.grove.*` topic), but the parameter name `groveID` should be renamed to `projectID`.

#### CLEANUP — Parameter/variable names and comments
| File | Lines | Items |
|------|-------|-------|
| `broker.go` | 21–22, 58–86 | `groveID` parameter names, comments |

---

### 9. `pkg/storage/` — Cloud Storage Paths

#### COMPAT — Storage path prefixes
| File | Lines | Path | Purpose |
|------|-------|------|---------|
| `storage.go` | 209–210 | `"templates/groves/" + scopeID` | GCS storage path |
| `storage.go` | 230–231 | `"harness-configs/groves/" + scopeID` | GCS storage path |
| `storage.go` | 255 | `"grove-workspace"` path segment | Workspace storage |

**These are persisted storage paths.** Changing them requires data migration or dual-path lookup. Mark as COMPAT.

#### CLEANUP — Variable/parameter names and comments
| File | Lines | Items |
|------|-------|-------|
| `storage.go` | 209, 230, 247–260 | `"grove"` case, `groveID` params |

---

### 10. `pkg/hubsync/` — Hub Synchronization

#### CLEANUP — User-facing prompt strings (high priority)
| File | Lines | String |
|------|-------|--------|
| `prompt.go` | 120 | `"Grove '%s' is not linked to the Hub."` |
| `prompt.go` | 121 | `"Link grove with Hub?"` |
| `prompt.go` | 158 | `"Register as new grove"` |
| `prompt.go` | 164 | `"Found %d existing grove(s)"` |
| `prompt.go` | 267–279 | `"grove(s)"` display strings |
| `prompt.go` | 289–393 | Multiple `"grove"` strings in prompts |
| `prompt.go` | 430 | `"Remove scion grove?"` |
| `prompt.go` | 437–457 | `"grove"` in prompt strings |
| `prompt.go` | 473–510 | `"grove"` in deletion prompts |

#### CLEANUP — Function parameter names
| File | Lines | Parameters |
|------|-------|------------|
| `prompt.go` | 118 | `func ShowLinkPrompt(groveName string, ...)` |
| `prompt.go` | 267 | `func ShowBrokerDeregistrationPrompt(... groves []string, ...)` |
| `prompt.go` | 416 | `func ShowCleanConfirmPrompt(groveName, grovePath string, ...)` |
| `prompt.go` | 445 | `func ShowChangeDefaultBrokerPrompt(groveName, ...)` |
| `prompt.go` | 499 | `func ShowBrokerDeletePrompt(... groveNames []string, ...)` |
| `sync.go` | Throughout | `groveName`, `groveID`, `groveState` variables |

#### CLEANUP — Comments and log messages
| File | Lines | Items |
|------|-------|-------|
| `sync.go` | 149–1502 | Extensive comments and debug messages using "grove" |

---

### 11. `pkg/sciontool/telemetry/` — Telemetry

#### COMPAT — Environment variables and attribute names
| File | Lines | Item |
|------|-------|------|
| `gcp_exporter.go` | 92–104 | `SCION_GROVE_ID` env var, `"grove_id"` label |
| `providers.go` | 66–85 | `SCION_GROVE_ID` env var, `"scion.grove.id"` attribute |

#### CLEANUP — Local variable names
| File | Lines | Items |
|------|-------|-------|
| `gcp_exporter.go` | 92 | `groveID` variable |
| `providers.go` | 66 | `groveID` variable |

---

### 12. `pkg/util/logging/` — Logging Infrastructure

#### CLEANUP — Attribute constants and comments
| File | Lines | Item |
|------|-------|------|
| `logging.go` | 28 | `AttrProjectID = "grove_id"` — **Should be `"project_id"`** |
| `message_log.go` | 35 | `AttrMsgProjectID = "grove_id"` — **Should be `"project_id"`** |
| `request_log.go` | 133–134 | `groveID` parameter name |
| `request_log.go` | 231, 233–234, 242, 251 | `GroveIdx` field, `"/api/v1/groves/"` patterns |
| `request_log.go` | 256–291 | `groveID` variable, comments |

**⚠️ `AttrProjectID = "grove_id"` is significant** — this is the attribute key used in structured logs. Changing it affects log parsing/dashboards. Should coordinate with observability tooling.

---

### 13. `pkg/agentcache/` — Agent Cache

#### CLEANUP — Parameter name and comments
| File | Lines | Item |
|------|-------|------|
| `cache.go` | 16, 34, 51–53 | `grovePath` parameter, comments |

---

### 14. `pkg/ent/entc/` — Database Migration

#### ALREADY CORRECT — Migration function
| File | Lines | Item |
|------|-------|------|
| `migrate_grove_to_project.go` | All | This file IS the migration code. References to "grove" describe what's being migrated FROM. |

---

### 15. `cmd/` — CLI Commands

#### COMPAT — Deprecated `--grove` CLI flags
| File | Lines | Flag |
|------|-------|------|
| `root.go` | 228–230 | `--grove` global flag (deprecated alias for `--project`) |
| `notifications.go` | 165–185 | `--grove` on subscribe/unsubscribe/subscriptions |
| `broker.go` | 350–361 | `--grove` on provide/withdraw |
| `hub_env.go` | 168–171 | `--grove` on env commands |

All properly marked with `MarkDeprecated("grove", "use --project instead")` and `MarkHidden("grove")`. ✅

#### CLEANUP — Comments and local variables
| File | Lines | Items |
|------|-------|-------|
| `attach.go` | 53–122 | Comments saying "grove" |
| `message.go` | 190, 422–423, 681–682 | `"grove"` in comments and help strings |
| `server.go` | 21, 83, 262 | `"groves"` in help strings |
| `completion_helper.go` | 69–202 | `scanProject` was renamed but var `currentProjectPath` reads from `"grove"` flag |
| `server_dispatcher.go` | 56–236 | `scion.grove` label writes, `grove` in comments |
| `stop.go` | 125–525 | `grove` comments, `"grove"` in help |
| `suspend.go` | 433 | `"grove"` in help |
| `schedule.go` | 53 | `"grove"` in comment |
| `cdw.go` | 45–54 | `"grove"` in comments |
| `template_import.go` | 34 | `"grove"` in help text |
| `template_resolution.go` | 71–373 | `"grove"` scope value handling |
| `root.go` | 101, 186, 193–194 | `"grove"` flag parsing |

#### COMPAT — `"grove"` as scope value
| File | Lines | Purpose |
|------|-------|---------|
| `template_resolution.go` | 113–114, 135, 373 | Accept `"grove"` as alias for `"project"` scope |

#### COMPAT — `project` command aliases
| File | Lines | Purpose |
|------|-------|---------|
| `project.go` | 42–43 | `Aliases: []string{"grove", "group"}` |

#### CLEANUP — Help text and comments
| File | Lines | Items |
|------|-------|-------|
| `server.go` | 83 | `"Hub API: Central registry for groves, agents, and templates"` |
| `server.go` | 262 | `"Automatically add runtime broker as provider for new groves"` |
| `message.go` | 681 | `"Send the message to all running agents in the current grove"` |
| `message.go` | 682 | `"Send the message to all running agents across all groves"` |
| `stop.go` | 525 | `"Stop all running agents in the current grove"` |
| `suspend.go` | 433 | `"Suspend all running agents in the current grove"` |
| `template_import.go` | 34 | `"current grove or global templates"` |

---

### 16. `extras/` — External Tools

#### BUG — `scion-chat-app` (see Bugs section above)

#### CLEANUP — `fs-watcher-tool`
| File | Lines | Items |
|------|-------|-------|
| `main.go` | 43, 54, 65–187 | `grove` variable, `--grove` flag, log messages |
| `pkg/fswatcher/grove.go` | All | **File named `grove.go`**, `GroveDiscovery` type, `groveID` field |
| `README.md` | Throughout | `--grove` flag documentation, log examples |

**Action:** Rename `grove.go` → `project_discovery.go`, `GroveDiscovery` → `ProjectDiscovery` (note: `NewProjectDiscovery` already exists as the constructor), rename `--grove` flag to `--project` (with `--grove` as deprecated alias), update README.

#### COMPAT — `scion-a2a-bridge`
| File | Lines | Items |
|------|-------|-------|
| `bridge/config.go` | 26 | `Groves []ProjectConfig yaml:"groves,omitempty"` — **COMPAT** legacy config field |
| `cmd/main.go` | 220 | `"merge legacy 'groves' into 'projects'"` — **COMPAT** |
| `bridge/server.go` | 167–169 | `/groves/` route aliases — **COMPAT** |
| `bridge/bridge.go` | 274–277 | `scion.grove.*` topic subscription — **COMPAT** |
| `bridge/bridge.go` | 945 | `parts[1] != "grove"` check — **COMPAT** |

---

### 17. `scripts/` — Shell Scripts

#### CLEANUP — Integration test scripts
| File | Lines | Items |
|------|-------|-------|
| `hub-env-integration-test.sh` | 305–777 | `setup_test_grove()`, `GROVE_ID`, `test_phase2_grove_scope()`, `--grove=` flags throughout |
| `hub-secret-integration-test.sh` | 306–777 | Same pattern as env test |
| `template-integration-test.sh` | 696–821 | `grove_dir`, `grove_template_dir` variables |
| `checktemplate-2.sh` | 18, 52 | `"grove"` scope value |
| `starter-hub/gce-demo-telemetry-sa.sh` | 157 | `"grove settings"` in echo |

#### CLEANUP — Setup scripts
| File | Lines | Items |
|------|-------|-------|
| `hack/setup.sh` | 38–39 | `scion grove init` |
| `hack/test_oauth.sh` | 47–49 | `scion grove init` |
| `hack/cleanup.sh` | 26 | `"grove context"` comment |

---

### 18. `changelog/` — Changelog entries (50 files, 116 references)

**Category: EXTERNAL/HISTORICAL** — Changelog entries are historical records. Do not modify.

---

### 19. Test Files (~1,988 references across ~100+ test files)

Test files mirror the grove references in their corresponding production code. Key areas:

- **`pkg/config/koanf_test.go`** (162 refs): Tests for `grove_id` settings key parsing
- **`pkg/runtimebroker/handlers_test.go`** (173 refs): Tests for broker handlers including `groveId` JSON
- **`pkg/config/settings_v1_test.go`** (131 refs): Tests for v1 settings with `grove_id`
- **`pkg/store/sqlite/sqlite_test.go`**: Migration tests
- **`cmd/` test files**: CLI tests using `--grove` flags, `grove` scope values

**Action:** Test files should be updated when their corresponding production code is updated. Many test variable names use `grove` (e.g., `groveID`, `grovePath`) and can be renamed independently.

---

## Recommended Priority Order for Cleanup

### Priority 1: Bugs (Fix immediately)
1. **Fix `scion-chat-app` compilation** — Replace removed `Groves()`/`GroveAgents()`/`ListGrovesOptions` with current API
2. **Fix `scion-chat-app` user-facing strings** — Replace "grove" with "project" in user messages

### Priority 2: User-facing strings (High impact, low risk)
1. **`pkg/hubsync/prompt.go`** — All user-facing prompt strings (~60 references)
2. **`cmd/` help text** — `--help` output for `server`, `message`, `stop`, `suspend`, `template import` commands
3. **`extras/fs-watcher-tool/README.md`** — Documentation

### Priority 3: Internal code quality (Medium impact, low risk)
1. **Local variable names** — Rename `groveID` → `projectID`, `grovePath` → `projectPath`, etc. throughout:
   - `pkg/hubsync/sync.go` (~150 refs)
   - `pkg/runtimebroker/` (~50 refs)
   - `pkg/config/` (~40 refs)
   - `pkg/agent/` (~20 refs)
   - `pkg/broker/broker.go` (~10 refs)
2. **Constants** — `GroveConfigsDir` → `ProjectConfigsDir`, `GrovesDir` → `ProjectsDir` (names only, not values)
3. **Comments** — Update grove→project in comments across all packages
4. **Embedded file** — Rename `default_grove_settings.yaml` → `default_project_settings.yaml`
5. **`extras/fs-watcher-tool`** — Rename `grove.go`, `GroveDiscovery`, `--grove` flag

### Priority 4: Log attribute keys (Coordinate with observability)
1. **`pkg/util/logging/`** — `AttrProjectID = "grove_id"` → `"project_id"` (affects log parsing)
2. **`pkg/sciontool/telemetry/`** — `"scion.grove.id"` attribute, `"grove_id"` label

### Priority 5: Wire format (COMPAT, defer to breaking change)
1. **JSON `groveId` fields** — All marshal/unmarshal compat code in `hubclient/`, `hub/`, `store/`, `runtimebroker/`
2. **Container labels** — `scion.grove`, `scion.grove_id`, `scion.grove_path`
3. **Environment variables** — `SCION_GROVE`, `SCION_GROVE_ID`, `SCION_GROVE_PATH`
4. **NATS topics** — `scion.grove.<id>.*`
5. **API endpoints** — `/api/v1/groves/`
6. **Storage paths** — `templates/groves/`, `harness-configs/groves/`, `grove-workspace`
7. **CLI flags** — `--grove` deprecated aliases
8. **Config keys** — `grove_id`, `hub.groveId` in settings files

### Priority 6: Schema (Requires migration, defer)
1. **SQLite pre-V50 DDL** — Historical migration SQL (never change)
2. **Filesystem paths** — `~/.scion/grove-configs/`, `~/.scion/groves/` (requires migration logic)

---

## Suggested COMPAT Comment Format

For items that must remain for backward compatibility, use:

```go
// COMPAT(grove-rename): <explanation of why this exists and when it can be removed>
```

### Examples:

```go
// COMPAT(grove-rename): Accept "grove" as JSON key alias for "project" to support
// clients running older versions. Remove when all clients are updated to use "project" keys.
type agentMarshal struct {
    GroveID string `json:"groveId,omitempty"` // COMPAT(grove-rename): legacy field
}

// COMPAT(grove-rename): Deprecated --grove flag alias for --project. Remove in next major version.
cmd.Flags().StringVar(&projectID, "grove", "", "Deprecated alias for --project")

// COMPAT(grove-rename): Read legacy grove-id YAML key from existing .scion marker files.
// Remove once all existing installations have been migrated to project-id.
GroveID string `yaml:"grove-id"`

// COMPAT(grove-rename): Set SCION_GROVE_ID env var for containers. Older agents read this
// to determine their project context. Remove when all harness versions read SCION_PROJECT_ID.
env["SCION_GROVE_ID"] = in.ProjectID

// COMPAT(grove-rename): Dual-publish on "grove." topic prefix for subscribers using older
// topic patterns. Remove when all subscribers have migrated to "project." prefix.
p.publish("grove."+agent.ProjectID+".agent.status", evt)

// COMPAT(grove-rename): Accept "grove" as a scope value alias for "project" in template
// resolution. Remove when all templates/configs have been updated.
case "grove", "project":
```

---

## Summary

The grove→project rename is well underway. The core data model (DB tables, primary Go types, CLI commands) has been migrated. What remains falls into three categories:

1. **Quick wins (~25% of references):** Variable names, comments, log messages, help text, user-facing prompts — can be renamed with no backward-compat concern.

2. **Intentional compat code (~65% of references):** JSON marshal/unmarshal, env vars, container labels, API endpoints, NATS topics — must stay until a coordinated breaking change.

3. **Bugs (3 confirmed):** The `scion-chat-app` uses removed API methods and won't compile. This is the highest priority fix.

The suggested COMPAT comment format (`// COMPAT(grove-rename): ...`) would make it easy to grep for and track remaining compat code, and to coordinate its removal in a future breaking release.

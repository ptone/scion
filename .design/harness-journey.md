# Design: Harness Config Lifecycle — Complete Journey

**Author:** harness-journey-inv (architect)
**Date:** 2026-06-13
**Status:** Approved — all open questions resolved 2026-06-13
**Based on:** [research.md](./research.md)

---

## 1. Problem Statement

The harness config lifecycle has four stages — Import, Build, Auth, Update — but only Import is fully implemented. The other three are either partially built (Build), fully designed but unimplemented (Auth), or blocked on a missing data field (Update). This design covers the changes needed to complete the journey end-to-end.

### Current state summary

| Stage | Status | Blocker |
|-------|--------|---------|
| **Import** | Done | — |
| **Build** | Partial (backend executor exists on branch, QA'd with fixes needed) | PRs #403/#404/#406 not merged; CLI Dockerfile sync; frontend JSON key mismatch (fixed in #410) |
| **Auth** | Designed, not built | No implementation of harness-auth-flow design phases 1-3 |
| **Update** | Not started | `source_url` not stored in schema |

---

## 2. Source URL Storage Gap — Detailed Change Spec

This is the **cheapest, highest-impact change** — ~45 lines of code that unblock the entire update/reimport flow.

### 2.1 What changes

The `sourceURL` parameter currently flows through the import pipeline for name derivation and fetching, then is discarded. It must be persisted on the resource record.

**This change applies to both harness-configs AND templates** — the shared `ResourceStore`/`ResourceRecord` pattern means adding it once benefits both resource kinds.

### 2.2 Schema layer

**File:** `pkg/ent/schema/harnessconfig.go`

Add field to `HarnessConfig.Fields()`:
```go
field.String("source_url").
    Optional(),
```

**File:** `pkg/ent/schema/template.go`

Add the same field to `Template.Fields()`:
```go
field.String("source_url").
    Optional(),
```

Then run `go generate ./pkg/ent` to regenerate.

Ent's auto-migration adds the column on startup — no manual migration script needed.

### 2.3 Store model layer

**File:** `pkg/store/models.go`

Add to `HarnessConfig` struct (after `Visibility` field, line ~573):
```go
SourceURL string `json:"sourceUrl,omitempty"`
```

Add to `Template` struct (equivalent location):
```go
SourceURL string `json:"sourceUrl,omitempty"`
```

### 2.4 Resource record layer

**File:** `pkg/hub/resource_store.go`

Add to `ResourceRecord` struct (line ~43):
```go
type ResourceRecord struct {
    // ... existing fields ...
    SourceURL     string
}
```

### 2.5 Persistence layer — harness-configs

**File:** `pkg/hub/resource_store.go`

In `harnessConfigPersistence.Create()` (line ~355), add to the `store.HarnessConfig` literal:
```go
SourceURL: rec.SourceURL,
```

In `harnessConfigPersistence.Update()` (line ~374), add:
```go
hc.SourceURL = rec.SourceURL
```

In `harnessConfigToRecord()` (line ~391), add:
```go
SourceURL: hc.SourceURL,
```

Equivalent changes in the template persistence.

### 2.6 Bootstrap layer

**File:** `pkg/hub/resource_store.go`

Modify `Bootstrap()` signature to accept source URL:
```go
func (rs *ResourceStore) Bootstrap(ctx context.Context, name, dir, scope, scopeID, sourceURL string, force bool) (bool, error)
```

Set `rec.SourceURL = sourceURL` when creating the initial record.

On update (force-sync path), update `rec.SourceURL = sourceURL` if non-empty (don't clear an existing URL on workspace re-sync).

### 2.7 Import pipeline layer

**File:** `pkg/hub/resource_import.go`

Thread `sourceURL` through `importResourceDirs()`:
- `resourceDir` struct gains a `sourceURL string` field
- `discoverResourceDirs()` sets `sourceURL` on the leaf case (remote imports), leaves it empty for workspace imports
- The worker loop passes `rd.sourceURL` to `Bootstrap()`

**File:** `pkg/hub/harness_config_bootstrap.go` and `pkg/hub/template_bootstrap.go`

Update the thin wrappers to pass `sourceURL` from `importFromRemote()` calls. For `importFromWorkspace()` calls, pass `""`.

### 2.8 API response layer

**File:** `pkg/hub/handlers.go`

Include `source_url` in harness-config detail/list/show responses. No new endpoint needed — it's just another field on the existing response.

### 2.9 Ent store adapter

**File:** `pkg/store/entadapter/` (harness config and template adapters)

Map `source_url` between Ent model and store model in CRUD operations.

### 2.10 Change count estimate

| Layer | Files touched | Lines changed |
|-------|--------------|---------------|
| Ent schema | 2 | ~6 |
| Store models | 1 | ~4 |
| Resource store (record + persistence + bootstrap) | 1 | ~20 |
| Import pipeline | 2 | ~10 |
| Ent adapter | 1-2 | ~8 |
| API response | 1 | ~3 |
| **Total** | **~8 files** | **~51 lines** |

---

## 3. Phased Implementation Plan

### Phase 0: Establish Baseline

**Goal:** Merge pending PRs to establish a working build flow on main.

**PRs to merge:**
- **#403** — (check current status; may relate to build executor)
- **#404** — (check current status)
- **#406** — Harness local build P2 (deploys `BuildHarnessConfigImageExecutor`, build UI on harness-config detail page)
- **#410** — runId JSON key fix (already merged to fix the blocker)

**Assumption for remaining phases:** These PRs are merged or their changes are assumed as baseline. The design does NOT depend on them being merged first — Phase 1 (source_url) is independent.

**Post-merge validation:**
- Verify build works end-to-end on a deployed instance (hard-refresh to avoid Finding 6)
- Verify harness-config detail page shows build button
- Verify CLI `scion build` path (may still fail per Finding 2 — Dockerfile sync gap)

---

### Phase 1: Source URL Storage + Reimport Command

**Goal:** Store import source URL; add `scion harness-config update` CLI command.

**Duration estimate:** 1-2 days

#### 1a. Source URL storage (see Section 2 above)

All changes from Section 2 — schema, models, resource store, import pipeline, API response.

**Testing:**
- Unit test: import from remote URL → verify `source_url` persisted on the record
- Unit test: import from workspace → verify `source_url` is empty
- Unit test: re-import (force) → verify `source_url` updated if a new URL is provided
- Integration: `scion harness-config show <name>` displays source URL

#### 1b. Reimport CLI command

**New file:** `cmd/harness_config_update.go`

```
scion harness-config update <name> [--url <override-url>]
scion harness-config update --all
```

Behavior:
- Looks up the harness-config by name (via Hub API)
- Reads the stored `source_url`; if `--url` is provided, uses that instead (and updates stored URL)
- Calls the existing import pipeline with `force=true`
- Reports what changed (content hash comparison)

Flags:
- `--url <url>` — override the stored source URL (and update it)
- `--all` — update all harness-configs that have a stored source URL (batch mode)

Edge cases:
- No `source_url` stored and no `--url` flag → error: "No source URL stored. Use --url to specify."
- Name not found → error
- No name and no `--all` → error: "Specify a harness-config name or use --all"
- `--all` with `--url` → error (ambiguous — can't set one URL for all configs)

**Register** in `cmd/harness_config.go` alongside existing subcommands.

#### 1c. Hub API endpoint for reimport

**Endpoint:** `POST /api/v1/harness-configs/{id}/reimport`

Request body (optional):
```json
{
  "sourceUrl": "https://..." // optional override; if omitted, uses stored source_url
}
```

Response: same streaming NDJSON progress as existing import endpoints.

**Authorization:** same as import — requires `harness_config:create` action on the owning scope.

#### 1d. Web UI "Refresh from Source" button

Add to harness-config detail page (`web/src/components/pages/harness-config-detail.ts`):
- "Refresh from Source" button, visible only when `source_url` is non-empty
- Calls `POST /api/v1/harness-configs/{id}/reimport`
- Reuses NDJSON streaming progress from `<scion-resource-import>`
- Shows before/after content hash to indicate whether anything changed
- Displays stored `source_url` as metadata on the detail page

---

### Phase 2: UX Validation Passes

**Goal:** End-to-end validation of build and auth flows, fixing gaps found.

**Duration estimate:** 3-5 days (parallelizable sub-phases)

#### 2a. Build flow end-to-end validation

**Prerequisite:** Phase 0 PRs merged.

**Known issues to fix:**
1. **Finding 2 (CLI Dockerfile sync):** `cmd/build.go` checks for local Dockerfile but it's not synced during install.
   - **Fix option A:** Sync Dockerfile to local dir during `harness-config install/pull`
   - **Fix option B:** CLI falls back to fetching Dockerfile from Hub storage when not found locally
   - **Recommendation:** B — less coupling, works without re-installing
2. **Finding 3 (log streaming):** Build logs appear only after completion.
   - **Fix:** Periodic flush of `bytes.Buffer` to DB record (every 2s), so polling picks up incremental output
3. **Finding 6 (browser cache):** `main.js` lacks content hash.
   - **Fix:** Configure Vite to hash `main.js` entry point; add ETag headers to static file serving
4. **Finding 4 (state leak):** `buildRunning` not reset on polling failure.
   - **Fix:** Reset `buildRunning = false` and show error state if `buildRunId` is empty after start-build response

**Validation checklist:**
- [ ] Hub build image: click Build → see streaming logs → image built → harness-config image field updated
- [ ] CLI build: `scion build codex` → Dockerfile fetched → image built locally
- [ ] Build with push: `scion build codex --push` → image pushed to registry
- [ ] Error path: missing Dockerfile → clear error message
- [ ] Error path: docker not running → clear error message

#### 2b. Auth capture flow implementation

**Prerequisite:** None (independent of build and import).

This follows the existing harness-auth-flow design doc at `/scion-volumes/scratchpad/projects/harness-auth-flow/design.md`.

**Phase 2b-1: Agent-initiated secrets (core plumbing)**
- Hub handler: `PUT /api/v1/agents/{agentID}/secrets/{key}`
- `sciontool secret set KEY VALUE [--type file] [--target PATH]`
- Tests: agent JWT auth, project-scope storage, force/no-force

**Phase 2b-2: No-auth broker dispatch**
- Add `NoAuth` to `RemoteCreateAgentRequest` and `CreateAgentRequest`
- Skip `resolveSecrets()` when `NoAuth=true`
- Tests: broker-dispatched no-auth agent gets zero credentials

**Phase 2b-3: Harness capture scripts**
- Python `capture_auth.py` per harness (Claude, Codex, Gemini, OpenCode)
- Config-driven `OverlayFileSecrets()` (move hardcoded switch to config.yaml)
- `no_auth` behavior config in harness configs

**Validation checklist:**
- [ ] Start no-auth agent (local): `scion start --no-auth` → container starts with zero creds
- [ ] Start no-auth agent (broker): Hub-dispatched agent starts with zero creds
- [ ] Inside container: authenticate with harness CLI (e.g., `claude login`)
- [ ] Run capture script: credentials captured and stored as project secrets
- [ ] Start subsequent agent: automatically receives captured credentials

---

### Phase 3: Reimport → Rebuild Automation

**Goal:** Connect the update flow to the build flow with optional automation.

**Duration estimate:** 1-2 days

**Prerequisite:** Phase 1 (source URL + reimport) and Phase 0 (build executor merged).

#### 3a. Post-reimport rebuild prompt (UI)

After a successful reimport that changed content (different `content_hash`):
- UI displays: "Config updated — would you like to rebuild the image?"
- User clicks "Rebuild" → triggers build via existing maintenance operation
- Or dismisses → no build

This is a UI-only change on the harness-config detail page.

#### 3b. CLI auto-rebuild flag

```
scion harness-config update <name> --rebuild
```

After reimport, if content changed and `--rebuild` is set:
- Automatically trigger `scion build <name>`
- Report combined result

#### 3c. Additional metadata fields (optional)

If useful for the UI:
```go
field.Time("last_imported_at").Optional()
field.Time("last_built_at").Optional()
```

These track when each lifecycle stage last ran, independent of the generic `updated` timestamp. The detail page could show: "Last imported: 2 hours ago | Last built: 1 day ago | Source: github.com/..."

---

## 4. Dependency Graph

```
Phase 0: Merge PRs (#403, #404, #406)
    │
    ├─── Phase 1: source_url storage + reimport (INDEPENDENT)
    │        │
    │        └─── Phase 3: reimport → rebuild automation
    │
    ├─── Phase 2a: Build flow validation (NEEDS Phase 0)
    │        │
    │        └─── Phase 3: reimport → rebuild automation
    │
    └─── Phase 2b: Auth capture implementation (INDEPENDENT)
```

**Critical path:** Phase 1 → Phase 3 (source_url is the foundation)

**Parallel work streams:**
- Phase 1 (source_url) can start immediately — no dependency on Phase 0
- Phase 2b (auth) can start immediately — fully independent
- Phase 2a (build validation) requires Phase 0 PRs merged first
- Phase 3 requires both Phase 1 and Phase 2a

---

## 5. Open Questions for User

### Q1: Reimport command naming — RESOLVED

**Decision:** New `scion harness-config update [name]` command. Leverages stored URL, enables batch updates. Confirmed by user 2026-06-13.

### Q2: Scope of "update all" — RESOLVED

**Decision:** Name is required by default. Batch mode via explicit `--all` flag: `scion harness-config update --all`. Without name or `--all`, command errors with usage hint. Confirmed by user 2026-06-13.

### Q3: Template source_url too? — RESOLVED

**Decision:** Yes, add `source_url` to both templates and harness-configs. Shared ResourceStore makes this trivial. Confirmed by user 2026-06-13.

### Q4: Auth capture priority vs. build validation — RESOLVED

**Decision:** Do both in parallel. Confirmed by user 2026-06-13.

### Q5: Metadata display — RESOLVED

**Decision:** Detail page shows full provenance: source URL (clickable), last imported, last built timestamps, content hash in advanced section. List view shows last updated time in short form. Same treatment for both templates and harness-configs. Confirmed by user 2026-06-13.

---

## 6. Key File Reference

| Component | File | Purpose |
|-----------|------|---------|
| Ent schema (harness) | `pkg/ent/schema/harnessconfig.go` | Add `source_url` field |
| Ent schema (template) | `pkg/ent/schema/template.go` | Add `source_url` field |
| Store model | `pkg/store/models.go:540` | Add `SourceURL` to struct |
| Resource store | `pkg/hub/resource_store.go:43` | Add to `ResourceRecord`, thread through Bootstrap |
| Import driver | `pkg/hub/resource_import.go` | Thread `sourceURL` to workers |
| CLI install | `cmd/harness_config_install.go` | Set source URL on install |
| CLI update (new) | `cmd/harness_config_update.go` | New reimport command |
| Hub handlers | `pkg/hub/handlers.go` | New reimport endpoint, include `source_url` in responses |
| Detail page | `web/src/components/pages/harness-config-detail.ts` | "Refresh from Source" button, metadata display |
| Build executor | `pkg/hub/maintenance_executors.go` | (Phase 0 — on branch) |
| Build CLI | `cmd/build.go` | (Phase 0 — on branch) |
| Auth design | `/scion-volumes/scratchpad/projects/harness-auth-flow/design.md` | Reference for Phase 2b |
| QA findings | `/scion-volumes/scratchpad/projects/harness-local-build/qa-findings.md` | Known issues for Phase 2a |

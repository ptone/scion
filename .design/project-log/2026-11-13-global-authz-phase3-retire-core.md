# Skills: retire the core skill scope with an idempotent data migration (ptone/scion#1713 Phase 2, slice 3)

**Date:** 2026-11-13
**Branch:** scion/global-authz-core
**Issue:** ptone/scion#1713

## Behaviour

- Retired `store.SkillScopeCore` across `pkg/`, `cmd/`, `web/`, and `docs-site/src/content/docs/local/skills.md` (based on `upstream-main` with Phase 1 `GoogleCloudPlatform/scion#4796` merged):
  - Removed `store.SkillScopeCore` from `pkg/store/models.go`, `pkg/store/store.go`, `pkg/store/entadapter/skill_store.go`, `pkg/hub/authz_catalog_scope.go` (dropping the interim `core` arm from `catalogWriteAction`), `pkg/hub/authz_skill_scope.go`, `pkg/hub/authz_boundary.go`, `pkg/hub/authz_kernel_vocab.go`, `pkg/hub/authz.go`, `pkg/hub/authz_skill_progeny.go`, `pkg/hub/permissions/project_applicability.go`, and `pkg/hub/handlers_skills_injection.go`.
  - Updated `pkg/hub/skill_handlers.go`: `listSkills` and `createSkill` return `400 Bad Request` (`validation_error`) naming `"global"` (`skill://scion/global/<name>`) when `scope="core"` is requested; `determineScopeSearchOrder` searches `user -> project -> global`.
  - Updated `pkg/api/skill_uri.go`: `ParseSkillURI` rejects `skill://core/<name>`, `skill://scion/core/<name>`, `skill:///core/<name>`, and `skill://<registry>/core/<name>` with a clear error naming `skill://scion/global/<name>` (or `skill://<registry>/global/<name>` for federated registries), keeps `skill://core` / `skill://core@1.0` working as the skill named `"core"`, and accepts the two-segment `skill://global/<name>` alias alongside `skill://project/<name>` and `skill://user/<name>`.
  - Updated `cmd/skills.go`: `scion skills list --scope core` and `scion skills publish --scope core` fail fast with a usage error naming `"global"`, and `--scope` help text lists `global, project, user`.
  - Updated `web/src/shared/types.ts`, `web/src/components/pages/skills.ts`, and `web/src/components/shared/injected-skills-panel.ts` to drop `'core'` from `SkillScope` and scope filter/create options, and to use canonical `skill://scion/global/my-skill` in the URI-mode input placeholder.
  - Updated `docs-site/src/content/docs/local/skills.md` and `resources/platform_skills/team-creation/SKILL.md` scope hierarchy and Hub catalog examples.
- Added an idempotent single-transaction startup data migration `entc.MigrateSkillCoreScope` (`pkg/ent/entc/migrate_skill_core_scope.go`, wired into `entc.AutoMigrate` in `pkg/ent/entc/client.go`) running on both SQLite and PostgreSQL via `*ent.Client`:
  1. Rewrites `scope="core"` skill rows without a same-slug `scope="global"` row to `scope="global"`, leaving `storage_path` and `storage_uri` unchanged.
  2. Pre-seeds occupied slugs with both `scope="global"` and `scope="core"` slugs so non-colliding `core` rows always retain their slug regardless of creation order; on slug collision with an existing `scope="global"` row, renames the `core` row's slug to `<slug>-core` (then `<slug>-core-2`, `<slug>-core-3`, ... truncated within the 64-character slug limit), sets `scope="global"`, and emits a structured `slog.Warn` after transaction commit recording `skill_id`, `old_slug`, and `new_slug`.
  3. Rewrites stored `skill://core/<slug>` (plus `skill://scion/core/<slug>` and `skill:///core/<slug>`) entries in `hub_settings["injected_skills"].user_defined` and in project/user `SkillInjection` rows to the canonical `skill://scion/global/<new slug>` (preserving `@version` suffixes), deduplicating `user_defined` entries (logging `slog.Warn` with the dropped and kept entries' URIs and `as`/`optional` values) and logging `slog.Warn` with both row IDs when a rewritten `SkillInjection` row duplicates an existing global row and is dropped.
  4. Leaves template files untouched on disk and in object storage.

## Storage Path Audit and DowngradeFloor Analysis

- **Stored `StoragePath` audit (zero object moves needed):** Confirmed every skill read, download, dispatch-resolve, file-serve, file-write, upload, and finalize path uses the persisted `skill.StoragePath` rather than recomputing from `skill.Scope` or `skill.Slug`:
  - `pkg/hub/skill_handlers.go:1152` (`publishSkillVersion`: uploads to `skill.StoragePath + "/" + req.Version`)
  - `pkg/hub/skill_handlers.go:1320` (`publishSkillVersionMultipart`: uploads to `skill.StoragePath + "/" + version + "/" + fh.Filename`)
  - `pkg/hub/skill_handlers.go:1424` (`handleSkillUpload`: generates signed upload URLs under `skill.StoragePath + "/" + req.Version`)
  - `pkg/hub/skill_handlers.go:1548` (`handleSkillFinalize`: verifies uploaded objects under `skill.StoragePath + "/" + req.Version`)
  - `pkg/hub/skill_handlers.go:1625` (`handleSkillDownload`: generates download URLs from `skill.StoragePath + "/" + sv.Version`)
  - `pkg/hub/skill_file_handlers.go:222` (`serveSkillFile`: reads file content from `skill.StoragePath + "/" + sv.Version`)
  - `pkg/hub/skill_file_handlers.go:313` (`handleSkillFileWrite`: writes draft file to `skill.StoragePath + "/" + version + "/" + filePath`)
  - `pkg/hub/skill_dispatch_resolve.go:108` (`resolveRegistrySkillRef`: generates agent-dispatch download URLs from `skill.StoragePath + "/" + sv.Version`)
  - `pkg/hub/skill_handlers.go:837-867` (`deleteSkill`: soft-deletes in DB without touching object storage)
  - `storage.SkillStoragePath` / `storage.SkillStorageURI` are called only when creating a new skill (`pkg/hub/skill_handlers.go:732-738`), and `pkg/hub/storage_migration.go:197-276` copies from stored `sk.StoragePath` before updating `sk.StoragePath` when namespacing legacy `"skills/<slug>"` paths.
- **`pkg/version.DowngradeFloor` needs no bump:** Under `AGENTS.md`, a destructive DB migration drops a table or column, or makes a schema change older binaries cannot ignore. `MigrateSkillCoreScope` makes zero DDL changes; it rewrites `skills.scope` to `"global"` (an existing scope older binaries already support), renames colliding slugs to valid `<= 64`-char kebab-case slugs (`<slug>-core`), and rewrites `skill://core/...` injection references to the canonical `skill://scion/global/...` form that both pre-Phase-3 and new binaries resolve as `Registry="scion"`, `Scope="global"`.
- **Adjacent observation (not modified):** `pkg/hub/skill_handlers.go:expandScopeAliases` (`:1897-1904`) only sets `uri.ScopeID = userID` when `uri.Scope == store.SkillScopeUser`, while `determineScopeSearchOrder` (`:1885`) checks `if uri.ScopeID != ""` on unscoped URIs (`uri.Scope == ""`), causing `POST /api/v1/skills/resolve` for an unscoped `<slug>` to skip the `user` scope rung even when `UserID` is supplied on `ResolveSkillsRequest`.

## Tests

- `pkg/ent/entc/migrate_skill_core_scope_test.go` and `pkg/ent/entc/migrate_skill_core_scope_postgres_integration_test.go`: `TestMigrateSkillCoreScope_RewritesToGlobal`, `TestMigrateSkillCoreScope_CollisionRenamesWithSuffix` (including `-core-2` and 64-character truncation), `TestMigrateSkillCoreScope_CollisionDoesNotStealSiblingCoreSlug` (both creation orders), `TestMigrateSkillCoreScope_RewrittenURI_ParsesAsScionGlobal`, `TestMigrateSkillCoreScope_RewritesInjectionReferences` (hub, project, and user scopes, `@version` preservation, `user_defined` deduplication, and `SkillInjection` duplicate-drop warning with both row IDs), `TestMigrateSkillCoreScope_Idempotent`, `TestMigrateSkillCoreScope_KeepsStoragePath`, and `TestMigrateSkillCoreScope_Postgres`.
- `pkg/api/skill_uri_test.go`: `TestParseSkillURI_CoreScopeRejected` (including federated registry preservation), `skill://core` / `skill://core@1.0` skill-name coverage, and `skill://global/<name>` alias coverage.
- `pkg/hub/skill_core_retirement_test.go`: `TestCreateSkill_CoreScopeRejected`, `TestListSkills_CoreScopeRejected`, `TestDetermineScopeSearchOrder`, `TestResolveSkill_DefaultSearchOrderUserProjectGlobal`, and end-to-end `TestMigrateSkillCoreScope_KeepsStoragePath_FilesDownloadable`.
- `resources/catalog_test.go`: `TestPlatformSkillsFS_NoRetiredCoreSkillScopeURIs`.
- `pkg/hub/skill_global_authz_test.go`: updated `TestCatalogWriteAction_MapsCorrectly` removing the interim `core` cases.
- `cmd/skills_core_scope_test.go`: `TestSkillsCommands_CoreScopeRejected` (`scion skills list --scope core` and `scion skills publish --scope core`).
- `web/src/components/pages/skills.test.ts` and `web/src/components/shared/injected-skills-panel.test.ts`: updated scope filter/create assertions and `skill://global/...` injection builder tests.

## Verification

- `go test -buildvcs=false -count=1 ./pkg/ent/entc/... ./pkg/api/... ./pkg/store/... ./pkg/config/... ./cmd/...` — PASS.
- `make check-authorization-catalog` and `make check-route-authz-manifest` — PASS.
- `make fmt-check`, `make lint-ci`, `make vet-integration-ci`, `make check-custom`, `make tidy-check` — PASS.
- `GOGC=40 golangci-lint run --new-from-rev=upstream-main --concurrency=1` (scoped across touched non-hub packages with tests and `./pkg/hub` with `--tests=false`) — PASS (0 issues).
- `make vet-hub-shard SHARD=1..6`, `make test-fast-ci`, `make test-fixture-coverage`, and `make test-hub-shard SHARD=1..6` — PASS.
- `GOOS=windows GOARCH=amd64 go build -buildvcs=false -o /dev/null ./cmd/scion` and `bash hack/winsec-guard.sh` — PASS.
- `make web`, `make web-budget`, `make web-typecheck`, `make web-lint`, and `make web-test` — PASS.

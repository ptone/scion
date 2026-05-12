# Skill Bank Phase 1C: Hub API + Hub Client

**Date**: 2026-05-12
**Agent**: dev-skill-bank-1c
**Branch**: scion/manage-skill-bank

## Summary

Implemented the Hub-side HTTP handlers for skill CRUD, file transfer (signed URLs), batch resolution, and the corresponding hub client `SkillService`. Also implemented the SQLite store for skills and skill versions to enable end-to-end testing.

## Deliverables

### New Files
1. **`pkg/hub/skill_handlers.go`** — Hub API handlers:
   - `handleSkills` — dispatches GET (list) / POST (create) on `/api/v1/skills`
   - `handleSkillRoutes` — routes `/api/v1/skills/{id}[/action]` requests
   - `handleSkillResolve` — POST `/api/v1/skills/resolve` for batch resolution
   - Skill CRUD: get, update, delete
   - Version operations: list, publish, get
   - URI parsing: `parseSkillURI()` for `skill://<registry>/<scope>/<name>@<version>` and bare names
   - Request/response types for all endpoints

2. **`pkg/hub/skill_file_handlers.go`** — File transfer handlers:
   - `handleSkillVersionUpload` — generates signed upload URLs
   - `handleSkillVersionFinalize` — verifies files and marks version as published
   - `handleSkillVersionDownload` — generates signed download URLs
   - `computeSkillContentHash()` — SHA-256 content hash
   - Local storage URL rewriting for skills

3. **`pkg/hubclient/skills.go`** — Hub client SkillService:
   - `SkillService` interface with 13 methods
   - `skillService` implementation using `apiclient.Transport`
   - All CRUD, version, upload/download, and resolve operations
   - Transfer client integration for file uploads/downloads

4. **`pkg/hub/skill_handlers_test.go`** — 12 handler tests covering CRUD, validation, version lifecycle, batch resolve, URI parsing, and content hashing

5. **`pkg/hubclient/skills_test.go`** — 9 client tests covering list, get, create, update, delete, publish version, list versions, resolve, and accessor wiring

### Modified Files
1. **`pkg/hub/server.go`** — Added route registration for `/api/v1/skills`, `/api/v1/skills/resolve`, `/api/v1/skills/`
2. **`pkg/hubclient/client.go`** — Added `Skills()` to `Client` interface and `client` struct, wired initialization
3. **`pkg/hubclient/client_test.go`** — Added Skills() nil check to TestNew
4. **`pkg/storage/storage.go`** — Added `SkillStoragePath`, `SkillStorageURI`, `SkillVersionStoragePath` helpers
5. **`pkg/store/sqlite/skills.go`** — Full implementation replacing stubs: all SkillStore methods with working SQLite queries

## Patterns Followed
- Route registration follows template pattern (`handleTemplatesV2` / `handleTemplateByIDV2`)
- Handler structure mirrors `template_handlers.go` (method dispatch, error handling, JSON responses)
- Hub client mirrors `templateService` (interface + struct, transport delegation, transfer client)
- Storage paths follow `TemplateStoragePath` convention (scope-based organization)
- Tests follow existing `testServer()` / `doRequest()` / httptest patterns

## Key Design Decisions
- Batch resolve (`/api/v1/skills/resolve`) returns partial results: resolved skills in `resolved`, failures in `errors`, deprecation notices in `warnings`
- URI parsing supports full form (`skill://scion/global/scion@1.0.0`), omitted registry (`skill:///global/scion`), and bare names (`scion`)
- Skill versions use delete+recreate during finalize since the store interface doesn't expose an UpdateSkillVersion method
- "grove" → "project" naming used in all new code (storage paths use "projects" not "groves")

## Verification
- `go build ./...` passes
- All 12 hub handler tests pass
- All 9 hubclient tests pass
- All existing tests in hub, hubclient, store/sqlite, and storage continue to pass

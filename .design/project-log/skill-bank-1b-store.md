# Skill Bank Phase 1B: Store Layer Implementation

**Date**: 2026-05-12
**Agent**: dev-skill-bank-1b
**Branch**: scion/manage-skill-bank

## Summary

Implemented the full SQLite `SkillStore` — all 11 methods defined in the `SkillStore` interface — replacing the stubs created in Phase 1A.

## Deliverables

### `pkg/store/sqlite/skills.go`
- **Skill CRUD**: `CreateSkill`, `GetSkill`, `GetSkillByName`, `UpdateSkill`, `DeleteSkill`, `ListSkills`
- **Version CRUD**: `CreateSkillVersion`, `GetSkillVersion`, `GetSkillVersionByNumber`, `ListSkillVersions`, `DeleteSkillVersion`
- **Scan helpers**: Shared `rowScanner` interface with `scanSkillFromRow` / `scanSkillVersionFromRow` to avoid duplicating scan logic between single-row and multi-row queries
- JSON serialization for `Labels`, `Annotations` (maps), and `Files` (slices) using existing `marshalJSON`/`unmarshalJSON` helpers

### `pkg/store/sqlite/skills_test.go`
- 40 test cases covering:
  - Basic CRUD for skills and versions
  - Default value injection (status, scope, timestamps)
  - Input validation (missing required fields)
  - Duplicate detection (UNIQUE constraint on name+scope+scope_id)
  - Same-name-in-different-scopes allowed
  - Not-found error paths for all get/update/delete methods
  - Cascade delete (deleting a skill removes all versions via FK)
  - List filtering: by scope, status, owner, search (LIKE on name+description), and combined filters
  - Pagination with limit and cursor
  - JSON round-trip for Labels/Annotations maps and Files arrays
  - Nil map/slice handling (no panics on empty metadata)
  - Full lifecycle integration test (create → version → update → lookup → delete)

## Patterns Followed

- **Schedule.go pattern**: CRUD structure, `scan*` helpers, error wrapping, `RowsAffected()` checks for updates/deletes
- **marshalJSON/unmarshalJSON**: Reused existing helpers from `sqlite.go` for map/slice fields
- **Store error constants**: `ErrNotFound`, `ErrAlreadyExists`, `ErrInvalidInput` from `pkg/store`
- **Test patterns**: `setupTestStore(t)` with in-memory SQLite, `api.NewUUID()` for IDs, `require`/`assert` from testify

## Design Observations

- The `SkillStore` interface in `store.go` uses `GetSkillByName` (not `GetSkillBySlug` as in the design doc). The actual types also use `Name` instead of `Slug` — the implementation follows the committed code, not the design doc.
- The `ResolveSkillVersion` method mentioned in the design doc is not in the committed interface. Semver resolution will likely be implemented at a higher layer (Hub API or provisioning) using `ListSkillVersions` + in-memory filtering.
- `SkillFile` round-trips correctly through JSON serialization in the `files` TEXT column.

## Verification

- `go test ./pkg/store/sqlite/ -run TestSkill -v` — all 40 tests pass
- `go build ./...` — clean build

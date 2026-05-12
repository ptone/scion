# Skill Bank Phase 1A: Types, Store Interface, and Migration

**Date:** 2026-05-12
**Agent:** dev-skill-bank-1a
**Branch:** scion/dev-skill-bank-1a

## Summary

Implemented the foundational types, store interface, and database migration for the Skill Bank feature (Issue #29). This is Phase 1A — the data layer scaffolding that later phases will build upon.

## What Was Done

### 1. API Types (`pkg/api/types.go`)
- Added `SkillReference` struct with `Name` (slug) and `Version` (semver constraint) fields
- Added `Skills []SkillReference` field to `ScionConfig` for declaring skill dependencies in templates

### 2. Store Models (`pkg/store/models.go`)
- Added `Skill` model — represents a reusable skill definition with identity, scope (global/project/user), status (draft/active/archived), metadata, and ownership fields
- Added `SkillVersion` model — represents an immutable versioned snapshot of a skill's content, with content hash, file manifest, status (draft/published/deprecated), and changelog
- Added `SkillFile` model — represents a file within a skill version (path, size, hash, mode)
- Added scope, status, and version status constants

### 3. Store Interface (`pkg/store/store.go`)
- Added `SkillStore` interface with CRUD operations for both skills and skill versions:
  - `CreateSkill`, `GetSkill`, `GetSkillByName`, `UpdateSkill`, `DeleteSkill`, `ListSkills`
  - `CreateSkillVersion`, `GetSkillVersion`, `GetSkillVersionByNumber`, `ListSkillVersions`, `DeleteSkillVersion`
- Added `SkillFilter` struct for querying skills by name, scope, status, owner, and search text
- Composed `SkillStore` into the main `Store` interface

### 4. Database Migration (`pkg/store/sqlite/sqlite.go`)
- Added `migrationV51` creating two tables:
  - `skills` — with unique constraint on (name, scope, scope_id) and indexes on scope, status, and owner
  - `skill_versions` — with FK to skills (CASCADE delete), unique constraint on (skill_id, version), and indexes on skill_id and status

### 5. Stub Implementation (`pkg/store/sqlite/skills.go`)
- Created stub implementations of all `SkillStore` methods that return `ErrNotFound` / empty results
- Ensures the build compiles and passes with the new interface

## Verification
- `go build ./...` — passes
- `go vet ./...` — passes
- `go test ./pkg/store/sqlite/` — all tests pass (including migration execution)

## Design Decisions
- Followed existing patterns: models in `models.go`, interface in `store.go`, migration as const string, stubs in a separate file
- Used "project" terminology throughout (not "grove") per the rename convention
- Kept `SkillFile` consistent with existing `TemplateFile` struct for familiarity
- Scope and status constants mirror the template/harness-config patterns already in the codebase

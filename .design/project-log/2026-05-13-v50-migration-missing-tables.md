# V50 Migration: Missing templates and harness_configs Scope Update

**Date:** 2026-05-13
**Commit:** 392c4a59
**Branch:** fix/workspace-path-fallback

## Problem

The V50 migration (grove-to-project rename, Phase 4) updated scope values from `'grove'` to `'project'` in 7 tables but missed two:

1. **templates** - has `scope TEXT NOT NULL DEFAULT 'global'` (from V16 schema)
2. **harness_configs** - has `scope TEXT NOT NULL DEFAULT 'global'` (from V16 schema)

The application code queries these tables with `scope='project'`, so any rows that still had `scope='grove'` were invisible to the application.

## Fix

1. **V50 updated**: Added `UPDATE templates` and `UPDATE harness_configs` scope updates to the data updates block inside `migrateV50()`. These are idempotent (UPDATE WHERE is a no-op when no matching rows exist).

2. **V51 catch-up migration added**: For hubs that already ran the broken V50, a new `migrationV51` const runs the same two UPDATE statements. This ensures databases in the wild get corrected regardless of when they first ran V50.

## Verification

- `go build ./...` passes
- `go vet ./...` passes

## Process Notes

- Found the issue by auditing all tables with a `scope` column against the V50 data updates block
- The task flagged templates as missing; investigation also revealed harness_configs was missed
- Both V50 fix and V51 catch-up are idempotent, so re-running migrations is safe

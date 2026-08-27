# seedFromWave1 muted-flag migration: asymmetry documentation

**Date:** 2026-08-27
**Branch:** scion/dev-p2f2-comments
**Commit:** bc1b0ed

## What

Added documentation comments to the muted-flag section of `seedFromWave1` in both
webchat store backends explaining why they use different SQL mechanisms.

## Why

The asymmetry between backends is intentional but was undocumented. Without
comments, future sweep audits would waste time re-investigating whether the
difference is a bug or by design.

## Details

- **SQLite** (`pkg/hub/webchannel_store.go`): Uses a two-step INSERT OR IGNORE +
  UPDATE because SQLite's INSERT OR REPLACE would drop other columns on existing rows.
- **Postgres** (`pkg/hub/webchannel_store_postgres.go`): Uses a single
  INSERT...ON CONFLICT DO UPDATE SET muted = EXCLUDED.muted because Postgres's
  ON CONFLICT DO UPDATE can target one column without affecting others.

Both produce identical outcomes. Each comment now references the sibling backend
so the difference reads as intentional.

## Files changed

- `pkg/hub/webchannel_store.go` (comment only)
- `pkg/hub/webchannel_store_postgres.go` (comment only)

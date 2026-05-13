# Grove→Project Rename: Cleanup Survey

**Date:** 2026-05-13  
**Agent:** survey-grove  
**Task:** Survey entire codebase for remaining "grove" references and categorize them

## Summary

Surveyed 316 files containing 4,499 "grove" references across the codebase. Findings written to `.scratch/grove-cleanup-survey.md`.

## Key Findings

- **3 bugs found** — `scion-chat-app` uses removed `hubclient` API methods (`Groves()`, `GroveAgents()`, `ListGrovesOptions`) and won't compile
- **~1,700 CLEANUP references** — variable names, comments, log messages, help text, user-facing prompts that can be renamed now
- **~1,570 COMPAT references** — intentional backward-compatibility code (JSON marshal/unmarshal, env vars, container labels, API endpoints) that should be marked with `// COMPAT(grove-rename):` comments
- **~100 ALREADY CORRECT references** — migration code where "grove" describes what's being migrated from

## Recommended Priority

1. Fix `scion-chat-app` compilation (critical bug)
2. Update user-facing prompt strings in `hubsync/prompt.go` and CLI help text
3. Rename internal variable names (`groveID` → `projectID`, etc.)
4. Add `// COMPAT(grove-rename):` comments to intentional compat code
5. Defer wire-format changes (JSON fields, env vars, labels) to coordinated breaking change

## Process Notes

- Used `grep -rni` to find all references, processed in batches by directory
- Verified `scion-chat-app` compilation failure with `go build ./...`
- Cross-referenced event topics to confirm dual-publishing pattern (both `project.` and `grove.` topics)
- Identified that container labels, env vars, and NATS topics form a coordinated compat surface that should be removed together

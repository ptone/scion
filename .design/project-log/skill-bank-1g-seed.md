# Phase 1G: Seed Data & Template Migration for Skill Bank

**Date**: 2026-05-12
**Agent**: dev-skill-bank-1g
**Branch**: scion/manage-skill-bank

## Summary

Implemented the final phase of the Skill Bank (Phase 1G): seeding core skills on Hub startup and updating default templates to reference skills from the Skill Bank.

## Changes

### 1. `pkg/hub/skill_bootstrap.go` (new)
- `BootstrapSkillsFromDir()` — scans a directory for skill subdirectories (identified by containing `SKILL.md`), imports new skills and syncs changed ones
- `bootstrapSingleSkill()` — imports a single skill directory as a global-scope skill with version 1.0.0
- `syncExistingSkill()` — compares on-disk files against stored content hash, re-uploads if changed
- Follows the same idempotent pattern as `template_bootstrap.go`

### 2. `cmd/server_foreground.go`
- Wired `BootstrapSkillsFromDir()` into Hub startup, after template and harness-config bootstrap
- Uses `~/.scion/skills/` as the skills directory (consistent with `~/.scion/templates/` pattern)

### 3. `pkg/config/embeds/templates/default/scion-agent.yaml`
- Added `skills:` section with references to `scion@^1.0` and `team-creation@^1.0`
- Both marked `optional: true` for backward compatibility (skills resolve only when Hub has Skill Bank populated)
- Local `skills/` directory retained with `.gitkeep` for backward compatibility

### 4. `pkg/templatecache/hydrator_test.go`
- Added missing `Skills()` method to `mockHubClient` to satisfy the updated `hubclient.Client` interface (from Phase 1C)

## Design Decisions

- **Global scope (not "core")**: The design doc proposes a `core` scope for first-party skills, but the current store implementation only has `global`, `project`, `user`. Used `global` scope since the `core` scope wasn't added to the store/storage layer in earlier phases. This can be updated when the `core` scope is implemented.
- **Bare name URIs**: Used bare name format (`scion@^1.0`) instead of full URIs (`skill://scion/core/scion@^1.0`) in the template YAML, since the scope-search resolution handles lookups across all scopes.
- **Optional skills**: Marked skill references as `optional: true` so that agents can still be provisioned even when the Hub doesn't have skills seeded. This ensures backward compatibility.

## Verification

- `go build ./...` passes
- `go vet ./...` passes
- All relevant tests pass: `pkg/hub`, `pkg/store`, `pkg/agent`, `pkg/templatecache`

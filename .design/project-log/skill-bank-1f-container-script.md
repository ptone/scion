# Phase 1F: Container-Script Harness Support for Skill Bank

**Date**: 2026-05-12
**Agent**: dev-skill-bank-1f
**Branch**: scion/manage-skill-bank

## Summary

Extended the container-script provisioning pipeline so that `provision.py` scripts
can see which skills were resolved from the skill bank. This enables container-side
scripts to post-process skills (e.g., transform for non-standard harness formats).

## Changes

### `pkg/api/harness.go`
- Added `ResolvedSkillRecord` struct describing a resolved skill (name, URI, version,
  content hash, installed path, source).
- Added `ResolvedSkillsApplier` optional interface following the same pattern as
  `MCPSettingsApplier` and `TelemetrySettingsApplier`.

### `pkg/harness/container_script_harness.go`
- Added `ResolvedSkills` field to `ProvisionInputs` struct.
- Implemented `ApplyResolvedSkills()` method on `ContainerScriptHarness` — stages
  `inputs/resolved-skills.json` with schema version and skills array.
- Added manifest detection for `resolved-skills.json` in `Provision()` method.

### `pkg/agent/skill_resolve.go`
- Changed `resolveSkillReferences` return type from `error` to
  `([]api.ResolvedSkillRecord, error)` so callers receive metadata about resolved skills.
- Each successfully downloaded registry skill produces a `ResolvedSkillRecord`.

### `pkg/agent/provision.go`
- Updated `resolveSkillReferences` call site to capture returned records.
- After skill resolution, calls `ApplyResolvedSkills` on harnesses that implement
  the `ResolvedSkillsApplier` interface.

### `pkg/harness/embeds/scion_harness.py`
- Added `read_resolved_skills(bundle_path)` helper that reads
  `inputs/resolved-skills.json` and returns the skills list (or empty list if absent).

### Tests
- Updated all `resolveSkillReferences` test call sites for new return type.
- Added record verification to `TestResolveSkillReferences_SuccessfulDownload` and
  `TestResolveSkillReferences_AsRename`.
- Added three new tests in `container_script_harness_test.go`:
  - `TestContainerScriptHarness_ApplyResolvedSkills_WritesInput`
  - `TestContainerScriptHarness_ApplyResolvedSkills_NoOpEmpty`
  - `TestContainerScriptHarness_ProvisionReferencesResolvedSkillsInManifest`

## Design Decisions

- Followed the existing `MCPSettingsApplier` pattern exactly: optional interface,
  no-op for empty input, staged as versioned JSON under `inputs/`.
- `resolveSkillReferences` returns records rather than requiring a separate data-gathering
  pass, keeping the resolution + metadata collection atomic.
- Built-in harnesses (claude, gemini) do not implement `ResolvedSkillsApplier` since
  they place skills directly into `SkillsDir()` without needing a manifest.

## Verification

- `go build ./...` passes
- `go vet ./pkg/agent/ ./pkg/harness/ ./pkg/api/` passes
- `go test ./pkg/harness/ ./pkg/agent/` passes (all tests green)

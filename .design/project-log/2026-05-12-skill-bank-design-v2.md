# Skill Bank Design Doc v2

**Date**: 2026-05-12
**Agent**: design-skill-bank
**Task**: Write design document for Skill Bank feature (#29)

## Summary

Wrote comprehensive design doc at `.design/skill-bank-design-v2.md` covering the Skill Bank — a centralized registry and late-binding resolution system for agent skills.

## Key design decisions

1. **URI scheme**: `skill://<registry>/<scope>/<name>@<version>` with shorthand forms for convenience.
2. **Follows template patterns**: Store interface, hub handlers, signed-URL file transfer, and scoping model all mirror the existing template infrastructure.
3. **Provisioning injection point**: After local skills copy (line ~615 in provision.go), before agent instructions injection. Registry skills win on name conflict with local skills.
4. **Four scopes**: core (new, admin-only), global, project, user — narrowest wins on bare-name resolution.
5. **Content-hash integrity**: SHA-256 hashes verified at every transfer boundary. Immutable versions once published.
6. **Container-script compatibility**: Extended `ProvisionManifest.Inputs` with `resolved_skills` field; staged `resolved-skills.json` manifest for `provision.py` post-processing.
7. **Three-phase migration**: Phase 1 (core registry), Phase 2 (caching + federation), Phase 3 (discovery + governance).

## Files explored

- `pkg/agent/provision.go` — provisioning pipeline, skills copy at lines 575–615
- `pkg/api/types.go` — ScionConfig struct
- `pkg/store/store.go` — Store interface composition pattern
- `pkg/store/models.go` — Template/TemplateFile models, scope constants
- `pkg/store/sqlite/sqlite.go` — Migration pattern (V1–V50)
- `pkg/hub/server.go` — Route registration pattern
- `pkg/hub/template_handlers.go` — Handler dispatch pattern, signed URL expiry
- `pkg/hubclient/templates.go` — TemplateService interface pattern
- `pkg/harness/container_script_harness.go` — ProvisionManifest, ProvisionInputs
- `skills/` — Existing skill format (scion, team-creation)

## Open questions sent to reviewer

See design doc §18 for 7 open questions requiring input before implementation.

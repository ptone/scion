# Skill Bank Phase 1D — CLI Commands

**Date**: 2026-05-12
**Agent**: dev-skill-bank-1d
**Branch**: scion/manage-skill-bank

## Summary

Implemented the `scion skills` CLI command group for skill bank management,
following the patterns established by `cmd/templates.go`.

## Deliverables

### 1. `pkg/hubclient/skills.go` — Hub Client Skill Service

- Defined `SkillService` interface with: List, Get, Create, Delete, PublishVersion,
  ListVersions, RequestUploadURLs, Finalize, RequestDownloadURLs, Resolve,
  UploadFile, DownloadFile
- Defined all request/response types: `Skill`, `SkillVersion`, `SkillFile`,
  `ListSkillsOptions`, `CreateSkillRequest`, `PublishVersionRequest`,
  `SkillManifest`, `ResolveSkillsRequest`, `ResolveSkillsResponse`, etc.
- Implemented `skillService` struct following the `templateService` pattern
- Wired `Skills()` accessor into `hubclient.Client` interface and `client` struct

### 2. `cmd/skills.go` — CLI Commands

Seven commands implemented:

| Command | Description |
|---------|-------------|
| `skills list` | List skills with --scope and --search filters |
| `skills show <name-or-id>` | Show detailed skill info |
| `skills create <name>` | Scaffold a local skill directory with SKILL.md |
| `skills publish <path>` | Publish a skill directory to Hub (create + version + upload + finalize) |
| `skills delete <name-or-id>` | Delete a skill from Hub with confirmation |
| `skills versions <name-or-id>` | List all versions of a skill |
| `skills resolve <uri>` | Test-resolve a skill URI for debugging |

All commands support both human-readable and `--format json` output.
A singular `skill` alias is registered alongside `skills`.

### 3. `cmd/cli_mode.go` — Mode Registration

Added all skill commands (and their singular aliases) to the `agentAllowed`
map so they are available in agent CLI mode.

## Design Decisions

- **Name-or-ID resolution**: `resolveSkillByNameOrID` tries name-based list
  lookup first, then falls back to direct ID GET. This mirrors the UX
  patterns in template commands.
- **Publish workflow**: The publish command handles the full flow —
  create skill if new, publish version, request upload URLs, upload files,
  build manifest, finalize. This matches the template sync pattern.
- **Singular alias**: Added `skill` as an alias for `skills` following the
  `template`/`templates` convention in the codebase.
- **Progress to stderr**: Uses `statusf`/`statusln` for progress messages so
  they're suppressed in JSON mode and don't pollute stdout.

## Observations

- The store models (`pkg/store/models.go`) and database migration already
  exist from Phase 1A.
- Pre-existing test failures in `cmd/` package (TestDeleteAgentsViaHub,
  TestResolveEnvScope, etc.) — not related to skill bank changes.
- Pre-existing formatting issues (164 files flagged by gofmt) — our files
  all pass formatting.

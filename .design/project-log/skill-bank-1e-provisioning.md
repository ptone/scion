# Skill Bank Phase 1E: Provisioning Integration

**Date**: 2026-05-12
**Agent**: dev-skill-bank-1e
**Branch**: scion/manage-skill-bank

## Summary

Integrated skill resolution into the agent provisioning pipeline so that `skills:` references in `scion-agent.yaml` are resolved from the Hub, downloaded, verified, and placed into the harness skills directory at agent creation time.

## What Changed

### New Files
- **`pkg/agent/skill_resolve.go`**: Core skill resolution logic
  - `ParseSkillURI()` — Parses skill URIs (`skill://registry/scope/name@version`) and bare name references into structured components
  - `resolveSkillReferences()` — Batch-resolves skill references via the Hub client, downloads files, verifies SHA-256 content hashes, and writes them to the skills directory
  - `ContextWithHubClient()` / `hubClientFromContext()` — Context helpers for passing the Hub client through the provisioning call chain
  - `skillRefToURI()` — Converts `SkillReference` (name+version or URI) to a URI string for the Hub API
  - `downloadSkillFiles()` — Downloads individual skill files with hash verification and correct file permissions

- **`pkg/agent/skill_resolve_test.go`**: Comprehensive test coverage
  - URI parsing tests (full form, bare name, errors)
  - URI string roundtrip tests
  - Context helper tests
  - Resolution tests: no-op, required/optional error handling, successful download, As-rename, hash mismatch, nested files, hub failure scenarios

### Modified Files
- **`pkg/api/types.go`**: Extended `SkillReference` with `URI`, `As`, and `Optional` fields alongside existing `Name`/`Version`
- **`pkg/agent/provision.go`**: Added Step 3c after local skills copy — resolves referenced skills from the Skill Bank when a Hub client is available
- **`pkg/runtimebroker/start_context.go`**: Added `HubConn` field to `startContext` struct, populated from `buildStartContext`
- **`pkg/runtimebroker/handlers.go`**: Injected Hub client into context at all four agent lifecycle entry points (createAgent provision, createAgent start, startAgent, restartAgent)

## Design Decisions

1. **Context-based Hub client propagation**: Rather than adding another parameter to the already-long `ProvisionAgent` signature, the Hub client is passed via `context.Context` following the existing pattern (`ContextWithBrokerMode`, `ContextWithGitClone`, etc.). This keeps the function signature stable and allows graceful degradation when no Hub is available.

2. **URI + Name/Version dual support**: The `SkillReference` type supports both URI-based references (`skill://scion/core/scion@^1.0`) and simple name+version references. If `URI` is set it takes precedence; otherwise, name+version is converted to a URI for the Hub resolve API.

3. **Registry skills override local skills**: Following the existing overlay pattern where later sources win on conflict, registry-resolved skills are placed after local skills copy, so they override local skills with the same name.

4. **Graceful degradation**: When no Hub client is available (local/solo mode), skill resolution is skipped. Optional skills are silently skipped on resolution failure. If the entire Hub is unreachable and all skills are optional, provisioning proceeds.

5. **Content hash verification**: Every downloaded file is verified against its SHA-256 hash from the Hub API response, using the existing `transfer.HashBytes()` utility.

## Verification
- `go build ./...` — passes
- `go vet ./pkg/agent/ ./pkg/api/ ./pkg/runtimebroker/` — clean
- `go test ./pkg/agent/ ./pkg/api/ ./pkg/runtimebroker/...` — all pass
- 14 new tests covering URI parsing, resolution, downloads, error handling, and edge cases

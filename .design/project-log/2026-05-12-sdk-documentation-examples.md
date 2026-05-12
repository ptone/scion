# SDK Documentation and Examples

**Date:** 2026-05-12
**Task:** Step 13 — Documentation and Examples for Python and TypeScript SDKs

## Summary

Added comprehensive README documentation and runnable example scripts for both the Python (`scion-sdk`) and TypeScript (`@scion/sdk`) SDKs.

## Deliverables

### READMEs
- **Python** (`sdk/python/README.md`): Replaced placeholder with full documentation covering installation, quick start, authentication (PAT, env vars, agent context), complete API reference for all 4 resources (agents, messaging, projects, secrets), SSE streaming, error handling, async usage, pagination, and type models.
- **TypeScript** (`sdk/typescript/README.md`): Created comprehensive README with installation, quick start, authentication, full API reference, streaming (async iteration and callbacks), error handling, pagination, ESM/CJS support, and type exports.

### Example Scripts
- **Python** (`sdk/python/examples/`):
  - `create_agent.py` — Create, poll, and optionally stream agent events until completion
  - `stream_logs.py` — Stream cloud logs with severity filtering and colorized output
  - `manage_secrets.py` — Full CRUD CLI for secrets with scope support
- **TypeScript** (`sdk/typescript/examples/`):
  - `create-agent.ts` — Create agent with polling and SSE streaming modes
  - `stream-logs.ts` — Stream cloud logs with ANSI-colored severity output
  - `manage-secrets.ts` — Full CRUD CLI for secrets with scope support

### Inline Documentation
- Enhanced docstrings on Python types: `Agent`, `Project`, `Secret`, `Message`, `StructuredMessage` with attribute documentation
- Enhanced JSDoc on TypeScript interfaces: `DirectConnect`, `ProjectProvider` field descriptions

## Verification
- Python: 280 tests pass, ruff reports only pre-existing TC001 warning (not introduced by this change)
- TypeScript: 174 tests pass, `tsc --noEmit` succeeds with no errors

## Process Notes
- Design doc path (`.design/sdk-design.md`) referenced in the brief does not exist; relied on reading all SDK source code directly for API accuracy
- Both READMEs were written to match the actual implemented API surface, not theoretical sketches
- Examples are structured as runnable CLI tools with `argparse`/manual arg parsing for real-world usability

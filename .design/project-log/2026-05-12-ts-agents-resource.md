# TypeScript SDK: Agents Resource (Step 7)

**Date:** 2026-05-12
**Author:** dev-ts-agents

## Summary

Implemented the TypeScript Agents resource module for the Scion Hub SDK at `sdk/typescript/`. Since the foundation (Steps 1-6) was not present on the branch, the full SDK scaffolding was also created.

## Deliverables

### Foundation (created from scratch)
- `sdk/typescript/package.json` — Project config with vitest, typescript
- `sdk/typescript/tsconfig.json` — Strict TypeScript compilation targeting ES2022+DOM
- `sdk/typescript/vitest.config.ts` — Test runner configuration
- `sdk/typescript/src/errors.ts` — Error hierarchy: `ScionError` > `ApiError` > `NotFoundError`, `AuthenticationError`, `ValidationError`, etc.
- `sdk/typescript/src/transport.ts` — HTTP transport layer with fetch, auth headers, timeout, JSON serialization, error parsing
- `sdk/typescript/src/pagination.ts` — Cursor-based `Page<T>` with `AsyncIterable` support for cross-page iteration
- `sdk/typescript/src/client.ts` — `ScionClient` entry point with bearer token, custom auth, project scoping
- `sdk/typescript/src/index.ts` — Barrel re-exports

### Agent Types
- `sdk/typescript/src/types/common.ts` — `StructuredMessage` interface
- `sdk/typescript/src/types/agents.ts` — `Agent`, `CreateAgentParams`, `CreateAgentResponse`, `ListAgentsParams`, `SendStructuredMessageOptions`, and related types
- `sdk/typescript/src/types/index.ts` — Type barrel

### Agents Resource
- `sdk/typescript/src/resources/agents.ts` — `AgentsResource` class with 12 methods:
  - `create`, `get`, `list` (paginated), `start`, `stop`, `suspend`, `restart`, `delete`, `restore`, `sendMessage`, `sendStructuredMessage`, `broadcastMessage`
  - Full JSDoc with examples
  - Project-scoped path support

### Tests
- `sdk/typescript/tests/agents.test.ts` — 29 unit tests covering:
  - Each method: correct URL path, HTTP method, request body, response parsing
  - Project-scoped vs. unscoped paths
  - Authentication (bearer token, custom auth provider)
  - Error cases (404, 401, 400, 500, non-JSON error bodies)
  - Pagination: async iteration across multiple pages

### Client Wiring
- `AgentsResource` wired into `ScionClient` as `client.agents`

## Design Decisions

1. **No legacy "grove" references** — All types and paths use "project" exclusively as instructed.
2. **Page as AsyncIterable** — `Page<T>` implements `AsyncIterable<T>` for `for await...of` iteration across all pages, matching modern TypeScript SDK patterns.
3. **Transport layer** — Thin abstraction over `fetch` with configurable auth, timeout via `AbortController`, and automatic error class mapping from HTTP status codes.
4. **Error hierarchy** — Specific error classes for common HTTP statuses (400, 401, 403, 404, 409) enable precise `catch` handling.

## Verification

- `tsc --noEmit` passes with zero errors
- `vitest run` — 29/29 tests pass

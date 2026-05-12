# TypeScript SDK Foundation (Steps 1-6)

**Date:** 2026-05-12
**Branch:** scion/dev-ts-foundation
**Author:** dev-ts-foundation agent

## Summary

Implemented the complete TypeScript SDK foundation at `sdk/typescript/`, covering design doc Steps 1-6. This produces a fully functional client stack that resource modules (agents, secrets, etc.) will build on in Phase B.

## What Was Built

### Step 1: Project Scaffolding
- `package.json` with `@scion/sdk` v0.1.0, Node 18+, ESM-first
- `tsconfig.json` with strict mode, ES2022 target, bundler resolution
- `tsup.config.ts` for dual ESM + CJS output with declaration files
- `vitest.config.ts` + test setup with MSW for mocking
- ESLint (strict `no-any`) + Prettier configuration

### Step 2: Transport Layer (`src/transport.ts`)
- `Transport` class wrapping native `fetch` (Node 18+)
- URL construction, JSON body serialization, auth header injection
- `User-Agent: scion-typescript-sdk/0.1.0`
- Timeout via `AbortSignal.timeout` (30s default)
- Retry with exponential backoff + jitter (3 retries on 5xx/network, no retry on 4xx)
- `request<T>()` for parsed JSON, `requestRaw()` for streaming

### Step 3: Error Handling (`src/errors.ts`)
- `ScionError` base with `status`, `code`, `message`, `requestId`, `details`
- Subclasses: `AuthenticationError` (401), `PermissionError` (403), `NotFoundError` (404), `ConflictError` (409), `ValidationError` (400), `RateLimitError` (429 + retryAfter), `ServerError` (5xx), `ConnectionError`, `StreamError`
- `parseErrorResponse()` factory from `Response` object
- `ErrorCode` constants matching Go `apiclient/errors.go`

### Step 4: Type Models (`src/types/`)
- TypeScript interfaces for Agent, Project, Secret, Message resources
- Common types: `HealthResponse`, `PageParams`, `PaginatedResponse<T>`
- All using camelCase (matching JSON wire format), no grove/groveId fields
- Derived from `pkg/hubclient/types.go`

### Step 5: Client Entrypoint (`src/client.ts`)
- `ScionClient` class with `hubUrl`, `token`, `timeout` options
- Token resolution chain: explicit -> `SCION_API_TOKEN` -> `SCION_DEV_TOKEN` -> `~/.scion/dev-token`
- `ScionClient.fromAgentEnv()` factory using `X-Scion-Agent-Token` header
- `health()` method
- Lazy-init resource stubs (agents, projects, secrets, messages)

### Step 6: Pagination Helpers (`src/pagination.ts`)
- `Page<T>` class with `data`, `hasNext`, `nextCursor`, `totalCount`
- Implements `AsyncIterable<T>` for transparent `for await...of` auto-pagination
- `getNextPage()` for manual page-by-page control
- Lazy fetching: next page only requested when current is exhausted

## Test Coverage

59 tests across 4 test files, all passing:
- `tests/errors.test.ts` — 23 tests
- `tests/transport.test.ts` — 13 tests (using MSW for HTTP mocking)
- `tests/client.test.ts` — 11 tests
- `tests/pagination.test.ts` — 12 tests

## Key Decisions

1. **No runtime dependencies** — uses native `fetch`, `AbortSignal.timeout`, and Node.js `fs`/`os`/`path` modules only
2. **Dual ESM + CJS** — via tsup, supporting both `import` and `require`
3. **Strict TypeScript** — `no-any` enforced via ESLint, all public APIs fully typed
4. **MSW for transport tests** — intercepts at the network level rather than mocking `fetch` directly, giving more realistic coverage
5. **project-only terminology** — no `grove` or `groveId` anywhere in the SDK types, per design doc Section 9
6. **Resource stubs** — lazily initialized placeholder objects for agents/projects/secrets/messages; Phase B will replace these with real service implementations

## Verification

All commands pass:
- `npm run build` — ESM + CJS + .d.ts generated
- `npm test` — 59/59 tests pass
- `npm run lint` — clean
- `npm run typecheck` — clean

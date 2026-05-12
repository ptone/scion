# TypeScript SDK: Secrets Resource (Step 10)

**Date**: 2026-05-12  
**Agent**: dev-ts-secrets  
**Branch**: scion/dev-ts-secrets  

## Summary

Implemented the Secrets resource module for the TypeScript SDK, including the SDK foundation (transport, base resource, client) since it did not yet exist on this branch.

## Deliverables

### 1. SDK Foundation (`sdk/typescript/src/`)
- **transport.ts** — HTTP transport with auth support (BearerAuth, AgentTokenAuth), error handling (ScionError), and all HTTP methods (GET, PUT, POST, DELETE, PATCH)
- **resource.ts** — BaseResource class with shared query-building helpers and `Page<T>` type
- **client.ts** — ScionClient entry point that wires resources to the transport
- **index.ts** — Barrel export for all public types and classes

### 2. Secrets Resource (`sdk/typescript/src/resources/secrets.ts`)
`SecretsResource` class with four methods matching the Go `SecretService` interface:
- `list(params?)` → `Page<Secret>` — GET `/api/v1/secrets` with scope/type query params
- `get(key, params?)` → `Secret` — GET `/api/v1/secrets/:key` with scope query params
- `set(key, params)` → `SetSecretResponse` — PUT `/api/v1/secrets/:key` with body
- `delete(key, params?)` → `void` — DELETE `/api/v1/secrets/:key` with scope query params

All methods URL-encode the key and have full JSDoc documentation.

### 3. Types (`sdk/typescript/src/types/secrets.ts`)
- `Secret` — metadata (id, key, type, scope, version, timestamps; no value)
- `ListSecretsParams` / `SecretScopeParams` — query options
- `SetSecretParams` — body for create/update (value, scope, description, injectionMode, type, target, allowProgeny)
- `SetSecretResponse` / `ListSecretsResponse` — response shapes

### 4. Tests (`sdk/typescript/tests/secrets.test.ts`)
19 unit tests covering:
- All four CRUD methods (list, get, set, delete)
- Scope and type query parameter passing
- URL encoding of special characters in keys
- Error handling (404 → ScionError)
- Authentication (Bearer token, agent token)
- Client wiring (secrets property exposure)

## Design Decisions

1. **Followed Go hubclient patterns** — URL paths, HTTP methods, query parameters, and body shapes all match the Go reference implementation exactly.
2. **Used `Page<T>` wrapper for list** — Wraps the API's `ListSecretsResponse` into a generic `Page<Secret>` to support future pagination.
3. **Mock fetch for testing** — Tests inject a mock `fetch` via `ScionClient` options rather than patching globals, keeping tests isolated and deterministic.
4. **Used "project" terminology** — No legacy "grove" references in the SDK.

## Verification

- TypeScript type-checking: `npx tsc --noEmit` passes
- Tests: `npx vitest run` — 19/19 pass

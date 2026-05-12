# TypeScript SDK: Resource Module Integration

**Date:** 2026-05-12
**Author:** dev-ts-integrate

## Summary

Integrated 4 TypeScript SDK resource modules (agents, messaging, projects, secrets) into the existing TypeScript SDK foundation on branch `scion/dev-ts-integrate`. Each resource was independently implemented on a separate branch with its own transport, client, and error infrastructure. This task extracted the resource-specific logic and adapted it to use the foundation's shared infrastructure.

## Source Branches

- `origin/scion/dev-ts-agents` — AgentsResource
- `origin/scion/dev-ts-messaging` — MessagesResource
- `origin/scion/dev-ts-projects` — ProjectsResource (also provided BaseResource pattern)
- `origin/scion/dev-ts-secrets` — SecretsResource

## Deliverables

### New Files Created

- `sdk/typescript/src/resources/base.ts` — BaseResource abstract class with shared transport access and query-building helpers
- `sdk/typescript/src/resources/agents.ts` — AgentsResource with 12 methods: create, get, list, start, stop, suspend, restart, delete, restore, sendMessage, sendStructuredMessage, broadcastMessage
- `sdk/typescript/src/resources/messages.ts` — MessagesResource with 4 methods: list, get, markRead, markAllRead
- `sdk/typescript/src/resources/projects.ts` — ProjectsResource with 6 methods: list, get, create, update, delete, listAgents
- `sdk/typescript/src/resources/secrets.ts` — SecretsResource with 4 methods: list, get, set, delete
- `sdk/typescript/src/resources/index.ts` — Barrel exports for all resources
- `sdk/typescript/tests/agents.test.ts` — 26 tests covering agents resource
- `sdk/typescript/tests/messages.test.ts` — 17 tests covering messages resource
- `sdk/typescript/tests/projects.test.ts` — 21 tests covering projects resource
- `sdk/typescript/tests/secrets.test.ts` — 15 tests covering secrets resource

### Modified Files

- `sdk/typescript/src/client.ts` — Replaced ResourceStub placeholders with real resource classes; added `projectId` option
- `sdk/typescript/src/index.ts` — Added re-exports for all resource classes, SecretsPage, and new types
- `sdk/typescript/src/transport.ts` — Fixed `request()` to handle 204 No Content responses
- `sdk/typescript/src/types/agents.ts` — Added CreateAgentRequest fields (projectId, template, task, etc.), SendStructuredMessageOptions, CreateAgentResponse.warnings
- `sdk/typescript/src/types/common.ts` — Added StructuredMessage type for inter-agent messaging
- `sdk/typescript/src/types/messages.ts` — Cleaned up types, removed incorrect StructuredMessage extension
- `sdk/typescript/src/types/projects.ts` — Added CreateProjectRequest optional fields, ListProjectAgentsOptions, enhanced filter options
- `sdk/typescript/src/types/secrets.ts` — Added ListSecretsOptions type
- `sdk/typescript/src/types/index.ts` — Updated barrel exports for all new types

## Key Adaptation Decisions

1. **Unified Transport API** — Each branch had its own transport with convenience methods (get, post, put, etc.). All resources now use the foundation's `Transport.request(method, path, options)` directly.

2. **Foundation's Page class** — All paginated endpoints use the foundation's `Page<T>` from `pagination.ts` with async iteration support, replacing branch-specific pagination implementations.

3. **Consistent error hierarchy** — All resources use the foundation's error classes (NotFoundError, ValidationError, etc.) instead of branch-specific error types.

4. **Test infrastructure** — All tests use `msw` (mock service worker) consistently, matching the foundation's test pattern, rather than the varied fetch-mocking approaches used by individual branches.

5. **204 No Content handling** — Fixed foundation's `Transport.request()` to gracefully handle 204 responses, which was needed for void-returning resource methods (delete, start, stop, etc.).

6. **BaseResource pattern** — Created a shared BaseResource with `buildQuery()` and `serializeLabels()` helpers, reducing duplication across resources.

## Verification

- `npm run build` — Clean build, produces ESM + CJS + declarations
- `npm run lint` — No lint errors
- `npm test` — 138/138 tests pass (59 foundation + 79 resource tests)

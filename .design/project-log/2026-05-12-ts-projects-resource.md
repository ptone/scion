# TypeScript SDK: Projects Resource (Step 9)

**Date:** 2026-05-12
**Agent:** dev-ts-projects
**Branch:** scion/dev-ts-projects

## Summary

Implemented the TypeScript SDK foundation and the Projects resource module. The SDK directory (`sdk/typescript/`) did not previously exist, so the full foundation was built as part of this task.

## What Was Built

### SDK Foundation
- **Transport layer** (`src/transport.ts`): Typed HTTP client with JSON serialization, Bearer token auth, timeout support, and structured error parsing. Accepts a custom `fetch` for testing.
- **Error handling** (`src/errors.ts`): `ScionAPIError` class with status-specific helpers (`isNotFound()`, `isUnauthorized()`, `isForbidden()`, `isConflict()`, `isRateLimited()`, `isServerError()`).
- **Base resource** (`src/resources/base.ts`): Abstract class providing shared transport access and pagination helpers used by all resource modules.
- **Client** (`src/client.ts`): `ScionClient` entry point that wires up resource modules as namespaced properties (e.g., `client.projects`).
- **Types** (`src/types/`): TypeScript interfaces for `Project`, `Agent`, `Page<T>`, `PageOptions`, `PageResult`, and all request parameter types.

### Projects Resource
- **`ProjectsResource`** (`src/resources/projects.ts`) with 6 methods:
  - `list(params?)` - List projects with filtering (visibility, gitRemote, brokerId, name, slug, labels) and pagination
  - `get(projectId)` - Get a single project by ID
  - `create(params)` - Create a new project
  - `update(projectId, params)` - Update project metadata (PATCH)
  - `delete(projectId)` - Delete a project
  - `listAgents(projectId, params?)` - List agents within a project with filtering and pagination

### Tests
- **27 unit tests** covering all CRUD operations, pagination, filter parameter passing, error handling (400/401/403/404/409/429/500), authentication header injection, and edge cases (empty lists, non-JSON error responses).

## Design Decisions

1. **No legacy grove support**: The brief specified using "project" exclusively. The TypeScript SDK does not include grove/groveId fallback fields — it targets the modern `/api/v1/projects` endpoints only.
2. **Injected fetch**: The transport accepts a custom `fetch` function, enabling test isolation without HTTP servers.
3. **Page<T> wrapper**: List methods return `Page<T>` with `data` and `page` fields rather than raw arrays, making pagination explicit and consistent.
4. **Label serialization**: Labels are serialized as comma-separated `key=value` pairs in a single query parameter, consistent with the Hub API's label filter support.

## API Endpoints Used

| Method | Endpoint | SDK Method |
|--------|----------|------------|
| GET    | `/api/v1/projects` | `projects.list()` |
| GET    | `/api/v1/projects/:id` | `projects.get()` |
| POST   | `/api/v1/projects` | `projects.create()` |
| PATCH  | `/api/v1/projects/:id` | `projects.update()` |
| DELETE | `/api/v1/projects/:id` | `projects.delete()` |
| GET    | `/api/v1/projects/:id/agents` | `projects.listAgents()` |

## Verification

- TypeScript compilation passes (`tsc --noEmit`)
- All 27 tests pass (`vitest run`)

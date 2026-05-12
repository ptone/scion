# Python SDK: Projects Resource Implementation

**Date:** 2026-05-12
**Agent:** dev-py-projects
**Task:** Step 9 — Implement Projects resource module for the Python SDK

## Summary

Implemented the `ProjectsResource` and `AsyncProjectsResource` classes for the Python SDK, providing full CRUD operations on projects and project-scoped agent listing.

## Files Created/Modified

### New Files
- `sdk/python/src/scion/resources/__init__.py` — Resource module package, exports `ProjectsResource` and `AsyncProjectsResource`
- `sdk/python/src/scion/resources/projects.py` — Full implementation of sync and async project operations
- `sdk/python/tests/test_projects.py` — 35 unit tests covering all methods

### Modified Files
- `sdk/python/src/scion/_client.py` — Wired `ProjectsResource` as `client.projects` property (lazy-initialized)
- `sdk/python/src/scion/_async_client.py` — Wired `AsyncProjectsResource` as `client.projects` property (lazy-initialized)

## API Methods Implemented

| Method | HTTP | Path | Description |
|--------|------|------|-------------|
| `list()` | GET | `/api/v1/projects` | List with filters + pagination |
| `get(id)` | GET | `/api/v1/projects/{id}` | Get single project |
| `create()` | POST | `/api/v1/projects` | Create project |
| `update(id)` | PATCH | `/api/v1/projects/{id}` | Update project |
| `delete(id)` | DELETE | `/api/v1/projects/{id}` | Delete project |
| `list_agents(id)` | GET | `/api/v1/projects/{id}/agents` | List agents in project |

## Design Decisions

1. **PATCH for update**: The Go reference uses `patch` for updates, so the SDK follows suit. This allows partial updates without requiring all fields.

2. **Lazy property initialization**: The `client.projects` property uses `hasattr` check to lazily create the resource instance on first access, avoiding unnecessary instantiation.

3. **Shared static helpers**: The `_build_list_params` and `_build_agent_list_params` methods are `@staticmethod` on `ProjectsResource` and reused by `AsyncProjectsResource` to avoid duplication.

4. **Pagination wiring**: Each `_fetch_*_page` method creates a closure that captures current params and wires it into `SyncPage.fetch_next` / `AsyncPage.fetch_next` for transparent pagination.

5. **Existing type reuse**: Used the existing `CreateProjectRequest`, `UpdateProjectRequest`, `Project`, and `Agent` Pydantic models from `scion.types` — no new types were needed.

## Test Coverage

- 35 tests total (25 sync + 10 async)
- All CRUD operations tested
- Pagination tested (manual and auto_paging_iter)
- Error cases: 404, 401, 500, 400 validation
- Filter parameters verified on request URLs
- Client property caching verified
- Full suite: 124 tests pass, ruff lint clean

## Observations

- The existing foundation (transport, pagination, types, errors) was well-designed and made this resource implementation straightforward.
- The `model_dump_api()` pattern on request types cleanly handles camelCase serialization with None exclusion.
- The `respx` library works well for mocking httpx transport in tests — both sync and async patterns are clean.

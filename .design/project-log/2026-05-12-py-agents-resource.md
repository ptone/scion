# Python SDK: Agents Resource Implementation (Step 7)

**Date:** 2026-05-12
**Author:** dev-py-agents
**Branch:** scion/manage-sdk

## Summary

Implemented the `AgentsResource` (sync) and `AsyncAgentsResource` (async) classes for the Python SDK, completing Step 7 of the SDK design plan.

## What Was Done

### New Files
- `sdk/python/src/scion/resources/__init__.py` — Resources package with exports
- `sdk/python/src/scion/resources/agents.py` — Sync and async agent resource classes
- `sdk/python/tests/test_agents.py` — 41 unit tests covering all methods

### Modified Files
- `sdk/python/src/scion/_client.py` — Added `agents` property to `ScionClient`
- `sdk/python/src/scion/_async_client.py` — Added `agents` property to `AsyncScionClient`
- `sdk/python/src/scion/__init__.py` — Exported `AgentsResource` and `AsyncAgentsResource`

### Methods Implemented
All 13 methods specified in the brief, mirroring the Go `AgentService` interface:

| Method | HTTP | Path |
|--------|------|------|
| `create` | POST | `/api/v1/agents` |
| `get` | GET | `/api/v1/agents/{id}` |
| `list` | GET | `/api/v1/agents` |
| `start` | POST | `/api/v1/agents/{id}/start` |
| `stop` | POST | `/api/v1/agents/{id}/stop` |
| `suspend` | POST | `/api/v1/agents/{id}/suspend` |
| `restart` | POST | `/api/v1/agents/{id}/restart` |
| `delete` | DELETE | `/api/v1/agents/{id}` |
| `restore` | POST | `/api/v1/agents/{id}/restore` |
| `send_message` | POST | `/api/v1/agents/{id}/message` |
| `send_structured_message` | POST | `/api/v1/agents/{id}/message` |
| `broadcast_message` | POST | `/api/v1/messages/broadcast` |

### Test Coverage
- 41 new tests (25 sync, 16 async)
- Tests cover: correct URL paths, HTTP methods, request body construction, response parsing, error mapping (404→NotFoundError, 409→ConflictError, 400→ValidationError), paginated list with auto-paging, optional parameter exclusion, and client property caching

## Design Decisions

1. **Lazy initialization**: The `agents` property on both clients uses lazy initialization with caching — the resource object is created on first access and reused thereafter.

2. **Keyword-only params for create**: The `create` method uses keyword-only arguments rather than accepting a `CreateAgentRequest` object directly. This provides a cleaner API for callers while still using the typed request model internally for serialization.

3. **Conditional body fields**: Boolean flags like `interrupt` and `notify` are only included in request bodies when `True`, matching the Go `omitempty` behavior.

4. **Existing type reuse**: No new types were needed in `types/agents.py` — the existing `Agent`, `CreateAgentRequest`, `CreateAgentResponse`, and `ListAgentsResponse` types covered all requirements.

5. **Pagination**: List uses cursor-based pagination via `SyncPage`/`AsyncPage`, wired with a `fetch_next` closure that preserves filter parameters across pages.

## Verification

- All 130 tests pass (41 new + 89 existing)
- No regressions in existing test suites
- Uses "project" terminology exclusively (no "grove" references)

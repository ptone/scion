# Python SDK Foundation v2

**Date**: 2026-05-12  
**Branch**: `scion/dev-py-foundation-v2`  
**Author**: dev-py-foundation-v2 agent

## Summary

Implemented the complete Python SDK foundation for the Scion Hub API, covering Steps 1-6 from the SDK design doc.

## What Was Built

### Step 1: Project Scaffolding (`sdk/python/`)
- `pyproject.toml` with PEP 621 metadata, hatchling build, Python 3.9+ target
- Dependencies: httpx, pydantic v2, typing-extensions
- Dev deps: pytest, pytest-asyncio, respx, ruff, coverage
- Test config, ruff config, `.gitignore`

### Step 2: Transport Layer (`_transport.py`)
- `Transport` (sync) and `AsyncTransport` (async) classes wrapping httpx
- URL construction, JSON serialization, auth header injection (Bearer token)
- User-Agent: `scion-python-sdk/0.1.0`
- Exponential backoff retry (3 retries on 5xx/network errors, never on 4xx)
- Default 30s timeout
- Error response parsing delegated to `_errors.py`

### Step 3: Error Handling (`_errors.py`)
- `ScionError` base with `status_code`, `code`, `message`, `request_id`, `details`
- Subclasses: `AuthenticationError` (401), `PermissionError` (403), `NotFoundError` (404), `ConflictError` (409), `ValidationError` (400), `RateLimitError` (429), `ServerError` (5xx), `ConnectionError`, `StreamError`
- `parse_error_response()` factory mirrors Go `ParseErrorResponse()`
- Error codes match Go API spec constants

### Step 4: Type Models (`types/`)
- All models use Pydantic v2 with camelCase alias support and `populate_by_name=True`
- `agents.py`: Agent, AgentConfig, DirectConnect, CreateAgentRequest/Response, UpdateAgentRequest, ListAgentsResponse
- `projects.py`: Project, ProjectProvider, CreateProjectRequest, UpdateProjectRequest, ListProjectsResponse
- `secrets.py`: Secret, SetSecretRequest/Response, ListSecretResponse
- `messages.py`: Message, StructuredMessage, AgentMessage
- `common.py`: HealthResponse, PaginationParams, ListMeta, base `_ScionModel`
- Request models have `model_dump_api()` for camelCase serialization excluding None fields

### Step 5: Client Entrypoint + Auth (`_client.py`, `_async_client.py`)
- `ScionClient` (sync) and `AsyncScionClient` (async)
- Token resolution: explicit param → `SCION_API_TOKEN` → `SCION_DEV_TOKEN` → `~/.scion/dev-token`
- `from_agent_env()` classmethod reads `SCION_HUB_URL` + `SCION_AGENT_TOKEN`
- `health()` method
- Context manager support (`with`/`async with`)
- Service property stubs ready for resource agents to fill

### Step 6: Pagination Helpers (`_pagination.py`)
- `SyncPage[T]` and `AsyncPage[T]` with `.data`, `.has_next`, `.next_cursor`
- `.auto_paging_iter()` yields items across all pages transparently
- `.get_next_page()` for manual pagination control

## Test Results

- **89 tests** across 5 test files, all passing
- Tests cover: error parsing, transport retry/auth, type serialization/deserialization, client auth resolution, pagination iteration
- Uses `respx` for httpx mocking
- Ruff lint clean

## Design Decisions

1. **No legacy grove fields**: Python SDK uses "project" exclusively — no backward compat shims needed since this is a new SDK
2. **Pydantic v2 over dataclasses**: Provides validation, serialization, alias mapping (camelCase ↔ snake_case) with minimal boilerplate
3. **TC003 rule suppressed**: Pydantic needs `datetime` at runtime, can't be in `TYPE_CHECKING` blocks
4. **`X | None` syntax**: Used modern union syntax with `from __future__ import annotations` for Python 3.9 compat
5. **Transport owns client lifecycle**: `_owns_client` flag ensures external httpx clients aren't closed by the SDK

## Files Created

```
sdk/python/
├── pyproject.toml
├── .gitignore
├── README.md
├── src/scion/
│   ├── __init__.py
│   ├── _async_client.py
│   ├── _client.py
│   ├── _errors.py
│   ├── _pagination.py
│   ├── _transport.py
│   └── types/
│       ├── __init__.py
│       ├── agents.py
│       ├── common.py
│       ├── messages.py
│       ├── projects.py
│       └── secrets.py
└── tests/
    ├── __init__.py
    ├── test_client.py
    ├── test_errors.py
    ├── test_pagination.py
    ├── test_transport.py
    └── test_types.py
```

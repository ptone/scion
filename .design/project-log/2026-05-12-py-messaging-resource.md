# Python SDK — Messaging Resource (Step 8)

**Date:** 2026-05-12
**Author:** dev-py-messaging
**Status:** Complete

## Summary

Implemented the `MessagesResource` (sync) and `AsyncMessagesResource` (async) for the Python SDK, providing inbox operations that mirror the Go `MessageService` in `pkg/hubclient/messages.go`.

## What was done

### New files
- `sdk/python/src/scion/resources/__init__.py` — Resources package init, exports both resource classes.
- `sdk/python/src/scion/resources/messages.py` — Full implementation of sync and async messaging resources.
- `sdk/python/tests/test_messages.py` — 26 unit tests covering all methods, pagination, error handling, model parsing, and client property caching.

### Modified files
- `sdk/python/src/scion/_client.py` — Wired `MessagesResource` as a lazy `client.messages` property on `ScionClient`.
- `sdk/python/src/scion/_async_client.py` — Wired `AsyncMessagesResource` as a lazy `client.messages` property on `AsyncScionClient`.
- `sdk/python/src/scion/types/messages.py` — Added missing `broadcasted` field to the `Message` model (matching the Go struct).

### API methods implemented
| Method | HTTP | Path |
|---|---|---|
| `list(...)` | `GET` | `/api/v1/messages?limit=&cursor=&unread=&agent=&project=&type=` |
| `get(id)` | `GET` | `/api/v1/messages/{id}` |
| `mark_read(id)` | `PUT` | `/api/v1/messages/{id}/read` |
| `mark_all_read()` | `PUT` | `/api/v1/messages/read-all` |

### Design decisions
- **Lazy property pattern:** `client.messages` creates the resource on first access and caches it, matching the stub pattern already in the client classes.
- **Pagination integration:** `list()` returns `SyncPage[Message]` / `AsyncPage[Message]` with `auto_paging_iter()` support for transparent multi-page iteration. The `_fetch_page` helper captures filter params so pagination preserves filters.
- **URL encoding:** Message IDs are URL-encoded via `urllib.parse.quote` to handle special characters safely.
- **Query param naming:** Uses `agent` and `project` as query param keys (matching Go `query.Set("agent", ...)` and `query.Set("project", ...)`), and `unread=true` for unread filtering (matching Go `query.Set("unread", "true")`).

## Verification
- All 26 new tests pass.
- Full suite of 115 tests passes with no regressions.
- Ruff lint passes cleanly on all changed files.

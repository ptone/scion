# Python SDK: SSE Streaming Support

**Date:** 2026-05-12  
**Author:** dev-py-streaming  
**Scope:** `sdk/python/` — streaming module (Step 11)

## Summary

Implemented Server-Sent Events (SSE) streaming support for the Python SDK, enabling real-time consumption of agent lifecycle events and cloud log entries via the Hub API's SSE endpoints.

## What Was Done

### New Files
- **`src/scion/_streaming.py`** — Core SSE implementation:
  - `parse_sse_line()` — Low-level SSE wire format parser handling `data:`, `event:`, `id:`, `retry:` fields, comment/heartbeat filtering, and multi-line data aggregation per the SSE specification.
  - `SyncSSEIterator[T]` — Synchronous iterator wrapping `httpx.Response.iter_lines()`, implementing both `Iterator` and context manager protocols.
  - `AsyncSSEIterator[T]` — Asynchronous iterator wrapping `httpx.Response.aiter_lines()`, implementing both `AsyncIterator` and async context manager protocols.
  - Both iterators support `last_event_id` tracking for reconnection scenarios.

- **`src/scion/types/streaming.py`** — Typed event models:
  - `StreamEvent` — Base model with `type`, `id`, and `raw_data` fields.
  - `AgentEvent` — Agent lifecycle events (status, phase, message, timestamp).
  - `LogEntry` — Structured log entries (severity, message, source, agent_id).

- **`tests/test_streaming.py`** — 60 unit tests covering:
  - SSE line parsing (16 tests): data fields, comments, event types, IDs, retry, edge cases
  - Event data parsing (9 tests): model validation, type precedence, multi-line, error handling
  - Sync iterator (14 tests): iteration, heartbeats, multi-line, context manager, close, errors
  - Async iterator (10 tests): same coverage as sync with async-specific patterns
  - Type models (6 tests): JSON parsing, extra fields, minimal fields
  - Full event sequences (5 tests): realistic streams, break-out, consecutive heartbeats

### Modified Files
- **`src/scion/_transport.py`** — Added `stream()` context managers to both `Transport` (sync) and `AsyncTransport` (async) for opening httpx streaming connections with error handling.
- **`src/scion/resources/agents.py`** — Added `stream_events()` and `stream_cloud_logs()` to both `AgentsResource` and `AsyncAgentsResource`.
- **`src/scion/__init__.py`**, **`src/scion/types/__init__.py`** — Exported new streaming types and iterators.

## Design Decisions

1. **Generic iterators** — `SyncSSEIterator[T]` and `AsyncSSEIterator[T]` are generic over any `_ScionModel` subclass, making it easy to add new SSE event types later.

2. **SSE spec compliance** — The parser follows the W3C SSE specification: single leading space stripping, multi-line data joining with newlines, null-byte rejection in event IDs, and unknown field ignoring.

3. **Context manager pattern** — Both iterators support context manager usage for clean teardown, matching the pattern users expect from streaming APIs.

4. **Transport-level streaming** — Added `stream()` to the transport layer rather than building directly on httpx, maintaining the existing abstraction boundary (auth headers, URL construction, error parsing).

5. **Graceful degradation** — Invalid JSON data events are logged and skipped rather than raising, so a single malformed event doesn't terminate the stream.

## API Surface

```python
# Sync
with client.agents.stream_events("agent-id") as stream:
    for event in stream:
        print(f"[{event.type}] {event.message}")
        if event.status == "completed":
            break

# Async
async with await client.agents.stream_events("agent-id") as stream:
    async for event in stream:
        print(f"[{event.type}] {event.message}")

# Cloud logs with severity filter
with client.agents.stream_cloud_logs("agent-id", severity="ERROR") as stream:
    for entry in stream:
        print(f"[{entry.severity}] {entry.message}")
```

## Test Results

All 280 tests pass (60 new streaming + 220 existing).
